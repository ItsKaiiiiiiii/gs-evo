// gs-cloud：云端单体服务。
//
//	gs-cloud serve                          启动 Gateway + API
//	gs-cloud enroll create --label xx       管理面：生成一次性 enroll token
//	gs-cloud device list / revoke --id      管理面：设备档案
//	gs-cloud cmd send / list                管理面：指令下发与审计
//	gs-cloud skill ...                      管理面：注册表与状态机
//	gs-cloud key show                       查看签名公钥（写入 client 配置用）
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/api"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// --help/-h：任何层级都以 exit 0 输出帮助；有子命令帮助则用分级版（N-P3-4/6）
	helpRequested := false
	for _, a := range os.Args[1:] {
		if a == "--help" || a == "-h" {
			helpRequested = true
		}
	}
	if helpRequested {
		if len(os.Args) >= 2 {
			if txt, ok := subHelp[os.Args[1]]; ok && len(os.Args) >= 2 {
				fmt.Println(txt)
				return
			}
		}
		fmt.Println(usageText())
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(log)
	case "key":
		err = cmdKey()
	case "enroll":
		err = cmdEnroll(os.Args[2:])
	case "device":
		err = cmdDevice(os.Args[2:])
	case "cmd":
		err = cmdCmd(os.Args[2:])
	case "skill":
		err = cmdSkill(os.Args[2:])
	case "runs":
		err = cmdRuns(os.Args[2:])
	case "diff":
		err = cmdDiff(os.Args[2:])
	case "task":
		err = cmdTask(os.Args[2:])
	case "benchmark":
		err = cmdBenchmark(os.Args[2:])
	case "evolve":
		err = cmdEvolve(os.Args[2:])
	case "evaluate":
		err = cmdEvaluate(os.Args[2:])
	case "policy":
		err = cmdPolicy(os.Args[2:])
	case "client-update":
		err = cmdClientUpdate(os.Args[2:])
	case "report":
		err = cmdReport()
	case "token":
		err = cmdToken(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var subHelp = map[string]string{
	"enroll": `usage: gs-cloud enroll create [--label L]
  签发一次性 enroll token（24h 有效，注册一次即作废）。
  --label 会成为该设备的标签（rollout 圈定用），例如 --label env:machineA`,
	"device": `usage: gs-cloud device list
        gs-cloud device revoke --id ID          吊销设备（立即踢线并拒绝重连）
        gs-cloud device label --id ID --label L...   设置设备标签（覆盖式）`,
	"cmd": `usage: gs-cloud cmd send --device ID --type T [--payload JSON] [--ttl S]
        gs-cloud cmd list --device ID [--limit N]
  T 允许值：agent.probe | skills.scan | skills.collect | task.events_replay
  （skill 安装请用 skill install；任务下发请用 task create）`,
	"skill": `usage: gs-cloud skill <subcommand>
  list                                    全部 skill（含 latest_stable）
  versions --name N                       某 skill 的版本列表与状态机位置
  set-status --name N --version V --status S
                                          状态机：draft→candidate→reviewed→stable（→deprecated）
  install --name N --version V --target-root R
                                          下发安装（仅 stable；--wait 默认等回执并逐台报告成败；
                                          目标目录必须是设备白名单的子目录）`,
	"evaluate": `usage: gs-cloud evaluate --name N --version V --benchmark ID --device ID... [--install --target-root R] [--agent T]
  在灰度设备上跑基准集并生成 EvaluationRun。
  --install 先把候选版本受控安装到灰度设备（target-root 必须与设备白名单一致）。
  --agent 评估任务的执行 agent（如 gsagent；默认用设备自己的默认 agent）`,
	"policy": `usage: gs-cloud policy --name N --show
        gs-cloud policy --name N --enable --min-pass-rate R --min-runs N --target-root R [--label L...]
        gs-cloud policy --name N --disable
  自动发布策略：连续 N 次评估达标 → 自动 reviewed→stable→签名→按标签下发。
  --enable 时 target_root 必填；建议同时 --label 圈定（各机器白名单路径可能不同）`,
	"benchmark": `usage: gs-cloud benchmark create --name N --cases-file F.json
  用例文件为 JSON 数组：[{"name":"c1","prompt":"...","expect_contains":"...","timeout_s":30}]`,
	"task": `usage: gs-cloud task create --device ID --prompt P [--agent T] [--timeout S]
        gs-cloud task list --device ID [--limit N]
        gs-cloud task events --task ID [--from-seq N]
        gs-cloud task cancel --task ID
  任务会下发到设备本机的 agent 执行；events 查看实时过程输出`,
	"client-update": `usage: gs-cloud client-update push --version V --file F [--notes X]
        gs-cloud client-update rollout --version V [--device ID]... [--label L]...`,
	"evolve": `usage: gs-cloud evolve --name N --instruction TEXT [--parent V]
  基于父版本用 LLM 生成改进候选（candidate，需人工/策略放行）`,
	"report": `usage: gs-cloud report
  一屏总览：设备/skill/指令统计与近期失败指令`,
	"token": `usage: gs-cloud token create --role admin|viewer [--label L]
  viewer 只能读（GET 类），不能签发指令/审批`,
}

func usage() {
	fmt.Fprint(os.Stderr, usageText())
}

func usageText() string {
	return `usage: gs-cloud <command> [flags]

设备管理:
  device list                                    设备列表（在线状态/标签）
  device revoke --id ID                          吊销设备（立即踢线并拒绝重连）
  device label --id ID --label L...              设置设备标签（rollout 圈定用）
  enroll create [--label L]                      签发一次性 enroll token（24h）

skill 管理:
  skill list                                     全部 skill（含 latest_stable）
  skill versions --name N                        版本列表与状态机位置
  skill set-status --name N --version V --status S
                                                 状态机: draft→candidate→reviewed→stable
  skill install --name N --version V --target-root R [--device ID]... [--label L]...
                                                 下发安装（仅 stable；默认等逐台回执）
  cmd send --device ID --type T [--payload JSON] 原始指令（scan/collect 等）
  cmd list --device ID [--limit N]               指令审计与结果

任务:
  task create --device ID --prompt P [--agent T] [--timeout 秒]
  task list --device ID [--limit N]              任务状态
  task events --task ID [--from-seq N]           过程事件流
  task cancel --task ID                          取消（杀进程树）

进化闭环:
  benchmark create --name N --cases-file F.json  基准集（JSON 数组用例）
  evolve --name N --instruction TEXT [--parent V] 生成改进候选
  diff --name N --version V                      审阅候选差异与 rationale
  evaluate --name N --version V --benchmark ID --device ID...
                                                 灰度评估（--install 先装候选）
  runs --name N --version V                      评估历史
  policy --name N --show | --enable ... | --disable  自动发布策略（默认关闭）

运营:
  client-update push --version V --file F        发布新 worker 二进制
  client-update rollout --version V [--device ID]... [--label L]...
  report                                         一屏总览（设备/skill/指令统计）
  token create --role admin|viewer               签发管理 token
  key show                                       查看签名公钥（worker 配置用）

env: GSEVO_DSN GSEVO_ADDR GSEVO_DATA_DIR GSEVO_ADMIN_TOKEN GSEVO_PUBLIC_URL
cli: GSEVO_URL (default http://127.0.0.1:8080) GSEVO_ADMIN_TOKEN
`
}

// ---- serve ----

func cmdServe(log *slog.Logger) error {
	dsn := env("GSEVO_DSN", "gsevo:gsevo_dev_pw@tcp(127.0.0.1:3306)/gsevo?parseTime=true&multiStatements=true&time_zone=%27%2B00%3A00%27")
	addr := env("GSEVO_ADDR", ":8080")
	dataDir := env("GSEVO_DATA_DIR", "./data")

	st, err := openStore(dsn)
	if err != nil {
		return err
	}
	// admin token 引导：环境变量优先，否则生成并落盘（仅一次明文展示）
	adminToken := os.Getenv("GSEVO_ADMIN_TOKEN")
	if adminToken == "" {
		p := dataDir + "/admin_token"
		if b, err := os.ReadFile(p); err == nil {
			adminToken = strings.TrimSpace(string(b))
		} else {
			adminToken = newAdminToken()
			if err := os.MkdirAll(dataDir, 0o755); err == nil {
				_ = os.WriteFile(p, []byte(adminToken+"\n"), 0o600)
			}
			log.Info("generated admin token (also written to " + p + ")")
		}
	}
	if err := st.AddAdminToken(hashAdminToken(adminToken), "bootstrap"); err != nil {
		return err
	}

	srv, err := newAPIServer(api.Config{Addr: addr, DataDir: dataDir}, st, log)
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	srv.Gateway().StartExpirySweeper(30*time.Second, stop)

	log.Info("gs-cloud serving", "addr", addr, "data_dir", dataDir)
	log.Info("signing pubkey (put into worker config [security] signing_pubkey): " + srv.PubKeyHex())
	httpSrv := &http.Server{Addr: addr, Handler: srv.Routes()}
	go func() {
		<-stop
	}()
	return httpSrv.ListenAndServe()
}

// ---- 管理面 CLI（走 admin HTTP API，与控制台同一路径）----

func adminReq(method, path string, body any) ([]byte, error) {
	base := env("GSEVO_URL", "http://127.0.0.1:8080")
	token := os.Getenv("GSEVO_ADMIN_TOKEN")
	if token == "" {
		b, err := os.ReadFile(env("GSEVO_DATA_DIR", "./data") + "/admin_token")
		if err != nil {
			return nil, fmt.Errorf("admin token: set GSEVO_ADMIN_TOKEN or ensure data_dir/admin_token exists")
		}
		token = strings.TrimSpace(string(b))
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
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
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func printJSON(b []byte) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err == nil {
		fmt.Println(buf.String())
	} else {
		fmt.Println(string(b))
	}
}

func cmdKey() error {
	if len(os.Args) >= 3 && os.Args[2] == "show" {
		b, err := adminReq("GET", "/api/v1/admin/key", nil)
		if err != nil {
			return err
		}
		printJSON(b)
		return nil
	}
	return fmt.Errorf("usage: gs-cloud key show")
}

func cmdEnroll(args []string) error {
	sub := "create"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		rest = args[1:]
	}
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	label := fs.String("label", "", "device label（会成为该设备的标签）")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	switch sub {
	case "create", "":
		b, err := adminReq("POST", "/api/v1/admin/enroll-tokens", map[string]string{"label": *label})
		if err != nil {
			return err
		}
		printJSON(b)
	default:
		return fmt.Errorf("usage: gs-cloud enroll create [--label L]")
	}
	return nil
}

func cmdDevice(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs-cloud device list | revoke --id ID")
	}
	sub := args[0]
	fs := flag.NewFlagSet("device", flag.ExitOnError)
	id := fs.String("id", "", "device id")
	var labels2 sliceFlags
	fs.Var(&labels2, "label", "device label (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch sub {
	case "list", "":
		b, err := adminReq("GET", "/api/v1/admin/devices", nil)
		if err != nil {
			return err
		}
		printJSON(b)
	case "revoke":
		if *id == "" {
			return fmt.Errorf("--id required")
		}
		b, err := adminReq("POST", "/api/v1/admin/devices/"+*id+"/revoke", map[string]any{})
		if err != nil {
			return err
		}
		printJSON(b)
	case "label":
		if *id == "" || len(labels2) == 0 {
			return fmt.Errorf("--id and --label required")
		}
		b, err := adminReq("POST", "/api/v1/admin/devices/"+*id+"/labels",
			map[string]any{"labels": []string(labels2)})
		if err != nil {
			return err
		}
		printJSON(b)
	default:
		return fmt.Errorf("usage: gs-cloud device list | revoke --id ID")
	}
	return nil
}

func cmdCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs-cloud cmd send|list")
	}
	sub := args[0]
	fs := flag.NewFlagSet("cmd", flag.ExitOnError)
	device := fs.String("device", "", "device id")
	typ := fs.String("type", "", "cmd_type")
	payload := fs.String("payload", "{}", "payload JSON")
	ttl := fs.Int("ttl", 0, "ttl seconds")
	limit := fs.Int("limit", 50, "list limit")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch sub {
	case "send":
		if *device == "" || *typ == "" {
			return fmt.Errorf("--device and --type required")
		}
		var pm map[string]any
		if err := json.Unmarshal([]byte(*payload), &pm); err != nil {
			return fmt.Errorf("bad payload json: %w", err)
		}
		b, err := adminReq("POST", "/api/v1/admin/commands", map[string]any{
			"device_id": *device, "cmd_type": *typ, "payload": pm, "ttl_seconds": *ttl,
		})
		if err != nil {
			return err
		}
		printJSON(b)
	case "list":
		if *device == "" {
			return fmt.Errorf("--device required")
		}
		b, err := adminReq("GET", fmt.Sprintf("/api/v1/admin/commands?device_id=%s&limit=%d", *device, *limit), nil)
		if err != nil {
			return err
		}
		printJSON(b)
	default:
		return fmt.Errorf("usage: gs-cloud cmd send|list")
	}
	return nil
}

