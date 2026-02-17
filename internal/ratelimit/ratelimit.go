// Package ratelimit provides a simple in-memory token bucket rate limiter
// middleware for net/http.
package ratelimit

import (
	"net/http"
	"sync"
	"time"
)

// Limiter is a per-IP token bucket rate limiter.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    int           // tokens added per interval
	burst   int           // max tokens (bucket capacity)
	interval time.Duration // refill interval
}

type bucket struct {
	tokens   int
	lastFill time.Time
}

// New creates a rate limiter. rate is requests per interval; burst is the
// maximum burst size.
func New(rate, burst int, interval time.Duration) *Limiter {
	l := &Limiter{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		burst:    burst,
		interval: interval,
	}
	// Periodically clean up stale entries.
	go l.cleanup()
	return l
}

// Middleware returns an http.Handler middleware that enforces rate limits per
// client IP (using X-Real-IP or RemoteAddr).
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.Header.Get("X-Real-IP")
		if ip == "" {
			ip = r.RemoteAddr
		}
		if !l.allow(ip) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *Limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastFill: time.Now()}
		l.buckets[key] = b
	}

	// Refill tokens based on elapsed time.
	elapsed := time.Since(b.lastFill)
	refill := int(elapsed / l.interval) * l.rate
	if refill > 0 {
		b.tokens += refill
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastFill = time.Now()
	}

	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for k, b := range l.buckets {
			if b.lastFill.Before(cutoff) {
				delete(l.buckets, k)
			}
		}
		l.mu.Unlock()
	}
}
