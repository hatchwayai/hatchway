# Hatchway

[![CI](https://github.com/zydo/hatchway/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zydo/hatchway/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/zydo/hatchway)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

CLI-first, self-hosted public tunnel system powered by [frp](https://github.com/fatedier/frp).

Expose any local HTTP service to the internet with a single command:

```
$ hatchway http 3000

Tunnel created: https://t-abc3x7km9w2p4rng.tunnel.example.com
```

## Features

- **CLI-first** — no web dashboard needed. `hatchway http 3000` and you're live.
- **Self-hosted** — runs on your own infrastructure. No third-party tunnel service.
- **Token-authenticated** — API tokens (`sk_live_...`) control access. Runtime tokens (`rt_...`) are scoped to individual tunnels.
- **Subdomain-per-tunnel** — each tunnel gets `t-<id>.tunnel.example.com` with automatic HTTPS via Caddy.
- **TTL + quotas** — tunnels auto-expire; per-user concurrent-tunnel and rate limits prevent abuse.
- **Observability** — Prometheus metrics, structured JSON logging, health/ready endpoints.

## Quick start (Docker)

### 1. Clone and configure

```bash
git clone https://github.com/zydo/hatchway.git
cd hatchway
cp .env.example .env
```

Edit `.env` and set the four required variables:

| Variable                   | Example                            | Purpose                                                                |
| -------------------------- | ---------------------------------- | ---------------------------------------------------------------------- |
| `HATCHWAY_DOMAIN`          | `example.com`                      | Your top-level domain                                                  |
| `POSTGRES_PASSWORD`        | (random string)                    | PostgreSQL password                                                    |
| `HATCHWAY_PLUGIN_SECRET`   | (output of `openssl rand -hex 32`) | frps→server plugin auth (server-internal, never returned to API users) |
| `HATCHWAY_FRPS_AUTH_TOKEN` | (output of `openssl rand -hex 32`) | frps↔frpc bootstrap secret (returned to API users for frpc config)     |

### 2. DNS records

Create three A records pointing to your server's IP:

```
api.example.com        A   <your-ip>
frps.example.com       A   <your-ip>
*.tunnel.example.com   A   <your-ip>
```

### 3. Start the stack

```bash
docker compose up -d
```

### 4. Bootstrap

```bash
docker compose exec hatchway-server hatchway server init
```

This creates the database schema and prints your first admin API token. Save it.

### 5. Create a tunnel

Install the client (see [docs/cli.md](docs/cli.md) for binary downloads):

```bash
hatchway auth set-token --server https://api.example.com sk_live_...
hatchway http 3000
```

Any request to `https://t-<id>.tunnel.example.com` now hits `localhost:3000`.

Press Ctrl-C to tear down the tunnel.

## Architecture

```
                      ┌──────────────────┐
                      │   Caddy (443)    │
                      │  TLS termination │
                      └──────┬───────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
      api.example.com     .tunnel.*     (internal)
              │              │              │
      ┌───────▼──────┐  ┌────▼─────┐  ┌─────▼──────────┐
      │ hatchway     │  │  frps    │  │ hatchway       │
      │ API (:9000)  │  │ (:8081)  │  │ plugin (:9001) │
      └───────┬──────┘  └───┬──────┘  └─────┬──────────┘
              │             │               │
              └─────────────┼───────────────┘
                            │
                    ┌───────▼────────┐
                    │  PostgreSQL    │
                    └────────────────┘
```

- **API server** (`:9000`) — REST API for tunnel CRUD. Public behind Caddy.
- **Plugin server** (`:9001`) — frps plugin endpoint for auth decisions. **Not exposed** — only reachable on the internal Docker network.
- **frps** (`:8081`) — receives tunnel traffic from the internet and proxies to frpc clients.
- **Caddy** — terminates TLS, routes `api.*` to the API server and `*.tunnel.*` to frps.

## Build from source

Requires Go 1.22+.

```bash
make build    # builds dist/hatchway
make test     # runs all tests
make lint     # runs golangci-lint
```

For release builds with bundled frpc, see [`.goreleaser.yml`](.goreleaser.yml).

## Documentation

| Document                               | Contents                                                         |
| -------------------------------------- | ---------------------------------------------------------------- |
| [docs/self-host.md](docs/self-host.md) | VPS setup, DNS, env vars, first-token bootstrap, troubleshooting |
| [docs/api.md](docs/api.md)             | REST API endpoint reference                                      |
| [docs/cli.md](docs/cli.md)             | CLI subcommand reference with examples                           |
| [DESIGN.md](DESIGN.md)                 | Full system design and rationale                                 |

## License

Hatchway is [MIT licensed](LICENSE).

This project uses [frp](https://github.com/fatedier/frp) (Apache 2.0) as standalone binaries. See [THIRD_PARTY_LICENSES](THIRD_PARTY_LICENSES) for details.
