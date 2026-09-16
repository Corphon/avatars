package planner

import "strings"

type RemediationActionProposal struct {
	ProposalID      string
	Kind            string
	Summary         string
	Intent          string
	ExpectedTargets []string
	FollowUpCommand string
}

// VerificationContext captures the current verification state: what was
// checked, what failed, and which targets are in scope for repair.
type VerificationContext struct {
	CurrentVerification        string
	CurrentVerificationSummary string
	LatestNonPassVerification  string
	TaskScopedFollowUp         string
	ReverifyStatus             string
	ExpectedTargets            []string
}

// RemediationActions captures the repair work: which remediation was
// attempted, its guard/approval status, and any derived proposals.
type RemediationActions struct {
	LatestRemediation           string
	LatestGuardedRemediation    string
	GuardedRemediationStatus    string
	GuardedRemediationAuthority string
	GuardedRemediationSource    string
	Proposals                   []RemediationActionProposal
}

// RecoveryContext captures recovery state: closure for verifier and
// node-retry paths, plus the recovery summary that guides the next run.
type RecoveryContext struct {
	VerifierRemediationClosureStatus      string
	VerifierRemediationClosure            string
	VerifierRemediationClosureReport      string
	VerifierRemediationClosurePausePoint  string
	VerifierRemediationClosureResume      string
	FailedNodeRetryClosureStatus          string
	FailedNodeRetryClosureReady           string
	FailedNodeRetryClosureVerifierGate    string
	FailedNodeRetryClosureVerifierVerdict string
	FailedNodeRetryClosureReport          string
	RecoverySummaryCategory               string
	RecoverySummaryAction                 string
	RecoverySummaryGuardStatus            string
	RecoverySummaryGuidance               string
}

// RemediationEnvelope carries the full remediation picture across three
// logical groups (see VerificationContext, RemediationActions, and
// RecoveryContext for the decomposition). The fields remain flat for
// backward compatibility; the three accessor methods let callers that
// want the structured grouping opt in without breaking existing literals.
type RemediationEnvelope struct {
	CurrentVerification                   string
	CurrentVerificationSummary            string
	LatestNonPassVerification             string
	TaskScopedFollowUp                    string
	ReverifyStatus                        string
	LatestRemediation                     string
	LatestGuardedRemediation              string
	GuardedRemediationStatus              string
	GuardedRemediationAuthority           string
	GuardedRemediationSource              string
	VerifierRemediationClosureStatus      string
	VerifierRemediationClosure            string
	VerifierRemediationClosureReport      string
	VerifierRemediationClosurePausePoint  string
	VerifierRemediationClosureResume      string
	FailedNodeRetryClosureStatus          string
	FailedNodeRetryClosureReady           string
	FailedNodeRetryClosureVerifierGate    string
	FailedNodeRetryClosureVerifierVerdict string
	FailedNodeRetryClosureReport          string
	RecoverySummaryCategory               string
	RecoverySummaryAction                 string
	RecoverySummaryGuardStatus            string
	RecoverySummaryGuidance               string
	ExpectedTargets                       []string
	Proposals                             []RemediationActionProposal
}

// Verification returns the verification sub-context.
func (r RemediationEnvelope) Verification() VerificationContext {
	return VerificationContext{
		CurrentVerification:        r.CurrentVerification,
		CurrentVerificationSummary: r.CurrentVerificationSummary,
		LatestNonPassVerification:  r.LatestNonPassVerification,
		TaskScopedFollowUp:         r.TaskScopedFollowUp,
		ReverifyStatus:             r.ReverifyStatus,
		ExpectedTargets:            r.ExpectedTargets,
	}
}

// Actions returns the remediation actions sub-context.
func (r RemediationEnvelope) Actions() RemediationActions {
	return RemediationActions{
		LatestRemediation:           r.LatestRemediation,
		LatestGuardedRemediation:    r.LatestGuardedRemediation,
		GuardedRemediationStatus:    r.GuardedRemediationStatus,
		GuardedRemediationAuthority: r.GuardedRemediationAuthority,
		GuardedRemediationSource:    r.GuardedRemediationSource,
		Proposals:                   r.Proposals,
	}
}

