package rules

import (
	"os"
	"path/filepath"
	"testing"
)

// testStore returns a store with the given user rules.
func testStore(t *testing.T, user UserRules) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "port_rules.json")
	s := NewStore(path)
	if err := s.Override("whitelist", "process", "keep-me", false); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.user = user
	s.mu.Unlock()
	return s
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"launchd", "launchd", true},
		{"launchd", "launchdd", false},
		{"kworker*", "kworker/0:1", true},
		{"kworker*", "kworker", true},
		{"*", "anything", true},
		{"", "anything", false},
		{"anything", "", false},
		{"Control*", "ControlCenter", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.value); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

func TestHasPathPrefix(t *testing.T) {
	cases := []struct {
		path   string
		prefix string
		want   bool
	}{
		{"/usr/libexec/foo", "/usr/libexec/", true},
		{"/usr/libexec/foo", "/usr/libexec", true},
		{"/usr/libexecx/foo", "/usr/libexec", false},
		{"/System/Library/foo", "/System/", true},
		{"/usr/local/bin/x", "/usr/local/bin", true},
		{"", "/usr", false},
	}
	for _, c := range cases {
		if got := hasPathPrefix(c.path, c.prefix); got != c.want {
			t.Errorf("hasPathPrefix(%q, %q) = %v, want %v", c.path, c.prefix, got, c.want)
		}
	}
}

func TestMatchUserPattern(t *testing.T) {
	if !matchUserPattern("_*", "_spotlight") {
		t.Error("the system user pattern must match")
	}
	if !matchUserPattern("root", "root") {
		t.Error("an exact user name must match")
	}
	if matchUserPattern("root", "zhangsong") {
		t.Error("a different user must not match")
	}
}

func TestWhitelistWinsOverBlacklist(t *testing.T) {
	s := testStore(t, UserRules{
		Whitelist: RuleSet{Processes: []string{"mymac*"}},
		Blacklist: RuleSet{Processes: []string{"mymac*"}},
	})
	eval := s.NewEvaluator()
	sub := Subject{PID: 10, Name: "mymac-helper", NameKnown: true}
	pm := eval.MatchProcess(sub)
	if pm.Whitelist == nil {
		t.Fatal("the whitelist must win")
	}
	d := eval.DecideProcess(pm)
	if d.IsSystem {
		t.Error("a whitelisted process must not be a system process")
	}
	if !d.Whitelisted {
		t.Error("the decision must record the whitelist")
	}
}

func TestBlacklistHidesProcess(t *testing.T) {
	s := testStore(t, UserRules{Blacklist: RuleSet{Processes: []string{"noisy*"}}})
	eval := s.NewEvaluator()
	pm := eval.MatchProcess(Subject{PID: 10, Name: "noisy-app", NameKnown: true})
	d := eval.DecideProcess(pm)
	if !d.IsSystem {
		t.Error("a blacklisted process must be hidden")
	}
	if d.Rule == nil || d.Rule.Source != SourceUserBlacklist {
		t.Errorf("the rule must be reported: %+v", d.Rule)
	}
}

func TestBuiltinRulesApply(t *testing.T) {
	s := testStore(t, UserRules{})
	eval := s.NewEvaluator()
	pm := eval.MatchProcess(Subject{PID: 1, Name: "launchd", NameKnown: true})
	if pm.Builtin == nil {
		t.Fatal("launchd must hit the builtin rules")
	}
	if !eval.DecideProcess(pm).IsSystem {
		t.Error("launchd must be a system process")
	}
}

func TestPortRules(t *testing.T) {
	s := testStore(t, UserRules{Whitelist: RuleSet{Ports: []int{8080}}})
	eval := s.NewEvaluator()
	pm := eval.MatchPort(8080)
	if pm.Whitelist == nil {
		t.Error("the whitelisted port must be found")
	}
	pm = eval.MatchPort(5353)
	if pm.Builtin == nil {
		t.Error("5353 must hit the builtin rules")
	}
}

