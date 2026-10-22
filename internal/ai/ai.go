// Package ai asks a model to look at what mymac found and say what it
// thinks of it.
//
// The model is whatever the user configured: any service that speaks the
// OpenAI chat API. mymac holds no key of its own and talks to nothing but
// the endpoint the user entered.
//
// The verdicts are kept in a cache file: they are expensive to get and
// hardly ever change, so the model is only asked about what is new.
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

// Config describes the model to talk to.
type Config struct {
	// BaseURL is the root of the API, for example
	// https://api.openai.com/v1.
	BaseURL string
	// APIKey is the token of the endpoint.
	APIKey string
	// Model is the name of the model.
	Model string
	// Timeout limits one request. The analysis of a long list takes a
	// while on a thinking model, and it runs in the background, so the
	// limit is generous.
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
		cfg.Timeout = 10 * time.Minute
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// Configured reports whether the model can be reached.
func (c *Client) Configured() bool { return c.cfg.Configured() }

// Model returns the name of the model.
func (c *Client) Model() string { return c.cfg.Model }

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

// endpoint returns the URL of the chat completions API.
//
// The user may enter the root of the API ("https://api.openai.com/v1") or
// the whole path ("https://openrouter.ai/api/v1/chat/completions"); both
// have to work, and neither may end up with the path twice.
func (c Config) endpoint() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.endpoint(), bytes.NewReader(body))
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
