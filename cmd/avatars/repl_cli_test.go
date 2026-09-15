package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	memstore "avatars/internal/memory"
	"avatars/internal/tasks"
)

func TestParseREPLCommandOptions_Task(t *testing.T) {
	options, err := parseREPLCommandOptions([]string{"--task", "demo-task"})
	if err != nil {
		t.Fatalf("parse repl command options failed: %v", err)
	}
	if options.TaskID != "demo-task" {
		t.Fatalf("expected task id demo-task, got %q", options.TaskID)
	}
}

func TestParseREPLCommandOptions_Display(t *testing.T) {
	options, err := parseREPLCommandOptions([]string{"--display", "verbose"})
	if err != nil {
		t.Fatalf("parse repl display failed: %v", err)
	}
	if !options.Verbose {
		t.Fatalf("expected verbose display, got %+v", options)
	}
	options, err = parseREPLCommandOptions([]string{"--display", "compact"})
	if err != nil {
		t.Fatalf("parse repl compact display failed: %v", err)
	}
	if options.Verbose {
		t.Fatalf("expected compact display, got %+v", options)
	}
	if _, err := parseREPLCommandOptions([]string{"--display", "loud"}); err == nil || !strings.Contains(err.Error(), "compact or verbose") {
		t.Fatalf("expected invalid display error, got %v", err)
	}
}

func TestParseREPLCommandOptions_RejectsTaskWithNewTask(t *testing.T) {
	_, err := parseREPLCommandOptions([]string{"--task", "demo-task", "--new-task"})
	if err == nil || !strings.Contains(err.Error(), "--task cannot be combined with --new-task") {
		t.Fatalf("expected task/new-task conflict, got %v", err)
	}
}

func TestREPLVerboseCommandTogglesDisplay(t *testing.T) {
	var output bytes.Buffer
	options := replCommandOptions{}
	handled, err := handleREPLVerboseCommand("/verbose", &output, &options)
	if err != nil || !handled {
		t.Fatalf("expected verbose status command handled, handled=%t err=%v", handled, err)
	}
	if !strings.Contains(output.String(), "Display: compact") {
		t.Fatalf("expected compact status, got %s", output.String())
	}
	output.Reset()
	handled, err = handleREPLVerboseCommand("/verbose on", &output, &options)
	if err != nil || !handled || !options.Verbose {
		t.Fatalf("expected verbose on, handled=%t options=%+v err=%v", handled, options, err)
	}
	if !strings.Contains(output.String(), "Display: verbose") {
		t.Fatalf("expected verbose cue, got %s", output.String())
	}
	output.Reset()
	handled, err = handleREPLVerboseCommand("/verbose off", &output, &options)
	if err != nil || !handled || options.Verbose {
		t.Fatalf("expected verbose off, handled=%t options=%+v err=%v", handled, options, err)
	}
	if !strings.Contains(output.String(), "Display: compact") {
		t.Fatalf("expected compact cue, got %s", output.String())
	}
}

