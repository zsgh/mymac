// Package perm probes macOS privacy permissions and provides a single
// elevation entry point.
//
// Nothing here is a silent failure: every probe returns a definite state so
// the UI can tell the user exactly which switch to flip.
package perm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"mymac/internal/errs"
)

// Target identifies a privacy permission.
type Target string

// Permission targets.
const (
	TargetFullDisk   Target = "full_disk_access"
	TargetAutomation Target = "automation"
	TargetAdmin      Target = "admin"
)

// State is the outcome of a probe.
type State string

// Permission states.
const (
	StateGranted  State = "granted"
	StateDenied   State = "denied"
	StateUnknown  State = "unknown"
	StateNotApply State = "not_applicable"
)

// Status describes one permission.
type Status struct {
	Target    Target `json:"target"`
	Label     string `json:"label"`
	State     State  `json:"state"`
	Detail    string `json:"detail,omitempty"`
	HowTo     string `json:"howTo"`
	ProbePath string `json:"probePath,omitempty"`
	Granted   bool   `json:"granted"`
}

// fdaProbes are directories that macOS protects with Full Disk Access.
// Reading any of them is enough to decide.
var fdaProbes = []string{
	"Library/Safari",
	"Library/Mail",
	"Library/Safari/Bookmarks.plist",
}

// CheckFullDiskAccess reports whether the current process can read a
// Full-Disk-Access protected path.
func CheckFullDiskAccess() Status {
	st := Status{
		Target:  TargetFullDisk,
		Label:   "完全磁盘访问权限",
		HowTo:   "系统设置 → 隐私与安全性 → 完全磁盘访问权限 → 添加并勾选启动 mymac 的程序（终端或 mymac.app）",
		Granted: false,
	}
	home, err := os.UserHomeDir()
	if err != nil {
		st.State = StateUnknown
		st.Detail = "无法确定 HOME: " + err.Error()
		return st
	}
	var firstErr error
	for _, p := range fdaProbes {
		full := filepath.Join(home, p)
		if _, err := os.ReadDir(full); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Readable: full disk access is granted.
		st.State = StateGranted
		st.Granted = true
		st.ProbePath = full
		st.Detail = "可读取 " + p
		return st
	}
	if firstErr != nil {
		st.State = StateDenied
		st.Detail = firstErr.Error()
		st.ProbePath = filepath.Join(home, "Library/Safari")
		return st
	}
	st.State = StateUnknown
	st.Detail = "受保护的路径不存在，无法判定"
	return st
}

// CheckAutomation reports whether the process may drive other applications
// through AppleScript (needed for the Finder trash helper).
func CheckAutomation(ctx context.Context) Status {
	if cached, ok := automationCache.Load(); ok {
		return cached.(Status)
	}
	st := probeAutomation(ctx)
	automationCache.Store(st)
	return st
}

// automationCache holds the last automation probe.
//
// The probe is cached because an AppleScript call that macOS refuses makes
// the system show a permission dialog, and asking on every request would
// fill the screen with them.
var automationCache = &timedCache{ttl: 5 * time.Minute}

// timedCache is a single value that expires.
type timedCache struct {
	mu  sync.Mutex
	val any
	at  time.Time
	ttl time.Duration
}

func (c *timedCache) Load() (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val == nil || time.Since(c.at) > c.ttl {
		return nil, false
	}
	return c.val, true
}

func (c *timedCache) Store(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.val = v
	c.at = time.Now()
}

// probeAutomation runs the actual AppleScript probe.
func probeAutomation(ctx context.Context) Status {
	st := Status{
		Target:  TargetAutomation,
		Label:   "自动化权限（Finder / System Events）",
		HowTo:   "系统设置 → 隐私与安全性 → 自动化 → 允许启动 mymac 的程序控制 Finder",
		Granted: false,
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "/usr/bin/osascript", "-e",
		`tell application "System Events" to return (count of processes) > 0`)
	out, err := cmd.Output()
	if err != nil {
		st.State = StateDenied
		st.Detail = describeAppleScriptError(err)
		return st
	}
	if strings.TrimSpace(string(out)) == "true" {
		st.State = StateGranted
		st.Granted = true
		st.Detail = "可以驱动 System Events"
		return st
	}
	st.State = StateUnknown
	st.Detail = "System Events 返回了非预期结果: " + strings.TrimSpace(string(out))
	return st
}

