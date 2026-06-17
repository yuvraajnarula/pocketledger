package internal

import (
	"context"
	"database/sql"
	"time"

	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

type EventRepo struct {
	db *sql.DB
}

func NewEventRepo(db *sql.DB) *EventRepo {
	return &EventRepo{db: db}
}

func (r *EventRepo) Append(ctx context.Context, tx *sql.Tx, event domain.Event) error {
	query := queryForDriver(r.db, "INSERT INTO event_log (aggregate_id, event_type, payload, created_at) VALUES (?,?,?,?)")
	_, err := tx.ExecContext(ctx, query, event.AggregateID, event.EventType, event.Payload, time.Now().UTC())
	return err
}

func (r *EventRepo) GetBalanceAt(ctx context.Context, walletID string, until time.Time) (int64, error) {
	var balance int64
	query := queryForDriver(r.db, `
		SELECT entry_type, amount FROM ledger_entries
		WHERE wallet_id = ? AND created_at <= ? ORDER BY created_at ASC`)
	rows, err := r.db.QueryContext(ctx, query, walletID, until)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	for rows.Next() {
		var t string
		var amt int64
		if err := rows.Scan(&t, &amt); err != nil {
			return 0, err
		}
		if t == "CREDIT" {
			balance += amt
		} else {
			balance -= amt
		}
	}
	return balance, nil
}