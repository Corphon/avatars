package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizePhaseDocMarkdown_LayerGluedRisks(t *testing.T) {
	// H3: ### title glued onto ## Risks with no punctuation.
	raw := `# Phase 1
## Tasks
### 1. Scaffold
- [ ] go.mod
### 2. Data Model & Database Layer## Risks
| Risk | Severity |
| SQLite | low |

## Verification
- [ ] go build
`
	out := sanitizePhaseDocMarkdown(raw)
	if strings.Contains(out, "Layer##") {
		t.Fatalf("Layer## glue not split:\n%s", out)
	}
	if !strings.Contains(out, "### 2. Data Model & Database Layer") {
		t.Fatalf("### title should be kept:\n%s", out)
	}
	if !strings.Contains(out, "## Risks") {
		t.Fatalf("Risks not salvaged:\n%s", out)
	}
}

func TestSanitizePhaseDocMarkdown_GluedHeadingSalvage(t *testing.T) {
	raw := `# Phase 1
## Overview
ok

## Scope & Success Criteria

## Tasks
### 1. Models
- [ ] Create Base = declarative_base().## Risks
- leftover risk line

## Verification
- [ ] ok
`
	out := sanitizePhaseDocMarkdown(raw)
	if strings.Contains(out, "declarative_base().##") {
		t.Fatalf("glued heading not cut:\n%s", out)
	}
	if !strings.Contains(out, "## Risks") {
		t.Fatalf("Risks section not salvaged:\n%s", out)
	}
	if !strings.Contains(out, "leftover risk line") {
		t.Fatalf("salvaged body lost:\n%s", out)
	}
}

func TestSanitizePhaseDocMarkdown_GluedHeadingNoParen(t *testing.T) {
	// W2: glue after trailing backtick/word without ')'
	raw := `# Phase 1
## Tasks
### 1. Project Scaffolding & Dependencies
- [ ] Write ` + "`requirements.txt`" + ` with pinned versions: fastapi, alembic, python-dotenv` + "`.## Risks\n" + `| Risk | Severity |
| SQLite | low |

## Verification
- [ ] ok
`
	out := sanitizePhaseDocMarkdown(raw)
	if strings.Contains(out, "dotenv`.##") || strings.Contains(out, "dotenv.##") {
		t.Fatalf("backtick glue not cut:\n%s", out)
	}
	if !strings.Contains(out, "## Risks") {
		t.Fatalf("Risks not salvaged:\n%s", out)
	}
	if !strings.Contains(out, "Write") || !strings.Contains(out, "requirements") {
		t.Fatalf("task prefix should be kept:\n%s", out)
	}
}

func TestPhaseDocHasEmptySuccessCriteria(t *testing.T) {
	empty := "# P\n## Scope & Success Criteria\n\n## Tasks\n- [ ] a\n"
	if !phaseDocHasEmptySuccessCriteria(empty) {
		t.Fatal("expected empty criteria")
	}
	filled := "# P\n## Scope & Success Criteria\n- [ ] health ok\n## Tasks\n- [ ] a\n"
	if phaseDocHasEmptySuccessCriteria(filled) {
		t.Fatal("expected non-empty criteria")
	}
}

func TestSanitizePhaseDocMarkdown_DedupeAndCutGlue(t *testing.T) {
	raw := `# Phase 1 · Scaffold
<!-- avatars:phase-detail=full -->

## Overview
ok

## Tasks
### 1. Skeleton
- [ ] Create app/main.py
### 2. Health Endpoint
- [ ] Add /health
### 3. Verification
- [ ] pytest
### 4. Migration SQL
- [ ] Create migrations/001_init.sql with DDL matching models (for documentation purposes).app/api/v1/__init__.py
- [ ] Add app/core/config.py
### 2. Health Endpoint
- [ ] Add /health again
### 3. Verification
- [ ] pytest again

## Verification
- [ ] health ok
`
	out := sanitizePhaseDocMarkdown(raw)
	if strings.Contains(out, ").app/") {
		t.Fatalf("glued path not cut:\n%s", out)
	}
	if strings.Count(out, "### 2. Health Endpoint") != 1 {
		t.Fatalf("Health Endpoint not deduped:\n%s", out)
	}
	if !strings.Contains(out, "## Verification") {
		t.Fatal("Verification section lost")
	}
	if strings.Contains(out, "pytest again") {
		t.Fatalf("duplicate group content kept:\n%s", out)
	}
}

