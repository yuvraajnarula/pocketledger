
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;

CREATE TABLE IF NOT EXISTS wallets (
    id         TEXT PRIMARY KEY,
    currency   TEXT NOT NULL CHECK (LENGTH(currency) = 3),
    balance    INTEGER NOT NULL DEFAULT 0 CHECK (balance >= 0),
    version    INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS ledger_entries (
    id          TEXT PRIMARY KEY,
    wallet_id   TEXT NOT NULL REFERENCES wallets(id),
    entry_type  TEXT NOT NULL CHECK (entry_type IN ('DEBIT', 'CREDIT')),
    amount      INTEGER NOT NULL CHECK (amount > 0),
    reference_id TEXT,
    created_at  DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_ledger_wallet ON ledger_entries(wallet_id, created_at DESC);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key           TEXT PRIMARY KEY,
    status        TEXT NOT NULL CHECK (status IN ('PENDING', 'COMPLETED', 'FAILED')),
    request_hash  TEXT,
    response_code INTEGER,
    response_body TEXT,
    created_at    DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at    DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+24 hours'))
);

CREATE INDEX IF NOT EXISTS idx_idem_expires ON idempotency_keys(expires_at) WHERE status = 'PENDING';

CREATE TABLE IF NOT EXISTS event_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    aggregate_id TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      TEXT NOT NULL,
    created_at   DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_event_aggregate ON event_log(aggregate_id, created_at);

CREATE TABLE IF NOT EXISTS fx_rates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    base_currency TEXT NOT NULL,
    target_currency TEXT NOT NULL,
    rate REAL NOT NULL,
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(base_currency, target_currency)
);

-- Insert sample rates
INSERT OR REPLACE INTO fx_rates (base_currency, target_currency, rate) VALUES
('USD', 'INR', 83.50),
('USD', 'EUR', 0.92),
('USD', 'GBP', 0.79);

-- Webhook Queue table
CREATE TABLE IF NOT EXISTS webhook_queue (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL,
    status TEXT CHECK (status IN ('PENDING', 'SENT', 'FAILED', 'RETRY')) DEFAULT 'PENDING',
    attempts INTEGER DEFAULT 0,
    last_attempt_at DATETIME,
    next_retry_at DATETIME,
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_webhook_status ON webhook_queue(status, next_retry_at);