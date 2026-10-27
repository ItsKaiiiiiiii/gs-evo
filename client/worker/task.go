package worker

// Task Runner：任务隔离工作目录、看门狗、事件流（内存环 + events.jsonl 落盘 + 重放）。
// §6.4 / docs/AGENT-PROTOCOL.md §7。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

const (
	eventBufferCap   = 2000 // 内存事件环上限（§6.4）
	maxEventFrame    = proto.MaxEventFrameBytes
	defaultTimeoutS  = 3600
	hardTaskDeadline = 24 * time.Hour // task.start 兜底上限（看门狗之外的硬顶）
)

type activeTask struct {
	id      string
	started time.Time

	cancelOnce sync.Once
	cancelled  chan struct{} // task.cancel 触发
	killTree   func() error

	mu             sync.Mutex
	events         []proto.TaskEvent // 内存环（≤eventBufferCap）
	seq            int64
	adapterSummary string
}

func (t *activeTask) nextEvent(kind string, data map[string]any) proto.TaskEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	ev := proto.TaskEvent{Type: proto.TypeTaskEvent, TaskID: t.id, Seq: t.seq, Kind: kind,
		Data: truncateEventData(kind, data), TS: time.Now()}
	t.events = append(t.events, ev)
	if len(t.events) > eventBufferCap {
		t.events = t.events[len(t.events)-eventBufferCap:]
	}
	return ev
}

func (t *activeTask) requestCancel() {
	t.cancelOnce.Do(func() {
		close(t.cancelled)
		if t.killTree != nil {
			_ = t.killTree()
		}
	})
}

// truncateEventData 单帧 ≤64KB：文本超限截断并打标（全量原文在 events.jsonl）。
func truncateEventData(kind string, data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	switch kind {
	case proto.EventKindMessage, proto.EventKindStdout, proto.EventKindStderr:
		if text, ok := data["text"].(string); ok && len(text) > maxEventFrame {
			out := map[string]any{}
			for k, v := range data {
				out[k] = v
			}
			out["text"] = text[:maxEventFrame]
			out["truncated"] = true
			out["original_size"] = len(text)
			return out
		}
	}
	return data
}

// ---- 任务注册表 ----

func (e *Engine) registerTask(id string) (*activeTask, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.tasks[id]; ok {
		return nil, fmt.Errorf("task %s already running", id)
	}
	t := &activeTask{id: id, started: time.Now(), cancelled: make(chan struct{})}
	e.tasks[id] = t
	return t, nil
}

func (e *Engine) finishTask(t *activeTask) {
	e.mu.Lock()
	delete(e.tasks, t.id)
	e.mu.Unlock()
}

func (e *Engine) defaultAgentType() string {
	for _, a := range e.ProbeAgents(context.Background()) {
		if a.OK {
			if a.Type == "claude-code" {
				return "claude-code"
			}
		}
	}
	if e.Cfg.AgentPath != "" {
		return "gsagent"
	}
	return "claude-code"
}

// ---- task.start ----

