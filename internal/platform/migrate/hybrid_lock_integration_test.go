package migrate_test

import (
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sagarsuperuser/velox/internal/platform/migrate"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// P7 / ADR-073: the hybrid migration loop is serialized under
// LockKeyMigrateHybrid. Before the fix, upHybrid's decide-then-dispatch
// reads of schema_migrations were unlocked — two replicas booting
// together could double-apply a no-tx migration, rewind the recorded
// version, or mis-dispatch a CONCURRENTLY file through the library's
// in-tx path (a deployment-wide dirty crash-loop).

func hybridTestAdminURL() string {
	if u := strings.TrimSpace(os.Getenv("TEST_ADMIN_DATABASE_URL")); u != "" {
		return u
	}
	return "postgres://velox:velox@localhost:5432/velox_test?sslmode=disable"
}

// TestHybridLoop_TakesTheLoopLock is the deterministic lock assertion
// (mutation-verify: remove acquireHybridLoopLock from upHybrid — the
// "still parked" check fails because Up proceeds under our held lock).
func TestHybridLoop_TakesTheLoopLock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires postgres")
	}
	adminURL := hybridTestAdminURL()
	scratchDB := "velox_migrate_hybridlock"
	scratchURL := rewriteDBName(t, adminURL, scratchDB)
	createScratchDB(t, adminURL, scratchDB)
	t.Cleanup(func() { dropScratchDB(t, adminURL, scratchDB) })

	// Baseline: fully migrate, then step back through the latest no-tx
	// migration (0130) so a real Up run has pending work including a
	// no-tx file (which forces the hybrid path).
	if err := migrate.Up(scratchURL); err != nil {
		t.Fatalf("initial Up: %v", err)
	}
	latest, err := migrate.EmbeddedLatestVersion()
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if _, err := migrate.Rollback(scratchURL, 3); err != nil {
		t.Fatalf("rollback 3: %v", err)
	}
	rolledBack := readVersion(t, scratchURL)
	if rolledBack >= latest {
		t.Fatalf("rollback did not move version: %d", rolledBack)
	}

	// Hold the hybrid loop lock on our own session — we ARE the
	// concurrent replica mid-loop.
	holder, err := sql.Open("pgx", scratchURL)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.Exec(`SELECT pg_advisory_lock($1)`, postgres.LockKeyMigrateHybrid); err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- migrate.Up(scratchURL) }()

	// While we hold the lock, Up must be parked BEFORE its first write:
	// version stays at the rolled-back value.
	time.Sleep(700 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Up finished under a held hybrid loop lock (err=%v) — the loop is not serialized", err)
	default:
	}
	if v := readVersion(t, scratchURL); v != rolledBack {
		t.Fatalf("version advanced to %d under a held loop lock, want parked at %d", v, rolledBack)
	}

	if _, err := holder.Exec(`SELECT pg_advisory_unlock($1)`, postgres.LockKeyMigrateHybrid); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Up after release: %v", err)
	}
	if v := readVersion(t, scratchURL); v != latest {
		t.Fatalf("final version: %d, want %d", v, latest)
	}
}

// TestHybridLoop_RacingAppliersOneSkips: two replicas boot together
// with pending work that includes a no-tx migration. Exactly one
// applies; the loser waits on the loop lock, then finds every version
// already applied and skips. Both return nil; no dirty flag, no
// version rewind, index present.
func TestHybridLoop_RacingAppliersOneSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires postgres")
	}
	adminURL := hybridTestAdminURL()
	scratchDB := "velox_migrate_hybridrace"
	scratchURL := rewriteDBName(t, adminURL, scratchDB)
	createScratchDB(t, adminURL, scratchDB)
	t.Cleanup(func() { dropScratchDB(t, adminURL, scratchDB) })

	if err := migrate.Up(scratchURL); err != nil {
		t.Fatalf("initial Up: %v", err)
	}
	latest, err := migrate.EmbeddedLatestVersion()
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	// Step back through 0130 (no-tx) + 0131 + 0132 so both racers see a
	// pending set that mixes library steps and a no-tx apply.
	if _, err := migrate.Rollback(scratchURL, 3); err != nil {
		t.Fatalf("rollback 3: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = migrate.Up(scratchURL)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: %v (the loser must skip cleanly, not error)", i, err)
		}
	}

	if v := readVersion(t, scratchURL); v != latest {
		t.Errorf("final version: %d, want %d (rewind?)", v, latest)
	}
	if dirtyFlag(t, scratchURL) {
		t.Error("schema_migrations dirty after racing appliers — the crash-loop shape")
	}
	if !indexExists(t, scratchURL, "idx_usage_events_tenant_time") {
		t.Error("0130's index missing after racing Ups")
	}
}

func readVersion(t *testing.T, dsn string) uint {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	v, _, err := migrate.DatabaseVersion(db)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	return v
}

func dirtyFlag(t *testing.T, dsn string) bool {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	_, dirty, err := migrate.DatabaseVersion(db)
	if err != nil {
		t.Fatalf("read dirty: %v", err)
	}
	return dirty
}
