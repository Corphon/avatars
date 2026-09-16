package events

import "time"

type Envelope struct {
	EventID   string         `json:"event_id"`
	Sequence  uint64         `json:"sequence"`
	RunID     string         `json:"run_id"`
	TaskID    string         `json:"task_id"`
	AvatarID  string         `json:"avatar_id,omitempty"`
	Phase     string         `json:"phase"`
	Type      string         `json:"type"`
	EmittedAt time.Time      `json:"emitted_at"`
	Source    string         `json:"source"`
	Payload   map[string]any `json:"payload"`
	StageHint map[string]any `json:"stage_hint,omitempty"`
}
