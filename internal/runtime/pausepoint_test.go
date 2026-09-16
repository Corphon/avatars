package runtime

import "testing"

func TestPausePointIDStableForSameInputs(t *testing.T) {
	first := NewPausePoint(PausePointKindApprovalGate, "run-1", "task-1", "node-build", "action-1", "approval-1", "write", "file_write", "digest-1")
	second := NewPausePoint(PausePointKindApprovalGate, "run-1", "task-1", "node-build", "action-1", "approval-1", "write", "file_write", "digest-1")

	if first.ID == "" {
		t.Fatalf("expected pause point id, got %+v", first)
	}
	if first.ID != second.ID {
		t.Fatalf("expected stable pause point id, got %s and %s", first.ID, second.ID)
	}
}

func TestPausePointIDDifferentWhenDigestChanges(t *testing.T) {
	first := NewPausePoint(PausePointKindApprovalGate, "run-1", "task-1", "node-build", "action-1", "approval-1", "write", "file_write", "digest-1")
	second := NewPausePoint(PausePointKindApprovalGate, "run-1", "task-1", "node-build", "action-1", "approval-1", "write", "file_write", "digest-2")

	if first.ID == second.ID {
		t.Fatalf("expected different pause point IDs when digest changes, got %s", first.ID)
	}
}

func TestPausePointPayloadIncludesRuntimeCoordinates(t *testing.T) {
	point := NewPausePoint(PausePointKindApprovalGate, "run-1", "task-1", "node-build", "action-1", "approval-1", "write", "file_write", "digest-1")
	payload := point.Payload()

	assertPayloadValue(t, payload, "pause_point_id", point.ID)
	assertPayloadValue(t, payload, "pause_point_kind", "approval_gate")
	assertPayloadValue(t, payload, "origin_run_id", "run-1")
	assertPayloadValue(t, payload, "origin_task_id", "task-1")
	assertPayloadValue(t, payload, "node_id", "node-build")
	assertPayloadValue(t, payload, "action_id", "action-1")
	assertPayloadValue(t, payload, "approval_key", "approval-1")
	assertPayloadValue(t, payload, "tool", "write")
	assertPayloadValue(t, payload, "operation", "file_write")
	assertPayloadValue(t, payload, "digest", "digest-1")
}

func assertPayloadValue(t *testing.T, payload map[string]any, key string, expected string) {
	t.Helper()
	if got := payload[key]; got != expected {
		t.Fatalf("expected payload[%q] = %q, got %#v", key, expected, got)
	}
}
