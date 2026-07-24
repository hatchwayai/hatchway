# Self-hosting Hatchway

This is the canonical guide for the bundled Docker Compose deployment:
PostgreSQL, Hatchway, frps, and Caddy on one host.

## Prerequisites

- A Linux host with a public IP
- Docker Engine with the Compose plugin
- A domain you control
- Cloudflare DNS for the bundled Caddy image, or equivalent custom Caddy
  configuration for another DNS provider
- A Cloudflare API token scoped to the zone with **Zone Read** and **DNS Edit**

Go is needed only when building the client or server outside Docker.

## Network and DNS

Open these inbound TCP ports:

| Port | Consumer | Purpose |
| --- | --- | --- |
| 80 | Caddy | ACME/HTTP redirect |
| 443 | Caddy | Public API and tunnel HTTPS |
| 7000 | frps | frpc control/data connection |

Do not expose 9000 directly when Caddy is the public API entrypoint. Never
expose port 9001: it contains the frps callback, Caddy request-authorization
endpoint, and Prometheus metrics, all intended only for the private Compose
network. frps's HTTP vhost port 8081 is also private and reached through
Caddy.

Create three A/AAAA records:

| Record | Destination | Cloudflare mode |
| --- | --- | --- |
| `api.example.com` | server | Proxied or DNS-only |
| `frps.example.com` | server | **DNS-only** |
| `*.tunnel.example.com` | server | **DNS-only** |

The normal Cloudflare proxy cannot carry arbitrary raw TCP on port 7000.
Caddy obtains and serves the wildcard tunnel certificate, so keep the wildcard
record DNS-only as well. If the API record is proxied, select Cloudflare
**Full (strict)** SSL/TLS mode; “Flexible” creates an HTTP→HTTPS redirect loop.

## Configure

```bash
git clone https://github.com/zydo/hatchway.git
cd hatchway
cp .env.example .env
```

Set these five values:

```dotenv
HATCHWAY_DOMAIN=example.com
POSTGRES_PASSWORD=<generated-password>
HATCHWAY_PLUGIN_SECRET=<generated-secret>
HATCHWAY_FRPS_AUTH_TOKEN=<different-generated-secret>
CLOUDFLARE_API_TOKEN=<scoped-cloudflare-token>
```

Generate the values in your shell and paste the outputs:

```bash
openssl rand -hex 24
openssl rand -hex 32
openssl rand -hex 32
```

Do not put a literal `$(openssl ...)` expression in `.env`; Compose does not
execute dotenv values.

Restrict the completed file because it contains database and service secrets:

```bash
chmod 600 .env
```

The optional domain variables may be left empty to derive
`api.${HATCHWAY_DOMAIN}`, `frps.${HATCHWAY_DOMAIN}`, and
`tunnel.${HATCHWAY_DOMAIN}`, or set explicitly:

```dotenv
HATCHWAY_API_DOMAIN=api.example.com
HATCHWAY_FRPS_DOMAIN=frps.example.com
HATCHWAY_TUNNEL_DOMAIN=tunnel.example.com
```

### Secret roles

Keep the two Hatchway secrets distinct:

- `HATCHWAY_PLUGIN_SECRET` is embedded in the internal frps callback and Caddy
  request-gate paths and is never returned through the API. Hatchway also
  derives the AES-GCM key for cached idempotency response bodies from it.
- `HATCHWAY_FRPS_AUTH_TOKEN` is the shared frps authentication token. Tunnel
  creators receive it as `frp.server_token`; it is a bootstrap barrier, not
  the tunnel-ownership boundary.

Both values must contain 32–256 URL-safe characters: ASCII letters, digits,
underscores, or hyphens. The hexadecimal `openssl` examples satisfy this
constraint.

Per-tunnel `rt_...` credentials enforce ownership in the plugin. They are
returned once during tunnel creation, stored only as digests, and expire with
their tunnel.

Rotating the plugin secret requires a coordinated restart of Hatchway, frps,
and Caddy. It also prevents decryption of cached idempotency responses written
under the old value. Prefer to wait for
`HATCHWAY_IDEMPOTENCY_RETENTION_HOURS` plus one hourly sweeper interval before
rotation, or plan for clients to retry affected requests with new keys.

### Optional settings

The Compose file passes these `.env` settings into the server:

