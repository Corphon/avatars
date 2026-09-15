package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF91_MechanicalAnswersYieldToGreenfieldBrief(t *testing.T) {
	// r30 live: these phrases appeared inside a real library-build NL and
	// used to steal Intent into phase-status / conversational-repair.
	with人话 := strings.ToLower(`帮我从零做一个开源风格的 Go 库 breakgate：进程内熔断器。
要求写齐单元测试；做完用人话写交付说明。不要替我拆成第几阶段，你自己看着规划就行。`)
	if !mechanicalLocalAnswersYieldToWork(with人话) {
		t.Fatal("expected work yield for greenfield breakgate brief")
	}
	if looksLikeConversationalRepairQuestion(with人话) {
		t.Fatal("F91: 「人话」inside build brief must not trigger conversational repair")
	}
	if _, ok := answerWorkflowPhaseStatusQuestion(with人话); ok {
		t.Fatal("F91: 「第几阶段」negation inside build brief must not answer phase status")
	}
}

func TestF91_ShortStyleComplaintStillRepairs(t *testing.T) {
	for _, in := range []string{"说人话", "你说人话好不好", "听不懂你在说什么"} {
		if !looksLikeConversationalRepairQuestion(strings.ToLower(in)) {
			t.Fatalf("short repair complaint should still match: %q", in)
		}
	}
}

func TestResumeWorkYieldsPastPhaseStatusQuestion(t *testing.T) {
	in := "刚才那轮没过门：go test 会卡住。请接着把测试修绿。做完用大白话说现在第几阶段、还差什么。"
	lowered := strings.ToLower(in)
	if !looksLikeResumeContinuationIntent(lowered) {
		t.Fatal("expected resume continuation")
	}
	if !mechanicalLocalAnswersYieldToWork(lowered) {
		t.Fatal("resume+phase-status must yield to work, not local Q&A")
	}
	if _, ok := answerWorkflowPhaseStatusQuestion(in); ok {
		t.Fatal("must not steal a repair request that also asks which phase")
	}
	en := "Please continue and make the tests pass. When done, say which phase we are in and what is left."
	if _, ok := answerWorkflowPhaseStatusQuestion(en); ok {
		t.Fatal("English repair+phase ask must not answer phase status locally")
	}
}

func TestF91_ExplicitPhaseStatusStillAnswers(t *testing.T) {
	in := "当前做到第几阶段了？"
	if mechanicalLocalAnswersYieldToWork(strings.ToLower(in)) {
		t.Fatal("pure status ask must not look like coding work")
	}
	if _, ok := answerWorkflowPhaseStatusQuestion(in); !ok {
		t.Fatal("explicit phase status ask should still answer locally")
	}
}

func TestPhasePlanningAskBeatsRepairNote(t *testing.T) {
	cases := []string{
		"用人话讲讲你这次是怎么划分阶段的，为什么这样划。现在做到哪了。不要再改代码。",
		"直接回答我：这次项目你自己划了几个阶段，依据是什么，现在进行到哪。不要跑任务，不要改文件。",
		"How many phases did you plan and where are we? Do not edit files.",
		"How did you divide the phases and why this split? Do not run a task.",
	}
	for _, in := range cases {
		lowered := strings.ToLower(in)
		if looksLikeConversationalRepairQuestion(lowered) {
			t.Fatalf("planning/status ask must not look like repair: %q", in)
		}
		if !looksLikeWorkflowPhaseStatusAsk(lowered) {
			t.Fatalf("expected phase-status ask: %q", in)
		}
		if _, ok := answerWorkflowPhaseStatusQuestion(in); !ok {
			t.Fatalf("planning/status ask must answer locally: %q", in)
		}
		d := classifyNaturalLanguageQuestionWithoutLLM(in, replCommandOptions{}, naturalLanguageContextIntent)
		if d.Kind != naturalLanguageDecisionAnswer {
			t.Fatalf("kind=%s reason=%q for %q", d.Kind, d.Reason, in)
		}
		if strings.Contains(d.Answer, "Repair note:") {
			t.Fatalf("got repair note for %q:\n%s", in, d.Answer)
		}
	}
}

