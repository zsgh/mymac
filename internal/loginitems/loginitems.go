// Package loginitems manages the startup items: the background services and
// launch agents that start with the system.
//
// The sources are the launchd domain of the current user, the LaunchAgent
// and LaunchDaemon folders, and the legacy login items of the system.
package loginitems

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mymac/internal/config"
	"mymac/internal/errs"
	"mymac/internal/trash"
)

// Kind is the kind of a startup item.
type Kind string

// Kinds.
const (
	KindLaunchAgent  Kind = "launchAgent"
	KindLaunchDaemon Kind = "launchDaemon"
	KindLoginItem    Kind = "loginItem"
	KindService      Kind = "service"
)

// Item is one startup item.
type Item struct {
	Label      string   `json:"label"`
	Kind       Kind     `json:"kind"`
	Enabled    bool     `json:"enabled"`
	Loaded     bool     `json:"loaded"`
	PID        int      `json:"pid"`
	Program    string   `json:"program,omitempty"`
	Args       []string `json:"args,omitempty"`
	PlistPath  string   `json:"plistPath,omitempty"`
	Source     string   `json:"source"`
	Developer  string   `json:"developer,omitempty"`
	Note       string   `json:"note,omitempty"`
	Removable  bool     `json:"removable"`
	NeedsAdmin bool     `json:"needsAdmin"`
	// IsSystem marks the items that belong to the operating system.
	IsSystem bool `json:"isSystem"`
	// Disableable reports whether launchd can turn the item off.
	//
	// The legacy login items are not launchd services: launchctl accepts
	// any label without an error, so a button that "disables" one would
	// only pretend to work.
	Disableable bool `json:"disableable"`
	// Group identifies the program the item belongs to. The items of one
	// program share the key, and the whole group can be turned off at
	// once.
	GroupKey  string `json:"groupKey"`
	GroupName string `json:"groupName"`
	// GroupSize is the number of items in the group, GroupOn the number
	// of them that are still enabled.
	GroupSize int `json:"groupSize"`
	GroupOn   int `json:"groupOn"`
}

// List is the whole set of startup items.
type List struct {
	Items       []Item    `json:"items"`
	CollectedAt time.Time `json:"collectedAt"`
	Warnings    []string  `json:"warnings"`
}

// Collector gathers the startup items.
type Collector struct {
	mu    sync.Mutex
	cache *List
}

// NewCollector returns a startup item collector.
func NewCollector() *Collector { return &Collector{} }

// Collect gathers the startup items.
func (c *Collector) Collect(ctx context.Context) *List {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := &List{CollectedAt: time.Now(), Items: []Item{}, Warnings: []string{}}

	disabled := c.disabledServices(ctx)
	loaded := c.loadedServices(ctx)

	// The LaunchAgent and LaunchDaemon folders hold the definitions.
	for _, d := range []struct {
		dir  string
		kind Kind
	}{
		{filepath.Join(config.Home(), "Library", "LaunchAgents"), KindLaunchAgent},
		{"/Library/LaunchAgents", KindLaunchAgent},
		{"/Library/LaunchDaemons", KindLaunchDaemon},
	} {
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".plist") {
				continue
			}
			path := filepath.Join(d.dir, e.Name())
			item := readPlist(path, d.kind)
			if item.Label == "" {
				item.Label = strings.TrimSuffix(e.Name(), ".plist")
			}
			applyState(&item, disabled, loaded)
			out.Items = append(out.Items, item)
		}
	}

	// The services in the launchd domain that have no plist of their own.
	for label, svc := range loaded {
		if hasPlist(out.Items, label) {
			continue
		}
		item := Item{
			Label:       label,
			Kind:        KindService,
			Source:      "launchd",
			Loaded:      true,
			PID:         svc.pid,
			Disableable: true,
		}
		applyState(&item, disabled, loaded)
		out.Items = append(out.Items, item)
	}

	// The legacy login items.
	out.Items = append(out.Items, c.loginItems(ctx)...)

	// The services do not say what they run; the process does. One ps call
	// answers for all of them.
	procs := runningPrograms()
	c.fillPrograms(out.Items, procs)

	// The items of one program are grouped, so the whole program can be
	// turned off at once.
	groupItems(out.Items, runningApps(procs))

	sort.Slice(out.Items, func(i, j int) bool {
		// The third party items come first; the system ones are noise.
		if out.Items[i].IsSystem != out.Items[j].IsSystem {
			return !out.Items[i].IsSystem
		}
		if out.Items[i].Kind != out.Items[j].Kind {
			return out.Items[i].Kind < out.Items[j].Kind
		}
		return out.Items[i].Label < out.Items[j].Label
	})
	return out
}

