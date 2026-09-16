package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/events"
	memstore "avatars/internal/memory"
	"avatars/internal/registry"
	"avatars/internal/tools"
	"avatars/internal/transcript"
	"avatars/internal/verification"
)

type failingTool struct {
	name string
	err  error
}

func (t failingTool) Name() string {
	return t.name
}

func (t failingTool) IsConcurrencySafe(input any) bool {
	return true
}

func (t failingTool) Call(ctx context.Context, input any) (tools.Result, error) {
	return tools.Result{}, t.err
}

func TestVerificationTargetForTool_ShellReadOnlyCommandSkipped(t *testing.T) {
	tempDir := t.TempDir()
	target, shouldVerify := verificationTargetForTool("shell", tools.ShellInput{
		Command:       []string{"go", "test", "./..."},
		WorkingDir:    tempDir,
		TimeoutMillis: 1000,
	})
	if shouldVerify {
		t.Fatalf("expected read-only shell command to skip verification, got %+v", target)
	}

	target, shouldVerify = verificationTargetForTool("shell", tools.ShellInput{
		Command:       []string{"cmd", "/c", "echo", "hello"},
		WorkingDir:    tempDir,
		TimeoutMillis: 1000,
	})
	if shouldVerify {
		t.Fatalf("expected echo wrapper to skip verification, got %+v", target)
	}
}

func TestVerificationTargetForTool_ShellGuardedCommandTriggersVerification(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/shellverify\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	target, shouldVerify := verificationTargetForTool("shell", tools.ShellInput{
		Command:       []string{"cmd", "/c", "echo", "hello", ">", "note.txt"},
		WorkingDir:    tempDir,
		TimeoutMillis: 1000,
	})
	if !shouldVerify {
		t.Fatal("expected mutating shell command to require verification")
	}
	if target.trigger != "tool_shell_guarded" {
		t.Fatalf("expected shell trigger, got %+v", target)
	}
	if !strings.Contains(target.commandSummary, "note.txt") {
		t.Fatalf("expected command summary to mention note.txt, got %q", target.commandSummary)
	}
	expectedPath := filepath.Join(tempDir, "note.txt")
	if len(target.changedFiles) != 1 || target.changedFiles[0] != expectedPath {
		t.Fatalf("expected shell changed_files to include %q, got %+v", expectedPath, target.changedFiles)
	}
}

func TestMaybeRunVerification_ShellGuardedCommandEmitsEvents(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/shellverify\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-shell-verify")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	engine := &Engine{
		tools:      registry.NewToolRegistry(),
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-shell-verify",
		verifier: func(workingDir string, includeRace bool) verifierRunner {
			return stubVerifierRunner{report: verification.Report{
				Verdict: verification.VerdictPass,
				Summary: "Verification finished with PASS. 1 passed, 0 partial, 0 failed.",
				Checks: []verification.CheckEvidence{{
					Name:           "go test",
					CommandRun:     "go test ./...",
					OutputObserved: "ok\tavatars/internal/runtime",
					Expected:       "All package tests should pass.",
					Actual:         "Command completed successfully.",
					Result:         verification.VerdictPass,
				}},
			}}
		},
	}

	err = engine.maybeRunVerification(context.Background(), toolCallEnvelope{runID: "run-1", taskID: "task-1", avatarID: "avatar-builder", phase: "executing"}, "shell", tools.ShellInput{
		Command:       []string{"cmd", "/c", "echo", "hello", ">", "note.txt"},
		WorkingDir:    tempDir,
		TimeoutMillis: 1000,
	})
	if err != nil {
		t.Fatalf("expected verification success, got %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush transcript failed: %v", err)
	}

	content, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "verification.started") {
		t.Fatalf("expected transcript to contain verification.started, got %s", text)
	}
	if !strings.Contains(text, `"node_id":"node-build"`) || !strings.Contains(text, `"node_role":"Builder"`) {
		t.Fatalf("expected verification transcript to carry workflow node coordinates, got %s", text)
	}
	if !strings.Contains(text, "tool_shell_guarded") {
		t.Fatalf("expected transcript to contain tool_shell_guarded trigger, got %s", text)
	}
	if !strings.Contains(text, "verification.completed") {
		t.Fatalf("expected transcript to contain verification.completed, got %s", text)
	}
	if !strings.Contains(text, "\"changed_files\"") || !strings.Contains(text, "note.txt") {
		t.Fatalf("expected transcript to contain shell changed_files evidence, got %s", text)
	}
	snapshot, err := memoryStore.LoadSnapshot("session-shell-verify")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	if snapshot.Verification == nil {
		t.Fatal("expected verification snapshot in hot memory")
	}
	if snapshot.Verification.Verdict != "PASS" {
		t.Fatalf("expected PASS verification snapshot, got %q", snapshot.Verification.Verdict)
	}
	if snapshot.Verification.ReportPath == "" {
		t.Fatal("expected verification artifact path in hot memory")
	}
	if len(snapshot.VerificationHistory) != 1 {
		t.Fatalf("expected verification history length 1, got %d", len(snapshot.VerificationHistory))
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected evaluation record length 1, got %d", len(snapshot.EvaluationRecords))
	}
	if snapshot.EvaluationRecords[0].Tool != "shell" {
		t.Fatalf("expected evaluation record tool shell, got %q", snapshot.EvaluationRecords[0].Tool)
	}
	if snapshot.EvaluationRecords[0].Kind != "verifier_verdict" {
		t.Fatalf("expected evaluation record kind verifier_verdict, got %q", snapshot.EvaluationRecords[0].Kind)
	}
	reportContent, err := os.ReadFile(snapshot.Verification.ReportPath)
	if err != nil {
		t.Fatalf("read verification artifact failed: %v", err)
	}
	if !strings.Contains(string(reportContent), "\"verdict\": \"PASS\"") {
		t.Fatalf("expected verification artifact to include PASS verdict, got %s", string(reportContent))
	}
}

