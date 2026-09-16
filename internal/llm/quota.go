package llm

import "strings"

// IsQuotaOrBillingFailure reports provider billing/quota errors (HTTP 402,
// insufficient balance, OpenAI quota). These are availability failures, not
// compile/test failures, and must not trigger a full DAG auto-retry.
func IsQuotaOrBillingFailure(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return false
	}
	for _, n := range []string{
		"insufficient balance",
		"insufficient_quota",
		"exceeded your current quota",
		"billing hard limit",
		"payment required",
		"error 402",
		"status 402",
		"http 402",
		"returned 402",
	} {
		if strings.Contains(s, n) {
			return true
		}
	}
	if strings.Contains(s, "402") {
		for _, n := range []string{"quota", "balance", "billing", "payment", "credit"} {
			if strings.Contains(s, n) {
				return true
			}
		}
	}
	return false
}
