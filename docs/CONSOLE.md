# gs-evo 控制台操作指南

> 面向控制台使用者的操作手册。技术设计与选型见 [DESIGN.md](DESIGN.md)（运行时概念见 §6.8 / D14）。

## 登录

浏览器打开 `http://<云端地址>/console/`，粘贴管理 token：

- **admin**（全权）：gs-cloud 首次启动时生成（打印在日志并写入 `<data_dir>/admin_token`），或由现有 admin 通过 `gs-cloud token create --role admin` 签发
- **viewer**（只读）：`gs-cloud token create --role viewer` 签发；登录后写操作按钮自动隐藏，直接调 API 也会被 403 拒绝并说明角色

token 保存在浏览器 localStorage，右上角「退出」可清除。

## 页面总览（按日常使用频率）

| 页面 | 回答的问题 |
|------|-----------|
| **总览**（着陆页） | 系统健康吗？设备/Skill/指令统计 + 近期失败指令 + 客户端升级下发 |
| **设备** | 有哪些机器、在线状态、装了什么、发生过什么 |
| **Skill** | 有哪些 skill、版本状态机、审阅发布、覆盖率 |
| **任务与操作** | 怎么把机器上的 skill 收上来、怎么给机器发任务 |
| **进化** | 基准集与评估运行的全局队列 |
| **审计** | 每条指令谁在何时下发、结果如何；token 管理 |

## 设备页

- **列表**：主机名、平台、worker 版本、在线状态、标签、最后在线时间。点行进入详情
- **详情**四个区块：
  - 基本信息 + 操作（断连/吊销/编辑标签）
  - **运行时**：本机 agent 实例列表（自动发现上报）+ **「添加运行时」**——云端下发 `runtime.register` 指令，本机 worker 校验二进制存在并探测版本后登记（创建即绑定该机器，全程审计）
  - **已装 skill**：装机记录（含签名标记）
  - **指令历史 / 任务历史**：最近 50 条，含操作者与错误信息
- **签发 enroll token**：新机器接入用（一次性，24h；label 会成为设备标签）。新机器侧执行：
  ```bash
  gs-worker login --server http://<云端> --enroll <token> \
      --skills-root ~/.claude/skills --signing-pubkey <key show 的公钥>
  gs-worker autostart enable   # 可选：开机自启 + 升级自动生效
  ```

## Skill 页

点上方下拉选择 skill，进入版本时间线。每个版本一行：

- **状态徽章**：`draft → candidate → reviewed → stable`（点「晋升 X」逐步流转；转 stable 会签名，之后才可安装下发）
- **审阅差异**：候选版本与父本的 SKILL.md 行级差异 + 生成 rationale
- **灰度评估**：选基准集与灰度设备跑 EvaluationRun（可先受控安装候选）
- **安装下发**：目标根目录 + 设备圈定 → 逐台回执
- **评估历史 / 覆盖率**：该版本的评估记录与装机覆盖

自动发布策略（页底卡片）：达标线 × 连续次数 + 目标根/标签。**默认关闭**；启用后评估达标自动转 stable 并下发（评审记录全部可查）。

## 任务与操作页（运行时中心）

> 指派粒度 = **运行时**（设备上的 agent 实例，参考 multica 的 runtime 产品思路）。运行时天然绑定机器——选运行时即选机器。

### 快捷操作（把机器上的 skill 收上来）

1. 「选择运行时」下拉：`主机名 · 类型 版本 [标签]`（仅列 agent 可用且设备在线的）
2. 点 **扫描 skill**：设备白名单里的 skill 清单（本地状态 + 云端状态两列）
3. 点逐行 **获取** 或 **获取全部 skill**：本地打包上传 → 自动登记 → 完成后出现 **「去 Skill 页审阅 X」深链接**（点击自动选中该 skill）
4. 已登记且内容无变化的行显示「已是最新」，不再可重复获取

### 任务

- **高级：自定义任务**（折叠项）：使用上方所选运行时，填 prompt + 超时下发
- **任务列表**：5s 自动刷新；点行展开**事件流**（status/message/tool_call 实时轮询，stderr 灰显）
- **取消**：终态任务不显示取消按钮；运行中取消走二次确认 → 进程树终止 → 事件留痕 `cancelled by operator` → 列表 2s 快速轮询至终态翻转
- **加载更多**：超过 50 条分页拉取

### 常见疑问

| 现象 | 说明 |
|------|------|
| 时间显示与我的钟不一致？ | 已按浏览器本机时区渲染；若见 UTC 字样为审计原始值 |
| 「获取」提示"内容无变化"？ | 该内容云端已有同哈希版本（幂等，不产生脏版本）。要出新版本请修改 SKILL.md 的 `version` 字段后重新获取 |
| 运行时下拉为空？ | 设备离线或 agent 不可用；确认设备在线后点「刷新」 |
| 想新增一个运行时？ | 「设备」详情 → 运行时区块 → 「添加运行时」（本机校验后登记，24h 内生效于任务指派） |
| 取消按钮不见了？ | 任务已结束（completed/failed/cancelled/timeout 均为终态） |

## 审计页

- 指令全量历史（跨设备）：按设备/类型/状态过滤，**操作者（created_by）**列可见
- token 管理：现有 admin/viewer token 清单 + 签发（仅 admin）

## 命令对照（页面操作 ↔ CLI）

| 页面操作 | CLI 等价 |
|----------|----------|
| 签发 enroll token | `gs-cloud enroll create --label L` |
| 吊销 / 断连 / 设标签 | `gs-cloud device revoke --id ID` / 无（页面专属）/ `gs-cloud device label --id ID --label L` |
| 扫描 / 获取 | `gs-cloud cmd send --device ID --type skills.scan` / `skills.collect --payload '{"name":"N"}'` |
| 状态流转 / 安装 | `gs-cloud skill set-status …` / `skill install …` |
| 任务下发 / 取消 / 事件 | `gs-cloud task create / cancel / events` |
| 评估 / 策略 | `gs-cloud evaluate …` / `policy …` |
| 客户端升级 | `gs-cloud client-update push / rollout` |
