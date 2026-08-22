# Hatchway TODO

Actionable tracker for remaining growth and release work. Growth priorities
come from the 2026-08-22 market research:
[docs/research/2026-08-22-market-and-agent-positioning.md](docs/research/2026-08-22-market-and-agent-positioning.md)
(English) and its `.zh-CN.md` translation.

## Growth priorities

- [ ] **Publish `hatchway-skills`** (the Agent Skill package is drafted; its
  repo targets `github.com/zydo/hatchway-skills`)
  - [ ] Create the remote, make the first commit, and push (skill, reference
    doc, and staged eval prompts are ready)
  - [ ] Submit the skill to terminalskills.io (needs the public repo live)
  - [ ] PR into [awesome-tunneling](https://github.com/anderspitman/awesome-tunneling)
  - [ ] Run the skill eval loop (three prompts staged in the skills repo's
    `evals/evals.json`); realistic runs need a reachable Hatchway server and
    credentials
- [x] **Reposition README** around the agent/script workflow (2026-08-22):
  ngrok-for-agents pitch, skills-repo callout, automation bullet, refreshed
  release-artifact wording, docs-table rows for the new pages
- [x] **docs/comparisons.md** (2026-08-22): vs frp, Pangolin, and managed
  tunnels, plus adjacent tools
- [x] **docs/security.md** (2026-08-22): credentials, authorization layers,
  abuse controls, and non-goals
- [ ] Later / exploratory: MCP server wrapping the tunnel API; anonymous
  quick-tunnel adoption funnel (hosted liability — approach carefully)

## Release chores (low priority, paused)

- [ ] Attach release artifacts to the GitHub v0.1.0 release — no release
  workflow exists; run locally with a token:
  `GITHUB_TOKEN=<token> go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --clean`
  (alternative: add a tag-triggered release workflow to CI)
- [ ] Tick the last checkbox in `PLAN.md` — only after artifacts are attached
