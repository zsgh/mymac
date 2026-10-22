// Package sysinfo gathers the system wide numbers shown in the overview
// cards: CPU load, memory pressure, disk usage, network throughput and
// battery state.
package sysinfo

import (
	"context"
	"mymac/internal/ai"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Overview is the system summary.
type Overview struct {
	CPUPercent  float64 `json:"cpuPercent"`
	CPUCount    int     `json:"cpuCount"`
	MemTotal    uint64  `json:"memTotal"`
	MemUsed     uint64  `json:"memUsed"`
	MemPercent  float64 `json:"memPercent"`
	MemPressure string  `json:"memPressure"`
	DiskTotal   uint64  `json:"diskTotal"`
	DiskFree    uint64  `json:"diskFree"`
	DiskUsed    uint64  `json:"diskUsed"`
	DiskPercent float64 `json:"diskPercent"`
	NetIn       uint64  `json:"netIn"`
	NetOut      uint64  `json:"netOut"`
	Battery     *int    `json:"battery"`
	Charging    bool    `json:"charging"`
	OnBattery   bool    `json:"onBattery"`
	CycleCount  int     `json:"cycleCount"`
	Uptime      string  `json:"uptime"`
	LoadAvg     string  `json:"loadAvg"`
	ProcCount   int     `json:"procCount"`
	PortCount   int     `json:"portCount"`
	// AI is what the model said about the state of the machine, if it
	// was asked.
	AI *ai.Verdict `json:"ai,omitempty"`
}

// Collector gathers the overview.
type Collector struct {
	prevCPU     uint64
	prevTime    time.Time
	prevNetIn   uint64
	prevNetOut  uint64
	haveCPU     bool
	haveNet     bool
	memPressure string
}

// NewCollector returns an overview collector.
func NewCollector() *Collector { return &Collector{} }

// Collect gathers a fresh overview.
func (c *Collector) Collect(ctx context.Context) *Overview {
	o := &Overview{}
	o.CPUCount = cpuCount()
	o.CPUPercent = c.cpuPercent()
	o.MemTotal, o.MemUsed = memory()
	if o.MemTotal > 0 {
		o.MemPercent = float64(o.MemUsed) / float64(o.MemTotal) * 100
	}
	o.MemPressure = c.memoryPressure(ctx)
	o.DiskTotal, o.DiskFree = disk("/")
	if o.DiskTotal > 0 {
		o.DiskUsed = o.DiskTotal - o.DiskFree
		o.DiskPercent = float64(o.DiskUsed) / float64(o.DiskTotal) * 100
	}
	c.netRates(ctx, o)
	c.battery(o)
	o.Uptime = uptime()
	o.LoadAvg = loadAvg()
	return o
}

// cpuCount returns the number of CPU cores.
func cpuCount() int {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.ncpu").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

// cpuPercent computes the CPU load from the host ticks.
//
// The first sample has no baseline, so it reports zero; the collector keeps
// the previous sample to compute a delta.
func (c *Collector) cpuPercent() float64 {
	var stat syscall.Statfs_t
	_ = stat
	ids, err := cpuTicks()
	if err != nil {
		return 0
	}
	now := time.Now()
	if !c.haveCPU {
		c.prevCPU, c.prevTime, c.haveCPU = ids, now, true
		return 0
	}
	delta := float64(ids - c.prevCPU)
	elapsed := now.Sub(c.prevTime).Seconds()
	c.prevCPU, c.prevTime = ids, now
	if delta <= 0 || elapsed <= 0 {
		return 0
	}
	// machdep.cpu.ticks.user + system + idle; idle is not exposed in the
	// same struct, so the load is derived from the user+system share.
	return clampPercent(delta / (elapsed * 100.0))
}

// cpuTicks reads the cumulative CPU tick counters.
func cpuTicks() (uint64, error) {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "kern.cp_time").Output()
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(out))
	var total uint64
	for _, f := range fields {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		total += n
	}
	return total, nil
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// memory returns the total and used physical memory.
//
// The vm_stat output looks like:
//
//	Mach Virtual Memory Statistics: (page size of 16384 bytes)
//	Pages free:                              4097.
//	Pages active:                           182252.
//	Pages inactive:                         180702.
//	Pages wired down:                       209308.
//	Pages occupied by compressor:            12345.
func memory() (uint64, uint64) {
	out, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return 0, 0
	}
	pageSize := uint64(16384)
	if idx := strings.Index(string(out), "page size of "); idx >= 0 {
		rest := string(out)[idx+len("page size of "):]
		if end := strings.Index(rest, " "); end > 0 {
			if n, err := strconv.ParseUint(rest[:end], 10, 64); err == nil {
				pageSize = n
			}
		}
	}

	pages := map[string]uint64{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "Pages ") {
			continue
		}
		rest := line[len("Pages "):]
		colon := strings.Index(rest, ":")
		if colon < 0 {
			continue
		}
		label := strings.TrimSpace(rest[:colon])
		value := strings.TrimSpace(rest[colon+1:])
		// The value carries a trailing dot.
		value = strings.TrimSuffix(value, ".")
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			continue
		}
		pages[label] = n
	}

	used := (pages["active"] + pages["inactive"] + pages["wired down"] + pages["occupied by compressor"]) * pageSize
	return totalMemory(), used
}

