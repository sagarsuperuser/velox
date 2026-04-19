package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sagarsuperuser/velox/internal/platform/migrate"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

const (
	// Superuser URL — used for migrations and cleanup (bypasses RLS).
	// Points to velox_test database so tests never touch dev data.
	defaultAdminDBURL = "postgres://velox:velox@localhost:5432/velox_test?sslmode=disable"
	// App user URL — used for queries (RLS enforced).
	defaultAppDBURL = "postgres://velox_test_app:velox_test_app@localhost:5432/velox_test?sslmode=disable"
)

// SetupTestDB runs migrations as superuser, cleans data, and returns an
// app-user connection where RLS is enforced.
func SetupTestDB(t *testing.T) *postgres.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test (use -short=false)")
	}

	adminURL := envOr("TEST_ADMIN_DATABASE_URL", defaultAdminDBURL)
	appURL := envOr("TEST_DATABASE_URL", defaultAppDBURL)

	// Admin connection: migrations + cleanup
	adminPool := openPool(t, adminURL)

	if err := migrate.Up(adminPool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	cleanDB(t, adminPool)

	// App connection: actual queries (RLS enforced)
	appPool := openPool(t, appURL)
	db := postgres.NewDB(appPool, 5*time.Second)

	t.Cleanup(func() {
		cleanDB(t, adminPool)
		_ = appPool.Close()
		_ = adminPool.Close()
	})

	return db
}

func openPool(t *testing.T, url string) *sql.DB {
	t.Helper()

	pool, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open db (%s): %v", url, err)
	}
	pool.SetMaxOpenConns(5)
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		t.Fatalf("ping db (%s): %v", url, err)
	}
	return pool
}

func cleanDB(t *testing.T, pool *sql.DB) {
	t.Helper()

	tables := []string{
		"invoice_dunning_events",
		"invoice_dunning_runs",
		"dunning_policies",
		"invoice_line_items",
		"invoices",
		"billed_entries",
		"usage_events",
		"subscriptions",
		"plans",
		"meters",
		"rating_rule_versions",
		"customer_payment_setups",
		"customer_billing_profiles",
		"customers",
		"stripe_webhook_events",
		"api_keys",
		"users",
		"billing_provider_connections",
		"tenants",
	}

	query := fmt.Sprintf("TRUNCATE %s CASCADE", strings.Join(tables, ", "))
	if _, err := pool.ExecContext(context.Background(), query); err != nil {
		t.Fatalf("clean db: %v", err)
	}
}

// CreateTestTenant inserts a tenant via the app connection (bypass RLS).
// Since tenants table has no RLS, this works directly.
func CreateTestTenant(t *testing.T, db *postgres.DB, name string) string {
	t.Helper()

	id := postgres.NewID("vlx_ten")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.Pool.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status) VALUES ($1, $2, 'active')`, id, name)
	if err != nil {
		t.Fatalf("create test tenant: %v", err)
	}
	return id
}

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
