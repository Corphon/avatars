package runtime

import (
	"fmt"
	"strings"
	"time"

	"avatars/internal/evolution"
	memstore "avatars/internal/memory"
)

func (e *Engine) emitStableBoundary(runID string, taskID string, avatarID string, status string, summary string, boundaryKind string, extra map[string]any) error {
	summaryPayload := clonePayload(extra)
	summaryPayload["status"] = status
	summaryPayload["summary"] = summary
	summaryPayload["boundary_kind"] = boundaryKind
	if runID != "" {
		summaryPayload["origin_run_id"] = runID
	}
	if taskID != "" {
		summaryPayload["origin_task_id"] = taskID
	}
	if err := e.emit(runID, taskID, avatarID, "reviewing", "memory.summary_updated", "memory", summaryPayload, nil); err != nil {
		return err
	}

	boundaryPayload := clonePayload(summaryPayload)
	boundaryPayload["stable"] = true
	if err := e.emit(runID, taskID, avatarID, "reviewing", "memory.compaction_boundary_written", "memory", boundaryPayload, nil); err != nil {
		return err
	}
	return e.persistHotSummary(memstore.SummaryRecord{
		SessionID:    e.sessionID,
		RunID:        runID,
		TaskID:       taskID,
		AvatarID:     avatarID,
		Scope:        memstore.ScopeStable,
		Status:       status,
		BoundaryKind: boundaryKind,
		Summary:      summary,
		UpdatedAt:    time.Now().UTC(),
	})
}

func (e *Engine) persistRunLifecycleEvaluation(runID string, taskID string, avatarID string, lifecycle RunLifecycle) error {
	if e == nil || e.memory == nil {
		return nil
	}
	payload := lifecycle.Payload()
	details := make([]string, 0, len(payload))
	for _, key := range []string{
		"status",
		"origin_run_id",
		"origin_task_id",
		"approval_key",
		"requested_action_id",
		"requested_event_id",
		"origin_boundary_id",
		"origin_boundary_type",
		"origin_transcript",
		"continuation_run_id",
		"continuation_task_id",
		"continuation_transcript",
		"continuation_status",
	} {
		if value, ok := payload[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				details = append(details, fmt.Sprintf("%s: %s", key, text))
			}
		}
	}
	summary := strings.TrimSpace(lifecycle.Summary)
	if summary == "" {
		summary = fmt.Sprintf("Run lifecycle updated to %s.", strings.TrimSpace(lifecycle.Status))
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     strings.TrimSpace(runID),
		TaskID:    strings.TrimSpace(taskID),
		AvatarID:  strings.TrimSpace(avatarID),
		Kind:      "runtime_lifecycle",
		Verdict:   "INFO",
		Cause:     "run_lifecycle_updated",
		Summary:   summary,
		Source:    "runtime",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	})
}

func (e *Engine) recordResumeAttemptEvaluation(request PendingApprovalRequest, toolName string, operation string, attempt ResumeAttempt) error {
	if e == nil || e.memory == nil {
		return nil
	}
	continuation := request.Continuation
	originRunID := ""
	originTaskID := ""
	avatarID := ""
	if continuation != nil {
		originRunID = strings.TrimSpace(continuation.RunID)
		originTaskID = strings.TrimSpace(continuation.TaskID)
		avatarID = strings.TrimSpace(continuation.AvatarID)
	}
	status := string(normalizeResumeAttemptStatus(attempt.Status))
	summary := strings.TrimSpace(attempt.Summary)
	if summary == "" {
		summary = fmt.Sprintf("Resume attempt %s for %s/%s.", status, strings.TrimSpace(toolName), strings.TrimSpace(operation))
	}
	details := []string{
		"approval_key: " + strings.TrimSpace(request.ApprovalKey),
		"operation: " + strings.TrimSpace(operation),
		"pause_point_id: " + strings.TrimSpace(attempt.PausePointID),
		"resume_attempt_id: " + strings.TrimSpace(attempt.ID),
		"resume_attempt_status: " + status,
		"origin_run_id: " + originRunID,
		"origin_task_id: " + originTaskID,
		"continuation_run_id: " + strings.TrimSpace(attempt.ContinuationRunID),
		"continuation_task_id: " + strings.TrimSpace(attempt.ContinuationTaskID),
	}
	if transcript := strings.TrimSpace(attempt.ContinuationTranscript); transcript != "" {
		details = append(details, "continuation_transcript: "+transcript)
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     originRunID,
		TaskID:    originTaskID,
		AvatarID:  avatarID,
		Tool:      strings.TrimSpace(toolName),
		Kind:      "runtime_lifecycle",
		Verdict:   "INFO",
		Cause:     "resume_attempt_recorded",
		Summary:   summary,
		Source:    "runtime",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	})
}

