package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/app"
	memstore "avatars/internal/memory"
	"avatars/internal/runtime"
	"avatars/internal/tasks"
	"avatars/internal/tools"
)

func TestLatestVerifierFailureRepairTargetPrefersDomainSourceFromTestName(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join("internal", "auto"), 0o755); err != nil {
		t.Fatalf("mkdir internal auto failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("internal", "auto", "hazard_manifest.go"), []byte("package auto\n"), 0o644); err != nil {
		t.Fatalf("write hazard manifest failed: %v", err)
	}
	report := "Focused verifier failed: `go test ./internal/auto -run TestRunner` => FAIL\n--- FAIL: TestRunner_Run_HazardManifestTask_WritesWorkbooks\nSource target: `internal/auto/runner.go (Runner.Run)`"
	target := latestVerifierFailureRepairTarget(report)
	if filepath.ToSlash(target) != "internal/auto/hazard_manifest.go" {
		t.Fatalf("expected domain source target, got %q", target)
	}
}

func TestResultLocationDoesNotStealExplicitVerifierRepairRequest(t *testing.T) {
	input := "修复刚才的 focused verifier 失败：go test ./internal/auto -run TestRunner 中期望输出文件名和实际文件名下划线不一致。请修改相关代码或测试，并验证。"
	lowered := strings.ToLower(input)
	if looksLikeResultLocationQuestion(lowered) {
		t.Fatalf("expected explicit repair request not to route as result-location")
	}
	if !looksLikeVerifierFailureRepairRequest(lowered) {
		t.Fatalf("expected verifier failure repair request")
	}
}

func TestLatestVerifierFailureRepairCommandUsesRootAnalysisReportArtifact(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore wd failed: %v", chdirErr)
		}
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("internal", "auto"), 0o755); err != nil {
		t.Fatalf("mkdir internal auto failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("internal", "auto", "hazard_manifest.go"), []byte("package auto\n"), 0o644); err != nil {
		t.Fatalf("write hazard manifest failed: %v", err)
	}
	report := strings.Join([]string{
		"# Analysis Report",
		"## Evidence Reviewed",
		"- Read internal\\auto\\runner_test.go successfully.",
		"## Verification Results",
		"- `go test ./internal/auto -run TestRunner` => FAIL (exit status 1) output: --- FAIL: TestRunner_Run_HazardManifestTask_WritesWorkbooks (24.77s) | runner_test.go:140: expected final output path result\\国际集装箱危品申报单泛洲胜利2503E.xlsx, got result\\国际集装箱危品申报单_泛洲胜利_2503E.xlsx | FAIL",
		"## Next Checks",
		"- Source target: `internal/auto/runner.go (NewRunner; Runner.Run)`.",
	}, "\n")
	if err := os.WriteFile("depth_smoke_3.md", []byte(report), 0o644); err != nil {
		t.Fatalf("write root report failed: %v", err)
	}

	command, summary, ok := buildLatestVerifierFailureRepairCommand("修复刚才的 focused verifier 失败，并验证。", naturalLanguageContextIntent)
	if !ok {
		t.Fatalf("expected repair command from root report artifact")
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("expected non-empty summary")
	}
	if len(command) < 3 || command[0] != "edit" || command[1] != "--apply" || command[2] != "internal/auto/hazard_manifest.go" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if !strings.Contains(command[len(command)-1], "depth_smoke_3.md") {
		t.Fatalf("expected instruction to cite root report artifact, got %q", command[len(command)-1])
	}
}

func TestFocusedGoTestCommandFromTextExtractsSafeFocusedVerifier(t *testing.T) {
	label, args, ok := focusedGoTestCommandFromText("Latest verifier failure: `go test ./internal/auto -run TestRunner` failed.")
	if !ok {
		t.Fatalf("expected focused go test command")
	}
	if label != "go test ./internal/auto -run TestRunner" {
		t.Fatalf("unexpected label: %q", label)
	}
	expected := []string{"test", "./internal/auto", "-run", "TestRunner"}
	if strings.Join(args, " ") != strings.Join(expected, " ") {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestFocusedGoTestCommandFromTextRejectsTraversalPackage(t *testing.T) {
	if _, _, ok := focusedGoTestCommandFromText("go test ./../other -run TestThing"); ok {
		t.Fatalf("expected traversal package to be rejected")
	}
}

func TestSummarizeLatestApprovalBlockPrintsActionableCommands(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: defaultREPLTaskID, Title: defaultREPLTaskID})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	store, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("open task memory failed: %v", err)
	}
	if err := store.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "session-approval",
		RunID:     "run-approval",
		TaskID:    workspace.ID,
		Tool:      "write",
		Kind:      "passive_feedback",
		Verdict:   "PARTIAL",
		Cause:     "tool_approval_required",
		Summary:   "Tool write/file_write is awaiting approval.",
		Source:    "tool_runtime",
		Details:   []string{"operation: file_write", "decision_source: runtime_permission_mode", "permission_mode: default", "approval_key: approval-key-demo"},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("record approval failed: %v", err)
	}
	_ = store.Close()
	if _, err := manager.MarkRun(workspace, filepath.Join(workspace.SessionsDir, "session.jsonl"), "approval required"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}

	summary := summarizeLatestApprovalBlock(".")
	if !strings.Contains(summary, "Approval required:") ||
		!strings.Contains(summary, "avatars tasks approve repl-session") ||
		!strings.Contains(summary, "avatars tasks deny repl-session") {
		t.Fatalf("expected actionable approval summary, got %s", summary)
	}
}

func TestApprovalCommandQuestionAnswersFromPendingApproval(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: defaultREPLTaskID, Title: defaultREPLTaskID})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	store, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("open task memory failed: %v", err)
	}
	if err := store.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "session-approval",
		RunID:     "run-approval",
		TaskID:    workspace.ID,
		Tool:      "write",
		Kind:      "passive_feedback",
		Verdict:   "PARTIAL",
		Cause:     "tool_approval_required",
		Summary:   "Tool write/file_write is awaiting approval.",
		Source:    "tool_runtime",
		Details:   []string{"operation: file_write", "decision_source: runtime_permission_mode", "permission_mode: default"},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("record approval failed: %v", err)
	}
	_ = store.Close()
	if _, err := manager.MarkRun(workspace, filepath.Join(workspace.SessionsDir, "session.jsonl"), "approval required"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}

	decision := classifyNaturalLanguageQuestionWithoutLLM("approval cli 指令是什么？", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer || !strings.Contains(decision.Answer, "avatars tasks approve repl-session") {
		t.Fatalf("expected approval command answer, got %+v", decision)
	}
}

func TestResultLocationReportsLatestTaskWithoutAnalysisReport(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: defaultREPLTaskID, Title: defaultREPLTaskID})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"run.completed","payload":{"summary":"readme summarized"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "readme summarized"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}
	answer, err := summarizeResultLocation(".")
	if err != nil {
		t.Fatalf("summarize result location failed: %v", err)
	}
	if !strings.Contains(answer, "transcript:") || !strings.Contains(answer, "readme summarized") || !strings.Contains(answer, defaultREPLTaskID) {
		t.Fatalf("expected latest task transcript and summary, got %s", answer)
	}
}

func TestResultLocationPrefersLatestAnalysisReportOverStaleREPLTask(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	replWorkspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: defaultREPLTaskID, Title: defaultREPLTaskID})
	if err != nil {
		t.Fatalf("resolve repl workspace failed: %v", err)
	}
	replTranscriptPath := filepath.Join(replWorkspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(replTranscriptPath, []byte("Analysis report: stale.md\n"), 0o644); err != nil {
		t.Fatalf("write repl transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(replWorkspace, replTranscriptPath, "stale report"); err != nil {
		t.Fatalf("mark repl run failed: %v", err)
	}
	analysisWorkspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "new-analysis", Title: "new-analysis"})
	if err != nil {
		t.Fatalf("resolve analysis workspace failed: %v", err)
	}
	analysisTranscriptPath := filepath.Join(analysisWorkspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(analysisTranscriptPath, []byte("Analysis report: latest.md\n"), 0o644); err != nil {
		t.Fatalf("write analysis transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(analysisWorkspace, analysisTranscriptPath, "latest report"); err != nil {
		t.Fatalf("mark analysis run failed: %v", err)
	}

	decision := classifyNaturalLanguageQuestionWithoutLLM("结果在哪？", replCommandOptions{}, naturalLanguageContextIntent)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected result location answer, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "latest.md") || strings.Contains(decision.Answer, "stale.md") {
		t.Fatalf("expected latest analysis report over stale repl report, got %s", decision.Answer)
	}
}

func TestCompactRunSummaryForDisplay_PreservesFocusedVerifierFailure(t *testing.T) {
	summary := strings.Join([]string{
		"Multi-avatar planning run complete for request \"Analyze\".",
		strings.Repeat("Read README.md successfully. ", 40),
		"Focused verification summary: Verification finished with FAIL. 3 passed, 0 partial, 1 failed.",
		"Focused verifier failure: `go test ./internal/auto -run TestRunner` failed at `internal/auto/runner_test.go:140` (`TestRunner_Run_HazardManifestTask_WritesWorkbooks`).",
		"Synthesis: demo.",
	}, " ")
	display := compactRunSummaryForDisplay(summary, 600)
	if !strings.Contains(display, "Focused verifier failure:") || !strings.Contains(display, "internal/auto/runner_test.go:140") {
		t.Fatalf("expected compact summary to preserve focused verifier failure, got %s", display)
	}
}

