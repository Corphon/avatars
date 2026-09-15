package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/app"
	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/planner"
	"avatars/internal/runtime"
	"avatars/internal/tasks"
	"avatars/internal/workflow"
)

// C1.1: avatars run dispatch (parse + runTask).
func effectiveRunPermissionMode(mode runtime.PermissionMode) runtime.PermissionMode {
	if mode == runtime.PermissionModeDefault {
		return runtime.PermissionModeAcceptEdits
	}
	return mode
}

func upgradeRunCommandPermissionMode(command []string) []string {
	result := make([]string, len(command))
	copy(result, command)
	for i := 0; i < len(result)-1; i++ {
		if result[i] == "--permission-mode" && result[i+1] == "default" {
			result[i+1] = "acceptEdits"
			return result
		}
	}
	return result
}
func runTask(args []string) error {
	options, err := parseRunCommandOptions(args)
	if err != nil {
		return err
	}
	input := strings.TrimSpace(options.Input)
	fileContext := fileContextState{}
	if strings.TrimSpace(options.InputFile) != "" {
		content, resolved, err := readIntentInputFileWithResolve(options.InputFile)
		if err != nil {
			return err
		}
		fileContext = fileContextState{Path: resolved, Content: content}
		input = mergeRunFileAndExtraInput(resolved, content, input)
	}
	if input == "" {
		return errors.New("task input cannot be empty")
	}
	// Wire REPL's upgradeREPLPermissionMode into one-shot `run`: default mode
	// blocks on awaiting_approval because CLI approval prompts are not live yet.
	options.PermissionMode = effectiveRunPermissionMode(options.PermissionMode)
	// P4-2: For plan-mode runs, check the NL classifier first. If it
	// can answer the question deterministically (file counts, project
	// stats, etc.), print the answer directly instead of launching a
	// pipeline that can't execute data-gathering operations. This
	// prevents the "pipeline completes but answer is empty" failure
	// mode seen with "统计这个项目有多少Go文件" type requests.
	if options.PermissionMode == "plan" {
		decision := classifyNaturalLanguageQuestion(input, replCommandOptions{}, naturalLanguageContextREPL)
		if decision.Kind == naturalLanguageDecisionAnswer || decision.Kind == naturalLanguageDecisionMemory {
			if strings.TrimSpace(decision.Answer) != "" {
				fmt.Println(decision.Answer)
				if decision.Reason != "" {
					fmt.Fprintf(os.Stderr, "# %s\n", decision.Reason)
				}
				return nil
			}
		}
	}
	// Workflow actions (mark/confirm/replan/plan construction) MUST skip the
	// trivial fast path and go through the full runOnce pipeline. These are
	// meta-commands that modify workflow state, not simple file operations.
	isWorkflowMeta := workflow.IsMarkTaskDoneCheck(input) || workflow.IsConfirmPlan(input) ||
		workflow.IsReplan(input) || workflow.IsPhaseConstruction(input) || workflow.IsPlanConstruction(input)

	// Trivial-task fast path: when the complexity assessment is "trivial"
	// and the input looks like a simple script or edit request, dispatch
	// directly to script --apply / edit --apply instead of launching the
	// multi-avatar workflow. This avoids the overhead of 3+ LLM calls
	// and workflow coordination for one-shot, well-defined tasks.
	assessment := planner.Assess(input)
	if !isWorkflowMeta && assessment.Level == planner.ComplexityTrivial {
		lowered := strings.ToLower(input)
		if looksLikeSimpleScriptRequest(lowered) {
			scriptPath := extractSimpleScriptPath(input)
			if scriptPath == "" {
				scriptPath = inferSimpleScriptPath(input)
			}
			if scriptPath != "" {
				scriptPath = nextAvailableScriptPath(scriptPath)
				return runScript([]string{"--apply", "--llm", scriptPath, input})
			}
		}
		if looksLikeImplementationWorkIntent(lowered) {
			if paths, err := editablePathsFromInstruction(input); err == nil && len(paths) >= 1 {
				if len(paths) == 1 {
					return runEdit([]string{"--apply", paths[0], input})
				}
				return runEditMany([]string{"--apply", input})
			}
		}
		// P4-2: In plan mode, never let a trivial assessment fall through
		// to the Direct avatar path. The Direct avatar tries to execute
		// shell/write actions which plan mode blocks, producing "permission
		// denied" failures. Instead, force a small pipeline
		// (Planner+Builder+Synthesizer) that can handle analysis tasks
		// within plan mode's read-only constraints.
		if options.PermissionMode == "plan" && !looksLikeSimpleScriptRequest(lowered) && !looksLikeImplementationWorkIntent(lowered) {
			assessment = planner.ComplexityAssessment{
				Level:            planner.ComplexitySmall,
				SuggestedAvatars: []string{"Planner", "Builder", "Synthesizer"},
				ParallelGroups:   []int{2, 1},
				Reason:           "plan-mode analysis task: cannot use Direct avatar",
			}
		}
		// Trivial but not a script/edit: fall through to the normal
		// multi-avatar workflow (the trivialPlan will use a single
		// "Direct" avatar, which is already leaner than the full
		// 5-avatar chain).
	}
	if options.ForceNewTask && options.ResumeTranscript != "" {
		return errors.New("--new-task cannot be combined with --resume or --continue-from-pause")
	}

	taskManager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace := tasks.Workspace{}
	created := false
	if options.ResumeTranscript != "" {
		resolvedWorkspace, ok, err := taskManager.LoadByTranscript(options.ResumeTranscript)
		if err != nil {
			return err
		}
		if ok {
			workspace = resolvedWorkspace
		} else if options.TaskID == "" {
			// Batch5/P: derive task id from transcript path so soft resume
			// reuses the same workspace even if LoadByTranscript failed briefly.
			if id := taskIDFromTranscriptPath(options.ResumeTranscript); id != "" {
				if loaded, loadErr := taskManager.Load(id); loadErr == nil {
					workspace = loaded
				}
			}
		}
	}
	if workspace.ID == "" {
		// Never ForceNew on --resume — soft resume must reuse/create under same slug only without --new-task.
		forceNew := options.ForceNewTask
		if options.ResumeTranscript != "" {
			forceNew = false
			if options.TaskID == "" {
				if id := taskIDFromTranscriptPath(options.ResumeTranscript); id != "" {
					options.TaskID = id
				}
			}
		}
		workspace, created, err = taskManager.Resolve(tasks.ResolveOptions{ID: options.TaskID, Title: input, ForceNew: forceNew})
		if err != nil {
			return err
		}
	}
	autoResumeTranscript := ""
	if options.ResumeTranscript == "" && !created {
		autoResumeTranscript = workspace.LatestTranscriptPath()
	}
	application, err := app.BootstrapWithOptions(app.BootstrapOptions{Workspace: &workspace, PermissionMode: options.PermissionMode})
	if err != nil {
		return err
	}
	if strings.TrimSpace(fileContext.Path) != "" {
		application.Engine.WithAttachedFile(fileContext.Path, fileContext.Content)
	} else if lastPath := getLastAttachedFilePath(); strings.TrimSpace(lastPath) != "" {
		application.Engine.WithAttachedFile(lastPath, "[File from previous @file — read directly]")
	}
	if strings.TrimSpace(getReplContextForRun()) != "" {
		application.Engine.WithREPLContext(getReplContextForRun())
	}
	defer func() {
		_ = application.Close()
	}()
	if progress := strings.TrimSpace(options.Progress); progress != "" {
		_ = os.Setenv("AVATARS_PROGRESS", progress)
	}
	stopProgress := startRunProgressPrinter(application.Events)
	defer stopProgress()
	var result runtime.RunResult
	sourceTranscript := options.ResumeTranscript
	if sourceTranscript == "" {
		sourceTranscript = autoResumeTranscript
	}
	if sourceTranscript != "" {
		mode := classifyCLIResumeMode(false, options.ContinueFromPause)
		printResumeDispatchBanner(mode, sourceTranscript, "")
		result, err = application.Engine.RunWithResume(context.Background(), input, sourceTranscript)
		if strings.TrimSpace(result.ResumeBoundaryType) != "" {
			fmt.Printf("Boundary type: %s\n", result.ResumeBoundaryType)
		}
	} else {
		result, err = application.Engine.Run(context.Background(), input)
	}
	// P7: If the engine paused for clarification, present questions
	// to the user and re-run with their answers as constraints.
	if result.ClarifyPending != nil {
		result, err = handleClarifyLoop(application.Engine, input, result)
	}
	runErr := err
	// P10: Record run in session state for cross-run continuity.
	if wd, wdErr := os.Getwd(); wdErr == nil {
		sessionMgr := runtime.NewSessionManager(wd)
		sessionMgr.RecordRun(runtime.RunRecord{
			RunID:  result.TranscriptPath,
			Input:  input,
			Status: "running",
		})
		defer func() {
			status := "completed"
			if runErr != nil {
				status = "failed"
			}
			sessionMgr.MarkRunComplete(status, result.Summary, nil)
			extractor := runtime.NewMemoryExtractor(wd)
			memories := extractor.ExtractFromRun(
				result.TranscriptPath, input, status,
				result.Summary, result.CriticInsights,
			)
			_ = extractor.Persist(memories)
		}()
	}
	if result.TranscriptPath != "" {
		summary := strings.TrimSpace(result.Summary)
		if summary == "" && runErr != nil {
			summary = runErr.Error()
		}
		statusHint := ""
		if runErr != nil {
			statusHint = "failed"
		}
		workspace, err = taskManager.MarkRunOutcome(workspace, result.TranscriptPath, summary, statusHint)
		if err != nil {
			return err
		}
	}
	if err := writeAnalysisReportResult(result); err != nil {
		return err
	}
	// Update project workflow record after a successful run.
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		summary := strings.TrimSpace(result.Summary)
		if summary != "" {
			_ = workflow.RecordTaskCompletion(cwd, input, summary)
		}
	}
	// Auto-retry on verification failure: pursue the outcome.
	// F7: do NOT auto-retry construct_plan / confirm_plan quality failures —
	// that papered over F2 abort and burned a full Builder pass on a bad plan.
	if runErr == nil && result.TranscriptPath != "" && shouldAutoRetryAfterRun(result) {
		retryInput := "Previous attempt had issues: " + firstLine(result.Summary, 200) + ". Fix and retry. Original: " + input
		retryResult, retryErr := application.Engine.Run(context.Background(), retryInput)
		if retryErr == nil {
			result = retryResult
			runErr = nil
			if result.TranscriptPath != "" {
				workspace, _ = taskManager.MarkRun(workspace, result.TranscriptPath, strings.TrimSpace(result.Summary))
			}
		}
	}
	if runErr != nil {
		if hint := formatRunLLMFailure(runErr); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
		if result.ReportPath != "" {
			fmt.Printf("Analysis report: %s\n", result.ReportPath)
		}
		return runErr
	}

	taskMode := "stable"
	if created {
		taskMode = "new"
	}

	if displaySummary := runSummaryForDisplay(result.Summary); displaySummary != "" {
		fmt.Println(displaySummary)
	}
	// Cursor-style file change stats + next-step suggestions.
	if footer := result.Footer.FormatCLI(); strings.TrimSpace(footer) != "" && len(result.Footer.Files)+len(result.Footer.NextSteps) > 0 {
		fmt.Println(footer)
	} else if wd, err := os.Getwd(); err == nil {
		// Fallback when Footer was wiped (e.g. flush edge) — still show phase-based tips.
		fb := runtime.BuildRunFooter(wd, nil, runtime.NextStepInput{
			Status:              footerStatusForCLI(result),
			Summary:             result.Summary,
			BuildOK:             result.BuildOK,
			BuildOKKnown:        result.BuildOKKnown,
			PhaseAdvanceBlocked: result.PhaseAdvanceBlocked,
		})
		if text := fb.FormatCLI(); strings.TrimSpace(text) != "" {
			fmt.Println(text)
		}
	}
	// Show workflow progress: what phase we're on, what's done, what's next.
	// Batch4/I: bind footer to the same build gate that blocks phase advance.
	if wd, err := os.Getwd(); err == nil {
		if progress := runtime.BuildProgressSummaryWithGate(wd, result.BuildOK, result.BuildOKKnown, result.PhaseAdvanceBlocked); progress != "" {
			fmt.Println(progress)
		}
	}
	fmt.Printf("Task: %s\n", workspace.ID)
	fmt.Printf("Task mode: %s\n", taskMode)
	fmt.Printf("Task root: %s\n", workspace.RootDir)
	fmt.Printf("Permission mode: %s\n", options.PermissionMode)
	if result.InvokedSkillName != "" {
		fmt.Printf("Invoked skill: %s\n", result.InvokedSkillName)
	}
	if result.GeneratedSkillPath != "" {
		fmt.Printf("Generated skill: %s\n", result.GeneratedSkillPath)
	}
	if result.GeneratedSkillPreview != "" {
		fmt.Printf("Skill preview: %s\n", result.GeneratedSkillPreview)
	}
	if strings.TrimSpace(result.SynthesisStatus) != "" {
		fmt.Printf("Synthesis status: %s\n", result.SynthesisStatus)
	}
	if result.ReportPath != "" {
		fmt.Printf("Analysis report: %s\n", result.ReportPath)
	}
	fmt.Printf("Transcript: %s\n", result.TranscriptPath)
	return nil
}

