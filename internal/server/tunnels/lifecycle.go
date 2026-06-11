package tunnels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zydo/hatchway/internal/server/api"
)

type Event string

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

func Transition(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event Event) error {
	var currentStatus string
	err := pool.QueryRow(ctx, "SELECT status FROM tunnels WHERE id = $1", tunnelID).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("lookup tunnel %s: %w", tunnelID, err)
	}

	transitions, ok := validTransitions[currentStatus]
	if !ok {
		return fmt.Errorf("no transitions from terminal state %s", currentStatus)
	}

	newStatus, ok := transitions[event]
	if !ok {
		return fmt.Errorf("invalid transition: %s + %s", currentStatus, event)
	}

	tag, err := pool.Exec(ctx, "UPDATE tunnels SET status = $1 WHERE id = $2 AND status = $3", newStatus, tunnelID, currentStatus)
	if err != nil {
		return fmt.Errorf("update tunnel status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("tunnel %s status changed concurrently", tunnelID)
	}

	emitEvent(ctx, pool, tunnelID, string(event), newStatus)
	api.GlobalMetrics.IncrTransition()
	slog.Info("tunnel transition", "tunnel_id", tunnelID, "from", currentStatus, "to", newStatus, "event", event)
	return nil
}

type eventExec interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func emitEvent(ctx context.Context, db eventExec, tunnelID, eventType, newStatus string) {
	payload, err := json.Marshal(map[string]string{"new_status": newStatus})
	if err != nil {
		slog.Error("marshal tunnel event payload failed", "tunnel_id", tunnelID, "error", err)
		return
	}
	if _, err := db.Exec(ctx,
		"INSERT INTO tunnel_events (tunnel_id, event_type, payload) VALUES ($1, $2, $3)",
		tunnelID, eventType, payload,
	); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("emit tunnel event failed", "tunnel_id", tunnelID, "event", eventType, "error", err)
	}
}

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
	// RETURNING gives us per-row IDs so we can emit one Expire event per
	// tunnel — bulk UPDATE alone wipes the audit trail for the only state
	// transition users can't trigger themselves.
	rows, err := pool.Query(ctx,
		`UPDATE tunnels SET status = 'expired'
		  WHERE expires_at < now() AND status IN ('reserved','active','closed')
		  RETURNING id`,
	)
	if err != nil {
		slog.Error("reaper failed", "error", err)
		return
	}
	defer rows.Close()

	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			slog.Error("reaper scan failed", "error", err)
			continue
		}
		expired = append(expired, id)
	}
	if err := rows.Err(); err != nil {
		slog.Error("reaper iterate failed", "error", err)
	}

	for _, id := range expired {
		emitEvent(ctx, pool, id, string(EventExpire), "expired")
		api.GlobalMetrics.IncrTransition()
	}
	if len(expired) > 0 {
		slog.Info("reaper expired tunnels", "count", len(expired))
	}
}

func CountActiveTunnels(ctx context.Context, pool *pgxpool.Pool, userID string) (int, error) {
	var count int
	err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM tunnels WHERE user_id = $1 AND status IN ('reserved','active','closed')",
		userID,
	).Scan(&count)
	return count, err
}

// StartSweepers runs periodic cleanup for tunnel_events and idempotency_keys.
func StartSweepers(ctx context.Context, pool *pgxpool.Pool, eventsRetentionDays, idempotencyRetentionHours int) {
	go runSweeper(ctx, time.Hour, func() {
		sweepExpiredEvents(ctx, pool, eventsRetentionDays)
		sweepIdempotencyKeys(ctx, pool, idempotencyRetentionHours)
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
