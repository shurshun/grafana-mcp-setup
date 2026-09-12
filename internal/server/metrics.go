package server

import (
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type metricKey struct {
	method string
	route  string
	status int
}

type metricValue struct {
	count   uint64
	seconds float64
}

type actionKey struct{ action, result string }
type grafanaMetricKey struct {
	method string
	status int
}

type metrics struct {
	inflight atomic.Int64
	mu       sync.Mutex
	http     map[metricKey]metricValue
	actions  map[actionKey]uint64
	grafana  map[grafanaMetricKey]metricValue
	auth     map[string]uint64
	version  string
}

func newMetrics() *metrics {
	return &metrics{http: make(map[metricKey]metricValue), actions: make(map[actionKey]uint64), grafana: make(map[grafanaMetricKey]metricValue), auth: make(map[string]uint64), version: "development"}
}

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return method
	default:
		return "other"
	}
}

func (m *metrics) SetVersion(version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.version = version
}

func (m *metrics) RecordAction(action, result string) {
	switch action {
	case "issue", "reissue", "revoke", "offboard", "reconcile":
	default:
		action = "other"
	}
	switch result {
	case "success", "failure", "partial":
	default:
		result = "failure"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actions[actionKey{action, result}]++
}

func (m *metrics) RecordGrafana(method string, status int, elapsed time.Duration) {
	if status < 100 || status > 599 {
		status = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := grafanaMetricKey{metricMethod(method), status}
	value := m.grafana[key]
	value.count++
	value.seconds += elapsed.Seconds()
	m.grafana[key] = value
}

func (m *metrics) RecordAuth(kind string) {
	switch kind {
	case "oidc_failure", "authorization_denial":
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auth[kind]++
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (m *metrics) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		m.inflight.Add(1)
		defer m.inflight.Add(-1)
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		key := metricKey{metricMethod(r.Method), route, sw.status}
		m.mu.Lock()
		value := m.http[key]
		value.count++
		value.seconds += time.Since(start).Seconds()
		m.http[key] = value
		m.mu.Unlock()
	})
}

func quoteMetric(v string) string { return strconv.Quote(v) }

func (m *metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	m.mu.Lock()
	httpMetrics, actions := maps.Clone(m.http), maps.Clone(m.actions)
	grafanaMetrics, auth := maps.Clone(m.grafana), maps.Clone(m.auth)
	version := m.version
	m.mu.Unlock()
	_, _ = fmt.Fprintln(w, "# HELP grafana_mcp_http_requests_total HTTP requests handled.")
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_http_requests_total counter")
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_http_request_duration_seconds summary")
	for key, value := range httpMetrics {
		labels := "method=" + quoteMetric(key.method) + ",route=" + quoteMetric(key.route) + ",status=" + quoteMetric(strconv.Itoa(key.status))
		_, _ = fmt.Fprintf(w, "grafana_mcp_http_requests_total{%s} %d\n", labels, value.count)
		_, _ = fmt.Fprintf(w, "grafana_mcp_http_request_duration_seconds_sum{%s} %g\n", labels, value.seconds)
		_, _ = fmt.Fprintf(w, "grafana_mcp_http_request_duration_seconds_count{%s} %d\n", labels, value.count)
	}
	_, _ = fmt.Fprintln(w, "# HELP grafana_mcp_actions_total Credential operations by outcome.")
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_actions_total counter")
	for key, count := range actions {
		_, _ = fmt.Fprintf(w, "grafana_mcp_actions_total{action=%s,result=%s} %d\n", quoteMetric(key.action), quoteMetric(key.result), count)
	}
	_, _ = fmt.Fprintln(w, "# HELP grafana_mcp_grafana_requests_total Grafana requests; status zero means no HTTP response.")
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_grafana_requests_total counter")
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_grafana_request_duration_seconds summary")
	for key, value := range grafanaMetrics {
		labels := "method=" + quoteMetric(key.method) + ",status=" + quoteMetric(strconv.Itoa(key.status))
		_, _ = fmt.Fprintf(w, "grafana_mcp_grafana_requests_total{%s} %d\n", labels, value.count)
		_, _ = fmt.Fprintf(w, "grafana_mcp_grafana_request_duration_seconds_sum{%s} %g\n", labels, value.seconds)
		_, _ = fmt.Fprintf(w, "grafana_mcp_grafana_request_duration_seconds_count{%s} %d\n", labels, value.count)
	}
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_auth_failures_total counter")
	for kind, count := range auth {
		_, _ = fmt.Fprintf(w, "grafana_mcp_auth_failures_total{kind=%s} %d\n", quoteMetric(kind), count)
	}
	_, _ = fmt.Fprintln(w, "# HELP grafana_mcp_http_requests_in_flight HTTP requests currently being handled.")
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_http_requests_in_flight gauge")
	_, _ = fmt.Fprintf(w, "grafana_mcp_http_requests_in_flight %d\n", m.inflight.Load())
	_, _ = fmt.Fprintln(w, "# TYPE grafana_mcp_build_info gauge")
	_, _ = fmt.Fprintf(w, "grafana_mcp_build_info{version=%s} 1\n", quoteMetric(version))
}
