# Hatchway: CLI-first Self-hosted Public Tunnel System

**Status:** Design frozen. Implementation has not started. `PLAN.md` tracks progress phase by phase. When resuming work: read `PLAN.md` first to find the lowest unchecked task, then consult this file for the detailed spec.

Hatchway is a CLI-first, self-hosted public tunnel system for exposing private/local services to the public internet through temporary or persistent public endpoints.

It is conceptually a minimal, self-hosted subset of Cloudflare Tunnel, ngrok, and Pangolin, but intentionally avoids building a large dashboard-first zero-trust platform. Hatchway focuses on:

- Simple CLI UX
- Self-hosted server
- User/token-based tunnel ownership
- Temporary high-entropy tunnel IDs
- Public subdomain registration
- frp as the tunnel data plane
- PostgreSQL-backed control plane
- Server-side Docker deployment
- Native client CLI UX
- Future support for AI Agent skills, without coupling agent-specific logic into the core project

## Core Positioning

Hatchway is not a full zero-trust access platform.

It is:

> A CLI-first, self-hosted public tunnel registry and control plane powered by frp.

The user runs a Hatchway server on a public domain such as:

```text
example.com
api.example.com
frps.example.com
*.tunnel.example.com
```

A client can then run:

```bash
hatchway auth set-token sk_xxx
hatchway http 3000 --ttl 1h
```

And receive:

```text
https://t-xxxxxxxx.tunnel.example.com -> http://127.0.0.1:3000
```

## Main Goals

Hatchway should provide:

1. A single Go codebase.
2. A single CLI binary that supports both server-side and client-side roles.
3. A simple public API for creating, listing, deleting, and inspecting tunnels.
4. Token-based user ownership for tunnels.
5. High-entropy random tunnel IDs.
6. Optional expiration / TTL.
7. Optional maximum visit count in the future.
8. frp-powered HTTP, HTTPS, TCP, and UDP tunnel support.
9. Caddy / Traefik / Nginx-based wildcard TLS termination.
10. PostgreSQL-backed user, token, and tunnel state.
11. Docker Compose deployment for server-side components.
12. Native binary distribution for client-side usage.
13. JSON-first output suitable for scripts and AI agents.
14. A clean separation between:
    - Hatchway core tunnel system
    - Optional future `hatchway-skills` repo for AI Agent Skills

## Non-goals

Hatchway should not initially implement:

- A full Web dashboard
- Enterprise zero-trust access control
- Complex RBAC
- Built-in OAuth login flow
- File sharing
- Mesh VPN
- Full private network overlay
- Custom domain management
- Billing
- WAF
- CDN
- Browser-first configuration UI

These can be added later if needed, but the MVP should remain CLI-first and minimal.

## Codebase and Packaging Strategy

Hatchway should use one monorepo and one primary CLI binary.

Recommended structure:

```text
hatchway/
  cmd/
    hatchway/
      main.go

  internal/
    cli/
      commands/
    client/
      credentials/
      tunnels/
      frpc/
    server/
      api/
      auth/
      tunnels/
      frps/
      plugin/
    db/
    models/
    config/
    frp/
    logging/
```

The same `hatchway` binary should support both runtime roles:

```bash
hatchway server run
hatchway server token create --name dev-token

hatchway auth set-token sk_live_xxx
hatchway http 3000 --ttl 15m
hatchway list
hatchway delete t-xxx
```

This means:

```text
One codebase.
One CLI.
Two runtime roles.
Different packaging.
```

Server-side deployments can run the same binary inside Docker:

```text
hatchway server run
```

Client-side users should run the native CLI directly on their host machine:

```text
hatchway http 3000
```

The client should not default to Docker, because it usually needs to connect to a local host service such as `127.0.0.1:3000`. Running the client in Docker would make `127.0.0.1` refer to the container instead of the host, which creates avoidable networking confusion.

## Architecture

Hatchway consists of:

```text
Client Machine
  ├─ hatchway CLI
  ├─ frpc subprocess
  └─ local service, e.g. 127.0.0.1:3000

Public Server
  ├─ Hatchway server
  │   ├─ Control API
  │   ├─ token management
  │   ├─ tunnel registry
  │   ├─ frps plugin callback endpoint
  │   └─ PostgreSQL connection
  │
  ├─ frps subprocess or frps container
  │   ├─ frp control/work connections
  │   ├─ HTTP/HTTPS/TCP/UDP tunnel handling
  │   └─ server plugin integration
  │
  └─ Caddy / Traefik / Nginx
      ├─ wildcard TLS for *.tunnel.example.com
      ├─ routes api.example.com to Hatchway API
      └─ routes *.tunnel.example.com to frps vhost HTTP port
```