```dotenv
POSTGRES_DB=hatchway
POSTGRES_USER=hatchway

HATCHWAY_MAX_CONCURRENT_TUNNELS=5
HATCHWAY_MAX_TTL=24h
HATCHWAY_RATE_CREATE_PER_MIN=10
HATCHWAY_MAX_REQUEST_BYTES=65536

HATCHWAY_API_READ_TIMEOUT=30s
HATCHWAY_API_WRITE_TIMEOUT=30s
HATCHWAY_PLUGIN_TIMEOUT=2s

HATCHWAY_LOG_USER_CONNS=false
HATCHWAY_EVENTS_RETENTION_DAYS=30
HATCHWAY_IDEMPOTENCY_RETENTION_HOURS=24
HATCHWAY_RUNTIME_TOKEN_RETENTION_DAYS=7
```

Invalid integer, boolean, or Go duration values stop startup. The concurrent
limit is per user and counts `reserved`, `active`, and `closed` tunnels. The
creation rate is per API token and per Hatchway process.

## Deploy

```bash
docker compose up -d --build
docker compose ps
```

`hatchway server run` applies embedded migrations before accepting traffic,
then verifies the required schema. The current schema history is `0001`
through `0006`. A failed migration stops the server rather than serving
against an incompatible schema.

The bundled services use health checks and dependency conditions:

- PostgreSQL checks the configured database;
- Hatchway runs `server healthcheck` against its internal `/readyz`;
- frps verifies its rendered configuration and process; and
- Caddy checks its admin endpoint and Hatchway readiness.

Application containers use read-only filesystems where practical, dropped
capabilities, PID limits, and rotated Docker JSON logs. The internal Hatchway
listener and frps vhost port are not host-published. PostgreSQL is additionally
isolated on a backend network joined only by the Hatchway server.

The bundled Caddyfile performs a `forward_auth` subrequest before every
wildcard tunnel request and sends traffic to frps only after Hatchway returns
`204`. This is required for immediate HTTP revocation and expiry. If you
replace Caddy, preserve the equivalent fail-closed request gate; routing
`*.tunnel.example.com` directly to frps bypasses it.

### Bootstrap the first admin

After PostgreSQL and Hatchway are healthy:

```bash
docker compose run --rm hatchway-server \
  server init --admin-email admin@example.com --admin-name admin
```

The image entrypoint is `/hatchway`, so the argument starts with `server`; do
not write `hatchway server init` after the service name.

`server init` also runs migrations, then creates an admin and prints a
one-time plaintext API token. Save it securely. Re-running without `--force`
refuses when users exist. `--force` is additive after confirmation: it does
not reset data or revoke existing tokens, and therefore needs a unique email.

### Verify from outside

```bash
curl -fsS https://api.example.com/healthz
curl -fsS https://api.example.com/readyz
curl -fsS \
  -H "Authorization: Bearer sk_live_..." \
  https://api.example.com/v1/me
```

Expected `/v1/me` response:

```json
{
  "user_id": "550e8400-e29b-41d4-a716-446655440000",
  "is_admin": true
}
```

You can also run the readiness probe inside the image:

```bash
docker compose run --rm hatchway-server \
  server healthcheck --url http://hatchway-server:9000/readyz --timeout 3s
```

## Create the first tunnel

Install the client and an executable frpc v0.69.0. `hatchway` searches for
frpc first beside its own executable, then on `PATH`.

```bash
hatchway auth set-token \
  --server https://api.example.com \
  sk_live_...

hatchway auth whoami
```

Start a local service:

```bash
python3 -m http.server 3000
```

In another terminal:

```bash
hatchway http 3000 --ttl 15m
```

The printed HTTPS URL should reach the local process. The client generates a
temporary `frpc.toml`, runs frpc, restarts it up to three times after
unexpected failures, and revokes the tunnel when the command exits. Ctrl-C is
therefore the normal teardown.

To run frpc manually instead, call `POST /v1/tunnels` and use the returned
fields:

```toml
serverAddr = "frps.example.com"
serverPort = 7000

auth.method = "token"
auth.token = "FRP_SERVER_TOKEN_FROM_RESPONSE"

metadatas.runtime_token = "RUNTIME_TOKEN_FROM_RESPONSE"

[[proxies]]
name = "TUNNEL_ID"
type = "http"
localIP = "127.0.0.1"
localPort = 3000
subdomain = "TUNNEL_ID"
```

`name` and `subdomain` must equal the issued tunnel ID. Client-level metadata
must use `metadatas.runtime_token`; a proxy-level metadata flag does not place
the credential in frps's `Login` callback.

