package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAsk talks to a fake OpenAI compatible service.
//
// The test must not need a key and must not reach the network: the point is
// that the request is built the way the service expects it and that the
// answer is read out of it.
func TestAsk(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		var req chatRequest
		_ = json.Unmarshal(buf, &req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"收到"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test", Model: "test-model"})
	out, err := c.Ask(context.Background(), "system", "question")
	if err != nil {
		t.Fatal(err)
	}
	if out != "收到" {
		t.Errorf("answer = %q", out)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if gotModel != "test-model" {
		t.Errorf("model = %q", gotModel)
	}
	if !strings.Contains(gotBody, "question") {
		t.Errorf("the question is missing: %s", gotBody)
	}
}

// TestAskWithoutAKey checks that a service without a key still gets a
// request: some local services do not want one.
func TestAskWithoutAKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "local"})
	if _, err := c.Ask(context.Background(), "s", "q"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("authorization = %q, want none", gotAuth)
	}
}

// TestAskReportsTheError checks that a service that says no is reported.
func TestAskReportsTheError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "m"})
	_, err := c.Ask(context.Background(), "s", "q")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error = %v", err)
	}
}

// TestAnalyzeAgainstTheFakeService checks the whole path: the question goes
// out, the answer comes back as assessments.
func TestAnalyzeAgainstTheFakeService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":
			"[{\"label\":\"com.example.agent\",\"risk\":\"high\",\"reason\":\"可疑\"}]"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "m"})
	got, err := c.Analyze(context.Background(), []Subject{{Label: "com.example.agent", Kind: "launchAgent"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d assessments", len(got))
	}
	if got[0].Risk != RiskHigh || got[0].Reason != "可疑" {
		t.Errorf("assessment = %+v", got[0])
	}
}
