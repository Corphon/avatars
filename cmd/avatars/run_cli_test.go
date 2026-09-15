package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/runtime"
	"avatars/internal/tasks"
)

func TestRun_ReusesStableTaskWorkspace(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	firstOutput := captureRunOutput(t, []string{"run", "--task", "demo-task", "Analyze the current repository and propose a refactoring plan"})
	secondOutput := captureRunOutput(t, []string{"run", "--task", "demo-task", "Continue repository analysis"})

	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, err := manager.Load("demo-task")
	if err != nil {
		t.Fatalf("load workspace failed: %v", err)
	}
	if workspace.RunCount != 2 {
		t.Fatalf("expected run count 2, got %d", workspace.RunCount)
	}
	if workspace.LatestTranscript == "" {
		t.Fatal("expected latest transcript to be tracked in task manifest")
	}
	if !strings.Contains(firstOutput, "Task mode: new") {
		t.Fatalf("expected first run to be marked new, got %s", firstOutput)
	}
	if !strings.Contains(secondOutput, "Task mode: stable") {
		t.Fatalf("expected second run to be marked stable, got %s", secondOutput)
	}
	if !strings.Contains(secondOutput, "Continued from transcript") {
		t.Fatalf("expected second run to continue from the previous transcript, got %s", secondOutput)
	}
	if !strings.Contains(secondOutput, filepath.ToSlash(filepath.Join(".avatars", "tasks", "demo-task"))) && !strings.Contains(secondOutput, filepath.Join(".avatars", "tasks", "demo-task")) {
		t.Fatalf("expected second run output to mention the stable task root, got %s", secondOutput)
	}
}

