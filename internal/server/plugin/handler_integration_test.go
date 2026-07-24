package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/db"
	"github.com/zydo/hatchway/internal/testutil"
	"github.com/zydo/hatchway/internal/tokens"
)

// Integration tests for plugin handlers — these need a real DB to exercise
// the runtime token lookup and state-transition paths. The pure-unit tests
// in handler_test.go and handler_sub_test.go cover the no-DB reject paths.

type pluginFixture struct {
	pool       *pgxpool.Pool
	cfg        *config.Config
	userID     string
	tunnelID   string
	runtimeTok string
	tokenID    string
}

func setupPluginFixture(t *testing.T) *pluginFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	connStr := testutil.DatabaseURL(t)

	require.NoError(t, db.RunMigrations(connStr))

	ctx := context.Background()
	database, err := db.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(database.Close)

	for _, table := range []string{"idempotency_keys", "tunnel_runtime_tokens", "tunnel_events", "tunnels", "api_tokens", "users"} {
		_, err := database.Pool.Exec(ctx, "DELETE FROM "+table)
		require.NoError(t, err)
	}

	userID := uuid.New().String()
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO users (id, email, name) VALUES ($1, 'plugin@test', 'plugin')",
		userID,
	)
	require.NoError(t, err)

	tunnelID := "t-pluginfixture00"
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, userID,
	)
	require.NoError(t, err)

	rt, err := tokens.MintRuntimeToken()
	require.NoError(t, err)
	tokenID := uuid.New().String()
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO tunnel_runtime_tokens (id, tunnel_id, token_prefix, token_hash, expires_at) VALUES ($1, $2, $3, $4, now() + interval '1 hour')",
		tokenID, tunnelID, rt.Prefix, rt.Hash,
	)
	require.NoError(t, err)

	return &pluginFixture{
		pool:       database.Pool,
		cfg:        &config.Config{PluginSecret: "plugin-secret", TunnelDomain: "tunnel.example.com"},
		userID:     userID,
		tunnelID:   tunnelID,
		runtimeTok: rt.Raw,
		tokenID:    tokenID,
	}
}

func callTunnelAuthorization(t *testing.T, f *pluginFixture, secret, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/internal/tunnels/authorize/"+secret, nil)
	req.Header.Set(TunnelHostHeader, host)
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/tunnels/authorize/{secret}", TunnelAuthorizationHandler(f.pool, f.cfg))
	mux.ServeHTTP(rec, req)
	return rec
}

func callPluginOp(t *testing.T, f *pluginFixture, transitionFn TransitionFunc, op string, body any) *PluginResponse {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/frp/plugin/"+f.cfg.PluginSecret+"?op="+op, bytes.NewReader(b))
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("/frp/plugin/{secret}", Handler(f.pool, f.cfg, transitionFn, nil, nil))
	mux.ServeHTTP(w, req)

	var resp PluginResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return &resp
}

func TestPluginIntegration_LoginAcceptsValidToken(t *testing.T) {
	f := setupPluginFixture(t)
	resp := callPluginOp(t, f, nil, "Login", map[string]any{
		"content": LoginContent{Metas: map[string]string{"runtime_token": f.runtimeTok}},
	})
	if resp.Reject {
		t.Fatalf("login should accept valid token, got reject: %s", resp.RejectReason)
	}
	if !resp.Unchange {
		t.Error("login should be unchange on accept")
	}

	// last_used_at must have been bumped.
	var useCount int64
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT use_count FROM tunnel_runtime_tokens WHERE id=$1", f.tokenID).Scan(&useCount))
	if useCount != 1 {
		t.Errorf("expected use_count=1 after login, got %d", useCount)
	}
}

func TestPluginIntegration_LoginRejectsRevokedToken(t *testing.T) {
	f := setupPluginFixture(t)
	_, err := f.pool.Exec(context.Background(),
		"UPDATE tunnel_runtime_tokens SET revoked_at = now() WHERE id=$1", f.tokenID)
	require.NoError(t, err)

	resp := callPluginOp(t, f, nil, "Login", map[string]any{
		"content": LoginContent{Metas: map[string]string{"runtime_token": f.runtimeTok}},
	})
	if !resp.Reject {
		t.Error("login should reject revoked token")
	}
}

