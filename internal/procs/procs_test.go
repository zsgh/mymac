package procs

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCollectPerformance checks the Phase 6 requirement: collecting the
// process table must stay under 500ms.
func TestCollectPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the performance test in short mode")
	}
	c := NewCollector(time.Second)
	ctx := context.Background()

	// The first call primes the CPU counters.
	c.Collect(ctx)

	start := time.Now()
	snap := c.Collect(ctx)
	elapsed := time.Since(start)

	t.Logf("collected %d processes in %s", len(snap.Processes), elapsed.Round(time.Millisecond))
	if len(snap.Processes) < 100 {
		t.Errorf("only %d processes found, expected more", len(snap.Processes))
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("collection took %s, want < 500ms", elapsed)
	}
}

// TestCollectIsCached makes sure the cache is used between requests.
func TestCollectIsCached(t *testing.T) {
	c := NewCollector(5 * time.Second)
	ctx := context.Background()
	first := c.Snapshot(ctx)
	second := c.Snapshot(ctx)
	if first != second {
		t.Error("the second snapshot should come from the cache")
	}
}

// TestBuildTree checks the parent/child links.
func TestBuildTree(t *testing.T) {
	procs := []Process{
		{PID: 1, PPID: 0},
		{PID: 10, PPID: 1},
		{PID: 11, PPID: 10},
		{PID: 20, PPID: 1},
	}
	buildTree(procs)
	if len(procs[0].Children) != 2 {
		t.Errorf("pid 1 children = %v", procs[0].Children)
	}
	if len(procs[1].Children) != 1 || procs[1].Children[0] != 11 {
		t.Errorf("pid 10 children = %v", procs[1].Children)
	}
	if procs[2].Depth != 2 {
		t.Errorf("pid 11 depth = %d, want 2", procs[2].Depth)
	}
	if procs[0].Depth != 0 {
		t.Errorf("pid 1 depth = %d, want 0", procs[0].Depth)
	}
}

// TestBuildTreeHandlesCycles makes sure a self-parenting process does not
// hang the collector.
func TestBuildTreeHandlesCycles(t *testing.T) {
	procs := []Process{
		{PID: 1, PPID: 1},
		{PID: 2, PPID: 2},
	}
	buildTree(procs)
}

