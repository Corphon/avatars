package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/tasks"
)

func TestRouteIntent_PrefersSkillGenerateOverSkillStatus(t *testing.T) {
	candidates := routeIntent("请帮我生成一个 用于分析该项目的 skills")
	if len(candidates) == 0 {
		t.Fatal("expected candidates for skill generation intent")
	}
	if candidates[0].Command[0] != "skills" || len(candidates[0].Command) < 2 || candidates[0].Command[1] != "generate" {
		t.Fatalf("expected skills generate first candidate, got %#v", candidates[0].Command)
	}
}

func TestRouteIntent_ReportFilenameSmokeDoesNotMatchSmokeCommand(t *testing.T) {
	candidates := routeIntent("分析项目，找问题，写入 smoke_compact.md")
	if len(candidates) == 0 {
		t.Fatal("expected report intent candidates")
	}
	for _, candidate := range candidates {
		if len(candidate.Command) >= 2 && candidate.Command[0] == "smoke" && candidate.Command[1] == "repl-routing" {
			t.Fatalf("expected report filename smoke_compact.md not to route to smoke command, got %+v", candidate.Command)
		}
	}
}

func TestIntentTaskSpecAttachedFileRoutesToScriptGeneration(t *testing.T) {
	tempDir := t.TempDir()
	docPath := filepath.Join(tempDir, "task.md")
	docContent := "# Task\n\n用 python 写一个 doc_task.py 脚本。\n脚本打印 countdown: 3, 2, 1, done。\n不要输出 Hello 模板。\n"
	if err := os.WriteFile(docPath, []byte(docContent), 0o644); err != nil {
		t.Fatalf("write task spec failed: %v", err)
	}
	originalGenerator := scriptContentGenerator
	originalAnalyzer := scriptIntentAnalyzer
	scriptContentGenerator = func(path string, description string) (string, bool, error) {
		if path != "doc_task.py" {
			t.Fatalf("expected doc_task.py path, got %q", path)
		}
		if !strings.Contains(description, "countdown") {
			t.Fatalf("expected attached task spec content in generator, got %q", description)
		}
		return "#!/usr/bin/env python3\nprint('countdown: 3, 2, 1, done')\n", true, nil
	}
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		if !strings.Contains(input, "doc_task.py") || !strings.Contains(input, "countdown") {
			t.Fatalf("expected attached task spec content in analyzer, got %q", input)
		}
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "doc_task.py", Topic: "countdown script", Confidence: 94}, true
	}
	defer func() {
		scriptContentGenerator = originalGenerator
		scriptIntentAnalyzer = originalAnalyzer
	}()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	output := captureRunOutput(t, []string{"intent", "@" + docPath})
	if strings.Contains(output, "File context:\n-") {
		t.Fatalf("expected task spec doc to route to execution, not summary, got %s", output)
	}
	if !strings.Contains(output, "Script mode: apply") || !strings.Contains(output, "Generator: llm") {
		t.Fatalf("expected intent task spec doc to route to llm-backed script generation, got %s", output)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "doc_task.py")); err != nil {
		t.Fatalf("expected doc_task.py to be written: %v", err)
	}
}

func TestIntentPreviousResultWriteRequestReportsMissingLatestResult(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	answer, ok, err := answerPreviousResultWriteRequest("把刚才的分析结果输出在 outcome.md", replCommandOptions{}, naturalLanguageContextIntent)
	if err != nil {
		t.Fatalf("expected missing-result reuse to stay non-error, got %v", err)
	}
	if !ok {
		t.Fatal("expected missing-result reuse to be handled")
	}
	if !strings.Contains(answer, "no previous analysis report found") {
		t.Fatalf("expected missing result guidance, got %s", answer)
	}
}

