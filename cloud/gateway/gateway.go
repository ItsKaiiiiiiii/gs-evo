// Package gateway 是 WS 接入端点：连接-设备绑定、消息路由、补发队列、吊销踢线（§5.2 Gateway）。
package gateway

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

const (
	sendBuffer    = 64
	readDeadline  = 90 * time.Second // 3× 心跳间隔（§6.1）
	writeDeadline = 10 * time.Second
	pingInterval  = 30 * time.Second
)

// ResultHandler cmd_result 汇聚回调（api 层接到 skillsvc/tasksvc）。
type ResultHandler func(deviceID string, res *proto.CmdResult)

// TaskEventHandler task.event 汇聚回调（api 层落库）。
type TaskEventHandler func(deviceID string, ev *proto.TaskEvent)

// PendingLoader 重连补发加载器（api 层接 store）。
type PendingLoader func(deviceID string) ([]*store.Command, error)

type conn struct {
	ws       *websocket.Conn
	send     chan []byte
	deviceID string
	closeCh  chan struct{}
	closed   sync.Once
	// send 通道从不 close（避免并发 send panic）；写循环只看 closeCh
}

type Gateway struct {
	store       *store.Store
	log         *slog.Logger
	mu          sync.RWMutex
	conns       map[string]*conn
	rejectUntil time.Time // 维护模式：拒绝新连接（不掐已建立的）
	OnResult    ResultHandler
	OnTaskEvent TaskEventHandler
	LoadPend    PendingLoader
}

// RejectNewConns 维护模式：duration 内拒绝新 WS 连接（已建立的不断）。
func (g *Gateway) RejectNewConns(duration time.Duration) {
	g.mu.Lock()
	g.rejectUntil = time.Now().Add(duration)
	g.mu.Unlock()
}

// Rejecting 是否处于维护模式。
func (g *Gateway) Rejecting() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return time.Now().Before(g.rejectUntil)
}

func New(st *store.Store, log *slog.Logger) *Gateway {
	return &Gateway{store: st, log: log, conns: make(map[string]*conn)}
}

// ServeConn 接管已鉴权的连接（upgrade 与 Bearer 校验在 api 层完成）。
func (g *Gateway) ServeConn(deviceID string, ws *websocket.Conn) {
	c := &conn{ws: ws, send: make(chan []byte, sendBuffer), deviceID: deviceID, closeCh: make(chan struct{})}

	g.mu.Lock()
	if old, ok := g.conns[deviceID]; ok && old != c { // 同设备旧连接（重连竞态）：踢旧保新
		g.mu.Unlock()
		g.closeConn(old, "replaced by new connection")
		g.mu.Lock()
	}
	g.conns[deviceID] = c
	g.mu.Unlock()
	_ = g.store.SetOnline(deviceID)
	defer func() {
		g.mu.Lock()
		if g.conns[deviceID] == c {
			delete(g.conns, deviceID)
		}
		g.mu.Unlock()
		g.closeConn(c, "connection closed")
		_ = g.store.SetOffline(deviceID)
	}()

	go g.writeLoop(c)
	defer g.stopWriteLoop(c)
	_ = ws.SetReadDeadline(time.Now().Add(readDeadline))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(readDeadline))
	})

	for {
		mt, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		_ = ws.SetReadDeadline(time.Now().Add(readDeadline))
		_ = g.store.TouchSeen(deviceID, true) // 任意帧刷新 last_seen（§6.1）
		if !g.handleFrame(c, raw) {
			return
		}
	}
}

// handleFrame 返回 false 表示连接应关闭。
func (g *Gateway) handleFrame(c *conn, raw []byte) bool {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return true // 丢坏帧，不断连
	}
	switch head.Type {
	case proto.TypeHello:
		g.handleHello(c, raw)
	case proto.TypePing:
		g.sendJSON(c, &proto.Pong{Type: proto.TypePong, TS: time.Now()})
	case proto.TypeCmdResult:
		var res proto.CmdResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return true
		}
		cmd, err := g.store.AckCommand(res.CmdID, res.OK, res.Data, res.Error)
		if err != nil {
			g.log.Warn("ack command failed", "cmd_id", res.CmdID, "err", err)
		}
		if g.OnResult != nil && cmd != nil {
			g.OnResult(cmd.DeviceID, &res)
		}
	case proto.TypeTaskEvent:
		var ev proto.TaskEvent
		if err := json.Unmarshal(raw, &ev); err != nil || ev.TaskID == "" {
			return true
		}
		if g.OnTaskEvent != nil {
			g.OnTaskEvent(c.deviceID, &ev)
		}
	case proto.TypeRuntimeSync:
		var sync proto.RuntimeSyncMsg
		if err := json.Unmarshal(raw, &sync); err == nil {
			g.log.Info("runtime.sync received", "device", c.deviceID, "count", len(sync.Runtimes), "removed", len(sync.Removed))
			for _, rt := range sync.Runtimes {
				source := rt.Source
				if source == "" {
					source = "probed"
				}
				if err := g.store.UpsertRuntime(c.deviceID, rt, source); err != nil {
					g.log.Warn("runtime upsert failed", "err", err, "rt", rt.RuntimeID)
				}
			}
			for _, rid := range sync.Removed {
				_ = g.store.DeleteRuntime(c.deviceID, rid)
			}
		} else {
			g.log.Warn("runtime.sync unmarshal failed", "err", err)
		}
	case proto.TypeKick:
		// 客户端不会发 kick
	}
	return true
}

