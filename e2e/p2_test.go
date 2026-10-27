package e2e

// P2 任务通道 e2e（§12 验收标准）：
//	P2-1 云端下发 prompt，实时回传事件与结果     TestP2_TaskFlow
//	P2-2 task.cancel 杀干净进程树                TestP2_Cancel
//	P2-3 断网重连后结果不丢（spool）+ 事件补齐     TestP2_OfflineSpoolReplay
//	P2-4 看门狗超时                              TestP2_Timeout

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/client/worker"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

var mockAgentPath string
var workerBinPath string

func buildMockAgent() {
	out := filepath.Join(os.TempDir(), "gsevo-mockagent")
	cmd := exec.Command("go", "build", "-o", out, "./e2e/mockagent")
	cmd.Dir = repoRoot()
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build mockagent:", err)
		return
	}
	mockAgentPath = out
}

func buildWorkerBinary() {
	out := filepath.Join(os.TempDir(), "gsevo-gs-worker")
	cmd := exec.Command("go", "build", "-o", out, "./client")
	cmd.Dir = repoRoot()
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build worker:", err)
		return
	}
	workerBinPath = out
}

func repoRoot() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..")
}

func (c *cloud) createTask(t *testing.T, deviceID, prompt, mode string, timeoutS int) (string, *cmdRow) {
	t.Helper()
	taskID, cmdID := c.createTaskNoWait(t, deviceID, prompt, mode, timeoutS)
	row := c.waitCmd(t, deviceID, cmdID) // task.start 的 acked == 任务终态（§6.4）
	return taskID, row
}

// createTaskNoWait 只下发不等待（cancel 等需要任务尚在运行的场景用）。
func (c *cloud) createTaskNoWait(t *testing.T, deviceID, prompt, mode string, timeoutS int) (string, string) {
	t.Helper()
	body := map[string]any{"device_id": deviceID, "prompt": prompt,
		"agent_type": "gsagent", "timeout_s": timeoutS,
		"env": map[string]string{"MOCK_MODE": mode}}
	b, err := c.admin("POST", "/api/v1/admin/tasks", body)
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		TaskID string `json:"task_id"`
		CmdID  string `json:"cmd_id"`
	}
	_ = json.Unmarshal(b, &issued)
	return issued.TaskID, issued.CmdID
}

func (c *cloud) taskRow(t *testing.T, deviceID, id string) *struct {
	Status string         `json:"status"`
	Result map[string]any `json:"result"`
} {
	t.Helper()
	b, err := c.admin("GET", "/api/v1/admin/tasks?device_id="+deviceID+"&limit=500", nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID     string         `json:"id"`
		Status string         `json:"status"`
		Result map[string]any `json:"result"`
	}
	_ = json.Unmarshal(b, &rows)
	for i := range rows {
		if rows[i].ID == id {
			return &struct {
				Status string         `json:"status"`
				Result map[string]any `json:"result"`
			}{rows[i].Status, rows[i].Result}
		}
	}
	return nil
}

func (c *cloud) waitTaskStatus(t *testing.T, deviceID, id, status string) *struct {
	Status string         `json:"status"`
	Result map[string]any `json:"result"`
} {
	t.Helper()
	var found *struct {
		Status string         `json:"status"`
		Result map[string]any `json:"result"`
	}
	waitFor(t, 30*time.Second, "task "+id+" -> "+status, func() (bool, error) {
		row := c.taskRow(t, deviceID, id)
		if row == nil {
			return false, nil
		}
		found = row
		return row.Status == status, nil
	})
	return found
}

func (c *cloud) eventsOf(t *testing.T, taskID string, fromSeq int) []map[string]any {
	t.Helper()
	b, err := c.admin("GET", fmt.Sprintf("/api/v1/admin/tasks/%s/events?from_seq=%d&limit=2000", taskID, fromSeq), nil)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	_ = json.Unmarshal(b, &evs)
	return evs
}

func taskConfigWithAgent(home worker.Home, base string, mockPath string) error {
	cfg, err := home.LoadConfig()
	if err != nil {
		return err
	}
	cfg.AgentPath = mockPath
	return home.SaveConfig(cfg)
}

// P2-1 常规任务流：prompt 下发 → 事件流（status/message/tool_call）→ 终态归档。
func TestP2_TaskFlow(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "task-host")
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	defer d.stop()
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})

	taskID, row := c.createTask(t, d.id, "hello mock, do the thing", "ok", 60)
	if row.Status != "acked" {
		t.Fatalf("task cmd status=%s result=%v", row.Status, row.Result)
	}
	fin := c.waitTaskStatus(t, d.id, taskID, "completed")
	if fin == nil {
		t.Fatal("task never completed")
	}
	if got := fin.Result["summary"]; got != "task finished ok" {
		t.Fatalf("summary=%v", got)
	}

	// 事件序列：按 seq 单调，包含 message×2 + tool_call + 终态 status
	evs := c.eventsOf(t, taskID, 0)
	if len(evs) < 5 {
		t.Fatalf("expected ≥5 events, got %d", len(evs))
	}
	kinds := map[string]int{}
	lastSeq := int64(0)
	for _, ev := range evs {
		seq := int64(ev["seq"].(float64))
		if seq <= lastSeq {
			t.Fatalf("event seq not monotonic: %d after %d", seq, lastSeq)
		}
		lastSeq = seq
		kinds[ev["kind"].(string)]++
	}
	if kinds["message"] != 2 || kinds["tool_call"] != 1 {
		t.Fatalf("kinds=%v", kinds)
	}
	lastKind := evs[len(evs)-1]["kind"].(string)
	if lastKind != "status" {
		t.Fatalf("last event kind=%s, want status", lastKind)
	}
}

