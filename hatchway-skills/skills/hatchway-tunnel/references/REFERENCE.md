# Hatchway reference

Details behind [SKILL.md](../SKILL.md). Upstream documentation:
[hatchwayai/hatchway](https://github.com/hatchwayai/hatchway) — `docs/cli.md` (commands),
`docs/api.md` (REST contract), `docs/self-host.md` (running a server).

## Install and prerequisites

- **Client release archive**: GitHub releases ship `hatchway` with a matching
  `frpc` side by side. The CLI finds `frpc` next to its own binary first, then
  on `PATH`.
- **From source**: `go install github.com/hatchwayai/hatchway/cmd/hatchway@latest`
  (or clone + `make build`); provide `frpc` separately — any recent frp release
  works, the server pins and tests against frp 0.69.
- **A deployed Hatchway server** is required. If the user has none and wants
  one, point them at `docs/self-host.md` in the hatchway repo (Docker Compose
  stack: Postgres, hatchway-server, frps, Caddy; one wildcard DNS record).

## Credential resolution

Order of precedence:

1. `HATCHWAY_TOKEN` and `HATCHWAY_SERVER` environment variables.
2. `${XDG_CONFIG_HOME:-$HOME/.config}/hatchway/credentials.json` — written by
   `auth set-token` with mode `0600` in a `0700` directory; the CLI refuses
   looser permissions and symlinks.

Validate at any time:

```bash
hatchway auth whoami --json
# {"server":"https://api.example.com","user_id":"550e8400-..."}
```

The `sk_live_` prefix is a naming convention only (unrelated to Stripe).

## Client command surface

| Command | JSON | Behavior |
| --- | --- | --- |
| `hatchway http <port> [--ttl D] [--json]` | yes | Create + run + supervise + auto-revoke. Prints the URL (or the JSON object below) after reservation |
| `hatchway list [--json]` | yes | All owned tunnels including `expired`/`revoked`; CLI follows pagination, `--json` prints a flat array |
| `hatchway delete <tunnel_id>` | no | Revoke; idempotent for already-terminal owned tunnels |
| `hatchway auth set-token [token] --server URL` | no | Store credentials; prompts without echo when the token argument is omitted |
| `hatchway auth whoami [--json]` | yes | Validate credentials |
| `hatchway auth logout` | no | Delete the credentials file (env vars unaffected) |

`hatchway http --json` initial object (streamed once, then the process keeps
running):

```json
{
  "public_url": "https://t-abc3x7km9w2p4rng.tunnel.example.com",
  "status": "reserved",
  "tunnel_id": "t-abc3x7km9w2p4rng"
}
```

It does not include expiry; the tunnel ID encodes into the URL, so `delete`
works directly with the `t-…` value.

## Retry, idempotency, supervision

- The CLI retries up to three times with exponential backoff on transport
  failures and HTTP `502/503/504` for `GET`, `HEAD`, `DELETE`, or any request
  carrying an `Idempotency-Key`.
- Every `hatchway http` run mints a fresh UUID idempotency key, so retrying the
  same command after a partial failure never double-creates a tunnel. A keyed
  create that receives a truncated `201` body replays the same key once to
  recover the committed response.
- frpc is supervised: unexpected exits restart up to three times (2s, 4s, 6s
  delays); after that the error surfaces and the tunnel is revoked.
- Every exit path after creation — signal, clean frpc exit, config failure,
  exhausted restarts — triggers a bounded (three-second) revocation request.

## Quota and TTL defaults

| Setting | Default | Effect |
| --- | --- | --- |
| Live tunnels per user | 5 (`reserved`/`active`/`closed` count; terminal states do not) | Excess creates → `QUOTA_EXCEEDED` |
| Creates per API token | 10/minute | Excess → `RATE_LIMITED` |
| TTL default | lower of 1h and server max | `--ttl` omitted |
| TTL maximum | 24h | Longer `--ttl` → `INVALID_REQUEST` |

Operators can change these (`HATCHWAY_MAX_CONCURRENT_TUNNELS`,
`HATCHWAY_MAX_TTL`, `HATCHWAY_RATE_CREATE_PER_MIN`).

## Raw REST path

When managing tunnels without the CLI (e.g. from a test harness that already
runs its own frpc, or a language without the binary):

```bash
curl -sS -X POST https://api.example.com/v1/tunnels \
  -H "Authorization: Bearer $HATCHWAY_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{"type":"http","local_host":"127.0.0.1","local_port":3000,"ttl_seconds":900}'
```

`201 Created` response:

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
    "server_token": "<bootstrap secret>",
    "proxy_name": "t-abc3x7km9w2p4rng",
    "proxy_type": "http",
    "subdomain": "t-abc3x7km9w2p4rng",
    "local_ip": "127.0.0.1",
    "local_port": 3000
  }
}
```

Notes for programmatic consumers:

- Send an `Idempotency-Key` on every create; retries then replay the original
  response instead of creating a second tunnel.
- `runtime_token` and the `frp` block appear only on creation. Treat both as
  secrets — the runtime token authorizes exactly this tunnel's data plane.
- To carry traffic, run frpc with the returned values (client-level
  `metadatas.runtime_token`, HTTP proxy named/subdomained exactly the tunnel
  ID). The `hatchway http` CLI does all of this; only go raw when you must.
- Other routes: `GET /v1/tunnels` (keyset pagination), `GET /v1/tunnels/{id}`
  (404 for other users' tunnels — existence is not leaked), `DELETE
  /v1/tunnels/{id}`, `GET /v1/me`.

## Webhook provider notes

- **Stripe**: register the endpoint in the dashboard (or via the Stripe CLI:
  `stripe webhook-endpoints create`). Verify signatures with the official SDK
  against your local webhook secret; the tunnel is transparent transport.
- **GitHub**: set the webhook URL in repo/organization settings; prefer
  `application/json` content type. Payloads arrive unmodified.
- **Slack / Shopify / generic**: any provider that accepts an HTTPS URL works.
  The URL's certificate is a valid wildcard cert for the server's tunnel
  domain — no self-signed warnings.
- Webhook receivers commonly need the tunnel to stay up for the whole test;
  pick `--ttl` comfortably above the session length and stop the tunnel when
  done rather than letting it lapse mid-test.

## Security notes for agents

- Never echo, log, or persist the `sk_live_` API token after storing it.
- The `rt_` runtime token and `frp.server_token` grant data-plane access for
  one tunnel only; still, avoid writing them into world-readable files or
  commit messages.
- URLs are unguessable (≈79 random bits in the label) but public by design —
  anyone with the URL can reach the tunneled service while it is live. Do not
  tunnel admin panels or services without their own auth unless the tunnel is
  short-lived and watched.
