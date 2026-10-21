// Package envs implements macOS environment variable management.
//
// macOS has three distinct layers and this package keeps them apart:
//
//	shell layer   -> ~/.zshenv marker block sourcing ~/.config/mymac/env.sh
//	launchd layer -> launchctl setenv, visible to newly launched GUI apps
//	running procs -> impossible to change from outside; the UI says so
//
// The single source of truth is ~/.config/mymac/env.json. Everything else is
// derived from it.
package envs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mymac/internal/config"
	"mymac/internal/errs"
)

// Scope marks what a variable affects.
type Scope string

// Scopes.
const (
	ScopeUser Scope = "user"
	ScopePath Scope = "path"
)

// storeVersion is bumped when the on-disk format changes.
const storeVersion = 1

// Var is one managed environment variable.
type Var struct {
	Name        string    `json:"name"`
	Value       string    `json:"value"`
	Note        string    `json:"note,omitempty"`
	Enabled     bool      `json:"enabled"`
	Scope       Scope     `json:"scope"`
	Sensitive   bool      `json:"sensitive"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	LastApplied time.Time `json:"lastApplied,omitempty"`
}

// PathConfig holds the PATH fragments mymac prepends and appends.
//
// PATH is never overwritten wholesale: the fragments are merged onto whatever
// PATH the system already has.
type PathConfig struct {
	Enabled bool     `json:"enabled"`
	Prepend []string `json:"prepend"`
	Append  []string `json:"append"`
}

// Store is the content of env.json.
type Store struct {
	Version   int        `json:"version"`
	UpdatedAt time.Time  `json:"updatedAt"`
	Vars      []Var      `json:"vars"`
	Path      PathConfig `json:"path"`

	// path is where the store lives; overridable for tests.
	path string `json:"-"`
}

// namePattern is the portable shell variable name rule.
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// sensitivePattern flags variables whose value should be masked in the UI.
var sensitivePattern = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|PRIVATE)`)

// protectedNames must never be deleted without a strong confirmation.
var protectedNames = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"PATH": true, "TMPDIR": true, "PWD": true, "OLDPWD": true,
	"TERM": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	"HOSTNAME": true, "UID": true, "GID": true, "EDITOR": true,
	"VISUAL": true, "PS1": true, "SSH_AUTH_SOCK": true,
}

// NewStore returns an empty store bound to the default path.
func NewStore() *Store { return NewStoreAt(config.EnvFile()) }

// NewStoreAt returns an empty store bound to path.
func NewStoreAt(path string) *Store {
	return &Store{Version: storeVersion, path: path, Vars: []Var{}, Path: PathConfig{Prepend: []string{}, Append: []string{}}}
}

// FilePath returns the file backing the store.
func (s *Store) FilePath() string { return s.path }

// Load reads the store from disk. A missing file yields an empty store.
func Load(path string) (*Store, error) {
	s := NewStoreAt(path)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, errs.Wrap(err, errs.KindPermission, "无法读取 %s", path)
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, errs.Wrap(err, errs.KindInvalid, "环境变量配置已损坏: %s", path)
	}
	s.path = path
	if s.Vars == nil {
		s.Vars = []Var{}
	}
	for i := range s.Vars {
		s.Vars[i].Scope = ScopeUser
		s.Vars[i].Sensitive = IsSensitive(s.Vars[i].Name)
	}
	return s, nil
}

// Save writes the store atomically with mode 0600.
func (s *Store) Save() error {
	s.Version = storeVersion
	s.UpdatedAt = time.Now()
	if s.Vars == nil {
		s.Vars = []Var{}
	}
	if s.Path.Prepend == nil {
		s.Path.Prepend = []string{}
	}
	if s.Path.Append == nil {
		s.Path.Append = []string{}
	}
	sort.SliceStable(s.Vars, func(i, j int) bool { return s.Vars[i].Name < s.Vars[j].Name })

	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "序列化失败")
	}
	b = append(b, '\n')
	return WriteFileAtomic(s.path, b, 0o600)
}

// Find returns the managed variable with the given name.
func (s *Store) Find(name string) (Var, bool) {
	for _, v := range s.Vars {
		if v.Name == name {
			return v, true
		}
	}
	return Var{}, false
}

// Upsert adds or replaces a variable.
func (s *Store) Upsert(v Var) {
	now := time.Now()
	for i := range s.Vars {
		if s.Vars[i].Name == v.Name {
			if v.CreatedAt.IsZero() {
				v.CreatedAt = s.Vars[i].CreatedAt
			}
			v.UpdatedAt = now
			v.Sensitive = IsSensitive(v.Name)
			s.Vars[i] = v
			return
		}
	}
	v.CreatedAt = now
	v.UpdatedAt = now
	v.Sensitive = IsSensitive(v.Name)
	s.Vars = append(s.Vars, v)
}

// Remove drops a variable by name.
func (s *Store) Remove(name string) bool {
	for i := range s.Vars {
		if s.Vars[i].Name == name {
			s.Vars = append(s.Vars[:i], s.Vars[i+1:]...)
			return true
		}
	}
	return false
}

// Active returns the enabled variables in stable name order.
func (s *Store) Active() []Var {
	out := make([]Var, 0, len(s.Vars))
	for _, v := range s.Vars {
		if v.Enabled {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// IsSensitive reports whether a variable name looks secret.
func IsSensitive(name string) bool { return sensitivePattern.MatchString(name) }

// IsProtected reports whether a name is on the do-not-delete list.
func IsProtected(name string) bool {
	if protectedNames[name] {
		return true
	}
	if strings.HasPrefix(name, "LC_") {
		return true
	}
	return false
}

// ValidateName checks a shell variable name.
func ValidateName(name string) error {
	if name == "" {
		return errs.New(errs.KindInvalid, "变量名不能为空")
	}
	if !namePattern.MatchString(name) {
		return errs.New(errs.KindInvalid, "变量名 %q 非法：只允许字母、数字和下划线，且不能以数字开头", name)
	}
	if IsProtected(name) {
		return errs.New(errs.KindInvalid, "%s 是系统关键变量，修改需要强确认", name)
	}
	return nil
}

// WriteFileAtomic writes data to path via a temp file and a rename, so a
// failure never leaves a half written configuration behind.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errs.Wrap(err, errs.KindPermission, "无法创建目录 %s", dir)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return errs.Wrap(err, errs.KindPermission, "无法在 %s 创建临时文件", dir)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return errs.Wrap(err, errs.KindInternal, "写入临时文件失败")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errs.Wrap(err, errs.KindInternal, "刷盘失败")
	}
	if err := tmp.Close(); err != nil {
		return errs.Wrap(err, errs.KindInternal, "关闭临时文件失败")
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return errs.Wrap(err, errs.KindPermission, "设置权限失败")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errs.Wrap(err, errs.KindPermission, "替换 %s 失败", path)
	}
	// Sync the directory so the rename is durable.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// String renders a variable for the audit log.
func (v Var) String() string {
	if v.Sensitive {
		return fmt.Sprintf("%s=***", v.Name)
	}
	return fmt.Sprintf("%s=%s", v.Name, v.Value)
}
