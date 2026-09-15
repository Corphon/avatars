package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/runtime"
	"avatars/internal/tasks"
)

func TestRequestedMarkdownOutputPathExtractsPathBeforeChinesePunctuation(t *testing.T) {
	path, ok := requestedMarkdownOutputPath("分析这个项目，找 1 个具体风险，写入 depth_report.md，不改文件。")
	if !ok {
		t.Fatal("expected markdown output path")
	}
	if path != "depth_report.md" {
		t.Fatalf("expected depth_report.md, got %q", path)
	}
}

func TestRunTask_WritesAnalysisReport(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Sheetforge\n\nThis project coordinates multi-avatar repo analysis.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module sheetforge\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go failed: %v", err)
	}

	output := captureRunOutput(t, []string{"run", "--new-task", "--permission-mode", "plan", "Analyze this repository, explain what it does, and report concrete issues. Do not modify files. Summarize in ana.md"})
	if !strings.Contains(output, "Analysis report: ana.md") {
		t.Fatalf("expected analysis report path in output, got %s", output)
	}
	if !strings.Contains(output, "Skill preview: Task Survey Skill") {
		t.Fatalf("expected skill preview in output, got %s", output)
	}
	if !strings.Contains(output, "Synthesis status: ") {
		t.Fatalf("expected synthesis status in output, got %s", output)
	}
	reportContent, err := os.ReadFile("ana.md")
	if err != nil {
		t.Fatalf("read report failed: %v", err)
	}
	report := string(reportContent)
	if !strings.Contains(report, "# Analysis Report") {
		t.Fatalf("expected markdown report header, got %s", report)
	}
	if !strings.Contains(report, "## Proven Findings") || !strings.Contains(report, "## Evidence-Tied Risks / Needs Verification") || !strings.Contains(report, "## Open Questions") {
		t.Fatalf("expected findings and risk sections, got %s", report)
	}
	if !strings.Contains(report, "## Project Purpose") {
		t.Fatalf("expected project purpose section, got %s", report)
	}
	if !strings.Contains(report, "Read README.md successfully") {
		t.Fatalf("expected report to include read evidence, got %s", report)
	}
	if !strings.Contains(report, "Generated skill preview: Task Survey Skill") {
		t.Fatalf("expected report to include skill preview evidence, got %s", report)
	}
}

func TestRunSummaryForDisplay_CompactsLongSummary(t *testing.T) {
	longSummary := "Multi-avatar planning run complete. " + strings.Repeat("Read README.md successfully. ", 80)
	display := runSummaryForDisplay(longSummary)
	if len(display) >= len(longSummary) {
		t.Fatalf("expected display summary to be compacted, got %d >= %d", len(display), len(longSummary))
	}
	if !strings.Contains(display, "Summary compacted.") {
		t.Fatalf("expected compacted marker, got %s", display)
	}
	if !strings.Contains(display, "Full summary preserved") {
		t.Fatalf("expected preservation cue, got %s", display)
	}
}

func TestCompactRunSummaryForDisplay_PreservesKeyRunFields(t *testing.T) {
	summary := strings.Join([]string{
		"Multi-avatar planning run complete for request \"Analyze\".",
		"Run status: completed | Synthesis status: completed",
		"Analysis report: outcome.md",
		"Transcript: .avatars/tasks/demo/sessions/demo.jsonl",
		"Code Trace Evidence",
		"- source: internal/runtime/loop.go | Function signals buildAnalysisReportMarkdown; reportFindingsSections",
		"Evidence Coverage",
		"- total=7 docs=2 manifests=1 entrypoints=1 source=3 config=0 ci=0 other=0",
	}, "\n")
	display := compactRunSummaryForDisplay(summary, 600)
	for _, expected := range []string{
		"Run status: completed | Synthesis status: completed",
		"Analysis report: outcome.md",
		"Transcript: .avatars/tasks/demo/sessions/demo.jsonl",
		"Code Trace Evidence",
		"Evidence Coverage",
	} {
		if !strings.Contains(display, expected) {
			t.Fatalf("expected compact summary to keep %q, got %s", expected, display)
		}
	}
}

