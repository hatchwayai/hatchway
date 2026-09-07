# CLI Reference

The `hatchway` binary contains both client commands and trusted server-side
operator commands. This page documents the current command tree.

## Installation

This repository does not yet have a published release tag. Build the current
client from source:

```bash
git clone https://github.com/hatchwayai/hatchway.git
cd hatchway
make build
```

The binary is written to `dist/hatchway`. `hatchway http` also needs an
executable `frpc` (the repository pins frp v0.69.0). Put `frpc`:

1. next to the `hatchway` executable; or
2. in a directory on `PATH`.

The sibling copy takes precedence. If neither is found, tunnel creation fails
and the server reservation is revoked during cleanup.

The GoReleaser configuration is prepared to bundle side-by-side `hatchway` and
`frpc` binaries in future client archives.

## General behavior

```bash
hatchway --help
hatchway <command> --help
```

Commands that support `--json` emit machine-readable **success** output to
stdout. CLI validation and transport errors remain plain text on stderr.
Operational logs emitted through `slog` are JSON on stderr.

The HTTP client makes up to three attempts, with exponential backoff, for
transport failures and HTTP 502/503/504 responses when a request is safe to
retry: `GET`, `HEAD`, `DELETE`, or a request carrying an `Idempotency-Key`.
A keyed request also retries a `409` that carries `Retry-After`, which marks a
temporary in-flight reservation; permanent same-key conflicts are returned
immediately. If a keyed create receives a truncated or malformed `201` body,
the client replays the same request once to recover the committed response and
one-time runtime token. Server-provided retry delays are honored up to five
seconds.

## Client commands

### `hatchway version`

Print the version embedded at build time. A direct development build commonly
prints a Git-derived value from the Makefile; an unversioned `go build` prints
`dev`.

### `hatchway auth set-token [token]`

Store an API endpoint and token:

```bash
hatchway auth set-token \
  --server https://api.example.com \
  sk_live_...
```

If the token argument is omitted, the command reads it without terminal echo.
`--server` is required unless `HATCHWAY_SERVER` is set. The URL must be an
`http://` or `https://` origin without credentials, a path, query, or fragment.

Credentials are stored atomically in
`${XDG_CONFIG_HOME:-$HOME/.config}/hatchway/credentials.json`. The directory
must not be accessible by group/other users and the file must not be a
symlink; the file is written with mode `0600`.

### `hatchway auth whoami`

Validate the resolved credentials:

```console
$ hatchway auth whoami
Authenticated as 550e8400-e29b-41d4-a716-446655440000 on https://api.example.com
```

```console
$ hatchway auth whoami --json
{"server":"https://api.example.com","user_id":"550e8400-e29b-41d4-a716-446655440000"}
```

The API also returns `is_admin`, but the current CLI output intentionally
contains only `user_id` and `server`.

### `hatchway auth logout`

Remove the stored credentials file. Environment variables, if set, are not
changed.

### `hatchway http <port>`

Create and run an HTTP tunnel from a public URL to `127.0.0.1:<port>`:

```console
$ hatchway http 3000 --ttl 15m
https://t-abc3x7km9w2p4rng.tunnel.example.com
```

| Input | Default | Contract |
| --- | --- | --- |
| `<port>` | — | Required integer from 1 through 65535 |
| `--ttl` | server default | Positive Go duration, at least one whole second; when omitted the server chooses the lower of one hour and its configured maximum |
| `--json` | `false` | Emit the initial tunnel result as JSON |

Current JSON output is:

```json
{
  "public_url": "https://t-abc3x7km9w2p4rng.tunnel.example.com",
  "status": "reserved",
  "tunnel_id": "t-abc3x7km9w2p4rng"
}
```

The command:

1. checks that `127.0.0.1:<port>` is reachable;
2. creates a tunnel with a fresh UUID idempotency key;
3. validates the returned runtime/frp configuration;
4. writes a temporary `frpc.toml` with mode `0600`;
5. finds `frpc` next to `hatchway`, then on `PATH`;
6. runs `frpc -c <temporary-config>`; and
7. revokes the tunnel with a three-second cleanup request on every exit path
   after creation.

