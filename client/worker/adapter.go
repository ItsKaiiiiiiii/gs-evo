package worker

// AgentAdapter：把各 agent 的输出翻译成统一内部事件流（D4、docs/AGENT-PROTOCOL.md）。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const maxLineBytes = 1 << 20 // 单行 1MB 上限（防失控 agent）

// agentEvent 内部事件流（§3）。
type agentEvent struct {
	Kind    string         // init | message | tool_call | result
	Text    string         // message / result summary
	Agent   string         // init
	Version string         // init
	Name    string         // tool_call
	Input   map[string]any // tool_call
	Exit    int            // result
	IsError bool           // result
}

// eventSink 消费内部事件（task.go 里转成 task.event 帧）。
type eventSink func(ev agentEvent)

// startAgent 按 agent 类型构造启动命令（不 Start）。
// 权限模式翻译见 AGENT-PROTOCOL.md §5。
func (e *Engine) startAgent(agentType string, opts *startOpts) (*exec.Cmd, error) {
	switch agentType {
	case "gsagent", "mock":
		// 内部线协议：stdin 收一行 prompt，stdout 按内部事件流输出（测试/调试用）
		bin := opts.binPath
		if bin == "" {
			return nil, fmt.Errorf("agent_path required for gsagent")
		}
		return exec.Command(bin), nil
	case "claude-code", "":
		args := []string{"-p", opts.prompt, "--output-format", "stream-json", "--verbose",
			"--permission-mode", claudePermMode(opts.permission)}
		if opts.binPath != "" {
			return exec.Command(opts.binPath, args...), nil
		}
		path, err := exec.LookPath("claude")
		if err != nil {
			return nil, fmt.Errorf("claude not found in PATH: %w", err)
		}
		return exec.Command(path, args...), nil
	default:
		return nil, fmt.Errorf("unknown agent type %q (supported: gsagent, claude-code)", agentType)
	}
}

type startOpts struct {
	binPath    string
	prompt     string
	permission string
}

func claudePermMode(perm string) string {
	switch perm {
	case "edits":
		return "acceptEdits"
	case "full":
		return "bypassPermissions"
	default:
		return "default"
	}
}

// pumpAgentLines 读 stdout 逐行翻译成内部事件；返回 adapter 是否已合成 Result。
func pumpAgentLines(agentType string, r io.Reader, sink eventSink) (gotResult bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		switch agentType {
		case "gsagent", "mock":
			if translateGSAgentLine(line, sink) {
				return true
			}
		default: // claude-code
			if translateClaudeLine(line, sink) {
				return true
			}
		}
	}
	return false
}

func translateGSAgentLine(line []byte, sink eventSink) bool {
	var ev struct {
		Type    string         `json:"type"`
		Agent   string         `json:"agent"`
		Version string         `json:"version"`
		Text    string         `json:"text"`
		Name    string         `json:"name"`
		Input   map[string]any `json:"input"`
		Exit    *int           `json:"exit"`
		IsError bool           `json:"is_error"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return false
	}
	switch ev.Type {
	case "hello":
		sink(agentEvent{Kind: "init", Agent: ev.Agent, Version: ev.Version})
	case "message":
		sink(agentEvent{Kind: "message", Text: ev.Text})
	case "tool_call":
		sink(agentEvent{Kind: "tool_call", Name: ev.Name, Input: ev.Input})
	case "result":
		exit := 0
		if ev.Exit != nil {
			exit = *ev.Exit
		}
		sink(agentEvent{Kind: "result", Text: ev.Text, Exit: exit, IsError: ev.IsError})
		return true
	}
	return false
}

// translateClaudeLine 解析 claude stream-json（§5 映射表）。
func translateClaudeLine(line []byte, sink eventSink) bool {
	var head struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &head) != nil {
		return false
	}
	switch head.Type {
	case "system":
		if head.Subtype == "init" {
			var init struct {
				Agent   string `json:"agent"`
				Model   string `json:"model"`
				Session string `json:"session_id"`
			}
			_ = json.Unmarshal(line, &init)
			agent := init.Agent
			if agent == "" {
				agent = "claude-code"
			}
			ver := init.Model
			if ver == "" {
				ver = init.Session
			}
			sink(agentEvent{Kind: "init", Agent: agent, Version: ver})
		}
	case "assistant":
		var msg struct {
			Message struct {
				Content []struct {
					Type  string         `json:"type"`
					Text  string         `json:"text"`
					Name  string         `json:"name"`
					Input map[string]any `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(head.Message, &msg) != nil {
			return false
		}
		for _, block := range msg.Message.Content {
			switch block.Type {
			case "text":
				if strings.TrimSpace(block.Text) != "" {
					sink(agentEvent{Kind: "message", Text: block.Text})
				}
			case "tool_use":
				sink(agentEvent{Kind: "tool_call", Name: block.Name, Input: block.Input})
			}
		}
	case "result":
		var res struct {
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		_ = json.Unmarshal(line, &res)
		sink(agentEvent{Kind: "result", Text: res.Result, IsError: res.IsError})
		return true
	}
	return false
}
