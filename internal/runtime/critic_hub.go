// Package runtime — CriticHub: centralized decision hub for the Critic avatar.
//
// The Critic is the director. After Builder produces output, Critic inspects
// it and decides the next step. CriticHub extends this to dispatch Researcher,
// Planner, or ask the user.
//
// v3-P0 changes:
//   - C-1: dispatchResearcher results flow back into work context (readSummary/structuredContext)
//   - D-5: dispatchResearcher injects a real DAG node via WorkflowState.AddNode
//   - C-3: askUser returns a PausePoint so the REPL can pause and wait for user input
//   - H-1: Decide returns a CriticHubResult so loop.go can act on the decision

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/workflow"
)

// CriticHub is the centralized decision hub for the Critic avatar.
type CriticHub struct {
	engine *Engine
}

// NewCriticHub creates a CriticHub bound to the given Engine.
func NewCriticHub(e *Engine) *CriticHub {
	return &CriticHub{engine: e}
}

// CriticHubResult carries the outcome of a Critic decision back to loop.go.
// v3-P0 H-1: loop.go uses this to merge Researcher results into work context
// or to surface a PausePoint for user input.
type CriticHubResult struct {
	// ResearcherSummary is the read summary produced by dispatchResearcher.
	// C-1: loop.go appends this to nodeWorkResult.readSummary so Builder sees it.
	ResearcherSummary string
	// PausePoint is set when askUser decides to pause for user input.
	// C-3: loop.go returns this as a pause point, halting execution until user responds.
	PausePoint *PausePoint
	// ErrorMessage is set when retries are exhausted and the run should not silently pass.
	// C-4: loop.go returns this as an error so the user knows the task failed.
	ErrorMessage string
	// Decision is a human-readable description of what CriticHub decided.
	Decision string
	// EnqueueNodes are remedial DAG nodes to schedule (S4.9) instead of inline bypass.
	EnqueueNodes []WorkflowNodeRuntime
}

