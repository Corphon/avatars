package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlignActivePhaseFromInput_BlocksWhenTodoEmpty(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 2

## Phase 1
scaffold

## Phase 2
auth
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("# Todo\n\n> **Active Phase**: 1\n"), 0644)

	e := &Engine{}
	prev, impl := e.alignActivePhaseFromInput("进入 Phase 2 继续实现")
	if prev != 1 {
		t.Fatalf("prev=%d", prev)
	}
	if impl != 1 {
		t.Fatalf("P9-3: empty checklist must not advance Active Phase, got implement=%d", impl)
	}
	raw, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if !strings.Contains(string(raw), "Active Phase**: 1") {
		t.Fatalf("plan Active Phase should stay 1:\n%s", raw)
	}
}

// R2: injectPhaseContextIntoPlan must not silent-jump Active Phase after align clamped.
func TestInjectPhaseContext_DoesNotSilentJump(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 2

### Phase 1
- **Goal**: scaffold
- **Status**: in-progress

### Phase 2
- **Goal**: auth
- **Status**: pending

## Success Criteria
- [ ] Phase 1: scaffold ok
- [ ] Phase 2: auth ok
`
	todo := `# Todo

> **Active Phase**: 1

## Phase 1 Checklist (active)
- [>] create app package
- [ ] write tests

## Phase 2 Checklist
- [ ] add API key middleware
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)

	e := &Engine{}
	_, _ = e.alignActivePhaseFromInput("进入 Phase 2 继续实现鉴权")
	_ = e.injectPhaseContextIntoPlan("进入 Phase 2 继续实现鉴权")

	raw, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if !strings.Contains(string(raw), "Active Phase**: 1") {
		t.Fatalf("R2: inject must not silent-jump Active Phase to 2:\n%s", raw)
	}
}
