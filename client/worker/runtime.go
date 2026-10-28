package worker

// 运行时（Runtime）：本机 agent 实例的一等实体（multica 产品思路）。
// 创建 = 用户选类型（不填路径）：worker 按类型注册表在设备 PATH 中定位二进制，
// 在 client 目录下建专属 workspace，做连通性自检后登记上报（D14/§6.8）。
// 生命周期：create（runtime.register）/ remove（runtime.remove）/ 状态同步（runtime.sync）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// probeRuntimeVersion 连通性自检：运行 --version 探测版本（5s 超时）。
// gsagent 协议的首行可能是 hello JSON（{"version":"1",...}）→ 提取 version 字段。
func (e *Engine) probeRuntimeVersion(ctx context.Context, rtType, binPath string, args []string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	outBytes, err := exec.CommandContext(cctx, binPath, append(args, "--version")...).Output()
	if err != nil {
		return "", false
	}
	first := firstLineStr(string(outBytes))
	if strings.HasPrefix(strings.TrimSpace(first), "{") {
		var probe struct {
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(first), &probe) == nil && probe.Version != "" {
			return probe.Version, true
		}
	}
	return first, true
}

const runtimeSyncInterval = 5 * time.Minute

// RuntimeEntry 本机运行时登记（registered 持久化在 state.json；probed 探测派生）。
type RuntimeEntry struct {
	Type      string   `json:"type"`
	BinPath   string   `json:"bin_path,omitempty"` // 解析出的二进制绝对路径
	Workspace string   `json:"workspace,omitempty"`
	Args      []string `json:"args,omitempty"`
	Source    string   `json:"source"` // probed | registered
}

// RuntimeID 稳定身份：derive(type)——绑定设备后，每类型一个运行时实例。
func RuntimeID(entryType string) string {
	sum := sha256.Sum256([]byte("runtime-type:" + entryType))
	return "rt-" + hex.EncodeToString(sum[:])[:16]
}

func (e *Engine) runtimeWorkspace(rtID string) string {
	return filepath.Join(e.Home.Dir, "runtimes", rtID)
}

// resolveBinary 按类型定位二进制：显式 AgentPath（gsagent）→ 注册表 Binaries 依次 LookPath。
func (e *Engine) resolveBinary(rtType string) (string, error) {
	if rtType == "gsagent" || rtType == "mock" {
		if e.Cfg.AgentPath != "" {
			return e.Cfg.AgentPath, nil
		}
	}
	for _, name := range proto.BinariesFor(rtType) {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if e.Cfg.AgentPath != "" {
		return e.Cfg.AgentPath, nil
	}
	return "", fmt.Errorf("设备上未找到 %s 的可执行文件（尝试: %s）——请先在设备上安装，或将二进制放入 PATH",
		rtType, strings.Join(proto.BinariesFor(rtType), "/"))
}

// buildRuntimes 汇总【已创建】的运行时（registered，用户显式创建）。
// 自动探测到的 agent 不作为运行时上报——运行时是用户创建的资源，不是探测结果。
// （任务的 agent_type 指派不受影响：ProbeAgents 仍服务于无 runtime_id 的任务。）
func (e *Engine) buildRuntimes(ctx context.Context) []proto.RuntimeInfo {
	e.mu.Lock()
	registered := map[string]RuntimeEntry{}
	for id, r := range e.State.Runtimes {
		registered[id] = r
	}
	e.mu.Unlock()

	out := make([]proto.RuntimeInfo, 0, len(registered))
	for id, r := range registered {
		ok := true
		version := ""
		if r.BinPath != "" {
			if _, err := os.Stat(r.BinPath); err == nil {
				version, ok = e.probeRuntimeVersion(ctx, r.Type, r.BinPath, r.Args)
			} else {
				ok = false
			}
		}
		out = append(out, proto.RuntimeInfo{RuntimeID: id, Type: r.Type, Version: version,
			Path: r.BinPath, Workspace: r.Workspace, OK: ok, Source: r.Source})
	}
	return out
}

// resolveRuntime 任务指派解析：runtime_id → 运行时（供货类型/二进制/参数）。
func (e *Engine) resolveRuntime(ctx context.Context, runtimeID string) (RuntimeEntry, error) {
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
			return RuntimeEntry{Type: rt.Type, BinPath: rt.Path, Source: "probed"}, nil
		}
	}
	avail := make([]string, 0, len(rts))
	for _, rt := range rts {
		avail = append(avail, rt.RuntimeID+"("+rt.Type+")")
	}
	return RuntimeEntry{}, fmt.Errorf("runtime %s not found on this device; available: %v", runtimeID, avail)
}

