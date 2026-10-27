package worker

// 连接管理 + 统一指令分发（§5.1 Connection Manager / Command Dispatcher）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

const (
	pingInterval  = 30 * time.Second
	readDeadline  = 90 * time.Second // 3× 心跳
	maxBackoff    = 30 * time.Second
	renewInterval = 24 * time.Hour
)

// Engine 一次运行周期的全部状态。
type Engine struct {
	Home  Home
	Cfg   *Config
	State *State
	Log   *slog.Logger

	// ExecHook 测试钩子：指令真正执行完成后回调（幂等回放不触发）。
	ExecHook func(cmdID, cmdType string)

	handlers    map[string]HandlerFunc
	sem         chan struct{} // 并发上限
	mu          sync.Mutex    // 保护 State / tasks / currentConn
	tasks       map[string]*activeTask
	currentConn *websocket.Conn
	inflight    map[string]chan struct{} // 执行中的 cmd_id（防同毫秒重复投递双跑）
}

// HandlerFunc 指令处理器：payload → data / error。
type HandlerFunc func(ctx context.Context, payload map[string]any) (map[string]any, error)

func NewEngine(h Home, log *slog.Logger) (*Engine, error) {
	cfg, err := h.LoadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, errors.New("no device token in config (run `worker ctl login`)")
	}
	e := &Engine{
		Home: h, Cfg: cfg, Log: log,
		State:    h.LoadState(),
		handlers: map[string]HandlerFunc{},
		tasks:    map[string]*activeTask{},
		inflight: map[string]chan struct{}{},
		sem:      make(chan struct{}, cfg.MaxConcurrency),
	}
	e.register(proto.CmdAgentProbe, e.handleProbe)
	e.register(proto.CmdSkillsScan, e.Scan)
	e.register(proto.CmdSkillsCollect, e.handleCollect)
	e.register(proto.CmdSkillsInstall, e.handleInstall)
	e.register(proto.CmdTaskStart, e.handleTaskStart)
	e.register(proto.CmdTaskCancel, e.handleTaskCancel)
	e.register(proto.CmdTaskEventsReplay, e.handleEventsReplay)
	return e, nil
}

func (e *Engine) register(cmdType string, h HandlerFunc) { e.handlers[cmdType] = h }

// Run 主循环：连接 → 服务 → 断线退避重连（§6.1）。
func (e *Engine) Run(ctx context.Context) error {
	go e.renewLoop(ctx)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		connected, err := e.serveOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			e.Log.Warn("connection ended", "err", err)
		}
		if connected {
			backoff = time.Second // 连接过就重置退避（§6.1：成功建连即恢复）
		}
		// 指数退避 + 抖动（1s→30s，§6.1）
		jitter := time.Duration(rand.Int63n(int64(backoff)/4 + 1))
		sleep := backoff + jitter
		e.Log.Info("reconnecting", "sleep", sleep.String())
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// serveOnce 单次连接生命周期；返回是否成功建连。
func (e *Engine) serveOnce(ctx context.Context) (bool, error) {
	wsURL, err := e.wsEndpoint()
	if err != nil {
		return false, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{"Authorization": {"Bearer " + e.Cfg.Token}}
	ws, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			// 401：不退出不清配置，持续提醒（§6.7 生命周期）
			e.Log.Warn("auth rejected (401) — run `worker ctl login` to re-enroll")
			return false, nil
		}
		return false, err
	}
	defer ws.Close()
	e.mu.Lock()
	e.currentConn = ws
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.currentConn == ws {
			e.currentConn = nil
		}
		e.mu.Unlock()
	}()
	// ctx 取消（进程退出）时主动断连，解除 ReadMessage 阻塞
	stopOnCtx := make(chan struct{})
	defer close(stopOnCtx)
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-stopOnCtx:
		}
	}()
	e.Log.Info("connected", "server", e.Cfg.ServerURL)
	e.flushSpool(ws)

	// ping 心跳
	pingStop := make(chan struct{})
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				b, _ := json.Marshal(&proto.Ping{Type: proto.TypePing, TS: time.Now()})
				if werr := ws.WriteMessage(websocket.TextMessage, b); werr != nil {
					_ = ws.Close()
					return
				}
			case <-pingStop:
				return
			}
		}
	}()
	defer close(pingStop)

	// hello（§6.4）
	_ = ws.SetReadDeadline(time.Now().Add(readDeadline))
	hello, _ := e.buildHello()
	hb, _ := json.Marshal(hello)
	if werr := ws.WriteMessage(websocket.TextMessage, hb); werr != nil {
		return true, werr
	}

	// 读循环
	for {
		mt, raw, err := ws.ReadMessage()
		if err != nil {
			return true, err
		}
		if mt != websocket.TextMessage {
			continue
		}
		_ = ws.SetReadDeadline(time.Now().Add(readDeadline))
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &head) != nil {
			continue
		}
		switch head.Type {
		case proto.TypeHelloOK:
			var ok proto.HelloOK
			if json.Unmarshal(raw, &ok) == nil {
				for _, cmd := range ok.PendingCommands {
					e.dispatch(ws, cmd) // 重连补发：幂等缓存挡重复执行
				}
				e.flushSpool(ws)
			}
		case proto.TypeCmd:
			var cmd proto.Cmd
			if json.Unmarshal(raw, &cmd) == nil {
				e.Log.Debug("cmd frame received", "cmd_id", cmd.CmdID, "seq", cmd.CmdSeq, "type", cmd.CmdType)
				e.dispatch(ws, &cmd)
			}
		case proto.TypeKick:
			var k proto.Kick
			_ = json.Unmarshal(raw, &k)
			e.Log.Warn("kicked by server", "reason", k.Reason)
			return true, fmt.Errorf("kicked: %s", k.Reason)
		case proto.TypePong:
			// 心跳应答
		}
	}
}

