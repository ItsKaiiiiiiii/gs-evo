package e2e

// Runtime（运行时）生命周期 e2e（D14，v2 模型：选类型创建，不填路径）：
//	创建（worker 定位二进制 + 建 workspace + 连通性自检）→ 同步上报
//	→ 任务按 runtime_id 精确指派 → 删除（workspace 清理 + 云端移除）
//	→ 重复创建拒绝 / 不支持类型拒绝（报错可读）

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRuntimeLifecycle(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "rt-host")
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	defer d.stop()
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})
	devID := d.id

	// 1. 创建运行时（选类型即可；worker 定位二进制 + 建 workspace + 连通性自检）
	regB, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "runtime.register", "ttl_seconds": 60,
		"payload": map[string]any{"type": "gsagent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(regB, &reg)
	regRow := c.waitCmd(t, devID, reg.CmdID)
	if !regRow.dataOfBool() {
		t.Fatalf("runtime.register failed: %v", regRow.Result)
	}
	createdID, _ := regRow.dataOf()["runtime_id"].(string)
	if createdID == "" {
		t.Fatalf("no runtime_id in receipt: %v", regRow.Result)
	}

	// 2. 同步上报：运行时列表出现（registered 来源、ok=true）
	var rts []struct {
		RuntimeID string `json:"runtime_id"`
		Type      string `json:"type"`
		OK        bool   `json:"ok"`
	}
	waitFor(t, 10*time.Second, "created runtime reported", func() (bool, error) {
		b, err := c.admin("GET", "/api/v1/admin/devices/"+devID+"/runtimes", nil)
		if err != nil {
			return false, err
		}
		rts = nil
		if err := json.Unmarshal(b, &rts); err != nil {
			return false, err
		}
		return len(rts) > 0, nil
	})
	found := false
	for _, rt := range rts {
		if rt.Type == "gsagent" && rt.OK {
			found = true
		}
	}
	if !found {
		t.Fatalf("gsagent runtime not reported: %+v", rts)
	}

	// 3. 任务按 runtime_id 精确指派
	body := map[string]any{"device_id": devID, "prompt": "run on runtime", "agent_type": "gsagent",
		"runtime_id": createdID, "timeout_s": 60}
	b, err := c.admin("POST", "/api/v1/admin/tasks", body)
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(b, &issued)
	fin := c.waitTaskStatus(t, devID, issued.TaskID, "completed")
	if fin == nil {
		t.Fatal("runtime-assigned task never completed")
	}

	// 4. 生命周期：删除（workspace 清理 + 云端移除）
	delB, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "runtime.remove", "ttl_seconds": 60,
		"payload": map[string]any{"runtime_id": createdID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var del struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(delB, &del)
	delRow := c.waitCmd(t, devID, del.CmdID)
	if delRow.Status != "acked" || !delRow.dataOfBool() {
		t.Fatalf("runtime.remove failed: %v", delRow.Result)
	}
	waitFor(t, 10*time.Second, "runtime removed from cloud", func() (bool, error) {
		b, err := c.admin("GET", "/api/v1/admin/devices/"+devID+"/runtimes", nil)
		if err != nil {
			return false, err
		}
		return len(b) > 0 && !strings.Contains(string(b), createdID), nil
	})
}

// 5. 校验路径：重复创建拒绝（同类型每设备一个实例）、不支持类型报错列出支持类型
func TestRuntimeRegisterValidation(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "rt-val-host")
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	defer d.stop()
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})
	devID := d.id

	// 创建第一个（成功基线）
	regB, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "runtime.register", "ttl_seconds": 60,
		"payload": map[string]any{"type": "gsagent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(regB, &reg)
	if row := c.waitCmd(t, devID, reg.CmdID); !row.dataOfBool() {
		t.Fatalf("first register failed: %v", row.Result)
	}

	// 重复创建 → 明确报错（不静默覆盖）
	regDup, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "runtime.register", "ttl_seconds": 60,
		"payload": map[string]any{"type": "gsagent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var dup struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(regDup, &dup)
	dupRow := c.waitCmd(t, devID, dup.CmdID)
	if dupRow.dataOfBool() {
		t.Fatal("duplicate register must fail")
	}
	if errMsg, _ := dupRow.Result["error"].(string); !strings.Contains(errMsg, "已存在") {
		t.Fatalf("duplicate error unclear: %v", dupRow.Result["error"])
	}

	// 不支持的类型 → 报错列出支持类型
	regBad, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "runtime.register", "ttl_seconds": 60,
		"payload": map[string]any{"type": "no-such-type"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var bad struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(regBad, &bad)
	badRow := c.waitCmd(t, devID, bad.CmdID)
	if badRow.dataOfBool() {
		t.Fatal("unsupported type must fail")
	}
	if errMsg, _ := badRow.Result["error"].(string); !strings.Contains(errMsg, "claude-code") {
		t.Fatalf("unsupported-type error must list supported types: %v", badRow.Result["error"])
	}
}
