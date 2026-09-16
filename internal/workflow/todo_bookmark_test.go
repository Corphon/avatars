package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateTodoProgressBookmark_NoDuplicateArrows(t *testing.T) {
	dir := t.TempDir()
	todoDir := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(todoDir, 0755); err != nil {
		t.Fatal(err)
	}
	body := `# Active Workspace

> **Updated**: 2026-01-01

## Phase 1 Checklist (active)
- [ ] 1. Define the Cache type and entry structure (0/4)
↳ (last task in this phase)
- [x] 2. Implement the public API methods (1/1)
- [x] 3. Write unit tests (16/16)

## Completed
`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	if err := os.WriteFile(todoPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTodoProgressBookmark(dir); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTodoProgressBookmark(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if n := strings.Count(text, "↳ (last task in this phase)"); n != 1 {
		t.Fatalf("F75: want exactly one bookmark arrow, got %d\n%s", n, text)
	}
	if !strings.Contains(text, "- [>] 1. Define the Cache type") {
		t.Fatalf("expected current-task marker, got:\n%s", text)
	}
}
