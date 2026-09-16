package runtime

import (
	"fmt"
	"strings"

	"avatars/internal/events"
	memstore "avatars/internal/memory"
)

// SummarizeHotEvent formats collaboration / MCP / tool hot events for
// operators (SQLite hot-event summaries and CLI stderr progress). S6.5:
// CLI runProgressLine reuses this instead of duplicating handoff/spoke logic.
func SummarizeHotEvent(envelope events.Envelope) string {
	return summarizeHotEvent(envelope)
}

func summarizeHotEvent(envelope events.Envelope) string {
	switch envelope.Type {
	case "task.decomposed":
		summary := payloadString(envelope.Payload, "summary")
		if summary == "" {
			return ""
		}
		return fmt.Sprintf("Planner decomposed the task: %s", summary)
	case "workflow.node_created":
		title := payloadString(envelope.Payload, "title")
		role := payloadString(envelope.Payload, "assigned_role")
		if title == "" {
			return ""
		}
		if role == "" {
			return fmt.Sprintf("Workflow node created: %s", title)
		}
		return fmt.Sprintf("Workflow node created for %s: %s", role, title)
	case "avatar.assigned":
		title := payloadString(envelope.Payload, "title")
		role := payloadString(envelope.Payload, "role")
		if title == "" {
			return ""
		}
		if role == "" {
			return fmt.Sprintf("Avatar assigned: %s", title)
		}
		return fmt.Sprintf("%s assigned: %s", role, title)
	case "avatar.handoff":
		summary := payloadString(envelope.Payload, "summary")
		if summary != "" {
			return summary
		}
		fromRole := payloadString(envelope.Payload, "from_role")
		fromTitle := payloadString(envelope.Payload, "from_title")
		toRole := payloadString(envelope.Payload, "to_role")
		toTitle := payloadString(envelope.Payload, "to_title")
		if fromRole == "" && toRole == "" && toTitle == "" {
			return ""
		}
		if fromRole == "" {
			if toRole == "" {
				return fmt.Sprintf("Avatar handoff prepared for %s.", toTitle)
			}
			return fmt.Sprintf("Work handed off to %s for %s.", toRole, toTitle)
		}
		if toRole == "" {
			if fromTitle == "" {
				return fmt.Sprintf("%s handed off completed work.", fromRole)
			}
			return fmt.Sprintf("%s handed off %s.", fromRole, fromTitle)
		}
		if fromTitle == "" {
			if toTitle == "" {
				return fmt.Sprintf("%s handed off work to %s.", fromRole, toRole)
			}
			return fmt.Sprintf("%s handed off work to %s for %s.", fromRole, toRole, toTitle)
		}
		if toTitle == "" {
			return fmt.Sprintf("%s handed off %s to %s.", fromRole, fromTitle, toRole)
		}
		return fmt.Sprintf("%s handed off %s to %s for %s.", fromRole, fromTitle, toRole, toTitle)
	case "avatar.spoke":
		messageType := payloadString(envelope.Payload, "message_type")
		if messageType != "report" && messageType != "ask" && messageType != "challenge" && messageType != "summarize" {
			return ""
		}
		summary := payloadString(envelope.Payload, "summary")
		if summary != "" {
			return summarizeAvatarMessageWithRouting(envelope.Payload, summary)
		}
		return summarizeAvatarMessageWithRouting(envelope.Payload, payloadString(envelope.Payload, "content"))
	case "tool.requested":
		toolName := payloadString(envelope.Payload, "tool")
		operation := payloadString(envelope.Payload, "operation")
		if toolName == "" {
			return ""
		}
		if operation == "" {
			return fmt.Sprintf("Tool requested: %s", toolName)
		}
		return fmt.Sprintf("Tool requested: %s (%s)", toolName, operation)
	case "tool.completed":
		return payloadString(envelope.Payload, "summary")
	case "tool.failed":
		toolName := payloadString(envelope.Payload, "tool")
		errText := payloadString(envelope.Payload, "error")
		if toolName == "" && errText == "" {
			return ""
		}
		if toolName == "" {
			return fmt.Sprintf("Tool failed: %s", errText)
		}
		if errText == "" {
			return fmt.Sprintf("Tool failed: %s", toolName)
		}
		return fmt.Sprintf("Tool failed: %s (%s)", toolName, errText)
	case "tool.denied":
		toolName := payloadString(envelope.Payload, "tool")
		errText := payloadString(envelope.Payload, "error")
		if toolName == "" && errText == "" {
			return ""
		}
		if toolName == "" {
			return fmt.Sprintf("Tool denied: %s", errText)
		}
		if errText == "" {
			return fmt.Sprintf("Tool denied: %s", toolName)
		}
		return fmt.Sprintf("Tool denied: %s (%s)", toolName, errText)
	case "tool.awaiting_approval":
		toolName := payloadString(envelope.Payload, "tool")
		errText := payloadString(envelope.Payload, "error")
		if toolName == "" && errText == "" {
			return ""
		}
		if toolName == "" {
			return fmt.Sprintf("Tool awaiting approval: %s", errText)
		}
		if errText == "" {
			return fmt.Sprintf("Tool awaiting approval: %s", toolName)
		}
		return fmt.Sprintf("Tool awaiting approval: %s (%s)", toolName, errText)
	case "memory.resume_restored":
		source := payloadString(envelope.Payload, "source_transcript")
		if source == "" {
			return ""
		}
		return fmt.Sprintf("Resume state restored from %s", source)
	case "mcp.completed":
		serverURL := payloadString(envelope.Payload, "server_url")
		method := payloadString(envelope.Payload, "method")
		if serverURL == "" && method == "" {
			return ""
		}
		parts := []string{"MCP call completed"}
		if method != "" {
			parts = append(parts, method)
		}
		if serverURL != "" {
			parts = append(parts, "at "+serverURL)
		}
		return strings.Join(parts, " ")
	case "workflow.edge_advanced":
		fromTitle := payloadString(envelope.Payload, "from_title")
		toTitle := payloadString(envelope.Payload, "to_title")
		fromID := payloadString(envelope.Payload, "from_node_id")
		toID := payloadString(envelope.Payload, "to_node_id")
		from := fromTitle
		if from == "" {
			from = fromID
		}
		to := toTitle
		if to == "" {
			to = toID
		}
		if from == "" || to == "" {
			return ""
		}
		return fmt.Sprintf("Edge: %s → %s", from, to)
	default:
		return ""
	}
}

