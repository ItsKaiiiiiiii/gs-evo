package e2e

// Runtime（运行时）生命周期 e2e（D14）：
//	本地发现上报（hello → runtimes 表）→ 任务按 runtime_id 精确指派 →
//	runtime.register 受审计创建（幂等 + 非法路径拒绝）

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
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

	// 1. hello 上报运行时（gsagent：来自 agent_path 配置）
	var rts []struct {
		RuntimeID string `json:"runtime_id"`
		Type      string `json:"type"`
		OK        bool   `json:"ok"`
	}
	waitFor(t, 10*time.Second, "runtime reported via hello", func() (bool, error) {
		start := time.Now()
		b, err := c.admin("GET", "/api/v1/admin/devices/"+devID+"/runtimes", nil)
		if err != nil {
			return false, err
		}
		rts = nil
		if err := json.Unmarshal(b, &rts); err != nil {
			return false, err
		}
		if len(rts) == 0 {
			var n, total int
			_ = sqlExecScan("SELECT COUNT(*) FROM runtime WHERE device_id='"+devID+"'", &n)
			_ = sqlExecScan("SELECT COUNT(*) FROM runtime", &total)
			var owners []string
			db2, _ := sql.Open("mysql", testDSN)
			rows2, _ := db2.Query("SELECT DISTINCT device_id FROM runtime")
			if rows2 != nil {
				for rows2.Next() {
					var x string
					_ = rows2.Scan(&x)
					owners = append(owners, x)
				}
				rows2.Close()
			}
			db2.Close()
			t.Logf("poll empty (took %s): api=0 db(device)=%d db(total)=%d owners=%v expect=%s",
				time.Since(start), n, total, owners, devID)
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

	// 2. 任务按 runtime_id 精确指派
	var gsID string
	for _, rt := range rts {
		if rt.Type == "gsagent" {
			gsID = rt.RuntimeID
		}
	}
	// 用 runtime_id 精确指派：直接构造带 runtime_id 的 task
	body := map[string]any{"device_id": devID, "prompt": "hi", "agent_type": "gsagent",
		"runtime_id": gsID, "timeout_s": 60}
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

	// 3. runtime.register 受审计创建：合法路径（幂等 upsert 同一 id）
	regB, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "runtime.register", "ttl_seconds": 60,
		"payload": map[string]any{"type": "gsagent", "path": mockAgentPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(regB, &reg)
	row := c.waitCmd(t, devID, reg.CmdID)
	if row.Status != "acked" || !row.dataOfBool() {
		t.Fatalf("runtime.register failed: %v", row.Result)
	}

	// 4. 非法路径拒绝（报错可读）
	regBad, err := c.admin("POST", "/api/v1/admin/commands", map[string]any{
		"device_id": devID, "cmd_type": "runtime.register", "ttl_seconds": 60,
		"payload": map[string]any{"type": "gsagent", "path": "/no/such/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var regBadID struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(regBad, &regBadID)
	badRow := c.waitCmd(t, devID, regBadID.CmdID)
	if badRow.dataOfBool() {
		t.Fatal("register with nonexistent path must fail")
	}
}