func (e *Engine) recordResumePolicyEvaluation(request PendingApprovalRequest, toolName string, operation string, decision ResumeDecision) error {
	if e == nil || e.memory == nil {
		return nil
	}
	continuation := request.Continuation
	originRunID := ""
	originTaskID := ""
	avatarID := ""
	if continuation != nil {
		originRunID = strings.TrimSpace(continuation.RunID)
		originTaskID = strings.TrimSpace(continuation.TaskID)
		avatarID = strings.TrimSpace(continuation.AvatarID)
	}
	summary := strings.TrimSpace(decision.Summary)
	if summary == "" {
		summary = "Resume policy evaluated the continuation request."
	}
	details := []string{
		"approval_key: " + strings.TrimSpace(request.ApprovalKey),
		"operation: " + strings.TrimSpace(operation),
		"pause_point_id: " + strings.TrimSpace(decision.PausePointID),
		"resume_attempt_id: " + strings.TrimSpace(decision.ResumeAttemptID),
		"retryable: " + fmt.Sprintf("%t", decision.Retryable),
		"allowed: " + fmt.Sprintf("%t", decision.Allowed),
		"reason: " + strings.TrimSpace(decision.Reason),
		"origin_run_id: " + originRunID,
		"origin_task_id: " + originTaskID,
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     originRunID,
		TaskID:    originTaskID,
		AvatarID:  avatarID,
		Tool:      strings.TrimSpace(toolName),
		Kind:      "runtime_lifecycle",
		Verdict:   "INFO",
		Cause:     "resume_policy_decision",
		Summary:   summary,
		Source:    "runtime",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	})
}

type nodeWorkEvidence struct {
	RunID                  string
	TaskID                 string
	AvatarID               string
	NodeID                 string
	NodeRole               string
	NodeTitle              string
	Tool                   string
	Operation              string
	Status                 string
	Summary                string
	ArtifactIDs            []string
	ExpectedTargets        []string
	ApprovalKey            string
	ContinuationRunID      string
	ContinuationTaskID     string
	ReplayStatus           string
	VerifierGateStatus     string
	VerifierVerdict        string
	VerificationReportPath string
	Verified               bool
	PausePointID           string
	PausePointKind         string
	RecoveryKind           string
}

