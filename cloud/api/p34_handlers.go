package api

// P3 自进化 + P4 运营化端点：BenchmarkSet / evolve / evaluate / diff / runs /
// auto-policy / client-update / 报表 / token 签发。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/evo"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/skillpkg"
)

// ---- BenchmarkSet ----

func (s *Server) handleCreateBenchmark(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		Name  string                `json:"name"`
		Cases []store.BenchmarkCase `json:"cases"`
	}](r)
	if err != nil || req.Name == "" || len(req.Cases) == 0 {
		writeErr(w, http.StatusBadRequest, "name and cases required")
		return
	}
	// 同名自增 version（基准集不可变，迭代即新版本）
	latest, _ := s.st.LatestBenchmarkSet(req.Name)
	nextVer := 1
	if latest != nil {
		nextVer = latest.Version + 1
	}
	b := &store.BenchmarkSetRow{
		ID: proto.NewID("bs-"), Name: req.Name, Version: nextVer,
		Cases: req.Cases, CreatedBy: "admin",
	}
	if err := s.st.CreateBenchmarkSet(b); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	fresh, _ := s.st.GetBenchmarkSet(b.ID)
	if fresh == nil {
		fresh = b
	}
	writeJSON(w, http.StatusOK, fresh)
}

func (s *Server) handleListBenchmarks(w http.ResponseWriter, r *http.Request) {
	bs, err := s.st.ListBenchmarkSets(atoiDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if bs == nil {
		bs = []*store.BenchmarkSetRow{}
	}
	writeJSON(w, http.StatusOK, bs)
}

func (s *Server) handleGetBenchmark(w http.ResponseWriter, r *http.Request) {
	b, err := s.st.GetBenchmarkSet(r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "benchmark set not found")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// ---- 进化生成（③）----

func (s *Server) handleEvolve(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		ParentVersion string `json:"parent_version,omitempty"`
		Instruction   string `json:"instruction"`
	}](r)
	if err != nil || req.Instruction == "" {
		writeErr(w, http.StatusBadRequest, "instruction required")
		return
	}
	v, err := s.evo.Evolve(r.Context(), r.PathValue("name"), req.ParentVersion, req.Instruction, "admin")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- 评估（④）----

func (s *Server) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		Version        string   `json:"version"`
		BenchmarkSetID string   `json:"benchmark_set_id"`
		DeviceIDs      []string `json:"device_ids"`
		Install        bool     `json:"install"`
		TargetRoot     string   `json:"target_root,omitempty"` // install=true 时必填（灰度安装目标根目录）
		AgentType      string   `json:"agent_type,omitempty"`  // 评估用例执行 agent（空=设备默认）
	}](r)
	if err != nil || req.Version == "" || req.BenchmarkSetID == "" || len(req.DeviceIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "version, benchmark_set_id, device_ids required")
		return
	}
	if req.Install && req.TargetRoot == "" {
		writeErr(w, http.StatusBadRequest, "target_root required when install=true")
		return
	}
	run, err := s.evo.Evaluate(r.Context(), r.PathValue("name"), req.Version,
		req.BenchmarkSetID, "admin", req.DeviceIDs, req.Install, req.TargetRoot, req.AgentType)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.st.ListEvaluationRuns(r.PathValue("name"), r.PathValue("version"), 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*store.EvaluationRunRow{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleVersionDiff 版本审阅：返回当前与 parent 的 SKILL.md 原文（diff 由前端渲染）。
func (s *Server) handleVersionDiff(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	v, err := s.st.GetVersion(name, r.PathValue("version"))
	if err != nil || v == nil {
		writeErr(w, http.StatusNotFound, "version not found")
		return
	}
	cur, err := skillpkg.ExtractFile(s.evo.BundlePath(v.ContentSHA256), "SKILL.md")
	if err != nil {
		writeErr(w, 500, "extract current: "+err.Error())
		return
	}
	out := map[string]any{"version": v.Version, "rationale": v.Rationale, "current_skill_md": string(cur)}
	if v.ParentVersion != "" {
		parent, err := s.st.GetVersion(name, v.ParentVersion)
		if err == nil && parent != nil {
			pmd, err := skillpkg.ExtractFile(s.evo.BundlePath(parent.ContentSHA256), "SKILL.md")
			if err == nil {
				out["parent_version"] = parent.Version
				out["parent_skill_md"] = string(pmd)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 自动发布策略（⑤受控豁免，默认关闭）----

// handleGetAutoPolicy 查看当前策略。
func (s *Server) handleGetAutoPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetAutoPolicy(r.PathValue("name"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		p = &store.AutoPolicy{}
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleAutoPolicy(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[store.AutoPolicy](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Enabled {
		if req.MinRuns < 1 || req.MinPassRate <= 0 || req.MinPassRate > 1 {
			writeErr(w, http.StatusBadRequest, "min_runs>=1 and 0<min_pass_rate<=1 required")
			return
		}
		if req.TargetRoot == "" {
			writeErr(w, http.StatusBadRequest,
				"target_root required: auto rollout installs to <target_root>/<skill>（各机器白名单路径可能不同，务必与设备侧一致）")
			return
		}
	}
	if err := s.st.SetAutoPolicy(r.PathValue("name"), req); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// ---- client.update（P4 自更新）----

// handlePushClientRelease 上传新 worker 二进制（admin 通道直传，服务端打包+签名+登记）。
func (s *Server) handlePushClientRelease(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if version == "" {
		writeErr(w, http.StatusBadRequest, "version required")
		return
	}
	bin, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
	if err != nil || len(bin) == 0 {
		writeErr(w, http.StatusBadRequest, "binary body required")
		return
	}
	// 打 tar.gz（内含 gs-worker）；sha 以 tar.gz 字节为准（内容寻址一致）
	blob, err := skillpkg.PackSingle("gs-worker", bin)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sum := sha256SumHex(blob)
	dst := filepath.Join(s.cfg.DataDir, "bundles", sum[:2], sum+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := os.WriteFile(dst, blob, 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.st.UpsertBundle(sum, int64(len(blob))); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rel := &store.ClientReleaseRow{Version: version, SHA256: sum, Size: int64(len(blob)),
		Notes: r.URL.Query().Get("notes")}
	if err := s.st.CreateClientRelease(rel); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

// handleClientUpdateRollout 向设备/标签下发 client.update 指令（签名包，P4）。
func (s *Server) handleClientUpdateRollout(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		Version   string   `json:"version"`
		DeviceIDs []string `json:"device_ids,omitempty"`
		Labels    []string `json:"labels,omitempty"`
	}](r)
	if err != nil || req.Version == "" {
		writeErr(w, http.StatusBadRequest, "version required")
		return
	}
	rel, err := s.st.GetClientRelease(req.Version)
	if err != nil || rel == nil {
		writeErr(w, http.StatusNotFound, "release not found")
		return
	}
	targets, err := s.svc.ResolveDevices(req.DeviceIDs, req.Labels)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(targets) == 0 {
		writeErr(w, http.StatusBadRequest, "no matching devices")
		return
	}
	sig, err := skillpkg.SignUpdate(s.privHex, rel.SHA256)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type issued struct {
		Device string `json:"device_id"`
		CmdID  string `json:"cmd_id"`
	}
	out := []issued{}
	for _, dev := range targets {
		url, err := s.deviceTicketDownloadURL(r.Context(), dev.ID, rel.SHA256)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		payload, _ := json.Marshal(proto.ClientUpdatePayload{
			Version: rel.Version, URL: url, SHA256: rel.SHA256, Signature: sig,
		})
		var pm map[string]any
		_ = json.Unmarshal(payload, &pm)
		cmd, err := s.CreateDeviceCommand(r.Context(), dev.ID, proto.CmdClientUpdate, pm, "admin")
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, issued{Device: dev.ID, CmdID: cmd.ID})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 报表（P4）----

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	devs, _ := s.st.ListDevices()
	skills, _ := s.st.ListSkills()
	report := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"devices":      map[string]any{"total": len(devs)},
		"skills":       map[string]any{"total": len(skills)},
	}
	online, revoked := 0, 0
	for _, d := range devs {
		switch d.Status {
		case "online":
			online++
		case "revoked":
			revoked++
		}
	}
	report["devices"] = map[string]any{"total": len(devs), "online": online, "revoked": revoked}
	stable := 0
	for _, sk := range skills {
		if sk.LatestStable != "" {
			stable++
		}
	}
	report["skills"] = map[string]any{"total": len(skills), "with_stable": stable}
	// 指令审计统计（近 500 条/设备上限内聚合）
	total, acked, expired := 0, 0, 0
	var recentFailed []map[string]any
	for _, d := range devs {
		cmds, err := s.st.ListCommands(d.ID, 500)
		if err != nil {
			continue
		}
		for _, c := range cmds {
			total++
			switch c.Status {
			case "acked":
				acked++
			case "expired":
				expired++
			}
			if c.Status == "acked" && c.Result != nil {
				if ok, _ := c.Result["ok"].(bool); !ok && len(recentFailed) < 5 {
					recentFailed = append(recentFailed, map[string]any{
						"cmd_id": c.ID, "device_id": d.ID, "cmd_type": c.CmdType,
						"error": c.Result["error"],
					})
				}
			}
		}
	}
	report["commands"] = map[string]any{"total": total, "acked": acked, "expired": expired,
		"recent_failed": recentFailed}
	writeJSON(w, http.StatusOK, report)
}

// ---- token 签发（P4 RBAC 最小版）----

func (s *Server) handleCreateAdminToken(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		Role  string `json:"role"`
		Label string `json:"label"`
	}](r)
	if err != nil || (req.Role != "admin" && req.Role != "viewer") {
		writeErr(w, http.StatusBadRequest, "role must be admin|viewer")
		return
	}
	token := proto.NewAdminToken()
	if err := s.st.AddAdminTokenRole(proto.HashToken(token), req.Label, req.Role); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "role": req.Role})
}

// setupEvolution 装配 Evolution Engine（NewServer 末尾调用）。
func (s *Server) setupEvolution() {
	var gen evo.Generator
	if os.Getenv("GSEVO_EVO_GENERATOR") == "fake" {
		gen = evo.FakeGenerator{}
	} else {
		gen = evo.NewAnthropicFromEnv()
	}
	eng := evo.New(s.st, s.log, filepath.Join(s.cfg.DataDir, "bundles"), s.privHex, gen)
	eng.InstallIssuer = func(ctx context.Context, skillName, version, targetRoot, onConflict, createdBy string,
		deviceIDs, labels []string, evaluationRunID string) ([]*store.Command, error) {
		return s.svc.CreateInstallCommands(ctx, skillName, version, targetRoot, onConflict,
			createdBy, deviceIDs, labels, evaluationRunID)
	}
	eng.TaskIssuer = func(ctx context.Context, deviceID, prompt, agentType string, timeoutS int, env map[string]string) (*store.Command, error) {
		if env == nil {
			env = map[string]string{}
		}
		taskID := proto.NewID("t-")
		// 评估任务入存储：task events 可查（否则 events 404）
		if err := s.st.CreateTask(&store.TaskRow{
			ID: taskID, DeviceID: deviceID, Prompt: prompt, AgentType: agentType,
			CreatedBy: "evaluation:" + eng.CurrentRunID(),
		}); err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(proto.TaskStartPayload{
			TaskID: taskID, Prompt: prompt, AgentType: agentType, TimeoutS: timeoutS, Env: env,
		})
		var pm map[string]any
		_ = json.Unmarshal(payload, &pm)
		cmd, err := s.CreateDeviceCommand(ctx, deviceID, proto.CmdTaskStart, pm, "admin")
		if err != nil {
			return nil, err
		}
		_ = s.st.SetTaskCmdID(taskID, cmd.ID)
		return cmd, nil
	}
	s.evo = eng
}

func sha256SumHex(b []byte) string {
	h := sha256.New()
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
