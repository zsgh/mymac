package ports

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"mymac/internal/errs"
	"mymac/internal/rules"
)

// Errors of the create time validation.
var (
	ErrMissingCreateTime  = errors.New("缺少 create_time")
	ErrUnknownCreateTime  = errors.New("无法获取进程创建时间")
	ErrCreateTimeMismatch = errors.New("进程创建时间不一致")
)

// validateCreateTime checks the creation time the frontend sent.
//
// The PID may have been reused since the list was rendered; the creation
// time is what tells the two apart.
func validateCreateTime(actual, provided int64) error {
	if provided <= 0 {
		return ErrMissingCreateTime
	}
	if actual <= 0 {
		return ErrUnknownCreateTime
	}
	if actual != provided {
		return ErrCreateTimeMismatch
	}
	return nil
}

// isPermissionErr reports whether the error is a permission problem.
func isPermissionErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, process.ErrorNotPermitted) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "not permitted") ||
		strings.Contains(lower, "access is denied") ||
		strings.Contains(lower, "access denied")
}

// isNoSuchProcessErr reports whether the process is gone.
func isNoSuchProcessErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, process.ErrorProcessNotRunning) || errors.Is(err, os.ErrProcessDone) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such process") ||
		strings.Contains(lower, "process does not exist") ||
		strings.Contains(lower, "process already finished")
}

// kernelThreadPatterns are the names of the kernel threads.
var kernelThreadPatterns = []string{
	"kthreadd", "kworker*", "ksoftirqd*", "migration*", "rcu_*",
	"watchdog*", "cpuhp*", "kswapd*", "kcompactd*", "khugepaged",
	"kdevtmpfs", "netns", "kauditd", "scsi_eh*", "jbd2*", "xfs-*",
	"ext4-*", "oom_reaper", "zswap*", "irq/*", "idle_inject*",
	"smpboot*", "kernel_task",
}

// isKernelThread recognises the kernel threads with a conservative naming
// rule.
func isKernelThread(pid, ppid int32, name, exe string) bool {
	if pid <= 0 {
		return true
	}
	for _, pattern := range kernelThreadPatterns {
		p := strings.ReplaceAll(pattern, "/", "\x00")
		n := strings.ReplaceAll(name, "/", "\x00")
		if ok, _ := path.Match(p, n); ok {
			return true
		}
	}
	if runtime.GOOS == "linux" && ppid <= 2 && exe == "" && name != "" {
		return true
	}
	return false
}

// hardGuardReason returns the reason a process must never be touched.
func hardGuardReason(pid, self int32, ppid int32, name, exe string) string {
	if pid == self {
		return "mymac 自身进程"
	}
	if pid <= 1 {
		return "PID 1 或内核进程"
	}
	if isKernelThread(pid, ppid, name, exe) {
		return "内核线程"
	}
	return ""
}

// permissionMessage explains a permission failure.
func permissionMessage(prefix string, err error) string {
	detail := ""
	if err != nil {
		detail = ": " + err.Error()
	}
	return fmt.Sprintf("权限不足，%s%s。mymac 不会自动提权，请使用有权限的账户运行。", prefix, detail)
}

// ActionResult is the result of a process operation.
type ActionResult struct {
	OK          bool   `json:"ok"`
	Message     string `json:"message"`
	PID         int32  `json:"pid"`
	Action      string `json:"action"`
	ProcessName string `json:"process_name,omitempty"`
	NewPID      int32  `json:"new_pid,omitempty"`
	TimedOut    bool   `json:"timed_out,omitempty"`
	NeedsKill   bool   `json:"needs_kill,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms,omitempty"`
}

// ActionManager performs the process operations.
type ActionManager struct {
	store   *rules.Store
	timeout time.Duration
}

// NewActionManager returns an action manager.
func NewActionManager(store *rules.Store, timeout time.Duration) *ActionManager {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &ActionManager{store: store, timeout: timeout}
}

// Do performs stop, kill or restart.
//
// The create time is mandatory: without it a recycled PID could be hit by
// mistake.
func (m *ActionManager) Do(ctx context.Context, action string, pid int32, createTime int64) (ActionResult, int) {
	started := time.Now()
	result, status := m.do(ctx, action, pid, createTime)
	result.ElapsedMS = time.Since(started).Milliseconds()
	return result, status
}

