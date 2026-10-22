// Package apps implements the application uninstaller: it finds the
// installed applications, locates the files an app leaves behind and moves
// them to the Trash.
package apps

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mymac/internal/config"
	"mymac/internal/errs"
	"mymac/internal/fsutil"
)

// App is one installed application.
type App struct {
	BundleID  string `json:"bundleId"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Path      string `json:"path"`
	Icon      string `json:"icon,omitempty"`
	BundleSz  uint64 `json:"bundleSize"`
	ExeSz     uint64 `json:"exeSize"`
	DataSz    uint64 `json:"dataSize"`
	Size      uint64 `json:"size"`
	SizeKnown bool   `json:"sizeKnown"`
	IsMAS     bool   `json:"isMAS"`
	IsIOS     bool   `json:"isIOS"`
	// IsApple marks the applications that come from Apple. They are still
	// ordinary applications the user may remove.
	IsApple bool `json:"isApple"`
	// IsSystem marks the applications that are part of macOS itself. Those
	// are the only ones that must not be removed.
	IsSystem   bool   `json:"isSystem"`
	Developer  string `json:"developer,omitempty"`
	TeamID     string `json:"teamId,omitempty"`
	Running    bool   `json:"running"`
	IconFailed bool   `json:"iconFailed,omitempty"`
}

// scanDirs are the directories that hold installed applications.
func scanDirs() []string {
	home := config.Home()
	return []string{
		"/Applications",
		"/Applications/Utilities",
		filepath.Join(home, "Applications"),
	}
}

// Scanner finds applications and their leftovers.
type Scanner struct {
	mu      sync.Mutex
	cache   []*App
	cacheAt time.Time
	// sizes holds the measured sizes per app path. It survives a rescan so
	// the numbers do not have to be measured again.
	sizes map[string]AppSizes
	// measuredAt remembers when a size was last taken, so the warmer does
	// not measure the same bundle over and over.
	measuredAt map[string]time.Time
	// warming marks a measurement that is running, so the scan does not
	// start a second one.
	warming bool
}

// AppSizes are the measured sizes of one application.
type AppSizes struct {
	Bundle uint64 `json:"bundle"`
	Exe    uint64 `json:"exe"`
	Data   uint64 `json:"data"`
	Total  uint64 `json:"total"`
}

// NewScanner returns an application scanner.
func NewScanner() *Scanner {
	return &Scanner{
		sizes:      map[string]AppSizes{},
		measuredAt: map[string]time.Time{},
	}
}

// StartSizeWarmer computes the size of the installed apps in the background.
//
// Measuring a bundle means walking thousands of files, so it must never
// happen while a list is being rendered. Only the apps whose size is unknown
// are measured, and a measurement is reused for a while: re-measuring every
// bundle every few seconds would keep the machine busy for nothing.
func (s *Scanner) StartSizeWarmer(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Second):
			}
			s.WarmSizes(ctx)
		}
	}()
}

// warmMissing measures the applications whose size is not known yet.
//
// It runs right after a scan: the warmer only ticks every 20 seconds, and
// waiting for that tick would leave the page showing "计算中…" for twenty
// seconds on a cold start. The measurement itself takes a few seconds, so
// the page is redrawn a couple of times while the numbers come in.
func (s *Scanner) warmMissing(ctx context.Context) {
	s.mu.Lock()
	if s.warming {
		s.mu.Unlock()
		return
	}
	missing := false
	for _, a := range s.cache {
		if !a.SizeKnown {
			missing = true
			break
		}
	}
	if !missing {
		s.mu.Unlock()
		return
	}
	s.warming = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.warming = false
			s.mu.Unlock()
		}()
		s.WarmSizes(ctx)
	}()
}

// sizeTTL is how long a measured size is reused.
const sizeTTL = 30 * time.Minute

// WarmSizes measures every application that has no size yet, and refreshes
// the ones whose measurement is older than sizeTTL.
//
// The work runs in parallel: a dozen `du` calls finish in a fraction of a
// second, where the same measurement done one at a time takes several.
func (s *Scanner) WarmSizes(ctx context.Context) { s.warmSizes(ctx, false) }

// MeasureSizes measures every application, ignoring the cached values.
//
// This is what a manual rescan asks for.
func (s *Scanner) MeasureSizes(ctx context.Context) { s.warmSizes(ctx, true) }

func (s *Scanner) warmSizes(ctx context.Context, force bool) {
	s.mu.Lock()
	list := s.cache
	s.mu.Unlock()
	if len(list) == 0 {
		return
	}

	const workers = 8
	jobs := make(chan *App, len(list))
	for _, a := range list {
		jobs <- a
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				if ctx.Err() != nil {
					return
				}
				// A size that is not too old is reused as is.
				s.mu.Lock()
				sz, known := s.sizes[a.Path]
				at, measured := s.measuredAt[a.Path]
				s.mu.Unlock()
				if !force && known && measured && time.Since(at) < sizeTTL {
					s.applySize(a, sz)
					continue
				}

				sz = AppSizes{
					Bundle: Size(a.Path),
					Exe:    fsutil.ExeSize(a.Path),
					Data:   DataSize(a),
				}
				sz.Total = sz.Bundle + sz.Data
				s.mu.Lock()
				s.sizes[a.Path] = sz
				s.measuredAt[a.Path] = time.Now()
				s.mu.Unlock()
				s.applySize(a, sz)
			}
		}()
	}
	wg.Wait()

	// A rescan may have replaced the cache while the workers were busy, so
	// the sizes are applied to whatever is live now.
	s.mu.Lock()
	for _, a := range s.cache {
		if sz, ok := s.sizes[a.Path]; ok {
			a.BundleSz = sz.Bundle
			a.ExeSz = sz.Exe
			a.DataSz = sz.Data
			a.Size = sz.Total
			a.SizeKnown = true
		}
	}
	s.mu.Unlock()
}

// applySize writes the measured size into an application.
func (s *Scanner) applySize(a *App, sz AppSizes) {
	s.mu.Lock()
	a.BundleSz = sz.Bundle
	a.ExeSz = sz.Exe
	a.DataSz = sz.Data
	a.Size = sz.Total
	a.SizeKnown = true
	s.mu.Unlock()
}

// DataSize is defined in residual.go: it sums the leftovers of an app.

// Scan lists the installed applications.
//
// The result is cached briefly because walking every bundle and reading its
// Info.plist is not cheap.
func (s *Scanner) Scan(ctx context.Context, rescan bool) ([]*App, error) {
	s.mu.Lock()
	cached := s.cache
	fresh := cached != nil && time.Since(s.cacheAt) < 60*time.Second
	// The warmer writes to the size map while the scan reads it, so the
	// map is copied under the lock rather than shared.
	sizes := make(map[string]AppSizes, len(s.sizes))
	for k, v := range s.sizes {
		sizes[k] = v
	}
	// The applications are copied under the lock as well. The cache belongs
	// to the scanner: the background warmer writes the measured sizes into
	// it, and a caller that held the cached applications would be reading
	// a field while it was being written.
	list := copyApps(cached)
	s.mu.Unlock()

	if !rescan && fresh {
		return list, nil
	}

	// The slow part (codesign, plutil) runs without the lock, so the size
	// warmer is not blocked while it works.
	var out []*App
	seen := map[string]bool{}
	for _, dir := range scanDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasSuffix(e.Name(), ".app") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if seen[path] {
				continue
			}
			seen[path] = true
			app := s.describe(ctx, path)
			if app == nil {
				continue
			}
			// Apply the sizes measured by the background warmer, if any.
			if sz, ok := sizes[path]; ok {
				app.BundleSz = sz.Bundle
				app.ExeSz = sz.Exe
				app.DataSz = sz.Data
				app.Size = sz.Total
				app.SizeKnown = true
			}
			out = append(out, app)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	s.mu.Lock()
	s.cache = out
	s.cacheAt = time.Now()
	// The caller gets copies: the cache stays the scanner's own, and the
	// warmer is free to write the measured sizes into it.
	list = copyApps(out)
	s.mu.Unlock()

	// The sizes of a fresh scan are not known yet. Measuring them right away
	// saves the page a wait for the next warmer tick. The context of the
	// request is dropped on purpose: it is cancelled as soon as the response
	// is written, which would abort the measurement half way.
	s.warmMissing(context.WithoutCancel(ctx))
	return list, nil
}

// copyApps returns copies of the applications.
//
// The applications are small and there are a few dozen of them, so copying
// them is cheap. Handing the cached applications out instead would let a
// caller race with the background warmer, which writes the measured sizes
// into them.
func copyApps(list []*App) []*App {
	if len(list) == 0 {
		return nil
	}
	out := make([]*App, 0, len(list))
	for _, a := range list {
		cp := *a
		out = append(out, &cp)
	}
	return out
}

// describe builds the metadata of one app bundle.
func (s *Scanner) describe(ctx context.Context, path string) *App {
	plist := filepath.Join(path, "Contents", "Info.plist")
	data, err := os.ReadFile(plist)
	if err != nil {
		return nil
	}
	doc, err := parsePlist(data)
	if err != nil {
		return nil
	}
	app := &App{
		Path:      path,
		BundleID:  plistString(doc, "CFBundleIdentifier"),
		Name:      firstNonEmpty(plistString(doc, "CFBundleDisplayName"), plistString(doc, "CFBundleName"), filepath.Base(path)),
		Version:   firstNonEmpty(plistString(doc, "CFBundleShortVersionString"), plistString(doc, "CFBundleVersion")),
		Developer: plistString(doc, "CFBundleDeveloperName"),
	}
	if app.Name == "" {
		app.Name = strings.TrimSuffix(filepath.Base(path), ".app")
	}
	// Mac App Store apps carry a receipt.
	if _, err := os.Stat(filepath.Join(path, "Contents", "_MASReceipt")); err == nil {
		app.IsMAS = true
	}
	// iPhone and iPad apps declare the iOS platform.
	for _, p := range plistStrings(doc, "CFBundleSupportedPlatforms") {
		if p == "iPhoneOS" || p == "iPhoneSimulator" {
			app.IsIOS = true
		}
	}
	if _, ok := doc["UIDeviceFamily"]; ok {
		app.IsIOS = true
	}
	// The signature tells us whether it is part of macOS itself.
	sig := ReadSignature(path)
	app.IsApple = sig.IsMAS || strings.HasPrefix(app.BundleID, "com.apple.")
	app.TeamID = sig.TeamID
	if app.Developer == "" {
		app.Developer = sig.Authority
	}
	// Only the applications in /System and the ones signed with the
	// system's own identity are off limits. An App Store application is
	// signed by Apple too, but it is an ordinary application the user may
	// remove at any time.
	app.IsSystem = isSystemApp(path) || sig.IsSystem

	// The size is expensive, so it is only computed for the detail view.
	return app
}

// isSystemApp reports whether the app lives in the read-only system
// volume.
func isSystemApp(path string) bool {
	return strings.HasPrefix(path, "/System/")
}

// Size computes the allocated size of an app bundle.
//
// The allocated size is used rather than the logical size so that APFS
// clones and sparse files are not counted twice.
func Size(path string) uint64 {
	return fsutil.DirSize(path)
}

// Signature is the code signing information of an app.
type Signature struct {
	Authority  string `json:"authority"`
	TeamID     string `json:"teamId,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	// IsSystem marks the operating system's own signing identity. Only
	// these apps are part of macOS itself.
	IsSystem bool `json:"isSystem"`
	// IsMAS marks the identity the App Store uses when it re-signs a
	// download. Every App Store app carries it, including third party
	// ones, so it says nothing about who owns the app.
	IsMAS  bool   `json:"isMAS"`
	Signed bool   `json:"signed"`
	Format string `json:"format,omitempty"`
}

