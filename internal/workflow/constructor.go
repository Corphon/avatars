package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GenerateFunc is the signature for an LLM generation function.
// The runtime wraps its LLM client with this signature.
type GenerateFunc func(systemPrompt string, userPrompt string) (string, error)

// ConstructPlan fills the avatars_plan.md template using the LLM based on
// a user requirement. It returns the filled plan content and any error.
//
// The caller (runtime) provides the LLM call via `generate`.
func ConstructPlan(projectRoot string, requirement string, generate GenerateFunc) (string, error) {
	projectName := filepath.Base(projectRoot)
	sysPrompt, userPrompt := BuildPlanPrompt(requirement, projectName)
	// C1: persist original NL so phase expansion can align routes/fields/env names.
	_ = PersistUserRequirement(projectRoot, requirement)

	// W12: Inject lessons learned from process_record.md so the plan
	// includes mitigations for known pitfalls.
	if lessonsSection := readLessonsForPrompt(projectRoot); lessonsSection != "" {
		sysPrompt += "\n\n" + lessonsSection
	}

	response, err := generate(sysPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("LLM plan generation failed: %w", err)
	}

	// Clean the LLM response: strip markdown fences if present.
	planContent := cleanLLMResponse(response)

	// Validate that the response looks like a plan (has key sections).
	if !looksLikePlan(planContent) {
		return "", fmt.Errorf("LLM response does not contain required plan sections (Goals, Phases, Success Criteria)")
	}

	// Fill in any missing template fields.
	planContent = fillPlanDefaults(planContent, projectName)
	// LLM often collapses "本轮只要 Phase 1" into Phase Count=1 even when the
	// user enumerated Phase 1..N. Enforce explicit phase markers before write.
	planContent = EnforcePhaseCountInPlan(planContent, requirement)
	planContent = collapseUnsolicitedPolishPhase(planContent, requirement)

	// Write the plan to disk.
	if err := WritePlan(projectRoot, planContent); err != nil {
		return "", fmt.Errorf("write plan: %w", err)
	}

	// Update process record to reflect plan construction.
	RecordTaskCompletion(projectRoot, "Plan construction: "+requirement, "Filled avatars_plan.md from user requirement")

	return planContent, nil
}

