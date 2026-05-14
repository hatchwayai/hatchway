package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/db"
)

type RouteRegistrar func(r chi.Router)

func NewRouter(pool *pgxpool.Pool, cfg *config.Config, registrars ...RouteRegistrar) *chi.Mux {
	r := chi.NewRouter()
	r.Use(RequestLogMiddleware)

	r.Get("/healthz", HealthzHandler())
	r.Get("/readyz", ReadyzHandler(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return pool.Ping(ctx)
	}))

	r.Route("/v1", func(r chi.Router) {
		r.Use(MaxBodySize(cfg.MaxRequestBytes))
		r.Use(AuthMiddleware(TokenLookupFromDB(pool)))
		r.Use(RateLimitMiddleware(NewRateLimiter(cfg.RateCreatePerMin)))
		r.Use(IdempotencyMiddleware(pool))

		for _, reg := range registrars {
			reg(r)
		}

		r.Get("/me", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"user_id":  UserIDFromContext(r.Context()),
				"is_admin": IsAdminFromContext(r.Context()),
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				slog.Error("/v1/me encode failed", "error", err)
			}
		})
	})

	return r
}

// MaxBodySize caps the request body for every route it wraps. Limit applies
// per-request; the JSON decoder and idempotency hasher see an error from
// MaxBytesReader once the cap is hit. A non-positive limit disables the cap
// entirely so tests constructing a bare *config.Config don't accidentally
// reject every request with a 0-byte limit.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if maxBytes > 0 && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func TokenLookupFromDB(pool *pgxpool.Pool) TokenLookup {
	return func(ctx context.Context, prefix string) ([]TokenCandidate, error) {
		rows, err := pool.Query(ctx,
			`SELECT t.id, t.user_id, t.token_hash, u.is_admin
			   FROM api_tokens t
			   JOIN users u ON u.id = t.user_id
			  WHERE t.token_prefix = $1 AND t.revoked_at IS NULL`,
			prefix,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var candidates []TokenCandidate
		for rows.Next() {
			var c TokenCandidate
			if err := rows.Scan(&c.TokenID, &c.UserID, &c.Hash, &c.IsAdmin); err != nil {
				return nil, err
			}
			candidates = append(candidates, c)
		}
		return candidates, rows.Err()
	}
}

// PluginHandlerFactory creates the frp plugin handler. Passed in from the caller
// (server.go) to avoid import cycles — api cannot import tunnels or plugin directly
// since tunnels imports api.
type PluginHandlerFactory func() http.HandlerFunc

// GlobalMetrics is the shared metrics instance for the server.
var GlobalMetrics = NewMetrics()

// StartServer runs the API and plugin HTTP servers. It returns when ctx is
// cancelled or either server fails. Callers own signal handling; cancelling
// ctx triggers graceful shutdown with a 30s deadline.
func StartServer(ctx context.Context, cfg *config.Config, database *db.DB, pluginHandler PluginHandlerFactory, registrars ...RouteRegistrar) error {
	router := NewRouter(database.Pool, cfg, registrars...)

	apiServer := &http.Server{
		Addr:         cfg.APIAddr,
		Handler:      router,
		ReadTimeout:  cfg.APIReadTimeout,
		WriteTimeout: cfg.APIWriteTimeout,
	}

	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/frp/plugin/{secret}", pluginHandler())
	pluginMux.HandleFunc("/metrics", MetricsHandler(database.Pool, GlobalMetrics))
	pluginServer := &http.Server{
		Addr:    cfg.FRPSPluginAddr,
		Handler: pluginMux,
	}

	errCh := make(chan error, 2)

	go func() {
		slog.Info("starting API server", "addr", cfg.APIAddr)
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	go func() {
		slog.Info("starting plugin server", "addr", cfg.FRPSPluginAddr)
		if err := pluginServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	slog.Info("hatchway server ready")

	var listenErr error
	select {
	case listenErr = <-errCh:
	case <-ctx.Done():
		slog.Info("shutdown requested")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	slog.Info("draining HTTP servers...")
	_ = apiServer.Shutdown(shutdownCtx)
	_ = pluginServer.Shutdown(shutdownCtx)

	slog.Info("hatchway server stopped")
	return listenErr
}
