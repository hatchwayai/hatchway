package tunnels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/server/api"
	"github.com/zydo/hatchway/internal/tokens"
)

type CreateTunnelRequest struct {
	Type       string `json:"type"`
	LocalHost  string `json:"local_host"`
	LocalPort  int    `json:"local_port"`
	TTLSeconds int    `json:"ttl_seconds"`
}

type TunnelResponse struct {
	TunnelID     string     `json:"tunnel_id"`
	Status       string     `json:"status"`
	Type         string     `json:"type"`
	PublicURL    string     `json:"public_url"`
	ExpiresAt    *time.Time `json:"expires_at"`
	RuntimeToken string     `json:"runtime_token,omitempty"`
	FRP          *FRPConfig `json:"frp,omitempty"`
}

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

func CreateTunnel(pool *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := api.UserIDFromContext(r.Context())
		if userID == "" {
			api.WriteError(w, http.StatusUnauthorized, api.ErrUnauthenticated, "not authenticated")
			return
		}

		var req CreateTunnelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "invalid JSON body")
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
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, "local_host must be 127.0.0.1")
			return
		}

		if req.TTLSeconds == 0 {
			req.TTLSeconds = 3600
		}
		ttl := time.Duration(req.TTLSeconds) * time.Second
		if ttl > cfg.MaxTTL {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, fmt.Sprintf("ttl exceeds maximum of %s", cfg.MaxTTL))
			return
		}

		// Check concurrent tunnel quota
		count, err := CountActiveTunnels(r.Context(), pool, userID)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "quota check failed")
			return
		}
		if count >= cfg.MaxConcurrent {
			api.WriteError(w, http.StatusForbidden, api.ErrQuotaExceeded, fmt.Sprintf("maximum %d concurrent tunnels", cfg.MaxConcurrent))
			return
		}

		// Generate tunnel ID with uniqueness retry. 80 bits of entropy makes a
		// real collision absurdly unlikely; the loop is here to make the
		// happy path bulletproof. Surface DB errors immediately rather than
		// looping over them and reporting "failed to generate unique ID".
		tunnelID := ""
		for range 3 {
			candidate, err := GenerateTunnelID()
			if err != nil {
				slog.Error("tunnel ID generation failed", "error", err)
				api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to generate tunnel ID")
				return
			}
			var exists bool
			if err := pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM tunnels WHERE id = $1)", candidate).Scan(&exists); err != nil {
				slog.Error("tunnel ID uniqueness check failed", "error", err)
				api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "uniqueness check failed")
				return
			}
			if !exists {
				tunnelID = candidate
				break
			}
		}
		if tunnelID == "" {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to generate unique tunnel ID")
			return
		}

		// Mint runtime token
		rt, err := tokens.MintRuntimeToken()
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to mint runtime token")
			return
		}

		expiresAt := time.Now().Add(ttl)

		// Insert tunnel
		_, err = pool.Exec(r.Context(),
			"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, $3, $4, $5, 'reserved', $6)",
			tunnelID, userID, req.Type, req.LocalHost, req.LocalPort, expiresAt,
		)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to create tunnel")
			return
		}

		// Insert runtime token
		_, err = pool.Exec(r.Context(),
			"INSERT INTO tunnel_runtime_tokens (id, tunnel_id, token_prefix, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5)",
			uuid.New().String(), tunnelID, rt.Prefix, rt.Hash, expiresAt,
		)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.ErrInternal, "failed to store runtime token")
			return
		}

		emitEvent(r.Context(), pool, tunnelID, "created", "reserved")

		resp := TunnelResponse{
			TunnelID:     tunnelID,
			Status:       "reserved",
			Type:         req.Type,
			PublicURL:    fmt.Sprintf("https://%s.%s", tunnelID, cfg.TunnelDomain),
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

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeJSON(r.Context(), w, "create_tunnel", resp)
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
	if c.TunnelID == "" {
		return c, fmt.Errorf("invalid cursor")
	}
	return c, nil
}