func TestMaybeRunVerification_ShellReadOnlyCommandSkipsEvents(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-shell-readonly")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	verifierCalled := false
	engine := &Engine{
		tools:      registry.NewToolRegistry(),
		transcript: writer,
		store:      store,
		sessionID:  "session-shell-readonly",
		verifier: func(workingDir string, includeRace bool) verifierRunner {
			verifierCalled = true
			return stubVerifierRunner{}
		},
	}

	err = engine.maybeRunVerification(context.Background(), toolCallEnvelope{runID: "run-2", taskID: "task-2", phase: "executing"}, "shell", tools.ShellInput{
		Command:       []string{"go", "test", "./..."},
		WorkingDir:    tempDir,
		TimeoutMillis: 1000,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if verifierCalled {
		t.Fatal("expected verifier to remain unused for read-only shell command")
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush transcript failed: %v", err)
	}
	content, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	if strings.Contains(string(content), "verification.started") {
		t.Fatalf("expected no verification events, got %s", string(content))
	}
}

func TestMaybeRunVerification_PersistsCheckPassiveFeedback(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/shellverifyfeedback\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-shell-check-feedback")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	engine := &Engine{
		tools:      registry.NewToolRegistry(),
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-shell-check-feedback",
		verifier: func(workingDir string, includeRace bool) verifierRunner {
			return stubVerifierRunner{report: verification.Report{
				Verdict: verification.VerdictPartial,
				Summary: "Verification finished with PARTIAL. 0 passed, 1 partial, 0 failed.",
				Checks: []verification.CheckEvidence{{
					Name:           "go test -race ./...",
					CommandRun:     "go test -race ./...",
					OutputObserved: "warning: CGO is not enabled",
					Expected:       "Race checks should run successfully.",
					Actual:         "Command completed with environment limitations.",
					Result:         verification.VerdictPartial,
				}},
			}}
		},
	}

	err = engine.maybeRunVerification(context.Background(), toolCallEnvelope{runID: "run-check-feedback", taskID: "task-check-feedback", phase: "executing"}, "shell", tools.ShellInput{
		Command:       []string{"cmd", "/c", "echo", "hello", ">", "note.txt"},
		WorkingDir:    tempDir,
		TimeoutMillis: 1000,
	})
	if err != nil {
		t.Fatalf("expected verification success, got %v", err)
	}

	snapshot, err := memoryStore.LoadSnapshot("session-shell-check-feedback")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 2 {
		t.Fatalf("expected evaluation record length 2, got %d", len(snapshot.EvaluationRecords))
	}
	var matched bool
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause != "verification_check_partial" {
			continue
		}
		if record.Source != "verification_check" {
			t.Fatalf("expected verification_check source for partial check feedback, got %q", record.Source)
		}
		if !strings.Contains(record.Summary, "CGO is not enabled") {
			t.Fatalf("expected check feedback summary to include check output, got %q", record.Summary)
		}
		matched = true
		break
	}
	if !matched {
		t.Fatalf("expected one evaluation record with cause verification_check_partial, got %+v", snapshot.EvaluationRecords)
	}
}

func TestInvokeTool_PermissionModeDontAskDeniesMutatingWrite(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-permission-mode")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-permission-mode",
		permissionMode: PermissionModeDontAsk,
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-permission-mode", taskID: "task-tool-permission-mode", phase: "executing"}, "write", "file_write", tools.WriteInput{Path: "notes.txt", Content: "blocked", WorkingDir: tempDir})
	if err == nil {
		t.Fatal("expected permission mode to deny mutating write")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected permission denied error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "notes.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected denied write to avoid filesystem mutation, got %v", statErr)
	}

	snapshot, err := memoryStore.LoadSnapshot("session-tool-permission-mode")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected evaluation record length 1, got %d", len(snapshot.EvaluationRecords))
	}
	record := snapshot.EvaluationRecords[0]
	if record.Cause != "tool_permission_denied" {
		t.Fatalf("expected tool_permission_denied cause, got %q", record.Cause)
	}
	joinedDetails := strings.Join(record.Details, "\n")
	if !strings.Contains(joinedDetails, "decision_source: runtime_permission_mode") {
		t.Fatalf("expected runtime permission mode decision source, got %+v", record.Details)
	}
	if !strings.Contains(joinedDetails, "permission_mode: dontAsk") {
		t.Fatalf("expected permission mode detail, got %+v", record.Details)
	}
}

func TestInvokeTool_PermissionModeDefaultMarksAwaitingApprovalForMutatingWrite(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-awaiting-approval")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-awaiting-approval",
		permissionMode: PermissionModeDefault,
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-awaiting-approval", taskID: "task-tool-awaiting-approval", phase: "executing"}, "write", "file_write", tools.WriteInput{Path: "notes.txt", Content: "blocked", WorkingDir: tempDir})
	if err == nil {
		t.Fatal("expected permission mode default to require approval for mutating write")
	}
	if !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected approval required error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "notes.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected awaiting approval write to avoid filesystem mutation, got %v", statErr)
	}

	history := store.History()
	if len(history) < 3 {
		t.Fatalf("expected tool awaiting approval and lifecycle update events, got %+v", history)
	}
	if history[len(history)-2].Type != "tool.awaiting_approval" {
		t.Fatalf("expected penultimate event tool.awaiting_approval, got %+v", history)
	}
	if history[len(history)-1].Type != "run.lifecycle_updated" {
		t.Fatalf("expected final event run.lifecycle_updated, got %+v", history)
	}
	snapshot, err := memoryStore.LoadSnapshot("session-tool-awaiting-approval")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 2 {
		t.Fatalf("expected evaluation record length 2, got %d", len(snapshot.EvaluationRecords))
	}
	var record *memstore.EvaluationRecord
	var lifecycleRecord *memstore.EvaluationRecord
	for index := range snapshot.EvaluationRecords {
		switch snapshot.EvaluationRecords[index].Cause {
		case "tool_approval_required":
			record = &snapshot.EvaluationRecords[index]
		case "run_lifecycle_updated":
			lifecycleRecord = &snapshot.EvaluationRecords[index]
		}
	}
	if lifecycleRecord == nil {
		t.Fatalf("expected run lifecycle evaluation record, got %+v", snapshot.EvaluationRecords)
	}
	if record == nil {
		t.Fatalf("expected tool approval required evaluation record, got %+v", snapshot.EvaluationRecords)
	}
	if record.Cause != "tool_approval_required" {
		t.Fatalf("expected tool_approval_required cause, got %q", record.Cause)
	}
	joinedDetails := strings.Join(record.Details, "\n")
	if !strings.Contains(joinedDetails, "decision_source: runtime_permission_mode") {
		t.Fatalf("expected runtime permission mode decision source, got %+v", record.Details)
	}
	if !strings.Contains(joinedDetails, "permission_mode: default") {
		t.Fatalf("expected permission mode detail, got %+v", record.Details)
	}
}

