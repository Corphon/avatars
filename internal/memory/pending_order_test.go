package memory

import (
	"testing"
	"time"
)

func TestPendingToolApproval_TwoPassClearsGranted(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "tool_approval_required", Details: []string{"approval_key: key-a"}, UpdatedAt: time.Now().Add(-time.Minute)},
		{Cause: "tool_approval_granted", Details: []string{"approval_key: key-a"}, UpdatedAt: time.Now()},
	}
	if p := PendingToolApprovalEvaluations(records); len(p) != 0 {
		t.Fatalf("oldest-first still pending: %+v", p)
	}
	records2 := []EvaluationRecord{
		{Cause: "tool_approval_granted", Details: []string{"approval_key: key-a"}, UpdatedAt: time.Now()},
		{Cause: "tool_approval_required", Details: []string{"approval_key: key-a"}, UpdatedAt: time.Now().Add(-time.Minute)},
	}
	if p := PendingToolApprovalEvaluations(records2); len(p) != 0 {
		t.Fatalf("newest-first still pending: %+v", p)
	}
	snap := Snapshot{EvaluationRecords: []EvaluationRecord{
		{Cause: "run_lifecycle_updated", Details: []string{"status: completed"}, UpdatedAt: time.Now()},
		{Cause: "tool_approval_granted", Details: []string{"approval_key: key-a"}, UpdatedAt: time.Now()},
		{Cause: "tool_approval_required", Details: []string{"approval_key: key-a"}, UpdatedAt: time.Now().Add(-time.Minute)},
	}}
	if got := TaskWorkspaceStatus("stable", snap); got != "completed" {
		t.Fatalf("got %q want completed", got)
	}
}
