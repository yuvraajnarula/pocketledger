package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

// APIKeyStore is the credential and tenant store.
type APIKeyStore struct {
	db       *sql.DB
	verifier *domain.APIKeyVerifier
}

func NewAPIKeyStore(db *sql.DB, verifier *domain.APIKeyVerifier) *APIKeyStore {
	return &APIKeyStore{
		db:       db,
		verifier: verifier,
	}
}

// EnsureSchema creates the tenant and API key tables if they do not exist.
func (s *APIKeyStore) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS tenants (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			scopes TEXT NOT NULL DEFAULT '[]',
			rate_limit_rps REAL NOT NULL DEFAULT 100,
			rate_limit_burst INTEGER NOT NULL DEFAULT 100,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(id),
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT '[]',
			rate_limit_rps REAL NOT NULL DEFAULT 100,
			rate_limit_burst INTEGER NOT NULL DEFAULT 100,
			status TEXT NOT NULL DEFAULT 'active',
			created_by TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			revoked_at TIMESTAMPTZ
		);
		CREATE TABLE IF NOT EXISTS audit_notes (
			id TEXT PRIMARY KEY,
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			note TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys(tenant_id);
		CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
		CREATE INDEX IF NOT EXISTS idx_audit_notes_entity ON audit_notes(entity_type, entity_id);
	`)
	return err
}

// TenantRow is the internal DB shape for a tenant.
type TenantRow struct {
	ID             string
	Name           string
	Status         domain.TenantStatus
	Scopes         []domain.TenantScope
	RateLimitRPS   float64
	RateLimitBurst int
	CreatedAt      string
	UpdatedAt      string
}

func scanTenant(row *sql.Row) (*TenantRow, error) {
	var r TenantRow
	var scopesRaw string
	err := row.Scan(
		&r.ID, &r.Name, &r.Status, &scopesRaw,
		&r.RateLimitRPS, &r.RateLimitBurst, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.Scopes = parseJSONScopes(scopesRaw)
	return &r, nil
}

func scanTenantRows(rows *sql.Rows) ([]*TenantRow, error) {
	var out []*TenantRow
	for rows.Next() {
		var r TenantRow
		var scopesRaw string
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Status, &scopesRaw,
			&r.RateLimitRPS, &r.RateLimitBurst, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		r.Scopes = parseJSONScopes(scopesRaw)
		out = append(out, &r)
	}
	return out, rows.Err()
}

func parseJSONScopes(raw string) []domain.TenantScope {
	var scopes []string
	if err := json.Unmarshal([]byte(raw), &scopes); err != nil {
		return nil
	}
	return domain.ParseScopes(scopes)
}

// Tenant returns a tenant by ID.
func (s *APIKeyStore) Tenant(ctx context.Context, id string) (*TenantRow, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, name, status, scopes, rate_limit_rps, rate_limit_burst, created_at, updated_at FROM tenants WHERE id = $1",
		id,
	)
	return scanTenant(row)
}

// ListTenants returns tenants with optional status filtering.
func (s *APIKeyStore) ListTenants(ctx context.Context, status domain.TenantStatus) ([]*TenantRow, error) {
	query := "SELECT id, name, status, scopes, rate_limit_rps, rate_limit_burst, created_at, updated_at FROM tenants"
	var args []any
	if status != "" {
		query += " WHERE status = $1"
		args = append(args, string(status))
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTenantRows(rows)
}

// CreateTenant inserts a tenant.
func (s *APIKeyStore) CreateTenant(ctx context.Context, req CreateTenantRequest) (*TenantRow, error) {
	id := req.ID
	if id == "" {
		id = uuid.NewString()
	}
	scopesJSON, err := json.Marshal(req.Scopes)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, scopes, rate_limit_rps, rate_limit_burst)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, req.Name, string(req.Status), scopesJSON, req.RateLimitRPS, req.RateLimitBurst,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, os.ErrExist
	}
	return s.Tenant(ctx, id)
}

// CreateAPIKey inserts a new API key for a tenant.
func (s *APIKeyStore) CreateAPIKey(ctx context.Context, rawKey string, req CreateAPIKeyRequest) (*APIKeyRow, error) {
	scopesJSON, err := json.Marshal(req.Scopes)
	if err != nil {
		return nil, err
	}
	keyID := req.ID
	if keyID == "" {
		keyID = uuid.NewString()
	}
	hash := s.verifier.KeyHash(rawKey)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, name, key_hash, scopes, rate_limit_rps, rate_limit_burst, status, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		keyID, req.TenantID, req.Name, hash, scopesJSON, req.RateLimitRPS, req.RateLimitBurst, string(req.Status), req.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return s.APIKey(ctx, keyID)
}

// APIKeyRow is the internal DB shape for an API key.
type APIKeyRow struct {
	ID             string
	TenantID       string
	Name           string
	KeyHash        string
	Scopes         []domain.TenantScope
	RateLimitRPS   float64
	RateLimitBurst int
	Status         domain.APIKeyStatus
	CreatedBy      string
	CreatedAt      string
	RevokedAt      sql.NullTime
}

func scanAPIKey(row *sql.Row) (*APIKeyRow, error) {
	var r APIKeyRow
	var scopesRaw string
	err := row.Scan(
		&r.ID, &r.TenantID, &r.Name, &r.KeyHash, &scopesRaw,
		&r.RateLimitRPS, &r.RateLimitBurst, &r.Status, &r.CreatedBy, &r.CreatedAt, &r.RevokedAt,
	)
	if err != nil {
		return nil, err
	}
	r.Scopes = parseJSONScopes(scopesRaw)
	return &r, nil
}

// APIKey returns an API key by ID.
func (s *APIKeyStore) APIKey(ctx context.Context, id string) (*APIKeyRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, key_hash, scopes, rate_limit_rps, rate_limit_burst, status, created_by, created_at, revoked_at
		 FROM api_keys WHERE id = $1`,
		id,
	)
	return scanAPIKey(row)
}