func TestCompactRunSummaryForDisplay_SuppressesReadSpam(t *testing.T) {
	summary := "Multi-avatar planning run complete. " + strings.Repeat("Read README.md successfully. ", 50) + "Run status: completed | Synthesis status: completed"
	display := compactRunSummaryForDisplay(summary, 600)
	if strings.Count(display, "Read README.md successfully") > 2 {
		t.Fatalf("expected compact summary to suppress repeated read spam, got %s", display)
	}
}

func TestCompactIntentRunCapturedOutputSuppressesWorkflowProgress(t *testing.T) {
	output := strings.Join([]string{
		"Running: analyze repo",
		"Avatar Planner: decompose the task",
		"Planner starts: Decompose task and set checkpoints.",
		"Node complete: Planner -> Decompose task and set checkpoints.",
		"Reading: README.md",
		"Read README.md successfully.",
		"LLM drafting with deepseek",
		"Multi-avatar planning run complete for request \"Analyze\".",
		"Synthesis: useful answer.",
		"Task: demo-task",
		"Task root: .avatars/tasks/demo-task",
		"Transcript: .avatars/tasks/demo-task/sessions/run.jsonl",
	}, "\n")
	display := compactIntentRunCapturedOutput(output)
	for _, noisy := range []string{"Running:", "Planner starts:", "Node complete:", "Reading:", "LLM drafting"} {
		if strings.Contains(display, noisy) {
			t.Fatalf("expected compact intent output to suppress %q, got %s", noisy, display)
		}
	}
	for _, expected := range []string{"Multi-avatar planning run complete", "Synthesis: useful answer.", "Task: demo-task", "Transcript:"} {
		if !strings.Contains(display, expected) {
			t.Fatalf("expected compact intent output to keep %q, got %s", expected, display)
		}
	}
}

func TestRunSummaryForDisplay_KeepsShortSummary(t *testing.T) {
	shortSummary := "Run completed. Analysis report: a.md"
	if got := runSummaryForDisplay(shortSummary); got != shortSummary {
		t.Fatalf("expected short summary unchanged, got %q", got)
	}
}

func TestRunTask_WritesAnalysisReportEvenWhenVerificationFails(t *testing.T) {
	tempDir := t.TempDir()
	reportPath := filepath.Join(tempDir, "ana.md")
	result := runtime.RunResult{
		ReportPath:    reportPath,
		ReportContent: "# Analysis Report\n\n## Proven Findings\n- example\n\n## Evidence-Tied Risks / Needs Verification\n- none\n\n## Open Questions\n- none\n",
	}
	if err := writeAnalysisReportResult(result); err != nil {
		t.Fatalf("write analysis report failed: %v", err)
	}
	reportContent, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read analysis report failed: %v", err)
	}
	if !strings.Contains(string(reportContent), "# Analysis Report") {
		t.Fatalf("expected markdown report header, got %s", string(reportContent))
	}
	if !strings.Contains(string(reportContent), "## Proven Findings") || !strings.Contains(string(reportContent), "## Evidence-Tied Risks / Needs Verification") || !strings.Contains(string(reportContent), "## Open Questions") {
		t.Fatalf("expected findings and risk sections, got %s", string(reportContent))
	}
}

func TestWriteAnalysisReportResult_IgnoresMissingReportFields(t *testing.T) {
	if err := writeAnalysisReportResult(runtime.RunResult{}); err != nil {
		t.Fatalf("expected empty report result to be ignored, got %v", err)
	}
	if err := writeAnalysisReportResult(runtime.RunResult{ReportPath: "ana.md"}); err != nil {
		t.Fatalf("expected missing content to be ignored, got %v", err)
	}
}

func TestParseServeCommandOptions_PermissionMode(t *testing.T) {
	options, err := parseServeCommandOptions([]string{"--permission-mode", "plan", "--task", "demo-task", "Inspect workspace"})
	if err != nil {
		t.Fatalf("parse serve command options failed: %v", err)
	}
	if options.PermissionMode != runtime.PermissionModePlan {
		t.Fatalf("expected plan permission mode, got %q", options.PermissionMode)
	}
	if options.TaskID != "demo-task" {
		t.Fatalf("expected task id demo-task, got %q", options.TaskID)
	}
	if options.Input != "Inspect workspace" {
		t.Fatalf("expected input to be preserved, got %q", options.Input)
	}
}

