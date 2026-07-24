package tunnels

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/zydo/hatchway/internal/db"
	"github.com/zydo/hatchway/internal/testutil"
)

// Internal-package integration tests exercising the unexported reaper and
// sweeper helpers directly. The external integration_test.go covers handler
// surface; this file covers the periodic-background paths.

func setupLifecyclePool(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	connStr := testutil.DatabaseURL(t)

	require.NoError(t, db.RunMigrations(connStr))

	ctx := context.Background()
	database, err := db.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(database.Close)

	// Reset rows so the test order doesn't matter within or across runs.
	for _, table := range []string{"idempotency_keys", "tunnel_runtime_tokens", "tunnel_events", "tunnels", "api_tokens", "users"} {
		_, err := database.Pool.Exec(ctx, "DELETE FROM "+table)
		require.NoError(t, err)
	}

	userID := uuid.New().String()
	tokenID := uuid.New().String()
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO users (id, email, name) VALUES ($1, 'lifecycle@test', 'lifecycle')",
		userID,
	)
	require.NoError(t, err)
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash) VALUES ($1, $2, 'tmp', 'p', 'h')",
		tokenID, userID,
	)
	require.NoError(t, err)

	return database.Pool, userID, tokenID
}

func TestTransition_StateMachineIntegration(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()

	tunnelID := "t-statemachine000"
	_, err := pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, userID,
	)
	require.NoError(t, err)
	runtimeTokenID := uuid.New().String()
	_, err = pool.Exec(ctx,
		`INSERT INTO tunnel_runtime_tokens
		   (id, tunnel_id, token_prefix, token_hash, expires_at)
		 VALUES ($1, $2, 'runtime-test', 'hash', now() + interval '1 hour')`,
		runtimeTokenID, tunnelID,
	)
	require.NoError(t, err)

	steps := []struct {
		event Event
		want  string
	}{
		{EventNewProxy, "active"},
		{EventCloseProxy, "closed"},
		{EventNewProxy, "active"}, // reconnect path
		{EventRevoke, "revoked"},
	}
	for _, s := range steps {
		if err := Transition(ctx, pool, tunnelID, s.event); err != nil {
			t.Fatalf("Transition %s: %v", s.event, err)
		}
		var got string
		require.NoError(t, pool.QueryRow(ctx, "SELECT status FROM tunnels WHERE id=$1", tunnelID).Scan(&got))
		if got != s.want {
			t.Fatalf("after %s, got %s want %s", s.event, got, s.want)
		}
	}

	var runtimeTokenRevoked bool
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT revoked_at IS NOT NULL FROM tunnel_runtime_tokens WHERE id = $1", runtimeTokenID,
	).Scan(&runtimeTokenRevoked))
	if !runtimeTokenRevoked {
		t.Error("EventRevoke should revoke live runtime credentials atomically")
	}

	if err := Transition(ctx, pool, tunnelID, EventNewProxy); err == nil {
		t.Error("transition from revoked should fail")
	}

	var eventCount int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM tunnel_events WHERE tunnel_id=$1", tunnelID).Scan(&eventCount))
	if eventCount != len(steps) {
		t.Errorf("expected %d events, got %d", len(steps), eventCount)
	}
}

func TestTransition_UnknownTunnel(t *testing.T) {
	pool, _, _ := setupLifecyclePool(t)
	if err := Transition(context.Background(), pool, "t-doesnotexist00", EventRevoke); err == nil {
		t.Error("transition on missing tunnel should error")
	}
}

func TestTransition_InvalidEventReturnsError(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()
	tunnelID := "t-invalidevent00"
	_, err := pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, userID,
	)
	require.NoError(t, err)

	// reserved → CloseProxy is not a defined transition.
	if err := Transition(ctx, pool, tunnelID, EventCloseProxy); err == nil {
		t.Error("invalid transition should error")
	}
}

