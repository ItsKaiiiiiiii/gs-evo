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
| C10 | 传输阈值调整：内嵌 base64 上限 256KB，其余一律走一次性票据 URL（云端自签发） | WS 帧过中间代理常限 1MB；4MB base64 膨胀 33% 易翻车 |
| C11 | 设备模型明确为 **1 OS 用户 = 1 device**；任务默认不跨 Client 重启存活（v1 明示） | skill 在用户 home 下；隐藏行为必须写成显式约定 |
| C12 | 数据模型新增 `EvaluationRun` / `BenchmarkSet` / `DeviceSkillInstall`，`Command` 补 `cmd_seq`/`acked_at`/`created_by` | 下发覆盖率、回滚决策、审计需要这些事实 |
| C13 | 新增 §4「语言与技术栈」：client/cloud 均为 Go，协议用 JSON 不上 protobuf，依赖清单与"明确不引入"清单定稿 | 语言技术栈此前未成文 |
| C14 | 新增 §6.7「鉴权与凭证」：参考 multica 定稿凭证体系（前缀化 token / Bearer 握手鉴权 / SHA-256 哈希落库 / 原地续期 / 吊销踢线 / enroll 注册流 / 防抢注绑定） | 鉴权逻辑此前只有一句话，未成文 |
| C15 | 存储选型变更：数据库 MySQL 8（原 PostgreSQL）；bundle 存**本地磁盘**（原 S3 兼容对象存储/MinIO）；"预签名 URL"相应改为**云端自签发一次性票据 URL**，协议形状不变 | 部署面最小化：一个 MySQL + 一个磁盘目录即可跑；迁移对象存储只需换实现，不动协议 |

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
                          │        Bundle Store（本地磁盘，内容寻址，           │
                          │          一次性票据 URL 读写）                     │
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
| D1 | **单一 WSS 长连接**承载心跳、指令、任务事件、skill 通知；大文件走 HTTPS 一次性票据 URL（云端本地存储） | 免轮询、免多协议；NAT/内网友好；大文件不走 WS 帧避免代理限制 |
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
| D15 | **机器侧信任三件套**：本地审计日志（audit.log 只追加）+ login 明示数据边界确认 + pause 哨兵（暂停期间拒绝一切动作类指令，心跳保持）。对被接入机器的使用者：知情、同意、可暂停——不是文档承诺，是代码强制 | 产品的可信度决定采用率；管理员的便利不能凌驾于机器使用者的知情权 |
| D14 | **运行时（Runtime）一等实体**：本机 agent 实例（type+version+path）由 worker 本地发现并注册上报；任务可按 runtime_id 精确指派；"云端创建运行时"收敛为受审计的 `runtime.register` 指令（worker 侧校验后登记）。参考 multica runtime 概念，采纳其"本地发现注册 + 云端指派"，**不采纳**云端直接创建（方向相反，会重开远程写机器的门） | 多 agent 机器需要精确指派；评估结果需绑定执行环境；agent 上下线需可视 |

## 4. 语言与技术栈

**总原则**：能用 stdlib 不引库；client 与 cloud 同语言；每个依赖有明确理由；被 multica 验证过的方案优先（同栈可直接移植经验）。

### 4.1 语言：Go（client 与 cloud 同栈）

1. Client 需要**单文件二进制 + 三平台交叉编译**（Windows/macOS/Linux × amd64/arm64）+ 开机自启注册，Go 是这个场景的最优解；
2. **multica 同为 Go**（server 为 Go module），其 daemon 生命周期、autostart 注册、WS 重连、进程树管理的工程经验可直接移植；
3. Cloud 初期单体，Go 的部署形态 = 一个二进制 + MySQL + 本地磁盘目录，运维面最小。

备选否决理由：Rust（安全性更好但迭代效率不适合 P0-P2 快速验证期）；TypeScript/Node（client 打包单文件困难，后台守护进程体验差）。

### 4.2 模块布局（monorepo）

