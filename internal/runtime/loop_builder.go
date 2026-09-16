package runtime

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"avatars/internal/llm"
	"avatars/internal/planner"
	"avatars/internal/prompt"
	"avatars/internal/skillbuilder"
	"avatars/internal/tools"
	"avatars/internal/verification"
	"avatars/internal/workflow"
)

func (e *Engine) executeBuilderNodeWork(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	// Batch2/E: confirm+implement used to bump complexity to Builder and skip
	// Direct's ConfirmPlan path → PHASE LOCK without actually confirming.
	// Route ConfirmPlan through Builder node when the input is a confirm.
	if workflow.IsConfirmPlan(work.input) {
		return e.executeConfirmPlan(ctx, work)
	}

	// F42: Critic director remaps/cancels split-Builder targets so parallel
	// nodes cannot each invent a conflicting tree (bare doc.go, internal/debug).
	wd0, _ := os.Getwd()
	if newTitle, changed, reject := directorRewriteGenerateTitle(wd0, work.nodeTitle); reject != "" {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.director_blocked_builder", "runtime", map[string]any{
			"node_id":    work.nodeID,
			"node_title": work.nodeTitle,
			"reason":     reject,
		}, nil)
		if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
			RunID: work.runID, TaskID: work.taskID, AvatarID: work.criticAvatarID,
			NodeID: work.nodeID, NodeRole: "Critic", NodeTitle: work.nodeTitle,
			Tool: "critic_director", Operation: "block", Status: "completed",
			Summary: reject, Verified: true,
		}); err != nil {
			return workflowNodeWorkResult{}, err
		}
		return workflowNodeWorkResult{}, nil
	} else if changed {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.director_remapped_builder", "runtime", map[string]any{
			"node_id": work.nodeID,
			"from":    work.nodeTitle,
			"to":      newTitle,
		}, nil)
		work.nodeTitle = newTitle
	}
	if brief := criticDirectorLayoutBrief(wd0); brief != "" {
		work.planReviewFindings = strings.TrimSpace(work.planReviewFindings + "\n\n" + brief)
	}

	// P12: For split Builder nodes (title "Generate file.go"), narrow the
	// input to focus only on that file. This prevents LLM output truncation
	// from trying to generate too many files in one pass.
	if strings.HasPrefix(work.nodeTitle, "Generate ") {
		if file := parseGenerateTarget(work.nodeTitle); file != "" {
			work.input = fmt.Sprintf("Generate ONLY the file %s. Do NOT create any other files. Obey CRITIC LAYOUT CHARTER. %s", file, work.input)
		}
	}
	// PHANTOM-3: Pre-flight Critic gatekeeper — block Builder on high risk.
	if planRisks := criticPreFlightCheck(work); len(planRisks) > 0 {
		hasDestructive := false
		for _, r := range planRisks {
			if strings.Contains(r, "destructive") {
				hasDestructive = true
				break
			}
		}
		verdict := "proceed_with_risks"
		if hasDestructive {
			verdict = "blocked_destructive"
		}
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.pre_flight", "runtime", map[string]any{
			"risks":   planRisks,
			"count":   len(planRisks),
			"verdict": verdict,
		}, nil)
		e.criticInsights = append(e.criticInsights, planRisks...)
		if hasDestructive {
			return workflowNodeWorkResult{}, fmt.Errorf("critic pre-flight blocked: destructive operation detected in task")
		}
	}

	// I21: Pre-build plan review gate — runs format checks + LLM content
	// review before Builder generates code. Findings are injected into
	// Builder's system prompt so it can address gaps proactively.
	if planReview := ReviewPlanBeforeBuild(ctx, work, e.llm, e.modelForRole("critic"), e.personality); planReview != "" {
		work.planReviewFindings = planReview
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.plan_review", "runtime", map[string]any{
			"has_findings": true,
		}, nil)
	}

	// I34: Phase-driven execution — constrain Builder to the active phase only.
	// R4-8/R4-10: align Active Phase from input FIRST, expand that phase's stub,
	// THEN inject phaseN.md + stripped request (not the full multi-phase dump).
	// Keep the user-facing request for delivery gates — phase banners contain
	// "implementing"/"Generate" and must not trip needsCodeImplementation.
	userFacingInput := work.input
	fromPhase, toPhase := e.alignActivePhaseFromInput(work.input)
	// R5-7 / P8-4: user "进入 Phase N" jumps must emit phase_advanced (not silent
	// UpdateActivePhase) — but only when the prior phase checklist is complete.
	// Resume/timeout prompts that mention Phase 2 must not speculative-skip Critic.
	if toPhase > fromPhase && fromPhase >= 1 {
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "workflow.phase_advanced", "runtime", map[string]any{
			"from_phase": fromPhase,
			"to_phase":   toPhase,
			"decided_by": "user_request",
		}, nil)
	}
	if exp, p, expErr := workflow.ExpandActivePhaseDetail(".", e.makeWorkflowGenerate(ctx, workflowDocLLMTimeout(), work.runID, work.taskID, "expand_phase")); expErr == nil && exp {
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "workflow.phase_detail_expanded", "runtime", map[string]any{
			"phase":  p,
			"reason": "expanded deferred stub before Builder",
		}, nil)
	}
	if planner.LooksLikeProgressReconcileTask(work.input) {
		// Keep the user ask as-is. Injecting the implement-phase dump would
		// turn checklist-align into another full codegen pass.
		work.input = strings.TrimSpace(work.input) + `

=== CHECKLIST SYNC ===
Update workflow checklists to match evidence already on disk.
Do not regenerate implementation sources.
`
	} else {
		work.input = e.injectPhaseContextIntoPlan(work.input)
	}

	// P14-4b / S4.3: Enforce hard limit of 3 node-level retries.
	// Count increments only on failure (see recordNodeRetryFailure), not on entry.
	const maxNodeRetries = 3
	if e.nodeRetryCount == nil {
		e.nodeRetryCount = make(map[string]int)
	}
	if e.nodeRetryCount[work.nodeID] >= maxNodeRetries {
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.node_retry_limit_exceeded", "runtime", map[string]any{
			"node_id": work.nodeID,
			"retries": e.nodeRetryCount[work.nodeID],
			"max":     maxNodeRetries,
		}, nil)
		return workflowNodeWorkResult{
			pausePoint: PausePoint{
				ID:           work.nodeID + "-retry-limit",
				Kind:         PausePointKindWorkflowNode,
				NodeID:       work.nodeID,
				StatusReason: fmt.Sprintf("Node %s exceeded max retries (%d)", work.nodeID, maxNodeRetries),
			},
		}, nil
	}
	if e.permissionMode == PermissionModePlan {
		// Root fix (P6): For analysis tasks in plan mode, execute actual
		// data collection via shell commands instead of generating an
		// empty "Task Survey Skill". This fixes the fundamental gap where
		// every complex analysis request produced "Synthesis: analysis
		// report generated" with no actual data.
		//
		// The Builder's job is to "produce the bounded output". For
		// analysis tasks, the bounded output IS the data. For code
		// creation tasks, the bounded output IS a skill/plan preview.
		if commands := tools.BuildAnalysisCommands(work.input, 5); len(commands) > 0 {
			result := tools.ExecuteAnalysisCommands(commands, 4000, 20*time.Second)
			_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.analysis_completed", "runtime", map[string]any{
				"command_count": len(commands),
				"output_length": len(result),
			}, nil)
			return workflowNodeWorkResult{readSummary: result}, nil
		}

		// Not an analysis task — fall through to existing skill generation.
		proposal := skillbuilder.Build(work.input, work.plan, work.readSummary)
		preview := strings.TrimSpace(proposal.Name)
		if preview == "" {
			preview = "Task Survey Skill"
		}
		if err := e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "skill.candidate_prepared", "skillbuilder", map[string]any{
			"name":              proposal.Name,
			"description":       proposal.Description,
			"when_to_use":       proposal.WhenToUse,
			"allowed_tools":     proposal.AllowedTools,
			"preview":           proposal.Body,
			"state":             "candidate_prepared",
			"approval_required": false,
		}, nil); err != nil {
			return workflowNodeWorkResult{}, err
		}
		if err := e.persistSkillCandidateEvaluation(work.runID, work.taskID, work.builderAvatarID, proposal.Name, "candidate_prepared", "", false); err != nil {
			return workflowNodeWorkResult{}, err
		}
		if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
			RunID:       work.runID,
			TaskID:      work.taskID,
			AvatarID:    work.builderAvatarID,
			NodeID:      work.nodeID,
			NodeRole:    work.nodeRole,
			NodeTitle:   work.nodeTitle,
			Tool:        "write",
			Operation:   "file_write",
			Status:      "prepared",
			Summary:     fmt.Sprintf("Workflow node %s prepared generated skill candidate in plan mode.", work.nodeID),
			ArtifactIDs: workflowNodeArtifactIDs(WorkflowNodeRuntime{ID: work.nodeID}),
			Verified:    true,
		}); err != nil {
			return workflowNodeWorkResult{}, err
		}
		return workflowNodeWorkResult{generatedSkillPreview: preview}, nil
	}
	if e.skills == nil {
		return workflowNodeWorkResult{}, nil
	}
	builderContext, contextErr := BuildAvatarContext(work.plan, work.builderAvatarID)
	if contextErr != nil {
		return workflowNodeWorkResult{}, contextErr
	}
	if err := builderContext.RequireTool("write", "file_write"); err != nil {
		return workflowNodeWorkResult{}, err
	}
	var proposal skillbuilder.Proposal
	if strings.TrimSpace(e.attachedFilePath) != "" {
		// @file attached: derive Name/Description/Slug/Body from the
		// attached file content (TODO-10). The planning phase computed
		// the same path via PrepareGenerated without writing; here we
		// re-derive so the actual file on disk matches the proposal
		// announced in skill.proposed.
		proposal = skillbuilder.BuildForAttachedFile(work.input, work.plan, skillbuilder.AttachedFile{
			Path:    e.attachedFilePath,
			Content: e.attachedFileBytes,
		})
	} else {
		// T8.5: Use Builder-specific skill template with role guidance.
		proposal = skillbuilder.BuildForRole("Builder", work.input, work.plan, work.readSummary)
	}
	if work.activeSkill != nil && strings.TrimSpace(work.activeSkill.Body) != "" {
		proposal.Body = "## Active Skill Context\nThe Builder avatar should follow the active skill playbook below:\n\n" + strings.TrimSpace(work.activeSkill.Body) + "\n\n---\n\n" + proposal.Body
	}
	// Ensure structured context is populated before we embed it in the
	// skill file. The Researcher may have already extracted it, but
	// if not (e.g., first node in chain), extract from task-guided sources.
	if work.structuredContext == nil {
		sourceFiles := codeTaskSuggestedTargets(work.input, 5)
		work.structuredContext = extractStructuredContext(work.readSummary, sourceFiles)
	}

	// Embed structured context in the skill file for cross-run reuse.
	// Future runs can reference these exact interface/type signatures
	// without needing to re-extract them from source files.
	if work.structuredContext != nil && !work.structuredContext.Empty() {
		proposal.Body += "\n\n## Structured Project Context (auto-extracted)\n\n"
		proposal.Body += formatStructuredContextForSkill(work.structuredContext)
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.structured_context_embedded", "runtime", map[string]any{
			"interface_count": len(work.structuredContext.InterfaceDecls),
			"struct_count":    len(work.structuredContext.StructDecls),
			"func_count":      len(work.structuredContext.FuncDecls),
		}, nil)
	}
	// T8.2+T8.4 / S3.10: embed Top-K past builder skills ranked by ledger
	// success rate — not an unfiltered dump of every builder skill.
	if e.skills != nil {
		for _, def := range e.topPastExperienceSkills("builder", 3) {
			proposal.Body += "\n\n## Past Builder Experience\n\n"
			body := def.Body
			if len(body) > 2000 {
				body = body[:2000] + "\n\n[...truncated...]"
			}
			proposal.Body += body
		}
	}

	prepared, err := e.skills.PrepareGenerated(work.runID, work.taskID, proposal)
	if err != nil {
		return workflowNodeWorkResult{}, err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return workflowNodeWorkResult{}, err
	}
	writePath := generatedSkillWritePath(workingDir, prepared.Path)
	writeInput := tools.WriteInput{
		Path:            writePath,
		Content:         prepared.Content,
		WorkingDir:      workingDir,
		Overwrite:       true, // P13-1: allow overwriting skill from prior runs
		Intent:          "persist generated candidate skill",
		ExpectedTargets: []string{writePath},
	}
	if _, err := e.invokeToolWithOptions(ctx, toolCallEnvelope{runID: work.runID, taskID: work.taskID, avatarID: work.builderAvatarID, phase: "executing"}, "write", "file_write", writeInput, toolInvokeOptions{workflowCheckpoint: enrichWorkflowCheckpoint(
		buildWorkflowCheckpointFromState(work.plan, work.workflowState, work.runID, work.taskID, "Builder", "tool.awaiting_approval"),
		work.workflowState,
		e.nodeRetryCount,
		work.input,
		work.readSummary,
	)}); err != nil {
		directOK := false
		if isSandboxEscapeWriteErr(err) {
			if werr := writeGeneratedSkillDirect(prepared.Path, prepared.Content); werr == nil {
				directOK = true
			}
		}
		if directOK {
			// Skill landed via HOME write; continue Builder (do not return).
		} else if strings.Contains(strings.ToLower(err.Error()), "approval required") {
			point, pointErr := work.workflowState.PausePointForNodeInRun(work.runID, work.taskID, work.nodeID)
			if pointErr != nil {
				return workflowNodeWorkResult{}, pointErr
			}
			point.StatusReason = strings.TrimSpace(err.Error())
			if persistErr := e.persistNodeWorkEvidence(nodeWorkEvidence{
				RunID:              work.runID,
				TaskID:             work.taskID,
				AvatarID:           work.builderAvatarID,
				NodeID:             work.nodeID,
				NodeRole:           work.nodeRole,
				NodeTitle:          work.nodeTitle,
				Tool:               "write",
				Operation:          "file_write",
				Status:             "awaiting_approval",
				Summary:            fmt.Sprintf("Workflow node %s awaits approval for write/file_write.", work.nodeID),
				ExpectedTargets:    writeInput.ExpectedTargets,
				VerifierGateStatus: "pending_approval",
				Verified:           false,
				PausePointID:       point.ID,
				PausePointKind:     string(point.Kind),
				RecoveryKind:       "approval_required",
			}); persistErr != nil {
				return workflowNodeWorkResult{}, persistErr
			}
			return workflowNodeWorkResult{pausePoint: point, pauseErr: err}, nil
		} else {
			if isVerificationFailureForTool("write", writeInput, err) {
				gate := e.latestNodeVerifierGate(work.runID, work.taskID, "write", true)
				if persistErr := e.persistNodeWorkEvidence(nodeWorkEvidence{
					RunID:                  work.runID,
					TaskID:                 work.taskID,
					AvatarID:               work.builderAvatarID,
					NodeID:                 work.nodeID,
					NodeRole:               work.nodeRole,
					NodeTitle:              work.nodeTitle,
					Tool:                   "write",
					Operation:              "file_write",
					Status:                 "needs_remediation",
					Summary:                fmt.Sprintf("Workflow node %s requires verifier remediation for write/file_write.", work.nodeID),
					ExpectedTargets:        writeInput.ExpectedTargets,
					VerifierGateStatus:     gate.Status,
					VerifierVerdict:        gate.Verdict,
					VerificationReportPath: gate.ReportPath,
					Verified:               gate.Verified,
					RecoveryKind:           "verifier_remediation",
				}); persistErr != nil {
					return workflowNodeWorkResult{}, persistErr
				}
			}
			return workflowNodeWorkResult{}, err
		}
	}
	_ = recoverGeneratedSkillIfMissing(prepared.Path, writePath, workingDir)
	if err := e.skills.FinalizeGenerated(prepared.Path, prepared.GeneratedAt); err != nil {
		return workflowNodeWorkResult{}, err
	}

	// === Code Implementation Phase (P0-2 fix) ===
	// After generating the skill survey, check if the task requires
	// actual code implementation. If so, call the LLM to generate
	// implementation files and write them through the guarded write tool.
	codeArtifactIDs := []string{}
	postBuildHealthErr := ""
	builderConvergedEarly := false
	diskFilesAtConverge := 0
	if e.llm != nil && needsCodeImplementation(work.input) {
		// P14-4a: Capture build error baseline before code generation so
		// post-generation verification can filter pre-existing errors.
		workingDir, _ := os.Getwd()
		e.CaptureBuildBaseline(workingDir)
		if emitErr := e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_implementation_started", "runtime", map[string]any{
			"node_id": work.nodeID,
			"input":   work.input,
		}, nil); emitErr != nil {
			// Non-fatal: log and continue.
		}
		codeFiles, codeErr := e.generateBuilderCode(ctx, work)
		if codeErr == nil {
			// P9-4: always merge layout finalize stubs after a successful generate
			// (tool-loop early-complete and single-shot paths both need this).
			if wd, wdErr := os.Getwd(); wdErr == nil {
				gaps := collectBuilderFinalizeGaps(wd, work.input, codeFiles)
				if len(gaps) > 0 {
					codeFiles = append(codeFiles, gaps...)
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.finalize_gaps", "runtime", map[string]any{
						"stub_count": len(gaps),
						"node_id":    work.nodeID,
					}, nil)
				}
			}
		}
		codeFiles, codeErr = e.retryHollowBuilderDelivery(ctx, work, userFacingInput, codeFiles, codeErr)
		if codeErr != nil {
			// Classify errors — network ≠ turn-cap self-heal; "no files"
			// + green build + completion prose → converge (don't death-loop).
			errText := codeErr.Error()
			isTurnCap := strings.Contains(errText, "exceeded max tool turns") || strings.Contains(errText, "missing field")
			isDeadline := strings.Contains(errText, "context deadline exceeded") || strings.Contains(errText, "deadline exceeded")
			isNetwork := isTransientLLMNetworkError(errText)
			isBusy := isProviderBusyError(errText)
			isNoFiles := strings.Contains(errText, "Builder produced no files")
			isTruncationExhausted := strings.Contains(errText, "truncated after")

			if isNoFiles && builderProseSaysComplete(errText) {
				if wd, wdErr := os.Getwd(); wdErr == nil && !deliveryLooksEmpty(wd) && crossLangHealthCheck(wd) == "" {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_generation_converged", "runtime", map[string]any{
						"reason":  "no new files + build green + completion prose",
						"node_id": work.nodeID,
					}, nil)
					codeErr = nil
					// W1: still run layout finalize stubs; don't silently skip gaps.
					codeFiles = collectBuilderFinalizeGaps(wd, work.input, nil)
					builderConvergedEarly = true
					diskFilesAtConverge = countProjectSourceFiles(wd)
				}
			}

			// Z9: provider capacity — fail fast with a clear event (do not burn L1–L3 drafts).
			if codeErr != nil && isBusy {
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.provider_busy", "runtime", map[string]any{
					"error":   codeErr.Error(),
					"node_id": work.nodeID,
					"hint":    "LLM provider overloaded; retry later or switch model/provider",
				}, nil)
			} else if codeErr != nil && isTruncationExhausted {
				_ = purgeUnhealthySourcesOnDisk(e.sweepRoot())
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.draft_thrash_abort", "runtime", map[string]any{
					"reason":  "truncation_exhausted",
					"error":   codeErr.Error(),
					"node_id": work.nodeID,
				}, nil)
			} else if codeErr != nil && isNetwork && !isBusy {
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_generation_retrying", "runtime", map[string]any{
					"error": codeErr.Error(), "node_id": work.nodeID,
					"level": 0, "attempt": "network-retry",
				}, nil)
				codeFiles, codeErr = e.generateBuilderCode(ctx, work)
			} else if codeErr != nil && (isTurnCap || isDeadline) && !isBusy {
				// v4-P0 / Batch4/H: Multi-level self-heal (only turn-cap / deadline).
				const maxSelfHealLevels = 3
				for level := 1; level <= maxSelfHealLevels && codeErr != nil; level++ {
					retryWork := work
					stopSelfHeal := false
					switch level {
					case 1:
						// L3/V6: turn-cap and deadline both need more turns + fewer reads.
						if state := e.ensureBuilderRetryState(work.nodeID); state != nil {
							base := builderMaxToolTurns()
							if state.EscalatedMaxToolTurns > base {
								base = state.EscalatedMaxToolTurns
							}
							state.EscalatedMaxToolTurns = base + 12
						}
						if wd := e.sweepRoot(); wd != "" && diskHasAppSources(wd) {
							_ = purgeUnhealthySourcesOnDisk(wd)
							// F22: heal duplicated toolchain before the green check.
							if ensureGoModNormalized(wd) {
								_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.go_mod_normalized", "runtime", map[string]any{
									"reason": "deduped repeated toolchain/go lines before health check",
								}, nil)
							}
							health := crossLangHealthCheck(wd)
							if health == "" {
								// Already have a green scaffold — stop burning turns,
								// but still finalize layout gaps (W1/W5/W6).
								codeFiles = collectBuilderFinalizeGaps(wd, work.input, nil)
								diskFilesAtConverge = countProjectSourceFiles(wd)
								builderConvergedEarly = true
								if e != nil && isTurnCap {
									e.builderTurnCapConverged = true
								}
								// F21: turn-cap stop ≠ phase complete — label honestly when
								// Active checklist still has zero progress.
								reason := "turn-cap/deadline but disk scaffold + health green"
								if done, total := workflow.CountTodoProgress(wd); total > 0 && done == 0 {
									reason = "turn-cap/deadline early-stop; compile green but checklist incomplete"
								}
								_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_generation_converged", "runtime", map[string]any{
									"reason":         reason,
									"node_id":        work.nodeID,
									"files_on_disk":  diskFilesAtConverge,
									"finalize_stubs": len(codeFiles),
								}, nil)
								codeErr = nil
								stopSelfHeal = true
								break
							}
							// J2-2/J2-3: env failure or already-populated disk — fail fast
							// instead of another 4–5min drafting loop toward node-build timeout.
							if isHostToolchainFailure(health) {
								codeErr = fmt.Errorf("builder: %s", annotateHostToolchainFailure(health))
								_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.host_toolchain_abort", "runtime", map[string]any{
									"error":   codeErr.Error(),
									"node_id": work.nodeID,
								}, nil)
								stopSelfHeal = true
								break
							}
							// W2: missing/network deps → ensure+retry before thrash abort.
							if looksLikeMissingPackageDeps(health) || looksLikeMissingGoSum(health) ||
								looksLikeDependencyNetworkFailure(health) {
								_ = os.Remove(filepath.Join(wd, ".avatars", "go_mod_tidy_failed"))
								_ = ensureProjectDependencies(wd)
								if looksLikeMissingGoSum(health) || looksLikeDependencyNetworkFailure(health) {
									_ = runGoModTidyWithRetry(wd, 3)
								}
								if recheck := crossLangHealthCheck(wd); recheck == "" {
									codeFiles = collectBuilderFinalizeGaps(wd, work.input, nil)
									diskFilesAtConverge = countProjectSourceFiles(wd)
									builderConvergedEarly = true
									_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_generation_converged", "runtime", map[string]any{
										"reason":        "deps ensured after health red — scaffold green",
										"node_id":       work.nodeID,
										"files_on_disk": diskFilesAtConverge,
									}, nil)
									codeErr = nil
									stopSelfHeal = true
									break
								}
								health = crossLangHealthCheck(wd)
								if looksLikeDependencyNetworkFailure(health) {
									_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.deps_network_abort", "runtime", map[string]any{
										"reason":  "deps network still red after ensure — skip draft thrash",
										"health":  health,
										"node_id": work.nodeID,
									}, nil)
									stopSelfHeal = true
									break
								}
							}
							if countProjectSourceFiles(wd) >= 2 {
								if e.retryHealthFailedBuilder(ctx, work, wd, health) {
									recheck := crossLangHealthCheck(wd)
									if recheck == "" {
										codeFiles = collectBuilderFinalizeGaps(wd, work.input, nil)
										diskFilesAtConverge = countProjectSourceFiles(wd)
										builderConvergedEarly = true
										codeErr = nil
										_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_generation_converged", "runtime", map[string]any{
											"reason":        "health retry after deadline; tests/compile green",
											"node_id":       work.nodeID,
											"files_on_disk": diskFilesAtConverge,
										}, nil)
										stopSelfHeal = true
										break
									}
									health = recheck
								}
								_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.draft_thrash_abort", "runtime", map[string]any{
									"reason":        "disk_has_sources_health_red",
									"health":        health,
									"files_on_disk": countProjectSourceFiles(wd),
									"node_id":       work.nodeID,
								}, nil)
								stopSelfHeal = true
								break
							}
						}
						if isDeadline {
							// R3-1/5: on deadline, prefer one-file-at-a-time immediately
							// instead of repeating the same full multi-file prompt.
							retryWork.input = "DEADLINE RECOVERY: Generate ONE file at a time. Start with the most important incomplete source file. Do not re-plan. Write the file now. " + work.input
						} else {
							retryWork.input = "TURN-CAP RECOVERY: stop surveying — write the required source files now with minimal reads. " + work.input
						}
					case 2:
						if isDeadline {
							retryWork.input = "Generate ONE file only — the highest-priority missing source. No survey. FILE: <path> then content. " + work.input
						} else {
							retryWork.input = "PRODUCE CODE NOW — no file reads. Write the files directly. " + work.input
						}
					case 3:
						retryWork.input = "Generate ONE file at a time. Start with the most important file. " + work.input
					}
					if stopSelfHeal || codeErr == nil {
						break
					}
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_generation_retrying", "runtime", map[string]any{
						"error": codeErr.Error(), "node_id": work.nodeID,
						"level": level, "attempt": fmt.Sprintf("self-heal L%d", level),
					}, nil)
					codeFiles, codeErr = e.generateBuilderCode(ctx, retryWork)
					// Z9: if a heal attempt hits provider busy, stop further levels.
					if codeErr != nil && isProviderBusyError(codeErr.Error()) {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.provider_busy", "runtime", map[string]any{
							"error":   codeErr.Error(),
							"node_id": work.nodeID,
							"level":   level,
							"hint":    "LLM provider overloaded during self-heal; retry later or switch model/provider",
						}, nil)
						break
					}
				}
			}
			if codeErr != nil {
				errText := codeErr.Error()
				isNoFiles := strings.Contains(errText, "Builder produced no files")
				if isNoFiles {
					state := e.ensureBuilderRetryState(work.nodeID)
					if !state.ProseNoFilesRetried {
						state.ProseNoFilesRetried = true
						retryWork := work
						retryWork.input = work.input + builderRepairWriteSuffix
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.prose_no_files_retry", "runtime", map[string]any{
							"node_id": work.nodeID,
							"reason":  "analysis reply wrote no sources; retry with write discipline",
						}, nil)
						codeFiles, codeErr = e.generateBuilderCode(ctx, retryWork)
					}
				}
			}
			if codeErr == nil && len(codeFiles) > 0 && asksInjectableClock(work.input) {
				var blob strings.Builder
				for _, cf := range codeFiles {
					blob.WriteString(cf.Content)
					blob.WriteByte('\n')
				}
				if waitUsesWallTimeDespiteClock(blob.String()) {
					state := e.ensureBuilderRetryState(work.nodeID)
					if !state.WallClockWaitRetried {
						state.WallClockWaitRetried = true
						retryWork := work
						retryWork.input = work.input + builderRepairWriteSuffix
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.clock_wait_retry", "runtime", map[string]any{
							"node_id": work.nodeID,
							"reason":  "wait/delay still uses wall sleep with an injectable clock",
						}, nil)
						codeFiles, codeErr = e.generateBuilderCode(ctx, retryWork)
					}
				}
			}
			if codeErr != nil {
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_generation_failed", "runtime", map[string]any{"error": codeErr.Error(), "node_id": work.nodeID}, nil)
				if strings.Contains(codeErr.Error(), "Builder produced no files") {
					return workflowNodeWorkResult{}, codeErr
				}
			}
		}
		// Capture retry state: file list for next Builder attempt.
		if len(codeFiles) > 0 {
			state := e.ensureBuilderRetryState(work.nodeID)
			state.FilesGenerated = make([]string, len(codeFiles))
			for i, cf := range codeFiles {
				state.FilesGenerated[i] = cf.Path
			}
			state.AttemptNumber++
		}
		if codeErr == nil && len(codeFiles) > 0 {
			// Heartbeat: tell user Builder is about to write files.
			_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "heartbeat.builder_writing", "runtime", map[string]any{
				"message": fmt.Sprintf("Writing %d file(s)...", len(codeFiles)),
			}, nil)

			// P11-1b: File path redirection — detect hallucinated paths and redirect to
			// the correct existing file from the task specification.
			expectedPaths := extractTaskFilePaths(work.input)
			for _, cf := range codeFiles {
				workingDir, wdErr := os.Getwd()
				if wdErr != nil {
					continue
				}
				writePath := cf.Path
				if !filepath.IsAbs(cf.Path) {
					writePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(cf.Path)))
				}
				// NL smoke Batch1/P6: language-agnostic root/source path hygiene.
				if sanitized, redirected := sanitizeWritePath(writePath); redirected {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.path_sanitized", "runtime", map[string]any{
						"from": writePath, "to": sanitized, "reason": "sanitizeWritePath",
					}, nil)
					writePath = sanitized
				}
				// P11-1b: If Builder created a new path that differs from expected paths,
				// and the expected path exists on disk, redirect to the existing file.
				if len(expectedPaths) > 0 {
					resolvedWrite := writePath
					if !filepath.IsAbs(resolvedWrite) {
						resolvedWrite = filepath.Join(workingDir, resolvedWrite)
					}
					if _, statErr := os.Stat(resolvedWrite); statErr != nil {
						// The file Builder wants to write does NOT exist yet (new file).
						// Check if there's an expected file in the same directory that DOES exist.
						for _, ep := range expectedPaths {
							resolvedEP := ep
							if !filepath.IsAbs(resolvedEP) {
								resolvedEP = filepath.Join(workingDir, resolvedEP)
							}
							if _, epStatErr := os.Stat(resolvedEP); epStatErr == nil {
								// Expected file exists. If same directory, redirect.
								if filepath.Dir(resolvedWrite) == filepath.Dir(resolvedEP) {
									_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.path_redirect", "runtime", map[string]any{
										"from": writePath, "to": ep, "reason": "redirecting new file to existing task target",
									}, nil)
									writePath = ep
									break
								}
							}
						}
					}
				}

				// GAP-2: Block known external frameworks BEFORE any import check.
				// This strips cobra, gin, gorm, etc. from content before it reaches
				// the go.mod check (which would pass if Builder already modified go.mod).
				if strings.HasSuffix(writePath, ".go") && strings.TrimSpace(cf.Content) != "" {
					if cleaned, blocked := BlockExternalImports(cf.Content); len(blocked) > 0 {
						for _, bi := range blocked {
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.external_framework_blocked", "runtime", map[string]any{
								"file": writePath, "import": bi.ImportPath,
								"stdlib_alt": bi.StdlibAlt,
								"reason":     fmt.Sprintf("external framework %s blocked - use %s", bi.ImportPath, bi.StdlibAlt),
							}, nil)
						}
						cf.Content = cleaned
					}
				}
				// P11-0b: Validate Go imports before writing — prevent hallucinated import paths.
				if strings.HasSuffix(writePath, ".go") && strings.TrimSpace(cf.Content) != "" {
					fixed, warnings := validateBuilderGoImports(workingDir, cf.Content)
					if len(warnings) > 0 {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.import_warning", "runtime", map[string]any{
							"file": writePath, "warnings": warnings,
						}, nil)
					}
					cf.Content = fixed
				}
				if strings.HasSuffix(writePath, "_test.go") && strings.TrimSpace(cf.Content) != "" {
					cf.Content = alignGoTestWaitKeys(cf.Content)
				}
				// BLOCK-1: Check external dependencies BEFORE writing — prevent
				// hallucinated imports not in go.mod from reaching disk.
				// CheckExternalDependencies parses go.mod and flags any import
				// that is NOT stdlib, NOT module-prefixed, and NOT in go.mod.
				if strings.HasSuffix(writePath, ".go") && strings.TrimSpace(cf.Content) != "" {
					depWarnings := CheckExternalDependencies(workingDir, []string{writePath})
					if len(depWarnings) > 0 {
						for _, dw := range depWarnings {
							alt := ""
							if dw.StdlibAlt != "" {
								alt = fmt.Sprintf(" (use %s instead)", dw.StdlibAlt)
							}
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.external_dep_blocked", "runtime", map[string]any{
								"file":       writePath,
								"import":     dw.ImportPath,
								"stdlib_alt": dw.StdlibAlt,
								"reason":     fmt.Sprintf("import %s is not in go.mod%s", dw.ImportPath, alt),
							}, nil)
						}
						// Remove the bad imports from content before writing.
						cf.Content = removeExternalImports(cf.Content, depWarnings, workingDir)
					}
				}
				// P14-1b: Content health check — block catastrophic file truncation.
				// If the target file exists and Builder's content is < 60% of existing size,
				// the LLM likely generated a stub instead of a real implementation.
				resolvedHealth := writePath
				if !filepath.IsAbs(resolvedHealth) {
					resolvedHealth = filepath.Join(workingDir, resolvedHealth)
				}
				// Track before/after line counts for rich progress events.
				var beforeLines int
				var beforeContent []byte // H-2: saved for auto-rollback on failure
				if existingContent, readErr := os.ReadFile(resolvedHealth); readErr == nil {
					existingLen := len(existingContent)
					beforeLines = strings.Count(string(existingContent), "\n")
					beforeContent = existingContent // H-2: save for rollback
					newLen := len(cf.Content)
					// P14-1a: Emit before-content snapshot for rollback/recovery.
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.before_write_snapshot", "runtime", map[string]any{
						"file": writePath, "before_bytes": existingLen, "before_lines": beforeLines,
					}, nil)
					if existingLen > 500 && newLen > 0 && float64(newLen) < float64(existingLen)*0.6 {
						// Catastrophic truncation detected — block the write.
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.health_block_truncation", "runtime", map[string]any{
							"file": writePath, "existing_bytes": existingLen, "new_bytes": newLen,
							"ratio":  fmt.Sprintf("%.1f%%", float64(newLen)/float64(existingLen)*100),
							"reason": "Builder content is < 60% of existing file — likely a stub/hallucination",
						}, nil)
						continue // skip this file, do NOT write
					}
				}
				// F63: coerce lib-dir *_test.go away from package main before health refuse.
				if fixed, ok := coerceGoLibTestPackage(writePath, cf.Content); ok {
					cf.Content = fixed
				}
				// F64: refuse CLI entrypoints when the task forbids them.
				if forbidsCLIScaffold(strings.ToLower(work.input)) && isForbiddenCLIEntrypointPath(writePath) {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.health_block_quality", "runtime", map[string]any{
						"file": writePath, "reason": "CLI/entrypoint blocked by no-CLI constraint",
					}, nil)
					continue
				}
				// Phase 5: Health guard extension — additional quality checks
				// beyond truncation. All checks are deterministic (no LLM).
				if reason := checkFileHealth(writePath, cf.Content); reason != "" {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.health_block_quality", "runtime", map[string]any{
						"file": writePath, "reason": reason,
					}, nil)
					continue // skip this file, do NOT write
				}
				if lifted, from := applyWritePathRewrite(workingDir, work.input, writePath, cf.Content); lifted != "" {
					if from != "" {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.path_sanitized", "runtime", map[string]any{
							"from": from, "to": lifted, "reason": "write remapped_from (F105)",
						}, nil)
					}
					writePath = lifted
				}
				writeInput := tools.WriteInput{
					Path:            writePath,
					Content:         cf.Content,
					WorkingDir:      workingDir,
					Overwrite:       true,
					Intent:          fmt.Sprintf("Builder: implement %s", filepath.Base(cf.Path)),
					ExpectedTargets: []string{writePath},
				}
				// P4-1c: Coordinator — file exists + modification task → use edit.
				resolvedPath := writePath
				if !filepath.IsAbs(resolvedPath) {
					resolvedPath = filepath.Join(workingDir, resolvedPath)
				}
				// Surgical edits already on disk — never full-overwrite.
				if cf.AlreadyOnDisk {
					codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-edit-%s", filepath.Base(cf.Path)))
					e.changedFiles = append(e.changedFiles, resolvedPath)
					// F62: do not TryAutoMarkDone on mid-write — wait for post-build green
					// (syncWorkflowAfterBuild). Path-evidence 虚勾 caused Critic 6/6 on red builds.
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.surgical_edit_kept", "runtime", map[string]any{
						"file": writePath, "reason": "edit_file/precise_edit already applied — skip full rewrite",
					}, nil)
					continue
				}
				// Existing substantive file: dump → surgical hunks before any Overwrite
				// (covers modification tasks and accidental full dumps on create paths).
				if existingBytes, readErr := os.ReadFile(resolvedPath); readErr == nil && len(strings.TrimSpace(string(existingBytes))) >= 50 {
					existingText := string(existingBytes)
					// F68: never apply a dump that guts the test suite to "make compile green".
					if testCoverageCollapse(writePath, existingText, cf.Content) {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.test_coverage_collapse_blocked", "runtime", map[string]any{
							"file":           writePath,
							"existing_tests": countTestSymbols(writePath, existingText),
							"proposed_tests": countTestSymbols(writePath, cf.Content),
							"reason":         "refusing test-suite shrink that would fake a green build",
						}, nil)
						continue
					}
					surg, _ := tools.SurgicalApplyFromDump(existingText, cf.Content, writePath, workingDir)
					if surg.Applied() > 0 {
						// Re-check on-disk result — surgical hunks can still gut coverage.
						if after, err := os.ReadFile(resolvedPath); err == nil &&
							testCoverageCollapse(writePath, existingText, string(after)) {
							_ = os.WriteFile(resolvedPath, existingBytes, 0o644)
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.test_coverage_collapse_reverted", "runtime", map[string]any{
								"file":   writePath,
								"reason": "surgical apply gutted tests — restored previous content",
							}, nil)
							continue
						}
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.surgical_apply_applied", "runtime", map[string]any{
							"file": writePath, "inserts": surg.Inserts, "replaces": surg.Replaces, "skipped": surg.Skipped,
						}, nil)
						codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-surg-%s", filepath.Base(cf.Path)))
						e.changedFiles = append(e.changedFiles, resolvedPath)
						// F62: defer checklist marks until post-build health is green.
						continue
					}
					// G2: when existing file is already unhealthy and proposed passes
					// health, allow one validated full rewrite (surgical thrash escape).
					existingBad := checkFileHealth(writePath, existingText) != ""
					proposedOK := checkFileHealth(writePath, cf.Content) == ""
					if existingBad && proposedOK {
						if testCoverageCollapse(writePath, existingText, cf.Content) {
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.test_coverage_collapse_blocked", "runtime", map[string]any{
								"file":   writePath,
								"reason": "refuse surgical-escape full rewrite that guts tests",
							}, nil)
							continue
						}
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.surgical_escape_full_rewrite", "runtime", map[string]any{
							"file": writePath, "reason": surg.Reason, "existing_unhealthy": true,
						}, nil)
						// fall through to normal write path below
					} else if looksLikeModificationTask(work.input) || len(strings.TrimSpace(existingText)) > 50 {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.surgical_apply_skipped", "runtime", map[string]any{
							"file": writePath, "reason": surg.Reason,
						}, nil)
						// Refuse destructive full rewrite of existing healthy content.
						continue
					}
				} // T8.8: Autonomous debugging — retry on verification failure.
				// Per-file hard limit prevents verification retry storms (observed 12+ retries).
				const maxBuilderRetries = 3
				writeSucceeded := false
				verifFailures := 0
				const maxVerifFailures = 2 // per-file verification failure cap
				var builderErrors []string
				for attempt := 0; attempt <= maxBuilderRetries; attempt++ {
					_, wErr := e.invokeToolWithOptions(ctx, toolCallEnvelope{runID: work.runID, taskID: work.taskID, avatarID: work.builderAvatarID, phase: "executing"}, "write", "file_write", writeInput, toolInvokeOptions{})
					if wErr == nil {
						writeSucceeded = true
						break
					}
					if strings.Contains(strings.ToLower(wErr.Error()), "approval required") {
						break
					}
					if strings.Contains(strings.ToLower(wErr.Error()), "refuses to overwrite") {
						// Never escalate to full overwrite for substantive existing files.
						if existingBytes, readErr := os.ReadFile(resolvedPath); readErr == nil && len(strings.TrimSpace(string(existingBytes))) >= 50 {
							surg, _ := tools.SurgicalApplyFromDump(string(existingBytes), writeInput.Content, writePath, workingDir)
							if surg.Applied() > 0 {
								writeSucceeded = true
								_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.overwrite_refused_surgical", "runtime", map[string]any{
									"file": writePath, "inserts": surg.Inserts, "replaces": surg.Replaces,
								}, nil)
							} else {
								_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.overwrite_refused_blocked", "runtime", map[string]any{
									"file": writePath, "reason": surg.Reason,
								}, nil)
							}
							break
						}
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.edit_fallback", "runtime", map[string]any{
							"file": writePath, "reason": "file exists, retry write with Overwrite=true",
						}, nil)
						editInput := tools.WriteInput{
							Path:            writePath,
							Content:         writeInput.Content,
							WorkingDir:      workingDir,
							Overwrite:       true,
							Intent:          fmt.Sprintf("Builder: edit %s", filepath.Base(writePath)),
							ExpectedTargets: []string{writePath},
						}
						if _, editErr := e.invokeToolWithOptions(ctx, toolCallEnvelope{runID: work.runID, taskID: work.taskID, avatarID: work.builderAvatarID, phase: "executing"}, "write", "file_write", editInput, toolInvokeOptions{}); editErr == nil {
							writeSucceeded = true
						}
						break
					}
					// Hard limit: always break at max retries.
					if attempt == maxBuilderRetries {
						break
					}
					// Verification failures: cap at maxVerifFailures, then let Critic handle.
					if strings.Contains(strings.ToLower(wErr.Error()), "verification") {
						verifFailures++
						if verifFailures > maxVerifFailures {
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.verif_retry_capped", "runtime", map[string]any{
								"file": writePath, "verif_failures": verifFailures,
								"reason": "verification retry cap reached — deferring to Critic",
							}, nil)
							break
						}
					} else if attempt > 0 {
						break // non-verification errors: only retry once
					}
					// Call LLM with error context to generate fix.
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.auto_debug_attempt", "runtime", map[string]any{
						"file": writePath, "attempt": attempt + 1, "error": wErr.Error(),
					}, nil)
					builderErrors = append(builderErrors, wErr.Error())
					// H-3: Read current file disk content for complete retry context.
					currentDiskContent := ""
					if diskBytes, diskErr := os.ReadFile(resolvedHealth); diskErr == nil {
						currentDiskContent = string(diskBytes)
					}
					fixCtx, fixCancel := context.WithTimeout(ctx, 120*time.Second)
					defer fixCancel()
					fixReq := llm.Request{
						SystemPrompt: appendPersonality(builderCodeGenSystemPrompt(), e.personality),
						UserPrompt:   builderFixPrompt(writePath, wErr.Error(), work.input, builderErrors, codeArtifactIDs, currentDiskContent),
						Model:        e.modelForRole(work.nodeRole),
						Tools:        e.builderLLMTools(),
						Category:     llm.CategoryCodeGeneration,
						MaxTokens:    16384,
					}
					applyBuilderCodeGenThinkMode(&fixReq)
					fixResp, fixErr := e.llm.Generate(fixCtx, fixReq)
					if fixErr == nil {
						appliedEdit := false
						for _, tc := range fixResp.ToolCalls {
							if tc.Name == "edit_file" {
								oldStr, _ := tc.Arguments["old_string"].(string)
								newStr, _ := tc.Arguments["new_string"].(string)
								path, _ := tc.Arguments["path"].(string)
								if path == "" {
									path = writePath
								}
								cleanPath := path
								if !filepath.IsAbs(cleanPath) {
									cleanPath = filepath.Join(workingDir, cleanPath)
								}
								body, rErr := os.ReadFile(cleanPath)
								if rErr == nil && oldStr != "" && strings.Count(string(body), oldStr) == 1 {
									replaced := strings.Replace(string(body), oldStr, newStr, 1)
									if wErr := writeFileAtomic(cleanPath, []byte(replaced)); wErr == nil {
										appliedEdit = true
										writeSucceeded = true
										_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.auto_debug_edit_applied", "runtime", map[string]any{"file": path}, nil)
										break
									}
								}
							}
						}
						if !appliedEdit {
							fc := ""
							for _, tc := range fixResp.ToolCalls {
								if tc.Name == "write_file" {
									if c, ok := tc.Arguments["content"].(string); ok && c != "" {
										fc = c
										break
									}
								}
							}
							if fc == "" {
								_, fc = parseDirectActionResponse(fixResp.Text, writePath)
							}
							// Only accept full rewrite if file does not exist yet.
							if strings.TrimSpace(fc) != "" {
								if _, st := os.Stat(resolvedPath); st != nil {
									writeInput.Content = fc
									writeInput.Overwrite = true
								} else {
									_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.auto_debug_full_rewrite_blocked", "runtime", map[string]any{
										"file": writePath, "reason": "existing file — require edit_file, not write_file",
									}, nil)
								}
							}
						}
					}
				}
				if writeSucceeded {
					codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-code-%s", filepath.Base(cf.Path)))
					// W13: Track written files for process_record memory.
					e.changedFiles = append(e.changedFiles, resolvedPath)
					// F62: checklist marks only after post-build green (syncWorkflowAfterBuild),
					// not on each write — otherwise Critic sees 6/6 while build is red.
					// W11b: Real-time progress — rich event with file name, line delta, and todo progress.
					afterLines := strings.Count(cf.Content, "\n")
					lineDelta := afterLines - beforeLines
					tdDone, tdTotal := workflow.CountTodoProgress(".")
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "workflow.file_completed", "runtime", map[string]any{
						"file":         writePath,
						"status":       "written",
						"before_lines": beforeLines,
						"after_lines":  afterLines,
						"line_delta":   lineDelta,
						"todo_done":    tdDone,
						"todo_total":   tdTotal,
					}, nil)
					_ = purgeEmptyBurialDirs(workingDir)
				} else {
					// C-4: No longer silent — emit a failure event so the user knows.
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.file_write_failed", "runtime", map[string]any{
						"file":            writePath,
						"verif_failures":  verifFailures,
						"builder_retries": maxBuilderRetries,
						"reason":          "all retries exhausted — file not written",
					}, nil)
					// H-2: Auto-rollback — if the file existed before, restore original content.
					if beforeContent != nil {
						if restoreErr := os.WriteFile(resolvedHealth, beforeContent, 0644); restoreErr == nil {
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.auto_rollback", "runtime", map[string]any{
								"file":   writePath,
								"reason": "restored original content after write failure",
							}, nil)
						}
					}
				}
			}
			// After writing sources / manifests, ensure dependency lockfiles / tidy.
			if len(codeArtifactIDs) > 0 {
				wd, _ := os.Getwd()
				_ = ensureGoModNormalized(wd)
				if needsGoModTidy(wd, codeFiles) {
					if tidyErr := runGoModTidy(wd); tidyErr == nil {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.go_mod_tidy", "runtime", map[string]any{
							"reason": "go.mod present or written — ran go mod tidy for go.sum",
						}, nil)
					} else {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.go_mod_tidy_failed", "runtime", map[string]any{
							"error": tidyErr.Error(),
						}, nil)
						if postBuildHealthErr == "" {
							postBuildHealthErr = "go mod tidy: " + tidyErr.Error()
						}
					}
				}
				// NL smoke Batch1/C: post-write cross-language health gate.
				if removed := resolveModulePackageShadowsOnDisk(wd); len(removed) > 0 {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.module_package_shadow_resolved", "runtime", map[string]any{
						"removed": removed,
					}, nil)
				}
				if healthErr := crossLangHealthCheck(wd); healthErr != "" {
					postBuildHealthErr = healthErr
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.post_build_failed", "runtime", map[string]any{
						"error": healthErr,
					}, nil)
					if e.retryHealthFailedBuilder(ctx, work, wd, healthErr) {
						if healthErr2 := crossLangHealthCheck(wd); healthErr2 != "" {
							postBuildHealthErr = healthErr2
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.post_build_failed", "runtime", map[string]any{
								"error":  healthErr2,
								"reason": "still red after health retry",
							}, nil)
						} else {
							postBuildHealthErr = ""
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.project_health_verified", "runtime", map[string]any{
								"kind":    "cross_lang_health",
								"verdict": "PASS",
								"reason":  "green after health retry",
							}, nil)
						}
					}
				} else if postBuildHealthErr == "" {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.project_health_verified", "runtime", map[string]any{
						"kind":    "cross_lang_health",
						"verdict": "PASS",
					}, nil)
				}
				// PY4: Phase1 /health success criteria — static route presence.
				if taskRequiresHealthEndpoint(work.input) && !healthEndpointPresentInTree(wd) {
					msg := "missing /health endpoint required by task/phase criteria"
					if postBuildHealthErr == "" {
						postBuildHealthErr = msg
					}
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.health_route_missing", "runtime", map[string]any{
						"error": msg,
					}, nil)
				} else if taskRequiresHealthEndpoint(work.input) && !healthPayloadLooksOK(wd) {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.health_payload_warn", "runtime", map[string]any{
						"warning": `/health present but payload may not include {"status":"ok"}`,
					}, nil)
				}
				// PY5 post-write: still missing migrations after stubs?
				if taskRequiresMigrations(work.input) && !migrationsLayoutPresent(wd) {
					msg := "missing migrations/ layout required by task"
					if postBuildHealthErr == "" {
						postBuildHealthErr = msg
					}
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.missing_layout", "runtime", map[string]any{
						"error": msg,
					}, nil)
				}
			}
			// P14-3a: Run interface compliance check on generated Go files.
			if len(codeArtifactIDs) > 0 {
				wd, _ := os.Getwd()
				genPaths := make([]string, 0, len(codeFiles))
				for _, cf := range codeFiles {
					genPaths = append(genPaths, cf.Path)
				}
				if reports, _ := verification.InterfaceCheckForGeneratedFiles(wd, genPaths); len(reports) > 0 {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.interface_compliance_failure", "runtime", map[string]any{
						"missing_methods": verification.FormatMissingMethodReport(reports),
					}, nil)
				}
			}
		}
		// P4: Dependency guard — scan generated files for unauthorized imports.
		if len(codeFiles) > 0 {
			if wd, wdErr := os.Getwd(); wdErr == nil {
				var gp []string
				for _, cf := range codeFiles {
					gp = append(gp, cf.Path)
				}
				if dw := CheckExternalDependencies(wd, gp); len(dw) > 0 {
					for _, d := range dw {
						_ = e.emit(work.runID, work.taskID, "critic", "reviewing",
							"critic.external_dependency_detected", "runtime", map[string]any{
								"import_path": d.ImportPath, "file": d.File, "stdlib_alt": d.StdlibAlt,
							}, nil)
					}
				}
				// Phase 6: Adversarial audit — type duplication detection.
				for _, af := range AdversarialAudit(wd, gp) {
					e.criticInsights = append(e.criticInsights,
						fmt.Sprintf("adversarial: %s in %s: %s", af.Kind, af.File, af.Detail))
					_ = e.emit(work.runID, work.taskID, "critic", "reviewing",
						"critic.adversarial_finding", "runtime", map[string]any{
							"kind": af.Kind, "file": af.File, "detail": af.Detail, "evidence": af.Evidence,
						}, nil)
				}
			}
		}
		if codeErr == nil {
			payload := map[string]any{
				"node_id":       work.nodeID,
				"files_written": len(codeArtifactIDs),
			}
			// W1: when early-converge used tool-loop disk writes, report them.
			if builderConvergedEarly {
				payload["converged_early"] = true
				if diskFilesAtConverge > 0 {
					payload["files_on_disk"] = diskFilesAtConverge
				}
				if len(codeArtifactIDs) == 0 && diskFilesAtConverge > 0 {
					payload["files_written_note"] = "tool-loop wrote scaffold before converge; finalize stubs may be 0"
				}
			}
			if emitErr := e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.code_implementation_completed", "runtime", payload, nil); emitErr != nil {
				// Non-fatal.
			}
		}
	}

	// B1: Deterministic edits — apply template-based file modifications
	// for common integration patterns (add import, add to list, add case).
	// This bypasses LLM entirely and handles what Builder always misses.
	// F29: must re-check project health AFTER these edits — they used to run
	// after project_health_verified and could inject compile poison unnoticed.
	detEditsApplied := 0
	if detEdits := workflow.ExtractDeterministicEdits(work.input); len(detEdits) > 0 {
		wd, _ := os.Getwd()
		for _, detEdit := range detEdits {
			applied, detErr := workflow.ApplyDeterministicEdit(wd, detEdit)
			if detErr != nil {
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.deterministic_edit_failed", "runtime", map[string]any{
					"file": detEdit.File, "operation": detEdit.Operation, "error": detErr.Error(),
				}, nil)
			} else if applied {
				detEditsApplied++
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.deterministic_edit_applied", "runtime", map[string]any{
					"file": detEdit.File, "operation": detEdit.Operation,
				}, nil)
				codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-det-%s", filepath.Base(detEdit.File)))
			}
		}
		if detEditsApplied > 0 {
			if healthErr := crossLangHealthCheck(wd); healthErr != "" {
				postBuildHealthErr = healthErr
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.post_build_failed", "runtime", map[string]any{
					"error":  healthErr,
					"reason": "health red after deterministic edits",
				}, nil)
			}
		}
	}

	// P11-2: Second pass — modify existing integration files one at a time.
	// Builder's LLM handles new files well but skips existing files.
	// Make focused per-file LLM calls for each existing file from the task.
	if len(codeArtifactIDs) > 0 && e.llm != nil {
		wd, _ := os.Getwd()
		dumpIgnored := 0
		const maxDumpIgnored = 3
		for _, targetPath := range extractTaskFilePaths(work.input) {
			if dumpIgnored >= maxDumpIgnored {
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.surgical_thrash_abort", "runtime", map[string]any{
					"reason": "per_file_full_dump_ignored_cap", "count": dumpIgnored,
				}, nil)
				break
			}
			resolvedTarget := targetPath
			if !filepath.IsAbs(resolvedTarget) {
				resolvedTarget = filepath.Join(wd, resolvedTarget)
			}
			existingBytes, readErr := os.ReadFile(resolvedTarget)
			if readErr != nil {
				continue
			}
			existingContent := string(existingBytes)
			promptContent := existingContent
			if len(promptContent) > 3000 {
				promptContent = promptContent[:3000] + "\n// [...file continues, modify only the relevant section...]"
			}
			focusedPrompt := fmt.Sprintf(
				"Modify %s with the SMALLEST change for the task.\nUse edit_file (path, old_string, new_string) or precise_edit (path, anchor, new_block, position).\nDo NOT call write_file. Do NOT dump the complete file.\n\nCurrent file:\n```\n%s\n```\n",
				targetPath, promptContent)
			perFileCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			perFileReq := llm.Request{
				SystemPrompt: appendPersonality("You are a surgical code editor. Prefer edit_file / precise_edit. Never rewrite whole existing files with write_file.", e.personality),
				UserPrompt:   focusedPrompt,
				Model:        e.modelForRole(work.nodeRole),
				Tools:        e.builderLLMTools(),
				Category:     llm.CategoryCodeGeneration,
				MaxToolTurns: builderMaxToolTurns(),
				MaxTokens:    8192,
			}
			applyBuilderCodeGenThinkMode(&perFileReq)
			perFileResp, perFileErr := e.llm.Generate(perFileCtx, perFileReq)
			cancel()
			// Prefer surgical tools; refuse text COMPLETE-file dumps (full rewrite trap).
			if len(perFileResp.ToolCalls) > 0 {
				for _, tc := range perFileResp.ToolCalls {
					switch tc.Name {
					case "edit_file":
						path, _ := tc.Arguments["path"].(string)
						oldStr, _ := tc.Arguments["old_string"].(string)
						newStr, _ := tc.Arguments["new_string"].(string)
						if path == "" || oldStr == "" {
							continue
						}
						cleanPath := filepath.Clean(path)
						body, err := os.ReadFile(cleanPath)
						if err != nil || strings.Count(string(body), oldStr) != 1 {
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.per_file_edit_failed", "runtime", map[string]any{"file": path, "reason": "old_string not unique"}, nil)
							continue
						}
						replaced := strings.Replace(string(body), oldStr, newStr, 1)
						if err := writeFileAtomic(cleanPath, []byte(replaced)); err != nil {
							continue
						}
						codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-perfile-edit-%s", filepath.Base(cleanPath)))
						e.changedFiles = append(e.changedFiles, cleanPath)
					case "precise_edit":
						// Already applied inside LLM tool loop when present; mark only.
						path, _ := tc.Arguments["path"].(string)
						if path != "" {
							codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-perfile-precise-%s", filepath.Base(path)))
						}
					case "write_file":
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.per_file_write_blocked", "runtime", map[string]any{
							"file": tc.Arguments["path"], "reason": "write_file blocked on existing file — use edit_file",
						}, nil)
					case "read_file", "git_diff", "shell_done":
						// Expected StandardCodeTools — do not spam per_file_tool_warning (F16).
					default:
						// F16: rate-limit unexpected-tool warnings (was 76× in one run).
						key := work.nodeID + "|" + tc.Name
						if e.perFileToolWarnCounts == nil {
							e.perFileToolWarnCounts = map[string]int{}
						}
						e.perFileToolWarnCounts[key]++
						n := e.perFileToolWarnCounts[key]
						if n <= 2 || n%20 == 0 {
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.per_file_tool_warning", "runtime", map[string]any{
								"tool": tc.Name, "count": n,
							}, nil)
						}
					}
				}
			} else if perFileErr != nil {
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.per_file_edit_error", "runtime", map[string]any{"file": targetPath, "error": perFileErr.Error()}, nil)
			} else if strings.TrimSpace(perFileResp.Text) != "" {
				// Text dump of COMPLETE file — try surgical hunks, never Overwrite.
				parsedPath, parsedContent := parseDirectActionResponse(perFileResp.Text, targetPath)
				if parsedPath == "" {
					parsedPath = targetPath
				}
				if strings.TrimSpace(parsedContent) == "" {
					parsedContent = perFileResp.Text
				}
				surg, _ := tools.SurgicalApplyFromDump(existingContent, parsedContent, parsedPath, wd)
				if surg.Applied() > 0 {
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.per_file_surgical_applied", "runtime", map[string]any{
						"file": parsedPath, "inserts": surg.Inserts, "replaces": surg.Replaces,
					}, nil)
					codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-perfile-surg-%s", filepath.Base(parsedPath)))
					e.changedFiles = append(e.changedFiles, resolvedTarget)
				} else {
					dumpIgnored++
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.per_file_full_dump_ignored", "runtime", map[string]any{
						"file": targetPath, "reason": surg.Reason,
					}, nil)
					// G2 escape: unhealthy on-disk + healthy proposed → one full rewrite.
					if checkFileHealth(parsedPath, existingContent) != "" &&
						checkFileHealth(parsedPath, parsedContent) == "" {
						if err := writeFileAtomic(resolvedTarget, []byte(parsedContent)); err == nil {
							_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.surgical_escape_full_rewrite", "runtime", map[string]any{
								"file": parsedPath, "reason": "per_file dump after unhealthy existing",
							}, nil)
							codeArtifactIDs = append(codeArtifactIDs, fmt.Sprintf("builder-perfile-escape-%s", filepath.Base(parsedPath)))
							e.changedFiles = append(e.changedFiles, resolvedTarget)
						}
					}
				}
			}
		}
	}

	if len(codeArtifactIDs) > 0 {
		wd, _ := os.Getwd()
		_ = ensureGoModNormalized(wd)
		if healthErr := crossLangHealthCheck(wd); healthErr != "" {
			postBuildHealthErr = healthErr
			_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.post_build_failed", "runtime", map[string]any{
				"error":  healthErr,
				"reason": "health red after per-file edits",
			}, nil)
			if e.retryHealthFailedBuilder(ctx, work, wd, healthErr) {
				if healthErr2 := crossLangHealthCheck(wd); healthErr2 != "" {
					postBuildHealthErr = healthErr2
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.post_build_failed", "runtime", map[string]any{
						"error":  healthErr2,
						"reason": "still red after health retry",
					}, nil)
				} else {
					postBuildHealthErr = ""
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.project_health_verified", "runtime", map[string]any{
						"kind":    "cross_lang_health",
						"verdict": "PASS",
						"reason":  "green after health retry",
					}, nil)
				}
			}
		}
	}

	// R2-2 / H10′: confirm+implement (and other code tasks) must leave real
	// sources on disk — a generated skill alone is not delivery (any language).
	// Use userFacingInput: injected phase banners contain "implementing"/"Generate".
	if needsCodeImplementation(userFacingInput) {
		wdCheck, _ := os.Getwd()
		wroteCode := len(codeArtifactIDs) > 0 || diskFilesAtConverge > 0
		if !wroteCode && deliveryLooksEmpty(wdCheck) {
			_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.skill_only_blocked", "runtime", map[string]any{
				"skill_path": prepared.Path,
				"reason":     "code task produced skill/docs only — no substantive source delivery",
			}, nil)
			return workflowNodeWorkResult{}, fmt.Errorf(
				"builder: code task delivered skill/docs only (no substantive sources on disk); refuse skill-only completion")
		}
	}

	// Auto-approve: when the engine flag is set, the candidate passes review,
	// AND ShouldAutoApprove quality gate passes (S3.8 / H-10), move it from
	// generated/ to approved/ instead of requiring manual /skills review.
	autoApproved := false
	reviewFindings := []string{}
	if e.autoApproveSkills {
		review, reviewErr := e.skills.ReviewGenerated(prepared.Path)
		if reviewErr == nil && review.ReadyForApproval && e.skills.ShouldAutoApprove(proposal.Name) {
			approvedPath, approveErr := e.skills.Approve(prepared.Path)
			if approveErr == nil {
				autoApproved = true
				prepared.Path = approvedPath
				_ = e.skills.RegenerateNavigator()
			}
		} else if reviewErr == nil && !review.ReadyForApproval && len(review.ValidationFindings) > 0 {
			// Review found issues: collect findings so the Builder
			// (or operator) can see what needs fixing before re-approval.
			for _, finding := range review.ValidationFindings {
				reviewFindings = append(reviewFindings, finding)
			}
		} else if reviewErr == nil && review.ReadyForApproval && !e.skills.ShouldAutoApprove(proposal.Name) {
			reviewFindings = append(reviewFindings, "auto-approve deferred: skill has not met ShouldAutoApprove quality threshold yet")
		}
	}
	skillPayload := map[string]any{
		"name":              proposal.Name,
		"path":              prepared.Path,
		"state":             "generated",
		"auto_approved":     autoApproved,
		"approval_required": !autoApproved,
	}
	if len(reviewFindings) > 0 {
		skillPayload["review_findings"] = reviewFindings
	}
	if err := e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "skill.generated", "skillbuilder", skillPayload, nil); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if autoApproved {
		if err := e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "skill.auto_approved", "skillbuilder", map[string]any{
			"name":          proposal.Name,
			"path":          prepared.Path,
			"state":         "approved",
			"auto_approved": true,
		}, nil); err != nil {
			return workflowNodeWorkResult{}, err
		}
	} else {
		if err := e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "skill.review_pending", "skillbuilder", map[string]any{
			"name":              proposal.Name,
			"path":              prepared.Path,
			"state":             "review_pending",
			"approval_required": true,
		}, nil); err != nil {
			return workflowNodeWorkResult{}, err
		}
	}
	if err := e.persistSkillCandidateEvaluation(work.runID, work.taskID, work.builderAvatarID, proposal.Name, "generated", prepared.Path, !autoApproved); err != nil {
		return workflowNodeWorkResult{}, err
	}
	builderMessage := ""
	if autoApproved {
		builderMessage = fmt.Sprintf("Generated and auto-approved skill at %s.", prepared.Path)
	} else {
		builderMessage = fmt.Sprintf("Generated candidate skill at %s. Approval is required before activation.", prepared.Path)
	}
	builderArtifacts := []ArtifactRef{{
		ID:      "generated-skill",
		Kind:    "skill",
		Path:    prepared.Path,
		Summary: fmt.Sprintf("Generated candidate skill at %s.", prepared.Path),
		OwnerID: work.builderAvatarID,
	}}
	if err := e.emitAvatarMessage(work.runID, work.taskID, work.builderAvatarID, "executing", "report", builderMessage, fmt.Sprintf("Builder report: generated candidate skill at %s.", prepared.Path), "build", nil, builderArtifacts); err != nil {
		return workflowNodeWorkResult{}, err
	}
	gate := e.latestNodeVerifierGate(work.runID, work.taskID, "write", true)
	if gate.Status == "pending_verification" && e != nil && e.verifier != nil {
		wdVerify, _ := os.Getwd()
		_, vErr := e.runVerification(ctx, toolCallEnvelope{
			runID:    work.runID,
			taskID:   work.taskID,
			avatarID: work.builderAvatarID,
			phase:    "reviewing",
		}, "write", verificationTarget{
			workingDir:   wdVerify,
			trigger:      "builder_node_completion",
			changedFiles: append([]string(nil), e.changedFiles...),
		})
		gate = e.latestNodeVerifierGate(work.runID, work.taskID, "write", true)
		if vErr != nil {
			if persistErr := e.persistNodeWorkEvidence(nodeWorkEvidence{
				RunID:                  work.runID,
				TaskID:                 work.taskID,
				AvatarID:               work.builderAvatarID,
				NodeID:                 work.nodeID,
				NodeRole:               work.nodeRole,
				NodeTitle:              work.nodeTitle,
				Tool:                   "write",
				Operation:              "file_write",
				Status:                 "needs_remediation",
				Summary:                fmt.Sprintf("Workflow node %s requires verifier remediation for write/file_write.", work.nodeID),
				ExpectedTargets:        writeInput.ExpectedTargets,
				VerifierGateStatus:     gate.Status,
				VerifierVerdict:        gate.Verdict,
				VerificationReportPath: gate.ReportPath,
				Verified:               gate.Verified,
				RecoveryKind:           "verifier_remediation",
			}); persistErr != nil {
				return workflowNodeWorkResult{}, persistErr
			}
			return workflowNodeWorkResult{}, vErr
		}
	}
	builderEvidenceSummary := fmt.Sprintf("Workflow node %s completed write/file_write evidence.", work.nodeID)
	if postBuildHealthErr != "" {
		builderEvidenceSummary += " post_build_failed: " + postBuildHealthErr + " build_ok=false"
	}
	evidenceStatus := "completed"
	if postBuildHealthErr != "" && isImportLayoutFailure(postBuildHealthErr) {
		evidenceStatus = "failed"
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:                  work.runID,
		TaskID:                 work.taskID,
		AvatarID:               work.builderAvatarID,
		NodeID:                 work.nodeID,
		NodeRole:               work.nodeRole,
		NodeTitle:              work.nodeTitle,
		Tool:                   "write",
		Operation:              "file_write",
		Status:                 evidenceStatus,
		Summary:                builderEvidenceSummary,
		ArtifactIDs:            artifactRefIDs(builderArtifacts),
		ExpectedTargets:        writeInput.ExpectedTargets,
		VerifierGateStatus:     gate.Status,
		VerifierVerdict:        gate.Verdict,
		VerificationReportPath: gate.ReportPath,
		Verified:               gate.Verified,
	}); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if evidenceStatus == "failed" {
		return workflowNodeWorkResult{
			artifactIDs:        artifactRefIDs(builderArtifacts),
			generatedSkillPath: prepared.Path,
			readSummary:        builderEvidenceSummary,
		}, fmt.Errorf("builder: post-build import/layout failure: %s", postBuildHealthErr)
	}
	return workflowNodeWorkResult{
		artifactIDs:        artifactRefIDs(builderArtifacts),
		generatedSkillPath: prepared.Path,
		readSummary:        builderEvidenceSummary,
	}, nil
}

