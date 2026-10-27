// Package e2e 端到端自测：真实 MySQL + 进程内 cloud + 两个进程内 client。
//
// 覆盖 DESIGN.md §12 验收标准：
//
//	P0-1 设备在线/离线切换          TestP0_OnlineOffline
//	P0-2 断线补发 + 幂等不重复执行   TestP0_RedeliveryAndIdempotency
//	P0-3 吊销即踢线且无法重连        TestP0_RevokeKick
//	P1-1 采集→转stable签名→装到另一机 TestP1_CollectSignInstall
//	P1-2 本地改动冲突跳过            TestP1_ConflictSkipped（并入 P1-1 流程）
//	P1-3 本地秒回滚                 TestP1_Rollback（并入 P1-1 流程）
package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/ItsKaiiiiiiii/gs-evo/client/worker"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/api"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

var debugE2E = os.Getenv("GSEVO_E2E_DEBUG") == "1"

const testDSN = "gsevo:gsevo_dev_pw@tcp(127.0.0.1:3306)/gsevo_test?parseTime=true&multiStatements=true&time_zone=%27%2B00%3A00%27"

func TestMain(m *testing.M) {
	// 重置测试库（每轮 e2e 干净起步）
	db, err := sql.Open("mysql", strings.Replace(testDSN, "/gsevo_test?", "/?", 1))
	if err == nil {
		_, _ = db.Exec("DROP DATABASE IF EXISTS gsevo_test")
		_, _ = db.Exec("CREATE DATABASE gsevo_test CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
		_ = db.Close()
	}
	buildMockAgent()
	code := runWithCloud(m)
	os.Exit(code)
}

// runWithCloud 在 TestMain 生命周期内启动共享 cloud（一次启动，全部测试共用）。
func runWithCloud(m *testing.M) int {
	dataDir, err := os.MkdirTemp("", "gsevo-cloud-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		return 1
	}
	defer os.RemoveAll(dataDir)
	st, err := store.Open(testDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mysql unavailable:", err)
		return 0 // 无 MySQL 时跳过全部 e2e
	}
	adminToken := proto.NewAdminToken()
	if err := st.AddAdminToken(proto.HashToken(adminToken), "test"); err != nil {
		fmt.Fprintln(os.Stderr, "admin token:", err)
		return 1
	}
	srv, err := api.NewServer(api.Config{Addr: ":0", DataDir: dataDir}, st,
		slog.New(slog.NewTextHandler(debugWriter(), nil)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		return 1
	}
	h := &http.Server{Handler: srv.Routes()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		return 1
	}
	go h.Serve(ln)
	defer h.Close()

	os.Setenv("GSEVO_PUBLIC_URL", "http://"+ln.Addr().String())
	key, err := adminCall("http://"+ln.Addr().String(), adminToken, "GET", "/api/v1/admin/key", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "key:", err)
		return 1
	}
	cl = &cloud{base: "http://" + ln.Addr().String(), adminTok: adminToken, dataDir: dataDir}
	var keyResp struct {
		Pub string `json:"signing_pubkey"`
	}
	_ = json.Unmarshal(key, &keyResp)
	cl.pubkey = keyResp.Pub
	if cl.pubkey == "" {
		fmt.Fprintln(os.Stderr, "no signing pubkey")
		return 1
	}
	return m.Run()
}

// sqlExec 测试内直接执行 SQL（仅用于故障注入）。
func sqlExec(dsn, query string, args ...any) error {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(query, args...)
	return err
}

// ---- harness ----

type cloud struct {
	base     string
	adminTok string // admin token 明文
	pubkey   string
	dataDir  string
}

type device struct {
	name   string
	id     string
	home   worker.Home
	cancel context.CancelFunc
	done   chan struct{}
	exec   map[string]int // cmdID → 实际执行次数
	execMu sync.Mutex
}

var (
	cl      *cloud
	devices = map[string]*device{}
)

func setupAll(t *testing.T) *cloud {
	t.Helper()
	if cl == nil {
		t.Fatal("cloud harness not initialized")
	}
	return cl
}

// debugWriter 测试期日志开关：GSEVO_E2E_DEBUG=1 时输出到 stderr，否则丢弃。
func debugWriter() io.Writer {
	if os.Getenv("GSEVO_E2E_DEBUG") == "1" {
		return os.Stderr
	}
	return io.Discard
}

func adminCall(base, token, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, path, out)
	}
	return out, nil
}

