package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig writes content to a temp file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	// An almost-empty file should come back fully populated with defaults.
	cfg, err := loadConfig(writeConfig(t, "company_name: \"ACME\"\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CompanyName != "ACME" {
		t.Errorf("company_name = %q, want ACME", cfg.CompanyName)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("listen_addr = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.DataDir != "/data" {
		t.Errorf("data_dir = %q, want /data", cfg.DataDir)
	}
	if cfg.DBDriver != driverSQLite {
		t.Errorf("db_driver = %q, want %q", cfg.DBDriver, driverSQLite)
	}
	if cfg.AuthUser != "admin" {
		t.Errorf("auth_user = %q, want admin", cfg.AuthUser)
	}
	if cfg.DBConnectTimeout != "5s" {
		t.Errorf("db_connect_timeout = %q, want 5s", cfg.DBConnectTimeout)
	}
	if got := cfg.ConnectTimeout(); got != 5*time.Second {
		t.Errorf("ConnectTimeout() = %s, want 5s", got)
	}
}

func TestLoadConfigConnectTimeoutOverride(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, "db_connect_timeout: \"2500ms\"\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.ConnectTimeout(); got != 2500*time.Millisecond {
		t.Errorf("ConnectTimeout() = %s, want 2.5s", got)
	}
}

func TestLoadConfigInvalidConnectTimeout(t *testing.T) {
	for _, v := range []string{"nope", "0s", "-1s"} {
		if _, err := loadConfig(writeConfig(t, "db_connect_timeout: \""+v+"\"\n")); err == nil {
			t.Errorf("expected error for db_connect_timeout %q, got nil", v)
		}
	}
}

func TestLoadConfigInvalidLogLevel(t *testing.T) {
	_, err := loadConfig(writeConfig(t, "log_level: \"loud\"\n"))
	if err == nil {
		t.Fatal("expected error for invalid log_level, got nil")
	}
}

func TestLoadConfigPostgresRequiresURL(t *testing.T) {
	_, err := loadConfig(writeConfig(t, "db_driver: \"postgres\"\n"))
	if err == nil {
		t.Fatal("expected error: postgres without database_url")
	}
}

func TestLoadConfigPostgresOK(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t,
		"db_driver: \"postgres\"\ndatabase_url: \"postgres://u:p@db/todo\"\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DBDriver != driverPostgres {
		t.Errorf("db_driver = %q, want %q", cfg.DBDriver, driverPostgres)
	}
}

func TestLoadConfigInvalidDriver(t *testing.T) {
	_, err := loadConfig(writeConfig(t, "db_driver: \"mongodb\"\n"))
	if err == nil {
		t.Fatal("expected error for unknown db_driver, got nil")
	}
}
