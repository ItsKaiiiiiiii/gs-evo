//go:build windows

package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func syscallExec(path string, args []string, env []string) error {
	// Windows 不支持原地 exec（调用方已有 fallback：退出并依赖计划任务/人工）
	return fmt.Errorf("exec not supported on windows")
}

func runtimeGOOS() string { return goos() }

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

func AutostartEnabled() bool {
	// 计划任务存在即认为已接管（登录时拉起）
	out, err := exec.Command("schtasks", "/query", "/tn", "gs-worker").CombinedOutput()
	return err == nil && len(out) > 0
}

func AutostartEnable(home Home) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	cmd := exec.Command("schtasks", "/create", "/f", "/sc", "onlogon",
		"/tn", "gs-worker",
		"/tr", fmt.Sprintf(`"%s" daemon --home "%s"`, exe, home.Dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("schtasks: %v: %s", err, out)
	}
	return "已注册计划任务（登录时自动拉起）。注意：升级后的新版本需重新登录或手动启动一次接管", nil
}

func AutostartDisable() (string, error) {
	if out, err := exec.Command("schtasks", "/delete", "/f", "/tn", "gs-worker").CombinedOutput(); err != nil {
		return "", fmt.Errorf("schtasks: %v: %s", err, out)
	}
	return "已移除计划任务", nil
}
