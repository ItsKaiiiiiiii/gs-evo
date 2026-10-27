//go:build !windows

package worker

// POSIX：进程组启动 + 整树 SIGKILL（AGENT-PROTOCOL.md §6）。

import (
	"os/exec"
	"syscall"
)

func prepareProc(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func finishProcSetup(cmd *exec.Cmd) (func() error, error) {
	return func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}, nil
}
