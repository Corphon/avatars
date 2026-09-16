package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfirmPlan_HardStopsWhenLLMFailsAndPhaseEmpty(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Status**: draft
> **Active Phase**: 1
> **Phase Count**: 2

## Goals
- Build pollbox API

### Phase 1: Core
- **Goal**: Ship health + polls
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

### Phase 2: Auth
- **Goal**: API key + DELETE
- **Status**: pending
- **Detail**: docs/workflow/phase2.md

## Success Criteria
- [ ] go test ./... green
`
	template := `# Phase N · [Phase Name] ([estimated duration])
## Overview
[2-3 sentences describing what this phase accomplishes and why it comes at this point in the project.]
## Tasks
### 1. [Task Group Name]
- [ ] [Verifiable task-level outcome]
- [ ] [Specific, actionable task]
## Verification
- [ ] [Verification step — e.g. "go build ./... passes"]
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "phase1.md"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "phase2.md"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "avatars_todo.md"), []byte("# Todo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	summary, err := ConfirmPlan(tmp, func(sys, usr string) (string, error) {
		return "", fmt.Errorf(`openai-compatible provider "deepseek" returned 503: Service is too busy`)
	})
	if err == nil {
		t.Fatal("ConfirmPlan must hard-stop when Active Phase stays empty after LLM failure")
	}
	if !strings.Contains(summary, "FAILED") && !strings.Contains(summary, "reverted") {
		t.Fatalf("summary should say confirm failed/reverted, got: %s", summary)
	}
	if strings.Contains(summary, "Reused") && strings.Contains(summary, "detailed") {
		t.Fatalf("must not claim reused detailed for empty templates:\n%s", summary)
	}
	data, _ := os.ReadFile(filepath.Join(docs, "avatars_plan.md"))
	if strings.Contains(string(data), "in-progress") {
		t.Fatalf("status must revert to draft after hard-stop:\n%s", data)
	}
	if !strings.Contains(string(data), "draft") {
		t.Fatalf("expected draft status:\n%s", data)
	}
}

func TestConfirmPlan_SoftDegradeWhenFilledPhaseExists(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Status**: draft
> **Active Phase**: 1
> **Phase Count**: 1

## Goals
- Build tool

### Phase 1: Core
- **Goal**: Ship
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

## Success Criteria
- [ ] done
`
	filled := `# Phase 1 · Core (1d)
<!-- avatars:phase-detail=full -->

## Overview
Already filled detail from a prior successful confirm.

## Tasks
### 1. Scaffold
- [ ] Create go.mod
- [ ] Add main.go

## Verification
- [ ] go build ./... passes
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "phase1.md"), []byte(filled), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "avatars_todo.md"), []byte("# Todo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// ConstructPhasesLayered will not call LLM when phase1 is already full.
	summary, err := ConfirmPlan(tmp, func(sys, usr string) (string, error) {
		return "", fmt.Errorf("LLM unavailable: connection refused")
	})
	if err != nil {
		t.Fatalf("filled Active Phase should confirm without LLM: %v\n%s", err, summary)
	}
	if !strings.Contains(summary, "Reused") && !strings.Contains(summary, "activated") {
		t.Fatalf("expected reuse/activate summary, got: %s", summary)
	}
	data, _ := os.ReadFile(filepath.Join(docs, "avatars_plan.md"))
	if !strings.Contains(string(data), "in-progress") {
		t.Fatalf("status should be in-progress:\n%s", data)
	}
}

func TestConfirmPlan_RejectsPlaceholderPlan(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Status**: draft
> **Active Phase**: 1
> **Phase Count**: 2

## Goals
- Complete mid_go_nl_p2_r5 — what must be true when this is done]

### Phase 1: [Name]
- **Goal**: [One-line goal]
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

## Success Criteria
- [ ] [Measurable — how do we know this is done?]
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ConfirmPlan(tmp, func(sys, usr string) (string, error) {
		t.Fatal("generate must not be called for placeholder plan")
		return "", nil
	})
	if err == nil {
		t.Fatal("expected error for placeholder plan")
	}
	if !strings.Contains(err.Error(), "template placeholders") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestListFilledDetailedPhaseDocs_SkipsTemplates(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Phase Count**: 2
### Phase 1: A
- **Detail**: docs/workflow/phase1.md
### Phase 2: B
- **Detail**: docs/workflow/phase2.md
`
	_ = os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(docs, "phase1.md"), []byte(`# Phase N · [Phase Name] ([estimated duration])
## Tasks
### 1. [Task Group Name]
- [ ] [Verifiable task-level outcome]
`), 0644)
	_ = os.WriteFile(filepath.Join(docs, "phase2.md"), []byte(`# Phase 2 · Auth
## Overview
API key auth.
## Tasks
### 1. Auth
- [ ] Add X-API-Key middleware
## Verification
- [ ] 401 without key
`), 0644)
	got := listFilledDetailedPhaseDocs(tmp)
	if len(got) != 1 || got[0] != "phase2.md" {
		t.Fatalf("got %v, want [phase2.md]", got)
	}
}
