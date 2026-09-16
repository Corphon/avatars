package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"avatars/internal/llm"
	"avatars/internal/planner"
	runtelemetry "avatars/internal/telemetry"
)

type ReaderKind string

const (
	ReaderPlanner     ReaderKind = "planner"
	ReaderSynthesizer ReaderKind = "synthesizer"
	ReaderVerifier    ReaderKind = "verifier"
	ReaderAvatar      ReaderKind = "avatar"
)

type QueryPolicy struct {
	Reader   ReaderKind
	AvatarID string
}

type QueryView struct {
	Reader              ReaderKind
	AvatarID            string
	StableSummary       string
	TaskSummary         string
	AvatarSummary       string
	RecentEvents        []string
	LLMStatus           *llm.ProviderStatus
	LLMStatusWarning    string
	Telemetry           *runtelemetry.Snapshot
	Verification        *VerificationSnapshot
	VerificationHistory []VerificationSnapshot
	EvaluationRecords   []EvaluationRecord
	WarmLessons         []WarmLesson
	EvolutionCandidates []EvolutionCandidate
	ProjectLessons      []ProjectLesson
}

func ParseQueryPolicy(spec string) (QueryPolicy, error) {
	trimmed := strings.TrimSpace(strings.ToLower(spec))
	switch {
	case trimmed == "planner":
		return QueryPolicy{Reader: ReaderPlanner}, nil
	case trimmed == "synthesizer":
		return QueryPolicy{Reader: ReaderSynthesizer}, nil
	case trimmed == "verifier":
		return QueryPolicy{Reader: ReaderVerifier}, nil
	case strings.HasPrefix(trimmed, "avatar:"):
		avatarID := strings.TrimSpace(spec[len("avatar:"):])
		if avatarID == "" {
			return QueryPolicy{}, fmt.Errorf("avatar query policy requires an avatar id")
		}
		return QueryPolicy{Reader: ReaderAvatar, AvatarID: avatarID}, nil
	default:
		return QueryPolicy{}, fmt.Errorf("unsupported query policy %q", spec)
	}
}

func (s Snapshot) View(policy QueryPolicy) QueryView {
	reader := policy.Reader
	if reader == "" {
		reader = ReaderPlanner
	}
	view := QueryView{
		Reader:           reader,
		AvatarID:         policy.AvatarID,
		RecentEvents:     append([]string(nil), s.RecentEvents...),
		LLMStatus:        cloneLLMStatus(s.LLMStatus),
		LLMStatusWarning: s.LLMStatusWarning,
		Telemetry:        cloneTelemetrySnapshot(s.Telemetry),
	}

	switch reader {
	case ReaderPlanner, ReaderSynthesizer:
		view.StableSummary = s.StableSummary
		view.TaskSummary = s.TaskSummary
		view.EvaluationRecords = cloneEvaluationRecords(s.EvaluationRecords)
		view.WarmLessons = cloneWarmLessons(s.WarmLessons)
		view.EvolutionCandidates = cloneEvolutionCandidates(s.EvolutionCandidates)
		view.ProjectLessons = cloneProjectLessons(s.ProjectLessons)
		rankProjectLessons(view.ProjectLessons)
	case ReaderVerifier:
		view.TaskSummary = s.TaskSummary
		view.Verification = cloneVerificationSnapshot(s.Verification)
		view.VerificationHistory = cloneVerificationHistory(s.VerificationHistory)
		view.EvaluationRecords = cloneEvaluationRecords(s.EvaluationRecords)
	case ReaderAvatar:
		view.TaskSummary = s.TaskSummary
		if policy.AvatarID != "" {
			view.AvatarSummary = s.AvatarSummaries[policy.AvatarID]
		}
	default:
		view.StableSummary = s.StableSummary
		view.TaskSummary = s.TaskSummary
	}

	return view
}

func (v QueryView) IsEmpty() bool {
	return v.StableSummary == "" && v.TaskSummary == "" && v.AvatarSummary == "" && len(v.RecentEvents) == 0 && v.LLMStatus == nil && v.LLMStatusWarning == "" && v.Telemetry == nil && v.Verification == nil && len(v.VerificationHistory) == 0 && len(v.EvaluationRecords) == 0 && len(v.WarmLessons) == 0 && len(v.EvolutionCandidates) == 0 && len(v.ProjectLessons) == 0
}

func WorkflowExecutionChain(recentEvents []string) []string {
	if len(recentEvents) == 0 {
		return nil
	}
	const workflowExecutionChainLimit = 6
	chain := make([]string, 0, min(len(recentEvents), workflowExecutionChainLimit))
	for index := len(recentEvents) - 1; index >= 0; index-- {
		summary := strings.TrimSpace(recentEvents[index])
		if !isWorkflowExecutionSummary(summary) {
			continue
		}
		chain = append(chain, summary)
		if len(chain) > workflowExecutionChainLimit {
			chain = append([]string(nil), chain[len(chain)-workflowExecutionChainLimit:]...)
		}
	}
	if len(chain) == 0 {
		return nil
	}
	return chain
}

func LatestAvatarReport(recentEvents []string) string {
	return latestAvatarMessage(recentEvents, "report")
}

func LatestAvatarReportRouteCue(recentEvents []string) string {
	return latestAvatarMessageRouteCue(recentEvents, "report")
}

func LatestAvatarAsk(recentEvents []string) string {
	return latestAvatarMessage(recentEvents, "ask")
}

func LatestAvatarAskRouteCue(recentEvents []string) string {
	return latestAvatarMessageRouteCue(recentEvents, "ask")
}

func LatestAvatarAskFollowUpStatus(recentEvents []string) string {
	askIndex, askSummary := latestAvatarMessageIndexByMarker(recentEvents, " ask: ")
	if askIndex < 0 || askSummary == "" {
		return ""
	}
	askRoute := avatarMessageRouteCueFromSummary(askSummary)
	followUpIndex, followUpSummary := latestAvatarMessageIndexByMarker(recentEvents, " follow-up: ")
	if followUpIndex >= 0 && followUpSummary != "" && followUpIndex < askIndex {
		followUpRoute := avatarMessageRouteCueFromSummary(followUpSummary)
		if followUpRoute != "" {
			return fmt.Sprintf("followed-up via %s", followUpRoute)
		}
		return "followed-up"
	}
	if askRoute != "" {
		return fmt.Sprintf("pending via %s", askRoute)
	}
	return "pending"
}

func LatestAvatarFollowUp(recentEvents []string) string {
	return latestAvatarMessageByLabel(recentEvents, "follow-up")
}

func LatestAvatarFollowUpRouteCue(recentEvents []string) string {
	return avatarMessageRouteCueFromSummary(LatestAvatarFollowUp(recentEvents))
}

func LatestAvatarChallenge(recentEvents []string) string {
	return latestAvatarMessage(recentEvents, "challenge")
}

func LatestAvatarChallengeRouteCue(recentEvents []string) string {
	return latestAvatarMessageRouteCue(recentEvents, "challenge")
}

func LatestAvatarSummary(recentEvents []string) string {
	return latestAvatarMessage(recentEvents, "summary")
}

func LatestAvatarSummaryRouteCue(recentEvents []string) string {
	return latestAvatarMessageRouteCue(recentEvents, "summary")
}

func isWorkflowExecutionSummary(summary string) bool {
	if summary == "" {
		return false
	}
	return strings.HasPrefix(summary, "Planner decomposed the task:") ||
		strings.HasPrefix(summary, "Workflow node created") ||
		strings.HasPrefix(summary, "Avatar assigned:") ||
		strings.Contains(summary, " assigned: ") ||
		strings.HasPrefix(summary, "Work handed off to ") ||
		strings.HasPrefix(summary, "Avatar handoff prepared ") ||
		strings.Contains(summary, " handed off ")
}

func latestAvatarMessage(recentEvents []string, kind string) string {
	marker := " " + strings.TrimSpace(kind) + ": "
	return latestAvatarMessageByMarker(recentEvents, marker)
}

func latestAvatarMessageByLabel(recentEvents []string, label string) string {
	marker := " " + strings.TrimSpace(label) + ": "
	return latestAvatarMessageByMarker(recentEvents, marker)
}

func latestAvatarMessageByMarker(recentEvents []string, marker string) string {
	_, summary := latestAvatarMessageIndexByMarker(recentEvents, marker)
	return summary
}

func latestAvatarMessageIndexByMarker(recentEvents []string, marker string) (int, string) {
	for _, summary := range recentEvents {
		_ = summary
	}
	for index, summary := range recentEvents {
		trimmed := strings.TrimSpace(summary)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, marker) {
			return index, trimmed
		}
	}
	return -1, ""
}

func latestAvatarMessageRouteCue(recentEvents []string, kind string) string {
	return avatarMessageRouteCueFromSummary(latestAvatarMessage(recentEvents, kind))
}

func avatarMessageRouteCueFromSummary(summary string) string {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return ""
	}
	markerIndex := strings.Index(trimmed, ": ")
	if markerIndex < 0 {
		return ""
	}
	remainder := trimmed[markerIndex+2:]
	if !strings.HasPrefix(remainder, "[") {
		return ""
	}
	closingIndex := strings.Index(remainder, "]")
	if closingIndex <= 1 {
		return ""
	}
	return strings.TrimSpace(remainder[1:closingIndex])
}

func SuggestedTaskCommands(taskID string, snapshot Snapshot) []string {
	return suggestedCommands(taskID, snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords, true)
}

func SuggestedQueryCommands(taskID string, view QueryView) []string {
	return suggestedCommands(taskID, view.Verification, view.VerificationHistory, view.EvaluationRecords, view.Reader != ReaderVerifier)
}

func suggestedCommands(taskID string, verification *VerificationSnapshot, history []VerificationSnapshot, evaluationRecords []EvaluationRecord, includeVerifierView bool) []string {
	commands := make([]string, 0, 3)
	trimmedTaskID := strings.TrimSpace(taskID)
	if includeVerifierView && trimmedTaskID != "" && (verification != nil || len(history) > 0 || len(evaluationRecords) > 0) {
		commands = appendUniqueCommand(commands, fmt.Sprintf("avatars tasks show %s --view verifier", trimmedTaskID))
	}
	if verification != nil {
		followUp := TaskVerificationFollowUpCommand(trimmedTaskID, *verification)
		if followUp == "" {
			followUp = strings.TrimSpace(verification.FollowUpCommand)
		}
		commands = appendUniqueCommand(commands, followUp)
	}
	if latestNonPass := LatestNonPassVerificationSnapshot(verification, history); latestNonPass != nil {
		followUp := TaskVerificationFollowUpCommand(trimmedTaskID, *latestNonPass)
		if followUp == "" {
			followUp = strings.TrimSpace(latestNonPass.FollowUpCommand)
		}
		commands = appendUniqueCommand(commands, followUp)
	}
	if VerificationReverifyStatus(verification, history) == "pending reverify for latest non-pass verification" {
		fallback := "avatars verify"
		if trimmedTaskID != "" {
			fallback = fmt.Sprintf("avatars verify --task %s", trimmedTaskID)
		}
		commands = appendUniqueCommand(commands, fallback)
	}
	if trimmedTaskID != "" {
		pendingApprovals := PendingToolApprovalEvaluations(evaluationRecords)
		if len(pendingApprovals) == 1 {
			if EvaluationRecordApprovalRequestArtifact(pendingApprovals[0]) != "" {
				commands = appendUniqueCommand(commands, fmt.Sprintf("avatars tasks approve %s --replay", trimmedTaskID))
			}
			commands = appendUniqueCommand(commands, fmt.Sprintf("avatars tasks approve %s", trimmedTaskID))
			commands = appendUniqueCommand(commands, fmt.Sprintf("avatars tasks deny %s", trimmedTaskID))
		} else if len(pendingApprovals) > 1 {
			approvalKey := EvaluationRecordApprovalKey(pendingApprovals[0])
			if approvalKey != "" {
				if EvaluationRecordApprovalRequestArtifact(pendingApprovals[0]) != "" {
					commands = appendUniqueCommand(commands, fmt.Sprintf("avatars tasks approve %s --approval-key %s --replay", trimmedTaskID, approvalKey))
				}
				commands = appendUniqueCommand(commands, fmt.Sprintf("avatars tasks approve %s --approval-key %s", trimmedTaskID, approvalKey))
				commands = appendUniqueCommand(commands, fmt.Sprintf("avatars tasks deny %s --approval-key %s", trimmedTaskID, approvalKey))
			}
		}
	}
	return commands
}

func appendUniqueCommand(commands []string, command string) []string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return commands
	}
	for _, existing := range commands {
		if strings.TrimSpace(existing) == trimmed {
			return commands
		}
	}
	return append(commands, trimmed)
}

