package evaluation

import (
	"fmt"
	"strings"

	"avatars/internal/verification"
)

type Record struct {
	Tool       string
	Kind       string
	Verdict    string
	Cause      string
	Summary    string
	Source     string
	ReportPath string
	Details    []string
}

func BuildToolApprovalRequiredRecord(toolName string, operation string, payload map[string]any, err error) Record {
	message := normalizePassiveSignal(fmt.Sprint(err))
	if message == "" {
		return Record{}
	}
	toolLabel := strings.TrimSpace(toolName)
	if toolLabel == "" {
		toolLabel = "tool"
	}
	operationLabel := strings.TrimSpace(operation)
	qualifiedTool := toolLabel
	if operationLabel != "" {
		qualifiedTool = qualifiedTool + "/" + operationLabel
	}
	details := make([]string, 0, 11)
	if operationLabel != "" {
		details = append(details, "operation: "+operationLabel)
	}
	if decisionSource := payloadNestedString(payload, "policy", "decision_source"); decisionSource != "" {
		details = append(details, "decision_source: "+decisionSource)
	}
	if rationale := payloadNestedString(payload, "policy", "rationale"); rationale != "" {
		details = append(details, "policy_rationale: "+normalizePassiveSignal(rationale))
	}
	if permissionMode := payloadNestedString(payload, "policy", "permission_mode"); permissionMode != "" {
		details = append(details, "permission_mode: "+permissionMode)
	}
	if approvalKey := payloadString(payload, "approval_key"); approvalKey != "" {
		details = append(details, "approval_key: "+approvalKey)
	}
	if artifactPath := payloadString(payload, "approval_request_artifact"); artifactPath != "" {
		details = append(details, "approval_request_artifact: "+artifactPath)
	}
	if proposalID := payloadString(payload, "proposal_id"); proposalID != "" {
		details = append(details, "proposal_id: "+proposalID)
	}
	if intent := payloadString(payload, "intent"); intent != "" {
		details = append(details, "intent: "+normalizePassiveSignal(intent))
	}
	if path := payloadString(payload, "path"); path != "" {
		details = append(details, "path: "+path)
	}
	if changedFiles := payloadStrings(payload, "changed_files"); len(changedFiles) > 0 {
		details = append(details, "changed_files: "+strings.Join(changedFiles, ", "))
	}
	if expectedTargets := payloadStrings(payload, "expected_targets"); len(expectedTargets) > 0 {
		details = append(details, "expected_targets: "+strings.Join(expectedTargets, ", "))
	}
	if rollbackArtifact := payloadString(payload, "rollback_artifact"); rollbackArtifact != "" {
		details = append(details, "rollback_artifact: "+rollbackArtifact)
	}
	if command := payloadString(payload, "command"); command != "" {
		details = append(details, "command: "+normalizePassiveSignal(command))
	}
	if workingDir := payloadString(payload, "working_dir"); workingDir != "" {
		details = append(details, "working_dir: "+workingDir)
	}
	return Record{
		Tool:    toolLabel,
		Kind:    "passive_feedback",
		Verdict: "PARTIAL",
		Cause:   "tool_approval_required",
		Summary: fmt.Sprintf("Tool %s is awaiting approval: %s", qualifiedTool, message),
		Source:  "tool_runtime",
		Details: details,
	}
}

