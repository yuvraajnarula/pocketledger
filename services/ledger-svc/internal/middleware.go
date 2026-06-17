package internal

import (
	"bytes"
	"context"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type ResponseRecorder struct {
	http.ResponseWriter
	Body       *bytes.Buffer
	StatusCode int
}

func (r *ResponseRecorder) Write(b []byte) (int, error) {
	r.Body.Write(b)
	if r.StatusCode == 0 {
		r.StatusCode = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *ResponseRecorder) WriteHeader(code int) {
	r.StatusCode = code
	r.ResponseWriter.WriteHeader(code)
}

type IdempotencyMiddleware struct {
	Store *IdempotencyStore
}

func NewIdempotencyMiddleware(store *IdempotencyStore) *IdempotencyMiddleware {
	return &IdempotencyMiddleware{Store: store}
}

func (m *IdempotencyMiddleware) isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (m *IdempotencyMiddleware) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.isMutationMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header required"})
			return
		}

		status, err := m.Store.CheckOrInsert(r.Context(), key)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}

		switch status {
		case "COMPLETED":
			code, body, _ := m.Store.GetCachedResponse(r.Context(), key)
			w.Header().Set("Content-Type", "application/json")
			if code == 0 {
				code = http.StatusOK
			}
			w.WriteHeader(code)
			if body != nil {
				w.Write(body)
			}
			return
		case "PENDING", "FAILED":
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusConflict, map[string]string{"error": "request in progress or recently failed, retry with backoff"})
			return
		}

		// Execute handler, capture response
		rec := &ResponseRecorder{ResponseWriter: w, Body: bytes.NewBuffer(nil)}
		next.ServeHTTP(rec, r)

		// Cache 2xx. Delete PENDING on anything else to allow immediate retry.
		if rec.StatusCode >= 200 && rec.StatusCode < 300 {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = m.Store.CompleteKey(ctx, key, rec.StatusCode, rec.Body.Bytes())
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = m.Store.FailKey(ctx, key)
		}
	})
}

// ChaosConfig injects latency and errors for testing.
type ChaosConfig struct {
	LatencyMin, LatencyMax time.Duration
	ErrorRate              float64 // 0.0 to 1.0
	Enabled                bool
	mu                     sync.RWMutex
}

func NewChaosMiddleware() *ChaosConfig {
	return &ChaosConfig{LatencyMin: 50, LatencyMax: 500, ErrorRate: 0.1}
}

func (c *ChaosConfig) Toggle(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Enabled = on
}

func (c *ChaosConfig) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		enabled := c.Enabled
		c.mu.RUnlock()
		if !enabled {
			next.ServeHTTP(w, r)
			return
		}

		if rand.Float64() < c.ErrorRate {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "chaos_injected"})
			return
		}
		delay := c.LatencyMin + time.Duration(rand.Int63n(int64(c.LatencyMax-c.LatencyMin)))
		time.Sleep(delay)
		next.ServeHTTP(w, r)
	})
}

func NewChaosConfig() *ChaosConfig {
	return &ChaosConfig{
		LatencyMin: 50 * time.Millisecond,
		LatencyMax: 500 * time.Millisecond,
		ErrorRate:  0.1,
	}
}