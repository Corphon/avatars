package runtime

import (
	"strings"
	"testing"

	"avatars/internal/planner"
	"avatars/internal/prompt"
)

func TestPersonalityPromptFragment(t *testing.T) {
	if got := personalityPromptFragment(PersonalityConfig{}); got != "" {
		t.Fatalf("empty config should yield empty fragment, got %q", got)
	}
	got := personalityPromptFragment(PersonalityConfig{
		Language:        "zh-CN",
		Style:           "concise",
		OperatorContext: "Python backend",
	})
	for _, want := range []string{
		"=== PERSONALITY ===",
		"Respond to the operator in zh-CN.",
		"Operator context: Python backend",
		"Be concise",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestSynthesizerAnalysisLanguageLine(t *testing.T) {
	if !strings.Contains(synthesizerAnalysisLanguageLine(PersonalityConfig{Language: "zh-CN"}), "zh-CN") {
		t.Fatal("configured language must appear in analysis line")
	}
	got := synthesizerAnalysisLanguageLine(PersonalityConfig{})
	if strings.Contains(got, "in English.") {
		t.Fatalf("empty language must not hard-code English-only: %q", got)
	}
	if !strings.Contains(got, "operator's language") {
		t.Fatalf("expected match-operator-language guidance, got %q", got)
	}
	if !strings.Contains(got, "delivery summary") {
		t.Fatalf("F61: synthesizer language line should say delivery summary, got %q", got)
	}
}

func TestBuildLLMSummaryRequest_ThisRunSourcesOverridePriorSummary(t *testing.T) {
	readSummary := "THIS RUN WROTE IMPLEMENTATION SOURCES: lib.py, lib_test.py. Prior Delivery Summaries are stale; What Changed must list these paths. Builder changed_files: lib.py, lib_test.py"
	req := buildLLMSummaryRequest(
		prompt.Build("continue the library"),
		planner.Build("continue the library"),
		readSummary,
		"", "", "", true, nil,
		PersonalityConfig{},
		"",
	)
	if !strings.Contains(req.UserPrompt, "this_run_wrote_sources: lib.py, lib_test.py") {
		t.Fatalf("user prompt should lift this-run sources out of the digest:\n%s", req.UserPrompt)
	}
	if !strings.Contains(req.UserPrompt, "summary_authority:") {
		t.Fatalf("user prompt should tell synthesizer to ignore prior summaries:\n%s", req.UserPrompt)
	}
	if strings.Contains(req.SystemPrompt, "summary_authority:") {
		t.Fatal("do not put this-run authority into the cached system prefix")
	}
	if !strings.Contains(req.UserPrompt, "synthesizer_tools:") {
		t.Fatal("no-write honesty belongs on the user turn")
	}
	if strings.Contains(req.SystemPrompt, "synthesizer_tools:") || strings.Contains(req.SystemPrompt, "precise_edit") {
		t.Fatal("do not put synthesizer no-write honesty into the cached system prefix")
	}
}

func TestBuildLLMSummaryRequest_UserFacingDelivery_F61(t *testing.T) {
	req := buildLLMSummaryRequest(
		prompt.Build("做一个 Go 库"),
		planner.Build("做一个 Go 库"),
		"changed_files: base32x/base32x.go\ndisk_inventory: base32x/base32x.go",
		"", "", "", false, nil,
		PersonalityConfig{},
		"",
	)
	if !strings.Contains(req.SystemPrompt, "Delivery Summary") {
		t.Fatalf("F61: synthesizer must ask for Delivery Summary:\n%s", req.SystemPrompt)
	}
	if strings.Contains(req.SystemPrompt, "Evidence Reviewed, Evidence Coverage, Code Trace Evidence") {
		t.Fatalf("F61: must not lead with audit-section template:\n%s", req.SystemPrompt)
	}
	if !strings.Contains(req.SystemPrompt, "MUST NOT say empty delivery") {
		t.Fatalf("F61: must forbid empty-delivery when sources listed:\n%s", req.SystemPrompt)
	}
}

func TestBuildLLMSummaryRequest_RespectsPersonalityLanguage(t *testing.T) {
	req := buildLLMSummaryRequest(
		prompt.Build("分析仓库"),
		planner.Build("分析仓库"),
		"Read README.md successfully.",
		"", "", "", false, nil,
		PersonalityConfig{Language: "zh-CN"},
		"",
	)
	if !strings.Contains(req.SystemPrompt, "zh-CN") {
		t.Fatalf("system prompt should include zh-CN:\n%s", req.SystemPrompt)
	}
	if strings.Contains(req.SystemPrompt, "analysis in English.") {
		t.Fatalf("must not hard-code English when language is set:\n%s", req.SystemPrompt)
	}
}

func TestAppendPersonality(t *testing.T) {
	base := "You are a Builder."
	got := appendPersonality(base, PersonalityConfig{Language: "ja"})
	if !strings.HasPrefix(got, base) || !strings.Contains(got, "Respond to the operator in ja.") {
		t.Fatalf("got %q", got)
	}
}
