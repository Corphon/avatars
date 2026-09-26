package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/llm"
)

func TestTalkAsideMatchesUnlistedChatter(t *testing.T) {
	chdirEmptyWorkspace(t)
	for _, input := range []string{
		"你午饭吃啥了，我这边盒饭好难吃",
		"帮我写两句打油诗夸夸这个统计",
		"讲个没听过的冷笑话",
		"write a short poem about rain",
	} {
		nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
		if nd.Kind != naturalLanguageDecisionAnswer || !strings.HasPrefix(nd.Reason, "talk aside") {
			t.Fatalf("%q: want talk aside, got %+v", input, nd)
		}
		if !deterministicNLReason(nd.Reason) {
			t.Fatalf("%q: reason %q should skip the router", input, nd.Reason)
		}
		if strings.Contains(nd.Answer, "Hash in") {
			t.Fatalf("%q: canned verse leaked: %s", input, nd.Answer)
		}
		if strings.Contains(strings.Join(nd.Command, " "), "--new-task") {
			t.Fatalf("%q: must not start a task", input)
		}
	}

	build := classifyNaturalLanguageQuestionWithoutLLM("帮我做个 go 命令行，统计扩展名", replCommandOptions{}, naturalLanguageContextREPL)
	if strings.HasPrefix(build.Reason, "talk aside") {
		t.Fatalf("software request must not be talk: %+v", build)
	}

	cont := classifyNaturalLanguageQuestionWithoutLLM("继续刚才那个，别跑偏，也不要重新开一轮", replCommandOptions{}, naturalLanguageContextREPL)
	if strings.HasPrefix(cont.Reason, "talk aside") || cont.Kind == naturalLanguageDecisionAnswer {
		t.Fatalf("continue with no transcript must clarify, got %+v", cont)
	}

	stage := classifyNaturalLanguageQuestionWithoutLLM("上次那个舞台页还在不", replCommandOptions{}, naturalLanguageContextREPL)
	if strings.HasPrefix(stage.Reason, "talk aside") {
		t.Fatalf("stage gallery must stay local, got %+v", stage)
	}
}

func TestTalkAsideColorQuestionIsNotNoMutate(t *testing.T) {
	chdirEmptyWorkspace(t)
	if err := os.WriteFile("count_lines.go", []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"如果做成冰箱贴卖，配色用薄荷绿还是铁锈红？先别写代码",
		"If we sold this as a fridge magnet, mint or copper? Don't write code.",
	} {
		nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
		if nd.Kind != naturalLanguageDecisionAnswer || !strings.HasPrefix(nd.Reason, "talk aside") {
			t.Fatalf("%q: want talk, got %+v", input, nd)
		}
		if !deterministicNLReason(nd.Reason) {
			t.Fatalf("%q: reason %q should skip the router", input, nd.Reason)
		}
		if strings.Contains(nd.Answer, "No files changed") || strings.Contains(strings.Join(nd.Command, " "), "--new-task") {
			t.Fatalf("%q: must not canned-answer or start work: %+v", input, nd)
		}
	}
}

func TestTalkRequestStaysSmall(t *testing.T) {
	req := talkLLMRequest("讲个没听过的冷笑话")
	if req.SystemPrompt != talkSystemPrompt {
		t.Fatalf("system prompt changed: %q", req.SystemPrompt)
	}
	if req.UserPrompt != "讲个没听过的冷笑话" {
		t.Fatalf("user prompt = %q", req.UserPrompt)
	}
	if req.ThinkMode != llm.ThinkModeOff || req.ToolChoice != "none" || len(req.Tools) != 0 {
		t.Fatalf("talk request is not tool-free and thinking-off: %+v", req)
	}
	if req.MaxTokens != talkMaxTokens || req.StructuredOutput {
		t.Fatalf("budget = %d structured=%v", req.MaxTokens, req.StructuredOutput)
	}
	blob := req.SystemPrompt + "\n" + req.UserPrompt
	for _, banned := range []string{"Decision Rules", "CLI_guide", "go.mod", "Session context"} {
		if strings.Contains(blob, banned) {
			t.Fatalf("prompt contains %q", banned)
		}
	}
}

func TestTalkFallsBackWhenModelMissing(t *testing.T) {
	prev := generateTalkAnswer
	t.Cleanup(func() { generateTalkAnswer = prev })
	generateTalkAnswer = func(string) (string, error) {
		return "", errTalkFallback
	}
	nd, ok := talkAsideDecision("今天午饭吃什么", true)
	if !ok || nd.Kind != naturalLanguageDecisionAnswer || nd.Answer != talkFallbackAnswer {
		t.Fatalf("got ok=%v %+v", ok, nd)
	}
	if len(nd.Command) != 0 || !strings.HasPrefix(nd.Reason, "talk aside") {
		t.Fatalf("fallback must not run: %+v", nd)
	}
}

