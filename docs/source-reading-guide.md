# Reading the Hatchway Source — Top-Down Guide

This guide is for someone who has the repo open and wants to understand how
Hatchway works end-to-end without getting lost in the file tree. Read the
sections in order; each one names the files to open and what to look for.

If you only have ten minutes, read **§1 The one-paragraph mental model**,
**§2 Repo map**, and **§3 The request that explains everything**. The rest
fills in the corners.

## Prereqs

Have these two files visible while you read code — they are the authoritative
spec, and almost every file makes more sense once you've skimmed them:

- `DESIGN.md` — the contract. Sections "Design Invariants", "Tunnel
  Lifecycle", "Token Model", and "frps Plugin Authorization" are the
  most load-bearing.
- `PLAN.md` — the phased implementation plan. Phase numbering (0–11) is
  roughly the order code was written, so it doubles as a reading order.

## 1. The one-paragraph mental model

Hatchway is a **control plane**; frp is the **data plane**. The Hatchway
server owns Postgres rows for users, API tokens, tunnels, and per-tunnel
runtime tokens. It exposes two HTTP listeners: a public REST API on `:9000`
authenticated by `sk_live_…` tokens, and an internal `:9001` that frps calls
back into to authorize each frpc login and proxy registration. The CLI
binary `hatchway` runs in both roles — `hatchway server run` boots the
control plane, `hatchway http 3000` is a client that calls the API, writes a
temporary `frpc.toml`, and spawns frpc as a subprocess. Tunnel state lives
in Postgres and transitions through a small state machine
(`reserved → active → closed → active → expired|revoked`).

## 2. Repo map

```
cmd/hatchway/main.go            entrypoint — 12 lines, calls commands.Execute
internal/
  cli/
    commands/                   Cobra command tree (BOTH server and client)
      root.go                   wires every subcommand
      server.go                 server init/run/user/token/tunnels
      client.go                 auth, http, list, delete, tcp, udp
    client.go                   typed HTTP client used by client commands
    credentials.go              ~/.config/hatchway/credentials.json read/write
  config/config.go              env-var loader + Validate()
  db/
    db.go                       pgxpool wrapper, InTx, CheckSchema
    migrate.go                  golang-migrate embed.FS driver
    migrations/0001…0004        schema, admin flag, idempotency tweaks
  models/models.go              row structs (User, Tunnel, …)
  tokens/tokens.go              MintAPIToken / MintRuntimeToken / Verify
  server/
    api/                        HTTP plumbing only — no domain logic
      router.go                 chi routes + middleware chain + StartServer
      auth.go                   Bearer token middleware + AdminOnly
      context.go                request-scoped user_id, token_id, is_admin
      errors.go                 JSON error envelope + ErrXxx constants
      idempotency.go            (token_id, key) reservation + reply cache
      ratelimit.go              in-memory per-token token bucket
      metrics.go                Prometheus counters, exposed on :9001/metrics
      health.go                 /healthz, /readyz (DB ping)
    tunnels/                    domain logic for tunnel CRUD + lifecycle
      handlers.go               POST/GET/LIST/DELETE /v1/tunnels handlers
      id.go                     GenerateTunnelID (Crockford alphabet)
      lifecycle.go              Transition(), reaper, sweepers
    plugin/handler.go           /frp/plugin/{secret} — frps callbacks
  frp/process.go                subprocess wrapper (start/stop/restart)
Caddyfile, docker-compose.yml,
Dockerfile*, frps.toml.tmpl     deployment artifacts (see docs/self-host.md)
docs/                           operator and reference docs
```

Three rules will save you time:

1. **`internal/server/api/` is plumbing only.** It doesn't know what a
   tunnel is. Anything tunnel-specific lives in `internal/server/tunnels/`.
2. **Import cycles are broken by callback functions.** `plugin/handler.go`
   takes a `TransitionFunc` parameter; `api/router.go` takes a
   `PluginHandlerFactory`. Wiring happens in `cli/commands/server.go`
   inside `serverRunCmd`. If you're hunting "who calls what", start there.
3. **Everything under `internal/` is unimportable from outside the module.**
   See `docs/extending.md` — Hatchway is a service, not a Go library.

## 3. The request that explains everything

Trace **one `hatchway http 3000` invocation** end-to-end. Every important
file is touched.

### 3a. Client side

