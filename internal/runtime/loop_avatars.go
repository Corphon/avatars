package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"avatars/internal/llm"
	"avatars/internal/planner"
	"avatars/internal/tools"
	"avatars/internal/workflow"
)

func (e *Engine) executeDirectNodeWork(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	if err := e.emit(work.runID, work.taskID, "avatar-direct", "executing", "direct.started", "runtime", map[string]any{"node_id": work.nodeID, "node_role": work.nodeRole, "input": work.input}, nil); err != nil {
		return workflowNodeWorkResult{}, err
	}

	// W6: Plan confirmation path — confirm plan and generate all phases.
	// Must be checked BEFORE OnlyDocTargets since "confirm the plan" has no file ext.
	if workflow.IsConfirmPlan(work.input) {
		return e.executeConfirmPlan(ctx, work)
	}
	// W6b: Replan path — regenerate the plan from new requirement.
	if workflow.IsReplan(work.input) {
		return e.executeReplan(ctx, work)
	}
	// W10: Mark task as done — mark specific todo checklist item as [x].
	if isMark, taskDesc := workflow.IsMarkTaskDone(work.input); isMark {
		return e.executeMarkTaskDone(ctx, work, taskDesc)
	}

	// P14: DocEditor path — for documentation-only tasks, don't read project code files.
	// Read the target doc, pass it with the task to LLM, write back the modified version.
	if planner.OnlyDocTargets(strings.ToLower(work.input)) && !planner.LooksLikeRepoAnalysisOrReport(work.input) {
		// W2: Plan constructor path — fill avatars_plan.md from a user requirement.
		if workflow.IsPlanConstruction(work.input) {
			return e.executePlanConstructor(ctx, work)
		}
		// W3: Phase constructor path — generate phaseN.md from the plan.
		if workflow.IsPhaseConstruction(work.input) {
			return e.executePhaseConstructor(ctx, work)
		}
		return e.executeDocEditor(ctx, work)
	}

	// The Direct avatar is the executor for trivial single-action tasks.
	if e.llm == nil {
		if err := e.persistNodeWorkEvidence(nodeWorkEvidence{RunID: work.runID, TaskID: work.taskID, AvatarID: "avatar-direct", NodeID: work.nodeID, NodeRole: work.nodeRole, NodeTitle: work.nodeTitle, Tool: "direct", Operation: "execute", Status: "completed", Summary: fmt.Sprintf("Direct node %s skipped: no LLM client available.", work.nodeID), Verified: true}); err != nil {
			return workflowNodeWorkResult{}, err
		}
		if err := e.emit(work.runID, work.taskID, "avatar-direct", "executing", "direct.completed", "runtime", map[string]any{"node_id": work.nodeID, "summary": "Direct path skipped: no LLM client."}, nil); err != nil {
			return workflowNodeWorkResult{}, err
		}
		return workflowNodeWorkResult{}, nil
	}

	// Extract a target file path from the user's natural language input.
	targetPath := extractFilePathFromInput(work.input)

	// If the task mentions modifying/replacing and the file exists, read it first.
	var existingContent string
	lowerInput := strings.ToLower(work.input)
	if (strings.Contains(lowerInput, "replace") || strings.Contains(lowerInput, "modify") || strings.Contains(lowerInput, "update") || strings.Contains(lowerInput, "fill in")) && targetPath != "" {
		if absPath, err := filepath.Abs(targetPath); err == nil {
			if data, readErr := os.ReadFile(absPath); readErr == nil {
				existingContent = string(data)
			}
		}
	}

	prompt := buildDirectActionPrompt(work.input, targetPath, existingContent)

	llmCtx, llmCancel := context.WithTimeout(ctx, 120*time.Second)
	defer llmCancel()
	directReq := llm.Request{
		SystemPrompt: appendPersonality("You are a Direct executor. Do EXACTLY what the task says — make only the specific changes requested. If told to replace text X with Y in a file, output the ENTIRE modified file with ONLY that change. Output: FILE: <path> on the first line, then the complete file content. No markdown fences, no explanations, no extra changes.", e.personality),
		UserPrompt:   prompt,
		Category:     llm.CategoryCodeGeneration,
		MaxTokens:    16384,
	}
	applyBuilderCodeGenThinkMode(&directReq)
	e.emitLLMPromptCost(work.runID, work.taskID, "avatar-direct", "executing", "Direct", directReq)
	response, llmErr := e.llm.Generate(llmCtx, directReq)
	if llmErr != nil {
		if llm.IsPrimaryFailureWithoutFallbacks(llmErr) {
			failPayload := map[string]any{"error": llmErr.Error(), "node_id": work.nodeID}
			mergePromptObservability(failPayload, "Direct", directReq.SystemPrompt, directReq.UserPrompt)
			mergeLLMUsage(failPayload, response.Usage)
			_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "llm.failed", "llm", failPayload, nil)
			return workflowNodeWorkResult{}, llmErr
		}
		if err := e.persistNodeWorkEvidence(nodeWorkEvidence{RunID: work.runID, TaskID: work.taskID, AvatarID: "avatar-direct", NodeID: work.nodeID, NodeRole: work.nodeRole, NodeTitle: work.nodeTitle, Tool: "direct", Operation: "execute", Status: "completed", Summary: fmt.Sprintf("Direct node %s LLM call failed: %s", work.nodeID, llmErr.Error()), Verified: true}); err != nil {
			return workflowNodeWorkResult{}, err
		}
		if err := e.emit(work.runID, work.taskID, "avatar-direct", "executing", "direct.completed", "runtime", map[string]any{"node_id": work.nodeID, "summary": fmt.Sprintf("Direct path LLM failed: %s", llmErr.Error())}, nil); err != nil {
			return workflowNodeWorkResult{}, err
		}
		return workflowNodeWorkResult{}, nil
	}

	parsedPath, parsedContent := parseDirectActionResponse(response.Text, targetPath)
	if parsedPath == "" || strings.TrimSpace(parsedContent) == "" {
		if err := e.persistNodeWorkEvidence(nodeWorkEvidence{RunID: work.runID, TaskID: work.taskID, AvatarID: "avatar-direct", NodeID: work.nodeID, NodeRole: work.nodeRole, NodeTitle: work.nodeTitle, Tool: "direct", Operation: "execute", Status: "completed", Summary: fmt.Sprintf("Direct node %s could not parse actionable output from LLM.", work.nodeID), Verified: true}); err != nil {
			return workflowNodeWorkResult{}, err
		}
		if err := e.emit(work.runID, work.taskID, "avatar-direct", "executing", "direct.completed", "runtime", map[string]any{"node_id": work.nodeID, "summary": "Direct path: LLM response did not contain a parsable file action."}, nil); err != nil {
			return workflowNodeWorkResult{}, err
		}
		return workflowNodeWorkResult{}, nil
	}

	// ALWAYS use the target path extracted from user input when available.
	// The LLM may change the extension (e.g., .json -> .js); we must not
	// allow that. If no target path was extracted, use whatever the LLM
	// gave us.
	if targetPath != "" {
		parsedPath = targetPath
	}

	// Write the file through the guarded write tool.
	workingDir, wdErr := os.Getwd()
	if wdErr != nil {
		return workflowNodeWorkResult{}, wdErr
	}
	writePath := parsedPath
	if !filepath.IsAbs(parsedPath) {
		writePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(parsedPath)))
	}
	// NL smoke Batch1/P6: Direct writes share Builder path hygiene (multi-lang).
	if sanitized, redirected := sanitizeWritePath(writePath); redirected {
		_ = e.emit(work.runID, work.taskID, "avatar-direct", "executing", "direct.path_sanitized", "runtime", map[string]any{
			"from": writePath, "to": sanitized,
		}, nil)
		writePath = sanitized
	}
	writeInput := tools.WriteInput{
		Path:            writePath,
		Content:         parsedContent,
		WorkingDir:      workingDir,
		Overwrite:       true,
		Intent:          fmt.Sprintf("Direct avatar: write %s", filepath.Base(writePath)),
		ExpectedTargets: []string{writePath},
	}
	if _, err := e.invokeToolWithOptions(ctx, toolCallEnvelope{runID: work.runID, taskID: work.taskID, avatarID: "avatar-direct", phase: "executing"}, "write", "file_write", writeInput, toolInvokeOptions{}); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "approval required") {
			point, pointErr := work.workflowState.PausePointForNodeInRun(work.runID, work.taskID, work.nodeID)
			if pointErr != nil {
				return workflowNodeWorkResult{}, pointErr
			}
			point.StatusReason = strings.TrimSpace(err.Error())
			return workflowNodeWorkResult{pausePoint: point, pauseErr: err}, nil
		}
		return workflowNodeWorkResult{}, err
	}

	// W13: Track changed files for process_record memory.
	e.changedFiles = append(e.changedFiles, writePath)

	// Post-write verification: language-agnostic health check (Go/Python/JS/Rust/TS).
	// Direct actions skip the Critic pipeline, so this is the only quality gate.
	buildOK := true
	healthErr := ""
	if wd, wdErr := os.Getwd(); wdErr == nil {
		healthErr = crossLangHealthCheck(wd)
		buildOK = healthErr == ""
		if !buildOK {
			_ = workflow.RecordTaskCompletion(wd,
				fmt.Sprintf("Direct write to %s broke build", filepath.Base(writePath)),
				"build/health check failed after Direct action",
				[]string{writePath},
				fmt.Sprintf("HAZARD: health check failed after Direct write to %s: %s — Critic should review and fix", writePath, healthErr))
		}
	}
	verified := buildOK

	result := workflowNodeWorkResult{artifactIDs: []string{"direct-output"}}
	summaryMsg := fmt.Sprintf("Direct node %s wrote %s.", work.nodeID, writePath)
	if !buildOK {
		summaryMsg += " WARNING: build/health check failed after write. build_ok=false"
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{RunID: work.runID, TaskID: work.taskID, AvatarID: "avatar-direct", NodeID: work.nodeID, NodeRole: work.nodeRole, NodeTitle: work.nodeTitle, Tool: "write", Operation: "file_write", Status: "completed", Summary: summaryMsg, ArtifactIDs: result.artifactIDs, ExpectedTargets: []string{writePath}, Verified: verified}); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if err := e.emit(work.runID, work.taskID, "avatar-direct", "executing", "direct.completed", "runtime", map[string]any{"node_id": work.nodeID, "summary": fmt.Sprintf("Direct path: wrote %s (%d bytes). build_ok=%v", writePath, len(parsedContent), buildOK)}, nil); err != nil {
		return workflowNodeWorkResult{}, err
	}
	result.readSummary = summaryMsg
	return result, nil
}

