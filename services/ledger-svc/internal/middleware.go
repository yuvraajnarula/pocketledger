package internal

import (
	"bytes"
	"context"
	"net/http"
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
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Idempotency-Key header required",
			})
			return
		}

		status, err := m.Store.CheckOrInsert(r.Context(), key)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "internal server error",
			})
			return
		}

		switch status {
		case "COMPLETED":
			code, body, err := m.Store.GetCachedResponse(r.Context(), key)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "failed to retrieve cached response",
				})
				return
			}

			w.Header().Set("Content-Type", "application/json")

			if code == 0 {
				code = http.StatusOK
			}

			w.WriteHeader(code)

			if body != nil {
				_, _ = w.Write(body)
			}
			return

		case "PENDING", "FAILED":
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "request in progress or recently failed, retry with backoff",
			})
			return
		}

		rec := &ResponseRecorder{
			ResponseWriter: w,
			Body:           bytes.NewBuffer(nil),
		}

		next.ServeHTTP(rec, r)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if rec.StatusCode >= 200 && rec.StatusCode < 300 {
			_ = m.Store.CompleteKey(
				ctx,
				key,
				rec.StatusCode,
				rec.Body.Bytes(),
			)
		} else {
			_ = m.Store.FailKey(ctx, key)
		}
	})
}