// ConstructPhase fills a phase detail document (phaseN.md) using the LLM
// based on the project plan. It returns the filled phase content and any error.
func ConstructPhase(projectRoot string, phaseNum int, generate GenerateFunc) (string, error) {
	// Read the plan for context.
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return "", fmt.Errorf("read plan for phase context: %w", err)
	}

	// Parse the plan to get phase info.
	meta := ParsePlanMeta(planContent)
	if phaseNum < 1 || phaseNum > len(meta.Phases) {
		return "", fmt.Errorf("phase %d not found in plan (has %d phases)", phaseNum, len(meta.Phases))
	}
	phase := meta.Phases[phaseNum-1]

	projectName := filepath.Base(projectRoot)
	userReq := LoadUserRequirement(projectRoot)
	sysPrompt, userPrompt := BuildPhasePrompt(planContent, phase, projectName, userReq)
	// R4-8: when expanding Phase N>1, inject prior phase detail + disk inventory
	// so the LLM plans against real delivery (language-agnostic).
	if phaseNum > 1 {
		if prev := ReadPhaseDocBody(projectRoot, phaseNum-1, 4000); prev != "" {
			userPrompt += fmt.Sprintf("\n\n=== PRIOR PHASE %d DETAIL (already delivered — EXTEND, do not redo) ===\n%s\n", phaseNum-1, prev)
		}
		userPrompt += "\n\n=== SOURCE FILES ALREADY ON DISK ===\n" + ProjectSourceInventory(projectRoot, 50) + "\n"
		sysPrompt += "\n\nCRITICAL: This phase builds on existing code listed above. Success Criteria and Tasks MUST only cover THIS phase's goal — never copy features from later phases into this document."
	}

	// W12: Inject lessons learned from process_record.md.
	if lessonsSection := readLessonsForPrompt(projectRoot); lessonsSection != "" {
		sysPrompt += "\n\n" + lessonsSection
	}

	response, err := generate(sysPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("LLM phase generation failed: %w", err)
	}

	phaseContent := cleanLLMResponse(response)
	// L7/V7: drop glued/corrupt checkbox lines and duplicate ### task groups.
	phaseContent = sanitizePhaseDocMarkdown(phaseContent)
	phaseContent = resetConstructCheckboxes(phaseContent)

	// Validate the phase content.
	if !looksLikePhase(phaseContent) {
		return "", fmt.Errorf("LLM response does not contain required phase sections (Overview, Tasks, Verification)")
	}
	// V7: blank Scope & Success Criteria usually means truncated LLM output — retry once.
	if phaseDocHasEmptySuccessCriteria(phaseContent) && generate != nil {
		retry, retryErr := generate(sysPrompt+"\n\nCRITICAL: Scope & Success Criteria MUST contain measurable checkbox bullets. Do not leave that section empty. Do not glue ## headings onto task lines.", userPrompt)
		if retryErr == nil {
			retryContent := sanitizePhaseDocMarkdown(cleanLLMResponse(retry))
			if looksLikePhase(retryContent) && !phaseDocHasEmptySuccessCriteria(retryContent) {
				phaseContent = retryContent
			}
		}
	}
	// G9: freeform prose without Tasks checkboxes is not a valid full detail doc.
	if !phaseDocHasTaskCheckboxes(phaseContent) && generate != nil {
		retry, retryErr := generate(sysPrompt+"\n\nCRITICAL: ## Tasks MUST use markdown checkboxes (- [ ] …). Include <!-- avatars:phase-detail=full -->. Do not write freeform essays without checkboxes.", userPrompt)
		if retryErr == nil {
			retryContent := sanitizePhaseDocMarkdown(cleanLLMResponse(retry))
			if looksLikePhase(retryContent) && phaseDocHasTaskCheckboxes(retryContent) {
				phaseContent = retryContent
			}
		}
	}
	if !phaseDocHasTaskCheckboxes(phaseContent) {
		return "", fmt.Errorf("phase %d detail missing Tasks checkboxes (freeform rejected)", phaseNum)
	}
	// B10/C2/F9: Tasks must cover Scope. Soft retries include uncovered Scope
	// bullets so any provider (flash/pro/other) can fill gaps — gate stays ≥80%.
	retryThinWithGaps := func(content string) string {
		if generate == nil || phaseTasksCoverScope(content) {
			return content
		}
		gaps := phaseTasksUncoveredScope(content)
		hint := formatUncoveredScopeForPrompt(gaps)
		retry, retryErr := generate(sysPrompt+"\n\nCRITICAL:\n"+hint, userPrompt)
		if retryErr != nil {
			return content
		}
		retryContent := sanitizePhaseDocMarkdown(cleanLLMResponse(retry))
		if !looksLikePhase(retryContent) || !phaseDocHasTaskCheckboxes(retryContent) {
			return content
		}
		if phaseTasksCoverScope(retryContent) {
			return retryContent
		}
		// Keep progress toward coverage; never regress uncovered count.
		if len(phaseTasksUncoveredScope(retryContent)) < len(gaps) {
			return retryContent
		}
		return content
	}
	phaseContent = retryThinWithGaps(phaseContent)
	// Second gap-targeted pass if still thin (same quality bar for all providers).
	phaseContent = retryThinWithGaps(phaseContent)
	phaseContent = resetConstructCheckboxes(phaseContent)
	if !phaseTasksCoverScope(phaseContent) {
		gaps := phaseTasksUncoveredScope(phaseContent)
		// F9′: persist best draft so re-confirm/debug is not staring at a blank
		// template. Marker keeps PhaseDocNeedsFullDetail=true (Confirm still fails).
		if looksLikePhase(phaseContent) && phaseDocHasTaskCheckboxes(phaseContent) {
			_ = WritePhase(projectRoot, phaseNum, annotatePhaseThinRejected(phaseContent))
		}
		return "", fmt.Errorf("phase %d Tasks do not cover Scope & Success Criteria (thin Tasks rejected)%s",
			phaseNum, formatUncoveredScopeForError(gaps))
	}
	// C1: phase Scope/Verification must retain distinctive routes from the user NL.
	if userReq != "" && !phaseDocCoversUserRoutes(phaseContent, userReq, phaseNum) && generate != nil {
		retry, retryErr := generate(sysPrompt+"\n\nCRITICAL: Copy HTTP routes from the ORIGINAL USER REQUIREMENT EXACTLY into Scope and Tasks (including nested paths like /rooms/{id}/slots). Do not flatten or invent alternate routes.", userPrompt)
		if retryErr == nil {
			retryContent := sanitizePhaseDocMarkdown(cleanLLMResponse(retry))
			if looksLikePhase(retryContent) && phaseDocHasTaskCheckboxes(retryContent) &&
				phaseTasksCoverScope(retryContent) && phaseDocCoversUserRoutes(retryContent, userReq, phaseNum) {
				phaseContent = retryContent
			}
		}
	}
	if userReq != "" && !phaseDocCoversUserRoutes(phaseContent, userReq, phaseNum) {
		return "", fmt.Errorf("phase %d drifts from user requirement routes/fields (fidelity rejected)", phaseNum)
	}

	phaseContent = resetConstructCheckboxes(phaseContent)
	// Write the phase to disk.
	if err := WritePhase(projectRoot, phaseNum, phaseContent); err != nil {
		return "", fmt.Errorf("write phase %d: %w", phaseNum, err)
	}
	_ = markPhaseDetailFull(projectRoot, phaseNum)

	RecordTaskCompletion(projectRoot,
		fmt.Sprintf("Phase %d construction: %s", phaseNum, phase.Name),
		fmt.Sprintf("Generated docs/workflow/phase%d.md", phaseNum))

	return phaseContent, nil
}

// ConstructMissingPhases is the ConfirmPlan entry point for phase docs.
// Layered plan (R1): LLM-expands Active Phase only; future phases stay deferred stubs.
func ConstructMissingPhases(projectRoot string, generate GenerateFunc) (generated []string, reused []string, err error) {
	gen, reusedFull, stubbed, err := ConstructPhasesLayered(projectRoot, generate)
	if err != nil {
		return gen, reusedFull, err
	}
	// Callers historically treat "reused" as "not LLM this call" — stubs count as reused.
	reused = append(reusedFull, stubbed...)
	return gen, reused, nil
}

// ConstructAllPhases force-expands every phase with the LLM (explicit "generate all
// phases" tooling). Prefer ConstructPhasesLayered / ConfirmPlan for normal runs.
func ConstructAllPhases(projectRoot string, generate GenerateFunc) ([]string, error) {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}

	meta := ParsePlanMeta(planContent)
	if len(meta.Phases) == 0 {
		return nil, fmt.Errorf("no phases found in plan")
	}

	var results []string
	for i := range meta.Phases {
		phaseNum := i + 1
		content, err := ConstructPhase(projectRoot, phaseNum, generate)
		if err != nil {
			return results, fmt.Errorf("phase %d: %w", phaseNum, err)
		}
		_ = markPhaseDetailFull(projectRoot, phaseNum)
		results = append(results, content)
	}

	if nodes := BuildPhaseNodes(projectRoot, ""); len(nodes) > 0 {
		if err := ValidatePhaseDAG(nodes); err != nil {
			return results, fmt.Errorf("phase DAG validation: %w", err)
		}
	}

	return results, nil
}

