package proto

import "time"

// P2 任务通道消息与载荷（§6.4、docs/AGENT-PROTOCOL.md）。

const (
	TypeTaskEvent = "task.event" // c→cloud 任务过程事件
)

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
	TimeoutS      int               `json:"timeout_s,omitempty"`  // 看门狗，缺省 3600
	MaxPermission string            `json:"max_permission,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

// TaskResult task.start 的 cmd_result.data（终态，经 spool 保证送达）。
type TaskResult struct {
	TaskID     string   `json:"task_id"`
	Status     string   `json:"status"` // completed|failed|cancelled|timeout
	Exit       int      `json:"exit"`
	DurationMs int64    `json:"duration_ms"`
	Summary    string   `json:"summary"`
	Artifacts  []string `json:"artifacts,omitempty"` // 工作目录内产物相对路径（v1 预留）
}

// ---- task.cancel / task.events_replay ----

type TaskCancelPayload struct {
	TaskID string `json:"task_id"`
}

type EventsReplayPayload struct {
	TaskID  string `json:"task_id"`
	FromSeq int64  `json:"from_seq"`
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
