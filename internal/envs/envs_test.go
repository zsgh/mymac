package envs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testHome points HOME at a temp directory so no test ever touches the real
// user's files.
func testHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// os.UserHomeDir on darwin reads $HOME; keep it consistent.
	return dir
}

// stubLaunchctl is a fake launchctl binary used for the agent paths.
func stubLaunchctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "launchctl")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestManager(t *testing.T) (*Manager, *FakeLaunchctl) {
	t.Helper()
	home := testHome(t)
	lc := NewFakeLaunchctl()
	m := NewManager(Options{
		StorePath:       filepath.Join(home, ".config", "mymac", "env.json"),
		EnvSh:           filepath.Join(home, ".config", "mymac", "env.sh"),
		Launchctl:       lc,
		LaunchctlBinary: stubLaunchctl(t),
	})
	return m, lc
}

// ------------------------------------------------------------ escaping

func TestEscapeSingleQuoted(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"bar", "'bar'"},
		{"hello world", "'hello world'"},
		{"it's", `'it'\''s'`},
		{"$HOME", "'$HOME'"},
		{"`whoami`", "'`whoami`'"},
		{"a\nb", "'a\nb'"},
		{"中文值", "'中文值'"},
		{`back\slash`, `'back\slash'`},
		{"", "''"},
	}
	for _, c := range cases {
		if got := EscapeSingleQuoted(c.in); got != c.want {
			t.Errorf("EscapeSingleQuoted(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestShellRoundTrip proves the escaping survives a real shell: the value
// read back from `sh` must be byte identical to the value written.
func TestShellRoundTrip(t *testing.T) {
	values := []string{
		"bar",
		"hello world",
		"it's got quotes",
		"$HOME and ${PATH}",
		"`backtick`",
		"line1\nline2",
		"中文值 with spaces",
		"trailing space ",
		"back\\slash",
		"a'b'c",
		"!@#$%^&*()",
	}
	dir := t.TempDir()
	envSh := filepath.Join(dir, "env.sh")

	for i, v := range values {
		name := "V" + string(rune('A'+i))
		content := RenderEnvSh([]Var{{Name: name, Value: v, Enabled: true}})
		if err := os.WriteFile(envSh, content, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := runShell("printf %s \"$"+name+"\"", envSh)
		if err != nil {
			t.Fatalf("shell failed for %q: %v", v, err)
		}
		if out != v {
			t.Errorf("value %q round-tripped to %q", v, out)
		}
	}
}

// runShell sources the env file and runs a command, returning stdout.
func runShell(cmd, envSh string) (string, error) {
	full := ". \"" + envSh + "\"\n" + cmd
	out, err := execShell(full)
	return out, err
}

func TestRenderEnvShSkipsDisabled(t *testing.T) {
	content := string(RenderEnvSh([]Var{
		{Name: "ON", Value: "1", Enabled: true},
		{Name: "OFF", Value: "2", Enabled: false},
	}))
	if !strings.Contains(content, "export ON='1'") {
		t.Errorf("missing enabled var in:\n%s", content)
	}
	if strings.Contains(content, "OFF") {
		t.Errorf("disabled var leaked into env.sh:\n%s", content)
	}
}

func TestRenderEnvShSkipsPath(t *testing.T) {
	content := string(RenderEnvSh([]Var{{Name: "PATH", Value: "/x", Enabled: true}}))
	if strings.Contains(content, "PATH") {
		t.Errorf("PATH must not be exported by RenderEnvSh:\n%s", content)
	}
}

// ---------------------------------------------------------- marker block

func TestFindMarkerStates(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    MarkerState
	}{
		{"missing", "export FOO=1\n", MarkerMissing},
		{"ok", MarkerBlock(), MarkerOK},
		{"damaged", "# >>> mymac env >>>\nexport FOO=1\n", MarkerDamaged},
		{"conflict", MarkerBlock() + MarkerBlock(), MarkerConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, state, _, _ := FindMarker(c.content)
			if state != c.want {
				t.Errorf("state = %q, want %q", state, c.want)
			}
		})
	}
}

func TestReplaceMarkerPreservesUserContent(t *testing.T) {
	user := "# my own header\nexport MINE=1\n"
	content := user + MarkerBlock() + "# my own footer\nexport OTHER=2\n"

	out, ok, reason := ReplaceMarker(content, MarkerBlock())
	if !ok {
		t.Fatalf("replace failed: %s", reason)
	}
	if !strings.Contains(out, "# my own header") || !strings.Contains(out, "export MINE=1") {
		t.Errorf("user content above the block was lost:\n%s", out)
	}
	if !strings.Contains(out, "# my own footer") || !strings.Contains(out, "export OTHER=2") {
		t.Errorf("user content below the block was lost:\n%s", out)
	}
	if strings.Count(out, MarkerStart) != 1 {
		t.Errorf("block was duplicated:\n%s", out)
	}
}

func TestReplaceMarkerAppendsWhenMissing(t *testing.T) {
	content := "export MINE=1\n"
	out, ok, _ := ReplaceMarker(content, MarkerBlock())
	if !ok {
		t.Fatal("replace failed")
	}
	if !strings.Contains(out, "export MINE=1") {
		t.Errorf("user content lost:\n%s", out)
	}
	if !strings.Contains(out, MarkerStart) {
		t.Errorf("block not appended:\n%s", out)
	}
}

func TestReplaceMarkerRefusesDamaged(t *testing.T) {
	content := "# >>> mymac env >>>\nhalf a block\n"
	out, ok, reason := ReplaceMarker(content, MarkerBlock())
	if ok {
		t.Error("damaged block must not be overwritten")
	}
	if out != content {
		t.Error("content must stay untouched when the block is damaged")
	}
	if reason == "" {
		t.Error("expected a reason for the refusal")
	}
}

func TestUpsertShellFileIsIdempotent(t *testing.T) {
	home := testHome(t)
	path := filepath.Join(home, ".zshenv")
	if err := os.WriteFile(path, []byte("# user header\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertShellFile(path, MarkerBlock()); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertShellFile(path, MarkerBlock()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), MarkerStart); n != 1 {
		t.Errorf("marker count = %d, want 1:\n%s", n, data)
	}
	if !strings.Contains(string(data), "# user header") {
		t.Errorf("user content lost:\n%s", data)
	}
}

func TestUpsertShellFileRefusesDamaged(t *testing.T) {
	home := testHome(t)
	path := filepath.Join(home, ".zshenv")
	original := "# >>> mymac env >>>\nbroken\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertShellFile(path, MarkerBlock()); err == nil {
		t.Fatal("expected an error for a damaged block")
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("damaged file was modified:\n%s", data)
	}
}

func TestInspectMarkerDetectsManualEdit(t *testing.T) {
	home := testHome(t)
	path := filepath.Join(home, ".zshenv")
	edited := MarkerStart + "\n# someone changed this\n" + MarkerEnd + "\n"
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := InspectMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.State != MarkerDamaged {
		t.Errorf("state = %q, want %q (%s)", rep.State, MarkerDamaged, rep.Detail)
	}
}

func TestRemoveShellBlock(t *testing.T) {
	home := testHome(t)
	path := filepath.Join(home, ".zshenv")
	content := "head\n" + MarkerBlock() + "tail\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveShellBlock(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), MarkerStart) {
		t.Errorf("marker survived removal:\n%s", data)
	}
	if !strings.Contains(string(data), "head") || !strings.Contains(string(data), "tail") {
		t.Errorf("user content lost:\n%s", data)
	}
}

// ------------------------------------------------------------- validation

func TestValidateName(t *testing.T) {
	good := []string{"FOO", "_x", "A1_b", "lower"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "1FOO", "FOO-BAR", "FOO BAR", "FOO.BAR", "a$b"}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", n)
		}
	}
	for _, n := range []string{"HOME", "PATH", "SHELL", "TMPDIR", "LC_CTYPE"} {
		if err := ValidateName(n); err == nil {
			t.Errorf("protected name %q must require a strong confirmation", n)
		}
	}
}

