package runtime

import (
	"fmt"
	"strings"
)

const (
	RunLifecycleStatusStarted          = "started"
	RunLifecycleStatusAwaitingApproval = "awaiting_approval"
	RunLifecycleStatusContinued        = "continued"
	RunLifecycleStatusCompleted        = "completed"
	RunLifecycleStatusFailed           = "failed"
)

type RunLifecycle struct {
	RunID    string
	TaskID   string
	Status   string
	Summary  string
	Approval *RunApprovalBoundary
}

type RunApprovalBoundary struct {
	ApprovalKey       string
	RequestedActionID string
	RequestedEventID  string
	BoundaryEventID   string
	BoundaryType      string
	TranscriptPath    string
	Status            string
	Summary           string

	ContinuationRunID      string
	ContinuationTaskID     string
	ContinuationTranscript string
	ContinuationStatus     string
	ContinuationSummary    string
}

type RunContinuationBoundary struct {
	ContinuationRunID      string
	ContinuationTaskID     string
	ContinuationTranscript string
	Status                 string
	Summary                string
}

func NewRunLifecycle(runID string, taskID string) RunLifecycle {
	return RunLifecycle{
		RunID:  strings.TrimSpace(runID),
		TaskID: strings.TrimSpace(taskID),
		Status: RunLifecycleStatusStarted,
	}
}

func (l *RunLifecycle) MarkAwaitingApproval(boundary RunApprovalBoundary) {
	if l == nil {
		return
	}
	boundary.ApprovalKey = strings.TrimSpace(boundary.ApprovalKey)
	boundary.RequestedActionID = strings.TrimSpace(boundary.RequestedActionID)
	boundary.RequestedEventID = strings.TrimSpace(boundary.RequestedEventID)
	boundary.BoundaryEventID = strings.TrimSpace(boundary.BoundaryEventID)
	boundary.BoundaryType = strings.TrimSpace(boundary.BoundaryType)
	boundary.TranscriptPath = strings.TrimSpace(boundary.TranscriptPath)
	boundary.Status = strings.TrimSpace(boundary.Status)
	if boundary.Status == "" {
		boundary.Status = RunLifecycleStatusAwaitingApproval
	}
	boundary.Summary = strings.TrimSpace(boundary.Summary)
	l.Status = RunLifecycleStatusAwaitingApproval
	l.Summary = boundary.Summary
	l.Approval = &boundary
}

func (l *RunLifecycle) MarkApprovalContinued(boundary RunContinuationBoundary) error {
	if l == nil {
		return fmt.Errorf("run lifecycle is required")
	}
	if l.Approval == nil || l.Status != RunLifecycleStatusAwaitingApproval {
		return fmt.Errorf("run %s is not awaiting approval", l.RunID)
	}
	boundary.ContinuationRunID = strings.TrimSpace(boundary.ContinuationRunID)
	boundary.ContinuationTaskID = strings.TrimSpace(boundary.ContinuationTaskID)
	boundary.ContinuationTranscript = strings.TrimSpace(boundary.ContinuationTranscript)
	boundary.Status = strings.TrimSpace(boundary.Status)
	if boundary.Status == "" {
		boundary.Status = RunLifecycleStatusCompleted
	}
	boundary.Summary = strings.TrimSpace(boundary.Summary)

	l.Status = RunLifecycleStatusContinued
	l.Summary = boundary.Summary
	l.Approval.ContinuationRunID = boundary.ContinuationRunID
	l.Approval.ContinuationTaskID = boundary.ContinuationTaskID
	l.Approval.ContinuationTranscript = boundary.ContinuationTranscript
	l.Approval.ContinuationStatus = boundary.Status
	l.Approval.ContinuationSummary = boundary.Summary
	return nil
}

func (l *RunLifecycle) MarkCompleted(summary string) {
	if l == nil {
		return
	}
	l.Status = RunLifecycleStatusCompleted
	l.Summary = strings.TrimSpace(summary)
}

func (l RunLifecycle) Payload() map[string]any {
	payload := map[string]any{
		"status":        strings.TrimSpace(l.Status),
		"origin_run_id": strings.TrimSpace(l.RunID),
	}
	if taskID := strings.TrimSpace(l.TaskID); taskID != "" {
		payload["origin_task_id"] = taskID
	}
	if summary := strings.TrimSpace(l.Summary); summary != "" {
		payload["summary"] = summary
	}
	if l.Approval == nil {
		return payload
	}
	addPayloadString(payload, "approval_key", l.Approval.ApprovalKey)
	addPayloadString(payload, "requested_action_id", l.Approval.RequestedActionID)
	addPayloadString(payload, "requested_event_id", l.Approval.RequestedEventID)
	addPayloadString(payload, "origin_boundary_id", l.Approval.BoundaryEventID)
	addPayloadString(payload, "origin_boundary_type", l.Approval.BoundaryType)
	addPayloadString(payload, "origin_transcript", l.Approval.TranscriptPath)
	addPayloadString(payload, "approval_boundary_status", l.Approval.Status)
	addPayloadString(payload, "approval_boundary_summary", l.Approval.Summary)
	addPayloadString(payload, "continuation_run_id", l.Approval.ContinuationRunID)
	addPayloadString(payload, "continuation_task_id", l.Approval.ContinuationTaskID)
	addPayloadString(payload, "continuation_transcript", l.Approval.ContinuationTranscript)
	addPayloadString(payload, "continuation_status", l.Approval.ContinuationStatus)
	addPayloadString(payload, "continuation_summary", l.Approval.ContinuationSummary)
	return payload
}

func addPayloadString(payload map[string]any, key string, value string) {
	if payload == nil {
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		payload[key] = trimmed
	}
}
