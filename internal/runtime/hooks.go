package runtime

import (
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/arch"
	"avatars/internal/llm"
	"avatars/internal/verification"
	"avatars/internal/workflow"
)

// =============================================================================
// INTEGRATION HOOKS — called from loop.go with single-line insertions.
// All the logic lives here so loop.go stays clean.
// =============================================================================

// HookArchitectureBeforePlan creates/updates architecture.md before
// the Planner builds the execution plan. I29.
func HookArchitectureBeforePlan(input string) {
	if cwd, err := os.Getwd(); err == nil {
		_ = arch.EnsureArchitectureDoc(cwd, input)
	}
}

// HookSyncTodoAtStart ensures todo reflects plan state at the start of every run. I30.
// S2.3: SyncTodoFromPlan is phase-aware (full rebuild only on phase change).
func HookSyncTodoAtStart() {
	plan, err := workflow.ReadWorkflowDoc(".", "plan")
	if err == nil {
		ap := workflow.ParsePlanMeta(plan).ActivePhase
		if ap < 1 {
			ap = 1
		}
		_ = workflow.MarkPhaseChecklistByPathEvidence(".", ap)
	}
	workflow.SyncTodoFromPlan(".")
	_ = workflow.ReconcileChecklistHonesty(".")
}

// HookExpandActivePhaseAtStart expands a deferred Active Phase stub into full
// detail before Builder runs (layered plan R1). generate may be nil → no-op.
func HookExpandActivePhaseAtStart(generate workflow.GenerateFunc) (expanded bool, phase int) {
	if generate == nil {
		return false, 0
	}
	ok, n, err := workflow.ExpandActivePhaseDetail(".", generate)
	if err != nil || !ok {
		return false, n
	}
	return true, n
}

// HookCheckPhaseDocsAtStart refreshes the Phase Docs process tag and returns
// missing phase numbers (language-agnostic workflow integrity). Batch1/G.
func HookCheckPhaseDocsAtStart() (missing []int) {
	missing, err := workflow.SyncPhaseDocsProcessTag(".")
	if err != nil || len(missing) == 0 {
		return missing
	}
	return missing
}

// HookAlignStackAtStart rewrites plan/phase DB decisions when the user input
// or project manifests conflict with Key Decisions (Batch3/P9).
func HookAlignStackAtStart(input string) string {
	summary, err := workflow.AlignStackDecisions(".", input)
	if err != nil || summary == "" {
		return ""
	}
	return summary
}

// workflowPlanInProgress reports an active multi-phase plan on disk.
func workflowPlanInProgress(projectRoot string) bool {
	return workflow.HasActiveProjectPlan(projectRoot)
}