// Recovery returns the recovery sub-context.
func (r RemediationEnvelope) Recovery() RecoveryContext {
	return RecoveryContext{
		VerifierRemediationClosureStatus:      r.VerifierRemediationClosureStatus,
		VerifierRemediationClosure:            r.VerifierRemediationClosure,
		VerifierRemediationClosureReport:      r.VerifierRemediationClosureReport,
		VerifierRemediationClosurePausePoint:  r.VerifierRemediationClosurePausePoint,
		VerifierRemediationClosureResume:      r.VerifierRemediationClosureResume,
		FailedNodeRetryClosureStatus:          r.FailedNodeRetryClosureStatus,
		FailedNodeRetryClosureReady:           r.FailedNodeRetryClosureReady,
		FailedNodeRetryClosureVerifierGate:    r.FailedNodeRetryClosureVerifierGate,
		FailedNodeRetryClosureVerifierVerdict: r.FailedNodeRetryClosureVerifierVerdict,
		FailedNodeRetryClosureReport:          r.FailedNodeRetryClosureReport,
		RecoverySummaryCategory:               r.RecoverySummaryCategory,
		RecoverySummaryAction:                 r.RecoverySummaryAction,
		RecoverySummaryGuardStatus:            r.RecoverySummaryGuardStatus,
		RecoverySummaryGuidance:               r.RecoverySummaryGuidance,
	}
}

type Context struct {
	Remediation *RemediationEnvelope
	Feedback    string // P9: rejection feedback from previous runs
	ArchSummary string // Architecture summary with Registration Points for Planner awareness
}

type Avatar struct {
	ID             string
	Role           string
	InstanceID     string // A1: instance number for same-role parallelism (e.g. "1", "2", "3")
	Responsibility string
}

type WorkflowNode struct {
	ID           string
	Title        string
	AssignedRole string
	DependsOn    []string
}

// ExecutionPlan is a single-run execution plan that assigns avatars to workflow
// nodes and carries remediation context. P1: Renamed from Plan to distinguish
// from workflow.ProjectPlanMeta (the multi-run project roadmap).
// Plan is kept as a backward-compatible alias.
type Plan struct {
	Title       string
	Summary     string
	Avatars     []Avatar
	Nodes       []WorkflowNode
	Remediation *RemediationEnvelope
	Feedback    string // P9: user rejection feedback for Planner context
	ArchSummary string // Architecture Registration Points for Builder context
}

// ExecutionPlan is the canonical name for a single-run execution plan.
// P1: This alias makes the distinction from workflow.ProjectPlanMeta explicit.
type ExecutionPlan = Plan

// WithActivePhase returns a copy of the plan with the active project phase
// number annotated. P3: Connects ExecutionPlan to ProjectPlanMeta.ActivePhase
// so the single-run plan knows which project phase it belongs to.
func (p Plan) WithActivePhase(phase int) Plan {
	p.Avatars = append([]Avatar(nil), p.Avatars...)
	return p
}

// SingleStepRemediation is an alias for RemediationActionProposal that clarifies
// this is a single-step fix (verification failure → one repair action), not a
// full phase rollback. P4.
type SingleStepRemediation = RemediationActionProposal

func Build(task string) Plan {
	return BuildWithContext(task, Context{})
}