// needsCodeImplementation detects whether a user task requires actual
// code file creation/modification (beyond the skill survey that the
// Builder always generates). Uses a conservative keyword heuristic.
func needsCodeImplementation(input string) bool {
	if planner.LooksLikeProgressReconcileTask(input) {
		return false
	}
	lowered := strings.ToLower(input)
	// Action verbs that indicate code-level work.
	actionVerbs := []string{
		"add", "create", "implement", "write", "generate", "build",
		"modify", "update", "register", "wire", "integrate", "change", "edit",
		"添加", "创建", "实现", "编写", "生成", "构建",
		"修改", "更新", "注册", "连接", "集成", "变更", "编辑",
		"plugin", "module", "feature", "function", "class",
		"插件", "模块", "功能", "函数",
	}
	for _, verb := range actionVerbs {
		if strings.Contains(lowered, verb) {
			return true
		}
	}
	// File extension signals: if the user mentions a specific source file
	// extension, they almost certainly want code generated.
	sourceExts := []string{".go", ".py", ".js", ".ts", ".rs", ".java", ".cs", ".ps1", ".sh", ".html", ".css"}
	for _, ext := range sourceExts {
		if strings.Contains(lowered, ext) {
			return true
		}
	}
	return false
}

type builderCodeFile struct {
	Path    string
	Content string
	// AlreadyOnDisk: surgical edit_file/precise_edit already applied in the LLM
	// tool loop — write loop must NOT full-overwrite this path.
	AlreadyOnDisk bool
}