```
gs-evo/
├── client/        # gs-agent-worker（Go module）
├── cloud/         # 云端单体服务（Go module）
├── pkg/
│   ├── proto/     # 协议消息类型 + 指令类型常量（两侧共享，单一事实）
│   └── skillpkg/  # 共享工具：tar+gzip 打包、manifest 解析、ed25519 签名/验签
├── deploy/        # docker-compose（cloud + mysql）、systemd/LaunchAgent 模板
└── docs/
```

### 4.3 协议序列化：JSON，不上 protobuf

- 两侧同语言，共享 `pkg/proto` 结构体即可，无跨语言序列化需求；
- 消息为低频控制面（指令/事件），非性能敏感数据面；JSON 文本帧**人可直接读**——审计与排障价值高（`Command.payload` 直接落 MySQL JSON 列）；
- `proto_version` 预留演进；未来若 client 需出 Rust 版，再抽 schema 并生成，届时协议已稳定，成本可控。

### 4.4 Client 依赖清单

| 用途 | 选型 | 说明 |
|------|------|------|
| CLI 子命令 | `spf13/cobra` | login / status / logs / skills / autostart 多子命令，事实标准 |
| WebSocket | `gorilla/websocket` | 与 cloud 同库；**重连/退避/心跳自己写**（核心路径，逻辑简单且必须自持） |
| 配置 | `BurntSushi/toml` | config.toml 读写 |
| 日志 | `log/slog`（stdlib）+ `natefinch/lumberjack` | 结构化日志 + 文件轮转 |
| 签名验签 | `crypto/ed25519`（stdlib） | D8 |
| 打包 | `archive/tar` + `compress/gzip`（stdlib） | 内容寻址 bundle |
| Windows 进程树 | `golang.org/x/sys/windows`（Job Object） | 平台文件用 `_windows.go` 后缀组织（multica 同款） |
| 自更新（P4） | 自写（download → 验签 → rename swap） | ~200 行且必须接验签链，不引第三方自更新库 |

### 4.5 Cloud 依赖清单

| 用途 | 选型 | 说明 |
|------|------|------|
| HTTP 路由 | stdlib `net/http`（Go 1.22+ pattern routing） | 端点少，不引框架 |
| WebSocket | `gorilla/websocket` | 与 client 同库 |
| 数据库 | MySQL 8.0+ | 唯一事实存储；JSON 列存 payload/manifest，UUID 用 BINARY(16) |
| DB 访问 | `sqlc`（MySQL engine）+ `golang-migrate` | 类型安全、SQL 即文档、编号版 up/down 迁移文件（模式同 multica，引擎换 MySQL） |
| bundle 存储 | **本地磁盘**（内容寻址：`<data_dir>/bundles/<aa>/<sha256>.tar.gz`） | 零外部依赖；读写经云端签发的一次性票据 URL；备份靠磁盘快照/rsync |
| 后台任务 | 同进程 goroutine + DB 队列表 | 初期不引 asynq/river/NATS——Evolution Engine 是异步 job，不是高吞吐队列 |
| LLM 调用 | `anthropic-sdk-go`（模型端点可配） | Evolution Engine 的生成与评审 |
| 管理界面 | P0-P2 用 `cloud ctl` CLI 子命令；Web 控制台 P3 后按需立项 | 初期不建前端 |

### 4.6 明确不引入（及替代方案）

| 不引入 | 替代 |
|--------|------|
| 微服务框架 / 服务网格 | 单体服务（§5.2 初期约束） |
| Kafka / NATS / RabbitMQ | WSS 通道 + DB 队列表 |
| Kubernetes | docker compose（自托管）；云上托管单机起步 |
| ORM（GORM/ent） | sqlc 手写 SQL |
| protobuf / gRPC | JSON over WSS（§4.3） |
| 前端框架（初期） | CLI + 极简静态页；P3 后再定 |

### 4.7 版本与工程化

- Go ≥ 1.23；
- 构建：`goreleaser` 交叉编译 client 多平台产物（P1 引入）；cloud 普通 `go build`；
- CI：GitHub Actions——PR 跑 `gofmt`/`golangci-lint`/`go test`；main 分支出 docker 镜像；
- 测试：单元测试（pkg 层全覆盖：proto、skillpkg、幂等缓存）；e2e 用 docker compose 起 cloud + 2 个 client 跑关键流程（P1 起：collect→install 全链路；P2 起：task 下发+断线补传）。

