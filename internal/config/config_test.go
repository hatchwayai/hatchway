package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Clear env vars to ensure defaults
	os.Clearenv()

	cfg := Load()
	if cfg.APIAddr != ":9000" {
		t.Errorf("default APIAddr = %q, want :9000", cfg.APIAddr)
	}
	if cfg.FRPSPluginAddr != ":9001" {
		t.Errorf("default FRPSPluginAddr = %q, want :9001", cfg.FRPSPluginAddr)
	}
	if cfg.APIReadTimeout != 30*time.Second {
		t.Errorf("default APIReadTimeout = %v, want 30s", cfg.APIReadTimeout)
	}
	if cfg.APIWriteTimeout != 30*time.Second {
		t.Errorf("default APIWriteTimeout = %v, want 30s", cfg.APIWriteTimeout)
	}
	if cfg.TunnelDomain != "tunnel.example.com" {
		t.Errorf("default TunnelDomain = %q, want tunnel.example.com", cfg.TunnelDomain)
	}
	if cfg.MaxConcurrent != 5 {
		t.Errorf("default MaxConcurrent = %d, want 5", cfg.MaxConcurrent)
	}
	if cfg.MaxTTL != 24*time.Hour {
		t.Errorf("default MaxTTL = %v, want 24h", cfg.MaxTTL)
	}
	if cfg.RateCreatePerMin != 10 {
		t.Errorf("default RateCreatePerMin = %d, want 10", cfg.RateCreatePerMin)
	}
	if cfg.LogUserConns != false {
		t.Error("default LogUserConns should be false")
	}
	if cfg.PluginSecret != "" {
		t.Error("default PluginSecret should be empty")
	}
	if cfg.DatabaseURL != "" {
		t.Error("default DatabaseURL should be empty")
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Clearenv()
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("HATCHWAY_API_ADDR", ":8080")
	os.Setenv("HATCHWAY_FRPS_PLUGIN_ADDR", ":9090")
	os.Setenv("HATCHWAY_PLUGIN_SECRET", "my-secret")
	os.Setenv("HATCHWAY_TUNNEL_DOMAIN", "t.example.com")
	os.Setenv("HATCHWAY_MAX_CONCURRENT_TUNNELS", "10")
	os.Setenv("HATCHWAY_MAX_TTL", "2h")
	os.Setenv("HATCHWAY_RATE_CREATE_PER_MIN", "20")
	os.Setenv("HATCHWAY_LOG_USER_CONNS", "true")
	os.Setenv("HATCHWAY_API_READ_TIMEOUT", "10s")
	os.Setenv("HATCHWAY_API_WRITE_TIMEOUT", "15s")

	cfg := Load()
	if cfg.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.APIAddr != ":8080" {
		t.Errorf("APIAddr = %q", cfg.APIAddr)
	}
	if cfg.FRPSPluginAddr != ":9090" {
		t.Errorf("FRPSPluginAddr = %q", cfg.FRPSPluginAddr)
	}
	if cfg.PluginSecret != "my-secret" {
		t.Errorf("PluginSecret = %q", cfg.PluginSecret)
	}
	if cfg.TunnelDomain != "t.example.com" {
		t.Errorf("TunnelDomain = %q", cfg.TunnelDomain)
	}
	if cfg.MaxConcurrent != 10 {
		t.Errorf("MaxConcurrent = %d", cfg.MaxConcurrent)
	}
	if cfg.MaxTTL != 2*time.Hour {
		t.Errorf("MaxTTL = %v", cfg.MaxTTL)
	}
	if cfg.RateCreatePerMin != 20 {
		t.Errorf("RateCreatePerMin = %d", cfg.RateCreatePerMin)
	}
	if cfg.LogUserConns != true {
		t.Error("LogUserConns should be true")
	}
	if cfg.APIReadTimeout != 10*time.Second {
		t.Errorf("APIReadTimeout = %v", cfg.APIReadTimeout)
	}
	if cfg.APIWriteTimeout != 15*time.Second {
		t.Errorf("APIWriteTimeout = %v", cfg.APIWriteTimeout)
	}
}

