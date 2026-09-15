// Package main — intelligent natural-language intent fallback.
//
// C-3 fix: Replaces the unhelpful "LLM unavailable — please rephrase" message
// with a structured fallback pipeline that:
//   1. Diagnoses WHY routing failed (network / auth / parse / unavailable)
//   2. Analyzes user input locally to determine the most likely action
//   3. Provides language-appropriate, actionable guidance
//   4. Auto-suggests "avatars run <input>" when input looks like a task
//
// Pattern adapted from claude_code_main: when you can't classify, default to
// "just try executing it" rather than erroring out. Claude Code doesn't have a
// separate "intent" step — it directly processes user input. This is the same
// principle: if routing fails, the fallback should still try to help the user
// get work done.

package main

import (
	"fmt"
	"strings"

	"avatars/internal/app"
)

// fallbackReason describes why LLM routing failed.
type fallbackReason int

const (
	fallbackNone           fallbackReason = iota
	fallbackLLMUnavailable                // network error, timeout, API down
	fallbackLLMAuthError                  // 401/403 — API key issue
	fallbackLLMParseError                 // LLM responded but output was unparseable
	fallbackLLMClarify                    // LLM explicitly asked for clarification
	fallbackNoMatch                       // no keyword or LLM match found
)

// fallbackResult is the structured output of the fallback analysis.
type fallbackResult struct {
	Reason      fallbackReason
	Diagnosis   string   // human-readable diagnosis
	Suggestion  string   // primary suggestion
	AltCommands []string // alternative commands to try
	AutoRun     bool     // if true, input is task-like and can be auto-executed
}

