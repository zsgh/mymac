// Package procs implements process collection and control.
//
// Collection runs in a background goroutine and writes into a cache; the HTTP
// layer only ever reads the cache. That keeps the list endpoint cheap even
// with a thousand processes on the machine.
package procs

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/process"

	"mymac/internal/ai"
	"mymac/internal/envs"
	"mymac/internal/errs"
)

// State is the run state of a process.
type State string

// Process states.
const (
	StateRunning  State = "running"
	StateSleeping State = "sleeping"
	StateStopped  State = "stopped"
	StateZombie   State = "zombie"
	StateUnknown  State = ""
)

// Port is one network connection of a process.
type Port struct {
	Proto  string `json:"proto"`
	Local  string `json:"local"`
	Remote string `json:"remote"`
	State  string `json:"state"`
	FD     string `json:"fd"`
}

// Process is one row in the process table.
type Process struct {
	PID       int      `json:"pid"`
	PPID      int      `json:"ppid"`
	UID       uint32   `json:"uid"`
	User      string   `json:"user"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Args      []string `json:"args,omitempty"`
	CPU       float64  `json:"cpu"`
	Mem       uint64   `json:"mem"`
	Footprint uint64   `json:"footprint,omitempty"`
	Threads   int32    `json:"threads"`
	FDs       int32    `json:"fds"`
	Status    string   `json:"status"`
	State     State    `json:"state"`
	StartTime int64    `json:"startTime"`
	Arch      string   `json:"arch"`
	Rosetta   bool     `json:"rosetta"`
	AppID     string   `json:"appId,omitempty"`
	AppName   string   `json:"appName,omitempty"`
	AppPath   string   `json:"appPath,omitempty"`
	Protected bool     `json:"protected"`
	// Self marks the mymac process itself.
	//
	// mymac must not stop or kill itself: a frozen mymac keeps its port
	// open but stops answering, and the single instance lock is still held,
	// so no new instance can start either.
	Self     bool   `json:"self"`
	Signer   string `json:"signer,omitempty"`
	Apple    bool   `json:"apple"`
	Ports    []Port `json:"ports,omitempty"`
	NetIn    uint64 `json:"netIn"`
	NetOut   uint64 `json:"netOut"`
	Group    string `json:"group,omitempty"`
	Children []int  `json:"children,omitempty"`
	Depth    int    `json:"depth"`
	// Note is what the model says about the program.
	Note string `json:"note,omitempty"`
	// NotePurpose is what the user is using the program for.
	NotePurpose string `json:"notePurpose,omitempty"`
	// NoteRisk is the risk the model gave the program.
	NoteRisk string `json:"noteRisk,omitempty"`
}

// Collection is one snapshot of the process table.
type Collection struct {
	Processes   []Process `json:"processes"`
	CollectedAt time.Time `json:"collectedAt"`
	Duration    int64     `json:"durationMs"`
	Count       int       `json:"count"`
}

// Collector gathers process information on a schedule.
type Collector struct {
	mu       sync.RWMutex
	cache    *Collection
	interval time.Duration

	archCache map[string]archResult
	archQ     archQueue
	prevTimes map[int32]cpu.TimesStat
	prevAt    time.Time

	// The port and network lookups shell out and take a few hundred
	// milliseconds, so they run in the background and the collection only
	// reads the cached result.
	portsMu    sync.RWMutex
	portsCache map[int][]Port
	netCache   map[int]netSample
	netRate    map[int]netSample
	netAt      time.Time
	netPrev    map[int]netSample
	netPrevAt  time.Time

	// ai explains the running programs. It is optional: without a model
	// the collector works exactly as before.
	ai ai.Provider
	// notes holds what the model said about the programs, by name.
	notes map[string]ai.ProcessNote
	// analyzing marks a running explanation.
	analyzing bool
	// analyzedAt is when the last explanation finished.
	analyzedAt time.Time
	// analyzeErr holds the error of the last explanation.
	analyzeErr string

	portsEnabled bool
	netEnabled   bool
}

type netSample struct {
	in  uint64
	out uint64
}

// NewCollector returns a collector with the given refresh interval.
func NewCollector(interval time.Duration) *Collector {
	return &Collector{
		interval:  interval,
		archCache: map[string]archResult{},
		prevTimes: map[int32]cpu.TimesStat{},
		netPrev:   map[int]netSample{},
		notes:     map[string]ai.ProcessNote{},
	}
}

// SetAI gives the collector a way to ask a model.
//
// The provider is called every time, so a model that is set up later is
// picked up without restarting anything.
func (c *Collector) SetAI(p ai.Provider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ai = p
}

// SetPortsEnabled turns the lsof collection on or off.
func (c *Collector) SetPortsEnabled(on bool) { c.portsEnabled = on }

// SetNetEnabled turns the nettop collection on or off.
func (c *Collector) SetNetEnabled(on bool) { c.netEnabled = on }

// Snapshot returns the cached collection, collecting one if the cache is
// cold or stale.
func (c *Collector) Snapshot(ctx context.Context) *Collection {
	c.mu.RLock()
	cache := c.cache
	c.mu.RUnlock()
	if cache != nil && time.Since(cache.CollectedAt) < c.interval {
		return cache
	}
	return c.Collect(ctx)
}

// Peek returns the cached collection without collecting a new one.
//
// It is used by the background workers, which must never make the machine
// busier than the work they are watching for.
func (c *Collector) Peek() *Collection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cache == nil {
		return &Collection{Processes: []Process{}}
	}
	return c.cache
}

// Collect gathers a fresh snapshot.
func (c *Collector) Collect(ctx context.Context) *Collection {
	start := time.Now()
	out := &Collection{CollectedAt: start, Processes: []Process{}}

	list, err := process.Processes()
	if err != nil {
		c.store(out)
		return out
	}

	users := userNames()
	ports := c.ports()

	// A single ps call for the whole table is far cheaper than asking
	// gopsutil per process, which shells out every time.
	statuses := collectStatuses(ctx)

	procs := make([]Process, 0, len(list))
	times := make(map[int32]cpu.TimesStat, len(list))
	for _, p := range list {
		pr := c.build(ctx, p, users, ports, statuses)
		procs = append(procs, pr)
		if t, err := p.Times(); err == nil {
			times[p.Pid] = *t
		}
	}

	// CPU percentages need a baseline: the first collection reports zero and
	// primes the counters.
	c.applyCPU(procs, times, start)
	c.applyNet(procs)

	buildTree(procs)
	sort.SliceStable(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	c.applyNotes(procs)
	out.Processes = procs
	out.Count = len(procs)
	out.Duration = time.Since(start).Milliseconds()
	c.store(out)
	return out
}

func (c *Collector) store(out *Collection) {
	c.mu.Lock()
	c.cache = out
	c.mu.Unlock()
}

// applyCPU computes the CPU percentage from the delta of the cumulative CPU
// times since the previous collection.
func (c *Collector) applyCPU(procs []Process, times map[int32]cpu.TimesStat, now time.Time) {
	elapsed := now.Sub(c.prevAt).Seconds()
	if elapsed <= 0 || len(c.prevTimes) == 0 {
		c.prevTimes = times
		c.prevAt = now
		return
	}
	for i := range procs {
		pid := int32(procs[i].PID)
		cur, ok := times[pid]
		if !ok {
			continue
		}
		prev, ok := c.prevTimes[pid]
		if !ok {
			continue
		}
		delta := (cur.User - prev.User) + (cur.System - prev.System)
		if delta < 0 {
			continue
		}
		procs[i].CPU = delta / elapsed * 100
	}
	c.prevTimes = times
	c.prevAt = now
}

// applyNet assigns the cached network rates to the process rows.
func (c *Collector) applyNet(procs []Process) {
	rates := c.netRates()
	if len(rates) == 0 {
		return
	}
	for i := range procs {
		if r, ok := rates[procs[i].PID]; ok {
			procs[i].NetIn = r.in
			procs[i].NetOut = r.out
		}
	}
}

// ports returns the cached port table.
func (c *Collector) ports() map[int][]Port {
	c.portsMu.RLock()
	defer c.portsMu.RUnlock()
	return c.portsCache
}

// netRates returns the cached per-process network rates.
func (c *Collector) netRates() map[int]netSample {
	c.portsMu.RLock()
	defer c.portsMu.RUnlock()
	return c.netRate
}

// StartBackground runs the background workers that refresh the port and
// network caches. It stops when ctx is cancelled.
//
// The two run on their own clocks: lsof is cheap and can be frequent, while
// nettop samples every connection of the machine and takes five seconds per
// call, so it runs rarely.
func (c *Collector) StartBackground(ctx context.Context) {
	// Fill the caches in the background, so the first page view has data.
	// This cannot happen inline: nettop alone takes five seconds per call
	// and the server must not wait for it before it starts listening.
	go func() {
		c.Collect(ctx)
		c.refreshPorts(ctx)
		c.refreshNet(ctx)
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Second):
				c.refreshPorts(ctx)
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Minute):
				// nettop needs five seconds per call, so it runs rarely.
				// The interface totals on the dashboard come from netstat
				// and are always current.
				c.refreshNet(ctx)
			}
		}
	}()
}

// refreshPorts updates the port cache.
func (c *Collector) refreshPorts(ctx context.Context) {
	if !c.portsEnabled {
		return
	}
	ports := collectPorts(ctx)
	c.portsMu.Lock()
	c.portsCache = ports
	c.portsMu.Unlock()
}

// refreshNet updates the network counter cache and computes the rates.
//
// nettop reports cumulative counters, so the rate is the difference between
// two samples. The sampling happens here in the background, which keeps the
// numbers meaningful no matter how often the process table is collected.
func (c *Collector) refreshNet(ctx context.Context) {
	if !c.netEnabled {
		return
	}
	net := collectNet(ctx)
	now := time.Now()

	c.portsMu.Lock()
	rates := map[int]netSample{}
	if elapsed := now.Sub(c.netPrevAt).Seconds(); elapsed > 0 && len(c.netPrev) > 0 {
		for pid, cur := range net {
			prev, ok := c.netPrev[pid]
			if !ok {
				continue
			}
			var r netSample
			if cur.in >= prev.in {
				r.in = uint64(float64(cur.in-prev.in) / elapsed)
			}
			if cur.out >= prev.out {
				r.out = uint64(float64(cur.out-prev.out) / elapsed)
			}
			rates[pid] = r
		}
	}
	c.netCache = net
	c.netRate = rates
	c.netPrev = net
	c.netPrevAt = now
	c.netAt = now
	c.portsMu.Unlock()
}

// processName returns the name to show for a process.
//
// Some system daemons come back from the process table without a name. The
// executable is the next best thing, and an empty name would also break the
// protection rules, which match on the name.
func processName(name, path string) string {
	if name != "" {
		return name
	}
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

// build fills in one process row.
func (c *Collector) build(ctx context.Context, p *process.Process, users map[uint32]string, ports map[int][]Port, statuses map[int]string) Process {
	pr := Process{PID: int(p.Pid)}

	if ppid, err := p.Ppid(); err == nil {
		pr.PPID = int(ppid)
	}
	if uids, err := p.Uids(); err == nil && len(uids) > 0 {
		pr.UID = uids[0]
		pr.User = users[pr.UID]
		if pr.User == "" {
			pr.User = strconv.FormatUint(uint64(pr.UID), 10)
		}
	}
	if name, err := p.Name(); err == nil {
		pr.Name = name
	}
	if path, err := p.Exe(); err == nil {
		pr.Path = path
	}
	pr.Name = processName(pr.Name, pr.Path)
	if args, err := p.CmdlineSlice(); err == nil {
		pr.Args = args
	}
	if mem, err := p.MemoryInfo(); err == nil && mem != nil {
		pr.Mem = mem.RSS
	}
	if t, err := p.NumThreads(); err == nil {
		pr.Threads = t
	}
	if fds, err := p.NumFDs(); err == nil {
		pr.FDs = fds
	}
	if st, ok := statuses[pr.PID]; ok {
		pr.Status = st
		pr.State = mapState(st)
	}
	if ct, err := p.CreateTime(); err == nil {
		pr.StartTime = ct / 1000
	}
	pr.Ports = ports[pr.PID]

	pr.AppPath = appBundlePath(pr.Path)
	if pr.AppPath != "" {
		pr.AppID = bundleIDFromPath(pr.AppPath)
		pr.AppName = appNameFromPath(pr.AppPath)
	}
	pr.Arch, pr.Rosetta = c.arch(pr.Path)
	pr.Protected = IsProtected(pr.Name, pr.PID)
	pr.Self = IsSelf(pr.PID)
	pr.Group = pr.Name
	if pr.AppName != "" {
		pr.Group = pr.AppName
	}
	return pr
}

// mapState converts a process status into our state.
//
// The status comes from the STAT column of ps, which is a letter plus some
// modifiers: "Ss" (sleeping, session leader), "R+" (running, foreground),
// "T" (stopped), "Z+" (zombie).
func mapState(st string) State {
	if st == "" {
		return StateUnknown
	}
	switch st[0] {
	case 'R':
		return StateRunning
	case 'S', 'D', 'I', 'U':
		return StateSleeping
	case 'T':
		return StateStopped
	case 'Z':
		return StateZombie
	case 'W':
		return StateSleeping
	}
	return StateUnknown
}

// buildTree links children to parents and computes the tree depth.
func buildTree(procs []Process) {
	byPID := make(map[int]*Process, len(procs))
	for i := range procs {
		byPID[procs[i].PID] = &procs[i]
	}
	for i := range procs {
		p := &procs[i]
		if parent, ok := byPID[p.PPID]; ok && parent.PID != p.PID {
			parent.Children = append(parent.Children, p.PID)
		}
	}
	depth := map[int]int{}
	var compute func(pid, guard int) int
	compute = func(pid, guard int) int {
		if guard > 64 {
			return 0
		}
		if d, ok := depth[pid]; ok {
			return d
		}
		p, ok := byPID[pid]
		if !ok {
			return 0
		}
		d := 0
		if parent, ok := byPID[p.PPID]; ok && parent.PID != p.PID {
			d = compute(parent.PID, guard+1) + 1
		}
		depth[pid] = d
		return d
	}
	for i := range procs {
		procs[i].Depth = compute(procs[i].PID, 0)
	}
}

// userNames maps uid to user name.
func userNames() map[uint32]string {
	out := map[uint32]string{}
	entries, err := readDir("/Users")
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			out[uidOf(e.Name())] = e.Name()
		}
	}
	out[0] = "root"
	return out
}

// StartedBefore returns the running processes that started before t.
// It satisfies envs.ProcessLister.
//
// A zero time means "every running process".
func (c *Collector) StartedBefore(t time.Time) []envs.RunningProcess {
	snap := c.Snapshot(context.Background())
	var out []envs.RunningProcess
	for _, p := range snap.Processes {
		if p.StartTime == 0 {
			continue
		}
		if !t.IsZero() && !time.Unix(p.StartTime, 0).Before(t) {
			continue
		}
		out = append(out, envs.RunningProcess{PID: p.PID, Name: p.Name, StartTime: time.Unix(p.StartTime, 0)})
	}
	return out
}

// Find returns the process with the given pid.
func Find(snap *Collection, pid int) (Process, bool) {
	for _, p := range snap.Processes {
		if p.PID == pid {
			return p, true
		}
	}
	return Process{}, false
}

// ProcessDetail is the extended information shown in the detail panel.
type ProcessDetail struct {
	Process
	FootprintDetail string   `json:"footprintDetail,omitempty"`
	OpenFiles       []string `json:"openFiles,omitempty"`
	Environment     []EnvVar `json:"environment,omitempty"`
	EnvError        string   `json:"envError,omitempty"`
}

// Detail gathers extended information for one process.
func (c *Collector) Detail(ctx context.Context, pid int) (*ProcessDetail, error) {
	snap := c.Snapshot(ctx)
	p, ok := Find(snap, pid)
	if !ok {
		return nil, errs.New(errs.KindProcessGone, "进程 %d 已退出", pid)
	}
	d := &ProcessDetail{Process: p}
	if fp, detail, err := Footprint(ctx, pid); err == nil {
		d.Footprint = fp
		d.FootprintDetail = detail
	}
	if env, err := EnvironmentOf(ctx, pid); err == nil {
		d.Environment = env
	} else {
		d.EnvError = errs.As(err).Message
	}
	return d, nil
}
