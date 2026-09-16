package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"avatars/internal/arch"
	"avatars/internal/events"
	"avatars/internal/evolution"
	"avatars/internal/llm"
	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/platform"
	"avatars/internal/prompt"
	"avatars/internal/runtime/health"
	"avatars/internal/skillbuilder"
	"avatars/internal/skills"
	"avatars/internal/tools"
	"avatars/internal/transcript"
	"avatars/internal/verification"
	"avatars/internal/workflow"
)

func (e *Engine) runOnce(ctx context.Context, input string, restored *transcript.RestoreSnapshot) (result RunResult, err error) {
	runID := fmt.Sprintf("%s-run-%d", e.sessionID, time.Now().UTC().UnixNano())
	taskID := runID + "-task-1"
	if e.taskID != "" {
		taskID = e.taskID
	}
	// Initialize node retry tracking per-run. Each node starts at 0 retries.
	if e.nodeRetryCount == nil {
		e.nodeRetryCount = make(map[string]int)
	}
	e.criticBuilderCycles = 0 // NL smoke #17: reset Critic↔Builder loop counter per run
	e.lastQualityFailFingerprint = ""
	e.qualityFailRepeatCount = 0
	e.confirmPlanDone = false
	e.lockedPhase = 0
	e.changedFiles = nil
	e.phaseAdvanceBlocked = ""
	e.phaseAdvanceResolved = false
	e.phaseAdvanceApplied = false
	e.phaseAdvanceOnce = phaseAdvanceDecision{}
	e.criticHardFailure = ""
	e.runStartedAt = time.Now()
	e.layoutTaskHint = input
	if e.health == nil {
		e.health = health.New()
	} else {
		e.health.Reset()
	}
	verification.SetCurrentHealth(e.health.Cache())
	defer verification.SetCurrentHealth(nil)
	resumeBoundary := ""
	if restored != nil {
		resumeBoundary = restored.BoundaryType
	}
	defer func() {
		if resumeBoundary != "" {
			result.ResumeBoundaryType = resumeBoundary
		}
	}()
	// R5-1/6: tool-loop write_file/edit_file must feed footer changedFiles.
	llm.SetFileMutationHandler(e.noteChangedFile)
	defer llm.ClearFileMutationHandler()
	// R7-2: remap greenfield tool writes into declared layout (cmd/server, internal/, migrations/).
	llm.SetPathRewriteHandler(func(path string) string {
		wd := e.projectRoot
		if wd == "" {
			wd, _ = os.Getwd()
		}
		return rewriteBuilderToolPath(wd, e.layoutTaskHint, path)
	})
	llm.SetPathContentRewriteHandler(func(path, content string) string {
		wd := e.projectRoot
		if wd == "" {
			wd, _ = os.Getwd()
		}
		return rewriteBuilderToolPathWithContent(wd, e.layoutTaskHint, path, content)
	})
	defer llm.ClearPathRewriteHandler()
	// J2-1: tool-loop writes share Builder checkFileHealth (JS/TS/Py/Rs/Go).
	llm.SetWriteHealthChecker(func(path, content string) error {
		if forbidsCLIScaffold(strings.ToLower(e.layoutTaskHint)) && isForbiddenCLIEntrypointPath(path) {
			return fmt.Errorf("CLI/entrypoint blocked by no-CLI constraint")
		}
		if reason := checkFileHealth(path, content); reason != "" {
			return fmt.Errorf("%s", reason)
		}
		return nil
	})
	defer llm.ClearWriteHealthChecker()
	// F63: coerce Go library-dir *_test.go package main before health/disk write.
	llm.SetWriteContentMutator(func(path, content string) string {
		if fixed, ok := coerceGoLibTestPackage(path, content); ok {
			return fixed
		}
		return content
	})
	defer llm.ClearWriteContentMutator()
	// P8-2: confine tool writes to project root (cross-lang jail).
	{
		wd := e.projectRoot
		if wd == "" {
			wd, _ = os.Getwd()
		}
		llm.SetToolWriteRoot(wd)
	}
	defer llm.ClearToolWriteRoot()
	llm.SetMCPToolHandler(e.handleLLMMCPTool)
	defer llm.ClearMCPToolHandler()
	e.discoverMCPTools(ctx, runID, taskID)
	restoredTaskMemoryID := e.taskID
	if restoredTaskMemoryID == "" && restored != nil {
		restoredTaskMemoryID = restored.TaskID
	}
	if restoredTaskMemoryID == "" {
		restoredTaskMemoryID = taskID
	}
	approvedSkills := 0
	generatedSkillPath := ""
	generatedSkillPreview := ""
	invokedSkillName := ""
	e.beginTelemetry("run")
	defer func() {
		if rec := recover(); rec != nil {
			if err == nil {
				err = fmt.Errorf("run panic: %v", rec)
			}
			if strings.TrimSpace(result.Summary) == "" {
				result.Summary = err.Error()
			}
			if result.SynthesisStatus == "" {
				result.SynthesisStatus = "failed: panic"
			}
		}
		status := runTerminalStatus(err)
		// P14-7a: Synthesizer strict status — if the error-based status is
		// "completed" but the verifier returned FAIL, override to needs_remediation.
		// F102: 402/quota is not a verifier FAIL — do not reopen remediation.
		if status == "completed" && !isSynthesisAvailabilityFailure(result) {
			if completionStatus, _ := e.runCompletionStatus(runID, taskID, result.Summary); completionStatus == "needs_remediation" {
				status = "needs_remediation"
			}
		}
		result.TranscriptPath = e.transcript.Path()
		if strings.TrimSpace(result.Summary) == "" {
			if err != nil {
				result.Summary = err.Error()
			}
		}
		result.GeneratedSkillPath = generatedSkillPath
		result.GeneratedSkillPreview = generatedSkillPreview
		result.InvokedSkillName = invokedSkillName
		projectRoot := e.projectRoot
		if projectRoot == "" {
			projectRoot = "."
		}
		if status == "awaiting_approval" {
			blockedSummary := strings.TrimSpace(result.Summary)
			if blockedSummary == "" {
				blockedSummary = err.Error()
			}
			lifecycle := NewRunLifecycle(runID, taskID)
			lifecycle.MarkAwaitingApproval(RunApprovalBoundary{
				BoundaryEventID: "",
				BoundaryType:    "run_terminal",
				TranscriptPath:  e.transcript.Path(),
				Status:          status,
				Summary:         blockedSummary,
			})
			footerPayload := e.mergeFooterIntoPayload(projectRoot, status, blockedSummary, &result)
			if emitErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", footerPayload, nil); emitErr != nil {
				err = errors.Join(err, emitErr)
			}
			if emitErr := e.emit(runID, taskID, "", "reviewing", "run.lifecycle_updated", "runtime", lifecycle.Payload(), nil); emitErr != nil {
				err = errors.Join(err, emitErr)
			}
			if persistErr := e.persistRunLifecycleEvaluation(runID, taskID, "", lifecycle); persistErr != nil {
				err = errors.Join(err, persistErr)
			}
			if emitErr := e.emitStableBoundary(runID, taskID, "", status, blockedSummary, "run_terminal", map[string]any{
				"approved_skill_count": approvedSkills,
			}); emitErr != nil {
				err = errors.Join(err, emitErr)
			}
		} else if status == "needs_remediation" {
			remediationSummary := strings.TrimSpace(result.Summary)
			if remediationSummary == "" && err != nil {
				remediationSummary = err.Error()
			}
			footerPayload := e.mergeFooterIntoPayload(projectRoot, status, remediationSummary, &result)
			if emitErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", footerPayload, nil); emitErr != nil {
				err = errors.Join(err, emitErr)
			}
			if emitErr := e.emitStableBoundary(runID, taskID, "", status, remediationSummary, "run_terminal", map[string]any{
				"approved_skill_count": approvedSkills,
			}); emitErr != nil {
				err = errors.Join(err, emitErr)
			}
		} else if status == "failed" || err != nil {
			// NL smoke #13: failed runs previously left no resumable boundary,
			// locking the task ("no resumable boundary found in transcript").
			failSummary := strings.TrimSpace(result.Summary)
			if failSummary == "" && err != nil {
				failSummary = err.Error()
			}
			if failSummary == "" {
				failSummary = "run failed"
			}
			if status == "" || status == "completed" {
				status = "failed"
			}
			footerPayload := e.mergeFooterIntoPayload(projectRoot, status, failSummary, &result)
			if emitErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", footerPayload, nil); emitErr != nil {
				err = errors.Join(err, emitErr)
			}
			if emitErr := e.emitStableBoundary(runID, taskID, "", status, failSummary, "run_terminal", map[string]any{
				"approved_skill_count": approvedSkills,
			}); emitErr != nil {
				err = errors.Join(err, emitErr)
			}
		}
		if telemetryErr := e.finalizeTelemetry(runID, taskID, status); telemetryErr != nil && err == nil {
			err = telemetryErr
			result = RunResult{}
			return
		}
		if flushErr := e.transcript.Flush(); flushErr != nil && err == nil {
			err = flushErr
			result = RunResult{}
		}
		// Record task completion and sync the todo checklist.
		changedFiles := e.changedFiles
		if changedFiles == nil {
			changedFiles = []string{}
		}
		// F15: ignore HTTP/go-test scrap at repo root when marking/advancing.
		deliveryFiles := filterDeliveryChangedFiles(changedFiles)
		// W13: Auto-extract lessons from the task result.
		learned := extractLessonsLearned(result.Summary, err, deliveryFiles, result.CriticInsights)
		workflow.RecordTaskCompletion(projectRoot, taskID, result.Summary, deliveryFiles, learned)

		// S2.2: Run build/test once and pass into WorkflowSyncCtx (was dead wiring).
		// Prefer reusing verifier evidence when the run already proved PASS/FAIL.
		buildOK, testOK := resolveWorkflowBuildTestOK(projectRoot, result, deliveryFiles)
		// F11: construct/confirm quality failures are not compile failures — empty
		// greenfield health must not paint build_failed over a planning abort.
		planningAbort := isPlanningQualityAbort(result.SynthesisStatus)
		// F14: needs_remediation / hub exhaustion must not still advance phases.
		remediationHold := runBlocksPhaseAdvance(status, result, err)
		if planningAbort {
			if result.PhaseAdvanceBlocked == "" {
				if strings.HasPrefix(strings.ToLower(result.SynthesisStatus), "failed: construct_plan") {
					result.PhaseAdvanceBlocked = "construct_failed"
				} else {
					result.PhaseAdvanceBlocked = "confirm_failed"
				}
			}
			e.phaseAdvanceBlocked = result.PhaseAdvanceBlocked
			result.BuildOKKnown = false
			buildOK = true // skip advance/build_failed branches below
		}
		syncWorkflowAfterBuild(WorkflowSyncCtx{
			ProjectRoot:  projectRoot,
			ChangedFiles: deliveryFiles,
			BuildOK:      buildOK,
			TestOK:       testOK,
			LLM:          e.llm,
			Ctx:          ctx,
		})
		// NL smoke Batch1/A+P3: skip keyword auto-mark when Direct wrote files
		// but build failed (false todo/criteria). Criteria always need buildOK
		// for verification-style lines (see TryAutoMarkPlanCriteria).
		directBuildFailed := strings.Contains(strings.ToLower(result.Summary), "build_ok=false")
		// G3/P4: require BuildOK — string alone is not enough when summary omits the flag.
		// F14: never auto-green checklist while the run itself needs remediation.
		if buildOK && !directBuildFailed && !planningAbort && !remediationHold {
			if autoMarked := workflow.TryAutoMarkDone(projectRoot, result.Summary, deliveryFiles, input); autoMarked > 0 {
				_ = e.emit(runID, taskID, "", "reviewing", "workflow.tasks_marked", "runtime", map[string]any{"count": autoMarked}, nil)
			}
		}
		if !planningAbort && !remediationHold {
			if criteriaMarked := workflow.TryAutoMarkPlanCriteriaWithTests(projectRoot, result.Summary, deliveryFiles, buildOK, testOK, input); criteriaMarked > 0 {
				_ = e.emit(runID, taskID, "", "reviewing", "workflow.criteria_marked", "runtime", map[string]any{"count": criteriaMarked}, nil)
			}
		}
		// W14: Sync [x] marks from todo back to the phase source file.
		if phaseMarked := workflow.SyncMarksToPhase(projectRoot); phaseMarked > 0 {
			_ = e.emit(runID, taskID, "", "reviewing", "workflow.phase_marks_synced", "runtime", map[string]any{"count": phaseMarked}, nil)
		}
		// F143: phase Scope & Success Criteria [x] must copy onto plan, even
		// when todo→phase task sync and keyword auto-mark both no-op.
		if planSynced := workflow.SyncPhaseCriteriaMarksToPlan(projectRoot); planSynced > 0 {
			_ = e.emit(runID, taskID, "", "reviewing", "workflow.plan_criteria_synced", "runtime", map[string]any{"count": planSynced}, nil)
		}
		// C2: one CriticDecide. If Critic already applied, defer only copies/upgrades.
		phaseDocsOK := workflow.PhaseDocsComplete(projectRoot)
		lock := e.lockedPhase
		d := e.resolvePhaseAdvance(phaseAdvanceInput{
			ProjectRoot:  projectRoot,
			LockedPhase:  lock,
			BuildOK:      buildOK && !directBuildFailed,
			PhaseDocsOK:  phaseDocsOK,
			PlanningGate: planningAbort || isPlanningGateFailure(result),
			RunBlocked:   remediationHold || strings.TrimSpace(e.criticHardFailure) != "",
		})
		quotaGreen := isSynthesisAvailabilityFailure(result) && buildOK && !directBuildFailed
		if (planningAbort || remediationHold) && !(quotaGreen && e.phaseAdvanceApplied) {
			if result.PhaseAdvanceBlocked == "" {
				result.PhaseAdvanceBlocked = d.Blocked
			}
			e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, result.PhaseAdvanceBlocked)
			_ = e.emit(runID, taskID, "", "reviewing", "workflow.phase_advance_blocked", "runtime", map[string]any{
				"reason": result.PhaseAdvanceBlocked,
			}, nil)
		} else if quotaGreen && e.phaseAdvanceApplied {
			// Critic already applied complete; synthesis 402 must not paint blocked.
			if result.PhaseAdvanceBlocked == "needs_remediation" {
				result.PhaseAdvanceBlocked = ""
			}
			if e.phaseAdvanceBlocked == "needs_remediation" {
				e.phaseAdvanceBlocked = ""
			}
		} else {
			result.PhaseAdvanceBlocked = mergePhaseBlock(result.PhaseAdvanceBlocked, d.Blocked)
			if !e.phaseAdvanceApplied {
				if d.Advanced && d.NextPhase > 0 {
					completedPhase := d.NextPhase - 1
					_ = e.emit(runID, taskID, "", "reviewing", "workflow.phase_advanced", "runtime", map[string]any{"from_phase": completedPhase, "to_phase": d.NextPhase, "decided_by": "critic"}, nil)
					RecordPhaseCompletion(projectRoot, completedPhase, buildOK, testOK, len(deliveryFiles))
					if exp, p, expErr := workflow.ExpandActivePhaseDetail(projectRoot, e.makeWorkflowGenerate(ctx, workflowDocLLMTimeout(), runID, taskID, "expand_phase")); expErr == nil && exp {
						_ = e.emit(runID, taskID, "", "reviewing", "workflow.phase_detail_expanded", "runtime", map[string]any{
							"phase": p, "reason": "expanded after end-of-run phase advance",
						}, nil)
						if syncErr := workflow.SyncTodoForPhase(projectRoot, d.NextPhase); syncErr != nil {
							_ = e.emit(runID, taskID, "", "reviewing", "workflow.sync_failed", "runtime", map[string]any{
								"op": "SyncTodoForPhase", "error": syncErr.Error(), "phase": d.NextPhase,
							}, nil)
						}
						_ = workflow.SetTodoActivePhasePointer(projectRoot, d.NextPhase)
						_ = workflow.AlignCurrentTaskToPhase(projectRoot, d.NextPhase)
					} else if expErr != nil {
						_ = e.emit(runID, taskID, "", "reviewing", "workflow.phase_detail_expand_failed", "runtime", map[string]any{
							"phase": d.NextPhase, "error": expErr.Error(),
						}, nil)
						if workflow.PhaseDocNeedsFullDetail(projectRoot, d.NextPhase) {
							e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, "phase_docs_incomplete")
							result.PhaseAdvanceBlocked = mergePhaseBlock(result.PhaseAdvanceBlocked, "phase_docs_incomplete")
							_ = e.emit(runID, taskID, "", "reviewing", "workflow.phase_advance_blocked", "runtime", map[string]any{
								"reason": "phase_docs_incomplete",
								"phase":  d.NextPhase,
								"error":  expErr.Error(),
							}, nil)
						}
					}
					e.phaseAdvanceApplied = true
				} else if d.ProjectDone {
					_ = e.emit(runID, taskID, "", "reviewing", "workflow.project_completed", "runtime", nil, nil)
					e.phaseAdvanceApplied = true
				} else if d.Blocked != "" {
					payload := map[string]any{"reason": d.Blocked}
					if d.Blocked == "user_phase_lock" {
						payload["locked_phase"] = lock
					}
					if d.Blocked == "checklist_incomplete" {
						payload["todo_done"] = d.TodoDone
						payload["todo_total"] = d.TodoTotal
						payload["health_ok"] = true
					}
					if d.HealthErr != "" {
						payload["health_check_err"] = d.HealthErr
					}
					_ = e.emit(runID, taskID, "", "reviewing", "workflow.phase_advance_blocked", "runtime", payload, nil)
				}
			}
		}
		result.PhaseAdvanceBlocked = mergePhaseBlock(result.PhaseAdvanceBlocked, e.phaseAdvanceBlocked)
		if planningAbort {
			result.BuildOKKnown = false
		} else {
			result.BuildOK = buildOK
			result.BuildOKKnown = true
		}
		// S2.6: Regenerate process_record.md from SQLite (export only).
		_ = workflow.RegenerateRecordMarkdownFromSQLite(projectRoot)

		// Authoritative end-of-run footer (after phase sync/advance).
		footerStatus := status
		if footerStatus == "" {
			footerStatus = "completed"
		}
		if planningAbort {
			footerStatus = "failed"
		}
		// D3: advance-blocked runs must not present as plain completed in the footer.
		// F102: synthesis 402 on green disk is completed, not unverified/blocked.
		if result.PhaseAdvanceBlocked != "" && (footerStatus == "" || footerStatus == "completed") {
			if !(isSynthesisAvailabilityFailure(result) && result.BuildOK) {
				footerStatus = "completed_unverified"
			}
		}
		footer := e.buildRunFooter(projectRoot, NextStepInput{
			Status:              footerStatus,
			Summary:             result.Summary,
			BuildOK:             result.BuildOK,
			BuildOKKnown:        result.BuildOKKnown,
			PhaseAdvanceBlocked: result.PhaseAdvanceBlocked,
			ClarifyPending:      result.ClarifyPending != nil,
		})
		result.Footer = footer
		if strings.TrimSpace(result.ReportContent) != "" {
			result.ReportContent = ReplaceRunFooterMarkdown(result.ReportContent, footer)
		}
		_ = e.emit(runID, taskID, "", "reviewing", "run.change_summary", "runtime", footer.EventPayload(), nil)
	}()

	restoredSummary := ""
	restoredTaskSummary := ""
	var restoredRecentEvents []string
	var restoredWarmLessons []string
	var restoredProjectLessons []string
	restoredVerificationContext := ""
	var planContext planner.Context
	if e != nil && e.memory != nil && restoredTaskMemoryID != "" {
		loaded, err := e.memory.LoadLatestTaskSnapshot(restoredTaskMemoryID)
		if err != nil {
			return RunResult{}, err
		}
		restoredVerificationContext = memstore.PromptVerificationContext(restoredTaskMemoryID, loaded)
		planContext.Remediation = plannerRemediationEnvelope(restoredTaskMemoryID, loaded)
		if restored != nil {
			snapshot := memstore.Snapshot{
				StableSummary:       restored.Summary,
				TaskSummary:         restored.TaskSummary,
				AvatarSummaries:     restored.AvatarSummaries,
				RecentEvents:        restored.RecentEvents,
				Verification:        loaded.Verification,
				VerificationHistory: loaded.VerificationHistory,
				EvaluationRecords:   loaded.EvaluationRecords,
				WarmLessons:         loaded.WarmLessons,
				EvolutionCandidates: loaded.EvolutionCandidates,
			}
			if e.projectMemory != nil {
				projectLessons, err := e.projectMemory.LoadProjectLessons()
				if err != nil {
					return RunResult{}, err
				}
				snapshot.ProjectLessons = projectLessons
			}
			plannerView := snapshot.View(memstore.QueryPolicy{Reader: memstore.ReaderPlanner})
			restoredSummary = plannerView.StableSummary
			restoredTaskSummary = plannerView.TaskSummary
			restoredRecentEvents = plannerView.RecentEvents
			restoredWarmLessons = warmLessonSummaries(plannerView.WarmLessons)
			restoredProjectLessons = projectLessonSummaries(plannerView.ProjectLessons)
		}
	} else if restored != nil {
		snapshot := memstore.Snapshot{
			StableSummary:   restored.Summary,
			TaskSummary:     restored.TaskSummary,
			AvatarSummaries: restored.AvatarSummaries,
			RecentEvents:    restored.RecentEvents,
		}
		if e != nil && e.projectMemory != nil {
			projectLessons, err := e.projectMemory.LoadProjectLessons()
			if err != nil {
				return RunResult{}, err
			}
			snapshot.ProjectLessons = projectLessons
		}
		plannerView := snapshot.View(memstore.QueryPolicy{Reader: memstore.ReaderPlanner})
		restoredSummary = plannerView.StableSummary
		restoredTaskSummary = plannerView.TaskSummary
		restoredRecentEvents = plannerView.RecentEvents
		restoredWarmLessons = warmLessonSummaries(plannerView.WarmLessons)
		restoredProjectLessons = projectLessonSummaries(plannerView.ProjectLessons)
	}
	bundle := prompt.BuildWithResume(input, restoredSummary, restoredTaskSummary, restoredRecentEvents, restoredWarmLessons, restoredProjectLessons, restoredVerificationContext)
	if e.attachedFilePath != "" {
		bundle = bundle.WithAttachedFile(prompt.AttachedFile{Path: e.attachedFilePath, Content: e.attachedFileBytes})
	}
	if e.replContext != "" {
		bundle = bundle.WithREPLContext(e.replContext)
	}
	// W1: Hard-load project workflow docs into LLM context.
	// This ensures every avatar sees the project's current phase, goals,
	// pitfalls, and progress — required by the avatars workflow spec.
	if wfCtx := workflow.LoadWorkflowContext("."); wfCtx != "" {
		bundle = bundle.WithWorkflowContext(wfCtx)
	}
	// I29: Ensure architecture.md exists before planning.
	HookArchitectureBeforePlan(input)
	// Direction layer first: bootstrap / Phase Count / deferred stubs BEFORE
	// ConstructPlan+ConfirmPlan so Confirm only expands Active Phase (R1).
	if cwd, err := os.Getwd(); err == nil {
		planPath := filepath.Join(cwd, "docs", "workflow", "avatars_plan.md")
		if _, statErr := os.Stat(planPath); os.IsNotExist(statErr) {
			_ = workflow.BootstrapWorkflowFromTask(cwd, input)
		}
		if n, enfErr := workflow.EnforcePlanPhaseCountFromTask(cwd, input); enfErr == nil && n >= 2 {
			_ = e.emit(runID, taskID, "", "planning", "workflow.phase_count_enforced", "runtime", map[string]any{
				"phase_count": n,
				"reason":      "explicit Phase N markers in task exceeded on-disk Phase Count",
			}, nil)
		}
		if created, skErr := workflow.EnsureWorkflowPhaseDocsFromPlan(cwd); skErr == nil && created > 0 {
			_ = e.emit(runID, taskID, "", "planning", "workflow.phase_docs_skeletoned", "runtime", map[string]any{
				"created": created,
			}, nil)
		}
	}
	// I30: Sync todo checklist from plan at the start of every run.
	HookSyncTodoAtStart()
	// Batch3/P9: align plan/phase DB stack with user input + manifests.
	if stackAlign := HookAlignStackAtStart(input); stackAlign != "" {
		_ = e.emit(runID, taskID, "", "planning", "workflow.stack_aligned", "runtime", map[string]any{
			"summary": stackAlign,
		}, nil)
	}
	// NL smoke Batch1/G: refresh Phase Docs process tag; emit if incomplete.
	if missingPhases := HookCheckPhaseDocsAtStart(); len(missingPhases) > 0 {
		_ = e.emit(runID, taskID, "", "planning", "workflow.phase_docs_incomplete", "runtime", map[string]any{
			"missing": missingPhases,
		}, nil)
	}

	wfTimeout := workflowDocLLMTimeout()
	wfGenerate := e.makeWorkflowGenerate(ctx, wfTimeout, runID, taskID, "workflow")

	// W1b: Auto-populate the plan if it's empty (fresh project).
	// F98: also rebuild when a filled plan is for a different product than
	// this live task (dirty workspace leftover --help / other library).
	planNeedsConstruct := !workflow.IsAttachmentSummarizePlaceholder(input) &&
		(workflow.IsPlanEmpty(".") || workflow.PlanMismatchesLiveTask(".", input))
	if planNeedsConstruct && e.llm != nil {
		// Do not truncate: phase lists often appear late in long Chinese prompts.
		// Truncating to ~200 chars caused Phase Count collapse (mid NL M2).
		req := strings.TrimSpace(input)
		reason := "empty plan template — auto-fill before Builder"
		if !workflow.IsPlanEmpty(".") {
			reason = "on-disk plan mismatches live task — rebuild SoT before Builder"
		}
		_ = e.emit(runID, taskID, "", "planning", "construct_plan.started",
			"runtime", map[string]any{
				"timeout_ms": wfTimeout.Milliseconds(),
				"think_mode": "off",
				"reason":     reason,
			}, nil)
		planContent, constructErr := workflow.ConstructPlan(".", req,
			e.makeWorkflowGenerate(ctx, wfTimeout, runID, taskID, "construct_plan"))
		if constructErr != nil || strings.TrimSpace(planContent) == "" {
			errMsg := "empty plan content"
			if constructErr != nil {
				errMsg = constructErr.Error()
			}
			_ = e.emit(runID, taskID, "", "planning", "construct_plan.failed",
				"runtime", map[string]any{"error": errMsg}, nil)
			// F2: never proceed to multi-avatar Confirm with a template plan.
			return RunResult{
				Summary: "Plan construction failed: " + errMsg +
					". docs/workflow/avatars_plan.md is still a template. " +
					"Re-run (or fill the plan manually), then confirm — do not advance phases yet.",
				SynthesisStatus:     "failed: construct_plan",
				PhaseAdvanceBlocked: "construct_failed",
				BuildOKKnown:        false,
			}, nil
		}
		_ = e.emit(runID, taskID, "", "planning", "construct_plan.completed",
			"runtime", map[string]any{"bytes": len(planContent)}, nil)
		// Re-stub after ConstructPlan rewrote Phase Count / sections.
		if cwd, err := os.Getwd(); err == nil {
			_, _ = workflow.EnsureWorkflowPhaseDocsFromPlan(cwd)
		}
		// S2.8: Respect workflow.auto_confirm. When false, leave plan as
		// draft for the user to confirm ("confirm the plan").
		if e.workflowAutoConfirm {
			_ = e.emit(runID, taskID, "", "planning", "plan.auto_confirmed",
				"runtime", map[string]any{"reason": "first run, empty plan auto-filled"}, nil)
			// H6: same observability as executeConfirmPlan (confirm_plan.*).
			_ = e.emit(runID, taskID, "", "planning", "confirm_plan.started",
				"runtime", map[string]any{"reason": "workflow_auto_confirm", "input": input}, nil)
			if summary, cErr := workflow.ConfirmPlan(".", wfGenerate); cErr != nil {
				_ = e.emit(runID, taskID, "", "planning", "confirm_plan.failed",
					"runtime", map[string]any{"error": cErr.Error()}, nil)
				return RunResult{
					Summary:             "Confirm plan failed after construct: " + cErr.Error(),
					SynthesisStatus:     "failed: confirm_plan",
					PhaseAdvanceBlocked: "confirm_failed",
					BuildOKKnown:        false,
				}, nil
			} else {
				_ = e.emit(runID, taskID, "", "planning", "confirm_plan.completed",
					"runtime", map[string]any{"summary": summary}, nil)
				e.confirmPlanDone = true
			}
		} else {
			_ = e.emit(runID, taskID, "", "planning", "plan.draft_awaiting_confirm",
				"runtime", map[string]any{"hint": "say: confirm the plan"}, nil)
			// PY6: draft must not silently proceed to Builder. acceptEdits /
			// explicit implement → ConfirmPlan (phase docs + todo). Otherwise pause.
			shouldConfirm := e.permissionMode == PermissionModeAcceptEdits ||
				workflow.WantsImplementAfterConfirm(input) ||
				planner.LooksLikeImplementOrResume(input) ||
				workflow.IsConfirmPlan(input)
			if shouldConfirm {
				_ = e.emit(runID, taskID, "", "planning", "plan.auto_confirmed",
					"runtime", map[string]any{
						"reason": "acceptEdits_or_implement after draft — run ConfirmPlan before Builder",
					}, nil)
				_ = e.emit(runID, taskID, "", "planning", "confirm_plan.started",
					"runtime", map[string]any{"reason": "acceptEdits_or_implement", "input": input}, nil)
				if summary, cErr := workflow.ConfirmPlan(".", wfGenerate); cErr != nil {
					_ = e.emit(runID, taskID, "", "planning", "plan.confirm_failed",
						"runtime", map[string]any{"error": cErr.Error()}, nil)
					_ = e.emit(runID, taskID, "", "planning", "confirm_plan.failed",
						"runtime", map[string]any{"error": cErr.Error()}, nil)
					return RunResult{
						Summary:             "Confirm plan failed after construct: " + cErr.Error(),
						SynthesisStatus:     "failed: confirm_plan",
						PhaseAdvanceBlocked: "confirm_failed",
						BuildOKKnown:        false,
					}, nil
				} else {
					_ = e.emit(runID, taskID, "", "planning", "confirm_plan.completed",
						"runtime", map[string]any{"summary": summary}, nil)
					e.confirmPlanDone = true
				}
			} else {
				_ = e.emit(runID, taskID, "", "planning", "plan.blocked_awaiting_confirm",
					"runtime", map[string]any{
						"message": "Plan is draft. Say \"confirm the plan\" before implementation.",
					}, nil)
				return RunResult{
					Summary: "Plan drafted at docs/workflow/avatars_plan.md (Status: draft). " +
						"Say \"confirm the plan\" when ready, or re-run with --permission-mode acceptEdits to confirm and implement.",
				}, nil
			}
		}
	}
	// R1: if Active Phase is still a deferred stub (e.g. advanced last run), expand now.
	if expanded, phaseN := HookExpandActivePhaseAtStart(wfGenerate); expanded {
		_ = e.emit(runID, taskID, "", "planning", "workflow.phase_detail_expanded", "runtime", map[string]any{
			"phase":  phaseN,
			"reason": "Active Phase was deferred stub — expanded before Builder",
		}, nil)
	}
	// W10: Bypass complexity for simple workflow actions (mark/confirm/replan).
	// NL smoke Batch1/A: confirm+implement must NOT force Direct-only trivial —
	// that left builderAvatarID empty → "avatar id is required".
	isWorkflowAction := workflow.IsMarkTaskDoneCheck(input) || workflow.IsConfirmPlan(input) || workflow.IsReplan(input) || workflow.IsPhaseConstruction(input) || workflow.IsPlanConstruction(input)
	var assessment planner.ComplexityAssessment
	if isWorkflowAction && workflow.WantsImplementAfterConfirm(input) {
		// G6: multi-phase / full-course confirm+implement must not stay "small".
		level := planner.ComplexitySmall
		reason := "confirm+implement — Builder required after ConfirmPlan"
		avatars := []string{"Planner", "Builder", "Critic", "Synthesizer"}
		groups := []int{2, 1, 1}
		if workflow.WantsFullCourse(input) || workflow.EstimatePhaseCountForTask(input) >= 2 {
			level = planner.ComplexityMedium
			reason = "confirm+implement multi-phase — medium+Builder/Critic"
			avatars = []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"}
			groups = []int{2, 2}
		}
		assessment = planner.ComplexityAssessment{
			Level:            level,
			Reason:           reason,
			SuggestedAvatars: avatars,
			ParallelGroups:   groups,
		}
	} else if isWorkflowAction {
		assessment = planner.ComplexityAssessment{Level: "trivial", Reason: "workflow action"}
	} else {
		assessment = planner.Assess(input)
	}
	// NL smoke Batch1/A: resume or in-progress plan must not stay on Direct trivial.
	if assessment.IsTrivial() && (restored != nil || workflowPlanInProgress(".")) {
		if restored != nil || planner.LooksLikeImplementOrResume(input) {
			assessment = planner.ComplexityAssessment{
				Level:            planner.ComplexityMedium,
				Reason:           "resume/in-progress workflow — bump trivial to medium+Builder",
				SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
				ParallelGroups:   []int{2, 2},
			}
		}
	}
	if planner.LooksLikeProgressReconcileTask(input) {
		assessment = planner.ComplexityAssessment{
			Level:            planner.ComplexitySmall,
			Reason:           "checklist/progress reconcile — skip implement DAG",
			SuggestedAvatars: []string{"Planner", "Synthesizer"},
			ParallelGroups:   []int{1},
		}
	}
	// P9: Inject recent rejection feedback into plan context.
	// This prevents the LLM from repeating patterns that the user rejected.
	if wd, wdErr := os.Getwd(); wdErr == nil {
		feedbackLog := NewFeedbackLog(wd)
		if recentFB := feedbackLog.RecentFeedback(5); len(recentFB) > 0 {
			planContext.Feedback = FormatFeedbackForPlanner(recentFB)
			_ = e.emit(runID, taskID, "", "planning", "feedback.injected", "runtime", map[string]any{
				"feedback_count": len(recentFB),
			}, nil)
		}
		// P10: Inject session context (extracted memories + compacted history).
		sessionMgr := NewSessionManager(wd)
		extractor := NewMemoryExtractor(wd)
		if sessionCtx := FormatSessionContext(sessionMgr, extractor); sessionCtx != "" {
			planContext.Feedback += sessionCtx
		}
	}
	// P7: Check for ambiguous input before committing to a plan.
	// R5-3: multi-phase / full-course / in-progress plan must not force clarify.
	if !shouldSkipClarifyGate(input) {
		if questions := detectAmbiguity(input); len(questions) > 0 {
			session := &ClarifySession{
				RunID:         runID,
				TaskID:        taskID,
				Questions:     questions,
				Status:        ClarifyStatusPending,
				CreatedAt:     time.Now().UTC(),
				OriginalInput: input,
			}
			_ = e.emit(runID, taskID, "", "planning", "run.needs_clarification", "runtime", map[string]any{
				"question_count": len(questions),
			}, nil)
			return RunResult{ClarifyPending: session}, nil
		}
	}
	// Inject architecture Registration Points so the Planner knows this task
	// can be done by editing existing files rather than doing read-only analysis.
	if planContext.ArchSummary == "" {
		if cwd, err := os.Getwd(); err == nil {
			planContext.ArchSummary = arch.PlannerInjection(cwd)
		}
	}
	// Bootstrap / Phase Count / stubs already applied earlier (before ConfirmPlan).
	// P3-2: Always use BuildForComplexity to enable parallel survey
	// plans for Medium/Large code-implementation tasks.
	var plan planner.Plan = planner.BuildForComplexity(input, planContext, assessment)
	readTargets := selectResearcherReadTargets(input)
	hasReadTarget := len(readTargets) > 0
	explorationRounds := buildExplorationRounds(input, readTargets)
	plannerAvatarID := plan.AvatarIDByRole("Planner")
	researcherAvatarID := plan.AvatarIDByRole("Researcher")
	builderAvatarID := plan.AvatarIDByRole("Builder")
	criticAvatarID := plan.AvatarIDByRole("Critic")
	var activeSkill *skills.Definition
	alwaysOnSkills := []skills.Definition{}
	readSummary := "No bootstrap context file was found."
	researcherReportArtifacts := []ArtifactRef{}
	reportFilePath := resolveAnalysisReportPath(input)
	analysisReportText := ""
	synthesisStatus := "not_requested"

	runStartedPayload := map[string]any{"input": input}
	if e.taskID != "" {
		runStartedPayload["task_workspace_id"] = e.taskID
		if e.taskRoot != "" {
			runStartedPayload["task_workspace_root"] = e.taskRoot
		}
	}
	if restored != nil {
		runStartedPayload["mode"] = "resume_continuation"
		runStartedPayload["source_transcript"] = restored.SourceTranscript
	}
	if err := e.emit(runID, taskID, "", "planning", "run.started", "runtime", runStartedPayload, nil); err != nil {
		return RunResult{}, err
	}
	if err := e.recordLLMStatus(runID, taskID); err != nil {
		return RunResult{}, err
	}
	if restored != nil {
		if err := e.emit(runID, taskID, plannerAvatarID, "planning", "memory.resume_restored", "memory", map[string]any{
			"source_transcript":    restored.SourceTranscript,
			"restored_run_id":      restored.RunID,
			"restored_task_id":     restored.TaskID,
			"boundary_type":        restored.BoundaryType,
			"boundary_event_id":    restored.BoundaryEventID,
			"status":               restored.Status,
			"summary":              restored.Summary,
			"task_summary":         restored.TaskSummary,
			"avatar_summary_count": len(restored.AvatarSummaries),
			"recent_event_count":   len(restored.RecentEvents),
			"event_count":          restored.EventCount,
		}, nil); err != nil {
			return RunResult{}, err
		}
	}
	if err := e.emit(runID, taskID, "", "planning", "task.created", "runtime", map[string]any{"title": plan.Title}, nil); err != nil {
		return RunResult{}, err
	}
	taskDecomposedPayload := map[string]any{"summary": plan.Summary, "avatar_count": len(plan.Avatars), "workflow_node_count": len(plan.Nodes)}
	if remediation := plannerRemediationPayload(plan.Remediation); len(remediation) > 0 {
		taskDecomposedPayload["remediation"] = remediation
	}
	if err := e.emit(runID, taskID, plannerAvatarID, "planning", "task.decomposed", "runtime", taskDecomposedPayload, nil); err != nil {
		return RunResult{}, err
	}
	if err := e.emitTaskSummary(runID, taskID, plannerAvatarID, plan.Summary); err != nil {
		return RunResult{}, err
	}

	avatarByID := make(map[string]planner.Avatar, len(plan.Avatars))
	for index, avatar := range plan.Avatars {
		avatarByID[avatar.ID] = avatar
		if err := e.emit(runID, taskID, avatar.ID, "planning", "avatar.spawned", "runtime", map[string]any{"role": avatar.Role, "status": "ready", "responsibility": avatar.Responsibility}, map[string]any{"slot": index}); err != nil {
			return RunResult{}, err
		}
		if err := e.emitAvatarSummary(runID, taskID, avatar.ID, avatar.Role, avatarSummaryText(avatar.Role, avatar.Responsibility, "")); err != nil {
			return RunResult{}, err
		}
	}

	nodeByID := make(map[string]planner.WorkflowNode, len(plan.Nodes))
	for _, node := range plan.Nodes {
		nodeByID[node.ID] = node
	}
	workflowState := NewWorkflowState(plan)

	planMessage := fmt.Sprintf(
		"Planner prepared %d stable prompt sections, %d dynamic sections, %d avatars and %d workflow nodes. LLM provider: %s. Web search capability declared: %t.",
		len(bundle.StablePrefix),
		len(bundle.DynamicSections),
		len(plan.Avatars),
		len(plan.Nodes),
		e.llm.Provider(),
		e.llm.Supports(llm.CapabilityWebSearch),
	)
	if restored != nil {
		planMessage += fmt.Sprintf(" Restored stable summary from %s before planning.", restored.SourceTranscript)
		if restored.BoundaryType == "soft_resume_fallback" {
			planMessage += " SOFT RESUME: no hard compaction/lifecycle boundary — continuing as a fresh workflow with prior transcript context (not a mid-node hard resume)."
		} else if restored.BoundaryType != "" {
			planMessage += fmt.Sprintf(" Hard resume boundary: %s.", restored.BoundaryType)
		}
		if restored.TaskSummary != "" {
			planMessage += " Restored task summary is available for this continuation."
		}
		if len(restored.AvatarSummaries) > 0 {
			planMessage += fmt.Sprintf(" Restored %d avatar summaries.", len(restored.AvatarSummaries))
		}
		if len(restored.RecentEvents) > 0 {
			planMessage += fmt.Sprintf(" Restored %d recent key events.", len(restored.RecentEvents))
		}
		if len(restoredWarmLessons) > 0 {
			planMessage += fmt.Sprintf(" Restored %d warm lessons.", len(restoredWarmLessons))
		}
		if len(restoredProjectLessons) > 0 {
			planMessage += fmt.Sprintf(" Restored %d project lessons.", len(restoredProjectLessons))
		}
	}
	plannerPayload := map[string]any{"plan_summary": plan.Summary}
	if remediation := plannerRemediationPayload(plan.Remediation); len(remediation) > 0 {
		plannerPayload["remediation"] = remediation
	}
	if err := e.emitAvatarMessage(runID, taskID, plannerAvatarID, "planning", "report", planMessage, fmt.Sprintf("Planner report: prepared %d avatars and %d workflow nodes.", len(plan.Avatars), len(plan.Nodes)), "bootstrap", plannerPayload, nil); err != nil {
		return RunResult{}, err
	}
	if e.skills != nil {
		listings, warnings, err := e.loadApprovedSkills()
		if err != nil {
			return RunResult{}, err
		}
		approvedSkills = len(listings)
		for _, warning := range warnings {
			if emitErr := e.emit(runID, taskID, plannerAvatarID, "planning", "skill.registry_warning", "runtime", map[string]any{
				"path":  warning.Path,
				"error": warning.Error,
			}, nil); emitErr != nil {
				return RunResult{}, emitErr
			}
		}
		for _, listing := range listings {
			if err := e.emit(runID, taskID, plannerAvatarID, "planning", "skill.discovered", "runtime", map[string]any{
				"name":        listing.Name,
				"description": listing.Description,
				"when_to_use": listing.WhenToUse,
				"path":        listing.Path,
				"version":     listing.Version,
				"always_on":   listing.AlwaysOn,
				"role":        listing.Role,
			}, nil); err != nil {
				return RunResult{}, err
			}
			if listing.AlwaysOn {
				definition, err := e.skills.LoadApproved(listing.Path)
				if err != nil {
					return RunResult{}, err
				}
				// S3.9: role-scoped always-on skills only inject for matching
				// avatar roles; empty role = global (intent/plan/navigator).
				alwaysOnSkills = append(alwaysOnSkills, definition)
				if err := e.emit(runID, taskID, plannerAvatarID, "planning", "skill.always_on_loaded", "runtime", map[string]any{
					"name":    definition.Name,
					"path":    definition.Path,
					"version": definition.Version,
					"role":    definition.Role,
				}, nil); err != nil {
					return RunResult{}, err
				}
				continue
			}
			if activeSkill == nil && shouldInvokeSkill(input, listing) {
				definition, err := e.skills.LoadApproved(listing.Path)
				if err != nil {
					return RunResult{}, err
				}
				activeSkill = &definition
				invokedSkillName = definition.Name
				if err := e.emit(runID, taskID, builderAvatarID, "planning", "skill.invoked", "runtime", map[string]any{
					"name":          definition.Name,
					"path":          definition.Path,
					"context":       definition.Context,
					"allowed_tools": definition.AllowedTools,
					"version":       definition.Version,
				}, nil); err != nil {
					return RunResult{}, err
				}
			}
		}
	}
	bundle = bundle.WithAlwaysOnSkills(alwaysOnPromptSkills(filterAlwaysOnSkills(alwaysOnSkills, "", alwaysOnBudgetChars)))
	// Skill proposal persistence happens inside the Builder workflow node
	// (see executeBuilderNodeWork), which routes the write through the
	// verifier-guarded `write` tool. The planning phase here only computes
	// the proposed path via PrepareGenerated (no file write, no Finalize)
	// and emits the `skill.proposed` event so downstream consumers see the
	// canonical path the Builder is about to claim. Avoid calling
	// Store.Generate here — that would write the file twice and collide
	// with the write tool's "refuse to overwrite" guard.
	if e.skills != nil && e.permissionMode != PermissionModePlan {
		var previewProposal skillbuilder.Proposal
		if strings.TrimSpace(e.attachedFilePath) != "" {
			// @file attached: derive Name/Description/Slug/Body from the
			// attached file content (TODO-10). Falls back to Build()
			// internally when the file content is empty.
			previewProposal = skillbuilder.BuildForAttachedFile(input, plan, skillbuilder.AttachedFile{
				Path:    e.attachedFilePath,
				Content: e.attachedFileBytes,
			})
		} else {
			previewProposal = skillbuilder.Build(input, plan, readSummary)
		}
		prepared, prepErr := e.skills.PrepareGenerated(runID, taskID, previewProposal)
		if prepErr != nil {
			return RunResult{}, fmt.Errorf("prepare proposed skill path: %w", prepErr)
		}
		if err := e.emit(runID, taskID, builderAvatarID, "planning", "skill.proposed", "skillbuilder", map[string]any{
			"name":              previewProposal.Name,
			"description":       previewProposal.Description,
			"path":              prepared.Path,
			"slug":              previewProposal.Slug,
			"approval_required": true,
		}, nil); err != nil {
			return RunResult{}, err
		}
	}

	nodeWorkContext := workflowNodeWorkContext{
		runID:              runID,
		taskID:             taskID,
		plan:               plan,
		workflowState:      workflowState,
		input:              input,
		readSummary:        readSummary,
		readTargets:        readTargets,
		explorationRounds:  explorationRounds,
		hasReadTarget:      hasReadTarget,
		researcherAvatarID: researcherAvatarID,
		builderAvatarID:    builderAvatarID,
		criticAvatarID:     criticAvatarID,
		activeSkill:        activeSkill,
		alwaysOnSkills:     alwaysOnSkills,
	}
	nodeWorkResult := workflowNodeWorkResult{readSummary: readSummary, researcherReportArtifacts: researcherReportArtifacts}
	var nodeWorkPauseErr error
	// v3-P0-6: Protect nodeWorkResult and nodeWorkContext from concurrent
	// modification when parallel survey nodes run in goroutines.
	var nodeWorkMu sync.Mutex
	if err := e.runWorkflowPlan(runID, taskID, plan, workflowState, avatarByID, nodeByID, func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		result, err := e.executeWorkflowNodeWork(ctx, node, nodeWorkContext)
		if err != nil {
			return SchedulerNodeResult{}, err
		}
		nodeWorkMu.Lock()
		defer nodeWorkMu.Unlock()
		if strings.TrimSpace(result.readSummary) != "" {
			// P3-2: For parallel survey nodes, accumulate results
			// instead of replacing. Each parallel Researcher surveys
			// a different file bucket (docs/src/cfg).
			if isParallelSurveyNode(node.ID) && nodeWorkResult.readSummary != "" {
				nodeWorkResult.readSummary += "\n[Parallel survey: " + node.ID + "]\n" + result.readSummary
			} else {
				nodeWorkResult.readSummary = result.readSummary
			}
			nodeWorkContext.readSummary = nodeWorkResult.readSummary
		}
		if len(result.researcherReportArtifacts) > 0 {
			// P3-2: Accumulate artifacts from parallel survey nodes.
			if isParallelSurveyNode(node.ID) {
				nodeWorkResult.researcherReportArtifacts = append(
					nodeWorkResult.researcherReportArtifacts,
					result.researcherReportArtifacts...,
				)
			} else {
				nodeWorkResult.researcherReportArtifacts = result.researcherReportArtifacts
			}
			// Extract/update structured context.
			sourceFiles := artifactPaths(result.researcherReportArtifacts)
			if nodeWorkContext.structuredContext == nil {
				nodeWorkContext.structuredContext = extractStructuredContext(
					nodeWorkContext.readSummary,
					sourceFiles,
				)
			} else {
				// Merge new declarations into existing context.
				additional := extractStructuredContext("", sourceFiles)
				nodeWorkContext.structuredContext.InterfaceDecls = append(
					nodeWorkContext.structuredContext.InterfaceDecls, additional.InterfaceDecls...)
				nodeWorkContext.structuredContext.StructDecls = append(
					nodeWorkContext.structuredContext.StructDecls, additional.StructDecls...)
				nodeWorkContext.structuredContext.FuncDecls = append(
					nodeWorkContext.structuredContext.FuncDecls, additional.FuncDecls...)
			}
		}
		if strings.TrimSpace(result.generatedSkillPath) != "" {
			nodeWorkResult.generatedSkillPath = result.generatedSkillPath
			nodeWorkContext.generatedSkillPath = result.generatedSkillPath
		}
		if strings.TrimSpace(result.generatedSkillPreview) != "" {
			nodeWorkResult.generatedSkillPreview = result.generatedSkillPreview
			nodeWorkContext.generatedSkillPreview = result.generatedSkillPreview
		}
		if strings.TrimSpace(result.criticChallenge) != "" {
			nodeWorkResult.criticChallenge = result.criticChallenge
		}
		if result.pauseErr != nil {
			nodeWorkPauseErr = result.pauseErr
		}
		if strings.TrimSpace(result.pausePoint.ID) != "" {
			return SchedulerNodeResult{PausePoint: result.pausePoint}, nil
		}
		return SchedulerNodeResult{ArtifactIDs: result.artifactIDs}, nil
	}); err != nil {
		remediationSummary := fmt.Sprintf("Run stopped during workflow execution: %s", strings.TrimSpace(err.Error()))
		reportContent := ""
		if reportFilePath != "" {
			reportContent = buildAnalysisReportMarkdown(input, plan, readSummary, remediationSummary, remediationSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored != nil)
		}
		return RunResult{
			Summary:               remediationSummary,
			TranscriptPath:        e.transcript.Path(),
			ReportPath:            reportFilePath,
			ReportContent:         reportContent,
			GeneratedSkillPath:    generatedSkillPath,
			GeneratedSkillPreview: generatedSkillPreview,
			ApprovedSkillCount:    approvedSkills,
			InvokedSkillName:      invokedSkillName,
		}, err
	}
	readSummary = nodeWorkResult.readSummary
	researcherReportArtifacts = nodeWorkResult.researcherReportArtifacts
	// Commander multi-round assessment: if the Researcher found no
	// evidence and the original plan was medium/large, emit a
	// downgrade recommendation for future runs.
	if strings.Contains(strings.ToLower(readSummary), "no repository evidence") && !assessment.IsBypassable() {
		_ = e.emit(runID, taskID, plan.AvatarIDByRole("Planner"), "reviewing", "commander.reassess", "runtime", map[string]any{
			"original_level": string(assessment.Level),
			"recommendation": "downgrade",
			"reason":         "researcher found no evidence; future runs should start at trivial or small complexity",
		}, nil)
	}
	generatedSkillPath = nodeWorkResult.generatedSkillPath
	generatedSkillPreview = nodeWorkResult.generatedSkillPreview
	if nodeWorkPauseErr != nil {
		remediationSummary := fmt.Sprintf("Run stopped before synthesis because workflow node needs remediation: %s", strings.TrimSpace(nodeWorkPauseErr.Error()))
		reportContent := ""
		if reportFilePath != "" {
			reportContent = buildAnalysisReportMarkdown(input, plan, readSummary, remediationSummary, remediationSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored != nil)
		}
		return RunResult{
			Summary:               remediationSummary,
			TranscriptPath:        e.transcript.Path(),
			ReportPath:            reportFilePath,
			ReportContent:         reportContent,
			GeneratedSkillPath:    generatedSkillPath,
			GeneratedSkillPreview: generatedSkillPreview,
			ApprovedSkillCount:    approvedSkills,
			InvokedSkillName:      invokedSkillName,
		}, nodeWorkPauseErr
	}

	if reportFilePath != "" {
		if focusedVerification := runFocusedVerificationLeads(ctx, ".", verification.ExecExecutor{}, readSummary); focusedVerification != "" {
			// Batch3/P10: if this run broke project health, do not advertise PASS.
			if e != nil && len(e.changedFiles) > 0 {
				if health := crossLangHealthCheck("."); health != "" {
					focusedVerification = strings.Replace(focusedVerification,
						"Verification finished with PASS.",
						"Verification finished with FAIL (project health). "+health+".",
						1)
					focusedVerification = strings.Replace(focusedVerification,
						"Verification finished with PARTIAL.",
						"Verification finished with FAIL (project health). "+health+".",
						1)
					focusedVerification += " Focused verification warning: honest_gate: cross-lang health failed after source writes."
				}
			}
			readSummary = appendReadSummaryEvidence(readSummary, focusedVerification)
			nodeWorkContext.readSummary = readSummary
			verdict := "PASS"
			sum := focusedVerificationSummary(readSummary)
			if strings.Contains(strings.ToLower(sum), "fail") {
				verdict = "FAIL"
			}
			if err := e.emit(runID, taskID, researcherAvatarID, "reviewing", "verification.completed", "verifier", map[string]any{
				"kind":    "focused_repository_checks",
				"verdict": verdict,
				"summary": sum,
			}, nil); err != nil {
				return RunResult{}, err
			}
		}
	}

	if err := e.emitAvatarMessage(runID, taskID, researcherAvatarID, "executing", "report", readSummary, fmt.Sprintf("Researcher report: %s", readSummary), "survey", nil, researcherReportArtifacts); err != nil {
		return RunResult{}, err
	}
	// Dynamic Planner-Researcher dialogue: context-aware ask and
	// follow-up reflecting actual read summary results.
	hasEvidence := !strings.Contains(strings.ToLower(readSummary), "no repository evidence") && strings.TrimSpace(readSummary) != ""
	researcherAsk := "Researcher survey complete. " + readSummary
	if !hasEvidence {
		researcherAsk = "No repository evidence was found. Researcher recommends skipping the full survey-build chain and using a Direct or Bootstrap approach instead."
	}
	researcherAskSummary := "Researcher ask: " + func() string {
		if len(researcherAsk) <= 120 {
			return researcherAsk
		}
		return researcherAsk[:120] + "..."
	}()
	if err := e.emitAvatarMessage(runID, taskID, researcherAvatarID, "executing", "ask", researcherAsk, researcherAskSummary, "survey", map[string]any{"to_avatar_id": plannerAvatarID, "to_role": "Planner"}, nil); err != nil {
		return RunResult{}, err
	}
	plannerFollowUp := "Planner confirms: repository context reviewed. Proceeding with the planned avatar chain."
	if !hasEvidence {
		plannerFollowUp = "Planner confirms: no evidence found. Proceeding with skill generation based on task description only."
	}
	plannerFollowUpSummary := "Planner follow-up: " + func() string {
		if len(plannerFollowUp) <= 120 {
			return plannerFollowUp
		}
		return plannerFollowUp[:120] + "..."
	}()
	if err := e.emitAvatarMessage(runID, taskID, plannerAvatarID, "planning", "report", plannerFollowUp, plannerFollowUpSummary, "follow_up", map[string]any{"to_avatar_id": researcherAvatarID, "to_role": "Researcher", "reply_to_message_type": "ask"}, nil); err != nil {
		return RunResult{}, err
	}

	finalSummary := fmt.Sprintf(
		"Multi-avatar planning run complete for request %q. Planned across %d avatars and %d workflow nodes. %s Independent verification currently defaults to %t when 3 or more files change.",
		input,
		len(plan.Avatars),
		len(plan.Nodes),
		readSummary,
		e.policy.MinEditedFiles == 3,
	)
	if generatedSkillPath != "" {
		finalSummary += fmt.Sprintf(" Generated candidate skill at %s and requires approval before activation.", generatedSkillPath)
	}
	if generatedSkillPreview != "" {
		finalSummary += fmt.Sprintf(" Prepared skill candidate preview %q in plan mode.", generatedSkillPreview)
	}
	if approvedSkills > 0 {
		finalSummary += fmt.Sprintf(" Discovered %d approved skills in the local registry.", approvedSkills)
	}
	if invokedSkillName != "" {
		finalSummary += fmt.Sprintf(" Invoked approved skill %q for this run.", invokedSkillName)
	}
	if restored != nil {
		finalSummary += fmt.Sprintf(" Continued from transcript %s using the latest stable summary.", restored.SourceTranscript)
	}
	if nodeWorkResult.criticChallenge != "" {
		finalSummary += " " + nodeWorkResult.criticChallenge
	}
	if e != nil && strings.TrimSpace(e.criticHardFailure) != "" {
		finalSummary += " Critic hard failure: " + strings.TrimSpace(e.criticHardFailure)
		readSummary = appendReadSummaryEvidence(readSummary,
			"CRITIC HARD FAILURE (must lead Delivery Summary; do not claim success):\n"+strings.TrimSpace(e.criticHardFailure))
	}
	if failureSummary := focusedVerificationFailureSummary(readSummary); failureSummary != "" {
		finalSummary += " " + failureSummary
	}

	// T8.5 / S3.9: Inject Synthesizer role-specific skill + always-on.
	// V5: refresh evidence with post-Builder disk inventory so synth cannot claim empty repo.
	if inv := buildPostBuildDiskInventory("."); inv != "" {
		readSummary = appendReadSummaryEvidence(readSummary, inv)
	}
	if e != nil {
		e.reconcileChangedFilesToDisk(".")
		if len(e.changedFiles) > 0 {
			readSummary = appendReadSummaryEvidence(readSummary,
				"Builder changed_files: "+strings.Join(e.changedFiles, ", "))
		}
		if impl := implementationSourcesFrom(e.changedFiles); len(impl) == 0 {
			readSummary = appendReadSummaryEvidence(readSummary,
				"THIS RUN DID NOT WRITE IMPLEMENTATION SOURCES. Do not claim you modified library/package files. answer.md, architecture.md, and harness logs do not count.")
		} else {
			readSummary = appendReadSummaryEvidence(readSummary,
				"THIS RUN WROTE IMPLEMENTATION SOURCES: "+strings.Join(impl, ", ")+". Prior Delivery Summaries are stale; What Changed must list these paths.")
		}
		if disk := listAuthoritativeSourcePaths("."); len(disk) > 0 {
			readSummary = appendReadSummaryEvidence(readSummary,
				"AUTHORITATIVE ON-DISK SOURCE PATHS: "+strings.Join(disk, ", "))
		}
		if sigs := formatAuthoritativeExportSignatures("."); sigs != "" {
			readSummary = appendReadSummaryEvidence(readSummary,
				"AUTHORITATIVE EXPORTED API (usage examples MUST match these signatures; do not drop params or error returns):\n"+sigs)
		}
	}
	// Inject current health so Synthesizer does not treat a stale
	// post_build_failed line as the final status.
	if e.health != nil {
		e.health.Reset()
	}
	if healthNow := crossLangHealthCheck("."); healthNow == "" {
		readSummary = appendReadSummaryEvidence(readSummary,
			"CURRENT PROJECT HEALTH (authoritative for Delivery Summary Status): GREEN — compile/test gate passes now. Ignore earlier post_build_failed lines unless they still reproduce.")
		if e != nil && e.builderTurnCapConverged {
			readSummary = appendReadSummaryEvidence(readSummary,
				"BUILDER NOTE: code generation hit max tool turns but on-disk compile/test is green. Mention this under Evidence Notes; do not hide it behind Status GREEN.")
		}
	} else {
		readSummary = appendReadSummaryEvidence(readSummary,
			"CURRENT PROJECT HEALTH (authoritative for Delivery Summary Status): RED — "+healthNow+
				" Do not claim the language toolchain is missing if this line shows go test / pytest / npm test / cargo test output. Do not claim the bug is already fixed while this line is RED.")
		if e != nil && e.builderTurnCapConverged {
			readSummary = appendReadSummaryEvidence(readSummary,
				"BUILDER NOTE: code generation hit max tool turns and compile/test is still RED. Do not claim tests passed.")
		}
	}
	synthProposal := skillbuilder.BuildForRole("Synthesizer", input, plan, readSummary)
	// F77: bundle already has compact always-on (WithAlwaysOnSkills). Do not
	// append the full "## Always-On Skills" dump on top.
	roleSkillBody := synthProposal.Body

	llmRequest := buildLLMSummaryRequest(bundle, plan, readSummary, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored != nil, activeSkill, e.personality, roleSkillBody)
	// Harness thinking policy: Synthesizer is extractive summary — CoT ROI low;
	// flash burned ~20k thinking chars after codegen already succeeded.
	applyHarnessThinkModeOff(&llmRequest)
	synthesizerNode := workflowNodeByRole(plan, "Synthesizer")
	synthesizerNodePayload := map[string]any{
		"lifecycle": "tail_synthesis",
		"node_role": "Synthesizer",
	}
	llmTimeoutMillis := 0
	if e.llmStatus != nil {
		llmTimeoutMillis = e.llmStatus.TimeoutMillis
	}
	if strings.TrimSpace(synthesizerNode.ID) != "" {
		synthesizerNodePayload["node_id"] = synthesizerNode.ID
		synthesizerNodePayload["node_title"] = synthesizerNode.Title
	}
	llmStartedPayload := map[string]any{
		"provider":             e.llm.Provider(),
		"summary":              "Synthesizer requesting LLM summary.",
		"read_evidence_items":  len(reportReadSummaryItems(readSummary)),
		"structured_output":    llmRequest.StructuredOutput,
		"web_search_requested": llmRequest.UseWebSearch,
	}
	mergePromptObservability(llmStartedPayload, "Synthesizer", llmRequest.SystemPrompt, llmRequest.UserPrompt)
	if llmTimeoutMillis > 0 {
		llmStartedPayload["timeout_ms"] = llmTimeoutMillis
	}
	for key, value := range synthesizerNodePayload {
		llmStartedPayload[key] = value
	}
	if err := e.emit(runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "llm.started", "llm", llmStartedPayload, nil); err != nil {
		return RunResult{}, err
	}
	// A4: surface thinking during Synthesizer draft (same UX as Builder codegen).
	var thinkingChars int
	var lastThinkingEmit time.Time
	var thinkingBuf strings.Builder
	e.attachContentStreamHeartbeats(&llmRequest, runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "")
	llmRequest.ThinkingCallback = func(chunk string) {
		thinkingChars += len(chunk)
		thinkingBuf.WriteString(chunk)
		now := time.Now()
		if lastThinkingEmit.IsZero() || now.Sub(lastThinkingEmit) >= 2*time.Second {
			lastThinkingEmit = now
			_ = e.emit(runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "llm.thinking", "llm", map[string]any{
				"chars":   thinkingChars,
				"preview": thinkingPreviewTail(thinkingBuf.String(), 240),
				"message": fmt.Sprintf("thinking… (%d chars)", thinkingChars),
			}, nil)
		}
	}
	if llmResponse, err := e.llm.Generate(ctx, llmRequest); err != nil {
		synthesisStatus = fmt.Sprintf("failed: %s", strings.TrimSpace(err.Error()))
		payload := map[string]any{
			"provider":            e.llm.Provider(),
			"error":               err.Error(),
			"read_evidence_items": len(reportReadSummaryItems(readSummary)),
		}
		mergePromptObservability(payload, "Synthesizer", llmRequest.SystemPrompt, llmRequest.UserPrompt)
		mergeLLMUsage(payload, llmResponse.Usage)
		if llmTimeoutMillis > 0 {
			payload["timeout_ms"] = llmTimeoutMillis
		}
		for key, value := range synthesizerNodePayload {
			payload[key] = value
		}
		if isProviderBusyError(err.Error()) {
			busyPayload := map[string]any{
				"error": err.Error(),
				"hint":  "LLM provider overloaded; retry later or switch model/provider",
			}
			for key, value := range synthesizerNodePayload {
				busyPayload[key] = value
			}
			_ = e.emit(runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "workflow.provider_busy", "runtime", busyPayload, nil)
		}
		if emitErr := e.emit(runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "llm.failed", "llm", payload, nil); emitErr != nil {
			return RunResult{}, emitErr
		}
	} else if strings.TrimSpace(llmResponse.Text) != "" {
		synthesisStatus = "completed"
		payload := map[string]any{
			"provider":            llmResponse.Provider,
			"model":               llmResponse.Model,
			"fallback":            llmResponse.Fallback,
			"content":             llmResponse.Text,
			"read_evidence_items": len(reportReadSummaryItems(readSummary)),
		}
		mergePromptObservability(payload, "Synthesizer", llmRequest.SystemPrompt, llmRequest.UserPrompt)
		if llmTimeoutMillis > 0 {
			payload["timeout_ms"] = llmTimeoutMillis
		}
		for key, value := range synthesizerNodePayload {
			payload[key] = value
		}
		if llmResponse.Usage.Known {
			mergeLLMUsage(payload, llmResponse.Usage)
		}
		if err := e.emit(runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "llm.completed", "llm", payload, nil); err != nil {
			return RunResult{}, err
		}
		analysisReportText = strings.TrimSpace(llmResponse.Text)
		finalSummary += " Synthesis: " + synthesisSummaryLine(analysisReportText)
	}
	if strings.TrimSpace(analysisReportText) == "" {
		if synthesisStatus == "not_requested" {
			synthesisStatus = "fallback: no LLM synthesis text was produced"
		}
		analysisReportText = buildFallbackAnalysisReport(readSummary, plan, generatedSkillPath, generatedSkillPreview, invokedSkillName, synthesisStatus)
		finalSummary += " Synthesis fallback used: " + synthesisStatus + "."
	}
	if synthesisStatusIndicatesDegraded(synthesisStatus) && !strings.Contains(strings.ToLower(finalSummary), "synthesis degraded") {
		finalSummary += " Synthesis degraded: " + synthesisStatus + "."
	}

	if err := e.emitAvatarMessage(runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "summarize", finalSummary, fmt.Sprintf("Synthesizer summary: completed planning run across %d avatars and %d workflow nodes.", len(plan.Avatars), len(plan.Nodes)), "summary", nil, nil); err != nil {
		return RunResult{}, err
	}
	if err := e.emit(runID, taskID, plan.AvatarIDByRole("Synthesizer"), "reviewing", "synthesis.completed", "runtime", map[string]any{
		"lifecycle":    "tail_synthesis",
		"node_role":    "Synthesizer",
		"status":       synthesisStatus,
		"summary_set":  strings.TrimSpace(finalSummary) != "",
		"report_chars": len(analysisReportText),
	}, nil); err != nil {
		return RunResult{}, err
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:     runID,
		TaskID:    taskID,
		AvatarID:  plan.AvatarIDByRole("Synthesizer"),
		NodeID:    "lifecycle-synthesize",
		NodeRole:  "Synthesizer",
		NodeTitle: "Summarize outcome for the user",
		Tool:      "synthesize",
		Operation: "summarize",
		Status:    "completed",
		Summary:   "Synthesizer tail lifecycle: merged run evidence into the user-facing summary.",
	}); err != nil {
		return RunResult{}, err
	}
	completionStatus, completionSummary := e.runCompletionStatus(runID, taskID, finalSummary)
	// Batch5/B5: compute build gate BEFORE run.completed so status matches footer.
	finalHealthErr := ""
	if wd, wdErr := os.Getwd(); wdErr == nil {
		if health := crossLangHealthCheck(wd); health != "" {
			finalHealthErr = health
			if completionStatus == "completed" && (e == nil || !planner.LooksLikeRepoAnalysisOrReport(e.layoutTaskHint)) {
				completionStatus = "completed_unverified"
			}
			if completionSummary == "" {
				completionSummary = health
			}
			if !strings.Contains(finalSummary, health) {
				finalSummary += " " + health
			}
			// F60: do not stamp build_failed over a planning-gate failure.
			if isPlanningGateFailure(result) {
				result.PhaseAdvanceBlocked = mergePhaseBlock(result.PhaseAdvanceBlocked, "plan_incomplete")
				e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, "plan_incomplete")
			} else {
				result.PhaseAdvanceBlocked = mergePhaseBlock(result.PhaseAdvanceBlocked, "build_failed")
				e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, "build_failed")
			}
			result.BuildOK = false
			result.BuildOKKnown = true
		} else {
			result.BuildOK = true
			result.BuildOKKnown = true
			// F67: soft-fail may have been healed — do not force needs_remediation on green disk.
			if e != nil && strings.TrimSpace(e.criticHardFailure) != "" {
				e.criticHardFailure = ""
			}
		}
	}
	// F65: Critic hard failure must never surface as plain completed (only when still red).
	if e != nil && strings.TrimSpace(e.criticHardFailure) != "" {
		completionStatus = "needs_remediation"
		if completionSummary == "" {
			completionSummary = strings.TrimSpace(e.criticHardFailure)
		}
		e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, "build_failed")
	}
	// D3: Critic may have blocked advance before Synthesizer — do not emit plain completed.
	result.PhaseAdvanceBlocked = mergePhaseBlock(result.PhaseAdvanceBlocked, e.phaseAdvanceBlocked)
	if isSynthesisAvailabilityFailure(result) && result.BuildOK {
		if result.PhaseAdvanceBlocked == "needs_remediation" {
			result.PhaseAdvanceBlocked = ""
		}
	}
	if result.PhaseAdvanceBlocked != "" && completionStatus == "completed" {
		if isSynthesisAvailabilityFailure(result) && result.BuildOK {
			// keep completed — 402 is availability, not an incomplete project
		} else {
			completionStatus = "completed_unverified"
			blockNote := "phase advance blocked: " + result.PhaseAdvanceBlocked
			if completionSummary == "" {
				completionSummary = blockNote
			}
			if !strings.Contains(finalSummary, blockNote) {
				finalSummary += " " + blockNote
			}
		}
	}
	if completionStatus != "completed" && completionSummary != "" && !strings.Contains(finalSummary, completionSummary) {
		finalSummary += " " + completionSummary
	}
	projectRoot := e.projectRoot
	if projectRoot == "" {
		projectRoot = "."
	}
	// F67: rewrite answer.md to match final gates (cross-lang).
	{
		e.reconcileChangedFilesToDisk(projectRoot)
		changed := e.changedFilesSnapshot()
		if e.builderTurnCapConverged && !strings.Contains(analysisReportText, "max tool turns") {
			analysisReportText += "\nBUILDER NOTE: code generation hit max tool turns"
		}
		_ = reconcileFinalDeliveryAnswer(projectRoot, completionStatus, analysisReportText,
			result.BuildOK, result.BuildOKKnown, finalHealthErr, changed)
		workflow.SyncCompletedPlanPhases(projectRoot)
	}
	footer := e.buildRunFooter(projectRoot, NextStepInput{
		Status:              completionStatus,
		Summary:             finalSummary,
		BuildOK:             result.BuildOK,
		BuildOKKnown:        result.BuildOKKnown,
		PhaseAdvanceBlocked: result.PhaseAdvanceBlocked,
		ClarifyPending:      result.ClarifyPending != nil,
	})
	result.Footer = footer
	runCompletedPayload := map[string]any{"status": completionStatus, "summary": finalSummary}
	for k, v := range footer.EventPayload() {
		runCompletedPayload[k] = v
	}
	if trimmedStatus := strings.TrimSpace(synthesisStatus); trimmedStatus != "" {
		runCompletedPayload["synthesis_status"] = trimmedStatus
		if synthesisStatusIndicatesDegraded(trimmedStatus) {
			runCompletedPayload["synthesis_degraded"] = true
		}
	}
	if err := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", runCompletedPayload, nil); err != nil {
		return RunResult{}, err
	}
	// run.change_summary is emitted in runOnce defer after phase sync so
	// next-steps reflect final Active Phase / advance blocks.
	// Update project workflow tracking file with file change summary.

	stableBoundaryPayload := map[string]any{"approved_skill_count": approvedSkills}
	if trimmedStatus := strings.TrimSpace(synthesisStatus); trimmedStatus != "" {
		stableBoundaryPayload["synthesis_status"] = trimmedStatus
		if synthesisStatusIndicatesDegraded(trimmedStatus) {
			stableBoundaryPayload["synthesis_degraded"] = true
		}
	}
	if err := e.emitStableBoundary(runID, taskID, "", completionStatus, finalSummary, "run_terminal", stableBoundaryPayload); err != nil {
		return RunResult{}, err
	}
	if err := e.persistWarmLesson(runID, taskID, plan.Summary, finalSummary); err != nil {
		return RunResult{}, err
	}
	if err := e.persistEvolutionCandidates(runID, taskID, plan.Summary, finalSummary); err != nil {
		return RunResult{}, err
	}

	// Auto-promotion: if evolution surfaced skill_promotion
	// candidates, automatically generate a new skill file to
	// close the self-iteration loop.
	if e.skills != nil {
		snapshot, snapErr := e.memory.LoadLatestTaskSnapshot(taskID)
		if snapErr == nil {
			skillRates := e.skillSuccessRates()
			for _, candidate := range evolution.BuildCandidates(plan.Summary, finalSummary, snapshot, skillRates) {
				if candidate.Kind != "skill_promotion" {
					continue
				}
				proposal := skillbuilder.Build(candidate.Summary, plan, readSummary)
				if _, genErr := e.skills.Generate(runID, taskID, proposal); genErr == nil {
					_ = e.emit(runID, taskID, plan.AvatarIDByRole("Builder"), "reviewing", "skill.auto_promoted", "evolution", map[string]any{
						"candidate_kind":    candidate.Kind,
						"candidate_summary": candidate.Summary,
						"skill_name":        proposal.Name,
					}, nil)
				}
			}
		}
	}
	if err := e.persistProjectLessons(taskID); err != nil {
		return RunResult{}, err
	}

	// Promote high-confidence project lessons to warm lessons
	// so successful patterns influence future runs (long-term learning).
	if e.projectMemory != nil {
		if promoted, promoteErr := e.projectMemory.PromoteProjectLessonsToWarm(taskID, 3); promoteErr == nil && promoted > 0 {
			_ = promoted // lessons promoted silently
		}
	}

	// S3.6: close the skill feedback loop — ledger task_completed entries
	// feed ShouldAutoApprove and Past Experience ranking.
	runSucceeded := strings.EqualFold(strings.TrimSpace(synthesisStatus), "completed")
	if synthesisStatusIndicatesDegraded(synthesisStatus) ||
		strings.Contains(strings.ToLower(finalSummary), "needs_remediation") ||
		strings.Contains(strings.ToLower(finalSummary), "verification finished with fail") {
		runSucceeded = false
	}
	e.recordInvokedSkillUse(taskID, invokedSkillName, activeSkill, runSucceeded)

	reportContent := ""
	if reportFilePath != "" {
		reportContent = buildAnalysisReportMarkdown(input, plan, readSummary, finalSummary, analysisReportText, generatedSkillPath, generatedSkillPreview, invokedSkillName, restored != nil)
		reportContent = AppendRunFooterMarkdown(reportContent, footer)
	}

	if err := e.transcript.Flush(); err != nil {
		return RunResult{}, err
	}

	result = RunResult{
		Summary:               finalSummary,
		TranscriptPath:        e.transcript.Path(),
		ReportPath:            reportFilePath,
		ReportContent:         reportContent,
		SynthesisStatus:       synthesisStatus,
		GeneratedSkillPath:    generatedSkillPath,
		GeneratedSkillPreview: generatedSkillPreview,
		ApprovedSkillCount:    approvedSkills,
		InvokedSkillName:      invokedSkillName,
		CriticInsights:        strings.Join(e.criticInsights, "; "),
		BuildOK:               result.BuildOK,
		BuildOKKnown:          result.BuildOKKnown,
		PhaseAdvanceBlocked:   result.PhaseAdvanceBlocked,
		Footer:                footer,
	}
	return result, nil
}

