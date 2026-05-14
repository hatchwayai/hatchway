# Hatchway Development Plan

Source of truth for implementation progress. Edit checkboxes in place as you complete work. The design itself lives in `DESIGN.md` — this file tracks what is built vs. not built.

## How to use this file

- Each task is a `- [ ]` checkbox. Tick it (`- [x]`) when the work is done **and** the exit criteria for the containing phase are still satisfied.
- Phases are roughly sequenced. Within a phase, tasks marked **(parallel)** can be done concurrently.
- When the design changes, reflect it in `DESIGN.md` first, then add/strike tasks here.
- When resuming after a gap: re-read `DESIGN.md` (it may have drifted), scan this file's "Open questions" section, then pick the lowest unchecked task in the lowest unfinished phase.
- **Every phase's exit criteria includes passing unit tests for that phase's code.** Do not defer all testing to Phase 10 — write tests for security-critical paths (token verify, state machine, plugin authorization) alongside the implementation. Phase 10 covers comprehensive integration and E2E testing.

## Status snapshot

- Current phase: **Phase 0 — not started**
- Last updated: 2026-05-05
- Implementation has not begun. Design is frozen at the version in `DESIGN.md` as of this date.

---

## Phase 0 — Repo bootstrap

**Goal:** an empty `hatchway` binary that builds, tests, and lints, with the directory layout from DESIGN.md in place.

**Exit criteria:** `go build ./...`, `go test ./...`, and the linter all pass on a fresh clone. `hatchway --help` runs and prints a top-level command list.

- [ ] `git init` and add `.gitignore` (Go, IDE, `dist/`, `.env`).
- [ ] `go mod init github.com/<owner>/hatchway` (pick the canonical module path before any imports — renaming later is painful).
- [ ] Create the directory tree from `DESIGN.md` § Codebase and Packaging Strategy. Empty `.gitkeep` files in leaf dirs to commit them.
- [ ] `cmd/hatchway/main.go` — minimal Cobra root with `server` and `auth` subcommand stubs. Include a `hatchway version` subcommand that prints the embedded version (set via `ldflags` in the Makefile).
- [ ] Pick CLI library: confirm Cobra (recommended) vs urfave/cli. Document the choice in this file.
- [ ] Pick logger: zap vs zerolog. Document the choice.
- [ ] `Makefile` (or `Taskfile`) with `build`, `test`, `lint`, `fmt`, `tidy`.
- [ ] `golangci-lint` config tuned for Go 1.22+ defaults.
- [ ] CI: GitHub Actions workflow that runs `make lint test build` on push/PR.

## Phase 1 — Database & migrations

**Goal:** schema from `DESIGN.md` § PostgreSQL Data Model is creatable and revertible via the migration tool.

**Exit criteria:** `hatchway server init --dry-run` connects, runs migrations against an empty Postgres, and reports success. All tables, indexes, and foreign keys exist.

- [ ] Pick migration tool: golang-migrate (recommended for SQL files) vs goose. Document.
- [ ] Add `pgx/v5` dependency.
- [ ] `internal/db` package: connection pool, ping, transaction helpers.
- [ ] Migration `0001_init.sql`: `users`, `api_tokens`, `tunnels`, `tunnel_runtime_tokens`, `tunnel_events`, `idempotency_keys`. Match column types exactly to DESIGN.md. Note: `users.email` has a `UNIQUE` constraint; `tunnel_events.tunnel_id` is `NOT NULL`; `tunnels` has no `deleted_at` column (revoked status handles lifecycle).
- [ ] Indexes: `api_tokens(token_prefix)`, `tunnel_runtime_tokens(tunnel_id, token_prefix)`, `tunnels(user_id, status)`, `tunnels(expires_at) WHERE status IN ('reserved','active','closed')`, `tunnel_events(tunnel_id, created_at)`, `tunnel_events(created_at)` for the retention sweep.
- [ ] Migration `down` files for each `up`.
- [ ] Repository structs in `internal/models` for each table.
- [ ] Integration test harness that spins up Postgres via `testcontainers-go` (or a docker-compose fixture) and runs migrations up→down→up.

## Phase 2 — Server bootstrap & user/token management

