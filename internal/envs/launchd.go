package envs

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"mymac/internal/config"
	"mymac/internal/errs"
)

// Launchctl is the slice of launchctl this package needs.
//
// The interface exists so the tests can substitute a fake and never touch the
// real launchd environment.
type Launchctl interface {
	// Setenv sets a variable in the launchd GUI domain.
	Setenv(ctx context.Context, key, value string) error
	// Unsetenv removes a variable from the launchd GUI domain.
	Unsetenv(ctx context.Context, key string) error
	// Getenv reads a variable back from the launchd GUI domain.
	// A missing variable returns ("", nil) because launchctl exits 0 for
	// unknown names.
	Getenv(ctx context.Context, key string) (string, error)
	// List returns every variable in the launchd GUI domain.
	List(ctx context.Context) (map[string]string, error)
}

// SystemLaunchctl talks to the real /bin/launchctl.
type SystemLaunchctl struct {
	// Path overrides the launchctl binary; tests point it at a stub.
	Path string
}

// NewSystemLaunchctl returns a launchctl backed by the real binary.
func NewSystemLaunchctl() *SystemLaunchctl { return &SystemLaunchctl{Path: "/bin/launchctl"} }

func (l *SystemLaunchctl) binary() string {
	if l.Path != "" {
		return l.Path
	}
	return "/bin/launchctl"
}

func (l *SystemLaunchctl) Setenv(ctx context.Context, key, value string) error {
	// launchctl takes the value as a single argv element, so no shell
	// quoting or escaping is needed for spaces, quotes, $ or newlines.
	out, err := exec.CommandContext(ctx, l.binary(), "setenv", key, value).CombinedOutput()
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "launchctl setenv %s 失败: %s", key, strings.TrimSpace(string(out)))
	}
	return nil
}

func (l *SystemLaunchctl) Unsetenv(ctx context.Context, key string) error {
	out, err := exec.CommandContext(ctx, l.binary(), "unsetenv", key).CombinedOutput()
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "launchctl unsetenv %s 失败: %s", key, strings.TrimSpace(string(out)))
	}
	return nil
}

func (l *SystemLaunchctl) Getenv(ctx context.Context, key string) (string, error) {
	out, err := exec.CommandContext(ctx, l.binary(), "getenv", key).Output()
	if err != nil {
		return "", errs.Wrap(err, errs.KindInternal, "launchctl getenv %s 失败", key)
	}
	// launchctl prints the value followed by exactly one newline, so only
	// that one newline is stripped. A value that itself ends in a newline
	// keeps it.
	// launchctl also exits 0 with empty output for an unknown variable, so
	// the caller has to treat "" as "not set".
	return strings.TrimSuffix(string(out), "\n"), nil
}

func (l *SystemLaunchctl) List(ctx context.Context) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, l.binary(), "print", launchDomain()).Output()
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "launchctl print %s 失败", launchDomain())
	}
	return parseLaunchdEnvironment(string(out)), nil
}

// launchDomain is the per-user launchd domain.
func launchDomain() string {
	return "gui/" + itoa(os.Getuid())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// parseLaunchdEnvironment extracts the environment block from
// `launchctl print gui/<uid>`.
//
// The real output looks like:
//
//	environment = {
//		SSH_AUTH_SOCK => /var/run/com.apple.launchd.0ukikaUqDw/Listeners
//	}
//
// A value that contains a newline is printed across several lines, so a line
// that is not a "KEY => VALUE" pair and not the closing brace continues the
// previous value.
func parseLaunchdEnvironment(out string) map[string]string {
	res := map[string]string{}
	inBlock := false
	var lastKey string

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "environment = {") {
				inBlock = true
			}
			continue
		}
		if trimmed == "}" {
			break
		}
		if key, val, ok := splitLaunchdEntry(trimmed); ok {
			res[key] = val
			lastKey = key
			continue
		}
		// Continuation of a multi-line value.
		if lastKey != "" {
			res[lastKey] += "\n" + line
		}
	}
	return res
}

// splitLaunchdEntry splits a "KEY => VALUE" line.
func splitLaunchdEntry(trimmed string) (string, string, bool) {
	idx := strings.Index(trimmed, "=>")
	if idx < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(trimmed[:idx])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.TrimSpace(trimmed[idx+2:]), true
}

// ApplyResult reports what happened when a variable was pushed to launchd.
type ApplyResult struct {
	Set     bool   `json:"set"`
	Value   string `json:"value"`
	Applied bool   `json:"applied"`
	Message string `json:"message,omitempty"`
}

// Apply pushes a variable into the launchd GUI domain and verifies the
// result by reading it back.
//
// The read-back matters: `launchctl setenv` can succeed while the value is
// rejected, and the UI must not claim success without proof.
func Apply(ctx context.Context, lc Launchctl, key, value string) (ApplyResult, error) {
	res := ApplyResult{}
	if err := lc.Setenv(ctx, key, value); err != nil {
		return res, err
	}
	got, err := lc.Getenv(ctx, key)
	if err != nil {
		return res, err
	}
	res.Value = got
	res.Set = got != ""
	res.Applied = got == value
	if !res.Applied {
		if res.Set {
			res.Message = "launchd 中的值与期望不一致（可能被其他程序覆盖）"
		} else {
			res.Message = "launchd 没有接受该变量"
		}
	}
	return res, nil
}

// Unset removes a variable from the launchd GUI domain.
func Unset(ctx context.Context, lc Launchctl, key string) error {
	return lc.Unsetenv(ctx, key)
}