Recommended production routing:

The Hatchway server should monitor frps liveness as part of its `/readyz` endpoint — a TCP dial to `frps:7000` (or the frps bind port). If frps is down, the control plane reports not-ready, even if the API and database are healthy. This prevents operators from being misled by a healthy API when tunnels are actually down.

```text
api.example.com
  -> Hatchway server API

frps.example.com:7000
  -> frps bind port for frpc control/work connections

*.tunnel.example.com
  -> Caddy / Traefik wildcard TLS
  -> frps vhost HTTP port
  -> frpc
  -> local service
```

## Design Invariants

Non-obvious constraints that must be preserved in implementation. These are the architectural rules of engagement — violating any of them indicates a design-level mistake, not a style preference.

- **Two-token model**: User API tokens (`sk_live_…`) call the REST API; runtime tunnel tokens (`rt_…`) authenticate frpc with frps via the plugin. They must never be mixed or cross-exposed.
- **Auth layering with frp**: frp's `auth.token` is a global bootstrap secret (not a security boundary). The per-tunnel runtime token is the real ownership check, validated in the `Login` and `NewProxy` plugin callbacks.
- **Tunnel ID format**: `t-` prefix + 16 chars from Crockford alphabet `abcdefghjkmnpqrstuvwxyz23456789`. No underscores (DNS-label illegal), no confusable characters (`0 Oli u`).
- **Token storage**: prefix (for index lookup) + hash (for verification). Never store plaintext tokens.
- **Plugin endpoint isolation**: `:9001` must never be exposed publicly — it authorizes frps callbacks. Only reachable on the internal Docker network, protected by `HATCHWAY_PLUGIN_SECRET`.
- **Tunnel lifecycle state machine**: `reserved → active → closed → active` (reconnect loop). `expired` and `revoked` are terminal. All status writes go through a single `Transition()` function.
- **Idempotency**: `POST /v1/tunnels` supports `Idempotency-Key` header; `(token_id, key) → response` cached for 24h. Cached response body capped at 4 KB.

## Docker Strategy

Hatchway should use Docker primarily for server-side development and deployment.

Recommended server-side Docker Compose services:

```text
postgres
hatchway-server
frps
caddy / traefik
```

Recommended server deployment:

```text
Public VPS / server

Docker Compose
  ├─ postgres
  ├─ hatchway-server
  ├─ frps
  └─ caddy
```

Server-side Docker is recommended because:

- PostgreSQL is easy to run and persist as a container.
- Caddy/Traefik can handle TLS and reverse proxying cleanly.
- frps can run as a separate data-plane container.
- Hatchway control-plane can run as its own application container.
- Each component has a clear lifecycle and logging boundary.

Client-side Docker should not be the default.

The client CLI should be distributed as a native binary because:

- It needs to reach services on the user's local machine.
- `127.0.0.1` should mean the user's host, not a container.
- The CLI should be simple for humans, scripts, and AI agents.
- No root/admin privileges should be required in the common case.

### Suggested Docker Compose

Example server-side `docker-compose.yml`:

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: hatchway
      POSTGRES_USER: hatchway
      POSTGRES_PASSWORD: hatchway_dev_password
    volumes:
      - postgres_data:/var/lib/postgresql/data
    networks:
      - hatchway_internal

  hatchway-server:
    image: hatchway:latest
    command: ["hatchway", "server", "run"]
    environment:
      DATABASE_URL: postgres://hatchway:hatchway_dev_password@postgres:5432/hatchway?sslmode=disable
      HATCHWAY_DOMAIN: example.com
      HATCHWAY_API_DOMAIN: api.example.com
      HATCHWAY_FRPS_DOMAIN: frps.example.com
      HATCHWAY_TUNNEL_DOMAIN: tunnel.example.com
      HATCHWAY_API_ADDR: :9000
      HATCHWAY_FRPS_PLUGIN_ADDR: :9001
    depends_on:
      - postgres
    networks:
      - hatchway_internal

  frps:
    image: hatchway-frps:latest
    command: ["frps", "-c", "/etc/frp/frps.toml"]
    volumes:
      - ./frps.toml:/etc/frp/frps.toml:ro
    ports:
      - "7000:7000"
    networks:
      - hatchway_internal
    depends_on:
      - hatchway-server

  caddy:
    image: caddy:latest
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    networks:
      - hatchway_internal
    depends_on:
      - hatchway-server
      - frps

