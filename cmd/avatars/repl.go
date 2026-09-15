package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	memstore "avatars/internal/memory"
	"avatars/internal/tasks"
)

const replUsageText = "usage: avatars repl [--task <task-id>] [--new-task] [--display compact|verbose] [--permission-mode plan|default|acceptEdits]\n\nThe REPL classifies each line and dispatches to a CLI command. The permission\nmode is auto-dispatched by intent: read-only requests use `plan`; mutating\ncoding and explicit replacements use `acceptEdits`. Named mode `default` is\nexperimental (interactive approval is not implemented) and is upgraded to\n`acceptEdits` on run/REPL dispatch. To force a specific mode for the\nunclassified fallback, use `/run --permission-mode <mode> ...`."
const defaultREPLTaskID = "repl-session"
const replHistoryLimit = 100

type replCommandOptions struct {
	TaskID         string
	ForceNewTask   bool
	Verbose        bool
	PermissionMode string
	ReplContext    string // formatted REPL conversation context, set per-turn
}

type replLastTurnArtifact struct {
	Input      string   `json:"input"`
	Kind       string   `json:"kind"`
	Command    []string `json:"command"`
	Status     string   `json:"status"`
	Output     string   `json:"output"`
	Artifacts  []string `json:"artifacts"`
	UpdatedUTC string   `json:"updated_utc"`
}

type replClarifyPending struct {
	OriginalInput string `json:"original_input"`
	Question      string `json:"question"`
	UpdatedUTC    string `json:"updated_utc"`
}

func replClarifyPendingPath(root string) string {
	return filepath.Join(root, ".avatars", "repl", "clarify_pending.json")
}

func saveREPLClarifyPending(root string, pending replClarifyPending) error {
	pending.OriginalInput = strings.TrimSpace(pending.OriginalInput)
	pending.Question = strings.TrimSpace(pending.Question)
	pending.UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	path := replClarifyPendingPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func loadREPLClarifyPending(root string) (replClarifyPending, bool, error) {
	data, err := os.ReadFile(replClarifyPendingPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return replClarifyPending{}, false, nil
		}
		return replClarifyPending{}, false, err
	}
	var pending replClarifyPending
	if err := json.Unmarshal(data, &pending); err != nil {
		return replClarifyPending{}, false, err
	}
	if strings.TrimSpace(pending.OriginalInput) == "" {
		return replClarifyPending{}, false, nil
	}
	return pending, true, nil
}

func clearREPLClarifyPending(root string) {
	_ = os.Remove(replClarifyPendingPath(root))
}

func mergeClarifyFollowUp(original, followUp string) string {
	o := strings.TrimSpace(original)
	f := strings.TrimSpace(followUp)
	if o == "" {
		return f
	}
	if f == "" {
		return o
	}
	return o + "\nFollow-up: " + f
}

func parseREPLCommandOptions(args []string) (replCommandOptions, error) {
	options := replCommandOptions{Verbose: true} // default to verbose for real-time feedback
	for len(args) > 0 {
		switch args[0] {
		case "--task":
			if len(args) < 2 {
				return replCommandOptions{}, errors.New(replUsageText)
			}
			options.TaskID = strings.TrimSpace(args[1])
			if options.TaskID == "" {
				return replCommandOptions{}, errors.New("repl --task requires a task id")
			}
			args = args[2:]
		case "--new-task":
			options.ForceNewTask = true
			args = args[1:]
		case "--display":
			if len(args) < 2 {
				return replCommandOptions{}, errors.New("repl --display requires compact or verbose")
			}
			display := strings.ToLower(strings.TrimSpace(args[1]))
			switch display {
			case "compact":
				options.Verbose = false
			case "verbose":
				options.Verbose = true
			default:
				return replCommandOptions{}, errors.New("repl --display requires compact or verbose")
			}
			args = args[2:]
		case "--verbose":
			options.Verbose = true
			args = args[1:]
		case "--permission-mode":
			if len(args) < 2 {
				return replCommandOptions{}, errors.New("repl --permission-mode requires plan, default, or acceptEdits")
			}
			// Permission mode is case-sensitive because "acceptEdits"
			// (with the internal capital E) is the canonical CLI
			// name and lowering would turn it into "acceptedits",
			// which silently does not match the switch below.
			mode := strings.TrimSpace(args[1])
			switch mode {
			case "plan", "default", "acceptEdits":
				options.PermissionMode = mode
			default:
				return replCommandOptions{}, errors.New("repl --permission-mode requires plan, default, or acceptEdits")
			}
			args = args[2:]
		default:
			return replCommandOptions{}, errors.New(replUsageText)
		}
	}
	if options.TaskID != "" && options.ForceNewTask {
		return replCommandOptions{}, errors.New("--task cannot be combined with --new-task")
	}
	return options, nil
}

