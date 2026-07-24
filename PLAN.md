# Hatchway Development Plan

> **Historical roadmap, reconciled 2026-07-24.** This file records how the
> current implementation was assembled and what release work remains. It is
> not the behavioral source of truth: use `DESIGN.md`, `docs/api.md`, and
> `docs/cli.md` for the current contract.

Checkboxes mean the corresponding capability exists in the repository. They
do not promise that a release artifact has been published. Unchecked
post-MVP items are proposals, not active API commitments.

## Status snapshot

- Implementation phases 0–10: complete for the current HTTP-only scope.
- Documentation reconciliation: complete in the working tree.
- Release/tag/publication: pending; this repository currently has no release
  tag.
- Current schema migrations: `0001` through `0006`.

## Phase 0 — Repository bootstrap

**Goal:** one Go module and one `hatchway` binary with repeatable development
commands.

- [x] Initialize `github.com/zydo/hatchway`.
- [x] Use Cobra for the command tree and `slog` for logging.
- [x] Add version injection through the Makefile and GoReleaser.
- [x] Add `build`, `test`, `test-short`, `coverage`, `lint`, `fmt`, and `tidy`
  Make targets.
- [x] Run build, lint, unit, integration, and vulnerability checks in CI as
  configured by the workflows.

## Phase 1 — PostgreSQL and migrations

**Goal:** an embedded, upgradeable control-plane schema.

- [x] Use pgx/v5 and golang-migrate with an embedded filesystem.
- [x] `0001`: users, API tokens, tunnels, runtime tokens, events, idempotency,
  and initial indexes.
- [x] `0002`: admin role.
- [x] `0003`: byte-exact idempotency response storage.
- [x] `0004`: in-flight/completed idempotency state and removal of unused event
  columns.
- [x] `0005`: runtime-token-prefix and idempotency-retention indexes.
- [x] `0006`: owner pagination and runtime-retention indexes plus database
  constraints for ports, states, use counts, and idempotency completion.
- [x] Make both `server run` and `server init` apply pending migrations.
- [x] Make startup/readiness verify the required schema tables.
- [x] Test migration up/down behavior against PostgreSQL.

There is no `server migrate` or `server init --dry-run` command. `server run`
is the migration-only upgrade path; `server init` additionally creates an
admin.

## Phase 2 — Operators, users, and API tokens

**Goal:** trusted host operators can bootstrap identities without a public
self-signup surface.

- [x] `server init` creates an admin and one-time token transactionally.
- [x] Make `server init --force` explicit, confirmed, and additive.
- [x] `server user create [--admin]` and `server user list [--json]`.
- [x] `server token create --user ... --name ...` resolves UUID, email, or a
  unique name and prints the token UUID.
- [x] `server token list [--user UUID|email] [--json]` exposes revocable IDs
  and non-secret metadata.
- [x] `server token revoke <token-id>` revokes one live token.
- [x] Mint high-entropy `sk_live_...` and `rt_...` values and store only a
  lookup prefix plus a versioned SHA-256 digest.
- [x] Preserve verification compatibility with legacy Argon2id PHC and
  pre-PHC bare-hex rows, with a global concurrency bound on legacy KDF work.

Public user/token administration endpoints remain out of scope.

## Phase 3 — HTTP control-plane skeleton

**Goal:** bounded, authenticated HTTP service behavior.

- [x] Public API listener on `:9000`; internal callback/gate/metrics listener on
  `:9001`.
- [x] `/healthz`, schema-aware `/readyz`, and
  `server healthcheck [--url] [--timeout]`.
- [x] Bearer authentication with prefix-collision-safe digest verification.
- [x] Request body, header, read, write, idle, and plugin deadline bounds.
- [x] JSON errors for handlers, unknown routes, and unsupported methods.
- [x] JSON application/request logs with request ID, status, latency, and
  authenticated token ID.