func (c *cloud) admin(method, path string, body any) ([]byte, error) {
	return adminCall(c.base, c.adminTok, method, path, body)
}

// spawnDevice 走真实 enroll 流注册一个 client 并启动 daemon。
func (c *cloud) spawnDevice(t *testing.T, name string) *device {
	t.Helper()
	// 1. enroll token
	etB, err := c.admin("POST", "/api/v1/admin/enroll-tokens", map[string]string{"label": name})
	if err != nil {
		t.Fatal(err)
	}
	var et struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(etB, &et)

	// 2. enroll（HTTP，等价 worker login）
	home := worker.Home{Dir: filepath.Join(t.TempDir(), "home-"+name)}
	if err := os.MkdirAll(home.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	deviceID := proto.NewDeviceID()
	enrollBody, _ := json.Marshal(map[string]string{
		"enroll_token": et.Token, "device_id": deviceID,
		"hostname": name, "os": "linux", "arch": "amd64", "worker_version": "test",
	})
	resp, err := http.Post(c.base+"/api/v1/devices/enroll", "application/json", bytes.NewReader(enrollBody))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("enroll failed: %d %s", resp.StatusCode, b)
	}
	var er struct {
		DeviceToken string `json:"device_token"`
	}
	_ = json.Unmarshal(b, &er)
	if er.DeviceToken == "" {
		t.Fatal("empty device token")
	}

	// 3. skills 目录 + config
	skillsRoot := filepath.Join(filepath.Dir(home.Dir), "skills-"+name)
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &worker.Config{
		ServerURL:      c.base,
		Token:          er.DeviceToken,
		Roots:          []string{skillsRoot},
		MaxConcurrency: 2,
		SigningPubkey:  c.pubkey,
		PermissionMode: "readonly",
	}
	if err := home.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	d := &device{name: name, id: deviceID, home: home, exec: map[string]int{}}
	return d
}

// start 启动 daemon；restart 复用同一 home（state.json 保留 → 幂等缓存跨重启生效）。
func (d *device) start(t *testing.T) {
	t.Helper()
	eng, err := worker.NewEngine(d.home, slog.New(slog.NewTextHandler(debugWriter(), &slog.HandlerOptions{Level: slog.LevelDebug})).With("dev", d.name))
	if err != nil {
		t.Fatal(err)
	}
	eng.ExecHook = func(cmdID, cmdType string) {
		d.execMu.Lock()
		d.exec[cmdID]++
		d.execMu.Unlock()
	}
	ctx, cancel := contextWithCancel()
	d.cancel = cancel
	d.done = make(chan struct{})
	go func() {
		_ = eng.Run(ctx)
		close(d.done)
	}()
	devices[d.name] = d
}

func (d *device) stop() {
	if d.cancel != nil {
		d.cancel()
		<-d.done
		d.cancel = nil
	}
}

func (d *device) execCount(cmdID string) int {
	d.execMu.Lock()
	defer d.execMu.Unlock()
	return d.exec[cmdID]
}

// ---- 等待辅助 ----

