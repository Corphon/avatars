package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptIntentLLMSpecMarksTopicScriptForLLMGeneration(t *testing.T) {
	originalAnalyzer := scriptIntentAnalyzer
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "hundred_chickens.py", Topic: "百钱买百鸡", Confidence: 96}, true
	}
	defer func() {
		scriptIntentAnalyzer = originalAnalyzer
	}()

	decision := classifyNaturalLanguageQuestion("帮我用python写一个脚本：公鸡每只五钱，母鸡每只三钱，小鸡每三只一钱，用一百钱买一百只鸡，问公鸡、母鸡、小鸡各买了多少只？", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "script --apply --llm hundred_chickens.py 帮我用python写一个脚本：公鸡每只五钱，母鸡每只三钱，小鸡每三只一钱，用一百钱买一百只鸡，问公鸡、母鸡、小鸡各买了多少只？" {
		t.Fatalf("expected LLM-topic script route, got %q", command)
	}
}

func TestScriptIntentLLMSpecLeavesGenericScriptDeterministic(t *testing.T) {
	originalAnalyzer := scriptIntentAnalyzer
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "hello.py", Topic: "", Confidence: 92}, true
	}
	defer func() {
		scriptIntentAnalyzer = originalAnalyzer
	}()

	decision := classifyNaturalLanguageQuestion("写一个简单脚本 hello.py", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "script --apply hello.py 写一个简单脚本 hello.py" {
		t.Fatalf("expected generic script route without --llm, got %q", command)
	}
}

func TestExplicitScriptWithConcreteRequirementsForcesLLM(t *testing.T) {
	originalAnalyzer := scriptIntentAnalyzer
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{}, false
	}
	defer func() {
		scriptIntentAnalyzer = originalAnalyzer
	}()

	decision := classifyNaturalLanguageQuestion("写一个python脚本 calc.py，包含 add(a,b)，并打印 add(2,3)", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "script --apply --llm calc.py 写一个python脚本 calc.py，包含 add(a,b)，并打印 add(2,3)" {
		t.Fatalf("expected concrete requirements to force llm script generation, got %q", command)
	}
}