## Users, tokens, and tunnels

These commands access the database as a trusted host operator:

```bash
docker compose run --rm hatchway-server \
  server user create --email alice@example.com --name Alice

docker compose run --rm hatchway-server server user list

docker compose run --rm hatchway-server \
  server token create --user alice@example.com --name laptop

docker compose run --rm hatchway-server \
  server token list --user alice@example.com

docker compose run --rm hatchway-server \
  server token revoke <full-token-uuid>

docker compose run --rm hatchway-server server tunnels
```

`token create --user` accepts UUID, email, or unique name and prints both the
token UUID and one-time plaintext value. Use `token list` to rediscover IDs;
the token body cannot be recovered.

End users should use owner-scoped API/CLI operations:

```bash
hatchway list
hatchway delete <tunnel-id>
```

`list` includes terminal rows. `delete` means revoke, not physical deletion.
New user connections are rejected after revocation or TTL expiry, including
when frps still has a registered proxy. Connections already accepted can
drain naturally.

An admin API token may revoke any tunnel:

```bash
curl -X POST \
  -H "Authorization: Bearer sk_live_<admin-token>" \
  https://api.example.com/v1/admin/tunnels/<tunnel-id>/revoke
```

That is the only current public admin route. The server-side user/token/list
commands are not exposed over HTTP.

## Monitoring

Public health endpoints:

- `/healthz` — process liveness, plain-text `ok`;
- `/readyz` — database ping plus required-schema check.

The internal `http://hatchway-server:9001/metrics` endpoint exposes:

| Metric | Type |
| --- | --- |
| `hatchway_plugin_ops_total{op=...}` | counter |
| `hatchway_plugin_deadline_exceeded_total` | counter |
| `hatchway_rate_limit_rejections_total` | counter |
| `hatchway_tunnel_transitions_total` | counter |
| `hatchway_tunnels_by_status{status=...}` | gauge |
| `hatchway_db_pool_total_connections` | gauge |
| `hatchway_db_pool_idle_connections` | gauge |

Put a Prometheus scraper on `hatchway_internal`; do not publish 9001. Hatchway
logs one JSON object per line. Completed API request logs include request ID,
method, path, status, latency, and authenticated token ID.

## Retention and expiry

The server starts:

- a 30-second reaper that transitions elapsed non-terminal tunnels to
  `expired`; and
- hourly sweepers for old events, idempotency rows, and dead runtime-token
  rows.

The Caddy request gate compares `expires_at` against PostgreSQL time before
each wildcard HTTP request, so the reaper interval does not grant an extra 30
seconds of new access.

## Troubleshooting

### API redirect loop

Set a proxied Cloudflare API record to **Full (strict)**, not Flexible.

### Wildcard certificate failure

Confirm the Cloudflare token has both Zone Read and DNS Edit on the correct
zone, the wildcard record exists, and Caddy received the token. Keep
`*.tunnel` DNS-only.

### frpc times out on port 7000

Open the provider/host firewall and keep the frps DNS record DNS-only:

```bash
nc -vz frps.example.com 7000
```

### frpc reports missing or invalid credentials

- `auth.token` must be the create response's `frp.server_token`.
- `metadatas.runtime_token` must be the create response's `runtime_token`.
- Never put `HATCHWAY_PLUGIN_SECRET` in a client config.
- Confirm `name` and `subdomain` equal the tunnel ID.

### Tunnel URL returns 502

Check all sides:

```bash
docker compose ps
docker compose logs hatchway-server frps caddy
hatchway list
```

Confirm the local service is still listening and frpc is still running.
Expired/revoked tunnels intentionally reject new connections.

### Readiness fails

```bash
docker compose logs postgres hatchway-server
docker compose exec postgres sh -c \
  'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
```

A migration error or missing table makes `/readyz` fail.

## Back up and upgrade

Back up PostgreSQL before an upgrade:

```bash
docker compose exec -T postgres \
  sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
  > hatchway-backup.sql
```

Then:

```bash
git pull
docker compose up -d --build
curl -fsS https://api.example.com/readyz
```

No separate migration command is required; `server run` migrates on startup.
Do not use `server init` as a manual migration command because it also attempts
to create an admin.

To deliberately destroy a development deployment, `docker compose down -v`
removes the PostgreSQL and Caddy volumes. That data is not recoverable unless
you made a backup.
