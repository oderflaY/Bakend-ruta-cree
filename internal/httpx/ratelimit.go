package httpx

import (
	"context"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter is a per-key token bucket (key = client IP). Idle buckets are swept periodically,
// so memory stays bounded by the number of recently active clients.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   rate.Limit
	burst   int
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimiter allows `perMinute` requests per minute with bursts of `burst`.
func NewRateLimiter(ctx context.Context, perMinute float64, burst int) *RateLimiter {
	rl := &RateLimiter{buckets: map[string]*bucket{}, limit: rate.Limit(perMinute / 60), burst: burst}
	go rl.sweep(ctx)
	return rl
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(rl.limit, rl.burst)}
		rl.buckets[key] = b
	}
	b.seen = time.Now()
	rl.mu.Unlock()
	return b.lim.Allow()
}

func (rl *RateLimiter) sweep(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-10 * time.Minute)
			rl.mu.Lock()
			for k, b := range rl.buckets {
				if b.seen.Before(cutoff) {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}

func (rl *RateLimiter) Middleware(ips *ClientIP) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rl.Allow(ips.Of(r)) {
				w.Header().Set("Retry-After", "60")
				WriteError(w, r, ErrTooMany)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
