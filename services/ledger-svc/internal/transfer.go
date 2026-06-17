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
	if _, err := s.WalletClient.GetWallet(ctx, destID); err != nil {
		return err
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

	var destVersion int64
	query = queryForDriver(s.DB, "SELECT version FROM wallets WHERE id = ?")
	err = tx.QueryRowContext(ctx, query, destID).Scan(&destVersion)
	if err == sql.ErrNoRows {
		return domain.ErrWalletNotFound
	}
	if err != nil {
		return err
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