func extractFilePathFromInput(input string) string {
	// Try to find a known extension in the input.
	knownExt := regexp.MustCompile(`(?i)([a-zA-Z0-9][a-zA-Z0-9._/-]*\.(?:json|mjs|js|tsx|ts|go|rs|java|cs|ps1|sh|html|css|md|txt|ya?ml|toml|sql|xml|csv|ini|cfg|env|yaml|py))`)
	if matches := knownExt.FindStringSubmatch(input); len(matches) >= 2 {
		return strings.Trim(matches[1], "\"'“”")
	}
	return ""
}

func buildDirectActionPrompt(input string, targetPath string, existingContent string) string {
	var b strings.Builder
	b.WriteString("User request:\n")
	b.WriteString(input)
	b.WriteString("\n\n")
	if targetPath != "" {
		b.WriteString("Target file path: ")
		b.WriteString(targetPath)
		b.WriteString("\n")
	}
	if existingContent != "" {
		b.WriteString("\nCurrent file content:\n```\n")
		b.WriteString(existingContent)
		b.WriteString("\n```\n\n")
		b.WriteString("Apply ONLY the changes requested by the user to this file. Keep everything else unchanged.\n")
	}
	b.WriteString("\nOutput the COMPLETE modified file. Format:\nFILE: ")
	if targetPath != "" {
		b.WriteString(targetPath)
	} else {
		b.WriteString("<path>")
	}
	b.WriteString("\n<file content here>\n")
	return b.String()
}

