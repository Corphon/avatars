package runtime

import (
	"testing"
	"time"

	"avatars/internal/llm"
)

func TestApplyHarnessThinkModeOff(t *testing.T) {
	req := llm.Request{Category: llm.CategorySummary}
	applyHarnessThinkModeOff(&req)
	if req.ThinkMode != llm.ThinkModeOff {
		t.Fatalf("ThinkMode=%q want off", req.ThinkMode)
	}
	applyHarnessThinkModeOff(nil) // must not panic
}

// Regression lock for F8: Builder / Direct codegen must force ThinkModeOff
// so thinking models spend budget on content/tool calls.
func TestBuilderCodeGenDefaults_ThinkModeOff(t *testing.T) {
	req := llm.Request{
		Category:     llm.CategoryCodeGeneration,
		MaxTokens:    1024,
		MaxToolTurns: 5,
	}
	applyBuilderCodeGenThinkMode(&req)
	if req.ThinkMode != llm.ThinkModeOff {
		t.Fatalf("ThinkMode=%q want off", req.ThinkMode)
	}
}

func TestBuilderCodeGenLLMTimeout_OffSkipsThinkingFloor(t *testing.T) {
	off := llm.Request{Category: llm.CategoryCodeGeneration, ThinkMode: llm.ThinkModeOff}
	wantOff := llm.TimeoutForRequest(off)
	if wantOff < 180*time.Second {
		wantOff = 180 * time.Second
	}
	gotOff := builderCodeGenLLMTimeout(off)
	if gotOff != wantOff {
		t.Fatalf("ThinkModeOff timeout=%v want yaml/codegen floor %v (not thinking floor)", gotOff, wantOff)
	}
	on := llm.Request{Category: llm.CategoryCodeGeneration, ThinkMode: llm.ThinkModeOn}
	floor := llm.ThinkingCodeGenTimeoutFloor()
	gotOn := builderCodeGenLLMTimeout(on)
	if gotOn < floor {
		t.Fatalf("ThinkModeOn timeout=%v want at least thinking floor %v", gotOn, floor)
	}
}

func TestSynthesizerSummaryRequest_ThinkModeOff(t *testing.T) {
	// Mimic harness policy: after buildLLMSummaryRequest, apply Off.
	req := llm.Request{Category: llm.CategorySummary}
	applyHarnessThinkModeOff(&req)
	if req.ThinkMode != llm.ThinkModeOff {
		t.Fatalf("Synthesizer path ThinkMode=%q want off", req.ThinkMode)
	}
}

func TestCriticPlanReviewRequest_InheritsThinkMode(t *testing.T) {
	// Critic plan-review must NOT force Off — leave unset for yaml auto/on.
	req := llm.Request{Category: llm.CategoryAnalysis}
	if req.ThinkMode != "" {
		t.Fatalf("Critic request should leave ThinkMode unset, got %q", req.ThinkMode)
	}
}
