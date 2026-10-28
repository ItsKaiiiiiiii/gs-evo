package worker

// 运行时（Runtime）：本机 agent 实例的一等实体（参考 multica 的 runtime 概念，
// 采纳其"本地发现注册 + 云端指派"；"创建"收敛为受审计的 runtime.register 指令）。
// 设计见 docs/DESIGN.md §6.8 / D14。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"


	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

const runtimeSyncInterval = 5 * time.Minute

// RuntimeEntry 本机运行时登记（registered 来源持久化在 state.json；probed 每次探测派生）。
type RuntimeEntry struct {
	Type   string            `json:"type"`
	Path   string            `json:"path,omitempty"`
	Args   []string          `json:"args,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Source string            `json:"source"` // probed | registered
}

// RuntimeID 稳定身份：derive(type + path)——重装/重启不变。
func RuntimeID(entryType, path string) string {
	sum := sha256.Sum256([]byte(entryType + "|" + path))
	return "rt-" + hex.EncodeToString(sum[:])[:16]
}

// probeRuntimeVersion 探测运行时版本（--version，5s 超时）。
func (e *Engine) probeRuntimeVersion(ctx context.Context, entryType, path string, args []string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	outBytes, err := exec.CommandContext(cctx, path, append(args, "--version")...).Output()
	if err != nil {
		return "", false
	}
	first := firstLineStr(string(outBytes))
	// gsagent 协议：首行可能是 hello JSON（{"version":"1",...}）→ 提取 version 字段
	var probe struct {
		Version string `json:"version"`
	}
	if strings.HasPrefix(strings.TrimSpace(first), "{") {
		if json.Unmarshal([]byte(first), &probe) == nil && probe.Version != "" {
			return probe.Version, true
		}
	}
	return first, true
}

// buildRuntimes 汇总当前运行时列表：registered（state 持久）∪ probed（探测派生）。
func (e *Engine) buildRuntimes(ctx context.Context) []proto.RuntimeInfo {
	e.mu.Lock()
	registered := map[string]RuntimeEntry{}
	for id, r := range e.State.Runtimes {
		registered[id] = r
	}
	e.mu.Unlock()

	seen := map[string]bool{}
	var out []proto.RuntimeInfo

	// registered 优先（显式登记的身份与参数）
	for id, r := range registered {
		ok := true
		version := ""
		if r.Path != "" {
			if _, err := os.Stat(r.Path); err == nil {
				version, ok = e.probeRuntimeVersion(ctx, r.Type, r.Path, r.Args)
			} else {
				ok = false
			}
		}
		seen[id] = true
		out = append(out, proto.RuntimeInfo{RuntimeID: id, Type: r.Type, Version: version, Path: r.Path, OK: ok, Source: "registered"})
	}

	// probed：探测到的本机 agent（claude-code 等）
	for _, a := range e.ProbeAgents(ctx) {
		id := RuntimeID(a.Type, a.Path)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, proto.RuntimeInfo{RuntimeID: id, Type: a.Type, Version: a.Version, Path: a.Path, OK: a.OK, Source: "probed"})
	}

	// 配置的 agent_path = 自带 agent（gsagent 内部协议）→ 派生为运行时
	if e.Cfg.AgentPath != "" {
		id := RuntimeID("gsagent", e.Cfg.AgentPath)
		if !seen[id] {
			version, ok := e.probeRuntimeVersion(ctx, "gsagent", e.Cfg.AgentPath, nil)
			out = append(out, proto.RuntimeInfo{RuntimeID: id, Type: "gsagent",
				Version: version, Path: e.Cfg.AgentPath, OK: ok, Source: "probed"})
		}
	}
	return out
}

// resolveRuntime 任务指派解析：runtime_id 精确匹配 → 供货类型/路径/参数/环境。
// 未指定 runtime_id 时返回零值（由 agent_type / 默认探测决定）。
func (e *Engine) resolveRuntime(ctx context.Context, runtimeID string) (RuntimeEntry, error) {
	if runtimeID == "" {
		return RuntimeEntry{}, nil
	}
	e.mu.Lock()
	reg, okReg := e.State.Runtimes[runtimeID]
	e.mu.Unlock()
	if okReg {
		reg.Source = "registered"
		return reg, nil
	}
	rts := e.buildRuntimes(ctx)
	for _, rt := range rts {
		if rt.RuntimeID == runtimeID {
			return RuntimeEntry{Type: rt.Type, Path: rt.Path, Source: "probed"}, nil
		}
	}
	avail := make([]string, 0, len(rts))
	for _, rt := range rts {
		avail = append(avail, rt.RuntimeID+"("+rt.Type+")")
	}
	return RuntimeEntry{}, fmt.Errorf("runtime %s not found on this device; available: %v", runtimeID, avail)
}

// handleRuntimeRegister runtime.register：云端要求登记运行时（受审计创建）。
// worker 侧校验（二进制存在、可探测版本）后才持久化——云端不直接写用户机器。
func (e *Engine) handleRuntimeRegister(ctx context.Context, payload map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	var p proto.RuntimeRegisterPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.Type == "" {
		return nil, errors.New(`bad runtime.register payload: 需要 {"type":"gsagent|claude-code","path":"...","args":[...],"env":{...}}`)
	}
	if p.Path == "" {
		return nil, errors.New("path required（运行时二进制必须显式指定）")
	}
	if _, err := os.Stat(p.Path); err != nil {
		return nil, fmt.Errorf("path 不存在或不可访问: %s", p.Path)
	}
	id := RuntimeID(p.Type, p.Path)
	version, ok := e.probeRuntimeVersion(ctx, p.Type, p.Path, p.Args)

	e.mu.Lock()
	if e.State.Runtimes == nil {
		e.State.Runtimes = map[string]RuntimeEntry{}
	}
	e.State.Runtimes[id] = RuntimeEntry{Type: p.Type, Path: p.Path, Args: p.Args, Env: p.Env, Source: "registered"}
	e.Home.SaveState(e.State)
	e.mu.Unlock()

	go e.syncRuntimes(context.Background())

	e.Log.Info("runtime registered", "runtime", id, "type", p.Type, "path", p.Path, "ok", ok)
	data, _ := structToMap(map[string]any{"runtime_id": id, "type": p.Type, "ok": ok, "version": version})
	return data, nil
}

// syncRuntimes 周期同步运行时列表（变更即发 + 5min 周期兜底）。
func (e *Engine) syncRuntimes(ctx context.Context) {
	e.mu.Lock()
	ws := e.currentConn
	e.mu.Unlock()
	if ws == nil {
		return
	}
	rts := e.buildRuntimes(ctx)
	if len(rts) == 0 {
		return
	}
	msg, err := json.Marshal(proto.RuntimeSyncMsg{Type: proto.TypeRuntimeSync, Runtimes: rts})
	if err != nil {
		return
	}
	_ = e.writeWS(ws, msg)
}

// startRuntimeSyncLoop 周期同步（引擎启动时开启一次）。
func (e *Engine) startRuntimeSyncLoop(ctx context.Context) {
	go func() {
		t := time.NewTicker(runtimeSyncInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.syncRuntimes(ctx)
			}
		}
	}()
}

func firstLineStr(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