func BuildWithContext(task string, context Context) Plan {
	title := strings.TrimSpace(task)
	if title == "" {
		title = "Untitled task"
	}

	// Assess complexity to select the right-sized pipeline.
	// This directly addresses the "workflow-shaped tool" anti-pattern:
	// instead of always running the full 5-avatar chain, we match the
	// pipeline to the task.
	assessment := Assess(title)

	var avatars []Avatar
	var nodes []WorkflowNode
	var summary string

	switch assessment.Level {
	case ComplexityTrivial:
		avatars = []Avatar{
			{ID: "avatar-direct", Role: "Direct", Responsibility: "execute the single concrete action"},
		}
		nodes = []WorkflowNode{
			{ID: "node-direct", Title: "Execute direct action", AssignedRole: "Direct"},
		}
		summary = "Trivial request dispatched as a single direct action."
	case ComplexitySmall:
		avatars = []Avatar{
			{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task and assign workflow nodes"},
			{ID: "avatar-builder", Role: "Builder", Responsibility: "propose the smallest viable implementation path"},
			{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "merge validated outputs into a user-ready summary"},
		}
		// C3: Planner/Synthesizer stay as avatars (lifecycle at Build / run tail).
		nodes = []WorkflowNode{
			{ID: "node-build", Title: "Shape the smallest viable change", AssignedRole: "Builder"},
		}
		summary = "Small request: planner -> builder -> synthesizer (3 avatars; DAG is builder-only)."
	case ComplexityMedium:
		avatars = []Avatar{
			{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task and assign workflow nodes"},
			{ID: "avatar-researcher", Role: "Researcher", Responsibility: "survey repository context and collect facts"},
			{ID: "avatar-builder", Role: "Builder", Responsibility: "propose the smallest viable implementation path"},
			{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "merge validated outputs into a user-ready summary"},
		}
		nodes = []WorkflowNode{
			{ID: "node-survey", Title: "Survey current repository context", AssignedRole: "Researcher"},
			{ID: "node-build", Title: "Shape the smallest viable change", AssignedRole: "Builder", DependsOn: []string{"node-survey"}},
		}
		summary = "Medium request: planner -> researcher -> builder -> synthesizer (4 avatars, no critic; DAG omits ceremonial slots)."
	default: // ComplexityLarge or unknown
		avatars = []Avatar{
			{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task and assign workflow nodes"},
			{ID: "avatar-researcher", Role: "Researcher", Responsibility: "survey repository context and collect facts"},
			{ID: "avatar-builder", Role: "Builder", Responsibility: "propose the smallest viable implementation path"},
			{ID: "avatar-critic", Role: "Critic", Responsibility: "challenge assumptions and capture risks"},
			{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "merge validated outputs into a user-ready summary"},
		}
		nodes = []WorkflowNode{
			{ID: "node-survey", Title: "Survey current repository context", AssignedRole: "Researcher"},
			{ID: "node-build", Title: "Shape the smallest viable change", AssignedRole: "Builder", DependsOn: []string{"node-survey"}},
			{ID: "node-review", Title: "Review risks and challenge gaps", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
		}
		summary = "Large request: full 5-avatar chain with critic; DAG is survey -> build -> review."
	}

	plan := Plan{
		Title:   title,
		Summary: summary,
		Avatars: avatars,
		Nodes:   nodes,
	}
	if context.Remediation != nil {
		trimmed := NormalizeRemediationEnvelope(*context.Remediation)
		if !trimmed.isEmpty() {
			plan.Remediation = &trimmed
		}
	}
	if context.Feedback != "" {
		plan.Feedback = context.Feedback
	}
	plan.Nodes = WithoutCeremonialDAGNodes(plan.Nodes)
	return plan
}

func (p Plan) AvatarIDByRole(role string) string {
	for _, avatar := range p.Avatars {
		if avatar.Role == role {
			return avatar.ID
		}
	}
	return ""
}

// AvatarIDsByRole returns all avatar IDs for the given role. A1: Enables
// same-role multi-instance parallelism (e.g., 3 Researchers scanning
// different directories simultaneously).
func (p Plan) AvatarIDsByRole(role string) []string {
	var ids []string
	for _, avatar := range p.Avatars {
		if avatar.Role == role {
			ids = append(ids, avatar.ID)
		}
	}
	return ids
}

func NormalizeRemediationEnvelope(envelope RemediationEnvelope) RemediationEnvelope {
	return cloneRemediationEnvelope(envelope)
}

func cloneRemediationEnvelope(envelope RemediationEnvelope) RemediationEnvelope {
	// Trim verification context fields.
	envelope.CurrentVerification = strings.TrimSpace(envelope.CurrentVerification)
	envelope.CurrentVerificationSummary = strings.TrimSpace(envelope.CurrentVerificationSummary)
	envelope.LatestNonPassVerification = strings.TrimSpace(envelope.LatestNonPassVerification)
	envelope.TaskScopedFollowUp = strings.TrimSpace(envelope.TaskScopedFollowUp)
	envelope.ReverifyStatus = strings.TrimSpace(envelope.ReverifyStatus)
	// Trim remediation action fields.
	envelope.LatestRemediation = strings.TrimSpace(envelope.LatestRemediation)
	envelope.LatestGuardedRemediation = strings.TrimSpace(envelope.LatestGuardedRemediation)
	envelope.GuardedRemediationStatus = strings.TrimSpace(envelope.GuardedRemediationStatus)
	envelope.GuardedRemediationAuthority = strings.TrimSpace(envelope.GuardedRemediationAuthority)
	envelope.GuardedRemediationSource = strings.TrimSpace(envelope.GuardedRemediationSource)
	// Trim recovery context fields.
	envelope.VerifierRemediationClosureStatus = strings.TrimSpace(envelope.VerifierRemediationClosureStatus)
	envelope.VerifierRemediationClosure = strings.TrimSpace(envelope.VerifierRemediationClosure)
	envelope.VerifierRemediationClosureReport = strings.TrimSpace(envelope.VerifierRemediationClosureReport)
	envelope.VerifierRemediationClosurePausePoint = strings.TrimSpace(envelope.VerifierRemediationClosurePausePoint)
	envelope.VerifierRemediationClosureResume = strings.TrimSpace(envelope.VerifierRemediationClosureResume)
	envelope.FailedNodeRetryClosureStatus = strings.TrimSpace(envelope.FailedNodeRetryClosureStatus)
	envelope.FailedNodeRetryClosureReady = strings.TrimSpace(envelope.FailedNodeRetryClosureReady)
	envelope.FailedNodeRetryClosureVerifierGate = strings.TrimSpace(envelope.FailedNodeRetryClosureVerifierGate)
	envelope.FailedNodeRetryClosureVerifierVerdict = strings.TrimSpace(envelope.FailedNodeRetryClosureVerifierVerdict)
	envelope.FailedNodeRetryClosureReport = strings.TrimSpace(envelope.FailedNodeRetryClosureReport)
	envelope.RecoverySummaryCategory = strings.TrimSpace(envelope.RecoverySummaryCategory)
	envelope.RecoverySummaryAction = strings.TrimSpace(envelope.RecoverySummaryAction)
	envelope.RecoverySummaryGuardStatus = strings.TrimSpace(envelope.RecoverySummaryGuardStatus)
	envelope.RecoverySummaryGuidance = strings.TrimSpace(envelope.RecoverySummaryGuidance)
	// Deduplicate and trim targets.
	cleanTargets := make([]string, 0, len(envelope.ExpectedTargets))
	for _, target := range envelope.ExpectedTargets {
		if trimmed := strings.TrimSpace(target); trimmed != "" {
			dup := false
			for _, existing := range cleanTargets {
				if existing == trimmed {
					dup = true
					break
				}
			}
			if !dup {
				cleanTargets = append(cleanTargets, trimmed)
			}
		}
	}
	envelope.ExpectedTargets = cleanTargets
	envelope.Proposals = cloneRemediationActionProposals(envelope.Proposals)
	if len(envelope.Proposals) == 0 {
		envelope.Proposals = deriveRemediationActionProposals(envelope)
	}
	return envelope
}

func (r RemediationEnvelope) isEmpty() bool {
	return r.Verification().isEmpty() && r.Actions().isEmpty() && r.Recovery().isEmpty()
}

func (v VerificationContext) isEmpty() bool {
	return strings.TrimSpace(v.CurrentVerification) == "" &&
		strings.TrimSpace(v.CurrentVerificationSummary) == "" &&
		strings.TrimSpace(v.LatestNonPassVerification) == "" &&
		strings.TrimSpace(v.TaskScopedFollowUp) == "" &&
		strings.TrimSpace(v.ReverifyStatus) == "" &&
		len(v.ExpectedTargets) == 0
}

func (a RemediationActions) isEmpty() bool {
	return strings.TrimSpace(a.LatestRemediation) == "" &&
		strings.TrimSpace(a.LatestGuardedRemediation) == "" &&
		strings.TrimSpace(a.GuardedRemediationStatus) == "" &&
		strings.TrimSpace(a.GuardedRemediationAuthority) == "" &&
		strings.TrimSpace(a.GuardedRemediationSource) == "" &&
		len(a.Proposals) == 0
}

func (c RecoveryContext) isEmpty() bool {
	return strings.TrimSpace(c.VerifierRemediationClosureStatus) == "" &&
		strings.TrimSpace(c.VerifierRemediationClosure) == "" &&
		strings.TrimSpace(c.VerifierRemediationClosureReport) == "" &&
		strings.TrimSpace(c.VerifierRemediationClosurePausePoint) == "" &&
		strings.TrimSpace(c.VerifierRemediationClosureResume) == "" &&
		strings.TrimSpace(c.FailedNodeRetryClosureStatus) == "" &&
		strings.TrimSpace(c.FailedNodeRetryClosureReady) == "" &&
		strings.TrimSpace(c.FailedNodeRetryClosureVerifierGate) == "" &&
		strings.TrimSpace(c.FailedNodeRetryClosureVerifierVerdict) == "" &&
		strings.TrimSpace(c.FailedNodeRetryClosureReport) == "" &&
		strings.TrimSpace(c.RecoverySummaryCategory) == "" &&
		strings.TrimSpace(c.RecoverySummaryAction) == "" &&
		strings.TrimSpace(c.RecoverySummaryGuardStatus) == "" &&
		strings.TrimSpace(c.RecoverySummaryGuidance) == ""
}

func cloneRemediationActionProposals(proposals []RemediationActionProposal) []RemediationActionProposal {
	if len(proposals) == 0 {
		return nil
	}
	cloned := make([]RemediationActionProposal, 0, len(proposals))
	for _, proposal := range proposals {
		proposal.ProposalID = strings.TrimSpace(proposal.ProposalID)
		proposal.Kind = strings.TrimSpace(proposal.Kind)
		proposal.Summary = strings.TrimSpace(proposal.Summary)
		proposal.Intent = strings.TrimSpace(proposal.Intent)
		proposal.FollowUpCommand = strings.TrimSpace(proposal.FollowUpCommand)
		cleanTargets := make([]string, 0, len(proposal.ExpectedTargets))
		for _, target := range proposal.ExpectedTargets {
			trimmed := strings.TrimSpace(target)
			if trimmed == "" {
				continue
			}
			duplicate := false
			for _, existing := range cleanTargets {
				if existing == trimmed {
					duplicate = true
					break
				}
			}
			if !duplicate {
				cleanTargets = append(cleanTargets, trimmed)
			}
		}
		proposal.ExpectedTargets = cleanTargets
		if proposal.ProposalID == "" && proposal.Kind == "" && proposal.Summary == "" && proposal.Intent == "" && proposal.FollowUpCommand == "" && len(proposal.ExpectedTargets) == 0 {
			continue
		}
		cloned = append(cloned, proposal)
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func deriveRemediationActionProposals(envelope RemediationEnvelope) []RemediationActionProposal {
	proposals := make([]RemediationActionProposal, 0, 2)
	if strings.TrimSpace(envelope.LatestNonPassVerification) != "" {
		intent := "Inspect the latest non-pass verification evidence before planning another bounded change."
		if strings.TrimSpace(envelope.CurrentVerificationSummary) != "" {
			intent = envelope.CurrentVerificationSummary
		}
		proposals = append(proposals, RemediationActionProposal{
			ProposalID:      "inspect-latest-non-pass",
			Kind:            "inspect_verification",
			Summary:         "Inspect the latest non-pass verification evidence before applying another bounded remediation step.",
			Intent:          intent,
			FollowUpCommand: strings.TrimSpace(envelope.TaskScopedFollowUp),
		})
	}
	if strings.TrimSpace(envelope.ReverifyStatus) != "" || len(envelope.ExpectedTargets) > 0 || strings.TrimSpace(envelope.LatestRemediation) != "" {
		intent := "Prepare the next bounded remediation attempt for the latest verifier issue."
		if strings.TrimSpace(envelope.LatestRemediation) != "" {
			intent = envelope.LatestRemediation
		}
		proposals = append(proposals, RemediationActionProposal{
			ProposalID:      "remediate-latest-non-pass",
			Kind:            "remediation_attempt",
			Summary:         "Prepare the next bounded remediation attempt and keep the rerun path explicit.",
			Intent:          intent,
			ExpectedTargets: append([]string(nil), envelope.ExpectedTargets...),
			FollowUpCommand: strings.TrimSpace(envelope.TaskScopedFollowUp),
		})
	}
	if len(proposals) == 0 {
		return nil
	}
	return proposals
}