// fillPrograms fills in the program of the items that do not have a plist.
//
// A launchd service that was submitted by another process does not say what
// it runs, but the running process does.
func (c *Collector) fillPrograms(items []Item, procs map[int]string) {
	for i := range items {
		if items[i].Program != "" || items[i].PID <= 0 {
			continue
		}
		if path, ok := procs[items[i].PID]; ok {
			items[i].Program = path
		}
	}
}

// runningApps lists the app bundles of the running processes.
//
// The paths come out of the process table, so the list costs nothing extra:
// the programs were read anyway.
func runningApps(procs map[int]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, path := range procs {
		bundle := appBundle(path)
		if bundle == "" || seen[bundle] {
			continue
		}
		seen[bundle] = true
		out = append(out, bundle)
	}
	return out
}

// runningPrograms maps the pid of every running process to its program.
func runningPrograms() map[int]string {
	out := map[int]string{}
	cmd := exec.Command("/bin/ps", "-axo", "pid=,comm=")
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(pid)
		if err != nil {
			continue
		}
		out[n] = strings.TrimSpace(path)
	}
	return out
}

// groupItems groups the items by the program they belong to.
//
// The user thinks of "a program that starts at login", not of "a label":
// one app often brings three or four startup items with it, and turning
// the program off means turning off all of them.
func groupItems(items []Item, apps []string) {
	type group struct {
		name string
		size int
		on   int
	}
	groups := map[string]*group{}
	for i := range items {
		key, name := groupOf(&items[i], apps)
		items[i].GroupKey = key
		items[i].GroupName = name
		g, ok := groups[key]
		if !ok {
			g = &group{name: name}
			groups[key] = g
		}
		g.size++
		if items[i].Enabled {
			g.on++
		}
	}
	for i := range items {
		if g, ok := groups[items[i].GroupKey]; ok {
			items[i].GroupSize = g.size
			items[i].GroupOn = g.on
		}
	}
}

// groupOf returns the key and the name of the group an item belongs to.
//
// The rules, in order:
//  1. The app bundle the program lives in. The helpers of one app belong
//     together, and that is what the user means by "同一个程序".
//  2. The program path itself, when it is not inside an app.
//  3. The app of a running process whose name matches the bundle id in the
//     label: "application.com.google.Chrome.123.456" names Google Chrome.
//  4. The bundle id in the label.
//  5. The label, when nothing else is known.
func groupOf(item *Item, apps []string) (string, string) {
	if bundle := appBundle(item.Program); bundle != "" {
		return bundle, filepath.Base(strings.TrimSuffix(bundle, ".app"))
	}
	if item.Program != "" {
		return item.Program, filepath.Base(item.Program)
	}
	id := bundleIDFromLabel(item.Label)
	if id == "" {
		return item.Label, item.Label
	}
	if path, ok := matchApp(id, apps); ok {
		return path, filepath.Base(strings.TrimSuffix(path, ".app"))
	}
	return id, id
}

// appBundle returns the app bundle a program lives in.
//
// The outermost bundle wins: /Library/Input Methods/SogouInput.app/Contents/
// SogouTaskManager.app/... belongs to SogouInput.
func appBundle(program string) string {
	if program == "" {
		return ""
	}
	parts := strings.Split(program, "/")
	for i, p := range parts {
		if strings.HasSuffix(p, ".app") {
			return strings.Join(parts[:i+1], "/")
		}
	}
	return ""
}

// bundleIDFromLabel reads the bundle id out of a launchd label.
//
// Two shapes are known: the services the system submits for an app
// ("application.com.google.Chrome.123.456") and the Sparkle updaters
// ("com.microsoft.VSCode.ShipIt").
func bundleIDFromLabel(label string) string {
	if rest, ok := strings.CutPrefix(label, "application."); ok {
		// The rest is the bundle id followed by numbers.
		var parts []string
		for _, p := range strings.Split(rest, ".") {
			if _, err := strconv.Atoi(p); err == nil {
				break
			}
			parts = append(parts, p)
		}
		if len(parts) >= 2 {
			return strings.Join(parts, ".")
		}
		return ""
	}
	if id, ok := strings.CutSuffix(label, ".ShipIt"); ok && strings.Contains(id, ".") {
		return id
	}
	return ""
}

