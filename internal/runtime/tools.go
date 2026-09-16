package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"avatars/internal/evaluation"
	memstore "avatars/internal/memory"
	"avatars/internal/runtime/approval"
	"avatars/internal/tools"
	"avatars/internal/verification"
)

var toolFailureResumePolicy = ResumePolicy{}

type toolCallEnvelope struct {
	runID    string
	taskID   string
	avatarID string
	phase    string
}

type verificationTarget struct {
	workingDir     string
	changedFiles   []string
	trigger        string
	commandSummary string
	includeRace    bool
	// P14-4a: Pre-existing build error baseline — passed to verifier to filter
	// pre-existing errors from results so only NEW errors cause FAIL.
	buildBaseline map[string]string
}

var shellRedirectionPattern = regexp.MustCompile(`(?:\d?>{1,2}|>&)\s*([^\s|&;]+)`)

func (e *Engine) verify(ctx context.Context, taskID string, workingDir string) (VerificationResult, error) {
	if e == nil || e.verifier == nil {
		return VerificationResult{}, fmt.Errorf("verifier is not configured")
	}
	runID := fmt.Sprintf("%s-verify-%d", e.sessionID, time.Now().UTC().UnixNano())
	resolvedTaskID := strings.TrimSpace(taskID)
	if resolvedTaskID == "" {
		resolvedTaskID = runID + "-task-verify"
	}
	resolvedWorkingDir := strings.TrimSpace(workingDir)
	if resolvedWorkingDir == "" {
		resolvedWorkingDir = "."
	}
	commandSummary := "avatars verify"
	if strings.TrimSpace(taskID) != "" {
		commandSummary = fmt.Sprintf("avatars verify --task %s", strings.TrimSpace(taskID))
	}
	if err := e.emit(runID, resolvedTaskID, "", "planning", "run.started", "runtime", map[string]any{"mode": "verify", "command": commandSummary}, nil); err != nil {
		return VerificationResult{}, err
	}
	if err := e.emit(runID, resolvedTaskID, "", "planning", "task.created", "runtime", map[string]any{"title": "Verification"}, nil); err != nil {
		return VerificationResult{}, err
	}
	result, verifyErr := e.runVerification(ctx, toolCallEnvelope{runID: runID, taskID: resolvedTaskID, phase: "reviewing"}, "verify", verificationTarget{
		workingDir:     resolvedWorkingDir,
		trigger:        "manual_verify",
		commandSummary: commandSummary,
	})
	status := "completed"
	summary := result.Report.Summary
	if verifyErr != nil {
		status = "failed"
		summary = verifyErr.Error()
	}
	if err := e.emit(runID, resolvedTaskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": status, "summary": summary}, nil); err != nil {
		return VerificationResult{}, err
	}
	if err := e.transcript.Flush(); err != nil {
		return VerificationResult{}, err
	}
	result.TranscriptPath = e.transcript.Path()
	if verifyErr != nil {
		return result, verifyErr
	}
	return result, nil
}

func (e *Engine) verifyWithRace(ctx context.Context, taskID string, workingDir string) (VerificationResult, error) {
	if e == nil || e.verifier == nil {
		return VerificationResult{}, fmt.Errorf("verifier is not configured")
	}
	runID := fmt.Sprintf("%s-race-verify-%d", e.sessionID, time.Now().UTC().UnixNano())
	resolvedTaskID := strings.TrimSpace(taskID)
	if resolvedTaskID == "" {
		resolvedTaskID = runID + "-task-verify"
	}
	resolvedWorkingDir := strings.TrimSpace(workingDir)
	if resolvedWorkingDir == "" {
		resolvedWorkingDir = "."
	}
	commandSummary := "avatars verify --race"
	if strings.TrimSpace(taskID) != "" {
		commandSummary = fmt.Sprintf("avatars verify --race --task %s", strings.TrimSpace(taskID))
	}
	if err := e.emit(runID, resolvedTaskID, "", "planning", "run.started", "runtime", map[string]any{"mode": "verify_race", "command": commandSummary}, nil); err != nil {
		return VerificationResult{}, err
	}
	if err := e.emit(runID, resolvedTaskID, "", "planning", "task.created", "runtime", map[string]any{"title": "Verification"}, nil); err != nil {
		return VerificationResult{}, err
	}
	result, verifyErr := e.runVerification(ctx, toolCallEnvelope{runID: runID, taskID: resolvedTaskID, phase: "reviewing"}, "verify", verificationTarget{
		workingDir:     resolvedWorkingDir,
		trigger:        "manual_verify_race",
		commandSummary: commandSummary,
		includeRace:    true,
	})
	status := "completed"
	summary := result.Report.Summary
	if verifyErr != nil {
		status = "failed"
		summary = verifyErr.Error()
	}
	if err := e.emit(runID, resolvedTaskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": status, "summary": summary}, nil); err != nil {
		return VerificationResult{}, err
	}
	if err := e.transcript.Flush(); err != nil {
		return VerificationResult{}, err
	}
	result.TranscriptPath = e.transcript.Path()
	if verifyErr != nil {
		return result, verifyErr
	}
	return result, nil
}

func (e *Engine) callTool(ctx context.Context, toolName string, operation string, input any) (ToolCallResult, error) {
	return e.callToolWithOptions(ctx, toolName, operation, input, ToolCallOptions{})
}

func (e *Engine) callToolWithOptions(ctx context.Context, toolName string, operation string, input any, options ToolCallOptions) (ToolCallResult, error) {
	runID, taskID, taskTitle, runStartedPayload := e.newToolRunContext(toolName, operation)

	if err := e.emit(runID, taskID, "", "planning", "run.started", "runtime", runStartedPayload, nil); err != nil {
		return ToolCallResult{}, err
	}
	if err := e.emit(runID, taskID, "", "planning", "task.created", "runtime", map[string]any{"title": taskTitle}, nil); err != nil {
		return ToolCallResult{}, err
	}

	result, err := e.invokeToolWithOptions(ctx, toolCallEnvelope{runID: runID, taskID: taskID, avatarID: "", phase: "executing"}, toolName, operation, input, toolInvokeOptions{skipVerification: options.SkipVerification})
	if err != nil {
		status := "failed"
		summary := err.Error()
		if !options.SkipVerification && isVerificationFailureForTool(toolName, input, err) {
			status = "needs_remediation"
			summary = fmt.Sprintf("Verifier blocked completion for %s/%s: %s", toolName, operation, err.Error())
		}
		if appendErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": status, "summary": summary}, nil); appendErr != nil {
			return ToolCallResult{}, appendErr
		}
		if appendErr := e.emitStableBoundary(runID, taskID, "", status, summary, "tool_terminal", map[string]any{"tool": toolName, "operation": operation}); appendErr != nil {
			return ToolCallResult{}, appendErr
		}
		if flushErr := e.transcript.Flush(); flushErr != nil {
			return ToolCallResult{}, flushErr
		}
		return ToolCallResult{TranscriptPath: e.transcript.Path(), Content: result.Content}, err
	}

	summary := summarizeToolResult(toolName, operation, input, result.Content)
	if err := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "completed", "summary": summary}, nil); err != nil {
		return ToolCallResult{}, err
	}
	if err := e.emitStableBoundary(runID, taskID, "", "completed", summary, "tool_terminal", map[string]any{"tool": toolName, "operation": operation}); err != nil {
		return ToolCallResult{}, err
	}
	if err := e.transcript.Flush(); err != nil {
		return ToolCallResult{}, err
	}

	return ToolCallResult{TranscriptPath: e.transcript.Path(), Content: result.Content}, nil
}

func isVerificationFailureForTool(toolName string, input any, err error) bool {
	if err == nil {
		return false
	}
	if _, shouldVerify := verificationTargetForTool(toolName, input); !shouldVerify {
		return false
	}
	lowered := strings.ToLower(err.Error())
	return strings.Contains(lowered, "verification failed")
}

func (e *Engine) newToolRunContext(toolName string, operation string) (string, string, string, map[string]any) {
	runID := fmt.Sprintf("%s-run-%d", e.sessionID, time.Now().UTC().UnixNano())
	taskID := runID + "-task-tool"
	taskTitle := fmt.Sprintf("%s %s", toolName, operation)
	runStartedPayload := map[string]any{"mode": "tool", "tool": toolName, "operation": operation}
	if e.taskID != "" {
		taskID = e.taskID
		runStartedPayload["task_workspace_id"] = e.taskID
		if e.taskRoot != "" {
			runStartedPayload["task_workspace_root"] = e.taskRoot
		}
	}
	return runID, taskID, taskTitle, runStartedPayload
}

func toolNodeEvidenceForAvatarID(avatarID string) map[string]any {
	id := strings.TrimSpace(avatarID)
	switch {
	case id == "avatar-planner":
		return map[string]any{"node_id": "node-plan", "node_role": "Planner", "node_title": "Decompose task and set checkpoints"}
	case strings.HasPrefix(id, "avatar-researcher"):
		return map[string]any{"node_id": "node-survey", "node_role": "Researcher", "node_title": "Survey current repository context"}
	case strings.HasPrefix(id, "avatar-builder"):
		return map[string]any{"node_id": "node-build", "node_role": "Builder", "node_title": "Shape the smallest viable change"}
	case id == "avatar-critic":
		return map[string]any{"node_id": "node-review", "node_role": "Critic", "node_title": "Review risks and challenge gaps"}
	case id == "avatar-synthesizer":
		return map[string]any{"node_id": "lifecycle-synthesize", "node_role": "Synthesizer", "node_title": "Summarize outcome for the user"}
	default:
		return nil
	}
}

func toolNodeEvidenceDetailsForAvatarID(avatarID string) []string {
	id := strings.TrimSpace(avatarID)
	switch {
	case id == "avatar-planner":
		return []string{"node_id: node-plan", "node_role: Planner", "node_title: Decompose task and set checkpoints"}
	case strings.HasPrefix(id, "avatar-researcher"):
		return []string{"node_id: node-survey", "node_role: Researcher", "node_title: Survey current repository context"}
	case strings.HasPrefix(id, "avatar-builder"):
		return []string{"node_id: node-build", "node_role: Builder", "node_title: Shape the smallest viable change"}
	case id == "avatar-critic":
		return []string{"node_id: node-review", "node_role: Critic", "node_title: Review risks and challenge gaps"}
	case id == "avatar-synthesizer":
		return []string{"node_id: lifecycle-synthesize", "node_role: Synthesizer", "node_title: Summarize outcome for the user"}
	default:
		return nil
	}
}

func mergeWorkflowNodeEvidence(payload map[string]any, avatarID string, nodeID string, nodeRole string, nodeTitle string) {
	if payload == nil {
		return
	}
	for key, value := range toolNodeEvidenceForAvatarID(avatarID) {
		payload[key] = value
	}
	if id := strings.TrimSpace(nodeID); id != "" {
		payload["node_id"] = id
	}
	if role := strings.TrimSpace(nodeRole); role != "" {
		payload["node_role"] = role
	}
	if title := strings.TrimSpace(nodeTitle); title != "" {
		payload["node_title"] = title
	}
}

type toolInvokeOptions struct {
	skipVerification   bool
	workflowCheckpoint *PendingApprovalWorkflow
}

func (e *Engine) invokeTool(ctx context.Context, envelope toolCallEnvelope, toolName string, operation string, input any) (tools.Result, error) {
	return e.invokeToolWithOptions(ctx, envelope, toolName, operation, input, toolInvokeOptions{})
}

