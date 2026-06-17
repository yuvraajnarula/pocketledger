package domain

import "time"

//wallet schema
type  Wallet struct {
	ID string `json:"id"`
	Currency string `json:"currency"`
	Balance int64 `json:"balance"`
	Version int64 `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}	


// ledger entry schema
type LedgerEntry struct {
	ID string `json:"id"`
	WalletID string `json:"wallet_id"`
	EntryType string `json:"entry_type"`
	ReferenceID string `json:"reference_id,omitempty"` // will not appear in the result json if it holds 0 val
	Amount int64 `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Event Schema
type Event struct {
	AggregateID string `json:"aggregate_id"`
	EventType string `json:"event_type"`
	Payload string `json:"payload"`
	Timestamp time.Time `json:"timestamp"`
}