func alwaysOnPromptSkills(definitions []skills.Definition) []prompt.AlwaysOnSkill {
	if len(definitions) == 0 {
		return nil
	}
	out := make([]prompt.AlwaysOnSkill, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, prompt.AlwaysOnSkill{
			Name:    definition.Name,
			Context: definition.Context,
			Body:    definition.Body,
		})
	}
	return out
}

// alwaysOnBudgetChars caps total always-on body chars injected into a prompt
// (Claude Code ~1% skill listing budget idea; avatars uses a fixed char budget).
const alwaysOnBudgetChars = 12000

// filterAlwaysOnSkills keeps global skills (empty role) plus skills matching
// avatarRole, then truncates by total body budget (S3.9).
// When avatarRole is empty (planning/synthesis bundle), only global skills
// are kept — role-specific always-on must not leak across avatars.
func filterAlwaysOnSkills(definitions []skills.Definition, avatarRole string, budget int) []skills.Definition {
	if len(definitions) == 0 {
		return nil
	}
	role := strings.ToLower(strings.TrimSpace(avatarRole))
	filtered := make([]skills.Definition, 0, len(definitions))
	for _, d := range definitions {
		if isEphemeralGeneratedSkill(d) {
			continue
		}
		skillRole := strings.ToLower(strings.TrimSpace(d.Role))
		switch {
		case skillRole == "":
			filtered = append(filtered, d) // global
		case role != "" && skillRole == role:
			filtered = append(filtered, d)
		}
	}
	if budget <= 0 {
		return filtered
	}
	out := make([]skills.Definition, 0, len(filtered))
	used := 0
	for _, d := range filtered {
		cost := len(d.Body) + len(d.Context)
		if used > 0 && used+cost > budget {
			continue
		}
		out = append(out, d)
		used += cost
	}
	return out
}

