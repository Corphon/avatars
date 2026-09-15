package main

import "strings"

// offlineIntentCandidates is the no-LLM fallback for routeIntent.
// Keyword taxonomy was removed from the live path (NL5); this table only
// covers bounded CLI utilities plus the existing NL classifier so tests and
// operators still get a command when DEEPSEEK_API_KEY is unset.
func offlineIntentCandidates(input string) []intentCandidate {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}
	lowered := strings.ToLower(trimmed)

	if cands := offlineGovernanceInspectCandidates(trimmed, lowered); len(cands) > 0 {
		return cands
	}
	if cmd, summary, ok := offlineUtilityCommand(lowered); ok {
		return []intentCandidate{{
			Summary:    summary,
			Confidence: 90,
			Command:    cmd,
			AutoSafe:   true,
		}}
	}
	if taskID := taskIDFromIntent(trimmed); taskID != "" && !looksLikeImplementationWorkIntent(lowered) {
		return []intentCandidate{{
			Summary:    "run the safe question in the named workspace",
			Confidence: 82,
			Command:    []string{"run", "--task", taskID, trimmed},
		}}
	}

	nd := classifyNaturalLanguageQuestionWithoutLLM(trimmed, replCommandOptions{}, naturalLanguageContextIntent)
	return naturalLanguageDecisionToIntentCandidates(nd)
}

func naturalLanguageDecisionToIntentCandidates(nd naturalLanguageDecision) []intentCandidate {
	if nd.Kind != naturalLanguageDecisionSafeRun || len(nd.Command) == 0 {
		return nil
	}
	return []intentCandidate{{
		Summary:    firstNonEmpty(nd.Reason, "offline safe_run"),
		Confidence: nd.Confidence,
		Command:    append([]string(nil), nd.Command...),
		AutoSafe:   isAutoSafeIntentCommand(nd.Command),
	}}
}

func offlineGovernanceInspectCandidates(input, lowered string) []intentCandidate {
	if !strings.Contains(lowered, "governance") || !containsAnyIntentToken(lowered, "inspect", "status", "show") {
		return nil
	}
	taskID := taskIDFromIntent(input)
	cands := []intentCandidate{{
		Summary:     "inspect governance status",
		Confidence:  78,
		Command:     []string{"governance", "status"},
		NeedsChoice: true,
	}}
	if taskID != "" {
		cands = append([]intentCandidate{{
			Summary:     "inspect governance status for the named task",
			Confidence:  86,
			Command:     []string{"governance", "status", "--task", taskID},
			NeedsChoice: true,
		}}, cands...)
	}
	return cands
}

func offlineUtilityCommand(lowered string) ([]string, string, bool) {
	switch {
	case lowered == "verify" || strings.Contains(lowered, "verify current project"):
		return []string{"verify"}, "run the default verifier", true
	case strings.Contains(lowered, "smoke repl routing"):
		return []string{"smoke", "repl-routing", "--new-task"}, "run repl-routing smoke", true
	case strings.Contains(lowered, "smoke coding gate"):
		return []string{"smoke", "coding-gate"}, "run coding-gate smoke", true
	case strings.Contains(lowered, "show action map") || lowered == "actions list":
		return []string{"actions", "list"}, "list bounded CLI actions", true
	case lowered == "git diff" || strings.HasPrefix(lowered, "git diff "):
		return []string{"git", "diff"}, "show git diff", true
	case strings.Contains(lowered, "git history"):
		return []string{"git", "log", "--oneline", "-5"}, "show recent git history", true
	case strings.Contains(lowered, "mcp inspect"):
		return []string{"mcp", "inspect", "repo-inspector"}, "inspect configured MCP server", true
	case strings.Contains(lowered, "llm provider details") || strings.Contains(lowered, "llm show"):
		return []string{"llm", "show", "openrouter"}, "show LLM provider details", true
	default:
		return nil, "", false
	}
}

func isReadOnlyUtilityCommand(command []string) bool {
	if len(command) == 0 {
		return false
	}
	switch command[0] {
	case "verify", "smoke", "actions", "git", "mcp", "llm":
		return true
	default:
		return false
	}
}
