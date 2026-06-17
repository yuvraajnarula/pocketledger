package main

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"log/slog"
	"net/http"
	"time"
	"os"

	_ "modernc.org/sqlite"

	"github.com/yuvraajnarula/pocketledger/services/wallet-svc/internal"
)

func buildLedgerClient() *http.Client {
    certFile := os.Getenv("LEDGER_SVC_CLIENT_CERT_FILE")
    keyFile  := os.Getenv("LEDGER_SVC_CLIENT_KEY_FILE")
    caFile   := os.Getenv("LEDGER_SVC_CA_FILE")

    if certFile == "" || keyFile == "" || caFile == "" {
        return &http.Client{Timeout: 10 * time.Second}
    }

    caCert, err := os.ReadFile(caFile)
    if err != nil {
        slog.Error("failed to read ledger CA cert", "error", err)
        os.Exit(1)
    }
    caCertPool := x509.NewCertPool()
    caCertPool.AppendCertsFromPEM(caCert)

    cert, err := tls.LoadX509KeyPair(certFile, keyFile)
    if err != nil {
        slog.Error("failed to load ledger client cert", "error", err)
        os.Exit(1)
    }

    return &http.Client{
        Timeout: 10 * time.Second,
        Transport: &http.Transport{
            TLSClientConfig: &tls.Config{
                Certificates: []tls.Certificate{cert},
                RootCAs:      caCertPool,
                MinVersion:   tls.VersionTLS12,
            },
        },
    }
}

func main() {
	db, err := sql.Open("sqlite", "file:wallet.db?cache=shared&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		slog.Error("open db", "error", err)
		os.Exit(1)
	}
	// Force single writer to avoid SQLITE_BUSY under concurrent writes
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Apply schema
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
	);
	`
	if _, err := db.Exec(schema); err != nil {
		slog.Error("schema migration", "error", err)
		os.Exit(1)
	}

	handler := internal.NewHandler(db, buildLedgerClient())
	metrics := internal.NewMetrics(func() (int64, int64, error) {
		var wallets, entries int64
		err := db.QueryRow("SELECT COUNT(*) FROM wallets").Scan(&wallets)
		if err != nil {
			return 0, 0, err
		}
		err = db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entries)
		return wallets, entries, err
	})
	handler.SetMetrics(metrics)

	mux := http.NewServeMux()
	handler.Register(mux)
	mux.HandleFunc("GET /health", handler.Health)
	mux.HandleFunc("GET /metrics", handler.Metrics)

	addr := ":8081"
	slog.Info("wallet-svc starting", "addr", addr)
	certFile := os.Getenv("TLS_CERT_FILE")
	keyFile := os.Getenv("TLS_KEY_FILE")
	caCertFile := os.Getenv("TLS_CA_FILE")

	if certFile != "" && keyFile != "" && caCertFile != "" {
		// Load CA cert pool
		caCert, err := os.ReadFile(caCertFile)
		if err != nil {
			slog.Error("failed to read CA cert", "error", err)
			os.Exit(1)
		}
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		tlsConfig := &tls.Config{
			ClientAuth: tls.RequireAndVerifyClientCert,
			ClientCAs:  caCertPool,
			MinVersion: tls.VersionTLS12,
		}
		server := &http.Server{
			Addr:      ":8081",
			Handler:   mux,
			TLSConfig: tlsConfig,
		}
		slog.Info("wallet-svc starting with mTLS", "addr", ":8081")
		if err := server.ListenAndServeTLS(certFile, keyFile); err != nil {
			slog.Error("server failed", "error", err)
		}
	} else {
		slog.Info("wallet-svc starting (plain HTTP)", "addr", ":8081")
		if err := http.ListenAndServe(":8081", mux); err != nil {
			slog.Error("server failed", "error", err)
		}
	}
}