package runtime

import (
	"time"

	memstore "avatars/internal/memory"
)

func (e *Engine) recordLLMStatus(runID string, taskID string) error {
	if e == nil || e.memory == nil || e.llmStatus == nil {
		return nil
	}
	payload := map[string]any{
		"provider":                   e.llmStatus.Name,
		"family":                     e.llmStatus.Family,
		"model":                      e.llmStatus.Model,
		"base_url":                   e.llmStatus.BaseURL,
		"api_key_env":                e.llmStatus.APIKeyEnv,
		"supports_web_search":        e.llmStatus.SupportsWebSearch,
		"web_search_enabled":         e.llmStatus.WebSearchEnabled,
		"think_mode":                 e.llmStatus.ThinkMode,
		"requires_explicit_model":    e.llmStatus.RequiresExplicitModel,
		"requires_explicit_base_url": e.llmStatus.RequiresExplicitBaseURL,
		"local":                      e.llmStatus.Local,
		"warning":                    e.llmWarning,
	}
	if err := e.emit(runID, taskID, "", "planning", "llm.status_updated", "llm", payload, nil); err != nil {
		return err
	}
	return e.memory.RecordLLMStatus(memstore.LLMStatusRecord{
		SessionID: e.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Status:    *e.llmStatus,
		Warning:   e.llmWarning,
		UpdatedAt: time.Now().UTC(),
	})
}
