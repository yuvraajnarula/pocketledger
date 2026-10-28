package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/yuvraajnarula/pocketledger/services/ledger-svc/internal/walletsvc"
	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

// bankWalletID is the internal source-of-funds wallet managed solely by ledger-svc.
var bankWalletID = func() string {
	if v := os.Getenv("BANK_WALLET_ID"); v != "" {
		return v
	}
	return "bank-main"
}()

// writeTxOpts makes SQLite use BEGIN IMMEDIATE so the write lock is claimed
// at transaction start rather than on first write. This eliminates the
// deferred-to-immediate upgrade race that caused SQLITE_BUSY (262) under
// concurrent load even when busy_timeout was set.
var writeTxOpts = &sql.TxOptions{Isolation: sql.LevelSerializable}

type TransferService struct {
	DB           *sql.DB
	LedgerRepo   *LedgerRepo
	WalletRepo   *WalletRepo
	WalletClient *walletsvc.Client
	eventRepo    *EventRepo // FIX: owned instance, not re-created per call
}

func NewTransferService(db *sql.DB, wr *WalletRepo, lr *LedgerRepo) *TransferService {
	return &TransferService{
		DB:         db,
		LedgerRepo: lr,
		WalletRepo: wr,
		eventRepo:  NewEventRepo(db), // FIX: created once here
	}
}

// WithWalletClient sets the wallet-svc HTTP client.
func (s *TransferService) WithWalletClient(c *walletsvc.Client) *TransferService {
	s.WalletClient = c
	return s
}

func (s *TransferService) Execute(ctx context.Context, srcID, destID string, amount int64, refID string) error {
	if amount <= 0 {
		return domain.ErrInvalidAmount
	}

	if srcID == bankWalletID {
		return s.executeBankDeposit(ctx, srcID, destID, amount, refID)
	}

	if s.WalletClient != nil {
		return s.executeWithClient(ctx, srcID, destID, amount, refID)
	}
	return s.executeLocal(ctx, srcID, destID, amount, refID)
}

