// Package runtime — ambiguity detection for P7 clarification loop.
//
// Lightweight heuristics that run BEFORE Planner.BuildForComplexity.
// Detects common ambiguity patterns in user input that lead to LLM guesswork.
// When ambiguity is found, emits ClarifyQuestions and pauses the workflow.
//
// This is the FIRST line of defense. The LLM Planner (when it runs as a
// workflow node with StablePrefix rule #13) provides the SECOND line.

package runtime

import (
	"strings"

	"avatars/internal/planner"
	"avatars/internal/workflow"
)

// detectAmbiguity analyzes user input for common ambiguity patterns.
// Returns nil if the task is specific enough to proceed without clarification.
// Returns 1-2 ClarifyQuestions if the task has multiple valid interpretations.
func detectAmbiguity(input string) []ClarifyQuestion {
	// [NO-CLARIFY] prefix suppresses ambiguity detection on re-runs.
	if strings.HasPrefix(input, "[NO-CLARIFY] ") {
		return nil
	}
	lowered := strings.ToLower(input)
	var questions []ClarifyQuestion

	// Pattern 1: "add a <feature>" without specifying where/how.
	// Examples: "add a search command", "add filtering", "add export"
	if q := detectAddFeatureAmbiguity(lowered, input); q != nil {
		questions = append(questions, *q)
	}

	// Pattern 2: Multiple unprioritized items (comma-separated or "and" chain).
	// Examples: "add search, fix the UI, and update the README"
	if q := detectMultiItemAmbiguity(lowered, input); q != nil && len(questions) < 4 {
		questions = append(questions, *q)
	}

	// Pattern 3: Inherently ambiguous verbs without concrete scope.
	// Examples: "improve the API", "refactor the codebase", "optimize performance"
	if q := detectVagueVerbAmbiguity(lowered, input); q != nil && len(questions) < 4 {
		questions = append(questions, *q)
	}

	if len(questions) == 0 {
		return nil
	}
	return questions
}

// shouldSkipClarifyGate reports when ambiguity heuristics must not block a run.
// Multi-phase / full-course NL and in-progress plans already encode scope (R5-3).
func shouldSkipClarifyGate(input string) bool {
	if strings.HasPrefix(input, "[NO-CLARIFY] ") {
		return true
	}
	if workflow.WantsFullCourse(input) {
		return true
	}
	if workflow.EstimatePhaseCountForTask(input) >= 2 {
		return true
	}
	lower := strings.ToLower(input)
	// Read-only / analysis surveys: clause commas are not a multi-task backlog.
	if strings.Contains(lower, "do not modify") || strings.Contains(lower, "don't modify") ||
		strings.Contains(lower, "without modifying") {
		return true
	}
	if (strings.Contains(lower, "analyze") || strings.Contains(lower, "inspect") ||
		strings.Contains(lower, "explain what") || strings.Contains(lower, "propose a")) &&
		workflow.EstimatePhaseCountForTask(input) < 2 {
		return true
	}
	if workflow.ParseRequestedPhase(input) > 0 && workflow.HasActiveProjectPlan(".") {
		return true
	}
	if workflow.HasActiveProjectPlan(".") && planner.LooksLikeImplementOrResume(input) {
		return true
	}
	return false
}

// detectAddFeatureAmbiguity checks for "add X" patterns where the UI surface
// is ambiguous (CLI subcommand? REPL command? HTTP endpoint? new file?).
func detectAddFeatureAmbiguity(lowered, original string) *ClarifyQuestion {
	// Must contain "add" in a task-oriented context.
	if !strings.Contains(lowered, "add ") && !strings.Contains(lowered, "create ") {
		return nil
	}

	// Skip trivially specific tasks.
	specificMarkers := []string{
		"add a new flag", "add error handling", "add a test",
		"add validation", "add logging", "rename", "fix typo",
		"add a comment", "add documentation",
	}
	for _, m := range specificMarkers {
		if strings.Contains(lowered, m) {
			return nil
		}
	}

	// Check if the task specifies the interaction surface (CLI? file? endpoint?).
	// "command" alone is too ambiguous — "search command" can mean a CLI
	// subcommand, a REPL command, or just a feature named "command".
	// Only count it when paired with a concrete surface word.
	hasSurface := strings.Contains(lowered, "cli command") ||
		strings.Contains(lowered, "subcommand") ||
		strings.Contains(lowered, "endpoint") ||
		strings.Contains(lowered, "api") ||
		strings.Contains(lowered, "repl") ||
		strings.Contains(lowered, "flag") ||
		strings.Contains(lowered, "modify main.go") ||
		strings.Contains(lowered, "http") ||
		strings.Contains(lowered, "route") ||
		strings.Contains(lowered, "handler") ||
		strings.Contains(lowered, "in main.go") ||
		strings.Contains(lowered, "wire into") ||
		strings.Contains(lowered, "register")

	// Check if the task specifies what file to modify or where to add code.
	hasTarget := strings.Contains(lowered, "modify ") ||
		strings.Contains(lowered, "change ") ||
		(strings.Contains(lowered, "in ") && (strings.Contains(lowered, ".go") || strings.Contains(lowered, ".py") || strings.Contains(lowered, ".js")))

	if hasSurface || hasTarget {
		return nil // task is specific enough
	}

	// This is an "add X" without specifying where/how.
	return &ClarifyQuestion{
		ID:       "clarify-surface",
		Question: "Where should this new feature be accessible?",
		Header:   "Interface",
		Options: []ClarifyOption{
			{Label: "CLI subcommand", Description: "Add as a new command-line subcommand (e.g., 'app search <keyword>')"},
			{Label: "Library function", Description: "Add as an internal function callable from other code"},
			{Label: "REPL command", Description: "Add as a command inside an interactive REPL/shell"},
		},
	}
}