func (e *Engine) persistNodeWorkEvidence(evidence nodeWorkEvidence) error {
	if e == nil || e.memory == nil {
		return nil
	}
	nodeID := strings.TrimSpace(evidence.NodeID)
	nodeRole := strings.TrimSpace(evidence.NodeRole)
	nodeTitle := strings.TrimSpace(evidence.NodeTitle)
	toolName := strings.TrimSpace(evidence.Tool)
	operation := strings.TrimSpace(evidence.Operation)
	status := strings.TrimSpace(evidence.Status)
	if nodeID == "" || toolName == "" || operation == "" {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	summary := strings.TrimSpace(evidence.Summary)
	if summary == "" {
		summary = fmt.Sprintf("Workflow node %s recorded %s/%s evidence.", nodeID, toolName, operation)
	}
	details := []string{
		"node_id: " + nodeID,
		"node_role: " + nodeRole,
		"tool: " + toolName,
		"operation: " + operation,
		"status: " + status,
		"origin_run_id: " + strings.TrimSpace(evidence.RunID),
		"origin_task_id: " + strings.TrimSpace(evidence.TaskID),
	}
	if nodeTitle != "" {
		details = append(details, "node_title: "+nodeTitle)
	}
	if len(evidence.ArtifactIDs) > 0 {
		details = append(details, "artifact_ids: "+strings.Join(cleanWorkflowIDs(evidence.ArtifactIDs), ", "))
	}
	if len(evidence.ExpectedTargets) > 0 {
		details = append(details, "expected_targets: "+strings.Join(cleanWorkflowIDs(evidence.ExpectedTargets), ", "))
	}
	if approvalKey := strings.TrimSpace(evidence.ApprovalKey); approvalKey != "" {
		details = append(details, "approval_key: "+approvalKey)
	}
	if continuationRunID := strings.TrimSpace(evidence.ContinuationRunID); continuationRunID != "" {
		details = append(details, "continuation_run_id: "+continuationRunID)
	}
	if continuationTaskID := strings.TrimSpace(evidence.ContinuationTaskID); continuationTaskID != "" {
		details = append(details, "continuation_task_id: "+continuationTaskID)
	}
	if replayStatus := strings.TrimSpace(evidence.ReplayStatus); replayStatus != "" {
		details = append(details, "replay_status: "+replayStatus)
	}
	if gateStatus := strings.TrimSpace(evidence.VerifierGateStatus); gateStatus != "" {
		details = append(details, "verifier_gate_status: "+gateStatus)
		details = append(details, "verified: "+fmt.Sprintf("%t", evidence.Verified))
	}
	if verdict := strings.TrimSpace(evidence.VerifierVerdict); verdict != "" {
		details = append(details, "verifier_verdict: "+verdict)
	}
	if reportPath := strings.TrimSpace(evidence.VerificationReportPath); reportPath != "" {
		details = append(details, "verification_report_path: "+reportPath)
	}
	if pausePointID := strings.TrimSpace(evidence.PausePointID); pausePointID != "" {
		details = append(details, "pause_point_id: "+pausePointID)
	}
	if pausePointKind := strings.TrimSpace(evidence.PausePointKind); pausePointKind != "" {
		details = append(details, "pause_point_kind: "+pausePointKind)
	}
	if recoveryKind := strings.TrimSpace(evidence.RecoveryKind); recoveryKind != "" {
		details = append(details, "recovery_kind: "+recoveryKind)
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID:  e.sessionID,
		RunID:      strings.TrimSpace(evidence.RunID),
		TaskID:     strings.TrimSpace(evidence.TaskID),
		AvatarID:   strings.TrimSpace(evidence.AvatarID),
		Tool:       toolName,
		Kind:       "workflow_feedback",
		Verdict:    "INFO",
		Cause:      "node_work_evidence",
		Summary:    summary,
		Source:     "runtime",
		ReportPath: strings.TrimSpace(evidence.VerificationReportPath),
		Details:    details,
		UpdatedAt:  time.Now().UTC(),
	})
}

func (e *Engine) emitTaskSummary(runID string, taskID string, avatarID string, summary string) error {
	if err := e.emit(runID, taskID, avatarID, "planning", "memory.summary_updated", "memory", map[string]any{
		"scope":   "task",
		"summary": summary,
	}, nil); err != nil {
		return err
	}
	return e.persistHotSummary(memstore.SummaryRecord{
		SessionID: e.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		AvatarID:  avatarID,
		Scope:     memstore.ScopeTask,
		Summary:   summary,
		UpdatedAt: time.Now().UTC(),
	})
}

func (e *Engine) emitAvatarSummary(runID string, taskID string, avatarID string, role string, summary string) error {
	if err := e.emit(runID, taskID, avatarID, "planning", "memory.summary_updated", "memory", map[string]any{
		"scope":   "avatar",
		"role":    role,
		"summary": summary,
	}, nil); err != nil {
		return err
	}
	return e.persistHotSummary(memstore.SummaryRecord{
		SessionID: e.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		AvatarID:  avatarID,
		Scope:     memstore.ScopeAvatar,
		Role:      role,
		Summary:   summary,
		UpdatedAt: time.Now().UTC(),
	})
}

func avatarSummaryText(role string, responsibility string, assignment string) string {
	base := fmt.Sprintf("%s avatar focus: %s.", role, responsibility)
	if assignment == "" {
		return base
	}
	return fmt.Sprintf("%s Current assignment: %s.", base, assignment)
}

func clonePayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func (e *Engine) persistHotSummary(record memstore.SummaryRecord) error {
	if e == nil || e.memory == nil {
		return nil
	}
	return e.memory.UpsertSummary(record)
}

func (e *Engine) persistWarmLesson(runID string, taskID string, taskSummary string, finalSummary string) error {
	if e == nil || e.memory == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	snapshot, err := e.memory.LoadLatestTaskSnapshot(taskID)
	if err != nil {
		return err
	}
	lesson, ok := deriveWarmLesson(e.sessionID, runID, taskID, taskSummary, finalSummary, snapshot)
	if !ok {
		return nil
	}
	return e.memory.RecordWarmLesson(lesson)
}

func (e *Engine) persistEvolutionCandidates(runID string, taskID string, taskSummary string, finalSummary string) error {
	if e == nil || e.memory == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	snapshot, err := e.memory.LoadLatestTaskSnapshot(taskID)
	if err != nil {
		return err
	}
	skillRates := e.skillSuccessRates()
	for _, candidate := range evolution.BuildCandidates(taskSummary, finalSummary, snapshot, skillRates) {
		record := memstore.EvolutionCandidateRecord{
			SessionID: e.sessionID,
			RunID:     runID,
			TaskID:    taskID,
			Kind:      candidate.Kind,
			Summary:   candidate.Summary,
			Source:    candidate.Source,
			Priority:  candidate.Priority,
			Status:    candidate.Status,
			UpdatedAt: time.Now().UTC(),
		}
		if err := e.memory.RecordEvolutionCandidate(record); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) persistSkillCandidateEvaluation(runID string, taskID string, avatarID string, name string, state string, path string, approvalRequired bool) error {
	if e == nil || e.memory == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		trimmedName = "Task Survey Skill"
	}
	trimmedState := strings.TrimSpace(state)
	if trimmedState == "" {
		trimmedState = "candidate_prepared"
	}
	details := []string{
		"skill_name: " + trimmedName,
		"skill_state: " + trimmedState,
		"approval_required: " + fmt.Sprintf("%t", approvalRequired),
	}
	if trimmedPath := strings.TrimSpace(path); trimmedPath != "" {
		details = append(details, "skill_path: "+trimmedPath)
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     strings.TrimSpace(runID),
		TaskID:    strings.TrimSpace(taskID),
		AvatarID:  strings.TrimSpace(avatarID),
		Tool:      "skillbuilder",
		Kind:      "skill_candidate",
		Verdict:   "INFO",
		Cause:     "skill_candidate_" + trimmedState,
		Summary:   fmt.Sprintf("Skill candidate %s reached state %s.", trimmedName, trimmedState),
		Source:    "skillbuilder",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	})
}

func (e *Engine) persistProjectLessons(taskID string) error {
	if e == nil || e.memory == nil || e.projectMemory == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	snapshot, err := e.memory.LoadLatestTaskSnapshot(taskID)
	if err != nil {
		return err
	}
	for _, lesson := range evolution.BuildProjectLessons(taskID, snapshot) {
		record := memstore.ProjectLessonRecord{
			SourceTaskID: lesson.SourceTaskID,
			Kind:         lesson.Kind,
			Summary:      lesson.Summary,
			Source:       lesson.Source,
			Confidence:   lesson.Confidence,
			UpdatedAt:    time.Now().UTC(),
		}
		if err := e.projectMemory.RecordProjectLesson(record); err != nil {
			return err
		}
	}
	return nil
}

