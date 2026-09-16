package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"avatars/internal/workflow"
)

func (e *Engine) executeCriticNodeWork(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	_ = ctx
	// S6.8: Do not dump Critic role templates into readSummary (evidence
	// pollution). Static criticReviewCode below is evidence-only; LLM Critic
	// paths should inject skills on sysPrompt, not into survey text.

	// Heartbeat: tell user Critic is starting its audit.
	// P2: Include retry cycle count for transparency.
	cycleInfo := ""
	if e.nodeRetryCount != nil {
		if count := e.nodeRetryCount[work.nodeID]; count > 0 {
			cycleInfo = fmt.Sprintf(" (cycle %d/%d)", count+1, 3)
		}
	}
	_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "heartbeat.critic_auditing", "runtime", map[string]any{
		"message":     "Critic auditing generated code against checklist..." + cycleInfo,
		"retry_cycle": e.nodeRetryCount[work.nodeID],
		"max_retries": 3,
	}, nil)

	// Batch2: remove root pollution when the correct package path already exists.
	// Never sweep the avatars harness source tree (cwd is often this repo or
	// internal/runtime during go test).
	if wd0 := e.sweepRoot(); wd0 != "" && !isAvatarsHarnessTree(wd0) {
		if removed := purgeMisplacedRootDuplicates(wd0); len(removed) > 0 {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.purged_misplaced_root", "runtime", map[string]any{
				"deleted": removed,
				"count":   len(removed),
			}, nil)
		}
		if removed := purgeRootGenScripts(wd0); len(removed) > 0 {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.purged_root_gen_scripts", "runtime", map[string]any{
				"deleted": removed,
				"count":   len(removed),
			}, nil)
		}
		// R11-2: sweep truncated leftovers before audit/rebuild.
		if removed := purgeUnhealthySourcesOnDisk(wd0); len(removed) > 0 {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.purged_unhealthy_sources", "runtime", map[string]any{
				"deleted": removed,
				"count":   len(removed),
			}, nil)
		}
		// F34: remove hollow internal/doc when the real library package already exists.
		if removed := purgeHollowGoDocPackages(wd0); len(removed) > 0 {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.purged_hollow_doc_package", "runtime", map[string]any{
				"deleted": removed,
				"count":   len(removed),
			}, nil)
		}
		// F38/F42: remove buried internal/<lib> duplicate when top-level lib exists.
		if removed := purgeDuplicateBuriedLibrary(wd0); len(removed) > 0 {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.purged_buried_library", "runtime", map[string]any{
				"deleted": removed,
				"count":   len(removed),
			}, nil)
		}
		// F55: root package shell (e.g. root doc.go) colliding with lib/ tree.
		if removed := purgeRootConflictingGoPackage(wd0); len(removed) > 0 {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.purged_root_package_conflict", "runtime", map[string]any{
				"deleted": removed,
				"count":   len(removed),
			}, nil)
		}
	}

	// T9.3: Critic→Builder feedback — review generated code for
	// common issues and produce actionable findings.
	findings := criticReviewCode(work)
	criticSummary := "Critic review: "
	if len(findings) > 0 {
		criticSummary += fmt.Sprintf("%d findings.", len(findings))
	} else {
		criticSummary += "no issues found."
	}

	// W16+W17: Critic audit-mark-dispatch loop. "总导演审核→mark→补漏→再审核"
	// 1. Audit checklist against generated code → mark completed items
	// 2. If gaps remain → dispatch Builder up to 3 times
	// 3. After each dispatch → re-audit and re-mark
	const maxCriticDispatches = 2 // TR-Fix-13b
	wd, _ := os.Getwd()
	// PY1: re-derive todo from phase docs before Critic reads gaps — prevents
	// chasing stale "[Task Group Name]" after phase1.md was filled/marked.
	_ = workflow.SyncTodoFromPlan(wd)
	criticMarked := workflow.CriticAuditAndMark(wd)
	if criticMarked > 0 {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.critic_audit_marked", "runtime", map[string]any{"count": criticMarked}, nil)
	}

	// Count remaining [ ] items. Dispatch Builder with SPECIFIC gap instructions.
	_, total, remaining := workflow.CountTodoProgressDetailed(wd)
	prevRemaining := remaining
	// H4: before rebuild thrash, ensure go.sum exists when go.mod has requires.
	if needsGoModTidy(wd, nil) {
		if tidyErr := runGoModTidy(wd); tidyErr == nil {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.go_mod_tidy", "runtime", map[string]any{
				"reason": "go.mod present without go.sum (or imports changed) — ran go mod tidy before Critic rebuild",
			}, nil)
		} else {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.go_mod_tidy_failed", "runtime", map[string]any{
				"error": tidyErr.Error(),
			}, nil)
		}
	}
	// W3: always try disk→todo progress when scaffold exists (even if total==0
	// because [>] was previously uncounted, or checklist was truncated).
	if diskHasAppSources(wd) {
		if n := workflow.MarkScaffoldProgressFromDisk(wd); n > 0 {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.scaffold_progress_marked", "runtime", map[string]any{
				"marked": n,
			}, nil)
			_, total, remaining = workflow.CountTodoProgressDetailed(wd)
		}
	}
	// R3-15: run staged build+test gate before skip-green so failures emit
	// critic.stage2* events and force rebuild even when checklist looks soft.
	// R11-1/3: host-toolchain / same-cause failures must abort Critic immediately —
	// do NOT fall through into checklist→Builder rebuild (that burned 20m on r2).
	if hub := NewCriticHub(e); hub != nil {
		if gate := hub.stagedQualityGate(wd, work); gate.Decision != "" {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.early_quality_gate", "runtime", map[string]any{
				"decision": gate.Decision,
				"error":    gate.ErrorMessage,
			}, nil)
			if isHostToolchainFailure(gate.ErrorMessage) {
				_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.host_toolchain_abort", "runtime", map[string]any{
					"error":  gate.ErrorMessage,
					"reason": "host toolchain failure — skipping Critic↔Builder rebuild loop",
				}, nil)
				// F65: still produce user-facing delivery; do not skip Synthesizer.
				return e.softFailCriticDelivery(wd, "critic: "+annotateHostToolchainFailure(gate.ErrorMessage)), nil
			}
			// C9: import/layout failures — heal Go dual-package tests / shadows once; soft-fail if still red.
			importLayoutCleared := false
			if isImportLayoutFailure(gate.ErrorMessage) {
				if healed := healGoLibTestPackagesOnDisk(wd); len(healed) > 0 {
					_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.go_lib_test_package_healed", "runtime", map[string]any{
						"healed": healed,
					}, nil)
				}
				if removed := resolveModulePackageShadowsOnDisk(wd); len(removed) > 0 {
					_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.module_package_shadow_resolved", "runtime", map[string]any{
						"removed": removed,
					}, nil)
				}
				if retry := hub.stagedQualityGate(wd, work); retry.Decision == "" {
					importLayoutCleared = true
				}
				if !importLayoutCleared {
					_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.import_layout_abort", "runtime", map[string]any{
						"error":  gate.ErrorMessage,
						"reason": "import/layout failure — skipping Critic↔Builder rebuild thrash",
					}, nil)
					return e.softFailCriticDelivery(wd, "critic: import/layout failure: "+gate.ErrorMessage), nil
				}
			}
			// Prefer one concrete fix pass before checklist thrash when budget remains.
			if !importLayoutCleared && !e.criticBuilderCyclesExhausted() {
				fixed := hub.tryFixStagedQualityFailure(ctx, work, wd, gate)
				_, total, remaining = workflow.CountTodoProgressDetailed(wd)
				if !fixed {
					if retry := hub.stagedQualityGate(wd, work); retry.Decision != "" {
						_ = e.noteQualityFailure(retry.ErrorMessage)
						msg := retry.ErrorMessage
						if isHostToolchainFailure(msg) {
							msg = annotateHostToolchainFailure(msg)
						}
						_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.quality_fix_abort", "runtime", map[string]any{
							"error":  msg,
							"reason": "quality gate still red after fix attempt — abort rebuild thrash",
						}, nil)
						return e.softFailCriticDelivery(wd, "critic: "+msg), nil
					}
				}
			}
		}
	}

	// PY7/V3/G11: when compile/health/test is already green, do not thrash Critic↔Builder
	// regenerating for soft checklist leftovers — BUT not when delivery is empty
	// or active checklist is entirely (0/N) with Current Task still open.
	skipRebuildGreen := remaining > 0 && projectLooksSkipGreenSafe(wd)
	if skipRebuildGreen {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.rebuild_skip_green", "runtime", map[string]any{
			"remaining": remaining,
			"total":     total,
			"reason":    "compile/health/test green — skip Critic→Builder rebuild loop",
		}, nil)
	}
	for dispatch := 0; !skipRebuildGreen && dispatch < maxCriticDispatches && remaining > 0; dispatch++ {
		if e.criticBuilderCyclesExhausted() {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.cycle_cap_reached", "runtime", map[string]any{
				"cycles": e.criticBuilderCycles, "max": maxCriticBuilderCycles,
				"reason": "hard stop — Critic↔Builder loop cap",
			}, nil)
			break
		}
		if total == 0 || float64(remaining)/float64(total) < 0.2 {
			break
		}
		// If the last dispatch didn't reduce the gap count, stop — Builder isn't helping.
		if dispatch > 0 && remaining >= prevRemaining {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.dispatch_stalled", "runtime", map[string]any{
				"dispatch": dispatch + 1, "remaining": remaining,
				"reason": "Builder dispatch did not reduce gap count — stopping loop",
			}, nil)
			break
		}
		prevRemaining = remaining

		if !e.beginCriticBuilderCycle("rebuild") {
			break
		}

		// Prefer go build/test error feedback (was dead code in critic_feedback.go).
		gapItems := workflow.GetRemainingChecklistItems(wd)
		gapInstruction := buildGapInstructionWithErrors(wd, gapItems, resolveBuilderTargetLanguage(work.input, work.input))
		rebuildReason := fmt.Sprintf("%d/%d items incomplete — checklist gap fill", remaining, total)
		if strings.HasPrefix(gapInstruction, "DIAGNOSE AND FIX") {
			rebuildReason = fmt.Sprintf("%d/%d items incomplete — dispatching Builder with build/test errors", remaining, total)
		}

		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.rebuilding", "runtime", map[string]any{
			"dispatch": dispatch + 1, "remaining": remaining, "total": total,
			"gaps":   gapItems,
			"cycles": e.criticBuilderCycles,
			"reason": rebuildReason,
		}, nil)

		gapWork := work
		gapWork.input = gapInstruction
		_, buildErr := e.executeBuilderNodeWork(ctx, gapWork)
		if buildErr != nil {
			e.recordNodeRetryFailure("node-build")
			break
		}
		criticMarked += workflow.CriticAuditAndMark(wd)
		_, total, remaining = workflow.CountTodoProgressDetailed(wd)
		_ = total
		if remaining >= prevRemaining {
			e.recordNodeRetryFailure("node-build")
		}
	}

	// v4-P0: When Critic dispatch stalls, escalate with focused prompt.
	if !skipRebuildGreen && remaining > 0 && remaining >= prevRemaining && !e.criticBuilderCyclesExhausted() {
		if e.beginCriticBuilderCycle("escalate") {
			e.criticEscalateToPlanner(work, work.runID, work.taskID, remaining, total)
		}
	} else if e.criticBuilderCyclesExhausted() {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.cycle_cap_skip_escalate", "runtime", map[string]any{
			"cycles": e.criticBuilderCycles,
		}, nil)
	}

	criticSummary = fmt.Sprintf("Critic review: %d/%d items complete", total-remaining, total)
	if criticMarked > 0 {
		criticSummary += fmt.Sprintf(" (%d marked this review)", criticMarked)
	}
	// F62: when health is already red, do not let checklist counts sound like delivery success.
	if earlyHealth := crossLangHealthCheck(wd); earlyHealth != "" {
		criticSummary += " (build/health red — checklist counts are not a green delivery)"
	} else if e != nil && len(e.changedFiles) == 0 {
		// Batch6/R (S7) / L5: distinguish "this review wrote nothing" from empty delivery.
		if diskHasAppSources(".") {
			criticSummary += " (this review did not mutate files — checklist counts are from disk)"
		} else {
			criticSummary += " (this run wrote no source files — counts reflect disk checklist, not this run's work)"
		}
	}

	criticMessage := criticSummary
	if remaining > 0 {
		criticMessage += fmt.Sprintf("\n- %d items remain incomplete after %d dispatch(es)", remaining, maxCriticDispatches)
	}
	if len(findings) > 0 {
		for _, f := range findings {
			criticMessage += "\n- " + f
		}
	}

	// v4-P0: Implementation completeness check.
	if !e.criticBuilderCyclesExhausted() {
		e.criticCheckImplCompleteness(work, work.runID, work.taskID)
	}

	// v4-P0: Critic Hub — centralized decision hub.
	// R3-15: even when cycle cap is hit, still run stagedQualityGate so red
	// go test / hollow main cannot be ignored (previously skipped Decide entirely).
	var criticResult CriticHubResult
	if e.criticBuilderCyclesExhausted() {
		hub := NewCriticHub(e)
		if gate := hub.stagedQualityGate(wd, work); gate.Decision != "" {
			criticResult = CriticHubResult{
				Decision:     fmt.Sprintf("cycle cap reached (%d) — quality gate still failing", e.criticBuilderCycles),
				ErrorMessage: gate.ErrorMessage,
			}
		} else {
			criticResult = CriticHubResult{
				Decision:     fmt.Sprintf("cycle cap reached (%d) — skipping hub quality fix", e.criticBuilderCycles),
				ErrorMessage: fmt.Sprintf("critic: Critic↔Builder cycle cap (%d) reached with %d items remaining — stopping to avoid unbounded loop", maxCriticBuilderCycles, remaining),
			}
		}
	} else {
		criticResult = CriticHubDecide(e, ctx, work)
	}

	// C-1: Merge Researcher deep-dive findings into work context so Builder sees them.
	if strings.TrimSpace(criticResult.ResearcherSummary) != "" {
		if strings.TrimSpace(work.readSummary) != "" {
			work.readSummary += "\n[Critic-requested Researcher deep dive]\n" + criticResult.ResearcherSummary
		} else {
			work.readSummary = criticResult.ResearcherSummary
		}
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.researcher_merged", "runtime", map[string]any{
			"summary_chars": len(criticResult.ResearcherSummary),
		}, nil)
	}

	// C-3: If Critic asked the user, return a PausePoint to halt execution (S4.8).
	if criticResult.PausePoint != nil {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.user_input_requested", "runtime", map[string]any{
			"question": criticResult.PausePoint.StatusReason,
		}, nil)
		return workflowNodeWorkResult{pausePoint: *criticResult.PausePoint}, nil
	}

	// S4.9: Hub-enqueued remedial nodes are already in workflowState via AddNode;
	// emit so operators see them. Scheduler picks them up on subsequent ready-set runs.
	if len(criticResult.EnqueueNodes) > 0 {
		ids := make([]string, 0, len(criticResult.EnqueueNodes))
		for _, n := range criticResult.EnqueueNodes {
			ids = append(ids, n.ID)
		}
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.hub_enqueued", "runtime", map[string]any{
			"node_ids": ids,
			"decision": criticResult.Decision,
		}, nil)
	}

	// BLOCK-3 / F65: CriticHub hard failures must not skip Synthesizer.
	// Soft-fail: record the error, block advance, write a deterministic
	// delivery note, then continue so the user gets a human summary.
	if strings.TrimSpace(criticResult.ErrorMessage) != "" {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.hub_error", "runtime", map[string]any{
			"error":     criticResult.ErrorMessage,
			"soft_fail": true,
		}, nil)
		soft := e.softFailCriticDelivery(wd, criticResult.ErrorMessage)
		if soft.criticChallenge != "" {
			criticSummary += "\n" + soft.criticChallenge
		}
	}

	// C2: same decidePhaseAdvance as end-of-run defer; do not call CriticDecide twice.
	phaseDocsOK := workflow.PhaseDocsComplete(wd)
	healthErr := ""
	if phaseDocsOK {
		healthErr = crossLangHealthCheck(wd)
		if healthErr == "" {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.project_health_verified", "runtime", map[string]any{
				"kind":    "cross_lang_health",
				"verdict": "PASS",
			}, nil)
		}
	}
	lock := 0
	if e != nil {
		lock = e.lockedPhase
	}
	d := e.resolvePhaseAdvance(phaseAdvanceInput{
		ProjectRoot: wd,
		LockedPhase: lock,
		BuildOK:     healthErr == "",
		HealthErr:   healthErr,
		PhaseDocsOK: phaseDocsOK,
		RunBlocked:  e != nil && strings.TrimSpace(e.criticHardFailure) != "",
	})
	if d.Advanced && d.NextPhase > 0 {
		nextPhase := d.NextPhase
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.phase_advanced", "runtime", map[string]any{
			"from_phase": nextPhase - 1, "to_phase": nextPhase,
			"decided_by": "critic",
		}, nil)
		expandOK := true
		if exp, p, expErr := workflow.ExpandActivePhaseDetail(wd, e.makeWorkflowGenerate(ctx, workflowDocLLMTimeout(), work.runID, work.taskID, "expand_phase")); expErr == nil && exp {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.phase_detail_expanded", "runtime", map[string]any{
				"phase":  p,
				"reason": "expanded after phase advance",
			}, nil)
		} else if expErr != nil {
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.phase_detail_expand_failed", "runtime", map[string]any{
				"phase": nextPhase, "error": expErr.Error(),
			}, nil)
			if workflow.PhaseDocNeedsFullDetail(wd, nextPhase) {
				expandOK = false
				e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, "phase_docs_incomplete")
				_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.phase_advance_blocked", "runtime", map[string]any{
					"reason": "phase_docs_incomplete",
					"phase":  nextPhase,
					"error":  expErr.Error(),
				}, nil)
			}
		}
		_ = workflow.SyncTodoForPhase(wd, nextPhase)
		_ = workflow.SetTodoActivePhasePointer(wd, nextPhase)
		_ = workflow.AlignCurrentTaskToPhase(wd, nextPhase)
		if expandOK && lock == 0 && e.beginCriticBuilderCycle("phase_advance") {
			nextWork := work
			nextWork.input = fmt.Sprintf(
				"Active Phase %d (advanced this run). Implement Phase %d now. Follow docs/workflow/phase%d.md and avatars_todo.md. Write real source files. Do NOT edit plan/phase markdown.",
				nextPhase, nextPhase, nextPhase,
			)
			_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.phase_advance_builder", "runtime", map[string]any{
				"phase":  nextPhase,
				"reason": "full-course handoff after advance+expand",
			}, nil)
			if _, buildErr := e.executeBuilderNodeWork(ctx, nextWork); buildErr != nil {
				_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.phase_advance_builder_failed", "runtime", map[string]any{
					"phase": nextPhase, "error": buildErr.Error(),
				}, nil)
			} else {
				criticMarked += workflow.CriticAuditAndMark(wd)
				_, total, remaining = workflow.CountTodoProgressDetailed(wd)
				_ = total
			}
		}
		e.phaseAdvanceApplied = true
	} else if d.ProjectDone {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.project_completed", "runtime", map[string]any{
			"decided_by": "critic",
		}, nil)
		e.phaseAdvanceApplied = true
	} else if d.Blocked != "" {
		payload := map[string]any{"reason": d.Blocked, "phase_docs_ok": phaseDocsOK}
		if d.HealthErr != "" {
			payload["health_check_err"] = d.HealthErr
		}
		if d.Blocked == "user_phase_lock" {
			payload["locked_phase"] = lock
		}
		if d.Blocked == "checklist_incomplete" {
			payload["todo_done"] = d.TodoDone
			payload["todo_total"] = d.TodoTotal
			payload["health_ok"] = true
		}
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "workflow.phase_advance_blocked", "runtime", payload, nil)
	}

	status := "approval_boundary_reviewed"
	if err := e.emitAvatarMessage(work.runID, work.taskID, work.criticAvatarID, "reviewing", "challenge", criticMessage, criticSummary, "review", nil, nil); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:       work.runID,
		TaskID:      work.taskID,
		AvatarID:    work.criticAvatarID,
		NodeID:      work.nodeID,
		NodeRole:    work.nodeRole,
		NodeTitle:   work.nodeTitle,
		Tool:        "review",
		Operation:   "boundary_review",
		Status:      status,
		Summary:     fmt.Sprintf("Workflow node %s completed boundary review evidence.", work.nodeID),
		ArtifactIDs: workflowNodeArtifactIDs(WorkflowNodeRuntime{ID: work.nodeID}),
	}); err != nil {
		return workflowNodeWorkResult{}, err
	}

	// W19: Accumulate Critic insights for process_record learned field.
	// Runtime records facts deterministically; Critic provides the WHY.
	if len(findings) > 0 {
		e.criticInsights = append(e.criticInsights, findings...)
	}
	if remaining > 0 {
		e.criticInsights = append(e.criticInsights,
			fmt.Sprintf("%d/%d items remain incomplete after Critic review", remaining, total))
	} else {
		e.criticInsights = append(e.criticInsights,
			fmt.Sprintf("all %d items verified complete by Critic", total))
	}

	return workflowNodeWorkResult{criticChallenge: criticSummary}, nil
}

