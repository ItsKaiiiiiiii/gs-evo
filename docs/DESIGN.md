# gs-evo 设计方案 v2

> 客户端-云端 Skill 收集 · 进化 · 分发系统
> v2 变更基线：在 v1 基础上融合评审意见，重点补强**安全模型、可靠投递语义、冲突策略、进化评估**四处。

## 0. v2 相对 v1 的变更摘要

| # | 变更 | 原因 |
|---|------|------|
| C1 | 承认 `task.start` = 间接任意代码执行，安全控制点从"指令封闭枚举"上移到**云端指令创建者鉴权 + Agent 权限模式 + 本地确认开关**三层 | v1 安全表自相矛盾：prompt 交给有 shell 工具的 agent 等于任意命令 |
| C2 | **ed25519 签名从 v2 提前到 v1**；SkillVer 引入生命周期状态机（draft→candidate→reviewed→stable→deprecated），进化产物不得自动 stable | skill 是"agent 将来遵循的指令"，下发=供应链写入；自进化使攻击面可自我复制 |
| C3 | 可靠投递语义落地：每设备单调 `cmd_seq` + Client ack + 云端补发（at-least-once）+ Client 按 `cmd_id` 幂等去重 + 任务结果落盘 spool | v1 只提"增量对账"，未定义机制，断线会丢指令/结果 |
| C4 | 新增 `task.events_replay` 指令；任务事件缓冲与全量 stdout 落盘规则明确 | v1 提到"重放请求"但协议中无此消息 |
| C5 | 白名单**主权**约束：云端 policies 只能收紧、不能扩大目录白名单 | 否则云端拥有自我扩权通道 |
| C6 | skill 安装冲突策略：本地被改动 → 默认 `skip_and_report`，不静默覆盖；本地保留一代 `.prev` 支持快速回滚 | 用户手改会被云端清掉；坏版本回滚不应依赖网络 |
| C7 | 明确 Windows 差异：进程树终止用 Job Object；安装覆盖处理被锁文件 | Windows 在自启支持范围内，这两处 POSIX 假设会翻车 |
| C8 | 进化闭环优先建**评估**（BenchmarkSet + EvaluationRun），P3 验收为"人工在环的完整闭环"，全自动发布推迟到 P4 且需数据达标 | 生成易、评估难；防 Goodhart（讨好基准而非真变好） |
| C9 | 协议第一天带 `proto_version` + `capabilities`；hello 上报完整 skill 列表（非摘要） | 事后补版本协商很痛；摘要比对有碰撞歧义 |
| C10 | 传输阈值调整：内嵌 base64 上限 256KB，其余一律走预签名 URL | WS 帧过中间代理常限 1MB；4MB base64 膨胀 33% 易翻车 |
| C11 | 设备模型明确为 **1 OS 用户 = 1 device**；任务默认不跨 Client 重启存活（v1 明示） | skill 在用户 home 下；隐藏行为必须写成显式约定 |
| C12 | 数据模型新增 `EvaluationRun` / `BenchmarkSet` / `DeviceSkillInstall`，`Command` 补 `cmd_seq`/`acked_at`/`created_by` | 下发覆盖率、回滚决策、审计需要这些事实 |

---

## 1. 背景与目标

**现状**：每台电脑上的 coding agent 相互隔离，各自的 skill 无法互通，无法集中管理和持续进化。

**目标**：构建客户端-云端系统，实现：

1. Client 常驻每台电脑后台，与云端保持实时连接并上报心跳；
2. 云端随时下发指令，**收集**各机器上的 skill（含能力描述）到云端；
3. 云端对 skill 集中管理与**自进化**（版本化、评估、改进、审批）；
4. 云端把审批后的 skill **下发安装/更新**到指定机器的指定目录；
5. 云端实时下发任务，Client 通过 **stdio** 驱动本机 agent 执行并回传结果与过程事件。

**设计原则**（按优先级）：

