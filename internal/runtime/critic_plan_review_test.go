package runtime

import (
	"strings"
	"testing"
)

func TestBuildPlanReviewUserPrompt_IncludesPlan(t *testing.T) {
	plan := "## Goals\n- Build auth system\n## Phases\n### Phase 1: Setup\n- Initialize project"
	survey := "Project uses Go 1.21, go.mod exists at root."

	result := buildPlanReviewUserPrompt(plan, survey)

	if !strings.Contains(result, "Plan to review") {
		t.Fatal("prompt should contain plan section header")
	}
	if !strings.Contains(result, "## Goals") {
		t.Fatal("prompt should contain plan content")
	}
	if !strings.Contains(result, "Survey findings from Researcher") {
		t.Fatal("prompt should contain survey section")
	}
	if !strings.Contains(result, "Go 1.21") {
		t.Fatal("prompt should contain survey content")
	}
	if !strings.Contains(result, "BLOCKER") || !strings.Contains(result, "WARNING") {
		t.Fatal("prompt should instruct LLM to use severity tags")
	}
}

func TestBuildPlanReviewUserPrompt_TruncatesLongPlan(t *testing.T) {
	// Create a plan longer than maxPlanChars (6000).
	longPlan := strings.Repeat("This is a very long plan line that repeats.\n", 500)
	result := buildPlanReviewUserPrompt(longPlan, "")

	if len(result) > 10000 {
		t.Fatalf("prompt should be truncated, got %d chars", len(result))
	}
	if !strings.Contains(result, "truncated for review") {
		t.Fatal("prompt should indicate that plan was truncated")
	}
}

func TestBuildPlanReviewUserPrompt_EmptySurvey(t *testing.T) {
	result := buildPlanReviewUserPrompt("## Goals\n- test", "")
	if strings.Contains(result, "Survey findings") {
		t.Fatal("prompt should not include survey section when empty")
	}
}

func TestBuildPlanReviewUserPrompt_EmptyPlan(t *testing.T) {
	// buildPlanReviewUserPrompt doesn't guard against empty plan —
	// the guard is in criticReviewPlanContent which checks before calling.
	// With an empty plan, we still get the instruction prompt.
	result := buildPlanReviewUserPrompt("", "some survey")
	if !strings.Contains(result, "Review the plan above") {
		t.Fatal("prompt should contain review instruction even with empty plan")
	}
}

func TestCriticReviewPlan_FormatChecks(t *testing.T) {
	// This tests the existing format-only checks (no LLM needed).
	// The function requires a working directory with plan docs.
	// We just verify it doesn't panic with invalid paths.
	work := workflowNodeWorkContext{
		readSummary: "Test survey findings",
	}
	// This will likely fail to read plan (no docs/workflow/ dir in test),
	// but it should return a warning string rather than panic.
	result := criticReviewPlan(work)
	if result == "" {
		t.Fatal("expected non-empty result (at minimum a warning about missing plan)")
	}
	if !strings.Contains(result, "Pre-build plan review") {
		t.Fatal("result should start with pre-build plan review header")
	}
}
