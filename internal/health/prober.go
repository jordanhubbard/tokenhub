package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Probeable is implemented by provider adapters that support health probing.
type Probeable interface {
	ID() string
	HealthEndpoint() string
}

// ProberConfig configures the health check prober.
type ProberConfig struct {
	Interval     time.Duration
	ProbeTimeout time.Duration
}

// DefaultProberConfig returns sensible defaults.
func DefaultProberConfig() ProberConfig {
	return ProberConfig{
		Interval:     30 * time.Second,
		ProbeTimeout: 5 * time.Second,
	}
}

// Prober periodically probes provider health endpoints and feeds results
// into the health Tracker.
type Prober struct {
	cfg     ProberConfig
	tracker *Tracker
	client  *http.Client
	logger  *slog.Logger
	stop    chan struct{}
	done    chan struct{}

	mu      sync.RWMutex
	targets map[string]Probeable // keyed by provider ID
}

// NewProber creates a health check prober.
func NewProber(cfg ProberConfig, tracker *Tracker, targets []Probeable, logger *slog.Logger) *Prober {
	m := make(map[string]Probeable, len(targets))
	for _, t := range targets {
		m[t.ID()] = t
	}
	return &Prober{
		cfg:     cfg,
		tracker: tracker,
		targets: m,
		client:  &http.Client{Timeout: cfg.ProbeTimeout},
		logger:  logger,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// AddTarget registers a new probe target at runtime. If a target with the
// same ID already exists it is replaced. Safe to call while the prober is running.
func (p *Prober) AddTarget(t Probeable) {
	p.mu.Lock()
	p.targets[t.ID()] = t
	p.mu.Unlock()
	p.logger.Info("health prober: added target", slog.String("provider", t.ID()))
}

// RemoveTarget removes a probe target by ID. Safe to call while the prober is running.
func (p *Prober) RemoveTarget(id string) {
	p.mu.Lock()
	delete(p.targets, id)
	p.mu.Unlock()
	p.logger.Info("health prober: removed target", slog.String("provider", id))
}

// Start begins the periodic probe loop in a goroutine.
func (p *Prober) Start() {
	go p.run()
}

// Stop signals the prober to stop and waits for it to finish.
func (p *Prober) Stop() {
	close(p.stop)
	<-p.done
}

func (p *Prober) run() {
	defer close(p.done)

	// Probe immediately on start.
	p.probeAll()

	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.probeAll()
		case <-p.stop:
			return
		}
	}
}

func (p *Prober) probeAll() {
	p.mu.RLock()
	snapshot := make([]Probeable, 0, len(p.targets))
	for _, t := range p.targets {
		snapshot = append(snapshot, t)
	}
	p.mu.RUnlock()

	var wg sync.WaitGroup
	for _, t := range snapshot {
		wg.Add(1)
		go func(target Probeable) {
			defer wg.Done()
			p.probe(target)
		}(t)
	}
	wg.Wait()
}

func (p *Prober) probe(target Probeable) {
	endpoint := target.HealthEndpoint()
	if endpoint == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.ProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		p.tracker.RecordError(target.ID(), "probe: "+err.Error())
		p.logger.Warn("health probe request error",
			slog.String("provider", target.ID()),
			slog.String("error", err.Error()),
		)
		return
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		p.tracker.RecordError(target.ID(), "probe: "+err.Error())
		p.logger.Warn("health probe failed",
			slog.String("provider", target.ID()),
			slog.String("error", err.Error()),
		)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Any 2xx, 401 (Unauthorized — endpoint exists, auth required), or 405
	// (Method Not Allowed — endpoint exists) counts as healthy.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 ||
		resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusMethodNotAllowed {
		p.tracker.RecordSuccess(target.ID(), latencyMs)
		p.logger.Debug("health probe ok",
			slog.String("provider", target.ID()),
			slog.Int("status", resp.StatusCode),
			slog.Float64("latency_ms", latencyMs),
		)
	} else {
		p.tracker.RecordError(target.ID(), "probe: HTTP "+resp.Status)
		p.logger.Warn("health probe unhealthy",
			slog.String("provider", target.ID()),
			slog.Int("status", resp.StatusCode),
		)
	}
}
