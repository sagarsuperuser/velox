package config

import (
	"os"
	"testing"
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
