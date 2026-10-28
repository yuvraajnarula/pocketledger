package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/yuvraajnarula/pocketledger/services/ledger-svc/internal/walletsvc"
	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

type Handler struct {
	db           *sql.DB
	metrics      *Metrics
	walletRepo   *WalletRepo
	ledgerRepo   *LedgerRepo
	eventRepo    *EventRepo
	idemStore    *IdempotencyStore
	transferSvc  *TransferService
	walletClient *walletsvc.Client
	advancedSvc  *AdvancedService
	proofEngine  *ProofEngine
	chaos        *ChaosController // replaces the old *ChaosConfig field
	rateLimiter  RateLimiter
	authStore    *APIKeyStore
}

func (h *Handler) WalletClient() *walletsvc.Client {
	if h.walletClient != nil {
		return h.walletClient
	}
	return h.transferSvc.WalletClient
}

func (h *Handler) SetWalletClient(wc *walletsvc.Client) {
	h.walletClient = wc
}

// NewHandler now accepts a pre-built *ChaosController so main.go owns the
// single instance that is also passed to ChaosMiddleware.
func NewHandler(db *sql.DB, rl RateLimiter, chaos *ChaosController, authStore *APIKeyStore) *Handler {
	wr := NewWalletRepo(db)
	lr := NewLedgerRepo(db)
	er := NewEventRepo(db)
	is := NewIdempotencyStore(db)

	ts := NewTransferService(db, wr, lr)

	var walletClient *walletsvc.Client
	if walletURL := os.Getenv("WALLET_SVC_URL"); walletURL != "" {
		walletClient = walletsvc.NewClient(walletURL)
		ts = ts.WithWalletClient(walletClient)
	}

	h := &Handler{
		db:           db,
		walletRepo:   wr,
		ledgerRepo:   lr,
		eventRepo:    er,
		idemStore:    is,
		transferSvc:  ts,
		walletClient: walletClient,
		advancedSvc:  NewAdvancedService(db),
		proofEngine:  NewProofEngine(os.Getenv("PROOF_SECRET_KEY")),
		chaos:        chaos,
		rateLimiter:  rl,
		authStore:    authStore,
	}

	if policyPath := os.Getenv("POLICY_CONFIG_PATH"); policyPath != "" {
		if _, err := LoadPolicy(policyPath); err != nil {
			fmt.Printf("warning: failed to load policy config: %v\n", err)
		}
	}

	return h
}

func (h *Handler) SetMetrics(m *Metrics) {
	h.metrics = m
}

func (h *Handler) Register(mux *http.ServeMux) {

	mux.Handle("/", h.rateLimiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "ledger-svc",
			"status":  "running",
		})
	})))

	mux.HandleFunc("POST /wallets/{src}/transfer", h.Transfer)
	mux.HandleFunc("GET /reconcile", h.Reconcile)
	mux.HandleFunc("GET /wallets/{id}/balance-at", h.GetBalanceAtTime)
	mux.HandleFunc("GET /idempotency/keys", h.ListIdempotencyKeys)
	if os.Getenv("CHAOS_ENABLED") == "true" {
		mux.HandleFunc("POST /admin/chaos", h.ToggleChaos)
	}
	mux.HandleFunc("GET /fx/rates", h.GetFXRates)
	mux.HandleFunc("POST /wallets/{src}/transfer/fx", h.TransferWithFX)
	mux.HandleFunc("GET /webhooks/deliveries", h.ListWebhooks)
	mux.HandleFunc("POST /admin/dispute", h.CreateDispute)
	mux.HandleFunc("GET /wallets/{id}/proof", h.GetBalanceProof)
	mux.HandleFunc("GET /admin/postmortem", h.GeneratePostMortem)
	mux.HandleFunc("POST /admin/postmortem", h.GeneratePostMortem)

	// Tenant / API key admin API.
	mux.HandleFunc("POST /admin/tenants", h.CreateTenant)
	mux.HandleFunc("GET /admin/tenants", h.ListTenants)
	mux.HandleFunc("GET /admin/tenants/{id}", h.GetTenant)
	mux.HandleFunc("POST /admin/tenants/{id}/keys", h.CreateAPIKey)
	mux.HandleFunc("GET /admin/tenants/{id}/keys", h.ListAPIKeys)
	mux.HandleFunc("POST /admin/keys/{keyId}/revoke", h.RevokeAPIKey)

	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /readyz", h.Readyz)
	mux.HandleFunc("GET /metrics", h.Metrics)
}

/* ---------------- HELPERS ---------------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

/* ---------------- CORE ---------------- */

