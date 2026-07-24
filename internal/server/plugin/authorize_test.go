package plugin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zydo/hatchway/internal/config"
)

func authorizationMux(cfg *config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/tunnels/authorize/{secret}", TunnelAuthorizationHandler(nil, cfg))
	return mux
}

func TestTunnelIDFromHost(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		domain string
		wantID string
		ok     bool
	}{
		{name: "hostname", host: "t-abc123.tunnel.example.com", domain: "tunnel.example.com", wantID: "t-abc123", ok: true},
		{name: "case and port", host: "T-AbC123.TUNNEL.EXAMPLE.COM:443", domain: "tunnel.example.com", wantID: "t-abc123", ok: true},
		{name: "absolute DNS names", host: "t-abc123.tunnel.example.com.", domain: "tunnel.example.com.", wantID: "t-abc123", ok: true},
		{name: "wrong domain", host: "t-abc123.attacker.example", domain: "tunnel.example.com"},
		{name: "domain itself", host: "tunnel.example.com", domain: "tunnel.example.com"},
		{name: "nested subdomain", host: "extra.t-abc123.tunnel.example.com", domain: "tunnel.example.com"},
		{name: "missing prefix", host: "abc123.tunnel.example.com", domain: "tunnel.example.com"},
		{name: "invalid label", host: "t-abc_123.tunnel.example.com", domain: "tunnel.example.com"},
		{name: "malformed port", host: "t-abc123.tunnel.example.com:not-a-port", domain: "tunnel.example.com"},
		{name: "whitespace", host: " t-abc123.tunnel.example.com", domain: "tunnel.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tunnelIDFromHost(tt.host, tt.domain)
			if ok != tt.ok || got != tt.wantID {
				t.Fatalf("tunnelIDFromHost(%q, %q) = (%q, %v), want (%q, %v)", tt.host, tt.domain, got, ok, tt.wantID, tt.ok)
			}
		})
	}
}

func TestTunnelAuthorizationRejectsUntrustedRequestsBeforeLookup(t *testing.T) {
	cfg := &config.Config{
		PluginSecret: "internal-secret",
		TunnelDomain: "tunnel.example.com",
	}
	tests := []struct {
		name   string
		method string
		path   string
		host   string
		status int
	}{
		{name: "wrong method", method: http.MethodPost, path: "/internal/tunnels/authorize/internal-secret", host: "t-abc123.tunnel.example.com", status: http.StatusMethodNotAllowed},
		{name: "wrong secret", method: http.MethodGet, path: "/internal/tunnels/authorize/wrong", host: "t-abc123.tunnel.example.com", status: http.StatusForbidden},
		{name: "missing host", method: http.MethodGet, path: "/internal/tunnels/authorize/internal-secret", status: http.StatusForbidden},
		{name: "wrong domain", method: http.MethodGet, path: "/internal/tunnels/authorize/internal-secret", host: "t-abc123.attacker.example", status: http.StatusForbidden},
		{name: "valid request fails closed without database", method: http.MethodGet, path: "/internal/tunnels/authorize/internal-secret", host: "t-abc123.tunnel.example.com", status: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.host != "" {
				req.Header.Set(TunnelHostHeader, tt.host)
			}
			rec := httptest.NewRecorder()
			authorizationMux(cfg).ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if cacheControl := rec.Header().Get("Cache-Control"); cacheControl != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
			}
		})
	}
}