func parseDirectActionResponse(response string, fallbackPath string) (string, string) {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return "", ""
	}
	// Parse "FILE: <path>" header.
	fileRe := regexp.MustCompile(`(?im)^FILE:\s*(.+)$`)
	path := fallbackPath
	content := trimmed
	if match := fileRe.FindStringSubmatch(trimmed); len(match) >= 2 {
		llmPath := strings.TrimSpace(match[1])
		// If we have a fallback path from the user's input, always use it.
		// The LLM might change the extension (e.g., .json -> .js) which
		// we must not allow.
		if fallbackPath != "" {
			path = fallbackPath
		} else {
			path = llmPath
		}
		content = strings.TrimSpace(strings.TrimPrefix(trimmed, match[0]))
	}
	// Strip markdown fences if present.
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		// Find first newline after opening fence.
		if idx := strings.Index(content, "\n"); idx >= 0 {
			content = content[idx+1:]
		}
		// Remove closing fence.
		if lastBacktick := strings.LastIndex(content, "```"); lastBacktick >= 0 {
			content = content[:lastBacktick]
		}
	}
	return strings.TrimSpace(path), strings.TrimSpace(content)
}

func (e *Engine) executeResearcherNodeWork(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	// S6.8: Role skill templates must not append into readSummary (evidence /
	// report pollution). Researcher survey is deterministic; Builder/Critic
	// LLM paths still inject always-on skills via appendAlwaysOnRoleSkills.
	// P8: In plan mode, inject Explore Mode prompt for deep read-only analysis.
	// This makes the Researcher read file CONTENTS and trace logic chains
	// instead of just listing files (Survey Mode).
	exploreCfg := ExploreConfigForMode(e.permissionMode)
	if explorePrompt := BuildExplorePrompt(exploreCfg); explorePrompt != "" {
		// Append explore instructions to the read summary so they flow
		// into the combinedSummary and then to the Builder.
		work.readSummary += explorePrompt
	}

	// P3-2: For parallel survey nodes, filter read targets to the
	// specific file bucket this node is responsible for. This allows
	// 3 Researcher goroutines to survey docs/src/cfg concurrently.
	if isParallelSurveyNode(work.nodeID) {
		work.readTargets = filterReadTargetsForParallelNode(work.nodeID, work.readTargets)
		// P3-2/P3-3: Source survey node also gets task-guided source files
		// (interfaces, registries) so the Builder has accurate signatures.
		if work.nodeID == "node-survey-src" && looksLikeCodeImplementationTask(work.input) {
			for _, candidate := range codeTaskSuggestedTargets(work.input, 5) {
				if path, ok := existingResearcherReadTarget(candidate); ok {
					// Avoid duplicates.
					found := false
					for _, t := range work.readTargets {
						if t == path {
							found = true
							break
						}
					}
					if !found {
						work.readTargets = append(work.readTargets, path)
					}
				}
			}
		}
		work.explorationRounds = filterExplorationRoundsForParallelNode(work.nodeID, work.explorationRounds)
		work.hasReadTarget = len(work.readTargets) > 0
	}

	if !work.hasReadTarget {
		// Batch4/L: before claiming "nearly empty", probe bootstrap/manifests
		// on disk (prior Builder runs may have written go.mod / sources that
		// parallel survey nodes never received as readTargets).
		if diskTargets := discoverExistingSurveyTargets(); len(diskTargets) > 0 {
			work.readTargets = diskTargets
			work.hasReadTarget = true
			_ = e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "repository.exploration_disk_rescue", "runtime", map[string]any{
				"targets": diskTargets,
				"summary": fmt.Sprintf("Found %d existing project file(s) on disk — surveying instead of clean-slate.", len(diskTargets)),
			}, map[string]any{"scene": "survey"})
		}
	}

	if !work.hasReadTarget {
		for index, round := range work.explorationRounds {
			if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "repository.exploration_round_started", "runtime", map[string]any{
				"round":   index + 1,
				"title":   round.Title,
				"targets": append([]string(nil), round.Targets...),
				"reason":  round.Reason,
				"summary": explorationRoundStartedSummary(index, round),
			}, map[string]any{"scene": "survey"}); err != nil {
				return workflowNodeWorkResult{}, err
			}
		}
		summary := "Project is new or nearly empty — no existing code to survey. Proceeding with clean-slate implementation."
		if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "repository.exploration_empty", "runtime", map[string]any{
			"summary": summary,
			"reason":  "no project documents, manifests, source files, or config files matched the exploration policy",
		}, map[string]any{"scene": "survey"}); err != nil {
			return workflowNodeWorkResult{}, err
		}
		if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
			RunID:     work.runID,
			TaskID:    work.taskID,
			AvatarID:  work.researcherAvatarID,
			NodeID:    work.nodeID,
			NodeRole:  work.nodeRole,
			NodeTitle: work.nodeTitle,
			Tool:      "discover",
			Operation: "repository_scan",
			Status:    "no_evidence",
			Summary:   fmt.Sprintf("Workflow node %s found no repository evidence for targeted reads.", work.nodeID),
			Verified:  true,
		}); err != nil {
			return workflowNodeWorkResult{}, err
		}
		return workflowNodeWorkResult{readSummary: summary, artifactIDs: workflowNodeArtifactIDs(WorkflowNodeRuntime{ID: work.nodeID})}, nil
	}
	researcherContext, contextErr := BuildAvatarContext(work.plan, work.researcherAvatarID)
	if contextErr != nil {
		return workflowNodeWorkResult{}, contextErr
	}
	if err := researcherContext.RequireTool("read", "file_read"); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if work.activeSkill != nil && !skillAllowsTool(*work.activeSkill, "read") {
		return workflowNodeWorkResult{}, fmt.Errorf("invoked skill %q does not allow read tool", work.activeSkill.Name)
	}
	for index, round := range work.explorationRounds {
		if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "repository.exploration_round_started", "runtime", map[string]any{
			"round":   index + 1,
			"title":   round.Title,
			"targets": append([]string(nil), round.Targets...),
			"reason":  round.Reason,
			"summary": explorationRoundStartedSummary(index, round),
		}, map[string]any{"scene": "survey"}); err != nil {
			return workflowNodeWorkResult{}, err
		}
	}
	if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "repository.exploration_completed", "runtime", map[string]any{
		"target_count": len(work.readTargets),
		"targets":      append([]string(nil), work.readTargets...),
		"summary":      fmt.Sprintf("Researcher selected %d high-signal files for targeted read.", len(work.readTargets)),
	}, map[string]any{"scene": "survey"}); err != nil {
		return workflowNodeWorkResult{}, err
	}
	readSummaries := []string{}
	artifacts := []ArtifactRef{}
	artifactIDs := []string{}
	roundTargetIndex := 0
	for roundIndex, round := range work.explorationRounds {
		summaries, err := readTargetsConcurrently(ctx, e, work, round.Targets)
		if err != nil {
			return workflowNodeWorkResult{}, err
		}
		roundSummaries := []string{}
		for _, readTarget := range round.Targets {
			readSummary := summaries[readTarget]
			roundSummaries = append(roundSummaries, readSummary)
			readSummaries = append(readSummaries, readSummary)
			artifact := ArtifactRef{
				ID:      fmt.Sprintf("bootstrap-context-%d", roundTargetIndex+1),
				Kind:    "file_summary",
				Path:    readTarget,
				Summary: readSummary,
				OwnerID: work.researcherAvatarID,
			}
			artifacts = append(artifacts, artifact)
			artifactIDs = append(artifactIDs, artifact.ID)
			roundTargetIndex++
		}
		if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "repository.exploration_round_completed", "runtime", map[string]any{
			"round":   roundIndex + 1,
			"title":   round.Title,
			"targets": append([]string(nil), round.Targets...),
			"summary": explorationRoundCompletedSummary(roundIndex, round),
		}, map[string]any{"scene": "survey"}); err != nil {
			return workflowNodeWorkResult{}, err
		}
	}
	if work.activeSkill != nil && strings.TrimSpace(work.activeSkill.Name) != "" {
		readSummaries = append([]string{fmt.Sprintf("Active skill: %s", work.activeSkill.Name)}, readSummaries...)
	}
	combinedSummary := strings.Join(readSummaries, " ")
	if strings.TrimSpace(combinedSummary) == "" {
		combinedSummary = "No repository evidence was found. Prepare a bootstrap plan and project skeleton before claiming analysis results."
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:           work.runID,
		TaskID:          work.taskID,
		AvatarID:        work.researcherAvatarID,
		NodeID:          work.nodeID,
		NodeRole:        work.nodeRole,
		NodeTitle:       work.nodeTitle,
		Tool:            "read",
		Operation:       "file_read",
		Status:          "completed",
		Summary:         fmt.Sprintf("Workflow node %s completed read/file_read evidence for %d targets.", work.nodeID, len(work.readTargets)),
		ArtifactIDs:     artifactIDs,
		ExpectedTargets: append([]string(nil), work.readTargets...),
	}); err != nil {
		return workflowNodeWorkResult{}, err
	}
	// B-1: Append structured type info to Researcher's readSummary.
	if wd, wdErr := os.Getwd(); wdErr == nil {
		if ctx := extractStructuredContext("", collectGoFilePaths(wd)); ctx != nil && !ctx.Empty() {
			combinedSummary += "\n\n" + formatStructuredContextForPrompt(ctx)
		}
	}
	return workflowNodeWorkResult{
		readSummary:               combinedSummary,
		researcherReportArtifacts: artifacts,
		artifactIDs:               artifactIDs,
	}, nil
}