SIGINT/SIGTERM cancels frpc. Unexpected frpc failures are restarted at most
three times with 2, 4, then 6 second delays. A clean frpc exit also ends the
command and triggers revocation.

### `hatchway list`

List **all** tunnels owned by the authenticated user, including `expired` and
`revoked` records:

```console
$ hatchway list
ID                    TYPE  STATUS   URL
t-abc3x7km9w2p4rng    http  active   https://t-abc3x7km9w2p4rng.tunnel.example.com
t-old3x7km9w2p4rng    http  expired  https://t-old3x7km9w2p4rng.tunnel.example.com
```

The CLI follows every API pagination cursor before printing, so `--json`
returns a JSON array rather than the API's paginated envelope.

### `hatchway delete <tunnel_id>`

Revoke an owned tunnel:

```console
$ hatchway delete t-abc3x7km9w2p4rng
Tunnel t-abc3x7km9w2p4rng revoked.
```

The database row is retained. Repeating the request for an already expired or
revoked owned tunnel succeeds.

### `hatchway tcp <port>` and `hatchway udp <port>`

These commands are placeholders and return a “not yet supported” error.
Hatchway currently creates HTTP tunnels only.

## Server commands

Server commands read PostgreSQL directly and assume trusted operator access.
They are not substitutes for end-user API authorization.

### `hatchway server init`

Apply embedded migrations and create a bootstrap admin plus its first API
token:

```console
$ hatchway server init --admin-email admin@example.com
Migrations applied.
Admin user created: admin@example.com
API token (save this — it won't be shown again):
sk_live_...
```

The token is written to stdout; progress is written to stderr.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--admin-email` | `admin@hatchway.local` | Bootstrap admin email |
| `--admin-name` | `admin` | Display name |
| `--force` | `false` | Allow creation when users already exist |
| `--yes`, `-y` | `false` | Skip the `--force` confirmation |

`--force` is additive: it does not erase users or revoke existing tokens. Use
a new unique email when adding another admin. `DATABASE_URL` is required.

### `hatchway server run`

Validate configuration, apply every embedded migration, verify the expected
schema, start the reaper/sweepers, then serve:

- the public API on `HATCHWAY_API_ADDR` (default `:9000`); and
- the internal callback/request-gate/metrics listener on
  `HATCHWAY_FRPS_PLUGIN_ADDR` (default `:9001`).

SIGINT/SIGTERM cancels background work and gives both HTTP servers up to 30
seconds to drain.

`--dev` selects `HATCHWAY_FRPS_MODE=subprocess`. In subprocess mode,
`HATCHWAY_FRPS_CONFIG_PATH` is required and `HATCHWAY_FRPS_BIN_PATH` defaults
to `frps`. If that frps process exits unexpectedly, the server stops instead
of silently continuing without a data plane. The default production mode is
`external`, used by Docker Compose.

### `hatchway server healthcheck`

Probe server readiness for container schedulers or scripts:

```bash
hatchway server healthcheck
hatchway server healthcheck --url http://127.0.0.1:9000/readyz --timeout 3s
```

The default URL is `http://127.0.0.1:9000/readyz` and the default timeout is
five seconds. The command requires a direct HTTP `200`, does not follow
redirects, drains/closes the bounded response, and returns concise errors that
do not echo URL credentials, query values, response bodies, or transport
details.

### `hatchway server user create`

```bash
hatchway server user create \
  --email alice@example.com \
  --name Alice \
  [--admin]
```

`--email` is required. The command prints the new user UUID.

### `hatchway server user list`

List every user. Add `--json` for a JSON array.

### `hatchway server token create`

```console
$ hatchway server token create --user alice@example.com --name laptop
Token created (d177c478-a89a-4a38-a2b0-b996029f92db). Save the value below — it won't be shown again:
sk_live_...
```

`--user` accepts a user UUID, email, or unique name. An ambiguous selector is
rejected; a UUID or unique email is safest. `--name` is required. Save both
the printed token ID and the one-time plaintext token.

### `hatchway server token list`

List token IDs and non-secret metadata:

```bash
hatchway server token list
hatchway server token list --user alice@example.com
hatchway server token list --json
```

