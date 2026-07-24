# Deployment Notes

This is a concise field checklist. [self-host.md](self-host.md) is the
canonical, detailed operator guide; [api.md](api.md) and [cli.md](cli.md)
define behavior.

## Before deployment

- Point `api.<domain>`, `frps.<domain>`, and `*.tunnel.<domain>` at the host.
- Keep the frps and wildcard records DNS-only when using Cloudflare. Raw TCP
  port 7000 cannot use the normal Cloudflare HTTP proxy.
- Use Cloudflare **Full (strict)** mode if the API record is proxied.
- Give Caddy's Cloudflare token only **Zone Read** and **DNS Edit** for the
  relevant zone.
- Open TCP 80, 443, and 7000. Do not publish Hatchway's internal port 9001.

## Secrets and `.env`

Start from `.env.example`. Generate values in a shell and paste the resulting
strings into `.env`:

```bash
openssl rand -hex 24  # POSTGRES_PASSWORD
openssl rand -hex 32  # HATCHWAY_PLUGIN_SECRET
openssl rand -hex 32  # HATCHWAY_FRPS_AUTH_TOKEN
```

Do not put `$(openssl ...)` in `.env`; Compose dotenv files do not execute
shell command substitution.

Run `chmod 600 .env` after filling it because the file contains database and
service secrets.

The two Hatchway secrets must be different:

- `HATCHWAY_PLUGIN_SECRET` remains internal to frps, Caddy, and Hatchway. It
  protects callback/request-gate paths and derives the encryption key for
  idempotency cache bodies.
- `HATCHWAY_FRPS_AUTH_TOKEN` is deliberately returned to tunnel creators as
  `frp.server_token`. The per-tunnel runtime token is the ownership credential.

Rotating the plugin secret requires updating frps, Caddy, and Hatchway
together. It also makes idempotency bodies encrypted with the previous value
unreadable; prefer to wait for the configured replay-retention window plus one
hourly sweeper interval before rotation, or accept that affected clients must
retry with new keys.

## Deploy and bootstrap

```bash
docker compose up -d --build
docker compose ps
docker compose run --rm hatchway-server \
  server init --admin-email admin@example.com
```

The container entrypoint is already `/hatchway`, so do not repeat the binary
name. Save the one-time API token printed by `server init`.

`server run` applies all embedded migrations automatically before opening
listeners, including upgrades through migration `0006`. `server init` also
applies migrations, but its distinct purpose is creating a bootstrap admin.
It is not a migration-only command.

`server init --force` adds another admin after confirmation; it does not reset
the database or revoke existing tokens. Supply a unique email.

## Verify

```bash
curl -fsS https://api.example.com/healthz
curl -fsS https://api.example.com/readyz
curl -fsS \
  -H "Authorization: Bearer sk_live_..." \
  https://api.example.com/v1/me
```

`readyz` checks both database reachability and required schema tables. Unknown
routes, invalid methods, and API failures use JSON error envelopes. Server
request/application logs are JSON and carry request IDs.

## End-to-end test

Install an executable frpc v0.69.0 next to `hatchway` or on `PATH`, then:

```bash
hatchway auth set-token \
  --server https://api.example.com \
  sk_live_...

python3 -m http.server 3000
```

In another terminal:

```bash
hatchway http 3000 --ttl 15m
```

The printed HTTPS URL should reach the local server. Ctrl-C stops frpc and
revokes the tunnel. Caddy checks Hatchway before every wildcard HTTP request,
so revocation and TTL expiry take effect even if frps still has a proxy
registration; requests already admitted, including upgraded connections, may
drain.

## Operator checks

```bash
docker compose run --rm hatchway-server server user list
docker compose run --rm hatchway-server server token list
docker compose run --rm hatchway-server server tunnels
docker compose logs hatchway-server frps caddy
```

Use `server token list` to obtain a complete token ID before
`server token revoke <token-id>`. The end-user `hatchway list` command returns
all owned records, including expired and revoked tunnels.

Prometheus metrics are available only on the internal
`http://hatchway-server:9001/metrics` endpoint. Scrape it from the Compose
network; do not expose it through Caddy or a host port.

## Upgrade

```bash
git pull
docker compose up -d --build
curl -fsS https://api.example.com/readyz
```

Take a PostgreSQL backup before upgrading. Startup aborts if a migration
fails, and readiness stays false if the expected schema is unavailable.