func SnapshotRemediationProposals(taskID string, snapshot Snapshot) []planner.RemediationActionProposal {
	return remediationProposals(taskID, snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords)
}

func QueryRemediationProposals(taskID string, view QueryView) []planner.RemediationActionProposal {
	return remediationProposals(taskID, view.Verification, view.VerificationHistory, view.EvaluationRecords)
}

func RemediationEnvelope(taskID string, verification *VerificationSnapshot, history []VerificationSnapshot, evaluationRecords []EvaluationRecord) *planner.RemediationEnvelope {
	envelope := planner.RemediationEnvelope{}
	if verification != nil {
		tool := strings.TrimSpace(verification.Tool)
		if tool == "" {
			tool = "n/a"
		}
		verdict := strings.TrimSpace(verification.Verdict)
		if verdict == "" {
			verdict = "n/a"
		}
		envelope.CurrentVerification = fmt.Sprintf("%s via %s", verdict, tool)
		envelope.CurrentVerificationSummary = strings.TrimSpace(verification.Summary)
	}
	if latestNonPass := LatestNonPassVerificationSnapshot(verification, history); latestNonPass != nil {
		envelope.LatestNonPassVerification = describePromptVerificationSnapshot(*latestNonPass)
		envelope.TaskScopedFollowUp = TaskVerificationFollowUpCommand(taskID, *latestNonPass)
		if envelope.TaskScopedFollowUp == "" {
			envelope.TaskScopedFollowUp = strings.TrimSpace(latestNonPass.FollowUpCommand)
		}
	}
	envelope.ReverifyStatus = VerificationReverifyStatus(verification, history)
	if attempt := LatestReverifyAttemptEvaluation(verification, history, evaluationRecords); attempt != nil {
		envelope.LatestRemediation = strings.TrimSpace(attempt.Summary)
		envelope.ExpectedTargets = append([]string(nil), EvaluationRecordExpectedTargets(*attempt)...)
	} else if attempt := LatestCoveredReverifyAttemptEvaluation(verification, history, evaluationRecords); attempt != nil {
		envelope.LatestRemediation = strings.TrimSpace(attempt.Summary)
		envelope.ExpectedTargets = append([]string(nil), EvaluationRecordExpectedTargets(*attempt)...)
	}
	if guarded := LatestVerifierRemediationResumeAttemptEvaluation(evaluationRecords); guarded != nil {
		envelope.LatestGuardedRemediation = strings.TrimSpace(guarded.Summary)
		envelope.GuardedRemediationStatus = EvaluationRecordRecoveryBoundaryGuardStatus(*guarded)
		envelope.GuardedRemediationAuthority = EvaluationRecordRecoveryBoundaryRequiredAuthority(*guarded)
		envelope.GuardedRemediationSource = EvaluationRecordRecoveryBoundarySource(*guarded)
		if len(envelope.ExpectedTargets) == 0 {
			envelope.ExpectedTargets = append([]string(nil), EvaluationRecordExpectedTargets(*guarded)...)
		}
	}
	if closureChain := LatestVerifierRemediationClosureChain(verification, history, evaluationRecords); closureChain != nil {
		envelope.VerifierRemediationClosureStatus = closureChain.Status
		if closureChain.PassClosureSummary != "" {
			envelope.VerifierRemediationClosure = closureChain.PassClosureSummary
		}
		envelope.VerifierRemediationClosureReport = closureChain.PassClosureReport
		envelope.VerifierRemediationClosurePausePoint = closureChain.RemediationPausePointID
		envelope.VerifierRemediationClosureResume = closureChain.RemediationResumeAttemptID
		if len(envelope.ExpectedTargets) == 0 {
			envelope.ExpectedTargets = append([]string(nil), closureChain.ExpectedTargets...)
		}
	}
	if retryClosure := LatestFailedNodeRetryAttemptClosureEvidence(evaluationRecords); retryClosure != nil {
		envelope.FailedNodeRetryClosureStatus = retryClosure.RetryAttemptStatus
		envelope.FailedNodeRetryClosureReady = fmt.Sprint(retryClosure.ClosureReady)
		envelope.FailedNodeRetryClosureVerifierGate = retryClosure.NodeWorkVerifierGateStatus
		envelope.FailedNodeRetryClosureVerifierVerdict = retryClosure.NodeWorkVerifierVerdict
		envelope.FailedNodeRetryClosureReport = retryClosure.NodeWorkVerificationReportPath
		if len(envelope.ExpectedTargets) == 0 {
			if attempt := LatestFailedNodeRetryAttempt(evaluationRecords); attempt != nil {
				envelope.ExpectedTargets = append([]string(nil), attempt.ExpectedTargets...)
			}
		}
	}
	if recoverySummary := LatestRecoveryInspectionSummary(verification, history, evaluationRecords, 10, "planner_remediation_envelope"); recoverySummary != nil {
		envelope.RecoverySummaryCategory = recoverySummary.DecisionCategory
		envelope.RecoverySummaryAction = recoverySummary.ActionKind
		envelope.RecoverySummaryGuardStatus = recoverySummary.GuardStatus
		envelope.RecoverySummaryGuidance = recoverySummary.Guidance
		if len(envelope.ExpectedTargets) == 0 {
			envelope.ExpectedTargets = append([]string(nil), recoverySummary.ExpectedTargets...)
		}
	}
	if envelope.CurrentVerification == "" && envelope.CurrentVerificationSummary == "" && envelope.LatestNonPassVerification == "" && envelope.TaskScopedFollowUp == "" && envelope.ReverifyStatus == "" && envelope.LatestRemediation == "" && envelope.LatestGuardedRemediation == "" && envelope.VerifierRemediationClosureStatus == "" && envelope.FailedNodeRetryClosureStatus == "" && envelope.RecoverySummaryGuidance == "" && len(envelope.ExpectedTargets) == 0 {
		return nil
	}
	normalized := planner.NormalizeRemediationEnvelope(envelope)
	if normalized.CurrentVerification == "" && normalized.CurrentVerificationSummary == "" && normalized.LatestNonPassVerification == "" && normalized.TaskScopedFollowUp == "" && normalized.ReverifyStatus == "" && normalized.LatestRemediation == "" && normalized.LatestGuardedRemediation == "" && normalized.VerifierRemediationClosureStatus == "" && normalized.FailedNodeRetryClosureStatus == "" && normalized.RecoverySummaryGuidance == "" && len(normalized.ExpectedTargets) == 0 && len(normalized.Proposals) == 0 {
		return nil
	}
	return &normalized
}

func remediationProposals(taskID string, verification *VerificationSnapshot, history []VerificationSnapshot, evaluationRecords []EvaluationRecord) []planner.RemediationActionProposal {
	envelope := RemediationEnvelope(taskID, verification, history, evaluationRecords)
	if envelope == nil || len(envelope.Proposals) == 0 {
		return nil
	}
	proposals := make([]planner.RemediationActionProposal, 0, len(envelope.Proposals))
	for _, proposal := range envelope.Proposals {
		cloned := proposal
		cloned.ExpectedTargets = append([]string(nil), proposal.ExpectedTargets...)
		proposals = append(proposals, cloned)
	}
	return proposals
}

func EvaluationRecordExpectedTargets(record EvaluationRecord) []string {
	return evaluationRecordDetailValues(record, "expected_targets")
}

func EvaluationRecordDependsOn(record EvaluationRecord) []string {
	return evaluationRecordDetailValues(record, "depends_on")
}

func EvaluationRecordDecisionSource(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "decision_source")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordOperation(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "operation")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordPermissionMode(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "permission_mode")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordApprovalKey(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "approval_key")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordApprovalRequestArtifact(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "approval_request_artifact")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordReplayTranscript(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "replay_transcript")
	if len(values) == 0 {
		values = evaluationRecordDetailValues(record, "continuation_transcript")
	}
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordContinuationRunID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "continuation_run_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordContinuationTaskID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "continuation_task_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordOriginRunID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "origin_run_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordOriginTaskID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "origin_task_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordPausePointID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "pause_point_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordPausePointKind(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "pause_point_kind")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordPausePointDigest(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "pause_point_digest")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordResumeAttemptID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "resume_attempt_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordResumeAttemptStatus(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "resume_attempt_status")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRetryAttemptID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "retry_attempt_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRetryAttemptStatus(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "retry_attempt_status")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRetryAttemptPolicyReason(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "retry_attempt_policy_reason")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRetryAttemptVerifierGateExpectation(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "retry_attempt_verifier_gate_expectation")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRetryAttemptClosureStatus(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "retry_attempt_closure_status")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRetryable(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "retryable")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordAllowed(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "allowed")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordReason(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "reason")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordStatusReason(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "status_reason")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordCurrentNodeStatus(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "current_node_status")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordDependencyReadiness(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "dependency_readiness")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordSchedulerContinuationReadiness(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "scheduler_continuation_readiness")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordNodeID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "node_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordNodeRole(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "node_role")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordNodeTitle(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "node_title")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordArtifactIDs(record EvaluationRecord) []string {
	return evaluationRecordDetailValues(record, "artifact_ids")
}

func EvaluationRecordReplayStatus(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "replay_status")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordVerifierGateStatus(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "verifier_gate_status")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordVerifierVerdict(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "verifier_verdict")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordVerified(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "verified")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordVerificationReportPath(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "verification_report_path")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRecoveryKind(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "recovery_kind")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordProposalID(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "proposal_id")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRecoveryBoundaryGuardStatus(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "recovery_boundary_guard_status")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRecoveryBoundaryRequiredAuthority(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "recovery_boundary_required_authority")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func EvaluationRecordRecoveryBoundarySource(record EvaluationRecord) string {
	values := evaluationRecordDetailValues(record, "recovery_boundary_source")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

type WorkflowNodeGovernanceSnapshot struct {
	LatestNodeEvidence             bool
	LatestNodeID                   string
	LatestNodeStatus               string
	LatestNodeVerifierGateStatus   string
	LatestNodeVerifierVerdict      string
	LatestNodeRecoveryKind         string
	RetryCandidateEvidence         bool
	RetryCandidateNodeID           string
	RetryCandidateRetryable        string
	RetryCandidateContractReady    bool
	PendingApprovalCount           int
	VerifierPending                bool
	RetryClosureEvidence           bool
	RetryClosureAttemptID          string
	RetryClosureReady              bool
	RetryClosureMissingEvidence    []string
	RetryClosureStatus             string
	RetryClosureVerifierGateStatus string
	RetryClosureVerifierVerdict    string
	RetryClosureVerifierReportPath string
	NextActionCue                  string
}

func WorkflowNodeGovernanceSnapshotForTask(snapshot Snapshot) WorkflowNodeGovernanceSnapshot {
	governance := WorkflowNodeGovernanceSnapshot{
		PendingApprovalCount: len(PendingToolApprovalEvaluations(snapshot.EvaluationRecords)),
		VerifierPending:      VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory) == "pending reverify for latest non-pass verification",
		NextActionCue:        WorkflowNodeGovernanceNextActionCue(snapshot),
	}
	if latestNode := LatestNodeWorkEvidence(snapshot.EvaluationRecords); latestNode != nil {
		governance.LatestNodeEvidence = true
		governance.LatestNodeID = EvaluationRecordNodeID(*latestNode)
		governance.LatestNodeStatus = EvaluationRecordDetailValue(*latestNode, "status")
		governance.LatestNodeVerifierGateStatus = EvaluationRecordVerifierGateStatus(*latestNode)
		governance.LatestNodeVerifierVerdict = EvaluationRecordVerifierVerdict(*latestNode)
		governance.LatestNodeRecoveryKind = EvaluationRecordRecoveryKind(*latestNode)
	}
	if retryCandidate := LatestFailedNodeRetryCandidate(snapshot.EvaluationRecords); retryCandidate != nil {
		governance.RetryCandidateEvidence = true
		governance.RetryCandidateNodeID = retryCandidate.NodeID
		governance.RetryCandidateRetryable = retryCandidate.Retryable
		governance.RetryCandidateContractReady = retryCandidate.ContractReady
	}
	if closure := LatestFailedNodeRetryAttemptClosureEvidence(snapshot.EvaluationRecords); closure != nil {
		governance.RetryClosureEvidence = true
		governance.RetryClosureAttemptID = closure.RetryAttemptID
		governance.RetryClosureReady = closure.ClosureReady
		governance.RetryClosureMissingEvidence = append([]string(nil), closure.MissingEvidence...)
		governance.RetryClosureStatus = closure.RetryAttemptStatus
		governance.RetryClosureVerifierGateStatus = closure.NodeWorkVerifierGateStatus
		governance.RetryClosureVerifierVerdict = closure.NodeWorkVerifierVerdict
		governance.RetryClosureVerifierReportPath = closure.NodeWorkVerificationReportPath
	}
	return governance
}