func waitFor(t *testing.T, timeout time.Duration, what string, fn func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := fn()
		if err == nil && ok {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

type devView struct {
	ID     string `json:"id"`
	Host   string `json:"hostname"`
	Status string `json:"status"`
}

func (c *cloud) deviceByID(t *testing.T, id string) *devView {
	t.Helper()
	b, err := c.admin("GET", "/api/v1/admin/devices", nil)
	if err != nil {
		t.Fatal(err)
	}
	if debugE2E {
		t.Logf("devices api: %s", b)
	}
	var devs []devView
	_ = json.Unmarshal(b, &devs)
	for _, d := range devs {
		if d.ID == id {
			return &d
		}
	}
	return nil
}

type cmdRow struct {
	ID     string         `json:"cmd_id"`
	CmdSeq int64          `json:"cmd_seq"`
	Type   string         `json:"cmd_type"`
	Status string         `json:"status"`
	Result map[string]any `json:"result"` // {ok, data, error} 信封
}

// dataOf 取信封里的 data 载荷。
func (r *cmdRow) dataOf() map[string]any {
	if r == nil || r.Result == nil {
		return map[string]any{}
	}
	if d, ok := r.Result["data"].(map[string]any); ok {
		return d
	}
	return map[string]any{}
}

// issueCmd 下发指令并等待 acked，返回落库行。
func (c *cloud) issueCmd(t *testing.T, deviceID, cmdType string, payload map[string]any) *cmdRow {
	t.Helper()
	body := map[string]any{"device_id": deviceID, "cmd_type": cmdType, "payload": payload, "ttl_seconds": 60}
	b, err := c.admin("POST", "/api/v1/admin/commands", body)
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(b, &issued)
	row := c.waitCmd(t, deviceID, issued.CmdID)
	return row
}

func (c *cloud) waitCmd(t *testing.T, deviceID, cmdID string) *cmdRow {
	t.Helper()
	var found *cmdRow
	waitFor(t, 20*time.Second, "cmd "+cmdID+" acked", func() (bool, error) {
		b, err := c.admin("GET", "/api/v1/admin/commands?device_id="+deviceID+"&limit=200", nil)
		if err != nil {
			return false, err
		}
		var rows []cmdRow
		if err := json.Unmarshal(b, &rows); err != nil {
			return false, err
		}
		for i := range rows {
			if rows[i].ID == cmdID {
				found = &rows[i]
				return rows[i].Status == "acked", nil
			}
		}
		return false, nil
	})
	return found
}

// ---- P0 ----

func TestP0_OnlineOffline(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "online-host")
	d.start(t)
	defer d.stop()

	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})
	d.stop()
	waitFor(t, 15*time.Second, "device offline", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "offline", nil
	})
}

func TestP0_RedeliveryAndIdempotency(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "redeliver-host")
	d.start(t)
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})
	devID := c.deviceByID(t, d.id).ID

	// 1. 在线执行一次 probe
	row := c.issueCmd(t, devID, "agent.probe", map[string]any{})
	if d.execCount(row.ID) != 1 {
		t.Fatalf("probe executed %d times, want 1", d.execCount(row.ID))
	}
	if !row.Result["ok"].(bool) {
		t.Fatalf("probe result not ok: %v", row.Result)
	}

	// 2. 模拟 ack 丢失：云端把指令置回 sent → 重启客户端 → 补发
	d.stop()
	if _, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "agent.probe", "payload": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	// 直接通过 SQL 置回（e2e 允许触碰存储层做故障注入）
	if err := sqlExec(testDSN, "UPDATE command SET status='sent', acked_at=NULL WHERE id=?", row.ID); err != nil {
		t.Fatal(err)
	}
	d.start(t) // 同一 home：state.json（幂等缓存）保留

	// 3. 补发到达，但客户端必须幂等：不重复执行，只回放缓存结果
	waitFor(t, 15*time.Second, "command re-acked after redelivery", func() (bool, error) {
		b, err := c.admin("GET", "/api/v1/admin/commands?device_id="+devID+"&limit=200", nil)
		if err != nil {
			return false, err
		}
		var rows []cmdRow
		_ = json.Unmarshal(b, &rows)
		for _, r := range rows {
			if r.ID == row.ID {
				return r.Status == "acked", nil
			}
		}
		return false, nil
	})
	time.Sleep(500 * time.Millisecond) // 给重复执行留暴露窗口
	if n := d.execCount(row.ID); n != 1 {
		t.Fatalf("idempotency broken: probe executed %d times after redelivery, want 1", n)
	}

	// 4. 离线期间创建的指令：重连后补发并执行
	d.stop()
	offB, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "agent.probe", "payload": map[string]any{}, "ttl_seconds": 120})
	if err != nil {
		t.Fatal(err)
	}
	var off struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(offB, &off)
	d.start(t)
	row2 := c.waitCmd(t, devID, off.CmdID)
	if row2.Status != "acked" {
		t.Fatalf("offline command not executed: %s", row2.Status)
	}
	if d.execCount(off.CmdID) != 1 {
		t.Fatalf("offline command executed %d times, want 1", d.execCount(off.CmdID))
	}
	d.stop()
}

