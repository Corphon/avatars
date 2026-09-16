package runtime

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

type ArtifactRef struct {
	ID      string `json:"id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Path    string `json:"path,omitempty"`
	Summary string `json:"summary,omitempty"`
	OwnerID string `json:"owner_id,omitempty"`
}

type AvatarMessage struct {
	MessageID      string        `json:"message_id"`
	RunID          string        `json:"run_id"`
	TaskID         string        `json:"task_id"`
	Type           string        `json:"message_type"`
	FromAvatarID   string        `json:"from_avatar_id,omitempty"`
	FromRole       string        `json:"from_role,omitempty"`
	ToAvatarID     string        `json:"to_avatar_id,omitempty"`
	ToRole         string        `json:"to_role,omitempty"`
	BroadcastScope string        `json:"broadcast_scope,omitempty"`
	ReplyTo        string        `json:"reply_to,omitempty"`
	Content        string        `json:"content,omitempty"`
	Summary        string        `json:"summary,omitempty"`
	Scene          string        `json:"scene,omitempty"`
	FromNodeID     string        `json:"from_node_id,omitempty"`
	FromTitle      string        `json:"from_title,omitempty"`
	ToNodeID       string        `json:"to_node_id,omitempty"`
	ToTitle        string        `json:"to_title,omitempty"`
	Artifacts      []ArtifactRef `json:"artifacts,omitempty"`
	CreatedAt      time.Time     `json:"created_at,omitempty"`
}

type Mailbox struct {
	messages []AvatarMessage
}

func (m *Mailbox) Append(message AvatarMessage) AvatarMessage {
	normalized := normalizeAvatarMessage(message)
	m.messages = append(m.messages, normalized)
	return normalized
}

func (m *Mailbox) ForAvatar(avatarID string) []AvatarMessage {
	trimmed := strings.TrimSpace(avatarID)
	if trimmed == "" {
		return nil
	}
	messages := make([]AvatarMessage, 0, len(m.messages))
	for _, message := range m.messages {
		if message.ToAvatarID != trimmed {
			continue
		}
		messages = append(messages, cloneAvatarMessage(message))
	}
	return messages
}

func (m *Mailbox) Broadcasts() []AvatarMessage {
	messages := make([]AvatarMessage, 0, len(m.messages))
	for _, message := range m.messages {
		if strings.TrimSpace(message.BroadcastScope) == "" {
			continue
		}
		messages = append(messages, cloneAvatarMessage(message))
	}
	return messages
}

func (m *Mailbox) ReplyTo(messageID string) []AvatarMessage {
	trimmed := strings.TrimSpace(messageID)
	if trimmed == "" {
		return nil
	}
	messages := make([]AvatarMessage, 0, len(m.messages))
	for _, message := range m.messages {
		if message.ReplyTo != trimmed {
			continue
		}
		messages = append(messages, cloneAvatarMessage(message))
	}
	return messages
}

func (m *Mailbox) Messages() []AvatarMessage {
	messages := make([]AvatarMessage, len(m.messages))
	for index, message := range m.messages {
		messages[index] = cloneAvatarMessage(message)
	}
	return messages
}

func normalizeAvatarMessage(message AvatarMessage) AvatarMessage {
	message.MessageID = strings.TrimSpace(message.MessageID)
	if message.MessageID == "" {
		message.MessageID = stableAvatarMessageID(message)
	}
	message.RunID = strings.TrimSpace(message.RunID)
	message.TaskID = strings.TrimSpace(message.TaskID)
	message.Type = strings.TrimSpace(message.Type)
	message.FromAvatarID = strings.TrimSpace(message.FromAvatarID)
	message.FromRole = strings.TrimSpace(message.FromRole)
	message.ToAvatarID = strings.TrimSpace(message.ToAvatarID)
	message.ToRole = strings.TrimSpace(message.ToRole)
	message.BroadcastScope = strings.TrimSpace(message.BroadcastScope)
	if message.ToAvatarID == "" && message.BroadcastScope == "" {
		message.BroadcastScope = "task"
	}
	message.ReplyTo = strings.TrimSpace(message.ReplyTo)
	message.Content = strings.TrimSpace(message.Content)
	message.Summary = strings.TrimSpace(message.Summary)
	message.Scene = strings.TrimSpace(message.Scene)
	message.FromNodeID = strings.TrimSpace(message.FromNodeID)
	message.FromTitle = strings.TrimSpace(message.FromTitle)
	message.ToNodeID = strings.TrimSpace(message.ToNodeID)
	message.ToTitle = strings.TrimSpace(message.ToTitle)
	message.Artifacts = cleanArtifactRefs(message.Artifacts)
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	return message
}

func stableAvatarMessageID(message AvatarMessage) string {
	parts := []string{
		strings.TrimSpace(message.RunID),
		strings.TrimSpace(message.TaskID),
		strings.TrimSpace(message.FromAvatarID),
		strings.TrimSpace(message.ToAvatarID),
		strings.TrimSpace(message.BroadcastScope),
		strings.TrimSpace(message.Type),
		strings.TrimSpace(message.FromNodeID),
		strings.TrimSpace(message.ToNodeID),
		strings.TrimSpace(message.Content),
		strings.TrimSpace(message.Summary),
	}
	joined := strings.Join(parts, "|")
	sum := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("msg-%x", sum[:8])
}

func cleanArtifactRefs(values []ArtifactRef) []ArtifactRef {
	if len(values) == 0 {
		return nil
	}
	cleaned := make([]ArtifactRef, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.Kind = strings.TrimSpace(value.Kind)
		value.Path = strings.TrimSpace(value.Path)
		value.Summary = strings.TrimSpace(value.Summary)
		value.OwnerID = strings.TrimSpace(value.OwnerID)
		if value.ID == "" && value.Kind == "" && value.Path == "" && value.Summary == "" {
			continue
		}
		key := value.ID + "|" + value.Kind + "|" + value.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, value)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func cloneAvatarMessage(message AvatarMessage) AvatarMessage {
	message.Artifacts = append([]ArtifactRef(nil), message.Artifacts...)
	return message
}

func avatarMessagePayload(message AvatarMessage) map[string]any {
	normalized := normalizeAvatarMessage(message)
	payload := map[string]any{
		"message_id":   normalized.MessageID,
		"message_type": normalized.Type,
		"content":      normalized.Content,
	}
	if normalized.FromAvatarID != "" {
		payload["from_avatar_id"] = normalized.FromAvatarID
	}
	if normalized.FromRole != "" {
		payload["from_role"] = normalized.FromRole
	}
	if normalized.ToAvatarID != "" {
		payload["to_avatar_id"] = normalized.ToAvatarID
	}
	if normalized.ToRole != "" {
		payload["to_role"] = normalized.ToRole
	}
	if normalized.BroadcastScope != "" {
		payload["broadcast_scope"] = normalized.BroadcastScope
	}
	if normalized.ReplyTo != "" {
		payload["reply_to"] = normalized.ReplyTo
	}
	if normalized.Summary != "" {
		payload["summary"] = normalized.Summary
	}
	if normalized.FromNodeID != "" {
		payload["from_node_id"] = normalized.FromNodeID
	}
	if normalized.FromTitle != "" {
		payload["from_title"] = normalized.FromTitle
	}
	if normalized.ToNodeID != "" {
		payload["to_node_id"] = normalized.ToNodeID
	}
	if normalized.ToTitle != "" {
		payload["to_title"] = normalized.ToTitle
	}
	if len(normalized.Artifacts) > 0 {
		payload["artifacts"] = artifactRefsPayload(normalized.Artifacts)
		payload["artifact_count"] = len(normalized.Artifacts)
	}
	return payload
}

func artifactRefsPayload(refs []ArtifactRef) []map[string]any {
	payloads := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		payload := map[string]any{}
		if ref.ID != "" {
			payload["id"] = ref.ID
		}
		if ref.Kind != "" {
			payload["kind"] = ref.Kind
		}
		if ref.Path != "" {
			payload["path"] = ref.Path
		}
		if ref.Summary != "" {
			payload["summary"] = ref.Summary
		}
		if ref.OwnerID != "" {
			payload["owner_id"] = ref.OwnerID
		}
		if len(payload) > 0 {
			payloads = append(payloads, payload)
		}
	}
	if len(payloads) == 0 {
		return nil
	}
	return payloads
}
