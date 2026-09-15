package main

import (
	"strings"
	"testing"
)

func TestLooksLikeVisualSketchSpec_StageWebpage(t *testing.T) {
	zh := `用 avatars stage 来写一个网页：
1. 推荐一本好书。这是初中一年级老师布置的练习任务。
2. 我选择《一千零一夜》。
3. 美化和布局完全交给你了，注意风格要适合学生。`
	en := `Use avatars stage to write a webpage:
1. Recommend a book for a 7th-grade talk.
2. Use One Thousand and One Nights as examples.
3. Layout is up to you; keep it student-friendly.`
	htmlFile := "Create an interactive HTML page (deck.html) with clickable slides for a book talk."
	for _, body := range []string{zh, en, htmlFile} {
		if !looksLikeVisualSketchSpec(body) {
			t.Fatalf("expected visual sketch spec, got false for %q", body[:60])
		}
		got := attachedFileDefaultInput(body)
		if got != strings.TrimSpace(body) {
			t.Fatalf("from-file must keep body, got %q", got)
		}
		if strings.Contains(got, "summarize this attached file") {
			t.Fatalf("must not rewrite sketch spec to summarize")
		}
	}
}

func TestLooksLikeVisualSketchSpec_NotLibraryOrAPI(t *testing.T) {
	lib := "做一个可以 import 的 Go 进程内库 bitsetx。不要 HTTP，也不要 CLI。Set/Clear/Test。"
	api := "Create a REST API web app with FastAPI and pytest. 1. add routes 2. add tests"
	if looksLikeVisualSketchSpec(lib) {
		t.Fatal("library create must not look like a stage sketch")
	}
	if looksLikeVisualSketchSpec(api) {
		t.Fatal("coded web app must not look like a stage sketch")
	}
}

func TestStageSketchSafeRunDecision_UsesFromFileWhenAttached(t *testing.T) {
	body := "用 avatars stage 来写一个网页：\n1. 推荐一本书\n2. 可点击"
	setReplFileContext(&fileContextState{Path: "prompt.txt", Content: body})
	defer setReplFileContext(nil)
	nd, ok := stageSketchSafeRunDecision("summarize this attached file")
	if !ok {
		t.Fatal("attached sketch spec should intercept summarize placeholder")
	}
	if len(nd.Command) < 3 || nd.Command[0] != "stage" || nd.Command[1] != "--from-file" || nd.Command[2] != "prompt.txt" {
		t.Fatalf("expected stage --from-file prompt.txt, got %q", nd.Command)
	}
}

func TestRewriteCommandTowardStageSketch_RunSummarize(t *testing.T) {
	body := "Use avatars stage to write a webpage:\n1. Recommend a book\n2. Clickable slides"
	setReplFileContext(&fileContextState{Path: "prompt.txt", Content: body})
	defer setReplFileContext(nil)
	got := rewriteCommandTowardStageSketch("summarize this attached file", []string{"run", "--new-task", "--permission-mode", "plan", "summarize this attached file"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "stage --from-file prompt.txt") {
		t.Fatalf("expected stage --from-file, got %q", joined)
	}
}

func TestIsAutoSafeStageCommand(t *testing.T) {
	if !isAutoSafeStageCommand([]string{"stage", "--from-file", "prompt.txt"}) {
		t.Fatal("generate stage should be auto-safe")
	}
	if isAutoSafeStageCommand([]string{"stage", "--delete", "latest"}) {
		t.Fatal("delete must not be auto-safe")
	}
	if isAutoSafeStageCommand([]string{"stage", "--serve"}) {
		t.Fatal("serve must not be auto-safe")
	}
}

func TestClassifyWithoutLLM_StageSketch(t *testing.T) {
	input := "用 avatars stage 来写一个网页：\n1. 推荐一本书\n2. 可点击变化"
	nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextIntent)
	if nd.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("kind=%s want safe_run", nd.Kind)
	}
	if len(nd.Command) == 0 || nd.Command[0] != "stage" {
		t.Fatalf("command=%q want stage", nd.Command)
	}
}