func (e *Engine) invokeToolWithOptions(ctx context.Context, envelope toolCallEnvelope, toolName string, operation string, input any, options toolInvokeOptions) (tools.Result, error) {
	actionID := fmt.Sprintf("%s-act-%d", envelope.runID, time.Now().UTC().UnixNano())
	// F105: emit rewrite-after path so progress matches disk (cross-lang lift).
	input, remappedFrom := e.rewriteWriteToolInput(input)
	payload := toolPayload(actionID, toolName, operation, input, e.permissionMode)
	if remappedFrom != "" {
		payload["remapped_from"] = remappedFrom
	}
	for key, value := range toolNodeEvidenceForAvatarID(envelope.avatarID) {
		payload[key] = value
	}
	if options.workflowCheckpoint != nil {
		payload["workflow_checkpoint"] = clonePendingApprovalWorkflow(options.workflowCheckpoint)
	}
	if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "tool.requested", "tool", payload, nil); err != nil {
		return tools.Result{}, err
	}

	toolDefinition, ok := e.tools.Get(toolName)
	if !ok {
		err := fmt.Errorf("%s tool not registered", toolName)
		failedPayload := mergeToolErrorPayload(payload, err)
		if appendErr := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "tool.failed", "tool", failedPayload, nil); appendErr != nil {
			return tools.Result{}, appendErr
		}
		if persistErr := e.persistToolFailureEvaluation(envelope, toolName, operation, payload, err, false); persistErr != nil {
			return tools.Result{}, persistErr
		}
		return tools.Result{}, err
	}
	payload = e.resolveToolApprovalDecision(envelope.taskID, payload)
	if preflightErr := applyCodingPreflightPolicy(payload, envelope, toolName, input, e.permissionMode); preflightErr != nil {
		blockedPayload := mergeToolErrorPayload(payload, preflightErr)
		if appendErr := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "tool.denied", "tool", blockedPayload, nil); appendErr != nil {
			return tools.Result{}, appendErr
		}
		if persistErr := e.persistToolDenialEvaluation(envelope, toolName, operation, blockedPayload, preflightErr); persistErr != nil {
			return tools.Result{}, persistErr
		}
		return tools.Result{}, preflightErr
	}
	if decisionErr, awaitingApproval := toolPermissionModeError(payload); decisionErr != nil {
		blockedPayload := mergeToolErrorPayload(payload, decisionErr)
		eventType := "tool.denied"
		persistBlockedEvaluation := e.persistToolDenialEvaluation
		if awaitingApproval {
			if artifactPath, artifactErr := e.persistPendingApprovalRequest(envelope, toolName, operation, input, blockedPayload); artifactErr != nil {
				return tools.Result{}, artifactErr
			} else if strings.TrimSpace(artifactPath) != "" {
				blockedPayload["approval_request_artifact"] = artifactPath
			}
			eventType = "tool.awaiting_approval"
			persistBlockedEvaluation = e.persistToolApprovalRequiredEvaluation
		}
		if appendErr := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, eventType, "tool", blockedPayload, nil); appendErr != nil {
			return tools.Result{}, appendErr
		}
		if awaitingApproval {
			lifecycle := NewRunLifecycle(envelope.runID, envelope.taskID)
			lifecycle.MarkAwaitingApproval(RunApprovalBoundary{
				ApprovalKey:       strings.TrimSpace(fmt.Sprint(blockedPayload["approval_key"])),
				RequestedActionID: strings.TrimSpace(fmt.Sprint(blockedPayload["approval_key"])),
				TranscriptPath:    strings.TrimSpace(e.transcript.Path()),
				Status:            RunLifecycleStatusAwaitingApproval,
				Summary:           decisionErr.Error(),
			})
			if appendErr := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "run.lifecycle_updated", "runtime", lifecycle.Payload(), nil); appendErr != nil {
				return tools.Result{}, appendErr
			}
			if persistErr := e.persistRunLifecycleEvaluation(envelope.runID, envelope.taskID, envelope.avatarID, lifecycle); persistErr != nil {
				return tools.Result{}, persistErr
			}
		}
		if persistErr := persistBlockedEvaluation(envelope, toolName, operation, blockedPayload, decisionErr); persistErr != nil {
			return tools.Result{}, persistErr
		}
		return tools.Result{}, decisionErr
	}

	if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "tool.authorized", "tool", payload, nil); err != nil {
		return tools.Result{}, err
	}

	// When bypassPermissions is active, emit an audit event so every
	// tool invocation that skipped the normal approval boundary is
	// recorded with full tool/operation/input context.
	if e.permissionMode == PermissionModeBypassPermissions {
		inputSummary := summarizeToolInput(toolName, operation, input)
		auditPayload := map[string]any{
			"tool":            toolName,
			"operation":       operation,
			"input_summary":   inputSummary,
			"decision_source": "bypass_permissions_audit",
		}
		auditSummary := fmt.Sprintf("Audit: %s invoked with bypassPermissions — approval boundary was skipped.", inputSummary)
		if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "audit.bypass_permissions", "runtime", auditPayload, nil); err != nil {
			return tools.Result{}, err
		}
		// Also persist as an evaluation record so the bypass is visible
		// in task memory, not just the transcript.
		if e.memory != nil {
			_ = e.memory.RecordEvaluation(memstore.EvaluationRecord{
				SessionID: e.sessionID,
				RunID:     envelope.runID,
				TaskID:    envelope.taskID,
				AvatarID:  envelope.avatarID,
				Tool:      toolName,
				Kind:      "security_audit",
				Verdict:   "WARNING",
				Cause:     "bypass_permissions",
				Summary:   auditSummary,
				Source:    "runtime_security",
				Details: []string{
					"tool: " + toolName,
					"operation: " + operation,
					"input_summary: " + inputSummary,
					"decision_source: bypass_permissions_audit",
				},
				UpdatedAt: time.Now().UTC(),
			})
		}
	}

	if artifactPath, artifactErr := e.persistCodingRollbackArtifact(envelope, actionID, toolName, operation, input, payload); artifactErr != nil {
		return tools.Result{}, artifactErr
	} else if strings.TrimSpace(artifactPath) != "" {
		payload["rollback_artifact"] = artifactPath
	}

	result, err := toolDefinition.Call(ctx, input)
	if err != nil {
		if isToolPermissionDenied(err) {
			deniedPayload := mergeToolDeniedPayload(payload, err)
			if appendErr := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "tool.denied", "tool", deniedPayload, nil); appendErr != nil {
				return tools.Result{}, appendErr
			}
			if persistErr := e.persistToolDenialEvaluation(envelope, toolName, operation, deniedPayload, err); persistErr != nil {
				return tools.Result{}, persistErr
			}
			return result, err
		}
		failedPayload := mergeToolErrorPayload(payload, err)
		if appendErr := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "tool.failed", "tool", failedPayload, nil); appendErr != nil {
			return tools.Result{}, appendErr
		}
		if persistErr := e.persistToolFailureEvaluation(envelope, toolName, operation, payload, err, true); persistErr != nil {
			return tools.Result{}, persistErr
		}
		return result, err
	}
	if rollbackArtifact := payloadTrimmedString(payload, "rollback_artifact"); rollbackArtifact != "" {
		if err := e.finalizeCodingRollbackArtifact(rollbackArtifact); err != nil {
			return tools.Result{}, err
		}
	}

	completedPayload := toolPayload(actionID, toolName, operation, input, e.permissionMode)
	completedPayload["summary"] = summarizeToolResult(toolName, operation, input, result.Content)
	if remappedFrom != "" {
		completedPayload["remapped_from"] = remappedFrom
	}
	if rollbackArtifact := payloadTrimmedString(payload, "rollback_artifact"); rollbackArtifact != "" {
		completedPayload["rollback_artifact"] = rollbackArtifact
	}
	for key, value := range toolNodeEvidenceForAvatarID(envelope.avatarID) {
		completedPayload[key] = value
	}
	if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, envelope.phase, "tool.completed", "tool", completedPayload, nil); err != nil {
		return tools.Result{}, err
	}
	if err := e.persistReverifyRepairAttemptEvaluation(envelope, toolName, operation, input, completedPayload); err != nil {
		return tools.Result{}, err
	}
	if !options.skipVerification {
		if err := e.maybeRunVerification(ctx, envelope, toolName, input); err != nil {
			return result, err
		}
	}

	return result, nil
}

func (e *Engine) persistToolFailureEvaluation(envelope toolCallEnvelope, toolName string, operation string, payload map[string]any, err error, registered bool) error {
	if e == nil || e.memory == nil || err == nil {
		return nil
	}
	record := evaluation.BuildToolFailureRecord(toolName, operation, payload, err, registered)
	if strings.TrimSpace(record.Summary) == "" {
		return nil
	}
	record.Details = append(record.Details, toolFailureRecoveryDetails(toolName, operation, payload, err, registered)...)
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     envelope.runID,
		TaskID:    envelope.taskID,
		AvatarID:  envelope.avatarID,
		Tool:      record.Tool,
		Kind:      record.Kind,
		Verdict:   record.Verdict,
		Cause:     record.Cause,
		Summary:   record.Summary,
		Source:    record.Source,
		Details:   append([]string(nil), record.Details...),
		UpdatedAt: time.Now().UTC(),
	})
}

func (e *Engine) persistToolDenialEvaluation(envelope toolCallEnvelope, toolName string, operation string, payload map[string]any, err error) error {
	if e == nil || e.memory == nil || err == nil {
		return nil
	}
	record := evaluation.BuildToolDenialRecord(toolName, operation, payload, err)
	if strings.TrimSpace(record.Summary) == "" {
		return nil
	}
	record.Details = append(record.Details, toolFailureRecoveryDetails(toolName, operation, payload, err, true)...)
	// P9: Capture rejection feedback for the next Planner run.
	if wd, wdErr := os.Getwd(); wdErr == nil {
		CaptureRejectionFeedback(wd, toolName, operation,
			payloadString(payload, "path"), err.Error())
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     envelope.runID,
		TaskID:    envelope.taskID,
		AvatarID:  envelope.avatarID,
		Tool:      record.Tool,
		Kind:      record.Kind,
		Verdict:   record.Verdict,
		Cause:     record.Cause,
		Summary:   record.Summary,
		Source:    record.Source,
		Details:   append([]string(nil), record.Details...),
		UpdatedAt: time.Now().UTC(),
	})
}

func toolFailureRecoveryDetails(toolName string, operation string, payload map[string]any, err error, registered bool) []string {
	if err == nil {
		return nil
	}
	decision := toolFailureResumePolicy.CanRetryToolFailure(toolName, operation, ToolFailureRetryContext{
		AttemptStatus:    "failed",
		FailureKind:      toolFailureKind(registered, err),
		PermissionDenied: registered && isToolPermissionDenied(err),
	})
	details := make([]string, 0, 4)
	details = append(details, "retryable: "+fmt.Sprintf("%t", decision.Retryable))
	details = append(details, "reason: "+strings.TrimSpace(decision.Reason))
	if permissionMode := payloadNestedString(payload, "policy", "permission_mode"); permissionMode != "" {
		details = append(details, "permission_mode: "+permissionMode)
	}
	if decisionSource := payloadNestedString(payload, "policy", "decision_source"); decisionSource != "" {
		details = append(details, "decision_source: "+decisionSource)
	}
	if approvalKey := payloadString(payload, "approval_key"); approvalKey != "" {
		details = append(details, "approval_key: "+approvalKey)
	}
	if operationLabel := strings.TrimSpace(operation); operationLabel != "" {
		details = append(details, "operation: "+operationLabel)
	}
	return details
}

func payloadNestedString(payload map[string]any, outer string, inner string) string {
	if payload == nil {
		return ""
	}
	nested, ok := payload[strings.TrimSpace(outer)].(map[string]any)
	if !ok {
		return ""
	}
	value, ok := nested[strings.TrimSpace(inner)]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func toolFailureKind(registered bool, err error) string {
	if !registered {
		return "tool_not_registered"
	}
	if isToolPermissionDenied(err) {
		return "tool_permission_denied"
	}
	return "tool_execution_failed"
}

func (e *Engine) persistToolApprovalRequiredEvaluation(envelope toolCallEnvelope, toolName string, operation string, payload map[string]any, err error) error {
	if e == nil || e.memory == nil || err == nil {
		return nil
	}
	record := evaluation.BuildToolApprovalRequiredRecord(toolName, operation, payload, err)
	if strings.TrimSpace(record.Summary) == "" {
		return nil
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     envelope.runID,
		TaskID:    envelope.taskID,
		AvatarID:  envelope.avatarID,
		Tool:      record.Tool,
		Kind:      record.Kind,
		Verdict:   record.Verdict,
		Cause:     record.Cause,
		Summary:   record.Summary,
		Source:    record.Source,
		Details:   append([]string(nil), record.Details...),
		UpdatedAt: time.Now().UTC(),
	})
}

func (e *Engine) persistPendingApprovalRequest(envelope toolCallEnvelope, toolName string, operation string, input any, payload map[string]any) (string, error) {
	if e == nil || e.memory == nil {
		return "", nil
	}
	memoryPath := strings.TrimSpace(e.memory.Path())
	approvalKey := strings.TrimSpace(fmt.Sprint(payload["approval_key"]))
	if memoryPath == "" || approvalKey == "" {
		return "", nil
	}
	memoryDir := filepath.Dir(memoryPath)
	requestedAt := time.Now().UTC()
	request, ok := buildPendingApprovalRequest(toolName, operation, input, e.permissionMode, approvalKey, requestedAt)
	if !ok {
		return "", nil
	}
	request.SuspendedAction = NewSuspendedActionFrame(envelope, strings.TrimSpace(fmt.Sprint(payload["action_id"])), approvalKey, toolName, operation, input, e.transcript.Path(), requestedAt)
	if optionsWorkflow := requestWorkflowCheckpoint(payload); optionsWorkflow != nil {
		request.Workflow = optionsWorkflow
	}
	pausePointID := request.PausePointID()
	request.Continuation = &PendingApprovalContinuation{
		RunID:                strings.TrimSpace(envelope.runID),
		TaskID:               strings.TrimSpace(envelope.taskID),
		AvatarID:             strings.TrimSpace(envelope.avatarID),
		Phase:                strings.TrimSpace(envelope.phase),
		TranscriptPath:       strings.TrimSpace(e.transcript.Path()),
		OriginTranscriptPath: strings.TrimSpace(e.transcript.Path()),
		PausePointID:         pausePointID,
		PausePointKind:       "approval_gate",
		RequestedActionID:    approvalKey,
		RequestedAt:          requestedAt,
	}
	artifactPath := PendingApprovalRequestPath(memoryDir, approvalKey)
	if artifactPath == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		return "", fmt.Errorf("create approval artifact directory: %w", err)
	}
	// Tag the request with the session that created it, then compute
	// an HMAC integrity tag over the payload before writing.
	request.SessionID = e.sessionID
	if err := request.ComputeHMAC(); err != nil {
		return "", fmt.Errorf("compute HMAC for approval request: %w", err)
	}
	content, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal approval request artifact: %w", err)
	}
	if err := os.WriteFile(artifactPath, append(content, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("write approval request artifact: %w", err)
	}
	return artifactPath, nil
}

func requestWorkflowCheckpoint(payload map[string]any) *PendingApprovalWorkflow {
	raw, ok := payload["workflow_checkpoint"]
	if !ok {
		return nil
	}
	checkpoint, ok := raw.(*PendingApprovalWorkflow)
	if !ok || checkpoint == nil {
		return nil
	}
	return clonePendingApprovalWorkflow(checkpoint)
}

func clonePendingApprovalWorkflow(checkpoint *PendingApprovalWorkflow) *PendingApprovalWorkflow {
	if checkpoint == nil {
		return nil
	}
	cloned := *checkpoint
	cloned.CompletedNodeIDs = append([]string(nil), checkpoint.CompletedNodeIDs...)
	cloned.RemainingNodeIDs = append([]string(nil), checkpoint.RemainingNodeIDs...)
	cloned.BlockedNodeDependsOn = append([]string(nil), checkpoint.BlockedNodeDependsOn...)
	cloned.CompletedNodes = clonePendingApprovalWorkflowNodes(checkpoint.CompletedNodes)
	cloned.RemainingNodes = clonePendingApprovalWorkflowNodes(checkpoint.RemainingNodes)
	if checkpoint.WorkflowSnapshot != nil {
		snap := *checkpoint.WorkflowSnapshot
		snap.Nodes = append([]WorkflowNodeRuntime(nil), checkpoint.WorkflowSnapshot.Nodes...)
		snap.Order = append([]string(nil), checkpoint.WorkflowSnapshot.Order...)
		cloned.WorkflowSnapshot = &snap
	}
	if len(checkpoint.NodeRetryCounts) > 0 {
		cloned.NodeRetryCounts = make(map[string]int, len(checkpoint.NodeRetryCounts))
		for k, v := range checkpoint.NodeRetryCounts {
			cloned.NodeRetryCounts[k] = v
		}
	}
	return &cloned
}

func clonePendingApprovalWorkflowNodes(nodes []PendingApprovalWorkflowNode) []PendingApprovalWorkflowNode {
	if len(nodes) == 0 {
		return nil
	}
	cloned := make([]PendingApprovalWorkflowNode, 0, len(nodes))
	for _, node := range nodes {
		copied := node
		copied.DependsOn = append([]string(nil), node.DependsOn...)
		cloned = append(cloned, copied)
	}
	return cloned
}

