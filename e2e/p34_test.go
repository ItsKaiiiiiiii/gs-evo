package e2e

// P3 自进化人工闭环 + P4 运营化 e2e：
//	P3-1 全闭环：采集→stable→evolve 生成候选（rationale 留存）→diff 审阅→
//        受控灰度安装→跑 BenchmarkSet→EvaluationRun→人工放行 stable→全网生效
//	P4-1 自动发布策略：达标自动 candidate→stable（签名）→下发
//	P4-2 client.update：发布→rollout→下载/验签/落盘（stub 形态）
//	P4-3 报表 + RBAC（viewer 只读）

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupDeviceWithSkill(t *testing.T, hostName, skillName string) (*cloud, *device, string) {
	t.Helper()
	c := setupAll(t)
	d := c.spawnDevice(t, hostName)
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	d.start(t)
	waitFor(t, 15*time.Second, hostName+" online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})
	root := d.cfgRoots(t)[0]
	writeSkill(t, root, skillName, "1", "EVO-ONE", false)
	scan := c.issueCmd(t, d.id, "skills.scan", map[string]any{})
	if scan.Status != "acked" {
		t.Fatal("scan failed")
	}
	c.issueCmd(t, d.id, "skills.collect", map[string]any{"name": skillName, "root": root})
	waitFor(t, 10*time.Second, "v1 registered", func() (bool, error) {
		return len(c.versions(t, skillName)) >= 1, nil
	})
	for _, st := range []string{"candidate", "reviewed", "stable"} {
		if _, err := c.admin("POST", "/api/v1/admin/skills/"+skillName+"/versions/1/status",
			map[string]string{"status": st}); err != nil {
			t.Fatalf("v1 promote %s: %v", st, err)
		}
	}
	return c, d, root
}

// P3-1 完整闭环（§7.5 ①-⑥）。
func TestP3_EvolutionLoop(t *testing.T) {
	c, d, root := setupDeviceWithSkill(t, "evo-host", "hello-evo")
	defer d.stop()
	devID := d.id

	// ②基准集
	bsBody := map[string]any{"name": "hello-bench", "cases": []map[string]any{{
		"name": "greet", "prompt": "say hello", "expect_contains": "task finished ok", "timeout_s": 30,
	}}}
	bsB, err := c.admin("POST", "/api/v1/admin/benchmarks", bsBody)
	if err != nil {
		t.Fatal(err)
	}
	var bs struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(bsB, &bs)

	// ③生成候选（fake generator：确定性改写，rationale 留存）
	if _, err := c.admin("POST", "/api/v1/admin/skills/hello-evo/evolve",
		map[string]string{"instruction": "polish wording"}); err != nil {
		t.Fatalf("evolve: %v", err)
	}
	var v2 struct {
		Version   string `json:"version"`
		Status    string `json:"status"`
		Source    string `json:"source"`
		Rationale string `json:"rationale"`
	}
	v2raw, _ := c.admin("GET", "/api/v1/admin/skills/hello-evo/versions", nil)
	var vers []struct {
		Version   string `json:"version"`
		Status    string `json:"status"`
		Source    string `json:"source"`
		Rationale string `json:"rationale"`
	}
	_ = json.Unmarshal(v2raw, &vers)
	for _, v := range vers {
		if v.Version != "1" {
			v2 = v
		}
	}
	if v2.Version == "" || v2.Version == "1" {
		t.Fatal("candidate version missing")
	}
	if v2.Status != "candidate" || v2.Source != "evolved" {
		t.Fatalf("candidate status/source=%s/%s, want candidate/evolved", v2.Status, v2.Source)
	}
	if v2.Rationale == "" {
		t.Fatal("rationale not persisted (must be, §7.5③)")
	}

	// diff 审阅：rationale + 当前/父本 SKILL.md
	diffB, err := c.admin("GET", "/api/v1/admin/skills/hello-evo/versions/"+v2.Version+"/diff", nil)
	if err != nil {
		t.Fatal(err)
	}
	var diff struct {
		ParentVersion  string `json:"parent_version"`
		CurrentSkillMD string `json:"current_skill_md"`
		ParentSkillMD  string `json:"parent_skill_md"`
	}
	_ = json.Unmarshal(diffB, &diff)
	if diff.ParentVersion != "1" || diff.CurrentSkillMD == "" || diff.ParentSkillMD == "" {
		t.Fatalf("diff incomplete: %+v", diff)
	}
	if diff.CurrentSkillMD == diff.ParentSkillMD {
		t.Fatal("candidate content identical to parent")
	}

	// ④受控灰度安装 + 跑基准 → EvaluationRun
	evBody := map[string]any{"version": v2.Version, "benchmark_set_id": bs.ID,
		"device_ids": []string{devID}, "install": true, "target_root": root,
		"agent_type": "gsagent"} // skill = hello-evo（URL 见下）
	runB, err := c.admin("POST", "/api/v1/admin/skills/hello-evo/evaluate", evBody)
	if err != nil {
		t.Fatal(err)
	}
	var run struct {
		ID      string         `json:"id"`
		Status  string         `json:"status"`
		Verdict string         `json:"verdict"`
		Metrics map[string]any `json:"metrics"`
	}
	_ = json.Unmarshal(runB, &run)
	if run.Verdict != "pass" || run.Status != "passed" {
		t.Fatalf("run verdict=%s status=%s metrics=%v", run.Verdict, run.Status, run.Metrics)
	}
	if pr, _ := run.Metrics["pass_rate"].(float64); pr != 1.0 {
		t.Fatalf("pass_rate=%v", run.Metrics["pass_rate"])
	}

	// ⑤人工放行：reviewed → stable（签名）→ ⑥装回设备生效
	for _, st := range []string{"reviewed", "stable"} {
		if _, err := c.admin("POST", "/api/v1/admin/skills/hello-evo/versions/"+v2.Version+"/status",
			map[string]string{"status": st}); err != nil {
			t.Fatalf("promote %s: %v", st, err)
		}
	}
	inst := c.installAndWait(t, "hello-evo", v2.Version, root, devID, "")
	// 评估阶段已灰度安装过 v2 → 幂等短路返回 already_installed（防 .prev 被重复安装覆盖）
	if inst["result"] != "installed" && inst["result"] != "already_installed" {
		t.Fatalf("final install: %v", inst)
	}
	got, _ := os.ReadFile(filepath.Join(root, "hello-evo", "SKILL.md"))
	if string(got) == "" {
		t.Fatal("installed file empty")
	}
}

// P4-1 自动发布策略：达标自动 candidate→stable（签名）→下发。
func TestP4_AutoPromote(t *testing.T) {
	c, d, root := setupDeviceWithSkill(t, "auto-host", "auto-evo")
	defer d.stop()
	devID := d.id

	if _, err := c.admin("POST", "/api/v1/admin/benchmarks", map[string]any{
		"name": "auto-bench", "cases": []map[string]any{{
			"name": "ok", "prompt": "go", "expect_contains": "task finished ok", "timeout_s": 30,
		}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.admin("POST", "/api/v1/admin/skills/auto-evo/evolve",
		map[string]string{"instruction": "auto candidate"}); err != nil {
		t.Fatal(err)
	}
	// 策略：1 次全过即自动发布，目标根 = 设备白名单根
	if _, err := c.admin("POST", "/api/v1/admin/skills/auto-evo/auto-policy", map[string]any{
		"enabled": true, "min_pass_rate": 1.0, "min_runs": 1, "target_root": root}); err != nil {
		t.Fatal(err)
	}

	evB, err := c.admin("POST", "/api/v1/admin/skills/auto-evo/evaluate", map[string]any{
		"version": "2", "benchmark_set_id": latestBenchmarkID(t, c),
		"device_ids": []string{devID}, "install": false, "agent_type": "gsagent"})
	if err != nil {
		t.Fatal(err)
	}
	var run struct {
		Verdict string `json:"verdict"`
	}
	_ = json.Unmarshal(evB, &run)
	if run.Verdict != "pass" {
		t.Fatalf("run verdict=%s", run.Verdict)
	}

	// 自动发布：v2 → stable（签名）→ 下发指令已发出
	vers := c.versions(t, "auto-evo")
	var v2 map[string]any
	for _, v := range vers {
		if v["version"] == "2" {
			v2 = v
		}
	}
	if v2 == nil || v2["status"] != "stable" {
		t.Fatalf("autopromote did not promote: %v", v2)
	}
	if sig, _ := v2["signature"].(string); sig == "" {
		t.Fatal("autopromoted version not signed")
	}
}

func latestBenchmarkID(t *testing.T, c *cloud) string {
	t.Helper()
	b, err := c.admin("GET", "/api/v1/admin/benchmarks?limit=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var bs []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &bs)
	if len(bs) == 0 {
		t.Fatal("no benchmark sets")
	}
	return bs[0].ID
}

// P4-2 client.update：发布→rollout→下载/验签/落盘（stub 形态）。
func TestP4_ClientUpdate(t *testing.T) {
	c := setupAll(t)
	d := c.spawnDevice(t, "update-host")
	if err := taskConfigWithAgent(d.home, c.base, mockAgentPath); err != nil {
		t.Fatal(err)
	}
	// stub 形态：engine 内嵌在测试进程里，不替换自身，止步于落盘
	cfg, _ := d.home.LoadConfig()
	cfg.UpdateStub = true
	_ = d.home.SaveConfig(cfg)
	d.start(t)
	defer d.stop()
	waitFor(t, 15*time.Second, "device online", func() (bool, error) {
		v := c.deviceByID(t, d.id)
		return v != nil && v.Status == "online", nil
	})

	// 发布：以真实 worker 二进制为"新版本"
	if workerBinPath == "" {
		t.Skip("worker binary not built")
	}
	upBody, err := os.Open(workerBinPath)
	if err != nil {
		t.Fatal(err)
	}
	defer upBody.Close()
	resp, err := c.adminUpload("POST", "/api/v1/admin/client-releases/v-test?notes=e2e", upBody)
	if err != nil {
		t.Fatal(err)
	}
	var rel struct {
		SHA256 string `json:"sha256"`
	}
	_ = json.Unmarshal(resp, &rel)
	if rel.SHA256 == "" {
		t.Fatal("release push failed")
	}

	// rollout
	rollB, err := c.admin("POST", "/api/v1/admin/client-update/rollout",
		map[string]any{"version": "v-test", "device_ids": []string{d.id}})
	if err != nil {
		t.Fatal(err)
	}
	var issued []struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(rollB, &issued)
	if len(issued) != 1 {
		t.Fatalf("rollout issued %d", len(issued))
	}
	row := c.waitCmd(t, d.id, issued[0].CmdID)
	if row.Status != "acked" {
		t.Fatalf("client.update cmd %s result=%v", row.Status, row.Result)
	}
	if updated, _ := row.dataOf()["updated"].(bool); !updated {
		t.Fatalf("update failed: ok=%v error=%v full=%v",
			row.Result["ok"], row.Result["error"], row.Result)
	}
	// stub：落盘到 <home>/bin/gs-worker-v-test
	staged := filepath.Join(d.home.Dir, "bin", "gs-worker-v-test")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged binary missing: %v", err)
	}
}

// P4-3 报表 + RBAC。
func TestP4_ReportRBAC(t *testing.T) {
	c := setupAll(t)

	repB, err := c.admin("GET", "/api/v1/admin/report/summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	_ = json.Unmarshal(repB, &rep)
	for _, k := range []string{"devices", "skills", "commands", "generated_at"} {
		if _, ok := rep[k]; !ok {
			t.Fatalf("report missing %s: %v", k, rep)
		}
	}

	// viewer token：只读放行、写拒绝
	tokB, err := c.admin("POST", "/api/v1/admin/tokens", map[string]string{"role": "viewer", "label": "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(tokB, &tok)
	if tok.Token == "" {
		t.Fatal("no viewer token")
	}
	if _, err := adminCall(c.base, tok.Token, "GET", "/api/v1/admin/devices", nil); err != nil {
		t.Fatalf("viewer GET denied: %v", err)
	}
	if _, err := adminCall(c.base, tok.Token, "POST", "/api/v1/admin/enroll-tokens", map[string]string{}); err == nil {
		t.Fatal("viewer POST must be rejected")
	}
	// 无效 token 401
	if _, err := adminCall(c.base, "gsa_bogus", "GET", "/api/v1/admin/devices", nil); err == nil {
		t.Fatal("bogus token must be rejected")
	}
}
