// mockagent：内部线协议（gsagent）的测试实现，见 docs/AGENT-PROTOCOL.md §4。
// 用法: mockagent [ok|sleep|stream]
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func emit(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func main() {
	mode := os.Getenv("MOCK_MODE")
	if mode == "" && len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "" {
		mode = "ok"
	}
	// 读一行 prompt（协议要求；内容不关心）
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &head) == nil && head.Type == "prompt" {
			break
		}
	}
	emit(map[string]any{"type": "hello", "agent": "gsagent", "version": "1"})

	switch mode {
	case "sleep": // 供 cancel/timeout 测试：长睡不发 result
		emit(map[string]any{"type": "message", "text": "starting long work"})
		time.Sleep(120 * time.Second)
		emit(map[string]any{"type": "result", "text": "should never see this", "exit": 0})
	case "gap": // 断线补齐测试：空窗期横跨断线窗口，事件必然在离线期产生
		emit(map[string]any{"type": "message", "text": "before-gap"})
		time.Sleep(3 * time.Second)
		emit(map[string]any{"type": "message", "text": "after-gap"})
		emit(map[string]any{"type": "result", "text": "gap done", "exit": 0})
	case "stream": // 慢速多条 message 再收尾
		for i := 1; i <= 5; i++ {
			emit(map[string]any{"type": "message", "text": fmt.Sprintf("chunk %d", i)})
			time.Sleep(400 * time.Millisecond)
		}
		emit(map[string]any{"type": "tool_call", "name": "mock.report", "input": map[string]any{"chunks": 5}})
		emit(map[string]any{"type": "result", "text": "streamed done", "exit": 0})
	default: // ok：常规成功路径
		emit(map[string]any{"type": "message", "text": "working on it"})
		emit(map[string]any{"type": "tool_call", "name": "demo.echo", "input": map[string]any{"path": "/tmp/demo"}})
		emit(map[string]any{"type": "message", "text": "done thinking"})
		emit(map[string]any{"type": "result", "text": "task finished ok", "exit": 0})
	}
}
