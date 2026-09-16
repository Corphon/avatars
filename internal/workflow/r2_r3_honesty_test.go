package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R3: TryAutoMarkDone must mark Active Phase checklist only, not Phase 1 leftovers.
func TestTryAutoMarkDone_ActivePhaseOnly(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	todo := `# Todo
> **Phase**: 2

## Phase 1 Checklist
- [ ] create app package

## Phase 2 Checklist (active)
- [ ] add auth middleware
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	authFile := filepath.Join(dir, "auth_middleware.go")
	_ = os.WriteFile(authFile, []byte("package main\nfunc AuthMiddleware() {}\n"), 0644)

	n := TryAutoMarkDone(dir, "Builder code generation", []string{authFile})
	raw, _ := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	s := string(raw)
	if strings.Contains(s, "- [x] create app package") {
		t.Fatalf("R3: must not mark Phase 1 item while Active=2:\n%s", s)
	}
	if n > 0 && !strings.Contains(s, "- [x] add auth middleware") {
		t.Fatalf("expected Phase 2 mark if any, got:\n%s", s)
	}
}

func TestPhaseChecklistReadyForCriteria_Exported(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(`# T
> **Phase**: 1
## Phase 1 Checklist (active)
- [ ] still open
`), 0644)
	if PhaseChecklistReadyForCriteria(dir, 1) {
		t.Fatal("exported wrapper must match unready")
	}
}
