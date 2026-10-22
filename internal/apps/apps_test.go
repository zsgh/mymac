package apps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeApp builds a minimal app bundle in a temp directory.
//
// The tests never touch the real /Applications.
func fakeApp(t *testing.T, dir, name, bundleID string, extra map[string]string) string {
	t.Helper()
	bundle := filepath.Join(dir, name+".app")
	macOS := filepath.Join(bundle, "Contents", "MacOS")
	resources := filepath.Join(bundle, "Contents", "Resources")
	for _, d := range []string{macOS, resources} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>` + bundleID + `</string>
	<key>CFBundleName</key><string>` + name + `</string>
	<key>CFBundleShortVersionString</key><string>1.2.3</string>
	<key>CFBundleExecutable</key><string>` + name + `</string>
` + extraPlist(extra) + `</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macOS, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func extraPlist(extra map[string]string) string {
	out := ""
	for k, v := range extra {
		out += "\t<key>" + k + "</key><string>" + v + "</string>\n"
	}
	return out
}

// TestParsePlist uses a real Info.plist.
func TestParsePlist(t *testing.T) {
	dir := t.TempDir()
	bundle := fakeApp(t, dir, "Example", "com.example.app", nil)
	data, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parsePlist(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := plistString(doc, "CFBundleIdentifier"); got != "com.example.app" {
		t.Errorf("bundle id = %q", got)
	}
	if got := plistString(doc, "CFBundleShortVersionString"); got != "1.2.3" {
		t.Errorf("version = %q", got)
	}
	if got := plistString(doc, "Missing"); got != "" {
		t.Errorf("missing key = %q", got)
	}
	if got := plistStrings(doc, "Missing"); got != nil {
		t.Errorf("missing array = %v", got)
	}
}

// TestVendorPrefix covers the vendor prefix used for fuzzy matching.
func TestVendorPrefix(t *testing.T) {
	cases := map[string]string{
		"com.example.app":             "",
		"com.example.app.helper":      "com.example.app.",
		"com.microsoft.VSCode":        "",
		"io.github.clash-verge-rev.x": "io.github.clash-verge-rev.",
		"com.tencent.workbuddy.mac":   "com.tencent.workbuddy.",
		"single":                      "",
		"":                            "",
	}
	for in, want := range cases {
		if got := VendorPrefix(in); got != want {
			t.Errorf("VendorPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClassifyResidual covers the three match kinds.
func TestClassifyResidual(t *testing.T) {
	bundleID := "com.example.app"
	name := "Example"
	vendor := "com.example."

	cases := []struct {
		entry string
		kind  string
		match string
		ok    bool
	}{
		{"com.example.app", "prefs", MatchExactBundleID, true},
		{"com.example.app.plist", "prefs", MatchExactBundleID, true},
		{"Example", "appSupport", MatchExactName, true},
		{"com.example.other", "appSupport", MatchVendorPrefix, true},
		{"com.other.app", "appSupport", "", false},
		{"SomethingElse", "appSupport", "", false},
	}
	for _, c := range cases {
		base := strings.TrimSuffix(strings.TrimSuffix(c.entry, ".plist"), ".binarycookies")
		base = strings.TrimSuffix(base, ".savedState")
		_, match, ok := classify(base, c.kind, bundleID, name, vendor)
		if ok != c.ok {
			t.Errorf("classify(%q) ok = %v, want %v", c.entry, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if match != c.match {
			t.Errorf("classify(%q) match = %q, want %q", c.entry, match, c.match)
		}
	}
}

// TestRiskLevels checks that the dangerous kinds are marked high.
func TestRiskLevels(t *testing.T) {
	if got := riskFor("helper"); got != RiskHigh {
		t.Errorf("helper risk = %q", got)
	}
	if got := riskFor("launchDaemon"); got != RiskHigh {
		t.Errorf("launchDaemon risk = %q", got)
	}
	if got := riskFor("groupContainer"); got != RiskHigh {
		t.Errorf("groupContainer risk = %q", got)
	}
	if got := riskFor("cache"); got != RiskLow {
		t.Errorf("cache risk = %q", got)
	}
	// High risk items must not be selected by default.
	if selectedByDefault("groupContainer", RiskHigh, MatchExactBundleID) {
		t.Error("a high risk item must not be selected by default")
	}
	if !selectedByDefault("cache", RiskLow, MatchExactBundleID) {
		t.Error("a cache must be selected by default")
	}
	// A vendor match may belong to a different application.
	if selectedByDefault("appSupport", RiskMedium, MatchVendorPrefix) {
		t.Error("a vendor match must not be selected by default")
	}
	if !selectedByDefault("appSupport", RiskMedium, MatchSameApp) {
		t.Error("a variant of the same app must be selected by default")
	}
}

// TestVendorPrefixIsSpecific makes sure the developer namespace is narrow
// enough not to catch unrelated applications of the same vendor.
func TestVendorPrefixIsSpecific(t *testing.T) {
	cases := map[string]string{
		"com.tencent.workbuddy.mac": "com.tencent.workbuddy.",
		"com.nektony.App-Cleaner":   "",
		"com.microsoft.VSCode":      "",
		"single":                    "",
		"":                          "",
	}
	for in, want := range cases {
		if got := VendorPrefix(in); got != want {
			t.Errorf("VendorPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsSameApp covers the "same application, different suffix" rule.
func TestIsSameApp(t *testing.T) {
	bundleID := "com.nektony.App-Cleaner-SIII"
	if !isSameApp("com.nektony.App-Cleaner-SIII-UIHelper", bundleID) {
		t.Error("a -UIHelper variant is the same app")
	}
	if !isSameApp("com.nektony.App-Cleaner-SIII.helper", bundleID) {
		t.Error("a .helper variant is the same app")
	}
	if isSameApp("com.nektony.App-Cleaner-SIIIOther", bundleID) {
		t.Error("a name that merely starts the same way is not the same app")
	}
	if isSameApp("com.tencent.xinWeChat", "com.tencent.workbuddy.mac") {
		t.Error("another vendor app must not match")
	}
}

// TestSizeUsesAllocatedSize checks that the size is the allocated size.
func TestSizeUsesAllocatedSize(t *testing.T) {
	dir := t.TempDir()
	bundle := fakeApp(t, dir, "Sized", "com.example.sized", nil)
	// Write a file with a known size.
	payload := make([]byte, 100*1024)
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Resources", "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	size := Size(bundle)
	if size < uint64(len(payload)) {
		t.Errorf("size = %d, want at least %d", size, len(payload))
	}
}

// TestSizeOfEmptyBundle makes sure a missing bundle does not panic.
func TestSizeOfEmptyBundle(t *testing.T) {
	if got := Size(filepath.Join(t.TempDir(), "nope")); got != 0 {
		t.Errorf("size of a missing bundle = %d", got)
	}
}

// TestFindResidualsInTempHome runs the residual finder against a fake home.
func TestFindResidualsInTempHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lib := filepath.Join(home, "Library")
	// A leftover that belongs to the app.
	appSupport := filepath.Join(lib, "Application Support", "com.example.app")
	if err := os.MkdirAll(appSupport, 0o755); err != nil {
		t.Fatal(err)
	}
	// A leftover of another app.
	other := filepath.Join(lib, "Application Support", "com.other.app")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	// A variant of the same application.
	fuzzy := filepath.Join(lib, "Caches", "com.example.app.helper")
	if err := os.MkdirAll(fuzzy, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unrelated application of the same vendor must not be reported.
	sameVendor := filepath.Join(lib, "Caches", "com.example.otherapp")
	if err := os.MkdirAll(sameVendor, 0o755); err != nil {
		t.Fatal(err)
	}

	app := &App{BundleID: "com.example.app", Name: "Example"}
	residuals, err := FindResiduals(app)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]Residual{}
	for _, r := range residuals {
		found[r.Path] = r
	}
	if _, ok := found[appSupport]; !ok {
		t.Errorf("the app support folder was not found: %+v", residuals)
	}
	if _, ok := found[fuzzy]; !ok {
		t.Errorf("the fuzzy match was not found: %+v", residuals)
	}
	if _, ok := found[sameVendor]; ok {
		t.Error("an unrelated app of the same vendor must not be reported")
	}
}

// TestMatchSource explains what matched.
func TestMatchSource(t *testing.T) {
	if got := matchSource("com.example.app", "com.example.app", "Example", "com.example."); got == "" {
		t.Error("expected an explanation")
	}
	if got := matchSource("unrelated", "com.example.app", "Example", "com.example."); got != "" {
		t.Errorf("unrelated = %q", got)
	}
}

// TestWarmMissingMeasuresRightAway covers the cold start.
//
// The warmer only ticks every 20 seconds. Without a measurement right after
// the scan, the page would show "计算中…" for twenty seconds and poll the
// server the whole time.
func TestWarmMissingMeasuresRightAway(t *testing.T) {
	dir := t.TempDir()
	bundle := fakeApp(t, dir, "Example", "com.example.app", nil)

	s := NewScanner()
	s.mu.Lock()
	s.cache = []*App{{Path: bundle, Name: "Example"}}
	s.mu.Unlock()

	s.warmMissing(context.Background())

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		known := s.cache[0].SizeKnown
		s.mu.Unlock()
		if known {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the sizes were not measured")
}

// TestWarmMissingSkipsKnownSizes makes sure a warm start does no work.
func TestWarmMissingSkipsKnownSizes(t *testing.T) {
	s := NewScanner()
	s.mu.Lock()
	s.cache = []*App{{Path: "/tmp/example.app", Name: "Example", SizeKnown: true}}
	s.mu.Unlock()

	s.warmMissing(context.Background())
	time.Sleep(200 * time.Millisecond)

	s.mu.Lock()
	warming := s.warming
	s.mu.Unlock()
	if warming {
		t.Error("a measurement was started although every size is known")
	}
}