## 5. 组件设计

### 5.1 Client（gs-agent-worker，每 OS 用户一个后台进程）

单二进制 Go 程序（语言与依赖选型见 §4.1/§4.4）。两种形态：`worker daemon`（后台服务）与 `worker ctl`（本机 CLI：login / status / logs / skills list / skills rollback）。

| 模块 | 职责 |
|------|------|
| **Connection Manager** | 维护 WSS；指数退避重连（1s→30s + 抖动）；心跳 30s（优先 WS 协议层 ping，应用层 ping 兜底）；鉴权：WS 握手 `Authorization: Bearer <device token>`（见 §6.7），成功后发 `hello`（`proto_version` + `capabilities` + `last_acked_cmd_seq`）；token 剩余 < 7 天自动续期 |
| **Command Dispatcher** | 按 `type` 路由指令到处理器；**按 `cmd_seq` 逐条 ack**；**幂等缓存**（最近 128 条 `cmd_id → result` 的 LRU，重复指令直接回缓存不重复执行）；本地并发上限（默认 2）；过期指令（`expires_at`）直接回 expired 不执行 |
| **Agent Adapter** | 启动时探测本机 agent（路径、版本、可用性）；运行期 spawn 子进程，stdin/stdout 按行交换 JSON（协议见 P2 spec）；**权限模式映射**（把 `task.start.max_permission` 翻译为各 agent 的无头权限参数）；超时与看门狗；**进程树终止**：POSIX 进程组 / **Windows Job Object** |
| **Task Runner** | 每任务隔离工作目录 `tasks/<task_id>/`；全量 stdout/stderr 落盘本地；流式转 `task.event` 上报（单帧上限 64KB，有界缓冲 2000 条，超出只传引用）；任务结束后**结果先写 spool 再发送**，成功送达后删除 |
| **Skill Manager** | 扫描白名单目录生成 manifest；打包（tar+gzip→sha256）；下载**验签 + sha256 校验**后**冲突检测**（磁盘 hash vs `state.json` 记录，本地被改动 → 默认 `skip_and_report`）→ 解压临时目录 → **原子安装** → 旧版移入 `<root>.prev`（保留一代，支持本地回滚） |
| **Result Spool** | `spool/` 目录：所有未确认送达的 `cmd_result` / 任务最终结果落盘，重连后优先补传 |
| **Local State** | `~/.gs-agent-worker/`：config.toml、日志、state.json、cache、tasks、spool |

**Client 刻意不含任何策略**：不决定何时进化、不决定装到哪台机器、不解析任务优先级。**但 Client 拥有两条不可协商的本地主权**：目录白名单的修改权、`require_confirm` 开关。

### 5.2 Cloud（初期单体服务 + MySQL + 本地磁盘存储）

| 模块 | 职责 |
|------|------|
| **Gateway** | WSS 接入；token 鉴权；协议版本协商（`proto_version` 不匹配即断并提示升级）；连接-设备绑定；`cmd_seq` 分配与补发队列（重连后重发未 ack 且未过期指令）；消息路由 |
| **Device Registry** | 设备档案：id、主机名、OS/arch、worker 版本、agent 运行时、labels（圈定分发范围）、`last_acked_cmd_seq`、心跳/在线状态 |
| **Skill Registry** | skill 版本库：manifest + 内容包（内容寻址）；**stable 版本签名服务**（ed25519）；**状态机执行**（draft→candidate→reviewed→stable→deprecated，stable 转正 v1 一律管理员显式操作）；检索与比对 |
| **Task Manager / Queue** | 任务创建（记录 `created_by`）、按设备排队、推送 `task.start`、汇聚过程事件与最终结果、断线重放事件 |
| **Evolution Engine** | 自进化闭环（离线/异步）：消费清单与任务反馈 → 生成候选版本（带 diff 与理由）→ **产出 candidate（不是 stable）** → 灰度设备跑 EvaluationRun → 评审报告给管理员 → 人工放行后签名转 stable |
| **Console/API** | 管理界面（初期 `cloud ctl` CLI，见 §4.5）：设备管理、skill 浏览与 diff 审阅、任务下发与审批、指令审计查询 |