**Goal:** an operator can run `hatchway server init` against a fresh DB and get a usable admin API token.

**Exit criteria:** `hatchway server init` → `hatchway server user create` → `hatchway server token create` chain works end-to-end on a clean DB. Tokens are hashed at rest, never logged.

- [ ] Config loader: env vars first, optional config file second. Twelve-factor. Include `HATCHWAY_API_READ_TIMEOUT` (default 30s) and `HATCHWAY_API_WRITE_TIMEOUT` (default 30s) for the API server.
- [ ] Token format: `sk_live_<base62>` for API tokens, `rt_<base62>` for runtime tokens. Implement a single `internal/tokens` package that mints and parses both. `token_prefix` = first 12 characters of the full token string (e.g. `sk_live_abc` from `sk_live_abcdef…`), used as a fast index lookup. Multiple tokens may share a prefix; hash comparison resolves collisions.
- [ ] Token hashing: argon2id (recommended) or bcrypt. Pin parameters in code; document why in a comment **(this is the rare case where a comment earns its keep)**.
- [ ] `hatchway server init`: runs migrations, creates one admin user, mints its first API token, prints the token to stdout exactly once. Refuses to run if any user already exists (use `--force` to override for dev).
- [ ] `hatchway server user create --email <e> --name <n>`.
- [ ] `hatchway server user list` (table + `--json`).
- [ ] `hatchway server token create --user <email|name> --name <label>`. Prints token once, then never again.
- [ ] `hatchway server token revoke <token_id>`.
- [ ] `hatchway server tunnels list` (admin view across all users).
- [ ] Unit tests for token mint/parse/hash/verify; round-trip and tampering tests.

## Phase 3 — Control-plane HTTP server skeleton

**Goal:** a chi router with auth, idempotency, rate limit, error envelope, and `/healthz`. No tunnel routes yet.

**Exit criteria:** `hatchway server run` listens on `:9000` (API) and `:9001` (plugin), the API rejects unauthenticated requests, idempotent replays return the cached response, and the rate limiter throttles excess creates.

- [ ] HTTP framework: chi (recommended) vs gin/fiber. Document.
- [ ] Two listeners: `HATCHWAY_API_ADDR` (`:9000`) and `HATCHWAY_FRPS_PLUGIN_ADDR` (`:9001`). Plugin port refuses any path other than `/frp/plugin`. Apply `HATCHWAY_API_READ_TIMEOUT` and `HATCHWAY_API_WRITE_TIMEOUT` to the API server.
- [ ] Bearer-token auth middleware: parse `Authorization: Bearer sk_live_…`, look up by `token_prefix`, verify hash, attach `user_id` + `token_id` to request context. Constant-time compare on the hash check.
- [ ] JSON error envelope `{ "error": { "code": "...", "message": "..." } }`. Map common error codes upfront: `UNAUTHENTICATED`, `FORBIDDEN`, `NOT_FOUND`, `RATE_LIMITED`, `QUOTA_EXCEEDED`, `INVALID_REQUEST`, `LOCAL_PORT_NOT_REACHABLE`, `INTERNAL`.
- [ ] Idempotency middleware: read `Idempotency-Key`, hash request body, look up `(token_id, key)` in `idempotency_keys`. On hit, return the cached response. On miss, capture and store after the handler returns 2xx. Cap cached response body at 4 KB — larger responses are not cached but the request still proceeds.
- [ ] Rate-limit middleware: in-memory token-bucket per `token_id`. **Note:** in-memory means rates are per-process; for multi-replica deployments this needs to move to Postgres or Redis. Acceptable for MVP; document as a known limitation here.
- [ ] `/healthz` (cheap, always 200) and `/readyz` (DB ping + optional frps TCP dial on `HATCHWAY_FRPS_HEALTH_ADDR`). If frps is unreachable, `/readyz` returns 503 so operators know tunnels are down even if the API is up.
- [ ] Structured request logs (trace ID, route, status, latency, token_id).

## Phase 4 — Tunnel CRUD API + lifecycle

**Goal:** the four tunnel endpoints from `DESIGN.md` § API Design work, the lifecycle state machine is enforced, and a reaper expires tunnels past their TTL.