func TestTransition_ActiveNewProxyIsIdempotent(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()
	tunnelID := "t-active-reconnect"
	_, err := pool.Exec(ctx,
		`INSERT INTO tunnels
		   (id, user_id, type, local_host, local_port, status, expires_at)
		 VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'active', now() + interval '1 hour')`,
		tunnelID, userID,
	)
	require.NoError(t, err)

	require.NoError(t, Transition(ctx, pool, tunnelID, EventNewProxy))

	var eventCount int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM tunnel_events WHERE tunnel_id = $1", tunnelID,
	).Scan(&eventCount))
	if eventCount != 0 {
		t.Errorf("idempotent active reconnect emitted %d lifecycle events", eventCount)
	}
}

func TestTransition_NewProxyRejectsElapsedTunnel(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()
	tunnelID := "t-elapsednewproxy"
	_, err := pool.Exec(ctx,
		`INSERT INTO tunnels
		   (id, user_id, type, local_host, local_port, status, expires_at)
		 VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() - interval '1 second')`,
		tunnelID, userID,
	)
	require.NoError(t, err)

	if err := Transition(ctx, pool, tunnelID, EventNewProxy); err == nil {
		t.Fatal("NewProxy should reject an elapsed tunnel")
	}

	var status string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT status FROM tunnels WHERE id = $1", tunnelID,
	).Scan(&status))
	if status != "reserved" {
		t.Errorf("elapsed tunnel status = %q, want reserved until reaper runs", status)
	}
}

func TestReapExpired_ExpiresAndEmits(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()

	tunnelID := "t-reaperexpire01"
	_, err := pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() - interval '1 minute')",
		tunnelID, userID,
	)
	require.NoError(t, err)

	reapExpired(ctx, pool)

	var status string
	require.NoError(t, pool.QueryRow(ctx, "SELECT status FROM tunnels WHERE id=$1", tunnelID).Scan(&status))
	if status != "expired" {
		t.Fatalf("expected expired, got %s", status)
	}
	var eventType string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT event_type FROM tunnel_events WHERE tunnel_id=$1", tunnelID).Scan(&eventType))
	if eventType != "Expire" {
		t.Errorf("expected Expire event, got %s", eventType)
	}
}

func TestReapExpired_LeavesTerminalAlone(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()

	tunnelID := "t-reaperterminal0"
	_, err := pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'revoked', now() - interval '1 hour')",
		tunnelID, userID,
	)
	require.NoError(t, err)

	reapExpired(ctx, pool)

	var status string
	require.NoError(t, pool.QueryRow(ctx, "SELECT status FROM tunnels WHERE id=$1", tunnelID).Scan(&status))
	if status != "revoked" {
		t.Errorf("reaper should not touch terminal states, got %s", status)
	}
}

func TestStartReaper_StopsOnContextCancel(t *testing.T) {
	pool, _, _ := setupLifecyclePool(t)
	ctx, cancel := context.WithCancel(context.Background())
	StartReaper(ctx, pool, 10*time.Millisecond)
	cancel()
	// Goroutine leak detection isn't built into the standard runner; this test
	// just verifies cancel doesn't deadlock. Give the goroutine a moment.
	time.Sleep(50 * time.Millisecond)
}

func TestSweepers_DeleteOldRows(t *testing.T) {
	pool, userID, tokenID := setupLifecyclePool(t)
	ctx := context.Background()

	tunnelID := "t-sweepertesting0"
	_, err := pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, userID,
	)
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		"INSERT INTO tunnel_events (tunnel_id, event_type, payload, created_at) VALUES ($1, 'Expire', '{}'::jsonb, now() - interval '60 days')",
		tunnelID,
	)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		"INSERT INTO idempotency_keys (token_id, key, request_hash, created_at) VALUES ($1, 'k-stale', 'h', now() - interval '5 days')",
		tokenID,
	)
	require.NoError(t, err)

	sweepExpiredEvents(ctx, pool, 30)
	sweepIdempotencyKeys(ctx, pool, 24)

	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM tunnel_events WHERE tunnel_id=$1", tunnelID).Scan(&n))
	if n != 0 {
		t.Errorf("events sweeper left %d rows", n)
	}
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM idempotency_keys WHERE token_id=$1", tokenID).Scan(&n))
	if n != 0 {
		t.Errorf("idempotency sweeper left %d rows", n)
	}
}

