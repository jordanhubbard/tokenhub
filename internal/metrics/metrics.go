package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	reg *prometheus.Registry

	RequestsTotal    *prometheus.CounterVec
	RequestLatency   *prometheus.HistogramVec
	CostUSD          *prometheus.CounterVec
	TokensTotal      *prometheus.CounterVec
	RateLimitedTotal prometheus.Counter
	TemporalUp       prometheus.Gauge

	// Circuit breaker metrics.
	TemporalCircuitState  prometheus.Gauge   // 0=closed, 1=open, 2=half-open
	TemporalFallbackTotal prometheus.Counter // count of requests that fell back to direct engine

	// Provider health state per provider (2=healthy, 1=degraded, 0=down).
	ProviderHealthState *prometheus.GaugeVec

	// Request errors broken down by HTTP status code.
	RequestErrorsByStatus *prometheus.CounterVec

	// Providers skipped during routing, by skip reason.
	ProviderSkipsTotal *prometheus.CounterVec
}

func New() *Registry {
	reg := prometheus.NewRegistry()
	m := &Registry{
		reg: reg,
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tokenhub_requests_total",
			Help: "Total requests routed through tokenhub",
		}, []string{"mode", "model", "provider", "status"}),
		RequestLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "tokenhub_request_latency_ms",
			Help: "Request latency in milliseconds",
			Buckets: prometheus.ExponentialBuckets(10, 2, 10),
		}, []string{"mode", "model", "provider"}),
		CostUSD: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tokenhub_cost_usd_total",
			Help: "Estimated USD cost",
		}, []string{"model", "provider"}),
		TokensTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tokenhub_tokens_total",
			Help: "Total tokens consumed (input + output)",
		}, []string{"model", "provider", "direction"}),
		RateLimitedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tokenhub_rate_limited_total",
			Help: "Total requests rejected by rate limiter",
		}),
		TemporalUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tokenhub_temporal_up",
			Help: "Whether Temporal workflow engine is connected (1=up, 0=down/disabled)",
		}),
		TemporalCircuitState: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tokenhub_temporal_circuit_state",
			Help: "Temporal circuit breaker state (0=closed, 1=open, 2=half-open)",
		}),
		TemporalFallbackTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tokenhub_temporal_fallback_total",
			Help: "Total requests that fell back to direct engine due to circuit breaker",
		}),
		ProviderHealthState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "tokenhub_provider_health_state",
			Help: "Current health state per provider (2=healthy, 1=degraded, 0=down)",
		}, []string{"provider"}),
		RequestErrorsByStatus: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tokenhub_request_errors_total",
			Help: "Request errors broken down by HTTP status code",
		}, []string{"mode", "model", "provider", "http_status"}),
		ProviderSkipsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tokenhub_provider_skips_total",
			Help: "Providers skipped during routing, by reason",
		}, []string{"provider", "reason"}),
	}
	reg.MustRegister(
		m.RequestsTotal, m.RequestLatency, m.CostUSD, m.TokensTotal,
		m.RateLimitedTotal, m.TemporalUp, m.TemporalCircuitState, m.TemporalFallbackTotal,
		m.ProviderHealthState, m.RequestErrorsByStatus, m.ProviderSkipsTotal,
	)
	return m
}

// RecordProviderSkip implements router.SkipRecorder.
func (m *Registry) RecordProviderSkip(providerID string, reason string) {
	m.ProviderSkipsTotal.WithLabelValues(providerID, reason).Inc()
}

func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
