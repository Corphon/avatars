package llm

import "strings"

// openAIThinking is the Chat Completions thinking toggle
// (extra_body.thinking / top-level "thinking" on the JSON body).
type openAIThinking struct {
	Type string `json:"type"` // "enabled" | "disabled"
}

// providerRequiresAssistantReasoningReplay reports providers that reject
// multi-turn tool history when assistant.reasoning_content is omitted
// (DeepSeek V4 thinking mode; Kimi/Moonshot uses the same field).
func providerRequiresAssistantReasoningReplay(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "deepseek", "kimi", "glm":
		return true
	default:
		return false
	}
}

// providerSupportsThinkingToggle reports providers that accept a top-level
// thinking:{type} object on Chat Completions requests.
func providerSupportsThinkingToggle(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "deepseek", "glm":
		return true
	default:
		return false
	}
}

func stringPtr(s string) *string { return &s }

func normalizeOpenAIModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.ReplaceAll(m, "_", "-")
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// glmForcedThinking is true for GLM-5.3 / GLM-5.3-Flash: the API rejects
// thinking.type=disabled (HTTP 400). Off maps to enabled + reasoning_effort=low.
func glmForcedThinking(model string) bool {
	return strings.HasPrefix(normalizeOpenAIModelID(model), "glm-5.3")
}

func reasoningContentForToolReplay(provider string, thinkingEnabled bool, reasoning string) *string {
	if !thinkingEnabled || !providerRequiresAssistantReasoningReplay(provider) {
		if strings.TrimSpace(reasoning) == "" {
			return nil
		}
		return stringPtr(reasoning)
	}
	return stringPtr(reasoning)
}

// applyThinkingToOpenAIRequest sets thinking / effort from effective ThinkMode.
// Auto leaves the field unset (provider default).
func applyThinkingToOpenAIRequest(provider string, mode ThinkMode, req *openAIRequest) {
	if req == nil || !providerSupportsThinkingToggle(provider) {
		return
	}
	if strings.EqualFold(strings.TrimSpace(provider), "glm") {
		applyGLMThinking(mode, req)
		return
	}
	switch normalizeThinkMode(mode) {
	case ThinkModeOff:
		req.Thinking = &openAIThinking{Type: "disabled"}
	case ThinkModeOn:
		req.Thinking = &openAIThinking{Type: "enabled"}
	default:
		// auto: omit — DeepSeek V4 defaults to thinking enabled.
	}
}

func applyGLMThinking(mode ThinkMode, req *openAIRequest) {
	if req == nil {
		return
	}
	forced := glmForcedThinking(req.Model)
	switch normalizeThinkMode(mode) {
	case ThinkModeOff:
		if forced {
			req.Thinking = &openAIThinking{Type: "enabled"}
			req.ReasoningEffort = "low"
			return
		}
		req.Thinking = &openAIThinking{Type: "disabled"}
		req.ReasoningEffort = ""
	case ThinkModeOn:
		req.Thinking = &openAIThinking{Type: "enabled"}
		if forced {
			req.ReasoningEffort = "high"
		}
	default:
		// auto: omit — GLM-5.3 defaults to max effort.
	}
}

func thinkingEnabledForRequest(provider, model string, mode ThinkMode) bool {
	if glmForcedThinking(model) {
		return true
	}
	if !providerSupportsThinkingToggle(provider) && !providerRequiresAssistantReasoningReplay(provider) {
		return false
	}
	switch normalizeThinkMode(mode) {
	case ThinkModeOff:
		return false
	case ThinkModeOn:
		return true
	default:
		return providerRequiresAssistantReasoningReplay(provider) || providerSupportsThinkingToggle(provider)
	}
}
