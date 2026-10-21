package apps

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mymac/internal/config"
	"mymac/internal/fsutil"
)

// Residual is one leftover file or directory of an application.
type Residual struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Size      uint64 `json:"size"`
	MatchKind string `json:"matchKind"`
	MatchedBy string `json:"matchedBy"`
	Risk      string `json:"risk"`
	Selected  bool   `json:"selected"`
	Note      string `json:"note,omitempty"`
}

// matchKind values.
const (
	MatchExactBundleID = "exactBundleId"
	MatchExactName     = "exactName"
	MatchSameApp       = "sameApp"
	MatchVendorPrefix  = "fuzzyVendor"
)

// risk values.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// userDir maps the library folder to the kind of leftover it holds.
type userDir struct {
	path string
	kind string
}

// residualDirs are the user library folders that hold app leftovers.
func residualDirs() []userDir {
	home := config.Home()
	lib := filepath.Join(home, "Library")
	return []userDir{
		{filepath.Join(lib, "Application Support"), "appSupport"},
		{filepath.Join(lib, "Caches"), "cache"},
		{filepath.Join(lib, "Preferences"), "prefs"},
		{filepath.Join(lib, "Logs"), "logs"},
		{filepath.Join(lib, "Saved Application State"), "savedState"},
		{filepath.Join(lib, "WebKit"), "webkit"},
		{filepath.Join(lib, "HTTPStorages"), "httpStorage"},
		{filepath.Join(lib, "Cookies"), "cookies"},
		{filepath.Join(lib, "Application Scripts"), "appScript"},
		{filepath.Join(lib, "Containers"), "container"},
		{filepath.Join(lib, "Group Containers"), "groupContainer"},
		{filepath.Join(lib, "LaunchAgents"), "launchAgent"},
	}
}

// systemResidualDirs are the system folders that hold app leftovers.
//
// They need root to change, so they are only reported.
func systemResidualDirs() []userDir {
	return []userDir{
		{"/Library/LaunchAgents", "launchAgent"},
		{"/Library/LaunchDaemons", "launchDaemon"},
		{"/Library/PrivilegedHelperTools", "helper"},
		{"/Library/Application Support", "appSupport"},
		{"/Library/Logs", "logs"},
	}
}

// rawResidual is one entry of the library index, before it is matched
// against an application.
//
// The size is not part of the index: measuring it means walking the
// directory, and only a handful of the thousands of library entries
// belong to any given application.
type rawResidual struct {
	path string
	base string
	kind string
}

// residualIndex is a snapshot of the library folders.
//
// Scanning the library names is cheap; the sizes are measured on demand and
// cached, so the whole index costs a directory listing instead of a full
// size walk of every folder.
type residualIndex struct {
	items []rawResidual
	at    time.Time
}

var (
	indexMu    sync.Mutex
	indexCache *residualIndex

	// sizeCache holds the measured size of the library entries.
	sizeMu    sync.Mutex
	sizeCache = map[string]uint64{}
)

// indexTTL is how long the library index stays valid.
const indexTTL = 60 * time.Second

// libraryIndex returns the current index of the library folders.
func libraryIndex() *residualIndex {
	indexMu.Lock()
	defer indexMu.Unlock()
	if indexCache != nil && time.Since(indexCache.at) < indexTTL {
		return indexCache
	}
	idx := &residualIndex{at: time.Now()}
	for _, d := range append(residualDirs(), systemResidualDirs()...) {
		entries, err := os.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			idx.items = append(idx.items, rawResidual{
				path: filepath.Join(d.path, e.Name()),
				base: baseName(e.Name()),
				kind: d.kind,
			})
		}
	}
	indexCache = idx
	return idx
}

// entrySize returns the allocated size of a library entry, measuring it once.
func entrySize(path string, isDir bool) uint64 {
	sizeMu.Lock()
	if sz, ok := sizeCache[path]; ok {
		sizeMu.Unlock()
		return sz
	}
	sizeMu.Unlock()

	var sz uint64
	if isDir {
		sz = fsutil.DirSize(path)
	} else if info, err := os.Stat(path); err == nil {
		sz = fsutil.AllocatedSize(info)
	}

	sizeMu.Lock()
	sizeCache[path] = sz
	sizeMu.Unlock()
	return sz
}

// InvalidateIndex drops the cached library index and the measured sizes.
func InvalidateIndex() {
	indexMu.Lock()
	indexCache = nil
	indexMu.Unlock()
	sizeMu.Lock()
	sizeCache = map[string]uint64{}
	sizeMu.Unlock()
}

// baseName strips the suffix a leftover uses to become a file.
func baseName(entryName string) string {
	base := strings.TrimSuffix(entryName, ".plist")
	base = strings.TrimSuffix(base, ".binarycookies")
	base = strings.TrimSuffix(base, ".savedState")
	return base
}

