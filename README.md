# gs-evo

客户端-云端的 coding agent **Skill 收集 · 进化 · 分发系统**。

各台电脑上的 coding agent 及其 skill 相互隔离。gs-evo 通过每台机器上的常驻 Client（`gs-agent-worker`）与云端保持单条 WSS 长连接，实现：

- **收集**：云端远程扫描、收集各机器上的 skill（含能力描述 manifest）；
- **进化**：云端集中管理 skill 版本，LLM 生成改进候选 → 基准评估 → 人工审阅 → 签名发布；
- **分发**：把审批后的 skill 版本安全下发安装/更新到指定机器的指定目录；
- **驱动**：云端实时下发任务，Client 通过 stdio 驱动本机 agent 执行，过程与结果实时回传。

## 设计原则

1. **一条 WSS 长连接**承载心跳、指令、事件、结果；大文件走 HTTPS 一次性票据 URL（云端本地存储）；
2. **统一指令模型**（`cmd → cmd_result`，at-least-once + Client 幂等）覆盖所有云端能力；
3. **Client 是执行器、云端是决策者**——但 Client 持有三道不可协商的本地保险：**验签、目录白名单主权、幂等**。

## 技术栈

- **语言**：Go（client 与 cloud 同栈；multica 同款，其 daemon 生命周期 / autostart / 重连经验可直接移植）
- **协议**：JSON over WSS（不上 protobuf），消息类型共享于 `pkg/proto`
- **存储**：MySQL 8（sqlc + golang-migrate）+ 本地磁盘内容寻址存储（bundle 经云端一次性票据 URL 读写）
- **部署**：云端单体服务 + docker compose；Client 单二进制 + 系统自启（LaunchAgent / systemd user unit / Windows 计划任务）

详见 [DESIGN.md §4](docs/DESIGN.md)。

## 文档

| 文档 | 内容 |
|------|------|
| [docs/DESIGN.md](docs/DESIGN.md) | 设计方案 v2：架构、技术栈、协议与鉴权、安全模型、数据模型、实施路线 |

## 仓库规划

```
gs-evo/
├── docs/
│   ├── DESIGN.md           # 设计方案（v2，当前）
│   └── AGENT-PROTOCOL.md   # （P2 前）内部 Agent stdio JSON-lines 协议规范
├── client/                 # gs-agent-worker（Go）—— P0 起建
├── cloud/                  # 云端单体服务（Go）—— P0 起建
├── pkg/                    # proto（协议类型）/ skillpkg（打包·签名·manifest）
└── deploy/                 # docker-compose、systemd/LaunchAgent 模板
```

## 路线图

| 阶段 | 交付 |
|------|------|
| P0 骨架 | enroll 注册 + 鉴权 + 连接 + 心跳 + 指令分发（seq/ack/幂等）+ 设备表 + `agent.probe` |
| P1 Skill 通道 | scan / collect / install + 内容寻址 + **签名** + 版本状态机 + 冲突检测 + 回滚 |
| P2 任务通道 | AgentAdapter（stdio）+ Task Runner + 任务事件流 + 断线补传 |
| P3 自进化 | 基准评估 + 候选生成 + 人工审阅放行 + 灰度分发闭环 |
| P4 运营化 | 自更新、多租户 RBAC、自动发布策略 |

详细验收标准见 [DESIGN.md §12](docs/DESIGN.md)。

## Web 控制台

gs-cloud 内嵌轻量控制台（纯前端，复用 admin API 与 Bearer 鉴权），浏览器打开：

```
http://<云端地址>/console/
```