func TestInvokeTool_AcceptEditsRequiresApprovalForCodeMutation(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-code-confirmation")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryDir := filepath.Join(tempDir, ".avatars", "memory")
	memoryStore, err := memstore.NewSQLiteStore(memoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-code-confirmation",
		permissionMode: PermissionModeAcceptEdits,
	}
	input := tools.WriteInput{
		Path:            "main.go",
		Content:         "package main\n",
		WorkingDir:      tempDir,
		Intent:          "write code target",
		ExpectedTargets: []string{"main.go"},
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-code-confirmation", taskID: "task-code-confirmation", avatarID: "avatar-builder", phase: "executing"}, "write", "file_write", input)
		if err != nil {
			t.Fatalf("acceptEdits should allow Builder code write with preflight metadata, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(tempDir, "main.go")); os.IsNotExist(statErr) {
			t.Fatalf("acceptEdits should write code file immediately, got %v", statErr)
		}
}

func TestInvokeTool_BuilderMutatingWriteRequiresCodingPreflight(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-builder-preflight")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-builder-preflight",
		permissionMode: PermissionModeDefault,
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-builder-preflight", taskID: "task-builder-preflight", avatarID: "avatar-builder", phase: "executing"}, "write", "file_write", tools.WriteInput{Path: "notes.txt", Content: "blocked", WorkingDir: tempDir})
	if err == nil {
		t.Fatal("expected builder write without preflight metadata to be denied in default mode")
	}
	if !strings.Contains(err.Error(), "coding preflight metadata") || !strings.Contains(err.Error(), "intent") || !strings.Contains(err.Error(), "expected_targets") {
		t.Fatalf("expected coding preflight denial, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "notes.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected denied builder write to avoid filesystem mutation, got %v", statErr)
	}

	history := store.History()
	if len(history) < 2 || history[len(history)-1].Type != "tool.denied" {
		t.Fatalf("expected final event tool.denied, got %+v", history)
	}
	policy, _ := history[len(history)-1].Payload["policy"].(map[string]any)
	if fmt.Sprint(policy["decision_source"]) != "runtime_coding_preflight" {
		t.Fatalf("expected coding preflight policy source, got %+v", policy)
	}
	snapshot, err := memoryStore.LoadSnapshot("session-builder-preflight")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected evaluation record length 1, got %d", len(snapshot.EvaluationRecords))
	}
	record := snapshot.EvaluationRecords[0]
	if record.Cause != "tool_permission_denied" {
		t.Fatalf("expected tool_permission_denied cause, got %q", record.Cause)
	}
	joinedDetails := strings.Join(record.Details, "\n")
	if !strings.Contains(joinedDetails, "decision_source: runtime_coding_preflight") {
		t.Fatalf("expected coding preflight decision source, got %+v", record.Details)
	}
}

func TestInvokeTool_BuilderMutatingWriteWithCodingPreflightAllowed(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-builder-preflight-allowed")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-builder-preflight-allowed",
		permissionMode: PermissionModeAcceptEdits,
		verifier: func(workingDir string, includeRace bool) verifierRunner {
			return stubVerifierRunner{report: verification.Report{
				Verdict: verification.VerdictPass,
				Summary: "Verification finished with PASS. 1 passed, 0 partial, 0 failed.",
			}}
		},
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-builder-preflight-allowed", taskID: "task-builder-preflight-allowed", avatarID: "avatar-builder", phase: "executing"}, "write", "file_write", tools.WriteInput{
		Path:            "notes.txt",
		Content:         "allowed",
		WorkingDir:      tempDir,
		Intent:          "write scoped implementation note",
		ExpectedTargets: []string{"notes.txt"},
	})
	if err != nil {
		t.Fatalf("expected builder write with preflight metadata to succeed, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "notes.txt")); statErr != nil {
		t.Fatalf("expected builder write to create file, got %v", statErr)
	}
	rollbackDir := filepath.Join(tempDir, ".avatars", "memory", "rollback")
	entries, err := os.ReadDir(rollbackDir)
	if err != nil {
		t.Fatalf("read rollback artifact dir failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected rollback artifact to be written")
	}
	artifactContent, err := os.ReadFile(filepath.Join(rollbackDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read rollback artifact failed: %v", err)
	}
	var artifact CodingRollbackArtifact
	if err := json.Unmarshal(artifactContent, &artifact); err != nil {
		t.Fatalf("decode rollback artifact failed: %v", err)
	}
	if strings.TrimSpace(artifact.AfterSHA256) == "" {
		t.Fatalf("expected rollback artifact to record after sha256, got %+v", artifact)
	}
}

func TestInvokeTool_PermissionModeDefaultPersistsApprovalReplayArtifact(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-awaiting-approval-artifact")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryDir := filepath.Join(tempDir, ".avatars", "memory")
	memoryStore, err := memstore.NewSQLiteStore(memoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-awaiting-approval-artifact",
		permissionMode: PermissionModeDefault,
	}
	input := tools.WriteInput{Path: "notes.txt", Content: "blocked", WorkingDir: tempDir}
	approvalKey := toolApprovalKey("write", "file_write", input)

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-awaiting-approval-artifact", taskID: "task-tool-awaiting-approval-artifact", phase: "executing"}, "write", "file_write", input)
	if err == nil {
		t.Fatal("expected permission mode default to require approval")
	}
	request, err := LoadPendingApprovalRequest(memoryDir, approvalKey)
	if err != nil {
		t.Fatalf("load pending approval request failed: %v", err)
	}
	if request.Tool != "write" || request.Operation != "file_write" {
		t.Fatalf("expected write/file_write replay request, got %+v", request)
	}
	if request.Write == nil || request.Write.Path != "notes.txt" || request.Write.Content != "blocked" {
		t.Fatalf("expected replayable write payload, got %+v", request)
	}
	if request.SuspendedAction == nil {
		t.Fatalf("expected suspended action frame in replay artifact, got %+v", request)
	}
	if request.SuspendedAction.InputDigest == "" {
		t.Fatalf("expected suspended action digest, got %+v", request.SuspendedAction)
	}
	if request.SuspendedAction.PausePointID == "" || request.Continuation == nil || request.Continuation.PausePointID != request.SuspendedAction.PausePointID {
		t.Fatalf("expected pending approval to carry matching pause point identity, got suspended=%+v continuation=%+v", request.SuspendedAction, request.Continuation)
	}
	snapshot, err := memoryStore.LoadSnapshot("session-tool-awaiting-approval-artifact")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	var foundApprovalArtifact bool
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause != "tool_approval_required" {
			continue
		}
		if strings.Contains(strings.Join(record.Details, "\n"), "approval_request_artifact:") {
			foundApprovalArtifact = true
			break
		}
	}
	if !foundApprovalArtifact {
		t.Fatalf("expected approval artifact detail, got %+v", snapshot.EvaluationRecords)
	}
}

func TestContinueApprovedToolCall_RejectsTamperedSuspendedActionInput(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-continuation-tampered")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryDir := filepath.Join(tempDir, ".avatars", "memory")
	memoryStore, err := memstore.NewSQLiteStore(memoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-continuation-tampered",
		taskID:         "task-tool-continuation-tampered",
		permissionMode: PermissionModeDefault,
	}
	input := tools.WriteInput{Path: "notes.txt", Content: "blocked", WorkingDir: tempDir}
	approvalKey := toolApprovalKey("write", "file_write", input)
	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-continuation-tampered", taskID: "task-tool-continuation-tampered", phase: "executing"}, "write", "file_write", input)
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected initial write to await approval, got %v", err)
	}
	request, err := LoadPendingApprovalRequest(memoryDir, approvalKey)
	if err != nil {
		t.Fatalf("load pending approval request failed: %v", err)
	}
	request.Write.Content = "tampered"
	if _, err := engine.ContinueApprovedToolCall(context.Background(), request, ToolCallOptions{}); err == nil {
		t.Fatal("expected tampered suspended action continuation to fail")
	} else if !strings.Contains(err.Error(), "input digest mismatch") {
		t.Fatalf("expected input digest mismatch, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "notes.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected tampered continuation to avoid filesystem mutation, got %v", statErr)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("task-tool-continuation-tampered")
	if err != nil {
		t.Fatalf("load tampered task snapshot failed: %v", err)
	}
	policy := memstore.LatestResumePolicyEvaluation(snapshot.EvaluationRecords)
	if policy == nil || memstore.EvaluationRecordReason(*policy) != "suspended_action_mismatch" || memstore.EvaluationRecordRetryable(*policy) != "false" {
		t.Fatalf("expected non-retryable digest mismatch policy evaluation, got %+v", policy)
	}
}

func TestContinueApprovedToolCall_UpdatesOriginRunLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-continuation")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryDir := filepath.Join(tempDir, ".avatars", "memory")
	memoryStore, err := memstore.NewSQLiteStore(memoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-continuation",
		taskID:         "task-tool-continuation",
		permissionMode: PermissionModeDefault,
	}
	input := tools.WriteInput{Path: "notes.txt", Content: "approved via continuation", WorkingDir: tempDir}
	approvalKey := toolApprovalKey("write", "file_write", input)

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-continuation", taskID: "task-tool-continuation", phase: "executing"}, "write", "file_write", input)
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected initial write to await approval, got %v", err)
	}
	request, err := LoadPendingApprovalRequest(memoryDir, approvalKey)
	if err != nil {
		t.Fatalf("load pending approval request failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "approval-session", RunID: "approval-run", TaskID: "task-tool-continuation", Tool: "write", Kind: "passive_feedback", Verdict: "PASS", Cause: "tool_approval_granted", Summary: "Tool write/file_write approval granted for the latest pending request.", Source: "task_operator_command", Details: []string{"operation: file_write", "decision_source: task_operator_command", "approval_key: " + approvalKey, "permission_mode: default"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval decision failed: %v", err)
	}
	result, err := engine.ContinueApprovedToolCall(context.Background(), request, ToolCallOptions{})
	if err != nil {
		t.Fatalf("expected approved continuation to succeed, got %v", err)
	}
	if result.ContinuationRun == "" || result.ContinuationTask != "task-tool-continuation" {
		t.Fatalf("expected continuation lineage in result, got %+v", result)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush transcript failed: %v", err)
	}
	content, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "run.lifecycle_updated") {
		t.Fatalf("expected lifecycle update in transcript, got %s", text)
	}
	if !strings.Contains(text, "\"status\":\"continued\"") && !strings.Contains(text, "\"status\": \"continued\"") {
		t.Fatalf("expected continued lifecycle status in transcript, got %s", text)
	}
	if !strings.Contains(text, "\"origin_run_id\":\"run-tool-continuation\"") && !strings.Contains(text, "\"origin_run_id\": \"run-tool-continuation\"") {
		t.Fatalf("expected origin run lifecycle linkage in transcript, got %s", text)
	}
	if !strings.Contains(text, "tool.approval_continued") {
		t.Fatalf("expected approval continued event in transcript, got %s", text)
	}
}

