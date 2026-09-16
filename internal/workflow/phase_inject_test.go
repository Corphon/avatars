package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactPhaseDocForPrompt_KeepsChecklistsDropsProse(t *testing.T) {
	body := `# Phase 1 · Core
<!-- avatars:phase-detail=full -->
## Overview
This is a long strategy paragraph about retry backoff, jitter, and why
the package should exist. It must not bloat the Builder user prompt.

## Tasks
- [ ] Create retryx.go
- [ ] Write tests in retryx_test.go
## Verification
- [ ] go test ./...
`
	got := CompactPhaseDocForPrompt(body, 2200)
	if !strings.Contains(got, "Create retryx.go") || !strings.Contains(got, "go test") {
		t.Fatalf("checklist lines must stay, got:\n%s", got)
	}
	if strings.Contains(got, "long strategy paragraph") {
		t.Fatalf("overview prose must be dropped, got:\n%s", got)
	}
	if strings.Contains(got, "avatars:phase-detail") {
		t.Fatalf("html comments must be dropped, got:\n%s", got)
	}
}

func TestStripOtherPhaseSections_KeepsOnlyRequestedPhase(t *testing.T) {
	input := `# Snippetbox

## Phase 1
Build CRUD create/list.

## Phase 2
Add API key and DELETE.

## Notes
Shared note.
`
	got := StripOtherPhaseSections(input, 1)
	if !strings.Contains(got, "Build CRUD") {
		t.Fatalf("expected phase 1 body, got:\n%s", got)
	}
	if strings.Contains(got, "API key") || strings.Contains(got, "## Phase 2") {
		t.Fatalf("phase 2 must be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, "Snippetbox") {
		t.Fatalf("preamble should be kept, got:\n%s", got)
	}

	got2 := StripOtherPhaseSections(input, 2)
	if !strings.Contains(got2, "API key") {
		t.Fatalf("expected phase 2 body, got:\n%s", got2)
	}
	if strings.Contains(got2, "Build CRUD") {
		t.Fatalf("phase 1 must be stripped when keep=2, got:\n%s", got2)
	}
}

func TestStripOtherPhaseSections_ChineseHeadings(t *testing.T) {
	input := "## 阶段 1\n做基础\n\n## 阶段 2\n做鉴权\n"
	got := StripOtherPhaseSections(input, 1)
	if strings.Contains(got, "鉴权") {
		t.Fatalf("stage 2 should be stripped: %s", got)
	}
	if !strings.Contains(got, "基础") {
		t.Fatalf("stage 1 kept: %s", got)
	}
}

func TestInjectPhasePrompt_UsesPhaseDocAndStripsFuture(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Active Phase**: 1
> **Phase Count**: 2

## Phases
### Phase 1: Core
- **Goal**: CRUD and health
- **Detail**: docs/workflow/phase1.md
### Phase 2: Auth
- **Goal**: API key and DELETE
- **Detail**: docs/workflow/phase2.md
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	phase1 := `# Phase 1 · Core
<!-- avatars:phase-detail=full -->
## Overview
Core delivery.
## Tasks
- [ ] Create handlers.go
## Verification
- [ ] build passes
`
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase1), 0644); err != nil {
		t.Fatal(err)
	}
	orig := "## Phase 1\nCRUD\n\n## Phase 2\nDELETE and API-Key\n"
	out := InjectPhasePrompt(orig, root, 1)
	if !strings.Contains(out, "handlers.go") {
		t.Fatalf("must inject phase1.md tasks, got:\n%s", out)
	}
	if !strings.Contains(out, "USER REQUEST (this phase only)") {
		t.Fatalf("missing scoped request header:\n%s", out)
	}
	if strings.Contains(out, "DELETE and API-Key") {
		t.Fatalf("future phase must not appear in user request section:\n%s", out)
	}
}

func TestChecklistItemHasPathEvidence_CrossLang(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "handlers.py"), []byte("def health(): pass\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if !checklistItemHasPathEvidence(root, "Implement HTTP Handlers (`handlers.py`)") {
		t.Fatal("expected handlers.py evidence")
	}
	if !checklistItemHasPathEvidence(root, "npm init / package.json") {
		t.Fatal("expected package.json evidence")
	}
	if checklistItemHasPathEvidence(root, "Create missing_file.rs") {
		t.Fatal("missing file must not count")
	}
}

func TestCriteriaOverlapsLaterPhaseGoals(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 2
## Phases
### Phase 1: Core
- **Goal**: health endpoint and create snippets
### Phase 2: Auth
- **Goal**: API-Key middleware and DELETE endpoint
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	if !criteriaOverlapsLaterPhaseGoals(root, "API-Key middleware protects DELETE", 1) {
		t.Fatal("expected later-phase overlap")
	}
	if criteriaOverlapsLaterPhaseGoals(root, "health endpoint returns ok", 1) {
		t.Fatal("active-phase criteria must not be treated as later")
	}
}

func TestMarkPhaseChecklistByPathEvidence(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.MkdirAll(filepath.Join(root, "internal"), 0755)
	_ = os.WriteFile(filepath.Join(root, "internal", "handlers.go"), []byte("package internal\n"), 0644)
	plan := `> **Active Phase**: 1
> **Phase Count**: 2
## Phases
### Phase 1: Core
- **Goal**: CRUD handlers
### Phase 2: Auth
- **Goal**: API-Key and DELETE
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	phase := `# Phase 1
## Scope & Success Criteria
- [ ] API-Key middleware and DELETE endpoint
- [ ] handlers.go serves health
## Tasks
### 1. Handlers
- [ ] Create HTTP Handlers (` + "`handlers.go`" + `)
- [ ] Missing file (` + "`nope.go`" + `)
`
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase), 0644)
	n := MarkPhaseChecklistByPathEvidence(root, 1)
	if n < 1 {
		t.Fatalf("expected at least handlers task marked, got %d", n)
	}
	body, _ := os.ReadFile(filepath.Join(wf, "phase1.md"))
	text := string(body)
	if !strings.Contains(text, "- [x] Create HTTP Handlers") {
		t.Fatalf("handlers task not marked:\n%s", text)
	}
	if strings.Contains(text, "- [x] Missing file") {
		t.Fatalf("missing file must stay unchecked:\n%s", text)
	}
	// Later-phase criteria should not be marked via path alone when no path.
	if strings.Contains(text, "- [x] API-Key") {
		t.Fatalf("later-phase success criteria must not be marked:\n%s", text)
	}
}
