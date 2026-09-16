// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package runtime

import (
	"testing"
	"time"

	"avatars/internal/events"
)

func TestSummarizeHotEvent_AvatarHandoff(t *testing.T) {
	envelope := events.Envelope{
		EventID:   "evt-handoff",
		Sequence:  4,
		RunID:     "run-1",
		TaskID:    "task-1",
		AvatarID:  "avatar-researcher",
		Phase:     "planning",
		Type:      "avatar.handoff",
		EmittedAt: time.Now().UTC(),
		Source:    "runtime",
		Payload: map[string]any{
			"from_role":  "Planner",
			"from_title": "Decompose task and set checkpoints",
			"to_role":    "Researcher",
			"to_title":   "Survey current repository context",
		},
	}

	summary := summarizeHotEvent(envelope)
	if summary != "Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context." {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestSummarizeHotEvent_AvatarSpokeReport(t *testing.T) {
	envelope := events.Envelope{
		EventID:   "evt-report",
		Sequence:  5,
		RunID:     "run-1",
		TaskID:    "task-1",
		AvatarID:  "avatar-builder",
		Phase:     "executing",
		Type:      "avatar.spoke",
		EmittedAt: time.Now().UTC(),
		Source:    "runtime",
		Payload: map[string]any{
			"message_type":    "report",
			"broadcast_scope": "task",
			"summary":         "Builder report: generated candidate skill at skills/generated/task-survey-skill.md.",
		},
	}

	summary := summarizeHotEvent(envelope)
	if summary != "Builder report: [broadcast task] generated candidate skill at skills/generated/task-survey-skill.md." {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestSummarizeHotEvent_AvatarSpokeAsk(t *testing.T) {
	envelope := events.Envelope{
		EventID:   "evt-ask",
		Sequence:  6,
		RunID:     "run-1",
		TaskID:    "task-1",
		AvatarID:  "avatar-researcher",
		Phase:     "executing",
		Type:      "avatar.spoke",
		EmittedAt: time.Now().UTC(),
		Source:    "runtime",
		Payload: map[string]any{
			"message_type": "ask",
			"to_role":      "Planner",
			"summary":      "Researcher ask: confirm whether the current repository context is sufficient before the builder locks the next implementation slice.",
		},
	}

	summary := summarizeHotEvent(envelope)
	if summary != "Researcher ask: [to Planner] confirm whether the current repository context is sufficient before the builder locks the next implementation slice." {
		t.Fatalf("unexpected summary: %q", summary)
	}
}