func runREPL(args []string) error {
	options, err := parseREPLCommandOptions(args)
	if err != nil {
		return err
	}
	options = effectiveREPLCommandOptions(options)
	return runREPLWithIO(os.Stdin, os.Stdout, options)
}

func runREPLWithIO(input io.Reader, output io.Writer, options replCommandOptions) error {
	options = effectiveREPLCommandOptions(options)
	previousFileContext := getReplFileContext()
	defer func() {
		setReplFileContext(previousFileContext)
	}()
	historyPath := replHistoryPath()
	history := loadREPLHistory(historyPath)
	fmt.Fprintln(output, "Avatars REPL started. Type /help or /exit.")
	fmt.Fprintln(output, "Safety: bounded intent routing; guarded tool approvals stay active.")
	fmt.Fprintf(output, "History: %s\n", historyPath)
	if options.TaskID != "" {
		fmt.Fprintf(output, "Task context: %s\n", options.TaskID)
	} else if options.ForceNewTask {
		fmt.Fprintln(output, "Task context: fresh task per routed run")
	}
	fmt.Fprintf(output, "Display: %s\n", replDisplayMode(options))

	// C2: Load existing session context for continuity across restarts.
	if ctx := memstore.LoadReplContext(); len(ctx.Files) > 0 || len(ctx.Actions) > 0 {
		fmt.Fprintf(output, "Session: resumed (%d files, %d actions from previous session)\n", len(ctx.Files), len(ctx.Actions))
	}

	printREPLPrompt(output)

	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			printREPLPrompt(output)
			continue
		}
		executableLine, replayed, err := resolveREPLHistoryReference(line, history)
		if err != nil {
			fmt.Fprintf(output, "Error: %v\n", err)
			printREPLPrompt(output)
			continue
		}
		if replayed {
			fmt.Fprintf(output, "Replaying: %s\n", executableLine)
		}
		if handled, err := handleREPLVerboseCommand(executableLine, output, &options); err != nil {
			fmt.Fprintf(output, "Error: %v\n", err)
			printREPLPrompt(output)
			continue
		} else if handled {
			printREPLPrompt(output)
			continue
		}
		shouldPrintStatus := false
		if routedTask, err := dispatchREPLLine(executableLine, output, options, history); err != nil {
			fmt.Fprintf(output, "Error: %v\n", err)
		} else {
			shouldPrintStatus = routedTask
		}
		if shouldRecordREPLHistory(executableLine) {
			history = appendREPLHistory(historyPath, history, executableLine)
		}
		// Before dispatching the next REPL turn, format the recent
		// history as a context string so the avatar LLM can maintain
		// conversational continuity across runs.
		setReplContextForRun(formatREPLContext(history))
		if options.Verbose && shouldPrintStatus && shouldPrintREPLTurnStatus(executableLine) {
			printREPLTurnStatus(output, executableLine, options)
		}
		if isREPLExitCommand(executableLine) {
			return nil
		}
		printREPLPrompt(output)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func dispatchREPLLine(line string, output io.Writer, options replCommandOptions, history []string) (bool, error) {
	trimmed := strings.TrimSpace(line)
	switch {
	case isREPLExitCommand(trimmed):
		fmt.Fprintln(output, "Exiting Avatars REPL.")
		return false, nil
	case trimmed == "/help":
		printREPLHelp(output)
		return false, nil
	case trimmed == "/history":
		printREPLHistory(output, history)
		return false, nil
	case strings.HasPrefix(trimmed, "@"):
		path, suffix := splitPlanFileInput(strings.TrimSpace(strings.TrimPrefix(trimmed, "@")))
		if path == "" {
			return false, errors.New("usage: @<plan.md>")
		}
		return false, runREPLIntentFile(path, suffix, output, options)
	case strings.HasPrefix(trimmed, "/load "):
		path := strings.TrimSpace(strings.TrimPrefix(trimmed, "/load "))
		if path == "" {
			return false, errors.New("usage: /load <plan.md>")
		}
		return false, runREPLIntentFile(path, "", output, options)
	case strings.HasPrefix(trimmed, "/"):
		return true, runREPLCommandLine(strings.TrimPrefix(trimmed, "/"), output, options)
	case strings.HasPrefix(trimmed, "avatars "):
		return true, runREPLCommandLine(strings.TrimPrefix(trimmed, "avatars "), output, options)
	default:
		return runREPLNaturalLanguage(trimmed, output, options)
	}
}

func replIntentArgsForFile(path string, options replCommandOptions) []string {
	options = effectiveREPLCommandOptions(options)
	if options.TaskID == "" {
		return []string{"intent", "--from-file", path}
	}
	return []string{"intent", "--from-file", path}
}