func TestREPLHelpAndExit(t *testing.T) {
	var output bytes.Buffer
	err := runREPLWithIO(strings.NewReader("/help\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if !strings.Contains(content, "Avatars REPL started") {
		t.Fatalf("expected repl start cue, got %s", content)
	}
	if !strings.Contains(content, "/load <path>") {
		t.Fatalf("expected repl help, got %s", content)
	}
	if !strings.Contains(content, "Exiting Avatars REPL.") {
		t.Fatalf("expected repl exit cue, got %s", content)
	}
	if !strings.Contains(content, "/history") {
		t.Fatalf("expected repl history help, got %s", content)
	}
	if !strings.Contains(content, "/verbose on") || !strings.Contains(content, "/verbose off") {
		t.Fatalf("expected repl verbose help, got %s", content)
	}
}

func TestREPLDirectAnswerKeepsSoftStatusWhenDefaultTaskWorkspaceIsMissing(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n\nA small demo project.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("这个项目，你觉得如何？\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if !strings.Contains(content, "Project opinion:") || !strings.Contains(content, "Project overview:") {
		t.Fatalf("expected direct project answer, got %s", content)
	}
	if strings.Contains(content, "REPL status: task=repl-session unavailable") || strings.Contains(content, "no workspace yet; routed question handled without task state") {
		t.Fatalf("expected routed direct answer to stay quiet about missing task state, got %s", content)
	}
}

func TestREPLAtFileRoutesToIntentFromFile(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("unroutable task plan\n"), 0o644); err != nil {
		t.Fatalf("write plan file failed: %v", err)
	}
	var output bytes.Buffer
	err := runREPLWithIO(strings.NewReader("@"+planPath+" 继续分析项目\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	if !strings.Contains(output.String(), "Intent input file loaded:") {
		t.Fatalf("expected at-file cue, got %s", output.String())
	}
}

func TestREPLAtFileQuestionAnswersAttachedFileWithoutTaskRun(t *testing.T) {
	tempDir := t.TempDir()
	docPath := filepath.Join(tempDir, "Milestone_Review.md")
	if err := os.WriteFile(docPath, []byte("# Milestone Review\n\nTracks release review risks.\n"), 0o644); err != nil {
		t.Fatalf("write doc failed: %v", err)
	}
	var output bytes.Buffer
	err := runREPLWithIO(strings.NewReader("@"+docPath+" 这个文件是干啥的\n那刚才的文件是干啥的\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if strings.Contains(content, "Executing: avatars run") || strings.Contains(content, "Avatar Planner") {
		t.Fatalf("expected attached file question to avoid task run, got %s", content)
	}
	if strings.Count(content, "File context:") < 2 || !strings.Contains(content, "Milestone Review") || !strings.Contains(content, "Tracks release review risks.") {
		t.Fatalf("expected attached file summary and follow-up reuse, got %s", content)
	}
}

func TestREPLAtFileBroadFileQuestionsUseAttachedFileContext(t *testing.T) {
	tempDir := t.TempDir()
	docPath := filepath.Join(tempDir, "process_record.md")
	if err := os.WriteFile(docPath, []byte("# Process Record\n\nRecords completed work, risks, validation, and next steps.\n"), 0o644); err != nil {
		t.Fatalf("write doc failed: %v", err)
	}
	var output bytes.Buffer
	err := runREPLWithIO(strings.NewReader("@"+docPath+" 这个文件记录了什么\n把刚才文件分析直接写出来\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if strings.Contains(content, "Executing: avatars run") || strings.Contains(content, "Avatar Planner") {
		t.Fatalf("expected broad attached-file questions to avoid task run, got %s", content)
	}
	if strings.Count(content, "File context:") < 2 || !strings.Contains(content, "Process Record") || !strings.Contains(content, "Records completed work") {
		t.Fatalf("expected attached file context reuse, got %s", content)
	}
}

func TestREPLTaskSpecAttachedFileRoutesToScriptGeneration(t *testing.T) {
	tempDir := t.TempDir()
	docPath := filepath.Join(tempDir, "1.md")
	docContent := "### coding plan\n分别编写4个 python 脚本：\n1. 找出100-200之间所有的质数\n2. 找出1-1000之间所有的完美数\n3. 公鸡每只五钱，母鸡每只三钱，小鸡每三只一钱，用一百钱买一百只鸡，问公鸡、母鸡、小鸡各买了多少只？\n4. 九九乘法表\n"
	if err := os.WriteFile(docPath, []byte(docContent), 0o644); err != nil {
		t.Fatalf("write task spec failed: %v", err)
	}
	originalGenerator := scriptContentGenerator
	originalAnalyzer := scriptIntentAnalyzer
	scriptContentGenerator = func(path string, description string) (string, bool, error) {
		if !strings.Contains(description, "100-200") || !strings.Contains(description, "九九乘法表") {
			t.Fatalf("expected attached task spec content in generator, got %q", description)
		}
		return "#!/usr/bin/env python3\nprint('task-spec')\n", true, nil
	}
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		if !strings.Contains(input, "coding plan") || !strings.Contains(input, "完美数") {
			t.Fatalf("expected attached task spec content in analyzer, got %q", input)
		}
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "task_spec.py", Topic: "task spec", Confidence: 94}, true
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
	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("@"+docPath+"\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v\n%s", err, output.String())
	}
	content := output.String()
	if strings.Contains(content, "File context:\n-") && strings.Contains(content, "Process Record") {
		t.Fatalf("expected task spec doc to route to execution, not summary, got %s", content)
	}
	if !strings.Contains(content, "Script mode: apply") || !strings.Contains(content, "Generator: llm") {
		t.Fatalf("expected task spec doc to route to llm-backed script generation, got %s", content)
	}
	files, err := listProjectFiles(tempDir, -1)
	if err != nil {
		t.Fatalf("list files failed: %v", err)
	}
	hasScript := false
	for _, file := range files {
		if strings.HasSuffix(file, ".py") {
			hasScript = true
			break
		}
	}
	if !hasScript {
		t.Fatalf("expected a python script to be written, files=%v", files)
	}
}

func TestREPLClarifyUsesHumanizedPrompt(t *testing.T) {
	var output bytes.Buffer
	err := runREPLWithIO(strings.NewReader("你好吗？今天感觉怎样\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if strings.Contains(content, "Clarify:") {
		t.Fatalf("expected humanized clarify text, got %s", content)
	}
	if !strings.Contains(content, "I'm not sure what to do next.") {
		t.Fatalf("expected humanized clarify prompt, got %s", content)
	}
}

func TestREPLCapabilityAndIdentityQuestionsAnswerDirectly(t *testing.T) {
	var output bytes.Buffer
	err := runREPLWithIO(strings.NewReader("你擅长干啥呢\n你是gpt吗\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if strings.Contains(content, "I cannot determine the next step") || strings.Contains(content, "Clarify:") {
		t.Fatalf("expected direct answers, got %s", content)
	}
	if !strings.Contains(content, "Capabilities:") || !strings.Contains(content, "Model identity:") {
		t.Fatalf("expected capability and identity answers, got %s", content)
	}
}

func TestREPLExplicitIntentCommandRoutesToIntent(t *testing.T) {
	tempDir := t.TempDir()
	planPath := filepath.Join(tempDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("unroutable task plan\n"), 0o644); err != nil {
		t.Fatalf("write plan file failed: %v", err)
	}
	var output bytes.Buffer
	err := runREPLWithIO(strings.NewReader("/intent --from-file "+planPath+"\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	if !strings.Contains(output.String(), "Intent input file loaded:") {
		t.Fatalf("expected explicit intent from-file cue, got %s", output.String())
	}
}

func TestREPLHistoryReplayCommands(t *testing.T) {
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
	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("analyze repository\n/history\n!!\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if !strings.Contains(content, "History:") {
		t.Fatalf("expected repl history output, got %s", content)
	}
	if !strings.Contains(content, "Replaying: analyze repository") {
		t.Fatalf("expected history replay cue, got %s", content)
	}
}

func TestFormatREPLContext_RecentTurnsOnly(t *testing.T) {
	// replContextMaxTurns=10, so 7-turn history should include all turns.
	history := []string{"turn1", "turn2", "turn3", "turn4", "turn5", "turn6", "turn7"}
	ctx := formatREPLContext(history)
	if !strings.Contains(ctx, "Turn 1:") || !strings.Contains(ctx, "Turn 7:") {
		t.Fatalf("expected all turns 1-7 with max=10, got %q", ctx)
	}
	// 15-turn history should only keep the 10 most recent.
	longHistory := make([]string, 15)
	for i := range longHistory {
		longHistory[i] = fmt.Sprintf("turn%d", i+1)
	}
	ctx2 := formatREPLContext(longHistory)
	if strings.Contains(ctx2, "Turn 1:") || strings.Contains(ctx2, "Turn 5:") {
		t.Fatalf("expected old turns 1-5 to be excluded with max=10, got %q", ctx2)
	}
	if !strings.Contains(ctx2, "Turn 6:") || !strings.Contains(ctx2, "Turn 15:") {
		t.Fatalf("expected recent turns 6-15, got %q", ctx2)
	}
}

func TestFormatREPLContext_EmptyHistory(t *testing.T) {
	ctx := formatREPLContext(nil)
	if ctx != "" {
		t.Fatalf("expected empty context for nil history, got %q", ctx)
	}
	ctx = formatREPLContext([]string{})
	if ctx != "" {
		t.Fatalf("expected empty context for empty history, got %q", ctx)
	}
}

func TestFormatREPLContext_TruncatesLongLines(t *testing.T) {
	// truncateLine max is now 500 chars. Use 600 to exercise truncation.
	longLine := strings.Repeat("x", 600)
	ctx := formatREPLContext([]string{longLine})
	if strings.Contains(ctx, strings.Repeat("x", 600)) {
		t.Fatalf("expected long line to be truncated at 500 chars, got %d chars", len(ctx))
	}
	if !strings.Contains(ctx, "…") {
		t.Fatalf("expected truncation marker, got %q", ctx)
	}
}

func TestFormatREPLContext_RespectsMaxLength(t *testing.T) {
	// replContextMaxTurns=10, replContextMaxLength=4000, truncateLine=500.
	// With 500-char lines and "Turn N: " prefix, 10 turns ≈ 5100 chars
	// which exceeds 4000. Verify the total-length cap triggers.
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strings.Repeat("x", 500)
	}
	ctx := formatREPLContext(lines)
	if len(ctx) > replContextMaxLength+200 {
		t.Fatalf("expected context to be under ~%d chars, got %d", replContextMaxLength+200, len(ctx))
	}
	// Should contain at most 10 turns (the most recent)
	count := strings.Count(ctx, "Turn ")
	if count > 10 {
		t.Fatalf("expected at most 10 turns, got %d", count)
	}
	// Should contain at least 6 turns (before hitting the 4000-char cap)
	if count < 6 {
		t.Fatalf("expected at least 6 turns under 4000-char cap, got %d", count)
	}
}

func TestREPLEditWritesFileAndLastActionAnswersFollowUp(t *testing.T) {
	originalAnalyzer := editIntentAnalyzer
	originalGenerator := editContentGenerator
	editIntentAnalyzer = func(input string) (editIntentSpec, bool) {
		return editIntentSpec{
			Action:      "edit",
			Path:        "calc.py",
			Instruction: "add subtract(a, b) and print add and subtract results",
			Confidence:  96,
		}, true
	}
	editContentGenerator = func(path string, instruction string, current string) (string, bool, error) {
		if path != "calc.py" || !strings.Contains(current, "def add") || !strings.Contains(instruction, "subtract") {
			t.Fatalf("unexpected edit generator input path=%q instruction=%q current=%q", path, instruction, current)
		}
		return "def add(a, b):\n    return a + b\n\n\ndef subtract(a, b):\n    return a - b\n\n\nif __name__ == \"__main__\":\n    print(add(2, 3))\n    print(subtract(2, 3))\n", true, nil
	}
	defer func() {
		editIntentAnalyzer = originalAnalyzer
		editContentGenerator = originalGenerator
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
	if err := os.WriteFile("calc.py", []byte("def add(a, b):\n    return a + b\n"), 0o644); err != nil {
		t.Fatalf("write calc failed: %v", err)
	}

	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("修改 calc.py：新增 subtract(a, b) 函数，返回 a - b，并让命令行同时打印 add 和 subtract 的结果\n刚才改了什么？\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v\n%s", err, output.String())
	}
	content := output.String()
	if strings.Contains(content, "plan-mode") || strings.Contains(content, "Avatar Planner") {
		t.Fatalf("expected edit direct action, not plan-mode, got %s", content)
	}
	if !strings.Contains(content, "Edit mode: apply") || !strings.Contains(content, "Generator: llm") {
		t.Fatalf("expected edit apply output, got %s", content)
	}
	if !strings.Contains(content, "Last action:") || !strings.Contains(content, "kind: edit") || !strings.Contains(content, "artifact: calc.py") {
		t.Fatalf("expected follow-up to reuse last edit artifact, got %s", content)
	}
	updated, err := os.ReadFile("calc.py")
	if err != nil {
		t.Fatalf("read updated calc failed: %v", err)
	}
	if !strings.Contains(string(updated), "def subtract") {
		t.Fatalf("expected calc.py to contain subtract, got %s", string(updated))
	}
}

func TestREPLFibonacciScriptWritesPythonFile(t *testing.T) {
	originalGenerator := scriptContentGenerator
	originalAnalyzer := scriptIntentAnalyzer
	scriptContentGenerator = func(path string, description string) (string, bool, error) {
		return "#!/usr/bin/env python3\n\ndef fibonacci(count: int) -> list[int]:\n    return [0, 1][:count]\n\nprint(fibonacci(2))\n", true, nil
	}
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "fibonacci.py", Topic: "斐波那契数列", Confidence: 95}, true
	}
	defer func() {
		scriptContentGenerator = originalGenerator
		scriptIntentAnalyzer = originalAnalyzer
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
	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("帮我用python写一个*斐波那契数列*脚本\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v\n%s", err, output.String())
	}
	content, err := os.ReadFile("fibonacci.py")
	if err != nil {
		t.Fatalf("expected fibonacci.py to be written: %v\n%s", err, output.String())
	}
	if !strings.Contains(string(content), "def fibonacci") {
		t.Fatalf("expected fibonacci script content, got %s", string(content))
	}
	if !strings.Contains(output.String(), "Path: fibonacci.py") || !strings.Contains(output.String(), "Verifier:") {
		t.Fatalf("expected script output, got %s", output.String())
	}
}

func TestREPLTopicScriptUsesLLMGeneratorAndTopicFilename(t *testing.T) {
	originalGenerator := scriptContentGenerator
	originalAnalyzer := scriptIntentAnalyzer
	scriptContentGenerator = func(path string, description string) (string, bool, error) {
		if !strings.Contains(description, "金字塔") {
			t.Fatalf("expected pyramid description, got %q", description)
		}
		return "#!/usr/bin/env python3\n\nfor row in range(1, 4):\n    print('■' * row)\n", true, nil
	}
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "pyramid.py", Topic: "■排列的金字塔", Confidence: 95}, true
	}
	defer func() {
		scriptContentGenerator = originalGenerator
		scriptIntentAnalyzer = originalAnalyzer
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
	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("帮我用python写一个*■排列的金字塔*脚本\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v\n%s", err, output.String())
	}
	if strings.Contains(output.String(), "Path: script.py") || !strings.Contains(output.String(), "Generator: llm") {
		t.Fatalf("expected topic filename and llm generator, got %s", output.String())
	}
	files, err := listProjectFiles(tempDir, -1)
	if err != nil {
		t.Fatalf("list files failed: %v", err)
	}
	scriptPath := ""
	for _, file := range files {
		if strings.HasSuffix(file, ".py") {
			scriptPath = file
			break
		}
	}
	if scriptPath == "" {
		t.Fatalf("expected python script file, files=%v", files)
	}
	content, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script failed: %v", err)
	}
	if !strings.Contains(string(content), "■") || strings.Contains(string(content), "Hello from script") {
		t.Fatalf("expected LLM script content, got %s", string(content))
	}
}