// generateBuilderCode produces implementation files in one LLM pass.
// Splitting the prompt on English "and" (or similar) turned one user ask
// into N full completions. Prefix cache still hits; completion tokens do not.
func (e *Engine) generateBuilderCode(ctx context.Context, work workflowNodeWorkContext) ([]builderCodeFile, error) {
	return e.generateBuilderCodeSingle(ctx, work, work.input)
}

// generateBuilderCodeSingle generates code for one task, including recovery
// and gap-fill retries that reuse the same prompt prefix.
func (e *Engine) generateBuilderCodeSingle(ctx context.Context, work workflowNodeWorkContext, taskInput string) ([]builderCodeFile, error) {
	// Heartbeat: tell user we're about to call LLM (can take minutes).
	_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "heartbeat.builder_generating", "runtime", map[string]any{
		"message": fmt.Sprintf("Builder generating code for task (may take 2-3 minutes)..."),
	}, nil)

	prompt := builderCodeGenUserPrompt(work)
	if taskInput != work.input {
		prompt = strings.Replace(prompt, work.input, taskInput, 1)
	}
	// RetryContext: show Builder what it generated last time.
	if state := e.builderRetryStates[work.nodeID]; state != nil && state.AttemptNumber > 0 {
		var rc strings.Builder
		rc.WriteString("\n\n=== PREVIOUS ATTEMPT #")
		rc.WriteString(fmt.Sprintf("%d", state.AttemptNumber))
		rc.WriteString(" ===\nYou previously generated:\n")
		for _, f := range state.FilesGenerated {
			rc.WriteString("  - " + f + "\n")
		}
		rc.WriteString("\nBuild on previous work. Fix errors; don't start over.\n")
		prompt += rc.String()
	}
	var sysPrompt string
	if work.customSystemPrompt != "" {
		sysPrompt = work.customSystemPrompt
	} else {
		sysPrompt = builderCodeGenSystemPrompt()
	}
	if e != nil {
		sysPrompt = appendPersonality(sysPrompt, e.personality)
	}
	// S3.9: inject global + builder-role always-on skills (budgeted).
	sysPrompt = appendAlwaysOnRoleSkills(sysPrompt, work.alwaysOnSkills, "builder")
	targetLang := resolveBuilderTargetLanguage(taskInput, work.input)
	maxTokens := llm.DefaultMaxTokensForCode()
	if e != nil {
		if state := e.builderRetryStates[work.nodeID]; state != nil && state.EscalatedMaxTokens > 0 {
			maxTokens = state.EscalatedMaxTokens
		}
	}
	apiLock := ""
	if wd, err := os.Getwd(); err == nil {
		apiLock = extractCrossLangAPILock(wd)
	}
	// Keep system prompt byte-stable for provider prefix cache. Per-task
	// LANGUAGE LOCK / API lock / TOKEN LIMIT / plan review go on the user turn
	// *after* the task text so the user-prefix (Task: …) stays identical
	// across Builder retries while extras (API lock grows as files land) change.
	prompt = prompt + builderPerTaskUserExtras(work.planReviewFindings, targetLang, extractTaskFilePaths(taskInput), apiLock, maxTokens)
	prompt = capBuilderUserPrompt(prompt)
	if lock := workflow.DeliveryLocksUserNote(taskInput); lock != "" {
		prompt += lock
	}
	if state := e.builderRetryStates[work.nodeID]; state != nil && state.HealthFailedRetried && strings.TrimSpace(state.CompileError) != "" {
		prompt += healthRepairUserSuffix(state.CompileError)
	}

	llmReq := llm.Request{
		SystemPrompt: sysPrompt,
		UserPrompt:   prompt,
		Model:        e.modelForRole(work.nodeRole),
		Tools:        e.builderLLMTools(),
		Category:     llm.CategoryCodeGeneration,
		MaxTokens:    maxTokens,
		MaxToolTurns: builderMaxToolTurns(),
	}
	applyBuilderCodeGenThinkMode(&llmReq)
	if state := e.builderRetryStates[work.nodeID]; state != nil && state.EscalatedMaxToolTurns > llmReq.MaxToolTurns {
		llmReq.MaxToolTurns = state.EscalatedMaxToolTurns
	}
	// Stream content/tool deltas into jsonl; thinking stays on ThinkingCallback.
	var thinkingChars int
	var lastThinkingEmit time.Time
	var thinkingBuf strings.Builder
	e.attachContentStreamHeartbeats(&llmReq, work.runID, work.taskID, work.builderAvatarID, "executing", work.nodeID)
	llmReq.ThinkingCallback = func(chunk string) {
		thinkingChars += len(chunk)
		thinkingBuf.WriteString(chunk)
		now := time.Now()
		if lastThinkingEmit.IsZero() || now.Sub(lastThinkingEmit) >= 2*time.Second {
			lastThinkingEmit = now
			_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "llm.thinking", "llm", map[string]any{
				"chars":   thinkingChars,
				"node_id": work.nodeID,
				"preview": thinkingPreviewTail(thinkingBuf.String(), 240),
				"message": fmt.Sprintf("thinking… (%d chars)", thinkingChars),
			}, nil)
		}
	}
	llmTimeout := builderCodeGenLLMTimeout(llmReq)
	llmCtx, llmCancel := context.WithTimeout(ctx, llmTimeout)
	defer llmCancel()
	e.emitLLMPromptCost(work.runID, work.taskID, work.builderAvatarID, "executing", "Builder", llmReq)
	response, err := e.llm.Generate(llmCtx, llmReq)
	if err != nil {
		failPayload := map[string]any{
			"provider": e.llm.Provider(),
			"error":    err.Error(),
			"node_id":  work.nodeID,
		}
		mergePromptObservability(failPayload, "Builder", llmReq.SystemPrompt, llmReq.UserPrompt)
		mergeLLMUsage(failPayload, response.Usage)
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "llm.failed", "llm", failPayload, nil)
		return nil, err
	}
	if response.Fallback {
		return nil, fmt.Errorf("Builder LLM returned deterministic fallback for code generation")
	}
	e.emitBuilderLLMCompleted(work, llmReq, response)
	if strings.TrimSpace(response.Text) == "" && len(response.ToolCalls) == 0 {
		// F8 belt: one degrade retry with ThinkModeOff + explicit "emit tools now"
		// (covers callers that somehow left thinking on, or empty first pass).
		_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.codegen_empty_retry", "runtime", map[string]any{
			"node_id":    work.nodeID,
			"think_mode": string(llm.ThinkModeOff),
			"message":    "empty content/tool calls — retry with thinking off",
		}, nil)
		retryReq := llmReq
		retryReq.ThinkMode = llm.ThinkModeOff
		retryReq.UserPrompt = prompt + "\n\nIMPORTANT: Do not spend tokens on silent reasoning. Immediately emit write_file/edit_file tool calls or FILE: blocks with real code."
		retryCtx, retryCancel := context.WithTimeout(ctx, llmTimeout)
		defer retryCancel()
		retryResp, retryErr := e.llm.Generate(retryCtx, retryReq)
		if retryErr != nil {
			return nil, retryErr
		}
		if retryResp.Fallback {
			return nil, fmt.Errorf("Builder LLM returned deterministic fallback for code generation")
		}
		if strings.TrimSpace(retryResp.Text) == "" && len(retryResp.ToolCalls) == 0 {
			return nil, fmt.Errorf("Builder LLM returned empty response for code generation. Check LLM configuration.")
		}
		response = retryResp
		e.emitBuilderLLMCompleted(work, retryReq, response)
	}
	// P0-1: Build file list. Prefer tool call arguments (authoritative LLM output)
	// over disk scan (may pick up stale/partial files from previous runs).
	files := parseBuilderCodeResponse(response.Text)
	if len(response.ToolCalls) > 0 {
		var fromTools []builderCodeFile
		surgicallyEdited := map[string]bool{}
		for _, tc := range response.ToolCalls {
			switch tc.Name {
			case "edit_file", "precise_edit":
				path, _ := tc.Arguments["path"].(string)
				if path == "" {
					continue
				}
				clean := filepath.Clean(path)
				if surgicallyEdited[clean] {
					continue
				}
				surgicallyEdited[clean] = true
				fromTools = append(fromTools, builderCodeFile{Path: path, AlreadyOnDisk: true})
			case "write_file":
				path, _ := tc.Arguments["path"].(string)
				content, _ := tc.Arguments["content"].(string)
				if path == "" || content == "" {
					continue
				}
				if surgicallyEdited[filepath.Clean(path)] {
					continue // surgical edit already applied — ignore full rewrite
				}
				// F105: keep the rewrite-after path so later writes/progress match disk.
				wd := strings.TrimSpace(e.projectRoot)
				if wd == "" {
					wd, _ = os.Getwd()
				}
				if lifted, from := applyWritePathRewrite(wd, work.input, path, content); lifted != "" {
					if from != "" {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.path_sanitized", "runtime", map[string]any{
							"from": from, "to": lifted, "reason": "write_file remapped_from (F105)",
						}, nil)
					}
					path = lifted
				}
				fromTools = append(fromTools, builderCodeFile{Path: path, Content: content})
			}
		}
		if len(fromTools) > 0 {
			files = fromTools
		}
	}
	if len(files) == 0 {
		// Fallback: scan disk for recently modified files.
		files = findRecentlyWrittenFiles(work)
	}
	// I35: Truncation detection and recovery (hooks.go pattern).
	if len(files) > 0 {
		truncResult := CheckBuilderTruncation(response, files)
		if truncResult.IsTruncated {
			apiCut := llm.IsTruncated(response)
			skipRetry := false
			if !apiCut {
				wd := strings.TrimSpace(e.projectRoot)
				if wd == "" {
					wd, _ = os.Getwd()
				}
				if wd != "" && diskHasAppSources(wd) && crossLangHealthCheck(wd) == "" {
					skipRetry = true
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.completeness_ignored", "runtime", map[string]any{
						"reason":  "provider finished; on-disk compile/test already green",
						"issues":  truncResult.Issues,
						"node_id": work.nodeID,
					}, nil)
				}
				if !skipRetry && builderFilesLookComplete(files) {
					skipRetry = true
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.completeness_ignored", "runtime", map[string]any{
						"reason":  "provider finished; generated sources parse as complete",
						"issues":  truncResult.Issues,
						"node_id": work.nodeID,
					}, nil)
				}
			}
			if !skipRetry {
				state := e.ensureBuilderRetryState(work.nodeID)
				state.TruncationCount++
				if state.TruncationCount <= MaxTruncationRecoveries {
					state.EscalatedMaxTokens = EscalateMaxTokens(state.EscalatedMaxTokens)
					contentPrefixes := make(map[string]string)
					for _, f := range files {
						for _, inc := range truncResult.IncompleteFiles {
							if f.Path == inc && len(f.Content) > 0 {
								contentPrefixes[f.Path] = f.Content
							}
						}
					}
					recoveryPrompt := BuildRecoveryPromptWithContent(truncResult.IncompleteFiles, contentPrefixes)
					_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.truncation_recovery", "runtime", map[string]any{
						"attempt":          state.TruncationCount,
						"escalated_tokens": state.EscalatedMaxTokens,
						"finish_reason":    truncResult.FinishReason,
						"incomplete_files": truncResult.IncompleteFiles,
						"issues":           truncResult.Issues,
					}, nil)
					root := strings.TrimSpace(e.projectRoot)
					if root == "" {
						root = "."
					}
					if err := persistBuilderRetryStates(root, e.builderRetryStates); err != nil {
						_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.retry_persist_failed", "runtime", map[string]any{
							"error":   err.Error(),
							"node_id": work.nodeID,
							"kind":    "builder_retry_states",
						}, nil)
					}
					return e.generateBuilderCodeSingle(ctx, work, taskInput+recoveryPrompt)
				}
				// Exceeded recovery limit — remove unhealthy tool-written files
				// before returning so truncated content cannot break go test/build.
				e.purgeUnhealthyBuilderFilesFromDisk(work, files)
				_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.truncation_exhausted", "runtime", map[string]any{
					"attempts":      state.TruncationCount,
					"finish_reason": truncResult.FinishReason,
					"issues":        truncResult.Issues,
				}, nil)
				return nil, fmt.Errorf("Builder output truncated after %d recovery attempts: %v",
					state.TruncationCount, truncResult.Issues)
			}
		}
		// Keep TruncationCount across nested generateBuilderCodeSingle calls
		// in the same node. Resetting here let heuristic-false positives
		// recurse forever: each "success" zeroed the counter.
	}
	// BUG-3: Tool-calling path bypasses pre-write checkFileHealth. Purge
	// unhealthy tool-written files from disk before continuing.
	if len(files) > 0 {
		files = e.purgeUnhealthyBuilderFilesFromDisk(work, files)
	}
	if len(files) == 0 {
		// Batch5/M / G11: completion prose + green build → converge only when
		// disk already has substantive sources (never empty-stub fake converge).
		if builderProseSaysComplete(response.Text) {
			if wd, wdErr := os.Getwd(); wdErr == nil && !deliveryLooksEmpty(wd) && crossLangHealthCheck(wd) == "" {
				// P9-4: still finalize layout stubs (migrations/, declared packages).
				return collectBuilderFinalizeGaps(wd, taskInput, nil), nil
			}
		}
		return nil, fmt.Errorf("Builder produced no files. LLM response: %s", truncateForLog(response.Text, 200))
	}

	// CODE-LEVEL cross-language hallucination interception.
	// Even if LLM ignores the LANGUAGE LOCK prompt, we filter deterministically.
	if targetLang != "" {
		kept, rejected := filterCrossLanguageFiles(files, targetLang)
		if len(rejected) > 0 {
			rejectedPaths := make([]string, len(rejected))
			for i, r := range rejected {
				rejectedPaths[i] = r.Path
			}
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.cross_language_blocked", "runtime",
				map[string]any{
					"target_language": targetLang,
					"rejected_files":  strings.Join(rejectedPaths, ", "),
					"kept_files":      len(kept),
				}, nil)
		}
		files = kept
	}

	// File whitelist: ONLY allow files mentioned in the original task OR files
	// that already exist on disk (to be modified). This prevents LLM hallucinations
	// like generating survey.py and index.js for a simple calc.py task.
	allowedPaths := extractTaskFilePaths(work.input)
	if len(allowedPaths) > 0 {
		allowedSet := make(map[string]bool, len(allowedPaths))
		for _, p := range allowedPaths {
			allowedSet[filepath.ToSlash(filepath.Clean(p))] = true
		}
		var whitelisted []builderCodeFile
		var blocked []string
		for _, f := range files {
			normalized := filepath.ToSlash(filepath.Clean(f.Path))
			// Allow if in task targets, or file already exists (modification task).
			if allowedSet[normalized] {
				whitelisted = append(whitelisted, f)
			} else if _, statErr := os.Stat(f.Path); statErr == nil {
				whitelisted = append(whitelisted, f) // existing file, allow modification
			} else {
				blocked = append(blocked, f.Path)
			}
		}
		if len(blocked) > 0 {
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.file_whitelist_blocked", "runtime",
				map[string]any{
					"blocked_files": strings.Join(blocked, ", "),
					"kept_files":    len(whitelisted),
				}, nil)
		}
		files = whitelisted
	}

	// NL smoke #14-16: reject nested workflow docs, nested packages, misplaced root configs.
	const protectedRedirectHint = "CRITICAL: Do NOT write avatars_todo.md, avatars_plan.md, or process_record"
	if sanitized, redirected, dropped := sanitizeBuilderPathsForTask(files, work.input); true {
		if len(redirected) > 0 || len(dropped) > 0 {
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.path_sanitized", "runtime", map[string]any{
				"redirected": redirected,
				"dropped":    dropped,
				"kept":       len(sanitized),
			}, nil)
		}
		// V8: hard PHASE LOCK Phase 1 — drop known later-phase model files.
		if workflow.ParsePhaseLock(work.input) == 1 ||
			strings.Contains(work.input, "PHASE LOCK (Phase 1") {
			var scoped []builderCodeFile
			var scopeDropped []string
			for _, f := range sanitized {
				if isFuturePhaseModelPath(f.Path) {
					scopeDropped = append(scopeDropped, f.Path)
					continue
				}
				scoped = append(scoped, f)
			}
			if len(scopeDropped) > 0 {
				_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.phase_scope_dropped", "runtime", map[string]any{
					"dropped": scopeDropped,
					"reason":  "Phase 1 lock — defer tag/version/search models",
				}, nil)
				sanitized = scoped
			}
		}
		files = sanitized
		// F71: after unbury remaps, drop empty internal/private shells so Explorer
		// never shows a create→delete flicker of burial dirs.
		if purged := purgeEmptyBurialDirs("."); len(purged) > 0 {
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.burial_dirs_purged", "runtime", map[string]any{
				"removed": purged,
				"reason":  "empty burial dirs after public-lib unbury (F71)",
			}, nil)
		}
		// Batch6/T: if Builder only touched protected workflow SoT, one forced
		// redirect to source files — never silently complete with 0 writes.
		if len(sanitized) == 0 && protectedOnlyDrops(dropped) &&
			!strings.Contains(work.input, protectedRedirectHint) {
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.protected_redirect", "runtime", map[string]any{
				"dropped": dropped,
				"reason":  "Builder only wrote protected workflow manifests — redirecting to source files",
			}, nil)
			redirectPrompt := work.input + "\n\n" + protectedRedirectHint +
				". Those files are system-managed. Implement ONLY source code / migrations / configs " +
				"(e.g. go.mod, cmd/, internal/, migrations/*.sql). Do not rewrite workflow docs."
			gapFiles, gapErr := e.generateBuilderCodeSingle(ctx, work, redirectPrompt)
			if gapErr != nil {
				return nil, fmt.Errorf("Builder only attempted protected workflow manifests and redirect failed: %w", gapErr)
			}
			files = gapFiles
			if reSanitized, reRedir, reDrop := sanitizeBuilderPathsForTask(files, work.input); true {
				if len(reRedir) > 0 || len(reDrop) > 0 {
					_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.path_sanitized", "runtime", map[string]any{
						"redirected": reRedir,
						"dropped":    reDrop,
						"kept":       len(reSanitized),
						"pass":       "after_protected_redirect",
					}, nil)
				}
				files = reSanitized
			}
			if len(files) == 0 {
				return nil, fmt.Errorf("Builder produced no writable source files after protected-manifest redirect")
			}
		} else {
			files = sanitized
		}
	}

	// Post-generation gap check: if the task mentions files that Builder
	// didn't generate, force a second call to fill them in. This prevents
	// Builder from silently skipping required files like src/lib.rs.
	mandatoryPaths := extractTaskFilePaths(work.input)
	if len(mandatoryPaths) > 0 {
		generatedSet := make(map[string]bool, len(files))
		for _, f := range files {
			generatedSet[filepath.ToSlash(filepath.Clean(f.Path))] = true
		}
		var missing []string
		for _, mp := range mandatoryPaths {
			normalized := filepath.ToSlash(filepath.Clean(mp))
			if !generatedSet[normalized] {
				// Check if file already exists on disk (from previous run).
				if _, statErr := os.Stat(mp); statErr != nil {
					missing = append(missing, mp)
				}
			}
		}
		if len(missing) > 0 {
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.missing_files", "runtime",
				map[string]any{
					"missing":     strings.Join(missing, ", "),
					"total_files": len(files),
				}, nil)
			// Force regeneration for missing files with explicit instructions.
			gapPrompt := fmt.Sprintf("MUST generate these files that were missing from the previous output:\n%s\n\nOriginal task:\n%s",
				strings.Join(missing, "\n"), work.input)
			if gapFiles, gapErr := e.generateBuilderCodeSingle(ctx, work, gapPrompt); gapErr == nil {
				files = append(files, gapFiles...)
			} else {
				// Last resort: create minimal stubs so verification loop can fill them.
				// N1-2 / T3: never stub dependency names, junk scaffold paths, or non-sources.
				for _, mp := range missing {
					if !isSourceExtForMandatoryStub(mp) || isJunkScaffoldPath(mp) ||
						isDependencyOrRuntimeFilename(mp) {
						_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.stub_skipped_dep_name", "runtime",
							map[string]any{"file": mp, "reason": "junk/dep/non-source path — not stubbing"}, nil)
						// Purge if a prior bad write already landed (T3 sql.js leftover).
						if wd, wdErr := os.Getwd(); wdErr == nil {
							full := mp
							if !filepath.IsAbs(full) {
								full = filepath.Join(wd, mp)
							}
							_ = os.Remove(full)
						}
						continue
					}
					stub := createMinimalStub(mp, targetLang)
					if strings.TrimSpace(stub) == "" {
						continue
					}
					files = append(files, builderCodeFile{Path: mp, Content: stub})
					_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.stub_created", "runtime",
						map[string]any{"file": mp, "reason": "LLM failed to generate, created stub for verification loop"}, nil)
				}
			}
		}
	}

	// PY5 / L4: task asked for migrations/ — stub immediately (LLM gap-regen
	// often burns minutes without SQL). Prefer richer DDL when we can.
	if stub := checkMissingMigrationsLayout(".", work.input, files); stub != "" {
		_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.missing_layout", "runtime",
			map[string]any{
				"missing": stub,
				"reason":  "task requires migrations/ layout",
			}, nil)
		files = append(files, builderCodeFile{
			Path:    stub,
			Content: defaultMigrationsStubContent(stub, work.input),
		})
		_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.stub_created", "runtime",
			map[string]any{"file": stub, "reason": "migrations layout required by task — stubbed immediately"}, nil)
	}
	// W6: app/api package placeholder when task requires that layout.
	if stub := checkMissingAPIPackage(".", work.input, files); stub != "" {
		files = append(files, builderCodeFile{
			Path:    stub,
			Content: "# API package (Phase 1 placeholder)\n",
		})
		_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.stub_created", "runtime",
			map[string]any{"file": stub, "reason": "task requires app/api/ package"}, nil)
	}

	return files, nil
}