// The two leaf authorities that matter.
//
//	Software Signing                     -> the operating system itself
//	Apple Mac OS Application Signing     -> the App Store distribution identity
const (
	leafSystemSigning = "Software Signing"
	leafMASSigning    = "Apple Mac OS Application Signing"
)

// ReadSignature reads the code signature of a bundle.
//
// The real output of `codesign -dv --verbose=2` looks like:
//
//	Identifier=ynote-desktop
//	Authority=Developer ID Application: NetEase Youdao (VHJLLQ36QB)
//	Authority=Developer ID Certification Authority
//	Authority=Apple Root CA
//	TeamIdentifier=VHJLLQ36QB
//
// Only the first Authority line matters: every Developer ID app chains up
// to the Apple Root CA, so the chain says nothing about who signed it.
//
// The result is cached: codesign costs about 50 ms per bundle, which is
// noticeable when the whole list is scanned.
func ReadSignature(path string) Signature {
	if v, ok := signatureCache.Load(path); ok {
		return v.(Signature)
	}
	sig := readSignature(path)
	signatureCache.Store(path, sig)
	return sig
}

// signatureCache holds the code signature of every bundle that was checked.
var signatureCache sync.Map // path -> Signature

// readSignature runs codesign and parses its output.
func readSignature(path string) Signature {
	sig := Signature{}
	out, err := exec.Command("/usr/bin/codesign", "-dv", "--verbose=2", path).CombinedOutput()
	text := string(out)
	if err != nil && text == "" {
		return sig
	}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Authority":
			if !sig.Signed {
				sig.Signed = true
				sig.Authority = value
				switch {
				case value == leafSystemSigning:
					// The operating system's own identity.
					sig.IsSystem = true
				case value == leafMASSigning:
					// The App Store re-signs every download it serves.
					sig.IsMAS = true
				}
			}
		case "TeamIdentifier":
			if value != "not set" {
				sig.TeamID = value
			}
		case "Identifier":
			sig.Identifier = value
		case "Format":
			sig.Format = value
		}
	}
	return sig
}

