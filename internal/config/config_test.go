package config

import (
	"strings"
	"testing"
	"time"
)

var configEnvKeys = []string{
	"DATABASE_URL",
	"HATCHWAY_API_ADDR",
	"HATCHWAY_FRPS_PLUGIN_ADDR",
	"HATCHWAY_API_READ_TIMEOUT",
	"HATCHWAY_API_WRITE_TIMEOUT",
	"HATCHWAY_MAX_REQUEST_BYTES",
	"HATCHWAY_FRPS_DOMAIN",
	"HATCHWAY_TUNNEL_DOMAIN",
	"HATCHWAY_PLUGIN_SECRET",
	"HATCHWAY_FRPS_AUTH_TOKEN",
	"HATCHWAY_PLUGIN_TIMEOUT",
	"HATCHWAY_MAX_CONCURRENT_TUNNELS",
	"HATCHWAY_MAX_TTL",
	"HATCHWAY_RATE_CREATE_PER_MIN",
	"HATCHWAY_LOG_USER_CONNS",
	"HATCHWAY_FRPS_MODE",
	"HATCHWAY_FRPS_BIN_PATH",
	"HATCHWAY_FRPS_CONFIG_PATH",
	"HATCHWAY_EVENTS_RETENTION_DAYS",
	"HATCHWAY_IDEMPOTENCY_RETENTION_HOURS",
	"HATCHWAY_RUNTIME_TOKEN_RETENTION_DAYS",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range configEnvKeys {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
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
	clearConfigEnv(t)
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("HATCHWAY_API_ADDR", ":8080")
	t.Setenv("HATCHWAY_FRPS_PLUGIN_ADDR", ":9090")
	t.Setenv("HATCHWAY_PLUGIN_SECRET", "my-secret")
	t.Setenv("HATCHWAY_TUNNEL_DOMAIN", "t.example.com")
	t.Setenv("HATCHWAY_MAX_CONCURRENT_TUNNELS", "10")
	t.Setenv("HATCHWAY_MAX_TTL", "2h")
	t.Setenv("HATCHWAY_RATE_CREATE_PER_MIN", "20")
	t.Setenv("HATCHWAY_LOG_USER_CONNS", "true")
	t.Setenv("HATCHWAY_API_READ_TIMEOUT", "10s")
	t.Setenv("HATCHWAY_API_WRITE_TIMEOUT", "15s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
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

func TestLoadInvalidValuesReturnError(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HATCHWAY_MAX_CONCURRENT_TUNNELS", "not-a-number")
	t.Setenv("HATCHWAY_MAX_TTL", "not-a-duration")
	t.Setenv("HATCHWAY_RATE_CREATE_PER_MIN", "abc")
	t.Setenv("HATCHWAY_LOG_USER_CONNS", "notbool")
	t.Setenv("HATCHWAY_API_READ_TIMEOUT", "bad")

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load() should reject invalid typed values")
	}
	for _, key := range []string{
		"HATCHWAY_MAX_CONCURRENT_TUNNELS",
		"HATCHWAY_MAX_TTL",
		"HATCHWAY_RATE_CREATE_PER_MIN",
		"HATCHWAY_LOG_USER_CONNS",
		"HATCHWAY_API_READ_TIMEOUT",
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Load() error %q does not mention %s", err, key)
		}
	}

	// Defaults are still populated so diagnostics and tests can inspect the
	// complete candidate config, but callers must not ignore the error.
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
		t.Setenv("TEST_BOOL", tt.val)
		got, err := envBool("TEST_BOOL", false)
		if err != nil {
			t.Fatalf("envBool(%q) error = %v", tt.val, err)
		}
		if got != tt.want {
			t.Errorf("envBool(%q) = %v, want %v", tt.val, got, tt.want)
		}
	}
}

