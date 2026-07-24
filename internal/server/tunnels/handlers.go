package tunnels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/server/api"
	"github.com/zydo/hatchway/internal/tokens"
)

// CreateTunnelRequest is the strictly decoded POST /v1/tunnels payload.
type CreateTunnelRequest struct {
	Type       string `json:"type"`
	LocalHost  string `json:"local_host"`
	LocalPort  int    `json:"local_port"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// TunnelResponse is the owner-visible API representation of a tunnel.
type TunnelResponse struct {
	TunnelID     string     `json:"tunnel_id"`
	Status       string     `json:"status"`
	Type         string     `json:"type"`
	PublicURL    string     `json:"public_url"`
	ExpiresAt    *time.Time `json:"expires_at"`
	RuntimeToken string     `json:"runtime_token,omitempty"`
	FRP          *FRPConfig `json:"frp,omitempty"`
}

// FRPConfig contains the issued frpc configuration for a new tunnel.
type FRPConfig struct {
	ServerAddr  string `json:"server_addr"`
	ServerPort  int    `json:"server_port"`
	ServerToken string `json:"server_token"`
	ProxyName   string `json:"proxy_name"`
	ProxyType   string `json:"proxy_type"`
	Subdomain   string `json:"subdomain"`
	LocalIP     string `json:"local_ip"`
	LocalPort   int    `json:"local_port"`
}

// RegisterRoutes mounts the owner and administrator tunnel routes.
func RegisterRoutes(r chi.Router, pool *pgxpool.Pool, cfg *config.Config) {
	r.Post("/tunnels", CreateTunnel(pool, cfg))
	r.Get("/tunnels", ListTunnels(pool, cfg))
	r.Get("/tunnels/{id}", GetTunnel(pool, cfg))
	r.Delete("/tunnels/{id}", DeleteTunnel(pool))

	r.Group(func(r chi.Router) {
		r.Use(api.AdminOnly)
		r.Post("/admin/tunnels/{id}/revoke", AdminRevokeTunnel(pool))
	})
}

// CreateTunnel validates, allocates, and returns a new HTTP tunnel.
func CreateTunnel(pool *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := api.UserIDFromContext(r.Context())
		if userID == "" {
			api.WriteError(w, http.StatusUnauthorized, api.ErrUnauthenticated, "not authenticated")
			return
		}

		var req CreateTunnelRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				api.WriteError(w, http.StatusRequestEntityTooLarge, api.ErrInvalidRequest, "request body too large")
				return
			}
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "invalid JSON body")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				api.WriteError(w, http.StatusRequestEntityTooLarge, api.ErrInvalidRequest, "request body too large")
				return
			}
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "request body must contain one JSON object")
			return
		}

		if req.Type != "http" {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "only http tunnels supported in MVP")
			return
		}
		if req.LocalPort < 1 || req.LocalPort > 65535 {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "local_port must be between 1 and 65535")
			return
		}
		if req.LocalHost == "" {
			req.LocalHost = "127.0.0.1"
		}
		if req.LocalHost != "127.0.0.1" && req.LocalHost != "localhost" {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "local_host must be 127.0.0.1 or localhost")
			return
		}

		if req.TTLSeconds < 0 {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "ttl_seconds must be non-negative")
			return
		}
		if req.TTLSeconds == 0 {
			defaultTTL := time.Hour
			if cfg.MaxTTL < defaultTTL {
				defaultTTL = cfg.MaxTTL
			}
			req.TTLSeconds = int(defaultTTL / time.Second)
		}
		if int64(req.TTLSeconds) > int64(cfg.MaxTTL/time.Second) {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, fmt.Sprintf("ttl exceeds maximum of %s", cfg.MaxTTL))
			return
		}

		// Mint runtime token
		rt, err := tokens.MintRuntimeToken()
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to mint runtime token")
			return
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to create tunnel")
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		// Serialize creates for one user while checking and consuming quota.
		// Without this transaction-scoped advisory lock, simultaneous requests
		// can all observe the same count and exceed MaxConcurrent.
		if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", userID); err != nil {
			slog.Error("quota lock failed", "error", err, "user_id", userID)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "quota check failed")
			return
		}

		count, err := CountNonTerminalTunnels(r.Context(), tx, userID)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "quota check failed")
			return
		}
		if count >= cfg.MaxConcurrent {
			api.WriteError(w, http.StatusForbidden, api.ErrQuotaExceeded, fmt.Sprintf("maximum %d concurrent tunnels", cfg.MaxConcurrent))
			return
		}

		// Generate and insert in one statement. ON CONFLICT lets the
		// transaction remain usable for the astronomically unlikely ID
		// collision, so a fresh ID can be tried without a savepoint.
		var (
			tunnelID  string
			expiresAt time.Time
		)
		for range 3 {
			candidate, err := GenerateTunnelID()
			if err != nil {
				slog.Error("tunnel ID generation failed", "error", err)
				api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to generate tunnel ID")
				return
			}
			err = tx.QueryRow(r.Context(),
				`INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at)
				 VALUES ($1, $2, $3, $4, $5, 'reserved', statement_timestamp() + $6::bigint * interval '1 second')
				 ON CONFLICT (id) DO NOTHING
				 RETURNING expires_at`,
				candidate, userID, req.Type, req.LocalHost, req.LocalPort, req.TTLSeconds,
			).Scan(&expiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to create tunnel")
				return
			}
			tunnelID = candidate
			break
		}
		if tunnelID == "" {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to generate unique tunnel ID")
			return
		}

		_, err = tx.Exec(r.Context(),
			"INSERT INTO tunnel_runtime_tokens (id, tunnel_id, token_prefix, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5)",
			uuid.New().String(), tunnelID, rt.Prefix, rt.Hash, expiresAt,
		)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to store runtime token")
			return
		}

		if err := insertEvent(r.Context(), tx, tunnelID, "created", "reserved"); err != nil {
			slog.Error("create tunnel event failed", "error", err, "tunnel_id", tunnelID)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to create tunnel")
			return
		}

		resp := TunnelResponse{
			TunnelID:     tunnelID,
			Status:       "reserved",
			Type:         req.Type,
			PublicURL:    publicURL(tunnelID, cfg.TunnelDomain),
			ExpiresAt:    &expiresAt,
			RuntimeToken: rt.Raw,
			FRP: &FRPConfig{
				ServerAddr: cfg.FRPSDomain,
				ServerPort: 7000,
				// The frps↔frpc shared secret — NOT the plugin secret, which
				// is server-internal and must never leak via the API.
				ServerToken: cfg.FRPSAuthToken,
				ProxyName:   tunnelID,
				ProxyType:   "http",
				Subdomain:   tunnelID,
				LocalIP:     req.LocalHost,
				LocalPort:   req.LocalPort,
			},
		}
		responseBody, err := json.Marshal(resp)
		if err != nil {
			slog.Error("create tunnel response marshal failed", "error", err, "tunnel_id", tunnelID)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to create tunnel")
			return
		}
		// Match json.Encoder's conventional trailing newline so an original
		// response and an idempotency replay are byte-for-byte identical.
		responseBody = append(responseBody, '\n')
		if _, err := api.CompleteIdempotentResponseInTransaction(
			r.Context(),
			tx,
			http.StatusCreated,
			responseBody,
		); err != nil {
			slog.Error("complete create idempotency response failed", "error", err, "tunnel_id", tunnelID)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to create tunnel")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to create tunnel")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if _, err := w.Write(responseBody); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("response write failed", "op", "create_tunnel", "error", err)
		}
	}
}

const (
	defaultListLimit = 50
	maxListLimit     = 100
)

// listCursor encodes the (created_at, id) of the last row in a page so the
// next page can request rows strictly older than that point. Tunnel IDs are
// unique, so the (timestamp, id) pair is a stable total order.
type listCursor struct {
	CreatedAt time.Time `json:"c"`
	TunnelID  string    `json:"i"`
}

func encodeCursor(c listCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (listCursor, error) {
	var c listCursor
	if s == "" {
		return c, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, fmt.Errorf("invalid cursor encoding")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("invalid cursor payload")
	}
	if c.TunnelID == "" || c.CreatedAt.IsZero() {
		return c, fmt.Errorf("invalid cursor")
	}
	return c, nil
}

// ListTunnels returns an owner-scoped, keyset-paginated tunnel list.
func ListTunnels(pool *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := api.UserIDFromContext(r.Context())

		limit := defaultListLimit
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > maxListLimit {
				api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, fmt.Sprintf("limit must be between 1 and %d", maxListLimit))
				return
			}
			limit = n
		}

		cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, err.Error())
			return
		}

		// Far-future and high-Unicode sentinels cover the application's
		// timestamp and ASCII ID domains, so `(created_at, id) < ($2, $3)`
		// includes every first-page row. One query handles both first-page
		// and follow-on pages. Fetch limit+1 to detect a next page without
		// a count query.
		ts := cursor.CreatedAt
		id := cursor.TunnelID
		if id == "" {
			ts = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
			id = "￿"
		}
		rows, err := pool.Query(r.Context(),
			`SELECT id, type, status, expires_at, created_at
			   FROM tunnels
			  WHERE user_id = $1
			    AND (created_at, id) < ($2, $3)
			  ORDER BY created_at DESC, id DESC
			  LIMIT $4`,
			userID, ts, id, limit+1,
		)
		if err != nil {
			slog.Error("list tunnels query failed", "error", err, "user_id", userID)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "query failed")
			return
		}
		defer rows.Close()

		type rowEntry struct {
			t         TunnelResponse
			createdAt time.Time
		}
		var entries []rowEntry
		for rows.Next() {
			var e rowEntry
			if err := rows.Scan(&e.t.TunnelID, &e.t.Type, &e.t.Status, &e.t.ExpiresAt, &e.createdAt); err != nil {
				slog.Error("list tunnels scan failed", "error", err)
				api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "scan failed")
				return
			}
			e.t.PublicURL = publicURL(e.t.TunnelID, cfg.TunnelDomain)
			entries = append(entries, e)
		}
		if err := rows.Err(); err != nil {
			slog.Error("list tunnels iteration failed", "error", err)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "query failed")
			return
		}

		var nextCursor any
		if len(entries) > limit {
			last := entries[limit-1]
			nextCursor = encodeCursor(listCursor{CreatedAt: last.createdAt, TunnelID: last.t.TunnelID})
			entries = entries[:limit]
		}

		result := make([]TunnelResponse, 0, len(entries))
		for _, e := range entries {
			result = append(result, e.t)
		}

		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, "list_tunnels", map[string]any{
			"tunnels":     result,
			"next_cursor": nextCursor,
		})
	}
}

// GetTunnel returns one owner-scoped tunnel without creation credentials.
func GetTunnel(pool *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := api.UserIDFromContext(r.Context())
		tunnelID := chi.URLParam(r, "id")

		var t TunnelResponse
		err := pool.QueryRow(r.Context(),
			"SELECT id, type, status, expires_at FROM tunnels WHERE id = $1 AND user_id = $2",
			tunnelID, userID,
		).Scan(&t.TunnelID, &t.Type, &t.Status, &t.ExpiresAt)

		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				api.WriteError(w, http.StatusNotFound, api.ErrNotFound, "tunnel not found")
			} else {
				slog.Error("get tunnel query failed", "error", err, "tunnel_id", tunnelID)
				api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "query failed")
			}
			return
		}

		t.PublicURL = publicURL(t.TunnelID, cfg.TunnelDomain)

		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, "get_tunnel", t)
	}
}

// DeleteTunnel idempotently revokes an owner-scoped tunnel.
func DeleteTunnel(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := api.UserIDFromContext(r.Context())
		tunnelID := chi.URLParam(r, "id")

		found, err := Revoke(r.Context(), pool, tunnelID, userID)
		if err != nil {
			slog.Error("delete tunnel revoke failed", "error", err, "tunnel_id", tunnelID)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to revoke tunnel")
			return
		}
		if !found {
			api.WriteError(w, http.StatusNotFound, api.ErrNotFound, "tunnel not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// AdminRevokeTunnel idempotently revokes a tunnel without an ownership filter.
func AdminRevokeTunnel(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tunnelID := chi.URLParam(r, "id")

		found, err := Revoke(r.Context(), pool, tunnelID, "")
		if err != nil {
			slog.Error("admin revoke failed", "error", err, "tunnel_id", tunnelID)
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to revoke tunnel")
			return
		}
		if !found {
			api.WriteError(w, http.StatusNotFound, api.ErrNotFound, "tunnel not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func revokeRuntimeTokens(ctx context.Context, db eventExec, tunnelID string) error {
	if _, err := db.Exec(ctx,
		"UPDATE tunnel_runtime_tokens SET revoked_at = now() WHERE tunnel_id = $1 AND revoked_at IS NULL",
		tunnelID,
	); err != nil {
		return err
	}
	return nil
}

// writeJSON encodes v as JSON and logs an error if the encoder fails. Status
// has already been written by the caller — failure here can't be turned into
// a 5xx, so this exists purely so silent encoder failures don't go unnoticed.
func writeJSON(w http.ResponseWriter, op string, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("response encode failed", "op", op, "error", err)
	}
}

// publicURL builds the public HTTPS URL for a tunnel from its ID and the
// configured tunnel domain.
func publicURL(tunnelID, tunnelDomain string) string {
	return fmt.Sprintf("https://%s.%s", tunnelID, tunnelDomain)
}
