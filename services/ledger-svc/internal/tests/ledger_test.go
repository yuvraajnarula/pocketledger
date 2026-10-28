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
	CREATE TABLE IF NOT EXISTS audit_notes (
		id TEXT PRIMARY KEY,
		entity_type TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		actor TEXT NOT NULL,
		action TEXT NOT NULL,
		note TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS webhook_queue (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL,
		status TEXT DEFAULT 'pending',
		attempts INTEGER DEFAULT 0,
		error TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS fx_rates (
		base_currency TEXT,
		target_currency TEXT,
		rate REAL,
		PRIMARY KEY (base_currency, target_currency)
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
	t.Setenv("PROOF_SECRET_KEY", "proof-test-secret")
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO tenants (id, name, status, scopes) VALUES ('t1', 'test-tenant', 'active', '["admin","transfer"]')`)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	_, err = tx.Exec(`INSERT INTO api_keys (id, tenant_id, name, key_hash, scopes, status) VALUES ('k1', 't1', 'test-key', ?, '["admin","transfer"]', 'active')`, domain.NewAPIKeyVerifier("test-secret").KeyHash("test-ledger-api-key"))
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

func seedFX(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO fx_rates (base_currency, target_currency, rate) VALUES ('USD', 'EUR', 0.5)`); err != nil {
		t.Fatalf("seed fx: %v", err)
	}
}

func seedWalletWithCurrency(t *testing.T, db *sql.DB, id, currency string, balance int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO wallets (id, currency, balance, version, created_at, updated_at) VALUES (?, ?, ?, 0, datetime('now'), datetime('now'))`, id, currency, balance); err != nil {
		t.Fatalf("seed wallet %s: %v", id, err)
	}
	if _, err := db.Exec(`INSERT INTO ledger_entries (id, wallet_id, entry_type, amount, reference_id, created_at) VALUES (?, ?, 'CREDIT', ?, 'initial', datetime('now'))`, id+"-init", id, balance); err != nil {
		t.Fatalf("seed ledger for %s: %v", id, err)
	}
}

// rawGet does a request and returns the raw body, for non-JSON responses.
func rawRequest(t *testing.T, method, url string, body io.Reader, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if k, ok := apiKeyHeader(); ok {
		req.Header.Set("X-API-Key", k)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func apiKeyHeader() (string, bool) {
	k := os.Getenv("API_KEY")
	return k, k != ""
}

func TestBalanceProof(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWallets(t, db, "alice", "bob", 10000, 5000)

	resp, body, err := doJSON(http.MethodGet, ts.URL+"/wallets/alice/proof?nonce=hotel-golf-7", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("proof: err=%v status=%d body=%+v", err, resp.StatusCode, body)
	}
	proof, _ := body["proof"].(string)
	if len(proof) != 64 { // hex-encoded SHA-256
		t.Errorf("expected 64-char hex proof, got %q", proof)
	}
	if bal, ok := body["balance"].(float64); !ok || bal != 10000 {
		t.Errorf("expected balance 10000 in proof payload, got %v (%v)", body["balance"], ok)
	}

	// Unknown wallet → 404.
	resp, _, err = doJSON(http.MethodGet, ts.URL+"/wallets/ghost/proof?nonce=x", nil)
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown wallet, got %d", resp.StatusCode)
	}
}

func TestGeneratePostMortem(t *testing.T) {
	ts, _ := newLedgerTestServer(t)
	defer ts.Close()

	resp, raw := rawRequest(t, http.MethodGet, ts.URL+"/admin/postmortem", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, raw)
	}
	if !bytes.Contains(raw, []byte("Incident Post-Mortem")) || !bytes.Contains(raw, []byte("Ledger Summary")) {
		t.Errorf("postmortem body missing expected sections: %s", raw)
	}
}

func TestCreateDispute(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWallets(t, db, "alice", "bob", 10000, 5000)

	_, body, err := doJSON(http.MethodPost, ts.URL+"/wallets/alice/transfer",
		map[string]any{"destination_id": "bob", "amount": 100, "reference_id": "disp-ref1"}, "idem-disp")
	if err != nil || body["status"] != "transferred" {
		t.Fatalf("transfer failed: %v %v", err, body)
	}

	resp, body, err := doJSON(http.MethodPost, ts.URL+"/admin/dispute",
		map[string]any{"reference_id": "disp-ref1", "wallet_id": "bob", "reason": "fraud reported"}, "idem-dispute")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("dispute failed: %v %d %v", err, resp.StatusCode, body)
	}

	// Reversal entry should exist for the debited amount.
	var amount int64
	err = db.QueryRow(`SELECT amount FROM ledger_entries WHERE reference_id = 'DISPUTE_REVERSAL_disp-ref1' AND entry_type = 'CREDIT'`).Scan(&amount)
	if err != nil {
		t.Fatalf("reversal entry missing: %v", err)
	}
	if amount != 100 {
		t.Errorf("expected reversal amount 100, got %d", amount)
	}

	// Unknown reference → 400.
	resp, _, _ = doJSON(http.MethodPost, ts.URL+"/admin/dispute",
		map[string]any{"reference_id": "no-such-ref", "wallet_id": "bob"}, "idem-dispute-bad")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown reference, got %d", resp.StatusCode)
	}
}

func TestFXTransfer(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWalletWithCurrency(t, db, "alice", "USD", 10000)
	seedWalletWithCurrency(t, db, "bob", "EUR", 0)
	seedFX(t, db)

	resp, body, err := doJSON(http.MethodPost, ts.URL+"/wallets/alice/transfer/fx",
		map[string]any{"destination_id": "bob", "amount": 100, "reference_id": "fx-1"}, "idem-fx-1")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("fx transfer failed: %v %d %v", err, resp.StatusCode, body)
	}
	if dst, ok := body["destination_amount"].(float64); !ok || dst != 50 {
		t.Errorf("expected destination_amount 50, got %v", body["destination_amount"])
	}

	var bal int64
	db.QueryRow("SELECT balance FROM wallets WHERE id='bob'").Scan(&bal)
	if bal != 50 {
		t.Errorf("expected bob balance 50, got %d", bal)
	}

	_, body, _ = doJSON(http.MethodGet, ts.URL+"/reconcile", nil)
	if body["status"] != "balanced" {
		t.Errorf("ledger not balanced after fx transfer: %v", body)
	}

	// Missing fx rate (USD→GBP) → 400.
	seedWalletWithCurrency(t, db, "carol", "GBP", 0)
	resp, _, _ = doJSON(http.MethodPost, ts.URL+"/wallets/alice/transfer/fx",
		map[string]any{"destination_id": "carol", "amount": 10}, "idem-fx-2")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing fx rate, got %d", resp.StatusCode)
	}
}

func TestCurrencyMismatchGuard(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWalletWithCurrency(t, db, "alice", "USD", 10000)
	seedWalletWithCurrency(t, db, "bob", "EUR", 0)
	seedFX(t, db)

	resp, _, _ := doJSON(http.MethodPost, ts.URL+"/wallets/alice/transfer",
		map[string]any{"destination_id": "bob", "amount": 100}, "idem-mismatch")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for cross-currency plain transfer, got %d", resp.StatusCode)
	}
}

func TestWebhookDeliveries(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	if _, err := db.Exec(`INSERT INTO webhook_queue (id, event_type, payload, status, attempts, error) VALUES ('wh-1', 'transfer.completed', '{"ref":"fx-1"}', 'completed', 1, '')`); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/webhooks/deliveries", nil)
	if k, ok := apiKeyHeader(); ok {
		req.Header.Set("X-API-Key", k)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var deliveries []map[string]any
	if err := json.Unmarshal(raw, &deliveries); err != nil {
		t.Fatalf("webhook deliveries not a JSON array: %s", raw)
	}
	if len(deliveries) != 1 || deliveries[0]["event_type"] != "transfer.completed" {
		t.Errorf("unexpected deliveries: %v", deliveries)
	}
}

func TestIdempotencyKeyExpiry(t *testing.T) {
	ts, db := newLedgerTestServer(t)
	defer ts.Close()

	seedWallets(t, db, "alice", "bob", 10000, 0)

	// Seed the same key twice? No — one expired COMPLETED key only.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO idempotency_keys (key, status, response_code, response_body, created_at, expires_at) VALUES ('idem-exp-1', 'COMPLETED', 200, '', ?, ?)`,
		"2024-01-01 00:00:00", "2024-01-02 00:00:00")
	if err != nil {
		t.Fatalf("seed expired idem key: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	resp, body, err := doJSON(http.MethodPost, ts.URL+"/wallets/alice/transfer",
		map[string]any{"destination_id": "bob", "amount": 100, "reference_id": "ref-exp-1"}, "idem-exp-1")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected expired key to allow fresh execution, got %d %v %v", resp.StatusCode, body, err)
	}

	var bal int64
	db.QueryRow("SELECT balance FROM wallets WHERE id='bob'").Scan(&bal)
	if bal != 100 {
		t.Errorf("expected bob balance 100 after replay of expired key, got %d", bal)
	}
}

