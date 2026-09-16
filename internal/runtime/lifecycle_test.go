package runtime

import "testing"

func TestRunLifecycleTransition_AllowsApprovalContinuation(t *testing.T) {
	state := NewRunLifecycle("run-origin", "task-1")
	state.MarkAwaitingApproval(RunApprovalBoundary{
		ApprovalKey:       "approval-key-1",
		RequestedActionID: "approval-key-1",
		BoundaryEventID:   "evt-awaiting",
		TranscriptPath:    "sessions/origin.jsonl",
	})

	if err := state.MarkApprovalContinued(RunContinuationBoundary{
		ContinuationRunID:      "run-origin-continued-1",
		ContinuationTaskID:     "task-1",
		ContinuationTranscript: "sessions/continued.jsonl",
		Status:                 RunLifecycleStatusCompleted,
		Summary:                "Continued approved write/file_write.",
	}); err != nil {
		t.Fatalf("expected approval continuation to succeed, got %v", err)
	}

	if state.Status != RunLifecycleStatusContinued {
		t.Fatalf("expected continued status, got %q", state.Status)
	}
	if state.Approval == nil || state.Approval.ContinuationRunID != "run-origin-continued-1" {
		t.Fatalf("expected continuation lineage on approval boundary, got %+v", state.Approval)
	}
}

func TestRunLifecycleTransition_RejectsContinuationWithoutAwaitingApproval(t *testing.T) {
	state := NewRunLifecycle("run-origin", "task-1")
	err := state.MarkApprovalContinued(RunContinuationBoundary{ContinuationRunID: "run-continued"})
	if err == nil {
		t.Fatal("expected continuation without awaiting approval to fail")
	}
}

func TestRunLifecyclePayloads_IncludeOriginAndContinuationLineage(t *testing.T) {
	state := NewRunLifecycle("run-origin", "task-1")
	state.MarkAwaitingApproval(RunApprovalBoundary{
		ApprovalKey:       "approval-key-1",
		RequestedActionID: "action-1",
		BoundaryEventID:   "evt-awaiting",
		TranscriptPath:    "sessions/origin.jsonl",
	})
	if err := state.MarkApprovalContinued(RunContinuationBoundary{
		ContinuationRunID:      "run-origin-continued-1",
		ContinuationTaskID:     "task-1",
		ContinuationTranscript: "sessions/continued.jsonl",
		Status:                 RunLifecycleStatusCompleted,
		Summary:                "Continued approved write/file_write.",
	}); err != nil {
		t.Fatalf("mark approval continued failed: %v", err)
	}

	payload := state.Payload()
	if payload["status"] != RunLifecycleStatusContinued {
		t.Fatalf("expected lifecycle payload status continued, got %+v", payload)
	}
	if payload["approval_key"] != "approval-key-1" {
		t.Fatalf("expected lifecycle payload approval key, got %+v", payload)
	}
	if payload["origin_run_id"] != "run-origin" || payload["origin_task_id"] != "task-1" {
		t.Fatalf("expected origin lineage in payload, got %+v", payload)
	}
	if payload["continuation_run_id"] != "run-origin-continued-1" {
		t.Fatalf("expected continuation run id in payload, got %+v", payload)
	}
	if payload["continuation_transcript"] != "sessions/continued.jsonl" {
		t.Fatalf("expected continuation transcript in payload, got %+v", payload)
	}
}
