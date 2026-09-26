package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chdirEmptyWorkspace(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "avatars", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir avatars bundle: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
}

func TestClassifyCreationDeliveryVagueGoCLIAsks(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "弄一个用go写的小东西，命令行能跑就行，功能你看着办"
	if got := classifyCreationDelivery(input); got != creationDeliveryClarify {
		t.Fatalf("want clarify, got %v", got)
	}
	nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("want clarify decision, got %+v", nd)
	}
	if !strings.Contains(strings.Join(nd.Options, "\n"), "avatars run --new-task") {
		t.Fatalf("clarify must offer workflow run, options=%v", nd.Options)
	}
	if !strings.Contains(strings.Join(nd.Options, "\n"), "script --apply --llm") {
		t.Fatalf("clarify must offer script, options=%v", nd.Options)
	}
}

func TestClassifyCreationDeliveryVagueEnglishGoCLIAsks(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "make a small go cli, you decide the features, it just needs to run from the command line"
	if got := classifyCreationDelivery(input); got != creationDeliveryClarify {
		t.Fatalf("want clarify, got %v", got)
	}
}

func TestClassifyCreationDeliverySelfPlanIsWorkflowAcrossLanguages(t *testing.T) {
	chdirEmptyWorkspace(t)
	cases := []string{
		"做成文件去重吧，md5那种。阶段你自己排",
		"build a python file-dedup tool using md5; plan the phases yourself",
		"create a rust cli that hashes files with sha256 and you plan the phases",
		"make a node cli that dedupes files by md5, arrange the phases yourself",
		"write a java command-line tool that dedupes files with md5; plan the phases yourself",
		"create a c# cli that dedupes files using sha256, plan phases yourself",
	}
	for _, input := range cases {
		if got := classifyCreationDelivery(input); got != creationDeliveryWorkflow {
			t.Fatalf("%q: want workflow, got %v", input, got)
		}
		nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
		if nd.Kind != naturalLanguageDecisionSafeRun || len(nd.Command) == 0 || nd.Command[0] != "run" {
			t.Fatalf("%q: want run workflow, got %+v", input, nd)
		}
		joined := strings.Join(nd.Command, " ")
		if !strings.Contains(joined, "--new-task") || !strings.Contains(joined, "acceptEdits") {
			t.Fatalf("%q: want acceptEdits new-task, got %q", input, joined)
		}
		if nd.Command[0] == "script" {
			t.Fatalf("%q: must not be a one-file script", input)
		}
	}
}

func TestClassifyCreationDeliveryNamedScriptStaysScript(t *testing.T) {
	chdirEmptyWorkspace(t)
	nd := classifyNaturalLanguageQuestionWithoutLLM("写一个简单脚本 hello.py", replCommandOptions{}, naturalLanguageContextREPL)
	joined := strings.Join(nd.Command, " ")
	if nd.Kind != naturalLanguageDecisionSafeRun || !strings.HasPrefix(joined, "script --apply") {
		t.Fatalf("named script must stay script, got %+v", nd)
	}
}

func TestClassifyCreationDeliveryEmptyProjectStaysBootstrap(t *testing.T) {
	chdirEmptyWorkspace(t)
	nd := classifyNaturalLanguageQuestionWithoutLLM("帮我搭一个空项目并生成代码", replCommandOptions{}, naturalLanguageContextREPL)
	joined := strings.Join(nd.Command, " ")
	if !strings.HasPrefix(joined, "bootstrap --apply") {
		t.Fatalf("empty scaffold must stay bootstrap, got %q", joined)
	}
}

func TestClassifyCreationDeliveryGreenfieldAPIStillRun(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "帮我从零搭一个 Go REST API，要用户注册登录、JWT、PostgreSQL、分阶段交付，先出计划和 todo"
	nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{ForceNewTask: true}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionSafeRun || len(nd.Command) == 0 || nd.Command[0] != "run" {
		t.Fatalf("greenfield API must stay run, got %+v", nd)
	}
}

func TestClassifyCreationDeliveryConcreteCLIWithoutFilenameIsWorkflow(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "create a rust cli that lists files in the current directory"
	if got := classifyCreationDelivery(input); got != creationDeliveryWorkflow {
		t.Fatalf("want workflow, got %v", got)
	}
}

func TestClassifyCreationDeliveryAnalysisStaysNone(t *testing.T) {
	chdirEmptyWorkspace(t)
	if got := classifyCreationDelivery("list all go files in the project"); got != creationDeliveryNone {
		t.Fatalf("analysis must stay none, got %v", got)
	}
}

