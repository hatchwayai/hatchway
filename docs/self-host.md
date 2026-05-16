# Self-hosting Hatchway

A guide to running Hatchway on your own infrastructure.

## Prerequisites

- A VPS or server with a public IP (minimum 1 GB RAM)
- Docker and Docker Compose installed
- A domain name you control (e.g. `example.com`)
- A Cloudflare API token with Zone > DNS > Edit permission (for wildcard TLS via DNS-01 challenge)
- Go 1.22+ (only needed for building from source)

## VPS preparation

### 1. Install Docker

```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
# Log out and back in for group changes to take effect
```

### 2. Open firewall ports

| Port | Protocol | Purpose                                 |
| ---- | -------- | --------------------------------------- |
| 80   | TCP      | HTTP (Caddy — redirects to HTTPS)       |
| 443  | TCP      | HTTPS (Caddy — API + tunnel traffic)    |
| 7000 | TCP      | frps control channel (frpc connections) |

> **Note:** Port 9001 (plugin endpoint) should **not** be exposed publicly. It is only used on the internal Docker network.

Example for GCP:

```bash
gcloud compute firewall-rules create allow-frps \
  --project YOUR_PROJECT \
  --allow tcp:7000 \
  --direction INGRESS \
  --source-ranges 0.0.0.0/0
```

### 3. Clone the repo

```bash
git clone https://github.com/zydo/hatchway.git
cd hatchway
```

## DNS records

Create the following A records with your DNS provider:

| Record                 | Type | Value              |
| ---------------------- | ---- | ------------------ |
| `api.example.com`      | A    | `<your-server-ip>` |
| `frps.example.com`     | A    | `<your-server-ip>` |
| `*.tunnel.example.com` | A    | `<your-server-ip>` |

### Cloudflare-specific setup

If using Cloudflare for DNS:

- **`api.example.com`** — can use **orange cloud** (proxied). Cloudflare handles DDoS protection and client-facing TLS.
- **`frps.example.com`** — must use **grey cloud** (DNS only). frpc connects on raw TCP port 7000, which Cloudflare cannot proxy.
- **`*.tunnel.example.com`** — must use **grey cloud** (DNS only). Caddy serves the wildcard TLS certificate directly via Let's Encrypt DNS-01 challenge. Cloudflare's Universal SSL wildcard provisioning is unreliable for per-tunnel subdomains.
- **SSL/TLS mode** — must be set to **Full (strict)** in Cloudflare Dashboard > SSL/TLS. "Flexible" causes an infinite 308 redirect loop (Cloudflare connects HTTP to origin, Caddy redirects to HTTPS, loop).

Wait for DNS propagation before proceeding (usually a few minutes).

## Configuration

### 1. Create `.env`

```bash
cp .env.example .env
```

### 2. Set required variables

Edit `.env`:

```bash
# Your domain
HATCHWAY_DOMAIN=example.com

# Strong PostgreSQL password (generate with: openssl rand -hex 24)
POSTGRES_PASSWORD=<random-24-byte-hex>

# Plugin secret: frps→server plugin auth. Server-internal — never
# returned to API users. Generate with: openssl rand -hex 32
HATCHWAY_PLUGIN_SECRET=<random-32-byte-hex>

# frps↔frpc bootstrap secret. Returned to API users in the create-tunnel
# response so frpc can authenticate to frps. Should be a separate value from
# HATCHWAY_PLUGIN_SECRET. Generate with: openssl rand -hex 32
HATCHWAY_FRPS_AUTH_TOKEN=<random-32-byte-hex>

# Cloudflare API token for DNS-01 wildcard certificate challenge.
# Requires Zone > DNS > Edit permission for your domain.
CLOUDFLARE_API_TOKEN=<your-cloudflare-api-token>
```

> **Templated configs:** `frps.toml` is rendered from `frps.toml.tmpl` at frps
> container start (via `envsubst` in the entrypoint), and `Caddyfile` reads
> `{$HATCHWAY_API_DOMAIN}` / `{$HATCHWAY_TUNNEL_DOMAIN}` / `{$CLOUDFLARE_API_TOKEN}`
> from the environment. Both pick up values from `.env` via `docker compose`.
> You should not see any placeholder strings (`example.com`, `CHANGE_ME_…`) in a
> healthy stack — if you do, your `.env` is missing a value.

### 3. Optional overrides

```bash
# PostgreSQL (defaults shown)
POSTGRES_DB=hatchway
POSTGRES_USER=hatchway

# Domain overrides (default to subdomains of HATCHWAY_DOMAIN)
HATCHWAY_API_DOMAIN=api.example.com
HATCHWAY_FRPS_DOMAIN=frps.example.com
HATCHWAY_TUNNEL_DOMAIN=tunnel.example.com

# Tunnel limits
HATCHWAY_MAX_CONCURRENT_TUNNELS=5
HATCHWAY_MAX_TTL=24h
HATCHWAY_RATE_CREATE_PER_MIN=10

# Server timeouts
HATCHWAY_API_READ_TIMEOUT=30s
HATCHWAY_API_WRITE_TIMEOUT=30s

# How long the frps plugin handler may take before returning.
# frps treats timeouts as failures, so don't set this below a typical DB query.
HATCHWAY_PLUGIN_TIMEOUT=2s

# Logging
HATCHWAY_LOG_USER_CONNS=false
```

