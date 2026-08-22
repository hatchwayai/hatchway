# Hatchway security model

This page is the operator-facing summary of Hatchway's security design:
what protects what, which boundaries are enforced where, and what the system
deliberately does not claim. The authoritative version lives in
[DESIGN.md](../DESIGN.md) ("Trust boundaries", "Credential model", "HTTP
request authorization", "frps plugin authorization", "Security defaults").

## Trust boundaries

```text
Public internet
  ├─ Caddy :80/:443          TLS termination, API routing, tunnel request gate
  └─ frps :7000              frpc control channel (raw TCP)

Private service network
  ├─ Hatchway API :9000      public only through Caddy
  ├─ Hatchway :9001          frps callbacks, Caddy gate, metrics — internal only
  └─ frps :8081              HTTP vhost data plane

Isolated database network
  ├─ Hatchway
  └─ PostgreSQL :5432
```

Port `9001` is a high-trust interface. Its callback and request-gate paths
carry a shared path secret, but that is defense in depth — never publish the
listener. Metrics on it are unauthenticated and expose aggregate state.
Server-side `hatchway server …` commands talk to PostgreSQL directly and are
trusted-operator tools.

## Two credential scopes

| Token | Format | Authorizes | Lifetime |
| --- | --- | --- | --- |
| API token | `sk_live_<24 chars>` | control-plane calls to `/v1/*` | until revoked by an operator |
| Runtime token | `rt_<24 chars>` | data-plane registration for **exactly one tunnel** | the tunnel's lifetime |

Each token body carries ~143 random bits. The separation means a credential
that rides inside an `frpc.toml` on some CI runner cannot list tunnels, create
new ones, or touch any other resource — worst case is that one tunnel until it
expires or is revoked.

## Storage and verification

Plaintext tokens are never stored. PostgreSQL holds:

- the first 12 characters, for indexed candidate lookup; and
- a versioned digest, `sha256:<base64url>`, for verification.

Prefixes are not assumed unique: authentication fetches every candidate row
and compares digests in constant time. SHA-256 (rather than a password KDF)
is a deliberate choice — these are uniformly random bearer secrets, not
human-chosen passwords, so offline brute force is infeasible against the
entropy itself and a slow KDF would only add request-denial cost.

Older installations may still hold Argon2id-format rows. The verifier accepts
those legacy formats with globally bounded concurrency (two workers; callers
wait at most one second and fail with `503` / a plugin-side "temporarily
unavailable" rejection rather than misclassifying a valid token), so legacy
verification cannot be used to denial-of-service the API. New tokens always
use the versioned SHA-256 format.

## Data-plane authorization (frps plugin)

frps calls back into Hatchway before accepting client activity. The callback
path embeds `HATCHWAY_PLUGIN_SECRET`, compared in constant time, and every
handler runs under a configurable timeout and body cap.

- **`Login`** — frpc presents the runtime token as client metadata. Hatchway
  verifies it against live, non-terminal, unelapsed tunnel rows.
- **`NewProxy`** — additionally requires `proxy_name == tunnel_id`, proxy type
  exactly `http`, `subdomain == tunnel_id`, empty `custom_domains`, and a
  non-terminal tunnel state. A tunnel cannot register under any name or
  subdomain other than the one the server issued.
- **`CloseProxy` / `Ping` / `NewWorkConn`** — lifecycle bookkeeping only.

The shared frps bootstrap token (`HATCHWAY_FRPS_AUTH_TOKEN`, returned in
creation responses so callers' frpc can connect) deters unauthenticated
scanners but is **not** the ownership boundary — the per-tunnel runtime token
and the checks above are.

## Request-time gating

The bundled Caddy configuration runs a `forward_auth` subrequest to Hatchway
before proxying any wildcard-host request to frps. A request is admitted only
when its host is a single tunnel label under the tunnel domain whose row is
`active` or `closed` with `expires_at` still in the future, judged against
PostgreSQL time (host clock skew cannot extend a TTL). Unknown, malformed,
elapsed, terminal, and database-unavailable cases fail closed. This is what
makes revocation and expiry take effect on the HTTP path immediately, without
waiting for frps state to converge; accepted connections, including upgraded
ones, drain naturally.

A custom reverse proxy must implement an equivalent non-cacheable check —
proxying wildcard traffic directly to frps gives up immediate HTTP
revocation.

## Abuse controls

- **Per-user concurrent-tunnel quota** (default 5 non-terminal tunnels),
  counted and allocated inside one serialized per-user transaction
  (advisory-lock keyed) so concurrent creates cannot double-spend a slot.
- **Per-token creation rate limit** (default 10/minute) — in-process token
  bucket.
- **TTL ceiling** (default 24h) and a 30-second reaper expiring elapsed
  tunnels; `expired` and `revoked` are terminal states and revocation is a
  state transition, never a row deletion.
- Request body size caps, header limits, and API read/write timeouts.
- Tunnel IDs draw ~79 random bits from a 31-symbol, ambiguity-free alphabet;
  the `t-` prefix keeps them DNS-legal.

## Secrets handling

- `HATCHWAY_PLUGIN_SECRET` is shared only among Hatchway, frps, and Caddy,
  never appears in an API response, and doubles as key material encrypting
  cached idempotency response bodies — those bodies can contain one-time
  runtime tokens, so successful creation replays are encrypted at rest.
- Owner-scoped reads cannot reveal whether another user's tunnel exists
  (cross-owner lookups return `404`, not `403`).
- Containers run as non-root; startup refuses to serve against a missing,
  outdated, or dirty database schema.

## What Hatchway does not claim

- It does not hide the origin server IP, and an openly exposed frps port
  7000 is not defended against arbitrary denial-of-service traffic. Host and
  network controls remain the operator's job (see the hardening backlog in
  [PLAN.md](../PLAN.md)).
- Rate limiting is per-process; the MVP server is single-replica.
- Only HTTP tunnels are in scope; `tcp`/`udp` commands are explicit
  placeholders.
- Operators are responsible for secret rotation and for keeping `.env`,
  credentials files (`0600`), and port `9001` private.
