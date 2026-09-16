package runtime

import "testing"

func TestAvatarMailbox_RoutesDirectedAsk(t *testing.T) {
	mailbox := &Mailbox{}
	ask := mailbox.Append(AvatarMessage{
		RunID:        "run-1",
		TaskID:       "task-1",
		Type:         "ask",
		FromAvatarID: "avatar-researcher",
		FromRole:     "Researcher",
		ToAvatarID:   "avatar-planner",
		ToRole:       "Planner",
		Content:      "Confirm repository context.",
		Summary:      "Researcher ask: confirm repository context.",
	})

	if ask.MessageID == "" {
		t.Fatal("expected message id")
	}
	plannerInbox := mailbox.ForAvatar("avatar-planner")
	if len(plannerInbox) != 1 {
		t.Fatalf("expected one planner inbox message, got %+v", plannerInbox)
	}
	if plannerInbox[0].Type != "ask" || plannerInbox[0].FromAvatarID != "avatar-researcher" {
		t.Fatalf("expected directed ask in planner inbox, got %+v", plannerInbox[0])
	}
	if len(mailbox.Broadcasts()) != 0 {
		t.Fatalf("expected no broadcasts for directed ask, got %+v", mailbox.Broadcasts())
	}
}

func TestAvatarMailbox_KeepsTaskBroadcastReport(t *testing.T) {
	mailbox := &Mailbox{}
	report := mailbox.Append(AvatarMessage{
		RunID:        "run-1",
		TaskID:       "task-1",
		Type:         "report",
		FromAvatarID: "avatar-researcher",
		Content:      "Repository context is sufficient.",
		Summary:      "Researcher report: repository context is sufficient.",
	})

	if report.BroadcastScope != "task" {
		t.Fatalf("expected task broadcast by default, got %+v", report)
	}
	broadcasts := mailbox.Broadcasts()
	if len(broadcasts) != 1 || broadcasts[0].MessageID != report.MessageID {
		t.Fatalf("expected report broadcast, got %+v", broadcasts)
	}
}

func TestAvatarMailbox_TracksReplyAndHandoffArtifacts(t *testing.T) {
	mailbox := &Mailbox{}
	ask := mailbox.Append(AvatarMessage{
		RunID:        "run-1",
		TaskID:       "task-1",
		Type:         "ask",
		FromAvatarID: "avatar-researcher",
		ToAvatarID:   "avatar-planner",
		Content:      "Confirm repository context.",
	})
	reply := mailbox.Append(AvatarMessage{
		RunID:        "run-1",
		TaskID:       "task-1",
		Type:         "report",
		FromAvatarID: "avatar-planner",
		ToAvatarID:   "avatar-researcher",
		ReplyTo:      ask.MessageID,
		Content:      "Context is sufficient.",
	})
	handoff := mailbox.Append(AvatarMessage{
		RunID:        "run-1",
		TaskID:       "task-1",
		Type:         "handoff",
		FromAvatarID: "avatar-researcher",
		ToAvatarID:   "avatar-builder",
		FromNodeID:   "node-survey",
		ToNodeID:     "node-build",
		Artifacts: []ArtifactRef{{
			ID:      "artifact-survey",
			Kind:    "summary",
			Path:    "process_record.md",
			Summary: "Read process_record.md successfully.",
			OwnerID: "avatar-researcher",
		}},
	})

	replies := mailbox.ReplyTo(ask.MessageID)
	if len(replies) != 1 || replies[0].MessageID != reply.MessageID {
		t.Fatalf("expected reply linkage, got %+v", replies)
	}
	builderInbox := mailbox.ForAvatar("avatar-builder")
	if len(builderInbox) != 1 || builderInbox[0].FromNodeID != "node-survey" || builderInbox[0].ToNodeID != "node-build" {
		t.Fatalf("expected handoff in builder inbox, got %+v", builderInbox)
	}
	if len(handoff.Artifacts) != 1 || handoff.Artifacts[0].OwnerID != "avatar-researcher" {
		t.Fatalf("expected handoff artifact owner, got %+v", handoff)
	}
}

func TestAvatarMessagePayload_IncludesArtifactRefs(t *testing.T) {
	payload := avatarMessagePayload(AvatarMessage{
		RunID:          "run-1",
		TaskID:         "task-1",
		Type:           "handoff",
		FromAvatarID:   "avatar-builder",
		ToAvatarID:     "avatar-critic",
		BroadcastScope: "task",
		Artifacts:      []ArtifactRef{{ID: "artifact-skill", Kind: "file", Path: "skills/generated/task.md"}},
	})

	if payload["artifact_count"] != 1 {
		t.Fatalf("expected artifact count, got %+v", payload)
	}
	artifacts, ok := payload["artifacts"].([]map[string]any)
	if !ok || len(artifacts) != 1 || artifacts[0]["path"] != "skills/generated/task.md" {
		t.Fatalf("expected artifact payload, got %+v", payload["artifacts"])
	}
}