func (e *Engine) continueApprovedToolCall(ctx context.Context, request PendingApprovalRequest, options ToolCallOptions) (ToolCallResult, error) {
	toolName, operation, toolInput, err := request.ResolveToolCall()
	if err != nil {
		return ToolCallResult{}, err
	}
	policy := ResumePolicy{}
	if err := request.ValidateSuspendedAction(toolName, operation, toolInput); err != nil {
		decision := policy.DenyValidationFailure(request, err)
		if recordErr := e.recordResumePolicyEvaluation(request, toolName, operation, decision); recordErr != nil {
			return ToolCallResult{}, recordErr
		}
		return ToolCallResult{}, err
	}
	continuation := request.Continuation
	if continuation == nil {
		return e.callToolWithOptions(ctx, toolName, operation, toolInput, options)
	}
	if result, handled, replayErr := e.completedApprovalReplayResult(request, toolName, operation); handled {
		return result, replayErr
	}
	// Operator already approved this mutation gate. For the rest of the
	// continuation (replay + remaining DAG nodes), do not re-block on every
	// subsequent write under PermissionModeDefault — that left a second
	// pending approval_key and TaskWorkspaceStatus stuck on awaiting_approval.
	prevMode := e.permissionMode
	nextMode := approval.ContinuationMode(prevMode)
	if nextMode != prevMode {
		e.permissionMode = nextMode
		defer func() { e.permissionMode = prevMode }()
	}
	decision := policy.CanStartAttempt(request)
	if !decision.Allowed {
		if recordErr := e.recordResumePolicyEvaluation(request, toolName, operation, decision); recordErr != nil {
			return ToolCallResult{}, recordErr
		}
		return ToolCallResult{}, decision.Err()
	}
	if recordErr := e.recordResumePolicyEvaluation(request, toolName, operation, decision); recordErr != nil {
		return ToolCallResult{}, recordErr
	}
	continuationRunID := fmt.Sprintf("%s-continued-%d", strings.TrimSpace(continuation.RunID), time.Now().UTC().UnixNano())
	continuationTaskID := strings.TrimSpace(continuation.TaskID)
	if continuationTaskID == "" {
		continuationTaskID = fmt.Sprintf("%s-task", continuationRunID)
	}
	continuationAvatarID := strings.TrimSpace(continuation.AvatarID)
	continuationPhase := strings.TrimSpace(continuation.Phase)
	if continuationPhase == "" {
		continuationPhase = "reviewing"
	}
	pausePointID := strings.TrimSpace(continuation.PausePointID)
	if pausePointID == "" {
		pausePointID = request.PausePointID()
	}
	attempt := NewResumeAttempt(pausePointID, continuationRunID, continuationTaskID, time.Now().UTC())
	if emitErr := e.emit(continuationRunID, continuationTaskID, continuationAvatarID, continuationPhase, "run.started", "runtime", map[string]any{
		"mode":                      "approval_continuation",
		"approval_key":              strings.TrimSpace(request.ApprovalKey),
		"pause_point_id":            pausePointID,
		"pause_point_kind":          "approval_gate",
		"resume_attempt_id":         attempt.ID,
		"origin_run_id":             strings.TrimSpace(continuation.RunID),
		"origin_task_id":            strings.TrimSpace(continuation.TaskID),
		"origin_transcript":         strings.TrimSpace(continuation.OriginTranscriptPath),
		"origin_boundary_id":        strings.TrimSpace(continuation.BoundaryEventID),
		"origin_boundary_type":      strings.TrimSpace(continuation.BoundaryType),
		"requested_action_id":       strings.TrimSpace(continuation.RequestedActionID),
		"task_workspace_id":         continuationTaskID,
		"continuation_requested_at": time.Now().UTC().Format(time.RFC3339Nano),
	}, nil); emitErr != nil {
		return ToolCallResult{}, emitErr
	}
	if recordErr := e.recordLLMStatus(continuationRunID, continuationTaskID); recordErr != nil {
		return ToolCallResult{}, recordErr
	}
	attempt.Summary = "Approval continuation attempt started."
	if attemptErr := e.recordResumeAttemptEvaluation(request, toolName, operation, attempt); attemptErr != nil {
		return ToolCallResult{}, attemptErr
	}
	result, err := e.callToolWithOptions(ctx, toolName, operation, toolInput, options)
	result.ContinuationRun = continuationRunID
	result.ContinuationTask = continuationTaskID
	outcome := "completed"
	summary := strings.TrimSpace(result.Content)
	if summary == "" {
		summary = fmt.Sprintf("Continued approved request for %s/%s.", toolName, operation)
	}
	payload := map[string]any{
		"approval_key":            strings.TrimSpace(request.ApprovalKey),
		"tool":                    strings.TrimSpace(toolName),
		"operation":               strings.TrimSpace(operation),
		"pause_point_id":          pausePointID,
		"pause_point_kind":        "approval_gate",
		"resume_attempt_id":       attempt.ID,
		"origin_run_id":           strings.TrimSpace(continuation.RunID),
		"origin_task_id":          strings.TrimSpace(continuation.TaskID),
		"origin_transcript":       strings.TrimSpace(continuation.OriginTranscriptPath),
		"requested_action_id":     strings.TrimSpace(continuation.RequestedActionID),
		"requested_event_id":      strings.TrimSpace(continuation.RequestedEventID),
		"origin_boundary_id":      strings.TrimSpace(continuation.BoundaryEventID),
		"origin_boundary_type":    strings.TrimSpace(continuation.BoundaryType),
		"continuation_run_id":     continuationRunID,
		"continuation_task_id":    continuationTaskID,
		"continuation_transcript": strings.TrimSpace(result.TranscriptPath),
		"summary":                 summary,
	}
	if err != nil {
		outcome = "failed"
		payload["summary"] = fmt.Sprintf("Approval continuation failed for %s/%s: %v", toolName, operation, err)
		payload["error"] = err.Error()
	}
	if outcome == "failed" {
		attempt = attempt.Fail(result.TranscriptPath, fmt.Sprint(payload["summary"]), time.Now().UTC())
	} else {
		attempt = attempt.Complete(result.TranscriptPath, fmt.Sprint(payload["summary"]), time.Now().UTC())
	}
	lifecycle := NewRunLifecycle(strings.TrimSpace(continuation.RunID), strings.TrimSpace(continuation.TaskID))
	lifecycle.MarkAwaitingApproval(RunApprovalBoundary{
		ApprovalKey:       strings.TrimSpace(request.ApprovalKey),
		RequestedActionID: strings.TrimSpace(continuation.RequestedActionID),
		RequestedEventID:  strings.TrimSpace(continuation.RequestedEventID),
		BoundaryEventID:   strings.TrimSpace(continuation.BoundaryEventID),
		BoundaryType:      strings.TrimSpace(continuation.BoundaryType),
		TranscriptPath:    strings.TrimSpace(continuation.OriginTranscriptPath),
		Status:            RunLifecycleStatusAwaitingApproval,
	})
	if lifecycleErr := lifecycle.MarkApprovalContinued(RunContinuationBoundary{
		ContinuationRunID:      continuationRunID,
		ContinuationTaskID:     continuationTaskID,
		ContinuationTranscript: strings.TrimSpace(result.TranscriptPath),
		Status:                 outcome,
		Summary:                fmt.Sprint(payload["summary"]),
	}); lifecycleErr != nil && err == nil {
		err = lifecycleErr
	}
	if emitErr := e.emit(strings.TrimSpace(continuation.RunID), strings.TrimSpace(continuation.TaskID), continuationAvatarID, continuationPhase, "run.lifecycle_updated", "runtime", lifecycle.Payload(), nil); emitErr != nil && err == nil {
		err = emitErr
	}
	if persistErr := e.persistRunLifecycleEvaluation(strings.TrimSpace(continuation.RunID), strings.TrimSpace(continuation.TaskID), continuationAvatarID, lifecycle); persistErr != nil && err == nil {
		err = persistErr
	}
	if emitErr := e.emit(strings.TrimSpace(continuation.RunID), strings.TrimSpace(continuation.TaskID), continuationAvatarID, continuationPhase, "tool.approval_continued", "runtime", payload, nil); emitErr != nil && err == nil {
		err = emitErr
	}
	if checkpointErr := e.emitWorkflowResumeCheckpoint(strings.TrimSpace(continuation.RunID), strings.TrimSpace(continuation.TaskID), continuationAvatarID, continuationPhase, continuationRunID, continuationTaskID, strings.TrimSpace(request.ApprovalKey), outcome, request.Workflow); checkpointErr != nil && err == nil {
		err = checkpointErr
	}
	if workflowErr := e.continueWorkflowFromCheckpoint(strings.TrimSpace(continuation.RunID), strings.TrimSpace(continuation.TaskID), strings.TrimSpace(request.ApprovalKey), continuationRunID, continuationTaskID, continuationAvatarID, toolName, operation, lifecycle, outcome, request.Workflow); workflowErr != nil && err == nil {
		err = workflowErr
	}
	if emitErr := e.emitStableBoundary(continuationRunID, continuationTaskID, continuationAvatarID, outcome, fmt.Sprint(payload["summary"]), "approval_continuation", map[string]any{
		"approval_key":            strings.TrimSpace(request.ApprovalKey),
		"origin_run_id":           strings.TrimSpace(continuation.RunID),
		"origin_task_id":          strings.TrimSpace(continuation.TaskID),
		"origin_transcript":       strings.TrimSpace(continuation.OriginTranscriptPath),
		"continuation_transcript": strings.TrimSpace(result.TranscriptPath),
	}); emitErr != nil && err == nil {
		err = emitErr
	}
	if updateErr := e.updatePendingApprovalContinuation(request, continuationRunID, continuationTaskID, result.TranscriptPath, outcome, fmt.Sprint(payload["summary"])); updateErr != nil && err == nil {
		err = updateErr
	}
	if attemptErr := e.recordResumeAttemptEvaluation(request, toolName, operation, attempt); attemptErr != nil && err == nil {
		err = attemptErr
	}
	return result, err
}

