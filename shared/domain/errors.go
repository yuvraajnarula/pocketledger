package domain

import "errors"

var (
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrInvalidAmount       = errors.New("amount must be greater than zero")
	ErrInvalidCurrency     = errors.New("invalid currency code (ISO 4217)")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrIdempotencyConflict = errors.New("request in progress or already completed")
	ErrOptimisticLockFail  = errors.New("concurrent modification detected")
)