func TestContinueApprovedToolCall_SkipsAlreadyCompletedReplay(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-continuation-idempotent")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryDir := filepath.Join(tempDir, ".avatars", "memory")
	memoryStore, err := memstore.NewSQLiteStore(memoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-continuation-idempotent",
		taskID:         "task-tool-continuation-idempotent",
		permissionMode: PermissionModeDefault,
	}
	input := tools.WriteInput{Path: "notes.txt", Content: "first approved replay", WorkingDir: tempDir}
	approvalKey := toolApprovalKey("write", "file_write", input)

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-continuation-idempotent", taskID: "task-tool-continuation-idempotent", phase: "executing"}, "write", "file_write", input)
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected initial write to await approval, got %v", err)
	}
	request, err := LoadPendingApprovalRequest(memoryDir, approvalKey)
	if err != nil {
		t.Fatalf("load pending approval request failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "approval-session", RunID: "approval-run", TaskID: "task-tool-continuation-idempotent", Tool: "write", Kind: "passive_feedback", Verdict: "PASS", Cause: "tool_approval_granted", Summary: "Tool write/file_write approval granted for the latest pending request.", Source: "task_operator_command", Details: []string{"operation: file_write", "decision_source: task_operator_command", "approval_key: " + approvalKey, "permission_mode: default"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval decision failed: %v", err)
	}
	first, err := engine.ContinueApprovedToolCall(context.Background(), request, ToolCallOptions{})
	if err != nil {
		t.Fatalf("expected first approved continuation to succeed, got %v", err)
	}
	if first.ContinuationRun == "" || first.TranscriptPath == "" {
		t.Fatalf("expected first continuation lineage, got %+v", first)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "notes.txt"), []byte("operator edit after replay"), 0o644); err != nil {
		t.Fatalf("write post-replay marker failed: %v", err)
	}
	completedRequest, err := LoadPendingApprovalRequest(memoryDir, approvalKey)
	if err != nil {
		t.Fatalf("reload completed pending approval request failed: %v", err)
	}
	if completedRequest.Continuation == nil || completedRequest.Continuation.PausePointID == "" || completedRequest.Continuation.ResumeAttemptID == "" || len(completedRequest.Continuation.ResumeAttempts) == 0 {
		t.Fatalf("expected completed approval artifact to carry resume attempt ledger, got %+v", completedRequest.Continuation)
	}
	second, err := engine.ContinueApprovedToolCall(context.Background(), completedRequest, ToolCallOptions{})
	if err != nil {
		t.Fatalf("expected idempotent replay skip to succeed, got %v", err)
	}
	if second.ContinuationRun != first.ContinuationRun || second.TranscriptPath != first.TranscriptPath {
		t.Fatalf("expected replay skip to reuse first continuation lineage, first=%+v second=%+v", first, second)
	}
	content, err := os.ReadFile(filepath.Join(tempDir, "notes.txt"))
	if err != nil {
		t.Fatalf("read post-replay marker failed: %v", err)
	}
	if string(content) != "operator edit after replay" {
		t.Fatalf("expected second replay to avoid mutating file, got %q", string(content))
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush transcript failed: %v", err)
	}
	transcriptContent, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	if !strings.Contains(string(transcriptContent), "tool.approval_replay_skipped") {
		t.Fatalf("expected replay skip event in transcript, got %s", string(transcriptContent))
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("task-tool-continuation-idempotent")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	replay := memstore.LatestToolApprovalReplayEvaluation(snapshot.EvaluationRecords)
	if replay == nil || replay.Cause != "tool_approval_replay_skipped" {
		t.Fatalf("expected latest replay evaluation to be skipped, got %+v", replay)
	}
	latestAttempt := memstore.LatestResumeAttemptEvaluation(snapshot.EvaluationRecords)
	if latestAttempt == nil || memstore.EvaluationRecordResumeAttemptStatus(*latestAttempt) != "skipped" {
		t.Fatalf("expected skipped resume attempt evaluation to be latest, got %+v", latestAttempt)
	}
	latestPolicy := memstore.LatestResumePolicyEvaluation(snapshot.EvaluationRecords)
	if latestPolicy == nil || memstore.EvaluationRecordReason(*latestPolicy) != "already_completed" || memstore.EvaluationRecordRetryable(*latestPolicy) != "false" {
		t.Fatalf("expected completed replay policy evaluation, got %+v", latestPolicy)
	}
	if memstore.EvaluationRecordPausePointID(*latestAttempt) != completedRequest.Continuation.PausePointID {
		t.Fatalf("expected skipped resume attempt to reuse pause point %q, got %+v", completedRequest.Continuation.PausePointID, latestAttempt)
	}
	foundAttempt := false
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause == "resume_attempt_recorded" && strings.Contains(strings.Join(record.Details, "\n"), "resume_attempt_status: completed") {
			foundAttempt = true
			break
		}
	}
	if !foundAttempt {
		t.Fatalf("expected completed resume attempt evaluation, got %+v", snapshot.EvaluationRecords)
	}
}