func WorkflowNodeGovernanceNextActionCue(snapshot Snapshot) string {
	pendingApprovals := PendingToolApprovalEvaluations(snapshot.EvaluationRecords)
	if len(pendingApprovals) > 0 {
		return "inspect pending approval before continuation"
	}
	if VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory) == "pending reverify for latest non-pass verification" {
		return "run verifier reverify before closure"
	}
	if retryCandidate := LatestFailedNodeRetryCandidate(snapshot.EvaluationRecords); retryCandidate != nil {
		attempt := LatestFailedNodeRetryAttempt(snapshot.EvaluationRecords)
		if attempt == nil || !attempt.IsClosed() {
			return "inspect failed-node retry preflight before scheduler continuation"
		}
	}
	if closure := LatestFailedNodeRetryAttemptClosureEvidence(snapshot.EvaluationRecords); closure != nil {
		if !closure.ClosureReady || len(closure.MissingEvidence) > 0 {
			return "collect missing retry closure evidence before risky continuation"
		}
		if closure.ClosureReady {
			return "manual review required before risky continuation"
		}
	}
	return ""
}

func LatestRunVerifierVerdictEvaluation(records []EvaluationRecord, runID string) *EvaluationRecord {
	trimmedRunID := strings.TrimSpace(runID)
	if trimmedRunID == "" {
		return nil
	}
	for _, record := range records {
		if strings.TrimSpace(record.RunID) != trimmedRunID {
			continue
		}
		if strings.TrimSpace(record.Kind) != "verifier_verdict" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func PromptVerificationContext(taskID string, snapshot Snapshot) string {
	lines := make([]string, 0, 8)
	if snapshot.Verification != nil {
		tool := strings.TrimSpace(snapshot.Verification.Tool)
		if tool == "" {
			tool = "n/a"
		}
		verdict := strings.TrimSpace(snapshot.Verification.Verdict)
		if verdict == "" {
			verdict = "n/a"
		}
		lines = append(lines, fmt.Sprintf("current_verification: %s via %s", verdict, tool))
		if summary := strings.TrimSpace(snapshot.Verification.Summary); summary != "" {
			lines = append(lines, "current_verification_summary: "+summary)
		}
	}
	if latestNonPass := LatestNonPassVerificationSnapshot(snapshot.Verification, snapshot.VerificationHistory); latestNonPass != nil {
		lines = append(lines, "latest_non_pass_verification: "+describePromptVerificationSnapshot(*latestNonPass))
		if followUp := TaskVerificationFollowUpCommand(taskID, *latestNonPass); followUp != "" {
			lines = append(lines, "latest_non_pass_follow_up: "+followUp)
		}
	}
	if status := VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory); status != "" {
		lines = append(lines, "reverify_status: "+status)
	}
	if attempt := LatestReverifyAttemptEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); attempt != nil {
		lines = append(lines, "latest_reverify_attempt: "+strings.TrimSpace(attempt.Summary))
		if proposalID := EvaluationRecordProposalID(*attempt); proposalID != "" {
			lines = append(lines, "latest_reverify_attempt_proposal_id: "+proposalID)
		}
		if targets := EvaluationRecordExpectedTargets(*attempt); len(targets) > 0 {
			lines = append(lines, "latest_reverify_attempt_targets: "+strings.Join(targets, ", "))
		}
	}
	if attempt := LatestCoveredReverifyAttemptEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); attempt != nil {
		lines = append(lines, "latest_reverify_remediation: "+strings.TrimSpace(attempt.Summary))
		if proposalID := EvaluationRecordProposalID(*attempt); proposalID != "" {
			lines = append(lines, "latest_reverify_remediation_proposal_id: "+proposalID)
		}
		if targets := EvaluationRecordExpectedTargets(*attempt); len(targets) > 0 {
			lines = append(lines, "latest_reverify_remediation_targets: "+strings.Join(targets, ", "))
		}
	}
	if guarded := LatestVerifierRemediationResumeAttemptEvaluation(snapshot.EvaluationRecords); guarded != nil {
		lines = append(lines, "latest_guarded_remediation_attempt: "+strings.TrimSpace(guarded.Summary))
		if guardStatus := EvaluationRecordRecoveryBoundaryGuardStatus(*guarded); guardStatus != "" {
			lines = append(lines, "latest_guarded_remediation_guard_status: "+guardStatus)
		}
		if authority := EvaluationRecordRecoveryBoundaryRequiredAuthority(*guarded); authority != "" {
			lines = append(lines, "latest_guarded_remediation_authority: "+authority)
		}
		if source := EvaluationRecordRecoveryBoundarySource(*guarded); source != "" {
			lines = append(lines, "latest_guarded_remediation_source: "+source)
		}
		if targets := EvaluationRecordExpectedTargets(*guarded); len(targets) > 0 {
			lines = append(lines, "latest_guarded_remediation_targets: "+strings.Join(targets, ", "))
		}
	}
	if closureChain := LatestVerifierRemediationClosureChain(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); closureChain != nil {
		lines = append(lines, "verifier_remediation_closure_status: "+closureChain.Status)
		if closureChain.RemediationPausePointID != "" {
			lines = append(lines, "verifier_remediation_closure_pause_point: "+closureChain.RemediationPausePointID)
		}
		if closureChain.RemediationResumeAttemptID != "" {
			lines = append(lines, "verifier_remediation_closure_resume_attempt: "+closureChain.RemediationResumeAttemptID)
		}
		if closureChain.PassClosureSummary != "" {
			lines = append(lines, "verifier_remediation_closure: "+closureChain.PassClosureSummary)
		}
		if closureChain.PassClosureReport != "" {
			lines = append(lines, "verifier_remediation_closure_report: "+closureChain.PassClosureReport)
		}
	}
	if recoverySummary := LatestRecoveryInspectionSummary(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords, 10, "prompt_verification_context"); recoverySummary != nil {
		if recoverySummary.DecisionCategory != "" {
			lines = append(lines, "recovery_summary_category: "+recoverySummary.DecisionCategory)
		}
		if recoverySummary.ActionKind != "" {
			lines = append(lines, "recovery_summary_action: "+recoverySummary.ActionKind)
		}
		if recoverySummary.GuardStatus != "" {
			lines = append(lines, "recovery_summary_guard_status: "+recoverySummary.GuardStatus)
		}
		if recoverySummary.ClosureStatus != "" {
			lines = append(lines, "recovery_summary_closure_status: "+recoverySummary.ClosureStatus)
		}
		if recoverySummary.PausePointID != "" {
			lines = append(lines, "recovery_summary_pause_point: "+recoverySummary.PausePointID)
		}
		if recoverySummary.ResumeAttemptID != "" {
			lines = append(lines, "recovery_summary_resume_attempt: "+recoverySummary.ResumeAttemptID)
		}
		if len(recoverySummary.ExpectedTargets) > 0 {
			lines = append(lines, "recovery_summary_targets: "+strings.Join(recoverySummary.ExpectedTargets, ", "))
		}
		if recoverySummary.Guidance != "" {
			lines = append(lines, "recovery_summary_guidance: "+recoverySummary.Guidance)
		}
	}
	if retryCandidate := LatestFailedNodeRetryCandidate(snapshot.EvaluationRecords); retryCandidate != nil {
		if retryCandidate.PausePointID != "" {
			lines = append(lines, "failed_node_retry_pause_point: "+retryCandidate.PausePointID)
		}
		if retryCandidate.PausePointKind != "" {
			lines = append(lines, "failed_node_retry_pause_point_kind: "+retryCandidate.PausePointKind)
		}
		if retryCandidate.PausePointDigest != "" {
			lines = append(lines, "failed_node_retry_pause_point_digest: "+retryCandidate.PausePointDigest)
		}
		if retryCandidate.NodeID != "" {
			lines = append(lines, "failed_node_retry_node_id: "+retryCandidate.NodeID)
		}
		if retryCandidate.OriginRunID != "" {
			lines = append(lines, "failed_node_retry_origin_run_id: "+retryCandidate.OriginRunID)
		}
		if retryCandidate.OriginTaskID != "" {
			lines = append(lines, "failed_node_retry_origin_task_id: "+retryCandidate.OriginTaskID)
		}
		if retryCandidate.StatusReason != "" {
			lines = append(lines, "failed_node_retry_status_reason: "+retryCandidate.StatusReason)
		}
		if retryCandidate.Retryable != "" {
			lines = append(lines, "failed_node_retry_retryable: "+retryCandidate.Retryable)
		}
		lines = append(lines, "failed_node_retry_contract_ready: "+fmt.Sprint(retryCandidate.ContractReady))
		if len(retryCandidate.MissingFields) > 0 {
			lines = append(lines, "failed_node_retry_missing_fields: "+strings.Join(retryCandidate.MissingFields, ", "))
		}
	}
	if attempt := LatestFailedNodeRetryAttempt(snapshot.EvaluationRecords); attempt != nil {
		if attempt.RetryAttemptID != "" {
			lines = append(lines, "failed_node_retry_attempt_id: "+attempt.RetryAttemptID)
		}
		if attempt.RetryAttemptStatus != "" {
			lines = append(lines, "failed_node_retry_attempt_status: "+attempt.RetryAttemptStatus)
		}
		if attempt.RetryAttemptClosureStatus != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_status: "+attempt.RetryAttemptClosureStatus)
		}
		if attempt.RetryAttemptVerifierGateExpectation != "" {
			lines = append(lines, "failed_node_retry_attempt_verifier_gate_expectation: "+attempt.RetryAttemptVerifierGateExpectation)
		}
		lines = append(lines, "failed_node_retry_attempt_open: "+fmt.Sprint(attempt.IsOpen()))
		lines = append(lines, "failed_node_retry_attempt_closed: "+fmt.Sprint(attempt.IsClosed()))
		lines = append(lines, "failed_node_retry_attempt_failed: "+fmt.Sprint(attempt.IsFailed()))
	}
	if closure := LatestFailedNodeRetryAttemptClosureEvidence(snapshot.EvaluationRecords); closure != nil {
		if closure.RetryAttemptID != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_retry_attempt_id: "+closure.RetryAttemptID)
		}
		if closure.RetryAttemptStatus != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_status: "+closure.RetryAttemptStatus)
		}
		lines = append(lines, "failed_node_retry_attempt_closure_ready: "+fmt.Sprint(closure.ClosureReady))
		if len(closure.MissingEvidence) > 0 {
			lines = append(lines, "failed_node_retry_attempt_closure_missing_evidence: "+strings.Join(closure.MissingEvidence, ", "))
		}
		if closure.NodeWorkNodeID != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_node_id: "+closure.NodeWorkNodeID)
		}
		if closure.NodeWorkVerifierGateStatus != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_verifier_gate_status: "+closure.NodeWorkVerifierGateStatus)
		}
		if closure.NodeWorkVerifierVerdict != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_verifier_verdict: "+closure.NodeWorkVerifierVerdict)
		}
		if closure.NodeWorkVerificationReportPath != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_verification_report_path: "+closure.NodeWorkVerificationReportPath)
		}
		if closure.SchedulerResumeStatus != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_scheduler_resume_status: "+closure.SchedulerResumeStatus)
		}
		if closure.LifecycleStatus != "" {
			lines = append(lines, "failed_node_retry_attempt_closure_lifecycle_status: "+closure.LifecycleStatus)
		}
	}
	if artifact := LatestFailedNodeRetryArtifact(snapshot.EvaluationRecords); artifact != nil {
		if artifact.RetryArtifactID != "" {
			lines = append(lines, "failed_node_retry_artifact_id: "+artifact.RetryArtifactID)
		}
		if artifact.RetryAttemptID != "" {
			lines = append(lines, "failed_node_retry_artifact_attempt_id: "+artifact.RetryAttemptID)
		}
		if artifact.PausePointID != "" {
			lines = append(lines, "failed_node_retry_artifact_pause_point: "+artifact.PausePointID)
		}
		if artifact.FailedNodeID != "" {
			lines = append(lines, "failed_node_retry_artifact_node_id: "+artifact.FailedNodeID)
		}
		if artifact.OriginRunID != "" {
			lines = append(lines, "failed_node_retry_artifact_origin_run_id: "+artifact.OriginRunID)
		}
		if artifact.OriginTaskID != "" {
			lines = append(lines, "failed_node_retry_artifact_origin_task_id: "+artifact.OriginTaskID)
		}
		if artifact.VerifierGateExpectation != "" {
			lines = append(lines, "failed_node_retry_artifact_verifier_gate_expectation: "+artifact.VerifierGateExpectation)
		}
		if len(artifact.ExpectedTargets) > 0 {
			lines = append(lines, "failed_node_retry_artifact_targets: "+strings.Join(artifact.ExpectedTargets, ", "))
		}
		if artifact.RequiredAuthority != "" {
			lines = append(lines, "failed_node_retry_artifact_authority: "+artifact.RequiredAuthority)
		}
		if artifact.GuardStatus != "" {
			lines = append(lines, "failed_node_retry_artifact_guard: "+artifact.GuardStatus)
		}
		lines = append(lines, "failed_node_retry_artifact_terminal: "+fmt.Sprint(artifact.Terminal))
		if artifact.TerminalAttemptID != "" {
			lines = append(lines, "failed_node_retry_artifact_terminal_attempt_id: "+artifact.TerminalAttemptID)
		}
		if artifact.TerminalAttemptStatus != "" {
			lines = append(lines, "failed_node_retry_artifact_terminal_attempt_status: "+artifact.TerminalAttemptStatus)
		}
	}
	if readiness := LatestFailedNodeRetryCurrentStateReadiness(snapshot.EvaluationRecords); readiness != nil {
		lines = append(lines, "failed_node_retry_current_state_ready: "+fmt.Sprint(readiness.Ready))
		if readiness.CurrentNodeStatus != "" {
			lines = append(lines, "failed_node_retry_current_node_status: "+readiness.CurrentNodeStatus)
		}
		if len(readiness.DependsOn) > 0 {
			lines = append(lines, "failed_node_retry_depends_on: "+strings.Join(readiness.DependsOn, ", "))
		}
		if readiness.DependencyReadiness != "" {
			lines = append(lines, "failed_node_retry_dependency_readiness: "+readiness.DependencyReadiness)
		}
		if readiness.SchedulerContinuationReadiness != "" {
			lines = append(lines, "failed_node_retry_scheduler_continuation_readiness: "+readiness.SchedulerContinuationReadiness)
		}
		if len(readiness.MissingEvidence) > 0 {
			lines = append(lines, "failed_node_retry_current_state_missing_evidence: "+strings.Join(readiness.MissingEvidence, ", "))
		}
		if readiness.RequiredAuthority != "" {
			lines = append(lines, "failed_node_retry_current_state_authority: "+readiness.RequiredAuthority)
		}
		if readiness.GuardStatus != "" {
			lines = append(lines, "failed_node_retry_current_state_guard: "+readiness.GuardStatus)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func LatestToolApprovalDecision(records []EvaluationRecord, approvalKey string) *EvaluationRecord {
	trimmedApprovalKey := strings.TrimSpace(approvalKey)
	if trimmedApprovalKey == "" {
		return nil
	}
	for _, record := range records {
		switch strings.TrimSpace(record.Cause) {
		case "tool_approval_granted", "tool_approval_denied":
			if EvaluationRecordApprovalKey(record) != trimmedApprovalKey {
				continue
			}
			cloned := record
			cloned.Details = append([]string(nil), record.Details...)
			return &cloned
		}
	}
	return nil
}

func PendingToolApprovalEvaluations(records []EvaluationRecord) []EvaluationRecord {
	if len(records) == 0 {
		return nil
	}
	// Two-pass: collect resolved keys first so order (newest/oldest-first)
	// cannot leave a granted approval stuck as "pending".
	resolvedApprovalKeys := make(map[string]struct{})
	for _, record := range records {
		switch strings.TrimSpace(record.Cause) {
		case "tool_approval_granted", "tool_approval_denied":
			if approvalKey := EvaluationRecordApprovalKey(record); approvalKey != "" {
				resolvedApprovalKeys[approvalKey] = struct{}{}
			}
		}
	}
	pending := make([]EvaluationRecord, 0, 2)
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "tool_approval_required" {
			continue
		}
		if approvalKey := EvaluationRecordApprovalKey(record); approvalKey != "" {
			if _, ok := resolvedApprovalKeys[approvalKey]; ok {
				continue
			}
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		pending = append(pending, cloned)
	}
	if len(pending) == 0 {
		return nil
	}
	return pending
}

func PendingToolApprovalEvaluationByKey(records []EvaluationRecord, approvalKey string) *EvaluationRecord {
	trimmedApprovalKey := strings.TrimSpace(approvalKey)
	if trimmedApprovalKey == "" {
		return nil
	}
	for _, record := range PendingToolApprovalEvaluations(records) {
		if EvaluationRecordApprovalKey(record) != trimmedApprovalKey {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestPendingToolApprovalEvaluation(records []EvaluationRecord) *EvaluationRecord {
	pending := PendingToolApprovalEvaluations(records)
	if len(pending) == 0 {
		return nil
	}
	cloned := pending[0]
	cloned.Details = append([]string(nil), pending[0].Details...)
	return &cloned
}

func LatestToolApprovalRequiredEvaluation(records []EvaluationRecord) *EvaluationRecord {
	return LatestPendingToolApprovalEvaluation(records)
}

func LatestToolApprovalReplayEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		switch strings.TrimSpace(record.Cause) {
		case "tool_approval_replayed", "tool_approval_replay_failed", "tool_approval_replay_skipped":
			cloned := record
			cloned.Details = append([]string(nil), record.Details...)
			return &cloned
		}
	}
	return nil
}

func LatestResumeAttemptEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "resume_attempt_recorded" {
			continue
		}
		if EvaluationRecordResumeAttemptID(record) == "" || EvaluationRecordPausePointID(record) == "" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestResumePolicyEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "resume_policy_decision" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestFailedNodePausePointEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "failed_node_pause_point" {
			continue
		}
		if EvaluationRecordPausePointID(record) == "" || EvaluationRecordStatusReason(record) == "" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

type FailedNodeRetryCandidate struct {
	TaskID            string
	OriginRunID       string
	OriginTaskID      string
	PausePointID      string
	PausePointKind    string
	PausePointDigest  string
	NodeID            string
	StatusReason      string
	Retryable         string
	ExpectedTargets   []string
	RequiredAuthority string
	GuardStatus       string
	ContractReady     bool
	MissingFields     []string
	Summary           string
	SourceCause       string
	SourceRunID       string
	SourceTaskID      string
	SourceUpdatedAt   time.Time
}

func LatestFailedNodeRetryCandidate(records []EvaluationRecord) *FailedNodeRetryCandidate {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "failed_node_pause_point" {
			continue
		}
		candidate := failedNodeRetryCandidateFromRecord(record)
		if candidate == nil {
			continue
		}
		return candidate
	}
	return nil
}

type FailedNodeRetryAttempt struct {
	RetryAttemptID                      string
	RetryAttemptStatus                  string
	RetryAttemptPolicyReason            string
	RetryAttemptVerifierGateExpectation string
	RetryAttemptClosureStatus           string
	TaskID                              string
	OriginRunID                         string
	OriginTaskID                        string
	PausePointID                        string
	PausePointKind                      string
	PausePointDigest                    string
	NodeID                              string
	StatusReason                        string
	ExpectedTargets                     []string
	RequiredAuthority                   string
	GuardStatus                         string
	SourceCause                         string
	SourceSummary                       string
	SourceRunID                         string
	SourceTaskID                        string
	SourceUpdatedAt                     time.Time
}

type FailedNodeRetryAttemptClosureEvidence struct {
	RetryAttemptID                      string
	RetryAttemptStatus                  string
	RetryAttemptClosureStatus           string
	RetryAttemptVerifierGateExpectation string
	NodeWorkStatus                      string
	NodeWorkNodeID                      string
	NodeWorkSummary                     string
	NodeWorkVerifierGateStatus          string
	NodeWorkVerifierVerdict             string
	NodeWorkVerificationReportPath      string
	SchedulerResumeStatus               string
	SchedulerResumeRunID                string
	SchedulerResumeTaskID               string
	SchedulerResumeSummary              string
	LifecycleStatus                     string
	LifecycleRunID                      string
	LifecycleTaskID                     string
	LifecycleSummary                    string
	ClosureReady                        bool
	MissingEvidence                     []string
	RequiredAuthority                   string
	GuardStatus                         string
}

func (a FailedNodeRetryAttempt) IsOpen() bool {
	switch strings.TrimSpace(a.RetryAttemptStatus) {
	case "requested", "started":
		return true
	default:
		return false
	}
}

func (a FailedNodeRetryAttempt) IsClosed() bool {
	switch strings.TrimSpace(a.RetryAttemptStatus) {
	case "completed", "skipped", "denied":
		return true
	default:
		return false
	}
}

func (a FailedNodeRetryAttempt) IsFailed() bool {
	return strings.TrimSpace(a.RetryAttemptStatus) == "failed"
}

func (e FailedNodeRetryAttemptClosureEvidence) IsInspectionOnly() bool {
	return strings.TrimSpace(e.RequiredAuthority) == "workflow_scheduler" &&
		strings.TrimSpace(e.GuardStatus) == "manual_review_required"
}

func LatestFailedNodeRetryAttempt(records []EvaluationRecord) *FailedNodeRetryAttempt {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "failed_node_retry_attempt" {
			continue
		}
		attempt := failedNodeRetryAttemptFromRecord(record)
		if attempt == nil {
			continue
		}
		return attempt
	}
	return nil
}

func failedNodeRetryAttemptFromRecord(record EvaluationRecord) *FailedNodeRetryAttempt {
	attemptID := EvaluationRecordRetryAttemptID(record)
	if attemptID == "" {
		return nil
	}
	attempt := &FailedNodeRetryAttempt{
		RetryAttemptID:                      attemptID,
		RetryAttemptStatus:                  EvaluationRecordRetryAttemptStatus(record),
		RetryAttemptPolicyReason:            EvaluationRecordRetryAttemptPolicyReason(record),
		RetryAttemptVerifierGateExpectation: EvaluationRecordRetryAttemptVerifierGateExpectation(record),
		RetryAttemptClosureStatus:           EvaluationRecordRetryAttemptClosureStatus(record),
		TaskID:                              strings.TrimSpace(record.TaskID),
		OriginRunID:                         EvaluationRecordOriginRunID(record),
		OriginTaskID:                        EvaluationRecordOriginTaskID(record),
		PausePointID:                        EvaluationRecordPausePointID(record),
		PausePointKind:                      EvaluationRecordPausePointKind(record),
		PausePointDigest:                    EvaluationRecordPausePointDigest(record),
		NodeID:                              EvaluationRecordNodeID(record),
		StatusReason:                        EvaluationRecordStatusReason(record),
		ExpectedTargets:                     append([]string(nil), EvaluationRecordExpectedTargets(record)...),
		RequiredAuthority:                   "workflow_scheduler",
		GuardStatus:                         "manual_review_required",
		SourceCause:                         strings.TrimSpace(record.Cause),
		SourceSummary:                       strings.TrimSpace(record.Summary),
		SourceRunID:                         strings.TrimSpace(record.RunID),
		SourceTaskID:                        strings.TrimSpace(record.TaskID),
		SourceUpdatedAt:                     record.UpdatedAt,
	}
	if attempt.OriginRunID == "" {
		attempt.OriginRunID = strings.TrimSpace(record.RunID)
	}
	if attempt.OriginTaskID == "" {
		attempt.OriginTaskID = strings.TrimSpace(record.TaskID)
	}
	return attempt
}

func LatestFailedNodeRetryAttemptChain(records []EvaluationRecord, limit int) []FailedNodeRetryAttempt {
	if limit <= 0 {
		return nil
	}
	chain := make([]FailedNodeRetryAttempt, 0, limit)
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "failed_node_retry_attempt" {
			continue
		}
		attempt := failedNodeRetryAttemptFromRecord(record)
		if attempt == nil {
			continue
		}
		chain = append(chain, *attempt)
		if len(chain) >= limit {
			return chain
		}
	}
	return chain
}

