package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	memstore "avatars/internal/memory"
	"avatars/internal/tasks"
)

func TestNaturalLanguageLocalAnswersWinBeforeMistakenLLMSafeRun(t *testing.T) {
	originalAnalyzer := naturalLanguageLLMAnalyzer
	called := false
	naturalLanguageLLMAnalyzer = func(input string, options replCommandOptions, context naturalLanguageContext) (naturalLanguageDecision, bool) {
		called = true
		return naturalLanguageDecision{Kind: naturalLanguageDecisionSafeRun, Reason: "mistaken llm safe_run", Command: []string{"run", "--task", "repl-session", "--permission-mode", "plan", input}, Confidence: 90}, true
	}
	defer func() {
		naturalLanguageLLMAnalyzer = originalAnalyzer
	}()

	for _, input := range []string{"你擅长干啥呢", "你是gpt吗", "早上好"} {
		decision := classifyNaturalLanguageQuestion(input, replCommandOptions{}, naturalLanguageContextREPL)
		// P2-8: LLM runs first. Generic safe_run from LLM is converted to clarify.
		if decision.Kind != naturalLanguageDecisionClarify {
			t.Fatalf("expected LLM safe_run to be converted to clarify (P2-8) for %q, got %+v", input, decision)
		}
	}
	if !called {
		t.Fatal("expected LLM analyzer to be called first (P2-8)")
	}
}

