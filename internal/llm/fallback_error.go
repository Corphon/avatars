package llm

import (
	"fmt"
	"strings"
)

const primaryFailureNoFallbackPrefix = "primary LLM provider failed (fallback_providers is empty)"

// AnnotatePrimaryFailureWithoutFallbacks wraps a primary Generate error so
// CLI/logs can grep that fallback_providers was empty (C6.2 / keep S5.4).
func AnnotatePrimaryFailureWithoutFallbacks(err error) error {
	if err == nil {
		return nil
	}
	if IsPrimaryFailureWithoutFallbacks(err) && strings.Contains(err.Error(), primaryFailureNoFallbackPrefix) {
		return err
	}
	return fmt.Errorf("%s: %w", primaryFailureNoFallbackPrefix, err)
}

// IsPrimaryFailureWithoutFallbacks reports whether err is a primary-provider
// failure with no usable fallback_providers chain.
func IsPrimaryFailureWithoutFallbacks(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, primaryFailureNoFallbackPrefix) ||
		strings.Contains(msg, "no fallback providers configured") ||
		strings.Contains(msg, "fallback_providers configured but none constructed")
}