func LatestFailedNodeRetryAttemptClosureEvidence(records []EvaluationRecord) *FailedNodeRetryAttemptClosureEvidence {
	attempt := LatestFailedNodeRetryAttempt(records)
	if attempt == nil {
		return nil
	}
	return FailedNodeRetryAttemptClosureEvidenceForAttempt(records, *attempt)
}

func FailedNodeRetryAttemptClosureEvidenceForAttempt(records []EvaluationRecord, attempt FailedNodeRetryAttempt) *FailedNodeRetryAttemptClosureEvidence {
	attemptID := strings.TrimSpace(attempt.RetryAttemptID)
	if attemptID == "" {
		return nil
	}
	closure := &FailedNodeRetryAttemptClosureEvidence{
		RetryAttemptID:                      attemptID,
		RetryAttemptStatus:                  strings.TrimSpace(attempt.RetryAttemptStatus),
		RetryAttemptClosureStatus:           strings.TrimSpace(attempt.RetryAttemptClosureStatus),
		RetryAttemptVerifierGateExpectation: strings.TrimSpace(attempt.RetryAttemptVerifierGateExpectation),
		RequiredAuthority:                   "workflow_scheduler",
		GuardStatus:                         "manual_review_required",
	}
	for _, record := range records {
		if EvaluationRecordRetryAttemptID(record) != attemptID {
			continue
		}
		switch strings.TrimSpace(record.Cause) {
		case "node_work_evidence":
			if closure.NodeWorkNodeID != "" {
				continue
			}
			closure.NodeWorkStatus = EvaluationRecordDetailValue(record, "status")
			closure.NodeWorkNodeID = EvaluationRecordNodeID(record)
			closure.NodeWorkSummary = strings.TrimSpace(record.Summary)
			closure.NodeWorkVerifierGateStatus = EvaluationRecordVerifierGateStatus(record)
			closure.NodeWorkVerifierVerdict = EvaluationRecordVerifierVerdict(record)
			closure.NodeWorkVerificationReportPath = EvaluationRecordVerificationReportPath(record)
		case "failed_node_retry_scheduler_resume":
			if closure.SchedulerResumeStatus != "" {
				continue
			}
			closure.SchedulerResumeStatus = EvaluationRecordDetailValue(record, "scheduler_resume_status")
			if closure.SchedulerResumeStatus == "" {
				closure.SchedulerResumeStatus = EvaluationRecordDetailValue(record, "status")
			}
			closure.SchedulerResumeRunID = EvaluationRecordContinuationRunID(record)
			if closure.SchedulerResumeRunID == "" {
				closure.SchedulerResumeRunID = strings.TrimSpace(record.RunID)
			}
			closure.SchedulerResumeTaskID = EvaluationRecordContinuationTaskID(record)
			if closure.SchedulerResumeTaskID == "" {
				closure.SchedulerResumeTaskID = strings.TrimSpace(record.TaskID)
			}
			closure.SchedulerResumeSummary = strings.TrimSpace(record.Summary)
		case "run_lifecycle_updated":
			if closure.LifecycleStatus != "" {
				continue
			}
			closure.LifecycleStatus = EvaluationRecordDetailValue(record, "status")
			closure.LifecycleRunID = EvaluationRecordOriginRunID(record)
			if closure.LifecycleRunID == "" {
				closure.LifecycleRunID = strings.TrimSpace(record.RunID)
			}
			closure.LifecycleTaskID = EvaluationRecordOriginTaskID(record)
			if closure.LifecycleTaskID == "" {
				closure.LifecycleTaskID = strings.TrimSpace(record.TaskID)
			}
			closure.LifecycleSummary = strings.TrimSpace(record.Summary)
		}
	}
	closure.MissingEvidence = failedNodeRetryAttemptClosureMissingEvidence(*closure)
	closure.ClosureReady = len(closure.MissingEvidence) == 0 && strings.TrimSpace(closure.RetryAttemptStatus) == "completed"
	return closure
}