volumes:
  postgres_data:
  caddy_data:
  caddy_config:

networks:
  hatchway_internal:
```

Example `Caddyfile`:

```caddyfile
api.example.com {
    reverse_proxy hatchway-server:9000
}

*.tunnel.example.com {
    reverse_proxy frps:8081
}
```

Caddy only routes the public API port (`:9000`). The frps plugin port (`:9001`) is **never** routed by Caddy and is reachable only on the `hatchway_internal` Docker network. This is critical: the plugin endpoint authorizes frps callbacks and must not accept traffic from the public internet — exposing it would let anyone forge `Login` / `NewProxy` decisions and bypass tunnel ownership checks.

As defense-in-depth, the plugin endpoint requires a shared secret header `X-Hatchway-Plugin-Secret`, set via `HATCHWAY_PLUGIN_SECRET` on both the Hatchway server and frps (passed as a plugin header). The handler rejects any request without a matching secret. This protects against accidental network misconfiguration without relying solely on network isolation.

For wildcard TLS, production deployments should use the DNS-01 challenge. The stock `caddy:latest` image cannot perform DNS-01 — you must build a custom Caddy image that includes the appropriate DNS provider plugin (for example, `caddy-dns/cloudflare`, `caddy-dns/route53`). The certificate is persisted in the `caddy_data` volume; back this up so renewals don't restart from scratch after a host migration.

The `hatchway-server` and `frps` containers should run as non-root users. The `Dockerfile` should include a `USER nonroot` directive (or equivalent for the chosen base image). Do not run services as root inside containers.

## frp Role

frp is the data plane of Hatchway.

Hatchway should not reimplement tunnel transport, NAT traversal, connection multiplexing, HTTP Host routing, TCP/UDP forwarding, or proxy connection handling.

frp should provide:

- frps public server
- frpc client process
- HTTP tunnel support
- HTTPS tunnel support
- TCP tunnel support
- UDP tunnel support
- subdomain-based HTTP routing
- remote port based TCP/UDP routing
- control/work connection management
- optional connection pooling
- optional TCP multiplexing
- server plugin hooks

Hatchway should provide:

- users
- API tokens
- tunnel ownership
- tunnel ID generation
- runtime tunnel tokens
- tunnel lifecycle management
- frpc config generation
- frps plugin authorization
- TTL / expiry
- rate limits
- CLI UX
- JSON output
- future agent-friendly skills integration

## frp Integration Strategy

For MVP, Hatchway should not import frp's Go internals.

Instead, Hatchway should package or locate `frpc` and `frps` binaries and manage them as subprocesses or separate containers.

Reasons:

- Faster integration
- Less coupling to frp internal APIs
- Easier frp upgrades
- Easier debugging
- Clear process boundaries
- Lower maintenance burden

### Client-side frp Strategy

Client release package:

```text
hatchway
frpc
```

The native `hatchway` CLI should generate a temporary `frpc.toml` and start `frpc` as a subprocess.

Client-side flow:

```text
hatchway http 3000
  -> call POST /v1/tunnels
  -> receive tunnel_id, runtime_token, frp config
  -> generate temporary frpc.toml
  -> start frpc subprocess
  -> print public URL
  -> clean up on Ctrl-C / SIGTERM
```

### Server-side frp Strategy

Server deployment can choose one of two models:

#### Model A: frps as a separate container

Recommended for Docker Compose and production.

```text
hatchway-server container
frps container
```

Advantages:

- Clear control-plane/data-plane separation
- Independent logs
- Independent restarts
- Cleaner production operations

#### Model B: frps as a subprocess of `hatchway server run`

Useful for local development or ultra-simple single-binary deployments.

```text
hatchway server run
  -> starts API server
  -> starts frps subprocess
```

The MVP can support subprocess mode first, but production documentation should recommend separate containers.

Server-side flow:

```text
hatchway server run
  -> start Hatchway API server
  -> connect to PostgreSQL
  -> generate or validate frps.toml
  -> expose /frp/plugin endpoint
  -> optionally start frps subprocess
  -> handle frps plugin callbacks