- **简单**：一条 WSS 长连接承载所有通信；一种"指令-结果"消息模型覆盖所有云端能力；
- **清晰**：Client 只做"执行器"，云端只做"决策者"，职责单向依赖；
- **可信**：skill 的每一次传播（采集、进化、下发）都可审计、可验证、可回滚；
- **可扩展**：新功能 = 云端新增一种指令类型 + Client 注册一个处理器，协议与架构不变。

## 2. 总体架构

```
                          ┌──────────────────────────────────────────────────┐
                          │                  Cloud（云端）                    │
                          │                                                  │
                          │  ┌──────────┐    ┌──────────────┐   ┌─────────┐  │
                          │  │ Gateway  │───▶│ Skill Registry│◀──│ Task    │  │
                          │  │ (WS网关) │    │  + 签名/状态机 │   │ Manager │  │
                          │  └────┬─────┘    └──────▲───────┘   └────┬────┘  │
                          │       │                 │          ┌─────┴─────┐ │
                          │  ┌────┴─────┐    ┌──────┴──────┐   │  Queue /  │ │
                          │  │ Device   │───▶│  Evolution   │  │  Dispatch │ │
                          │  │ Registry │    │   Engine     │  └───────────┘ │
                          │  │ + RBAC   │    │ (评估→候选→人审)│               │
                          │  └──────────┘    └─────────────┘                │
                          │        Object Store（内容寻址 bundle，预签名URL）   │
                          └──────────────────────▲───────────────────────────┘
                                                 │ WSS（唯一通道）+ HTTPS（大文件）
                 ┌──────────────────────────────┼──────────────────────────────┐
                 │                              │                              │
         ┌───────┴────────┐             ┌───────┴────────┐             ┌───────┴────────┐
         │ Client (用户A@机1)│             │ Client (用户B@机2)│      ...   │ Client (用户N@机M)│
         │                │             │                │             │                │
         │  Connection Mgr（重连/心跳/鉴权） │                │             │                │
         │  Command Dispatcher（seq ack +  │                │             │                │
         │    cmd_id 幂等去重）             │                │             │                │
         │  Result Spool（断线结果落盘补传） │                │             │                │
         │  Agent Adapter（探测/spawn/stdio）│               │             │                │
         │  Task Runner（隔离目录/看门狗/事件）│               │             │                │
         │  Skill Manager（扫描/打包/冲突检测/│               │             │                │
         │    验签/原子安装/.prev 回滚）      │               │             │                │
         └────────────────┘             └────────────────┘             └────────────────┘
```

一句话概括：**Client 是云端的"手"**——云端所有意图（收 skill、发 skill、跑任务）都变成一条结构化指令，经同一条 WSS 通道下发；Client 执行后把结果与过程事件沿原通道回传。**但"手"带三道保险：验签、白名单、幂等。**

## 3. 核心设计决策

| # | 决策 | 理由 |
|---|------|------|
| D1 | **单一 WSS 长连接**承载心跳、指令、任务事件、skill 通知；大文件走 HTTPS 预签名 URL | 免轮询、免多协议；NAT/内网友好；大文件不走 WS 帧避免代理限制 |
| D2 | **统一指令模型** `cmd → cmd_result`，投递语义 **at-least-once + Client 幂等** | 所有云端能力收敛到一种交互模式；网络抖动下指令/结果不丢、不重复执行 |
| D3 | Client **不开放入站端口**，只外连 | 无需在用户机器上暴露服务，安全面最小 |
| D4 | Agent 交互收敛为 **AgentAdapter 接口**（stdio JSON-lines，P2 前出协议 spec） | 未来支持 codex/claude/ACP 等只加 adapter，不动框架 |
| D5 | skill 传输用**内容寻址**（sha256 tar 包） | 天然去重、增量跳过、完整性校验一次解决 |
| D6 | 云端持有**唯一事实**（skill 版本库、设备表、任务表、指令审计表） | 进化、审计、灰度都发生在云端，Client 无需理解策略 |
| D7 | Client **无状态可重装**：本地仅配置 + 缓存 + spool | 运维简单，故障恢复 = 重装重连 |
| D8 | **stable 版本必须带云端 ed25519 签名，Client 内置公钥验签**（v1 即启用） | skill 是 agent 将来执行的指令；sha256 只证明传输完整，签名才证明来源可信 |
| D9 | SkillVer **生命周期状态机**，进化产物不得自动 stable | 自进化=自动改写的供应链，必须有发布闸门（v1 由管理员人工闸门，见 D11） |
| D10 | skill 目录白名单**主权在本地**：仅本机 owner 可改，云端 policies 只能收紧不能扩大 | 防云端（或攻破云端的攻击者）自我扩权 |
| D11 | 任务执行权限三层控制：云端指令创建者鉴权（v1 单管理员）→ `task.start` 携带 `max_permission` 与本地配置取交集 → 本地 `require_confirm` 开关 | `task.start` 实质是间接任意代码执行，真正的信任边界是"谁能创建指令"与"agent 以什么权限模式运行" |
| D12 | Agent 运行凭证用**本机已有配置**，云端不下发 env/secret | 云端推送密钥既扩攻击面又制造密钥管理负担 |
| D13 | v1 **任务不跨 Client 进程重启存活**；机器级多用户按 **1 OS 用户 = 1 device** | 显式声明边界，避免隐藏 bug；skill 在用户 home 下，按用户建模最简 |

