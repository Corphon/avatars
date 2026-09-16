package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SuspendedActionFrame struct {
	RunID            string                `json:"run_id,omitempty"`
	TaskID           string                `json:"task_id,omitempty"`
	AvatarID         string                `json:"avatar_id,omitempty"`
	Phase            string                `json:"phase,omitempty"`
	ActionID         string                `json:"action_id,omitempty"`
	ApprovalKey      string                `json:"approval_key,omitempty"`
	Tool             string                `json:"tool,omitempty"`
	Operation        string                `json:"operation,omitempty"`
	InputDigest      string                `json:"input_digest,omitempty"`
	PausePointID     string                `json:"pause_point_id,omitempty"`
	RequestedAt      time.Time             `json:"requested_at,omitempty"`
	OriginTranscript string                `json:"origin_transcript,omitempty"`
	BoundaryEventID  string                `json:"boundary_event_id,omitempty"`
	// R1: Workflow DAG state at suspension time. Restored on resume so
	// the scheduler can continue from the paused node.
	WorkflowSnapshot *WorkflowStateSnapshot `json:"workflow_snapshot,omitempty"`
}

func NewSuspendedActionFrame(envelope toolCallEnvelope, actionID string, approvalKey string, toolName string, operation string, input any, originTranscript string, requestedAt time.Time) *SuspendedActionFrame {
	toolName = strings.TrimSpace(toolName)
	operation = strings.TrimSpace(operation)
	if toolName == "" || operation == "" {
		return nil
	}
	if requestedAt.IsZero() {
		requestedAt = time.Now().UTC()
	}
	frame := &SuspendedActionFrame{
		RunID:            strings.TrimSpace(envelope.runID),
		TaskID:           strings.TrimSpace(envelope.taskID),
		AvatarID:         strings.TrimSpace(envelope.avatarID),
		Phase:            strings.TrimSpace(envelope.phase),
		ActionID:         strings.TrimSpace(actionID),
		ApprovalKey:      strings.TrimSpace(approvalKey),
		Tool:             toolName,
		Operation:        operation,
		InputDigest:      SuspendedActionDigest(toolName, operation, input),
		RequestedAt:      requestedAt,
		OriginTranscript: strings.TrimSpace(originTranscript),
	}
	frame.PausePointID = suspendedActionPausePointID(*frame)
	return frame
}

func SuspendedActionDigest(toolName string, operation string, input any) string {
	normalized := map[string]any{
		"tool":      strings.TrimSpace(toolName),
		"operation": strings.TrimSpace(operation),
		"input":     normalizeSuspendedActionInput(input),
	}
	content, err := json.Marshal(normalized)
	if err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%#v", strings.TrimSpace(toolName), strings.TrimSpace(operation), input)))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (f *SuspendedActionFrame) ValidateResolvedToolCall(toolName string, operation string, input any) error {
	if f == nil {
		return nil
	}
	toolName = strings.TrimSpace(toolName)
	operation = strings.TrimSpace(operation)
	if !strings.EqualFold(strings.TrimSpace(f.Tool), toolName) || !strings.EqualFold(strings.TrimSpace(f.Operation), operation) {
		return fmt.Errorf("suspended action metadata mismatch: expected %s/%s, got %s/%s", strings.TrimSpace(f.Tool), strings.TrimSpace(f.Operation), toolName, operation)
	}
	expectedDigest := strings.TrimSpace(f.InputDigest)
	actualDigest := SuspendedActionDigest(toolName, operation, input)
	if expectedDigest != "" && actualDigest != expectedDigest {
		return fmt.Errorf("suspended action input digest mismatch: expected %s, got %s", expectedDigest, actualDigest)
	}
	return nil
}

func normalizeSuspendedActionInput(input any) any {
	content, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprintf("%#v", input)
	}
	var normalized any
	if err := json.Unmarshal(content, &normalized); err != nil {
		return string(content)
	}
	return normalized
}

func suspendedActionPausePointID(frame SuspendedActionFrame) string {
	return approvalGatePausePointID(
		frame.RunID,
		frame.TaskID,
		frame.ActionID,
		frame.ApprovalKey,
		frame.Tool,
		frame.Operation,
		frame.InputDigest,
	)
}
