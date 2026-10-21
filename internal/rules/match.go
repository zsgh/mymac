package rules

import (
	"fmt"
	"path"
	"runtime"
	"strings"
)

// Check results.
const (
	CheckMatched    = "matched"
	CheckNotMatched = "not_matched"
	CheckUnknown    = "unknown"
)

// Subject is the identity of a process, used for rule matching.
//
// The Known fields tell whether a value could be read; "read failed" and
// "confirmed empty" must not be confused.
type Subject struct {
	PID       int32
	PPID      int32
	Name      string
	Exe       string
	Username  string
	NameKnown bool
	ExeKnown  bool
	UserKnown bool
}

// ProcessMatch is the set of rules that hit a process.
type ProcessMatch struct {
	Whitelist    *RuleRef
	Blacklist    *RuleRef
	Builtin      *RuleRef
	Unknown      bool
	UnknownCause string
}

// PortMatch is the set of rules that hit a port.
type PortMatch struct {
	Whitelist *RuleRef
	Blacklist *RuleRef
	Builtin   *RuleRef
}

// Decision is the final verdict for a process or a connection.
type Decision struct {
	IsSystem     bool     `json:"is_system"`
	Check        string   `json:"system_check"`
	Reason       string   `json:"system_reason,omitempty"`
	Rule         *RuleRef `json:"system_rule,omitempty"`
	Whitelisted  bool     `json:"whitelisted,omitempty"`
	UnknownCause string   `json:"-"`
}

// Evaluator is a read-only snapshot of the rules, cheap enough to use for
// every row.
type Evaluator struct {
	builtin   RuleSet
	whitelist RuleSet
	blacklist RuleSet
}

// NewEvaluator returns a snapshot of the current rules.
func (s *Store) NewEvaluator() *Evaluator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &Evaluator{
		builtin:   s.builtin,
		whitelist: s.user.Whitelist,
		blacklist: s.user.Blacklist,
	}
}

// MatchProcess decides the process level rules.
//
// The port rules are not part of the process decision: a connection is
// judged by both.
func (e *Evaluator) MatchProcess(sub Subject) ProcessMatch {
	var pm ProcessMatch
	if ref := matchProcessRuleSet(e.whitelist, SourceUserWhitelist, sub); ref != nil {
		pm.Whitelist = ref
		return pm
	}
	if ref := matchProcessRuleSet(e.blacklist, SourceUserBlacklist, sub); ref != nil {
		pm.Blacklist = ref
		return pm
	}
	if ref := matchProcessRuleSet(e.builtin, SourceBuiltin, sub); ref != nil {
		pm.Builtin = ref
		return pm
	}

	var missing []string
	if !sub.NameKnown {
		missing = append(missing, "进程名")
	}
	if !sub.ExeKnown {
		missing = append(missing, "可执行路径")
	}
	if !sub.UserKnown {
		missing = append(missing, "属主")
	}
	if len(missing) > 0 {
		pm.Unknown = true
		pm.UnknownCause = "无法确认进程身份：缺少" + strings.Join(missing, "、") + "（可能权限不足）"
	}
	return pm
}

// MatchPort decides the port level rules.
func (e *Evaluator) MatchPort(port uint32) PortMatch {
	var pm PortMatch
	if ref := matchPortRuleSet(e.whitelist, SourceUserWhitelist, port); ref != nil {
		pm.Whitelist = ref
	}
	if ref := matchPortRuleSet(e.blacklist, SourceUserBlacklist, port); ref != nil {
		pm.Blacklist = ref
	}
	if ref := matchPortRuleSet(e.builtin, SourceBuiltin, port); ref != nil {
		pm.Builtin = ref
	}
	return pm
}

// DecideProcess turns a process match into the display verdict.
func (e *Evaluator) DecideProcess(pm ProcessMatch) Decision {
	if pm.Whitelist != nil {
		return Decision{
			Check:       CheckNotMatched,
			Reason:      pm.Whitelist.Reason() + "（强制显示）",
			Rule:        pm.Whitelist,
			Whitelisted: true,
		}
	}
	if pm.Blacklist != nil {
		return Decision{IsSystem: true, Check: CheckMatched, Reason: pm.Blacklist.Reason(), Rule: pm.Blacklist}
	}
	if pm.Builtin != nil {
		return Decision{IsSystem: true, Check: CheckMatched, Reason: pm.Builtin.Reason(), Rule: pm.Builtin}
	}
	if pm.Unknown {
		return Decision{Check: CheckUnknown, Reason: pm.UnknownCause, UnknownCause: pm.UnknownCause}
	}
	return Decision{Check: CheckNotMatched}
}