// matchApp finds the running app whose name matches a bundle id.
//
// The bundle id of an app ends with its name: com.google.Chrome is Google
// Chrome. The last part of the id that is not a generic word is used, and
// the name of the app has to contain it.
func matchApp(id string, apps []string) (string, bool) {
	parts := strings.Split(id, ".")
	for i := len(parts) - 1; i >= 0; i-- {
		word := parts[i]
		if genericWords[word] {
			continue
		}
		for _, app := range apps {
			name := filepath.Base(strings.TrimSuffix(app, ".app"))
			if strings.Contains(strings.ToLower(name), strings.ToLower(word)) {
				return app, true
			}
		}
		return "", false
	}
	return "", false
}

// genericWords are the parts of a bundle id that say nothing about the app.
var genericWords = map[string]bool{
	"app": true, "apps": true, "osx": true, "mac": true, "macos": true,
	"helper": true, "agent": true, "daemon": true, "desktop": true,
	"service": true, "services": true, "updater": true, "xpc": true,
	"client": true, "xpcservice": true,
}

// hasPlist reports whether a label already has a definition.
func hasPlist(items []Item, label string) bool {
	for _, it := range items {
		if it.Label == label {
			return true
		}
	}
	return false
}

// serviceState is the state of one launchd service.
type serviceState struct {
	pid    int
	loaded bool
}

// disabledServices reads the enabled and disabled state of the services.
func (c *Collector) disabledServices(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "print-disabled", launchDomain())
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "=>") {
			continue
		}
		key, value, _ := strings.Cut(line, "=>")
		key = strings.TrimSpace(key)
		key = strings.Trim(key, `"`)
		value = strings.TrimSpace(value)
		out[key] = value != "disabled"
	}
	return out
}

// loadedServices reads the services of the launchd domain.
func (c *Collector) loadedServices(ctx context.Context) map[string]serviceState {
	out := map[string]serviceState{}
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "print", launchDomain())
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	inServices := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !inServices {
			if strings.HasPrefix(trimmed, "services = {") {
				inServices = true
			}
			continue
		}
		if trimmed == "}" {
			break
		}
		// The rows look like: "<pid>  <state>  <label>".
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		out[fields[2]] = serviceState{pid: pid, loaded: true}
	}
	return out
}

// applyState fills in the enabled and loaded state of an item.
func applyState(item *Item, disabled map[string]bool, loaded map[string]serviceState) {
	if state, ok := disabled[item.Label]; ok {
		item.Enabled = state
	} else {
		item.Enabled = true
	}
	if svc, ok := loaded[item.Label]; ok {
		item.Loaded = true
		item.PID = svc.pid
	}
	item.IsSystem = isSystemItem(item)
}

// isSystemItem reports whether a startup item belongs to macOS.
//
// The com.apple.* labels and the /System paths are the operating system's
// own; they are not something the user installed and should not be shown
// next to the third party items.
func isSystemItem(item *Item) bool {
	if strings.HasPrefix(item.Label, "com.apple.") {
		return true
	}
	if strings.HasPrefix(item.Program, "/System/") ||
		strings.HasPrefix(item.Program, "/usr/libexec/") {
		return true
	}
	return false
}

// loginItems reads the legacy login items of the system.
func (c *Collector) loginItems(ctx context.Context) []Item {
	script := `tell application "System Events" to get the name of every login item`
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil
	}
	out := make([]Item, 0, len(names))
	for _, name := range names {
		out = append(out, Item{
			Label:   name,
			Kind:    KindLoginItem,
			Enabled: true,
			Source:  "System Events",
			// The legacy login items are not launchd services.
			// launchctl would accept any label without an error, so
			// pretending to disable one would only lie.
			Disableable: false,
			Removable:   false,
			Note:        "旧式登录项，需在「系统设置 → 通用 → 登录项」中移除",
		})
	}
	return out
}

