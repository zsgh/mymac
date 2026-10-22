package server

import (
	"testing"

	"mymac/internal/ai"
)

// TestResolveVerdictsByKey checks the normal case.
func TestResolveVerdictsByKey(t *testing.T) {
	items := []ai.Item{
		{Key: "net.qihoo.www.360-zip", Name: "360压缩大师"},
		{Key: "com.apple.Safari", Name: "Safari"},
	}
	verdicts := []ai.Verdict{
		{Key: "com.apple.Safari", Level: ai.LevelLow, Label: "留着"},
		{Key: "net.qihoo.www.360-zip", Level: ai.LevelHigh, Label: "删掉"},
	}
	got, dropped := resolveVerdicts(items, verdicts)
	if dropped != 0 {
		t.Errorf("dropped = %d", dropped)
	}
	if len(got) != 2 {
		t.Fatalf("got %d verdicts", len(got))
	}
	byKey := map[string]string{}
	for _, v := range got {
		byKey[v.Key] = v.Level
	}
	if byKey["com.apple.Safari"] != ai.LevelLow {
		t.Errorf("Safari = %q", byKey["com.apple.Safari"])
	}
	if byKey["net.qihoo.www.360-zip"] != ai.LevelHigh {
		t.Errorf("360 = %q", byKey["net.qihoo.www.360-zip"])
	}
}

// TestResolveVerdictsByName checks the case that actually happens.
//
// The model is asked to copy the identifier, but it often answers with the
// name instead. The verdict has to land on the right item anyway.
func TestResolveVerdictsByName(t *testing.T) {
	items := []ai.Item{
		{Key: "net.qihoo.www.360-zip", Name: "360压缩大师"},
		{Key: "com.apple.Safari", Name: "Safari"},
	}
	got, dropped := resolveVerdicts(items, []ai.Verdict{
		{Key: "360压缩大师", Level: ai.LevelMedium, Label: "按需保留"},
	})
	if dropped != 0 {
		t.Errorf("dropped = %d", dropped)
	}
	if len(got) != 1 || got[0].Key != "net.qihoo.www.360-zip" {
		t.Fatalf("got %+v", got)
	}
}

// TestResolveVerdictsByPrefix checks the case where the model cuts the
// name short.
func TestResolveVerdictsByPrefix(t *testing.T) {
	items := []ai.Item{
		{Key: "com.oray.sunlogin.macclient", Name: "向日葵远程控制"},
		{Key: "com.apple.Safari", Name: "Safari"},
	}
	got, dropped := resolveVerdicts(items, []ai.Verdict{
		{Key: "向日葵远程", Level: ai.LevelLow, Label: "正常"},
	})
	if dropped != 0 {
		t.Errorf("dropped = %d", dropped)
	}
	if len(got) != 1 || got[0].Key != "com.oray.sunlogin.macclient" {
		t.Fatalf("got %+v", got)
	}
}

// TestResolveVerdictsDropsTheUnknown checks that a verdict that cannot be
// placed is dropped instead of landing on the wrong item.
func TestResolveVerdictsDropsTheUnknown(t *testing.T) {
	items := []ai.Item{
		{Key: "com.apple.Safari", Name: "Safari"},
	}
	got, dropped := resolveVerdicts(items, []ai.Verdict{
		{Key: "某个不存在的应用", Level: ai.LevelLow, Label: "x"},
	})
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

// TestResolveVerdictsDropsTheAmbiguous checks that a prefix that matches
// two items is dropped: neither of them is certain.
func TestResolveVerdictsDropsTheAmbiguous(t *testing.T) {
	items := []ai.Item{
		{Key: "com.example.one", Name: "Example Helper"},
		{Key: "com.example.two", Name: "Example Helper Two"},
	}
	got, dropped := resolveVerdicts(items, []ai.Verdict{
		{Key: "Example", Level: ai.LevelLow, Label: "x"},
	})
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

// TestResolveVerdictsPrefersTheExactName checks that an exact name wins
// over a prefix.
func TestResolveVerdictsPrefersTheExactName(t *testing.T) {
	items := []ai.Item{
		{Key: "com.example.one", Name: "Example Helper"},
		{Key: "com.example.two", Name: "Example Helper Two"},
	}
	got, dropped := resolveVerdicts(items, []ai.Verdict{
		{Key: "Example Helper", Level: ai.LevelLow, Label: "x"},
	})
	if dropped != 0 {
		t.Errorf("dropped = %d", dropped)
	}
	if len(got) != 1 || got[0].Key != "com.example.one" {
		t.Fatalf("got %+v", got)
	}
}

// TestBatches checks that the items are asked about in batches.
func TestBatches(t *testing.T) {
	items := make([]ai.Item, 45)
	got := batches(items, 20)
	if len(got) != 3 {
		t.Fatalf("got %d batches", len(got))
	}
	if len(got[0]) != 20 || len(got[1]) != 20 || len(got[2]) != 5 {
		t.Errorf("the batches hold %d, %d and %d items", len(got[0]), len(got[1]), len(got[2]))
	}
	// A short list is one batch.
	if len(batches(items[:5], 20)) != 1 {
		t.Error("a short list must be one batch")
	}
}
