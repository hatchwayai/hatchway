# Hatchway 续作任务 Prompt（交给下一个 AI Agent）

> **只读这一份 `FOR_CODEX.md`，不要打开仓库里的 `FOR_CLAUDE.md`。**
> `FOR_CLAUDE.md` 是交给另一个 agent 的**平行任务书**，除了域名和 Cloudflare zone
> 之外内容几乎完全相同。那份用 `dongziyu.com`，这份用 `avpn.vip`；两个 agent 必须在
> **各自独立的域名 / zone / 分支**上部署，互不干扰。读了另一份只会让你混淆该操作哪个
> zone、该新建哪些 DNS 记录、该写哪个部署文档。**本任务书里所有域名一律是 `avpn.vip`。**

你将在 `/Users/dongziyu/code/hatchway` 这一个仓库上继续开发，并把成果
**真实部署上线、端到端验证通过**。配套的 Agent Skill 包已经并入本仓库的
`hatchway-skills/` 子目录，**在原地修改、和主仓库代码一起提交**，不要再把它当成独立仓库。
以下是背景、目标、约束和交付要求。请完整读完再动手。

---

## 0. 开工前必须做的两件事

1. **先建工作分支，绝不在 `main` 上工作。**
   `git switch -c <descriptive-branch-name>`（例如 `feat/one-command-expose`）。
   只有这一个仓库、一条提交流：Go 代码、文档和 `hatchway-skills/` 下的 skill 改动
   都提交到同一个分支上。

2. **先整体调研，再一次性预申请全部可能用到的权限。**
   在写第一行代码之前，先把整个任务从头到尾想清楚，列出你将要用到的全部工具与命令类别
   （gcloud / ssh / docker / curl 到 Cloudflare API / 浏览器自动化 / go build / git 等），
   **一次性向用户申请所有权限**。之后的执行必须在**零人工干预**下跑完全程——不要中途停下来
   问问题、不要等待确认。遇到需要人类拍板的事项（见第 5 节），写进待决策清单继续往下做，
   不要阻塞。

---

## 1. 项目现状（背景）

### 1.1 Hatchway 是什么

