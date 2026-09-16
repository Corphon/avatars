package llm

import "testing"

func TestIsQuotaOrBillingFailure(t *testing.T) {
	yes := []string{
		"failed: openai-compatible provider \"deepseek\" returned 402: Insufficient Balance",
		"Error 402 Insufficient Balance",
		"You exceeded your current quota",
		"insufficient_quota",
		"HTTP 402 payment required",
	}
	for _, s := range yes {
		if !IsQuotaOrBillingFailure(s) {
			t.Fatalf("expected quota/billing: %q", s)
		}
	}
	no := []string{
		"failed: builder produced no files",
		"go test failed: TestWaitSuccess",
		"line 402: syntax error",
		"",
	}
	for _, s := range no {
		if IsQuotaOrBillingFailure(s) {
			t.Fatalf("did not expect quota/billing: %q", s)
		}
	}
}
