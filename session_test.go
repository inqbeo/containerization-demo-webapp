package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestSessionPersistsAcrossRestart is the headline behaviour of this change:
// sessions live in the database, not in process memory, so a cookie obtained
// before a "restart" (close + reopen the SAME SQLite store) still authenticates
// afterwards.
func TestSessionPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hash, err := hashPassword(testPass)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{CompanyName: "TestCo", Theme: "coral", AuthUser: testUser}

	// --- first "process": open the store, log in, capture the cookie ---
	store1, err := openTestStore(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	srv1, err := newServer(cfg, store1, log, testUser, hash)
	if err != nil {
		t.Fatal(err)
	}
	ts1 := httptest.NewServer(srv1.routes())
	client := loginClient(t, ts1)

	u, _ := url.Parse(ts1.URL)
	cookies := client.Jar.Cookies(u)
	ts1.Close()
	_ = store1.Close()
	if len(cookies) == 0 {
		t.Fatal("no session cookie was issued")
	}

	// --- second "process": reopen the SAME database directory ---
	store2, err := openTestStore(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	srv2, err := newServer(cfg, store2, log, testUser, hash)
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(srv2.routes())
	t.Cleanup(ts2.Close)

	// Replay the OLD cookie against the NEW server — it must still be authorised.
	req, _ := http.NewRequest(http.MethodGet, ts2.URL+"/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / with a pre-restart cookie = %d, want 200 (the session should persist)", resp.StatusCode)
	}
}

// TestSessionStoreLifecycle covers the Store's session methods directly: a valid
// session, an expired one, deletion, and pruning.
func TestSessionStoreLifecycle(t *testing.T) {
	store, err := openTestStore(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	// A future-dated session is valid.
	if err := store.CreateSession(ctx, "tok-ok", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.SessionValid(ctx, "tok-ok"); err != nil || !ok {
		t.Fatalf("tok-ok should be valid (ok=%v err=%v)", ok, err)
	}

	// A past-dated session is not valid (expired).
	if err := store.CreateSession(ctx, "tok-exp", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.SessionValid(ctx, "tok-exp"); ok {
		t.Error("expired session should be invalid")
	}

	// Unknown and empty tokens are not valid.
	if ok, _ := store.SessionValid(ctx, "nope"); ok {
		t.Error("unknown token should be invalid")
	}
	if ok, _ := store.SessionValid(ctx, ""); ok {
		t.Error("empty token should be invalid")
	}

	// Deletion invalidates a live session.
	if err := store.DeleteSession(ctx, "tok-ok"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.SessionValid(ctx, "tok-ok"); ok {
		t.Error("deleted session should be invalid")
	}

	// Pruning removes the expired row (no error, and it stays invalid).
	if err := store.DeleteExpiredSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.SessionValid(ctx, "tok-exp"); ok {
		t.Error("pruned session should be invalid")
	}
}
