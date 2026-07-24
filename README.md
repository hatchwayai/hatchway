<p align="center">
  <img
    src="https://raw.githubusercontent.com/zydo/hatchway/main/docs/assets/hatchway_logo.svg"
    alt="Hatchway"
    width="480"
  >
</p>

[![CI](https://github.com/zydo/hatchway/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zydo/hatchway/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/zydo/hatchway)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

CLI-first, self-hosted public HTTP tunnels powered by [frp](https://github.com/fatedier/frp).

Hatchway is built for short-lived, scriptable workflows: expose a local HTTP
service, get a URL on your own domain, and let the tunnel expire or revoke it
when the work is done.

```console
$ hatchway http 3000
https://t-abc3x7km9w2p4rng.tunnel.example.com
```

## Who is this for?

Use Hatchway when you want:

- an ngrok-like HTTP tunnel on infrastructure and DNS you control;
- a CLI suitable for local development, CI, webhooks, and agent workflows;
- per-tunnel TTL and runtime credentials; and
- per-user concurrent-tunnel quotas plus per-token creation rate limits.

Hatchway is intentionally narrower than a VPN, zero-trust access platform,
managed tunnel service, or web dashboard. It adds a small control plane around
frp: Hatchway owns users, credentials, tunnel reservations, lifecycle, and
authorization; frp carries the traffic.

## Features

- **CLI-first** — `hatchway http 3000` creates and runs a tunnel without a dashboard.
- **Self-hosted** — PostgreSQL, Hatchway, frps, and Caddy run on your infrastructure.
- **Two credential scopes** — API tokens (`sk_live_...`) call the control API;
  runtime tokens (`rt_...`) authorize one tunnel.
- **Subdomain per tunnel** — URLs use
  `t-<random-id>.tunnel.example.com`, with wildcard HTTPS terminated by Caddy.
- **Bounded lifetime and usage** — TTL, a per-user non-terminal tunnel cap, and
  a per-token creation rate limit are enforced server-side.
- **Automation support** — selected CLI commands emit JSON, API creation is
  idempotent, and the client retries safe or explicitly idempotent requests.
- **Operations** — JSON request/application logs, Prometheus metrics, liveness,
  readiness, retention sweepers, and automatic database migrations at startup.

Only HTTP tunnels are implemented. The `tcp` and `udp` commands are visible as
explicitly unsupported placeholders.

## Security model

Hatchway separates control-plane access from data-plane registration:

- **API tokens** (`sk_live_...`) are long-lived and revocable. Only their
  prefix and a versioned SHA-256 digest are stored. The tokens contain enough
  random entropy for digest-based verification; comparison is constant-time.
  The verifier retains compatibility with legacy Argon2id rows so existing
  installations can upgrade without rotating every token immediately, while
  globally bounding concurrent legacy KDF work.
- **Runtime tokens** (`rt_...`) are minted per tunnel, stored the same way, and
  expire with that tunnel. frpc presents one as metadata; the internal frps
  plugin validates it during `Login` and `NewProxy`.
- **Request-time gating** in the bundled Caddy configuration checks tunnel
  state and TTL against PostgreSQL before every HTTP request reaches frps.
  Non-elapsed `active` and `closed` rows pass this check; reserved, expired,
  revoked, and unknown tunnels fail closed. `closed` is advisory because frps
  callbacks are asynchronous, and frps remains authoritative for whether a
  route actually exists. Requests already admitted, including upgraded
  connections, may drain naturally.

Additional boundaries:

- The internal listener (`:9001`) serves the frps callback, Caddy request gate,
  and metrics. Its callback/gate paths are protected by a shared path secret;
  do not publish the listener.
- `HATCHWAY_PLUGIN_SECRET` is shared only among Hatchway, frps, and Caddy. It
  also derives the encryption key for successful idempotency-response bodies
  cached in PostgreSQL.
- `HATCHWAY_FRPS_AUTH_TOKEN` is a shared frps↔frpc bootstrap credential and is
  returned in every tunnel-creation response. It deters unauthenticated
  scanners, but it is not the ownership boundary; the per-tunnel runtime token
  and plugin checks are.
- The defaults allow 10 creates/minute per API token, 5 non-terminal tunnels
  per user, and a maximum 24-hour TTL.
- Tunnel IDs contain 16 characters from a 31-symbol, ambiguity-free
  Crockford-style alphabet (about 79 random bits); the `t-` prefix makes them
  valid DNS labels.

The `sk_live_` spelling is only a naming convention and is unrelated to
Stripe. See [DESIGN.md](DESIGN.md) for the full trust model.

## Quick start with Docker

### 1. Configure

```bash
git clone https://github.com/zydo/hatchway.git
cd hatchway
cp .env.example .env
```

Set these values in `.env`:

| Variable | Purpose |
| --- | --- |
| `HATCHWAY_DOMAIN` | Top-level domain, for example `example.com` |
| `POSTGRES_PASSWORD` | PostgreSQL password |
| `HATCHWAY_PLUGIN_SECRET` | Internal frps→Hatchway callback secret |
| `HATCHWAY_FRPS_AUTH_TOKEN` | Shared frps↔frpc bootstrap credential |
| `CLOUDFLARE_API_TOKEN` | Caddy DNS-01 credential; grant Zone Read and DNS Edit for the zone |

Generate the two Hatchway secrets independently, for example with
`openssl rand -hex 32`, then protect the completed file with
`chmod 600 .env`.

### 2. Configure DNS and the firewall

Point these records at the server:

| Record | Cloudflare mode | Purpose |
| --- | --- | --- |
| `api.example.com` | proxied or DNS-only | Public control API |
| `frps.example.com` | **DNS-only** | Raw frpc TCP connection on port 7000 |
| `*.tunnel.example.com` | **DNS-only** | Wildcard HTTPS served by Caddy |

When Cloudflare proxies the API record, use **Full (strict)** SSL mode.
Open inbound TCP ports 80, 443, and 7000. Never expose 9001.

### 3. Start and bootstrap

```bash
docker compose up -d
docker compose run --rm hatchway-server server init
```

`server run` automatically applies embedded migrations before accepting
traffic. `server init` is still required once to create the bootstrap admin
and print its first API token. Save that token: plaintext tokens are shown
only when created.

The image entrypoint is already `hatchway`; therefore the Compose command is
`server init`, not `hatchway server init`.

### 4. Run a tunnel

Build or install the client, and put an executable `frpc` either next to the
`hatchway` binary or on `PATH`:

```bash
hatchway auth set-token --server https://api.example.com sk_live_...
hatchway http 3000
```

Requests to the printed URL now reach `127.0.0.1:3000`. Ctrl-C stops frpc and
the CLI makes a bounded cleanup request that revokes the tunnel. The same
cleanup runs after configuration failures, clean frpc exits, and exhausted
restart attempts.

## Architecture

```text
API client ──HTTPS──► Caddy ─────────► hatchway-server:9000 ──► PostgreSQL

Browser ──HTTPS──► Caddy ──auth──► hatchway-server:9001 ──► PostgreSQL
                     │ allow
                     ▼
                  frps:8081 ──► frpc ──► 127.0.0.1:<port>
                     ▲
frpc ───────TCP:7000─┘
```

- **Hatchway API (`:9000`)** — authenticated tunnel CRUD and `/v1/me`.
- **Hatchway internal listener (`:9001`)** — frps registration callbacks,
  Caddy request authorization, and metrics; it is not a public API.
- **frps (`:7000`, `:8081`)** — frpc control channel and HTTP vhost data plane.
- **Caddy (`:80`, `:443`)** — API and wildcard tunnel TLS/routing, with an
  authorization subrequest before each wildcard request.
- **PostgreSQL** — users, token digests, tunnel state, events, and encrypted
  idempotency replay records.

## Build from source

Requires Go 1.26.5 or a compatible newer Go release.

```bash
make build
make test
make lint
```

Integration tests start isolated PostgreSQL containers by default. To reuse a
local database, set `HATCHWAY_TEST_DATABASE_URL`; its parsed database name must
end in `_test`. These fixtures clear application tables and migration tests
recreate the `public` schema, so they deliberately ignore the service's
`DATABASE_URL`. Run shared-database tests serially with
`go test -p 1 ./...`.

`make build` writes `dist/hatchway`; provide `frpc` separately. The GoReleaser
configuration is prepared to place `hatchway` and `frpc` side by side in
client archives, but this repository currently has no published release tag.

## Documentation

| Document | Contents |
| --- | --- |
| [docs/self-host.md](docs/self-host.md) | Canonical operator setup and upgrade guide |
| [docs/api.md](docs/api.md) | Implemented REST API contract |
| [docs/cli.md](docs/cli.md) | Implemented client and server commands |
| [DESIGN.md](DESIGN.md) | Current architecture, invariants, and future boundaries |
| [PLAN.md](PLAN.md) | Historical roadmap and remaining release work |
| [docs/source-reading-guide.md](docs/source-reading-guide.md) | Top-down code-reading path |
| [docs/extending.md](docs/extending.md) | Supported integration surface and current gaps |
| [docs/deployment-notes.md](docs/deployment-notes.md) | Concise deployment checklist |

## License

Hatchway is [MIT licensed](LICENSE).

This project uses [frp](https://github.com/fatedier/frp) (Apache 2.0) as
standalone binaries. See [THIRD_PARTY_LICENSES](THIRD_PARTY_LICENSES).
