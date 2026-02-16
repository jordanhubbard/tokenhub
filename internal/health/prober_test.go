package health

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTarget struct {
	id       string
	endpoint string
}

func (f *fakeTarget) ID() string              { return f.id }
func (f *fakeTarget) HealthEndpoint() string   { return f.endpoint }

func TestProberHealthyEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tracker := NewTracker(DefaultConfig())
	target := &fakeTarget{id: "test-provider", endpoint: srv.URL + "/health"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prober := NewProber(ProberConfig{
		Interval:     50 * time.Millisecond,
		ProbeTimeout: 2 * time.Second,
	}, tracker, []Probeable{target}, logger)

	prober.Start()
	time.Sleep(80 * time.Millisecond)
	prober.Stop()

	stats := tracker.GetStats("test-provider")
	if stats.State != StateHealthy {
		t.Errorf("expected healthy, got %s", stats.State)
	}
	if stats.TotalRequests == 0 {
		t.Error("expected at least one probe request recorded")
	}
}

func TestProberUnhealthyEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := TrackerConfig{
		ConsecErrorsForDegraded: 1,
		ConsecErrorsForDown:     3,
		CooldownDuration:        time.Minute,
	}
	tracker := NewTracker(cfg)
	target := &fakeTarget{id: "bad-provider", endpoint: srv.URL + "/health"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prober := NewProber(ProberConfig{
		Interval:     30 * time.Millisecond,
		ProbeTimeout: 2 * time.Second,
	}, tracker, []Probeable{target}, logger)

	prober.Start()
	time.Sleep(120 * time.Millisecond)
	prober.Stop()

	stats := tracker.GetStats("bad-provider")
	if stats.TotalErrors == 0 {
		t.Error("expected errors to be recorded for unhealthy endpoint")
	}
	if stats.State == StateHealthy {
		t.Errorf("expected degraded or down, got %s", stats.State)
	}
}

func TestProber405CountsAsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	tracker := NewTracker(DefaultConfig())
	target := &fakeTarget{id: "anthropic", endpoint: srv.URL + "/v1/messages"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prober := NewProber(ProberConfig{
		Interval:     50 * time.Millisecond,
		ProbeTimeout: 2 * time.Second,
	}, tracker, []Probeable{target}, logger)

	prober.Start()
	time.Sleep(80 * time.Millisecond)
	prober.Stop()

	stats := tracker.GetStats("anthropic")
	if stats.State != StateHealthy {
		t.Errorf("expected healthy for 405, got %s", stats.State)
	}
}

func TestProberUnreachableEndpoint(t *testing.T) {
	cfg := TrackerConfig{
		ConsecErrorsForDegraded: 1,
		ConsecErrorsForDown:     2,
		CooldownDuration:        time.Minute,
	}
	tracker := NewTracker(cfg)
	// Point to a port that's not listening.
	target := &fakeTarget{id: "dead-provider", endpoint: "http://127.0.0.1:1/health"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prober := NewProber(ProberConfig{
		Interval:     30 * time.Millisecond,
		ProbeTimeout: 1 * time.Second,
	}, tracker, []Probeable{target}, logger)

	prober.Start()
	time.Sleep(120 * time.Millisecond)
	prober.Stop()

	stats := tracker.GetStats("dead-provider")
	if stats.TotalErrors == 0 {
		t.Error("expected errors for unreachable endpoint")
	}
}

func TestProberEmptyEndpointSkipped(t *testing.T) {
	tracker := NewTracker(DefaultConfig())
	target := &fakeTarget{id: "no-probe", endpoint: ""}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prober := NewProber(ProberConfig{
		Interval:     50 * time.Millisecond,
		ProbeTimeout: 2 * time.Second,
	}, tracker, []Probeable{target}, logger)

	prober.Start()
	time.Sleep(80 * time.Millisecond)
	prober.Stop()

	stats := tracker.GetStats("no-probe")
	if stats.TotalRequests != 0 {
		t.Errorf("expected no requests for empty endpoint, got %d", stats.TotalRequests)
	}
}

func TestProberStopIsClean(t *testing.T) {
	var probeCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tracker := NewTracker(DefaultConfig())
	target := &fakeTarget{id: "p1", endpoint: srv.URL + "/health"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prober := NewProber(ProberConfig{
		Interval:     10 * time.Second, // long interval — only initial probe fires
		ProbeTimeout: 2 * time.Second,
	}, tracker, []Probeable{target}, logger)

	prober.Start()
	time.Sleep(50 * time.Millisecond)
	prober.Stop()

	countAfterStop := probeCount.Load()
	time.Sleep(50 * time.Millisecond)

	if probeCount.Load() != countAfterStop {
		t.Error("probes continued after Stop()")
	}
}

func TestProberMultipleTargets(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tracker := NewTracker(DefaultConfig())
	targets := []Probeable{
		&fakeTarget{id: "p1", endpoint: srv.URL + "/health"},
		&fakeTarget{id: "p2", endpoint: srv.URL + "/health"},
		&fakeTarget{id: "p3", endpoint: srv.URL + "/health"},
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prober := NewProber(ProberConfig{
		Interval:     10 * time.Second,
		ProbeTimeout: 2 * time.Second,
	}, tracker, targets, logger)

	prober.Start()
	time.Sleep(80 * time.Millisecond)
	prober.Stop()

	// Initial probe should hit all 3 targets.
	if hits.Load() < 3 {
		t.Errorf("expected at least 3 probe hits, got %d", hits.Load())
	}

	for _, id := range []string{"p1", "p2", "p3"} {
		s := tracker.GetStats(id)
		if s.TotalRequests == 0 {
			t.Errorf("expected probe recorded for %s", id)
		}
	}
}
