package health

import (
	"testing"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/events"
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

func TestHealthChangeEventsPublished(t *testing.T) {
	bus := events.NewBus()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub)

	cfg := TrackerConfig{
		ConsecErrorsForDegraded: 2,
		ConsecErrorsForDown:     4,
		CooldownDuration:        10 * time.Millisecond,
	}
	tr := NewTracker(cfg, WithEventBus(bus))

	// First error: still healthy (1 < 2), no transition event.
	tr.RecordError("p1", "err1")
	select {
	case e := <-sub.C:
		t.Fatalf("unexpected event after first error: %+v", e)
	default:
	}

	// Second error: healthy -> degraded, expect event.
	tr.RecordError("p1", "err2")
	select {
	case e := <-sub.C:
		if e.Type != events.EventHealthChange {
			t.Errorf("expected EventHealthChange, got %s", e.Type)
		}
		if e.OldState != string(StateHealthy) {
			t.Errorf("expected old state healthy, got %s", e.OldState)
		}
		if e.NewState != string(StateDegraded) {
			t.Errorf("expected new state degraded, got %s", e.NewState)
		}
		if e.ProviderID != "p1" {
			t.Errorf("expected provider p1, got %s", e.ProviderID)
		}
	default:
		t.Fatal("expected health_change event on degraded transition")
	}

	// Third + fourth errors: degraded -> down, expect event.
	tr.RecordError("p1", "err3")
	tr.RecordError("p1", "err4")
	select {
	case e := <-sub.C:
		if e.NewState != string(StateDown) {
			t.Errorf("expected new state down, got %s", e.NewState)
		}
	default:
		t.Fatal("expected health_change event on down transition")
	}

	// Wait for cooldown, then success: down -> healthy.
	time.Sleep(15 * time.Millisecond)
	tr.RecordSuccess("p1", 50)
	select {
	case e := <-sub.C:
		if e.OldState != string(StateDown) {
			t.Errorf("expected old state down, got %s", e.OldState)
		}
		if e.NewState != string(StateHealthy) {
			t.Errorf("expected new state healthy, got %s", e.NewState)
		}
	default:
		t.Fatal("expected health_change event on recovery transition")
	}
}