func TestEditIntentLLMSpecRoutesToEditCommand(t *testing.T) {
	originalAnalyzer := editIntentAnalyzer
	editIntentAnalyzer = func(input string) (editIntentSpec, bool) {
		return editIntentSpec{
			Action:      "edit",
			Path:        "calc.py",
			Instruction: "add subtract(a, b) and print add and subtract results",
			Confidence:  95,
		}, true
	}
	defer func() {
		editIntentAnalyzer = originalAnalyzer
	}()

	decision := classifyNaturalLanguageQuestion("修改 calc.py：新增 subtract(a, b) 函数，返回 a - b，并让命令行同时打印 add 和 subtract 的结果", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "edit --apply calc.py add subtract(a, b) and print add and subtract results" {
		t.Fatalf("expected llm-backed edit route, got %q", command)
	}
}

func TestScriptApplyWritesPythonScript(t *testing.T) {
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
	// Use a specific topic (*hello* syntax) so the LLM path is triggered.
	// Stub the generator to avoid hitting a real LLM.
	original := scriptContentGenerator
	scriptContentGenerator = func(path, description string) (string, bool, error) {
		return "#!/usr/bin/env python3\n\ndef main() -> None:\n    print(\"Hello from hello\")\n\nif __name__ == \"__main__\":\n    main()\n", true, nil
	}
	defer func() { scriptContentGenerator = original }()

	output := captureRunOutput(t, []string{"script", "--apply", "hello.py", "写一个 *hello* 脚本"})
	content, err := os.ReadFile("hello.py")
	if err != nil {
		t.Fatalf("expected hello.py to be written: %v", err)
	}
	if !strings.Contains(string(content), "def main()") || !strings.Contains(string(content), "Hello from hello") {
		t.Fatalf("unexpected script content: %s", string(content))
	}
	if !strings.Contains(output, "Script mode: apply") || !strings.Contains(output, "Wrote:") || !strings.Contains(output, "Verifier:") {
		t.Fatalf("expected script apply output with verifier, got %s", output)
	}
}

func TestParseScriptCommandOptions_ReadsExpectedOutputFile(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "multiplication_table.expected.txt")
	want := "1*1=1\n2*2=4\n9*9=81\n"
	if err := os.WriteFile(sidecar, []byte(want), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	options, err := parseScriptCommandOptions([]string{
		"--apply",
		"--expected-output-file", sidecar,
		"multiplication_table.py",
		"用 python 写一个九九乘法表",
	})
	if err != nil {
		t.Fatalf("parse options failed: %v", err)
	}
	if !options.Apply {
		t.Fatalf("expected Apply=true")
	}
	if options.Path != "multiplication_table.py" {
		t.Fatalf("expected path multiplication_table.py, got %q", options.Path)
	}
	if !options.ExpectedOutputIsSet {
		t.Fatalf("expected ExpectedOutputIsSet=true")
	}
	if options.ExpectedOutput != want {
		t.Fatalf("expected expected output %q, got %q", want, options.ExpectedOutput)
	}
}

func TestParseScriptCommandOptions_ExpectedOutputFileMissingPath(t *testing.T) {
	_, err := parseScriptCommandOptions([]string{"--expected-output-file"})
	if err == nil {
		t.Fatalf("expected error for missing path arg")
	}
	if !strings.Contains(err.Error(), "--expected-output-file requires a path") {
		t.Fatalf("expected path-required error, got %v", err)
	}
}

func TestParseScriptCommandOptions_ExpectedOutputFileUnreadable(t *testing.T) {
	_, err := parseScriptCommandOptions([]string{
		"--expected-output-file", filepath.Join(t.TempDir(), "does_not_exist.txt"),
		"foo.py",
	})
	if err == nil {
		t.Fatalf("expected error for unreadable sidecar")
	}
	if !strings.Contains(err.Error(), "read --expected-output-file") {
		t.Fatalf("expected read-error, got %v", err)
	}
}

func TestParseScriptCommandOptions_NoExpectedOutputFlagLeavesFieldEmpty(t *testing.T) {
	options, err := parseScriptCommandOptions([]string{"--apply", "hello.py", "say hi"})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if options.ExpectedOutputIsSet {
		t.Fatalf("expected ExpectedOutputIsSet=false when flag absent")
	}
	if options.ExpectedOutput != "" {
		t.Fatalf("expected empty ExpectedOutput, got %q", options.ExpectedOutput)
	}
}

func TestScriptScaffoldContentWithOptions_PropagatesExpectedOutput(t *testing.T) {
	want := "9x9=81"
	result, err := scriptScaffoldContentWithOptions("multiplication_table.py", "用 python 写一个九九乘法表", want, true)
	// forceLLM=true will try scriptContentGenerator which is the real LLM call.
	// To avoid hitting the network, we override the generator below.
	if err == nil {
		if result.ExpectedOutput != want {
			t.Fatalf("expected ExpectedOutput=%q in result, got %q", want, result.ExpectedOutput)
		}
	}
	// Re-test with a stubbed generator to keep the test offline.
	original := scriptContentGenerator
	scriptContentGenerator = func(path, description string) (string, bool, error) {
		return "print('9x9=81')\n", true, nil
	}
	defer func() { scriptContentGenerator = original }()
	result, err = scriptScaffoldContentWithOptions("multiplication_table.py", "用 python 写一个九九乘法表", want, true)
	if err != nil {
		t.Fatalf("scaffold failed: %v", err)
	}
	if result.Source != "llm" {
		t.Fatalf("expected source=llm, got %q", result.Source)
	}
	if result.ExpectedOutput != want {
		t.Fatalf("expected ExpectedOutput=%q in result, got %q", want, result.ExpectedOutput)
	}
}

func TestScriptScaffoldContentWithOptions_DeterministicTemplateRefused(t *testing.T) {
	// When the description is generic (no specific topic) and forceLLM is
	// false, the function must refuse to produce a meaningless "Hello from
	// {name}" scaffold. The user should be told to supply a concrete topic.
	_, err := scriptScaffoldContentWithOptions("hello.py", "write a simple script hello.py", "any-expected-text", false)
	if err == nil {
		t.Fatalf("expected error for generic topic without LLM, got nil")
	}
	if !strings.Contains(err.Error(), "no specific script topic") && !strings.Contains(err.Error(), "wrap your topic") {
		t.Fatalf("expected topic-guidance error, got: %v", err)
	}
}

func TestScriptScaffoldContentWithOptions_SpecificTopicStillWorks(t *testing.T) {
	original := scriptContentGenerator
	scriptContentGenerator = func(path, description string) (string, bool, error) {
		return "print('9x9=81')\n", true, nil
	}
	defer func() { scriptContentGenerator = original }()

	result, err := scriptScaffoldContentWithOptions("multiplication_table.py", "write a *multiplication table* script", "", false)
	if err != nil {
		t.Fatalf("scaffold with specific topic failed: %v", err)
	}
	if result.Source != "llm" {
		t.Fatalf("expected source=llm, got %q", result.Source)
	}
}

func TestScriptScaffoldContentWithOptions_PropagatesGeneratorError(t *testing.T) {
	original := scriptContentGenerator
	scriptContentGenerator = func(path, description string) (string, bool, error) {
		return "", false, errors.New("agnes: no API key configured (provider=agnes)")
	}
	defer func() { scriptContentGenerator = original }()

	_, err := scriptScaffoldContentWithOptions("star.py", "画一个五角星", "", true)
	if err == nil {
		t.Fatal("expected generator error to surface")
	}
	if !strings.Contains(err.Error(), "agnes: no API key configured") {
		t.Fatalf("expected real LLM error, got: %v", err)
	}
}

func TestScriptScaffoldContentWithOptions_ForceLLMWithGenericDescription(t *testing.T) {
	original := scriptContentGenerator
	scriptContentGenerator = func(path, description string) (string, bool, error) {
		return "print('hello')\n", true, nil
	}
	defer func() { scriptContentGenerator = original }()

	result, err := scriptScaffoldContentWithOptions("hello.py", "write a simple script", "", true)
	if err != nil {
		t.Fatalf("scaffold with forceLLM failed: %v", err)
	}
	if result.Source != "llm" {
		t.Fatalf("expected source=llm, got %q", result.Source)
	}
}

func TestScriptIntentAnalyzerFailureReturnsClarify(t *testing.T) {
	// When looksLikeSimpleScriptRequest matches but the LLM spec analyzer
	// returns a non-"script" action, buildNaturalLanguageTaskCommandWithLLM
	// must return nil (triggering clarify) instead of falling through to a
	// generic script --apply that will fail at scaffold time.
	originalScript := scriptIntentAnalyzer
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{Action: "not_script"}, true
	}
	defer func() { scriptIntentAnalyzer = originalScript }()

	cmd, summary := buildNaturalLanguageTaskCommandWithLLM("写脚本", replCommandOptions{}, true)
	if len(cmd) != 0 {
		t.Fatalf("expected nil command for failed script spec, got %v", cmd)
	}
	if !strings.Contains(summary, "clarify") && !strings.Contains(summary, "could not determine") {
		t.Fatalf("expected clarify summary, got %q", summary)
	}
}