// IsAppleSigned reports whether the bundle carries the operating system's
// own signature.
//
// The App Store identity does not count: Microsoft Word and Keynote are
// signed by Apple too, but they are ordinary applications the user may
// remove.
func IsAppleSigned(path string) bool { return ReadSignature(path).IsSystem }

// ------------------------------------------------------------------ plist

// parsePlist converts a plist into a map using plutil.
func parsePlist(data []byte) (map[string]any, error) {
	cmd := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = strings.NewReader(string(data))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// plistString reads a string value from a parsed plist.
func plistString(doc map[string]any, key string) string {
	v, ok := doc[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// plistStrings reads an array of strings from a parsed plist.
func plistStrings(doc map[string]any, key string) []string {
	v, ok := doc[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// --------------------------------------------------------------- running

// RunningApps returns the bundle identifiers of the running applications.
func RunningApps() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir("/proc")
	_ = entries
	_ = err
	// Use ps to avoid walking the process table in Go.
	cmd := exec.Command("/bin/ps", "-axo", "comm=")
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if i := strings.Index(p, ".app/"); i > 0 {
			out[p[:i+4]] = true
		}
	}
	return out
}

// ErrNotFound is returned when an app is unknown.
var ErrNotFound = errs.New(errs.KindNotFound, "找不到该应用")

// debugSizes turns on the size warmer tracing.
var debugSizes = os.Getenv("MYMAC_DEBUG_SIZES") != ""