func summarizeAvatarMessageWithRouting(payload map[string]any, summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	if artifactCount := payloadInt(payload, "artifact_count"); artifactCount > 0 {
		summary = fmt.Sprintf("%s Artifacts: %d.", summary, artifactCount)
	}
	routeCue := avatarMessageRouteCue(payload)
	if routeCue == "" {
		return summary
	}
	markerIndex := strings.Index(summary, ": ")
	if markerIndex >= 0 {
		prefix := summary[:markerIndex+2]
		suffix := summary[markerIndex+2:]
		return prefix + "[" + routeCue + "] " + suffix
	}
	return summary + " [" + routeCue + "]"
}

func avatarMessageRouteCue(payload map[string]any) string {
	toRole := payloadString(payload, "to_role")
	if toRole != "" {
		return "to " + toRole
	}
	toAvatarID := payloadString(payload, "to_avatar_id")
	if toAvatarID != "" {
		return "to " + toAvatarID
	}
	broadcastScope := payloadString(payload, "broadcast_scope")
	if broadcastScope != "" {
		return "broadcast " + broadcastScope
	}
	return ""
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func payloadInt(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	value, ok := payload[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func (e *Engine) persistHotEvent(envelope events.Envelope) error {
	if e == nil || e.memory == nil {
		return nil
	}
	summary := summarizeHotEvent(envelope)
	if summary == "" {
		return nil
	}
	return e.memory.RecordEvent(memstore.EventRecord{
		SessionID: e.sessionID,
		RunID:     envelope.RunID,
		TaskID:    envelope.TaskID,
		AvatarID:  envelope.AvatarID,
		EventID:   envelope.EventID,
		EventType: envelope.Type,
		Phase:     envelope.Phase,
		Source:    envelope.Source,
		Summary:   summary,
		EmittedAt: envelope.EmittedAt,
	})
}
