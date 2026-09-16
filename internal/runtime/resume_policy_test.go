package runtime

import (
	"strings"
	"testing"
	"time"

	"avatars/internal/tools"
)

func TestResumePolicy_CompletedAttemptIsNotRetryable(t *testing.T) {
	request := resumePolicyRequestWithStatus("completed")

	decision := (ResumePolicy{}).CanStartAttempt(request)
	if decision.Allowed || decision.Retryable || decision.Reason != "already_completed" {
		t.Fatalf("expected completed attempt to be non-retryable, got %+v", decision)
	}
}

func TestResumePolicy_FailedAttemptIsRetryableByDefault(t *testing.T) {
	request := resumePolicyRequestWithStatus("failed")

	decision := (ResumePolicy{}).CanStartAttempt(request)
	if !decision.Allowed || !decision.Retryable || decision.Reason != "previous_attempt_failed" {
		t.Fatalf("expected failed attempt to be retryable, got %+v", decision)
	}
}

func TestResumePolicy_DigestMismatchIsNotRetryable(t *testing.T) {
	input := tools.WriteInput{Path: "notes.txt", Content: "approved", WorkingDir: t.TempDir()}
	request, ok := buildPendingApprovalRequest("write", "file_write", input, PermissionModeDefault, "approval-key-1", time.Now().UTC())
	if !ok {
		t.Fatal("expected pending approval request")
	}
	request.SuspendedAction = NewSuspendedActionFrame(toolCallEnvelope{runID: "run-1", taskID: "task-1", phase: "executing"}, "action-1", request.ApprovalKey, request.Tool, request.Operation, input, "sessions/origin.jsonl", request.RequestedAt)
	request.Continuation = &PendingApprovalContinuation{
		RunID:               "run-1",
		TaskID:              "task-1",
		PausePointID:        request.PausePointID(),
		ResumeAttemptID:     "resume-1",
		ResumeAttemptStatus: "failed",
	}
	validationErr := request.ValidateSuspendedAction("write", "file_write", tools.WriteInput{Path: "notes.txt", Content: "tampered", WorkingDir: input.WorkingDir})
	if validationErr == nil {
		t.Fatal("expected digest mismatch validation error")
	}

	decision := (ResumePolicy{}).DenyValidationFailure(request, validationErr)
	if decision.Allowed || decision.Retryable || decision.Reason != "suspended_action_mismatch" {
		t.Fatalf("expected digest mismatch to be non-retryable, got %+v", decision)
	}
	if err := decision.Err(); err == nil || !strings.Contains(err.Error(), "suspended_action_mismatch") {
		t.Fatalf("expected explicit policy error, got %v", err)
	}
}

func TestResumePolicy_SkippedAttemptReusesCompletedLineage(t *testing.T) {
	request := resumePolicyRequestWithStatus("skipped")

	decision := (ResumePolicy{}).CanStartAttempt(request)
	if decision.Allowed || decision.Retryable || decision.Reason != "already_skipped" {
		t.Fatalf("expected skipped attempt to be non-retryable, got %+v", decision)
	}
}

func TestResumePolicy_VerifierRemediationAllowsSameTargets(t *testing.T) {
	point := NewPausePoint(PausePointKindVerifierRemediation, "run-1", "task-1", "", "verifier:tool_file_edit", "", "write", "verifier_remediation", "digest-1")

	decision := (ResumePolicy{}).CanRetryVerifierRemediation(point, []string{"note.txt"}, []string{"note.txt"}, false)
	if !decision.Allowed || !decision.Retryable || decision.Reason != "verifier_remediation_retryable" {
		t.Fatalf("expected verifier remediation to allow same targets, got %+v", decision)
	}
}

func TestResumePolicy_VerifierRemediationRejectsChangedTargetsWithoutExplicitUpdate(t *testing.T) {
	point := NewPausePoint(PausePointKindVerifierRemediation, "run-1", "task-1", "", "verifier:tool_file_edit", "", "write", "verifier_remediation", "digest-1")

	decision := (ResumePolicy{}).CanRetryVerifierRemediation(point, []string{"note.txt"}, []string{"other.txt"}, false)
	if decision.Allowed || decision.Retryable || decision.Reason != "verifier_targets_changed" {
		t.Fatalf("expected changed verifier targets to be non-retryable, got %+v", decision)
	}
}

