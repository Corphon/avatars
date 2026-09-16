package planner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsCodeImplementationGuard_MultiLangScaffold(t *testing.T) {
	cases := []string{
		"只做 Phase 1：按 phase1.md 搭脚手架。强制要求：1) go mod init example.com/blog 2) 创建 cmd/server",
		"Implement phase 1 from docs/workflow/phase1.md with pyproject.toml and pytest",
		"Create the scaffold: Cargo.toml, src/main.rs, and run cargo build",
		"按 phase1.md 实现脚手架，生成 package.json 和 src/index.ts",
	}
	for _, in := range cases {
		if !isCodeImplementationGuard(in) {
			t.Errorf("isCodeImplementationGuard(%q) = false, want true", in)
		}
		if OnlyDocTargets(in) {
			t.Errorf("OnlyDocTargets(%q) = true, want false (not doc-only)", in)
		}
	}
}

func TestLooksLikePhaseGuidedCodeWork(t *testing.T) {
	in := "按 docs/workflow/phase1.md 实现 Phase 1 脚手架"
	if !looksLikePhaseGuidedCodeWork(in) {
		t.Fatal("expected phase-guided code work")
	}
	if OnlyDocTargets(in) {
		t.Fatal("phase-guided implement must not be doc-only")
	}
	a := Assess(in)
	if a.IsTrivial() {
		t.Fatalf("Assess must not be trivial for phase-guided implement, got %+v", a)
	}
}

func TestIsContinuationPhrase_ResumeSignals(t *testing.T) {
	cases := []string{
		"继续构建",
		"从中断处继续完成 Phase 1",
		"resume from the interrupted build",
		"继续上一个被中断的构建任务",
	}
	for _, in := range cases {
		if !isContinuationPhrase(in) {
			t.Errorf("isContinuationPhrase(%q) = false", in)
		}
	}
}

func TestLooksLikeActivePhaseImplement_WithPlan(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := "> **Active Phase**: 1\n### Phase 1: Scaffold\n- **Goal**: x\n"
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	in := "按 phase1.md 实现脚手架并生成源码"
	if !looksLikeActivePhaseImplement(in) {
		t.Fatal("expected active phase implement")
	}
	a := Assess(in)
	if a.IsTrivial() {
		t.Fatalf("want non-trivial, got %+v", a)
	}
}

func TestLooksLikeTaskBenefitsFromParallelSurvey_SkipsEmptyTree(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if looksLikeTaskBenefitsFromParallelSurvey("implement an in-process LRU TTL cache library with tests") {
		t.Fatal("empty tree must not fan out 3 parallel surveys")
	}
}

func TestParallelSurveyPlan_SkipsCompactSourceTree(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "lib"+string(rune('a'+i))+".go")
		if err := os.WriteFile(name, []byte("package lib\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if isNearlyEmptyProject(".") {
		t.Fatal("5 source files is not empty")
	}
	if !isCompactSourceTree(".") {
		t.Fatal("5 source files should be a compact tree")
	}
	if _, ok := parallelSurveyPlan("implement fix wait clock", Context{}, ComplexityAssessment{}, true); ok {
		t.Fatal("compact tree must not fan out 3 parallel surveys")
	}
}

func TestParallelSurveyPlan_SkipsTwoFileLibrary(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib_test.go"), []byte("package lib\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if !isCompactSourceTree(".") {
		t.Fatal("two source files should be a compact tree")
	}
	if _, ok := parallelSurveyPlan("implement circuit breaker", Context{}, ComplexityAssessment{}, true); ok {
		t.Fatal("two-file library must not fan out 3 parallel surveys")
	}
}

func TestLooksLikeRepoAnalysisOrReport(t *testing.T) {
	analysis := []string{
		"Analyze this repository, explain what it does, and report concrete issues. Do not modify files. Summarize in ana.md",
		"Analyze this repository, find concrete issues, and write ana.md",
		"Analyze the current repository and propose a refactoring plan",
	}
	for _, in := range analysis {
		if !LooksLikeRepoAnalysisOrReport(in) {
			t.Errorf("LooksLikeRepoAnalysisOrReport(%q) = false", in)
		}
		if OnlyDocTargets(strings.ToLower(in)) && LooksLikeRepoAnalysisOrReport(in) {
			a := Assess(in)
			if a.IsTrivial() {
				t.Errorf("analysis report must not be Direct trivial: %q got %+v", in, a)
			}
		}
	}
	if LooksLikeRepoAnalysisOrReport("implement a circuit breaker library with tests") {
		t.Fatal("library implement is not analysis/report")
	}
}

func TestNeedsCriticForRefactoringAnalysis(t *testing.T) {
	task := "Analyze the current repository and propose a refactoring plan"
	if !needsCriticForMediumTask(task) {
		t.Fatal("refactoring analysis should keep Critic")
	}
	if needsCriticForMediumTask("Analyze this repository, find concrete issues, and write ana.md") {
		t.Fatal("issue-report analysis should not force Critic")
	}
}

func TestLooksLikeProgressReconcileTask(t *testing.T) {
	if !LooksLikeProgressReconcileTask("请接着把清单和真实进度对齐。用人话讲为什么这么划阶段。现在第几阶段。") {
		t.Fatal("checklist/progress NL should reconcile")
	}
	if LooksLikeProgressReconcileTask("请接着把测试修绿，go test still fails") {
		t.Fatal("test repair must not be treated as checklist-only")
	}
	if LooksLikeProgressReconcileTask("还没看到库代码，请接着写出来测试跑绿；不要 resume 数字；现在第几阶段") {
		t.Fatal("implement continue must not be treated as checklist-only")
	}
	if LooksLikeProgressReconcileTask("go test still fails；假时钟拨了回调不跑；修绿；现在第几阶段") {
		t.Fatal("failing tests + clock repair must not be checklist-only")
	}
	a := Assess("align the checklist with real progress and explain phases")
	if a.Level != ComplexitySmall {
		t.Fatalf("progress reconcile should be small, got %+v", a)
	}
	for _, role := range a.SuggestedAvatars {
		if strings.EqualFold(role, "Builder") {
			t.Fatalf("reconcile assessment must not suggest Builder, got %+v", a.SuggestedAvatars)
		}
	}
	plan := BuildForComplexity("align the checklist with real progress and explain phases", Context{}, a)
	if plan.AvatarIDByRole("Builder") != "" {
		t.Fatalf("reconcile plan must omit Builder, got %+v", plan.Avatars)
	}
	if plan.AvatarIDByRole("Planner") == "" || plan.AvatarIDByRole("Synthesizer") == "" {
		t.Fatalf("reconcile plan should keep Planner+Synthesizer, got %+v", plan.Avatars)
	}
	for _, n := range plan.Nodes {
		if strings.EqualFold(n.AssignedRole, "Builder") {
			t.Fatalf("reconcile DAG must not schedule Builder, got %+v", plan.Nodes)
		}
	}
	alreadyGreen := "代码已经写出来、go test 也绿了，但清单看起来还全是空的。请把清单和真实进度对齐。用人话讲为什么这么划阶段、现在第几阶段。"
	if !LooksLikeProgressReconcileTask(alreadyGreen) {
		t.Fatal("already-green checklist align must still be reconcile, not test-repair")
	}
	greenPlan := BuildForComplexity(alreadyGreen, Context{}, Assess(alreadyGreen))
	if greenPlan.AvatarIDByRole("Builder") != "" {
		t.Fatalf("already-green checklist align must omit Builder, got %+v", greenPlan.Avatars)
	}
}