func BuildToolDenialRecord(toolName string, operation string, payload map[string]any, err error) Record {
	message := normalizePassiveSignal(fmt.Sprint(err))
	if message == "" {
		return Record{}
	}
	toolLabel := strings.TrimSpace(toolName)
	if toolLabel == "" {
		toolLabel = "tool"
	}
	operationLabel := strings.TrimSpace(operation)
	qualifiedTool := toolLabel
	if operationLabel != "" {
		qualifiedTool = qualifiedTool + "/" + operationLabel
	}
	details := make([]string, 0, 10)
	if operationLabel != "" {
		details = append(details, "operation: "+operationLabel)
	}
	if decisionSource := payloadNestedString(payload, "policy", "decision_source"); decisionSource != "" {
		details = append(details, "decision_source: "+decisionSource)
	}
	if rationale := payloadNestedString(payload, "policy", "rationale"); rationale != "" {
		details = append(details, "policy_rationale: "+normalizePassiveSignal(rationale))
	}
	if permissionMode := payloadNestedString(payload, "policy", "permission_mode"); permissionMode != "" {
		details = append(details, "permission_mode: "+permissionMode)
	}
	if approvalKey := payloadString(payload, "approval_key"); approvalKey != "" {
		details = append(details, "approval_key: "+approvalKey)
	}
	if proposalID := payloadString(payload, "proposal_id"); proposalID != "" {
		details = append(details, "proposal_id: "+proposalID)
	}
	if intent := payloadString(payload, "intent"); intent != "" {
		details = append(details, "intent: "+normalizePassiveSignal(intent))
	}
	if path := payloadString(payload, "path"); path != "" {
		details = append(details, "path: "+path)
	}
	if changedFiles := payloadStrings(payload, "changed_files"); len(changedFiles) > 0 {
		details = append(details, "changed_files: "+strings.Join(changedFiles, ", "))
	}
	if expectedTargets := payloadStrings(payload, "expected_targets"); len(expectedTargets) > 0 {
		details = append(details, "expected_targets: "+strings.Join(expectedTargets, ", "))
	}
	if rollbackArtifact := payloadString(payload, "rollback_artifact"); rollbackArtifact != "" {
		details = append(details, "rollback_artifact: "+rollbackArtifact)
	}
	if command := payloadString(payload, "command"); command != "" {
		details = append(details, "command: "+normalizePassiveSignal(command))
	}
	if workingDir := payloadString(payload, "working_dir"); workingDir != "" {
		details = append(details, "working_dir: "+workingDir)
	}
	return Record{
		Tool:    toolLabel,
		Kind:    "passive_feedback",
		Verdict: "FAIL",
		Cause:   "tool_permission_denied",
		Summary: fmt.Sprintf("Tool %s was denied: %s", qualifiedTool, message),
		Source:  "tool_runtime",
		Details: details,
	}
}

func BuildToolFailureRecord(toolName string, operation string, payload map[string]any, err error, registered bool) Record {
	message := normalizePassiveSignal(fmt.Sprint(err))
	if message == "" {
		return Record{}
	}
	toolLabel := strings.TrimSpace(toolName)
	if toolLabel == "" {
		toolLabel = "tool"
	}
	operationLabel := strings.TrimSpace(operation)
	qualifiedTool := toolLabel
	if operationLabel != "" {
		qualifiedTool = qualifiedTool + "/" + operationLabel
	}
	cause := "tool_execution_failed"
	summary := fmt.Sprintf("Tool %s failed: %s", qualifiedTool, message)
	if !registered {
		cause = "tool_not_registered"
		summary = fmt.Sprintf("Tool %s is unavailable: %s", qualifiedTool, message)
	}
	details := make([]string, 0, 6)
	if operationLabel != "" {
		details = append(details, "operation: "+operationLabel)
	}
	if proposalID := payloadString(payload, "proposal_id"); proposalID != "" {
		details = append(details, "proposal_id: "+proposalID)
	}
	if intent := payloadString(payload, "intent"); intent != "" {
		details = append(details, "intent: "+normalizePassiveSignal(intent))
	}
	if path := payloadString(payload, "path"); path != "" {
		details = append(details, "path: "+path)
	}
	if changedFiles := payloadStrings(payload, "changed_files"); len(changedFiles) > 0 {
		details = append(details, "changed_files: "+strings.Join(changedFiles, ", "))
	}
	if expectedTargets := payloadStrings(payload, "expected_targets"); len(expectedTargets) > 0 {
		details = append(details, "expected_targets: "+strings.Join(expectedTargets, ", "))
	}
	if command := payloadString(payload, "command"); command != "" {
		details = append(details, "command: "+normalizePassiveSignal(command))
	}
	if workingDir := payloadString(payload, "working_dir"); workingDir != "" {
		details = append(details, "working_dir: "+workingDir)
	}
	if timeout := payloadValue(payload, "timeout_ms"); timeout != "" {
		details = append(details, "timeout_ms: "+timeout)
	}
	if contentLength := payloadValue(payload, "content_length"); contentLength != "" {
		details = append(details, "content_length: "+contentLength)
	}
	return Record{
		Tool:    toolLabel,
		Kind:    "passive_feedback",
		Verdict: "FAIL",
		Cause:   cause,
		Summary: summary,
		Source:  "tool_runtime",
		Details: details,
	}
}