// isDirPath reports whether a path is a directory.
func isDirPath(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Group is the leftovers of one application, for the batch uninstall view.
type Group struct {
	BundleID string     `json:"bundleId"`
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Version  string     `json:"version,omitempty"`
	BundleSz uint64     `json:"bundleSize"`
	Running  bool       `json:"running"`
	IsSystem bool       `json:"isSystem"`
	Items    []Residual `json:"items"`
}

// FindResiduals locates the files an application left behind.
//
// Matching happens on the bundle id (exact), the application name (exact)
// and the vendor prefix (fuzzy). Every item records what it matched on, so
// the user can judge the risk before deleting anything.
func FindResiduals(app *App) ([]Residual, error) {
	if app == nil {
		return nil, nil
	}
	return matchResiduals(libraryIndex(), app), nil
}

// matchResiduals filters the index down to the entries of one application.
func matchResiduals(idx *residualIndex, app *App) []Residual {
	bundleID := app.BundleID
	name := app.Name
	vendor := vendorPrefix(bundleID)

	var out []Residual
	for _, item := range idx.items {
		_, matchedBy, ok := classify(item.base, item.kind, bundleID, name, vendor)
		if !ok {
			continue
		}
		risk := riskFor(item.kind)
		out = append(out, Residual{
			Path:      item.path,
			Kind:      item.kind,
			Size:      entrySize(item.path, isDirPath(item.path)),
			MatchKind: matchedBy,
			MatchedBy: matchSource(item.base, bundleID, name, vendor),
			Risk:      risk,
			Selected:  selectedByDefault(item.kind, risk, matchedBy),
			Note:      noteFor(item.kind),
		})
	}

	// The privileged helper tool is named after the bundle id.
	if bundleID != "" {
		helper := filepath.Join("/Library/PrivilegedHelperTools", bundleID)
		if fsutil.Exists(helper) {
			out = append(out, Residual{
				Path:      helper,
				Kind:      "helper",
				Size:      fsutil.DirSize(helper),
				MatchKind: MatchExactBundleID,
				MatchedBy: bundleID,
				Risk:      RiskHigh,
				Note:      "需要管理员权限删除",
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Risk != out[j].Risk {
			// Higher risk first, so it is impossible to miss.
			return out[i].Risk > out[j].Risk
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// DataSize returns the total size of the leftovers of an application.
func DataSize(app *App) uint64 {
	if app == nil {
		return 0
	}
	var total uint64
	for _, r := range matchResiduals(libraryIndex(), app) {
		total += r.Size
	}
	return total
}

// classify decides whether an entry belongs to the app.
//
// The rules go from precise to loose:
//
//	exactBundleId  the entry is the bundle id itself
//	exactName      the entry is named after the application
//	sameApp        a variant of the same bundle id, e.g. "-UIHelper"
//	vendorPrefix   the same developer, but possibly a different app
//
// The vendor rule is deliberately the loosest one: com.tencent. covers
// WeChat, Tencent Meeting and everything else from that vendor, so it must
// never be treated as "belongs to this app".
func classify(base, kind, bundleID, name, vendor string) (string, string, bool) {
	if bundleID == "" && name == "" {
		return "", "", false
	}
	switch {
	case bundleID != "" && base == bundleID:
		return kind, MatchExactBundleID, true
	case name != "" && base == name:
		return kind, MatchExactName, true
	case bundleID != "" && isSameApp(base, bundleID):
		return kind, MatchSameApp, true
	case vendor != "" && strings.HasPrefix(base, vendor):
		return kind, MatchVendorPrefix, true
	}
	return "", "", false
}

// isSameApp reports whether an entry is a variant of the same application,
// such as "com.nektony.App-Cleaner-SIII-UIHelper" for
// "com.nektony.App-Cleaner-SIII".
func isSameApp(base, bundleID string) bool {
	if !strings.HasPrefix(base, bundleID) {
		return false
	}
	rest := base[len(bundleID):]
	if rest == "" {
		return false
	}
	switch rest[0] {
	case '-', '_', '.':
		return true
	}
	return false
}

// riskFor returns the risk level of a leftover kind.
func riskFor(kind string) string {
	switch kind {
	case "helper", "launchDaemon":
		return RiskHigh
	case "groupContainer":
		return RiskHigh
	case "appSupport", "container", "webkit", "httpStorage", "appScript":
		return RiskMedium
	case "launchAgent":
		return RiskMedium
	}
	return RiskLow
}

// selectedByDefault decides whether an item starts checked.
//
// Group containers and privileged helpers are shared or need root, and a
// vendor match may belong to a different application, so those stay
// unchecked.
func selectedByDefault(kind, risk, matchKind string) bool {
	if risk == RiskHigh {
		return false
	}
	if matchKind == MatchVendorPrefix {
		return false
	}
	return true
}

// noteFor explains the item to the user.
func noteFor(kind string) string {
	switch kind {
	case "groupContainer":
		return "Group Container 可能被多个应用共享，默认不勾选"
	case "helper":
		return "特权辅助工具，删除需要管理员权限"
	case "launchDaemon":
		return "系统级后台服务，删除需要管理员权限"
	case "container":
		return "沙盒容器，删除后应用需要重新登录"
	}
	return ""
}

// matchSource explains what matched.
func matchSource(base, bundleID, name, vendor string) string {
	switch {
	case bundleID != "" && base == bundleID:
		return "Bundle ID: " + bundleID
	case name != "" && base == name:
		return "应用名: " + name
	case bundleID != "" && isSameApp(base, bundleID):
		return "同一应用: " + bundleID
	case vendor != "" && strings.HasPrefix(base, vendor):
		return "开发者前缀: " + vendor
	}
	return ""
}

// vendorPrefix returns the developer namespace of a bundle id.
//
// The namespace is only useful when it is specific enough. "com.microsoft."
// covers every Microsoft tool on the machine, so a bundle id needs at least
// four components before its namespace means anything: "com.tencent.workbuddy."
// belongs to one product, while "com.microsoft.VSCode" has no namespace at
// all.
func vendorPrefix(bundleID string) string {
	parts := strings.Split(bundleID, ".")
	if len(parts) < 4 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], ".") + "."
}

// VendorPrefix returns the vendor prefix of a bundle id.
func VendorPrefix(bundleID string) string { return vendorPrefix(bundleID) }