func TestScriptIntentAnalyzerParseFailureReturnsClarify(t *testing.T) {
	// When the LLM spec analyzer fails to parse (ok=false),
	// buildNaturalLanguageTaskCommandWithLLM must return nil.
	originalScript := scriptIntentAnalyzer
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{}, false
	}
	defer func() { scriptIntentAnalyzer = originalScript }()

	cmd, summary := buildNaturalLanguageTaskCommandWithLLM("写脚本", replCommandOptions{}, true)
	if len(cmd) != 0 {
		t.Fatalf("expected nil command for failed script spec parse, got %v", cmd)
	}
	if !strings.Contains(summary, "clarify") && !strings.Contains(summary, "could not determine") {
		t.Fatalf("expected clarify summary, got %q", summary)
	}
}

func TestEditIntentAnalyzerFailureReturnsClarify(t *testing.T) {
	// When the LLM edit spec analyzer returns action=="edit" but cannot
	// produce a valid command, buildNaturalLanguageTaskCommandWithLLM must
	// return nil (triggering clarify).
	originalEdit := editIntentAnalyzer
	editIntentAnalyzer = func(input string) (editIntentSpec, bool) {
		return editIntentSpec{Action: "edit", Path: "", Instruction: ""}, true
	}
	defer func() { editIntentAnalyzer = originalEdit }()

	cmd, summary := buildNaturalLanguageTaskCommandWithLLM("修改 main.go", replCommandOptions{}, true)
	if len(cmd) != 0 {
		t.Fatalf("expected nil command for failed edit spec, got %v", cmd)
	}
	if !strings.Contains(summary, "clarify") && !strings.Contains(summary, "could not determine") {
		t.Fatalf("expected clarify summary, got %q", summary)
	}
}