1. `cmd/hatchway/main.go` → `internal/cli/commands/root.go:Execute` →
   `httpCmd()` in `internal/cli/commands/client.go`.
2. `httpCmd` resolves credentials (`cli.ResolveCredentials` in
   `internal/cli/credentials.go`: env first, then
   `${XDG_CONFIG_HOME:-$HOME/.config}/hatchway/credentials.json`, refusing
   loose perms).
3. Pre-flight: `cli.CheckLocalPort` dials `127.0.0.1:3000` so a missing
   local service is reported before going over the network.
4. Mints a UUID Idempotency-Key, builds a `CreateTunnelRequest`, and calls
   `cli.Client.CreateTunnel` (`internal/cli/client.go`), which hits
   `POST /v1/tunnels` with `Authorization: Bearer sk_live_…` and the
   `Idempotency-Key` header.
5. On success it gets back `tunnel_id`, `public_url`, `runtime_token`, and
   an `frp.*` block. `generateFRPCConfig` renders these into a `frpc.toml`
   in a temp dir (mode `0600`), then spawns `frpc -c <path>` via
   `exec.CommandContext`.
6. Signal handling and frpc restart-with-backoff (up to
   `maxFRPCRestarts = 3`) live in the same function. On SIGINT/SIGTERM the
   `cleanup` closure cancels the frpc context and best-effort calls
   `DELETE /v1/tunnels/{id}` with a 3 s timeout.

### 3b. Server side (control plane)

1. `cli/commands/server.go:serverRunCmd` is the wiring point. Read it once
   slowly — it sets up the cancellation context, opens the DB, builds the
   `tunnelRoutes` registrar and `pluginHandler` factory, starts the reaper
   and sweepers, optionally spawns frps as a subprocess, then calls
   `api.StartServer`.
2. `internal/server/api/router.go:NewRouter` builds the chi router. The
   middleware chain on `/v1` is (in order):

   ```
   RequestLogMiddleware       ← outermost (slog with trace_id, status, latency)
     MaxBodySize               ← per-route cap from cfg.MaxRequestBytes
       AuthMiddleware          ← Bearer → token_prefix lookup → argon2id verify
         RateLimitMiddleware   ← in-memory token bucket per token_id
           IdempotencyMiddleware  ← (token_id, key) reservation, 4 KB cache cap
             handler
   ```

   The order matters: rate-limit before idempotency means a replay still
   counts against the bucket, and idempotency runs *after* auth so it can
   key on `token_id`. `StartServer` also stands up the plugin listener on
   `:9001` with two endpoints: `/frp/plugin/{secret}` and `/metrics`.

3. `internal/server/tunnels/handlers.go:CreateTunnel` is the handler:
   validate body → `CountActiveTunnels` quota check (`MaxConcurrent`) →
   `GenerateTunnelID` with a 3-try uniqueness loop against the DB →
   `tokens.MintRuntimeToken` → INSERT into `tunnels` (status=`reserved`)
   and `tunnel_runtime_tokens` → emit `tunnel_events` → respond with the
   shape DESIGN.md "Create Tunnel" documents. The bootstrap `auth.token`
   secret returned as `frp.server_token` is `cfg.FRPSAuthToken` — read the
   comments here for why that is **not** the same thing as
   `cfg.PluginSecret`.

4. `internal/server/api/idempotency.go` is worth reading as a unit. It uses
   Postgres `INSERT … ON CONFLICT DO NOTHING` as a one-row reservation
   lock: the winner runs the handler and writes back `response_status` and
   `response_body`; concurrent retries either replay the cached row, get a
   `409` if the original is still in flight, or get a `409` for body
   mismatch on the same key.

### 3c. Data plane (frps → plugin callback)

When frpc connects to frps, frps issues two HTTP callbacks to
`/frp/plugin/{secret}` on `:9001`. `internal/server/plugin/handler.go`:

1. `Handler` (the factory) bakes in `cfg.PluginTimeout` and wraps every
   request in a deadline context. The path secret check is the first gate
   — it must match `cfg.PluginSecret` exactly.