## 4. 组件设计

### 4.1 Client（gs-agent-worker，每 OS 用户一个后台进程）

单二进制（**Go**：交叉编译单文件、WS 生态成熟、multica 同栈复用经验）。两种形态：`worker daemon`（后台服务）与 `worker ctl`（本机 CLI：login / status / logs / skills list / skills rollback）。

| 模块 | 职责 |
|------|------|
| **Connection Manager** | 维护 WSS；指数退避重连（1s→30s + 抖动）；心跳 30s（优先 WS 协议层 ping，应用层 ping 兜底）；`hello` 鉴权握手（device token + `proto_version` + `capabilities` + `last_acked_cmd_seq`） |
| **Command Dispatcher** | 按 `type` 路由指令到处理器；**按 `cmd_seq` 逐条 ack**；**幂等缓存**（最近 128 条 `cmd_id → result` 的 LRU，重复指令直接回缓存不重复执行）；本地并发上限（默认 2）；过期指令（`expires_at`）直接回 expired 不执行 |
| **Agent Adapter** | 启动时探测本机 agent（路径、版本、可用性）；运行期 spawn 子进程，stdin/stdout 按行交换 JSON（协议见 P2 spec）；**权限模式映射**（把 `task.start.max_permission` 翻译为各 agent 的无头权限参数）；超时与看门狗；**进程树终止**：POSIX 进程组 / **Windows Job Object** |
| **Task Runner** | 每任务隔离工作目录 `tasks/<task_id>/`；全量 stdout/stderr 落盘本地；流式转 `task.event` 上报（单帧上限 64KB，有界缓冲 2000 条，超出只传引用）；任务结束后**结果先写 spool 再发送**，成功送达后删除 |
| **Skill Manager** | 扫描白名单目录生成 manifest；打包（tar+gzip→sha256）；下载**验签 + sha256 校验**后**冲突检测**（磁盘 hash vs `state.json` 记录，本地被改动 → 默认 `skip_and_report`）→ 解压临时目录 → **原子安装** → 旧版移入 `<root>.prev`（保留一代，支持本地回滚） |
| **Result Spool** | `spool/` 目录：所有未确认送达的 `cmd_result` / 任务最终结果落盘，重连后优先补传 |
| **Local State** | `~/.gs-agent-worker/`：config.toml、日志、state.json、cache、tasks、spool |

**Client 刻意不含任何策略**：不决定何时进化、不决定装到哪台机器、不解析任务优先级。**但 Client 拥有两条不可协商的本地主权**：目录白名单的修改权、`require_confirm` 开关。

### 4.2 Cloud（初期单体服务 + 数据库 + 对象存储）