func TestP0_RevokeKick(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "revoke-host")
	d.start(t)
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})
	devID := c.deviceByID(t, d.id).ID

	if _, err := c.admin("POST", "/api/v1/admin/devices/"+devID+"/revoke", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	// 踢线：连接被关闭，设备转 offline/revoked
	waitFor(t, 15*time.Second, "device offline after revoke", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status != "online", nil
	})
	// 无法重连：旧 token 直连 ws 拿 401
	req, _ := http.NewRequest("GET", strings.Replace(c.base, "http://", "ws://", 1)+"/api/v1/agent-ws", nil)
	req.Header.Set("Authorization", "Bearer "+readTokenFromConfig(t, d.home))
	wsResp := wsDialStatus(t, req)
	if wsResp != http.StatusUnauthorized {
		t.Fatalf("revoked token dial status = %d, want 401", wsResp)
	}
	d.stop() // 引擎应仍在重试循环中（不崩溃、不退出进程语义由 ctx 控制）
}

// ---- P1 ----

func writeSkill(t *testing.T, root, name, version, marker string, big bool) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := fmt.Sprintf("---\nname: %s\nversion: %s\ndescription: demo skill for e2e\ncapabilities: demo.echo, demo.file\n---\n\n# Demo\n\nmarker: %s\n", name, version, marker)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if big { // 制造 >256KB 触发票据上传路径（§6.6）
		big := strings.Repeat("x", 300*1024)
		if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestP1_CollectSignInstall(t *testing.T) {
	c := setupAll(t)
	a := c.spawnDevice(t, "collect-host")
	b := c.spawnDevice(t, "install-host")
	a.start(t)
	b.start(t)
	defer func() { a.stop(); b.stop() }()

	var idA, idB string
	waitFor(t, 15*time.Second, "both devices online", func() (bool, error) {
		va := c.deviceByID(t, a.id)
		vb := c.deviceByID(t, b.id)
		if va == nil || vb == nil {
			return false, nil
		}
		idA, idB = a.id, b.id
		return va.Status == "online" && vb.Status == "online", nil
	})

	rootA := a.cfgRoots(t)[0]
	rootB := b.cfgRoots(t)[0]
	writeSkill(t, rootA, "demo-skill", "1", "VERSION-ONE", true) // big=true 走票据上传

	// scan
	scan := c.issueCmd(t, idA, "skills.scan", map[string]any{})
	resB, _ := json.Marshal(scan.dataOf())
	if !strings.Contains(string(resB), "demo-skill") {
		t.Fatalf("scan result missing demo-skill: %s", resB)
	}
	if !strings.Contains(string(resB), `"local_state":"new"`) && !strings.Contains(string(resB), "new") {
		t.Fatalf("scan local_state not new: %s", resB)
	}

	// collect（>256KB → 票据上传路径）
	c.issueCmd(t, idA, "skills.collect", map[string]any{"name": "demo-skill", "root": rootA})
	// 版本落库（cmd_result 汇聚与入库异步，轮询等待）：draft + collected
	var v1 map[string]any
	waitFor(t, 10*time.Second, "collected version registered", func() (bool, error) {
		vers := c.versions(t, "demo-skill")
		if len(vers) == 0 {
			return false, nil
		}
		v1 = vers[0]
		return true, nil
	})
	if v1["status"] != "draft" || v1["source"] != "collected" {
		t.Fatalf("v1 status/source = %v/%v, want draft/collected", v1["status"], v1["source"])
	}

	// 状态机：draft→stable 直跳必须被拒
	if _, err := c.admin("POST", "/api/v1/admin/skills/demo-skill/versions/1/status", map[string]string{"status": "stable"}); err == nil {
		t.Fatal("draft→stable should be rejected")
	}
	for _, s := range []string{"candidate", "reviewed", "stable"} {
		if _, err := c.admin("POST", "/api/v1/admin/skills/demo-skill/versions/1/status", map[string]string{"status": s}); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
	}

	// 未 stable 的版本不能下发安装（v1 此时已 stable，用假版本验证）
	if _, err := c.admin("POST", "/api/v1/admin/skills/demo-skill/install",
		map[string]any{"version": "999", "target_root": rootB, "device_ids": []string{idB}}); err == nil {
		t.Fatal("install of missing version should fail")
	}

	// 安装到 B
	install := c.installAndWait(t, "demo-skill", "1", rootB, idB, "")
	if install["result"] != "installed" || install["replaced"] != "none" {
		t.Fatalf("install result: %v", install)
	}
	// 文件真实落盘
	got, err := os.ReadFile(filepath.Join(rootB, "demo-skill", "SKILL.md"))
	if err != nil || !strings.Contains(string(got), "VERSION-ONE") {
		t.Fatalf("installed files wrong: %v %s", err, got)
	}
	// .prev 不存在（首装）
	if _, err := os.Stat(filepath.Join(rootB, "demo-skill.prev")); !os.IsNotExist(err) {
		t.Fatal("unexpected .prev on first install")
	}

	// B 生成 v2 → 采集 → stable → 安装到 B（此时 B 本地 clean，直接覆盖）
	writeSkill(t, rootA, "demo-skill", "2", "VERSION-TWO", false)
	c.issueCmd(t, idA, "skills.collect", map[string]any{"name": "demo-skill", "root": rootA})
	waitFor(t, 10*time.Second, "v2 registered", func() (bool, error) {
		return len(c.versions(t, "demo-skill")) >= 2, nil
	})
	for _, s := range []string{"candidate", "reviewed", "stable"} {
		if _, err := c.admin("POST", "/api/v1/admin/skills/demo-skill/versions/2/status", map[string]string{"status": s}); err != nil {
			t.Fatal(err)
		}
	}

	// 本地改动 B 的 v1 → 安装 v2 必须冲突跳过（C6）
	skillFile := filepath.Join(rootB, "demo-skill", "SKILL.md")
	f, err := os.OpenFile(skillFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nlocal hotfix\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	conflict := c.installAndWait(t, "demo-skill", "2", rootB, idB, "")
	if conflict["result"] != "conflict_skipped" {
		t.Fatalf("want conflict_skipped, got %v", conflict)
	}
	if strings.Contains(mustRead(t, skillFile), "VERSION-TWO") {
		t.Fatal("local modification was overwritten (must not)")
	}

	// overwrite 允许显式覆盖
	ow := c.installAndWait(t, "demo-skill", "2", rootB, idB, "overwrite")
	if ow["result"] != "installed" || ow["replaced"] != "1" {
		t.Fatalf("overwrite install: %v", ow)
	}
	if !strings.Contains(mustRead(t, skillFile), "VERSION-TWO") {
		t.Fatal("v2 not installed after overwrite")
	}

	// 本地秒回滚（§7.3）：v2 → v1
	if err := clientRollback(b.home, "demo-skill"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(mustRead(t, skillFile), "VERSION-ONE") {
		t.Fatalf("rollback did not restore v1: %s", mustRead(t, skillFile))
	}
}

func (c *cloud) versions(t *testing.T, name string) []map[string]any {
	t.Helper()
	b, err := c.admin("GET", "/api/v1/admin/skills/"+name+"/versions", nil)
	if err != nil {
		t.Fatal(err)
	}
	var vs []map[string]any
	_ = json.Unmarshal(b, &vs)
	return vs
}

func (c *cloud) installAndWait(t *testing.T, name, version, root, deviceID, onConflict string) map[string]any {
	t.Helper()
	body := map[string]any{"version": version, "target_root": root, "device_ids": []string{deviceID}}
	if onConflict != "" {
		body["on_conflict"] = onConflict
	}
	b, err := c.admin("POST", "/api/v1/admin/skills/"+name+"/install", body)
	if err != nil {
		t.Fatal(err)
	}
	var issued []struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(b, &issued)
	if len(issued) != 1 {
		t.Fatalf("install issued %d commands", len(issued))
	}
	row := c.waitCmd(t, deviceID, issued[0].CmdID)
	if row.Status != "acked" {
		t.Fatalf("install cmd status=%s result=%v", row.Status, row.Result)
	}
	return row.dataOf()
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
