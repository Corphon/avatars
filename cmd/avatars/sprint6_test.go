package main

import (
	"strings"
	"testing"

	"avatars/internal/runtime"
)

func TestS6_LocalHandlersWithinBudget(t *testing.T) {
	n := len(localQuestionHandlers)
	if n > 20 {
		t.Fatalf("localQuestionHandlers=%d, want ≤20 (H-1 / S6.3)", n)
	}
	if n < 10 {
		t.Fatalf("localQuestionHandlers=%d looks too aggressive — check accidental wipe", n)
	}
}

func TestS6_MergeClarifyFollowUp(t *testing.T) {
	got := mergeClarifyFollowUp("改一下标题", "README.md 第一行")
	if got == "" || got == "改一下标题" {
		t.Fatalf("expected merged clarify follow-up, got %q", got)
	}
}

func TestS6_SmokeMatrix(t *testing.T) {
	if err := runSmokeMatrix(); err != nil {
		t.Fatal(err)
	}
}

func TestS6_NL_OfflineStatusAndShellCatchall(t *testing.T) {
	// Natural-language status questions must answer offline without shell/run.
	cases := []struct {
		q      string
		wantIn string
	}{
		{"有哪些已批准的技能", "Skills:"},
		{"多文件并行的时候 stderr 进度树怎么看", "node-survey"},
		{"审批后 Critic 会不会继续跑", "Approval resume"},
	}
	for _, tc := range cases {
		ans, ok := answerOfflineStatusQuestion(tc.q)
		if !ok {
			t.Fatalf("%q: expected offline status answer", tc.q)
		}
		if !strings.Contains(ans, tc.wantIn) {
			t.Fatalf("%q: want substring %q in %q", tc.q, tc.wantIn, ans)
		}
	}

	// Root-cause fix: bare "分析一下 auth" must NOT match shell catchall.
	if looksLikeShellAnalysisQuestion(strings.ToLower("分析一下 auth 模块")) {
		t.Fatal(`"分析一下 auth 模块" must not match shell-analysis catchall`)
	}
	// Shell-shaped counting still may match.
	if !looksLikeShellAnalysisQuestion(strings.ToLower("统计一下有多少 go 文件")) {
		t.Fatal(`"统计一下有多少 go 文件" should still match shell-shaped analysis`)
	}

	buildQ := "帮我从零搭一个 Go REST API，要用户注册登录、JWT、PostgreSQL、分阶段交付，先出计划和 todo"
	route := classifyNaturalLanguageQuestionWithoutLLM(buildQ, replCommandOptions{ForceNewTask: true}, naturalLanguageContextREPL)
	if route.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("greenfield build: want safe_run, got %s reason=%q", route.Kind, route.Reason)
	}
	if len(route.Command) == 0 || route.Command[0] != "run" {
		t.Fatalf("greenfield build: want run command, got %v", route.Command)
	}
	if looksLikeSQLQueryQuestion(strings.ToLower(buildQ)) {
		t.Fatal("greenfield build must not match sqlite-db-query heuristics")
	}

	// Read-only module analysis must offline-route to plan safe_run, not clarify.
	authRoute := classifyNaturalLanguageQuestionWithoutLLM("分析一下 auth 模块", replCommandOptions{ForceNewTask: true}, naturalLanguageContextREPL)
	if authRoute.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("auth module analysis: want safe_run, got %s reason=%q", authRoute.Kind, authRoute.Reason)
	}
	if len(authRoute.Command) < 4 || authRoute.Command[3] != "plan" {
		t.Fatalf("auth module analysis: want plan mode, got %v", authRoute.Command)
	}

	confirmRoute, handled, err := resolveNaturalLanguageQuestion("确认计划", replCommandOptions{TaskID: "demo-task"}, naturalLanguageContextREPL)
	if err != nil || !handled {
		t.Fatalf("confirm plan with task: handled=%v err=%v", handled, err)
	}
	if confirmRoute.Kind != naturalLanguageRouteSafeRun {
		t.Fatalf("confirm plan with task: want safe_run, got %s", confirmRoute.Kind)
	}
	if !strings.Contains(strings.Join(confirmRoute.Command, " "), "--task demo-task") {
		t.Fatalf("confirm plan with task: want --task demo-task, got %v", confirmRoute.Command)
	}
	if !shouldExecuteNaturalLanguageSafeRun(confirmRoute, []intentCandidate{{Summary: "next_steps", Command: []string{"repl"}}}) {
		t.Fatal("workflow confirm must win over non-generic LLM candidates")
	}
}

func TestS6_ParseRouteOptions_PermissionMode(t *testing.T) {
	options, err := parseRouteOptions([]string{"--new-task", "--permission-mode", "plan", "帮我从零搭 REST API"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.ForceNewTask {
		t.Fatal("expected --new-task")
	}
	if options.PermissionMode != runtime.PermissionModePlan {
		t.Fatalf("expected plan mode, got %q", options.PermissionMode)
	}
	if options.Input != "帮我从零搭 REST API" {
		t.Fatalf("unexpected input %q", options.Input)
	}
}

func TestEffectiveRunPermissionMode_UpgradesDefault(t *testing.T) {
	if got := effectiveRunPermissionMode(runtime.PermissionModeDefault); got != runtime.PermissionModeAcceptEdits {
		t.Fatalf("default should upgrade to acceptEdits, got %q", got)
	}
	if got := effectiveRunPermissionMode(runtime.PermissionModePlan); got != runtime.PermissionModePlan {
		t.Fatalf("plan should stay plan, got %q", got)
	}
	cmd := upgradeRunCommandPermissionMode([]string{"run", "--task", "x", "--permission-mode", "default", "build"})
	if len(cmd) < 5 || cmd[4] != "acceptEdits" {
		t.Fatalf("command upgrade failed: %v", cmd)
	}
}
