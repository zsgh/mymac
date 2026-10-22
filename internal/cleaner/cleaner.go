// Package cleaner implements the system cleanup: user caches, developer
// caches, large files and duplicates.
//
// Nothing is ever deleted without the user selecting it, and the deletion
// itself always goes to the Trash.
package cleaner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mymac/internal/ai"
	"mymac/internal/config"
	"mymac/internal/fsutil"
)

// Item is one cleanable thing.
type Item struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Size     uint64 `json:"size"`
	Selected bool   `json:"selected"`
	Note     string `json:"note,omitempty"`
	Count    int    `json:"count,omitempty"`
	// Readable is false when the folder could not be listed, for example
	// because of a missing Full Disk Access grant.
	Readable bool `json:"readable"`
	// ReadOnly items are only reported, never deleted.
	ReadOnly bool `json:"readOnly"`
	// AI is what the model said about the item, if it was asked.
	AI *ai.Verdict `json:"ai,omitempty"`
}

// Summary is the overview of a scan.
type Summary struct {
	UserCaches uint64   `json:"userCaches"`
	UserLogs   uint64   `json:"userLogs"`
	DevCaches  uint64   `json:"devCaches"`
	Total      uint64   `json:"total"`
	Count      int      `json:"count"`
	Warnings   []string `json:"warnings"`
}

// ScanResult is the full result of a scan.
type ScanResult struct {
	Items     []Item    `json:"items"`
	Summary   Summary   `json:"summary"`
	ScannedAt time.Time `json:"scannedAt"`
}

// Scanner finds the cleanable files.
type Scanner struct {
	mu      sync.Mutex
	cache   *ScanResult
	cacheAt time.Time
}

// NewScanner returns a cleaner scanner.
func NewScanner() *Scanner { return &Scanner{} }

// Scan looks for cleanable files.
//
// The result is cached briefly; a rescan always refreshes it.
func (s *Scanner) Scan(ctx context.Context, rescan bool) *ScanResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !rescan && s.cache != nil && time.Since(s.cacheAt) < 60*time.Second {
		return s.cache
	}

	res := &ScanResult{Items: []Item{}, ScannedAt: time.Now()}
	home := config.Home()

	// User caches and logs.
	res.Items = append(res.Items, s.scanDir(filepath.Join(home, "Library", "Caches"), "userCache", "用户缓存")...)
	res.Items = append(res.Items, s.scanDir(filepath.Join(home, "Library", "Logs"), "userLog", "用户日志")...)

	// Developer caches.
	res.Items = append(res.Items, s.scanDevCaches(home)...)

	// A developer cache that lives inside ~/Library/Caches is already
	// covered by the user cache scan, so it must not be listed twice.
	res.Items = dedupeByPath(res.Items)

	// Read only items: Time Machine snapshots and update leftovers.
	res.Items = append(res.Items, s.scanReadOnly(ctx)...)

	// The config directory is never touched.
	res.Items = filterProtected(res.Items)

	// Summary.
	for _, it := range res.Items {
		if it.ReadOnly {
			continue
		}
		res.Summary.Total += it.Size
		res.Summary.Count++
		switch it.Category {
		case "userCache":
			res.Summary.UserCaches += it.Size
		case "userLog":
			res.Summary.UserLogs += it.Size
		case "devCache":
			res.Summary.DevCaches += it.Size
		}
	}
	sort.Slice(res.Items, func(i, j int) bool {
		if res.Items[i].ReadOnly != res.Items[j].ReadOnly {
			return !res.Items[i].ReadOnly
		}
		return res.Items[i].Size > res.Items[j].Size
	})

	s.cache = res
	s.cacheAt = time.Now()
	return res
}

// scanDir lists the children of a directory and measures them.
func (s *Scanner) scanDir(dir, category, note string) []Item {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A folder that cannot be read is reported, never silently
		// skipped: the user needs to know why nothing was found.
		return []Item{{
			Path:     dir,
			Name:     filepath.Base(dir),
			Category: category,
			Note:     "无法读取该目录（可能需要完全磁盘访问权限）：" + err.Error(),
			Readable: false,
		}}
	}
	var out []Item
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		size := uint64(0)
		if e.IsDir() {
			size = fsutil.DirSize(full)
		} else if info, err := e.Info(); err == nil {
			size = fsutil.AllocatedSize(info)
		}
		out = append(out, Item{
			Path:     full,
			Name:     e.Name(),
			Category: category,
			Size:     size,
			Selected: size > 0,
			Note:     note,
			Readable: true,
		})
	}
	return out
}

// devCache describes one developer cache location.
type devCache struct {
	path     string
	name     string
	category string
	note     string
	readOnly bool
}

