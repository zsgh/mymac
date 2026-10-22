// Package ai asks a local AI CLI to look at the startup items and rate them.
//
// The analysis runs through the claude CLI: mymac has no API key and no
// network of its own, and the CLI is already signed in on the machine. The
// items never leave the computer — the CLI talks to the model, mymac only
// hands over a list of labels and paths.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Risk levels.
const (
	RiskHigh   = "high"
	RiskMedium = "medium"
	RiskLow    = "low"
)

// Subject is one startup item as the AI sees it.
//
// Only the facts that matter for a verdict are passed on: what the item is
// called, what it runs, where it came from and whether it is running.
type Subject struct {
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Program string `json:"program,omitempty"`
	Source  string `json:"source,omitempty"`
	Plist   string `json:"plist,omitempty"`
	Loaded  bool   `json:"loaded"`
	Enabled bool   `json:"enabled"`
	PID     int    `json:"pid,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Assessment is the verdict about one item.
type Assessment struct {
	Label  string `json:"label"`
	Risk   string `json:"risk"`
	Reason string `json:"reason"`
}

// Analyzer runs the risk analysis.
type Analyzer struct {
	// Command is the CLI to run. Empty means "claude".
	Command string
	// Model is the model to ask for. Empty means the CLI's default.
	Model string
	// Timeout limits one analysis. The default is three minutes: a list
	// of fifty items takes a while.
	Timeout time.Duration
}

// ErrUnavailable reports that no AI CLI could be found.
var ErrUnavailable = errors.New("找不到 claude 命令，请先安装 Claude Code")

// Available reports whether the CLI can be used.
func (a *Analyzer) Available() bool {
	_, err := a.lookup()
	return err == nil
}

// lookup finds the CLI.
func (a *Analyzer) lookup() (string, error) {
	name := a.Command
	if name == "" {
		name = "claude"
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	// The CLI is often installed outside the PATH of a GUI application.
	for _, dir := range []string{
		filepath.Join(os.Getenv("HOME"), ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", ErrUnavailable
}

// Analyze asks the AI to rate the given items.
//
// The answer holds one assessment per item; the labels the AI did not
// mention are simply left out.
func (a *Analyzer) Analyze(ctx context.Context, subjects []Subject) ([]Assessment, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	bin, err := a.lookup()
	if err != nil {
		return nil, err
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"-p", Prompt(subjects),
		"--output-format", "text",
		"--no-session-persistence",
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// The CLI must not pick up a project of its own: the analysis is
	// about the items, not about the directory mymac was started in.
	cmd.Dir = os.TempDir()
	// The CLI must never ask for a tool: this is a text answer, and a
	// permission prompt would hang the request.
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")

	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("AI 分析超时（%s）", timeout)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("claude 失败: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("claude 失败: %w", err)
	}
	return ParseAssessments(string(out))
}

// Prompt builds the question that is sent to the AI.
func Prompt(subjects []Subject) string {
	var b strings.Builder
	b.WriteString("下面是一台 macOS 上的启动项清单。每行一个，字段用竖线分隔：\n")
	b.WriteString("标签 | 类型 | 程序路径 | 来源 | 状态\n\n")
	for _, s := range subjects {
		state := "已禁用"
		if s.Enabled {
			state = "已启用"
		}
		if s.PID > 0 {
			state += fmt.Sprintf("，运行中 pid %d", s.PID)
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s\n",
			s.Label, s.Kind, orDash(s.Program), orDash(s.Source), state)
	}
	b.WriteString(`
请评估每个启动项「该不该被信任」，只输出一个 JSON 数组，不要任何解释性文字，也不要用代码块：

[{"label": "原样的标签", "risk": "high 或 medium 或 low", "reason": "一句话中文理由"}]

判断标准：
- high（高风险）：来源可疑或行为危险——以 root 运行的可疑程序、开发者与路径对不上、
  藏在用户目录或临时目录里的可执行文件、名字伪装成系统组件的服务
- medium（中风险）：需要留意——自动更新器（会下载并执行代码）、装在 ~/Library 下的
  helper、没有 plist 的服务（由别的程序提交，来路不明）
- low（低风险）：可信——macOS 自带（com.apple.*）、装在 /Applications 下的应用自己的组件

清单里出现的每个标签都要有且只有一条评估。`)
	return b.String()
}

// orDash returns a dash for an empty value.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ParseAssessments reads the answer of the AI.
//
// The model is asked for a bare JSON array, but a model is a model: the
// answer may come wrapped in a code block or with a sentence around it. The
// parser looks for the array instead of trusting the format.
func ParseAssessments(out string) ([]Assessment, error) {
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, errors.New("AI 没有返回内容")
	}
	if i := strings.Index(text, "["); i >= 0 {
		if j := strings.LastIndex(text, "]"); j > i {
			text = text[i : j+1]
		}
	}
	var list []Assessment
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		return nil, fmt.Errorf("看不懂 AI 的回答: %w", err)
	}
	out2 := make([]Assessment, 0, len(list))
	for _, a := range list {
		a.Label = strings.TrimSpace(a.Label)
		a.Risk = strings.ToLower(strings.TrimSpace(a.Risk))
		a.Reason = strings.TrimSpace(a.Reason)
		if a.Label == "" {
			continue
		}
		switch a.Risk {
		case RiskHigh, RiskMedium, RiskLow:
		default:
			// An unknown level would be a lie in the table.
			a.Risk = RiskMedium
		}
		out2 = append(out2, a)
	}
	if len(out2) == 0 {
		return nil, errors.New("AI 没有给出任何评估")
	}
	return out2, nil
}