func (h *Handler) Transfer(w http.ResponseWriter, r *http.Request) {
	srcID := r.PathValue("src")

	var req struct {
		DestID string `json:"destination_id"`
		Amount int64  `json:"amount"`
		RefID  string `json:"reference_id"`
		Memo   string `json:"memo"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DestID == "" {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}

	if err := ValidatePolicy(req.Amount, req.Memo != ""); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	err := h.transferSvc.Execute(r.Context(), srcID, req.DestID, req.Amount, req.RefID)

	switch {
	case err == domain.ErrWalletNotFound:
		writeError(w, http.StatusNotFound, err)
	case err == domain.ErrInsufficientFunds:
		writeError(w, http.StatusPaymentRequired, err)
	case err == domain.ErrOptimisticLockFail:
		writeError(w, http.StatusConflict, err)
	case err == domain.ErrCurrencyMismatch:
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "transferred"})
	}
}

func (h *Handler) Reconcile(w http.ResponseWriter, r *http.Request) {
	var totalWallet, totalCred, totalDeb int64

	err := h.db.QueryRowContext(r.Context(), `
		SELECT
			COALESCE((SELECT SUM(balance) FROM wallets WHERE id != 'bank-main'), 0),
			COALESCE((SELECT SUM(amount) FROM ledger_entries WHERE entry_type='CREDIT'), 0),
			COALESCE((SELECT SUM(amount) FROM ledger_entries WHERE entry_type='DEBIT'), 0)
	`).Scan(&totalWallet, &totalCred, &totalDeb)

	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	diff := totalCred - totalDeb - totalWallet
	status := "balanced"
	if diff != 0 {
		status = "discrepancy"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       status,
		"total_wallet": totalWallet,
		"credits":      totalCred,
		"debits":       totalDeb,
		"diff":         diff,
	})
}

func (h *Handler) GetBalanceAtTime(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	at := r.URL.Query().Get("at")

	ts, err := time.Parse(time.RFC3339, at)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timestamp"))
		return
	}

	bal, err := h.eventRepo.GetBalanceAt(r.Context(), id, ts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"wallet_id":  id,
		"balance_at": bal,
		"timestamp":  ts,
	})
}

func (h *Handler) ListIdempotencyKeys(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = l
	}

	keys, err := h.idemStore.ListKeys(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, keys)
}

func (h *Handler) ToggleChaos(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body"))
		return
	}

	if req.Enabled {
		h.chaos.Enable()
	} else {
		h.chaos.Disable()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": h.chaos.IsEnabled(),
		"config": map[string]any{
			"failure_probability": h.chaos.cfg.FailureProbability,
			"min_latency_ms":      h.chaos.cfg.MinLatencyMs,
			"max_latency_ms":      h.chaos.cfg.MaxLatencyMs,
		},
	})
}

/* ---------------- REQUIRED METHOD ---------------- */

func (h *Handler) GetFXRates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT base_currency, target_currency, rate FROM fx_rates
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	var out []any
	for rows.Next() {
		var b, t string
		var rate float64
		_ = rows.Scan(&b, &t, &rate)
		out = append(out, map[string]any{
			"base":   b,
			"target": t,
			"rate":   rate,
		})
	}

	writeJSON(w, http.StatusOK, out)
}

/* ---------------- REAL IMPLEMENTATIONS (previously stubs) ---------------- */

// TransferWithFX executes a cross-currency transfer via ExecuteFX.
func (h *Handler) TransferWithFX(w http.ResponseWriter, r *http.Request) {
	srcID := r.PathValue("src")

	var req struct {
		DestID string `json:"destination_id"`
		Amount int64  `json:"amount"`
		RefID  string `json:"reference_id"`
		Memo   string `json:"memo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DestID == "" {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}

	srcAmt, dstAmt, err := h.transferSvc.ExecuteFX(r.Context(), srcID, req.DestID, req.Amount, req.RefID)
	switch {
	case err == domain.ErrWalletNotFound:
		writeError(w, http.StatusNotFound, err)
	case err == domain.ErrInsufficientFunds:
		writeError(w, http.StatusPaymentRequired, err)
	case err == domain.ErrOptimisticLockFail:
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusBadRequest, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"status":             "transferred",
			"source_amount":      srcAmt,
			"destination_amount": dstAmt,
		})
	}
}

// ListWebhooks returns webhook delivery history from the webhook_queue table.
func (h *Handler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}

	deliveries, err := h.advancedSvc.ListWebhookDeliveries(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if deliveries == nil {
		deliveries = []WebhookDelivery{}
	}
	writeJSON(w, http.StatusOK, deliveries)
}