**初期实现约束**：单体服务 + 一个 MySQL + 一个本地存储目录；Evolution Engine 是同进程异步 job。**先不分微服务。**

## 6. 通信协议

### 6.1 通道与心跳

- 传输：`wss://cloud/api/v1/agent-ws`，JSON 文本帧；大文件走 `https://cloud/api/v1/bundles/*`（云端自签发的**一次性票据 URL**，TTL 10 分钟、PUT/GET 各消费一次即作废，由云端服务自己鉴权自己落盘）；
- 鉴权：WS upgrade 请求头 `Authorization: Bearer <device token>`，服务端在 **upgrade 前**完成校验，失败以 401/404 JSON 拒绝、连接不建立（multica 同款，优于"首条消息带 token"：未鉴权连接根本不存在）；详见 §6.7；
- 心跳：WS 协议层 ping/pong 优先，应用层 `ping`（30s）兜底；云端 **90s** 无任何帧判定离线（3 倍容忍）；
- 重连：指数退避（1s→30s）+ 抖动；重连成功即重新 `hello`，云端按 `last_acked_cmd_seq` **补发未 ack 指令**，Client 补传 spool 中未送达结果。

### 6.2 可靠投递：at-least-once + 幂等

- 云端为每台设备维护单调递增 `cmd_seq`，每条指令携带 `{cmd_id, cmd_seq, expires_at?}`；
- Client 处理完成后回 `cmd_result`，云端据此置 `acked_at`；
- 连接断开期间下发的指令在重连后**补发**；Client 靠 `cmd_id` 幂等缓存保证**不重复执行**（`task.start`、`skills.install` 重复执行后果严重，这是硬要求）；
- 指令默认有效期（如 10 分钟），过期未送达置 `expired`，避免陈旧指令迟到执行。

### 6.3 统一指令模型

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
| `runtime.register` | `{type, path, args?, env?}` | 登记本机运行时（受审计创建：worker 校验二进制存在并探测版本后才持久化） |
| `client.update` | `{version, url, sha256, signature}` |（预留）Client 自更新 |

客户端 → 云端其余消息：

| type | 作用 |
|------|------|
| `hello` | 鉴权 + 协商 + 对账（详见下） |
| `ping` / `pong` | 应用层心跳兜底 |
| `task.event` | `{task_id, seq, kind, data}`：stdout/stderr 增量、状态迁移、产物引用（超限内容只传引用） |
| `ack` | `{cmd_seq}`：逐条确认收到（可并入 cmd_result，视实现合并） |

### 6.4 hello 与对账

```json
{
  "type": "hello",
  "proto_version": 1,
  "device": { "hostname": "…", "os": "darwin", "arch": "arm64", "worker_version": "0.3.0" },
  "agents": [ { "type": "claude-code", "version": "2.x", "path": "…", "ok": true } ],
  "runtimes": [ { "runtime_id": "rt-…", "type": "gsagent", "version": "1", "path": "…", "ok": true } ],
  "skills": [ { "name": "gerrit-review", "version": "3", "content_hash": "sha256:1f0c…", "local_state": "clean" } ],
  "last_acked_cmd_seq": 136
}
```

要点：`proto_version` 不匹配云端直接断连并在错误中给出最低支持版本；`skills` 上报**完整列表**（数量级很小，不用摘要，无碰撞歧义），`local_state: clean|modified` 由磁盘 hash 与 `state.json` 比对得出——云端据此能发现"本地有未上报的改动"，可发起 collect 收走。

### 6.5 Skill Manifest

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

### 6.6 内容传输

- **< 256KB**：base64 内嵌 `cmd_result.data.bundle`；
- **≥ 256KB**：`skills.collect` 返回 `{upload_url}`（一次性 PUT 票据 URL），Client 直传云端，落 `bundles/<aa>/<sha256>.tar.gz`；`skills.install` 同理给一次性下载票据 URL；
- 票据 URL 由云端服务自签发自校验（绑 `device_id` + `sha256` + 动作 + TTL）——**协议形状与对象存储方案的预签名 URL 一致**，未来若迁移到 S3/OSS 只换实现，协议与 Client 不变；
- 一律附 sha256；**stable 版本另附 ed25519 签名（对 sha256 值签名）**，Client 内置公钥，验签失败拒装并告警。

