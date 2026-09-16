package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/prompt"
)

// PhaseInfo holds the parsed phase metadata from a plan document.
type PhaseInfo struct {
	Number int
	Name   string
	Goal   string
	Status string
	Detail string // path to phase detail file, e.g. docs/workflow/phase1.md
}

// PlanMeta holds the top-level project plan metadata.
// P2: Use ProjectPlanMeta (alias) for clarity when distinguishing from
// planner.ExecutionPlan (the single-run execution plan).
type PlanMeta struct {
	Project     string
	Created     string
	Status      string
	ActivePhase int
	PhaseCount  int
	PhaseDocs   string // complete | incomplete | "" (legacy)
	Goals       []string
	Phases      []PhaseInfo
}

// ProjectPlanMeta is an alias for PlanMeta that clarifies the distinction
// between the multi-run project roadmap (this type) and a single-run
// planner.ExecutionPlan. P2.
type ProjectPlanMeta = PlanMeta

// BuildPlanPrompt returns a system+user prompt pair for the LLM to fill in
// the avatars_plan.md template based on a user requirement.
//
// The caller (runtime) should send this to the LLM and write the response
// via WritePlan.
func BuildPlanPrompt(requirement string, projectName string) (systemPrompt string, userPrompt string) {
	systemPrompt = prompt.PlannerStablePrefix + "\n\n" + `You are a project architect. Given a user requirement, fill in the avatars project plan template.

Output the COMPLETE filled plan in markdown. Use this EXACT structure:

# Project Plan

> **Project**: [brief one-line description]
> **Created**: [today's date]
> **Status**: draft
> **Active Phase**: 1
> **Phase Count**: [N — number of phases, 1-5]

## Goals
- [Primary goal — what must be true when this is done]

## Phases

### Phase 1: [Name]
- **Goal**: [One-line goal]
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

[more phases as needed]

## Key Decisions
| Date | Decision | Rationale |
|------|----------|-----------|
| | | |

## Risks
| Risk | Severity | Mitigation |
|------|----------|------------|
| | low / medium / high | |

## Success Criteria
- [ ] [Measurable, verifiable outcome]

RULES:
1. Keep Goals to 1-3 lines. Be specific and verifiable.
2. Decompose into 1-5 phases. Simple projects need 1 phase. Complex projects need 3-5.
   CRITICAL: If the user explicitly lists "Phase 1 … Phase N", Phase Count MUST be N.
   "only implement Phase 1 this round" means Active Phase=1, NOT Phase Count=1.
3. Each phase must have a clear, distinct goal. Phases should be sequential.
4. Every phase gets a detail file path: docs/workflow/phaseN.md
5. Success Criteria must be measurable (e.g. "all tests pass", "UI shows 3 provider options").
6. Fill in at least 1 Risk with honest assessment.
7. Use English for all fields.
8. Output ONLY the filled plan — no extra commentary.
9. When the user asks for a library/package AND unit tests / "tests must pass" / go test / pytest / npm test / cargo test, put the public API and those tests in the SAME early phase. Do NOT defer required tests. Do NOT invent a later Example/polish/README phase unless the user asked for examples, docs, or polish.
10. Shape fidelity (cross-lang): a library/package/crate/module is NOT an HTTP API, REST service, or backend unless the user asked for HTTP/REST/server/handlers. Do not invent transport layers. Success Criteria must not require 'go test -race' / TSAN unless the user asked for a race/sanitizer gate.
11. Phase titles are goals, not calendar estimates. Do not write "2-3 days" (or any duration) in a phase name.`

	userPrompt = fmt.Sprintf(`Project name: %s

User requirement:
%s

Fill in the avatars project plan template for this requirement.

%s%s`, projectName, requirement, phasePlanningGranularityUserNote(), DeliveryLocksUserNote(requirement))

	return systemPrompt, userPrompt
}

