package ports

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// restart stops a process and starts it again with the same arguments,
// working directory and environment.
//
// Everything is read before the process is stopped: a failure to read any
// of it cancels the restart, so a process is never left dead.
func (m *ActionManager) restart(ctx context.Context, proc *process.Process, result ActionResult) (ActionResult, int) {
	fail := func(status int, message string) (ActionResult, int) {
		result.Message = message
		return result, status
	}

	argv, err := proc.CmdlineSlice()
	if err != nil || len(argv) == 0 {
		return fail(httpStatusForbidden, "无法读取进程的命令行，已取消重启")
	}
	cwd, err := proc.Cwd()
	if err != nil {
		return fail(httpStatusForbidden, "无法读取进程的工作目录，已取消重启")
	}
	environ, err := proc.Environ()
	if err != nil {
		return fail(httpStatusForbidden, "无法读取进程的环境变量，已取消重启")
	}

	// Stop the original process.
	if err := proc.TerminateWithContext(ctx); err != nil {
		if isNoSuchProcessErr(err) {
			return fail(httpStatusNotFound, "进程已经退出")
		}
		if isPermissionErr(err) {
			return fail(httpStatusForbidden, permissionMessage("停止进程", err))
		}
		return fail(httpStatusInternal, "发送停止信号失败: "+err.Error())
	}
	if !m.waitExit(proc.Pid) {
		result.TimedOut = true
		result.NeedsKill = true
		result.Message = "已发送停止信号，但进程未在 " + m.timeout.String() + " 内退出，已取消重启"
		return result, httpStatusOK
	}

	// Start it again with the same arguments, directory and environment.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Args = argv
	cmd.Dir = cwd
	cmd.Env = environ
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return fail(httpStatusInternal, "重启失败: "+err.Error())
	}
	pid := int32(cmd.Process.Pid)
	// The child is detached, so it must not be waited on here.
	go func() { _ = cmd.Wait() }()

	result.OK = true
	result.NewPID = pid
	result.Message = "进程已重启"
	return result, httpStatusOK
}

// detach puts the new process in its own session so it survives the tool.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	// The new process must not inherit the terminal of mymac.
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer devNull.Close()
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
}

// FormatUptime renders a duration the way a person would read it.
func FormatUptime(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return itoa(days) + "天" + itoa(hours) + "小时"
	case hours > 0:
		return itoa(hours) + "小时" + itoa(minutes) + "分"
	default:
		return itoa(minutes) + "分"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TrimCommand shortens a command line for display.
func TrimCommand(argv []string, n int) string {
	s := strings.Join(argv, " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
