package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseTTL(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"15m", 15 * time.Minute},
		{"30m", 30 * time.Minute},
		{"1h", time.Hour},
		{"24h", 24 * time.Hour},
		{"2h30m", 2*time.Hour + 30*time.Minute},
	}
	for _, tt := range tests {
		got, err := ParseTTL(tt.input)
		if err != nil {
			t.Errorf("ParseTTL(%q) error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseTTL(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseTTLErrors(t *testing.T) {
	_, err := ParseTTL("")
	if err == nil {
		t.Error("expected error for empty TTL")
	}
	_, err = ParseTTL("abc")
	if err == nil {
		t.Error("expected error for invalid TTL")
	}
	if _, err := ParseTTL("0s"); err == nil {
		t.Error("expected error for zero TTL")
	}
	if _, err := ParseTTL("-5m"); err == nil {
		t.Error("expected error for negative TTL")
	}
}

func TestClientCreateTunnel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tunnels" {
			t.Errorf("expected /v1/tunnels, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk_live_test" {
			t.Errorf("expected Bearer token, got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Idempotency-Key") != "idem-key-1" {
			t.Errorf("expected Idempotency-Key, got %q", r.Header.Get("Idempotency-Key"))
		}

		resp := TunnelResponse{
			TunnelID:     "t-abc123",
			Status:       "reserved",
			Type:         "http",
			PublicURL:    "https://t-abc123.tunnel.example.com",
			RuntimeToken: "rt_testtoken",
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
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	tunnel, err := client.CreateTunnel(&CreateTunnelRequest{
		Type:       "http",
		LocalHost:  "127.0.0.1",
		LocalPort:  3000,
		TTLSeconds: 3600,
	}, "idem-key-1")
	if err != nil {
		t.Fatalf("CreateTunnel: %v", err)
	}
	if tunnel.TunnelID != "t-abc123" {
		t.Errorf("tunnel_id = %q", tunnel.TunnelID)
	}
	if tunnel.PublicURL != "https://t-abc123.tunnel.example.com" {
		t.Errorf("public_url = %q", tunnel.PublicURL)
	}
	if tunnel.FRP == nil || tunnel.FRP.ServerAddr != "frps.example.com" {
		t.Error("missing or wrong FRP config")
	}
}

func TestClientListTunnels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/tunnels" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(ListTunnelsResponse{
			Tunnels: []TunnelResponse{
				{TunnelID: "t-1", Status: "active", Type: "http", PublicURL: "https://t-1.example.com"},
			},
			NextCursor: nil,
		})
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	list, err := client.ListTunnels(0)
	if err != nil {
		t.Fatalf("ListTunnels: %v", err)
	}
	if len(list.Tunnels) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(list.Tunnels))
	}
	if list.Tunnels[0].TunnelID != "t-1" {
		t.Errorf("tunnel_id = %q", list.Tunnels[0].TunnelID)
	}
}

func TestClientGetTunnel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tunnels/t-abc" {
			t.Errorf("expected /v1/tunnels/t-abc, got %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(TunnelResponse{TunnelID: "t-abc", Status: "active"})
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	tunnel, err := client.GetTunnel("t-abc")
	if err != nil {
		t.Fatalf("GetTunnel: %v", err)
	}
	if tunnel.TunnelID != "t-abc" {
		t.Errorf("tunnel_id = %q", tunnel.TunnelID)
	}
}

func TestClientDeleteTunnel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/v1/tunnels/t-abc" {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	if err := client.DeleteTunnel("t-abc"); err != nil {
		t.Fatalf("DeleteTunnel: %v", err)
	}
}

func TestClientWhoami(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" {
			t.Errorf("expected /v1/me, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"user_id":"user-123"}`))
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	userID, err := client.Whoami()
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if userID != "user-123" {
		t.Errorf("user_id = %q", userID)
	}
}

func TestClientErrorParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"QUOTA_EXCEEDED","message":"maximum 5 concurrent tunnels"}}`))
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	_, err := client.CreateTunnel(&CreateTunnelRequest{Type: "http", LocalPort: 3000}, "")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "QUOTA_EXCEEDED: maximum 5 concurrent tunnels" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestClientUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"not authenticated"}}`))
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "bad-token"})
	_, err := client.ListTunnels(0)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckLocalPort_NotListening(t *testing.T) {
	err := CheckLocalPort(1) // Port 1 unlikely to be listening
	if err == nil {
		t.Error("expected error for port 1")
	}
}

func TestCheckLocalPort_Listening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("can't listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port //nolint:errcheck
	if err := CheckLocalPort(port); err != nil {
		t.Errorf("CheckLocalPort(%d) = %v", port, err)
	}
}

func TestClientCreateTunnel_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal error`))
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	_, err := client.CreateTunnel(&CreateTunnelRequest{Type: "http", LocalPort: 3000}, "")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	// Should fall back to "HTTP 500: ..." format
	if err.Error() == "" {
		t.Error("error should not be empty")
	}
}

func TestClientGetTunnel_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"tunnel not found"}}`))
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	_, err := client.GetTunnel("t-nonexistent")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if err.Error() != "NOT_FOUND: tunnel not found" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestClientDeleteTunnel_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"tunnel not found"}}`))
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "sk_live_test"})
	err := client.DeleteTunnel("t-nonexistent")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestClientWhoami_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"not authenticated"}}`))
	}))
	defer server.Close()

	client := NewClient(&Credentials{Server: server.URL, Token: "bad"})
	_, err := client.Whoami()
	if err == nil {
		t.Fatal("expected error")
	}
}