2. Dispatch by `?op=` query param:
   - `Login` — parse `metas.runtime_token`, look it up by 12-char prefix
     (multiple rows possible — iterate and call `tokens.VerifyToken` on
     each), reject if revoked or expired, bump `last_used_at` and
     `use_count`.
   - `NewProxy` — same lookup, then assert `proxy_name == tunnel_id`,
     `proxy_type == "http"`, `subdomain == tunnel_id`, no `custom_domains`,
     tunnel not terminal. Then `transitionFn(…, "NewProxy")` —
     `reserved → active` or `closed → active`.
   - `CloseProxy` — if currently `active`, transition to `closed`. Other
     states are no-ops; never blocks frps.
   - `NewUserConn` — gated by `HATCHWAY_LOG_USER_CONNS`; writes a
     `tunnel_events` row.
   - `Ping`, `NewWorkConn` — accept unchanged.

Every rejection returns a generic reason; specific failure modes are only
logged. This matters — it's the security boundary for tunnel ownership.

### 3d. The state machine

`internal/server/tunnels/lifecycle.go` is the **only** writer of
`tunnels.status`. `validTransitions` is the whole table; everything goes
through `Transition(ctx, pool, id, event)`. Read this map alongside the
DESIGN.md "Tunnel Lifecycle" diagram — they must match.

`StartReaper` ticks every 30 s and does the only transition users can't
trigger: `… → expired`. It uses `RETURNING id` so it can emit one event per
expired tunnel without an extra round-trip. `StartSweepers` runs hourly and
deletes `tunnel_events` older than `HATCHWAY_EVENTS_RETENTION_DAYS` and
`idempotency_keys` older than `HATCHWAY_IDEMPOTENCY_RETENTION_HOURS`.

## 4. Recommended reading order

If you want to read every file once and have it stick, do it in this order
— roughly inside-out from the data model:

1. `DESIGN.md` "Design Invariants" + "Tunnel Lifecycle" + "Token Model".
2. `internal/db/migrations/0001_init.up.sql` — the schema is the spine of
   the system. Open `0002`/`0003`/`0004` to see how it evolved.
3. `internal/models/models.go` — the Go structs for those tables.
4. `internal/tokens/tokens.go` — token format, argon2id PHC, legacy
   fallback. This is security-critical; read the comments.
5. `internal/server/tunnels/id.go` — Crockford alphabet, 80 bits.
6. `internal/server/tunnels/lifecycle.go` — state machine + reaper +
   sweepers. The whole transition policy fits on one screen.
7. `internal/server/api/auth.go` — token prefix lookup → hash verify →
   request context. Then `context.go` and `errors.go` for the small
   helpers used everywhere.
8. `internal/server/api/idempotency.go` — read the Stripe-style
   reservation pattern; it's the trickiest middleware.
9. `internal/server/api/router.go` — see how the middleware stack and the
   two listeners are assembled.
10. `internal/server/tunnels/handlers.go` — Create/List/Get/Delete and the
    admin revoke endpoint.
11. `internal/server/plugin/handler.go` — the frps side of the contract.
12. `internal/config/config.go` — env vars + `Validate()` for required
    fields. Useful as a glossary of every knob.
13. `internal/frp/process.go` — subprocess lifecycle (start, stop with
    grace, restart with backoff).
14. `internal/cli/credentials.go` and `internal/cli/client.go` — CLI plumbing.
15. `internal/cli/commands/server.go` and `…/client.go` — the Cobra trees
    that glue everything together.

## 5. Where the load-bearing wiring lives

When you need to know "who passes X to Y", these are the seams:

- **`cli/commands/server.go:serverRunCmd`** — builds the cancel context,
  the DB pool, the `tunnelRoutes` chi registrar, and the
  `pluginHandler` factory. Plumbs `tunnels.Transition` into the plugin
  package as a `TransitionFunc` to break the import cycle (plugin can't
  import tunnels; tunnels imports api).
- **`server/api/router.go:NewRouter`** — defines middleware order and
  mounts `/v1/me`. Domain routes are added by registrars from
  `tunnels.RegisterRoutes`.
- **`server/api/router.go:StartServer`** — two `http.Server`s, two
  goroutines, a single `errCh`, graceful shutdown with 30 s deadline.
- **`server/api/metrics.go`** — `GlobalMetrics` is a package-level var
  consumed by the plugin handler (via the `MetricsIncrFunc` parameter)
  and by `lifecycle.Transition`. It is served on the plugin port
  (`:9001/metrics`), which is internal-only.

## 6. The two-token model in one place

