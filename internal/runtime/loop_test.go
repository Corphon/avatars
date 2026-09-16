package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/projectfiles"
	"avatars/internal/prompt"
	"avatars/internal/skills"
)

type focusedVerifierFakeExecutor struct {
	commands []string
}

func (f *focusedVerifierFakeExecutor) Run(_ context.Context, _ string, command []string) (string, error) {
	f.commands = append(f.commands, strings.Join(command, " "))
	return "ok\tfocused/package\t0.01s\n", nil
}

func withTempWorkingDir(t *testing.T) string {
	t.Helper()
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
	return tempDir
}

func TestSelectResearcherReadTarget_PrefersExplicitExistingFile(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), []byte("runtime: ready\n"), 0o644); err != nil {
		t.Fatalf("write agent config failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	target, ok := selectResearcherReadTarget("Inspect configs/agent.yaml and report concrete drift.")
	if !ok {
		t.Fatal("expected explicit request target")
	}
	if target != filepath.Clean("configs/agent.yaml") {
		t.Fatalf("expected configs/agent.yaml, got %q", target)
	}
}

func TestSelectResearcherReadTarget_MapsCLIDocsAlias(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.WriteFile("CLI_guide.md", []byte("# CLI\n"), 0o644); err != nil {
		t.Fatalf("write CLI guide failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	target, ok := selectResearcherReadTarget("Inspect CLI docs for runtime usage drift.")
	if !ok {
		t.Fatal("expected CLI docs target")
	}
	if target != "CLI_guide.md" {
		t.Fatalf("expected CLI_guide.md, got %q", target)
	}
}

func TestSelectResearcherReadTarget_FallbackExcludesCodexContextDocs(t *testing.T) {
	withTempWorkingDir(t)
	for name, content := range map[string]string{
		"process_record.md": "# Process Record\n",
		"coding_plan.md":    "# Coding Plan\n",
		"analysis.md":       "# Analysis\n",
		"README.md":         "# Readme\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	target, ok := selectResearcherReadTarget("Analyze the current repository.")
	if !ok {
		t.Fatal("expected fallback target")
	}
	if target != "README.md" {
		t.Fatalf("expected README.md fallback, got %q", target)
	}
}

func TestSelectResearcherReadTargets_IssueHuntExpandsRepositorySurvey(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll(filepath.Join("cmd", "sheetforge"), 0o755); err != nil {
		t.Fatalf("mkdir cmd failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("internal", "workbook"), 0o755); err != nil {
		t.Fatalf("mkdir internal workbook failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("internal", "sql"), 0o755); err != nil {
		t.Fatalf("mkdir internal sql failed: %v", err)
	}
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	for name, content := range map[string]string{
		"README.md": "# Readme\n",
		"go.mod":    "module demo\n",
		"assets.go": "package main\n",
		filepath.Join("cmd", "sheetforge", "main.go"):        "package main\n",
		filepath.Join("internal", "workbook", "importer.go"): "package workbook\nfunc LoadWorkbook() {}\n",
		filepath.Join("internal", "sql", "engine.go"):        "package sql\nfunc ExecuteSQL() {}\n",
		filepath.Join("configs", "agent.yaml"):               "runtime: ready\n",
		"process_record.md":                                  "# Process Record\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Analyze this repository and find concrete problems.")
	joined := strings.Join(targets, "\n")
	for _, expected := range []string{"README.md", "go.mod", "assets.go", filepath.Clean(filepath.Join("internal", "workbook", "importer.go")), filepath.Clean(filepath.Join("internal", "sql", "engine.go"))} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected expanded survey target %s in %v", expected, targets)
		}
	}
	if strings.Contains(joined, "process_record.md") {
		t.Fatalf("expected Codex context docs to stay out of repository survey, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_DeepIssueHuntIncludesTopLevelAppGo(t *testing.T) {
	withTempWorkingDir(t)
	for name, content := range map[string]string{
		"README.md": "# Demo\n",
		"go.mod":    "module demo\n",
		"main.go":   "package main\nfunc main() {}\n",
		"app.go":    "package main\nfunc (a *App) LoadSQLPresets() {}\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Deep analysis: analyze this repository, find issues, and write ana.md.")
	joined := filepath.ToSlash(strings.Join(targets, "\n"))
	if !strings.Contains(joined, "app.go") {
		t.Fatalf("expected deep survey to include top-level app.go, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_DeepIssueHuntExpandsBudget(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll("internal", 0o755); err != nil {
		t.Fatalf("mkdir internal failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	for i := 0; i < 50; i++ {
		name := filepath.Join("internal", "service_"+string(rune('a'+(i%26)))+"_"+strings.Repeat("x", i/26)+".go")
		if err := os.WriteFile(name, []byte("package internal\nfunc Demo() {}\n"), 0o644); err != nil {
			t.Fatalf("write source %s failed: %v", name, err)
		}
	}

	defaultTargets := selectResearcherReadTargets("Analyze this repository.")
	deepTargets := selectResearcherReadTargets("Deep analysis: analyze this repository, find issues, and write ana.md.")
	if len(defaultTargets) >= len(deepTargets) {
		t.Fatalf("expected deep issue hunt to expand target budget, default=%d deep=%d", len(defaultTargets), len(deepTargets))
	}
	if len(deepTargets) > explorationTargetBudget(explorationDepthDeep) {
		t.Fatalf("expected deep targets to stay bounded, got %d targets", len(deepTargets))
	}
	joined := filepath.ToSlash(strings.Join(deepTargets, "\n"))
	if strings.Contains(joined, "avatars/") || strings.Contains(joined, "process_record.md") {
		t.Fatalf("expected deep survey to keep runtime/codex paths excluded, got %v", deepTargets)
	}
}

func TestSelectResearcherReadTargets_DeepIssueHuntIncludesTests(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll(filepath.Join("internal", "runtime"), 0o755); err != nil {
		t.Fatalf("mkdir runtime failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("internal", "runtime", "loop.go"), []byte("package runtime\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatalf("write source failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("internal", "runtime", "loop_test.go"), []byte("package runtime\nfunc TestRun(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatalf("write test failed: %v", err)
	}

	targets := selectResearcherReadTargets("Deep analysis: analyze this repository, find issues, and write ana.md.")
	joined := strings.Join(targets, "\n")
	if !strings.Contains(joined, filepath.Clean(filepath.Join("internal", "runtime", "loop_test.go"))) {
		t.Fatalf("expected deep survey to include test files, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_DeepIssueHuntCapsDocsAndKeepsTraceFiles(t *testing.T) {
	withTempWorkingDir(t)
	for _, dir := range []string{
		"configs",
		"docs",
		filepath.Join("internal", "manual"),
		filepath.Join("internal", "sql"),
		filepath.Join("internal", "auto"),
		filepath.Join("internal", "output"),
		filepath.Join("internal", "plugin"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", dir, err)
		}
	}
	files := map[string]string{
		"README.md": "# Demo\n",
		"go.mod":    "module demo\n",
		"main.go":   "package main\nfunc main() {}\n",
		filepath.Join("internal", "manual", "session.go"):   "package manual\nfunc LoadWorkbook() {}\nfunc Query() {}\n",
		filepath.Join("internal", "sql", "query.go"):        "package sql\nfunc ExecuteQuery() {}\n",
		filepath.Join("internal", "auto", "runner.go"):      "package auto\nfunc RunTask() {}\n",
		filepath.Join("internal", "auto", "runner_test.go"): "package auto\nfunc TestRunTask(t *testing.T) {}\n",
		filepath.Join("internal", "output", "export.go"):    "package output\nfunc WriteExport() {}\n",
		filepath.Join("internal", "plugin", "registry.go"):  "package plugin\nfunc Register() {}\n",
		filepath.Join("configs", "sqlstr.json"):             "[]\n",
	}
	for index := 0; index < 30; index++ {
		files[filepath.Join("docs", fmt.Sprintf("phase_%02d.md", index))] = "# Phase\n"
	}
	for name, content := range files {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Deep analysis: analyze this repository, find issues, and write ana.md.")
	joined := filepath.ToSlash(strings.Join(targets, "\n"))
	for _, expected := range []string{
		"internal/manual/session.go",
		"internal/sql/query.go",
		"internal/auto/runner.go",
		"internal/auto/runner_test.go",
		"internal/output/export.go",
		"internal/plugin/registry.go",
		"configs/sqlstr.json",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected deep trace target %s in %v", expected, targets)
		}
	}
	if surveyPriorityBucketForPath(filepath.Join("internal", "auto", "runner_test.go")) != "tests" {
		t.Fatalf("expected _test.go file to use tests bucket")
	}
	docCount := 0
	for _, target := range targets {
		if surveyPriorityBucketForPath(target) == "docs" {
			docCount++
		}
	}
	docCap := surveyBucketCapsForInput("Deep analysis: analyze this repository, find issues, and write ana.md.")["docs"]
	if docCount > docCap {
		t.Fatalf("expected docs capped at %d, got %d targets=%v", docCap, docCount, targets)
	}
}

func TestSelectResearcherReadTargets_ChineseConcreteRiskRequestIsDeep(t *testing.T) {
	withTempWorkingDir(t)
	for _, dir := range []string{
		filepath.Join("internal", "auto"),
		filepath.Join("internal", "output"),
		filepath.Join("internal", "manual"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", dir, err)
		}
	}
	files := map[string]string{
		"README.md": "# Demo\n",
		"go.mod":    "module demo\n",
		"app.go":    "package main\nfunc NewApp() {}\n",
		filepath.Join("internal", "manual", "session.go"):     "package manual\nfunc ExecuteSQL() {}\n",
		filepath.Join("internal", "auto", "runner.go"):        "package auto\nfunc Run() {}\n",
		filepath.Join("internal", "auto", "runner_test.go"):   "package auto\nfunc TestRunner(t *testing.T) {}\n",
		filepath.Join("internal", "output", "export.go"):      "package output\nfunc WriteQueryToWorkbook() {}\n",
		filepath.Join("internal", "output", "export_test.go"): "package output\nfunc TestWriteQueryToWorkbook(t *testing.T) {}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	input := "分析这个项目，找 3 个具体风险；每个风险必须给出文件或函数证据、影响、验证办法；结果写入 depth.md"
	if got := determineExplorationDepth(input); got != explorationDepthDeep {
		t.Fatalf("expected Chinese concrete risk request to be deep, got %s", got)
	}
	targets := selectResearcherReadTargets(input)
	joined := filepath.ToSlash(strings.Join(targets, "\n"))
	for _, expected := range []string{
		"internal/auto/runner.go",
		"internal/auto/runner_test.go",
		"internal/output/export.go",
		"internal/output/export_test.go",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected deep Chinese risk target %s in %v", expected, targets)
		}
	}
}

func TestSelectResearcherReadTargets_DeepIssueHuntReachesConfigBucketBeforeBackfill(t *testing.T) {
	withTempWorkingDir(t)
	for _, dir := range []string{"docs", "configs", filepath.Join("internal", "service")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", dir, err)
		}
	}
	files := map[string]string{
		"README.md":                        "# Demo\n",
		"go.mod":                           "module demo\n",
		"main.go":                          "package main\nfunc main() {}\n",
		filepath.Join("configs", "a.yaml"): "a: true\n",
		filepath.Join("configs", "b.yaml"): "b: true\n",
		filepath.Join("configs", "c.yaml"): "c: true\n",
	}
	for index := 0; index < 30; index++ {
		files[filepath.Join("docs", fmt.Sprintf("phase_%02d.md", index))] = "# Phase\n"
		files[filepath.Join("internal", "service", fmt.Sprintf("service_%02d.go", index))] = "package service\nfunc Run() {}\n"
	}
	for name, content := range files {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Deep analysis: analyze this repository, find issues, and write ana.md.")
	configCount := 0
	for _, target := range targets {
		if surveyPriorityBucketForPath(target) == "config-ci" {
			configCount++
		}
	}
	if configCount < 3 {
		t.Fatalf("expected deep cap pass to reach config bucket before source backfill, got %d targets=%v", configCount, targets)
	}
	if len(targets) > explorationTargetBudget(explorationDepthDeep) {
		t.Fatalf("expected bounded target list, got %d", len(targets))
	}
}

func TestSummarizeGoSourceRead_EmitsFunctionSignals(t *testing.T) {
	summary := summarizeGoSourceRead("internal/runtime/loop.go", "package runtime\n\nimport \"fmt\"\n\nfunc buildAnalysisReportMarkdown() {}\nfunc reportFindingsSections() {}\nfunc helper() {}\n")
	if !strings.Contains(summary, "Function signals: buildAnalysisReportMarkdown; reportFindingsSections; helper.") {
		t.Fatalf("expected function signals in summary, got %q", summary)
	}
}

func TestSummarizeGoSourceRead_EmitsRepositoryPathSignals(t *testing.T) {
	summary := summarizeGoSourceRead("app.go", "package main\n\nfunc (a *App) LoadSQLPresets() error {\n\t_, _ = os.ReadFile(\"configs/sqlstr.json\")\n\treturn nil\n}\n")
	if !strings.Contains(summary, "Function signals: App.LoadSQLPresets.") || !strings.Contains(summary, "Path signals: configs/sqlstr.json.") {
		t.Fatalf("expected function and path signals in summary, got %q", summary)
	}
}

func TestSummarizeGoSourceRead_EmitsBehaviorSignals(t *testing.T) {
	summary := summarizeGoSourceRead("internal/manual/session.go", "package manual\n\nimport \"context\"\n\nfunc (m *SessionManager) ExecuteSQL(ctx context.Context, query string) error {\n\trows, err := m.db.QueryContext(ctx, query)\n\tif err != nil { return fmt.Errorf(\"query failed: %w\", err) }\n\t_ = rows\n\treturn os.WriteFile(\"out.xlsx\", []byte(query), 0o644)\n}\n")
	for _, expected := range []string{"Behavior signals:", "writes files", "executes SQL", "uses context cancellation boundary", "returns explicit errors"} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("expected behavior signal %q in summary, got %q", expected, summary)
		}
	}
}

func TestSelectResearcherReadTargets_DoesNotTreatRequestedReportAsInputEvidence(t *testing.T) {
	withTempWorkingDir(t)
	for name, content := range map[string]string{
		"README.md":  "# Demo\n",
		"go.mod":     "module demo\n",
		"ana.md":     "# stale report\n",
		"anal.md":    "# stale analysis report\n",
		"a.md":       "# stale short-name report\n",
		"outcome.md": "# stale outcome report\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Analyze this repository, find issues, and write outcome.md.")
	joined := strings.ToLower(filepath.ToSlash(strings.Join(targets, "\n")))
	// EV-Fix-2: Removed early return — repositorySurveyTargets now runs
	// as fallback even when explicit targets are found. Canonical report
	// outputs (analysis.md, report.md, outcome.md) are still excluded,
	// but short-name .md files like a.md, ana.md are valid survey docs now.
	for _, unwanted := range []string{"outcome.md"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("expected output report path %s to stay out of read targets, got %v", unwanted, targets)
		}
	}
	if strings.Contains(joined, "report.md") || strings.Contains(joined, "analysis.md") {
		t.Fatalf("expected output report path to stay out of read targets, got %v", targets)
	}
	if !strings.Contains(joined, "readme.md") || !strings.Contains(joined, "go.mod") {
		t.Fatalf("expected repository evidence targets after excluding report output, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_DoesNotTreatArbitraryRequestedMarkdownOutputAsInputEvidence(t *testing.T) {
	withTempWorkingDir(t)
	for name, content := range map[string]string{
		"README.md":     "# Demo\n",
		"go.mod":        "module demo\n",
		"smoke_repl.md": "# stale generated report\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("analyze project, find issues, write smoke_repl.md")
	joined := strings.ToLower(filepath.ToSlash(strings.Join(targets, "\n")))
	if strings.Contains(joined, "smoke_repl.md") {
		t.Fatalf("expected arbitrary requested report output to stay out of read targets, got %v", targets)
	}
	if !strings.Contains(joined, "readme.md") || !strings.Contains(joined, "go.mod") {
		t.Fatalf("expected repository evidence targets after excluding arbitrary report output, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_ExcludesGeneratedAnalysisReportByContent(t *testing.T) {
	withTempWorkingDir(t)
	for name, content := range map[string]string{
		"README.md":        "# Demo\n",
		"go.mod":           "module demo\n",
		"smoke_compact.md": "# Analysis Report\n\n## Project Purpose\nstale generated report\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("analyze project, find issues, write smoke_repl.md")
	joined := strings.ToLower(filepath.ToSlash(strings.Join(targets, "\n")))
	if strings.Contains(joined, "smoke_compact.md") {
		t.Fatalf("expected generated analysis report content to stay out of survey targets, got %v", targets)
	}
	if !strings.Contains(joined, "readme.md") || !strings.Contains(joined, "go.mod") {
		t.Fatalf("expected repository evidence targets after excluding generated reports, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_ExplicitReportInspectionStillWorks(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.WriteFile("anal.md", []byte("# prior report\n"), 0o644); err != nil {
		t.Fatalf("write anal.md failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	targets := selectResearcherReadTargets("Inspect anal.md for report quality.")
	if len(targets) == 0 || targets[0] != "anal.md" {
		t.Fatalf("expected explicit inspect request to read anal.md, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_ExcludesGenericGeneratedReportNames(t *testing.T) {
	withTempWorkingDir(t)
	for name, content := range map[string]string{
		"README.md":             "# Demo\n",
		"go.mod":                "module demo\n",
		"analysis-final.md":     "# stale generated report\n",
		"repository_report.md":  "# stale generated report\n",
		"findings.md":           "# stale generated report\n",
		"summary-2026-05-28.md": "# stale generated report\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Analyze project, find issues, and write analysis-final.md.")
	joined := strings.ToLower(filepath.ToSlash(strings.Join(targets, "\n")))
	for _, unwanted := range []string{"analysis-final.md", "repository_report.md", "findings.md", "summary-2026-05-28.md"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("expected generated report-like file %s to stay out of default survey, got %v", unwanted, targets)
		}
	}
}

func TestSelectResearcherReadTargets_CanStillInspectExistingReportWhenAsked(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.WriteFile("anal.md", []byte("# prior report\n"), 0o644); err != nil {
		t.Fatalf("write anal.md failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	targets := selectResearcherReadTargets("Inspect anal.md for report quality.")
	if len(targets) == 0 || targets[0] != "anal.md" {
		t.Fatalf("expected explicit inspect request to read anal.md, got %v", targets)
	}
}

func TestParseGoModFacts_IgnoresRequireBlockParens(t *testing.T) {
	module, requirements := parseGoModFacts("module demo\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/b v1.2.3\n\tgithub.com/c/d v0.4.0 // indirect\n)\n")
	if module != "demo" {
		t.Fatalf("expected module demo, got %q", module)
	}
	joined := strings.Join(requirements, "\n")
	if strings.Contains(joined, "(") || strings.Contains(joined, ")") {
		t.Fatalf("expected require block delimiters to stay out of requirements, got %v", requirements)
	}
	if !strings.Contains(joined, "github.com/a/b v1.2.3") || !strings.Contains(joined, "github.com/c/d v0.4.0") {
		t.Fatalf("expected real requirements, got %v", requirements)
	}
}

func TestBuildAnalysisReportMarkdown_PassesThroughCompleteMarkdown(t *testing.T) {
	report := "```markdown\n# Project Purpose\nDemo purpose.\n\n# Proven Findings\n1. `internal/service.go`: Concrete issue. Evidence anchor: internal/service.go.\n\n## Evidence-Tied Risks / Needs Verification\n- Example risk.\n\n## Open Questions\n- Example question.\n```"
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), "Read README.md successfully. Read go.mod successfully. Module: demo. Read main.go successfully. Package: main.", "summary", report, "", "", "", false)
	if strings.Contains(content, "## Synthesis") || strings.Contains(content, "## Evidence Reviewed") || strings.Contains(content, "## Proven Findings\n- No concrete defect was proven from the current survey.") {
		t.Fatalf("expected complete markdown report passthrough, got %s", content)
	}
	if !strings.HasPrefix(content, "# Project Purpose") || !strings.Contains(content, "# Proven Findings") {
		t.Fatalf("expected complete report content, got %s", content)
	}
	if !strings.Contains(content, "## Evidence Appendix") {
		t.Fatalf("expected complete report to append compact evidence appendix when missing, got %s", content)
	}
}

func TestBuildAnalysisReportMarkdown_WrapsCompleteLookingReportWithoutProofAnchors(t *testing.T) {
	report := "```markdown\n# Project Purpose\nDemo purpose.\n\n# Proven Findings\n1. Concrete issue.\n\n## Evidence-Tied Risks / Needs Verification\n- Example risk.\n\n## Open Questions\n- Example question.\n```"
	readSummary := "Read README.md successfully. Title: Demo. Read internal/service.go successfully. Package: internal. Top declarations: func Run() error {."
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), readSummary, "summary", report, "", "", "", false)
	if !strings.Contains(content, "# Analysis Report") || !strings.Contains(content, "## Synthesis") {
		t.Fatalf("expected complete-looking but unanchored report to be wrapped, got %s", content)
	}
	if !strings.Contains(content, "No concrete defect was proven from the current survey.") {
		t.Fatalf("expected unanchored finding to be demoted, got %s", content)
	}
}

func TestBuildAnalysisReportMarkdown_WrapsFindingLackingRequiredProofSource(t *testing.T) {
	report := "```markdown\n# Project Purpose\nDemo purpose.\n\n# Proven Findings\n1. Missing CI/CD. Evidence anchor: README.md.\n\n## Evidence-Tied Risks / Needs Verification\n- Example risk.\n\n## Open Questions\n- Example question.\n```"
	readSummary := "Read README.md successfully. Title: Demo."
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), readSummary, "summary", report, "", "", "", false)
	if !strings.Contains(content, "# Analysis Report") || !strings.Contains(content, "## Synthesis") {
		t.Fatalf("expected evidence-thin finding to be wrapped, got %s", content)
	}
	if !strings.Contains(content, "No concrete defect was proven from the current survey.") {
		t.Fatalf("expected missing proof source to block passthrough, got %s", content)
	}
}

func TestBuildAnalysisReportMarkdown_WrapsMixedAnchoredAndUnanchoredFindings(t *testing.T) {
	report := "```markdown\n# Project Purpose\nDemo purpose.\n\n# Proven Findings\n1. `internal/service.go`: Concrete issue. Evidence anchor: internal/service.go.\n2. Generic issue with no anchor.\n\n## Evidence-Tied Risks / Needs Verification\n- Example risk.\n\n## Open Questions\n- Example question.\n```"
	readSummary := "Read README.md successfully. Title: Demo. Read internal/service.go successfully. Package: internal. Top declarations: func Run() error {."
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), readSummary, "summary", report, "", "", "", false)
	if !strings.Contains(content, "# Analysis Report") || !strings.Contains(content, "## Synthesis") {
		t.Fatalf("expected mixed proof quality report to be wrapped, got %s", content)
	}
	if !strings.Contains(content, "No concrete defect was proven from the current survey.") {
		t.Fatalf("expected unanchored finding to block passthrough, got %s", content)
	}
}

func TestBuildAnalysisReportMarkdown_WrapsShallowIssuesReport(t *testing.T) {
	report := "# Project Purpose\nDemo purpose.\n\n## Issues\n- Missing CI/CD.\n- Go 1.25 does not exist.\n"
	readSummary := "Read README.md successfully. Title: Demo. Read go.mod successfully. Module: demo."
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), readSummary, "summary", report, "", "", "", false)
	if !strings.Contains(content, "# Analysis Report") || !strings.Contains(content, "## Synthesis") {
		t.Fatalf("expected shallow report to be wrapped in evidence framework, got %s", content)
	}
	if !strings.Contains(content, "## Proven Findings") || !strings.Contains(content, "No concrete defect was proven from the current survey.") {
		t.Fatalf("expected no unproven issue promotion, got %s", content)
	}
	if !strings.Contains(content, "## Evidence-Tied Risks / Needs Verification") {
		t.Fatalf("expected evidence-tied risk section, got %s", content)
	}
}

func TestBuildAnalysisReportMarkdown_WrappedMarkdownSynthesisDoesNotDuplicateHeadings(t *testing.T) {
	report := "# Project Purpose\nDemo purpose.\n\n# Evidence Reviewed\n- README.md\n\n# Proven Findings\n1. Missing config without evidence anchor.\n\n# Open Questions\n- What now?\n"
	readSummary := "Read README.md successfully. Title: Demo. Key points: SQL presets are loaded from and saved to `configs/sqlstr.json`. Read go.mod successfully. Module: demo."
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), readSummary, "summary", report, "", "", "", false)
	if strings.Count(content, "\n## Project Purpose\n") != 1 || strings.Count(content, "\n## Synthesis\n") != 1 {
		t.Fatalf("expected wrapper report not to embed raw markdown headings, got %s", content)
	}
	if strings.Contains(content, "# Evidence Reviewed\n- README.md") {
		t.Fatalf("expected raw markdown synthesis to be summarized inside wrapper, got %s", content)
	}
	if !strings.Contains(content, "## Repository Gap Evidence") || !strings.Contains(content, "`configs/sqlstr.json`") {
		t.Fatalf("expected deterministic gap evidence to remain, got %s", content)
	}
}

func TestBuildAnalysisReportMarkdown_DoesNotAppendFallbackFindingsToCompleteReport(t *testing.T) {
	report := "```markdown\n# Project Purpose\nDemo purpose.\n\n# Proven Findings\n1. `internal/service.go`: Concrete issue. Evidence anchor: internal/service.go.\n\n## Evidence-Tied Risks / Needs Verification\n- Example risk.\n\n## Open Questions\n- Example question.\n```"
	readSummary := "Read README.md successfully. Read go.mod successfully. Module: demo. Read main.go successfully. Package: main."
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), readSummary, "summary", report, "", "", "", false)
	for _, unwanted := range []string{
		"## Proven Findings\n- No concrete defect was proven from the current survey.",
		"## Evidence-Tied Risks / Needs Verification\n- No tentative risk was identified by the current fallback checks.",
		"## Open Questions\n- No open question remained after the current fallback checks.",
		"## Synthesis\n",
	} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("expected complete report not to append fallback findings %q, got %s", unwanted, content)
		}
	}
}

func TestSelectResearcherReadTargets_DoesNotDefaultToCopiedAvatarsPaths(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll(filepath.Join("cmd", "avatars"), 0o755); err != nil {
		t.Fatalf("mkdir cmd avatars failed: %v", err)
	}
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	for name, content := range map[string]string{
		"README.md": "# Target Project\n",
		"go.mod":    "module target\n",
		filepath.Join("cmd", "avatars", "main.go"): "package main\n",
		filepath.Join("configs", "agent.yaml"):     "provider: copied-agent-config\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Analyze this repository and find concrete problems.")
	joined := strings.Join(targets, "\n")
	for _, unwanted := range []string{filepath.Clean(filepath.Join("cmd", "avatars", "main.go")), filepath.Clean(filepath.Join("configs", "agent.yaml"))} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("expected copied avatars path %s to stay out of default survey, got %v", unwanted, targets)
		}
	}
}

func TestSelectResearcherReadTargets_AllowsAvatarsOwnSignaturePathsInAvatarsRepo(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll(filepath.Join("cmd", "avatars"), 0o755); err != nil {
		t.Fatalf("mkdir cmd avatars failed: %v", err)
	}
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	for name, content := range map[string]string{
		"README.md": "# Avatars\n",
		"go.mod":    "module avatars\n",
		filepath.Join("cmd", "avatars", "main.go"): "package main\nfunc main() {}\n",
		filepath.Join("configs", "agent.yaml"):     "provider: demo\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Analyze this repository and find concrete problems.")
	joined := strings.Join(targets, "\n")
	if !strings.Contains(joined, filepath.Clean(filepath.Join("cmd", "avatars", "main.go"))) {
		t.Fatalf("expected avatars repo survey to allow own CLI entrypoint, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_DoesNotSurveyBundledAvatarsDirectory(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll(filepath.Join("avatars", "configs"), 0o755); err != nil {
		t.Fatalf("mkdir avatars configs failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("avatars", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir avatars skills failed: %v", err)
	}
	for name, content := range map[string]string{
		"README.md": "# Target Project\n",
		"go.mod":    "module target\n",
		filepath.Join("avatars", "configs", "agent.yaml"): "provider: copied-agent-config\n",
		filepath.Join("avatars", "CLI_guide.md"):          "# Avatars CLI\n",
		filepath.Join("avatars", "skills", "demo.md"):     "# Skill\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	targets := selectResearcherReadTargets("Analyze this repository and find concrete problems.")
	joined := filepath.ToSlash(strings.Join(targets, "\n"))
	if strings.Contains(joined, "avatars/") {
		t.Fatalf("expected bundled avatars directory to stay out of default survey, got %v", targets)
	}
}

func TestSelectResearcherReadTargets_DoesNotTreatDocsAvatarsAsGenericProjectEvidence(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll("docs", 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Target Project\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("docs", "avatars.md"), []byte("# Avatars Runtime Notes\n"), 0o644); err != nil {
		t.Fatalf("write docs avatars failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("docs", "usage.md"), []byte("# Usage\n"), 0o644); err != nil {
		t.Fatalf("write docs usage failed: %v", err)
	}

	targets := selectResearcherReadTargets("Analyze this repository and find concrete problems.")
	joined := filepath.ToSlash(strings.Join(targets, "\n"))
	if strings.Contains(joined, "docs/avatars.md") {
		t.Fatalf("expected docs/avatars.md to stay out of generic target-project survey, got %v", targets)
	}
	if !strings.Contains(joined, "docs/usage.md") {
		t.Fatalf("expected generic docs to remain surveyable, got %v", targets)
	}
}

func TestBuildExplorationRounds_SplitsExplicitAndDiscoveryTargets(t *testing.T) {
	rounds := buildExplorationRounds("Inspect README.md and the rest of the repository.", []string{"README.md", "go.mod", "internal/app/bootstrap.go"})
	if len(rounds) != 3 {
		t.Fatalf("expected 3 rounds, got %+v", rounds)
	}
	if rounds[0].Title != "explicit request targets" {
		t.Fatalf("expected explicit round first, got %+v", rounds[0])
	}
	if len(rounds[0].Targets) != 1 || rounds[0].Targets[0] != "README.md" {
		t.Fatalf("expected explicit round to keep README.md, got %+v", rounds[0].Targets)
	}
	if rounds[1].Title != "manifests and dependencies" {
		t.Fatalf("expected manifests round second, got %+v", rounds[1])
	}
	if rounds[2].Title != "domain logic" {
		t.Fatalf("expected domain logic round third, got %+v", rounds[2])
	}
}

func TestBuildExplorationRounds_EmptyTargetSetUsesBootstrapRound(t *testing.T) {
	rounds := buildExplorationRounds("Analyze an empty project.", nil)
	if len(rounds) != 1 {
		t.Fatalf("expected bootstrap round, got %+v", rounds)
	}
	if rounds[0].Title != "bootstrap scan" {
		t.Fatalf("expected bootstrap scan, got %+v", rounds[0])
	}
	want := projectfiles.BootstrapSurveyTargets()
	if len(rounds[0].Targets) != len(want) {
		t.Fatalf("expected bootstrap targets %v, got %v", want, rounds[0].Targets)
	}
	for _, target := range rounds[0].Targets {
		base := filepath.Base(target)
		switch base {
		case "docs", "cmd", "internal", "src", "configs", "workflows":
			t.Fatalf("bootstrap scan must not read directories, got %q", target)
		}
	}
}

func TestExplorationRoundStartedSummary_IncludesTargetsAndReason(t *testing.T) {
	summary := explorationRoundStartedSummary(0, explorationRound{
		Title:   "project documentation",
		Targets: []string{"README.md", "docs/avatars.md", "CLI_guide.md", "process_record.md", "analysis.md"},
		Reason:  "understand declared purpose",
	})
	if !strings.Contains(summary, "Researcher round 1: project documentation") {
		t.Fatalf("expected round title in summary, got %q", summary)
	}
	if !strings.Contains(summary, "targets: README.md, docs/avatars.md, CLI_guide.md, process_record.md, +1 more") {
		t.Fatalf("expected compact target summary, got %q", summary)
	}
	if !strings.Contains(summary, "reason: understand declared purpose") {
		t.Fatalf("expected reason in summary, got %q", summary)
	}
}

func TestExplorationRoundCompletedSummary_UsesCountNotRawJoinedSummaries(t *testing.T) {
	summary := explorationRoundCompletedSummary(1, explorationRound{
		Title:   "manifests and dependencies",
		Targets: []string{"go.mod", "go.sum"},
	})
	if summary != "Researcher round 2 complete: manifests and dependencies (2 file(s))." {
		t.Fatalf("unexpected completion summary: %q", summary)
	}
}

func TestBuildFallbackAnalysisReport_ExplainsEmptyProject(t *testing.T) {
	report := buildFallbackAnalysisReport("No repository evidence was found. Prepare a bootstrap plan and project skeleton before claiming analysis results.", planner.Build("Analyze an empty project."), "", "", "", "failed: timeout")
	if !strings.Contains(report, "# Bootstrap Planning Report") || !strings.Contains(report, "## Required Preparation Files") || !strings.Contains(report, "docs/architecture.md") {
		t.Fatalf("expected empty-project fallback report, got %s", report)
	}
}

func TestBuildFallbackAnalysisReport_ProjectStateNotBlankWhenSourceEvidenceExists(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Read go.mod successfully. Module: demo. Read main.go successfully. Package: main. Entrypoint signal: main function present."
	report := buildFallbackAnalysisReport(readSummary, planner.Build("Analyze project"), "", "", "", "failed: timeout")
	if !strings.Contains(report, "## Project State") || !strings.Contains(report, "Source evidence was found.") {
		t.Fatalf("expected fallback report project state to be populated, got %s", report)
	}
	if !strings.Contains(report, "## Evidence Coverage") || !strings.Contains(report, "total=3") || !strings.Contains(report, "docs=1") || !strings.Contains(report, "manifests=1") || !strings.Contains(report, "entrypoints=1") {
		t.Fatalf("expected fallback report evidence coverage, got %s", report)
	}
}

func TestBuildFallbackAnalysisReport_MarksSynthesisFailureAndSeparatesFindings(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Sheetforge. Key points: Manual mode imports workbooks; Automatic mode runs task scripts. Read go.mod successfully. Module: sheetforge. Read main.go successfully. Package: main. Entrypoint signal: main function present. Read frontend/app.js successfully. First line: // app. Read internal/manual/session.go successfully. Package: manual. Read internal/auto/runner.go successfully. Package: auto. Read internal/output/export.go successfully. Package: output."
	report := buildFallbackAnalysisReport(readSummary, planner.Build("Analyze project"), "", "", "", "failed: context deadline exceeded")
	for _, expected := range []string{
		"## Evidence-Backed Conclusions",
		"credible architecture read",
		"two user flows",
		"Startup wiring spans frontend bootstrap and a Go entrypoint",
		"Evidence anchor: internal/manual/session.go and internal/auto/runner.go",
		"## Synthesis Status",
		"failed: context deadline exceeded",
		"## Proven Findings",
		"No concrete defect was proven from the current survey.",
		"## Evidence-Tied Risks / Needs Verification",
		"## Open Questions",
		"LLM synthesis did not complete",
		"manual/auto split still needs code-level tracing",
		"Does the workbook/session boundary actually stay shared",
		"Trace `internal/manual/session.go`",
		"Trace `internal/auto/runner.go`",
		"Trace `internal/output/export.go`",
		"Trace `main.go` and natural-language routing entrypoints",
		"Evidence anchor: internal/manual/session.go and internal/auto/runner.go should be read together",
		"## Review Leads",
		"`internal/manual/session.go` + `internal/auto/runner.go`",
		"`internal/output/export.go`: verify workbook write-back",
		"`frontend/app.js` + `main.go`",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("expected fallback report to contain %q, got %s", expected, report)
		}
	}
	if strings.Index(report, "## Evidence-Backed Conclusions") > strings.Index(report, "## Evidence Reviewed") {
		t.Fatalf("expected fallback report to put conclusions before evidence appendix, got %s", report)
	}
	if !strings.Contains(report, "## Evidence Reviewed") {
		t.Fatalf("expected fallback report to keep evidence appendix, got %s", report)
	}
	if !strings.Contains(report, "Evidence anchor: README.md, go.mod") {
		t.Fatalf("expected evidence-linked fallback report, got %s", report)
	}
	if strings.Contains(report, "## Project Purpose\nRead README.md successfully") {
		t.Fatalf("expected fallback report purpose to avoid raw read log, got %s", report)
	}
	if strings.Contains(report, "### ") {
		t.Fatalf("expected fallback report to use top-level findings headings, got %s", report)
	}
}

func TestBuildAnalysisReportMarkdown_EmptyProjectUsesBootstrapReport(t *testing.T) {
	report := buildAnalysisReportMarkdown("Analyze empty project", planner.Build("Analyze empty project"), "No repository evidence was found. Prepare a bootstrap plan and project skeleton before claiming analysis results.", "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "# Bootstrap Planning Report") {
		t.Fatalf("expected bootstrap report, got %s", report)
	}
	if strings.Contains(report, "## Synthesis") {
		t.Fatalf("expected bootstrap report to avoid generic synthesis wrapper, got %s", report)
	}
}

func TestBuildAnalysisReportMarkdown_MinimalDocsOnlyUsesAnalysisReport(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Headings: What It Does; Quick Start."
	report := buildAnalysisReportMarkdown("Analyze docs-only project", planner.Build("Analyze docs-only project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "# Analysis Report") {
		t.Fatalf("expected docs-only analysis report, got %s", report)
	}
	if !strings.Contains(report, "Only documentation evidence was found") {
		t.Fatalf("expected docs-only state guidance, got %s", report)
	}
}

func TestBuildAnalysisReportMarkdown_MinimalManifestOnlyUsesAnalysisReport(t *testing.T) {
	readSummary := "Read go.mod successfully. Module: demo."
	report := buildAnalysisReportMarkdown("Analyze manifest-only project", planner.Build("Analyze manifest-only project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "# Analysis Report") {
		t.Fatalf("expected manifest-only analysis report, got %s", report)
	}
	if !strings.Contains(report, "Only manifest evidence was found") {
		t.Fatalf("expected manifest-only state guidance, got %s", report)
	}
}

func TestBuildAnalysisReportMarkdown_DoesNotDumpReadLogAsPurpose(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Sheetforge. Headings: What It Does; Data Flow; Quick Start. Key points: Manual mode imports workbooks; Automatic mode runs task scripts. Read go.mod successfully. Module: sheetforge."
	report := buildAnalysisReportMarkdown("Analyze project", planner.Build("Analyze project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if strings.Contains(report, "## Project Purpose\nRead README.md successfully") {
		t.Fatalf("expected project purpose to avoid raw read log dump, got %s", report)
	}
	if !strings.Contains(report, "## Project Purpose\nManual mode imports workbooks") {
		t.Fatalf("expected project purpose to use concise README evidence, got %s", report)
	}
	if !strings.Contains(report, "- Read README.md successfully.") || !strings.Contains(report, "- Read go.mod successfully.") {
		t.Fatalf("expected evidence to split read summaries into list items, got %s", report)
	}
}

func TestBuildAnalysisReportMarkdown_ProjectStateNotBlankWhenSourceEvidenceExists(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Read go.mod successfully. Module: demo. Read main.go successfully. Package: main. Entrypoint signal: main function present. Read internal/service.go successfully. Package: internal. Top declarations: func Run() error {."
	report := buildAnalysisReportMarkdown("Analyze project", planner.Build("Analyze project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "## Project State\n\n- Source evidence was found.\n- Docs, manifests, and source were all reviewed.") {
		t.Fatalf("expected populated project state for source-backed survey, got %s", report)
	}
}

func TestProjectPurposeLine_RejectsChattyFallbackText(t *testing.T) {
	purpose := projectPurposeLine("Read README.md successfully. Title: Sheetforge.", "Local deterministic synthesis preserved runtime continuity without a live provider response.")
	if strings.Contains(strings.ToLower(purpose), "synthesis") || strings.Contains(strings.ToLower(purpose), "runtime continuity") {
		t.Fatalf("expected purpose to reject chatty fallback text, got %q", purpose)
	}
	if purpose != "Sheetforge" && purpose != "Sheetforge." && purpose != "Project purpose could not be safely inferred from 1 read summary item(s)." {
		t.Fatalf("expected purpose to stay evidence-bound, got %q", purpose)
	}
}

func TestSurveyPriorityBucketForPath_ClassifiesCoreTargets(t *testing.T) {
	cases := map[string]string{
		"README.md":                            "docs",
		"go.mod":                               "manifests",
		filepath.Join("cmd", "app", "main.go"): "entrypoints",
		filepath.Join("internal", "service", "run.go"):  "domain-logic",
		filepath.Join(".github", "workflows", "ci.yml"): "config-ci",
	}
	for path, expected := range cases {
		if got := surveyPriorityBucketForPath(path); got != expected {
			t.Fatalf("expected %s -> %s, got %s", path, expected, got)
		}
	}
}

func TestSelectResearcherReadTarget_RejectsUnsafeMentionThenFallsBack(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.WriteFile("README.md", []byte("# Readme\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), []byte("runtime: ready\n"), 0o644); err != nil {
		t.Fatalf("write agent config failed: %v", err)
	}

	target, ok := selectResearcherReadTarget("Inspect ../secret.txt then configs/agent.yaml.")
	if !ok {
		t.Fatal("expected safe explicit target")
	}
	if target != filepath.Clean("configs/agent.yaml") {
		t.Fatalf("expected configs/agent.yaml after rejecting unsafe path, got %q", target)
	}
}

func TestSummarizeReadResult_UsesEnglishFallbackForNonASCIIPreview(t *testing.T) {
	summary := summarizeReadResult("process_record.md", "# Process Record — 编码进度与隐患\nnext line")
	if summary != "Read process_record.md successfully. First line captured from repository content." {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestSummarizeMarkdownRead_CapturesCommandSignals(t *testing.T) {
	content := "# Demo\n\n## Quick Start\n\n```powershell\ngo test ./...\ngo build ./cmd/avatars\n```\n"
	summary := summarizeMarkdownRead("README.md", content)
	if !strings.Contains(summary, "Command signals: go test ./...; go build ./cmd/avatars.") {
		t.Fatalf("expected markdown command signals, got %q", summary)
	}
}

func TestSummarizeGoSourceRead_AddsEntrypointSignal(t *testing.T) {
	content := "package main\n\nimport \"fmt\"\n\nfunc main() {}\nfunc helper() {}\nfunc another() {}\ntype App struct{}\n"
	summary := summarizeGoSourceRead("cmd/avatars/main.go", content)
	if !strings.Contains(summary, "Entrypoint signal: main function present.") {
		t.Fatalf("expected entrypoint signal in go source summary, got %q", summary)
	}
	if !strings.Contains(summary, "Declaration surface:") {
		t.Fatalf("expected declaration surface signal in go source summary, got %q", summary)
	}
}

func TestBuildLLMSummaryRequest_IncludesGuardedMutationContract(t *testing.T) {
	bundle := prompt.Build("repair the verifier warning")
	request := buildLLMSummaryRequest(bundle, planner.Build("repair the verifier warning"), "Read process_record.md successfully.", "", "Task Survey Skill", "", false, nil, PersonalityConfig{}, "")
	if !strings.Contains(request.SystemPrompt, "intent, expected_targets, and verification follow-up") {
		t.Fatalf("expected llm system prompt to include guarded mutation contract, got %q", request.SystemPrompt)
	}
	if !strings.Contains(request.SystemPrompt, "changed_files remains the observed mutation evidence") {
		t.Fatalf("expected llm system prompt to distinguish declared targets from observed changes, got %q", request.SystemPrompt)
	}
	if !strings.Contains(request.SystemPrompt, "Before any coding or mutating action") || !strings.Contains(request.SystemPrompt, "remain in plan mode/read-only") {
		t.Fatalf("expected llm system prompt to include coding preflight gate, got %q", request.SystemPrompt)
	}
	if !strings.Contains(request.SystemPrompt, "closure must cite changed_files plus the verifier result") {
		t.Fatalf("expected llm system prompt to include coding closure gate, got %q", request.SystemPrompt)
	}
	if !strings.Contains(request.UserPrompt, "generated_skill_preview: Task Survey Skill") {
		t.Fatalf("expected llm user prompt to include skill preview, got %q", request.UserPrompt)
	}
}

func TestBuildLLMSummaryRequest_CompactsLongReadSummary(t *testing.T) {
	items := []string{}
	for index := 0; index < 20; index++ {
		items = append(items, fmt.Sprintf("Read file_%02d.go successfully. Package: demo. Top declarations: func Demo%d() {}.", index, index))
	}
	request := buildLLMSummaryRequest(prompt.Build("analyze"), planner.Build("analyze"), strings.Join(items, " "), "", "", "", false, nil, PersonalityConfig{}, "")
	if !strings.Contains(request.UserPrompt, "digest total=20") {
		t.Fatalf("expected digest summary, got %q", request.UserPrompt)
	}
	if strings.Contains(request.UserPrompt, "file_19.go") {
		t.Fatalf("expected long read summary to be bounded, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "source: file_00.go [package=demo; top declarations=func Demo0() {}], file_01.go [package=demo; top declarations=func Demo1() {}], file_02.go [package=demo; top declarations=func Demo2() {}]") {
		t.Fatalf("expected bounded source anchors, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "source_more: +17") {
		t.Fatalf("expected source overflow marker, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "evidence_coverage: total=20") || !strings.Contains(request.SystemPrompt, "Evidence Coverage") {
		t.Fatalf("expected evidence coverage in synthesis request, got system=%q user=%q", request.SystemPrompt, request.UserPrompt)
	}
}

func TestBuildLLMSummaryRequest_UsesBucketedEvidenceDigest(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Read go.mod successfully. Module: demo. Read main.go successfully. Package: main. Entrypoint signal: main function present. Read internal/manual/session.go successfully. Package: manual. Read internal/auto/runner.go successfully. Package: auto. Read internal/output/export.go successfully. Package: output."
	request := buildLLMSummaryRequest(prompt.Build("analyze"), planner.Build("analyze"), readSummary, "", "", "", false, nil, PersonalityConfig{}, "")
	for _, want := range []string{
		"docs: README.md [title=Demo]",
		"manifests: go.mod [module=demo]",
		"entrypoints: main.go [package=main; entrypoint signal=main function present]",
		"source: internal/manual/session.go [package=manual], internal/auto/runner.go [package=auto], internal/output/export.go [package=output]",
	} {
		if !strings.Contains(request.UserPrompt, want) {
			t.Fatalf("expected digest anchor %q, got %q", want, request.UserPrompt)
		}
	}
}

func TestBuildLLMSummaryRequest_PreservesGoFunctionSignalsInDigest(t *testing.T) {
	readSummary := "Read internal/runtime/loop.go successfully. Package: runtime. Imports: fmt; strings. Top declarations: func buildAnalysisReportMarkdown() {} | func reportFindingsSections() {} | func helper() {}. Function signals: buildAnalysisReportMarkdown; reportFindingsSections; helper. Behavior signals: reads files; writes files; executes SQL. Path signals: configs/sqlstr.json."
	request := buildLLMSummaryRequest(prompt.Build("analyze"), planner.Build("analyze"), readSummary, "", "", "", false, nil, PersonalityConfig{}, "")
	if !strings.Contains(request.UserPrompt, "source: internal/runtime/loop.go [package=runtime; imports=fmt; strings; top declarations=func buildAnalysisReportMarkdown() {} | func reportFindingsSections() {} | func helper() {}; function signals=buildAnalysisReportMarkdown; reportFindingsSections; helper; behavior signals=reads files; writes files; executes SQL; path signals=configs/sqlstr.json]") {
		t.Fatalf("expected go function signals to survive digest compression, got %q", request.UserPrompt)
	}
}

func TestBuildLLMSummaryRequest_IncludesRepositoryGapEvidence(t *testing.T) {
	withTempWorkingDir(t)
	readSummary := "Read README.md successfully. Key points: SQL presets are loaded from and saved to `configs/sqlstr.json`."
	request := buildLLMSummaryRequest(prompt.Build("analyze"), planner.Build("analyze"), readSummary, "", "", "", false, nil, PersonalityConfig{}, "")
	if !strings.Contains(request.SystemPrompt, "Repository Gap Evidence") || !strings.Contains(request.UserPrompt, "repository_gap_evidence: - `configs/sqlstr.json` is referenced") {
		t.Fatalf("expected repository gap evidence in synthesis request, system=%q user=%q", request.SystemPrompt, request.UserPrompt)
	}
}

func TestEvidenceCoverageMarkdown_ReportsCoverageGaps(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Read go.mod successfully. Module: demo."
	coverage := evidenceCoverageMarkdown(readSummary)
	if !strings.Contains(coverage, "total=2") || !strings.Contains(coverage, "docs=1") || !strings.Contains(coverage, "manifests=1") {
		t.Fatalf("expected coverage counts, got %s", coverage)
	}
	if !strings.Contains(coverage, "no source-domain files") || !strings.Contains(coverage, "no entrypoint files") {
		t.Fatalf("expected coverage gaps, got %s", coverage)
	}
}

func TestReportFindingsSections_AnchorsRisksAndOpenQuestions(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Key points: demo. Read go.mod successfully. Module: demo."
	sections := strings.Join(reportFindingsSections(readSummary, "synthesis fallback used", "LLM synthesis did not complete"), "\n")
	if !strings.Contains(sections, "## Evidence-Tied Risks / Needs Verification") || !strings.Contains(sections, "## Open Questions") {
		t.Fatalf("expected updated findings sections, got %s", sections)
	}
	if !strings.Contains(sections, "Evidence anchor: README.md, go.mod") {
		t.Fatalf("expected evidence anchors in findings sections, got %s", sections)
	}
}

func TestReportReviewLeads_SurfaceFileLevelChecks(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Read go.mod successfully. Module: demo. Read main.go successfully. Package: main. Read frontend/app.js successfully. First line: // app. Read internal/manual/session.go successfully. Package: manual. Read internal/auto/runner.go successfully. Package: auto. Read internal/output/export.go successfully. Package: output. Read internal/plugin/registry.go successfully. Package: plugin. Read internal/plugins/registry.go successfully. Package: plugins."
	leads := reportReviewLeads(readSummary)
	for _, expected := range []string{
		"`internal/manual/session.go` + `internal/auto/runner.go`",
		"`internal/output/export.go`: verify workbook write-back",
		"`internal/plugin/registry.go` + `internal/plugins/registry.go`",
		"`frontend/app.js` + `main.go`",
		"`go.mod`: verify declared Go/Wails/Excel dependencies",
	} {
		if !strings.Contains(leads, expected) {
			t.Fatalf("expected review lead %q, got %s", expected, leads)
		}
	}
}

func TestReportCodeTraceEvidence_SurfacesFunctionTestAndCommandSignals(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Command signals: go test ./...; go build ./cmd/app. Read internal/service.go successfully. Package: internal. Top declarations: func Run() error {} | type Service struct{}. Function signals: Run; Service.Handle. Behavior signals: executes SQL; returns explicit errors. Read internal/service_test.go successfully. Package: internal. Function signals: TestRun; TestHandle."
	trace := reportCodeTraceEvidence(readSummary)
	for _, expected := range []string{
		"- docs: README.md | Command signals go test ./...; go build ./cmd/app",
		"- source: internal/service.go | Function signals Run; Service.Handle | Behavior signals executes SQL; returns explicit errors | Top declarations func Run() error {} | type Service struct{} | Package internal",
		"- tests: internal/service_test.go | Function signals TestRun; TestHandle | Package internal",
	} {
		if !strings.Contains(trace, expected) {
			t.Fatalf("expected trace evidence %q, got %s", expected, trace)
		}
	}
}

func TestReportCodeTraceEvidence_PrioritizesSourceBehaviorOverDocs(t *testing.T) {
	items := []string{}
	for i := 0; i < 12; i++ {
		items = append(items, fmt.Sprintf("Read docs/phase_%02d.md successfully. First line: # Phase", i))
	}
	items = append(items, "Read internal/manual/session.go successfully. Package: manual. Function signals: SessionManager.ExecuteSQL. Behavior signals: executes SQL; handles Excel workbooks; returns explicit errors.")
	trace := reportCodeTraceEvidence(strings.Join(items, " "))
	if !strings.Contains(trace, "- source: internal/manual/session.go") || !strings.Contains(trace, "Behavior signals executes SQL; handles Excel workbooks; returns explicit errors") {
		t.Fatalf("expected source behavior to be promoted over docs, got %s", trace)
	}
	if strings.Count(trace, "- docs:") > 1 {
		t.Fatalf("expected docs to be capped in trace evidence, got %s", trace)
	}
}

func TestReportVerificationLeads_MapsSourcesToRunnableTests(t *testing.T) {
	readSummary := strings.Join([]string{
		"Read internal\\manual\\session.go successfully. Package: manual. Function signals: SessionManager.ExecuteSQL. Behavior signals: executes SQL; handles Excel workbooks.",
		"Read internal\\manual\\session_test.go successfully. Package: manual. Function signals: TestSessionManager_LoadWorkbookAndExecuteSQL.",
		"Read internal/output/export.go successfully. Package: output. Function signals: WriteQueryToWorkbook. Behavior signals: writes files; handles Excel workbooks.",
	}, " ")
	leads := reportVerificationLeads(readSummary)
	if !strings.Contains(leads, "go test ./internal/manual -run TestSessionManager") || !strings.Contains(leads, "Evidence anchor: internal/manual/session_test.go") {
		t.Fatalf("expected runnable manual verifier lead, got %s", leads)
	}
	if !strings.Contains(leads, "no matching `internal/output/export_test.go` evidence was captured") {
		t.Fatalf("expected missing export test lead, got %s", leads)
	}
}

func TestFocusedVerificationChecksFromReadSummary_MapsRunnableLeadsOnly(t *testing.T) {
	readSummary := strings.Join([]string{
		"Read internal\\manual\\session.go successfully. Package: manual. Function signals: SessionManager.ExecuteSQL. Behavior signals: executes SQL.",
		"Read internal\\manual\\session_test.go successfully. Package: manual. Function signals: TestSessionManager_LoadWorkbookAndExecuteSQL.",
		"Read internal/output/export.go successfully. Package: output. Function signals: WriteQueryToWorkbook. Behavior signals: writes files.",
	}, " ")
	checks := focusedVerificationChecksFromReadSummary(readSummary)
	if len(checks) != 1 {
		t.Fatalf("expected one runnable focused check, got %+v", checks)
	}
	if got := strings.Join(checks[0].Command, " "); got != "go test ./internal/manual -run TestSessionManager" {
		t.Fatalf("expected manual focused command, got %q", got)
	}
}

func TestRunFocusedVerificationLeads_FormatsActualResults(t *testing.T) {
	readSummary := strings.Join([]string{
		"Read internal/manual/session.go successfully. Package: manual. Function signals: SessionManager.ExecuteSQL. Behavior signals: executes SQL.",
		"Read internal/manual/session_test.go successfully. Package: manual. Function signals: TestSessionManager_LoadWorkbookAndExecuteSQL.",
	}, " ")
	executor := &focusedVerifierFakeExecutor{}
	evidence := runFocusedVerificationLeads(context.Background(), ".", executor, readSummary)
	if len(executor.commands) != 1 || executor.commands[0] != "go test ./internal/manual -run TestSessionManager" {
		t.Fatalf("expected focused verifier command to run once, got %v", executor.commands)
	}
	for _, expected := range []string{"Read focused verification results successfully.", "`go test ./internal/manual -run TestSessionManager` => PASS", "Focused verification summary: Verification finished with PASS"} {
		if !strings.Contains(evidence, expected) {
			t.Fatalf("expected verification evidence to include %q, got %s", expected, evidence)
		}
	}
	results := reportVerificationResults(appendReadSummaryEvidence(readSummary, evidence))
	if !strings.Contains(results, "`go test ./internal/manual -run TestSessionManager` => PASS") || !strings.Contains(results, "summary: Verification finished with PASS") {
		t.Fatalf("expected report verification results, got %s", results)
	}
}

func TestVerificationOutputExcerpt_KeepsFailureDetail(t *testing.T) {
	output := "\n--- FAIL: TestRunner_Run_HazardManifestTask_WritesWorkbooks (26.97s)\n    runner_test.go:140: expected final output path a, got b\nFAIL\n"
	excerpt := verificationOutputExcerpt(output, 3)
	if !strings.Contains(excerpt, "--- FAIL: TestRunner_Run_HazardManifestTask_WritesWorkbooks") || !strings.Contains(excerpt, "expected final output path") {
		t.Fatalf("expected failure detail in excerpt, got %q", excerpt)
	}
}

func TestReportFunctionRiskLeads_UsesBehaviorSignals(t *testing.T) {
	readSummary := strings.Join([]string{
		"Read internal/manual/session.go successfully. Package: manual. Function signals: SessionManager.LoadWorkbook; SessionManager.ExecuteSQL. Behavior signals: executes SQL; handles Excel workbooks; uses locking.",
		"Read internal/output/export.go successfully. Package: output. Function signals: WriteQueryToWorkbook. Behavior signals: writes files; handles Excel workbooks.",
		"Read internal/auto/runner.go successfully. Package: auto. Function signals: Runner.Register; Runner.Run. Behavior signals: registers runtime items; uses context cancellation boundary.",
	}, " ")
	leads := reportFunctionRiskLeads(readSummary)
	for _, expected := range []string{"schema drift", "append row detection", "unknown task errors"} {
		if !strings.Contains(leads, expected) {
			t.Fatalf("expected risk lead %q, got %s", expected, leads)
		}
	}
}

func TestReportEvidenceQuality_ScoresTraceAndVerifierEvidence(t *testing.T) {
	readSummary := strings.Join([]string{
		"Read app.go successfully. Package: main. Function signals: App.LoadSQLPresets. Behavior signals: reads files.",
		"Read internal/manual/session.go successfully. Package: manual. Function signals: SessionManager.ExecuteSQL. Behavior signals: executes SQL.",
		"Read internal/output/export.go successfully. Package: output. Function signals: WriteQueryToWorkbook. Behavior signals: writes files.",
		"Read internal/auto/runner.go successfully. Package: auto. Function signals: Runner.Run. Behavior signals: uses context cancellation boundary.",
		"Read internal/plugin/registry.go successfully. Package: plugin. Function signals: Registry.Register. Behavior signals: registers runtime items.",
		"Read internal/manual/session_test.go successfully. Package: manual. Function signals: TestSessionManager_LoadWorkbookAndExecuteSQL. Behavior signals: executes SQL.",
		"Read internal/auto/runner_test.go successfully. Package: auto. Function signals: TestRunner_Run_WithUnknownTask_ReturnsError. Behavior signals: returns explicit errors.",
	}, " ")
	quality := reportEvidenceQuality(readSummary)
	if !strings.Contains(quality, "verdict: high") || !strings.Contains(quality, "verifier leads:") {
		t.Fatalf("expected high evidence quality, got %s", quality)
	}
}

func TestBuildAnalysisReportMarkdown_IncludesVerifierRiskAndQualitySections(t *testing.T) {
	readSummary := "Read internal/manual/session.go successfully. Package: manual. Function signals: SessionManager.ExecuteSQL. Behavior signals: executes SQL; handles Excel workbooks. Read internal/manual/session_test.go successfully. Package: manual. Function signals: TestSessionManager_LoadWorkbookAndExecuteSQL."
	readSummary = appendReadSummaryEvidence(readSummary, "Read focused verification results successfully. Focused verification results: `go test ./internal/manual -run TestSessionManager` => PASS (Command completed successfully.). Focused verification summary: Verification finished with PASS. 1 passed, 0 partial, 0 failed.")
	report := buildAnalysisReportMarkdown("Analyze project", planner.Build("Analyze project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	for _, expected := range []string{"## Verification Leads", "go test ./internal/manual -run TestSessionManager", "## Verification Results", "`go test ./internal/manual -run TestSessionManager` => PASS", "## Function-Level Risk Leads", "schema drift", "## Evidence Quality"} {
		if !strings.Contains(report, expected) {
			t.Fatalf("expected report to include %q, got %s", expected, report)
		}
	}
}

func TestBuildAnalysisReportMarkdown_PromotesFocusedVerifierFailure(t *testing.T) {
	readSummary := "Read internal/auto/runner.go successfully. Package: auto. Function signals: Runner.Run. Behavior signals: handles Excel workbooks; returns explicit errors. Read internal/auto/runner_test.go successfully. Package: auto. Function signals: TestRunner_Run_HazardManifestTask_WritesWorkbooks."
	readSummary = appendReadSummaryEvidence(readSummary, "Read focused verification results successfully. Focused verification results: `go test ./internal/auto -run TestRunner` => FAIL (exit status 1) output: --- FAIL: TestRunner_Run_HazardManifestTask_WritesWorkbooks (26.40s) | runner_test.go:140: expected final output path a, got b | FAIL. Focused verification summary: Verification finished with FAIL. 0 passed, 0 partial, 1 failed.")
	report := buildAnalysisReportMarkdown("Analyze project", planner.Build("Analyze project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "## Proven Findings") || !strings.Contains(report, "Focused verifier failed: `go test ./internal/auto -run TestRunner` => FAIL") {
		t.Fatalf("expected verifier failure to become proven finding, got %s", report)
	}
	if strings.Contains(report, "No concrete defect was proven from the current survey") {
		t.Fatalf("expected no-defect fallback to be suppressed, got %s", report)
	}
}

func TestReportRemediationLeads_MapsVerifierFailureToTestLine(t *testing.T) {
	readSummary := "Read internal/auto/runner.go successfully. Package: auto. Function signals: Runner.Register; Runner.Run. Read internal/auto/runner_test.go successfully. Package: auto."
	readSummary = appendReadSummaryEvidence(readSummary, "Read focused verification results successfully. Focused verification results: `go test ./internal/auto -run TestRunner` => FAIL (exit status 1) output: --- FAIL: TestRunner_Run_HazardManifestTask_WritesWorkbooks (26.40s) | runner_test.go:140: expected final output path a, got b | FAIL. Focused verification summary: Verification finished with FAIL. 0 passed, 0 partial, 1 failed.")

	leads := reportRemediationLeads(readSummary)
	for _, expected := range []string{"internal/auto/runner_test.go:140", "TestRunner_Run_HazardManifestTask_WritesWorkbooks", "go test ./internal/auto -run TestRunner", "Source target: `internal/auto/runner.go (Runner.Register; Runner.Run)`"} {
		if !strings.Contains(leads, expected) {
			t.Fatalf("expected remediation lead to include %q, got %s", expected, leads)
		}
	}
	nextChecks := reportNextChecks(readSummary, "", "", "")
	firstLine := strings.Split(nextChecks, "\n")[0]
	if !strings.Contains(firstLine, "Fix/inspect verifier failure first") || !strings.Contains(firstLine, "internal/auto/runner_test.go:140") {
		t.Fatalf("expected next checks to prioritize verifier failure, got %s", nextChecks)
	}
	if strings.Contains(nextChecks, "Trace `internal/auto/runner.go`") {
		t.Fatalf("expected verifier-covered package to suppress generic trace lead, got %s", nextChecks)
	}
	summary := focusedVerificationFailureSummary(readSummary)
	if !strings.Contains(summary, "Focused verifier failure") || !strings.Contains(summary, "internal/auto/runner_test.go:140") {
		t.Fatalf("expected terminal failure summary, got %s", summary)
	}
}

func TestBuildAnalysisReportMarkdown_CompleteReportAppendsDeterministicVerificationAppendix(t *testing.T) {
	synthesis := "```markdown\n# Project Purpose\nDemo purpose.\n\n# Proven Findings\n1. `internal/service.go`: Concrete issue. Evidence anchor: internal/service.go.\n\n## Evidence-Tied Risks / Needs Verification\n- Example risk.\n\n## Open Questions\n- Example question.\n```"
	readSummary := "Read internal/manual/session.go successfully. Package: manual. Function signals: SessionManager.ExecuteSQL. Behavior signals: executes SQL. Read internal/manual/session_test.go successfully. Package: manual. Function signals: TestSessionManager_LoadWorkbookAndExecuteSQL."
	readSummary = appendReadSummaryEvidence(readSummary, "Read focused verification results successfully. Focused verification results: `go test ./internal/manual -run TestSessionManager` => PASS (Command completed successfully.). Focused verification summary: Verification finished with PASS. 1 passed, 0 partial, 0 failed.")
	content := buildAnalysisReportMarkdown("analyze", planner.Build("analyze"), readSummary, "summary", synthesis, "", "", "", false)
	for _, expected := range []string{"## Evidence Appendix", "### Verification Results", "`go test ./internal/manual -run TestSessionManager` => PASS", "### Evidence Quality"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected deterministic appendix to include %q, got %s", expected, content)
		}
	}
	if strings.Contains(content, "## Synthesis\n") {
		t.Fatalf("expected complete report passthrough, got %s", content)
	}
}

func TestReportRepositoryGapEvidence_SurfacesDeclaredMissingFiles(t *testing.T) {
	withTempWorkingDir(t)
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("internal", "manual"), 0o755); err != nil {
		t.Fatalf("mkdir internal manual failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), []byte("ok: true\n"), 0o644); err != nil {
		t.Fatalf("write existing config failed: %v", err)
	}
	readSummary := "Read README.md successfully. Key points: SQL presets are loaded from and saved to `configs/sqlstr.json`; runtime config is in `configs/agent.yaml`; manual package is in `internal/manual/`; docs mention `https://example.com/nope`; run `go test ./internal/manual -run TestSessionManager`."

	gaps := reportRepositoryGapEvidence(readSummary)
	if !strings.Contains(gaps, "`configs/sqlstr.json` is referenced") {
		t.Fatalf("expected missing declared sqlstr path, got %s", gaps)
	}
	if strings.Contains(gaps, "configs/agent.yaml") || strings.Contains(gaps, "internal/manual") || strings.Contains(gaps, "https://example.com") || strings.Contains(gaps, "go test") {
		t.Fatalf("expected existing paths and urls to stay out of gaps, got %s", gaps)
	}
}

func TestBuildAnalysisReportMarkdown_IncludesCodeTraceEvidence(t *testing.T) {
	readSummary := "Read README.md successfully. Title: Demo. Read internal/service.go successfully. Package: internal. Top declarations: func Run() error {}. Function signals: Run."
	report := buildAnalysisReportMarkdown("Analyze project", planner.Build("Analyze project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "## Code Trace Evidence") || !strings.Contains(report, "- source: internal/service.go | Function signals Run") {
		t.Fatalf("expected generated report to include code trace evidence, got %s", report)
	}
}

func TestBuildAnalysisReportMarkdown_IncludesRepositoryGapEvidence(t *testing.T) {
	withTempWorkingDir(t)
	readSummary := "Read README.md successfully. Key points: SQL presets are loaded from and saved to `configs/sqlstr.json`."
	report := buildAnalysisReportMarkdown("Analyze project", planner.Build("Analyze project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "## Repository Gap Evidence") || !strings.Contains(report, "`configs/sqlstr.json` is referenced") {
		t.Fatalf("expected generated report to include repository gap evidence, got %s", report)
	}
}

func TestBuildAnalysisReportMarkdown_PromotesSourceBackedMissingPathFinding(t *testing.T) {
	withTempWorkingDir(t)
	readSummary := "Read README.md successfully. Key points: SQL presets are loaded from and saved to `configs/sqlstr.json`. Read app.go successfully. Package: main. Function signals: App.LoadSQLPresets; App.SaveSQLPresets. Path signals: configs/sqlstr.json."
	report := buildAnalysisReportMarkdown("Analyze project", planner.Build("Analyze project"), readSummary, "summary", "No synthesis available.", "", "", "", false)
	if !strings.Contains(report, "## Proven Findings") || !strings.Contains(report, "`configs/sqlstr.json` is missing while `app.go` references it in source path signals") {
		t.Fatalf("expected source-backed missing path finding, got %s", report)
	}
	if !strings.Contains(report, "Evidence anchor: app.go") {
		t.Fatalf("expected finding to anchor app.go, got %s", report)
	}
}

func TestReportNextChecks_SuggestsTargetedVerificationForSourceBackedMissingPath(t *testing.T) {
	withTempWorkingDir(t)
	readSummary := "Read README.md successfully. Key points: SQL presets are loaded from and saved to `configs/sqlstr.json`. Read app.go successfully. Package: main. Function signals: App.LoadSQLPresets; App.SaveSQLPresets. Path signals: configs/sqlstr.json."
	checks := reportNextChecks(readSummary, "", "", "")
	for _, expected := range []string{
		"Verify source-backed missing path `configs/sqlstr.json`",
		"`app.go` (`App.LoadSQLPresets; App.SaveSQLPresets`)",
		"runtime fallback/error behavior",
	} {
		if !strings.Contains(checks, expected) {
			t.Fatalf("expected next checks to include %q, got %s", expected, checks)
		}
	}
}

func TestFallbackConclusionLines_SurfaceArchitectureAndTimeoutContext(t *testing.T) {
	lines := fallbackConclusionLines("Read README.md successfully. Title: Demo. Read go.mod successfully. Module: demo. Read main.go successfully. Package: main. Entrypoint signal: main function present. Read frontend/app.js successfully. First line: // app. Read internal/manual/session.go successfully. Package: manual. Read internal/auto/runner.go successfully. Package: auto.", "failed: context deadline exceeded")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "manual workbook/SQL handling and task-driven automation") {
		t.Fatalf("expected product-shape conclusion, got %s", joined)
	}
	if !strings.Contains(joined, "Startup wiring spans frontend bootstrap and a Go entrypoint") {
		t.Fatalf("expected wiring conclusion, got %s", joined)
	}
	if !strings.Contains(joined, "timed out") {
		t.Fatalf("expected timeout conclusion, got %s", joined)
	}
}

func TestResolveAnalysisReportPath_AllowsRequestedMarkdownName(t *testing.T) {
	for _, input := range []string{
		"分析项目是干啥的，找问题，提出下一步想法，最终结果写入 anal.md",
		"分析这个项目，找 1 个具体风险，写入 depth_report.md，不改文件。",
		"Analyze this repository and write results to report.md",
	} {
		got := resolveAnalysisReportPath(input)
		if got == "" {
			t.Fatalf("expected report path from %q", input)
		}
		if !strings.HasSuffix(strings.ToLower(got), ".md") {
			t.Fatalf("expected markdown report path, got %q", got)
		}
	}
}

func TestBuildLLMSummaryRequest_IncludesRestoredVerificationContext(t *testing.T) {
	bundle := prompt.BuildWithResume(
		"repair the verifier warning",
		"stable summary",
		"task summary",
		nil,
		nil,
		nil,
		memstore.PromptVerificationContext("demo-task", memstore.Snapshot{
			Verification:      &memstore.VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL."},
			EvaluationRecords: []memstore.EvaluationRecord{{Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Details: []string{"expected_targets: note.txt"}}},
		}),
	)
	request := buildLLMSummaryRequest(bundle, planner.Build("repair the verifier warning"), "Read process_record.md successfully.", "", "", "", true, nil, PersonalityConfig{}, "")
	if !strings.Contains(request.UserPrompt, "restored_verification_context: current_verification: PARTIAL via patch") {
		t.Fatalf("expected llm user prompt to include verification context, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "latest_reverify_attempt_targets: note.txt") {
		t.Fatalf("expected llm user prompt to include remediation targets, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "latest_non_pass_follow_up: avatars verify --task demo-task") {
		t.Fatalf("expected llm user prompt to include task-scoped follow-up, got %q", request.UserPrompt)
	}
}

func TestBuildLLMSummaryRequest_IncludesRestoredRetryClosureVerifierContext(t *testing.T) {
	bundle := prompt.BuildWithResume(
		"continue failed node retry closure",
		"stable summary",
		"task summary",
		nil,
		nil,
		nil,
		memstore.PromptVerificationContext("demo-task", memstore.Snapshot{
			EvaluationRecords: []memstore.EvaluationRecord{
				{RunID: "run-retry", TaskID: "demo-task", Cause: "failed_node_retry_attempt", Summary: "Retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
				{RunID: "run-retry", TaskID: "demo-task", Cause: "node_work_evidence", Summary: "Retry node work completed.", Details: []string{"retry_attempt_id: retry-a", "node_id: node-build", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verification_report_path: verifier/retry-a.json"}},
				{RunID: "run-retry", TaskID: "demo-task", Cause: "failed_node_retry_scheduler_resume", Summary: "Retry scheduler resume completed.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed"}},
				{RunID: "run-retry", TaskID: "demo-task", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"retry_attempt_id: retry-a", "status: completed"}},
			},
		}),
	)
	request := buildLLMSummaryRequest(bundle, planner.Build("continue failed node retry closure"), "Read process_record.md successfully.", "", "", "", true, nil, PersonalityConfig{}, "")
	for _, want := range []string{
		"restored_verification_context:",
		"failed_node_retry_attempt_closure_verifier_gate_status: verified_pass",
		"failed_node_retry_attempt_closure_verifier_verdict: PASS",
		"failed_node_retry_attempt_closure_verification_report_path: verifier/retry-a.json",
	} {
		if !strings.Contains(request.UserPrompt, want) {
			t.Fatalf("expected llm user prompt to include %q, got %q", want, request.UserPrompt)
		}
	}
}

func TestBuildLLMSummaryRequest_IncludesPlanRemediationEnvelope(t *testing.T) {
	request := buildLLMSummaryRequest(
		prompt.Build("repair the verifier warning"),
		planner.BuildWithContext("repair the verifier warning", planner.Context{Remediation: &planner.RemediationEnvelope{
			CurrentVerification:                   "PARTIAL via patch",
			LatestNonPassVerification:             "2026-05-05T03:14:49Z | patch | PARTIAL",
			TaskScopedFollowUp:                    "avatars verify --task demo-task",
			ReverifyStatus:                        "pending reverify for latest non-pass verification",
			LatestRemediation:                     "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.",
			LatestGuardedRemediation:              "Verifier remediation resume attempt recorded for reverify repair work.",
			GuardedRemediationStatus:              "guarded_ready",
			GuardedRemediationAuthority:           "verifier_reverify",
			GuardedRemediationSource:              "verifier_remediation_resume_attempt",
			FailedNodeRetryClosureStatus:          "completed",
			FailedNodeRetryClosureReady:           "true",
			FailedNodeRetryClosureVerifierGate:    "verified_pass",
			FailedNodeRetryClosureVerifierVerdict: "PASS",
			FailedNodeRetryClosureReport:          "verifier/retry-a.json",
			RecoverySummaryCategory:               "manual_required",
			RecoverySummaryAction:                 "run_verifier_remediation_repair",
			RecoverySummaryGuardStatus:            "guarded_ready",
			RecoverySummaryGuidance:               "Guarded remediation is pending verifier closure; keep repair and reverify explicit.",
			ExpectedTargets:                       []string{"note.txt", "process_record.md"},
		}}),
		"Read process_record.md successfully.",
		"",
		"",
		"",
		false,
		nil,
		PersonalityConfig{},
		"",
	)
	if !strings.Contains(request.UserPrompt, "plan_remediation_current: PARTIAL via patch") {
		t.Fatalf("expected llm user prompt to include remediation verification summary, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "plan_remediation_follow_up: 2026-05-05T03:14:49Z | patch | PARTIAL | avatars verify --task demo-task | pending reverify for latest non-pass verification") {
		t.Fatalf("expected llm user prompt to include remediation follow-up, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "plan_remediation_expected_targets: note.txt, process_record.md") {
		t.Fatalf("expected llm user prompt to include remediation expected targets, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "plan_remediation_attempts:") || !strings.Contains(request.UserPrompt, "plan_remediation_guard: guarded_ready | verifier_reverify | verifier_remediation_resume_attempt") {
		t.Fatalf("expected llm user prompt to include compressed remediation attempts and guard summary, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "plan_remediation_retry_closure: completed | true | verified_pass | PASS | verifier/retry-a.json") {
		t.Fatalf("expected llm user prompt to include failed-node retry closure verifier gate, got %q", request.UserPrompt)
	}
	if !strings.Contains(request.UserPrompt, "plan_remediation_recovery: manual_required | run_verifier_remediation_repair | guarded_ready | Guarded remediation is pending verifier closure; keep repair and reverify explicit.") {
		t.Fatalf("expected llm user prompt to include recovery summary guidance, got %q", request.UserPrompt)
	}
}

func TestPlannerRemediationEnvelope(t *testing.T) {
	envelope := plannerRemediationEnvelope("demo-task", memstore.Snapshot{
		Verification: &memstore.VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL."},
		EvaluationRecords: []memstore.EvaluationRecord{
			{Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation resume attempt recorded for reverify repair work.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "expected_targets: note.txt", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}},
			{Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Details: []string{"expected_targets: note.txt"}},
			{Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
			{Cause: "node_work_evidence", Summary: "Retried workflow node completed.", Details: []string{"retry_attempt_id: retry-a", "node_id: node-build", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verification_report_path: verifier/retry-a.json"}},
			{Cause: "failed_node_retry_scheduler_resume", Summary: "Scheduler resumed retry.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed"}},
			{Cause: "run_lifecycle_updated", Summary: "Retry run completed.", Details: []string{"retry_attempt_id: retry-a", "status: completed"}},
		},
	})
	if envelope == nil {
		t.Fatal("expected remediation envelope")
	}
	if envelope.TaskScopedFollowUp != "avatars verify --task demo-task" {
		t.Fatalf("expected task-scoped follow-up, got %+v", envelope)
	}
	if len(envelope.ExpectedTargets) != 1 || envelope.ExpectedTargets[0] != "note.txt" {
		t.Fatalf("expected remediation expected targets, got %+v", envelope)
	}
	if envelope.GuardedRemediationStatus != "guarded_ready" || envelope.GuardedRemediationAuthority != "verifier_reverify" {
		t.Fatalf("expected guarded remediation metadata, got %+v", envelope)
	}
	if envelope.VerifierRemediationClosureStatus != "pending_guarded_remediation" || envelope.VerifierRemediationClosureResume != "resume-remediate" {
		t.Fatalf("expected pending guarded remediation closure chain, got %+v", envelope)
	}
	if envelope.FailedNodeRetryClosureStatus != "completed" || envelope.FailedNodeRetryClosureReady != "true" {
		t.Fatalf("expected failed-node retry closure fields, got %+v", envelope)
	}
	if envelope.FailedNodeRetryClosureVerifierGate != "verified_pass" || envelope.FailedNodeRetryClosureVerifierVerdict != "PASS" || envelope.FailedNodeRetryClosureReport != "verifier/retry-a.json" {
		t.Fatalf("expected failed-node retry closure verifier fields, got %+v", envelope)
	}
	if envelope.RecoverySummaryCategory != "manual_required" || envelope.RecoverySummaryAction != "run_verifier_remediation_repair" || envelope.RecoverySummaryGuardStatus != "guarded_ready" {
		t.Fatalf("expected recovery summary metadata in remediation envelope, got %+v", envelope)
	}
	if envelope.RecoverySummaryGuidance != "Guarded remediation is pending verifier closure; keep repair and reverify explicit." {
		t.Fatalf("expected recovery summary guidance in remediation envelope, got %+v", envelope)
	}
	if len(envelope.Proposals) != 2 {
		t.Fatalf("expected remediation proposals, got %+v", envelope)
	}
	if envelope.Proposals[1].FollowUpCommand != "avatars verify --task demo-task" {
		t.Fatalf("expected remediation proposal follow-up, got %+v", envelope.Proposals)
	}
}

// === TODO-04 (P1) skill注入 tests ===
//
// TODO-04 fixes a regression where the active (invoked) skill's body
// never reached the avatar LLM. Previously only invokedSkillName was
// threaded into the prompt; the skill's Body was discarded. The fix
// renders the body verbatim (multi-line preserved, no 1200-char cap)
// into the system prompt as an `invoked-skill: <name>` section.

func TestBuildLLMSummaryRequest_IncludesActiveSkillBodyVerbatim(t *testing.T) {
	skill := &skills.Definition{
		Listing: skills.Listing{
			Name:    "task-survey",
			Context: "Use before deciding whether to invoke a workflow.",
		},
		Body: "## Steps\n\n1. Read the user request.\n2. Check memory for prior context.\n3. Survey skill catalog.\n4. Decide: invoke, clarify, or pass-through.",
	}
	request := buildLLMSummaryRequest(prompt.Build("analyze"), planner.Build("analyze"), "", "", "", "task-survey", false, skill, PersonalityConfig{}, "")
	if !strings.Contains(request.SystemPrompt, "invoked-skill: task-survey") {
		t.Fatalf("expected invoked-skill marker in system prompt, got %q", request.SystemPrompt)
	}
	if !strings.Contains(request.SystemPrompt, "## Steps\n\n1. Read the user request.\n2. Check memory for prior context.\n3. Survey skill catalog.\n4. Decide: invoke, clarify, or pass-through.") {
		t.Fatalf("expected full multi-line body in system prompt, got %q", request.SystemPrompt)
	}
	// Context must also be present and multi-line.
	if !strings.Contains(request.SystemPrompt, "context:\nUse before deciding whether to invoke a workflow.") {
		t.Fatalf("expected context in system prompt with newlines, got %q", request.SystemPrompt)
	}
}

func TestBuildLLMSummaryRequest_OmitsActiveSkillSectionWhenNil(t *testing.T) {
	request := buildLLMSummaryRequest(prompt.Build("analyze"), planner.Build("analyze"), "", "", "", "", false, nil, PersonalityConfig{}, "")
	if strings.Contains(request.SystemPrompt, "invoked-skill:") {
		t.Fatalf("did not expect invoked-skill section when activeSkill is nil, got %q", request.SystemPrompt)
	}
}

func TestBuildLLMSummaryRequest_ActiveSkillNotTruncatedAt1200(t *testing.T) {
	// Construct a body longer than the legacy 1200-char cap.
	longLines := []string{}
	for i := 0; i < 30; i++ {
		longLines = append(longLines, fmt.Sprintf("Step %d: %s", i+1, strings.Repeat("x", 50)))
	}
	longBody := strings.Join(longLines, "\n")
	skill := &skills.Definition{
		Listing: skills.Listing{Name: "long-skill"},
		Body:    longBody,
	}
	request := buildLLMSummaryRequest(prompt.Build("analyze"), planner.Build("analyze"), "", "", "", "long-skill", false, skill, PersonalityConfig{}, "")
	// Every step line must appear in the system prompt, not just the
	// first ~20 of them (the legacy oneLine(1200) cap would have dropped
	// the rest).
	for _, line := range longLines {
		if !strings.Contains(request.SystemPrompt, line) {
			t.Fatalf("expected long-body line %q to survive, got %q", line, request.SystemPrompt)
		}
	}
}

func TestFormatActiveSkillSection_CapsAt16000Chars(t *testing.T) {
	huge := strings.Repeat("Z", 20000)
	skill := &skills.Definition{
		Listing: skills.Listing{Name: "huge-skill"},
		Body:    huge,
	}
	section := formatActiveSkillSection(skill)
	if !strings.Contains(section, "[truncated: active skill body exceeded 16000 chars]") {
		t.Fatalf("expected 16000-char truncation marker, got prefix %q", section[:min(200, len(section))])
	}
	if strings.Count(section, "Z") > 16100 {
		t.Fatalf("expected at most ~16000 Z's, got %d", strings.Count(section, "Z"))
	}
}

func TestFormatActiveSkillSection_NilSkillReturnsEmpty(t *testing.T) {
	if got := formatActiveSkillSection(nil); got != "" {
		t.Fatalf("expected empty for nil skill, got %q", got)
	}
}

func TestFormatActiveSkillSection_EmptyNameReturnsEmpty(t *testing.T) {
	skill := &skills.Definition{Listing: skills.Listing{Name: "  "}, Body: "rules"}
	if got := formatActiveSkillSection(skill); got != "" {
		t.Fatalf("expected empty for blank name, got %q", got)
	}
}

// === TODO-05 (P1) 触发面 tests ===
//
// TODO-05 fixes a regression where the caveman-commit / caveman-help /
// caveman-review skills were dead code. The legacy shouldInvokeSkill
// only matched the "survey" skill via an "analy/survey/refactor"
// keyword heuristic, so the other approved skills never auto-triggered.
// The fix expands the trigger surface to:
//   1. direct skill-name match (handles /caveman-commit invocations)
//   2. caveman-<action> action keyword match (handles "commit", "review",
//      "help" in natural language)
//   3. small Chinese phrase map (handles "提交", "审查", "帮助")
//   4. the legacy survey heuristic (preserved for backward compat)

func TestShouldInvokeSkill_DirectNameMatch(t *testing.T) {
	listing := skills.Listing{Name: "caveman-commit", Description: "commit message skill", WhenToUse: "drafting commits"}
	for _, request := range []string{
		"caveman-commit",
		"caveman commit",
		"please run caveman-commit on this",
		"/caveman-commit",
	} {
		if !shouldInvokeSkill(request, listing) {
			t.Fatalf("expected shouldInvokeSkill(%q, caveman-commit) = true", request)
		}
	}
}

func TestShouldInvokeSkill_CavemanActionKeywordMatch(t *testing.T) {
	cases := []struct {
		skillName string
		requests  []string
	}{
		{"caveman-commit", []string{"please draft a commit message", "write a commit for this diff", "commit message"}},
		{"caveman-help", []string{"show me caveman help", "I need help with caveman"}},
		{"caveman-review", []string{"review this PR", "code review please", "review the diff"}},
	}
	for _, c := range cases {
		listing := skills.Listing{Name: c.skillName, Description: c.skillName + " skill", WhenToUse: "auto-trigger on " + strings.TrimPrefix(c.skillName, "caveman-")}
		for _, request := range c.requests {
			if !shouldInvokeSkill(request, listing) {
				t.Fatalf("expected shouldInvokeSkill(%q, %s) = true", request, c.skillName)
			}
		}
	}
}

func TestShouldInvokeSkill_CavemanChinesePhraseMatch(t *testing.T) {
	cases := []struct {
		skillName string
		requests  []string
	}{
		{"caveman-commit", []string{"帮我写一个提交信息", "写一个 commit message"}},
		{"caveman-help", []string{"给我 caveman help 看看", "需要 caveman 的帮助"}},
		{"caveman-review", []string{"帮我审查一下这段代码", "代码审查"}},
	}
	for _, c := range cases {
		listing := skills.Listing{Name: c.skillName, Description: c.skillName + " skill", WhenToUse: "drafting commits"}
		for _, request := range c.requests {
			if !shouldInvokeSkill(request, listing) {
				t.Fatalf("expected shouldInvokeSkill(%q, %s) = true (Chinese variant)", request, c.skillName)
			}
		}
	}
}

func TestShouldInvokeSkill_NoMatchForUnrelatedRequest(t *testing.T) {
	listing := skills.Listing{Name: "caveman-commit", Description: "commit skill", WhenToUse: "drafting commits"}
	for _, request := range []string{
		"build the project",
		"what is the weather",
		"add a new endpoint to the API",
		"",
	} {
		if shouldInvokeSkill(request, listing) {
			t.Fatalf("expected shouldInvokeSkill(%q, caveman-commit) = false", request)
		}
	}
}

func TestShouldInvokeSkill_LegacySurveyHeuristicPreserved(t *testing.T) {
	// repo-survey skill with description mentioning "survey" must still
	// trigger on analy/survey/refactor requests — the previous behavior.
	listing := skills.Listing{
		Name:        "repo-survey",
		Description: "Survey the repository and report findings.",
		WhenToUse:   "Use for repo-survey workflows.",
	}
	for _, request := range []string{
		"analyze this repository",
		"please survey the codebase",
		"refactor the auth flow",
	} {
		if !shouldInvokeSkill(request, listing) {
			t.Fatalf("expected shouldInvokeSkill(%q, repo-survey) = true (legacy survey heuristic)", request)
		}
	}
	// And the survey skill must NOT match unrelated input.
	if shouldInvokeSkill("what time is it", listing) {
		t.Fatalf("expected survey skill to NOT trigger on unrelated request")
	}
}

func TestShouldInvokeSkill_EmptyInputsReturnFalse(t *testing.T) {
	listing := skills.Listing{Name: "caveman-commit", Description: "x", WhenToUse: "y"}
	if shouldInvokeSkill("", listing) {
		t.Fatalf("expected empty request to return false")
	}
	emptyName := skills.Listing{Name: "  ", Description: "x", WhenToUse: "y"}
	if shouldInvokeSkill("caveman commit", emptyName) {
		t.Fatalf("expected empty skill name to return false")
	}
}

func TestShouldInvokeSkill_NonCavemanSkillNameOnlyTriggersByExactName(t *testing.T) {
	// A non-caveman skill that is NOT tagged as a survey skill must
	// not auto-trigger on the bare word "survey" — the survey heuristic
	// gates on the description containing "survey".
	listing := skills.Listing{
		Name:        "build-fixer",
		Description: "Fixes build errors and CI failures.",
		WhenToUse:   "Use when the build is broken.",
	}
	// Direct name match works.
	if !shouldInvokeSkill("please run build-fixer", listing) {
		t.Fatalf("expected direct name match for build-fixer")
	}
	// Bare keyword "survey" does NOT trigger a non-survey-tagged skill.
	if shouldInvokeSkill("survey the codebase", listing) {
		t.Fatalf("expected bare 'survey' keyword to NOT trigger build-fixer (description has no 'survey')")
	}
	if shouldInvokeSkill("refactor the auth flow", listing) {
		t.Fatalf("expected bare 'refactor' keyword to NOT trigger build-fixer (description has no 'survey')")
	}
}