func isEphemeralGeneratedSkill(d skills.Definition) bool {
	p := filepath.ToSlash(strings.ToLower(d.Path))
	if strings.Contains(p, "/skills/generated/") || strings.HasPrefix(p, "skills/generated/") {
		return true
	}
	n := strings.TrimSpace(d.Name)
	if len(n) >= 9 && n[8] == '-' {
		allDigit := true
		for i := 0; i < 8; i++ {
			if n[i] < '0' || n[i] > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return true
		}
	}
	return false
}

func (e *Engine) loadApprovedSkills() ([]skills.Listing, []skills.ListingWarning, error) {
	if e.skills == nil {
		return nil, nil, nil
	}
	scan, err := e.skills.ListApprovedTolerant()
	if err != nil {
		return nil, nil, err
	}
	return scan.Listings, scan.Warnings, nil
}

func runTerminalStatus(err error) string {
	if err == nil {
		return "completed"
	}
	if strings.Contains(strings.ToLower(err.Error()), "approval required") {
		return "awaiting_approval"
	}
	if strings.Contains(strings.ToLower(err.Error()), "verification failed") {
		return "needs_remediation"
	}
	return "failed"
}

// extractLessonsLearned auto-extracts a lesson from task results for process_record memory.
// For failures: extracts the specific error pattern. For successes: records what files changed.
// W13: These become cross-session memory — the LLM reads them next run to avoid past mistakes.
func extractLessonsLearned(summary string, runErr error, changedFiles []string, criticInsights string) string {
	// Critic insights come first — they are the most valuable for cross-session learning.
	if strings.TrimSpace(criticInsights) != "" {
		return criticInsights
	}
	if runErr != nil {
		errStr := runErr.Error()
		switch {
		case strings.Contains(errStr, "verification failed"):
			if idx := strings.Index(errStr, "[FAIL]"); idx >= 0 {
				end := strings.Index(errStr[idx:], "\n")
				if end < 0 {
					end = len(errStr[idx:])
				}
				return "FAIL: " + strings.TrimSpace(errStr[idx:idx+end])
			}
			return "FAIL: verification failed — check build/test errors"
		case strings.Contains(errStr, "health_block") || strings.Contains(errStr, "truncation"):
			return "PITFALL: Builder output truncated — file health check blocked write"
		case strings.Contains(errStr, "empty response") || strings.Contains(errStr, "no parseable"):
			return "PITFALL: Builder returned empty response — task may need decomposition"
		case strings.Contains(errStr, "not found") || strings.Contains(errStr, "cannot find"):
			return "PITFALL: file not found — use create path, not edit, for new files"
		default:
			return "ERROR: " + firstLine(errStr, 180)
		}
	}
	// Success: record what was built/changed with relative paths.
	if len(changedFiles) > 0 {
		// Convert absolute paths to relative for portability.
		relPaths := make([]string, 0, len(changedFiles))
		seen := map[string]bool{}
		if wd, wdErr := os.Getwd(); wdErr == nil {
			for _, f := range changedFiles {
				if rel, relErr := filepath.Rel(wd, f); relErr == nil {
					if !seen[rel] {
						relPaths = append(relPaths, rel)
						seen[rel] = true
					}
				} else if !seen[f] {
					relPaths = append(relPaths, filepath.Base(f))
					seen[f] = true
				}
			}
		}
		// Extract verification result from summary.
		verdict := "built"
		if strings.Contains(strings.ToLower(summary), "verification finished with pass") {
			verdict = "PASS"
		} else if strings.Contains(strings.ToLower(summary), "verification finished with fail") {
			verdict = "FAIL"
		}
		return fmt.Sprintf("%s: %d file(s) — %s", verdict, len(relPaths), strings.Join(relPaths, ", "))
	}
	// No files changed — record what happened from the summary.
	if strings.Contains(strings.ToLower(summary), "verification") {
		return "verified: no file changes needed"
	}
	return "task processed: no file changes"
}