func replIntentArgsForLine(line string, options replCommandOptions) []string {
	options = effectiveREPLCommandOptions(options)
	input := strings.TrimSpace(line)
	if options.TaskID != "" && taskIDFromIntent(input) == "" {
		input = strings.TrimSpace(input + " task=" + options.TaskID)
	}
	args := []string{"intent", input}
	return args
}

// replRunArgsForLine builds the `run` command the REPL dispatches
// when the natural-language classifier declines to take over (i.e.
// the route is the generic plan-mode fallback). The permission mode
// is dispatched by intent: a mutating coding request picks
// `--permission-mode default` (or `acceptEdits` for explicit string
// replacements) so guarded approval can engage, while a read-only /
// analysis request keeps `plan` so the run is read-only by default.
// An explicit `--permission-mode` on the REPL command line (or set
// via options.PermissionMode by the caller) wins over the
// intent-derived mode, so users can force a specific mode with
// `/run --permission-mode <mode> ...` style flows.
func replRunArgsForLine(line string, options replCommandOptions) []string {
	options = effectiveREPLCommandOptions(options)
	input := strings.TrimSpace(line)
	mode := strings.TrimSpace(options.PermissionMode)
	if mode == "" {
		mode = naturalLanguagePermissionMode(strings.ToLower(input))
	}
	if options.ForceNewTask || options.TaskID == "" {
		return []string{"run", "--new-task", "--permission-mode", mode, input}
	}
	return []string{"run", "--task", options.TaskID, "--permission-mode", mode, input}
}

func runREPLNaturalLanguage(line string, output io.Writer, options replCommandOptions) (bool, error) {
	// S6.4: If a clarify is pending, merge the follow-up into the original intent.
	if pending, ok, err := loadREPLClarifyPending("."); err == nil && ok {
		clearREPLClarifyPending(".")
		line = mergeClarifyFollowUp(pending.OriginalInput, line)
	}
	route, handled, err := resolveNaturalLanguageQuestion(line, options, naturalLanguageContextREPL)
	if err != nil {
		return false, err
	}
	if !handled {
		return true, run(replRunArgsForLine(line, options))
	}
	switch route.Kind {
	case naturalLanguageRouteDirectAnswer:
		fmt.Fprintln(output, route.Answer)
		return false, nil
	case naturalLanguageRouteSafeRun:
		fmt.Fprintln(output, executionCueTaskIntro(line))
		// REPL natural language: user already expressed intent to write.
		// Upgrade --permission-mode default → acceptEdits to avoid
		// the unimplemented approval prompt blocking execution.
		route.Command = upgradeREPLPermissionMode(route.Command)
		if isBootstrapRouteCommand(route.Command) {
			fmt.Fprintln(output, executionCueBootstrap(line))
			captured, err := runREPLCommandCompactly(route.Command)
			if strings.TrimSpace(captured) != "" {
				fmt.Fprintln(output, strings.TrimSpace(captured))
			}
			_ = saveREPLLastTurn(".", replLastTurnArtifact{
				Input:     line,
				Kind:      "bootstrap",
				Command:   route.Command,
				Status:    replLastTurnStatus(err),
				Output:    captured,
				Artifacts: replArtifactsFromOutput(captured),
			})
			return false, err
		} else if isScriptRouteCommand(route.Command) {
			fmt.Fprintln(output, executionCueScript(line))
		} else if isEditRouteCommand(route.Command) {
			fmt.Fprintln(output, executionCueEdit(line))
		} else if isDirectActionRouteCommand(route.Command) {
			fmt.Fprintln(output, executionCueDirect(line))
		} else if mode := routeCommandPermissionMode(route.Command); isMutatingRunPermissionMode(mode) {
			fmt.Fprintln(output, executionCueForMutatingRun(mode, line))
		} else {
			fmt.Fprintln(output, executionCuePlan(line))
		}
		if options.Verbose {
			runErr := run(route.Command)
			_ = saveREPLLastTurn(".", replLastTurnArtifact{
				Input:     line,
				Kind:      replLastTurnKind(route.Command),
				Command:   route.Command,
				Status:    replLastTurnStatus(runErr),
				Artifacts: replArtifactsFromCommand(route.Command),
			})
			if runErr != nil {
				return true, runErr
			}
			if answer, ok, err := summarizeLatestRunAnswer("."); err == nil && ok && strings.TrimSpace(answer) != "" {
				fmt.Fprintln(output, answer)
			}
			if location, err := summarizeResultLocation("."); err == nil && strings.TrimSpace(location) != "" {
				fmt.Fprintln(output, location)
			}
			return true, nil
		}
		captured, err := runREPLCommandCompactly(route.Command)
		if err != nil {
			if strings.TrimSpace(captured) != "" {
				fmt.Fprintln(output, strings.TrimSpace(captured))
			}
			_ = saveREPLLastTurn(".", replLastTurnArtifact{
				Input:     line,
				Kind:      replLastTurnKind(route.Command),
				Command:   route.Command,
				Status:    replLastTurnStatus(err),
				Output:    captured,
				Artifacts: replArtifactsFromOutput(captured),
			})
			if approval := summarizeLatestApprovalBlock("."); looksLikeApprovalRequiredError(err) && approval != "" {
				fmt.Fprintln(output, approval)
				return false, nil
			}
			return false, err
		}
		if isDirectActionRouteCommand(route.Command) {
			if strings.TrimSpace(captured) != "" {
				fmt.Fprintln(output, strings.TrimSpace(captured))
			}
			_ = saveREPLLastTurn(".", replLastTurnArtifact{
				Input:     line,
				Kind:      replLastTurnKind(route.Command),
				Command:   route.Command,
				Status:    "completed",
				Output:    captured,
				Artifacts: replArtifactsFromOutput(captured),
			})
			return false, nil
		}
		if answer, ok, err := summarizeLatestRunAnswer("."); err == nil && ok && strings.TrimSpace(answer) != "" {
			fmt.Fprintln(output, answer)
		}
		if location, err := summarizeResultLocation("."); err == nil && strings.TrimSpace(location) != "" {
			fmt.Fprintln(output, location)
		}
		_ = saveREPLLastTurn(".", replLastTurnArtifact{
			Input:     line,
			Kind:      replLastTurnKind(route.Command),
			Command:   route.Command,
			Status:    "completed",
			Output:    captured,
			Artifacts: replArtifactsFromOutput(captured),
		})
		return false, nil
	case naturalLanguageRouteClarify:
		_ = saveREPLClarifyPending(".", replClarifyPending{
			OriginalInput: line,
			Question:      route.Question,
		})
		fmt.Fprintf(output, "I'm not sure what to do next.\n%s\n(Reply with more detail — I'll continue from this question.)\n", route.Question)
		return false, nil
	default:
		return true, run(replRunArgsForLine(line, options))
	}
}