func TestEnvDuration(t *testing.T) {
	t.Setenv("TEST_DUR", "5m30s")
	got, err := envDuration("TEST_DUR", time.Hour)
	if err != nil {
		t.Fatalf("envDuration() error = %v", err)
	}
	if got != 5*time.Minute+30*time.Second {
		t.Errorf("got %v", got)
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	got, err := envInt("TEST_INT", 0)
	if err != nil {
		t.Fatalf("envInt() error = %v", err)
	}
	if got != 42 {
		t.Errorf("got %d", got)
	}
}

func baseValidConfig() *Config {
	return &Config{
		DatabaseURL:               "postgres://localhost/x",
		PluginSecret:              "plugin_secret_0123456789abcdef01",
		FRPSAuthToken:             "frps_auth_token_0123456789abcdef",
		TunnelDomain:              "tunnel.example.com",
		FRPSDomain:                "frps.example.com",
		PluginTimeout:             2 * time.Second,
		APIReadTimeout:            30 * time.Second,
		APIWriteTimeout:           30 * time.Second,
		MaxConcurrent:             5,
		MaxTTL:                    time.Hour,
		RateCreatePerMin:          10,
		MaxRequestBytes:           1024,
		FRPSMode:                  "external",
		EventsRetentionDays:       30,
		IdempotencyRetentionHours: 24,
		RuntimeTokenRetentionDays: 7,
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
		{"missing FRPS_DOMAIN", func(c *Config) { c.FRPSDomain = "" }, "HATCHWAY_FRPS_DOMAIN"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error mentioning %s", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
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
		{"subsecond MaxTTL", func(c *Config) { c.MaxTTL = 500 * time.Millisecond }, "MAX_TTL"},
		{"zero PluginTimeout", func(c *Config) { c.PluginTimeout = 0 }, "PLUGIN_TIMEOUT"},
		{"zero APIReadTimeout", func(c *Config) { c.APIReadTimeout = 0 }, "API_READ_TIMEOUT"},
		{"zero APIWriteTimeout", func(c *Config) { c.APIWriteTimeout = 0 }, "API_WRITE_TIMEOUT"},
		{"zero RateCreatePerMin", func(c *Config) { c.RateCreatePerMin = 0 }, "RATE_CREATE_PER_MIN"},
		{"zero MaxRequestBytes", func(c *Config) { c.MaxRequestBytes = 0 }, "MAX_REQUEST_BYTES"},
		{"bad FRPSMode", func(c *Config) { c.FRPSMode = "weird" }, "FRPS_MODE"},
		{"zero EventsRetentionDays", func(c *Config) { c.EventsRetentionDays = 0 }, "EVENTS_RETENTION_DAYS"},
		{"negative EventsRetentionDays", func(c *Config) { c.EventsRetentionDays = -1 }, "EVENTS_RETENTION_DAYS"},
		{"zero IdempotencyRetentionHours", func(c *Config) { c.IdempotencyRetentionHours = 0 }, "IDEMPOTENCY_RETENTION_HOURS"},
		{"zero RuntimeTokenRetentionDays", func(c *Config) { c.RuntimeTokenRetentionDays = 0 }, "RUNTIME_TOKEN_RETENTION_DAYS"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error mentioning %s", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidate_ServiceSecretsAndDomains(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"short plugin secret", func(c *Config) { c.PluginSecret = "short" }, "HATCHWAY_PLUGIN_SECRET"},
		{"unsafe plugin secret", func(c *Config) { c.PluginSecret = strings.Repeat("a", 31) + "/" }, "HATCHWAY_PLUGIN_SECRET"},
		{"short frps token", func(c *Config) { c.FRPSAuthToken = "short" }, "HATCHWAY_FRPS_AUTH_TOKEN"},
		{"same secrets", func(c *Config) { c.FRPSAuthToken = c.PluginSecret }, "must be different"},
		{"tunnel scheme", func(c *Config) { c.TunnelDomain = "https://tunnel.example.com" }, "HATCHWAY_TUNNEL_DOMAIN"},
		{"tunnel wildcard", func(c *Config) { c.TunnelDomain = "*.tunnel.example.com" }, "HATCHWAY_TUNNEL_DOMAIN"},
		{"frps port", func(c *Config) { c.FRPSDomain = "frps.example.com:7000" }, "HATCHWAY_FRPS_DOMAIN"},
		{"trailing dot", func(c *Config) { c.TunnelDomain = "tunnel.example.com." }, "HATCHWAY_TUNNEL_DOMAIN"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want mention of %q", err, tt.want)
			}
		})
	}
}

func TestValidate_TunnelDomainGeneratedHostBoundary(t *testing.T) {
	maxRoot := strings.Join([]string{
		strings.Repeat("a", 63),
		strings.Repeat("b", 63),
		strings.Repeat("c", 63),
		strings.Repeat("d", 42),
	}, ".")
	if len(maxRoot) != maxTunnelDomainLength {
		t.Fatalf("test root length = %d, want %d", len(maxRoot), maxTunnelDomainLength)
	}

	cfg := baseValidConfig()
	cfg.TunnelDomain = maxRoot
	if err := cfg.Validate(); err != nil {
		t.Fatalf("maximum tunnel domain should validate: %v", err)
	}

	cfg.TunnelDomain += "e"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at most 234 bytes") {
		t.Fatalf("oversized tunnel domain error = %v", err)
	}
}

func TestValidate_SubprocessRequiresConfigPath(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FRPSMode = "subprocess"
	if err := cfg.Validate(); err == nil {
		t.Fatal("subprocess without config path should fail")
	}
	cfg.FRPSConfigPath = "/etc/frp/frps.toml"
	cfg.FRPSBinPath = "/usr/local/bin/frps"
	if err := cfg.Validate(); err != nil {
		t.Errorf("subprocess with binary and config paths should pass: %v", err)
	}
}
