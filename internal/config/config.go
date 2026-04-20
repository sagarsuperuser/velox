package config

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	Port    string
	Env     string // local, staging, production
	DB      DBConfig
	Migrate bool

	// Stripe — dual-key (live + test). Either may be empty; at least one
	// should be set for payment flows to function. Test keys gate test-mode
	// API-key traffic end-to-end.
	StripeSecretKey         string
	StripeWebhookSecret     string
	StripeSecretKeyTest     string
	StripeWebhookSecretTest string

	// Redis (optional — rate limiting fails open without it)
	RedisURL string

	// Bootstrap
	BootstrapToken string
}

type DBConfig struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PingTimeout     time.Duration
	QueryTimeout    time.Duration
}

func Load() (Config, error) {
	env := envOr("APP_ENV", "local")

	dbURL, err := loadDatabaseURL()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Port:    envOr("PORT", "8080"),
		Env:     env,
		Migrate: boolEnv("RUN_MIGRATIONS_ON_BOOT", false),
		DB: DBConfig{
			URL:             dbURL,
			MaxOpenConns:    intEnv("DB_MAX_OPEN_CONNS", 20),
			MaxIdleConns:    intEnv("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: time.Duration(intEnv("DB_CONN_MAX_LIFETIME_MIN", 30)) * time.Minute,
			ConnMaxIdleTime: time.Duration(intEnv("DB_CONN_MAX_IDLE_TIME_SEC", 120)) * time.Second,
			PingTimeout:     time.Duration(intEnv("DB_PING_TIMEOUT_SEC", 5)) * time.Second,
			QueryTimeout:    time.Duration(intEnv("DB_QUERY_TIMEOUT_MS", 5000)) * time.Millisecond,
		},
		StripeSecretKey:         strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")),
		StripeWebhookSecret:     strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")),
		StripeSecretKeyTest:     strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY_TEST")),
		StripeWebhookSecretTest: strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET_TEST")),
		RedisURL:            strings.TrimSpace(os.Getenv("REDIS_URL")),
		BootstrapToken:      strings.TrimSpace(os.Getenv("VELOX_BOOTSTRAP_TOKEN")),
	}

	if warnings := cfg.Validate(); len(warnings) > 0 {
		for _, w := range warnings {
			slog.Warn("config validation", "warning", w)
		}
	}

	if err := cfg.validateFatal(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// validateFatal returns an error for misconfigurations that must not be
// tolerated at startup. These are a strict subset of Validate's warnings:
// conditions where continuing would risk silent data loss, compliance
// violation, or secret exposure.
func (c Config) validateFatal() error {
	encKey := strings.TrimSpace(os.Getenv("VELOX_ENCRYPTION_KEY"))

	if c.Env == "production" && encKey == "" {
		return fmt.Errorf("VELOX_ENCRYPTION_KEY is required in production — refusing to start with plaintext PII storage")
	}

	if encKey != "" {
		if len(encKey) != 64 {
			return fmt.Errorf("VELOX_ENCRYPTION_KEY must be exactly 64 hex characters (32 bytes), got %d", len(encKey))
		}
		if _, err := hex.DecodeString(encKey); err != nil {
			return fmt.Errorf("VELOX_ENCRYPTION_KEY is not valid hex: %w", err)
		}
	}

	return nil
}

// Validate checks the config for common misconfigurations.
func (c Config) Validate() []string {
	var warnings []string

	if c.StripeSecretKey == "" {
		warnings = append(warnings, "STRIPE_SECRET_KEY is not set — payment processing will fail")
	} else if !strings.HasPrefix(c.StripeSecretKey, "sk_") {
		warnings = append(warnings, "STRIPE_SECRET_KEY does not start with 'sk_' — may be invalid")
	}
	if c.StripeWebhookSecret == "" {
		warnings = append(warnings, "STRIPE_WEBHOOK_SECRET is not set — webhook signature verification will fail")
	} else if !strings.HasPrefix(c.StripeWebhookSecret, "whsec_") {
		warnings = append(warnings, "STRIPE_WEBHOOK_SECRET does not start with 'whsec_' — may be invalid")
	}
	// Test-mode keys are optional; only validate shape when provided. Absence
	// just means test-mode API keys will fail to charge (explicit error at
	// call site), which is the correct behavior for operators who haven't
	// opted into test mode yet.
	if c.StripeSecretKeyTest != "" && !strings.HasPrefix(c.StripeSecretKeyTest, "sk_test_") {
		warnings = append(warnings, "STRIPE_SECRET_KEY_TEST does not start with 'sk_test_' — may be a live key in the wrong slot")
	}
	if c.StripeWebhookSecretTest != "" && !strings.HasPrefix(c.StripeWebhookSecretTest, "whsec_") {
		warnings = append(warnings, "STRIPE_WEBHOOK_SECRET_TEST does not start with 'whsec_' — may be invalid")
	}

	encKey := strings.TrimSpace(os.Getenv("VELOX_ENCRYPTION_KEY"))
	if encKey == "" {
		if c.Env == "production" {
			warnings = append(warnings, "VELOX_ENCRYPTION_KEY is not set — customer PII will be stored in plaintext")
		}
	} else {
		if len(encKey) != 64 {
			warnings = append(warnings, fmt.Sprintf("VELOX_ENCRYPTION_KEY must be exactly 64 hex characters (32 bytes), got %d characters", len(encKey)))
		} else if _, err := hex.DecodeString(encKey); err != nil {
			warnings = append(warnings, "VELOX_ENCRYPTION_KEY is not valid hex")
		}
	}

	if c.RedisURL == "" && c.Env == "production" {
		warnings = append(warnings, "REDIS_URL is not set — rate limiting will fail open (not enforced)")
	}

	switch c.Env {
	case "local", "staging", "production":
	default:
		warnings = append(warnings, fmt.Sprintf("APP_ENV=%q is not a recognized environment (expected: local, staging, production)", c.Env))
	}

	if port, err := strconv.Atoi(c.Port); err != nil || port < 1 || port > 65535 {
		warnings = append(warnings, fmt.Sprintf("PORT=%q is not a valid port number", c.Port))
	}

	if c.DB.MaxOpenConns < 1 {
		warnings = append(warnings, "DB_MAX_OPEN_CONNS must be at least 1")
	}
	if c.DB.MaxIdleConns > c.DB.MaxOpenConns {
		warnings = append(warnings, fmt.Sprintf("DB_MAX_IDLE_CONNS (%d) exceeds DB_MAX_OPEN_CONNS (%d) — idle conns will be capped", c.DB.MaxIdleConns, c.DB.MaxOpenConns))
	}
	if c.DB.QueryTimeout < 100*time.Millisecond {
		warnings = append(warnings, fmt.Sprintf("DB_QUERY_TIMEOUT_MS=%d is very low — queries may time out unnecessarily", c.DB.QueryTimeout.Milliseconds()))
	}

	return warnings
}

// LoadDBOnly loads just the database config without validating Stripe/Redis/etc.
func LoadDBOnly() (DBConfig, error) {
	dbURL, err := loadDatabaseURL()
	if err != nil {
		return DBConfig{}, err
	}
	return DBConfig{
		URL:             dbURL,
		MaxOpenConns:    intEnv("DB_MAX_OPEN_CONNS", 20),
		MaxIdleConns:    intEnv("DB_MAX_IDLE_CONNS", 5),
		ConnMaxLifetime: time.Duration(intEnv("DB_CONN_MAX_LIFETIME_MIN", 30)) * time.Minute,
		ConnMaxIdleTime: time.Duration(intEnv("DB_CONN_MAX_IDLE_TIME_SEC", 120)) * time.Second,
		PingTimeout:     time.Duration(intEnv("DB_PING_TIMEOUT_SEC", 5)) * time.Second,
		QueryTimeout:    time.Duration(intEnv("DB_QUERY_TIMEOUT_MS", 5000)) * time.Millisecond,
	}, nil
}

func OpenPostgres(cfg DBConfig) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.PingTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return db, nil
}

func loadDatabaseURL() (string, error) {
	host := strings.TrimSpace(os.Getenv("DB_HOST"))
	name := strings.TrimSpace(os.Getenv("DB_NAME"))
	user := strings.TrimSpace(os.Getenv("DB_USER"))
	pass := strings.TrimSpace(os.Getenv("DB_PASSWORD"))

	if host != "" || name != "" || user != "" || pass != "" {
		if host == "" || name == "" || user == "" || pass == "" {
			return "", fmt.Errorf("DB_HOST, DB_NAME, DB_USER, DB_PASSWORD are all required together")
		}
		port := envOr("DB_PORT", "5432")
		sslMode := envOr("DB_SSLMODE", "require")
		query := neturl.Values{}
		query.Set("sslmode", sslMode)
		return (&neturl.URL{
			Scheme:   "postgres",
			User:     neturl.UserPassword(user, pass),
			Host:     net.JoinHostPort(host, port),
			Path:     name,
			RawQuery: query.Encode(),
		}).String(), nil
	}

	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return "", fmt.Errorf("DATABASE_URL or DB_HOST/DB_NAME/DB_USER/DB_PASSWORD required")
	}
	return dbURL, nil
}

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func intEnv(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func boolEnv(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		return fallback
	}
}
