package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/tasks"
)

// Sprint 1 NL regression: natural-language utterances must feel like an
// assistant, not a CLI error dump. Prefer semantic/local handlers over
// keyword sprawl; these tests exercise the decision layer without requiring
// a live LLM (allowLLM=false or pre-LLM local path).

func TestS1_GuardedBecomesClarifyNotError(t *testing.T) {
	decision := naturalLanguageDecision{
		Kind:       naturalLanguageDecisionGuarded,
		Reason:     "safety audit: rm -rf /",
		Confidence: 99,
		Options:    defaultGuardedOptions("删除所有文件"),
	}
	route, handled, err := resolveFromDecision("删除所有文件", decision)
	if err != nil {
		t.Fatalf("guarded must not return error, got %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	if route.Kind != naturalLanguageRouteClarify {
		t.Fatalf("kind=%s, want clarify", route.Kind)
	}
	if strings.Contains(strings.ToLower(route.Question), "error:") {
		t.Fatalf("question must not look like Error: %q", route.Question)
	}
	if !strings.Contains(route.Question, "1.") {
		t.Fatalf("expected numbered recovery options, got %q", route.Question)
	}
}

func TestS1_UnresolvedAnswerIDDoesNotLeak(t *testing.T) {
	// Simulate classify path: unknown answer_id should clarify or local-fallback,
	// never print the raw id as the user-visible answer.
	nd := naturalLanguageDecision{
		Kind:       naturalLanguageDecisionAnswer,
		Answer:     "totally_unknown_answer_id_xyz",
		Reason:     "llm router: test",
		Confidence: 80,
	}
	if answer, ok, _ := answerClassifiedLocalQuestion("你是谁", nd.Answer, replCommandOptions{}, naturalLanguageContextREPL); ok {
		t.Fatalf("expected unknown id to fail local resolve, got %q", answer)
	}
	// After failure, production path falls back to answerLocalQuestion / clarify.
	if strings.Contains(nd.Answer, "totally_unknown") {
		// Ensure format helpers never treat raw id as user question alone.
		q := unresolvedAnswerClarifyQuestion("你是谁", nd.Answer)
		if strings.TrimSpace(q) == nd.Answer {
			t.Fatal("clarify question must not be the raw answer_id")
		}
		if !strings.Contains(q, "1.") {
			t.Fatalf("expected options in clarify, got %q", q)
		}
	}
}

func TestS1_EmptySafeRunFilledWithUserText(t *testing.T) {
	cmd := []string{"run", "--new-task", "--permission-mode", "default", ""}
	if !isEmptySafeRunCommand(cmd) {
		t.Fatal("expected empty-task placeholder detected")
	}
	input := "实现一个 REST API"
	mode := "default"
	filled := []string{"run", "--new-task", "--permission-mode", mode, strings.TrimSpace(input)}
	if isEmptySafeRunCommand(filled) {
		t.Fatal("filled command must not be treated as empty")
	}
	if filled[len(filled)-1] != input {
		t.Fatalf("last arg=%q, want user text", filled[len(filled)-1])
	}
}

func TestS1_PreLLMLocalCapability(t *testing.T) {
	answer, ok, err := answerHighConfidenceLocalQuestion("你能做什么？")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("capability question should match high-confidence local handler")
	}
	if strings.TrimSpace(answer) == "" {
		t.Fatal("empty capability answer")
	}
	if strings.EqualFold(strings.TrimSpace(answer), "capabilities") {
		t.Fatal("must not leak answer_id")
	}
}

func TestS1_PreLLMLocalGreeting(t *testing.T) {
	answer, ok, err := answerHighConfidenceLocalQuestion("你好")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("greeting should match high-confidence local handler")
	}
	if strings.TrimSpace(answer) == "" {
		t.Fatal("empty greeting")
	}
}

func TestS1_ShellCatchallNotInPreLLMWhitelist(t *testing.T) {
	// "分析一下 auth 模块" must NOT be stolen by shell-analysis catchall
	// before the LLM — whitelist excludes that handler.
	if highConfidenceLocalHandlerNames["shell-analysis-catchall"] {
		t.Fatal("shell-analysis-catchall must not be pre-LLM whitelisted")
	}
	_, ok, err := answerHighConfidenceLocalQuestion("分析一下 auth 模块")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("analysis work request must not be answered by pre-LLM whitelist")
	}
}

