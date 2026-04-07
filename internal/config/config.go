package config

import (
	"context"
	"database/sql"
	"fmt"
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
	Env     string
	DB      DBConfig
	Migrate bool
}

type DBConfig struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	PingTimeout     time.Duration
	QueryTimeout    time.Duration
}

func Load() (Config, error) {
	env := envOr("APP_ENV", "local")

	dbURL, err := loadDatabaseURL()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Port:    envOr("PORT", "8080"),
		Env:     env,
		Migrate: boolEnv("RUN_MIGRATIONS_ON_BOOT", false),
		DB: DBConfig{
			URL:             dbURL,
			MaxOpenConns:    intEnv("DB_MAX_OPEN_CONNS", 20),
			MaxIdleConns:    intEnv("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: time.Duration(intEnv("DB_CONN_MAX_LIFETIME_MIN", 30)) * time.Minute,
			PingTimeout:     time.Duration(intEnv("DB_PING_TIMEOUT_SEC", 5)) * time.Second,
			QueryTimeout:    time.Duration(intEnv("DB_QUERY_TIMEOUT_MS", 5000)) * time.Millisecond,
		},
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