func deriveWarmLesson(sessionID string, runID string, taskID string, taskSummary string, finalSummary string, snapshot memstore.Snapshot) (memstore.WarmLessonRecord, bool) {
	updatedAt := time.Now().UTC()
	if snapshot.Verification != nil {
		return memstore.WarmLessonRecord{
			SessionID:  sessionID,
			RunID:      runID,
			TaskID:     taskID,
			Kind:       "verification",
			Summary:    fmt.Sprintf("Before marking this task complete, repeat the verifier path for %s and expect %s.", snapshot.Verification.Tool, snapshot.Verification.Verdict),
			Source:     "verification",
			Confidence: "high",
			UpdatedAt:  updatedAt,
		}, true
	}
	if tool, summary, ok := latestToolRuntimeFeedback(snapshot.EvaluationRecords); ok {
		return memstore.WarmLessonRecord{
			SessionID:  sessionID,
			RunID:      runID,
			TaskID:     taskID,
			Kind:       "tool_runtime",
			Summary:    fmt.Sprintf("Before repeating %s, address this runtime tool failure: %s", tool, summary),
			Source:     "tool_runtime",
			Confidence: "high",
			UpdatedAt:  updatedAt,
		}, true
	}
	// Check evaluation records for intent_classification records.
	if record, ok := latestIntentClassification(snapshot.EvaluationRecords); ok {
		verdict := strings.ToLower(strings.TrimSpace(record.Verdict))
		correct := verdict == "pass" || verdict == "correct" || verdict == "true"
		status := "incorrect"
		if correct {
			status = "correct"
		}
		return memstore.WarmLessonRecord{
			SessionID:  sessionID,
			RunID:      runID,
			TaskID:     taskID,
			Kind:       "intent_classification",
			Summary:    fmt.Sprintf("Intent classification: %s — %s", strings.TrimSpace(record.Summary), status),
			Source:     "intent_router",
			Confidence: "medium",
			UpdatedAt:  updatedAt,
		}, true
	}
	// NF-4: Record intent classification for learning from past routing decisions.
	trimmedTaskSummary := strings.TrimSpace(taskSummary)
	if trimmedTaskSummary != "" {
		return memstore.WarmLessonRecord{
			SessionID:  sessionID,
			RunID:      runID,
			TaskID:     taskID,
			Kind:       "intent_classification",
			Summary:    fmt.Sprintf("Previous task was classified as: %s", trimmedTaskSummary),
			Source:     "intent_router",
			Confidence: "medium",
			UpdatedAt:  updatedAt,
		}, true
	}
	trimmedFinalSummary := strings.TrimSpace(finalSummary)
	if trimmedFinalSummary != "" {
		return memstore.WarmLessonRecord{
			SessionID:  sessionID,
			RunID:      runID,
			TaskID:     taskID,
			Kind:       "outcome",
			Summary:    trimmedFinalSummary,
			Source:     "run_summary",
			Confidence: "medium",
			UpdatedAt:  updatedAt,
		}, true
	}
	return memstore.WarmLessonRecord{}, false
}

func latestToolRuntimeFeedback(records []memstore.EvaluationRecord) (string, string, bool) {
	for _, record := range records {
		if strings.ToLower(strings.TrimSpace(record.Kind)) != "passive_feedback" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(record.Source)) != "tool_runtime" {
			continue
		}
		tool := strings.TrimSpace(record.Tool)
		if tool == "" {
			tool = "the same tool path"
		}
		summary := strings.TrimSpace(record.Summary)
		if summary == "" {
			continue
		}
		return tool, summary, true
	}
	return "", "", false
}

func latestIntentClassification(records []memstore.EvaluationRecord) (memstore.EvaluationRecord, bool) {
	for _, record := range records {
		if strings.ToLower(strings.TrimSpace(record.Kind)) != "intent_classification" {
			continue
		}
		if strings.TrimSpace(record.Summary) == "" {
			continue
		}
		return record, true
	}
	return memstore.EvaluationRecord{}, false
}

func warmLessonSummaries(lessons []memstore.WarmLesson) []string {
	if len(lessons) == 0 {
		return nil
	}
	summaries := make([]string, 0, len(lessons))
	for _, lesson := range lessons {
		if strings.TrimSpace(lesson.Summary) == "" {
			continue
		}
		summaries = append(summaries, lesson.Summary)
	}
	return summaries
}

func projectLessonSummaries(lessons []memstore.ProjectLesson) []string {
	if len(lessons) == 0 {
		return nil
	}
	summaries := make([]string, 0, len(lessons))
	for _, lesson := range lessons {
		if strings.TrimSpace(lesson.Summary) == "" {
			continue
		}
		if lesson.SupportCount > 1 {
			summaries = append(summaries, fmt.Sprintf("Supported by %d tasks: %s", lesson.SupportCount, lesson.Summary))
			continue
		}
		summaries = append(summaries, lesson.Summary)
	}
	return summaries
}