// describeAppleScriptError turns the common osascript failures into a hint
// about the missing privacy grant.
func describeAppleScriptError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "-1743"):
		return "系统拒绝发送 Apple 事件（错误 -1743）：未授予自动化权限"
	case strings.Contains(s, "-600"):
		return "无法启动 System Events（错误 -600），可能未授予自动化权限"
	case strings.Contains(s, "-1728"):
		return "System Events 拒绝了请求（错误 -1728）"
	case strings.Contains(s, "timed out"), strings.Contains(s, "deadline"):
		return "探测超时，System Events 可能正在等待用户交互"
	}
	return s
}

// CheckAdmin reports whether the current user can elevate via osascript.
func CheckAdmin() Status {
	st := Status{
		Target:  TargetAdmin,
		Label:   "管理员提权",
		HowTo:   "需要时系统会弹出密码输入框；请确认弹出的对话框来自 osascript",
		Granted: false,
	}
	if os.Geteuid() == 0 {
		st.State = StateGranted
		st.Granted = true
		st.Detail = "当前进程已是 root"
		return st
	}
	// We cannot ask for a password during a status probe without prompting the
	// user, so we only report the capability.
	st.State = StateNotApply
	st.Detail = "按需提权：执行需要 root 的操作时会弹出系统密码框"
	return st
}

// All runs every probe and returns the results.
func All(ctx context.Context) []Status {
	return []Status{
		CheckFullDiskAccess(),
		CheckAutomation(ctx),
		CheckAdmin(),
	}
}

// Elevate runs cmd under root via osascript, prompting for an admin password.
//
// The command is passed as an argv element so no shell quoting of the command
// itself is required; the osascript shell still interprets it, so callers must
// pass a fully formed shell command.
func Elevate(ctx context.Context, cmd string) (string, error) {
	if os.Geteuid() == 0 {
		out, err := exec.CommandContext(ctx, "/bin/sh", "-c", cmd).CombinedOutput()
		return string(out), err
	}
	// `on run argv` keeps the command out of the AppleScript source, which
	// avoids quoting bugs in do shell script.
	script := "on run argv\ndo shell script (item 1 of argv) with administrator privileges\nend run"
	proc := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script, cmd)
	var stderr strings.Builder
	proc.Stderr = &stderr
	out, err := proc.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		if strings.Contains(detail, "User canceled") || strings.Contains(detail, "-128") {
			return "", errs.New(errs.KindPermission, "用户取消了提权操作")
		}
		return string(out), errs.Wrap(err, errs.KindPermission, "提权执行失败")
	}
	return string(out), nil
}

// OpenPrivacyPane opens the System Settings privacy pane for the target.
func OpenPrivacyPane(target Target) error {
	var arg string
	switch target {
	case TargetFullDisk:
		arg = "x-apple.systempreferences:com.apple.preferences.privacy.security#Privacy_AllFiles"
	case TargetAutomation:
		arg = "x-apple.systempreferences:com.apple.preferences.privacy.security#Privacy_Automation"
	default:
		arg = "x-apple.systempreferences:com.apple.preferences.privacy.security"
	}
	return exec.Command("/usr/bin/open", arg).Run()
}

// Truncate shortens s for display in the UI.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// IsRunningAsRoot reports whether the current process has uid 0.
func IsRunningAsRoot() bool { return os.Geteuid() == 0 }

// SignalName maps a signal number to its name.
func SignalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGSTOP:
		return "SIGSTOP"
	case syscall.SIGCONT:
		return "SIGCONT"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	}
	return fmt.Sprintf("SIG%d", int(sig))
}
