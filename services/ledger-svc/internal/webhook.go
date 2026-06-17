package internal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type WebhookEvent struct {
	ID        string
	EventType string
	Payload   json.RawMessage
	Status    string
	Attempts  int
}

type WebhookWorker struct {
	db           *sql.DB
	httpClient   *http.Client
	stopCh       chan struct{}
	doneCh       chan struct{}
	webhookURL   string
	eventURLs    map[string]string
	maxRetries   int
	pollInterval time.Duration
}

type WebhookWorkerConfig struct {
	WebhookURL   string
	EventURLs    map[string]string
	MaxRetries   int
	PollInterval time.Duration
}

func NewWebhookWorker(db *sql.DB, cfg WebhookWorkerConfig) *WebhookWorker {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 5
	}

	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}

	return &WebhookWorker{
		db:           db,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		webhookURL:   cfg.WebhookURL,
		eventURLs:    cfg.EventURLs,
		maxRetries:   cfg.MaxRetries,
		pollInterval: cfg.PollInterval,
	}
}

func (w *WebhookWorker) Start() {
	go w.run()
	slog.Info("webhook worker started")
}

func (w *WebhookWorker) Stop() {
	close(w.stopCh)
	<-w.doneCh
	slog.Info("webhook worker stopped")
}

func (w *WebhookWorker) run() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	defer close(w.doneCh)

	for {
		select {
		case <-ticker.C:
			w.processPending()

		case <-w.stopCh:
			return
		}
	}
}

func (w *WebhookWorker) processPending() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `
		SELECT id, event_type, payload, status, attempts
		FROM webhook_queue
		WHERE status = 'pending'
		  AND attempts < $1
		ORDER BY created_at ASC
		LIMIT 10
	`

	rows, err := w.db.QueryContext(ctx, query, w.maxRetries)
	if err != nil {
		slog.Error("webhook worker query failed", "error", err)
		return
	}
	defer rows.Close()

	var events []WebhookEvent

	for rows.Next() {
		var ev WebhookEvent

		if err := rows.Scan(
			&ev.ID,
			&ev.EventType,
			&ev.Payload,
			&ev.Status,
			&ev.Attempts,
		); err != nil {
			slog.Error("scan webhook row", "error", err)
			continue
		}

		events = append(events, ev)
	}

	if err := rows.Err(); err != nil {
		slog.Error("rows iteration error", "error", err)
		return
	}

	for _, ev := range events {
		w.deliverWebhook(ev)
	}
}

func (w *WebhookWorker) deliverWebhook(ev WebhookEvent) {
	url := w.webhookURL

	if custom, ok := w.eventURLs[ev.EventType]; ok && custom != "" {
		url = custom
	}

	if url == "" {
		slog.Warn("no webhook URL configured", "event_id", ev.ID)
		w.markFailed(ev.ID, "no webhook url configured")
		return
	}

	req, err := http.NewRequest(
		http.MethodPost,
		url,
		bytes.NewReader(ev.Payload),
	)
	if err != nil {
		slog.Error(
			"create webhook request failed",
			"event_id", ev.ID,
			"error", err,
		)

		w.recordFailure(ev.ID, ev.Attempts+1, err.Error())
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-ID", ev.ID)
	req.Header.Set("X-Event-Type", ev.EventType)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		slog.Error(
			"webhook delivery failed",
			"event_id", ev.ID,
			"attempt", ev.Attempts+1,
			"error", err,
		)

		w.recordFailure(ev.ID, ev.Attempts+1, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info(
			"webhook delivered successfully",
			"event_id", ev.ID,
			"status", resp.StatusCode,
		)

		w.markCompleted(ev.ID)
		return
	}

	errMsg := fmt.Sprintf("HTTP %d", resp.StatusCode)

	slog.Warn(
		"webhook returned non-success status",
		"event_id", ev.ID,
		"status", resp.StatusCode,
	)

	w.recordFailure(ev.ID, ev.Attempts+1, errMsg)
}

func (w *WebhookWorker) recordFailure(
	id string,
	newAttempts int,
	reason string,
) {
	if newAttempts >= w.maxRetries {
		w.markFailed(id, reason)
		return
	}

	_, err := w.db.Exec(`
		UPDATE webhook_queue
		SET attempts = $1,
		    status = 'pending',
		    error = $2,
		    updated_at = NOW()
		WHERE id = $3
	`, newAttempts, reason, id)

	if err != nil {
		slog.Error(
			"update webhook attempt failed",
			"event_id", id,
			"error", err,
		)
	}
}

func (w *WebhookWorker) markCompleted(id string) {
	_, err := w.db.Exec(`
		UPDATE webhook_queue
		SET status = 'completed',
		    updated_at = NOW()
		WHERE id = $1
	`, id)

	if err != nil {
		slog.Error(
			"mark webhook completed failed",
			"event_id", id,
			"error", err,
		)
	}
}

func (w *WebhookWorker) markFailed(id string, reason string) {
	_, err := w.db.Exec(`
		UPDATE webhook_queue
		SET status = 'failed',
		    error = $1,
		    updated_at = NOW()
		WHERE id = $2
	`, reason, id)

	if err != nil {
		slog.Error(
			"mark webhook failed",
			"event_id", id,
			"error", err,
		)
	}
}

func EnqueueWebhook(
	db *sql.DB,
	eventType string,
	payload interface{},
) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	id := generateUUID()

	_, err = db.Exec(`
		INSERT INTO webhook_queue (
			id,
			event_type,
			payload,
			status,
			attempts,
			created_at,
			updated_at
		)
		VALUES (
			$1,
			$2,
			$3,
			'pending',
			0,
			NOW(),
			NOW()
		)
	`, id, eventType, payloadBytes)

	if err != nil {
		return fmt.Errorf("insert webhook queue: %w", err)
	}

	return nil
}

func generateUUID() string {
	return uuid.NewString()
}