func TestRun_PersistsBlockedRunTranscriptToTaskWorkspace(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	runErr := run([]string{"run", "--task", "demo-task", "--permission-mode", "default", "Analyze the current repository and propose a refactoring plan"})
	if runErr == nil || !strings.Contains(runErr.Error(), "approval required") {
		t.Fatalf("expected approval required run error, got %v", runErr)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, err := manager.Load("demo-task")
	if err != nil {
		t.Fatalf("load workspace failed: %v", err)
	}
	if workspace.RunCount != 1 {
		t.Fatalf("expected blocked run to be tracked in manifest, got run count %d", workspace.RunCount)
	}
	if strings.TrimSpace(workspace.LatestTranscript) == "" {
		t.Fatal("expected blocked run latest transcript to be persisted in task manifest")
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	if !strings.Contains(showOutput, "Status: awaiting_approval") {
		t.Fatalf("expected tasks show awaiting_approval status for blocked run, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest transcript:") {
		t.Fatalf("expected tasks show latest transcript for blocked run, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest run summary:") || !strings.Contains(showOutput, "approval required") {
		t.Fatalf("expected tasks show blocked run summary to surface approval requirement, got %s", showOutput)
	}
}

func TestParseRunCommandOptions_PermissionMode(t *testing.T) {
	options, err := parseRunCommandOptions([]string{"--permission-mode", "dontAsk", "--task", "demo-task", "Analyze repository"})
	if err != nil {
		t.Fatalf("parse run command options failed: %v", err)
	}
	if options.PermissionMode != runtime.PermissionModeDontAsk {
		t.Fatalf("expected dontAsk permission mode, got %q", options.PermissionMode)
	}
	if options.TaskID != "demo-task" {
		t.Fatalf("expected task id demo-task, got %q", options.TaskID)
	}
	if options.Input != "Analyze repository" {
		t.Fatalf("expected input to be preserved, got %q", options.Input)
	}
}

func TestParseRunCommandOptions_ExtraDashesOnKnownFlags(t *testing.T) {
	options, err := parseRunCommandOptions([]string{"--new-task", "----permission-mode", "acceptEdits", "Build an in-process LRU cache named lrux"})
	if err != nil {
		t.Fatalf("parse run command options failed: %v", err)
	}
	if !options.ForceNewTask {
		t.Fatal("expected --new-task to be recognized")
	}
	if options.PermissionMode != runtime.PermissionModeAcceptEdits {
		t.Fatalf("expected acceptEdits, got %q", options.PermissionMode)
	}
	if options.Input != "Build an in-process LRU cache named lrux" {
		t.Fatalf("extra-dash flags leaked into input: %q", options.Input)
	}
}

func TestParseRunCommandOptions_FromFile(t *testing.T) {
	options, err := parseRunCommandOptions([]string{"--from-file", "plan.md"})
	if err != nil {
		t.Fatalf("parse run command options failed: %v", err)
	}
	if options.InputFile != "plan.md" {
		t.Fatalf("expected input file plan.md, got %q", options.InputFile)
	}
}

func TestParseRunCommandOptions_FromFileWithExtra(t *testing.T) {
	options, err := parseRunCommandOptions([]string{"--from-file", "phase1.md", "继续推进 phase1.md"})
	if err != nil {
		t.Fatalf("from-file plus leftover NL must not usage-error: %v", err)
	}
	if options.InputFile != "phase1.md" {
		t.Fatalf("expected input file phase1.md, got %q", options.InputFile)
	}
	if options.Input != "继续推进 phase1.md" {
		t.Fatalf("expected leftover input preserved for merge, got %q", options.Input)
	}
}

func TestRunWithoutArgsStartsREPL(t *testing.T) {
	originalStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe failed: %v", err)
	}
	defer func() {
		os.Stdin = originalStdin
		_ = reader.Close()
		_ = writer.Close()
	}()
	if _, err := writer.WriteString("/exit\n"); err != nil {
		t.Fatalf("write repl input failed: %v", err)
	}
	_ = writer.Close()
	os.Stdin = reader
	output, runErr := captureRunOutputAllowError(t, nil)
	if runErr != nil {
		t.Fatalf("run without args failed: %v\n%s", runErr, output)
	}
	if !strings.Contains(output, "Avatars REPL started") {
		t.Fatalf("expected bare run to start repl, got %s", output)
	}
}

func TestRunIntent_AnswersLocalProjectQuestionBeforeActionMap(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Demo\n\nA small demo project.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	output := captureRunOutput(t, []string{"intent", "这个项目是干啥的"})
	if !strings.Contains(output, "Project overview:") {
		t.Fatalf("expected local direct answer, got %s", output)
	}
	if strings.Contains(output, "Intent route:") {
		t.Fatalf("expected local answer to bypass action map route, got %s", output)
	}
}

func TestRunIntent_StillRoutesSpecificCliAction(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	output := captureRunOutput(t, []string{"intent", "verify current project"})
	if !strings.Contains(output, "Intent route: run the default verifier") {
		t.Fatalf("expected specific CLI action route, got %s", output)
	}
	if !strings.Contains(output, "Executing: avatars verify") {
		t.Fatalf("expected verifier execution, got %s", output)
	}
}

func TestRunProgressLine_ReportsReadAndLLMProgress(t *testing.T) {
	readLine := runProgressLine(events.Envelope{
		Type:    "tool.requested",
		Payload: map[string]any{"tool": "read", "path": "README.md"},
	})
	if readLine != "Reading: README.md" {
		t.Fatalf("expected read progress line, got %q", readLine)
	}

	llmLine := runProgressLine(events.Envelope{
		Type:    "llm.started",
		Payload: map[string]any{"provider": "deepseek"},
	})
	if llmLine != "LLM drafting with deepseek" {
		t.Fatalf("expected llm progress line, got %q", llmLine)
	}

	avatarLine := runProgressLine(events.Envelope{
		Type: "avatar.spawned",
		Payload: map[string]any{
			"role":           "Researcher",
			"responsibility": "survey repository context and collect facts",
		},
	})
	if avatarLine != "Avatar Researcher: survey repository context and collect facts" {
		t.Fatalf("expected avatar progress line, got %q", avatarLine)
	}

	explorationLine := runProgressLine(events.Envelope{
		Type: "repository.exploration_completed",
		Payload: map[string]any{
			"summary": "Researcher selected 4 high-signal files for targeted read.",
		},
	})
	if explorationLine != "Researcher selected 4 high-signal files for targeted read." {
		t.Fatalf("expected exploration progress line, got %q", explorationLine)
	}

	roundLine := runProgressLine(events.Envelope{
		Type: "repository.exploration_round_started",
		Payload: map[string]any{
			"round":  1,
			"title":  "explicit request targets",
			"reason": "user named README.md",
		},
	})
	if roundLine != "Researcher pass 1: explicit request targets. Reason: user named README.md" {
		t.Fatalf("expected exploration round progress line, got %q", roundLine)
	}

	nodeLine := runProgressLine(events.Envelope{
		Type: "workflow.node_activated",
		Payload: map[string]any{
			"assigned_role": "Researcher",
			"title":         "Survey current repository context",
		},
	})
	if nodeLine != "Researcher starts: Survey current repository context. This is the current checkpoint." {
		t.Fatalf("expected human node progress line, got %q", nodeLine)
	}

	nodeCompleteLine := runProgressLine(events.Envelope{
		Type: "workflow.node_completed",
		Payload: map[string]any{
			"assigned_role": "Researcher",
			"node_title":    "Survey current repository context",
		},
	})
	if nodeCompleteLine != "Node complete: Researcher -> Survey current repository context. Next step can resume from this checkpoint." {
		t.Fatalf("expected human node completion line, got %q", nodeCompleteLine)
	}

	skillPreviewLine := runProgressLine(events.Envelope{
		Type:    "skill.candidate_prepared",
		Payload: map[string]any{"name": "Task Survey Skill"},
	})
	if skillPreviewLine != "Skill draft ready: Task Survey Skill" {
		t.Fatalf("expected skill preview progress line, got %q", skillPreviewLine)
	}
}

func TestC62_FormatRunLLMFailure_EmptyFallbacks(t *testing.T) {
	got := formatRunLLMFailure(nil)
	if got != "" {
		t.Fatalf("nil err should not print hint, got %q", got)
	}
	plain := formatRunLLMFailure(errors.New("permission denied"))
	if plain != "" {
		t.Fatalf("non-LLM errors should not print fallback hint, got %q", plain)
	}
	annotated := llm.AnnotatePrimaryFailureWithoutFallbacks(errors.New("401 unauthorized"))
	hint := formatRunLLMFailure(annotated)
	if !strings.Contains(hint, "Primary LLM provider failed") || !strings.Contains(hint, "fallback_providers") {
		t.Fatalf("hint=%q", hint)
	}
}
