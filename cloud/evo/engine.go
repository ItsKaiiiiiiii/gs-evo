package evo

// Evolution Engine 编排（§7.5 ③④⑤）：
//   Evolve   — parent bundle + LLM → candidate（source=evolved, rationale 留存）
//   Evaluate — candidate 装灰度设备 → 跑 BenchmarkSet → EvaluationRun（指标+verdict）
//   autopromote — 达标策略下 reviewed→stable（受控豁免 D9，默认关闭）

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/skillpkg"
)

// 安装指令签发回调（api 层注入，避免依赖环）。
type InstallIssuer func(ctx context.Context, skillName, version, targetRoot, onConflict, createdBy string,
	deviceIDs, labels []string, evaluationRunID string) ([]*store.Command, error)

// TaskIssuer 任务下发回调（评估用例执行）。
type TaskIssuer func(ctx context.Context, deviceID, prompt, agentType, runtimeID string, timeoutS int, env map[string]string) (*store.Command, error)

type Engine struct {
	st        *store.Store
	log       *slog.Logger
	bundleDir string
	privHex   string
	Gen       Generator

	runMu      sync.Mutex
	currentRun string

	InstallIssuer InstallIssuer
	TaskIssuer    TaskIssuer
}

func New(st *store.Store, log *slog.Logger, bundleDir, privHex string, gen Generator) *Engine {
	return &Engine{st: st, log: log, bundleDir: bundleDir, privHex: privHex, Gen: gen}
}

func (e *Engine) bundlePath(sha string) string {
	return filepath.Join(e.bundleDir, sha[:2], sha+".tar.gz")
}

// BundlePath 导出：版本 diff 审阅需要读包内 SKILL.md。
func (e *Engine) BundlePath(sha string) string { return e.bundlePath(sha) }

// CurrentRunID 当前评估运行 id（评估任务入 task 存储的来源标注）。
func (e *Engine) CurrentRunID() string {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.currentRun
}

// nextVersionFrom 基于已有版本集合取未占用号：整数版本取 max+1，避免 parent+1 撞号。
func nextVersionFrom(existing []string, parent string) string {
	maxInt := 0
	hasInt := false
	used := map[string]bool{}
	for _, v := range existing {
		used[v] = true
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && fmt.Sprint(n) == v {
			hasInt = true
			if n > maxInt {
				maxInt = n
			}
		}
	}
	if hasInt {
		return fmt.Sprint(maxInt + 1)
	}
	// 全是非整数版本：parent.evo<N> 找空位
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s.evo%d", parent, i)
		if !used[cand] {
			return cand
		}
	}
}

// Evolve 基于指定（或最新 stable）版本生成进化候选。
// 产物：新 SkillVer{source: evolved, status: candidate}，rationale 强制留存。
func (e *Engine) Evolve(ctx context.Context, skillName, parentVersion, instruction, createdBy string) (*store.SkillVersionRow, error) {
	sk, err := e.st.GetSkillByName(skillName)
	if err != nil {
		return nil, err
	}
	if sk == nil {
		return nil, store.ErrNotFound
	}
	if parentVersion == "" {
		parentVersion = sk.LatestStable
		if parentVersion == "" {
			parentVersion = sk.LatestAny
		}
	}
	parent, err := e.st.GetVersion(skillName, parentVersion)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, fmt.Errorf("%w: parent version %s", store.ErrNotFound, parentVersion)
	}

	src := e.bundlePath(parent.ContentSHA256)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("parent bundle missing: %w", err)
	}
	parentMD, err := skillpkg.ExtractFile(src, "SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("extract parent SKILL.md: %w", err)
	}

	versions, err := e.st.ListVersions(skillName)
	if err != nil {
		return nil, err
	}
	existing := make([]string, 0, len(versions))
	for _, v := range versions {
		existing = append(existing, v.Version)
	}
	newVersion := nextVersionFrom(existing, parent.Version)
	gen, err := e.Gen.Generate(ctx, skillName, string(parentMD), instruction)
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}
	newMD := SetFrontMatterVersion(gen.NewSkillMD, newVersion)

	// 重打包（替换 SKILL.md）→ 内容寻址落盘
	blob, newSHA, err := skillpkg.RepackWithSkillMD(src, "SKILL.md", newMD)
	if err != nil {
		return nil, err
	}
	dst := e.bundlePath(newSHA)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(dst+".tmp", blob, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(dst+".tmp", dst); err != nil {
		return nil, err
	}
	if err := e.st.UpsertBundle(newSHA, int64(len(blob))); err != nil {
		return nil, err
	}

	manifest, _ := json.Marshal(map[string]any{
		"name": skillName, "version": newVersion,
		"content_hash": "sha256:" + newSHA, "size": len(blob),
		"parent": parent.Version, "evolved": true,
	})
	tx, err := e.st.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	dup, err := e.st.ContentExists(tx, sk.ID, newSHA)
	if err != nil {
		return nil, err
	}
	if dup {
		return nil, fmt.Errorf("identical candidate already registered")
	}
	v := &store.SkillVersionRow{
		SkillID: sk.ID, Version: newVersion, Manifest: manifest,
		ContentSHA256: newSHA, Size: int64(len(blob)),
		OriginDevice: "", Source: "evolved", ParentVersion: parent.Version, Status: "candidate",
	}
	if err := e.st.InsertVersion(tx, v); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// 候选即 candidate（draft→candidate 走状态机）
	if err := e.st.UpdateVersionStatus(skillName, newVersion, "candidate", ""); err != nil {
		return nil, err
	}
	// rationale 强制留存（§7.5 ③）
	_ = e.st.SetVersionRationale(skillName, newVersion, gen.Rationale)
	e.log.Info("candidate generated", "skill", skillName, "version", newVersion,
		"parent", parent.Version, "sha", newSHA[:8])
	return e.st.GetVersion(skillName, newVersion)
}

