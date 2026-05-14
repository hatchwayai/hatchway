package api

import (
	"fmt"
	"net/http"
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

func (m *Metrics) IncrPluginOp(op string) {
	if c, ok := m.PluginOps[op]; ok {
		c.Add(1)
	}
}

func (m *Metrics) IncrPluginDeadlineExceeded() {
	m.PluginDeadlineExceeded.Add(1)
}

func (m *Metrics) IncrRateLimitReject() {
	m.RateLimitRejects.Add(1)
}

func (m *Metrics) IncrTransition() {
	m.TunnelTransitions.Add(1)
}

// MetricsHandler returns an http.HandlerFunc that serves Prometheus-format
// metrics. HELP/TYPE lines are emitted so scrapers don't warn and so the
// output renders correctly in Grafana's metric browser.
func MetricsHandler(pool *pgxpool.Pool, m *Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		fmt.Fprintln(w, "# HELP hatchway_plugin_ops_total frps plugin callback count by op.")
		fmt.Fprintln(w, "# TYPE hatchway_plugin_ops_total counter")
		for op, counter := range m.PluginOps {
			fmt.Fprintf(w, "hatchway_plugin_ops_total{op=%q} %d\n", op, counter.Load())
		}

		fmt.Fprintln(w, "# HELP hatchway_rate_limit_rejections_total Requests rejected by the API rate limiter.")
		fmt.Fprintln(w, "# TYPE hatchway_rate_limit_rejections_total counter")
		fmt.Fprintf(w, "hatchway_rate_limit_rejections_total %d\n", m.RateLimitRejects.Load())

		fmt.Fprintln(w, "# HELP hatchway_plugin_deadline_exceeded_total Plugin callbacks that tripped the request deadline.")
		fmt.Fprintln(w, "# TYPE hatchway_plugin_deadline_exceeded_total counter")
		fmt.Fprintf(w, "hatchway_plugin_deadline_exceeded_total %d\n", m.PluginDeadlineExceeded.Load())

		fmt.Fprintln(w, "# HELP hatchway_tunnel_transitions_total Tunnel state machine transitions.")
		fmt.Fprintln(w, "# TYPE hatchway_tunnel_transitions_total counter")
		fmt.Fprintf(w, "hatchway_tunnel_transitions_total %d\n", m.TunnelTransitions.Load())

		if pool != nil {
			rows, err := pool.Query(r.Context(),
				"SELECT status, COUNT(*) FROM tunnels GROUP BY status",
			)
			if err == nil {
				fmt.Fprintln(w, "# HELP hatchway_tunnels_by_status Active tunnel count grouped by status.")
				fmt.Fprintln(w, "# TYPE hatchway_tunnels_by_status gauge")
				defer rows.Close()
				for rows.Next() {
					var status string
					var count int64
					if rows.Scan(&status, &count) == nil {
						fmt.Fprintf(w, "hatchway_tunnels_by_status{status=%q} %d\n", status, count)
					}
				}
			}

			stat := pool.Stat()
			fmt.Fprintln(w, "# HELP hatchway_db_pool_total_connections Total pgx pool connections.")
			fmt.Fprintln(w, "# TYPE hatchway_db_pool_total_connections gauge")
			fmt.Fprintf(w, "hatchway_db_pool_total_connections %d\n", stat.TotalConns())

			fmt.Fprintln(w, "# HELP hatchway_db_pool_idle_connections Idle pgx pool connections.")
			fmt.Fprintln(w, "# TYPE hatchway_db_pool_idle_connections gauge")
			fmt.Fprintf(w, "hatchway_db_pool_idle_connections %d\n", stat.IdleConns())
		}
	}
}