// totalMemory returns the physical memory size.
func totalMemory() uint64 {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// memoryPressure reads the pressure level reported by macOS.
func (c *Collector) memoryPressure(ctx context.Context) string {
	if c.memPressure != "" {
		return c.memPressure
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/memory_pressure").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "System-wide memory free percentage") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				c.memPressure = fields[len(fields)-1]
			}
		}
	}
	if c.memPressure == "" {
		c.memPressure = "—"
	}
	return c.memPressure
}

// disk returns the total and free space of a mount point.
func disk(path string) (uint64, uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bavail * bsize
}

// netRates computes the network throughput since the previous sample.
func (c *Collector) netRates(ctx context.Context, o *Overview) {
	in, out, err := netBytes(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	if !c.haveNet {
		c.prevNetIn, c.prevNetOut, c.prevTime, c.haveNet = in, out, now, true
		return
	}
	elapsed := now.Sub(c.prevTime).Seconds()
	if elapsed <= 0 {
		return
	}
	if in >= c.prevNetIn {
		o.NetIn = uint64(float64(in-c.prevNetIn) / elapsed)
	}
	if out >= c.prevNetOut {
		o.NetOut = uint64(float64(out-c.prevNetOut) / elapsed)
	}
	c.prevNetIn, c.prevNetOut, c.prevTime = in, out, now
}

// netBytes returns the cumulative received and sent bytes of all interfaces.
func netBytes(ctx context.Context) (uint64, uint64, error) {
	out, err := exec.CommandContext(ctx, "/usr/sbin/netstat", "-ibn").Output()
	if err != nil {
		return 0, 0, err
	}
	var received, sent uint64
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.HasPrefix(line, "Name:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		// The interface block repeats with the Mtu column; the second
		// occurrence of the header separates the blocks.
		ri, err := strconv.ParseUint(fields[6], 10, 64)
		if err != nil {
			continue
		}
		si, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		received += ri
		sent += si
	}
	return received, sent, nil
}

// battery reads the battery state.
func (c *Collector) battery(o *Overview) {
	out, err := exec.Command("/usr/sbin/ioreg", "-rn", "AppleSmartBattery").Output()
	if err != nil {
		return
	}
	text := string(out)
	cur := ioregInt(text, "CurrentCapacity")
	max := ioregInt(text, "MaxCapacity")
	if max > 0 {
		pct := int(float64(cur) / float64(max) * 100)
		o.Battery = &pct
	}
	o.Charging = strings.Contains(text, "\"IsCharging\" = Yes")
	o.OnBattery = strings.Contains(text, "\"ExternalConnected\" = No")
	o.CycleCount = ioregInt(text, "CycleCount")
}

// ioregInt reads a numeric property from an ioreg dump.
func ioregInt(text, key string) int {
	needle := "\"" + key + "\" = "
	idx := strings.Index(text, needle)
	if idx < 0 {
		return 0
	}
	rest := text[idx+len(needle):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// uptime returns the system uptime.
func uptime() string {
	out, err := exec.Command("/usr/bin/uptime").Output()
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	if idx := strings.Index(text, "up "); idx >= 0 {
		rest := text[idx+3:]
		if comma := strings.Index(rest, ","); comma > 0 {
			return rest[:comma]
		}
	}
	return text
}

// loadAvg returns the load average.
func loadAvg() string {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return ""
	}
	// The output looks like "{ 5.47 3.74 3.63 }".
	text := strings.TrimSpace(string(out))
	text = strings.TrimPrefix(text, "{")
	text = strings.TrimSuffix(text, "}")
	return strings.Join(strings.Fields(text), " / ")
}
