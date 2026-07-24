# Hatchway: CLI-first Self-hosted HTTP Tunnels

## Document status

This document describes the current implementation contract as reconciled on
2026-07-24. Statements under **Future work** are design direction only and are
not implemented APIs. `PLAN.md` is a historical roadmap, not a second
behavioral specification.

Endpoint and command details live in `docs/api.md` and `docs/cli.md`.
Deployment instructions live in `docs/self-host.md`.

## Product position

Hatchway is a small, self-hosted control plane for short-lived public HTTP
tunnels. A user starts a local service and runs:

```bash
hatchway auth set-token \
  --server https://api.example.com \
  sk_live_...
hatchway http 3000 --ttl 15m
```

The CLI reserves a random subdomain, starts frpc, and prints the public HTTPS
URL. Hatchway owns identity, credentials, allocation, quotas, lifecycle, and
authorization. [frp](https://github.com/fatedier/frp) owns traffic transport.

The system is aimed at local development, webhook testing, CI, and agent
workflows. It is not a hosted service, VPN, mesh network, zero-trust access
suite, or dashboard product.

## Current goals

- One native `hatchway` binary for clients and server operators.
- One command to expose a localhost HTTP port.
- Random, DNS-safe, server-assigned subdomains.
- Bounded tunnel lifetime and server-side revocation.
- Long-lived API credentials separated from per-tunnel runtime credentials.
- Owner-scoped API access with an explicit, narrow admin capability.
- Race-safe per-user quotas and per-token creation rate limits.
- PostgreSQL durability and embedded migrations.
- Scriptable success output and idempotent creation.
- A conservative single-host Docker Compose deployment.
- Useful operations signals without a public admin dashboard.

## Current non-goals

- TCP or UDP tunnels
- persistent/no-expiry tunnels
- user-selected tunnel IDs or custom domains
- public user signup or public token administration
- forced termination of sockets already accepted by frps
- multi-region/high-availability orchestration
- distributed rate limiting
- OAuth, SSO, MFA, organizations, billing, or a web UI
- importing Hatchway as a Go library

## System architecture

```text
                              CONTROL PLANE

CLI / API caller ──HTTPS──► Caddy ───────► hatchway-server:9000 ──► PostgreSQL
                                           │
                                           ├─ migrations + schema check
                                           ├─ API/auth/quota/lifecycle
                                           └─ reaper + retention sweepers

                               DATA PLANE

Browser ──HTTPS──► Caddy ──auth request──► hatchway-server:9001 ──► PostgreSQL
                     │ allow
                     ▼
                  frps:8081 ──► frpc ──► 127.0.0.1:<port>
                     ▲
frpc ───────TCP:7000─┘
```

### Components

#### Hatchway client

- Resolves an API origin and `sk_live_...` token.
- Checks the local port before allocating server state.
- Creates one HTTP tunnel with an idempotency key.
- Writes a temporary frpc configuration.
- Finds frpc beside the Hatchway executable, then on `PATH`.
- Supervises frpc and revokes the reservation whenever the command exits.

#### Hatchway public API (`:9000`)

- Serves `/healthz`, `/readyz`, and `/v1/*`.
- Authenticates API tokens and enforces owner/admin scope.
- Creates, lists, reads, and revokes tunnels.
- Emits JSON errors and JSON request/application logs.

#### Hatchway internal listener (`:9001`)

- Serves the frps plugin path `/frp/plugin/{secret}`.
- Serves Caddy's HTTP request gate at
  `/internal/tunnels/authorize/{secret}`.
- Serves Prometheus metrics at `/metrics`.
- Is not published or routed by the bundled deployment.

#### frps

- Accepts frpc connections on port 7000.
- Serves HTTP vhosts internally on 8081.
- Calls Hatchway for configured plugin operations. `Login` and `NewProxy`
  establish tunnel ownership; HTTP access does not depend on `NewUserConn`,
  which frp does not emit for HTTP proxies.

#### Caddy

- Terminates API and wildcard tunnel HTTPS.
- Routes the API hostname to 9000.
- Authorizes every wildcard HTTP request through Hatchway before routing it
  to frps:8081.
- Uses DNS-01 for the wildcard certificate in the bundled Cloudflare build.

#### PostgreSQL

- Stores users, token prefixes/digests, tunnel state, runtime-credential
  metadata, lifecycle events, and idempotency records.

## Trust boundaries

```text
Public internet
  ├─ Caddy :80/:443
  └─ frps :7000

Private service network
  ├─ Hatchway API :9000 (reached publicly only through Caddy)
  ├─ Hatchway callbacks/request gate/metrics :9001
  └─ frps vhost HTTP :8081

Isolated database network
  ├─ Hatchway
  └─ PostgreSQL :5432
```

The internal port is a high-trust interface. Its protected paths are defense
in depth, not permission to expose it. Metrics on that listener are
unauthenticated and may reveal aggregate tunnel and database-pool state.

Server-side `hatchway server ...` administration reads PostgreSQL directly.
Anyone able to invoke those commands with `DATABASE_URL` is a trusted
operator and can act across users. End-user tools must use the public API.

## Core invariants

1. Hatchway is authoritative for tunnel allocation and lifecycle; frp is not.
2. API tokens authorize control-plane operations; runtime tokens authorize
   exactly one data-plane registration.
3. The shared frps authentication token is not the ownership boundary.
4. Tunnel IDs, proxy names, and HTTP subdomains are the same server-issued
   value.
5. Users cannot choose custom domains or arbitrary proxy types.
6. Owner-scoped reads do not reveal whether another user's tunnel exists.
7. `expired` and `revoked` are terminal database states.
8. Revocation is a state transition, not row deletion.
9. A terminal or TTL-elapsed tunnel cannot accept a new user connection.
10. Accepted connections may drain; Hatchway does not kill their sockets.
11. Per-user quota checks and allocation occur under one serialized
    transaction.
12. Plaintext bearer/runtime tokens are never stored as token columns.
13. Successful idempotency bodies containing creation secrets are encrypted at
    rest.
14. The plugin secret never appears in an API response.
15. Startup does not serve against an old, missing, or dirty schema.

## Repository and packaging

```text
cmd/hatchway/                 process entrypoint
internal/
  cli/                        credentials, typed HTTP client, Cobra commands
  config/                     environment loading and validation
  db/                         pgx pool, schema check, embedded migrations
  frp/                        optional server subprocess supervision
  models/                     database-facing structs
  server/
    api/                      middleware, listeners, errors, metrics
    plugin/                   frps callback protocol and authorization
    tunnels/                  CRUD, quota, lifecycle, reaper, sweepers
  tokens/                     mint, digest, and legacy verification
docs/                         API, CLI, deployment, extension, source guides
docker-compose.yml            production-oriented single-host topology
Caddyfile                     public HTTP routing/TLS
frps.toml.tmpl                rendered frps configuration
scripts/                      verified frp download, smoke test, entrypoint
```

Everything under `internal/` is intentionally unavailable to external Go
modules. The stable extension surface is HTTP and, secondarily, documented
CLI success output.

The project pins frp v0.69.0. The download and frps-image paths verify known
SHA-256 checksums for supported architectures. GoReleaser is configured to
produce:

- client archives containing sibling `hatchway` and `frpc` executables; and
- server archives containing the Hatchway executable.

Archives include the README and license notices. No release tag has been
published yet, so source builds are currently the concrete installation path.

## Deployment models

### External frps: current production model

The bundled Compose stack runs:

- PostgreSQL 16;
- a non-root Hatchway server;
- a non-root, checksum-verified frps image; and
- a Cloudflare-DNS-enabled Caddy.

Only 80, 443, and 7000 are published. The stack uses health-gated dependencies,
graceful stop windows, bounded Docker log rotation, read-only filesystems,
dropped capabilities, PID limits, and tmpfs scratch/config storage where
applicable.

`server run` automatically applies embedded migrations, connects to
PostgreSQL, verifies schema version 6 is clean/current, starts background
workers, then opens listeners.

### Subprocess frps: implemented alternate/development model

`hatchway server run --dev` selects subprocess mode. The server runs the
configured `HATCHWAY_FRPS_BIN_PATH` with
`HATCHWAY_FRPS_CONFIG_PATH`. It does not generate that server configuration.
An unexpected frps exit cancels the server; server shutdown stops frps.

### Client frpc model

The client always uses a subprocess. It validates every required field in the
create response, writes a mode-`0600` temporary configuration, and launches:

```bash
frpc -c <temporary-path>
```

The executable search order is:

1. executable sibling named `frpc`;
2. `frpc` found through `PATH`.

The CLI restarts unexpected failures up to three times with 2, 4, and 6 second
delays. A deferred three-second API cleanup runs after every post-allocation
exit, including clean frpc exit, signal, configuration/write/start failure, or
exhausted restarts.

## Configuration

The server reads environment variables only; it has no application config
file. Typed parsing is strict, so invalid values abort configuration loading.
Required `server run` values are:

- `DATABASE_URL`
- `HATCHWAY_PLUGIN_SECRET`
- `HATCHWAY_FRPS_AUTH_TOKEN`
- `HATCHWAY_FRPS_DOMAIN`

`HATCHWAY_TUNNEL_DOMAIN` has a development placeholder default but production
deployments must set/derive their real domain. Other important defaults:

| Setting | Default |
| --- | --- |
| API/plugin addresses | `:9000` / `:9001` |
| API read/write timeout | `30s` / `30s` |
| request body limit | 65,536 bytes |
| plugin deadline | `2s` |
| non-terminal tunnels per user | 5 |
| maximum TTL | `24h` |
| creates per minute/token/process | 10 |
| connection-event logging | false |
| event retention | 30 days |
| idempotency retention | 24 hours |
| dead runtime-token retention | 7 days |

`HATCHWAY_MAX_TTL` must be at least one second. When a request omits
`ttl_seconds` or sends zero, the server chooses the lower of one hour and the
configured maximum.

Compose additionally consumes the base/domain, PostgreSQL, and Cloudflare
variables documented in `docs/self-host.md`.

## Tunnel IDs and protocol scope

Tunnel IDs use:

```text
t-<16 random characters>
```

The lowercase Crockford-style alphabet excludes visually ambiguous
characters. Sixteen selections from 31 characters provide about 79 random bits.
Creation retries a database collision up to three times.

Only HTTP is currently accepted. A tunnel URL is:

```text
https://<tunnel-id>.<HATCHWAY_TUNNEL_DOMAIN>
```

The server restricts `local_host` to `127.0.0.1` or `localhost` and port to
1–65535. The CLI always chooses `127.0.0.1`.

## Creation flow

```text
hatchway http
  │
  ├─ check localhost port
  ├─ POST /v1/tunnels + fresh Idempotency-Key
  │    ├─ authenticate API token
  │    ├─ rate limit token
  │    ├─ reserve idempotency record
  │    ├─ validate HTTP/local endpoint/TTL
  │    ├─ mint runtime token
  │    ├─ lock this user's quota
  │    ├─ count non-terminal tunnels
  │    ├─ insert tunnel + runtime-token digest + created event
  │    └─ return tunnel/runtime/frp fields
  ├─ write frpc.toml
  ├─ start frpc
  └─ DELETE tunnel during command teardown
```

The client sends `ttl_seconds` only when `--ttl` is supplied; otherwise the
server chooses its configured default. The create transaction makes quota
checking and slot consumption atomic for one user with
`pg_advisory_xact_lock(hashtextextended(user_id, 0))`.

## Lifecycle

| State | Meaning |
| --- | --- |
| `reserved` | Row and runtime credential exist; proxy is not registered |
| `active` | Hatchway accepted a NewProxy callback; data-plane liveness is advisory |
| `closed` | A CloseProxy callback was observed; reconnect remains possible and data-plane liveness is advisory |
| `expired` | TTL elapsed; terminal |
| `revoked` | User/admin revoked; terminal |

```text
reserved ──NewProxy──► active ──CloseProxy──► closed
                          ▲                     │
                          └──── NewProxy ───────┘

reserved | active | closed ──Expire──► expired
reserved | active | closed ──Revoke──► revoked
```

`Transition` performs callback changes under a row lock and records an event
in the same transaction. `Revoke` uses the same lifecycle table while also
revoking live runtime credentials atomically; `Transition(EventRevoke)`
delegates to it. The reaper uses a guarded bulk update with `RETURNING`, then
writes one `Expire` event per changed row before commit.

`NewProxy` rechecks `expires_at` against the database wall clock while holding
that row lock. An `active` + `NewProxy` callback is accepted as an idempotent
no-op, covering frps restarts or lost `CloseProxy` callbacks without emitting a
fake duplicate transition event.

DELETE and admin revoke:

1. transition a non-terminal tunnel to `revoked`;
2. set `revoked_at` on live runtime credentials; and
3. return `204`.

They also return `204` for already `expired`/`revoked` owned targets, making
network retries safe. The database row remains visible in list/get.

The 30-second reaper cadence is not an access grace period. In the bundled
topology, Caddy independently reads both status and `expires_at` through an
authorization subrequest before every wildcard HTTP request. This gate uses
PostgreSQL time and permits the non-terminal routable states `active` and
`closed`, while reserved, elapsed, and terminal tunnels fail closed. `closed`
is intentionally advisory because frps sends `CloseProxy` asynchronously and
does not expose a per-registration generation; a delayed close may arrive
after a replacement `NewProxy`. If no proxy is actually registered, frps has
no route to serve. Requests already admitted, including upgraded connections,
may drain.

## Credential model

### User API token

```text
sk_live_<24 random base62 characters>
```

It authenticates `/v1/*` and remains live until an operator revokes its token
row. The owning user's `is_admin` flag is copied into request context.

### Runtime tunnel token

```text
rt_<24 random base62 characters>
```

It belongs to one tunnel and is valid until its explicit revocation or expiry.
frpc carries it as client-level `metadatas.runtime_token`. Reuse is intentional
so frpc may reconnect. Successful Login updates `last_used_at` and `use_count`
on a best-effort basis; telemetry failure does not reject valid credentials.

### Stored form

Each token body contains roughly 143 random bits. PostgreSQL stores:

- the first 12 characters for indexed candidate lookup; and
- `sha256:<base64url-digest>` for current verification.

The full token is not stored in token columns. Prefixes are not assumed unique;
authentication iterates every candidate and compares each digest in constant
time.

SHA-256 is suitable here because these are uniformly random bearer secrets,
not human passwords. A slow password KDF would add request-denial cost without
materially changing offline brute-force feasibility.

The verifier also accepts two bounded historical formats:

- Argon2id PHC with the exact legacy parameters and random salt; and
- an older bare-hex Argon2id result with the historical fixed salt.

Those paths exist only for upgrade compatibility; new tokens use the versioned
SHA-256 format. At most two legacy Argon2 checks run concurrently. Additional
checks wait for bounded capacity for at most one second. Public API auth
returns `503` on saturation; the frps plugin rejects with an
`authentication temporarily unavailable` reason. Both avoid misclassifying a
valid legacy token as bad credentials. An Argon2 call already in progress
cannot be canceled, so a timed-out caller returns while the fixed-cost worker
finishes and retains one of the two slots.

### Operator secrets

| Secret | Shared with | Returned by API? | Role |
| --- | --- | --- | --- |
| `HATCHWAY_FRPS_AUTH_TOKEN` | frps and every frpc | yes | frps bootstrap authentication |
| `HATCHWAY_PLUGIN_SECRET` | frps, Caddy, and Hatchway | no | internal callback/gate paths and cache-key material |

The shared frps token cannot grant ownership because the plugin also requires a
valid per-tunnel token and exact proxy/subdomain binding.

## API design

The implemented public routes are:

```text
GET    /healthz
GET    /readyz
GET    /v1/me
POST   /v1/tunnels
GET    /v1/tunnels
GET    /v1/tunnels/{id}
DELETE /v1/tunnels/{id}
POST   /v1/admin/tunnels/{id}/revoke
```

There are no public user/token management, event, cross-user list, or
impersonation endpoints.

`GET /v1/tunnels` is owner-scoped and returns all states with stable descending
`(created_at, id)` keyset pagination. The client `hatchway list` follows every
page.

`GET /v1/me` returns both `user_id` and `is_admin`. The sole admin route is
cross-owner revoke. Admin status does not make ordinary tunnel list/get routes
cross-user.

### Error and request behavior

- `/v1` bodies default to a 64 KiB limit.
- JSON create bodies reject unknown fields and trailing objects.
- Unknown routes and unsupported methods use the same JSON error envelope as
  handlers.
- Request IDs are returned and logged.
- API and application logs use one JSON object per line.

### Rate limiting

Only `POST /v1/tunnels` is rate-limited. The token bucket is keyed by API token
ID and lives in process memory. It is a useful single-instance safeguard, not
a distributed global quota.

### Idempotency

Mutating methods (`POST`, `PUT`, `PATCH`, `DELETE`) recognize an optional
`Idempotency-Key`; the current API uses POST and DELETE. A key is:

- at most 255 bytes;
- unique within one API token ID; and
- fingerprinted with method, escaped path, raw query, and body.

An atomic `INSERT ... ON CONFLICT DO NOTHING` is the request reservation.
Reusing a key with a different fingerprint or during an in-flight request
returns 409.

Only successful `2xx` results are retained. Responses up to 4 KiB are encrypted
with AES-GCM. The encryption key is derived from
`HATCHWAY_PLUGIN_SECRET`; token ID, key, and status are associated data. Older
plaintext cache rows remain readable for their configured retention window.
Tunnel creation writes its encrypted replay response in the same transaction
as the tunnel, runtime credential, and creation event. A crash after commit
therefore cannot strand a real tunnel behind an unrecoverable in-flight key or
lose the only plaintext copy of its runtime credential.
Non-`2xx` results release the reservation. Oversized successful results are
marked complete without a replay body, so a replay reports that it must be
retried without the key.

Rotating the plugin secret invalidates still-retained encrypted replay bodies
from the previous key. This coupling is accepted in the current design and
must be considered in rotation procedures.

## HTTP request authorization

The bundled Caddy wildcard site runs `forward_auth` before `reverse_proxy`.
It overwrites `X-Hatchway-Tunnel-Host` with the original request host and
calls:

```text
GET /internal/tunnels/authorize/{HATCHWAY_PLUGIN_SECRET}
```

Hatchway accepts only a single-label tunnel ID under
`HATCHWAY_TUNNEL_DOMAIN` whose row is `active` or `closed` and whose
`expires_at` is later than PostgreSQL `now()`. Unknown, malformed, reserved,
elapsed, terminal, and database-unavailable checks fail closed. Caddy proceeds
to frps only after a `204` response.

This request gate is part of the deployment security boundary. A custom
reverse proxy must implement an equivalent non-cacheable check; proxying
wildcard traffic directly to frps does not provide immediate HTTP revocation.

## frps plugin authorization

The callback path is:

```text
POST /frp/plugin/{HATCHWAY_PLUGIN_SECRET}?op=<operation>
```

The path secret uses constant-time comparison. The configured plugin timeout
and request body cap bound each handler call; at most two non-cancelable legacy
Argon workers may finish in the background after their callers time out.

### `Login`

1. Read client-level `metas.runtime_token`.
2. Look up all live rows with its 12-character prefix and a non-terminal,
   unelapsed tunnel.
3. Materialize the candidates and release the database connection.
4. Constant-time verify current digests before bounded legacy candidates.
5. Best-effort update usage metadata on acceptance.

### `NewProxy`

Perform the same runtime-token lookup, then enforce:

- `proxy_name == tunnel_id`;
- proxy type is exactly `http`;
- `subdomain == tunnel_id`;
- `custom_domains` is empty; and
- tunnel state is non-terminal.

An accepted callback transitions `reserved → active` or
`closed → active`.

Quota is enforced during API allocation, not during `NewProxy`. This avoids
double-counting reconnects and places the concurrency race at one transaction
boundary.

Tunnel `expires_at` is calculated and returned by PostgreSQL in the allocation
transaction. Plugin authorization and the reaper also compare against
PostgreSQL `now()`, so host clock skew cannot change the requested TTL.

### `CloseProxy`

Transition `active → closed`. Unknown/non-active rows are allowed unchanged so
cleanup callbacks do not destabilize frps. Because frps sends this notification
asynchronously, `closed` is an observation rather than a hard data-plane
boundary; request authorization still lets frps decide whether a route exists.

### `NewUserConn`

When frp emits this callback, look up the proxy's tunnel and accept only an
`active` or `closed`, non-elapsed row. If `HATCHWAY_LOG_USER_CONNS=true`, also
persist an event containing proxy type and remote address. frp v0.69 does not emit
`NewUserConn` for HTTP proxies, so this callback is defense in depth for
applicable proxy types and is not the HTTP request gate.

### `Ping` and `NewWorkConn`

Allow unchanged.

## PostgreSQL model and migrations

The migration SQL is authoritative. The current logical roles are:

| Table | Purpose |
| --- | --- |
| `users` | identity and admin role |
| `api_tokens` | owner, label, lookup prefix, digest, revocation |
| `tunnels` | owner, local target, state, TTL, timestamps |
| `tunnel_runtime_tokens` | tunnel-scoped credential digest and usage/expiry |
| `tunnel_events` | lifecycle and optional connection audit events |
| `idempotency_keys` | request fingerprint, reservation/completion, encrypted response bytes |

Migration history:

| Version | Change |
| --- | --- |
| `0001` | initial tables and indexes |
| `0002` | `users.is_admin` |
| `0003` | idempotency response `JSONB → BYTEA` |
| `0004` | nullable in-flight response fields, `completed_at`, event-column cleanup |
| `0005` | runtime prefix and idempotency retention indexes |
| `0006` | owner-pagination/runtime-retention indexes and integrity constraints |

Version 0006 enforces local-port range, allowed tunnel states, non-negative
runtime-token use count, and consistent idempotency completion fields. It also
indexes owner keyset pagination and both dead-token sweep branches.

## Quota and concurrency

The quota counts `reserved`, `active`, and `closed` rows. Terminal rows do not
consume a slot. Within the create transaction:

1. lock a stable PostgreSQL advisory key derived from `user_id`;
2. count this user's non-terminal rows;
3. reject at the configured maximum; or
4. insert the new tunnel and runtime credential before commit.

This prevents concurrent requests for one user from observing the same free
slot. Different users can allocate concurrently.

## Operations and retention

The reaper runs every 30 seconds and expires elapsed non-terminal rows.
Hourly sweepers remove:

- tunnel events older than `HATCHWAY_EVENTS_RETENTION_DAYS`;
- idempotency records older than
  `HATCHWAY_IDEMPOTENCY_RETENTION_HOURS`; and
- revoked/expired runtime credentials whose dead time is older than
  `HATCHWAY_RUNTIME_TOKEN_RETENTION_DAYS`.

Prometheus metrics on the internal listener cover plugin operations/deadlines,
rate-limit rejections, tunnel transitions/state counts, and DB pool
connections. The current metrics do not include request-duration histograms or
per-tunnel traffic bytes.

`scripts/smoke.sh` is an authenticated **API CRUD smoke test**, not a
data-plane end-to-end test. It requires curl, jq, an explicit API origin, and
an API token; it creates/lists/gets/revokes a tunnel and verifies the retained
`revoked` state without printing the token-bearing creation payload.

## Security defaults

- Cryptographic randomness for tokens and IDs.
- Current token digests and constant-time comparison; bounded legacy formats.
- Localhost-only client targets.
- HTTP-only proxy type with exact name/subdomain binding.
- No user-chosen IDs or custom domains.
- One-hour requested default capped by a configurable maximum.
- Per-user serialized non-terminal quota.
- Per-token process-local rate limit.
- API body, header, and server timeouts.
- Internal-only callback/request-gate/metrics listener.
- Constant-time plugin path-secret comparison.
- Per-request HTTP tunnel authorization in the bundled Caddy topology.
- Encrypted idempotency response bodies.
- Schema version/dirty-state verification.
- Non-root Hatchway/frps images and least-capability application containers.
- Lifecycle events and structured request logs.

The system does not claim to hide the origin IP or defend an openly exposed
port 7000 from all denial-of-service traffic. Operators remain responsible for
host/network controls and secret rotation.

## CLI contract

Implemented server/operator commands:

```text
hatchway version
hatchway server init
hatchway server run
hatchway server healthcheck
hatchway server user create
hatchway server user list
hatchway server token create
hatchway server token list
hatchway server token revoke
hatchway server tunnels
```

Implemented client commands:

```text
hatchway auth set-token
hatchway auth whoami
hatchway auth logout
hatchway http
hatchway list
hatchway delete
```

`tcp` and `udp` exist only to return explicit unsupported errors.

JSON success output exists for `http`, `list`, `whoami`, user list, token list,
and server tunnel list. CLI-local errors remain text. The initial
`hatchway http --json` object contains:

```json
{
  "public_url": "https://t-xxx.tunnel.example.com",
  "status": "reserved",
  "tunnel_id": "t-xxx"
}
```

It does not currently include expiry or local endpoint fields.

## Future work

The following ideas are deliberately separated from the implemented contract.

### Additional protocols

TCP and UDP require server-side port allocation, published port ranges,
plugin validation of assigned remote ports, materially stronger abuse
controls, and updated proxy/TLS semantics. The visible CLI placeholders do not
imply those pieces exist.

### Public administration and console support

Potential additive endpoints include user/token lifecycle, cross-user
inventory, audit-event reads, and webhook/stream delivery. No route names or
payloads are committed. A current console must use pre-provisioned user tokens
or model all callers as one Hatchway owner.

### Access controls

Possible later features include per-tunnel basic auth, access tokens,
allowlists, visit/connection limits, bandwidth caps, and abuse reporting.

### Agent integrations

Agent-specific orchestration should remain outside this Go module and consume
the documented API/CLI. It must parse the actual three-field `http --json`
result and handle plain-text CLI failures until a structured-error contract is
added.

### Scale-out

Multiple API replicas need a shared rate limiter and careful coordination of
background workers. PostgreSQL already coordinates idempotency and per-user
allocation, but the current deployment and rate semantics are single-process.

## Implementation stack

- Go 1.26.5 module
- Cobra
- chi
- pgx/v5
- golang-migrate
- standard-library `slog`
- PostgreSQL 16 in the bundled deployment
- frp v0.69.0
- Docker Compose
- Caddy with the Cloudflare DNS plugin

The central design principle remains:

> Hatchway owns the control plane. frp owns the data plane.