```

## Suggested frps Configuration

Example generated `frps.toml`:

```toml
bindPort = 7000
vhostHTTPPort = 8081
subDomainHost = "tunnel.example.com"

auth.method = "token"
auth.token = "<bootstrap-secret>"

[[httpPlugins]]
name = "hatchway-control-plane"
addr = "http://hatchway-server:9001"
path = "/frp/plugin"
ops = ["Login", "NewProxy", "CloseProxy", "NewUserConn"]

[httpPlugins.headers]
X-Hatchway-Plugin-Secret = "<HATCHWAY_PLUGIN_SECRET>"
```

### Auth Layering

frp's built-in `auth.token` is a single shared secret across all clients — it cannot be per-tunnel. Hatchway therefore uses two layers:

1. **Bootstrap secret** — the value of `auth.token` on both `frps` and `frpc`. It is global and rotates only when the operator rotates it. Its job is to keep random scanners from connecting to frps at all. It is **not** the security boundary for tunnel ownership.
2. **Runtime tunnel token** — per-tunnel, short-lived, generated by the Hatchway control plane and carried by frpc as `metadatas.runtime_token`. The plugin's `Login` callback validates this token against the `tunnel_runtime_tokens` table; `NewProxy` cross-checks that the proxy name and subdomain match the tunnel the token belongs to.

The bootstrap secret is returned to clients in the tunnel-create response so the CLI can write it into the generated `frpc.toml`. Treat it like a public-but-rate-limited value: leakage lowers the cost of probing frps but does not let an attacker register a tunnel they don't own.

## Tunnel ID Format

Tunnel IDs are exposed in DNS labels (`<tunnel_id>.tunnel.example.com`), so they must conform to RFC-1035 label rules: lowercase letters, digits, and hyphens; cannot begin or end with a hyphen; max 63 chars. Crucially, **underscores are not legal** in DNS labels — strict resolvers (and many corporate DNS proxies) will refuse to resolve them, even though the rest of the world is forgiving.

Hatchway tunnel IDs:

- Prefix: `t-` (DNS-safe; the leading `t` ensures the label never starts with a digit, which some legacy resolvers also reject).
- Body: 16 random characters from a Crockford-style alphabet `abcdefghjkmnpqrstuvwxyz23456789` (lowercase base32 minus `0`, `1`, `i`, `l`, `o` and minus `u` to keep things copy/paste-safe and unambiguous when read aloud).
- Total length: 18 chars. Entropy: ~16 × log2(31) ≈ 79 bits. More than enough to make enumeration economically infeasible against per-token rate limits.

Example: `t-8xk4mq9pz2nv7rb6`.

Runtime tokens (`rt_…`) and user API tokens (`sk_live_…`) are not used as DNS labels and may keep the underscore separator.

## Tunnel Types

### HTTP Tunnel

HTTP tunnels use subdomains.

Example:

```bash
hatchway http 3000 --ttl 1h
```

Returns:

```text
https://t-8xk4mq9pz2nv7rb6.tunnel.example.com
```

Routing:

```text
Browser
  -> https://t-8xk4mq9pz2nv7rb6.tunnel.example.com
  -> Caddy wildcard TLS
  -> frps vhostHTTPPort
  -> frpc
  -> 127.0.0.1:3000
```

frpc config:

```toml
serverAddr = "frps.example.com"
serverPort = 7000

auth.method = "token"
auth.token = "<bootstrap-secret>"

# Per-tunnel runtime token; validated by the Login plugin op.
metadatas.runtime_token = "rt_xxxxxxxxxxxxxxxxx"

[[proxies]]
name = "t-8xk4mq9pz2nv7rb6"
type = "http"
localIP = "127.0.0.1"
localPort = 3000
subdomain = "t-8xk4mq9pz2nv7rb6"
```

frp HTTP tunnels transparently support WebSocket and Server-Sent Events without extra configuration — the `http` proxy type is enough.

### TCP Tunnel

TCP tunnels should use assigned public ports.

Example:

```bash
hatchway tcp 5432 --ttl 1h
```

Returns:

```text
tcp.tunnel.example.com:24017 -> 127.0.0.1:5432
```

TCP/UDP cannot rely on HTTP Host-based subdomain routing, so Hatchway must allocate and manage remote ports.

For Docker deployments, TCP/UDP tunnel support requires either:

1. Publishing a predefined port range, such as:

```yaml
ports:
  - "20000-30000:20000-30000/tcp"
  - "20000-30000:20000-30000/udp"
