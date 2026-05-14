package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL               string
	APIAddr                   string
	FRPSPluginAddr            string
	APIReadTimeout            time.Duration
	APIWriteTimeout           time.Duration
	MaxRequestBytes           int64
	Domain                    string
	APIDomain                 string
	FRPSDomain                string
	TunnelDomain              string
	PluginSecret              string // frps→server plugin auth header (never returned to API users)
	FRPSAuthToken             string // frps↔frpc bootstrap secret (returned to API users)
	PluginTimeout             time.Duration
	MaxConcurrent             int
	MaxTTL                    time.Duration
	RateCreatePerMin          int
	LogUserConns              bool
	FRPSMode                  string // "external" (default) or "subprocess"
	FRPSBinPath               string // path to frps binary (for subprocess mode)
	FRPSConfigPath            string // path to frps config (subprocess mode)
	EventsRetentionDays       int
	IdempotencyRetentionHours int
}

func Load() *Config {
	return &Config{
		DatabaseURL:               envString("DATABASE_URL", ""),
		APIAddr:                   envString("HATCHWAY_API_ADDR", ":9000"),
		FRPSPluginAddr:            envString("HATCHWAY_FRPS_PLUGIN_ADDR", ":9001"),
		APIReadTimeout:            envDuration("HATCHWAY_API_READ_TIMEOUT", 30*time.Second),
		APIWriteTimeout:           envDuration("HATCHWAY_API_WRITE_TIMEOUT", 30*time.Second),
		MaxRequestBytes:           int64(envInt("HATCHWAY_MAX_REQUEST_BYTES", 64*1024)),
		Domain:                    envString("HATCHWAY_DOMAIN", ""),
		APIDomain:                 envString("HATCHWAY_API_DOMAIN", ""),
		FRPSDomain:                envString("HATCHWAY_FRPS_DOMAIN", ""),
		TunnelDomain:              envString("HATCHWAY_TUNNEL_DOMAIN", "tunnel.example.com"),
		PluginSecret:              envString("HATCHWAY_PLUGIN_SECRET", ""),
		FRPSAuthToken:             envString("HATCHWAY_FRPS_AUTH_TOKEN", ""),
		PluginTimeout:             envDuration("HATCHWAY_PLUGIN_TIMEOUT", 2*time.Second),
		MaxConcurrent:             envInt("HATCHWAY_MAX_CONCURRENT_TUNNELS", 5),
		MaxTTL:                    envDuration("HATCHWAY_MAX_TTL", 24*time.Hour),
		RateCreatePerMin:          envInt("HATCHWAY_RATE_CREATE_PER_MIN", 10),
		LogUserConns:              envBool("HATCHWAY_LOG_USER_CONNS", false),
		FRPSMode:                  envString("HATCHWAY_FRPS_MODE", "external"),
		FRPSBinPath:               envString("HATCHWAY_FRPS_BIN_PATH", "frps"),
		FRPSConfigPath:            envString("HATCHWAY_FRPS_CONFIG_PATH", ""),
		EventsRetentionDays:       envInt("HATCHWAY_EVENTS_RETENTION_DAYS", 30),
		IdempotencyRetentionHours: envInt("HATCHWAY_IDEMPOTENCY_RETENTION_HOURS", 24),
	}
}

// Validate returns an error if the config is missing values that the server
// needs at startup. Defaults are applied in Load(), so anything missing here
// is something an operator must set explicitly.
func (c *Config) Validate() error {
	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if c.PluginSecret == "" {
		missing = append(missing, "HATCHWAY_PLUGIN_SECRET")
	}
	if c.FRPSAuthToken == "" {
		missing = append(missing, "HATCHWAY_FRPS_AUTH_TOKEN")
	}
	if c.TunnelDomain == "" {
		missing = append(missing, "HATCHWAY_TUNNEL_DOMAIN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	if c.MaxConcurrent <= 0 {
		return fmt.Errorf("HATCHWAY_MAX_CONCURRENT_TUNNELS must be > 0, got %d", c.MaxConcurrent)
	}
	if c.MaxTTL <= 0 {
		return fmt.Errorf("HATCHWAY_MAX_TTL must be > 0, got %s", c.MaxTTL)
	}
	if c.PluginTimeout <= 0 {
		return fmt.Errorf("HATCHWAY_PLUGIN_TIMEOUT must be > 0, got %s", c.PluginTimeout)
	}
	if c.FRPSMode != "external" && c.FRPSMode != "subprocess" {
		return fmt.Errorf("HATCHWAY_FRPS_MODE must be 'external' or 'subprocess', got %q", c.FRPSMode)
	}
	if c.FRPSMode == "subprocess" && c.FRPSConfigPath == "" {
		return fmt.Errorf("HATCHWAY_FRPS_CONFIG_PATH is required when HATCHWAY_FRPS_MODE=subprocess")
	}
	if c.MaxRequestBytes <= 0 {
		return fmt.Errorf("HATCHWAY_MAX_REQUEST_BYTES must be > 0, got %d", c.MaxRequestBytes)
	}
	return nil
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