func TestNoMutateFollowUpsDoNotLaunchWork(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ring.go"), []byte("package ring\n\nfunc New(n int) *Buf { return nil }\nfunc (b *Buf) Push(v int) {}\nfunc (b *Buf) TryPush(v int) bool { return false }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := "> **Status**: completed\n> **Active Phase**: 1\n> **Phase Count**: 1\n\n## Key Decisions\n| Date | Decision | Rationale |\n|------|----------|-----------|\n| today | Push overwrites; TryPush rejects | user left overflow open |\n"
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("## Phase 1 Checklist\n- [x] a\n- [x] b\n- [ ] c\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cases := []struct {
		in   string
		want []string
		ban  []string
	}{
		{
			in:   "对了你觉得容量满了该覆盖还是该阻塞？先别改代码，就用人话说说。我中午吃了拉面，别往项目里加吃的。",
			want: []string{"Exported API", "Push"},
			ban:  []string{"Repair note:"},
		},
		{
			in:   "这个和 Go 的 channel 有啥区别啊，大白话就行，别动文件也别开新任务。",
			want: []string{"in-process"},
			ban:  []string{"Repair note:", "acceptEdits"},
		},
		{
			in:   "这跟 sony/gobreaker 或者 hystrix 有啥区别？别开任务，别改代码，直接说。",
			want: []string{"in-process"},
			ban:  []string{"Repair note:", "acceptEdits", "language channel"},
		},
		{
			in:   "跳闸之后是直接拒绝还是排队等冷却？用人话说，别改代码。对了中午想吃黄焖鸡。",
			want: []string{"Exported API"},
			ban:  []string{"Repair note:"},
		},
		{
			in:   "如果短时间连点，只留最后一次还是每次调用也排队跑？用人话说，先别写代码。对了中午想喝珍珠奶茶。",
			want: []string{"Push"},
			ban:  []string{"Repair note:", "last-write", "debounce"},
		},
		{
			in:   "和 time.AfterFunc / lodash.debounce 比有啥区别？别开任务，别改代码，直接说。",
			want: []string{"AfterFunc"},
			ban:  []string{"Repair note:", "acceptEdits", "--new-task"},
		},
		{
			in:   "先定一下 API：Allow 要不要返回 error？RecordFailure 要不要带 error。先别写代码，直接答。",
			want: []string{"Exported API"},
			ban:  []string{"Repair note:", "acceptEdits"},
		},
		{
			in:   "等等我是不是该先定 API？Push 现在返回值是啥，满了会怎样？先别写代码，直接答。",
			want: []string{"Exported API", "Push"},
			ban:  []string{"Repair note:"},
		},
		{
			in:   "哦对了周末有空吗——算了当我没问。回话里清单一会儿 0/3 一会儿做完了，到底完成没有？就解释，别改代码别开任务。",
			want: []string{"Plan status", "no test files"},
			ban:  []string{"Repair note:"},
		},
		{
			in:   "要不给它加个 HTTP 接口方便测？——开玩笑的，当我没说。还是进程内库，别改方向，也别改文件。",
			want: []string{"withdrawn", "in-process"},
			ban:  []string{"Repair note:"},
		},
		{
			in:   "Should Push overwrite or block when full? Do not edit files. Just answer.",
			want: []string{"Exported API"},
			ban:  []string{"Repair note:"},
		},
	}
	for _, tc := range cases {
		if looksLikeConversationalRepairQuestion(strings.ToLower(tc.in)) {
			t.Fatalf("must not look like repair: %q", tc.in)
		}
		d := classifyNaturalLanguageQuestionWithoutLLM(tc.in, replCommandOptions{}, naturalLanguageContextIntent)
		if d.Kind != naturalLanguageDecisionAnswer {
			t.Fatalf("kind=%s reason=%q for %q", d.Kind, d.Reason, tc.in)
		}
		if d.Kind == naturalLanguageDecisionSafeRun {
			t.Fatalf("must not safe_run: %q", tc.in)
		}
		for _, w := range tc.want {
			if !strings.Contains(d.Answer, w) {
				t.Fatalf("missing %q in answer for %q:\n%s", w, tc.in, d.Answer)
			}
		}
		for _, b := range tc.ban {
			if strings.Contains(d.Answer, b) {
				t.Fatalf("banned %q in answer for %q:\n%s", b, tc.in, d.Answer)
			}
		}
	}
}