CLI-first、自托管的公网 HTTP 隧道服务，数据面用 [frp](https://github.com/fatedier/frp)。
定位是"给脚本和 AI Agent 用的自托管 ngrok"：一条命令拿到自己域名下的公网 HTTPS URL，
用完自动吊销或 TTL 过期。

核心设计原则（DESIGN.md）：**Hatchway 拥有控制面，frp 拥有数据面。**

```console
$ hatchway http 3000
https://t-abc3x7km9w2p4rng.tunnel.example.com
```

### 1.2 代码状态

- Go 1.26.5 模块 `github.com/zydo/hatchway`，Cobra + chi + pgx/v5 + golang-migrate + slog。
- 单一二进制 `hatchway`，含客户端命令（`http`/`list`/`delete`/`auth`）与
  `server` 子命令树（`run`/`init`/`user`/`token`/`healthcheck`）。
- 目录：`cmd/hatchway`、`internal/{config,db,models,tokens,cli,frp,testutil}`、
  `internal/server/{api,tunnels,plugin}`。
- **PLAN.md 的 Phase 0–11 已全部实现**（HTTP-only 范围），仅剩：发布 tag、附加
  release artifacts、干净主机安装验证。数据库迁移 `0001`–`0006`。
- 测试完备（单测 + PostgreSQL 集成测试 + fuzz + CI），`make build/test/lint/coverage`。
- 文档：`DESIGN.md`（行为契约的唯一权威）、`docs/{api,cli,self-host,deployment-notes,security,comparisons,extending,source-reading-guide}.md`、
  `PLAN.md`（历史路线图）、`TODO.md`（剩余增长与发布事项）。
- 最近 commit：`d63c374 Add positioning docs, agent-focused README, and TODO tracker`，
  工作区干净，分支 `main`。

### 1.3 运行时架构（当前 bundled 部署）

`docker-compose.yml` 起四个服务：

| 服务 | 说明 |
| --- | --- |
| `postgres` | PostgreSQL 16，仅在 backend 网络，只有 hatchway-server 能连 |
| `hatchway-server` | 公开 API `:9000`；**内部**监听 `:9001`（frps 回调 + Caddy 请求鉴权 + Prometheus metrics，绝不可暴露） |
| `frps` | frp 服务端，宿主暴露 `7000`（frpc 控制连接），vhost `8081` 仅内网 |
| `caddy` | 宿主暴露 `80/443`，自定义构建带 Cloudflare DNS 插件，DNS-01 签 `*.tunnel.<domain>` 通配符证书 |

关键机制：

- 两级凭证：API token `sk_live_…`（长期，调控制 API）与 runtime token `rt_…`（每隧道一枚，
  随隧道过期）。仅存前缀 + 版本化 SHA-256 摘要，常数时间比较；兼容历史 Argon2id 行。
- 隧道生命周期：`reserved → active → closed → active`，终态 `expired` / `revoked`。
  30s 一次的 reaper 处理过期；每小时清理 events / idempotency / 死 runtime token。
- **每个公网 HTTP 请求**都经 Caddy `forward_auth` 打到 `:9001` 的
  `/internal/tunnels/authorize/{PLUGIN_SECRET}`，用数据库时钟校验隧道状态与 TTL
  （因为 frp 对 HTTP 代理不发 `NewUserConn`）。失败一律 fail closed。
- frps 插件回调 `/frp/plugin/{secret}` 在 `Login` / `NewProxy` 上校验 runtime token、
  把 proxy name / subdomain 绑定到已发放的隧道、拒绝自定义域名。
- 配额与并发：per-user 非终态隧道上限（默认 5）在事务级 PostgreSQL advisory lock 下判定；
  per-token 创建限流（默认 10/min，**当前是进程内的，不是分布式的**）；
  `Idempotency-Key` 按 `(token_id, key)` 作用域，成功响应体用从 plugin secret 派生的
  AES-GCM 密钥加密后落库。
- 配置全部走环境变量，见 `.env.example`（`HATCHWAY_DOMAIN` 派生 `api.` / `frps.` /
  `tunnel.` 三个子域；`HATCHWAY_PLUGIN_SECRET`、`HATCHWAY_FRPS_AUTH_TOKEN` 必须不同）。

### 1.4 已知的架构限制（与本次"可扩展"要求直接相关）

`DESIGN.md` 的 "Scale-out" 一节明确写着：**多副本需要共享限流器和后台 worker 协调；
当前部署与限流语义是单进程的。** 后台任务（reaper、retention sweeper）没有 leader 选举
或分布式锁保护。这是你要解决的核心问题之一。

### 1.5 hatchway-skills（配套 Agent Skill）

- 路径：本仓库内的 `hatchway-skills/`（原先是 `/Users/dongziyu/code/hatchway-skills`
  独立仓库，已并入本仓库；它自带的空 `.git` 已删除，自带的 `.gitignore` 保留）。
  README 里仍写着 `github.com/zydo/hatchway-skills` 这个目标 remote，但**现在它不是独立
  仓库了**——就地修改、随本仓库一起提交；README 里关于安装路径/仓库地址的说法如果因此
  过时，一并更新。
- 内容：`skills/hatchway-tunnel/SKILL.md` + `references/REFERENCE.md`、`evals/evals.json`
  （3 条已写好的 eval prompt：Stripe webhook、给远程老板看 React dev server、CI 冒烟测试脚本）、
  `README.md`、`LICENSE`。
- SKILL.md 已教 agent：先 `hatchway version && hatchway auth whoami` 快速失败；
  交互式 `hatchway http 3000 --ttl 15m`；脚本化时后台运行 `--json` 并轮询解析
  `{"public_url":…,"status":"reserved","tunnel_id":…}`；用 SIGINT 停止以触发吊销
  （SIGKILL 会漏掉吊销，只能等 TTL）。
- 这个 skill 必须和你改造后的 CLI/服务端契约保持一致——**你改了契约就必须在同一个
  commit 系列里同步改 skill**，不允许出现代码已改、skill 还教旧用法的漂移。

### 1.6 GCP 现状（已实测）

项目 `zdong-14850-alefa-ai`，账号 `zdong.14850@gmail.com`：

```
NAME         ZONE        MACHINE_TYPE   INTERNAL_IP  EXTERNAL_IP     STATUS
fetchwright  us-east1-b  c4d-highcpu-8  10.142.0.2                   TERMINATED
katze        us-east4-a  c4d-highcpu-4  10.150.0.2   34.145.247.213  RUNNING
```

`katze`（Ubuntu 26.04 LTS，6 GB 内存，**根盘 38G 已用 35G，仅剩 3.1G，92%**）上已有在跑的服务
（`caddy` 独占宿主 80/443 tcp + 443 udp；`bedtimenews-*` 占 8080；`openoj-*` 占 8081；
宿主还有 `sing-box` 等进程）。**这些只是背景信息：katze 完全不在本次任务范围内。**

**结论：不要在 katze 上做任何操作。** 不 ssh 上去部署、不改它的
`~/code/mycaddy/Caddyfile`、不复用它的 caddy 容器、不在它上面占端口或写文件。
`fetchwright` 处于 TERMINATED，同样不要动。

**本次部署必须在同一 project / 同一账号下新建一台专用于 Hatchway 的 VM。**
规模按"暂不考虑大量用户、少量 traffic"来定，目标是**在满足部署要求的前提下最省钱**：

- **不要照抄 katze 的 `c4d-highcpu-4`**——那是明显的浪费。基线取 **`e2-small`
  （2 vCPU 共享 / 2 GB）**；如果实测 `e2-micro`（1 GB）跑得动全套（控制面 + PostgreSQL +
  Caddy + frps）也可以用，跑不动就老实用 `e2-small`，不要为了省钱牺牲可用性。
  确需更大机型必须在 `docs/decisions.md` 里说明理由。
- 磁盘取够用即可（**20–30 GB，`pd-standard` 或 `pd-balanced`**），镜像用 Ubuntu LTS。
  内存紧张时可以开 swap；**不要在这台小 VM 上跑重型 Docker 构建**——本地构建好再传，
  或用多阶段构建 + 及时清理构建缓存。
- 网络用 **Standard 网络层级**（比 Premium 便宜），并**预留一个静态外部 IP**：
  `frps.` 与 `*.tunnel.` 必须是 DNS-only 指向固定 IP，临时 IP 一重启就失效。
- **不要用 Spot / 抢占式实例**——隧道服务被随时抢占会直接破坏可用性；
  想用它省钱就写进 `PENDING-APPROVAL.md` 等用户拍板。
- 区域选择自行决定并说明理由（离用户近、Standard 层级可用即可）；
  注意 GCP Always Free 的 `e2-micro` 只在 `us-west1` / `us-central1` / `us-east1` 生效。
- 命名与标签必须可辨识、便于回收（例如实例名 `hatchway-*`、加 `purpose=hatchway` label），
  并把**预估月成本**写进 `docs/deployment-avpn.md`。

### 1.7 Cloudflare 现状（已实测）

本机 Chrome 已登录用户的 CF 账号；本机有 `cloudflared` 2026.8.3 与 `flarectl`；
shell 环境里有 `CLOUDFLARE_API_TOKEN`（`cfat_…`）和 `CLOUDFLARE_ACCOUNT_ID`。
该 token 可读到 3 个 zone，**全部是 Free 计划**：`avpn.vip`、`bedtime.blog`、`dongziyu.com`。

**本次开发只准动 `avpn.vip`（zone id `7b58a67408d8f76d55798b9814a92184`）。**
该 zone 当前 8 条记录：

```
A     api.avpn.vip        -> 35.212.223.107   proxied=True
A     cdn.avpn.vip        -> 35.212.172.198   proxied=False
A     frps.avpn.vip       -> 35.212.223.107   proxied=False
A     oregon.avpn.vip     -> 35.212.204.4     proxied=False
A     *.tunnel.avpn.vip   -> 35.212.223.107   proxied=False
CNAME gcp-oregon.avpn.vip -> 736fdaef-…-fd18d3e4c76f.cfargotunnel.com  proxied=True
AAAA  avpn.vip            -> 100::            proxied=True
AAAA  www.avpn.vip        -> 100::            proxied=True
```

注意：`api.` / `frps.` / `*.tunnel.` 三条指向 `35.212.223.107` 的记录**看起来是上一次
Hatchway 部署留下的遗留记录**（该 IP 不属于本项目现存的任何 VM）。这三条属于 Hatchway
自身的记录，你可以按需要重新指向新部署；**其余记录（`cdn`、`oregon`、`gcp-oregon`、
根域与 `www` 的 AAAA）一律不得修改、删除或改变 proxy 状态。**
注意 `.env` 里还有一个旧的 `CLOUDFLARE_API_TOKEN=cfut_…`，未必仍然有效，需自行校验。

DNS 规则约束（来自 `docs/self-host.md`）：`frps` 与 `*.tunnel` 记录必须 **DNS-only**
（CF 代理不能承载 7000 端口的裸 TCP；通配符证书由自建 Caddy 用 DNS-01 签发）；
若 `api` 走代理必须用 **Full (strict)**，"Flexible" 会造成重定向循环。

---

## 2. 本次任务目标

> **一条简洁的命令，把一个 API 服务（可能是 AI Agent，也可能是别的）安全地暴露到公网的
> 临时域名上**（开发期用 `avpn.vip`），并让 `hatchway-skills` 作为配套的 Agent Skill
> 与之协同工作。

围绕这个目标：

1. **不必拘泥于现有设计和实现**。为了把 feature 做好，可以自由修改 DESIGN.md 定义的契约、
   数据模型、CLI 表面、部署拓扑。改了就同步更新 `DESIGN.md`、`docs/`、`README.md`、
   `PLAN.md`/`TODO.md` 与 `hatchway-skills`，不要留文档漂移。
2. **真实部署到 GCP VM 并端到端验证**：本地起一个示例 API/Agent 服务 → 一条命令暴露 →
   从公网真实 curl 到 `https://t-*.tunnel.avpn.vip`（或你设计的新域名形态）拿到 200 →
   停止后确认隧道被吊销、再次访问失败。把验证过程的真实输出记录下来。
3. **可扩展性方案**：当前只有一台 VM，但你必须**做好 scaling 的方案与代码准备**——
   多 zone、多 VM 的分布式部署能力，**必须保证服务的一致性**。具体至少要处理：
   - 控制面多副本：限流器从进程内改为共享（PostgreSQL 或其他共享存储），
     后台 worker（reaper / retention sweeper）需要 leader 选举或分布式锁，避免重复执行；
   - 数据面多 frps 节点：隧道要记录归属节点，客户端要被路由到正确的节点，
     Caddy 请求鉴权与 frps 插件回调要在多节点下仍然一致；
   - DNS / 边缘：多 zone 下 `*.tunnel` 的解析与证书策略；
   - 一致性与幂等：跨副本的 `Idempotency-Key`、配额判定、状态迁移不能出现竞态。
   **现在只需要实现到"单节点跑通、但改成分布式部署很容易"的程度**，分布式的具体实现可以
   留作后续，但设计必须写清楚、接口必须预留好、不能留下只能单机成立的隐含假设。
4. 如果方案需要**别的云服务**（负载均衡、托管数据库、对象存储、Redis 等），
   **要设计出来写进文档，但暂时不要实现**。

---

## 3. 硬约束（不可违反）

### 云资源

- **只允许创建 GCP VM**。除 VM 外不要创建任何其他 GCP 云服务（LB、Cloud SQL、GCS、
  Memorystore、Cloud DNS、Artifact Registry…）。确有需要就写进待决策清单等用户拍板。
- 只用 project `zdong-14850-alefa-ai` + 账号 `zdong.14850@gmail.com`。
  本机 gcloud 还登录着 `aws20231023@gmail.com`、`aws20241109@gmail.com`、`dongziyu6@gmail.com`
  （当前 active 账号是 `aws20241109@gmail.com`！每条 gcloud 命令都显式带
  `--account` 和 `--project`），**绝不能碰这些账号或本机登录的其它云服务凭证。**
- **必须新建一台专用于 Hatchway 的 VM**，规格按第 1.6 节：在满足部署要求的前提下取最省的
  配置（基线 `e2-small`，20–30 GB 盘，Standard 网络层级，非 Spot）。新建资源用可辨识的
  命名/标签并记录，方便回收。
- 除 VM 外，**只允许附带创建这台 VM 必需的最小网络资源**：一个静态外部 IP 预留、
  以及为它放行 22 / 80 / 443 / frps 端口的防火墙规则。除此之外的 GCP 资源一律不创建。
- **完全不要碰 `katze`**：不在其上部署、不 ssh 进去改配置、不改它的 Caddyfile、
  不占它的端口，更不许停止 / 重启 / 重配它上面的 caddy / bedtimenews / openoj / sing-box。
  `fetchwright`（TERMINATED）同样不要动。

### Cloudflare

- **只能操作 `avpn.vip` 这一个 zone**，其它 zone 一律不碰。
- **不要改变已有配置**（第 1.7 节列出的非 Hatchway 记录、以及 zone 级别的 SSL/安全/缓存
  等既有设置）。Hatchway 自己的 `api.` / `frps.` / `*.tunnel.` 记录可以按需调整。
- **必须仅使用 Cloudflare 免费功能。** 如果某个付费功能能显著提升系统性能或安全性，
  **写进待决策清单说明收益与成本，等用户拍板，不要自行启用。**
- 可以用浏览器操作 CF 控制台（Chrome 已登录），也可以用本机 CLI / API token；
  优先用可复现的 CLI/API 方式，浏览器仅在 CLI 做不到时使用。

### 代码与 git

- 全程在同一个工作分支上进行（Go 代码 + 文档 + `hatchway-skills/` 一起），
  **不要在 main 上工作，不要合并回 main，不要 push 到 remote，不要改写已有历史**。
- 本次**显式授权你在工作分支上自行 commit**（这是对"不自动提交"默认规则的一次性豁免），
  请提交粒度清晰、信息只描述改动本身，**不要加任何 co-author 行或 AI 署名 trailer**。
- 秘密（token、密码、CF/GCP 凭证）绝不进 git，绝不写进任何被 tracked 的文件；
  `.env` 已被 gitignore，保持 `chmod 600`。日志与文档里出现凭证一律脱敏。
- 临时文件、草稿、调研中间产物放 `.localonly/`（已 gitignore，需要时自行创建），
  **且不允许任何被 tracked 的文件引用 `.localonly/` 里的内容**——上面列出的交付物
  都是要提交进仓库的正式文件，不要放进 `.localonly/`。
- 如果引入了新的第三方可再分发组件（二进制、镜像里打包的软件），必须补进
  `THIRD_PARTY_LICENSES`。
- CI 必须仍然通过：`make build && make test && make lint`（PostgreSQL 集成测试需要本地
  可用的数据库；`make test-short` 可跳过）。改了行为就补测试。

---

## 4. 交付物

1. 工作分支上的完整实现 + 通过的测试 + 同步更新的文档（含 `hatchway-skills/`）。
2. **线上可用的部署**：`avpn.vip` 下真实可访问的 Hatchway 服务，以及一份能复现该部署的
   脚本/compose/文档（不含明文密钥）。
3. `docs/deployment-avpn.md`：本次实际部署的拓扑、主机、端口、域名、密钥位置（只写位置
   不写值，**绝不写值**）、启动与回滚步骤、如何安全销毁。
4. **`docs/scaling.md`（或等价文档）**：多 zone / 多 VM 分布式方案、一致性模型（谁是权威、
   哪些状态需要共享、如何避免竞态）、需要但暂不实现的外部云服务清单、以及从当前单节点
   演进到分布式的具体迁移步骤。
5. `docs/decisions.md`：你做过的重要设计取舍与理由（尤其是偏离原 DESIGN.md 的地方）。
6. `PENDING-APPROVAL.md`（仓库根目录）：见第 5 节。
7. 一份**端到端验证记录**（真实命令 + 真实输出，凭证脱敏），证明"一条命令暴露服务"确实可用。
8. 收尾报告：做了什么、验证了什么、留下了哪些线上资源、哪些没做完及原因。

---

## 5. 需要用户拍板的事项：记录，不阻塞

建立仓库根目录下的 `PENDING-APPROVAL.md`，把下列情况**写下来继续往前做**
（用当前免费/已授权方案先跑通），不要停下来等人：

- 任何能提升性能/安全但需要 **Cloudflare 付费**的功能（写清楚：解决什么问题、
  哪个套餐、大致成本、不启用的代价）。
- 任何需要 **GCP VM 之外的云服务**的方案。
- 任何会改动 `avpn.vip` 上非 Hatchway 记录、或影响 katze 已有服务的操作——
  这类操作**一律不许执行**，只记录。
- 任何公开发布动作（把 skill 单独推成 GitHub 仓库、提交 terminalskills.io、
  给 awesome-tunneling 提 PR、打 release tag）——只准备好内容，不要对外发布。

---

## 6. 工作方式

- 先调研、后设计、再实现、最后部署验证；每个阶段结束时把结论落到文件里，
  这样即使上下文被压缩也能续上。
- 优先用可复现的命令行手段；浏览器自动化只在必要时用。
- 遇到失败不要反复重试同一条命令，换方案并把原因记下来。
- 报告要如实：测试失败就贴输出，步骤跳过就说明跳过，不要用"应该可以"代替实际验证。
- 全程零人工干预跑完；确实无法在不违反上述硬约束的情况下完成的部分，
  就把其余部分全部做完，并在收尾报告里明确说明哪一块没做、为什么。