func firstLine(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\n\r"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if maxLen <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-3]) + "..."
}

// isPlanningQualityAbort reports construct/confirm quality failures that must
// not be re-labeled as build_failed by end-of-run health probes (F11).
// Cross-provider: applies to any model that fails the same planning gates.
func isPlanningQualityAbort(synthesisStatus string) bool {
	s := strings.ToLower(strings.TrimSpace(synthesisStatus))
	return strings.HasPrefix(s, "failed: construct_plan") ||
		strings.HasPrefix(s, "failed: confirm_plan")
}

func (e *Engine) runCompletionStatus(runID string, taskID string, summary string) (string, string) {
	// G11: empty greenfield *implementation* must never report completed.
	// Analysis / inspect / plan-only runs often have empty app/ — do not punish those.
	if wd, wdErr := os.Getwd(); wdErr == nil && deliveryLooksEmpty(wd) {
		lowerSum := strings.ToLower(summary)
		implementish := strings.Contains(lowerSum, "builder produced no files") ||
			strings.Contains(lowerSum, "confirm_plan") ||
			(e != nil && e.confirmPlanDone) ||
			(strings.Contains(lowerSum, "builder") &&
				(strings.Contains(lowerSum, "implement phase") ||
					strings.Contains(lowerSum, "wrote no source files — counts reflect disk checklist, not this run's work")))
		if implementish {
			return "needs_remediation", "empty delivery: no substantive sources on disk"
		}
	}
	// When memory is unavailable, default to "completed_unverified"
	// instead of "completed" — we cannot confirm verifier passed without memory.
	// Inspect/report runs do not require a verifier record.
	if e == nil || e.memory == nil {
		if e != nil && planner.LooksLikeRepoAnalysisOrReport(e.layoutTaskHint) {
			return "completed", ""
		}
		return "completed_unverified", "memory store unavailable — verifier status unknown"
	}
	snapshot, err := e.memory.LoadLatestTaskSnapshot(taskID)
	if err != nil {
		return "completed_unverified", "failed to load task snapshot — verifier status unknown"
	}
	verdictRecord := memstore.LatestRunVerifierVerdictEvaluation(snapshot.EvaluationRecords, runID)
	if verdictRecord == nil {
		// NEW-1: No verifier record — don't blindly trust "completed".
		// Run a quick go build check to confirm the project actually compiles.
		// If it fails, the run should be "needs_remediation" not "completed".
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if errStr := crossLangHealthCheck(wd); errStr != "" {
				return "needs_remediation", errStr
			}
		}
		return "completed", ""
	}
	verdict := strings.ToUpper(strings.TrimSpace(verdictRecord.Verdict))
	switch verdict {
	case "FAIL":
		return "needs_remediation", fmt.Sprintf("Verifier blocked completion: %s", strings.TrimSpace(verdictRecord.Summary))
	case "PARTIAL":
		// P8-6 / P9-6: advisory PARTIAL with green health → completed (not churn).
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if errStr := crossLangHealthCheck(wd); errStr == "" {
				sum := strings.ToLower(strings.TrimSpace(verdictRecord.Summary))
				if isAdvisoryPartialVerdict(sum) {
					return "completed", ""
				}
			}
		}
		return "completed_unverified", fmt.Sprintf("Verifier produced a partial verdict: %s", strings.TrimSpace(verdictRecord.Summary))
	default:
		// P2/NEW-1: Even when verifier says PASS, run cross-language health checks.
		// This catches cases where verifier didn't check all languages.
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if errStr := crossLangHealthCheck(wd); errStr != "" {
				return "needs_remediation", errStr
			}
		}
		return "completed", ""
	}
}

