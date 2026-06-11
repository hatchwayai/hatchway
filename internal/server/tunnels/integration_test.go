package tunnels_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/db"
	"github.com/zydo/hatchway/internal/server/api"
	"github.com/zydo/hatchway/internal/server/tunnels"
	"github.com/zydo/hatchway/internal/tokens"
)

// Integration tests for the API surface that previously had no end-to-end
// coverage: cursor pagination, idempotency replay, AdminOnly routing, and
// the non-admin 403 path. Skipped under `go test -short`.

type fixture struct {
	pool        *pgxpool.Pool
	cfg         *config.Config
	router      http.Handler
	adminToken  string
	userToken   string
	userID      string
	adminUserID string
}

func setupFixture(t *testing.T) *fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		ctx := context.Background()
		c, err := postgres.Run(ctx,
			"postgres:16",
			postgres.WithDatabase("hatchway_test"),
			postgres.WithUsername("hatchway"),
			postgres.WithPassword("hatchway_test"),
			testcontainers.WithWaitStrategy(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections"),
			),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Terminate(ctx) })

		connStr, err = c.ConnectionString(ctx, "sslmode=disable")
		require.NoError(t, err)
	}

	require.NoError(t, db.RunMigrations(connStr))

	ctx := context.Background()
	database, err := db.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(database.Close)

	// Reset rows from prior runs so test order doesn't matter.
	for _, table := range []string{"idempotency_keys", "tunnel_runtime_tokens", "tunnel_events", "tunnels", "api_tokens", "users"} {
		_, err := database.Pool.Exec(ctx, "DELETE FROM "+table)
		require.NoError(t, err)
	}

	cfg := &config.Config{
		TunnelDomain:     "tunnel.test.local",
		MaxConcurrent:    100,
		MaxTTL:           24 * time.Hour,
		PluginSecret:     "test-plugin-secret",
		RateCreatePerMin: 1000,
	}

	adminUserID := uuid.New().String()
	userID := uuid.New().String()
	_, err = database.Pool.Exec(ctx,
		"INSERT INTO users (id, email, name, is_admin) VALUES ($1, 'admin@test', 'admin', true), ($2, 'user@test', 'user', false)",
		adminUserID, userID,
	)
	require.NoError(t, err)

	adminTok := mustMintToken(t, database.Pool, adminUserID, "admin")
	userTok := mustMintToken(t, database.Pool, userID, "user")

	router := api.NewRouter(database.Pool, cfg, func(r chi.Router) {
		tunnels.RegisterRoutes(r, database.Pool, cfg)
	})

	return &fixture{
		pool:        database.Pool,
		cfg:         cfg,
		router:      router,
		adminToken:  adminTok,
		userToken:   userTok,
		userID:      userID,
		adminUserID: adminUserID,
	}
}

func mustMintToken(t *testing.T, pool *pgxpool.Pool, userID, label string) string {
	t.Helper()
	tok, err := tokens.MintAPIToken()
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		"INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash) VALUES ($1, $2, $3, $4, $5)",
		uuid.New().String(), userID, label, tok.Prefix, tok.Hash,
	)
	require.NoError(t, err)
	return tok.Raw
}

func (f *fixture) do(t *testing.T, method, path, token, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// --- Tests ---

func TestIntegration_AdminRevokeRequiresAdminFlag(t *testing.T) {
	f := setupFixture(t)

	// Admin creates a tunnel for the non-admin user via direct insert.
	tunnelID := "t-adminrevoketest1"
	_, err := f.pool.Exec(context.Background(),
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, f.userID,
	)
	require.NoError(t, err)

	// Non-admin token must be rejected.
	w := f.do(t, "POST", "/v1/admin/tunnels/"+tunnelID+"/revoke", f.userToken, "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin should get 403, got %d body=%s", w.Code, w.Body.String())
	}

	// Tunnel must still be reserved.
	var status string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT status FROM tunnels WHERE id = $1", tunnelID).Scan(&status))
	if status != "reserved" {
		t.Fatalf("tunnel status changed despite 403: %s", status)
	}

	// Admin token succeeds.
	w = f.do(t, "POST", "/v1/admin/tunnels/"+tunnelID+"/revoke", f.adminToken, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("admin revoke should be 204, got %d body=%s", w.Code, w.Body.String())
	}

	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT status FROM tunnels WHERE id = $1", tunnelID).Scan(&status))
	if status != "revoked" {
		t.Fatalf("expected revoked, got %s", status)
	}
}

