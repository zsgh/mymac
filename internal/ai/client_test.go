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

// TestTheWholeEndpointIsAccepted checks that a full path works too.
//
// The user may paste the whole address of the endpoint, and it must not
// end up with the path twice.
func TestTheWholeEndpointIsAccepted(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL + "/v1/chat/completions", Model: "m"})
	if _, err := c.Ask(context.Background(), "s", "q"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
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

// TestJudgeAgainstTheFakeService checks the whole path: the question goes
// out, the answer comes back as verdicts.
func TestJudgeAgainstTheFakeService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":
			"[{\"key\":\"com.example.agent\",\"level\":\"high\",\"label\":\"可疑\",\"reason\":\"可疑\"}]"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "m"})
	got, err := c.Judge(context.Background(), "system", "question", []Item{{Key: "com.example.agent", Name: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d verdicts", len(got))
	}
	if got[0].Level != LevelHigh || got[0].Reason != "可疑" {
		t.Errorf("verdict = %+v", got[0])
	}
}