func failedNodeRetryAttemptClosureMissingEvidence(closure FailedNodeRetryAttemptClosureEvidence) []string {
	missing := make([]string, 0, 4)
	if strings.TrimSpace(closure.NodeWorkNodeID) == "" {
		missing = append(missing, "node_work_evidence")
	}
	if strings.TrimSpace(closure.SchedulerResumeStatus) == "" {
		missing = append(missing, "scheduler_resume_evidence")
	}
	if strings.TrimSpace(closure.RetryAttemptVerifierGateExpectation) != "" && strings.TrimSpace(closure.NodeWorkVerifierGateStatus) == "" && strings.TrimSpace(closure.NodeWorkVerifierVerdict) == "" {
		missing = append(missing, "verifier_gate_evidence")
	}
	if strings.TrimSpace(closure.LifecycleStatus) == "" {
		missing = append(missing, "lifecycle_evidence")
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

func failedNodeRetryCandidateFromRecord(record EvaluationRecord) *FailedNodeRetryCandidate {
	candidate := &FailedNodeRetryCandidate{
		TaskID:            strings.TrimSpace(record.TaskID),
		OriginRunID:       EvaluationRecordOriginRunID(record),
		OriginTaskID:      EvaluationRecordOriginTaskID(record),
		PausePointID:      EvaluationRecordPausePointID(record),
		PausePointKind:    EvaluationRecordPausePointKind(record),
		PausePointDigest:  EvaluationRecordPausePointDigest(record),
		NodeID:            EvaluationRecordNodeID(record),
		StatusReason:      EvaluationRecordStatusReason(record),
		Retryable:         EvaluationRecordRetryable(record),
		ExpectedTargets:   append([]string(nil), EvaluationRecordExpectedTargets(record)...),
		RequiredAuthority: "workflow_scheduler",
		GuardStatus:       "manual_review_required",
		Summary:           strings.TrimSpace(record.Summary),
		SourceCause:       strings.TrimSpace(record.Cause),
		SourceRunID:       strings.TrimSpace(record.RunID),
		SourceTaskID:      strings.TrimSpace(record.TaskID),
		SourceUpdatedAt:   record.UpdatedAt,
	}
	if candidate.OriginRunID == "" {
		candidate.OriginRunID = strings.TrimSpace(record.RunID)
	}
	if candidate.OriginTaskID == "" {
		candidate.OriginTaskID = strings.TrimSpace(record.TaskID)
	}
	candidate.MissingFields = failedNodeRetryCandidateMissingFields(candidate)
	candidate.ContractReady = len(candidate.MissingFields) == 0
	return candidate
}

func failedNodeRetryCandidateMissingFields(candidate *FailedNodeRetryCandidate) []string {
	if candidate == nil {
		return nil
	}
	missing := make([]string, 0, 8)
	if strings.TrimSpace(candidate.TaskID) == "" {
		missing = append(missing, "task_id")
	}
	if strings.TrimSpace(candidate.OriginRunID) == "" {
		missing = append(missing, "origin_run_id")
	}
	if strings.TrimSpace(candidate.OriginTaskID) == "" {
		missing = append(missing, "origin_task_id")
	}
	if strings.TrimSpace(candidate.PausePointID) == "" {
		missing = append(missing, "pause_point_id")
	}
	if strings.TrimSpace(candidate.PausePointKind) != "workflow_node" {
		missing = append(missing, "pause_point_kind=workflow_node")
	}
	if strings.TrimSpace(candidate.PausePointDigest) == "" {
		missing = append(missing, "pause_point_digest")
	}
	if strings.TrimSpace(candidate.NodeID) == "" {
		missing = append(missing, "node_id")
	}
	if strings.TrimSpace(candidate.StatusReason) == "" {
		missing = append(missing, "status_reason")
	}
	if strings.TrimSpace(candidate.Retryable) != "true" {
		missing = append(missing, "retryable=true")
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

type FailedNodeRetryPreflightRequest struct {
	TaskID                  string
	OriginRunID             string
	OriginTaskID            string
	PausePointID            string
	NodeID                  string
	PausePointDigest        string
	ExpectedTargets         []string
	VerifierGateExpectation string
	OperatorConfirmed       bool
	OperatorSource          string
}

type FailedNodeRetryPreflightBoundary struct {
	Allowed                 bool
	Reasons                 []string
	TaskID                  string
	OriginRunID             string
	OriginTaskID            string
	PausePointID            string
	NodeID                  string
	PausePointDigest        string
	ExpectedTargets         []string
	VerifierGateExpectation string
	RequiredAuthority       string
	GuardStatus             string
	OperatorSource          string
}

type FailedNodeRetryArtifact struct {
	RetryArtifactID         string
	RetryAttemptID          string
	TaskID                  string
	OriginRunID             string
	OriginTaskID            string
	PausePointID            string
	PausePointDigest        string
	FailedNodeID            string
	StatusReason            string
	ExpectedTargets         []string
	VerifierGateExpectation string
	OperatorSource          string
	RequiredAuthority       string
	GuardStatus             string
	TerminalAttemptID       string
	TerminalAttemptStatus   string
	Terminal                bool
}

type FailedNodeRetryCurrentStateReadiness struct {
	TaskID                         string
	OriginRunID                    string
	OriginTaskID                   string
	PausePointID                   string
	PausePointDigest               string
	NodeID                         string
	CurrentNodeStatus              string
	DependsOn                      []string
	DependencyReadiness            string
	SchedulerContinuationReadiness string
	RequiredAuthority              string
	GuardStatus                    string
	Ready                          bool
	MissingEvidence                []string
	SourceSummary                  string
	SourceUpdatedAt                time.Time
}

func EvaluateFailedNodeRetryPreflight(records []EvaluationRecord, request FailedNodeRetryPreflightRequest) FailedNodeRetryPreflightBoundary {
	boundary := FailedNodeRetryPreflightBoundary{
		TaskID:                  strings.TrimSpace(request.TaskID),
		OriginRunID:             strings.TrimSpace(request.OriginRunID),
		OriginTaskID:            strings.TrimSpace(request.OriginTaskID),
		PausePointID:            strings.TrimSpace(request.PausePointID),
		NodeID:                  strings.TrimSpace(request.NodeID),
		PausePointDigest:        strings.TrimSpace(request.PausePointDigest),
		ExpectedTargets:         normalizePreflightTargets(request.ExpectedTargets),
		VerifierGateExpectation: strings.TrimSpace(request.VerifierGateExpectation),
		RequiredAuthority:       "workflow_scheduler",
		GuardStatus:             "manual_review_required",
		OperatorSource:          strings.TrimSpace(request.OperatorSource),
	}
	candidate := LatestFailedNodeRetryCandidate(records)
	if candidate == nil {
		boundary.Reasons = append(boundary.Reasons, "failed_node_retry_candidate_missing")
		return boundary
	}
	if !request.OperatorConfirmed {
		boundary.Reasons = append(boundary.Reasons, "operator_confirmation_missing")
	}
	if strings.TrimSpace(candidate.TaskID) != "" && boundary.TaskID != strings.TrimSpace(candidate.TaskID) {
		boundary.Reasons = append(boundary.Reasons, "task_id_mismatch")
	}
	if boundary.OriginRunID != strings.TrimSpace(candidate.OriginRunID) {
		boundary.Reasons = append(boundary.Reasons, "origin_run_id_mismatch")
	}
	if boundary.OriginTaskID != "" && boundary.OriginTaskID != strings.TrimSpace(candidate.OriginTaskID) {
		boundary.Reasons = append(boundary.Reasons, "origin_task_id_mismatch")
	}
	if boundary.PausePointID != strings.TrimSpace(candidate.PausePointID) {
		boundary.Reasons = append(boundary.Reasons, "pause_point_id_mismatch")
	}
	if boundary.NodeID != strings.TrimSpace(candidate.NodeID) {
		boundary.Reasons = append(boundary.Reasons, "node_id_mismatch")
	}
	if boundary.PausePointDigest != strings.TrimSpace(candidate.PausePointDigest) {
		boundary.Reasons = append(boundary.Reasons, "pause_point_digest_mismatch")
	}
	if strings.TrimSpace(candidate.PausePointKind) != "workflow_node" {
		boundary.Reasons = append(boundary.Reasons, "pause_point_kind_not_workflow_node")
	}
	if strings.TrimSpace(candidate.Retryable) != "true" {
		boundary.Reasons = append(boundary.Reasons, "retryable_not_true")
	}
	if len(candidate.MissingFields) > 0 {
		boundary.Reasons = append(boundary.Reasons, "candidate_contract_incomplete")
	}
	candidateTargets := normalizePreflightTargets(candidate.ExpectedTargets)
	if !samePreflightTargets(boundary.ExpectedTargets, candidateTargets) {
		boundary.Reasons = append(boundary.Reasons, "expected_targets_mismatch")
	}
	if len(candidateTargets) > 0 && boundary.VerifierGateExpectation == "" {
		boundary.Reasons = append(boundary.Reasons, "verifier_gate_expectation_missing")
	}
	if readiness := LatestFailedNodeRetryCurrentStateReadiness(records); readiness == nil || !readiness.Ready || !failedNodeRetryReadinessMatchesCandidate(*candidate, readiness) {
		boundary.Reasons = append(boundary.Reasons, "current_state_readiness_incomplete")
	}
	for _, attempt := range LatestFailedNodeRetryAttemptChain(records, 20) {
		if !sameFailedNodeRetryCoordinates(*candidate, attempt) {
			continue
		}
		recordedExpectation := strings.TrimSpace(attempt.RetryAttemptVerifierGateExpectation)
		if recordedExpectation != "" && boundary.VerifierGateExpectation != "" && boundary.VerifierGateExpectation != recordedExpectation {
			boundary.Reasons = append(boundary.Reasons, "verifier_gate_expectation_mismatch")
		}
		switch strings.TrimSpace(attempt.RetryAttemptStatus) {
		case "requested", "started":
			boundary.Reasons = append(boundary.Reasons, "retry_attempt_in_flight")
		case "completed", "skipped":
			boundary.Reasons = append(boundary.Reasons, "terminal_retry_scope")
		case "failed":
			if !hasFailedNodeRetryAfterFailurePolicy(records, attempt) {
				boundary.Reasons = append(boundary.Reasons, "retry_after_failure_policy_missing")
			}
		case "denied":
			boundary.Reasons = append(boundary.Reasons, "retry_after_denial_policy_missing")
		}
		break
	}
	boundary.Allowed = len(boundary.Reasons) == 0
	return boundary
}

func failedNodeRetryReadinessMatchesCandidate(candidate FailedNodeRetryCandidate, readiness *FailedNodeRetryCurrentStateReadiness) bool {
	if readiness == nil {
		return false
	}
	return strings.TrimSpace(readiness.TaskID) == strings.TrimSpace(candidate.TaskID) &&
		strings.TrimSpace(readiness.OriginRunID) == strings.TrimSpace(candidate.OriginRunID) &&
		strings.TrimSpace(readiness.OriginTaskID) == strings.TrimSpace(candidate.OriginTaskID) &&
		strings.TrimSpace(readiness.PausePointID) == strings.TrimSpace(candidate.PausePointID) &&
		strings.TrimSpace(readiness.PausePointDigest) == strings.TrimSpace(candidate.PausePointDigest) &&
		strings.TrimSpace(readiness.NodeID) == strings.TrimSpace(candidate.NodeID)
}

func hasFailedNodeRetryAfterFailurePolicy(records []EvaluationRecord, attempt FailedNodeRetryAttempt) bool {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "failed_node_retry_after_failure_policy" {
			continue
		}
		if EvaluationRecordRetryAttemptID(record) != strings.TrimSpace(attempt.RetryAttemptID) {
			continue
		}
		if strings.TrimSpace(EvaluationRecordAllowed(record)) == "true" {
			return true
		}
	}
	return false
}

func FailedNodeRetryArtifactFromPreflight(records []EvaluationRecord, boundary FailedNodeRetryPreflightBoundary) *FailedNodeRetryArtifact {
	if !boundary.Allowed {
		return nil
	}
	candidate := LatestFailedNodeRetryCandidate(records)
	if candidate == nil {
		return nil
	}
	artifact := &FailedNodeRetryArtifact{
		TaskID:                  strings.TrimSpace(boundary.TaskID),
		OriginRunID:             strings.TrimSpace(boundary.OriginRunID),
		OriginTaskID:            strings.TrimSpace(boundary.OriginTaskID),
		PausePointID:            strings.TrimSpace(boundary.PausePointID),
		PausePointDigest:        strings.TrimSpace(boundary.PausePointDigest),
		FailedNodeID:            strings.TrimSpace(boundary.NodeID),
		StatusReason:            strings.TrimSpace(candidate.StatusReason),
		ExpectedTargets:         normalizePreflightTargets(boundary.ExpectedTargets),
		VerifierGateExpectation: strings.TrimSpace(boundary.VerifierGateExpectation),
		OperatorSource:          strings.TrimSpace(boundary.OperatorSource),
		RequiredAuthority:       "workflow_scheduler",
		GuardStatus:             "manual_review_required",
	}
	artifact.RetryArtifactID = failedNodeRetryArtifactID(*artifact)
	artifact.RetryAttemptID = failedNodeRetryAttemptIDForArtifact(*artifact)
	if terminal := failedNodeRetryTerminalAttemptForArtifact(records, *artifact); terminal != nil {
		artifact.Terminal = true
		artifact.TerminalAttemptID = terminal.RetryAttemptID
		artifact.TerminalAttemptStatus = terminal.RetryAttemptStatus
	}
	return artifact
}

func LatestFailedNodeRetryArtifact(records []EvaluationRecord) *FailedNodeRetryArtifact {
	candidate := LatestFailedNodeRetryCandidate(records)
	if candidate == nil {
		return nil
	}
	verifierGateExpectation := latestFailedNodeRetryArtifactVerifierGateExpectation(records, *candidate)
	boundary := EvaluateFailedNodeRetryPreflight(records, FailedNodeRetryPreflightRequest{
		TaskID:                  strings.TrimSpace(candidate.TaskID),
		OriginRunID:             strings.TrimSpace(candidate.OriginRunID),
		OriginTaskID:            strings.TrimSpace(candidate.OriginTaskID),
		PausePointID:            strings.TrimSpace(candidate.PausePointID),
		NodeID:                  strings.TrimSpace(candidate.NodeID),
		PausePointDigest:        strings.TrimSpace(candidate.PausePointDigest),
		ExpectedTargets:         candidate.ExpectedTargets,
		VerifierGateExpectation: verifierGateExpectation,
		OperatorConfirmed:       true,
		OperatorSource:          "artifact_inspection",
	})
	return FailedNodeRetryArtifactFromPreflight(records, boundary)
}

func LatestFailedNodeRetryCurrentStateReadiness(records []EvaluationRecord) *FailedNodeRetryCurrentStateReadiness {
	candidate := LatestFailedNodeRetryCandidate(records)
	if candidate == nil {
		return nil
	}
	record := LatestFailedNodePausePointEvaluation(records)
	if record == nil {
		return nil
	}
	readiness := &FailedNodeRetryCurrentStateReadiness{
		TaskID:                         strings.TrimSpace(candidate.TaskID),
		OriginRunID:                    strings.TrimSpace(candidate.OriginRunID),
		OriginTaskID:                   strings.TrimSpace(candidate.OriginTaskID),
		PausePointID:                   strings.TrimSpace(candidate.PausePointID),
		PausePointDigest:               strings.TrimSpace(candidate.PausePointDigest),
		NodeID:                         strings.TrimSpace(candidate.NodeID),
		CurrentNodeStatus:              EvaluationRecordCurrentNodeStatus(*record),
		DependsOn:                      EvaluationRecordDependsOn(*record),
		DependencyReadiness:            EvaluationRecordDependencyReadiness(*record),
		SchedulerContinuationReadiness: EvaluationRecordSchedulerContinuationReadiness(*record),
		RequiredAuthority:              "workflow_scheduler",
		GuardStatus:                    "manual_review_required",
		SourceSummary:                  strings.TrimSpace(record.Summary),
		SourceUpdatedAt:                record.UpdatedAt,
	}
	readiness.MissingEvidence = failedNodeRetryCurrentStateMissingEvidence(*readiness)
	readiness.Ready = len(readiness.MissingEvidence) == 0
	return readiness
}

func failedNodeRetryCurrentStateMissingEvidence(readiness FailedNodeRetryCurrentStateReadiness) []string {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(readiness.CurrentNodeStatus) == "" {
		missing = append(missing, "current_node_status")
	}
	if strings.TrimSpace(readiness.DependencyReadiness) == "" {
		missing = append(missing, "dependency_readiness")
	}
	if strings.TrimSpace(readiness.SchedulerContinuationReadiness) == "" {
		missing = append(missing, "scheduler_continuation_readiness")
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

func latestFailedNodeRetryArtifactVerifierGateExpectation(records []EvaluationRecord, candidate FailedNodeRetryCandidate) string {
	for _, attempt := range LatestFailedNodeRetryAttemptChain(records, 20) {
		if !sameFailedNodeRetryCoordinates(candidate, attempt) {
			continue
		}
		if !samePreflightTargets(attempt.ExpectedTargets, candidate.ExpectedTargets) {
			continue
		}
		if expectation := strings.TrimSpace(attempt.RetryAttemptVerifierGateExpectation); expectation != "" {
			return expectation
		}
	}
	if len(normalizePreflightTargets(candidate.ExpectedTargets)) == 0 {
		return "no mutating targets recorded"
	}
	return ""
}

func failedNodeRetryTerminalAttemptForArtifact(records []EvaluationRecord, artifact FailedNodeRetryArtifact) *FailedNodeRetryAttempt {
	for _, attempt := range LatestFailedNodeRetryAttemptChain(records, 20) {
		if !failedNodeRetryAttemptMatchesArtifact(attempt, artifact) {
			continue
		}
		if attempt.RetryAttemptStatus == "completed" || attempt.RetryAttemptStatus == "skipped" {
			return &attempt
		}
	}
	return nil
}

func failedNodeRetryAttemptMatchesArtifact(attempt FailedNodeRetryAttempt, artifact FailedNodeRetryArtifact) bool {
	return strings.TrimSpace(attempt.TaskID) == strings.TrimSpace(artifact.TaskID) &&
		strings.TrimSpace(attempt.OriginRunID) == strings.TrimSpace(artifact.OriginRunID) &&
		strings.TrimSpace(attempt.OriginTaskID) == strings.TrimSpace(artifact.OriginTaskID) &&
		strings.TrimSpace(attempt.PausePointID) == strings.TrimSpace(artifact.PausePointID) &&
		strings.TrimSpace(attempt.NodeID) == strings.TrimSpace(artifact.FailedNodeID) &&
		strings.TrimSpace(attempt.PausePointDigest) == strings.TrimSpace(artifact.PausePointDigest) &&
		samePreflightTargets(attempt.ExpectedTargets, artifact.ExpectedTargets)
}

func failedNodeRetryArtifactID(artifact FailedNodeRetryArtifact) string {
	return "failed-node-retry-artifact-" + shortFailedNodeRetryDigest([]string{
		artifact.TaskID,
		artifact.OriginRunID,
		artifact.OriginTaskID,
		artifact.PausePointID,
		artifact.PausePointDigest,
		artifact.FailedNodeID,
		strings.Join(sortedPreflightTargets(artifact.ExpectedTargets), "\x00"),
		artifact.OperatorSource,
	})
}

func failedNodeRetryAttemptIDForArtifact(artifact FailedNodeRetryArtifact) string {
	return "failed-node-retry-attempt-" + shortFailedNodeRetryDigest([]string{
		artifact.TaskID,
		artifact.OriginRunID,
		artifact.OriginTaskID,
		artifact.PausePointID,
		artifact.PausePointDigest,
		artifact.FailedNodeID,
	})
}

func shortFailedNodeRetryDigest(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

func sameFailedNodeRetryCoordinates(candidate FailedNodeRetryCandidate, attempt FailedNodeRetryAttempt) bool {
	return strings.TrimSpace(candidate.TaskID) == strings.TrimSpace(attempt.TaskID) &&
		strings.TrimSpace(candidate.OriginRunID) == strings.TrimSpace(attempt.OriginRunID) &&
		strings.TrimSpace(candidate.OriginTaskID) == strings.TrimSpace(attempt.OriginTaskID) &&
		strings.TrimSpace(candidate.PausePointID) == strings.TrimSpace(attempt.PausePointID) &&
		strings.TrimSpace(candidate.NodeID) == strings.TrimSpace(attempt.NodeID) &&
		strings.TrimSpace(candidate.PausePointDigest) == strings.TrimSpace(attempt.PausePointDigest)
}

func normalizePreflightTargets(targets []string) []string {
	normalized := make([]string, 0, len(targets))
	for _, target := range targets {
		trimmed := strings.TrimSpace(target)
		if trimmed == "" || containsPreflightTarget(normalized, trimmed) {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func samePreflightTargets(left []string, right []string) bool {
	left = normalizePreflightTargets(left)
	right = normalizePreflightTargets(right)
	if len(left) != len(right) {
		return false
	}
	for _, target := range left {
		if !containsPreflightTarget(right, target) {
			return false
		}
	}
	return true
}

func sortedPreflightTargets(targets []string) []string {
	normalized := normalizePreflightTargets(targets)
	sort.Strings(normalized)
	return normalized
}

func containsPreflightTarget(targets []string, target string) bool {
	for _, existing := range targets {
		if existing == target {
			return true
		}
	}
	return false
}

func LatestNodeWorkEvidence(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "node_work_evidence" {
			continue
		}
		if EvaluationRecordNodeID(record) == "" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestNodeWorkEvidenceChain(records []EvaluationRecord, limit int) []EvaluationRecord {
	if limit <= 0 {
		return nil
	}
	chain := make([]EvaluationRecord, 0, limit)
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "node_work_evidence" {
			continue
		}
		if EvaluationRecordNodeID(record) == "" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		chain = append(chain, cloned)
		if len(chain) >= limit {
			return chain
		}
	}
	return chain
}

func LatestRecoveryEvidenceChain(records []EvaluationRecord, limit int) []EvaluationRecord {
	if limit <= 0 {
		return nil
	}
	chain := make([]EvaluationRecord, 0, limit)
	for _, record := range records {
		if !isRecoveryEvidenceRecord(record) {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		chain = append(chain, cloned)
		if len(chain) >= limit {
			return chain
		}
	}
	return chain
}

type VerifierRemediationClosureChain struct {
	Status                     string
	LatestNonPassVerification  string
	LatestNonPassSummary       string
	LatestNonPassReport        string
	GuardedRemediationSummary  string
	RemediationPausePointID    string
	RemediationResumeAttemptID string
	GuardStatus                string
	RequiredAuthority          string
	BoundarySource             string
	ExpectedTargets            []string
	PassClosureSummary         string
	PassClosureReport          string
}

type RecoveryInspectionSummary struct {
	DecisionCategory  string
	SourceCause       string
	ActionKind        string
	RequiredAuthority string
	GuardStatus       string
	ClosureStatus     string
	PausePointID      string
	ResumeAttemptID   string
	Reason            string
	Retryable         string
	Allowed           string
	ExpectedTargets   []string
	Summary           string
	Guidance          string
}

func LatestVerifierRemediationClosureChain(current *VerificationSnapshot, history []VerificationSnapshot, records []EvaluationRecord) *VerifierRemediationClosureChain {
	latestNonPass := LatestNonPassVerificationSnapshot(current, history)
	if latestNonPass == nil {
		return nil
	}
	guarded := LatestVerifierRemediationResumeAttemptEvaluation(records)
	closure := LatestReverifyClosureEvaluation(current, history, records)
	if guarded == nil && closure == nil {
		return nil
	}
	status := "pending_guarded_remediation"
	if closure != nil {
		status = "pass_closed"
	}
	chain := &VerifierRemediationClosureChain{
		Status:                    status,
		LatestNonPassVerification: describePromptVerificationSnapshot(*latestNonPass),
		LatestNonPassSummary:      strings.TrimSpace(latestNonPass.Summary),
		LatestNonPassReport:       strings.TrimSpace(latestNonPass.ReportPath),
	}
	if guarded != nil {
		chain.GuardedRemediationSummary = strings.TrimSpace(guarded.Summary)
		chain.RemediationPausePointID = EvaluationRecordPausePointID(*guarded)
		chain.RemediationResumeAttemptID = EvaluationRecordResumeAttemptID(*guarded)
		chain.GuardStatus = EvaluationRecordRecoveryBoundaryGuardStatus(*guarded)
		chain.RequiredAuthority = EvaluationRecordRecoveryBoundaryRequiredAuthority(*guarded)
		chain.BoundarySource = EvaluationRecordRecoveryBoundarySource(*guarded)
		chain.ExpectedTargets = append([]string(nil), EvaluationRecordExpectedTargets(*guarded)...)
	}
	if closure != nil {
		chain.PassClosureSummary = strings.TrimSpace(closure.Summary)
		chain.PassClosureReport = strings.TrimSpace(closure.ReportPath)
		if chain.PassClosureReport == "" {
			chain.PassClosureReport = EvaluationRecordVerificationReportPath(*closure)
		}
		if chain.RemediationPausePointID == "" {
			chain.RemediationPausePointID = EvaluationRecordDetailValue(*closure, "remediation_pause_point_id")
		}
		if chain.RemediationResumeAttemptID == "" {
			chain.RemediationResumeAttemptID = EvaluationRecordDetailValue(*closure, "remediation_resume_attempt_id")
		}
		if len(chain.ExpectedTargets) == 0 {
			chain.ExpectedTargets = evaluationRecordDetailValues(*closure, "remediation_expected_targets")
		}
	}
	return chain
}

func LatestRecoveryInspectionSummary(current *VerificationSnapshot, history []VerificationSnapshot, records []EvaluationRecord, limit int, operatorSource string) *RecoveryInspectionSummary {
	decision := LatestRecoveryDecision(records, limit)
	proposal := LatestRecoveryActionProposal(records, limit)
	request := LatestRecoveryActionRequest(records, limit, operatorSource)
	boundary := LatestRecoveryExecutionBoundary(records, limit, operatorSource)
	closure := LatestVerifierRemediationClosureChain(current, history, records)
	closureEvidence := LatestReverifyClosureEvaluation(current, history, records)
	if closureEvidence == nil {
		closureEvidence = latestReverifyClosureEvidenceRecord(records)
	}
	if decision == nil && proposal == nil && request == nil && boundary == nil && closure == nil {
		return nil
	}
	summary := &RecoveryInspectionSummary{}
	if decision != nil {
		summary.DecisionCategory = strings.TrimSpace(decision.Category)
		summary.SourceCause = strings.TrimSpace(decision.SourceCause)
		summary.PausePointID = strings.TrimSpace(decision.PausePointID)
		summary.ResumeAttemptID = strings.TrimSpace(decision.ResumeAttemptID)
		summary.Reason = strings.TrimSpace(decision.Reason)
		summary.Retryable = strings.TrimSpace(decision.Retryable)
		summary.Allowed = strings.TrimSpace(decision.Allowed)
		summary.ExpectedTargets = appendUniqueValues(summary.ExpectedTargets, decision.ExpectedTargets...)
		summary.Summary = strings.TrimSpace(decision.Summary)
	}
	if proposal != nil {
		summary.ActionKind = strings.TrimSpace(proposal.ActionKind)
		if summary.SourceCause == "" {
			summary.SourceCause = strings.TrimSpace(proposal.SourceCause)
		}
		if summary.DecisionCategory == "" {
			summary.DecisionCategory = strings.TrimSpace(proposal.DecisionCategory)
		}
		if summary.PausePointID == "" {
			summary.PausePointID = strings.TrimSpace(proposal.PausePointID)
		}
		if summary.ResumeAttemptID == "" {
			summary.ResumeAttemptID = strings.TrimSpace(proposal.ResumeAttemptID)
		}
		if summary.Reason == "" {
			summary.Reason = strings.TrimSpace(proposal.Reason)
		}
		summary.RequiredAuthority = strings.TrimSpace(proposal.RequiredAuthority)
		summary.ExpectedTargets = appendUniqueValues(summary.ExpectedTargets, proposal.ExpectedTargets...)
		if summary.Summary == "" {
			summary.Summary = strings.TrimSpace(proposal.Summary)
		}
	}
	if request != nil {
		if summary.ActionKind == "" {
			summary.ActionKind = strings.TrimSpace(request.ActionKind)
		}
		if summary.RequiredAuthority == "" {
			summary.RequiredAuthority = strings.TrimSpace(request.RequiredAuthority)
		}
		if summary.PausePointID == "" {
			summary.PausePointID = strings.TrimSpace(request.PausePointID)
		}
		if summary.ResumeAttemptID == "" {
			summary.ResumeAttemptID = strings.TrimSpace(request.ResumeAttemptID)
		}
		summary.ExpectedTargets = appendUniqueValues(summary.ExpectedTargets, request.ExpectedTargets...)
	}
	if boundary != nil {
		summary.GuardStatus = strings.TrimSpace(boundary.GuardStatus)
		if summary.ActionKind == "" {
			summary.ActionKind = strings.TrimSpace(boundary.ActionKind)
		}
		if summary.RequiredAuthority == "" {
			summary.RequiredAuthority = strings.TrimSpace(boundary.RequiredAuthority)
		}
		if summary.PausePointID == "" {
			summary.PausePointID = strings.TrimSpace(boundary.PausePointID)
		}
		if summary.ResumeAttemptID == "" {
			summary.ResumeAttemptID = strings.TrimSpace(boundary.ResumeAttemptID)
		}
		summary.ExpectedTargets = appendUniqueValues(summary.ExpectedTargets, boundary.ExpectedTargets...)
	}
	if closure != nil {
		summary.ClosureStatus = strings.TrimSpace(closure.Status)
		if summary.PausePointID == "" {
			summary.PausePointID = strings.TrimSpace(closure.RemediationPausePointID)
		}
		if summary.ResumeAttemptID == "" {
			summary.ResumeAttemptID = strings.TrimSpace(closure.RemediationResumeAttemptID)
		}
		if summary.DecisionCategory != "closed" && summary.GuardStatus == "" {
			summary.GuardStatus = strings.TrimSpace(closure.GuardStatus)
		}
		if summary.DecisionCategory != "closed" && summary.RequiredAuthority == "" {
			summary.RequiredAuthority = strings.TrimSpace(closure.RequiredAuthority)
		}
		summary.ExpectedTargets = appendUniqueValues(summary.ExpectedTargets, closure.ExpectedTargets...)
	}
	if summary.DecisionCategory == "closed" && summary.ClosureStatus == "" && closureEvidence != nil {
		summary.ClosureStatus = "pass_closed"
	}
	if summary.DecisionCategory == "closed" && summary.ClosureStatus == "pass_closed" && LatestNonPassVerificationSnapshot(current, history) == nil {
		if summary.Reason == "" {
			summary.Reason = "bounded_history_window"
		}
		if summary.Summary == "" {
			if closureEvidence != nil && strings.TrimSpace(closureEvidence.Summary) != "" {
				summary.Summary = strings.TrimSpace(closureEvidence.Summary)
			} else {
				summary.Summary = "Verifier PASS closed the remediation chain from bounded history; keep the closure coordinates for audit."
			}
		}
	}
	if summary.DecisionCategory == "" && summary.ActionKind == "" && summary.GuardStatus == "" && summary.ClosureStatus == "" && summary.PausePointID == "" && summary.ResumeAttemptID == "" && len(summary.ExpectedTargets) == 0 {
		return nil
	}
	summary.Guidance = recoveryInspectionGuidance(*summary)
	return summary
}

func recoveryInspectionGuidance(summary RecoveryInspectionSummary) string {
	switch {
	case strings.TrimSpace(summary.DecisionCategory) == "closed" && strings.TrimSpace(summary.ClosureStatus) == "pass_closed":
		return "Verifier PASS closed the remediation chain; keep the closure coordinates for audit."
	case strings.TrimSpace(summary.ClosureStatus) == "pending_guarded_remediation":
		return "Guarded remediation is pending verifier closure; keep repair and reverify explicit."
	case strings.TrimSpace(summary.ActionKind) == "plan_failed_node_retry":
		return "A failed workflow node is retryable; inspect the pause point before planning a bounded retry."
	case strings.TrimSpace(summary.ActionKind) == "request_tool_approval":
		return "Tool approval is required; review the pending approval before replaying guarded work."
	case strings.TrimSpace(summary.DecisionCategory) == "blocked":
		return "Recovery is blocked by policy; inspect the reason before starting new work."
	case strings.TrimSpace(summary.DecisionCategory) == "manual_required":
		return "Manual recovery review is required; inspect the recorded coordinates before acting."
	case strings.TrimSpace(summary.DecisionCategory) == "retryable":
		return "Recovery may be retryable; keep retry planning explicit and bounded."
	default:
		return "Recovery summary is inspection-only; do not treat it as execution authority."
	}
}

func latestReverifyClosureEvidenceRecord(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "verification_reverify_covered" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

type RecoveryDecision struct {
	Category        string
	SourceCause     string
	Summary         string
	PausePointID    string
	ResumeAttemptID string
	Reason          string
	Retryable       string
	Allowed         string
	ExpectedTargets []string
}

func LatestRecoveryDecision(records []EvaluationRecord, limit int) *RecoveryDecision {
	chain := LatestRecoveryEvidenceChain(records, limit)
	if len(chain) == 0 {
		return nil
	}
	for _, record := range chain {
		if decision := recoveryDecisionFromRecord(record); decision != nil {
			return decision
		}
	}
	return nil
}

func recoveryDecisionFromRecord(record EvaluationRecord) *RecoveryDecision {
	cause := strings.TrimSpace(record.Cause)
	category := ""
	switch cause {
	case "verification_reverify_covered", "tool_approval_replayed", "tool_approval_replay_skipped":
		category = "closed"
	case "tool_approval_replay_failed":
		category = "retryable"
	case "tool_approval_required", "verification_reverify_fix_attempt", "verifier_remediation_pause_point":
		category = "manual_required"
	case "failed_node_pause_point":
		if strings.TrimSpace(EvaluationRecordRetryable(record)) == "true" {
			category = "retryable"
		} else {
			category = "manual_required"
		}
	case "resume_policy_decision":
		if strings.TrimSpace(EvaluationRecordAllowed(record)) == "false" {
			category = "blocked"
		} else if strings.TrimSpace(EvaluationRecordRetryable(record)) == "true" {
			category = "retryable"
		} else {
			category = "manual_required"
		}
	case "resume_attempt_recorded":
		switch strings.TrimSpace(EvaluationRecordResumeAttemptStatus(record)) {
		case "completed", "skipped":
			category = "closed"
		case "failed":
			category = "retryable"
		default:
			category = "manual_required"
		}
	case "verifier_remediation_resume_attempt":
		if strings.TrimSpace(EvaluationRecordAllowed(record)) == "false" {
			category = "blocked"
		} else {
			category = "manual_required"
		}
	case "node_work_evidence":
		switch strings.TrimSpace(EvaluationRecordRecoveryKind(record)) {
		case "approval_replay":
			category = "closed"
		case "approval_required", "verifier_remediation":
			category = "manual_required"
		default:
			return nil
		}
	default:
		return nil
	}
	return &RecoveryDecision{
		Category:        category,
		SourceCause:     cause,
		Summary:         strings.TrimSpace(record.Summary),
		PausePointID:    EvaluationRecordPausePointID(record),
		ResumeAttemptID: EvaluationRecordResumeAttemptID(record),
		Reason:          EvaluationRecordReason(record),
		Retryable:       EvaluationRecordRetryable(record),
		Allowed:         EvaluationRecordAllowed(record),
		ExpectedTargets: append([]string(nil), EvaluationRecordExpectedTargets(record)...),
	}
}

type RecoveryActionProposal struct {
	ActionKind        string
	SourceCause       string
	DecisionCategory  string
	PausePointID      string
	ResumeAttemptID   string
	Reason            string
	ExpectedTargets   []string
	RequiredAuthority string
	Summary           string
}

func LatestRecoveryActionProposal(records []EvaluationRecord, limit int) *RecoveryActionProposal {
	decision := LatestRecoveryDecision(records, limit)
	if decision == nil || decision.Category == "closed" {
		return nil
	}
	actionKind := "manual_review"
	requiredAuthority := "operator"
	switch decision.SourceCause {
	case "tool_approval_required", "node_work_evidence":
		if decision.SourceCause == "node_work_evidence" && decision.PausePointID == "" {
			actionKind = "manual_review"
			break
		}
		actionKind = "request_tool_approval"
		requiredAuthority = "operator_approval"
	case "tool_approval_replay_failed", "resume_attempt_recorded", "resume_policy_decision":
		if decision.Category == "retryable" {
			actionKind = "start_recovery_retry"
			requiredAuthority = "runtime_resume_policy"
		}
	case "failed_node_pause_point":
		if decision.Category == "retryable" {
			actionKind = "plan_failed_node_retry"
			requiredAuthority = "workflow_scheduler"
		}
	case "verifier_remediation_pause_point", "verifier_remediation_resume_attempt", "verification_reverify_fix_attempt":
		actionKind = "run_verifier_remediation_repair"
		requiredAuthority = "verifier_reverify"
	}
	return &RecoveryActionProposal{
		ActionKind:        actionKind,
		SourceCause:       decision.SourceCause,
		DecisionCategory:  decision.Category,
		PausePointID:      decision.PausePointID,
		ResumeAttemptID:   decision.ResumeAttemptID,
		Reason:            decision.Reason,
		ExpectedTargets:   append([]string(nil), decision.ExpectedTargets...),
		RequiredAuthority: requiredAuthority,
		Summary:           decision.Summary,
	}
}

type RecoveryActionRequest struct {
	ActionKind        string
	SourceCause       string
	DecisionCategory  string
	PausePointID      string
	ResumeAttemptID   string
	Reason            string
	ExpectedTargets   []string
	RequiredAuthority string
	OperatorSource    string
	Summary           string
}

func LatestRecoveryActionRequest(records []EvaluationRecord, limit int, operatorSource string) *RecoveryActionRequest {
	proposal := LatestRecoveryActionProposal(records, limit)
	if proposal == nil {
		return nil
	}
	if strings.TrimSpace(proposal.ActionKind) == "" {
		return nil
	}
	return &RecoveryActionRequest{
		ActionKind:        proposal.ActionKind,
		SourceCause:       proposal.SourceCause,
		DecisionCategory:  proposal.DecisionCategory,
		PausePointID:      proposal.PausePointID,
		ResumeAttemptID:   proposal.ResumeAttemptID,
		Reason:            proposal.Reason,
		ExpectedTargets:   append([]string(nil), proposal.ExpectedTargets...),
		RequiredAuthority: proposal.RequiredAuthority,
		OperatorSource:    strings.TrimSpace(operatorSource),
		Summary:           proposal.Summary,
	}
}

type RecoveryExecutionBoundary struct {
	ActionKind        string
	SourceCause       string
	DecisionCategory  string
	PausePointID      string
	ResumeAttemptID   string
	Reason            string
	ExpectedTargets   []string
	RequiredAuthority string
	OperatorSource    string
	VerifierRequired  bool
	ApprovalRequired  bool
	GuardStatus       string
	Summary           string
}

func LatestRecoveryExecutionBoundary(records []EvaluationRecord, limit int, operatorSource string) *RecoveryExecutionBoundary {
	request := LatestRecoveryActionRequest(records, limit, operatorSource)
	if request == nil {
		return nil
	}
	requiredAuthority := strings.TrimSpace(request.RequiredAuthority)
	if requiredAuthority == "" {
		return nil
	}
	boundary := &RecoveryExecutionBoundary{
		ActionKind:        strings.TrimSpace(request.ActionKind),
		SourceCause:       strings.TrimSpace(request.SourceCause),
		DecisionCategory:  strings.TrimSpace(request.DecisionCategory),
		PausePointID:      strings.TrimSpace(request.PausePointID),
		ResumeAttemptID:   strings.TrimSpace(request.ResumeAttemptID),
		Reason:            strings.TrimSpace(request.Reason),
		ExpectedTargets:   append([]string(nil), request.ExpectedTargets...),
		RequiredAuthority: requiredAuthority,
		OperatorSource:    strings.TrimSpace(request.OperatorSource),
		GuardStatus:       "manual_review_required",
		Summary:           strings.TrimSpace(request.Summary),
	}
	if boundary.ActionKind == "run_verifier_remediation_repair" && requiredAuthority == "verifier_reverify" && strings.TrimSpace(boundary.PausePointID) != "" {
		boundary.VerifierRequired = true
		boundary.ApprovalRequired = false
		boundary.GuardStatus = "guarded_ready"
		if boundary.Summary == "" {
			boundary.Summary = "Recovery action request may enter the guarded verifier remediation path."
		}
		return boundary
	}
	boundary.ApprovalRequired = true
	if boundary.Summary == "" {
		boundary.Summary = "Recovery action request requires manual review before execution."
	}
	return boundary
}

func isRecoveryEvidenceRecord(record EvaluationRecord) bool {
	switch strings.TrimSpace(record.Cause) {
	case "failed_node_pause_point",
		"resume_attempt_recorded",
		"resume_policy_decision",
		"tool_approval_required",
		"tool_approval_replayed",
		"tool_approval_replay_failed",
		"tool_approval_replay_skipped",
		"verifier_remediation_pause_point",
		"verifier_remediation_resume_attempt",
		"verification_reverify_fix_attempt",
		"verification_reverify_covered":
		return true
	case "node_work_evidence":
		return EvaluationRecordRecoveryKind(record) != ""
	default:
		return false
	}
}

func LatestRunLifecycleEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "run_lifecycle_updated" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestToolPermissionDenialEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "tool_permission_denied" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestToolFailureRetryEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		switch strings.TrimSpace(record.Cause) {
		case "tool_execution_failed", "tool_not_registered", "tool_permission_denied":
			cloned := record
			cloned.Details = append([]string(nil), record.Details...)
			return &cloned
		}
	}
	return nil
}

func TaskWorkspaceStatus(baseStatus string, snapshot Snapshot) string {
	status := strings.TrimSpace(baseStatus)
	if status == "" {
		status = "stable"
	}
	if len(PendingToolApprovalEvaluations(snapshot.EvaluationRecords)) > 0 {
		return "awaiting_approval"
	}
	if VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory) == "pending reverify for latest non-pass verification" {
		return "awaiting_verification"
	}
	if closure := LatestFailedNodeRetryAttemptClosureEvidence(snapshot.EvaluationRecords); closure != nil {
		if !closure.ClosureReady || len(closure.MissingEvidence) > 0 {
			return "awaiting_retry_closure"
		}
	}
	if lifecycle := LatestRunLifecycleEvaluation(snapshot.EvaluationRecords); lifecycle != nil {
		if lifecycleStatus := strings.TrimSpace(EvaluationRecordDetailValue(*lifecycle, "status")); lifecycleStatus != "" {
			return lifecycleStatus
		}
	}
	return status
}

type TaskFinalityReadiness struct {
	TaskStatus            string
	RuntimeTruthReadiness string
	NeedsReview           bool
	BlockerCount          int
}

func TaskFinalityReadinessForTask(baseStatus string, snapshot Snapshot) TaskFinalityReadiness {
	status := TaskWorkspaceStatus(baseStatus, snapshot)
	readiness := TaskFinalityReadiness{
		TaskStatus:            status,
		RuntimeTruthReadiness: "ok",
	}
	switch strings.TrimSpace(status) {
	case "awaiting_approval", "awaiting_retry_closure":
		readiness.RuntimeTruthReadiness = "review"
		readiness.NeedsReview = true
		readiness.BlockerCount = 1
	case "awaiting_verification":
		readiness.RuntimeTruthReadiness = "review"
		readiness.NeedsReview = true
		if VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory) != "pending reverify for latest non-pass verification" {
			readiness.BlockerCount = 1
		}
	}
	return readiness
}

func describePromptVerificationSnapshot(snapshot VerificationSnapshot) string {
	tool := strings.TrimSpace(snapshot.Tool)
	if tool == "" {
		tool = "n/a"
	}
	verdict := strings.TrimSpace(snapshot.Verdict)
	if verdict == "" {
		verdict = "n/a"
	}
	updatedAt := "n/a"
	if !snapshot.UpdatedAt.IsZero() {
		updatedAt = snapshot.UpdatedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("%s | %s | %s", updatedAt, tool, verdict)
}

func evaluationRecordDetailValues(record EvaluationRecord, key string) []string {
	prefix := strings.TrimSpace(key) + ":"
	if prefix == ":" {
		return nil
	}
	values := make([]string, 0, 2)
	for _, detail := range record.Details {
		trimmed := strings.TrimSpace(detail)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if remainder == "" {
			continue
		}
		for _, value := range strings.Split(remainder, ",") {
			trimmedValue := strings.TrimSpace(value)
			if trimmedValue == "" {
				continue
			}
			duplicate := false
			for _, existing := range values {
				if existing == trimmedValue {
					duplicate = true
					break
				}
			}
			if !duplicate {
				values = append(values, trimmedValue)
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func appendUniqueValues(values []string, additions ...string) []string {
	for _, addition := range additions {
		trimmed := strings.TrimSpace(addition)
		if trimmed == "" {
			continue
		}
		duplicate := false
		for _, existing := range values {
			if strings.TrimSpace(existing) == trimmed {
				duplicate = true
				break
			}
		}
		if !duplicate {
			values = append(values, trimmed)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func EvaluationRecordDetailValue(record EvaluationRecord, key string) string {
	values := evaluationRecordDetailValues(record, key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func cloneLLMStatus(status *llm.ProviderStatus) *llm.ProviderStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	return &cloned
}

func cloneTelemetrySnapshot(snapshot *runtelemetry.Snapshot) *runtelemetry.Snapshot {
	if snapshot == nil {
		return nil
	}
	cloned := *snapshot
	return &cloned
}

func cloneVerificationSnapshot(snapshot *VerificationSnapshot) *VerificationSnapshot {
	if snapshot == nil {
		return nil
	}
	cloned := normalizeVerificationSnapshot(*snapshot)
	cloned.Warnings = append([]string(nil), snapshot.Warnings...)
	cloned.Checks = append([]string(nil), snapshot.Checks...)
	return &cloned
}

func cloneVerificationHistory(history []VerificationSnapshot) []VerificationSnapshot {
	if len(history) == 0 {
		return nil
	}
	cloned := make([]VerificationSnapshot, 0, len(history))
	for _, entry := range history {
		copyEntry := normalizeVerificationSnapshot(entry)
		copyEntry.Warnings = append([]string(nil), entry.Warnings...)
		copyEntry.Checks = append([]string(nil), entry.Checks...)
		cloned = append(cloned, copyEntry)
	}
	return cloned
}

func cloneWarmLessons(lessons []WarmLesson) []WarmLesson {
	if len(lessons) == 0 {
		return nil
	}
	cloned := make([]WarmLesson, len(lessons))
	copy(cloned, lessons)
	return cloned
}

func cloneEvolutionCandidates(candidates []EvolutionCandidate) []EvolutionCandidate {
	if len(candidates) == 0 {
		return nil
	}
	cloned := make([]EvolutionCandidate, len(candidates))
	copy(cloned, candidates)
	return cloned
}

func cloneProjectLessons(lessons []ProjectLesson) []ProjectLesson {
	if len(lessons) == 0 {
		return nil
	}
	cloned := make([]ProjectLesson, len(lessons))
	copy(cloned, lessons)
	return cloned
}

func cloneEvaluationRecords(records []EvaluationRecord) []EvaluationRecord {
	if len(records) == 0 {
		return nil
	}
	cloned := make([]EvaluationRecord, len(records))
	for index, record := range records {
		copyRecord := record
		copyRecord.Details = append([]string(nil), record.Details...)
		cloned[index] = copyRecord
	}
	return cloned
}
