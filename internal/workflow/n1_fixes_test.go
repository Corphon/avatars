package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTaskCompletionVerdict_TimeoutIsFail(t *testing.T) {
	got := taskCompletionVerdict("Run stopped during workflow execution: avatar node node-review timed out after 20m0s")
	if got != "FAIL" {
		t.Fatalf("timeout verdict=%q want FAIL", got)
	}
}

func TestPhaseChecklistReadyForCriteria(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	todo := `# Active Workspace
> **Phase**: 2

## Phase 2 Checklist (active)
- [ ] 1. API-Key Middleware (0/2)
- [ ] 2. Delete Model (0/2)
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	if phaseChecklistReadyForCriteria(dir, 2) {
		t.Fatal("open deliverables must not be ready for criteria")
	}
	todoDone := `# Active Workspace
> **Phase**: 2

## Phase 2 Checklist (active)
- [x] 1. API-Key Middleware (2/2)
- [x] 2. Delete Model (2/2)
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todoDone), 0644)
	if !phaseChecklistReadyForCriteria(dir, 2) {
		t.Fatal("completed checklist should be ready")
	}
}

func TestChecklistItemRequiresLiveTestEvidence(t *testing.T) {
	if !checklistItemRequiresLiveTestEvidence("integration tests (3/3)") {
		t.Fatal("expected integration tests to require live evidence")
	}
	if checklistItemRequiresLiveTestEvidence("add note model fields") {
		t.Fatal("model deliverable must not require live test evidence")
	}
}
