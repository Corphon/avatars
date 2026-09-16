package llm

// EstimateUsageFromOutput approximates token usage from UTF-8 bytes when the
// provider usage chunk never arrived (HTTP timeout mid-stream / mid-body).
// The 4-byte heuristic under-counts CJK; it is still better than reporting 0.
func EstimateUsageFromOutput(completionText, reasoningText string) Usage {
	n := len(completionText) + len(reasoningText)
	if n <= 0 {
		return Usage{}
	}
	tokens := (n + 3) / 4
	if tokens < 1 {
		tokens = 1
	}
	return Usage{
		CompletionTokens: tokens,
		TotalTokens:      tokens,
		Known:            true,
		Estimated:        true,
	}
}

func usagePreferKnown(provider Usage, estimated Usage) Usage {
	if provider.Known {
		return provider
	}
	return estimated
}
