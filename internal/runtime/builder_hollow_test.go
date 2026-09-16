package runtime

import (
	"path/filepath"
	"testing"
)

func TestBuilderProseSaysComplete(t *testing.T) {
	complete := []string{
		"**Phase 1 is fully complete.** All files already existed.\nBuild Status: PASSING",
		"## Phase 2 Complete — All Verification Passed\nNo code changes were needed.",
		"Phase 3 is verified complete. Tests passed.",
		"implementation was already on disk; all checks pass",
		"no further code changes",
		"pytest: 12 passed, exit 0",
		"npm test ok (exit 0)",
	}
	for _, text := range complete {
		if !builderProseSaysComplete(text) {
			t.Fatalf("expected completion prose: %q", text)
		}
	}
	incomplete := []string{
		"Implement Connect() in internal/database/db.go",
		"Need to complete the remaining tests",
		"Add a pytest fixture for the clock",
		"the feature is not already complete",
	}
	for _, text := range incomplete {
		if builderProseSaysComplete(text) {
			t.Fatalf("did not expect completion prose: %q", text)
		}
	}
}

func TestAsksForAppSources(t *testing.T) {
	if !asksForAppSources("Need a DoChan variant. Extend the library, write docs/workflow/phase2.md") {
		t.Fatal("implement + phase doc should still ask for app sources")
	}
	if asksForAppSources("write docs/workflow/phase2.md") {
		t.Fatal("phase doc only should not ask for app sources")
	}
	if asksForAppSources("继续推进 docs/workflow/phase2.md") {
		t.Fatal("continue-from-phase-doc should not ask for app sources")
	}
	if !asksForAppSources("Add DoChan(key, fn) <-chan Result to the package") {
		t.Fatal("API ask should require app sources")
	}
}

func TestBuilderFilesAreNonImplementation(t *testing.T) {
	docs := []builderCodeFile{{Path: "docs/workflow/phase2.md", Content: "# Phase 2"}}
	if !builderFilesAreNonImplementation(docs) {
		t.Fatal("workflow markdown is not implementation")
	}
	src := []builderCodeFile{
		{Path: "docs/workflow/phase2.md", Content: "# Phase 2"},
		{Path: "flightx.go", Content: "package flightx"},
	}
	if builderFilesAreNonImplementation(src) {
		t.Fatal("library source counts as implementation")
	}
	if builderFilesAreNonImplementation(nil) {
		t.Fatal("empty file list is a different case")
	}
}

func TestMergeBuilderCodeFiles(t *testing.T) {
	base := []builderCodeFile{{Path: "docs/workflow/phase2.md", Content: "old"}}
	extra := []builderCodeFile{
		{Path: "docs/workflow/phase2.md", Content: "new"},
		{Path: filepath.ToSlash("lib.py"), Content: "x = 1"},
	}
	got := mergeBuilderCodeFiles(base, extra)
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Content != "new" {
		t.Fatalf("expected extra to win on same path, got %q", got[0].Content)
	}
}