| 模块 | 职责 |
|------|------|
| **Gateway** | WSS 接入；token 鉴权；协议版本协商（`proto_version` 不匹配即断并提示升级）；连接-设备绑定；`cmd_seq` 分配与补发队列（重连后重发未 ack 且未过期指令）；消息路由 |
| **Device Registry** | 设备档案：id、主机名、OS/arch、worker 版本、agent 运行时、labels（圈定分发范围）、`last_acked_cmd_seq`、心跳/在线状态 |
| **Skill Registry** | skill 版本库：manifest + 内容包（内容寻址）；**stable 版本签名服务**（ed25519）；**状态机执行**（draft→candidate→reviewed→stable→deprecated，stable 转正 v1 一律管理员显式操作）；检索与比对 |
| **Task Manager / Queue** | 任务创建（记录 `created_by`）、按设备排队、推送 `task.start`、汇聚过程事件与最终结果、断线重放事件 |
| **Evolution Engine** | 自进化闭环（离线/异步）：消费清单与任务反馈 → 生成候选版本（带 diff 与理由）→ **产出 candidate（不是 stable）** → 灰度设备跑 EvaluationRun → 评审报告给管理员 → 人工放行后签名转 stable |
| **Console/API** | 管理界面（初期 CLI 即可）：设备管理、skill 浏览与 diff 审阅、任务下发与审批、指令审计查询 |

**初期实现约束**：单体服务 + 一个数据库 + 一个对象存储；Evolution Engine 是同进程异步 job。**先不分微服务。**

## 5. 通信协议

### 5.1 通道与心跳

- 传输：`wss://cloud/api/v1/agent-ws`，JSON 文本帧；大文件走 `https://cloud/api/v1/bundles/*`（预签名 URL，一次性 PUT/GET）；
- 鉴权：连接后首条 `hello` 携带 device token，失败即断；
- 心跳：WS 协议层 ping/pong 优先，应用层 `ping`（30s）兜底；云端 **90s** 无任何帧判定离线（3 倍容忍）；
- 重连：指数退避（1s→30s）+ 抖动；重连成功即重新 `hello`，云端按 `last_acked_cmd_seq` **补发未 ack 指令**，Client 补传 spool 中未送达结果。

### 5.2 可靠投递：at-least-once + 幂等

- 云端为每台设备维护单调递增 `cmd_seq`，每条指令携带 `{cmd_id, cmd_seq, expires_at?}`；
- Client 处理完成后回 `cmd_result`，云端据此置 `acked_at`；
- 连接断开期间下发的指令在重连后**补发**；Client 靠 `cmd_id` 幂等缓存保证**不重复执行**（`task.start`、`skills.install` 重复执行后果严重，这是硬要求）；
- 指令默认有效期（如 10 分钟），过期未送达置 `expired`，避免陈旧指令迟到执行。

### 5.3 统一指令模型

云端 → 客户端只有一种消息形状：

```json
{ "type": "cmd", "cmd_id": "c-8f2a", "cmd_seq": 137, "cmd_type": "skills.install",
  "expires_at": "2026-10-27T12:00:00Z", "payload": { } }
```

客户端 → 云端回复：

```json
{ "type": "cmd_result", "cmd_id": "c-8f2a", "ok": true, "data": { }, "error": null }
```

指令集（v1）：

| cmd_type | payload 摘要 | 作用 |
|----------|--------------|------|
| `agent.probe` | `{}` | 探测本机 agent 类型/版本/可用性 |
| `skills.scan` | `{roots?: [..]}` | 扫描白名单目录，返回 manifest 列表（含本地改动状态） |
| `skills.collect` | `{name, root, content_hash?}` | 打包上传指定 skill；云端已有同 hash 则跳过 |
| `skills.install` | `{name, version, bundle_url, sha256, signature, target_root, on_conflict?}` | 验签+校验+冲突检测后原子安装/覆盖；冲突默认 skip |
| `task.start` | `{task_id, prompt, workdir_policy, timeout_s, max_permission, env?}` | spawn agent 执行任务，流式回传；`max_permission` 与本地配置取**更严者** |
| `task.cancel` | `{task_id}` | 终止进程树（POSIX 进程组 / Windows Job Object），回收工作目录 |
| `task.events_replay` | `{task_id, from_seq}` | 从本地事件缓冲/落盘文件重发任务事件（v2 新增） |
| `client.update` | `{version, url, sha256, signature}` |（预留）Client 自更新 |

