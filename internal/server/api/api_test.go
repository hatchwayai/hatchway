package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/zydo/hatchway/internal/tokens"
)

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusNotFound, ErrNotFound, "tunnel not found")

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}

	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != ErrNotFound {
		t.Errorf("expected code %s, got %s", ErrNotFound, resp.Error.Code)
	}
	if resp.Error.Message != "tunnel not found" {
		t.Errorf("expected message 'tunnel not found', got %s", resp.Error.Message)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}

func TestHealthzHandler(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/healthz", nil)
	HealthzHandler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("expected ok, got %s", w.Body.String())
	}
}

func TestReadyzHandler_Healthy(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/readyz", nil)
	ReadyzHandler(func() error { return nil }).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestReadyzHandler_Unhealthy(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/readyz", nil)
	ReadyzHandler(func() error { return errors.New("db down") }).ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestStatusRecorder_DefaultStatusOnWrite(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: w}

	n, err := rec.Write([]byte("ok"))
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != 2 {
		t.Fatalf("short write: got %d", n)
	}
	if rec.status != http.StatusOK {
		t.Errorf("recorder status = %d, want 200", rec.status)
	}
	if w.Code != http.StatusOK {
		t.Errorf("response status = %d, want 200", w.Code)
	}
}

// --- Auth middleware tests ---

type mockLookup struct {
	tokenID string
	userID  string
	hash    string
	isAdmin bool
	err     error
}

func (m *mockLookup) lookup(ctx context.Context, prefix string) ([]TokenCandidate, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.tokenID == "" && m.hash == "" {
		return nil, nil
	}
	return []TokenCandidate{{TokenID: m.tokenID, UserID: m.userID, Hash: m.hash, IsAdmin: m.isAdmin}}, nil
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func TestAuthMiddleware_NoHeader(t *testing.T) {
	handler := AuthMiddleware((&mockLookup{}).lookup)((http.HandlerFunc(okHandler)))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_BadFormat(t *testing.T) {
	handler := AuthMiddleware((&mockLookup{}).lookup)(http.HandlerFunc(okHandler))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Set("Authorization", "Basic abc")
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_RuntimeToken(t *testing.T) {
	handler := AuthMiddleware((&mockLookup{}).lookup)(http.HandlerFunc(okHandler))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer rt_sometoken12345")
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for runtime token, got %d", w.Code)
	}
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	tok, _ := tokens.MintAPIToken()
	mock := &mockLookup{tokenID: "t-1", userID: "u-1", hash: tok.Hash}
	handler := AuthMiddleware(mock.lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TokenIDFromContext(r.Context()) != "t-1" {
			t.Error("token_id not in context")
		}
		if UserIDFromContext(r.Context()) != "u-1" {
			t.Error("user_id not in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok.Raw)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuthMiddleware_WrongToken(t *testing.T) {
	tok, _ := tokens.MintAPIToken()
	mock := &mockLookup{tokenID: "t-1", userID: "u-1", hash: tok.Hash}
	handler := AuthMiddleware(mock.lookup)(http.HandlerFunc(okHandler))

	other, _ := tokens.MintAPIToken()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+other.Raw)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token, got %d", w.Code)
	}
}

func TestAuthMiddleware_TokenNotFound(t *testing.T) {
	// Test the "not found" path via the lookup returning empty values
	mock := &mockLookup{tokenID: "", userID: "", hash: "", err: nil}
	handler := AuthMiddleware(mock.lookup)(http.HandlerFunc(okHandler))

	tok, _ := tokens.MintAPIToken()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok.Raw)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty lookup, got %d", w.Code)
	}
}

func TestAuthMiddleware_DBLookupError(t *testing.T) {
	mock := &mockLookup{err: errors.New("connection refused")}
	handler := AuthMiddleware(mock.lookup)(http.HandlerFunc(okHandler))

	tok, _ := tokens.MintAPIToken()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok.Raw)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for DB error, got %d", w.Code)
	}
}

func TestLookupCandidates_DBError(t *testing.T) {
	_, err := lookupCandidates(context.Background(), func(ctx context.Context, prefix string) ([]TokenCandidate, error) {
		return nil, errors.New("connection refused")
	}, "sk_live_abc")
	if err == nil {
		t.Error("expected error")
	}
}

func TestLookupCandidates_NotFound(t *testing.T) {
	_, err := lookupCandidates(context.Background(), func(ctx context.Context, prefix string) ([]TokenCandidate, error) {
		return nil, pgx.ErrNoRows
	}, "sk_live_abc")
	if err == nil {
		t.Error("expected error for ErrNoRows")
	}
}

func TestLookupCandidates_Empty(t *testing.T) {
	_, err := lookupCandidates(context.Background(), func(ctx context.Context, prefix string) ([]TokenCandidate, error) {
		return nil, nil
	}, "sk_live_abc")
	if err == nil {
		t.Error("expected error for empty candidates")
	}
}

