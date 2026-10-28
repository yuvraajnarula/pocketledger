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
	rows, err := r.db.QueryContext(ctx,
		queryForDriver(r.db, `
		SELECT wallet_id, amount, reference_id, created_at, COALESCE(reconcile_attempts, 0)
		FROM ledger_entries
		WHERE entry_type = 'CREDIT'
		  AND reconciled = false
		  AND COALESCE(reconcile_attempts, 0) < ?
		ORDER BY created_at ASC
		LIMIT 50`), maxReconcileAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []UnreconciledEntry
	for rows.Next() {
		var e UnreconciledEntry
		if err := rows.Scan(&e.WalletID, &e.Amount, &e.ReferenceID, &e.CreatedAt, &e.Attempts); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// IncReconcileAttempts bumps the retry counter for a deposit that failed again.
func (r *LedgerRepo) IncReconcileAttempts(ctx context.Context, refID string) error {
	_, err := r.db.ExecContext(ctx,
		queryForDriver(r.db, `
		UPDATE ledger_entries
		SET reconcile_attempts = COALESCE(reconcile_attempts, 0) + 1
		WHERE reference_id = ?`), refID)
	return err
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

// UnreconciledEntry is a deposit that needs retrying. Attempts counts how many
// reconciler passes have already failed for it (dead-letter cap input).
type UnreconciledEntry struct {
	WalletID    string
	Amount      int64
	ReferenceID string
	CreatedAt   time.Time
	Attempts    int
}