func (e *Engine) completedApprovalReplayResult(request PendingApprovalRequest, toolName string, operation string) (ToolCallResult, bool, error) {
	continuation := request.Continuation
	if continuation == nil || strings.TrimSpace(continuation.ContinuationStatus) != "completed" {
		return ToolCallResult{}, false, nil
	}
	continuationRunID := strings.TrimSpace(continuation.ContinuationRunID)
	continuationTaskID := strings.TrimSpace(continuation.ContinuationTaskID)
	continuationTranscript := strings.TrimSpace(continuation.ContinuationTranscript)
	if continuationTranscript == "" {
		continuationTranscript = strings.TrimSpace(continuation.TranscriptPath)
	}
	if continuationRunID == "" && continuationTaskID == "" && continuationTranscript == "" {
		return ToolCallResult{}, false, nil
	}
	originRunID := strings.TrimSpace(continuation.RunID)
	originTaskID := strings.TrimSpace(continuation.TaskID)
	avatarID := strings.TrimSpace(continuation.AvatarID)
	phase := strings.TrimSpace(continuation.Phase)
	if phase == "" {
		phase = "reviewing"
	}
	pausePointID := strings.TrimSpace(continuation.PausePointID)
	if pausePointID == "" {
		pausePointID = request.PausePointID()
	}
	attemptID := strings.TrimSpace(continuation.ResumeAttemptID)
	if attemptID == "" {
		attemptID = makeResumeAttemptID(pausePointID, continuationRunID)
	}
	summary := fmt.Sprintf("Approval replay skipped for %s/%s because the approval request already has a completed continuation.", strings.TrimSpace(toolName), strings.TrimSpace(operation))
	attempt := ResumeAttemptFromStatus(pausePointID, continuationRunID, continuationTaskID, string(ResumeAttemptStatusSkipped), continuationTranscript, summary, time.Now().UTC())
	if strings.TrimSpace(continuation.ResumeAttemptID) != "" {
		attempt.ID = strings.TrimSpace(continuation.ResumeAttemptID)
	}
	payload := map[string]any{
		"approval_key":            strings.TrimSpace(request.ApprovalKey),
		"tool":                    strings.TrimSpace(toolName),
		"operation":               strings.TrimSpace(operation),
		"pause_point_id":          pausePointID,
		"pause_point_kind":        "approval_gate",
		"resume_attempt_id":       attempt.ID,
		"origin_run_id":           originRunID,
		"origin_task_id":          originTaskID,
		"continuation_run_id":     continuationRunID,
		"continuation_task_id":    continuationTaskID,
		"continuation_transcript": continuationTranscript,
		"status":                  "skipped",
		"reason":                  "already_completed",
		"summary":                 summary,
	}
	if err := e.emit(originRunID, originTaskID, avatarID, phase, "tool.approval_replay_skipped", "runtime", payload, nil); err != nil {
		return ToolCallResult{}, true, err
	}
	if e != nil && e.memory != nil {
		details := []string{
			"decision_source: runtime_idempotency_guard",
			"approval_key: " + strings.TrimSpace(request.ApprovalKey),
			"operation: " + strings.TrimSpace(operation),
			"pause_point_id: " + pausePointID,
			"resume_attempt_id: " + attempt.ID,
			"continuation_run_id: " + continuationRunID,
			"continuation_task_id: " + continuationTaskID,
			"continuation_transcript: " + continuationTranscript,
			"replay_status: skipped",
			"skip_reason: already_completed",
		}
		if err := e.memory.RecordEvaluation(memstore.EvaluationRecord{
			SessionID: e.sessionID,
			RunID:     continuationRunID,
			TaskID:    originTaskID,
			AvatarID:  avatarID,
			Tool:      strings.TrimSpace(toolName),
			Kind:      "passive_feedback",
			Verdict:   "PASS",
			Cause:     "tool_approval_replay_skipped",
			Summary:   summary,
			Source:    "runtime",
			Details:   details,
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			return ToolCallResult{}, true, err
		}
	}
	if err := e.recordResumePolicyEvaluation(request, toolName, operation, ResumePolicy{}.CanStartAttempt(request)); err != nil {
		return ToolCallResult{}, true, err
	}
	if err := e.recordResumeAttemptEvaluation(request, toolName, operation, attempt); err != nil {
		return ToolCallResult{}, true, err
	}
	return ToolCallResult{
		TranscriptPath:   continuationTranscript,
		Content:          summary,
		ContinuationRun:  continuationRunID,
		ContinuationTask: continuationTaskID,
	}, true, nil
}

func makeResumeAttemptID(pausePointID string, continuationRunID string) string {
	parts := []string{strings.TrimSpace(pausePointID), strings.TrimSpace(continuationRunID)}
	return strings.Replace(hashPausePointParts(parts), "pause-", "resume-", 1)
}

// continueWorkflowFromCheckpoint lives in workflow_resume.go (S4.1 real scheduler resume).

func workflowContinuationNodes(checkpoint *PendingApprovalWorkflow) []PendingApprovalWorkflowNode {
	if checkpoint == nil {
		return nil
	}
	if len(checkpoint.RemainingNodes) > 0 {
		nodes := make([]PendingApprovalWorkflowNode, 0, len(checkpoint.RemainingNodes))
		for i, node := range checkpoint.RemainingNodes {
			nodes = append(nodes, normalizeWorkflowContinuationNode(node, workflowNodeIDAt(checkpoint.RemainingNodeIDs, i)))
		}
		return nodes
	}
	nodes := make([]PendingApprovalWorkflowNode, 0, len(checkpoint.RemainingNodeIDs))
	for _, id := range checkpoint.RemainingNodeIDs {
		nodes = append(nodes, normalizeWorkflowContinuationNode(PendingApprovalWorkflowNode{ID: id}, id))
	}
	return nodes
}

func normalizeWorkflowContinuationNode(node PendingApprovalWorkflowNode, fallbackID string) PendingApprovalWorkflowNode {
	node.ID = strings.TrimSpace(node.ID)
	if node.ID == "" {
		node.ID = strings.TrimSpace(fallbackID)
	}
	node.Role = strings.TrimSpace(node.Role)
	node.Title = strings.TrimSpace(node.Title)
	if node.Role == "" {
		node.Role = workflowRoleFromNodeID(node.ID)
	}
	if node.Title == "" {
		node.Title = node.ID
	}
	return node
}

func workflowNodeIDAt(ids []string, index int) string {
	if index < 0 || index >= len(ids) {
		return ""
	}
	return ids[index]
}

func workflowRoleFromNodeID(nodeID string) string {
	switch strings.TrimSpace(nodeID) {
	case "node-review":
		return "Critic"
	case "node-summarize":
		return "Synthesizer"
	case "node-build":
		return "Builder"
	case "node-survey":
		return "Researcher"
	case "node-plan":
		return "Planner"
	default:
		return ""
	}
}

func workflowContinuationAvatarID(role string) string {
	switch strings.TrimSpace(role) {
	case "Planner":
		return "avatar-planner"
	case "Researcher":
		return "avatar-researcher"
	case "Builder":
		return "avatar-builder"
	case "Critic":
		return "avatar-critic"
	case "Synthesizer":
		return "avatar-synthesizer"
	default:
		return ""
	}
}

func (e *Engine) emitWorkflowContinuationMessage(runID string, taskID string, avatarID string, node PendingApprovalWorkflowNode) error {
	switch strings.TrimSpace(node.Role) {
	case "Critic":
		content := "Continuation review: approved guarded action completed; remaining workflow nodes resumed serially from checkpoint."
		return e.emitAvatarMessage(runID, taskID, avatarID, "reviewing", "challenge", content, "Critic challenge: approved guarded action completed and serial checkpoint continuation is bounded.", "review", map[string]any{"node_id": node.ID, "resume_mode": "approval_checkpoint_serial"}, nil)
	case "Synthesizer":
		content := "Workflow continuation completed after approval replay. Remaining workflow nodes resumed serially from checkpoint."
		return e.emitAvatarMessage(runID, taskID, avatarID, "reviewing", "summarize", content, "Synthesizer summary: workflow continuation completed from approval checkpoint.", "summary", map[string]any{"node_id": node.ID, "resume_mode": "approval_checkpoint_serial"}, nil)
	default:
		content := fmt.Sprintf("%s completed during serial workflow continuation from approval checkpoint.", strings.TrimSpace(node.Role))
		if strings.TrimSpace(node.Role) == "" {
			content = "Workflow node completed during serial workflow continuation from approval checkpoint."
		}
		return e.emitAvatarMessage(runID, taskID, avatarID, "planning", "report", content, "Workflow continuation report: node completed from approval checkpoint.", "workflow_continuation", map[string]any{"node_id": node.ID, "resume_mode": "approval_checkpoint_serial"}, nil)
	}
}

func (e *Engine) emitWorkflowResumeCheckpoint(originRunID string, originTaskID string, avatarID string, phase string, continuationRunID string, continuationTaskID string, approvalKey string, outcome string, checkpoint *PendingApprovalWorkflow) error {
	if checkpoint == nil {
		return nil
	}
	summary := "Approval continuation replayed the guarded action at the workflow checkpoint."
	if strings.TrimSpace(outcome) == "completed" && len(checkpoint.RemainingNodeIDs) > 0 {
		summary = "Approval continuation replayed the guarded action at the workflow checkpoint; remaining workflow nodes are resuming serially."
	}
	payload := map[string]any{
		"origin_run_id":        strings.TrimSpace(originRunID),
		"origin_task_id":       strings.TrimSpace(originTaskID),
		"continuation_run_id":  strings.TrimSpace(continuationRunID),
		"continuation_task_id": strings.TrimSpace(continuationTaskID),
		"approval_key":         strings.TrimSpace(approvalKey),
		"outcome":              strings.TrimSpace(outcome),
		"pause_point_id":       strings.TrimSpace(checkpoint.PausePointID),
		"pause_point_kind":     strings.TrimSpace(checkpoint.PausePointKind),
		"pause_point_digest":   strings.TrimSpace(checkpoint.PausePointDigest),
		"blocked_node_id":      strings.TrimSpace(checkpoint.BlockedNodeID),
		"blocked_node_role":    strings.TrimSpace(checkpoint.BlockedNodeRole),
		"blocked_node_title":   strings.TrimSpace(checkpoint.BlockedNodeTitle),
		"blocked_depends_on":   append([]string(nil), checkpoint.BlockedNodeDependsOn...),
		"completed_node_ids":   append([]string(nil), checkpoint.CompletedNodeIDs...),
		"remaining_node_ids":   append([]string(nil), checkpoint.RemainingNodeIDs...),
		"checkpoint_event":     strings.TrimSpace(checkpoint.CheckpointEvent),
		"summary":              summary,
	}
	if strings.TrimSpace(checkpoint.BlockedNodeID) != "" {
		payload["node_id"] = strings.TrimSpace(checkpoint.BlockedNodeID)
	}
	if strings.TrimSpace(checkpoint.BlockedNodeRole) != "" {
		payload["node_role"] = strings.TrimSpace(checkpoint.BlockedNodeRole)
	}
	if strings.TrimSpace(checkpoint.BlockedNodeTitle) != "" {
		payload["node_title"] = strings.TrimSpace(checkpoint.BlockedNodeTitle)
	}
	if strings.TrimSpace(checkpoint.PlanSummary) != "" {
		payload["plan_summary"] = strings.TrimSpace(checkpoint.PlanSummary)
	}
	if err := e.emit(originRunID, originTaskID, avatarID, phase, "workflow.resume_checkpoint", "runtime", payload, nil); err != nil {
		return err
	}
	if e == nil || e.memory == nil {
		return nil
	}
	details := []string{
		"origin_run_id: " + strings.TrimSpace(originRunID),
		"origin_task_id: " + strings.TrimSpace(originTaskID),
		"continuation_run_id: " + strings.TrimSpace(continuationRunID),
		"continuation_task_id: " + strings.TrimSpace(continuationTaskID),
		"approval_key: " + strings.TrimSpace(approvalKey),
		"pause_point_id: " + strings.TrimSpace(checkpoint.PausePointID),
		"pause_point_kind: " + strings.TrimSpace(checkpoint.PausePointKind),
		"blocked_node_id: " + strings.TrimSpace(checkpoint.BlockedNodeID),
		"blocked_node_role: " + strings.TrimSpace(checkpoint.BlockedNodeRole),
		"node_id: " + strings.TrimSpace(checkpoint.BlockedNodeID),
		"node_role: " + strings.TrimSpace(checkpoint.BlockedNodeRole),
		"remaining_node_count: " + fmt.Sprint(len(checkpoint.RemainingNodeIDs)),
	}
	if strings.TrimSpace(checkpoint.BlockedNodeTitle) != "" {
		details = append(details, "node_title: "+strings.TrimSpace(checkpoint.BlockedNodeTitle))
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     strings.TrimSpace(originRunID),
		TaskID:    strings.TrimSpace(originTaskID),
		AvatarID:  strings.TrimSpace(avatarID),
		Kind:      "workflow_feedback",
		Verdict:   "INFO",
		Cause:     "workflow_resume_checkpoint",
		Summary:   summary,
		Source:    "runtime",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	})
}

func (e *Engine) updatePendingApprovalContinuation(request PendingApprovalRequest, continuationRunID string, continuationTaskID string, continuationTranscript string, outcome string, summary string) error {
	if e == nil || e.memory == nil {
		return nil
	}
	memoryPath := strings.TrimSpace(e.memory.Path())
	approvalKey := strings.TrimSpace(request.ApprovalKey)
	if memoryPath == "" || approvalKey == "" {
		return nil
	}
	memoryDir := filepath.Dir(memoryPath)
	artifactPath := PendingApprovalRequestPath(memoryDir, approvalKey)
	if artifactPath == "" {
		return nil
	}
	stored, err := LoadPendingApprovalRequest(memoryDir, approvalKey)
	if err != nil {
		return err
	}
	if stored.Continuation == nil {
		stored.Continuation = &PendingApprovalContinuation{}
	}
	if stored.SuspendedAction == nil {
		stored.SuspendedAction = NewSuspendedActionFrame(toolCallEnvelope{
			runID:  stored.Continuation.RunID,
			taskID: stored.Continuation.TaskID,
			phase:  stored.Continuation.Phase,
		}, stored.ApprovalKey, stored.ApprovalKey, stored.Tool, stored.Operation, nil, stored.Continuation.OriginTranscriptPath, stored.RequestedAt)
	}
	pausePointID := strings.TrimSpace(stored.Continuation.PausePointID)
	if pausePointID == "" {
		pausePointID = stored.PausePointID()
	}
	attemptID := makeResumeAttemptID(pausePointID, continuationRunID)
	now := time.Now().UTC()
	stored.Continuation.PausePointID = pausePointID
	stored.Continuation.PausePointKind = "approval_gate"
	stored.Continuation.ResumeAttemptID = attemptID
	stored.Continuation.ResumeAttemptStatus = strings.TrimSpace(outcome)
	stored.Continuation.BoundaryStatus = strings.TrimSpace(outcome)
	stored.Continuation.BoundarySummary = strings.TrimSpace(summary)
	stored.Continuation.BoundaryType = "approval_continuation"
	stored.Continuation.ContinuationRunID = strings.TrimSpace(continuationRunID)
	stored.Continuation.ContinuationTaskID = strings.TrimSpace(continuationTaskID)
	stored.Continuation.ContinuationStatus = strings.TrimSpace(outcome)
	stored.Continuation.ContinuationSummary = strings.TrimSpace(summary)
	stored.Continuation.ContinuationTranscript = strings.TrimSpace(continuationTranscript)
	stored.Continuation.TranscriptPath = strings.TrimSpace(continuationTranscript)
	stored.Continuation.ContinuationRequestedAt = now
	stored.Continuation.ResumeAttempts = append(stored.Continuation.ResumeAttempts, ResumeAttemptRecord{
		AttemptID:              attemptID,
		PausePointID:           pausePointID,
		Status:                 strings.TrimSpace(outcome),
		ContinuationRunID:      strings.TrimSpace(continuationRunID),
		ContinuationTaskID:     strings.TrimSpace(continuationTaskID),
		ContinuationTranscript: strings.TrimSpace(continuationTranscript),
		Summary:                strings.TrimSpace(summary),
		StartedAt:              now,
		CompletedAt:            now,
	})
	// Recompute HMAC after mutating the continuation so the on-disk
	// integrity tag stays consistent with the updated payload.
	if err := stored.ComputeHMAC(); err != nil {
		return fmt.Errorf("recompute HMAC for approval continuation: %w", err)
	}
	content, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal approval continuation artifact: %w", err)
	}
	if err := os.WriteFile(artifactPath, append(content, '\n'), 0o644); err != nil {
		return fmt.Errorf("write approval continuation artifact: %w", err)
	}
	return nil
}

func (e *Engine) persistReverifyRepairAttemptEvaluation(envelope toolCallEnvelope, toolName string, operation string, input any, payload map[string]any) error {
	if e == nil || e.memory == nil || !isReverifyRepairTool(toolName, input) {
		return nil
	}
	if strings.TrimSpace(envelope.taskID) == "" {
		return nil
	}
	snapshot, err := e.memory.LoadLatestTaskSnapshot(envelope.taskID)
	if err != nil {
		return err
	}
	if memstore.VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory) != "pending reverify for latest non-pass verification" {
		return nil
	}
	latestNonPass := memstore.LatestNonPassVerificationSnapshot(snapshot.Verification, snapshot.VerificationHistory)
	if latestNonPass == nil {
		return nil
	}
	record := evaluation.BuildReverifyRepairAttemptRecord(toolName, operation, payload, latestNonPass.Tool, latestNonPass.Verdict, latestNonPass.ReportPath)
	if strings.TrimSpace(record.Summary) == "" {
		return nil
	}
	if err := e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID:  e.sessionID,
		RunID:      envelope.runID,
		TaskID:     envelope.taskID,
		AvatarID:   envelope.avatarID,
		Tool:       record.Tool,
		Kind:       record.Kind,
		Verdict:    record.Verdict,
		Cause:      record.Cause,
		Summary:    record.Summary,
		Source:     record.Source,
		ReportPath: record.ReportPath,
		Details:    append([]string(nil), record.Details...),
		UpdatedAt:  time.Now().UTC(),
	}); err != nil {
		return err
	}
	return e.recordVerifierRemediationResumeAttemptEvaluation(envelope, toolName, operation, payload, snapshot.EvaluationRecords, latestNonPass.ReportPath)
}