func TestInvokeTool_PermissionModeDefaultAllowsGrantedApprovalOnRetry(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-approved")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-approved",
		permissionMode: PermissionModeDefault,
	}
	input := tools.WriteInput{Path: "notes.txt", Content: "approved", WorkingDir: tempDir}
	approvalKey := toolApprovalKey("write", "file_write", input)
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "approval-session", RunID: "approval-run", TaskID: "task-tool-approved", Tool: "write", Kind: "passive_feedback", Verdict: "PASS", Cause: "tool_approval_granted", Summary: "Tool write/file_write approval granted for the latest pending request.", Source: "task_operator_command", Details: []string{"operation: file_write", "decision_source: task_operator_command", "approval_key: " + approvalKey, "permission_mode: default"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval decision failed: %v", err)
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-approved", taskID: "task-tool-approved", phase: "executing"}, "write", "file_write", input)
	if err != nil {
		t.Fatalf("expected approved write to succeed, got %v", err)
	}
	content, err := os.ReadFile(filepath.Join(tempDir, "notes.txt"))
	if err != nil {
		t.Fatalf("read approved file failed: %v", err)
	}
	if string(content) != "approved" {
		t.Fatalf("expected approved write content, got %q", string(content))
	}
}

func TestInvokeTool_PermissionModeDefaultDeniesRejectedApprovalOnRetry(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-rejected")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-tool-rejected",
		permissionMode: PermissionModeDefault,
	}
	input := tools.WriteInput{Path: "notes.txt", Content: "rejected", WorkingDir: tempDir}
	approvalKey := toolApprovalKey("write", "file_write", input)
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "approval-session", RunID: "approval-run", TaskID: "task-tool-rejected", Tool: "write", Kind: "passive_feedback", Verdict: "FAIL", Cause: "tool_approval_denied", Summary: "Tool write/file_write approval denied for the latest pending request.", Source: "task_operator_command", Details: []string{"operation: file_write", "decision_source: task_operator_command", "approval_key: " + approvalKey, "permission_mode: default"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record denial decision failed: %v", err)
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-rejected", taskID: "task-tool-rejected", phase: "executing"}, "write", "file_write", input)
	if err == nil {
		t.Fatal("expected rejected write to be denied")
	}
	if !strings.Contains(err.Error(), "approval denied") {
		t.Fatalf("expected approval denied error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tempDir, "notes.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected rejected approval to avoid filesystem mutation, got %v", statErr)
	}
}

func TestEngineVerify_PersistsTaskScopedVerification(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "sessions"), "session-task-verify")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	engine := &Engine{
		tools:      registry.NewToolRegistry(),
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-task-verify",
		verifier: func(workingDir string, includeRace bool) verifierRunner {
			if workingDir != tempDir {
				t.Fatalf("expected verify working dir %q, got %q", tempDir, workingDir)
			}
			return stubVerifierRunner{report: verification.Report{
				Verdict: verification.VerdictPartial,
				Summary: "Verification finished with PARTIAL. 0 passed, 1 partial, 0 failed.",
				Checks: []verification.CheckEvidence{{
					Name:           "go test -race",
					CommandRun:     "go test -race ./...",
					OutputObserved: "warning: CGO is not enabled",
					Expected:       "Race-enabled tests should pass, or report a real environment limitation.",
					Actual:         "exit status 1",
					Result:         verification.VerdictPartial,
				}},
			}}
		},
	}

	result, err := engine.Verify(context.Background(), "demo-task", tempDir)
	if err != nil {
		t.Fatalf("expected partial verify to succeed, got %v", err)
	}
	if result.Report.Verdict != verification.VerdictPartial {
		t.Fatalf("expected PARTIAL report, got %+v", result.Report)
	}
	if result.ReportPath == "" {
		t.Fatalf("expected report path, got %+v", result)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("demo-task")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	if snapshot.Verification == nil || snapshot.Verification.Tool != "verify" {
		t.Fatalf("expected persisted verify snapshot, got %+v", snapshot.Verification)
	}
	if snapshot.Verification.ReportPath == "" {
		t.Fatalf("expected persisted report path, got %+v", snapshot.Verification)
	}
	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "manual_verify") {
		t.Fatalf("expected transcript to contain manual_verify trigger, got %s", text)
	}
	if !strings.Contains(text, "avatars verify --task demo-task") {
		t.Fatalf("expected transcript to contain task verify command, got %s", text)
	}
}

func TestEngineVerify_PersistsReverifyClosureEvaluation(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "sessions"), "session-task-reverify-pass")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{
		SessionID:  "session-before",
		RunID:      "run-before",
		TaskID:     "demo-task",
		Tool:       "patch",
		Verdict:    "PARTIAL",
		Summary:    "Verification finished with PARTIAL.",
		ReportPath: filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory", "verifier", "run-before-patch.json"),
		Checks:     []string{"go test -race ./... => PARTIAL"},
		UpdatedAt:  time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("record prior verification failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "session-before",
		RunID:     "run-remediation",
		TaskID:    "demo-task",
		Tool:      "write",
		Kind:      "runtime_lifecycle",
		Verdict:   "INFO",
		Cause:     "verifier_remediation_resume_attempt",
		Summary:   "Verifier remediation resume attempt recorded for reverify repair work.",
		Source:    "runtime",
		Details:   []string{"pause_point_id: pause-remediate", "pause_point_kind: verifier_remediation", "resume_attempt_id: resume-remediate", "resume_attempt_status: started", "expected_targets: note.txt, process_record.md"},
		UpdatedAt: time.Now().UTC().Add(-30 * time.Second),
	}); err != nil {
		t.Fatalf("record remediation attempt failed: %v", err)
	}
	engine := &Engine{
		tools:      registry.NewToolRegistry(),
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-task-reverify-pass",
		verifier: func(workingDir string, includeRace bool) verifierRunner {
			return stubVerifierRunner{report: verification.Report{
				Verdict: verification.VerdictPass,
				Summary: "Verification finished with PASS. 1 passed, 0 partial, 0 failed.",
				Checks: []verification.CheckEvidence{{
					Name:           "go test",
					CommandRun:     "go test ./...",
					OutputObserved: "ok\texample.com/taskverify",
					Expected:       "All package tests should pass.",
					Actual:         "Command completed successfully.",
					Result:         verification.VerdictPass,
				}},
			}}
		},
	}

	result, err := engine.Verify(context.Background(), "demo-task", tempDir)
	if err != nil {
		t.Fatalf("expected verify PASS, got %v", err)
	}
	if result.Report.Verdict != verification.VerdictPass {
		t.Fatalf("expected PASS report, got %+v", result.Report)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot("demo-task")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) == 0 {
		t.Fatal("expected persisted evaluation records")
	}
	matched := false
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause != "verification_reverify_covered" {
			continue
		}
		if record.Kind != "workflow_feedback" {
			t.Fatalf("expected workflow_feedback kind, got %q", record.Kind)
		}
		if record.Source != "verification_followup" {
			t.Fatalf("expected verification_followup source, got %q", record.Source)
		}
		if !strings.Contains(record.Summary, "PASS now covers the latest non-pass verification") {
			t.Fatalf("expected closure summary, got %q", record.Summary)
		}
		details := strings.Join(record.Details, "\n")
		if !strings.Contains(details, "remediation_pause_point_id: pause-remediate") {
			t.Fatalf("expected closure to carry remediation pause point provenance, got %+v", record.Details)
		}
		if !strings.Contains(details, "remediation_resume_attempt_id: resume-remediate") {
			t.Fatalf("expected closure to carry remediation resume attempt provenance, got %+v", record.Details)
		}
		if !strings.Contains(details, "remediation_expected_targets: note.txt, process_record.md") {
			t.Fatalf("expected closure to carry remediation targets, got %+v", record.Details)
		}
		matched = true
		break
	}
	if !matched {
		t.Fatalf("expected one reverify closure evaluation record, got %+v", snapshot.EvaluationRecords)
	}
}