// handleTaskStart 同步等待任务终态后返回（dispatcher 对 task.start 用长超时）。
// 最终 cmd_result 经 spool 保证送达（§7.4）。
func (e *Engine) handleTaskStart(ctx context.Context, payload map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	var p proto.TaskStartPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.TaskID == "" || p.Prompt == "" {
		return nil, errors.New("bad task.start payload (need task_id, prompt)")
	}
	// D11：权限 = min(云端 max_permission, 本地配置)；本地确认开关优先拒绝
	perm := proto.StrictestPermission(p.MaxPermission, e.Cfg.PermissionMode)
	if e.Cfg.RequireConfirm {
		return nil, errors.New("confirmation required (require_confirm enabled on this device)")
	}

	t, err := e.registerTask(p.TaskID)
	if err != nil {
		return nil, err
	}

	workdir := filepath.Join(e.Home.TasksDir(), p.TaskID, "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		e.finishTask(t)
		return nil, err
	}
	evFile, err := os.OpenFile(filepath.Join(e.Home.TasksDir(), p.TaskID, "events.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		e.finishTask(t)
		return nil, err
	}
	defer evFile.Close()

	timeout := time.Duration(p.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeoutS * time.Second
	}
	runCtx, watchdog := context.WithTimeout(context.Background(), timeout)
	defer watchdog()

	agentType := p.AgentType
	if agentType == "" {
		agentType = e.defaultAgentType()
	}
	binPath := ""
	if agentType == "gsagent" || agentType == "mock" {
		binPath = e.Cfg.AgentPath
	} else if e.Cfg.AgentPath != "" {
		binPath = e.Cfg.AgentPath // 显式指定二进制时交给对应 adapter 使用
	}

	cmd, err := e.startAgent(agentType, &startOpts{binPath: binPath, prompt: p.Prompt, permission: perm})
	if err != nil {
		e.finishTask(t)
		return nil, err
	}
	cmd.Dir = workdir
	cmd.Env = envFor(p.Env)
	prepareProc(cmd) // POSIX 进程组 / Windows 挂起创建（§6）

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	e.Log.Info("task starting", "task", p.TaskID, "agent", agentType, "permission", perm, "timeout", timeout.String())
	if err := cmd.Start(); err != nil {
		e.finishTask(t)
		return nil, fmt.Errorf("spawn agent: %w", err)
	}
	killTree, err := finishProcSetup(cmd) // Windows: 挂入 Job 并恢复；POSIX: 组杀函数
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		e.finishTask(t)
		return nil, err
	}
	t.killTree = killTree

	// 事件出口：内存环 + events.jsonl 全量 + WS 尽力发送
	emit := func(kind string, data map[string]any) {
		ev := t.nextEvent(kind, data)
		if b, err := json.Marshal(ev); err == nil {
			_, _ = evFile.Write(append(b, '\n'))
		}
		e.sendTaskEvent(ev) // 断线丢弃；补齐靠 events_replay（§6.4）
	}

	// gsagent 内部协议：prompt 经 stdin 传递
	if agentType == "gsagent" || agentType == "mock" {
		b, _ := json.Marshal(map[string]string{"type": "prompt", "text": p.Prompt})
		_, _ = stdin.Write(append(b, '\n'))
	}
	_ = stdin.Close()

	// stderr 采集（尾部留存 + 事件流）
	var stderrTail strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sb := bufio.NewScanner(stderr)
		sb.Buffer(make([]byte, 64*1024), maxLineBytes)
		for sb.Scan() {
			line := sb.Text()
			if stderrTail.Len() > 16*1024 {
				stderrTail.Reset()
			}
			stderrTail.WriteString(line + "\n")
			emit(proto.EventKindStderr, map[string]any{"text": line})
		}
	}()

	// stdout → 内部事件流
	adapterSummary := new(string)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		pumpAgentLines(agentType, stdout, func(ev agentEvent) {
			e.Log.Debug("agent event", "task", p.TaskID, "kind", ev.Kind, "text", ev.Text)
			switch ev.Kind {
			case "init":
				emit(proto.EventKindStatus, map[string]any{"status": proto.TaskRunning,
					"agent": ev.Agent, "version": ev.Version, "reason": "init"})
			case "message":
				emit(proto.EventKindMessage, map[string]any{"text": ev.Text})
			case "tool_call":
				emit(proto.EventKindToolCall, map[string]any{"name": ev.Name, "input": ev.Input})
			case "result":
				*adapterSummary = ev.Text
			}
		})
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	finalStatus, exitCode, reason := proto.TaskFailed, -1, ""
	select {
	case err := <-waitDone: // 进程自然退出
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			}
		} else {
			exitCode = 0
		}
		finalStatus = proto.TaskCompleted
		reason = "exit"
		if exitCode != 0 {
			finalStatus = proto.TaskFailed
			reason = fmt.Sprintf("exit %d", exitCode)
		}
	case <-t.cancelled: // task.cancel
		_ = t.killTree()
		<-waitDone
		finalStatus = proto.TaskCancelled
		reason = "cancelled by operator"
	case <-runCtx.Done(): // 看门狗超时
		_ = t.killTree()
		<-waitDone
		finalStatus = proto.TaskTimeout
		reason = fmt.Sprintf("watchdog timeout (%s)", timeout)
	}
	<-pumpDone
	<-stderrDone

	summary := *adapterSummary
	if summary == "" {
		summary = strings.TrimSpace(stderrTail.String())
	}
	if summary == "" {
		summary = reason
	}
	if len(summary) > 8*1024 {
		summary = summary[:8*1024] + "…(truncated)"
	}

	emit(proto.EventKindStatus, proto.StatusEventData(finalStatus, reason))
	e.finishTask(t)
	duration := time.Since(t.started)

	tr := proto.TaskResult{
		TaskID: p.TaskID, Status: finalStatus, Exit: exitCode,
		DurationMs: duration.Milliseconds(), Summary: summary,
	}
	data, _ := structToMap(tr)
	return data, nil
}