func TestREPLColonScriptProblemUsesLLMGenerator(t *testing.T) {
	originalGenerator := scriptContentGenerator
	originalAnalyzer := scriptIntentAnalyzer
	scriptContentGenerator = func(path string, description string) (string, bool, error) {
		if !strings.Contains(description, "公鸡") || !strings.Contains(description, "一百只鸡") {
			t.Fatalf("expected hundred-chicken description, got %q", description)
		}
		return "#!/usr/bin/env python3\n\nfor rooster in range(21):\n    for hen in range(34):\n        chick = 100 - rooster - hen\n        if chick >= 0 and chick % 3 == 0 and rooster * 5 + hen * 3 + chick // 3 == 100:\n            print(rooster, hen, chick)\n", true, nil
	}
	scriptIntentAnalyzer = func(input string) (scriptIntentSpec, bool) {
		return scriptIntentSpec{Action: "script", Language: "python", Filename: "hundred_chickens.py", Topic: "百钱买百鸡", Confidence: 95}, true
	}
	defer func() {
		scriptContentGenerator = originalGenerator
		scriptIntentAnalyzer = originalAnalyzer
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
	input := "帮我用python写一个脚本：公鸡每只五钱，母鸡每只三钱，小鸡每三只一钱，用一百钱买一百只鸡，问公鸡、母鸡、小鸡各买了多少只？\n/exit\n"
	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader(input), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v\n%s", err, output.String())
	}
	if strings.Contains(output.String(), "Path: script.py") || !strings.Contains(output.String(), "Generator: llm") {
		t.Fatalf("expected topic path and llm generator, got %s", output.String())
	}
	files, err := listProjectFiles(tempDir, -1)
	if err != nil {
		t.Fatalf("list files failed: %v", err)
	}
	scriptPath := ""
	for _, file := range files {
		if strings.HasSuffix(file, ".py") {
			scriptPath = file
			break
		}
	}
	if scriptPath == "" {
		t.Fatalf("expected generated python script, files=%v", files)
	}
	content, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script failed: %v", err)
	}
	if strings.Contains(string(content), "Hello from script") || !strings.Contains(string(content), "rooster") {
		t.Fatalf("expected problem-specific script, got %s", string(content))
	}
}