func TestS1_ClarifySplitsQuestionFromReason(t *testing.T) {
	decision := naturalLanguageDecision{
		Kind:     naturalLanguageDecisionClarify,
		Reason:   "llm router: internal diagnosis wall\n\nline2\nline3",
		Question: "",
		Options:  defaultClarifyOptions("帮我改一下"),
	}
	q := formatUserClarifyQuestion("帮我改一下", decision)
	if strings.Contains(q, "llm router:") {
		t.Fatalf("user question must not include internal reason prefix: %q", q)
	}
	if strings.Contains(q, "internal diagnosis wall") {
		t.Fatalf("verbose reason must not become user question: %q", q)
	}
	if !strings.Contains(q, "1.") {
		t.Fatalf("expected numbered options: %q", q)
	}
}

func TestS1_LLMRouterEmptyCommandPlaceholder(t *testing.T) {
	nd := llmRouterDecisionToNaturalLanguage(llmRouterDecision{
		Action:     "safe_run",
		Command:    nil,
		Confidence: 70,
		Reason:     "user wants work",
	})
	if nd.Kind != naturalLanguageDecisionSafeRun {
		t.Fatalf("kind=%s", nd.Kind)
	}
	if !isEmptySafeRunCommand(nd.Command) {
		t.Fatalf("expected empty placeholder command, got %#v", nd.Command)
	}
}

func TestS1_VagueEditClarifiesNotAutoRun(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("帮我改一下", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionClarify {
		t.Fatalf("kind=%s reason=%q, want clarify", decision.Kind, decision.Reason)
	}
	if strings.Contains(decision.Question, "avatars run --new-task \"帮我改一下\"") {
		t.Fatalf("must not AutoRun vague edit: %q", decision.Question)
	}
	route, handled, err := resolveNaturalLanguageQuestion("帮我改一下", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || route.Kind != naturalLanguageRouteClarify {
		t.Fatalf("handled=%v kind=%s", handled, route.Kind)
	}
}

func TestS1_MassDeleteNotAutoRunSuggested(t *testing.T) {
	decision := classifyNaturalLanguageQuestionWithoutLLM("删除所有文件", replCommandOptions{}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionGuarded {
		t.Fatalf("kind=%s reason=%q, want guarded", decision.Kind, decision.Reason)
	}
	route, handled, err := resolveNaturalLanguageQuestion("删除所有文件", replCommandOptions{}, naturalLanguageContextREPL)
	if err != nil {
		t.Fatalf("must not error: %v", err)
	}
	if !handled || route.Kind != naturalLanguageRouteClarify {
		t.Fatalf("handled=%v kind=%s", handled, route.Kind)
	}
	if strings.Contains(route.Question, `avatars run --new-task "删除所有文件"`) {
		t.Fatalf("must not suggest unrestricted run for mass delete: %q", route.Question)
	}
	if strings.Contains(strings.ToLower(route.Question), "error:") {
		t.Fatalf("must not dump Error: %q", route.Question)
	}
}

// resolveFromDecision exercises the guarded/clarify mapping without going
// through classify (avoids live LLM). Mirrors resolveNaturalLanguageQuestion switch.
func resolveFromDecision(trimmed string, decision naturalLanguageDecision) (naturalLanguageRoute, bool, error) {
	switch decision.Kind {
	case naturalLanguageDecisionAnswer:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteDirectAnswer,
			Answer:     decision.Answer,
			Summary:    decision.Reason,
			Confidence: decision.Confidence,
		}, true, nil
	case naturalLanguageDecisionSafeRun:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteSafeRun,
			Command:    decision.Command,
			Summary:    decision.Reason,
			Confidence: decision.Confidence,
		}, true, nil
	case naturalLanguageDecisionGuarded:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteClarify,
			Summary:    decision.Reason,
			Question:   formatGuardedClarifyQuestion(trimmed, decision),
			Options:    decision.Options,
			Confidence: decision.Confidence,
		}, true, nil
	case naturalLanguageDecisionClarify:
		return naturalLanguageRoute{
			Kind:       naturalLanguageRouteClarify,
			Summary:    decision.Reason,
			Question:   formatUserClarifyQuestion(trimmed, decision),
			Options:    decision.Options,
			Confidence: decision.Confidence,
		}, true, nil
	default:
		return naturalLanguageRoute{}, false, nil
	}
}

func TestPersistLatestAnswerArtifact_DoesNotClobberReconciled(t *testing.T) {
	root := t.TempDir()
	reconciled := "# Answer\n\n_Reconciled by avatars final-delivery path._\n"
	if err := os.WriteFile(filepath.Join(root, "answer.md"), []byte(reconciled), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := persistLatestAnswerArtifact(root, tasks.Workspace{}, "raw synthesizer blob citing ttlcache/ttlcache.go")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("expected existing answer.md path")
	}
	body, err := os.ReadFile(filepath.Join(root, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != reconciled {
		t.Fatalf("F70: must not clobber reconciled answer.md, got %q", body)
	}
}
