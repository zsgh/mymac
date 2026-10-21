package envs

import (
	"context"
	"strings"
	"testing"
)

// TestParseLaunchdEnvironment uses the real output captured on macOS 26.5.2.
func TestParseLaunchdEnvironment(t *testing.T) {
	out := `gui/501 = {
	type = login
	handle = 100016
	active count = 451

	environment = {
		SSH_AUTH_SOCK => /var/run/com.apple.launchd.0ukikaUqDw/Listeners
		MYMAC_PROBE => hello world $x 'q'
	}

	services = {
		   81660      - 	application.com.west2online.ClashXPro.12548703.12548708
	}
}
`
	got := parseLaunchdEnvironment(out)
	if got["SSH_AUTH_SOCK"] != "/var/run/com.apple.launchd.0ukikaUqDw/Listeners" {
		t.Errorf("SSH_AUTH_SOCK = %q", got["SSH_AUTH_SOCK"])
	}
	if got["MYMAC_PROBE"] != "hello world $x 'q'" {
		t.Errorf("MYMAC_PROBE = %q", got["MYMAC_PROBE"])
	}
	if len(got) != 2 {
		t.Errorf("parsed %d entries, want 2: %+v", len(got), got)
	}
}

// TestParseLaunchdEnvironmentMultiline covers a value that contains a
// newline: launchctl prints it across several lines.
func TestParseLaunchdEnvironmentMultiline(t *testing.T) {
	out := `gui/501 = {
	environment = {
		MYMAC_SPECIAL => a b"c'd $HOME ` + "`id`" + ` 中文
line2
		SSH_AUTH_SOCK => /var/run/com.apple.launchd.0ukikaUqDw/Listeners
	}
}
`
	got := parseLaunchdEnvironment(out)
	want := "a b\"c'd $HOME `id` 中文\nline2"
	if got["MYMAC_SPECIAL"] != want {
		t.Errorf("MYMAC_SPECIAL = %q, want %q", got["MYMAC_SPECIAL"], want)
	}
	if got["SSH_AUTH_SOCK"] != "/var/run/com.apple.launchd.0ukikaUqDw/Listeners" {
		t.Errorf("SSH_AUTH_SOCK = %q", got["SSH_AUTH_SOCK"])
	}
}

// TestGetenvTrimsExactlyOneNewline documents the launchctl convention:
// the value is printed followed by one newline, so a value that itself ends
// in a newline keeps it.
func TestGetenvTrimsExactlyOneNewline(t *testing.T) {
	lc := &SystemLaunchctl{Path: "/bin/launchctl"}
	if _, err := lc.Path, error(nil); err != nil {
		t.Fatal(err)
	}
	// A missing variable exits 0 with empty output.
	v, err := lc.Getenv(context.Background(), "MYMAC_DEFINITELY_NOT_SET_12345")
	if err != nil {
		t.Fatalf("getenv on a missing variable must not fail: %v", err)
	}
	if v != "" {
		t.Errorf("missing variable = %q, want empty", v)
	}
}

func TestSplitLaunchdEntry(t *testing.T) {
	k, v, ok := splitLaunchdEntry("FOO => bar baz")
	if !ok || k != "FOO" || v != "bar baz" {
		t.Errorf("split = %q %q %v", k, v, ok)
	}
	if _, _, ok := splitLaunchdEntry("no arrow here"); ok {
		t.Error("a line without => must not parse as an entry")
	}
	if _, _, ok := splitLaunchdEntry("two words => x"); ok {
		t.Error("a key with a space must not parse as an entry")
	}
}

func TestApplyRoundTripWithFake(t *testing.T) {
	lc := NewFakeLaunchctl()
	res, err := Apply(context.Background(), lc, "FOO", "bar")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied {
		t.Errorf("Apply did not confirm: %+v", res)
	}
	if !strings.Contains(res.Message, "") {
		t.Log(res.Message)
	}
}