// criticPreFlightCheck performs a lightweight feasibility check before
// Builder starts. Returns risks as strings for transcript audit. P1-2a.
func criticPreFlightCheck(work workflowNodeWorkContext) []string {
	var risks []string
	task := strings.TrimSpace(work.input)
	planTitle := strings.TrimSpace(work.plan.Title)
	if task == "" {
		risks = append(risks, "Empty task description — Builder may produce unrelated output.")
	}
	// PY8: NL prompts use "Phase 1" / "阶段 1", not only "## Phase".
	if len(task) > 500 && !taskHasPhaseStructure(task) {
		risks = append(risks, fmt.Sprintf("Task is very broad (%d chars) without phase structure.", len(task)))
	}
	if planTitle == "" || planTitle == "Untitled task" {
		risks = append(risks, "Plan has no meaningful title — Planner may not have fully decomposed the task.")
	}
	// Destructive ops only — never bare substrings that appear in normal
	// coding tasks (NL smoke: "format" matched "formatter" and killed all
	// parallel Builders creating toc/formatter.py).
	for _, d := range []string{
		"rm -rf", "rm -fr", "delete all", "drop table", "drop database",
		"format disk", "format drive", "format c:", "diskpart",
		"删除所有", "清空数据库", "清空全部",
	} {
		if strings.Contains(strings.ToLower(task), d) {
			risks = append(risks, fmt.Sprintf("Task mentions potentially destructive operation: %q.", d))
			break
		}
	}
	if fileRe := regexp.MustCompile(`\b([\w./-]+\.(go|py|js|ts|rs|java|rb))\b`); fileRe != nil {
		for _, match := range fileRe.FindAllString(task, -1) {
			if _, err := os.Stat(match); os.IsNotExist(err) &&
				(strings.Contains(strings.ToLower(task), "edit") || strings.Contains(strings.ToLower(task), "modify") || strings.Contains(strings.ToLower(task), "修改")) {
				risks = append(risks, fmt.Sprintf("Task references non-existent file %q with edit/modify verb.", match))
				break
			}
		}
	}
	return risks
}

