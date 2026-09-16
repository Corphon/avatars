package runtime

import (
	"context"
	"strings"
	"time"

	"avatars/internal/llm"
)

// =============================================================================
// CRITIC PLAN CONTENT REVIEW — I21 enhancement
// Replaces format-only criticReviewPlan with LLM-based content evaluation.
// Adapted from claude_code_main's adversarial verification pattern:
//   "Your job is not to confirm the implementation works — it's to try to break it."
// =============================================================================

// planReviewTimeoutDefault is the fallback timeout for plan content review
// LLM calls. When Engine.PlanReviewTimeout is set, that value is used instead.
// T1: Engine loads its timeout from agent.yaml; this constant is the fallback.
const planReviewTimeoutDefault = 60 * time.Second

// planReviewPrompt is the system prompt for the LLM plan reviewer.
// Structured to produce concise, actionable, severity-tagged findings.
const planReviewPrompt = `You are an adversarial plan reviewer. Your job is to FIND FLAWS in this execution plan — not to confirm it works.

Evaluate the plan across 7 dimensions:
1. **Phase Ordering**: are phases in the right dependency order? Could any be parallelized?
2. **Feasibility**: can each phase actually be completed with the given context? Are there gaps?
3. **Completeness**: any missing phases, tasks, or concerns the plan didn't address?
4. **Risk**: what could go wrong? What edge cases are ignored?
5. **Architecture Alignment**: does the plan fit the existing code structure? Does it violate patterns?
6. **Test Coverage** (C2): are there phases that lack test coverage? Does the plan include testing as explicit tasks or assume it implicitly?
7. **External Dependencies** (C2): does any phase assume the existence of services, APIs, or tools that haven't been confirmed available?

If the plan is solid, say "No findings." — do not fabricate issues.
If you find issues, output a bullet list. Each finding MUST use this exact format:

- [BLOCKER] description of critical issue that prevents execution
- [WARNING] description of likely problem
- [INFO] description of suggestion for improvement

BLOCKER = plan cannot proceed as written (missing dependency, impossible phase order, conflict)
WARNING = likely to cause problems (risky assumption, unclear scope, missing detail)
INFO = suggestion that would improve quality but isn't blocking

Keep findings specific and actionable. Reference specific phases or sections of the plan.
Keep under 400 words. No preamble, no closing remarks.`

// criticReviewPlanContent performs LLM-based content review of the execution plan.
// Returns actionable findings with severity tags, or empty string if plan is solid.
// Unlike criticReviewPlan (format-only checks), this evaluates the actual content
// quality of the plan: phase ordering, feasibility, completeness, risk, and
// architecture alignment.
func criticReviewPlanContent(ctx context.Context, llmClient llm.Client, model string, planContent string, readSummary string, personality PersonalityConfig) string {
	if planContent == "" {
		return ""
	}

	userPrompt := buildPlanReviewUserPrompt(planContent, readSummary)
	if userPrompt == "" {
		return ""
	}

	reviewCtx, cancel := context.WithTimeout(ctx, llm.TimeoutConfigOrDefault().PlanReview)
	defer cancel()

	reviewReq := llm.Request{
		SystemPrompt: appendPersonality(planReviewPrompt, personality),
		UserPrompt:   userPrompt,
		Model:        model,
		Category:     llm.CategoryAnalysis,
		// ThinkMode intentionally unset: Critic plan-review is advisory judgment —
		// inherit agent.yaml (auto → provider may enable CoT). Unlike Builder/
		// Synthesizer, empty/slow CoT here is fail-silent and does not block the run.
	}
	resp, err := llmClient.Generate(reviewCtx, reviewReq)
	if err != nil {
		// Fail silently — don't inject error messages into Builder's prompt.
		// Format checks already ran and provide sufficient review.
		return ""
	}
	if resp.Fallback {
		return ""
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" || strings.Contains(text, "No findings") {
		return ""
	}

	return text
}

// buildPlanReviewUserPrompt constructs the user prompt for the plan reviewer.
// Includes the plan content and survey findings, capped to avoid token waste.
func buildPlanReviewUserPrompt(planContent string, readSummary string) string {
	var b strings.Builder

	// Cap plan content to prevent overflowing the review LLM's context.
	// Plan reviews shouldn't need more than 6000 chars to evaluate quality.
	const maxPlanChars = 6000
	plan := planContent
	if len(plan) > maxPlanChars {
		plan = plan[:maxPlanChars] + "\n\n[... plan truncated for review ...]"
	}
	b.WriteString("Plan to review:\n```\n")
	b.WriteString(plan)
	b.WriteString("\n```")

	if readSummary != "" {
		// Cap survey findings similarly.
		const maxSurveyChars = 3000
		survey := readSummary
		if len(survey) > maxSurveyChars {
			survey = survey[:maxSurveyChars] + "\n[... survey truncated for review ...]"
		}
		b.WriteString("\n\nSurvey findings from Researcher:\n```\n")
		b.WriteString(survey)
		b.WriteString("\n```")
	}

	b.WriteString("\n\nReview the plan above. Output findings as [BLOCKER]/[WARNING]/[INFO] bullet list, or \"No findings.\"")
	return b.String()
}
