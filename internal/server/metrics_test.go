package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetricsBoundUntrustedLabels(t *testing.T) {
	m := newMetrics()
	handler := m.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	for i := range 100 {
		label := fmt.Sprintf("PRIVATE%d", i)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(label, "/user/private@example.com", nil))
		m.RecordAction(label, label)
		m.RecordAuth(label)
		m.RecordGrafana(label, -1, time.Millisecond)
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	for _, private := range []string{"PRIVATE", "private@example.com"} {
		if strings.Contains(body, private) {
			t.Fatal("untrusted identity or method leaked into metrics")
		}
	}
	if len(m.http) != 1 || len(m.actions) != 1 || len(m.grafana) != 1 || len(m.auth) != 0 {
		t.Fatal("untrusted labels expanded metric cardinality")
	}
	if !strings.Contains(body, `grafana_mcp_grafana_requests_total{method="other",status="0"} 100`) {
		t.Fatal("missing bounded transport failure counter")
	}
}

func TestMetricsRecordOutcomesDuringConcurrentScrapes(t *testing.T) {
	m := newMetrics()
	m.SetVersion("test-version")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			m.RecordAction("reissue", "partial")
			m.RecordAuth("authorization_denial")
			m.RecordGrafana(http.MethodDelete, 502, time.Millisecond)
			m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
		})
	}
	wg.Wait()
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, expected := range []string{
		`grafana_mcp_actions_total{action="reissue",result="partial"} 20`,
		`grafana_mcp_auth_failures_total{kind="authorization_denial"} 20`,
		`grafana_mcp_build_info{version="test-version"} 1`,
	} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("missing expected metric %s", expected)
		}
	}
}