- [x] Per-token, process-local create rate limiting.
- [x] Idempotency reservations scoped by `(token_id, key)`.
- [x] Bind idempotency keys to method, escaped path, raw query, and body.
- [x] Encrypt replayable successful response bodies with AES-GCM derived from
  the plugin secret; retain legacy plaintext reads for the retention window.
- [x] Limit keys to 255 bytes and cached bodies to 4 KiB.

## Phase 4 — Tunnel API and lifecycle

**Goal:** owner-scoped short-lived HTTP tunnels with race-safe quotas.

- [x] Generate DNS-safe `t-` IDs with 80 random bits and collision retries.
- [x] `POST /v1/tunnels` with HTTP-only type, local endpoint validation, TTL,
  runtime credential, and frpc configuration.
- [x] Default omitted/zero TTL to the lower of one hour and
  `HATCHWAY_MAX_TTL`; require the configured maximum to be at least one
  second.
- [x] Enforce a per-user quota over `reserved`, `active`, and `closed` rows
  under a transaction-scoped PostgreSQL advisory lock.
- [x] Owner-scoped keyset pagination for `GET /v1/tunnels`; return all owned
  states rather than only active tunnels.
- [x] Owner-scoped `GET /v1/tunnels/{id}`.
- [x] Treat `DELETE /v1/tunnels/{id}` as idempotent revocation, not row
  deletion.
- [x] Admin-only cross-owner revoke.
- [x] Implement `reserved → active → closed → active`, with terminal
  `expired` and `revoked` transitions.
- [x] Record lifecycle events and expire elapsed rows in the reaper.

## Phase 5 — frps plugin authorization

**Goal:** keep tunnel ownership in Hatchway while frp owns traffic transport.

- [x] Protect `/frp/plugin/{secret}` with constant-time path-secret comparison
  on the internal listener.
- [x] Enforce configurable per-callback deadlines and body limits.
- [x] Validate runtime token expiry/revocation on `Login`.
- [x] On `NewProxy`, bind proxy name, HTTP type, and subdomain to the issued
  tunnel and reject custom domains.
- [x] Transition accepted registrations and clean disconnects.
- [x] Apply an active-state and database-clock TTL check whenever frp emits
  `NewUserConn`; optionally persist accepted callback events.
- [x] Gate every wildcard HTTP request through Hatchway in the bundled Caddy
  topology because frp does not emit `NewUserConn` for HTTP proxies.
- [x] Allow already admitted requests and upgraded connections to drain.
- [x] Accept `Ping` and `NewWorkConn` unchanged.

## Phase 6 — Native client CLI

**Goal:** one foreground command creates, runs, and tears down a tunnel.

- [x] Store credentials atomically with restrictive permissions and reject
  symlink/loose-permission files.
- [x] Validate and normalize the API origin.
- [x] Let environment credentials override the file.
- [x] `auth set-token`, `auth whoami [--json]`, and `auth logout`.
- [x] `http <port> [--ttl] [--json]` with a local-port preflight and a fresh
  idempotency key.
- [x] Let the server choose its capped default TTL when `--ttl` is absent.
- [x] Generate/validate a mode-`0600` frpc config.
- [x] Find frpc beside `hatchway`, then on `PATH`.
- [x] Restart an unexpectedly failed frpc up to three times.
- [x] Revoke the reservation after signals, normal exit, startup/configuration
  failures, or exhausted restarts.
- [x] Retry safe and explicitly idempotent HTTP requests on transport failure
  and 502/503/504.
- [x] `list [--json]` follows pagination and returns all owned states.
- [x] `delete` reports revocation.
- [x] Leave `tcp`/`udp` as explicit unsupported placeholders.

Structured CLI error output remains a future improvement; `--json` currently
covers selected success paths only.

## Phase 7 — frp packaging

**Goal:** pin and verify the frp data-plane binary.

