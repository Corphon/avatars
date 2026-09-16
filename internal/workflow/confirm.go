package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IsConfirmPlan returns true if the user's input is a confirmation to proceed
// with the current plan (generate phases and start phase 1).
func IsConfirmPlan(taskInput string) bool {
	lower := strings.ToLower(taskInput)
	confirmPhrases := []string{
		"confirm the plan", "confirm plan", "approve the plan", "approve plan",
		"确认计划", "批准计划", "同意计划",
		"generate phases", "generate all phases", "生成阶段", "生成所有阶段",
		"proceed with the plan", "proceed with plan",
	}
	for _, phrase := range confirmPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// IsReplan returns true if the user wants to regenerate the plan.
func IsReplan(taskInput string) bool {
	lower := strings.ToLower(taskInput)
	replanPhrases := []string{
		"replan", "re-plan", "重新规划", "重新计划", "regenerate plan", "regenerate the plan",
	}
	for _, phrase := range replanPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// ConfirmPlan transitions the plan from draft to in-progress, materializes
// layered phase docs (full detail for Active Phase only; deferred stubs for
// the rest), and populates the todo with phase 1 tasks.
//
// A1–A3 / Z2–Z4: refuse placeholder plans; on LLM failure, only treat
// non-empty phase docs as "reused detailed"; if Active Phase still needs
// full detail, revert to draft and return an error (do not activate / load
// fake todos from English templates). Soft-degrade only when a real filled
// Active Phase already exists on disk.
func ConfirmPlan(projectRoot string, generate GenerateFunc) (string, error) {
	// A3: plan must not still be the PreloadDocs / plan_builder placeholder.
	if IsPlanEmpty(projectRoot) {
		return "", fmt.Errorf("plan still has template placeholders ([Name]/[One-line goal]/…); fill docs/workflow/avatars_plan.md before confirm")
	}

	// 1. Update plan status: draft → in-progress (reverted on hard fail below).
	if err := UpdatePlanStatus(projectRoot, "in-progress"); err != nil {
		return "", fmt.Errorf("update plan status: %w", err)
	}

	// 2. Update process record.
	RecordTaskCompletion(projectRoot, "Plan confirmed", "User confirmed the project plan. Expanding Active Phase detail; stubbing later phases.")

	// 3. Layered phase docs: LLM Active Phase only; stubs for the rest.
	generated, reused, stubbed, genErr := ConstructPhasesLayered(projectRoot, generate)
	degraded := false
	degradeReason := ""
	if genErr != nil {
		degraded = true
		degradeReason = genErr.Error()
		// A1: never call empty/preload templates "detailed reused".
		reused = listFilledDetailedPhaseDocs(projectRoot)
		generated = nil
		stubbed = listDeferredOrEmptyPhaseDocs(projectRoot)
	}

	active := 1
	if planContent, rErr := ReadWorkflowDoc(projectRoot, "plan"); rErr == nil {
		if ap := ParsePlanMeta(planContent).ActivePhase; ap >= 1 {
			active = ap
		}
	}

	// A2: Active Phase still empty/stub after LLM failure → hard stop.
	if degraded && PhaseDocNeedsFullDetail(projectRoot, active) {
		_ = UpdatePlanStatus(projectRoot, "draft")
		RecordTaskCompletion(projectRoot, "Plan confirm aborted",
			fmt.Sprintf("HAZARD: phase %d detail missing after LLM failure; status reverted to draft", active))
		var sb strings.Builder
		sb.WriteString("Plan confirm FAILED — Active Phase detail not available.\n\n")
		// Distinguish quality-gate rejection (thin Tasks) from true LLM outage.
		if isPhaseDetailQualityGateError(degradeReason) {
			sb.WriteString("⚠️ Phase generation was rejected by the quality gate (not an LLM outage).\n")
			sb.WriteString("  Reason: " + degradeReason + "\n")
			sb.WriteString("  Status reverted to draft; Phase 1 was NOT activated.\n")
			sb.WriteString("  Empty/template phase files were NOT treated as detailed docs.\n")
			sb.WriteString("  Fix: expand Active Phase ## Tasks to cover every Scope & Success Criteria\n")
			sb.WriteString("  item (see uncovered list in Reason), then re-run confirm.\n")
			sb.WriteString("  Do not chase compile/test errors until confirm succeeds.\n")
		} else {
			sb.WriteString("⚠️ Phase generation failed (LLM unavailable or returned an error).\n")
			sb.WriteString("  Reason: " + degradeReason + "\n")
			sb.WriteString("  Status reverted to draft; Phase 1 was NOT activated.\n")
			sb.WriteString("  Empty/template phase files were NOT treated as detailed docs.\n")
			sb.WriteString("  Re-run confirm when the LLM is available.\n")
		}
		if len(reused) > 0 {
			sb.WriteString(fmt.Sprintf("\nKept %d already-filled phase file(s):\n", len(reused)))
			for _, name := range reused {
				sb.WriteString("  - docs/workflow/" + name + "\n")
			}
		}
		return sb.String(), fmt.Errorf("phase %d detail missing after LLM failure: %w", active, genErr)
	}

	// 4. Populate todo with ALL phase checklists; pointer at phase 1.
	if err := SyncTodoForPhase(projectRoot, 1); err != nil && !degraded {
		return "", fmt.Errorf("sync phase 1 todo: %w", err)
	}

	// Batch3/P9: align Key Decisions / phase prose with confirmed stack.
	if _, alignErr := AlignStackDecisions(projectRoot, ""); alignErr != nil {
		_ = alignErr
	}

	var sb strings.Builder
	sb.WriteString("Plan confirmed and activated (layered phase docs).\n\n")
	if degraded {
		sb.WriteString("⚠️ Phase generation degraded (LLM unavailable or failed).\n")
		sb.WriteString("  Reason: " + degradeReason + "\n")
		sb.WriteString("  Status is in-progress; existing filled phase docs were kept.\n")
		sb.WriteString("  Re-run confirm when LLM is available to fill missing phases.\n\n")
	}
	if len(generated) > 0 {
		sb.WriteString(fmt.Sprintf("Expanded Active Phase detail (%d file(s)):\n", len(generated)))
		for _, name := range generated {
			sb.WriteString("  - docs/workflow/" + name + "\n")
		}
	}
	if len(stubbed) > 0 {
		sb.WriteString(fmt.Sprintf("Deferred stubs for later phases (%d):\n", len(stubbed)))
		for _, name := range stubbed {
			sb.WriteString("  - docs/workflow/" + name + " (expand when Active)\n")
		}
	}
	if len(reused) > 0 {
		sb.WriteString(fmt.Sprintf("Reused %d existing detailed phase file(s):\n", len(reused)))
		for _, name := range reused {
			sb.WriteString("  - docs/workflow/" + name + "\n")
		}
	}
	if len(generated) == 0 && len(reused) == 0 && len(stubbed) == 0 {
		sb.WriteString("No phase detail files were found in the plan.\n")
	}

	// NL smoke Batch1/G: durable process tag for phase-doc completeness.
	if missing, syncErr := SyncPhaseDocsProcessTag(projectRoot); syncErr == nil {
		if len(missing) > 0 {
			sb.WriteString(fmt.Sprintf("\n⚠️ Phase Docs incomplete — missing phase files: %v\n", missing))
			sb.WriteString("  Process tag written to avatars_plan.md (> **Phase Docs**: incomplete).\n")
			RecordTaskCompletion(projectRoot, "Phase docs incomplete",
				fmt.Sprintf("HAZARD: incomplete phase docs after confirm: %v", missing))
		} else {
			sb.WriteString("\nPhase Docs process tag: complete (Active detailed; later phases may be stubs).\n")
		}
	}

	sb.WriteString("\nPhase 1 tasks loaded into avatars_todo.md.\n")
	if !degraded {
		sb.WriteString("Say \"start implementing\" or re-run with acceptEdits to begin implementation.\n")
	}

	return sb.String(), nil
}

// listFilledDetailedPhaseDocs returns phaseN.md names that exist and are
// real filled detail (not PreloadDocs placeholders, not deferred stubs).
func listFilledDetailedPhaseDocs(projectRoot string) []string {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return nil
	}
	meta := ParsePlanMeta(planContent)
	var filled []string
	for i := range meta.Phases {
		name := fmt.Sprintf("phase%d.md", i+1)
		path := PhaseDocPath(projectRoot, i+1)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		if IsPhaseDocEmpty(text) || IsPhaseDocDeferredStub(text) {
			continue
		}
		filled = append(filled, name)
	}
	return filled
}

// listDeferredOrEmptyPhaseDocs lists phase files that are missing, empty
// templates, or deferred stubs (for degrade messaging — not "reused detailed").
func listDeferredOrEmptyPhaseDocs(projectRoot string) []string {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return nil
	}
	meta := ParsePlanMeta(planContent)
	var names []string
	for i := range meta.Phases {
		name := fmt.Sprintf("phase%d.md", i+1)
		path := PhaseDocPath(projectRoot, i+1)
		data, err := os.ReadFile(path)
		if err != nil || IsPhaseDocEmpty(string(data)) || IsPhaseDocDeferredStub(string(data)) {
			names = append(names, name)
		}
	}
	return names
}

