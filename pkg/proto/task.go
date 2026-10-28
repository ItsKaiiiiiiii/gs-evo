package proto

import "time"

// P2 任务通道消息与载荷（§6.4、docs/AGENT-PROTOCOL.md）。

const (
	TypeTaskEvent   = "task.event"   // c→cloud 任务过程事件
	TypeRuntimeSync = "runtime.sync" // c→cloud 运行时列表同步（周期 + 变更即发）
)

// 指令类型（P5 运行时）
const CmdRuntimeRegister = "runtime.register" // 云端要求 worker 登记一个运行时（受审计的"创建"入口）

// 事件 kind（内部事件流 → 线上 kind 的映射见 AGENT-PROTOCOL.md §7）。
const (
	EventKindStatus   = "status" // 生命周期迁移（running/completed/failed/cancelled/timeout）
	EventKindStdout   = "stdout" // 原始 stdout 增量（≤64KB/帧，超出截断打标）
	EventKindStderr   = "stderr"
	EventKindMessage  = "message" // 助手可见输出
	EventKindToolCall = "tool_call"
)

// 任务终态（status 事件 / task.start cmd_result 共用）。
const (
	TaskRunning   = "running"
	TaskCompleted = "completed"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
	TaskTimeout   = "timeout"
)

// 权限模式（严格度递增；取"更严者"= 序号小者，§6.4）。
var permissionRank = map[string]int{"readonly": 0, "edits": 1, "full": 2}

// StrictestPermission 取两者中更严的权限模式（D11）。
func StrictestPermission(a, b string) string {
	ra, oka := permissionRank[a]
	rb, okb := permissionRank[b]
	if !oka {
		return b
	}
	if !okb {
		return a
	}
	if ra <= rb {
		return a
	}
	return b
}

// ---- task.start ----

type TaskStartPayload struct {
	TaskID        string            `json:"task_id"`
	Prompt        string            `json:"prompt"`
	AgentType     string            `json:"agent_type,omitempty"` // 空 = 首个可用 agent
	RuntimeID     string            `json:"runtime_id,omitempty"` // 精确指派本机运行时（优先于 AgentType，D14）
	TimeoutS      int               `json:"timeout_s,omitempty"`  // 看门狗，缺省 3600
	MaxPermission string            `json:"max_permission,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

// TaskResult task.start 的 cmd_result.data（终态，经 spool 保证送达）。
type TaskResult struct {
	TaskID     string   `json:"task_id"`
	RuntimeID  string   `json:"runtime_id,omitempty"`
	Status     string   `json:"status"` // completed|failed|cancelled|timeout
	Exit       int      `json:"exit"`
	DurationMs int64    `json:"duration_ms"`
	Summary    string   `json:"summary"`
	Artifacts  []string `json:"artifacts,omitempty"` // 工作目录内产物相对路径（v1 预留）
}

// ---- client.update（P4 自更新）----

type ClientUpdatePayload struct {
	Version   string `json:"version"`
	URL       string `json:"url"` // 一次性票据 URL（tar.gz，内含新 worker 二进制）
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"` // ed25519("gs-evo-update-v1:"+sha256)
}

// ---- task.cancel / task.events_replay ----

type TaskCancelPayload struct {
	TaskID string `json:"task_id"`
}

type EventsReplayPayload struct {
	TaskID  string `json:"task_id"`
	FromSeq int64  `json:"from_seq"`
}

// RuntimeInfo 运行时（本机 agent 实例，一等实体）。
type RuntimeInfo struct {
	RuntimeID string `json:"runtime_id"` // 稳定身份：derive(type + path)
	Type      string `json:"type"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	OK        bool   `json:"ok"`
	Source    string `json:"source,omitempty"` // probed | registered
}

// RuntimeSyncMsg c→cloud 运行时列表同步。
type RuntimeSyncMsg struct {
	Type     string        `json:"type"` // "runtime.sync"
	Runtimes []RuntimeInfo `json:"runtimes"`
}

// RuntimeRegisterPayload runtime.register：云端要求 worker 登记运行时（受审计创建）。
type RuntimeRegisterPayload struct {
	Type string            `json:"type"` // gsagent | claude-code
	Path string            `json:"path,omitempty"`
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// EventsReplayResult cmd_result.data。
type EventsReplayResult struct {
	TaskID    string `json:"task_id"`
	Resent    int64  `json:"resent"`
	Truncated bool   `json:"truncated"` // 缓冲不足时 true（老事件已丢，以 seq 为准）
}

// ---- task.event 帧 ----

type TaskEvent struct {
	Type   string         `json:"type"` // "task.event"
	TaskID string         `json:"task_id"`
	Seq    int64          `json:"seq"`
	Kind   string         `json:"kind"`
	Data   map[string]any `json:"data,omitempty"`
	TS     time.Time      `json:"ts"`
}

// StatusEventData kind=status 的 data。
func StatusEventData(status, reason string) map[string]any {
	return map[string]any{"status": status, "reason": reason}
}