// PopulateTodoFromPhase loads the tasks from phaseN.md into avatars_todo.md.
// This is the bridge between phase planning and task execution.
func PopulateTodoFromPhase(projectRoot string, phaseNum int) error {
	phasePath := PhaseDocPath(projectRoot, phaseNum)
	if _, err := os.Stat(phasePath); err != nil {
		return fmt.Errorf("phase %d detail file not found at %s: run ConstructPhase first", phaseNum, phasePath)
	}

	return SyncTodoForPhase(projectRoot, phaseNum)
}

// SyncTodoForPhase updates avatars_todo.md for ALL phases (keeping overall
// progress visible) and points the active header at phaseNum.
func SyncTodoForPhase(projectRoot string, phaseNum int) error {
	pointerErr := SetTodoActivePhasePointer(projectRoot, phaseNum)
	// Prefer full multi-phase sync from plan + phase docs.
	if err := SyncTodoFromPlan(projectRoot); err == nil {
		return SetTodoActivePhasePointer(projectRoot, phaseNum)
	}
	// Fallback: single-phase rebuild when plan is missing.
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	todoContent, err := os.ReadFile(todoPath)
	if err != nil {
		if pointerErr != nil {
			return fmt.Errorf("read todo: %w (phase pointer: %v)", err, pointerErr)
		}
		return fmt.Errorf("read todo: %w", err)
	}
	if err := rebuildTodoChecklistFromPhase(projectRoot, todoPath, string(todoContent), fmt.Sprintf("%d", phaseNum)); err != nil {
		return err
	}
	return pointerErr
}

// IsPlanConstruction returns true if the user's task input indicates they want
// to construct/fill/create a project plan document (avatars_plan.md).
func IsPlanConstruction(taskInput string) bool {
	lower := strings.ToLower(taskInput)

	// Must mention the plan document or "plan" concept.
	mentionsPlan := strings.Contains(lower, "avatars_plan.md") ||
		strings.Contains(lower, "project plan") ||
		strings.Contains(lower, "开发计划") ||
		strings.Contains(lower, "项目计划") ||
		strings.Contains(lower, "construct a plan") ||
		strings.Contains(lower, "build a plan") ||
		strings.Contains(lower, "create a plan") ||
		strings.Contains(lower, "plan for ")

	if !mentionsPlan {
		return false
	}

	// Must include a construction verb.
	constructVerbs := []string{
		"construct", "build", "create", "fill", "generate", "write",
		"填写", "构造", "构建", "创建", "生成", "写出",
	}
	for _, verb := range constructVerbs {
		if strings.Contains(lower, verb) {
			return true
		}
	}

	return false
}

// IsPhaseConstruction returns true if the task is about generating a phase detail file.
func IsPhaseConstruction(taskInput string) bool {
	lower := strings.ToLower(taskInput)
	mentionsPhase := strings.Contains(lower, "phase") &&
		(strings.Contains(lower, ".md") || strings.Contains(lower, "generate") || strings.Contains(lower, "生成"))
	if !mentionsPhase {
		return false
	}
	constructVerbs := []string{"construct", "build", "create", "generate", "生成", "构造", "构建", "创建"}
	for _, verb := range constructVerbs {
		if strings.Contains(lower, verb) {
			return true
		}
	}
	return false
}

// PlanStatus returns a human-readable summary of the project workflow state.
func PlanStatus(projectRoot string) string {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return "No project plan found. Use 'ConstructPlan' to create one from a requirement."
	}

	meta := ParsePlanMeta(planContent)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Project: %s\n", meta.Project))
	sb.WriteString(fmt.Sprintf("Status: %s\n", meta.Status))
	sb.WriteString(fmt.Sprintf("Active Phase: %d of %d\n", meta.ActivePhase, meta.PhaseCount))
	sb.WriteString("\nGoals:\n")
	for _, goal := range meta.Goals {
		sb.WriteString(fmt.Sprintf("  - %s\n", goal))
	}
	sb.WriteString("\nPhases:\n")
	for _, phase := range meta.Phases {
		marker := "  "
		if phase.Number == meta.ActivePhase {
			marker = "→ "
		}
		sb.WriteString(fmt.Sprintf("%sPhase %d: %s [%s] — %s\n",
			marker, phase.Number, phase.Name, phase.Status, phase.Goal))
	}

	return sb.String()
}

