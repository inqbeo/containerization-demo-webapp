package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// scrape fetches /metrics (public, no login) and returns the body.
func scrape(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/metrics")
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
	ts := httptest.NewServer(newTestServer(t).routes())
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

	out := scrape(t, ts)
	for _, want := range []string{
		`db_up 1`,
		`todos_total{state="open"} 1`,
		`todos_total{state="done"} 1`,
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
}

func TestMetricsDBDown(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)

	_ = srv.store.Close() // simulate a lost database
	out := scrape(t, ts)
	if !strings.Contains(out, "db_up 0") {
		t.Errorf("want db_up 0 after closing the store, got:\n%s", out)
	}
}
