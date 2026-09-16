package evaluation

import (
	"errors"
	"strings"
	"testing"

	"avatars/internal/verification"
)

func TestBuildVerificationRecords_EmitsVerdictAndPassiveFeedback(t *testing.T) {
	records := BuildVerificationRecords("patch", ".avatars/memory/verifier/run-1.json", verification.Report{
		Verdict:  verification.VerdictPartial,
		Summary:  "Verification finished with PARTIAL.",
		Checks:   []verification.CheckEvidence{{Name: "go test -race ./...", Result: verification.VerdictPartial}},
		Warnings: []string{"CGO is not enabled"},
	})
	if len(records) != 2 {
		t.Fatalf("expected 2 evaluation records, got %d", len(records))
	}
	if records[0].Kind != "verifier_verdict" {
		t.Fatalf("expected first evaluation record kind verifier_verdict, got %q", records[0].Kind)
	}
	if records[0].Tool != "patch" {
		t.Fatalf("expected first evaluation record tool patch, got %q", records[0].Tool)
	}
	if records[0].Cause != "verification_partial" {
		t.Fatalf("expected first evaluation cause verification_partial, got %q", records[0].Cause)
	}
	if records[1].Kind != "passive_feedback" {
		t.Fatalf("expected second evaluation record kind passive_feedback, got %q", records[1].Kind)
	}
	if records[1].Tool != "patch" {
		t.Fatalf("expected second evaluation record tool patch, got %q", records[1].Tool)
	}
	if records[1].Summary != "CGO is not enabled" {
		t.Fatalf("expected warning summary to be preserved, got %q", records[1].Summary)
	}
	if len(records[1].Details) != 1 {
		t.Fatalf("expected passive feedback details length 1, got %d", len(records[1].Details))
	}
}

func TestBuildVerificationRecords_EmitsCheckPassiveFeedbackForNonPassResults(t *testing.T) {
	records := BuildVerificationRecords("patch", ".avatars/memory/verifier/run-2.json", verification.Report{
		Verdict: verification.VerdictFail,
		Summary: "Verification finished with FAIL.",
		Checks: []verification.CheckEvidence{{
			Name:           "go test ./...",
			CommandRun:     "go test ./...",
			OutputObserved: "# avatars/internal/runtime\ninternal/runtime/tools.go:12: undefined: missingSymbol",
			Expected:       "All package tests should pass.",
			Actual:         "Command exited with status 1.",
			Result:         verification.VerdictFail,
		}},
	})
	if len(records) != 2 {
		t.Fatalf("expected 2 evaluation records, got %d", len(records))
	}
	if records[1].Kind != "passive_feedback" {
		t.Fatalf("expected second evaluation record kind passive_feedback, got %q", records[1].Kind)
	}
	if records[1].Cause != "verification_check_failed" {
		t.Fatalf("expected check feedback cause verification_check_failed, got %q", records[1].Cause)
	}
	if records[1].Source != "verification_check" {
		t.Fatalf("expected check feedback source verification_check, got %q", records[1].Source)
	}
	if !strings.Contains(records[1].Summary, "go test ./...") {
		t.Fatalf("expected check feedback summary to mention check name, got %q", records[1].Summary)
	}
	if !strings.Contains(records[1].Summary, "undefined: missingSymbol") {
		t.Fatalf("expected check feedback summary to include normalized output, got %q", records[1].Summary)
	}
	if len(records[1].Details) != 4 {
		t.Fatalf("expected check feedback details length 4, got %d", len(records[1].Details))
	}
}

func TestBuildToolFailureRecord_EmitsRuntimePassiveFeedback(t *testing.T) {
	record := BuildToolFailureRecord("write", "file_write", map[string]any{
		"path":           "notes.txt",
		"working_dir":    ".",
		"content_length": 12,
	}, errors.New("path escapes sandbox root"), true)
	if record.Kind != "passive_feedback" {
		t.Fatalf("expected passive_feedback kind, got %q", record.Kind)
	}
	if record.Tool != "write" {
		t.Fatalf("expected tool write, got %q", record.Tool)
	}
	if record.Cause != "tool_execution_failed" {
		t.Fatalf("expected tool_execution_failed cause, got %q", record.Cause)
	}
	if record.Source != "tool_runtime" {
		t.Fatalf("expected tool_runtime source, got %q", record.Source)
	}
	if !strings.Contains(record.Summary, "Tool write/file_write failed") {
		t.Fatalf("expected runtime failure summary, got %q", record.Summary)
	}
	if len(record.Details) < 3 {
		t.Fatalf("expected stable runtime failure details, got %v", record.Details)
	}
	if record.Details[0] != "operation: file_write" {
		t.Fatalf("expected operation detail first, got %q", record.Details[0])
	}
}