func TestPluginIntegration_LoginRejectsExpiredToken(t *testing.T) {
	f := setupPluginFixture(t)
	_, err := f.pool.Exec(context.Background(),
		"UPDATE tunnel_runtime_tokens SET expires_at = now() - interval '1 minute' WHERE id=$1", f.tokenID)
	require.NoError(t, err)

	resp := callPluginOp(t, f, nil, "Login", map[string]any{
		"content": LoginContent{Metas: map[string]string{"runtime_token": f.runtimeTok}},
	})
	if !resp.Reject {
		t.Error("login should reject expired token")
	}
}

func TestPluginIntegration_LoginRejectsUnavailableTunnel(t *testing.T) {
	tests := []struct {
		name   string
		update string
	}{
		{
			name:   "terminal",
			update: "UPDATE tunnels SET status = 'revoked' WHERE id = $1",
		},
		{
			name:   "elapsed",
			update: "UPDATE tunnels SET expires_at = now() - interval '1 minute' WHERE id = $1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := setupPluginFixture(t)
			_, err := f.pool.Exec(context.Background(), tt.update, f.tunnelID)
			require.NoError(t, err)

			resp := callPluginOp(t, f, nil, "Login", map[string]any{
				"content": LoginContent{Metas: map[string]string{"runtime_token": f.runtimeTok}},
			})
			if !resp.Reject {
				t.Error("login should reject a runtime token for an unavailable tunnel")
			}
		})
	}
}