// isAdvisoryPartialVerdict reports verifier PARTIALs that should not block
// completion when cross-lang health is already green (P8-6 / P9-6).
func isAdvisoryPartialVerdict(sum string) bool {
	if sum == "" {
		return false
	}
	if strings.Contains(sum, "skipped") || strings.Contains(sum, "not found") ||
		strings.Contains(sum, "advisory") || strings.Contains(sum, "sqlite3") {
		return true
	}
	// Typical advisory line: "0 passed, 1 partial (...), 0 failed"
	if strings.Contains(sum, "0 failed") && strings.Contains(sum, "partial") {
		return true
	}
	return false
}

// hasGoFiles returns true if the directory contains any .go files.
func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

func quickGoBuildCheck(workingDir string) string {
	if h := verification.CurrentHealth(); h != nil {
		return h.Memo(workingDir, "go-build", func() string {
			return quickGoBuildCheckUncached(workingDir)
		})
	}
	return quickGoBuildCheckUncached(workingDir)
}

// quickGoBuildCheckUncached runs `go build ./...` in the given directory and returns
// a non-empty error string if the build fails. Returns "" on success or if
// the project is not a Go project (no go.mod). NEW-1.
func quickGoBuildCheckUncached(workingDir string) string {
	goModPath := filepath.Join(workingDir, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		return "" // not a Go project, skip
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := platform.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = workingDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(stderr.String())
		if len(output) > 500 {
			output = output[:500] + " [...]"
		}
		return clarifyGoBuildNoNonTest(workingDir, output)
	}
	// GAP-3: go build ./... passes even when main.go deleted.
	// Also verify root package builds to catch missing entry points.
	mainCmd := platform.CommandContext(ctx, "go", "build", ".")
	mainCmd.Dir = workingDir
	mainCmd.Stderr = &stderr
	if err := mainCmd.Run(); err != nil {
		if hasGoFiles(workingDir) {
			output := strings.TrimSpace(stderr.String())
			if len(output) > 500 {
				output = output[:500] + " [...]"
			}
			return clarifyGoBuildNoNonTest(workingDir, output)
		}
	}
	// R3-10: hollow entrypoints (Go cmd/* + other langs via hollowEntrypointCheck).
	if reason := hollowEntrypointCheck(workingDir); reason != "" {
		return reason
	}
	return ""
}

// clarifyGoBuildNoNonTest rewrites the Go-specific "no non-test Go files"
// message when the module actually has implementation files. The toolchain
// prints that for a nested test-only package (often a junk diag dir); stating
// it as if the whole project were empty contradicts files_changed (F86).
func clarifyGoBuildNoNonTest(wd, output string) string {
	if !strings.Contains(strings.ToLower(output), "no non-test go files") {
		return output
	}
	impl := listNonTestGoFiles(wd)
	if len(impl) == 0 {
		return output
	}
	return "nested package has tests but no implementation files; module still has: " +
		strings.Join(impl, ", ") + ". original: " + output
}

