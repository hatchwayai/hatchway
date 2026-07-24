package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Generated public hosts prepend an 18-byte tunnel label and a dot. DNS names
// are limited to 253 bytes, leaving at most 234 bytes for the configured root.
const maxTunnelDomainLength = 253 - 18 - 1

// Config contains all server runtime settings.
type Config struct {
	DatabaseURL               string
	APIAddr                   string
	FRPSPluginAddr            string
	APIReadTimeout            time.Duration
	APIWriteTimeout           time.Duration
	MaxRequestBytes           int64
	FRPSDomain                string
	TunnelDomain              string
	PluginSecret              string // internal callback/request-gate path secret (never returned to API users)
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
	RuntimeTokenRetentionDays int
}

// Load reads Config from the environment. Invalid typed values are reported
// instead of silently falling back to defaults, which keeps operator typos
// from starting a server with settings other than the ones requested.
func Load() (*Config, error) {
	var parseErrors []error

	readInt := func(key string, fallback int) int {
		value, err := envInt(key, fallback)
		if err != nil {
			parseErrors = append(parseErrors, err)
		}
		return value
	}
	readDuration := func(key string, fallback time.Duration) time.Duration {
		value, err := envDuration(key, fallback)
		if err != nil {
			parseErrors = append(parseErrors, err)
		}
		return value
	}
	readBool := func(key string, fallback bool) bool {
		value, err := envBool(key, fallback)
		if err != nil {
			parseErrors = append(parseErrors, err)
		}
		return value
	}

	cfg := &Config{
		DatabaseURL:               envString("DATABASE_URL", ""),
		APIAddr:                   envString("HATCHWAY_API_ADDR", ":9000"),
		FRPSPluginAddr:            envString("HATCHWAY_FRPS_PLUGIN_ADDR", ":9001"),
		APIReadTimeout:            readDuration("HATCHWAY_API_READ_TIMEOUT", 30*time.Second),
		APIWriteTimeout:           readDuration("HATCHWAY_API_WRITE_TIMEOUT", 30*time.Second),
		MaxRequestBytes:           int64(readInt("HATCHWAY_MAX_REQUEST_BYTES", 64*1024)),
		FRPSDomain:                envString("HATCHWAY_FRPS_DOMAIN", ""),
		TunnelDomain:              envString("HATCHWAY_TUNNEL_DOMAIN", "tunnel.example.com"),
		PluginSecret:              envString("HATCHWAY_PLUGIN_SECRET", ""),
		FRPSAuthToken:             envString("HATCHWAY_FRPS_AUTH_TOKEN", ""),
		PluginTimeout:             readDuration("HATCHWAY_PLUGIN_TIMEOUT", 2*time.Second),
		MaxConcurrent:             readInt("HATCHWAY_MAX_CONCURRENT_TUNNELS", 5),
		MaxTTL:                    readDuration("HATCHWAY_MAX_TTL", 24*time.Hour),
		RateCreatePerMin:          readInt("HATCHWAY_RATE_CREATE_PER_MIN", 10),
		LogUserConns:              readBool("HATCHWAY_LOG_USER_CONNS", false),
		FRPSMode:                  envString("HATCHWAY_FRPS_MODE", "external"),
		FRPSBinPath:               envString("HATCHWAY_FRPS_BIN_PATH", "frps"),
		FRPSConfigPath:            envString("HATCHWAY_FRPS_CONFIG_PATH", ""),
		EventsRetentionDays:       readInt("HATCHWAY_EVENTS_RETENTION_DAYS", 30),
		IdempotencyRetentionHours: readInt("HATCHWAY_IDEMPOTENCY_RETENTION_HOURS", 24),
		RuntimeTokenRetentionDays: readInt("HATCHWAY_RUNTIME_TOKEN_RETENTION_DAYS", 7),
	}
	return cfg, errors.Join(parseErrors...)
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
	if c.FRPSDomain == "" {
		missing = append(missing, "HATCHWAY_FRPS_DOMAIN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	if !validServiceSecret(c.PluginSecret) {
		return fmt.Errorf("HATCHWAY_PLUGIN_SECRET must be 32-256 URL-safe characters (letters, digits, '_' or '-')")
	}
	if !validServiceSecret(c.FRPSAuthToken) {
		return fmt.Errorf("HATCHWAY_FRPS_AUTH_TOKEN must be 32-256 URL-safe characters (letters, digits, '_' or '-')")
	}
	if c.PluginSecret == c.FRPSAuthToken {
		return fmt.Errorf("HATCHWAY_PLUGIN_SECRET and HATCHWAY_FRPS_AUTH_TOKEN must be different")
	}
	if !validHostname(c.TunnelDomain) {
		return fmt.Errorf("HATCHWAY_TUNNEL_DOMAIN must be a valid hostname without a scheme, wildcard, port, or trailing dot")
	}
	if len(c.TunnelDomain) > maxTunnelDomainLength {
		return fmt.Errorf("HATCHWAY_TUNNEL_DOMAIN must be at most %d bytes so generated tunnel hosts fit DNS limits", maxTunnelDomainLength)
	}
	if !validHostname(c.FRPSDomain) {
		return fmt.Errorf("HATCHWAY_FRPS_DOMAIN must be a valid hostname without a scheme, wildcard, port, or trailing dot")
	}
	if c.MaxConcurrent <= 0 {
		return fmt.Errorf("HATCHWAY_MAX_CONCURRENT_TUNNELS must be > 0, got %d", c.MaxConcurrent)
	}
	if c.MaxTTL < time.Second {
		return fmt.Errorf("HATCHWAY_MAX_TTL must be at least 1s, got %s", c.MaxTTL)
	}
	if c.PluginTimeout <= 0 {
		return fmt.Errorf("HATCHWAY_PLUGIN_TIMEOUT must be > 0, got %s", c.PluginTimeout)
	}
	if c.APIReadTimeout <= 0 {
		return fmt.Errorf("HATCHWAY_API_READ_TIMEOUT must be > 0, got %s", c.APIReadTimeout)
	}
	if c.APIWriteTimeout <= 0 {
		return fmt.Errorf("HATCHWAY_API_WRITE_TIMEOUT must be > 0, got %s", c.APIWriteTimeout)
	}
	if c.RateCreatePerMin <= 0 {
		return fmt.Errorf("HATCHWAY_RATE_CREATE_PER_MIN must be > 0, got %d", c.RateCreatePerMin)
	}
	if c.FRPSMode != "external" && c.FRPSMode != "subprocess" {
		return fmt.Errorf("HATCHWAY_FRPS_MODE must be 'external' or 'subprocess', got %q", c.FRPSMode)
	}
	if c.FRPSMode == "subprocess" && c.FRPSConfigPath == "" {
		return fmt.Errorf("HATCHWAY_FRPS_CONFIG_PATH is required when HATCHWAY_FRPS_MODE=subprocess")
	}
	if c.FRPSMode == "subprocess" && c.FRPSBinPath == "" {
		return fmt.Errorf("HATCHWAY_FRPS_BIN_PATH is required when HATCHWAY_FRPS_MODE=subprocess")
	}
	if c.MaxRequestBytes <= 0 {
		return fmt.Errorf("HATCHWAY_MAX_REQUEST_BYTES must be > 0, got %d", c.MaxRequestBytes)
	}
	if c.EventsRetentionDays <= 0 {
		return fmt.Errorf("HATCHWAY_EVENTS_RETENTION_DAYS must be > 0, got %d", c.EventsRetentionDays)
	}
	if c.IdempotencyRetentionHours <= 0 {
		return fmt.Errorf("HATCHWAY_IDEMPOTENCY_RETENTION_HOURS must be > 0, got %d", c.IdempotencyRetentionHours)
	}
	if c.RuntimeTokenRetentionDays <= 0 {
		return fmt.Errorf("HATCHWAY_RUNTIME_TOKEN_RETENTION_DAYS must be > 0, got %d", c.RuntimeTokenRetentionDays)
	}
	return nil
}

func validServiceSecret(value string) bool {
	if len(value) < 32 || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' ||
			char == '-' {
			continue
		}
		return false
	}
	return true
}

func validHostname(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for label := range strings.SplitSeq(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') ||
				(char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') ||
				char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fallback, fmt.Errorf("%s must be an integer, got %q: %w", key, v, err)
		}
		return n, nil
	}
	return fallback, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fallback, fmt.Errorf("%s must be a duration, got %q: %w", key, v, err)
		}
		return d, nil
	}
	return fallback, nil
}

func envBool(key string, fallback bool) (bool, error) {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fallback, fmt.Errorf("%s must be a boolean, got %q: %w", key, v, err)
		}
		return b, nil
	}
	return fallback, nil
}
