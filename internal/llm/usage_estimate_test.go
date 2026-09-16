package llm

import "testing"

func TestEstimateUsageFromOutput(t *testing.T) {
	if got := EstimateUsageFromOutput("", ""); got.Known {
		t.Fatal("empty output is unknown")
	}
	got := EstimateUsageFromOutput("abcd", "")
	if !got.Known || !got.Estimated || got.CompletionTokens != 1 {
		t.Fatalf("4 bytes → 1 token, got %+v", got)
	}
	got = EstimateUsageFromOutput(string(make([]byte, 400)), "rrrr")
	if !got.Known || got.CompletionTokens != 101 {
		t.Fatalf("404 bytes → 101 tokens, got %+v", got)
	}
}

func TestUsagePreferKnown(t *testing.T) {
	provider := Usage{CompletionTokens: 12, Known: true}
	est := EstimateUsageFromOutput("xxxxxxxx", "")
	got := usagePreferKnown(provider, est)
	if got.Estimated || got.CompletionTokens != 12 {
		t.Fatalf("provider usage must win, got %+v", got)
	}
	got = usagePreferKnown(Usage{}, est)
	if !got.Estimated || !got.Known {
		t.Fatalf("estimate fills missing provider usage, got %+v", got)
	}
}