func listNonTestGoFiles(wd string) []string {
	var out []string
	_ = filepath.WalkDir(wd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".avatars" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(wd, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isJunkScaffoldPath(rel) {
			return nil
		}
		out = append(out, rel)
		if len(out) >= 8 {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}

// hollowCmdMainCheck reports entrypoints under cmd/ that are package-only stubs
// (no func main) or empty HTTP servers (func main with no listen/serve — W3).
func hollowCmdMainCheck(workingDir string) string {
	cmdRoot := filepath.Join(workingDir, "cmd")
	entries, err := os.ReadDir(cmdRoot)
	if err != nil {
		return ""
	}
	hasHandlerPkg := projectHasHTTPHandlerLayout(workingDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mainPath := filepath.Join(cmdRoot, e.Name(), "main.go")
		data, err := os.ReadFile(mainPath)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		rel := filepath.ToSlash(filepath.Join("cmd", e.Name(), "main.go"))
		if body == "" {
			return fmt.Sprintf("%s is empty", rel)
		}
		if !strings.Contains(body, "func main") {
			return fmt.Sprintf("%s has no func main (hollow entrypoint)", rel)
		}
		if reason := hollowHTTPEntrypointReason(rel, body, hasHandlerPkg); reason != "" {
			return reason
		}
	}
	return ""
}

// projectHasHTTPHandlerLayout is true when the tree looks like an HTTP API
// (handlers/routes packages) so an empty main is clearly unfinished (W3).
func projectHasHTTPHandlerLayout(workingDir string) bool {
	for _, rel := range []string{
		"internal/handler", "internal/handlers", "internal/api", "internal/server",
		"pkg/handler", "pkg/api", "handler", "handlers", "routes", "api",
	} {
		if st, err := os.Stat(filepath.Join(workingDir, filepath.FromSlash(rel))); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// hollowHTTPEntrypointReason rejects entrypoints that declare main but never
// start an HTTP server / attach routes (W3 pingwatch empty main). Cross-lang
// tokens cover Go/Node/Python common patterns when content is checked.
func hollowHTTPEntrypointReason(rel, body string, expectHTTP bool) string {
	relLower := strings.ToLower(rel)
	serverishPath := strings.Contains(relLower, "/server/") || strings.Contains(relLower, "/api/") ||
		strings.HasSuffix(relLower, "/server/main.go")
	if !expectHTTP && !serverishPath {
		return ""
	}
	lower := strings.ToLower(body)
	serveTokens := []string{
		"listenandserve", "listenandservetls", "http.server", ".listen(",
		"servemux", "handles(", "handlefunc(", "chi.newrouter", "gin.default",
		"echo.new", "mux.newrouter", "http.handler", "fasthttp",
		"uvicorn", "fastapi(", "app.run(", "create_app(",
		"createserver", "express()", "koa(", "hono(",
	}
	for _, t := range serveTokens {
		if strings.Contains(lower, t) {
			return ""
		}
	}
	if goMainBodyIsTrivial(body) {
		return fmt.Sprintf("%s has an empty/trivial func main (hollow entrypoint)", rel)
	}
	// Non-trivial body without listen tokens: only reject when the tree already
	// looks like an HTTP API (handlers present). Bare cmd/server CLI scaffolds
	// with real statements must still pass (W3 targets empty/unwired, not fmt.Println).
	if expectHTTP {
		return fmt.Sprintf("%s declares main but never listens/serves HTTP (unwired entrypoint)", rel)
	}
	return ""
}

// hollowNonGoHTTPEntrypointReason rejects empty Node/Python HTTP entry files (W3).
func hollowNonGoHTTPEntrypointReason(rel, body string) string {
	slash := strings.ToLower(filepath.ToSlash(rel))
	base := strings.ToLower(filepath.Base(rel))
	entryName := false
	switch base {
	case "server.js", "server.mjs", "server.cjs", "server.ts", "app.js", "app.ts",
		"index.js", "index.ts", "main.js", "main.ts",
		"server.py", "app.py", "main.py", "__main__.py":
		entryName = true
	}
	serverish := strings.Contains(slash, "/server/") || strings.Contains(slash, "/api/") ||
		base == "server.js" || base == "server.ts" || base == "server.py" ||
		base == "app.js" || base == "app.ts" || base == "app.py"
	if !entryName || !serverish {
		return ""
	}
	lower := strings.ToLower(body)
	for _, t := range []string{
		".listen(", "createserver", "express()", "fastapi(", "app.run(",
		"uvicorn", "http.server", "listenandserve", "koa(", "hono(",
	} {
		if strings.Contains(lower, t) {
			return ""
		}
	}
	if len(strings.TrimSpace(body)) < 80 {
		return fmt.Sprintf("%s looks like an empty/unwired HTTP entrypoint", rel)
	}
	return ""
}

func goMainBodyIsTrivial(body string) bool {
	// Strip package/import and see if main is effectively empty.
	trimmed := strings.TrimSpace(body)
	if !strings.Contains(trimmed, "func main") {
		return false
	}
	idx := strings.Index(trimmed, "func main")
	rest := trimmed[idx:]
	// Find opening brace of main.
	brace := strings.Index(rest, "{")
	if brace < 0 {
		return true
	}
	depth := 0
	var inner strings.Builder
	for i := brace; i < len(rest); i++ {
		c := rest[i]
		if c == '{' {
			depth++
			if depth == 1 {
				continue
			}
		}
		if c == '}' {
			depth--
			if depth == 0 {
				break
			}
		}
		if depth >= 1 {
			inner.WriteByte(c)
		}
	}
	core := strings.TrimSpace(inner.String())
	core = strings.TrimSpace(strings.ReplaceAll(core, "\t", " "))
	if core == "" || core == "return" || core == "return nil" {
		return true
	}
	// Only comments / blank lines.
	for _, line := range strings.Split(core, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		return false
	}
	return true
}

func quickGoTestCheck(workingDir string) string {
	if h := verification.CurrentHealth(); h != nil {
		return h.Memo(workingDir, "go-test", func() string {
			return quickGoTestCheckUncached(workingDir)
		})
	}
	return quickGoTestCheckUncached(workingDir)
}

// quickGoTestCheckUncached runs go test ./... with a 60s timeout. Returns error output
// on failure, empty string on success. Skips non-Go projects (no go.mod).
// R3-14: use CombinedOutput — go test writes FAIL details to stdout, not stderr.
func quickGoTestCheckUncached(workingDir string) string {
	goModPath := filepath.Join(workingDir, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		return ""
	}
	if !goModuleHasSourceFiles(workingDir) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := platform.CommandContext(ctx, "go", "test", "-timeout", "45s", "./...")
	cmd.Dir = workingDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if hung := hungTestRunnerMessage("go test", ctx, out); hung != "" {
			return "command timed out with no output (test suite likely hung — wall-clock wait or deadlock)"
		}
		output := strings.TrimSpace(string(out))
		if output == "" {
			output = err.Error()
		}
		lower := strings.ToLower(output)
		if strings.Contains(lower, "matched no packages") || strings.Contains(lower, "no packages to test") {
			return ""
		}
		if len(output) > 500 {
			output = output[:500] + " [...]"
		}
		return output
	}
	if !workflow.ProjectHasTestFiles(workingDir) {
		return "go test ran with no test files"
	}
	return ""
}

func goModuleHasSourceFiles(workingDir string) bool {
	found := false
	_ = filepath.Walk(workingDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor", "node_modules", ".avatars":
				if path != workingDir {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(info.Name()), ".go") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// cavemanActionChinese is the small set of Chinese trigger phrases
// the user types for the corresponding caveman-<action> skill. The map
// is intentionally narrow to avoid accidental triggers; new entries
// should be added only after observing a real user phrasing in the
// wild that is NOT already covered by the English action keyword.
var cavemanActionChinese = map[string][]string{
	"commit": {"提交", "commit 信息", "commit message"},
	"help":   {"帮助", "caveman 用法", "caveman help"},
	"review": {"审查", "代码审查", "code review"},
}

// shouldInvokeSkill decides whether a skill should auto-trigger for a
// given user request. Triggers (in order of evaluation):
//  1. Direct name match: request contains the skill's name, or the
//     same name with dashes collapsed to spaces (e.g. "caveman commit"
//     still matches "caveman-commit").
//  2. caveman-<action> action match: when the skill follows the
//     `caveman-<action>` naming convention, the request matching the
//     action keyword (e.g. "commit", "review", "help") also triggers
//     it. This is what makes the approved caveman-commit / caveman-
//     help / caveman-review skills reachable instead of being dead
//     code behind an overly narrow survey-only heuristic.
//  3. Chinese action variant: the small cavemanActionChinese map adds
//     explicit Chinese phrasings for the common caveman actions.
//  4. Backward compat: the legacy survey heuristic (analy/survey/
//     refactor) still triggers a skill whose description mentions
//     "survey", preserving the previous behavior for the repo-survey
//     skill and any other survey-tagged catalog entry.
func shouldInvokeSkill(input string, listing skills.Listing) bool {
	request := strings.ToLower(strings.TrimSpace(input))
	if request == "" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(listing.Name))
	if name == "" {
		return false
	}
	// (1) Direct name match (and dash-collapsed form).
	if strings.Contains(request, name) {
		return true
	}
	nameSpaced := strings.ReplaceAll(name, "-", " ")
	if nameSpaced != name && strings.Contains(request, nameSpaced) {
		return true
	}
	// (2) caveman-<action> action keyword match.
	if strings.HasPrefix(name, "caveman-") {
		action := strings.TrimPrefix(name, "caveman-")
		if action != "" && strings.Contains(request, action) {
			return true
		}
		// (3) Chinese action variant match.
		if phrases, ok := cavemanActionChinese[action]; ok {
			for _, phrase := range phrases {
				if strings.Contains(request, phrase) {
					return true
				}
			}
		}
	}
	// (4) Backward compat: survey heuristic for skills tagged "survey".
	joined := strings.ToLower(strings.Join([]string{listing.Description, listing.WhenToUse}, " "))
	if strings.Contains(joined, "survey") && (strings.Contains(request, "analy") || strings.Contains(request, "survey") || strings.Contains(request, "refactor")) {
		return true
	}
	return false
}

func skillAllowsTool(definition skills.Definition, toolName string) bool {
	for _, allowedTool := range definition.AllowedTools {
		if strings.EqualFold(strings.TrimSpace(allowedTool), toolName) {
			return true
		}
	}
	return false
}

func buildLLMSummaryRequest(bundle prompt.Bundle, plan planner.Plan, readSummary string, generatedSkillPath string, generatedSkillPreview string, invokedSkillName string, restored bool, activeSkill *skills.Definition, personality PersonalityConfig, roleSkillBody string) llm.Request {
	systemSections := append([]string{}, bundle.StablePrefix...)
	// T8.5: Inject role-specific skill template when available.
	if strings.TrimSpace(roleSkillBody) != "" {
		systemSections = append(systemSections, roleSkillBody)
	}
	// Inject operator personality (language / style / context) once via shared helper.
	if frag := personalityPromptFragment(personality); frag != "" {
		systemSections = append(systemSections, frag)
	}
	// TODO-04 (P1): activeSkill.Body must reach the avatar LLM as
	// multi-line, full-body evidence so the avatar can follow the skill's
	// playbook. Previously the skill name was passed via invokedSkillName
	// but the body never appeared in the prompt, so the avatar followed
	// general contract text instead of the skill's specific guidance.
	if activeSkill != nil {
		if section := formatActiveSkillSection(activeSkill); section != "" {
			systemSections = append(systemSections, section)
		}
	}
	systemSections = append(systemSections,
		"You are the runtime synthesizer for the avatars CLI.",
		synthesizerAnalysisLanguageLine(personality),
		// F61: user-facing delivery summary first — not a repository audit report.
		"Answer the operator's request first. Lead with what was delivered (or what failed), concrete paths, and the next action.",
		"Use clear markdown with sections IN THIS ORDER: Delivery Summary, Status, What Changed, How to Verify, Next Steps.",
		"Optional short Evidence Notes only if needed; do NOT lead with Evidence Reviewed / Evidence Coverage tables.",
		"Do not title the report 'Repository Analysis' for implementation runs.",
		"Only put a claim under Status as green when build/test evidence says so. If build_ok=false or post_build_failed appears in context, Status must be needs_remediation / failed and Next Steps must say how to fix.",
		"Do not make time-sensitive claims such as latest language versions, current releases, package freshness, or platform support unless the evidence summary explicitly contains that fact.",
		"Do not invent tools, files, or outcomes that were not explicitly provided.",
		// F70: never invent paths (e.g. internal/<lib>/…) that are absent from AUTHORITATIVE ON-DISK SOURCE PATHS.
		"In What Changed / file lists, cite ONLY paths that appear in AUTHORITATIVE ON-DISK SOURCE PATHS or Builder changed_files. Never invent internal/<pkg>/, pkg/, or parallel trees that are not on disk.",
		"Honor repository_gap_evidence (Repository Gap Evidence): never claim a referenced file exists when the gap list says it was not found.",
		// V5/F61: never claim an empty repo when Builder already wrote sources.
		"If disk_inventory or changed_files lists source paths, you MUST NOT claim that no source code, configuration, tests, or manifests exist, and MUST NOT say empty delivery.",
		"Before any coding or mutating action, Builder must declare intent, expected_targets, and verification command; if expected targets are unclear, remain in plan mode/read-only and ask for one clarifying constraint.",
		"After any mutating action, closure must cite changed_files plus the verifier result; expected_targets are intended scope, changed_files remains the observed mutation evidence.",
	)

	userSections := make([]string, 0, len(bundle.DynamicSections)+6)
	for _, section := range bundle.DynamicSections {
		content := strings.TrimSpace(section.Content)
		if content == "" {
			continue
		}
		userSections = append(userSections, fmt.Sprintf("%s: %s", section.Name, content))
	}
	userSections = append(userSections,
		fmt.Sprintf("plan_summary: %s", plan.Summary),
		fmt.Sprintf("avatar_count: %d", len(plan.Avatars)),
		fmt.Sprintf("workflow_node_count: %d", len(plan.Nodes)),
		fmt.Sprintf("evidence_coverage: %s", evidenceCoverageSummaryLine(readSummary)),
		fmt.Sprintf("read_summary: %s", buildSynthesisEvidenceDigest(readSummary, 3)),
		fmt.Sprintf("repository_gap_evidence: %s", reportRepositoryGapEvidence(readSummary)),
	)
	if inv := extractDiskInventoryLine(readSummary); inv != "" {
		userSections = append(userSections, "disk_inventory: "+inv)
	}
	if cf := extractChangedFilesLine(readSummary); cf != "" {
		userSections = append(userSections, "changed_files: "+cf)
	}
	if wrote := extractThisRunWroteSourcesLine(readSummary); wrote != "" {
		userSections = append(userSections, "this_run_wrote_sources: "+wrote)
		userSections = append(userSections, "summary_authority: this_run_wrote_sources and changed_files override any prior Delivery Summary")
	} else if strings.Contains(readSummary, "THIS RUN DID NOT WRITE IMPLEMENTATION SOURCES") {
		userSections = append(userSections, "this_run_wrote_sources: none")
	}
	userSections = append(userSections,
		"synthesizer_tools: none this turn (no write/edit). Do not claim precise_edit or edit_file was applied. If CURRENT PROJECT HEALTH is RED, do not say the compile/test issue is already fixed.")
	if plan.Remediation != nil {
		if plan.Remediation.CurrentVerification != "" || plan.Remediation.CurrentVerificationSummary != "" {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_current: %s | %s", plan.Remediation.CurrentVerification, plan.Remediation.CurrentVerificationSummary))
		}
		if plan.Remediation.LatestNonPassVerification != "" || plan.Remediation.TaskScopedFollowUp != "" || plan.Remediation.ReverifyStatus != "" {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_follow_up: %s | %s | %s", plan.Remediation.LatestNonPassVerification, plan.Remediation.TaskScopedFollowUp, plan.Remediation.ReverifyStatus))
		}
		if plan.Remediation.LatestRemediation != "" || plan.Remediation.LatestGuardedRemediation != "" {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_attempts: %s | %s", plan.Remediation.LatestRemediation, plan.Remediation.LatestGuardedRemediation))
		}
		if plan.Remediation.GuardedRemediationStatus != "" || plan.Remediation.GuardedRemediationAuthority != "" || plan.Remediation.GuardedRemediationSource != "" {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_guard: %s | %s | %s", plan.Remediation.GuardedRemediationStatus, plan.Remediation.GuardedRemediationAuthority, plan.Remediation.GuardedRemediationSource))
		}
		if plan.Remediation.FailedNodeRetryClosureStatus != "" || plan.Remediation.FailedNodeRetryClosureReady != "" || plan.Remediation.FailedNodeRetryClosureVerifierGate != "" {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_retry_closure: %s | %s | %s | %s | %s", plan.Remediation.FailedNodeRetryClosureStatus, plan.Remediation.FailedNodeRetryClosureReady, plan.Remediation.FailedNodeRetryClosureVerifierGate, plan.Remediation.FailedNodeRetryClosureVerifierVerdict, plan.Remediation.FailedNodeRetryClosureReport))
		}
		if plan.Remediation.RecoverySummaryCategory != "" || plan.Remediation.RecoverySummaryAction != "" || plan.Remediation.RecoverySummaryGuardStatus != "" || plan.Remediation.RecoverySummaryGuidance != "" {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_recovery: %s | %s | %s | %s", plan.Remediation.RecoverySummaryCategory, plan.Remediation.RecoverySummaryAction, plan.Remediation.RecoverySummaryGuardStatus, plan.Remediation.RecoverySummaryGuidance))
		}
		if len(plan.Remediation.ExpectedTargets) > 0 {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_expected_targets: %s", strings.Join(plan.Remediation.ExpectedTargets, ", ")))
		}
		if len(plan.Remediation.Proposals) > 0 {
			userSections = append(userSections, fmt.Sprintf("plan_remediation_proposal_count: %d", len(plan.Remediation.Proposals)))
			for index, proposal := range plan.Remediation.Proposals {
				fields := []string{proposal.ProposalID, proposal.Kind, proposal.Summary, proposal.Intent}
				if len(proposal.ExpectedTargets) > 0 {
					fields = append(fields, strings.Join(proposal.ExpectedTargets, ", "))
				}
				if proposal.FollowUpCommand != "" {
					fields = append(fields, proposal.FollowUpCommand)
				}
				userSections = append(userSections, fmt.Sprintf("plan_remediation_proposal_%d: %s", index+1, strings.Join(fields, " | ")))
			}
		}
	}
	if generatedSkillPath != "" {
		userSections = append(userSections, fmt.Sprintf("generated_skill_path: %s", generatedSkillPath))
	}
	if generatedSkillPreview != "" {
		userSections = append(userSections, fmt.Sprintf("generated_skill_preview: %s", generatedSkillPreview))
	}
	if invokedSkillName != "" {
		userSections = append(userSections, fmt.Sprintf("invoked_skill_name: %s", invokedSkillName))
	}
	userSections = append(userSections, fmt.Sprintf("continuation: %t", restored))

	return llm.Request{
		SystemPrompt: strings.Join(systemSections, "\n"),
		UserPrompt:   strings.Join(userSections, "\n"),
		ThinkMode:    llm.ThinkModeOff, // F37: synthesis needs answer text, not long CoT
	}
}

// formatActiveSkillSection renders an invoked skill as a multi-line
// system section. The body is preserved verbatim (no `oneLine`/1200-char
// collapse) so the avatar LLM can follow the skill's specific playbook.
// Truncation only kicks in at 16000 chars as a last-resort safety valve;
// a marker is appended so the avatar notices the cutoff.
func formatActiveSkillSection(skill *skills.Definition) string {
	if skill == nil {
		return ""
	}
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		return ""
	}
	var lines []string
	lines = append(lines, "invoked-skill: "+name)
	if context := strings.TrimSpace(skill.Context); context != "" {
		lines = append(lines, "context:")
		lines = append(lines, strings.TrimRight(context, "\n"))
	}
	body := strings.TrimSpace(skill.Body)
	if body != "" {
		lines = append(lines, "rules:")
		lines = append(lines, capSkillBodyForPrompt(body, 16000))
	}
	return strings.Join(lines, "\n")
}

func capSkillBodyForPrompt(body string, limit int) string {
	if limit <= 0 || len(body) <= limit {
		return strings.TrimRight(body, "\n")
	}
	return body[:limit] + "\n[truncated: active skill body exceeded " + fmt.Sprintf("%d", limit) + " chars]"
}

func plannerRemediationEnvelope(taskID string, snapshot memstore.Snapshot) *planner.RemediationEnvelope {
	return memstore.RemediationEnvelope(taskID, snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords)
}

func plannerRemediationPayload(remediation *planner.RemediationEnvelope) map[string]any {
	if remediation == nil {
		return nil
	}
	payload := map[string]any{}
	if remediation.CurrentVerification != "" {
		payload["current_verification"] = remediation.CurrentVerification
	}
	if remediation.CurrentVerificationSummary != "" {
		payload["current_verification_summary"] = remediation.CurrentVerificationSummary
	}
	if remediation.LatestNonPassVerification != "" {
		payload["latest_non_pass_verification"] = remediation.LatestNonPassVerification
	}
	if remediation.TaskScopedFollowUp != "" {
		payload["task_scoped_follow_up"] = remediation.TaskScopedFollowUp
	}
	if remediation.ReverifyStatus != "" {
		payload["reverify_status"] = remediation.ReverifyStatus
	}
	if remediation.LatestRemediation != "" {
		payload["latest_remediation"] = remediation.LatestRemediation
	}
	if remediation.LatestGuardedRemediation != "" {
		payload["latest_guarded_remediation"] = remediation.LatestGuardedRemediation
	}
	if remediation.GuardedRemediationStatus != "" {
		payload["guarded_remediation_status"] = remediation.GuardedRemediationStatus
	}
	if remediation.GuardedRemediationAuthority != "" {
		payload["guarded_remediation_authority"] = remediation.GuardedRemediationAuthority
	}
	if remediation.GuardedRemediationSource != "" {
		payload["guarded_remediation_source"] = remediation.GuardedRemediationSource
	}
	if remediation.VerifierRemediationClosureStatus != "" {
		payload["verifier_remediation_closure_status"] = remediation.VerifierRemediationClosureStatus
	}
	if remediation.VerifierRemediationClosure != "" {
		payload["verifier_remediation_closure"] = remediation.VerifierRemediationClosure
	}
	if remediation.VerifierRemediationClosureReport != "" {
		payload["verifier_remediation_closure_report"] = remediation.VerifierRemediationClosureReport
	}
	if remediation.VerifierRemediationClosurePausePoint != "" {
		payload["verifier_remediation_closure_pause_point"] = remediation.VerifierRemediationClosurePausePoint
	}
	if remediation.VerifierRemediationClosureResume != "" {
		payload["verifier_remediation_closure_resume_attempt"] = remediation.VerifierRemediationClosureResume
	}
	if remediation.FailedNodeRetryClosureStatus != "" {
		payload["failed_node_retry_closure_status"] = remediation.FailedNodeRetryClosureStatus
	}
	if remediation.FailedNodeRetryClosureReady != "" {
		payload["failed_node_retry_closure_ready"] = remediation.FailedNodeRetryClosureReady
	}
	if remediation.FailedNodeRetryClosureVerifierGate != "" {
		payload["failed_node_retry_closure_verifier_gate_status"] = remediation.FailedNodeRetryClosureVerifierGate
	}
	if remediation.FailedNodeRetryClosureVerifierVerdict != "" {
		payload["failed_node_retry_closure_verifier_verdict"] = remediation.FailedNodeRetryClosureVerifierVerdict
	}
	if remediation.FailedNodeRetryClosureReport != "" {
		payload["failed_node_retry_closure_verification_report_path"] = remediation.FailedNodeRetryClosureReport
	}
	if remediation.RecoverySummaryCategory != "" {
		payload["recovery_summary_category"] = remediation.RecoverySummaryCategory
	}
	if remediation.RecoverySummaryAction != "" {
		payload["recovery_summary_action"] = remediation.RecoverySummaryAction
	}
	if remediation.RecoverySummaryGuardStatus != "" {
		payload["recovery_summary_guard_status"] = remediation.RecoverySummaryGuardStatus
	}
	if remediation.RecoverySummaryGuidance != "" {
		payload["recovery_summary_guidance"] = remediation.RecoverySummaryGuidance
	}
	if len(remediation.ExpectedTargets) > 0 {
		payload["expected_targets"] = append([]string(nil), remediation.ExpectedTargets...)
	}
	if len(remediation.Proposals) > 0 {
		proposals := make([]map[string]any, 0, len(remediation.Proposals))
		for _, proposal := range remediation.Proposals {
			proposalPayload := map[string]any{}
			if proposal.ProposalID != "" {
				proposalPayload["proposal_id"] = proposal.ProposalID
			}
			if proposal.Kind != "" {
				proposalPayload["kind"] = proposal.Kind
			}
			if proposal.Summary != "" {
				proposalPayload["summary"] = proposal.Summary
			}
			if proposal.Intent != "" {
				proposalPayload["intent"] = proposal.Intent
			}
			if len(proposal.ExpectedTargets) > 0 {
				proposalPayload["expected_targets"] = append([]string(nil), proposal.ExpectedTargets...)
			}
			if proposal.FollowUpCommand != "" {
				proposalPayload["follow_up_command"] = proposal.FollowUpCommand
			}
			if len(proposalPayload) > 0 {
				proposals = append(proposals, proposalPayload)
			}
		}
		if len(proposals) > 0 {
			payload["proposals"] = proposals
		}
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func plannerVerificationSnapshotSummary(snapshot memstore.VerificationSnapshot) string {
	updatedAt := "n/a"
	if !snapshot.UpdatedAt.IsZero() {
		updatedAt = snapshot.UpdatedAt.Format(time.RFC3339)
	}
	tool := strings.TrimSpace(snapshot.Tool)
	if tool == "" {
		tool = "n/a"
	}
	verdict := strings.TrimSpace(snapshot.Verdict)
	if verdict == "" {
		verdict = "n/a"
	}
	return fmt.Sprintf("%s | %s | %s", updatedAt, tool, verdict)
}

type workflowNodeWorkContext struct {
	runID                 string
	taskID                string
	nodeID                string
	nodeRole              string
	nodeTitle             string
	plan                  planner.Plan
	workflowState         *WorkflowState
	input                 string
	readSummary           string
	readTargets           []string
	explorationRounds     []explorationRound
	hasReadTarget         bool
	researcherAvatarID    string
	builderAvatarID       string
	criticAvatarID        string
	activeSkill           *skills.Definition
	alwaysOnSkills        []skills.Definition // S3.9: role-filtered at node execution
	generatedSkillPath    string
	generatedSkillPreview string

	// StructuredContext carries typed declarations (interfaces, types,
	// functions) extracted from source files the Researcher read. This
	// eliminates LLM guesswork when the Builder generates code.
	structuredContext *StructuredContext

	// planReviewFindings carries pre-build plan review results (I21).
	// Set by ReviewPlanBeforeBuild in executeBuilderNodeWork; injected
	// into Builder's system prompt so it can address gaps before writing code.
	planReviewFindings string

	customSystemPrompt string // WR-Fix-1: override Builder system prompt
}

type workflowNodeWorkResult struct {
	readSummary               string
	researcherReportArtifacts []ArtifactRef
	artifactIDs               []string
	generatedSkillPath        string
	generatedSkillPreview     string
	criticChallenge           string
	pausePoint                PausePoint
	pauseErr                  error
}

func (e *Engine) executeWorkflowNodeWork(ctx context.Context, node WorkflowNodeRuntime, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	work.nodeID = strings.TrimSpace(node.ID)
	work.nodeRole = strings.TrimSpace(node.AssignedRole)
	work.nodeTitle = strings.TrimSpace(node.Title)
	switch strings.TrimSpace(node.AssignedRole) {
	case "Planner":
		// S4.6 / C3: Planner DAG node is not on the default plan. CriticHub
		// may still enqueue a replan slot after executeReplan.
		return e.executePlannerNodeWork(ctx, work)
	case "Researcher":
		return e.executeResearcherNodeWork(ctx, work)
	case "Builder":
		return e.executeBuilderNodeWork(ctx, work)
	case "Critic":
		return e.executeCriticNodeWork(ctx, work)
	case "Synthesizer":
		// S4.7 / C3: default DAG omits Synthesizer; real LLM synthesis stays at
		// run tail. This case remains for leftover/custom plans only.
		return e.executeSynthesizerNodeWork(ctx, work)
	case "Direct":
		return e.executeDirectNodeWork(ctx, work)
	case "Runner":
		return e.executeRunnerNodeWork(ctx, work)
	default:
		return workflowNodeWorkResult{}, nil
	}
}

// executeDocEditor handles documentation-only tasks (md, yaml, txt).
// It reads the target file, passes it with the task to the LLM for targeted
// edits, and writes back the result. No project code files are read.
func (e *Engine) executeDocEditor(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	targetPath := extractFilePathFromInput(work.input)
	if targetPath == "" {
		return workflowNodeWorkResult{}, fmt.Errorf("doc editor: could not determine target file from task")
	}

	// Missing files are created (empty current content). Directory paths are not.
	existing, err := os.ReadFile(targetPath)
	if err != nil {
		if isDirectoryReadError(err) {
			return workflowNodeWorkResult{}, fmt.Errorf("doc editor: read %s: %w", targetPath, err)
		}
		if !isSkippableSurveyReadError(err) {
			return workflowNodeWorkResult{}, fmt.Errorf("doc editor: read %s: %w", targetPath, err)
		}
		existing = nil
	}

	// Build a doc-specific prompt with the file content inline.
	llmCtx, llmCancel := context.WithTimeout(ctx, 120*time.Second)
	defer llmCancel()
	response, llmErr := e.llm.Generate(llmCtx, llm.Request{
		SystemPrompt: appendPersonality("You are a document editor. Apply ONLY the changes requested by the user to the file content below. Keep everything else EXACTLY as-is. Return the COMPLETE modified file. Output format: FILE: <path> on the first line, then the complete file content. No markdown fences, no explanations.", e.personality),
		UserPrompt:   fmt.Sprintf("TASK:\n%s\n\nCURRENT FILE (%s):\n```\n%s\n```\n\nApply the task's changes to this file. Return the COMPLETE modified file.", work.input, targetPath, string(existing)),
	})
	if llmErr != nil {
		return workflowNodeWorkResult{}, llmErr
	}
	if strings.TrimSpace(response.Text) == "" {
		return workflowNodeWorkResult{}, fmt.Errorf("doc editor: LLM returned empty response")
	}

	writePath, content := parseDirectActionResponse(response.Text, targetPath)
	if writePath == "" {
		writePath = targetPath
	}
	if strings.TrimSpace(content) == "" {
		return workflowNodeWorkResult{}, fmt.Errorf("doc editor: LLM returned empty content")
	}

	workingDir, _ := os.Getwd()
	writeInput := tools.WriteInput{
		Path:            writePath,
		Content:         content,
		WorkingDir:      workingDir,
		Overwrite:       true,
		Intent:          fmt.Sprintf("DocEditor: edit %s", filepath.Base(writePath)),
		ExpectedTargets: []string{writePath},
	}
	if _, err := e.invokeToolWithOptions(ctx, toolCallEnvelope{runID: work.runID, taskID: work.taskID, avatarID: "avatar-direct", phase: "executing"}, "write", "file_write", writeInput, toolInvokeOptions{skipVerification: true}); err != nil {
		return workflowNodeWorkResult{}, err
	}

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "doc_editor.completed", "runtime", map[string]any{"path": writePath, "size": len(content)}, nil)
	return workflowNodeWorkResult{artifactIDs: []string{"doc-editor-output"}}, nil
}

// workflowDocLLMTimeoutFloor is the minimum wall-clock budget for filling
// plan/phase markdown. Thinking models (DeepSeek/Kimi/etc.) can burn a short
// budget entirely on reasoning_content and return empty answer text.
const workflowDocLLMTimeoutFloor = 180 * time.Second

// workflowDocLLMTimeout returns PlanAutoFill with a cross-provider floor.
func workflowDocLLMTimeout() time.Duration {
	t := llm.TimeoutConfigOrDefault().PlanAutoFill
	if t < workflowDocLLMTimeoutFloor {
		return workflowDocLLMTimeoutFloor
	}
	return t
}

// makeWorkflowGenerate wraps the engine LLM for workflow ConfirmPlan /
// ConstructPlan / ConstructPhase / ExpandActivePhaseDetail calls.
//
// Cross-provider: ThinkModeOff so wall-clock goes to answer content (not CoT).
// Emits workflow.llm.* so ConstructPlan is not a pre-run.started dark box.
func (e *Engine) makeWorkflowGenerate(ctx context.Context, timeout time.Duration, runID, taskID, purpose string) workflow.GenerateFunc {
	if e == nil || e.llm == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = workflowDocLLMTimeout()
	}
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		purpose = "workflow_llm"
	}
	return func(sys, usr string) (string, error) {
		startedPayload := map[string]any{
			"purpose":    purpose,
			"timeout_ms": timeout.Milliseconds(),
			"think_mode": "off",
		}
		mergePromptObservability(startedPayload, "Planner", sys, usr)
		_ = e.emit(runID, taskID, "", "planning", "workflow.llm.started", "runtime", startedPayload, nil)
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var contentChars, thinkingChars int
		req := llm.Request{
			SystemPrompt: sys,
			UserPrompt:   usr,
			// Structured workflow docs must not spend the timeout on CoT —
			// applies to DeepSeek, Kimi, and any future thinking providers.
			ThinkMode: llm.ThinkModeOff,
			StreamCallback: func(chunk string) {
				contentChars += len(chunk)
			},
			ThinkingCallback: func(chunk string) {
				thinkingChars += len(chunk)
			},
		}
		started := time.Now()
		resp, err := e.llm.Generate(cctx, req)
		elapsed := time.Since(started).Milliseconds()
		if err != nil {
			failPayload := map[string]any{
				"purpose":        purpose,
				"error":          err.Error(),
				"elapsed_ms":     elapsed,
				"content_chars":  contentChars,
				"thinking_chars": thinkingChars,
			}
			mergePromptObservability(failPayload, "Planner", sys, usr)
			_ = e.emit(runID, taskID, "", "planning", "workflow.llm.failed", "runtime", failPayload, nil)
			return "", err
		}
		text := strings.TrimSpace(resp.Text)
		if text == "" {
			err = fmt.Errorf("workflow LLM returned empty content (purpose=%s, thinking_chars=%d, elapsed_ms=%d)",
				purpose, thinkingChars, elapsed)
			failPayload := map[string]any{
				"purpose":        purpose,
				"error":          err.Error(),
				"elapsed_ms":     elapsed,
				"content_chars":  contentChars,
				"thinking_chars": thinkingChars,
			}
			mergePromptObservability(failPayload, "Planner", sys, usr)
			_ = e.emit(runID, taskID, "", "planning", "workflow.llm.failed", "runtime", failPayload, nil)
			return "", err
		}
		donePayload := map[string]any{
			"purpose":        purpose,
			"elapsed_ms":     elapsed,
			"content_chars":  len(text),
			"thinking_chars": thinkingChars,
		}
		mergePromptObservability(donePayload, "Planner", sys, usr)
		_ = e.emit(runID, taskID, "", "planning", "workflow.llm.completed", "runtime", donePayload, nil)
		return text, nil
	}
}

// executePlanConstructor fills avatars_plan.md from a user requirement using
// the workflow.ConstructPlan function. This is the W2 workflow path.
func (e *Engine) executePlanConstructor(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	if e.llm == nil {
		return workflowNodeWorkResult{}, fmt.Errorf("plan constructor: no LLM client available")
	}

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "plan_constructor.started", "runtime", map[string]any{"input": work.input}, nil)

	// Extract the requirement from the task input.
	// The input looks like: "In docs/workflow/avatars_plan.md: fill in the plan for X"
	requirement := extractRequirementFromTask(work.input)

	planContent, err := workflow.ConstructPlan(".", requirement, func(sysPrompt, userPrompt string) (string, error) {
		// Keep PlannerStablePrefix first and byte-identical (provider prefix cache).
		// Skill body, structured context, and plan feedback are user-tail — they
		// change across retries/files and must not sit in front of the system prefix.
		plannerProposal := skillbuilder.BuildForRole("Planner", work.input, work.plan, work.readSummary)
		var psc string
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if ctx := extractStructuredContext("", collectGoFilePaths(wd)); ctx != nil && !ctx.Empty() {
				psc = formatStructuredContextForPrompt(ctx)
			}
		}
		sysPrompt, userPrompt = assemblePlannerLLMPrompts(sysPrompt, userPrompt, plannerProposal.Body, work.plan.Feedback, psc)
		sysPrompt = appendAlwaysOnRoleSkills(sysPrompt, work.alwaysOnSkills, "planner")
		planReq := llm.Request{
			SystemPrompt: sysPrompt,
			UserPrompt:   userPrompt,
			ThinkMode:    llm.ThinkModeOff,
		}
		e.emitLLMPromptCost(work.runID, work.taskID, "avatar-direct", "executing", "Planner", planReq)
		llmCtx, llmCancel := context.WithTimeout(ctx, workflowDocLLMTimeout())
		defer llmCancel()
		response, llmErr := e.llm.Generate(llmCtx, planReq)
		if llmErr != nil {
			return "", llmErr
		}
		return response.Text, nil
	})
	if err != nil {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "plan_constructor.failed", "runtime", map[string]any{"error": err.Error()}, nil)
		return workflowNodeWorkResult{}, fmt.Errorf("plan constructor: %w", err)
	}

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "plan_constructor.completed", "runtime", map[string]any{"plan_size": len(planContent)}, nil)

	// Emit the constructed plan with confirmation instructions.
	confirmMsg := workflow.BuildConfirmPromptMessage(planContent)
	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "plan_constructor.report", "runtime", map[string]any{
		"summary":    "Project plan constructed. Review docs/workflow/avatars_plan.md and confirm before proceeding.",
		"plan":       planContent,
		"next_steps": confirmMsg,
	}, nil)

	return workflowNodeWorkResult{artifactIDs: []string{"plan-constructor-output"}}, nil
}

