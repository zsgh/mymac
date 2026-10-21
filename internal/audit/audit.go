// Package audit appends a JSON Lines record of every mutating operation.
//
// The log lives at ~/.config/mymac/audit.log with mode 0600. Writes are
// append-only and flushed to disk so the trail survives a crash.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Action names used across the app.
const (
	ActionEnvSet     = "env.set"
	ActionEnvDelete  = "env.delete"
	ActionEnvImport  = "env.import"
	ActionEnvPath    = "env.path"
	ActionEnvRestore = "env.restore"
	ActionEnvApply   = "env.apply"
	ActionProcSignal = "proc.signal"
	ActionProcRenice = "proc.renice"
	ActionProcAlert  = "proc.alert"
	ActionAppUninst  = "app.uninstall"
	ActionCleanExec  = "clean.execute"
	ActionElevate    = "perm.elevate"
	ActionPermGrant  = "perm.grant"
)

// Result values.
const (
	ResultOK     = "ok"
	ResultDenied = "denied"
	ResultFailed = "failed"
)

// Entry is one audit record.
type Entry struct {
	TS      time.Time `json:"ts"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Detail  string    `json:"detail,omitempty"`
	Result  string    `json:"result"`
	PID     int       `json:"pid"`
	Elapsed int64     `json:"elapsedMs,omitempty"`
}

// Logger appends entries to the audit log.
type Logger struct {
	mu   sync.Mutex
	path string
}

// New returns a logger writing to path.
func New(path string) *Logger { return &Logger{path: path} }

// Log appends an entry. Errors are swallowed: auditing must never break the
// operation being audited.
func (l *Logger) Log(e Entry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	if e.PID == 0 {
		e.PID = os.Getpid()
	}
	if e.Result == "" {
		e.Result = ResultOK
	}

	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = f.Write(b)
	_ = f.Sync()
}

// Record is a convenience wrapper that fills in the timestamp and pid.
func (l *Logger) Record(action, target, detail, result string) {
	l.Log(Entry{Action: action, Target: target, Detail: detail, Result: result})
}

// Read returns the last n entries, oldest first.
func (l *Logger) Read(n int) ([]Entry, error) {
	b, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	lines := splitLines(b)
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]Entry, 0, len(lines))
	for _, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(ln, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
