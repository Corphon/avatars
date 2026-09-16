package runtime

import (
	"strings"
	"testing"
	"time"

	"avatars/internal/tools"
)

func TestSuspendedActionFrame_ValidatesResolvedToolCall(t *testing.T) {
	input := tools.WriteInput{Path: "notes.txt", Content: "blocked", WorkingDir: t.TempDir(), Intent: "persist notes"}
	frame := NewSuspendedActionFrame(toolCallEnvelope{
		runID:    "run-origin",
		taskID:   "task-1",
		avatarID: "avatar-builder",
		phase:    "executing",
	}, "action-1", "approval-key-1", "write", "file_write", input, "sessions/origin.jsonl", time.Now().UTC())

	if frame == nil {
		t.Fatal("expected suspended action frame")
	}
	if frame.InputDigest == "" {
		t.Fatalf("expected input digest, got %+v", frame)
	}
	if frame.PausePointID == "" {
		t.Fatalf("expected pause point id, got %+v", frame)
	}
	if err := frame.ValidateResolvedToolCall("write", "file_write", input); err != nil {
		t.Fatalf("expected matching tool call to validate, got %v", err)
	}
	if err := frame.ValidateResolvedToolCall("write", "file_write", tools.WriteInput{Path: "notes.txt", Content: "changed", WorkingDir: input.WorkingDir, Intent: "persist notes"}); err == nil {
		t.Fatal("expected changed replay input to fail digest validation")
	} else if !strings.Contains(err.Error(), "input digest mismatch") {
		t.Fatalf("expected input digest mismatch, got %v", err)
	}
	if err := frame.ValidateResolvedToolCall("patch", "file_patch", input); err == nil {
		t.Fatal("expected changed replay tool metadata to fail validation")
	}
}

func TestPendingApprovalRequest_CarriesSuspendedActionFrame(t *testing.T) {
	input := tools.WriteInput{Path: "notes.txt", Content: "blocked", WorkingDir: t.TempDir()}
	request, ok := buildPendingApprovalRequest("write", "file_write", input, PermissionModeDefault, "approval-key-1", time.Now().UTC())
	if !ok {
		t.Fatal("expected pending approval request")
	}
	request.SuspendedAction = NewSuspendedActionFrame(toolCallEnvelope{
		runID:  "run-origin",
		taskID: "task-1",
		phase:  "executing",
	}, "action-1", request.ApprovalKey, request.Tool, request.Operation, input, "sessions/origin.jsonl", request.RequestedAt)

	toolName, operation, resolvedInput, err := request.ResolveToolCall()
	if err != nil {
		t.Fatalf("resolve tool call failed: %v", err)
	}
	if err := request.ValidateSuspendedAction(toolName, operation, resolvedInput); err != nil {
		t.Fatalf("expected suspended action validation to pass, got %v", err)
	}
	request.Write.Content = "tampered"
	toolName, operation, resolvedInput, err = request.ResolveToolCall()
	if err != nil {
		t.Fatalf("resolve tampered tool call failed: %v", err)
	}
	if err := request.ValidateSuspendedAction(toolName, operation, resolvedInput); err == nil {
		t.Fatal("expected tampered pending approval input to fail validation")
	}
}