// cleanLLMResponse strips markdown code fences and trims whitespace
// from an LLM response. LLMs often wrap output in ```markdown``` fences.
func cleanLLMResponse(response string) string {
	content := strings.TrimSpace(response)

	// Remove leading ```markdown or ``` fences.
	if strings.HasPrefix(content, "```") {
		if idx := strings.Index(content, "\n"); idx > 0 {
			content = content[idx+1:]
		}
	}

	// Remove trailing ``` fences.
	if strings.HasSuffix(content, "```") {
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	return content
}

// looksLikePlan checks if the content has the required plan sections.
func looksLikePlan(content string) bool {
	hasGoals := strings.Contains(content, "## Goals")
	hasPhases := strings.Contains(content, "## Phases")
	hasSuccess := strings.Contains(content, "## Success Criteria")
	return hasGoals && hasPhases && hasSuccess
}

// looksLikePhase checks if the content has the required phase sections.
func looksLikePhase(content string) bool {
	hasOverview := strings.Contains(content, "## Overview")
	hasTasks := strings.Contains(content, "## Tasks")
	hasVerification := strings.Contains(content, "## Verification")
	return hasOverview && hasTasks && hasVerification
}

// fillPlanDefaults fills in placeholder values that the LLM might have missed.
// P1-6b: Covers all IsPlanEmpty placeholders to prevent inconsistent detection.
func fillPlanDefaults(content string, projectName string) string {
	today := time.Now().Format("2006-01-02")

	// Fill all template placeholders that IsPlanEmpty checks.
	content = strings.ReplaceAll(content, "[brief one-line description]", projectName)
	content = strings.ReplaceAll(content, "[Primary goal — what must this project deliver?]", "Complete "+projectName)
	content = strings.ReplaceAll(content, "[Primary goal", "Complete "+projectName)
	content = strings.ReplaceAll(content, "[Name]", "Phase 1: Foundation")
	content = strings.ReplaceAll(content, "[Measurable — how do we know this is done?]", "All checklist items marked [x]")
	content = strings.ReplaceAll(content, "[One-line goal]", "Deliver the core implementation")

	// NL smoke #9: always normalize Created header to today; replace any
	// YYYY-MM-DD in the Created line and Key Decisions date column.
	content = regexp.MustCompile(`(?m)^> \*\*Created\*\*:.*$`).ReplaceAllString(content, "> **Created**: "+today)
	content = regexp.MustCompile(`(?m)^(\| )(\d{4}-\d{2}-\d{2})( \|)`).ReplaceAllString(content, "${1}"+today+"${3}")

	// Fill empty Key Decisions table if completely empty.
	if strings.Contains(content, "| Date | Decision | Rationale |") {
		// Table header exists — check if there's at least one row.
		afterHeader := content[strings.Index(content, "| Date | Decision | Rationale |"):]
		lines := strings.Split(afterHeader, "\n")
		hasDataRow := false
		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "|") && !strings.Contains(trimmed, "---") &&
				!strings.Contains(trimmed, "Date") && strings.Count(trimmed, "|") >= 3 {
				parts := strings.Split(trimmed, "|")
				nonEmpty := false
				for _, p := range parts {
					if strings.TrimSpace(p) != "" {
						nonEmpty = true
						break
					}
				}
				if nonEmpty {
					hasDataRow = true
					break
				}
			}
		}
		if !hasDataRow {
			content = strings.Replace(content,
				"| Date | Decision | Rationale |\n|------|----------|------------|\n| | | |",
				"| Date | Decision | Rationale |\n|------|----------|------------|\n| "+today+" | Initial plan constructed | User requirement analysis |",
				1)
		}
	}

	return content
}

// readLessonsForPrompt returns a "Lessons Learned" section from SQLite
// (warm_lessons + evaluation_records) for injection into plan/phase prompts.
// PARTIAL-3: Now reads from SQLite instead of process_record file.
// BootstrapWorkflowFromArch fills the project workflow files using architecture.md
// data. No LLM needed. This is called after arch --analyze/--init completes, so the
// workflow docs are never left as empty templates.
// IsAttachmentSummarizePlaceholder is the default --from-file rewrite used
// when a file is not a task spec. It must not seed architecture.md or a plan.
func IsAttachmentSummarizePlaceholder(taskDesc string) bool {
	t := strings.ToLower(strings.TrimSpace(taskDesc))
	return t == "summarize this attached file" || t == "看看这个文件"
}

// BootstrapWorkflowFromTask fills workflow docs from the user's task description
// AND architecture.md Registration Points. Unlike BootstrapWorkflowFromArch which
// only captures static structure, this creates actionable implementation steps.
func BootstrapWorkflowFromTask(root string, taskDesc string) error {
	if IsAttachmentSummarizePlaceholder(taskDesc) {
		return nil
	}
	// Check if plan exists AND has been filled (no placeholders).
	// PreloadDocs writes a raw template — that doesn't count as "existing workflow."
	planPath := filepath.Join(root, "docs", "workflow", "avatars_plan.md")
	if data, err := os.ReadFile(planPath); err == nil {
		content := string(data)
		// If the plan still has unfilled placeholders, fill it.
		if !strings.Contains(content, "[Name]") && !strings.Contains(content, "[One-line goal]") {
			return nil // Already properly filled by a previous phase.
		}
	}
	// Phase check: skip only if already filled (same gate as ConfirmPlan).
	if !IsPhaseEmpty(root) {
		return nil
	}
	projectName := filepath.Base(root)
	if projectName == "." {
		if abs, err := filepath.Abs(root); err == nil {
			projectName = filepath.Base(abs)
		}
	}
	if strings.TrimSpace(taskDesc) == "" {
		taskDesc = "Build and verify the " + projectName + " project."
	}

	// Use PreloadDocs (which reads go:embed templates) to create the files
	// from embedded templates, then fill in the project-specific content.
	if _, err := PreloadDocs(root); err != nil {
		return err
	}
	// Now fill the templates with actual task content.
	fillPlanContent(root, projectName, taskDesc)
	fillPhase1Content(root, taskDesc)
	fillTodoContent(root, taskDesc)
	return nil
}

