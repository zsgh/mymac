// Package fsutil holds the filesystem helpers shared by the app uninstaller
// and the system cleaner.
package fsutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// AllocatedSize returns the space a file actually occupies on disk.
//
// The logical size is wrong for APFS: a cloned file reports the full size
// even though it shares the blocks, and a sparse file reports more than it
// uses. The allocated size is what the user gets back.
func AllocatedSize(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Blocks) * 512
	}
	return uint64(info.Size())
}

// DirSize returns the total allocated size of a directory tree.
//
// It shells out to `du -sk`, which counts 1024-byte blocks. That is the
// allocated size, the same number `du -sh` shows in the terminal, and it is
// an order of magnitude faster than walking the tree in Go: a few thousand
// files cost one process spawn instead of one lstat each.
func DirSize(path string) uint64 {
	size, _ := DirSizeErr(path)
	return size
}

// DirSizeErr is DirSize with the error, so the caller can report a
// directory that could not be read instead of silently reporting zero.
func DirSizeErr(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return AllocatedSize(info), nil
	}
	out, err := exec.Command("/usr/bin/du", "-sk", path).Output()
	if err != nil {
		// du prints the readable part to stdout and an error to stderr, so
		// a partial result is still usable.
		if len(out) == 0 {
			return 0, err
		}
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, nil
	}
	kb, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return kb * 1024, nil
}

// DirChildrenSizes returns the allocated size of every direct child of a
// directory.
//
// One `du -k -d 1` call measures the whole folder, instead of one call per
// child. On a machine with a few thousand library entries that is the
// difference between half a second and half a minute.
func DirChildrenSizes(dir string) (map[string]uint64, error) {
	out := map[string]uint64{}
	cmd := exec.Command("/usr/bin/du", "-k", "-d", "1", dir)
	raw, err := cmd.Output()
	if len(raw) == 0 {
		if err != nil {
			return out, err
		}
		return out, nil
	}
	abs, _ := filepath.Abs(dir)
	for _, line := range strings.Split(string(raw), "\n") {
		kb, path, ok := strings.Cut(strings.TrimRight(line, "\n"), "\t")
		if !ok {
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		// The last line is the total for the directory itself.
		if p, _ := filepath.Abs(path); p == abs {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(kb), 10, 64)
		if err != nil {
			continue
		}
		out[path] = n * 1024
	}
	return out, nil
}

// Exists reports whether a path exists.
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// FileStat is the size and the modification time of a file.
type FileStat struct {
	Size uint64
	Mod  int64
}

// StatPath reads the size and the modification time of a file.
func StatPath(path string) (FileStat, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileStat{}, err
	}
	return FileStat{Size: AllocatedSize(info), Mod: info.ModTime().Unix()}, nil
}

// LinkCount returns the number of hard links to a file.
//
// A file with more than one link exists under several names but is a single
// inode: deleting one of the names frees no space.
func LinkCount(path string) int {
	info, err := os.Stat(path)
	if err != nil {
		return 1
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Nlink)
	}
	return 1
}

// SameInode reports whether two paths are the same file.
func SameInode(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	sa, ok1 := ia.Sys().(*syscall.Stat_t)
	sb, ok2 := ib.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	return sa.Ino == sb.Ino && sa.Dev == sb.Dev
}

// IsDir reports whether a path is a directory.
func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ReadableDir lists a directory, reporting whether the read failed because
// of a missing Full Disk Access grant.
func ReadableDir(path string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// ExeSize returns the size of the main executable of an app bundle.
func ExeSize(appPath string) uint64 {
	exe, err := MainExecutable(appPath)
	if err != nil {
		return 0
	}
	info, err := os.Stat(exe)
	if err != nil {
		return 0
	}
	return AllocatedSize(info)
}

// MainExecutable returns the path of the main executable of an app bundle.
//
// The name comes from CFBundleExecutable; when that is missing the only
// executable in Contents/MacOS is used.
func MainExecutable(appPath string) (string, error) {
	macOS := filepath.Join(appPath, "Contents", "MacOS")
	entries, err := os.ReadDir(macOS)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		return filepath.Join(macOS, e.Name()), nil
	}
	return "", os.ErrNotExist
}
