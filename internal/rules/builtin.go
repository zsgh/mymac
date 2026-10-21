package rules

import (
	_ "embed"
	"encoding/json"
)

//go:embed macos.json
var builtinJSON []byte

// builtinRules returns the built-in macOS rules.
//
// It lists the ports and processes that belong to the operating system, so
// the port view can hide them by default.
func builtinRules() RuleSet {
	var rs RuleSet
	if err := json.Unmarshal(builtinJSON, &rs); err != nil {
		// The file is embedded, so a parse error is a build time mistake.
		return RuleSet{Name: "macOS built-in"}
	}
	return rs
}