func runREPLCommandCompactly(command []string) (string, error) {
	return withSuppressedRunProgressAndCapturedStdout(func() error {
		return run(command)
	})
}

func replLastTurnPath(root string) string {
	return filepath.Join(root, ".avatars", "repl", "last_turn.json")
}

// upgradeREPLPermissionMode replaces --permission-mode default with acceptEdits
// in REPL natural language commands. The user already communicated intent to write;
// requiring a second approval confirmation is redundant and currently unimplemented.
func upgradeREPLPermissionMode(command []string) []string {
	return upgradeRunCommandPermissionMode(command)
}

func saveREPLLastTurn(root string, artifact replLastTurnArtifact) error {
	artifact.Input = strings.TrimSpace(artifact.Input)
	artifact.Kind = strings.TrimSpace(artifact.Kind)
	artifact.Status = strings.TrimSpace(artifact.Status)
	artifact.Output = strings.TrimSpace(artifact.Output)
	artifact.UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	path := replLastTurnPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	// Accumulate session context for multi-turn conversational continuity.
	// Uses the SQLite-backed REPL context store (.avatars/repl/context.db).
	// Inspired by Claude Code's in-memory message accumulation.
	memstore.UpdateReplContext(artifact.Input, artifact.Kind, artifact.Artifacts)
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func loadREPLLastTurn(root string) (replLastTurnArtifact, bool, error) {
	content, err := os.ReadFile(replLastTurnPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return replLastTurnArtifact{}, false, nil
		}
		return replLastTurnArtifact{}, false, err
	}
	var artifact replLastTurnArtifact
	if err := json.Unmarshal(content, &artifact); err != nil {
		return replLastTurnArtifact{}, false, err
	}
	if strings.TrimSpace(artifact.Input) == "" && len(artifact.Command) == 0 && strings.TrimSpace(artifact.Output) == "" {
		return replLastTurnArtifact{}, false, nil
	}
	return artifact, true, nil
}