// executePhaseConstructor fills a phaseN.md detail file from the project plan.
// This is the W3 workflow path.
func (e *Engine) executePhaseConstructor(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	if e.llm == nil {
		return workflowNodeWorkResult{}, fmt.Errorf("phase constructor: no LLM client available")
	}

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "phase_constructor.started", "runtime", map[string]any{"input": work.input}, nil)

	// Determine phase number from task input.
	phaseNum := extractPhaseNumFromTask(work.input)

	phaseContent, err := workflow.ConstructPhase(".", phaseNum, func(sysPrompt, userPrompt string) (string, error) {
		phaseReq := llm.Request{
			SystemPrompt: sysPrompt,
			UserPrompt:   userPrompt,
			ThinkMode:    llm.ThinkModeOff,
		}
		e.emitLLMPromptCost(work.runID, work.taskID, "avatar-direct", "executing", "Planner", phaseReq)
		llmCtx, llmCancel := context.WithTimeout(ctx, workflowDocLLMTimeout())
		defer llmCancel()
		response, llmErr := e.llm.Generate(llmCtx, phaseReq)
		if llmErr != nil {
			return "", llmErr
		}
		return response.Text, nil
	})
	if err != nil {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "phase_constructor.failed", "runtime", map[string]any{"error": err.Error()}, nil)
		return workflowNodeWorkResult{}, fmt.Errorf("phase constructor: %w", err)
	}

	// After constructing the phase, populate the todo.
	if syncErr := workflow.PopulateTodoFromPhase(".", phaseNum); syncErr != nil {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "phase_constructor.warning", "runtime", map[string]any{"warning": fmt.Sprintf("Phase generated but todo sync failed: %s", syncErr.Error())}, nil)
	}

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "phase_constructor.completed", "runtime", map[string]any{"phase_num": phaseNum, "phase_size": len(phaseContent)}, nil)

	return workflowNodeWorkResult{artifactIDs: []string{"phase-constructor-output"}}, nil
}

// extractRequirementFromTask pulls the core requirement from a plan construction task.
// Input: "In docs/workflow/avatars_plan.md: fill in the plan for building a Go calculator"
// Output: "building a Go calculator"
func extractRequirementFromTask(input string) string {
	// Remove file path prefix: "In docs/workflow/avatars_plan.md: "
	s := input
	if idx := strings.Index(s, ".md:"); idx > 0 {
		s = strings.TrimSpace(s[idx+4:])
	} else if idx := strings.Index(s, ".md "); idx > 0 {
		s = strings.TrimSpace(s[idx+4:])
	}

	// Remove construction verbs from the beginning.
	prefixes := []string{
		"fill in the plan for ", "fill in plan for ",
		"construct a plan for ", "construct plan for ",
		"build a plan for ", "build plan for ",
		"create a plan for ", "create plan for ",
		"generate a plan for ", "generate plan for ",
		"write a plan for ", "write plan for ",
		"fill in ", "construct ", "build ", "create ", "generate ", "write ",
	}
	lower := strings.ToLower(s)
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			s = s[len(prefix):]
			break
		}
	}

	if strings.TrimSpace(s) == "" {
		return input // fallback: return original input
	}
	return strings.TrimSpace(s)
}

// extractPhaseNumFromTask extracts the phase number from a phase construction task.
// Input: "generate phase 2" or "construct docs/workflow/phase3.md"
// Returns the phase number, defaulting to 1.
func extractPhaseNumFromTask(input string) int {
	lower := strings.ToLower(input)

	// Try "phase N" pattern.
	for i := 1; i <= 9; i++ {
		if strings.Contains(lower, fmt.Sprintf("phase %d", i)) ||
			strings.Contains(lower, fmt.Sprintf("phase%d", i)) {
			return i
		}
	}

	return 1 // default to phase 1
}

// executeConfirmPlan handles the W6 "confirm the plan" action.
// It transitions the plan from draft to in-progress, generates all phase
// detail files, and populates the todo with phase 1 tasks.
func (e *Engine) executeConfirmPlan(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	// Batch4/K: split Builder nodes share the original confirm input — only
	// the first node may ConfirmPlan; later nodes continue as implement.
	if e.confirmPlanDone {
		implement, lock := e.resolveConfirmImplementPhases(work.input)
		implWork := work
		if implWork.builderAvatarID == "" {
			implWork.builderAvatarID = "avatar-builder"
		}
		implWork.input = formatConfirmImplementInput(implement, lock)
		return e.executeBuilderNodeWork(ctx, implWork)
	}

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "confirm_plan.started", "runtime", map[string]any{"input": work.input}, nil)

	// NL smoke #12: allow confirm without LLM — ConstructMissingPhases will
	// fail-soft via ConfirmPlan degrade when generate errors / is nil.
	var generate workflow.GenerateFunc
	if e.llm != nil {
		generate = e.makeWorkflowGenerate(ctx, workflowDocLLMTimeout(), work.runID, work.taskID, "confirm_plan")
	} else {
		generate = func(sysPrompt, userPrompt string) (string, error) {
			return "", fmt.Errorf("LLM unavailable")
		}
	}

	summary, err := workflow.ConfirmPlan(".", generate)
	if err != nil {
		payload := map[string]any{"error": err.Error()}
		if summary != "" {
			payload["summary"] = summary
		}
		if isProviderBusyError(err.Error()) {
			payload["hint"] = "LLM provider overloaded; retry confirm later or switch model/provider"
			_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "workflow.provider_busy", "runtime", payload, nil)
		}
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "confirm_plan.failed", "runtime", payload, nil)
		return workflowNodeWorkResult{}, fmt.Errorf("confirm plan: %w", err)
	}
	e.confirmPlanDone = true

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "confirm_plan.completed", "runtime", map[string]any{"summary": summary}, nil)

	// NL smoke #5/#11: compound confirm+implement, OR acceptEdits mode (user
	// already opted into writes) → continue into Builder instead of stopping.
	shouldImplement := workflow.WantsImplementAfterConfirm(work.input) ||
		e.permissionMode == PermissionModeAcceptEdits
	if shouldImplement && !strings.Contains(summary, "Phase generation degraded") {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "confirm_plan.continue_implement", "runtime", map[string]any{
			"reason": "confirm+implement or acceptEdits",
		}, nil)
		implWork := work
		if implWork.builderAvatarID == "" {
			implWork.builderAvatarID = "avatar-builder"
		}
		implement, lock := e.resolveConfirmImplementPhases(work.input)
		// Hard-lock only when user said 只做/本轮只要; full-course keeps lock=0.
		e.lockedPhase = lock
		_ = workflow.UpdateActivePhase(".", implement)
		_ = workflow.SetTodoActivePhasePointer(".", implement)
		implWork.input = formatConfirmImplementInput(implement, lock)
		return e.executeBuilderNodeWork(ctx, implWork)
	}

	return workflowNodeWorkResult{artifactIDs: []string{"confirm-plan-output"}, readSummary: summary}, nil
}

