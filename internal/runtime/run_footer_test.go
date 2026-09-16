package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDedupeChangedFiles(t *testing.T) {
	got := DedupeChangedFiles([]string{"b.go", "a.go", "b.go", " ./a.go ", ""})
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
		t.Fatalf("unexpected dedupe: %#v", got)
	}
}

func TestComputeFileChangeStats_NewFiles(t *testing.T) {
	root := t.TempDir()
	p1 := filepath.Join(root, "hello.py")
	p2 := filepath.Join(root, "pkg", "main.rs")
	_ = os.MkdirAll(filepath.Dir(p2), 0755)
	_ = os.WriteFile(p1, []byte("print(1)\nprint(2)\n"), 0644)
	_ = os.WriteFile(p2, []byte("fn main() {}\n"), 0644)

	footer := ComputeFileChangeStats(root, []string{"hello.py", "pkg/main.rs"})
	if len(footer.Files) != 2 {
		t.Fatalf("want 2 files, got %#v", footer.Files)
	}
	if footer.TotalAdded < 3 {
		t.Fatalf("expected line additions from new files, got +%d", footer.TotalAdded)
	}
	headline := footer.Headline()
	if !strings.Contains(headline, "2 files changed") {
		t.Fatalf("headline=%q", headline)
	}
}

func TestComputeFileChangeStats_DropsMissingBurialPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ratebucket.go"), []byte("package ratebucket\n"), 0644); err != nil {
		t.Fatal(err)
	}
	footer := ComputeFileChangeStats(root, []string{
		"ratebucket.go",
		"internal/ratebucket/ratebucket.go",
		"internal/ratebucket/ratebucket_test.go",
	})
	if len(footer.Files) != 1 || footer.Files[0].Path != "ratebucket.go" {
		t.Fatalf("F70 footer must drop deleted burial paths, got %#v", footer.Files)
	}
}

func TestComputeFileChangeStats_RemapsFoldedPackagePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ttlcache.go"), []byte("package ttlcache\n"), 0644); err != nil {
		t.Fatal(err)
	}
	footer := ComputeFileChangeStats(root, []string{
		"ttlcache/ttlcache.go",
		"ttlcache.go",
		"go.mod",
	})
	paths := map[string]bool{}
	for _, f := range footer.Files {
		paths[f.Path] = true
	}
	if !paths["ttlcache.go"] {
		t.Fatalf("F79: folded package path must map to root file, got %#v", footer.Files)
	}
	if paths["ttlcache/ttlcache.go"] {
		t.Fatalf("F79: ghost package-dir path must not remain, got %#v", footer.Files)
	}
}

func TestSuggestNextSteps_BuildFailed(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Project**: demo
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1
## Goals
- Ship demo
## Phases
### Phase 1: Core
- **Goal**: working binary
## Success Criteria
- [ ] go build passes
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	steps := SuggestNextSteps(root, NextStepInput{
		Status:              "completed_unverified",
		BuildOK:             false,
		BuildOKKnown:        true,
		PhaseAdvanceBlocked: "build_failed",
		FilesChanged:        2,
	})
	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "Repair compile/test") {
		t.Fatalf("expected build repair suggestion, got %v", steps)
	}
}

func TestSuggestNextSteps_ConfirmFailedNotBuild(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Project**: inkserve
> **Status**: draft
> **Active Phase**: 1
> **Phase Count**: 2
## Goals
- Serve markdown as HTML
## Phases
### Phase 1: Core
- **Goal**: health + render
### Phase 2: SSE
- **Goal**: events
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)

	steps := SuggestNextSteps(root, NextStepInput{
		Status:              "failed",
		Summary:             "Confirm plan failed after construct: phase 1 Tasks do not cover Scope & Success Criteria (thin Tasks rejected); uncovered: GET /health",
		BuildOK:             false,
		BuildOKKnown:        false,
		PhaseAdvanceBlocked: "confirm_failed",
		FilesChanged:        1,
	})
	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "Tasks") || !strings.Contains(joined, "Scope") {
		t.Fatalf("expected Tasks/Scope confirm guidance, got %v", steps)
	}
	if strings.Contains(joined, "Repair compile") {
		t.Fatalf("must not suggest compile repair on confirm_failed: %v", steps)
	}
	if strings.Contains(joined, "advance into Phase") {
		t.Fatalf("must not suggest phase advance on confirm_failed: %v", steps)
	}
}

func TestIsPlanningQualityAbort(t *testing.T) {
	if !isPlanningQualityAbort("failed: confirm_plan") {
		t.Fatal("confirm_plan")
	}
	if !isPlanningQualityAbort("failed: construct_plan") {
		t.Fatal("construct_plan")
	}
	if isPlanningQualityAbort("failed: builder") {
		t.Fatal("builder failure is not planning abort")
	}
	if isPlanningQualityAbort("completed") {
		t.Fatal("completed")
	}
}

func TestSuggestNextSteps_ContinuePhase(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 2
## Phases
### Phase 1: Core
- **Goal**: base
### Phase 2: Auth
- **Goal**: auth
`
	todo := `# Todo
> **Phase**: 1 of 2
## Phase 1 Checklist
- [x] one
- [ ] two
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	steps := SuggestNextSteps(root, NextStepInput{Status: "completed", BuildOK: true, BuildOKKnown: true})
	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "Continue Phase 1") {
		t.Fatalf("expected continue phase tip, got %v", steps)
	}
}