func TestAdminTenantAndKeys(t *testing.T) {
	ts, _ := newLedgerTestServer(t)
	defer ts.Close()

	// Switch to store-based auth for this test: the middleware attaches a
	// RequestContext (with scopes) only on the store path.
	t.Setenv("API_KEY_STORE_DISABLED", "")

	// 1. Create tenant.
	resp, body, err := doJSON(http.MethodPost, ts.URL+"/admin/tenants",
		map[string]any{"name": "acme"}, "idem-tenant-1")
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create tenant: err=%v status=%d body=%v", err, resp.StatusCode, body)
	}
	tenantID, _ := body["id"].(string)
	if tenantID == "" {
		t.Fatalf("tenant id missing: %v", body)
	}

	// 2. Issue a non-admin key.
	resp, body, err = doJSON(http.MethodPost, ts.URL+"/admin/tenants/"+tenantID+"/keys",
		map[string]any{"name": "svc-key", "scopes": []string{"transfer"}}, "idem-key-1")
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create key: err=%v status=%d body=%v", err, resp.StatusCode, body)
	}
	rawKey, _ := body["raw_key"].(string)
	keyID, _ := body["id"].(string)
	if rawKey == "" || keyID == "" {
		t.Fatalf("raw_key/id missing from key response: %v", body)
	}
	if _, leaked := body["key_hash"]; leaked {
		t.Errorf("key_hash must never be returned: %v", body)
	}

	// 3. The raw key authenticates against a protected endpoint.
	resp, raw := rawRequest(t, http.MethodGet, ts.URL+"/reconcile", nil,
		map[string]string{"X-API-Key": rawKey})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw key should authenticate, got %d body=%s", resp.StatusCode, raw)
	}

	// 4. Non-admin scope cannot use the admin API.
	resp, _ = rawRequest(t, http.MethodGet, ts.URL+"/admin/tenants", nil,
		map[string]string{"X-API-Key": rawKey})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin key on admin API, got %d", resp.StatusCode)
	}

	// 5. Revoke, then the key stops authenticating.
	resp, body, err = doJSON(http.MethodPost, ts.URL+"/admin/keys/"+keyID+"/revoke", nil, "idem-revoke-1")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke failed: %v %d %v", err, resp.StatusCode, body)
	}
	resp, _ = rawRequest(t, http.MethodGet, ts.URL+"/reconcile", nil,
		map[string]string{"X-API-Key": rawKey})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for revoked key, got %d", resp.StatusCode)
	}

	// 6. Listing tenants works with the scoped admin key and includes acme.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/tenants", nil)
	if k, ok := apiKeyHeader(); ok {
		req.Header.Set("X-API-Key", k)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list tenants: %v", err)
	}
	defer resp.Body.Close()
	listRaw, _ := io.ReadAll(resp.Body)
	var tenants []map[string]any
	if err := json.Unmarshal(listRaw, &tenants); err != nil {
		t.Fatalf("tenants not a JSON array: %s", listRaw)
	}
	found := false
	for _, tv := range tenants {
		if tv["id"] == tenantID && tv["name"] == "acme" {
			found = true
		}
	}
	if !found {
		t.Errorf("acme tenant missing from list: %v", tenants)
	}

	// 7. Duplicate tenant ID → 409.
	resp, _, _ = doJSON(http.MethodPost, ts.URL+"/admin/tenants",
		map[string]any{"id": "t1", "name": "dup"}, "idem-tenant-2")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 for duplicate tenant, got %d", resp.StatusCode)
	}
}
