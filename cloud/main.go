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
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: gs-cloud <command> [flags]

  serve                                  start cloud server
  key show                               show ed25519 signing public key
  enroll create [--label L]              create one-time enroll token (24h)
  device list | device revoke --id ID
  cmd send --device ID --type T [--payload JSON] [--ttl S]
  cmd list --device ID [--limit N]
  skill list | skill versions --name N
  skill set-status --name N --version V --status S
  skill install --name N --version V --target-root R [--device ID]... [--label L] [--on-conflict skip|overwrite]

env: GSEVO_DSN GSEVO_ADDR GSEVO_DATA_DIR GSEVO_ADMIN_TOKEN GSEVO_PUBLIC_URL
cli: GSEVO_URL (default http://127.0.0.1:8080) GSEVO_ADMIN_TOKEN
`)
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
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	label := fs.String("label", "", "device label")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch fs.Arg(0) {
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
		printJSON(b)
	default:
		return fmt.Errorf("usage: gs-cloud skill list|versions|set-status|install")
	}
	return nil
}

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