func TestPluginIntegration_LoginRejectsWrongHashWithPrefixCollision(t *testing.T) {
	f := setupPluginFixture(t)
	// Stash a second row with the same prefix but a hash that won't match —
	// proves the multi-candidate lookup picks the right row rather than the
	// first one returned.
	other := uuid.New().String()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO tunnel_runtime_tokens (id, tunnel_id, token_prefix, token_hash, expires_at)
		 VALUES ($1, $2, $3, 'unrelated-hash-value', now() + interval '1 hour')`,
		other, f.tunnelID, f.runtimeTok[:12],
	)
	require.NoError(t, err)

	resp := callPluginOp(t, f, nil, "Login", map[string]any{
		"content": LoginContent{Metas: map[string]string{"runtime_token": f.runtimeTok}},
	})
	if resp.Reject {
		t.Errorf("login should still accept real token despite prefix collision: %s", resp.RejectReason)
	}
}

func TestPluginIntegration_NewProxyAcceptsAndTransitions(t *testing.T) {
	f := setupPluginFixture(t)

	called := false
	transitionFn := func(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error {
		called = true
		if tunnelID != f.tunnelID {
			t.Errorf("transition tunnel_id mismatch: %s", tunnelID)
		}
		if event != "NewProxy" {
			t.Errorf("transition event = %s", event)
		}
		return nil
	}

	resp := callPluginOp(t, f, transitionFn, "NewProxy", map[string]any{
		"content": NewProxyContent{
			User:      NewProxyUser{RunID: "run-new", Metas: map[string]string{"runtime_token": f.runtimeTok}},
			ProxyName: f.tunnelID,
			ProxyType: "http",
			Subdomain: f.tunnelID,
		},
	})
	if resp.Reject {
		t.Fatalf("NewProxy should accept, got: %s", resp.RejectReason)
	}
	if !called {
		t.Error("transition function should have been called")
	}
}

func TestPluginIntegration_NewProxyRejectsProxyNameMismatch(t *testing.T) {
	f := setupPluginFixture(t)
	resp := callPluginOp(t, f, nil, "NewProxy", map[string]any{
		"content": NewProxyContent{
			User:      NewProxyUser{RunID: "run-new", Metas: map[string]string{"runtime_token": f.runtimeTok}},
			ProxyName: "t-wrong0000000000",
			ProxyType: "http",
			Subdomain: f.tunnelID,
		},
	})
	if !resp.Reject {
		t.Error("expected reject for proxy_name mismatch")
	}
}

func TestPluginIntegration_NewProxyRejectsSubdomainMismatch(t *testing.T) {
	f := setupPluginFixture(t)
	resp := callPluginOp(t, f, nil, "NewProxy", map[string]any{
		"content": NewProxyContent{
			User:      NewProxyUser{RunID: "run-new", Metas: map[string]string{"runtime_token": f.runtimeTok}},
			ProxyName: f.tunnelID,
			ProxyType: "http",
			Subdomain: "wrong-subdomain",
		},
	})
	if !resp.Reject {
		t.Error("expected reject for subdomain mismatch")
	}
}

func TestPluginIntegration_NewProxyRejectsCustomDomains(t *testing.T) {
	f := setupPluginFixture(t)
	resp := callPluginOp(t, f, nil, "NewProxy", map[string]any{
		"content": NewProxyContent{
			User:          NewProxyUser{RunID: "run-new", Metas: map[string]string{"runtime_token": f.runtimeTok}},
			ProxyName:     f.tunnelID,
			ProxyType:     "http",
			Subdomain:     f.tunnelID,
			CustomDomains: []string{"evil.example.com"},
		},
	})
	if !resp.Reject {
		t.Error("expected reject for custom_domains")
	}
}

func TestPluginIntegration_NewProxyRejectsNonHTTP(t *testing.T) {
	f := setupPluginFixture(t)
	resp := callPluginOp(t, f, nil, "NewProxy", map[string]any{
		"content": NewProxyContent{
			User:      NewProxyUser{RunID: "run-new", Metas: map[string]string{"runtime_token": f.runtimeTok}},
			ProxyName: f.tunnelID,
			ProxyType: "tcp",
			Subdomain: f.tunnelID,
		},
	})
	if !resp.Reject {
		t.Error("expected reject for non-http proxy_type")
	}
}

func TestPluginIntegration_NewProxyRejectsTerminalTunnel(t *testing.T) {
	f := setupPluginFixture(t)
	_, err := f.pool.Exec(context.Background(),
		"UPDATE tunnels SET status='revoked' WHERE id=$1", f.tunnelID)
	require.NoError(t, err)

	resp := callPluginOp(t, f, nil, "NewProxy", map[string]any{
		"content": NewProxyContent{
			User:      NewProxyUser{RunID: "run-new", Metas: map[string]string{"runtime_token": f.runtimeTok}},
			ProxyName: f.tunnelID,
			ProxyType: "http",
			Subdomain: f.tunnelID,
		},
	})
	if !resp.Reject {
		t.Error("NewProxy on revoked tunnel should reject")
	}
}

func TestPluginIntegration_CloseProxyTransitionsActiveToClosed(t *testing.T) {
	f := setupPluginFixture(t)
	_, err := f.pool.Exec(context.Background(),
		"UPDATE tunnels SET status='active' WHERE id=$1", f.tunnelID)
	require.NoError(t, err)

	called := false
	transitionFn := func(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error {
		called = true
		if event != "CloseProxy" {
			t.Errorf("event = %s", event)
		}
		return nil
	}

	resp := callPluginOp(t, f, transitionFn, "CloseProxy", map[string]any{
		"content": CloseProxyContent{User: NewProxyUser{RunID: "run-active"}, ProxyName: f.tunnelID},
	})
	if resp.Reject {
		t.Errorf("CloseProxy should not reject, got: %s", resp.RejectReason)
	}
	if !called {
		t.Error("transition function should have been called for active tunnel")
	}
}

func TestPluginIntegration_CloseProxyNoOpForNonActive(t *testing.T) {
	f := setupPluginFixture(t)
	// Tunnel still in 'reserved' — CloseProxy should be a no-op.
	called := false
	transitionFn := func(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error {
		called = true
		return nil
	}

	resp := callPluginOp(t, f, transitionFn, "CloseProxy", map[string]any{
		"content": CloseProxyContent{ProxyName: f.tunnelID},
	})
	if resp.Reject {
		t.Errorf("CloseProxy on reserved should not reject, got: %s", resp.RejectReason)
	}
	if called {
		t.Error("transition should not run for non-active tunnel")
	}
}

func TestPluginIntegration_CloseProxyUnknownTunnel(t *testing.T) {
	f := setupPluginFixture(t)
	resp := callPluginOp(t, f, nil, "CloseProxy", map[string]any{
		"content": CloseProxyContent{ProxyName: "t-doesnotexist00"},
	})
	if resp.Reject {
		t.Error("CloseProxy for unknown tunnel should silently allow (frps shouldn't be blocked)")
	}
}

func TestPluginIntegration_NewUserConnEmitsEventWhenEnabled(t *testing.T) {
	f := setupPluginFixture(t)
	f.cfg.LogUserConns = true
	_, err := f.pool.Exec(context.Background(), "UPDATE tunnels SET status = 'active' WHERE id = $1", f.tunnelID)
	require.NoError(t, err)

	resp := callPluginOp(t, f, nil, "NewUserConn", map[string]any{
		"content": NewUserConnContent{
			ProxyName:  f.tunnelID,
			ProxyType:  "http",
			RemoteAddr: "1.2.3.4:5678",
		},
	})
	if resp.Reject {
		t.Errorf("NewUserConn should not reject: %s", resp.RejectReason)
	}

	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM tunnel_events WHERE tunnel_id=$1 AND event_type='NewUserConn'", f.tunnelID).Scan(&count))
	if count != 1 {
		t.Errorf("expected 1 NewUserConn event, got %d", count)
	}
}

func TestTunnelAuthorizationIntegration(t *testing.T) {
	f := setupPluginFixture(t)
	ctx := context.Background()
	host := f.tunnelID + "." + f.cfg.TunnelDomain

	_, err := f.pool.Exec(ctx, "UPDATE tunnels SET status = 'active', expires_at = now() + interval '1 hour' WHERE id = $1", f.tunnelID)
	require.NoError(t, err)
	if got := callTunnelAuthorization(t, f, f.cfg.PluginSecret, host).Code; got != http.StatusNoContent {
		t.Fatalf("active tunnel status = %d, want %d", got, http.StatusNoContent)
	}
	_, err = f.pool.Exec(ctx, "UPDATE tunnels SET status = 'closed' WHERE id = $1", f.tunnelID)
	require.NoError(t, err)
	if got := callTunnelAuthorization(t, f, f.cfg.PluginSecret, host).Code; got != http.StatusNoContent {
		t.Fatalf("closed tunnel status = %d, want %d", got, http.StatusNoContent)
	}

	tests := []struct {
		name   string
		update string
	}{
		{name: "reserved", update: "UPDATE tunnels SET status = 'reserved', expires_at = now() + interval '1 hour' WHERE id = $1"},
		{name: "revoked", update: "UPDATE tunnels SET status = 'revoked', expires_at = now() + interval '1 hour' WHERE id = $1"},
		{name: "expired status", update: "UPDATE tunnels SET status = 'expired', expires_at = now() + interval '1 hour' WHERE id = $1"},
		{name: "elapsed TTL", update: "UPDATE tunnels SET status = 'active', expires_at = now() - interval '1 second' WHERE id = $1"},
		{name: "missing TTL", update: "UPDATE tunnels SET status = 'active', expires_at = NULL WHERE id = $1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.pool.Exec(ctx, tt.update, f.tunnelID)
			require.NoError(t, err)
			if got := callTunnelAuthorization(t, f, f.cfg.PluginSecret, host).Code; got != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", got, http.StatusForbidden)
			}
		})
	}

	if got := callTunnelAuthorization(t, f, f.cfg.PluginSecret, "t-unknown00000000."+f.cfg.TunnelDomain).Code; got != http.StatusForbidden {
		t.Fatalf("unknown tunnel status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestPluginIntegration_NewUserConnRejectsTerminalOrExpiredTunnel(t *testing.T) {
	tests := []struct {
		name   string
		update string
	}{
		{name: "revoked", update: "UPDATE tunnels SET status = 'revoked' WHERE id = $1"},
		{name: "expired status", update: "UPDATE tunnels SET status = 'expired' WHERE id = $1"},
		{name: "ttl elapsed before reaper", update: "UPDATE tunnels SET status = 'active', expires_at = now() - interval '1 second' WHERE id = $1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := setupPluginFixture(t)
			_, err := f.pool.Exec(context.Background(),
				"UPDATE tunnels SET status = 'active', expires_at = now() + interval '1 hour' WHERE id = $1",
				f.tunnelID,
			)
			require.NoError(t, err)
			_, err = f.pool.Exec(context.Background(), tt.update, f.tunnelID)
			require.NoError(t, err)

			resp := callPluginOp(t, f, nil, "NewUserConn", map[string]any{
				"content": NewUserConnContent{
					ProxyName:  f.tunnelID,
					ProxyType:  "http",
					RemoteAddr: "1.2.3.4:5678",
				},
			})
			if !resp.Reject {
				t.Fatal("NewUserConn should reject a terminal or elapsed tunnel")
			}
		})
	}
}

func TestPluginIntegration_LookupRuntimeTokenDirect(t *testing.T) {
	// Drive lookupRuntimeToken directly to cover edge branches.
	f := setupPluginFixture(t)
	ctx := context.Background()

	info, err := lookupRuntimeToken(ctx, f.pool, f.runtimeTok)
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	if info.tunnelID != f.tunnelID {
		t.Errorf("tunnel_id = %q", info.tunnelID)
	}

	// Garbage token (too short for prefix).
	if _, err := lookupRuntimeToken(ctx, f.pool, "rt_x"); err == nil {
		t.Error("short token should error")
	}

	// Well-formed token whose prefix doesn't exist in the DB.
	if _, err := lookupRuntimeToken(ctx, f.pool, "rt_aaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Error("unknown prefix should error")
	}
}