// P2-2 cancel：杀进程树 + 终态 cancelled 及时回传。
func TestP2_Cancel(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "cancel-host")
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	defer d.stop()
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})

	start := time.Now()
	taskID, _ := c.createTaskNoWait(t, d.id, "long running", "sleep", 300)
	waitFor(t, 10*time.Second, "task running", func() (bool, error) {
		row := c.taskRow(t, d.id, taskID)
		return row != nil && row.Status == "running", nil
	})
	if _, err := c.admin("POST", "/api/v1/admin/tasks/"+taskID+"/cancel", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	fin := c.waitTaskStatus(t, d.id, taskID, "cancelled")
	if fin == nil {
		t.Fatal("task never cancelled")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("cancel took too long: %s（进程树没被杀干净）", elapsed)
	}
}

// P2-3 断网重连：结果 spool 不丢 + 事件经 replay 补齐。
func TestP2_OfflineSpoolReplay(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "offline-host")
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	defer d.stop()
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})

	taskID, _ := c.createTaskNoWait(t, d.id, "gap please", "gap", 120)
	// 等首条事件（before-gap）到达：任务已 running 且云端有事件
	waitFor(t, 10*time.Second, "task has first event", func() (bool, error) {
		return len(c.eventsOf(t, taskID, 0)) >= 2, nil // started/init + before-gap
	})

	// 维护模式 6s：拒绝重连，任务整个尾段（after-gap + 终态 + 结果）都在离线期产生
	if _, err := c.admin("POST", "/api/v1/admin/maintenance", map[string]any{"reject_seconds": 6}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.admin("POST", "/api/v1/admin/devices/"+d.id+"/disconnect", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.admin("POST", "/api/v1/admin/devices/"+d.id+"/disconnect", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "device offline", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "offline", nil
	})

	// 断线期间任务继续跑完：结果先落 spool；重连后补传
	fin := c.waitTaskStatus(t, d.id, taskID, "completed")
	if fin == nil {
		t.Fatal("offline task result lost (spool broken)")
	}

	// before-gap 在线送达；after-gap/终态在离线期产生（可能有丢失）→ replay 补齐
	before := len(c.eventsOf(t, taskID, 0))
	seenTexts := map[string]bool{}
	for _, ev := range c.eventsOf(t, taskID, 0) {
		if dm, _ := ev["data"].(map[string]any); dm != nil {
			if tx, _ := dm["text"].(string); tx != "" {
				seenTexts[tx] = true
			}
		}
	}
	if seenTexts["after-gap"] {
		t.Log("after-gap arrived online (reconnect won the race); replay still must be a no-op-safe op")
	}
	d2 := c.issueCmd(t, d.id, proto.CmdTaskEventsReplay,
		map[string]any{"task_id": taskID, "from_seq": 0})
	if d2.Status != "acked" {
		t.Fatalf("replay cmd %s result=%v", d2.Status, d2.Result)
	}
	t.Logf("replay result: %v (before=%d)", d2.dataOf(), before)
	after := len(c.eventsOf(t, taskID, 0))
	if after <= before {
		dump := []string{}
		for _, ev := range c.eventsOf(t, taskID, 0) {
			dump = append(dump, fmt.Sprintf("%v:%v", ev["seq"], ev["kind"]))
		}
		raw, _ := os.ReadFile(filepath.Join(d.home.TasksDir(), taskID, "events.jsonl"))
		t.Fatalf("replay added nothing: before=%d after=%d resent=%v events=%v\nlocal events.jsonl:\n%s",
			before, after, d2.dataOf()["resent"], dump, raw)
	}
	// 补齐后应覆盖完整序列：2 message（before/after-gap）+ 终态 status，且 seq 连续无空洞
	evs := c.eventsOf(t, taskID, 0)
	kinds := map[string]int{}
	texts := map[string]bool{}
	lastSeq := int64(0)
	for _, ev := range evs {
		seq := int64(ev["seq"].(float64))
		if seq != lastSeq+1 {
			t.Fatalf("seq hole after replay: %d after %d", seq, lastSeq)
		}
		lastSeq = seq
		kinds[ev["kind"].(string)]++
		if dm, _ := ev["data"].(map[string]any); dm != nil {
			if tx, _ := dm["text"].(string); tx != "" {
				texts[tx] = true
			}
		}
	}
	if kinds["message"] != 2 || !texts["before-gap"] || !texts["after-gap"] {
		t.Fatalf("after replay kinds=%v texts=%v", kinds, texts)
	}
	lastKind := evs[len(evs)-1]["kind"].(string)
	if lastKind != "status" {
		t.Fatalf("last event kind=%s, want terminal status", lastKind)
	}
}

// P2-4 看门狗超时。
func TestP2_Timeout(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "timeout-host")
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	defer d.stop()
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})

	start := time.Now()
	taskID, _ := c.createTask(t, d.id, "run forever", "sleep", 2)
	if fin := c.waitTaskStatus(t, d.id, taskID, "timeout"); fin == nil {
		t.Fatal("task never timed out")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("timeout watchdog too slow: %s", elapsed)
	}
}