// ExecuteFX performs a cross-currency transfer entirely within the ledger DB.
// The source wallet is debited `amount` and the destination wallet is credited
// the FX-converted amount, so the ledger stays balanced by construction.
// Returns (srcAmount, dstAmount).
func (s *TransferService) ExecuteFX(ctx context.Context, srcID, destID string, amount int64, refID string) (int64, int64, error) {
	if amount <= 0 {
		return 0, 0, domain.ErrInvalidAmount
	}
	if srcID == destID {
		return 0, 0, fmt.Errorf("source and destination wallets must differ")
	}
	if srcID == bankWalletID {
		return 0, 0, fmt.Errorf("bank wallet transfers must use the plain transfer endpoint")
	}

	fx := NewAdvancedService(s.DB)

	tx, err := s.DB.BeginTx(ctx, writeTxOpts)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	var srcBal, srcVersion int64
	var srcCur string
	query := queryForDriver(s.DB, "SELECT balance, version, currency FROM wallets WHERE id = ?")
	err = tx.QueryRowContext(ctx, query, srcID).Scan(&srcBal, &srcVersion, &srcCur)
	if err == sql.ErrNoRows {
		return 0, 0, domain.ErrWalletNotFound
	}
	if err != nil {
		return 0, 0, err
	}
	if srcBal < amount {
		return 0, 0, domain.ErrInsufficientFunds
	}

	var dstVersion int64
	var dstCur string
	query = queryForDriver(s.DB, "SELECT version, currency FROM wallets WHERE id = ?")
	err = tx.QueryRowContext(ctx, query, destID).Scan(&dstVersion, &dstCur)
	if err == sql.ErrNoRows {
		return 0, 0, domain.ErrWalletNotFound
	}
	if err != nil {
		return 0, 0, err
	}

	// Resolve the FX rate now that both currencies are known. Read-only query,
	// safe to run against the DB handle inside the write tx.
	dstAmount, err := fx.ConvertAmount(ctx, srcCur, dstCur, amount)
	if err != nil {
		return 0, 0, err
	}
	if dstAmount <= 0 {
		return 0, 0, domain.ErrInvalidAmount
	}

	if err := ValidateDailyWithdrawal(ctx, s.DB, srcID, amount); err != nil {
		return 0, 0, err
	}

	query = queryForDriver(s.DB, "UPDATE wallets SET balance = balance - ?, version = version + 1, updated_at = "+currentTimestampExpr(s.DB)+" WHERE id = ? AND version = ?")
	res, err := tx.ExecContext(ctx, query, amount, srcID, srcVersion)
	if err != nil {
		return 0, 0, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return 0, 0, domain.ErrOptimisticLockFail
	}

	query = queryForDriver(s.DB, "UPDATE wallets SET balance = balance + ?, version = version + 1, updated_at = "+currentTimestampExpr(s.DB)+" WHERE id = ? AND version = ?")
	res, err = tx.ExecContext(ctx, query, dstAmount, destID, dstVersion)
	if err != nil {
		return 0, 0, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return 0, 0, domain.ErrOptimisticLockFail
	}

	if refID == "" {
		refID = fmt.Sprintf("auto:fx:%s:%s:%d", srcID, destID, amount)
	}

	entries := []domain.LedgerEntry{
		{WalletID: srcID, EntryType: "DEBIT", Amount: amount, ReferenceID: refID},
		{WalletID: destID, EntryType: "CREDIT", Amount: dstAmount, ReferenceID: refID},
	}
	if err := s.LedgerRepo.InsertEntries(ctx, tx, entries); err != nil {
		return 0, 0, err
	}

	payload, _ := json.Marshal(map[string]any{
		"from": srcID, "to": destID, "amount": amount, "dest_amount": dstAmount,
		"reference_id": refID, "currencies": []string{srcCur, dstCur},
	})
	if err := s.eventRepo.Append(ctx, tx, domain.Event{
		AggregateID: srcID,
		EventType:   "wallet.transfer.fx",
		Payload:     string(payload),
	}); err != nil {
		return 0, 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return amount, dstAmount, nil
}

func (s *TransferService) executeBankDeposit(ctx context.Context, srcID, destID string, amount int64, refID string) error {
	tx, err := s.DB.BeginTx(ctx, writeTxOpts)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Load and lock bank wallet from ledger DB
	var srcBal, srcVersion int64
	query := queryForDriver(s.DB, "SELECT balance, version FROM wallets WHERE id = ?")
	err = tx.QueryRowContext(ctx, query, srcID).Scan(&srcBal, &srcVersion)
	if err == sql.ErrNoRows {
		return domain.ErrWalletNotFound
	}
	if err != nil {
		return err
	}
	if srcBal < amount {
		return domain.ErrInsufficientFunds
	}

	// 2. Debit bank wallet in ledger DB
	query = queryForDriver(s.DB, "UPDATE wallets SET balance = balance - ?, version = version + 1, updated_at = "+currentTimestampExpr(s.DB)+" WHERE id = ? AND version = ?")
	res, err := tx.ExecContext(ctx, query, amount, srcID, srcVersion)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.ErrOptimisticLockFail
	}

	if s.WalletClient != nil {
		// Verify destination exists in wallet-svc first
		if _, err := s.WalletClient.GetWallet(ctx, destID); err != nil {
			return err
		}
		// Write ledger entries before committing so they're in the same tx
		if err := s.recordLedger(ctx, tx, srcID, destID, srcBal, amount, refID); err != nil {
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}

		// Now deposit into wallet-svc (outside the ledger tx — wallet-svc is
		// the source of truth for user balances)
		if err := s.WalletClient.Deposit(ctx, destID, amount, refID); err != nil {
			slog.Error("bank deposit: wallet-svc deposit failed after ledger commit — needs reconciliation",
				"dest", destID, "amount", amount, "ref", refID, "error", err)

			// Mark the committed ledger entries as unreconciled for later retry.
			// Use a background context because the request context may be cancelled.
			if markErr := s.LedgerRepo.MarkUnreconciled(context.Background(), refID); markErr != nil {
				slog.Error("bank deposit: failed to mark entries as unreconciled",
					"ref", refID, "error", markErr)
			}

			return fmt.Errorf("bank deposit: wallet-svc credit failed: %w", err)
		}
		return nil
	}

	// Local path: destination must also be in ledger DB
	var destVersion int64
	query = queryForDriver(s.DB, "SELECT version FROM wallets WHERE id = ?")
	err = tx.QueryRowContext(ctx, query, destID).Scan(&destVersion)
	if err == sql.ErrNoRows {
		return domain.ErrWalletNotFound
	}
	if err != nil {
		return err
	}
	query = queryForDriver(s.DB, "UPDATE wallets SET balance = balance + ?, version = version + 1, updated_at = "+currentTimestampExpr(s.DB)+" WHERE id = ? AND version = ?")
	res, err = tx.ExecContext(ctx, query, amount, destID, destVersion)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.ErrOptimisticLockFail
	}

	if err := s.recordLedger(ctx, tx, srcID, destID, srcBal, amount, refID); err != nil {
		return err
	}
	return tx.Commit()
}

