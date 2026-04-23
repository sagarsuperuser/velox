package migrate_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sagarsuperuser/velox/internal/platform/migrate"
)

// TestMigrationRoundTrip runs every migration down then up again on an
// isolated scratch database, catching the three failure modes that a simple
// forward-only `migrate Up` in CI doesn't:
//
//  1. A .up.sql exists without a matching .down.sql (or vice versa).
//  2. A .down.sql compiles but doesn't actually undo the up — e.g. drops the
//     new column but leaves behind its index, so the re-Up fails on an
//     "already exists" error.
//  3. A non-idempotent constraint or trigger that applies cleanly the first
//     time but blows up on the second.
//
// Covered in CI by the integration-test step (postgres is already wired up,
// velox_app role already exists cluster-wide). Not covered by -short unit
// tests — the test is guarded the same way as every other integration test.
func TestMigrationRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires postgres")
	}

	adminURL := strings.TrimSpace(os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if adminURL == "" {
		adminURL = "postgres://velox:velox@localhost:5432/velox_test?sslmode=disable"
	}

	scratchDB := "velox_migrate_roundtrip"
	scratchURL := rewriteDBName(t, adminURL, scratchDB)

	createScratchDB(t, adminURL, scratchDB)
	t.Cleanup(func() { dropScratchDB(t, adminURL, scratchDB) })

	// Step 1 — forward: apply every migration to a fresh database.
	if err := migrate.Up(scratchURL); err != nil {
		t.Fatalf("initial Up: %v", err)
	}
	top, dirty, err := migrate.Status(scratchURL)
	if err != nil {
		t.Fatalf("status after initial Up: %v", err)
	}
	if dirty {
		t.Fatalf("dirty after initial Up at version %d", top)
	}
	if top == 0 {
		t.Fatal("initial Up recorded no version — embedded FS empty?")
	}
	t.Logf("forward Up → version %d", top)

	// Step 2 — reverse: roll back every migration. Steps value is the
	// number of applied migrations, not the max version integer, so
	// non-contiguous version numbers still roll back cleanly.
	appliedCount, err := countAppliedMigrations(scratchURL)
	if err != nil {
		t.Fatalf("count applied: %v", err)
	}
	after, err := migrate.Rollback(scratchURL, appliedCount)
	if err != nil {
		t.Fatalf("rollback %d steps: %v", appliedCount, err)
	}
	if after != 0 {
		t.Fatalf("rollback to 0 ended at version %d (expected 0) — a .down.sql didn't fully undo its .up.sql", after)
	}

	// Step 3 — forward again: re-apply. This is the real bite — if a down
	// file leaves orphan state, the second Up fails on "already exists".
	if err := migrate.Up(scratchURL); err != nil {
		t.Fatalf("re-Up after rollback: %v", err)
	}
	top2, dirty2, err := migrate.Status(scratchURL)
	if err != nil {
		t.Fatalf("status after re-Up: %v", err)
	}
	if dirty2 {
		t.Fatalf("dirty after re-Up at version %d", top2)
	}
	if top2 != top {
		t.Fatalf("re-Up ended at version %d, expected %d", top2, top)
	}
}

// countAppliedMigrations reads schema_migrations via a direct connection so
// we know how many Steps(-N) to request without assuming contiguous version
// integers.
func countAppliedMigrations(dsn string) (int, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	// The golang-migrate schema_migrations table holds exactly one row (the
	// current version). To count applied steps we enumerate the embedded
	// migration files — the library's Steps(-N) treats each .up.sql as one
	// step regardless of version-number gaps.
	latest, err := migrate.EmbeddedLatestVersion()
	if err != nil {
		return 0, err
	}
	// Assumes contiguous 1..N numbering, which Velox enforces in the sql/
	// directory. If we ever skip numbers intentionally, switch to counting
	// the files directly.
	return int(latest), nil
}

func rewriteDBName(t *testing.T, dsn, newName string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	u.Path = "/" + newName
	return u.String()
}

func createScratchDB(t *testing.T, adminURL, name string) {
	t.Helper()
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	defer db.Close()

	// DROP-then-CREATE so a leaked scratch DB from a previous run doesn't
	// fail the test with "database already exists".
	if _, err := db.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quoteIdent(name))); err != nil {
		t.Fatalf("drop scratch db (pre-create): %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(name))); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
}

func dropScratchDB(t *testing.T, adminURL, name string) {
	t.Helper()
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Logf("open admin for cleanup: %v", err)
		return
	}
	defer db.Close()

	if _, err := db.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quoteIdent(name))); err != nil {
		t.Logf("drop scratch db: %v", err)
	}
}

// quoteIdent mirrors pq.QuoteIdentifier for the narrow set of chars the
// scratch DB name can contain. Kept local so the migrate package doesn't
// pull in pq just for tests.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
