package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	// Set minimum required env
	os.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	defer os.Unsetenv("DATABASE_URL")

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
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("DB_HOST")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when DATABASE_URL is missing")
	}
}

func TestLoad_CustomPort(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	os.Setenv("PORT", "3000")
	defer os.Unsetenv("DATABASE_URL")
	defer os.Unsetenv("PORT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "3000" {
		t.Errorf("port: got %q, want 3000", cfg.Port)
	}
}

func TestValidate_MissingStripeKeys(t *testing.T) {
	cfg := Config{
		Port: "8080",
		Env:  "production",
		DB:   DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second},
	}
	warnings := cfg.Validate()
	found := map[string]bool{}
	for _, w := range warnings {
		if w == "STRIPE_SECRET_KEY is not set — payment processing will fail" {
			found["stripe_key"] = true
		}
		if w == "STRIPE_WEBHOOK_SECRET is not set — webhook signature verification will fail" {
			found["webhook_secret"] = true
		}
		if w == "REDIS_URL is not set — rate limiting will fail open (not enforced)" {
			found["redis"] = true
		}
	}
	if !found["stripe_key"] {
		t.Error("expected warning about missing STRIPE_SECRET_KEY")
	}
	if !found["webhook_secret"] {
		t.Error("expected warning about missing STRIPE_WEBHOOK_SECRET")
	}
	if !found["redis"] {
		t.Error("expected warning about missing REDIS_URL in production")
	}
}

func TestValidate_InvalidStripePrefix(t *testing.T) {
	cfg := Config{
		Port:            "8080",
		Env:             "local",
		StripeSecretKey: "bad_key_123",
		DB:              DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5000},
	}
	warnings := cfg.Validate()
	for _, w := range warnings {
		if w == "STRIPE_SECRET_KEY does not start with 'sk_' — may be invalid" {
			return
		}
	}
	t.Error("expected warning about invalid STRIPE_SECRET_KEY prefix")
}

func TestValidate_ValidConfig(t *testing.T) {
	// Set a valid 64-char hex encryption key so the validator doesn't warn
	os.Setenv("VELOX_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("VELOX_ENCRYPTION_KEY")

	cfg := Config{
		Port:                "8080",
		Env:                 "production",
		StripeSecretKey:     "sk_live_abc123",
		StripeWebhookSecret: "whsec_abc123",
		RedisURL:            "redis://localhost:6379",
		DB:                  DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second},
	}
	warnings := cfg.Validate()
	if len(warnings) > 0 {
		t.Errorf("expected no warnings for valid config, got: %v", warnings)
	}
}

func TestValidate_EncryptionKeyWarnings(t *testing.T) {
	// Production without key should warn
	os.Unsetenv("VELOX_ENCRYPTION_KEY")
	cfg := Config{Env: "production", Port: "8080", StripeSecretKey: "sk_live_x", StripeWebhookSecret: "whsec_x",
		RedisURL: "redis://localhost:6379", DB: DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second}}
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

	// Invalid length should warn
	os.Setenv("VELOX_ENCRYPTION_KEY", "tooshort")
	defer os.Unsetenv("VELOX_ENCRYPTION_KEY")
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

	// Invalid hex should warn
	os.Setenv("VELOX_ENCRYPTION_KEY", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
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

	// Local env without key should NOT warn
	os.Unsetenv("VELOX_ENCRYPTION_KEY")
	localCfg := Config{Env: "local", Port: "8080", StripeSecretKey: "sk_test_x", StripeWebhookSecret: "whsec_x",
		DB: DBConfig{MaxOpenConns: 20, MaxIdleConns: 5, QueryTimeout: 5 * time.Second}}
	warnings = localCfg.Validate()
	for _, w := range warnings {
		if w == "VELOX_ENCRYPTION_KEY is not set — customer PII will be stored in plaintext" {
			t.Error("should not warn about missing encryption key in local env")
		}
	}
}

func TestValidate_DBPoolSanity(t *testing.T) {
	cfg := Config{
		Port:                "8080",
		Env:                 "local",
		StripeSecretKey:     "sk_test_abc",
		StripeWebhookSecret: "whsec_abc",
		DB:                  DBConfig{MaxOpenConns: 5, MaxIdleConns: 10, QueryTimeout: 5000},
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

func TestLoad_DiscreteDBVars(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	os.Setenv("DB_HOST", "localhost")
	os.Setenv("DB_NAME", "velox")
	os.Setenv("DB_USER", "velox")
	os.Setenv("DB_PASSWORD", "secret")
	defer func() {
		os.Unsetenv("DB_HOST")
		os.Unsetenv("DB_NAME")
		os.Unsetenv("DB_USER")
		os.Unsetenv("DB_PASSWORD")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DB.URL == "" {
		t.Error("DB URL should be constructed from discrete vars")
	}
}
