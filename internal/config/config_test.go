package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	// Explicitly control env to avoid .env file interference via Makefile
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("PORT", "")
	t.Setenv("APP_ENV", "")
	t.Setenv("RUN_MIGRATIONS_ON_BOOT", "")
	t.Setenv("DB_MAX_OPEN_CONNS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("port: got %q, want 8080", cfg.Port)
	}
	if cfg.Env != "local" {
		t.Errorf("env: got %q, want local", cfg.Env)
	}
	if cfg.Migrate != false {
		t.Error("migrate should default to false")
	}
	if cfg.DB.MaxOpenConns != 20 {
		t.Errorf("max_open_conns: got %d, want 20", cfg.DB.MaxOpenConns)
	}
}

func TestLoad_MissingDatabaseURL(t *testing.T) {
	_ = os.Unsetenv("DATABASE_URL")
	_ = os.Unsetenv("DB_HOST")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when DATABASE_URL is missing")
	}
}

func TestLoad_CustomPort(t *testing.T) {
	_ = os.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	_ = os.Setenv("PORT", "3000")
	defer func() { _ = os.Unsetenv("DATABASE_URL") }()
	defer func() { _ = os.Unsetenv("PORT") }()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "3000" {
		t.Errorf("port: got %q, want 3000", cfg.Port)
	}
}

func TestValidate_ProductionWarnsMissingRedis(t *testing.T) {
	cfg := Config{
		Port: "8080",
		Env:  "production",
		DB:   DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second},
	}
	warnings := cfg.Validate()
	var found bool
	for _, w := range warnings {
		if w == "REDIS_URL is not set — rate limiting will fail open (not enforced)" {
			found = true
		}
	}
	if !found {
		t.Error("expected warning about missing REDIS_URL in production")
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	t.Setenv("VELOX_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	cfg := Config{
		Port:     "8080",
		Env:      "production",
		RedisURL: "redis://localhost:6379",
		DB:       DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second},
	}
	warnings := cfg.Validate()
	if len(warnings) > 0 {
		t.Errorf("expected no warnings for valid config, got: %v", warnings)
	}
}

func TestValidate_EncryptionKeyWarnings(t *testing.T) {
	_ = os.Unsetenv("VELOX_ENCRYPTION_KEY")
	cfg := Config{
		Env: "production", Port: "8080",
		RedisURL: "redis://localhost:6379",
		DB:       DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second},
	}
	warnings := cfg.Validate()
	found := false
	for _, w := range warnings {
		if w == "VELOX_ENCRYPTION_KEY is not set — customer PII will be stored in plaintext" {
			found = true
		}
	}
	if !found {
		t.Error("expected warning about missing encryption key in production")
	}

	t.Setenv("VELOX_ENCRYPTION_KEY", "tooshort")
	warnings = cfg.Validate()
	found = false
	for _, w := range warnings {
		if len(w) > 20 && w[:20] == "VELOX_ENCRYPTION_KEY" {
			found = true
		}
	}
	if !found {
		t.Error("expected warning about invalid encryption key length")
	}

	t.Setenv("VELOX_ENCRYPTION_KEY", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	warnings = cfg.Validate()
	found = false
	for _, w := range warnings {
		if w == "VELOX_ENCRYPTION_KEY is not valid hex" {
			found = true
		}
	}
	if !found {
		t.Error("expected warning about invalid hex")
	}

	_ = os.Unsetenv("VELOX_ENCRYPTION_KEY")
	localCfg := Config{
		Env: "local", Port: "8080",
		DB: DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second},
	}
	warnings = localCfg.Validate()
	for _, w := range warnings {
		if w == "VELOX_ENCRYPTION_KEY is not set — customer PII will be stored in plaintext" {
			t.Error("should not warn about missing encryption key in local env")
		}
	}
}

func TestValidate_DBPoolSanity(t *testing.T) {
	cfg := Config{
		Port: "8080",
		Env:  "local",
		DB:   DBConfig{MaxOpenConns: 5, MaxIdleConns: 10, QueryTimeout: 5 * time.Second},
	}
	warnings := cfg.Validate()
	found := false
	for _, w := range warnings {
		if w == "DB_MAX_IDLE_CONNS (10) exceeds DB_MAX_OPEN_CONNS (5) — idle conns will be capped" {
			found = true
		}
	}
	if !found {
		t.Error("expected warning about idle > open conns")
	}
}

func TestLoad_ProductionRequiresEncryptionKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("VELOX_ENCRYPTION_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when VELOX_ENCRYPTION_KEY is missing in production")
	}
	if !strings.Contains(err.Error(), "VELOX_ENCRYPTION_KEY") {
		t.Errorf("error should mention encryption key, got: %v", err)
	}
}

func TestLoad_InvalidEncryptionKeyFormat(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("APP_ENV", "local")
	t.Setenv("VELOX_ENCRYPTION_KEY", "tooshort")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid encryption key length")
	}

	t.Setenv("VELOX_ENCRYPTION_KEY", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	_, err = Load()
	if err == nil {
		t.Fatal("expected error for invalid hex in encryption key")
	}
}

func TestLoad_DiscreteDBVars(t *testing.T) {
	_ = os.Unsetenv("DATABASE_URL")
	_ = os.Setenv("DB_HOST", "localhost")
	_ = os.Setenv("DB_NAME", "velox")
	_ = os.Setenv("DB_USER", "velox")
	_ = os.Setenv("DB_PASSWORD", "secret")
	defer func() {
		_ = os.Unsetenv("DB_HOST")
		_ = os.Unsetenv("DB_NAME")
		_ = os.Unsetenv("DB_USER")
		_ = os.Unsetenv("DB_PASSWORD")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DB.URL == "" {
		t.Error("DB URL should be constructed from discrete vars")
	}
}
