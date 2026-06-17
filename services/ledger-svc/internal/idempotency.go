package internal

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

type IdempotencyStore struct{ db *sql.DB }

func NewIdempotencyStore(db *sql.DB) *IdempotencyStore {
	return &IdempotencyStore{db: db}
}

// CheckOrInsert atomically inserts a new PENDING key or returns the status of
// an existing one. The INSERT and SELECT are wrapped in a single transaction
// to close the TOCTOU race in the original two-statement version, where two
// concurrent requests with the same key could both observe RowsAffected==0
// before either had committed.
func (s *IdempotencyStore) CheckOrInsert(ctx context.Context, key string) (string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", fmt.Errorf("idempotency begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
    `INSERT INTO idempotency_keys (key, status, expires_at)
     VALUES ($1, 'PENDING', NOW() + INTERVAL '24 hours')
     ON CONFLICT (key) DO NOTHING`, key)
	if err != nil {
		return "", fmt.Errorf("idempotency insert: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("idempotency rows affected: %w", err)
	}

	if rows == 0 {
		// Key already exists — read its current status inside the same tx.
		var status string
		if err := tx.QueryRowContext(ctx,
			"SELECT status FROM idempotency_keys WHERE key = $1", key).Scan(&status); err != nil {
			return "", fmt.Errorf("idempotency status check: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return status, nil
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "NEW", nil
}

func (s *IdempotencyStore) CompleteKey(ctx context.Context, key string, code int, body []byte) error {
	cacheCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := s.db.ExecContext(cacheCtx,
		`UPDATE idempotency_keys 
		SET status = 'COMPLETED', response_code = $1, response_body = $2
		WHERE key = $3 AND status = 'PENDING'`, code, string(body), key)

	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		slog.Warn("idempotency key already updated or missing", "key", key)
	}
	return nil
}

func (s *IdempotencyStore) FailKey(ctx context.Context, key string) error {
	cacheCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(cacheCtx,
		`DELETE FROM idempotency_keys WHERE key = $1 AND status = 'PENDING'`, key)
	return err
}

func (s *IdempotencyStore) GetCachedResponse(ctx context.Context, key string) (int, []byte, error) {
	var code int
	var body sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT response_code, response_body FROM idempotency_keys
		WHERE key = $1 AND status = 'COMPLETED'`, key).Scan(&code, &body)

	if err == sql.ErrNoRows {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("get cached: %w", err)
	}
	var b []byte
	if body.Valid {
		b = []byte(body.String)
	}
	return code, b, nil
}

type KeyInfo struct {
	Key       string `json:"key"`
	Status    string `json:"status"`
	ReqHash   string `json:"request_hash"`
	RespCode  int    `json:"response_code"`
	RespBody  string `json:"response_body"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

func (s *IdempotencyStore) ListKeys(ctx context.Context, limit int) ([]KeyInfo, error) {
	query := queryForDriver(s.db, `SELECT key, status, request_hash, response_code, response_body, created_at, expires_at
		 FROM idempotency_keys ORDER BY created_at DESC LIMIT ?`)
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []KeyInfo
	for rows.Next() {
		var k KeyInfo
		rows.Scan(&k.Key, &k.Status, &k.ReqHash, &k.RespCode, &k.RespBody, &k.CreatedAt, &k.ExpiresAt)
		keys = append(keys, k)
	}
	return keys, nil
}