func (e *Engine) recordVerifierRemediationResumeAttemptEvaluation(envelope toolCallEnvelope, toolName string, operation string, payload map[string]any, records []memstore.EvaluationRecord, reportPath string) error {
	if e == nil || e.memory == nil {
		return nil
	}
	boundary := verifierRemediationExecutionBoundary(records)
	if boundary == nil {
		return nil
	}
	pauseRecord := memstore.LatestVerifierRemediationPausePointEvaluation(records)
	if pauseRecord == nil {
		return nil
	}
	pausePointID := memstore.EvaluationRecordPausePointID(*pauseRecord)
	if strings.TrimSpace(pausePointID) == "" {
		return nil
	}
	previousTargets := memstore.EvaluationRecordExpectedTargets(*pauseRecord)
	nextTargets := payloadStringList(payload, "expected_targets")
	if len(nextTargets) == 0 {
		nextTargets = payloadStringList(payload, "changed_files")
	}
	point := PausePoint{
		ID:           pausePointID,
		Kind:         PausePointKindVerifierRemediation,
		OriginRunID:  memstore.EvaluationRecordOriginRunID(*pauseRecord),
		OriginTaskID: memstore.EvaluationRecordOriginTaskID(*pauseRecord),
		AvatarID:     envelope.avatarID,
		Tool:         strings.TrimSpace(toolName),
		Operation:    "verifier_remediation",
	}
	proposalID := payloadString(payload, "proposal_id")
	explicitUpdate := proposalID == "remediate-latest-non-pass"
	decision := (ResumePolicy{}).CanRetryVerifierRemediation(point, previousTargets, nextTargets, explicitUpdate)
	attemptID := makeResumeAttemptID(pausePointID, strings.TrimSpace(envelope.runID))
	details := []string{
		"pause_point_id: " + pausePointID,
		"pause_point_kind: " + string(PausePointKindVerifierRemediation),
		"resume_attempt_id: " + attemptID,
		"resume_attempt_status: started",
		"retryable: " + fmt.Sprintf("%t", decision.Retryable),
		"allowed: " + fmt.Sprintf("%t", decision.Allowed),
		"reason: " + strings.TrimSpace(decision.Reason),
		"origin_run_id: " + memstore.EvaluationRecordOriginRunID(*pauseRecord),
		"origin_task_id: " + memstore.EvaluationRecordOriginTaskID(*pauseRecord),
		"continuation_run_id: " + strings.TrimSpace(envelope.runID),
		"continuation_task_id: " + strings.TrimSpace(envelope.taskID),
		"tool: " + strings.TrimSpace(toolName),
		"operation: " + strings.TrimSpace(operation),
		"recovery_boundary_action: " + strings.TrimSpace(boundary.ActionKind),
		"recovery_boundary_guard_status: " + strings.TrimSpace(boundary.GuardStatus),
		"recovery_boundary_required_authority: " + strings.TrimSpace(boundary.RequiredAuthority),
		"recovery_boundary_source: " + strings.TrimSpace(boundary.SourceCause),
	}
	if len(previousTargets) > 0 {
		details = append(details, "previous_targets: "+strings.Join(previousTargets, ", "))
	}
	if len(nextTargets) > 0 {
		details = append(details, "expected_targets: "+strings.Join(nextTargets, ", "))
	}
	if trimmedReportPath := strings.TrimSpace(reportPath); trimmedReportPath != "" {
		details = append(details, "verification_report_path: "+trimmedReportPath)
	}
	if proposalID != "" {
		details = append(details, "proposal_id: "+proposalID)
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID:  e.sessionID,
		RunID:      strings.TrimSpace(envelope.runID),
		TaskID:     strings.TrimSpace(envelope.taskID),
		AvatarID:   strings.TrimSpace(envelope.avatarID),
		Tool:       strings.TrimSpace(toolName),
		Kind:       "runtime_lifecycle",
		Verdict:    "INFO",
		Cause:      "verifier_remediation_resume_attempt",
		Summary:    "Verifier remediation resume attempt recorded for reverify repair work.",
		Source:     "runtime",
		ReportPath: strings.TrimSpace(reportPath),
		Details:    details,
		UpdatedAt:  time.Now().UTC(),
	})
}

func verifierRemediationExecutionBoundary(records []memstore.EvaluationRecord) *memstore.RecoveryExecutionBoundary {
	boundary := memstore.LatestRecoveryExecutionBoundary(records, 10, "runtime_reverify_repair")
	if boundary == nil {
		return nil
	}
	if strings.TrimSpace(boundary.ActionKind) != "run_verifier_remediation_repair" {
		return nil
	}
	if strings.TrimSpace(boundary.RequiredAuthority) != "verifier_reverify" {
		return nil
	}
	if strings.TrimSpace(boundary.GuardStatus) != "guarded_ready" {
		return nil
	}
	if !boundary.VerifierRequired {
		return nil
	}
	return boundary
}

func payloadStringList(payload map[string]any, key string) []string {
	if payload == nil {
		return nil
	}
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	values := make([]string, 0)
	switch typed := raw.(type) {
	case []string:
		for _, value := range typed {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	case []any:
		for _, value := range typed {
			if trimmed := strings.TrimSpace(fmt.Sprint(value)); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	case string:
		for _, value := range strings.Split(typed, ",") {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	}
	return values
}

func isReverifyRepairTool(toolName string, input any) bool {
	switch strings.TrimSpace(toolName) {
	case "write", "patch":
		return true
	case "shell":
		_, shouldVerify := verificationTargetForTool(toolName, input)
		return shouldVerify
	default:
		return false
	}
}

func (e *Engine) maybeRunVerification(ctx context.Context, envelope toolCallEnvelope, toolName string, input any) error {
	if e == nil || e.verifier == nil {
		return nil
	}

	target, shouldVerify := verificationTargetForTool(toolName, input)
	if !shouldVerify {
		return nil
	}
	_, err := e.runVerification(ctx, envelope, toolName, target)
	return err
}

func (e *Engine) runVerification(ctx context.Context, envelope toolCallEnvelope, toolName string, target verificationTarget) (VerificationResult, error) {
	startedPayload := map[string]any{
		"trigger": target.trigger,
		"tool":    toolName,
	}
	for key, value := range toolNodeEvidenceForAvatarID(envelope.avatarID) {
		startedPayload[key] = value
	}
	if len(target.changedFiles) > 0 {
		startedPayload["changed_files"] = append([]string(nil), target.changedFiles...)
	}
	if target.commandSummary != "" {
		startedPayload["command"] = target.commandSummary
	}
	if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, "reviewing", "verification.started", "verifier", startedPayload, nil); err != nil {
		return VerificationResult{}, err
	}

	runner := e.verifier(target.workingDir, target.includeRace)
	// P14-4a: Inject pre-existing build error baseline so the verifier
	// can filter out errors that existed before this task ran.
	baseline := target.buildBaseline
	if len(baseline) == 0 {
		baseline = e.BuildBaseline()
	}
	if len(baseline) > 0 {
		if rb, ok := runner.(interface {
			WithBaseline(map[string]string) verification.Runner
		}); ok {
			updated := rb.WithBaseline(baseline)
			runner = &updated
		}
	}
	// R2-3: scope toolchain checks to the language(s) of changed files
	// (skills/docs-only writes → no go/python/rust suite).
	if len(target.changedFiles) > 0 {
		if sc, ok := runner.(interface{ ScopeToChangedFiles([]string) }); ok {
			sc.ScopeToChangedFiles(target.changedFiles)
		}
	}
	report := runner.Run(ctx)
	// Batch3/P10: never emit PASS when this run wrote sources but project
	// health (cross-lang compile) still fails.
	report = e.applyHonestVerificationGate(target.workingDir, report)
	for _, check := range report.Checks {
		payload := map[string]any{
			"tool":            toolName,
			"name":            check.Name,
			"command_run":     check.CommandRun,
			"expected":        check.Expected,
			"actual":          check.Actual,
			"result":          check.Result,
			"output_observed": check.OutputObserved,
		}
		for key, value := range toolNodeEvidenceForAvatarID(envelope.avatarID) {
			payload[key] = value
		}
		if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, "reviewing", "verification.check_completed", "verifier", payload, nil); err != nil {
			return VerificationResult{}, err
		}
	}

	completedPayload := map[string]any{
		"tool":    toolName,
		"verdict": report.Verdict,
		"summary": report.Summary,
	}
	for key, value := range toolNodeEvidenceForAvatarID(envelope.avatarID) {
		completedPayload[key] = value
	}
	if len(target.changedFiles) > 0 {
		completedPayload["changed_files"] = append([]string(nil), target.changedFiles...)
	}
	if target.commandSummary != "" {
		completedPayload["command"] = target.commandSummary
	}
	if len(report.Warnings) > 0 {
		completedPayload["warnings"] = append([]string(nil), report.Warnings...)
	}
	reportPath, err := e.persistVerificationReport(envelope, toolName, report)
	if err != nil {
		return VerificationResult{}, err
	}
	if reportPath != "" {
		completedPayload["report_path"] = reportPath
	}
	if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, "reviewing", "verification.completed", "verifier", completedPayload, nil); err != nil {
		return VerificationResult{}, err
	}
	result := VerificationResult{ReportPath: reportPath, Report: report}

	if report.Verdict == verification.VerdictFail {
		if _, err := e.emitVerifierRemediationPausePoint(envelope, toolName, target, report, reportPath); err != nil {
			return result, err
		}
		// Include detailed check output so the Builder can see WHAT failed.
		detail := formatVerificationFailureDetail(report)
		return result, fmt.Errorf("verification failed after %s: %s\n\n%s", toolName, report.Summary, detail)
	}
	return result, nil
}

func (e *Engine) emitVerifierRemediationPausePoint(envelope toolCallEnvelope, toolName string, target verificationTarget, report verification.Report, reportPath string) (PausePoint, error) {
	failedChecks := failedVerificationChecks(report)
	digest := verifierRemediationDigest(toolName, target, report, reportPath, failedChecks)
	point := NewPausePoint(PausePointKindVerifierRemediation, envelope.runID, envelope.taskID, "", "verifier:"+strings.TrimSpace(target.trigger), "", toolName, "verifier_remediation", digest)
	point.AvatarID = envelope.avatarID
	point.Phase = "reviewing"

	payload := point.Payload()
	payload["trigger"] = strings.TrimSpace(target.trigger)
	payload["verdict"] = report.Verdict
	payload["summary"] = strings.TrimSpace(report.Summary)
	payload["failed_checks"] = failedChecks
	if len(target.changedFiles) > 0 {
		payload["expected_targets"] = append([]string(nil), target.changedFiles...)
		payload["changed_files"] = append([]string(nil), target.changedFiles...)
	}
	if strings.TrimSpace(target.commandSummary) != "" {
		payload["command"] = strings.TrimSpace(target.commandSummary)
	}
	if strings.TrimSpace(reportPath) != "" {
		payload["report_path"] = strings.TrimSpace(reportPath)
	}
	for key, value := range toolNodeEvidenceForAvatarID(envelope.avatarID) {
		payload[key] = value
	}

	if err := e.emit(envelope.runID, envelope.taskID, envelope.avatarID, "reviewing", "verification.remediation_pause_point", "verifier", payload, nil); err != nil {
		return point, err
	}
	if err := e.recordVerifierRemediationPausePoint(point, envelope, toolName, target, report, reportPath, failedChecks); err != nil {
		return point, err
	}
	return point, nil
}

func failedVerificationChecks(report verification.Report) []map[string]any {
	failed := make([]map[string]any, 0)
	for _, check := range report.Checks {
		if check.Result != verification.VerdictFail {
			continue
		}
		failed = append(failed, map[string]any{
			"name":            check.Name,
			"command_run":     check.CommandRun,
			"expected":        check.Expected,
			"actual":          check.Actual,
			"output_observed": check.OutputObserved,
			"result":          check.Result,
		})
	}
	return failed
}

// formatVerificationFailureDetail extracts failed/passflicted check output for Builder retry feedback.
func formatVerificationFailureDetail(report verification.Report) string {
	var b strings.Builder
	b.WriteString("--- Verification Details ---\n")
	for _, check := range report.Checks {
		if check.Result == verification.VerdictPass {
			continue
		}
		b.WriteString(fmt.Sprintf("[%s] %s\n", check.Result, check.Name))
		if strings.TrimSpace(check.OutputObserved) != "" {
			b.WriteString(fmt.Sprintf("  output: %s\n", strings.TrimSpace(check.OutputObserved)))
		}
		if strings.TrimSpace(check.Actual) != "" && check.Actual != check.OutputObserved {
			b.WriteString(fmt.Sprintf("  error: %s\n", strings.TrimSpace(check.Actual)))
		}
	}
	return b.String()
}

func verifierRemediationDigest(toolName string, target verificationTarget, report verification.Report, reportPath string, failedChecks []map[string]any) string {
	payload := map[string]any{
		"tool":              strings.TrimSpace(toolName),
		"trigger":           strings.TrimSpace(target.trigger),
		"changed_files":     append([]string(nil), target.changedFiles...),
		"command":           strings.TrimSpace(target.commandSummary),
		"verdict":           report.Verdict,
		"summary":           strings.TrimSpace(report.Summary),
		"report_path":       strings.TrimSpace(reportPath),
		"failed_checks":     failedChecks,
		"warning_count":     len(report.Warnings),
		"total_check_count": len(report.Checks),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return strings.TrimSpace(toolName) + "|" + strings.TrimSpace(target.trigger) + "|" + string(report.Verdict)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (e *Engine) recordVerifierRemediationPausePoint(point PausePoint, envelope toolCallEnvelope, toolName string, target verificationTarget, report verification.Report, reportPath string, failedChecks []map[string]any) error {
	if e == nil || e.memory == nil {
		return nil
	}
	details := []string{
		"pause_point_id: " + strings.TrimSpace(point.ID),
		"pause_point_kind: " + string(PausePointKindVerifierRemediation),
		"tool: " + strings.TrimSpace(toolName),
		"operation: verifier_remediation",
		"trigger: " + strings.TrimSpace(target.trigger),
		"verdict: " + string(report.Verdict),
	}
	if strings.TrimSpace(reportPath) != "" {
		details = append(details, "report_path: "+strings.TrimSpace(reportPath))
	}
	if len(target.changedFiles) > 0 {
		details = append(details, "expected_targets: "+strings.Join(target.changedFiles, ", "))
	}
	if strings.TrimSpace(target.commandSummary) != "" {
		details = append(details, "command: "+strings.TrimSpace(target.commandSummary))
	}
	for _, check := range failedChecks {
		name, _ := check["name"].(string)
		commandRun, _ := check["command_run"].(string)
		details = append(details, "failed_check: "+strings.TrimSpace(name)+" => "+strings.TrimSpace(commandRun))
	}

	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID:  e.sessionID,
		RunID:      strings.TrimSpace(envelope.runID),
		TaskID:     strings.TrimSpace(envelope.taskID),
		AvatarID:   strings.TrimSpace(envelope.avatarID),
		Tool:       strings.TrimSpace(toolName),
		Kind:       "runtime_lifecycle",
		Verdict:    "INFO",
		Cause:      "verifier_remediation_pause_point",
		Summary:    fmt.Sprintf("Verifier %s opened remediation pause point for %s.", report.Verdict, strings.TrimSpace(toolName)),
		Source:     "verifier",
		ReportPath: strings.TrimSpace(reportPath),
		Details:    details,
		UpdatedAt:  time.Now().UTC(),
	})
}

