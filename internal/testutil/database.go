// Package testutil contains shared infrastructure for integration tests.
package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestDatabaseURLEnv is deliberately distinct from the application's
// DATABASE_URL. Integration fixtures clear application tables and some
// migration tests recreate the public schema, so they must never implicitly
// target a database selected for the running service.
const TestDatabaseURLEnv = "HATCHWAY_TEST_DATABASE_URL"

// DatabaseURL returns an explicitly configured disposable test database or
// starts an isolated PostgreSQL container. Explicit database names must end in
// "_test" before any caller can run destructive fixture setup.
func DatabaseURL(t testing.TB) string {
	t.Helper()

	if connStr := strings.TrimSpace(os.Getenv(TestDatabaseURLEnv)); connStr != "" {
		if err := validateTestDatabaseURL(connStr); err != nil {
			t.Fatalf("%s is unsafe: %v", TestDatabaseURLEnv, err)
		}
		return connStr
	}

	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:16",
		postgres.WithDatabase("hatchway_test"),
		postgres.WithUsername("hatchway"),
		postgres.WithPassword("hatchway_test"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL test container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminate PostgreSQL test container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("build PostgreSQL test connection string: %v", err)
	}
	if err := validateTestDatabaseURL(connStr); err != nil {
		t.Fatalf("generated test database URL is unsafe: %v", err)
	}
	return connStr
}

func validateTestDatabaseURL(connStr string) error {
	if strings.TrimSpace(connStr) == "" {
		return fmt.Errorf("connection string is empty")
	}
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		return fmt.Errorf("database name %q must end in _test", cfg.ConnConfig.Database)
	}
	return nil
}