func ListTunnels(pool *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := api.UserIDFromContext(r.Context())

		limit := defaultListLimit
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxListLimit {
				limit = n
			}
		}

		cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.ErrInvalidRequest, err.Error())
			return
		}

		// Sentinel for the first page: 'infinity'::timestamptz is greater
		// than any real created_at, and any string compares strictly less
		// than the sentinel id, so `(created_at, id) < ($2, $3)` includes
		// every row. One query handles both first-page and follow-on pages.
		// Fetch limit+1 to detect a next page without a count query.
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
			e.t.PublicURL = fmt.Sprintf("https://%s.%s", e.t.TunnelID, cfg.TunnelDomain)
			entries = append(entries, e)
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
		writeJSON(r.Context(), w, "list_tunnels", map[string]any{
			"tunnels":     result,
			"next_cursor": nextCursor,
		})
	}
}

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
			api.WriteError(w, http.StatusNotFound, api.ErrNotFound, "tunnel not found")
			return
		}

		t.PublicURL = fmt.Sprintf("https://%s.%s", t.TunnelID, cfg.TunnelDomain)

		w.Header().Set("Content-Type", "application/json")
		writeJSON(r.Context(), w, "get_tunnel", t)
	}
}

func DeleteTunnel(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := api.UserIDFromContext(r.Context())
		tunnelID := chi.URLParam(r, "id")

		var owner, status string
		err := pool.QueryRow(r.Context(),
			"SELECT user_id, status FROM tunnels WHERE id = $1", tunnelID,
		).Scan(&owner, &status)
		if err != nil || owner != userID {
			api.WriteError(w, http.StatusNotFound, api.ErrNotFound, "tunnel not found")
			return
		}

		// Idempotent: DELETE on an already-terminal tunnel succeeds. Retries
		// after a network blip shouldn't surface internal state-machine
		// errors to the caller.
		if status == "expired" || status == "revoked" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if err := Transition(r.Context(), pool, tunnelID, EventRevoke); err != nil {
			slog.Warn("delete tunnel transition", "tunnel_id", tunnelID, "error", err)
			api.WriteError(w, http.StatusConflict, api.ErrInvalidRequest, "tunnel state changed concurrently")
			return
		}

		revokeRuntimeTokens(r.Context(), pool, tunnelID)

		w.WriteHeader(http.StatusNoContent)
	}
}

func AdminRevokeTunnel(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tunnelID := chi.URLParam(r, "id")

		var status string
		err := pool.QueryRow(r.Context(), "SELECT status FROM tunnels WHERE id = $1", tunnelID).Scan(&status)
		if err != nil {
			api.WriteError(w, http.StatusNotFound, api.ErrNotFound, "tunnel not found")
			return
		}

		if status == "expired" || status == "revoked" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if err := Transition(r.Context(), pool, tunnelID, EventRevoke); err != nil {
			slog.Warn("admin revoke transition", "tunnel_id", tunnelID, "error", err)
			api.WriteError(w, http.StatusConflict, api.ErrInvalidRequest, "tunnel state changed concurrently")
			return
		}

		revokeRuntimeTokens(r.Context(), pool, tunnelID)

		w.WriteHeader(http.StatusNoContent)
	}
}

func revokeRuntimeTokens(ctx context.Context, pool *pgxpool.Pool, tunnelID string) {
	if _, err := pool.Exec(ctx,
		"UPDATE tunnel_runtime_tokens SET revoked_at = now() WHERE tunnel_id = $1 AND revoked_at IS NULL",
		tunnelID,
	); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("revoke runtime tokens failed", "tunnel_id", tunnelID, "error", err)
	}
}

// writeJSON encodes v as JSON and logs an error if the encoder fails. Status
// has already been written by the caller — failure here can't be turned into
// a 5xx, so this exists purely so silent encoder failures don't go unnoticed.
func writeJSON(ctx context.Context, w http.ResponseWriter, op string, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("response encode failed", "op", op, "error", err)
	}
	_ = ctx
}