func TestREPLExplicitPatchAppliesReplacement(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n\nold text\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("把 README.md 里的 old text 改成 new text\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v\n%s", err, output.String())
	}
	content, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README failed: %v", err)
	}
	if !strings.Contains(string(content), "new text") || strings.Contains(string(content), "old text") {
		t.Fatalf("expected replacement applied, got %s", string(content))
	}
	if strings.Contains(output.String(), "no task result found yet") {
		t.Fatalf("expected direct patch route not to print task result location, got %s", output.String())
	}
}

func TestREPLSimpleScriptWithTopicPrintsScriptResult(t *testing.T) {
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
	// Stub the script content generator to avoid hitting a real LLM.
	original := scriptContentGenerator
	scriptContentGenerator = func(path, description string) (string, bool, error) {
		return "#!/usr/bin/env python3\n\ndef main() -> None:\n    print(\"Hello from hello\")\n\nif __name__ == \"__main__\":\n    main()\n", true, nil
	}
	defer func() { scriptContentGenerator = original }()

	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("写一个 *hello* 脚本 hello.py\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v", err)
	}
	content := output.String()
	if !strings.Contains(content, "Script mode: apply") || !strings.Contains(content, "Wrote:") || !strings.Contains(content, "Verifier:") {
		t.Fatalf("expected repl script result output, got %s", content)
	}
	if strings.Contains(content, "no task result found yet") {
		t.Fatalf("expected script route not to print task result location, got %s", content)
	}
	if _, err := os.Stat("hello.py"); err != nil {
		t.Fatalf("expected hello.py to be written: %v", err)
	}
}

func TestREPLFollowUpUsesLastTurnArtifactForScriptWithTopic(t *testing.T) {
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
	// Stub the script content generator to avoid hitting a real LLM.
	original := scriptContentGenerator
	scriptContentGenerator = func(path, description string) (string, bool, error) {
		return "#!/usr/bin/env python3\n\ndef main() -> None:\n    print(\"Hello from hello\")\n\nif __name__ == \"__main__\":\n    main()\n", true, nil
	}
	defer func() { scriptContentGenerator = original }()

	var output bytes.Buffer
	err = runREPLWithIO(strings.NewReader("写一个 *hello* 脚本 hello.py\n你刚才做了什么\n脚本在哪\n结果在哪？\n继续\n/exit\n"), &output, replCommandOptions{})
	if err != nil {
		t.Fatalf("run repl failed: %v\n%s", err, output.String())
	}
	content := output.String()
	scriptCues := []string{
		"OK — I'll write the script, then run verification.",
		"Got it. Generating the code.",
		"Sure. I'll write it and then check syntax.",
	}
	scriptHits := 0
	for _, cue := range scriptCues {
		scriptHits += strings.Count(content, cue)
	}
	if scriptHits != 1 {
		t.Fatalf("expected script to execute once and follow-ups to reuse memory, got %s", content)
	}
	if !strings.Contains(content, "Last action:") || !strings.Contains(content, "- kind: script") {
		t.Fatalf("expected last action from repl artifact, got %s", content)
	}
	if !strings.Contains(content, "Script location:") || !strings.Contains(content, "hello.py") {
		t.Fatalf("expected script location from last turn artifact, got %s", content)
	}
	if !strings.Contains(content, "Result location:") || !strings.Contains(content, "last action: script") {
		t.Fatalf("expected result location from last turn artifact, got %s", content)
	}
	if strings.Count(content, "Last action:") < 2 {
		t.Fatalf("expected continue follow-up to reuse last action artifact, got %s", content)
	}
	if _, ok, err := loadREPLLastTurn("."); err != nil || !ok {
		t.Fatalf("expected last turn artifact to be readable, ok=%t err=%v", ok, err)
	}
}

func TestREPLIntentArgsInjectTaskContext(t *testing.T) {
	args := replIntentArgsForLine("analyze repository", replCommandOptions{TaskID: "demo-task"})
	command := strings.Join(args, " ")
	if command != "intent analyze repository task=demo-task" {
		t.Fatalf("expected task context injection, got %q", command)
	}
}

func TestREPLIntentArgsPreserveExplicitTaskContext(t *testing.T) {
	args := replIntentArgsForLine("analyze repository task=other-task", replCommandOptions{TaskID: "demo-task"})
	command := strings.Join(args, " ")
	if command != "intent analyze repository task=other-task" {
		t.Fatalf("expected explicit task context to be preserved, got %q", command)
	}
}

func TestREPLRunArgsRoutePlainInputToPlanModeTask(t *testing.T) {
	args := replRunArgsForLine("普通指令你无法识别？", replCommandOptions{TaskID: "demo-task"})
	command := strings.Join(args, " ")
	if command != "run --task demo-task --permission-mode plan 普通指令你无法识别？" {
		t.Fatalf("expected plain repl input to route to safe plan-mode run, got %q", command)
	}
}

func TestREPLRunArgsFreshModeUsesNewPlanTask(t *testing.T) {
	args := replRunArgsForLine("为什么会出现 Survey depth is limited", replCommandOptions{ForceNewTask: true})
	command := strings.Join(args, " ")
	if command != "run --new-task --permission-mode plan 为什么会出现 Survey depth is limited" {
		t.Fatalf("expected fresh repl input to route to new plan-mode run, got %q", command)
	}
}

func TestREPLRunArgsMutatingCodingPicksDefaultMode(t *testing.T) {
	// TODO-08: the REPL fallback for unclassified input should
	// dispatch a sensible permission mode based on the request's
	// intent. Mutating coding requests get `default` so guarded
	// approval can engage for sensitive operations.
	args := replRunArgsForLine("修复 README.md 里的错别字", replCommandOptions{ForceNewTask: true})
	command := strings.Join(args, " ")
	if !strings.Contains(command, "--permission-mode default") {
		t.Fatalf("expected mutating coding request to pick --permission-mode default, got %q", command)
	}
}

func TestREPLRunArgsExplicitReplacementPicksAcceptEdits(t *testing.T) {
	// TODO-08: explicit before/after string replacements get
	// `acceptEdits` so the harness can apply the edit without a
	// separate approval round.
	args := replRunArgsForLine("把 main.go 里的 'foo' 替换成 'bar'", replCommandOptions{ForceNewTask: true})
	command := strings.Join(args, " ")
	if !strings.Contains(command, "--permission-mode acceptEdits") {
		t.Fatalf("expected explicit replacement to pick --permission-mode acceptEdits, got %q", command)
	}
}

func TestREPLRunArgsAnalysisStaysPlanMode(t *testing.T) {
	// TODO-08: read-only / analysis requests stay on `plan` so the
	// LLM does not silently start writing files.
	args := replRunArgsForLine("分析这个项目并总结写入 outcome.md", replCommandOptions{ForceNewTask: true})
	command := strings.Join(args, " ")
	if !strings.Contains(command, "--permission-mode plan") {
		t.Fatalf("expected analysis request to stay on --permission-mode plan, got %q", command)
	}
}

