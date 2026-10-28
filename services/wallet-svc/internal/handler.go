package internal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

type Handler struct {
	db         *sql.DB
	metrics    *Metrics
	httpClient *http.Client
	authStore  *APIKeyStore
}

func NewHandler(db *sql.DB, httpClient *http.Client, authStore *APIKeyStore) *Handler {
	return &Handler{db: db, httpClient: httpClient, authStore: authStore}
}

func (h *Handler) SetMetrics(m *Metrics) {
	h.metrics = m
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /wallets", h.CreateWallet)
	mux.HandleFunc("GET /wallets", h.ListWallets)
	mux.HandleFunc("GET /wallets/{id}", h.GetWallet)
	mux.HandleFunc("POST /wallets/{id}/deposit", h.Deposit) // proxy to ledger-svc
	mux.HandleFunc("POST /wallets/{id}/withdraw", h.Withdraw)
	mux.HandleFunc("POST /internal/wallets/{id}/credit", h.Credit) // called by ledger-svc
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (h *Handler) CreateWallet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Currency string `json:"currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidCurrency)
		return
	}

	currency := strings.ToUpper(req.Currency)
	if len(currency) != 3 {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidCurrency)
		return
	}

	wallet := &domain.Wallet{
		ID:        uuid.New().String(),
		Currency:  currency,
		Balance:   0,
		Version:   0,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	wr := NewWalletRepo(h.db)
	if err := wr.Create(r.Context(), tx, wallet); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Append event (inline for brevity)
	payload, _ := json.Marshal(map[string]any{"id": wallet.ID, "currency": wallet.Currency})
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO event_log (aggregate_id, event_type, payload, created_at) VALUES (?,?,?,?)",
		wallet.ID, "wallet.created", string(payload), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusCreated, wallet)
}

// ---------- ListWallets ----------
func (h *Handler) ListWallets(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}

	wr := NewWalletRepo(h.db)
	wallets, err := wr.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if wallets == nil {
		wallets = []*domain.Wallet{}
	}
	writeJSON(w, http.StatusOK, wallets)
}

// ---------- GetWallet ----------
func (h *Handler) GetWallet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wr := NewWalletRepo(h.db)
	wallet, err := wr.Get(r.Context(), nil, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, domain.ErrWalletNotFound)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, wallet)
}

// ---------- Deposit (proxy to ledger-svc) ----------
func (h *Handler) Deposit(w http.ResponseWriter, r *http.Request) {
	userWalletID := r.PathValue("id")
	var req struct {
		Amount int64  `json:"amount"`
		RefID  string `json:"reference_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}

	// Get bank wallet ID from environment
	bankWalletID := os.Getenv("BANK_WALLET_ID")
	if bankWalletID == "" {
		bankWalletID = "bank-main"
	}

	// Get ledger-svc URL
	ledgerSvcURL := os.Getenv("LEDGER_SVC_URL")
	if ledgerSvcURL == "" {
		ledgerSvcURL = "http://ledger-svc:8082"
	}

	// Build transfer request to ledger-svc
	transferReq := map[string]interface{}{
		"destination_id": userWalletID,
		"amount":         req.Amount,
		"reference_id":   req.RefID,
		"memo":           "deposit via bank wallet",
	}
	jsonBody, err := json.Marshal(transferReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	idempotencyKey := uuid.New().String()

	transferURL := fmt.Sprintf("%s/wallets/%s/transfer", ledgerSvcURL, bankWalletID)
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST", transferURL, bytes.NewReader(jsonBody))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to call ledger service: %w", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil {
			if msg, ok := errResp["error"]; ok {
				writeError(w, resp.StatusCode, fmt.Errorf(msg))
				return
			}
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("ledger service returned %d", resp.StatusCode))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deposited"})
}

// ---------- Credit (called by ledger-svc – idempotent + event log) ----------
func (h *Handler) Credit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Amount int64  `json:"amount"`
		RefID  string `json:"reference_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	// Idempotency check
	var already bool
	err = tx.QueryRowContext(r.Context(),
		"SELECT EXISTS(SELECT 1 FROM idempotency_keys WHERE key = ?)", req.RefID,
	).Scan(&already)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if already {
		tx.Commit()
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_credited"})
		return
	}

	// Verify wallet exists
	var exists bool
	err = tx.QueryRowContext(r.Context(),
		"SELECT EXISTS(SELECT 1 FROM wallets WHERE id = ?)", id,
	).Scan(&exists)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, domain.ErrWalletNotFound)
		return
	}

	// Credit the wallet
	_, err = tx.ExecContext(r.Context(),
		`UPDATE wallets SET balance = balance + ?, version = version + 1, updated_at = ?
		 WHERE id = ?`,
		req.Amount, time.Now().UTC(), id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	now := time.Now().UTC()
	expiresAt := now.Add(24 * time.Hour)
	responseBodyJSON := `{"status":"credited"}`

	// Record idempotency key
	_, err = tx.ExecContext(r.Context(),
		`INSERT INTO idempotency_keys (key, status, request_hash, response_code, response_body, created_at, expires_at)
		 VALUES (?, 'completed', ?, 200, ?, ?, ?)`,
		req.RefID, req.RefID, responseBodyJSON, now, expiresAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var balance int64
	err = tx.QueryRowContext(r.Context(), `SELECT balance FROM wallets WHERE id = ?`, id).Scan(&balance)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Log event for time-travel balance queries
	payload, _ := json.Marshal(map[string]any{"amount": req.Amount, "reference_id": req.RefID})
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO event_log (aggregate_id, event_type, payload, created_at) VALUES (?,?,?,?)",
		id, "wallet.credit", string(payload), time.Now().UTC(),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "credited", "balance": balance})
}

func (h *Handler) Withdraw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Amount int64  `json:"amount"`
		RefID  string `json:"reference_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidAmount)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(r.Context(),
		`UPDATE wallets SET balance = balance - ?, version = version + 1, updated_at = ?
		 WHERE id = ? AND balance >= ?`,
		req.Amount, time.Now().UTC(), id, req.Amount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		var exists bool
		tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM wallets WHERE id=?)", id).Scan(&exists)
		if !exists {
			writeError(w, http.StatusNotFound, domain.ErrWalletNotFound)
		} else {
			writeError(w, http.StatusPaymentRequired, domain.ErrInsufficientFunds)
		}
		return
	}

	entries, _ := domain.ComputeWithdraw(id, 0, req.Amount)
	if err := insertLedgerEntries(r.Context(), tx, entries); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	payload, _ := json.Marshal(map[string]any{"amount": req.Amount, "reference_id": req.RefID})
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO event_log (aggregate_id, event_type, payload, created_at) VALUES (?,?,?,?)",
		id, "wallet.withdraw", string(payload), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var balance int64
	err = tx.QueryRowContext(r.Context(), `SELECT balance FROM wallets WHERE id = ?`, id).Scan(&balance)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "withdrawn", "balance": balance})
}

func insertLedgerEntries(ctx context.Context, tx *sql.Tx, entries []domain.LedgerEntry) error {
	for _, e := range entries {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO ledger_entries (id, wallet_id, entry_type, amount, reference_id, created_at) VALUES (?,?,?,?,?,?)",
			uuid.New().String(), e.WalletID, e.EntryType, e.Amount, e.ReferenceID, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return nil
}

var startTime = time.Now()

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unhealthy", "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "healthy",
		"version":        "0.1.0",
		"uptime_seconds": time.Since(startTime).Seconds(),
		"database":       "sqlite_wal",
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	type probe struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Err  string `json:"error,omitempty"`
	}
	probes := []probe{
		{
			Name: "database",
			OK:   true,
		},
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
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprint(w, h.metrics.PrometheusText())
}
