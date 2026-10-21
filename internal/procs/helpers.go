package procs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mymac/internal/errs"
)

// readDir is os.ReadDir, wrapped so the helper set stays small.
func readDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }

// uidOf resolves a user name to a uid, or 0 when unknown.
func uidOf(name string) uint32 {
	u, err := user.Lookup(name)
	if err != nil {
		return 0
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0
	}
	return uint32(uid)
}

// appBundlePath returns the .app bundle a path belongs to.
func appBundlePath(path string) string {
	if path == "" {
		return ""
	}
	// Walk up looking for a component ending in .app.
	dir := filepath.Dir(path)
	for i := 0; i < 8; i++ {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// plistCache memoises the Info.plist lookups per bundle.
var plistCache sync.Map // appPath -> plistInfo

type plistInfo struct {
	BundleID  string
	Name      string
	Version   string
	IsIOS     bool
	Developer string
	TeamID    string
	Loaded    bool
}

// appQueue holds the bundle paths whose Info.plist has not been read yet.
type appQueue struct {
	mu    sync.Mutex
	paths map[string]bool
}

// appQ is the global queue of pending bundle paths.
var appQ appQueue

// appInfo returns the cached Info.plist data of an app bundle.
//
// Reading a plist means running plutil, which is far too slow to do inside
// the collection loop, so an unknown bundle only gets its name from the path
// and the background warmer fills in the rest.
func appInfo(appPath string) plistInfo {
	if v, ok := plistCache.Load(appPath); ok {
		return v.(plistInfo)
	}
	info := plistInfo{Name: appNameFromPath(appPath)}
	plistCache.Store(appPath, info)
	enqueueApp(appPath)
	return info
}

// enqueueApp adds a bundle path to the background queue.
func enqueueApp(appPath string) {
	appQ.mu.Lock()
	defer appQ.mu.Unlock()
	if appQ.paths == nil {
		appQ.paths = map[string]bool{}
	}
	if len(appQ.paths) > 2000 {
		return
	}
	appQ.paths[appPath] = true
}

// StartAppWarmer runs the background worker that reads the Info.plist files.
func (c *Collector) StartAppWarmer(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			drainAppQueue(ctx)
		}
	}()
}

// drainAppQueue reads the queued Info.plist files.
func drainAppQueue(ctx context.Context) {
	appQ.mu.Lock()
	paths := make([]string, 0, len(appQ.paths))
	for p := range appQ.paths {
		paths = append(paths, p)
	}
	appQ.paths = map[string]bool{}
	appQ.mu.Unlock()

	for _, p := range paths {
		if _, ok := plistCache.Load(p); ok {
			continue
		}
		if v, ok := plistCache.Load(p); ok && v.(plistInfo).Loaded {
			continue
		}
		info := readPlistInfo(ctx, p)
		plistCache.Store(p, info)
	}
}

// readPlistInfo reads the Info.plist of an app bundle.
func readPlistInfo(ctx context.Context, appPath string) plistInfo {
	info := plistInfo{Name: appNameFromPath(appPath), Loaded: true}
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	b, err := os.ReadFile(plist)
	if err != nil {
		return info
	}
	doc, err := parsePlist(b)
	if err != nil {
		return info
	}
	info.BundleID = plistString(doc, "CFBundleIdentifier")
	if n := firstNonEmpty(
		plistString(doc, "CFBundleDisplayName"),
		plistString(doc, "CFBundleName"),
	); n != "" {
		info.Name = n
	}
	info.Version = firstNonEmpty(
		plistString(doc, "CFBundleShortVersionString"),
		plistString(doc, "CFBundleVersion"),
	)
	info.Developer = plistString(doc, "CFBundleDeveloperName")
	for _, p := range plistStrings(doc, "CFBundleSupportedPlatforms") {
		if p == "iPhoneOS" || p == "iPhoneSimulator" {
			info.IsIOS = true
		}
	}
	if _, ok := doc["UIDeviceFamily"]; ok {
		info.IsIOS = true
	}
	return info
}

// bundleInfo reads and caches the Info.plist of an app bundle.
func bundleInfo(appPath string) plistInfo { return appInfo(appPath) }

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

// bundleIDFromPath returns the bundle identifier of an app bundle.
func bundleIDFromPath(appPath string) string {
	if appPath == "" {
		return ""
	}
	return appInfo(appPath).BundleID
}

// appNameFromPath returns the display name of an app bundle.
//
// The name comes from the bundle path so it is available immediately; the
// background warmer refines it with CFBundleDisplayName.
func appNameFromPath(appPath string) string {
	if appPath == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(appPath), ".app")
}

// parsePlist converts a plist into a map using plutil.
//
// plutil handles both binary and XML plists and is always present, so this
// avoids a third party plist dependency.
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

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// archCache memoises lipo results per executable path.
var archCache sync.Map // path -> archResult

