	package internal

	import (
		"context"
		"net/http"
		"sync"
		"time"

		"github.com/go-redis/redis_rate/v10"
		"github.com/redis/go-redis/v9"
		"golang.org/x/time/rate"
	)

	// RateLimiter interface
	type RateLimiter interface {
		Allow(walletID string) bool
		Middleware(next http.Handler) http.Handler
	}

	// LocalRateLimiter uses in-memory token buckets
	type LocalRateLimiter struct {
		mu           sync.RWMutex
		limiters     map[string]*rate.Limiter
		defaultRPS   float64
		defaultBurst int
		perWallet    map[string]struct {
			rps   float64
			burst int
		}
	}

	func NewLocalRateLimiter(cfg *PolicyConfig) *LocalRateLimiter {
		rl := &LocalRateLimiter{
			limiters:     make(map[string]*rate.Limiter),
			defaultRPS:   cfg.RateLimits.Default.RequestsPerSecond,
			defaultBurst: cfg.RateLimits.Default.Burst,
			perWallet: make(map[string]struct {
				rps   float64
				burst int
			}),
		}

		for id, limits := range cfg.RateLimits.PerWallet {
			rl.perWallet[id] = struct {
				rps   float64
				burst int
			}{
				rps:   limits.RequestsPerSecond,
				burst: limits.Burst,
			}
		}

		return rl
	}

	func (rl *LocalRateLimiter) getLimiter(walletID string) *rate.Limiter {
		rl.mu.RLock()
		lim, ok := rl.limiters[walletID]
		rl.mu.RUnlock()

		if ok {
			return lim
		}

		rl.mu.Lock()
		defer rl.mu.Unlock()

		lim, ok = rl.limiters[walletID]
		if ok {
			return lim
		}

		rps := rl.defaultRPS
		burst := rl.defaultBurst

		if ov, ok := rl.perWallet[walletID]; ok {
			rps = ov.rps
			burst = ov.burst
		}

		lim = rate.NewLimiter(rate.Limit(rps), burst)
		rl.limiters[walletID] = lim

		return lim
	}

	func (rl *LocalRateLimiter) Allow(walletID string) bool {
		return rl.getLimiter(walletID).Allow()
	}

	func (rl *LocalRateLimiter) Middleware(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			walletID := extractWalletID(r.URL.Path)

			if walletID == "" {
				next.ServeHTTP(w, r)
				return
			}

			if !rl.Allow(walletID) {
				http.Error(
					w,
					`{"error":"rate limit exceeded"}`,
					http.StatusTooManyRequests,
				)
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	// RedisRateLimiter uses Redis for distributed rate limiting
	type RedisRateLimiter struct {
		limiter      *redis_rate.Limiter
		defaultRPS   float64
		defaultBurst int
		perWallet    map[string]struct {
			rps   float64
			burst int
		}
	}

	func NewRedisRateLimiter(
		redisAddr string,
		cfg *PolicyConfig,
	) *RedisRateLimiter {
		rdb := redis.NewClient(&redis.Options{
			Addr: redisAddr,
		})

		limiter := redis_rate.NewLimiter(rdb)

		rl := &RedisRateLimiter{
			limiter:      limiter,
			defaultRPS:   cfg.RateLimits.Default.RequestsPerSecond,
			defaultBurst: cfg.RateLimits.Default.Burst,
			perWallet: make(map[string]struct {
				rps   float64
				burst int
			}),
		}

		for id, limits := range cfg.RateLimits.PerWallet {
			rl.perWallet[id] = struct {
				rps   float64
				burst int
			}{
				rps:   limits.RequestsPerSecond,
				burst: limits.Burst,
			}
		}

		return rl
	}

	func (rl *RedisRateLimiter) Allow(walletID string) bool {
		rps := rl.defaultRPS
		burst := rl.defaultBurst

		if ov, ok := rl.perWallet[walletID]; ok {
			rps = ov.rps
			burst = ov.burst
		}

		res, err := rl.limiter.Allow(
			context.Background(),
			walletID,
			redis_rate.Limit{
				Rate:   int(rps),
				Burst:  burst,
				Period: time.Second,
			},
		)

		if err != nil {
			// fallback to allow on redis failure
			return true
		}

		return res.Allowed > 0
	}

	func (rl *RedisRateLimiter) Middleware(
		next http.Handler,
	) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			walletID := extractWalletID(r.URL.Path)

			if walletID == "" {
				next.ServeHTTP(w, r)
				return
			}

			if !rl.Allow(walletID) {
				http.Error(
					w,
					`{"error":"rate limit exceeded"}`,
					http.StatusTooManyRequests,
				)
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	func extractWalletID(path string) string {
		// assuming path starts with /wallets/
		if len(path) < 9 {
			return ""
		}

		remain := path[9:] // after "/wallets/"

		for i, ch := range remain {
			if ch == '/' {
				return remain[:i]
			}
		}

		return remain
	}