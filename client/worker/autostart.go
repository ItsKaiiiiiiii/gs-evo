package worker

// 自更新自动生效 + 自启注册（用户条件：云端下发，worker 自己完成升级，无需上机操作）。
// 策略：
//   已注册自启（systemd/LaunchAgent 管理）→ swap 后 os.Exit(0)，由服务管理器拉起新版；
//   未注册自启 + POSIX → syscall.Exec 原地重启为新二进制（不依赖任何服务管理器）；
//   未注册自启 + Windows → 无法原地 exec，返回提示（依赖人工/计划任务）。

import (
	"os"
	"runtime"
	"time"
)

// RestartIntoSelf 升级完成后让新二进制接管进程。
// exe 为替换后的可执行文件绝对路径。返回给用户的说明文案（Windows 未装自启时非空）。
func (e *Engine) RestartIntoSelf(exe string) string {
	if AutostartEnabled() {
		// 服务管理器（systemd Restart=always / launchd KeepAlive）负责拉起
		e.Log.Info("update applied: exiting for service manager restart", "binary", exe)
		go func() { persistAndExit() }()
		return "已替换为新版本，进程将由系统服务自动拉起"
	}
	if runtime.GOOS == "windows" {
		return "已替换为新版本；Windows 下未注册自启，请手动重启 daemon（或先运行 gs-worker autostart enable）"
	}
	// POSIX：原地 exec，新二进制立即接管（argv 原样保留，如 daemon --home …）
	go func() { persistAndExit() }()
	e.Log.Info("update applied: exec into new binary", "binary", exe)
	args := append([]string{exe}, os.Args[1:]...)
	if err := syscallExec(exe, args, os.Environ()); err != nil {
		e.Log.Warn("exec restart failed; exiting (rely on autostart or manual start)", "err", err)
		persistAndExit()
	}
	return ""
}

func persistAndExit() {
	// 给 cmd_result 的发送留出窗口（spool 兜底：未送达也会在重启后补传）
	time.Sleep(800 * time.Millisecond)
	os.Exit(0)
}
