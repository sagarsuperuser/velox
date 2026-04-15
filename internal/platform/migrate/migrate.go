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
)

//go:embed sql/*.sql
var sqlFS embed.FS

const defaultTimeout = 60 * time.Second

// MigrationStatus represents the state of a single migration file.
type MigrationStatus struct {
	Version   string
	Filename  string
	Applied   bool
	AppliedAt time.Time // zero value if not applied
}

type Runner struct {
	db      *sql.DB
	timeout time.Duration
}

type Option func(*Runner)

func WithTimeout(d time.Duration) Option {
	return func(r *Runner) {
		if d > 0 {
			r.timeout = d
		}
	}
}

func NewRunner(db *sql.DB, opts ...Option) *Runner {
	r := &Runner{db: db, timeout: defaultTimeout}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// isUpMigration returns true if the filename is a forward (up) migration,
// i.e. it ends in .sql but NOT .down.sql.
func isUpMigration(name string) bool {
	return strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".down.sql")
}

// upMigrationFiles returns sorted up-migration filenames from the embedded FS.
func upMigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(sqlFS, "sql")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && isUpMigration(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// versionFromFilename extracts the version string from an up-migration filename.
// Example: "0019_enterprise_hardening.sql" -> "0019_enterprise_hardening"
func versionFromFilename(name string) string {
	return strings.TrimSuffix(name, ".sql")
}

// downFilename returns the expected down-migration filename for a version.
// Example: "0019_enterprise_hardening" -> "0019_enterprise_hardening.down.sql"
func downFilename(version string) string {
	return version + ".down.sql"
}

func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.ensureTable(ctx); err != nil {
		return err
	}

	applied, err := r.appliedVersions(ctx)
	if err != nil {
		return err
	}

	names, err := upMigrationFiles()
	if err != nil {
		return err
	}

	for _, name := range names {
		version := versionFromFilename(name)
		if applied[version] {
			continue
		}

		content, err := fs.ReadFile(sqlFS, "sql/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}

		slog.Info("migration applied", "version", version)
	}

	return nil
}

// Rollback reverses a single applied migration by running its .down.sql file.
// It returns an error if the version was never applied, or if no down file exists.
func (r *Runner) Rollback(ctx context.Context, version string) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.ensureTable(ctx); err != nil {
		return err
	}

	// Verify the version is actually applied.
	applied, err := r.appliedVersions(ctx)
	if err != nil {
		return err
	}
	if !applied[version] {
		return fmt.Errorf("rollback %s: version is not applied", version)
	}

	// Read the down migration file.
	downFile := downFilename(version)
	content, err := fs.ReadFile(sqlFS, "sql/"+downFile)
	if err != nil {
		return fmt.Errorf("rollback %s: no down migration file found (expected sql/%s)", version, downFile)
	}

	// Execute the down migration and remove the version record in one transaction.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rollback begin tx for %s: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("rollback execute %s: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("rollback remove record %s: %w", version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rollback commit %s: %w", version, err)
	}

	slog.Info("migration rolled back", "version", version)
	return nil
}

// DryRun returns the list of pending migration filenames that would be applied
// by Run, without actually executing them. Useful for CI/CD validation.
func (r *Runner) DryRun(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.ensureTable(ctx); err != nil {
		return nil, err
	}

	applied, err := r.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}

	names, err := upMigrationFiles()
	if err != nil {
		return nil, err
	}

	var pending []string
	for _, name := range names {
		version := versionFromFilename(name)
		if !applied[version] {
			pending = append(pending, name)
		}
	}
	return pending, nil
}

// Status returns the full state of every known migration: which are applied
// (with timestamps) and which are pending. Results are sorted by version.
func (r *Runner) Status(ctx context.Context) ([]MigrationStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.ensureTable(ctx); err != nil {
		return nil, err
	}

	// Fetch applied versions with their timestamps.
	appliedMap, err := r.appliedVersionsWithTimestamps(ctx)
	if err != nil {
		return nil, err
	}

	names, err := upMigrationFiles()
	if err != nil {
		return nil, err
	}

	var statuses []MigrationStatus
	for _, name := range names {
		version := versionFromFilename(name)
		s := MigrationStatus{
			Version:  version,
			Filename: name,
		}
		if ts, ok := appliedMap[version]; ok {
			s.Applied = true
			s.AppliedAt = ts
		}
		statuses = append(statuses, s)
	}
	return statuses, nil
}

// ensureTable creates the schema_migrations table if it does not exist.
func (r *Runner) ensureTable(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func (r *Runner) appliedVersions(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// appliedVersionsWithTimestamps returns applied versions mapped to their applied_at timestamps.
func (r *Runner) appliedVersionsWithTimestamps(ctx context.Context) (map[string]time.Time, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT version, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]time.Time)
	for rows.Next() {
		var v string
		var ts time.Time
		if err := rows.Scan(&v, &ts); err != nil {
			return nil, err
		}
		applied[v] = ts
	}
	return applied, rows.Err()
}
