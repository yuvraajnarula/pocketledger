package internal

import (
	"context"
	"database/sql"
	"time"

	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

type WalletRepo struct {
	db *sql.DB
}

func NewWalletRepo(db *sql.DB) *WalletRepo {
	return &WalletRepo{db: db}
}

func (r *WalletRepo) Create(ctx context.Context, tx *sql.Tx, wallet *domain.Wallet) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO wallets (id, currency, balance, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		wallet.ID, wallet.Currency, wallet.Balance, wallet.Version, time.Now().UTC(), time.Now().UTC(),
	)
	return err
}

func (r *WalletRepo) Get(ctx context.Context, tx *sql.Tx, id string) (*domain.Wallet, error) {
	w := &domain.Wallet{}
	// If no tx, use db directly
	row := dbOrTx(ctx, r.db, tx).QueryRowContext(ctx,
		"SELECT id, currency, balance, version, created_at, updated_at FROM wallets WHERE id = ?", id)
	err := row.Scan(&w.ID, &w.Currency, &w.Balance, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// dbOrTx is a helper to execute on either *sql.DB or *sql.Tx
func dbOrTx(ctx context.Context, db *sql.DB, tx *sql.Tx) executer {
	if tx != nil {
		return tx
	}
	return db
}

type executer interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

// List returns the most recent wallets, oldest first, capped at limit.
func (r *WalletRepo) List(ctx context.Context, limit int) ([]*domain.Wallet, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, currency, balance, version, created_at, updated_at FROM wallets ORDER BY created_at DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.Wallet
	for rows.Next() {
		w := &domain.Wallet{}
		if err := rows.Scan(&w.ID, &w.Currency, &w.Balance, &w.Version, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *WalletRepo) IdempotentCredit(ctx context.Context, tx *sql.Tx, walletID, refID string, amount int64) (bool, error) {
	var already bool
	err := tx.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM idempotency_keys WHERE key = ?)", refID,
	).Scan(&already)

	if err != nil {
		return false, err
	}
	if already {
		return false, nil // idempotent no-op
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE wallets SET balance = balance + ?, version = version + 1, updated_at = ?
         WHERE id = ?`,
		amount, time.Now().UTC(), walletID,
	)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return false, domain.ErrWalletNotFound
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO idempotency_keys (key, status, request_hash, response_code, response_body, created_at, expires_at)
         VALUES (?, 'completed', ?, 200, '{"status":"credited"}', ?, ?)`,
		refID, refID, time.Now().UTC(), time.Now().Add(24*time.Hour),
	)
	if err != nil {
		return false, err
	}

	return true, nil
}
