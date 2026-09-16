package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollapseUnsolicitedPolishPhase_DropsFiller(t *testing.T) {
	plan := `# Project Plan
> **Active Phase**: 1
> **Phase Count**: 2

### Phase 1: Core library + tests
- **Goal**: Implement the library and unit tests.
- **Status**: in-progress
- **Detail**: docs/workflow/phase1.md

### Phase 2: Example + polish
- **Goal**: Add a runnable example and finalize docs/README.
- **Status**: pending
- **Detail**: docs/workflow/phase2.md

## Success Criteria
- [ ] tests pass
`
	task := "in-process circuit breaker library with tests, injectable clock"
	out := collapseUnsolicitedPolishPhase(plan, task)
	if strings.Contains(out, "> **Phase Count**: 2") {
		t.Fatalf("expected phase count 1:\n%s", out)
	}
	if strings.Contains(out, "Example + polish") {
		t.Fatalf("filler phase must be removed:\n%s", out)
	}
	if !strings.Contains(out, "Core library + tests") {
		t.Fatalf("phase 1 must remain:\n%s", out)
	}
}

func TestCollapseUnsolicitedPolishPhase_KeepsWhenUserAsked(t *testing.T) {
	plan := `# Project Plan
> **Phase Count**: 2

### Phase 1: Core library + tests
- **Goal**: library
- **Detail**: docs/workflow/phase1.md

### Phase 2: Example + polish
- **Goal**: Add a runnable example
- **Detail**: docs/workflow/phase2.md
`
	out := collapseUnsolicitedPolishPhase(plan, "library plus a runnable example and README")
	if !strings.Contains(out, "Example + polish") {
		t.Fatalf("user-asked example phase must stay:\n%s", out)
	}
}

func TestFormatChecklistSection_PreservesDoneWhenDiskHasTests(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package x\nimport \"testing\"\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n"), 0644)
	tasks := []string{"- [ ] 4. Unit tests (2/11)"}
	completed := []string{"4. Unit tests (11/11)"}
	out := formatChecklistSectionWithRoot("1", tasks, completed, true, dir)
	if !strings.Contains(out, "- [x] 4. Unit tests") {
		t.Fatalf("should keep completed tests when tests exist on disk:\n%s", out)
	}
}

func TestMarkPhaseChecklist_MarksAPIWhenSymbolOnDisk(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "breaker.go"), []byte("package breaker\nfunc Allow() bool { return true }\nfunc RecordSuccess() {}\n"), 0644)
	phase := `# Phase 1
## Scope & Success Criteria
- [ ] Public API exposes ` + "`Allow()`" + ` and ` + "`RecordSuccess()`" + `.
## Tasks
### 1. Public API methods
- [ ] Implement ` + "`Allow()`" + `
`
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}
	if n := MarkPhaseChecklistByPathEvidence(dir, 1); n == 0 {
		t.Fatal("expected disk symbols to mark phase leaves")
	}
	body, _ := os.ReadFile(filepath.Join(wf, "phase1.md"))
	if strings.Count(string(body), "- [x]") < 1 {
		t.Fatalf("expected marked items:\n%s", body)
	}
}
