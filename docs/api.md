# API Reference

This document describes the implemented public HTTP API. The default local
base URL is `http://localhost:9000`; a typical deployment exposes it as
`https://api.example.com`.

## Authentication

Every `/v1/*` route requires an API token:

```http
Authorization: Bearer sk_live_...
```

Invalid, malformed, or revoked tokens return `401 UNAUTHENTICATED`. A database
failure during authentication returns `503 INTERNAL` without exposing the
storage error.

API tokens inherit the `is_admin` value of their owning user. There is no
separate admin-token format.

## Request behavior

### Body limits and JSON decoding

The default maximum body size for `/v1/*` is 65,536 bytes, configurable with
`HATCHWAY_MAX_REQUEST_BYTES`. Oversize bodies return `413 INVALID_REQUEST`.
Create-tunnel bodies must contain exactly one JSON object and may not contain
unknown fields.

### Idempotency

`POST`, `PUT`, `PATCH`, and `DELETE` requests may include:

```http
Idempotency-Key: <unique-string>
```

The current API has mutating `POST` and `DELETE` routes. A key:

- is scoped to the authenticated API token ID;
- may contain at most 255 bytes;
- is bound to the request method, escaped path, raw query string, and body;
- returns `409 INVALID_REQUEST` if reused for a different request or while the
  original request is still in flight; temporary in-flight conflicts include
  `Retry-After: 1`, while permanent fingerprint conflicts do not; and
- defaults to a 24-hour retention window, configurable with
  `HATCHWAY_IDEMPOTENCY_RETENTION_HOURS`.

Only successful `2xx` responses with bodies no larger than 4 KiB are
replayable. Non-`2xx` results release the reservation so a later retry can run
again. An oversized successful response is allowed to complete, but later
replay attempts return an error and must be retried without that key.
Tunnel creation stores the encrypted replay response in the same transaction
as the tunnel and its one-time runtime credential, so a committed create is
recoverable by replay even if the server exits before writing the first HTTP
response.

Cached response bodies are encrypted with AES-GCM using a key derived from
`HATCHWAY_PLUGIN_SECRET`; the token ID, idempotency key, and response status
are authenticated as associated data. Rows written by older versions may
remain readable as plaintext until the retention sweeper removes them.
Rotating `HATCHWAY_PLUGIN_SECRET` also makes encrypted cache rows from the old
key unreadable, so either allow the replay window plus one hourly sweeper
interval to elapse first or accept that affected retries must use a new
idempotency key.

### Rate and concurrent-tunnel limits

`POST /v1/tunnels` uses an in-memory token bucket per API token. The default is
10 creates/minute, configurable with `HATCHWAY_RATE_CREATE_PER_MIN`. Because
rate limiting runs before idempotency, a replay attempt also consumes a rate
limit token. This limiter is process-local; a multi-replica deployment does
not have a shared global bucket.

The default concurrent quota is 5 **non-terminal** tunnels per user
(`reserved`, `active`, or `closed`), configurable with
`HATCHWAY_MAX_CONCURRENT_TUNNELS`. Creation for one user is serialized with a
transaction-scoped PostgreSQL advisory lock, so simultaneous requests cannot
race past the quota.

## Responses and errors

Successful JSON responses use `Content-Type: application/json`. Health checks
and Prometheus metrics are intentionally plain text.

All routed errors, including unknown routes and unsupported methods, use this
JSON envelope:

```json
{
  "error": {
    "code": "NOT_FOUND",
    "message": "tunnel not found"
  }
}
```

| Code | Typical status | Meaning |
| --- | --- | --- |
| `UNAUTHENTICATED` | 401 | Missing, malformed, invalid, or revoked API token |
| `FORBIDDEN` | 403 | Admin privilege is required |
| `QUOTA_EXCEEDED` | 403 | The user's non-terminal tunnel quota is full |
| `NOT_FOUND` | 404 | Route/resource absent, or the tunnel is owned by another user |
| `INVALID_REQUEST` | 400, 405, 409, 413 | Invalid body/parameters/method, idempotency conflict, or oversize body |
| `RATE_LIMITED` | 429 | Per-token create rate exceeded |
| `INTERNAL` | 500, 503 | Internal or temporarily unavailable dependency |

Every API request receives an `X-Request-ID` response header. Server logs are
JSON and include `request_id`, method, path, status, latency in milliseconds,
and the authenticated token ID when available.

## Unauthenticated endpoints

### `GET /healthz`

Returns `200 OK` with plain-text body `ok`. This is a cheap process-liveness
check.

### `GET /readyz`

Returns `200 OK` with plain-text body `ok` only when PostgreSQL is reachable
and the expected schema tables are present. Otherwise it returns a JSON
`503 INTERNAL` error.

