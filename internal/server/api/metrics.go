package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Metrics tracks counters exposed via the /metrics endpoint.
type Metrics struct {
	PluginOps              map[string]*atomic.Int64 // op -> count
	PluginDeadlineExceeded atomic.Int64
	RateLimitRejects       atomic.Int64
	TunnelTransitions      atomic.Int64
}

// NewMetrics creates a Metrics with initialized counters.
func NewMetrics() *Metrics {
	return &Metrics{
		PluginOps: map[string]*atomic.Int64{
			"Login":       {},
			"NewProxy":    {},
			"CloseProxy":  {},
			"NewUserConn": {},
			"Ping":        {},
			"NewWorkConn": {},
		},
	}
}

// IncrPluginOp increments the counter for a recognized frps callback.
func (m *Metrics) IncrPluginOp(op string) {
	if c, ok := m.PluginOps[op]; ok {
		c.Add(1)
	}
}

// IncrPluginDeadlineExceeded records a plugin callback deadline.
func (m *Metrics) IncrPluginDeadlineExceeded() {
	m.PluginDeadlineExceeded.Add(1)
}

// IncrRateLimitReject records a rejected API create request.
func (m *Metrics) IncrRateLimitReject() {
	m.RateLimitRejects.Add(1)
}

// IncrTransition records a committed tunnel state transition.
func (m *Metrics) IncrTransition() {
	m.TunnelTransitions.Add(1)
}

// MetricsHandler returns an http.HandlerFunc that serves Prometheus-format
// metrics. HELP/TYPE lines are emitted so scrapers don't warn and so the
// output renders correctly in Grafana's metric browser.
func MetricsHandler(pool *pgxpool.Pool, m *Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		body := make([]byte, 0, 2048)
		body = append(body,
			"# HELP hatchway_plugin_ops_total frps plugin callback count by op.\n"+
				"# TYPE hatchway_plugin_ops_total counter\n"...,
		)
		ops := make([]string, 0, len(m.PluginOps))
		for op := range m.PluginOps {
			ops = append(ops, op)
		}
		sort.Strings(ops)
		for _, op := range ops {
			counter := m.PluginOps[op]
			body = fmt.Appendf(body, "hatchway_plugin_ops_total{op=%q} %d\n", op, counter.Load())
		}

		body = fmt.Appendf(body,
			"# HELP hatchway_rate_limit_rejections_total Requests rejected by the API rate limiter.\n"+
				"# TYPE hatchway_rate_limit_rejections_total counter\n"+
				"hatchway_rate_limit_rejections_total %d\n"+
				"# HELP hatchway_plugin_deadline_exceeded_total Plugin callbacks that tripped the request deadline.\n"+
				"# TYPE hatchway_plugin_deadline_exceeded_total counter\n"+
				"hatchway_plugin_deadline_exceeded_total %d\n"+
				"# HELP hatchway_tunnel_transitions_total Tunnel state machine transitions.\n"+
				"# TYPE hatchway_tunnel_transitions_total counter\n"+
				"hatchway_tunnel_transitions_total %d\n",
			m.RateLimitRejects.Load(),
			m.PluginDeadlineExceeded.Load(),
			m.TunnelTransitions.Load(),
		)

		if pool != nil {
			rows, err := pool.Query(r.Context(),
				"SELECT status, COUNT(*) FROM tunnels GROUP BY status ORDER BY status",
			)
			if err == nil {
				body = append(body,
					"# HELP hatchway_tunnels_by_status Tunnel count grouped by status.\n"+
						"# TYPE hatchway_tunnels_by_status gauge\n"...,
				)
				for rows.Next() {
					var status string
					var count int64
					if err := rows.Scan(&status, &count); err != nil {
						slog.Warn("metrics tunnel status scan failed", "error", err)
						continue
					}
					body = fmt.Appendf(body, "hatchway_tunnels_by_status{status=%q} %d\n", status, count)
				}
				if err := rows.Err(); err != nil {
					slog.Warn("metrics tunnel status iteration failed", "error", err)
				}
				rows.Close()
			} else {
				slog.Warn("metrics tunnel status query failed", "error", err)
			}

			stat := pool.Stat()
			body = fmt.Appendf(body,
				"# HELP hatchway_db_pool_total_connections Total pgx pool connections.\n"+
					"# TYPE hatchway_db_pool_total_connections gauge\n"+
					"hatchway_db_pool_total_connections %d\n"+
					"# HELP hatchway_db_pool_idle_connections Idle pgx pool connections.\n"+
					"# TYPE hatchway_db_pool_idle_connections gauge\n"+
					"hatchway_db_pool_idle_connections %d\n",
				stat.TotalConns(),
				stat.IdleConns(),
			)
		}

		if _, err := w.Write(body); err != nil {
			slog.Warn("metrics response write failed", "error", err)
		}
	}
}
