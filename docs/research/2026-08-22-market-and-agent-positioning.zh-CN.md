# Hatchway 市场与定位调研报告

**日期：** 2026-08-22 · **数据：** 当日通过 GitHub API 与 Tavily 网络搜索实时采集
（Star 数与最近推送时间为当日快照）。

**核心问题：** Hatchway 是否有用？与同类项目相比处于什么位置？AI Agent 趋势
对其定位与下一步方向意味着什么？

---

## 1. 竞争格局

### 可自建（self-hostable）的开源项目

| 项目 | Star 数 | 最近推送 | 模式 |
|---|---|---|---|
| frp（Hatchway 的数据面） | ★108,938 | 2026-08-17 | 原始反向代理；手动 TOML 配置；无 API、无用户体系、无配额 |
| **Pangolin** | ★22,424 | 2026-08-21 | 完整平台：WireGuard 隧道（Gerbil/Newt）、SSO/OIDC、RBAC、仪表盘 UI、Traefik 集成 |
| rathole | ★14,050 | 2026-08-22 | 高性能 TCP/UDP NAT 穿透；无 HTTP 子域名模型、无控制面 |
| bore | ★11,414 | 2026-02-04 | 极简 CLI 隧道；实质上已停止维护；无控制面 |
| sish | ★4,700 | 2026-06-25 | 基于纯 SSH 的 HTTP(S)/WS/TCP 隧道；无 REST API |
| zrok | ★4,630 | 2026-06-22 | 基于 OpenZiti 的安全共享（零信任 Overlay） |
| tunnelmole | 约 1,900 | 2026-04-13 | 可自建的 ngrok 类似物；社区较小 |

（portr —— 此前该赛道的参与者之一 —— 在 GitHub 上已无法访问。）

### 托管 / SaaS 默认选项