func TestREPLRunArgsExplicitUserModeWinsOverIntent(t *testing.T) {
	// TODO-08: an explicit `--permission-mode` on the REPL command
	// line (or set in options by the caller) wins over the
	// intent-derived mode. This is the "use `/run --permission-mode
	// <mode>` to force a specific mode" affordance the help text
	// documents.
	args := replRunArgsForLine("修复 README.md 里的错别字", replCommandOptions{
		ForceNewTask:   true,
		PermissionMode: "plan",
	})
	command := strings.Join(args, " ")
	if !strings.Contains(command, "--permission-mode plan") {
		t.Fatalf("expected explicit --permission-mode=plan to win over intent, got %q", command)
	}
}

func TestParseREPLCommandOptions_RejectsInvalidPermissionMode(t *testing.T) {
	_, err := parseREPLCommandOptions([]string{"--permission-mode", "elevated"})
	if err == nil {
		t.Fatalf("expected error for unsupported permission mode, got nil")
	}
	if !strings.Contains(err.Error(), "plan, default, or acceptEdits") {
		t.Fatalf("expected helpful error message, got %v", err)
	}
}

func TestParseREPLCommandOptions_AcceptsAllThreePermissionModes(t *testing.T) {
	for _, mode := range []string{"plan", "default", "acceptEdits"} {
		options, err := parseREPLCommandOptions([]string{"--permission-mode", mode})
		if err != nil {
			t.Fatalf("parseREPLCommandOptions(%q): %v", mode, err)
		}
		if options.PermissionMode != mode {
			t.Fatalf("expected PermissionMode=%q, got %q", mode, options.PermissionMode)
		}
	}
}

func TestSmokeREPLRoutingCommand(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(".avatars", "tasks", "repl-session"), 0o755); err != nil {
		t.Fatalf("mkdir task workspace failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "repl-session", Title: "repl-session", ForceNew: true})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("open task memory store failed: %v", err)
	}
	if err := memoryStore.UpsertSummary(memstore.SummaryRecord{SessionID: "session-smoke", RunID: "run-smoke", TaskID: workspace.ID, Scope: memstore.ScopeTask, Role: "Synthesizer", Summary: "Smoke task memory says the project still needs clearer issue proof."}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record smoke task summary failed: %v", err)
	}
	if err := memoryStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: workspace.ID, Kind: "opinion", Summary: "Smoke project lesson says follow-up opinion questions should reuse memory before fresh survey.", Source: "warm_lesson", Confidence: "high", SupportCount: 1, SupportTasks: []string{workspace.ID}}); err != nil {
		_ = memoryStore.Close()
		t.Fatalf("record smoke project lesson failed: %v", err)
	}
	if closeErr := memoryStore.Close(); closeErr != nil {
		t.Fatalf("close task memory store failed: %v", closeErr)
	}
	output := captureRunOutput(t, []string{"smoke", "repl-routing", "--task", "repl-session"})
	if !strings.Contains(output, "direct_answer\tdirect_answer") {
		t.Fatalf("expected direct answer smoke line, got %s", output)
	}
	if !strings.Contains(output, "memory_answer\tmemory_answer") {
		t.Fatalf("expected memory answer smoke line, got %s", output)
	}
	if !strings.Contains(output, "safe_run\tsafe_run") {
		t.Fatalf("expected safe run smoke line, got %s", output)
	}
	if !strings.Contains(output, "guarded\tguarded") {
		t.Fatalf("expected guarded smoke line, got %s", output)
	}
	if !strings.Contains(output, "clarify\tclarify") {
		t.Fatalf("expected clarify smoke line, got %s", output)
	}
	if !strings.Contains(output, "Smoke: repl routing ok") {
		t.Fatalf("expected smoke success line, got %s", output)
	}
}

func TestSmokeCodingGateCommand(t *testing.T) {
	output := captureRunOutput(t, []string{"smoke", "coding-gate"})
	for _, expected := range []string{
		"intent_coding_default_mode\tPASS",
		"ux_repair_direct_answer\tPASS",
		"report_proof_gate\tPASS",
		"external_ambiguous_clarify\tPASS",
		"Smoke: coding gate ok",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected coding gate smoke output to contain %q, got %s", expected, output)
		}
	}
}

