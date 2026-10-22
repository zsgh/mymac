package ai

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCacheRoundTrip checks that the verdicts survive a round trip.
func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-cache.json")
	c := NewCacheAt(path)

	if _, ok := c.Get("apps"); ok {
		t.Error("an empty cache must not have an entry")
	}
	entry := Entry{
		At:    time.Now(),
		Model: "test-model",
		Items: []Verdict{{Key: "com.example.app", Level: LevelHigh, Label: "建议清理", Reason: "占地方"}},
	}
	if err := c.Put("apps", entry); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("apps")
	if !ok {
		t.Fatal("the entry is gone")
	}
	if got.Model != "test-model" || len(got.Items) != 1 {
		t.Errorf("entry = %+v", got)
	}
	if v, ok := got.VerdictOf("com.example.app"); !ok || v.Label != "建议清理" {
		t.Errorf("verdict = %+v", v)
	}
}

// TestCacheMergeKeepsTheOldVerdicts checks the point of the cache.
//
// A full rebuild replaces everything; the cache mode only adds what is
// new, and the verdicts that are already there stay.
func TestCacheMergeKeepsTheOldVerdicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-cache.json")
	c := NewCacheAt(path)

	if _, err := c.Merge("apps", []Verdict{{Key: "a", Level: LevelLow, Label: "没事"}}, "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge("apps", []Verdict{{Key: "b", Level: LevelHigh, Label: "要删"}}, "m1"); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Get("apps")
	if len(got.Items) != 2 {
		t.Fatalf("the cache holds %d verdicts", len(got.Items))
	}
	if v, ok := got.VerdictOf("a"); !ok || v.Label != "没事" {
		t.Errorf("the old verdict is gone: %+v", v)
	}
	if v, ok := got.VerdictOf("b"); !ok || v.Level != LevelHigh {
		t.Errorf("the new verdict is missing: %+v", v)
	}
}

// TestCacheReplacesOnAFullRebuild checks that a verdict can be replaced.
func TestCacheReplacesOnAFullRebuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-cache.json")
	c := NewCacheAt(path)

	if _, err := c.Merge("apps", []Verdict{{Key: "a", Level: LevelLow, Label: "没事"}}, "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge("apps", []Verdict{{Key: "a", Level: LevelHigh, Label: "要删"}}, "m2"); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Get("apps")
	if len(got.Items) != 1 {
		t.Fatalf("the cache holds %d verdicts", len(got.Items))
	}
	if got.Items[0].Level != LevelHigh {
		t.Errorf("the verdict was not replaced: %+v", got.Items[0])
	}
	if got.Model != "m2" {
		t.Errorf("model = %q", got.Model)
	}
}

// TestABrokenCacheDoesNotBreakTheTool checks that a damaged cache file
// falls back to an empty cache.
func TestABrokenCacheDoesNotBreakTheTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-cache.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCacheAt(path)
	if _, ok := c.Get("apps"); ok {
		t.Error("a broken file must not produce an entry")
	}
	if _, err := c.Merge("apps", []Verdict{{Key: "a", Level: LevelLow}}, "m"); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Get("apps")
	if len(got.Items) != 1 {
		t.Errorf("the cache holds %d verdicts", len(got.Items))
	}
}

// TestTheCacheIsNotWorldReadable checks that the verdicts stay private.
func TestTheCacheIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-cache.json")
	c := NewCacheAt(path)
	if err := c.Put("apps", Entry{Items: []Verdict{{Key: "a"}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("the cache is readable by others: %v", info.Mode().Perm())
	}
}