func buildPhase1FromArch(root string, taskDesc string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Phase 1: Implementation\n\n> **Plan**: avatars_plan.md | **Status**: active\n\n"))
	b.WriteString(fmt.Sprintf("## Task\n%s\n\n", taskDesc))

	// Try to read architecture Registration Points for actionable file list.
	// Check both root (arch --analyze output) and docs/ (bootstrap output).
	var archStr string
	for _, archPath := range []string{
		filepath.Join(root, "architecture.md"),
		filepath.Join(root, "docs", "architecture.md"),
	} {
		if data, err := os.ReadFile(archPath); err == nil {
			archStr = string(data)
			break
		}
	}
	if archStr != "" {
		// Extract Registration Points section.
		rpSection := extractSection(archStr, "## Registration Points", "## Cross-cutting")
		if rpSection == "" {
			rpSection = extractSection(archStr, "## Registration Points", "## Dependencies")
		}
		if rpSection != "" {
			b.WriteString("\n## Files to Modify (from architecture Registration Points)\n\n")
			currentRP := ""
			for _, line := range strings.Split(rpSection, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "### ") {
					currentRP = strings.TrimPrefix(trimmed, "### ")
					b.WriteString(fmt.Sprintf("### %s\n", currentRP))
				} else if strings.HasPrefix(trimmed, "| ") && !strings.HasPrefix(trimmed, "| File") && !strings.HasPrefix(trimmed, "|--") {
					b.WriteString(fmt.Sprintf("- [ ] %s\n", trimmed))
				}
			}
		}

		// Extract Entry Points as implementation targets.
		epSection := extractSection(archStr, "## Entry Points", "## Layers")
		if epSection == "" {
			epSection = extractSection(archStr, "## Entry Points", "## Registration")
		}
		if epSection != "" {
			b.WriteString("\n## Entry Points to Implement\n\n")
			for _, line := range strings.Split(epSection, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "| ") && !strings.HasPrefix(trimmed, "| Entry") && !strings.HasPrefix(trimmed, "|--") {
					parts := strings.Split(trimmed, "|")
					if len(parts) >= 2 {
						path := strings.TrimSpace(parts[1])
						if path != "" && !strings.HasPrefix(path, "-") {
							b.WriteString(fmt.Sprintf("- [ ] Implement %s\n", path))
						}
					}
				}
			}
		}

	}

	// List actual project source files as concrete implementation targets.
	b.WriteString("\n## Implementation Tasks\n\n")
	added := collectSourceFiles(&b, root, "")
	if !added {
		b.WriteString("- [ ] Implement core functionality\n")
	}

	b.WriteString("\n## Verification\n- [ ] Run go build ./... (or language-appropriate check)\n- [ ] Run existing tests\n- [ ] Manual review of changed files\n")
	return b.String()
}