```

   **Warning:** publishing a 10,000-port range causes Docker to install ~10,000 iptables/userland-proxy rules at container start. On most Linux hosts this adds tens of seconds to minutes of startup time, balloons memory, and can lock up `iptables` for other services on the host. Keep the range as small as the tunnel quota allows (e.g. 200–500 ports) or use option 2.

2. Or using `network_mode: host` on Linux. This avoids the per-port rules entirely and is the recommended option once you outgrow a small published range. Linux only — host networking is not supported on Docker Desktop for macOS/Windows in the same way.

For MVP, HTTP-only support is recommended. TCP/UDP can be added after the HTTP tunnel path is stable.

### UDP Tunnel

Similar to TCP, but using UDP remote port allocation.

## Tunnel Lifecycle

A tunnel transitions through a small, explicit set of states. The `tunnels.status` column stores the current state.

| State      | Meaning                                                                                                | Triggered by                               |
| ---------- | ------------------------------------------------------------------------------------------------------ | ------------------------------------------ |
| `reserved` | API row exists, runtime token issued, frpc has not yet logged in.                                      | `POST /v1/tunnels` succeeds.               |
| `active`   | frpc is connected and the proxy is registered with frps.                                               | Plugin `NewProxy` accepted.                |
| `closed`   | frpc disconnected cleanly or by SIGTERM. May re-enter `active` on reconnect while not yet expired.     | Plugin `CloseProxy`.                       |
| `expired`  | TTL elapsed without explicit deletion. Terminal; runtime token rejected at next `Login`.               | Reaper job sees `now() > expires_at`.      |
| `revoked`  | User called `DELETE /v1/tunnels/{id}` or admin kill switch fired. Terminal; runtime token blacklisted. | `DELETE /v1/tunnels/{id}` or admin action. |

Transitions:

```text
reserved ──NewProxy──▶ active ──CloseProxy──▶ closed ──NewProxy──▶ active   (reconnect)
   │                      │                      │
   │                      │                      └──ttl elapsed──▶ expired
   │                      └──ttl elapsed────────────────────────▶ expired
   ├──ttl elapsed────────────────────────────────────────────────▶ expired
   └──DELETE / admin────────────────────────────────────────────▶ revoked
                                                                    ▲
                                                            (active/closed)
```

`expired` and `revoked` are terminal: a fresh tunnel requires a new `POST /v1/tunnels`, which mints a new ID and runtime token.

Connections in flight at the time of revocation or expiry are allowed to drain naturally — frps removes the proxy from its routing table on the next frpc disconnect. Hatchway does not forcefully terminate established user connections.

## Token Model

Hatchway should use two token layers.

### User API Token

Longer-lived token used by the CLI to call the control API.

Example:

```text
sk_live_xxxxxxxxxxxxxxxxx
```

Used for:

```http
Authorization: Bearer sk_live_xxx
```

The user token allows the user to:

- Create tunnels
- List their tunnels
- Delete their tunnels
- View tunnel status

### Runtime Tunnel Token

Short-lived token generated per tunnel.

Example:

```text
rt_xxxxxxxxxxxxxxxxx
```

Used by frpc to authenticate with frps via the `Login` plugin op.

The runtime token is scoped to exactly one tunnel and expires when the tunnel expires or is deleted. It is **reusable** until then — `last_used_at` and `use_count` are updated on every `Login` so that frpc reconnects (network blips, NAT rebinds, container restarts) work without the user re-running the CLI. A one-shot token would break reconnection semantics.

This two-token split prevents the long-lived user token from being exposed to the frp data plane.

## API Design

### Create Tunnel

```http
POST /v1/tunnels
Authorization: Bearer sk_live_xxx
Idempotency-Key: 4b1f9c0e-7a2d-4d2a-9b1d-2c1f8b0e4a3f
Content-Type: application/json