// executeWithClient is the standard path for user→user transfers via wallet-svc.
func (s *TransferService) executeWithClient(ctx context.Context, srcID, destID string, amount int64, refID string) error {
	src, err := s.WalletClient.GetWallet(ctx, srcID)
	if err != nil {
		return err
	}
	if src.Balance < amount {
		return domain.ErrInsufficientFunds
	}
	dest, err := s.WalletClient.GetWallet(ctx, destID)
	if err != nil {
		return err
	}
	if dest.Currency != src.Currency {
		return domain.ErrCurrencyMismatch
	}

	if err := ValidateDailyWithdrawal(ctx, s.DB, srcID, amount); err != nil {
		return err
	}
	if err := s.WalletClient.Withdraw(ctx, srcID, amount, refID); err != nil {
		return err
	}

	if err := s.WalletClient.Deposit(ctx, destID, amount, refID); err != nil {
		slog.Error("transfer: dest deposit failed, reversing src withdraw",
			"src", srcID, "dest", destID, "amount", amount, "error", err)

		reverseRefID := "reverse:" + refID
		if reverseErr := s.WalletClient.Deposit(ctx, srcID, amount, reverseRefID); reverseErr != nil {
			slog.Error("transfer: REVERSAL FAILED — manual intervention required",
				"src", srcID, "amount", amount, "reverse_error", reverseErr)
		}
		return fmt.Errorf("transfer: dest deposit failed (src reversed): %w", err)
	}

	return s.recordLedger(ctx, nil, srcID, destID, src.Balance, amount, refID)
}

// executeLocal performs the transfer entirely within the local DB.
func (s *TransferService) executeLocal(ctx context.Context, srcID, destID string, amount int64, refID string) error {
	// FIX: writeTxOpts → BEGIN IMMEDIATE.
	tx, err := s.DB.BeginTx(ctx, writeTxOpts)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var srcBal, srcVersion int64
	var srcCur string
	query := queryForDriver(s.DB, "SELECT balance, version, currency FROM wallets WHERE id = ?")
	err = tx.QueryRowContext(ctx, query, srcID).Scan(&srcBal, &srcVersion, &srcCur)
	if err == sql.ErrNoRows {
		return domain.ErrWalletNotFound
	}
	if err != nil {
		return err
	}
	if srcBal < amount {
		return domain.ErrInsufficientFunds
	}

	var destVersion int64
	var destCur string
	query = queryForDriver(s.DB, "SELECT version, currency FROM wallets WHERE id = ?")
	err = tx.QueryRowContext(ctx, query, destID).Scan(&destVersion, &destCur)
	if err == sql.ErrNoRows {
		return domain.ErrWalletNotFound
	}
	if err != nil {
		return err
	}
	if destCur != srcCur {
		return domain.ErrCurrencyMismatch
	}

	if err := ValidateDailyWithdrawal(ctx, s.DB, srcID, amount); err != nil {
		return err
	}

	query = queryForDriver(s.DB, "UPDATE wallets SET balance = balance - ?, version = version + 1, updated_at = "+currentTimestampExpr(s.DB)+" WHERE id = ? AND version = ?")
	res, err := tx.ExecContext(ctx, query, amount, srcID, srcVersion)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.ErrOptimisticLockFail
	}

	query = queryForDriver(s.DB, "UPDATE wallets SET balance = balance + ?, version = version + 1, updated_at = "+currentTimestampExpr(s.DB)+" WHERE id = ? AND version = ?")
	res, err = tx.ExecContext(ctx, query, amount, destID, destVersion)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.ErrOptimisticLockFail
	}

	if err := s.recordLedger(ctx, tx, srcID, destID, srcBal, amount, refID); err != nil {
		return err
	}

	return tx.Commit()
}
func (s *TransferService) recordLedger(ctx context.Context, tx *sql.Tx, srcID, destID string, srcBal, amount int64, refID string) error {

	if refID == "" {
		refID = fmt.Sprintf("auto:%s:%s:%d", srcID, destID, amount)
	}

	commit := false
	if tx == nil {
		var err error
		tx, err = s.DB.BeginTx(ctx, writeTxOpts)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		commit = true
	}

	entries, err := domain.ComputeTransfer(srcID, destID, srcBal, amount)
	if err != nil {
		return err
	}
	// Tag entries with the reference so /admin/dispute and MarkReconciled can
	// look them up later (ComputeTransfer doesn't know the ref).
	for i := range entries {
		if entries[i].ReferenceID == "" {
			entries[i].ReferenceID = refID
		}
	}
	if len(entries) == 0 {
		return fmt.Errorf("recordLedger: ComputeTransfer returned no entries for src=%s dest=%s", srcID, destID)
	}
	if err := s.LedgerRepo.InsertEntries(ctx, tx, entries); err != nil {
		return err
	}

	payload, _ := json.Marshal(map[string]any{
		"from": srcID, "to": destID, "amount": amount, "reference_id": refID,
	})
	if err := s.eventRepo.Append(ctx, tx, domain.Event{
		AggregateID: srcID,
		EventType:   "wallet.transfer",
		Payload:     string(payload),
	}); err != nil {
		return err
	}

	if commit {
		return tx.Commit()
	}
	return nil
}