func (m *ActionManager) do(ctx context.Context, action string, pid int32, createTime int64) (ActionResult, int) {
	result := ActionResult{PID: pid, Action: action}
	fail := func(status int, message string) (ActionResult, int) {
		result.Message = message
		return result, status
	}

	switch action {
	case "stop", "kill", "restart":
	default:
		return fail(httpStatusBadRequest, "不支持的进程操作")
	}
	if pid <= 0 {
		return fail(httpStatusBadRequest, "PID 必须是正整数")
	}
	if createTime <= 0 {
		return fail(httpStatusBadRequest, "缺少 create_time；为防止 PID 复用误杀，操作必须携带进程创建时间")
	}

	proc, err := process.NewProcess(pid)
	if err != nil {
		if isNoSuchProcessErr(err) {
			return fail(httpStatusNotFound, "进程不存在或已经退出")
		}
		return fail(httpStatusInternal, "读取进程失败: "+err.Error())
	}

	sub, name, exe, ppid := m.subject(proc)
	result.ProcessName = name
	if name == "" {
		result.ProcessName = "未知进程"
	}

	if reason := hardGuardReason(pid, int32(os.Getpid()), ppid, name, exe); reason != "" {
		return fail(httpStatusForbidden, "该进程属于系统保护范围："+reason)
	}
	if ref := m.store.NewEvaluator().Protection(sub); ref != nil {
		return fail(httpStatusForbidden, "该进程属于系统保护范围："+ref.Reason())
	}

	actual, ctErr := proc.CreateTime()
	if ctErr != nil {
		return fail(httpStatusForbidden, "无法确认进程创建时间，已拒绝操作: "+ctErr.Error())
	}
	if verr := validateCreateTime(actual, createTime); verr != nil {
		switch {
		case errors.Is(verr, ErrCreateTimeMismatch):
			return fail(httpStatusConflict, "进程创建时间已变化（PID 可能被复用），已拒绝操作")
		case errors.Is(verr, ErrMissingCreateTime):
			return fail(httpStatusBadRequest, "缺少有效的 create_time")
		default:
			return fail(httpStatusForbidden, "无法确认进程创建时间，已拒绝操作")
		}
	}

	// A fresh handle right before the signal, to shrink the PID reuse
	// window as much as possible.
	fresh, err := process.NewProcess(pid)
	if err != nil {
		if isNoSuchProcessErr(err) {
			return fail(httpStatusNotFound, "进程不存在或已经退出")
		}
		return fail(httpStatusInternal, "读取进程失败: "+err.Error())
	}
	freshTime, err := fresh.CreateTime()
	if err != nil {
		return fail(httpStatusForbidden, "无法确认进程创建时间，已拒绝操作: "+err.Error())
	}
	if verr := validateCreateTime(freshTime, createTime); verr != nil {
		if errors.Is(verr, ErrCreateTimeMismatch) {
			return fail(httpStatusConflict, "进程创建时间已变化（PID 可能被复用），已拒绝操作")
		}
		return fail(httpStatusForbidden, "无法确认进程创建时间，已拒绝操作")
	}

	switch action {
	case "stop":
		return m.stop(ctx, fresh, result)
	case "kill":
		return m.kill(ctx, fresh, result)
	case "restart":
		return m.restart(ctx, fresh, result)
	}
	return fail(httpStatusBadRequest, "不支持的进程操作")
}

// stop sends SIGTERM and waits for the process to exit.
func (m *ActionManager) stop(ctx context.Context, proc *process.Process, result ActionResult) (ActionResult, int) {
	fail := func(status int, message string) (ActionResult, int) {
		result.Message = message
		return result, status
	}
	if err := proc.TerminateWithContext(ctx); err != nil {
		if isNoSuchProcessErr(err) {
			return fail(httpStatusNotFound, "进程已经退出")
		}
		if isPermissionErr(err) {
			return fail(httpStatusForbidden, permissionMessage("停止进程", err))
		}
		return fail(httpStatusInternal, "发送停止信号失败: "+err.Error())
	}
	if m.waitExit(proc.Pid) {
		result.OK = true
		result.Message = "进程已停止"
		return result, httpStatusOK
	}
	result.TimedOut = true
	result.NeedsKill = true
	result.Message = fmt.Sprintf("已发送停止信号，但进程未在 %s 内退出，可改用强杀", m.timeout)
	return result, httpStatusOK
}

// kill sends SIGKILL and waits for the process to exit.
func (m *ActionManager) kill(ctx context.Context, proc *process.Process, result ActionResult) (ActionResult, int) {
	fail := func(status int, message string) (ActionResult, int) {
		result.Message = message
		return result, status
	}
	if err := proc.KillWithContext(ctx); err != nil {
		if isNoSuchProcessErr(err) {
			return fail(httpStatusNotFound, "进程已经退出")
		}
		if isPermissionErr(err) {
			return fail(httpStatusForbidden, permissionMessage("强制结束进程", err))
		}
		return fail(httpStatusInternal, "发送强杀信号失败: "+err.Error())
	}
	if m.waitExit(proc.Pid) {
		result.OK = true
		result.Message = "进程已被强制结束"
		return result, httpStatusOK
	}
	result.TimedOut = true
	result.Message = fmt.Sprintf("已发送强杀信号，但进程未在 %s 内退出", m.timeout)
	return result, httpStatusOK
}

// waitExit waits for a process to disappear.
func (m *ActionManager) waitExit(pid int32) bool {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, err := process.PidExistsWithContext(ctx, pid)
		if err == nil && !exists {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// subject builds the rule matching subject of a process.
func (m *ActionManager) subject(proc *process.Process) (rules.Subject, string, string, int32) {
	sub := rules.Subject{PID: proc.Pid}
	if name, err := proc.Name(); err == nil {
		sub.Name = strings.TrimSpace(name)
		sub.NameKnown = sub.Name != ""
	}
	if exe, err := proc.Exe(); err == nil {
		sub.Exe = strings.TrimSpace(exe)
		sub.ExeKnown = sub.Exe != ""
	}
	if username, err := proc.Username(); err == nil {
		sub.Username = strings.TrimSpace(username)
		sub.UserKnown = sub.Username != ""
	}
	if ppid, err := proc.Ppid(); err == nil {
		sub.PPID = ppid
	}
	return sub, sub.Name, sub.Exe, sub.PPID
}

// HTTP status codes used by the action results.
const (
	httpStatusBadRequest = 400
	httpStatusForbidden  = 403
	httpStatusNotFound   = 404
	httpStatusConflict   = 409
	httpStatusInternal   = 500
	httpStatusOK         = 200
)

// AsError converts a failed action into an error with a kind.
func (r ActionResult) AsError() error {
	switch {
	case r.TimedOut:
		return errs.New(errs.KindFileBusy, "%s", r.Message)
	case r.Message == "":
		return nil
	}
	return &errs.Error{Kind: errs.KindInternal, Message: r.Message}
}