func TestTasksCLI_ListShowDelete(t *testing.T) {
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
	if err := os.WriteFile("go.mod", []byte("module example.com/taskverify\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.WriteFile("verify_fixture.go", []byte("package taskverify\n\nfunc OK() string { return \"ok\" }\n"), 0o644); err != nil {
		t.Fatalf("write verify fixture failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "--task", "demo-task", "Analyze the current repository and propose a refactoring plan"})
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, err := manager.Load("demo-task")
	if err != nil {
		t.Fatalf("load workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	reportPath := filepath.Join(workspace.MemoryDir, "verifier", "run-cli-test-write.json")
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, Tool: "write", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: reportPath, Checks: []string{"go test ./... => PASS"}}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record verification failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, Tool: "write", Kind: "verifier_verdict", Verdict: "PASS", Cause: "verification_completed", Summary: "Verification finished with PASS.", Source: "verification", ReportPath: reportPath, Details: []string{"go test ./... => PASS"}}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record evaluation failed: %v", err)
	}
	secondReportPath := filepath.Join(workspace.MemoryDir, "verifier", "run-cli-test-patch.json")
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-cli-test-2", RunID: "run-cli-test-2", TaskID: workspace.ID, Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: secondReportPath, Checks: []string{"go test -race ./... => PARTIAL"}}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record second verification failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-cli-test-2", RunID: "run-cli-test-2", TaskID: workspace.ID, Tool: "patch", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "verification_warning", Summary: "CGO is not enabled", Source: "verification_warning", ReportPath: secondReportPath, Details: []string{"go test -race ./... => PARTIAL"}}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record passive evaluation failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-cli-test-2", RunID: "run-cli-test-2", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "INFO", Cause: "node_work_evidence", Summary: "Workflow node node-build completed through approval replay.", Source: "workflow_node", Details: []string{"node_id: node-build", "node_role: Builder", "node_title: Shape the smallest viable change", "tool: write", "operation: file_write", "status: replay_completed", "approval_key: approval-build", "continuation_run_id: continuation-run", "continuation_task_id: continuation-task", "replay_status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verified: true", "recovery_kind: approval_replay"}, UpdatedAt: time.Now().UTC().Add(time.Second)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record node evidence failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, Tool: "read", Kind: "workflow_feedback", Verdict: "INFO", Cause: "node_work_evidence", Summary: "Workflow node node-survey completed repository survey.", Source: "workflow_node", Details: []string{"node_id: node-survey", "node_role: Researcher", "tool: read", "operation: file_read", "status: completed"}, UpdatedAt: time.Now().UTC()}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record survey node evidence failed: %v", err)
	}
	if err := memoryStore.RecordWarmLesson(memstore.WarmLessonRecord{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, Kind: "workflow", Summary: "Keep the planner decomposition stable before execution.", Source: "task_summary", Confidence: "medium"}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record warm lesson failed: %v", err)
	}
	if err := memoryStore.RecordEvolutionCandidate(memstore.EvolutionCandidateRecord{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, Kind: "workflow_pattern", Summary: "Promote this lesson into a reusable workflow pattern: Keep the planner decomposition stable before execution.", Source: "warm_lesson", Priority: "medium", Status: "open"}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record evolution candidate failed: %v", err)
	}
	for _, record := range []memstore.EventRecord{
		{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, EventID: "evt-chain-1", EventType: "task.decomposed", Summary: "Planner decomposed the task: Planner -> Researcher -> Builder -> Critic -> Synthesizer."},
		{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, AvatarID: "avatar-researcher", EventID: "evt-chain-2", EventType: "avatar.assigned", Summary: "Researcher assigned: Survey current repository context"},
		{SessionID: "session-cli-test", RunID: "run-cli-test", TaskID: workspace.ID, AvatarID: "avatar-researcher", EventID: "evt-chain-3", EventType: "avatar.handoff", Summary: "Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context."},
	} {
		if err := memoryStore.RecordEvent(record); err != nil {
			_ = memoryStore.Close()
			t.Fatalf("record event failed: %v", err)
		}
	}
	_ = memoryStore.Close()
	projectStore, err := memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
	if err != nil {
		t.Fatalf("new project memory store failed: %v", err)
	}
	if err := projectStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: workspace.ID, Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Source: "evolution_candidate", Confidence: "high"}); err != nil {
		_ = projectStore.Close()
		t.Fatalf("record project lesson failed: %v", err)
	}
	if err := projectStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: "peer-task", Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Source: "evolution_candidate", Confidence: "high"}); err != nil {
		_ = projectStore.Close()
		t.Fatalf("record second project lesson failed: %v", err)
	}
	_ = projectStore.Close()
	listOutput := captureRunOutput(t, []string{"tasks", "list"})
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	plannerViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "planner"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	avatarViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "avatar:avatar-builder"})
	verifyOutput := captureRunOutput(t, []string{"verify", "--task", "demo-task"})
	verifyRaceOutput := captureRunOutput(t, []string{"verify", "--race"})
	deleteOutput := ""

	if !strings.Contains(listOutput, "demo-task") {
		t.Fatalf("expected tasks list to include demo-task, got %s", listOutput)
	}
	if !strings.Contains(showOutput, "Task: demo-task") {
		t.Fatalf("expected tasks show to print task id, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest transcript:") {
		t.Fatalf("expected tasks show to print latest transcript, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Task memory summary:") {
		t.Fatalf("expected tasks show to print task memory summary, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Avatar summaries:") {
		t.Fatalf("expected tasks show to print avatar summary count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recent key events:") {
		t.Fatalf("expected tasks show to print recent key events, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Execution chain:") {
		t.Fatalf("expected tasks show to print execution chain, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context.") {
		t.Fatalf("expected tasks show to print handoff chain step, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest avatar challenge:") {
		t.Fatalf("expected tasks show to print latest avatar challenge, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest avatar ask route:") {
		t.Fatalf("expected tasks show to print latest avatar ask route, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest avatar ask status:") {
		t.Fatalf("expected tasks show to print latest avatar ask status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest avatar follow-up route:") {
		t.Fatalf("expected tasks show to print latest avatar follow-up route, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest avatar follow-up:") {
		t.Fatalf("expected tasks show to print latest avatar follow-up, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest avatar ask:") {
		t.Fatalf("expected tasks show to print latest avatar ask, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest avatar summary:") {
		t.Fatalf("expected tasks show to print latest avatar summary, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest run LLM:") {
		t.Fatalf("expected tasks show to print latest run llm status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Configured LLM:") {
		t.Fatalf("expected tasks show to print configured llm status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Telemetry:") {
		t.Fatalf("expected tasks show to print telemetry, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest verifier report:") {
		t.Fatalf("expected tasks show to print latest verifier report, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest verifier follow-up: avatars verify --task demo-task") {
		t.Fatalf("expected tasks show to print verifier follow-up, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest non-pass verifier:") {
		t.Fatalf("expected tasks show to print latest non-pass verifier summary, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest non-pass follow-up: avatars verify --task demo-task") {
		t.Fatalf("expected tasks show to print latest non-pass verifier follow-up, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node evidence: Workflow node node-build completed through approval replay.") {
		t.Fatalf("expected tasks show to print latest node evidence, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node ID: node-build") {
		t.Fatalf("expected tasks show to print latest node id, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node role: Builder") {
		t.Fatalf("expected tasks show to print latest node role, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node title: Shape the smallest viable change") {
		t.Fatalf("expected tasks show to print latest node title, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node tool: write/file_write") {
		t.Fatalf("expected tasks show to print latest node tool operation, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node status: replay_completed") {
		t.Fatalf("expected tasks show to print latest node status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node verifier gate: verified_pass") {
		t.Fatalf("expected tasks show to print latest node verifier gate, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node recovery: approval_replay") {
		t.Fatalf("expected tasks show to print latest node recovery, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest node replay: completed") {
		t.Fatalf("expected tasks show to print latest node replay, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Node evidence chain:") {
		t.Fatalf("expected tasks show to print node evidence chain count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "#1 node-build | Builder | write/file_write | status=replay_completed | gate=verified_pass | recovery=approval_replay | replay=completed") {
		t.Fatalf("expected tasks show to print latest node chain entry, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "#2 node-survey | Researcher | read/file_read | status=completed") {
		t.Fatalf("expected tasks show to print second node chain entry, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "#3 node-summarize | Synthesizer | synthesize/summary_prepare | status=prepared") {
		t.Fatalf("expected tasks show to print synthesizer chain entry, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "@3 artifacts: node-summarize") {
		t.Fatalf("expected tasks show to print synthesizer provenance artifacts, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "#4 node-review | Critic | review/boundary_review | status=approval_boundary_reviewed") {
		t.Fatalf("expected tasks show to print critic chain entry, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "@4 artifacts: node-review") {
		t.Fatalf("expected tasks show to print critic provenance artifacts, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Reverify status: pending reverify for latest non-pass verification") {
		t.Fatalf("expected tasks show to print reverify status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Suggested commands: 2") {
		t.Fatalf("expected tasks show to print suggested command count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "> avatars tasks show demo-task --view verifier") {
		t.Fatalf("expected tasks show to suggest verifier view command, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "> avatars verify --task demo-task") {
		t.Fatalf("expected tasks show to suggest verify command, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Verifier history entries: 3") {
		t.Fatalf("expected tasks show to print verifier history count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Evaluation records:") {
		t.Fatalf("expected tasks show to print evaluation record count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Warm lessons:") {
		t.Fatalf("expected tasks show to print warm lesson count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Evolution candidates:") {
		t.Fatalf("expected tasks show to print evolution candidate count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Project lessons:") {
		t.Fatalf("expected tasks show to print project lesson count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "top support: 2 tasks") {
		t.Fatalf("expected tasks show to print project lesson support evidence, got %s", showOutput)
	}
	if !strings.Contains(plannerViewOutput, "View warm lessons:") {
		t.Fatalf("expected planner view to include warm lessons, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View execution chain:") {
		t.Fatalf("expected planner view to include execution chain, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View latest avatar report:") {
		t.Fatalf("expected planner view to include latest avatar report, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View latest avatar ask route:") {
		t.Fatalf("expected planner view to include latest avatar ask route, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View latest avatar ask status:") {
		t.Fatalf("expected planner view to include latest avatar ask status, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View latest avatar follow-up route:") {
		t.Fatalf("expected planner view to include latest avatar follow-up route, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View latest avatar follow-up:") {
		t.Fatalf("expected planner view to include latest avatar follow-up, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View latest avatar ask:") {
		t.Fatalf("expected planner view to include latest avatar ask, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View telemetry:") {
		t.Fatalf("expected planner view to include telemetry, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View run LLM:") {
		t.Fatalf("expected planner view to include latest run llm status, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "Keep the planner decomposition stable before execution.") {
		t.Fatalf("expected planner view to include warm lesson summary, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View evolution candidates:") {
		t.Fatalf("expected planner view to include evolution candidates, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View evaluation records:") {
		t.Fatalf("expected planner view to include evaluation records, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "Verification finished with PASS.") {
		t.Fatalf("expected planner view to include evaluation record summary, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "Promote this lesson into a reusable workflow pattern") {
		t.Fatalf("expected planner view to include evolution candidate summary, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View project lessons:") {
		t.Fatalf("expected planner view to include project lessons, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "Add a deterministic follow-up path for patch") {
		t.Fatalf("expected planner view to include project lesson summary, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "tasks=2") {
		t.Fatalf("expected planner view to include project lesson support count, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "View suggested commands: 1") {
		t.Fatalf("expected planner view to include suggested commands, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "> avatars tasks show demo-task --view verifier") {
		t.Fatalf("expected planner view to suggest verifier view command, got %s", plannerViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "Query view: verifier") {
		t.Fatalf("expected verifier query view output, got %s", verifierViewOutput)
	}
	if strings.Contains(verifierViewOutput, "View avatar summary:") {
		t.Fatalf("expected verifier view to omit avatar summary, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View verification verdict: PARTIAL") {
		t.Fatalf("expected verifier view to include latest verification verdict, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View verification report: "+secondReportPath) {
		t.Fatalf("expected verifier view to include latest verification report path, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View verification follow-up: avatars verify --task demo-task") {
		t.Fatalf("expected verifier view to include latest verification follow-up, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest non-pass verification:") {
		t.Fatalf("expected verifier view to include latest non-pass summary, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest non-pass follow-up: avatars verify --task demo-task") {
		t.Fatalf("expected verifier view to include latest non-pass follow-up, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node evidence: Workflow node node-build completed through approval replay.") {
		t.Fatalf("expected verifier view to include latest node evidence, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node ID: node-build") {
		t.Fatalf("expected verifier view to include latest node id, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node role: Builder") {
		t.Fatalf("expected verifier view to include latest node role, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node title: Shape the smallest viable change") {
		t.Fatalf("expected verifier view to include latest node title, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node tool: write/file_write") {
		t.Fatalf("expected verifier view to include latest node tool operation, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node verifier gate: verified_pass") {
		t.Fatalf("expected verifier view to include latest node verifier gate, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node verifier verdict: PASS") {
		t.Fatalf("expected verifier view to include latest node verifier verdict, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node replay: completed") {
		t.Fatalf("expected verifier view to include latest node replay, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node continuation run ID: continuation-run") {
		t.Fatalf("expected verifier view to include latest node continuation run id, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest node continuation task ID: continuation-task") {
		t.Fatalf("expected verifier view to include latest node continuation task id, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View node evidence chain:") {
		t.Fatalf("expected verifier view to include node evidence chain count, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "#1 node-build | Builder | write/file_write | status=replay_completed | gate=verified_pass | recovery=approval_replay | replay=completed") {
		t.Fatalf("expected verifier view to include latest node chain entry, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "#2 node-survey | Researcher | read/file_read | status=completed") {
		t.Fatalf("expected verifier view to include second node chain entry, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View reverify status: pending reverify for latest non-pass verification") {
		t.Fatalf("expected verifier view to include reverify status, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View suggested commands: 1") || !strings.Contains(verifierViewOutput, "> avatars verify --task demo-task") {
		t.Fatalf("expected verifier view to include deduplicated suggested verify command, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View verification history: 3") {
		t.Fatalf("expected verifier view to include verification history, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View evaluation records:") {
		t.Fatalf("expected verifier view to include evaluation records, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "passive_feedback/PARTIAL/verification_warning/patch") {
		t.Fatalf("expected verifier view to include evaluation tool context, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "CGO is not enabled") {
		t.Fatalf("expected verifier view to include passive feedback summary, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, reportPath) {
		t.Fatalf("expected verifier view history to include previous report path, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, secondReportPath) {
		t.Fatalf("expected verifier view history to include prior report path, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, ">avatars verify --task demo-task") {
		t.Fatalf("expected verifier view history to include follow-up command, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifyOutput, "\"verdict\": \"PASS\"") {
		t.Fatalf("expected task verify output to include PASS verdict, got %s", verifyOutput)
	}
	if strings.Contains(verifyOutput, "go test -race ./...") {
		t.Fatalf("expected default task verify to omit race check, got %s", verifyOutput)
	}
	if !strings.Contains(verifyRaceOutput, "go test -race ./...") {
		t.Fatalf("expected race verify output to include race check, got %s", verifyRaceOutput)
	}
	memoryStore, err = memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("reopen memory store failed: %v", err)
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	_ = memoryStore.Close()
	if err != nil {
		t.Fatalf("load latest task snapshot failed: %v", err)
	}
	if snapshot.Verification == nil || snapshot.Verification.Tool != "verify" {
		t.Fatalf("expected task verify to persist verify snapshot, got %+v", snapshot.Verification)
	}
	if snapshot.Verification.ReportPath == "" {
		t.Fatalf("expected task verify to persist report path, got %+v", snapshot.Verification)
	}
	if reverifyStatus := memstore.VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory); reverifyStatus != "latest non-pass verification is covered by a later PASS" {
		t.Fatalf("expected covered reverify status after task verify, got %q", reverifyStatus)
	}
	if !strings.Contains(avatarViewOutput, "Query view: avatar") {
		t.Fatalf("expected avatar query view output, got %s", avatarViewOutput)
	}
	if !strings.Contains(avatarViewOutput, "View avatar summary:") {
		t.Fatalf("expected avatar view to include avatar summary, got %s", avatarViewOutput)
	}
	deleteOutput = captureRunOutput(t, []string{"tasks", "delete", "demo-task"})
	if !strings.Contains(deleteOutput, "Deleted task workspace: demo-task") {
		t.Fatalf("expected tasks delete confirmation, got %s", deleteOutput)
	}
	if _, err := os.Stat(filepath.Join(".avatars", "tasks", "demo-task")); !os.IsNotExist(err) {
		t.Fatalf("expected task workspace to be deleted, got err=%v", err)
	}
}

func TestTasksCLI_ShowLatestReverifyClosure(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	olderTime := time.Now().UTC().Add(-time.Minute)
	newerTime := time.Now().UTC()
	partialReportPath := filepath.Join(workspace.MemoryDir, "verifier", "run-old-patch.json")
	passReportPath := filepath.Join(workspace.MemoryDir, "verifier", "run-new-verify.json")
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-old", RunID: "run-old", TaskID: workspace.ID, Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: partialReportPath, Checks: []string{"go test -race ./... => PARTIAL"}, UpdatedAt: olderTime}); err != nil {
		t.Fatalf("record partial verification failed: %v", err)
	}
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-new", RunID: "run-new", TaskID: workspace.ID, Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: passReportPath, Checks: []string{"go test ./... => PASS"}, UpdatedAt: newerTime}); err != nil {
		t.Fatalf("record pass verification failed: %v", err)
	}
	closureSummary := "Verification PASS now covers the latest non-pass verification: PARTIAL via patch."
	attemptSummary := "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch."
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-mid", RunID: "run-mid", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "PARTIAL", Cause: "verification_reverify_fix_attempt", Summary: attemptSummary, Source: "verification_followup", Details: []string{"path: note.txt", "expected_targets: note.txt, process_record.md"}, UpdatedAt: olderTime.Add(30 * time.Second)}); err != nil {
		t.Fatalf("record attempt evaluation failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-new", RunID: "run-new", TaskID: workspace.ID, Tool: "verify", Kind: "workflow_feedback", Verdict: "PASS", Cause: "verification_reverify_covered", Summary: closureSummary, Source: "verification_followup", ReportPath: passReportPath, Details: []string{"covered_tool: patch", "covered_verdict: PARTIAL"}, UpdatedAt: newerTime.Add(time.Nanosecond)}); err != nil {
		t.Fatalf("record closure evaluation failed: %v", err)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	if !strings.Contains(showOutput, "Reverify status: latest non-pass verification is covered by a later PASS") {
		t.Fatalf("expected tasks show covered reverify status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest reverify closure: "+closureSummary) {
		t.Fatalf("expected tasks show latest reverify closure, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest reverify remediation: "+attemptSummary) {
		t.Fatalf("expected tasks show latest reverify remediation, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Advisory remediation proposals: 2") {
		t.Fatalf("expected tasks show advisory remediation proposal count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "!1 inspect-latest-non-pass | inspect_verification") {
		t.Fatalf("expected tasks show inspect remediation proposal, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "!2 remediate-latest-non-pass | remediation_attempt") {
		t.Fatalf("expected tasks show remediation attempt proposal, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest reverify remediation targets: 2") || !strings.Contains(showOutput, "= note.txt") || !strings.Contains(showOutput, "= process_record.md") {
		t.Fatalf("expected tasks show remediation targets, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View reverify status: latest non-pass verification is covered by a later PASS") {
		t.Fatalf("expected verifier view covered reverify status, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest reverify remediation: "+attemptSummary) {
		t.Fatalf("expected verifier view latest reverify remediation, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View advisory remediation proposals: 2") {
		t.Fatalf("expected verifier view advisory remediation proposal count, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest reverify remediation targets: 2") {
		t.Fatalf("expected verifier view remediation targets, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest reverify closure: "+closureSummary) {
		t.Fatalf("expected verifier view latest reverify closure, got %s", verifierViewOutput)
	}
}

func TestTasksCLI_ShowLatestReverifyAttempt(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	olderTime := time.Now().UTC().Add(-time.Minute)
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-old", RunID: "run-old", TaskID: workspace.ID, Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: filepath.Join(workspace.MemoryDir, "verifier", "run-old-patch.json"), Checks: []string{"go test -race ./... => PARTIAL"}, UpdatedAt: olderTime}); err != nil {
		t.Fatalf("record partial verification failed: %v", err)
	}
	attemptSummary := "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch."
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-new", RunID: "run-new", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "PARTIAL", Cause: "verification_reverify_fix_attempt", Summary: attemptSummary, Source: "verification_followup", Details: []string{"path: note.txt", "proposal_id: remediate-latest-non-pass", "expected_targets: note.txt"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record reverify attempt evaluation failed: %v", err)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	if !strings.Contains(showOutput, "Reverify status: pending reverify for latest non-pass verification") {
		t.Fatalf("expected tasks show pending reverify status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest reverify attempt: "+attemptSummary) {
		t.Fatalf("expected tasks show latest reverify attempt, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest reverify attempt proposal: remediate-latest-non-pass") {
		t.Fatalf("expected tasks show latest reverify attempt proposal, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Advisory remediation proposals: 2") {
		t.Fatalf("expected tasks show advisory remediation proposal count, got %s", showOutput)
	}
	if !strings.Contains(showOutput, ">2 avatars verify --task demo-task") {
		t.Fatalf("expected tasks show remediation proposal follow-up, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest reverify attempt targets: 1") || !strings.Contains(showOutput, "= note.txt") {
		t.Fatalf("expected tasks show latest reverify attempt targets, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest reverify attempt: "+attemptSummary) {
		t.Fatalf("expected verifier view latest reverify attempt, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest reverify attempt proposal: remediate-latest-non-pass") {
		t.Fatalf("expected verifier view latest reverify attempt proposal, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View advisory remediation proposals: 2") {
		t.Fatalf("expected verifier view advisory remediation proposal count, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest reverify attempt targets: 1") {
		t.Fatalf("expected verifier view latest reverify attempt targets, got %s", verifierViewOutput)
	}
}

func TestTasksCLI_ShowRecoveryInspectionSummary(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	now := time.Now().UTC()
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-old", RunID: "run-old", TaskID: workspace.ID, Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: filepath.Join(workspace.MemoryDir, "verifier", "run-old-patch.json"), Checks: []string{"go test ./... => PARTIAL"}, UpdatedAt: now}); err != nil {
		t.Fatalf("record partial verification failed: %v", err)
	}
	attemptSummary := "Verifier remediation resume attempt recorded for reverify repair work."
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-new", RunID: "run-new", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "PARTIAL", Cause: "verifier_remediation_resume_attempt", Summary: attemptSummary, Source: "verification_followup", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true", "reason: verifier_targets_explicitly_updated", "expected_targets: note.txt, process_record.md", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}, UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("record remediation resume attempt failed: %v", err)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	if !strings.Contains(showOutput, "Recovery summary: category=manual_required | action=run_verifier_remediation_repair | guard=guarded_ready | closure=pending_guarded_remediation | authority=verifier_reverify | source=verifier_remediation_resume_attempt") {
		t.Fatalf("expected tasks show recovery summary, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary coordinates: pause=pause-remediate | resume=resume-remediate") {
		t.Fatalf("expected tasks show recovery coordinates, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary targets: 2") || !strings.Contains(showOutput, "= note.txt") || !strings.Contains(showOutput, "= process_record.md") {
		t.Fatalf("expected tasks show recovery targets, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary guidance: Guarded remediation is pending verifier closure; keep repair and reverify explicit.") {
		t.Fatalf("expected tasks show recovery guidance, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View recovery summary: category=manual_required | action=run_verifier_remediation_repair | guard=guarded_ready | closure=pending_guarded_remediation | authority=verifier_reverify | source=verifier_remediation_resume_attempt") {
		t.Fatalf("expected verifier view recovery summary, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View recovery summary guidance: Guarded remediation is pending verifier closure; keep repair and reverify explicit.") {
		t.Fatalf("expected verifier view recovery guidance, got %s", verifierViewOutput)
	}
}

func TestTasksCLI_ShowClosedRecoverySummarySuppressesStaleGuard(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	now := time.Now().UTC()
	partialReport := filepath.Join(workspace.MemoryDir, "verifier", "run-old-patch.json")
	passReport := filepath.Join(workspace.MemoryDir, "verifier", "run-new-verify.json")
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-old", RunID: "run-old", TaskID: workspace.ID, Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: partialReport, Checks: []string{"go test ./... => PARTIAL"}, UpdatedAt: now}); err != nil {
		t.Fatalf("record partial verification failed: %v", err)
	}
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-new", RunID: "run-new", TaskID: workspace.ID, Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: passReport, Checks: []string{"go test ./... => PASS"}, UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("record pass verification failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-mid", RunID: "run-mid", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "PARTIAL", Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation resume attempt recorded for reverify repair work.", Source: "verification_followup", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "expected_targets: note.txt", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}, UpdatedAt: now.Add(30 * time.Second)}); err != nil {
		t.Fatalf("record guarded remediation attempt failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-new", RunID: "run-new", TaskID: workspace.ID, Tool: "verify", Kind: "workflow_feedback", Verdict: "PASS", Cause: "verification_reverify_covered", Summary: "Verification PASS now covers the latest non-pass verification: PARTIAL via patch.", Source: "verification_followup", ReportPath: passReport, Details: []string{"remediation_pause_point_id: pause-remediate", "remediation_resume_attempt_id: resume-remediate", "remediation_expected_targets: note.txt"}, UpdatedAt: now.Add(time.Minute + time.Nanosecond)}); err != nil {
		t.Fatalf("record closure evaluation failed: %v", err)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	if !strings.Contains(showOutput, "Recovery summary: category=closed | closure=pass_closed | source=verification_reverify_covered") {
		t.Fatalf("expected closed recovery summary, got %s", showOutput)
	}
	if strings.Contains(showOutput, "Recovery summary: category=closed | action=") || strings.Contains(showOutput, "guard=guarded_ready") || strings.Contains(showOutput, "authority=verifier_reverify") {
		t.Fatalf("expected closed summary to suppress stale execution readiness, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary coordinates: pause=pause-remediate | resume=resume-remediate") {
		t.Fatalf("expected closed summary coordinates, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary guidance: Verifier PASS closed the remediation chain; keep the closure coordinates for audit.") {
		t.Fatalf("expected closed summary guidance, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View recovery summary: category=closed | closure=pass_closed | source=verification_reverify_covered") {
		t.Fatalf("expected verifier view closed recovery summary, got %s", verifierViewOutput)
	}
	if strings.Contains(verifierViewOutput, "View recovery summary: category=closed | action=") || strings.Contains(verifierViewOutput, "guard=guarded_ready") || strings.Contains(verifierViewOutput, "authority=verifier_reverify") {
		t.Fatalf("expected verifier view closed summary to suppress stale execution readiness, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View recovery summary guidance: Verifier PASS closed the remediation chain; keep the closure coordinates for audit.") {
		t.Fatalf("expected verifier view closed summary guidance, got %s", verifierViewOutput)
	}
}

func TestMemoryCLI_StatusShowsLifecycleInspection(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	now := time.Now().UTC()
	if err := memoryStore.RecordWarmLesson(memstore.WarmLessonRecord{SessionID: "session-memory", RunID: "run-memory", TaskID: workspace.ID, Kind: "workflow", Summary: "Old low-confidence lesson.", Source: "task_summary", Confidence: "low", UpdatedAt: now.AddDate(0, 0, -45)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record warm lesson failed: %v", err)
	}
	if err := memoryStore.RecordEvolutionCandidate(memstore.EvolutionCandidateRecord{SessionID: "session-memory", RunID: "run-memory", TaskID: workspace.ID, Kind: "workflow_pattern", Summary: "Closed candidate.", Source: "warm_lesson", Priority: "low", Status: "closed", UpdatedAt: now}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record evolution candidate failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-memory", RunID: "run-memory", TaskID: workspace.ID, Kind: "runtime_lifecycle", Verdict: "INFO", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Source: "runtime", Details: []string{"status: completed"}, UpdatedAt: now}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record evaluation failed: %v", err)
	}
	_ = memoryStore.Close()

	output := captureRunOutput(t, []string{"memory", "status", "--task", "demo-task"})
	if !strings.Contains(output, "Memory status: scope=task task=demo-task") {
		t.Fatalf("expected task memory status header, got %s", output)
	}
	if !strings.Contains(output, "Memory maintenance mode: dry_run_with_guarded_archive_apply") || !strings.Contains(output, "Memory maintenance apply supported: yes") {
		t.Fatalf("expected guarded archive maintenance output, got %s", output)
	}
	if !strings.Contains(output, "Memory tables: 10") || !strings.Contains(output, "- warm_lessons rows=1") {
		t.Fatalf("expected table counts, got %s", output)
	}
	if !strings.Contains(output, "Memory retirement candidates: total=2 stale_warm_lessons=1 retirable_evolution_candidates=1") {
		t.Fatalf("expected advisory retirement candidate counts, got %s", output)
	}
	if !strings.Contains(output, "Memory protected truth: evaluation_records=1") {
		t.Fatalf("expected protected truth count, got %s", output)
	}
	if !strings.Contains(output, "Memory lifecycle action: inspect_only; guarded archive apply requires memory maintain --apply --confirm-archive") {
		t.Fatalf("expected guarded lifecycle action, got %s", output)
	}
	maintainOutput := captureRunOutput(t, []string{"memory", "maintain", "--dry-run", "--task", "demo-task"})
	if !strings.Contains(maintainOutput, "Memory maintenance dry run: scope=task task=demo-task") {
		t.Fatalf("expected dry-run maintenance header, got %s", maintainOutput)
	}
	if !strings.Contains(maintainOutput, "Memory maintenance apply supported: yes") || !strings.Contains(maintainOutput, "Memory maintenance planned action: preview_archive_candidates") {
		t.Fatalf("expected archive candidate preview maintenance, got %s", maintainOutput)
	}
	if !strings.Contains(maintainOutput, "Memory retirement candidates: total=2 stale_warm_lessons=1 retirable_evolution_candidates=1") {
		t.Fatalf("expected dry-run retirement candidates, got %s", maintainOutput)
	}
	if !strings.Contains(maintainOutput, "Memory protected truth skipped: total=1 evaluation_records=1") {
		t.Fatalf("expected protected truth skip count, got %s", maintainOutput)
	}
	if !strings.Contains(maintainOutput, "Memory maintenance guardrail: Dry-run does not delete") {
		t.Fatalf("expected dry-run guardrail, got %s", maintainOutput)
	}
	if err := run([]string{"memory", "maintain", "--task", "demo-task"}); err == nil || !strings.Contains(err.Error(), "requires --dry-run") {
		t.Fatalf("expected missing dry-run error, got %v", err)
	}
	if err := run([]string{"memory", "maintain", "--apply", "--task", "demo-task"}); err == nil || !strings.Contains(err.Error(), "requires --confirm-archive") {
		t.Fatalf("expected missing archive confirmation error, got %v", err)
	}
	if err := run([]string{"memory", "maintain", "--confirm-archive", "--task", "demo-task"}); err == nil || !strings.Contains(err.Error(), "requires --apply") {
		t.Fatalf("expected missing apply error, got %v", err)
	}
	applyOutput := captureRunOutput(t, []string{"memory", "maintain", "--apply", "--confirm-archive", "--task", "demo-task"})
	if !strings.Contains(applyOutput, "Memory archive apply: scope=task task=demo-task") {
		t.Fatalf("expected archive apply header, got %s", applyOutput)
	}
	if !strings.Contains(applyOutput, "Memory archive apply result: archived=2 retired_from_hot=2 idempotent_skipped=0 protected_skipped=0") {
		t.Fatalf("expected archive apply result, got %s", applyOutput)
	}
	if !strings.Contains(applyOutput, "Memory archive guardrail: Advisory records are tombstoned before leaving hot recall") {
		t.Fatalf("expected archive guardrail, got %s", applyOutput)
	}
	archiveOutput := captureRunOutput(t, []string{"memory", "archive-status", "--task", "demo-task"})
	if !strings.Contains(archiveOutput, "Memory archive status: scope=task task=demo-task") {
		t.Fatalf("expected archive status header, got %s", archiveOutput)
	}
	if !strings.Contains(archiveOutput, "Memory archive schema ready: yes") || !strings.Contains(archiveOutput, "Memory archive apply supported: yes") {
		t.Fatalf("expected archive schema reversible apply output, got %s", archiveOutput)
	}
	if !strings.Contains(archiveOutput, "Memory archive tombstones: total=2 archived=0 retired=2") {
		t.Fatalf("expected retired tombstone counts, got %s", archiveOutput)
	}
	if !strings.Contains(archiveOutput, "Memory archive supported tables: warm_lessons, evolution_candidates, project_lessons") {
		t.Fatalf("expected supported advisory tables, got %s", archiveOutput)
	}
	if !strings.Contains(archiveOutput, "Memory archive protected classes:") || !strings.Contains(archiveOutput, "verification_history") {
		t.Fatalf("expected protected classes, got %s", archiveOutput)
	}
	archiveListOutput := captureRunOutput(t, []string{"memory", "archive-list", "--task", "demo-task"})
	if !strings.Contains(archiveListOutput, "Memory archive list: scope=task task=demo-task") {
		t.Fatalf("expected archive list header, got %s", archiveListOutput)
	}
	if !strings.Contains(archiveListOutput, "Memory archive tombstones listed: 2") {
		t.Fatalf("expected two listed tombstones, got %s", archiveListOutput)
	}
	if !strings.Contains(archiveListOutput, "table=warm_lessons") || !strings.Contains(archiveListOutput, "reason=stale_or_low_confidence") || !strings.Contains(archiveListOutput, "state=retired") {
		t.Fatalf("expected warm lesson tombstone details, got %s", archiveListOutput)
	}
	if !strings.Contains(archiveListOutput, "summary=Old low-confidence lesson.") {
		t.Fatalf("expected tombstone summary, got %s", archiveListOutput)
	}
	memoryStoreAfterApply, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("reopen memory store failed: %v", err)
	}
	tombstones, err := memoryStoreAfterApply.ListMemoryArchiveTombstones(10)
	if closeErr := memoryStoreAfterApply.Close(); closeErr != nil {
		t.Fatalf("close memory store failed: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("list memory archive tombstones failed: %v", err)
	}
	if len(tombstones) == 0 {
		t.Fatalf("expected tombstones after apply")
	}
	tombstoneID := tombstones[0].TombstoneID
	if err := run([]string{"memory", "archive-restore", "--tombstone", tombstoneID, "--task", "demo-task"}); err == nil || !strings.Contains(err.Error(), "requires --dry-run") {
		t.Fatalf("expected missing dry-run restore error, got %v", err)
	}
	if err := run([]string{"memory", "archive-restore", "--dry-run", "--task", "demo-task"}); err == nil || !strings.Contains(err.Error(), "requires --tombstone") {
		t.Fatalf("expected missing tombstone restore error, got %v", err)
	}
	restoreOutput := captureRunOutput(t, []string{"memory", "archive-restore", "--dry-run", "--tombstone", tombstoneID, "--task", "demo-task"})
	if !strings.Contains(restoreOutput, "Memory archive restore dry run: scope=task task=demo-task") {
		t.Fatalf("expected restore dry-run header, got %s", restoreOutput)
	}
	if !strings.Contains(restoreOutput, "Memory archive restore target: table=") || !strings.Contains(restoreOutput, "state=retired") {
		t.Fatalf("expected restore target details, got %s", restoreOutput)
	}
	if !strings.Contains(restoreOutput, "Memory archive restore conflict: no hot_record_exists=no") || !strings.Contains(restoreOutput, "Memory archive restore supported: yes") {
		t.Fatalf("expected restorable dry-run output, got %s", restoreOutput)
	}
	if !strings.Contains(restoreOutput, "Memory archive restore guardrail: Dry-run only") {
		t.Fatalf("expected restore guardrail, got %s", restoreOutput)
	}
}

func TestRollbackCLI_InspectAndApplyBeforeImage(t *testing.T) {
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
	targetPath := filepath.Join(tempDir, "notes.txt")
	if err := os.WriteFile(targetPath, []byte("after"), 0o644); err != nil {
		t.Fatalf("write target failed: %v", err)
	}
	artifactPath := filepath.Join(tempDir, "rollback.json")
	artifact := runtime.CodingRollbackArtifact{
		SessionID:       "session-rollback",
		RunID:           "run-rollback",
		TaskID:          "task-rollback",
		Tool:            "write",
		Operation:       "file_write",
		Intent:          "restore test target",
		ExpectedTargets: []string{"notes.txt"},
		TargetPath:      targetPath,
		ExistedBefore:   true,
		BeforeContent:   "before",
		AfterSHA256:     sha256Hex("after"),
		CapturedAt:      time.Now().UTC(),
	}
	content, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact failed: %v", err)
	}
	if err := os.WriteFile(artifactPath, append(content, '\n'), 0o644); err != nil {
		t.Fatalf("write artifact failed: %v", err)
	}

	inspectOutput := captureRunOutput(t, []string{"rollback", "inspect", "--artifact", artifactPath})
	if !strings.Contains(inspectOutput, "Rollback guardrail: inspect only") || !strings.Contains(inspectOutput, "Rollback target: "+targetPath) {
		t.Fatalf("expected rollback inspect output, got %s", inspectOutput)
	}
	if err := run([]string{"rollback", "apply", "--artifact", artifactPath}); err == nil || !strings.Contains(err.Error(), "requires --confirm") {
		t.Fatalf("expected missing confirm error, got %v", err)
	}
	applyOutput := captureRunOutput(t, []string{"rollback", "apply", "--artifact", artifactPath, "--confirm"})
	if !strings.Contains(applyOutput, "Rollback applied: "+targetPath) {
		t.Fatalf("expected rollback apply output, got %s", applyOutput)
	}
	restored, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target failed: %v", err)
	}
	if string(restored) != "before" {
		t.Fatalf("expected rollback to restore before content, got %q", string(restored))
	}
}

func TestRollbackCLI_ApplyRejectsChangedCurrentTarget(t *testing.T) {
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
	targetPath := filepath.Join(tempDir, "notes.txt")
	if err := os.WriteFile(targetPath, []byte("operator follow-up edit"), 0o644); err != nil {
		t.Fatalf("write target failed: %v", err)
	}
	artifactPath := filepath.Join(tempDir, "rollback.json")
	artifact := runtime.CodingRollbackArtifact{
		SessionID:     "session-rollback",
		RunID:         "run-rollback",
		TaskID:        "task-rollback",
		Tool:          "write",
		Operation:     "file_write",
		TargetPath:    targetPath,
		ExistedBefore: true,
		BeforeContent: "before",
		AfterSHA256:   sha256Hex("after"),
		CapturedAt:    time.Now().UTC(),
	}
	content, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact failed: %v", err)
	}
	if err := os.WriteFile(artifactPath, append(content, '\n'), 0o644); err != nil {
		t.Fatalf("write artifact failed: %v", err)
	}

	err = run([]string{"rollback", "apply", "--artifact", artifactPath, "--confirm"})
	if err == nil || !strings.Contains(err.Error(), "rollback target changed since capture") {
		t.Fatalf("expected changed target rollback denial, got %v", err)
	}
	current, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target failed: %v", err)
	}
	if string(current) != "operator follow-up edit" {
		t.Fatalf("expected rollback denial to preserve current content, got %q", string(current))
	}
}

func TestGovernanceCLI_StatusReportsCrossDomainReadiness(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "governance-task", Title: "Governance task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	now := time.Now().UTC()
	if err := memoryStore.RecordWarmLesson(memstore.WarmLessonRecord{SessionID: "session-governance", RunID: "run-governance", TaskID: workspace.ID, Kind: "workflow", Summary: "Low-confidence governance lesson.", Source: "task_summary", Confidence: "low", UpdatedAt: now.AddDate(0, 0, -45)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record warm lesson failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-governance", RunID: "run-governance", TaskID: workspace.ID, Kind: "runtime_lifecycle", Verdict: "INFO", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated.", Source: "runtime", Details: []string{"status: completed", "origin_run_id: run-governance", "origin_task_id: " + workspace.ID}, UpdatedAt: now}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record evaluation failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-governance", RunID: "run-governance", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "INFO", Cause: "node_work_evidence", Summary: "Workflow node node-build completed.", Source: "workflow_node", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: completed", "verifier_gate_status: verified_pass", "verified: true"}, UpdatedAt: now.Add(time.Second)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record node evidence failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-governance", RunID: "run-governance", TaskID: workspace.ID, Tool: "write", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "tool_approval_required", Summary: "Tool write/file_write is awaiting approval.", Source: "tool_runtime", Details: []string{"operation: file_write", "approval_key: approval-governance"}, UpdatedAt: now.Add(2 * time.Second)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record pending approval failed: %v", err)
	}
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-governance", RunID: "run-governance", TaskID: workspace.ID, Tool: "go test", Verdict: "FAIL", Summary: "Verification failed.", UpdatedAt: now}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record verification failed: %v", err)
	}
	_ = memoryStore.Close()

	output := captureRunOutput(t, []string{"governance", "status", "--task", "governance-task"})
	if !strings.Contains(output, "Governance status: scope=task task=governance-task") {
		t.Fatalf("expected governance status header, got %s", output)
	}
	if !strings.Contains(output, "Governance readiness: runtime_truth=review memory=review skills=ok verifier=pending") {
		t.Fatalf("expected cross-domain readiness, got %s", output)
	}
	if !strings.Contains(output, "Runtime truth: task_status=awaiting_approval") || !strings.Contains(output, "evaluation_records=3") {
		t.Fatalf("expected runtime truth counts, got %s", output)
	}
	if !strings.Contains(output, "Memory lifecycle: candidates=1") || !strings.Contains(output, "protected_truth=4") {
		t.Fatalf("expected memory lifecycle counts, got %s", output)
	}
	if !strings.Contains(output, "Workflow node execution: latest_node_evidence=yes failed_node_retry_candidate=no pending_approval=yes verifier_pending=yes") {
		t.Fatalf("expected workflow node execution summary, got %s", output)
	}
	if !strings.Contains(output, "Workflow node latest: node-build status=completed gate=verified_pass") {
		t.Fatalf("expected latest workflow node details, got %s", output)
	}
	if !strings.Contains(output, "Workflow node next action: inspect pending approval before continuation") {
		t.Fatalf("expected workflow node approval action cue, got %s", output)
	}
	if !strings.Contains(output, "Skill governance: tracked=0") || !strings.Contains(output, "drifts=0") {
		t.Fatalf("expected skill governance counts, got %s", output)
	}
	if !strings.Contains(output, "Governance blockers: total=2") || !strings.Contains(output, "verifier_pending=yes") {
		t.Fatalf("expected governance blockers, got %s", output)
	}
	if !strings.Contains(output, "Governance guardrail: read-only status.") {
		t.Fatalf("expected governance guardrail, got %s", output)
	}
	viewOutput := captureRunOutput(t, []string{"tasks", "show", "governance-task", "--view", "governance"})
	if !strings.Contains(viewOutput, "Task governance view: task=governance-task") {
		t.Fatalf("expected task governance view header, got %s", viewOutput)
	}
	if !strings.Contains(viewOutput, "Governance readiness: runtime_truth=review memory=review skills=ok verifier=pending") {
		t.Fatalf("expected task governance readiness, got %s", viewOutput)
	}
	if !strings.Contains(viewOutput, "Runtime truth: task_status=awaiting_approval") {
		t.Fatalf("expected task governance finality to show pending approval over completed lifecycle, got %s", viewOutput)
	}
	if !strings.Contains(viewOutput, "Workflow node execution: latest_node_evidence=yes failed_node_retry_candidate=no pending_approval=yes verifier_pending=yes") {
		t.Fatalf("expected task workflow node execution summary, got %s", viewOutput)
	}
	if !strings.Contains(viewOutput, "Workflow node next action: inspect pending approval before continuation") {
		t.Fatalf("expected task workflow node approval action cue, got %s", viewOutput)
	}
	if !strings.Contains(viewOutput, "Task governance gate: review blockers before memory restore apply") {
		t.Fatalf("expected task governance gate wording, got %s", viewOutput)
	}
}

func TestGovernanceCLI_StatusShowsRecordsOnlyVerifierClosureEvidence(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "records-only-task", Title: "Records only task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	now := time.Now().UTC()
	if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: "session-old", RunID: "run-old", TaskID: workspace.ID, Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: filepath.Join(workspace.MemoryDir, "verifier", "run-old-patch.json"), Checks: []string{"go test -race ./... => PARTIAL"}, UpdatedAt: now.Add(-time.Minute)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record partial verification failed: %v", err)
	}
	for i := 0; i < 11; i++ {
		runID := fmt.Sprintf("run-pass-%02d", i+1)
		if err := memoryStore.RecordVerification(memstore.VerificationRecord{SessionID: fmt.Sprintf("session-pass-%02d", i+1), RunID: runID, TaskID: workspace.ID, Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: filepath.Join(workspace.MemoryDir, "verifier", runID+".json"), Checks: []string{"go test ./... => PASS"}, UpdatedAt: now.Add(time.Duration(i) * time.Minute)}); err != nil {
			_ = memoryStore.Close()
			t.Fatalf("record pass verification %d failed: %v", i+1, err)
		}
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-new", RunID: "run-new", TaskID: workspace.ID, Tool: "verify", Kind: "workflow_feedback", Verdict: "PASS", Cause: "verification_reverify_covered", Summary: "Verification PASS now covers the latest non-pass verification: PARTIAL via patch.", Source: "verification_followup", ReportPath: filepath.Join(workspace.MemoryDir, "verifier", "run-new-verify.json"), Details: []string{"covered_tool: patch", "covered_verdict: PARTIAL"}, UpdatedAt: now.Add(time.Nanosecond)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record closure evaluation failed: %v", err)
	}
	_ = memoryStore.Close()

	output := captureRunOutput(t, []string{"tasks", "show", workspace.ID, "--view", "governance"})
	if !strings.Contains(output, "Verifier: current=PASS latest_non_pass=none reverify=none history_window=bounded closure_evidence=evaluation_records") {
		t.Fatalf("expected records-only verifier closure evidence in governance view, got %s", output)
	}
	if !strings.Contains(output, "Governance guardrail: read-only status.") {
		t.Fatalf("expected governance guardrail, got %s", output)
	}
}

func TestTasksCLI_ShowFailedNodeRecoverySummaryStaysManual(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-failed-node", RunID: "run-failed-node", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "FAIL", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Source: "workflow_node", Details: []string{"pause_point_id: pause-build", "pause_point_kind: workflow_node", "pause_point_digest: digest-build", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-failed-node", "origin_task_id: demo-task", "depends_on: node-plan", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review", "expected_targets: note.txt"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record failed-node pause point failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-retry", RunID: "run-retry", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "INFO", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Source: "workflow_scheduler", Details: []string{"retry_attempt_id: retry-build", "retry_attempt_status: completed", "retry_attempt_policy_reason: operator requested bounded failed-node retry", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "pause_point_id: pause-build", "pause_point_kind: workflow_node", "pause_point_digest: digest-build", "node_id: node-build", "status_reason: tool failed", "origin_run_id: run-failed-node", "origin_task_id: demo-task", "expected_targets: note.txt"}, UpdatedAt: time.Now().UTC().Add(time.Second)}); err != nil {
		t.Fatalf("record failed-node retry attempt failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-retry", RunID: "run-retry", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "INFO", Cause: "node_work_evidence", Summary: "Retried workflow node node-build completed.", Source: "workflow_node", Details: []string{"retry_attempt_id: retry-build", "node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verified: true", "verification_report_path: verifier/retry-build.json"}, UpdatedAt: time.Now().UTC().Add(2 * time.Second)}); err != nil {
		t.Fatalf("record failed-node retry node evidence failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-retry", RunID: "run-retry", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "INFO", Cause: "failed_node_retry_scheduler_resume", Summary: "Dependent workflow nodes resumed after failed-node retry.", Source: "workflow_scheduler", Details: []string{"retry_attempt_id: retry-build", "scheduler_resume_status: completed", "continuation_run_id: run-retry", "continuation_task_id: demo-task"}, UpdatedAt: time.Now().UTC().Add(3 * time.Second)}); err != nil {
		t.Fatalf("record failed-node retry scheduler resume failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-retry", RunID: "run-retry", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "INFO", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated after failed-node retry.", Source: "workflow_lifecycle", Details: []string{"retry_attempt_id: retry-build", "status: completed", "origin_run_id: run-retry", "origin_task_id: demo-task"}, UpdatedAt: time.Now().UTC().Add(4 * time.Second)}); err != nil {
		t.Fatalf("record failed-node retry lifecycle failed: %v", err)
	}
	latestSnapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		t.Fatalf("load latest task snapshot failed: %v", err)
	}
	expectedGovernance := memstore.WorkflowNodeGovernanceSnapshotForTask(latestSnapshot)
	expectedGovernanceCue := expectedGovernance.NextActionCue
	if expectedGovernanceCue != "manual review required before risky continuation" {
		t.Fatalf("expected shared workflow node governance cue, got %q", expectedGovernanceCue)
	}
	if expectedGovernance.RetryClosureVerifierReportPath != "verifier/retry-build.json" {
		t.Fatalf("expected shared workflow node verifier report path, got %+v", expectedGovernance)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	governanceViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "governance"})
	if !strings.Contains(showOutput, "Recovery summary: category=retryable | action=plan_failed_node_retry | guard=manual_review_required | authority=workflow_scheduler | source=failed_node_pause_point") {
		t.Fatalf("expected failed-node recovery summary, got %s", showOutput)
	}
	if strings.Contains(showOutput, "Recovery summary: category=retryable | action=plan_failed_node_retry | guard=guarded_ready") || strings.Contains(showOutput, "Recovery summary: category=retryable | action=plan_failed_node_retry | guard=manual_review_required | closure=") {
		t.Fatalf("expected failed-node summary to avoid guarded readiness and verifier closure, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary coordinates: pause=pause-build") {
		t.Fatalf("expected failed-node recovery coordinates, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary retryable: true") {
		t.Fatalf("expected failed-node retryable flag, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Recovery summary guidance: A failed workflow node is retryable; inspect the pause point before planning a bounded retry.") {
		t.Fatalf("expected failed-node recovery guidance, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry candidate: node=node-build | pause=pause-build | kind=workflow_node | contract_ready=true | authority=workflow_scheduler | guard=manual_review_required") {
		t.Fatalf("expected failed-node retry candidate, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry candidate coordinates: digest=digest-build | origin_run=run-failed-node | origin_task=demo-task") {
		t.Fatalf("expected failed-node retry candidate coordinates, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry attempt: attempt=retry-build | status=completed | closure=node_scheduler_verifier_lifecycle_closed | open=false | closed=true | failed=false") {
		t.Fatalf("expected failed-node retry attempt, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry attempt verifier gate expectation: verify mutating targets before closure") {
		t.Fatalf("expected failed-node retry verifier gate expectation, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry attempt closure: attempt=retry-build | attempt_status=completed | closure_status=node_scheduler_verifier_lifecycle_closed | ready=true | authority=workflow_scheduler | guard=manual_review_required") {
		t.Fatalf("expected failed-node retry closure evidence, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry attempt closure node work: node=node-build | status=completed | gate=verified_pass | verdict=PASS") {
		t.Fatalf("expected failed-node retry node work closure evidence, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry attempt closure scheduler resume: status=completed | run=run-retry | task=demo-task") {
		t.Fatalf("expected failed-node retry scheduler closure evidence, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry attempt closure lifecycle: status=completed | run=run-retry | task=demo-task") {
		t.Fatalf("expected failed-node retry lifecycle closure evidence, got %s", showOutput)
	}
	if strings.Contains(showOutput, "Failed node retry artifact: artifact=failed-node-retry-artifact-") {
		t.Fatalf("expected terminal retry scope to suppress executable-looking artifact, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry current state: ready=true | node_status=failed | dependencies=recorded | continuation=requires_manual_review") {
		t.Fatalf("expected failed-node retry current state readiness, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry current state depends on: node-plan") {
		t.Fatalf("expected failed-node retry dependency snapshot, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Failed node retry current state policy: authority=workflow_scheduler | guard=manual_review_required") {
		t.Fatalf("expected failed-node retry current state policy guard, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View recovery summary: category=retryable | action=plan_failed_node_retry | guard=manual_review_required | authority=workflow_scheduler | source=failed_node_pause_point") {
		t.Fatalf("expected verifier view failed-node recovery summary, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View failed node retry candidate: node=node-build | pause=pause-build | kind=workflow_node | contract_ready=true | authority=workflow_scheduler | guard=manual_review_required") {
		t.Fatalf("expected verifier view failed-node retry candidate, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View failed node retry attempt: attempt=retry-build | status=completed | closure=node_scheduler_verifier_lifecycle_closed | open=false | closed=true | failed=false") {
		t.Fatalf("expected verifier view failed-node retry attempt, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View failed node retry attempt closure: attempt=retry-build | attempt_status=completed | closure_status=node_scheduler_verifier_lifecycle_closed | ready=true | authority=workflow_scheduler | guard=manual_review_required") {
		t.Fatalf("expected verifier view failed-node retry closure evidence, got %s", verifierViewOutput)
	}
	if strings.Contains(verifierViewOutput, "View failed node retry artifact: artifact=failed-node-retry-artifact-") {
		t.Fatalf("expected verifier terminal retry scope to suppress executable-looking artifact, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View failed node retry current state: ready=true | node_status=failed | dependencies=recorded | continuation=requires_manual_review") {
		t.Fatalf("expected verifier view failed-node retry current state readiness, got %s", verifierViewOutput)
	}
	if strings.Contains(verifierViewOutput, "View recovery summary: category=retryable | action=plan_failed_node_retry | guard=guarded_ready") || strings.Contains(verifierViewOutput, "View recovery summary: category=retryable | action=plan_failed_node_retry | guard=manual_review_required | closure=") {
		t.Fatalf("expected verifier view failed-node summary to avoid guarded readiness and verifier closure, got %s", verifierViewOutput)
	}
	if !strings.Contains(governanceViewOutput, "Workflow node latest: node-build status=completed gate=verified_pass verdict=PASS") {
		t.Fatalf("expected governance view workflow node verifier verdict, got %s", governanceViewOutput)
	}
	if !strings.Contains(governanceViewOutput, "Workflow node retry closure: attempt=retry-build ready=yes missing=0 status=completed") {
		t.Fatalf("expected governance view retry closure readiness, got %s", governanceViewOutput)
	}
	if !strings.Contains(governanceViewOutput, "Workflow node retry verifier gate: status=verified_pass verdict=PASS") {
		t.Fatalf("expected governance view retry verifier gate, got %s", governanceViewOutput)
	}
	if !strings.Contains(governanceViewOutput, "Workflow node retry verifier report: verifier/retry-build.json") {
		t.Fatalf("expected governance view retry verifier report path, got %s", governanceViewOutput)
	}
	if !strings.Contains(governanceViewOutput, "Workflow node next action: "+expectedGovernanceCue) {
		t.Fatalf("expected governance view manual review action cue, got %s", governanceViewOutput)
	}
}

func TestTasksCLI_RetryNodeDryRunInspection(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-failed-node", RunID: "run-failed-node", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "FAIL", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Source: "workflow_node", Details: []string{"pause_point_id: pause-build", "pause_point_kind: workflow_node", "pause_point_digest: digest-build", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-failed-node", "origin_task_id: demo-task", "depends_on: node-plan", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review", "expected_targets: note.txt"}, UpdatedAt: time.Now().UTC()}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record failed-node pause point failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-retry", RunID: "run-retry", TaskID: workspace.ID, Tool: "write", Kind: "workflow_feedback", Verdict: "INFO", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt requested.", Source: "workflow_scheduler", Details: []string{"retry_attempt_id: retry-build", "retry_attempt_status: requested", "retry_attempt_policy_reason: operator requested bounded failed-node retry", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "pause_point_id: pause-build", "pause_point_kind: workflow_node", "pause_point_digest: digest-build", "node_id: node-build", "status_reason: tool failed", "origin_run_id: run-failed-node", "origin_task_id: demo-task", "expected_targets: note.txt"}, UpdatedAt: time.Now().UTC().Add(time.Second)}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record failed-node retry attempt failed: %v", err)
	}
	beforeSnapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		_ = memoryStore.Close()
		t.Fatalf("load before snapshot failed: %v", err)
	}
	beforeRecords := len(beforeSnapshot.EvaluationRecords)
	_ = memoryStore.Close()

	output := captureRunOutput(t, []string{"tasks", "retry-node", "demo-task", "--origin-run", "run-failed-node", "--pause-point", "pause-build", "--node", "node-build", "--pause-digest", "digest-build", "--dry-run"})
	if !strings.Contains(output, "Failed node retry dry run: inspection-only") {
		t.Fatalf("expected dry-run inspection header, got %s", output)
	}
	if !strings.Contains(output, "Failed node retry preflight: allowed=false | status=denied | authority=workflow_scheduler | guard=manual_review_required") {
		t.Fatalf("expected denied preflight, got %s", output)
	}
	if !strings.Contains(output, "Failed node retry preflight verifier gate expectation: verify mutating targets before closure") {
		t.Fatalf("expected verifier gate expectation, got %s", output)
	}
	if !strings.Contains(output, "Failed node retry preflight reasons: retry_attempt_in_flight") {
		t.Fatalf("expected in-flight denial, got %s", output)
	}
	if !strings.Contains(output, "Failed node retry candidate: node=node-build | pause=pause-build | kind=workflow_node | contract_ready=true | authority=workflow_scheduler | guard=manual_review_required") {
		t.Fatalf("expected candidate rendering, got %s", output)
	}
	if !strings.Contains(output, "Failed node retry current state: ready=true | node_status=failed | dependencies=recorded | continuation=requires_manual_review") {
		t.Fatalf("expected current state readiness, got %s", output)
	}
	if strings.Contains(output, "Failed node retry artifact: artifact=failed-node-retry-artifact-") {
		t.Fatalf("expected in-flight preflight denial to produce no artifact, got %s", output)
	}
	if !strings.Contains(output, "Failed node retry latest attempt: attempt=retry-build | status=requested") {
		t.Fatalf("expected latest attempt inspection, got %s", output)
	}
	if !strings.Contains(output, "Failed node retry dry run result: no retry attempt recorded; scheduler execution unavailable") {
		t.Fatalf("expected no-write guidance, got %s", output)
	}
	memoryStore, err = memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("reopen memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	afterSnapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		t.Fatalf("load after snapshot failed: %v", err)
	}
	if len(afterSnapshot.EvaluationRecords) != beforeRecords {
		t.Fatalf("expected dry run to avoid writing records, before=%d after=%d", beforeRecords, len(afterSnapshot.EvaluationRecords))
	}
}

func TestTasksCLI_RetryNodeDryRunRejectsExecutableAndMissingDryRun(t *testing.T) {
	if err := run([]string{"tasks", "retry-node", "demo-task", "--origin-run", "run-failed-node", "--pause-point", "pause-build", "--node", "node-build", "--pause-digest", "digest-build"}); err == nil || !strings.Contains(err.Error(), "requires --dry-run") {
		t.Fatalf("expected missing dry-run denial, got %v", err)
	}
	if err := run([]string{"tasks", "retry-node", "demo-task", "--origin-run", "run-failed-node", "--pause-point", "pause-build", "--node", "node-build", "--pause-digest", "digest-build", "--confirm-recorded-truth"}); err == nil || !strings.Contains(err.Error(), "execution is unavailable") {
		t.Fatalf("expected executable unavailable denial, got %v", err)
	}
}

func TestTasksCLI_ShowLatestPermissionDenial(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-denied", RunID: "run-denied", TaskID: workspace.ID, Tool: "write", Kind: "passive_feedback", Verdict: "FAIL", Cause: "tool_permission_denied", Summary: "Tool write/file_write was denied: write tool target escapes sandbox: ../notes.txt", Source: "tool_runtime", Details: []string{"decision_source: tool_sandbox", "permission_mode: dontAsk", "retryable: false", "reason: tool_failure_permission_gated"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record permission denial evaluation failed: %v", err)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	if !strings.Contains(showOutput, "Latest permission denial: Tool write/file_write was denied") {
		t.Fatalf("expected tasks show latest permission denial, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest permission denial source: tool_sandbox") {
		t.Fatalf("expected tasks show permission denial source, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest tool failure retry: Tool write/file_write was denied") {
		t.Fatalf("expected tasks show latest tool failure retry, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest tool failure retryable: false") {
		t.Fatalf("expected tasks show retryable false, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest tool failure retry reason: tool_failure_permission_gated") {
		t.Fatalf("expected tasks show retry reason, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest tool failure retry source: tool_sandbox") {
		t.Fatalf("expected tasks show retry source, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest tool failure retry mode: dontAsk") {
		t.Fatalf("expected tasks show retry permission mode, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest permission denial: Tool write/file_write was denied") {
		t.Fatalf("expected verifier view latest permission denial, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest permission denial source: tool_sandbox") {
		t.Fatalf("expected verifier view permission denial source, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest tool failure retry: Tool write/file_write was denied") {
		t.Fatalf("expected verifier view latest tool failure retry, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest tool failure retryable: false") {
		t.Fatalf("expected verifier view retryable false, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest tool failure retry reason: tool_failure_permission_gated") {
		t.Fatalf("expected verifier view retry reason, got %s", verifierViewOutput)
	}
}

func TestTasksCLI_ShowLatestNodeEvidence(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	record := memstore.EvaluationRecord{
		SessionID: "session-node",
		RunID:     "run-node",
		TaskID:    workspace.ID,
		Tool:      "write",
		Kind:      "workflow_feedback",
		Verdict:   "INFO",
		Cause:     "node_work_evidence",
		Summary:   "Workflow node node-build completed through approval replay.",
		Source:    "workflow_node",
		Details: []string{
			"node_id: node-build",
			"node_role: Builder",
			"node_title: Shape the smallest viable change",
			"tool: write",
			"operation: file_write",
			"status: replay_completed",
			"approval_key: approval-build",
			"artifact_ids: generated-skill, bootstrap-context",
			"expected_targets: skills/generated/example.md, process_record.md",
			"origin_run_id: origin-run",
			"origin_task_id: origin-task",
			"pause_point_id: pause-build",
			"continuation_run_id: continuation-run",
			"continuation_task_id: continuation-task",
			"replay_status: completed",
			"verifier_gate_status: verified_pass",
			"verifier_verdict: PASS",
			"verified: true",
			"verification_report_path: verifier/run-node.json",
			"recovery_kind: approval_replay",
		},
		UpdatedAt: time.Now().UTC(),
	}
	if err := memoryStore.RecordEvaluation(record); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record node evidence failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "session-node",
		RunID:     "run-node",
		TaskID:    workspace.ID,
		Tool:      "read",
		Kind:      "workflow_feedback",
		Verdict:   "INFO",
		Cause:     "node_work_evidence",
		Summary:   "Workflow node node-survey completed repository survey.",
		Source:    "workflow_node",
		Details: []string{
			"node_id: node-survey",
			"node_role: Researcher",
			"tool: read",
			"operation: file_read",
			"status: completed",
		},
		UpdatedAt: time.Now().UTC().Add(-time.Second),
	}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record survey node evidence failed: %v", err)
	}
	_ = memoryStore.Close()

	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	for _, expected := range []string{
		"Latest node evidence: Workflow node node-build completed through approval replay.",
		"Latest node ID: node-build",
		"Latest node role: Builder",
		"Latest node title: Shape the smallest viable change",
		"Latest node tool: write/file_write",
		"Latest node status: replay_completed",
		"Latest node verifier gate: verified_pass",
		"Latest node recovery: approval_replay",
		"Latest node replay: completed",
		"Node evidence chain:",
		"#1 node-build | Builder | write/file_write | status=replay_completed | gate=verified_pass | recovery=approval_replay | replay=completed",
		"@1 artifacts: generated-skill, bootstrap-context",
		"@1 targets: skills/generated/example.md, process_record.md",
		"@1 verification report: verifier/run-node.json",
		"@1 coordinates: pause=pause-build | origin_run=origin-run | origin_task=origin-task | node_title=Shape the smallest viable change | continuation_run=continuation-run | continuation_task=continuation-task",
		"#2 node-survey | Researcher | read/file_read | status=completed",
	} {
		if !strings.Contains(showOutput, expected) {
			t.Fatalf("expected tasks show to include %q, got %s", expected, showOutput)
		}
	}
	for _, expected := range []string{
		"View latest node evidence: Workflow node node-build completed through approval replay.",
		"View latest node ID: node-build",
		"View latest node role: Builder",
		"View latest node title: Shape the smallest viable change",
		"View latest node tool: write/file_write",
		"View latest node verifier gate: verified_pass",
		"View latest node verifier verdict: PASS",
		"View latest node replay: completed",
		"View latest node continuation run ID: continuation-run",
		"View latest node continuation task ID: continuation-task",
		"View node evidence chain:",
		"#1 node-build | Builder | write/file_write | status=replay_completed | gate=verified_pass | recovery=approval_replay | replay=completed",
		"@1 artifacts: generated-skill, bootstrap-context",
		"@1 targets: skills/generated/example.md, process_record.md",
		"@1 verification report: verifier/run-node.json",
		"@1 coordinates: pause=pause-build | origin_run=origin-run | origin_task=origin-task | node_title=Shape the smallest viable change | continuation_run=continuation-run | continuation_task=continuation-task",
		"#2 node-survey | Researcher | read/file_read | status=completed",
	} {
		if !strings.Contains(verifierViewOutput, expected) {
			t.Fatalf("expected verifier view to include %q, got %s", expected, verifierViewOutput)
		}
	}
}

func TestTasksCLI_ShowLatestApprovalRequired(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-awaiting-approval", RunID: "run-awaiting-approval", TaskID: workspace.ID, Tool: "write", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "tool_approval_required", Summary: "Tool write/file_write is awaiting approval: approval required: Permission mode default requires approval before mutating write actions, and approval prompts are not implemented yet.", Source: "tool_runtime", Details: []string{"decision_source: runtime_permission_mode", "permission_mode: default"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval required evaluation failed: %v", err)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	if !strings.Contains(showOutput, "Latest approval required: Tool write/file_write is awaiting approval") {
		t.Fatalf("expected tasks show latest approval required, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest approval required source: runtime_permission_mode") {
		t.Fatalf("expected tasks show approval required source, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest approval required: Tool write/file_write is awaiting approval") {
		t.Fatalf("expected verifier view latest approval required, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest approval required source: runtime_permission_mode") {
		t.Fatalf("expected verifier view approval required source, got %s", verifierViewOutput)
	}
}

func TestTasksCLI_DerivesAwaitingApprovalStatus(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-awaiting-approval", RunID: "run-awaiting-approval", TaskID: workspace.ID, Tool: "write", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "tool_approval_required", Summary: "Tool write/file_write is awaiting approval.", Source: "tool_runtime", Details: []string{"decision_source: runtime_permission_mode", "approval_key: approval-key-demo"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval required evaluation failed: %v", err)
	}
	listOutput := captureRunOutput(t, []string{"tasks", "list"})
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	if !strings.Contains(listOutput, "demo-task\tawaiting_approval\t0\tDemo task") {
		t.Fatalf("expected tasks list awaiting_approval status, got %s", listOutput)
	}
	if !strings.Contains(showOutput, "Status: awaiting_approval") {
		t.Fatalf("expected tasks show awaiting_approval status, got %s", showOutput)
	}
}

func TestTasksCLI_ApproveLatestPendingApproval(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	approvalKey := "approval-key-demo"
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-awaiting-approval", RunID: "run-awaiting-approval", TaskID: workspace.ID, Tool: "write", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "tool_approval_required", Summary: "Tool write/file_write is awaiting approval: approval required: Permission mode default requires approval before mutating write actions, and approval prompts are not implemented yet.", Source: "tool_runtime", Details: []string{"operation: file_write", "decision_source: runtime_permission_mode", "permission_mode: default", "approval_key: " + approvalKey}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval required evaluation failed: %v", err)
	}
	approveOutput := captureRunOutput(t, []string{"tasks", "approve", "demo-task"})
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	if !strings.Contains(approveOutput, "approval granted") {
		t.Fatalf("expected approve output, got %s", approveOutput)
	}
	if !strings.Contains(approveOutput, approvalKey) {
		t.Fatalf("expected approve output to print approval key, got %s", approveOutput)
	}
	if strings.Contains(showOutput, "Latest approval required:") {
		t.Fatalf("expected tasks show to clear pending approval after approve, got %s", showOutput)
	}
	if strings.Contains(showOutput, "avatars tasks approve demo-task") {
		t.Fatalf("expected tasks show suggested commands to drop approve after approval, got %s", showOutput)
	}
}

func TestTasksCLI_DenyLatestPendingApproval(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	approvalKey := "approval-key-demo"
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-awaiting-approval", RunID: "run-awaiting-approval", TaskID: workspace.ID, Tool: "write", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "tool_approval_required", Summary: "Tool write/file_write is awaiting approval: approval required: Permission mode default requires approval before mutating write actions, and approval prompts are not implemented yet.", Source: "tool_runtime", Details: []string{"operation: file_write", "decision_source: runtime_permission_mode", "permission_mode: default", "approval_key: " + approvalKey}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval required evaluation failed: %v", err)
	}
	denyOutput := captureRunOutput(t, []string{"tasks", "deny", "demo-task"})
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	if !strings.Contains(denyOutput, "approval denied") {
		t.Fatalf("expected deny output, got %s", denyOutput)
	}
	if strings.Contains(showOutput, "Latest approval required:") {
		t.Fatalf("expected tasks show to clear pending approval after deny, got %s", showOutput)
	}
}

func TestTasksCLI_ShowsContinuedStatusFromLifecycleEvaluation(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-lifecycle", RunID: "run-origin", TaskID: workspace.ID, Tool: "write", Kind: "runtime_lifecycle", Verdict: "INFO", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to continued.", Source: "runtime", Details: []string{"status: continued", "origin_run_id: run-origin", "origin_task_id: " + workspace.ID}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record lifecycle evaluation failed: %v", err)
	}
	listOutput := captureRunOutput(t, []string{"tasks", "list"})
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	if !strings.Contains(listOutput, "demo-task\tcontinued\t0\tDemo task") {
		t.Fatalf("expected tasks list continued status, got %s", listOutput)
	}
	if !strings.Contains(showOutput, "Status: continued") {
		t.Fatalf("expected tasks show continued status, got %s", showOutput)
	}
}

func TestTasksCLI_ApproveSelectedPendingApprovalByKey(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-awaiting-approval", RunID: "run-awaiting-approval-b", TaskID: workspace.ID, Tool: "patch", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "tool_approval_required", Summary: "Tool patch/file_patch is awaiting approval.", Source: "tool_runtime", Details: []string{"operation: file_patch", "decision_source: runtime_permission_mode", "permission_mode: default", "approval_key: approval-key-b"}, UpdatedAt: time.Now().UTC().Add(time.Second)}); err != nil {
		t.Fatalf("record approval required evaluation failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-awaiting-approval", RunID: "run-awaiting-approval-a", TaskID: workspace.ID, Tool: "write", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "tool_approval_required", Summary: "Tool write/file_write is awaiting approval.", Source: "tool_runtime", Details: []string{"operation: file_write", "decision_source: runtime_permission_mode", "permission_mode: default", "approval_key: approval-key-a"}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval required evaluation failed: %v", err)
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	if !strings.Contains(showOutput, "Pending approvals: 2") {
		t.Fatalf("expected pending approval count in show output, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "approval-key-b") || !strings.Contains(showOutput, "approval-key-a") {
		t.Fatalf("expected both pending approval keys in show output, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "avatars tasks approve demo-task --approval-key approval-key-b") {
		t.Fatalf("expected keyed suggested command for latest pending approval, got %s", showOutput)
	}
	approveOutput := captureRunOutput(t, []string{"tasks", "approve", "demo-task", "--approval-key", "approval-key-a"})
	if !strings.Contains(approveOutput, "approval granted") {
		t.Fatalf("expected approve output, got %s", approveOutput)
	}
	if !strings.Contains(approveOutput, "approval-key-a") {
		t.Fatalf("expected selected approval key in output, got %s", approveOutput)
	}
	showAfter := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	if !strings.Contains(showAfter, "Pending approvals: 1") {
		t.Fatalf("expected one pending approval to remain, got %s", showAfter)
	}
	if strings.Contains(showAfter, "approval-key-a") {
		t.Fatalf("expected selected approval key to be cleared from pending queue, got %s", showAfter)
	}
	if !strings.Contains(showAfter, "approval-key-b") {
		t.Fatalf("expected unresolved approval-key-b to remain pending, got %s", showAfter)
	}
}

func TestTasksCLI_ApproveLatestPendingApprovalWithReplay(t *testing.T) {
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
	if err := os.WriteFile("go.mod", []byte("module example.com/continuationtest\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.WriteFile("demo.go", []byte("package continuationtest\n\nfunc Value() string { return \"ok\" }\n"), 0o644); err != nil {
		t.Fatalf("write demo.go failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	application, err := app.BootstrapWithOptions(app.BootstrapOptions{Workspace: &workspace, PermissionMode: runtime.PermissionModeDefault, AllowInvalidLLMConfig: true})
	if err != nil {
		t.Fatalf("bootstrap app failed: %v", err)
	}
	_, err = application.Engine.CallTool(context.Background(), "write", "file_write", tools.WriteInput{Path: "notes.txt", Content: "approved via continuation", WorkingDir: tempDir})
	_ = application.Close()
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected initial write to await approval, got %v", err)
	}
	approveOutput := captureRunOutput(t, []string{"tasks", "approve", "demo-task", "--replay"})
	if !strings.Contains(approveOutput, "approval granted") {
		t.Fatalf("expected approve output, got %s", approveOutput)
	}
	if !strings.Contains(approveOutput, "Wrote") {
		t.Fatalf("expected continued write output, got %s", approveOutput)
	}
	content, err := os.ReadFile(filepath.Join(tempDir, "notes.txt"))
	if err != nil {
		t.Fatalf("read continued file failed: %v", err)
	}
	if string(content) != "approved via continuation" {
		t.Fatalf("expected continued file content, got %q", string(content))
	}
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task"})
	verifierViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-task", "--view", "verifier"})
	if strings.Contains(showOutput, "Latest approval required:") {
		t.Fatalf("expected continuation to clear pending approval, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest transcript:") {
		t.Fatalf("expected continuation to update latest transcript, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest approval continuation: Continued approved request for write/file_write") {
		t.Fatalf("expected tasks show latest approval continuation, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest approval continuation source: task_operator_replay") {
		t.Fatalf("expected tasks show latest approval continuation source, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest approval continuation transcript:") {
		t.Fatalf("expected tasks show latest approval continuation transcript, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest approval continuation run ID:") {
		t.Fatalf("expected tasks show latest approval continuation run ID, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Latest approval continuation task ID: demo-task") {
		t.Fatalf("expected tasks show latest approval continuation task ID, got %s", showOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest approval continuation: Continued approved request for write/file_write") {
		t.Fatalf("expected verifier view latest approval continuation, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest approval continuation source: task_operator_replay") {
		t.Fatalf("expected verifier view latest approval continuation source, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest approval continuation transcript:") {
		t.Fatalf("expected verifier view latest approval continuation transcript, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest approval continuation run ID:") {
		t.Fatalf("expected verifier view latest approval continuation run ID, got %s", verifierViewOutput)
	}
	if !strings.Contains(verifierViewOutput, "View latest approval continuation task ID: demo-task") {
		t.Fatalf("expected verifier view latest approval continuation task ID, got %s", verifierViewOutput)
	}
}

func TestLLMCLI_ProvidersAndShow(t *testing.T) {
	providersOutput := captureRunOutput(t, []string{"llm", "providers"})
	showOutput := captureRunOutput(t, []string{"llm", "show", "openrouter"})

	if !strings.Contains(providersOutput, "Provider\tFamily\tWebSearch") {
		t.Fatalf("expected providers output header, got %s", providersOutput)
	}
	if !strings.Contains(providersOutput, "openrouter\topenai-compatible\tyes") {
		t.Fatalf("expected providers output to include openrouter summary, got %s", providersOutput)
	}
	if !strings.Contains(providersOutput, "google\tgenkit\tyes") {
		t.Fatalf("expected providers output to include google summary, got %s", providersOutput)
	}
	if !strings.Contains(showOutput, "Provider: openrouter") {
		t.Fatalf("expected provider show output, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Supports web search: yes") {
		t.Fatalf("expected openrouter web search support in show output, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Default base URL: https://openrouter.ai/api/v1") {
		t.Fatalf("expected openrouter base url in show output, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Default API key env: OPENROUTER_API_KEY") {
		t.Fatalf("expected openrouter api key env in show output, got %s", showOutput)
	}
}

func TestMCPCLI_ListAndShow(t *testing.T) {
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
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	config := []byte("mcp:\n  servers:\n    repo-inspector:\n      url: https://example.com/mcp\n      description: Repository inspection server\n")
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), config, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	listOutput := captureRunOutput(t, []string{"mcp", "list"})
	showOutput := captureRunOutput(t, []string{"mcp", "show", "repo-inspector"})

	if !strings.Contains(listOutput, "repo-inspector | https://example.com/mcp | Repository inspection server") {
		t.Fatalf("expected mcp list output, got %s", listOutput)
	}
	if !strings.Contains(showOutput, "Name: repo-inspector") {
		t.Fatalf("expected mcp show name, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "URL: https://example.com/mcp") {
		t.Fatalf("expected mcp show url, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Description: Repository inspection server") {
		t.Fatalf("expected mcp show description, got %s", showOutput)
	}
}

func TestRunScriptVerifier_CompileFailureBypassesExecution(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// A script that fails py_compile. Execution must be skipped.
	bad := "def broken(:\n    pass\n"
	path := "broken.py"
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var stdout bytes.Buffer
	// Inject a failing interpreter to make sure execution is NOT attempted.
	// (If execution were attempted after a compile failure, the test would
	// still observe a compile error, but we want to assert the bypass
	// structurally: we redirect stdout to detect any execution log.)
	previousStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = previousStdout }()
	verifyErr := runScriptVerifier(dir, path, "should not be checked at all")
	_ = w.Close()
	io.Copy(&stdout, r)
	if verifyErr == nil {
		t.Fatalf("expected compile failure, got nil\n%s", stdout.String())
	}
	combined := stdout.String()
	if !strings.Contains(combined, "Verifier: FAIL") {
		t.Fatalf("expected FAIL marker, got:\n%s", combined)
	}
	if strings.Contains(combined, "stdout did not match expected_output") {
		t.Fatalf("execution should have been bypassed, but stdout diff ran:\n%s", combined)
	}
}

func TestRunScriptVerifier_DiffExpectedOutput_PassOnMatch(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// A script that prints exactly the expected output.
	script := "print('Hello from test')\n"
	path := "hello.py"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var output bytes.Buffer
	// Capture stdout to inspect Verifier: line.
	previousStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = previousStdout }()
	verifyErr := runScriptVerifier(dir, path, "Hello from test")
	_ = w.Close()
	io.Copy(&output, r)
	if verifyErr != nil {
		t.Fatalf("expected verifier PASS, got error: %v\n%s", verifyErr, output.String())
	}
	if !strings.Contains(output.String(), "Verifier: PASS") {
		t.Fatalf("expected Verifier: PASS, got:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "matched expected_output") {
		t.Fatalf("expected strong-assertion PASS reason, got:\n%s", output.String())
	}
}

func TestRunScriptVerifier_DiffExpectedOutput_FailOnMismatchShowsExpected(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Deterministic "Hello from {name}" template is the canonical bug case
	// from report.md (TODO-03 success criterion #4).
	script := "print('Hello from multiplication_table')\n"
	path := "multiplication_table.py"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	expected := "1*1=1\n9x9=81\n"
	var output bytes.Buffer
	previousStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = previousStdout }()
	verifyErr := runScriptVerifier(dir, path, expected)
	_ = w.Close()
	io.Copy(&output, r)
	if verifyErr == nil {
		t.Fatalf("expected verifier FAIL on stdout mismatch, got nil\n%s", output.String())
	}
	combined := output.String()
	if !strings.Contains(combined, "Verifier: FAIL") {
		t.Fatalf("expected Verifier: FAIL, got:\n%s", combined)
	}
	// Success criterion #4: verifier output must contain "9x9" or "81" so the
	// human/agent can tell the script printed the wrong thing.
	if !strings.Contains(combined, "9x9=81") {
		t.Fatalf("expected verifier output to surface expected_output '9x9=81', got:\n%s", combined)
	}
	if !strings.Contains(combined, "Hello from multiplication_table") {
		t.Fatalf("expected verifier output to surface actual stdout, got:\n%s", combined)
	}
}

func TestRunScriptVerifier_FallbackToExitCodeAndStdout_Pass(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	script := "print('hi')\n"
	path := "hi.py"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var output bytes.Buffer
	previousStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = previousStdout }()
	verifyErr := runScriptVerifier(dir, path, "")
	_ = w.Close()
	io.Copy(&output, r)
	if verifyErr != nil {
		t.Fatalf("expected verifier PASS, got: %v\n%s", verifyErr, output.String())
	}
	if !strings.Contains(output.String(), "Verifier: PASS") {
		t.Fatalf("expected Verifier: PASS, got:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "no expected_output supplied") {
		t.Fatalf("expected weak-assertion reason, got:\n%s", output.String())
	}
}

func TestRunScriptVerifier_FallbackToExitCodeAndStdout_FailOnEmptyStdout(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Valid Python, no print, no error: exits 0 with empty stdout.
	script := "def main():\n    return None\n"
	path := "silent.py"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var output bytes.Buffer
	previousStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = previousStdout }()
	verifyErr := runScriptVerifier(dir, path, "")
	_ = w.Close()
	io.Copy(&output, r)
	if verifyErr == nil {
		t.Fatalf("expected verifier FAIL on empty stdout without expected_output, got nil\n%s", output.String())
	}
	if !strings.Contains(output.String(), "empty stdout") {
		t.Fatalf("expected empty-stdout reason, got:\n%s", output.String())
	}
}

func TestRunScriptVerifier_FallbackToExitCodeAndStdout_FailOnNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Exits with status 2.
	script := "import sys\nsys.exit(2)\n"
	path := "fatal.py"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var output bytes.Buffer
	previousStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = previousStdout }()
	verifyErr := runScriptVerifier(dir, path, "")
	_ = w.Close()
	io.Copy(&output, r)
	if verifyErr == nil {
		t.Fatalf("expected verifier FAIL on non-zero exit, got nil\n%s", output.String())
	}
	if !strings.Contains(output.String(), "non-zero status") {
		t.Fatalf("expected non-zero-status reason, got:\n%s", output.String())
	}
}

func TestE2E_ScriptVerifierRejectsHelloTemplate(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "hello_template.py")
	// Script that outputs the "Hello from" template — should fail verifier.
	if err := os.WriteFile(scriptPath, []byte("print('Hello from hello_template')\n"), 0o644); err != nil {
		t.Fatalf("write test script: %v", err)
	}
	err := runScriptExecutionCheck(tempDir, scriptPath, ".py", "")
	if err == nil {
		t.Fatal("expected Hello from template to be rejected by verifier")
	}
	if !strings.Contains(err.Error(), "Hello from") {
		t.Fatalf("expected Hello from error, got %v", err)
	}
}

func TestE2E_ScriptVerifierPassesMultiLineOutput(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "multi_line.py")
	if err := os.WriteFile(scriptPath, []byte("for i in range(1,10):\n for j in range(1,i+1):\n  print(f'{j}*{i}={i*j}', end=' ')\n print()\n"), 0o644); err != nil {
		t.Fatalf("write test script: %v", err)
	}
	err := runScriptExecutionCheck(tempDir, scriptPath, ".py", "")
	if err != nil {
		t.Fatalf("expected multi-line script to pass verifier, got %v", err)
	}
}
