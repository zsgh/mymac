// Package rules manages the system service rules: which ports, processes,
// paths and users count as operating system services.
//
// The priority order is:
//
//  1. user whitelist  (always shown)
//  2. user blacklist  (always hidden)
//  3. builtin rules
//  4. unknown identity -> shown by default
//
// A whitelist controls what is displayed. It never lifts the process
// protection: the system process list is checked separately.
package rules

import "fmt"

// PortRange is a closed port range.
type PortRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// RuleSet is the structure shared by the builtin rules and the user rules.
//
// The process and user patterns support shell wildcards (*, ?, []); the path
// prefixes are matched on path boundaries.
type RuleSet struct {
	Name       string      `json:"name,omitempty"`
	Ports      []int       `json:"ports,omitempty"`
	PortRanges []PortRange `json:"port_ranges,omitempty"`
	Processes  []string    `json:"processes,omitempty"`
	Paths      []string    `json:"paths,omitempty"`
	Users      []string    `json:"users,omitempty"`
	PIDs       []int32     `json:"pids,omitempty"`
}

// UserRules are the user's own whitelist and blacklist.
//
// Priority: whitelist > blacklist > builtin rules.
type UserRules struct {
	Whitelist RuleSet `json:"whitelist"`
	Blacklist RuleSet `json:"blacklist"`
}

// RuleRef names a specific rule, so the UI can show why and where a hit
// came from.
type RuleRef struct {
	Source string `json:"source"` // builtin / user-whitelist / user-blacklist
	Kind   string `json:"kind"`   // port / port_range / process / path / user / pid
	Value  string `json:"value"`
}

// Rule sources.
const (
	SourceBuiltin       = "builtin"
	SourceUserWhitelist = "user-whitelist"
	SourceUserBlacklist = "user-blacklist"
)

func sourceLabel(source string) string {
	switch source {
	case SourceUserWhitelist:
		return "用户白名单"
	case SourceUserBlacklist:
		return "用户黑名单"
	default:
		return "内置规则"
	}
}

func kindLabel(kind string) string {
	switch kind {
	case "port":
		return "端口"
	case "port_range":
		return "端口范围"
	case "process":
		return "进程名"
	case "path":
		return "路径前缀"
	case "user":
		return "用户"
	case "pid":
		return "PID"
	default:
		return kind
	}
}

// Reason is the human readable explanation of a hit.
func (r RuleRef) Reason() string {
	return fmt.Sprintf("%s：%s %s", sourceLabel(r.Source), kindLabel(r.Kind), r.Value)
}

func builtinRef(kind, value string) *RuleRef {
	return &RuleRef{Source: SourceBuiltin, Kind: kind, Value: value}
}

func userRef(source, kind, value string) *RuleRef {
	return &RuleRef{Source: source, Kind: kind, Value: value}
}

// EmptyUserRules returns the empty user rules, used as the fallback when the
// rule file cannot be parsed.
func EmptyUserRules() UserRules {
	return UserRules{
		Whitelist: RuleSet{
			Ports:      []int{},
			PortRanges: []PortRange{},
			Processes:  []string{},
			Paths:      []string{},
			Users:      []string{},
			PIDs:       []int32{},
		},
		Blacklist: RuleSet{
			Ports:      []int{},
			PortRanges: []PortRange{},
			Processes:  []string{},
			Paths:      []string{},
			Users:      []string{},
			PIDs:       []int32{},
		},
	}
}
