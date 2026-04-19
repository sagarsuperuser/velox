package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestRLSIsolation_ClosedBypassTables asserts that the tenant_isolation policy
// added in migration 0006 actually blocks cross-tenant reads on the three
// tables that were previously unprotected: tenant_settings, idempotency_keys,
// payment_update_tokens.
//
// Row-level checks cover tenant_settings and idempotency_keys (no heavy
// FK chain). payment_update_tokens shares the identical policy definition and
// is covered by the metadata assertion plus its own integration path.
func TestRLSIsolation_ClosedBypassTables(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantA := testutil.CreateTestTenant(t, db, "RLS Test A")
	tenantB := testutil.CreateTestTenant(t, db, "RLS Test B")

	assertRLSEnabled(t, db, ctx, "tenant_settings")
	assertRLSEnabled(t, db, ctx, "idempotency_keys")
	assertRLSEnabled(t, db, ctx, "payment_update_tokens")

	seedSettingsAndIdempotency(t, db, ctx, tenantA, "key-A")
	seedSettingsAndIdempotency(t, db, ctx, tenantB, "key-B")

	// Both tables: TxTenant(A) with no WHERE filter must see exactly one row
	// (A's), never B's.
	assertOnlyTenantVisible(t, db, ctx, tenantA,
		`SELECT tenant_id FROM tenant_settings`, 1)
	assertOnlyTenantVisible(t, db, ctx, tenantA,
		`SELECT tenant_id FROM idempotency_keys`, 1)

	// Write isolation: acting as A, inserting a row tagged with B's id must fail.
	assertInsertDenied(t, db, ctx, tenantA, tenantB)
}

func assertRLSEnabled(t *testing.T, db *postgres.DB, ctx context.Context, tableName string) {
	t.Helper()

	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin bypass: %v", err)
	}
	defer postgres.Rollback(tx)

	var relrowsecurity, relforcerowsecurity bool
	err = tx.QueryRowContext(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1`,
		tableName,
	).Scan(&relrowsecurity, &relforcerowsecurity)
	if err != nil {
		t.Fatalf("read pg_class for %s: %v", tableName, err)
	}
	if !relrowsecurity {
		t.Fatalf("%s: ROW LEVEL SECURITY is not ENABLED", tableName)
	}
	if !relforcerowsecurity {
		t.Fatalf("%s: ROW LEVEL SECURITY is not FORCED", tableName)
	}

	var policyCount int
	err = tx.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_policies WHERE tablename = $1 AND policyname = 'tenant_isolation'`,
		tableName,
	).Scan(&policyCount)
	if err != nil {
		t.Fatalf("read pg_policies for %s: %v", tableName, err)
	}
	if policyCount != 1 {
		t.Fatalf("%s: expected exactly one tenant_isolation policy, got %d", tableName, policyCount)
	}
}

func seedSettingsAndIdempotency(t *testing.T, db *postgres.DB, ctx context.Context,
	tenantID, idemKey string) {
	t.Helper()

	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("seed begin: %v", err)
	}
	defer postgres.Rollback(tx)

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_settings (tenant_id) VALUES ($1)
		ON CONFLICT (tenant_id) DO NOTHING`, tenantID); err != nil {
		t.Fatalf("seed tenant_settings for %s: %v", tenantID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency_keys (tenant_id, key, http_method, http_path, status_code, response_body)
		VALUES ($1, $2, 'POST', '/v1/test', 200, '{}')`,
		tenantID, idemKey); err != nil {
		t.Fatalf("seed idempotency_keys for %s: %v", tenantID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
}

func assertOnlyTenantVisible(t *testing.T, db *postgres.DB, ctx context.Context,
	tenantID, query string, expected int) {
	t.Helper()

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin tenant tx: %v", err)
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()

	count := 0
	for rows.Next() {
		var seen string
		if err := rows.Scan(&seen); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if seen != tenantID {
			t.Fatalf("RLS LEAK: query %q returned row for tenant %q while context is %q",
				query, seen, tenantID)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if count != expected {
		t.Fatalf("query %q: expected %d row(s) visible to tenant %s, got %d",
			query, expected, tenantID, count)
	}
}

func assertInsertDenied(t *testing.T, db *postgres.DB, ctx context.Context,
	actingTenant, targetTenant string) {
	t.Helper()

	tx, err := db.BeginTx(ctx, postgres.TxTenant, actingTenant)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO idempotency_keys (tenant_id, key, http_method, http_path, status_code, response_body)
		VALUES ($1, 'hostile-key', 'POST', '/v1/test', 200, '{}')`,
		targetTenant)
	if err == nil {
		t.Fatalf("RLS LEAK: tenant %s successfully inserted a row tagged with tenant %s",
			actingTenant, targetTenant)
	}
}
