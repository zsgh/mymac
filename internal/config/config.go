// Package config resolves mymac's filesystem layout and shared constants.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Version is stamped into the binary and surfaced on /api/health.
const Version = "0.1.0"

// AppName is used for window titles, launch agent labels and log lines.
const AppName = "mymac"

// DefaultPort is the TCP port the web interface listens on.
//
// The port is fixed so the address can be typed by hand and so a second
// instance fails loudly instead of quietly taking another port.
const DefaultPort = 8888

// LaunchAgentLabel is the reverse-DNS label of the login agent that replays
// the managed environment into the launchd GUI domain.
const LaunchAgentLabel = "com.mymac.env"

// Subdirectories created under the config root.
const (
	dirBackup = "backup"
	dirIcons  = "icons"
)

// File names inside the config root.
const (
	fileEnv      = "env.json"
	fileEnvSh    = "env.sh"
	fileEnvApply = "env-apply.sh"
	filePortRule = "port_rules.json"
	fileAudit    = "audit.log"
	fileLock     = "mymac.lock"
)

// BackupKeep is the number of rotated backups retained for rewritten files.
const BackupKeep = 10

// Home returns the user's home directory.
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// Root is ~/.config/mymac.
func Root() string {
	return filepath.Join(Home(), ".config", AppName)
}

// BackupDir is ~/.config/mymac/backup.
func BackupDir() string { return filepath.Join(Root(), dirBackup) }

// IconsDir is ~/.config/mymac/icons.
func IconsDir() string { return filepath.Join(Root(), dirIcons) }

// EnvFile is ~/.config/mymac/env.json.
func EnvFile() string { return filepath.Join(Root(), fileEnv) }

// PortRulesFile is ~/.config/mymac/port_rules.json.
func PortRulesFile() string { return filepath.Join(Root(), filePortRule) }

// EnvSh is ~/.config/mymac/env.sh, sourced by the shell marker block.
func EnvSh() string { return filepath.Join(Root(), fileEnvSh) }

// EnvApplySh is ~/.config/mymac/env-apply.sh, run by the login launch agent.
func EnvApplySh() string { return filepath.Join(Root(), fileEnvApply) }

// AuditLog is ~/.config/mymac/audit.log.
func AuditLog() string { return filepath.Join(Root(), fileAudit) }

// LockFile is ~/.config/mymac/mymac.lock, the single instance lock.
func LockFile() string { return filepath.Join(Root(), fileLock) }

// LaunchAgentPlist is ~/Library/LaunchAgents/com.mymac.env.plist.
func LaunchAgentPlist() string {
	return filepath.Join(Home(), "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}

// ZshEnv is ~/.zshenv, the file mymac owns a marker block inside.
func ZshEnv() string { return filepath.Join(Home(), ".zshenv") }

// BashProfile is ~/.bash_profile.
func BashProfile() string { return filepath.Join(Home(), ".bash_profile") }

// FishConfDir is ~/.config/fish/conf.d.
func FishConfDir() string { return filepath.Join(Home(), ".config", "fish", "conf.d") }

// EnsureDirs creates the config tree with restrictive permissions.
func EnsureDirs() error {
	for _, d := range []string{Root(), BackupDir(), IconsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" {
		return Home()
	}
	if len(p) > 1 && p[0] == '~' && (p[1] == '/' || os.IsPathSeparator(p[1])) {
		return filepath.Join(Home(), p[2:])
	}
	return p
}

// IsProtected reports whether p lives inside the config root. Protected paths
// are never offered for deletion by the cleaner.
func IsProtected(p string) bool {
	root := Root()
	if root == "" {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !hasParentPrefix(rel)
}

func hasParentPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && (rel[2] == '/' || os.IsPathSeparator(rel[2]))
}