func TestPortRangeRules(t *testing.T) {
	s := testStore(t, UserRules{Blacklist: RuleSet{PortRanges: []PortRange{{From: 49664, To: 49669}}}})
	eval := s.NewEvaluator()
	if pm := eval.MatchPort(49666); pm.Blacklist == nil {
		t.Error("a port inside the range must be blacklisted")
	}
	if pm := eval.MatchPort(49670); pm.Blacklist != nil {
		t.Error("a port outside the range must not be blacklisted")
	}
}

// TestWhitelistDoesNotLiftProtection is the important one: the whitelist only
// controls what is displayed, it must never allow a system process to be
// killed.
func TestWhitelistDoesNotLiftProtection(t *testing.T) {
	s := testStore(t, UserRules{Whitelist: RuleSet{Processes: []string{"launchd"}}})
	eval := s.NewEvaluator()
	sub := Subject{PID: 1, Name: "launchd", NameKnown: true}
	if ref := eval.Protection(sub); ref == nil {
		t.Error("a whitelisted system process must still be protected")
	}
}

func TestUnknownIdentityIsShown(t *testing.T) {
	s := testStore(t, UserRules{})
	eval := s.NewEvaluator()
	pm := eval.MatchProcess(Subject{PID: 42})
	if !pm.Unknown {
		t.Error("an unreadable identity must be unknown")
	}
	d := eval.DecideProcess(pm)
	if d.IsSystem {
		t.Error("an unknown identity must not be hidden")
	}
	if d.Check != CheckUnknown {
		t.Errorf("check = %q", d.Check)
	}
}

func TestOverrideAddsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "port_rules.json"))

	if err := s.Override("whitelist", "port", "8080", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Override("whitelist", "process", "my-server", false); err != nil {
		t.Fatal(err)
	}
	user := s.User()
	if len(user.Whitelist.Ports) != 1 || user.Whitelist.Ports[0] != 8080 {
		t.Errorf("ports = %v", user.Whitelist.Ports)
	}
	if len(user.Whitelist.Processes) != 1 {
		t.Errorf("processes = %v", user.Whitelist.Processes)
	}

	// The rule must survive a reload.
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(s.User().Whitelist.Ports) != 1 {
		t.Error("the rule was not written to disk")
	}

	if err := s.Override("whitelist", "port", "8080", true); err != nil {
		t.Fatal(err)
	}
	if len(s.User().Whitelist.Ports) != 0 {
		t.Errorf("the rule was not removed: %v", s.User().Whitelist.Ports)
	}
}

func TestOverrideRejectsBadInput(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "port_rules.json"))
	if err := s.Override("whitelist", "port", "not-a-port", false); err == nil {
		t.Error("a bad port must be rejected")
	}
	if err := s.Override("whitelist", "port", "70000", false); err == nil {
		t.Error("a port out of range must be rejected")
	}
	if err := s.Override("nonsense", "port", "80", false); err == nil {
		t.Error("an unknown set must be rejected")
	}
	if err := s.Override("whitelist", "nonsense", "x", false); err == nil {
		t.Error("an unknown kind must be rejected")
	}
}

// TestBrokenRuleFileFallsBack makes sure a broken file does not stop the
// service and does not allow the rules to be overwritten.
func TestBrokenRuleFileFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "port_rules.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if s.LoadError() == "" {
		t.Error("the parse error must be recorded")
	}
	if len(s.User().Whitelist.Ports) != 0 {
		t.Error("the fallback must be the empty rules")
	}
	if err := s.Override("whitelist", "port", "80", false); err == nil {
		t.Error("writing must be refused while the file is broken")
	}
}

func TestBuiltinRulesAreLoaded(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "port_rules.json"))
	b := s.Builtin()
	if len(b.Ports) == 0 || len(b.Processes) == 0 {
		t.Errorf("the builtin rules are empty: %+v", b)
	}
	if b.Name == "" {
		t.Error("the builtin rules must be named")
	}
}
