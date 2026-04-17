package migrate

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	mpg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed sql/*.sql
var sqlFS embed.FS

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
func Up(db *sql.DB) error {
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