// resolveConfirmImplementPhases picks implement target vs hard lock for confirm+implement.
// Full-course prompts keep lock=0 so Critic may advance in the same run (G1).
func (e *Engine) resolveConfirmImplementPhases(input string) (implement, lock int) {
	active := 1
	if planBody, err := workflow.ReadWorkflowDoc(".", "plan"); err == nil {
		active = workflow.ParsePlanMeta(planBody).ActivePhase
	}
	if active < 1 {
		active = 1
	}
	if e != nil && e.lockedPhase > 0 {
		return e.lockedPhase, e.lockedPhase
	}
	implement = workflow.ResolveImplementPhase(input, active)
	lock = workflow.ResolveLockedPhase(input, active)
	if implement < 1 {
		implement = active
	}
	return implement, lock
}

func formatConfirmImplementInput(implement, lock int) string {
	// H1: do not name peer languages (Python/JS/Rust) here — that string was
	// fed to detectTargetLanguage and flipped Go tasks to rust via "Rust".
	base := fmt.Sprintf(
		"Implement Phase %d now. Follow docs/workflow/phase%d.md and avatars_todo.md as the layout source of truth (Critic charter overrides architecture.md on conflicts). Write real source files using the package layout declared in the phase/task — do not invent a parallel tree (no bare root library files, no junk internal/debug or tmp/). Typical shapes: <lib>/ + cmd/<name>/, or app/, or src/ — only use internal/ for truly private packages. Do NOT edit markdown plan/phase docs. Do NOT create docs/workflow/ inside package directories. Do NOT nest package/package/.",
		implement, implement,
	)
	if lock > 0 {
		return fmt.Sprintf("PHASE LOCK Phase %d (do not advance past this phase). %s", lock, base)
	}
	return fmt.Sprintf("Active Phase %d (full-course allowed after Critic verifies). %s", implement, base)
}

// executeMarkTaskDone handles the W10 "mark task X as done" action.
// It marks a specific todo checklist item as [x] by substring match.
func (e *Engine) executeMarkTaskDone(ctx context.Context, work workflowNodeWorkContext, taskDesc string) (workflowNodeWorkResult, error) {
	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "mark_task.started", "runtime", map[string]any{"task": taskDesc}, nil)

	matched, err := workflow.MarkTaskAsDone(".", taskDesc)
	if err != nil {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "mark_task.failed", "runtime", map[string]any{"error": err.Error()}, nil)
		return workflowNodeWorkResult{}, fmt.Errorf("mark task: %w", err)
	}

	if matched {
		completed, total := workflow.CountTodoProgress(".")
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "mark_task.completed", "runtime", map[string]any{
			"task":     taskDesc,
			"progress": fmt.Sprintf("%d/%d", completed, total),
			"summary":  fmt.Sprintf("Task marked as done: %s (%d/%d complete)", taskDesc, completed, total),
		}, nil)
	} else {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "mark_task.not_found", "runtime", map[string]any{
			"task":    taskDesc,
			"summary": fmt.Sprintf("No matching todo item found for: %s", taskDesc),
		}, nil)
	}

	return workflowNodeWorkResult{artifactIDs: []string{"mark-task-output"}}, nil
}

// executeReplan handles the W6b "replan" action.
// It regenerates the plan from a new/changed requirement.
func (e *Engine) executeReplan(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	if e.llm == nil {
		return workflowNodeWorkResult{}, fmt.Errorf("replan: no LLM client available")
	}

	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "replan.started", "runtime", map[string]any{"input": work.input}, nil)

	// Extract the new requirement from the task.
	requirement := extractRequirementFromTask(work.input)

	planContent, err := workflow.ConstructPlan(".", requirement, func(sysPrompt, userPrompt string) (string, error) {
		plannerProposal := skillbuilder.BuildForRole("Planner", work.input, work.plan, work.readSummary)
		var psc string
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if ctx := extractStructuredContext("", collectGoFilePaths(wd)); ctx != nil && !ctx.Empty() {
				psc = formatStructuredContextForPrompt(ctx)
			}
		}
		sysPrompt, userPrompt = assemblePlannerLLMPrompts(sysPrompt, userPrompt, plannerProposal.Body, work.plan.Feedback, psc)
		sysPrompt = appendAlwaysOnRoleSkills(sysPrompt, work.alwaysOnSkills, "planner")
		replanReq := llm.Request{
			SystemPrompt: sysPrompt,
			UserPrompt:   userPrompt,
			ThinkMode:    llm.ThinkModeOff,
		}
		e.emitLLMPromptCost(work.runID, work.taskID, "avatar-direct", "executing", "Planner", replanReq)
		llmCtx, llmCancel := context.WithTimeout(ctx, workflowDocLLMTimeout())
		defer llmCancel()
		response, llmErr := e.llm.Generate(llmCtx, replanReq)
		if llmErr != nil {
			return "", llmErr
		}
		return response.Text, nil
	})
	if err != nil {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "replan.failed", "runtime", map[string]any{"error": err.Error()}, nil)
		return workflowNodeWorkResult{}, fmt.Errorf("replan: %w", err)
	}

	// Emit the confirmation prompt for the new plan.
	confirmMsg := workflow.BuildConfirmPromptMessage(planContent)
	_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "replan.completed", "runtime", map[string]any{
		"summary":    "Plan regenerated. Review and confirm to proceed.",
		"plan":       planContent,
		"next_steps": confirmMsg,
	}, nil)

	// C-2: Inject new plan nodes into the running workflow DAG so the
	// scheduler picks them up. This makes replan actually affect the
	// current run, not just generate a document for the next run.
	if work.workflowState != nil {
		newPlan := planner.Build(requirement)
		injectedCount := 0
		for _, node := range newPlan.Nodes {
			nodeID := strings.TrimSpace(node.ID)
			if nodeID == "" {
				continue
			}
			// Skip nodes that already exist in the DAG.
			if existing := work.workflowState.Node(nodeID); existing.ID != "" {
				continue
			}
			runtimeNode := WorkflowNodeRuntime{
				ID:           nodeID,
				Title:        strings.TrimSpace(node.Title),
				AssignedRole: strings.TrimSpace(node.AssignedRole),
				DependsOn:    cleanWorkflowIDs(node.DependsOn),
			}
			if err := work.workflowState.AddNode(runtimeNode); err == nil {
				injectedCount++
			}
		}
		if injectedCount > 0 {
			_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "replan.nodes_injected", "runtime", map[string]any{
				"injected_count":  injectedCount,
				"total_new_nodes": len(newPlan.Nodes),
			}, nil)
		}
	}

	return workflowNodeWorkResult{artifactIDs: []string{"replan-output"}}, nil
}

func workflowNodeArtifactIDs(node WorkflowNodeRuntime) []string {
	if len(node.ArtifactIDs) > 0 {
		return append([]string(nil), node.ArtifactIDs...)
	}
	id := strings.TrimSpace(node.ID)
	if id == "" {
		return []string{}
	}
	return []string{id}
}

func workflowNodeByRole(plan planner.Plan, role string) planner.WorkflowNode {
	for _, node := range plan.Nodes {
		if strings.EqualFold(strings.TrimSpace(node.AssignedRole), strings.TrimSpace(role)) {
			return node
		}
	}
	return planner.WorkflowNode{}
}

type nodeVerifierGate struct {
	Status     string
	Verdict    string
	ReportPath string
	Verified   bool
}

func (e *Engine) latestNodeVerifierGate(runID string, taskID string, toolName string, requiresVerification bool) nodeVerifierGate {
	if !requiresVerification {
		return nodeVerifierGate{Status: "not_required", Verified: true}
	}
	if e == nil || e.memory == nil || strings.TrimSpace(taskID) == "" {
		return nodeVerifierGate{Status: "pending_verification", Verified: false}
	}
	snapshot, err := e.memory.LoadLatestTaskSnapshot(taskID)
	if err != nil {
		return nodeVerifierGate{Status: "pending_verification", Verified: false}
	}
	record := memstore.LatestRunVerifierVerdictEvaluation(snapshot.EvaluationRecords, runID)
	if record == nil {
		return nodeVerifierGate{Status: "pending_verification", Verified: false}
	}
	if strings.TrimSpace(toolName) != "" && strings.TrimSpace(record.Tool) != strings.TrimSpace(toolName) {
		return nodeVerifierGate{Status: "pending_verification", Verified: false}
	}
	verdict := strings.TrimSpace(record.Verdict)
	gate := nodeVerifierGate{
		Status:     "pending_verification",
		Verdict:    verdict,
		ReportPath: strings.TrimSpace(record.ReportPath),
		Verified:   false,
	}
	switch verdict {
	case string(verification.VerdictPass):
		gate.Status = "verified_pass"
		gate.Verified = true
	case string(verification.VerdictPartial):
		gate.Status = "verified_partial"
	case string(verification.VerdictFail):
		gate.Status = "blocked_fail"
	default:
		if verdict != "" {
			gate.Status = "verifier_" + strings.ToLower(verdict)
		}
	}
	return gate
}

func artifactRefIDs(artifacts []ArtifactRef) []string {
	if len(artifacts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if id := strings.TrimSpace(artifact.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return cleanWorkflowIDs(ids)
}

// artifactPaths returns the file paths from Researcher artifacts.
// Used to feed structured context extraction after the Researcher
// completes its file survey.
func artifactPaths(artifacts []ArtifactRef) []string {
	paths := make([]string, 0, len(artifacts))
	seen := map[string]bool{}
	for _, a := range artifacts {
		p := strings.TrimSpace(a.Path)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		// Only include source files (skip markdown/docs for
		// interface extraction).
		if looksLikeSourceFile(p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// looksLikeSourceFile returns true for files whose extension suggests
// they contain code-level declarations (interfaces, types, functions).
func looksLikeSourceFile(path string) bool {
	ext := strings.ToLower(pathExt(path))
	switch ext {
	case ".go", ".rs", ".java", ".cs", ".ts", ".tsx", ".py", ".js":
		return true
	}
	return false
}

func (e *Engine) emit(runID string, taskID string, avatarID string, phase string, eventType string, source string, payload map[string]any, stageHint map[string]any) error {
	if e == nil {
		return nil
	}
	// P3-2: Protect concurrent event emission during parallel avatar
	// execution. Multiple goroutines may emit events simultaneously.
	e.emitMu.Lock()
	defer e.emitMu.Unlock()
	e.sequence++
	envelope := events.Envelope{
		EventID:   fmt.Sprintf("evt-%s-%06d", e.sessionID, e.sequence),
		Sequence:  e.sequence,
		RunID:     runID,
		TaskID:    taskID,
		AvatarID:  avatarID,
		Phase:     phase,
		Type:      eventType,
		EmittedAt: time.Now().UTC(),
		Source:    source,
		Payload:   payload,
		StageHint: stageHint,
	}

	if e.transcript != nil {
		if err := e.transcript.Append(envelope); err != nil {
			return err
		}
	}
	if e.telemetry != nil {
		e.telemetry.ObserveEvent(eventType, payload)
	}
	if e.store != nil {
		e.store.Append(envelope)
	}
	return e.persistHotEvent(envelope)
}

func (e *Engine) emitAvatarHandoff(runID string, taskID string, fromNode planner.WorkflowNode, toNode planner.WorkflowNode, plan planner.Plan) error {
	fromAvatarID := plan.AvatarIDByRole(fromNode.AssignedRole)
	toAvatarID := plan.AvatarIDByRole(toNode.AssignedRole)
	if fromAvatarID == "" || toAvatarID == "" || fromAvatarID == toAvatarID {
		return nil
	}
	summary := avatarHandoffSummary(fromNode.AssignedRole, fromNode.Title, toNode.AssignedRole, toNode.Title)
	payload := map[string]any{
		"message_type":   "handoff",
		"from_avatar_id": fromAvatarID,
		"from_role":      fromNode.AssignedRole,
		"from_node_id":   fromNode.ID,
		"from_title":     fromNode.Title,
		"to_avatar_id":   toAvatarID,
		"to_role":        toNode.AssignedRole,
		"to_node_id":     toNode.ID,
		"to_title":       toNode.Title,
	}
	if summary != "" {
		payload["summary"] = summary
	}
	message := normalizeAvatarMessage(AvatarMessage{
		RunID:        runID,
		TaskID:       taskID,
		Type:         "handoff",
		FromAvatarID: fromAvatarID,
		FromRole:     fromNode.AssignedRole,
		FromNodeID:   fromNode.ID,
		FromTitle:    fromNode.Title,
		ToAvatarID:   toAvatarID,
		ToRole:       toNode.AssignedRole,
		ToNodeID:     toNode.ID,
		ToTitle:      toNode.Title,
		Summary:      summary,
		Artifacts: []ArtifactRef{{
			ID:      fromNode.ID,
			Kind:    "workflow_node",
			Summary: fromNode.Title,
			OwnerID: fromAvatarID,
		}},
	})
	for key, value := range avatarMessagePayload(message) {
		payload[key] = value
	}
	return e.emit(runID, taskID, toAvatarID, "planning", "avatar.handoff", "runtime", payload, nil)
}

func (e *Engine) emitAvatarMessage(runID string, taskID string, avatarID string, phase string, messageType string, content string, summary string, scene string, extra map[string]any, artifacts []ArtifactRef) error {
	payload := clonePayload(extra)
	message := normalizeAvatarMessage(AvatarMessage{
		RunID:          runID,
		TaskID:         taskID,
		Type:           messageType,
		FromAvatarID:   avatarID,
		ToAvatarID:     payloadString(payload, "to_avatar_id"),
		ToRole:         payloadString(payload, "to_role"),
		BroadcastScope: payloadString(payload, "broadcast_scope"),
		ReplyTo:        payloadString(payload, "reply_to"),
		Content:        content,
		Summary:        summary,
		Scene:          scene,
		Artifacts:      artifacts,
	})
	for key, value := range avatarMessagePayload(message) {
		payload[key] = value
	}
	var stageHint map[string]any
	if strings.TrimSpace(scene) != "" {
		stageHint = map[string]any{"scene": scene}
	}
	return e.emit(runID, taskID, avatarID, phase, "avatar.spoke", "runtime", payload, stageHint)
}

func avatarHandoffSummary(fromRole string, fromTitle string, toRole string, toTitle string) string {
	fromRole = strings.TrimSpace(fromRole)
	fromTitle = strings.TrimSpace(fromTitle)
	toRole = strings.TrimSpace(toRole)
	toTitle = strings.TrimSpace(toTitle)
	if fromRole == "" && toRole == "" && toTitle == "" {
		return ""
	}
	if fromRole == "" {
		if toRole == "" {
			return fmt.Sprintf("Avatar handoff prepared for %s.", toTitle)
		}
		return fmt.Sprintf("Work handed off to %s for %s.", toRole, toTitle)
	}
	if toRole == "" {
		if fromTitle == "" {
			return fmt.Sprintf("%s handed off completed work.", fromRole)
		}
		return fmt.Sprintf("%s handed off %s.", fromRole, fromTitle)
	}
	if fromTitle == "" {
		if toTitle == "" {
			return fmt.Sprintf("%s handed off work to %s.", fromRole, toRole)
		}
		return fmt.Sprintf("%s handed off work to %s for %s.", fromRole, toRole, toTitle)
	}
	if toTitle == "" {
		return fmt.Sprintf("%s handed off %s to %s.", fromRole, fromTitle, toRole)
	}
	return fmt.Sprintf("%s handed off %s to %s for %s.", fromRole, fromTitle, toRole, toTitle)
}

// assemblePlannerLLMPrompts keeps PlannerStablePrefix at the front of the
// system prompt (byte-stable for provider prefix cache). Changing extras
// (skill body, critic feedback, structured API dump) go on the user turn.
func assemblePlannerLLMPrompts(sysPrompt, userPrompt, skillBody, feedback, structuredContext string) (string, string) {
	sys := strings.TrimSpace(sysPrompt)
	if sys == "" {
		sys = prompt.PlannerStablePrefix
	} else if !strings.Contains(sys, "=== PLANNER CORE RULES") {
		sys = prompt.PlannerStablePrefix + "\n\n" + sys
	}
	user := strings.TrimSpace(userPrompt)
	if body := strings.TrimSpace(skillBody); body != "" {
		user += "\n\n## Planner Role Skill Template\n\n" + body
	}
	if fb := strings.TrimSpace(feedback); fb != "" {
		user += "\n\n" + fb
	}
	if psc := strings.TrimSpace(structuredContext); psc != "" {
		user += "\n\n" + psc
	}
	return sys, user
}

// roleSkillForAvatar returns the body of a role-specific skill template
// for the given avatar role, or empty string if none exists. S3.9: prefer
// frontmatter role, then fall back to name matching.
func roleSkillForAvatar(role string, definitions []skills.Definition) string {
	target := strings.ToLower(strings.TrimSpace(role))
	if target == "" {
		return ""
	}
	for _, d := range definitions {
		if strings.ToLower(strings.TrimSpace(d.Role)) == target {
			return d.Body
		}
	}
	for _, d := range definitions {
		dl := strings.ToLower(d.Name)
		if strings.Contains(dl, target) || strings.Contains(dl, target+" skill") {
			return d.Body
		}
	}
	return ""
}

// appendAlwaysOnRoleSkills appends budgeted always-on skills for avatarRole
// onto a system/user prompt string (S3.9).
func appendAlwaysOnRoleSkills(base string, definitions []skills.Definition, avatarRole string) string {
	// F77: compact always-on ("always-on skill:") + full "## Always-On Skills"
	// dual-inject doubles Synthesizer prompt cost. Keep whichever is already
	// in base; do not append the full dump on top.
	if strings.Contains(base, alwaysOnCompactMarker) || strings.Contains(base, alwaysOnFullMarker) {
		return base
	}
	filtered := filterAlwaysOnSkills(definitions, avatarRole, alwaysOnBudgetChars)
	if len(filtered) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n## Always-On Skills (role-filtered)\n")
	for _, d := range filtered {
		b.WriteString("\n### ")
		b.WriteString(d.Name)
		b.WriteString("\n")
		body := strings.TrimSpace(d.Body)
		if len(body) > 3000 {
			body = body[:3000] + "\n[...truncated...]"
		}
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}
