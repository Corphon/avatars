package runtime

import (
	"time"

	memstore "avatars/internal/memory"
	runtelemetry "avatars/internal/telemetry"
)

func (e *Engine) beginTelemetry(mode string) {
	if e == nil {
		return
	}
	e.telemetry = runtelemetry.NewCollector(mode, time.Now().UTC())
}

func (e *Engine) finalizeTelemetry(runID string, taskID string, status string) error {
	if e == nil || e.telemetry == nil {
		return nil
	}
	collector := e.telemetry
	e.telemetry = nil
	snapshot := collector.Finish(status)
	payload := map[string]any{
		"mode":              snapshot.Mode,
		"status":            snapshot.Status,
		"provider":          snapshot.Provider,
		"model":             snapshot.Model,
		"llm_requests":      snapshot.LLMRequests,
		"llm_completions":   snapshot.LLMCompletions,
		"llm_failures":      snapshot.LLMFailures,
		"llm_fallbacks":     snapshot.LLMFallbacks,
		"tool_requests":     snapshot.ToolRequests,
		"tool_completions":  snapshot.ToolCompletions,
		"tool_failures":     snapshot.ToolFailures,
		"verification_runs": snapshot.VerificationRuns,
		"duration_ms":       snapshot.DurationMillis,
		"usage_known":       snapshot.UsageKnown,
		"prompt_tokens":     snapshot.PromptTokens,
		"completion_tokens": snapshot.CompletionTokens,
		"total_tokens":      snapshot.TotalTokens,
		"cost_micros":       snapshot.CostMicros,
	}
	if err := e.emit(runID, taskID, "", "reviewing", "telemetry.updated", "telemetry", payload, nil); err != nil {
		return err
	}
	if e.memory == nil {
		if e.transcript != nil {
			return e.transcript.Flush()
		}
		return nil
	}
	if err := e.memory.RecordTelemetry(memstore.TelemetryRecord{
		SessionID: e.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Snapshot:  snapshot,
	}); err != nil {
		return err
	}
	if e.transcript != nil {
		return e.transcript.Flush()
	}
	return nil
}
