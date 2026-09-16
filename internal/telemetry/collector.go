package telemetry

import (
	"strings"
	"time"
)

type Snapshot struct {
	Mode             string
	Status           string
	Provider         string
	Model            string
	LLMRequests      int
	LLMCompletions   int
	LLMFailures      int
	LLMFallbacks     int
	ToolRequests     int
	ToolCompletions  int
	ToolFailures     int
	VerificationRuns int
	DurationMillis   int64
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostMicros       int64
	UsageKnown       bool
	UpdatedAt        time.Time
}

type Collector struct {
	startedAt time.Time
	snapshot  Snapshot
}

func NewCollector(mode string, startedAt time.Time) *Collector {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &Collector{
		startedAt: startedAt,
		snapshot: Snapshot{
			Mode:      strings.TrimSpace(mode),
			UpdatedAt: startedAt.UTC(),
		},
	}
}

func (c *Collector) ObserveEvent(eventType string, payload map[string]any) {
	if c == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "tool.requested":
		c.snapshot.ToolRequests++
	case "tool.completed":
		c.snapshot.ToolCompletions++
	case "tool.failed":
		c.snapshot.ToolFailures++
	case "tool.denied":
		c.snapshot.ToolFailures++
	case "tool.awaiting_approval":
		c.snapshot.ToolFailures++
	case "verification.completed":
		c.snapshot.VerificationRuns++
	case "critic.project_health_verified", "builder.project_health_verified":
		// R4-3: cross-lang health gates are real verification even without
		// the formal verifier tool path.
		c.snapshot.VerificationRuns++
	case "llm.completed":
		c.snapshot.LLMRequests++
		c.snapshot.LLMCompletions++
		c.snapshot.Provider = firstNonEmpty(asString(payload["provider"]), c.snapshot.Provider)
		c.snapshot.Model = firstNonEmpty(asString(payload["model"]), c.snapshot.Model)
		if asBool(payload["fallback"]) {
			c.snapshot.LLMFallbacks++
		}
		c.mergeUsage(payload)
	case "llm.failed":
		c.snapshot.LLMRequests++
		c.snapshot.LLMFailures++
		c.snapshot.Provider = firstNonEmpty(asString(payload["provider"]), c.snapshot.Provider)
	}
}

func (c *Collector) Finish(status string) Snapshot {
	if c == nil {
		return Snapshot{Status: strings.TrimSpace(status), UpdatedAt: time.Now().UTC()}
	}
	finishedAt := time.Now().UTC()
	c.snapshot.Status = strings.TrimSpace(status)
	c.snapshot.UpdatedAt = finishedAt
	c.snapshot.DurationMillis = finishedAt.Sub(c.startedAt).Milliseconds()
	if c.snapshot.DurationMillis < 0 {
		c.snapshot.DurationMillis = 0
	}
	return c.snapshot
}

func (c *Collector) mergeUsage(payload map[string]any) {
	if c == nil || !asBool(payload["usage_known"]) {
		return
	}
	c.snapshot.UsageKnown = true
	c.snapshot.PromptTokens += asInt(payload["prompt_tokens"])
	c.snapshot.CompletionTokens += asInt(payload["completion_tokens"])
	c.snapshot.TotalTokens += asInt(payload["total_tokens"])
	c.snapshot.CostMicros += asInt64(payload["cost_micros"])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func asString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func asBool(value any) bool {
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	return false
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func asInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}
