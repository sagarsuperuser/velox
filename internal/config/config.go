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

	env "github.com/caarlos0/env/v11"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	Port    string `env:"PORT" envDefault:"8080"`
	Env     string `env:"APP_ENV" envDefault:"local"`
	Migrate bool   `env:"RUN_MIGRATIONS_ON_BOOT" envDefault:"false"`

	DB DBConfig

	// Stripe
	StripeSecretKey     string `env:"STRIPE_SECRET_KEY"`
	StripeWebhookSecret string `env:"STRIPE_WEBHOOK_SECRET"`

	// Redis (optional — rate limiting fails open without it)
	RedisURL string `env:"REDIS_URL"`

	// Bootstrap
	BootstrapToken string `env:"VELOX_BOOTSTRAP_TOKEN"`
}

type DBConfig struct {
	URL             string        // Populated by loadDatabaseURL fallback, not env tags
	MaxOpenConns    int           `env:"DB_MAX_OPEN_CONNS" envDefault:"20"`
	MaxIdleConns    int           `env:"DB_MAX_IDLE_CONNS" envDefault:"5"`
	ConnMaxLifetime time.Duration `env:"DB_CONN_MAX_LIFETIME" envDefault:"30m"`
	ConnMaxIdleTime time.Duration `env:"DB_CONN_MAX_IDLE_TIME" envDefault:"120s"`
	PingTimeout     time.Duration `env:"DB_PING_TIMEOUT" envDefault:"5s"`
	QueryTimeout    time.Duration `env:"DB_QUERY_TIMEOUT" envDefault:"5s"`
}

func Load() (Config, error) {
	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	// Handle DATABASE_URL fallback from discrete vars
	if cfg.DB.URL == "" {
		url, err := loadDatabaseURL()
		if err != nil {
			return Config{}, err
		}
		cfg.DB.URL = url
	}

	// Run validation
	if warnings := cfg.Validate(); len(warnings) > 0 {
		for _, w := range warnings {
			slog.Warn("config validation", "warning", w)
		}
	}

	return cfg, nil
}

// Validate checks the config for common misconfigurations.
// Returns warnings (not errors) so the server can still start in dev mode,
// but operators see exactly what's missing before production traffic hits.
func (c Config) Validate() []string {
	var warnings []string

	// Stripe — required for payment processing
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

	// PII encryption — strongly recommended in production
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

	// Redis — optional but recommended
	if c.RedisURL == "" && c.Env == "production" {
		warnings = append(warnings, "REDIS_URL is not set — rate limiting will fail open (not enforced)")
	}

	// Environment sanity
	switch c.Env {
	case "local", "staging", "production":
		// valid
	default:
		warnings = append(warnings, fmt.Sprintf("APP_ENV=%q is not a recognized environment (expected: local, staging, production)", c.Env))
	}

	// Port sanity
	if port, err := strconv.Atoi(c.Port); err != nil || port < 1 || port > 65535 {
		warnings = append(warnings, fmt.Sprintf("PORT=%q is not a valid port number", c.Port))
	}

	// DB pool sanity
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

// LoadDBOnly loads just the database config without validating
// Stripe, Redis, or encryption settings. Used by CLI commands
// (migrate, rollback) that only need a DB connection.
func LoadDBOnly() (DBConfig, error) {
	cfg := DBConfig{}
	if err := env.Parse(&cfg); err != nil {
		return DBConfig{}, fmt.Errorf("parse db config: %w", err)
	}

	if cfg.URL == "" {
		url, err := loadDatabaseURL()
		if err != nil {
			return DBConfig{}, err
		}
		cfg.URL = url
	}

	return cfg, nil
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

