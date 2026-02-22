package health

import (
	"sync"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/events"
)

// State represents the health state of a provider.
type State string

const (
	StateHealthy  State = "healthy"
	StateDegraded State = "degraded"
	StateDown     State = "down"
)

// Stats captures runtime health metrics for a single provider.
type Stats struct {
	ProviderID     string    `json:"provider_id"`
	State          State     `json:"state"`
	TotalRequests  int64     `json:"total_requests"`
	TotalErrors    int64     `json:"total_errors"`
	ConsecErrors   int       `json:"consec_errors"`
	AvgLatencyMs   float64   `json:"avg_latency_ms"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorTime  time.Time `json:"last_error_time,omitempty"`
	LastSuccessAt  time.Time `json:"last_success_at,omitempty"`
	CooldownUntil  time.Time `json:"cooldown_until,omitempty"`
}

// TrackerConfig configures the health tracker thresholds.
type TrackerConfig struct {
	// ConsecErrorsForDegraded: how many consecutive errors before degraded state.
	ConsecErrorsForDegraded int
	// ConsecErrorsForDown: how many consecutive errors before down state.
	ConsecErrorsForDown int
	// CooldownDuration: how long to keep a provider in down state.
	CooldownDuration time.Duration
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() TrackerConfig {
	return TrackerConfig{
		ConsecErrorsForDegraded: 2,
		ConsecErrorsForDown:     5,
		CooldownDuration:        30 * time.Second,
	}
}

// Tracker tracks runtime health of all providers.
type Tracker struct {
	cfg      TrackerConfig
	EventBus *events.Bus
	onUpdate func(providerID string, state State)

	mu    sync.RWMutex
	stats map[string]*Stats
}

// TrackerOption configures optional Tracker behaviour.
type TrackerOption func(*Tracker)

// WithEventBus attaches an event bus to the tracker so that health state
// transitions are published as EventHealthChange events.
func WithEventBus(bus *events.Bus) TrackerOption {
	return func(t *Tracker) {
		t.EventBus = bus
	}
}

// WithOnUpdate registers a callback invoked on every RecordSuccess/RecordError
// call (not just state transitions). Use this to keep external gauges current.
func WithOnUpdate(fn func(providerID string, state State)) TrackerOption {
	return func(t *Tracker) {
		t.onUpdate = fn
	}
}

// NewTracker creates a health tracker with the given config.
func NewTracker(cfg TrackerConfig, opts ...TrackerOption) *Tracker {
	t := &Tracker{
		cfg:   cfg,
		stats: make(map[string]*Stats),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// RecordSuccess records a successful request to a provider.
func (t *Tracker) RecordSuccess(providerID string, latencyMs float64) {
	t.mu.Lock()

	s := t.getOrCreate(providerID)
	oldState := s.State

	s.TotalRequests++
	s.ConsecErrors = 0
	s.LastSuccessAt = time.Now()
	s.State = StateHealthy
	s.CooldownUntil = time.Time{}

	// Running average (simple weighted).
	if s.TotalRequests == 1 {
		s.AvgLatencyMs = latencyMs
	} else {
		s.AvgLatencyMs = s.AvgLatencyMs*0.9 + latencyMs*0.1
	}

	newState := s.State
	t.mu.Unlock()

	if t.onUpdate != nil {
		t.onUpdate(providerID, newState)
	}
	if oldState != newState && t.EventBus != nil {
		t.EventBus.Publish(events.Event{
			Type:       events.EventHealthChange,
			ProviderID: providerID,
			OldState:   string(oldState),
			NewState:   string(newState),
			Reason:     "success recorded",
		})
	}
}

// RecordError records a failed request to a provider.
func (t *Tracker) RecordError(providerID string, errMsg string) {
	t.mu.Lock()

	s := t.getOrCreate(providerID)
	oldState := s.State

	s.TotalRequests++
	s.TotalErrors++
	s.ConsecErrors++
	s.LastError = errMsg
	s.LastErrorTime = time.Now()

	if s.ConsecErrors >= t.cfg.ConsecErrorsForDown {
		s.State = StateDown
		s.CooldownUntil = time.Now().Add(t.cfg.CooldownDuration)
	} else if s.ConsecErrors >= t.cfg.ConsecErrorsForDegraded {
		s.State = StateDegraded
	}

	newState := s.State
	t.mu.Unlock()

	if t.onUpdate != nil {
		t.onUpdate(providerID, newState)
	}
	if oldState != newState && t.EventBus != nil {
		t.EventBus.Publish(events.Event{
			Type:       events.EventHealthChange,
			ProviderID: providerID,
			OldState:   string(oldState),
			NewState:   string(newState),
			Reason:     errMsg,
		})
	}
}

// IsAvailable returns whether a provider should receive requests.
func (t *Tracker) IsAvailable(providerID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s, ok := t.stats[providerID]
	if !ok {
		return true // unknown provider is assumed available
	}
	if s.State == StateDown && time.Now().Before(s.CooldownUntil) {
		return false
	}
	return true
}

// GetStats returns a copy of the health stats for a provider.
func (t *Tracker) GetStats(providerID string) *Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s, ok := t.stats[providerID]
	if !ok {
		return &Stats{ProviderID: providerID, State: StateHealthy}
	}
	cp := *s
	return &cp
}

// AllStats returns a copy of health stats for all known providers.
func (t *Tracker) AllStats() []Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]Stats, 0, len(t.stats))
	for _, s := range t.stats {
		result = append(result, *s)
	}
	return result
}

// GetAvgLatencyMs returns the average latency for a provider.
// Implements router.StatsProvider.
func (t *Tracker) GetAvgLatencyMs(providerID string) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if s, ok := t.stats[providerID]; ok {
		return s.AvgLatencyMs
	}
	return 0
}

// GetErrorRate returns the error rate for a provider.
// Implements router.StatsProvider.
func (t *Tracker) GetErrorRate(providerID string) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if s, ok := t.stats[providerID]; ok && s.TotalRequests > 0 {
		return float64(s.TotalErrors) / float64(s.TotalRequests)
	}
	return 0
}

func (t *Tracker) getOrCreate(providerID string) *Stats {
	s, ok := t.stats[providerID]
	if !ok {
		s = &Stats{ProviderID: providerID, State: StateHealthy}
		t.stats[providerID] = s
	}
	return s
}
