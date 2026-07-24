package tunnels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zydo/hatchway/internal/server/api"
)

// Event identifies a lifecycle transition trigger.
type Event string

// Lifecycle transition events.
const (
	EventNewProxy   Event = "NewProxy"
	EventCloseProxy Event = "CloseProxy"
	EventExpire     Event = "Expire"
	EventRevoke     Event = "Revoke"
)

var validTransitions = map[string]map[Event]string{
	"reserved": {EventNewProxy: "active", EventExpire: "expired", EventRevoke: "revoked"},
	"active":   {EventCloseProxy: "closed", EventExpire: "expired", EventRevoke: "revoked"},
	"closed":   {EventNewProxy: "active", EventExpire: "expired", EventRevoke: "revoked"},
}

// Transition atomically validates and applies one tunnel state change and
// records its event. Revocation is routed through Revoke so runtime
// credentials cannot remain live after an EventRevoke transition.
func Transition(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event Event) error {
	if event == EventRevoke {
		found, err := Revoke(ctx, pool, tunnelID, "")
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("lookup tunnel %s: %w", tunnelID, pgx.ErrNoRows)
		}
		return nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tunnel transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM tunnels WHERE id = $1 FOR UPDATE", tunnelID).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("lookup tunnel %s: %w", tunnelID, err)
	}

	if event == EventNewProxy {
		// now()/statement_timestamp() are fixed before a blocked row lock is
		// acquired. Check the database wall clock in a second statement while
		// holding the lock so a callback cannot activate an elapsed tunnel.
		var withinTTL bool
		if err := tx.QueryRow(ctx,
			"SELECT COALESCE(expires_at > clock_timestamp(), false) FROM tunnels WHERE id = $1",
			tunnelID,
		).Scan(&withinTTL); err != nil {
			return fmt.Errorf("check tunnel expiry: %w", err)
		}
		if !withinTTL {
			return fmt.Errorf("tunnel %s has expired", tunnelID)
		}

		// A reconnect may arrive while the database still says active if frps
		// restarted or its CloseProxy callback was lost. Accept it as an
		// idempotent no-op rather than fabricating a duplicate transition.
		if currentStatus == "active" {
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit idempotent tunnel activation: %w", err)
			}
			return nil
		}
	}

	transitions, ok := validTransitions[currentStatus]
	if !ok {
		return fmt.Errorf("no transitions from terminal state %s", currentStatus)
	}

	newStatus, ok := transitions[event]
	if !ok {
		return fmt.Errorf("invalid transition: %s + %s", currentStatus, event)
	}

	tag, err := tx.Exec(ctx, "UPDATE tunnels SET status = $1 WHERE id = $2 AND status = $3", newStatus, tunnelID, currentStatus)
	if err != nil {
		return fmt.Errorf("update tunnel status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("tunnel %s status changed concurrently", tunnelID)
	}

	if err := insertEvent(ctx, tx, tunnelID, string(event), newStatus); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tunnel transition: %w", err)
	}

	api.GlobalMetrics.IncrTransition()
	slog.Info("tunnel transition", "tunnel_id", tunnelID, "from", currentStatus, "to", newStatus, "event", event)
	return nil
}

// Revoke atomically revokes a tunnel, its live runtime credentials, and its
// lifecycle event. ownerID scopes the lookup when non-empty; an empty ownerID
// is the administrator path. Terminal rows are successful no-ops apart from
// repairing any still-live credentials left by an interrupted older release.
// The boolean result is false when the tunnel does not exist in the requested
// ownership scope.
func Revoke(ctx context.Context, pool *pgxpool.Pool, tunnelID, ownerID string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tunnel revoke: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := "SELECT status FROM tunnels WHERE id = $1 FOR UPDATE"
	args := []any{tunnelID}
	if ownerID != "" {
		query = "SELECT status FROM tunnels WHERE id = $1 AND user_id = $2 FOR UPDATE"
		args = append(args, ownerID)
	}

	var currentStatus string
	if err := tx.QueryRow(ctx, query, args...).Scan(&currentStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lookup tunnel for revoke: %w", err)
	}

	transitioned := false
	if currentStatus != "expired" && currentStatus != "revoked" {
		newStatus, ok := validTransitions[currentStatus][EventRevoke]
		if !ok {
			return false, fmt.Errorf("invalid revoke from state %s", currentStatus)
		}
		if _, err := tx.Exec(ctx, "UPDATE tunnels SET status = $1 WHERE id = $2", newStatus, tunnelID); err != nil {
			return false, fmt.Errorf("update tunnel status for revoke: %w", err)
		}
		if err := insertEvent(ctx, tx, tunnelID, string(EventRevoke), newStatus); err != nil {
			return false, err
		}
		transitioned = true
	}

	if err := revokeRuntimeTokens(ctx, tx, tunnelID); err != nil {
		return false, fmt.Errorf("revoke runtime tokens: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit tunnel revoke: %w", err)
	}

	if transitioned {
		api.GlobalMetrics.IncrTransition()
		slog.Info("tunnel transition", "tunnel_id", tunnelID, "from", currentStatus, "to", "revoked", "event", EventRevoke)
	}
	return true, nil
}

