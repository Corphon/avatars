package runtime

import (
	"strings"

	"avatars/internal/llm"
)

// Greppable llm.started / llm.failed payload keys for C6.1 prompt-cost
// observability. Do not dump prompt text — only byte counts and injection flags.
const (
	promptCostRoleKey        = "node_role"
	promptCostSystemBytesKey = "system_prompt_bytes"
	promptCostUserBytesKey   = "user_prompt_bytes"
	promptCostTotalBytesKey  = "prompt_bytes_total"
	// SkillDualInjectPayloadKey is true when compact always-on (bundle
	// WithAlwaysOnSkills) and full always-on (appendAlwaysOnRoleSkills)
	// both land in the same system prompt — the old L-3 dual-inject.
	SkillDualInjectPayloadKey    = "skill_dual_inject"
	skillAlwaysOnCompactCountKey = "always_on_compact_count"
	skillAlwaysOnFullCountKey    = "always_on_full_count"
	skillAlwaysOnCompactBytesKey = "always_on_compact_bytes"
	skillAlwaysOnFullBytesKey    = "always_on_full_bytes"
)

const (
	alwaysOnCompactMarker = "always-on skill:"
	alwaysOnFullMarker    = "## Always-On Skills"
)

func promptCostFields(role, systemPrompt, userPrompt string) map[string]any {
	sysN := len(systemPrompt)
	usrN := len(userPrompt)
	out := map[string]any{
		promptCostSystemBytesKey: sysN,
		promptCostUserBytesKey:   usrN,
		promptCostTotalBytesKey:  sysN + usrN,
	}
	if role = strings.TrimSpace(role); role != "" {
		out[promptCostRoleKey] = role
		out["role"] = role
	}
	return out
}

// skillDualInjectReport counts L-3 always-on injection forms in a system prompt.
// compact = prompt.Bundle.WithAlwaysOnSkills ("always-on skill:");
// full = appendAlwaysOnRoleSkills ("## Always-On Skills").
func skillDualInjectReport(systemPrompt string) map[string]any {
	compactCount := strings.Count(systemPrompt, alwaysOnCompactMarker)
	fullCount := strings.Count(systemPrompt, alwaysOnFullMarker)
	compactBytes := 0
	if compactCount > 0 {
		compactBytes = countedMarkerSpanBytes(systemPrompt, alwaysOnCompactMarker)
	}
	fullBytes := 0
	if fullCount > 0 {
		fullBytes = countedMarkerSpanBytes(systemPrompt, alwaysOnFullMarker)
	}
	return map[string]any{
		skillAlwaysOnCompactCountKey: compactCount,
		skillAlwaysOnFullCountKey:    fullCount,
		skillAlwaysOnCompactBytesKey: compactBytes,
		skillAlwaysOnFullBytesKey:    fullBytes,
		SkillDualInjectPayloadKey:    compactCount > 0 && fullCount > 0,
	}
}

func countedMarkerSpanBytes(text, marker string) int {
	total := 0
	rest := text
	for {
		idx := strings.Index(rest, marker)
		if idx < 0 {
			return total
		}
		rest = rest[idx:]
		nextCompact := strings.Index(rest[len(marker):], alwaysOnCompactMarker)
		nextFull := strings.Index(rest[len(marker):], alwaysOnFullMarker)
		end := len(rest)
		for _, n := range []int{nextCompact, nextFull} {
			if n >= 0 {
				abs := n + len(marker)
				if abs < end {
					end = abs
				}
			}
		}
		total += end
		if end >= len(rest) {
			return total
		}
		rest = rest[end:]
	}
}

func mergePromptObservability(payload map[string]any, role, systemPrompt, userPrompt string) {
	if payload == nil {
		return
	}
	for k, v := range promptCostFields(role, systemPrompt, userPrompt) {
		payload[k] = v
	}
	for k, v := range skillDualInjectReport(systemPrompt) {
		payload[k] = v
	}
}

func mergeLLMUsage(payload map[string]any, usage llm.Usage) {
	if payload == nil || !usage.Known {
		return
	}
	payload["usage_known"] = true
	payload["prompt_tokens"] = usage.PromptTokens
	payload["completion_tokens"] = usage.CompletionTokens
	payload["total_tokens"] = usage.TotalTokens
	payload["cost_micros"] = usage.CostMicros
	payload["prompt_cache_hit_tokens"] = usage.CachedPromptTokens
	if usage.Estimated {
		payload["usage_estimated"] = true
	}
	if usage.PromptTokens > 0 {
		payload["prompt_cache_hit_ratio"] = float64(usage.CachedPromptTokens) / float64(usage.PromptTokens)
	}
}

func (e *Engine) emitLLMPromptCost(runID, taskID, avatarID, phase, role string, req llm.Request) {
	if e == nil {
		return
	}
	payload := map[string]any{
		"summary": role + " requesting LLM.",
	}
	if e.llm != nil {
		payload["provider"] = e.llm.Provider()
	}
	mergePromptObservability(payload, role, req.SystemPrompt, req.UserPrompt)
	_ = e.emit(runID, taskID, avatarID, phase, "llm.started", "llm", payload, nil)
}