func TestRewriteScriptAwayFromProjectWorkflow(t *testing.T) {
	plan := "做成文件去重吧，md5那种。阶段你自己排"
	got := rewriteScriptAwayFromProjectWorkflow(plan, []string{"script", "--apply", "--llm", "dedup.py", plan})
	if got[0] != "run" {
		t.Fatalf("planned product must not stay script, got %q", strings.Join(got, " "))
	}

	named := "写一个简单脚本 hello.py"
	keep := rewriteScriptAwayFromProjectWorkflow(named, []string{"script", "--apply", "--llm", "hello.py", named})
	if keep[0] != "script" {
		t.Fatalf("named script must stay script, got %q", strings.Join(keep, " "))
	}

	scaffold := "帮我搭一个空项目并生成代码"
	boot := rewriteScriptAwayFromProjectWorkflow(scaffold, []string{"bootstrap", "--apply", "--name", "app"})
	if boot[0] != "bootstrap" {
		t.Fatalf("empty scaffold must stay bootstrap, got %q", strings.Join(boot, " "))
	}
}

func TestClassifyCreationDeliveryVagueGoProjectAsks(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "帮我搞个 go 项目吧，别太复杂，你定"
	if got := classifyCreationDelivery(input); got != creationDeliveryClarify {
		t.Fatalf("want clarify, got %v", got)
	}
	for _, input := range []string{
		"make a small python project, you pick",
		"build a rust project, your call",
		"create a java project, you choose",
		"make a node project, up to you",
	} {
		if got := classifyCreationDelivery(input); got != creationDeliveryClarify {
			t.Fatalf("%q: want clarify, got %v", input, got)
		}
	}
}

func TestOffTopicChatAnswersLocally(t *testing.T) {
	for _, input := range []string{
		"你午饭吃啥了，我这边盒饭好难吃",
		"今天外面下雨吗，要不要带伞",
		"先停一下，帮我写两句打油诗夸夸哈希",
		"what is the weather today",
		"my cat stepped on the keyboard",
		"讲个没听过的冷笑话",
	} {
		nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
		if nd.Kind != naturalLanguageDecisionAnswer {
			t.Fatalf("%q: want answer, got %+v", input, nd)
		}
		if !strings.HasPrefix(nd.Reason, "talk aside") || !deterministicNLReason(nd.Reason) {
			t.Fatalf("%q: want talk aside, got %q", input, nd.Reason)
		}
		if len(nd.Command) != 0 {
			t.Fatalf("%q: talk must not run a command", input)
		}
	}
}

func TestContinueWithoutTranscriptClarifies(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "继续刚才那个，别跑偏，也不要重新开一轮"
	nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("want clarify, got %+v", nd)
	}
	if !deterministicNLReason(nd.Reason) {
		t.Fatalf("reason %q should skip the router", nd.Reason)
	}
	if strings.Contains(strings.Join(nd.Command, " "), "--new-task") {
		t.Fatalf("must not start a task, got %+v", nd)
	}
	if strings.Contains(nd.Question, "already on disk") {
		t.Fatalf("empty workspace must not claim files exist: %q", nd.Question)
	}
}

func TestColloquialSelfPlanIsWorkflowNotScript(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "用 go 写个命令行：读一个文本文件，按行统计出现次数，从高到低打印。只要标准库。阶段你自己排，别搞太大。"
	nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionSafeRun || len(nd.Command) == 0 || nd.Command[0] != "run" {
		t.Fatalf("want workflow run, got %+v", nd)
	}
	if !deterministicNLReason(nd.Reason) {
		t.Fatalf("reason %q should skip the router", nd.Reason)
	}
	joined := strings.Join(nd.Command, " ")
	if strings.Contains(joined, "script") || !strings.Contains(joined, "--new-task") {
		t.Fatalf("want new-task workflow, got %q", joined)
	}

	script := classifyNaturalLanguageQuestionWithoutLLM("写个脚本 hello.py", replCommandOptions{}, naturalLanguageContextREPL)
	scriptCmd := strings.Join(script.Command, " ")
	if script.Kind != naturalLanguageDecisionSafeRun || !strings.HasPrefix(scriptCmd, "script --apply") {
		t.Fatalf("bare script must stay a script, got %+v", script)
	}
}