Prometheus metrics are not on the public API listener. They are served from
the internal control listener at `GET :9001/metrics`.

## Authenticated endpoints

### `GET /v1/me`

Returns the authenticated principal:

```json
{
  "user_id": "550e8400-e29b-41d4-a716-446655440000",
  "is_admin": true
}
```

### `POST /v1/tunnels`

Creates a short-lived HTTP tunnel in `reserved` state.

```json
{
  "type": "http",
  "local_host": "127.0.0.1",
  "local_port": 3000,
  "ttl_seconds": 3600
}
```

| Field | Required | Default | Contract |
| --- | --- | --- | --- |
| `type` | yes | — | Must be `"http"` |
| `local_host` | no | `"127.0.0.1"` | Must be `127.0.0.1` or `localhost` |
| `local_port` | yes | — | Integer from 1 through 65535 |
| `ttl_seconds` | no | lower of `3600` and the configured maximum | Non-negative integer; `0` selects the server default; values above `HATCHWAY_MAX_TTL` are rejected |

Response: `201 Created`.

```json
{
  "tunnel_id": "t-abc3x7km9w2p4rng",
  "status": "reserved",
  "type": "http",
  "public_url": "https://t-abc3x7km9w2p4rng.tunnel.example.com",
  "expires_at": "2026-07-23T15:00:00Z",
  "runtime_token": "rt_...",
  "frp": {
    "server_addr": "frps.example.com",
    "server_port": 7000,
    "server_token": "<HATCHWAY_FRPS_AUTH_TOKEN>",
    "proxy_name": "t-abc3x7km9w2p4rng",
    "proxy_type": "http",
    "subdomain": "t-abc3x7km9w2p4rng",
    "local_ip": "127.0.0.1",
    "local_port": 3000
  }
}
```

`runtime_token` and the `frp` object are returned only by creation. Treat them
as secrets. The shared `frp.server_token` is the frps bootstrap credential,
not the internal plugin secret.

### `GET /v1/tunnels`

Lists all tunnels owned by the authenticated user, including terminal
`expired` and `revoked` rows. It is not an “active only” view.

| Query | Default | Contract |
| --- | --- | --- |
| `limit` | `50` | Integer from 1 through 100 |
| `cursor` | absent | Opaque cursor returned by the previous page |

Results use descending `(created_at, tunnel_id)` keyset pagination:

```json
{
  "tunnels": [
    {
      "tunnel_id": "t-abc3x7km9w2p4rng",
      "status": "active",
      "type": "http",
      "public_url": "https://t-abc3x7km9w2p4rng.tunnel.example.com",
      "expires_at": "2026-07-23T15:00:00Z"
    }
  ],
  "next_cursor": null
}
```

Pass a non-null `next_cursor` back verbatim. Its encoding is an implementation
detail.

### `GET /v1/tunnels/{id}`

Returns one owned tunnel using the same non-secret fields as list. A missing
or differently owned tunnel returns `404 NOT_FOUND`.

### `DELETE /v1/tunnels/{id}`

Revokes an owned tunnel and its live runtime credentials. This operation does
not delete its database row. It returns `204 No Content`.

Deleting an already `expired` or `revoked` tunnel is idempotent and also
returns `204`. A missing or differently owned tunnel returns `404 NOT_FOUND`.
In the bundled topology, Caddy rejects subsequent HTTP requests after
revocation by consulting Hatchway before proxying to frps. Requests already
admitted, including upgraded connections, may drain; Hatchway does not forcibly
terminate their sockets.

### `POST /v1/admin/tunnels/{id}/revoke`

Revokes any tunnel regardless of ownership. The caller must belong to an admin
user. It has the same `204` terminal-state idempotency and connection-drain
semantics as `DELETE`.

This is the only implemented public admin API. User creation, token creation,
token listing, and cross-user tunnel listing are trusted server-side CLI
operations, not `/v1/admin` endpoints.

## Tunnel lifecycle

```text
reserved ──NewProxy──► active ──CloseProxy──► closed
    ▲                    │                       │
    │                    └──────── reconnect ───┘
    │
    └─ initial create

reserved | active | closed ──TTL──► expired
reserved | active | closed ──revoke──► revoked
```

`expired` and `revoked` are terminal. The reaper records TTL expiry at most
about 30 seconds after it elapses, but the bundled Caddy request gate compares
`expires_at` against PostgreSQL time before every HTTP request, so the reaper
interval does not extend access. A new tunnel requires a new
`POST /v1/tunnels`.

`closed` records an asynchronous frps callback and is advisory: a delayed
callback can describe a registration that was already replaced. The bundled
request gate therefore permits both `active` and `closed` non-elapsed rows and
lets frps determine whether a route currently exists; it always denies
`reserved`, `expired`, and `revoked`.
