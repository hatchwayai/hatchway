# Extending Hatchway

Hatchway is deliberately minimal: a token-authenticated tunnel control plane with no
dashboard, no SSO, no MFA, and no built-in user-management UX. If you want those
features, the recommended approach is **to build them around Hatchway, not into it**,
so the core stays small and upgradable.

## Using Hatchway in another project

Hatchway is **not a Go library**. Every package lives under `internal/`, which Go
forbids other modules from importing. There is no stable public Go API surface.

Use Hatchway as a **standalone service** and integrate with it over HTTP:

1. **Run it as a separate service** (via `docker-compose.yml` or the `hatchway`
   binary) and call its HTTP API from your project using an `sk_live_…` token.
   This is the intended integration model.
2. **Shell out to the `hatchway` CLI** if you only need occasional tunnel
   create/list operations — the CLI is JSON-first and scriptable.
3. **Fork** (not vendor) if you genuinely need to modify Hatchway's behavior.
   Keep it as its own repo/module so upstream changes remain mergeable.

The MIT license permits all three; the choice is about integration architecture,
not licensing.

## Adding SSO / OAuth / MFA / WebUI without forking

The clean pattern is to **build a separate "console" service alongside Hatchway**
and have it own all the auth and UX features, talking to Hatchway only via its
HTTP API. Hatchway stays as it is; the console becomes the human/SSO-facing layer.

Two complementary pieces:

### 1. A console app (your code, separate repo) — owns SSO / OAuth / MFA / WebUI

- Handles login via your IdP (Okta, Google, Authelia, Keycloak, …) and MFA.
- Holds a single admin `sk_live_…` token for Hatchway.
- Maintains its own `console_user ↔ hatchway_user` mapping. On first SSO login,
  it calls `/v1/admin/users` to provision the Hatchway user and stores the issued
  `sk_live_…` per console-user (or just acts on their behalf with the admin token).
- Serves the WebUI: list / create / revoke tunnels by calling `/v1/tunnels`,
  show events, etc.
- This is the cleanest split — Hatchway never learns about SSO, and you can
  upgrade Hatchway without merge pain.

### 2. Optionally, an auth proxy in front of Hatchway's API

Only needed if you also want SSO on the `hatchway` CLI flow.

- Drop `oauth2-proxy`, Authelia, or Caddy `forward_auth` in front of `:9000`.
- The proxy enforces SSO + MFA, then injects the right
  `Authorization: Bearer sk_live_…` header before forwarding to Hatchway.
- Useful if humans currently `hatchway http 3000` from laptops and you want that
  gated by SSO. Skip this if the CLI is only used by CI / agents with API tokens.

## What to avoid

- **Don't embed Hatchway as a Go library** — everything lives under `internal/`,
  there is no stable public API surface.
- **Don't proxy the frps plugin port (`:9001`)** — that is an internal control
  channel for frps, not for humans.
- **Don't share Hatchway's SQLite / Postgres directly** from the console. Go
  through the HTTP API so Hatchway's invariants (token hashing, idempotency,
  event log, quotas) stay enforced.

## Gaps you may hit (candidates for small additive upstream features)

These keep Hatchway minimal but make a console viable:

- A way to mint an API token on behalf of a user from the admin API (so the
  console does not have to store an admin token forever).
- Webhooks or an events stream so the console UI can update without polling.
- A `created_by` / source field on tunnels so the WebUI can show who made what
  when the admin token is the actual caller.