func BuildReverifyRepairAttemptRecord(toolName string, operation string, payload map[string]any, latestNonPassTool string, latestNonPassVerdict string, latestNonPassReport string) Record {
	toolLabel := strings.TrimSpace(toolName)
	if toolLabel == "" {
		return Record{}
	}
	operationLabel := strings.TrimSpace(operation)
	qualifiedTool := toolLabel
	if operationLabel != "" {
		qualifiedTool = qualifiedTool + "/" + operationLabel
	}
	actionLabel := qualifiedTool
	if toolLabel == "shell" {
		if command := payloadString(payload, "command"); command != "" {
			actionLabel = "shell command " + normalizePassiveSignal(command)
		}
	}
	targetTool := strings.TrimSpace(latestNonPassTool)
	if targetTool == "" {
		targetTool = "n/a"
	}
	targetVerdict := strings.TrimSpace(strings.ToUpper(latestNonPassVerdict))
	if targetVerdict == "" {
		targetVerdict = "n/a"
	}
	intentLabel := payloadString(payload, "intent")
	normalizedIntent := normalizePassiveSignal(intentLabel)
	details := make([]string, 0, 8)
	if operationLabel != "" {
		details = append(details, "operation: "+operationLabel)
	}
	if proposalID := payloadString(payload, "proposal_id"); proposalID != "" {
		details = append(details, "proposal_id: "+proposalID)
	}
	if normalizedIntent != "" {
		details = append(details, "intent: "+normalizedIntent)
	}
	if path := payloadString(payload, "path"); path != "" {
		details = append(details, "path: "+path)
	}
	if changedFiles := payloadStrings(payload, "changed_files"); len(changedFiles) > 0 {
		details = append(details, "changed_files: "+strings.Join(changedFiles, ", "))
	}
	if expectedTargets := payloadStrings(payload, "expected_targets"); len(expectedTargets) > 0 {
		details = append(details, "expected_targets: "+strings.Join(expectedTargets, ", "))
	}
	if workingDir := payloadString(payload, "working_dir"); workingDir != "" {
		details = append(details, "working_dir: "+workingDir)
	}
	if command := payloadString(payload, "command"); command != "" {
		details = append(details, "command: "+normalizePassiveSignal(command))
	}
	if contentLength := payloadValue(payload, "content_length"); contentLength != "" {
		details = append(details, "content_length: "+contentLength)
	}
	if oldLength := payloadValue(payload, "old_length"); oldLength != "" {
		details = append(details, "old_length: "+oldLength)
	}
	if newLength := payloadValue(payload, "new_length"); newLength != "" {
		details = append(details, "new_length: "+newLength)
	}
	details = append(details, "latest_non_pass_tool: "+targetTool, "latest_non_pass_verdict: "+targetVerdict)
	if report := strings.TrimSpace(latestNonPassReport); report != "" {
		details = append(details, "latest_non_pass_report: "+report)
	}
	summary := fmt.Sprintf("%s completed while the latest non-pass verification still awaits reverify: %s via %s.", actionLabel, targetVerdict, targetTool)
	if normalizedIntent != "" {
		summary = fmt.Sprintf("%s completed to %s while the latest non-pass verification still awaits reverify: %s via %s.", actionLabel, normalizedIntent, targetVerdict, targetTool)
	}
	return Record{
		Tool:    toolLabel,
		Kind:    "workflow_feedback",
		Verdict: targetVerdict,
		Cause:   "verification_reverify_fix_attempt",
		Summary: summary,
		Source:  "verification_followup",
		Details: details,
	}
}

