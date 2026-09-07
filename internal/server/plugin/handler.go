package plugin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hatchwayai/hatchway/internal/config"
	"github.com/hatchwayai/hatchway/internal/tokens"
)

// TransitionFunc is a callback that transitions a tunnel's state.
// Passed in from the caller to avoid import cycles.
type TransitionFunc func(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error

// MetricsIncrFunc increments a plugin op counter. Passed in to avoid import cycles.
type MetricsIncrFunc func(op string)

// DeadlineMetricFunc bumps a counter when the plugin handler hit its deadline.
// Passed in to avoid an import cycle on the api package.
type DeadlineMetricFunc func()

// PluginRequest is the envelope sent by the frps HTTP plugin protocol.
type PluginRequest struct {
	Version string          `json:"version"`
	Op      string          `json:"op"`
	Content json.RawMessage `json:"content"`
}

// PluginResponse is the allow/reject envelope expected by frps.
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

// LoginContent is the frps Login callback payload.
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

// NewProxyContent is the frps NewProxy callback payload.
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

// NewProxyUser contains the client identity and metadata copied into proxy
// callbacks.
type NewProxyUser struct {
	User  string            `json:"user"`
	Metas map[string]string `json:"metas"`
	RunID string            `json:"run_id"`
}

// CloseProxyContent is the frps CloseProxy callback payload.
type CloseProxyContent struct {
	User      NewProxyUser `json:"user"`
	ProxyName string       `json:"proxy_name"`
}

// NewUserConnContent is the frps NewUserConn callback payload.
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
		if r.Method != http.MethodPost {
			writePluginResponse(w, reject("method not allowed"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)

		if cfg.PluginSecret == "" {
			slog.Error("plugin secret not configured")
			writePluginResponse(w, reject("server misconfigured"))
			return
		}

		if !constantTimeEqual(r.PathValue("secret"), cfg.PluginSecret) {
			writePluginResponse(w, reject("unauthorized"))
			return
		}

		op := r.URL.Query().Get("op")
		if op == "" {
			writePluginResponse(w, reject("missing op"))
			return
		}

		maxBodyBytes := cfg.MaxRequestBytes
		if maxBodyBytes <= 0 {
			maxBodyBytes = 64 * 1024
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		var req PluginRequest
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil {
			writePluginResponse(w, reject("invalid request body"))
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writePluginResponse(w, reject("request body must contain one JSON object"))
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

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
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
		return reject(runtimeTokenRejectReason(err))
	}

	// Usage telemetry is deliberately best-effort: authorization has already
	// succeeded, and an accounting write must not turn a valid frps login into
	// an outage.
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
	if np.ProxyType != "http" {
		return reject("only http proxy type supported")
	}
	// Custom domains would bypass the tunnel_id→subdomain isolation enforced
	// below, so the HTTP-only MVP rejects them before touching storage.
	if len(np.CustomDomains) > 0 {
		return reject("custom domains not supported")
	}

	info, err := lookupRuntimeToken(ctx, pool, rt)
	if err != nil {
		slog.Warn("newproxy rejected: token lookup failed", "error", err)
		return reject(runtimeTokenRejectReason(err))
	}

	// proxy_name must equal tunnel_id
	if np.ProxyName != info.tunnelID {
		return reject("proxy name mismatch")
	}

	// The subdomain is always the authenticated tunnel ID.
	if np.Subdomain != info.tunnelID {
		return reject("subdomain mismatch")
	}

	// lookupRuntimeToken already rejects terminal or elapsed tunnels.
	// Transition takes a row lock and closes the race with expiry/revocation.
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
	if errors.Is(err, pgx.ErrNoRows) {
		// Unknown tunnel, just allow — don't block frps.
		return allowUnchange()
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("closeproxy status lookup failed", "tunnel_id", cp.ProxyName, "error", err)
		}
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
	var uc NewUserConnContent
	if err := json.Unmarshal(content, &uc); err != nil {
		return reject("invalid new user connection content")
	}
	if pool == nil || uc.ProxyName == "" {
		return reject("tunnel unavailable")
	}

	// frps does not emit this callback for every proxy type. When it does,
	// apply the same database-clock check as the HTTP request gate.
	allowed, err := tunnelAcceptsTraffic(ctx, pool, uc.ProxyName)
	if err != nil {
		slog.Warn("new user connection rejected: tunnel lookup failed", "tunnel_id", uc.ProxyName, "error", err)
		return reject("tunnel unavailable")
	}
	if !allowed {
		return reject("tunnel unavailable")
	}

	if cfg.LogUserConns {
		emitConnEvent(ctx, pool, uc)
	}
	return allowUnchange()
}

func lookupRuntimeToken(ctx context.Context, pool *pgxpool.Pool, rawToken string) (*tokenInfo, error) {
	prefix, err := tokens.ParsePrefix(rawToken)
	if err != nil {
		return nil, err
	}

	// Multiple tokens may share a 12-char prefix; iterate candidates and let
	// the hash compare decide which is the real match. Filter dead credentials
	// and tunnels before doing any expensive legacy verification.
	rows, err := pool.Query(ctx,
		`SELECT rt.id, rt.tunnel_id, rt.token_hash
		   FROM tunnel_runtime_tokens rt
		   JOIN tunnels t ON t.id = rt.tunnel_id
		  WHERE rt.token_prefix = $1
		    AND rt.revoked_at IS NULL
		    AND rt.expires_at > now()
		    AND t.expires_at > now()
		    AND t.status IN ('reserved', 'active', 'closed')
		  ORDER BY (rt.token_hash LIKE 'sha256:%') DESC, rt.created_at DESC, rt.id`,
		prefix,
	)
	if err != nil {
		return nil, err
	}

	type candidate struct {
		tokenID  string
		tunnelID string
		hash     string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.tokenID, &c.tunnelID, &c.hash); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// Release the query's pool connection before bounded legacy Argon work.
	// Verify current hashes first so an unrelated legacy-prefix collision
	// cannot consume scarce KDF capacity ahead of a valid current token.
	for _, currentPass := range []bool{true, false} {
		for _, c := range candidates {
			if tokens.UsesCurrentHash(c.hash) != currentPass {
				continue
			}
			matches, err := tokens.VerifyTokenContext(ctx, rawToken, c.hash)
			if err != nil {
				return nil, fmt.Errorf("verify runtime token: %w", err)
			}
			if matches {
				return &tokenInfo{tokenID: c.tokenID, tunnelID: c.tunnelID}, nil
			}
		}
	}
	return nil, fmt.Errorf("no matching runtime token")
}

func runtimeTokenRejectReason(err error) string {
	if errors.Is(err, tokens.ErrLegacyVerificationBusy) ||
		errors.Is(err, context.DeadlineExceeded) {
		return "authentication temporarily unavailable"
	}
	return "invalid credentials"
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