{
  "type": "http",
  "local_host": "127.0.0.1",
  "local_port": 3000,
  "ttl_seconds": 3600
}
```

The `Idempotency-Key` header is optional but recommended. The server stores `(token_id, key) → response` for 24h; replays return the original response and do not allocate a new tunnel ID. Without it, a network-retry on an in-flight request can burn a tunnel slot and consume the per-token quota twice.

Response:

```json
{
  "tunnel_id": "t-8xk4mq9pz2nv7rb6",
  "status": "reserved",
  "type": "http",
  "public_url": "https://t-8xk4mq9pz2nv7rb6.tunnel.example.com",
  "expires_at": "2026-05-06T00:00:00Z",
  "runtime_token": "rt_xxxxxxxxxxxxxxxxx",
  "frp": {
    "server_addr": "frps.example.com",
    "server_port": 7000,
    "server_token": "<bootstrap-secret>",
    "proxy_name": "t-8xk4mq9pz2nv7rb6",
    "proxy_type": "http",
    "subdomain": "t-8xk4mq9pz2nv7rb6",
    "local_ip": "127.0.0.1",
    "local_port": 3000
  }
}
```

`runtime_token` goes into `metadatas.runtime_token` in the generated `frpc.toml`. `frp.server_token` is the bootstrap secret and goes into `auth.token`. See "Auth Layering" above for why both are needed.

### List Tunnels

```http
GET /v1/tunnels?limit=50&cursor=xxx
Authorization: Bearer sk_live_xxx
```

Paginated. `limit` defaults to 50, max 100. `cursor` is an opaque token from the previous page's response. Response wraps results in a `tunnels` array and includes `next_cursor` (null when no more results):

```json
{
  "tunnels": [...],
  "next_cursor": "xxx"
}
```

### Get Tunnel

```http
GET /v1/tunnels/{tunnel_id}
Authorization: Bearer sk_live_xxx
```

### Delete Tunnel

```http
DELETE /v1/tunnels/{tunnel_id}
Authorization: Bearer sk_live_xxx
```

## CLI Design

Hatchway should be a single binary with multiple roles.

### Server Commands

```bash
hatchway version
hatchway server init
hatchway server run
hatchway server user create --email alice@example.com --name alice
hatchway server user list
hatchway server token create --user alice --name dev-token
hatchway server token revoke <token_id>
hatchway server tunnels list
```

`hatchway server init` runs DB migrations, creates a single bootstrap admin user, and prints its first API token to stdout exactly once. The admin user owns the operator CLI's tokens; ordinary tunnel users are added with `hatchway server user create`. There is intentionally no self-signup endpoint in MVP — the operator decides who gets a token. `--user` on `token create` accepts either email or name.

### Client Auth Commands

```bash
hatchway auth set-token --server https://api.example.com sk_live_xxx
hatchway auth whoami
hatchway auth logout
```

The token and server URL are persisted at `${XDG_CONFIG_HOME:-$HOME/.config}/hatchway/credentials.json` with file mode `0600` and parent directory mode `0700`. The CLI refuses to read the file if its permissions are looser than `0600`. `HATCHWAY_TOKEN` and `HATCHWAY_SERVER` in the environment override the file when set, which is the recommended way to use Hatchway from CI and from agent skills.

`HATCHWAY_SERVER` (or `--server`) is the base URL of the Hatchway control API (e.g. `https://api.example.com`). The CLI needs it to call `POST /v1/tunnels` and other endpoints. It must be set either via the env var, the `--server` flag on `auth set-token`, or in `credentials.json` — the CLI errors if no server is configured.

### Tunnel Commands

```bash
hatchway http 3000
hatchway http 3000 --ttl 15m
hatchway http 3000 --ttl 15m --json

hatchway tcp 5432 --ttl 1h
hatchway udp 5353 --ttl 10m

hatchway list
hatchway status <tunnel_id>
hatchway delete <tunnel_id>
```

All commands should support machine-readable JSON output:

```bash
hatchway http 3000 --ttl 15m --json
```

Example JSON output:

```json
{
  "tunnel_id": "t-8xk4mq9pz2nv7rb6",
  "url": "https://t-8xk4mq9pz2nv7rb6.tunnel.example.com",
  "local": "http://127.0.0.1:3000",
  "expires_at": "2026-05-06T00:15:00Z"
}
```

Errors should also be JSON when `--json` is enabled:

```json
{
  "error": {
    "code": "LOCAL_PORT_NOT_REACHABLE",
    "message": "No service is listening on 127.0.0.1:3000"
  }
}
```

## PostgreSQL Data Model

Use PostgreSQL for the control plane.

Suggested tables:

```sql
CREATE TABLE users (
  id UUID PRIMARY KEY,
  email TEXT UNIQUE,
  name TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE api_tokens (
  id UUID PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES users(id),
  name TEXT NOT NULL,
  token_prefix TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tunnels (
  id TEXT PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES users(id),
  type TEXT NOT NULL,
  public_host TEXT,
  public_port INTEGER,
  local_host TEXT NOT NULL,
  local_port INTEGER NOT NULL,
  status TEXT NOT NULL,
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tunnel_runtime_tokens (
  id UUID PRIMARY KEY,
  tunnel_id TEXT NOT NULL REFERENCES tunnels(id),
  token_prefix TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  last_used_at TIMESTAMPTZ,
  use_count BIGINT NOT NULL DEFAULT 0,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tunnel_events (
  id BIGSERIAL PRIMARY KEY,
  tunnel_id TEXT NOT NULL,
  user_id UUID,
  event_type TEXT NOT NULL,
  remote_addr TEXT,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_keys (
  token_id UUID NOT NULL REFERENCES api_tokens(id),
  key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  response_status INTEGER NOT NULL,
  response_body JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (token_id, key)
);
```

Tokens should not be stored in plaintext.

Store:

- token prefix for lookup
- token hash for verification

The `token_prefix` is the first 12 characters of the full token string (e.g. `sk_live_abc` from `sk_live_abcdef…`). It is used as a fast index lookup — the full token is never stored. Multiple tokens may share the same prefix; the hash comparison resolves any collisions.

### Retention

`tunnel_events` will grow unbounded if `NewUserConn` records every connection. Default retention: **30 days**, enforced by a periodic cleanup job (`DELETE FROM tunnel_events WHERE created_at < now() - interval '30 days'`). For higher-traffic deployments, switch to monthly partitioning and drop old partitions instead of row-deletion.

`idempotency_keys` rows can be purged after 24h (the documented replay window).

## Security Defaults

Hatchway should be safe by default.

Default behavior:

- Generate high-entropy tunnel IDs.
- Do not allow user-chosen tunnel IDs in MVP.
- Bind local services to `127.0.0.1` by default.
- Do not expose `0.0.0.0` unless explicitly requested.
- Require user API token for all tunnel operations.
- Require runtime token for frpc activation.
- Default tunnel TTL, such as 1 hour.
- Support shorter TTLs, such as 5m, 15m, 30m.
- Delete or disable expired tunnels automatically.
- Rate-limit tunnel creation per user token. **Default: 10 creates/min/token**, configurable via `HATCHWAY_RATE_CREATE_PER_MIN`.
- Limit concurrent tunnels per user. **Default: 5 active tunnels/user**, configurable via `HATCHWAY_MAX_CONCURRENT_TUNNELS`.
- Cap maximum TTL on a tunnel. **Default: 24h**, configurable via `HATCHWAY_MAX_TTL`.
- Cap idempotency cached response body size. **Default: 4 KB**. Responses exceeding this are not cached — the request still proceeds, just without idempotency protection.
- Limit TCP/UDP support initially if abuse risk is high.
- Provide an admin kill switch.
- Log tunnel lifecycle events.
- Configurable API server timeouts: `HATCHWAY_API_READ_TIMEOUT` (default 30s), `HATCHWAY_API_WRITE_TIMEOUT` (default 30s). Prevent slow clients from holding goroutines indefinitely.
- Avoid exposing frp admin dashboard publicly.

Future security features:

- Max visits per tunnel
- Optional public access token
- Basic auth per tunnel
- IP allowlist
- Per-tunnel bandwidth limit
- Per-tunnel connection limit
- Abuse reporting
- OAuth login
- Team/org model

## frps Plugin Authorization

The frps plugin endpoint should enforce:

### Login

Validate that the presented runtime token is valid.

### NewProxy

Validate:

- tunnel exists
- tunnel is not expired
- tunnel belongs to the token owner
- proxy name matches tunnel ID
- subdomain matches tunnel ID for HTTP tunnels
- remote port matches server-assigned port for TCP/UDP tunnels
- proxy type is allowed
- tunnel is not deleted
- user has not exceeded quota

### CloseProxy

Mark tunnel as offline if appropriate.

### NewUserConn

Optional:

- record connection event
- increment visit count
- enforce max visits
- block expired tunnel
- block deleted tunnel
- enforce abuse controls

## TLS and DNS

Recommended DNS:

```text
api.example.com          -> server public IP
frps.example.com         -> server public IP
*.tunnel.example.com     -> server public IP
```

Recommended TLS:

- Caddy / Traefik / Nginx terminates HTTPS.
- Use wildcard certificate for `*.tunnel.example.com`.
- Use DNS-01 challenge for wildcard certificates.
- frp should receive HTTP traffic from the reverse proxy on its vhost HTTP port.

Recommended routing:

```text
https://api.example.com
  -> Hatchway API server

https://*.tunnel.example.com
  -> Caddy wildcard TLS
  -> frps vhostHTTPPort
```

## Privilege Model

Client CLI should not require root/admin privileges.

Client-side Hatchway should:

- Run as normal user.
- Start frpc as normal user.
- Connect outbound to frps.
- Connect to local service on 127.0.0.1.
- Avoid TUN/TAP.
- Avoid kernel extensions.
- Avoid privileged local ports.

Server-side Hatchway should also avoid root where possible.

Recommended server deployment:

- Caddy/Nginx/Traefik handles ports 80/443.
- Hatchway API listens on high local port.
- frps listens on high ports.
- TCP/UDP tunnel public ports should be high ports by default.

If TCP/UDP tunnel support is enabled in Docker, either publish a fixed port range or document Linux-only host networking mode.

## Future AI Agent Skills

Agent-specific logic should live in a separate repository, such as:

```text
hatchway-skills/
  expose-http-service/
  expose-tcp-service/
  stop-tunnel/
  check-tunnel-status/
```

The core Hatchway project should remain generic.

Agent-facing commands should rely on stable CLI and JSON output.

Example agent command:

```bash
hatchway http 3000 --ttl 15m --json
```

The agent skill should parse:

```json
{
  "tunnel_id": "t-xxx",
  "url": "https://t-xxx.tunnel.example.com",
  "expires_at": "..."
}
```

## MVP Scope

MVP should include:

1. Single Go monorepo.
2. Single `hatchway` CLI binary.
3. `hatchway server run`.
4. Server-side Docker Compose deployment.
5. PostgreSQL persistence.
6. Manual token creation.
7. `hatchway auth set-token`.
8. HTTP tunnel creation.
9. Native client CLI.
10. frpc subprocess management on client.
11. frps subprocess or container management on server.
12. frps plugin authorization.
13. Random high-entropy tunnel IDs.
14. TTL support.
15. JSON output.
16. Caddy/Traefik-compatible wildcard TLS deployment guide.

MVP may postpone:

- TCP/UDP tunnels
- OAuth login
- Web dashboard
- Custom domains
- Billing
- Organizations/teams
- Agent skills repo
- Max visits
- Per-tunnel access auth

## Recommended Implementation Stack

Preferred language:

- Go 1.22+

Reasons:

- frp is Go-based
- single static binary distribution
- good CLI ecosystem
- good process management
- easy cross-compilation
- good PostgreSQL libraries
- easy Docker packaging

Planned libraries:

- Cobra for CLI
- pgx for PostgreSQL
- chi for HTTP API
- zap or zerolog for structured logging
- os/exec for frpc/frps subprocess management
- golang-migrate for DB migrations

Server deployment: Docker Compose (postgres, hatchway-server, frps, caddy). Client: native binary (no Docker — must reach `127.0.0.1` on host).

HTTP-only tunnels for MVP. TCP/UDP are post-MVP.

### Security-critical Code Paths

These code paths must have high test coverage. Bugs here are not just crashes — they are security vulnerabilities:

- Token mint and verify (argon2id hash, prefix lookup, constant-time compare)
- Plugin `Login` and `NewProxy` authorization (runtime token validation, tunnel ownership, proxy-name/subdomain matching)
- Tunnel lifecycle state machine transitions (terminal states, reconnect loop)
- Idempotency middleware (correct caching and replay without double-allocation)

## Product Summary

Hatchway is a minimal self-hosted tunnel platform.

It should feel like:

```bash
hatchway http 3000 --ttl 15m
```

And return:

```text
https://t-xxx.tunnel.example.com
```

Under the hood:

```text
Hatchway CLI
  -> Hatchway API
  -> PostgreSQL tunnel registry
  -> frpc subprocess
  -> frps subprocess/container
  -> Caddy wildcard TLS
  -> public tunnel URL
```

The key design principle:

> Hatchway owns the control plane. frp owns the data plane.

Keep the core small, CLI-first, scriptable, secure by default, easy to self-host, and deployable with Docker on the server side while remaining native and frictionless on the client side.