func cmdSkill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs-cloud skill list|versions|set-status|install")
	}
	sub := args[0]
	fs := flag.NewFlagSet("skill", flag.ExitOnError)
	name := fs.String("name", "", "skill name")
	version := fs.String("version", "", "version")
	status := fs.String("status", "", "target status")
	targetRoot := fs.String("target-root", "", "install target root")
	onConflict := fs.String("on-conflict", "", "skip|overwrite")
	var devices, labels sliceFlags
	fs.Var(&devices, "device", "device id (repeatable)")
	fs.Var(&labels, "label", "label (repeatable)")
	wait := fs.Bool("wait", true, "等待安装回执并展示结果（--wait=false 只下发）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch sub {
	case "list":
		b, err := adminReq("GET", "/api/v1/admin/skills", nil)
		if err != nil {
			return err
		}
		printJSON(b)
	case "versions":
		if *name == "" {
			return fmt.Errorf("--name required")
		}
		b, err := adminReq("GET", "/api/v1/admin/skills/"+*name+"/versions", nil)
		if err != nil {
			return err
		}
		printJSON(b)
	case "set-status":
		if *name == "" || *version == "" || *status == "" {
			return fmt.Errorf("--name --version --status required")
		}
		b, err := adminReq("POST", fmt.Sprintf("/api/v1/admin/skills/%s/versions/%s/status", *name, *version),
			map[string]string{"status": *status})
		if err != nil {
			return err
		}
		printJSON(b)
	case "install":
		if *name == "" || *version == "" || *targetRoot == "" {
			return fmt.Errorf("--name --version --target-root required")
		}
		body := map[string]any{
			"version": *version, "target_root": *targetRoot,
			"device_ids": []string(devices), "labels": []string(labels),
		}
		if *onConflict != "" {
			body["on_conflict"] = *onConflict
		}
		b, err := adminReq("POST", "/api/v1/admin/skills/"+*name+"/install", body)
		if err != nil {
			return err
		}
		var issued []struct {
			CmdID  string `json:"cmd_id"`
			Device string `json:"device_id"`
		}
		_ = json.Unmarshal(b, &issued)
		if len(issued) == 0 {
			fmt.Println("没有匹配的设备，未下发任何指令")
			return fmt.Errorf("no matching devices")
		}
		fmt.Printf("已下发 %d 台设备的安装指令（%s v%s → %s）\n", len(issued), *name, *version, *targetRoot)
		if !*wait {
			for _, it := range issued {
				fmt.Printf("  cmd_id=%s device=%s（用 cmd list 查询结果）\n", it.CmdID, it.Device)
			}
			return nil
		}
		fmt.Println("等待安装结果…")
		fail := false
		for _, it := range issued {
			row := waitCmdResult(base(), tokenForCLI(), it.CmdID, 120*time.Second)
			switch {
			case row.status != "acked":
				fail = true
				fmt.Printf("  [%s] 超时未回执（cmd_id=%s，可稍后用 cmd list 查询）\n", it.Device, it.CmdID)
			case row.ok == nil || !*row.ok:
				fail = true
				fmt.Printf("  [%s] 安装失败：%v\n", it.Device, row.err)
			default:
				res := row.data
				switch res["result"] {
				case "installed":
					fmt.Printf("  [%s] 安装成功：%s v%s（替换 %s）\n", it.Device,
						nameOr(res["name"]), strOr(res["installed"]), strOr(res["replaced"]))
				case "already_installed":
					fmt.Printf("  [%s] 已是目标版本 %s（幂等跳过，未改动磁盘）\n", it.Device, strOr(res["installed"]))
				case "conflict_skipped":
					fail = true
					fmt.Printf("  [%s] 跳过安装：设备本地有改动（保留现场）。如需覆盖请加 --on-conflict overwrite\n", it.Device)
				default:
					fmt.Printf("  [%s] 结果：%v\n", it.Device, res)
				}
			}
		}
		if fail {
			return fmt.Errorf("部分设备安装失败（见上）")
		}
		return nil
	default:
		return fmt.Errorf("usage: gs-cloud skill list|versions|set-status|install")
	}
	return nil
}

