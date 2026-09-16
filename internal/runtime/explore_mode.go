// Package runtime — Explore mode for Plan/Explore separation.
//
// P8: Adopts Claude Code's Explore agent pattern
// (claude_code_main/tools/AgentTool/built-in/exploreAgent.ts)
// to enforce deep read-only exploration BEFORE implementation.
//
// Key difference from Survey Mode:
//   Survey: lists files, categorizes by bucket (docs/src/cfg)
//   Explore: reads file contents, traces call chains, understands logic
//
// When permissionMode == PermissionModePlan:
//   1. Researcher switches from Survey to Explore depth
//   2. Mutating tools are denied at permission level (already enforced)
//   3. Exploration report is written to process_record.md
//   4. Builder does NOT execute until user approves the plan

package runtime

import "strings"

// ExploreDepth describes how deeply the Researcher should investigate.
type ExploreDepth string

const (
	// ExploreDepthSurvey — shallow file listing (current behavior).
	ExploreDepthSurvey ExploreDepth = "survey"

	// ExploreDepthExplore — deep content analysis with call-chain tracing.
	ExploreDepthExplore ExploreDepth = "explore"
)

// ExploreConfig controls the Researcher's exploration behavior.
type ExploreConfig struct {
	Depth          ExploreDepth
	MaxFilesToRead int    // cap on file reads during exploration
	ReportPath     string // where to write findings ("" = no write)
	TraceCallChain bool   // follow type/function references across files
}

// DefaultExploreConfig returns the explore configuration for plan mode.
func DefaultExploreConfig() ExploreConfig {
	return ExploreConfig{
		Depth:          ExploreDepthExplore,
		MaxFilesToRead: 20,
		ReportPath:     "docs/workflow/process_record.md",
		TraceCallChain: true,
	}
}

// DefaultSurveyConfig returns the survey configuration for non-plan mode.
func DefaultSurveyConfig() ExploreConfig {
	return ExploreConfig{
		Depth:          ExploreDepthSurvey,
		MaxFilesToRead: 8,
		ReportPath:     "",
		TraceCallChain: false,
	}
}

// ExploreConfigForMode returns the appropriate explore configuration
// based on the current permission mode.
func ExploreConfigForMode(mode PermissionMode) ExploreConfig {
	if mode == PermissionModePlan {
		return DefaultExploreConfig()
	}
	return DefaultSurveyConfig()
}

// IsReadOnlyTool returns true if the tool does not modify files or state.
// Used by permission enforcement but also by Explore mode to select
// appropriate tool sets for deep exploration.
func IsReadOnlyTool(toolName string) bool {
	readOnlyTools := map[string]bool{
		"read":       true,
		"grep":       true,
		"glob":       true,
		"list":       true,
		"stat":       true,
		"git":        true, // git operations are VCS-only, not file mutation
	}
	return readOnlyTools[strings.ToLower(strings.TrimSpace(toolName))]
}

// IsWriteTool returns true if the tool modifies files.
func IsWriteTool(toolName string) bool {
	writeTools := map[string]bool{
		"write":        true,
		"edit_file":    true,
		"patch":        true,
		"precise_edit": true,
		"notebook_edit": true,
	}
	return writeTools[strings.ToLower(strings.TrimSpace(toolName))]
}

// BuildExplorePrompt returns a system prompt fragment instructing the
// Researcher to do deep exploration instead of shallow survey.
// Mirrors Claude Code's exploreAgent prompt pattern:
// "Do not attempt to implement. Your job is to understand, not to build."
func BuildExplorePrompt(config ExploreConfig) string {
	if config.Depth != ExploreDepthExplore {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n## Explore Mode (Deep Read-Only Analysis)\n")
	b.WriteString("You are in EXPLORE mode. Your ONLY job is to understand the codebase deeply.\n")
	b.WriteString("Do NOT attempt to implement anything. Do NOT suggest code changes.\n")
	b.WriteString("Do NOT create, modify, or delete any files.\n\n")
	b.WriteString("Your task:\n")
	b.WriteString("1. READ the key source files end-to-end — don't just list them\n")
	b.WriteString("2. TRACE how types and functions connect across files\n")
	if config.TraceCallChain {
		b.WriteString("3. MAP the call chain: entry point → handler → model → store\n")
		b.WriteString("4. IDENTIFY existing patterns that new code should follow\n")
	}
	b.WriteString("5. DOCUMENT your findings in a structured report\n\n")
	b.WriteString("Output format: Write your findings to " + config.ReportPath + ".\n")
	b.WriteString("Include: [1] Key types and their locations, [2] Data flow, ")
	b.WriteString("[3] Extension points, [4] Risks or gotchas.\n")

	return b.String()
}
