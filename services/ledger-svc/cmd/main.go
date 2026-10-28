package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/yuvraajnarula/pocketledger/services/ledger-svc/internal"
)

const (
	bankWalletID       = "bank-main"
	initialBankBalance = 1_000_000_000_000
)

func main() {
	// ── Chaos controller ────────────────────────────────────────────────────
	// Created once here so the same pointer goes to both NewHandler (for the
	// /admin/chaos toggle) and ChaosMiddleware (which reads the atomic flag).
	chaos := internal.NewChaosController()

	// ── PostgreSQL ───────────────────────────────────────────────────────────
	pgHost := os.Getenv("PG_HOST")
	pgPort := os.Getenv("PG_PORT")
	pgUser := os.Getenv("PG_USER")
	pgPassword := os.Getenv("PG_PASSWORD")
	pgDB := os.Getenv("PG_DB")
	if pgHost == "" {
		pgHost = "postgres"
	}
	if pgPort == "" {
		pgPort = "5432"
	}
	if pgUser == "" {
		pgUser = "pocketledger"
	}
	if pgPassword == "" {
		slog.Error("PG_PASSWORD environment variable is required")
		os.Exit(1)
	}
	if pgDB == "" {
		pgDB = "pocketledger"
	}

	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		pgHost, pgPort, pgUser, pgPassword, pgDB)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		slog.Error("open postgres", "error", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)

	// ── Schema migrations ────────────────────────────────────────────────────
	schema := `
	CREATE TABLE IF NOT EXISTS wallets (
		id TEXT PRIMARY KEY,
		currency TEXT NOT NULL,
		balance BIGINT DEFAULT 0,
		version INTEGER DEFAULT 0,
		created_at TIMESTAMP,
		updated_at TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS ledger_entries (
		id TEXT PRIMARY KEY,
		wallet_id TEXT,
		entry_type TEXT,
		amount BIGINT,
		reference_id TEXT,
		created_at TIMESTAMP,
		reconciled BOOLEAN DEFAULT TRUE,
		reconciled_at TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS idempotency_keys (
		key TEXT PRIMARY KEY,
		status TEXT,
		request_hash TEXT,
		response_code INTEGER,
		response_body TEXT,
		created_at TIMESTAMP,
		expires_at TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS event_log (
		id SERIAL PRIMARY KEY,
		aggregate_id TEXT,
		event_type TEXT,
		payload TEXT,
		created_at TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS webhook_queue (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL,
		status TEXT DEFAULT 'pending',
		attempts INTEGER DEFAULT 0,
		error TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS fx_rates (
		base_currency TEXT,
		target_currency TEXT,
		rate REAL,
		PRIMARY KEY (base_currency, target_currency)
	);
	INSERT INTO fx_rates (base_currency, target_currency, rate)
	VALUES ('USD', 'EUR', 0.92)
	ON CONFLICT (base_currency, target_currency) DO NOTHING;
	`
	if _, err := db.Exec(schema); err != nil {
		slog.Error("schema migration", "error", err)
		os.Exit(1)
	}

	// ── Bank wallet ──────────────────────────────────────────────────────────
	var exists bool
	if err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM wallets WHERE id = $1)", bankWalletID).Scan(&exists); err != nil {
		slog.Error("check bank wallet", "error", err)
		os.Exit(1)
	}
	if !exists {
		_, err = db.Exec(`
			INSERT INTO wallets (id, currency, balance, version, created_at, updated_at)
			VALUES ($1, 'USD', $2, 1, NOW(), NOW())
		`, bankWalletID, initialBankBalance)
		if err != nil {
			slog.Error("create bank wallet", "error", err)
			os.Exit(1)
		}
		slog.Info("created bank wallet", "id", bankWalletID, "balance", initialBankBalance)
	} else {
		if _, err = db.Exec("UPDATE wallets SET balance = $1 WHERE id = $2", initialBankBalance, bankWalletID); err != nil {
			slog.Error("reset bank balance", "error", err)
		} else {
			slog.Info("reset bank wallet balance", "id", bankWalletID, "balance", initialBankBalance)
		}
	}

	// ── Policy & rate limiter ────────────────────────────────────────────────
	policyPath := os.Getenv("POLICY_CONFIG_PATH")
	if policyPath == "" {
		policyPath = "./infra/policy.yaml"
	}
	policyCfg, err := internal.LoadPolicy(policyPath)
	if err != nil {
		slog.Error("failed to load policy configuration", "error", err)
		os.Exit(1)
	}
	slog.Info("policy configuration loaded", "path", policyPath)

	rateLimiter := internal.NewLocalRateLimiter(policyCfg)

	// ── Handler & metrics ────────────────────────────────────────────────────
	// Pass chaos so handler.ToggleChaos and ChaosMiddleware share the same
	// atomic flag — no coordination needed.
	handler := internal.NewHandler(db, rateLimiter, chaos)

	metrics := internal.NewMetrics()
	metrics.SetDBQuery(func() (int64, int64, error) {
		var wallets, entries int64
		if err := db.QueryRow("SELECT COUNT(*) FROM wallets").Scan(&wallets); err != nil {
			return 0, 0, err
		}
		err := db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entries)
		return wallets, entries, err
	})
	handler.SetMetrics(metrics)

	if wc := handler.WalletClient(); wc != nil {
		reconciler := internal.NewReconciler(db, wc, metrics)
		go reconciler.Run(context.Background())
	}

	// ── Middleware chain ─────────────────────────────────────────────────────
	//
	//   Request → ChaosMiddleware → IdempotencyMiddleware → mux
	//
	// Chaos is outermost so it can short-circuit before idempotency writes
	// anything to the DB. /health, /admin/chaos, and /metrics are exempt
	// inside ChaosMiddleware so the toggle itself is never chaos'd.
	idemStore := internal.NewIdempotencyStore(db)
	idemMiddleware := internal.NewIdempotencyMiddleware(idemStore)

	mux := http.NewServeMux()
	handler.Register(mux)

	finalHandler := internal.ChaosMiddleware(chaos)(idemMiddleware.Handle(mux))

	// ── Webhook worker ───────────────────────────────────────────────────────
	webhookCfg := internal.WebhookWorkerConfig{
		WebhookURL:   os.Getenv("WEBHOOK_URL"),
		EventURLs:    map[string]string{},
		MaxRetries:   5,
		PollInterval: 5 * time.Second,
	}
	webhookWorker := internal.NewWebhookWorker(db, webhookCfg)
	webhookWorker.Start()
	defer webhookWorker.Stop()

	// ── TLS / mTLS ───────────────────────────────────────────────────────────
	certFile := os.Getenv("TLS_CERT_FILE")
	keyFile := os.Getenv("TLS_KEY_FILE")
	caCertFile := os.Getenv("TLS_CA_FILE")

	var srv *http.Server
	if certFile != "" && keyFile != "" && caCertFile != "" {
		caCert, err := os.ReadFile(caCertFile)
		if err != nil {
			slog.Error("failed to read CA cert", "error", err)
			os.Exit(1)
		}
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		srv = &http.Server{
			Addr:    ":8082",
			Handler: finalHandler,
			TLSConfig: &tls.Config{
				ClientAuth: tls.RequireAndVerifyClientCert,
				ClientCAs:  caCertPool,
				MinVersion: tls.VersionTLS12,
			},
		}
		slog.Info("ledger-svc starting with mTLS", "addr", ":8082")
	} else {
		srv = &http.Server{
			Addr:    ":8082",
			Handler: finalHandler,
		}
		slog.Info("ledger-svc starting (plain HTTP)", "addr", ":8082")
	}

	// ── Serve ────────────────────────────────────────────────────────────────
	go func() {
		if certFile != "" && keyFile != "" && caCertFile != "" {
			if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
				slog.Error("server failed", "error", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("server failed", "error", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced shutdown", "error", err)
	}
	slog.Info("server exited")
}