func TestTalkShowsStubAnswer(t *testing.T) {
	prev := generateTalkAnswer
	t.Cleanup(func() { generateTalkAnswer = prev })
	generateTalkAnswer = func(string) (string, error) {
		return "Rain is outside this chat.", nil
	}
	nd, ok := talkAsideDecision("what is the weather today", true)
	if !ok || nd.Answer != "Rain is outside this chat." {
		t.Fatalf("got ok=%v %+v", ok, nd)
	}
	generateTalkAnswer = func(string) (string, error) {
		return "One. Two. Three. Four.", nil
	}
	nd, ok = talkAsideDecision("say something long", true)
	if !ok || nd.Answer != "One. Two." {
		t.Fatalf("truncate got ok=%v %q", ok, nd.Answer)
	}
}

func TestTalkTaskSentinelFallsThrough(t *testing.T) {
	prev := generateTalkAnswer
	t.Cleanup(func() { generateTalkAnswer = prev })
	generateTalkAnswer = func(string) (string, error) {
		return "TASK", nil
	}
	_, ok := talkAsideDecision("今天午饭吃什么", true)
	if ok {
		t.Fatal("TASK must fall through to the work router")
	}
}

func TestTalkDoesNotWriteLastTurn(t *testing.T) {
	chdirEmptyWorkspace(t)
	clearREPLTalkLines()
	t.Cleanup(clearREPLTalkLines)
	prev := generateTalkAnswer
	t.Cleanup(func() { generateTalkAnswer = prev })
	generateTalkAnswer = func(string) (string, error) {
		return "I have no lunch.", nil
	}
	var output strings.Builder
	if _, err := runREPLNaturalLanguage("今天午饭吃什么", &output, replCommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(".avatars", "repl", "last_turn.json")); !os.IsNotExist(err) {
		t.Fatalf("talk wrote last_turn: %v", err)
	}
	if _, err := os.Stat(filepath.Join(".avatars", "tasks")); !os.IsNotExist(err) {
		t.Fatalf("talk wrote a task transcript dir: %v", err)
	}
	if !replTalkLines["今天午饭吃什么"] {
		t.Fatal("talk line was not marked for context exclusion")
	}
	ctx := formatREPLContext([]string{"今天午饭吃什么", "继续刚才那个"})
	if strings.Contains(ctx, "午饭") {
		t.Fatalf("talk leaked into run context: %q", ctx)
	}
	if !strings.Contains(ctx, "继续刚才那个") {
		t.Fatalf("work turn missing: %q", ctx)
	}
	if !strings.Contains(output.String(), "I have no lunch.") {
		t.Fatalf("screen missed the talk reply: %s", output.String())
	}
}

func TestProjectLayoutQuestionsStayLocal(t *testing.T) {
	chdirEmptyWorkspace(t)
	if err := os.MkdirAll(filepath.Join("internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("internal", "store", "store.go"), []byte("package store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	layer := "存储要不要让 HTTP 直接读 JSON，还是单独放一个 store 包？先别写代码，就说分层。"
	merge := "领域包是不是多余，并进 HTTP 包里吧？先说说，别写代码。"
	for _, input := range []string{layer, merge} {
		if looksLikeTalkAside(strings.ToLower(input)) {
			t.Fatalf("%q must not be talk", input)
		}
		nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
		if strings.HasPrefix(nd.Reason, "talk aside") {
			t.Fatalf("%q routed to talk: %+v", input, nd)
		}
		if nd.Kind != naturalLanguageDecisionAnswer {
			t.Fatalf("%q want local answer, got %+v", input, nd)
		}
		if !strings.Contains(nd.Answer, "internal/store") || !strings.Contains(nd.Answer, "No files changed") {
			t.Fatalf("%q answer should cite the package:\n%s", input, nd.Answer)
		}
		if strings.Contains(nd.Answer, "database") {
			t.Fatalf("%q should not invent a database: %s", input, nd.Answer)
		}
	}
}

func TestStorageSwitchConfirmsOnCurrentTask(t *testing.T) {
	chdirEmptyWorkspace(t)
	input := "我改主意了，不要 JSON 文件了，换成 sqlite。先别动手，确认一下再改。"
	if looksLikeTalkAside(strings.ToLower(input)) {
		t.Fatal("storage switch must not be talk")
	}
	nd := classifyNaturalLanguageQuestionWithoutLLM(input, replCommandOptions{}, naturalLanguageContextREPL)
	if nd.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("want clarify, got %+v", nd)
	}
	if !strings.HasPrefix(nd.Reason, "storage change") {
		t.Fatalf("reason %q should be deterministic", nd.Reason)
	}
	if !deterministicNLReason(nd.Reason) {
		t.Fatal("storage clarify must skip the intent router")
	}
	joined := nd.Question + "\n" + strings.Join(nd.Options, "\n")
	if strings.Contains(joined, "--new-task") {
		t.Fatalf("must not open a new task:\n%s", joined)
	}
	if !strings.Contains(joined, "current task") {
		t.Fatalf("clarify should stay on the current task:\n%s", joined)
	}
}
