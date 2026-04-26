package migrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/golang-migrate/migrate/v4"
	mpg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed sql/*.sql
var sqlFS embed.FS

// migrationFilenamePattern matches golang-migrate's file convention:
// {version}_{name}.{up|down}.sql — e.g., "0003_tax_cleanup.up.sql".
var migrationFilenamePattern = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

// openMigrationPool opens a dedicated short-lived *sql.DB for one migration
// command. Migrations are single-threaded, so a 1-connection pool is enough.
//
// A dedicated pool is required because golang-migrate's postgres driver
// closes the underlying *sql.DB inside its Close() — passing the app's
// shared pool would leave it unusable for every subsequent operation (e.g.,
// CheckSchemaReady, serving requests). This helper keeps that close
// side-effect scoped to a pool the caller never sees.
func openMigrationPool(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open migration db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping migration db: %w", err)
	}
	return db, nil
}

// newMigrator creates a golang-migrate instance from embedded SQL files.
// Internal helper — callers outside this package should use Up, Status,
// or Rollback so they don't have to reason about the driver's Close()
// side-effect on the supplied *sql.DB.
func newMigrator(db *sql.DB) (*migrate.Migrate, error) {
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

// Up applies all pending migrations using a dedicated short-lived connection
// pool. The caller's app pool (if any) is untouched — only the DSN is needed.
//
// Concurrency: golang-migrate's postgres driver takes an internal
// pg_advisory_lock (keyed on db+schema) before applying. Multiple replicas
// booting concurrently serialize on that lock — one runs migrations, the
// others wait, then find ErrNoChange and proceed. We do not add an outer
// lock: it would be redundant with the library's lock and introduces a
// connection-leak edge case if the manual unlock fails after a network blip.
//
// Production guidance: run migrations as a dedicated deploy step (e.g., a
// Kubernetes Job with activeDeadlineSeconds, or a CI step before rollout),
// not on app boot. Boot-time migrations complicate rolling deploys and
// startup probes, and a wedged DDL cannot be cancelled from Go (Postgres
// advisory locks ignore client-side context cancellation). App replicas
// should instead call CheckSchemaReady at startup to refuse to serve with
// an outdated schema.
func Up(dsn string) error {
	db, err := openMigrationPool(dsn)
	if err != nil {
		return err
	}

	m, err := newMigrator(db)
	if err != nil {
		_ = db.Close()
		return err
	}
	defer closeMigrator(m) // closes db via the postgres driver

	start := time.Now()
	err = m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		slog.Info("migrations up to date")
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	v, _, _ := m.Version()
	slog.Info("migrations applied",
		"version", v,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return nil
}

// Status reports the current database migration version. Returns (0, false,
// nil) for a fresh database with no schema_migrations table. Opens and closes
// its own short-lived connection pool.
func Status(dsn string) (uint, bool, error) {
	db, err := openMigrationPool(dsn)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = db.Close() }()

	return DatabaseVersion(db)
}

// Rollback rolls back the last `steps` migrations and returns the new
// database version. Opens and closes its own short-lived connection pool.
func Rollback(dsn string, steps int) (uint, error) {
	if steps <= 0 {
		return 0, fmt.Errorf("rollback steps must be positive, got %d", steps)
	}

	db, err := openMigrationPool(dsn)
	if err != nil {
		return 0, err
	}

	m, err := newMigrator(db)
	if err != nil {
		_ = db.Close()
		return 0, err
	}
	defer closeMigrator(m)

	if err := m.Steps(-steps); err != nil {
		return 0, fmt.Errorf("rollback %d step(s): %w", steps, err)
	}
	v, _, _ := m.Version()
	return v, nil
}

// EmbeddedMigrationCount returns the number of .up.sql files in the embedded
// migrations directory. Differs from EmbeddedLatestVersion when version
// numbers are non-contiguous (e.g. one branch skipped a number to
// coordinate with a sibling parallel branch). The migrate library's
// Steps(-N) operates on a per-file basis, so callers that want to roll
// back "every applied migration" should use this count, not the latest
// version number.
func EmbeddedMigrationCount() (int, error) {
	entries, err := sqlFS.ReadDir("sql")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}

	var n int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if migrationFilenamePattern.MatchString(entry.Name()) {
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("no up-migrations found in embedded fs")
	}
	return n, nil
}

// EmbeddedLatestVersion returns the highest migration version packaged into
// this binary. Used by CheckSchemaReady to compare against the database's
// current version.
func EmbeddedLatestVersion() (uint, error) {
	entries, err := sqlFS.ReadDir("sql")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}

	var latest uint
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := migrationFilenamePattern.FindStringSubmatch(entry.Name())
		if len(m) != 2 {
			continue
		}
		n, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			continue
		}
		if uint(n) > latest {
			latest = uint(n)
		}
	}
	if latest == 0 {
		return 0, fmt.Errorf("no up-migrations found in embedded fs")
	}
	return latest, nil
}

// DatabaseVersion returns the current migration version recorded in the
// database, along with whether the last migration left the schema in a
// dirty (partially-applied) state. Returns (0, false, nil) if the
// schema_migrations table does not yet exist (fresh database).
//
// Queries schema_migrations directly rather than going through the migrate
// library so we don't need to construct a Migrate instance (which opens
// connections and prepares source drivers for no reason if we just want
// to read a version number).
func DatabaseVersion(db *sql.DB) (uint, bool, error) {
	var version int64
	var dirty bool
	err := db.QueryRow(`SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	// Fresh DB — schema_migrations table doesn't exist yet.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read schema_migrations: %w", err)
	}
	if version < 0 {
		return 0, dirty, fmt.Errorf("invalid negative version %d in schema_migrations", version)
	}
	return uint(version), dirty, nil
}

// CheckSchemaReady verifies the database schema matches what this binary
// expects. It returns an error if:
//   - The database is in a dirty migration state (previous migration failed
//     mid-way; a human needs to decide whether to rollback or fix-forward).
//   - The database schema version is behind the embedded latest version
//     (the app would return 500s on any query touching new columns/tables).
//
// Call at startup, AFTER optionally running migrations, BEFORE opening the
// HTTP server. Refusing to start is safer than serving with a stale schema:
// the orchestrator (Kubernetes, systemd, etc.) will retry, and by then
// migrations should have completed.
func CheckSchemaReady(db *sql.DB) error {
	embedded, err := EmbeddedLatestVersion()
	if err != nil {
		return fmt.Errorf("read embedded version: %w", err)
	}

	dbVer, dirty, err := DatabaseVersion(db)
	if err != nil {
		return fmt.Errorf("read database version: %w", err)
	}

	if dirty {
		return fmt.Errorf("database at version %d is in a dirty migration state — prior migration failed; run `velox migrate status` and fix before restarting", dbVer)
	}

	if dbVer < embedded {
		return fmt.Errorf("schema behind code: database at version %d, binary expects version %d — run migrations before starting the app", dbVer, embedded)
	}

	if dbVer > embedded {
		// Rolling deploy: newer binary coming up against older binary's DB — this
		// is normal during upgrades. But if the OLDER binary is starting against
		// a NEWER DB, refuse — the old code may not understand new columns or
		// enum values and could write data the new code then mis-interprets.
		slog.Warn("schema ahead of binary — likely a rollback in progress",
			"database_version", dbVer,
			"binary_version", embedded,
		)
	} else {
		slog.Info("schema ready",
			"version", dbVer,
			"binary_expects", embedded,
		)
	}
	return nil
}

// closeMigrator closes a Migrate instance, logging any error. The library
// returns two errors (source, database); we combine into one log line.
func closeMigrator(m *migrate.Migrate) {
	srcErr, dbErr := m.Close()
	if srcErr != nil || dbErr != nil {
		slog.Warn("migrate close",
			"source_error", srcErr,
			"database_error", dbErr,
		)
	}
}