// listExistingPhaseDocs is retained for callers that only need on-disk names;
// prefer listFilledDetailedPhaseDocs when labeling "reused detailed".
func listExistingPhaseDocs(projectRoot string) (generated []string, reused []string) {
	return nil, listFilledDetailedPhaseDocs(projectRoot)
}

// WantsImplementAfterConfirm reports whether input confirms the plan and also
// asks to start implementation in the same utterance (NL smoke #5/#11).
func WantsImplementAfterConfirm(input string) bool {
	if !IsConfirmPlan(input) {
		return false
	}
	lower := strings.ToLower(input)
	for _, s := range []string{"然后", "接着", "立刻", "立即", "开始写", "开始实现", "写代码", "then ", "implement", "start coding", "write code", "生成 go.mod", "按 phase"} {
		if strings.Contains(input, s) || strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func phaseDocsPresent(projectRoot string) bool {
	return allPhaseDocsPresent(projectRoot)
}

// allPhaseDocsPresent reports whether every phase declared in avatars_plan.md
// has a filled phaseN.md on disk (not missing, not a PreloadDocs placeholder).
func allPhaseDocsPresent(projectRoot string) bool {
	nums, err := missingPhaseNumbers(projectRoot)
	if err != nil {
		return false
	}
	return len(nums) == 0
}

// PhaseDocNeedsGeneration reports whether phaseN.md is missing or still a template.
func PhaseDocNeedsGeneration(projectRoot string, phaseNum int) bool {
	path := PhaseDocPath(projectRoot, phaseNum)
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return IsPhaseDocEmpty(string(data))
}

func missingPhaseNumbers(projectRoot string) ([]int, error) {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return nil, err
	}
	meta := ParsePlanMeta(planContent)
	if len(meta.Phases) == 0 {
		return []int{1}, nil
	}
	var missing []int
	for i := range meta.Phases {
		phaseNum := i + 1
		if PhaseDocNeedsGeneration(projectRoot, phaseNum) {
			missing = append(missing, phaseNum)
		}
	}
	return missing, nil
}

// MissingPhaseNumbers is the exported form of missingPhaseNumbers.
func MissingPhaseNumbers(projectRoot string) ([]int, error) {
	return missingPhaseNumbers(projectRoot)
}

// IsPhaseEmpty reports whether phase1.md is missing or still a template.
// Kept for BootstrapWorkflowFromTask; multi-phase completeness uses
// allPhaseDocsPresent / missingPhaseNumbers.
func IsPhaseEmpty(projectRoot string) bool {
	return PhaseDocNeedsGeneration(projectRoot, 1)
}

// isPhaseDetailQualityGateError reports whether ConfirmPlan/ConstructPhase failed
// because generated phase markdown failed Tasks⊇Scope (or similar) gates — not
// because the LLM transport was unavailable.
func isPhaseDetailQualityGateError(reason string) bool {
	lower := strings.ToLower(strings.TrimSpace(reason))
	if lower == "" {
		return false
	}
	needles := []string{
		"thin tasks rejected",
		"tasks do not cover scope",
		"does not cover scope",
		"phase doc quality",
		"missing task checkboxes",
		"no real task checkbox",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// IsPhaseDocEmpty reports whether phase markdown is unfilled template content.
// Shared by ConfirmPlan reuse gate and BootstrapWorkflowFromTask skip checks.
func IsPhaseDocEmpty(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" {
		return true
	}
	// Markers from templates/phase_template.md and plan_builder.go.
	placeholders := []string{
		"[Phase Name]",
		"[Task Group Name]",
		"[Verifiable task-level outcome]",
		"[Specific, actionable task",
		"[2-3 sentences describing what this phase",
		"[estimated duration]",
		"[Reference files or APIs to consult]",
		"[Tools, credentials, or access needed]",
		"[Verification step",
	}
	for _, p := range placeholders {
		if strings.Contains(text, p) {
			return true
		}
	}
	return !phaseDocHasRealTaskCheckbox(text)
}

func phaseDocHasRealTaskCheckbox(content string) bool {
	inTasks := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, "## ") {
			inTasks = strings.Contains(lower, "task")
			continue
		}
		if !inTasks {
			continue
		}
		var item string
		switch {
		case strings.HasPrefix(trimmed, "- [ ]"):
			item = strings.TrimSpace(trimmed[len("- [ ]"):])
		case strings.HasPrefix(trimmed, "- [x]"), strings.HasPrefix(trimmed, "- [X]"):
			item = strings.TrimSpace(trimmed[len("- [x]"):])
		default:
			continue
		}
		if item == "" || strings.HasPrefix(item, "[") {
			continue
		}
		return true
	}
	return false
}