// BuildPhasePrompt returns a system+user prompt pair for the LLM to fill in
// a phase detail document (phaseN.md) based on the plan and phase info.
// userRequirement is the original NL task (C1); when non-empty it is the
// fidelity source for routes, fields, and env names — language-agnostic.
func BuildPhasePrompt(planContent string, phase PhaseInfo, projectName string, userRequirement ...string) (systemPrompt string, userPrompt string) {
	systemPrompt = prompt.PlannerStablePrefix + "\n\n" + `You are a technical lead writing a detailed implementation phase document.

Output the COMPLETE phase document in markdown. Use this EXACT structure:

# Phase N · [Phase Name]

## Overview
[2-3 sentences describing what this phase accomplishes and why it comes at this point in the project.]

## Scope & Success Criteria
- [ ] [Verifiable task-level outcome]
- [ ] [Verifiable task-level outcome]
[3-8 checklist items covering what must be true when this phase is done.]

## Inputs
- [What existing code, docs, or data this phase depends on]
- [Reference files or APIs to consult]

## Dependencies & Prerequisites
- [What must be complete before this phase can start]
- [Tools, credentials, or access needed]

## Implementation Strategy
[1-2 sentences on the approach — e.g. "bottom-up: data models first, then services, then handlers"]

## Tasks
### 1. [Task Group Name]
- [ ] [Specific, actionable task — one file or one concern]
- [ ] [Specific, actionable task]
[Group tasks by concern: models, services, handlers, frontend, tests]

### 2. [Task Group Name]
- [ ] [Specific, actionable task]
[Continue with task groups as needed]

## Risks
| Risk | Severity | Mitigation |
|------|----------|------------|
| | low / medium / high | |

## Verification
- [ ] [How to verify this phase is complete — e.g. "go build ./... passes", "frontend shows new dropdown"]

RULES:
1. Tasks must be SPECIFIC and ACTIONABLE — name exact files or endpoints.
2. Each task should be completable in one focused session.
3. Group related tasks under numbered task groups.
4. Verification criteria must be testable.
5. Use English for all fields.
6. Output ONLY the phase document — no extra commentary.
7. Include test files, config files, and all supporting files needed to make the implementation complete and verifiable.
8. Scope & Success Criteria must cover ONLY this phase's goal — never include later-phase features.
9. When prior-phase context or on-disk files are provided, EXTEND that delivery; do not redesign from scratch.
10. FIDELITY: When an ORIGINAL USER REQUIREMENT is provided, copy HTTP routes, schema/field names, and environment variable names EXACTLY from that text for this phase. Do NOT invent flatter routes, rename fields, or substitute generic env names (e.g. API_KEYS for SLOTBOOK_API_KEY).
11. ## Tasks MUST cover every Scope & Success Criteria checkbox (same key nouns/routes/fields). A skeleton-only Tasks section is invalid.
12. If this phase's goal includes shipping a library/API and the user required tests, include test Tasks and Verification (go test / pytest / npm test / cargo test) in THIS phase — do not leave required tests for a later phase.
13. Do not put calendar estimates ("2-3 days", "1 week") in the phase title. Success Criteria must not require -race / TSAN / ASAN unless the user asked.`

	req := ""
	if len(userRequirement) > 0 {
		req = strings.TrimSpace(userRequirement[0])
	}
	reqBlock := ""
	if req != "" {
		scoped := StripOtherPhaseSections(req, phase.Number)
		if strings.TrimSpace(scoped) == "" {
			scoped = req
		}
		reqBlock = fmt.Sprintf("\n\n=== ORIGINAL USER REQUIREMENT (fidelity source for Phase %d — copy routes/fields/env names EXACTLY) ===\n%s\n", phase.Number, scoped)
	}

	userPrompt = fmt.Sprintf(`Project: %s
Phase %d: %s
Phase Goal: %s
%s
Full Project Plan for context:
%s

Write the detailed phase document for Phase %d.

%s%s`,
		projectName, phase.Number, phase.Name, phase.Goal,
		reqBlock, planContent, phase.Number, phasePlanningGranularityUserNote(), DeliveryLocksUserNote(req))

	return systemPrompt, userPrompt
}

// phasePlanningGranularityUserNote is appended to Planner *user* prompts only
// so the stable system prefix stays cacheable. Cross-language: Go/Python/JS/TS/Rust/Java/C#.
func phasePlanningGranularityUserNote() string {
	return `PLANNING GRANULARITY: Write observable outcomes (suite green, public API present, peak outstanding work never exceeds capacity). Do not invent exact test function names or prescribe test-helper algorithms (shadow counters, increment/decrement recipes, mock sequences) unless the user wrote those names or steps.`
}

// WritePlan writes the filled plan content to docs/workflow/avatars_plan.md.
func WritePlan(projectRoot string, content string) error {
	return WriteWorkflowDoc(projectRoot, "plan", content)
}

// WritePhase writes a phase detail document to docs/workflow/phaseN.md.
func WritePhase(projectRoot string, phaseNum int, content string) error {
	phasePath := filepath.Join(projectRoot, "docs", "workflow", fmt.Sprintf("phase%d.md", phaseNum))
	dir := filepath.Dir(phasePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create phase directory: %w", err)
	}
	return os.WriteFile(phasePath, []byte(content), 0644)
}

