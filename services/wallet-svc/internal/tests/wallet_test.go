package tests

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/yuvraajnarula/pocketledger/services/wallet-svc/internal"
)

func newTestServer(t *testing.T) *httptest.Server {
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
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}

	handler := internal.NewHandler(db, &http.Client{})
	mux := http.NewServeMux()
	handler.Register(mux)
	mux.HandleFunc("GET /health", handler.Health)

	return httptest.NewServer(mux)
}

func doJSON(method, url string, body any) (*http.Response, map[string]any, error) {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return nil, nil, err
		}
	}
	req, _ := http.NewRequest(method, url, &b)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	bodyBytes, _ := io.ReadAll(resp.Body)
	json.Unmarshal(bodyBytes, &result)
	return resp, result, nil
}

func TestWalletLifecycle(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Create wallet
	resp, body, err := doJSON(http.MethodPost, ts.URL+"/wallets", map[string]string{"currency": "USD"})
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create wallet: %v %v", err, body)
	}
	walletID, ok := body["id"].(string)
	if !ok {
		t.Fatalf("wallet id missing: %v", body)
	}

	// Deposit 5000
	resp, _, err = doJSON(http.MethodPost, fmt.Sprintf("%s/wallets/%s/deposit", ts.URL, walletID),
		map[string]any{"amount": 5000, "reference_id": "dep1"})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("deposit: %v", err)
	}

	// Withdraw 2000 -> ok
	resp, _, err = doJSON(http.MethodPost, fmt.Sprintf("%s/wallets/%s/withdraw", ts.URL, walletID),
		map[string]any{"amount": 2000, "reference_id": "wd1"})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("withdraw: %v", err)
	}

	// Withdraw 4000 -> insufficient funds (402)
	resp, _, _ = doJSON(http.MethodPost, fmt.Sprintf("%s/wallets/%s/withdraw", ts.URL, walletID),
		map[string]any{"amount": 4000, "reference_id": "wd2"})
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("expected 402, got %d", resp.StatusCode)
	}

	// Invalid currency
	resp, _, _ = doJSON(http.MethodPost, ts.URL+"/wallets", map[string]string{"currency": "XX"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}