func cmdTask(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs-cloud task create|list|events|cancel")
	}
	sub := args[0]
	fs := flag.NewFlagSet("task", flag.ExitOnError)
	device := fs.String("device", "", "device id")
	prompt := fs.String("prompt", "", "task prompt")
	agent := fs.String("agent", "", "agent type")
	timeout := fs.Int("timeout", 0, "看门狗超时（单位：秒，整数）")
	maxPerm := fs.String("max-permission", "", "readonly|edits|full")
	taskID := fs.String("task", "", "task id")
	limit := fs.Int("limit", 50, "list limit")
	fromSeq := fs.Int("from-seq", 0, "replay from seq")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch sub {
	case "create":
		if *device == "" || *prompt == "" {
			return fmt.Errorf("--device and --prompt required")
		}
		b, err := adminReq("POST", "/api/v1/admin/tasks", map[string]any{
			"device_id": *device, "prompt": *prompt, "agent_type": *agent,
			"timeout_s": *timeout, "max_permission": *maxPerm,
		})
		if err != nil {
			return err
		}
		printJSON(b)
	case "list":
		if *device == "" {
			return fmt.Errorf("--device required")
		}
		b, err := adminReq("GET", fmt.Sprintf("/api/v1/admin/tasks?device_id=%s&limit=%d", *device, *limit), nil)
		if err != nil {
			return err
		}
		printJSON(b)
	case "events":
		if *taskID == "" {
			return fmt.Errorf("--task required")
		}
		b, err := adminReq("GET", fmt.Sprintf("/api/v1/admin/tasks/%s/events?from_seq=%d&limit=%d", *taskID, *fromSeq, *limit), nil)
		if err != nil {
			return err
		}
		printJSON(b)
	case "cancel":
		if *taskID == "" {
			return fmt.Errorf("--task required")
		}
		b, err := adminReq("POST", "/api/v1/admin/tasks/"+*taskID+"/cancel", map[string]any{})
		if err != nil {
			return err
		}
		printJSON(b)
	default:
		return fmt.Errorf("usage: gs-cloud task create|list|events|cancel")
	}
	return nil
}