// FakeLaunchctl is an in-memory Launchctl for tests.
type FakeLaunchctl struct {
	mu     sync.Mutex
	values map[string]string
	// FailSetenv makes Setenv return an error, for failure-path tests.
	FailSetenv bool
	// Mangle makes Setenv store a different value, to exercise the
	// read-back verification.
	Mangle bool
}

// NewFakeLaunchctl returns an empty fake.
func NewFakeLaunchctl() *FakeLaunchctl {
	return &FakeLaunchctl{values: map[string]string{}}
}

func (f *FakeLaunchctl) Setenv(ctx context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSetenv {
		return errs.New(errs.KindInternal, "fake launchctl: setenv failed")
	}
	if f.Mangle {
		value = value + "-mangled"
	}
	f.values[key] = value
	return nil
}

func (f *FakeLaunchctl) Unsetenv(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, key)
	return nil
}

func (f *FakeLaunchctl) Getenv(ctx context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key], nil
}
func (f *FakeLaunchctl) List(ctx context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out, nil
}

// ------------------------------------------------------------------ agent

// AgentState describes the login launch agent.
type AgentState struct {
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
	Path      string `json:"path"`
	Detail    string `json:"detail,omitempty"`
}

// agentPlist renders the login agent that replays the managed environment.
//
// RunAtLoad makes it fire at every login; the script calls launchctl setenv
// for each managed variable, which restores the launchd layer after a
// reboot (launchctl setenv does not survive one on its own).
func agentPlist(scriptPath string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + config.LaunchAgentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>` + scriptPath + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + config.Root() + `/launchagent.log</string>
	<key>StandardErrorPath</key>
	<string>` + config.Root() + `/launchagent.log</string>
</dict>
</plist>
`)
}

// applyScript renders the helper the launch agent runs.
func applyScript(vars []Var) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Generated by mymac. Replays the managed environment into the launchd GUI domain.\n")
	b.WriteString("set -u\n")
	for _, v := range vars {
		if v.Name == "PATH" {
			continue
		}
		b.WriteString("/bin/launchctl setenv " + shellQuote(v.Name) + " " + shellQuote(v.Value) + "\n")
	}
	b.WriteString("exit 0\n")
	return []byte(b.String())
}

// shellQuote renders a value as a single shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// AgentManager installs, loads and removes the login launch agent.
type AgentManager struct {
	lc         Launchctl
	plistPath  string
	scriptPath string
	uid        int
	binary     string
}

// NewAgentManager returns a manager for the login agent.
func NewAgentManager(lc Launchctl) *AgentManager {
	return NewAgentManagerWithBinary(lc, "/bin/launchctl")
}

// NewAgentManagerWithBinary returns a manager that calls the given launchctl
// binary. Tests point it at a stub so the real launchd is never touched.
func NewAgentManagerWithBinary(lc Launchctl, binary string) *AgentManager {
	if binary == "" {
		binary = "/bin/launchctl"
	}
	return &AgentManager{
		lc:         lc,
		plistPath:  config.LaunchAgentPlist(),
		scriptPath: config.EnvApplySh(),
		uid:        os.Getuid(),
		binary:     binary,
	}
}

// Install writes the helper script and the plist, then loads the agent.
func (a *AgentManager) Install(ctx context.Context, vars []Var) error {
	if err := WriteFileAtomic(a.scriptPath, applyScript(vars), 0o700); err != nil {
		return err
	}
	if err := WriteFileAtomic(a.plistPath, agentPlist(a.scriptPath), 0o644); err != nil {
		return err
	}
	return a.Load(ctx)
}

// Load bootstraps the agent into the user's launchd domain.
func (a *AgentManager) Load(ctx context.Context) error {
	// bootout first so a reload is idempotent; a missing agent is fine.
	_ = a.Unload(ctx)
	cmd := exec.CommandContext(ctx, a.binary, "bootstrap", a.domain(), a.plistPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "launchctl bootstrap 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Unload removes the agent from the launchd domain.
//
// Note that bootout does not clear the variables the agent set, so callers
// must unsetenv explicitly when they really want them gone.
func (a *AgentManager) Unload(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, a.binary, "bootout", a.domain()+"/"+config.LaunchAgentLabel)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// "Could not find service" simply means it was not loaded.
		if strings.Contains(string(out), "Could not find service") ||
			strings.Contains(string(out), "not found") {
			return nil
		}
		return errs.Wrap(err, errs.KindInternal, "launchctl bootout 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall removes the agent and its helper script.
func (a *AgentManager) Uninstall(ctx context.Context) error {
	if err := a.Unload(ctx); err != nil {
		return err
	}
	if err := os.Remove(a.plistPath); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(err, errs.KindPermission, "无法删除 %s", a.plistPath)
	}
	if err := os.Remove(a.scriptPath); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(err, errs.KindPermission, "无法删除 %s", a.scriptPath)
	}
	return nil
}

func (a *AgentManager) domain() string {
	return "gui/" + itoa(a.uid)
}

// State reports whether the agent is installed and loaded.
func (a *AgentManager) State(ctx context.Context) AgentState {
	st := AgentState{Path: a.plistPath}
	if _, err := os.Stat(a.plistPath); err == nil {
		st.Installed = true
	} else {
		st.Detail = "未安装登录代理，重启后环境变量不会自动恢复"
		return st
	}
	cmd := exec.CommandContext(ctx, a.binary, "print", a.domain()+"/"+config.LaunchAgentLabel)
	if err := cmd.Run(); err == nil {
		st.Loaded = true
	} else {
		st.Detail = "已安装但未加载，下次登录时生效"
	}
	return st
}

// timeoutCtx returns a context with a short deadline, used for the many
// small launchctl calls.
func timeoutCtx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