// buildFallbackResponse analyzes a failed LLM routing attempt and produces
// a structured response that guides the user toward a working command.
//
// Unlike the old "please rephrase" message, this:
//   - Tells the user WHAT went wrong (network? auth? parse error?)
//   - Suggests SPECIFIC next steps based on their input
//   - Auto-generates the most likely command when input looks task-like
//   - Adapts language (Chinese / English) to match the user's input
func buildFallbackResponse(input string, routeErr error, llmAvailable bool, llmParseOK bool) fallbackResult {
	lower := strings.ToLower(strings.TrimSpace(input))

	// Step 1: Diagnose the failure reason.
	var reason fallbackReason
	var diagnosis string

	if routeErr != nil {
		errStr := strings.ToLower(routeErr.Error())
		switch {
		case strings.Contains(errStr, "401") || strings.Contains(errStr, "403") ||
			strings.Contains(errStr, "unauthorized") || strings.Contains(errStr, "invalid api key"):
			reason = fallbackLLMAuthError
			diagnosis = "API authentication failed. The configured API key may be invalid or expired."
		case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") ||
			strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "no such host"):
			reason = fallbackLLMUnavailable
			diagnosis = "The LLM service is unreachable (network timeout or connection error)."
		default:
			reason = fallbackLLMUnavailable
			diagnosis = fmt.Sprintf("LLM routing error: %v", routeErr)
		}
	} else if !llmAvailable {
		reason = fallbackLLMUnavailable
		diagnosis = "The LLM service is currently unavailable. This may be a temporary network issue or the configured provider is down."
	} else if !llmParseOK {
		reason = fallbackLLMParseError
		diagnosis = "The LLM responded but the output could not be parsed into a valid command."
	} else {
		reason = fallbackNoMatch
		diagnosis = "The request could not be matched to a specific command."
	}

	// Step 2: Local analysis — what is the user trying to do?
	category := classifyInputLocally(lower)
	isTask := isTaskLike(lower)
	isQuestion := isQuestionLike(lower)

	result := fallbackResult{
		Reason:    reason,
		Diagnosis: diagnosis,
	}

	// S1 safety: never AutoRun mass-wipe / system destruction intents.
	if looksLikeMassDestructiveWipe(lower) || shouldOverrideLLMDecision(auditInputForUnconditionalRisk(input)) {
		result.Suggestion = "This looks like a mass-delete / destructive wipe. I will not suggest avatars run.\nRephrase to a narrower, reversible action, or use read-only analysis first."
		result.AutoRun = false
		result.AltCommands = []string{
			fmt.Sprintf(`run --new-task --permission-mode plan %q`, input),
			"route \"" + input + "\"",
		}
		return result
	}

	// Vague mutate-without-target ("帮我改一下" / "fix it") must clarify,
	// not AutoRun — otherwise LLM-down path feels reckless.
	if looksLikeVagueEditWithoutTarget(lower) {
		result.AutoRun = false
		result.Suggestion = "Which file should I change, and how? Add a path and concrete edit, e.g.:\n  avatars edit --apply README.md \"fix the title\""
		result.AltCommands = []string{
			`edit --apply <file> "<instruction>"`,
			fmt.Sprintf(`route %q`, input),
		}
		return result
	}

	switch {
	case isTask && (reason == fallbackLLMUnavailable || reason == fallbackLLMAuthError):
		// User wants to do something, LLM can't help. Best we can do: run it directly.
		result.AutoRun = true
		result.Suggestion = fmt.Sprintf(
			"Network issue — AI analysis unavailable. Your input looks like a task. Try running it directly:\n  avatars run --new-task %q\n\nOr wait and retry when the network recovers:\n  avatars %q",
			input, input)
		result.AltCommands = []string{fmt.Sprintf("run --new-task %s", input), input}

	case isTask && reason == fallbackLLMParseError:
		// LLM responded but we couldn't parse it. Run directly as fallback.
		result.AutoRun = true
		result.Suggestion = fmt.Sprintf(
			"AI response was unparseable. Your input looks like a task — try running directly:\n  avatars run --new-task %q",
			input)
		result.AltCommands = []string{fmt.Sprintf("run --new-task %s", input)}

	case isQuestion:
		// User asked a question. Without LLM, we can only answer simple ones.
		if answer, ok, _ := answerLocalQuestion(lower); ok {
			result.Suggestion = fmt.Sprintf("AI unavailable, but found a local answer:\n\n%s\n\nFor tasks, use an explicit command:\n  avatars run \"%s\"", answer, input)
		} else {
			result.Suggestion = fmt.Sprintf(
				"%s\n\nYour input looks like a question. The AI is currently unavailable. You can:\n  1. Retry when the network recovers: avatars %q\n  2. If this is a task, not a question: avatars run --new-task %q\n  3. See available features with: avatars skills  or  avatars help",
				diagnosis, input, input)
		}

	default:
		// Generic fallback — neither clear task nor clear question.
		switch category {
		case "code_impl", "multi_step":
			result.AutoRun = true
			result.Suggestion = fmt.Sprintf("%s\n\nInput contains code-implementation keywords. Try running directly:\n  avatars run --new-task %q", diagnosis, input)
			result.AltCommands = []string{fmt.Sprintf("run --new-task %s", input)}
		default:
			result.Suggestion = fmt.Sprintf(
				"%s\n\nCannot determine your intent. Try:\n  1. Explicit task: avatars run \"%s\"\n  2. Help: avatars help\n  3. Rephrase your request",
				diagnosis, input)
		}
	}

	return result
}

// classifyInputLocally uses the centralized keyword registry to determine
// what category the user's input most likely falls into. This is a fast
// local check that doesn't require LLM.
func classifyInputLocally(lower string) string {
	if app.HasKeyword(lower, "code_impl", "") {
		return "code_impl"
	}
	if app.HasKeyword(lower, "multi_step", "") {
		return "multi_step"
	}
	if app.HasKeyword(lower, "single_action", "") {
		return "single_action"
	}
	if app.HasKeyword(lower, "documentation", "") {
		return "documentation"
	}
	if app.HasKeyword(lower, "deep", "") {
		return "deep"
	}
	return ""
}

