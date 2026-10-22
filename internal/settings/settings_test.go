package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSettings points the store at a temporary file.
func writeSettings(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return NewStore()
}

// TestGetWithoutAFile checks that a missing file means the defaults.
func TestGetWithoutAFile(t *testing.T) {
	s := writeSettings(t)
	got, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.Configured() {
		t.Error("a model must not be configured out of the box")
	}
	if !got.Docs.Enabled {
		t.Error("the documentation must be reachable out of the box")
	}
}

// TestSetAndGet checks that the settings survive a round trip.
func TestSetAndGet(t *testing.T) {
	s := writeSettings(t)
	cur, err := s.Update(func(v *Settings) {
		v.LLM = LLM{BaseURL: "https://api.example.com/v1", APIKey: "sk-1", Model: "m1"}
		v.Docs.Enabled = false
	})
	if err != nil {
		t.Fatal(err)
	}
	if cur.LLM.APIKey != "sk-1" {
		t.Errorf("key = %q", cur.LLM.APIKey)
	}

	got, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.BaseURL != "https://api.example.com/v1" || got.LLM.Model != "m1" {
		t.Errorf("llm = %+v", got.LLM)
	}
	if got.Docs.Enabled {
		t.Error("the documentation should be closed")
	}
}

// TestTheKeyIsNotLost checks that a change that does not touch the key keeps
// it: the interface never sees the key, so it cannot send it back.
func TestTheKeyIsNotLost(t *testing.T) {
	s := writeSettings(t)
	if _, err := s.Update(func(v *Settings) {
		v.LLM = LLM{BaseURL: "https://api.example.com/v1", APIKey: "sk-1", Model: "m1"}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(func(v *Settings) {
		v.LLM.Model = "m2"
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.APIKey != "sk-1" {
		t.Errorf("key = %q, want it to be kept", got.LLM.APIKey)
	}
	if got.LLM.Model != "m2" {
		t.Errorf("model = %q", got.LLM.Model)
	}
}

// TestTheFileIsNotWorldReadable checks that the key stays private.
func TestTheFileIsNotWorldReadable(t *testing.T) {
	s := writeSettings(t)
	if _, err := s.Update(func(v *Settings) {
		v.LLM = LLM{BaseURL: "https://api.example.com/v1", APIKey: "sk-secret", Model: "m1"}
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".config", "mymac", FileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("the settings file is readable by others: %v", info.Mode().Perm())
	}
}

// TestABrokenFileDoesNotBreakTheTool checks that a damaged settings file
// falls back to the defaults.
func TestABrokenFileDoesNotBreakTheTool(t *testing.T) {
	s := writeSettings(t)
	dir := filepath.Join(os.Getenv("HOME"), ".config", "mymac")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.Configured() {
		t.Error("a broken file must not configure a model")
	}
}