// extractTaskFilePaths extracts file paths from task descriptions.
// N1-2: bare names like "sql.js" / "Node.js" are npm/runtime labels, not project
// paths — never treat them as mandatory files (avoids stub pollution).
func extractTaskFilePaths(taskInput string) []string {
	re := regexp.MustCompile(`(?:[a-zA-Z0-9_]+/)*[a-zA-Z0-9_]+\.(go|rs|py|jsx?|tsx?|yaml|yml|json|toml|mod|sum|md|txt)`)
	matches := re.FindAllString(taskInput, -1)
	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		m = strings.TrimSpace(m)
		if strings.Contains(m, "://") {
			continue
		}
		if isDependencyOrRuntimeFilename(m) || isJunkScaffoldPath(m) {
			continue
		}
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	return result
}

// isDependencyOrRuntimeFilename reports filenames that look like language
// runtimes or published packages (sql.js, Node.js, express.js…), not app sources.
// T3: basename match applies even under directories (src/types/sql.js).
func isDependencyOrRuntimeFilename(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	return isDependencyOrRuntimeBasename(filepath.Base(path))
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// alignGoTestWaitKeys rewrites waitForDups(t, g, "a") to the sole Do("b")
// key used in the same Test* function. Language-specific to Go helpers;
// other languages keep the user-prompt rule that wait keys must match calls.
func alignGoTestWaitKeys(src string) string {
	if !strings.Contains(src, "waitForDups") {
		return src
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x_test.go", src, 0)
	if err != nil {
		return src
	}
	type repl struct {
		start, end int
		text       string
	}
	var reps []repl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		var doKeys []string
		seen := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if astCallName(call) != "Do" || len(call.Args) < 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key := strings.Trim(lit.Value, `"`)
			if key == "" || seen[key] {
				return true
			}
			seen[key] = true
			doKeys = append(doKeys, key)
			return true
		})
		if len(doKeys) != 1 {
			continue
		}
		want := doKeys[0]
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if astCallName(call) != "waitForDups" || len(call.Args) < 3 {
				return true
			}
			lit, ok := call.Args[2].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			got := strings.Trim(lit.Value, `"`)
			if got == want {
				return true
			}
			start := fset.Position(lit.Pos()).Offset
			end := fset.Position(lit.End()).Offset
			if start < 0 || end <= start || end > len(src) {
				return true
			}
			reps = append(reps, repl{start: start, end: end, text: strconv.Quote(want)})
			return true
		})
	}
	if len(reps) == 0 {
		return src
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].start > reps[j].start })
	out := src
	for _, r := range reps {
		out = out[:r.start] + r.text + out[r.end:]
	}
	return out
}