// isTaskLike returns true if the input looks like a task/action request
// rather than a question or conversation.
func isTaskLike(lower string) bool {
	// Strong task indicators: imperative verbs, code operations.
	taskIndicators := []string{
		"create", "build", "make", "add", "fix", "update", "remove",
		"delete", "install", "setup", "configure", "deploy", "run",
		"test", "generate", "write", "implement", "refactor", "migrate",
		"创建", "构建", "添加", "修复", "更新", "删除", "安装",
		"配置", "部署", "运行", "测试", "生成", "实现", "重构",
		"写一个", "做一个", "改一下", "加一个", "新建",
	}
	for _, v := range taskIndicators {
		if strings.Contains(lower, v) {
			return true
		}
	}

	// File-oriented requests.
	if strings.Contains(lower, ".go") || strings.Contains(lower, ".py") ||
		strings.Contains(lower, ".js") || strings.Contains(lower, ".ts") ||
		strings.Contains(lower, ".yaml") || strings.Contains(lower, ".json") {
		return true
	}

	// Code/tech terms suggest task intent.
	techCount := 0
	techTerms := []string{"api", "function", "struct", "class", "module", "package",
		"import", "handler", "server", "database", "sql", "json", "yaml",
		"接口", "函数", "模块", "包", "服务", "数据库"}
	for _, t := range techTerms {
		if strings.Contains(lower, t) {
			techCount++
		}
	}
	return techCount >= 2
}

// isQuestionLike returns true if the input looks like a question.
func isQuestionLike(lower string) bool {
	questionMarks := []string{
		"what", "how", "why", "when", "where", "who", "which",
		"can you", "could you", "would you", "is it", "are there",
		"什么是", "怎么", "如何", "为什么", "是什么", "能不能",
		"可以", "是否", "有没有", "怎么样", "? ", "?",
		"吗？", "呢？", "？",
	}
	for _, q := range questionMarks {
		if strings.Contains(lower, q) {
			return true
		}
	}
	return false
}

// looksLikeVagueEditWithoutTarget detects underspecified mutate requests
// with no file path / extension — e.g. "帮我改一下", "fix it".
func looksLikeVagueEditWithoutTarget(lower string) bool {
	lower = strings.ToLower(strings.TrimSpace(lower))
	if lower == "" {
		return false
	}
	// Has an explicit path or extension → not vague.
	if strings.Contains(lower, "/") || strings.Contains(lower, "\\") ||
		strings.Contains(lower, ".go") || strings.Contains(lower, ".md") ||
		strings.Contains(lower, ".py") || strings.Contains(lower, ".js") ||
		strings.Contains(lower, ".ts") || strings.Contains(lower, ".yaml") ||
		strings.Contains(lower, ".json") || strings.Contains(lower, ".html") {
		return false
	}
	vague := []string{
		"帮我改一下", "改一下", "修改一下", "改改", "修一下",
		"fix it", "fix this", "change it", "update it", "edit it",
		"make a change", "帮我改", "改一下吧",
	}
	for _, v := range vague {
		if lower == v || strings.TrimSpace(lower) == v {
			return true
		}
	}
	// Short vague mutate with no object noun.
	if (strings.Contains(lower, "改一下") || strings.Contains(lower, "fix it") ||
		strings.Contains(lower, "change it") || strings.Contains(lower, "edit it")) &&
		len([]rune(lower)) <= 12 {
		return true
	}
	return false
}

// printFallbackResponse displays the fallback result to the user.
func printFallbackResponse(result fallbackResult) {
	// Step 1: State what went wrong (diagnosis).
	fmt.Printf("\n⚠️  %s\n", result.Diagnosis)

	// Step 2: Provide the primary suggestion.
	fmt.Printf("\n%s\n", result.Suggestion)

	// Step 3: If auto-run is appropriate, ask for confirmation.
	if result.AutoRun {
		fmt.Printf("\n💡 Tip: When the LLM is unavailable, you can bypass intent routing entirely:\n")
		fmt.Printf("   avatars run \"<your task description>\"\n")
	}

	// Step 4: List alternative commands.
	if len(result.AltCommands) > 0 {
		fmt.Printf("\nAlternative commands:\n")
		for _, cmd := range result.AltCommands {
			fmt.Printf("  avatars %s\n", cmd)
		}
	}
}
