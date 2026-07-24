package db

import (
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// toMigrateURL converts a standard postgres:// or postgresql:// URL to pgx5://
// for golang-migrate, which registers its pgx v5 driver under that scheme.
func toMigrateURL(databaseURL string) string {
	if rest, ok := strings.CutPrefix(databaseURL, "postgresql://"); ok {
		return "pgx5://" + rest
	}
	if rest, ok := strings.CutPrefix(databaseURL, "postgres://"); ok {
		return "pgx5://" + rest
	}
	return databaseURL
}

func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	d, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("create migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", d, toMigrateURL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return m, nil
}

func closeMigrator(m *migrate.Migrate) error {
	sourceErr, databaseErr := m.Close()
	return errors.Join(sourceErr, databaseErr)
}

// RunMigrations applies every pending embedded migration.
func RunMigrations(databaseURL string) (returnErr error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeMigrator(m); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close migrator: %w", err))
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// RunMigrationsDown rolls back every applied migration. It is intended for
// migration verification and destructive development teardown.
func RunMigrationsDown(databaseURL string) (returnErr error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeMigrator(m); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close migrator: %w", err))
		}
	}()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run down migrations: %w", err)
	}
	return nil
}