type archResult struct {
	Arch    string
	Rosetta bool
}

// arch returns the cached architecture of an executable.
//
// The lookup never runs lipo: that would mean one process spawn per unique
// binary, which is far too slow for the collection loop. The background
// warmer fills the cache and the next refresh picks the values up.
func (c *Collector) arch(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if v, ok := archCache.Load(path); ok {
		r := v.(archResult)
		return r.Arch, r.Rosetta
	}
	c.enqueueArch(path)
	return "", false
}

// archQueue holds the executable paths whose architecture is still unknown.
type archQueue struct {
	mu    sync.Mutex
	paths map[string]bool
}

func (c *Collector) enqueueArch(path string) {
	c.archQ.mu.Lock()
	defer c.archQ.mu.Unlock()
	if c.archQ.paths == nil {
		c.archQ.paths = map[string]bool{}
	}
	if len(c.archQ.paths) > 4000 {
		return
	}
	c.archQ.paths[path] = true
}

// StartArchWarmer runs the background worker that fills the architecture
// cache. It stops when ctx is cancelled.
func (c *Collector) StartArchWarmer(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			c.drainArchQueue(ctx)
		}
	}()
}

// drainArchQueue computes the architecture of the queued paths.
func (c *Collector) drainArchQueue(ctx context.Context) {
	c.archQ.mu.Lock()
	paths := make([]string, 0, len(c.archQ.paths))
	for p := range c.archQ.paths {
		paths = append(paths, p)
	}
	c.archQ.paths = map[string]bool{}
	c.archQ.mu.Unlock()

	if len(paths) == 0 {
		return
	}
	const workers = 8
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, p := range paths {
		if _, done := archCache.Load(p); done {
			continue
		}
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			archCache.Store(path, detectArch(ctx, path))
		}(p)
	}
	wg.Wait()
}

// detectArch runs lipo on an executable and records the result.
func detectArch(ctx context.Context, path string) archResult {
	res := archResult{Arch: "unknown"}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "/usr/bin/lipo", "-archs", path).Output()
	if err != nil {
		return res
	}
	archs := strings.Fields(string(out))
	switch {
	case len(archs) == 0:
	case len(archs) == 1:
		res.Arch = archs[0]
	default:
		res.Arch = "universal"
	}
	// A binary that only has x86_64 slices must be running under Rosetta on
	// Apple Silicon; that is the only reliable signal available without
	// privileged APIs.
	if res.Arch == "x86_64" && isAppleSilicon() {
		res.Rosetta = true
	}
	return res
}

// isAppleSilicon reports whether the host is an arm64 Mac.
var isAppleSilicon = sync.OnceValue(func() bool {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.optional.arm64").Output()
	if err == nil {
		return strings.TrimSpace(string(out)) == "1"
	}
	out, err = exec.Command("/usr/bin/uname", "-m").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "arm64"
})

// ------------------------------------------------------------- protections

// protectedNames are the processes that must not be killed without a
// deliberate confirmation.
var protectedNames = map[string]bool{
	"launchd": true, "kernel_task": true, "WindowServer": true,
	"loginwindow": true, "SystemUIServer": true, "Dock": true,
	"Finder": true, "coreaudiod": true, "configd": true,
	"SecurityAgent": true, "cfprefsd": true, "diskarbitrationd": true,
	"hidd": true, "notifyd": true, "opendirectoryd": true,
	"powerd": true, "syslogd": true, "watchdogd": true,
	"mDNSResponder": true, "airportd": true, "bluetoothd": true,
	"runningboardd": true, "zombie reaper": true, "sandboxd": true,
	"coreservicesd": true, "securityd": true, "symptomsd": true,
	"rapportd": true, "locationd": true, "nsurlsessiond": true,
	"mediaanalysisd": true, "photolibraryd": true, "bird": true,
	"cloudd": true, "trustd": true,
}

// protectedPIDs are always protected regardless of their name.
var protectedPIDs = map[int]bool{
	0: true, 1: true, 2: true,
}

// IsProtected reports whether a process is protected from termination.
func IsProtected(name string, pid int) bool {
	if protectedPIDs[pid] {
		return true
	}
	return protectedNames[name]
}

// selfPID is the pid of the running mymac process.
//
// mymac must never be able to stop, kill or freeze itself. A frozen mymac
// keeps its port open but stops answering, the menu bar icon stays, and the
// single instance lock is still held, so the user is left with a service
// that looks alive but is not.
var selfPID = os.Getpid()

// IsSelf reports whether a pid belongs to this mymac process.
func IsSelf(pid int) bool { return pid == selfPID }

// ------------------------------------------------------------- statuses

