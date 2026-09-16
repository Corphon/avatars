package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCriticDecidePhaseAdvance_BookmarkCountsAsRemaining(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Project**: demo
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 2

### Phase 1: Scaffold
- **Status**: in-progress
### Phase 2: Auth
- **Status**: pending
`
	// Only incomplete item is the [>] bookmark — previously this looked "done"
	// to CriticDecide (ignored [>]) then tryAdvancePhase refused (counted [>]).
	todo := `> **Phase**: 1 of 2
## Phase 1 Checklist (active)
- [x] done item
- [>] still working on handlers
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	advanced, next := CriticDecidePhaseAdvanceUpTo(dir, 0)
	if advanced || next != 0 {
		t.Fatalf("bookmark remaining must block advance: advanced=%v next=%d", advanced, next)
	}
	items := GetRemainingChecklistItems(dir)
	if len(items) != 1 || !strings.Contains(items[0], "handlers") {
		t.Fatalf("remaining should include bookmark item: %v", items)
	}
}

func TestCriticAuditAndMark_MarksBookmarkWhenSatisfied(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "handlers"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "handlers", "snippet.go"), []byte("package handlers\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plan := `> **Project**: demo
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1

### Phase 1: Scaffold
- **Status**: in-progress
`
	todo := `> **Phase**: 1 of 1
## Phase 1 Checklist (active)
- [>] Create internal/handlers/snippet.go
`
	phase := `# Phase 1
## Tasks
- [ ] Create internal/handlers/snippet.go
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}
	n := CriticAuditAndMark(dir)
	if n < 1 {
		t.Fatalf("expected bookmark item marked, got %d", n)
	}
	out, err := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "- [x] Create internal/handlers/snippet.go") {
		t.Fatalf("bookmark not marked done:\n%s", out)
	}
}
