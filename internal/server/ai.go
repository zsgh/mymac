package server

import (
	"os"

	"mymac/internal/ai"
)

// newAIAnalyzer builds the analyzer that rates the startup items.
//
// The model can be picked with MYMAC_AI_MODEL; without it the CLI's own
// default is used. The analyzer is optional in every sense: when the CLI is
// not installed the feature is simply not offered.
func newAIAnalyzer() *ai.Analyzer {
	return &ai.Analyzer{
		Model: os.Getenv("MYMAC_AI_MODEL"),
	}
}
