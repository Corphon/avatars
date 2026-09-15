package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeepGoingPhrasesAreContinuationNotRecap(t *testing.T) {
	cases := []string{
		"继续",
		"继续吧",
		"继续推进",
		"请继续",
		"接着做",
		"continue",
		"go on",
		"keep going",
		"continue advancing",
		"继续推进 phase1.md",
		"keep going phase1.md",
	}
	for _, in := range cases {
		lowered := strings.ToLower(in)
		if !looksLikeContinuationWorkRequest(lowered) {
			t.Fatalf("expected continuation work: %q", in)
		}
		if looksLikeLastActionQuestion(lowered) {
			t.Fatalf("keep-going must not be last-action recap: %q", in)
		}
		if looksLikeResumeContinuationIntent(lowered) != true {
			t.Fatalf("keep-going should resume last task: %q", in)
		}
	}
	if looksLikeLastActionQuestion("上一轮做了什么") {
		// still recap
	} else {
		t.Fatal("status questions should remain last-action")
	}
}

func TestKeepGoingPermissionMode(t *testing.T) {
	if got := naturalLanguagePermissionMode("继续"); got != "acceptEdits" {
		t.Fatalf("继续 want acceptEdits, got %q", got)
	}
	if got := naturalLanguagePermissionMode("继续推进"); got != "acceptEdits" {
		t.Fatalf("继续推进 want acceptEdits, got %q", got)
	}
	if got := naturalLanguagePermissionMode(strings.ToLower("继续推进 phase1.md")); got != "acceptEdits" {
		t.Fatalf("继续推进 phase1.md want acceptEdits, got %q", got)
	}
	if got := naturalLanguagePermissionMode("继续分析"); got != "plan" {
		t.Fatalf("继续分析 should stay plan, got %q", got)
	}
}

func TestKeepGoingFromFileDoesNotAppendNL(t *testing.T) {
	cmd := restoreOriginalRunTaskText(
		[]string{"run", "--from-file", "phase1.md"},
		"继续推进 phase1.md",
	)
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "继续推进") {
		t.Fatalf("from-file must not grow leftover NL: %q", joined)
	}
	options, err := parseRunCommandOptions(cmd[1:])
	if err != nil {
		t.Fatalf("parse --from-file after restore: %v", err)
	}
	if options.InputFile != "phase1.md" {
		t.Fatalf("InputFile=%q", options.InputFile)
	}
	if options.Input != "" {
		t.Fatalf("leftover Input=%q", options.Input)
	}
}

func TestParseRunAllowsFromFilePlusKeepGoingExtra(t *testing.T) {
	options, err := parseRunCommandOptions([]string{"--from-file", "phase1.md", "继续推进", "phase1.md"})
	if err != nil {
		t.Fatalf("from-file + leftover must parse, got %v", err)
	}
	if options.InputFile != "phase1.md" {
		t.Fatalf("InputFile=%q", options.InputFile)
	}
	got := mergeRunFileAndExtraInput(options.InputFile, "# Phase 1\n\n## Goals\n", options.Input)
	if got != "# Phase 1\n\n## Goals" {
		t.Fatalf("keep-going leftover must not replace file body, got %q", got)
	}
}

func TestContinuationWorkUsesFromFileAndResume(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(".avatars", "tasks", "demo-task", "sessions")
	if err := os.MkdirAll(sess, 0755); err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(sess, "run.jsonl")
	if err := os.WriteFile(tp, []byte(strings.Repeat("{}\n", 40)), 0644); err != nil {
		t.Fatal(err)
	}

	in := "继续推进 phase1.md"
	nd, ok := continuationWorkSafeRunDecision(in, strings.ToLower(in))
	if !ok {
		t.Fatal("expected continuation run")
	}
	joined := strings.Join(nd.Command, " ")
	if !strings.Contains(joined, "--resume") {
		t.Fatalf("expected --resume, got %q", joined)
	}
	if strings.Contains(joined, "--new-task") {
		t.Fatalf("must not --new-task, got %q", joined)
	}
	if !strings.Contains(joined, "--from-file phase1.md") {
		t.Fatalf("expected --from-file phase1.md, got %q", joined)
	}
	if strings.Contains(joined, "继续推进") {
		t.Fatalf("must not keep leftover NL beside --from-file, got %q", joined)
	}
	if !strings.Contains(joined, "--permission-mode acceptEdits") {
		t.Fatalf("keep-going should acceptEdits, got %q", joined)
	}

	bare := "继续"
	nd, ok = continuationWorkSafeRunDecision(bare, bare)
	if !ok {
		t.Fatal("bare 继续 should be a run")
	}
	joined = strings.Join(nd.Command, " ")
	if !strings.Contains(joined, "--resume") || !strings.Contains(joined, "--permission-mode acceptEdits") {
		t.Fatalf("bare 继续 should resume with acceptEdits, got %q", joined)
	}
}

func TestGreenfieldStillNotResume(t *testing.T) {
	in := strings.ToLower("接着做一个开源库 ttlcache，从零写")
	if looksLikeResumeContinuationIntent(in) {
		t.Fatal("greenfield must not look like resume")
	}
}
