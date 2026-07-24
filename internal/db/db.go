package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CurrentSchemaVersion is the newest embedded database migration.
const CurrentSchemaVersion = 6

// DB wraps the application's PostgreSQL connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// New opens and verifies a PostgreSQL connection pool.
func New(ctx context.Context, databaseURL string) (*DB, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close releases every connection in the pool.
func (db *DB) Close() {
	db.Pool.Close()
}

// Ping verifies that PostgreSQL is reachable.
func (db *DB) Ping(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

// TX is the query surface shared by pgx transactions and test doubles.
type TX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// InTx runs fn in a transaction and commits only when fn succeeds.
func (db *DB) InTx(ctx context.Context, isoLevel pgx.TxIsoLevel, fn func(tx pgx.Tx) error) error {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isoLevel})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// CheckSchema verifies that all required tables exist in the active schema and
// that golang-migrate reports the expected clean version.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tables := []string{"users", "api_tokens", "tunnels", "tunnel_runtime_tokens", "tunnel_events", "idempotency_keys"}
	var tableCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*)
		   FROM information_schema.tables
		  WHERE table_schema = current_schema()
		    AND table_name = ANY($1)`,
		tables,
	).Scan(&tableCount); err != nil {
		return fmt.Errorf("check required tables: %w", err)
	}
	if tableCount != len(tables) {
		return fmt.Errorf("database schema incomplete: found %d of %d required tables in %s", tableCount, len(tables), "current schema")
	}

	var (
		version int
		dirty   bool
	)
	if err := pool.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty); err != nil {
		return fmt.Errorf("check migration version: %w", err)
	}
	if dirty {
		return fmt.Errorf("database migration %d is dirty", version)
	}
	if version != CurrentSchemaVersion {
		return fmt.Errorf("database schema version %d is not current (want %d)", version, CurrentSchemaVersion)
	}
	return nil
}