func TestWriteCLI_PermissionModeDontAskDeniesMutation(t *testing.T) {
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

	runErr := run([]string{"write", "--permission-mode", "dontAsk", "note.txt", "blocked content"})
	if runErr == nil {
		t.Fatal("expected write command to be denied in dontAsk mode")
	}
	if !strings.Contains(runErr.Error(), "permission denied") {
		t.Fatalf("expected permission denied error, got %v", runErr)
	}
	if _, statErr := os.Stat("note.txt"); !os.IsNotExist(statErr) {
		t.Fatalf("expected denied write to avoid creating note.txt, got %v", statErr)
	}
}

func TestFormatMCPInspectResult(t *testing.T) {
	output := formatMCPInspectResult(runtime.MCPInspectResult{Sections: []runtime.MCPInspectionSection{
		{Label: "Tools", Supported: true, Names: []string{"read_file", "write_file"}},
		{Label: "Resources", Supported: false},
		{Label: "Prompts", Supported: true, Names: nil},
	}})
	if !strings.Contains(output, "Tools: 2") {
		t.Fatalf("expected tools count, got %s", output)
	}
	if !strings.Contains(output, "- read_file") || !strings.Contains(output, "- write_file") {
		t.Fatalf("expected tool names, got %s", output)
	}
	if !strings.Contains(output, "Resources: unsupported") {
		t.Fatalf("expected unsupported resources line, got %s", output)
	}
	if !strings.Contains(output, "Prompts: 0") || !strings.Contains(output, "- none") {
		t.Fatalf("expected empty prompts section, got %s", output)
	}
}

func TestParseMCPCommandOptions_TaskBinding(t *testing.T) {
	options, err := parseMCPCommandOptions([]string{"--task", "demo-task", "repo-inspector", "tools/list", `{"cursor":"0"}`}, "usage")
	if err != nil {
		t.Fatalf("parse mcp options failed: %v", err)
	}
	if options.TaskID != "demo-task" {
		t.Fatalf("expected task id demo-task, got %q", options.TaskID)
	}
	if len(options.Positionals) != 3 {
		t.Fatalf("expected 3 positionals, got %+v", options.Positionals)
	}
	if options.Positionals[0] != "repo-inspector" || options.Positionals[1] != "tools/list" {
		t.Fatalf("unexpected positionals: %+v", options.Positionals)
	}

	if _, err := parseMCPCommandOptions([]string{"--task"}, "usage"); err == nil {
		t.Fatal("expected missing task id to fail")
	}
}

func TestParseServeCommandOptions_SupportsTaskAndResume(t *testing.T) {
	options, err := parseServeCommandOptions([]string{"--task", "demo-task", "--resume", ".avatars/tasks/demo-task/sessions/demo.jsonl", "Inspect archived runs"})
	if err != nil {
		t.Fatalf("parse serve command options failed: %v", err)
	}
	if options.TaskID != "demo-task" {
		t.Fatalf("expected task id demo-task, got %q", options.TaskID)
	}
	if options.ResumeTranscript != ".avatars/tasks/demo-task/sessions/demo.jsonl" {
		t.Fatalf("expected resume transcript, got %q", options.ResumeTranscript)
	}
	if options.Input != "Inspect archived runs" {
		t.Fatalf("expected input to be preserved, got %q", options.Input)
	}
}