- 登录：粘贴 admin token（gsa_ 开头；gs-cloud 首启生成，或 `gs-cloud token create` 签发，viewer 角色自动只读）
- 页面（实体轴 IA，总览为着陆页）：**总览**（健康+客户端升级入口）· **设备**（实体详情：运行时/装机清单/指令历史/任务历史/标签/吊销，详情内可**添加运行时**）· **Skill**（实体详情：版本时间线=diff+rationale+评估历史+自动策略+覆盖率，生成/评估/安装动作内联）· **任务与操作**（**运行时中心**：选择运行时 → 一键扫描/获取 skill/探测 agent；自定义任务按运行时精确指派；任务列表/事件流/取消）· **进化**（全局队列：BenchmarkSet + EvaluationRun）· **审计**（指令全量历史+操作者+token 管理）
- **操作手册**：[docs/CONSOLE.md](docs/CONSOLE.md)——各页面能做什么、常见疑问、页面操作与 CLI 对照表
- 扩展：页面即模块——`cloud/console/static/js/pages/` 下加一个 js 文件并 `App.register(...)` 即成新页面，后端零改动

## 运行时（Runtime）：agent 实例一等实体

参考 multica 的 runtime 概念（采纳"本地发现注册 + 云端指派"，不采纳云端直接创建）。每台设备上的 agent 实例（type+version+path）是一个可寻址的运行时：

```
gs-cloud runtime list --device ID                                  # 查看设备运行时（hello/周期自动上报）
gs-cloud runtime register --device ID --type gsagent --path ./my-agent --arg X --env K=V
                                                                    # 受审计创建：worker 校验二进制并探测版本后登记
gs-cloud task create --device ID --prompt P --runtime <runtime_id>  # 任务精确指派到某个运行时
```

**控制台操作**（任务与操作页，multica 产品思路——指派粒度 = 运行时，天然绑定机器）：
- 顶部「选择运行时」下拉列出所有在线设备上的可用 agent 实例（`主机名 · 类型 版本 [标签]`）
- 扫描 skill / 获取全部 skill / 探测 agent：一键下发到所选运行时所在机器，回执自动展示
- 「设备」详情页 → 运行时区块 → 「添加运行时」：创建即绑定该机器（云端下发 runtime.register 指令，本机校验二进制并探测版本后登记）

- **身份**：`runtime_id = derive(type+path)`，单机内稳定（重装/重启不变）
- **上报**：hello 携带全量 + 每 5 分钟 `runtime.sync` 周期同步；控制台「设备」详情可见
- **指派**：`task.start` 带 `runtime_id` 时精确到实例（多 agent 机器必备）；任务记录执行所用运行时
- **安全**：`runtime.register` 是云端"创建"运行时的唯一入口——worker 校验二进制存在并探测版本后才登记，全程审计（created_by）
- **表设计坑**：`runtime_id` 仅单机内唯一，表主键必须 `(device_id, id)` 复合，否则相同机器互相覆盖

## Worker 自更新（无人上机操作）

```
# 云端：发布（按平台）+ 一键下发
gs-cloud client-update push --version v0.2.0 --file ./gs-worker --os linux --arch amd64
gs-cloud client-update rollout --version v0.2.0 --label env:all   # 按设备平台自动匹配 release
```

worker 收到指令后自动完成：下载（一次性票据 URL）→ sha256 校验 → ed25519 验签 → 原子替换自身（`.old` 留一代可回退）→ **自动重启进新版本**（已注册自启则交服务管理器拉起；POSIX 未注册则原地 exec 接管）→ 重连云端并上报新版本。全程无需登录用户机器。

建议 worker 侧执行一次 `gs-worker autostart enable`（systemd user unit / LaunchAgent / 计划任务），获得开机自启与崩溃自动拉起。

## 快速开始（开发）

依赖：Go ≥ 1.23、MySQL 8（库与用户自建）。

