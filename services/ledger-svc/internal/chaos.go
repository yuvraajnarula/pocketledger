package internal

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"
)

type ChaosConfig struct {
	FailureProbability float64 // fraction of non-admin requests to hard-fail
	MinLatencyMs       int     // always injected when chaos is on
	MaxLatencyMs       int     // upper bound for the random extra latency
}

// ChaosController is safe for concurrent use; embed one per server instance.
type ChaosController struct {
	enabled atomic.Bool
	cfg     ChaosConfig
}

func NewChaosController() *ChaosController {
	return &ChaosController{
		cfg: ChaosConfig{
			FailureProbability: 0.30,
			MinLatencyMs:       200,
			MaxLatencyMs:       1500,
		},
	}
}

func (c *ChaosController) Enable()         { c.enabled.Store(true) }
func (c *ChaosController) Disable()        { c.enabled.Store(false) }
func (c *ChaosController) IsEnabled() bool { return c.enabled.Load() }

func ChaosMiddleware(ctrl *ChaosController) func(http.Handler) http.Handler {
	exempt := map[string]bool{
		"/health":      true,
		"/admin/chaos": true,
		"/metrics":     true,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt[r.URL.Path] || !ctrl.IsEnabled() {
				next.ServeHTTP(w, r)
				return
			}

			// 1. Latency injection — always fires when chaos is on.
			jitter := ctrl.cfg.MinLatencyMs
			if span := ctrl.cfg.MaxLatencyMs - ctrl.cfg.MinLatencyMs; span > 0 {
				jitter += rand.Intn(span)
			}
			time.Sleep(time.Duration(jitter) * time.Millisecond)

			// 2. Random hard failure.
			if rand.Float64() < ctrl.cfg.FailureProbability {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Chaos-Injected", "fault")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "chaos: simulated fault",
					"code":  "CHAOS_FAULT",
				})
				return
			}

			// Request survived — still mark it so metrics can track chaos exposure.
			w.Header().Set("X-Chaos-Injected", "latency")
			next.ServeHTTP(w, r)
		})
	}
}

// ─── Admin Handler ────────────────────────────────────────────────────────────

type chaosToggleRequest struct {
	Enabled bool `json:"enabled"`
}

type chaosStatusResponse struct {
	Enabled bool        `json:"enabled"`
	Config  ChaosConfig `json:"config"`
}

// HandleChaosToggle handles POST /admin/chaos.
// Body: { "enabled": true|false }
// Response: { "enabled": bool, "config": { ... } }
func HandleChaosToggle(ctrl *ChaosController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chaosToggleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
			return
		}

		if req.Enabled {
			ctrl.Enable()
		} else {
			ctrl.Disable()
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chaosStatusResponse{
			Enabled: ctrl.IsEnabled(),
			Config:  ctrl.cfg,
		})
	}
}