func summarizeREPLLastTurn(root string) (string, bool, error) {
	artifact, ok, err := loadREPLLastTurn(root)
	if err != nil || !ok {
		return "", ok, err
	}
	lines := []string{"Last action:"}
	if artifact.Kind != "" {
		lines = append(lines, "- kind: "+artifact.Kind)
	}
	if artifact.Status != "" {
		lines = append(lines, "- status: "+artifact.Status)
	}
	if artifact.Input != "" {
		lines = append(lines, "- input: "+conciseNLContextLine(artifact.Input, 220))
	}
	if len(artifact.Command) > 0 {
		lines = append(lines, "- command: avatars "+strings.Join(artifact.Command, " "))
	}
	for _, path := range artifact.Artifacts {
		if strings.TrimSpace(path) != "" {
			lines = append(lines, "- artifact: "+filepath.ToSlash(path))
		}
	}
	if artifact.Output != "" {
		lines = append(lines, "- output: "+conciseNLContextLine(artifact.Output, 500))
	}
	return strings.Join(lines, "\n"), true, nil
}

// replArtifactsFromCommand extracts file paths from command args for session
// tracking in verbose mode (where output isn't captured). Without this, files
// created in verbose mode are invisible to the session context (Bug: "0 files").
func replArtifactsFromCommand(command []string) []string {
	var files []string
	for i, arg := range command {
		if arg == "--apply" || arg == "--llm" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(arg))
		if ext != "" && ext != ".exe" && ext != ".dll" && ext != ".so" {
			if i > 0 {
				prev := command[i-1]
				if prev == "--apply" || prev == "--llm" {
					files = append(files, arg)
				}
			}
		}
	}
	if len(files) == 0 {
		for _, arg := range command {
			if strings.Contains(arg, ".") && !strings.HasPrefix(arg, "-") {
				ext := strings.ToLower(filepath.Ext(arg))
				if ext != "" && ext != ".exe" && ext != ".dll" && ext != ".so" {
					files = append(files, arg)
				}
			}
		}
	}
	return files
}

func replArtifactsFromOutput(captured string) []string {
	artifacts := []string{}
	for _, line := range strings.Split(captured, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if value != "" && strings.Contains(value, ".") {
				artifacts = append(artifacts, filepath.ToSlash(value))
			}
			continue
		}
		for _, prefix := range []string{"Path:", "Full answer:", "analysis report:", "bootstrap summary:"} {
			if strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(prefix)) {
				value := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
				if value != "" {
					artifacts = append(artifacts, filepath.ToSlash(value))
				}
			}
		}
	}
	return dedupeStringSlice(artifacts)
}

func dedupeStringSlice(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func replLastTurnKind(command []string) string {
	if len(command) == 0 {
		return "unknown"
	}
	switch command[0] {
	case "bootstrap":
		return "bootstrap"
	case "script":
		return "script"
	case "patch":
		return "patch"
	case "run":
		return "task"
	default:
		return command[0]
	}
}

func replLastTurnStatus(err error) string {
	if err == nil {
		return "completed"
	}
	if looksLikeApprovalRequiredError(err) {
		return "awaiting_approval"
	}
	return "failed"
}

func isBootstrapRouteCommand(command []string) bool {
	return len(command) > 0 && command[0] == "bootstrap"
}

func isScriptRouteCommand(command []string) bool {
	return len(command) > 0 && command[0] == "script"
}

func isEditRouteCommand(command []string) bool {
	return len(command) > 0 && command[0] == "edit"
}

func isDirectActionRouteCommand(command []string) bool {
	return len(command) > 0 && command[0] != "run"
}

func routeCommandPermissionMode(command []string) string {
	for index := 0; index < len(command)-1; index++ {
		if command[index] == "--permission-mode" {
			return strings.TrimSpace(command[index+1])
		}
	}
	return ""
}

// isMutatingRunPermissionMode is true for run modes that write files.
// F97: acceptEdits/dontAsk/bypass must not print the read-only plan cue.
func isMutatingRunPermissionMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "default", "acceptEdits", "dontAsk", "bypassPermissions":
		return true
	default:
		return false
	}
}

func looksLikeApprovalRequiredError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "approval required")
}

func summarizeLatestApprovalBlock(root string) string {
	workspace, ok, err := latestTaskWorkspace(root)
	if err != nil || !ok {
		return ""
	}
	store, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		return ""
	}
	defer func() {
		_ = store.Close()
	}()
	snapshot, err := store.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return ""
	}
	pending := memstore.PendingToolApprovalEvaluations(snapshot.EvaluationRecords)
	if len(pending) == 0 {
		return ""
	}
	lines := []string{
		"Approval required:",
		"- task: " + workspace.ID,
		"- status: awaiting_approval",
	}
	commands := memstore.SuggestedTaskCommands(workspace.ID, snapshot)
	if len(commands) > 0 {
		lines = append(lines, "- next commands:")
		limit := 4
		if len(commands) < limit {
			limit = len(commands)
		}
		for index := 0; index < limit; index++ {
			lines = append(lines, "  - "+commands[index])
		}
	}
	return strings.Join(lines, "\n")
}

