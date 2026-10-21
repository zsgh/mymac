package ports

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"mymac/internal/errs"
	"mymac/internal/rules"
)

// Process is a process that owns at least one socket.
type Process struct {
	PID          int32          `json:"pid"`
	PPID         int32          `json:"ppid"`
	Name         string         `json:"name"`
	Exe          string         `json:"exe"`
	Cmdline      []string       `json:"cmdline,omitempty"`
	Cwd          string         `json:"cwd,omitempty"`
	Username     string         `json:"username"`
	State        string         `json:"state"`
	CreateTime   int64          `json:"createTime"`
	CPUPercent   float64        `json:"cpuPercent"`
	Memory       uint64         `json:"memory"`
	VMS          uint64         `json:"vms"`
	Threads      int32          `json:"threads"`
	FDs          int32          `json:"fds"`
	Connections  []Connection   `json:"connections"`
	ListenCount  int            `json:"listenCount"`
	Decision     rules.Decision `json:"decision"`
	PortDecision rules.Decision `json:"portDecision"`
	// PermissionDenied marks the processes whose details could not be read.
	PermissionDenied bool `json:"permissionDenied,omitempty"`
	// Self marks the mymac process itself. It must not be stopped, killed
	// or restarted: mymac would take its own web interface down with it.
	Self bool `json:"self,omitempty"`
	// SystemCheck is "unknown" when the identity could not be confirmed.
	SystemCheck string `json:"systemCheck,omitempty"`
	// Error explains why the details could not be read.
	Error string `json:"error,omitempty"`
}

// Snapshot is the whole port view.
type Snapshot struct {
	Processes   []Process `json:"processes"`
	HiddenCount int       `json:"hiddenCount"`
	CollectedAt time.Time `json:"collectedAt"`
	DurationMS  int64     `json:"durationMs"`
	Stats       Stats     `json:"stats"`
	// Error explains why the collection failed, if it did.
	Error string `json:"error,omitempty"`
}

// Stats counts the snapshot.
type Stats struct {
	Total     int `json:"total"`
	Listen    int `json:"listen"`
	System    int `json:"system"`
	Hidden    int `json:"hidden"`
	Whitelist int `json:"whitelisted"`
}

// Collector gathers the port view.
type Collector struct {
	mu       sync.RWMutex
	cache    *Snapshot
	interval time.Duration
	store    *rules.Store

	// cpuPrev holds the previous CPU times per pid, so the percentage is
	// computed from the difference.
	cpuPrev map[int32]cpuSample
	prevAt  time.Time
}

type cpuSample struct {
	user   float64
	system float64
}

// NewCollector returns a port collector.
func NewCollector(store *rules.Store, interval time.Duration) *Collector {
	return &Collector{
		interval: interval,
		store:    store,
		cpuPrev:  map[int32]cpuSample{},
	}
}

// Snapshot returns the cached view, collecting a new one when it is stale.
func (c *Collector) Snapshot(ctx context.Context) *Snapshot {
	c.mu.RLock()
	cache := c.cache
	c.mu.RUnlock()
	if cache != nil && time.Since(cache.CollectedAt) < c.interval {
		return cache
	}
	return c.Collect(ctx)
}

// Peek returns the cached view without collecting a new one.
//
// It is used by the endpoints that only want a number for a card: a stale
// count is fine there, and collecting on every request would keep the
// machine busy.
func (s *Collector) Peek() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cache == nil {
		return &Snapshot{Processes: []Process{}}
	}
	return s.cache
}

