package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	mpg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed sql/*.sql
var sqlFS embed.FS

// MigrationStatus represents the state of a single migration file.
type MigrationStatus struct {
	Version   string
	Filename  string
	Applied   bool
	AppliedAt time.Time // zero value if not applied
}

type Runner struct {
	db *sql.DB
}

func NewRunner(db *sql.DB) *Runner {
	return &Runner{db: db}
}

// newMigrate creates a golang-migrate instance from the embedded SQL files.
func (r *Runner) newMigrate() (*migrate.Migrate, error) {
	subFS, err := fs.Sub(sqlFS, "sql")
	if err != nil {
		return nil, fmt.Errorf("sub fs: %w", err)
	}

	source, err := iofs.New(subFS, ".")
	if err != nil {
		return nil, fmt.Errorf("iofs source: %w", err)
	}

	driver, err := mpg.WithInstance(r.db, &mpg.Config{
		MigrationsTable: "schema_migrations",
	})
	if err != nil {
		return nil, fmt.Errorf("postgres driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return nil, fmt.Errorf("create migrate: %w", err)
	}

	return m, nil
}

// convertLegacyTable migrates from our old schema_migrations format
// (version TEXT, applied_at TIMESTAMPTZ) to golang-migrate's format
// (version BIGINT, dirty BOOLEAN). Only runs once on first upgrade.
func (r *Runner) convertLegacyTable(ctx context.Context) {
	// Check if legacy table exists (has applied_at column)
	var hasAppliedAt bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'schema_migrations' AND column_name = 'applied_at'
		)
	`).Scan(&hasAppliedAt)
	if err != nil || !hasAppliedAt {
		return // Not a legacy table, nothing to do
	}

	// Find the highest applied version number
	var maxVersion int
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(CAST(SUBSTRING(version FROM '^[0-9]+') AS INTEGER)), 0)
		FROM schema_migrations
	`).Scan(&maxVersion)
	if err != nil || maxVersion == 0 {
		// Can't determine version — drop and let golang-migrate recreate
		r.db.ExecContext(ctx, `DROP TABLE IF EXISTS schema_migrations`)
		return
	}

	slog.Info("converting legacy schema_migrations table", "max_version", maxVersion)

	// Drop and recreate in golang-migrate format
	r.db.ExecContext(ctx, `DROP TABLE IF EXISTS schema_migrations`)
	r.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, dirty BOOLEAN NOT NULL)`)
	r.db.ExecContext(ctx, `INSERT INTO schema_migrations (version, dirty) VALUES ($1, false)`, maxVersion)

	slog.Info("legacy migration table converted", "version", maxVersion)
}

// Run applies all pending migrations. Uses golang-migrate with advisory
// locking (safe for concurrent execution) and dirty state tracking.
func (r *Runner) Run(ctx context.Context) error {
	r.convertLegacyTable(ctx)

	m, err := r.newMigrate()
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}

	err = m.Up()
	if err == migrate.ErrNoChange {
		slog.Info("migrations up to date")
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, dirty, _ := m.Version()
	slog.Info("migrations applied", "version", version, "dirty", dirty)
	return nil
}

// Rollback reverts a specific migration version.
func (r *Runner) Rollback(ctx context.Context, version string) error {
	// Extract numeric version from name like "0021_stripe_tax_flag"
	vNum := extractVersion(version)
	if vNum == 0 {
		return fmt.Errorf("invalid version: %s", version)
	}

	r.convertLegacyTable(ctx)

	m, err := r.newMigrate()
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}

	currentVersion, _, _ := m.Version()
	if uint(vNum) != currentVersion {
		return fmt.Errorf("can only rollback the latest version (current: %d, requested: %d)", currentVersion, vNum)
	}

	// Steps(−1) reverts exactly one migration
	if err := m.Steps(-1); err != nil {
		return fmt.Errorf("rollback %s: %w", version, err)
	}

	slog.Info("migration rolled back", "version", version)
	return nil
}

// DryRun returns the list of pending migration filenames without applying them.
func (r *Runner) DryRun(ctx context.Context) ([]string, error) {
	r.convertLegacyTable(ctx)

	m, err := r.newMigrate()
	if err != nil {
		return nil, fmt.Errorf("init migrate: %w", err)
	}

	currentVersion, _, verr := m.Version()
	if verr == migrate.ErrNilVersion {
		currentVersion = 0
	} else if verr != nil {
		return nil, fmt.Errorf("get version: %w", verr)
	}

	// List all up migration files with version > current
	files, err := upMigrationFiles()
	if err != nil {
		return nil, err
	}

	var pending []string
	for _, f := range files {
		v := extractVersion(strings.TrimSuffix(f, ".up.sql"))
		if v > int(currentVersion) {
			pending = append(pending, f)
		}
	}
	return pending, nil
}

// Status returns the state of every known migration.
func (r *Runner) Status(ctx context.Context) ([]MigrationStatus, error) {
	r.convertLegacyTable(ctx)

	m, err := r.newMigrate()
	if err != nil {
		return nil, fmt.Errorf("init migrate: %w", err)
	}

	currentVersion, dirty, verr := m.Version()
	if verr == migrate.ErrNilVersion {
		currentVersion = 0
	} else if verr != nil {
		return nil, fmt.Errorf("get version: %w", verr)
	}

	files, err := upMigrationFiles()
	if err != nil {
		return nil, err
	}

	var statuses []MigrationStatus
	for _, f := range files {
		name := strings.TrimSuffix(f, ".up.sql")
		v := extractVersion(name)
		applied := v > 0 && uint(v) <= currentVersion
		s := MigrationStatus{
			Version:  name,
			Filename: f,
			Applied:  applied,
		}
		if applied && dirty && uint(v) == currentVersion {
			s.Version = name + " (DIRTY)"
		}
		statuses = append(statuses, s)
	}
	return statuses, nil
}

// upMigrationFiles returns sorted list of *.up.sql filenames.
func upMigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(sqlFS, "sql")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// extractVersion parses the numeric prefix from a migration name.
// "0021_stripe_tax_flag" → 21
func extractVersion(name string) int {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 0 {
		return 0
	}
	v := 0
	for _, c := range parts[0] {
		if c >= '0' && c <= '9' {
			v = v*10 + int(c-'0')
		}
	}
	return v
}
