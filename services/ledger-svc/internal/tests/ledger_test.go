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

	"github.com/yuvraajnarula/pocketledger/services/ledger-svc/internal"
	"github.com/yuvraajnarula/pocketledger/shared/domain"
)

func newLedgerTestServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	schema := `
	CREATE TABLE IF NOT EXISTS wallets (
		id TEXT PRIMARY KEY, currency TEXT, balance INTEGER DEFAULT 0,
		version INTEGER DEFAULT 0, created_at DATETIME, updated_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS ledger_entries (
		id TEXT PRIMARY KEY, wallet_id TEXT, entry_type TEXT, amount INTEGER,
		reference_id TEXT, created_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS idempotency_keys (
		key TEXT PRIMARY KEY, status TEXT, request_hash TEXT,
		response_code INTEGER, response_body TEXT, created_at DATETIME, expires_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS event_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, aggregate_id TEXT,
		event_type TEXT, payload TEXT, created_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS tenants (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		scopes TEXT NOT NULL DEFAULT '[]',
		rate_limit_rps REAL NOT NULL DEFAULT 100,
		rate_limit_burst INTEGER NOT NULL DEFAULT 100,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		revoked_at DATETIME
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}

	cfg := &internal.PolicyConfig{}
	cfg.RateLimits.Default.RequestsPerSecond = 1000
	cfg.RateLimits.Default.Burst = 1000
	rl := internal.NewLocalRateLimiter(cfg)
	authStore := internal.NewAPIKeyStore(db, domain.NewAPIKeyVerifier("test-secret"))
	t.Setenv("API_KEY_STORE_DISABLED", "1")
	t.Setenv("API_KEY", "test-ledger-api-key")
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO tenants (id, name, status, scopes) VALUES ('t1', 'test-tenant', 'active', '[]')`)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	_, err = tx.Exec(`INSERT INTO api_keys (id, tenant_id, name, key_hash, scopes, status) VALUES ('k1', 't1', 'test-key', ?, '[]', 'active')`, domain.NewAPIKeyVerifier("test-secret").KeyHash("test-ledger-api-key"))
	if err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	handler := internal.NewHandler(db, rl, nil, authStore)
	idemStore := internal.NewIdempotencyStore(db)
	idemMiddleware := internal.NewIdempotencyMiddleware(idemStore)

	mux := http.NewServeMux()
	handler.Register(mux)

	protected := internal.AuthMiddleware(authStore)(idemMiddleware.Handle(mux))
	return httptest.NewServer(protected), db
}

func doJSON(method, url string, body any, idemKey ...string) (*http.Response, map[string]any, error) {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return nil, nil, err
		}
	}
	req, _ := http.NewRequest(method, url, &b)
	req.Header.Set("Content-Type", "application/json")
	if len(idemKey) > 0 && idemKey[0] != "" {
		req.Header.Set("Idempotency-Key", idemKey[0])
	}
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
	json.Unmarshal(bodyBytes, &result)
	return resp, result, nil
}
func seedWallets(t *testing.T, db *sql.DB, src, dest string, srcBal, destBal int64) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback()
	// Insert wallets
	_, err = tx.Exec(`INSERT INTO wallets (id, currency, balance, version, created_at, updated_at)
		VALUES (?, 'USD', ?, 0, datetime('now'), datetime('now')),
		       (?, 'USD', ?, 0, datetime('now'), datetime('now'))`,
		src, srcBal, dest, destBal)
	if err != nil {
		t.Fatalf("seed wallets: %v", err)
	}

	// Seed ledger entries: CREDIT for initial balances (so ledger is balanced)
	_, err = tx.Exec(`INSERT INTO ledger_entries (id, wallet_id, entry_type, amount, reference_id, created_at)
		VALUES (?, ?, 'CREDIT', ?, 'initial', datetime('now')),
		       (?, ?, 'CREDIT', ?, 'initial', datetime('now'))`,
		src+"-init", src, srcBal,
		dest+"-init", dest, destBal)
	if err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