// Collect gathers a fresh view.
func (c *Collector) Collect(ctx context.Context) *Snapshot {
	start := time.Now()
	snap := &Snapshot{CollectedAt: start, Processes: []Process{}}

	conns, err := net.Connections("inet")
	if err != nil {
		snap.Error = err.Error()
		c.storeSnapshot(snap)
		return snap
	}
	grouped := groupConnections(conns)

	eval := c.store.NewEvaluator()

	for pid, conns := range grouped {
		p, err := c.buildProcess(ctx, pid, conns)
		if err != nil {
			p.PermissionDenied = true
			p.Error = err.Error()
		}
		p.Connections = conns
		for _, conn := range conns {
			if conn.IsListen {
				p.ListenCount++
			}
		}
		p.Self = int(pid) == os.Getpid()
		p.Decision = eval.DecideProcess(eval.MatchProcess(c.subject(p)))
		// The connection verdict also takes the port rules into account.
		for _, conn := range conns {
			pm := eval.MatchPort(conn.Port)
			pd := eval.DecideConnection(eval.MatchProcess(c.subject(p)), pm)
			if pd.IsSystem {
				p.PortDecision = pd
				break
			}
		}
		if p.Decision.Check == rules.CheckUnknown {
			p.SystemCheck = rules.CheckUnknown
		}
		snap.Processes = append(snap.Processes, p)
	}

	sort.Slice(snap.Processes, func(i, j int) bool {
		if snap.Processes[i].ListenCount != snap.Processes[j].ListenCount {
			return snap.Processes[i].ListenCount > snap.Processes[j].ListenCount
		}
		return snap.Processes[i].PID < snap.Processes[j].PID
	})

	// Statistics over the visible processes.
	for _, p := range snap.Processes {
		snap.Stats.Total++
		if p.ListenCount > 0 {
			snap.Stats.Listen++
		}
		if p.Decision.IsSystem {
			snap.Stats.System++
			snap.Stats.Hidden++
		}
		if p.Decision.Whitelisted {
			snap.Stats.Whitelist++
		}
	}
	snap.DurationMS = time.Since(start).Milliseconds()
	c.storeSnapshot(snap)
	return snap
}

func (c *Collector) storeSnapshot(s *Snapshot) {
	c.mu.Lock()
	c.cache = s
	c.mu.Unlock()
}

// subject builds the rule matching subject of a process.
func (c *Collector) subject(p Process) rules.Subject {
	sub := rules.Subject{PID: p.PID, PPID: p.PPID}
	sub.Name = strings.TrimSpace(p.Name)
	sub.NameKnown = sub.Name != ""
	sub.Exe = strings.TrimSpace(p.Exe)
	sub.ExeKnown = sub.Exe != ""
	sub.Username = strings.TrimSpace(p.Username)
	sub.UserKnown = sub.Username != ""
	return sub
}

// buildProcess reads the details of one process.
func (c *Collector) buildProcess(ctx context.Context, pid int32, conns []Connection) (Process, error) {
	p := Process{PID: pid}
	proc, err := process.NewProcess(pid)
	if err != nil {
		return p, err
	}
	if name, err := proc.Name(); err == nil {
		p.Name = name
	}
	if exe, err := proc.Exe(); err == nil {
		p.Exe = exe
	}
	if args, err := proc.CmdlineSlice(); err == nil {
		p.Cmdline = args
	}
	if cwd, err := proc.Cwd(); err == nil {
		p.Cwd = cwd
	}
	if user, err := proc.Username(); err == nil {
		p.Username = user
	}
	if ppid, err := proc.Ppid(); err == nil {
		p.PPID = ppid
	}
	if st, err := proc.Status(); err == nil && len(st) > 0 {
		p.State = st[0]
	}
	if ct, err := proc.CreateTime(); err == nil {
		p.CreateTime = ct
	}
	if mem, err := proc.MemoryInfo(); err == nil && mem != nil {
		p.Memory = mem.RSS
		p.VMS = mem.VMS
	}
	if t, err := proc.NumThreads(); err == nil {
		p.Threads = t
	}
	if fds, err := proc.NumFDs(); err == nil {
		p.FDs = fds
	}
	p.CPUPercent = c.cpuPercent(proc)
	return p, nil
}

// cpuPercent computes the CPU usage from the difference of the CPU times.
//
// The first sample has no baseline, so it reports zero; the counters are
// primed for the next round.
func (c *Collector) cpuPercent(proc *process.Process) float64 {
	times, err := proc.Times()
	if err != nil {
		return 0
	}
	pid := proc.Pid
	cur := cpuSample{user: times.User, system: times.System}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	prev, ok := c.cpuPrev[pid]
	if !ok {
		c.cpuPrev[pid] = cur
		c.prevAt = now
		return 0
	}
	elapsed := now.Sub(c.prevAt).Seconds()
	c.cpuPrev[pid] = cur
	c.prevAt = now
	if elapsed <= 0 {
		return 0
	}
	delta := (cur.user - prev.user) + (cur.system - prev.system)
	if delta < 0 {
		return 0
	}
	return delta / elapsed * 100
}

// Find returns the process with the given pid.
func Find(snap *Snapshot, pid int32) (Process, bool) {
	for _, p := range snap.Processes {
		if p.PID == pid {
			return p, true
		}
	}
	return Process{}, false
}

// FormatPort renders a port number.
func FormatPort(p uint32) string { return strconv.FormatUint(uint64(p), 10) }

// ErrProcessGone is returned when a process no longer exists.
var ErrProcessGone = errs.New(errs.KindProcessGone, "进程不存在或已经退出")
