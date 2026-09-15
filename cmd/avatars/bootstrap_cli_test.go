package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/tasks"
)

func TestBootstrapDryRunDoesNotWriteFiles(t *testing.T) {
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
	output := captureRunOutput(t, []string{"bootstrap", "--name", "Demo App"})
	if !strings.Contains(output, "Bootstrap mode: dry-run") || !strings.Contains(output, "Generator: deterministic") || !strings.Contains(output, "cmd/demo-app/main.go") {
		t.Fatalf("expected dry-run scaffold preview, got %s", output)
	}
	if _, err := os.Stat("README.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected dry-run not to write README.md, stat err=%v", err)
	}
}

func TestBootstrapApplyWritesGoCLIScaffoldAndRunsVerifier(t *testing.T) {
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
	output := captureRunOutput(t, []string{"bootstrap", "--apply", "--name", "Demo App", "--module", "example.com/demo-app"})
	for _, expected := range []string{"Bootstrap mode: apply", "Generator: deterministic", "Wrote:", "README.md", "docs/architecture.md", "bootstrap_summary.md", "cmd/demo-app/main.go", "Verifier: PASS", "Command: go test ./..."} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected bootstrap output to contain %q, got %s", expected, output)
		}
	}
	for _, path := range []string{"README.md", filepath.Join("docs", "architecture.md"), "coding_plan.md", "process_record.md", "go.mod", "bootstrap_summary.md", filepath.Join("cmd", "demo-app", "main.go"), filepath.Join("cmd", "demo-app", "main_test.go")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected scaffold file %s: %v", path, err)
		}
	}
	goMod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod failed: %v", err)
	}
	if !strings.Contains(string(goMod), "module example.com/demo-app") {
		t.Fatalf("expected module override, got %s", string(goMod))
	}
	summary, err := os.ReadFile("bootstrap_summary.md")
	if err != nil {
		t.Fatalf("read bootstrap summary failed: %v", err)
	}
	if !strings.Contains(string(summary), "Stack: go-cli") || !strings.Contains(string(summary), "cmd/demo-app/main.go") {
		t.Fatalf("expected bootstrap summary to describe scaffold, got %s", string(summary))
	}
}

func TestBootstrapDryRunSupportsPythonCLIScaffold(t *testing.T) {
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
	output := captureRunOutput(t, []string{"bootstrap", "--stack", "python-cli", "--name", "Ledger CLI"})
	for _, expected := range []string{"Stack: python-cli", "main.py", "test_main.py", "pyproject.toml"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected python bootstrap preview to contain %q, got %s", expected, output)
		}
	}
}

func TestBootstrapDryRunSupportsNodeCLIScaffold(t *testing.T) {
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
	output := captureRunOutput(t, []string{"bootstrap", "--stack", "node-cli", "--name", "Ledger CLI"})
	for _, expected := range []string{"Stack: node-cli", "package.json", "src/index.js", "test/index.test.js"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected node bootstrap preview to contain %q, got %s", expected, output)
		}
	}
}

func TestBootstrapDryRunSupportsRustCLIScaffold(t *testing.T) {
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
	output := captureRunOutput(t, []string{"bootstrap", "--stack", "rust-cli", "--name", "Ledger CLI"})
	for _, expected := range []string{"Stack: rust-cli", "Cargo.toml", "src/main.rs"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected rust bootstrap preview to contain %q, got %s", expected, output)
		}
	}
}

func TestBootstrapApplyNonEmptyWorkspacePrintsRemediation(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Existing\n"), 0o644); err != nil {
		t.Fatalf("write existing README failed: %v", err)
	}
	output, runErr := captureRunOutputAllowError(t, []string{"bootstrap", "--apply", "--name", "demo"})
	if runErr != nil {
		t.Fatalf("expected non-empty bootstrap to print remediation without error, err=%v output=%s", runErr, output)
	}
	for _, expected := range []string{"Bootstrap blocked: project root is not empty.", "Blocking entries:", "README.md", "Safe options:", "suggested empty target directory: demo"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected remediation output to contain %q, got %s", expected, output)
		}
	}
}

func TestBootstrapWorkspaceEmptyIgnoresBundledAvatarsDirectory(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, "avatars", "bin"), 0o755); err != nil {
		t.Fatalf("create avatars bundle dir failed: %v", err)
	}
	empty, entries, err := bootstrapWorkspaceEmpty(tempDir)
	if err != nil {
		t.Fatalf("bootstrap empty check failed: %v", err)
	}
	if !empty || len(entries) != 0 {
		t.Fatalf("expected avatars bundle dir to be ignored, empty=%t entries=%v", empty, entries)
	}
}