### 6.7 鉴权与凭证（参考 multica）

> 本节参考 multica（multica-ai/multica，Go）已验证的 daemon↔cloud 鉴权实现（按 token 前缀分发的鉴权中间件、Bearer 握手、哈希落库、原地续期、owner 绑定防抢注），结合 gs-evo 更简单的信任模型（v1 单管理员、无 workspace）裁剪而成。

**凭证体系**（按前缀区分；统一走 `Authorization: Bearer` 头，服务端中间件按前缀分发校验路径——multica `daemon_auth.go` 同款模式）：

| 前缀 | 名称 | 使用者 | 生命周期 | 说明 |
|------|------|--------|----------|------|
| `gsd_` | **device token** | Client 全部云端通信（WSS 握手、换取 bundle URL、续期） | 90 天，原地续期 | 每设备一枚，`Device.token_hash` 唯一 |
| `gse_` | **enroll token** | 新设备注册，换取 device token | 一次性 + 24h TTL | 管理员在 console 生成并分发 |
| `gsa_` | **admin token** | `cloud ctl`（创建指令 / 审批 / 签名） | 长效，可吊销 | D11"指令创建者鉴权"的载体，`Command.created_by` 记录 |
| （bundle 票据 URL） | bundle 上传/下载 | Client | 一次性，TTL 10 分钟 | Client 先凭 device token 换取（云端本地存储的自签发 URL）；bundle 端点不认长凭证 |

要点：全部为**不透明随机 token**（20 字节 `crypto/rand` + 可读前缀，multica 同款），**不用 JWT**——无状态验证意味着无法即时吊销，而吊销能力对本系统是硬需求；服务端**只存 SHA-256 哈希 + 唯一索引**，明文仅在签发响应中出现一次。

**注册（enroll）流程**（multica headless `login --token` 同款；浏览器 OAuth 配对流随 console 用户体系放 P4）：

```
管理员 console:  cloud ctl enroll create --label "办公室台式机"   → gse_…（24h 有效）
新机器:          worker ctl login --enroll gse_…（或交互粘贴，不进 shell history）
  1. 本地生成 device.id（UUIDv7，存 ~/.gs-agent-worker/device.id，0600，
     与 token 分离存放——multica identity.go 同款）
  2. POST /api/v1/devices/enroll {enroll_token, device_id, hostname, os, arch, worker_version}
  3. 云端：校验 enroll token → 创建 Device{owner=管理员} → 签发 device token
     （响应体唯一一次返回明文）→ enroll token 立即作废
  4. Client：写入 config.toml（chmod 0600 + 临时文件原子 rename，multica config.go 同款）
```

**防抢注**（借 multica MUL-8154 的教训）：device token 签发即绑定 `Device.owner`。同一 `device_id` 再次 enroll 时旧 token 立即作废、新 token 归属本次 enroll 的所有者——v1 仅管理员能生成 enroll token，天然无抢注面；**P4 多租户后规则为"非 owner 不得对已存在的 device_id 重新 enroll"，拒绝统一回 404（防枚举探测，multica 同款）**。

**连接鉴权**：

- WS upgrade 请求头 `Authorization: Bearer gsd_…`；服务端**在 upgrade 前**校验：SHA-256 → 查缓存/库 → 存在、未过期、设备未吊销 → 通过后把 `device_id` 绑定为**连接级 context**，连接生命周期内不再逐帧鉴权（multica 升级时批量授权 + lease 播种同思路）；
- 失败码：无效/过期 → **401**；设备存在但不属于你（P4 多租户）→ **404**（防探测）；
- WS 内每帧的归属校验（如 `task.event.task_id` 必须属于本 device）复用连接绑定的 device context——multica `daemon_rpc.go` "握手凭证回放进 context" 同思路；
- `worker daemon` 只从 config.toml 读 token，**刻意不读环境变量**（防 agent 子进程伪造凭证，multica `requireDaemonAuth` 同款）；
- 服务端剥离客户端可伪造的服务端语义头（multica 剥 `X-Actor-Source` 等，gs-evo 如有同类头一并剥离）。