// CreateDispute reversals the debit side of a reference via AdvancedService.
func (h *Handler) CreateDispute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReferenceID string `json:"reference_id"`
		WalletID    string `json:"wallet_id"`
		Reason      string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body"))
		return
	}
	if req.ReferenceID == "" || req.WalletID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("reference_id and wallet_id are required"))
		return
	}

	if err := h.advancedSvc.CreateDispute(r.Context(), req.ReferenceID, req.WalletID, req.Reason); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reversed", "reference_id": req.ReferenceID})
}

// GetBalanceProof returns an HMAC proof of the wallet's current balance.
func (h *Handler) GetBalanceProof(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nonce := r.URL.Query().Get("nonce")

	wallet, err := h.walletRepo.Get(r.Context(), nil, id)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, domain.ErrWalletNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	proof, err := h.proofEngine.GenerateBalanceProof(wallet.ID, wallet.Balance, nonce)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"wallet_id": wallet.ID,
		"balance":   wallet.Balance,
		"currency":  wallet.Currency,
		"nonce":     nonce,
		"proof":     proof,
		"algorithm": "HMAC-SHA256",
	})
}

// GeneratePostMortem returns the full text report from AdvancedService.
func (h *Handler) GeneratePostMortem(w http.ResponseWriter, r *http.Request) {
	report, err := h.advancedSvc.GeneratePostMortem(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(report))
}

/* ---------------- ADMIN API (tenants & API keys) ---------------- */

// serializeTenant makes a JSON view of a TenantRow.
func serializeTenant(t *TenantRow) map[string]any {
	scopes := make([]string, 0, len(t.Scopes))
	for _, s := range t.Scopes {
		scopes = append(scopes, string(s))
	}
	return map[string]any{
		"id":               t.ID,
		"name":             t.Name,
		"status":           t.Status,
		"scopes":           scopes,
		"rate_limit_rps":   t.RateLimitRPS,
		"rate_limit_burst": t.RateLimitBurst,
		"created_at":       t.CreatedAt,
		"updated_at":       t.UpdatedAt,
	}
}

// serializeAPIKey makes a JSON view of an APIKeyRow. The key hash is never returned.
func serializeAPIKey(k *APIKeyRow) map[string]any {
	scopes := make([]string, 0, len(k.Scopes))
	for _, s := range k.Scopes {
		scopes = append(scopes, string(s))
	}
	out := map[string]any{
		"id":               k.ID,
		"tenant_id":        k.TenantID,
		"name":             k.Name,
		"scopes":           scopes,
		"rate_limit_rps":   k.RateLimitRPS,
		"rate_limit_burst": k.RateLimitBurst,
		"status":           k.Status,
		"created_by":       k.CreatedBy,
		"created_at":       k.CreatedAt,
	}
	if k.RevokedAt.Valid {
		out["revoked_at"] = k.RevokedAt.Time
	}
	return out
}

const adminAuditEntityTypeTenant = "tenant"
const adminAuditEntityTypeAPIKey = "api_key"

// defaultTenantScopes is what a tenant gets when no scopes are supplied.
var defaultTenantScopes = []string{string(domain.ScopeTransfer), string(domain.ScopeWalletRead), string(domain.ScopeWalletWrite)}

func parseScopesParam(raw []string, fallback []string) []domain.TenantScope {
	if len(raw) == 0 {
		raw = fallback
	}
	return domain.ParseScopes(raw)
}

// requireAdmin returns true when the request is allowed to use the admin API.
// When a RequestContext exists (store-based auth) the admin scope is enforced;
// when the legacy shared env key authenticated the request, access is granted so
// the admin API can bootstrap its very first tenant+admin key.
func requireAdmin(r *http.Request) bool {
	rc, ok := RequestContextFromContext(r.Context())
	if !ok {
		// Legacy / bootstrap mode.
		return true
	}
	for _, s := range rc.Scopes {
		if s == domain.ScopeAdmin {
			return true
		}
	}
	return false
}