// dispatch 幂等 → 过期检查 → 并发上限 → 执行 → 结果落缓存/spool/发送（§5.2 at-least-once+幂等）。
func (e *Engine) dispatch(ws *websocket.Conn, cmd *proto.Cmd) {
	// 1. 幂等缓存命中：直接回放缓存结果，不执行
	e.mu.Lock()
	if cached, ok := e.State.Results[cmd.CmdID]; ok {
		e.mu.Unlock()
		e.Log.Info("duplicate command (idempotent replay)", "cmd_id", cmd.CmdID, "type", cmd.CmdType)
		res := &proto.CmdResult{Type: proto.TypeCmdResult, CmdID: cmd.CmdID, CmdSeq: cmd.CmdSeq,
			OK: cached.OK, Data: cached.Data, Error: cached.Error}
		e.sendResult(ws, res) // 重新走 spool（可能此前未送达）
		return
	}
	if done, busy := e.inflight[cmd.CmdID]; busy {
		e.mu.Unlock()
		<-done // 等首跑出结果并回放缓存（下方重查），保证恰好一次执行
		e.dispatch(ws, cmd)
		return
	}
	done := make(chan struct{})
	e.inflight[cmd.CmdID] = done
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.inflight, cmd.CmdID)
		e.mu.Unlock()
		close(done)
	}()

	// 2. 过期指令直接拒绝执行（§6.2）
	if cmd.ExpiresAt != nil && time.Now().After(*cmd.ExpiresAt) {
		e.Log.Warn("expired command dropped", "cmd_id", cmd.CmdID, "type", cmd.CmdType)
		e.sendResult(ws, &proto.CmdResult{Type: proto.TypeCmdResult, CmdID: cmd.CmdID, CmdSeq: cmd.CmdSeq,
			OK: false, Error: "expired"})
		return
	}

	handler := e.handlers[cmd.CmdType]
	if handler == nil {
		e.sendResult(ws, &proto.CmdResult{Type: proto.TypeCmdResult, CmdID: cmd.CmdID, CmdSeq: cmd.CmdSeq,
			OK: false, Error: "unsupported"}) // 反向兼容：未知指令（§11）
		return
	}

	select {
	case e.sem <- struct{}{}:
	case <-time.After(10 * time.Minute):
		e.sendResult(ws, &proto.CmdResult{Type: proto.TypeCmdResult, CmdID: cmd.CmdID, CmdSeq: cmd.CmdSeq,
			OK: false, Error: "busy: concurrency limit"})
		return
	}

	go func() {
		defer func() { <-e.sem }()
		defer func() {
			if r := recover(); r != nil {
				e.Log.Error("handler panic", "cmd_id", cmd.CmdID, "panic", r)
				e.sendResult(ws, &proto.CmdResult{Type: proto.TypeCmdResult, CmdID: cmd.CmdID, CmdSeq: cmd.CmdSeq,
					OK: false, Error: fmt.Sprintf("internal panic: %v", r)})
			}
		}()

		timeout := 30 * time.Minute
		if cmd.CmdType == proto.CmdTaskStart {
			timeout = hardTaskDeadline // 任务自身有看门狗，这里是兜底硬顶
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		data, err := handler(ctx, cmd.Payload)
		if err != nil {
			e.Log.Warn("command failed", "cmd_id", cmd.CmdID, "type", cmd.CmdType, "err", err)
		} else {
			e.Log.Info("command done", "cmd_id", cmd.CmdID, "type", cmd.CmdType)
		}
		if e.ExecHook != nil {
			e.ExecHook(cmd.CmdID, cmd.CmdType)
		}
		res := &proto.CmdResult{Type: proto.TypeCmdResult, CmdID: cmd.CmdID, CmdSeq: cmd.CmdSeq,
			OK: err == nil, Data: data}
		if err != nil {
			res.Error = err.Error()
		}
		// 3. 落幂等缓存（进程重启后仍可去重）
		e.mu.Lock()
		e.State.Results[cmd.CmdID] = ResultRec{CmdSeq: cmd.CmdSeq, OK: res.OK, Data: res.Data,
			Error: res.Error, At: nowRFC3339()}
		if cmd.CmdSeq > e.State.MaxSeq {
			e.State.MaxSeq = cmd.CmdSeq
		}
		e.Home.SaveState(e.State)
		e.mu.Unlock()

		// 4. spool 先落盘，送达后删除（§5.1 Result Spool）
		e.sendResult(ws, res)
	}()
}

