package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	memstore "avatars/internal/memory"
)

func TestS2_SyncTodoSamePhasePreservesMarks(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Plan
> **Active Phase**: 1
> **Status**: in-progress
`
	// L1: phase children must be [x] for group row to stay [x]; preserving
	// [x] with (0/N) from stale todo was the false-complete bug.
	phase := `# Phase 1
## Tasks
### 1. First group
- [x] a
- [x] b
### 2. Second group
- [ ] c
`
	todo := `# Todo
> **Phase**: 1 of 2
## Phase 1 Checklist
- [x] 1. First group (2/2)
- [ ] 2. Second group (0/1)

## Blockers
- [ ] something blocked
`
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("avatars_plan.md", plan)
	write("phase1.md", phase)
	write("avatars_todo.md", todo)

	if err := SyncTodoFromPlan(tmp); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(docs, "avatars_todo.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if !strings.Contains(body, "- [x] 1. First group") {
		t.Fatalf("same-phase sync wiped [x] mark:\n%s", body)
	}
	if strings.Contains(body, "- [x] 2. Second group (0/") {
		t.Fatalf("must not keep false [x] with (0/N):\n%s", body)
	}
	if !strings.Contains(body, "## Blockers") || !strings.Contains(body, "- [ ] something blocked") {
		t.Fatalf("blockers section corrupted:\n%s", body)
	}
}

func TestS2_TruncationPlaceholderNotCounted(t *testing.T) {
	todo := `## Phase 1 Checklist
- [x] one
- [x] two
> (+9 more items — see full plan in phase1.md)
- [ ] three
`
	completed, total := CountTodoProgressFromContent(todo)
	if total != 3 || completed != 2 {
		t.Fatalf("completed=%d total=%d, want 2/3 (placeholder ignored)", completed, total)
	}
}

func TestS2_MarkRemainingScopedToChecklist(t *testing.T) {
	todo := `## Current Task
- [ ] do not touch

## Phase 1 Checklist
- [ ] go build
- [ ] go test

## Blockers
- [ ] keep open
`
	out := markRemainingAsDeferredByCritic(todo)
	if !strings.Contains(out, "## Current Task\n- [ ] do not touch") {
		t.Fatalf("Current Task was marked:\n%s", out)
	}
	if !strings.Contains(out, "## Blockers\n- [ ] keep open") {
		t.Fatalf("Blockers was marked:\n%s", out)
	}
	if !strings.Contains(out, "- [x] go build (verified by Critic)") {
		t.Fatalf("checklist item not marked:\n%s", out)
	}
}

func TestS2_UpdateActivePhaseStatusMigration(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Plan
> **Active Phase**: 1

## Phases
### Phase 1: Bootstrap
- **Status**: in-progress
- **Goal**: g1

### Phase 2: Build
- **Status**: pending
- **Goal**: g2
`
	if err := os.WriteFile(filepath.Join(docs, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateActivePhase(tmp, 2); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(docs, "avatars_plan.md"))
	body := string(out)
	if !strings.Contains(body, "> **Active Phase**: 2") {
		t.Fatalf("active phase not updated:\n%s", body)
	}
	if !strings.Contains(body, "### Phase 1: Bootstrap\n- **Status**: completed") {
		t.Fatalf("phase1 not completed from in-progress:\n%s", body)
	}
	if !strings.Contains(body, "### Phase 2: Build\n- **Status**: in-progress") {
		t.Fatalf("phase2 not in-progress:\n%s", body)
	}
}

func TestS2_RegenerateRecordMarkdownFromSQLite(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := memstore.NewSQLiteStore(filepath.Join(tmp, ".avatars", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	SetMemoryStore(store)
	_ = store.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "workflow",
		RunID:     "workflow",
		TaskID:    "t1",
		Kind:      "task_completion",
		Verdict:   "PASS",
		Summary:   "built hello.go successfully",
		Source:    "test",
	})
	if err := RegenerateRecordMarkdownFromSQLite(tmp); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(docs, "process_record.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if !strings.Contains(body, "Export-only") {
		t.Fatalf("missing export header:\n%s", body)
	}
	if !strings.Contains(body, "built hello.go") {
		t.Fatalf("missing evaluation export:\n%s", body)
	}
}

func TestS2_PhaseChangeRebuildsChecklist(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("avatars_plan.md", "> **Active Phase**: 2\n> **Phase Count**: 2\n")
	write("phase1.md", "## Tasks\n### 1. Old work\n- [x] a\n")
	write("phase2.md", "## Tasks\n### 1. New work\n- [ ] x\n### 2. More\n- [ ] y\n")
	write("avatars_todo.md", "> **Phase**: 1 of 2\n## Phase 1 Checklist\n- [x] old item\n\n## Completed\n")

	if err := SyncTodoFromPlan(tmp); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(docs, "avatars_todo.md"))
	body := string(out)
	if !strings.Contains(body, "## Phase 1 Checklist") {
		t.Fatalf("expected phase 1 checklist retained:\n%s", body)
	}
	if !strings.Contains(body, "## Phase 2 Checklist") {
		t.Fatalf("expected phase 2 checklist:\n%s", body)
	}
	if !strings.Contains(body, "1. New work") {
		t.Fatalf("expected new groups:\n%s", body)
	}
	if !strings.Contains(body, "(active)") {
		t.Fatalf("expected active marker on current phase:\n%s", body)
	}
}
