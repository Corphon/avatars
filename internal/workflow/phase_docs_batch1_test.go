package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncPhaseDocsProcessTag_Incomplete(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 2

### Phase 1: A
- **Goal**: x
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

### Phase 2: B
- **Goal**: y
- **Status**: pending
- **Detail**: docs/workflow/phase2.md
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	// Only phase1 present and filled enough.
	phase1 := "# Phase 1\n## Tasks\n### 1. Scaffold\n- [ ] do work item alpha beta\n- [ ] second task gamma\n"
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase1), 0644); err != nil {
		t.Fatal(err)
	}
	missing, err := SyncPhaseDocsProcessTag(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) == 0 {
		t.Fatal("expected missing phase2")
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if !strings.Contains(string(body), "> **Phase Docs**: incomplete") {
		t.Fatalf("expected Phase Docs tag, got:\n%s", body)
	}
	if PhaseDocsComplete(dir) {
		t.Fatal("PhaseDocsComplete should be false")
	}
}

func TestTryAutoMarkPlanCriteria_RequiresBuildOK(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1

## Success Criteria
- [ ] All integration tests pass with an in-memory database
- [ ] The service can be started with a single go run
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	marked := TryAutoMarkPlanCriteria(dir, "built server", []string{"cmd/server/main.go"}, false, "go build and test")
	if marked != 0 {
		t.Fatalf("must not mark verify criteria without buildOK, marked=%d", marked)
	}
	marked = TryAutoMarkPlanCriteria(dir, "built server", []string{}, true, "go build")
	if marked != 0 {
		t.Fatalf("must not mark without changed files, marked=%d", marked)
	}
}
