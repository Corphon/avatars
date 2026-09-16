package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCriteriaRequiresBuildEvidence_StubClaims(t *testing.T) {
	if !criteriaRequiresBuildEvidence("no leftover code stubs; all handlers fully implemented") {
		t.Fatal("G3: stub/leftover criteria must require build evidence")
	}
}

func TestForceCompletePhaseChecklist(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	todo := `# Todo
> **Phase**: 2

## Phase 1 Checklist
- [>] 1. Scaffold (1/2)
- [ ] leftover item

## Phase 2 Checklist (active)
- [ ] do auth
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	if err := forceCompletePhaseChecklist(dir, 1); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	s := string(raw)
	if strings.Contains(s, "- [>]") || strings.Contains(s, "- [ ] leftover") {
		t.Fatalf("G4: Phase1 must be fully [x]:\n%s", s)
	}
	if !strings.Contains(s, "- [ ] do auth") {
		t.Fatal("Phase2 must stay open")
	}
}

func TestCriticAuditAndMark_SkippedWhenBuildRed(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nimport _ \"example.com/x/missing/pkg\"\nfunc main() {}\n"), 0644)
	todo := `# Todo
> **Phase**: 1
## Phase 1 Checklist (active)
- [ ] create main.go
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	n := CriticAuditAndMark(dir)
	if n != 0 {
		t.Fatalf("G3: CriticAuditAndMark must not mark when build red, got %d", n)
	}
}
