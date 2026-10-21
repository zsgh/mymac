package procs

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mymac/internal/envs"
	"mymac/internal/errs"
)

// signalMap maps the API names to signals.
var signalMap = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM,
	"KILL": syscall.SIGKILL,
	"STOP": syscall.SIGSTOP,
	"CONT": syscall.SIGCONT,
	"INT":  syscall.SIGINT,
	"HUP":  syscall.SIGHUP,
	"USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2,
}

// ValidSignal reports whether the name is a known signal.
func ValidSignal(name string) bool {
	_, ok := signalMap[strings.ToUpper(name)]
	return ok
}

// Signal sends a signal to a process.
//
// SIP protected processes simply refuse the signal; that is reported as a
// permission problem rather than a crash.
func Signal(ctx context.Context, pid int, name string) error {
	sig, ok := signalMap[strings.ToUpper(name)]
	if !ok {
		return errs.New(errs.KindInvalid, "未知信号 %q", name)
	}
	if pid <= 1 {
		return errs.New(errs.KindPermission, "拒绝向 pid %d 发送信号", pid)
	}
	// mymac must not signal itself. A SIGSTOP would freeze the process while
	// it keeps its port and its single instance lock, leaving the user with
	// a service that looks alive but never answers.
	if IsSelf(pid) {
		return errs.New(errs.KindPermission, "拒绝向 mymac 自己（pid %d）发送信号", pid)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		switch err {
		case syscall.ESRCH:
			return errs.New(errs.KindProcessGone, "进程 %d 已退出", pid)
		case syscall.EPERM:
			return errs.New(errs.KindPermission, "没有权限向进程 %d 发送信号（可能受 SIP 保护）", pid)
		}
		return errs.Wrap(err, errs.KindInternal, "发送信号失败")
	}
	return nil
}

// Renice changes the scheduling priority of a process.
//
// Lowering the priority (a negative nice value) needs root, so that path
// goes through the privileged helper.
func Renice(ctx context.Context, pid int, nice int) error {
	if nice < -20 || nice > 19 {
		return errs.New(errs.KindInvalid, "nice 值必须在 -20 到 19 之间")
	}
	if pid <= 1 {
		return errs.New(errs.KindPermission, "拒绝修改 pid %d 的优先级", pid)
	}
	// Renicing mymac itself is pointless and would only slow it down.
	if IsSelf(pid) {
		return errs.New(errs.KindPermission, "拒绝修改 mymac 自己（pid %d）的优先级", pid)
	}
	cur, err := syscall.Getpriority(syscall.PRIO_PROCESS, pid)
	if err != nil {
		if err == syscall.ESRCH {
			return errs.New(errs.KindProcessGone, "进程 %d 已退出", pid)
		}
		if err == syscall.EPERM {
			return errs.New(errs.KindPermission, "无法读取进程 %d 的优先级", pid)
		}
		return errs.Wrap(err, errs.KindInternal, "读取优先级失败")
	}
	if nice >= cur {
		// Raising the nice value (making the process less important) is
		// always allowed for the user's own processes.
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice); err != nil {
			return classifySetpriority(err, pid)
		}
		return nil
	}
	// Lowering the priority needs root.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		"/usr/bin/nice -n "+strconv.Itoa(nice)+" /bin/true")
	// The helper is expected to call renice(2) with the right arguments.
	cmd = exec.CommandContext(ctx, "/usr/bin/renice", "-n", strconv.Itoa(nice), "-p", strconv.Itoa(pid))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "Permission denied") || strings.Contains(string(out), "not permitted") {
			return errs.New(errs.KindPermission, "降低优先级需要管理员权限")
		}
		return errs.Wrap(err, errs.KindInternal, "renice 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func classifySetpriority(err error, pid int) error {
	switch err {
	case syscall.ESRCH:
		return errs.New(errs.KindProcessGone, "进程 %d 已退出", pid)
	case syscall.EPERM:
		return errs.New(errs.KindPermission, "没有权限修改进程 %d 的优先级", pid)
	}
	return errs.Wrap(err, errs.KindInternal, "设置优先级失败")
}