func TestIsSensitive(t *testing.T) {
	sensitive := []string{"API_KEY", "my_token", "SECRET", "db_PASSWORD", "GITHUB_TOKEN"}
	for _, n := range sensitive {
		if !IsSensitive(n) {
			t.Errorf("%q should be sensitive", n)
		}
	}
	plain := []string{"PATH", "HOME", "LANG", "EDITOR"}
	for _, n := range plain {
		if IsSensitive(n) {
			t.Errorf("%q should not be sensitive", n)
		}
	}
}

func TestIsProtected(t *testing.T) {
	for _, n := range []string{"HOME", "USER", "SHELL", "PATH", "TMPDIR", "LC_ALL"} {
		if !IsProtected(n) {
			t.Errorf("%q should be protected", n)
		}
	}
	if IsProtected("FOO") {
		t.Error("FOO should not be protected")
	}
}

// ------------------------------------------------------------------ store

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.json")
	s := NewStoreAt(path)
	s.Upsert(Var{Name: "FOO", Value: "bar", Enabled: true})
	s.Upsert(Var{Name: "BAZ", Value: "qux", Enabled: false, Note: "note"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("env.json mode = %o, want 600", perm)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Vars) != 2 {
		t.Fatalf("vars = %d, want 2", len(loaded.Vars))
	}
	v, ok := loaded.Find("FOO")
	if !ok || v.Value != "bar" {
		t.Errorf("FOO = %+v, ok=%v", v, ok)
	}
	if !loaded.Vars[0].Sensitive == false {
		t.Skip() // sensitivity is derived, checked elsewhere
	}
}

