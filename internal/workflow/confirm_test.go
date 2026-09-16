package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsPhaseDocEmpty_TemplateIsEmpty(t *testing.T) {
	templatePath := filepath.Join("templates", "phase_template.md")
	data, err := os.ReadFile(templatePath)
	if err != nil {
		// Fall back to inline copy of the embedded markers when cwd is not package dir.
		data = []byte(`# Phase N · [Phase Name] ([estimated duration])
## Tasks
### 1. [Task Group Name]
- [ ] [Specific, actionable task — name exact files or endpoints]
- [ ] [Specific, actionable task]
`)
	}
	if !IsPhaseDocEmpty(string(data)) {
		t.Fatal("preload/plan_builder phase template must count as empty")
	}
}

func TestIsPhaseDocEmpty_FilledPhase(t *testing.T) {
	filled := `# Phase 1 · Scaffold REST API (1 week)

## Overview
Stand up a Go module with PostgreSQL connectivity and a health endpoint.

## Tasks
### 1. Project scaffold
- [ ] Create go.mod and cmd/server/main.go
- [ ] Add PostgreSQL connection helper

### 2. First endpoint
- [ ] Implement GET /health
`
	if IsPhaseDocEmpty(filled) {
		t.Fatal("filled phase with real task checkboxes must not count as empty")
	}
}

func TestIsPhaseDocEmpty_CheckboxWithoutTasksStillEmpty(t *testing.T) {
	// Scope checkboxes alone (outside ## Tasks) must not satisfy the gate —
	// that was the ConfirmPlan false-positive shape when only "- [ ]" was checked.
	body := `# Phase 1
## Scope & Success Criteria
- [ ] ship something
## Overview
done enough
`
	if !IsPhaseDocEmpty(body) {
		t.Fatal("checkboxes outside Tasks section must not count as a filled phase")
	}
}

