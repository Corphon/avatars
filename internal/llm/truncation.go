package llm

// IsTruncated returns true if the LLM response was cut off due to
// hitting the max_tokens limit. OpenAI-compatible APIs signal this
// via finish_reason="length"; Anthropic uses stop_reason="max_tokens".
// TR-Fix-1: Both signals are now recognized.
func IsTruncated(resp Response) bool {
	return resp.FinishReason == "length" || resp.FinishReason == "max_tokens"
}

// NormalizeFinishReason maps provider-specific truncation signals to a
// canonical FinishReason value. Call this when ingesting responses from
// different providers (DeepSeek, Anthropic, OpenAI, etc.) so downstream
// code only needs to check IsTruncated.
func NormalizeFinishReason(provider string, rawFinishReason string) string {
	switch {
	case rawFinishReason == "max_tokens":
		// Anthropic uses "max_tokens" instead of "length".
		return FinishReasonLength
	case provider == "anthropic" && rawFinishReason == "end_turn":
		// Anthropic's normal stop — not a truncation.
		return FinishReasonStop
	default:
		return rawFinishReason
	}
}

// FinishReasonLength is the canonical API value for max_tokens truncation
// in OpenAI-compatible APIs (DeepSeek, Qwen, etc.).
const FinishReasonLength = "length"

// FinishReasonStop is the normal completion signal.
const FinishReasonStop = "stop"

// FinishReasonToolCalls means the model stopped to invoke tools.
const FinishReasonToolCalls = "tool_calls"
