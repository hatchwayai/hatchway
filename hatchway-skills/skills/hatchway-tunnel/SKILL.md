---
name: hatchway-tunnel
description: Expose a local HTTP port through a self-hosted Hatchway server and return a public HTTPS URL on the operator's own domain. Use this whenever a task needs a public URL reaching a local service — testing webhooks (Stripe, GitHub, Shopify, Slack), OAuth callbacks, sharing a dev server for preview or demo, smoke-testing an app from outside localhost — and especially when traffic should stay on infrastructure the user controls instead of a SaaS tunnel like ngrok, Cloudflare Tunnel, or localhost.run. Also use when the user mentions Hatchway, or asks to expose, share, publish, or tunnel a local port or localhost URL.
license: MIT
compatibility: Requires the hatchway CLI with a bundled or PATH-visible frpc, network access to a self-hosted Hatchway server, and credentials via HATCHWAY_TOKEN/HATCHWAY_SERVER env vars or hatchway auth set-token.
metadata:
  author: zydo
  source: https://github.com/hatchwayai/hatchway
  version: "0.1.0"
---

# Hatchway tunnels

Give a local HTTP service a public HTTPS URL on infrastructure the user controls.
Hatchway is a self-hosted tunnel server: one command reserves a unique URL like
`https://t-<id>.tunnel.example.com`, forwards public requests to
`127.0.0.1:<port>`, and revokes itself when the work ends. Nothing routes
through a third-party SaaS, no account signup is involved, and every URL is
TTL-bounded — an exposed port cannot be forgotten open.

## Before you start

Verify the CLI exists and credentials resolve. This fails fast and avoids
diagnosing tunnel errors that are really setup errors:

```bash
hatchway version && hatchway auth whoami
```

- `command not found` → the client is not installed; see the install section
  of [references/REFERENCE.md](references/REFERENCE.md).
- `UNAUTHENTICATED` or a no-credentials error → ask the user for their server
  URL and `sk_live_…` API token, then store them once:

  ```bash
  hatchway auth set-token --server https://api.example.com sk_live_...
  ```

  Environment variables `HATCHWAY_SERVER` and `HATCHWAY_TOKEN` work for
  session-scoped use. Never print or log the token after storing it.

## Expose a port (interactive)

```bash
hatchway http 3000 --ttl 15m
```

Prints the public URL, then runs in the foreground. Ctrl-C stops it and
revokes the tunnel.

## Expose a port (scripted / headless)

`hatchway http` runs in the foreground by design, so background it and read
the single JSON object it prints once the tunnel is reserved:

```bash
hatchway http 3000 --ttl 15m --json \
  > /tmp/hatchway-tunnel.json 2> /tmp/hatchway-tunnel.log &
TUNNEL_PID=$!
```

The JSON appears within a second or two:

```json
{"public_url":"https://t-abc3x7km9w2p4rng.tunnel.example.com","status":"reserved","tunnel_id":"t-abc3x7km9w2p4rng"}
```

Poll until the file parses, then use `public_url`. The local service must
already be listening on `127.0.0.1:<port>` — the CLI preflights it and refuses
to start otherwise (plain-text error on stderr), which prevents reserving
tunnels for dead services.

Stop with SIGINT/SIGTERM so built-in cleanup revokes the tunnel:

```bash
kill -INT "$TUNNEL_PID" && wait "$TUNNEL_PID"
```

SIGKILL skips revocation; the tunnel then lingers until its TTL expires. That
still works — but see cleanup discipline below.

## Webhook testing

The standard recipe:

1. Start the local webhook receiver (e.g. `localhost:3000/webhooks/stripe`).
2. Expose it with `hatchway http 3000 --json`. TLS is already valid — the
   server's Caddy terminates HTTPS with a wildcard certificate, so providers
   accept the URL as-is.
3. Register `https://t-<id>.tunnel.example.com/webhooks/stripe` as the webhook
   endpoint with the provider.
4. Trigger a test event from the provider's dashboard; confirm delivery
   reached the local receiver and verify signatures locally with the
   provider's SDK as usual.
5. Stop the tunnel.

Provider-specific notes live in [references/REFERENCE.md](references/REFERENCE.md).

## Cleanup discipline

Each user can hold only a small number of live tunnels (default 5) and tunnel
creation is rate-limited (default 10/minute per API token). Leaked tunnels
occupy quota until their TTL expires — an hour at the default TTL, up to 24h at
the maximum — which can block later work with `QUOTA_EXCEEDED`. Prefer short
TTLs (`--ttl 15m`), stop tunnels promptly, and revoke strays:

```bash
hatchway list            # includes expired/revoked leftovers
hatchway delete t-abc3x7km9w2p4rng
```

## Errors

API failures use the envelope `{"error":{"code":"…","message":"…"}}`;
CLI-local problems are plain text on stderr.

| Situation | Meaning and action |
| --- | --- |
| Port unreachable (plain text) | Start the local service first; the CLI preflights `127.0.0.1:<port>` |
| `UNAUTHENTICATED` | Credentials missing, revoked, or mistyped → re-run `auth set-token` |
| `RATE_LIMITED` | Too many creates (default 10/min) → wait briefly and retry |
| `QUOTA_EXCEEDED` | Too many live tunnels → `hatchway list`, `delete` stale ones |
| `INVALID_REQUEST` | Bad port or TTL → `--ttl` must be a positive duration within the server max (24h default) |
| frpc restart exhausted | frpc died repeatedly → check the stderr log; the tunnel auto-revokes |

## Going deeper

[references/REFERENCE.md](references/REFERENCE.md) holds the full JSON
contract, credential resolution order, the raw REST path (calling
`POST /v1/tunnels` directly with an `Idempotency-Key`), quota and TTL defaults,
and per-provider webhook notes. Read it when a task needs more than the
commands above.