func TestIntentPreviousResultWriteRequestReusesLatestReport(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	reportContent := "# Analysis Report\n\n## Proven Findings\n- prior result\n"
	if err := os.WriteFile("analysis.md", []byte(reportContent), 0o644); err != nil {
		t.Fatalf("write source report failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "analysis-task", Title: "analysis-task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("Analysis report: analysis.md\n"), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "analysis report generated"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}

	decision := classifyNaturalLanguageQuestionWithoutLLM("把刚才的分析结果输出在 outcome.md", replCommandOptions{}, naturalLanguageContextIntent)
	if decision.Kind != naturalLanguageDecisionMemory {
		t.Fatalf("expected previous result reuse in intent mode, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Previous result reused:") || !strings.Contains(decision.Answer, "outcome.md") {
		t.Fatalf("expected reuse answer with target, got %s", decision.Answer)
	}
	copied, err := os.ReadFile("outcome.md")
	if err != nil {
		t.Fatalf("expected outcome.md to be written: %v", err)
	}
	if string(copied) != reportContent {
		t.Fatalf("expected copied report content, got %s", string(copied))
	}
}

func TestIntentPreviousResultWriteRequestReusesLatestReportBeforeLLM(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	reportContent := "# Analysis Report\n\n## Proven Findings\n- prior result\n"
	if err := os.WriteFile("analysis.md", []byte(reportContent), 0o644); err != nil {
		t.Fatalf("write source report failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "analysis-task-llm", Title: "analysis-task-llm"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("Analysis report: analysis.md\n"), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "analysis report generated"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}

	start := time.Now()
	decision := classifyNaturalLanguageQuestion("把刚才的分析结果输出在 outcome.md", replCommandOptions{}, naturalLanguageContextIntent)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("expected previous result reuse to avoid LLM wait")
	}
	if decision.Kind != naturalLanguageDecisionMemory {
		t.Fatalf("expected previous result reuse before LLM, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Previous result reused:") || !strings.Contains(decision.Answer, "outcome.md") {
		t.Fatalf("expected reuse answer with target, got %s", decision.Answer)
	}
}

func TestIntentFreshAnalysisWriteStillRoutesToSafeRunWhenReportExists(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("analysis.md", []byte("# Analysis Report\n"), 0o644); err != nil {
		t.Fatalf("write source report failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "analysis-task", Title: "analysis-task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("Analysis report: analysis.md\n"), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "analysis report generated"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}

	decision := classifyNaturalLanguageQuestionWithoutLLM("分析项目，找问题，写入 outcome.md", replCommandOptions{}, naturalLanguageContextIntent)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected fresh analysis write to stay a safe run, got %+v", decision)
	}
	if !strings.Contains(strings.Join(decision.Command, " "), "outcome.md") {
		t.Fatalf("expected output path to stay in command, got %+v", decision.Command)
	}
}

func TestIntentRoute_VerifyAutoExecutes(t *testing.T) {
	candidates := routeIntent("verify current project")
	if len(candidates) != 1 {
		t.Fatalf("expected one verifier candidate, got %+v", candidates)
	}
	if strings.Join(candidates[0].Command, " ") != "verify" || !candidates[0].AutoSafe {
		t.Fatalf("expected verifier route, got %+v", candidates[0])
	}
}

func TestIntentRoute_AmbiguousPrintsChoices(t *testing.T) {
	output := captureRunOutput(t, []string{"intent", "inspect governance task=demo-task"})
	if !strings.Contains(output, "Intent needs choice: inspect governance task=demo-task") {
		t.Fatalf("expected ambiguous intent choices, got %s", output)
	}
	if !strings.Contains(output, "avatars governance status --task demo-task") || !strings.Contains(output, "avatars governance status") {
		t.Fatalf("expected governance candidates, got %s", output)
	}
}

func TestIntentRoute_ChoiceRequiresConfirmForAmbiguousCandidate(t *testing.T) {
	err := run([]string{"intent", "--choose", "1", "inspect governance task=demo-task"})
	if err == nil || !strings.Contains(err.Error(), "requires --confirm") {
		t.Fatalf("expected --confirm requirement, got %v", err)
	}
}

func TestIntentRoute_HarmlessQuestionFallsBackToSafeRun(t *testing.T) {
	route, handled, err := resolveNaturalLanguageQuestion("为什么会出现 Survey depth is limited to high-signal file summaries in this run.", replCommandOptions{}, naturalLanguageContextIntent)
	if err != nil {
		t.Fatalf("expected harmless question fallback, got %v", err)
	}
	if !handled {
		t.Fatal("expected harmless question to be handled")
	}
	if route.Kind != naturalLanguageRouteSafeRun {
		t.Fatalf("expected safe run route, got %+v", route)
	}
	if len(route.Command) == 0 || route.Command[0] != "run" || !strings.Contains(strings.Join(route.Command, " "), "--permission-mode plan") {
		t.Fatalf("expected safe plan-mode fallback command, got %+v", route.Command)
	}
}

func TestIntentRoute_DestructiveQuestionStaysAmbiguous(t *testing.T) {
	err := run([]string{"intent", "delete all files"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous or risky") {
		t.Fatalf("expected destructive intent to stay guarded, got %v", err)
	}
}

func TestIntentRoute_VerifyDoesNotStealImplementationWork(t *testing.T) {
	candidates := routeIntent("给这个 note-taker CLI 增加 delete 命令：支持按 ID 删除笔记，并运行 go test ./... 验证。")
	if len(candidates) == 0 {
		t.Fatalf("expected implementation work to route to a task candidate")
	}
	for _, candidate := range candidates {
		if len(candidate.Command) > 0 && candidate.Command[0] == "verify" {
			t.Fatalf("expected pure verifier candidate not to steal implementation work: %+v", candidates)
		}
	}
	if candidates[0].Command[0] != "run" {
		t.Fatalf("expected implementation work to route through run, got %+v", candidates)
	}
}

func TestIntentRoute_NamedTaskDoesNotForceFreshWorkspace(t *testing.T) {
	candidates := routeIntent("analyze repo task=demo-task")
	if len(candidates) != 1 {
		t.Fatalf("expected one run candidate, got %+v", candidates)
	}
	command := strings.Join(candidates[0].Command, " ")
	if command != "run --task demo-task analyze repo task=demo-task" {
		t.Fatalf("expected named task route, got %q", command)
	}
}

func TestIntentRoute_ReadOnlyIntentUsesPlanMode(t *testing.T) {
	candidates := routeIntent("analyze this repo and do not modify files")
	if len(candidates) != 1 {
		t.Fatalf("expected one run candidate, got %+v", candidates)
	}
	command := strings.Join(candidates[0].Command, " ")
	if !strings.Contains(command, "run --new-task --permission-mode plan") {
		t.Fatalf("expected plan mode for read-only intent, got %q", command)
	}
}

func TestIntentRoute_AnalysisReportUsesPlanMode(t *testing.T) {
	candidates := routeIntent("分析项目，找问题，写入 anal.md")
	if len(candidates) != 1 {
		t.Fatalf("expected one run candidate, got %+v", candidates)
	}
	command := strings.Join(candidates[0].Command, " ")
	if !strings.Contains(command, "run --new-task --permission-mode plan") {
		t.Fatalf("expected plan mode for analysis report intent, got %q", command)
	}
	if !strings.Contains(command, "anal.md") {
		t.Fatalf("expected requested report path to stay in command, got %q", command)
	}
}

func TestIntentRoute_ReadOnlyUtilityCommandsRouteDeterministically(t *testing.T) {
	cases := []struct {
		input   string
		command string
	}{
		{"smoke repl routing", "smoke repl-routing --new-task"},
		{"smoke coding gate", "smoke coding-gate"},
		{"show action map", "actions list"},
		{"git diff", "git diff"},
		{"git history", "git log --oneline -5"},
		{"mcp inspect server", "mcp inspect repo-inspector"},
		{"llm provider details", "llm show openrouter"},
	}
	for _, tc := range cases {
		candidates := routeIntent(tc.input)
		if len(candidates) == 0 {
			t.Fatalf("expected candidate for %q", tc.input)
		}
		command := strings.Join(candidates[0].Command, " ")
		if !strings.Contains(command, tc.command) {
			t.Fatalf("expected %q to route to %q, got %q", tc.input, tc.command, command)
		}
	}
}

func TestParseIntentCommandOptions_FromFile(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("Analyze the current repository and propose a refactoring plan\n"), 0o644); err != nil {
		t.Fatalf("write plan file failed: %v", err)
	}
	options, err := parseIntentCommandOptions([]string{"--from-file", planPath})
	if err != nil {
		t.Fatalf("parse intent command options failed: %v", err)
	}
	if options.InputFile != planPath {
		t.Fatalf("expected input file %q, got %q", planPath, options.InputFile)
	}
	input, err := readIntentInputFile(options.InputFile)
	if err != nil {
		t.Fatalf("read input file failed: %v", err)
	}
	if !strings.Contains(input, "Analyze the current repository") {
		t.Fatalf("expected plan content to be read, got %q", input)
	}
}

func TestIntentFromFile_PrintsLoadedInputCue(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("unroutable task plan\n"), 0o644); err != nil {
		t.Fatalf("write plan file failed: %v", err)
	}
	output, runErr := captureRunOutputAllowError(t, []string{"intent", "--from-file", planPath})
	if runErr != nil {
		t.Fatalf("expected from-file intent to be handled, got %v", runErr)
	}
	if !strings.Contains(output, "Intent input file loaded:") {
		t.Fatalf("expected from-file cue, got %s", output)
	}
	// BUG-3.2: When --from-file is used without explicit input, the file is treated as
	// context and a default intent is generated. This may route to file summary, clarify,
	// or intent route depending on the file content.
	if !strings.Contains(output, "Clarify:") && !strings.Contains(output, "Intent route:") && !strings.Contains(output, "File context:") {
		t.Fatalf("expected from-file input to route, clarify, or show file context, got %s", output)
	}
}

func TestIntentAttachedFileContextSummarizesActiveFile(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})
	if err := os.WriteFile("README.md", []byte("# Sheetforge\n\nWorkbook SQL workbench.\n\n## Quick Start\n\n- Import Excel.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	output, runErr := captureRunOutputAllowError(t, []string{"intent", "@README.md 查看这个文件，概况项目"})
	if runErr != nil {
		t.Fatalf("expected from-file intent to be handled, got %v", runErr)
	}
	if !strings.Contains(output, "Intent input file loaded:") {
		t.Fatalf("expected from-file cue, got %s", output)
	}
	if !strings.Contains(output, "File context:") || !strings.Contains(output, "Sheetforge") {
		t.Fatalf("expected from-file intent to use active file context, got %s", output)
	}
	if strings.Contains(output, "no active file") {
		t.Fatalf("expected active file context, got %s", output)
	}
}

func TestIntentRouterPromptIncludesStructuredCLIActions(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("AVATARS_HOME", tempDir)
	if err := os.MkdirAll(filepath.Join(tempDir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	actionsYAML := []byte(`version: 1
purpose: "test action map"
routing_rules:
  - "Use bounded commands only."
actions:
  - id: analyze_readonly
    command: "avatars run --new-task --permission-mode plan \"<task>\""
    summary: "Analyze without mutation."
    safety: "read"
    requires_confirmation: false
    use_when:
      - "The user asks to analyze without edits."
`)
	if err := os.WriteFile(filepath.Join(tempDir, "configs", "cli_actions.yaml"), actionsYAML, 0o644); err != nil {
		t.Fatalf("write cli actions failed: %v", err)
	}

	prompt := buildIntentRouterPrompt("analyze this repository without changes", []intentCandidate{
		{Command: []string{"run", "--new-task", "--permission-mode", "plan", "analyze this repository without changes"}, Summary: "run the intent as a fresh task", Confidence: 78, AutoSafe: true},
	})
	if !strings.Contains(prompt, "Structured CLI action map:") {
		t.Fatalf("expected structured action map section, got %s", prompt)
	}
	if !strings.Contains(prompt, "id=analyze_readonly") || !strings.Contains(prompt, "Use bounded commands only.") {
		t.Fatalf("expected cli action metadata in prompt, got %s", prompt)
	}
	if !strings.Contains(prompt, "Candidates:") || !strings.Contains(prompt, "Return JSON only.") {
		t.Fatalf("expected original router prompt contract to remain, got %s", prompt)
	}
}

func TestActionsCLIListsAndShowsStructuredActions(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("AVATARS_HOME", tempDir)
	if err := os.MkdirAll(filepath.Join(tempDir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	actionsYAML := []byte(`version: 1
purpose: "test action map"
actions:
  - id: analyze_readonly
    command: "avatars run --new-task --permission-mode plan \"<task>\""
    summary: "Analyze without mutation."
    safety: "read"
    requires_confirmation: false
    use_when:
      - "The user asks to analyze without edits."
`)
	if err := os.WriteFile(filepath.Join(tempDir, "configs", "cli_actions.yaml"), actionsYAML, 0o644); err != nil {
		t.Fatalf("write cli actions failed: %v", err)
	}

	listOutput := captureRunOutput(t, []string{"actions", "list"})
	if !strings.Contains(listOutput, "Actions: 1") || !strings.Contains(listOutput, "analyze_readonly") {
		t.Fatalf("expected action list output, got %s", listOutput)
	}
	showOutput := captureRunOutput(t, []string{"actions", "show", "analyze_readonly"})
	if !strings.Contains(showOutput, "Action: analyze_readonly") || !strings.Contains(showOutput, "Use when:") {
		t.Fatalf("expected action detail output, got %s", showOutput)
	}
}

func TestDefaultCLIActionMapIncludesTargetProjectDiagnostics(t *testing.T) {
	actions, err := loadCLIActions(filepath.Join("..", "..", defaultCLIActionsPath))
	if err != nil {
		t.Fatalf("load default cli actions failed: %v", err)
	}
	found := map[string]bool{}
	for _, action := range actions.Actions {
		found[action.ID] = true
	}
	for _, id := range []string{
		"bundle_health",
		"route_preview",
		"target_project_shakedown",
		"report_evidence_check",
		"memory_recall_check",
	} {
		if !found[id] {
			t.Fatalf("expected default cli action %q", id)
		}
	}
	prompt := formatCLIActionPromptSection(actions)
	if !strings.Contains(prompt, "id=route_preview") || !strings.Contains(prompt, "id=memory_recall_check") {
		t.Fatalf("expected diagnostic actions in prompt, got %s", prompt)
	}
	if strings.Contains(prompt, "use_when=") {
		t.Fatalf("expected prompt section to exclude use_when details, got %s", prompt)
	}
}

func TestRouteCLIExplainsIntentWithoutExecuting(t *testing.T) {
	output := captureRunOutput(t, []string{"route", "分析项目，找问题，总结写入 outcome.md"})
	if !strings.Contains(output, "Route input:") {
		t.Fatalf("expected route input line, got %s", output)
	}
	if !strings.Contains(output, "Natural language route: safe_run") {
		t.Fatalf("expected natural language route preview, got %s", output)
	}
	if !strings.Contains(output, "Command: avatars run --new-task --permission-mode plan") {
		t.Fatalf("expected plan-mode run preview, got %s", output)
	}
	if !strings.Contains(output, "Route mode: dry-run; no command executed.") {
		t.Fatalf("expected dry-run marker, got %s", output)
	}
}

func TestParseIntentLLMDecision_ExtractsJSON(t *testing.T) {
	decision, ok := parseIntentLLMDecision("```json\n{\"action\":\"choose\",\"choice\":2,\"confidence\":88}\n```")
	if !ok {
		t.Fatal("expected LLM decision to parse")
	}
	if decision.Action != "choose" || decision.Choice != 2 || decision.Confidence != 88 {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestIntentRoutingSkillFileParsesAsAlwaysOn(t *testing.T) {
	// Verify the skill file on disk parses correctly with always-on=true,
	// user-invocable=false, and the expected routing content.
	skillPath := filepath.Join("..", "..", "skills", "approved", "Intent_Routing_Skill.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read Intent Routing Skill file: %v", err)
	}
	content := string(data)

	// Check frontmatter fields
	if !strings.Contains(content, "always-on: true") {
		t.Fatal("Intent Routing Skill must have always-on: true")
	}
	if !strings.Contains(content, "user-invocable: false") {
		t.Fatal("Intent Routing Skill must have user-invocable: false")
	}
	if !strings.Contains(content, "lifecycle-state: approved") {
		t.Fatal("Intent Routing Skill must have lifecycle-state: approved")
	}
	// Check routing content
	if !strings.Contains(content, "Script Path") {
		t.Fatal("Intent Routing Skill must contain Script Path routing rule")
	}
	if !strings.Contains(content, "Edit Path") {
		t.Fatal("Intent Routing Skill must contain Edit Path routing rule")
	}
	if !strings.Contains(content, "游戏") {
		t.Fatal("Intent Routing Skill must contain Chinese game keyword")
	}
}

func TestIntentFromFile_SkillDocumentRoutesToFileSummary(t *testing.T) {
	// BUG-3.2: When --from-file is used with a skill document, it should route
	// to file summary instead of returning "intent is ambiguous or risky".
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "skill.md")
	skillContent := `---
name: Test Skill
---
# Test Skill
This is a test skill document.
`
	if err := os.WriteFile(planPath, []byte(skillContent), 0o644); err != nil {
		t.Fatalf("write skill file failed: %v", err)
	}
	output, runErr := captureRunOutputAllowError(t, []string{"intent", "--from-file", planPath})
	if runErr != nil {
		t.Fatalf("expected from-file intent to be handled, got %v", runErr)
	}
	if !strings.Contains(output, "Intent input file loaded:") {
		t.Fatalf("expected from-file cue, got %s", output)
	}
	// Should route to file summary, not "intent is ambiguous or risky"
	if strings.Contains(output, "intent is ambiguous or risky") {
		t.Fatalf("expected file summary routing, got error: %s", output)
	}
	if !strings.Contains(output, "File context:") {
		t.Fatalf("expected File context: output, got %s", output)
	}
}

// E2E Smoke Tests — cover critical user paths end-to-end.