func TestPhaseDocsPresent_RejectsTemplateOnDisk(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	template := `# Phase N · [Phase Name] ([estimated duration])
## Tasks
### 1. [Task Group Name]
- [ ] [Verifiable task-level outcome]
- [ ] [Specific, actionable task]
`
	if err := os.WriteFile(filepath.Join(docs, "phase1.md"), []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	if phaseDocsPresent(tmp) {
		t.Fatal("phaseDocsPresent must be false for placeholder phase1.md")
	}
	if !IsPhaseEmpty(tmp) {
		t.Fatal("IsPhaseEmpty must be true for placeholder phase1.md")
	}
}

func TestAllPhaseDocsPresent_RequiresEveryPhaseFile(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Phase Count**: 3
> **Active Phase**: 1

## Goals
- Build API

### Phase 1: Core
- **Goal**: Endpoints
- **Detail**: docs/workflow/phase1.md

### Phase 2: Storage
- **Goal**: DB
- **Detail**: docs/workflow/phase2.md

### Phase 3: Auth
- **Goal**: JWT
- **Detail**: docs/workflow/phase3.md
`
	filled := `# Phase 1 · Core (1d)
## Overview
Real.
## Tasks
### 1. API
- [ ] Create go.mod
## Verification
- [ ] go test ./...
`
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("avatars_plan.md", plan)
	write("phase1.md", filled)

	if allPhaseDocsPresent(tmp) {
		t.Fatal("allPhaseDocsPresent must be false when phase2/3 are missing")
	}
	missing, err := missingPhaseNumbers(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 || missing[0] != 2 || missing[1] != 3 {
		t.Fatalf("missing phases = %v, want [2 3]", missing)
	}
}

func TestConfirmPlan_GeneratesMissingLaterPhases(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Status**: draft
> **Active Phase**: 1
> **Phase Count**: 3

## Goals
- Build API

### Phase 1: Core
- **Goal**: Endpoints
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

### Phase 2: Storage
- **Goal**: DB
- **Status**: pending
- **Detail**: docs/workflow/phase2.md

### Phase 3: Auth
- **Goal**: JWT
- **Status**: pending
- **Detail**: docs/workflow/phase3.md

## Success Criteria
- [ ] API works
`
	filled := `# Phase 1 · Core (1d)
## Overview
Already done.
## Tasks
### 1. API
- [ ] Create go.mod
## Verification
- [ ] go test ./...
`
	todo := `# Active Workspace
> **Phase**: 1
## Phase 1 Checklist
- [ ] 1. API (0/1)
`
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("avatars_plan.md", plan)
	write("phase1.md", filled)
	write("avatars_todo.md", todo)
	write("process_record.md", "# Record\n")

	calls := 0
	summary, err := ConfirmPlan(tmp, func(sys, usr string) (string, error) {
		calls++
		return `# Phase should not be requested for future phases
## Overview
x
## Tasks
### 1. X
- [ ] y
## Verification
- [ ] z
`, nil
	})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	// R1 layered: phase1 already full → 0 LLM calls; phase2/3 deferred stubs.
	if calls != 0 {
		t.Fatalf("want 0 LLM phase generations (later phases stubbed), got %d", calls)
	}
	if !strings.Contains(summary, "Deferred stubs") && !strings.Contains(summary, "layered") {
		t.Fatalf("unexpected summary: %s", summary)
	}
	for _, n := range []string{"phase2.md", "phase3.md"} {
		data, err := os.ReadFile(filepath.Join(docs, n))
		if err != nil {
			t.Fatalf("%s missing: %v", n, err)
		}
		if IsPhaseDocEmpty(string(data)) {
			t.Fatalf("%s still empty:\n%s", n, data)
		}
		if !IsPhaseDocDeferredStub(string(data)) {
			t.Fatalf("%s should be deferred stub:\n%s", n, data)
		}
	}
	p1, _ := os.ReadFile(filepath.Join(docs, "phase1.md"))
	if IsPhaseDocDeferredStub(string(p1)) {
		t.Fatal("phase1 should remain full detail, not stub")
	}
}

func TestConfirmPlan_RegeneratesWhenPhaseIsTemplate(t *testing.T) {
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
- Build a Go REST API with PostgreSQL

### Phase 1: Foundation
- **Goal**: Scaffold and health endpoint
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

## Success Criteria
- [ ] Health endpoint returns 200
`
	phaseTemplate := `# Phase N · [Phase Name] ([estimated duration])
## Tasks
### 1. [Task Group Name]
- [ ] [Specific, actionable task — name exact files or endpoints]
- [ ] [Specific, actionable task]
`
	todo := `# Active Workspace
> **Phase**: 1
## Current Task
- [ ] Define phase 1 tasks in docs/workflow/phase1.md
## Phase 1 Checklist
- [ ] 1. [Task Group Name] (0/2)
`
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("avatars_plan.md", plan)
	write("phase1.md", phaseTemplate)
	write("avatars_todo.md", todo)
	write("process_record.md", "# Record\n")

	called := false
	summary, err := ConfirmPlan(tmp, func(sys, usr string) (string, error) {
		called = true
		return `# Phase 1 · Foundation (1d)

## Overview
Scaffold the Go REST API.

## Tasks
### 1. Scaffold
- [ ] Create go.mod
- [ ] Add main.go with /health

## Verification
- [ ] go build ./... passes
`, nil
	})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	if !called {
		t.Fatal("ConfirmPlan must call generate when phase1.md is still a template")
	}
	if !strings.Contains(summary, "Generated") && !strings.Contains(summary, "phase") {
		t.Fatalf("unexpected summary: %s", summary)
	}
	out, err := os.ReadFile(filepath.Join(docs, "phase1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if IsPhaseDocEmpty(string(out)) {
		t.Fatalf("phase1.md still empty after ConfirmPlan:\n%s", out)
	}
}

func TestConfirmPlan_ReusesFilledPhase(t *testing.T) {
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
- Build API

### Phase 1: Foundation
- **Goal**: Scaffold
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

## Success Criteria
- [ ] Health works
`
	filled := `# Phase 1 · Foundation (1d)
## Overview
Real content.
## Tasks
### 1. Scaffold
- [ ] Create go.mod
- [ ] Add /health
## Verification
- [ ] go test ./...
`
	todo := `# Active Workspace
> **Phase**: 1
## Current Task
- [ ] Create go.mod
## Phase 1 Checklist
- [ ] 1. Scaffold (0/2)
`
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("avatars_plan.md", plan)
	write("phase1.md", filled)
	write("avatars_todo.md", todo)
	write("process_record.md", "# Record\n")

	called := false
	summary, err := ConfirmPlan(tmp, func(sys, usr string) (string, error) {
		called = true
		return "should not be used", nil
	})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	if called {
		t.Fatal("ConfirmPlan must reuse filled phase1.md without calling generate")
	}
	if !strings.Contains(summary, "Reused") {
		t.Fatalf("expected reuse summary, got: %s", summary)
	}
}

func TestWantsImplementAfterConfirm(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"确认计划", false},
		{"确认计划，然后开始写代码实现", true},
		{"confirm the plan then implement phase 1", true},
		{"批准计划并开始实现", true},
		{"hello world", false},
	}
	for _, tc := range cases {
		if got := WantsImplementAfterConfirm(tc.in); got != tc.want {
			t.Errorf("WantsImplementAfterConfirm(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