func TestStoreActiveFiltersDisabled(t *testing.T) {
	s := NewStoreAt(filepath.Join(t.TempDir(), "env.json"))
	s.Upsert(Var{Name: "ON", Value: "1", Enabled: true})
	s.Upsert(Var{Name: "OFF", Value: "2", Enabled: false})
	active := s.Active()
	if len(active) != 1 || active[0].Name != "ON" {
		t.Errorf("active = %+v", active)
	}
}

// --------------------------------------------------------------- manager

func TestManagerCreateAppliesToBothLayers(t *testing.T) {
	m, lc := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Create(ctx, "FOO", "bar", "a note"); err != nil {
		t.Fatal(err)
	}

	// launchd layer.
	got, err := lc.Getenv(ctx, "FOO")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bar" {
		t.Errorf("launchd FOO = %q, want %q", got, "bar")
	}

	// shell layer.
	envSh := filepath.Join(os.Getenv("HOME"), ".config", "mymac", "env.sh")
	data, err := os.ReadFile(envSh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "export FOO='bar'") {
		t.Errorf("env.sh missing export:\n%s", data)
	}

	// zshenv marker block.
	zshenv := filepath.Join(os.Getenv("HOME"), ".zshenv")
	zdata, err := os.ReadFile(zshenv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zdata), MarkerStart) {
		t.Errorf("zshenv missing marker block:\n%s", zdata)
	}
}

