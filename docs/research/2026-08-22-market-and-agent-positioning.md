# Hatchway Market & Positioning Research

**Date:** 2026-08-22 · **Data:** collected live via GitHub API and Tavily web search on
the same day (star counts and push dates are point-in-time).

**Question:** Is Hatchway useful, where does it fit against similar projects, and what
does the AI-agent trend imply for its positioning and next steps?

---

## 1. Competitive landscape

### Self-hostable open source

| Project | Stars | Last push | Model |
|---|---|---|---|
| frp (Hatchway's data plane) | ★108,938 | 2026-08-17 | Raw reverse proxy; manual TOML config; no API, users, or quotas |
| **Pangolin** | ★22,424 | 2026-08-21 | Full platform: WireGuard tunnels (Gerbil/Newt), SSO/OIDC, RBAC, dashboard UI, Traefik integration |
| rathole | ★14,050 | 2026-08-22 | High-performance TCP/UDP NAT traversal; no HTTP-subdomain model, no control plane |
| bore | ★11,414 | 2026-02-04 | Minimal CLI tunnel; effectively dormant; no control plane |
| sish | ★4,700 | 2026-06-25 | HTTP(S)/WS/TCP tunnels over plain SSH; no REST API |
| zrok | ★4,630 | 2026-06-22 | Secure sharing on OpenZiti (zero-trust overlay) |
| tunnelmole | ~1,900 | 2026-04-13 | Self-hostable ngrok-like; small community |

(portr — previously a contender in this space — no longer resolves on GitHub.)

### Managed / SaaS defaults

- **ngrok** — the developer default; now repositioning up-stack toward AI gateways
  and agent traffic management (see [ngrok on AI gateways, 2026](https://ngrok.com/blog/ai-gateways-2026)).
- **Cloudflare Tunnel** — free, zero-infra; the casual default. Closed source.
- **Tailscale Funnel** — WireGuard mesh convenience; identity-bound.
- Long-tail: localhost.run, Playit.gg, Pinggy, LocalXpose, inlets, zrok's hosted tier.

The canonical discovery point for the self-hosting audience is the
[awesome-tunneling](https://github.com/anderspitman/awesome-tunneling) list.
Hatchway is not currently listed there.

## 2. The gap Hatchway occupies

The two poles of the self-hosted space are taken:

- **frp** is the raw 109k-star primitive. No users, tokens, quotas, audit, or API —
  you edit TOML and share a bootstrap token.
- **Pangolin** is the polished platform for *permanent* resource exposure
  (homelab / VPS-to-home) with identity: dashboard, SSO, RBAC, five-plus containers.

What is missing between them is precisely what Hatchway built: **ephemeral,
per-command, API-driven tunnels — the ngrok developer workflow, self-hosted.**
REST API with per-tunnel scoped runtime tokens, quotas, idempotency keys, rate
limits, and an event audit trail. The closest in spirit (sish, bore, tunnelmole)
all lack the control plane; none of them is machine-first.

## 3. Honest headwinds

- **Pangolin's momentum** (22k stars, daily pushes, strong Show-HN/YouTube presence)
  owns "self-hosted tunnel" mindshare. Its orientation (permanent resources,
  identity) differs from Hatchway's, but it absorbs the attention.
- **"Good enough" incumbents:** for a solo user exposing one service, plain frp or
  free Cloudflare Tunnel is genuinely sufficient. Hatchway's control plane pays off
  only when tunnels are created and consumed *programmatically, at frequency, by
  multiple identities*.
- **Realistic ceiling:** this is a wedge, not a mass market. Success looks like
  "the tool a specific growing niche reaches for" (hundreds to low thousands of
  stars), unless the agent wedge (below) widens it.
- MVP constraints (single-replica server, HTTP-only) are fine for the wedge and do
  not need early revision.

## 4. The AI-agent trend — the strongest card, now provable

Since Hatchway's plan was written, a distribution channel emerged that did not
exist before: **agents acquire tools through skills and MCP now, and agents
constantly need public URLs.**

Evidence (all live as of 2026-08-22):

- [hookdeck/webhook-skills](https://github.com/hookdeck/webhook-skills) — webhook
  receiver/verification skills for Claude Code, Cursor, and Copilot, built on the
  **Agent Skills specification**, because agents need a public URL to test Stripe /
  Shopify / GitHub webhooks locally.
- An [ngrok agent skill on terminalskills.io](https://terminalskills.io/skills/ngrok)
  — ngrok is already distributing through this channel.
- [WebhookRelay agent skills](https://webhookrelay.com/docs/skills) ("Give me a
  webhook URL to test my Stripe integration") and MCP servers for disposable
  webhook URLs.

Every existing offering routes through someone's SaaS: account signup, rate
limits, third-party domains, data egress. **"Tunnel through my own VPS, no
account, my own domain, JSON out"** is a genuinely differentiated skill.

Hatchway's existing design already fits this use case unusually well — arguably
better than it fits the human-dev workflow it was sketched around:

| Agent need | Hatchway feature already shipped |
|---|---|
| Machine-readable output | JSON-first CLI on every command |
| Agents retry aggressively | Idempotency-Key on tunnel creation |
| Compromised credentials must not cascade | Per-tunnel scoped runtime tokens, short TTLs |
| Agents misbehave / loop | Rate limits + concurrent-tunnel quotas |
| Auditability | `tunnel_events` trail, `/metrics` |

## 5. Recommendations (prioritized)

1. **Ship `hatchway-skills` next** (promote from post-MVP backlog). An
   Agent-Skills-spec skill: "expose port 3000 and give me the public URL," a
   webhook-testing recipe, and the JSON output contract. Submit to
   terminalskills.io and PR into awesome-tunneling.
2. **Reposition the README** around the agent/script workflow: lead with
   "one command → URL on your own domain, JSON out." Candidate one-liner:
   *"self-hosted ngrok for AI agents and scripts."*
3. **Add a "vs Pangolin / vs ngrok / vs frp" doc** — self-hosters search exactly
   this before adopting; it makes the ephemeral-vs-permanent distinction legible.
4. **Write up the security model as marketing** (two-token design, argon2id at
   rest, per-tunnel scoping, quotas). The self-hoster audience reads this first.
5. **Keep HTTP-only for now** — TCP/UDP matters less for the agent wedge than
   distribution does; the backlog ordering is already right.
6. **Later / exploratory:** an MCP server wrapping the tunnel API; a
   TryCloudflare-style anonymous quick tunnel as an adoption funnel (hosted
   liability — approach carefully).

## 6. Bottom line

As a general-purpose self-hosted tunnel, Hatchway is a well-built entrant in a
crowded field. As **the self-hosted tunnel for AI agents**, it is early to a gap
the market just opened, and the technical decisions already made (JSON-first,
idempotency, scoped tokens, quotas) are the right ones for it. The binding
constraint is distribution, not code.

## Sources

- GitHub API (star counts, push dates): fosrl/pangolin, fatedier/frp,
  rathole-org/rathole, ekzhang/bore, antoniomika/sish, openziti/zrok,
  robbie-cahill/tunnelmole-{client,service} — retrieved 2026-08-22
- [awesome-tunneling](https://github.com/anderspitman/awesome-tunneling)
- [Show HN: Pangolin](https://news.ycombinator.com/item?id=44526015)
- [ngrok: What Is an AI Gateway (2026)](https://ngrok.com/blog/ai-gateways-2026)
- [hookdeck/webhook-skills](https://github.com/hookdeck/webhook-skills) ·
  [Hookdeck: Introducing Webhook Skills](https://hookdeck.com/blog/webhook-skills)
- [terminalskills.io: Ngrok — AI Agent Skill](https://terminalskills.io/skills/ngrok)
- [WebhookRelay: Agent Skills](https://webhookrelay.com/docs/skills)
- [Pinggy: Top 10 ngrok alternatives in 2026](https://pinggy.io/blog/best_ngrok_alternatives)
- [CrowdSec: Web Defense with Pangolin](https://www.crowdsec.net/blog/web-defense-with-pangolin-and-crowdsec)
