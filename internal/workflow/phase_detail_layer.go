package workflow

import (
	"fmt"
	"os"
	"strings"
)

// Layered phase docs (R1):
//
//	Direction layer  — avatars_plan.md (Phase Count, Goals, one-line phase Goals)
//	Method layer     — phaseN.md full Tasks/Verification only for Active Phase
//	Future phases    — deferred stubs (direction only); expanded when they become Active
//
// ConfirmPlan must NOT LLM-write Phase 2..N in full. Advance / run-start expands
// the new Active Phase from stub → full detail.

const (
	phaseDetailDeferredMarker = "<!-- avatars:phase-detail=deferred -->"
	phaseDetailFullMarker     = "<!-- avatars:phase-detail=full -->"
)

// IsPhaseDocDeferredStub reports whether phase markdown is a direction-only stub
// awaiting expansion when that phase becomes Active.
func IsPhaseDocDeferredStub(content string) bool {
	return strings.Contains(content, phaseDetailDeferredMarker) ||
		strings.Contains(content, "Deferred until Active Phase reaches this number")
}

// PhaseDocNeedsFullDetail reports whether phaseN.md must be LLM-expanded before
// Builder executes that phase (missing, empty template, deferred stub, or
// thin-rejected draft from a failed Confirm quality gate).
func PhaseDocNeedsFullDetail(projectRoot string, phaseNum int) bool {
	path := PhaseDocPath(projectRoot, phaseNum)
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	text := string(data)
	if IsPhaseDocEmpty(text) {
		return true
	}
	if IsPhaseDocThinRejected(text) {
		return true
	}
	return IsPhaseDocDeferredStub(text)
}

// IsPhaseDocThinRejected reports a best-effort draft that failed Tasks⊇Scope
// and must be re-expanded before Confirm activates the phase (F9′).
func IsPhaseDocThinRejected(content string) bool {
	return strings.Contains(content, "avatars:phase-detail=thin-rejected")
}

// annotatePhaseThinRejected stamps a rejected draft without treating it as full.
func annotatePhaseThinRejected(content string) string {
	text := strings.TrimSpace(content)
	text = stripPhaseDetailMarkers(text)
	return "<!-- avatars:phase-detail=thin-rejected -->\n" + text + "\n"
}

func stripPhaseDetailMarkers(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.Contains(trim, "avatars:phase-detail=") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ConstructPhasesLayered implements Confirm-time phase materialization:
//   - ensure stubs for every declared phase (direction layer on disk)
//   - LLM-expand ONLY the Active Phase to full method detail
//   - never LLM-write future phases on confirm
//
// generated = LLM-expanded full docs; reused = already-full docs kept;
// stubbed = deferred stubs created or retained.
func ConstructPhasesLayered(projectRoot string, generate GenerateFunc) (generated, reused, stubbed []string, err error) {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read plan: %w", err)
	}
	meta := ParsePlanMeta(planContent)
	if len(meta.Phases) == 0 {
		return nil, nil, nil, fmt.Errorf("no phases found in plan")
	}
	count := meta.PhaseCount
	if count < len(meta.Phases) {
		count = len(meta.Phases)
	}
	if count < 1 {
		count = 1
	}
	if _, skErr := EnsurePhaseDocSkeletons(projectRoot, count); skErr != nil {
		return nil, nil, nil, skErr
	}

	active := meta.ActivePhase
	if active < 1 {
		active = 1
	}

	for i := range meta.Phases {
		phaseNum := i + 1
		name := fmt.Sprintf("phase%d.md", phaseNum)
		path := PhaseDocPath(projectRoot, phaseNum)
		data, _ := os.ReadFile(path)
		text := string(data)

		if phaseNum == active {
			if PhaseDocNeedsFullDetail(projectRoot, phaseNum) {
				if _, cErr := ConstructPhase(projectRoot, phaseNum, generate); cErr != nil {
					return generated, reused, stubbed, fmt.Errorf("phase %d: %w", phaseNum, cErr)
				}
				_ = markPhaseDetailFull(projectRoot, phaseNum)
				generated = append(generated, name)
			} else {
				reused = append(reused, name)
			}
			continue
		}

		// Past or future: keep full detail if already expanded; otherwise stub.
		if IsPhaseDocEmpty(text) {
			title, goal := phaseStubTitleGoal(meta, phaseNum)
			stub := buildPhaseDocSkeleton(phaseNum, title, goal, phaseNum > active)
			if wErr := WriteFileAtomic(path, []byte(stub), 0644); wErr != nil {
				return generated, reused, stubbed, wErr
			}
			stubbed = append(stubbed, name)
			continue
		}
		if IsPhaseDocDeferredStub(text) {
			stubbed = append(stubbed, name)
			continue
		}
		reused = append(reused, name)
	}

	if nodes := BuildPhaseNodes(projectRoot, ""); len(nodes) > 0 {
		if err := ValidatePhaseDAG(nodes); err != nil {
			return generated, reused, stubbed, fmt.Errorf("phase DAG validation: %w", err)
		}
	}
	return generated, reused, stubbed, nil
}

// ExpandActivePhaseDetail LLM-expands the plan's Active Phase when it is still
// a deferred stub (or empty). Safe to call at run start and after phase advance.
// Returns expanded=true when ConstructPhase ran.
func ExpandActivePhaseDetail(projectRoot string, generate GenerateFunc) (expanded bool, phaseNum int, err error) {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return false, 0, err
	}
	meta := ParsePlanMeta(planContent)
	phaseNum = meta.ActivePhase
	if phaseNum < 1 {
		phaseNum = 1
	}
	if !PhaseDocNeedsFullDetail(projectRoot, phaseNum) {
		return false, phaseNum, nil
	}
	if generate == nil {
		return false, phaseNum, fmt.Errorf("LLM generate required to expand phase %d", phaseNum)
	}
	if _, cErr := ConstructPhase(projectRoot, phaseNum, generate); cErr != nil {
		return false, phaseNum, cErr
	}
	_ = markPhaseDetailFull(projectRoot, phaseNum)
	_ = SyncTodoForPhase(projectRoot, phaseNum)
	return true, phaseNum, nil
}

func markPhaseDetailFull(projectRoot string, phaseNum int) error {
	path := PhaseDocPath(projectRoot, phaseNum)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	text = strings.ReplaceAll(text, phaseDetailDeferredMarker, "")
	text = strings.ReplaceAll(text, "<!-- avatars:phase-detail=thin-rejected -->", "")
	text = strings.ReplaceAll(text, "<!-- avatars:phase-detail=thin-rejected -->\n", "")
	if !strings.Contains(text, phaseDetailFullMarker) {
		// Place marker after the first heading line.
		if idx := strings.Index(text, "\n"); idx >= 0 {
			text = text[:idx+1] + phaseDetailFullMarker + "\n" + text[idx+1:]
		} else {
			text = phaseDetailFullMarker + "\n" + text
		}
	}
	// Drop deferred prose if LLM/stub left it.
	text = strings.ReplaceAll(text, "Deferred until Active Phase reaches this number. Do not implement yet.\n\n", "")
	return WriteFileAtomic(path, []byte(text), 0644)
}