func TestInvokeTool_PersistsReverifyRepairAttemptEvaluation(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "sessions"), "session-task-reverify-attempt")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{
		SessionID:  "session-before",
		RunID:      "run-before",
		TaskID:     "demo-task",
		Tool:       "patch",
		Verdict:    "PARTIAL",
		Summary:    "Verification finished with PARTIAL.",
		ReportPath: filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory", "verifier", "run-before-patch.json"),
		Checks:     []string{"go test -race ./... => PARTIAL"},
		UpdatedAt:  time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("record prior verification failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{
		SessionID:  "session-before",
		RunID:      "run-before",
		TaskID:     "demo-task",
		Tool:       "verifier",
		Kind:       "runtime_lifecycle",
		Verdict:    "INFO",
		Cause:      "verifier_remediation_pause_point",
		Summary:    "Verifier opened remediation pause point for write.",
		Source:     "verifier",
		ReportPath: filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory", "verifier", "run-before-patch.json"),
		Details:    []string{"pause_point_id: pause-remediate", "pause_point_kind: verifier_remediation", "origin_run_id: run-before", "origin_task_id: demo-task", "expected_targets: note.txt, process_record.md"},
		UpdatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record remediation pause point failed: %v", err)
	}
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:      toolRegistry,
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-task-reverify-attempt",
	}

	result, err := engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-reverify-attempt", taskID: "demo-task", phase: "executing"}, "write", "file_write", tools.WriteInput{Path: "note.txt", Content: "repair attempt", WorkingDir: tempDir})
	if err != nil {
		t.Fatalf("expected write tool success, got %v", err)
	}
	if !strings.Contains(result.Content, "Wrote") {
		t.Fatalf("expected write output, got %q", result.Content)
	}

	snapshot, err := memoryStore.LoadLatestTaskSnapshot("demo-task")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	matched := false
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause != "verification_reverify_fix_attempt" {
			continue
		}
		if record.Kind != "workflow_feedback" {
			t.Fatalf("expected workflow_feedback kind, got %q", record.Kind)
		}
		if record.Source != "verification_followup" {
			t.Fatalf("expected verification_followup source, got %q", record.Source)
		}
		if !strings.Contains(record.Summary, "awaits reverify") {
			t.Fatalf("expected fix attempt summary, got %q", record.Summary)
		}
		if len(record.Details) == 0 || !strings.Contains(strings.Join(record.Details, "\n"), "path: note.txt") {
			t.Fatalf("expected fix attempt details to mention note.txt, got %+v", record.Details)
		}
		matched = true
		break
	}
	if !matched {
		t.Fatalf("expected one reverify repair attempt evaluation record, got %+v", snapshot.EvaluationRecords)
	}
}

func TestInvokeTool_PersistsReverifyRepairIntentEvaluation(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "sessions"), "session-task-reverify-intent-attempt")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{
		SessionID:  "session-before",
		RunID:      "run-before",
		TaskID:     "demo-task",
		Tool:       "patch",
		Verdict:    "PARTIAL",
		Summary:    "Verification finished with PARTIAL.",
		ReportPath: filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory", "verifier", "run-before-patch.json"),
		Checks:     []string{"go test -race ./... => PARTIAL"},
		UpdatedAt:  time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("record prior verification failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{
		SessionID:  "session-before",
		RunID:      "run-before",
		TaskID:     "demo-task",
		Tool:       "verifier",
		Kind:       "runtime_lifecycle",
		Verdict:    "INFO",
		Cause:      "verifier_remediation_pause_point",
		Summary:    "Verifier opened remediation pause point for write.",
		Source:     "verifier",
		ReportPath: filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory", "verifier", "run-before-patch.json"),
		Details:    []string{"pause_point_id: pause-remediate", "pause_point_kind: verifier_remediation", "origin_run_id: run-before", "origin_task_id: demo-task", "expected_targets: note.txt, process_record.md"},
		UpdatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record remediation pause point failed: %v", err)
	}
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:      toolRegistry,
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-task-reverify-intent-attempt",
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-reverify-intent-attempt", taskID: "demo-task", phase: "executing"}, "write", "file_write", tools.WriteInput{Path: "note.txt", Content: "repair attempt", WorkingDir: tempDir, ProposalID: "remediate-latest-non-pass", Intent: "address the failing verifier warning", ExpectedTargets: []string{"note.txt", "process_record.md"}})
	if err != nil {
		t.Fatalf("expected write tool success, got %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush transcript failed: %v", err)
	}
	content, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	if strings.Count(string(content), "\"proposal_id\":\"remediate-latest-non-pass\"") < 6 {
		t.Fatalf("expected transcript payloads to include proposal_id in both flat payloads and action args, got %s", string(content))
	}

	snapshot, err := memoryStore.LoadLatestTaskSnapshot("demo-task")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	matched := false
	foundResumeAttempt := false
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause != "verification_reverify_fix_attempt" {
			if record.Cause == "verifier_remediation_resume_attempt" {
				foundResumeAttempt = true
				if record.Kind != "runtime_lifecycle" || record.Source != "runtime" {
					t.Fatalf("expected runtime lifecycle remediation attempt record, got %+v", record)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "pause_point_kind: verifier_remediation") {
					t.Fatalf("expected remediation attempt details to include verifier remediation pause kind, got %+v", record.Details)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "retryable: true") {
					t.Fatalf("expected remediation attempt details to include retryable true, got %+v", record.Details)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "resume_attempt_status: started") {
					t.Fatalf("expected remediation attempt details to include started status, got %+v", record.Details)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "reason: verifier_targets_explicitly_updated") {
					t.Fatalf("expected remediation attempt details to include explicit target update reason, got %+v", record.Details)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "expected_targets: note.txt, process_record.md") {
					t.Fatalf("expected remediation attempt details to include expected targets, got %+v", record.Details)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "verification_report_path:") {
					t.Fatalf("expected remediation attempt details to include verification report path, got %+v", record.Details)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "recovery_boundary_guard_status: guarded_ready") {
					t.Fatalf("expected remediation attempt details to include guarded boundary status, got %+v", record.Details)
				}
				if !strings.Contains(strings.Join(record.Details, "\n"), "recovery_boundary_required_authority: verifier_reverify") {
					t.Fatalf("expected remediation attempt details to include guarded boundary authority, got %+v", record.Details)
				}
				continue
			}
			continue
		}
		if !strings.Contains(record.Summary, "completed to address the failing verifier warning while") {
			t.Fatalf("expected fix attempt summary to include remediation intent, got %q", record.Summary)
		}
		if !strings.Contains(strings.Join(record.Details, "\n"), "intent: address the failing verifier warning") {
			t.Fatalf("expected fix attempt details to include remediation intent, got %+v", record.Details)
		}
		if !strings.Contains(strings.Join(record.Details, "\n"), "proposal_id: remediate-latest-non-pass") {
			t.Fatalf("expected fix attempt details to include proposal id, got %+v", record.Details)
		}
		if !strings.Contains(strings.Join(record.Details, "\n"), "expected_targets: note.txt, process_record.md") {
			t.Fatalf("expected fix attempt details to include expected targets, got %+v", record.Details)
		}
		matched = true
		break
	}
	if !matched {
		t.Fatalf("expected one reverify repair attempt evaluation record, got %+v", snapshot.EvaluationRecords)
	}
	if closure := memstore.LatestReverifyClosureEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); closure != nil {
		t.Fatalf("expected no reverify closure before PASS, got %+v", closure)
	}
	if !foundResumeAttempt {
		t.Fatalf("expected verifier remediation resume attempt record, got %+v", snapshot.EvaluationRecords)
	}
	remediation := memstore.LatestVerifierRemediationPausePointEvaluation(snapshot.EvaluationRecords)
	if remediation == nil {
		t.Fatalf("expected remediation pause point evaluation, got %+v", snapshot.EvaluationRecords)
	}
	if memstore.EvaluationRecordPausePointID(*remediation) != "pause-remediate" {
		t.Fatalf("expected remediation pause point id, got %+v", remediation)
	}
	if memstore.EvaluationRecordDetailValue(*remediation, "pause_point_kind") != "verifier_remediation" {
		t.Fatalf("expected verifier remediation pause point kind, got %+v", remediation)
	}
}

