package internal

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type RateLimitRule struct {
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

type PolicyConfig struct {
	MaxWithdrawalPerDay int64 `yaml:"max_withdrawal_per_day"`
	MinTransferAmount   int64 `yaml:"min_transfer_amount"`
	RequireMemoAbove    int64 `yaml:"require_memo_above"`
	EnableFXFees        bool  `yaml:"enable_fx_fees"`

	RateLimits struct {
		Default   RateLimitRule            `yaml:"default"`
		PerWallet map[string]RateLimitRule `yaml:"per_wallet"`
	} `yaml:"rate_limits"`
}

var (
	policy   PolicyConfig
	policyMu sync.RWMutex
)

func LoadPolicy(path string) (*PolicyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy file: %w", err)
	}

	var p PolicyConfig

	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}

	policyMu.Lock()
	policy = p
	policyMu.Unlock()

	return &p, nil
}

func ValidatePolicy(amount int64, hasMemo bool) error {
	policyMu.RLock()
	defer policyMu.RUnlock()

	if amount < policy.MinTransferAmount {
		return fmt.Errorf(
			"amount below minimum policy threshold: %d",
			policy.MinTransferAmount,
		)
	}

	if policy.RequireMemoAbove > 0 && amount > policy.RequireMemoAbove && !hasMemo {
		return fmt.Errorf(
			"memo required for amounts above %d",
			policy.RequireMemoAbove,
		)
	}

	return nil
}

func ValidateDailyWithdrawal(
	ctx context.Context,
	db *sql.DB,
	walletID string,
	amount int64,
) error {
	policyMu.RLock()
	cap := policy.MaxWithdrawalPerDay
	policyMu.RUnlock()

	if cap <= 0 {
		return nil
	}

	startOfDay := time.Now().UTC().Truncate(24 * time.Hour)

	var totalToday int64

	query := queryForDriver(db, `
		SELECT COALESCE(SUM(amount), 0)
		FROM ledger_entries
		WHERE wallet_id = ?
		  AND entry_type = 'DEBIT'
		  AND created_at >= ?
	`)
	err := db.QueryRowContext(ctx, query, walletID, startOfDay).Scan(&totalToday)

	if err != nil {
		return fmt.Errorf(
			"policy: daily withdrawal check failed: %w",
			err,
		)
	}

	if totalToday+amount > cap {
		return fmt.Errorf(
			"daily withdrawal limit exceeded: used %d, cap %d",
			totalToday,
			cap,
		)
	}

	return nil
}