## Deploy

### 1. Start the stack

```bash
docker compose up -d
```

Verify all services are healthy:

```bash
docker compose ps
```

### 2. Bootstrap the database

```bash
docker compose run --rm hatchway-server server init
```

> **Note:** The container's ENTRYPOINT is already `/hatchway`, so use
> `docker compose run --rm hatchway-server server init` (not `exec` and not
> `hatchway server init` — the binary name is implicit).

This command:
- Runs database migrations
- Creates the admin user
- Prints the first API token to stdout

**Save this token** — it is shown exactly once.

If you need to reinitialize (e.g. in development):

```bash
docker compose run --rm hatchway-server server init --force
```

### 3. Verify

```bash
curl -sf https://api.example.com/healthz
curl -sf https://api.example.com/readyz
```

Both should return `200 OK`.

## First tunnel

### Option A: Using the Hatchway CLI (recommended)

#### 1. Configure the client

```bash
hatchway auth set-token --server https://api.example.com sk_live_abc123...
```

#### 2. Verify authentication

```bash
hatchway auth whoami
```

#### 3. Create a tunnel

Start a local server (e.g. a Python HTTP server):

```bash
python3 -m http.server 3000
```

In another terminal:

```bash
hatchway http 3000 --ttl 15m
```

This prints a public URL. Visit it in a browser to confirm traffic reaches your local server.

#### 4. Tear down

Press Ctrl-C in the terminal running `hatchway http`. The tunnel is cleaned up automatically.

### Option B: Using frpc directly

If the Hatchway CLI is not yet installed, you can test with frpc manually.

#### 1. Create a tunnel via the API

```bash
curl -X POST https://api.example.com/v1/tunnels \
  -H "Authorization: Bearer sk_live_abc123..." \
  -H "Content-Type: application/json" \
  -d '{"type":"http","local_port":8080,"ttl_seconds":300}'
```

Save the `runtime_token`, `tunnel_id`, and `server_token` from the response.

#### 2. Create an frpc config file

frpc requires a TOML config file — CLI flags cannot set client-level metadatas:

```toml
serverAddr = "frps.example.com"
serverPort = 7000
auth.method = "token"
auth.token = "SERVER_TOKEN_FROM_API_RESPONSE"
metadatas.runtime_token = "RUNTIME_TOKEN_FROM_API_RESPONSE"

[[proxies]]
name = "TUNNEL_ID"
type = "http"
localPort = 8080
subdomain = "TUNNEL_ID"
```

Key points:
- `auth.token` is the `server_token` from the API response (your `HATCHWAY_FRPS_AUTH_TOKEN`).
- `metadatas.runtime_token` uses dotted-key format, NOT a `[metas]` section.
- `name` and `subdomain` must both equal the tunnel ID.

#### 3. Start frpc

```bash
python3 -m http.server 8080 &
./frpc -c frpc.toml
```

frpc prints `login to server success` and `start proxy success`, then stays running.

#### 4. Test

```bash
curl https://TUNNEL_ID.tunnel.example.com
```

You should see your local HTTP server's response.

## Managing users and tokens

### Create a user

```bash
docker compose run --rm hatchway-server \
  hatchway server user create --email alice@example.com --name Alice
```

### Create an API token for a user

```bash
docker compose run --rm hatchway-server \
  hatchway server token create --user alice@example.com --name "laptop"
```

### List users

```bash
docker compose run --rm hatchway-server hatchway server user list
```

### Revoke a token

```bash
docker compose run --rm hatchway-server \
  hatchway server token revoke <token-id>
```

### Admin revoke a tunnel

> Server-side commands (`hatchway server tunnels`, `server user create`, `server token revoke`)
> read the database directly and do **not** go through the API or `AdminOnly` middleware.
> They assume operator-on-host trust: anyone who can `docker compose run --rm hatchway-server …`
> can list and modify any tunnel. End-user tooling should always go through the API
> with a Bearer token.

The endpoint requires the caller's token to belong to a user with `is_admin = true`.
Bootstrap admins are created by `hatchway server init`; create additional admins via:

```bash
docker compose run --rm hatchway-server \
  hatchway server user create --email ops@example.com --name Ops --admin
```

Then:

```bash
curl -X POST https://api.example.com/v1/admin/tunnels/<tunnel-id>/revoke \
  -H "Authorization: Bearer sk_live_<admin-token>"
```

Non-admin tokens get `403 FORBIDDEN`.

## Monitoring

