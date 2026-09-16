package runtime

import (
	"fmt"
	"os"
	"strings"

	"avatars/internal/workflow"
)

// alignActivePhaseFromInput resolves the implement phase from user input and
// updates plan/todo Active Phase BEFORE ExpandActivePhaseDetail runs.
// R4-8: Expand must see the phase we are about to implement (not stale Active=1).
// Returns previous Active and the phase we aligned to (for phase_advanced emit).
func (e *Engine) alignActivePhaseFromInput(input string) (prevActive, implement int) {
	cwd, err := os.Getwd()
	if err != nil {
		return 0, 0
	}
	planContent, planErr := workflow.ReadWorkflowDoc(cwd, "plan")
	if planErr != nil {
		return 0, 0
	}
	meta := workflow.ParsePlanMeta(planContent)
	prevActive = meta.ActivePhase
	implement = workflow.ResolveImplementPhase(input, meta.ActivePhase)
	lock := workflow.ResolveLockedPhase(input, meta.ActivePhase)
	if e != nil && e.lockedPhase > 0 {
		lock = e.lockedPhase
		implement = e.lockedPhase
	} else if e != nil {
		e.lockedPhase = lock
	}
	if implement < 1 {
		implement = 1
	}
	// P8-4 / P9-3: do not speculative-jump Active Phase ahead of incomplete checklist.
	// "进入 Phase 2" in a resume prompt must wait until Phase 1 checklist is done
	// (Critic soft-advance / TryAdvancePhase remains the primary path).
	// Also block when checklist total==0 (no tracked work yet) — otherwise greenfield
	// runs advance Active Phase before any Phase 1 deliverable lands.
	if implement > prevActive && prevActive >= 1 {
		completed, total, _ := workflow.CountTodoProgressDetailed(cwd)
		if total == 0 || completed < total {
			implement = prevActive
		}
	}
	_ = workflow.UpdateActivePhase(cwd, implement)
	_ = workflow.SetTodoActivePhasePointer(cwd, implement)
	_ = workflow.AlignCurrentTaskToPhase(cwd, implement)
	return prevActive, implement
}

// injectPhaseContextIntoPlan enriches Builder input with the Active (or locked)
// phase focus. Hard PHASE LOCK only when ResolveLockedPhase > 0 ("只做 Phase N").
// Full-course runs keep lockedPhase=0 so Critic may advance in the same run.
//
// Batch4/K: when Engine.lockedPhase is set (>0), reuse it for the whole run so
// SyncTodoFromPlan / plan header churn cannot flip the focus mid-run.
func (e *Engine) injectPhaseContextIntoPlan(input string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return input
	}

	planContent, planErr := workflow.ReadWorkflowDoc(cwd, "plan")
	if planErr != nil {
		return input
	}

	meta := workflow.ParsePlanMeta(planContent)
	implement := workflow.ResolveImplementPhase(input, meta.ActivePhase)
	lock := workflow.ResolveLockedPhase(input, meta.ActivePhase)
	if e != nil && e.lockedPhase > 0 {
		lock = e.lockedPhase
		implement = e.lockedPhase
	} else if e != nil {
		// Persist lock only when explicit; never default-lock to Active.
		e.lockedPhase = lock
	}
	if implement < 1 {
		implement = 1
	}
	if meta.ActivePhase < 1 && implement < 1 && lock < 1 {
		return input
	}

	// R2 / P8-4 / P9-3: never silent-jump Active Phase here. alignActivePhaseFromInput
	// already gated + emits phase_advanced; inject must not bypass that clamp
	// (resume "进入 Phase 2" previously wrote Active=2 with zero events).
	prevActive := meta.ActivePhase
	if implement > prevActive && prevActive >= 1 {
		completed, total, _ := workflow.CountTodoProgressDetailed(cwd)
		if total == 0 || completed < total {
			implement = prevActive
		}
	}

	// Align plan/todo to the phase we are implementing (not lock=0).
	_ = workflow.UpdateActivePhase(cwd, implement)
	_ = workflow.SetTodoActivePhasePointer(cwd, implement)
	_ = workflow.AlignCurrentTaskToPhase(cwd, implement)

	phasePrompt := workflow.InjectPhasePrompt(input, cwd, implement)

	var phaseBanner string
	if lock > 0 {
		phaseBanner = fmt.Sprintf(`
=== PHASE LOCK (Phase %d of %d) ===
You are implementing ONLY Phase %d. Generate ONLY the files needed for this phase.
Do NOT pre-implement future phases — they will be handled in subsequent runs.
Files outside this phase's scope will be VERIFIED and REJECTED by the health gate.
If you finish this phase early, STOP — do NOT start on the next phase.
User locked this run to Phase %d; Critic must NOT advance past it.
`, lock, meta.PhaseCount, lock, lock)
	} else {
		phaseBanner = fmt.Sprintf(`
=== ACTIVE PHASE FOCUS (Phase %d of %d) ===
You are implementing Phase %d now. Generate ONLY the files needed for THIS phase.
Do NOT pre-implement future phases in this Builder pass.
After Critic verifies this phase is complete, THIS RUN may advance to Phase %d and continue (full-course allowed).
`, implement, meta.PhaseCount, implement, implement+1)
	}
	phasePrompt = phaseBanner + "\n" + phasePrompt

	if stack := workflow.StackConstraintPrompt(input, cwd); stack != "" {
		phasePrompt = stack + "\n" + phasePrompt
	}
	if envLock := workflow.EnvConstraintPrompt(input, cwd); envLock != "" {
		phasePrompt = envLock + "\n" + phasePrompt
	}

	planSnapshot := extractPlanSnapshot(planContent, implement)
	if planSnapshot != "" {
		phasePrompt += "\n\n=== OVERALL PROJECT PLAN ===\n" + planSnapshot
	}

	return phasePrompt
}

// extractPlanSnapshot returns a condensed version of the plan showing
// all phases and which is currently locked for this run.
func extractPlanSnapshot(content string, activePhase int) string {
	var sb strings.Builder
	lines := strings.Split(content, "\n")
	inPhases := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## Goals") {
			sb.WriteString("## Goals\n")
			inPhases = false
			continue
		}
		if strings.HasPrefix(trimmed, "## Phases") {
			sb.WriteString("\n## Phases\n")
			inPhases = true
			continue
		}
		if inPhases && strings.HasPrefix(trimmed, "## ") {
			break
		}

		if strings.HasPrefix(trimmed, "### Phase ") {
			phaseNum := 0
			rest := strings.TrimPrefix(trimmed, "### Phase ")
			fmt.Sscanf(rest, "%d", &phaseNum)
			marker := "  "
			if phaseNum == activePhase {
				marker = "→ "
			}
			sb.WriteString(marker + trimmed + "\n")
			continue
		}

		if inPhases && strings.HasPrefix(trimmed, "- **Goal**:") {
			sb.WriteString("   " + trimmed + "\n")
			continue
		}
		if inPhases && strings.HasPrefix(trimmed, "- **Status**:") {
			sb.WriteString("   " + trimmed + "\n")
			continue
		}
	}

	return sb.String()
}
