package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mymac/internal/config"
	"mymac/internal/errs"
)

// Store holds the builtin rules and the user rules.
//
// The user rules live in ~/.config/mymac/port_rules.json. When that file
// cannot be parsed the store falls back to empty user rules and records the
// reason, so a broken file never stops the service.
type Store struct {
	mu       sync.RWMutex
	builtin  RuleSet
	user     UserRules
	userPath string
	loadErr  string
}

// NewStore loads the builtin rules and the user rules.
func NewStore(userPath string) *Store {
	if userPath == "" {
		userPath = config.PortRulesFile()
	}
	s := &Store{userPath: userPath, user: EmptyUserRules()}
	s.builtin = builtinRules()
	s.loadUser()
	return s
}

// UserPath returns the path of the user rule file.
func (s *Store) UserPath() string { return s.userPath }

// LoadError returns the reason the user rules could not be loaded, if any.
func (s *Store) LoadError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr
}

// Reload reads the user rule file again.
func (s *Store) Reload() error {
	s.mu.Lock()
	s.user = EmptyUserRules()
	s.loadErr = ""
	s.mu.Unlock()
	return s.loadUser()
}

// loadUser reads the user rule file.
func (s *Store) loadUser() error {
	if err := os.MkdirAll(filepath.Dir(s.userPath), 0o700); err != nil {
		s.mu.Lock()
		s.loadErr = err.Error()
		s.mu.Unlock()
		return err
	}
	data, err := os.ReadFile(s.userPath)
	if err != nil {
		if os.IsNotExist(err) {
			// First run: write the empty rules so the file exists.
			return s.saveUser(EmptyUserRules())
		}
		s.mu.Lock()
		s.loadErr = err.Error()
		s.mu.Unlock()
		return err
	}
	var user UserRules
	if err := json.Unmarshal(data, &user); err != nil {
		s.mu.Lock()
		s.loadErr = "规则文件解析失败: " + err.Error()
		s.mu.Unlock()
		return errs.New(errs.KindInvalid, "%s", s.loadErr)
	}
	s.mu.Lock()
	s.user = user
	s.mu.Unlock()
	return nil
}

// saveUser writes the user rule file.
func (s *Store) saveUser(user UserRules) error {
	b, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "序列化规则失败")
	}
	b = append(b, '\n')
	if err := os.WriteFile(s.userPath, b, 0o600); err != nil {
		return errs.Wrap(err, errs.KindPermission, "无法写入规则文件")
	}
	return nil
}

// User returns a copy of the user rules.
func (s *Store) User() UserRules {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.user
}

// Builtin returns a copy of the builtin rules.
func (s *Store) Builtin() RuleSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.builtin
}

// Override adds or removes one user rule.
//
// The value is appended to the matching list of the whitelist or the
// blacklist.
func (s *Store) Override(mode, kind, value string, remove bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loadErr != "" {
		return errs.New(errs.KindInvalid, "规则文件当前无法解析，已禁止修改：%s", s.loadErr)
	}

	var target *RuleSet
	switch mode {
	case "whitelist":
		target = &s.user.Whitelist
	case "blacklist":
		target = &s.user.Blacklist
	default:
		return errs.New(errs.KindInvalid, "未知的规则集合 %q", mode)
	}

	switch kind {
	case "port":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n <= 0 || n > 65535 {
			return errs.New(errs.KindInvalid, "端口必须是 1-65535 之间的数字")
		}
		target.Ports = toggleInt(target.Ports, n, remove)
	case "process":
		if strings.TrimSpace(value) == "" {
			return errs.New(errs.KindInvalid, "进程名不能为空")
		}
		target.Processes = toggleString(target.Processes, value, remove)
	case "path":
		if strings.TrimSpace(value) == "" {
			return errs.New(errs.KindInvalid, "路径不能为空")
		}
		target.Paths = toggleString(target.Paths, value, remove)
	case "user":
		if strings.TrimSpace(value) == "" {
			return errs.New(errs.KindInvalid, "用户名不能为空")
		}
		target.Users = toggleString(target.Users, value, remove)
	case "pid":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n <= 0 {
			return errs.New(errs.KindInvalid, "PID 必须是正整数")
		}
		target.PIDs = toggleInt32(target.PIDs, int32(n), remove)
	default:
		return errs.New(errs.KindInvalid, "未知的规则类型 %q", kind)
	}

	if err := s.saveUser(s.user); err != nil {
		return err
	}
	return nil
}

// toggleInt adds or removes a value from a list.
func toggleInt(list []int, v int, remove bool) []int {
	for i, x := range list {
		if x == v {
			if remove {
				return append(list[:i], list[i+1:]...)
			}
			return list
		}
	}
	if remove {
		return list
	}
	return append(list, v)
}

// toggleInt32 adds or removes a value from a list.
func toggleInt32(list []int32, v int32, remove bool) []int32 {
	for i, x := range list {
		if x == v {
			if remove {
				return append(list[:i], list[i+1:]...)
			}
			return list
		}
	}
	if remove {
		return list
	}
	return append(list, v)
}

// toggleString adds or removes a value from a list.
func toggleString(list []string, v string, remove bool) []string {
	for i, x := range list {
		if x == v {
			if remove {
				return append(list[:i], list[i+1:]...)
			}
			return list
		}
	}
	if remove {
		return list
	}
	return append(list, v)
}

// Summary describes the loaded rules for the UI.
type Summary struct {
	Builtin   RuleSet   `json:"builtin"`
	User      UserRules `json:"user"`
	UserPath  string    `json:"userPath"`
	LoadError string    `json:"loadError,omitempty"`
	Platform  string    `json:"platform"`
	Verified  bool      `json:"verified"`
}

// Summary returns the rule summary.
func (s *Store) Summary() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Summary{
		Builtin:   s.builtin,
		User:      s.user,
		UserPath:  s.userPath,
		LoadError: s.loadErr,
		Platform:  "macOS",
		Verified:  true,
	}
}

// String renders the rules for the log.
func (s Summary) String() string {
	return fmt.Sprintf("builtin: %d ports / %d processes, user: %d whitelist / %d blacklist",
		len(s.Builtin.Ports), len(s.Builtin.Processes),
		len(s.User.Whitelist.Ports)+len(s.User.Whitelist.Processes),
		len(s.User.Blacklist.Ports)+len(s.User.Blacklist.Processes))
}