func TestAuthMiddleware_PrefixCollisionPicksMatch(t *testing.T) {
	t1, _ := tokens.MintAPIToken()
	t2, _ := tokens.MintAPIToken()
	// Both candidates share a prefix in DB; only one hash matches the raw token.
	lookup := func(ctx context.Context, prefix string) ([]TokenCandidate, error) {
		return []TokenCandidate{
			{TokenID: "decoy", UserID: "u-decoy", Hash: t1.Hash},
			{TokenID: "real", UserID: "u-real", Hash: t2.Hash},
		}, nil
	}
	handler := AuthMiddleware(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TokenIDFromContext(r.Context()) != "real" {
			t.Errorf("expected token_id 'real', got %q", TokenIDFromContext(r.Context()))
		}
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+t2.Raw)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAdminOnly_RejectsNonAdmin(t *testing.T) {
	called := false
	wrapped := AdminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("POST", "/v1/admin/things", nil)
	r = r.WithContext(ContextWithAdmin(r.Context(), "tok-1", "usr-1", false))
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	if called {
		t.Error("inner handler should not run for non-admin")
	}
}

func TestAdminOnly_AllowsAdmin(t *testing.T) {
	called := false
	wrapped := AdminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("POST", "/v1/admin/things", nil)
	r = r.WithContext(ContextWithAdmin(r.Context(), "tok-1", "usr-1", true))
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !called {
		t.Error("inner handler should run for admin")
	}
}

// --- Rate limiter tests ---

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	rl := NewRateLimiter(5)
	for i := 0; i < 5; i++ {
		if !rl.Allow("key1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
}

func TestRateLimiter_RejectsOverLimit(t *testing.T) {
	rl := NewRateLimiter(2)
	rl.Allow("key1")
	rl.Allow("key1")
	if rl.Allow("key1") {
		t.Error("third request should be rejected")
	}
}

func TestRateLimiter_IndependentKeys(t *testing.T) {
	rl := NewRateLimiter(1)
	if !rl.Allow("key1") {
		t.Error("first request for key1 should be allowed")
	}
	if !rl.Allow("key2") {
		t.Error("first request for key2 should be allowed")
	}
}

func TestRateLimitMiddleware_RateLimitsTunnelCreation(t *testing.T) {
	rl := NewRateLimiter(1)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RateLimitMiddleware(rl)(handler)

	r1 := httptest.NewRequest("POST", "/v1/tunnels", nil)
	r1 = r1.WithContext(ContextWithAuth(r1.Context(), "tok-1", "user-1"))
	w1 := httptest.NewRecorder()
	wrapped.ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Errorf("first create request should succeed, got %d", w1.Code)
	}

	r2 := httptest.NewRequest("POST", "/v1/tunnels", nil)
	r2 = r2.WithContext(ContextWithAuth(r2.Context(), "tok-1", "user-1"))
	w2 := httptest.NewRecorder()
	wrapped.ServeHTTP(w2, r2)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("second create request should be rate limited, got %d", w2.Code)
	}
}

func TestRateLimitMiddleware_SkipsReadRequests(t *testing.T) {
	rl := NewRateLimiter(1)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RateLimitMiddleware(rl)(handler)

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/v1/me", nil)
		r = r.WithContext(ContextWithAuth(r.Context(), "tok-1", "user-1"))
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("read request %d should not be rate limited, got %d", i+1, w.Code)
		}
	}
}

func TestRateLimitMiddleware_NoTokenID(t *testing.T) {
	rl := NewRateLimiter(1)
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RateLimitMiddleware(rl)(handler)

	// Request without token_id in context should bypass rate limiting
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)

	if !called {
		t.Error("handler should be called when no token_id")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- Context tests ---

func TestContextWithAuth(t *testing.T) {
	ctx := context.Background()
	ctx = ContextWithAuth(ctx, "tok-123", "usr-456")

	if TokenIDFromContext(ctx) != "tok-123" {
		t.Error("token_id mismatch")
	}
	if UserIDFromContext(ctx) != "usr-456" {
		t.Error("user_id mismatch")
	}
}

func TestContextEmpty(t *testing.T) {
	ctx := context.Background()
	if TokenIDFromContext(ctx) != "" {
		t.Error("expected empty token_id")
	}
	if UserIDFromContext(ctx) != "" {
		t.Error("expected empty user_id")
	}
}

// --- extractBearerToken tests ---

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		header    string
		wantToken string
		wantErr   bool
	}{
		{"", "", true},
		{"Basic abc", "", true},
		{"Bearer sk_live_abc123", "sk_live_abc123", false},
		{"bearer sk_live_abc123", "sk_live_abc123", false},
	}

	for _, tt := range tests {
		token, err := extractBearerToken(tt.header)
		if tt.wantErr {
			if err == nil {
				t.Errorf("expected error for header %q", tt.header)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for header %q: %v", tt.header, err)
			}
			if token != tt.wantToken {
				t.Errorf("expected token %q, got %q", tt.wantToken, token)
			}
		}
	}
}

// --- Request log middleware test ---

func TestRequestLogMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	wrapped := RequestLogMiddleware(handler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/tunnels", strings.NewReader(`{}`))
	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
}

func TestMetricsHandler_NilPool(t *testing.T) {
	m := NewMetrics()
	m.IncrPluginOp("Login")

	handler := MetricsHandler(nil, m)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "hatchway_plugin_ops_total") {
		t.Error("missing plugin ops metric")
	}
}

func TestMetricsHandler_StatusNoPool(t *testing.T) {
	m := NewMetrics()
	handler := MetricsHandler(nil, m)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	handler.ServeHTTP(w, r)

	body := w.Body.String()
	// Should still have basic metrics without pool stats
	if !strings.Contains(body, "hatchway_tunnel_transitions_total") {
		t.Error("missing transitions metric")
	}
}
