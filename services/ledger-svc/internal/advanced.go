package internal

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type AdvancedService struct {
	db *sql.DB
}

func NewAdvancedService(db *sql.DB) *AdvancedService {
	return &AdvancedService{db: db}
}

func (s *AdvancedService) ConvertAmount(ctx context.Context, from, to string, amount int64) (int64, error) {
	var rate float64
	query := queryForDriver(s.db, "SELECT rate FROM fx_rates WHERE base_currency=? AND target_currency=?")
	err := s.db.QueryRowContext(ctx, query, from, to).Scan(&rate)
	if err != nil {
		if from == to { return amount, nil }
		return 0, fmt.Errorf("fx rate not found for %s/%s", from, to)
	}
	return int64(float64(amount) * rate), nil
}

// Webhook Queue
func (s *AdvancedService) QueueWebhook(ctx context.Context, eventType, payloadJSON string) error {
	query := queryForDriver(s.db, `INSERT INTO webhook_queue (event_type, payload, status, created_at) VALUES (?, ?, 'PENDING', ?)`)
	_, err := s.db.ExecContext(ctx, query, eventType, payloadJSON, time.Now().UTC())
	return err
}

func (s *AdvancedService) CreateDispute(ctx context.Context, refID, walletID string, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return err }
	defer tx.Rollback()

	// Find original entries
	query := queryForDriver(s.db, `SELECT entry_type, amount FROM ledger_entries WHERE reference_id=?`)
	rows, err := tx.QueryContext(ctx, query, refID)
	if err != nil { return err }
	defer rows.Close()

	var totalAmount int64
	for rows.Next() {
		var t string; var amt int64
		if err := rows.Scan(&t, &amt); err != nil { return err }
		if t == "DEBIT" { totalAmount += amt }
	}
	if totalAmount == 0 { return fmt.Errorf("no transactions found for ref: %s", refID) }

	query = queryForDriver(s.db, `INSERT INTO ledger_entries (id, wallet_id, entry_type, amount, reference_id, created_at) 
		 VALUES (?, ?, 'CREDIT', ?, 'DISPUTE_REVERSAL_' || ?, ?)`)
	_, err = tx.ExecContext(ctx, query, "disp-"+refID, walletID, totalAmount, refID, time.Now().UTC())
	if err != nil { return err }

	query = queryForDriver(s.db, `INSERT INTO event_log (aggregate_id, event_type, payload, created_at) VALUES (?, 'dispute.created', ?, ?)`)
	_, err = tx.ExecContext(ctx, query, walletID, fmt.Sprintf(`{"ref":"%s","reason":"%s","amount":%d}`, refID, reason, totalAmount), time.Now().UTC())
	
	return tx.Commit()
}

func (s *AdvancedService) GeneratePostMortem(ctx context.Context) (string, error) {
	var totalWallet, totalCred, totalDeb int64
	s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(balance),0), COALESCE(SUM(amount),0) FROM wallets, 
		(SELECT SUM(amount) FROM ledger_entries WHERE entry_type='CREDIT'),
		(SELECT SUM(amount) FROM ledger_entries WHERE entry_type='DEBIT')`).Scan(&totalWallet, &totalCred, &totalDeb)

	diff := totalCred - totalDeb - totalWallet
	status := "BALANCED"
	if diff != 0 { status = "DISCREPANCY" }

	var eventCount int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_log").Scan(&eventCount)

	report := fmt.Sprintf(`# Incident Post-Mortem
Generated: %s
Status: %s

## Ledger Summary
- Total Wallet Balance: %d
- Total Credits: %d
- Total Debits: %d
- Discrepancy: %d

## System State
- Total Events Logged: %d
- Idempotency Keys: Check /idempotency/keys endpoint

## Remediation Steps
1. Verify idempotency cache consistency
2. Check event_log for missing entries
3. Run reconciliation script
`, time.Now().UTC().Format(time.RFC3339), status, totalWallet, totalCred, totalDeb, diff, eventCount)

	return report, nil
}