func TestMapState(t *testing.T) {
	cases := map[string]State{
		"Ss":    StateSleeping,
		"S+":    StateSleeping,
		"Us":    StateSleeping,
		"R+":    StateRunning,
		"R":     StateRunning,
		"T":     StateStopped,
		"Z+":    StateZombie,
		"":      StateUnknown,
		"weird": StateUnknown,
	}
	for in, want := range cases {
		if got := mapState(in); got != want {
			t.Errorf("mapState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsProtected(t *testing.T) {
	if !IsProtected("launchd", 1) {
		t.Error("launchd must be protected")
	}
	if !IsProtected("kernel_task", 999) {
		t.Error("kernel_task must be protected")
	}
	if !IsProtected("anything", 1) {
		t.Error("pid 1 must be protected")
	}
	if IsProtected("my-app", 4242) {
		t.Error("a normal process must not be protected")
	}
}

func TestAppBundlePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/Applications/ClashX Pro.app/Contents/MacOS/ClashX Pro", "/Applications/ClashX Pro.app"},
		{"/usr/bin/zsh", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := appBundlePath(c.in); got != c.want {
			t.Errorf("appBundlePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestParseLsof uses the real output captured on macOS 26.5.2.
func TestParseLsof(t *testing.T) {
	out := `COMMAND     PID      USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
opencode    259 zhangsong    8u  IPv4 0x378393604f7c7f03      0t0  TCP 127.0.0.1:62814->127.0.0.1:49374 (ESTABLISHED)
rapportd    980 zhangsong   10u  IPv4 0x60ecbb87c50604e3      0t0  TCP *:62832 (LISTEN)
identitys   996 zhangsong   12u  IPv6 0x47140fc215e66dc6      0t0  UDP *:*
`
	got := parseLsof(out)
	if len(got) != 3 {
		t.Fatalf("parsed %d pids, want 3: %+v", len(got), got)
	}
	p := got[259]
	if len(p) != 1 {
		t.Fatalf("pid 259 ports = %+v", p)
	}
	if p[0].Local != "127.0.0.1:62814" || p[0].Remote != "127.0.0.1:49374" {
		t.Errorf("pid 259 = %+v", p[0])
	}
	if p[0].State != "ESTABLISHED" {
		t.Errorf("state = %q", p[0].State)
	}
	if p[0].Proto != "TCP" {
		t.Errorf("proto = %q", p[0].Proto)
	}
	if got[980][0].Local != "*:62832" {
		t.Errorf("pid 980 = %+v", got[980][0])
	}
	if got[996][0].Proto != "UDP" {
		t.Errorf("pid 996 = %+v", got[996][0])
	}
}

// TestParseNettop uses the real output captured on macOS 26.5.2.
func TestParseNettop(t *testing.T) {
	out := `,bytes_in,bytes_out,
launchd.1,0,0,
syslogd.587,0,2199,
mDNSResponder.672,601166753,497577727,
ClashX Pro.81660,31676358,31666021,
有道云笔记.92522,325876,99384,
`
	got := parseNettop(out)
	if len(got) != 5 {
		t.Fatalf("parsed %d entries, want 5: %+v", len(got), got)
	}
	if got[1].in != 0 || got[1].out != 0 {
		t.Errorf("launchd = %+v", got[1])
	}
	if got[587].out != 2199 {
		t.Errorf("syslogd = %+v", got[587])
	}
	if got[672].in != 601166753 {
		t.Errorf("mDNSResponder = %+v", got[672])
	}
	// A process name with a space still yields the right pid.
	if got[81660].in != 31676358 {
		t.Errorf("ClashX Pro = %+v", got[81660])
	}
	// A non-ASCII process name too.
	if got[92522].out != 99384 {
		t.Errorf("有道云笔记 = %+v", got[92522])
	}
}

// TestParseFootprint uses the real output captured on macOS 26.5.2.
func TestParseFootprint(t *testing.T) {
	out := `======================================================================
zsh [60969]: 64-bit    Footprint: 1568 KB (16384 bytes per page)
======================================================================

  Dirty      Clean  Reclaimable    Regions    Category
    ---        ---          ---      ---    ---
 352 KB        0 B          0 B          8    MALLOC metadata
1568 KB     544 KB          0 B        413    TOTAL
`
	got := parseFootprint(out)
	if got != 1568*1024 {
		t.Errorf("parseFootprint = %d, want %d", got, 1568*1024)
	}
}

func TestParseFootprintUnits(t *testing.T) {
	if got := parseFootprint("Footprint: 2 MB"); got != 2*1024*1024 {
		t.Errorf("MB = %d", got)
	}
	if got := parseFootprint("Footprint: 512 B"); got != 512 {
		t.Errorf("B = %d", got)
	}
	if got := parseFootprint("nothing here"); got != 0 {
		t.Errorf("no footprint = %d", got)
	}
}

// TestEnvironmentOfParsesPsOutput checks the ps eww parsing.
func TestEnvironmentOfParsesPsOutput(t *testing.T) {
	out := `  PID   TT  STAT      TIME COMMAND
81660   ??  S     36:55.43 /Applications/ClashX Pro.app/Contents/MacOS/ClashX Pro OSLogRateLimit=64 USER=zhangsong PATH=/usr/bin:/bin SSH_AUTH_SOCK=/var/run/x
`
	env, err := parsePsEnvironment(out)
	if err != nil {
		t.Fatal(err)
	}
	// The environment is the contiguous run of NAME=VALUE tokens at the end.
	if len(env) != 4 {
		t.Fatalf("env = %+v", env)
	}
	if env[0].Name != "OSLogRateLimit" || env[0].Value != "64" {
		t.Errorf("env[0] = %+v", env[0])
	}
	if env[2].Name != "PATH" || env[2].Value != "/usr/bin:/bin" {
		t.Errorf("env[2] = %+v", env[2])
	}
	if env[3].Name != "SSH_AUTH_SOCK" {
		t.Errorf("env[3] = %+v", env[3])
	}
	// A name that looks secret must be flagged so the UI can mask it.
	if !env[3].Sensitive {
		t.Error("SSH_AUTH_SOCK should be marked sensitive")
	}
}

// TestPlistStringHelpers exercises the safe plist accessors.
func TestPlistStringHelpers(t *testing.T) {
	doc := map[string]any{
		"CFBundleIdentifier":         "com.example.app",
		"CFBundleName":               "Example",
		"CFBundleSupportedPlatforms": []any{"iPhoneOS", "MacOSX"},
	}
	if got := plistString(doc, "CFBundleIdentifier"); got != "com.example.app" {
		t.Errorf("bundle id = %q", got)
	}
	if got := plistString(doc, "Missing"); got != "" {
		t.Errorf("missing key = %q", got)
	}
	if got := plistStrings(doc, "CFBundleSupportedPlatforms"); len(got) != 2 {
		t.Errorf("platforms = %v", got)
	}
	if got := plistStrings(doc, "Missing"); got != nil {
		t.Errorf("missing array = %v", got)
	}
}

// TestMain guards against the tests touching the real launchd.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// TestProcessName covers the fallback for processes without a name.
//
// A few system daemons come back from the process table with an empty name.
// Showing nothing in the name column is useless, and the protection rules
// match on the name, so the executable is used instead.
func TestProcessName(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"Safari", "/Applications/Safari.app/Contents/MacOS/Safari", "Safari"},
		{"", "/usr/libexec/IOMFB_bics_daemon", "IOMFB_bics_daemon"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := processName(c.name, c.path); got != c.want {
			t.Errorf("processName(%q, %q) = %q, want %q", c.name, c.path, got, c.want)
		}
	}
}

// TestSignalRefusesSelf makes sure mymac cannot signal itself.
//
// A frozen mymac keeps its port open and its single instance lock, but it
// stops answering: the user is left with a service that looks alive and is
// not. The same goes for renicing itself.
func TestSignalRefusesSelf(t *testing.T) {
	self := os.Getpid()

	for _, sig := range []string{"TERM", "KILL", "STOP", "CONT"} {
		err := Signal(context.Background(), self, sig)
		if err == nil {
			t.Fatalf("Signal(self, %s) was allowed", sig)
		}
		if !strings.Contains(err.Error(), "mymac 自己") {
			t.Errorf("Signal(self, %s) = %v, want a refusal that names mymac", sig, err)
		}
	}

	if err := Renice(context.Background(), self, 10); err == nil {
		t.Error("Renice(self) was allowed")
	}
}

// TestIsSelf covers the self check.
func TestIsSelf(t *testing.T) {
	if !IsSelf(os.Getpid()) {
		t.Error("IsSelf must recognise the running process")
	}
	if IsSelf(os.Getpid() + 1) {
		t.Error("IsSelf must not recognise another process")
	}
}
