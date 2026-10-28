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

/* ---------------- STUBS ---------------- */

func (h *Handler) TransferWithFX(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *Handler) CreateDispute(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) GetBalanceProof(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) GeneratePostMortem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