func TestSuggestNextSteps_LastPhaseCompleteDespiteRemainingBoxes(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 1
> **Status**: completed
## Phases
### Phase 1: Core
- **Goal**: library
`
	todo := `# Todo
> **Phase**: 1 of 1
## Phase 1 Checklist
- [x] Library
- [ ] race check
- [ ] polish
- [ ] docs
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	steps := SuggestNextSteps(root, NextStepInput{Status: "completed", BuildOK: true, BuildOKKnown: true})
	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "All phases complete") {
		t.Fatalf("last phase + green build should not nag leftover boxes, got %v", steps)
	}
	if strings.Contains(joined, "finish the remaining") {
		t.Fatalf("must not say finish remaining on a completed last phase, got %v", steps)
	}
}

func TestSuggestNextSteps_NoContinueChecklistWhenFailed(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 2
## Phases
### Phase 1: Core
- **Goal**: base
### Phase 2: Auth
- **Goal**: auth
`
	todo := `# Todo
> **Phase**: 1 of 2
## Phase 1 Checklist
- [ ] one
- [ ] two
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	steps := SuggestNextSteps(root, NextStepInput{
		Status:              "failed",
		Summary:             "critic: go mod tidy failed: repeated toolchain statement",
		BuildOK:             false,
		BuildOKKnown:        true,
		PhaseAdvanceBlocked: "needs_remediation",
	})
	joined := strings.Join(steps, "\n")
	if strings.Contains(joined, "Continue Phase") {
		t.Fatalf("F11': must not Continue Phase while failed/remediation, got %v", steps)
	}
}

func TestSuggestNextSteps_NoPhase2WhenBuildFailed(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 2
## Phases
### Phase 1: Core
- **Goal**: base
### Phase 2: Auth
- **Goal**: auth
`
	todo := `# Todo
> **Phase**: 1 of 2
## Phase 1 Checklist
- [x] one
- [x] two
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	steps := SuggestNextSteps(root, NextStepInput{
		Status:              "needs_remediation",
		BuildOK:             false,
		BuildOKKnown:        true,
		PhaseAdvanceBlocked: "build_failed",
	})
	joined := strings.Join(steps, "\n")
	if strings.Contains(joined, "Phase 2") {
		t.Fatalf("A6: must not suggest Phase 2 while build_failed/needs_remediation, got %v", steps)
	}
	if !strings.Contains(joined, "remediation") && !strings.Contains(joined, "compile/test") {
		t.Fatalf("expected remediation/build tip, got %v", steps)
	}
}

func TestFormatCLIAndMarkdown(t *testing.T) {
	f := RunFooter{
		Files: []FileChangeStat{
			{Path: "a.go", Added: 10, Deleted: 2},
			{Path: "b.py", Added: 5, Deleted: 0},
		},
		TotalAdded:   15,
		TotalDeleted: 2,
		NextSteps:    []string{"Re-run to advance into Phase 2."},
	}
	cli := f.FormatCLI()
	if !strings.Contains(cli, "a.go  +10 -2") || !strings.Contains(cli, "Next steps:") {
		t.Fatalf("cli=%s", cli)
	}
	md := f.FormatMarkdown()
	if !strings.Contains(md, "## Files Changed") || !strings.Contains(md, "## Next Steps") {
		t.Fatalf("md=%s", md)
	}
	payload := f.EventPayload()
	if payload["files_changed_count"] != 2 {
		t.Fatalf("payload=%v", payload)
	}
}

func TestReplaceRunFooterMarkdown(t *testing.T) {
	base := "# Analysis Report\n\n## Request\nok\n"
	f1 := RunFooter{NextSteps: []string{"first"}}
	once := AppendRunFooterMarkdown(base, f1)
	f2 := RunFooter{
		Files:     []FileChangeStat{{Path: "x.ts", Added: 1, Deleted: 0}},
		NextSteps: []string{"second"},
	}
	twice := ReplaceRunFooterMarkdown(once, f2)
	if strings.Contains(twice, "first") {
		t.Fatalf("old next step should be replaced: %s", twice)
	}
	if !strings.Contains(twice, "x.ts") || !strings.Contains(twice, "second") {
		t.Fatalf("new footer missing: %s", twice)
	}
}

func TestComputeFileChangeStats_DropsHarnessLogs(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "lib.py"), []byte("x = 1\n"), 0644)
	_ = os.WriteFile(filepath.Join(root, "avatars_test_mid_go_nl_p2_r29b.log"), []byte("noise\n"), 0644)
	_ = os.WriteFile(filepath.Join(root, "avatars_test_mid_go_nl_p2_r29b.err.log"), []byte("err\n"), 0644)
	footer := ComputeFileChangeStats(root, []string{
		"lib.py",
		"avatars_test_mid_go_nl_p2_r29b.log",
		"avatars_test_mid_go_nl_p2_r29b.err.log",
	})
	for _, f := range footer.Files {
		if strings.Contains(f.Path, "avatars_test_") {
			t.Fatalf("F89: harness log must not appear in footer: %#v", footer.Files)
		}
	}
	if len(footer.Files) != 1 || footer.Files[0].Path != "lib.py" {
		t.Fatalf("expected only lib.py, got %#v", footer.Files)
	}
}

func TestComputeFileChangeStats_KeepsRealDeletions(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "hashringx.go"), []byte("package hashringx\n"), 0644)
	footer := ComputeFileChangeStats(root, []string{
		"hashringx.go",
		"internal/zzdiag/zzdiag_test.go",
	})
	foundDel := false
	for _, f := range footer.Files {
		if f.Path == "internal/zzdiag/zzdiag_test.go" && f.Deleted > 0 {
			foundDel = true
		}
	}
	if !foundDel {
		t.Fatalf("F89: deleted junk path should show as deletion, got %#v", footer.Files)
	}
}