// devCaches lists the developer caches mymac knows about.
func devCaches(home string) []devCache {
	return []devCache{
		{filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData"), "Xcode DerivedData", "devCache", "编译产物，可安全删除", false},
		{filepath.Join(home, "Library", "Developer", "Xcode", "Archives"), "Xcode Archives", "devCache", "已归档的 App，删除后无法导出 dSYM", false},
		{filepath.Join(home, "Library", "Developer", "Xcode", "iOS DeviceSupport"), "iOS DeviceSupport", "devCache", "旧设备符号，重新连接设备时会下载", false},
		{filepath.Join(home, "Library", "Developer", "CoreSimulator"), "CoreSimulator", "devCache", "模拟器数据", false},
		{filepath.Join(home, "Library", "Caches", "go-build"), "Go 编译缓存", "devCache", "", false},
		{filepath.Join(home, "go", "pkg", "mod"), "Go 模块缓存", "devCache", "删除后需要重新下载依赖", false},
		{filepath.Join(home, "Library", "Caches", "CocoaPods"), "CocoaPods 缓存", "devCache", "", false},
		{filepath.Join(home, "Library", "Caches", "Homebrew"), "Homebrew 缓存", "devCache", "", false},
		{filepath.Join(home, ".npm", "_cacache"), "npm 缓存", "devCache", "", false},
		{filepath.Join(home, "Library", "pnpm", "store"), "pnpm store", "devCache", "", false},
		{filepath.Join(home, ".cache", "pip"), "pip 缓存", "devCache", "", false},
		{filepath.Join(home, ".gradle", "caches"), "Gradle 缓存", "devCache", "", false},
		{filepath.Join(home, ".m2", "repository"), "Maven 仓库", "devCache", "删除后需要重新下载依赖", false},
		{filepath.Join(home, "Library", "Caches", "Yarn"), "Yarn 缓存", "devCache", "", false},
		{filepath.Join(home, "Library", "Containers", "com.docker.docker", "Data", "vms", "0", "data"), "Docker 虚拟磁盘", "devCache", "Docker 数据较大，删除会清空所有镜像和容器", true},
	}
}

// scanDevCaches measures the known developer cache locations.
func (s *Scanner) scanDevCaches(home string) []Item {
	var out []Item
	for _, dc := range devCaches(home) {
		if !fsutil.Exists(dc.path) {
			continue
		}
		size := fsutil.DirSize(dc.path)
		count := 0
		if entries, err := os.ReadDir(dc.path); err == nil {
			count = len(entries)
		}
		out = append(out, Item{
			Path:     dc.path,
			Name:     dc.name,
			Category: dc.category,
			Size:     size,
			Selected: size > 0 && !dc.readOnly,
			Note:     dc.note,
			Count:    count,
			Readable: true,
			ReadOnly: dc.readOnly,
		})
	}
	return out
}

// scanReadOnly reports the things mymac shows but never deletes.
//
// The two probes are slow, so they run in parallel and their timeouts never
// add up.
func (s *Scanner) scanReadOnly(ctx context.Context) []Item {
	var out []Item
	var wg sync.WaitGroup
	var snapshots []string
	var pending string
	var snapErr, updErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		snapshots, snapErr = localSnapshots(ctx)
	}()
	go func() {
		defer wg.Done()
		pending, updErr = pendingUpdates(ctx)
	}()
	wg.Wait()

	// Local Time Machine snapshots.
	if snapErr == nil && len(snapshots) > 0 {
		out = append(out, Item{
			Path:     "/",
			Name:     fmt.Sprintf("本地 Time Machine 快照 (%d 个)", len(snapshots)),
			Category: "snapshot",
			Size:     0,
			Note:     "系统管理，仅供查看：" + strings.Join(snapshots, ", "),
			Readable: true,
			ReadOnly: true,
		})
	}
	// Pending system updates.
	if updErr == nil && pending != "" {
		out = append(out, Item{
			Path:     "/Library/Updates",
			Name:     "待安装的系统更新",
			Category: "update",
			Size:     0,
			Note:     pending,
			Readable: true,
			ReadOnly: true,
		})
	}
	return out
}

// localSnapshots lists the local Time Machine snapshots.
func localSnapshots(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/tmutil", "listlocalsnapshots", "/")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var snaps []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "com.apple.TimeMachine") {
			snaps = append(snaps, strings.TrimPrefix(line, "com.apple.TimeMachine."))
		}
	}
	return snaps, nil
}

// pendingUpdates reports the pending macOS updates.
//
// The softwareupdate tool is slow, so it gets a short timeout: this is a
// read-only extra, never a reason to hold up the scan.
func pendingUpdates(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/softwareupdate", "-l")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" || strings.Contains(text, "No new software available") ||
		strings.Contains(text, "Software Update Tool found no new or applicable updates") {
		return "", nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 6 {
		lines = lines[:6]
	}
	return strings.Join(lines, "; "), nil
}

// dedupeByPath removes the entries whose path was already listed.
//
// The user cache scan and the developer cache list overlap: the Go build
// cache for example lives in ~/Library/Caches and is covered by both.
func dedupeByPath(items []Item) []Item {
	seen := map[string]bool{}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if seen[it.Path] {
			continue
		}
		seen[it.Path] = true
		out = append(out, it)
	}
	return out
}

