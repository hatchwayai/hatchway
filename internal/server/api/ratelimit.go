package api

import (
	"net/http"
	"sync"
	"time"
)

// In-memory token-bucket rate limiter. Per-process only — not suitable for
// multi-replica deployments without moving to Postgres or Redis.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     int // requests per minute
	capacity int // max tokens
	cleanup  time.Duration
}

type bucket struct {
	tokens   float64
	lastLeak time.Time
}

func NewRateLimiter(requestsPerMinute int) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     requestsPerMinute,
		capacity: requestsPerMinute,
		cleanup:  5 * time.Minute,
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rl.capacity), lastLeak: now}
		rl.buckets[key] = b
	}

	// Leak tokens based on elapsed time
	elapsed := now.Sub(b.lastLeak).Seconds()
	b.tokens += elapsed * float64(rl.rate) / 60.0
	if b.tokens > float64(rl.capacity) {
		b.tokens = float64(rl.capacity)
	}
	b.lastLeak = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-rl.cleanup)
		for k, b := range rl.buckets {
			if b.lastLeak.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
		rl.mu.Unlock()
	}
}

func RateLimitMiddleware(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
