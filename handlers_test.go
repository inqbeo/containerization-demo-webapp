package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testUser = "admin"
const testPass = "s3cret-pw"

// openTestStore opens a throwaway SQLite store for tests: a quiet (discarded)
// logger and a generous connect timeout. Keeps the call sites short now that
// openSQLiteStore takes a timeout and logger.
func openTestStore(t *testing.T, dir string) (Store, error) {
	t.Helper()
	return openSQLiteStore(dir, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// newTestServer builds a server backed by a real (temp-dir) SQLite store with a
// known credential, ready to drive via httptest.
func newTestServer(t *testing.T) *server {
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
	cfg := Config{CompanyName: "TestCo", Theme: "coral", AuthUser: testUser}
	srv, err := newServer(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)), testUser, hash)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

// loginClient returns an http.Client (with a cookie jar) already authenticated
// against ts, plus the server it points at.
func loginClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects automatically
		},
	}
	form := url.Values{"username": {testUser}, "password": {testPass}}
	resp, err := client.PostForm(ts.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303", resp.StatusCode)
	}
	return client
}

func TestUnauthenticatedRedirectsToLogin(t *testing.T) {
	ts := httptest.NewServer(newTestServer(t).routes())
	t.Cleanup(ts.Close)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET / unauthenticated = %d, want 303 redirect", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("redirect to %q, want /login", loc)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	ts := httptest.NewServer(newTestServer(t).routes())
	t.Cleanup(ts.Close)

	resp, err := http.PostForm(ts.URL+"/login", url.Values{
		"username": {testUser}, "password": {"wrong"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad login = %d, want 401", resp.StatusCode)
	}
}

func TestHealthzIsPublic(t *testing.T) {
	ts := httptest.NewServer(newTestServer(t).routes())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("GET /healthz = %d %q, want 200 \"ok\"", resp.StatusCode, body)
	}
}

func TestAuthenticatedFlowAndEscaping(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	client := loginClient(t, ts)

	// Add a todo with an XSS attempt in the title via the HTTP handler.
	resp, err := client.PostForm(ts.URL+"/todos", url.Values{"title": {"<b>buy milk</b>"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /todos = %d, want 303", resp.StatusCode)
	}

	// Fetch the page and check rendering + escaping.
	resp, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if !strings.Contains(page, "TestCo") {
		t.Error("company name not rendered")
	}
	if strings.Contains(page, "<b>buy milk</b>") {
		t.Error("title was NOT escaped — XSS risk")
	}
	if !strings.Contains(page, "&lt;b&gt;buy milk&lt;/b&gt;") {
		t.Error("escaped title not found in output")
	}

	// Toggle + delete directly via the store, then verify counts.
	todos, err := srv.store.List(context.Background())
	if err != nil || len(todos) != 1 {
		t.Fatalf("expected 1 todo, got %d (err=%v)", len(todos), err)
	}
	id := todos[0].ID
	if err := srv.store.Toggle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	todos, _ = srv.store.List(context.Background())
	if !todos[0].Done {
		t.Error("todo should be done after toggle")
	}
	if err := srv.store.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	todos, _ = srv.store.List(context.Background())
	if len(todos) != 0 {
		t.Errorf("after delete got %d todos, want 0", len(todos))
	}
}

func TestLogout(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	client := loginClient(t, ts)

	// Logged in: GET / should be 200.
	resp, _ := client.Get(ts.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / while logged in = %d, want 200", resp.StatusCode)
	}

	// Log out, then GET / should redirect to login again.
	resp, _ = client.PostForm(ts.URL+"/logout", nil)
	resp.Body.Close()

	resp, _ = client.Get(ts.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET / after logout = %d, want 303 redirect", resp.StatusCode)
	}
}

// TestCredentialFrozenAfterInit verifies the password is owned by the DB: once
// SetCredential has run, a different supplied password is ignored.
func TestCredentialFrozenAfterInit(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First start: initialise with "first-pw".
	store1, err := openTestStore(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg1 := Config{AuthUser: "admin", AuthPassword: "first-pw"}
	user, hash, err := resolveCredential(context.Background(), store1, cfg1, log)
	if err != nil {
		t.Fatal(err)
	}
	_ = store1.Close()
	if !checkPassword(user, hash, "admin", "first-pw") {
		t.Fatal("first password should verify")
	}

	// Second start with a DIFFERENT supplied password — must be ignored.
	store2, err := openTestStore(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	cfg2 := Config{AuthUser: "admin", AuthPassword: "changed-pw"}
	user2, hash2, err := resolveCredential(context.Background(), store2, cfg2, log)
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(user2, hash2, "admin", "first-pw") {
		t.Error("original password should still work after restart")
	}
	if checkPassword(user2, hash2, "admin", "changed-pw") {
		t.Error("changed password should be IGNORED — DB owns the credential")
	}
}