// detectMultiItemAmbiguity checks for tasks that list multiple items without
// clear prioritization.
func detectMultiItemAmbiguity(lowered, original string) *ClarifyQuestion {
	// Count task-level separators.
	commas := strings.Count(original, ",")
	ands := strings.Count(lowered, " and ") + strings.Count(lowered, " & ")
	alsos := strings.Count(lowered, "also ") + strings.Count(lowered, "plus ")

	totalItems := commas + ands + alsos + 1 // N separators = N+1 items
	if totalItems < 3 {
		return nil
	}

	// Multiple items detected but no prioritization.
	hasPriority := strings.Contains(lowered, "first") ||
		strings.Contains(lowered, "priority") ||
		strings.Contains(lowered, "most important") ||
		strings.Contains(lowered, "focus on") ||
		strings.Contains(lowered, "start with")

	if hasPriority {
		return nil
	}

	return &ClarifyQuestion{
		ID:       "clarify-priority",
		Question: "You mentioned multiple things. Which should I focus on first?",
		Header:   "Priority",
		Options: []ClarifyOption{
			{Label: "Do all at once", Description: "Implement all items in a single pass"},
			{Label: "Start with first", Description: "Focus on the first item, defer the rest"},
			{Label: "User choice", Description: "Let me pick the most impactful one and explain why"},
		},
	}
}

// detectVagueVerbAmbiguity checks for inherently ambiguous verbs without
// concrete scope or acceptance criteria.
func detectVagueVerbAmbiguity(lowered, original string) *ClarifyQuestion {
	vagueVerbs := []string{"improve", "refactor", "optimize", "clean up", "fix", "enhance"}
	hasVague := false
	for _, v := range vagueVerbs {
		if strings.Contains(lowered, v+" ") || strings.HasPrefix(lowered, v+" ") {
			hasVague = true
			break
		}
	}
	if !hasVague {
		return nil
	}

	// Check for concrete scope indicators.
	hasScope := strings.Contains(lowered, "error handling") ||
		strings.Contains(lowered, "performance") ||
		strings.Contains(lowered, "memory") ||
		strings.Contains(lowered, "speed") ||
		strings.Contains(lowered, "test coverage") ||
		strings.Contains(lowered, "n+1") ||
		strings.Contains(lowered, "slow query") ||
		strings.Contains(lowered, "in ") && (strings.Contains(lowered, ".go") || strings.Contains(lowered, ".py"))

	hasMetric := strings.Contains(lowered, "faster") ||
		strings.Contains(lowered, "less memory") ||
		strings.Contains(lowered, "under ") ||
		strings.Contains(lowered, "from ") && strings.Contains(lowered, "to ")

	if hasScope || hasMetric {
		return nil // task has enough specificity
	}

	return &ClarifyQuestion{
		ID:       "clarify-scope",
		Question: "What specific aspect needs " + extractVagueVerb(lowered) + "?",
		Header:   "Scope",
		Options: []ClarifyOption{
			{Label: "Code quality", Description: "Reduce duplication, improve naming, simplify logic"},
			{Label: "Performance", Description: "Make it faster or use less memory"},
			{Label: "Error handling", Description: "Add proper error checks, messages, and recovery"},
			{Label: "All of the above", Description: "Full polish pass across all dimensions"},
		},
	}
}

func extractVagueVerb(lowered string) string {
	for _, v := range []string{"improve", "refactor", "optimize", "clean up", "fix", "enhance"} {
		if strings.Contains(lowered, v) {
			return v
		}
	}
	return "improvement"
}
