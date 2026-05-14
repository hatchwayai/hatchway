package db

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupTestDB(t *testing.T) string {
	t.Helper()

	// Use DATABASE_URL env var if set (CI service container)
	if connStr := os.Getenv("DATABASE_URL"); connStr != "" {
		return connStr
	}

	// Fall back to testcontainers (local dev)
	ctx := context.Background()

	c, err := postgres.Run(ctx,
		"postgres:16",
		postgres.WithDatabase("hatchway_test"),
		postgres.WithUsername("hatchway"),
		postgres.WithPassword("hatchway_test"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections"),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	connStr, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return connStr
}

func TestMigrations_UpDownUp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	connStr := setupTestDB(t)

	// Up
	err := RunMigrations(connStr)
	require.NoError(t, err)

	// Verify schema
	ctx := context.Background()
	database, err := New(ctx, connStr)
	require.NoError(t, err)
	defer database.Close()

	err = CheckSchema(ctx, database.Pool)
	require.NoError(t, err)

	// Down
	err = RunMigrationsDown(connStr)
	require.NoError(t, err)

	// Up again (idempotent)
	err = RunMigrations(connStr)
	require.NoError(t, err)

	err = CheckSchema(ctx, database.Pool)
	require.NoError(t, err)
}

func TestDB_PingAndInTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	connStr := setupTestDB(t)
	require.NoError(t, RunMigrations(connStr))

	ctx := context.Background()
	database, err := New(ctx, connStr)
	require.NoError(t, err)
	defer database.Close()

	if err := database.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}

	// Commit path.
	err = database.InTx(ctx, pgx.ReadCommitted, func(tx pgx.Tx) error {
		var one int
		return tx.QueryRow(ctx, "SELECT 1").Scan(&one)
	})
	if err != nil {
		t.Errorf("InTx commit: %v", err)
	}

	// Rollback path: returning an error from the callback must propagate
	// and prevent commit.
	sentinel := errors.New("rollback please")
	err = database.InTx(ctx, pgx.ReadCommitted, func(tx pgx.Tx) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("InTx rollback err = %v, want %v", err, sentinel)
	}
}

func TestCheckSchema_MissingTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	connStr := setupTestDB(t)
	require.NoError(t, RunMigrations(connStr))

	ctx := context.Background()
	database, err := New(ctx, connStr)
	require.NoError(t, err)
	defer database.Close()

	// Drop a required table to force CheckSchema to fail.
	_, err = database.Pool.Exec(ctx, "DROP TABLE IF EXISTS users CASCADE")
	require.NoError(t, err)

	if err := CheckSchema(ctx, database.Pool); err == nil {
		t.Error("CheckSchema should error after dropping users")
	}

	// Restore for other tests.
	require.NoError(t, RunMigrationsDown(connStr))
	require.NoError(t, RunMigrations(connStr))
}

func TestNew_BadURL(t *testing.T) {
	_, err := New(context.Background(), "not-a-url")
	if err == nil {
		t.Error("expected error for malformed URL")
	}
}
