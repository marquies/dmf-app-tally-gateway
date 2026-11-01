package gateway

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type metrics struct {
	reg           *prometheus.Registry
	connected     prometheus.Gauge
	tallySent     prometheus.Counter
	pollErrors    prometheus.Counter
	pollDuration  prometheus.Histogram
	upstreamValid prometheus.Gauge
}

func newMetrics() *metrics {
	m := &metrics{
		reg: prometheus.NewRegistry(),
		connected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tally_gateway_connected_devices",
			Help: "Registered tally devices currently connected.",
		}),
		tallySent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tally_gateway_tally_messages_sent_total",
			Help: "Tally messages queued to devices.",
		}),
		pollErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tally_gateway_upstream_poll_errors_total",
			Help: "Failed SwitchFrame state polls.",
		}),
		pollDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "tally_gateway_upstream_poll_duration_seconds",
			Help:    "Duration of SwitchFrame state polls, failed ones included.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}),
		upstreamValid: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tally_gateway_upstream_valid",
			Help: "1 while the gateway holds fresh SwitchFrame state, else 0.",
		}),
	}
	m.reg.MustRegister(m.connected, m.tallySent, m.pollErrors, m.pollDuration, m.upstreamValid,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}