**校验路径与缓存**（v1 无 Redis，multica 多级缓存简化为进程内 LRU）：

- 键：`sha256(token)` → 值 `{device_id, owner, expires_at}`；缓存 TTL **钳制到 token 剩余寿命**（multica `TTLForExpiry` 同款，防"缓存活得比 token 久"）；
- miss 走 SQL（查询自带 `expires_at > now()`）后回填；缓存故障降级直查 DB——**鉴权 fail-closed，缓存 fail-open**。

**生命周期**：

- device token 90 天；剩余 < 7 天时 `POST /api/v1/devices/token/renew`（Bearer 鉴权），CAS 延长到 now+90 天。**原地续期、不轮换值**——multica 同款理由：daemon 与 `worker ctl` 多进程共享同一 config.toml，换值会互相踩；
- 续期节奏：daemon 每 24h 一次 + 启动 preflight 同步续期（multica 为 3 天间隔；gs-evo 指令面更敏感，取 24h）；
- **401 处理**（multica 同款）：daemon 不退出、不清配置，日志持续 WARN "run `worker ctl login`" + 退避重连；鉴权不过自然收不到任何指令，无额外风险面；
- **吊销**（有意比 multica 更强）：multica 吊销后不断开在线连接，靠"不再分配工作"兜底——因为它的 daemon 断了也只是收不到任务。gs-evo 的设备还能执行指令/装 skill，所以 **Gateway 维护 device_id → 连接映射，吊销事务提交后立即主动关闭该连接并拒绝重连**（§9"吊销"行的落地机制）。

**重连对账**：鉴权与首次连接**完全相同，无会话恢复**（不做 resume ticket——multica 同款，省一类状态机）；重连 = 全新 Bearer 握手 + hello 对账（补发未 ack 指令、补传 spool，见 §7.1）。设备档案被删（404）→ 本地提示重新 enroll（multica "runtime 消失 → 重新注册" 同款）。

**不沿用 multica 的部分**：

| multica 机制 | gs-evo 取舍 | 原因 |
|--------------|-------------|------|
| workspace/成员体系（membership、批量吊销事务） | 不引入，P4 多租户时再扩展 | v1 单管理员 |
| `mat_` task token 注入 agent 进程 | 不需要 | gs-evo 的 agent 进程不与云端通信（D12），不存在"owner 凭证泄露给 agent 进程"的面 |
| 浏览器 OAuth login（本地回调端口 + state 防 CSRF） | P4 随 console 用户体系引入 | v1 headless enroll 流已覆盖 |
| Redis 缓存 | 进程内 LRU | v1 规模不需要 |

### 6.8 运行时（Runtime，D14）

**概念**：runtime = 本机一个可被寻址的 agent 实例（type + version + path），参考 multica 的 runtime 概念。与 multica 一致采纳**本地发现注册**（worker 探测 → hello/`runtime.sync` 上报 → 云端 runtime 表），**不采纳**"云端直接创建"（方向相反，等于远程写用户机器）。

- **身份**：`runtime_id = derive(type + path)`——重装/重启稳定；同类型多实例（不同路径）各自成体；
- **同步**：hello 携带全量 + 每 5 分钟 `runtime.sync` 周期上报，云端 runtime 表 upsert；
- **指派**：`task.start` 可带 `runtime_id`（优先于 `agent_type`），任务记录执行所用 runtime——评估结果绑定执行环境；
- **创建**：`runtime.register` 指令（审计页可见 created_by）——worker 校验二进制存在并探测版本后才持久化到 state.json（source=registered，含 args/env）；
- **控制台产品形态**（multica 思路）：指派粒度 = 运行时而非设备。「任务与操作」页顶部为运行时选择器（`主机名 · 类型 版本 [标签]`，来自全局 `GET /admin/runtimes`），扫描/获取/任务均指派到所选运行时所在机器；「设备」详情的运行时区块提供「添加运行时」（创建即绑定该机器，走 runtime.register 指令）；
- **前端连锁（坑）**：`runtime_id` 非全局唯一 → 控制台运行时选择器的选项 value 必须用复合键 `device_id|runtime_id`（当全局唯一值会让多设备场景"选 B 机发到 A 机"且无感知）；任务创建端点同时校验 runtime 归属（不属于该设备 400 拒绝）。用户操作手册见 [CONSOLE.md](CONSOLE.md)。
- **产品分期**：一期导航 6 页（总览/设备/运行时/Skill/进化/审计），任务模块架构预留（导航隐藏，`#tasks` 直达）；运行时选择未来作为任务下发流程内的步骤呈现，不再是独立选项。

