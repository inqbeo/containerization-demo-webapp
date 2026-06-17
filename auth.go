package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Authentication model (kept deliberately tiny — this is teaching material):
//
//   * ONE user. No groups, no roles, no permissions. Logged in → full access.
//   * Form-based login (a real <form>, not the browser's Basic Auth popup),
//     backed by server-side sessions PERSISTED IN THE DATABASE and an HttpOnly
//     cookie. Because the session lives in the database (sqlite or postgres),
//     a login survives a process restart and is shared across replicas — the
//     same "externalise the state" lesson as the todo data (see store.go).
//   * The password is never stored in plaintext: only a bcrypt hash is kept,
//     and it lives in the DATABASE. It is written once on first start and frozen
//     afterwards — changing the supplied password env var later has no effect.
//
// For production you'd add CSRF protection and TLS-only "Secure" cookies. Those
// are intentionally omitted here.

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

// Session storage now lives in the database (see the sessions table + the
// CreateSession/SessionValid/DeleteSession methods in store.go). The token is
// still generated here with randomToken; the handlers persist and look it up
// through the Store.

// checkPassword verifies a plaintext password against the bcrypt hash and the
// username in constant time.
func checkPassword(user, hash, gotUser, gotPass string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(gotUser)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(gotPass)) == nil
	return userOK && passOK
}