func astCallName(call *ast.CallExpr) string {
	switch x := call.Fun.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if x.Sel != nil {
			return x.Sel.Name
		}
	}
	return ""
}

// validateBuilderGoImports checks Go import paths in Builder-generated code
// against the actual directory structure, fixing hallucinated imports.
func validateBuilderGoImports(workingDir string, goSource string) (string, []string) {
	var warnings []string
	moduleName := readModuleName(workingDir)
	if moduleName == "" {
		return goSource, nil
	}

	importRe := regexp.MustCompile(`"([^"]+)"`)
	lines := strings.Split(goSource, "\n")
	inImportBlock := false
	var fixedLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import (") {
			inImportBlock = true
			fixedLines = append(fixedLines, line)
			continue
		}
		if inImportBlock && trimmed == ")" {
			inImportBlock = false
			fixedLines = append(fixedLines, line)
			continue
		}

		if inImportBlock && strings.HasPrefix(trimmed, "//") {
			fixedLines = append(fixedLines, line)
			continue
		}

		if !inImportBlock && !strings.HasPrefix(trimmed, "import ") {
			fixedLines = append(fixedLines, line)
			continue
		}

		matches := importRe.FindAllStringSubmatch(line, -1)
		removeLine := false
		for _, m := range matches {
			importPath := m[1]
			if strings.ContainsAny(importPath, " \t:") {
				continue
			}
			if !strings.HasPrefix(importPath, moduleName) {
				continue
			}
			relPath := strings.TrimPrefix(importPath, moduleName+"/")
			absPath := filepath.Join(workingDir, filepath.FromSlash(relPath))
			if _, err := os.Stat(absPath); os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("import %s: directory %s does not exist — removed", importPath, relPath))
				removeLine = true
			}
		}
		if !removeLine {
			fixedLines = append(fixedLines, line)
		}
	}

	if len(warnings) > 0 {
		return strings.Join(fixedLines, "\n"), warnings
	}
	return goSource, nil
}