// sendResult 结果先写 spool 再发送，发送成功即删（§7.4）。
func (e *Engine) sendResult(ws *websocket.Conn, res *proto.CmdResult) {
	b, err := json.Marshal(res)
	if err != nil {
		return
	}
	spoolPath := filepath.Join(e.Home.SpoolDir(), res.CmdID+".json")
	if _, statErr := os.Stat(spoolPath); statErr != nil {
		_ = os.MkdirAll(e.Home.SpoolDir(), 0o700)
		_ = os.WriteFile(spoolPath, b, 0o600)
	}
	_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if werr := ws.WriteMessage(websocket.TextMessage, b); werr == nil {
		_ = os.Remove(spoolPath)
	} else {
		e.Log.Warn("result send failed, kept in spool", "cmd_id", res.CmdID, "err", werr)
	}
}

// flushSpool 重连后补传未送达结果（§6.1）。
func (e *Engine) flushSpool(ws *websocket.Conn) {
	entries, err := os.ReadDir(e.Home.SpoolDir())
	if err != nil {
		return
	}
	var names []string
	for _, f := range entries {
		if strings.HasSuffix(f.Name(), ".json") {
			names = append(names, f.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(e.Home.SpoolDir(), name))
		if err != nil {
			continue
		}
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := ws.WriteMessage(websocket.TextMessage, b); err != nil {
			return // 连接坏了，留给下次
		}
		_ = os.Remove(filepath.Join(e.Home.SpoolDir(), name))
	}
}

func (e *Engine) wsEndpoint() (string, error) {
	u, err := url.Parse(e.Cfg.ServerURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/v1/agent-ws"
	return u.String(), nil
}

func (e *Engine) buildHello() (*proto.Hello, error) {
	id, err := e.Home.DeviceID()
	if err != nil {
		return nil, err
	}
	_ = id
	host, _ := os.Hostname()
	h := &proto.Hello{
		Type:         proto.TypeHello,
		ProtoVersion: proto.ProtoVersion,
		Capabilities: proto.SupportedCommands,
		Device: proto.DeviceInfo{
			Hostname: host, OS: goos(), Arch: goarch(), WorkerVersion: WorkerVersion,
		},
		LastAckedCmdSeq: e.State.MaxSeq,
	}
	h.Agents = e.ProbeAgents(context.Background())
	// 已装 skill 清单（完整列表，§6.4）
	installed := map[string]string{}
	for name, rec := range e.State.Installed {
		installed[name] = rec.ContentHash
	}
	for _, root := range e.expandRoots(nil) {
		if ms, err := skillScanDir(root, installed); err == nil {
			for _, m := range ms {
				h.Skills = append(h.Skills, proto.SkillBrief{
					Name: m.Name, Version: m.Version, ContentHash: m.ContentHash, LocalState: m.LocalState,
				})
			}
		}
	}
	return h, nil
}

// renewLoop 24h 周期原地续期（§6.7；启动时也做一次 preflight）。
func (e *Engine) renewLoop(ctx context.Context) {
	do := func() {
		req, err := http.NewRequestWithContext(ctx, "POST",
			strings.TrimSuffix(e.Cfg.ServerURL, "/")+"/api/v1/devices/token/renew", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+e.Cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			e.Log.Warn("token renewal rejected — run `worker ctl login`")
			return
		}
		var out struct {
			Renewed   bool   `json:"renewed"`
			ExpiresAt string `json:"expires_at"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) == nil {
			e.Log.Info("token renew checked", "renewed", out.Renewed, "expires_at", out.ExpiresAt)
		}
	}
	do()
	t := time.NewTicker(renewInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			do()
		case <-ctx.Done():
			return
		}
	}
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// Rollback 本地秒回滚的导出入口（CLI 与测试共用）。
func (e *Engine) Rollback(name string) error { return e.rollbackLocal(name) }