// formatRunLLMFailure returns a CLI-facing line when the primary provider
// failed and fallback_providers was empty or unusable (C6.2 / S5.4).
func formatRunLLMFailure(err error) string {
	if err == nil {
		return ""
	}
	if llm.IsQuotaOrBillingFailure(err.Error()) {
		return "LLM provider billing/quota error (HTTP 402 / insufficient balance). On-disk compile/test was not treated as a build failure. Add credit or switch providers; this run will not auto-retry."
	}
	if !llm.IsPrimaryFailureWithoutFallbacks(err) {
		return ""
	}
	return "Primary LLM provider failed; no usable fallback_providers. Fix the primary key/endpoint or add fallback_providers in agent.yaml llm_defaults."
}

func runSummaryForDisplay(summary string) string {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return ""
	}
	const displayLimit = 600
	compacted := compactRunSummaryForDisplay(trimmed, displayLimit)
	if compacted == trimmed {
		return trimmed
	}
	return compacted + "\nSummary compacted. Full summary preserved in task transcript and task memory."
}

func compactRunSummaryForDisplay(summary string, limit int) string {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return ""
	}
	if limit <= 0 || len([]rune(trimmed)) <= limit {
		return trimmed
	}
	segments := splitRunSummarySegments(trimmed)
	if len(segments) == 0 {
		return conciseNLContextLine(trimmed, limit)
	}
	selected := []string{conciseNLContextLine(segments[0], 220)}
	for _, marker := range []string{
		"Focused verifier failure:",
		"Run status:",
		"Synthesis status:",
		"Analysis report:",
		"Result location:",
		"Transcript:",
		"Code Trace Evidence",
		"Evidence Coverage",
		"Continued from transcript",
		"Invoked skill:",
		"Generated skill:",
		"Skill preview:",
		"Synthesis:",
		"Verification",
	} {
		for _, segment := range segments[1:] {
			if !strings.Contains(segment, marker) {
				continue
			}
			line := conciseNLContextLine(segment, 220)
			if !stringSliceContains(selected, line) {
				selected = append(selected, line)
			}
			break
		}
		if len(selected) >= 5 {
			break
		}
	}
	if len(selected) == 1 {
		for _, segment := range segments[1:] {
			if strings.HasPrefix(segment, "Read ") {
				continue
			}
			line := conciseNLContextLine(segment, 220)
			if line != "" && !stringSliceContains(selected, line) {
				selected = append(selected, line)
				break
			}
		}
	}
	compacted := strings.Join(selected, "\n")
	if len([]rune(compacted)) > limit {
		return conciseNLContextLine(compacted, limit)
	}
	return compacted
}