客户端 → 云端其余消息：

| type | 作用 |
|------|------|
| `hello` | 鉴权 + 协商 + 对账（详见下） |
| `ping` / `pong` | 应用层心跳兜底 |
| `task.event` | `{task_id, seq, kind, data}`：stdout/stderr 增量、状态迁移、产物引用（超限内容只传引用） |
| `ack` | `{cmd_seq}`：逐条确认收到（可并入 cmd_result，视实现合并） |

### 5.4 hello 与对账

```json
{
  "type": "hello",
  "proto_version": 1,
  "token": "…",
  "device": { "hostname": "…", "os": "darwin", "arch": "arm64", "worker_version": "0.3.0" },
  "agents": [ { "type": "claude-code", "version": "2.x", "path": "…", "ok": true } ],
  "skills": [ { "name": "gerrit-review", "version": "3", "content_hash": "sha256:1f0c…", "local_state": "clean" } ],
  "last_acked_cmd_seq": 136
}
```

要点：`proto_version` 不匹配云端直接断连并在错误中给出最低支持版本；`skills` 上报**完整列表**（数量级很小，不用摘要，无碰撞歧义），`local_state: clean|modified` 由磁盘 hash 与 `state.json` 比对得出——云端据此能发现"本地有未上报的改动"，可发起 collect 收走。

### 5.5 Skill Manifest

```json
{
  "name": "gerrit-review",
  "version": "3",
  "root": "~/.claude/skills/gerrit-review",
  "entry": "SKILL.md",
  "summary": "拉取 Gerrit 评论并逐条回复、标记 Done",
  "capabilities": ["gerrit.query", "gerrit.comment", "review.reply"],
  "content_hash": "sha256:1f0c…",
  "files": 7,
  "local_state": "clean",
  "updated_at": "2026-10-20T08:12:00Z"
}
```

`summary`/`capabilities` 从 `SKILL.md`（或 front-matter）解析；解析不出则上报纯 hash，由云端补齐。**多 agent 可移植性问题显式记录**：v1 假定 skill 面向 claude-code 格式；manifest 增加 `agent_compat: ["claude-code"]` 字段（v1 写死），为未来多 agent 类型预留判定依据。

### 5.6 内容传输

- **< 256KB**：base64 内嵌 `cmd_result.data.bundle`；
- **≥ 256KB**：`skills.collect` 返回 `{upload_url}`（预签名 PUT），Client 直传对象存储；`skills.install` 同理给下载 URL；
- 一律附 sha256；**stable 版本另附 ed25519 签名（对 sha256 值签名）**，Client 内置公钥，验签失败拒装并告警。

## 6. 关键流程

### 6.1 上线注册与对账

```
Client                                Cloud
  │── hello {token, proto_version, device, agents, skills[], last_acked_cmd_seq} ─▶│
  │◀──── ok {server_time, policies(仅收紧类), pending_cmds[n]} ────────────────────│
  │     （云端补发未 ack 指令 → Client 幂等处理 → 逐条 cmd_result/ack）              │
  │── spool 补传（此前断线期间的任务结果）────────────────────────────────────────▶│
  │── ping(30s) ──────────────────────────────────────────────────────────────▶│
  │      断线 → 退避重连 → 重新 hello → 补发/补传                                 │  90s 无帧 → 离线
```

`policies` 仅允许包含收紧类字段（如 `disable_roots: [...]`、`max_concurrency`）；任何试图**新增**白名单目录的 policy 被 Client 忽略并记录审计日志（D10）。

### 6.2 Skill 上行收集

