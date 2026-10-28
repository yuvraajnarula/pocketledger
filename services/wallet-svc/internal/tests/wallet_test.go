package tests

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/yuvraajnarula/pocketledger/services/wallet-svc/internal"
	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

func newTestServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	ledgerStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(ledgerStub.Close)
	t.Setenv("LEDGER_SVC_URL", ledgerStub.URL)

	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	schema := `
	CREATE TABLE IF NOT EXISTS wallets (
		id TEXT PRIMARY KEY,
		currency TEXT NOT NULL,
		balance INTEGER DEFAULT 0,
		version INTEGER DEFAULT 0,
		created_at DATETIME,
		updated_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS ledger_entries (
		id TEXT PRIMARY KEY,
		wallet_id TEXT,
		entry_type TEXT,
		amount INTEGER,
		reference_id TEXT,
		created_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS event_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		aggregate_id TEXT,
		event_type TEXT,
		payload TEXT,
		created_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS idempotency_keys (
		key TEXT PRIMARY KEY,
		status TEXT,
		request_hash TEXT,
		response_code INTEGER,
		response_body TEXT,
		created_at DATETIME,
		expires_at DATETIME
	);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}

	authStore := internal.NewAPIKeyStore(db, domain.NewAPIKeyVerifier("test-secret"))
	t.Setenv("API_KEY_STORE_DISABLED", "1")
	t.Setenv("API_KEY", "test-api-key-123")
	handler := internal.NewHandler(db, &http.Client{}, authStore)
	mux := http.NewServeMux()
	handler.Register(mux)
	mux.HandleFunc("GET /health", handler.Health)

	protected := internal.AuthMiddleware(authStore)(mux)
	return httptest.NewServer(protected), db
}

func doJSON(method, url string, body any) (*http.Response, map[string]any, error) {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return nil, nil, err
		}
	}
	req, err := http.NewRequest(method, url, &b)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := os.Getenv("API_KEY"); apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	bodyBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		result = map[string]any{"raw": string(bodyBytes), "status": resp.StatusCode}
	}
	return resp, result, nil
}

func TestWalletLifecycle(t *testing.T) {
	ts, db := newTestServer(t)
	defer ts.Close()

	// Create wallet
	baseURL := ts.URL

	// Create wallet
	resp, body, err := doJSON(http.MethodPost, baseURL+"/wallets", map[string]string{"currency": "USD"})
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create wallet: err=%v status=%d body=%+v", err, resp.StatusCode, body)
	}
	walletID, ok := body["id"].(string)
	if !ok {
		t.Fatalf("wallet id missing: %v", body)
	}

	// Deposit 5000 via a direct Credit call. This call is idempotent; repeating it
	// with the same reference_id later returns "already_credited".
	depResp, depBody, _ := doJSON(http.MethodPost, baseURL+fmt.Sprintf("/internal/wallets/%s/credit", walletID),
		map[string]any{"amount": 5000, "reference_id": "dep1"})
	if depResp.StatusCode != http.StatusOK {
		t.Fatalf("deposit credit: status=%d body=%+v", depResp.StatusCode, depBody)
	}
	t.Logf("deposit credit status=%d body=%+v", depResp.StatusCode, depBody)

	// Withdraw 2000 -> ok

	// Withdraw 2000 -> ok

	// Reconcile local wallet balance with the credit recorded by the Credit endpoint
	// Credit via internal endpoint and assert the response.
	type creditResp struct {
		Status  string `json:"status"`
		Balance int64  `json:"balance"`
	}
	resp, body, err = doJSON(http.MethodPost, baseURL+fmt.Sprintf("/internal/wallets/%s/credit", walletID),
		map[string]any{"amount": 5000, "reference_id": "dep1"})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("credit reconcile: err=%v status=%d body=%+v", err, resp.StatusCode, body)
	}
	var creditRespBody creditResp
	if rawJSON, ok := body["raw"].(string); ok {
		if err := json.Unmarshal([]byte(rawJSON), &creditRespBody); err != nil {
			t.Fatalf("parse credit response: %v", err)
		}
	} else {
		creditRespBody.Status = body["status"].(string)
		if b, ok := body["balance"].(float64); ok {
			creditRespBody.Balance = int64(b)
		}
	}
	if creditRespBody.Status != "already_credited" {
		t.Fatalf("expected already_credited, got %s", creditRespBody.Status)
	}

	// Withdraw 2000 -> ok
	wd1Resp, wd1Body, err := doJSON(http.MethodPost, fmt.Sprintf("%s/wallets/%s/withdraw", baseURL, walletID),
		map[string]any{"amount": 2000, "reference_id": "wd1"})
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	t.Logf("withdraw 2000 status=%d body=%+v", wd1Resp.StatusCode, wd1Body)
	t.Logf("wallet balance after withdraw 2000: %v", wd1Body["balance"])
	t.Logf("wallet balance after withdraw 2000 (DB): %v", readBalance(t, db, walletID))

	// Withdraw 4000 -> insufficient funds (402)
	wd2Resp, wd2Body, err := doJSON(http.MethodPost, fmt.Sprintf("%s/wallets/%s/withdraw", baseURL, walletID),
		map[string]any{"amount": 4000, "reference_id": "wd2"})
	t.Logf("withdraw 4000 status=%d body=%+v", wd2Resp.StatusCode, wd2Body)
	t.Logf("wallet balance after withdraw 4000 (DB): %v", readBalance(t, db, walletID))
	if err != nil {
		t.Fatalf("withdraw 4000: %v", err)
	}
	if wd2Resp.StatusCode == http.StatusOK {
		t.Fatalf("expected 402, got 200 body=%+v", wd2Body)
	}
	if wd2Resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("expected 402, got %d body=%+v", wd2Resp.StatusCode, wd2Body)
	}

	// Verify final wallet balance (should be 3000)
	var finalBalance int64
	db.QueryRow("SELECT balance FROM wallets WHERE id = ?", walletID).Scan(&finalBalance)
	t.Logf("final wallet balance: %d", finalBalance)
	if finalBalance != 3000 {
		t.Errorf("expected final balance 3000, got %d", finalBalance)
	}
}

func readBalance(t *testing.T, db *sql.DB, id string) int64 {
	t.Helper()
	var b int64
	db.QueryRow("SELECT balance FROM wallets WHERE id = ?", id).Scan(&b)
	return b
}

func TestListWallets(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	for i := 0; i < 3; i++ {
		resp, body, err := doJSON(http.MethodPost, ts.URL+"/wallets", map[string]string{"currency": "USD"})
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("create wallet %d: err=%v status=%d body=%+v", i, err, resp.StatusCode, body)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/wallets", nil)
	req.Header.Set("X-API-Key", os.Getenv("API_KEY"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var wallets []map[string]any
	if err := json.Unmarshal(raw, &wallets); err != nil {
		t.Fatalf("wallets not a JSON array: %s", raw)
	}
	if len(wallets) != 3 {
		t.Errorf("expected 3 wallets, got %d", len(wallets))
	}
	for _, w := range wallets {
		if w["currency"] != "USD" || w["balance"] != float64(0) {
			t.Errorf("unexpected wallet shape: %v", w)
		}
	}

	// limit works
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/wallets?limit=2", nil)
	req2.Header.Set("X-API-Key", os.Getenv("API_KEY"))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("list limit: %v", err)
	}
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(resp2.Body)
	var limited []map[string]any
	if err := json.Unmarshal(raw2, &limited); err != nil {
		t.Fatalf("limit wallets not a JSON array: %s", raw2)
	}
	if len(limited) != 2 {
		t.Errorf("expected 2 wallets with limit=2, got %d", len(limited))
	}
}
