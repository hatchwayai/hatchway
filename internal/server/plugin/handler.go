package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/tokens"
)

// TransitionFunc is a callback that transitions a tunnel's state.
// Passed in from the caller to avoid import cycles.
type TransitionFunc func(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error

// MetricsIncrFunc increments a plugin op counter. Passed in to avoid import cycles.
type MetricsIncrFunc func(op string)

// DeadlineMetricFunc bumps a counter when the plugin handler hit its deadline.
// Passed in to avoid an import cycle on the api package.
type DeadlineMetricFunc func()

// frp plugin protocol types

type PluginRequest struct {
	Version string          `json:"version"`
	Op      string          `json:"op"`
	Content json.RawMessage `json:"content"`
}

type PluginResponse struct {
	Reject       bool            `json:"reject"`
	RejectReason string          `json:"reject_reason,omitempty"`
	Unchange     bool            `json:"unchange,omitempty"`
	Content      json.RawMessage `json:"content,omitempty"`
}

func reject(reason string) PluginResponse {
	return PluginResponse{Reject: true, RejectReason: reason}
}

func allowUnchange() PluginResponse {
	return PluginResponse{Reject: false, Unchange: true}
}

// Login content

type LoginContent struct {
	Version       string            `json:"version"`
	Hostname      string            `json:"hostname"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	User          string            `json:"user"`
	Timestamp     int64             `json:"timestamp"`
	PrivilegeKey  string            `json:"privilege_key"`
	RunID         string            `json:"run_id"`
	PoolCount     int               `json:"pool_count"`
	Metas         map[string]string `json:"metas"`
	ClientAddress string            `json:"client_address"`
}

// NewProxy content

type NewProxyContent struct {
	User               NewProxyUser      `json:"user"`
	ProxyName          string            `json:"proxy_name"`
	ProxyType          string            `json:"proxy_type"`
	UseEncryption      bool              `json:"use_encryption"`
	UseCompression     bool              `json:"use_compression"`
	BandwidthLimit     string            `json:"bandwidth_limit"`
	BandwidthLimitMode string            `json:"bandwidth_limit_mode"`
	Group              string            `json:"group"`
	GroupKey           string            `json:"group_key"`
	RemotePort         int               `json:"remote_port"`
	CustomDomains      []string          `json:"custom_domains"`
	Subdomain          string            `json:"subdomain"`
	Locations          []string          `json:"locations"`
	HTTPUser           string            `json:"http_user"`
	HTTPPwd            string            `json:"http_pwd"`
	HostHeaderRewrite  string            `json:"host_header_rewrite"`
	Headers            map[string]string `json:"headers"`
	SK                 string            `json:"sk"`
	Multiplexer        string            `json:"multiplexer"`
	Metas              map[string]string `json:"metas"`
}

type NewProxyUser struct {
	User  string            `json:"user"`
	Metas map[string]string `json:"metas"`
	RunID string            `json:"run_id"`
}

// CloseProxy content

type CloseProxyContent struct {
	User      NewProxyUser `json:"user"`
	ProxyName string       `json:"proxy_name"`
}

// NewUserConn content

type NewUserConnContent struct {
	User       NewProxyUser `json:"user"`
	ProxyName  string       `json:"proxy_name"`
	ProxyType  string       `json:"proxy_type"`
	RemoteAddr string       `json:"remote_addr"`
}

// tokenInfo holds the result of a runtime token lookup.

type tokenInfo struct {
	tokenID  string
	tunnelID string
}

// Handler returns an http.HandlerFunc that handles frps plugin callbacks.
// transitionFn is a callback to the tunnels.Transition function, passed in to
// avoid an import cycle between the plugin and tunnels packages.
func Handler(pool *pgxpool.Pool, cfg *config.Config, transitionFn TransitionFunc, metricsIncr MetricsIncrFunc, deadlineMetric DeadlineMetricFunc) http.HandlerFunc {
	timeout := cfg.PluginTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)

		if cfg.PluginSecret == "" {
			slog.Error("plugin secret not configured")
			writePluginResponse(w, reject("server misconfigured"))
			return
		}

		if r.PathValue("secret") != cfg.PluginSecret {
			writePluginResponse(w, reject("unauthorized"))
			return
		}

		op := r.URL.Query().Get("op")
		if op == "" {
			writePluginResponse(w, reject("missing op"))
			return
		}

		var req PluginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writePluginResponse(w, reject("invalid request body"))
			return
		}

		slog.Info("plugin request", "op", op)

		var resp PluginResponse
		switch op {
		case "Login":
			resp = handleLogin(ctx, pool, req.Content)
		case "NewProxy":
			resp = handleNewProxy(ctx, pool, transitionFn, req.Content)
		case "CloseProxy":
			resp = handleCloseProxy(ctx, pool, transitionFn, req.Content)
		case "NewUserConn":
			resp = handleNewUserConn(ctx, pool, cfg, req.Content)
		case "Ping", "NewWorkConn":
			resp = allowUnchange()
		default:
			resp = reject("unknown op")
		}

		// If our deadline tripped while handling the call, frps will treat
		// us as unhealthy. Surface it as a metric so operators can tune
		// HATCHWAY_PLUGIN_TIMEOUT before it becomes a regular outage.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			slog.Warn("plugin handler deadline exceeded", "op", op, "timeout", timeout)
			if deadlineMetric != nil {
				deadlineMetric()
			}
		}

		writePluginResponse(w, resp)

		if metricsIncr != nil {
			metricsIncr(op)
		}
	}
}

func writePluginResponse(w http.ResponseWriter, resp PluginResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Warn("plugin response encode failed", "error", err)
	}
}

func handleLogin(ctx context.Context, pool *pgxpool.Pool, content json.RawMessage) PluginResponse {
	var login LoginContent
	if err := json.Unmarshal(content, &login); err != nil {
		return reject("invalid login content")
	}

	rt := login.Metas["runtime_token"]
	if rt == "" {
		return reject("missing runtime token")
	}

	info, err := lookupRuntimeToken(ctx, pool, rt)
	if err != nil {
		slog.Warn("login rejected: token lookup failed", "error", err)
		return reject("invalid credentials")
	}

	if _, err := pool.Exec(ctx,
		"UPDATE tunnel_runtime_tokens SET last_used_at = now(), use_count = use_count + 1 WHERE id = $1",
		info.tokenID,
	); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("update runtime token last_used_at failed", "tunnel_id", info.tunnelID, "error", err)
	}

	slog.Info("login accepted", "tunnel_id", info.tunnelID)
	return allowUnchange()
}

func handleNewProxy(ctx context.Context, pool *pgxpool.Pool, transitionFn TransitionFunc, content json.RawMessage) PluginResponse {
	var np NewProxyContent
	if err := json.Unmarshal(content, &np); err != nil {
		return reject("invalid newproxy content")
	}

	// Extract runtime token from user.metas (global metadata moves to user.metas in non-Login ops)
	rt := ""
	if np.User.Metas != nil {
		rt = np.User.Metas["runtime_token"]
	}
	if rt == "" {
		return reject("missing runtime token")
	}

	info, err := lookupRuntimeToken(ctx, pool, rt)
	if err != nil {
		slog.Warn("newproxy rejected: token lookup failed", "error", err)
		return reject("invalid credentials")
	}

	// proxy_name must equal tunnel_id
	if np.ProxyName != info.tunnelID {
		return reject("proxy name mismatch")
	}

	// proxy_type must be http in MVP
	if np.ProxyType != "http" {
		return reject("only http proxy type supported")
	}

	// subdomain must equal tunnel_id; custom_domains are not allowed in MVP
	// because they bypass the tunnel_id→subdomain isolation that the plugin
	// otherwise enforces.
	if np.Subdomain != info.tunnelID {
		return reject("subdomain mismatch")
	}
	if len(np.CustomDomains) > 0 {
		return reject("custom domains not supported")
	}

	// Check tunnel is not in a terminal state
	var status string
	err = pool.QueryRow(ctx, "SELECT status FROM tunnels WHERE id = $1", info.tunnelID).Scan(&status)
	if err != nil {
		return reject("tunnel not found")
	}

	if status == "expired" || status == "revoked" {
		return reject("tunnel is " + status)
	}

	// Transition: reserved→active or closed→active
	err = transitionFn(ctx, pool, info.tunnelID, "NewProxy")
	if err != nil {
		slog.Warn("newproxy transition failed", "tunnel_id", info.tunnelID, "error", err)
		return reject("transition failed")
	}

	slog.Info("newproxy accepted", "tunnel_id", info.tunnelID, "proxy_type", np.ProxyType)
	return allowUnchange()
}

func handleCloseProxy(ctx context.Context, pool *pgxpool.Pool, transitionFn TransitionFunc, content json.RawMessage) PluginResponse {
	var cp CloseProxyContent
	if err := json.Unmarshal(content, &cp); err != nil {
		return reject("invalid closeproxy content")
	}

	// Check current state before transitioning
	var status string
	err := pool.QueryRow(ctx, "SELECT status FROM tunnels WHERE id = $1", cp.ProxyName).Scan(&status)
	if err != nil {
		// Unknown tunnel, just allow — don't block frps
		return allowUnchange()
	}

	if status == "active" {
		err = transitionFn(ctx, pool, cp.ProxyName, "CloseProxy")
		if err != nil {
			slog.Warn("closeproxy transition failed", "tunnel_id", cp.ProxyName, "error", err)
		}
	}

	return allowUnchange()
}

func handleNewUserConn(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, content json.RawMessage) PluginResponse {
	if !cfg.LogUserConns {
		return allowUnchange()
	}

	var uc NewUserConnContent
	if err := json.Unmarshal(content, &uc); err != nil {
		return allowUnchange()
	}

	emitConnEvent(ctx, pool, uc)
	return allowUnchange()
}

func lookupRuntimeToken(ctx context.Context, pool *pgxpool.Pool, rawToken string) (*tokenInfo, error) {
	prefix, err := tokens.ParsePrefix(rawToken)
	if err != nil {
		return nil, err
	}

	// Multiple tokens may share a 12-char prefix; iterate candidates and let
	// the hash compare decide which is the real match. Without this, a fresh
	// token whose prefix happens to collide with an older one in the same
	// table would be silently rejected.
	rows, err := pool.Query(ctx,
		"SELECT id, tunnel_id, token_hash, expires_at, revoked_at FROM tunnel_runtime_tokens WHERE token_prefix = $1",
		prefix,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	for rows.Next() {
		var tokenID, tunnelID, storedHash string
		var expiresAt time.Time
		var revokedAt *time.Time
		if err := rows.Scan(&tokenID, &tunnelID, &storedHash, &expiresAt, &revokedAt); err != nil {
			return nil, err
		}
		if !tokens.VerifyToken(rawToken, storedHash) {
			continue
		}
		if revokedAt != nil {
			return nil, fmt.Errorf("token revoked")
		}
		if now.After(expiresAt) {
			return nil, fmt.Errorf("token expired")
		}
		return &tokenInfo{tokenID: tokenID, tunnelID: tunnelID}, nil
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no matching runtime token")
}

func emitConnEvent(ctx context.Context, pool *pgxpool.Pool, uc NewUserConnContent) {
	// remote_addr and proxy_type come from frpc's report; never interpolate
	// them into a JSON literal.
	payload, err := json.Marshal(map[string]string{
		"proxy_type":  uc.ProxyType,
		"remote_addr": uc.RemoteAddr,
	})
	if err != nil {
		slog.Warn("marshal conn event payload failed", "tunnel_id", uc.ProxyName, "error", err)
		return
	}
	// tunnel_events.id is BIGSERIAL — don't pass an id.
	if _, err := pool.Exec(ctx,
		"INSERT INTO tunnel_events (tunnel_id, event_type, payload) VALUES ($1, 'NewUserConn', $2)",
		uc.ProxyName, payload,
	); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("emit conn event failed", "tunnel_id", uc.ProxyName, "error", err)
	}
}