func splitRunSummarySegments(summary string) []string {
	normalized := strings.ReplaceAll(strings.TrimSpace(summary), "\r\n", "\n")
	lines := []string{}
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) > 1 {
		return lines
	}
	parts := strings.Split(normalized, ". ")
	segments := []string{}
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if !strings.HasSuffix(trimmed, ".") && strings.Contains(summary, trimmed+".") {
			trimmed += "."
		}
		segments = append(segments, trimmed)
	}
	return segments
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func writeAnalysisReportResult(result runtime.RunResult) error {
	if strings.TrimSpace(result.ReportPath) == "" || strings.TrimSpace(result.ReportContent) == "" {
		return nil
	}
	if err := os.WriteFile(result.ReportPath, []byte(result.ReportContent), 0o644); err != nil {
		return err
	}
	return nil
}

func runProgressLine(event events.Envelope) string {
	line := runProgressLineBody(event)
	if line == "" {
		return ""
	}
	return decorateParallelProgressLine(event, line)
}

// runProgressLineBody maps runtime events to operator-facing stderr lines.
func runProgressLineBody(event events.Envelope) string {
	return runtime.NarrateRunProgress(event)
}

// decorateParallelProgressLine prefixes parallel survey / node-scoped events
// so concurrent waves are attributable on stderr (S6.5 progress tree seed).
func decorateParallelProgressLine(event events.Envelope, line string) string {
	nodeID := payloadStringValue(event.Payload, "node_id")
	if nodeID == "" {
		nodeID = payloadStringValue(event.Payload, "to_node_id")
	}
	if nodeID == "" {
		return line
	}
	lower := strings.ToLower(nodeID)
	if !strings.Contains(lower, "survey") && !strings.HasPrefix(lower, "node-") {
		return line
	}
	switch event.Type {
	case "workflow.node_activated", "workflow.node_completed", "avatar.assigned",
		"avatar.handoff", "repository.exploration_round_started",
		"repository.exploration_round_completed", "repository.exploration_completed",
		"repository.exploration_empty", "tool.requested", "tool.completed":
		if strings.HasPrefix(line, "[") {
			return line
		}
		return fmt.Sprintf("[%s] %s", nodeID, line)
	default:
		return line
	}
}