func withSuppressedRunProgressAndCapturedStdout(fn func() error) (string, error) {
	return withCapturedStdout(func() error {
		return withSuppressedRunProgress(fn)
	})
}

func withSuppressedRunProgress(fn func() error) error {
	previous, hadPrevious := os.LookupEnv("AVATARS_PROGRESS")
	// F72 (cross-lang): compact Intent/REPL must not clobber an explicit
	// operator progress preference (json/text/on). Also keep progress when
	// stderr is redirected to a file/pipe — otherwise process logs look empty.
	if shouldKeepRunProgress(previous, hadPrevious) {
		if !hadPrevious && !stderrLooksInteractive() {
			_ = os.Setenv("AVATARS_PROGRESS", "text")
			defer func() { _ = os.Unsetenv("AVATARS_PROGRESS") }()
		}
		return fn()
	}
	if err := os.Setenv("AVATARS_PROGRESS", "0"); err != nil {
		return err
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("AVATARS_PROGRESS", previous)
			return
		}
		_ = os.Unsetenv("AVATARS_PROGRESS")
	}()
	return fn()
}

// shouldKeepRunProgress reports whether compact wrappers must leave progress on.
func shouldKeepRunProgress(previous string, hadPrevious bool) bool {
	if hadPrevious {
		switch strings.ToLower(strings.TrimSpace(previous)) {
		case "0", "off", "false", "quiet":
			return false
		default:
			// json / text / 1 / on / true / anything explicit non-off
			return strings.TrimSpace(previous) != ""
		}
	}
	return !stderrLooksInteractive()
}

func withCapturedStdout(fn func() error) (string, error) {
	if fn == nil {
		return "", nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	previous := os.Stdout
	os.Stdout = writer
	outputCh := make(chan string, 1)
	copyErrCh := make(chan error, 1)
	go func() {
		var buffer bytes.Buffer
		_, copyErr := io.Copy(&buffer, reader)
		outputCh <- buffer.String()
		copyErrCh <- copyErr
	}()
	runErr := fn()
	os.Stdout = previous
	_ = writer.Close()
	captured := <-outputCh
	copyErr := <-copyErrCh
	_ = reader.Close()
	if runErr != nil {
		return captured, runErr
	}
	if copyErr != nil {
		return captured, copyErr
	}
	return captured, nil
}

func replHistoryPath() string {
	return filepath.Join(".avatars", "repl", "history.txt")
}

func loadREPLHistory(path string) []string {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	history := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		history = append(history, trimmed)
	}
	if len(history) > replHistoryLimit {
		history = history[len(history)-replHistoryLimit:]
	}
	return history
}

func appendREPLHistory(path string, history []string, line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return history
	}
	if len(history) > 0 && history[len(history)-1] == trimmed {
		return history
	}
	history = append(history, trimmed)
	if len(history) > replHistoryLimit {
		history = history[len(history)-replHistoryLimit:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return history
	}
	content := strings.Join(history, "\n")
	if content != "" {
		content += "\n"
	}
	_ = os.WriteFile(path, []byte(content), 0o644)
	return history
}

func shouldRecordREPLHistory(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || isREPLExitCommand(trimmed) || trimmed == "/help" || trimmed == "/history" {
		return false
	}
	if trimmed == "!!" || strings.HasPrefix(trimmed, "!") {
		return false
	}
	return true
}

func resolveREPLHistoryReference(line string, history []string) (string, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "!!" {
		if len(history) == 0 {
			return "", false, errors.New("history is empty")
		}
		return history[len(history)-1], true, nil
	}
	if strings.HasPrefix(trimmed, "!") && len(trimmed) > 1 {
		index, err := strconv.Atoi(strings.TrimPrefix(trimmed, "!"))
		if err != nil || index < 1 || index > len(history) {
			return "", false, fmt.Errorf("history item %q not found", trimmed)
		}
		return history[index-1], true, nil
	}
	return trimmed, false, nil
}

func printREPLHistory(output io.Writer, history []string) {
	if len(history) == 0 {
		fmt.Fprintln(output, "History: empty")
		return
	}
	start := 0
	if len(history) > 20 {
		start = len(history) - 20
	}
	fmt.Fprintln(output, "History:")
	for index := start; index < len(history); index++ {
		fmt.Fprintf(output, "  !%d %s\n", index+1, history[index])
	}
	fmt.Fprintln(output, "Replay: !! or !<number>")
}