func (h *Handler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient scope"})
		return
	}
	var req struct {
		ID             string   `json:"id"`
		Name           string   `json:"name"`
		Scopes         []string `json:"scopes"`
		RateLimitRPS   float64  `json:"rate_limit_rps"`
		RateLimitBurst int      `json:"rate_limit_burst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body"))
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("name is required"))
		return
	}
	// Explicit-ID duplicate check (DB PK violations don't map to os.ErrExist).
	if req.ID != "" {
		if _, err := h.authStore.Tenant(r.Context(), req.ID); err == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "tenant already exists"})
			return
		}
	}

	tenant, err := h.authStore.CreateTenant(r.Context(), CreateTenantRequest{
		ID:             req.ID,
		Name:           req.Name,
		Status:         string(domain.TenantStatusActive),
		Scopes:         parseScopesParam(req.Scopes, defaultTenantScopes),
		RateLimitRPS:   req.RateLimitRPS,
		RateLimitBurst: req.RateLimitBurst,
	})
	if err == os.ErrExist {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "tenant already exists"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	_ = h.authStore.CreateAuditNote(r.Context(), CreateAuditNoteRequest{
		EntityType: adminAuditEntityTypeTenant,
		EntityID:   tenant.ID,
		Actor:      adminActor(r),
		Action:     "tenant.created",
		Note:       "tenant created via admin API",
	})

	writeJSON(w, http.StatusCreated, serializeTenant(tenant))
}

// adminActor identifies the caller for audit notes.
func adminActor(r *http.Request) string {
	if rc, ok := RequestContextFromContext(r.Context()); ok {
		return "apikey:" + rc.APIKeyID
	}
	return "legacy-shared-key"
}

func (h *Handler) ListTenants(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient scope"})
		return
	}
	tenants, err := h.authStore.ListTenants(r.Context(), domain.TenantStatus(r.URL.Query().Get("status")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tenants == nil {
		tenants = []*TenantRow{}
	}
	out := make([]map[string]any, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, serializeTenant(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) GetTenant(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient scope"})
		return
	}
	tenant, err := h.authStore.Tenant(r.Context(), r.PathValue("id"))
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, serializeTenant(tenant))
}

// CreateAPIKey issues a new key for a tenant. The raw secret is returned exactly
// once; only its hash is stored.
func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient scope"})
		return
	}
	var req struct {
		ID             string   `json:"id"`
		TenantID       string   `json:"tenant_id"`
		Name           string   `json:"name"`
		Scopes         []string `json:"scopes"`
		RateLimitRPS   float64  `json:"rate_limit_rps"`
		RateLimitBurst int      `json:"rate_limit_burst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body"))
		return
	}
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = r.PathValue("id")
	}
	if tenantID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("tenant_id and name are required"))
		return
	}

	// Ensure tenant exists first.
	if _, err := h.authStore.Tenant(r.Context(), tenantID); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	rawKey, err := GenerateRawAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	key, err := h.authStore.CreateAPIKey(r.Context(), rawKey, CreateAPIKeyRequest{
		ID:             req.ID,
		TenantID:       tenantID,
		Name:           req.Name,
		Scopes:         parseScopesParam(req.Scopes, defaultTenantScopes),
		RateLimitRPS:   req.RateLimitRPS,
		RateLimitBurst: req.RateLimitBurst,
		Status:         string(domain.APIKeyActive),
		CreatedBy:      adminActor(r),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	_ = h.authStore.CreateAuditNote(r.Context(), CreateAuditNoteRequest{
		EntityType: adminAuditEntityTypeAPIKey,
		EntityID:   key.ID,
		Actor:      adminActor(r),
		Action:     "api_key.created",
		Note:       "key issued via admin API for tenant " + tenantID,
	})

	resp := serializeAPIKey(key)
	resp["raw_key"] = rawKey // shown once, never recoverable later
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient scope"})
		return
	}
	keys, err := h.authStore.ListAPIKeys(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if keys == nil {
		keys = []*APIKeyRow{}
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, serializeAPIKey(k))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient scope"})
		return
	}
	keyID := r.PathValue("keyId")

	if err := h.authStore.RevokeAPIKey(r.Context(), keyID, adminActor(r)); err != nil {
		if err == os.ErrExist {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found or already revoked"})
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	_ = h.authStore.CreateAuditNote(r.Context(), CreateAuditNoteRequest{
		EntityType: adminAuditEntityTypeAPIKey,
		EntityID:   keyID,
		Actor:      adminActor(r),
		Action:     "api_key.revoked",
		Note:       "key revoked via admin API",
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": keyID})
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if err := h.db.PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	type probe struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Err  string `json:"error,omitempty"`
	}
	probes := []probe{
		{Name: "database", OK: true},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.PingContext(ctx); err != nil {
		probes[0].OK = false
		probes[0].Err = err.Error()
	}

	allOK := true
	for _, p := range probes {
		if !p.OK {
			allOK = false
			break
		}
	}

	status := http.StatusOK
	if !allOK {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  allOK,
		"probes":  probes,
		"version": "0.1.0",
	})
}

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	if h.metrics == nil {
		http.Error(w, "metrics disabled", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, h.metrics.PrometheusText())
}