func (e *Engine) persistVerificationReport(envelope toolCallEnvelope, toolName string, report verification.Report) (string, error) {
	if e == nil || e.memory == nil {
		return "", nil
	}
	var previousLatestNonPass *memstore.VerificationSnapshot
	if strings.TrimSpace(envelope.taskID) != "" {
		previousSnapshot, err := e.memory.LoadLatestTaskSnapshot(envelope.taskID)
		if err != nil {
			return "", err
		}
		previousLatestNonPass = memstore.LatestNonPassVerificationSnapshot(previousSnapshot.Verification, previousSnapshot.VerificationHistory)
	}
	recordedAt := time.Now().UTC()
	reportPath, err := e.writeVerificationArtifact(envelope, toolName, report, recordedAt)
	if err != nil {
		return "", err
	}
	checks := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		checks = append(checks, fmt.Sprintf("%s => %s", check.Name, check.Result))
	}
	err = e.memory.RecordVerification(memstore.VerificationRecord{
		SessionID:  e.sessionID,
		RunID:      envelope.runID,
		TaskID:     envelope.taskID,
		AvatarID:   envelope.avatarID,
		Tool:       toolName,
		Verdict:    string(report.Verdict),
		Summary:    report.Summary,
		ReportPath: reportPath,
		Warnings:   append([]string(nil), report.Warnings...),
		Checks:     checks,
		UpdatedAt:  recordedAt,
	})
	if err != nil {
		return "", err
	}
	for _, record := range evaluation.BuildVerificationRecords(toolName, reportPath, report) {
		details := append([]string(nil), record.Details...)
		details = append(details, toolNodeEvidenceDetailsForAvatarID(envelope.avatarID)...)
		if err := e.memory.RecordEvaluation(memstore.EvaluationRecord{
			SessionID:  e.sessionID,
			RunID:      envelope.runID,
			TaskID:     envelope.taskID,
			AvatarID:   envelope.avatarID,
			Tool:       record.Tool,
			Kind:       record.Kind,
			Verdict:    record.Verdict,
			Cause:      record.Cause,
			Summary:    record.Summary,
			Source:     record.Source,
			ReportPath: record.ReportPath,
			Details:    details,
			UpdatedAt:  recordedAt,
		}); err != nil {
			return "", err
		}
	}
	var records []memstore.EvaluationRecord
	if strings.TrimSpace(envelope.taskID) != "" {
		if snapshot, snapshotErr := e.memory.LoadLatestTaskSnapshot(envelope.taskID); snapshotErr == nil {
			records = snapshot.EvaluationRecords
		}
	}
	if closureRecord, ok := buildVerificationClosureRecord(toolName, reportPath, report, previousLatestNonPass, records); ok {
		details := append([]string(nil), closureRecord.Details...)
		details = append(details, toolNodeEvidenceDetailsForAvatarID(envelope.avatarID)...)
		if err := e.memory.RecordEvaluation(memstore.EvaluationRecord{
			SessionID:  e.sessionID,
			RunID:      envelope.runID,
			TaskID:     envelope.taskID,
			AvatarID:   envelope.avatarID,
			Tool:       closureRecord.Tool,
			Kind:       closureRecord.Kind,
			Verdict:    closureRecord.Verdict,
			Cause:      closureRecord.Cause,
			Summary:    closureRecord.Summary,
			Source:     closureRecord.Source,
			ReportPath: closureRecord.ReportPath,
			Details:    details,
			UpdatedAt:  recordedAt.Add(time.Nanosecond),
		}); err != nil {
			return "", err
		}
	}
	return reportPath, nil
}

func buildVerificationClosureRecord(toolName string, reportPath string, report verification.Report, previousLatestNonPass *memstore.VerificationSnapshot, records []memstore.EvaluationRecord) (evaluation.Record, bool) {
	if report.Verdict != verification.VerdictPass || previousLatestNonPass == nil || !memstore.IsNonPassVerificationVerdict(previousLatestNonPass.Verdict) {
		return evaluation.Record{}, false
	}
	previousTool := strings.TrimSpace(previousLatestNonPass.Tool)
	if previousTool == "" {
		previousTool = "n/a"
	}
	previousVerdict := strings.TrimSpace(strings.ToUpper(previousLatestNonPass.Verdict))
	if previousVerdict == "" {
		previousVerdict = "n/a"
	}
	details := []string{
		"covered_tool: " + previousTool,
		"covered_verdict: " + previousVerdict,
	}
	if strings.TrimSpace(previousLatestNonPass.ReportPath) != "" {
		details = append(details, "covered_report: "+strings.TrimSpace(previousLatestNonPass.ReportPath))
	}
	if attempt := memstore.LatestVerifierRemediationResumeAttemptEvaluation(records); attempt != nil {
		if pausePointID := memstore.EvaluationRecordPausePointID(*attempt); pausePointID != "" {
			details = append(details, "remediation_pause_point_id: "+pausePointID)
		}
		if attemptID := memstore.EvaluationRecordResumeAttemptID(*attempt); attemptID != "" {
			details = append(details, "remediation_resume_attempt_id: "+attemptID)
		}
		if targets := memstore.EvaluationRecordExpectedTargets(*attempt); len(targets) > 0 {
			details = append(details, "remediation_expected_targets: "+strings.Join(targets, ", "))
		}
	}
	for _, check := range report.Checks {
		details = append(details, fmt.Sprintf("current_check: %s => %s", check.Name, check.Result))
	}
	return evaluation.Record{
		Tool:       toolName,
		Kind:       "workflow_feedback",
		Verdict:    string(report.Verdict),
		Cause:      "verification_reverify_covered",
		Summary:    fmt.Sprintf("Verification PASS now covers the latest non-pass verification: %s via %s.", previousVerdict, previousTool),
		Source:     "verification_followup",
		ReportPath: reportPath,
		Details:    details,
	}, true
}

func (e *Engine) writeVerificationArtifact(envelope toolCallEnvelope, toolName string, report verification.Report, recordedAt time.Time) (string, error) {
	if e == nil || e.memory == nil {
		return "", nil
	}
	memoryPath := e.memory.Path()
	if strings.TrimSpace(memoryPath) == "" {
		return "", nil
	}
	artifactDir := filepath.Join(filepath.Dir(memoryPath), "verifier")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", fmt.Errorf("create verifier artifact directory: %w", err)
	}
	reportPath := filepath.Join(artifactDir, fmt.Sprintf("%s.json", sanitizeArtifactName(fmt.Sprintf("%s-%s", envelope.runID, toolName))))
	artifact := struct {
		SessionID   string              `json:"session_id"`
		RunID       string              `json:"run_id"`
		TaskID      string              `json:"task_id"`
		AvatarID    string              `json:"avatar_id,omitempty"`
		Tool        string              `json:"tool"`
		GeneratedAt time.Time           `json:"generated_at"`
		Report      verification.Report `json:"report"`
	}{
		SessionID:   e.sessionID,
		RunID:       envelope.runID,
		TaskID:      envelope.taskID,
		AvatarID:    envelope.avatarID,
		Tool:        toolName,
		GeneratedAt: recordedAt,
		Report:      report,
	}
	content, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal verifier artifact: %w", err)
	}
	if err := os.WriteFile(reportPath, append(content, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("write verifier artifact: %w", err)
	}
	return reportPath, nil
}

func sanitizeArtifactName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "verification-report"
	}
	replacer := strings.NewReplacer("\\", "-", "/", "-", ":", "-", " ", "-")
	sanitized := strings.Trim(replacer.Replace(trimmed), "-.")
	if sanitized == "" {
		return "verification-report"
	}
	return sanitized
}

// rewriteWriteToolInput lifts write paths to the on-disk location before
// tool.requested/completed fire (F105). remapped_from is event metadata only —
// never prepended onto the LLM stable prefix (prefix-cache).
func (e *Engine) rewriteWriteToolInput(input any) (any, string) {
	if e == nil {
		return input, ""
	}
	wd := strings.TrimSpace(e.projectRoot)
	if wd == "" {
		wd, _ = os.Getwd()
	}
	hint := e.layoutTaskHint
	switch v := input.(type) {
	case tools.WriteInput:
		final, from := applyWritePathRewrite(wd, hint, v.Path, v.Content)
		if final != "" {
			v.Path = final
		}
		return v, from
	case *tools.WriteInput:
		if v != nil {
			final, from := applyWritePathRewrite(wd, hint, v.Path, v.Content)
			if final != "" {
				v.Path = final
			}
			return v, from
		}
	}
	return input, ""
}

