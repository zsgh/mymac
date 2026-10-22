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

// TestReadPlistProgramKey checks the plists that name the program directly.
//
// Plenty of agents use the Program key instead of ProgramArguments; without
// it the program of the item stays empty and the item cannot be grouped.
func TestReadPlistProgramKey(t *testing.T) {
	dir := t.TempDir()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.example.program</string>
	<key>Program</key><string>/usr/local/bin/agent</string>
</dict>
</plist>
`
	path := writePlist(t, dir, "com.example.program.plist", body)
	item := readPlist(path, KindLaunchAgent)
	if item.Program != "/usr/local/bin/agent" {
		t.Errorf("program = %q", item.Program)
	}
}

// TestGroupOfTheSameApp checks that the helpers of one app land together.
//
// One app often brings several startup items with it, and the user wants to
// turn the program off in one go.
func TestGroupOfTheSameApp(t *testing.T) {
	items := []Item{
		{Label: "com.example.main", Program: "/Applications/Example.app/Contents/MacOS/Example", Enabled: true},
		{Label: "com.example.helper", Program: "/Applications/Example.app/Contents/Helpers/Helper", Enabled: true},
		// A helper that brings its own bundle still belongs to the app.
		{Label: "com.example.nested", Program: "/Applications/Example.app/Contents/Helper.app/Contents/MacOS/Helper", Enabled: false},
		// Another program, on its own.
		{Label: "com.other.main", Program: "/Applications/Other.app/Contents/MacOS/Other", Enabled: true},
		// No program at all: the label is the group.
		{Label: "com.example.leftover", Enabled: true},
	}
	groupItems(items, nil)

	if items[0].GroupKey != "/Applications/Example.app" {
		t.Errorf("group key = %q", items[0].GroupKey)
	}
	if items[0].GroupName != "Example" {
		t.Errorf("group name = %q", items[0].GroupName)
	}
	for _, i := range []int{0, 1, 2} {
		if items[i].GroupSize != 3 {
			t.Errorf("item %d: group size = %d", i, items[i].GroupSize)
		}
	}
	// Two of the three are still enabled.
	if items[0].GroupOn != 2 {
		t.Errorf("group on = %d", items[0].GroupOn)
	}
	if items[3].GroupSize != 1 {
		t.Errorf("the other program has %d items", items[3].GroupSize)
	}
	if items[4].GroupKey != "com.example.leftover" {
		t.Errorf("the leftover has the group key %q", items[4].GroupKey)
	}
}

// TestGroupOfTheLabelBundleID checks the services that do not name their
// program.
func TestGroupOfTheLabelBundleID(t *testing.T) {
	cases := []struct {
		label string
		want  string
	}{
		{"application.com.google.Chrome.23188742.62308572.AADEA518", "com.google.Chrome"},
		{"application.cn.trae.app.62794438.62794443", "cn.trae.app"},
		{"com.microsoft.VSCode.ShipIt", "com.microsoft.VSCode"},
		{"com.example.plain", ""},
		{"ynote-desktop.ShipIt", ""},
	}
	for _, c := range cases {
		if got := bundleIDFromLabel(c.label); got != c.want {
			t.Errorf("bundleIDFromLabel(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}

// TestGroupNameFromARunningApp checks that a service is matched to the app
// it belongs to.
func TestGroupNameFromARunningApp(t *testing.T) {
	apps := []string{"/Applications/Google Chrome.app", "/Applications/Trae CN.app"}
	item := Item{Label: "application.com.google.Chrome.23188742.62308572"}
	key, name := groupOf(&item, apps)
	if key != "/Applications/Google Chrome.app" {
		t.Errorf("group key = %q", key)
	}
	if name != "Google Chrome" {
		t.Errorf("group name = %q", name)
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