## 7. 关键流程

### 7.1 上线注册与对账

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

### 7.2 Skill 上行收集

```
Cloud                                          Client
  │── cmd skills.scan ──────────────────────────▶│  扫描白名单目录（含 modified 检测）
  │◀─ cmd_result {manifests[]} ─────────────────│
  │   （对比 content_hash，仅对新增/变更发起 collect）│
  │── cmd skills.collect {name, root} ──────────▶│  tar+gzip → sha256
  │◀─ {bundle(<256KB) | upload_url} ────────────│  上传内容包
  │   落 SkillVer {source: collected, status: draft}，记录 origin_device
```

### 7.3 Skill 下发安装（含冲突与回滚）

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
- **target_root 语义**：§6.5 manifest.root = skill 自身目录。管理面 install 接口传**白名单根目录**，云端签发指令时拼接 `<target_root>/<skill_name>`，Client 仍按白名单（等于根或其子目录）校验；
- **Windows**：rename 覆盖遇被锁文件 → 退避重试（3 次）→ 失败回报，不部分覆盖。

### 7.4 任务实时下发

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

### 7.5 Skill 自进化闭环

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

## 8. 数据模型（云端）

```
Device      {id, owner, token_hash, token_expires_at, hostname, os, arch,
             worker_version, labels[], last_seen, status, last_acked_cmd_seq}
EnrollToken {token_hash, label, created_by, expires_at, used_at}

Skill       {id, name, latest_stable, latest_any, agent_compat[]}
SkillVer    {id, skill_id, version, manifest, content_sha256, signature,
             origin_device, source: collected|evolved|forked, parent_version?,
             status: draft|candidate|reviewed|stable|deprecated, created_at}

BenchmarkSet  {id, name, version, task_cases_ref, updated_by, updated_at}
EvaluationRun {id, skill_ver_id, benchmark_set_id, device_ids[], started_at,
               finished_at, metrics, judge_outputs_ref, verdict, notes}

DeviceSkillInstall {id, device_id, skill_ver_id, installed_at,
                    result: ok|conflict_skipped|failed, prev_ver_id}

Runtime     {id, device_id, type, version, path, ok, source: probed|registered,
             last_seen}                                       ← 本机 agent 实例（D14）
Task        {id, device_id, prompt, agent_type, runtime_id, max_permission,
             status, created_at, created_by, events_ref, result_ref}
Command     {id, cmd_seq, device_id, cmd_type, payload, created_by,
             status: pending|sent|acked|expired, issued_at, acked_at, result_at}
```

要点：
- `Command` 全量落库 + `created_by`——审计与排障的单一事实来源，**也是"谁有权创建指令"的追责依据**（D11）；
- `SkillVer.source` + `parent_version` 构成进化谱系；`status` 是发布闸门的物理载体；
- `DeviceSkillInstall` 回答"哪个版本覆盖到哪台机器、结果如何"——灰度决策与回滚范围的依据；
- `EvaluationRun` 留存 LLM-judge 原始输出——评估结论必须可复查、可迭代。

## 9. 安全

**诚实的前提**：`task.start` 把 prompt 交给本机有 shell 工具的 coding agent，**实质上等于云端可间接执行任意命令**。因此安全模型的核心不是"指令封闭枚举"（那只是收窄接口），而是下面四层：

