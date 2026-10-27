//go:build !windows

package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func syscallExec(path string, args []string, env []string) error {
	return syscall.Exec(path, args, env)
}

const unitName = "gs-worker.service"

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// autostartEnabled 是否已被服务管理器接管（systemd user unit / LaunchAgent）。
func AutostartEnabled() bool {
	switch runtimeGOOS() {
	case "darwin":
		_, err := os.Stat(filepath.Join(homeDir(), "Library", "LaunchAgents", "ai.gsevo.worker.plist"))
		return err == nil
	default: // linux 等 systemd
		_, err := os.Stat(filepath.Join(homeDir(), ".config", "systemd", "user", unitName))
		return err == nil
	}
}

// AutostartEnable 注册开机/登录自启。返回给用户的后继说明（可能为空）。
func AutostartEnable(home Home) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	switch runtimeGOOS() {
	case "darwin":
		dir := filepath.Join(homeDir(), "Library", "LaunchAgents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		plist := filepath.Join(dir, "ai.gsevo.worker.plist")
		content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>ai.gsevo.worker</string>
  <key>ProgramArguments</key><array>
    <string>%s</string><string>daemon</string><string>-home</string><string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
`, exe, home.Dir)
		if err := os.WriteFile(plist, []byte(content), 0o644); err != nil {
			return "", err
		}
		_ = exec.Command("launchctl", "unload", plist).Run()
		if out, err := exec.Command("launchctl", "load", plist).CombinedOutput(); err != nil {
			return "", fmt.Errorf("launchctl load: %v: %s", err, out)
		}
		return "已注册 LaunchAgent（KeepAlive：退出自动拉起；升级后自动生效）", nil
	default: // systemd user unit
		dir := filepath.Join(homeDir(), ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		unit := filepath.Join(dir, unitName)
		content := fmt.Sprintf(`[Unit]
Description=gs-evo worker
After=network-online.target

[Service]
ExecStart=%s daemon --home %s
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
`, exe, home.Dir)
		if err := os.WriteFile(unit, []byte(content), 0o644); err != nil {
			return "", err
		}
		run := func(args ...string) (string, error) {
			out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
			return string(out), err
		}
		_, _ = run("daemon-reload")
		if out, err := run("enable", "--now", unitName); err != nil {
			hint := ""
			if strings.Contains(out, "Failed to connect to bus") || strings.Contains(out, "NOTIFY_SOCKET") || strings.Contains(out, "DBUS_SESSION_BUS_ADDRESS") {
				hint = "；当前会话无 systemd user bus——unit 文件已写入，登录后执行 `systemctl --user enable --now gs-worker.service`，无界面机器执行 `sudo loginctl enable-linger $USER`"
			} else {
				hint = "；" + out
			}
			return hint, nil // unit 已写好，不算失败
		}
		return "已注册 systemd user 服务（Restart=always：退出自动拉起；升级后自动生效）。无界面机器建议 `sudo loginctl enable-linger $USER`", nil
	}
}

// AutostartDisable 注销自启。
func AutostartDisable() (string, error) {
	switch runtimeGOOS() {
	case "darwin":
		plist := filepath.Join(homeDir(), "Library", "LaunchAgents", "ai.gsevo.worker.plist")
		_ = exec.Command("launchctl", "unload", plist).Run()
		if err := os.Remove(plist); err != nil {
			return "", err
		}
		return "已注销 LaunchAgent", nil
	default:
		_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
		if err := os.Remove(filepath.Join(homeDir(), ".config", "systemd", "user", unitName)); err != nil {
			return "", err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return "已注销 systemd user 服务", nil
	}
}

func runtimeGOOS() string { return goos() }