func TestScriptIntentAnalyzerSuccessStillProducesCommand(t *testing.T) {
	// When the LLM spec analyzer returns a valid script spec,
	// buildNaturalLanguageTaskCommandWithLLM must produce a command.
	originalScript := scriptIntentAnalyzer
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "hello.py", Topic: "greeting"}, true
	}
	defer func() { scriptIntentAnalyzer = originalScript }()

	cmd, summary := buildNaturalLanguageTaskCommandWithLLM("写脚本", replCommandOptions{}, true)
	if len(cmd) == 0 {
		t.Fatalf("expected command for successful script spec, got nil (summary=%q)", summary)
	}
	if cmd[0] != "script" {
		t.Fatalf("expected script command, got %v", cmd)
	}
}

func TestScriptCommandFromLLMSpec_WritesExpectedOutputSidecar(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	spec := scriptIntentSpec{
		Action:         "script",
		Language:       "python",
		Filename:       "multiplication_table.py",
		Topic:          "9x9 multiplication table",
		ExpectedOutput: "1*1=1\n9x9=81\n",
		Confidence:     95,
	}
	command := scriptCommandFromLLMSpec("用 python 写一个九九乘法表", spec)
	if len(command) == 0 {
		t.Fatalf("expected non-empty command")
	}
	// Find --expected-output-file and the next arg.
	flagIdx := -1
	for i, arg := range command {
		if arg == "--expected-output-file" {
			flagIdx = i
			break
		}
	}
	if flagIdx < 0 || flagIdx+1 >= len(command) {
		t.Fatalf("expected --expected-output-file flag in command, got %v", command)
	}
	sidecar := command[flagIdx+1]
	if !strings.HasSuffix(sidecar, ".expected.txt") {
		t.Fatalf("expected sidecar path to end with .expected.txt, got %q", sidecar)
	}
	content, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("expected sidecar file to be written: %v", err)
	}
	if string(content) != spec.ExpectedOutput {
		t.Fatalf("expected sidecar content %q, got %q", spec.ExpectedOutput, string(content))
	}
}

func TestScriptCommandFromLLMSpec_NoExpectedOutput_OmitsFlagAndSidecar(t *testing.T) {
	dir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	spec := scriptIntentSpec{
		Action:     "script",
		Language:   "python",
		Filename:   "hello.py",
		Topic:      "",
		Confidence: 80,
	}
	command := scriptCommandFromLLMSpec("write a simple script hello.py", spec)
	if len(command) == 0 {
		t.Fatalf("expected non-empty command")
	}
	for _, arg := range command {
		if arg == "--expected-output-file" {
			t.Fatalf("did not expect --expected-output-file flag when ExpectedOutput is empty, got %v", command)
		}
	}
	// No .expected.txt files should exist in the temp dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".expected.txt") {
			t.Fatalf("did not expect any sidecar files, found %q", e.Name())
		}
	}
}

func TestScriptIntentSystemPrompt_RequestsExpectedOutput(t *testing.T) {
	prompt := scriptIntentSystemPrompt()
	if !strings.Contains(prompt, "expected_output") {
		t.Fatalf("expected prompt to mention expected_output field, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "deterministic") {
		t.Fatalf("expected prompt to gate expected_output on determinism, got:\n%s", prompt)
	}
}

func TestScriptIntentSystemPromptContainsRoutingGuidance(t *testing.T) {
	prompt := scriptIntentSystemPrompt()
	// Verify the prompt contains routing guidance for creation requests.
	routingKeywords := []string{
		"action=script",
		"Routing guidance",
		"game",
		"写一个游戏",
		"Chinese creation verbs",
	}
	for _, kw := range routingKeywords {
		if !strings.Contains(prompt, kw) {
			t.Fatalf("scriptIntentSystemPrompt missing routing keyword %q", kw)
		}
	}
	// Verify the prompt tells LLM NOT to classify game creation as not_script.
	if !strings.Contains(prompt, "NOT not_script") && !strings.Contains(prompt, "not_script") {
		t.Fatal("scriptIntentSystemPrompt should mention not_script misclassification risk")
	}
}

func TestEditIntentSystemPromptContainsRoutingGuidance(t *testing.T) {
	prompt := editIntentSystemPrompt()
	// Verify the prompt tells LLM not to classify creation requests as edit.
	routingKeywords := []string{
		"not_edit",
		"CREATE",
		"写一个游戏",
	}
	for _, kw := range routingKeywords {
		if !strings.Contains(prompt, kw) {
			t.Fatalf("editIntentSystemPrompt missing routing keyword %q", kw)
		}
	}
}