// Decide is the main entry point. It inspects the current workflow state
// and decides which avatar to dispatch next, or whether to ask the user.
// Returns a CriticHubResult so the caller can act on the decision.
func (ch *CriticHub) Decide(ctx context.Context, work workflowNodeWorkContext) CriticHubResult {
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		return CriticHubResult{}
	}

	// P5: Staged quality gate (S4.8: prefers changedFiles when available).
	// On failure: one inline Builder fix with concrete syntax/build/test errors
	// (NL smoke: Critic previously never fed go test failures into a fix cycle).
	if stageResult := ch.stagedQualityGate(wd, work); stageResult.Decision != "" {
		if isHostToolchainFailure(stageResult.ErrorMessage) {
			stageResult.ErrorMessage = annotateHostToolchainFailure(stageResult.ErrorMessage)
			stageResult.Decision = "host toolchain failure — cannot fix via Builder"
			ch.emit(work, "critic.hub_host_toolchain_abort", map[string]any{
				"error": stageResult.ErrorMessage,
			})
			return stageResult
		}
		if !ch.tryFixStagedQualityFailure(ctx, work, wd, stageResult) {
			// Re-probe so ErrorMessage reflects post-attempt state; abort same-cause.
			if retry := ch.stagedQualityGate(wd, work); retry.Decision != "" {
				if isHostToolchainFailure(retry.ErrorMessage) {
					retry.ErrorMessage = annotateHostToolchainFailure(retry.ErrorMessage)
					return retry
				}
				if ch.engine != nil && ch.engine.sameQualityFailureRepeated(retry.ErrorMessage) {
					retry.ErrorMessage = fmt.Sprintf("same quality failure repeated — %s", retry.ErrorMessage)
					retry.Decision = "same-cause circuit breaker"
					return retry
				}
				return retry
			}
			return stageResult
		}
	}
	// 1. Implementation completeness — enqueue Builder (S4.9), do not inline.
	if nodes, ok := ch.checkAndEnqueueImpl(wd, work); ok {
		return CriticHubResult{
			Decision:     "enqueued Builder to fix missing implementations",
			EnqueueNodes: nodes,
		}
	}

	// Phase 7: Token Budget — if Builder has been retried excessively
	// for this node, escalate to Planner regardless of failure count.
	if ch.engine.nodeRetryCount != nil {
		builderKey := "node-build"
		if retries := ch.engine.nodeRetryCount[builderKey]; retries >= 3 {
			nodes := ch.enqueuePlanner(work)
			return CriticHubResult{
				Decision:     fmt.Sprintf("escalated to Planner after %d Builder retries", retries),
				EnqueueNodes: nodes,
			}
		}
	}

	// 2. Verification — how many rounds have we failed?
	_, _, remaining := workflowCountTodo(wd)
	if remaining == 0 {
		return CriticHubResult{Decision: "all items complete"}
	}

	// 3. Stuck with remaining items — escalate progressively.
	failures := ch.countVerificationFailures(work)
	switch {
	case failures >= 5:
		// S4.8 / F20: REPL → PausePoint; headless → remediation error (no fake
		// "ask user" that cannot be answered, and F14 blocks phase advance).
		question := fmt.Sprintf(
			"Verification has failed %d times with %d items remaining. What should I do?",
			failures, remaining)
		if ch.engine != nil && strings.TrimSpace(ch.engine.replContext) != "" {
			pp := ch.askUserPause(work, question)
			return CriticHubResult{
				PausePoint: &pp,
				Decision:   fmt.Sprintf("asked user via pause (failures=%d, remaining=%d)", failures, remaining),
			}
		}
		ch.emit(work, "critic.hub_remediation_needed", map[string]any{
			"failures":  failures,
			"remaining": remaining,
			"message":   "headless mode — re-run after fixing build/test, or continue in REPL to guide Critic",
		})
		return CriticHubResult{
			ErrorMessage: fmt.Sprintf("critic: verification failed %d times with %d items remaining — needs_remediation (headless: fix tests/build then re-run; REPL can supply guidance)", failures, remaining),
			Decision:     fmt.Sprintf("remediation hold (failures=%d, remaining=%d) — no phase advance", failures, remaining),
		}
	case failures >= 3:
		nodes, summary := ch.enqueueResearcher(work, wd)
		return CriticHubResult{
			ResearcherSummary: summary,
			Decision:          fmt.Sprintf("enqueued Researcher for deep-dive (failures=%d)", failures),
			EnqueueNodes:      nodes,
		}
	case failures >= 1:
		nodes := ch.enqueuePlanner(work)
		return CriticHubResult{
			Decision:     "enqueued Planner for re-analysis",
			EnqueueNodes: nodes,
		}
	}
	return CriticHubResult{Decision: "no action needed"}
}

// checkAndEnqueueImpl queues a Builder remedial node when symbols are missing (S4.9).
func (ch *CriticHub) checkAndEnqueueImpl(wd string, work workflowNodeWorkContext) ([]WorkflowNodeRuntime, bool) {
	// Batch6/S / G11: same green short-circuit as critic_fix — but never on
	// empty / stub-only disks (parent go.mod must not fake green either).
	if projectLooksSkipGreenSafe(wd) {
		ch.emit(work, "critic.hub_impl_skip_green", map[string]any{
			"reason": "compile/health green — skip symbol-scan false positives",
		})
		return nil, false
	}
	missing := ch.checkImplCompleteness(wd)
	if len(missing) == 0 {
		return nil, false
	}
	missing = filterGapItemsByFileExistence(missing, wd)
	if len(missing) == 0 {
		return nil, false
	}
	ch.emit(work, "critic.hub_impl_fix", map[string]any{
		"missing": missing,
		"action":  "enqueue Builder to implement missing symbols",
	})
	nodeID := fmt.Sprintf("critic-builder-impl-%d", time.Now().Unix())
	node := WorkflowNodeRuntime{
		ID:           nodeID,
		Title:        "Critic-requested missing implementations",
		AssignedRole: "Builder",
		DependsOn:    []string{work.nodeID},
	}
	if work.workflowState != nil {
		if err := work.workflowState.AddNode(node); err != nil {
			ch.emit(work, "critic.hub_enqueue_failed", map[string]any{
				"error":   err.Error(),
				"node_id": nodeID,
				"role":    "Builder",
			})
		}
	}
	return []WorkflowNodeRuntime{node}, true
}

