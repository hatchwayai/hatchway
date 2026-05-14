# Hatchway Deployment Notes

## Prerequisites

- A domain with DNS managed by Cloudflare
- A cloud VM (GCP, AWS, etc.) with a public IP
- Docker and Docker Compose installed on the VM
- Cloudflare API token with Zone > DNS > Edit permission

## 1. DNS Setup (Cloudflare)

Create three A records pointing to your VM's public IP:

| Record          | Type | Name | Proxy          | Purpose                                   |
| --------------- | ---- | ---- | -------------- | ----------------------------------------- |
| api.domain      | A    | api  | Orange cloud   | API endpoint, proxied via Cloudflare      |
| frps.domain     | A    | frps | **Grey cloud** | frpc TCP connections on port 7000         |
| *.tunnel.domain | A    | *    | **Grey cloud** | Tunnel traffic, Caddy serves wildcard TLS |

**Cloudflare SSL/TLS mode** must be set to **Full (strict)** in Dashboard > SSL/TLS.
"Flexible" causes an infinite 308 redirect loop because Cloudflare connects HTTP to origin, Caddy redirects to HTTPS, loop.

## 2. Cloud Provider Firewall

Open these ports on the VM's cloud firewall:

| Port | Protocol | Purpose                                   |
| ---- | -------- | ----------------------------------------- |
| 80   | TCP      | HTTP (Caddy redirects to HTTPS)           |
| 443  | TCP      | HTTPS (API + tunnel traffic via Caddy)    |
| 7000 | TCP      | frps control channel (frpc connects here) |

GCP example:
```bash
gcloud compute firewall-rules create allow-frps \
  --project YOUR_PROJECT \
  --allow tcp:7000 \
  --direction INGRESS \
  --source-ranges 0.0.0.0/0
```

## 3. Environment Configuration

Copy `.env.example` to `.env` and fill in:

```bash
# Required
HATCHWAY_DOMAIN=yourdomain.com
POSTGRES_PASSWORD=$(openssl rand -hex 24)
HATCHWAY_PLUGIN_SECRET=$(openssl rand -hex 32)
HATCHWAY_FRPS_AUTH_TOKEN=$(openssl rand -hex 32)
CLOUDFLARE_API_TOKEN=your-cloudflare-api-token

# Optional (defaults work)
HATCHWAY_API_DOMAIN=api.yourdomain.com
HATCHWAY_FRPS_DOMAIN=frps.yourdomain.com
HATCHWAY_TUNNEL_DOMAIN=tunnel.yourdomain.com
```

## 4. Deploy

```bash
docker compose up -d --build
```

Wait for postgres to become healthy, then bootstrap:

```bash
docker compose run --rm hatchway-server server init
```

Save the admin token printed — it won't be shown again.

## 5. Verify

```bash
# Health check (from any machine)
curl https://api.yourdomain.com/healthz

# Auth check
curl -H "Authorization: Bearer YOUR_ADMIN_TOKEN" https://api.yourdomain.com/v1/me
```

## 6. End-to-End Tunnel Test

### Create a tunnel

```bash
curl -X POST https://api.yourdomain.com/v1/tunnels \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"type":"http","local_port":8080,"ttl_seconds":300}'
```

Save the `runtime_token`, `tunnel_id`, and `server_token` from the response.

### Configure frpc

frpc requires a TOML config file (CLI flags cannot set client-level metadatas):

```toml
serverAddr = "frps.yourdomain.com"
serverPort = 7000
auth.method = "token"
auth.token = "SERVER_TOKEN_FROM_API"
metadatas.runtime_token = "RUNTIME_TOKEN_FROM_API"

[[proxies]]
name = "TUNNEL_ID"
type = "http"
localPort = 8080
subdomain = "TUNNEL_ID"
```

Key points:
- `auth.token` is `HATCHWAY_FRPS_AUTH_TOKEN` (returned as `server_token` in the API response)
- `metadatas.runtime_token` is the runtime token from the API response (dotted key format, NOT `[metas]` section)
- `name` and `subdomain` must both equal the tunnel ID

### Start local service and frpc

```bash
python3 -m http.server 8080 &
./frpc -c frpc.toml
```

frpc will print `login to server success` and `start proxy success`, then stay running.

### Test

```bash
curl https://TUNNEL_ID.tunnel.yourdomain.com
```

You should see your local HTTP server's response.

## 7. Tunnel Management

```bash
# List tunnels
curl https://api.yourdomain.com/v1/tunnels \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN"

# Get a specific tunnel
curl https://api.yourdomain.com/v1/tunnels/TUNNEL_ID \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN"

# Delete a tunnel
curl -X DELETE https://api.yourdomain.com/v1/tunnels/TUNNEL_ID \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN"

# Admin revoke (force-revoke even if active)
curl -X POST https://api.yourdomain.com/v1/admin/tunnels/TUNNEL_ID/revoke \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN"
```

## Common Issues

| Symptom                                                  | Cause                                                                      | Fix                                                                               |
| -------------------------------------------------------- | -------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| 308 redirect loop on API                                 | Cloudflare SSL mode is "Flexible"                                          | Set to "Full (strict)"                                                            |
| frps crash-loop: `unknown field "headers"`               | frps doesn't support custom headers in httpPlugins config                  | Use path-based auth: `path = "/frp/plugin/${SECRET}"`                             |
| hatchway-server crash-loop: `unknown command "hatchway"` | Dockerfile ENTRYPOINT is already `/hatchway`, command had extra `hatchway` | Use `command: ["server", "run"]` in docker-compose                                |
| frpc: `i/o timeout` on port 7000                         | Cloud provider firewall blocks port 7000                                   | Add firewall rule for tcp:7000                                                    |
| frpc: `missing runtime token`                            | `--metadatas` CLI flag sets proxy-level metas, not client-level            | Use TOML config file with `metadatas.runtime_token`                               |
| frpc: `invalid credentials`                              | frps entrypoint not substituting `HATCHWAY_FRPS_AUTH_TOKEN`                | Ensure envsubst includes all needed vars in entrypoint                            |
| SSL handshake failure on tunnel URL                      | Cloudflare Universal SSL wildcard not provisioned                          | Grey-cloud the `*.tunnel` DNS record; let Caddy serve Let's Encrypt wildcard cert |
| `curl` returns empty on HTTP                             | Caddy auto-HTTPS redirects HTTP to HTTPS                                   | Use `https://` or `curl -L` to follow redirects                                   |
| frps "Not Found" page                                    | frpc not running or tunnel expired                                         | Keep frpc running; recreate tunnel if TTL expired                                 |
| Docker socket permission denied                          | User not in docker group                                                   | Use `sudo` or `sudo usermod -aG docker $USER`                                     |

## Architecture Summary

```
Client (curl/browser)
  │
  ├─ HTTPS ──► Cloudflare (orange cloud) ──► Caddy:443 ──► hatchway-server:9000
  │             api.yourdomain.com                          (API endpoints)
  │
  └─ HTTPS ──► Caddy:443 ──► frps:8081 ──► frpc ──► localhost:8080
               *.tunnel.yourdomain.com      (vhost proxy)   (your local service)

frpc ──TCP:7000──► frps:7000
                    (control channel, grey cloud DNS)
```