// Sample captures a thread stack sample of a process.
//
// The `sample` command needs elevated rights for processes owned by other
// users, so a failure is reported as a permission problem.
func Sample(ctx context.Context, pid int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/bin/sample", strconv.Itoa(pid), "1", "-file", "/dev/stdout")
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		if strings.Contains(text, "requires root") || strings.Contains(text, "must be run as root") ||
			strings.Contains(text, "Permission denied") {
			return text, errs.New(errs.KindPermission, "采样其他用户的进程需要管理员权限")
		}
		if strings.Contains(text, "Unable to examine") || strings.Contains(text, "No such process") {
			return text, errs.New(errs.KindProcessGone, "进程 %d 已退出或无法采样", pid)
		}
		return text, errs.Wrap(err, errs.KindInternal, "采样失败")
	}
	return text, nil
}

// OpenFiles lists the open files of a process.
func OpenFiles(ctx context.Context, pid int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, errs.Wrap(err, errs.KindPermission, "无法读取进程 %d 的打开文件", pid)
	}
	var files []string
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		files = append(files, fields[8])
	}
	return files, nil
}

// EnvVar is one environment variable of a running process.
type EnvVar struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

// EnvironmentOf returns the environment a process was started with.
//
// macOS exposes this through `ps eww`, which appends the environment to the
// command line. Only processes owned by the current user are readable; for
// the rest the error explains what is needed.
//
// The environment block is a contiguous run of NAME=VALUE tokens at the end
// of the line, so the parser walks backwards from the end. A value that
// contains a space cannot be told apart from two variables; that is a
// limitation of the ps output itself.
func EnvironmentOf(ctx context.Context, pid int) ([]EnvVar, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/ps", "eww", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return nil, errs.New(errs.KindProcessGone, "进程 %d 已退出或无权读取", pid)
	}
	return parsePsEnvironment(string(out))
}

// parsePsEnvironment extracts the environment from `ps eww` output.
//
// The real output looks like:
//
//	  PID   TT  STAT      TIME COMMAND
//	81660   ??  S     36:55.43 /Applications/ClashX Pro.app/Contents/MacOS/ClashX Pro USER=zhangsong PATH=/usr/bin:/bin
//
// The environment is a contiguous run of NAME=VALUE tokens at the end of the
// line, so the parser walks backwards from the end. A value that contains a
// space cannot be told apart from two variables; that is a limitation of the
// ps output itself.
func parsePsEnvironment(out string) ([]EnvVar, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		return nil, errs.New(errs.KindProcessGone, "进程已退出")
	}
	line := strings.TrimSpace(lines[1])
	tokens := strings.Fields(line)

	var envTokens []string
	for i := len(tokens) - 1; i >= 0; i-- {
		if !envTokenPattern.MatchString(tokens[i]) {
			break
		}
		envTokens = append([]string{tokens[i]}, envTokens...)
	}
	if len(envTokens) == 0 {
		return nil, errs.New(errs.KindUnsupported, "无法读取该进程的环境变量（可能属于其他用户）")
	}

	res := make([]EnvVar, 0, len(envTokens))
	for _, t := range envTokens {
		idx := strings.IndexByte(t, '=')
		name := t[:idx]
		value := t[idx+1:]
		res = append(res, EnvVar{
			Name:      name,
			Value:     value,
			Sensitive: envs.IsSensitive(name),
		})
	}
	return res, nil
}

// envTokenPattern matches a shell-style NAME=VALUE token.
var envTokenPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=.*$`)

// ProcessAlive reports whether a pid still exists.
func ProcessAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return err == syscall.EPERM
	}
	return true
}

// Owns reports whether the current user owns the process.
func Owns(uid uint32) bool { return uid == uint32(os.Getuid()) }
