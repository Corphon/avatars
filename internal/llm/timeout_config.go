package llm

import (
	"strings"
	"time"
)

// TimeoutConfig centralizes all runtime timeout values. T1+L2: Previously
// hardcoded across the codebase; now configurable via agent.yaml.
//
// Default values match the previously hardcoded constants so existing
// behavior is preserved when no config override is present.
type TimeoutConfig struct {
	// LLMClassify is the timeout for NL intent classification / routing calls.
	// Previously: llmCallTimeout = 120s (natural_language.go).
	LLMClassify time.Duration

	// PlanReview is the timeout for LLM-based plan content review.
	// Previously: planReviewTimeout = 60s (critic_plan_review.go).
	PlanReview time.Duration

	// PlanAutoFill is the timeout for auto-filling plan/phase workflow docs
	// via LLM (ConstructPlan / ExpandActivePhase / ConfirmPlan generates).
	// Must be long enough for slow providers; thinking models should still
	// use ThinkModeOff on these paths (and Builder codegen — F8) so the
	// budget goes to answer content / tool calls.
	PlanAutoFill time.Duration

	// CodeGen is the timeout for code generation LLM requests.
	// Previously: 120s (client.go TimeoutForRequest).
	CodeGen time.Duration

	// Summary is the timeout for summarization/synthesis LLM requests.
	// Previously: 60s (client.go TimeoutForRequest).
	Summary time.Duration

	// DefaultLLM is the fallback timeout for uncategorized LLM requests.
	// Previously: 30s (client.go TimeoutForRequest).
	DefaultLLM time.Duration

	// NodeTimeout is the per-workflow-node execution timeout.
	// Previously: defaultNodeTimeout = 20min (scheduler.go).
	NodeTimeout time.Duration

	// NodeTimeoutByRole overrides NodeTimeout for specific workflow roles
	// (builder, critic, researcher, synthesizer, planner). Keys are lowercase.
	// E3: Critic must not share a single 20m budget with nested Builder work.
	NodeTimeoutByRole map[string]time.Duration

	// ShellDone is the timeout for shell_done tool execution (S5.7 / M-15).
	// Previously hardcoded 30s in tool_bridge.go and openai_compatible.go.
	ShellDone time.Duration
}

// DefaultTimeoutConfig returns the baseline timeout config matching the
// previously hardcoded values across the codebase.
func DefaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{
		LLMClassify:  120 * time.Second,
		PlanReview:   60 * time.Second,
		PlanAutoFill: 180 * time.Second,
		CodeGen:      300 * time.Second,
		Summary:      60 * time.Second,
		DefaultLLM:   30 * time.Second,
		NodeTimeout:  20 * time.Minute,
		ShellDone:    30 * time.Second,
	}
}

// ThinkingCodeGenTimeoutFloor is the minimum wall-clock budget for Builder
// codegen when the provider may run thinking/CoT (Z9). Plain CodeGen (300s)
// is often consumed entirely by reasoning before the first tool_call.
func ThinkingCodeGenTimeoutFloor() time.Duration {
	cfg := TimeoutConfigOrDefault()
	floor := 10 * time.Minute
	if cfg.CodeGen > floor {
		return cfg.CodeGen
	}
	return floor
}

// GlobalDefaultMaxTokens is the code-generation max_tokens loaded from
// agent.yaml llm_defaults.max_tokens. Zero means use DefaultBuilderBudget.
var GlobalDefaultMaxTokens int

// DefaultBuilderBudget is the daily codegen output request, not the model
// maximum. Providers like DeepSeek V4 accept much larger completions; sending
// that ceiling as the everyday budget lets a stream run until HTTP timeout.
const DefaultBuilderBudget = 16384

// HardMaxOutputTokens is the harness ceiling for any codegen request,
// including stale yaml and persisted truncation-escalation state.
const HardMaxOutputTokens = 65536

// DefaultMaxTokensForCode returns the effective code-gen max_tokens.
func DefaultMaxTokensForCode() int {
	n := DefaultBuilderBudget
	if GlobalDefaultMaxTokens > 0 {
		n = GlobalDefaultMaxTokens
	}
	return clampMaxTokensForProvider(HardMaxOutputTokens, n)
}

// GlobalTimeoutConfig holds the runtime timeout values loaded from agent.yaml.
// Set once during startup by app.LoadTimeoutConfig. If nil, callers fall back
// to DefaultTimeoutConfig().
var GlobalTimeoutConfig *TimeoutConfig

// TimeoutConfigOrDefault returns the global config if loaded, otherwise
// the default config. Safe for use at any call site.
func TimeoutConfigOrDefault() TimeoutConfig {
	if GlobalTimeoutConfig != nil {
		return *GlobalTimeoutConfig
	}
	return DefaultTimeoutConfig()
}

// GlobalMaxToolTurnsByRole holds per-role MaxToolTurns loaded from agent.yaml.
// SP-Fix-3: Set once during startup by app.loadLLMConfig. When nil, callers
// use the default (5).
var GlobalMaxToolTurnsByRole map[string]int

// MaxToolTurnsForRole returns the configured max tool turns for the given
// role, or the default (5) if no config is set. SP-Fix-3.
func MaxToolTurnsForRole(role string) int {
	if GlobalMaxToolTurnsByRole != nil {
		if v, ok := GlobalMaxToolTurnsByRole[strings.ToLower(role)]; ok && v > 0 {
			return v
		}
	}
	return 5 // default
}

// GlobalFallbackProviders holds the fallback provider list from agent.yaml.
// G2: When empty and primary fails, the runtime uses deterministic fallback.
var GlobalFallbackProviders []string

// HasFallbackProviders returns true if fallback providers are configured.
func HasFallbackProviders() bool {
	return len(GlobalFallbackProviders) > 0
}

// SetFallbackProviders updates the global fallback list (tests / loadLLMConfig).
func SetFallbackProviders(providers []string) {
	GlobalFallbackProviders = append([]string(nil), providers...)
}
