package db

import (
	"embed"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// toMigrateURL converts a standard postgres:// URL to pgx5:// for golang-migrate.
func toMigrateURL(databaseURL string) string {
	return strings.Replace(databaseURL, "postgres://", "pgx5://", 1)
}

func RunMigrations(databaseURL string) error {
	d, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", d, toMigrateURL(databaseURL))
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

func RunMigrationsDown(databaseURL string) error {
	d, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", d, toMigrateURL(databaseURL))
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer m.Close()

	if err := m.Drop(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("drop migrations: %w", err)
	}
	return nil
}