func TestContinueWithFilesButNoTranscriptClarifies(t *testing.T) {
	chdirEmptyWorkspace(t)
	if err := os.WriteFile("count_lines.go", []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nd := classifyNaturalLanguageQuestionWithoutLLM("接着弄", replCommandOptions{}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("want clarify, got %+v", nd)
	}
	if !deterministicNLReason(nd.Reason) || !strings.HasPrefix(nd.Reason, "nothing to resume") {
		t.Fatalf("reason %q", nd.Reason)
	}
	if strings.Contains(strings.Join(nd.Command, " "), "--new-task") {
		t.Fatalf("must not start a task, got %+v", nd)
	}
	if !strings.Contains(nd.Question, "on disk") {
		t.Fatalf("question should mention existing files: %q", nd.Question)
	}
}

func TestCompletedDeliveryDoesNotResume(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs", "workflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".avatars", "tasks", "demo", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := "# Project Plan\n\n> **Project**: extension counter\n> **Status**: completed\n> **Active Phase**: 1\n> **Phase Count**: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docs", "workflow", "avatars_plan.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	todo := "# Active Workspace\n\n> **Phase**: 1\n\n## Phase 1 Checklist (active)\n- [x] 1. done\n"
	if err := os.WriteFile(filepath.Join(dir, "docs", "workflow", "avatars_todo.md"), []byte(todo), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".avatars", "tasks", "demo", "sessions", "run.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	nd := classifyNaturalLanguageQuestionWithoutLLM("继续刚才那个，别跑偏", replCommandOptions{}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("completed project must not resume, got %+v", nd)
	}
	if !strings.Contains(nd.Answer, "Already delivered") {
		t.Fatalf("answer=%s", nd.Answer)
	}
	if !deterministicNLReason(nd.Reason) {
		t.Fatalf("reason %q", nd.Reason)
	}
}

func TestStageGalleryQuestionAnswersLocally(t *testing.T) {
	chdirEmptyWorkspace(t)
	nd := classifyNaturalLanguageQuestionWithoutLLM("上次那个舞台页还在不，跟这次有关系吗", replCommandOptions{}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("want local stage answer, got %+v", nd)
	}
	if !strings.Contains(nd.Answer, "stage/") && !strings.Contains(nd.Answer, "No stage gallery") {
		t.Fatalf("answer=%s", nd.Answer)
	}
	if !deterministicNLReason(nd.Reason) {
		t.Fatalf("reason %q", nd.Reason)
	}
}

func TestStackSwitchAsksBeforeNewTask(t *testing.T) {
	for _, input := range []string{
		"诶能不能改成 rust，go 突然觉得没劲",
		"switch this project to python",
		"rewrite in java",
		"port to csharp",
	} {
		nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
		if nd.Kind != naturalLanguageDecisionClarify {
			t.Fatalf("%q: want clarify, got %+v", input, nd)
		}
		joined := strings.Join(nd.Command, " ")
		if strings.Contains(joined, "--new-task") {
			t.Fatalf("%q: must not auto-start a task, got %q", input, joined)
		}
	}
	keep := classifyNaturalLanguageQuestionWithoutLLM("确认，按默认把项目改成 rust", replCommandOptions{}, naturalLanguageContextREPL)
	if keep.Kind == naturalLanguageDecisionClarify && strings.Contains(keep.Reason, "language switch") {
		t.Fatal("confirmed migration must not be blocked by the switch gate")
	}
}

func TestUnknownRouterActionWithCommandIsSafeRun(t *testing.T) {
	nd := llmRouterDecisionToNaturalLanguage(llmRouterDecision{
		Action:     "bootstrap",
		Command:    []string{"bootstrap", "--apply", "--name", "todo", "--stack", "go-cli"},
		Confidence: 80,
		Reason:     "new project",
	})
	if nd.Kind != naturalLanguageDecisionSafeRun || len(nd.Command) == 0 || nd.Command[0] != "bootstrap" {
		t.Fatalf("known subcommand action should be safe_run, got %+v", nd)
	}
	unknown := llmRouterDecisionToNaturalLanguage(llmRouterDecision{Action: "bootstrap", Confidence: 80})
	if unknown.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("unknown action without a command must stay clarify, got %+v", unknown)
	}
}

func TestClassifyCreationDeliveryStrayFilesStillAsk(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"README.md", "notes.txt", "r56_nl_route.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	for _, input := range []string{
		"帮我搞个 go 项目吧，别太复杂，你定",
		"make a small python project, you pick",
		"build a rust project, your call",
		"create a java project, you choose",
		"make a node project, up to you",
	} {
		if got := classifyCreationDelivery(input); got != creationDeliveryClarify {
			t.Fatalf("%q: stray notes must still clarify, got %v", input, got)
		}
	}
}

func TestClassifyCreationDeliveryExistingProjectDoesNotAsk(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	input := "弄一个用go写的小东西，命令行能跑就行，功能你看着办"
	for _, manifest := range []string{"go.mod", "Cargo.toml", "package.json", "pyproject.toml", "pom.xml", "App.csproj"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, manifest), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", manifest, err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		if got := classifyCreationDelivery(input); got != creationDeliveryNone {
			t.Fatalf("%s: existing project must not force clarify, got %v", manifest, got)
		}
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("chdir back: %v", err)
		}
	}
}
