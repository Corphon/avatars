package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhaseDocCoversUserRoutes(t *testing.T) {
	user := `## Phase 1
POST /rooms/{id}/slots
GET /rooms/{id}/slots
DELETE /slots/{id}
## Phase 2
PATCH /slots/{id}
`
	good := `## Scope
- [ ] POST /rooms/{id}/slots books a slot
- [ ] GET /rooms/{id}/slots lists bookings
- [ ] DELETE /slots/{id}
`
	if !phaseDocCoversUserRoutes(good, user, 1) {
		t.Fatal("good phase should cover nested routes")
	}
	bad := `## Scope
- [ ] POST /slots with room_id
- [ ] DELETE /slots/{id}
`
	if phaseDocCoversUserRoutes(bad, user, 1) {
		t.Fatal("flattened POST /slots must not cover nested /rooms/{id}/slots requirement")
	}
}

func TestPersistAndLoadUserRequirement(t *testing.T) {
	dir := t.TempDir()
	if err := PersistUserRequirement(dir, "SLOTBOOK_API_KEY auth"); err != nil {
		t.Fatal(err)
	}
	got := LoadUserRequirement(dir)
	if got != "SLOTBOOK_API_KEY auth" {
		t.Fatalf("got %q", got)
	}
}

func TestPlanMismatchesLiveTask_HelpPlanVsLibrary(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Project**: Add a --help flag to the CLI tool
> **Phase Count**: 1
## Goals
- The CLI tool prints usage information and exits successfully when invoked with --help.
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PersistUserRequirement(dir, "--help"); err != nil {
		t.Fatal(err)
	}
	goLib := "创建一个纯 Go 开源库，模块名 retrybudget。进程内带预算的重试工具库。"
	if !PlanMismatchesLiveTask(dir, goLib) {
		t.Fatal("F98: leftover --help plan must mismatch retrybudget library brief")
	}
	pyLib := "Create a Python package named retrybudget with pytest coverage."
	if !PlanMismatchesLiveTask(dir, pyLib) {
		t.Fatal("F98: leftover --help plan must mismatch Python package brief")
	}
	jsLib := "Build a TypeScript library named retrybudget; npm test must pass."
	if !PlanMismatchesLiveTask(dir, jsLib) {
		t.Fatal("F98: leftover --help plan must mismatch TypeScript library brief")
	}
	if PlanMismatchesLiveTask(dir, "接着修到能过 go test") {
		t.Fatal("short continue prompt must not rebuild the plan")
	}
}

func TestPlanMismatchesLiveTask_SameProductContinues(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Project**: retrybudget in-process retry library
## Goals
- Implement retrybudget with MaxElapsed and jitter.
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	live := "完善 retrybudget：补 Peek API，保持 go test 绿。"
	if PlanMismatchesLiveTask(dir, live) {
		t.Fatal("same-product follow-up must not look like a mismatched plan")
	}
}

func TestNormalizeCheckedZeroCounts(t *testing.T) {
	in := "## Phase 2 Checklist (active)\n- [x] 1. API-Key Authentication (0/2)\n- [ ] other\n"
	out := normalizeCheckedZeroCounts(in)
	if !strings.Contains(out, "(2/2)") {
		t.Fatalf("expected (2/2), got %q", out)
	}
}

func TestEnvConstraintPrompt_PrefersUserEnvName(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 2
> **Phase Count**: 2
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase2.md"), []byte("# Phase 2\nAPI key auth with API_KEYS defaulting to dev-key.\n"), 0644)
	_ = PersistUserRequirement(dir, "## Phase 2\nUse SLOTBOOK_API_KEY with X-API-Key header\n")
	got := EnvConstraintPrompt("auth phase", dir)
	if !strings.Contains(got, "SLOTBOOK_API_KEY") {
		t.Fatalf("should prefer user env name, got %q", got)
	}
	if strings.Contains(got, "API_KEYS") && !strings.Contains(got, "SLOTBOOK") {
		t.Fatalf("must not lock only API_KEYS, got %q", got)
	}
}

func TestPhaseTasksCoverScope_RequiresEightyPercent(t *testing.T) {
	// 2 of 3 covered ≈ 66% — must fail under 80% gate (C2).
	partial := `# Phase 1
## Scope & Success Criteria
- [ ] Add API key auth middleware for write endpoints
- [ ] DELETE /polls/{id} returns 204
- [ ] PATCH /polls/{id} updates title
## Tasks
### 1. Auth
- [ ] Implement API key auth middleware on write endpoints
### 2. Delete
- [ ] DELETE /polls/{id} handler returns 204
`
	if phaseTasksCoverScope(partial) {
		t.Fatal("66% coverage must fail 80% B10/C2 gate")
	}
}
