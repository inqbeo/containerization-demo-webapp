package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServerWithLog is like newTestServer but lets the test inspect the log
// output and toggle LogRequests.
func newTestServerWithLog(t *testing.T, logRequests bool) (*server, *bytes.Buffer) {
	t.Helper()
	store, err := openTestStore(t, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hash, err := hashPassword(testPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := Config{CompanyName: "TestCo", Theme: "coral", AuthUser: testUser, LogRequests: logRequests}
	srv, err := newServer(cfg, store, log, testUser, hash)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv, &buf
}

func TestRequestLoggingOn(t *testing.T) {
	srv, buf := newTestServerWithLog(t, true)
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)

	if _, err := http.Get(ts.URL + "/healthz"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "msg=request") {
		t.Errorf("expected a request log line, got:\n%s", out)
	}
	if !strings.Contains(out, "path=/healthz") || !strings.Contains(out, "status=200") {
		t.Errorf("request log missing path/status:\n%s", out)
	}
}

func TestRequestLoggingOff(t *testing.T) {
	srv, buf := newTestServerWithLog(t, false)
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)

	if _, err := http.Get(ts.URL + "/healthz"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "msg=request") {
		t.Errorf("did not expect request logging when LogRequests is false:\n%s", buf.String())
	}
}
