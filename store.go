package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// Pure-Go SQLite driver (no CGO) — registers the "sqlite" driver name.
	// CGO_ENABLED=0 → static binary → tiny multi-stage image, distroless-capable.
	_ "modernc.org/sqlite"

	// Pure-Go Postgres driver — registers the "pgx" driver name with database/sql.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Todo is one to-do item. created_at is kept as a string for simplicity; it is
// only ever displayed, never parsed.
type Todo struct {
	ID        int64
	Title     string
	Done      bool
	CreatedAt string
}

// Store is the storage abstraction the rest of the app depends on. Keeping the
// HTTP handlers behind this interface is what lets us swap SQLite for Postgres
// without touching any handler code — the same lever we pull on Day 2 when the
// state is "externalised" into its own container/Pod.
type Store interface {
	List(ctx context.Context) ([]Todo, error)
	Add(ctx context.Context, title string) error
	Toggle(ctx context.Context, id int64) error
	Delete(ctx context.Context, id int64) error
	Ping(ctx context.Context) error // used by /healthz

	// GetCredential returns the auth credential persisted in the database.
	// ok is false when the database has not been initialised with one yet
	// (first start). The password is stored ONLY as a bcrypt hash.
	GetCredential(ctx context.Context) (user, passwordHash string, ok bool, err error)

	// SetCredential persists the auth credential. Called exactly once, on the
	// first start, to "lock in" the initial password. Later starts read it
	// back via GetCredential and never overwrite it — so changing the supplied
	// password env var after the database exists has no effect.
	SetCredential(ctx context.Context, user, passwordHash string) error

	// CreateSession persists a login session: a random token and its absolute
	// expiry. Sessions live in the DATABASE (not in process memory), so a login
	// survives a restart and is shared across replicas — the same "externalise
	// the state" lesson as the todo data, now applied to session state.
	CreateSession(ctx context.Context, token string, expiresAt time.Time) error

	// SessionValid reports whether token names a session that exists and has
	// not yet expired.
	SessionValid(ctx context.Context, token string) (bool, error)

	// DeleteSession removes a single session (logout).
	DeleteSession(ctx context.Context, token string) error

	// DeleteExpiredSessions prunes sessions whose expiry has passed so the
	// table does not grow without bound (this app sweeps on each login).
	DeleteExpiredSessions(ctx context.Context) error

	Close() error
}

// openStore builds the Store selected by the config. Both backends share the
// same database/sql-based implementation; only the driver, DSN and a few SQL
// dialect details differ.
func openStore(cfg Config) (Store, error) {
	switch cfg.DBDriver {
	case driverPostgres:
		return openPostgresStore(cfg.DatabaseURL)
	default:
		return openSQLiteStore(cfg.DataDir)
	}
}

// ----------------------------------------------------------------------------
// SQLite backend
// ----------------------------------------------------------------------------

// sqlStore is a database/sql-backed Store. The placeholder style differs
// between SQLite ("?") and Postgres ("$1"), so we remember which one to use.
type sqlStore struct {
	db          *sql.DB
	placeholder placeholderStyle
}

type placeholderStyle int

const (
	placeholderQuestion placeholderStyle = iota // SQLite, MySQL: ?
	placeholderDollar                           // Postgres: $1, $2, ...
)