**Exit criteria:** integration test creates → lists → gets → deletes a tunnel; an expired tunnel transitions to `expired` within one reaper interval; concurrent-tunnel and create-rate quotas reject excess requests with the right error code.

- [ ] Tunnel ID generator: 16 chars from the Crockford-style alphabet `abcdefghjkmnpqrstuvwxyz23456789`, prefixed `t-`. Use `crypto/rand`. Add a uniqueness retry loop against the DB (collisions ~impossible at 80 bits but the loop is cheap).
- [ ] Runtime token generator: reuse `internal/tokens`.
- [ ] `POST /v1/tunnels`: validate body, check concurrent-tunnel quota, mint tunnel_id + runtime token, insert `tunnels` row in `reserved`, insert `tunnel_runtime_tokens` row, return the response shape from DESIGN.md including `frp.server_token` (bootstrap secret).
- [ ] `GET /v1/tunnels` (paginated; `limit` default 50, max 100; `cursor` for next page). Owner scope only.
- [ ] `GET /v1/tunnels/{id}`. 404 if not owned by the caller (do not leak existence).
- [ ] `DELETE /v1/tunnels/{id}`: transition to `revoked`, revoke runtime token, mark for closure (the actual frps disconnect happens via plugin or out-of-band).
- [ ] State machine: enforce the transition table from `DESIGN.md` § Tunnel Lifecycle. A single `internal/server/tunnels` function `Transition(ctx, id, event)` is the only place that writes `tunnels.status`.
- [ ] Reaper: background goroutine. Every 30s: `UPDATE tunnels SET status='expired' WHERE expires_at < now() AND status IN ('reserved','active','closed')`. Emit `tunnel_events`.
- [ ] Quota enforcement: `HATCHWAY_MAX_CONCURRENT_TUNNELS` (default 5) on create.
- [ ] TTL cap: `HATCHWAY_MAX_TTL` (default 24h) on create.
- [ ] Validate `local_host` is `127.0.0.1` unless explicitly opted out (future flag).

## Phase 5 — frps plugin endpoint

**Goal:** frps callbacks land on `:9001` and authorize tunnels exactly per `DESIGN.md` § frps Plugin Authorization.

**Exit criteria:** with a real frps configured against the plugin, an frpc that presents a valid runtime token logs in and registers; an frpc with no/invalid token is rejected at `Login`; a token whose tunnel was deleted is rejected; a `NewProxy` whose subdomain doesn't match the tunnel ID is rejected.