`--user` filters by UUID or email. Plain output includes full token ID, user,
label, lookup prefix, revoked state, and creation time. Neither output reveals
the token body or digest.

### `hatchway server token revoke <token_id>`

Set `revoked_at` for one live API token. Use `server token list` to discover
the full token ID. A missing or already revoked ID returns an error.

### `hatchway server tunnels`

List tunnels across every user, including terminal rows. This is a direct
database operator view and does not require an API admin token. Add `--json`
for a JSON array.

## Environment variables

Invalid integers, booleans, or Go duration strings fail configuration loading
instead of silently falling back.

### Client

| Variable | Meaning |
| --- | --- |
| `HATCHWAY_SERVER` | API origin; overrides the saved value |
| `HATCHWAY_TOKEN` | API token; overrides the saved value |
| `XDG_CONFIG_HOME` | Optional base directory for `hatchway/credentials.json` |

### Server binary

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | — | PostgreSQL connection URL |
| `HATCHWAY_PLUGIN_SECRET` | for `server run` | — | Internal callback/gate path secret and idempotency-cache key material |
| `HATCHWAY_FRPS_AUTH_TOKEN` | for `server run` | — | Shared frps↔frpc bootstrap credential |
| `HATCHWAY_FRPS_DOMAIN` | for `server run` | — | Public frps hostname returned to clients |
| `HATCHWAY_TUNNEL_DOMAIN` | no | `tunnel.example.com` | Wildcard tunnel domain |
| `HATCHWAY_API_ADDR` | no | `:9000` | API listen address |
| `HATCHWAY_FRPS_PLUGIN_ADDR` | no | `:9001` | Internal callback/gate/metrics listen address |
| `HATCHWAY_MAX_CONCURRENT_TUNNELS` | no | `5` | Non-terminal tunnels per user |
| `HATCHWAY_MAX_TTL` | no | `24h` | Maximum tunnel TTL; must be at least one second |
| `HATCHWAY_RATE_CREATE_PER_MIN` | no | `10` | Create rate per API token and process |
| `HATCHWAY_MAX_REQUEST_BYTES` | no | `65536` | Body limit for `/v1` and plugin callbacks |
| `HATCHWAY_API_READ_TIMEOUT` | no | `30s` | API read timeout |
| `HATCHWAY_API_WRITE_TIMEOUT` | no | `30s` | API write timeout |
| `HATCHWAY_PLUGIN_TIMEOUT` | no | `2s` | Per-callback/request-gate deadline |
| `HATCHWAY_LOG_USER_CONNS` | no | `false` | Persist accepted `NewUserConn` audit events |
| `HATCHWAY_FRPS_MODE` | no | `external` | `external` or `subprocess` |
| `HATCHWAY_FRPS_BIN_PATH` | subprocess | `frps` | frps executable |
| `HATCHWAY_FRPS_CONFIG_PATH` | subprocess | — | frps TOML path |
| `HATCHWAY_EVENTS_RETENTION_DAYS` | no | `30` | Event retention |
| `HATCHWAY_IDEMPOTENCY_RETENTION_HOURS` | no | `24` | Idempotency replay retention |
| `HATCHWAY_RUNTIME_TOKEN_RETENTION_DAYS` | no | `7` | Retention after a runtime token is dead |

### Docker Compose and Caddy

These values are consumed by Compose/Caddy rather than by the bare binary:

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `HATCHWAY_DOMAIN` | yes | — | Base domain used to derive service domains |
| `HATCHWAY_API_DOMAIN` | no | `api.${HATCHWAY_DOMAIN}` | API hostname |
| `HATCHWAY_FRPS_DOMAIN` | no | `frps.${HATCHWAY_DOMAIN}` | frps hostname |
| `HATCHWAY_TUNNEL_DOMAIN` | no | `tunnel.${HATCHWAY_DOMAIN}` | Tunnel wildcard root |
| `POSTGRES_PASSWORD` | yes | — | PostgreSQL password |
| `POSTGRES_DB` | no | `hatchway` | Database name |
| `POSTGRES_USER` | no | `hatchway` | Database role |
| `CLOUDFLARE_API_TOKEN` | yes | — | DNS-01 credential with Zone Read and DNS Edit |
