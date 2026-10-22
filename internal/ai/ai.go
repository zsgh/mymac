// Package ai asks a model to look at the startup items and the processes and
// to say what it thinks of them.
//
// The model is whatever the user configured: any service that speaks the
// OpenAI chat API. mymac holds no key of its own and talks to nothing but
// the endpoint the user entered.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// Config describes the model to talk to.
type Config struct {
	// BaseURL is the root of the API, for example
	// https://api.openai.com/v1.
	BaseURL string
	// APIKey is the token of the endpoint.
	APIKey string
	// Model is the name of the model.
	Model string
	// Timeout limits one request.
	Timeout time.Duration
}

// Configured reports whether the model can be reached.
func (c Config) Configured() bool {
	return c.BaseURL != "" && c.Model != ""
}

// ErrNotConfigured reports that no model has been set up.
var ErrNotConfigured = errors.New("还没有配置大模型")

// Client talks to the model.
type Client struct {
	cfg  Config
	http *http.Client
}

// Provider hands out the client for the current settings.
//
// The settings can change while mymac runs, so the client is looked up
// every time instead of being kept.
type Provider func() *Client

// New returns a client for the given configuration.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Minute
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// Configured reports whether the model can be reached.
func (c *Client) Configured() bool { return c.cfg.Configured() }

// chatRequest is the body of a chat completion.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the answer of a chat completion.
type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Ask sends a question and returns the answer.
func (c *Client) Ask(ctx context.Context, system, question string) (string, error) {
	if !c.Configured() {
		return "", ErrNotConfigured
	}
	body, err := json.Marshal(chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: question},
		},
	})
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("连不上大模型: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("看不懂大模型的回答: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("大模型报错: %s", out.Error.Message)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("大模型返回 %d", res.StatusCode)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("大模型没有回答")
	}
	return out.Choices[0].Message.Content, nil
}

// Analyze asks the model to rate the given startup items.
//
// The answer holds one assessment per item; the labels the model did not
// mention are simply left out.
func (c *Client) Analyze(ctx context.Context, subjects []Subject) ([]Assessment, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	out, err := c.Ask(ctx, riskSystem, RiskQuestion(subjects))
	if err != nil {
		return nil, err
	}
	return ParseAssessments(out)
}

// riskSystem tells the model what it is looking at.
const riskSystem = `你是一个 macOS 系统安全助手。你的任务是评估「开机自启项」的风险。
你只看得到给到你的事实（标签、类型、程序路径、来源目录、是否在运行），
看不到的内容不要臆测。回答用中文，简短直接。`

// RiskQuestion builds the question that is sent to the model.
func RiskQuestion(subjects []Subject) string {
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

// ---------------------------------------------------------------- processes

// ProcessSubject is one group of processes as the model sees it.
//
// The processes are grouped by the name of the executable: the user does
// not know what "python3.11" is, but the paths and the command line say a
// lot more than the name does.
type ProcessSubject struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Paths []string `json:"paths,omitempty"`
	Users []string `json:"users,omitempty"`
	Args  []string `json:"args,omitempty"`
}

// ProcessNote is what the model says about a group of processes.
type ProcessNote struct {
	Name string `json:"name"`
	// Summary says what the program is.
	Summary string `json:"summary"`
	// Purpose says what the user is using it for.
	Purpose string `json:"purpose,omitempty"`
	// Risk is high, medium or low.
	Risk string `json:"risk,omitempty"`
}

// AnalyzeProcesses asks the model what the running processes are.
func (c *Client) AnalyzeProcesses(ctx context.Context, subjects []ProcessSubject) ([]ProcessNote, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	out, err := c.Ask(ctx, processSystem, ProcessQuestion(subjects))
	if err != nil {
		return nil, err
	}
	return ParseProcessNotes(out)
}

// processSystem tells the model what it is looking at.
const processSystem = `你是一个 macOS 系统助手。用户看不懂进程表里那些进程是干什么的，
你的任务是看路径和命令行，告诉他每个进程是什么、在替谁干活。
你看不到的东西不要臆测，回答用中文，简短直接。`

// ProcessQuestion builds the question that is sent to the model.
func ProcessQuestion(subjects []ProcessSubject) string {
	var b strings.Builder
	b.WriteString("下面是一台 macOS 上正在运行的进程，按可执行文件名分组。\n")
	b.WriteString("每行一组，字段用竖线分隔：名字 | 个数 | 路径 | 用户 | 命令行\n\n")
	for _, s := range subjects {
		fmt.Fprintf(&b, "%s | %d | %s | %s | %s\n",
			s.Name, s.Count,
			strings.Join(orEmpty(s.Paths), ", "),
			strings.Join(orEmpty(s.Users), ", "),
			strings.Join(orEmpty(s.Args), " "))
	}
	b.WriteString(`
请解释每一组进程是干什么的，只输出一个 JSON 数组，不要解释性文字，也不要用代码块：

[{"name": "原样的名字", "summary": "一句话说明它是什么", "purpose": "它在替用户做什么", "risk": "high 或 medium 或 low"}]

判断标准：
- high：来路不明或者危险——藏在用户目录里的可执行文件、名字伪装成系统组件的、
  看不出用途却常驻的
- medium：需要留意——自动更新器、下载并执行代码的、来路不明的脚本
- low：正常——系统组件、知名应用的子进程、用户自己跑的开发工具

每一组都要有且只有一条。`)
	return b.String()
}

// orEmpty joins a list, or returns a dash for an empty one.
func orEmpty(list []string) []string {
	if len(list) == 0 {
		return []string{"-"}
	}
	return list
}

// ParseProcessNotes reads the answer of the model.
func ParseProcessNotes(out string) ([]ProcessNote, error) {
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, errors.New("大模型没有返回内容")
	}
	if i := strings.Index(text, "["); i >= 0 {
		if j := strings.LastIndex(text, "]"); j > i {
			text = text[i : j+1]
		}
	}
	var list []ProcessNote
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		return nil, fmt.Errorf("看不懂大模型的回答: %w", err)
	}
	res := make([]ProcessNote, 0, len(list))
	for _, n := range list {
		n.Name = strings.TrimSpace(n.Name)
		n.Summary = strings.TrimSpace(n.Summary)
		n.Purpose = strings.TrimSpace(n.Purpose)
		n.Risk = strings.ToLower(strings.TrimSpace(n.Risk))
		if n.Name == "" {
			continue
		}
		switch n.Risk {
		case RiskHigh, RiskMedium, RiskLow:
		default:
			n.Risk = ""
		}
		res = append(res, n)
	}
	if len(res) == 0 {
		return nil, errors.New("大模型没有给出任何说明")
	}
	return res, nil
}

// ParseAssessments reads the answer of the model.
//
// The model is asked for a bare JSON array, but a model is a model: the
// answer may come wrapped in a code block or with a sentence around it. The
// parser looks for the array instead of trusting the format.
func ParseAssessments(out string) ([]Assessment, error) {
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, errors.New("大模型没有返回内容")
	}
	if i := strings.Index(text, "["); i >= 0 {
		if j := strings.LastIndex(text, "]"); j > i {
			text = text[i : j+1]
		}
	}
	var list []Assessment
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		return nil, fmt.Errorf("看不懂大模型的回答: %w", err)
	}
	res := make([]Assessment, 0, len(list))
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
		res = append(res, a)
	}
	if len(res) == 0 {
		return nil, errors.New("大模型没有给出任何评估")
	}
	return res, nil
}
