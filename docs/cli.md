# CLI Reference

## Installation

### Pre-built binaries

Download from the [GitHub releases](https://github.com/zydo/hatchway/releases) page. Client archives include the `frpc` binary.

### Build from source

```bash
git clone https://github.com/zydo/hatchway.git
cd hatchway
make build
# Binary at dist/hatchway
```

## Global options

```bash
hatchway [command] [flags]
```

Use `hatchway --help` to see all available commands.

## Commands

### `hatchway version`

Print the binary version.

```bash
$ hatchway version
0.1.0
```

### `hatchway auth`

Manage client authentication.

#### `hatchway auth set-token [token]`

Save an API token and server URL for client commands.

```bash
$ hatchway auth set-token --server https://api.example.com sk_live_abc123...
Token saved.
```

If the token argument is omitted, you are prompted to paste it.

The token is stored in `${XDG_CONFIG_HOME:-$HOME/.config}/hatchway/credentials.json` with mode `0600`.

Environment variable overrides: `HATCHWAY_TOKEN` and `HATCHWAY_SERVER` take precedence over the saved credentials.

#### `hatchway auth whoami`

Verify the saved token against the server.

```bash
$ hatchway auth whoami
Authenticated as 550e8400-e29b-41d4-a716-446655440000 on https://api.example.com
```

With JSON output:

```bash
$ hatchway auth whoami --json
{"user_id":"550e8400-e29b-41d4-a716-446655440000","server":"https://api.example.com"}
```

#### `hatchway auth logout`

Remove the saved token.

```bash
$ hatchway auth logout
Token removed.
```

### `hatchway http <port>`

Create an HTTP tunnel forwarding traffic from a public URL to a local port.

```bash
$ hatchway http 3000
Tunnel created: https://t-abc3x7km9w2p4rng.tunnel.example.com
```

**Arguments**:

| Arg    | Required | Description                     |
| ------ | -------- | ------------------------------- |
| `port` | yes      | Local port to forward (1–65535) |

**Flags**:

| Flag     | Default | Description                                   |
| -------- | ------- | --------------------------------------------- |
| `--ttl`  | `1h`    | Tunnel time-to-live (e.g. `15m`, `1h`, `24h`) |
| `--json` | `false` | Output tunnel info as JSON                    |

The local host is fixed to `127.0.0.1` in MVP — non-localhost forwarding will
be reintroduced together with TCP/UDP tunnels.

**JSON output**:

```bash
$ hatchway http 3000 --ttl 15m --json
{"tunnel_id":"t-abc3x7km9w2p4rng","public_url":"https://t-abc3x7km9w2p4rng.tunnel.example.com","status":"reserved"}
```

**Lifecycle**:

1. Pre-flight: checks that the local port is reachable.
2. Generates a fresh `Idempotency-Key` and calls `POST /v1/tunnels` to reserve a tunnel.
3. Generates an `frpc.toml` config in a temp directory (mode `0600`).
4. Spawns `frpc` as a subprocess.
5. On Ctrl-C (SIGINT/SIGTERM): kills frpc, deletes the tunnel (best-effort), exits.

If `frpc` is not found in `PATH`, the command prints the generated config for manual use.

If `frpc` exits unexpectedly, the CLI restarts it up to 3 times with a linear backoff (2s, 4s, 6s) before giving up and tearing down the tunnel.

### `hatchway list`

List active tunnels.

```bash
$ hatchway list
ID                      TYPE    STATUS  URL
t-abc3x7km9w2p4rng      http    active  https://t-abc3x7km9w2p4rng.tunnel.example.com
```

**Flags**:

| Flag     | Default | Description                    |
| -------- | ------- | ------------------------------ |
| `--json` | `false` | Output as tab-separated values |

### `hatchway delete <tunnel_id>`

Delete a tunnel by ID.

```bash
$ hatchway delete t-abc3x7km9w2p4rng
Tunnel t-abc3x7km9w2p4rng deleted.
```

### `hatchway tcp <port>`

Create a TCP tunnel. **Not yet supported** — returns an error in the current version.

### `hatchway udp <port>`

Create a UDP tunnel. **Not yet supported** — returns an error in the current version.

## Server commands

These commands run on the server (typically inside the Docker container).

### `hatchway server init`

Initialize the database and create the first admin user.

```bash
$ hatchway server init
Created admin user
API token: sk_live_abc123def456...
```

**Flags**:

| Flag            | Default                | Description                                                       |
| --------------- | ---------------------- | ----------------------------------------------------------------- |
| `--force`       | —                      | Allow init when users already exist (existing tokens stay valid). |
| `--yes`, `-y`   | `false`                | Skip the `--force` confirmation prompt (CI / scripted use).       |
| `--admin-email` | `admin@hatchway.local` | Email for the bootstrap admin user.                               |
| `--admin-name`  | `admin`                | Display name for the bootstrap admin user.                        |

The bootstrap admin user is created with `is_admin = true` so it can call
`/v1/admin/...`. To grant additional admins later, see `server user create --admin`.

**Required environment variables**:

- `DATABASE_URL` — PostgreSQL connection string

### `hatchway server run`

Start the Hatchway server (API + plugin endpoints).

```bash
$ hatchway server run
```

Starts two HTTP listeners:
- API server on `$HATCHWAY_API_ADDR` (default `:9000`)
- Plugin server on `$HATCHWAY_FRPS_PLUGIN_ADDR` (default `:9001`)

Graceful shutdown on SIGINT/SIGTERM with a 30-second drain deadline.

### `hatchway server user create`

Create a new user.

```bash
$ hatchway server user create --email alice@example.com --name Alice
User created (user): alice@example.com (550e8400-...)
```

**Flags**:

| Flag      | Required | Description                          |
| --------- | -------- | ------------------------------------ |
| `--email` | yes      | User email address                   |
| `--name`  | no       | Display name                         |
| `--admin` | no       | Grant admin privileges (`is_admin`). |

### `hatchway server user list`

List all users.

```bash
$ hatchway server user list
ID                                      EMAIL               NAME
550e8400-e29b-41d4-a716-446655440000    admin@example.com   Admin
```

**Flags**:

| Flag     | Default | Description    |
| -------- | ------- | -------------- |
| `--json` | `false` | Output as JSON |

### `hatchway server token create`

Create an API token for a user.

```bash
$ hatchway server token create --user alice@example.com --name "laptop"
API token: sk_live_xyz789...
```

**Flags**:

| Flag     | Required | Description                       |
| -------- | -------- | --------------------------------- |
| `--user` | yes      | User email or name                |
| `--name` | yes      | Token label (e.g. "laptop", "ci") |

The token is printed exactly once. Store it securely.

### `hatchway server token revoke <token_id>`

Revoke an API token by its ID.

```bash
$ hatchway server token revoke abc-123-def
Token abc-123-def revoked.
```

### `hatchway server tunnels`

List all tunnels across all users (admin view).

```bash
$ hatchway server tunnels
```

**Flags**:

| Flag     | Default | Description    |
| -------- | ------- | -------------- |
| `--json` | `false` | Output as JSON |

## Environment variables

### Client

| Variable          | Description                              |
| ----------------- | ---------------------------------------- |
| `HATCHWAY_TOKEN`  | API token (overrides saved credentials)  |
| `HATCHWAY_SERVER` | Server URL (overrides saved credentials) |

### Server

| Variable                               | Required | Default          | Description                                    |
| -------------------------------------- | -------- | ---------------- | ---------------------------------------------- |
| `DATABASE_URL`                         | yes      | —                | PostgreSQL connection string                   |
| `HATCHWAY_DOMAIN`                      | yes      | —                | Top-level domain                               |
| `HATCHWAY_PLUGIN_SECRET`               | yes      | —                | frps→server plugin auth header (internal)      |
| `HATCHWAY_FRPS_AUTH_TOKEN`             | yes      | —                | frps↔frpc bootstrap secret (returned to users) |
| `HATCHWAY_API_ADDR`                    | no       | `:9000`          | API server listen address                      |
| `HATCHWAY_FRPS_PLUGIN_ADDR`            | no       | `:9001`          | Plugin server listen address                   |
| `HATCHWAY_API_DOMAIN`                  | no       | `api.$DOMAIN`    | API subdomain                                  |
| `HATCHWAY_FRPS_DOMAIN`                 | no       | `frps.$DOMAIN`   | frps subdomain                                 |
| `HATCHWAY_TUNNEL_DOMAIN`               | no       | `tunnel.$DOMAIN` | Tunnel wildcard subdomain                      |
| `HATCHWAY_MAX_CONCURRENT_TUNNELS`      | no       | `5`              | Max concurrent tunnels per user                |
| `HATCHWAY_MAX_TTL`                     | no       | `24h`            | Maximum tunnel TTL                             |
| `HATCHWAY_RATE_CREATE_PER_MIN`         | no       | `10`             | Tunnel creation rate limit per token           |
| `HATCHWAY_API_READ_TIMEOUT`            | no       | `30s`            | API server read timeout                        |
| `HATCHWAY_API_WRITE_TIMEOUT`           | no       | `30s`            | API server write timeout                       |
| `HATCHWAY_LOG_USER_CONNS`              | no       | `false`          | Log individual user connections                |
| `HATCHWAY_FRPS_MODE`                   | no       | `external`       | frps mode: `external` or `subprocess`          |
| `HATCHWAY_FRPS_BIN_PATH`               | no       | `frps`           | Path to frps binary (subprocess mode)          |
| `HATCHWAY_EVENTS_RETENTION_DAYS`       | no       | `30`             | Tunnel events retention in days                |
| `HATCHWAY_IDEMPOTENCY_RETENTION_HOURS` | no       | `24`             | Idempotency key retention in hours             |
| `HATCHWAY_PLUGIN_TIMEOUT`              | no       | `2s`             | Per-call deadline for frps plugin handlers     |