func TestBootstrapNaturalLanguageEmptyProjectRoutesToBootstrap(t *testing.T) {
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
	decision := classifyNaturalLanguageQuestionWithoutLLM("帮我搭一个空项目并生成代码", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected bootstrap safe run, got %+v", decision)
	}
	command := strings.Join(decision.Command, " ")
	if !strings.HasPrefix(command, "bootstrap --apply --name ") {
		t.Fatalf("expected bootstrap command, got %q", command)
	}
	if !strings.Contains(decision.Reason, "bootstrap") {
		t.Fatalf("expected bootstrap reason, got %q", decision.Reason)
	}
}

func TestBootstrapNaturalLanguageEmptyProjectWithAvatarsBundleRoutesToBootstrap(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.MkdirAll(filepath.Join(tempDir, "avatars", "bin"), 0o755); err != nil {
		t.Fatalf("create avatars bundle dir failed: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	decision := classifyNaturalLanguageQuestionWithoutLLM("帮我搭一个叫 ledger-cli 的空项目", replCommandOptions{}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "bootstrap --apply --name ledger-cli" {
		t.Fatalf("expected bootstrap despite avatars tool dir, got %q", command)
	}
}

func TestBootstrapNaturalLanguageExtractsNameAndModule(t *testing.T) {
	for _, tc := range []struct {
		name           string
		input          string
		expectedName   string
		expectedModule string
	}{
		{name: "chinese called", input: "帮我搭一个叫 ledger-cli 的空项目并生成代码", expectedName: "ledger-cli"},
		{name: "chinese named", input: "创建一个名为 Report_App 的项目", expectedName: "report-app"},
		{name: "english project module", input: "create project ledger-cli module example.com/ledger-cli", expectedName: "ledger-cli", expectedModule: "example.com/ledger-cli"},
		{name: "explicit flags", input: "bootstrap --name ops-tool --module example.com/ops-tool", expectedName: "ops-tool", expectedModule: "example.com/ops-tool"},
		{name: "python stack", input: "帮我用 python 搭一个叫 ledger-cli 的空项目", expectedName: "ledger-cli"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := bootstrapSpecFromNaturalLanguage(tc.input)
			if spec.Name != tc.expectedName {
				t.Fatalf("expected name %q, got %+v", tc.expectedName, spec)
			}
			if spec.Module != tc.expectedModule {
				t.Fatalf("expected module %q, got %+v", tc.expectedModule, spec)
			}
		})
	}
}

func TestBootstrapNaturalLanguageExtractsPythonStack(t *testing.T) {
	spec := bootstrapSpecFromNaturalLanguage("帮我用python搭一个叫 ledger-cli 的空项目")
	if spec.Stack != "python-cli" {
		t.Fatalf("expected python stack, got %+v", spec)
	}
}

func TestBootstrapNaturalLanguageExtractsNodeStack(t *testing.T) {
	spec := bootstrapSpecFromNaturalLanguage("帮我用 node 搭一个叫 ledger-cli 的空项目")
	if spec.Stack != "node-cli" {
		t.Fatalf("expected node stack, got %+v", spec)
	}
}

func TestBootstrapNaturalLanguageExtractsRustStack(t *testing.T) {
	spec := bootstrapSpecFromNaturalLanguage("帮我用 rust 搭一个叫 ledger-cli 的空项目")
	if spec.Stack != "rust-cli" {
		t.Fatalf("expected rust stack, got %+v", spec)
	}
}

func TestBootstrapNaturalLanguageCommandIncludesExtractedNameAndModule(t *testing.T) {
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
	decision := classifyNaturalLanguageQuestionWithoutLLM("create project ledger-cli module example.com/ledger-cli", replCommandOptions{}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "bootstrap --apply --name ledger-cli --module example.com/ledger-cli" {
		t.Fatalf("expected extracted bootstrap command, got %q", command)
	}
}

func TestBootstrapLanguageFollowUpUsesLatestAnswerContext(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: defaultREPLTaskID, Title: defaultREPLTaskID})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	answerContent := "The request asks to bootstrap an empty project named `ledger-cli`."
	line := `{"type":"llm.completed","payload":{"content":` + fmt.Sprintf("%q", answerContent) + `}}` + "\n"
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "bootstrap question asked"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}
	decision := classifyNaturalLanguageQuestionWithoutLLM("用python", replCommandOptions{}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "bootstrap --apply --name ledger-cli --stack python-cli" {
		t.Fatalf("expected python bootstrap follow-up, got %q", command)
	}
}