// taskHasPhaseStructure reports whether the task text already carves work into
// phases (Mid Python NL PY8 — "Phase 1" / "阶段2", not only "## Phase").
func taskHasPhaseStructure(task string) bool {
	if strings.Contains(task, "## Phase") || strings.Contains(task, "### Phase") {
		return true
	}
	if workflow.ParseRequestedPhase(task) > 0 {
		return true
	}
	phaseRe := regexp.MustCompile(`(?i)(?:phase|阶段)\s*[1-9]\d?`)
	return len(phaseRe.FindAllString(task, -1)) >= 2
}

// criticReviewCode checks the Builder's generated code for common issues
// and returns actionable findings. P15: Director dispatch — if Builder
// produced 0 files, Critic flags it and triggers Builder retry.
func criticReviewCode(work workflowNodeWorkContext) []string {
	var findings []string

	// P15: CRITICAL check — did Builder produce any output at all?
	builderArtifacts := workflowNodeArtifactIDs(WorkflowNodeRuntime{ID: "node-build"})
	codeArtifacts := 0
	for _, a := range builderArtifacts {
		if strings.HasPrefix(a, "builder-code-") || strings.HasPrefix(a, "builder-diff-") {
			codeArtifacts++
		}
	}
	if codeArtifacts == 0 {
		if diskHasAppSources(".") {
			findings = append(findings,
				"NOTE: No new Builder artifacts this review, but source already exists on disk — do not treat as empty delivery.")
		} else {
			findings = append(findings,
				"CRITICAL: Builder produced 0 code files. The implementation is empty. RE-DISPATCH Builder with the original task to generate actual code files.")
		}
	}

	// Check: do generated files use the correct package?
	if work.structuredContext != nil {
		for _, lc := range work.structuredContext.LayoutConventions {
			if strings.Contains(lc.FilePattern, "subdir") {
				findings = append(findings, fmt.Sprintf(
					"FilePattern warning: %s uses subdirectory pattern (%s) — ensure new files follow this convention exactly.",
					lc.Category, lc.FilePattern))
			}
		}
	}

	// Check: are known interfaces properly referenced?
	if work.structuredContext != nil && len(work.structuredContext.InterfaceDecls) > 0 {
		for _, iface := range work.structuredContext.InterfaceDecls {
			for _, m := range iface.Methods {
				if m.Name == "Apply" && !strings.Contains(m.Params, "context.Context") {
					findings = append(findings, fmt.Sprintf(
						"Interface %s.%s should accept context.Context for cancellation support.",
						iface.Name, m.Name))
				}
			}
		}
	}

	// Check: is the task too complex for a single run?
	if looksLikeCodeImplementationTask(work.input) && looksLikeModificationTask(work.input) {
		findings = append(findings,
			"Mixed task detected (create+modify). Consider splitting into separate runs: first generate new files, then edit existing ones.")
	}

	if len(findings) == 0 {
		findings = append(findings, "Code review passed: generated code follows project conventions.")
	}
	return findings
}

