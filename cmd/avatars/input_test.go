package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveIntentInputPath_WorkflowDir(t *testing.T) {
	root := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join("docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	body := "# Phase 1\n\n## Goals\n\n- ship"
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	got := resolveIntentInputPath("phase1.md")
	want := filepath.Join("docs", "workflow", "phase1.md")
	if got != want {
		t.Fatalf("bare name: got %q want %q", got, want)
	}

	content, resolved, err := readIntentInputFileWithResolve("phase1.md")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("resolved %q want %q", resolved, want)
	}
	if !strings.Contains(content, "## Goals") {
		t.Fatalf("content: %q", content)
	}

	if got := resolveIntentInputPath("missing-plan.md"); got != "missing-plan.md" {
		t.Fatalf("missing path should stay unchanged, got %q", got)
	}
}

func TestResolveIntentInputPath_PrefersExplicitFile(t *testing.T) {
	root := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join("docs", "workflow"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("phase1.md", []byte("# root\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("docs", "workflow", "phase1.md"), []byte("# workflow\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolveIntentInputPath("phase1.md"); got != "phase1.md" {
		t.Fatalf("explicit cwd file should win, got %q", got)
	}
}
