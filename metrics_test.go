package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// scrape fetches /metrics from the separate metrics listener (no login) and
// returns the body.
func scrape(t *testing.T, srv *server) string {
	t.Helper()
	ms := httptest.NewServer(srv.met.routes())
	t.Cleanup(ms.Close)
	resp, err := http.Get(ms.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestMetrics(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	client := loginClient(t, ts)

	// Two todos, one of them done (id 1), and a 404 that must not leak its URL.
	for _, title := range []string{"a", "b"} {
		resp, err := client.PostForm(ts.URL+"/todos", url.Values{"title": {title}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	for _, p := range []string{"/todos/1/toggle", "/no-such-page"} {
		resp, err := client.Post(ts.URL+p, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	out := scrape(t, srv)
	for _, want := range []string{
		`db_up 1`,
		`todos{state="open"} 1`,
		`todos{state="done"} 1`,
		`http_requests_total{path="/login",status="303"} 1`,
		`http_requests_total{path="/todos",status="303"} 2`,
		`http_requests_total{path="/todos/{id}/toggle",status="303"} 1`,
		`http_requests_total{path="unmatched",status="404"} 1`,
		`go_goroutines `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
	if strings.Contains(out, "no-such-page") {
		t.Errorf("raw URL leaked into a label:\n%s", out)
	}
	// Scrapes go to the metrics listener, so they are not counted as requests.
	if strings.Contains(scrape(t, srv), `path="/metrics"`) {
		t.Errorf("scrapes must not show up in http_requests_total")
	}
}

func TestMetricsDBDown(t *testing.T) {
	srv := newTestServer(t)

	_ = srv.store.Close() // simulate a lost database
	out := scrape(t, srv)
	if !strings.Contains(out, "db_up 0") {
		t.Errorf("want db_up 0 after closing the store, got:\n%s", out)
	}
	if strings.Contains(out, "todos{") {
		t.Errorf("want no todos series without a database, got:\n%s", out)
	}
}

// TestMetricsNotOnAppPort: the app listener must not serve the Prometheus
// text — metrics live on their own port so the Ingress cannot expose them.
func TestMetricsNotOnAppPort(t *testing.T) {
	ts := httptest.NewServer(newTestServer(t).routes())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), "db_up") || strings.Contains(string(b), "go_goroutines") {
		t.Errorf("app port served Prometheus metrics:\n%s", b)
	}
}

// TestMetricsListenerServesOnlyMetrics: everything but GET /metrics is a 404.
func TestMetricsListenerServesOnlyMetrics(t *testing.T) {
	ms := httptest.NewServer(newTestServer(t).met.routes())
	t.Cleanup(ms.Close)

	for _, p := range []string{"/", "/healthz", "/login"} {
		resp, err := http.Get(ms.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("metrics listener GET %s = %d, want 404", p, resp.StatusCode)
		}
	}
}