// tryFixStagedQualityFailure runs go mod tidy when needed, then one Builder
// pass with the staged-gate error. Returns true if a subsequent gate pass succeeds.
func (ch *CriticHub) tryFixStagedQualityFailure(ctx context.Context, work workflowNodeWorkContext, wd string, stage CriticHubResult) bool {
	errMsg := strings.TrimSpace(stage.ErrorMessage)
	if errMsg == "" || ch.engine == nil {
		return false
	}
	// R11-1: host toolchain / missing linker etc. cannot be fixed by rewriting
	// application code — never dispatch Builder (avoids 20m Critic timeout).
	if isHostToolchainFailure(errMsg) {
		ch.engine.noteQualityFailure(errMsg)
		ch.emit(work, "critic.hub_quality_fix_skipped_env", map[string]any{
			"error":  errMsg,
			"reason": "host toolchain failure — Builder rewrite cannot fix",
		})
		return false
	}
	// T1/T2: purge poison stubs / junk scaffold before burning Builder LLM cycles.
	if removed := purgeUnhealthySourcesOnDisk(ch.engine.sweepRoot()); len(removed) > 0 {
		ch.emit(work, "critic.hub_purged_poison_sources", map[string]any{
			"deleted": removed,
			"count":   len(removed),
			"reason":  "remove compile-poison stubs before quality fix",
		})
		retry := ch.stagedQualityGate(wd, work)
		if retry.Decision == "" {
			return true
		}
		if retry.ErrorMessage != "" {
			errMsg = retry.ErrorMessage
			stage = retry
		}
	}
	// F22: duplicated toolchain is a one-line harness heal — fix before Builder rewrite.
	if strings.Contains(strings.ToLower(errMsg), "repeated toolchain") ||
		strings.Contains(strings.ToLower(errMsg), "errors parsing go.mod") {
		if ensureGoModNormalized(wd) {
			ch.emit(work, "critic.hub_go_mod_normalized", map[string]any{
				"reason": "deduped repeated toolchain/go lines after compile fail",
			})
			retry := ch.stagedQualityGate(wd, work)
			if retry.Decision == "" {
				return true
			}
			if retry.ErrorMessage != "" {
				errMsg = retry.ErrorMessage
				stage = retry
			}
		}
	}
	// U1/U2/W1: missing package deps OR network/download flakes — install/retry
	// first, never waste Builder rewrite on "downloading …" / go mod tidy fails.
	if looksLikeMissingPackageDeps(errMsg) || looksLikeMissingGoSum(errMsg) ||
		looksLikeDependencyNetworkFailure(errMsg) {
		ch.emit(work, "critic.hub_ensure_dependencies", map[string]any{
			"error":  errMsg,
			"reason": "compile/test failure looks like missing/network deps — install before Builder",
		})
		if looksLikeDependencyNetworkFailure(errMsg) || looksLikeMissingGoSum(errMsg) {
			_ = os.Remove(filepath.Join(wd, ".avatars", "go_mod_tidy_failed"))
			if tidyErr := runGoModTidyWithRetry(wd, 3); tidyErr == nil {
				ch.emit(work, "critic.hub_go_mod_tidy", map[string]any{
					"reason": "deps network/missing sum — retried go mod tidy",
				})
			}
		}
		if depErr := ensureProjectDependencies(wd); depErr != "" {
			ch.engine.noteQualityFailure(depErr)
			if looksLikeDependencyNetworkFailure(depErr) || looksLikeDependencyNetworkFailure(errMsg) {
				ch.emit(work, "critic.hub_quality_fix_skipped_deps", map[string]any{
					"error":  depErr,
					"reason": "deps network unavailable after retry — Builder rewrite cannot fix",
					"kind":   "deps_network",
				})
				return false
			}
			ch.emit(work, "critic.hub_quality_fix_skipped_deps", map[string]any{
				"error":  depErr,
				"reason": "dependency install failed — Builder rewrite cannot fix",
			})
			return false
		}
		// Force npm reinstall attempt when types still look missing but
		// npmDepsNeedInstall thought tree was OK (partial node_modules).
		if looksLikeMissingPackageDeps(errMsg) {
			_ = forceNpmInstallOnce(wd)
		}
		retry := ch.stagedQualityGate(wd, work)
		if retry.Decision == "" {
			return true
		}
		if retry.ErrorMessage != "" {
			errMsg = retry.ErrorMessage
			stage = retry
		}
		// Still a pure "missing Node/types toolchain" signal after install → abort Builder.
		if looksLikePersistentMissingTypes(errMsg) {
			ch.engine.noteQualityFailure(errMsg)
			ch.emit(work, "critic.hub_quality_fix_skipped_deps", map[string]any{
				"error":  errMsg,
				"reason": "deps installed but compile still reports missing types/runtime — skip Builder thrash",
			})
			return false
		}
		// Network still red after ensure → do not Builder-rewrite.
		if looksLikeDependencyNetworkFailure(errMsg) {
			ch.engine.noteQualityFailure(errMsg)
			ch.emit(work, "critic.hub_quality_fix_skipped_deps", map[string]any{
				"error":  errMsg,
				"reason": "deps still network-red after ensure — skip Builder thrash",
				"kind":   "deps_network",
			})
			return false
		}
	}
	// R11-3: identical compile/test failure already attempted → stop thrash.
	if ch.engine.noteQualityFailure(errMsg) >= 2 {
		ch.emit(work, "critic.hub_quality_fix_same_cause", map[string]any{
			"error":       errMsg,
			"fingerprint": ch.engine.lastQualityFailFingerprint,
			"count":       ch.engine.qualityFailRepeatCount,
			"reason":      "same quality failure repeated — skipping Builder rewrite",
		})
		return false
	}
	const maxQualityFixRetries = 2
	if ch.engine.nodeRetryCount != nil && ch.engine.nodeRetryCount["node-build"] >= maxQualityFixRetries {
		ch.emit(work, "critic.hub_quality_fix_capped", map[string]any{
			"retries": ch.engine.nodeRetryCount["node-build"],
			"error":   errMsg,
		})
		return false
	}
	ch.engine.recordNodeRetryFailure("node-build")
	if !ch.engine.beginCriticBuilderCycle("quality_gate") {
		ch.emit(work, "critic.hub_quality_fix_capped", map[string]any{
			"cycles": ch.engine.criticBuilderCycles,
			"error":  errMsg,
		})
		return false
	}

	if looksLikeMissingGoSum(errMsg) {
		if tidyErr := runGoModTidy(wd); tidyErr == nil {
			ch.emit(work, "critic.hub_go_mod_tidy", map[string]any{
				"reason": "compile/test failed with missing module/sum — ran go mod tidy",
			})
			// Tidy alone may clear the failure (no code rewrite needed).
			if retry := ch.stagedQualityGate(wd, work); retry.Decision == "" {
				return true
			}
		}
	}

	ch.emit(work, "critic.hub_quality_fix", map[string]any{
		"error":  errMsg,
		"action": "inline Builder fix from staged quality gate",
	})
	fixWork := work
	fixWork.input = "FIX QUALITY GATE FAILURE (syntax / build / test):\n" + errMsg +
		"\n\nDiagnose root cause from the error above. Apply the SMALLEST fix. " +
		"Do NOT rewrite unrelated files. After your fix, project compile and tests must pass. " +
		"If the error is missing packages/types, run the language package manager install — do not invent stub packages."
	fixWork.customSystemPrompt = criticFixSystemPrompt
	if _, err := ch.engine.executeBuilderNodeWork(ctx, fixWork); err != nil {
		ch.emit(work, "critic.hub_quality_fix_failed", map[string]any{"error": err.Error()})
		return false
	}
	retry := ch.stagedQualityGate(wd, work)
	return retry.Decision == ""
}

