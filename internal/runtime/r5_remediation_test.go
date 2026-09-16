package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"avatars/internal/llm"
)

func TestShouldSkipClarifyGate_FullCourseAndMultiPhase(t *testing.T) {
	if !shouldSkipClarifyGate("三段都跑通，允许升相，Phase 1 then Phase 2") {
		t.Fatal("full-course NL must skip clarify gate")
	}
	if !shouldSkipClarifyGate("Implement Phase 1 and Phase 2 with go build/test green") {
		t.Fatal("multi-phase estimate must skip clarify gate")
	}
	// Small ambiguous task still clarifies.
	if shouldSkipClarifyGate("add a search command") {
		t.Fatal("vague single feature must still hit clarify heuristics")
	}
	qs := detectAmbiguity("add a search command")
	if len(qs) == 0 {
		t.Fatal("expected clarify questions for vague add-feature")
	}
}

func TestFilesTouchedSince_IncludesRecentWrites(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hello.go")
	since := time.Now().Add(-time.Second)
	if err := os.WriteFile(path, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := FilesTouchedSince(root, since)
	found := false
	for _, p := range got {
		if p == "hello.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected hello.go in %v", got)
	}
}

func TestNoteChangedFile_FromLLMHook(t *testing.T) {
	e := &Engine{}
	llm.SetFileMutationHandler(e.noteChangedFile)
	defer llm.ClearFileMutationHandler()
	llm.NotifyFileMutation("internal/handlers/a.go")
	llm.NotifyFileMutation("cmd/server/main.go")
	snap := e.changedFilesSnapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 changed files, got %v", snap)
	}
}