func TestResumeCLI_PrintsWarmLessonCount(t *testing.T) {
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
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "--task", "demo-resume-task", "Analyze the current repository and propose a refactoring plan"})
	workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load("demo-resume-task")
	if err != nil {
		t.Fatalf("load workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("new task memory store failed: %v", err)
	}
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{SessionID: "session-resume-eval", RunID: "run-resume-eval", TaskID: workspace.ID, Tool: "patch", Kind: "verifier_verdict", Verdict: "PARTIAL", Cause: "verification_partial", Summary: "Verification finished with PARTIAL.", Source: "verification", Details: []string{"go test -race ./... => PARTIAL"}}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record evaluation failed: %v", err)
	}
	_ = memoryStore.Close()
	projectStore, err := memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
	if err != nil {
		t.Fatalf("new project memory store failed: %v", err)
	}
	if err := projectStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: workspace.ID, Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Source: "evolution_candidate", Confidence: "high"}); err != nil {
		_ = projectStore.Close()
		t.Fatalf("record project lesson failed: %v", err)
	}
	if err := projectStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: "peer-task", Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Source: "evolution_candidate", Confidence: "high"}); err != nil {
		_ = projectStore.Close()
		t.Fatalf("record second project lesson failed: %v", err)
	}
	_ = projectStore.Close()
	resumeOutput := captureRunOutput(t, []string{"resume", workspace.LatestTranscriptPath()})
	if !strings.Contains(resumeOutput, "Latest run LLM:") {
		t.Fatalf("expected resume output to print latest run llm status, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Configured LLM:") {
		t.Fatalf("expected resume output to print configured llm status, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Telemetry:") {
		t.Fatalf("expected resume output to print telemetry, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Evaluation records:") {
		t.Fatalf("expected resume output to print evaluation record count, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Warm lessons:") {
		t.Fatalf("expected resume output to print warm lesson count, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Evolution candidates:") {
		t.Fatalf("expected resume output to print evolution candidate count, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Project lessons:") {
		t.Fatalf("expected resume output to print project lesson count, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "top support: 2 tasks") {
		t.Fatalf("expected resume output to print project lesson support evidence, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Task: demo-resume-task") {
		t.Fatalf("expected resume output to print task id, got %s", resumeOutput)
	}
}

func TestResumeCLI_WarnsWhenCurrentLLMConfigIsInvalid(t *testing.T) {
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
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "--task", "demo-invalid-config-resume", "Analyze the current repository and propose a refactoring plan"})
	workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load("demo-invalid-config-resume")
	if err != nil {
		t.Fatalf("load workspace failed: %v", err)
	}
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	invalidConfig := []byte("llm:\n  active_provider: doubao\n  providers:\n    doubao:\n      provider: doubao\n      base_url: https://ark.cn-beijing.volces.com/api/v3\n")
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), invalidConfig, 0o644); err != nil {
		t.Fatalf("write invalid config failed: %v", err)
	}

	resumeOutput := captureRunOutput(t, []string{"resume", workspace.LatestTranscriptPath()})
	if !strings.Contains(resumeOutput, "Configured LLM warning:") {
		t.Fatalf("expected resume output to warn on invalid llm config, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "model must be set explicitly") {
		t.Fatalf("expected resume warning to surface config validation details, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Restored summary:") {
		t.Fatalf("expected resume to keep working despite invalid current config, got %s", resumeOutput)
	}
	if !strings.Contains(resumeOutput, "Restored task memory summary:") {
		t.Fatalf("expected resume to print restored task memory summary, got %s", resumeOutput)
	}
}

func TestFeedbackImportDiagnostics_PersistsEvaluationAndCandidates(t *testing.T) {
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
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "--task", "demo-feedback-task", "Analyze the current repository and propose a refactoring plan"})
	diagnosticsPath := filepath.Join(tempDir, "diagnostics.json")
	diagnosticsJSON := `[
	  {
	    "tool": "go-lsp",
	    "severity": "error",
	    "message": "undefined: missingSymbol",
	    "path": "internal/runtime/tools.go",
	    "line": 12,
	    "column": 4,
	    "code": "undefined-name",
	    "source": "gopls"
	  }
	]`
	if err := os.WriteFile(diagnosticsPath, []byte(diagnosticsJSON), 0o644); err != nil {
		t.Fatalf("write diagnostics file failed: %v", err)
	}

	importOutput := captureRunOutput(t, []string{"feedback", "import-diagnostics", "demo-feedback-task", diagnosticsPath})
	showOutput := captureRunOutput(t, []string{"tasks", "show", "demo-feedback-task"})
	plannerViewOutput := captureRunOutput(t, []string{"tasks", "show", "demo-feedback-task", "--view", "planner"})
	workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load("demo-feedback-task")
	if err != nil {
		t.Fatalf("load feedback workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("open feedback memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		t.Fatalf("load feedback task snapshot failed: %v", err)
	}

	if !strings.Contains(importOutput, "Imported diagnostics: 1") {
		t.Fatalf("expected diagnostics import count, got %s", importOutput)
	}
	if !strings.Contains(importOutput, "Imported warm lessons: 1") {
		t.Fatalf("expected diagnostics import to print warm lesson count, got %s", importOutput)
	}
	if !strings.Contains(importOutput, "Generated evolution candidates:") {
		t.Fatalf("expected diagnostics import to print evolution candidate count, got %s", importOutput)
	}
	if !strings.Contains(importOutput, "Promoted project lessons:") {
		t.Fatalf("expected diagnostics import to print promoted project lesson count, got %s", importOutput)
	}
	if !strings.Contains(showOutput, "Evaluation records:") {
		t.Fatalf("expected tasks show to print evaluation records after diagnostics import, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Evolution candidates:") {
		t.Fatalf("expected tasks show to print evolution candidates after diagnostics import, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "Project lessons:") {
		t.Fatalf("expected tasks show to print project lessons after diagnostics import, got %s", showOutput)
	}
	if !strings.Contains(plannerViewOutput, "Imported diagnostic ERROR for go-lsp") {
		t.Fatalf("expected planner view to include imported diagnostic feedback, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "Capture and address passive feedback for go-lsp") {
		t.Fatalf("expected planner view to include evolution candidate derived from diagnostics, got %s", plannerViewOutput)
	}
	if !strings.Contains(plannerViewOutput, "resolve this imported diagnostic") {
		t.Fatalf("expected planner view to include promoted diagnostics lesson, got %s", plannerViewOutput)
	}
	if len(snapshot.WarmLessons) == 0 {
		t.Fatal("expected imported diagnostics to create at least one warm lesson")
	}
	if snapshot.WarmLessons[0].Kind != "diagnostic_import" {
		t.Fatalf("expected diagnostics warm lesson kind, got %q", snapshot.WarmLessons[0].Kind)
	}
	if !strings.Contains(snapshot.WarmLessons[0].Summary, "resolve this imported diagnostic") {
		t.Fatalf("expected diagnostics warm lesson summary, got %q", snapshot.WarmLessons[0].Summary)
	}
	projectStore, err := memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
	if err != nil {
		t.Fatalf("open project memory store failed: %v", err)
	}
	defer func() {
		_ = projectStore.Close()
	}()
	projectLessons, err := projectStore.LoadProjectLessons()
	if err != nil {
		t.Fatalf("load project lessons failed: %v", err)
	}
	if len(projectLessons) == 0 {
		t.Fatal("expected imported diagnostics to promote at least one project lesson")
	}
	if !strings.Contains(projectLessons[0].Summary, "resolve this imported diagnostic") && !strings.Contains(projectLessons[0].Summary, "Capture and address passive feedback") {
		t.Fatalf("expected promoted project lesson to reflect imported diagnostics, got %q", projectLessons[0].Summary)
	}
}

// writeBuiltinHealedMarker creates the .builtin-healed marker file in the
// skills directory. This prevents SelfHealBuiltinSkills from running during
// bootstrap, which would add builtin skills and break tests that expect a
// specific approved skill count.

func TestRunScriptEndToEnd_DeterministicTemplateRefusedForGenericTopic(t *testing.T) {
	// Regression test for report.md bug: "script verifier approves the
	// 'Hello from {name}' template even when the user asked for
	// topic-specific behavior." Now the deterministic template is gone
	// entirely: a generic description without a specific topic is refused
	// at scaffold time, not caught later by the verifier.
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	sidecar := filepath.Join(dir, "multiplication_table.expected.txt")
	expected := "1*1=1\n9x9=81\n"
	if err := os.WriteFile(sidecar, []byte(expected), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	runErr := runScript([]string{
		"--apply",
		"--expected-output-file", sidecar,
		"multiplication_table.py",
		"用 python 写一个九九乘法表",
	})
	// The scaffold must refuse to generate because "用 python 写一个九九乘法表"
	// does not match *...* or 脚本：<topic> syntax, so hasSpecificScriptTopic
	// returns false and the deterministic template is no longer available.
	if runErr == nil {
		t.Fatalf("expected runScript to refuse generic topic, got nil")
	}
	if !strings.Contains(runErr.Error(), "no specific script topic") {
		t.Fatalf("expected topic-guidance error, got: %v", runErr)
	}
	// No file should have been written since scaffold refused.
	if _, err := os.Stat("multiplication_table.py"); err == nil {
		t.Fatalf("expected multiplication_table.py NOT to be written, but it exists")
	}
}

func TestTrivialScriptRequestDispatchesDirectly(t *testing.T) {
	// When the complexity assessment returns "trivial" and the input
	// looks like a simple script request, runTask should dispatch
	// directly to script --apply --llm instead of launching the
	// multi-avatar workflow.
	//
	// We verify the assessment here; the actual dispatch is in runTask.
	assessment := planner.Assess("用 python 写一个九九乘法表")
	if assessment.Level != planner.ComplexityTrivial {
		t.Fatalf("expected trivial assessment for short script request, got %q (reason: %q)", assessment.Level, assessment.Reason)
	}
}

func TestNonTrivialRequestDoesNotFastPath(t *testing.T) {
	// A request with a multi-step verb should not be assessed as trivial.
	assessment := planner.Assess("重构整个项目的错误处理")
	if assessment.Level == planner.ComplexityTrivial {
		t.Fatalf("expected non-trivial assessment for multi-step request, got %q", assessment.Level)
	}
}

func TestDebugTokenMatchForGameRequest(t *testing.T) {
	input := "帮我用 py 写一个 文字猜谜语游戏"
	lowered := strings.ToLower(input)

	// Single token test
	if !containsAnyIntentToken(lowered, "游戏") {
		t.Fatal("containsAnyIntentToken should match '游戏'")
	}

	// Two token test
	if !containsAnyIntentToken(lowered, "游戏", "工具") {
		t.Fatal("containsAnyIntentToken should match '游戏' in two-token list")
	}

	// Negation only — should be false
	if containsAnyIntentToken(lowered, "read-only", "readonly") {
		t.Fatal("containsAnyIntentToken should NOT match negation tokens")
	}

	// The actual function call
	scriptResult := looksLikeSimpleScriptRequest(lowered)
	t.Logf("looksLikeSimpleScriptRequest = %v", scriptResult)

	if !scriptResult {
		t.Fatal("expected looksLikeSimpleScriptRequest to return true for game request")
	}
}

func TestBroadCreationIntentRoutesChineseGameRequestToScript(t *testing.T) {
	// The original failure: "帮我用 py 写一个 文字猜谜语游戏" routed to
	// 5-avatar plan mode instead of script --apply --llm.
	input := "帮我用 py 写一个 文字猜谜语游戏"
	lowered := strings.ToLower(input)

	// Debug: check each routing predicate
	t.Logf("looksLikeSimpleScriptRequest: %v", looksLikeSimpleScriptRequest(lowered))
	t.Logf("looksLikeImplementationWorkIntent: %v", looksLikeImplementationWorkIntent(lowered))
	t.Logf("looksLikeExplicitProjectWorkRequest: %v", looksLikeExplicitProjectWorkRequest(lowered))

	decision := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	t.Logf("decision.Kind=%q decision.Reason=%q command=%q", decision.Kind, decision.Reason, command)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected safe_run decision for game request, got %q (reason: %q)", decision.Kind, decision.Reason)
	}
	if !strings.HasPrefix(command, "script --apply --llm") && !strings.HasPrefix(command, "script --apply") && !strings.HasPrefix(command, "edit") {
		t.Fatalf("expected script or edit command for game request, got %q", command)
	}
}

func TestBroadCreationIntentRoutesChineseProgramRequest(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("写一个计算器程序", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected safe_run decision for program request, got %q", decision.Kind)
	}
	if !strings.HasPrefix(command, "script --apply") {
		t.Fatalf("expected script command for program request, got %q", command)
	}
}

func TestBroadCreationIntentRoutesEnglishGameRequest(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("write a text riddle game using python", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected safe_run decision for English game request, got %q", decision.Kind)
	}
	if !strings.HasPrefix(command, "script --apply") {
		t.Fatalf("expected script command for English game request, got %q", command)
	}
}

func TestImplementationWorkIntentMatchesBroadTarget(t *testing.T) {
	// "写一个游戏" has action "写" + target "游戏" → should match
	if !looksLikeImplementationWorkIntent("写一个游戏") {
		t.Fatal("expected 写+游戏 to match implementation work intent")
	}
	// "搞一个小工具" has action "搞" + target "工具"
	if !looksLikeImplementationWorkIntent("搞一个小工具") {
		t.Fatal("expected 搞+工具 to match implementation work intent")
	}
	// "弄一个计算器" has action "弄" + target (none of the target tokens yet but "一个" is not target either)
	// This should NOT match since "一个" and "计算器" are not in target list
	// unless "计算器" maps to a target token — it doesn't, so this tests the boundary
}

func TestInferSimpleScriptPathDetectsPyShorthand(t *testing.T) {
	// "用 py" should resolve to .py extension
	path := inferSimpleScriptPath("帮我用 py 写一个文字猜谜语游戏")
	if path == "" {
		t.Fatal("expected inferSimpleScriptPath to detect py shorthand, got empty path")
	}
	if filepath.Ext(path) != ".py" {
		t.Fatalf("expected .py extension, got %q", path)
	}
}

func TestStripSimpleScriptBoilerplateRemovesGamePhrases(t *testing.T) {
	// "帮我用 py 写一个 文字猜谜语游戏" → after stripping, "文字猜谜语游戏" should remain
	remainder := stripSimpleScriptBoilerplate("帮我用 py 写一个 文字猜谜语游戏")
	if remainder == "" {
		t.Fatal("expected non-empty remainder after stripping boilerplate")
	}
	// The remainder should contain the topic "文字猜谜语游戏"
	if !strings.Contains(remainder, "文字猜谜语游戏") {
		t.Fatalf("expected remainder to contain the game topic, got %q", remainder)
	}
}

// --- TODO-17: Intent Routing Skill and LLM prompt routing guidance ---

func TestAttachedFileTriggersWorkRequest(t *testing.T) {
	// When a file with task-spec content is attached and the user suffix
	// contains an action verb, the intent classifier should route to work request.
	originalFileContext := getReplFileContext()
	defer func() { setReplFileContext(originalFileContext) }()

	// Simulate a file with VBA code content.
	setReplFileContext(&fileContextState{
		Path:    "vba脚本.md",
		Content: "```vba\nSub HelloWorld()\nMsgBox \"Hello\"\nEnd Sub\n```",
	})

	// User says "按照要求执行" with the file attached.
	lowered := strings.ToLower("按照要求执行")
	if !attachedFileTriggersWorkRequest(lowered) {
		t.Fatal("expected attachedFileTriggersWorkRequest to return true for '按照要求执行' with VBA file")
	}

	// User says "run this" with the file attached.
	lowered = strings.ToLower("run this")
	if !attachedFileTriggersWorkRequest(lowered) {
		t.Fatal("expected attachedFileTriggersWorkRequest to return true for 'run this' with VBA file")
	}

	// User says "summarize this file" — should NOT trigger work request.
	lowered = strings.ToLower("summarize this file")
	if attachedFileTriggersWorkRequest(lowered) {
		t.Fatal("expected attachedFileTriggersWorkRequest to return false for 'summarize this file'")
	}

	// No file attached — should return false regardless.
	setReplFileContext(nil)
	lowered = strings.ToLower("按照要求执行")
	if attachedFileTriggersWorkRequest(lowered) {
		t.Fatal("expected attachedFileTriggersWorkRequest to return false when no file attached")
	}
}

func TestLooksLikeAttachedFileQuestion_WhenFileAttached(t *testing.T) {
	// BUG-3.1/4.2: When a file is already attached, the user's question is implicitly
	// about the attached file. Even without explicit "文件" keyword, analysis
	// questions should be recognized as file questions.
	originalFileContext := getReplFileContext()
	defer func() { setReplFileContext(originalFileContext) }()

	setReplFileContext(&fileContextState{
		Path:    "P5.js",
		Content: "p5.js library code",
	})

	testCases := []struct {
		input    string
		expected bool
		desc     string
	}{
		{"分析功能", true, "analysis verb + function keyword"},
		{"这个是干啥的", true, "question about what it does"},
		{"功能是什么", true, "asking about functionality"},
		{"内容", true, "asking about content"},
		{"总结", true, "asking for summary"},
		{"解释", true, "asking for explanation"},
		{"read", true, "read command should match when file attached"},
		{"阅读", true, "read command (Chinese) should match when file attached"},
		{"查看", true, "view command should match when file attached"},
		{"写一个 .html", false, "creation request should NOT match"},
		{"创建新文件", false, "creation request should NOT match"},
	}

	for _, tc := range testCases {
		lowered := strings.ToLower(tc.input)
		result := looksLikeAttachedFileQuestion(lowered)
		if result != tc.expected {
			t.Errorf("looksLikeAttachedFileQuestion(%q) = %v, want %v (%s)", tc.input, result, tc.expected, tc.desc)
		}
	}
}

func TestLooksLikeCapabilityQuestion_WhenFileAttached(t *testing.T) {
	// BUG-3.3/4.1: When a file is attached and user asks analysis/ability questions,
	// capability question should NOT match, so active-file-summary can handle it.
	originalFileContext := getReplFileContext()
	defer func() { setReplFileContext(originalFileContext) }()

	setReplFileContext(&fileContextState{
		Path:    "P5.js",
		Content: "p5.js library code",
	})

	testCases := []struct {
		input    string
		expected bool
		desc     string
	}{
		{"功能", false, "file attached + function keyword should NOT match capability"},
		{"分析功能", false, "file attached + analysis should NOT match capability"},
		{"这个文件能干啥", false, "file attached + ability verb should NOT match capability"},
		{"能干什么", false, "file attached + ability verb should NOT match capability"},
		{"能做什么", false, "file attached + ability verb should NOT match capability"},
		{"擅长什么", false, "file attached + ability verb should NOT match capability"},
		{"你能做什么", true, "ability question without file reference should still match"},
		{"what can you do", true, "English ability question should still match"},
		{"会写代码", true, "coding ability question should still match"},
	}

	for _, tc := range testCases {
		lowered := strings.ToLower(tc.input)
		result := looksLikeCapabilityQuestion(lowered)
		if result != tc.expected {
			t.Errorf("looksLikeCapabilityQuestion(%q) = %v, want %v (%s)", tc.input, result, tc.expected, tc.desc)
		}
	}
}

func TestLooksLikeSimpleScriptRequest_HtmlCreation(t *testing.T) {
	// BUG-3.4: ".html" creation requests should trigger script path.
	testCases := []struct {
		input    string
		expected bool
		desc     string
	}{
		{"写一个 .html", true, "write a .html"},
		{"创建 .html", true, "create .html"},
		{"写一个网页", true, "write a webpage"},
		{"创建页面", true, "create a page"},
		{"write html", true, "write html (English)"},
		{"create webpage", true, "create webpage (English)"},
		{"分析项目", false, "analysis request should NOT match"},
		{"查看文件", false, "view request should NOT match"},
	}

	for _, tc := range testCases {
		lowered := strings.ToLower(tc.input)
		result := looksLikeSimpleScriptRequest(lowered)
		if result != tc.expected {
			t.Errorf("looksLikeSimpleScriptRequest(%q) = %v, want %v (%s)", tc.input, result, tc.expected, tc.desc)
		}
	}
}

func TestInferSimpleScriptPath_Html(t *testing.T) {
	// BUG-3.4: ".html" creation requests should infer .html extension.
	testCases := []struct {
		input    string
		expected string
		desc     string
	}{
		{"写一个 .html", "script.html", "write a .html"},
		{"写一个网页", "script.html", "write a webpage"},
		{"write html page", "script.html", "write html page"},
	}

	for _, tc := range testCases {
		result := inferSimpleScriptPath(tc.input)
		if result != tc.expected {
			t.Errorf("inferSimpleScriptPath(%q) = %q, want %q (%s)", tc.input, result, tc.expected, tc.desc)
		}
	}
}

func TestE2E_TrivialScriptRequestSkipsMultiAvatar(t *testing.T) {
	// A short, single-action request should be classified as trivial
	// and bypass the 5-avatar chain.
	assessment := planner.Assess("say hello world")
	if !assessment.IsBypassable() {
		t.Fatalf("expected 'say hello world' to be trivial/ bypassable, got level=%s reason=%s", assessment.Level, assessment.Reason)
	}
	plan := planner.BuildForComplexity("say hello world", planner.Context{}, assessment)
	if len(plan.Avatars) != 1 {
		t.Fatalf("expected 1 avatar for trivial plan, got %d", len(plan.Avatars))
	}
	if plan.Avatars[0].Role != "Direct" {
		t.Fatalf("expected Direct avatar for trivial plan, got %s", plan.Avatars[0].Role)
	}
}

func TestE2E_ComplexityAssessment_Heuristic(t *testing.T) {
	// The heuristic fallback should classify short requests correctly.
	tests := []struct {
		input     string
		isTrivial bool
	}{
		{"用 python 写一个九九乘法表", true},
		{"重构整个项目的错误处理", false},
		{"say hello", true},
		{"analyze the repository and propose a refactoring plan", false},
	}
	for _, tt := range tests {
		assessment := planner.Assess(tt.input)
		if assessment.IsTrivial() != tt.isTrivial {
			t.Errorf("Assess(%q): IsTrivial=%v, want %v (level=%s)", tt.input, assessment.IsTrivial(), tt.isTrivial, assessment.Level)
		}
	}
}