func cmdRuns(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	name := fs.String("name", "", "skill name")
	version := fs.String("version", "", "version")
	limit := fs.Int("limit", 20, "limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *version == "" {
		return fmt.Errorf("--name and --version required")
	}
	b, err := adminReq("GET", fmt.Sprintf("/api/v1/admin/skills/%s/versions/%s/runs?limit=%d", *name, *version, *limit), nil)
	if err != nil {
		return err
	}
	printJSON(b)
	return nil
}

func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	name := fs.String("name", "", "skill name")
	version := fs.String("version", "", "candidate version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *version == "" {
		return fmt.Errorf("--name and --version required")
	}
	b, err := adminReq("GET", fmt.Sprintf("/api/v1/admin/skills/%s/versions/%s/diff", *name, *version), nil)
	if err != nil {
		return err
	}
	var d struct {
		ParentVersion  string `json:"parent_version"`
		Rationale      string `json:"rationale"`
		CurrentSkillMD string `json:"current_skill_md"`
		ParentSkillMD  string `json:"parent_skill_md"`
	}
	_ = json.Unmarshal(b, &d)
	fmt.Printf("== rationale ==\n%s\n\n", d.Rationale)
	if d.ParentVersion != "" {
		fmt.Printf("== %s → %s ==\n", d.ParentVersion, *version)
		fmt.Println(unifiedTextDiff(d.ParentSkillMD, d.CurrentSkillMD))
	} else {
		fmt.Printf("== %s（无父本）==\n%s\n", *version, d.CurrentSkillMD)
	}
	return nil
}

