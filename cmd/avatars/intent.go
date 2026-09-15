package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"avatars/internal/app"
	"avatars/internal/llm"
	"avatars/internal/workflow"
)

type intentCommandOptions struct {
	Choose    int
	Confirm   bool
	Input     string
	InputFile string
}

type intentCandidate struct {
	Command     []string
	Summary     string
	Confidence  int
	AutoSafe    bool
	NeedsChoice bool
}

type intentLLMDecision struct {
	Action     string `json:"action"`
	Choice     int    `json:"choice"`
	Question   string `json:"question"`
	Confidence int    `json:"confidence"`
}

func runIntent(args []string) error {
	options, err := parseIntentCommandOptions(args)
	if err != nil {
		return err
	}
	input := strings.TrimSpace(options.Input)
	fileContext := fileContextState{}
	if strings.HasPrefix(input, "@") {
		path, suffix := splitPlanFileInput(strings.TrimSpace(strings.TrimPrefix(input, "@")))
		if path != "" {
			content, resolved, readErr := readIntentInputFileWithResolve(path)
			if readErr != nil {
				return readErr
			}
			fileContext = fileContextState{Path: resolved, Content: content}
			if resolved != path {
				path = resolved
			}
			input = strings.TrimSpace(suffix)
			if input == "" {
				input = attachedFileDefaultInput(content)
			}
			fmt.Printf("Intent input file loaded: %s (%d chars)\n", path, len(content))
		}
	}
	if strings.TrimSpace(options.InputFile) != "" {
		content, resolved, readErr := readIntentInputFileWithResolve(options.InputFile)
		if readErr != nil {
			return readErr
		}
		fmt.Printf("Intent input file loaded: %s (%d chars)\n", resolved, len(content))
		fileContext = fileContextState{Path: resolved, Content: content}
		// Empty extra input uses the file body when it is a task spec or a
		// continuation/repair note; other files keep the summarize default.
		if input == "" {
			input = attachedFileDefaultInput(content)
		}
	}
	if input == "" {
		return errors.New("intent cannot be empty")
	}
	if strings.TrimSpace(fileContext.Path) != "" {
		setReplFileContext(&fileContext)
		defer func() {
			setReplFileContext(nil)
		}()
	}
	route, handled, routeErr := resolveNaturalLanguageQuestion(input, replCommandOptions{}, naturalLanguageContextIntent)
	if routeErr != nil {
		return routeErr
	}
	if handled {
		switch route.Kind {
		case naturalLanguageRouteDirectAnswer:
			fmt.Println(route.Answer)
			return nil
		}
	}
	candidates := routeIntent(input)
	if handled && route.Kind == naturalLanguageRouteSafeRun && shouldExecuteNaturalLanguageSafeRun(route, candidates) {
		return executeIntentSafeRun(input, route.Summary, route.Command)
	}
	if options.Choose > 0 {
		if options.Choose > len(candidates) {
			return fmt.Errorf("intent choice %d is out of range", options.Choose)
		}
		selected := candidates[options.Choose-1]
		if selected.NeedsChoice && !options.Confirm {
			return fmt.Errorf("intent choice %d requires --confirm before executing: avatars %s", options.Choose, strings.Join(selected.Command, " "))
		}
		return executeIntentSafeRun(input, selected.Summary, selected.Command)
	}
	if len(candidates) > 1 {
		if decision, ok := analyzeIntentWithLLM(input, candidates); ok {
			if decision.Action == "clarify" && strings.TrimSpace(decision.Question) != "" {
				fmt.Printf("Clarify: %s\n", decision.Question)
				printIntentChoices(input, candidates)
				return nil
			}
			if decision.Action == "choose" && decision.Choice >= 1 && decision.Choice <= len(candidates) {
				selected := candidates[decision.Choice-1]
				if selected.NeedsChoice && !options.Confirm {
					fmt.Printf("Intent route: %s\n", selected.Summary)
					fmt.Printf("Choice needs confirmation: avatars %s\n", strings.Join(selected.Command, " "))
					printIntentChoices(input, candidates)
					return nil
				}
				return executeIntentSafeRun(input, selected.Summary, selected.Command)
			}
		}
	}
	if handled && route.Kind == naturalLanguageRouteClarify && len(candidates) == 0 {
		fmt.Printf("Clarify: %s\n", route.Question)
		if isGuardedIntentClarify(route) {
			return fmt.Errorf("intent is ambiguous or risky")
		}
		return nil
	}
	if len(candidates) == 0 {
		// S1.7: Pass real LLM availability/parse flags, not (handled, handled).
		llmAvailable := routeErr == nil
		llmParseOK := handled && route.Kind != naturalLanguageRouteClarify
		fb := buildFallbackResponse(input, routeErr, llmAvailable, llmParseOK)
		printFallbackResponse(fb)
		if fb.AutoRun {
			fmt.Printf("\nTo execute directly:\n  avatars run %s\n", input)
		}
		return nil
	}
	if len(candidates) == 1 && candidates[0].AutoSafe && !candidates[0].NeedsChoice {
		selected := candidates[0]
		return executeIntentSafeRun(input, selected.Summary, selected.Command)
	}
	printIntentChoices(input, candidates)
	return nil
}

