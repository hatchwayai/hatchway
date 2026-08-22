# Hatchway compared with other tunnel options

Self-hosters usually arrive here comparing Hatchway against three very
different things: raw [frp](https://github.com/fatedier/frp), a full access
platform like [Pangolin](https://github.com/fosrl/pangolin), or a managed
service like [ngrok](https://ngrok.com). This page makes the differences
concrete so you can pick quickly.

**The short version:** Hatchway is a control plane for *ephemeral,
programmatic* tunnels — the ngrok developer workflow, self-hosted. If you want
to permanently publish homelab services behind SSO, use Pangolin. If you want
one static reverse proxy and don't need users, quotas, or an API, plain frp is
enough. If you don't want to run anything at all, use a managed service.

## Comparison table

| | Hatchway | frp | Pangolin | ngrok / Cloudflare Tunnel |
| --- | --- | --- | --- | --- |
| Hosting | self-hosted | self-hosted | self-hosted | managed SaaS |
| Tunnel lifetime | ephemeral (TTL-bounded, minutes–hours) | static, config-defined | permanent resources | both |
| Creation interface | REST API + CLI, JSON output | edit `frps.toml` / `frpc.toml` | dashboard UI + API | CLI + dashboard |
| Per-tunnel credentials | yes (`rt_…` scoped to one tunnel) | shared bootstrap token | per-resource identity rules | per-agent credential |
| Users, quotas, rate limits | yes (multi-user, per-user caps) | no | yes (SSO, RBAC) | account plans |
| Idempotent scripted creation | yes (`Idempotency-Key`) | n/a | partial | no |
| Data plane | frp | itself | WireGuard (Newt/Gerbil) | proprietary |
| HTTP subdomain URLs | automatic per tunnel | manual config | configured per resource | automatic |
| Dashboard UI | none (CLI/API only) | none | yes | yes |
| HTTP-only scope (MVP) | yes | HTTP + TCP + UDP | HTTP + TCP + UDP | HTTP + TCP + more |
| License | MIT | Apache-2.0 | license varies by component | proprietary |

## Hatchway vs frp

frp is the data plane Hatchway runs on — 100k+ stars, battle-tested, fast.
Choose plain frp when one person controls both ends and a shared bootstrap
token is an acceptable trust model: you edit TOML, open ports, and go.

Choose Hatchway on top of frp when tunnels are created *programmatically and
repeatably*: multiple users or tokens, per-tunnel credentials you can revoke
independently, server-enforced TTLs and quotas, idempotent creation for retry
logic, and an audit trail. In other words, when the missing piece is a control
plane, not a proxy.

## Hatchway vs Pangolin

Pangolin is excellent at what it targets: permanently exposing private
resources (home lab, VPS-to-home) behind identity — WireGuard tunnels, SSO via
OIDC, RBAC, a dashboard, Traefik integration. It replaces "port forwarding +
DDNS + nginx + Authelia" as one platform.

Hatchway is aimed at the opposite shape of problem: *short-lived* tunnels
created by a CLI or a script, where identity lives in an API token and the
desired end state is the tunnel being gone. A typical Hatchway session is
minutes long — expose a port, test webhooks, revoke. There is no dashboard to
build because the API is the product. If your tunnels are long-lived
infrastructure, Pangolin is the better fit; if they are ephemeral automation,
Hatchway is.

## Hatchway vs ngrok / Cloudflare Tunnel

ngrok and Cloudflare Tunnel give you a public URL with zero infrastructure —
that convenience is real, and for casual one-off use it is hard to beat.

The reasons to run Hatchway instead are sovereignty and machine-friendliness:
URLs live on **your domain**, traffic transits **your server**, no account
signup or per-service plan limits apply, and the API contract (JSON, stable
error codes, `Idempotency-Key`, per-token rate limits) is designed for
automated clients — CI jobs, scripts, and AI agents — rather than humans in a
dashboard. The trade is operating the stack yourself: one VPS, Docker Compose,
a wildcard DNS record.

## Adjacent tools, briefly

- **sish** — SSH-based tunnels (`ssh -R`), no REST API or per-tunnel
  credentials; great when SSH is already your answer.
- **bore** — minimal, elegant CLI tunnel; dormant and no control plane.
- **rathole** — high-performance TCP/UDP NAT traversal relay; no HTTP
  subdomain model.
- **Tailscale Funnel** — exposes services from a WireGuard mesh; identity is
  your Tailscale account.
- **zrok** — sharing on an OpenZiti zero-trust overlay.

A longer, community-maintained list lives in
[awesome-tunneling](https://github.com/anderspitman/awesome-tunneling).

---

Star counts and ecosystem observations referenced here were collected in the
[2026-08 market research](research/2026-08-22-market-and-agent-positioning.md).
