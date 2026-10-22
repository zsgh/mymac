// Package trash moves files to the macOS Trash.
//
// Nothing in mymac ever deletes a file permanently: everything goes through
// the Finder so the user can put it back. When the Finder is not reachable
// (no automation permission) the fallback moves the file into ~/.Trash
// directly, which is still recoverable.
package trash

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mymac/internal/errs"
)

// Result reports the outcome of a trash operation.
type Result struct {
	Moved  []string          `json:"moved"`
	Failed []FailedItem      `json:"failed"`
	Method map[string]string `json:"method"`
	Note   string            `json:"note,omitempty"`
}

// FailedItem is one path that could not be moved.
type FailedItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Kind   string `json:"kind"`
}

// finderUsable caches whether the Finder automation works.
//
// Without the cache every trash operation would pay the permission timeout.
var finderUsable = struct {
	sync.Mutex
	ok      bool
	checked time.Time
}{}

// usable reports whether the Finder can be driven right now.
//
// MYMAC_NO_FINDER turns the Finder off: the fallback moves the file into
// ~/.Trash, which is still recoverable. The tests use it so that a test run
// does not make macOS show a permission dialog.
func usable(ctx context.Context) bool {
	if os.Getenv("MYMAC_NO_FINDER") != "" {
		return false
	}
	finderUsable.Lock()
	defer finderUsable.Unlock()
	if time.Since(finderUsable.checked) < 2*time.Minute {
		return finderUsable.ok
	}
	finderUsable.ok = Check(ctx).Available
	finderUsable.checked = time.Now()
	return finderUsable.ok
}

// Move sends the given paths to the Trash.
//
// The paths are tried in one batch first; if the batch fails the items are
// retried one by one so a single bad path cannot block the rest.
func Move(ctx context.Context, paths []string) (*Result, error) {
	res := &Result{Moved: []string{}, Failed: []FailedItem{}, Method: map[string]string{}}
	if len(paths) == 0 {
		return res, nil
	}

	// Filter out paths that do not exist.
	var existing []string
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			res.Failed = append(res.Failed, FailedItem{Path: p, Reason: "文件不存在", Kind: string(errs.KindNotFound)})
			continue
		}
		existing = append(existing, p)
	}
	if len(existing) == 0 {
		return res, nil
	}

	// The Finder path is preferred because the user can restore the files
	// from the Trash. When it is not available the items still go to
	// ~/.Trash, which keeps them recoverable.
	if usable(ctx) {
		if batchErr := finderTrash(ctx, existing); batchErr == nil {
			for _, p := range existing {
				res.Moved = append(res.Moved, p)
				res.Method[p] = "finder"
			}
			return res, nil
		} else {
			res.Note = batchErr.Error()
		}
	} else {
		res.Note = "未获得 Finder 自动化权限，文件已直接移入 ~/.Trash（仍可恢复）"
	}

	for _, p := range existing {
		if moveErr := moveToTrashDir(p); moveErr == nil {
			res.Moved = append(res.Moved, p)
			res.Method[p] = "trashdir"
			continue
		} else {
			res.Failed = append(res.Failed, FailedItem{
				Path:   p,
				Reason: moveErr.Error(),
				Kind:   string(errs.Classify(moveErr)),
			})
		}
	}
	return res, nil
}

// finderTrash asks Finder to move the paths to the Trash.
//
// The timeout matters: the very first call makes macOS show the automation
// permission dialog, and the call blocks until the user answers it.
func finderTrash(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, finderTimeout)
	defer cancel()

	script := buildFinderScript(paths)
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return errs.New(errs.KindPermission,
				"Finder 没有响应：首次使用需要你在弹出的对话框中点击「好」，以允许 mymac 控制 Finder")
		}
		return classifyAppleScriptError(err, string(out))
	}
	return nil
}

// finderTimeout is how long a Finder call may take before it is considered
// blocked on the permission dialog.
const finderTimeout = 25 * time.Second

// buildFinderScript renders the AppleScript that trashes the paths.
func buildFinderScript(paths []string) string {
	if len(paths) == 1 {
		return fmt.Sprintf(`tell application "Finder" to delete POSIX file %s`, appleString(paths[0]))
	}
	items := make([]string, 0, len(paths))
	for _, p := range paths {
		items = append(items, "POSIX file "+appleString(p))
	}
	return fmt.Sprintf(`tell application "Finder" to delete {%s}`, strings.Join(items, ", "))
}

// appleString renders a Go string as an AppleScript string literal.
func appleString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// classifyAppleScriptError turns the common failures into clear messages.
func classifyAppleScriptError(err error, out string) error {
	switch {
	case strings.Contains(out, "-1743"):
		return errs.New(errs.KindPermission,
			"系统拒绝了 Finder 自动化请求（-1743）：请在「系统设置 → 隐私与安全性 → 自动化」中允许 mymac 控制 Finder")
	case strings.Contains(out, "-1728"):
		return errs.New(errs.KindPermission, "Finder 拒绝了删除请求（-1728）")
	case strings.Contains(out, "-600"):
		return errs.New(errs.KindPermission, "无法启动 Finder（-600）")
	case strings.Contains(out, "User canceled"):
		return errs.New(errs.KindPermission, "用户取消了操作")
	case strings.Contains(out, "not authorized"):
		return errs.New(errs.KindPermission, "没有权限访问该位置")
	}
	if out != "" {
		return errs.Wrap(err, errs.KindInternal, "Finder 移入废纸篓失败: %s", strings.TrimSpace(out))
	}
	return errs.Wrap(err, errs.KindInternal, "Finder 移入废纸篓失败")
}

// moveToTrashDir moves a path into ~/.Trash.
//
// This is the fallback for when the Finder automation is not available. The
// file stays recoverable, it just does not show up in the Finder's Trash
// stack until the Finder notices it.
func moveToTrashDir(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "无法确定用户目录")
	}
	trashDir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return errs.Wrap(err, errs.KindPermission, "无法创建废纸篓目录")
	}
	base := filepath.Base(path)
	dst := filepath.Join(trashDir, base)
	if _, err := os.Lstat(dst); err == nil {
		// A file with the same name is already there; add a suffix.
		dst = filepath.Join(trashDir, fmt.Sprintf("%s %s", base, time.Now().Format("2006-01-02 15.04.05")))
	}
	if err := os.Rename(path, dst); err != nil {
		return errs.Wrap(err, errs.KindPermission, "无法移入废纸篓")
	}
	return nil
}

// Status reports whether the Finder automation is usable.
type Status struct {
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
	Hint      string `json:"hint,omitempty"`
}

// Check reports whether mymac may drive the Finder.
//
// The first call makes macOS show the automation dialog, so a timeout here
// usually means the dialog is on screen waiting for an answer.
func Check(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e",
		`tell application "Finder" to return name of startup disk`)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return Status{Available: true, Detail: "可以控制 Finder"}
	}
	text := string(out)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return Status{
			Detail: "Finder 没有响应",
			Hint:   "首次使用需要你在弹出的对话框中点击「好」，允许 mymac 控制 Finder",
		}
	case strings.Contains(text, "-1743"):
		return Status{
			Detail: "系统拒绝了 Finder 自动化请求（-1743）",
			Hint:   "请在「系统设置 → 隐私与安全性 → 自动化」中允许 mymac 控制 Finder",
		}
	}
	return Status{Detail: strings.TrimSpace(text)}
}

// Available reports whether the Finder automation works.
func Available(ctx context.Context) bool {
	return Check(ctx).Available
}