func executeIntentSafeRun(input string, summary string, command []string) error {
	fmt.Println(executionCueTaskIntro(input))
	fmt.Println(intentExecutionCue(input, command))
	fmt.Printf("Intent route: %s\n", summary)
	fmt.Printf("Executing: avatars %s\n", strings.Join(command, " "))
	if len(command) > 0 && command[0] == "run" && strings.TrimSpace(os.Getenv("AVATARS_INTENT_VERBOSE")) != "1" {
		captured, err := runREPLCommandCompactly(command)
		if err != nil {
			if approval := summarizeLatestApprovalBlock("."); looksLikeApprovalRequiredError(err) && approval != "" {
				fmt.Println(approval)
				return nil
			}
			if strings.TrimSpace(captured) != "" {
				fmt.Println(compactIntentRunCapturedOutput(captured))
			}
			return err
		}
		if answer, ok, err := summarizeLatestRunAnswer("."); err == nil && ok && strings.TrimSpace(answer) != "" {
			fmt.Println(answer)
		} else if compacted := compactIntentRunCapturedOutput(captured); compacted != "" {
			fmt.Println(compacted)
		}
		if location, err := summarizeResultLocation("."); err == nil && strings.TrimSpace(location) != "" {
			fmt.Println(location)
		}
		return nil
	}
	if err := run(command); err != nil {
		if approval := summarizeLatestApprovalBlock("."); looksLikeApprovalRequiredError(err) && approval != "" {
			fmt.Println(approval)
			return nil
		}
		return err
	}
	if len(command) > 0 && command[0] == "run" {
		if answer, ok, err := summarizeLatestRunAnswer("."); err == nil && ok && strings.TrimSpace(answer) != "" {
			fmt.Println(answer)
		}
	}
	if len(command) > 0 && command[0] == "run" {
		if location, err := summarizeResultLocation("."); err == nil && strings.TrimSpace(location) != "" {
			fmt.Println(location)
		}
	}
	return nil
}

func compactIntentRunCapturedOutput(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	selected := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || intentRunProgressLine(trimmed) {
			continue
		}
		if intentRunKeepLine(trimmed) {
			if !stringSliceContains(selected, trimmed) {
				selected = append(selected, trimmed)
			}
		}
	}
	if len(selected) == 0 {
		return ""
	}
	return conciseNLContextLine(strings.Join(selected, "\n"), 1200)
}

