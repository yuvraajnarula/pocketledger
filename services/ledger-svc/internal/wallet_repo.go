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
	query := queryForDriver(r.db, `INSERT INTO wallets (id, currency, balance, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	_, err := tx.ExecContext(ctx, query, wallet.ID, wallet.Currency, wallet.Balance, wallet.Version, time.Now().UTC(), time.Now().UTC())
	return err
}

func (r *WalletRepo) Get(ctx context.Context, tx *sql.Tx, id string) (*domain.Wallet, error) {
	w := &domain.Wallet{}
	query := queryForDriver(r.db, "SELECT id, currency, balance, version, created_at, updated_at FROM wallets WHERE id = ?")
	row := dbOrTx(ctx, r.db, tx).QueryRowContext(ctx, query, id)
	err := row.Scan(&w.ID, &w.Currency, &w.Balance, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// dbOrTx executes on either *sql.Tx (if provided) or falls back to *sql.DB.
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
