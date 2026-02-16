package health

import (
	"testing"
	"time"
)

func TestRecordSuccess(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.RecordSuccess("openai", 150.0)
	tr.RecordSuccess("openai", 200.0)

	s := tr.GetStats("openai")
	if s.TotalRequests != 2 {
		t.Errorf("expected 2 requests, got %d", s.TotalRequests)
	}
	if s.State != StateHealthy {
		t.Errorf("expected healthy, got %s", s.State)
	}
	if s.ConsecErrors != 0 {
		t.Errorf("expected 0 consec errors, got %d", s.ConsecErrors)
	}
}

func TestDegradedAfterErrors(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.RecordError("openai", "timeout")
	tr.RecordError("openai", "timeout")

	s := tr.GetStats("openai")
	if s.State != StateDegraded {
		t.Errorf("expected degraded after 2 errors, got %s", s.State)
	}
	if !tr.IsAvailable("openai") {
		t.Error("degraded provider should still be available")
	}
}

func TestDownAfterErrors(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	for i := 0; i < 5; i++ {
		tr.RecordError("openai", "server error")
	}

	s := tr.GetStats("openai")
	if s.State != StateDown {
		t.Errorf("expected down after 5 errors, got %s", s.State)
	}
	if tr.IsAvailable("openai") {
		t.Error("down provider should not be available during cooldown")
	}
}

func TestCooldownExpiry(t *testing.T) {
	cfg := TrackerConfig{
		ConsecErrorsForDegraded: 1,
		ConsecErrorsForDown:     2,
		CooldownDuration:        10 * time.Millisecond,
	}
	tr := NewTracker(cfg)
	tr.RecordError("openai", "error1")
	tr.RecordError("openai", "error2")

	if tr.IsAvailable("openai") {
		t.Error("should be unavailable during cooldown")
	}

	time.Sleep(15 * time.Millisecond)

	if !tr.IsAvailable("openai") {
		t.Error("should be available after cooldown expires")
	}
}

func TestSuccessResetsErrors(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.RecordError("openai", "error1")
	tr.RecordError("openai", "error2")

	s := tr.GetStats("openai")
	if s.State != StateDegraded {
		t.Fatalf("expected degraded, got %s", s.State)
	}

	tr.RecordSuccess("openai", 100)

	s = tr.GetStats("openai")
	if s.State != StateHealthy {
		t.Errorf("expected healthy after success, got %s", s.State)
	}
	if s.ConsecErrors != 0 {
		t.Errorf("expected 0 consec errors after success, got %d", s.ConsecErrors)
	}
}

func TestUnknownProviderAvailable(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	if !tr.IsAvailable("unknown") {
		t.Error("unknown provider should be available by default")
	}
}

func TestAllStats(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.RecordSuccess("openai", 100)
	tr.RecordSuccess("anthropic", 200)
	tr.RecordError("vllm", "error")

	all := tr.AllStats()
	if len(all) != 3 {
		t.Errorf("expected 3 providers in AllStats, got %d", len(all))
	}
}

func TestGetStatsUnknown(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	s := tr.GetStats("nonexistent")
	if s.State != StateHealthy {
		t.Errorf("expected healthy for unknown provider, got %s", s.State)
	}
}

func TestErrorCountTracking(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.RecordSuccess("p1", 50)
	tr.RecordError("p1", "err1")
	tr.RecordError("p1", "err2")

	s := tr.GetStats("p1")
	if s.TotalRequests != 3 {
		t.Errorf("expected 3 total requests, got %d", s.TotalRequests)
	}
	if s.TotalErrors != 2 {
		t.Errorf("expected 2 total errors, got %d", s.TotalErrors)
	}
}
