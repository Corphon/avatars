package telemetry

import (
	"testing"
	"time"
)

func TestNewCollector_ZeroStartUsesNow(t *testing.T) {
	c := NewCollector("engine", time.Time{})
	if c == nil {
		t.Fatal("nil collector")
	}
	if c.snapshot.Mode != "engine" {
		t.Fatalf("mode=%q", c.snapshot.Mode)
	}
	if c.startedAt.IsZero() {
		t.Fatal("startedAt should be filled when caller passes zero")
	}
}

func TestObserveEvent_CountsToolsLLMAndVerification(t *testing.T) {
	c := NewCollector("run", time.Now().UTC().Add(-50*time.Millisecond))
	c.ObserveEvent("tool.requested", nil)
	c.ObserveEvent("tool.completed", nil)
	c.ObserveEvent("tool.failed", nil)
	c.ObserveEvent("tool.denied", nil)
	c.ObserveEvent("tool.awaiting_approval", nil)
	c.ObserveEvent("verification.completed", nil)
	c.ObserveEvent("critic.project_health_verified", nil)
	c.ObserveEvent("builder.project_health_verified", nil)
	c.ObserveEvent("llm.completed", map[string]any{
		"provider":          "deepseek",
		"model":             "chat",
		"fallback":          true,
		"usage_known":       true,
		"prompt_tokens":     10,
		"completion_tokens": int64(4),
		"total_tokens":      float64(14),
		"cost_micros":       250,
	})
	c.ObserveEvent("llm.failed", map[string]any{"provider": "openai"})
	c.ObserveEvent("ignored.event", nil)

	snap := c.Finish("completed")
	if snap.ToolRequests != 1 || snap.ToolCompletions != 1 || snap.ToolFailures != 3 {
		t.Fatalf("tools req=%d done=%d fail=%d", snap.ToolRequests, snap.ToolCompletions, snap.ToolFailures)
	}
	if snap.VerificationRuns != 3 {
		t.Fatalf("verification_runs=%d", snap.VerificationRuns)
	}
	if snap.LLMRequests != 2 || snap.LLMCompletions != 1 || snap.LLMFailures != 1 || snap.LLMFallbacks != 1 {
		t.Fatalf("llm req=%d ok=%d fail=%d fallback=%d", snap.LLMRequests, snap.LLMCompletions, snap.LLMFailures, snap.LLMFallbacks)
	}
	if snap.Provider != "openai" {
		t.Fatalf("failed event should keep latest provider, got %q", snap.Provider)
	}
	if snap.Model != "chat" {
		t.Fatalf("model=%q", snap.Model)
	}
	if !snap.UsageKnown || snap.PromptTokens != 10 || snap.CompletionTokens != 4 || snap.TotalTokens != 14 || snap.CostMicros != 250 {
		t.Fatalf("usage %+v", snap)
	}
	if snap.Status != "completed" {
		t.Fatalf("status=%q", snap.Status)
	}
	if snap.DurationMillis < 0 {
		t.Fatalf("duration=%d", snap.DurationMillis)
	}
}

func TestObserveEvent_IgnoresUsageUntilKnown(t *testing.T) {
	c := NewCollector("run", time.Now().UTC())
	c.ObserveEvent("llm.completed", map[string]any{
		"prompt_tokens": 99,
		"usage_known":   false,
	})
	snap := c.Finish("ok")
	if snap.UsageKnown || snap.PromptTokens != 0 {
		t.Fatalf("unknown usage should not merge: %+v", snap)
	}
}

func TestCollector_NilSafe(t *testing.T) {
	var c *Collector
	c.ObserveEvent("tool.requested", nil)
	snap := c.Finish("failed")
	if snap.Status != "failed" {
		t.Fatalf("nil Finish status=%q", snap.Status)
	}
}

func TestObserveEvent_CaseInsensitiveAndWhitespace(t *testing.T) {
	c := NewCollector("run", time.Now().UTC())
	c.ObserveEvent("  TOOL.REQUESTED  ", nil)
	c.ObserveEvent("Llm.Completed", map[string]any{"provider": "stub"})
	snap := c.Finish("ok")
	if snap.ToolRequests != 1 {
		t.Fatalf("tool requests=%d", snap.ToolRequests)
	}
	if snap.LLMCompletions != 1 || snap.Provider != "stub" {
		t.Fatalf("llm %+v", snap)
	}
}

func TestFinish_ClampsNegativeDuration(t *testing.T) {
	c := NewCollector("run", time.Now().UTC().Add(time.Hour))
	snap := c.Finish("ok")
	if snap.DurationMillis < 0 {
		t.Fatalf("duration must clamp at 0, got %d", snap.DurationMillis)
	}
}

func TestFirstNonEmpty_AllBlank(t *testing.T) {
	if firstNonEmpty("", "  ", "\t") != "" {
		t.Fatal("expected empty when every candidate is blank")
	}
}

func TestAsHelpers_CoerceNumericPayloads(t *testing.T) {
	if asInt(int32(3)) != 3 || asInt(int64(4)) != 4 || asInt(float64(5)) != 5 || asInt("x") != 0 {
		t.Fatal("asInt coercion")
	}
	if asInt64(3) != 3 || asInt64(int32(4)) != 4 || asInt64(float64(6)) != 6 || asInt64(true) != 0 {
		t.Fatal("asInt64 coercion")
	}
	if asString(7) != "" || asBool(1) || !asBool(true) {
		t.Fatal("asString/asBool")
	}
	if firstNonEmpty("  ", "keep") != "keep" {
		t.Fatal("firstNonEmpty")
	}
}
