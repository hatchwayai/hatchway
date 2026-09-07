package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hatchwayai/hatchway/internal/config"
	"github.com/hatchwayai/hatchway/internal/db"
)

// RouteRegistrar mounts domain routes beneath /v1.
type RouteRegistrar func(r chi.Router)

// NewRouter builds the public API router and its middleware stack.
func NewRouter(pool *pgxpool.Pool, cfg *config.Config, registrars ...RouteRegistrar) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(RequestLogMiddleware)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, ErrNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusMethodNotAllowed, ErrInvalidRequest, "method not allowed")
	})

	r.Get("/healthz", HealthzHandler())
	r.Get("/readyz", ReadyzHandler(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			return err
		}
		return db.CheckSchema(ctx, pool)
	}))

	r.Route("/v1", func(r chi.Router) {
		r.Use(MaxBodySize(cfg.MaxRequestBytes))
		r.Use(AuthMiddleware(TokenLookupFromDB(pool)))
		r.Use(RateLimitMiddleware(NewRateLimiter(cfg.RateCreatePerMin)))
		r.Use(IdempotencyMiddleware(pool, cfg.PluginSecret))

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

// TokenLookupFromDB returns a prefix lookup backed by PostgreSQL.
func TokenLookupFromDB(pool *pgxpool.Pool) TokenLookup {
	return func(ctx context.Context, prefix string) ([]TokenCandidate, error) {
		rows, err := pool.Query(ctx,
			`SELECT t.id, t.user_id, t.token_hash, u.is_admin
			   FROM api_tokens t
			   JOIN users u ON u.id = t.user_id
			  WHERE t.token_prefix = $1 AND t.revoked_at IS NULL
			  ORDER BY (t.token_hash LIKE 'sha256:%') DESC, t.created_at DESC, t.id`,
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

// GlobalMetrics is the shared process metrics instance.
var GlobalMetrics = NewMetrics()

// StartServer runs the public API and internal control HTTP servers. It returns
// when ctx is cancelled or either server fails. Callers own signal handling;
// cancelling ctx triggers graceful shutdown with a 30s deadline. Handlers are
// passed in by the caller to avoid import cycles with domain packages.
func StartServer(
	ctx context.Context,
	cfg *config.Config,
	database *db.DB,
	pluginHandler http.Handler,
	tunnelAuthorizationHandler http.Handler,
	registrars ...RouteRegistrar,
) error {
	router := NewRouter(database.Pool, cfg, registrars...)

	apiServer := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           router,
		ReadHeaderTimeout: min(cfg.APIReadTimeout, 5*time.Second),
		ReadTimeout:       cfg.APIReadTimeout,
		WriteTimeout:      cfg.APIWriteTimeout,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 * 1024,
	}

	internalMux := http.NewServeMux()
	internalMux.Handle("/frp/plugin/{secret}", pluginHandler)
	internalMux.Handle("/internal/tunnels/authorize/{secret}", tunnelAuthorizationHandler)
	internalMux.HandleFunc("/metrics", MetricsHandler(database.Pool, GlobalMetrics))
	internalServer := &http.Server{
		Addr:              cfg.FRPSPluginAddr,
		Handler:           internalMux,
		ReadHeaderTimeout: min(cfg.PluginTimeout, 5*time.Second),
		ReadTimeout:       cfg.PluginTimeout,
		WriteTimeout:      cfg.PluginTimeout,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    64 * 1024,
	}

	errCh := make(chan error, 2)
	apiListener, err := net.Listen("tcp", cfg.APIAddr)
	if err != nil {
		return fmt.Errorf("listen on API address %s: %w", cfg.APIAddr, err)
	}
	internalListener, err := net.Listen("tcp", cfg.FRPSPluginAddr)
	if err != nil {
		_ = apiListener.Close()
		return fmt.Errorf("listen on internal address %s: %w", cfg.FRPSPluginAddr, err)
	}

	go func() {
		slog.Info("starting API server", "addr", apiListener.Addr().String())
		if err := apiServer.Serve(apiListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("API server: %w", err)
		}
	}()

	go func() {
		slog.Info("starting internal server", "addr", internalListener.Addr().String())
		if err := internalServer.Serve(internalListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("internal server: %w", err)
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
	var (
		shutdownWG   sync.WaitGroup
		shutdownErrs = make(chan error, 2)
	)
	for name, server := range map[string]*http.Server{"API": apiServer, "internal": internalServer} {
		shutdownWG.Add(1)
		go func() {
			defer shutdownWG.Done()
			if err := server.Shutdown(shutdownCtx); err != nil {
				shutdownErrs <- fmt.Errorf("shut down %s server: %w", name, err)
			}
		}()
	}
	shutdownWG.Wait()
	close(shutdownErrs)
	for err := range shutdownErrs {
		listenErr = errors.Join(listenErr, err)
	}

	slog.Info("hatchway server stopped")
	return listenErr
}