func BuildVerificationRecords(toolName string, reportPath string, report verification.Report) []Record {
	records := make([]Record, 0, 1+len(report.Warnings)+len(report.Checks))
	checks := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		checks = append(checks, check.Name+" => "+string(check.Result))
	}
	records = append(records, Record{
		Tool:       toolName,
		Kind:       "verifier_verdict",
		Verdict:    string(report.Verdict),
		Cause:      causeFromVerdict(report.Verdict),
		Summary:    report.Summary,
		Source:     "verification",
		ReportPath: reportPath,
		Details:    checks,
	})
	for _, warning := range report.Warnings {
		records = append(records, Record{
			Tool:       toolName,
			Kind:       "passive_feedback",
			Verdict:    string(report.Verdict),
			Cause:      "verification_warning",
			Summary:    warning,
			Source:     "verification_warning",
			ReportPath: reportPath,
			Details:    checks,
		})
	}
	for _, check := range report.Checks {
		record, ok := buildCheckPassiveFeedback(toolName, reportPath, check)
		if !ok {
			continue
		}
		records = append(records, record)
	}
	return records
}

func causeFromVerdict(verdict verification.Verdict) string {
	switch verdict {
	case verification.VerdictFail:
		return "verification_failed"
	case verification.VerdictPartial:
		return "verification_partial"
	default:
		return "verification_completed"
	}
}

func buildCheckPassiveFeedback(toolName string, reportPath string, check verification.CheckEvidence) (Record, bool) {
	if check.Result == verification.VerdictPass {
		return Record{}, false
	}
	signal := firstNonEmpty(check.OutputObserved, check.Actual, check.Expected)
	signal = normalizePassiveSignal(signal)
	if signal == "" {
		return Record{}, false
	}
	checkName := strings.TrimSpace(check.Name)
	if checkName == "" {
		checkName = strings.TrimSpace(check.CommandRun)
	}
	if checkName == "" {
		checkName = "verification check"
	}
	target := "the current task"
	if trimmedTool := strings.TrimSpace(toolName); trimmedTool != "" {
		target = trimmedTool
	}
	details := make([]string, 0, 4)
	if strings.TrimSpace(check.CommandRun) != "" {
		details = append(details, "command: "+strings.TrimSpace(check.CommandRun))
	}
	if strings.TrimSpace(check.Expected) != "" {
		details = append(details, "expected: "+normalizePassiveSignal(check.Expected))
	}
	if strings.TrimSpace(check.Actual) != "" {
		details = append(details, "actual: "+normalizePassiveSignal(check.Actual))
	}
	if strings.TrimSpace(check.OutputObserved) != "" {
		details = append(details, "output: "+normalizePassiveSignal(check.OutputObserved))
	}
	return Record{
		Tool:       toolName,
		Kind:       "passive_feedback",
		Verdict:    string(check.Result),
		Cause:      causeFromCheckResult(check.Result),
		Summary:    fmt.Sprintf("Verifier check %s reported %s for %s: %s", checkName, check.Result, target, signal),
		Source:     "verification_check",
		ReportPath: reportPath,
		Details:    details,
	}, true
}

func payloadNestedString(payload map[string]any, outer string, inner string) string {
	if payload == nil {
		return ""
	}
	nested, ok := payload[strings.TrimSpace(outer)].(map[string]any)
	if !ok {
		return ""
	}
	return payloadString(nested, inner)
}

func causeFromCheckResult(result verification.Verdict) string {
	switch result {
	case verification.VerdictFail:
		return "verification_check_failed"
	case verification.VerdictPartial:
		return "verification_check_partial"
	default:
		return "verification_check_completed"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizePassiveSignal(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	fields := strings.Fields(trimmed)
	normalized := strings.Join(fields, " ")
	const maxLength = 160
	if len(normalized) <= maxLength {
		return normalized
	}
	return normalized[:maxLength-3] + "..."
}

func payloadString(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func payloadValue(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func payloadStrings(payload map[string]any, key string) []string {
	if len(payload) == 0 {
		return nil
	}
	value, ok := payload[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(fmt.Sprint(item)); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	default:
		trimmed := strings.TrimSpace(fmt.Sprint(value))
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	}
}
