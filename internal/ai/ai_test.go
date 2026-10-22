package ai

import (
	"context"
	"strings"
	"testing"
)

// TestParseAssessments checks the answer of the AI.
//
// A model is a model: the answer may come in a code block, with a sentence
// around it, or with a level nobody knows. The parser has to survive all of
// that without making up a verdict.
func TestParseAssessments(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"plain", `[{"label":"a","risk":"high","reason":"x"}]`, 1},
		{"code block", "```json\n[{\"label\":\"a\",\"risk\":\"low\",\"reason\":\"x\"}]\n```", 1},
		{"with a sentence", "这是我的分析：\n[{\"label\":\"a\",\"risk\":\"medium\",\"reason\":\"x\"}]\n希望有用", 1},
		{"several", `[{"label":"a","risk":"high","reason":"x"},{"label":"b","risk":"low","reason":"y"}]`, 2},
		{"empty label", `[{"label":"  ","risk":"high","reason":"x"}]`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseAssessments(c.out)
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
				t.Fatalf("got %d assessments, want %d", len(got), c.want)
			}
		})
	}
}

// TestParseAssessmentsUnknownLevel checks that a level nobody knows does
// not end up in the table as a made up verdict.
func TestParseAssessmentsUnknownLevel(t *testing.T) {
	got, err := ParseAssessments(`[{"label":"a","risk":"very dangerous","reason":"x"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Risk != RiskMedium {
		t.Errorf("risk = %q, want %q", got[0].Risk, RiskMedium)
	}
}

// TestParseAssessmentsRejectsGarbage checks that a useless answer is an
// error and not an empty list.
func TestParseAssessmentsRejectsGarbage(t *testing.T) {
	if _, err := ParseAssessments("我不知道"); err == nil {
		t.Error("expected an error")
	}
	if _, err := ParseAssessments(""); err == nil {
		t.Error("expected an error")
	}
}

// TestPromptHoldsTheFacts checks that the question carries what the AI
// needs to judge.
func TestPromptHoldsTheFacts(t *testing.T) {
	p := Prompt([]Subject{
		{Label: "com.example.agent", Kind: "launchAgent", Program: "/Applications/Example.app/Contents/MacOS/Example", Source: "/Users/x/Library/LaunchAgents", Enabled: true, PID: 42},
		{Label: "com.example.other", Kind: "service"},
	})
	for _, want := range []string{
		"com.example.agent",
		"/Applications/Example.app/Contents/MacOS/Example",
		"launchAgent",
		"pid 42",
		"com.example.other",
		`"risk"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the question is missing %q", want)
		}
	}
}

// TestAvailableWithoutTheCLI checks that a missing CLI is reported instead
// of panicking.
func TestAvailableWithoutTheCLI(t *testing.T) {
	a := &Analyzer{Command: "mymac-does-not-exist"}
	if a.Available() {
		t.Error("the analyzer must not be available")
	}
	if _, err := a.Analyze(context.Background(), []Subject{{Label: "a"}}); err == nil {
		t.Error("expected an error")
	}
}