// openSQLiteStore opens (and if needed creates) the SQLite database file at
// {dataDir}/todo.db and ensures the schema exists.
func openSQLiteStore(dataDir string) (Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", dataDir, err)
	}
	dbPath := filepath.Join(dataDir, "todo.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dbPath, err)
	}
	// SQLite is a single file; one connection avoids "database is locked".
	db.SetMaxOpenConns(1)

	s := &sqlStore{db: db, placeholder: placeholderQuestion}
	if err := s.initSQLite(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// initSQLite creates the table idempotently (no migration tooling — the schema
// is established with CREATE TABLE IF NOT EXISTS on every start).
func (s *sqlStore) initSQLite(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS todos (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT    NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);
-- Single-row table (id is pinned to 1) holding the auth credential. The
-- password lives here only as a bcrypt hash, never in plaintext.
CREATE TABLE IF NOT EXISTS auth (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT    NOT NULL,
    password_hash TEXT    NOT NULL
);
-- Login sessions: token -> absolute expiry (unix seconds, an integer so the
-- exact same SQL works on SQLite and Postgres). Keeping sessions here, rather
-- than in process memory, is what lets a login survive a restart.
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT    PRIMARY KEY,
    expires_at INTEGER NOT NULL
);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create sqlite schema: %w", err)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Postgres backend
// ----------------------------------------------------------------------------

// openPostgresStore connects to Postgres via the pure-Go pgx stdlib driver and
// ensures the schema exists. The DSN comes from config (database_url).
func openPostgresStore(dsn string) (Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	s := &sqlStore{db: db, placeholder: placeholderDollar}
	if err := s.initPostgres(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// initPostgres creates the table idempotently using Postgres types/syntax.
func (s *sqlStore) initPostgres(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS todos (
    id         BIGSERIAL   PRIMARY KEY,
    title      TEXT        NOT NULL,
    done       BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Single-row table (id is pinned to 1) holding the auth credential. The
-- password lives here only as a bcrypt hash, never in plaintext.
CREATE TABLE IF NOT EXISTS auth (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT    NOT NULL,
    password_hash TEXT    NOT NULL
);
-- Login sessions: token -> absolute expiry (unix seconds as BIGINT, matching
-- the SQLite integer column so the same SQL runs on both backends). Sessions
-- in the database survive a restart and are shared across replicas.
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT   PRIMARY KEY,
    expires_at BIGINT NOT NULL
);`
	// When several replicas start against an empty database at once, a
	// transaction-scoped advisory lock makes exactly one run the DDL at a time
	// (the rest block, then find the tables already there) — this closes the
	// small window where concurrent CREATE TABLE could collide. The lock is
	// released automatically on commit. (SQLite needs none: single file, single
	// connection, one process.)
	const migrateLockKey int64 = 4711003
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create postgres schema: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("create postgres schema: lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create postgres schema: %w", err)
	}
	return tx.Commit()
}

// ----------------------------------------------------------------------------
// Shared Store implementation
// ----------------------------------------------------------------------------

// q rewrites the "?" placeholders in a query to the dialect this store uses.
// SQLite keeps "?"; Postgres gets "$1", "$2", ... in order. Keeping the SQL
// written with "?" makes the queries below read the same for both backends.
func (s *sqlStore) q(query string) string {
	if s.placeholder == placeholderQuestion {
		return query
	}
	var b []byte
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b = append(b, '$')
			b = append(b, []byte(fmt.Sprintf("%d", n))...)
			continue
		}
		b = append(b, query[i])
	}
	return string(b)
}

func (s *sqlStore) List(ctx context.Context) ([]Todo, error) {
	// Open items first, then completed ones; newest first within each group.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, done, created_at FROM todos ORDER BY done ASC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	defer rows.Close()

	var todos []Todo
	for rows.Next() {
		var t Todo
		// scanning created_at into a string works for both the SQLite TEXT
		// column and the Postgres timestamptz (driver formats it for us).
		if err := rows.Scan(&t.ID, &t.Title, &t.Done, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan todo: %w", err)
		}
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

func (s *sqlStore) Add(ctx context.Context, title string) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO todos (title) VALUES (?)`), title)
	if err != nil {
		return fmt.Errorf("add todo: %w", err)
	}
	return nil
}

func (s *sqlStore) Toggle(ctx context.Context, id int64) error {
	// NOT done flips the boolean on both backends (SQLite stores 0/1 as a
	// boolean-ish integer; "NOT done" works there too).
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE todos SET done = NOT done WHERE id = ?`), id)
	if err != nil {
		return fmt.Errorf("toggle todo %d: %w", id, err)
	}
	return nil
}

func (s *sqlStore) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM todos WHERE id = ?`), id)
	if err != nil {
		return fmt.Errorf("delete todo %d: %w", id, err)
	}
	return nil
}

func (s *sqlStore) GetCredential(ctx context.Context) (string, string, bool, error) {
	var user, hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT username, password_hash FROM auth WHERE id = 1`).Scan(&user, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil // not initialised yet
	}
	if err != nil {
		return "", "", false, fmt.Errorf("get credential: %w", err)
	}
	return user, hash, true, nil
}

func (s *sqlStore) SetCredential(ctx context.Context, user, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`INSERT INTO auth (id, username, password_hash) VALUES (1, ?, ?)`),
		user, passwordHash)
	if err != nil {
		return fmt.Errorf("set credential: %w", err)
	}
	return nil
}

func (s *sqlStore) CreateSession(ctx context.Context, token string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`INSERT INTO sessions (token, expires_at) VALUES (?, ?)`),
		token, expiresAt.Unix())
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *sqlStore) SessionValid(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		s.q(`SELECT 1 FROM sessions WHERE token = ? AND expires_at > ?`),
		token, time.Now().Unix()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // unknown or expired
	}
	if err != nil {
		return false, fmt.Errorf("session valid: %w", err)
	}
	return true, nil
}

func (s *sqlStore) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE token = ?`), token)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *sqlStore) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`DELETE FROM sessions WHERE expires_at <= ?`), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

func (s *sqlStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *sqlStore) Close() error {
	return s.db.Close()
}
