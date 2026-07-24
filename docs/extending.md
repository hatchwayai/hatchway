# Extending Hatchway

Hatchway's supported integration boundary is its HTTP API and command-line
interface. It is a standalone service, not a reusable Go library: all Go
packages live under `internal/` and cannot be imported by another module.

## What is available today

The public API supports:

- inspecting the authenticated principal;
- creating, listing, reading, and revoking that principal's tunnels; and
- revoking any tunnel with an admin user's token.

See [api.md](api.md) for the complete route list. In particular, the current
server does **not** expose public APIs for:

- creating or listing users;
- minting, listing, or revoking API tokens;
- listing tunnels across users;
- reading tunnel events; or
- impersonating another user.

Those operations exist only as trusted, database-backed
`hatchway server ...` commands where documented in [cli.md](cli.md).

## Integration patterns

### Call the HTTP API

For CI, agents, or another backend, use a dedicated `sk_live_...` token and
call the owner-scoped endpoints. Send a unique `Idempotency-Key` when creating
a tunnel and retain the returned tunnel ID for later revocation.

If one external service manages tunnels for multiple people with one Hatchway
token, Hatchway sees a single owning user. The external service must enforce
its own tenant authorization and mapping. An admin token does not make normal
`/v1/tunnels` calls cross-user; it only unlocks the explicit admin-revoke
route.

### Invoke the CLI

Shelling out can be practical for local automation. `hatchway http --json`,
`hatchway list --json`, and `hatchway auth whoami --json` have
machine-readable success output. CLI-originated failures are still plain text
on stderr, and `hatchway http` remains attached while frpc is running.

For unattended callers, prefer `HATCHWAY_SERVER` and `HATCHWAY_TOKEN` over a
shared credentials file. Remember that `hatchway http` requires `frpc` beside
the Hatchway executable or on `PATH`.

### Build a separate console

A dashboard or SSO-facing console can live beside Hatchway and use the HTTP
API for tunnel operations. With the current API, provision Hatchway users and
their tokens through an operator-controlled workflow before handing a token to
the console. Do not claim that the console can call an admin user-provisioning
route; no such route exists.

If a reverse auth proxy injects an `Authorization` header, it must:

- remove any client-supplied authorization header;
- map the authenticated identity to the correct Hatchway token;
- protect that token as a secret; and
- prevent one identity from selecting another identity's token.

The identity proxy should cover only the public API listener. Never expose the
internal control listener (`:9001`) to end users.

## Boundaries to preserve

- Do not read or write PostgreSQL from an external application. Direct writes
  bypass token handling, per-user quota serialization, lifecycle events,
  idempotency, and ownership checks.
- Do not reuse `HATCHWAY_PLUGIN_SECRET` or
  `HATCHWAY_FRPS_AUTH_TOKEN` as an end-user API credential.
- Do not store the one-time plaintext runtime token longer than needed to
  launch frpc.
- Do not assume an API `DELETE` removes a row. It transitions the tunnel to
  `revoked`. The bundled Caddy request gate blocks subsequent HTTP requests;
  requests already admitted, including upgraded connections, may drain.
- If you replace Caddy or the data-plane routing, preserve its fail-closed
  authorization subrequest before every wildcard HTTP request. It permits
  non-elapsed `active`/`closed` rows because close callbacks are asynchronous,
  but denies `reserved` and terminal rows. Direct wildcard proxying to frps
  bypasses immediate revocation and expiry checks.
- Fork the service if you need to change its internal behavior. Vendoring an
  `internal/` package is not a stable extension contract.

## Possible future APIs

The following are design candidates, not implemented promises:

- admin user and API-token lifecycle endpoints;
- cross-user tunnel inventory for an operator console;
- webhook or event-stream delivery;
- a safe audit-event read API; and
- provenance fields such as `created_by`.

Any future endpoint should remain additive, owner-aware, and subject to the
same JSON error, request-size, audit, and idempotency rules as the current API.
