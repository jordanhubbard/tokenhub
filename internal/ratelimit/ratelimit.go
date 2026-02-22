// Package ratelimit provides a simple in-memory token bucket rate limiter
// middleware for net/http.
package ratelimit

import (
	"container/list"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Limiter is a per-IP token bucket rate limiter.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*list.Element // key -> list element (whose Value is *entry)
	lru      *list.List               // front = most recently used, back = least recently used
	rate     int                      // tokens added per interval
	burst    int                      // max tokens (bucket capacity)
	interval time.Duration            // refill interval
	maxKeys  int                      // max entries before evicting LRU
	stop     chan struct{}
	counter  prometheus.Counter // optional: incremented on each 429
}

// entry is stored in each list element, pairing the key with its bucket.
type entry struct {
	key string
	b   bucket
}

type bucket struct {
	tokens   int
	lastFill time.Time
}

// New creates a rate limiter. rate is requests per interval; burst is the
// maximum burst size. An optional Prometheus counter is incremented on each
// rejected request (pass nil to disable).
func New(rate, burst int, interval time.Duration, opts ...Option) *Limiter {
	l := &Limiter{
		buckets:  make(map[string]*list.Element),
		lru:      list.New(),
		rate:     rate,
		burst:    burst,
		interval: interval,
		maxKeys:  100000, // default cap: 100k unique IPs
		stop:     make(chan struct{}),
	}
	for _, o := range opts {
		o(l)
	}
	// Periodically clean up stale entries.
	go l.cleanup()
	return l
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithCounter sets a Prometheus counter that is incremented on each 429.
func WithCounter(c prometheus.Counter) Option {
	return func(l *Limiter) {
		l.counter = c
	}
}

// WithMaxKeys sets the maximum number of tracked keys before LRU eviction.
func WithMaxKeys(n int) Option {
	return func(l *Limiter) {
		l.maxKeys = n
	}
}

// Allow reports whether the given key is within the global rate limit.
// It uses the rate and burst values configured at construction time.
func (l *Limiter) Allow(key string) bool {
	return l.allow(key)
}

// AllowCustom reports whether the given key is within a custom rate limit.
// rate and burst override the limiter's global defaults for this specific key.
// When rate <= 0, the request is always allowed (unlimited).
func (l *Limiter) AllowCustom(key string, rate, burst int) bool {
	if rate <= 0 {
		return true // unlimited
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	elem, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			l.evictOldest()
		}
		e := &entry{key: key, b: bucket{tokens: burst, lastFill: time.Now()}}
		elem = l.lru.PushFront(e)
		l.buckets[key] = elem
	} else {
		l.lru.MoveToFront(elem)
	}

	b := &elem.Value.(*entry).b
	elapsed := time.Since(b.lastFill)
	refill := int(elapsed/l.interval) * rate
	if refill > 0 {
		b.tokens += refill
		if b.tokens > burst {
			b.tokens = burst
		}
		b.lastFill = time.Now()
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
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
			if l.counter != nil {
				l.counter.Inc()
			}
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

	elem, ok := l.buckets[key]
	if !ok {
		// Evict LRU entry if at capacity.
		if len(l.buckets) >= l.maxKeys {
			l.evictOldest()
		}
		e := &entry{
			key: key,
			b:   bucket{tokens: l.burst, lastFill: time.Now()},
		}
		elem = l.lru.PushFront(e)
		l.buckets[key] = elem
	} else {
		// Move to front on access (most recently used).
		l.lru.MoveToFront(elem)
	}

	b := &elem.Value.(*entry).b

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

// evictOldest removes the least recently used bucket (back of the list).
// Must be called with l.mu held.
func (l *Limiter) evictOldest() {
	back := l.lru.Back()
	if back == nil {
		return
	}
	e := back.Value.(*entry)
	delete(l.buckets, e.key)
	l.lru.Remove(back)
}

// UpdateLimits changes the rate and burst parameters at runtime.
// Existing per-IP buckets are not reset; they will use the new burst cap on
// their next refill cycle.
func (l *Limiter) UpdateLimits(rate, burst int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate = rate
	l.burst = burst
}

// Stop terminates the background cleanup goroutine.
func (l *Limiter) Stop() {
	close(l.stop)
}

func (l *Limiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			// Walk from back (oldest) and remove stale entries.
			for elem := l.lru.Back(); elem != nil; {
				e := elem.Value.(*entry)
				prev := elem.Prev()
				if e.b.lastFill.Before(cutoff) {
					delete(l.buckets, e.key)
					l.lru.Remove(elem)
				}
				elem = prev
			}
			l.mu.Unlock()
		case <-l.stop:
			return
		}
	}
}