func BootstrapWorkflowFromArch(root string) error {
	// Read the architecture document.
	archPath := filepath.Join(root, "architecture.md")
	archContent, err := os.ReadFile(archPath)
	if err != nil {
		return fmt.Errorf("read architecture.md: %w", err)
	}

	projectName := filepath.Base(root)
	now := time.Now().UTC().Format(time.RFC3339)

	// Build a filled plan from architecture data.
	plan := fmt.Sprintf(`# Project Plan

> **Project**: %s
> **Created**: %s
> **Status**: active
> **Active Phase**: 1
> **Phase Count**: 1

## Goals
- Architecture analyzed: see architecture.md for full structure, entry points, and registration points.

## Phases

### Phase 1: Architecture foundation
- **Goal**: Establish project structure and core patterns per architecture.md
- **Status**: active
- **Detail**: docs/workflow/phase1.md

## Key Decisions
| Date | Decision | Rationale |
|------|----------|-----------|
| %s | Architecture analysis | Generated via avatars arch --analyze |

## Risks
| Risk | Severity | Mitigation |
|------|----------|------------|
| Architecture drift | medium | Run arch --check periodically; auto-stale on key file edits |

## Success Criteria
- [ ] Registration Points documented and verified
- [ ] Entry Points confirmed functional
- [ ] Cross-cutting conventions followed
`, projectName, now, now[:10])

	if err := os.MkdirAll(filepath.Join(root, "docs", "workflow"), 0755); err != nil {
		return fmt.Errorf("create workflow dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "workflow", "avatars_plan.md"), []byte(plan), 0644); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}

	// Write a phase1.md with key tasks derived from the architecture.
	phase1 := fmt.Sprintf(`# Phase 1: Architecture foundation

> **Plan**: avatars_plan.md | **Status**: active

## Overview
Project architecture has been analyzed. The full architecture document is at architecture.md.

## Key Architecture Findings
%s

## Tasks
- [ ] Review and confirm Registration Points in architecture.md
- [ ] Verify Entry Points are accurate
- [ ] Ensure conventions match actual code
- [ ] Run arch --check to validate staleness

## Verification
- architecture.md status confirmed
- Entry Points match actual project structure
- Registration Points tested with a sample task
`, archContent[:min(len(archContent), 2000)])

	if err := os.WriteFile(filepath.Join(root, "docs", "workflow", "phase1.md"), []byte(phase1), 0644); err != nil {
		return err
	}

	// Also create avatars_todo.md — the active task tracker.
	todo := fmt.Sprintf(`# Active Workspace

> **Phase**: 1
> **Plan**: docs/workflow/avatars_plan.md
> **Updated**: %s

## Current Task
- [ ] Review and confirm Registration Points in architecture.md

## Phase Checklist
- [ ] Entry Points verified
- [ ] Registration Points confirmed
- [ ] Conventions reviewed

## Completed

## Blockers

## Notes
- Architecture analyzed via avatars arch --analyze
`, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(filepath.Join(root, "docs", "workflow", "avatars_todo.md"), []byte(todo), 0644)
}

func readLessonsForPrompt(projectRoot string) string {
	memCtx := LoadMemoryContext()
	if memCtx == "" {
		return ""
	}
	return "Lessons learned from past tasks:\n" + memCtx
}

// fillPlanContent reads the just-created plan template and fills in project info.
func fillPlanContent(root, projectName, taskDesc string) {
	planPath := filepath.Join(root, "docs", "workflow", "avatars_plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return
	}
	content := string(data)
	now := time.Now().UTC().Format(time.RFC3339)
	content = strings.Replace(content, "[brief one-line description]", projectName, 1)
	content = strings.Replace(content, "[YYYY-MM-DD]", now[:10], 1)
	content = strings.Replace(content, "[Primary goal — what must be true when this is done]", taskDesc, 1)
	content = strings.Replace(content, "[Name]", "Implementation", 1)
	content = strings.Replace(content, "[One-line goal]", taskDesc, 1)
	content = strings.Replace(content, "draft", "active", 1)
	content = strings.Replace(content, "pending", "active", 1)
	// Estimate phase count from task complexity — count distinct feature mentions.
	phaseCount := estimatePhaseCount(taskDesc)
	content = replacePlanPhaseCount(content, phaseCount)
	content = ensurePlanPhaseSections(content, phaseCount)
	content = strings.Replace(content, "[Measurable, verifiable outcome]", "Build passes and core functionality works", 1)
	// Clean empty tables.
	content = strings.Replace(content, "| Date | Decision | Rationale |\n|------|----------|-----------|\n| | | |", "", 1)
	content = strings.Replace(content, "| Risk | Severity | Mitigation |\n|------|----------|------------|\n| | low / medium / high | |", "", 1)
	// Remove HTML comments to keep it clean.
	content = strings.ReplaceAll(content, "<!-- Add more phases as needed -->", "")
	_ = os.WriteFile(planPath, []byte(content), 0644)
	_, _ = EnsurePhaseDocSkeletons(root, phaseCount)
}

// fillPhase1Content fills the phase1 template with task description and actual files.
func fillPhase1Content(root, taskDesc string) {
	phasePath := filepath.Join(root, "docs", "workflow", "phase1.md")
	data, err := os.ReadFile(phasePath)
	if err != nil {
		return
	}
	content := string(data)
	content = strings.Replace(content, "Phase N", "Phase 1", 1)
	content = strings.Replace(content, "[Phase Name]", "Implementation", 1)
	content = strings.Replace(content, "[estimated duration]", "—", 1)
	content = strings.Replace(content, "[2-3 sentences describing what this phase accomplishes and why it comes at this point in the project.]", taskDesc, 1)
	content = strings.ReplaceAll(content, "[Verifiable task-level outcome]", "Core functionality implemented")
	content = strings.Replace(content, "[Reference files or APIs to consult]", "architecture.md", 1)
	content = strings.Replace(content, "[Tools, credentials, or access needed]", "available toolchain", 1)
	content = strings.ReplaceAll(content, "[Task Group Name]", "Implementation")
	content = strings.Replace(content, "[Specific, actionable task — name exact files or endpoints]", "Per project source files (see below)", 1)
	content = strings.ReplaceAll(content, "[Specific, actionable task]", "")
	// Add actual source files.
	fileTasks := collectSourceFilesAsMarkdown(root, "")
	content = strings.Replace(content, "Per project source files (see below)", "Per project source files (see below)\n"+fileTasks, 1)
	// Clean up the verification section.
	content = strings.Replace(content, "[Verification step — e.g. \"go build ./... passes\", \"frontend shows new dropdown\"]", "Run the language-appropriate build/test command", 1)
	// Remove empty task placeholders.
	content = strings.Replace(content, "- [ ] \n", "", 1)
	os.WriteFile(phasePath, []byte(content), 0644)
}

func collectSourceFilesAsMarkdown(root, prefix string) string {
	var b strings.Builder
	collectSourceFiles(&b, root, prefix)
	return b.String()
}

// fillTodoContent fills the todo template with the current task.
func fillTodoContent(root, taskDesc string) {
	todoPath := filepath.Join(root, "docs", "workflow", "avatars_todo.md")
	data, err := os.ReadFile(todoPath)
	if err != nil {
		return
	}
	content := string(data)
	now := time.Now().UTC().Format(time.RFC3339)
	content = strings.Replace(content, "[YYYY-MM-DD]", now[:10], 1)
	content = strings.Replace(content, "Define phase 1 tasks in docs/workflow/phase1.md", taskDesc, 1)
	os.WriteFile(todoPath, []byte(content), 0644)
}

// collectSourceFiles recursively lists source files as implementation checkboxes.
// estimatePhaseCount estimates the number of phases from the task description.
// Prefer explicit "Phase N" / "阶段 N" / Chinese "第N段" markers over keyword
// heuristics. Keyword bumps must NOT inflate past an explicit stage count (G10).
func estimatePhaseCount(taskDesc string) int {
	if n := maxExplicitPhaseNumber(taskDesc); n >= 1 {
		return n
	}
	if n := countChineseStageMarkers(taskDesc); n >= 1 {
		return n
	}
	if n := parseExplicitStageTotal(taskDesc); n >= 1 {
		return n
	}
	// Repair / continue-last-run must not invent extra stages (F88).
	if looksLikeRepairOrContinueTask(taskDesc) {
		return 1
	}
	// No explicit structure — soft keyword heuristic, capped at 3 (not 5).
	lower := strings.ToLower(taskDesc)
	count := 1
	indicators := []string{"middleware", "auth", "deploy",
		"鉴权", "部署", "中间件", "migration", "迁移"}
	for _, ind := range indicators {
		if containsPhaseIndicator(lower, ind) {
			count++
		}
	}
	if count > 3 {
		count = 3
	}
	return count
}

// looksLikeRepairOrContinueTask is true when the user is fixing a red gate
// rather than describing a multi-phase greenfield. Cross-language: go test /
// pytest / npm test / cargo test are the same signal.
func looksLikeRepairOrContinueTask(taskDesc string) bool {
	if hasExplicitMultiPhase(taskDesc) {
		return false
	}
	lower := strings.ToLower(taskDesc)
	tokens := []string{
		"接着修", "请接着", "刚才那轮", "刚才那次", "没过门", "修到能过",
		"还是红", "修红", "needs_remediation",
		"fix the failing", "fix failing tests", "make tests pass", "make the tests pass",
		"continue fixing", "tests still fail", "test still fail",
		"go test 绿", "pytest 绿",
	}
	for _, t := range tokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	if (strings.Contains(lower, "修复") || strings.Contains(lower, "fix ")) &&
		(strings.Contains(lower, "失败") || strings.Contains(lower, "fail") ||
			strings.Contains(lower, "没过") || strings.Contains(lower, "go test") ||
			strings.Contains(lower, "pytest") || strings.Contains(lower, "npm test") ||
			strings.Contains(lower, "cargo test")) {
		return true
	}
	return false
}

func hasExplicitMultiPhase(taskDesc string) bool {
	return maxExplicitPhaseNumber(taskDesc) >= 2 ||
		countChineseStageMarkers(taskDesc) >= 2 ||
		parseExplicitStageTotal(taskDesc) >= 2
}

// containsPhaseIndicator matches CJK substrings as-is, but requires ASCII
// word boundaries so "TestMigration" does not count as a "migration" phase.
func containsPhaseIndicator(lower, ind string) bool {
	if ind == "" {
		return false
	}
	ascii := true
	for _, r := range ind {
		if r > 127 {
			ascii = false
			break
		}
	}
	if !ascii {
		return strings.Contains(lower, ind)
	}
	start := 0
	for {
		i := strings.Index(lower[start:], ind)
		if i < 0 {
			return false
		}
		i += start
		beforeOK := i == 0 || !isASCIILetterByte(lower[i-1])
		after := i + len(ind)
		afterOK := after >= len(lower) || !isASCIILetterByte(lower[after])
		if beforeOK && afterOK {
			return true
		}
		start = i + 1
	}
}

func isASCIILetterByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// countChineseStageMarkers counts 第一段/第二阶段/第3段 style markers.
func countChineseStageMarkers(taskDesc string) int {
	re := regexp.MustCompile(`第\s*([一二三四五六七八九十百\d]+)\s*(?:段|阶段|期)`)
	seen := map[int]bool{}
	for _, m := range re.FindAllStringSubmatch(taskDesc, -1) {
		n := parseChineseOrDigit(m[1])
		if n >= 1 && n <= 12 {
			seen[n] = true
		}
	}
	if len(seen) == 0 {
		return 0
	}
	max := 0
	for n := range seen {
		if n > max {
			max = n
		}
	}
	// Prefer max ordinal when markers are sequential (第一…第三 → 3).
	return max
}

// parseExplicitStageTotal catches "一共三段" / "共 3 个阶段" / "3 phases".
func parseExplicitStageTotal(taskDesc string) int {
	re := regexp.MustCompile(`(?i)(?:一共|共|总计|总共|总共有)?\s*([一二三四五六七八九十\d]+)\s*(?:段|个阶段|阶段|phases?)`)
	if m := re.FindStringSubmatch(taskDesc); len(m) >= 2 {
		if n := parseChineseOrDigit(m[1]); n >= 1 && n <= 12 {
			return n
		}
	}
	return 0
}

func parseChineseOrDigit(s string) int {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	zh := map[string]int{
		"一": 1, "二": 2, "两": 2, "三": 3, "四": 4, "五": 5,
		"六": 6, "七": 7, "八": 8, "九": 9, "十": 10,
	}
	if n, ok := zh[s]; ok {
		return n
	}
	return 0
}

// EstimatePhaseCountForTask is the exported wrapper for confirm/complexity sizing (G6/G10).
func EstimatePhaseCountForTask(taskDesc string) int {
	return estimatePhaseCount(taskDesc)
}

// maxExplicitPhaseNumber returns the highest Phase/阶段 number mentioned (1–12).
func maxExplicitPhaseNumber(taskDesc string) int {
	re := regexp.MustCompile(`(?i)(?:phase|阶段)\s*([1-9]\d?)`)
	max := 0
	for _, m := range re.FindAllStringSubmatch(taskDesc, -1) {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if n > max && n <= 12 {
			max = n
		}
	}
	return max
}

// countDistinctExplicitPhases counts unique Phase/阶段 numbers (G1/G10).
func countDistinctExplicitPhases(taskDesc string) int {
	re := regexp.MustCompile(`(?i)(?:phase|阶段)\s*([1-9]\d?)`)
	seen := map[int]bool{}
	for _, m := range re.FindAllStringSubmatch(taskDesc, -1) {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if n >= 1 && n <= 12 {
			seen[n] = true
		}
	}
	return len(seen)
}

// replacePlanPhaseCount updates both markdown and plain Phase Count lines.
func replacePlanPhaseCount(planStr string, phaseCount int) string {
	if phaseCount < 1 {
		phaseCount = 1
	}
	re := regexp.MustCompile(`(?m)^(>\s*\*\*Phase Count\*\*:\s*)\d+`)
	if re.MatchString(planStr) {
		return re.ReplaceAllString(planStr, fmt.Sprintf("${1}%d", phaseCount))
	}
	rePlain := regexp.MustCompile(`(?m)^(>\s*Phase Count:\s*)\d+`)
	if rePlain.MatchString(planStr) {
		return rePlain.ReplaceAllString(planStr, fmt.Sprintf("${1}%d", phaseCount))
	}
	return strings.Replace(planStr, "Phase Count: 1", fmt.Sprintf("Phase Count: %d", phaseCount), 1)
}

// ensurePlanPhaseSections appends Phase 2..N stubs when Phase Count > 1 but the
// plan only lists Phase 1 (Batch mid NL: multi-phase collapsed to 1).
func ensurePlanPhaseSections(planStr string, phaseCount int) string {
	if phaseCount <= 1 {
		return planStr
	}
	present := map[int]bool{}
	phaseHdr := regexp.MustCompile(`(?i)###\s*Phase\s*(\d+)`)
	for _, m := range phaseHdr.FindAllStringSubmatch(planStr, -1) {
		n := 0
		fmt.Sscanf(m[1], "%d", &n)
		if n >= 1 {
			present[n] = true
		}
	}
	var b strings.Builder
	for i := 1; i <= phaseCount; i++ {
		if present[i] {
			continue
		}
		b.WriteString(fmt.Sprintf("\n### Phase %d: TBD\n", i))
		b.WriteString("- **Goal**: (to be refined)\n")
		b.WriteString("- **Status**: pending\n")
		b.WriteString(fmt.Sprintf("- **Detail**: docs/workflow/phase%d.md\n", i))
	}
	extra := b.String()
	if extra == "" {
		return planStr
	}
	// Insert before Key Decisions / Risks / Success Criteria if present.
	for _, marker := range []string{"\n## Key Decisions", "\n## Risks", "\n## Success Criteria"} {
		if idx := strings.Index(planStr, marker); idx >= 0 {
			return planStr[:idx] + extra + planStr[idx:]
		}
	}
	return planStr + extra
}

// EnforcePhaseCountInPlan bumps Phase Count (and stubs) when the user
// explicitly listed more phases than the plan currently claims. Safe to call
// after LLM ConstructPlan which often collapses "本轮 Phase 1" into Count=1.
func EnforcePhaseCountInPlan(planStr, taskDesc string) string {
	// F88: repairing a red test/build must not pad TBD phases onto an
	// existing (possibly deferred) Phase Count.
	if looksLikeRepairOrContinueTask(taskDesc) && !hasExplicitMultiPhase(taskDesc) {
		return planStr
	}
	want := estimatePhaseCount(taskDesc)
	if want < 2 {
		return planStr
	}
	meta := ParsePlanMeta(planStr)
	if meta.PhaseCount >= want {
		return ensurePlanPhaseSections(planStr, meta.PhaseCount)
	}
	out := replacePlanPhaseCount(planStr, want)
	return ensurePlanPhaseSections(out, want)
}

// EnforcePlanPhaseCountFromTask rewrites docs/workflow/avatars_plan.md when the
// on-disk Phase Count under-counts explicit Phase N markers in the task.
// Always materializes phaseN.md skeletons for the resulting count (PY2) and
// re-syncs todo from phase docs (PY1).
func EnforcePlanPhaseCountFromTask(projectRoot, taskDesc string) (int, error) {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	data, err := os.ReadFile(planPath)
	if err != nil {
		return 0, err
	}
	want := estimatePhaseCount(taskDesc)
	out := string(data)
	if looksLikeRepairOrContinueTask(taskDesc) && !hasExplicitMultiPhase(taskDesc) {
		meta := ParsePlanMeta(out)
		count := meta.PhaseCount
		if count < 1 {
			count = 1
		}
		if _, skErr := EnsurePhaseDocSkeletons(projectRoot, count); skErr != nil {
			return count, skErr
		}
		_ = SyncTodoFromPlan(projectRoot)
		_, _ = SyncPhaseDocsProcessTag(projectRoot)
		return count, nil
	}
	if want >= 2 {
		out = EnforcePhaseCountInPlan(string(data), taskDesc)
		if out != string(data) {
			if err := WriteFileAtomic(planPath, []byte(out), 0644); err != nil {
				return 0, err
			}
		}
	}
	meta := ParsePlanMeta(out)
	count := meta.PhaseCount
	if want > count {
		count = want
	}
	if count < 1 {
		count = 1
	}
	if _, skErr := EnsurePhaseDocSkeletons(projectRoot, count); skErr != nil {
		return count, skErr
	}
	_ = SyncTodoFromPlan(projectRoot)
	_, _ = SyncPhaseDocsProcessTag(projectRoot)
	return count, nil
}

func collectSourceFiles(b *strings.Builder, dir string, prefix string) bool {
	entries, err := os.ReadDir(filepath.Join(dir, prefix))
	if err != nil {
		return false
	}
	added := false
	for _, e := range entries {
		name := e.Name()
		if name == ".git" || name == ".avatars" || name == "docs" || name == "avatars" || strings.HasPrefix(name, ".") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(prefix, name))
		if e.IsDir() {
			added = collectSourceFiles(b, dir, rel) || added
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".rs", ".java":
			if strings.Contains(name, "_test.") || strings.Contains(name, ".test.") {
				b.WriteString(fmt.Sprintf("- [ ] Verify tests pass: %s\n", rel))
			} else {
				b.WriteString(fmt.Sprintf("- [ ] Implement: %s\n", rel))
			}
			added = true
		}
	}
	return added
}
