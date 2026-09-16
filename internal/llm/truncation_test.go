package llm

import "testing"

func TestIsTruncated_OpenAI(t *testing.T) {
	if !IsTruncated(Response{FinishReason: "length"}) {
		t.Fatal("expected IsTruncated=true for finish_reason=length (OpenAI style)")
	}
}

func TestIsTruncated_Anthropic(t *testing.T) {
	if !IsTruncated(Response{FinishReason: "max_tokens"}) {
		t.Fatal("expected IsTruncated=true for finish_reason=max_tokens (Anthropic style)")
	}
}

func TestIsTruncated_Stop(t *testing.T) {
	if IsTruncated(Response{FinishReason: "stop"}) {
		t.Fatal("expected IsTruncated=false for finish_reason=stop")
	}
}

func TestIsTruncated_ToolCalls(t *testing.T) {
	if IsTruncated(Response{FinishReason: "tool_calls"}) {
		t.Fatal("expected IsTruncated=false for finish_reason=tool_calls")
	}
}

func TestNormalizeFinishReason_AnthropicMaxTokens(t *testing.T) {
	normalized := NormalizeFinishReason("anthropic", "max_tokens")
	if normalized != FinishReasonLength {
		t.Fatalf("expected Anthropic max_tokens → length, got %q", normalized)
	}
}

func TestNormalizeFinishReason_AnthropicEndTurn(t *testing.T) {
	normalized := NormalizeFinishReason("anthropic", "end_turn")
	if normalized != FinishReasonStop {
		t.Fatalf("expected Anthropic end_turn → stop, got %q", normalized)
	}
}

func TestNormalizeFinishReason_OpenAIPassthrough(t *testing.T) {
	normalized := NormalizeFinishReason("openai", "length")
	if normalized != "length" {
		t.Fatalf("expected OpenAI length to pass through unchanged, got %q", normalized)
	}
}
