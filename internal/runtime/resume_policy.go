package runtime

import (
	"fmt"
	"strings"
)

type ResumePolicy struct{}

type ResumeDecision struct {
	Allowed         bool
	Retryable       bool
	Reason          string
	Summary         string
	PausePointID    string
	ResumeAttemptID string
}

func (p ResumePolicy) CanStartAttempt(request PendingApprovalRequest) ResumeDecision {
	pausePointID := request.PausePointID()
	decision := ResumeDecision{
		Allowed:      true,
		Retryable:    true,
		Reason:       "no_prior_attempt",
		PausePointID: strings.TrimSpace(pausePointID),
		Summary:      "Resume attempt may start.",
	}
	if request.Continuation == nil {
		return decision
	}
	decision.ResumeAttemptID = strings.TrimSpace(request.Continuation.ResumeAttemptID)
	status := strings.TrimSpace(request.Continuation.ResumeAttemptStatus)
	if status == "" {
		status = strings.TrimSpace(request.Continuation.ContinuationStatus)
	}
	switch status {
	case string(ResumeAttemptStatusCompleted):
		decision.Allowed = false
		decision.Retryable = false
		decision.Reason = "already_completed"
		decision.Summary = "Resume attempt is not retryable because the prior attempt completed."
	case string(ResumeAttemptStatusSkipped):
		decision.Allowed = false
		decision.Retryable = false
		decision.Reason = "already_skipped"
		decision.Summary = "Resume attempt is not retryable because the prior replay reused completed lineage."
	case string(ResumeAttemptStatusFailed):
		decision.Allowed = true
		decision.Retryable = true
		decision.Reason = "previous_attempt_failed"
		decision.Summary = "Resume attempt is retryable because the prior attempt failed."
	default:
		decision.Allowed = true
		decision.Retryable = true
		decision.Reason = "attempt_status_retryable"
		decision.Summary = "Resume attempt is retryable."
	}
	return decision
}

func (p ResumePolicy) DenyValidationFailure(request PendingApprovalRequest, validationErr error) ResumeDecision {
	pausePointID := request.PausePointID()
	reason := "suspended_action_validation_failed"
	if validationErr != nil {
		lowered := strings.ToLower(validationErr.Error())
		if strings.Contains(lowered, "digest mismatch") || strings.Contains(lowered, "metadata mismatch") {
			reason = "suspended_action_mismatch"
		}
	}
	attemptID := ""
	if request.Continuation != nil {
		attemptID = strings.TrimSpace(request.Continuation.ResumeAttemptID)
	}
	summary := "Resume attempt is not retryable because the suspended action no longer matches the approval artifact."
	if validationErr != nil {
		summary = fmt.Sprintf("%s %s", summary, validationErr.Error())
	}
	return ResumeDecision{
		Allowed:         false,
		Retryable:       false,
		Reason:          reason,
		Summary:         strings.TrimSpace(summary),
		PausePointID:    strings.TrimSpace(pausePointID),
		ResumeAttemptID: attemptID,
	}
}

func (p ResumePolicy) CanRetryVerifierRemediation(point PausePoint, previousTargets []string, nextTargets []string, explicitUpdate bool) ResumeDecision {
	decision := ResumeDecision{
		Allowed:      true,
		Retryable:    true,
		Reason:       "verifier_remediation_retryable",
		PausePointID: strings.TrimSpace(point.ID),
		Summary:      "Verifier remediation attempt may start.",
	}
	if normalizePausePointKind(point.Kind) != PausePointKindVerifierRemediation {
		decision.Allowed = false
		decision.Retryable = false
		decision.Reason = "pause_point_not_verifier_remediation"
		decision.Summary = "Resume attempt is not retryable because pause point is not verifier remediation."
		return decision
	}
	previousSet := normalizedStringSet(previousTargets)
	nextSet := normalizedStringSet(nextTargets)
	if len(previousSet) > 0 && !stringSetEqual(previousSet, nextSet) && !explicitUpdate {
		decision.Allowed = false
		decision.Retryable = false
		decision.Reason = "verifier_targets_changed"
		decision.Summary = "Verifier remediation attempt is not retryable because targets changed without explicit update."
		return decision
	}
	if explicitUpdate {
		decision.Reason = "verifier_targets_explicitly_updated"
		decision.Summary = "Verifier remediation attempt may start after explicit target update."
	}
	return decision
}

func (p ResumePolicy) CanRetryToolFailure(toolName string, operation string, failure ToolFailureRetryContext) ResumeDecision {
	decision := ResumeDecision{
		Allowed:   true,
		Retryable: true,
		Reason:    "tool_failure_retryable",
		Summary:   "Tool failure retry may start.",
	}
	if strings.TrimSpace(failure.AttemptStatus) == "" {
		decision.Reason = "tool_failure_retryable"
	} else {
		switch strings.TrimSpace(failure.AttemptStatus) {
		case "completed", "skipped":
			decision.Allowed = false
			decision.Retryable = false
			decision.Reason = "tool_failure_attempt_completed"
			decision.Summary = "Tool failure retry is not retryable because the prior attempt already completed."
			return decision
		case "failed":
			decision.Reason = "tool_failure_retryable"
		default:
			decision.Reason = "tool_failure_retryable"
		}
	}
	if strings.TrimSpace(failure.FailureKind) == "tool_not_registered" {
		decision.Allowed = false
		decision.Retryable = false
		decision.Reason = "tool_failure_unavailable"
		decision.Summary = "Tool failure retry is not retryable because the tool is unavailable."
		return decision
	}
	if failure.PermissionDenied {
		decision.Reason = "tool_failure_permission_gated"
		decision.Summary = "Tool failure retry requires a permission or approval context change."
		if !failure.PermissionContextChanged {
			decision.Allowed = false
			decision.Retryable = false
			return decision
		}
		decision.Summary = "Tool failure retry may start after permission or approval context changes."
		return decision
	}
	if strings.TrimSpace(failure.FailureKind) == "" {
		decision.Summary = "Tool failure retry may start."
		return decision
	}
	decision.Reason = "tool_failure_retryable"
	decision.Summary = "Tool failure retry may start."
	return decision
}

type ToolFailureRetryContext struct {
	AttemptStatus            string
	FailureKind              string
	PermissionDenied         bool
	PermissionContextChanged bool
}

func normalizedStringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		result[trimmed] = struct{}{}
	}
	return result
}

func stringSetEqual(left map[string]struct{}, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, ok := right[value]; !ok {
			return false
		}
	}
	return true
}

func (d ResumeDecision) Err() error {
	if d.Allowed {
		return nil
	}
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "resume_not_allowed"
	}
	summary := strings.TrimSpace(d.Summary)
	if summary == "" {
		summary = "Resume attempt is not allowed by policy."
	}
	return fmt.Errorf("resume policy denied continuation: %s: %s", reason, summary)
}