func TestDiskSemanticsCoalesceAndWaitGroup(t *testing.T) {
	dir := t.TempDir()
	src := "package flight\n\nfunc (g *Group) Do(key string, fn func() (any, error)) (any, error, bool) { return nil, nil, false }\nfunc (g *Group) DoChan(key string, fn func() (any, error)) <-chan Result { return nil }\nfunc (g *Group) Forget(key string) {}\n"
	if err := os.WriteFile(filepath.Join(dir, "flight.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	share := "如果同一 key 短时间来一堆调用，是只执行一次大家共享结果，还是每个都排队跑一遍？用人话说，先别写代码。对了中午想喝珍珠奶茶。"
	d := classifyNaturalLanguageQuestionWithoutLLM(share, replCommandOptions{}, naturalLanguageContextIntent)
	if d.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("kind=%s reason=%q", d.Kind, d.Reason)
	}
	if !strings.Contains(d.Answer, "share one") && !strings.Contains(d.Answer, "Forget") {
		t.Fatalf("want coalesce semantics, got:\n%s", d.Answer)
	}
	if strings.Contains(d.Answer, "last-write") || strings.Contains(d.Answer, "debounce") {
		t.Fatalf("must not slap debounce onto coalesce disk:\n%s", d.Answer)
	}

	cmp := "这个和 errgroup 或者 WaitGroup 有啥区别？别开任务，别改代码，直接说。"
	d2 := classifyNaturalLanguageQuestionWithoutLLM(cmp, replCommandOptions{}, naturalLanguageContextIntent)
	if d2.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("kind=%s reason=%q", d2.Kind, d2.Reason)
	}
	if !strings.Contains(d2.Answer, "wait-group") || !strings.Contains(d2.Answer, "same key") {
		t.Fatalf("want wait-group vs coalesce, got:\n%s", d2.Answer)
	}
	if strings.Contains(d2.Answer, "Repair note:") || strings.Contains(d2.Answer, "--new-task") {
		t.Fatalf("must not launch work:\n%s", d2.Answer)
	}
}

func TestDiskSemanticsFIFOAndHeapOrder(t *testing.T) {
	dir := t.TempDir()
	src := "package pqueue\n\n// FIFO insertion order. Higher priorities pop first (max-heap).\nfunc (q *Queue[T]) Push(item T, priority int) {}\nfunc (q *Queue[T]) Pop() (T, bool) { var z T; return z, false }\nfunc (h inner[T]) Less(i, j int) bool { return h[i].prio > h[j].prio }\n"
	if err := os.WriteFile(filepath.Join(dir, "pqueue.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	fifo := "同优先级要是连着 Push，后进的会不会插队？用人话说，先别写代码。"
	d := classifyNaturalLanguageQuestionWithoutLLM(fifo, replCommandOptions{}, naturalLanguageContextIntent)
	if d.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("kind=%s reason=%q", d.Kind, d.Reason)
	}
	if !strings.Contains(d.Answer, "FIFO") && !strings.Contains(d.Answer, "insertion order") {
		t.Fatalf("want FIFO semantics, got:\n%s", d.Answer)
	}

	ord := "我当初说数字越小越优先，你现在是越大越先出还是越小越先出？先别写代码，直接答。"
	d2 := classifyNaturalLanguageQuestionWithoutLLM(ord, replCommandOptions{}, naturalLanguageContextIntent)
	if d2.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("kind=%s reason=%q", d2.Kind, d2.Reason)
	}
	if !strings.Contains(d2.Answer, "max-heap") && !strings.Contains(d2.Answer, "larger") {
		t.Fatalf("want on-disk max-heap, got:\n%s", d2.Answer)
	}

	cmp := "这个和 container/heap 有啥区别？别开任务，别改代码，直接说。"
	d3 := classifyNaturalLanguageQuestionWithoutLLM(cmp, replCommandOptions{}, naturalLanguageContextIntent)
	if d3.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("kind=%s", d3.Kind)
	}
	if !strings.Contains(d3.Answer, "heap primitive") && !strings.Contains(d3.Answer, "wraps") {
		t.Fatalf("want heap wrapper vs primitive, got:\n%s", d3.Answer)
	}
}

