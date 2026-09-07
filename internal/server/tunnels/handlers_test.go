package tunnels

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hatchwayai/hatchway/internal/config"
	"github.com/hatchwayai/hatchway/internal/server/api"
)

// --- CreateTunnel request validation tests (nil pool — validation happens before DB calls) ---

func TestCreateTunnel_MissingAuth(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com"}
	handler := CreateTunnel(nil, cfg)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`{"type":"http","local_port":3000}`))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}

func TestCreateTunnel_InvalidJSON(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com"}
	handler := CreateTunnel(nil, cfg)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`not json`))
	r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON, got %d", w.Code)
	}
}

func TestCreateTunnel_BodyTooLarge(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com"}
	handler := CreateTunnel(nil, cfg)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`{"type":"http","local_port":3000}`))
	r.Body = http.MaxBytesReader(w, r.Body, 5)
	r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized body, got %d", w.Code)
	}
}

func TestCreateTunnel_InvalidType(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com"}
	handler := CreateTunnel(nil, cfg)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`{"type":"tcp","local_port":3000}`))
	r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for tcp type, got %d", w.Code)
	}
	var resp api.ErrorResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error.Code != api.ErrInvalidRequest {
		t.Errorf("expected INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestCreateTunnel_InvalidPort(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com"}

	tests := []struct {
		name string
		body string
	}{
		{"zero port", `{"type":"http","local_port":0}`},
		{"negative port", `{"type":"http","local_port":-1}`},
		{"too large", `{"type":"http","local_port":70000}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := CreateTunnel(nil, cfg)
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(tt.body))
			r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))
			handler.ServeHTTP(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", w.Code)
			}
		})
	}
}

func TestCreateTunnel_InvalidLocalHost(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com"}
	handler := CreateTunnel(nil, cfg)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`{"type":"http","local_port":3000,"local_host":"0.0.0.0"}`))
	r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-localhost, got %d", w.Code)
	}
}

func TestCreateTunnel_TTLExceedsMax(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com", MaxTTL: time.Hour}
	handler := CreateTunnel(nil, cfg)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`{"type":"http","local_port":3000,"ttl_seconds":7200}`))
	r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for TTL exceeding max, got %d", w.Code)
	}
}

func TestCreateTunnel_InvalidTTL(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com", MaxTTL: 24 * time.Hour}

	tests := []struct {
		name string
		body string
	}{
		{"negative", `{"type":"http","local_port":3000,"ttl_seconds":-1}`},
		{"duration overflow", `{"type":"http","local_port":3000,"ttl_seconds":9223372037}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := CreateTunnel(nil, cfg)
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(tt.body))
			r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))
			handler.ServeHTTP(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", w.Code)
			}
		})
	}
}

func TestCreateTunnel_LocalhostAccepted(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com", MaxTTL: 86400000000000}
	handler := CreateTunnel(nil, cfg)

	// "localhost" should be accepted — this will fail at the DB layer (nil pool)
	// but should not fail at validation
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`{"type":"http","local_port":3000,"local_host":"localhost"}`))
	r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))

	// nil pool will panic inside handler — recover and check it wasn't a validation error
	defer func() { _ = recover() }()
	handler.ServeHTTP(w, r)
	// If we got here without panic, check it's not a validation error
	if w.Code == http.StatusBadRequest {
		t.Error("localhost should be accepted as local_host")
	}
}

func TestCreateTunnel_DefaultLocalHost(t *testing.T) {
	cfg := &config.Config{TunnelDomain: "tunnel.example.com", MaxTTL: 86400000000000}
	handler := CreateTunnel(nil, cfg)

	// Empty local_host should default to 127.0.0.1
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tunnels", strings.NewReader(`{"type":"http","local_port":3000,"local_host":""}`))
	r = r.WithContext(api.ContextWithAuth(r.Context(), "tok-1", "usr-1"))

	defer func() { _ = recover() }()
	handler.ServeHTTP(w, r)
	if w.Code == http.StatusBadRequest {
		var resp api.ErrorResponse
		_ = json.NewDecoder(w.Body).Decode(&resp)
		t.Errorf("empty local_host should default, got: %s", resp.Error.Message)
	}
}

// --- ListTunnels tests ---
// Note: ListTunnels, GetTunnel, DeleteTunnel, AdminRevokeTunnel all require a DB pool
// for querying. Auth is handled by middleware, not the handlers themselves.
// These handlers get userID from context — with no context value they'll query with empty userID.
// The main testable paths without a DB are the chi routing and param extraction.

// --- TunnelResponse JSON structure ---

// --- TunnelResponse JSON structure ---

func TestTunnelResponse_JSON(t *testing.T) {
	ts := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	resp := TunnelResponse{
		TunnelID:  "t-abc123",
		Status:    "reserved",
		Type:      "http",
		PublicURL: "https://t-abc123.tunnel.example.com",
		ExpiresAt: &ts,
		FRP: &FRPConfig{
			ServerAddr:  "frps.example.com",
			ServerPort:  7000,
			ServerToken: "secret",
			ProxyName:   "t-abc123",
			ProxyType:   "http",
			Subdomain:   "t-abc123",
			LocalIP:     "127.0.0.1",
			LocalPort:   3000,
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var parsed map[string]any
	_ = json.Unmarshal(data, &parsed)

	if parsed["tunnel_id"] != "t-abc123" {
		t.Errorf("tunnel_id mismatch: %v", parsed["tunnel_id"])
	}
	if parsed["status"] != "reserved" {
		t.Errorf("status mismatch: %v", parsed["status"])
	}
	if parsed["public_url"] != "https://t-abc123.tunnel.example.com" {
		t.Errorf("public_url mismatch: %v", parsed["public_url"])
	}

	frp := parsed["frp"].(map[string]any)     //nolint:errcheck
	if frp["server_port"].(float64) != 7000 { //nolint:errcheck
		t.Errorf("server_port mismatch: %v", frp["server_port"])
	}
}

func TestTunnelResponse_OmitsEmptyFRP(t *testing.T) {
	resp := TunnelResponse{
		TunnelID:  "t-abc",
		Status:    "active",
		PublicURL: "https://t-abc.tunnel.example.com",
	}

	data, _ := json.Marshal(resp)
	var parsed map[string]any
	_ = json.Unmarshal(data, &parsed)

	if _, ok := parsed["frp"]; ok {
		t.Error("frp should be omitted when nil")
	}
	if _, ok := parsed["runtime_token"]; ok {
		t.Error("runtime_token should be omitted when empty")
	}
}

// --- CreateTunnelRequest tests ---

func TestCreateTunnelRequest_Parse(t *testing.T) {
	body := `{"type":"http","local_host":"127.0.0.1","local_port":8080,"ttl_seconds":1800}`
	var req CreateTunnelRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.Type != "http" {
		t.Errorf("type: %s", req.Type)
	}
	if req.LocalPort != 8080 {
		t.Errorf("port: %d", req.LocalPort)
	}
	if req.TTLSeconds != 1800 {
		t.Errorf("ttl: %d", req.TTLSeconds)
	}
}

// --- Lifecycle function tests ---

func TestTransition_ValidTransitions(t *testing.T) {
	for from, transitions := range validTransitions {
		for event, to := range transitions {
			if to == "" {
				t.Errorf("empty target state for %s + %s", from, event)
			}
		}
	}
}

func TestTransition_NoTransitionsFromTerminal(t *testing.T) {
	for _, state := range []string{"expired", "revoked"} {
		if _, ok := validTransitions[state]; ok {
			t.Errorf("terminal state %s should have no transitions", state)
		}
	}
}
