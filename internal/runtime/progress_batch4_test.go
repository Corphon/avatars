package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProgressSummaryWithGate_BlocksAdvanceClaim(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Project**: demo
> **Status**: in-progress
> **Active Phase**: 2
> **Phase Count**: 5

# Plan
`
	todo := `> **Phase**: 2 of 5
## Phase 2 Checklist
- [x] a
- [x] b
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}

	honest := BuildProgressSummaryWithGate(dir, false, true, "build_failed")
	if !strings.Contains(honest, "Phase advance blocked") {
		t.Fatalf("expected blocked message, got %q", honest)
	}
	if strings.Contains(honest, "Next run advances") {
		t.Fatalf("must not claim advance when blocked: %q", honest)
	}

	confirm := BuildProgressSummaryWithGate(dir, false, false, "confirm_failed")
	if !strings.Contains(confirm, "confirm blocked") && !strings.Contains(confirm, "confirm_failed") {
		t.Fatalf("expected confirm_failed messaging, got %q", confirm)
	}
	if strings.Contains(confirm, "fix build/docs") {
		t.Fatalf("must not say fix build/docs for confirm_failed: %q", confirm)
	}
	if strings.Contains(confirm, "checklist complete") {
		t.Fatalf("must not claim checklist complete for confirm_failed: %q", confirm)
	}

	ok := BuildProgressSummaryWithGate(dir, true, true, "")
	if !strings.Contains(ok, "Next run advances to Phase 3") {
		t.Fatalf("expected advance claim when healthy: %q", ok)
	}
}

func TestDiscoverExistingSurveyTargets_FindsGoMod(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module demo\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("main.go", []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := discoverExistingSurveyTargets()
	if len(got) == 0 {
		t.Fatal("expected disk targets")
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "go.mod") && !strings.Contains(joined, "main.go") {
		t.Fatalf("unexpected targets: %v", got)
	}
}