// readModuleName extracts the Go module name from go.mod.
func readModuleName(workingDir string) string {
	data, err := os.ReadFile(filepath.Join(workingDir, "go.mod"))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`^module\s+(\S+)`)
	m := re.FindSubmatch(data)
	if len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

// createMinimalStub generates a minimal valid file stub when the LLM fails to
// generate a required file. T2: never emit `# … — stub` for JS/TS (breaks tsc).
func createMinimalStub(path, language string) string {
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)
	switch {
	case ext == ".go":
		pkg := "main"
		if strings.Contains(path, "/") || strings.Contains(path, "\\") {
			pkg = filepath.Base(filepath.Dir(path))
			pkg = strings.ReplaceAll(pkg, "-", "_")
			if pkg == "." || pkg == "" {
				pkg = "main"
			}
		}
		return fmt.Sprintf("package %s\n\n// Placeholder — replace with real implementation.\n", pkg)
	case ext == ".rs":
		return "// Placeholder — replace with real implementation.\n"
	case ext == ".py":
		return "# Placeholder — replace with real implementation.\n"
	case ext == ".js", ext == ".mjs", ext == ".cjs", ext == ".jsx":
		return "// Placeholder — replace with real implementation.\nexport {};\n"
	case ext == ".ts", ext == ".tsx":
		// Valid TS — never '#' (createMinimalStub default used to emit "# name — stub").
		return "// Placeholder — replace with real implementation.\nexport {};\n"
	case ext == ".toml" && base == "Cargo.toml":
		return "[package]\nname = \"stub\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"
	default:
		return ""
	}
}