```
Cloud                                          Client
  │── cmd skills.scan ──────────────────────────▶│  扫描白名单目录（含 modified 检测）
  │◀─ cmd_result {manifests[]} ─────────────────│
  │   （对比 content_hash，仅对新增/变更发起 collect）│
  │── cmd skills.collect {name, root} ──────────▶│  tar+gzip → sha256
  │◀─ {bundle(<256KB) | upload_url} ────────────│  上传内容包
  │   落 SkillVer {source: collected, status: draft}，记录 origin_device
```

### 6.3 Skill 下发安装（含冲突与回滚）

```
Cloud                                              Client
  │── cmd skills.install {name, v4, url, sha256,   │ 1. 下载 → sha256 校验
  │      signature, target_root, on_conflict} ───▶│ 2. ed25519 验签（非 stable 拒绝）
  │                                               │ 3. target_root ∈ 本地白名单？
  │                                               │ 4. 冲突检测：磁盘 hash == state.json？
  │                                               │    ├ clean → 继续
  │                                               │    └ modified → skip_and_report（默认）
  │◀─ cmd_result {installed: v4 | conflict_skipped}│ 5. 旧版移 <root>.prev → tmp 解压 → 原子 rename
```

- **回滚**：`worker ctl skills rollback <name>`（本地用 `.prev` 秒回）或云端重发上一 stable 版本的 install 指令；
- **Windows**：rename 覆盖遇被锁文件 → 退避重试（3 次）→ 失败回报，不部分覆盖。

### 6.4 任务实时下发

```
Cloud                                Client                          agent 进程
  │── cmd task.start {task_id, prompt, │ 建隔离工作目录                  │
  │    timeout_s, max_permission} ───▶│ permission = min(云端, 本地配置)  │
  │                                   │── spawn(agent, cwd, perm) ────▶│
  │                                   │── stdin: prompt(JSON-line) ───▶│
  │◀─ task.event {stdout_delta} ───────│◀─ stdout: JSON lines ─────────│  全量同时落盘
  │◀─ task.event {tool_call|status} ───│◀─ …                           │  单帧≤64KB，缓冲2000条
  │◀─ cmd_result {exit, summary,       │  等待退出 → 结果写 spool → 发送  │
  │    artifacts[]} ──────────────────│        （送达成功才删 spool）     │
  │── cmd task.cancel {task_id} ─────▶│  终止进程树（Win: Job Object）    │
```

- `task.event.seq` 单调递增；云端检测到空洞即下发 `task.events_replay {task_id, from_seq}`；
- **断线场景**：Client 继续跑完任务，stdout 已落盘；重连后云端可 replay 事件 + Client 从 spool 补传最终结果；
- v1 明示：**Client 进程重启后进行中任务即失败**（不恢复），任务目录按 TTL 回收。

### 6.5 Skill 自进化闭环

```
 ①采集   skills.scan/collect ──▶ Skill Registry（全局视图：谁有什么、能做什么）
 ②评估输入  Task 执行结果/评分 + 使用统计 + BenchmarkSet ──▶ Evolution Engine
 ③生成    LLM 改写 SKILL.md / 脚本 / 变体 ──▶ SkillVer {source: evolved, status: candidate}
          （diff 与生成理由强制留存，供人工审阅）
 ④验证    candidate 下发灰度设备（按 Device labels 圈选）→ 跑 BenchmarkSet → EvaluationRun
          （指标 + LLM-judge 原始输出留存可复查；防 Goodhart：基准集定期人工更新）
 ⑤放行    EvaluationRun 报告 ──▶ 管理员审阅 diff + 指标 ──▶ 显式转 stable → 云端签名
 ⑥分发    skills.install（全网/分批，按 labels）──▶ DeviceSkillInstall 记录覆盖率
 ⑦回流    新版本执行效果进入 ②，闭环持续
```

- **状态机**：`draft → candidate → reviewed → stable → deprecated`；`evolved` 来源**必须**经 `reviewed`（人工）才能 stable；`collected` 来源 v1 同样由管理员显式放行（v1 无自动发布，所有 stable 转正都是人工动作——这本身就是闸门）；
- **Evolution Engine 与系统其余部分只通过 Skill Registry 的版本表交互**——进化策略可独立演进（规则→LLM→多 agent 评审），不影响通道与客户端；
- P3 验收口径：**人工在环**跑通一条 skill 的完整闭环；全自动发布是 P4 目标，且以 EvaluationRun 历史数据证明评估可靠为前提。

