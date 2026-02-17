package circuitbreaker

import (
	"testing"
	"time"
)

func TestClosed_AllowsRequests(t *testing.T) {
	b := New()
	if !b.Allow() {
		t.Fatal("closed breaker should allow requests")
	}
	if b.CurrentState() != Closed {
		t.Fatalf("expected Closed, got %s", b.CurrentState())
	}
}

func TestTripsAfterThreshold(t *testing.T) {
	b := New(WithThreshold(3))

	// First two failures should not trip.
	b.RecordFailure()
	b.RecordFailure()
	if b.CurrentState() != Closed {
		t.Fatalf("expected Closed after 2 failures, got %s", b.CurrentState())
	}
	if !b.Allow() {
		t.Fatal("should still allow after 2 failures")
	}

	// Third failure trips the breaker.
	b.RecordFailure()
	if b.CurrentState() != Open {
		t.Fatalf("expected Open after 3 failures, got %s", b.CurrentState())
	}
}

func TestOpen_RejectsRequests(t *testing.T) {
	now := time.Now()
	b := New(WithThreshold(1), WithCooldown(10*time.Second))
	b.nowFunc = func() time.Time { return now }

	b.RecordFailure() // trips immediately
	if b.CurrentState() != Open {
		t.Fatalf("expected Open, got %s", b.CurrentState())
	}
	if b.Allow() {
		t.Fatal("open breaker should reject requests")
	}
}

func TestHalfOpen_AfterCooldown(t *testing.T) {
	now := time.Now()
	b := New(WithThreshold(1), WithCooldown(10*time.Second))
	b.nowFunc = func() time.Time { return now }

	b.RecordFailure() // trips
	if b.CurrentState() != Open {
		t.Fatalf("expected Open, got %s", b.CurrentState())
	}

	// Advance time past cooldown.
	now = now.Add(11 * time.Second)
	if !b.Allow() {
		t.Fatal("should allow one probe after cooldown")
	}
	if b.CurrentState() != HalfOpen {
		t.Fatalf("expected HalfOpen, got %s", b.CurrentState())
	}

	// Second request in HalfOpen should be rejected (only one probe).
	if b.Allow() {
		t.Fatal("should reject second request in HalfOpen")
	}
}

func TestHalfOpen_SuccessCloses(t *testing.T) {
	now := time.Now()
	b := New(WithThreshold(1), WithCooldown(5*time.Second))
	b.nowFunc = func() time.Time { return now }

	b.RecordFailure() // trips

	// Advance past cooldown, transition to HalfOpen.
	now = now.Add(6 * time.Second)
	if !b.Allow() {
		t.Fatal("should allow probe")
	}
	if b.CurrentState() != HalfOpen {
		t.Fatalf("expected HalfOpen, got %s", b.CurrentState())
	}

	// Probe succeeds -> close the breaker.
	b.RecordSuccess()
	if b.CurrentState() != Closed {
		t.Fatalf("expected Closed after success, got %s", b.CurrentState())
	}
	if !b.Allow() {
		t.Fatal("closed breaker should allow requests")
	}
}

func TestHalfOpen_FailureReopens(t *testing.T) {
	now := time.Now()
	b := New(WithThreshold(1), WithCooldown(5*time.Second))
	b.nowFunc = func() time.Time { return now }

	b.RecordFailure() // trips

	// Advance past cooldown.
	now = now.Add(6 * time.Second)
	b.Allow() // transitions to HalfOpen

	// Probe fails -> reopen the breaker.
	b.RecordFailure()
	if b.CurrentState() != Open {
		t.Fatalf("expected Open after HalfOpen failure, got %s", b.CurrentState())
	}

	// Should not allow immediately.
	if b.Allow() {
		t.Fatal("should reject immediately after reopening")
	}
}

func TestRecordSuccess_ResetsFailureCount(t *testing.T) {
	b := New(WithThreshold(3))

	// Accumulate failures but don't trip.
	b.RecordFailure()
	b.RecordFailure()

	// A success resets the counter.
	b.RecordSuccess()

	// Now three more failures are needed to trip.
	b.RecordFailure()
	b.RecordFailure()
	if b.CurrentState() != Closed {
		t.Fatalf("expected Closed, got %s", b.CurrentState())
	}
	b.RecordFailure()
	if b.CurrentState() != Open {
		t.Fatalf("expected Open after 3 failures, got %s", b.CurrentState())
	}
}

func TestOnStateChange_Callback(t *testing.T) {
	var transitions []struct{ from, to State }
	cb := func(from, to State) {
		transitions = append(transitions, struct{ from, to State }{from, to})
	}

	now := time.Now()
	b := New(WithThreshold(1), WithCooldown(5*time.Second), WithOnStateChange(cb))
	b.nowFunc = func() time.Time { return now }

	// Trip: Closed -> Open
	b.RecordFailure()
	// Cooldown elapsed: Open -> HalfOpen
	now = now.Add(6 * time.Second)
	b.Allow()
	// Success: HalfOpen -> Closed
	b.RecordSuccess()

	if len(transitions) != 3 {
		t.Fatalf("expected 3 transitions, got %d", len(transitions))
	}
	expected := []struct{ from, to State }{
		{Closed, Open},
		{Open, HalfOpen},
		{HalfOpen, Closed},
	}
	for i, tr := range transitions {
		if tr.from != expected[i].from || tr.to != expected[i].to {
			t.Errorf("transition %d: expected %s->%s, got %s->%s",
				i, expected[i].from, expected[i].to, tr.from, tr.to)
		}
	}
}

func TestState_String(t *testing.T) {
	tests := []struct {
		s    State
		want string
	}{
		{Closed, "closed"},
		{Open, "open"},
		{HalfOpen, "half-open"},
		{State(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestWithThreshold_IgnoresNonPositive(t *testing.T) {
	b := New(WithThreshold(0))
	if b.failureThreshold != defaultThreshold {
		t.Fatalf("expected default threshold %d, got %d", defaultThreshold, b.failureThreshold)
	}
	b = New(WithThreshold(-1))
	if b.failureThreshold != defaultThreshold {
		t.Fatalf("expected default threshold %d, got %d", defaultThreshold, b.failureThreshold)
	}
}

func TestWithCooldown_IgnoresNonPositive(t *testing.T) {
	b := New(WithCooldown(0))
	if b.cooldown != defaultCooldown {
		t.Fatalf("expected default cooldown %v, got %v", defaultCooldown, b.cooldown)
	}
	b = New(WithCooldown(-1 * time.Second))
	if b.cooldown != defaultCooldown {
		t.Fatalf("expected default cooldown %v, got %v", defaultCooldown, b.cooldown)
	}
}