// DecideConnection turns a connection match into the display verdict.
//
// The whitelist wins over the blacklist, the blacklist over the builtin
// rules.
func (e *Evaluator) DecideConnection(pm ProcessMatch, portMatch PortMatch) Decision {
	if pm.Whitelist != nil {
		return Decision{
			Check:       CheckNotMatched,
			Reason:      pm.Whitelist.Reason() + "（强制显示）",
			Rule:        pm.Whitelist,
			Whitelisted: true,
		}
	}
	if portMatch.Whitelist != nil {
		return Decision{
			Check:       CheckNotMatched,
			Reason:      portMatch.Whitelist.Reason() + "（强制显示）",
			Rule:        portMatch.Whitelist,
			Whitelisted: true,
		}
	}
	if pm.Blacklist != nil {
		return Decision{IsSystem: true, Check: CheckMatched, Reason: pm.Blacklist.Reason(), Rule: pm.Blacklist}
	}
	if portMatch.Blacklist != nil {
		return Decision{IsSystem: true, Check: CheckMatched, Reason: portMatch.Blacklist.Reason(), Rule: portMatch.Blacklist}
	}
	if pm.Builtin != nil {
		return Decision{IsSystem: true, Check: CheckMatched, Reason: pm.Builtin.Reason(), Rule: pm.Builtin}
	}
	if portMatch.Builtin != nil {
		return Decision{IsSystem: true, Check: CheckMatched, Reason: portMatch.Builtin.Reason(), Rule: portMatch.Builtin}
	}
	if pm.Unknown {
		return Decision{Check: CheckUnknown, Reason: pm.UnknownCause, UnknownCause: pm.UnknownCause}
	}
	return Decision{Check: CheckNotMatched}
}

// Protection checks whether a process is off limits.
//
// The user whitelist is not consulted: the whitelist only controls what is
// displayed, it never lifts the system process protection.
func (e *Evaluator) Protection(sub Subject) *RuleRef {
	if ref := matchProcessRuleSet(e.blacklist, SourceUserBlacklist, sub); ref != nil {
		return ref
	}
	return matchProcessRuleSet(e.builtin, SourceBuiltin, sub)
}

func matchProcessRuleSet(rs RuleSet, source string, sub Subject) *RuleRef {
	for _, pid := range rs.PIDs {
		if pid > 0 && pid == sub.PID {
			return userRef(source, "pid", fmt.Sprintf("%d", pid))
		}
	}
	if sub.NameKnown {
		for _, pattern := range rs.Processes {
			if matchGlob(pattern, sub.Name) {
				return userRef(source, "process", pattern)
			}
		}
	}
	if sub.ExeKnown {
		for _, prefix := range rs.Paths {
			if hasPathPrefix(sub.Exe, prefix) {
				return userRef(source, "path", prefix)
			}
		}
	}
	if sub.UserKnown {
		for _, pattern := range rs.Users {
			if matchUserPattern(pattern, sub.Username) {
				return userRef(source, "user", pattern)
			}
		}
	}
	return nil
}

func matchPortRuleSet(rs RuleSet, source string, port uint32) *RuleRef {
	if port == 0 || port > 65535 {
		return nil
	}
	p := int(port)
	for _, candidate := range rs.Ports {
		if candidate == p {
			return userRef(source, "port", fmt.Sprintf("%d", candidate))
		}
	}
	for _, r := range rs.PortRanges {
		if p >= r.From && p <= r.To {
			return userRef(source, "port_range", fmt.Sprintf("%d-%d", r.From, r.To))
		}
	}
	return nil
}

// matchGlob matches a shell style pattern.
//
// A process name may contain a slash (kworker/0:1) and path.Match would
// treat it as a separator, so the slash is replaced before matching.
func matchGlob(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" || value == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		pattern = strings.ToLower(pattern)
		value = strings.ToLower(value)
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return pattern == value
	}
	pattern = strings.ReplaceAll(pattern, "/", "\x00")
	value = strings.ReplaceAll(value, "/", "\x00")
	ok, err := path.Match(pattern, value)
	return err == nil && ok
}

// matchUserPattern matches a user name.
func matchUserPattern(pattern, value string) bool {
	if matchGlob(pattern, value) {
		return true
	}
	// A Windows user name may carry a domain prefix (NT AUTHORITY\SYSTEM);
	// the rule is matched against the plain name as well.
	if idx := strings.LastIndexAny(value, `\/`); idx >= 0 && idx < len(value)-1 {
		return matchGlob(pattern, value[idx+1:])
	}
	return false
}

// hasPathPrefix matches a path prefix on a path boundary, so /usr/sbin does
// not match /usr/sbinx.
func hasPathPrefix(p, prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	p = strings.TrimSpace(p)
	if prefix == "" || p == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
		prefix = strings.ToLower(prefix)
	}
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	if len(p) == len(prefix) {
		return true
	}
	if strings.HasSuffix(prefix, "/") || strings.HasSuffix(prefix, `\`) {
		return true
	}
	next := p[len(prefix)]
	return next == '/' || next == '\\'
}