type eventExec interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func insertEvent(ctx context.Context, db eventExec, tunnelID, eventType, newStatus string) error {
	payload, err := json.Marshal(map[string]string{"new_status": newStatus})
	if err != nil {
		return fmt.Errorf("marshal tunnel event payload: %w", err)
	}
	if _, err := db.Exec(ctx,
		"INSERT INTO tunnel_events (tunnel_id, event_type, payload) VALUES ($1, $2, $3)",
		tunnelID, eventType, payload,
	); err != nil {
		return fmt.Errorf("insert tunnel event: %w", err)
	}
	return nil
}

// StartReaper starts a background loop that expires elapsed non-terminal
// tunnels until ctx is canceled.
func StartReaper(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				reapExpired(ctx, pool)
			}
		}
	}()
}

func reapExpired(ctx context.Context, pool *pgxpool.Pool) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		slog.Error("reaper transaction failed", "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// RETURNING gives us per-row IDs so we can emit one Expire event per
	// tunnel — bulk UPDATE alone wipes the audit trail for the only state
	// transition users can't trigger themselves.
	rows, err := tx.Query(ctx,
		`UPDATE tunnels SET status = 'expired'
		  WHERE expires_at < now() AND status IN ('reserved','active','closed')
		  RETURNING id`,
	)
	if err != nil {
		slog.Error("reaper failed", "error", err)
		return
	}

	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			slog.Error("reaper scan failed", "error", err)
			rows.Close()
			return
		}
		expired = append(expired, id)
	}
	if err := rows.Err(); err != nil {
		slog.Error("reaper iterate failed", "error", err)
		rows.Close()
		return
	}
	rows.Close()

	for _, id := range expired {
		if err := insertEvent(ctx, tx, id, string(EventExpire), "expired"); err != nil {
			slog.Error("reaper event failed", "tunnel_id", id, "error", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("reaper commit failed", "error", err)
		return
	}
	for range expired {
		api.GlobalMetrics.IncrTransition()
	}
	if len(expired) > 0 {
		slog.Info("reaper expired tunnels", "count", len(expired))
	}
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CountNonTerminalTunnels returns the quota-consuming tunnel count for a user.
func CountNonTerminalTunnels(ctx context.Context, db rowQuerier, userID string) (int, error) {
	var count int
	err := db.QueryRow(ctx,
		"SELECT COUNT(*) FROM tunnels WHERE user_id = $1 AND status IN ('reserved','active','closed')",
		userID,
	).Scan(&count)
	return count, err
}

// StartSweepers runs periodic cleanup for tunnel_events, idempotency_keys,
// and dead runtime tokens.
func StartSweepers(ctx context.Context, pool *pgxpool.Pool, eventsRetentionDays, idempotencyRetentionHours, runtimeTokenRetentionDays int) {
	go runSweeper(ctx, time.Hour, func() {
		sweepExpiredEvents(ctx, pool, eventsRetentionDays)
		sweepIdempotencyKeys(ctx, pool, idempotencyRetentionHours)
		sweepRuntimeTokens(ctx, pool, runtimeTokenRetentionDays)
	})
}

func runSweeper(ctx context.Context, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn()
		}
	}
}

func sweepExpiredEvents(ctx context.Context, pool *pgxpool.Pool, retentionDays int) {
	tag, err := pool.Exec(ctx,
		"DELETE FROM tunnel_events WHERE created_at < now() - $1::int * interval '1 day'",
		retentionDays,
	)
	if err != nil {
		slog.Error("events sweeper failed", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("swept old tunnel_events", "count", n, "retention_days", retentionDays)
	}
}

func sweepIdempotencyKeys(ctx context.Context, pool *pgxpool.Pool, retentionHours int) {
	tag, err := pool.Exec(ctx,
		"DELETE FROM idempotency_keys WHERE created_at < now() - $1::int * interval '1 hour'",
		retentionHours,
	)
	if err != nil {
		slog.Error("idempotency sweeper failed", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("swept old idempotency_keys", "count", n, "retention_hours", retentionHours)
	}
}

// sweepRuntimeTokens deletes runtime tokens that are no longer accepted by the
// plugin (revoked OR past expires_at) and whose terminal event is older than
// retentionDays. Live tokens — not revoked and not past expiry — are kept so
// in-flight or recently-issued credentials are never removed.
func sweepRuntimeTokens(ctx context.Context, pool *pgxpool.Pool, retentionDays int) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM tunnel_runtime_tokens
		  WHERE (revoked_at IS NOT NULL AND revoked_at < now() - $1::int * interval '1 day')
		     OR (revoked_at IS NULL     AND expires_at < now() - $1::int * interval '1 day')`,
		retentionDays,
	)
	if err != nil {
		slog.Error("runtime tokens sweeper failed", "error", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("swept old tunnel_runtime_tokens", "count", n, "retention_days", retentionDays)
	}
}