// builderFixPrompt generates a Claude Code-style targeted fix prompt.
// Instead of "regenerate from scratch", it teaches the LLM to:
// 1. Read the compiler error to find exact file:line:problem
// 2. Make MINIMAL edits at those specific locations
// 3. NOT regenerate the entire file
// builderFixPrompt builds the LLM prompt for a Builder retry attempt.
// H-3: Now includes complete context — already-written files list and
// current file disk content (truncated) — so the LLM has enough info
// to actually fix the problem rather than guessing.
func builderFixPrompt(filePath, compilerError, originalTask string, previousErrors []string, writtenFiles []string, currentFileContent string) string {
	prevSection := ""
	if len(previousErrors) > 0 {
		var b strings.Builder
		b.WriteString("\n## Previous Attempts\n")
		start := 0
		if len(previousErrors) > 3 {
			start = len(previousErrors) - 3
		}
		for _, e := range previousErrors[start:] {
			b.WriteString("- FAIL: ")
			b.WriteString(e)
			b.WriteString("\n")
		}
		prevSection = b.String()
	}

	// H-3: Already-written files list so LLM doesn't duplicate work.
	writtenSection := ""
	if len(writtenFiles) > 0 {
		var b strings.Builder
		b.WriteString("\n## Already-Written Files (do NOT rewrite these)\n")
		for _, f := range writtenFiles {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
		writtenSection = b.String()
	}

	// H-3: Current file disk content (truncated to 2000 chars) so LLM
	// can see the actual state of the file it's fixing.
	fileStateSection := ""
	if strings.TrimSpace(currentFileContent) != "" {
		content := currentFileContent
		if len(content) > 2000 {
			content = content[:2000] + "\n// [...file continues, fix only the relevant section...]"
		}
		fileStateSection = fmt.Sprintf("\n## Current File State (on disk)\n```\n%s\n```\n", content)
	}

	return fmt.Sprintf(
		`FIX ONLY THE COMPILER ERRORS in %s — do NOT regenerate the entire file.
%s%s%s
COMPILER ERROR:
%s

ORIGINAL TASK: %s

RULES (follow exactly):
1. The error above tells you the EXACT file, line number, and problem.
2. Fix ONLY what the error describes. Make the MINIMAL edit possible.
3. If error says "line N: non-declaration statement outside function body":
   → Everything AFTER the last closing } of a function is garbage. DELETE those lines.
4. If error says "name redeclared" in a struct:
   → Remove the DUPLICATE field declaration, keep only the FIRST one.
5. If error says "missing return" or "expected }":
   → Add exactly what's missing at the stated line.
6. If error says "too few values in struct literal":
   → The struct has more fields than the literal provides. Align the literal with the struct definition.
7. Use the edit_file tool to make the MINIMAL fix. Do NOT regenerate the entire file — only fix the broken lines.
8. Do NOT add new features. Do NOT rename things. Do NOT change working code.
9. NEW-8: If previous fix attempts failed with the SAME error, try a DIFFERENT approach — the same fix won't work again.
10. H-3: The "Current File State" section shows what's ACTUALLY on disk. Fix from that state, not from memory.`,
		filePath, prevSection, writtenSection, fileStateSection, compilerError, originalTask,
	)
}

func builderCodeGenSystemPrompt() string {
	return prompt.BuilderStablePrefix + "\n\n" + `You are a Builder avatar. Write code files NOW using write_file. Do NOT read files for new projects.
Your ONLY job: call write_file for each requested file, then STOP.

TOOLS: write_file (new) and edit_file (existing).

RULES:
	0. NEW PROJECT: write_file per file. DO NOT read_file. DO NOT analyze. Write NOW.
	0b. MODIFICATION: read_file first, then edit_file with minimal changes.
	1. Task is the spec. Follow it exactly — no extra features, no gold-plating.
	2. Every file COMPLETE — all braces closed, no truncated functions, no placeholders.
	3. Task asks for tests → output BOTH implementation AND test files.
	4. Reuse types from the API LOCK section in the user turn. Do not create duplicates.
	5. One entry point per project. Check for an existing main before creating one.
	6. Standard library only unless the task or project manifest requires external packages.
	7. After ALL planned files are on disk, run the language build/test runner. Mid-write PASS is not completion.
	8. If task says "modify X" or "fill in X", use path X exactly. Do not create new files for modifications.
	9. Do not wrap output in markdown fences. No explanations between files.`
}

func builderPerTaskUserExtras(planReview, targetLang string, paths []string, apiLock string, maxTokens int) string {
	var b strings.Builder
	if strings.TrimSpace(planReview) != "" {
		b.WriteString("=== PLAN REVIEW FINDINGS (address before writing code) ===\n")
		b.WriteString(planReview)
		b.WriteString("\n=== END PLAN REVIEW ===\n\n")
	}
	if targetLang != "" {
		exts := getLanguageExtensions(targetLang)
		b.WriteString(fmt.Sprintf("LANGUAGE LOCK: This is a %s project. ONLY output files with extensions: %s. Do NOT create files in any other language.\n\n",
			targetLang, strings.Join(exts, ", ")))
	}
	if len(paths) > 0 {
		b.WriteString("MANDATORY FILE PATHS FROM TASK:\n")
		for _, p := range paths {
			b.WriteString("- MUST use path: ")
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("Listed paths must exist (keep these names). Companion test files are required when the task asks for tests — *_test.go, test_*.py, *.test.ts, *_test.rs, and language equivalents. Do not skip tests because they were omitted from this list.\n\n")
	}
	if al := strings.TrimSpace(apiLock); al != "" {
		const maxAPILock = 8000
		if len(al) > maxAPILock {
			al = al[:maxAPILock] + "\n[...truncated...]"
		}
		b.WriteString(al)
		b.WriteString("\n\n")
	}
	if maxTokens > 0 {
		b.WriteString(fmt.Sprintf("TOKEN LIMIT: %d max. Write code, then STOP. Do NOT waste tool calls.\n\n", maxTokens))
	}
	return b.String()
}

func (e *Engine) emitBuilderLLMCompleted(work workflowNodeWorkContext, req llm.Request, response llm.Response) {
	if e == nil {
		return
	}
	payload := map[string]any{
		"provider": response.Provider,
		"model":    response.Model,
		"node_id":  work.nodeID,
	}
	if strings.TrimSpace(response.Provider) == "" && e.llm != nil {
		payload["provider"] = e.llm.Provider()
	}
	mergePromptObservability(payload, "Builder", req.SystemPrompt, req.UserPrompt)
	mergeLLMUsage(payload, response.Usage)
	_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "llm.completed", "llm", payload, nil)
}

func builderCodeGenUserPrompt(work workflowNodeWorkContext) string {
	var b strings.Builder
	b.WriteString("Task: ")
	b.WriteString(work.input)
	b.WriteString("\n\n")

	// === Structured Context (PRIMARY — exact signatures extracted
	// deterministically from source files, no LLM guesswork needed) ===
	if work.structuredContext != nil && !work.structuredContext.Empty() {
		b.WriteString(formatStructuredContextForPrompt(work.structuredContext))
	}

	// === Raw project summary (secondary — narrative context) ===
	if work.readSummary != "" {
		b.WriteString("Project context from repository survey:\n")
		// Cap at 2000 chars (reduced since structured context is primary).
		summary := work.readSummary
		if len(summary) > 2000 {
			summary = summary[:2000] + "\n[...truncated...]"
		}
		b.WriteString(summary)
		b.WriteString("\n\n")
	}

	// T8.1: Cross-run learning — present proven patterns from past
	// successes so the LLM generates consistent, verified code.
	// This is how avatars reach Claude Code-level capability.
	if work.structuredContext != nil && len(work.structuredContext.InterfaceDecls) > 0 {
		b.WriteString("=== PROVEN PATTERNS (from past successes) ===\n")
		for _, iface := range work.structuredContext.InterfaceDecls {
			b.WriteString("// ")
			b.WriteString(iface.Name)
			b.WriteString(" (")
			b.WriteString(iface.Package)
			b.WriteString("): ")
			for i, m := range iface.Methods {
				if i > 0 {
					b.WriteString("; ")
				}
				b.WriteString(m.Name)
				b.WriteString("(")
				b.WriteString(m.Params)
				b.WriteString(")")
				if m.Returns != "" {
					b.WriteString("(")
					b.WriteString(m.Returns)
					b.WriteString(")")
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("=== END PROVEN PATTERNS ===\n\n")
	}

	existingTargets := extractTaskFilePaths(work.input)
	var mustModifyPaths []string
	mustModifySet := map[string]bool{}
	for _, tp := range existingTargets {
		if _, err := os.Stat(tp); err == nil {
			mustModifyPaths = append(mustModifyPaths, tp)
			mustModifySet[filepath.Clean(tp)] = true
		}
	}

	// Skip reference dumps that are already in MUST MODIFY — duplicate file
	// bodies bloat the user turn and miss the prefix cache on the suffix.
	sourceFiles := codeTaskSuggestedTargets(work.input, 3)
	var refFiles []string
	for _, path := range sourceFiles {
		if mustModifySet[filepath.Clean(path)] {
			continue
		}
		refFiles = append(refFiles, path)
	}
	if len(refFiles) > 0 {
		b.WriteString("=== REFERENCE SOURCE FILES ===\n")
		for _, path := range refFiles {
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			b.WriteString("--- ")
			b.WriteString(path)
			b.WriteString(" ---\n")
			text := string(content)
			if len(text) > 800 {
				text = text[:800] + "\n// [...truncated...]"
			}
			b.WriteString(text)
			b.WriteString("\n\n")
		}
		b.WriteString("=== END REFERENCE FILES ===\n\n")
	}

	if len(mustModifyPaths) > 0 {
		b.WriteString("\n=== FILES YOU MUST MODIFY (surgical) ===\n")
		b.WriteString("These files ALREADY EXIST. Use edit_file or precise_edit — do NOT dump COMPLETE file content.\n\n")
		for _, mp := range mustModifyPaths {
			b.WriteString("--- ")
			b.WriteString(mp)
			b.WriteString(" ---\n")
			if content, err := os.ReadFile(mp); err == nil {
				text := string(content)
				if len(text) > 1200 {
					text = text[:1200] + "\n// [...truncated, file continues...]"
				}
				b.WriteString(text)
				b.WriteString("\n\n")
			}
		}
		b.WriteString("=== END MUST MODIFY ===\n\n")
	}
	if looksLikeModificationTask(work.input) {
		b.WriteString("\n\n=== MODIFICATION TASK — Surgical edits only ===\n")
		b.WriteString("Existing files: use edit_file (path, old_string, new_string) or precise_edit (path, anchor, new_block, position=before|after|replace).\n")
		b.WriteString("Do NOT output full file content. Do NOT use write_file / FILE: headers for existing files.\n")
		b.WriteString("write_file / FILE: headers are allowed ONLY for brand-new paths that do not exist yet.\n")
	} else {
		b.WriteString("\n\n=== CREATION TASK — Generate New Files ===\n")
		b.WriteString("Generate the complete implementation files for this task. Include ALL necessary files.\n")
		b.WriteString("Use FILE: <path> headers or write_file for each NEW file. Prefer edit_file when touching existing files.")
	}
	b.WriteString("\nIf a test waits on a key/name/id, that identifier must match the production call in the same test.\n")
	return b.String()
}

const maxBuilderUserPromptBytes = 12000

func capBuilderUserPrompt(s string) string {
	if len(s) <= maxBuilderUserPromptBytes {
		return s
	}
	const notice = "\n[...truncated middle to keep the generate turn small; edit existing paths with edit_file...]\n"
	head := maxBuilderUserPromptBytes * 2 / 5
	tail := maxBuilderUserPromptBytes - head
	if tail < 2400 {
		tail = 2400
		head = maxBuilderUserPromptBytes - tail
	}
	if head < 512 {
		head = 512
	}
	if head+tail >= len(s) {
		return s[:maxBuilderUserPromptBytes] + notice
	}
	return s[:head] + notice + s[len(s)-tail:]
}

// looksLikeModificationTask detects tasks that modify existing code
// rather than creating new files from scratch (P4-1 Coordinator).
// Language-agnostic: verbs + soft add/wire only when a named path already exists.
// Bare extensions (.go/.js) alone must NOT classify greenfield as modification.
func looksLikeModificationTask(input string) bool {
	lowered := strings.ToLower(input)
	for _, cue := range []string{
		"from scratch", "从零", "空目录", "bootstrap", "scaffold",
		"create a new", "新建", "搭脚手架", "greenfield",
	} {
		if strings.Contains(lowered, cue) {
			return false
		}
	}
	strong := []string{
		"fix", "change", "modify", "update", "refactor", "repair",
		"修复", "修改", "更改", "更新", "重构",
		"bug", "issue", "problem", "error",
		"问题", "错误", "故障",
	}
	for _, m := range strong {
		if strings.Contains(lowered, m) {
			return true
		}
	}
	soft := []string{
		"add ", "wire", "integrate", "register", "include",
		"添加", "接入", "集成", "注册", "引入",
	}
	hasSoft := false
	for _, m := range soft {
		if strings.Contains(lowered, m) {
			hasSoft = true
			break
		}
	}
	if !hasSoft {
		return false
	}
	for _, p := range extractTaskFilePaths(input) {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// purgeUnhealthyBuilderFilesFromDisk removes tool-written files that fail
// checkFileHealth and returns the healthy subset. Files not on disk are kept
// in the returned list (they may only exist in LLM tool-call arguments).
// R11-2: also sweeps the whole source tree so truncated leftovers from a
// prior batch (not in this `files` list) cannot linger on disk.
func (e *Engine) purgeUnhealthyBuilderFilesFromDisk(work workflowNodeWorkContext, files []builderCodeFile) []builderCodeFile {
	var healthyFiles []builderCodeFile
	for _, cf := range files {
		fullPath := cf.Path
		if !filepath.IsAbs(fullPath) {
			if wd, wdErr := os.Getwd(); wdErr == nil {
				fullPath = filepath.Join(wd, fullPath)
			}
		}
		content, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			healthyFiles = append(healthyFiles, cf)
			continue
		}
		if reason := checkFileHealth(cf.Path, string(content)); reason != "" {
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.tool_write_health_block", "runtime", map[string]any{
				"file":   cf.Path,
				"reason": reason,
			}, nil)
			if !fileLivesInHarnessTree(fullPath) {
				_ = os.Remove(fullPath)
				_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.tool_write_health_removed", "runtime", map[string]any{
					"file":   cf.Path,
					"action": "deleted unhealthy file from disk",
				}, nil)
			}
			continue
		}
		healthyFiles = append(healthyFiles, cf)
	}
	if wd := e.sweepRoot(); wd != "" {
		if removed := purgeUnhealthySourcesOnDisk(wd); len(removed) > 0 {
			_ = e.emit(work.runID, work.taskID, "builder", "generating", "builder.disk_health_sweep", "runtime", map[string]any{
				"removed": removed,
				"count":   len(removed),
			}, nil)
		}
	}
	return healthyFiles
}

// purgeUnhealthySourcesOnDisk deletes source files under wd that fail
// checkFileHealth (R11-2). Returns relative or absolute paths removed.
func purgeUnhealthySourcesOnDisk(wd string) []string {
	if isAvatarsHarnessTree(wd) {
		return nil
	}
	var removed []string
	for _, f := range scanRecentSourceFiles(wd) {
		if fileLivesInHarnessTree(f) {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if reason := checkFileHealth(f, string(data)); reason == "" {
			continue
		}
		if err := os.Remove(f); err != nil {
			continue
		}
		removed = append(removed, f)
	}
	return removed
}

// needsGoModTidy is true when go.sum is missing, Builder wrote go.mod,
// or any .go sources were written (new imports often need tidy).
func needsGoModTidy(wd string, files []builderCodeFile) bool {
	goMod := filepath.Join(wd, "go.mod")
	if _, err := os.Stat(goMod); err != nil {
		return false
	}
	goSum := filepath.Join(wd, "go.sum")
	if _, err := os.Stat(goSum); err != nil {
		return true
	}
	for _, f := range files {
		base := filepath.Base(f.Path)
		if base == "go.mod" || strings.HasSuffix(strings.ToLower(f.Path), ".go") {
			return true
		}
	}
	return false
}

// checkFileHealth runs deterministic quality checks on Builder-generated
// Go files. Returns a reason string if the file is unhealthy, empty string
// if it passes. Phase 5: Health Guard extension.
func checkFileHealth(path string, content string) string {
	// T2/T3: junk scaffold paths and comment-only poison stubs (cross-lang).
	if isJunkScaffoldPath(path) {
		return fmt.Sprintf("File %s is a junk/dependency scaffold path — refuse", path)
	}
	if reason := looksLikePoisonStubContent(path, content); reason != "" {
		return fmt.Sprintf("File %s: %s", path, reason)
	}
	// G1: refuse harness context leaks (llm/providers, skills/*) in any common language.
	if reason := harnessContextLeakReason(path, content); reason != "" {
		return fmt.Sprintf("File %s: %s", path, reason)
	}
	// F63 (Go): library-dir *_test.go must not be package main (dual-package).
	if reason := goLibTestPackageMainReason(path, content); reason != "" {
		return fmt.Sprintf("File %s: %s", path, reason)
	}
	// G1: refuse Go module-local imports whose packages are missing on disk.
	if reason := missingLocalGoImportReason(".", path, content); reason != "" {
		return fmt.Sprintf("File %s: %s", path, reason)
	}
	// P2: reject duplicate TOML keys (pyproject/Cargo) before they brick pytest/cargo.
	if strings.HasSuffix(strings.ToLower(path), ".toml") {
		if reason := tomlDuplicateKeyReason(path, content); reason != "" {
			return fmt.Sprintf("File %s: %s", path, reason)
		}
	}
	// Check 1: Empty or near-empty non-test Go files.
	if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
		trimmed := strings.TrimSpace(content)
		base := strings.ToLower(filepath.Base(path))
		slash := filepath.ToSlash(strings.ToLower(path))
		// R3-10 / W3: hollow entrypoints parse but destroy cmd/server.
		if (base == "main.go" || strings.Contains(slash, "/cmd/")) &&
			strings.HasPrefix(trimmed, "package main") {
			if !strings.Contains(trimmed, "func main") {
				return fmt.Sprintf("File %s is a hollow package main with no func main", path)
			}
			expectHTTP := strings.Contains(slash, "/server/") || strings.Contains(slash, "/api/") ||
				strings.Contains(trimmed, "net/http")
			if reason := hollowHTTPEntrypointReason(path, trimmed, expectHTTP); reason != "" {
				return fmt.Sprintf("File %s: %s", path, reason)
			}
		}
		if len(trimmed) < 50 && !strings.Contains(trimmed, "func ") {
			return fmt.Sprintf("File %s is too small (%d bytes) — likely empty or stub output", path, len(content))
		}
	}
	// Check 2: Go syntax quick scan using go/parser.
	if strings.HasSuffix(path, ".go") {
		if _, err := parseGoFile(path, content); err != nil {
			return fmt.Sprintf("File %s has Go syntax errors: %s", path, err.Error())
		}
	}
	// BLOCK-2: Python syntax check for .py files.
	if strings.HasSuffix(path, ".py") {
		if reason := checkPythonSyntax(path, content); reason != "" {
			return reason
		}
		if reason := hollowNonGoHTTPEntrypointReason(path, content); reason != "" {
			return fmt.Sprintf("File %s: %s", path, reason)
		}
	}
	// P3: Node.js/JavaScript syntax check.
	if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs") ||
		strings.HasSuffix(path, ".cjs") || strings.HasSuffix(path, ".ts") ||
		strings.HasSuffix(path, ".tsx") {
		if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs") ||
			strings.HasSuffix(path, ".cjs") {
			if reason := checkNodeJSSyntax(path, content); reason != "" {
				return reason
			}
		}
		if reason := hollowNonGoHTTPEntrypointReason(path, content); reason != "" {
			return fmt.Sprintf("File %s: %s", path, reason)
		}
	}
	// R10-2 / J2-1: shared brace/paren completeness for compiled + JS sources.
	if strings.HasSuffix(path, ".rs") || strings.HasSuffix(path, ".go") ||
		strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx") ||
		strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs") ||
		strings.HasSuffix(path, ".cjs") || strings.HasSuffix(path, ".jsx") {
		if issues := codeCompletenessIssues(content); len(issues) > 0 {
			return fmt.Sprintf("File %s looks truncated/incomplete: %s", path, strings.Join(issues, "; "))
		}
	}
	return ""
}

// checkPythonSyntax performs lightweight syntax validation for Python files.
// BLOCK-2: Called by checkFileHealth to prevent incomplete Python code from
// reaching disk. Uses py_compile if Python is available, otherwise falls back
// to heuristic checks for common syntax errors.
func checkPythonSyntax(path string, content string) string {
	// Quick heuristic checks for common Python syntax errors.
	// Strip trailing newline to prevent false negatives on truncated files.
	lines := strings.Split(strings.TrimRight(content, "\n\r"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Check for incomplete control flow: "if x:" with no indented body.
		if strings.HasPrefix(trimmed, "if ") || strings.HasPrefix(trimmed, "for ") || strings.HasPrefix(trimmed, "while ") {
			if !strings.HasSuffix(trimmed, ":") {
				// BUG-2: "if a" without colon — truncated statement.
				return fmt.Sprintf("File %s line %d: control flow statement missing ':' — %s", path, i+1, trimmed)
			}
			// Has colon — check if next line has indentation (body exists).
			// BUG-2: If this is the LAST line, there's no body — truncated.
			if i+1 >= len(lines) {
				return fmt.Sprintf("File %s line %d: control flow statement has no body (end of file) — %s", path, i+1, trimmed)
			}
			nextLine := lines[i+1]
			nextTrimmed := strings.TrimSpace(nextLine)
			if nextTrimmed != "" && nextTrimmed != "else:" && !strings.HasPrefix(nextTrimmed, "elif ") && !strings.HasPrefix(nextLine, " ") && !strings.HasPrefix(nextLine, "\t") {
				return fmt.Sprintf("File %s line %d: control flow statement has no indented body — next line: %s", path, i+1, nextTrimmed)
			}
		}
		// Check for incomplete def/class.
		if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "class ") {
			if !strings.HasSuffix(trimmed, ":") {
				return fmt.Sprintf("File %s line %d: %s statement missing ':' — %s", path, i+1, strings.SplitN(trimmed, " ", 2)[0], trimmed)
			}
			// BUG-2: def/class at end of file with no body.
			if i+1 >= len(lines) {
				return fmt.Sprintf("File %s line %d: %s statement has no body (end of file) — %s", path, i+1, strings.SplitN(trimmed, " ", 2)[0], trimmed)
			}
		}
		// BUG-2: Check for bare trailing expression (truncated line).
		// Lines like "if a " or "return " at end of file with no completion.
		if i == len(lines)-1 && trimmed != "" && !strings.HasSuffix(trimmed, ";") && !strings.HasSuffix(trimmed, ":") {
			// Check if line ends abruptly mid-expression.
			if strings.HasSuffix(trimmed, " ") || strings.HasSuffix(line, " ") {
				// Trailing space may indicate truncation.
				continue // not conclusive enough to block
			}
		}
		// Check for unbalanced parentheses on non-comment lines.
		if !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "\"\"\"") {
			open := strings.Count(line, "(")
			close := strings.Count(line, ")")
			if open != close && !strings.HasSuffix(trimmed, "\\") {
				// Allow multi-line function calls — only flag if grossly unbalanced.
				if open > close+2 || close > open+2 {
					return fmt.Sprintf("File %s line %d: unbalanced parentheses (%d open, %d close) — %s", path, i+1, open, close, trimmed)
				}
			}
		}
	}
	// Try py_compile if Python is available.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("avatars_health_%d.py", time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err == nil {
		defer os.Remove(tmpFile)
		cmd := exec.CommandContext(ctx, "python", "-m", "py_compile", tmpFile)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg != "" {
				return fmt.Sprintf("File %s has Python syntax errors: %s", path, errMsg)
			}
		}
	}
	return ""
}

// parseGoFile attempts to parse Go source and returns any syntax error.
// Uses go/parser for quick validation without writing to disk.
func parseGoFile(path string, content string) (any, error) {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	return nil, err
}