func TestBootstrapLanguageFollowUpUsesBootstrapContextArtifact(t *testing.T) {
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
	if err := persistBootstrapContext(".", bootstrapCommandOptions{Name: "ledger-cli", Module: "ledger-cli", Stack: "go-cli"}); err != nil {
		t.Fatalf("persist bootstrap context failed: %v", err)
	}

	decision := classifyNaturalLanguageQuestionWithoutLLM("用python", replCommandOptions{}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "bootstrap --apply --name ledger-cli --stack python-cli" {
		t.Fatalf("expected python bootstrap follow-up from context artifact, got %q", command)
	}
}

func TestBootstrapRequestWithListCommandDoesNotRouteToProjectFileList(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join("avatars", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir avatars bin failed: %v", err)
	}

	input := "帮我搭一个用 Python 写的 todo-cli 空项目：支持 add/list/done 三个命令，保存到本地 json 文件，带 README、架构说明和最小测试"
	decision := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if decision.Kind != naturalLanguageDecisionSafeRun || !strings.HasPrefix(command, "bootstrap --apply --name todo-cli") || !strings.Contains(command, "--stack python-cli") || !strings.Contains(command, "--spec") {
		t.Fatalf("expected bootstrap safe run, not local file-list answer, decision=%+v command=%q", decision, command)
	}
}

func TestBootstrapIntentLLMSpecRoutesCustomScaffold(t *testing.T) {
	originalAnalyzer := bootstrapIntentAnalyzer
	bootstrapIntentAnalyzer = func(input string) (bootstrapIntentSpec, bool) {
		return bootstrapIntentSpec{
			Action:     "bootstrap",
			Name:       "todo-cli",
			Module:     "todo-cli",
			Stack:      "python-cli",
			Custom:     true,
			Confidence: 96,
		}, true
	}
	defer func() {
		bootstrapIntentAnalyzer = originalAnalyzer
	}()

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
	input := "帮我搭一个用 Python 写的 todo-cli 空项目：支持 add/list/done 三个命令，保存到本地 json 文件，带 README、架构说明和最小测试"
	decision := classifyNaturalLanguageQuestion(input, replCommandOptions{}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if !strings.HasPrefix(command, "bootstrap --apply --name todo-cli --stack python-cli --module todo-cli --spec ") {
		t.Fatalf("expected custom bootstrap spec route, got %q", command)
	}
	if !strings.Contains(command, "add/list/done") {
		t.Fatalf("expected original requirements to be preserved in --spec, got %q", command)
	}
}

func TestBootstrapSpecWritesLLMGeneratedProject(t *testing.T) {
	originalGenerator := bootstrapFilesGenerator
	bootstrapFilesGenerator = func(options bootstrapCommandOptions) ([]bootstrapScaffoldFile, bool, error) {
		if options.Name != "todo-cli" || options.Stack != "python-cli" || !strings.Contains(options.Spec, "add/list/done") {
			t.Fatalf("unexpected bootstrap options: %+v", options)
		}
		return []bootstrapScaffoldFile{
			{Path: "README.md", Content: "# todo-cli\n\nSupports add/list/done.\n"},
			{Path: filepath.Join("docs", "architecture.md"), Content: "# Architecture\n\nJSON-backed CLI.\n"},
			{Path: "main.py", Content: "def main() -> None:\n    print('todo-cli')\n\n\nif __name__ == '__main__':\n    main()\n"},
			{Path: "test_main.py", Content: "import unittest\n\n\nclass TodoTest(unittest.TestCase):\n    def test_placeholder(self):\n        self.assertTrue(True)\n\n\nif __name__ == '__main__':\n    unittest.main()\n"},
		}, true, nil
	}
	defer func() {
		bootstrapFilesGenerator = originalGenerator
	}()

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
	output := captureRunOutput(t, []string{"bootstrap", "--apply", "--name", "todo-cli", "--stack", "python-cli", "--module", "todo-cli", "--spec", "支持 add/list/done 三个命令"})
	if !strings.Contains(output, "Generator: llm") || !strings.Contains(output, "Wrote:") || !strings.Contains(output, "README.md") || !strings.Contains(output, "Verifier: PASS") {
		t.Fatalf("expected LLM-generated bootstrap output, got %s", output)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README failed: %v", err)
	}
	if !strings.Contains(string(readme), "add/list/done") {
		t.Fatalf("expected generated README to preserve requirements, got %s", string(readme))
	}
}

func TestValidateBootstrapGeneratedFilesAllowsEmptyPythonInit(t *testing.T) {
	files := []bootstrapScaffoldFile{
		{Path: "README.md", Content: "# Demo\n"},
		{Path: filepath.Join("cli", "__init__.py"), Content: "\n"},
		{Path: filepath.Join("cli", "main.py"), Content: "def main():\n    pass\n"},
	}
	if err := validateBootstrapGeneratedFiles(files); err != nil {
		t.Fatalf("expected empty __init__.py to be allowed, got %v", err)
	}
}

func TestValidateBootstrapGeneratedFilesRejectsOtherEmptyFiles(t *testing.T) {
	files := []bootstrapScaffoldFile{
		{Path: "README.md", Content: "# Demo\n"},
		{Path: filepath.Join("cli", "main.py"), Content: "\n"},
	}
	if err := validateBootstrapGeneratedFiles(files); err == nil || !strings.Contains(err.Error(), "empty content") {
		t.Fatalf("expected empty non-init file rejection, got %v", err)
	}
}

func TestBootstrapPythonVerifierDiscoversTestsDirectory(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tempDir, "tests"), 0o755); err != nil {
		t.Fatalf("mkdir tests failed: %v", err)
	}
	label, args := bootstrapPythonVerifierCommand(tempDir, "python")
	if label != "python -m unittest discover -s tests" {
		t.Fatalf("unexpected label: %q", label)
	}
	if strings.Join(args, " ") != "-m unittest discover -s tests" {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestPythonVerifierEnvIncludesSrcLayout(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tempDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src failed: %v", err)
	}
	env := pythonVerifierEnv(tempDir)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "PYTHONPATH=") {
		t.Fatalf("expected PYTHONPATH in verifier env, got %v", env)
	}
	if !strings.Contains(joined, filepath.Join(tempDir, "src")) {
		t.Fatalf("expected src path in verifier env, got %v", env)
	}
}