func TestExtractPhaseGroupChecklist_Dedupe(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	phase := `# Phase 1
## Tasks
### 1. A
- [ ] a1
### 2. Health Endpoint
- [ ] h1
- [ ] h2
### 2. Health Endpoint
- [ ] h1
## Verification
- [ ] ok
`
	path := filepath.Join(wf, "phase1.md")
	_ = os.WriteFile(path, []byte(phase), 0644)
	tasks := extractPhaseGroupChecklist(path)
	nHealth := 0
	for _, tline := range tasks {
		if strings.Contains(strings.ToLower(tline), "health") {
			nHealth++
			if strings.Contains(tline, "[x]") && strings.Contains(tline, "(0/") {
				t.Fatalf("false [x] with (0/N): %s", tline)
			}
		}
	}
	if nHealth != 1 {
		t.Fatalf("health rows=%d tasks=%v", nHealth, tasks)
	}
}

func TestFormatChecklistSection_NoFalseComplete(t *testing.T) {
	tasks := []string{"- [ ] 4. Basic Models (0/4)"}
	completed := []string{"4. Basic Models (4/4)"}
	out := formatChecklistSection("1", tasks, completed, true)
	if strings.Contains(out, "- [x] 4. Basic Models (0/4)") {
		t.Fatalf("wasCompleted upgraded incomplete count:\n%s", out)
	}
}

func TestCountTodoProgress_CountsBookmark(t *testing.T) {
	todo := `> **Phase**: 1
## Phase 1 Checklist (active)
- [>] 1. Project Scaffolding (0/2)
- [ ] 2. Health Endpoint (0/1)
## Phase 2 Checklist
- [ ] 1. Auth (0/1)
`
	completed, total := CountTodoProgressFromContent(todo)
	if total != 2 {
		t.Fatalf("total=%d want 2 (include [>])", total)
	}
	if completed != 0 {
		t.Fatalf("completed=%d", completed)
	}
}

func TestPlanStructurePathsOK(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "app", "core"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "app", "models"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "migrations"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "tests"), 0755)
	desc := "project structure matches (app/main.py, app/core/, app/models/, app/api/, app/db/, migrations/, tests/)."
	if PlanStructurePathsOK(root, desc) {
		t.Fatal("missing app/api and app/db should fail")
	}
	_ = os.MkdirAll(filepath.Join(root, "app", "api"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "app", "db"), 0755)
	_ = os.WriteFile(filepath.Join(root, "app", "main.py"), []byte("x"), 0644)
	if !PlanStructurePathsOK(root, desc) {
		t.Fatal("expected ok when all paths exist")
	}
}

func TestCriteriaLooksLikeCoverageDesc(t *testing.T) {
	if !criteriaLooksLikeCoverageDesc("pytest suite passes with >80% code coverage") {
		t.Fatal("expected coverage detect")
	}
}

func TestSuccessCriteriaPhaseNum(t *testing.T) {
	if got := SuccessCriteriaPhaseNum("- [ ] Phase 5: Test coverage ≥80%"); got != 5 {
		t.Fatalf("got %d", got)
	}
	if got := SuccessCriteriaPhaseNum("phase 1: health ok"); got != 1 {
		t.Fatalf("got %d", got)
	}
	if got := SuccessCriteriaPhaseNum("all tests pass"); got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestTryAutoMarkPlanCriteria_ActivePhaseOnly(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `# Project Plan
> **Active Phase**: 1
> **Phase Count**: 5

## Goals
- g

## Phases
### Phase 1: A
- **Goal**: a
- **Status**: pending
### Phase 5: Polish
- **Goal**: tests
- **Status**: pending

## Success Criteria
- [ ] Phase 1: FastAPI /health returns ok
- [ ] Phase 5: Test coverage ≥80%, all endpoints documented
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	n := TryAutoMarkPlanCriteria(root, "health ok", []string{"app/main.py", "tests/test_health.py"}, true, "build fastapi /health")
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	s := string(body)
	if !strings.Contains(s, "- [x] Phase 1:") && n == 0 {
		// may or may not mark phase1 depending on candidate match; Phase 5 must stay open
	}
	if strings.Contains(s, "- [x] Phase 5:") {
		t.Fatalf("Phase 5 must not be auto-marked:\n%s", s)
	}
	if !strings.Contains(s, "- **Status**: in-progress") && strings.Contains(s, "- [x] Phase 1:") {
		t.Fatalf("Active Phase Status should become in-progress when criteria marked:\n%s", s)
	}
}