- **ngrok** —— 开发者默认选择；目前正向上层重新定位，聚焦 AI 网关与 Agent
  流量管理（见 [ngrok 关于 AI 网关的文章（2026）](https://ngrok.com/blog/ai-gateways-2026)）。
- **Cloudflare Tunnel** —— 免费、零基础设施；休闲场景的默认选择。闭源。
- **Tailscale Funnel** —— WireGuard Mesh 网络的便捷暴露方式；与身份绑定。
- 长尾：localhost.run、Playit.gg、Pinggy、LocalXpose、inlets、zrok 托管版。

自托管受众的权威发现入口是
[awesome-tunneling](https://github.com/anderspitman/awesome-tunneling) 列表。
Hatchway 目前未被收录。

## 2. Hatchway 占据的空档

自建隧道空间的两个极端都已被占据：

- **frp** 是 10.9 万 Star 的原始原语。没有用户、令牌、配额、审计或 API ——
  你需要手动编辑 TOML 并共享一个引导令牌。
- **Pangolin** 是面向**永久性**资源暴露（家庭实验室 / VPS 到家）的成熟平台，
  带身份体系：仪表盘、SSO、RBAC，五个以上的容器组件。

两者之间缺失的正是 Hatchway 所构建的东西：**临时性、按命令创建、由 API 驱动
的隧道 —— 即 ngrok 的开发者工作流，但可自建。** 提供 REST API、按隧道隔离的
运行时令牌、配额、幂等键、限流，以及事件审计轨迹。精神上最接近的项目
（sish、bore、tunnelmole）都缺少控制面；它们中没有一个是面向机器优先设计的。

## 3. 必须正视的不利因素

- **Pangolin 的势头**（2.2 万 Star、每日推送、Show HN 与 YouTube 曝光强劲）
  占据了"自建隧道"的话题度。它的取向（永久资源、身份体系）与 Hatchway 不同，
  但会吸收大部分注意力。
- **"够用就好"的在位者：** 对于只暴露一个服务的单人用户来说，朴素的 frp 或
  免费的 Cloudflare Tunnel 确实足够。Hatchway 的控制面只在隧道被**以程序化
  方式、高频地、由多个身份**创建和消费时才体现出价值。
- **现实天花板：** 这是一个楔子型市场，不是大众市场。成功的形态是"某个正在
  成长的小众群体指定使用的工具"（数百到低数千 Star），除非下文的 Agent 楔子
  将其拓宽。
- MVP 约束（单副本服务端、仅支持 HTTP）对楔子市场而言没有问题，无需早期修改。

## 4. AI Agent 趋势 —— 最强的一张牌，且如今有据可依

自 Hatchway 的计划写成以来，出现了一个当时不存在的分发渠道：**Agent 现在
通过 Skills 和 MCP 获取工具，而 Agent 持续需要公网 URL。**

证据（均为 2026-08-22 当日有效）：

- [hookdeck/webhook-skills](https://github.com/hookdeck/webhook-skills) ——
  面向 Claude Code、Cursor 和 Copilot 的 Webhook 接收/验签 Skills，基于
  **Agent Skills 规范**构建，因为 Agent 需要公网 URL 来在本地测试 Stripe /
  Shopify / GitHub Webhook。
- [terminalskills.io 上的 ngrok Agent Skill](https://terminalskills.io/skills/ngrok)
  —— ngrok 已经在通过这个渠道分发。
- [WebhookRelay 的 Agent Skills](https://webhookrelay.com/docs/skills)（"给我一个
  Webhook URL 来测试我的 Stripe 集成"）以及提供一次性 Webhook URL 的 MCP 服务器。

现有的每一种方案都要经过某家的 SaaS：注册账号、限流、第三方域名、数据出站。
**"通过我自己的 VPS 建隧道、无需账号、用我自己的域名、输出 JSON"** 是一个
真正差异化的 Skill。

Hatchway 的现有设计已经异常契合这个用例 —— 可以说比它当初构想时所面向的
人类开发者工作流更契合：

| Agent 需求 | Hatchway 已交付的对应特性 |
|---|---|
| 机器可读输出 | 所有命令 JSON 优先的 CLI |
| Agent 会频繁重试 | 隧道创建支持 Idempotency-Key |
| 凭据泄露不能级联扩散 | 按隧道隔离的运行时令牌、短 TTL |
| Agent 可能失控 / 循环 | 限流 + 并发隧道配额 |
| 可审计 | `tunnel_events` 事件轨迹、`/metrics` |

## 5. 建议（按优先级排序）

1. **下一步交付 `hatchway-skills`**（从 post-MVP 积压列表提前）。做一个符合
   Agent Skills 规范的 Skill："暴露 3000 端口并给我公网 URL"、Webhook 测试
   配方、以及 JSON 输出契约。提交到 terminalskills.io，并向
   awesome-tunneling 提 PR。
2. **围绕 Agent / 脚本工作流重新定位 README：** 开篇即"一条命令 → 自己域名上
   的 URL、输出 JSON"。候选一句话定位：*"面向 AI Agent 与脚本的自建 ngrok。"*
3. **增加 "vs Pangolin / vs ngrok / vs frp" 对比文档** —— 自托管用户在采纳前
   搜索的正是这个；它能让"临时 vs 永久"的区分变得清晰可辨。
4. **把安全模型写成营销内容**（双令牌设计、argon2id 静态哈希、按隧道隔离、
   配额）。自托管受众首先阅读的就是这一节。
5. **暂保持仅支持 HTTP** —— 对 Agent 楔子而言，分发比 TCP/UDP 更重要；
   积压列表的排序已经是正确的。
6. **更远期 / 探索性：** 封装隧道 API 的 MCP 服务器；类似 TryCloudflare 的
   匿名快速隧道作为获客漏斗（托管方承担责任 —— 需谨慎推进）。

## 6. 结论

作为一个通用型自建隧道，Hatchway 是一个拥挤赛道中构建良好的新进入者。但作为
**面向 AI Agent 的自建隧道**，它提前布局了市场刚刚打开的空档，而且已做出的
技术决策（JSON 优先、幂等性、隔离令牌、配额）恰好是正确的。当前的瓶颈在
分发，而不在代码。

## 来源

- GitHub API（Star 数、推送日期）：fosrl/pangolin、fatedier/frp、
  rathole-org/rathole、ekzhang/bore、antoniomika/sish、openziti/zrok、
  robbie-cahill/tunnelmole-{client,service} —— 2026-08-22 获取
- [awesome-tunneling](https://github.com/anderspitman/awesome-tunneling)
- [Show HN: Pangolin](https://news.ycombinator.com/item?id=44526015)
- [ngrok: What Is an AI Gateway (2026)](https://ngrok.com/blog/ai-gateways-2026)
- [hookdeck/webhook-skills](https://github.com/hookdeck/webhook-skills) ·
  [Hookdeck: Introducing Webhook Skills](https://hookdeck.com/blog/webhook-skills)
- [terminalskills.io: Ngrok — AI Agent Skill](https://terminalskills.io/skills/ngrok)
- [WebhookRelay: Agent Skills](https://webhookrelay.com/docs/skills)
- [Pinggy: Top 10 ngrok alternatives in 2026](https://pinggy.io/blog/best_ngrok_alternatives)
- [CrowdSec: Web Defense with Pangolin](https://www.crowdsec.net/blog/web-defense-with-pangolin-and-crowdsec)