// Evaluate 在灰度设备上跑 BenchmarkSet：
//
//	受控例外——candidate 允许装到指定设备（evaluationRunID 随安装指令留痕，普通 install 不放行）；
//	逐 case 下发 task.start，聚合 pass/fail → EvaluationRun{metrics, verdict}。
func (e *Engine) Evaluate(ctx context.Context, skillName, version, benchmarkSetID, createdBy string,
	deviceIDs []string, install bool, targetRoot, agentType, runtimeID string) (*store.EvaluationRunRow, error) {
	v, err := e.st.GetVersion(skillName, version)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, store.ErrNotFound
	}
	bs, err := e.st.GetBenchmarkSet(benchmarkSetID)
	if err != nil {
		return nil, err
	}
	if bs == nil || len(bs.Cases) == 0 {
		return nil, fmt.Errorf("benchmark set empty or missing")
	}
	if len(deviceIDs) == 0 {
		return nil, fmt.Errorf("no gray devices given")
	}

	e.runMu.Lock()
	e.currentRun = proto.NewID("er-")
	runID := e.currentRun
	e.runMu.Unlock()
	run := &store.EvaluationRunRow{
		ID: runID, SkillName: skillName, SkillVersion: version,
		BenchmarkSetID: benchmarkSetID, DeviceIDs: deviceIDs, CreatedBy: createdBy,
	}
	if err := e.st.CreateEvaluationRun(run); err != nil {
		return nil, err
	}

	// 灰度安装（受控：带 evaluation_run_id 的安装指令）
	if install {
		if _, err := e.InstallIssuer(ctx, skillName, version, targetRoot, "overwrite", createdBy, deviceIDs, nil, run.ID); err != nil {
			_ = e.st.FinishEvaluationRun(run.ID, "error", nil, "")
			return nil, fmt.Errorf("gray install: %w", err)
		}
		// 等 install 生效（P1 指令流，e2e mock 秒级；超时 60s）
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if installs, _ := e.st.ListDeviceSkillInstalls(skillName, version, deviceIDs); len(installs) >= len(deviceIDs) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	// 逐 case 执行（在第一台灰度设备上；多设备分片执行留待 P4 运营化）
	total, passed := 0, 0
	perCase := []map[string]any{}
	for _, bc := range bs.Cases {
		total++
		timeoutS := bc.TimeoutS
		if timeoutS <= 0 {
			timeoutS = 60
		}
		cmd, err := e.TaskIssuer(ctx, deviceIDs[0], bc.Prompt, agentType, runtimeID, timeoutS, map[string]string{"MOCK_MODE": "ok"})
		if err != nil {
			perCase = append(perCase, map[string]any{"case": bc.Name, "pass": false, "error": err.Error()})
			continue
		}
		taskPass, snippet := e.waitTaskPass(ctx, deviceIDs[0], cmd.ID, bc, timeoutS)
		if taskPass {
			passed++
		}
		perCase = append(perCase, map[string]any{"case": bc.Name, "pass": taskPass,
			"task_id": cmd.ID, "snippet": snippet})
	}
	passRate := 0.0
	if total > 0 {
		passRate = float64(passed) / float64(total)
	}
	verdict := "fail"
	if total > 0 && passed == total {
		verdict = "pass"
	}
	metrics := map[string]any{
		"total": total, "passed": passed, "pass_rate": passRate, "per_case": perCase,
	}
	status := "failed"
	if verdict == "pass" {
		status = "passed"
	}
	if err := e.st.FinishEvaluationRun(run.ID, status, metrics, verdict); err != nil {
		return nil, err
	}
	e.log.Info("evaluation finished", "run", run.ID, "skill", skillName, "version", version,
		"passed", passed, "total", total, "verdict", verdict)

	// ⑤自动发布检查（受控豁免，默认关闭）
	if err := e.maybeAutoPromote(skillName, run.ID); err != nil {
		e.log.Warn("autopromote check failed", "skill", skillName, "err", err)
	}
	return e.st.GetEvaluationRun(run.ID)
}