func TestInvokeTool_DoesNotRecordVerifierRemediationAttemptWithoutGuardedBoundary(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "sessions"), "session-task-reverify-no-boundary")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{
		SessionID:  "session-before",
		RunID:      "run-before",
		TaskID:     "demo-task",
		Tool:       "patch",
		Verdict:    "PARTIAL",
		Summary:    "Verification finished with PARTIAL.",
		ReportPath: filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory", "verifier", "run-before-patch.json"),
		Checks:     []string{"go test -race ./... => PARTIAL"},
		UpdatedAt:  time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("record prior verification failed: %v", err)
	}
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:      toolRegistry,
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-task-reverify-no-boundary",
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-reverify-no-boundary", taskID: "demo-task", phase: "executing"}, "write", "file_write", tools.WriteInput{Path: "note.txt", Content: "repair attempt", WorkingDir: tempDir, ProposalID: "remediate-latest-non-pass", Intent: "address the failing verifier warning", ExpectedTargets: []string{"note.txt"}})
	if err != nil {
		t.Fatalf("expected write tool success, got %v", err)
	}

	snapshot, err := memoryStore.LoadLatestTaskSnapshot("demo-task")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	foundFixAttempt := false
	for _, record := range snapshot.EvaluationRecords {
		switch record.Cause {
		case "verification_reverify_fix_attempt":
			foundFixAttempt = true
		case "verifier_remediation_resume_attempt":
			t.Fatalf("expected no verifier remediation resume attempt without guarded boundary, got %+v", record)
		}
	}
	if !foundFixAttempt {
		t.Fatalf("expected normal reverify repair attempt evidence, got %+v", snapshot.EvaluationRecords)
	}
	boundary := memstore.LatestRecoveryExecutionBoundary(snapshot.EvaluationRecords, 10, "runtime_reverify_repair")
	if boundary == nil || boundary.GuardStatus != "manual_review_required" || boundary.VerifierRequired {
		t.Fatalf("expected non-executable manual boundary without remediation pause point, got %+v", boundary)
	}
	if executable := verifierRemediationExecutionBoundary(snapshot.EvaluationRecords); executable != nil {
		t.Fatalf("expected no executable verifier remediation boundary without pause point, got %+v", executable)
	}
}

func TestInvokeTool_PersistsGuardedWriteReverifyRepairAttemptEvaluation(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/writereverify\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "sessions"), "session-task-reverify-write-attempt")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{
		SessionID:  "session-before",
		RunID:      "run-before",
		TaskID:     "demo-task",
		Tool:       "patch",
		Verdict:    "PARTIAL",
		Summary:    "Verification finished with PARTIAL.",
		ReportPath: filepath.Join(tempDir, ".avatars", "tasks", "demo-task", "memory", "verifier", "run-before-patch.json"),
		Checks:     []string{"go test -race ./... => PARTIAL"},
		UpdatedAt:  time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("record prior verification failed: %v", err)
	}
	targetPath := filepath.Join(tempDir, "repair_note.txt")
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	engine := &Engine{
		tools:      toolRegistry,
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-task-reverify-write-attempt",
	}

	result, err := engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-reverify-write-attempt", taskID: "demo-task", phase: "executing"}, "write", "file_write", tools.WriteInput{Path: targetPath, Content: "repair attempt content", WorkingDir: tempDir, Overwrite: true, ProposalID: "remediate-latest-non-pass", ExpectedTargets: []string{targetPath}})
	if err != nil {
		t.Fatalf("expected write tool success, got %v", err)
	}
	if !strings.Contains(result.Content, "Wrote") && !strings.Contains(result.Content, "repair") {
		t.Fatalf("expected write output to indicate success, got %q", result.Content)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("expected repair_note.txt to exist after write attempt: %v", err)
	}

	snapshot, err := memoryStore.LoadLatestTaskSnapshot("demo-task")
	if err != nil {
		t.Fatalf("load task snapshot failed: %v", err)
	}
	matched := false
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause != "verification_reverify_fix_attempt" {
			continue
		}
		if record.Tool != "write" {
			t.Fatalf("expected write tool record, got %q", record.Tool)
		}
		if !strings.Contains(record.Summary, "write") {
			t.Fatalf("expected write summary, got %q", record.Summary)
		}
		if !strings.Contains(strings.Join(record.Details, "\n"), "proposal_id: remediate-latest-non-pass") {
			t.Fatalf("expected write details to include proposal id, got %+v", record.Details)
		}
		expectedChangedFiles := "expected_targets: " + targetPath
		if !strings.Contains(strings.Join(record.Details, "\n"), expectedChangedFiles) {
			t.Fatalf("expected write expected_targets details, got %+v", record.Details)
		}
		matched = true
		break
	}
	if !matched {
		t.Fatalf("expected one guarded write reverify repair attempt evaluation record, got %+v", snapshot.EvaluationRecords)
	}
}