func TestTransferAndReconcile(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWallets(t, db, "alice", "bob", 10000, 5000)

	resp, body, err := doJSON(http.MethodPost, ts.URL+"/wallets/alice/transfer",
		map[string]any{"destination_id": "bob", "amount": 1000, "reference_id": "t1"},
		"idem-transfer-1")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("transfer failed: %v %v", err, body)
	}

	resp, body, err = doJSON(http.MethodGet, ts.URL+"/reconcile", nil)
	if err != nil || body["status"] != "balanced" {
		t.Fatalf("reconcile failed: %v %v", err, body)
	}
}

func TestConcurrentTransfers(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWallets(t, db, "X", "Y", 100000, 100000)

	errCh := make(chan error, 80)
	for i := 0; i < 20; i++ {
		go func(i int) {
			_, _, err := doJSON(http.MethodPost, ts.URL+"/wallets/X/transfer",
				map[string]any{"destination_id": "Y", "amount": 10, "reference_id": fmt.Sprintf("conc-%d", i)},
				fmt.Sprintf("key-XY-%d", i))
			errCh <- err
		}(i)
		go func(i int) {
			_, _, err := doJSON(http.MethodPost, ts.URL+"/wallets/Y/transfer",
				map[string]any{"destination_id": "X", "amount": 10, "reference_id": fmt.Sprintf("conc-%d", i)},
				fmt.Sprintf("key-YX-%d", i))
			errCh <- err
		}(i)
	}

	for i := 0; i < 40; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent error: %v", err)
		}
	}

	var balX, balY int64
	db.QueryRow("SELECT balance FROM wallets WHERE id='X'").Scan(&balX)
	db.QueryRow("SELECT balance FROM wallets WHERE id='Y'").Scan(&balY)
	total := balX + balY
	if total != 200000 {
		t.Errorf("total money drift: expected 200000, got %d (X=%d, Y=%d)", total, balX, balY)
	}
	// Also check that ledger is balanced
	_, body, _ := doJSON(http.MethodGet, ts.URL+"/reconcile", nil)

	if body["status"] != "balanced" {
		t.Errorf("ledger not balanced after concurrent transfers: %v", body)
	}

}

func TestIdempotency(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWallets(t, db, "A", "B", 5000, 0)

	resp1, _, _ := doJSON(http.MethodPost, ts.URL+"/wallets/A/transfer",
		map[string]any{"destination_id": "B", "amount": 500, "reference_id": "idem1"},
		"idem-1")
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first transfer failed: %d", resp1.StatusCode)
	}

	resp2, body2, _ := doJSON(http.MethodPost, ts.URL+"/wallets/A/transfer",
		map[string]any{"destination_id": "B", "amount": 500, "reference_id": "idem1"},
		"idem-1")
	if resp2.StatusCode != http.StatusOK || body2["status"] != "transferred" {
		t.Fatalf("idempotent replay should return 200, got %d %v", resp2.StatusCode, body2)
	}

	resp3, _, _ := doJSON(http.MethodPost, ts.URL+"/wallets/A/transfer",
		map[string]any{"destination_id": "B", "amount": 100, "reference_id": "idem2"},
		"idem-2")
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("second transfer after idempotent key should work: %d", resp3.StatusCode)
	}
}

func TestInvalidTransferCases(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWallets(t, db, "C", "D", 1000, 0)

	resp, _, _ := doJSON(http.MethodPost, ts.URL+"/wallets/C/transfer",
		map[string]any{"amount": 100}, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing dest, got %d", resp.StatusCode)
	}

	resp, _, _ = doJSON(http.MethodPost, ts.URL+"/wallets/C/transfer",
		map[string]any{"destination_id": "D", "amount": 2000}, "idem-insuf")
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("expected 402 for insufficient funds, got %d", resp.StatusCode)
	}

	resp, _, _ = doJSON(http.MethodPost, ts.URL+"/wallets/Z/transfer",
		map[string]any{"destination_id": "D", "amount": 50}, "idem-404")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown source, got %d", resp.StatusCode)
	}
}
