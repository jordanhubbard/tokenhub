package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	reg *prometheus.Registry

	RequestsTotal *prometheus.CounterVec
	RequestLatency *prometheus.HistogramVec
	CostUSD *prometheus.CounterVec
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
	}
	reg.MustRegister(m.RequestsTotal, m.RequestLatency, m.CostUSD)
	return m
}

func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