func runREPLIntentFile(path string, suffix string, output io.Writer, options replCommandOptions) error {
	options = effectiveREPLCommandOptions(options)
	content, resolved, err := readIntentInputFileWithResolve(path)
	if err != nil {
		return err
	}
	setReplFileContext(&fileContextState{Path: resolved, Content: content})
	// Clear file context after this turn so the loaded file does not
	// influence routing of subsequent unrelated REPL turns.
	defer func() { setReplFileContext(nil) }()
	fmt.Fprintf(output, "Intent input file loaded: %s (%d chars)\n", resolved, len(content))
	// When @file is loaded with a suffix, include a content summary
	// so the intent classifier can detect script/task patterns inside
	// the file that the user's suffix alone would miss. Without this,
	// "按照文档描述执行任务" routes to plan-mode even when the file
	// specifies "写一个python脚本".
	// classifyFromAttachedFile handles file-content-based routing
	// directly — no need to prepend file content to the input.
	// Keeping the suffix clean ensures the edit/script instruction
	// is the user's actual request, not a 33KB dump.
	input := strings.TrimSpace(suffix)
	if input == "" {
		input = attachedFileDefaultInput(content)
	}
	// BUG-4.3: When a new file is attached with @file, force a new task to avoid
	// context contamination from previous tasks. This ensures each @file attachment
	// starts with fresh context.
	if options.TaskID != "" {
		options.ForceNewTask = true
	}
	routedTask, err := runREPLNaturalLanguage(input, output, options)
	if err != nil {
		return err
	}
	if routedTask && shouldPrintREPLTurnStatus(input) {
		if !options.Verbose {
			return nil
		}
		printREPLTurnStatus(output, input, options)
	}
	return nil
}

func handleREPLVerboseCommand(line string, output io.Writer, options *replCommandOptions) (bool, error) {
	trimmed := strings.TrimSpace(line)
	lowered := strings.ToLower(trimmed)
	if lowered != "/verbose" && !strings.HasPrefix(lowered, "/verbose ") {
		return false, nil
	}
	fields := strings.Fields(lowered)
	if len(fields) == 1 {
		fmt.Fprintf(output, "Display: %s\n", replDisplayMode(*options))
		return true, nil
	}
	if len(fields) != 2 {
		return true, errors.New("usage: /verbose on|off")
	}
	switch fields[1] {
	case "on", "true", "1", "verbose":
		options.Verbose = true
		fmt.Fprintln(output, "Display: verbose")
	case "off", "false", "0", "compact":
		options.Verbose = false
		fmt.Fprintln(output, "Display: compact")
	default:
		return true, errors.New("usage: /verbose on|off")
	}
	return true, nil
}

func replDisplayMode(options replCommandOptions) string {
	if options.Verbose {
		return "verbose"
	}
	return "compact"
}

func replIntentInputWithTaskContext(input string, options replCommandOptions) string {
	options = effectiveREPLCommandOptions(options)
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || options.TaskID == "" || taskIDFromIntent(trimmed) != "" {
		return trimmed
	}
	return strings.TrimSpace(trimmed + "\n\ntask=" + options.TaskID)
}

func runREPLCommandLine(commandLine string, output io.Writer, options replCommandOptions) error {
	args := strings.Fields(strings.TrimSpace(commandLine))
	if len(args) == 0 {
		return errors.New("empty REPL command")
	}
	if args[0] == "avatars" {
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("empty REPL command")
	}
	if args[0] == "intent" {
		parsed, err := parseIntentCommandOptions(args[1:])
		if err != nil {
			return err
		}
		if parsed.Choose > 0 || parsed.Confirm {
			return runIntent(args[1:])
		}
		if parsed.InputFile != "" {
			return runREPLIntentFile(parsed.InputFile, "", output, options)
		}
	}
	return run(args)
}

func splitPlanFileInput(input string) (string, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", ""
	}
	path := fields[0]
	remainder := strings.TrimSpace(strings.TrimPrefix(trimmed, path))
	return path, remainder
}

func shouldPrintREPLTurnStatus(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || isREPLExitCommand(trimmed) || trimmed == "/help" {
		return false
	}
	if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "/load ") && !strings.HasPrefix(trimmed, "/intent") && !strings.HasPrefix(trimmed, "/run") && !strings.HasPrefix(trimmed, "/verify") && !strings.HasPrefix(trimmed, "/tasks") && !strings.HasPrefix(trimmed, "/memory") && !strings.HasPrefix(trimmed, "/governance") && !strings.HasPrefix(trimmed, "/llm") && !strings.HasPrefix(trimmed, "/serve") && !strings.HasPrefix(trimmed, "/shell") && !strings.HasPrefix(trimmed, "/write") && !strings.HasPrefix(trimmed, "/patch") && !strings.HasPrefix(trimmed, "/git") && !strings.HasPrefix(trimmed, "/mcp") && !strings.HasPrefix(trimmed, "/skills") {
		return false
	}
	return true
}

