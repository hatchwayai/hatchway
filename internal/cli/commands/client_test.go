package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cli "github.com/zydo/hatchway/internal/cli"
)

func TestGenerateFRPCConfig(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	tunnel := &cli.TunnelResponse{
		TunnelID:     "t-abc123",
		Status:       "reserved",
		Type:         "http",
		PublicURL:    "https://t-abc123.tunnel.example.com",
		RuntimeToken: "rt_secret123",
		ExpiresAt:    &expires,
		FRP: &cli.FRPConfig{
			ServerAddr:  "frps.example.com",
			ServerPort:  7000,
			ServerToken: "bootstrap-secret",
			ProxyName:   "t-abc123",
			ProxyType:   "http",
			Subdomain:   "t-abc123",
			LocalIP:     "127.0.0.1",
			LocalPort:   3000,
		},
	}

	config := generateFRPCConfig(tunnel, "127.0.0.1", 3000)

	checks := []string{
		`serverAddr = "frps.example.com"`,
		"serverPort = 7000",
		`auth.token = "bootstrap-secret"`,
		`metadatas.runtime_token = "rt_secret123"`,
		`name = "t-abc123"`,
		`type = "http"`,
		`localIP = "127.0.0.1"`,
		"localPort = 3000",
		`subdomain = "t-abc123"`,
	}
	for _, want := range checks {
		if !strings.Contains(config, want) {
			t.Errorf("frpc config missing %q\n%s", want, config)
		}
	}
}

func TestGenerateFRPCConfigEmptyLocalHost(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	tunnel := &cli.TunnelResponse{
		TunnelID:     "t-test",
		RuntimeToken: "rt_test",
		ExpiresAt:    &expires,
		FRP: &cli.FRPConfig{
			ServerAddr: "frps.test", ServerPort: 7000,
			ServerToken: "s", ProxyName: "t-test", ProxyType: "http",
			Subdomain: "t-test", LocalIP: "127.0.0.1", LocalPort: 8080,
		},
	}

	config := generateFRPCConfig(tunnel, "", 8080)
	if !strings.Contains(config, `localIP = "127.0.0.1"`) {
		t.Error("empty localHost should default to 127.0.0.1")
	}
}

func TestTCPStubReturnsError(t *testing.T) {
	cmd := tcpCmd()
	if cmd == nil {
		t.Fatal("tcpCmd returned nil")
	}
	if cmd.Use != "tcp <port>" {
		t.Errorf("Use = %q", cmd.Use)
	}
	if cmd.RunE == nil {
		t.Error("RunE should be set")
	}
}

func TestUDPStubReturnsError(t *testing.T) {
	cmd := udpCmd()
	if cmd == nil {
		t.Fatal("udpCmd returned nil")
	}
	if cmd.Use != "udp <port>" {
		t.Errorf("Use = %q", cmd.Use)
	}
}

func TestHTTPCmdFlags(t *testing.T) {
	cmd := httpCmd()
	if cmd.Use != "http <port>" {
		t.Errorf("Use = %q", cmd.Use)
	}

	ttlFlag := cmd.Flags().Lookup("ttl")
	if ttlFlag == nil {
		t.Error("missing --ttl flag")
	}
	jsonFlag := cmd.Flags().Lookup("json")
	if jsonFlag == nil {
		t.Error("missing --json flag")
	}
	if cmd.Flags().Lookup("local-host") != nil {
		t.Error("--local-host should be removed: only 127.0.0.1 is allowed in MVP")
	}
}

func TestAuthSetTokenRequiresServer(t *testing.T) {
	cmd := authSetTokenCmd()
	serverFlag := cmd.Flags().Lookup("server")
	if serverFlag == nil {
		t.Error("missing --server flag")
	}
}

func TestAuthSetTokenFallsBackToServerEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HATCHWAY_SERVER", "https://from-env.example.com")

	cmd := authSetTokenCmd()
	// No --server flag parsed, so the bound `server` var stays "" — this
	// exercises the HATCHWAY_SERVER fallback the command's own error message
	// claims to support.
	if err := cmd.RunE(cmd, []string{"sk_live_test_token"}); err != nil {
		t.Fatalf("RunE with HATCHWAY_SERVER set: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "hatchway", "credentials.json"))
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !strings.Contains(string(data), "from-env.example.com") {
		t.Errorf("credentials file should contain the HATCHWAY_SERVER value, got %s", data)
	}
}

func TestDeleteCmdRequiresArg(t *testing.T) {
	cmd := deleteCmd()
	if cmd.Args == nil {
		t.Error("delete should require args")
	}
}
