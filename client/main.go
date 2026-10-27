// gs-worker：每 OS 用户一个后台进程（§5.1）。
//
//	gs-worker daemon                          前台运行守护进程
//	gs-worker login --server URL --enroll TOK 注册并保存凭证
//	gs-worker status                          本地状态
//	gs-worker skills list | rollback NAME     skill 清单与本地秒回滚
//	gs-worker probe                           本机 agent 探测
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/ItsKaiiiiiiii/gs-evo/client/worker"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	fs := flag.NewFlagSet("gs-worker", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	homeFlag := fs.String("home", "", "worker 数据目录（默认 ~/.gs-agent-worker）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		usage()
		os.Exit(2)
	}
	args := fs.Args()
	// --help/-h 任何位置：打印完整帮助并 exit 0（P2-8）
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		usage()
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	home := worker.DefaultHome()
	if *homeFlag != "" {
		home = worker.Home{Dir: *homeFlag}
	}

	var err error
	switch args[0] {
	case "daemon":
		err = cmdDaemon(home, log)
	case "login":
		err = cmdLogin(home, args[1:])
	case "status":
		err = cmdStatus(home)
	case "skills":
		err = cmdSkills(home, args[1:])
	case "probe":
		err = cmdProbe(home)
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
	fmt.Fprint(os.Stdout, `usage: gs-worker [-home DIR] <command> [flags]

commands:
  daemon                       前台运行守护进程
  login --server URL --enroll TOK
                               注册本机到云端（TOK 为一次性 enroll token；
                               --skills-root 设置 skill 白名单目录，
                               --signing-pubkey 设置安装验签公钥）
  status                       查看本机配置与已装 skill
  probe                        探测本机可用的 coding agent
  skills list                  列出白名单目录中的 skill
  skills rollback NAME         回滚到上一版本（.prev）
`)
}

// cmdLogin enroll 注册流（§6.7）：enroll token → device token → config.toml(0600)。
func cmdLogin(home worker.Home, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", worker.EnvOr("GS_WORKER_SERVER", "http://127.0.0.1:8080"), "cloud base URL")
	enroll := fs.String("enroll", "", "enroll token ('-' to read from stdin)")
	pubkey := fs.String("signing-pubkey", worker.EnvOr("GS_WORKER_SIGNING_PUBKEY", ""), "ed25519 signing pubkey hex")
	_ = pubkey // 由下方 cfg 引用
	root := fs.String("skills-root", worker.EnvOr("GS_WORKER_SKILLS_ROOT", worker.DefaultSkillsRoot()), "whitelisted skills dir (repeatable not supported here)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *enroll == "" {
		return fmt.Errorf("--enroll required ('-' reads token from stdin, keeps it out of shell history)")
	}
	tok := *enroll
	if tok == "-" {
		fmt.Print("enroll token: ")
		b, err := worker.ReadLineStdin()
		if err != nil {
			return err
		}
		tok = strings.TrimSpace(b)
	}
	if !strings.HasPrefix(tok, proto.TokenPrefixEnroll) {
		return fmt.Errorf("token does not look like an enroll token (%s…)", proto.TokenPrefixEnroll)
	}
	deviceID, hadID := home.LoadDeviceID()
	if !hadID {
		deviceID = proto.NewDeviceID()
	}
	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{
		"enroll_token": tok, "device_id": deviceID,
		"hostname": host, "os": runtime.GOOS, "arch": runtime.GOARCH, "worker_version": worker.WorkerVersion,
	})
	resp, err := worker.HTTPPostJSON(*server+"/api/v1/devices/enroll", body)
	if err != nil {
		return err
	}
	var out struct {
		DeviceToken string `json:"device_token"`
		ExpiresAt   string `json:"expires_at"`
	}
	if err := json.Unmarshal(resp, &out); err != nil || out.DeviceToken == "" {
		return fmt.Errorf("bad enroll response")
	}
	// 注册成功才持久化本机身份（失败不残留）
	if err := home.SaveDeviceID(deviceID); err != nil {
		return err
	}
	cfg := &worker.Config{
		ServerURL:      strings.TrimSuffix(*server, "/"),
		Token:          out.DeviceToken,
		Roots:          []string{*root},
		MaxConcurrency: 2,
		SigningPubkey:  *pubkey,
		PermissionMode: "readonly",
	}
	// labels 仅本地记录（v1 云端打标走管理面），保存在 config 注释级别——toml 不支持注释，忽略
	if err := home.SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("enrolled as device %s\nsaved %s\n", deviceID, home.ConfigPath())
	return nil
}

func cmdDaemon(home worker.Home, log *slog.Logger) error {
	eng, err := worker.NewEngine(home, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("gs-worker daemon starting", "home", home.Dir, "server", eng.Cfg.ServerURL)
	if err := eng.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("daemon stopped")
	return nil
}

func cmdStatus(home worker.Home) error {
	cfg, err := home.LoadConfig()
	if err != nil {
		return err
	}
	st := home.LoadState()
	id, _ := home.LoadDeviceID()
	if id == "" {
		id = "(未注册)"
	}
	spool, _ := os.ReadDir(home.SpoolDir())
	fmt.Printf("home:        %s\n", home.Dir)
	fmt.Printf("device_id:   %s\n", id)
	fmt.Printf("server:      %s\n", cfg.ServerURL)
	fmt.Printf("roots:       %s\n", strings.Join(cfg.Roots, ", "))
	fmt.Printf("installed:   %d skill(s)\n", len(st.Installed))
	for name, rec := range st.Installed {
		fmt.Printf("  - %-24s v%s  %s\n", name, rec.Version, rec.InstalledAt)
	}
	fmt.Printf("spool:       %d pending result(s)\n", len(spool))
	return nil
}

func cmdSkills(home worker.Home, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gs-worker skills list | rollback NAME")
	}
	switch args[0] {
	case "list":
		cfg, err := home.LoadConfig()
		if err != nil {
			return err
		}
		_ = cfg
		eng, err := worker.NewEngine(home, slog.Default())
		if err != nil {
			return err
		}
		res, err := eng.Scan(context.Background(), nil)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return nil
	case "rollback":
		if len(args) < 2 {
			return fmt.Errorf("usage: gs-worker skills rollback NAME")
		}
		eng, err := worker.NewEngine(home, slog.Default())
		if err != nil {
			return err
		}
		if err := eng.Rollback(args[1]); err != nil {
			return err
		}
		fmt.Printf("rolled back %s to previous version\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown skills subcommand %q", args[0])
}

func cmdProbe(home worker.Home) error {
	eng, err := worker.NewEngine(home, slog.Default())
	if err != nil {
		return err
	}
	agents := eng.ProbeAgents(context.Background())
	for _, a := range agents {
		status := "missing"
		if a.OK {
			status = "ok"
		}
		fmt.Printf("%-14s %-8s %s %s\n", a.Type, status, a.Version, a.Path)
	}
	return nil
}
