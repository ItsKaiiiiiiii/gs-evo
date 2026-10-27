# gs-evo

客户端-云端的 coding agent **Skill 收集 · 进化 · 分发系统**。

各台电脑上的 coding agent 及其 skill 相互隔离。gs-evo 通过每台机器上的常驻 Client（`gs-agent-worker`）与云端保持单条 WSS 长连接，实现：

- **收集**：云端远程扫描、收集各机器上的 skill（含能力描述 manifest）；
- **进化**：云端集中管理 skill 版本，LLM 生成改进候选 → 基准评估 → 人工审阅 → 签名发布；
- **分发**：把审批后的 skill 版本安全下发安装/更新到指定机器的指定目录；
- **驱动**：云端实时下发任务，Client 通过 stdio 驱动本机 agent 执行，过程与结果实时回传。

## 设计原则

1. **一条 WSS 长连接**承载心跳、指令、事件、结果；大文件走 HTTPS 预签名 URL；
2. **统一指令模型**（`cmd → cmd_result`，at-least-once + Client 幂等）覆盖所有云端能力；
3. **Client 是执行器、云端是决策者**——但 Client 持有三道不可协商的本地保险：**验签、目录白名单主权、幂等**。

## 技术栈

- **语言**：Go（client 与 cloud 同栈；multica 同款，其 daemon 生命周期 / autostart / 重连经验可直接移植）
- **协议**：JSON over WSS（不上 protobuf），消息类型共享于 `pkg/proto`
- **存储**：PostgreSQL（sqlc + golang-migrate）+ S3 兼容对象存储（自托管 MinIO 起步）
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

## 状态

🚧 设计阶段（v2 方案已定稿，P0 未开始）。