// ResolveAPIKey looks up an API key by its stored verifier.
func (s *APIKeyStore) ResolveAPIKey(ctx context.Context, rawKey string) (*APIKeyRow, error) {
	hash := s.verifier.KeyHash(rawKey)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, key_hash, scopes, rate_limit_rps, rate_limit_burst, status, created_by, created_at, revoked_at
		 FROM api_keys WHERE key_hash = $1`,
		hash,
	)
	return scanAPIKey(row)
}

// Authenticate verifies the raw key and returns the resolved API key row if valid.
func (s *APIKeyStore) Authenticate(ctx context.Context, rawKey string) (*APIKeyRow, error) {
	key, err := s.ResolveAPIKey(ctx, rawKey)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, domain.ErrWalletNotFound
	}
	if key.Status != domain.APIKeyActive {
		return nil, domain.ErrWalletNotFound
	}
	return key, nil
}

// RevokeAPIKey marks a key revoked.
func (s *APIKeyStore) RevokeAPIKey(ctx context.Context, keyID, actor string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET status = 'revoked', revoked_at = NOW() WHERE id = $1 AND status = 'active'`,
		keyID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return os.ErrExist
	}
	return nil
}

// CreateAuditNote inserts a structured audit note.
func (s *APIKeyStore) CreateAuditNote(ctx context.Context, req CreateAuditNoteRequest) error {
	id := req.ID
	if id == "" {
		id = uuid.NewString()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_notes (id, entity_type, entity_id, actor, action, note)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, req.EntityType, req.EntityID, req.Actor, req.Action, req.Note,
	)
	return err
}

// ListAuditNotes returns audit notes for an entity.
func (s *APIKeyStore) ListAuditNotes(ctx context.Context, entityType, entityID string) ([]*domain.AdminNote, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, entity_type, entity_id, actor, action, note, created_at
		 FROM audit_notes WHERE entity_type = $1 AND entity_id = $2 ORDER BY created_at DESC`,
		entityType, entityID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.AdminNote
	for rows.Next() {
		var n domain.AdminNote

		var createdAt string
		if err := rows.Scan(&n.ID, &n.EntityType, &n.EntityID, &n.Actor, &n.Action, &n.Note, &createdAt); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			n.CreatedAt = t
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

// RequestContext carries the authenticated principal for a request.
type RequestContext struct {
	TenantID string
	APIKeyID string
	Scopes   []domain.TenantScope
}

type ctxKeyRequestContextType struct{}

var ctxKeyRequestContext = &ctxKeyRequestContextType{}

// WithRequestContext attaches a RequestContext to the request.
func WithRequestContext(ctx context.Context, rc *RequestContext) context.Context {
	return context.WithValue(ctx, ctxKeyRequestContext, rc)
}

// RequestContextFromContext extracts the RequestContext from a context.
func RequestContextFromContext(ctx context.Context) (*RequestContext, bool) {
	v, ok := ctx.Value(ctxKeyRequestContext).(*RequestContext)
	return v, ok
}

// RequireScope returns an error text if the request context lacks a scope.
func RequireScope(ctx context.Context, scope domain.TenantScope) (bool, string) {
	rc, ok := RequestContextFromContext(ctx)
	if !ok {
		return false, "unauthenticated"
	}
	for _, s := range rc.Scopes {
		if s == scope {
			return true, ""
		}
	}
	return false, "insufficient scope"
}

// HasScope returns true if the request context includes the scope.
func HasScope(ctx context.Context, scope domain.TenantScope) bool {
	ok, _ := RequireScope(ctx, scope)
	return ok
}

// AuthMiddleware is the tenant-aware auth middleware.
func AuthMiddleware(store *APIKeyStore) func(http.Handler) http.Handler {
	exempt := map[string]bool{
		"/health":  true,
		"/readyz":  true,
		"/metrics": true,
	}

	legacyDisabled := os.Getenv("API_KEY_STORE_DISABLED") == ""

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			rawKey := r.Header.Get("X-API-Key")
			if rawKey == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}

			if legacyDisabled {
				key, err := store.Authenticate(r.Context(), rawKey)
				if err != nil {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
					return
				}
				rc := &RequestContext{
					TenantID: key.TenantID,
					APIKeyID: key.ID,
					Scopes:   key.Scopes,
				}
				next.ServeHTTP(w, r.WithContext(WithRequestContext(r.Context(), rc)))
				return
			}

			// Legacy single-key fallback.
			if expected := os.Getenv("API_KEY"); expected != "" && rawKey == expected {
				next.ServeHTTP(w, r)
				return
			}

			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		})
	}
}

// Admin request types.

// CreateTenantRequest is the admin request for creating a tenant.
type CreateTenantRequest struct {
	ID             string
	Name           string
	Status         string
	Scopes         []domain.TenantScope
	RateLimitRPS   float64
	RateLimitBurst int
}

// CreateAPIKeyRequest is the admin request for creating an API key.
type CreateAPIKeyRequest struct {
	ID             string
	TenantID       string
	Name           string
	Scopes         []domain.TenantScope
	RateLimitRPS   float64
	RateLimitBurst int
	Status         string
	CreatedBy      string
}

// CreateAuditNoteRequest is the admin request for an audit note.
type CreateAuditNoteRequest struct {
	ID         string
	EntityType string
	EntityID   string
	Actor      string
	Action     string
	Note       string
}