// collectStatuses reads the run state of every process in one ps call.
//
// gopsutil's Status() runs a command per process, which costs seconds on a
// machine with a thousand processes. One `ps -axo pid=,stat=` call is enough.
func collectStatuses(ctx context.Context) map[int]string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,stat=")
	out, err := cmd.Output()
	if err != nil {
		return map[int]string{}
	}
	return parsePsStatuses(string(out))
}

// parsePsStatuses parses `ps -axo pid=,stat=` output.
func parsePsStatuses(out string) map[int]string {
	res := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		res[pid] = fields[1]
	}
	return res
}

// --------------------------------------------------------------- lsof

// collectPorts runs lsof once and groups the connections by pid.
func collectPorts(ctx context.Context) map[int][]Port {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-i")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return map[int][]Port{}
	}
	return parseLsof(string(out))
}

// parseLsof parses the output of `lsof -nP -i`.
//
// The real output looks like:
//
//	COMMAND     PID      USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
//	opencode    259 zhangsong    8u  IPv4 0x378393604f7c7f03      0t0  TCP 127.0.0.1:62814->127.0.0.1:49374 (ESTABLISHED)
//
// The NODE column is empty for network entries, so the column positions
// shift. Instead of counting columns the parser looks for the protocol
// token, which is always there.
func parseLsof(out string) map[int][]Port {
	res := map[int][]Port{}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if i == 0 && strings.HasPrefix(line, "COMMAND") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		// COMMAND PID USER FD TYPE ...
		if len(fields) < 8 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		protoIdx := -1
		for j := 5; j < len(fields); j++ {
			switch fields[j] {
			case "TCP", "UDP", "UNIX":
				protoIdx = j
			}
			if protoIdx >= 0 {
				break
			}
		}
		if protoIdx < 0 || protoIdx+1 >= len(fields) {
			continue
		}
		proto := fields[protoIdx]
		addr := fields[protoIdx+1]
		state := ""
		if protoIdx+2 < len(fields) {
			state = strings.Trim(fields[protoIdx+2], "()")
		}
		local, remote := addr, ""
		if idx := strings.Index(addr, "->"); idx >= 0 {
			local = addr[:idx]
			remote = addr[idx+2:]
		}
		res[pid] = append(res[pid], Port{
			Proto:  proto,
			Local:  local,
			Remote: remote,
			State:  state,
			FD:     fields[3],
		})
	}
	return res
}

// --------------------------------------------------------------- nettop

// collectNet runs nettop once and returns the cumulative byte counters.
func collectNet(ctx context.Context) map[int]netSample {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/bin/nettop", "-P", "-L", "1")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return map[int]netSample{}
	}
	return parseNettop(string(out))
}

// parseNettop parses the output of `nettop -P -L 1`.
//
// The real output is CSV without a header row in the default mode:
//
//	,bytes_in,bytes_out,
//	launchd.1,0,0,
//	mDNSResponder.672,601166753,497577727,
//
// With -J the counters are named. Both shapes are handled here. The process
// name may contain spaces and non-ASCII characters, and the pid is the part
// after the last dot.
func parseNettop(out string) map[int]netSample {
	res := map[int]netSample{}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Header line for the -J form.
		if strings.HasPrefix(line, ",") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 3 {
			continue
		}
		nameAndPid := fields[0]
		dot := strings.LastIndex(nameAndPid, ".")
		if dot < 0 {
			continue
		}
		pid, err := strconv.Atoi(nameAndPid[dot+1:])
		if err != nil {
			continue
		}
		in, err1 := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
		outB, err2 := strconv.ParseUint(strings.TrimSpace(fields[2]), 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		res[pid] = netSample{in: in, out: outB}
	}
	return res
}

// -------------------------------------------------------------- footprint

// Footprint returns the memory footprint of a process and the raw report.
func Footprint(ctx context.Context, pid int) (uint64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/bin/footprint", "-p", strconv.Itoa(pid))
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil && text == "" {
		return 0, "", errs.Wrap(err, errs.KindPermission, "无法读取进程 %d 的 footprint", pid)
	}
	return parseFootprint(text), text, nil
}

// parseFootprint extracts the byte count from a footprint report.
//
// The real output contains a line like:
//
//	zsh [60969]: 64-bit    Footprint: 1568 KB (16384 bytes per page)
func parseFootprint(text string) uint64 {
	for _, line := range strings.Split(text, "\n") {
		idx := strings.Index(line, "Footprint:")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len("Footprint:"):])
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		num := fields[0]
		unit := ""
		if len(fields) > 1 {
			unit = strings.ToUpper(fields[1])
		}
		value, err := strconv.ParseFloat(num, 64)
		if err != nil {
			continue
		}
		switch unit {
		case "KB":
			return uint64(value * 1024)
		case "MB":
			return uint64(value * 1024 * 1024)
		case "GB":
			return uint64(value * 1024 * 1024 * 1024)
		case "B", "":
			return uint64(value)
		}
	}
	return 0
}