func TestInvokeTool_PersistsRuntimePermissionDenialEvaluation(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-runtime-failure")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(failingTool{name: "failing", err: fmt.Errorf("permission denied by sandbox")}); err != nil {
		t.Fatalf("register failing tool failed: %v", err)
	}
	engine := &Engine{
		tools:      toolRegistry,
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-tool-runtime-failure",
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-runtime-failure", taskID: "task-tool-runtime-failure", phase: "executing"}, "failing", "demo_operation", tools.WriteInput{Path: "notes.txt", WorkingDir: tempDir, ProposalID: "inspect-latest-non-pass", ExpectedTargets: []string{"notes.txt"}})
	if err == nil {
		t.Fatal("expected failing tool to return error")
	}
	if !strings.Contains(err.Error(), "permission denied by sandbox") {
		t.Fatalf("expected runtime failure error, got %v", err)
	}

	snapshot, err := memoryStore.LoadSnapshot("session-tool-runtime-failure")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected evaluation record length 1, got %d", len(snapshot.EvaluationRecords))
	}
	record := snapshot.EvaluationRecords[0]
	if record.Tool != "failing" {
		t.Fatalf("expected failing tool record, got %q", record.Tool)
	}
	if record.Cause != "tool_permission_denied" {
		t.Fatalf("expected tool_permission_denied cause, got %q", record.Cause)
	}
	if record.Source != "tool_runtime" {
		t.Fatalf("expected tool_runtime source, got %q", record.Source)
	}
	if !strings.Contains(strings.Join(record.Details, "\n"), "decision_source: tool_sandbox") {
		t.Fatalf("expected runtime denial details to include decision source, got %+v", record.Details)
	}
	if !strings.Contains(strings.Join(record.Details, "\n"), "expected_targets: notes.txt") {
		t.Fatalf("expected runtime failure details to include expected targets, got %+v", record.Details)
	}
	if !strings.Contains(strings.Join(record.Details, "\n"), "proposal_id: inspect-latest-non-pass") {
		t.Fatalf("expected runtime failure details to include proposal id, got %+v", record.Details)
	}
	if !strings.Contains(record.Summary, "was denied") || !strings.Contains(record.Summary, "permission denied by sandbox") {
		t.Fatalf("expected runtime denial summary to include sandbox error, got %q", record.Summary)
	}
	if len(record.Details) == 0 || record.Details[0] != "operation: demo_operation" {
		t.Fatalf("expected operation detail for runtime denial, got %v", record.Details)
	}
	if memstore.EvaluationRecordRetryable(record) != "false" {
		t.Fatalf("expected runtime denial to be non-retryable until permission context changes, got %+v", record.Details)
	}
	if memstore.EvaluationRecordReason(record) != "tool_failure_permission_gated" {
		t.Fatalf("expected permission-gated retry reason, got %+v", record.Details)
	}
}

func TestInvokeTool_PersistsExecutionFailureRetryEvaluation(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-execution-failure")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(failingTool{name: "failing", err: fmt.Errorf("temporary tool transport failure")}); err != nil {
		t.Fatalf("register failing tool failed: %v", err)
	}
	engine := &Engine{
		tools:      toolRegistry,
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-tool-execution-failure",
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-execution-failure", taskID: "task-tool-execution-failure", phase: "executing"}, "failing", "demo_operation", tools.ReadInput{Path: "notes.txt"})
	if err == nil {
		t.Fatal("expected failing tool to return error")
	}

	snapshot, err := memoryStore.LoadSnapshot("session-tool-execution-failure")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	retry := memstore.LatestToolFailureRetryEvaluation(snapshot.EvaluationRecords)
	if retry == nil {
		t.Fatalf("expected latest tool failure retry evaluation, got %+v", snapshot.EvaluationRecords)
	}
	if retry.Cause != "tool_execution_failed" {
		t.Fatalf("expected tool_execution_failed cause, got %+v", retry)
	}
	if memstore.EvaluationRecordRetryable(*retry) != "true" {
		t.Fatalf("expected transient failure to be retryable, got %+v", retry.Details)
	}
	if memstore.EvaluationRecordReason(*retry) != "tool_failure_retryable" {
		t.Fatalf("expected retryable reason, got %+v", retry.Details)
	}
}

func TestInvokeTool_PersistsMissingToolEvaluation(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-tool-missing")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	store := events.NewStore()
	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	engine := &Engine{
		tools:      registry.NewToolRegistry(),
		transcript: writer,
		store:      store,
		memory:     memoryStore,
		sessionID:  "session-tool-missing",
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-tool-missing", taskID: "task-tool-missing", phase: "executing"}, "missing", "file_read", tools.ReadInput{Path: "process_record.md"})
	if err == nil {
		t.Fatal("expected missing tool to return error")
	}

	snapshot, err := memoryStore.LoadSnapshot("session-tool-missing")
	if err != nil {
		t.Fatalf("load memory snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected evaluation record length 1, got %d", len(snapshot.EvaluationRecords))
	}
	record := snapshot.EvaluationRecords[0]
	if record.Cause != "tool_not_registered" {
		t.Fatalf("expected tool_not_registered cause, got %q", record.Cause)
	}
	if !strings.Contains(record.Summary, "is unavailable") {
		t.Fatalf("expected unavailable tool summary, got %q", record.Summary)
	}
	if memstore.EvaluationRecordRetryable(record) != "false" {
		t.Fatalf("expected unavailable tool to be non-retryable, got %+v", record.Details)
	}
	if memstore.EvaluationRecordReason(record) != "tool_failure_unavailable" {
		t.Fatalf("expected unavailable retry reason, got %+v", record.Details)
	}
}

func TestInvokeTool_BypassPermissionsEmitsAuditEvent(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/bypassaudit\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-bypass-audit")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	memoryStore, err := memstore.NewSQLiteStore(filepath.Join(tempDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()

	store := events.NewStore()
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ShellTool{}); err != nil {
		t.Fatalf("register shell tool failed: %v", err)
	}
	engine := &Engine{
		tools:          toolRegistry,
		transcript:     writer,
		store:          store,
		memory:         memoryStore,
		sessionID:      "session-bypass-audit",
		permissionMode: PermissionModeBypassPermissions,
	}

	_, err = engine.invokeTool(context.Background(), toolCallEnvelope{runID: "run-bypass", taskID: "task-bypass", phase: "executing"}, "shell", "command_exec", tools.ShellInput{Command: []string{"cmd", "/c", "echo", "ok"}, WorkingDir: tempDir, TimeoutMillis: 5000})
	if err != nil {
		t.Fatalf("expected bypassPermissions shell to succeed, got %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush transcript failed: %v", err)
	}

	content, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "audit.bypass_permissions") {
		t.Fatalf("expected audit.bypass_permissions event in transcript, got: %s", text)
	}
	if !strings.Contains(text, "bypass_permissions_audit") {
		t.Fatalf("expected bypass_permissions_audit in transcript, got: %s", text)
	}

	// Verify the evaluation record was persisted to task memory.
	snapshot, loadErr := memoryStore.LoadLatestTaskSnapshot("task-bypass")
	if loadErr != nil {
		t.Fatalf("load task snapshot failed: %v", loadErr)
	}
	found := false
	for _, record := range snapshot.EvaluationRecords {
		if record.Cause == "bypass_permissions" && record.Kind == "security_audit" {
			found = true
			if record.Verdict != "WARNING" {
				t.Fatalf("expected WARNING verdict, got %q", record.Verdict)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected security_audit evaluation record in task memory, got %+v", snapshot.EvaluationRecords)
	}
}