| 层 | 措施 |
|----|------|
| 传输 | 强制 WSS/HTTPS（TLS） |
| 身份 | 凭证体系与握手鉴权见 §6.7：前缀化 device/admin/enroll token（不透明随机值，非 JWT）、`Authorization: Bearer` 握手、SHA-256 哈希落库、90 天原地续期、**吊销即踢线**；云端**指令创建者鉴权**（v1 单管理员 admin token，多租户 RBAC 在 P4） |
| 指令面 | 封闭枚举 + 结构化 payload；`Command` 全量审计（含创建者）；指令带有效期；**at-least-once + Client 幂等**防重放副作用 |
| 执行面 | `task.start` 的 `max_permission` 与本地 permission_mode 配置**取更严者**（建议默认 `readonly` 起步）；本地 `require_confirm` 开关（高危机器可开：任务到达先本机确认）；进程树隔离 + 看门狗超时；敏感 env 不透传，agent 凭证用本机已有配置（D12） |
| 文件面 | skill 读写仅限本地白名单目录，**白名单主权在本地，云端 policies 只能收紧**（D10）；安装走临时目录 + 原子 rename + `.prev` 保留 |
| 内容 | 包 sha256 校验；**stable 版本 ed25519 签名，Client 内置公钥验签，v1 即启用**（D8）；进化产物强制人工审后才可签名 |
| 吊销 | 云端吊销 token 后 Gateway 立即断连并拒绝重连 |

## 10. 本地布局（Client）

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

## 11. 可扩展性

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

## 12. 实施路线

| 阶段 | 范围 | 验收标准 |
|------|------|----------|
| **P0 骨架**（1-2 周） | Client：**enroll 注册 + Bearer 握手鉴权** + 连接+心跳+hello（proto_version/capabilities/last_acked_cmd_seq）+ 指令分发框架 + **cmd_seq ack + 幂等缓存 + spool 骨架**；Cloud：Gateway（鉴权中间件 + 吊销踢线）+ Device Registry + EnrollToken + Command 审计落库；`agent.probe` | 控制台可见设备在线/离线切换；**拔网线重连后指令补发且不重复执行**；**吊销 token 后在线设备立即被踢且无法重连** |
| **P1 Skill 通道**（2-3 周） | `skills.scan/collect/install` + Skill Registry + 内容寻址 + **ed25519 签名/验签** + **版本状态机** + **冲突检测（skip_and_report）** + `.prev` 回滚 | A 机采集 → 云端人工转 stable 签名 → 装到 B 机；**本地改动被跳过并上报**；`rollback` 秒回上一版 |
| **P2 任务通道**（3 周） | **Agent 协议 spec（先行文档）** → AgentAdapter（stdio JSON-lines + 权限模式映射）+ Task Runner + `task.start/cancel/events_replay` + 事件流 + Windows Job Object | 云端下发 prompt 实时回传；**断网重连后事件补齐、结果不丢（spool）**；cancel 在三平台都能杀干净进程树 |
| **P3 自进化（人工闭环）**（持续） | BenchmarkSet + EvaluationRun + 生成候选（diff/理由留存）+ 灰度 + **管理员审阅放行流** | 一条 skill 走完"采集→生成→评估→**人工审**→签名→灰度→全网"闭环，全程记录可复查 |
| **P4 运营化** | Client 自更新、多租户 RBAC、审计报表、**自动发布策略（数据达标后）** | 生产可用 |

P2 动工前置条件：`docs/AGENT-PROTOCOL.md`（内部 stdio JSON-lines 协议规范，以 claude `stream-json` 为基型、吸收 ACP 会话/权限语义）评审通过。

## 13. 开放问题（需产品决策）

1. **首批接入机器数量**：若个位数，P1 期间可并行用一个 git 同步脚本过渡（拿到七成价值），正式通道按本方案推进；
2. **默认 permission_mode**：建议 `readonly` 起步，观察一段时间再放宽；
3. **多用户机器**：v1 按"1 OS 用户 = 1 device"实现，同机多用户即多 device（labels 上打 `host:<hostname>` 归组）；
4. **`require_confirm` 的交互形态**：桌面通知 / CLI / 轮询，P2 设计时定。