## 7. 数据模型（云端）

```
Device      {id, token_hash, hostname, os, arch, worker_version, labels[],
             last_seen, status, last_acked_cmd_seq}

Skill       {id, name, latest_stable, latest_any, agent_compat[]}
SkillVer    {id, skill_id, version, manifest, content_sha256, signature,
             origin_device, source: collected|evolved|forked, parent_version?,
             status: draft|candidate|reviewed|stable|deprecated, created_at}

BenchmarkSet  {id, name, version, task_cases_ref, updated_by, updated_at}
EvaluationRun {id, skill_ver_id, benchmark_set_id, device_ids[], started_at,
               finished_at, metrics, judge_outputs_ref, verdict, notes}

DeviceSkillInstall {id, device_id, skill_ver_id, installed_at,
                    result: ok|conflict_skipped|failed, prev_ver_id}

Task        {id, device_id, prompt, max_permission, status, created_at,
             created_by, events_ref, result_ref}
Command     {id, cmd_seq, device_id, cmd_type, payload, created_by,
             status: pending|sent|acked|expired, issued_at, acked_at, result_at}
```

要点：
- `Command` 全量落库 + `created_by`——审计与排障的单一事实来源，**也是"谁有权创建指令"的追责依据**（D11）；
- `SkillVer.source` + `parent_version` 构成进化谱系；`status` 是发布闸门的物理载体；
- `DeviceSkillInstall` 回答"哪个版本覆盖到哪台机器、结果如何"——灰度决策与回滚范围的依据；
- `EvaluationRun` 留存 LLM-judge 原始输出——评估结论必须可复查、可迭代。

## 8. 安全

**诚实的前提**：`task.start` 把 prompt 交给本机有 shell 工具的 coding agent，**实质上等于云端可间接执行任意命令**。因此安全模型的核心不是"指令封闭枚举"（那只是收窄接口），而是下面四层：

| 层 | 措施 |
|----|------|
| 传输 | 强制 WSS/HTTPS（TLS） |
| 身份 | 每设备独立 token（`worker login` 获取，可吊销）；token 哈希落库；云端**指令创建者鉴权**（v1 单管理员 + API token，多租户 RBAC 在 P4） |
| 指令面 | 封闭枚举 + 结构化 payload；`Command` 全量审计（含创建者）；指令带有效期；**at-least-once + Client 幂等**防重放副作用 |
| 执行面 | `task.start` 的 `max_permission` 与本地 permission_mode 配置**取更严者**（建议默认 `readonly` 起步）；本地 `require_confirm` 开关（高危机器可开：任务到达先本机确认）；进程树隔离 + 看门狗超时；敏感 env 不透传，agent 凭证用本机已有配置（D12） |
| 文件面 | skill 读写仅限本地白名单目录，**白名单主权在本地，云端 policies 只能收紧**（D10）；安装走临时目录 + 原子 rename + `.prev` 保留 |
| 内容 | 包 sha256 校验；**stable 版本 ed25519 签名，Client 内置公钥验签，v1 即启用**（D8）；进化产物强制人工审后才可签名 |
| 吊销 | 云端吊销 token 后 Gateway 立即断连并拒绝重连 |

## 9. 本地布局（Client）

```
~/.gs-agent-worker/
├── config.toml        # server_url、token、skill 目录白名单、并发上限、
│                      # task.permission_mode、require_confirm
├── worker.log
├── state.json         # 已装 skill 清单（name → version/content_hash），用于增量对账与冲突检测
├── cache/bundles/     # 内容包缓存（内容寻址）
├── spool/             # 未确认送达的结果（断线补传）
└── tasks/<task_id>/   # 每任务隔离工作目录（含全量输出落盘），按 TTL 回收
```

安装后的 skill 目录：

