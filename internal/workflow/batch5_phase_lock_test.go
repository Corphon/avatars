package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCriticDecidePhaseAdvanceUpTo_RespectsLock(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Project**: demo
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 5

### Phase 1: Scaffold
- **Status**: in-progress
### Phase 2: Auth
- **Status**: pending
`
	todo := `> **Phase**: 1 of 5
## Phase 1 Checklist (active)
- [x] a
- [x] b
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	// Stub go.mod so projectCompiles may still fail — lock should block regardless
	// when ActivePhase >= maxPhase. Use maxPhase=1 with ActivePhase=1.
	advanced, next := CriticDecidePhaseAdvanceUpTo(dir, 1)
	if advanced || next != 0 {
		t.Fatalf("locked phase must not advance: advanced=%v next=%d", advanced, next)
	}
}

func TestNormalizeTodoPhaseHeader_StripsLLMCurrentPhase(t *testing.T) {
	in := `# Active Workspace

> Current Phase: Phase 3 - Post RESTful CRUD APIs
> Plan: docs/workflow/avatars_plan.md

## Phase 1 Checklist
- [x] a
`
	out := normalizeTodoPhaseHeader(in, 1, 5)
	if strings.Contains(out, "Current Phase:") {
		t.Fatalf("LLM header remained: %s", out)
	}
	if !strings.Contains(out, "**Phase**: 1") {
		t.Fatalf("missing canonical header: %s", out)
	}
}

func TestAlignCurrentTaskToPhase(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	todo := `# Active Workspace
> **Phase**: 1 of 5

## Current Task
- [ ] Implement Phase 3: Post RESTful CRUD APIs

## Phase 1 Checklist (active)
- [x] a
`
	plan := `> **Active Phase**: 1
> **Phase Count**: 5
### Phase 1: Scaffolding
- **Status**: in-progress
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AlignCurrentTaskToPhase(dir, 1); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "Implement Phase 3") {
		t.Fatalf("Current Task still Phase 3:\n%s", s)
	}
	if !strings.Contains(s, "Implement Phase 1") {
		t.Fatalf("Current Task not aligned to Phase 1:\n%s", s)
	}
}
