package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNew(t *testing.T) {
	r := New()
	if r == nil {
		t.Fatal("expected non-nil Registry")
	}
	if r.reg == nil {
		t.Fatal("expected non-nil prometheus registry")
	}
	if r.RequestsTotal == nil {
		t.Fatal("expected non-nil RequestsTotal counter")
	}
	if r.RequestLatency == nil {
		t.Fatal("expected non-nil RequestLatency histogram")
	}
	if r.CostUSD == nil {
		t.Fatal("expected non-nil CostUSD counter")
	}
}

func TestHandlerNonNil(t *testing.T) {
	r := New()
	h := r.Handler()
	if h == nil {
		t.Fatal("expected non-nil http.Handler from Handler()")
	}
}

func TestMetricsCanBeCollected(t *testing.T) {
	r := New()

	// Increment a counter to ensure it doesn't panic.
	r.RequestsTotal.WithLabelValues("normal", "gpt-4", "openai", "200").Inc()
	r.CostUSD.WithLabelValues("gpt-4", "openai").Add(0.01)
	r.RequestLatency.WithLabelValues("normal", "gpt-4", "openai").Observe(150.0)

	// Gather metrics from the registry; this exercises the full collection path.
	mfs, err := r.reg.Gather()
	if err != nil {
		t.Fatalf("unexpected error gathering metrics: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("expected at least one metric family after recording values")
	}

	names := make(map[string]bool)
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	want := []string{
		"tokenhub_requests_total",
		"tokenhub_request_latency_ms",
		"tokenhub_cost_usd_total",
	}
	for _, name := range want {
		if !names[name] {
			t.Errorf("expected metric %q in gathered metrics", name)
		}
	}
}

func TestMultipleRegistriesAreIndependent(t *testing.T) {
	r1 := New()
	r2 := New()

	r1.RequestsTotal.WithLabelValues("normal", "gpt-4", "openai", "200").Inc()

	// r2 should have zero metrics gathered (no observations made).
	mfs, err := r2.reg.Gather()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			if m.GetCounter() != nil && m.GetCounter().GetValue() > 0 {
				t.Error("r2 should not have any non-zero counters")
			}
		}
	}
	_ = r1
}

func TestRegisteredMetricDescriptions(t *testing.T) {
	r := New()

	// Describe should emit descriptors for all registered metrics.
	ch := make(chan *prometheus.Desc, 10)
	go func() {
		r.RequestsTotal.Describe(ch)
		r.RequestLatency.Describe(ch)
		r.CostUSD.Describe(ch)
		close(ch)
	}()

	count := 0
	for range ch {
		count++
	}
	if count != 3 {
		t.Errorf("expected 3 metric descriptors, got %d", count)
	}
}
