package cleaner

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mymac/internal/config"
	"mymac/internal/errs"
	"mymac/internal/fsutil"
)

// Job kinds.
const (
	JobLargeFiles = "large"
	JobDuplicates = "duplicates"
)

// Job states.
const (
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// Job is a long running scan.
type Job struct {
	ID        string           `json:"id"`
	Kind      string           `json:"kind"`
	State     string           `json:"state"`
	Message   string           `json:"message,omitempty"`
	Scanned   int              `json:"scanned"`
	Found     int              `json:"found"`
	MinSize   uint64           `json:"minSize"`
	Files     []LargeFile      `json:"files,omitempty"`
	Groups    []DuplicateGroup `json:"groups,omitempty"`
	Error     string           `json:"error,omitempty"`
	StartedAt time.Time        `json:"startedAt"`
	ElapsedMS int64            `json:"elapsedMs"`
}

// JobManager runs the scans in the background and keeps their state.
//
// A full scan of the home directory takes tens of seconds, so the UI starts a
// job and polls it instead of waiting for a single request.
//
// The jobs run in the manager's own context: the context of the request that
// started them is cancelled as soon as the request ends.
type JobManager struct {
	mu   sync.Mutex
	jobs map[string]*Job
	seq  int
	ctx  context.Context
}

// NewJobManager returns a job manager.
func NewJobManager() *JobManager {
	return &JobManager{jobs: map[string]*Job{}, ctx: context.Background()}
}

// Start begins a scan of the given kind.
//
// A job of the same kind that is still running is returned instead of
// starting a second one.
func (m *JobManager) Start(ctx context.Context, kind string, minSize uint64) *Job {
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.Kind == kind && j.State == JobRunning {
			m.mu.Unlock()
			return j
		}
	}
	m.seq++
	job := &Job{
		ID:        fmt.Sprintf("%s-%d", kind, m.seq),
		Kind:      kind,
		State:     JobRunning,
		MinSize:   minSize,
		StartedAt: time.Now(),
	}
	m.jobs[job.ID] = job
	m.mu.Unlock()

	// The job must survive the request that started it.
	go m.run(m.ctx, job)
	return job
}

// Get returns a job by id.
func (m *JobManager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// Latest returns the most recent job of a kind.
func (m *JobManager) Latest(kind string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Job
	for _, j := range m.jobs {
		if j.Kind != kind {
			continue
		}
		if best == nil || j.StartedAt.After(best.StartedAt) {
			best = j
		}
	}
	return best
}

// run executes the scan of the job.
func (m *JobManager) run(ctx context.Context, job *Job) {
	defer func() {
		job.ElapsedMS = time.Since(job.StartedAt).Milliseconds()
	}()

	switch job.Kind {
	case JobLargeFiles:
		err := m.scanLargeFiles(ctx, job)
		if err != nil {
			job.State = JobFailed
			job.Error = err.Error()
			return
		}
	case JobDuplicates:
		err := m.scanDuplicates(ctx, job)
		if err != nil {
			job.State = JobFailed
			job.Error = err.Error()
			return
		}
	default:
		job.State = JobFailed
		job.Error = "未知的扫描类型: " + job.Kind
		return
	}
	job.State = JobDone
}

// update writes the progress of a job.
func (m *JobManager) update(job *Job, fn func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(job)
}

// scanLargeFiles finds the files above the threshold.
//
// The Spotlight index answers immediately, and a full walk follows in the
// background so the user sees results right away and the complete list a
// little later.
func (m *JobManager) scanLargeFiles(ctx context.Context, job *Job) error {
	seen := map[string]bool{}
	add := func(path string, size uint64, mod int64) {
		if seen[path] {
			return
		}
		seen[path] = true
		m.update(job, func(j *Job) {
			j.Files = append(j.Files, LargeFile{Path: path, Size: size, Mod: mod})
			j.Found = len(j.Files)
		})
	}

	// Phase one: the Spotlight index, which is instant.
	m.update(job, func(j *Job) { j.Message = "正在查询 Spotlight 索引…" })
	for _, path := range m.mdfindLarge(ctx, job.MinSize) {
		info, err := statFile(path)
		if err != nil {
			continue
		}
		add(path, info.size, info.mod)
	}

	// Phase two: the full walk.
	m.update(job, func(j *Job) { j.Message = "正在遍历主目录…" })
	paths, err := m.findLarge(ctx, job.MinSize)
	if err != nil {
		return err
	}
	m.update(job, func(j *Job) { j.Message = "正在统计大小…" })
	for _, path := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		info, err := statFile(path)
		if err != nil {
			continue
		}
		add(path, info.size, info.mod)
		m.update(job, func(j *Job) { j.Scanned++ })
	}

	m.update(job, func(j *Job) {
		sort.Slice(j.Files, func(a, b int) bool { return j.Files[a].Size > j.Files[b].Size })
		j.Message = fmt.Sprintf("找到 %d 个大文件", len(j.Files))
	})
	return nil
}

