package worker

// 信任包（D15）：机器侧用户的三项权利——知情、同意、暂停。
//  - audit.log：云端发起的每个动作本地留痕（gs-worker history 可查）
//  - paused 哨兵：暂停期间心跳保持，但拒绝一切动作类指令
//  - login 明示同意：数据边界打印 + 确认（--yes 跳过）

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// audit 本地审计日志：只追加，人类可读（机器侧用户的"账本"）。
func (e *Engine) audit(action, detail string) {
	f, err := os.OpenFile(e.Home.AuditLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("[%s] %-18s %s\n", ts, action, detail)
	_, _ = f.WriteString(line)
}

// isPaused 暂停哨兵：home/paused 文件存在 = 已暂停。
func (e *Engine) isPaused() bool {
	_, err := os.Stat(filepath.Join(e.Home.Dir, "paused"))
	return err == nil
}

// SetPaused 暂停/恢复（CLI 入口；写哨兵文件 + 本地审计）。
func SetPaused(home Home, paused bool) error {
	if paused {
		if err := os.WriteFile(filepath.Join(home.Dir, "paused"), []byte(time.Now().Format("2006-01-02 15:04:05")+"\n"), 0o600); err != nil {
			return err
		}
		appendAudit(home, "PAUSE ", "设备已暂停：不执行云端下发的扫描/收集/安装/任务，心跳保持")
		return nil
	}
	if err := os.Remove(filepath.Join(home.Dir, "paused")); err != nil && !os.IsNotExist(err) {
		return err
	}
	appendAudit(home, "RESUME", "设备已恢复：正常执行云端指令")
	return nil
}

func PausedState(home Home) (bool, string) {
	b, err := os.ReadFile(filepath.Join(home.Dir, "paused"))
	if err != nil {
		return false, ""
	}
	return true, strings.TrimSpace(string(b))
}

func appendAudit(home Home, action, detail string) {
	f, err := os.OpenFile(filepath.Join(home.Dir, "audit.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(fmt.Sprintf("[%s] %-18s %s\n", time.Now().Format("2006-01-02 15:04:05"), action, detail))
}

var _ = strings.ToUpper
