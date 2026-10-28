package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// failingStore wraps a real store and, while down is set, fails EVERY call —
// a database that went away while the app was running. Clearing down again
// simulates the database coming back.
type failingStore struct {
	Store
	down atomic.Bool
}

var errDBDown = errors.New("simulated: database unreachable")

func (f *failingStore) err() error {
	if f.down.Load() {
		return errDBDown
	}
	return nil
}

func (f *failingStore) List(ctx context.Context) ([]Todo, error) {
	if err := f.err(); err != nil {
		return nil, err
	}
	return f.Store.List(ctx)
}

func (f *failingStore) Add(ctx context.Context, title string) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.Add(ctx, title)
}

func (f *failingStore) Toggle(ctx context.Context, id int64) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.Toggle(ctx, id)
}

func (f *failingStore) Delete(ctx context.Context, id int64) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.Delete(ctx, id)
}

func (f *failingStore) Ping(ctx context.Context) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.Ping(ctx)
}

func (f *failingStore) GetCredential(ctx context.Context) (string, string, bool, error) {
	if err := f.err(); err != nil {
		return "", "", false, err
	}
	return f.Store.GetCredential(ctx)
}

func (f *failingStore) SetCredential(ctx context.Context, user, hash string) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.SetCredential(ctx, user, hash)
}

func (f *failingStore) CreateSession(ctx context.Context, token string, exp time.Time) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.CreateSession(ctx, token, exp)
}

func (f *failingStore) SessionValid(ctx context.Context, token string) (bool, error) {
	if err := f.err(); err != nil {
		return false, err
	}
	return f.Store.SessionValid(ctx, token)
}

func (f *failingStore) DeleteSession(ctx context.Context, token string) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.DeleteSession(ctx, token)
}

func (f *failingStore) DeleteExpiredSessions(ctx context.Context) error {
	if err := f.err(); err != nil {
		return err
	}
	return f.Store.DeleteExpiredSessions(ctx)
}

// newFlakyServer returns a test server on a failingStore (initially healthy),
// the store itself, and an httptest server for the app port.
func newFlakyServer(t *testing.T) (*server, *failingStore, *httptest.Server) {
	t.Helper()
	real, err := openTestStore(t, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	fs := &failingStore{Store: real}
	srv := newTestServerWithStore(t, fs)
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	return srv, fs, ts
}

// get performs a GET with client (redirects not followed) and returns status,
// Location header and body.
func get(t *testing.T, client *http.Client, u string) (int, string, string) {
	t.Helper()
	resp, err := client.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

func TestHealthEndpoints(t *testing.T) {
	_, fs, ts := newFlakyServer(t)
	client := http.DefaultClient

	cases := []struct {
		path     string
		up, down int
		downBody string
	}{
		{"/livez", 200, 200, "ok"},
		{"/readyz", 200, 503, "unavailable"},
		{"/healthz", 200, 503, "unavailable"},
	}
	for _, c := range cases {
		fs.down.Store(false)
		if code, _, body := get(t, client, ts.URL+c.path); code != c.up || body != "ok" {
			t.Errorf("healthy GET %s = %d %q, want %d \"ok\"", c.path, code, body, c.up)
		}
		fs.down.Store(true)
		if code, _, body := get(t, client, ts.URL+c.path); code != c.down || body != c.downBody {
			t.Errorf("db down GET %s = %d %q, want %d %q", c.path, code, body, c.down, c.downBody)
		}
	}
}

func TestDatabaseLostWhileRunning(t *testing.T) {
	srv, fs, ts := newFlakyServer(t)
	client := loginClient(t, ts)

	fs.down.Store(true)

	// The UI answers 503 with the error page — and must NOT send the user to
	// /login, the session simply could not be checked.
	code, loc, body := get(t, client, ts.URL+"/")
	if code != http.StatusServiceUnavailable {
		t.Errorf("GET / with db down = %d, want 503", code)
	}
	if loc != "" {
		t.Errorf("GET / with db down redirected to %q, want no redirect", loc)
	}
	if !strings.Contains(body, "Database unavailable") {
		t.Errorf("error page missing heading:\n%s", body)
	}
	if strings.Contains(body, errDBDown.Error()) {
		t.Errorf("database error leaked into the HTML")
	}

	resp, err := client.PostForm(ts.URL+"/todos", url.Values{"title": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("POST /todos with db down = %d, want 503", resp.StatusCode)
	}

	// Metrics keep working and report the outage.
	out := scrape(t, srv)
	if !strings.Contains(out, "db_up 0") || strings.Contains(out, "todos{") {
		t.Errorf("want db_up 0 and no todos series, got:\n%s", out)
	}

	// Database back → everything works again, same session, no restart.
	fs.down.Store(false)
	if code, _, _ := get(t, client, ts.URL+"/"); code != http.StatusOK {
		t.Errorf("GET / after recovery = %d, want 200", code)
	}
	if !strings.Contains(scrape(t, srv), "db_up 1") {
		t.Errorf("want db_up 1 after recovery")
	}
}

// TestLoginWithDatabaseDown: correct credentials, but the session cannot be
// stored → 503, not 500 and not a half-logged-in state.
func TestLoginWithDatabaseDown(t *testing.T) {
	_, fs, ts := newFlakyServer(t)
	fs.down.Store(true)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(ts.URL+"/login", url.Values{"username": {testUser}, "password": {testPass}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("login with db down = %d, want 503", resp.StatusCode)
	}
}