// projectLooksSkipGreenSafe is true only when there is real app source, health
// is green, and the active todo is not still entirely (0/N). Empty / stub-only
// disks must NOT short-circuit Critic↔Builder (G11).
func projectLooksSkipGreenSafe(wd string) bool {
	if deliveryLooksEmpty(wd) {
		return false
	}
	if todoProgressLooksStaleZero(wd) {
		return false
	}
	if crossLangHealthCheck(wd) != "" {
		return false
	}
	// Only consult go build when this workspace itself is a Go module —
	// never inherit a parent go.mod (false green under nested test dirs).
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
		if captureCompileErrors(wd) != "" {
			return false
		}
	}
	return true
}

// deliveryLooksEmpty reports stub-only / empty greenfield disks (G11).
// Language-agnostic: Go/JS/Rust/Python roots + root entrypoints all count.
func deliveryLooksEmpty(wd string) bool {
	return countSubstantiveAppSources(wd) == 0
}

// countSubstantiveAppSources counts non-trivial source files across common
// layouts. Bare __init__.py / .gitkeep / empty stubs alone do not count.
// F61: also counts top-level package dirs (base32x/, mylib/, …) — library-first
// layouts are not under app/internal/src.
func countSubstantiveAppSources(wd string) int {
	exts := map[string]bool{
		".py": true, ".go": true, ".ts": true, ".tsx": true,
		".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
		".rs": true, ".java": true, ".kt": true, ".cs": true,
	}
	stubNames := map[string]bool{
		"__init__.py": true, ".gitkeep": true, ".keep": true,
	}
	roots := []string{
		"app", "cmd", "src", "internal", "pkg", "lib",
		"server", "backend", "frontend", "crates",
	}
	seenRoot := map[string]bool{}
	for _, r := range roots {
		seenRoot[r] = true
	}
	// Top-level package dirs (Go library-first, Python packages, JS packages).
	if entries, err := os.ReadDir(wd); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			switch name {
			case "docs", "tmp", "testdata", "vendor", "node_modules", "target",
				"dist", "build", "coverage", "venv", ".venv", "__pycache__":
				continue
			}
			if !seenRoot[name] {
				roots = append(roots, name)
				seenRoot[name] = true
			}
		}
	}
	n := 0
	countFile := func(path string, info os.FileInfo) {
		if info == nil || info.IsDir() {
			return
		}
		name := strings.ToLower(info.Name())
		if stubNames[name] {
			return
		}
		if !exts[strings.ToLower(filepath.Ext(name))] {
			return
		}
		if info.Size() < 20 {
			return
		}
		n++
	}
	for _, root := range roots {
		base := filepath.Join(wd, root)
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", ".avatars", "venv", ".venv", "node_modules",
					"__pycache__", "vendor", "target", "dist", "build":
					return filepath.SkipDir
				}
				return nil
			}
			countFile(path, info)
			return nil
		})
	}
	// Root-level entrypoints (main.go, index.ts, lib.rs, …) also count.
	if entries, err := os.ReadDir(wd); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			countFile(filepath.Join(wd, e.Name()), info)
		}
	}
	// Manifests alone are NOT enough for "delivery" — only boost when we
	// already saw at least one real source file (avoids go.mod-only green).
	if n > 0 {
		return n
	}
	return 0
}

