package stats

import (
	"sort"
	"sync"
	"time"
)

// Snapshot is a single data point recorded for a request.
type Snapshot struct {
	Timestamp    time.Time
	ModelID      string
	ProviderID   string
	LatencyMs    float64
	CostUSD      float64
	Success      bool
	InputTokens  int
	OutputTokens int
}

// Window defines a named time window for aggregation.
type Window struct {
	Name     string
	Duration time.Duration
}

// DefaultWindows returns the standard set of rolling windows.
func DefaultWindows() []Window {
	return []Window{
		{Name: "1m", Duration: time.Minute},
		{Name: "5m", Duration: 5 * time.Minute},
		{Name: "1h", Duration: time.Hour},
		{Name: "24h", Duration: 24 * time.Hour},
	}
}

// Aggregate holds computed stats for a time window.
type Aggregate struct {
	Window        string  `json:"window"`
	ModelID       string  `json:"model_id,omitempty"`
	ProviderID    string  `json:"provider_id,omitempty"`
	RequestCount  int     `json:"request_count"`
	ErrorCount    int     `json:"error_count"`
	ErrorRate     float64 `json:"error_rate"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	P95LatencyMs  float64 `json:"p95_latency_ms"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	TotalTokens   int     `json:"total_tokens"`
}

// Collector maintains rolling snapshots for dashboard aggregation.
type Collector struct {
	mu        sync.RWMutex
	snapshots []Snapshot
	maxAge    time.Duration // oldest snapshot to keep
	windows   []Window
}

// NewCollector creates a new stats collector.
func NewCollector() *Collector {
	return &Collector{
		windows: DefaultWindows(),
		maxAge:  25 * time.Hour, // keep slightly more than largest window
	}
}

// Record adds a new snapshot.
func (c *Collector) Record(s Snapshot) {
	if s.Timestamp.IsZero() {
		s.Timestamp = time.Now().UTC()
	}
	c.mu.Lock()
	c.snapshots = append(c.snapshots, s)
	c.mu.Unlock()
}

// Seed bulk-loads historical snapshots (e.g. from the database on startup)
// so the dashboard is not blank after a restart.
func (c *Collector) Seed(snapshots []Snapshot) {
	c.mu.Lock()
	c.snapshots = append(c.snapshots, snapshots...)
	c.mu.Unlock()
}

// Prune removes snapshots older than maxAge.
func (c *Collector) Prune() {
	cutoff := time.Now().Add(-c.maxAge)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(cutoff)
}

// pruneLocked removes expired snapshots. Caller must hold c.mu (write lock).
func (c *Collector) pruneLocked(cutoff time.Time) {
	i := 0
	for i < len(c.snapshots) && c.snapshots[i].Timestamp.Before(cutoff) {
		i++
	}
	if i > 0 {
		c.snapshots = c.snapshots[i:]
	}
}

// snapshotsAfterPrune acquires a write lock, prunes expired snapshots, and
// returns a snapshot of the current data. This avoids the lock gap that exists
// when Prune() and a read lock are acquired separately.
func (c *Collector) snapshotsAfterPrune() []Snapshot {
	cutoff := time.Now().Add(-c.maxAge)
	c.mu.Lock()
	c.pruneLocked(cutoff)
	cp := make([]Snapshot, len(c.snapshots))
	copy(cp, c.snapshots)
	c.mu.Unlock()
	return cp
}

// Summary returns aggregated stats for all windows grouped by model.
func (c *Collector) Summary() map[string][]Aggregate {
	snapshots := c.snapshotsAfterPrune()

	now := time.Now()
	result := make(map[string][]Aggregate)

	for _, w := range c.windows {
		cutoff := now.Add(-w.Duration)

		byModel := make(map[string][]Snapshot)
		for _, s := range snapshots {
			if s.Timestamp.After(cutoff) {
				byModel[s.ModelID] = append(byModel[s.ModelID], s)
			}
		}

		for modelID, snaps := range byModel {
			result[w.Name] = append(result[w.Name], computeAggregate(w.Name, modelID, "", snaps))
		}
	}

	return result
}

// SummaryByProvider returns aggregated stats for all windows grouped by provider.
func (c *Collector) SummaryByProvider() map[string][]Aggregate {
	snapshots := c.snapshotsAfterPrune()

	now := time.Now()
	result := make(map[string][]Aggregate)

	for _, w := range c.windows {
		cutoff := now.Add(-w.Duration)

		byProvider := make(map[string][]Snapshot)
		for _, s := range snapshots {
			if s.Timestamp.After(cutoff) {
				byProvider[s.ProviderID] = append(byProvider[s.ProviderID], s)
			}
		}

		for providerID, snaps := range byProvider {
			result[w.Name] = append(result[w.Name], computeAggregate(w.Name, "", providerID, snaps))
		}
	}

	return result
}

// Global returns aggregate stats across all models and providers.
func (c *Collector) Global() []Aggregate {
	snapshots := c.snapshotsAfterPrune()

	now := time.Now()
	var result []Aggregate

	for _, w := range c.windows {
		cutoff := now.Add(-w.Duration)
		var snaps []Snapshot
		for _, s := range snapshots {
			if s.Timestamp.After(cutoff) {
				snaps = append(snaps, s)
			}
		}
		if len(snaps) > 0 {
			result = append(result, computeAggregate(w.Name, "", "", snaps))
		}
	}

	return result
}

// SnapshotCount returns the total number of stored snapshots.
func (c *Collector) SnapshotCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.snapshots)
}

func computeAggregate(window, modelID, providerID string, snaps []Snapshot) Aggregate {
	a := Aggregate{
		Window:       window,
		ModelID:      modelID,
		ProviderID:   providerID,
		RequestCount: len(snaps),
	}

	var totalLatency float64
	latencies := make([]float64, 0, len(snaps))

	for _, s := range snaps {
		totalLatency += s.LatencyMs
		latencies = append(latencies, s.LatencyMs)
		a.TotalCostUSD += s.CostUSD
		a.InputTokens += s.InputTokens
		a.OutputTokens += s.OutputTokens
		if !s.Success {
			a.ErrorCount++
		}
	}
	a.TotalTokens = a.InputTokens + a.OutputTokens

	if a.RequestCount > 0 {
		a.AvgLatencyMs = totalLatency / float64(a.RequestCount)
		a.ErrorRate = float64(a.ErrorCount) / float64(a.RequestCount)
	}

	// P95 latency.
	sort.Float64s(latencies)
	if len(latencies) > 0 {
		idx := int(float64(len(latencies)) * 0.95)
		if idx >= len(latencies) {
			idx = len(latencies) - 1
		}
		a.P95LatencyMs = latencies[idx]
	}

	return a
}
