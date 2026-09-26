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

func TestMarkDraftInProgressAfterWorkAndPhaseDocsResync(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `# Project Plan
> **Project**: bookmark service
> **Status**: draft
> **Active Phase**: 2
> **Phase Count**: 2

## Goals
- Serve bookmarks over HTTP.

### Phase 1: Store
- **Goal**: persist
- **Status**: completed
- **Detail**: docs/workflow/phase1.md

### Phase 2: HTTP
- **Goal**: serve
- **Status**: in-progress
- **Detail**: docs/workflow/phase2.md
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	filled := "# Phase\n## Tasks\n### 1. Work\n- [x] write the package sources and tests\n"
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(filled), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "phase2.md"), []byte(filled), 0644); err != nil {
		t.Fatal(err)
	}
	if err := MarkDraftInProgress(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := MarkDraftInProgress(dir, true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "> **Status**: in-progress") {
		t.Fatalf("draft should become in-progress after sources landed:\n%s", body)
	}
	missing, err := SyncPhaseDocsProcessTag(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("filled phase docs should be complete, missing %v", missing)
	}
	body, _ = os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if !strings.Contains(string(body), "> **Phase Docs**: complete") {
		t.Fatalf("phase docs tag should be complete:\n%s", body)
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

func TestMarkProseTasksBesideDonePaths(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	phase := `# Phase 1

## Tasks

### 2. storage
- [x] create storage/file.go
- [ ] internal read and write helper
- [ ] go test ./...

### 3. empty group
- [ ] later work (0/4)
`
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase), 0o644); err != nil {
		t.Fatal(err)
	}
	n := MarkProseTasksBesideDonePaths(dir)
	if n != 1 {
		t.Fatalf("expected one prose task marked, got %d", n)
	}
	body, err := os.ReadFile(filepath.Join(wf, "phase1.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "- [x] internal read and write helper") {
		t.Fatalf("prose task beside checked paths should be marked:\n%s", text)
	}
	if !strings.Contains(text, "- [ ] go test ./...") {
		t.Fatalf("command task must stay open:\n%s", text)
	}
	if !strings.Contains(text, "- [ ] later work (0/4)") {
		t.Fatalf("(0/N) group must stay open:\n%s", text)
	}
}