func existingPhaseDocNames(projectRoot string) []string {
	dir := filepath.Join(projectRoot, "docs", "workflow")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "phase") && strings.HasSuffix(name, ".md") {
			names = append(names, name)
		}
	}
	return names
}

// BuildConfirmPromptMessage returns the user-facing message to display after
// plan construction, with clear next-step options.
func BuildConfirmPromptMessage(planContent string) string {
	meta := ParsePlanMeta(planContent)

	var sb strings.Builder
	sb.WriteString("Plan constructed: docs/workflow/avatars_plan.md\n\n")
	sb.WriteString(fmt.Sprintf("Project: %s\n", meta.Project))
	sb.WriteString(fmt.Sprintf("Phases: %d\n", meta.PhaseCount))
	sb.WriteString("\n## Next Steps\n\n")
	sb.WriteString("1. Review the plan in docs/workflow/avatars_plan.md\n")
	sb.WriteString("   Check goals, phases, risks, and success criteria\n\n")
	sb.WriteString("2. Confirm to generate phase detail files and start:\n")
	sb.WriteString("   avatars run \"confirm the plan\"\n\n")
	sb.WriteString("3. Modify if something needs changing:\n")
	sb.WriteString("   avatars run \"In docs/workflow/avatars_plan.md: change X to Y\"\n")
	sb.WriteString("   Then confirm again.\n\n")
	sb.WriteString("4. Replan if the direction is wrong:\n")
	sb.WriteString("   avatars run \"replan for <new requirement>\"\n")

	return sb.String()
}
