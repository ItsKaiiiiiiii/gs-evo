# Agent 内部协议规范（v1）

> Client（gs-agent-worker 的 AgentAdapter）与本机 coding agent 之间的 stdio 交互规范。
> 本文是 D4 的落地文档：**各 agent 差异被 adapter 吸收，框架只见统一事件流**。
> 基型：claude Code `--output-format stream-json`；吸收 ACP 的会话/权限语义。

## 1. 术语

- **Adapter**：把某种 agent 的输出翻译成"内部事件流"的翻译层。框架（Task Runner）只消费内部事件流。
- **内部事件流**：`Init / Message / ToolCall / Result` 四种（§3）。

## 2. Spawn 契约

| 项 | 约定 |
|----|------|
| 工作目录 | 任务隔离目录 `~/.gs-agent-worker/tasks/<task_id>/work`（§7.4） |
| stdin/stdout | UTF-8，**JSON lines**（每行一个 JSON 对象，`\n` 分隔）；stderr 为自由文本（整体采集，不解析） |
| env | 继承 worker 进程环境 + `payload.env` 显式键覆盖；云端**不下发**密钥（D12，agent 用本机凭证） |
| 退出码 | 0 = 成功；非 0 = 失败；被信号终止（cancel/timeout）由 worker 判定，不依赖退出码 |
| 进程树 | worker 以进程组（POSIX `setpgid`）/ Job Object（Windows）启动；终止时整树回收（§6） |

## 3. 内部事件流（adapter 输出，框架输入）

按时间顺序产生，`Result` 恰好一个、必然最后：

| 事件 | 字段 | 语义 |
|------|------|------|
| `Init` | `agent, version, session?` | agent 就绪（连接建立/初始化完成） |
| `Message` | `text` | 助手可见输出（增量或整段，由 adapter 保证语义完整） |
| `ToolCall` | `name, input` | agent 调用了工具（input 为对象，供云端观测） |
| `Result` | `exit, summary, is_error` | 终态：summary 为最终文本摘要 |

## 4. 内部线协议（`gsagent` 类型，内置实现与测试 mock 共用）

adapter 类型 `gsagent` 直接说内部事件流的线格式：

```
stdin:   {"type":"prompt","text":"<整个 prompt>"}
stdout:  {"type":"hello","agent":"gsagent","version":"1"}
         {"type":"message","text":"…"}
         {"type":"tool_call","name":"…","input":{…}}
         {"type":"result","text":"…","exit":0,"is_error":false}
```

约束：逐行读写、行缓冲无界由 agent 自控；worker 对每行大小设 1MB 上限（防失控）。

## 5. claude-code 映射

spawn：`claude -p <prompt> --output-format stream-json --verbose --permission-mode <mapped>`（追加 `--max-turns` 可选）

| 内部事件 | claude stream-json 来源 |
|----------|------------------------|
| `Init` | `{"type":"system","subtype":"init",…}` → agent/version/model |
| `Message` | `{"type":"assistant","message":{"content":[{"type":"text","text":…}]}}` 的 text 块 |
| `ToolCall` | 同上 content 中 `{"type":"tool_use","name":…,"input":…}` 块 |
| `Result` | `{"type":"result","subtype":…,"result":…,"is_error":…}` |

**权限模式映射**（D11 三层之最严者生效后，翻译为 claude 参数）：

| gs-evo 权限 | claude 参数 | 语义 |
|-------------|-------------|------|
| `readonly`（默认） | `--permission-mode default` | 无头模式下未预授权工具一律拒绝，仅读 |
| `edits` | `--permission-mode acceptEdits` | 允许文件编辑 |
| `full` | `--dangerously-skip-permissions` | 完全授权（高危，需显式配置） |

未来 ACP/codex 等各加一个 adapter 文件，框架不动（§11）。

## 6. 终止语义

- `task.cancel` / 看门狗超时 → worker 杀**进程树**：POSIX `kill(-pgid, SIGKILL)`；Windows `Job Object`（`TerminateJobObject`）。
- 终止后 worker 合成 `Result{exit:-1, summary:"cancelled|timeout", is_error:true}`——**Result 永不缺席**。

## 7. 与线上协议的边界

- 内部事件流 → `task.event` 帧（`Init/Message/ToolCall` → kind `status/message/tool_call`；单帧 ≤64KB，超出截断并打标；全量原文落 `events.jsonl`）；
- `Result` → `task.start` 的 `cmd_result`（经 spool 保证送达），同时补一条 `status` 事件；
- 事件缓冲 2000 条（内存环）+ 磁盘 `events.jsonl` 全量，`task.events_replay {from_seq}` 从两者重发（§6.3）。
