// Package circuitbreaker implements a thread-safe circuit breaker for Temporal
// workflow dispatch. When Temporal becomes unavailable, the breaker trips after
// a configurable number of consecutive failures and routes requests directly
// through the engine for a cooldown period before probing again.
package circuitbreaker

import (
	"sync"
	"time"
)

// State represents the current state of the circuit breaker.
type State int

const (
	// Closed is the normal operating state: requests are dispatched via Temporal.
	Closed State = iota
	// Open means the circuit has tripped: requests bypass Temporal entirely.
	Open
	// HalfOpen allows a single probe request through to test if Temporal has recovered.
	HalfOpen
)

// String returns a human-readable name for the state.
func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

const (
	defaultThreshold = 3
	defaultCooldown  = 30 * time.Second
)

// Breaker is a goroutine-safe circuit breaker that tracks consecutive Temporal
// failures and transitions between Closed, Open, and HalfOpen states.
type Breaker struct {
	mu               sync.Mutex
	state            State
	failureCount     int
	failureThreshold int
	cooldown         time.Duration
	lastTripped      time.Time
	onStateChange    func(from, to State)

	// nowFunc is used for testing; defaults to time.Now.
	nowFunc func() time.Time
}

// Option configures a Breaker.
type Option func(*Breaker)

// WithThreshold sets the number of consecutive failures required to trip the
// breaker from Closed to Open. The default is 3.
func WithThreshold(n int) Option {
	return func(b *Breaker) {
		if n > 0 {
			b.failureThreshold = n
		}
	}
}

// WithCooldown sets how long the breaker stays Open before transitioning to
// HalfOpen. The default is 30 seconds.
func WithCooldown(d time.Duration) Option {
	return func(b *Breaker) {
		if d > 0 {
			b.cooldown = d
		}
	}
}

// WithOnStateChange registers a callback that fires on every state transition.
// The callback is invoked while the breaker's mutex is held, so it must not
// call back into the breaker.
func WithOnStateChange(fn func(from, to State)) Option {
	return func(b *Breaker) {
		b.onStateChange = fn
	}
}

// New creates a Breaker in the Closed state with the given options.
func New(opts ...Option) *Breaker {
	b := &Breaker{
		state:            Closed,
		failureThreshold: defaultThreshold,
		cooldown:         defaultCooldown,
		nowFunc:          time.Now,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Allow reports whether the next request should be dispatched through Temporal.
//
// In Closed state it always returns true. In Open state it returns false unless
// the cooldown has elapsed, in which case it transitions to HalfOpen and returns
// true for a single probe request. In HalfOpen state it returns false (only one
// probe is allowed at a time).
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case Closed:
		return true
	case Open:
		if b.nowFunc().After(b.lastTripped.Add(b.cooldown)) {
			b.setState(HalfOpen)
			return true
		}
		return false
	case HalfOpen:
		// Only one probe at a time; reject additional requests while probing.
		return false
	default:
		return false
	}
}

// RecordSuccess records a successful Temporal call. If the breaker is HalfOpen
// (probe succeeded), it transitions back to Closed. In Closed state it resets
// the consecutive failure counter.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failureCount = 0
	if b.state == HalfOpen {
		b.setState(Closed)
	}
}

// RecordFailure records a Temporal failure. In Closed state it increments the
// consecutive failure counter and trips the breaker if the threshold is reached.
// In HalfOpen state (probe failed) it immediately reopens the breaker.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failureCount++

	switch b.state {
	case Closed:
		if b.failureCount >= b.failureThreshold {
			b.setState(Open)
			b.lastTripped = b.nowFunc()
		}
	case HalfOpen:
		b.setState(Open)
		b.lastTripped = b.nowFunc()
	}
}

// State returns the current breaker state. Note: in Open state this does NOT
// check the cooldown timer; use Allow() for that.
func (b *Breaker) CurrentState() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// setState transitions the breaker and fires the callback if registered.
// Caller must hold b.mu.
func (b *Breaker) setState(to State) {
	from := b.state
	b.state = to
	if b.onStateChange != nil && from != to {
		b.onStateChange(from, to)
	}
}
