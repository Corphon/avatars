package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R3: build+test green must NOT mark Success Criteria while Active Phase todo is open.
func TestMarkPlanSuccessCriteria_RequiresChecklistReady(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 2
> **Phase Count**: 2

### Phase 2
- **Status**: in-progress

## Success Criteria
- [ ] Phase 2: API key auth works
- [ ] Phase 2: DELETE endpoint works
`
	todo := `# Todo
> **Phase**: 2

## Phase 2 Checklist (active)
- [ ] add API key middleware
- [ ] add DELETE route
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)

	n := markPlanSuccessCriteria(dir)
	if n != 0 {
		t.Fatalf("R3: expected 0 criteria marks while todo open, got %d", n)
	}
	raw, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if strings.Contains(string(raw), "- [x]") {
		t.Fatalf("R3: Criteria must stay open:\n%s", raw)
	}
}

// P4: failed build must not TryAutoMarkDone via syncWorkflowAfterBuild.
func TestSyncWorkflowAfterBuild_RequiresBuildOK(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	todo := `# Todo
> **Phase**: 1

## Phase 1 Checklist (active)
- [ ] create main.go
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	mainPath := filepath.Join(dir, "main.go")
	_ = os.WriteFile(mainPath, []byte("package main\n"), 0644)

	ctx := syncWorkflowAfterBuild(WorkflowSyncCtx{
		ProjectRoot:  dir,
		BuildOK:      false,
		TestOK:       false,
		ChangedFiles: []string{mainPath},
	})
	if ctx.MarkedCount != 0 {
		t.Fatalf("P4: BuildOK=false must not mark, got %d", ctx.MarkedCount)
	}
	raw, _ := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	if strings.Contains(string(raw), "- [x]") {
		t.Fatalf("P4: todo must stay unmarked:\n%s", raw)
	}
}