// ParsePlanMeta extracts plan metadata from avatars_plan.md content.
func ParsePlanMeta(planContent string) PlanMeta {
	meta := PlanMeta{
		Status:      "draft",
		ActivePhase: 1,
		PhaseCount:  1,
	}

	lines := strings.Split(planContent, "\n")

	// Parse header fields.
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "> **Project**:") {
			meta.Project = strings.TrimSpace(strings.TrimPrefix(trimmed, "> **Project**:"))
		}
		if strings.HasPrefix(trimmed, "> **Created**:") {
			meta.Created = strings.TrimSpace(strings.TrimPrefix(trimmed, "> **Created**:"))
		}
		if strings.HasPrefix(trimmed, "> **Status**:") {
			meta.Status = strings.TrimSpace(strings.TrimPrefix(trimmed, "> **Status**:"))
		}
		if strings.HasPrefix(trimmed, "> **Active Phase**:") {
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(trimmed, "> **Active Phase**:")), "%d", &meta.ActivePhase)
		}
		if strings.HasPrefix(trimmed, "> **Phase Count**:") {
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(trimmed, "> **Phase Count**:")), "%d", &meta.PhaseCount)
		}
		if strings.HasPrefix(trimmed, "> **Phase Docs**:") {
			meta.PhaseDocs = strings.TrimSpace(strings.TrimPrefix(trimmed, "> **Phase Docs**:"))
		}
	}

	// Parse Goals.
	inGoals := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Goals") {
			inGoals = true
			continue
		}
		if inGoals && strings.HasPrefix(trimmed, "## ") {
			break
		}
		if inGoals && strings.HasPrefix(trimmed, "- ") {
			meta.Goals = append(meta.Goals, strings.TrimPrefix(trimmed, "- "))
		}
	}

	// Parse Phases.
	for i := 1; i <= meta.PhaseCount; i++ {
		phaseMarker := fmt.Sprintf("### Phase %d:", i)
		for j, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, phaseMarker) {
				phase := PhaseInfo{
					Number: i,
					Name:   strings.TrimSpace(strings.TrimPrefix(trimmed, phaseMarker)),
					Status: "pending",
					Detail: fmt.Sprintf("docs/workflow/phase%d.md", i),
				}
				// Look ahead for Goal and Status.
				for k := j + 1; k < len(lines) && k < j+12; k++ {
					nextLine := strings.TrimSpace(lines[k])
					if strings.HasPrefix(nextLine, "- **Goal**:") {
						phase.Goal = strings.TrimSpace(strings.TrimPrefix(nextLine, "- **Goal**:"))
					}
					if strings.HasPrefix(nextLine, "- **Status**:") {
						phase.Status = strings.TrimSpace(strings.TrimPrefix(nextLine, "- **Status**:"))
					}
					if strings.HasPrefix(nextLine, "- **Detail**:") {
						phase.Detail = strings.TrimSpace(strings.TrimPrefix(nextLine, "- **Detail**:"))
					}
					if strings.HasPrefix(nextLine, "### ") {
						break
					}
				}
				meta.Phases = append(meta.Phases, phase)
				break
			}
		}
	}

	return meta
}

// PhaseDocPath returns the expected path for a phase detail document.
func PhaseDocPath(projectRoot string, phaseNum int) string {
	return filepath.Join(projectRoot, "docs", "workflow", fmt.Sprintf("phase%d.md", phaseNum))
}

// PhaseDocExists checks if a phase detail document already exists.
func PhaseDocExists(projectRoot string, phaseNum int) bool {
	_, err := os.Stat(PhaseDocPath(projectRoot, phaseNum))
	return err == nil
}