func TestManagerUpdateAndDelete(t *testing.T) {
	m, lc := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Create(ctx, "FOO", "one", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(ctx, "FOO", strPtr("two"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := lc.Getenv(ctx, "FOO"); got != "two" {
		t.Errorf("launchd FOO = %q, want %q", got, "two")
	}
	envSh := filepath.Join(os.Getenv("HOME"), ".config", "mymac", "env.sh")
	data, _ := os.ReadFile(envSh)
	if !strings.Contains(string(data), "export FOO='two'") {
		t.Errorf("env.sh not updated:\n%s", data)
	}

	if err := m.Delete(ctx, "FOO"); err != nil {
		t.Fatal(err)
	}
	if got, _ := lc.Getenv(ctx, "FOO"); got != "" {
		t.Errorf("launchd FOO = %q after delete, want empty", got)
	}
	data, _ = os.ReadFile(envSh)
	if strings.Contains(string(data), "FOO") {
		t.Errorf("env.sh still exports FOO:\n%s", data)
	}
}

func TestManagerDisableRemovesFromLaunchd(t *testing.T) {
	m, lc := newTestManager(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, "FOO", "one", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(ctx, "FOO", nil, nil, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if got, _ := lc.Getenv(ctx, "FOO"); got != "" {
		t.Errorf("disabled var still in launchd: %q", got)
	}
}

func TestManagerRejectsDuplicateAndPath(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, "FOO", "1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, "FOO", "2", ""); err == nil {
		t.Error("duplicate name must be rejected")
	}
	if _, err := m.Create(ctx, "PATH", "/x", ""); err == nil {
		t.Error("PATH must go through the PATH editor")
	}
}

func TestApplyDetectsMangledValue(t *testing.T) {
	lc := NewFakeLaunchctl()
	lc.Mangle = true
	res, err := Apply(context.Background(), lc, "FOO", "bar")
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Error("Apply must not report success when the read-back differs")
	}
	if res.Message == "" {
		t.Error("expected an explanatory message")
	}
}

func TestSnapshotReportsLayers(t *testing.T) {
	m, lc := newTestManager(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, "FOO", "bar", ""); err != nil {
		t.Fatal(err)
	}
	// A foreign definition in .zshrc should show up as a conflict.
	zshrc := filepath.Join(os.Getenv("HOME"), ".zshrc")
	if err := os.WriteFile(zshrc, []byte("export FOO=other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Managed) != 1 {
		t.Fatalf("managed = %d, want 1", len(snap.Managed))
	}
	mv := snap.Managed[0]
	if !mv.ShellApplied {
		t.Error("shellApplied should be true")
	}
	if !mv.LaunchdSet || !mv.LaunchdOK {
		t.Errorf("launchd state = %v/%v", mv.LaunchdSet, mv.LaunchdOK)
	}
	if len(mv.Conflicts) == 0 {
		t.Error("expected a conflict for the .zshrc definition")
	}
	if len(snap.Launchd) == 0 {
		t.Error("expected launchd variables in the snapshot")
	}
	if len(snap.Self) == 0 {
		t.Error("expected the process environment in the snapshot")
	}
	_ = lc
}

func TestSnapshotDetectsDamagedMarker(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, "FOO", "bar", ""); err != nil {
		t.Fatal(err)
	}
	zshenv := filepath.Join(os.Getenv("HOME"), ".zshenv")
	if err := os.WriteFile(zshenv, []byte("# >>> mymac env >>>\nbroken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Marker.State != MarkerDamaged {
		t.Errorf("marker state = %q, want damaged", snap.Marker.State)
	}
	if len(snap.Warnings) == 0 {
		t.Error("expected a warning about the damaged marker block")
	}
}

func TestImportPreviewAndApply(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, "FOO", "old", ""); err != nil {
		t.Fatal(err)
	}

	content := `{"vars":[{"name":"FOO","value":"new"},{"name":"BAR","value":"two"}]}`
	changes, err := m.ImportPreview(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want 2", changes)
	}
	// The preview must not have changed anything.
	if v, _ := mustStore(t, m).Find("FOO"); v.Value != "old" {
		t.Errorf("preview mutated the store: %q", v.Value)
	}

	if _, err := m.Import(ctx, content); err != nil {
		t.Fatal(err)
	}
	if v, _ := mustStore(t, m).Find("FOO"); v.Value != "new" {
		t.Errorf("FOO = %q, want new", v.Value)
	}
	if v, ok := mustStore(t, m).Find("BAR"); !ok || v.Value != "two" {
		t.Errorf("BAR = %+v ok=%v", v, ok)
	}
}

func TestImportEnvFormat(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	changes, err := m.Import(ctx, "export A=1\nexport B='two words'\n# comment\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	if v, _ := mustStore(t, m).Find("B"); v.Value != "two words" {
		t.Errorf("B = %q, want %q", v.Value, "two words")
	}
}

func TestPathEditorMergesWithoutOverwriting(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	view, err := m.SetPath(ctx, []string{"/opt/homebrew/bin"}, []string{"/custom/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Prepend) != 1 || view.Prepend[0] != "/opt/homebrew/bin" {
		t.Errorf("prepend = %+v", view.Prepend)
	}
	if len(view.Append) != 1 || view.Append[0] != "/custom/bin" {
		t.Errorf("append = %+v", view.Append)
	}
	// The merged value must contain the base PATH too.
	base := m.currentPath(ctx)
	if base != "" && !strings.Contains(view.Merged, base) && !strings.Contains(view.Merged, "/usr/bin") {
		t.Errorf("merged PATH lost the base entries: %q", view.Merged)
	}
	if !strings.HasPrefix(view.Merged, "/opt/homebrew/bin:") {
		t.Errorf("merged PATH = %q, want the prepend first", view.Merged)
	}
}

// TestPathExportIsDynamic checks that env.sh does not bake in a literal PATH
// but prepends and appends to whatever PATH the shell already has.
func TestPathExportIsDynamic(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := m.SetPath(ctx, []string{"/opt/homebrew/bin"}, []string{"/custom/bin"}); err != nil {
		t.Fatal(err)
	}
	envSh := filepath.Join(os.Getenv("HOME"), ".config", "mymac", "env.sh")
	data, err := os.ReadFile(envSh)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "export PATH='/opt/homebrew/bin:/") {
		t.Errorf("PATH must not be baked in as a literal:\n%s", content)
	}
	if !strings.Contains(content, `export PATH='/opt/homebrew/bin':"$PATH"`) {
		t.Errorf("missing dynamic prepend export:\n%s", content)
	}
	if !strings.Contains(content, `export PATH="$PATH":'/custom/bin'`) {
		t.Errorf("missing dynamic append export:\n%s", content)
	}

	// Sourcing it must actually change PATH the way we expect.
	out, err := execShell("PATH=/usr/bin:/bin; . \"" + envSh + "\"; printf %s \"$PATH\"")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "/opt/homebrew/bin:/usr/bin:/bin") {
		t.Errorf("sourced PATH = %q, want the fragment prepended", out)
	}
	if !strings.HasSuffix(out, ":/custom/bin") {
		t.Errorf("sourced PATH = %q, want the fragment appended", out)
	}
}

func TestPathFragmentWithColonIsRejected(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := m.SetPath(ctx, []string{"/a:/b"}, nil); err == nil {
		t.Error("a fragment containing a colon must be rejected")
	}
}

func TestPathMissingDirs(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	view, err := m.SetPath(ctx, []string{missing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range view.Missing {
		if d == missing {
			found = true
		}
	}
	if !found {
		t.Errorf("missing dirs = %+v, want to contain %q", view.Missing, missing)
	}
}

func TestExportRoundTrip(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := m.Create(ctx, "FOO", "bar", ""); err != nil {
		t.Fatal(err)
	}
	doc, err := m.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	if doc["version"] == nil {
		t.Error("export missing version")
	}
	envText, err := m.ExportEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envText, "export FOO='bar'") {
		t.Errorf("env export = %q", envText)
	}
}

func TestAuditTrailRecordsChanges(t *testing.T) {
	home := testHome(t)
	logPath := filepath.Join(home, ".config", "mymac", "audit.log")
	m := NewManager(Options{
		StorePath:       filepath.Join(home, ".config", "mymac", "env.json"),
		EnvSh:           filepath.Join(home, ".config", "mymac", "env.sh"),
		Launchctl:       NewFakeLaunchctl(),
		LaunchctlBinary: stubLaunchctl(t),
	})
	al := auditFor(logPath)
	m.audit = al

	ctx := context.Background()
	if _, err := m.Create(ctx, "SECRET_TOKEN", "s3cr3t", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "SECRET_TOKEN"); err != nil {
		t.Fatal(err)
	}
	entries, err := al.Read(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("audit entries = %d, want >= 2", len(entries))
	}
	// The sensitive value must never reach the log.
	for _, e := range entries {
		if strings.Contains(e.Detail, "s3cr3t") {
			t.Errorf("audit log leaked a sensitive value: %q", e.Detail)
		}
	}
}

func TestBackupsAreKept(t *testing.T) {
	home := testHome(t)
	path := filepath.Join(home, ".zshenv")
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(path, []byte("# v"+string(rune('0'+i))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := UpsertShellFile(path, MarkerBlock()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // unique timestamp per backup
	}
	backups, err := ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) < 2 {
		t.Errorf("backups = %d, want >= 2", len(backups))
	}
}

// mustStore loads the store or fails the test.
func mustStore(t *testing.T, m *Manager) *Store {
	t.Helper()
	s, err := m.Store()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
