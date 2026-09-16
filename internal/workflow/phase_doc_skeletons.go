package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsurePhaseDocSkeletons creates docs/workflow/phaseN.md for 1..phaseCount when
// missing or still an empty template (except it never overwrites a filled phase1+).
// Future-phase stubs are intentional deferred detail so PhaseDocsComplete can pass
// after Phase Count is enforced (Mid Python NL PY2).
func EnsurePhaseDocSkeletons(projectRoot string, phaseCount int) (created int, err error) {
	if phaseCount < 1 {
		phaseCount = 1
	}
	planContent, _ := ReadWorkflowDoc(projectRoot, "plan")
	meta := ParsePlanMeta(planContent)
	if err := os.MkdirAll(filepath.Join(projectRoot, "docs", "workflow"), 0755); err != nil {
		return 0, err
	}
	for n := 1; n <= phaseCount; n++ {
		path := PhaseDocPath(projectRoot, n)
		data, readErr := os.ReadFile(path)
		if readErr == nil && !IsPhaseDocEmpty(string(data)) {
			continue
		}
		// Phase 1 empty template is normally filled by Bootstrap/LLM; only
		// replace missing phase1. Later phases always get a non-empty stub.
		if n == 1 && readErr == nil {
			continue
		}
		title, goal := phaseStubTitleGoal(meta, n)
		content := buildPhaseDocSkeleton(n, title, goal, n > meta.ActivePhase && meta.ActivePhase >= 1)
		if writeErr := WriteFileAtomic(path, []byte(content), 0644); writeErr != nil {
			return created, writeErr
		}
		created++
	}
	return created, nil
}

// EnsureWorkflowPhaseDocsFromPlan materializes phaseN.md stubs for the plan's
// Phase Count and re-syncs todo + process tag (PY1+PY2).
func EnsureWorkflowPhaseDocsFromPlan(projectRoot string) (created int, err error) {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return 0, err
	}
	meta := ParsePlanMeta(planContent)
	count := meta.PhaseCount
	if count < 1 {
		count = 1
	}
	created, err = EnsurePhaseDocSkeletons(projectRoot, count)
	if err != nil {
		return created, err
	}
	_ = SyncTodoFromPlan(projectRoot)
	_, _ = SyncPhaseDocsProcessTag(projectRoot)
	return created, nil
}

func phaseStubTitleGoal(meta PlanMeta, n int) (title, goal string) {
	title = fmt.Sprintf("Phase %d", n)
	goal = "(to be refined when this phase starts)"
	if n >= 1 && n <= len(meta.Phases) {
		p := meta.Phases[n-1]
		if strings.TrimSpace(p.Name) != "" {
			title = strings.TrimSpace(p.Name)
		}
		if strings.TrimSpace(p.Goal) != "" {
			goal = strings.TrimSpace(p.Goal)
		}
	}
	return title, goal
}

func buildPhaseDocSkeleton(n int, title, goal string, deferred bool) string {
	statusNote := "Ready for detail expansion when this phase becomes Active."
	marker := phaseDetailDeferredMarker
	if deferred {
		statusNote = "Deferred until Active Phase reaches this number. Do not implement yet."
	}
	return fmt.Sprintf(`# Phase %d · %s
%s

## Overview
%s

%s

## Scope & Success Criteria
- [ ] Deliver Phase %d goals from docs/workflow/avatars_plan.md
- [ ] Keep changes scoped to this phase only

## Inputs
- docs/workflow/avatars_plan.md
- Prior phase outputs when Active Phase > 1

## Dependencies & Prerequisites
- **depends_on**: %s

## Implementation Strategy
Direction only (layered plan). Expand into concrete tasks when this phase becomes Active; Builder must follow the refined checklist after expansion.

## Tasks

### 1. Phase %d scope
- [ ] Expand phase detail and implement Phase %d: %s

## Risks
| Risk | Severity | Mitigation |
|------|----------|------------|
| Scope creep into later phases | medium | PHASE LOCK; only Active Phase work |

## Verification
- [ ] Phase %d success criteria in avatars_plan.md are met
`, n, title, marker, goal, statusNote, n, phaseDependsOn(n), n, n, title, n)
}

func phaseDependsOn(n int) string {
	if n <= 1 {
		return "[]"
	}
	return fmt.Sprintf(`["phase-%d"]`, n-1)
}

// isTemplatePlaceholderLabel reports checklist / group labels that must never
// drive Critic gaps or todo progress (Mid Python NL PY1).
func isTemplatePlaceholderLabel(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	// Strip leading checkbox / numbering noise.
	for _, p := range []string{"- [ ]", "- [x]", "- [X]", "- [>]", "-"} {
		if strings.HasPrefix(t, p) {
			t = strings.TrimSpace(t[len(p):])
		}
	}
	if i := strings.Index(t, " "); i > 0 {
		// "1. [Task Group Name]" → check after number.
		head := t[:i]
		if len(head) > 0 && head[len(head)-1] == '.' {
			t = strings.TrimSpace(t[i+1:])
		}
	}
	placeholders := []string{
		"[Task Group Name]",
		"[Specific, actionable",
		"[Verifiable task-level outcome]",
		"[Phase Name]",
		"[estimated duration]",
		"[Reference files",
		"[Tools, credentials",
		"[Verification step",
		"[2-3 sentences",
	}
	for _, p := range placeholders {
		if strings.Contains(t, p) {
			return true
		}
	}
	// W4: deferred phase skeleton groups must not drive Critic gaps / progress.
	lower := strings.ToLower(t)
	if strings.Contains(lower, "phase ") && strings.Contains(lower, " scope") {
		return true
	}
	if strings.HasPrefix(lower, "expand phase detail") ||
		strings.Contains(lower, "expand phase detail and implement") {
		return true
	}
	// Bare bracket tokens like "[Name]".
	if strings.HasPrefix(t, "[") && strings.Contains(t, "]") {
		inner := t
		if end := strings.Index(inner, "]"); end > 1 {
			inner = inner[1:end]
			if inner == "Task Group Name" || strings.Contains(strings.ToLower(inner), "name") && len(inner) < 40 {
				return true
			}
		}
	}
	return false
}
