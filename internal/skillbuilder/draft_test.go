package skillbuilder

import (
	"strings"
	"testing"

	"avatars/internal/planner"
)

func TestBuildWithDraft_AppliesBoundedLLMValues(t *testing.T) {
	plan := planner.Build("Analyze the current repository and propose a refactoring plan")
	proposal := BuildWithDraft("Analyze the current repository and propose a refactoring plan", plan, "Read process_record.md successfully.", Draft{
		Description: "LLM draft description for task survey skill",
		WhenToUse:   "LLM draft when the runtime needs a readable task survey before broader execution",
		Body: strings.Join([]string{
			"## Purpose",
			"Draft purpose",
			"",
			"## Task Focus",
			"Draft focus",
			"",
			"## Repo Survey Summary",
			"Draft summary",
			"",
			"## Suggested Workflow",
			"1. Draft step",
			"",
			"## Constraints",
			"- Draft constraint",
		}, "\n"),
	})

	if proposal.Description != "LLM draft description for task survey skill" {
		t.Fatalf("expected draft description to be applied, got %q", proposal.Description)
	}
	if proposal.WhenToUse != "LLM draft when the runtime needs a readable task survey before broader execution" {
		t.Fatalf("expected draft when_to_use to be applied, got %q", proposal.WhenToUse)
	}
	if proposal.Body != strings.Join([]string{
		"## Purpose",
		"Draft purpose",
		"",
		"## Task Focus",
		"Draft focus",
		"",
		"## Repo Survey Summary",
		"Draft summary",
		"",
		"## Suggested Workflow",
		"1. Draft step",
		"",
		"## Constraints",
		"- Draft constraint",
	}, "\n") {
		t.Fatalf("expected valid draft body to be applied, got %q", proposal.Body)
	}
}

func TestTaskSurveyBodyLooksValid_RequiresSections(t *testing.T) {
	if TaskSurveyBodyLooksValid("Draft only") {
		t.Fatal("expected incomplete body to be rejected")
	}
}

func TestTaskSurveyTemplateText_UsesExternalTemplateOrFallback(t *testing.T) {
	if got := TaskSurveyTemplateText(); strings.TrimSpace(got) == "" {
		t.Fatal("expected template text to be available")
	}
}