func TestIntegration_MeReturnsIsAdmin(t *testing.T) {
	f := setupFixture(t)

	cases := []struct {
		token string
		want  bool
	}{
		{f.adminToken, true},
		{f.userToken, false},
	}
	for _, c := range cases {
		w := f.do(t, "GET", "/v1/me", c.token, "", "")
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp struct {
			UserID  string `json:"user_id"`
			IsAdmin bool   `json:"is_admin"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		if resp.IsAdmin != c.want {
			t.Errorf("is_admin = %v, want %v", resp.IsAdmin, c.want)
		}
	}
}

func TestIntegration_DeleteIsIdempotent(t *testing.T) {
	f := setupFixture(t)

	tunnelID := "t-deletetwice0001"
	_, err := f.pool.Exec(context.Background(),
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, f.userID,
	)
	require.NoError(t, err)

	w := f.do(t, "DELETE", "/v1/tunnels/"+tunnelID, f.userToken, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("first delete: %d body=%s", w.Code, w.Body.String())
	}
	// Second DELETE on the same (now revoked) tunnel must also be 204, not 400.
	w = f.do(t, "DELETE", "/v1/tunnels/"+tunnelID, f.userToken, "", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("repeat delete should be idempotent, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestIntegration_CreateTunnelReturnsFRPSAuthToken(t *testing.T) {
	f := setupFixture(t)
	// Distinct from the plugin secret — verify the API response uses the auth
	// token, not the plugin secret, so the plugin secret never escapes the
	// server boundary.
	f.cfg.FRPSAuthToken = "frps-bootstrap"
	f.cfg.PluginSecret = "plugin-internal"

	body := `{"type":"http","local_port":3000,"ttl_seconds":300}`
	w := f.do(t, "POST", "/v1/tunnels", f.userToken, body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		FRP struct {
			ServerToken string `json:"server_token"`
		} `json:"frp"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	if resp.FRP.ServerToken != "frps-bootstrap" {
		t.Errorf("server_token should be FRPSAuthToken, got %q", resp.FRP.ServerToken)
	}
	if resp.FRP.ServerToken == "plugin-internal" {
		t.Error("plugin secret leaked via server_token")
	}
}

func TestIntegration_IdempotencyReplay(t *testing.T) {
	f := setupFixture(t)

	body := `{"type":"http","local_port":3000,"ttl_seconds":300}`
	key := uuid.New().String()

	w1 := f.do(t, "POST", "/v1/tunnels", f.userToken, body, key)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create: %d body=%s", w1.Code, w1.Body.String())
	}
	w2 := f.do(t, "POST", "/v1/tunnels", f.userToken, body, key)
	if w2.Code != http.StatusCreated {
		t.Fatalf("replay: %d body=%s", w2.Code, w2.Body.String())
	}
	if w1.Body.String() != w2.Body.String() {
		t.Fatalf("replay returned different body:\n  first:  %s\n  second: %s", w1.Body.String(), w2.Body.String())
	}

	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM tunnels WHERE user_id = $1", f.userID).Scan(&count))
	if count != 1 {
		t.Fatalf("idempotency replay created %d tunnels, want 1", count)
	}

	// Different body, same key → 409.
	other := `{"type":"http","local_port":3001,"ttl_seconds":300}`
	w3 := f.do(t, "POST", "/v1/tunnels", f.userToken, other, key)
	if w3.Code != http.StatusConflict {
		t.Fatalf("different body, same key: expected 409, got %d", w3.Code)
	}
}

func TestIntegration_DeleteNotFoundFor404(t *testing.T) {
	f := setupFixture(t)
	w := f.do(t, "DELETE", "/v1/tunnels/t-doesnotexist00", f.userToken, "", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestIntegration_DeleteOtherUsersTunnelIs404(t *testing.T) {
	f := setupFixture(t)
	tunnelID := "t-otheruser00001a"
	_, err := f.pool.Exec(context.Background(),
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, f.adminUserID, // owned by admin, not f.userID
	)
	require.NoError(t, err)

	w := f.do(t, "DELETE", "/v1/tunnels/"+tunnelID, f.userToken, "", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-user delete should be 404 (no existence leak), got %d", w.Code)
	}
}

func TestIntegration_GetTunnelHappyAndNotFound(t *testing.T) {
	f := setupFixture(t)
	tunnelID := "t-getsomethinghr"
	_, err := f.pool.Exec(context.Background(),
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour')",
		tunnelID, f.userID,
	)
	require.NoError(t, err)

	w := f.do(t, "GET", "/v1/tunnels/"+tunnelID, f.userToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("happy GET: %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	if resp["tunnel_id"] != tunnelID {
		t.Errorf("body tunnel_id = %v", resp["tunnel_id"])
	}

	w = f.do(t, "GET", "/v1/tunnels/t-nothere00000000", f.userToken, "", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("missing tunnel GET: %d", w.Code)
	}
}

func TestIntegration_CreateRespectsQuota(t *testing.T) {
	f := setupFixture(t)
	prev := f.cfg.MaxConcurrent
	f.cfg.MaxConcurrent = 1
	t.Cleanup(func() { f.cfg.MaxConcurrent = prev })

	body := `{"type":"http","local_port":3000,"ttl_seconds":300}`
	w1 := f.do(t, "POST", "/v1/tunnels", f.userToken, body, "")
	if w1.Code != http.StatusCreated {
		t.Fatalf("first should be 201, got %d body=%s", w1.Code, w1.Body.String())
	}
	w2 := f.do(t, "POST", "/v1/tunnels", f.userToken, body, "")
	if w2.Code != http.StatusForbidden {
		t.Fatalf("over-quota should be 403, got %d body=%s", w2.Code, w2.Body.String())
	}
	var resp struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
	if resp.Error.Code != "QUOTA_EXCEEDED" {
		t.Errorf("error code = %s", resp.Error.Code)
	}
}

func TestIntegration_CreateRejectsTTLOverMax(t *testing.T) {
	f := setupFixture(t)
	body := `{"type":"http","local_port":3000,"ttl_seconds":999999999}`
	w := f.do(t, "POST", "/v1/tunnels", f.userToken, body, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestIntegration_AdminRevokeNotFound(t *testing.T) {
	f := setupFixture(t)
	w := f.do(t, "POST", "/v1/admin/tunnels/t-doesnotexist00/revoke", f.adminToken, "", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestIntegration_AdminRevokeOnAlreadyRevokedIsIdempotent(t *testing.T) {
	f := setupFixture(t)
	tunnelID := "t-admindouble001"
	_, err := f.pool.Exec(context.Background(),
		"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'revoked', now() + interval '1 hour')",
		tunnelID, f.userID,
	)
	require.NoError(t, err)

	w := f.do(t, "POST", "/v1/admin/tunnels/"+tunnelID+"/revoke", f.adminToken, "", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("admin revoke on revoked should be 204, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestIntegration_IdempotencyOversizeReplay(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	// Pre-seed an idempotency row whose body cache is NULL (simulates the
	// "response too large to cache" code path). Replay must surface the
	// retry-without-key error, not return a corrupt body.
	var tokenID string
	require.NoError(t, f.pool.QueryRow(ctx,
		"SELECT id FROM api_tokens WHERE user_id = $1 LIMIT 1", f.userID,
	).Scan(&tokenID))
	bodyJSON := `{"type":"http","local_port":3000,"ttl_seconds":300}`
	// SHA-256 of the body; must match what the middleware computes.
	hash := "f3a47e64eb33d57bf94a3f7d9d6b6f0d3cf9c0a32a8bba6bd17e8b6bb1f30ea1"
	_, err := f.pool.Exec(ctx,
		`INSERT INTO idempotency_keys (token_id, key, request_hash, response_status, response_body, completed_at)
		 VALUES ($1, 'oversize-key', $2, 201, NULL, now())`,
		tokenID, hash,
	)
	require.NoError(t, err)
	// Force the hash to actually match by overwriting with the real digest.
	_, err = f.pool.Exec(ctx,
		`UPDATE idempotency_keys SET request_hash = encode(digest($1::bytea, 'sha256'), 'hex')
		   WHERE token_id = $2 AND key = 'oversize-key'`,
		[]byte(bodyJSON), tokenID,
	)
	if err != nil {
		// pgcrypto extension might not be enabled — fall back to letting the
		// idempotency middleware notice mismatch (409). Either branch covers
		// the relevant path; just record which one we exercised.
		t.Logf("digest function unavailable; will exercise body-mismatch path instead")
	}

	// Need pgcrypto for digest(); enable if available.
	_, _ = f.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto")
	_, err = f.pool.Exec(ctx,
		`UPDATE idempotency_keys SET request_hash = encode(digest($1::bytea, 'sha256'), 'hex')
		   WHERE token_id = $2 AND key = 'oversize-key'`,
		[]byte(bodyJSON), tokenID,
	)
	if err != nil {
		t.Skipf("pgcrypto unavailable in test image: %v", err)
	}

	w := f.do(t, "POST", "/v1/tunnels", f.userToken, bodyJSON, "oversize-key")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (oversize replay), got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "too large to cache") {
		t.Errorf("expected oversize replay error, got %s", w.Body.String())
	}
}

func TestIntegration_IdempotencyInFlightReturnsConflict(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	var tokenID string
	require.NoError(t, f.pool.QueryRow(ctx,
		"SELECT id FROM api_tokens WHERE user_id = $1 LIMIT 1", f.userID,
	).Scan(&tokenID))

	bodyJSON := `{"type":"http","local_port":3000,"ttl_seconds":300}`
	_, _ = f.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto")
	// Seed an unfinished reservation: same body hash, completed_at NULL.
	_, err := f.pool.Exec(ctx,
		`INSERT INTO idempotency_keys (token_id, key, request_hash)
		 VALUES ($1, 'inflight-key', encode(digest($2::bytea, 'sha256'), 'hex'))`,
		tokenID, []byte(bodyJSON),
	)
	if err != nil {
		t.Skipf("pgcrypto unavailable: %v", err)
	}

	w := f.do(t, "POST", "/v1/tunnels", f.userToken, bodyJSON, "inflight-key")
	if w.Code != http.StatusConflict {
		t.Fatalf("in-flight replay should be 409, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "in flight") {
		t.Errorf("expected in-flight error, got %s", w.Body.String())
	}
}

func TestIntegration_BodySizeCap(t *testing.T) {
	f := setupFixture(t)
	prev := f.cfg.MaxRequestBytes
	f.cfg.MaxRequestBytes = 50
	t.Cleanup(func() { f.cfg.MaxRequestBytes = prev })

	// Rebuild router so the smaller cap takes effect.
	f.router = api.NewRouter(f.pool, f.cfg, func(r chi.Router) {
		tunnels.RegisterRoutes(r, f.pool, f.cfg)
	})

	huge := `{"type":"http","local_port":3000,"ttl_seconds":300,"junk":"` + strings.Repeat("x", 200) + `"}`
	w := f.do(t, "POST", "/v1/tunnels", f.userToken, huge, "")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize body should return 413, got %d", w.Code)
	}
}

func TestIntegration_ListTunnelsCursorPagination(t *testing.T) {
	f := setupFixture(t)

	// Insert 5 tunnels with deterministic created_at so pagination is predictable.
	base := time.Now().UTC().Truncate(time.Second)
	ids := []string{"t-page0000000000a", "t-page0000000000b", "t-page0000000000c", "t-page0000000000d", "t-page0000000000e"}
	for i, id := range ids {
		_, err := f.pool.Exec(context.Background(),
			"INSERT INTO tunnels (id, user_id, type, local_host, local_port, status, expires_at, created_at) VALUES ($1, $2, 'http', '127.0.0.1', 3000, 'reserved', now() + interval '1 hour', $3)",
			id, f.userID, base.Add(time.Duration(i)*time.Second),
		)
		require.NoError(t, err)
	}

	// Page 1: limit=2 returns 2 newest (e, d).
	w := f.do(t, "GET", "/v1/tunnels?limit=2", f.userToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("page 1: %d", w.Code)
	}
	type listResp struct {
		Tunnels    []map[string]any `json:"tunnels"`
		NextCursor any              `json:"next_cursor"`
	}
	var page listResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	if len(page.Tunnels) != 2 {
		t.Fatalf("page 1 size: got %d, want 2", len(page.Tunnels))
	}
	if page.Tunnels[0]["tunnel_id"] != "t-page0000000000e" || page.Tunnels[1]["tunnel_id"] != "t-page0000000000d" {
		t.Fatalf("page 1 ordering wrong: %+v", page.Tunnels)
	}
	cursor1, _ := page.NextCursor.(string)
	if cursor1 == "" {
		t.Fatal("page 1 should have next_cursor")
	}

	// Page 2: limit=2 with cursor returns (c, b).
	w = f.do(t, "GET", "/v1/tunnels?limit=2&cursor="+cursor1, f.userToken, "", "")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	if len(page.Tunnels) != 2 || page.Tunnels[0]["tunnel_id"] != "t-page0000000000c" || page.Tunnels[1]["tunnel_id"] != "t-page0000000000b" {
		t.Fatalf("page 2 contents wrong: %+v", page.Tunnels)
	}
	cursor2, _ := page.NextCursor.(string)

	// Page 3: limit=2 returns the single remaining (a) and no further cursor.
	w = f.do(t, "GET", "/v1/tunnels?limit=2&cursor="+cursor2, f.userToken, "", "")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	if len(page.Tunnels) != 1 || page.Tunnels[0]["tunnel_id"] != "t-page0000000000a" {
		t.Fatalf("page 3 contents wrong: %+v", page.Tunnels)
	}
	if page.NextCursor != nil {
		t.Fatalf("page 3 should not have next_cursor, got %v", page.NextCursor)
	}

	// Bad cursor → 400.
	w = f.do(t, "GET", "/v1/tunnels?cursor=not-base64!", f.userToken, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: expected 400, got %d", w.Code)
	}
}