func TestBuildToolDenialRecord_EmitsRuntimePermissionDenial(t *testing.T) {
	record := BuildToolDenialRecord("write", "file_write", map[string]any{
		"path":             "notes.txt",
		"expected_targets": []string{"notes.txt"},
		"policy": map[string]any{
			"decision_source": "tool_sandbox",
			"rationale":       "Tool sandbox denied write because the requested target escapes the current sandbox.",
		},
	}, errors.New("write tool target escapes sandbox: ../notes.txt"))
	if record.Kind != "passive_feedback" {
		t.Fatalf("expected passive_feedback kind, got %q", record.Kind)
	}
	if record.Cause != "tool_permission_denied" {
		t.Fatalf("expected tool_permission_denied cause, got %q", record.Cause)
	}
	if !strings.Contains(record.Summary, "Tool write/file_write was denied") {
		t.Fatalf("expected runtime denial summary, got %q", record.Summary)
	}
	if len(record.Details) < 3 {
		t.Fatalf("expected stable runtime denial details, got %v", record.Details)
	}
	if record.Details[1] != "decision_source: tool_sandbox" {
		t.Fatalf("expected decision source detail, got %v", record.Details)
	}
}

func TestBuildToolApprovalRequiredRecord_EmitsRollbackArtifactDetail(t *testing.T) {
	record := BuildToolApprovalRequiredRecord("write", "file_write", map[string]any{
		"path":              "notes.txt",
		"rollback_artifact": "memory/rollback/run-a-act-1-write-notes.txt.json",
	}, errors.New("approval required: wait"))
	if !strings.Contains(strings.Join(record.Details, "\n"), "rollback_artifact: memory/rollback/run-a-act-1-write-notes.txt.json") {
		t.Fatalf("expected rollback artifact detail, got %+v", record.Details)
	}
}

func TestBuildToolApprovalRequiredRecord_EmitsRuntimeApprovalRequirement(t *testing.T) {
	record := BuildToolApprovalRequiredRecord("write", "file_write", map[string]any{
		"path":             "notes.txt",
		"expected_targets": []string{"notes.txt"},
		"policy": map[string]any{
			"decision_source": "runtime_permission_mode",
			"permission_mode": "default",
			"rationale":       "Permission mode default requires approval before mutating write actions, and approval prompts are not implemented yet.",
		},
	}, errors.New("approval required: Permission mode default requires approval before mutating write actions, and approval prompts are not implemented yet."))
	if record.Kind != "passive_feedback" {
		t.Fatalf("expected passive_feedback kind, got %q", record.Kind)
	}
	if record.Cause != "tool_approval_required" {
		t.Fatalf("expected tool_approval_required cause, got %q", record.Cause)
	}
	if record.Verdict != "PARTIAL" {
		t.Fatalf("expected PARTIAL verdict, got %q", record.Verdict)
	}
	if !strings.Contains(record.Summary, "Tool write/file_write is awaiting approval") {
		t.Fatalf("expected approval summary, got %q", record.Summary)
	}
	if len(record.Details) < 4 {
		t.Fatalf("expected stable runtime approval details, got %v", record.Details)
	}
	if record.Details[1] != "decision_source: runtime_permission_mode" {
		t.Fatalf("expected decision source detail, got %v", record.Details)
	}
	if record.Details[3] != "permission_mode: default" {
		t.Fatalf("expected permission mode detail, got %v", record.Details)
	}
}

func TestBuildToolFailureRecord_EmitsUnavailableToolFeedback(t *testing.T) {
	record := BuildToolFailureRecord("missing", "file_read", map[string]any{"path": "process_record.md"}, errors.New("missing tool not registered"), false)
	if record.Cause != "tool_not_registered" {
		t.Fatalf("expected tool_not_registered cause, got %q", record.Cause)
	}
	if !strings.Contains(record.Summary, "Tool missing/file_read is unavailable") {
		t.Fatalf("expected unavailable tool summary, got %q", record.Summary)
	}
	if len(record.Details) != 2 {
		t.Fatalf("expected 2 details for unavailable tool, got %d", len(record.Details))
	}
}
