package internal

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

type LedgerRepo struct {
	db *sql.DB
}

func NewLedgerRepo(db *sql.DB) *LedgerRepo {
	return &LedgerRepo{db: db}
}

func (r *LedgerRepo) InsertEntries(ctx context.Context, tx *sql.Tx, entries []domain.LedgerEntry) error {
	for _, e := range entries {
		id := uuid.New().String()
		_, err := tx.ExecContext(ctx,
			queryForDriver(r.db, `INSERT INTO ledger_entries (id, wallet_id, entry_type, amount, reference_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`),
			id, e.WalletID, e.EntryType, e.Amount, e.ReferenceID, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *LedgerRepo) GetUnreconciledDeposits(ctx context.Context) ([]UnreconciledEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT le.wallet_id, le.amount, le.reference_id, le.created_at
		FROM ledger_entries le
		WHERE le.entry_type = 'CREDIT'
		  AND le.reconciled = false
		ORDER BY le.created_at ASC
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []UnreconciledEntry
	for rows.Next() {
		var e UnreconciledEntry
		if err := rows.Scan(&e.WalletID, &e.Amount, &e.ReferenceID, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// MarkReconciled sets reconciled=true for all entries with the given reference_id.
func (r *LedgerRepo) MarkReconciled(ctx context.Context, refID string) error {
	_, err := r.db.ExecContext(ctx,
		queryForDriver(r.db, `
		UPDATE ledger_entries
		SET reconciled = true, reconciled_at = NOW()
		WHERE reference_id = ?`), refID)
	return err
}

// MarkUnreconciled sets reconciled=false for all entries with the given reference_id.
func (r *LedgerRepo) MarkUnreconciled(ctx context.Context, refID string) error {
	_, err := r.db.ExecContext(ctx,
		queryForDriver(r.db, `
		UPDATE ledger_entries
		SET reconciled = false
		WHERE reference_id = ?`), refID)
	return err
}

// UnreconciledEntry is a deposit that needs retrying.
type UnreconciledEntry struct {
	WalletID    string
	Amount      int64
	ReferenceID string
	CreatedAt   time.Time
}
