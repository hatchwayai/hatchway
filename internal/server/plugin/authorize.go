package plugin

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hatchwayai/hatchway/internal/config"
)

// TunnelHostHeader is set by the trusted reverse proxy on tunnel
// authorization subrequests. The proxy must overwrite any client-supplied
// value before calling TunnelAuthorizationHandler.
const TunnelHostHeader = "X-Hatchway-Tunnel-Host"

// TunnelAuthorizationHandler authorizes one HTTP request to a wildcard tunnel.
// The bundled Caddy configuration calls it before proxying traffic to frps, so
// revocation and TTL expiry take effect even while frps retains a registered
// HTTP proxy.
func TunnelAuthorizationHandler(pool *pgxpool.Pool, cfg *config.Config) http.HandlerFunc {
	timeout := cfg.PluginTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if cfg.PluginSecret == "" || !constantTimeEqual(r.PathValue("secret"), cfg.PluginSecret) {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		hostValues := r.Header.Values(TunnelHostHeader)
		if len(hostValues) != 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		tunnelID, ok := tunnelIDFromHost(hostValues[0], cfg.TunnelDomain)
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if pool == nil {
			slog.Error("tunnel authorization unavailable: database pool is nil")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		allowed, err := tunnelAcceptsTraffic(ctx, pool, tunnelID)
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if err != nil {
			slog.Warn("tunnel authorization lookup failed", "tunnel_id", tunnelID, "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func tunnelAcceptsTraffic(ctx context.Context, pool *pgxpool.Pool, tunnelID string) (bool, error) {
	var allowed bool
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(status IN ('active', 'closed') AND expires_at > now(), false)
		   FROM tunnels
		  WHERE id = $1`,
		tunnelID,
	).Scan(&allowed)
	return allowed, err
}

func tunnelIDFromHost(rawHost, rawDomain string) (string, bool) {
	host, ok := canonicalRequestHost(rawHost)
	if !ok {
		return "", false
	}

	domain := strings.ToLower(strings.TrimSuffix(rawDomain, "."))
	if domain == "" || strings.ContainsAny(domain, "/:@ \t\r\n") {
		return "", false
	}

	suffix := "." + domain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	tunnelID := strings.TrimSuffix(host, suffix)
	if len(tunnelID) < 3 || len(tunnelID) > 63 || !strings.HasPrefix(tunnelID, "t-") {
		return "", false
	}
	for i, char := range tunnelID {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			continue
		}
		if char == '-' && i > 0 && i < len(tunnelID)-1 {
			continue
		}
		return "", false
	}
	return tunnelID, true
}

func canonicalRequestHost(rawHost string) (string, bool) {
	if rawHost == "" || strings.TrimSpace(rawHost) != rawHost {
		return "", false
	}

	host := rawHost
	if strings.Contains(host, ":") {
		var err error
		var port string
		host, port, err = net.SplitHostPort(host)
		if err != nil {
			return "", false
		}
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return "", false
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || strings.ContainsAny(host, "/@ \t\r\n") {
		return "", false
	}
	return host, true
}