func intentRunProgressLine(line string) bool {
	for _, prefix := range []string{
		"Running:",
		"▶ ",
		"Avatar ",
		"● ",
		"▸ ",
		"→ ",
		"◈ ",
		"⚙ ",
		"✍ ",
		"✎ ",
		"$ ",
		"🔎 ",
		"⏳ ",
		"⌛ ",
		"🔍 ",
		"✓ ",
		"✗ ",
		"⚠ ",
		"🛡 ",
		"📋 ",
		"☑ ",
		"⛔ ",
		"📄 ",
		"◎ ",
		"📎 ",
		"🏁 ",
		"Planner starts:",
		"Researcher starts:",
		"Builder starts:",
		"Critic starts:",
		"Synthesizer starts:",
		"Node complete:",
		"Reading:",
		"Read ",
		"Researcher pass ",
		"LLM drafting",
		"LLM draft done",
		"Skill draft ready:",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func intentRunKeepLine(line string) bool {
	for _, prefix := range []string{
		"Multi-avatar planning run complete",
		"Run stopped",
		"Run status:",
		"Synthesis:",
		"Task:",
		"Task mode:",
		"Task root:",
		"Permission mode:",
		"Synthesis status:",
		"Analysis report:",
		"Transcript:",
		"Summary compacted.",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func intentExecutionCue(input string, command []string) string {
	if isBootstrapRouteCommand(command) {
		return executionCueBootstrap(input)
	}
	if isScriptRouteCommand(command) {
		return executionCueScript(input)
	}
	if isDirectActionRouteCommand(command) {
		return executionCueDirect(input)
	}
	if mode := routeCommandPermissionMode(command); isMutatingRunPermissionMode(mode) {
		return executionCueForMutatingRun(mode, input)
	}
	return executionCuePlan(input)
}

func printIntentRunResultLocation(command []string) {
	if len(command) == 0 || command[0] != "run" {
		return
	}
	if location, err := summarizeResultLocation("."); err == nil && strings.TrimSpace(location) != "" {
		fmt.Println(location)
	}
}

func shouldExecuteNaturalLanguageSafeRun(route naturalLanguageRoute, candidates []intentCandidate) bool {
	if route.Kind != naturalLanguageRouteSafeRun {
		return false
	}
	if isDirectActionRouteCommand(route.Command) {
		return true
	}
	if isWorkflowMetaRunCommand(route.Command) {
		return true
	}
	if len(candidates) == 0 {
		return true
	}
	for _, candidate := range candidates {
		if !isGenericTaskCandidate(candidate) {
			return false
		}
	}
	return true
}

func isWorkflowMetaRunCommand(command []string) bool {
	if len(command) < 2 || command[0] != "run" {
		return false
	}
	input := strings.TrimSpace(command[len(command)-1])
	return workflow.IsConfirmPlan(input) || workflow.IsReplan(input)
}

func isGenericTaskCandidate(candidate intentCandidate) bool {
	if len(candidate.Command) == 0 {
		return false
	}
	return candidate.Command[0] == "run"
}

func parseIntentCommandOptions(args []string) (intentCommandOptions, error) {
	options := intentCommandOptions{}
	for len(args) > 0 {
		switch args[0] {
		case "--from-file":
			if len(args) < 2 {
				return intentCommandOptions{}, errors.New("usage: avatars intent [--choose <n>] [--confirm] [--from-file <path>] \"<intent>\"")
			}
			options.InputFile = strings.TrimSpace(args[1])
			args = args[2:]
		case "--choose":
			if len(args) < 2 {
				return intentCommandOptions{}, errors.New("usage: avatars intent [--choose <n>] [--confirm] [--from-file <path>] \"<intent>\"")
			}
			choice, err := strconv.Atoi(strings.TrimSpace(args[1]))
			if err != nil || choice < 1 {
				return intentCommandOptions{}, errors.New("intent --choose requires a positive number")
			}
			options.Choose = choice
			args = args[2:]
		case "--confirm":
			options.Confirm = true
			args = args[1:]
		default:
			options.Input = strings.TrimSpace(strings.Join(args, " "))
			if options.InputFile != "" && options.Input != "" {
				return intentCommandOptions{}, errors.New("usage: avatars intent [--choose <n>] [--confirm] [--from-file <path>] \"<intent>\"")
			}
			return options, nil
		}
	}
	return options, nil
}

// routeIntent returns intent candidates for the `avatars intent` and
// routeIntent converts LLM router decisions into legacy intent candidates.
// S6.3: No keyword taxonomy. Primary path is routeIntentViaLLM; this wrapper
// exists for `avatars intent` / `avatars route` entry points and tests that
// still speak in candidates.
func routeIntent(input string) []intentCandidate {
	decision, ok := routeIntentViaLLM(input)
	if !ok {
		return offlineIntentCandidates(input)
	}
	cands := llmRouterDecisionToCandidates(input, decision)
	if len(cands) == 1 && isAutoSafeIntentCommand(cands[0].Command) {
		cands[0].AutoSafe = true
	}
	return cands
}

func isAutoSafeIntentCommand(command []string) bool {
	return isReadOnlyUtilityCommand(command) || isAutoSafeStageCommand(command)
}

func llmRouterDecisionToCandidates(input string, decision llmRouterDecision) []intentCandidate {
	action := strings.ToLower(strings.TrimSpace(decision.Action))
	switch action {
	case "safe_run":
		if len(decision.Command) == 0 {
			return nil
		}
		return []intentCandidate{{
			Summary:    firstNonEmpty(decision.Reason, "llm router safe_run"),
			Confidence: decision.Confidence,
			Command:    append([]string(nil), decision.Command...),
		}}
	case "direct_answer", "memory_answer":
		return []intentCandidate{{
			Summary:    firstNonEmpty(decision.AnswerID, decision.Reason, "llm router direct_answer"),
			Confidence: decision.Confidence,
			Command:    []string{"repl"}, // informational — not executed as a task
		}}
	case "clarify", "guarded":
		return nil
	default:
		_ = input
		return nil
	}
}

func isGuardedIntentClarify(route naturalLanguageRoute) bool {
	blob := strings.ToLower(route.Summary + "\n" + route.Question)
	return strings.Contains(blob, "safety audit") ||
		strings.Contains(blob, "looks risky") ||
		strings.Contains(blob, "mass delete") ||
		strings.Contains(blob, "wipe-all")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// NL5: The following keyword-based intent routing functions have been removed.
// Intent routing is now handled by routeIntentViaLLM (nl_llm_router.go) which
// gives the LLM the full CLI_guide.md catalog and returns a specific command.

func containsAnyIntentToken(value string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(value, strings.ToLower(strings.TrimSpace(token))) {
			return true
		}
	}
	return false
}

func printIntentChoices(input string, candidates []intentCandidate) {
	fmt.Printf("Intent needs choice: %s\n", input)
	fmt.Println("Candidates:")
	for index, candidate := range candidates {
		fmt.Printf("%d. %s | confidence=%d | command=avatars %s\n", index+1, candidate.Summary, candidate.Confidence, strings.Join(candidate.Command, " "))
	}
	fmt.Println("Run one candidate:")
	fmt.Println("avatars intent --choose <n> --confirm \"<intent>\"")
}

func looksLikeImplementationWorkIntent(lowered string) bool {

	// P4-3: "fix" token in analysis context (e.g., "find all FIXME")
	// is not a code-fix request. Strip the false signal.
	if isAnalysisRequest(lowered) {
		return false
	}
	// Direct checks for CJK action tokens — avoids containsAnyIntentToken variadic
	// encoding issues with long multi-byte token lists.
	actionSignal := strings.Contains(lowered, "实现") ||
		strings.Contains(lowered, "增加") ||
		strings.Contains(lowered, "新增") ||
		strings.Contains(lowered, "添加") ||
		strings.Contains(lowered, "支持") ||
		strings.Contains(lowered, "修改") ||
		strings.Contains(lowered, "更新") ||
		strings.Contains(lowered, "修复") ||
		strings.Contains(lowered, "改代码") ||
		strings.Contains(lowered, "写代码") ||
		strings.Contains(lowered, "同步更新") ||
		strings.Contains(lowered, "写") ||
		strings.Contains(lowered, "搞") ||
		strings.Contains(lowered, "弄") ||
		strings.Contains(lowered, "做") ||
		containsAnyIntentToken(lowered,
			"implement", "add", "support", "modify", "update", "change", "write code", "edit",
			"fix", "repair",
		)

	// Direct checks for CJK target tokens.
	targetSignal := strings.Contains(lowered, "命令") ||
		strings.Contains(lowered, "子命令") ||
		strings.Contains(lowered, "功能") ||
		strings.Contains(lowered, "函数") ||
		strings.Contains(lowered, "接口") ||
		strings.Contains(lowered, "代码") ||
		strings.Contains(lowered, "文件") ||
		strings.Contains(lowered, "测试") ||
		strings.Contains(lowered, "验证") ||
		strings.Contains(lowered, "游戏") ||
		strings.Contains(lowered, "程序") ||
		strings.Contains(lowered, "应用") ||
		strings.Contains(lowered, "工具") ||
		strings.Contains(lowered, "小工具") ||
		strings.Contains(lowered, "网页") ||
		strings.Contains(lowered, "网站") ||
		strings.Contains(lowered, "服务") ||
		containsAnyIntentToken(lowered,
			"command", "subcommand", "feature", "function", "handler", "endpoint", "cli", "code", "file", "test", "readme",
			"game", "program", "app", "tool", "utility", "calculator", "demo", "prototype",
		)

	return actionSignal && targetSignal
}

func intentRequestsReadOnly(lowered string) bool {
	for _, token := range []string{
		"do not modify",
		"don't modify",
		"no code changes",
		"read-only",
		"readonly",
		"inspect only",
		"analyze only",
		"不要改",
		"不改代码",
		"不要修改",
		"只读",
		"仅分析",
		"只分析",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func intentRequestsAnalysisReport(lowered string) bool {
	hasAnalysis := false
	for _, token := range []string{
		"analyze",
		"analyse",
		"analysis",
		"inspect",
		"review",
		"report",
		"summarize",
		"scan",
		"find issues",
		"找问题",
		"分析",
		"回审",
	} {
		if strings.Contains(lowered, token) {
			hasAnalysis = true
			break
		}
	}
	if !hasAnalysis {
		return false
	}
	for _, token := range []string{
		"write",
		"output",
		"save",
		"report to",
		"summarize in",
		"写入",
		"输出",
		"保存",
		"记录在",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func analyzeIntentWithLLM(input string, candidates []intentCandidate) (intentLLMDecision, bool) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		return intentLLMDecision{}, false
	}
	if cleanup != nil {
		defer cleanup()
	}
	llmCtx, llmCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer llmCancel()
	response, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt:     intentRouterSystemPrompt(),
		UserPrompt:       buildIntentRouterPrompt(input, candidates),
		StructuredOutput: true,
	})
	if err != nil || response.Fallback {
		return intentLLMDecision{}, false
	}
	return parseIntentLLMDecision(response.Text)
}

func bootstrapIntentLLMClient() (llm.Client, func(), error) {
	client, err := app.NewLLMClient()
	if err != nil {
		return nil, nil, err
	}
	return client, func() {}, nil
}

func intentRouterSystemPrompt() string {
	return "You route one user intent into one existing CLI command. Return only compact JSON: {\"action\":\"choose|clarify\",\"choice\":number,\"question\":\"\",\"confidence\":number}. Choose only from candidates. Clarify when intent is ambiguous or destructive."
}

func buildIntentRouterPrompt(input string, candidates []intentCandidate) string {
	var builder strings.Builder
	builder.WriteString("User intent:\n")
	builder.WriteString(strings.TrimSpace(input))
	if actionMap := intentCLIActionPromptSection(); actionMap != "" {
		builder.WriteString("\n\n")
		builder.WriteString(actionMap)
	}
	builder.WriteString("\n\nCandidates:\n")
	for index, candidate := range candidates {
		builder.WriteString(fmt.Sprintf("%d. %s | confidence=%d | command=avatars %s\n", index+1, candidate.Summary, candidate.Confidence, strings.Join(candidate.Command, " ")))
	}
	builder.WriteString("\nReturn JSON only.")
	return builder.String()
}

func parseIntentLLMDecision(text string) (intentLLMDecision, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return intentLLMDecision{}, false
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		trimmed = trimmed[start : end+1]
	}
	var decision intentLLMDecision
	if err := json.Unmarshal([]byte(trimmed), &decision); err != nil {
		return intentLLMDecision{}, false
	}
	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	return decision, decision.Action == "choose" || decision.Action == "clarify"
}

func taskIDFromIntentOrDefault(input string) string {
	taskID := taskIDFromIntent(input)
	if taskID != "" {
		return taskID
	}
	slug := strings.Trim(strings.ToLower(input), " ")
	var builder strings.Builder
	lastHyphen := false
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || r == ' ':
			if builder.Len() > 0 && !lastHyphen {
				builder.WriteByte('-')
				lastHyphen = true
			}
		}
		if builder.Len() >= 32 {
			break
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "intent-task"
	}
	return result
}

func taskIDFromIntent(input string) string {
	fields := strings.Fields(input)
	for index, field := range fields {
		trimmed := strings.Trim(field, "`'\".,;:()[]{}")
		lowered := strings.ToLower(trimmed)
		if (lowered == "--task" || lowered == "task") && index+1 < len(fields) {
			if lowered == "task" && !looksLikeExplicitTaskMarker(trimmed, fields[index+1]) {
				continue
			}
			return cleanIntentTaskID(fields[index+1])
		}
		if strings.HasPrefix(lowered, "task=") {
			return cleanIntentTaskID(strings.TrimPrefix(trimmed, "task="))
		}
		if strings.HasPrefix(lowered, "task:") {
			return cleanIntentTaskID(strings.TrimPrefix(trimmed, "task:"))
		}
	}
	return ""
}

func looksLikeExplicitTaskMarker(marker string, next string) bool {
	trimmedMarker := strings.TrimSpace(marker)
	if trimmedMarker == "--task" {
		return true
	}
	cleanNext := strings.Trim(next, "`'\".,;:()[]{}")
	return strings.Contains(cleanNext, "-") || strings.Contains(cleanNext, "_") || strings.HasPrefix(strings.ToLower(cleanNext), "task")
}

func cleanIntentTaskID(value string) string {
	trimmed := strings.Trim(value, "`'\".,;:()[]{}")
	trimmed = filepath.Base(trimmed)
	trimmed = strings.ToLower(trimmed)
	var builder strings.Builder
	lastHyphen := false
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_':
			if builder.Len() > 0 && !lastHyphen {
				builder.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}