// diskHasAppSources reports whether the project already has real source under
// common layouts or at repo root (L5/G11: language-agnostic).
func diskHasAppSources(wd string) bool {
	return countSubstantiveAppSources(wd) > 0 || hasAnySourceFile(wd)
}

func hasAnySourceFile(wd string) bool {
	exts := map[string]bool{
		".py": true, ".go": true, ".ts": true, ".tsx": true,
		".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
		".rs": true, ".java": true, ".kt": true, ".cs": true,
	}
	roots := []string{"app", "cmd", "src", "internal", "pkg", "lib", "server", "backend", "frontend"}
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r] = true
	}
	if entries, err := os.ReadDir(wd); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || seen[name] {
				continue
			}
			switch name {
			case "docs", "tmp", "testdata", "vendor", "node_modules", "target", "dist", "build":
				continue
			}
			roots = append(roots, name)
		}
	}
	for _, root := range roots {
		base := filepath.Join(wd, root)
		found := false
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if exts[strings.ToLower(filepath.Ext(info.Name()))] {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if found {
			return true
		}
	}
	if entries, err := os.ReadDir(wd); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if exts[strings.ToLower(filepath.Ext(e.Name()))] {
				return true
			}
		}
	}
	return false
}

// todoProgressLooksStaleZero reports active checklist still all (0/N) while
// Current Task is unchecked — skip-green would hide unfinished SoT (V3).
func todoProgressLooksStaleZero(wd string) bool {
	todoPath := filepath.Join(wd, "docs", "workflow", "avatars_todo.md")
	data, err := os.ReadFile(todoPath)
	if err != nil {
		return false
	}
	s := string(data)
	hasUncheckedCurrent := strings.Contains(s, "## Current Task") &&
		(strings.Contains(s, "## Current Task\n- [ ]") || strings.Contains(s, "## Current Task\r\n- [ ]") ||
			regexp.MustCompile(`(?m)^## Current Task\s*\n- \[ \]`).MatchString(s))
	hasZeroCounts := regexp.MustCompile(`(?m)^- \[[ >x]\] .+\(0/\d+\)`).MatchString(s)
	hasDoneCounts := regexp.MustCompile(`(?m)^- \[[xX]\] .+\([1-9]\d*/\d+\)`).MatchString(s)
	return hasUncheckedCurrent && hasZeroCounts && !hasDoneCounts
}