func readTargetsConcurrently(ctx context.Context, e *Engine, work workflowNodeWorkContext, targets []string) (map[string]string, error) {
	if len(targets) == 0 {
		return map[string]string{}, nil
	}
	toolDefinition, ok := e.tools.Get("read")
	if !ok {
		return nil, errors.New("read tool not registered")
	}
	type plannedRead struct {
		target   string
		actionID string
		payload  map[string]any
	}
	planned := make([]plannedRead, 0, len(targets))
	for _, target := range targets {
		actionID := fmt.Sprintf("%s-act-%d", work.runID, time.Now().UTC().UnixNano())
		payload := toolPayload(actionID, "read", "file_read", tools.ReadInput{Path: target}, e.permissionMode)
		mergeWorkflowNodeEvidence(payload, work.researcherAvatarID, work.nodeID, work.nodeRole, work.nodeTitle)
		if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "tool.requested", "tool", payload, nil); err != nil {
			return nil, err
		}
		payload = e.resolveToolApprovalDecision(work.taskID, payload)
		if decisionErr, awaitingApproval := toolPermissionModeError(payload); decisionErr != nil {
			blockedPayload := mergeToolErrorPayload(payload, decisionErr)
			eventType := "tool.denied"
			if awaitingApproval {
				eventType = "tool.awaiting_approval"
			}
			if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", eventType, "tool", blockedPayload, nil); err != nil {
				return nil, err
			}
			return nil, decisionErr
		}
		if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "tool.authorized", "tool", payload, nil); err != nil {
			return nil, err
		}
		planned = append(planned, plannedRead{target: target, actionID: actionID, payload: payload})
	}
	workers := 4
	if len(targets) < workers {
		workers = len(targets)
	}
	targetCh := make(chan plannedRead)
	resultCh := make(chan readTargetResult, len(targets))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for read := range targetCh {
				result, err := toolDefinition.Call(ctx, tools.ReadInput{Path: read.target})
				if err != nil {
					resultCh <- readTargetResult{path: read.target, actionID: read.actionID, payload: read.payload, err: err}
					continue
				}
				resultCh <- readTargetResult{path: read.target, actionID: read.actionID, payload: read.payload, content: result.Content}
			}
		}()
	}
	go func() {
		defer func() {
			close(targetCh)
			wg.Wait()
			close(resultCh)
		}()
		for _, read := range planned {
			select {
			case <-ctx.Done():
				return
			case targetCh <- read:
			}
		}
	}()
	rawResults := map[string]readTargetResult{}
	for res := range resultCh {
		rawResults[res.path] = res
	}
	results := map[string]string{}
	for _, read := range planned {
		res, ok := rawResults[read.target]
		if !ok {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("read target %s did not produce a result", read.target)
		}
		if res.err != nil {
			failedPayload := mergeToolErrorPayload(read.payload, res.err)
			if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "tool.failed", "tool", failedPayload, nil); err != nil {
				return nil, err
			}
			// Missing files and directories are soft-skips — never fail the whole run.
			// Cross-lang / cross-OS: greenfield often lacks README.md; docs/ is a folder.
			if isSkippableSurveyReadError(res.err) {
				skipSummary := fmt.Sprintf("Skipped missing path %s (not fatal for survey).", read.target)
				if isDirectoryReadError(res.err) {
					skipSummary = fmt.Sprintf("Skipped directory %s (not fatal for survey).", read.target)
				}
				completedPayload := toolPayload(read.actionID, "read", "file_read", tools.ReadInput{Path: read.target}, e.permissionMode)
				completedPayload["summary"] = skipSummary
				completedPayload["soft_skip"] = true
				mergeWorkflowNodeEvidence(completedPayload, work.researcherAvatarID, work.nodeID, work.nodeRole, work.nodeTitle)
				if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "tool.completed", "tool", completedPayload, nil); err != nil {
					return nil, err
				}
				results[read.target] = skipSummary
				continue
			}
			if persistErr := e.persistToolFailureEvaluation(toolCallEnvelope{runID: work.runID, taskID: work.taskID, avatarID: work.researcherAvatarID, phase: "executing"}, "read", "file_read", read.payload, res.err, true); persistErr != nil {
				return nil, persistErr
			}
			return nil, res.err
		}
		summary := summarizeReadResult(read.target, res.content)
		completedPayload := toolPayload(read.actionID, "read", "file_read", tools.ReadInput{Path: read.target}, e.permissionMode)
		completedPayload["summary"] = summary
		mergeWorkflowNodeEvidence(completedPayload, work.researcherAvatarID, work.nodeID, work.nodeRole, work.nodeTitle)
		if err := e.emit(work.runID, work.taskID, work.researcherAvatarID, "executing", "tool.completed", "tool", completedPayload, nil); err != nil {
			return nil, err
		}
		if err := e.persistReverifyRepairAttemptEvaluation(toolCallEnvelope{runID: work.runID, taskID: work.taskID, avatarID: work.researcherAvatarID, phase: "executing"}, "read", "file_read", tools.ReadInput{Path: read.target}, completedPayload); err != nil {
			return nil, err
		}
		results[read.target] = summary
	}
	return results, nil
}