func TestDeliveryHonestyMentionsRedSuite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/ring\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ring.go"), []byte("package ring\n\nfunc New(n int) *Buf { return nil }\nfunc (b *Buf) Push(v int) {}\nfunc (b *Buf) TryPush(v int) bool { return false }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ring_test.go"), []byte("package ring\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) {\n\tvar n int64\n\tvar total int\n\tif n > total {\n\t\tt.Fatal(\"never\")\n\t}\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte("> **Status**: in-progress\n> **Active Phase**: 1\n> **Phase Count**: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("## Phase 1 Checklist\n- [x] Test suite\n- [ ] Suite green\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	in := "哦对了周末想去钓鱼——算了当我没问。测试套件勾完了，go test 编不过，到底完成没有？就解释，别改代码别开任务。"
	lowered := strings.ToLower(in)
	if looksLikeConversationalRepairQuestion(lowered) {
		t.Fatal("delivery honesty ask must not look like repair")
	}
	d := classifyNaturalLanguageQuestionWithoutLLM(in, replCommandOptions{}, naturalLanguageContextIntent)
	if d.Kind != naturalLanguageDecisionAnswer {
		t.Fatalf("kind=%s reason=%q", d.Kind, d.Reason)
	}
	if !strings.Contains(d.Answer, "still red") && !strings.Contains(d.Answer, "incomplete") {
		t.Fatalf("want compile/test honesty, got:\n%s", d.Answer)
	}
	if strings.Contains(d.Answer, "Exported API") {
		t.Fatalf("must not dump API instead of honesty:\n%s", d.Answer)
	}
	if strings.Contains(d.Answer, "Repair note:") {
		t.Fatalf("repair leaked:\n%s", d.Answer)
	}
}

func TestNoMutateDoesNotStealGreenfieldOrRepair(t *testing.T) {
	brief := "帮我从零做一个开源风格的 Go 库 ringbufx：进程内环形缓冲。写齐单元测试。"
	if looksLikeNoMutateFollowUp(strings.ToLower(brief)) {
		t.Fatal("greenfield brief must not be a no-mutate follow-up")
	}
	repair := "go test 红了接着改代码修绿。不要只分析。"
	if looksLikeNoMutateFollowUp(strings.ToLower(repair)) {
		t.Fatal("test-repair must still be work")
	}
}

func TestPhasePlanningAskReadsPlanFacts(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Project**: semax
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1

## Phases

### Phase 1: Weighted semaphore
- **Goal**: In-process Acquire/Release/TryAcquire
- **Status**: in-progress
- **Detail**: docs/workflow/phase1.md
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("## Phase 1 Checklist\n- [x] a\n- [ ] b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	ans, ok := answerWorkflowPhaseStatusQuestion("用人话讲讲你这次是怎么划分阶段的，现在做到哪了。不要再改代码。")
	if !ok {
		t.Fatal("expected local phase-status answer")
	}
	if strings.Contains(ans, "Repair note:") {
		t.Fatalf("repair note leaked:\n%s", ans)
	}
	for _, want := range []string{"Phase count: 1", "Active phase: 1", "Weighted semaphore"} {
		if !strings.Contains(ans, want) {
			t.Fatalf("missing %q in:\n%s", want, ans)
		}
	}
}

func TestF96_ParallelInsideLibraryBriefDoesNotStealIntent(t *testing.T) {
	// r32a: coding brief mentioned concurrent Runs with 「并行」and was
	// answered as avatars progress UX.
	brief := strings.ToLower(`创建一个纯 Go 开源库，模块名 retrybudget。多个 Run 可以并行互不踩脚。写齐单元测试。`)
	if !mechanicalLocalAnswersYieldToWork(brief) {
		t.Fatal("library brief with 并行 should yield to work routing")
	}
	if _, ok := answerOfflineStatusQuestion(brief); ok {
		t.Fatal("F96: 「并行」inside a library brief must not answer progress UX")
	}
	py := strings.ToLower("Create a Python package named retrybudget; concurrent runs must be parallel-safe. Include pytest.")
	if _, ok := answerOfflineStatusQuestion(py); ok {
		t.Fatal("F96: English parallel inside a Python package brief must not steal")
	}
	js := strings.ToLower("Build a TypeScript library named retrybudget. Parallel request retries must not share state. npm test must pass.")
	if _, ok := answerOfflineStatusQuestion(js); ok {
		t.Fatal("F96: parallel inside a TS library brief must not steal")
	}
}

func TestF96_ExplicitProgressTreeStillAnswers(t *testing.T) {
	q := "多文件并行的时候 stderr 进度树怎么看"
	ans, ok := answerOfflineStatusQuestion(q)
	if !ok {
		t.Fatal("explicit progress-tree question should still answer offline")
	}
	if !strings.Contains(ans, "node-survey") {
		t.Fatalf("want progress UX answer, got %q", ans)
	}
}

func TestF107_ContinuationWorkYieldsPastLocalQA(t *testing.T) {
	cases := []string{
		"请继续把剩余能力做完，不要只回答上次做了啥",
		"继续做完",
		"还缺 worker，立刻写 worker.go",
		"please continue implementing the remaining python package",
		"finish remaining crate tests",
	}
	for _, in := range cases {
		lowered := strings.ToLower(in)
		if !looksLikeContinuationWorkRequest(lowered) {
			t.Fatalf("expected continuation work: %q", in)
		}
		if looksLikeLastActionQuestion(lowered) {
			t.Fatalf("F107: continuation must not be last-action: %q", in)
		}
		if looksLikeAttachedFileQuestion(lowered) {
			t.Fatalf("F107: continuation must not be file-context: %q", in)
		}
		if !mechanicalLocalAnswersYieldToWork(lowered) {
			t.Fatalf("F107: continuation must yield to work: %q", in)
		}
		if ans, ok, _ := answerHighConfidenceLocalQuestion(lowered); ok {
			t.Fatalf("F107: high-confidence Q&A stole %q → %q", in, ans)
		}
	}
	if got := attachedFileDefaultInput("请继续把剩余能力做完"); got != "请继续把剩余能力做完" {
		t.Fatalf("--from-file continue.txt should use body as intent, got %q", got)
	}
	if looksLikeLastActionQuestion("继续") {
		t.Fatal("bare 继续 is keep-going work, not last-action recap")
	}
	if !looksLikeContinuationWorkRequest("继续") {
		t.Fatal("bare 继续 should be continuation work")
	}
	if looksLikeLastActionQuestion("继续推进") {
		t.Fatal("继续推进 must not be last-action recap")
	}
	if !looksLikeContinuationWorkRequest("继续推进") {
		t.Fatal("继续推进 should be continuation work")
	}
	if !looksLikeContinuationWorkRequest("继续推进 phase1.md") {
		t.Fatal("继续推进 phase1.md should be continuation work")
	}
}

func TestChecklistAlignAndLastRoundWorkNotStolen(t *testing.T) {
	continue1 := "代码已经写出来、go test 也绿了，但清单看起来还全是空的。请把清单和真实进度对齐。用人话讲为什么这么划阶段、现在第几阶段。不要让我填 resume 数字。只动当前目录。"
	continue2 := "请接着上一轮任务做：磁盘上 ttlcache 已经能测绿，但 avatars_todo 还全是空勾。请把清单和真实进度对齐。用人话讲为什么这么划阶段、现在第几阶段。不要 summarize this file。"
	en := "Please continue last round's task: tests are green but the checklist is empty. Align the checklist with real progress and say which phase."

	for _, in := range []string{continue1, continue2, en} {
		lowered := strings.ToLower(in)
		if !looksLikeChecklistProgressReconcile(lowered) && !looksLikeResumeContinuationIntent(lowered) {
			t.Fatalf("expected reconcile or resume work: %q", in)
		}
		if looksLikeLastActionQuestion(lowered) {
			t.Fatalf("progress-align / last-round work must not be last-action: %q", in)
		}
		if looksLikeConversationalRepairQuestion(lowered) {
			t.Fatalf("人话 inside checklist-align must not trigger repair: %q", in)
		}
		if !mechanicalLocalAnswersYieldToWork(lowered) {
			t.Fatalf("checklist-align must yield to work: %q", in)
		}
		if ans, ok, _ := answerHighConfidenceLocalQuestion(lowered); ok {
			t.Fatalf("high-confidence Q&A stole %q → %q", in, ans)
		}
		if got := attachedFileDefaultInput(in); got != strings.TrimSpace(in) {
			t.Fatalf("--from-file should keep body, got %q", got)
		}
	}
	if !looksLikeLastActionQuestion("上一轮做了什么") {
		t.Fatal("pure last-round status question should still be last-action")
	}
	alignOnly := "代码已经写出来、go test 也绿了，但清单看起来还没勾完。请把清单和真实进度对齐。用人话讲现在第几阶段。"
	if nd, ok := continuationWorkSafeRunDecision(alignOnly, strings.ToLower(alignOnly)); !ok || nd.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("checklist-align without 上一轮 must still be a run, got ok=%v kind=%s", ok, nd.Kind)
	}
}

func TestF101_RestoreOriginalRunTaskText(t *testing.T) {
	orig := "帮我从零做一个开源风格的 Go 库 tokenbucketx：令牌桶限流。要求写齐单元测试。"
	compressed := "Create a Go token bucket library named tokenbucketx"
	cmd := restoreOriginalRunTaskText(
		[]string{"run", "--new-task", "--permission-mode", "acceptEdits", compressed},
		orig,
	)
	if got := cmd[len(cmd)-1]; got != orig {
		t.Fatalf("expected original NL as task payload, got %q", got)
	}
	resume := restoreOriginalRunTaskText(
		[]string{"run", "--resume", "x.jsonl", "--task", "t1", "--permission-mode", "default", compressed},
		orig,
	)
	if got := resume[len(resume)-1]; got != orig {
		t.Fatalf("resume last arg should be original NL, got %q", got)
	}
	fromFile := restoreOriginalRunTaskText(
		[]string{"run", "--from-file", "phase1.md"},
		"继续推进 phase1.md",
	)
	joined := strings.Join(fromFile, " ")
	if strings.Contains(joined, "继续推进") {
		t.Fatalf("--from-file must not append original NL, got %q", joined)
	}
	if !containsAll(fromFile, []string{"run", "--from-file", "phase1.md"}) {
		t.Fatalf("expected from-file command preserved, got %q", joined)
	}
}

func containsAll(got, want []string) bool {
	if len(got) < len(want) {
		return false
	}
	for i, w := range want {
		if got[i] != w {
			return false
		}
	}
	return true
}

func TestF101_FirstLineDoesNotSplitUTF8Runes(t *testing.T) {
	s := "帮我从零做一个开源风格的Go库tokenbucketx令牌桶"
	got := firstLine(s, 12)
	if strings.Contains(got, "\uFFFD") {
		t.Fatalf("firstLine must not emit replacement runes, got %q", got)
	}
	if strings.HasSuffix(got, "...") {
		body := strings.TrimSuffix(got, "...")
		if !strings.HasPrefix(s, body) {
			t.Fatalf("truncated prefix %q is not a prefix of %q", body, s)
		}
	}
}

func TestExportedSignaturesSkipUnexportedReceiver(t *testing.T) {
	src := "package x\nfunc (p *panicError) Error() string { return \"\" }\nfunc (g *Group) Do(key string) {}\nfunc NewGroup() *Group { return nil }\n"
	got := exportedSignaturesFromSource(".go", src)
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "Error") {
		t.Fatalf("unexported receiver method leaked: %v", got)
	}
	if !strings.Contains(joined, "Do") || !strings.Contains(joined, "NewGroup") {
		t.Fatalf("expected exported APIs, got %v", got)
	}
}

func TestPythonSignaturesSkipPrivateDefs(t *testing.T) {
	src := "def public_api():\n    pass\ndef _internal():\n    pass\n"
	got := exportedSignaturesFromSource(".py", src)
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "_internal") {
		t.Fatalf("private def leaked: %v", got)
	}
	if !strings.Contains(joined, "public_api") {
		t.Fatalf("expected public_api, got %v", got)
	}
}