// handleRuntimeRegister runtime.register：创建运行时（选类型即可，不填路径）。
// 流程：类型校验 → 定位二进制（PATH）→ 建专属 workspace → 连通性自检（版本探测）
// → 持久化登记（source=registered）→ 立即同步上报在离线状态。
func (e *Engine) handleRuntimeRegister(ctx context.Context, payload map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	var p proto.RuntimeRegisterPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.Type == "" {
		return nil, errors.New(`bad runtime.register payload: 需要 {"type":"claude-code|qodercn|gsagent","args":[...]}`)
	}
	if !proto.IsSupportedRuntimeType(p.Type) {
		return nil, fmt.Errorf("不支持的运行时类型 %q（当前支持: claude-code, qodercn, gsagent）", p.Type)
	}

	rtID := RuntimeID(p.Type)
	e.mu.Lock()
	if _, exists := e.State.Runtimes[rtID]; exists {
		e.mu.Unlock()
		return nil, fmt.Errorf("runtime %s 已存在（同类型每设备一个实例；如需重建请先删除）", p.Type)
	}
	e.mu.Unlock()

	binPath, err := e.resolveBinary(p.Type)
	if err != nil {
		return nil, err
	}

	// 专属 workspace：client 目录下 runtimes/<rtID>/
	wsDir := e.runtimeWorkspace(rtID)
	if err := os.MkdirAll(filepath.Join(wsDir, "work"), 0o755); err != nil {
		return nil, err
	}

	// 连通性自检：二进制可执行 + 可探测版本
	version, ok := e.probeRuntimeVersion(ctx, p.Type, binPath, p.Args)

	e.mu.Lock()
	if e.State.Runtimes == nil {
		e.State.Runtimes = map[string]RuntimeEntry{}
	}
	e.State.Runtimes[rtID] = RuntimeEntry{
		Type: p.Type, BinPath: binPath, Workspace: wsDir, Args: p.Args, Source: "registered",
	}
	e.Home.SaveState(e.State)
	e.mu.Unlock()

	go e.syncRuntimes(context.Background())

	e.Log.Info("runtime created", "runtime", rtID, "type", p.Type, "bin", binPath, "selfcheck", ok)
	data, _ := structToMap(map[string]any{
		"runtime_id": rtID, "type": p.Type, "bin": binPath, "version": version,
		"workspace": wsDir, "status": map[bool]string{true: "online", false: "offline"}[ok],
	})
	return data, nil
}

// handleRuntimeRemove runtime.remove：删除运行时（清 workspace + state + 同步移除）。
func (e *Engine) handleRuntimeRemove(ctx context.Context, payload map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	var p proto.RuntimeRemovePayload
	if err := json.Unmarshal(raw, &p); err != nil || p.RuntimeID == "" {
		return nil, errors.New("need runtime_id")
	}
	e.mu.Lock()
	rt, exists := e.State.Runtimes[p.RuntimeID]
	e.mu.Unlock()
	if !exists {
		return nil, fmt.Errorf("runtime %s 不存在", p.RuntimeID)
	}
	_ = os.RemoveAll(rt.Workspace)
	e.mu.Lock()
	delete(e.State.Runtimes, p.RuntimeID)
	e.Home.SaveState(e.State)
	e.mu.Unlock()

	go e.syncRuntimesRemoved(context.Background(), p.RuntimeID)
	e.Log.Info("runtime removed", "runtime", p.RuntimeID)
	data, _ := structToMap(map[string]any{"removed": p.RuntimeID})
	return data, nil
}

// syncRuntimes 周期同步运行时列表。
func (e *Engine) syncRuntimes(ctx context.Context) {
	e.syncRuntimesRemoved(ctx)
}

func (e *Engine) syncRuntimesRemoved(ctx context.Context, removed ...string) {
	e.mu.Lock()
	ws := e.currentConn
	e.mu.Unlock()
	if ws == nil {
		return
	}
	rts := e.buildRuntimes(ctx)
	msg, err := json.Marshal(proto.RuntimeSyncMsg{Type: proto.TypeRuntimeSync, Runtimes: rts, Removed: removed})
	if err != nil {
		return
	}
	e.Log.Debug("runtime.sync sending", "count", len(rts), "removed", len(removed))
	if err := e.writeWS(ws, msg); err != nil {
		e.Log.Warn("runtime.sync send failed", "err", err)
	}
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
