package loginitems

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writePlist writes a LaunchAgent definition for the tests.
func writePlist(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const samplePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.example.agent</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/agent</string>
		<string>--daemon</string>
	</array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`

func TestReadPlist(t *testing.T) {
	dir := t.TempDir()
	path := writePlist(t, dir, "com.example.agent.plist", samplePlist)

	item := readPlist(path, KindLaunchAgent)
	if item.Label != "com.example.agent" {
		t.Errorf("label = %q", item.Label)
	}
	if item.Program != "/usr/local/bin/agent" {
		t.Errorf("program = %q", item.Program)
	}
	if len(item.Args) != 2 {
		t.Errorf("args = %v", item.Args)
	}
	if !item.Removable {
		t.Error("a plist must be removable")
	}
	if item.NeedsAdmin {
		t.Error("a user LaunchAgent must not need admin rights")
	}
}

func TestReadPlistDaemonNeedsAdmin(t *testing.T) {
	dir := t.TempDir()
	path := writePlist(t, dir, "com.example.daemon.plist", samplePlist)
	item := readPlist(path, KindLaunchDaemon)
	if !item.NeedsAdmin {
		t.Error("a LaunchDaemon must need admin rights")
	}
}

func TestReadPlistMissing(t *testing.T) {
	item := readPlist(filepath.Join(t.TempDir(), "nope.plist"), KindLaunchAgent)
	if item.Label != "" {
		t.Errorf("label = %q", item.Label)
	}
}

func TestIsSystemItem(t *testing.T) {
	cases := []struct {
		item Item
		want bool
	}{
		{Item{Label: "com.apple.something"}, true},
		{Item{Label: "com.example.something"}, false},
		{Item{Label: "com.example.x", Program: "/System/Library/foo"}, true},
		{Item{Label: "com.example.x", Program: "/usr/libexec/foo"}, true},
		{Item{Label: "com.example.x", Program: "/Applications/Foo.app/Contents/MacOS/Foo"}, false},
	}
	for _, c := range cases {
		if got := isSystemItem(&c.item); got != c.want {
			t.Errorf("isSystemItem(%+v) = %v, want %v", c.item, got, c.want)
		}
	}
}

func TestRemoveRefusesForeignPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A path outside the known folders must be refused.
	if err := Remove(context.Background(), "/tmp/something-else.plist"); err == nil {
		t.Error("a foreign path must be refused")
	}
	// A path in a known folder that does not exist is a not-found error.
	if err := Remove(context.Background(), filepath.Join(home, "Library", "LaunchAgents", "nope.plist")); err == nil {
		t.Error("a missing file must be reported")
	}
}
