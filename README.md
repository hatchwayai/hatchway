<p align="center">
  <img src="https://raw.githubusercontent.com/zydo/hatchway/main/docs/assets/hatchway_logo.svg" alt="Hatchway" width="480">
</p>

[![CI](https://github.com/zydo/hatchway/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zydo/hatchway/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/zydo/hatchway)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

CLI-first, self-hosted public tunnel system powered by [frp](https://github.com/fatedier/frp).

Hatchway is designed for agent-native workflows: temporary public endpoints, scoped runtime credentials, automatic expiration, and zero-dashboard automation.

Expose any local HTTP service to the internet with a single command:

```
$ hatchway http 3000

Tunnel created: https://t-abc3x7km9w2p4rng.tunnel.example.com
```

## Who is this for?

Use Hatchway if you want:

- An ngrok-like tunnel you fully control — on your own domain, your own server, no third-party service.
- CLI-first ephemeral public URLs for dev tools, CI, webhooks, or AI agents.
- Per-tunnel TTL, quotas, and token-scoped access — not just an open relay.

Don't use Hatchway if you need a full zero-trust access platform, web dashboard, or managed cloud service.

## How it compares

|                   | Hosted | Self-hosted | CLI-first | TTL / quota | Per-tunnel token |
| ----------------- | ------ | ----------- | --------- | ----------- | ---------------- |
| ngrok             | yes    | no          | yes       | limited     | limited          |
| Cloudflare Tunnel | yes    | partial     | yes       | not core    | no               |
| frp (raw)         | no     | yes         | no        | no          | no               |
| zrok              | no     | yes         | yes       | yes         | limited          |
| **Hatchway**      | no     | **yes**     | **yes**   | **yes**     | **yes**          |

Hatchway is not a hosted service — you deploy it on your own VPS. It is not a general-purpose VPN or mesh network. It is a focused tunnel control plane: the CLI creates short-lived, token-scoped HTTP tunnels backed by frp as the data plane.

## Features

- **CLI-first** — no web dashboard needed. `hatchway http 3000` and you're live.
- **Self-hosted** — runs on your own infrastructure. No third-party tunnel service.
- **Token-authenticated** — API tokens (`sk_live_...`) control access. Runtime tokens (`rt_...`) are scoped to individual tunnels.
- **Subdomain-per-tunnel** — each tunnel gets `t-<id>.tunnel.example.com` with automatic HTTPS via Caddy.
- **TTL + quotas** — tunnels auto-expire; per-user concurrent-tunnel and rate limits prevent abuse.
- **Observability** — Prometheus metrics, structured JSON logging, health/ready endpoints.

## Security model

Hatchway uses a two-token architecture to separate control plane access from data plane auth:

- **API tokens** (`sk_live_...`) — long-lived, used to create, list, and delete tunnels via the REST API. Stored hashed with argon2id; never stored plaintext.
- **Runtime tokens** (`rt_...`) — short-lived, scoped to a single tunnel. Carried by frpc as metadata and validated by the server-side plugin on every `Login` and `NewProxy` callback. Expires with the tunnel.

Additional protections:

- Tunnels auto-expire when their TTL elapses. Expired tunnels stop accepting traffic — the runtime token is rejected at the next frpc reconnect.
- The frps plugin server (`:9001`) is **internal-only** — never exposed publicly, protected by a path-based secret on top of network isolation.
- The frps bootstrap token (`HATCHWAY_FRPS_AUTH_TOKEN`) is returned to API users in the tunnel-create response so frpc can authenticate to frps. It keeps random scanners off frps but is **not** the security boundary for tunnel ownership — the per-tunnel runtime token is. If it leaks, an attacker can probe frps but cannot register a tunnel they don't own. The plugin secret (`HATCHWAY_PLUGIN_SECRET`) is never returned via the API.
- Rate limits (default: 10 tunnel creates/min/token) and concurrent tunnel caps (default: 5/user) prevent abuse.
- Tunnel IDs use 16 random characters from a Crockford alphabet (~79 bits of entropy), making enumeration infeasible.

> **Token format note:** `sk_live_` is a naming convention (secret key, live), not related to Stripe. `rt_` stands for runtime token. Tunnel IDs use the `t-` prefix to ensure DNS-label compliance.

## Quick start (Docker)

### 1. Clone and configure

```bash
git clone https://github.com/zydo/hatchway.git
cd hatchway
cp .env.example .env
```

Edit `.env` and set the five required variables:

| Variable                   | Example                            | Purpose                                                                |
| -------------------------- | ---------------------------------- | ---------------------------------------------------------------------- |
| `HATCHWAY_DOMAIN`          | `example.com`                      | Your top-level domain                                                  |
| `POSTGRES_PASSWORD`        | (random string)                    | PostgreSQL password                                                    |
| `HATCHWAY_PLUGIN_SECRET`   | (output of `openssl rand -hex 32`) | frps→server plugin auth (server-internal, never returned to API users) |
| `HATCHWAY_FRPS_AUTH_TOKEN` | (output of `openssl rand -hex 32`) | frps↔frpc bootstrap secret (returned to API users for frpc config)     |
| `CLOUDFLARE_API_TOKEN`     | (Cloudflare API token)             | For Caddy DNS-01 wildcard certificate challenge                        |

### 2. DNS records

Create three A records pointing to your server's IP. If using Cloudflare:

| Record                 | Proxy mode     | Why                                                                                   |
| ---------------------- | -------------- | ------------------------------------------------------------------------------------- |
| `api.example.com`      | Orange cloud   | API endpoint, proxied via Cloudflare for DDoS protection                              |
| `frps.example.com`     | **Grey cloud** | frpc connects on raw TCP port 7000, which Cloudflare cannot proxy                     |
| `*.tunnel.example.com` | **Grey cloud** | Caddy serves the wildcard TLS cert directly; Cloudflare's Universal SSL is unreliable |

If using Cloudflare, set **SSL/TLS mode to "Full (strict)"** — "Flexible" causes an infinite redirect loop.

### 3. Firewall

Open ports 80, 443, and 7000 on your cloud provider's firewall. For example on GCP:

```bash
gcloud compute firewall-rules create allow-frps \
  --project YOUR_PROJECT --allow tcp:7000 \
  --direction INGRESS --source-ranges 0.0.0.0/0
```

### 4. Start the stack

```bash
docker compose up -d
```

### 5. Bootstrap

```bash
docker compose run --rm hatchway-server server init
```

This creates the database schema and prints your first admin API token. Save it.

> **Note:** Use `docker compose run --rm hatchway-server server init`, not `docker compose exec`. The container's ENTRYPOINT is already `hatchway`, so `exec` would require omitting the binary name.

### 6. Create a tunnel

Install the client (see [docs/cli.md](docs/cli.md) for binary downloads):

```bash
hatchway auth set-token --server https://api.example.com sk_live_...
hatchway http 3000
```

Any request to `https://t-<id>.tunnel.example.com` now hits `localhost:3000`.

Press Ctrl-C to tear down the tunnel.

## Architecture

```
Client (curl/browser)
  │
  ├─ HTTPS ──► Cloudflare (orange cloud) ──► Caddy:443 ──► hatchway-server:9000
  │             api.example.com                          (API endpoints)
  │
  └─ HTTPS ──► Caddy:443 ──► frps:8081 ──► frpc ──► localhost:8080
               *.tunnel.example.com      (vhost proxy)   (your local service)

frpc ──TCP:7000──► frps:7000
                    (control channel, grey cloud DNS)
```

- **API server** (`:9000`) — REST API for tunnel CRUD. Public behind Caddy.
- **Plugin server** (`:9001`) — frps plugin endpoint for auth decisions. **Not exposed** — only reachable on the internal Docker network. Auth via path-based secret (`/frp/plugin/{secret}`).
- **frps** (`:8081`) — receives tunnel traffic from the internet and proxies to frpc clients.
- **Caddy** — terminates TLS, routes `api.*` to the API server and `*.tunnel.*` to frps. Uses DNS-01 challenge with Cloudflare for the wildcard certificate.

## Build from source

Requires Go 1.22+.

```bash
make build    # builds dist/hatchway
make test     # runs all tests
make lint     # runs golangci-lint
```

For release builds with bundled frpc, see [`.goreleaser.yml`](.goreleaser.yml).

## Documentation

| Document                                             | Contents                                                         |
| ---------------------------------------------------- | ---------------------------------------------------------------- |
| [docs/self-host.md](docs/self-host.md)               | VPS setup, DNS, env vars, first-token bootstrap, troubleshooting |
| [docs/api.md](docs/api.md)                           | REST API endpoint reference                                      |
| [docs/cli.md](docs/cli.md)                           | CLI subcommand reference with examples                           |
| [DESIGN.md](DESIGN.md)                               | Full system design and rationale                                 |
| [docs/deployment-notes.md](docs/deployment-notes.md) | End-to-end deployment walkthrough and common issues              |

## License

Hatchway is [MIT licensed](LICENSE).

This project uses [frp](https://github.com/fatedier/frp) (Apache 2.0) as standalone binaries. See [THIRD_PARTY_LICENSES](THIRD_PARTY_LICENSES) for details.