// unifiedTextDiff 极简行级差异输出（+ 新增 - 删除，审阅用）。
func unifiedTextDiff(oldText, newText string) string {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	var out []string
	i, j := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			out = append(out, "  "+oldLines[i])
			i++
			j++
		case j < len(newLines) && (i >= len(oldLines) || !containsLine(oldLines, newLines[j], i)):
			out = append(out, "+ "+newLines[j])
			j++
		default:
			out = append(out, "- "+oldLines[i])
			i++
		}
	}
	return strings.Join(out, "\n")
}

func containsLine(lines []string, target string, from int) bool {
	for i := from; i < len(lines); i++ {
		if lines[i] == target {
			return true
		}
	}
	return false
}

type cmdOutcome struct {
	status string
	ok     *bool
	data   map[string]any
	err    any
}

func base() string { return env("GSEVO_URL", "http://127.0.0.1:8080") }
func tokenForCLI() string {
	t := os.Getenv("GSEVO_ADMIN_TOKEN")
	if t == "" {
		if b, err := os.ReadFile(env("GSEVO_DATA_DIR", "./data") + "/admin_token"); err == nil {
			t = strings.TrimSpace(string(b))
		}
	}
	return t
}

// waitCmdResult 轮询单条指令直到 acked/超时（CLI 回执用）。
func waitCmdResult(base, token, cmdID string, timeout time.Duration) cmdOutcome {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := adminReq2(base, token, "GET", "/api/v1/admin/commands?limit=500&cmd_id="+cmdID, nil)
		if err == nil {
			var rows []struct {
				CmdID  string         `json:"cmd_id"`
				Status string         `json:"status"`
				Result map[string]any `json:"result"`
			}
			if json.Unmarshal(b, &rows) == nil {
				for _, r := range rows {
					if r.CmdID != cmdID {
						continue
					}
					if r.Status == "acked" || r.Status == "expired" {
						out := cmdOutcome{status: r.Status}
						if r.Result != nil {
							if okv, ok := r.Result["ok"].(bool); ok {
								out.ok = &okv
							}
							out.err = r.Result["error"]
							if d, ok := r.Result["data"].(map[string]any); ok {
								out.data = d
							}
						}
						return out
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return cmdOutcome{status: "timeout"}
}

func adminReq2(base, token, method, path string, body any) ([]byte, error) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func strOr(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "none"
}
func nameOr(v any) string { return strOr(v) }

type sliceFlags []string

func (s *sliceFlags) String() string { return strings.Join(*s, ",") }
func (s *sliceFlags) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// 由 internal 包桥接（避免循环依赖，main 侧最小引用）。
func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---- P3/P4 CLI ----

func cmdBenchmark(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs-cloud benchmark create --name N --cases-file F.json")
	}
	sub := args[0]
	fs := flag.NewFlagSet("benchmark", flag.ExitOnError)
	name := fs.String("name", "", "benchmark name")
	casesFile := fs.String("cases-file", "", "JSON file: [{name,prompt,expect_contains,timeout_s}]")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch sub {
	case "create":
		if *name == "" || *casesFile == "" {
			return fmt.Errorf("--name and --cases-file required")
		}
		raw, err := os.ReadFile(*casesFile)
		if err != nil {
			return err
		}
		var cases []map[string]any
		if err := json.Unmarshal(raw, &cases); err != nil {
			return fmt.Errorf("cases file: %w", err)
		}
		b, err := adminReq("POST", "/api/v1/admin/benchmarks", map[string]any{"name": *name, "cases": cases})
		if err != nil {
			return err
		}
		printJSON(b)
	default:
		return fmt.Errorf("usage: gs-cloud benchmark create")
	}
	return nil
}

func cmdEvolve(args []string) error {
	fs := flag.NewFlagSet("evolve", flag.ExitOnError)
	name := fs.String("name", "", "skill name")
	parent := fs.String("parent", "", "parent version (default latest stable)")
	instruction := fs.String("instruction", "", "improvement instruction")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *instruction == "" {
		return fmt.Errorf("--name and --instruction required")
	}
	b, err := adminReq("POST", "/api/v1/admin/skills/"+*name+"/evolve",
		map[string]string{"parent_version": *parent, "instruction": *instruction})
	if err != nil {
		return err
	}
	printJSON(b)
	return nil
}

func cmdEvaluate(args []string) error {
	fs := flag.NewFlagSet("evaluate", flag.ExitOnError)
	name := fs.String("name", "", "skill name")
	version := fs.String("version", "", "version to evaluate")
	benchmark := fs.String("benchmark", "", "benchmark set id")
	var devices sliceFlags
	fs.Var(&devices, "device", "gray device id (repeatable)")
	agent := fs.String("agent", "", "agent type for benchmark tasks (default: device default)")
	install := fs.Bool("install", false, "gray-install candidate first")
	targetRoot := fs.String("target-root", "", "gray install root (required with --install)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *version == "" || *benchmark == "" || len(devices) == 0 {
		return fmt.Errorf("--name --version --benchmark --device required")
	}
	body := map[string]any{"version": *version, "benchmark_set_id": *benchmark,
		"device_ids": []string(devices), "install": *install, "target_root": *targetRoot,
		"agent_type": *agent}
	b, err := adminReq("POST", "/api/v1/admin/skills/"+*name+"/evaluate", body)
	if err != nil {
		return err
	}
	printJSON(b)
	return nil
}

func cmdPolicy(args []string) error {
	fs := flag.NewFlagSet("policy", flag.ExitOnError)
	name := fs.String("name", "", "skill name")
	show := fs.Bool("show", false, "show current policy")
	disable := fs.Bool("disable", false, "disable autopromote (keeps other settings)")
	enable := fs.Bool("enable", false, "enable autopromote")
	rate := fs.Float64("min-pass-rate", 0, "min pass rate, 0<r<=1 (default: keep current or 1.0)")
	runs := fs.Int("min-runs", 0, "min consecutive passing runs (default: keep current or 2)")
	targetRoot := fs.String("target-root", "", "rollout target root (skill 装到 <root>/<name>)")
	var labels sliceFlags
	fs.Var(&labels, "label", "rollout label (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name required")
	}
	if *show {
		b, err := adminReq("GET", "/api/v1/admin/skills/"+*name+"/auto-policy", nil)
		if err != nil {
			return err
		}
		printJSON(b)
		return nil
	}
	// 合并语义：未指定的字段保留现值（--enable=false 不再重置其它字段）
	cur := map[string]any{"enabled": false, "min_pass_rate": 1.0, "min_runs": 2,
		"target_root": "", "target_labels": []string{}}
	if cb, err := adminReq("GET", "/api/v1/admin/skills/"+*name+"/auto-policy", nil); err == nil {
		_ = json.Unmarshal(cb, &cur)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	patch := map[string]any{}
	if set["enable"] {
		patch["enabled"] = *enable
	}
	if *disable {
		patch["enabled"] = false
	}
	if set["min-pass-rate"] {
		patch["min_pass_rate"] = *rate
	}
	if set["min-runs"] {
		patch["min_runs"] = *runs
	}
	if set["target-root"] {
		patch["target_root"] = *targetRoot
	}
	if set["label"] {
		patch["target_labels"] = []string(labels)
	}
	for k, v := range patch {
		cur[k] = v
	}
	b, err := adminReq("POST", "/api/v1/admin/skills/"+*name+"/auto-policy", cur)
	if err != nil {
		return err
	}
	printJSON(b)
	return nil
}

func cmdClientUpdate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs-cloud client-update push|rollout")
	}
	sub := args[0]
	fs := flag.NewFlagSet("client-update", flag.ExitOnError)
	version := fs.String("version", "", "release version")
	file := fs.String("file", "", "worker binary path")
	notes := fs.String("notes", "", "release notes")
	var devices, labels sliceFlags
	fs.Var(&devices, "device", "device id (repeatable)")
	fs.Var(&labels, "label", "label (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch sub {
	case "push":
		if *version == "" || *file == "" {
			return fmt.Errorf("--version and --file required")
		}
		base := env("GSEVO_URL", "http://127.0.0.1:8080")
		token := os.Getenv("GSEVO_ADMIN_TOKEN")
		if token == "" {
			b, err := os.ReadFile(env("GSEVO_DATA_DIR", "./data") + "/admin_token")
			if err != nil {
				return fmt.Errorf("admin token not found")
			}
			token = strings.TrimSpace(string(b))
		}
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		req, err := http.NewRequest("POST", base+"/api/v1/admin/client-releases/"+*version+"?notes="+*notes, f)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, out)
		}
		printJSON(out)
	case "rollout":
		if *version == "" {
			return fmt.Errorf("--version required")
		}
		b, err := adminReq("POST", "/api/v1/admin/client-update/rollout", map[string]any{
			"version": *version, "device_ids": []string(devices), "labels": []string(labels)})
		if err != nil {
			return err
		}
		printJSON(b)
	default:
		return fmt.Errorf("usage: gs-cloud client-update push|rollout")
	}
	return nil
}

func cmdReport() error {
	b, err := adminReq("GET", "/api/v1/admin/report/summary", nil)
	if err != nil {
		return err
	}
	printJSON(b)
	return nil
}

func cmdToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	role := fs.String("role", "viewer", "admin|viewer")
	label := fs.String("label", "", "label")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.Arg(0) != "create" {
		return fmt.Errorf("usage: gs-cloud token create --role admin|viewer")
	}
	b, err := adminReq("POST", "/api/v1/admin/tokens", map[string]string{"role": *role, "label": *label})
	if err != nil {
		return err
	}
	printJSON(b)
	return nil
}
