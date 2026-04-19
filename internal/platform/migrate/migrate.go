package migrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/golang-migrate/migrate/v4"
	mpg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed sql/*.sql
var sqlFS embed.FS

// veloxMigrationLockKey is a 64-bit int passed to pg_advisory_lock. It's
// distinct from golang-migrate's internally-derived key, so both locks can
// coexist without deadlock. Don't change once deployed — existing replicas
// would stop serializing with new ones.
const veloxMigrationLockKey int64 = 0x56454c4f58 // "VELOX"

// lockAcquireTimeout bounds how long a replica will wait for another replica's
// migrations to finish before giving up and refusing to start. Five minutes is
// enough for any reasonable migration; longer than that likely indicates a
// hung migration that an operator should investigate.
const lockAcquireTimeout = 5 * time.Minute

// New creates a golang-migrate instance from embedded SQL files.
func New(db *sql.DB) (*migrate.Migrate, error) {
	subFS, err := fs.Sub(sqlFS, "sql")
	if err != nil {
		return nil, fmt.Errorf("sub fs: %w", err)
	}

	source, err := iofs.New(subFS, ".")
	if err != nil {
		return nil, fmt.Errorf("iofs source: %w", err)
	}

	driver, err := mpg.WithInstance(db, &mpg.Config{
		MigrationsTable: "schema_migrations",
	})
	if err != nil {
		return nil, fmt.Errorf("postgres driver: %w", err)
	}

	return migrate.NewWithInstance("iofs", source, "postgres", driver)
}

// Up applies all pending migrations. Safe to call on every boot.
//
// Takes a session-scoped advisory lock before running. When multiple replicas
// boot simultaneously with RUN_MIGRATIONS_ON_BOOT=true, only one holds the
// lock at a time; others block until it's released, then see an empty
// migration set and proceed. If a replica crashes mid-migration, Postgres
// releases the lock automatically when its session closes — no manual recovery.
func Up(db *sql.DB) error {
	lockCtx, cancel := context.WithTimeout(context.Background(), lockAcquireTimeout)
	defer cancel()

	// Use a dedicated connection for the lock so we can release it deterministically.
	// Advisory locks are session-scoped; if we used the pool, a different connection
	// might run the unlock and succeed silently without releasing anything.
	conn, err := db.Conn(lockCtx)
	if err != nil {
		return fmt.Errorf("get migration lock connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	slog.Info("acquiring migration advisory lock",
		"key", veloxMigrationLockKey,
		"timeout", lockAcquireTimeout,
	)
	start := time.Now()
	if _, err := conn.ExecContext(lockCtx, `SELECT pg_advisory_lock($1)`, veloxMigrationLockKey); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("migration lock timeout after %s — another replica may be stuck holding the lock", lockAcquireTimeout)
		}
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	slog.Info("migration advisory lock acquired", "wait", time.Since(start))
	defer func() {
		// Use Background context so shutdown/timeout on the parent doesn't leave
		// the lock orphaned. The connection Close above also releases it, but
		// explicit unlock is cleaner for observability.
		if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, veloxMigrationLockKey); err != nil {
			slog.Warn("release migration lock", "error", err)
		}
	}()

	m, err := New(db)
	if err != nil {
		return err
	}

	err = m.Up()
	if err == migrate.ErrNoChange {
		slog.Info("migrations up to date")
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	v, _, _ := m.Version()
	slog.Info("migrations applied", "version", v)
	return nil
}
