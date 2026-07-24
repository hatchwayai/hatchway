package api

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter is an in-memory token bucket keyed by caller identity.
// It is per-process only and is not a distributed multi-replica limit.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     int // requests per minute
	capacity int // max tokens
	cleanup  time.Duration
	lastGC   time.Time
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewRateLimiter constructs a limiter whose burst capacity equals one minute
// of configured traffic.
func NewRateLimiter(requestsPerMinute int) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     requestsPerMinute,
		capacity: requestsPerMinute,
		cleanup:  5 * time.Minute,
		lastGC:   time.Now(),
	}
	return rl
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	rl.cleanupStale(now)
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rl.capacity), lastRefill: now}
		rl.buckets[key] = b
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * float64(rl.rate) / 60.0
	if b.tokens > float64(rl.capacity) {
		b.tokens = float64(rl.capacity)
	}
	b.lastRefill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *RateLimiter) cleanupStale(now time.Time) {
	if now.Sub(rl.lastGC) < rl.cleanup {
		return
	}
	cutoff := now.Add(-rl.cleanup)
	for key, b := range rl.buckets {
		if b.lastRefill.Before(cutoff) {
			delete(rl.buckets, key)
		}
	}
	rl.lastGC = now
}

// RateLimitMiddleware limits authenticated tunnel-creation requests.
func RateLimitMiddleware(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !requestNeedsRateLimit(r) {
				next.ServeHTTP(w, r)
				return
			}

			tokenID := TokenIDFromContext(r.Context())
			if tokenID == "" {
				next.ServeHTTP(w, r)
				return
			}

			if !rl.Allow(tokenID) {
				GlobalMetrics.IncrRateLimitReject()
				WriteError(w, http.StatusTooManyRequests, ErrRateLimited, "rate limit exceeded")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func requestNeedsRateLimit(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/tunnels"
}
