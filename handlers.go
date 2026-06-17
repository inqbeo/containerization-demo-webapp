package main

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// templatesFS embeds the HTML templates into the binary, so there are no
// external template files to ship at runtime (everything lives in one binary).
//
//go:embed templates/*.html
var templatesFS embed.FS

// staticFS embeds the static assets (the official Inqbeo logo SVGs, one per
// theme) into the binary. They are served as standalone files so each SVG's
// internal styles stay scoped to itself.
//
//go:embed static/*.svg
var staticFS embed.FS

// server bundles the dependencies the HTTP handlers need.
type server struct {
	cfg   Config
	store Store
	tmpl  *template.Template
	log   *slog.Logger

	// Resolved auth credential: the effective username and the bcrypt password
	// hash (read from the database, not from the config plaintext). See main().
	authUser string
	authHash string
}

// newServer parses the embedded templates and wires up the handler set. The
// authUser/authHash are the effective credentials resolved at startup.
func newServer(cfg Config, store Store, log *slog.Logger, authUser, authHash string) (*server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &server{
		cfg:      cfg,
		store:    store,
		tmpl:     tmpl,
		log:      log,
		authUser: authUser,
		authHash: authHash,
	}, nil
}

// routes registers all routes on a fresh ServeMux. Go 1.22+ method patterns
// keep the routing readable without any third-party router.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /static/", http.FileServerFS(staticFS)) // logo SVGs (public)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// Protected routes — wrapped with requireLogin below.
	app := http.NewServeMux()
	app.HandleFunc("GET /{$}", s.handleIndex)
	app.HandleFunc("POST /todos", s.handleAdd)
	app.HandleFunc("POST /todos/{id}/toggle", s.handleToggle)
	app.HandleFunc("POST /todos/{id}/delete", s.handleDelete)
	mux.Handle("/", s.requireLogin(app))

	return mux
}

// requireLogin redirects unauthenticated requests to the login page. There are
// no roles or permissions: a valid session means full access.
func (s *server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggedIn reports whether the request carries a valid session cookie. The
// session is looked up in the database (the Store), so it stays valid across
// restarts. On a store error we fail closed (treat as not logged in) rather
// than letting the request through.
func (s *server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	ok, err := s.store.SessionValid(r.Context(), c.Value)
	if err != nil {
		s.log.Error("check session", "err", err)
		return false
	}
	return ok
}

// ----------------------------------------------------------------------------
// View model
// ----------------------------------------------------------------------------

// baseData carries the fields every page needs for branding/skinning.
type baseData struct {
	CompanyName string
	Theme       string
}

func (s *server) base() baseData {
	return baseData{CompanyName: s.cfg.CompanyName, Theme: s.cfg.Theme}
}

// indexData is the view model for the todo list page.
type indexData struct {
	baseData
	User      string
	Todos     []Todo
	OpenCount int
	DoneCount int
}

// loginData is the view model for the login page.
type loginData struct {
	baseData
	Error string
}

// ----------------------------------------------------------------------------
// Auth handlers (form-based)
// ----------------------------------------------------------------------------

func (s *server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	// Already logged in? Skip the form.
	if s.loggedIn(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", loginData{baseData: s.base()})
}

func (s *server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	user := strings.TrimSpace(r.FormValue("username"))
	pass := r.FormValue("password")

	if !checkPassword(s.authUser, s.authHash, user, pass) {
		s.log.Warn("failed login attempt", "username", user)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login.html", loginData{baseData: s.base(), Error: "Invalid username or password."})
		return
	}

	token, err := randomToken(24)
	if err != nil {
		s.log.Error("create session token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Best-effort sweep so expired rows don't accumulate (teaching app — no
	// background janitor). A failure here must not block a valid login.
	if err := s.store.DeleteExpiredSessions(r.Context()); err != nil {
		s.log.Warn("prune expired sessions", "err", err)
	}
	if err := s.store.CreateSession(r.Context(), token, time.Now().Add(sessionTTL)); err != nil {
		s.log.Error("create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
		// Secure is intentionally false so the lab works over plain HTTP.
		// Behind TLS you would set Secure: true.
	})
	s.log.Info("login successful", "username", user)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.store.DeleteSession(r.Context(), c.Value); err != nil {
			s.log.Warn("delete session", "err", err)
		}
	}
	// Expire the cookie on the client.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ----------------------------------------------------------------------------
// Todo handlers
// ----------------------------------------------------------------------------

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	todos, err := s.store.List(r.Context())
	if err != nil {
		s.log.Error("list todos", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := indexData{baseData: s.base(), User: s.authUser, Todos: todos}
	for _, t := range todos {
		if t.Done {
			data.DoneCount++
		} else {
			data.OpenCount++
		}
	}
	s.render(w, "index.html", data)
}

func (s *server) handleAdd(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := s.store.Add(r.Context(), title); err != nil {
		s.log.Error("add todo", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.log.Debug("todo added", "title", title)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.Toggle(r.Context(), id); err != nil {
		s.log.Error("toggle todo", "id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.Delete(r.Context(), id); err != nil {
		s.log.Error("delete todo", "id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleHealthz reports readiness by pinging the store. Returns 200 "ok" on
// success, 503 otherwise. Public (no login) so the Docker HEALTHCHECK and the
// Kubernetes liveness/readiness probes can reach it.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("healthz: store not reachable", "err", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// render executes a template. html/template escapes interpolated values by
// default — this is what prevents XSS through a todo title; we never bypass it.
func (s *server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render template", "name", name, "err", err)
	}
}

// pathID parses the {id} path value as an int64. On failure it writes a 400 and
// returns ok=false so the caller can stop.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