// scanDuplicates finds the identical files.
func (m *JobManager) scanDuplicates(ctx context.Context, job *Job) error {
	m.update(job, func(j *Job) { j.Message = "正在遍历主目录…" })
	paths, err := m.findLarge(ctx, job.MinSize)
	if err != nil {
		return err
	}
	m.update(job, func(j *Job) { j.Message = "正在计算哈希…" })

	groups, err := FindDuplicatesIn(ctx, paths, job.MinSize, func(scanned, total int) {
		m.update(job, func(j *Job) {
			j.Scanned = scanned
			j.Message = fmt.Sprintf("正在计算哈希… %d/%d", scanned, total)
		})
	})
	if err != nil {
		return err
	}
	m.update(job, func(j *Job) {
		j.Groups = groups
		j.Message = fmt.Sprintf("找到 %d 组重复文件", len(groups))
	})
	return nil
}

// fileInfo is the size and the modification time of a file.
type fileInfo struct {
	size uint64
	mod  int64
}

// statFile reads the size and the modification time of a file.
func statFile(path string) (fileInfo, error) {
	st, err := fsutil.StatPath(path)
	if err != nil {
		return fileInfo{}, err
	}
	return fileInfo{size: st.Size, mod: st.Mod}, nil
}

// mdfindLarge asks the Spotlight index for the big files.
//
// The index is incomplete on purpose: folders like the caches are excluded
// from it. It is used for the instant first result, not as the only source.
func (m *JobManager) mdfindLarge(ctx context.Context, minSize uint64) []string {
	out, err := exec.CommandContext(ctx, "/usr/bin/mdfind", "-onlyin", config.Home(),
		fmt.Sprintf("kMDItemFSSize > %d", minSize)).Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// findLarge walks the home directory and returns the files above the
// threshold.
//
// The walk is done by find, a tight C loop: the same walk in Go takes four
// times as long, because every entry needs a stat call.
func (m *JobManager) findLarge(ctx context.Context, minSize uint64) ([]string, error) {
	mb := minSize / (1024 * 1024)
	if mb < 1 {
		mb = 1
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/find", config.Home(),
		"-type", "f",
		"-size", "+"+strconv.FormatInt(int64(mb), 10)+"M",
		"-not", "-path", "*/.Trash/*",
		"-not", "-path", "*/.git/*",
		"-not", "-path", "*/node_modules/*",
		"-print")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "无法启动 find")
	}
	if err := cmd.Start(); err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "无法启动 find")
	}
	var paths []string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}
	if err := cmd.Wait(); err != nil {
		// find exits non-zero when it cannot read a folder; the paths it
		// found are still useful.
		if len(paths) == 0 {
			return nil, errs.Wrap(err, errs.KindInternal, "文件扫描失败")
		}
	}
	return paths, nil
}