// filterProtected drops anything inside the mymac config directory.
func filterProtected(items []Item) []Item {
	out := items[:0]
	for _, it := range items {
		if config.IsProtected(it.Path) {
			continue
		}
		out = append(out, it)
	}
	return out
}

// ----------------------------------------------------------------- large files

// LargeFile is a file bigger than the threshold.
type LargeFile struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
	Mod  int64  `json:"mod"`
}

// FindLargeFiles walks the given roots and returns the files above the
// threshold.
func FindLargeFiles(ctx context.Context, roots []string, minSize uint64, limit int) ([]LargeFile, error) {
	var out []LargeFile
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				name := d.Name()
				// Skip the directories that would only slow the walk down.
				if name == ".Trash" || name == "node_modules" || name == ".git" {
					return filepath.SkipDir
				}
				if config.IsProtected(path) {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			size := fsutil.AllocatedSize(info)
			if size < minSize {
				return nil
			}
			out = append(out, LargeFile{Path: path, Size: size, Mod: info.ModTime().Unix()})
			if limit > 0 && len(out) >= limit {
				return filepath.SkipAll
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out, nil
}

// ---------------------------------------------------------------- duplicates

// DuplicateGroup is a set of identical files.
type DuplicateGroup struct {
	Size  uint64   `json:"size"`
	Hash  string   `json:"hash"`
	Paths []string `json:"paths"`
	// Links is the hard link count of each path.
	//
	// A file with more than one link is the same inode under several
	// names: removing one of them frees no space at all, and the name may
	// be referenced from somewhere else.
	Links map[string]int `json:"links,omitempty"`
	// Shared is true when the group is a set of APFS clones: the blocks
	// are shared, so removing one of them frees nothing either.
	Shared bool `json:"shared,omitempty"`
}

// FindDuplicates looks for identical files.
//
// The search goes in three steps, cheapest first: the file size, then a
// partial hash of the first and last 4 KiB, then the full SHA-256. Only the
// files that survive all three steps are reported.
func FindDuplicates(ctx context.Context, roots []string, minSize uint64) ([]DuplicateGroup, error) {
	var paths []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".Trash" || name == "node_modules" || name == ".git" {
					return filepath.SkipDir
				}
				if config.IsProtected(path) {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			size := fsutil.AllocatedSize(info)
			if size < minSize {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
	}
	return FindDuplicatesIn(ctx, paths, minSize, nil)
}

// FindDuplicatesIn looks for identical files among the given paths.
//
// The progress callback is called with the number of files hashed so far and
// the total.
func FindDuplicatesIn(ctx context.Context, paths []string, minSize uint64, progress func(scanned, total int)) ([]DuplicateGroup, error) {
	bySize := map[uint64][]string{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		size := fsutil.AllocatedSize(info)
		if size < minSize {
			continue
		}
		bySize[size] = append(bySize[size], path)
	}

	total := 0
	for _, group := range bySize {
		if len(group) > 1 {
			total += len(group)
		}
	}
	scanned := 0

	var groups []DuplicateGroup
	for size, group := range bySize {
		if len(group) < 2 {
			continue
		}
		// Step two: partial hash.
		byPartial := map[string][]string{}
		for _, p := range group {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			h, err := partialHash(p)
			if err != nil {
				continue
			}
			byPartial[h] = append(byPartial[h], p)
			scanned++
			if progress != nil {
				progress(scanned, total)
			}
		}
		for _, candidates := range byPartial {
			if len(candidates) < 2 {
				continue
			}
			// Step three: full hash.
			byFull := map[string][]string{}
			for _, p := range candidates {
				h, err := fullHash(p)
				if err != nil {
					continue
				}
				byFull[h] = append(byFull[h], p)
			}
			for h, dup := range byFull {
				if len(dup) < 2 {
					continue
				}
				sort.Strings(dup)
				links := map[string]int{}
				for _, p := range dup {
					links[p] = fsutil.LinkCount(p)
				}
				groups = append(groups, DuplicateGroup{
					Size:  size,
					Hash:  h,
					Paths: dup,
					Links: links,
				})
			}
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Size > groups[j].Size })
	return groups, nil
}

// partialHash hashes the first and last 4 KiB of a file.
func partialHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 4096)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	h.Write(buf[:n])

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > 4096 {
		if _, err := f.Seek(-4096, io.SeekEnd); err == nil {
			n, err := io.ReadFull(f, buf)
			if err == nil {
				h.Write(buf[:n])
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fullHash hashes the whole file.
func fullHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
