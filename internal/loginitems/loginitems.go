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
			Label:  label,
			Kind:   KindService,
			Source: "launchd",
			Loaded: true,
			PID:    svc.pid,
		}
		applyState(&item, disabled, loaded)
		out.Items = append(out.Items, item)
	}

	// The legacy login items.
	out.Items = append(out.Items, c.loginItems(ctx)...)

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
			Label:     name,
			Kind:      KindLoginItem,
			Enabled:   true,
			Source:    "System Events",
			Removable: false,
			Note:      "旧式登录项，需在「系统设置 → 通用 → 登录项」中移除",
		})
	}
	return out
}

// readPlist reads a LaunchAgent or LaunchDaemon definition.
func readPlist(path string, kind Kind) Item {
	item := Item{
		Kind:      kind,
		PlistPath: path,
		Source:    filepath.Dir(path),
		Removable: true,
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
	if args, ok := doc["ProgramArguments"].([]any); ok {
		for _, a := range args {
			if s, ok := a.(string); ok {
				item.Args = append(item.Args, s)
			}
		}
	}
	if len(item.Args) > 0 {
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