// ---- task.cancel ----

func (e *Engine) handleTaskCancel(ctx context.Context, payload map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	var p proto.TaskCancelPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.TaskID == "" {
		return nil, errors.New("need task_id")
	}
	e.mu.Lock()
	t := e.tasks[p.TaskID]
	e.mu.Unlock()
	if t == nil {
		return nil, fmt.Errorf("task %s not running", p.TaskID)
	}
	t.requestCancel()
	data, _ := structToMap(map[string]any{"task_id": p.TaskID, "cancel_requested": true})
	return data, nil
}

// ---- task.events_replay ----

// handleEventsReplay 从内存环（活跃）/ events.jsonl（已结束）重发 from_seq 之后的事件。
func (e *Engine) handleEventsReplay(ctx context.Context, payload map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	var p proto.EventsReplayPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.TaskID == "" {
		return nil, errors.New("need task_id")
	}
	e.mu.Lock()
	t := e.tasks[p.TaskID]
	var buffered []proto.TaskEvent
	if t != nil {
		t.mu.Lock()
		buffered = append([]proto.TaskEvent(nil), t.events...)
		t.mu.Unlock()
	}
	e.mu.Unlock()

	var resent int64
	if t != nil {
		for _, ev := range buffered {
			if ev.Seq >= p.FromSeq {
				e.sendTaskEvent(ev)
				resent++
			}
		}
		// 活跃任务的极新事件可能晚于快照——truncated=false 但以云端 seq 空洞检测兜底
		data, _ := structToMap(proto.EventsReplayResult{TaskID: p.TaskID, Resent: resent})
		return data, nil
	}

	// 已结束任务：从磁盘重放
	f, err := os.Open(filepath.Join(e.Home.TasksDir(), p.TaskID, "events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("task %s not found", p.TaskID)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	truncated := false
	for sc.Scan() {
		var ev proto.TaskEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Seq < p.FromSeq {
			continue
		}
		// 重放帧同样受 64KB 上限约束（原文可能超出）
		ev.Data = truncateEventData(ev.Kind, ev.Data)
		if ev.Data != nil {
			if _, isTrunc := ev.Data["truncated"]; isTrunc {
				truncated = true
			}
		}
		e.sendTaskEvent(ev)
		resent++
	}
	data, _ := structToMap(proto.EventsReplayResult{TaskID: p.TaskID, Resent: resent, Truncated: truncated})
	return data, nil
}

// sendTaskEvent 经当前连接尽力发送（无连接则丢弃——seq 空洞检测 + replay 兜底）。
func (e *Engine) sendTaskEvent(ev proto.TaskEvent) {
	e.mu.Lock()
	ws := e.currentConn
	e.mu.Unlock()
	if ws == nil {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = ws.WriteMessage(websocket.TextMessage, b) // 写失败仅丢帧，重放兜底
}

// envFor 继承 worker 环境 + 显式覆盖（D12：密钥用本机已有，云端不下发）。
func envFor(overrides map[string]string) []string {
	env := os.Environ()
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}