// readPlist reads a LaunchAgent or LaunchDaemon definition.
func readPlist(path string, kind Kind) Item {
	item := Item{
		Kind:        kind,
		PlistPath:   path,
		Source:      filepath.Dir(path),
		Removable:   true,
		Disableable: true,
	}
	switch kind {
	case KindLaunchDaemon:
		item.NeedsAdmin = true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return item
	}
	cmd := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = strings.NewReader(string(b))
	out, err := cmd.Output()
	if err != nil {
		return item
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		return item
	}
	if s, ok := doc["Label"].(string); ok {
		item.Label = s
	}
	// The program is either a single path or the first argument.
	if s, ok := doc["Program"].(string); ok {
		item.Program = s
	}
	if args, ok := doc["ProgramArguments"].([]any); ok {
		for _, a := range args {
			if s, ok := a.(string); ok {
				item.Args = append(item.Args, s)
			}
		}
	}
	if item.Program == "" && len(item.Args) > 0 {
		item.Program = item.Args[0]
	}
	if runAtLoad, ok := doc["RunAtLoad"].(bool); ok && runAtLoad {
		item.Note = "登录时启动"
	}
	return item
}

// launchDomain is the per-user launchd domain.
func launchDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

// SetEnabled enables or disables a service.
//
// The change only lasts until the next login, but it takes effect
// immediately, which is what the user asked for.
func SetEnabled(ctx context.Context, label string, enabled bool) error {
	if strings.TrimSpace(label) == "" {
		return errs.New(errs.KindInvalid, "标签不能为空")
	}
	verb := "disable"
	if enabled {
		verb = "enable"
	}
	cmd := exec.CommandContext(ctx, "/bin/launchctl", verb, launchDomain()+"/"+label)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "launchctl %s 失败: %s", verb, strings.TrimSpace(string(out)))
	}
	return nil
}

// GroupResult reports what a group action did.
type GroupResult struct {
	// Key and Name identify the group that was handled.
	Key  string `json:"key"`
	Name string `json:"name"`
	// Total is the number of items in the group, Changed the number of
	// items that were actually turned on or off.
	Total   int      `json:"total"`
	Changed int      `json:"changed"`
	Skipped []string `json:"skipped,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// SetGroupEnabled enables or disables every startup item of one program.
//
// One app often brings several startup items with it, and the user thinks
// of "the program that starts at login", not of the individual labels. The
// group is looked up in a fresh collection, so the action always covers
// the items that are on screen.
func (c *Collector) SetGroupEnabled(ctx context.Context, key string, enabled bool) (GroupResult, error) {
	if strings.TrimSpace(key) == "" {
		return GroupResult{}, errs.New(errs.KindInvalid, "分组不能为空")
	}
	list := c.Collect(ctx)

	var members []Item
	res := GroupResult{Key: key}
	for _, it := range list.Items {
		if it.GroupKey != key {
			continue
		}
		members = append(members, it)
		res.Name = it.GroupName
	}
	if len(members) == 0 {
		return GroupResult{}, errs.New(errs.KindNotFound, "找不到这个程序的启动项")
	}
	res.Total = len(members)

	for _, it := range members {
		if !it.Disableable {
			// The legacy login items are not launchd services; they
			// can only be removed in the system settings.
			res.Skipped = append(res.Skipped, it.Label)
			continue
		}
		if err := SetEnabled(ctx, it.Label, enabled); err != nil {
			res.Failed = append(res.Failed, it.Label+": "+errs.As(err).Message)
			continue
		}
		res.Changed++
	}
	return res, nil
}

// Remove deletes a LaunchAgent definition.
//
// The file goes to the Trash, so a mistake can be undone.
func Remove(ctx context.Context, path string) error {
	clean := filepath.Clean(path)
	// Only the known folders may be touched.
	allowed := []string{
		filepath.Join(config.Home(), "Library", "LaunchAgents"),
		"/Library/LaunchAgents",
		"/Library/LaunchDaemons",
	}
	ok := false
	for _, dir := range allowed {
		if strings.HasPrefix(clean, dir+"/") {
			ok = true
			break
		}
	}
	if !ok {
		return errs.New(errs.KindPermission, "只能删除 LaunchAgent / LaunchDaemon 目录中的文件")
	}
	if _, err := os.Stat(clean); err != nil {
		return errs.New(errs.KindNotFound, "文件不存在")
	}
	res, err := trash.Move(ctx, []string{clean})
	if err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		return errs.New(errs.KindInternal, "%s", res.Failed[0].Reason)
	}
	return nil
}
