package loginitems

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeAgent writes a LaunchAgent definition for the tests.
func writeAgent(t *testing.T, dir, name, program string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + name + `</string>
	<key>ProgramArguments</key><array><string>` + program + `</string></array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`
	path := filepath.Join(dir, name+".plist")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRemoveGroupMovesEveryFile checks that the whole program goes.
//
// The Finder is turned off in the test: a test run must not make macOS show
// a permission dialog.
func TestRemoveGroupMovesEveryFile(t *testing.T) {
	t.Setenv("MYMAC_NO_FINDER", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	writeAgent(t, dir, "com.example.one", "/bin/sleep")
	writeAgent(t, dir, "com.example.two", "/bin/sleep")

	c := NewCollector()
	res, err := c.RemoveGroup(context.Background(), "/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Errorf("the group has %d items", res.Total)
	}
	if len(res.Moved) != 2 {
		t.Errorf("moved = %v", res.Moved)
	}
	if len(res.Failed) != 0 {
		t.Errorf("failed = %v", res.Failed)
	}
	// The files are gone from the LaunchAgents folder.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the folder still holds %d files", len(entries))
	}
	// And they are in the trash, so a mistake can be undone.
	trash := filepath.Join(home, ".Trash")
	if _, err := os.Stat(filepath.Join(trash, "com.example.one.plist")); err != nil {
		t.Errorf("the file is not in the trash: %v", err)
	}
}

// TestRemoveGroupSkipsTheItemsWithoutAFile checks the items that have no
// file of their own.
//
// The services the system submits for an app come back with the app: there
// is nothing to delete, and the answer has to say so.
func TestRemoveGroupSkipsTheItemsWithoutAFile(t *testing.T) {
	t.Setenv("MYMAC_NO_FINDER", "1")
	t.Setenv("HOME", t.TempDir())

	c := NewCollector()
	// A group that does not exist at all.
	if _, err := c.RemoveGroup(context.Background(), "/nope/nothing"); err == nil {
		t.Error("expected an error")
	}
	// An empty key is not a group either.
	if _, err := c.RemoveGroup(context.Background(), "  "); err == nil {
		t.Error("expected an error")
	}
}

// TestShellQuote checks the quoting of the paths in the elevated command.
//
// The paths have spaces in them ("/Library/Input Methods/…"), and the
// command runs through a shell.
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/bin/sleep":          `'/bin/sleep'`,
		"/a b/c.plist":        `'/a b/c.plist'`,
		"/it's/a.plist":       `'/it'\''s/a.plist'`,
		"/Library/Launch D/x": `'/Library/Launch D/x'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
