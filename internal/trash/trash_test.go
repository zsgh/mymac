package trash

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildFinderScript checks the AppleScript generation.
func TestBuildFinderScript(t *testing.T) {
	one := buildFinderScript([]string{"/tmp/a b.txt"})
	if !strings.Contains(one, `POSIX file "/tmp/a b.txt"`) {
		t.Errorf("single = %q", one)
	}
	if strings.Count(one, "POSIX file") != 1 {
		t.Errorf("single should have one item: %q", one)
	}

	many := buildFinderScript([]string{"/tmp/a", "/tmp/b"})
	if strings.Count(many, "POSIX file") != 2 {
		t.Errorf("many = %q", many)
	}
	if !strings.Contains(many, "delete {") {
		t.Errorf("many = %q", many)
	}
}

// TestAppleStringEscaping checks that a path with a quote does not break the
// AppleScript.
func TestAppleStringEscaping(t *testing.T) {
	got := appleString(`/tmp/say "hi".txt`)
	if !strings.Contains(got, `\"`) {
		t.Errorf("quote not escaped: %q", got)
	}
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("not quoted: %q", got)
	}
	// A backslash must be escaped too.
	got = appleString(`/tmp/back\slash`)
	if strings.Count(got, `\\`) != 1 {
		t.Errorf("backslash not escaped: %q", got)
	}
}

// TestMoveMissingPath reports the missing file instead of failing hard.
func TestMoveMissingPath(t *testing.T) {
	res, err := Move(context.Background(), []string{filepath.Join(t.TempDir(), "nope")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 0 {
		t.Errorf("moved = %v", res.Moved)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("failed = %v", res.Failed)
	}
	if res.Failed[0].Kind != "not_found" {
		t.Errorf("kind = %q", res.Failed[0].Kind)
	}
}

// TestMoveEmpty does nothing.
func TestMoveEmpty(t *testing.T) {
	res, err := Move(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 0 || len(res.Failed) != 0 {
		t.Errorf("res = %+v", res)
	}
}

// TestMoveToTrashDir moves a file into ~/.Trash.
//
// The test uses a temporary HOME so the real Trash is never touched.
func TestMoveToTrashDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := filepath.Join(home, "doomed.txt")
	if err := os.WriteFile(src, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrashDir(src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the source file is still there")
	}
	trashed := filepath.Join(home, ".Trash", "doomed.txt")
	if _, err := os.Stat(trashed); err != nil {
		t.Errorf("the file is not in the trash: %v", err)
	}
}

// TestMoveToTrashDirKeepsExistingName makes sure a second file with the
// same name does not overwrite the first one.
func TestMoveToTrashDirKeepsExistingName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	first := filepath.Join(home, "same.txt")
	if err := os.WriteFile(first, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrashDir(first); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(home, "same.txt")
	if err := os.WriteFile(second, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveToTrashDir(second); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".Trash"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("trash has %d entries, want 2", len(entries))
	}
}
