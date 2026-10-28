package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"golang.org/x/time/rate"
)

// Request context keys.
//
// These are used by auth/tenant middleware to attach tenant and API key identity
// to the request without weaving that concern into handlers.

const (
	ctxKeyTenantID  = "tenant_id"
	ctxKeyAPIKey    = "api_key"
	ctxKeyAPIKeyRow = "api_key_row"
	ctxKeyWalletID  = "wallet_id"
)

// wallet schema
type Wallet struct {
	ID        string    `json:"id"`
	Currency  string    `json:"currency"`
	Balance   int64     `json:"balance"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ledger entry schema
type LedgerEntry struct {
	ID          string    `json:"id"`
	WalletID    string    `json:"wallet_id"`
	EntryType   string    `json:"entry_type"`
	ReferenceID string    `json:"reference_id,omitempty"` // omitted from JSON when empty
	Amount      int64     `json:"amount"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Event schema.
type Event struct {
	AggregateID string    `json:"aggregate_id"`
	EventType   string    `json:"event_type"`
	Payload     string    `json:"payload"`
	Timestamp   time.Time `json:"timestamp"`
}

// Rate limit buckets are configured per tenant or per wallet.
type RateLimitConfig struct {
	Allowance rate.Limit
	Burst     int
}

// Wallet-scoped rate limit decision.
type RateLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// Tenants are the top-level tenant boundary for the platform.
// Each tenant has one or more API keys and its own rate-limit tier.
type Tenant struct {
	ID        string
	Name      string
	Status    TenantStatus
	Scopes    []TenantScope
	RateLimit RateLimitConfig
	CreatedAt time.Time
	UpdatedAt time.Time
}

type TenantStatus string

const (
	TenantStatusActive    TenantStatus = "active"
	TenantStatusSuspended TenantStatus = "suspended"
	TenantStatusDeleted   TenantStatus = "deleted"
)

type TenantScope string

const (
	ScopeWalletRead  TenantScope = "wallet:read"
	ScopeWalletWrite TenantScope = "wallet:write"
	ScopeTransfer    TenantScope = "transfer"
	ScopeAdmin       TenantScope = "admin"
	ScopeWebhooks    TenantScope = "webhooks"
)

// APIKey is the credential presented by callers.
// The plaintext secret is never stored; only a derived verifier is persisted.
type APIKey struct {
	ID        string
	TenantID  string
	Name      string
	KeyHash   string
	Scopes    []TenantScope
	RateLimit RateLimitConfig
	Status    APIKeyStatus
	CreatedBy string
	CreatedAt time.Time
	RevokedAt time.Time
}

type APIKeyStatus string

const (
	APIKeyActive  APIKeyStatus = "active"
	APIKeyRevoked APIKeyStatus = "revoked"
)

// APIKeyPair is the raw secret presented by the client, validated in memory only.
type APIKeyPair struct {
	RawKey   string
	TenantID string
	Scopes   []TenantScope
}

// ParseAPIKeyPrefix extracts a stable, safe-to-log prefix from a raw key.
//
// For keys that carry an identity segment (for example "pk_test_..."), the prefix
// is the type + environment segment ("pk_test") so logs and admin tooling see
// something readable. For keys without a second separator, the whole key is returned.
// For keys without any separator, the first 8 chars are used.
func ParseAPIKeyPrefix(rawKey string) string {
	if len(rawKey) == 0 {
		return ""
	}
	first := indexByte(rawKey, '_')
	if first < 0 {
		// No separator — use first 8 chars or the whole string.
		if len(rawKey) >= 8 {
			return rawKey[:8]
		}
		return rawKey
	}
	// Find the second underscore after the first.
	second := indexByte(rawKey[first+1:], '_')
	if second < 0 {
		// Only one underscore — return the whole key (two segments, no id portion).
		return rawKey
	}
	// Two or more underscores — return up to (but not including) the second underscore.
	return rawKey[:first+1+second]
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// AdminNote is a structured audit note attached to tenant or API key mutations.
type AdminNote struct {
	ID         string
	EntityType string
	EntityID   string
	Actor      string
	Action     string
	Note       string
	CreatedAt  time.Time
}

// APIKeyVerifier turns a raw key into a derived verifier for storage and auth.
//
// This is HMAC-based rather than hashed because the same derivation is needed
// at auth time. In a larger deployment you would bind this to a per-tenant or
// per-service HMAC key; here we use a global service secret so both ledger-svc
// and wallet-svc can verify the same keys.
type APIKeyVerifier struct {
	secret []byte
}

func NewAPIKeyVerifier(secret string) *APIKeyVerifier {
	return &APIKeyVerifier{secret: []byte(secret)}
}

// KeyHash derives the stored verifier for a raw key.
func (v *APIKeyVerifier) KeyHash(rawKey string) string {
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(rawKey))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify compares a raw key against a stored verifier in constant time.
func (v *APIKeyVerifier) Verify(rawKey, storedVerifier string) bool {
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(rawKey))
	got := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(got), []byte(storedVerifier))
}

// ParseScopes is a small helper used by admin endpoints that accept scope lists.
func ParseScopes(raw []string) []TenantScope {
	out := make([]TenantScope, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			out = append(out, TenantScope(s))
		}
	}
	return out
}