func TestNaturalLanguageMutatingCodingUsesDefaultPermissionMode(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("修复 README.md 里的错别字", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if !strings.Contains(command, "run --task demo-task --permission-mode default") {
		t.Fatalf("expected mutating coding request to use default permission mode, got %q", command)
	}
}

func TestNaturalLanguageRepairExistingSingleFileRoutesToEdit(t *testing.T) {
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
	if err := os.WriteFile("calc.go", []byte("package main\n\nfunc Multiply(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatalf("write calc.go failed: %v", err)
	}
	decision := classifyNaturalLanguageQuestionWithoutLLM("修复 calc.go：Multiply 当前实现错误，改到 go test ./... 通过。", replCommandOptions{}, naturalLanguageContextIntent)
	command := strings.Join(decision.Command, " ")
	if !strings.HasPrefix(command, "edit --apply calc.go ") {
		t.Fatalf("expected existing single-file repair to route to edit, got %q", command)
	}
}

func TestNaturalLanguageSimpleScriptRoutesToScriptCommand(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("写一个简单脚本 hello.py", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "script --apply hello.py 写一个简单脚本 hello.py" {
		t.Fatalf("expected simple script request to route to script command, got %q", command)
	}
}

func TestNaturalLanguageInfersPythonFibonacciScriptPath(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("帮我用python写一个*斐波那契数列*脚本", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if !strings.HasPrefix(command, "script --apply --llm topic-") || !strings.Contains(command, ".py 帮我用python写一个*斐波那契数列*脚本") {
		t.Fatalf("expected structural topic script route without hardcoded topic dictionary, got %q", command)
	}
}

func TestNaturalLanguageExplicitPatchRoutesToPatchCommand(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("把 README.md 里的 old text 改成 new text", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	command := strings.Join(decision.Command, " ")
	if command != "patch --permission-mode acceptEdits README.md old text new text" {
		t.Fatalf("expected explicit replacement to route to patch command, got %q", command)
	}
}

func TestIntentSharedNaturalLanguageDirectActionOverridesGenericCandidates(t *testing.T) {
	route := naturalLanguageRoute{
		Kind:    naturalLanguageRouteSafeRun,
		Command: []string{"edit", "--apply", "internal/auto/hazard_manifest.go", "fix latest verifier failure"},
		Summary: "repair latest focused verifier failure from task memory",
	}
	candidates := []intentCandidate{
		{Command: []string{"verify"}, Summary: "run the default verifier", Confidence: 90, AutoSafe: true},
		{Command: []string{"run", "--new-task", "fix latest verifier failure"}, Summary: "run the intent as a fresh task", Confidence: 78, AutoSafe: true},
	}
	if !shouldExecuteNaturalLanguageSafeRun(route, candidates) {
		t.Fatalf("expected shared direct action route to override generic intent candidates")
	}
}

func TestNaturalLanguagePermissionMode_DispatchesThreeTiers(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"分析这个项目", "plan"},
		{"修复 README.md 里的错别字", "acceptEdits"},
		{"write a python script to print hello", "acceptEdits"},
		{"把 main.go 里的 'foo' 替换成 'bar'", "acceptEdits"},
		{"把 var x = 1 改成 var x = 2", "acceptEdits"},
		{"列出所有 task", "plan"},
	}
	for _, tc := range cases {
		got := naturalLanguagePermissionMode(tc.input)
		if got != tc.want {
			t.Fatalf("naturalLanguagePermissionMode(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestClassifyNaturalLanguageQuestion_PrefersTaskMemoryBeforeLLMFollowUp(t *testing.T) {
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
	if err := memoryStore.UpsertSummary(memstore.SummaryRecord{SessionID: "session-task-memory", RunID: "run-task-memory", TaskID: workspace.ID, Scope: memstore.ScopeTask, Role: "Synthesizer", Summary: "Task memory says the analysis exists and the follow-up should reuse memory before rereading."}); err != nil {
		t.Fatalf("record task summary failed: %v", err)
	}
	if err := memoryStore.RecordProjectLesson(memstore.ProjectLessonRecord{SourceTaskID: workspace.ID, Kind: "analysis", Summary: "Project lesson says follow-up analysis questions should reuse memory first.", Source: "warm_lesson", Confidence: "high", SupportCount: 2, SupportTasks: []string{workspace.ID}}); err != nil {
		t.Fatalf("record project lesson failed: %v", err)
	}
	decision := classifyNaturalLanguageQuestion("这个分析是否肤浅", replCommandOptions{TaskID: workspace.ID}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionMemory {
		t.Fatalf("expected memory follow-up to win before LLM, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Memory recall:") {
		t.Fatalf("expected memory recall answer, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_DirectAnswer(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("项目总共有多少文件", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected answer decision, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Project files:") {
		t.Fatalf("expected answer text, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_ProjectOverviewAnswer(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("这个项目是干什么的", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected overview answer decision, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Project overview:") {
		t.Fatalf("expected overview answer text, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_ProjectConversationalEvaluationAnswer(t *testing.T) {
	for _, input := range []string{
		"这个项目，你觉得如何？",
		"这个仓库怎么样？",
		"what do you think of this repository?",
	} {
		decision := classifyNaturalLanguageQuestion(input, replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
		if decision.Kind != naturalLanguageDecisionAnswer {
			t.Fatalf("expected conversational project answer for %q, got %+v", input, decision)
		}
		if strings.Contains(strings.Join(decision.Command, " "), "run") {
			t.Fatalf("expected no safe-run command for %q, got %+v", input, decision.Command)
		}
	}
}

func TestClassifyNaturalLanguageQuestion_ProjectCodeLocationAnswer(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Sheetforge\n\n`internal/auto/` - automatic task runner and one Go file per VBA migration task.\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	decision := classifyNaturalLanguageQuestion("如果我要增加新的自动脚本任务，应该在哪个包里写.go代码？", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected direct code-location answer, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "internal/auto/") || strings.Contains(strings.ToLower(decision.Reason), "safe") {
		t.Fatalf("expected internal/auto direct answer, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_GreenfieldLibNotStolenByCodeLocation(t *testing.T) {
	// F81: r28 tripgate prompt was answered as empty-repo overview because
	// 实现 + 自动记 + 文件在哪 tripped looksLikeProjectCodeLocationQuestion
	// before the LLM router.
	input := "我想从零做一个很小的 Go 开源库，库名就叫 tripgate。实现一个进程内熔断器，调用业务函数时自动记成功或失败。做完后告诉我做成了什么、文件在哪、怎么验证。只要库和测试。"
	if looksLikeProjectCodeLocationQuestion(strings.ToLower(input)) {
		t.Fatal("F81: greenfield library request must not match auto-script code-location Q&A")
	}
	if !looksLikeGreenfieldCreateRequest(strings.ToLower(input)) {
		t.Fatal("expected greenfield create detector to fire")
	}
	decision := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind == naturalLanguageDecisionAnswer && strings.Contains(decision.Answer, "Project overview:") {
		t.Fatalf("F81: must not steal create-lib NL as project overview, got %+v", decision)
	}
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("F81: offline fallback should safe_run greenfield create, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_ProjectManifestAnswer(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("这个项目的 manifest 和依赖是什么", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected manifest answer decision, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Project manifest summary:") {
		t.Fatalf("expected manifest answer text, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_GuardsDestructiveIntent(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("delete all files in project", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionGuarded {
		t.Fatalf("expected guarded decision, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_ClarifiesOutOfScopeIntent(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("what is the weather today", replCommandOptions{}, naturalLanguageContextIntent)
	if decision.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("expected clarify decision, got %+v", decision)
	}
	if !strings.Contains(strings.ToLower(decision.Reason), "outside bounded project task scope") {
		t.Fatalf("expected bounded-scope clarification reason, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_StillRoutesProjectWorkToSafeRun(t *testing.T) {
	for _, input := range []string{
		"why is this repository analysis shallow",
		"分析项目，找问题，写入 anal.md",
		"analyze this repository and write a report",
	} {
		decision := classifyNaturalLanguageQuestion(input, replCommandOptions{}, naturalLanguageContextIntent)
		if decision.Kind != naturalLanguageDecisionSafeRun {
			t.Fatalf("expected safe run decision for project work %q, got %+v", input, decision)
		}
		if len(decision.Command) == 0 || decision.Command[0] != "run" {
			t.Fatalf("expected safe run command for %q, got %+v", input, decision.Command)
		}
	}
}

func TestClassifyNaturalLanguageQuestion_ExplicitProjectWorkPreemptsLLM(t *testing.T) {
	originalAnalyzer := naturalLanguageLLMAnalyzer
	called := false
	naturalLanguageLLMAnalyzer = func(input string, options replCommandOptions, context naturalLanguageContext) (naturalLanguageDecision, bool) {
		called = true
		return naturalLanguageDecision{Kind: naturalLanguageDecisionClarify, Reason: "llm should not be called", Confidence: 90}, true
	}
	defer func() {
		naturalLanguageLLMAnalyzer = originalAnalyzer
	}()

	decision := classifyNaturalLanguageQuestion("分析项目，找问题，写入 smoke.md", replCommandOptions{}, naturalLanguageContextIntent)
	// P2-8: LLM runs first. Even if mistaken, LLM output is authoritative.
	if decision.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("expected LLM clarify to be used (P2-8), got %+v", decision)
	}
	if !called {
		t.Fatal("expected LLM analyzer to be called first (P2-8)")
	}
}

func TestClassifyNaturalLanguageWithLLMClient_SafeRun(t *testing.T) {
	decision, ok := classifyNaturalLanguageWithLLMClient(
		"Explain repo status",
		replCommandOptions{},
		naturalLanguageContextIntent,
		stubNaturalLanguageLLM{response: `{"action":"safe_run","confidence":83}`},
	)
	if !ok {
		t.Fatal("expected llm decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected safe_run decision, got %+v", decision)
	}
	if len(decision.Command) == 0 || decision.Command[0] != "run" {
		t.Fatalf("expected safe run command, got %+v", decision.Command)
	}
}

func TestClassifyNaturalLanguageWithLLMClient_Guarded(t *testing.T) {
	decision, ok := classifyNaturalLanguageWithLLMClient(
		"delete all files",
		replCommandOptions{},
		naturalLanguageContextIntent,
		stubNaturalLanguageLLM{response: `{"action":"guarded","confidence":99}`},
	)
	if !ok {
		t.Fatal("expected llm guarded decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionGuarded {
		t.Fatalf("expected guarded decision, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageWithLLMClient_Clarify(t *testing.T) {
	decision, ok := classifyNaturalLanguageWithLLMClient(
		"do the thing",
		replCommandOptions{},
		naturalLanguageContextIntent,
		stubNaturalLanguageLLM{response: `{"action":"clarify","question":"Which project task should avatars run?","confidence":71}`},
	)
	if !ok {
		t.Fatal("expected llm clarify decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("expected clarify decision, got %+v", decision)
	}
	if decision.Reason != "Which project task should avatars run?" {
		t.Fatalf("expected clarify question as reason, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageWithLLMClient_DirectAnswerInREPLContext(t *testing.T) {
	decision, ok := classifyNaturalLanguageWithLLMClient(
		"这个项目，你觉得如何？",
		replCommandOptions{TaskID: "demo-task"},
		naturalLanguageContextREPL,
		stubNaturalLanguageLLM{response: `{"action":"direct_answer","answer_id":"project_opinion","confidence":91}`},
	)
	if !ok {
		t.Fatal("expected llm direct answer decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected direct answer decision, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageWithLLMClient_DirectAnswerUsesAnswerID(t *testing.T) {
	decision, ok := classifyNaturalLanguageWithLLMClient(
		"你好",
		replCommandOptions{},
		naturalLanguageContextREPL,
		stubNaturalLanguageLLM{response: `{"action":"direct_answer","answer_id":"greeting","confidence":90}`},
	)
	if !ok {
		t.Fatal("expected llm direct answer decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected direct answer decision, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Hi.") && !strings.Contains(decision.Answer, "Capabilities:") {
		t.Fatalf("expected classified answer content, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageWithLLMClient_CapabilityQuestionIsDirectAnswer(t *testing.T) {
	decision, ok := classifyNaturalLanguageWithLLMClient(
		"你能写代码吗",
		replCommandOptions{},
		naturalLanguageContextREPL,
		stubNaturalLanguageLLM{response: `{"action":"direct_answer","answer_id":"capabilities","confidence":90}`},
	)
	if !ok {
		t.Fatal("expected llm direct answer decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected direct answer decision, got %+v", decision)
	}
	if strings.Contains(decision.Reason, "safe task") {
		t.Fatalf("expected capability question to avoid safe-run reason, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Capabilities:") {
		t.Fatalf("expected capabilities answer, got %q", decision.Answer)
	}
}

func TestClassifyNaturalLanguageWithLLMClient_LocalCapabilityQuestionWinsBeforeLLMMissafeRun(t *testing.T) {
	originalAnalyzer := naturalLanguageLLMAnalyzer
	called := false
	naturalLanguageLLMAnalyzer = func(input string, options replCommandOptions, context naturalLanguageContext) (naturalLanguageDecision, bool) {
		called = true
		return naturalLanguageDecision{Kind: naturalLanguageDecisionSafeRun, Reason: "mistaken llm safe_run", Command: []string{"run", "--task", "demo-task", "--permission-mode", "plan", input}, Confidence: 90}, true
	}
	defer func() {
		naturalLanguageLLMAnalyzer = originalAnalyzer
	}()

	decision := classifyNaturalLanguageQuestion("你是gpt吗", replCommandOptions{}, naturalLanguageContextIntent)
	// P2-8: LLM runs first. Even when mistaken, LLM output is authoritative.
	// Generic safe_run is converted to clarify.
	if decision.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("expected LLM safe_run to be converted to clarify (P2-8), got %+v", decision)
	}
	if !called {
		t.Fatal("expected LLM analyzer to be called first (P2-8)")
	}
}

func TestClassifyNaturalLanguageWithLLMClient_EnglishModelIdentityQuestionWinsBeforeLLM(t *testing.T) {
	originalAnalyzer := naturalLanguageLLMAnalyzer
	called := false
	naturalLanguageLLMAnalyzer = func(input string, options replCommandOptions, context naturalLanguageContext) (naturalLanguageDecision, bool) {
		called = true
		return naturalLanguageDecision{Kind: naturalLanguageDecisionClarify, Reason: "mistaken clarify", Confidence: 60}, true
	}
	defer func() {
		naturalLanguageLLMAnalyzer = originalAnalyzer
	}()

	decision := classifyNaturalLanguageQuestion("are you gpt", replCommandOptions{}, naturalLanguageContextREPL)
	// P2-8: LLM runs first. LLM's clarify decision is authoritative.
	if decision.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("expected LLM clarify to be used (P2-8), got %+v", decision)
	}
	if !called {
		t.Fatal("expected LLM analyzer to be called first (P2-8)")
	}
}

// TODO-01 (P0): When the LLM classifier returns safe_run but the spec-builder
// fell through to a generic "run --permission-mode plan" command (no specific
// script/edit/bootstrap intent matched), the outer classifier must convert
// the decision to Clarify. The user said: "不知道就说不知道，乱输出就麻烦了".
// Otherwise the system silently dispatches a vague input as a plan-mode task
// without the user confirming the target.

func TestClassifyNaturalLanguageQuestion_LLMSafeRunFallsThroughToClarifyWhenCommandIsGeneric(t *testing.T) {
	originalAnalyzer := naturalLanguageLLMAnalyzer
	naturalLanguageLLMAnalyzer = func(input string, options replCommandOptions, context naturalLanguageContext) (naturalLanguageDecision, bool) {
		// Simulate a misclassification: the LLM says safe_run, but the resulting
		// command is the generic plan-mode fallback (the兜底 path inside
		// buildNaturalLanguageTaskCommandWithLLM when no specific intent matches).
		return naturalLanguageDecision{
			Kind:       naturalLanguageDecisionSafeRun,
			Reason:     "llm classified as safe task: run the safe question as a fresh task",
			Command:    []string{"run", "--new-task", "--permission-mode", "plan", input},
			Confidence: 90,
		}, true
	}
	defer func() {
		naturalLanguageLLMAnalyzer = originalAnalyzer
	}()

	// Input must NOT match any deterministic explicit route (so the LLM path
	// at classifyNaturalLanguageQuestionWithOptions line 419 is actually
	// exercised) and must NOT match looksLikeProjectWorkRequest /
	// looksLikeNaturalLanguageRequest (so the post-LLM fallback at line 432
	// would otherwise route to a safe_run plan task via the deterministic
	//兜底). "monitor background activity" is vague: it has no "analyze" /
	// "find" / "fix" / "implement" / etc. trigger, no script keyword, no
	// bootstrap keyword, and no question mark for looksLikeNaturalLanguageRequest.
	decision := classifyNaturalLanguageQuestion("monitor background activity", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("expected clarify for vague LLM-classified safe_run with generic run command, got %+v", decision)
	}
	if !strings.Contains(decision.Reason, "clarify") && !strings.Contains(decision.Reason, "specific") {
		t.Fatalf("expected clarify reason to mention disambiguation, got %+v", decision)
	}
}

// TODO-01 (P0): Defensive companion. The fix must only target generic run
// fallbacks. When the LLM returns safe_run with a SPECIFIC command shape
// (script, edit, bootstrap, patch), the decision must survive unchanged —
// otherwise the fix would block every legitimate LLM-classified task.

func TestClassifyNaturalLanguageQuestion_LLMSafeRunWithSpecificCommandSurvives(t *testing.T) {
	cases := []struct {
		label   string
		command []string
	}{
		{"script_apply", []string{"script", "--apply", "task.py", "write a script that prints hello"}},
		{"edit_apply", []string{"edit", "--apply", "main.go", "rename foo to bar"}},
		{"bootstrap_apply", []string{"bootstrap", "--apply", "--name", "demo"}},
		{"patch", []string{"patch", "--permission-mode", "acceptEdits", "main.go", "old text", "new text"}},
	}
	for _, tc := range cases {
		originalAnalyzer := naturalLanguageLLMAnalyzer
		naturalLanguageLLMAnalyzer = func(input string, options replCommandOptions, context naturalLanguageContext) (naturalLanguageDecision, bool) {
			return naturalLanguageDecision{
				Kind:       naturalLanguageDecisionSafeRun,
				Reason:     "llm classified as safe task: specific intent matched",
				Command:    tc.command,
				Confidence: 92,
			}, true
		}
		decision := classifyNaturalLanguageQuestion("please do the thing", replCommandOptions{}, naturalLanguageContextREPL)
		naturalLanguageLLMAnalyzer = originalAnalyzer
		if decision.Kind != naturalLanguageDecisionSafeRun {
			t.Fatalf("%s: expected safe_run for LLM-classified specific command, got %+v", tc.label, decision)
		}
		if len(decision.Command) != len(tc.command) {
			t.Fatalf("%s: expected command to be preserved %v, got %v", tc.label, tc.command, decision.Command)
		}
	}
}

// TODO-01 (P0): Direct unit test for the helper that distinguishes generic
// run fallbacks from specific intent commands. This is the contract the outer
// classifier relies on; if it returns the wrong shape, the routing either
// over-clarifies (blocking legitimate tasks) or under-clarifies (regressing
// the bug we just fixed).

func TestIsGenericRunFallbackCommand(t *testing.T) {
	cases := []struct {
		label   string
		command []string
		want    bool
	}{
		{"fresh_run_plan_fallback", []string{"run", "--new-task", "--permission-mode", "plan", "do thing"}, true},
		{"named_run_plan_fallback", []string{"run", "--task", "demo-task", "--permission-mode", "plan", "do thing"}, true},
		{"run_with_default_mode_is_not_fallback", []string{"run", "--task", "demo", "--permission-mode", "default", "do thing"}, false},
		{"script_apply_survives", []string{"script", "--apply", "task.py", "do thing"}, false},
		{"edit_apply_survives", []string{"edit", "--apply", "main.go", "do thing"}, false},
		{"bootstrap_apply_survives", []string{"bootstrap", "--apply", "--name", "demo"}, false},
		{"patch_survives", []string{"patch", "--permission-mode", "acceptEdits", "main.go", "old", "new"}, false},
		{"empty_command", []string{}, false},
		{"non_run_command", []string{"script", "--permission-mode", "plan", "do thing"}, false},
		{"run_without_permission_mode", []string{"run", "--task", "demo", "do thing"}, false},
		{"run_with_plan_not_adjacent", []string{"run", "--task", "demo", "plan", "do thing"}, false},
	}
	for _, tc := range cases {
		got := isGenericRunFallbackCommand(tc.command)
		if got != tc.want {
			t.Fatalf("%s: isGenericRunFallbackCommand(%v) = %v, want %v", tc.label, tc.command, got, tc.want)
		}
	}
}

func TestClassifyNaturalLanguageWithLLMClient_MemoryAnswerDoesNotNeedFollowUpWords(t *testing.T) {
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
	if err := memoryStore.UpsertSummary(memstore.SummaryRecord{SessionID: "session-memory", RunID: "run-memory", TaskID: workspace.ID, Scope: memstore.ScopeTask, Role: "Synthesizer", Summary: "Task memory says Sheetforge is an Excel and SQL workbench."}); err != nil {
		t.Fatalf("record task summary failed: %v", err)
	}

	decision, ok := classifyNaturalLanguageWithLLMClient(
		"Sheetforge 能用来干啥",
		replCommandOptions{TaskID: workspace.ID},
		naturalLanguageContextREPL,
		stubNaturalLanguageLLM{response: `{"action":"memory_answer","answer_id":"project_overview","confidence":88}`},
	)
	if !ok {
		t.Fatal("expected llm memory decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionMemory {
		t.Fatalf("expected LLM-classified memory answer, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Memory recall:") || !strings.Contains(decision.Answer, "Excel and SQL workbench") {
		t.Fatalf("expected memory recall content, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageWithLLMClient_DirectProjectOpinionReusesMemory(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Demo\n\nFresh survey should not be needed.\n"), 0o644); err != nil {
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
	if err := memoryStore.UpsertSummary(memstore.SummaryRecord{SessionID: "session-memory", RunID: "run-memory", TaskID: workspace.ID, Scope: memstore.ScopeTask, Role: "Synthesizer", Summary: "Task memory says the last project review already found the report was shallow."}); err != nil {
		t.Fatalf("record task summary failed: %v", err)
	}

	decision, ok := classifyNaturalLanguageWithLLMClient(
		"你觉得刚才那个分析靠谱不",
		replCommandOptions{TaskID: workspace.ID},
		naturalLanguageContextREPL,
		stubNaturalLanguageLLM{response: `{"action":"direct_answer","answer_id":"project_opinion","confidence":90}`},
	)
	if !ok {
		t.Fatal("expected llm direct answer decision to parse")
	}
	if decision.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("expected direct answer decision, got %+v", decision)
	}
	if !strings.Contains(decision.Answer, "Memory recall:") || strings.Contains(decision.Answer, "Project overview:") {
		t.Fatalf("expected direct answer to reuse task memory, got %+v", decision)
	}
}

func TestClassifyNaturalLanguageQuestion_ExplicitWorkSurvivesBadLLMDirectAnswer(t *testing.T) {
	decision := classifyNaturalLanguageQuestion("分析项目，找问题，写入 anal.md", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected explicit work to fall through to safe run, got %+v", decision)
	}
}

func TestNaturalLanguageClarifyingQuestion_DefaultsToActionablePrompt(t *testing.T) {
	question := naturalLanguageClarifyingQuestion("not a natural-language request")
	if !strings.Contains(question, "next step") || !strings.Contains(question, "file path") || !strings.Contains(question, "action") {
		t.Fatalf("expected user-centered clarification question, got %q", question)
	}
}

func TestIntentRoute_ConcreteWorkRunsThroughSharedNaturalLanguageRoute(t *testing.T) {
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
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}
	output := captureRunOutput(t, []string{"inspect README.md and summarize runtime readiness"})
	if !strings.Contains(output, "Intent route: run the safe question as a fresh task") {
		t.Fatalf("expected bare intent to run as task, got %s", output)
	}
	if !strings.Contains(output, "Executing: avatars run --new-task") {
		t.Fatalf("expected bare intent to force a fresh task, got %s", output)
	}
	if !strings.Contains(output, "Task:") || !strings.Contains(output, "Transcript:") {
		t.Fatalf("expected task run output, got %s", output)
	}
}

func TestNaturalLanguageQuestionRouter_RejectsDestructiveQuestions(t *testing.T) {
	_, handled, err := resolveNaturalLanguageQuestion("delete all files in project", replCommandOptions{}, naturalLanguageContextREPL)
	if err == nil || !strings.Contains(err.Error(), "ambiguous or risky") {
		t.Fatalf("expected destructive natural language to stay guarded, got %v", err)
	}
	if handled {
		t.Fatal("expected destructive question to remain unhandled for direct answer routing")
	}
}

func TestNaturalLanguageDestructiveGuardAllowsFeatureImplementation(t *testing.T) {
	input := "给这个 note-taker CLI 增加 delete 命令：支持按 ID 删除笔记，并运行 go test ./... 验证。"
	decision := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextIntent)
	if decision.Kind == naturalLanguageDecisionGuarded {
		t.Fatalf("expected delete-command feature implementation not to be guarded")
	}
}

func TestNaturalLanguageRoutesExplicitMultiFileImplementationToEditMany(t *testing.T) {
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
	for _, path := range []string{"README.md", "main.go", "notes.go", "notes_test.go"} {
		if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", path, err)
		}
	}
	input := "给 CLI 增加 delete 命令；同步更新 README.md、main.go、notes.go、notes_test.go，并运行 go test ./... 验证。"
	decision := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextIntent)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("expected safe run edit-many decision, got %+v", decision)
	}
	if len(decision.Command) < 3 || decision.Command[0] != "edit-many" || decision.Command[1] != "--apply" {
		t.Fatalf("expected edit-many command, got %+v", decision.Command)
	}
}

func TestRoutePreviewUsesSharedNaturalLanguageClassification(t *testing.T) {
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
	output := captureRunOutput(t, []string{"route", "你是什么大模型"})
	if !strings.Contains(output, "Natural language route: direct_answer") {
		t.Fatalf("expected direct answer preview, got %s", output)
	}
	if !strings.Contains(output, "Model identity:") {
		t.Fatalf("expected model identity preview, got %s", output)
	}
}

func TestRouteCLIExplainsNaturalLanguageWithoutExecuting(t *testing.T) {
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
	output := captureRunOutput(t, []string{"route", "这个项目是干什么的"})
	if !strings.Contains(output, "Natural language route: direct_answer") {
		t.Fatalf("expected direct answer route, got %s", output)
	}
	if !strings.Contains(output, "Answer preview:") || !strings.Contains(output, "Project overview:") {
		t.Fatalf("expected answer preview, got %s", output)
	}
}

func TestRouteCLIPrefersNaturalLanguageForFollowUpOpinion(t *testing.T) {
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
	output := captureRunOutput(t, []string{"route", "--task", "demo-task", "你认为这个项目构建的如何？"})
	if !strings.Contains(output, "Natural language route: direct_answer") {
		t.Fatalf("expected natural language route, got %s", output)
	}
	if strings.Contains(output, "Intent candidates:") {
		t.Fatalf("expected no intent candidates for follow-up opinion, got %s", output)
	}
	if !strings.Contains(output, "Memory recall:") && !strings.Contains(output, "Project opinion:") {
		t.Fatalf("expected memory or opinion preview, got %s", output)
	}
}

func TestRouteCLIPrefersNaturalLanguageForModelIdentity(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join("configs", "agent.yaml"), []byte("llm:\n  provider: deepseek\n  model: deepseek-chat\n"), 0o644); err != nil {
		t.Fatalf("write agent config failed: %v", err)
	}
	output := captureRunOutput(t, []string{"route", "你是什么大模型"})
	if !strings.Contains(output, "Natural language route: direct_answer") {
		t.Fatalf("expected natural language route, got %s", output)
	}
	if strings.Contains(output, "Intent candidates:") {
		t.Fatalf("expected no intent candidates for model identity, got %s", output)
	}
	if !strings.Contains(output, "Model identity:") {
		t.Fatalf("expected model identity preview, got %s", output)
	}
}

func TestNaturalLanguageClassifierSystemPrompt_IncludesOperatingContract(t *testing.T) {
	prompt := naturalLanguageClassifierSystemPrompt()
	for _, want := range []string{
		"Interpret natural language first; do not route by keyword guess alone.",
		"If task memory or the latest result can answer, choose memory_answer before scanning or running again.",
		"Choose clarify when the goal, target path, or requested action is missing; ask the smallest concrete question.",
		"User-facing responses should answer first, then give one short reason or next step.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected classifier prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestBuildNaturalLanguageClassifierContextIncludesFileContent(t *testing.T) {
	// Verify that the classifier context includes full file content
	// when a file is attached, not just the title and first line.
	originalFileContext := getReplFileContext()
	defer func() { setReplFileContext(originalFileContext) }()

	setReplFileContext(&fileContextState{
		Path:    "test_script.py",
		Content: "#!/usr/bin/env python3\nprint('hello world')\nimport os\nos.listdir('.')\n",
	})

	ctx := buildNaturalLanguageClassifierContext(replCommandOptions{}, naturalLanguageContextIntent)
	if !strings.Contains(ctx, "active_file_content:") {
		t.Fatal("expected classifier context to include active_file_content when file is attached")
	}
	if !strings.Contains(ctx, "test_script.py") {
		t.Fatal("expected classifier context to include file path")
	}
	// Verify full content is present (not just a truncated preview)
	if !strings.Contains(ctx, "os.listdir('.')") {
		t.Fatal("expected classifier context to include full file content, not just first-line preview")
	}

	// No file context — should NOT include content section.
	setReplFileContext(nil)
	ctx = buildNaturalLanguageClassifierContext(replCommandOptions{}, naturalLanguageContextIntent)
	if strings.Contains(ctx, "active_file_content:") {
		t.Fatal("expected no active_file_content when no file is attached")
	}
}

func TestBuildNaturalLanguageClassifierContextTruncatesLargeFile(t *testing.T) {
	// Verify that very large file content gets truncated at 4000 chars
	// with a truncation notice, not silently clipped.
	originalFileContext := getReplFileContext()
	defer func() { setReplFileContext(originalFileContext) }()

	largeContent := strings.Repeat("x", 5000)
	setReplFileContext(&fileContextState{
		Path:    "huge_plan.md",
		Content: largeContent,
	})

	ctx := buildNaturalLanguageClassifierContext(replCommandOptions{}, naturalLanguageContextIntent)
	if !strings.Contains(ctx, "active_file_content:") {
		t.Fatal("expected classifier context to include active_file_content for large file")
	}
	if !strings.Contains(ctx, "[truncated: file content exceeded 4000 chars]") {
		t.Fatal("expected truncation notice for files exceeding 4000 chars")
	}
	// Verify the truncated portion is exactly 4000 chars of content
	// (the first 4000 'x' chars should be present)
	if !strings.Contains(ctx, strings.Repeat("x", 4000)) {
		t.Fatal("expected first 4000 chars of file content to be preserved")
	}
}

// --- BUG-3: Real-world @file routing failures ---
