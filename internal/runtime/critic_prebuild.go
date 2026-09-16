package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"

	"avatars/internal/llm"
	"avatars/internal/workflow"
)

// criticReviewPlan performs a read-only review of the project plan and survey
// findings BEFORE Builder generates code. It is used by node-review-plan (the
// I21 pre-build review gate).
//
// Unlike the post-build Critic (node-review), this function must NOT dispatch
// Builder — there is no generated code to audit yet. It checks:
//  1. Plan completeness: are Goals, Phases, Success Criteria present?
//  2. Phase detail: does each phase have concrete, verifiable tasks?
//  3. Survey alignment: do survey findings cover all required areas?
//
// Returns a summary string that is injected into the Builder's context so it
// can address gaps before writing code.
func criticReviewPlan(work workflowNodeWorkContext) string {
	var sb strings.Builder
	sb.WriteString("Pre-build plan review:\n")

	// 1. Check plan completeness.
	wd, err := os.Getwd()
	if err != nil {
		sb.WriteString("- WARNING: cannot read working directory\n")
		return sb.String()
	}

	planContent, planErr := workflow.ReadWorkflowDoc(wd, "plan")
	if planErr != nil {
		sb.WriteString(fmt.Sprintf("- WARNING: cannot read plan: %v\n", planErr))
		return sb.String()
	}

	checks := 0
	passed := 0

	// Check for Goals section.
	if strings.Contains(planContent, "## Goals") {
		checks++
		if hasNonEmptyListItems(planContent, "## Goals", "## Phases") {
			passed++
		} else {
			sb.WriteString("- GAP: Goals section has no specific, non-placeholder items.\n")
		}
	}

	// Check for Phases.
	if strings.Contains(planContent, "## Phases") {
		checks++
		phaseCount := strings.Count(planContent, "### Phase")
		if phaseCount > 0 {
			passed++
			sb.WriteString(fmt.Sprintf("- OK: Plan has %d phase(s).\n", phaseCount))
		} else {
			sb.WriteString("- GAP: No phases defined.\n")
		}
	}

	// Check for Success Criteria.
	if strings.Contains(planContent, "## Success Criteria") {
		checks++
		if hasNonEmptyListItems(planContent, "## Success Criteria", "\n## ") {
			passed++
		} else {
			sb.WriteString("- GAP: Success Criteria have no measurable outcomes.\n")
		}
	}

	// 2. Check phase1.md quality.
	if data, err := os.ReadFile(getPhasePath(wd, 1)); err == nil {
		text := string(data)
		placeholderChecks := []string{"[Phase Name]", "[estimated duration]", "[Task Group Name]", "[Verification step"}
		remaining := 0
		for _, ph := range placeholderChecks {
			if strings.Contains(text, ph) {
				remaining++
			}
		}
		if remaining > 0 {
			checks++
			sb.WriteString(fmt.Sprintf("- GAP: phase1.md has %d unfilled template placeholder(s).\n", remaining))
		} else {
			checks++
			passed++
			sb.WriteString("- OK: phase1.md is fully filled (no placeholders).\n")
		}
	}

	// 3. Survey alignment: does the readSummary have content?
	if work.readSummary != "" && !strings.Contains(work.readSummary, "No bootstrap context file was found") {
		checks++
		passed++
		sb.WriteString("- OK: Survey findings are available for Builder context.\n")
	}

	sb.WriteString(fmt.Sprintf("\nPre-build review: %d/%d checks passed.\n", passed, checks))
	if passed < checks {
		sb.WriteString("Builder should address the GAP items noted above before generating code.\n")
	}
	return sb.String()
}

// hasNonEmptyListItems returns true if the section between startHeader and
// endHeader (or end of file) contains at least one list item ("- ") that is
// not a template placeholder (doesn't start with "[").
func hasNonEmptyListItems(content, startHeader, endHeader string) bool {
	start := strings.Index(content, startHeader)
	if start < 0 {
		return false
	}
	section := content[start:]
	if endIdx := strings.Index(section[len(startHeader):], endHeader); endIdx > 0 {
		section = section[:len(startHeader)+endIdx]
	}
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "- [") {
			return true
		}
	}
	return false
}

// getPhasePath returns the path to phaseN.md.
func getPhasePath(root string, n int) string {
	return fmt.Sprintf("%s/docs/workflow/phase%d.md", root, n)
}

// ReviewPlanBeforeBuild orchestrates pre-build plan review: format checks first
// (fast, deterministic), then LLM content review (evaluates plan quality).
// Returns findings string for injection into Builder context, or "" if solid.
//
// I21: This activates the previously dead criticReviewPlan and adds LLM-based
// content evaluation — phase ordering, feasibility, completeness, risk, and
// architecture alignment.
//
// Call from executeBuilderNodeWork before Builder generates code.
func ReviewPlanBeforeBuild(ctx context.Context, work workflowNodeWorkContext, llmClient llm.Client, model string, personality PersonalityConfig) string {
	var findings strings.Builder
	hasGaps := false

	// 0. Critic director charter — deterministic layout command (F42).
	if wd, err := os.Getwd(); err == nil {
		findings.WriteString(criticDirectorLayoutBrief(wd))
		findings.WriteString("\n")
		if target := parseGenerateTarget(work.nodeTitle); target != "" {
			if _, _, reject := directorRewriteGenerateTitle(wd, work.nodeTitle); reject != "" {
				findings.WriteString("- [BLOCKER] " + reject + "\n")
				hasGaps = true
			}
		}
		if layoutErr := detectConflictingGoLayout(wd); layoutErr != "" {
			findings.WriteString("- [BLOCKER] " + layoutErr + "\n")
			hasGaps = true
		}
	}

	// 1. Format checks first (fast, deterministic, no LLM cost).
	formatFindings := criticReviewPlan(work)
	if strings.Contains(formatFindings, "GAP:") || strings.Contains(formatFindings, "WARNING:") {
		hasGaps = true
	}
	findings.WriteString(formatFindings)

	// 2. LLM content review — evaluates actual plan quality.
	if llmClient != nil {
		wd, err := os.Getwd()
		if err == nil {
			planContent, planErr := workflow.ReadWorkflowDoc(wd, "plan")
			if planErr == nil && planContent != "" {
				llmFindings := criticReviewPlanContent(ctx, llmClient, model, planContent, work.readSummary, personality)
				if llmFindings != "" {
					findings.WriteString("\n\n=== LLM Content Review ===\n")
					findings.WriteString(llmFindings)
					hasGaps = true
				}
			}
		}
	}

	if !hasGaps {
		return "" // plan is solid
	}
	return findings.String()
}
