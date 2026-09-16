package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedOnlyDrops(t *testing.T) {
	if !protectedOnlyDrops([]string{
		"avatars_todo.md (protected workflow SoT — Builder cannot write)",
	}) {
		t.Fatal("expected protected-only")
	}
	if protectedOnlyDrops([]string{
		"avatars_todo.md (protected workflow SoT — Builder cannot write)",
		"foo.go (duplicate after sanitize)",
	}) {
		t.Fatal("mixed drops should not be protected-only")
	}
	if protectedOnlyDrops(nil) {
		t.Fatal("empty should be false")
	}
}

func TestGoLanguageAllowsSQL(t *testing.T) {
	exts := getLanguageExtensions("go")
	foundSQL, foundHTML := false, false
	for _, e := range exts {
		if e == ".sql" {
			foundSQL = true
		}
		if e == ".html" {
			foundHTML = true
		}
	}
	if !foundSQL {
		t.Fatal("go stack must allow .sql migrations")
	}
	if !foundHTML {
		t.Fatal("go stack must allow .html (stage edits)")
	}
	kept, rejected := filterCrossLanguageFiles([]builderCodeFile{
		{Path: "migrations/001_init.sql", Content: "CREATE TABLE t(id INT);"},
		{Path: "stage/demo.html", Content: "<html></html>"},
		{Path: "cmd/server/main.go", Content: "package main"},
		{Path: "evil.py", Content: "print(1)"},
	}, "go")
	if len(rejected) != 1 || rejected[0].Path != "evil.py" {
		t.Fatalf("rejected=%v", rejected)
	}
	if len(kept) != 3 {
		t.Fatalf("kept=%v", kept)
	}
}

func TestResolveWorkflowBuildTestOK_NoChangesUsesHealth(t *testing.T) {
	dir := t.TempDir()
	// Minimal compiling Go module so crossLangHealthCheck can pass.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/noop\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ok, _ := resolveWorkflowBuildTestOK(dir, RunResult{Summary: "noop run", SynthesisStatus: "completed"}, nil)
	if !ok {
		t.Fatal("zero changedFiles + green build must not invent build_failed")
	}
}

func TestResolveWorkflowBuildTestOK_ConfirmPlanNotBuildFail_F60(t *testing.T) {
	dir := t.TempDir()
	// Empty project (docs only) — confirm_plan fail must not become knownFail build.
	ok, _ := resolveWorkflowBuildTestOK(dir, RunResult{
		Summary:         "Confirm plan failed after construct: thin Tasks rejected",
		SynthesisStatus: "failed: confirm_plan",
	}, nil)
	if !ok {
		t.Fatal("F60: confirm_plan failure with no source writes must not invent build_failed")
	}
	if !isPlanningGateFailure(RunResult{SynthesisStatus: "failed: confirm_plan"}) {
		t.Fatal("expected planning gate failure detector")
	}
}
