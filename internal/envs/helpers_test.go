package envs

import (
	"os/exec"

	"mymac/internal/audit"
)

// execShell runs a shell snippet and returns its stdout.
func execShell(script string) (string, error) {
	cmd := exec.Command("/bin/sh", "-c", script)
	out, err := cmd.Output()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// auditFor returns a logger writing to path.
func auditFor(path string) *audit.Logger { return audit.New(path) }