This trips up every newcomer. Three secrets, three different lifetimes,
three different consumers:

| Secret                     | Env / source                    | Who holds it                   | Who validates it               | Lifetime                     |
| -------------------------- | ------------------------------- | ------------------------------ | ------------------------------ | ---------------------------- |
| `sk_live_…` API token      | minted by `server token create` | the user / their CLI           | `api/auth.go` middleware       | until revoked                |
| `rt_…` runtime token       | minted by `POST /v1/tunnels`    | frpc via `metas.runtime_token` | `plugin/handler.go` lookup     | until tunnel expires/revokes |
| `HATCHWAY_FRPS_AUTH_TOKEN` | operator env var                | frpc + frps as `auth.token`    | frps internally                | rotation only                |
| `HATCHWAY_PLUGIN_SECRET`   | operator env var                | frps callback URL path         | `plugin/handler.go` path check | rotation only                |

Crucially: the API response includes `frp.server_token` (the bootstrap
secret) but **never** the plugin secret. See the comment in
`tunnels/handlers.go:CreateTunnel` next to the `ServerToken` field.

## 7. Tests as a second reading path

Most packages have a `_test.go` next to the file. Two are especially good
as living documentation:

- `internal/server/plugin/handler_test.go` — every reject branch named in
  DESIGN.md "frps Plugin Authorization" has a corresponding test. Reading
  the table-driven cases is faster than re-reading the spec.
- `internal/server/tunnels/lifecycle.go` companion tests — every entry in
  `validTransitions` plus every invalid transition.
- `internal/tokens/tokens_test.go` — round-trip + tampering + PHC vs legacy
  hash format.
- `internal/server/tunnels/id_fuzz_test.go` — fuzz target on the tunnel-ID
  parser; useful as a tour of the legality rules.

## 8. Deployment side, in one page

You don't have to understand Docker to understand the code, but knowing how
the pieces are deployed helps. See `docs/self-host.md` for the long version;
the short version is:

- `docker-compose.yml` brings up four services: `postgres`,
  `hatchway-server` (this binary, `server run`), `frps` (or use upstream
  `snowdreamtech/frps`), `caddy` (custom built with `xcaddy` + a DNS
  provider plugin for wildcard TLS).
- `Caddyfile` reverse-proxies `api.example.com` to
  `hatchway-server:9000` and `*.tunnel.example.com` to `frps:8081` (vhost
  HTTP port). It must **not** route `:9001`.
- `frps.toml.tmpl` is rendered with `HATCHWAY_PLUGIN_SECRET` and
  `HATCHWAY_FRPS_AUTH_TOKEN` at container start. The `httpPlugins`
  section is what makes frps call back into `:9001`.
- Client-side: `dist/hatchway-<os>-<arch>.tar.gz` ships the `hatchway`
  binary alongside `frpc`. Goreleaser config in `.goreleaser.yml`.

## 9. Common "where is …?" answers

- The **HTTP error envelope** and code constants: `server/api/errors.go`.
- **Bearer token attached to request context**:
  `server/api/auth.go:authenticateRequest` → `context.go:ContextWithAdmin`.
- **The list cursor format**: `server/tunnels/handlers.go:listCursor`
  (base64-encoded `{created_at, tunnel_id}`).
- **The 4 KB idempotency cap**: `server/api/idempotency.go:maxCachedBodySize`
  and the `responseRecorder` that enforces it.
- **The argon2id parameters**: `internal/tokens/tokens.go` `argonTime`,
  `argonMemory`, `argonThreads`, `argonKeyLen` constants.
- **The 30 s graceful shutdown deadline**:
  `server/api/router.go:StartServer` (`context.WithTimeout(..., 30s)`).
- **frpc restart cap**: `cli/commands/client.go:maxFRPCRestarts = 3`.
- **Reaper interval**: hardcoded `30*time.Second` in
  `cli/commands/server.go:serverRunCmd`.

## 10. What to read after this

- `docs/api.md` — endpoint reference, useful when reading handler code.
- `docs/cli.md` — every subcommand with example output.
- `docs/extending.md` — what *not* to do (no Go library imports, no direct
  DB access from outside, no proxying `:9001`).
- `docs/self-host.md` — operator's guide; explains every env var and
  common pitfalls.
- `docs/deployment-notes.md` — captured field notes from real deploys.
