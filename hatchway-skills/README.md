# hatchway-skills

[Agent Skills](https://agentskills.io) for
[Hatchway](https://github.com/hatchwayai/hatchway), the self-hosted tunnel server —
so your AI coding agent can expose a local port and hand you a public HTTPS URL
on **your own domain**, with no SaaS account in the loop.

## Skills

| Skill | What it does |
| --- | --- |
| [`hatchway-tunnel`](skills/hatchway-tunnel/) | Expose a local HTTP port through a Hatchway server; webhook testing, dev-server sharing, scripted/headless use with the JSON contract |

## Prerequisites

- A deployed Hatchway server (see the
  [self-host guide](https://github.com/hatchwayai/hatchway/blob/main/docs/self-host.md)).
- The `hatchway` CLI on the machine where the agent runs, with `frpc` next to
  it or on `PATH`. Release archives bundle both.
- Credentials: `hatchway auth set-token --server <url> <sk_live_…>`, or the
  `HATCHWAY_SERVER` / `HATCHWAY_TOKEN` environment variables.

## Install a skill

Skills follow the open [Agent Skills](https://agentskills.io) format and work
with any compatible agent (Claude Code, Cursor, Codex, Gemini CLI, Copilot,
and many others).

**Claude Code** — personal, all projects:

```bash
git clone https://github.com/hatchwayai/hatchway-skills.git
mkdir -p ~/.claude/skills
cp -r hatchway-skills/skills/hatchway-tunnel ~/.claude/skills/
```

Project-scoped: copy into `<project>/.claude/skills/` instead and commit, so
everyone's agent knows how to use your team's tunnel server.

**Other agents** — follow your agent's skills documentation and point it at
the `skills/` directory here.

## Why skills for a tunnel server?

Agents increasingly need public URLs — webhook callbacks (Stripe, GitHub),
OAuth redirects, sharing a preview — and until now every skill-shaped answer
to that need routed through a SaaS tunnel. If you run Hatchway, your agent can
get the same one-command URL on infrastructure you control, with per-tunnel
scoped credentials, TTLs, and quotas designed for exactly this kind of
automated, retry-happy client.

## License

[MIT](LICENSE)
