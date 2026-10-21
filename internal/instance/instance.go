// Package instance keeps a single mymac process alive per user.
//
// The lock is an advisory file lock held for the lifetime of the process. The
// kernel drops it when the process dies, so a crashed instance never leaves a
// stale lock behind and the next start just works.
package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrRunning reports that another mymac already owns the lock.
var ErrRunning = errors.New("mymac 已在运行")

// Info describes the process that owns the lock. It is written to the lock
// file so a second instance can tell the user where the running one lives.
type Info struct {
	PID  int    `json:"pid"`
	Port int    `json:"port"`
	Addr string `json:"addr"`
}

// Lock is the held lock. Release it when the process is done.
type Lock struct {
	file *os.File
	path string
}

// Acquire takes the exclusive lock stored at path.
//
// The lock file is created when it is missing. A second instance gets
// ErrRunning instead of a lock, so the caller can point the user at the
// address of the running one.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件 %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrRunning
		}
		return nil, fmt.Errorf("加锁 %s: %w", path, err)
	}
	return &Lock{file: f, path: path}, nil
}

// WriteInfo records who owns the lock. The content is informational: the
// lock itself is the kernel state, not the file.
func (l *Lock) WriteInfo(info Info) error {
	if l == nil || l.file == nil {
		return errors.New("锁未持有")
	}
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.WriteAt(b, 0); err != nil {
		return err
	}
	return l.file.Sync()
}

// Release drops the lock. Calling it twice is harmless.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}

// ReadInfo returns what the owner of the lock recorded. It returns an error
// when the file is missing or unreadable; the caller treats that as "the
// running instance did not say where it is".
func ReadInfo(path string) (Info, error) {
	var info Info
	b, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	return info, nil
}