// resolveWorkflowBuildTestOK prefers verifier evidence already on the run
// result; otherwise probes cross-language health once for WorkflowSyncCtx (S2.2).
//
// Important: never blindly test the ambient cwd (unit tests / read-only
// runs). Only probe when this run mutated files or synthesis already reported
// a verifier outcome. Mirrors Claude Code: completion needs evidence from the
// run, not a second unbounded test sweep.
//
// NL smoke Batch1/C: with changed source files, do NOT trust verifier:pass
// alone — re-probe crossLangHealthCheck (Go/Python/Node/Rust/TS).
func resolveWorkflowBuildTestOK(projectRoot string, result RunResult, changedFiles []string) (buildOK bool, testOK bool) {
	status := strings.ToLower(strings.TrimSpace(result.SynthesisStatus))
	summary := strings.ToLower(result.Summary)
	hasSourceChange := false
	for _, f := range changedFiles {
		ext := strings.ToLower(filepath.Ext(f))
		switch ext {
		case ".go", ".py", ".js", ".mjs", ".ts", ".tsx", ".jsx", ".rs", ".java":
			hasSourceChange = true
		}
		base := strings.ToLower(filepath.Base(f))
		if base == "go.mod" || base == "cargo.toml" || base == "package.json" ||
			base == "pyproject.toml" || base == "requirements.txt" {
			hasSourceChange = true
		}
	}
	knownFail := strings.Contains(status, "fail") || strings.Contains(status, "needs_remediation") ||
		strings.Contains(summary, "verifier: fail") || strings.Contains(summary, "go build failed") ||
		strings.Contains(summary, "build_ok=false") || strings.Contains(summary, "post_build_failed") ||
		strings.Contains(summary, "timed out") || strings.Contains(summary, "timeout") ||
		strings.Contains(summary, "run status: failed")
	// F60: confirm/construct plan failures are planning gates, not compile failures.
	if isPlanningGateFailure(result) {
		knownFail = false
	}
	// F102: provider 402 / quota is not a language compile failure.
	if isSynthesisAvailabilityFailure(result) {
		knownFail = false
	}

	if !hasSourceChange && (strings.Contains(status, "pass") || strings.Contains(summary, "verifier: pass") ||
		strings.Contains(summary, "verification passed")) {
		return true, true
	}
	if len(changedFiles) == 0 {
		if knownFail {
			return false, false
		}
		// Batch6/R (S1): no writes this run must NOT invent build_failed.
		// Probe real health — green build stays green (footer/status stay honest).
		if healthErr := crossLangHealthCheck(projectRoot); healthErr != "" {
			return false, false
		}
		return true, true
	}
	// Probe real health for any language stack.
	healthErr := crossLangHealthCheck(projectRoot)
	buildOK = healthErr == ""
	if !buildOK {
		return false, false
	}
	// Language-specific tests: Go uses go test; Node/Python/Rust already
	// included in crossLangHealthCheck via projectTestCheck (N1-5).
	if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err == nil {
		return true, runGoTestCheck(projectRoot)
	}
	// Health green already ran npm/pytest/cargo test when manifests exist.
	return true, true
}

// isPlanningGateFailure reports confirm/construct plan hard-stops (F60).
// These must not be labeled build_failed when no compile was attempted.
func isPlanningGateFailure(result RunResult) bool {
	blob := strings.ToLower(strings.TrimSpace(result.SynthesisStatus) + " " + strings.TrimSpace(result.Summary))
	if blob == "" {
		return false
	}
	return strings.Contains(blob, "confirm_plan") ||
		strings.Contains(blob, "construct_plan") ||
		strings.Contains(blob, "confirm plan failed") ||
		strings.Contains(blob, "plan construction failed")
}

// isSynthesisAvailabilityFailure reports provider quota/billing/402 so
// disk-green runs are not labeled build_failed (F102). Language-agnostic.
func isSynthesisAvailabilityFailure(result RunResult) bool {
	return llm.IsQuotaOrBillingFailure(result.SynthesisStatus) ||
		llm.IsQuotaOrBillingFailure(result.Summary)
}

// applyHonestVerificationGate downgrades PASS/PARTIAL to FAIL when this run
// wrote source/manifest files but cross-lang project health still fails (P10).
func (e *Engine) applyHonestVerificationGate(wd string, report verification.Report) verification.Report {
	if e == nil {
		return report
	}
	if report.Verdict != verification.VerdictPass && report.Verdict != verification.VerdictPartial {
		return report
	}
	wroteSource := false
	for _, f := range e.changedFiles {
		base := strings.ToLower(filepath.Base(f))
		if pathLooksCodeTarget(f) || base == "go.mod" || base == "cargo.toml" ||
			base == "package.json" || base == "pyproject.toml" || base == "requirements.txt" {
			wroteSource = true
			break
		}
	}
	if !wroteSource {
		return report
	}
	if health := crossLangHealthCheck(wd); health != "" {
		report.Verdict = verification.VerdictFail
		report.Summary = "Verification finished with FAIL (project health after source writes). " + health
		report.Warnings = append(report.Warnings, "honest_gate: cross-lang health failed — refusing PASS while build_ok=false")
	}
	return report
}
