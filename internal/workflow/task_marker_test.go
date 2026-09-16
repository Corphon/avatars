package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsPlanEmpty_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	if !IsPlanEmpty(tmpDir) {
		t.Error("expected IsPlanEmpty=true for empty directory")
	}
}

func TestIsPlanEmpty_UnfilledTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs", "workflow")
	os.MkdirAll(docsDir, 0755)
	planPath := filepath.Join(docsDir, "avatars_plan.md")
	// Write a plan with unfilled placeholders.
	content := `# Project Plan
## Goals
- [brief one-line description]
- [Primary goal — must deliver something]
`
	os.WriteFile(planPath, []byte(content), 0644)

	if !IsPlanEmpty(tmpDir) {
		t.Error("expected IsPlanEmpty=true for unfilled template")
	}
}

func TestIsPlanEmpty_FilledPlan(t *testing.T) {
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs", "workflow")
	os.MkdirAll(docsDir, 0755)
	planPath := filepath.Join(docsDir, "avatars_plan.md")
	content := `# Project Plan
**Active Phase**: 1
## Goals
- Build a CLI tool
## Success Criteria
- [x] Project compiles
`
	os.WriteFile(planPath, []byte(content), 0644)

	if IsPlanEmpty(tmpDir) {
		t.Error("expected IsPlanEmpty=false for filled plan")
	}
}

func TestMarkTaskAsDone_FindsAndMarks(t *testing.T) {
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs", "workflow")
	os.MkdirAll(docsDir, 0755)
	todoPath := filepath.Join(docsDir, "avatars_todo.md")
	content := `# Active Workspace
## Phase 1 Checklist
- [ ] Write hello.go
- [ ] Write main.go
- [ ] Write tests
## Completed
`
	os.WriteFile(todoPath, []byte(content), 0644)

	marked, err := MarkTaskAsDone(tmpDir, "Write hello.go")
	if err != nil {
		t.Fatalf("MarkTaskAsDone: %v", err)
	}
	if !marked {
		t.Error("expected MarkTaskAsDone to find and mark task")
	}

	// Verify file was updated.
	updated, _ := os.ReadFile(todoPath)
	if !strings.Contains(string(updated), "- [x] Write hello.go") {
		t.Error("expected [x] mark in updated file")
	}
	if !strings.Contains(string(updated), "- [ ] Write main.go") {
		t.Error("expected other items to remain [ ]")
	}
}

func TestMarkTaskAsDone_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs", "workflow")
	os.MkdirAll(docsDir, 0755)
	todoPath := filepath.Join(docsDir, "avatars_todo.md")
	os.WriteFile(todoPath, []byte("## Phase 1 Checklist\n- [ ] Write main.go\n"), 0644)

	marked, _ := MarkTaskAsDone(tmpDir, "nonexistent task")
	if marked {
		t.Error("expected MarkTaskAsDone to return false for non-matching task")
	}
}

func TestCountTodoProgress(t *testing.T) {
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs", "workflow")
	os.MkdirAll(docsDir, 0755)
	todoPath := filepath.Join(docsDir, "avatars_todo.md")
	content := `## Phase 1 Checklist
- [x] Done task 1
- [x] Done task 2
- [ ] Pending task
- [ ] Another pending
`
	os.WriteFile(todoPath, []byte(content), 0644)

	completed, total := CountTodoProgress(tmpDir)
	if completed != 2 {
		t.Errorf("expected 2 completed, got %d", completed)
	}
	if total != 4 {
		t.Errorf("expected 4 total, got %d", total)
	}
}

func TestHasFilesWithExt(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "test.py"), []byte("print('hi')"), 0644)

	if !hasFilesWithExt(tmpDir, ".go") {
		t.Error("expected .go files to be found")
	}
	if !hasFilesWithExt(tmpDir, ".py") {
		t.Error("expected .py files to be found")
	}
	if hasFilesWithExt(tmpDir, ".js") {
		t.Error("expected no .js files")
	}
}