func TestBootstrapPythonPromptRequiresWindowsSafeDiscoverableTests(t *testing.T) {
	prompt := bootstrapGenerationSystemPrompt(bootstrapCommandOptions{Stack: "python-cli"})
	for _, expected := range []string{
		"python -m unittest discover -s tests",
		"malformed, invalid, or wrong-shaped JSON files",
		"JSONDecodeError/ValueError",
		"NamedTemporaryFile",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected Python bootstrap prompt to contain %q, got %s", expected, prompt)
		}
	}
}

func TestIntentExecutionCueReflectsBootstrapAndDefaultMode(t *testing.T) {
	// Bootstrap with English input → English cue
	bootstrapCue := intentExecutionCue("scaffold a new project", []string{"bootstrap", "--apply", "--name", "ledger-cli"})
	if !strings.Contains(strings.ToLower(bootstrapCue), "scaffold") {
		t.Fatalf("expected English bootstrap cue, got %q", bootstrapCue)
	}
	// Script with Chinese input still uses English cue (user-facing strings are English-only).
	scriptCue := intentExecutionCue("写一个Python脚本", []string{"script", "--apply", "hello.py"})
	if !strings.Contains(scriptCue, "script") && !strings.Contains(scriptCue, "Generating") && !strings.Contains(scriptCue, "write") {
		t.Fatalf("expected English script cue, got %q", scriptCue)
	}
	// Default mode with Chinese input still uses English cue
	defaultCue := intentExecutionCue("写一个简单脚本 hello.py", []string{"run", "--new-task", "--permission-mode", "default", "写一个简单脚本 hello.py"})
	if !strings.Contains(defaultCue, "code") && !strings.Contains(defaultCue, "plan") && !strings.Contains(defaultCue, "confirm") {
		t.Fatalf("expected English default cue, got %q", defaultCue)
	}
	// Plan mode with English input → English cue
	planCue := intentExecutionCue("analyze the project", []string{"run", "--new-task", "--permission-mode", "plan", "analyze the project"})
	if !strings.Contains(planCue, "read-only") {
		t.Fatalf("expected English plan cue, got %q", planCue)
	}
	acceptCue := intentExecutionCue("创建一个纯 Go 开源库 retrybudget", []string{"run", "--new-task", "--permission-mode", "acceptEdits", "创建一个纯 Go 开源库 retrybudget"})
	if strings.Contains(acceptCue, "read-only") {
		t.Fatalf("F97: acceptEdits must not print read-only cue, got %q", acceptCue)
	}
	lowerAccept := strings.ToLower(acceptCue)
	if strings.Contains(lowerAccept, "confirm") || strings.Contains(lowerAccept, "ask before") {
		t.Fatalf("F100: acceptEdits must not promise ask/confirm, got %q", acceptCue)
	}
	if !strings.Contains(acceptCue, "write") && !strings.Contains(acceptCue, "implement") && !strings.Contains(acceptCue, "code") {
		t.Fatalf("F100: acceptEdits should use write/verify cue, got %q", acceptCue)
	}
}

func TestBootstrapNaturalLanguageNonEmptyProjectDoesNotRouteToBootstrap(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Existing\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	decision := classifyNaturalLanguageQuestionWithoutLLM("帮我搭一个空项目并生成代码", replCommandOptions{}, naturalLanguageContextREPL)
	if len(decision.Command) > 0 && decision.Command[0] == "bootstrap" {
		t.Fatalf("expected non-empty project not to route to bootstrap, got %+v", decision)
	}
}