```
~/.claude/skills/gerrit-review/          # 当前版本
~/.claude/skills/gerrit-review.prev/     # 上一版本（保留一代，支持本地秒回滚）
```

开机自启：Windows 计划任务/Run 键、macOS LaunchAgent、Linux systemd user unit（multica 同款，`worker autostart enable`）。

## 10. 可扩展性

扩展一律走"**新指令类型 + 新处理器**"，通道、网关、审计、幂等、SDK 全部复用：

| 未来功能 | 落点 | 新增内容 |
|----------|------|----------|
| 多 agent 类型（codex/claude/ACP…） | AgentAdapter + `agent_compat` 判定 | 一个 adapter 实现；hello 上报多 runtime |
| 客户端自更新 | `client.update`（已预留，含签名） | 一个处理器 |
| 遥测/健康巡检 | `client.diag` | 磁盘/版本/日志尾部采集 |
| 远程文件/配置分发 | `files.put` | 复用 bundle 传输 + 白名单 + 验签 |
| 定时任务/Cron | Task Manager | 到期入队，无需改 Client |
| 多租户/团队 | Device Registry 加 org + RBAC | 云端纯数据模型变更 |
| MCP server 分发 | `skills.install` 的 target 扩一类根 | 复用安装与签名机制 |
| 全自动发布 | Evolution Engine 发布策略 | 前提：EvaluationRun 数据证明评估可靠 |

反向兼容：Client 遇到未知 `cmd_type` 回 `{ok:false, error:"unsupported"}`；hello 的 `proto_version` + `capabilities` 让云端按设备能力下发，升级永不阻塞。

## 11. 实施路线

| 阶段 | 范围 | 验收标准 |
|------|------|----------|
| **P0 骨架**（1-2 周） | Client：连接+心跳+hello（proto_version/capabilities/last_acked_cmd_seq）+ 指令分发框架 + **cmd_seq ack + 幂等缓存 + spool 骨架**；Cloud：Gateway + Device Registry + Command 审计落库；`agent.probe` | 控制台可见设备在线/离线切换；**拔网线重连后指令补发且不重复执行** |
| **P1 Skill 通道**（2-3 周） | `skills.scan/collect/install` + Skill Registry + 内容寻址 + **ed25519 签名/验签** + **版本状态机** + **冲突检测（skip_and_report）** + `.prev` 回滚 | A 机采集 → 云端人工转 stable 签名 → 装到 B 机；**本地改动被跳过并上报**；`rollback` 秒回上一版 |
| **P2 任务通道**（3 周） | **Agent 协议 spec（先行文档）** → AgentAdapter（stdio JSON-lines + 权限模式映射）+ Task Runner + `task.start/cancel/events_replay` + 事件流 + Windows Job Object | 云端下发 prompt 实时回传；**断网重连后事件补齐、结果不丢（spool）**；cancel 在三平台都能杀干净进程树 |
| **P3 自进化（人工闭环）**（持续） | BenchmarkSet + EvaluationRun + 生成候选（diff/理由留存）+ 灰度 + **管理员审阅放行流** | 一条 skill 走完"采集→生成→评估→**人工审**→签名→灰度→全网"闭环，全程记录可复查 |
| **P4 运营化** | Client 自更新、多租户 RBAC、审计报表、**自动发布策略（数据达标后）** | 生产可用 |

P2 动工前置条件：`docs/AGENT-PROTOCOL.md`（内部 stdio JSON-lines 协议规范，以 claude `stream-json` 为基型、吸收 ACP 会话/权限语义）评审通过。

## 12. 开放问题（需产品决策）

1. **首批接入机器数量**：若个位数，P1 期间可并行用一个 git 同步脚本过渡（拿到七成价值），正式通道按本方案推进；
2. **默认 permission_mode**：建议 `readonly` 起步，观察一段时间再放宽；
3. **多用户机器**：v1 按"1 OS 用户 = 1 device"实现，同机多用户即多 device（labels 上打 `host:<hostname>` 归组）；
4. **`require_confirm` 的交互形态**：桌面通知 / CLI / 轮询，P2 设计时定。