// removeExternalImports removes import lines from Go source that reference
// external packages not in go.mod. BLOCK-1: Called before invokeToolWithOptions
// to prevent hallucinated imports from reaching disk.
func removeExternalImports(source string, warnings []DependencyWarning, projectRoot string) string {
	modulePath, _ := parseGoMod(projectRoot)
	stdlib := stdlibPackageSet()
	badImports := make(map[string]bool, len(warnings))
	for _, dw := range warnings {
		badImports[dw.ImportPath] = true
	}
	lines := strings.Split(source, "\n")
	var result []string
	skipGroupImport := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Handle grouped imports: import ( ... )
		if trimmed == "import (" {
			result = append(result, line)
			skipGroupImport = true
			continue
		}
		if skipGroupImport {
			if trimmed == ")" {
				result = append(result, line)
				skipGroupImport = false
				continue
			}
			// Check if this import line has a bad import path.
			impPath := extractImportPathFromLine(trimmed)
			if impPath != "" && badImports[impPath] {
				continue // skip this import line
			}
			result = append(result, line)
			continue
		}
		// Handle single import: import "pkg" or import alias "pkg"
		if strings.HasPrefix(trimmed, "import ") {
			impPath := extractImportPathFromLine(strings.TrimPrefix(trimmed, "import "))
			if impPath != "" && badImports[impPath] {
				continue // skip this import line
			}
		}
		_ = modulePath
		_ = stdlib
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// extractImportPathFromLine extracts the quoted import path from a line like:
// "github.com/foo/bar" or alias "github.com/foo/bar" or _ "github.com/foo/bar"
func extractImportPathFromLine(line string) string {
	line = strings.TrimSpace(line)
	// Find the quoted string.
	start := strings.Index(line, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(line[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return line[start+1 : start+1+end]
}

// in the last 5 minutes. Used when LLM writes files via tools instead of
// text-based FILE: headers. PHANTOM-1 integration fix.
func findRecentlyWrittenFiles(work workflowNodeWorkContext) []builderCodeFile {
	wd, _ := os.Getwd()
	cutoff := time.Now().Add(-5 * time.Minute)
	var files []builderCodeFile
	_ = filepath.WalkDir(wd, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(path, ".avatars") || strings.Contains(path, ".git") {
			return nil
		}
		info, _ := d.Info()
		if info == nil || info.ModTime().Before(cutoff) {
			return nil
		}
		ext := filepath.Ext(path)
		switch ext {
		case ".go", ".py", ".js", ".ts", ".rs", ".java", ".mod", ".sum":
			content, err := os.ReadFile(path)
			if err == nil && len(content) > 50 {
				text := strings.TrimSpace(string(content))
				// Skip truncated files: files ending with incomplete constructs
				// (trailing {, (, or other mid-statement endings) are likely truncated.
				// Files ending with }, ;, ), or normal characters are kept.
				lastChar := text[len(text)-1:]
				if lastChar == "{" || lastChar == "(" || lastChar == "," {
					return nil // truncated — ends mid-statement
				}
				rel, _ := filepath.Rel(wd, path)
				files = append(files, builderCodeFile{Path: filepath.ToSlash(rel), Content: string(content)})
			}
		}
		return nil
	})
	return files
}

func parseBuilderCodeResponse(response string) []builderCodeFile {
	// Strip markdown code fences that some LLMs wrap output in.
	cleaned := strings.TrimSpace(response)
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimPrefix(cleaned, "```go")
	cleaned = strings.TrimPrefix(cleaned, "```text")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	fileRe := regexp.MustCompile(`(?mi)^\s*FILE:\s*(.+)$`)
	matches := fileRe.FindAllStringSubmatchIndex(cleaned, -1)
	if len(matches) == 0 {
		// No FILE: headers found; treat entire response as a single file
		// with unknown path — skip.
		return nil
	}
	var files []builderCodeFile
	for i, match := range matches {
		path := strings.TrimSpace(cleaned[match[2]:match[3]])
		// P15-2: Validate that FILE: paths look like real file paths.
		// Reject paths that are clearly code fragments (single words without
		// path separators or known extensions — e.g., "PathBuf," hallucination).
		if path == "" || !looksLikeFilePath(path) {
			continue
		}
		contentStart := match[1] // end of the FILE: line
		contentEnd := len(cleaned)
		if i+1 < len(matches) {
			contentEnd = matches[i+1][0]
		}
		content := strings.TrimSpace(cleaned[contentStart:contentEnd])
		// Strip markdown fences if present.
		content = stripMarkdownFences(content)
		if content == "" {
			return nil
		}
		files = append(files, builderCodeFile{Path: path, Content: content})
	}
	return files
}

// looksLikeFilePath validates that a path extracted from a FILE: header
// looks like a real file path, not a code fragment hallucinated by the LLM.
// P15-2: Rejects paths like "PathBuf," or "}" that the LLM might insert.
func looksLikeFilePath(path string) bool {
	// Must have either a path separator or a known file extension.
	hasSep := strings.Contains(path, "/") || strings.Contains(path, "\\")
	hasExt := false
	for _, ext := range []string{".rs", ".go", ".js", ".jsx", ".ts", ".tsx", ".py",
		".toml", ".yaml", ".yml", ".json", ".md", ".txt", ".html", ".css",
		".mod", ".sum", ".lock", ".sh", ".bat", ".cfg", ".ini", ".env"} {
		if strings.HasSuffix(path, ext) || strings.Contains(path, ext+"/") {
			hasExt = true
			break
		}
	}
	// Reject paths that look like code: trailing punctuation, single words without ext.
	hasCodeArtifact := strings.ContainsAny(path, ",;(){}[]<>") ||
		(len(path) < 4 && !hasExt && !hasSep) ||
		strings.HasPrefix(path, "}") || strings.HasPrefix(path, ")")
	if hasCodeArtifact {
		return false
	}
	return hasSep || hasExt
}

// resolveBuilderTargetLanguage picks the language lock for Builder writes.
// Preference: original user/task input → current node input → on-disk manifest.
// Disk is last so running tests/tools inside the avatars Go repo does not lock
// every unrelated task to "go". Phase boilerplate must not override the prompt.
func resolveBuilderTargetLanguage(taskInput, originalInput string) string {
	fromOrig := detectTargetLanguage(originalInput)
	fromTask := detectTargetLanguage(taskInput)
	if fromOrig != "" && fromTask != "" && fromOrig != fromTask {
		return fromOrig
	}
	if fromOrig != "" {
		return fromOrig
	}
	if fromTask != "" {
		return fromTask
	}
	return detectLanguageFromDisk()
}

// languageDetectionNoiseRe strips multi-language layout hints that poisoned
// detectTargetLanguage (H1: "src/ for JS/TS/Rust" → rust).
var languageDetectionNoiseRe = regexp.MustCompile(`(?i)` +
	`(?:app/\s*for\s*python|internal/\+?cmd/\s*for\s*go|src/\s*for\s*js\s*/\s*ts\s*/\s*rust|` +
	`js\s*/\s*ts\s*/\s*rust|layout declared in the task\s*\([^)]*\))`)

func stripLanguageDetectionNoise(taskInput string) string {
	return languageDetectionNoiseRe.ReplaceAllString(taskInput, " ")
}

// detectTargetLanguage extracts the target programming language from task input.
// Returns lowercase language name: "go", "rust", "python", "javascript", "typescript", etc.
// Returns "" if no clear language signal is found.
func detectTargetLanguage(taskInput string) string {
	lower := strings.ToLower(stripLanguageDetectionNoise(taskInput))

	// Phase 1: File extension counting (most reliable — no false positives).
	extLang := map[string]string{
		".rs": "rust", ".toml": "rust",
		".go": "go",
		".py": "python", ".pyi": "python",
		".ts": "typescript", ".tsx": "typescript",
		".js": "javascript", ".jsx": "javascript", ".mjs": "javascript",
		".java": "java",
		".cpp":  "c++", ".cc": "c++", ".cxx": "c++", ".hpp": "c++",
		".c": "c", ".h": "c",
	}
	extCount := map[string]int{}
	extRe := regexp.MustCompile(`\.(rs|go|py|pyi|tsx?|jsx?|mjs|java|cpp|cc|cxx|hpp|c|h)\b`)
	for _, m := range extRe.FindAllString(lower, -1) {
		if lang, ok := extLang[m]; ok {
			extCount[lang]++
		}
	}

	// Phase 2: Explicit unambiguous language names (high confidence keywords).
	// NOTE: "go", "java", "c" are NOT used as standalone keywords because they
	// are common English words. We rely on extensions for those languages.
	// Single-token names use word boundaries so "trust" / layout lists do not match.
	unambiguousKeywords := []struct {
		lang     string
		keywords []string
		bounded  bool // true → \b word match for each keyword
	}{
		{"rust", []string{"rust", "rustlang", "cargo"}, true},
		{"python", []string{"python", "python3", "pyenv", "pip install", "pytest", "python script", "python project"}, false},
		{"typescript", []string{"typescript", "type script"}, false},
		{"javascript", []string{"javascript", "java script", "node.js", "nodejs", "npm install", "npx "}, false},
		{"go", []string{"golang", "go语言", "go http", "go server", "go api", "go cli", "go library", "go package", "go module", "go program", "go.mod"}, false},
		{"java", []string{"spring boot", "spring framework"}, false},
		{"c++", []string{"c++", "cpp", "c++17", "c++20"}, false},
		{"c", []string{"c language", "c programming", "c program"}, false},
	}

	keywordLang := ""
	for _, lk := range unambiguousKeywords {
		for _, kw := range lk.keywords {
			hit := false
			if lk.bounded || !strings.Contains(kw, " ") {
				// Word-boundary for single tokens (esp. rust).
				if len(strings.Fields(kw)) == 1 {
					re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(kw) + `\b`)
					hit = re.MatchString(lower)
				} else {
					hit = strings.Contains(lower, kw)
				}
			} else {
				hit = strings.Contains(lower, kw)
			}
			if hit {
				keywordLang = lk.lang
				break
			}
		}
		if keywordLang != "" {
			break
		}
	}

	// Phase 3: Resolve.
	// Key principle: explicit language names are deliberate and should win over
	// incidental extension mentions (e.g. "Python script that reads .go files").
	// Extensions only win when there's NO explicit keyword signal.

	// Count extension votes to find dominant language.
	bestExt, bestExtCount := "", 0
	for lang, count := range extCount {
		if count > bestExtCount {
			bestExt, bestExtCount = lang, count
		}
	}

	// If explicit keyword found, prefer it (user deliberately named the language).
	if keywordLang != "" {
		return keywordLang
	}

	// No explicit keyword — use extension-based detection.
	if bestExtCount > 0 {
		return bestExt
	}

	return ""
}

// getLanguageExtensions returns the expected file extensions for a given language.
// Also includes universal files (.md, .txt, .yaml, .json, etc.).
func getLanguageExtensions(lang string) []string {
	// Batch6/U + stage edits: sibling assets (.sql, .html, .css) belong with any stack.
	base := []string{".md", ".txt", ".yaml", ".yml", ".json", ".lock", ".cfg", ".ini", ".env", ".sh", ".bat", ".gitignore", ".dockerignore", ".sql", ".proto", ".graphql", ".html", ".css", ".toml", ".mako"}
	switch lang {
	case "rust":
		return append(base, ".rs", ".toml")
	case "go":
		return append(base, ".go", ".mod", ".sum")
	case "python":
		return append(base, ".py", ".pyi", ".pyx", ".cfg")
	case "javascript":
		return append(base, ".js", ".jsx", ".mjs", ".cjs")
	case "typescript":
		return append(base, ".ts", ".tsx")
	case "java":
		return append(base, ".java", ".xml", ".gradle", ".properties")
	case "c++":
		return append(base, ".cpp", ".cc", ".cxx", ".hpp", ".h", ".hxx", ".cmake")
	case "c":
		return append(base, ".c", ".h", ".cmake")
	default:
		// No language detected — allow all common code extensions.
		return append(base, ".rs", ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".java", ".cpp", ".c", ".h", ".toml", ".mod", ".sum")
	}
}

// filterCrossLanguageFiles separates files matching the target language from those that don't.
// Returns (kept, rejected). When targetLang is "", all files are kept.
func filterCrossLanguageFiles(files []builderCodeFile, targetLang string) (kept, rejected []builderCodeFile) {
	if targetLang == "" {
		return files, nil
	}
	allowed := getLanguageExtensions(targetLang)
	for _, f := range files {
		if isUniversalProjectConfigPath(f.Path) {
			kept = append(kept, f)
			continue
		}
		ext := ""
		if idx := strings.LastIndex(f.Path, "."); idx >= 0 {
			ext = strings.ToLower(f.Path[idx:])
		}
		matched := false
		for _, a := range allowed {
			if ext == a {
				matched = true
				break
			}
		}
		if matched {
			kept = append(kept, f)
		} else {
			rejected = append(rejected, f)
		}
	}
	return
}

// isFuturePhaseModelPath reports known later-phase domain models that should
// not land during PHASE LOCK Phase 1 (V8).
func isFuturePhaseModelPath(path string) bool {
	p := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	base := filepath.Base(p)
	switch base {
	case "tag.py", "tags.py", "version.py", "versions.py", "search.py",
		"document_version.py", "document_versions.py",
		"tag.go", "tags.go", "version.go", "versions.go", "search.go",
		"document_version.go":
		return true
	}
	if strings.Contains(p, "/models/tag") || strings.Contains(p, "/models/version") ||
		strings.Contains(p, "/models/search") || strings.Contains(p, "/schemas/tag") ||
		strings.Contains(p, "/schemas/version") {
		return true
	}
	return false
}

// isUniversalProjectConfigPath allows root/config artifacts that are not
// language-source extensions (V1: `.env.example` was blocked as `.example`).
func isUniversalProjectConfigPath(path string) bool {
	p := filepath.ToSlash(filepath.Clean(path))
	base := filepath.Base(p)
	lower := strings.ToLower(base)
	if projectRootConfigFiles[base] || projectRootConfigFiles[lower] {
		return true
	}
	switch lower {
	case ".env", ".env.example", ".env.sample", ".env.local", ".gitignore", ".dockerignore",
		"dockerfile", "makefile", "alembic.ini", "pyproject.toml", "requirements.txt",
		"requirements-dev.txt", "setup.cfg", "setup.py", "pipfile", "cargo.toml",
		"go.mod", "go.sum", "package.json", "tsconfig.json", "readme.md", "license":
		return true
	}
	if strings.HasPrefix(lower, ".env.") {
		return true
	}
	return false
}

// stripMarkdownFences removes surrounding ``` fences from content.
func stripMarkdownFences(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		if idx := strings.Index(content, "\n"); idx >= 0 {
			content = content[idx+1:]
		}
		if lastBacktick := strings.LastIndex(content, "```"); lastBacktick >= 0 {
			content = content[:lastBacktick]
		}
	}
	return strings.TrimSpace(content)
}

// supersetMerge takes existing file content and Builder's proposed content,
// and returns a merged result. Two strategies:
//
//  1. Full rewrite: if >40% of proposed non-empty lines are NOT in the existing
//     file, the Builder likely regenerated the file from scratch. Return the
//     proposed content as-is to avoid appending duplicate code.
//  2. Append-only merge: otherwise, preserve all existing lines and append
//     only genuinely new lines. This handles incremental edits safely.
//
// P14-FINAL: Diff-based append-only merge for existing file edits.
// I35-FIX: Detect full rewrites to prevent duplicate code corruption.
func supersetMerge(existing string, proposed string) string {
	existingLines := strings.Split(existing, "\n")
	proposedLines := strings.Split(proposed, "\n")

	existingSet := map[string]bool{}
	for _, l := range existingLines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			existingSet[trimmed] = true
		}
	}

	// Count how many proposed non-empty lines exist in the current file.
	proposedNonEmpty := 0
	proposedFound := 0
	for _, l := range proposedLines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		proposedNonEmpty++
		if existingSet[trimmed] {
			proposedFound++
		}
	}

	// Count existing non-empty lines for size comparison.
	existingNonEmpty := len(existingSet)

	// Detect full rewrite vs incremental edit:
	//   Full rewrite: proposed is similar in size to existing AND most lines are new.
	//     → Builder regenerated the file from scratch. Return proposed as-is.
	//   Incremental edit: proposed is much smaller than existing.
	//     → Builder is adding a function. Append only new lines.
	overlapRatio := float64(proposedFound) / float64(proposedNonEmpty)
	isRewriteSize := proposedNonEmpty >= existingNonEmpty/2 // proposed is at least half of existing
	isNewContent := overlapRatio < 0.5                      // at least half of proposed is new
	if proposedNonEmpty > 5 && isRewriteSize && isNewContent {
		return proposed
	}

	// Append-only merge: keep existing + add only genuinely new lines.
	existingSet = map[string]bool{}
	for _, l := range existingLines {
		existingSet[strings.TrimSpace(l)] = true
	}

	var newLines []string
	for _, l := range proposedLines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if !existingSet[trimmed] {
			newLines = append(newLines, l)
			existingSet[trimmed] = true
		}
	}

	if len(newLines) == 0 {
		return existing
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(existing, "\n"))
	b.WriteString("\n")
	for _, nl := range newLines {
		b.WriteString(nl)
		b.WriteString("\n")
	}
	return b.String()
}

// isTransientLLMNetworkError detects transport failures that should get a
// plain retry — not turn-cap self-heal (Batch5/B9).
func isTransientLLMNetworkError(errText string) bool {
	lower := strings.ToLower(errText)
	for _, needle := range []string{
		"read tcp", "wsarecv", "connection reset", "connection refused",
		"i/o timeout", "tls handshake", "eof", "broken pipe",
		"temporary failure", "server misbehaving", "status 429", "status 502",
		"status 503", "status 504", "http 429", "http 502", "http 503", "http 504",
		// Z9: DeepSeek OpenAI-compatible errors look like `returned 503: …`
		// (not `status 503`), so the old needles never matched.
		"returned 429", "returned 502", "returned 503", "returned 504",
		"too many requests",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// isProviderBusyError detects capacity/overload responses that should NOT
// burn multi-level self-heal drafts (Z9). Distinct from brief transport blips.
func isProviderBusyError(errText string) bool {
	lower := strings.ToLower(errText)
	for _, needle := range []string{
		"service is too busy",
		"too busy",
		"server is busy",
		"capacity",
		"overloaded",
		"temporarily unavailable",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	// Bare 503 without "busy" still often means capacity on DeepSeek.
	if strings.Contains(lower, "returned 503") || strings.Contains(lower, "status 503") ||
		strings.Contains(lower, "http 503") {
		return true
	}
	return false
}

// builderRepairWriteSuffix is appended to the Builder *user* prompt on a
// one-shot retry. It must not be folded into the stable system prefix.
const builderRepairWriteSuffix = `

=== REPAIR WRITE DISCIPLINE ===
Write source files with write_file/edit_file (or FILE dumps). Do not explain a patch without applying it.
If a clock/time source is injectable, wait/delay/timer must block on that clock (channel, cond, or fake timer) — never wall time.Sleep / time.After / asyncio.sleep / setTimeout / thread::sleep.
`
