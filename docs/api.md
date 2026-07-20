# API Reference

Base URL: `https://api.example.com` (or `http://localhost:9000` for local development).

All `/v1/*` endpoints require a Bearer token:

```
Authorization: Bearer sk_live_abc123...
```

## Authentication

### Bearer token

Pass an API token (`sk_live_...`) in the `Authorization` header. Tokens are created via `hatchway server token create`.

Invalid or revoked tokens return `401 UNAUTHENTICATED`.

### Idempotency

POST endpoints accept an `Idempotency-Key` header. On retry with the same key and body, the original response is returned. Keys expire after 24 hours.

```
Idempotency-Key: <unique-string>
```

### Rate limiting

Tunnel creation is rate-limited per token. Default: 10 requests/minute. Exceeding the limit returns `429 RATE_LIMITED`.

## Error format

All errors return JSON:

```json
{
  "error": {
    "code": "NOT_FOUND",
    "message": "tunnel not found"
  }
}
```

Error codes:

| Code                       | HTTP status | Meaning                                       |
| -------------------------- | ----------- | --------------------------------------------- |
| `UNAUTHENTICATED`          | 401         | Missing or invalid token                      |
| `FORBIDDEN`                | 403         | Token lacks permission                        |
| `NOT_FOUND`                | 404         | Resource does not exist (or not owned by you) |
| `RATE_LIMITED`             | 429         | Too many requests                             |
| `QUOTA_EXCEEDED`           | 403         | Concurrent tunnel limit reached               |
| `INVALID_REQUEST`          | 400         | Malformed request body or parameters          |
| `INTERNAL`                 | 500         | Unexpected server error                       |

## Health endpoints (unauthenticated)

### `GET /healthz`

Cheap liveness check. Always returns `200 OK`.

### `GET /readyz`

Readiness check. Returns `200 OK` if the database is reachable, `503` otherwise.

## API endpoints

### `GET /v1/me`

Returns the authenticated user's ID.

**Response** `200 OK`:

```json
{
  "user_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

### `POST /v1/tunnels`

Create a new tunnel.

**Request body**:

```json
{
  "type": "http",
  "local_host": "127.0.0.1",
  "local_port": 3000,
  "ttl_seconds": 3600
}
```

| Field         | Type    | Required | Default       | Description                                            |
| ------------- | ------- | -------- | ------------- | ------------------------------------------------------ |
| `type`        | string  | yes      | —             | Tunnel type. Only `"http"` in MVP.                     |
| `local_host`  | string  | no       | `"127.0.0.1"` | Local host to forward to. Must be `127.0.0.1` or `localhost`. |
| `local_port`  | integer | yes      | —             | Local port (1–65535).                                  |
| `ttl_seconds` | integer | no       | `3600` (1h)   | Time-to-live in seconds. Capped by `HATCHWAY_MAX_TTL`. |

**Response** `201 Created`:

```json
{
  "tunnel_id": "t-abc3x7km9w2p4rng",
  "status": "reserved",
  "type": "http",
  "public_url": "https://t-abc3x7km9w2p4rng.tunnel.example.com",
  "expires_at": "2026-05-14T15:00:00Z",
  "runtime_token": "rt_a1b2c3d4e5f6...",
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

The `frp` object contains everything needed to generate an `frpc.toml` config. The `runtime_token` is scoped to this tunnel and expires with it.

### `GET /v1/tunnels`

List tunnels owned by the authenticated user.

**Query parameters**:

| Param    | Type    | Default | Max | Description                            |
| -------- | ------- | ------- | --- | -------------------------------------- |
| `limit`  | integer | 50      | 100 | Number of results per page             |
| `cursor` | string  | —       | —   | Opaque cursor returned by a prior call |

**Response** `200 OK`:

```json
{
  "tunnels": [
    {
      "tunnel_id": "t-abc3x7km9w2p4rng",
      "type": "http",
      "status": "active",
      "public_url": "https://t-abc3x7km9w2p4rng.tunnel.example.com",
      "expires_at": "2026-05-14T15:00:00Z"
    }
  ],
  "next_cursor": "eyJjIjoiMjAyNi0wNS0xNFQxNTowMDowMFoiLCJpIjoidC1hYmMzeDdrbTl3MnA0cm5nIn0"
}
```

`next_cursor` is `null` when the page is the last one. Otherwise pass it
verbatim back as `?cursor=...` to fetch the next page. The cursor encodes
`(created_at, tunnel_id)`; treat it as opaque — callers should not parse it.

### `GET /v1/tunnels/{id}`

Get details for a specific tunnel. Returns `404` if the tunnel does not exist or is owned by a different user.

**Response** `200 OK`:

```json
{
  "tunnel_id": "t-abc3x7km9w2p4rng",
  "type": "http",
  "status": "active",
  "public_url": "https://t-abc3x7km9w2p4rng.tunnel.example.com",
  "expires_at": "2026-05-14T15:00:00Z"
}
```

### `DELETE /v1/tunnels/{id}`

Delete (revoke) a tunnel. Revokes the runtime token and disconnects the frpc client.
Idempotent: deleting a tunnel that's already `expired` or `revoked` also returns
`204` rather than an error, so retries after a network blip don't fail.

**Response** `204 No Content` (empty body).

Returns `404` if the tunnel does not exist or is owned by a different user.

### `POST /v1/admin/tunnels/{id}/revoke`

Admin-only endpoint to forcefully revoke any tunnel, regardless of ownership.
The caller's API token must belong to a user with `is_admin = true`. Non-admin
callers receive `403 FORBIDDEN`.

**Response** `204 No Content` (empty body). Idempotent: revoking a tunnel
that's already `expired` or `revoked` also returns `204` rather than an error.

Returns `403 FORBIDDEN` if the caller is not an admin. Returns `404` if the
tunnel does not exist.

## Tunnel lifecycle

```
                         ┌──────────┐
                         │ reserved │
                         └─────┬────┘
                               │
              ┌────────────────┼────────────────┐
              │ NewProxy       │                │ Expire
              ▼                │                ▼
        ┌──────────┐           │          ┌──────────┐
        │  active  │◄─────┐    │          │ expired  │ (terminal)
        └────┬─────┘      │    │          └──────────┘
             │            │    │
    ┌────────┼────────┐   │    │
    │        │        │   │    │ Revoke
    │ Close  │ Expire │   │    │
    │ Proxy  │        │   │    ▼
    ▼        ▼        │   │ ┌──────────┐
┌────────┐ ┌───────┐  │   │ │ revoked  │ (terminal)
│ closed │ │expired│  │   │ └──────────┘
└───┬────┘ └───────┘  │   │
    │                 │   │
    │ NewProxy        │   │
    └─────────────────┘   │
           (reconnect)    │
```

> `Expire` and `Revoke` transitions are possible from `reserved`, `active`, and `closed`.
> `expired` and `revoked` are terminal — a new tunnel requires `POST /v1/tunnels`.

- `reserved` → initial state after `POST /tunnels`
- `active` → frpc connected and proxy registered
- `closed` → frpc disconnected
- `expired` → TTL elapsed (terminal)
- `revoked` → manually deleted (terminal)