func TestResumePolicy_VerifierRemediationDistinguishesApprovalGate(t *testing.T) {
	point := NewPausePoint(PausePointKindApprovalGate, "run-1", "task-1", "", "action-1", "approval-key-1", "write", "file_write", "digest-1")

	decision := (ResumePolicy{}).CanRetryVerifierRemediation(point, []string{"note.txt"}, []string{"note.txt"}, false)
	if decision.Allowed || decision.Retryable || decision.Reason != "pause_point_not_verifier_remediation" {
		t.Fatalf("expected approval gate to be rejected by verifier remediation policy, got %+v", decision)
	}
}

func TestResumePolicy_ToolFailureTransientIsRetryable(t *testing.T) {
	decision := (ResumePolicy{}).CanRetryToolFailure("write", "file_write", ToolFailureRetryContext{
		AttemptStatus: "failed",
		FailureKind:   "tool_execution_failed",
	})
	if !decision.Allowed || !decision.Retryable || decision.Reason != "tool_failure_retryable" {
		t.Fatalf("expected transient tool failure to be retryable, got %+v", decision)
	}
}

func TestResumePolicy_ToolFailurePermissionDeniedRequiresContextChange(t *testing.T) {
	decision := (ResumePolicy{}).CanRetryToolFailure("write", "file_write", ToolFailureRetryContext{
		AttemptStatus:    "failed",
		FailureKind:      "tool_permission_denied",
		PermissionDenied: true,
	})
	if decision.Allowed || decision.Retryable || decision.Reason != "tool_failure_permission_gated" {
		t.Fatalf("expected permission denied tool failure to be blocked without context change, got %+v", decision)
	}
}

func TestResumePolicy_ToolFailurePermissionDeniedAllowsChangedContext(t *testing.T) {
	decision := (ResumePolicy{}).CanRetryToolFailure("write", "file_write", ToolFailureRetryContext{
		AttemptStatus:            "failed",
		FailureKind:              "tool_permission_denied",
		PermissionDenied:         true,
		PermissionContextChanged: true,
	})
	if !decision.Allowed || !decision.Retryable || decision.Reason != "tool_failure_permission_gated" {
		t.Fatalf("expected permission denied tool failure to allow retry after context change, got %+v", decision)
	}
}

func TestResumePolicy_ToolFailureCompletedAttemptIsNotRetryable(t *testing.T) {
	decision := (ResumePolicy{}).CanRetryToolFailure("write", "file_write", ToolFailureRetryContext{
		AttemptStatus: "completed",
		FailureKind:   "tool_execution_failed",
	})
	if decision.Allowed || decision.Retryable || decision.Reason != "tool_failure_attempt_completed" {
		t.Fatalf("expected completed tool failure attempt to be non-retryable, got %+v", decision)
	}
}

func TestResumePolicy_ToolFailureUnavailableToolIsNotRetryable(t *testing.T) {
	decision := (ResumePolicy{}).CanRetryToolFailure("missing", "file_read", ToolFailureRetryContext{
		AttemptStatus: "failed",
		FailureKind:   "tool_not_registered",
	})
	if decision.Allowed || decision.Retryable || decision.Reason != "tool_failure_unavailable" {
		t.Fatalf("expected unavailable tool failure to be non-retryable, got %+v", decision)
	}
}

func resumePolicyRequestWithStatus(status string) PendingApprovalRequest {
	request := PendingApprovalRequest{
		ApprovalKey: "approval-key-1",
		Tool:        "write",
		Operation:   "file_write",
		Continuation: &PendingApprovalContinuation{
			RunID:               "run-1",
			TaskID:              "task-1",
			PausePointID:        "pause-1",
			ResumeAttemptID:     "resume-1",
			ResumeAttemptStatus: status,
			ContinuationStatus:  status,
		},
	}
	return request
}