func payloadStringValue(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func payloadIntValue(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func canonicalizeRunFlag(arg string) string {
	if !strings.HasPrefix(arg, "-") {
		return arg
	}
	name := strings.TrimLeft(arg, "-")
	switch name {
	case "from-file", "task", "new-task", "resume", "continue-from-pause", "permission-mode", "progress":
		return "--" + name
	default:
		return arg
	}
}

func parseRunCommandOptions(args []string) (runCommandOptions, error) {
	options := runCommandOptions{PermissionMode: runtime.PermissionModeAcceptEdits}
	for len(args) > 0 {
		args[0] = canonicalizeRunFlag(args[0])
		switch args[0] {
		case "--from-file":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars run [--task <task-id>] [--new-task] [--resume <transcript-path>] [--permission-mode <mode>] [--from-file <path>] \"<task>\"")
			}
			options.InputFile = strings.TrimSpace(args[1])
			args = args[2:]
		case "--task":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars run [--task <task-id>] [--new-task] [--resume <transcript-path>] [--permission-mode <mode>] [--from-file <path>] \"<task>\"")
			}
			options.TaskID = strings.TrimSpace(args[1])
			args = args[2:]
		case "--new-task":
			options.ForceNewTask = true
			args = args[1:]
		case "--resume":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars run [--task <task-id>] [--new-task] [--resume <transcript-path>] [--continue-from-pause <transcript-path>] [--permission-mode <mode>] [--from-file <path>] \"<task>\"")
			}
			options.ResumeTranscript = strings.TrimSpace(args[1])
			args = args[2:]
		case "--continue-from-pause":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars run [--continue-from-pause <transcript-path>] \"<task>\"")
			}
			if options.ResumeTranscript != "" && options.ResumeTranscript != strings.TrimSpace(args[1]) {
				return runCommandOptions{}, errors.New("--resume and --continue-from-pause cannot target different transcripts")
			}
			options.ContinueFromPause = true
			options.ResumeTranscript = strings.TrimSpace(args[1])
			args = args[2:]
		case "--permission-mode":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars run [--task <task-id>] [--new-task] [--resume <transcript-path>] [--permission-mode <mode>] [--progress <text|json|off>] [--from-file <path>] \"<task>\"")
			}
			permissionMode, err := runtime.ParsePermissionMode(args[1])
			if err != nil {
				return runCommandOptions{}, err
			}
			options.PermissionMode = permissionMode
			args = args[2:]
		case "--progress":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars run ... --progress <text|json|off>")
			}
			options.Progress = strings.TrimSpace(args[1])
			args = args[2:]
		default:
			options.Input = strings.TrimSpace(strings.Join(args, " "))
			return options, nil
		}
	}
	return options, nil
}

func mergeRunFileAndExtraInput(filePath, content, extra string) string {
	extra = strings.TrimSpace(extra)
	content = strings.TrimSpace(content)
	if extra == "" {
		return content
	}
	if content == "" {
		return extra
	}
	if extraIsFromFileKeepGoing(filePath, extra) {
		return content
	}
	return extra + "\n\n" + content
}

func extraIsFromFileKeepGoing(filePath, extra string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(filePath)))
	lowered := strings.ToLower(strings.TrimSpace(extra))
	if lowered == base || strings.EqualFold(strings.TrimSpace(extra), strings.TrimSpace(filePath)) {
		return true
	}
	file, rest := splitTrailingGuidanceFile(lowered)
	if file != "" {
		fileBase := strings.ToLower(filepath.Base(file))
		if fileBase == base || strings.EqualFold(file, strings.TrimSpace(filePath)) {
			if strings.TrimSpace(rest) == "" || looksLikeContinuationWorkRequest(rest) {
				return true
			}
		}
	}
	return looksLikeContinuationWorkRequest(lowered)
}