// UpdatePlanStatus updates the status field in avatars_plan.md.
func UpdatePlanStatus(projectRoot string, status string) error {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	planStr := string(content)

	// Update the status line.
	oldStatus := "> **Status**: draft"
	newStatus := fmt.Sprintf("> **Status**: %s", status)
	if strings.Contains(planStr, "> **Status**: in-progress") {
		oldStatus = "> **Status**: in-progress"
	} else if strings.Contains(planStr, "> **Status**: completed") {
		oldStatus = "> **Status**: completed"
	} else if strings.Contains(planStr, "> **Status**: draft") {
		oldStatus = "> **Status**: draft"
	}
	planStr = strings.Replace(planStr, oldStatus, newStatus, 1)

	// Update created date only if still empty (W-B9 regression fix).
	if strings.Contains(planStr, "> **Created**: \n") || strings.Contains(planStr, "> **Created**:  \n") {
		today := time.Now().Format("2006-01-02")
		planStr = strings.Replace(planStr, "> **Created**: \n", "> **Created**: "+today+"\n", 1)
		planStr = strings.Replace(planStr, "> **Created**:  \n", "> **Created**: "+today+"\n", 1)
	}

	if err := WriteFileAtomic(planPath, []byte(planStr), 0644); err != nil {
		return err
	}
	// R7-4: project-level completed must sync each ### Phase N Status.
	if status == "completed" {
		meta := ParsePlanMeta(planStr)
		for _, ph := range meta.Phases {
			if ph.Number > 0 {
				_ = SetPhaseStatus(projectRoot, ph.Number, "completed")
			}
		}
		if len(meta.Phases) == 0 && meta.PhaseCount > 0 {
			for n := 1; n <= meta.PhaseCount; n++ {
				_ = SetPhaseStatus(projectRoot, n, "completed")
			}
		}
		// P8-9: plan completed ⇒ Current Task checked.
		if todoContent, err := os.ReadFile(filepath.Join(projectRoot, DocPaths["todo"])); err == nil {
			updated := markCurrentTaskDone(string(todoContent), meta.ActivePhase)
			if updated != string(todoContent) {
				_ = WriteFileAtomic(filepath.Join(projectRoot, DocPaths["todo"]), []byte(updated), 0644)
			}
		}
	}
	return nil
}

// UpdatePlanPhaseDocsStatus sets the durable process tag for phase-doc completeness.
// value examples: "complete", "incomplete (5)", "incomplete (2,5)"
func UpdatePlanPhaseDocsStatus(projectRoot string, value string) error {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	planStr := string(content)
	newLine := "> **Phase Docs**: " + strings.TrimSpace(value)
	if strings.Contains(planStr, "> **Phase Docs**:") {
		lines := strings.Split(planStr, "\n")
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "> **Phase Docs**:") {
				lines[i] = newLine
				break
			}
		}
		planStr = strings.Join(lines, "\n")
	} else if idx := strings.Index(planStr, "> **Phase Count**:"); idx >= 0 {
		// Insert after Phase Count line.
		end := idx
		for end < len(planStr) && planStr[end] != '\n' {
			end++
		}
		if end < len(planStr) {
			planStr = planStr[:end+1] + newLine + "\n" + planStr[end+1:]
		} else {
			planStr = planStr + "\n" + newLine + "\n"
		}
	} else {
		planStr = newLine + "\n" + planStr
	}
	return WriteFileAtomic(planPath, []byte(planStr), 0644)
}

// PhaseDocsComplete reports whether every declared phaseN.md is present and filled.
// X2: Active Phase must not remain a deferred stub (direction-only skeleton).
func PhaseDocsComplete(projectRoot string) bool {
	missing, err := MissingPhaseNumbers(projectRoot)
	if err != nil {
		// No plan / unreadable → don't block (legacy projects).
		return true
	}
	if len(missing) > 0 {
		return false
	}
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return true
	}
	meta := ParsePlanMeta(planContent)
	if meta.ActivePhase >= 1 && PhaseDocNeedsFullDetail(projectRoot, meta.ActivePhase) {
		return false
	}
	return true
}

// HasActiveProjectPlan reports whether docs/workflow/avatars_plan.md exists
// with at least one phase (used to bump Direct-trivial assessments).
func HasActiveProjectPlan(projectRoot string) bool {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return false
	}
	meta := ParsePlanMeta(planContent)
	return meta.PhaseCount >= 1 && (meta.Status == "in-progress" || meta.Status == "draft" || len(meta.Phases) > 0)
}

// SyncPhaseDocsProcessTag recomputes missing phases and persists the process tag.
func SyncPhaseDocsProcessTag(projectRoot string) (missing []int, err error) {
	missing, err = MissingPhaseNumbers(projectRoot)
	if err != nil {
		return nil, err
	}
	if len(missing) == 0 {
		_ = UpdatePlanPhaseDocsStatus(projectRoot, "complete")
		return missing, nil
	}
	parts := make([]string, 0, len(missing))
	for _, n := range missing {
		parts = append(parts, fmt.Sprintf("%d", n))
	}
	_ = UpdatePlanPhaseDocsStatus(projectRoot, "incomplete ("+strings.Join(parts, ",")+")")
	return missing, nil
}