func TestREPLDirectAnswerProjectFileCount(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(".avatars", "tasks"), 0o755); err != nil {
		t.Fatalf("mkdir .avatars failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(".avatars", "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write ignored file failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("项目总共有多少文件", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("direct answer failed: %v", err)
	}
	if !handled {
		t.Fatal("expected project file count question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	text := route.Answer
	if !strings.Contains(text, "Project files: 1") {
		t.Fatalf("expected deterministic file count, got %s", text)
	}
}

func TestREPLDirectAnswerProjectFileList(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("列出项目文件", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("file list answer failed: %v", err)
	}
	if !handled {
		t.Fatal("expected project file list question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	if !strings.Contains(route.Answer, "README.md") || !strings.Contains(route.Answer, "go.mod") {
		t.Fatalf("expected listed project files, got %s", route.Answer)
	}
}

func TestREPLDirectAnswerProjectOverview(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n\nA small demo project.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("这个项目是干什么的", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("project overview failed: %v", err)
	}
	if !handled {
		t.Fatal("expected project overview question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	if !strings.Contains(route.Answer, "Project overview:") || !strings.Contains(route.Answer, "README: README.md") || !strings.Contains(route.Answer, "Title: Demo") {
		t.Fatalf("expected overview summary, got %s", route.Answer)
	}
	if !strings.Contains(route.Answer, "Stack signals: Go module") {
		t.Fatalf("expected stack signals summary, got %s", route.Answer)
	}
}

func TestREPLDirectAnswerProjectOverviewMatchesColloquialQuestion(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n\nA small demo project.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("这个项目是干啥的", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("project overview colloquial failed: %v", err)
	}
	if !handled {
		t.Fatal("expected colloquial overview question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	if !strings.Contains(route.Answer, "Project overview:") {
		t.Fatalf("expected overview answer, got %s", route.Answer)
	}
}

func TestREPLDirectAnswerModelIdentity(t *testing.T) {
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
	if err := os.MkdirAll("configs", 0o755); err != nil {
		t.Fatalf("mkdir configs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), []byte("llm:\n  provider: deepseek\n  model: deepseek-chat\n  base_url: https://api.deepseek.com\n  api_key_env: DEEPSEEK_API_KEY\n"), 0o644); err != nil {
		t.Fatalf("write agent config failed: %v", err)
	}
	for _, input := range []string{"你是什么大模型", "你是啥模型"} {
		route, handled, err := resolveNaturalLanguageQuestion(input, replCommandOptions{}, naturalLanguageContextREPL)
		if err != nil {
			t.Fatalf("model identity failed for %q: %v", input, err)
		}
		if !handled {
			t.Fatalf("expected model identity question to be handled for %q", input)
		}
		if route.Kind != naturalLanguageRouteDirectAnswer {
			t.Fatalf("expected direct answer route for %q, got %+v", input, route)
		}
		if !strings.Contains(route.Answer, "Model identity:") || !strings.Contains(route.Answer, "deepseek") {
			t.Fatalf("expected model identity answer for %q, got %s", input, route.Answer)
		}
	}
}

func TestREPLDirectAnswerProjectConversationalEvaluation(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n\nA small demo project.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.MkdirAll("docs", 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("这个项目，你觉得如何？", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("project evaluation failed: %v", err)
	}
	if !handled {
		t.Fatal("expected project evaluation question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	if len(route.Command) != 0 {
		t.Fatalf("expected no task command for conversational evaluation, got %+v", route.Command)
	}
	if !strings.Contains(route.Answer, "Project opinion:") || !strings.Contains(route.Answer, "Project overview:") {
		t.Fatalf("expected lightweight project opinion, got %s", route.Answer)
	}
}

func TestREPLDirectAnswerProjectConversationalEvaluationPrefersTaskMemory(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join(".avatars", "tasks", "repl-session"), 0o755); err != nil {
		t.Fatalf("mkdir task workspace failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Demo\n\nA small demo project.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "repl-session", Title: "repl-session", ForceNew: true})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		t.Fatalf("create task memory store failed: %v", err)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	if err := memoryStore.UpsertSummary(memstore.SummaryRecord{SessionID: "session-task-memory", RunID: "run-task-memory", TaskID: workspace.ID, Scope: memstore.ScopeTask, Role: "Synthesizer", Summary: "Task memory says the project is promising but still needs clearer issue proof."}); err != nil {
		t.Fatalf("record task summary failed: %v", err)
	}
	if err := memoryStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: workspace.ID, Kind: "opinion", Summary: "Project lesson says follow-up questions should reuse memory before fresh survey.", Source: "warm_lesson", Confidence: "high", SupportCount: 2, SupportTasks: []string{workspace.ID}}); err != nil {
		t.Fatalf("record project lesson failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("你认为这个项目构建的如何？", replCommandOptions{TaskID: workspace.ID}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("task-memory project evaluation failed: %v", err)
	}
	if !handled {
		t.Fatal("expected task-memory follow-up to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected memory-backed direct answer route, got %+v", route)
	}
	if !strings.Contains(route.Answer, "Memory recall:") {
		t.Fatalf("expected memory recall answer, got %s", route.Answer)
	}
	if strings.Contains(route.Answer, "Project overview:") {
		t.Fatalf("expected memory recall to avoid fresh overview survey, got %s", route.Answer)
	}
}

func TestREPLCapabilityQuestionAnswersDirectlyWithoutSafeRun(t *testing.T) {
	for _, input := range []string{"你会写py吧？", "你擅长改代码吗？"} {
		decision := classifyNaturalLanguageQuestion(input, replCommandOptions{}, naturalLanguageContextREPL)
		if decision.Kind != naturalLanguageDecisionAnswer {
			t.Fatalf("expected capability question %q to answer directly, got %+v", input, decision)
		}
		if strings.TrimSpace(decision.Answer) == "" {
			t.Fatalf("expected capability answer content for %q", input)
		}
	}
}

func TestREPLCompactRunTemporarilySuppressesProgress(t *testing.T) {
	previous, hadPrevious := os.LookupEnv("AVATARS_PROGRESS")
	defer func() {
		if hadPrevious {
			_ = os.Setenv("AVATARS_PROGRESS", previous)
			return
		}
		_ = os.Unsetenv("AVATARS_PROGRESS")
	}()
	if err := os.Setenv("AVATARS_PROGRESS", "1"); err != nil {
		t.Fatalf("set env failed: %v", err)
	}
	called := false
	err := withSuppressedRunProgress(func() error {
		called = true
		if got := os.Getenv("AVATARS_PROGRESS"); got != "0" {
			t.Fatalf("expected progress suppressed inside compact run, got %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("compact run wrapper failed: %v", err)
	}
	if !called {
		t.Fatal("expected wrapped function to run")
	}
	if got := os.Getenv("AVATARS_PROGRESS"); got != "1" {
		t.Fatalf("expected progress env restored, got %q", got)
	}
}

func TestREPLCompactRunCapturesStdoutMetadata(t *testing.T) {
	previousStdout := os.Stdout
	captured, err := withSuppressedRunProgressAndCapturedStdout(func() error {
		fmt.Println("Task mode: stable")
		fmt.Println("Task root: .avatars/tasks/demo")
		if got := os.Getenv("AVATARS_PROGRESS"); got != "0" {
			t.Fatalf("expected progress suppressed inside compact run, got %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("compact capture failed: %v", err)
	}
	if os.Stdout != previousStdout {
		t.Fatal("expected stdout to be restored")
	}
	if !strings.Contains(captured, "Task mode: stable") || !strings.Contains(captured, "Task root:") {
		t.Fatalf("expected stdout metadata to be captured, got %q", captured)
	}
}

func TestREPLGreetingQuestionAnswersDirectly(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("你好啊，avatars", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected greeting to answer directly, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Hi.") {
		t.Fatalf("expected greeting answer, got %+v", decision)
	}
}

func TestREPLProjectOpinionQuestionDoesNotRouteToSafeRun(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("你认为这个项目构建的如何？", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected project opinion question to answer directly, got %+v", decision)
	}
	if strings.TrimSpace(decision.Answer) == "" {
		t.Fatal("expected project opinion answer content")
	}
}

func TestREPLConversationalRepairQuestionDoesNotClarify(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("让你表达意见，你又审一遍代码？", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected repair question to answer directly, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Repair note:") {
		t.Fatalf("expected repair note answer, got %+v", decision)
	}
}

func TestREPLConversationalRepairQuestionAlsoHandlesEnglishRepair(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("why did you reread the repository again?", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected english repair question to answer directly, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Repair note:") {
		t.Fatalf("expected repair note answer, got %+v", decision)
	}
}

func TestREPLConversationalRepairHandlesDirectAnswerComplaint(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("你不会直接回答问题？", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected direct-answer complaint to answer directly, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Repair note:") {
		t.Fatalf("expected repair note answer, got %+v", decision)
	}
}

func TestREPLExplicitWorkRequestStillRoutesToSafeRun(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("分析项目，找问题，总结写入 outcome.md", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected explicit work request to route to safe run, got %+v", decision)
	}
	if len(decision.Command) == 0 || decision.Command[0] != "run" {
		t.Fatalf("expected run command, got %+v", decision.Command)
	}
}

func TestREPLAnalysisReportIntentRoutesToSafeRun(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("分析项目，找问题，总结写入 a.md", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected analysis report intent to route to safe run, got %+v", decision)
	}
	if len(decision.Command) == 0 || decision.Command[0] != "run" || !strings.Contains(strings.Join(decision.Command, " "), "--permission-mode plan") {
		t.Fatalf("expected plan-mode run command, got %+v", decision.Command)
	}
	if !strings.Contains(strings.Join(decision.Command, " "), "a.md") {
		t.Fatalf("expected report path to stay in command, got %+v", decision.Command)
	}
}

func TestREPLEvaluationWithOutputFileRoutesToSafeRun(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("分析这个项目，找问题并评价。结果写入 analisis.md", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected output-producing evaluation to route to safe run, got %+v", decision)
	}
	command := strings.Join(decision.Command, " ")
	if !strings.Contains(command, "run --task demo-task --permission-mode plan") || !strings.Contains(command, "analisis.md") {
		t.Fatalf("expected named plan-mode run preserving output file, got %+v", decision.Command)
	}
}

func TestREPLResultLocationQuestionReportsLatestAnalysisReport(t *testing.T) {
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
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("Analysis report: analisis.md\n"), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "analysis report generated"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}
	decision := classifyNaturalLanguageQuestion("结果在那呢？", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected result location direct answer, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Result location:") || !strings.Contains(decision.Answer, "analisis.md") {
		t.Fatalf("expected result location to include analysis report path, got %s", decision.Answer)
	}
}

func TestREPLPreviousResultWriteRequestReusesLatestReport(t *testing.T) {
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
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: defaultREPLTaskID, Title: defaultREPLTaskID})
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

	decision := classifyNaturalLanguageQuestion("把刚才的分析结果输出在 outcome.md", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionMemory {
		t.Fatalf("expected previous result reuse, got %+v", decision)
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

func TestREPLPreviousAnswerWriteRequestReusesLatestAnswer(t *testing.T) {
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
	answerContent := "# Full Answer\n\nThis is the complete operator-facing answer.\n"
	line := `{"type":"llm.completed","payload":{"content":` + fmt.Sprintf("%q", answerContent) + `}}` + "\n"
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "answer generated"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}

	preview, ok, err := summarizeLatestRunAnswer(".")
	if err != nil || !ok {
		t.Fatalf("expected latest answer preview, ok=%t err=%v", ok, err)
	}
	if !strings.Contains(preview, "Full answer:") {
		t.Fatalf("expected answer artifact path in preview, got %s", preview)
	}
	if artifact, err := os.ReadFile("answer.md"); err != nil || string(artifact) != strings.TrimSpace(answerContent) {
		t.Fatalf("expected answer artifact, content=%q err=%v", string(artifact), err)
	}

	decision := classifyNaturalLanguageQuestion("上述 answer 显示不完整，请将其写入 a.md", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionMemory {
		t.Fatalf("expected previous answer reuse, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Previous answer reused:") || !strings.Contains(decision.Answer, "a.md") {
		t.Fatalf("expected answer reuse response, got %s", decision.Answer)
	}
	written, err := os.ReadFile("a.md")
	if err != nil {
		t.Fatalf("expected a.md to be written: %v", err)
	}
	if strings.TrimSpace(string(written)) != strings.TrimSpace(answerContent) {
		t.Fatalf("expected full answer content, got %s", string(written))
	}
}

func TestCompactAnswerPreviewMarkdownPreservesStructureAndBounds(t *testing.T) {
	answer := "# Title\n\nIntro paragraph.\n\n## Findings\n\n- first issue\n- second issue\n\n```go\nfunc main() {}\n```\n"
	preview := compactAnswerPreviewMarkdown(answer, 1600)
	if !strings.Contains(preview, "# Title\n\nIntro paragraph.") {
		t.Fatalf("expected heading and paragraph line breaks, got %q", preview)
	}
	if !strings.Contains(preview, "## Findings\n\n- first issue\n- second issue") {
		t.Fatalf("expected list line breaks, got %q", preview)
	}
	if !strings.Contains(preview, "```go\nfunc main() {}\n```") {
		t.Fatalf("expected code fence line breaks, got %q", preview)
	}
	if strings.Contains(preview, "# Title Intro paragraph.") {
		t.Fatalf("expected markdown not to be collapsed into one line, got %q", preview)
	}

	var longLines []string
	for index := 0; index < 60; index++ {
		longLines = append(longLines, fmt.Sprintf("- item %02d", index))
	}
	longPreview := compactAnswerPreviewMarkdown(strings.Join(longLines, "\n"), 1600)
	if !strings.Contains(longPreview, "[preview truncated; see Full answer below]") {
		t.Fatalf("expected truncation marker, got %q", longPreview)
	}
	if strings.Contains(longPreview, "- item 59") {
		t.Fatalf("expected line-bounded preview, got %q", longPreview)
	}

	charPreview := compactAnswerPreviewMarkdown("# 中文标题\n\n"+strings.Repeat("内容", 100), 30)
	if !strings.Contains(charPreview, "[preview truncated; see Full answer below]") {
		t.Fatalf("expected char-bounded preview, got %q", charPreview)
	}
	if !strings.Contains(charPreview, "# 中文标题") {
		t.Fatalf("expected unicode heading to remain readable, got %q", charPreview)
	}
}

func TestLatestRunAnswerFromTranscriptLineExtractsLLMContent(t *testing.T) {
	line := `{"type":"llm.completed","payload":{"content":"Place the new task in internal/auto.\n\nNext: register it in the runner."}}`
	answer := latestRunAnswerFromTranscriptLine(line)
	if !strings.Contains(answer, "Place the new task in internal/auto.") || !strings.Contains(answer, "register it") {
		t.Fatalf("expected llm answer content, got %q", answer)
	}
	if got := latestRunAnswerFromTranscriptLine(`{"type":"run.completed","payload":{"summary":"not direct answer"}}`); got != "" {
		t.Fatalf("expected non-llm line ignored, got %q", got)
	}
}

func TestAttachedFileContextSummarizesActiveFile(t *testing.T) {
	setReplFileContext(&fileContextState{
		Path:    "README.md",
		Content: "# Sheetforge\n\nManual mode imports workbooks.\n",
	})
	defer func() {
		setReplFileContext(nil)
	}()
	answer, _, err := answerClassifiedLocalQuestion("这个文件说了啥？", "active_file_summary", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("active file summary failed: %v", err)
	}
	if !strings.Contains(answer, "File context:") || !strings.Contains(answer, "README.md") || !strings.Contains(answer, "Sheetforge") {
		t.Fatalf("expected active file summary, got %s", answer)
	}
}

func TestREPLPreviousResultWriteRequestRejectsUnsafeTarget(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("把刚才的分析结果输出在 ../outcome.md", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind == naturalLanguageDecisionMemory && strings.Contains(decision.Answer, "Previous result reused:") {
		t.Fatalf("expected unsafe target to avoid reuse write, got %+v", decision)
	}
}

func TestLatestAnalysisReportPathFromTranscriptLine_ParsesJSONLContent(t *testing.T) {
	line := `{"type":"avatar.spoke","payload":{"content":"Multi-avatar planning run complete. Analysis report: outcome.md"}}`
	if got := latestAnalysisReportPathFromTranscriptLine(line); got != "outcome.md" {
		t.Fatalf("expected transcript parser to extract report path, got %q", got)
	}
}

func TestREPLAnalysisReportQualityQuestionUsesLatestAnalysisReport(t *testing.T) {
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
	reportContent := `# Repository Analysis

## Project Purpose
Demo project.

## Issues
1. Missing CI/CD
2. Outdated Go Version

> Note: Individual source files were not displayed; the analysis relies on the aggregated summaries.
`
	if err := os.WriteFile("anali.md", []byte(reportContent), 0o644); err != nil {
		t.Fatalf("write report failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: defaultREPLTaskID, Title: defaultREPLTaskID})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("Analysis report: anali.md\n"), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	if _, err := manager.MarkRun(workspace, transcriptPath, "analysis report generated"); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}
	decision := classifyNaturalLanguageQuestionWithoutLLM("这个分析是否肤浅", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected report quality direct answer, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Analysis report quality:") || !strings.Contains(decision.Answer, "verdict: shallow") || !strings.Contains(decision.Answer, "anali.md") {
		t.Fatalf("expected shallow report quality answer, got %s", decision.Answer)
	}
}

func TestREPLDirectAnswerProjectManifestSummary(t *testing.T) {
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
	if err := os.WriteFile("go.mod", []byte("module demo\n\nrequire example.com/lib v1.2.3\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	if err := os.WriteFile("package.json", []byte("{\"name\":\"demo-app\",\"dependencies\":{\"lodash\":\"^4.17.21\"}}\n"), 0o644); err != nil {
		t.Fatalf("write package.json failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("这个项目的 manifest 和依赖是什么", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("manifest summary failed: %v", err)
	}
	if !handled {
		t.Fatal("expected manifest question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	if !strings.Contains(route.Answer, "Project manifest summary:") || !strings.Contains(route.Answer, "go.mod") || !strings.Contains(route.Answer, "package.json") {
		t.Fatalf("expected manifest summary answer, got %s", route.Answer)
	}
	if !strings.Contains(route.Answer, "Go module: demo, requires=1") {
		t.Fatalf("expected go.mod summary, got %s", route.Answer)
	}
	if !strings.Contains(route.Answer, "Node package: demo-app, dependencies=1") {
		t.Fatalf("expected package.json summary, got %s", route.Answer)
	}
}

func TestREPLDirectAnswerDocsOverview(t *testing.T) {
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
	if err := os.MkdirAll("docs", 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("docs", "README.md"), []byte("# Project Docs\n\nUse these docs for the target project.\n"), 0o644); err != nil {
		t.Fatalf("write docs README failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join("docs", "usage.md"), []byte("# Usage\n"), 0o644); err != nil {
		t.Fatalf("write docs usage failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("这个项目的文档怎么读", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("docs overview failed: %v", err)
	}
	if !handled {
		t.Fatal("expected docs overview question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	if !strings.Contains(route.Answer, "Docs overview:") || !strings.Contains(route.Answer, "Index: docs/README.md | Title: Project Docs") || !strings.Contains(route.Answer, "Docs files: docs/README.md, docs/usage.md") {
		t.Fatalf("expected docs overview answer, got %s", route.Answer)
	}
}

func TestREPLDirectAnswerRecentTasks(t *testing.T) {
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
	manager := tasks.NewManager(filepath.Join(tempDir, ".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	workspace, err = manager.MarkRun(workspace, filepath.Join(workspace.SessionsDir, "session.jsonl"), "Latest task summary")
	if err != nil {
		t.Fatalf("mark run failed: %v", err)
	}
	route, handled, err := resolveNaturalLanguageQuestion("最近任务进展如何", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("recent tasks failed: %v", err)
	}
	if !handled {
		t.Fatal("expected recent task question to be handled")
	}
	if route.Kind != naturalLanguageRouteDirectAnswer {
		t.Fatalf("expected direct answer route, got %+v", route)
	}
	if !strings.Contains(route.Answer, "Recent tasks:") || !strings.Contains(route.Answer, "demo-task") || !strings.Contains(route.Answer, "Latest task summary") {
		t.Fatalf("expected recent task summary answer, got %s", route.Answer)
	}
}

func TestREPLLoadRoutesThroughIntentFromFile(t *testing.T) {
	args := replIntentArgsForFile("plan.md", replCommandOptions{TaskID: "demo-task"})
	command := strings.Join(args, " ")
	if command != "intent --from-file plan.md" {
		t.Fatalf("expected from-file intent route, got %q", command)
	}
}

func TestREPLIntentInputWithTaskContextInjectsTaskOnce(t *testing.T) {
	input := replIntentInputWithTaskContext("Analyze from plan", replCommandOptions{TaskID: "demo-task"})
	if input != "Analyze from plan\n\ntask=demo-task" {
		t.Fatalf("expected task context injection, got %q", input)
	}
	preserved := replIntentInputWithTaskContext("Analyze from plan task=other-task", replCommandOptions{TaskID: "demo-task"})
	if preserved != "Analyze from plan task=other-task" {
		t.Fatalf("expected explicit task marker to be preserved, got %q", preserved)
	}
}

func TestEffectiveREPLCommandOptions_DefaultTask(t *testing.T) {
	options := effectiveREPLCommandOptions(replCommandOptions{})
	if options.TaskID != defaultREPLTaskID {
		t.Fatalf("expected default repl task id %q, got %q", defaultREPLTaskID, options.TaskID)
	}
}

func TestEffectiveREPLCommandOptions_NewTaskKeepsFreshMode(t *testing.T) {
	options := effectiveREPLCommandOptions(replCommandOptions{ForceNewTask: true})
	if options.TaskID != "" {
		t.Fatalf("expected fresh repl mode to keep empty task id, got %q", options.TaskID)
	}
}

func TestREPLStatusTaskIDPrefersIntentMarker(t *testing.T) {
	taskID := replStatusTaskID("inspect repo task=demo-task", replCommandOptions{TaskID: "repl-session"})
	if taskID != "demo-task" {
		t.Fatalf("expected explicit task marker, got %q", taskID)
	}
}

func TestShouldPrintREPLTurnStatusSkipsMetaCommands(t *testing.T) {
	if shouldPrintREPLTurnStatus("/help") {
		t.Fatal("expected /help to skip turn status")
	}
	if shouldPrintREPLTurnStatus("/exit") {
		t.Fatal("expected /exit to skip turn status")
	}
	if shouldPrintREPLTurnStatus("") {
		t.Fatal("expected blank line to skip turn status")
	}
	if !shouldPrintREPLTurnStatus("analyze repository") {
		t.Fatal("expected normal input to print turn status")
	}
}

func TestTruncateRunes_DoesNotSplitUTF8(t *testing.T) {
	// F80: byte-slicing Chinese NL used to emit 需求�…
	in := "需求：做一个很小的 Go 开源库 ttlcache，只要库和测试，不要 CLI"
	got := truncateRunes(in, 4)
	if strings.Contains(got, "\ufffd") {
		t.Fatalf("truncated Chinese must stay valid UTF-8, got %q", got)
	}
	if utf8.RuneCountInString(got) > 4 {
		t.Fatalf("want ≤4 runes, got %q (%d)", got, utf8.RuneCountInString(got))
	}
	long := strings.Repeat("库", 300)
	echo := executionCueTaskIntro(long)
	if strings.Contains(echo, "\ufffd") {
		t.Fatalf("task intro must not split UTF-8: %q", echo)
	}
}
