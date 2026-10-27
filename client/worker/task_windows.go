//go:build windows

package worker

// Windows：Job Object 整树终止（§6.4/C7）。子进程挂入独立 Job，KILL_ON_JOB_CLOSE 兜底，
// 终止用 TerminateJobObject 整树回收。
// 注：不使用 CREATE_SUSPENDED（exec 不暴露主线程句柄），Assign 在 Start 后立即执行，
// 竞态窗口内子进程尚未 spawn 孙进程，工程上可接受。

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareProc(cmd *exec.Cmd) {}

func finishProcSetup(cmd *exec.Cmd) (func() error, error) {
	if cmd.Process == nil {
		return nil, fmt.Errorf("process not started")
	}
	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(h)

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		return nil, fmt.Errorf("set job info: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		return nil, fmt.Errorf("assign job: %w", err)
	}
	return func() error { return windows.TerminateJobObject(job, 1) }, nil
}
