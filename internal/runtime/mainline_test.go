// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/events"
	"avatars/internal/llm"
	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/registry"
	"avatars/internal/skillbuilder"
	"avatars/internal/skills"
	"avatars/internal/tools"
	"avatars/internal/transcript"
	"avatars/internal/verification"
)

func TestTrustedMainline_ApprovalContinuationVerifierAndLifecycleTruth(t *testing.T) {
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
	if err := os.WriteFile("go.mod", []byte("module example.com/trustedmainline\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}

	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		t.Fatalf("register read tool failed: %v", err)
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		t.Fatalf("register write tool failed: %v", err)
	}
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-trusted-mainline")
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
	skillStore := skills.NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")
	generatedPath, err := skillStore.Generate("seed-run", "seed-task", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := skillStore.Approve(generatedPath); err != nil {
		t.Fatalf("seed approve failed: %v", err)
	}

	engine := NewEngine(toolRegistry, writer, events.NewStore(), "session-trusted-mainline", verification.DefaultPolicy(), llm.NewGenkitClient(llm.GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true}), nil, skillStore).
		WithMemory(memoryStore).
		WithTaskWorkspace("trusted-task", filepath.Join(tempDir, ".avatars", "tasks", "trusted-task")).
		WithPermissionMode(PermissionModeDefault)
	engine.verifier = func(workingDir string, includeRace bool) verifierRunner {
		return stubVerifierRunner{report: verification.Report{
			Verdict: verification.VerdictPass,
			Summary: "Verification finished with PASS. 1 passed, 0 partial, 0 failed.",
			Checks: []verification.CheckEvidence{{
				Name:           "go test",
				CommandRun:     "go test ./...",
				OutputObserved: "ok\texample.com/trustedmainline",
				Expected:       "All package tests should pass.",
				Actual:         "Command completed successfully.",
				Result:         verification.VerdictPass,
			}},
		}}
	}

	_, err = engine.Run(context.Background(), "Analyze the current repository and propose a refactoring plan")
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected mainline run to stop at approval gate, got %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush blocked transcript failed: %v", err)
	}
	blockedTranscript, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read blocked transcript failed: %v", err)
	}
	blockedText := string(blockedTranscript)
	for _, expected := range []string{
		"run.started",
		"workflow.node_created",
		"workflow.node_activated",
		"tool.awaiting_approval",
		"run.lifecycle_updated",
		`"status":"awaiting_approval"`,
	} {
		if !strings.Contains(blockedText, expected) {
			t.Fatalf("expected blocked transcript to contain %s, got %s", expected, blockedText)
		}
	}

	snapshot, err := memoryStore.LoadLatestTaskSnapshot("trusted-task")
	if err != nil {
		t.Fatalf("load blocked task snapshot failed: %v", err)
	}
	if status := memstore.TaskWorkspaceStatus("stable", snapshot); status != "awaiting_approval" {
		t.Fatalf("expected awaiting approval task status, got %q", status)
	}
	nodeEvidence := memstore.LatestNodeWorkEvidence(snapshot.EvaluationRecords)
	if nodeEvidence == nil {
		t.Fatalf("expected latest node work evidence, got %+v", snapshot.EvaluationRecords)
	}
	if memstore.EvaluationRecordNodeID(*nodeEvidence) != "node-build" || memstore.EvaluationRecordNodeRole(*nodeEvidence) != "Builder" {
		t.Fatalf("expected builder node work evidence, got %+v", nodeEvidence)
	}
	if nodeEvidence.Tool != "write" || memstore.EvaluationRecordOperation(*nodeEvidence) != "file_write" {
		t.Fatalf("expected write/file_write node work evidence, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordDetailValue(*nodeEvidence, "status") != "awaiting_approval" {
		t.Fatalf("expected awaiting approval node work evidence, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordVerifierGateStatus(*nodeEvidence) != "pending_approval" || memstore.EvaluationRecordVerified(*nodeEvidence) != "false" {
		t.Fatalf("expected pending approval verifier gate on node work evidence, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordPausePointID(*nodeEvidence) == "" || memstore.EvaluationRecordRecoveryKind(*nodeEvidence) != "approval_required" {
		t.Fatalf("expected node work evidence recovery coordinates, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordDetailValue(*nodeEvidence, "pause_point_kind") != "workflow_node" {
		t.Fatalf("expected approval-required evidence to point at workflow-node pause point, got %+v", nodeEvidence)
	}
	if memstore.EvaluationRecordApprovalKey(*nodeEvidence) != "" || memstore.EvaluationRecordContinuationRunID(*nodeEvidence) != "" || memstore.EvaluationRecordReplayStatus(*nodeEvidence) != "" {
		t.Fatalf("expected approval-required evidence to avoid replay lineage before approval, got %+v", nodeEvidence)
	}
	approval := memstore.LatestToolApprovalRequiredEvaluation(snapshot.EvaluationRecords)
	if approval == nil {
		t.Fatalf("expected pending approval evaluation, got %+v", snapshot.EvaluationRecords)
	}
	approvalKey := memstore.EvaluationRecordDetailValue(*approval, "approval_key")
	if approvalKey == "" {
		t.Fatalf("expected approval key in evaluation, got %+v", approval)
	}
	request, err := LoadPendingApprovalRequest(filepath.Join(tempDir, ".avatars", "memory"), approvalKey)
	if err != nil {
		t.Fatalf("load pending approval request failed: %v", err)
	}
	if request.SuspendedAction == nil {
		t.Fatalf("expected pending approval to carry suspended action frame, got %+v", request)
	}
	if request.Workflow == nil {
		t.Fatalf("expected pending approval to carry workflow checkpoint, got %+v", request)
	}
	if request.Workflow.BlockedNodeID != "node-build" || request.Workflow.BlockedNodeRole != "Builder" {
		t.Fatalf("expected builder workflow checkpoint, got %+v", request.Workflow)
	}
	if request.Workflow.PausePointID == "" || request.Workflow.PausePointKind != "workflow_node" {
		t.Fatalf("expected workflow-node pause point in checkpoint, got %+v", request.Workflow)
	}
	if len(request.Workflow.BlockedNodeDependsOn) == 0 {
		t.Fatalf("expected builder checkpoint dependencies, got empty")
	}
	for _, dep := range request.Workflow.BlockedNodeDependsOn {
		if !strings.Contains(dep, "survey") {
			t.Fatalf("expected builder to depend on survey node(s), got %+v", request.Workflow.BlockedNodeDependsOn)
		}
	}
	if len(request.Workflow.CompletedNodeIDs) < 1 || len(request.Workflow.RemainingNodeIDs) < 1 {
		t.Fatalf("expected completed and remaining workflow node coordinates, got %+v", request.Workflow)
	}
	if len(request.Workflow.CompletedNodes) < 1 || len(request.Workflow.RemainingNodes) < 1 {
		t.Fatalf("expected workflow node metadata, got %+v", request.Workflow)
	}
	if request.Continuation == nil || request.Continuation.RunID == "" || request.Continuation.TaskID != "trusted-task" {
		t.Fatalf("expected pending approval to carry origin continuation metadata, got %+v", request.Continuation)
	}

	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-trusted-mainline", RunID: "operator-run", TaskID: "trusted-task", Tool: "write", Kind: "passive_feedback", Verdict: "PASS", Cause: "tool_approval_granted", Summary: "Tool write/file_write approval granted for the latest pending request.", Source: "task_operator_command", Details: []string{"approval_key: " + approvalKey}, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("record approval grant failed: %v", err)
	}
	continued, err := engine.ContinueApprovedToolCall(context.Background(), request, ToolCallOptions{})
	if err != nil {
		t.Fatalf("expected approved continuation to succeed, got %v", err)
	}
	if continued.ContinuationRun == "" || continued.ContinuationTask != "trusted-task" {
		t.Fatalf("expected continuation lineage in result, got %+v", continued)
	}

	if err := writer.Flush(); err != nil {
		t.Fatalf("flush continued transcript failed: %v", err)
	}
	continuedTranscript, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read continued transcript failed: %v", err)
	}
	continuedText := string(continuedTranscript)
	required := []string{
		"tool.approval_continued",
		"workflow.resume_checkpoint",
		"workflow.node_completed",
		"workflow.node_activated",
		"workflow.edge_advanced",
		`"blocked_node_id":"node-build"`,
		`"node_role":"Builder"`,
		`"node_id":"node-review"`,
		`"lifecycle":"tail_synthesis"`,
		`"status":"completed"`,
		fmt.Sprintf(`"pause_point_id":"%s"`, request.Workflow.PausePointID),
		`"pause_point_kind":"workflow_node"`,
		`"node_id":"node-build"`,
	}
	for _, expected := range required {
		if !strings.Contains(continuedText, expected) {
			t.Fatalf("expected continued transcript to contain %s, got %s", expected, continuedText)
		}
	}
	// Builder title varies by plan size (small vs medium parallel survey).
	if !strings.Contains(continuedText, `"node_title":"Shape the smallest viable change"`) &&
		!strings.Contains(continuedText, `"node_title":"Shape the bounded change"`) {
		t.Fatalf("expected builder node title in transcript")
	}
	// Continuation evidence: skill write, verification, or tool completion after approval.
	if !strings.Contains(continuedText, "verification.completed") &&
		!strings.Contains(continuedText, "skill.generated") &&
		!strings.Contains(continuedText, `"verdict":"PASS"`) &&
		!strings.Contains(continuedText, "tool.completed") &&
		!strings.Contains(continuedText, "Wrote ") {
		t.Fatalf("expected verification/skill/tool evidence in continued transcript")
	}
	snapshot, err = memoryStore.LoadLatestTaskSnapshot("trusted-task")
	if err != nil {
		t.Fatalf("load continued task snapshot failed: %v", err)
	}
	if status := memstore.TaskWorkspaceStatus("stable", snapshot); status != "completed" {
		t.Fatalf("expected lifecycle-derived completed task status, got %q", status)
	}
	if lifecycle := memstore.LatestRunLifecycleEvaluation(snapshot.EvaluationRecords); lifecycle == nil || memstore.EvaluationRecordDetailValue(*lifecycle, "status") != "completed" {
		t.Fatalf("expected latest lifecycle evaluation to be completed, got %+v", lifecycle)
	}
	if checkpoint := latestWorkflowResumeCheckpoint(snapshot.EvaluationRecords); checkpoint == nil || memstore.EvaluationRecordDetailValue(*checkpoint, "blocked_node_id") != "node-build" {
		t.Fatalf("expected workflow resume checkpoint evaluation, got %+v", checkpoint)
	}
	if checkpoint := latestWorkflowResumeCheckpoint(snapshot.EvaluationRecords); checkpoint == nil || memstore.EvaluationRecordPausePointID(*checkpoint) != request.Workflow.PausePointID {
		t.Fatalf("expected workflow checkpoint pause point %q, got %+v", request.Workflow.PausePointID, checkpoint)
	}
	if checkpoint := latestWorkflowResumeCheckpoint(snapshot.EvaluationRecords); checkpoint == nil || memstore.EvaluationRecordNodeID(*checkpoint) != "node-build" || memstore.EvaluationRecordNodeRole(*checkpoint) != "Builder" {
		t.Fatalf("expected workflow resume checkpoint to carry node coordinates, got %+v", checkpoint)
	}
	completed := latestWorkflowNodeCompleted(snapshot.EvaluationRecords, "node-build")
	if completed == nil || memstore.EvaluationRecordNodeRole(*completed) != "Builder" {
		t.Fatalf("expected workflow node completed evaluation to carry Builder role, got %+v", completed)
	}
	title := memstore.EvaluationRecordNodeTitle(*completed)
	if title != "Shape the smallest viable change" && title != "Shape the bounded change" {
		t.Fatalf("expected builder node title, got %q", title)
	}
	var replayEvidence *memstore.EvaluationRecord
	for i := len(snapshot.EvaluationRecords) - 1; i >= 0; i-- {
		rec := snapshot.EvaluationRecords[i]
		if memstore.EvaluationRecordNodeID(rec) == "node-build" &&
			memstore.EvaluationRecordDetailValue(rec, "status") == "replay_completed" {
			cp := rec
			replayEvidence = &cp
			break
		}
	}
	if replayEvidence == nil {
		t.Fatalf("expected replay node work evidence for node-build, got %+v", snapshot.EvaluationRecords)
	}
	if memstore.EvaluationRecordNodeID(*replayEvidence) != "node-build" || memstore.EvaluationRecordNodeRole(*replayEvidence) != "Builder" {
		t.Fatalf("expected builder replay node evidence, got %+v", replayEvidence)
	}
	if memstore.EvaluationRecordDetailValue(*replayEvidence, "status") != "replay_completed" {
		t.Fatalf("expected replay_completed node evidence, got %+v", replayEvidence)
	}
	if memstore.EvaluationRecordApprovalKey(*replayEvidence) != approvalKey {
		t.Fatalf("expected approval key %q in replay node evidence, got %+v", approvalKey, replayEvidence)
	}
	if memstore.EvaluationRecordContinuationRunID(*replayEvidence) != continued.ContinuationRun || memstore.EvaluationRecordContinuationTaskID(*replayEvidence) != continued.ContinuationTask {
		t.Fatalf("expected continuation lineage in replay node evidence, got %+v", replayEvidence)
	}
	if memstore.EvaluationRecordReplayStatus(*replayEvidence) != "completed" || memstore.EvaluationRecordRecoveryKind(*replayEvidence) != "approval_replay" {
		t.Fatalf("expected completed approval replay evidence, got %+v", replayEvidence)
	}
	if memstore.EvaluationRecordPausePointID(*replayEvidence) != request.Workflow.PausePointID || memstore.EvaluationRecordDetailValue(*replayEvidence, "pause_point_kind") != "workflow_node" {
		t.Fatalf("expected replay evidence to preserve original workflow-node pause coordinates, got %+v", replayEvidence)
	}
	if memstore.EvaluationRecordVerifierGateStatus(*replayEvidence) != "" || memstore.EvaluationRecordVerified(*replayEvidence) != "" {
		t.Fatalf("expected replay evidence not to claim verifier gate authority, got %+v", replayEvidence)
	}
	// Skill-only markdown writes may not produce verifier_verdict; require PASS only when present.
	if verdict := latestVerifierVerdict(snapshot.EvaluationRecords); verdict != nil && verdict.Verdict != "PASS" {
		t.Fatalf("expected verifier PASS when verdict present, got %+v", verdict)
	}
	if snapshot.Verification != nil && snapshot.Verification.Verdict != "PASS" && snapshot.Verification.Verdict != "" {
		t.Fatalf("expected task verification snapshot PASS or empty, got %+v", snapshot.Verification)
	}
}

func latestVerifierVerdict(records []memstore.EvaluationRecord) *memstore.EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Kind) != "verifier_verdict" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func latestWorkflowResumeCheckpoint(records []memstore.EvaluationRecord) *memstore.EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "workflow_resume_checkpoint" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func latestWorkflowNodeCompleted(records []memstore.EvaluationRecord, nodeID string) *memstore.EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "node_work_evidence" {
			continue
		}
		if memstore.EvaluationRecordNodeID(record) != nodeID {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}