func TestSweepers_KeepRecentRows(t *testing.T) {
	pool, userID, tokenID := setupLifecyclePool(t)
	ctx := context.Background()

	tunnelID := "t-sweeperrecent01"
	_, err := pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, userID,
	)
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		"INSERT INTO tunnel_events (tunnel_id, event_type, payload) VALUES ($1, 'Expire', '{}'::jsonb)",
		tunnelID,
	)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		"INSERT INTO idempotency_keys (token_id, key, request_hash) VALUES ($1, 'k-recent', 'h')",
		tokenID,
	)
	require.NoError(t, err)

	sweepExpiredEvents(ctx, pool, 30)
	sweepIdempotencyKeys(ctx, pool, 24)

	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM tunnel_events WHERE tunnel_id=$1", tunnelID).Scan(&n))
	if n != 1 {
		t.Errorf("recent event sweeper deleted unexpected rows, got %d", n)
	}
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM idempotency_keys WHERE token_id=$1", tokenID).Scan(&n))
	if n != 1 {
		t.Errorf("recent idempotency key swept, got %d", n)
	}
}

func TestSweepRuntimeTokens(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()

	tunnelID := "t-runtimetoksweep"
	_, err := pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, userID,
	)
	require.NoError(t, err)

	type tokRow struct {
		id        string
		prefix    string
		expiresAt string // SQL expression
		revokedAt string // SQL expression or "NULL"
	}
	rows := []tokRow{
		// Past retention: revoked 30 days ago.
		{uuid.New().String(), "rt_oldrevoked", "now() + interval '1 hour'", "now() - interval '30 days'"},
		// Past retention: expired 30 days ago, never revoked.
		{uuid.New().String(), "rt_oldexpired", "now() - interval '30 days'", "NULL"},
		// Recently dead (within retention): revoked 1 day ago.
		{uuid.New().String(), "rt_recentrevoke", "now() + interval '1 hour'", "now() - interval '1 day'"},
		// Live: not revoked, not yet expired.
		{uuid.New().String(), "rt_live________", "now() + interval '1 hour'", "NULL"},
	}
	for _, r := range rows {
		_, err := pool.Exec(ctx,
			"INSERT INTO tunnel_runtime_tokens (id, tunnel_id, token_prefix, token_hash, expires_at, revoked_at) VALUES ($1, $2, $3, 'h', "+r.expiresAt+", "+r.revokedAt+")",
			r.id, tunnelID, r.prefix,
		)
		require.NoError(t, err)
	}

	sweepRuntimeTokens(ctx, pool, 7)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM tunnel_runtime_tokens WHERE tunnel_id=$1", tunnelID).Scan(&n))
	if n != 2 {
		t.Fatalf("expected 2 surviving tokens (recent-revoke + live), got %d", n)
	}

	// Past-retention rows must be gone; recent-revoke and live must survive.
	for _, r := range rows[:2] {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM tunnel_runtime_tokens WHERE id=$1)", r.id).Scan(&exists))
		if exists {
			t.Errorf("expected token %s to be swept", r.prefix)
		}
	}
	for _, r := range rows[2:] {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM tunnel_runtime_tokens WHERE id=$1)", r.id).Scan(&exists))
		if !exists {
			t.Errorf("expected token %s to survive", r.prefix)
		}
	}
}

func TestCountNonTerminalTunnels(t *testing.T) {
	pool, userID, _ := setupLifecyclePool(t)
	ctx := context.Background()

	for i, status := range []string{"reserved", "active", "closed", "expired", "revoked"} {
		_, err := pool.Exec(ctx,
			"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, $3, now() + interval '1 hour')",
			"t-counttest"+string(rune('a'+i))+"00000", userID, status,
		)
		require.NoError(t, err)
	}

	n, err := CountNonTerminalTunnels(ctx, pool, userID)
	require.NoError(t, err)
	if n != 3 {
		t.Errorf("expected 3 active (reserved+active+closed), got %d", n)
	}
}