```bash
# 全量自测（e2e：真实 MySQL + 进程内 cloud + 两个 client）
#   e2e 默认 DSN 指向 127.0.0.1:3306/gsevo_test（用户 gsevo），可用例见 e2e/e2e_test.go
go test ./... -count=1

# 起云端
GSEVO_DSN="gsevo:pw@tcp(127.0.0.1:3306)/gsevo?parseTime=true&multiStatements=true&time_zone=%27%2B00%3A00%27" \
GSEVO_ADDR=:8080 GSEVO_DATA_DIR=./data ./gs-cloud serve   # 首次启动打印 admin token 与签名公钥

# 注册一台机器（headless 流，§6.7）
./gs-cloud enroll create --label my-laptop          # → gse_…（24h 一次性）
./gs-worker login --server http://cloud:8080 --enroll gse_… \
    --skills-root ~/.claude/skills --signing-pubkey <gs-cloud key show>

# 常用管理命令
./gs-cloud device list | device revoke --id ID
./gs-cloud cmd send --device ID --type skills.scan
./gs-cloud skill list | versions --name N | set-status … | install …
```

## 状态

🚧 **P0-P4 已实现并通过自测**（e2e 覆盖全部验收标准；12 条 e2e，连续多轮全绿）：

| 验收项 | 状态 |
|--------|------|
| P0 设备在线/离线切换 | ✅ `TestP0_OnlineOffline` |
| P0 拔线重连补发 + 幂等不重复执行 | ✅ `TestP0_RedeliveryAndIdempotency` |
| P0 吊销即踢线且无法重连 | ✅ `TestP0_RevokeKick` |
| P1 采集→人工转 stable 签名→装到另一机 | ✅ `TestP1_CollectSignInstall` |
| P1 本地改动冲突跳过（skip_and_report） | ✅ 同上（含 overwrite 显式覆盖路径） |
| P1 `.prev` 本地秒回滚 | ✅ 同上 |
| P2 云端下发 prompt 实时回传 | ✅ `TestP2_TaskFlow` |
| P2 断网重连结果不丢（spool）+ 事件补齐（replay） | ✅ `TestP2_OfflineSpoolReplay` |
| P2 `task.cancel` 杀干净进程树 | ✅ `TestP2_Cancel` |
| P2 看门狗超时 | ✅ `TestP2_Timeout` |
| P3 进化全闭环：采集→生成（rationale/diff）→灰度评估→人工放行→生效 | ✅ `TestP3_EvolutionLoop` |
| P4 自动发布策略（达标自动转 stable+签名+下发） | ✅ `TestP4_AutoPromote` |
| P4 client.update 发布→rollout→验签落盘 | ✅ `TestP4_ClientUpdate` |
| P4 审计报表 + RBAC（viewer 只读） | ✅ `TestP4_ReportRBAC` |

P2 关键件：`docs/AGENT-PROTOCOL.md`（内部协议 spec，先行评审）→ AgentAdapter（gsagent 内部协议 / claude-code stream-json 映射）→ Task Runner（隔离工作目录、看门狗、事件内存环 2000 条 + events.jsonl 全量落盘、单帧 64KB 截断）→ 进程树终止（POSIX 进程组 / Windows Job Object）。`task.cancel` 为管理员操作（D11：`max_permission` 与本地 `permission_mode` 取更严者，`require_confirm` 生效时拒绝执行）。

P3 关键件：Evolution Engine（`cloud/evo`）——生成器接口（生产 Anthropic HTTP / e2e 确定性 fake）、`evolve` 生成候选（rationale 强制留存 + diff 审阅端点）、`evaluate` 受控灰度安装（candidate 评估签名，安装记录带 run id）→ 跑 BenchmarkSet → EvaluationRun（指标+verdict 留存）；人工放行沿用 stable 闸门。

P4 关键件：自动发布策略（per-skill `auto-policy`，默认关闭；达标自动 reviewed→stable→签名→下发，D9 受控豁免）、client.update 自更新（发布→rollout→下载/验签/原子 swap，v1 替换后由自启拉起，不自动退出）、审计报表端点、RBAC 最小版（admin/viewer 双角色 token）。

已知实现偏差（与设计文档）：sqlc 代码生成暂以手写 store 层替代（接口形态一致，便于后续替换）；TLS 终止依赖外部反代（dev 环境为 http/ws）。
