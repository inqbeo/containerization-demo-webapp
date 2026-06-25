package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the effective runtime configuration of the app.
//
// Didactic note: the app reads its configuration ONLY from a YAML file.
// It never reads COMPANY_NAME, APP_PORT, ... directly from the environment.
// Turning environment variables into this file is the job of
// docker-entrypoint.sh. This separation is intentional — it mirrors the
// classic "docker-entrypoint.sh templates a config" pattern and keeps the
// Go program ignorant of how the file came to be (12-factor config).
type Config struct {
	CompanyName string `yaml:"company_name"`
	ListenAddr  string `yaml:"listen_addr"`
	DataDir     string `yaml:"data_dir"`
	LogLevel    string `yaml:"log_level"`

	// LogRequests, when true, logs one line per HTTP request (method, path,
	// status, duration). Off by default; driven by the LOG_REQUESTS env var
	// through the entrypoint. Handy for showing access logs in the lab.
	LogRequests bool `yaml:"log_requests"`

	// DBDriver selects the storage backend: "sqlite" (default, a file under
	// DataDir) or "postgres" (an external database addressed by DatabaseURL).
	// This is the "externalise the state" lever used in Block 6 / Day 2.
	DBDriver string `yaml:"db_driver"`

	// DatabaseURL is the connection string for the postgres backend, e.g.
	// "postgres://todo:todo@db:5432/todo?sslmode=disable". Ignored for sqlite.
	DatabaseURL string `yaml:"database_url"`

	// DBConnectTimeout bounds the INITIAL database connection attempt at startup,
	// as a Go duration string (e.g. "5s"). This matters in Kubernetes: when the
	// database is unreachable (a NetworkPolicy blocking egress, a wrong host, a
	// Pod not up yet), the connect would otherwise block indefinitely with no log
	// output. The readiness probe then restarts the Pod into CrashLoopBackOff with
	// no clue why. A bounded attempt fails fast and gets logged instead. Default
	// "5s"; driven by the DB_CONNECT_TIMEOUT env var through the entrypoint.
	DBConnectTimeout string `yaml:"db_connect_timeout"`

	// AuthUser / AuthPassword guard the UI with HTTP Basic Auth — the lab runs
	// on public machines, so the app must not be wide open. AuthUser defaults
	// to "admin". If AuthPassword is empty, main() generates a random one at
	// startup and logs it once (see auth.go). /healthz is always exempt so the
	// container/Kubernetes health probes keep working without credentials.
	AuthUser     string `yaml:"auth_user"`
	AuthPassword string `yaml:"auth_password"`

	// Theme selects the UI skin (a CSS palette). Built-in themes: "coral"
	// (default Inqbeo coral/pink), "navy" (Inqbeo dark navy) and "dark".
	Theme string `yaml:"theme"`
}

// validThemes are the skins the embedded CSS knows about.
var validThemes = map[string]bool{"coral": true, "navy": true, "dark": true, "sleepy": true}

// Recognised database drivers.
const (
	driverSQLite   = "sqlite"
	driverPostgres = "postgres"
)

// defaultConfigPath is used when the CONFIG_FILE env var is not set.
const defaultConfigPath = "/etc/todoapp/config.yaml"

// defaultDBConnectTimeout is the fallback for db_connect_timeout when unset.
const defaultDBConnectTimeout = "5s"

// configPath returns the path of the config file to read. This is the ONLY
// environment variable the Go program itself looks at.
func configPath() string {
	if p := os.Getenv("CONFIG_FILE"); p != "" {
		return p
	}
	return defaultConfigPath
}

// loadConfig reads and validates the YAML config file at path. Missing fields
// are filled with sensible defaults; invalid values are rejected with a clear
// error so main() can exit non-zero.
func loadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config file %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config file %q: %w", path, err)
	}

	// Fill in defaults for empty fields.
	if cfg.CompanyName == "" {
		cfg.CompanyName = "ACME Corp"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/data"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.DBConnectTimeout == "" {
		cfg.DBConnectTimeout = defaultDBConnectTimeout
	}
	if cfg.DBDriver == "" {
		cfg.DBDriver = driverSQLite
	}
	cfg.DBDriver = strings.ToLower(strings.TrimSpace(cfg.DBDriver))
	if cfg.AuthUser == "" {
		cfg.AuthUser = "admin"
	}
	if cfg.Theme == "" {
		cfg.Theme = "coral"
	}
	cfg.Theme = strings.ToLower(strings.TrimSpace(cfg.Theme))

	// Validate the log level so we fail fast on typos.
	if _, ok := parseLogLevel(cfg.LogLevel); !ok {
		return Config{}, fmt.Errorf("invalid log_level %q (want debug|info|warn|error)", cfg.LogLevel)
	}

	// Validate the connect timeout so a typo fails fast at startup, not later.
	if d, err := time.ParseDuration(cfg.DBConnectTimeout); err != nil || d <= 0 {
		return Config{}, fmt.Errorf("invalid db_connect_timeout %q (want a positive Go duration like %q)", cfg.DBConnectTimeout, defaultDBConnectTimeout)
	}

	// Validate the database selection.
	switch cfg.DBDriver {
	case driverSQLite:
		// nothing extra required; the file lives under data_dir
	case driverPostgres:
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("db_driver is %q but database_url is empty", driverPostgres)
		}
	default:
		return Config{}, fmt.Errorf("invalid db_driver %q (want %s|%s)", cfg.DBDriver, driverSQLite, driverPostgres)
	}

	// Validate the theme so a typo fails fast instead of rendering unstyled.
	if !validThemes[cfg.Theme] {
		return Config{}, fmt.Errorf("invalid theme %q (want coral|navy|dark)", cfg.Theme)
	}

	return cfg, nil
}

// ConnectTimeout returns the parsed db_connect_timeout. loadConfig has already
// validated it, so the parse cannot fail here; the fallback is defensive only.
func (c Config) ConnectTimeout() time.Duration {
	d, err := time.ParseDuration(c.DBConnectTimeout)
	if err != nil || d <= 0 {
		d, _ = time.ParseDuration(defaultDBConnectTimeout)
	}
	return d
}

// parseLogLevel maps a config string to an slog.Level. The bool reports
// whether the input was a recognised level.
func parseLogLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}