func looksLikeMissingGoSum(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, token := range []string{
		"missing go.sum",
		"updates to go.mod needed",
		"missing go.sum entry",
		"cannot find module",
		"no required module provides",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// looksLikeMissingPackageDeps reports compile failures that often clear after
// package-manager install (U1) — npm/@types, pip ModuleNotFound for third-party.
// R1: local roots like `app` / `src` must NOT trigger install thrash.
func looksLikeMissingPackageDeps(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, token := range []string{
		"cannot find name 'node:",
		"cannot find name \"node:",
		"try `npm i --save-dev",
		"try npm i --save-dev",
		"ts2591",
		"could not find a declaration file",
		"npm install failed",
		"pip install -r requirements.txt failed",
		// W1: Go module fetch / tidy failures (before Builder rewrite).
		"go mod tidy failed",
		"missing go.sum",
		"updates to go.mod needed",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	if m := noModuleNamedRe.FindStringSubmatch(errMsg); len(m) == 2 {
		if isLocalProjectModuleName(m[1]) {
			return false
		}
		return true
	}
	// Broad ModuleNotFoundError without a parseable name — only if not clearly local.
	if strings.Contains(lower, "modulenotfounderror") {
		if strings.Contains(lower, "no module named 'app'") ||
			strings.Contains(lower, "no module named \"app\"") ||
			strings.Contains(lower, "no module named 'src'") {
			return false
		}
		return true
	}
	return false
}

// looksLikePersistentMissingTypes is the post-install abort signal (U2):
// Builder rewrite cannot invent @types/node or node: builtins.
func looksLikePersistentMissingTypes(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, token := range []string{
		"cannot find name 'node:",
		"cannot find name \"node:",
		"try `npm i --save-dev",
		"try npm i --save-dev",
		"ts2591",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// forceNpmInstallOnce clears the "deps OK" heuristic and runs npm install once
// (partial node_modules that still miss @types/node).
func forceNpmInstallOnce(wd string) string {
	if _, err := os.Stat(filepath.Join(wd, "package.json")); err != nil {
		return ""
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npm", "install", "--no-audit", "--no-fund")
	cmd.Dir = wd
	withInheritedProxyEnv(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		errStr := strings.TrimSpace(string(output))
		if errStr == "" {
			errStr = err.Error()
		}
		errStr = clipCommandOutput(errStr, 1200)
		return annotateHostToolchainFailure(fmt.Sprintf("npm install failed: %s", errStr))
	}
	_ = os.Remove(filepath.Join(wd, ".avatars", npmInstallErrorMarker))
	return ""
}

func runGoModTidy(wd string) error {
	// F22: tidy cannot parse duplicated toolchain lines — normalize first.
	_ = ensureGoModNormalized(wd)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = wd
	withInheritedProxyEnv(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go mod tidy: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// enqueueResearcher adds a Researcher node to the DAG (S4.9). Still runs a
// lightweight inline survey once so Critic can merge findings in the same turn;
// the DAG node records the work for scheduler honesty.
func (ch *CriticHub) enqueueResearcher(work workflowNodeWorkContext, wd string) ([]WorkflowNodeRuntime, string) {
	ch.emit(work, "critic.hub_dispatch_researcher", map[string]any{
		"reason": "verification failures — deep-diving before next Builder attempt",
	})
	_, _, remaining := workflowCountTodo(wd)
	researchWork := work
	researchWork.input = fmt.Sprintf(
		"DEEP DIVE: %d items remain incomplete after multiple Builder attempts. "+
			"Read the failing files and identify ROOT CAUSES. "+
			"Report: what exactly is wrong and what needs to change.",
		remaining)
	result, _ := ch.engine.executeResearcherNodeWork(context.Background(), researchWork)

	nodeID := fmt.Sprintf("critic-researcher-%d", time.Now().Unix())
	node := WorkflowNodeRuntime{
		ID:           nodeID,
		Title:        "Critic-requested deep dive",
		AssignedRole: "Researcher",
		DependsOn:    []string{work.nodeID},
	}
	if work.workflowState != nil {
		if err := work.workflowState.AddNode(node); err != nil {
			ch.emit(work, "critic.hub_enqueue_failed", map[string]any{
				"error":   err.Error(),
				"node_id": nodeID,
				"role":    "Researcher",
			})
		}
	}
	return []WorkflowNodeRuntime{node}, strings.TrimSpace(result.readSummary)
}

// enqueuePlanner records a Planner replan intent on the DAG (S4.9).
func (ch *CriticHub) enqueuePlanner(work workflowNodeWorkContext) []WorkflowNodeRuntime {
	ch.emit(work, "critic.hub_dispatch_planner", map[string]any{
		"reason": "verification failures — re-planning before next Builder attempt",
	})
	// Keep executeReplan for immediate plan file update; also enqueue DAG node.
	if _, err := ch.engine.executeReplan(context.Background(), work); err != nil {
		ch.emit(work, "critic.hub_replan_failed", map[string]any{"error": err.Error()})
	}
	nodeID := fmt.Sprintf("critic-planner-%d", time.Now().Unix())
	node := WorkflowNodeRuntime{
		ID:           nodeID,
		Title:        "Critic-requested replan",
		AssignedRole: "Planner",
		DependsOn:    []string{work.nodeID},
	}
	if work.workflowState != nil {
		if err := work.workflowState.AddNode(node); err != nil {
			ch.emit(work, "critic.hub_enqueue_failed", map[string]any{
				"error":   err.Error(),
				"node_id": nodeID,
				"role":    "Planner",
			})
		}
	}
	return []WorkflowNodeRuntime{node}
}

// checkAndFixImpl is retained for unit tests that expect immediate Builder work.
func (ch *CriticHub) checkAndFixImpl(wd string, work workflowNodeWorkContext) bool {
	if projectLooksSkipGreenSafe(wd) {
		return false
	}
	missing := ch.checkImplCompleteness(wd)
	if len(missing) == 0 {
		return false
	}
	missing = filterGapItemsByFileExistence(missing, wd)
	if len(missing) == 0 {
		return false
	}
	ch.emit(work, "critic.hub_impl_fix", map[string]any{
		"missing": missing,
		"action":  "dispatching Builder to implement missing symbols (legacy inline)",
	})
	gapWork := work
	gapWork.input = "MISSING IMPLEMENTATIONS:\n" + strings.Join(missing, "\n") +
		"\n\nImplement ONLY these missing parts. Do NOT create new files."
	_, err := ch.engine.executeBuilderNodeWork(context.Background(), gapWork)
	return err == nil
}

// dispatchResearcher kept for compatibility; prefer enqueueResearcher.
func (ch *CriticHub) dispatchResearcher(ctx context.Context, work workflowNodeWorkContext, wd string) string {
	_, summary := ch.enqueueResearcher(work, wd)
	_ = ctx
	return summary
}

// dispatchPlanner kept for compatibility; prefer enqueuePlanner.
func (ch *CriticHub) dispatchPlanner(ctx context.Context, work workflowNodeWorkContext) {
	_ = ch.enqueuePlanner(work)
	_ = ctx
}

// askUser emits an event asking the user for guidance.
// Deprecated: use askUserPause which returns a PausePoint.
func (ch *CriticHub) askUser(work workflowNodeWorkContext, question string) {
	ch.emit(work, "critic.hub_ask_user", map[string]any{
		"question": question,
		"options":  []string{"continue", "change_strategy", "abort"},
	})
}

// askUserPause emits an event AND returns a PausePoint so the REPL can
// pause execution and wait for user input.
// C-3: This is the real "ask user" — the pause point halts the workflow
// until the user responds via the REPL/serve layer.
func (ch *CriticHub) askUserPause(work workflowNodeWorkContext, question string) PausePoint {
	ch.emit(work, "critic.hub_ask_user", map[string]any{
		"question": question,
		"options":  []string{"continue", "change_strategy", "abort"},
	})
	pp := NewPausePoint(
		PausePointKindUserInput,
		work.runID,
		work.taskID,
		work.nodeID,
		"", "", "critic", "ask_user",
		hashPausePointParts([]string{question, work.nodeID}),
	)
	pp.StatusReason = question
	pp.NodeRole = "Critic"
	pp.NodeTitle = "Critic asking user for guidance"
	return pp
}

// countVerificationFailures estimates how many times verification has failed
// for this run (S4.8: prefer current-run records over whole-task history).
func (ch *CriticHub) countVerificationFailures(work workflowNodeWorkContext) int {
	if ch.engine == nil || ch.engine.memory == nil {
		return 0
	}
	snapshot, err := ch.engine.memory.LoadLatestTaskSnapshot(work.taskID)
	if err != nil {
		return 0
	}
	runID := strings.TrimSpace(work.runID)
	runScoped := 0
	total := 0
	for _, record := range snapshot.EvaluationRecords {
		if !strings.Contains(strings.ToLower(record.Verdict), "fail") {
			continue
		}
		total++
		if runID != "" && strings.EqualFold(strings.TrimSpace(record.RunID), runID) {
			runScoped++
		}
	}
	if runScoped > 0 {
		return runScoped
	}
	return total
}

// checkImplCompleteness delegates to workflow.CheckImplementationCompleteness.
func (ch *CriticHub) checkImplCompleteness(projectRoot string) []string {
	return workflowCheckImpl(projectRoot)
}

// emit is a convenience wrapper for emitting Critic hub events.
func (ch *CriticHub) emit(work workflowNodeWorkContext, eventType string, payload map[string]any) {
	if ch.engine != nil && ch.engine.transcript != nil {
		_ = ch.engine.emit(work.runID, work.taskID, work.criticAvatarID,
			"reviewing", eventType, "critic_hub", payload, nil)
	}
}

// workflowCountTodo is a thin wrapper around workflow.CountTodoProgressDetailed.
func workflowCountTodo(projectRoot string) (completed, total, remaining int) {
	completed, total, remaining = workflow.CountTodoProgressDetailed(projectRoot)
	return
}

// workflowCheckImpl is a thin wrapper around workflow.CheckImplementationCompleteness.
func workflowCheckImpl(projectRoot string) []string {
	return workflow.CheckImplementationCompleteness(projectRoot)
}

// CriticHubDecide is the standalone entry point called from loop.go.
// v3-P0: Now returns a CriticHubResult so loop.go can merge Researcher
// results into the work context and handle PausePoints.
func CriticHubDecide(e *Engine, ctx context.Context, work workflowNodeWorkContext) CriticHubResult {
	hub := NewCriticHub(e)
	result := hub.Decide(ctx, work)
	hub.recordToProcessRecord(work, result)
	return result
}

func (ch *CriticHub) recordToProcessRecord(work workflowNodeWorkContext, result CriticHubResult) {
	failures := ch.countVerificationFailures(work)
	_, _, remaining := workflowCountTodo(getwdOrEmpty())
	if failures >= 3 {
		ch.emit(work, "critic.hub_record_pitfall", map[string]any{
			"pitfall": fmt.Sprintf("PITFALL: %d verification failures — Builder may be systematically wrong", failures),
		})
	}
	if remaining > 0 && failures >= 5 {
		ch.emit(work, "critic.hub_record_blocker", map[string]any{
			"blocker": fmt.Sprintf("BLOCKER: %d items incomplete after %d failures", remaining, failures),
		})
	}
}

func getwdOrEmpty() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}