### Health endpoints

- `GET /healthz` — always returns `200 OK` (cheap liveness check)
- `GET /readyz` — returns `200 OK` if the database is reachable; `503` otherwise

### Prometheus metrics

Metrics are served on the **internal** plugin port (`:9001`) — not publicly exposed. To scrape them, run Prometheus on the same Docker network or use an internal endpoint:

```
GET http://hatchway-server:9001/metrics
```

> **Trust boundary:** the plugin port is unauthenticated. Anything reachable on the `hatchway_internal` network (frps, Caddy, your Prometheus sidecar) sees tunnel counts, plugin op counters, and DB pool stats. Verify it never appears in any `ports:` block in `docker-compose.yml` or in your firewall rules. Putting Prometheus on the same Docker network is the supported pattern; exposing `:9001` to a host or VPC network is not.

Metrics exposed:

| Metric                                    | Type    | Description                                                                          |
| ----------------------------------------- | ------- | ------------------------------------------------------------------------------------ |
| `hatchway_plugin_ops_total`               | counter | frps plugin operations (Login, NewProxy, CloseProxy, NewUserConn, Ping, NewWorkConn) |
| `hatchway_plugin_deadline_exceeded_total` | counter | Plugin handler invocations that hit `HATCHWAY_PLUGIN_TIMEOUT`                        |
| `hatchway_rate_limit_rejections_total`    | counter | API rate limit rejections                                                            |
| `hatchway_tunnel_transitions_total`       | counter | State machine transitions                                                            |
| `hatchway_tunnels_by_status`              | gauge   | Current tunnels per status                                                           |
| `hatchway_db_pool_total_connections`      | gauge   | PostgreSQL pool total connections                                                    |
| `hatchway_db_pool_idle_connections`       | gauge   | PostgreSQL pool idle connections                                                     |

## Troubleshooting

### `308` redirect loop on API

Cloudflare SSL/TLS mode is set to "Flexible". Set it to **Full (strict)** in the Cloudflare dashboard.

### frps crash-loop: `unknown field "headers"`

frps does not support custom headers in `httpPlugins` config. The plugin secret should be embedded in the plugin callback URL path, not as a header. Ensure you're using the latest `frps.toml.tmpl`.

### hatchway-server crash-loop: `unknown command "hatchway"`

The Dockerfile ENTRYPOINT is already `/hatchway`. The docker-compose command should be `["server", "run"]`, not `["hatchway", "server", "run"]`.

### frpc: `i/o timeout` on port 7000

The cloud provider firewall is blocking port 7000. Add a firewall rule for tcp:7000 (see "Open firewall ports" above). Also verify that `frps.example.com` uses grey cloud (DNS only) if behind Cloudflare — Cloudflare cannot proxy raw TCP.

### frpc: `missing runtime token`

The `--metadatas` CLI flag sets proxy-level metas, not client-level metas that appear in the Login message. Use a TOML config file with `metadatas.runtime_token = "..."` (dotted-key format) instead.

### frpc: `invalid credentials`

The `auth.token` in `frpc.toml` must be the `HATCHWAY_FRPS_AUTH_TOKEN` (returned as `server_token` in the API response), not the `runtime_token` or `HATCHWAY_PLUGIN_SECRET`. Also verify the frps entrypoint is correctly substituting all environment variables.

### SSL handshake failure on tunnel URL

If `*.tunnel.example.com` is behind a Cloudflare proxy (orange cloud), Cloudflare's Universal SSL wildcard cert may not cover the subdomain. Switch to grey cloud (DNS only) and let Caddy serve the Let's Encrypt wildcard cert directly.

Also verify that `CLOUDFLARE_API_TOKEN` is set in `.env` — Caddy needs it for the DNS-01 challenge to obtain the wildcard certificate.

### Tunnels don't connect (502 errors)

1. Check frps is running: `docker compose logs frps`
2. Verify the plugin secret matches in `.env`
3. Check that frpc can reach `frps.example.com:7000`

### Certificate renewal fails

Caddy handles TLS automatically via ACME DNS-01 challenge. Ensure:
- Port 80 and 443 are open
- DNS records point to the correct IP
- The `*.tunnel.example.com` wildcard record exists
- `CLOUDFLARE_API_TOKEN` is set and valid

### Database connection errors

```bash
docker compose exec postgres pg_isready -U hatchway
docker compose logs postgres
```

### Plugin port accidentally exposed

The plugin port (`:9001`) must not be in the `ports:` section of `docker-compose.yml`. If it is, remove it — the plugin is only for internal frps communication.

### Reset everything

```bash
docker compose down -v   # removes containers AND volumes
docker compose up -d
docker compose run --rm hatchway-server server init --force
```

## Updating

```bash
git pull
docker compose up -d --build
```

Database migrations run automatically via `hatchway server run` on startup. For manual migration:

```bash
docker compose run --rm hatchway-server server init
```