- [ ] `/frp/plugin` handler dispatches by `op` field. Decode payloads per frp's plugin protocol (see frp docs; pin a frp version in go.mod or in a constants file). Require `X-Hatchway-Plugin-Secret` header matching `HATCHWAY_PLUGIN_SECRET` — reject all other requests.
- [ ] `Login`: read `metadatas.runtime_token`, look up by prefix, verify hash, check `expires_at > now()` and `revoked_at IS NULL`. On success, bump `last_used_at` and `use_count`. Return `Reject` with a generic message on failure (don't enumerate why).
- [ ] `NewProxy`: validate `proxy_name == tunnel_id`, `subdomain == tunnel_id` for HTTP, owner matches the runtime token's tunnel, `proxy_type` allowed (HTTP only in MVP), tunnel not in `expired` or `revoked`. Transition `reserved → active` (or `closed → active` on reconnect).
- [ ] `CloseProxy`: transition to `closed` if currently `active`. Do not transition out of terminal states.
- [ ] `NewUserConn` (optional, behind `HATCHWAY_LOG_USER_CONNS`): write a `tunnel_events` row. Off by default to avoid table bloat.
- [ ] All plugin handlers must complete within 1s (frp will time out otherwise). Add a request deadline.
- [ ] Defense-in-depth: `HATCHWAY_PLUGIN_SECRET` is required (not optional). frps passes it as a plugin header; the handler rejects requests without it. This is documented in DESIGN.md.

## Phase 6 — Client CLI

**Goal:** `hatchway http <port>` works end-to-end against a running server.

**Exit criteria:** `hatchway http 3000 --ttl 15m` prints a working public URL. Ctrl-C cleans up. `--json` produces machine-readable output. Token storage respects 0600 mode.

- [ ] `hatchway auth set-token --server <url> <token>`: write `${XDG_CONFIG_HOME:-$HOME/.config}/hatchway/credentials.json` mode 0600, parent 0700. Refuse to overwrite a file with looser permissions without confirmation. Store both the token and server URL. Error if no server is configured (via `--server`, `HATCHWAY_SERVER` env, or existing `credentials.json`).
- [ ] `hatchway auth whoami`: call `/v1/me` (add this endpoint in Phase 4 if not already).
- [ ] `hatchway auth logout`: delete the credential file.
- [ ] `HATCHWAY_TOKEN` and `HATCHWAY_SERVER` env overrides (take precedence over file).
- [ ] HTTP client: typed wrappers around the four tunnel endpoints with timeouts and retry on idempotent operations.
- [ ] `--ttl` parser accepting `5m`, `15m`, `30m`, `1h`, `24h` (use `time.ParseDuration` after lowercasing).
- [ ] `hatchway http <port> [--ttl] [--json]`:
  - [ ] Pre-flight: `net.Dial` to `127.0.0.1:<port>` to surface `LOCAL_PORT_NOT_REACHABLE` early.
  - [ ] Generate idempotency key.
  - [ ] Call `POST /v1/tunnels`.
  - [ ] Generate `frpc.toml` in a temp dir; mode 0600.
  - [ ] Spawn `frpc` as a subprocess; pipe stderr to our logger at debug level.
  - [ ] Print the public URL to stdout (or JSON if `--json`).
  - [ ] On SIGINT/SIGTERM: kill frpc, call `DELETE /v1/tunnels/{id}` (best-effort, short timeout), exit 0.
  - [ ] If frpc exits unexpectedly: log, attempt one restart with backoff, then surface error and clean up.
- [ ] `hatchway list` / `hatchway status <id>` / `hatchway delete <id>` (table + `--json`).
- [ ] `hatchway tcp <port>` / `hatchway udp <port>` — stubbed in Phase 6 with a clear "not yet supported" error; implement in a post-MVP phase.

## Phase 7 — frp packaging

**Goal:** the right frpc/frps binaries ship with the right artifacts.

**Exit criteria:** `dist/hatchway-<os>-<arch>.tar.gz` (client) contains `hatchway` and `frpc` and runs without further downloads. The server Docker image contains `frps` and `hatchway`.

- [ ] Pin frp version. Document the exact version (e.g. `v0.58.x`).
- [ ] Build script that downloads the official frp release for each target triple, verifies checksums, and stages it next to the `hatchway` binary.
- [ ] Goreleaser config that emits client tarballs for darwin/linux × amd64/arm64 with the bundled `frpc`.
- [ ] Subprocess wrapper (`internal/frp/process.go`): start, stop, wait, log capture, restart-with-backoff. Used by both client (frpc) and server (frps in subprocess mode).
- [ ] frps subprocess mode flag: `HATCHWAY_FRPS_MODE=subprocess|external` (default `external` for production, `subprocess` for `hatchway server run --dev`).

## Phase 8 — Server Docker deployment

**Goal:** `docker compose up` brings up Postgres + hatchway-server + frps + Caddy and a manually-issued admin token can create a working tunnel.

**Exit criteria:** a fresh VPS with only Docker installed can clone the repo, set three env vars, run `docker compose up -d`, and have a working tunnel terminate at `https://<id>.tunnel.example.com`.

- [ ] `Dockerfile` for `hatchway:latest` (multi-stage; final stage `gcr.io/distroless/static-debian12` or `alpine`). Include a `USER nonroot` directive — do not run the service as root inside the container.
- [ ] `Dockerfile` for `hatchway-frps:latest` if separate-container mode (or document reuse of upstream `snowdreamtech/frps`).
- [ ] Custom Caddy `Dockerfile` built with `xcaddy` and the chosen DNS provider plugin (default example: Cloudflare).
- [ ] `docker-compose.yml` matching `DESIGN.md` § Docker Strategy. Verify the plugin port (`:9001`) is **not** in any `ports:` section.
- [ ] `Caddyfile` matching DESIGN.md.
- [ ] `frps.toml` template with bootstrap secret pulled from env at container start.
- [ ] `.env.example` listing every `HATCHWAY_*` env var with sensible defaults and brief comments.
- [ ] Smoke test script: `scripts/smoke.sh` runs the full create-tunnel + curl-public-URL flow against a local compose stack.

## Phase 9 — Reaper, retention, ops

**Goal:** the system stays healthy unattended.

**Exit criteria:** after a 24h soak run, no table grows unboundedly, no goroutines leak, expired tunnels are reclaimed promptly.

- [ ] Tunnel reaper (covered in Phase 4) — verify it runs on the server-side schedule.
- [ ] `tunnel_events` retention sweeper: every hour, delete rows older than 30 days (`HATCHWAY_EVENTS_RETENTION_DAYS`).
- [ ] `idempotency_keys` sweeper: every hour, delete rows older than 24h.
- [ ] Admin kill switch: `POST /v1/admin/tunnels/{id}/revoke` (admin-only). Wire it to the same `Transition` path as user-initiated delete.
- [ ] Metrics endpoint (`/metrics`, Prometheus format) at minimum: tunnels by status, plugin op counts/latency, rate-limit rejections, DB pool stats. Listen on the plugin port (internal) so it isn't public.
- [ ] Graceful shutdown: drain HTTP, stop reapers, close DB, kill subprocesses, all within 30s.

## Phase 10 — Tests

**Goal:** a green CI run gives reasonable confidence the system works.

**Exit criteria:** unit, integration, and one end-to-end smoke test all run in CI. Coverage on the security-critical paths (token verify, plugin authorization, state machine) is high.

- [ ] Unit tests: token mint/verify, ID generator (alphabet, length, prefix, no confusables), state machine transitions (table-driven), TTL parser, idempotency hash.
- [ ] Integration tests against real Postgres (testcontainers): all four CRUD endpoints, idempotency replay, rate-limit reject, quota reject, reaper run.
- [ ] Plugin authorization tests: every Reject branch in DESIGN.md § frps Plugin Authorization is covered by a test that proves it rejects.
- [ ] End-to-end smoke (CI job): boot the compose stack, create a tunnel against a local origin, curl the public URL, verify the response, tear down.
- [ ] Fuzz target on the tunnel-ID parser/regex.

## Phase 11 — Docs & release

**Goal:** a stranger can self-host Hatchway from the README in under an hour.

**Exit criteria:** README quickstart works on a clean machine; `goreleaser release --snapshot` produces all artifacts.

- [ ] `README.md`: 60-second pitch, quickstart, link to DESIGN.md and self-host guide.
- [ ] `docs/self-host.md`: VPS prep, DNS records, env vars, first-token bootstrap, common pitfalls (cert renewal, port range warnings).
- [ ] `docs/api.md`: endpoint reference generated or hand-written from the chi router.
- [ ] `docs/cli.md`: every subcommand with example output.
- [ ] Goreleaser config validated end-to-end.
- [ ] License file (MIT or Apache-2.0).
- [ ] Tag `v0.1.0`, push, verify the release artifacts attach.

## Post-MVP backlog (do not start until all phases above are checked)

- [ ] TCP and UDP tunnel types (revisit the iptables port-range tradeoff first).
- [ ] OAuth login / web dashboard.
- [ ] Per-tunnel basic auth or IP allowlist.
- [ ] Max-visits-per-tunnel.
- [ ] Custom domains.
- [ ] Per-tunnel bandwidth and connection caps.
- [ ] `hatchway-skills` repo for AI-agent skills.
- [ ] Multi-replica server (move idempotency cache and rate limiter out of process memory).
- [ ] Audit log retention beyond `tunnel_events`.

## Open questions (resolve before starting the relevant phase)

- [ ] Module path / GitHub owner for `go mod init` (Phase 0).
- [ ] CLI library: Cobra vs urfave/cli (Phase 0).
- [ ] Logger: zap vs zerolog (Phase 0).
- [ ] HTTP framework: chi vs gin vs fiber (Phase 3).
- [ ] Token hash: argon2id vs bcrypt (Phase 2).
- [ ] frp version pin (Phase 7).
- [ ] Default DNS provider for the custom Caddy image (Phase 8).
- [ ] License (Phase 11).
