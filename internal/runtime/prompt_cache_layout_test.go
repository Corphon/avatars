package runtime

import (
	"strings"
	"testing"

	"avatars/internal/prompt"
)

func TestAssemblePlannerLLMPrompts_KeepsStablePrefixFirst(t *testing.T) {
	sysIn, userIn := "=== PLANNER CORE RULES (always active — never override) ===\nYou are a project architect.", "User requirement:\nbuild a limiter"
	sys, user := assemblePlannerLLMPrompts(sysIn, userIn, "skill body here", "FEEDBACK: add tests", "=== STRUCTS ===\ntype Bucket struct{}")
	if !strings.HasPrefix(sys, strings.TrimSpace(prompt.PlannerStablePrefix)[:len("=== PLANNER CORE RULES")]) {
		t.Fatalf("system must start with PlannerStablePrefix, got %q", sys[:min(80, len(sys))])
	}
	if strings.Count(sys, "=== PLANNER CORE RULES") != 1 {
		t.Fatalf("must not duplicate PlannerStablePrefix, got %d", strings.Count(sys, "=== PLANNER CORE RULES"))
	}
	if strings.Contains(sys, "skill body") || strings.Contains(sys, "FEEDBACK") || strings.Contains(sys, "type Bucket") {
		t.Fatalf("changing extras must not land in system prompt (prefix cache): %q", sys)
	}
	if !strings.Contains(user, "skill body here") || !strings.Contains(user, "FEEDBACK") || !strings.Contains(user, "type Bucket") {
		t.Fatalf("extras should be on the user turn, got %q", user)
	}
	if !strings.HasPrefix(user, "User requirement") {
		t.Fatalf("user requirement should stay first, got %q", user[:min(40, len(user))])
	}
}

func TestCapBuilderUserPrompt_KeepsPrefixAndTruncatesSuffix(t *testing.T) {
	prefix := "Task: fix Wait to observe the injectable clock\n"
	body := strings.Repeat("x", maxBuilderUserPromptBytes)
	got := capBuilderUserPrompt(prefix + body)
	if !strings.HasPrefix(got, "Task: fix Wait") {
		t.Fatalf("truncated prompt must keep the task prefix, got %q", got[:min(40, len(got))])
	}
	if len(got) <= maxBuilderUserPromptBytes {
		t.Fatal("expected truncation marker after the budget")
	}
	if !strings.Contains(got, "truncated middle to keep the generate turn small") {
		t.Fatal("expected truncation notice")
	}
}

func TestCapBuilderUserPrompt_KeepsTailExtras(t *testing.T) {
	prefix := "Task: " + strings.Repeat("PHASE DUMP ", 3000)
	extras := "LANGUAGE LOCK: This is a Go project.\nTOKEN LIMIT: 16384 max.\n"
	got := capBuilderUserPrompt(prefix + extras)
	if !strings.HasPrefix(got, "Task:") {
		t.Fatalf("must keep task prefix, got %q", got[:min(40, len(got))])
	}
	if !strings.Contains(got, "LANGUAGE LOCK") {
		t.Fatal("tail extras must survive truncation")
	}
	if !strings.Contains(got, "TOKEN LIMIT") {
		t.Fatal("token-limit extra must survive truncation")
	}
}

func TestBuilderPerTaskUserExtrasAppendedAfterTask(t *testing.T) {
	task := "Task: implement limiter\n\nProject context from repository survey:\nnone\n"
	extras := builderPerTaskUserExtras("", "Go", nil, "=== EXISTING API LOCK ===\nfunc Allow()\n", 4096)
	prompt := task + extras
	if idxTask, idxLock := strings.Index(prompt, "Task:"), strings.Index(prompt, "API LOCK"); idxTask < 0 || idxLock < 0 || idxLock < idxTask {
		t.Fatalf("Task must precede API lock extras, prompt=%q", prompt)
	}
}

func TestBuilderPerTaskUserExtras_AllowsCompanionTests(t *testing.T) {
	extras := builderPerTaskUserExtras("", "Go", []string{"breaker.go", "go.mod"}, "", 0)
	if strings.Contains(extras, "ONLY these exact paths") {
		t.Fatal("mandatory paths must not forbid companion test files")
	}
	if !strings.Contains(extras, "Companion test files") {
		t.Fatalf("expected companion-test guidance, got %q", extras)
	}
}