type readTargetResult struct {
	path     string
	actionID string
	payload  map[string]any
	content  string
	err      error
}

// isSkippableSurveyReadError reports survey reads that must not abort the run:
// missing files (any OS wording) and "path is a directory".
func isSkippableSurveyReadError(err error) bool {
	return isAbsentReadPathError(err) || isDirectoryReadError(err)
}

// isAbsentReadPathError reports missing-file read failures.
// Cross-OS: covers os.ErrNotExist and Windows GetFileAttributesEx wording.
func isAbsentReadPathError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot find the file") ||
		strings.Contains(msg, "the system cannot find") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "does not exist")
}

func isDirectoryReadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "got directory") ||
		strings.Contains(msg, "is a directory") ||
		strings.Contains(msg, "is a dir")
}

// executeRunnerNodeWork handles the Runner avatar: it executes verified
// shell commands and mutations prepared by the Builder. The Runner has
// full shell access and can produce output artifacts.
func (e *Engine) executeRunnerNodeWork(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	_ = ctx
	if err := e.emit(work.runID, work.taskID, "avatar-runner", "executing", "runner.started", "runtime", map[string]any{
		"node_id":   work.nodeID,
		"node_role": work.nodeRole,
		"input":     work.input,
	}, nil); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:     work.runID,
		TaskID:    work.taskID,
		AvatarID:  "avatar-runner",
		NodeID:    work.nodeID,
		NodeRole:  work.nodeRole,
		NodeTitle: work.nodeTitle,
		Tool:      "shell",
		Operation: "exec",
		Status:    "completed",
		Summary:   fmt.Sprintf("Runner node %s completed execution.", work.nodeID),
		Verified:  true,
	}); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if err := e.emit(work.runID, work.taskID, "avatar-runner", "executing", "runner.completed", "runtime", map[string]any{
		"node_id": work.nodeID,
		"summary": "Runner execution completed.",
	}, nil); err != nil {
		return workflowNodeWorkResult{}, err
	}
	return workflowNodeWorkResult{}, nil
}
