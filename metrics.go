package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics holds the app's Prometheus registry. Each server gets its own
// registry (not the global default) so tests can build many servers without
// "duplicate metrics collector registration" panics.
type metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
}

// newMetrics registers the three app metrics plus the standard Go runtime and
// process metrics (go_*, process_*) that every Go exporter ships:
//
//	http_requests_total{path,status}  counter — one per handled request
//	todos{state}                      gauge   — open / done todos, read at scrape time
//	db_up                             gauge   — 1 if the database answers a ping, else 0
func newMetrics(store Store) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests handled, by route pattern and status code.",
		}, []string{"path", "status"}),
	}
	m.registry.MustRegister(
		m.requests,
		&storeCollector{store: store},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// handler serves the metrics in the Prometheus text format (GET /metrics).
func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// routes is the handler for the separate metrics listener (metrics_addr,
// default :8081): GET /metrics and nothing else — no login, no middleware.
// Keeping metrics off the app port means an Ingress/Route, which only points at
// the app port, can never expose them.
//
// Side effect: Prometheus scrapes no longer pass through countRequests, so they
// do not show up as path="/metrics" in http_requests_total any more.
func (m *metrics) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.handler())
	return mux
}

// countRequests is middleware that increments http_requests_total. The path
// label is the matched ROUTE PATTERN (e.g. "/todos/{id}/toggle"), not the raw
// URL — otherwise every todo id would create a new time series.
func (m *metrics) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.requests.WithLabelValues(routeLabel(r), strconv.Itoa(rec.status)).Inc()
	})
}

// routeLabel turns the ServeMux pattern ("POST /todos/{id}/delete") into a
// label value ("/todos/{id}/delete"). Unmatched requests (404) have no pattern.
func routeLabel(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	if _, path, ok := strings.Cut(r.Pattern, " "); ok {
		return path
	}
	return r.Pattern
}

// storeCollector asks the database at scrape time, so todos and db_up
// are always current without any bookkeeping in the handlers.
type storeCollector struct{ store Store }

var (
	todosDesc = prometheus.NewDesc("todos", "Number of todos, by state (open/done).", []string{"state"}, nil)
	dbUpDesc  = prometheus.NewDesc("db_up", "1 if the database answers a ping, 0 otherwise.", nil, nil)
)

func (c *storeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- todosDesc
	ch <- dbUpDesc
}

func (c *storeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.store.Ping(ctx); err != nil {
		ch <- prometheus.MustNewConstMetric(dbUpDesc, prometheus.GaugeValue, 0)
		return // no todo counts without a database
	}
	ch <- prometheus.MustNewConstMetric(dbUpDesc, prometheus.GaugeValue, 1)

	todos, err := c.store.List(ctx)
	if err != nil {
		return
	}
	var open, done float64
	for _, t := range todos {
		if t.Done {
			done++
		} else {
			open++
		}
	}
	ch <- prometheus.MustNewConstMetric(todosDesc, prometheus.GaugeValue, open, "open")
	ch <- prometheus.MustNewConstMetric(todosDesc, prometheus.GaugeValue, done, "done")
}
