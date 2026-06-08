package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Authentication model (kept deliberately tiny — this is teaching material):
//
//   * ONE user. No groups, no roles, no permissions. Logged in → full access.
//   * Form-based login (a real <form>, not the browser's Basic Auth popup),
//     backed by an in-memory session store and an HttpOnly cookie.
//   * The password is never stored in plaintext: only a bcrypt hash is kept,
//     and it lives in the DATABASE. It is written once on first start and frozen
//     afterwards — changing the supplied password env var later has no effect.
//
// For production you'd add CSRF protection, TLS-only "Secure" cookies, and a
// persistent/shared session store. Those are intentionally omitted here.

const sessionCookieName = "todo_session"
const sessionTTL = 12 * time.Hour

// generatePassword returns a URL-safe random password. Used when no password is
// supplied via config/env, so the app is never accidentally left with a blank
// or hard-coded one.
func generatePassword() (string, error) {
	return randomToken(18) // 18 bytes → 24 base64 chars
}

// hashPassword returns a bcrypt hash of the given plaintext password.
func hashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// randomToken returns n random bytes as a URL-safe base64 string.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// resolveCredential establishes the effective auth credential at startup.
//
// First start (DB has no credential): take the supplied password from config
// (env → config via the entrypoint) or, if none was given, generate a random
// one and log it once. Hash it with bcrypt and persist it.
//
// Later starts (DB already has a credential): use the stored hash and IGNORE
// whatever password is currently supplied — the database owns the password now.
func resolveCredential(ctx context.Context, store Store, cfg Config, log *slog.Logger) (user, hash string, err error) {
	if u, h, ok, err := store.GetCredential(ctx); err != nil {
		return "", "", err
	} else if ok {
		log.Info("auth: using credential stored in database (supplied password ignored)", "username", u)
		return u, h, nil
	}

	// First start — initialise the credential.
	plain := cfg.AuthPassword
	if plain == "" {
		plain, err = generatePassword()
		if err != nil {
			return "", "", err
		}
		log.Warn("auth: no password supplied — generated one for this database",
			"username", cfg.AuthUser, "password", plain)
	} else {
		log.Info("auth: initialising database with supplied password", "username", cfg.AuthUser)
	}

	hash, err = hashPassword(plain)
	if err != nil {
		return "", "", err
	}
	if err := store.SetCredential(ctx, cfg.AuthUser, hash); err != nil {
		return "", "", err
	}
	return cfg.AuthUser, hash, nil
}

// sessionStore is a tiny in-memory set of valid session tokens with expiry.
// In-memory means sessions are lost on restart (fine for the lab) and not shared
// across replicas — a teaching point for Day 2 (why you externalise session
// state, just like the todo data).
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token → expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

// create issues a new session token valid for sessionTTL.
func (s *sessionStore) create() (string, error) {
	token, err := randomToken(24)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return token, nil
}

// valid reports whether token is a known, unexpired session.
func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

// destroy removes a session token (logout).
func (s *sessionStore) destroy(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// checkPassword verifies a plaintext password against the bcrypt hash and the
// username in constant time.
func checkPassword(user, hash, gotUser, gotPass string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(gotUser)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(gotPass)) == nil
	return userOK && passOK
}