func (e *Engine) waitTaskPass(ctx context.Context, deviceID, cmdID string, bc store.BenchmarkCase, timeoutS int) (bool, string) {
	deadline := time.Now().Add(time.Duration(timeoutS)*time.Second + 10*time.Second)
	for time.Now().Before(deadline) {
		cmd, err := e.st.GetCommand(cmdID)
		if err == nil && cmd != nil && cmd.Status == "acked" {
			if !cmd.Result["ok"].(bool) {
				return false, "task failed"
			}
			summary, _ := cmd.Result["data"].(map[string]any)["summary"].(string)
			pass := bc.ExpectContains == "" || strings.Contains(summary, bc.ExpectContains)
			snippet := summary
			if len(snippet) > 200 {
				snippet = snippet[:200]
			}
			return pass, snippet
		}
		select {
		case <-ctx.Done():
			return false, "cancelled"
		case <-time.After(300 * time.Millisecond):
		}
	}
	return false, "task timeout"
}

// maybeAutoPromote：最近 MinRuns 次评估全部达标 → reviewed→stable（签名）→ 灰度/全网下发。
// 策略默认关闭（D9 闸门的受控豁免，开启是显式管理动作）。
func (e *Engine) maybeAutoPromote(skillName, runID string) error {
	policy, err := e.st.GetAutoPolicy(skillName)
	if err != nil || policy == nil || !policy.Enabled {
		return err
	}
	run, err := e.st.GetEvaluationRun(runID)
	if err != nil || run == nil {
		return err
	}
	ver, err := e.st.GetVersion(skillName, run.SkillVersion)
	if err != nil || ver == nil || ver.Status != "candidate" {
		return nil // 只对 candidate 生效
	}
	runs, err := e.st.ListEvaluationRuns(skillName, run.SkillVersion, policy.MinRuns)
	if err != nil {
		return err
	}
	if len(runs) < policy.MinRuns {
		e.log.Info("autopromote: not enough runs", "skill", skillName, "runs", len(runs), "need", policy.MinRuns)
		return nil
	}
	for _, r := range runs {
		if r.Verdict != "pass" {
			e.log.Info("autopromote: run not passing", "run", r.ID, "verdict", r.Verdict)
			return nil
		}
		pr, _ := r.Metrics["pass_rate"].(float64)
		if pr < policy.MinPassRate {
			e.log.Info("autopromote: pass_rate below policy", "skill", skillName, "run", r.ID, "pass_rate", pr)
			return nil
		}
	}
	// 达标：reviewed → stable（签名）→ 下发
	if err := e.st.UpdateVersionStatus(skillName, run.SkillVersion, "reviewed", ""); err != nil {
		return err
	}
	if err := e.st.UpdateVersionStatus(skillName, run.SkillVersion, "stable",
		mustSign(e.privHex, ver.ContentSHA256)); err != nil {
		return err
	}
	e.log.Info("autopromote: version promoted to stable", "skill", skillName, "version", run.SkillVersion)
	if e.InstallIssuer != nil {
		_, err := e.InstallIssuer(context.Background(), skillName, run.SkillVersion,
			policy.TargetRoot, "skip", "autopromote", nil, policy.TargetLabels, "")
		return err
	}
	return nil
}

func mustSign(privHex, sha string) string {
	sig, err := skillpkg.Sign(privHex, sha)
	if err != nil {
		return ""
	}
	return sig
}