// handleHello 对账：落库运行时档案 + 补发全部未 ack 指令（§6.4/§7.1）。
func (g *Gateway) handleHello(c *conn, raw []byte) {
	var h proto.Hello
	if err := json.Unmarshal(raw, &h); err == nil {
		agentsJSON, _ := json.Marshal(h.Agents)
		rootsJSON, _ := json.Marshal(h.Roots)
		_ = g.store.UpdateRuntimeFull(c.deviceID, h.Device.Hostname, h.Device.OS, h.Device.Arch, h.Device.WorkerVersion, agentsJSON, rootsJSON, h.Paused)
		g.log.Info("hello runtimes received", "device", c.deviceID, "count", len(h.Runtimes))
		for _, rt := range h.Runtimes {
			source := rt.Source
			if source == "" {
				source = "probed"
			}
			if err := g.store.UpsertRuntime(c.deviceID, rt, source); err != nil {
				g.log.Warn("runtime upsert failed", "device", c.deviceID, "runtime", rt.RuntimeID, "err", err)
			}
		}
	}

	welcome := proto.HelloOK{Type: proto.TypeHelloOK, ServerTime: time.Now()}
	if g.LoadPend != nil {
		if cmds, err := g.LoadPend(c.deviceID); err == nil {
			for _, cmd := range cmds {
				welcome.PendingCommands = append(welcome.PendingCommands, cmd.ToProto())
			}
		}
	}
	g.sendJSON(c, &welcome)
	// 补发即视为已送达一次（at-least-once：客户端幂等去重）
	for _, cmd := range welcome.PendingCommands {
		_ = g.store.MarkSent(cmd.CmdID)
	}
}

func (g *Gateway) writeLoop(c *conn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case msg := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeDeadline))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				g.stopWriteLoop(c)
				_ = c.ws.Close()
				return
			}
		case <-ticker.C: // WS 协议层 ping（§6.1 优先方式）
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeDeadline))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				g.stopWriteLoop(c)
				_ = c.ws.Close()
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

func (g *Gateway) stopWriteLoop(c *conn) {
	c.closed.Do(func() { close(c.closeCh) })
}

func (g *Gateway) closeConn(c *conn, reason string) {
	g.stopWriteLoop(c) // 写循环退出 + 唤醒 readLoop 的下一轮超时
	_ = c.ws.Close()   // ReadMessage 立即返回错误
	_ = reason
}

func (g *Gateway) sendJSON(c *conn, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- b:
	default: // 慢消费者：丢弃（cmd 有补发机制兜底；心跳丢帧无碍）
	}
}

// TryDeliver 设备在线时投递指令；返回是否已推送。
func (g *Gateway) TryDeliver(deviceID string, cmd *proto.Cmd) bool {
	g.mu.RLock()
	c := g.conns[deviceID]
	g.mu.RUnlock()
	if c == nil {
		return false
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return false
	}
	select {
	case c.send <- b:
		return true
	default:
		return false
	}
}

// Kick 吊销踢线（§6.7：比 multica 更强——事务提交后主动断连）。
func (g *Gateway) Kick(deviceID, reason string) {
	g.mu.RLock()
	c := g.conns[deviceID]
	g.mu.RUnlock()
	if c == nil {
		return
	}
	g.sendJSON(c, &proto.Kick{Type: proto.TypeKick, Reason: reason})
	time.Sleep(200 * time.Millisecond) // 给 kick 帧留写出窗口
	g.closeConn(c, "kicked: "+reason)
}

// IsOnline 设备当前是否有活跃连接。
func (g *Gateway) IsOnline(deviceID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.conns[deviceID] != nil
}

// StartExpirySweeper 周期把过期指令置 expired（§6.2）。
func (g *Gateway) StartExpirySweeper(interval time.Duration, stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if n, err := g.store.ExpireStale(); err != nil {
					g.log.Warn("expire sweep failed", "err", err)
				} else if n > 0 {
					g.log.Info("expired stale commands", "count", n)
				}
				if n, err := g.store.ExpireStaleTasks(); err != nil {
					g.log.Warn("task expire sweep failed", "err", err)
				} else if n > 0 {
					g.log.Info("expired stale tasks", "count", n)
				}
			case <-stop:
				return
			}
		}
	}()
}
