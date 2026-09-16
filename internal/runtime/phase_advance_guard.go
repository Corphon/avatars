package runtime

import (
	"strings"

	"avatars/internal/workflow"
)

// phaseAdvanceDecision is the single authority for whether this run may
// promote Active Phase (C2). loop.go defer and Critic only apply or copy it.
type phaseAdvanceDecision struct {
	Advanced    bool
	NextPhase   int
	ProjectDone bool
	Blocked     string
	HealthErr   string
	TodoDone    int
	TodoTotal   int
}

type phaseAdvanceInput struct {
	ProjectRoot  string
	LockedPhase  int
	BuildOK      bool
	HealthErr    string
	PhaseDocsOK  bool
	PlanningGate bool
	RunBlocked   bool
	Advance      func(projectRoot string, maxPhase int) (advanced bool, newPhase int)
}

// runBlocksPhaseAdvance reports whether end-of-run Critic phase advance must be
// suppressed (F14). needs_remediation / failed / hub verification exhaustion
// must not still promote Active Phase when checklist/build probe looks green.
func runBlocksPhaseAdvance(status string, result RunResult, runErr error) bool {
	// F102: provider 402/quota is not a compile failure. If disk is green,
	// do not hold phase advance or emit a second terminal "blocked" after
	// Critic already marked the project complete.
	if isSynthesisAvailabilityFailure(result) {
		if result.BuildOKKnown && !result.BuildOK {
			return true
		}
		sum := strings.ToLower(result.Summary)
		if strings.Contains(sum, "critic: verification failed") {
			return true
		}
		return false
	}
	s := strings.ToLower(strings.TrimSpace(status))
	switch s {
	case "needs_remediation", "failed", "awaiting_approval":
		return true
	}
	if runErr != nil {
		return true
	}
	sum := strings.ToLower(result.Summary)
	if strings.Contains(sum, "needs_remediation") ||
		strings.Contains(sum, "critic: verification failed") ||
		strings.Contains(sum, "user guidance needed") ||
		strings.Contains(sum, "interactive input is not available") {
		return true
	}
	st := strings.ToLower(strings.TrimSpace(result.SynthesisStatus))
	if strings.HasPrefix(st, "failed:") {
		return true
	}
	return false
}

func phaseBlockRank(reason string) int {
	switch strings.TrimSpace(reason) {
	case "checklist_incomplete":
		return 1
	case "delivery_incomplete":
		return 2
	case "user_phase_lock":
		return 3
	case "phase_docs_incomplete":
		return 4
	case "build_failed":
		return 5
	case "needs_remediation":
		return 6
	case "plan_incomplete":
		return 7
	default:
		return 0
	}
}

// mergePhaseBlock keeps the stronger reason so checklist_incomplete /
// build_failed / user_phase_lock cannot overwrite each other at random (C2.2).
func mergePhaseBlock(current, incoming string) string {
	if phaseBlockRank(incoming) > phaseBlockRank(current) {
		return incoming
	}
	if current == "" {
		return incoming
	}
	return current
}

func phaseAdvanceGateReason(in phaseAdvanceInput) string {
	if in.PlanningGate {
		return "plan_incomplete"
	}
	if in.RunBlocked {
		if !in.BuildOK {
			return "build_failed"
		}
		if !in.PhaseDocsOK {
			return "phase_docs_incomplete"
		}
		return "needs_remediation"
	}
	if !in.BuildOK {
		return "build_failed"
	}
	if !in.PhaseDocsOK {
		return "phase_docs_incomplete"
	}
	return ""
}

func decidePhaseAdvance(in phaseAdvanceInput) phaseAdvanceDecision {
	d := phaseAdvanceDecision{HealthErr: in.HealthErr}
	if gate := phaseAdvanceGateReason(in); gate != "" {
		d.Blocked = gate
		return d
	}
	advance := in.Advance
	if advance == nil {
		advance = workflow.CriticDecidePhaseAdvanceUpTo
	}
	ok, next := advance(in.ProjectRoot, in.LockedPhase)
	if ok {
		if next > 0 {
			d.Advanced = true
			d.NextPhase = next
			return d
		}
		if in.ProjectRoot != "" {
			if health := crossLangHealthCheck(in.ProjectRoot); health != "" {
				d.Blocked = "build_failed"
				d.HealthErr = health
				return d
			}
			if deliveryLooksEmpty(in.ProjectRoot) {
				d.Blocked = "delivery_incomplete"
				return d
			}
		}
		d.Advanced = true
		d.ProjectDone = true
		return d
	}
	if in.LockedPhase > 0 {
		d.Blocked = "user_phase_lock"
		return d
	}
	d.Blocked = "checklist_incomplete"
	if in.ProjectRoot != "" {
		d.TodoDone, d.TodoTotal = workflow.CountTodoProgress(in.ProjectRoot)
	}
	return d
}

func (e *Engine) resolvePhaseAdvance(in phaseAdvanceInput) phaseAdvanceDecision {
	if e != nil && e.phaseAdvanceResolved {
		d := e.phaseAdvanceOnce
		d.Blocked = mergePhaseBlock(d.Blocked, phaseAdvanceGateReason(in))
		if in.HealthErr != "" {
			d.HealthErr = in.HealthErr
		}
		e.phaseAdvanceOnce = d
		e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, d.Blocked)
		return d
	}
	d := decidePhaseAdvance(in)
	if e != nil {
		e.phaseAdvanceOnce = d
		e.phaseAdvanceResolved = true
		e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, d.Blocked)
	}
	return d
}
