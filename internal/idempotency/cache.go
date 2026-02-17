// Package idempotency provides request deduplication via Idempotency-Key headers.
package idempotency

import (
	"sync"
	"time"
)

// entry holds a cached HTTP response.
type entry struct {
	Response   []byte
	StatusCode int
	Headers    map[string]string
	CreatedAt  time.Time
}

// Cache is a TTL-bounded, size-limited in-memory cache for idempotent responses.
type Cache struct {
	mu         sync.Mutex
	entries    map[string]*entry
	ttl        time.Duration
	maxEntries int
	stop       chan struct{}
}

// New creates a Cache that expires entries after ttl and evicts the oldest
// entry when maxEntries is exceeded. A background goroutine prunes expired
// entries every ttl/2.
func New(ttl time.Duration, maxEntries int) *Cache {
	c := &Cache{
		entries:    make(map[string]*entry),
		ttl:        ttl,
		maxEntries: maxEntries,
		stop:       make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Get returns a cached entry if it exists and has not expired.
func (c *Cache) Get(key string) (*entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.CreatedAt) > c.ttl {
		delete(c.entries, key)
		return nil, false
	}
	return e, true
}

// Set stores a response under the given key. If the cache is at capacity, the
// oldest entry is evicted to make room.
func (c *Cache) Set(key string, response []byte, statusCode int, headers map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict oldest if at capacity and key is not already present.
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxEntries {
		c.evictOldest()
	}

	c.entries[key] = &entry{
		Response:   response,
		StatusCode: statusCode,
		Headers:    headers,
		CreatedAt:  time.Now(),
	}
}

// Stop terminates the background cleanup goroutine.
func (c *Cache) Stop() {
	close(c.stop)
}

// cleanupLoop runs in a goroutine and removes expired entries periodically.
func (c *Cache) cleanupLoop() {
	interval := c.ttl / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.prune()
		case <-c.stop:
			return
		}
	}
}

// prune removes all expired entries.
func (c *Cache) prune() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.entries {
		if now.Sub(e.CreatedAt) > c.ttl {
			delete(c.entries, k)
		}
	}
}

// evictOldest removes the entry with the earliest CreatedAt. Caller must hold c.mu.
func (c *Cache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, e := range c.entries {
		if first || e.CreatedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.CreatedAt
			first = false
		}
	}
	if !first {
		delete(c.entries, oldestKey)
	}
}