// SetPhaseStatus updates "- **Status**: …" under ### Phase N without changing
// Active Phase (L2: criteria/progress sync must not leave Status stuck pending).
func SetPhaseStatus(projectRoot string, phaseNum int, status string) error {
	if phaseNum < 1 || status == "" {
		return nil
	}
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(content), "\n")
	inPhase := false
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isPhaseHeader(trimmed, phaseNum) {
			inPhase = true
			continue
		}
		if inPhase && strings.HasPrefix(trimmed, "### Phase ") {
			break
		}
		if inPhase && strings.Contains(trimmed, "**Status**:") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			updated := indent + "- **Status**: " + status
			if lines[i] == updated {
				return nil
			}
			lines[i] = updated
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	return WriteFileAtomic(planPath, []byte(strings.Join(lines, "\n")), 0644)
}

func isPhaseHeader(trimmed string, phaseNum int) bool {
	prefix := fmt.Sprintf("### Phase %d", phaseNum)
	if trimmed == prefix {
		return true
	}
	return strings.HasPrefix(trimmed, prefix+":") || strings.HasPrefix(trimmed, prefix+" ")
}

// SyncCompletedPlanPhases sets every ### Phase N Status to completed when the
// plan header is already completed (F92: header vs phase-line drift).
func SyncCompletedPlanPhases(projectRoot string) {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return
	}
	meta := ParsePlanMeta(string(content))
	if !strings.EqualFold(strings.TrimSpace(meta.Status), "completed") {
		return
	}
	if len(meta.Phases) == 0 {
		for n := 1; n <= meta.PhaseCount; n++ {
			_ = SetPhaseStatus(projectRoot, n, "completed")
		}
		return
	}
	for _, ph := range meta.Phases {
		if ph.Number > 0 {
			_ = SetPhaseStatus(projectRoot, ph.Number, "completed")
		}
	}
}

// UpdateActivePhase updates the "Active Phase" field in avatars_plan.md to
// the given phase number and marks the previous phase as completed.
// v3-P0-2: CriticDecidePhaseAdvance calls this to actually advance phases.
func UpdateActivePhase(projectRoot string, newPhase int) error {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan for active phase update: %w", err)
	}
	planStr := string(content)

	// Parse current active phase to mark the previous one completed.
	meta := ParsePlanMeta(planStr)
	oldPhase := meta.ActivePhase

	// Update "Active Phase: N" → "Active Phase: newPhase".
	oldActive := fmt.Sprintf("> **Active Phase**: %d", oldPhase)
	newActive := fmt.Sprintf("> **Active Phase**: %d", newPhase)
	planStr = strings.Replace(planStr, oldActive, newActive, 1)

	// Mark the previous phase status as completed in the Phases section.
	// S2.5: pending / active / in-progress → completed; next phase → in-progress.
	if oldPhase != newPhase {
		oldPhaseHeader := fmt.Sprintf("### Phase %d:", oldPhase)
		newPhaseHeader := fmt.Sprintf("### Phase %d:", newPhase)
		lines := strings.Split(planStr, "\n")
		inOldPhase := false
		inNewPhase := false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, oldPhaseHeader) {
				inOldPhase = true
				inNewPhase = false
				continue
			}
			if strings.HasPrefix(trimmed, newPhaseHeader) {
				inNewPhase = true
				inOldPhase = false
				continue
			}
			if (inOldPhase || inNewPhase) && strings.HasPrefix(trimmed, "### Phase ") {
				inOldPhase = false
				inNewPhase = false
			}
			if inOldPhase && strings.Contains(trimmed, "- **Status**:") {
				for _, from := range []string{"pending", "active", "in-progress", "in_progress"} {
					needle := "- **Status**: " + from
					if strings.Contains(trimmed, needle) {
						lines[i] = strings.Replace(trimmed, needle, "- **Status**: completed", 1)
						break
					}
				}
			}
			if inNewPhase && strings.Contains(trimmed, "- **Status**:") {
				for _, from := range []string{"pending", "active"} {
					needle := "- **Status**: " + from
					if strings.Contains(trimmed, needle) {
						lines[i] = strings.Replace(trimmed, needle, "- **Status**: in-progress", 1)
						break
					}
				}
			}
		}
		planStr = strings.Join(lines, "\n")
	}

	return WriteFileAtomic(planPath, []byte(planStr), 0644)
}
