package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/hatchwayai/hatchway/internal/testutil"
)

func setupTestDB(t *testing.T) string {
	t.Helper()
	return testutil.DatabaseURL(t)
}

func resetPublicSchema(t *testing.T, ctx context.Context, database *DB) {
	t.Helper()
	_, err := database.Pool.Exec(ctx, "DROP SCHEMA public CASCADE")
	require.NoError(t, err)
	_, err = database.Pool.Exec(ctx, "CREATE SCHEMA public")
	require.NoError(t, err)
}

func TestMigrations_UpgradePopulatedV5(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	connStr := setupTestDB(t)
	ctx := context.Background()
	database, err := New(ctx, connStr)
	require.NoError(t, err)
	resetPublicSchema(t, ctx, database)
	database.Close()

	migrator, err := newMigrator(connStr)
	require.NoError(t, err)
	require.NoError(t, migrator.Migrate(5))
	sourceErr, databaseErr := migrator.Close()
	require.NoError(t, errors.Join(sourceErr, databaseErr))

	database, err = New(ctx, connStr)
	require.NoError(t, err)
	defer database.Close()

	userID := uuid.New().String()
	tokenID := uuid.New().String()
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO users (id, email) VALUES ($1, 'v5-upgrade@test')",
		userID,
	)
	require.NoError(t, err)
	_, err = database.Pool.Exec(ctx,
		`INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash)
		 VALUES ($1, $2, 'v5-upgrade', 'prefix', 'hash')`,
		tokenID, userID,
	)
	require.NoError(t, err)
	originalBody := []byte(`{"upgrade":"preserved"}`)
	_, err = database.Pool.Exec(ctx,
		`INSERT INTO idempotency_keys
		   (token_id, key, request_hash, response_status, response_body)
		 VALUES ($1, 'v5-key', 'request-hash', 201, $2)`,
		tokenID, originalBody,
	)
	require.NoError(t, err)

	require.NoError(t, RunMigrations(connStr))
	require.NoError(t, CheckSchema(ctx, database.Pool))

	var (
		completedAt time.Time
		storedBody  []byte
	)
	require.NoError(t, database.Pool.QueryRow(ctx,
		`SELECT completed_at, response_body
		   FROM idempotency_keys
		  WHERE token_id = $1 AND key = 'v5-key'`,
		tokenID,
	).Scan(&completedAt, &storedBody))
	require.False(t, completedAt.IsZero())
	require.Equal(t, originalBody, storedBody)

	_, err = database.Pool.Exec(ctx,
		`INSERT INTO idempotency_keys
		   (token_id, key, request_hash, response_status, response_body, completed_at)
		 VALUES ($1, 'invalid-completion', 'request-hash', NULL, NULL, now())`,
		tokenID,
	)
	require.Error(t, err, "completed rows without a response status must violate the completion constraint")
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

	// Downgrading past migration 0003 must tolerate current encrypted cache
	// bytes, which are intentionally neither UTF-8 nor JSON.
	userID := uuid.New().String()
	tokenID := uuid.New().String()
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO users (id, email) VALUES ($1, 'migration-cache@test')",
		userID,
	)
	require.NoError(t, err)
	_, err = database.Pool.Exec(ctx,
		`INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash)
		 VALUES ($1, $2, 'migration-test', 'prefix', 'hash')`,
		tokenID, userID,
	)
	require.NoError(t, err)
	_, err = database.Pool.Exec(ctx,
		`INSERT INTO idempotency_keys
		   (token_id, key, request_hash, response_status, response_body, completed_at)
		 VALUES ($1, 'encrypted-cache', 'request-hash', 201, $2, now())`,
		tokenID, []byte{0xff, 0x00, 0x81},
	)
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

	// Restore the shared CI database from a genuinely empty schema. A normal
	// migration rollback cannot run after this test deliberately removed a
	// table that older down migrations need to alter.
	resetPublicSchema(t, ctx, database)
	require.NoError(t, RunMigrations(connStr))
}

func TestNew_BadURL(t *testing.T) {
	_, err := New(context.Background(), "not-a-url")
	if err == nil {
		t.Error("expected error for malformed URL")
	}
}
