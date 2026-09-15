package main

import (
	"fmt"
	"strings"

	"avatars/internal/app"
)

// IntentDecision is the unified output of all intent classification paths.
// L4: Replaces the three parallel systems (command routing, NL classifier,
// heuristic keyword assessment) with a single struct so the pipeline can
// be explicit: heuristic first, LLM fallback only when needed.
type IntentDecision struct {
	Action     string  `json:"action"`     // e.g. "run", "skills.new", "clarify", "answer"
	Target     string  `json:"target"`     // e.g. "file.go", "config", "workflow"
	Confidence float64 `json:"confidence"` // 0.0-1.0
	Source     string  `json:"source"`     // "command", "heuristic", "keyword", "llm"
	Reason     string  `json:"reason"`     // human-readable explanation
}

// ClassifyIntent is the unified intent classification pipeline. L4.
//
// Pipeline:
//  1. Command matching (exact) → confidence 1.0
//  2. Heuristic/keyword from intent_keywords.yaml → confidence varies
//  3. If confidence < 0.7, fall back to LLM classification
//
// This replaces the previous ad-hoc flow where command routing, keyword
// matching, and LLM classification ran independently and their results
// were merged implicitly.
func ClassifyIntent(input string) IntentDecision {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return IntentDecision{Action: "none", Confidence: 0, Source: "command", Reason: "empty input"}
	}

	// Phase 1: Command matching (highest priority, deterministic).
	if decision, ok := classifyIntentByCommand(trimmed); ok {
		decision.Source = "command"
		return decision
	}

	// Phase 2: Heuristic keyword matching from intent_keywords.yaml.
	if decision, ok := classifyIntentByKeywords(trimmed); ok {
		decision.Source = "keyword"
		return decision
	}

	// Phase 3: Fall through — the caller should now invoke LLM classification.
	// We return a low-confidence placeholder that signals "needs LLM".
	return IntentDecision{
		Action:     "classify",
		Target:     trimmed,
		Confidence: 0.3,
		Source:     "heuristic",
		Reason:     "no keyword match — needs LLM classification",
	}
}

// classifyIntentByCommand matches against known CLI commands.
// Returns high-confidence decisions for exact command patterns.
func classifyIntentByCommand(input string) (IntentDecision, bool) {
	lower := strings.ToLower(input)

	// Skills management commands.
	if strings.HasPrefix(lower, "skills new") || strings.HasPrefix(lower, "skill new") {
		topic := strings.TrimSpace(input[strings.Index(lower, "new")+3:])
		return IntentDecision{
			Action: "skills.new", Target: topic, Confidence: 1.0,
			Reason: fmt.Sprintf("exact command match: skills new %q", topic),
		}, true
	}
	if strings.HasPrefix(lower, "skills generate") || strings.HasPrefix(lower, "skill generate") {
		return IntentDecision{Action: "skills.generate", Confidence: 1.0, Reason: "exact command: skills generate"}, true
	}
	if strings.HasPrefix(lower, "skills approve") {
		return IntentDecision{Action: "skills.approve", Confidence: 1.0, Reason: "exact command: skills approve"}, true
	}
	if strings.HasPrefix(lower, "skills list") || lower == "skills" {
		return IntentDecision{Action: "skills.list", Confidence: 1.0, Reason: "exact command: skills list"}, true
	}

	// Plan management.
	if strings.Contains(lower, "construct a plan") || strings.Contains(lower, "build a plan") ||
		strings.Contains(lower, "create a plan") || strings.Contains(lower, "生成计划") || strings.Contains(lower, "制定计划") {
		return IntentDecision{Action: "plan.construct", Confidence: 0.95, Reason: "plan construction request"}, true
	}
	if strings.Contains(lower, "confirm the plan") || strings.Contains(lower, "确认计划") {
		return IntentDecision{Action: "plan.confirm", Confidence: 0.95, Reason: "plan confirmation request"}, true
	}

	// Run / task execution.
	if strings.HasPrefix(lower, "run ") {
		task := strings.TrimSpace(input[4:])
		return IntentDecision{Action: "run", Target: task, Confidence: 1.0, Reason: "exact command: run"}, true
	}
	if strings.Contains(lower, "继续推进") || strings.Contains(lower, "继续做") || strings.Contains(lower, "continue") {
		return IntentDecision{Action: "run.continue", Confidence: 0.9, Reason: "continue keyword"}, true
	}

	// Architecture.
	if strings.HasPrefix(lower, "arch ") || strings.Contains(lower, "architecture") || strings.Contains(lower, "架构") {
		return IntentDecision{Action: "arch", Confidence: 0.95, Reason: "architecture/arch keyword"}, true
	}

	// Verify.
	if strings.HasPrefix(lower, "verify") || strings.Contains(lower, "验证") || strings.Contains(lower, "检查编译") {
		return IntentDecision{Action: "verify", Confidence: 0.95, Reason: "verify keyword"}, true
	}

	// Questions / help.
	if looksLikeQuestion(lower) {
		return IntentDecision{Action: "answer", Confidence: 0.8, Reason: "question pattern detected"}, true
	}

	return IntentDecision{}, false
}

// classifyIntentByKeywords uses the unified intent_keywords.yaml word lists
// loaded via L1's GlobalIntentKeywords.
func classifyIntentByKeywords(input string) (IntentDecision, bool) {
	lower := strings.ToLower(input)

	// Documentation request.
	if app.HasKeyword(lower, "documentation", "") {
		return IntentDecision{Action: "run", Target: "documentation", Confidence: 0.85,
			Reason: "documentation keyword match"}, true
	}

	// Multi-step / large task.
	if app.HasKeyword(lower, "large", "") {
		return IntentDecision{Action: "run", Target: "large", Confidence: 0.8,
			Reason: "large/multi-step keyword match"}, true
	}
	if app.HasKeyword(lower, "multi_step", "") {
		return IntentDecision{Action: "run", Target: "multi_step", Confidence: 0.75,
			Reason: "multi-step keyword match"}, true
	}

	// Code implementation.
	if app.HasKeyword(lower, "code_impl", "") {
		return IntentDecision{Action: "run", Target: "code_impl", Confidence: 0.7,
			Reason: "code implementation keyword match"}, true
	}

	// Single action (fix, rename, delete, etc.).
	if app.HasKeyword(lower, "single_action", "") {
		return IntentDecision{Action: "run", Target: "single_action", Confidence: 0.7,
			Reason: "single action keyword match"}, true
	}

	// Deep signals (database, encryption, auth, etc.).
	if app.HasKeyword(lower, "deep", "") {
		return IntentDecision{Action: "run", Target: "deep", Confidence: 0.65,
			Reason: "deep technical signal match"}, true
	}

	return IntentDecision{}, false
}

// looksLikeQuestion returns true if the input appears to be a question
// rather than a command.
func looksLikeQuestion(input string) bool {
	lower := strings.ToLower(input)
	questionWords := []string{
		"what", "how", "why", "when", "where", "who", "which",
		"can you", "could you", "would you", "is it", "are there",
		"explain", "describe", "tell me", "show me",
		"什么是", "怎么", "如何", "为什么", "能不能", "可以",
	}
	for _, qw := range questionWords {
		if strings.HasPrefix(lower, qw) || strings.Contains(lower, " "+qw) {
			return true
		}
	}
	// Ends with question mark.
	if strings.HasSuffix(strings.TrimSpace(input), "?") || strings.HasSuffix(strings.TrimSpace(input), "？") {
		return true
	}
	return false
}