func printREPLTurnStatus(output io.Writer, line string, options replCommandOptions) {
	taskID := replStatusTaskID(line, options)
	if taskID == "" {
		fmt.Fprintln(output, "REPL status: fresh task mode; use task output for the created task id.")
		return
	}
	workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load(taskID)
	if err != nil {
		fmt.Fprintf(output, "REPL status: task=%s unavailable (%v)\n", taskID, err)
		fmt.Fprintln(output, "REPL status: no workspace yet; routed question handled without task state.")
		return
	}
	status := taskWorkspaceStatus(workspace)
	fmt.Fprintf(output, "REPL status: task=%s status=%s runs=%d\n", workspace.ID, status, workspace.RunCount)
	if transcript := strings.TrimSpace(workspace.LatestTranscriptPath()); transcript != "" {
		fmt.Fprintf(output, "REPL transcript: %s\n", transcript)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		return
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return
	}
	if snapshot.Verification != nil {
		fmt.Fprintf(output, "REPL verifier: %s", displaySkillField(snapshot.Verification.Verdict))
		if summary := strings.TrimSpace(snapshot.Verification.Summary); summary != "" {
			fmt.Fprintf(output, " | %s", summary)
		}
		fmt.Fprintln(output)
		if reportPath := strings.TrimSpace(snapshot.Verification.ReportPath); reportPath != "" {
			fmt.Fprintf(output, "REPL verifier report: %s\n", reportPath)
		}
	} else {
		fmt.Fprintln(output, "REPL verifier: none")
	}
	commands := memstore.SuggestedTaskCommands(workspace.ID, snapshot)
	if len(commands) == 0 {
		fmt.Fprintln(output, "REPL suggested: none")
		return
	}
	limit := 3
	if len(commands) < limit {
		limit = len(commands)
	}
	for index := 0; index < limit; index++ {
		fmt.Fprintf(output, "REPL suggested: %s\n", commands[index])
	}
}

func replStatusTaskID(line string, options replCommandOptions) string {
	options = effectiveREPLCommandOptions(options)
	if taskID := taskIDFromIntent(line); taskID != "" {
		return taskID
	}
	return strings.TrimSpace(options.TaskID)
}

func effectiveREPLCommandOptions(options replCommandOptions) replCommandOptions {
	if options.TaskID == "" && !options.ForceNewTask {
		options.TaskID = defaultREPLTaskID
	}
	return options
}

func isREPLExitCommand(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "/exit" || trimmed == "/quit"
}

func printREPLPrompt(output io.Writer) {
	fmt.Fprint(output, "avatars> ")
}

func printREPLHelp(output io.Writer) {
	fmt.Fprintln(output, "Commands:")
	fmt.Fprintln(output, "  /help          Show this help.")
	fmt.Fprintln(output, "  /history       Show recent REPL commands.")
	fmt.Fprintln(output, "  /load <path>   Load a markdown plan as intent input.")
	fmt.Fprintln(output, "  /verbose on    Show workflow progress and task status.")
	fmt.Fprintln(output, "  /verbose off   Show compact answers only.")
	fmt.Fprintln(output, "  /exit          Exit the REPL.")
	fmt.Fprintln(output, "  /quit          Exit the REPL.")
	fmt.Fprintln(output, "  !! or !<n>     Replay a recent history item.")
	fmt.Fprintf(output, "Normal input is routed through safe plan-mode task runs. Default task: %s.\n", defaultREPLTaskID)
	fmt.Fprintln(output, "Safety: ambiguous or risky routed commands still require existing confirmation or approval flow.")
}

const replContextMaxTurns = 10
const replContextMaxLength = 4000

// formatREPLContext builds a compact conversation context string from the
// REPL history. It takes the most recent N turns (up to
// replContextMaxTurns) and formats them as "Turn <n>: <input>" lines.
// The total output is capped at replContextMaxLength bytes with a
// truncation marker.
func formatREPLContext(history []string) string {
	if len(history) == 0 {
		return ""
	}
	start := len(history) - replContextMaxTurns
	if start < 0 {
		start = 0
	}
	recent := history[start:]
	var buf strings.Builder
	for i, line := range recent {
		turnNum := start + i + 1
		entry := fmt.Sprintf("Turn %d: %s", turnNum, truncateLine(line, 500))
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		if buf.Len()+len(entry) > replContextMaxLength {
			buf.WriteString("\n[truncated: repl context exceeded ")
			buf.WriteString(fmt.Sprintf("%d", replContextMaxLength))
			buf.WriteString(" chars]")
			break
		}
		buf.WriteString(entry)
	}
	return buf.String()
}

func truncateLine(line string, maxLen int) string {
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen] + "…"
}