- [x] Pin frp v0.69.0.
- [x] Verify downloaded client and server archives by SHA-256.
- [x] Build a dedicated non-root frps image.
- [x] Configure GoReleaser client archives with sibling `hatchway` and `frpc`
  files and separate server archives.
- [ ] Publish and verify the first tagged release artifacts.

## Phase 8 — Docker deployment

**Goal:** a conservative single-host production baseline.

- [x] Compose PostgreSQL, Hatchway, frps, and a Cloudflare-DNS-enabled Caddy.
- [x] Require Caddy authorization before proxying wildcard HTTP traffic to
  frps.
- [x] Render frps configuration from explicit environment variables.
- [x] Keep the internal Hatchway listener and frps vhost port off
  host-published ports.
- [x] Use non-root Hatchway/frps images, read-only application containers,
  dropped capabilities, PID limits, tmpfs scratch space, service health
  checks, graceful stop windows, and bounded JSON log rotation where
  applicable.
- [x] Isolate PostgreSQL on a backend network joined only by Hatchway.
- [x] Derive optional service domains from `HATCHWAY_DOMAIN`.
- [x] Pass server limit, timeout, logging, and retention settings through
  Compose.

## Phase 9 — Operations and retention

**Goal:** predictable expiry, cleanup, and minimum viable visibility.

- [x] Run the expiry reaper every 30 seconds.
- [x] Run event, idempotency, and dead-runtime-token retention hourly.
- [x] Expose plugin operation, plugin deadline, rate rejection, lifecycle,
  tunnel-state, and DB-pool metrics on the internal listener.
- [x] Gracefully drain both HTTP listeners for up to 30 seconds.
- [x] Stop background jobs and subprocess frps from the same cancellation
  context.

## Phase 10 — Verification

**Goal:** cover security and concurrency boundaries.

- [x] Unit tests for token mint/verify/legacy compatibility.
- [x] Unit tests for tunnel IDs/lifecycle transitions and fuzz coverage for
  the opaque pagination cursor parser.
- [x] API tests for auth, roles, body limits, JSON errors, pagination,
  idempotency, and replay encryption.
- [x] PostgreSQL integration tests for quota races, lifecycle/reaper/sweepers,
  and plugin callbacks.
- [x] CLI tests for credential safety, retries, cleanup, frpc lookup/config,
  and health checks.
- [x] Docker/Compose configuration validation in CI.

## Phase 11 — Documentation and release

- [x] Reconcile README, design, CLI, API, self-hosting, extension, deployment,
  and source-reading docs with the implementation.
- [x] Remove time-sensitive competitor comparison claims.
- [x] Mark future APIs and protocols explicitly.
- [ ] Create a release tag and publish checksummed client/server archives.
- [ ] Perform a clean-host installation test from the published artifacts.

## Post-MVP backlog

These are intentionally **not implemented**:

- TCP and UDP tunnel allocation, routing, and abuse controls
- custom domains
- per-tunnel HTTP authentication, allowlists, visit caps, and bandwidth caps
- distributed rate limiting for multi-replica deployments
- forced termination of already accepted user connections
- public admin APIs for user/token lifecycle or cross-user inventory
- event webhooks/streams and audit export
- OAuth/SSO/MFA and a web dashboard
- organizations/teams and billing
- separate agent-skill packages
- alternate DNS provider examples and origin-lockdown guidance

## Recorded design decisions

- **Data plane:** frp, used as standalone binaries rather than a Go library.
- **MVP protocol:** HTTP only.
- **Database:** PostgreSQL.
- **API authentication:** operator-minted bearer tokens; no self-signup.
- **TLS:** Caddy wildcard certificate via DNS-01 in the bundled deployment.
- **Production frps model:** separate container; subprocess mode is for
  development/alternate deployments.
- **Tunnel IDs:** server-generated and non-customizable.
- **Revocation semantics:** retain the audit row, revoke credentials, and block
  new connections; existing connections may drain.