func TestLoadInvalidValuesFallBack(t *testing.T) {
	os.Clearenv()
	os.Setenv("HATCHWAY_MAX_CONCURRENT_TUNNELS", "not-a-number")
	os.Setenv("HATCHWAY_MAX_TTL", "not-a-duration")
	os.Setenv("HATCHWAY_RATE_CREATE_PER_MIN", "abc")
	os.Setenv("HATCHWAY_LOG_USER_CONNS", "notbool")
	os.Setenv("HATCHWAY_API_READ_TIMEOUT", "bad")

	cfg := Load()
	if cfg.MaxConcurrent != 5 {
		t.Errorf("invalid int should fall back to default, got %d", cfg.MaxConcurrent)
	}
	if cfg.MaxTTL != 24*time.Hour {
		t.Errorf("invalid duration should fall back to default, got %v", cfg.MaxTTL)
	}
	if cfg.RateCreatePerMin != 10 {
		t.Errorf("invalid rate should fall back to default, got %d", cfg.RateCreatePerMin)
	}
	if cfg.LogUserConns != false {
		t.Error("invalid bool should fall back to false")
	}
	if cfg.APIReadTimeout != 30*time.Second {
		t.Errorf("invalid timeout should fall back to default, got %v", cfg.APIReadTimeout)
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"True", true},
		{"FALSE", false},
		{"", false},
	}
	for _, tt := range tests {
		os.Clearenv()
		if tt.val != "" {
			os.Setenv("TEST_BOOL", tt.val)
		}
		got := envBool("TEST_BOOL", false)
		if got != tt.want {
			t.Errorf("envBool(%q) = %v, want %v", tt.val, got, tt.want)
		}
	}
}

func TestEnvDuration(t *testing.T) {
	os.Clearenv()
	os.Setenv("TEST_DUR", "5m30s")
	got := envDuration("TEST_DUR", time.Hour)
	if got != 5*time.Minute+30*time.Second {
		t.Errorf("got %v", got)
	}
}

func TestEnvInt(t *testing.T) {
	os.Clearenv()
	os.Setenv("TEST_INT", "42")
	got := envInt("TEST_INT", 0)
	if got != 42 {
		t.Errorf("got %d", got)
	}
}

func baseValidConfig() *Config {
	return &Config{
		DatabaseURL:     "postgres://localhost/x",
		PluginSecret:    "p",
		FRPSAuthToken:   "a",
		TunnelDomain:    "tunnel.example.com",
		PluginTimeout:   2 * time.Second,
		MaxConcurrent:   5,
		MaxTTL:          time.Hour,
		MaxRequestBytes: 1024,
		FRPSMode:        "external",
	}
}

func TestValidate_OK(t *testing.T) {
	if err := baseValidConfig().Validate(); err != nil {
		t.Fatalf("base config should validate, got %v", err)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"missing DATABASE_URL", func(c *Config) { c.DatabaseURL = "" }, "DATABASE_URL"},
		{"missing PLUGIN_SECRET", func(c *Config) { c.PluginSecret = "" }, "HATCHWAY_PLUGIN_SECRET"},
		{"missing FRPS_AUTH_TOKEN", func(c *Config) { c.FRPSAuthToken = "" }, "HATCHWAY_FRPS_AUTH_TOKEN"},
		{"missing TUNNEL_DOMAIN", func(c *Config) { c.TunnelDomain = "" }, "HATCHWAY_TUNNEL_DOMAIN"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error mentioning %s", tt.want)
			}
			if !contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidate_NumericBounds(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"zero MaxConcurrent", func(c *Config) { c.MaxConcurrent = 0 }, "MAX_CONCURRENT"},
		{"zero MaxTTL", func(c *Config) { c.MaxTTL = 0 }, "MAX_TTL"},
		{"zero PluginTimeout", func(c *Config) { c.PluginTimeout = 0 }, "PLUGIN_TIMEOUT"},
		{"zero MaxRequestBytes", func(c *Config) { c.MaxRequestBytes = 0 }, "MAX_REQUEST_BYTES"},
		{"bad FRPSMode", func(c *Config) { c.FRPSMode = "weird" }, "FRPS_MODE"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error mentioning %s", tt.want)
			}
			if !contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidate_SubprocessRequiresConfigPath(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FRPSMode = "subprocess"
	if err := cfg.Validate(); err == nil {
		t.Fatal("subprocess without config path should fail")
	}
	cfg.FRPSConfigPath = "/etc/frp/frps.toml"
	if err := cfg.Validate(); err != nil {
		t.Errorf("subprocess with config path should pass: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