func toolPayload(actionID string, toolName string, operation string, input any, permissionMode PermissionMode) map[string]any {
	payload := map[string]any{"tool": toolName, "operation": operation}
	if strings.TrimSpace(actionID) != "" {
		payload["action_id"] = actionID
	}
	if approvalKey := toolApprovalKey(toolName, operation, input); approvalKey != "" {
		payload["approval_key"] = approvalKey
	}
	actionArgs := make(map[string]any)
	constraints := make(map[string]any)
	switch value := input.(type) {
	case tools.ReadInput:
		payload["path"] = value.Path
		actionArgs["path"] = value.Path
		constraints["readonly"] = true
	case *tools.ReadInput:
		if value != nil {
			payload["path"] = value.Path
			actionArgs["path"] = value.Path
			constraints["readonly"] = true
		}
	case tools.ShellInput:
		payload["command"] = strings.Join(value.Command, " ")
		payload["working_dir"] = value.WorkingDir
		payload["timeout_ms"] = value.TimeoutMillis
		actionArgs["command"] = append([]string(nil), value.Command...)
		constraints["working_dir"] = value.WorkingDir
		constraints["timeout_ms"] = value.TimeoutMillis
		constraints["readonly"] = shellCommandLooksReadOnly(value.Command)
		if proposalID := strings.TrimSpace(value.ProposalID); proposalID != "" {
			payload["proposal_id"] = proposalID
			actionArgs["proposal_id"] = proposalID
		}
		if intent := strings.TrimSpace(value.Intent); intent != "" {
			payload["intent"] = intent
			actionArgs["intent"] = intent
		}
		if len(value.ExpectedTargets) > 0 {
			payload["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
			actionArgs["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
		}
	case *tools.ShellInput:
		if value != nil {
			payload["command"] = strings.Join(value.Command, " ")
			payload["working_dir"] = value.WorkingDir
			payload["timeout_ms"] = value.TimeoutMillis
			actionArgs["command"] = append([]string(nil), value.Command...)
			constraints["working_dir"] = value.WorkingDir
			constraints["timeout_ms"] = value.TimeoutMillis
			constraints["readonly"] = shellCommandLooksReadOnly(value.Command)
			if proposalID := strings.TrimSpace(value.ProposalID); proposalID != "" {
				payload["proposal_id"] = proposalID
				actionArgs["proposal_id"] = proposalID
			}
			if intent := strings.TrimSpace(value.Intent); intent != "" {
				payload["intent"] = intent
				actionArgs["intent"] = intent
			}
			if len(value.ExpectedTargets) > 0 {
				payload["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
				actionArgs["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
			}
		}
	case tools.WriteInput:
		payload["path"] = value.Path
		payload["working_dir"] = value.WorkingDir
		payload["overwrite"] = value.Overwrite
		payload["content_length"] = len(value.Content)
		actionArgs["path"] = value.Path
		actionArgs["overwrite"] = value.Overwrite
		actionArgs["content_length"] = len(value.Content)
		constraints["working_dir"] = value.WorkingDir
		constraints["readonly"] = false
		if proposalID := strings.TrimSpace(value.ProposalID); proposalID != "" {
			payload["proposal_id"] = proposalID
			actionArgs["proposal_id"] = proposalID
		}
		if intent := strings.TrimSpace(value.Intent); intent != "" {
			payload["intent"] = intent
			actionArgs["intent"] = intent
		}
		if len(value.ExpectedTargets) > 0 {
			payload["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
			actionArgs["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
		}
	case *tools.WriteInput:
		if value != nil {
			payload["path"] = value.Path
			payload["working_dir"] = value.WorkingDir
			payload["overwrite"] = value.Overwrite
			payload["content_length"] = len(value.Content)
			actionArgs["path"] = value.Path
			actionArgs["overwrite"] = value.Overwrite
			actionArgs["content_length"] = len(value.Content)
			constraints["working_dir"] = value.WorkingDir
			constraints["readonly"] = false
			if proposalID := strings.TrimSpace(value.ProposalID); proposalID != "" {
				payload["proposal_id"] = proposalID
				actionArgs["proposal_id"] = proposalID
			}
			if intent := strings.TrimSpace(value.Intent); intent != "" {
				payload["intent"] = intent
				actionArgs["intent"] = intent
			}
			if len(value.ExpectedTargets) > 0 {
				payload["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
				actionArgs["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
			}
		}
	case tools.PatchInput:
		payload["path"] = value.Path
		payload["working_dir"] = value.WorkingDir
		payload["replace_all"] = value.ReplaceAll
		payload["old_length"] = len(value.Old)
		payload["new_length"] = len(value.New)
		actionArgs["path"] = value.Path
		actionArgs["replace_all"] = value.ReplaceAll
		actionArgs["old_length"] = len(value.Old)
		actionArgs["new_length"] = len(value.New)
		constraints["working_dir"] = value.WorkingDir
		constraints["readonly"] = false
		if proposalID := strings.TrimSpace(value.ProposalID); proposalID != "" {
			payload["proposal_id"] = proposalID
			actionArgs["proposal_id"] = proposalID
		}
		if intent := strings.TrimSpace(value.Intent); intent != "" {
			payload["intent"] = intent
			actionArgs["intent"] = intent
		}
		if len(value.ExpectedTargets) > 0 {
			payload["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
			actionArgs["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
		}
	case *tools.PatchInput:
		if value != nil {
			payload["path"] = value.Path
			payload["working_dir"] = value.WorkingDir
			payload["replace_all"] = value.ReplaceAll
			payload["old_length"] = len(value.Old)
			payload["new_length"] = len(value.New)
			actionArgs["path"] = value.Path
			actionArgs["replace_all"] = value.ReplaceAll
			actionArgs["old_length"] = len(value.Old)
			actionArgs["new_length"] = len(value.New)
			constraints["working_dir"] = value.WorkingDir
			constraints["readonly"] = false
			if proposalID := strings.TrimSpace(value.ProposalID); proposalID != "" {
				payload["proposal_id"] = proposalID
				actionArgs["proposal_id"] = proposalID
			}
			if intent := strings.TrimSpace(value.Intent); intent != "" {
				payload["intent"] = intent
				actionArgs["intent"] = intent
			}
			if len(value.ExpectedTargets) > 0 {
				payload["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
				actionArgs["expected_targets"] = append([]string(nil), value.ExpectedTargets...)
			}
		}
	case tools.GitInput:
		payload["args"] = strings.Join(value.Args, " ")
		payload["working_dir"] = value.WorkingDir
		payload["timeout_ms"] = value.TimeoutMillis
		actionArgs["args"] = append([]string(nil), value.Args...)
		constraints["working_dir"] = value.WorkingDir
		constraints["timeout_ms"] = value.TimeoutMillis
		constraints["readonly"] = true
	case *tools.GitInput:
		if value != nil {
			payload["args"] = strings.Join(value.Args, " ")
			payload["working_dir"] = value.WorkingDir
			payload["timeout_ms"] = value.TimeoutMillis
			actionArgs["args"] = append([]string(nil), value.Args...)
			constraints["working_dir"] = value.WorkingDir
			constraints["timeout_ms"] = value.TimeoutMillis
			constraints["readonly"] = true
		}
	}
	if target, shouldVerify := verificationTargetForTool(toolName, input); shouldVerify && len(target.changedFiles) > 0 {
		payload["changed_files"] = append([]string(nil), target.changedFiles...)
		actionArgs["changed_files"] = append([]string(nil), target.changedFiles...)
	}
	if len(actionArgs) > 0 || len(constraints) > 0 || strings.TrimSpace(actionID) != "" {
		action := map[string]any{"tool": toolName, "operation": operation}
		if strings.TrimSpace(actionID) != "" {
			action["action_id"] = actionID
		}
		if len(actionArgs) > 0 {
			action["args"] = actionArgs
		}
		if len(constraints) > 0 {
			payload["constraints"] = constraints
			action["constraints"] = constraints
		}
		payload["action"] = action
	}
	payload["policy"] = toolPolicyPayload(toolName, input, permissionMode)
	return payload
}

func toolPolicyPayload(toolName string, input any, permissionMode PermissionMode) map[string]any {
	riskLevel := toolRiskLevel(toolName)
	readonly := toolIsReadOnly(toolName, input)
	_, verifierRequired := verificationTargetForTool(toolName, input)
	decision, decisionSource, modeRationale := permissionModePolicyDecision(permissionMode, toolName, input)
	rationale := toolPolicyRationale(toolName, riskLevel, readonly, verifierRequired)
	if strings.TrimSpace(modeRationale) != "" {
		if decision == "allow" {
			rationale = strings.TrimSpace(modeRationale + " " + rationale)
		} else {
			rationale = modeRationale
		}
	}
	policy := map[string]any{
		"decision":          decision,
		"decision_source":   decisionSource,
		"permission_mode":   string(normalizePermissionMode(permissionMode)),
		"risk_level":        riskLevel,
		"readonly":          readonly,
		"verifier_required": verifierRequired,
	}
	policy["rationale"] = rationale
	return policy
}

func (e *Engine) resolveToolApprovalDecision(taskID string, payload map[string]any) map[string]any {
	if e == nil || e.memory == nil {
		return payload
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	if trimmedTaskID == "" {
		return payload
	}
	policy, _ := payload["policy"].(map[string]any)
	if policy == nil || strings.TrimSpace(fmt.Sprint(policy["decision"])) != "ask" {
		return payload
	}
	approvalKey := strings.TrimSpace(fmt.Sprint(payload["approval_key"]))
	if approvalKey == "" {
		return payload
	}
	snapshot, err := e.memory.LoadLatestTaskSnapshot(trimmedTaskID)
	if err != nil {
		return payload
	}
	decision := memstore.LatestToolApprovalDecision(snapshot.EvaluationRecords, approvalKey)
	if decision == nil {
		return payload
	}
	policy = clonePolicyPayload(policy)
	switch strings.TrimSpace(decision.Cause) {
	case "tool_approval_granted":
		policy["decision"] = "allow"
		policy["decision_source"] = "task_approval_record"
		policy["rationale"] = strings.TrimSpace(decision.Summary)
	case "tool_approval_denied":
		policy["decision"] = "deny"
		policy["decision_source"] = "task_approval_record"
		policy["rationale"] = strings.TrimSpace(decision.Summary)
	default:
		return payload
	}
	payload["policy"] = policy
	return payload
}

func clonePolicyPayload(policy map[string]any) map[string]any {
	if policy == nil {
		return nil
	}
	cloned := make(map[string]any, len(policy))
	for key, value := range policy {
		cloned[key] = value
	}
	return cloned
}

func toolApprovalKey(toolName string, operation string, input any) string {
	builder := strings.Builder{}
	builder.WriteString(strings.TrimSpace(toolName))
	builder.WriteString("|")
	builder.WriteString(strings.TrimSpace(operation))
	appendValue := func(value string) {
		builder.WriteString("|")
		builder.WriteString(strings.TrimSpace(value))
	}
	appendList := func(values []string) {
		cleaned := make([]string, 0, len(values))
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				cleaned = append(cleaned, trimmed)
			}
		}
		appendValue(strings.Join(cleaned, ","))
	}
	appendDigest := func(value string) {
		sum := sha256.Sum256([]byte(value))
		appendValue(hex.EncodeToString(sum[:]))
	}
	switch value := input.(type) {
	case tools.ReadInput:
		appendValue(value.Path)
	case *tools.ReadInput:
		if value != nil {
			appendValue(value.Path)
		}
	case tools.ShellInput:
		appendValue(strings.Join(value.Command, " "))
		appendValue(value.WorkingDir)
		appendValue(fmt.Sprintf("%d", value.TimeoutMillis))
		appendValue(value.ProposalID)
		appendValue(value.Intent)
		appendList(value.ExpectedTargets)
	case *tools.ShellInput:
		if value != nil {
			appendValue(strings.Join(value.Command, " "))
			appendValue(value.WorkingDir)
			appendValue(fmt.Sprintf("%d", value.TimeoutMillis))
			appendValue(value.ProposalID)
			appendValue(value.Intent)
			appendList(value.ExpectedTargets)
		}
	case tools.WriteInput:
		appendValue(value.Path)
		appendValue(value.WorkingDir)
		appendValue(fmt.Sprintf("%t", value.Overwrite))
		appendValue(value.ProposalID)
		appendValue(value.Intent)
		appendList(value.ExpectedTargets)
		appendDigest(value.Content)
	case *tools.WriteInput:
		if value != nil {
			appendValue(value.Path)
			appendValue(value.WorkingDir)
			appendValue(fmt.Sprintf("%t", value.Overwrite))
			appendValue(value.ProposalID)
			appendValue(value.Intent)
			appendList(value.ExpectedTargets)
			appendDigest(value.Content)
		}
	case tools.PatchInput:
		appendValue(value.Path)
		appendValue(value.WorkingDir)
		appendValue(fmt.Sprintf("%t", value.ReplaceAll))
		appendValue(value.ProposalID)
		appendValue(value.Intent)
		appendList(value.ExpectedTargets)
		appendDigest(value.Old)
		appendDigest(value.New)
	case *tools.PatchInput:
		if value != nil {
			appendValue(value.Path)
			appendValue(value.WorkingDir)
			appendValue(fmt.Sprintf("%t", value.ReplaceAll))
			appendValue(value.ProposalID)
			appendValue(value.Intent)
			appendList(value.ExpectedTargets)
			appendDigest(value.Old)
			appendDigest(value.New)
		}
	case tools.GitInput:
		appendValue(strings.Join(value.Args, " "))
		appendValue(value.WorkingDir)
		appendValue(fmt.Sprintf("%d", value.TimeoutMillis))
	case *tools.GitInput:
		if value != nil {
			appendValue(strings.Join(value.Args, " "))
			appendValue(value.WorkingDir)
			appendValue(fmt.Sprintf("%d", value.TimeoutMillis))
		}
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func toolPermissionModeError(payload map[string]any) (error, bool) {
	policy, _ := payload["policy"].(map[string]any)
	if policy == nil {
		return nil, false
	}
	decision := strings.TrimSpace(fmt.Sprint(policy["decision"]))
	if decision != "deny" && decision != "ask" {
		return nil, false
	}
	rationale := strings.TrimSpace(fmt.Sprint(policy["rationale"]))
	if rationale == "" {
		rationale = "request blocked by the current permission mode"
	}
	if decision == "ask" {
		return fmt.Errorf("approval required: %s", rationale), true
	}
	return fmt.Errorf("permission denied: %s", rationale), false
}

func applyCodingPreflightPolicy(payload map[string]any, envelope toolCallEnvelope, toolName string, input any, permissionMode PermissionMode) error {
	if normalizePermissionMode(permissionMode) == PermissionModeBypassPermissions {
		return nil
	}
	if !workflowBuilderMutationNeedsPreflight(envelope, toolName, input) {
		return nil
	}
	missing := codingPreflightMissingFields(payload)
	if len(missing) == 0 {
		return nil
	}
	policy, _ := payload["policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	// P13-1: acceptEdits means the user trusts avatars to edit their codebase.
	// Downgrade missing preflight metadata from "deny" to "warn" — record the
	// gap for audit but allow the write to proceed. The user's explicit choice
	// of acceptEdits mode takes precedence over preflight completeness.
	if normalizePermissionMode(permissionMode) == PermissionModeAcceptEdits {
		policy["decision"] = "allow"
		policy["decision_source"] = "runtime_coding_preflight_warn"
		policy["rationale"] = fmt.Sprintf("acceptEdits: Builder write allowed despite missing preflight metadata: %s.", strings.Join(missing, ", "))
		payload["policy"] = policy
		return nil
	}
	policy["decision"] = "deny"
	policy["decision_source"] = "runtime_coding_preflight"
	policy["rationale"] = fmt.Sprintf("Builder mutating tool calls require coding preflight metadata before execution: missing %s.", strings.Join(missing, ", "))
	payload["policy"] = policy
	return fmt.Errorf("permission denied: %s", strings.TrimSpace(fmt.Sprint(policy["rationale"])))
}

func workflowBuilderMutationNeedsPreflight(envelope toolCallEnvelope, toolName string, input any) bool {
	if strings.TrimSpace(envelope.avatarID) != "avatar-builder" {
		return false
	}
	switch strings.TrimSpace(toolName) {
	case "write", "patch":
		return true
	case "shell":
		_, shouldVerify := verificationTargetForTool(toolName, input)
		return shouldVerify
	default:
		return false
	}
}

func codingPreflightMissingFields(payload map[string]any) []string {
	missing := make([]string, 0, 2)
	if payloadTrimmedString(payload, "intent") == "" {
		missing = append(missing, "intent")
	}
	if len(payloadStringList(payload, "expected_targets")) == 0 {
		missing = append(missing, "expected_targets")
	}
	return missing
}

func payloadTrimmedString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func toolRiskLevel(toolName string) string {
	switch strings.TrimSpace(toolName) {
	case "read":
		return "safe"
	case "shell", "write", "patch", "git":
		return "guarded"
	default:
		return "guarded"
	}
}

func toolIsReadOnly(toolName string, input any) bool {
	name := strings.TrimSpace(toolName)
	if tools.IsMCPToolName(name) {
		return mcpToolLooksReadOnly(name)
	}
	switch name {
	case "read":
		return true
	case "git":
		return true
	case "shell":
		switch value := input.(type) {
		case tools.ShellInput:
			return shellCommandLooksReadOnly(value.Command)
		case *tools.ShellInput:
			if value != nil {
				return shellCommandLooksReadOnly(value.Command)
			}
		}
	}
	return false
}

func toolMutatesCodeTarget(toolName string, input any) bool {
	switch strings.TrimSpace(toolName) {
	case "write":
		switch value := input.(type) {
		case tools.WriteInput:
			return pathLooksCodeTarget(value.Path)
		case *tools.WriteInput:
			return value != nil && pathLooksCodeTarget(value.Path)
		}
	case "patch":
		switch value := input.(type) {
		case tools.PatchInput:
			return pathLooksCodeTarget(value.Path)
		case *tools.PatchInput:
			return value != nil && pathLooksCodeTarget(value.Path)
		}
	case "shell":
		if target, shouldVerify := verificationTargetForTool(toolName, input); shouldVerify {
			for _, changedFile := range target.changedFiles {
				if pathLooksCodeTarget(changedFile) {
					return true
				}
			}
		}
	}
	return false
}

func pathLooksCodeTarget(path string) bool {
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(path)))
	switch ext {
	case ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".java", ".kt", ".rs", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".php", ".rb", ".swift", ".m", ".mm", ".sh", ".ps1", ".bat", ".cmd", ".sql", ".html", ".css", ".scss", ".vue", ".svelte", ".xml", ".csv", ".ini", ".cfg", ".env":
		return true
	default:
		return false
	}
}

func toolPolicyRationale(toolName string, riskLevel string, readonly bool, verifierRequired bool) string {
	base := fmt.Sprintf("Runtime builtin policy allowed %s as a %s tool action.", strings.TrimSpace(toolName), riskLevel)
	if readonly {
		base = fmt.Sprintf("%s The action is bounded as read-only within the current sandbox.", base)
	} else {
		base = fmt.Sprintf("%s The action is allowed to mutate sandboxed workspace state.", base)
	}
	if verifierRequired {
		return fmt.Sprintf("%s Independent verification is required after completion.", base)
	}
	return fmt.Sprintf("%s Independent verification is not required for this action.", base)
}

func mergeToolErrorPayload(payload map[string]any, err error) map[string]any {
	failed := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		failed[key] = value
	}
	failed["error"] = err.Error()
	return failed
}

func mergeToolDeniedPayload(payload map[string]any, err error) map[string]any {
	denied := mergeToolErrorPayload(payload, err)
	policy, _ := denied["policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	policy["decision"] = "deny"
	policy["decision_source"] = "tool_sandbox"
	policy["rationale"] = toolPolicyDenialRationale(denied["tool"], err)
	denied["policy"] = policy
	return denied
}

func toolPolicyDenialRationale(toolName any, err error) string {
	label := strings.TrimSpace(fmt.Sprint(toolName))
	if label == "" {
		label = "tool"
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "request violates the tool sandbox or write policy"
	}
	return fmt.Sprintf("Tool sandbox denied %s because %s.", label, message)
}

func isToolPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	return strings.Contains(message, "permission denied") ||
		strings.Contains(message, "denied by sandbox") ||
		strings.Contains(message, "escapes sandbox") ||
		strings.Contains(message, "refuses to overwrite")
}

func summarizeToolInput(toolName string, operation string, input any) string {
	switch toolName {
	case "read":
		switch v := input.(type) {
		case tools.ReadInput:
			return fmt.Sprintf("read %s", v.Path)
		case *tools.ReadInput:
			if v != nil {
				return fmt.Sprintf("read %s", v.Path)
			}
		}
	case "write":
		switch v := input.(type) {
		case tools.WriteInput:
			return fmt.Sprintf("write %s (%d bytes)", v.Path, len(v.Content))
		case *tools.WriteInput:
			if v != nil {
				return fmt.Sprintf("write %s (%d bytes)", v.Path, len(v.Content))
			}
		}
	case "patch":
		switch v := input.(type) {
		case tools.PatchInput:
			return fmt.Sprintf("patch %s (old=%d, new=%d bytes)", v.Path, len(v.Old), len(v.New))
		case *tools.PatchInput:
			if v != nil {
				return fmt.Sprintf("patch %s (old=%d, new=%d bytes)", v.Path, len(v.Old), len(v.New))
			}
		}
	case "shell":
		switch v := input.(type) {
		case tools.ShellInput:
			cmd := strings.Join(v.Command, " ")
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			return fmt.Sprintf("shell: %s", cmd)
		case *tools.ShellInput:
			if v != nil {
				cmd := strings.Join(v.Command, " ")
				if len(cmd) > 120 {
					cmd = cmd[:120] + "..."
				}
				return fmt.Sprintf("shell: %s", cmd)
			}
		}
	case "git":
		switch v := input.(type) {
		case tools.GitInput:
			return fmt.Sprintf("git %s", strings.Join(v.Args, " "))
		case *tools.GitInput:
			if v != nil {
				return fmt.Sprintf("git %s", strings.Join(v.Args, " "))
			}
		}
	}
	return fmt.Sprintf("%s %s", toolName, operation)
}

func summarizeToolResult(toolName string, operation string, input any, content string) string {
	if toolName == "read" {
		switch value := input.(type) {
		case tools.ReadInput:
			return summarizeReadResult(value.Path, content)
		case *tools.ReadInput:
			if value != nil {
				return summarizeReadResult(value.Path, content)
			}
		}
	}

	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return fmt.Sprintf("%s %s completed successfully.", toolName, operation)
	}
	lines := strings.Split(trimmed, "\n")
	preview := strings.TrimSpace(lines[0])
	if len(preview) > 160 {
		preview = preview[:160] + "..."
	}
	if len(lines) > 1 {
		return fmt.Sprintf("%s (+%d more lines)", preview, len(lines)-1)
	}
	return preview
}

func verificationTargetForTool(toolName string, input any) (verificationTarget, bool) {
	switch toolName {
	case "write":
		switch value := input.(type) {
		case tools.WriteInput:
			return verificationPathContext(value.WorkingDir, value.Path, value.Intent)
		case *tools.WriteInput:
			if value != nil {
				return verificationPathContext(value.WorkingDir, value.Path, value.Intent)
			}
		}
	case "patch":
		switch value := input.(type) {
		case tools.PatchInput:
			return verificationPathContext(value.WorkingDir, value.Path)
		case *tools.PatchInput:
			if value != nil {
				return verificationPathContext(value.WorkingDir, value.Path)
			}
		}
	case "shell":
		switch value := input.(type) {
		case tools.ShellInput:
			return verificationShellContext(value)
		case *tools.ShellInput:
			if value != nil {
				return verificationShellContext(*value)
			}
		}
	}
	return verificationTarget{}, false
}

func verificationPathContext(workingDir string, path string, intent ...string) (verificationTarget, bool) {
	trimmedDir := strings.TrimSpace(workingDir)
	trimmedPath := strings.TrimSpace(path)
	if trimmedDir == "" || trimmedPath == "" {
		return verificationTarget{}, false
	}
	resolvedDir, err := filepath.Abs(filepath.Clean(trimmedDir))
	if err != nil {
		return verificationTarget{}, false
	}
	resolvedPath := trimmedPath
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(resolvedDir, resolvedPath)
	}
	resolvedPath, err = filepath.Abs(filepath.Clean(resolvedPath))
	if err != nil {
		return verificationTarget{}, false
	}
	// Protected workflow SoT must never trigger write-verification ceremonies.
	base := strings.ToLower(filepath.Base(resolvedPath))
	switch base {
	case "avatars_todo.md", "avatars_plan.md", "process_record.md", "process_record.yaml":
		return verificationTarget{}, false
	}
	// R2-3 / H10′: skill / workflow-doc writes are not language delivery —
	// do not run go/python/rust suites (that aborted confirm+implement mid-skill).
	intentJoined := strings.ToLower(strings.Join(intent, " "))
	slash := strings.ToLower(filepath.ToSlash(resolvedPath))
	if strings.Contains(intentJoined, "skill") ||
		strings.Contains(slash, "/skills/") ||
		strings.Contains(slash, "/docs/workflow/") {
		return verificationTarget{}, false
	}
	// Guarded writes always require the verification event contract when a
	// WorkingDir is provided — including non-code files (note.txt) and dirs
	// without go.mod. Do not require a parent go.mod (nested NL workspaces).
	return verificationTarget{workingDir: resolvedDir, changedFiles: []string{resolvedPath}, trigger: "tool_file_edit"}, true
}

func verificationShellContext(input tools.ShellInput) (verificationTarget, bool) {
	trimmedDir := strings.TrimSpace(input.WorkingDir)
	if trimmedDir == "" || len(input.Command) == 0 {
		return verificationTarget{}, false
	}
	resolvedDir, err := filepath.Abs(filepath.Clean(trimmedDir))
	if err != nil {
		return verificationTarget{}, false
	}
	if !hasLocalVerificationWorkspace(resolvedDir) {
		return verificationTarget{}, false
	}
	if shellCommandLooksReadOnly(input.Command) {
		return verificationTarget{}, false
	}
	return verificationTarget{
		workingDir:     resolvedDir,
		changedFiles:   shellChangedFiles(resolvedDir, input.Command),
		trigger:        "tool_shell_guarded",
		commandSummary: strings.Join(input.Command, " "),
	}, true
}

// hasLocalVerificationWorkspace reports a language manifest in workingDir only
// (never parent directories — nested workspaces otherwise inherit avatars go.mod).
func hasLocalVerificationWorkspace(workingDir string) bool {
	for _, name := range []string{"go.mod", "go.work", "package.json", "Cargo.toml", "pyproject.toml", "requirements.txt"} {
		if fileExists(filepath.Join(workingDir, name)) {
			return true
		}
	}
	return false
}

// hasGoVerificationWorkspace reports a Go module in workingDir only (no parent walk).
func hasGoVerificationWorkspace(workingDir string) bool {
	trimmedDir := strings.TrimSpace(workingDir)
	if trimmedDir == "" {
		return false
	}
	current, err := filepath.Abs(filepath.Clean(trimmedDir))
	if err != nil {
		return false
	}
	return fileExists(filepath.Join(current, "go.mod")) || fileExists(filepath.Join(current, "go.work"))
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func shellChangedFiles(workingDir string, command []string) []string {
	if strings.TrimSpace(workingDir) == "" || len(command) == 0 {
		return nil
	}
	executable := normalizedExecutable(command[0])
	switch executable {
	case "cmd":
		if script, ok := extractCmdWrapperScript(command[1:]); ok {
			return shellChangedFilesFromScript(workingDir, script)
		}
	case "pwsh", "powershell":
		if script, ok := extractPowerShellScript(command[1:]); ok {
			return shellChangedFilesFromScript(workingDir, script)
		}
	}
	return shellChangedFilesFromCommand(workingDir, executable, command[1:])
}

func shellChangedFilesFromScript(workingDir string, script string) []string {
	candidates := make([]string, 0, 4)
	for _, match := range shellRedirectionPattern.FindAllStringSubmatch(script, -1) {
		if len(match) < 2 {
			continue
		}
		candidates = append(candidates, match[1])
	}
	fields := strings.Fields(script)
	if len(fields) > 0 {
		candidates = append(candidates, shellChangedFileCandidates(normalizedExecutable(fields[0]), fields[1:])...)
	}
	return resolveShellChangedFiles(workingDir, candidates)
}

func shellChangedFilesFromCommand(workingDir string, executable string, args []string) []string {
	return resolveShellChangedFiles(workingDir, shellChangedFileCandidates(executable, args))
}

func shellChangedFileCandidates(executable string, args []string) []string {
	if len(args) == 0 {
		return nil
	}
	switch executable {
	case "cp", "copy", "copy-item", "mv", "move", "move-item", "rename-item", "ren", "rename":
		if target := lastNonFlagShellArg(args); target != "" {
			return []string{target}
		}
	case "rm", "del", "erase", "remove-item", "touch", "mkdir", "new-item", "clear-content":
		return nonFlagShellArgs(args)
	case "set-content", "add-content", "out-file", "set-item":
		if target := shellFlagValue(args, "-path", "-literalpath", "-filepath"); target != "" {
			return []string{target}
		}
		if target := lastNonFlagShellArg(args); target != "" {
			return []string{target}
		}
	case "gofmt":
		if !containsExactArg(args, "-w") {
			return nil
		}
		return nonFlagShellArgs(args)
	}
	return nil
}

func resolveShellChangedFiles(workingDir string, candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		trimmed := normalizeShellPathCandidate(candidate)
		if trimmed == "" {
			continue
		}
		absolute := trimmed
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(workingDir, absolute)
		}
		absolute, err := filepath.Abs(filepath.Clean(absolute))
		if err != nil {
			continue
		}
		if _, ok := seen[absolute]; ok {
			continue
		}
		seen[absolute] = struct{}{}
		resolved = append(resolved, absolute)
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

func normalizeShellPathCandidate(candidate string) string {
	trimmed := strings.TrimSpace(candidate)
	trimmed = strings.Trim(trimmed, "\"'`()[]{}")
	trimmed = strings.TrimRight(trimmed, ",;)")
	if trimmed == "" || trimmed == "." {
		return ""
	}
	return trimmed
}

func lastNonFlagShellArg(args []string) string {
	for index := len(args) - 1; index >= 0; index-- {
		candidate := normalizeShellPathCandidate(args[index])
		if candidate == "" || strings.HasPrefix(candidate, "-") {
			continue
		}
		return candidate
	}
	return ""
}

func nonFlagShellArgs(args []string) []string {
	paths := make([]string, 0, len(args))
	for _, arg := range args {
		candidate := normalizeShellPathCandidate(arg)
		if candidate == "" || strings.HasPrefix(candidate, "-") {
			continue
		}
		paths = append(paths, candidate)
	}
	return paths
}

func shellFlagValue(args []string, names ...string) string {
	for index := 0; index < len(args)-1; index++ {
		current := strings.ToLower(strings.TrimSpace(args[index]))
		for _, name := range names {
			if current == name {
				return normalizeShellPathCandidate(args[index+1])
			}
		}
	}
	return ""
}

func containsExactArg(args []string, value string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == value {
			return true
		}
	}
	return false
}

func shellCommandLooksReadOnly(command []string) bool {
	if len(command) == 0 {
		return false
	}
	executable := normalizedExecutable(command[0])
	args := command[1:]

	switch executable {
	case "go":
		return isReadOnlyGoCommand(args)
	case "git":
		return isReadOnlyGitCommand(args)
	case "rg", "cat", "type", "dir", "ls", "pwd", "echo":
		return true
	case "get-childitem", "get-location", "write-output":
		return true
	case "cmd":
		script, ok := extractCmdWrapperScript(args)
		if !ok {
			return false
		}
		return scriptLooksReadOnly(script)
	case "pwsh", "powershell":
		script, ok := extractPowerShellScript(args)
		if !ok {
			return false
		}
		return scriptLooksReadOnly(script)
	default:
		return false
	}
}

func normalizedExecutable(executable string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(executable)))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func isReadOnlyGoCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	subcommand := strings.ToLower(strings.TrimSpace(args[0]))
	switch subcommand {
	case "test", "vet", "env", "list", "version":
		return true
	default:
		return false
	}
}

func isReadOnlyGitCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			continue
		}
		switch strings.ToLower(trimmed) {
		case "status", "diff", "log", "show", "rev-parse":
			return true
		default:
			return false
		}
	}
	return false
}

func extractCmdWrapperScript(args []string) (string, bool) {
	for index, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "/c", "/k":
			script := strings.TrimSpace(strings.Join(args[index+1:], " "))
			return script, script != ""
		}
	}
	return "", false
}

func extractPowerShellScript(args []string) (string, bool) {
	for index, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "-command", "-c":
			script := strings.TrimSpace(strings.Join(args[index+1:], " "))
			return script, script != ""
		}
	}
	return "", false
}

func scriptLooksReadOnly(script string) bool {
	normalized := strings.ToLower(strings.TrimSpace(script))
	if normalized == "" {
		return false
	}
	if scriptLooksMutating(normalized) {
		return false
	}
	for _, prefix := range []string{
		"echo ",
		"echo.",
		"dir",
		"type ",
		"git status",
		"git diff",
		"git log",
		"git show",
		"go test",
		"go vet",
		"rg ",
		"pwd",
		"get-location",
		"get-childitem",
		"write-output ",
	} {
		if normalized == strings.TrimSpace(prefix) || strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func scriptLooksMutating(normalized string) bool {
	for _, token := range []string{
		">",
		">>",
		"set-content",
		"add-content",
		"out-file",
		"new-item",
		"move-item",
		"copy-item",
		"rename-item",
		"remove-item",
		"clear-content",
		"set-item",
		"gofmt ",
		"go fmt",
		"go generate",
		"go mod tidy",
		"go mod vendor",
		"git apply",
		"git add",
		"git commit",
		"git checkout",
		"git switch",
		"git reset",
		"git clean",
		"del ",
		"erase ",
		"copy ",
		"move ",
		"ren ",
		"rename ",
	} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}
