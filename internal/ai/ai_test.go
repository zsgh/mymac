package ai

import (
	"context"
	"strings"
	"testing"
)

// TestUnconfiguredClient checks that a client without a model says so
// instead of pretending to work.
func TestUnconfiguredClient(t *testing.T) {
	c := New(Config{})
	if c.Configured() {
		t.Error("the client must not be configured")
	}
	if _, err := c.Judge(context.Background(), "system", "q", []Item{{Key: "a"}}); err == nil {
		t.Error("expected an error")
	}
	if _, err := c.Ask(context.Background(), "system", "q"); err == nil {
		t.Error("expected an error")
	}
}

// TestItemListHoldsTheFacts checks that the list carries what the model
// needs to judge.
func TestItemListHoldsTheFacts(t *testing.T) {
	p := ItemList([]Item{
		{Key: "com.example.agent", Name: "com.example.agent", Kind: "launchAgent", Path: "/Applications/Example.app/Contents/MacOS/Example", Size: 2048, Extra: map[string]string{"运行中": "是"}},
		{Key: "com.example.other", Name: "com.example.other", Kind: "service"},
	})
	for _, want := range []string{
		"com.example.agent",
		"/Applications/Example.app/Contents/MacOS/Example",
		"launchAgent",
		"2 KB",
		"运行中=是",
		"com.example.other",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the list is missing %q", want)
		}
	}
}

// TestParseVerdicts checks the answer of the model.
//
// A model is a model: the answer may come in a code block, with a sentence
// around it, or with a level nobody knows.
func TestParseVerdicts(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"plain", `[{"key":"a","level":"high","label":"要删","reason":"x"}]`, 1},
		{"code block", "```json\n[{\"key\":\"a\",\"level\":\"low\",\"label\":\"没事\",\"reason\":\"x\"}]\n```", 1},
		{"with a sentence", "这是我的分析：\n[{\"key\":\"a\",\"level\":\"medium\",\"label\":\"留意\",\"reason\":\"x\"}]\n希望有用", 1},
		{"several", `[{"key":"a","level":"high","label":"要删","reason":"x"},{"key":"b","level":"low","label":"没事","reason":"y"}]`, 2},
		{"empty key", `[{"key":"  ","level":"high","label":"x","reason":"x"}]`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseVerdicts(c.out)
			if c.want == 0 {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != c.want {
				t.Fatalf("got %d verdicts, want %d", len(got), c.want)
			}
		})
	}
}

// TestParseVerdictsUnknownLevel checks that a level nobody knows does not
// end up in the table as a made up verdict.
func TestParseVerdictsUnknownLevel(t *testing.T) {
	got, err := ParseVerdicts(`[{"key":"a","level":"very dangerous","label":"x","reason":"x"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Level != LevelMedium {
		t.Errorf("level = %q, want %q", got[0].Level, LevelMedium)
	}
}

// TestParseVerdictsRejectsGarbage checks that a useless answer is an error
// and not an empty list.
func TestParseVerdictsRejectsGarbage(t *testing.T) {
	if _, err := ParseVerdicts("我不知道"); err == nil {
		t.Error("expected an error")
	}
	if _, err := ParseVerdicts(""); err == nil {
		t.Error("expected an error")
	}
}
