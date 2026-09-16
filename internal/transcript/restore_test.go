// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package transcript

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"avatars/internal/events"
)

func TestRestore_UsesLatestBoundaryEvent(t *testing.T) {
	writer, err := NewJSONLWriter(t.TempDir(), "session-restore")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	entries := []events.Envelope{
		{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Phase: "planning", Type: "run.started", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"input": "demo"}},
		{EventID: "evt-task", Sequence: 2, RunID: "run-1", TaskID: "task-1", Phase: "planning", Type: "memory.summary_updated", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"scope": "task", "summary": "Task summary"}},
		{EventID: "evt-avatar", Sequence: 3, RunID: "run-1", TaskID: "task-1", AvatarID: "avatar-planner", Phase: "planning", Type: "memory.summary_updated", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"scope": "avatar", "summary": "Planner summary"}},
		{EventID: "evt-handoff", Sequence: 4, RunID: "run-1", TaskID: "task-1", AvatarID: "avatar-researcher", Phase: "planning", Type: "avatar.handoff", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"summary": "Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context."}},
		{EventID: "evt-2", Sequence: 5, RunID: "run-1", TaskID: "task-1", Phase: "reviewing", Type: "run.completed", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"status": "completed", "summary": "First summary"}},
		{EventID: "evt-3", Sequence: 6, RunID: "run-1", TaskID: "task-1", Phase: "reviewing", Type: "memory.compaction_boundary_written", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"status": "completed", "summary": "Boundary summary", "boundary_kind": "run_terminal"}},
	}
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	snapshot, err := Restore(writer.Path())
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if snapshot.Summary != "Boundary summary" {
		t.Fatalf("expected boundary summary, got %q", snapshot.Summary)
	}
	if snapshot.BoundaryType != "run_terminal" {
		t.Fatalf("expected run_terminal boundary, got %q", snapshot.BoundaryType)
	}
	if snapshot.TaskSummary != "Task summary" {
		t.Fatalf("expected task summary, got %q", snapshot.TaskSummary)
	}
	if snapshot.AvatarSummaries["avatar-planner"] != "Planner summary" {
		t.Fatalf("expected planner summary, got %q", snapshot.AvatarSummaries["avatar-planner"])
	}
	if snapshot.EventCount != 6 {
		t.Fatalf("expected 6 events, got %d", snapshot.EventCount)
	}
	if len(snapshot.RecentEvents) == 0 {
		t.Fatal("expected recent events to include handoff summary")
	}
	if snapshot.RecentEvents[0] != "Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context." {
		t.Fatalf("expected latest recent event to be handoff summary, got %q", snapshot.RecentEvents[0])
	}
}

func TestRestore_FallsBackToRunCompletedWhenBoundaryMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := os.WriteFile(path, []byte("{\"event_id\":\"evt-task\",\"sequence\":1,\"run_id\":\"run-legacy\",\"task_id\":\"task-legacy\",\"phase\":\"planning\",\"type\":\"memory.summary_updated\",\"emitted_at\":\"2026-04-28T00:00:00Z\",\"source\":\"memory\",\"payload\":{\"scope\":\"task\",\"summary\":\"Legacy task summary\"}}\n{\"event_id\":\"evt-1\",\"sequence\":2,\"run_id\":\"run-legacy\",\"task_id\":\"task-legacy\",\"phase\":\"reviewing\",\"type\":\"run.completed\",\"emitted_at\":\"2026-04-28T00:00:01Z\",\"source\":\"runtime\",\"payload\":{\"status\":\"completed\",\"summary\":\"Legacy summary\"}}\n"), 0o644); err != nil {
		t.Fatalf("write legacy transcript failed: %v", err)
	}

	snapshot, err := Restore(path)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if snapshot.BoundaryType != "run_terminal_fallback" {
		t.Fatalf("expected fallback boundary, got %q", snapshot.BoundaryType)
	}
	if snapshot.Summary != "Legacy summary" {
		t.Fatalf("expected legacy summary, got %q", snapshot.Summary)
	}
	if snapshot.TaskSummary != "Legacy task summary" {
		t.Fatalf("expected legacy task summary, got %q", snapshot.TaskSummary)
	}
}

func TestRestore_UsesLatestRunLifecycleUpdate(t *testing.T) {
	writer, err := NewJSONLWriter(t.TempDir(), "session-lifecycle")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	entries := []events.Envelope{
		{EventID: "evt-1", Sequence: 1, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "run.completed", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"status": "awaiting_approval", "summary": "approval required"}},
		{EventID: "evt-2", Sequence: 2, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "memory.compaction_boundary_written", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"status": "awaiting_approval", "summary": "approval required", "boundary_kind": "run_terminal"}},
		{EventID: "evt-3", Sequence: 3, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "run.lifecycle_updated", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"status": "continued", "summary": "Continued approved request for write/file_write.", "approval_key": "approval-key-demo", "continuation_run_id": "run-origin-continued", "continuation_task_id": "task-1", "continuation_transcript": "sessions/continued.jsonl"}},
	}
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	snapshot, err := Restore(writer.Path())
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if snapshot.Status != "continued" {
		t.Fatalf("expected lifecycle status continued, got %q", snapshot.Status)
	}
	if snapshot.BoundaryType != "run_lifecycle" {
		t.Fatalf("expected lifecycle boundary type, got %q", snapshot.BoundaryType)
	}
	if snapshot.Summary != "Continued approved request for write/file_write." {
		t.Fatalf("expected lifecycle summary, got %q", snapshot.Summary)
	}
}

func TestRestore_RestoresPausePointAndResumeAttemptTruth(t *testing.T) {
	writer, err := NewJSONLWriter(t.TempDir(), "session-pause-attempt")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	entries := []events.Envelope{
		{EventID: "evt-1", Sequence: 1, RunID: "run-origin-continued", TaskID: "task-1", Phase: "reviewing", Type: "run.started", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"mode": "approval_continuation", "pause_point_id": "pause-approval", "pause_point_kind": "approval_gate", "resume_attempt_id": "resume-1"}},
		{EventID: "evt-2", Sequence: 2, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "tool.approval_continued", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"pause_point_id": "pause-approval", "pause_point_kind": "approval_gate", "resume_attempt_id": "resume-1", "status": "completed", "summary": "Continued approved request."}},
		{EventID: "evt-3", Sequence: 3, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "run.lifecycle_updated", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"status": "continued", "summary": "Continued approved request."}},
	}
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	snapshot, err := Restore(writer.Path())
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if snapshot.PausePointID != "pause-approval" || snapshot.PausePointKind != "approval_gate" {
		t.Fatalf("expected restored pause point, got %+v", snapshot)
	}
	if snapshot.ResumeAttemptID != "resume-1" || snapshot.ResumeAttemptStatus != "completed" {
		t.Fatalf("expected restored resume attempt, got %+v", snapshot)
	}
}

func TestRestore_RestoresLatestResumeAttemptStatus(t *testing.T) {
	writer, err := NewJSONLWriter(t.TempDir(), "session-latest-attempt")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	entries := []events.Envelope{
		{EventID: "evt-1", Sequence: 1, RunID: "run-continued", TaskID: "task-1", Phase: "reviewing", Type: "run.started", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"pause_point_id": "pause-1", "pause_point_kind": "approval_gate", "resume_attempt_id": "resume-1", "resume_attempt_status": "started"}},
		{EventID: "evt-2", Sequence: 2, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "tool.approval_continued", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"pause_point_id": "pause-1", "pause_point_kind": "approval_gate", "resume_attempt_id": "resume-1", "status": "failed", "summary": "Continuation failed."}},
		{EventID: "evt-3", Sequence: 3, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "tool.approval_continued", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"pause_point_id": "pause-1", "pause_point_kind": "approval_gate", "resume_attempt_id": "resume-2", "status": "completed", "summary": "Continuation completed."}},
		{EventID: "evt-4", Sequence: 4, RunID: "run-origin", TaskID: "task-1", Phase: "reviewing", Type: "run.lifecycle_updated", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"status": "continued", "summary": "Continuation completed."}},
	}
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	snapshot, err := Restore(writer.Path())
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if snapshot.ResumeAttemptID != "resume-2" || snapshot.ResumeAttemptStatus != "completed" {
		t.Fatalf("expected latest resume attempt status, got %+v", snapshot)
	}
}

func TestRestore_UsesTypedAvatarMessageSummaryInRecentEvents(t *testing.T) {
	writer, err := NewJSONLWriter(t.TempDir(), "session-avatar-message")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	entries := []events.Envelope{
		{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Phase: "planning", Type: "run.started", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"input": "demo"}},
		{EventID: "evt-report", Sequence: 2, RunID: "run-1", TaskID: "task-1", AvatarID: "avatar-builder", Phase: "executing", Type: "avatar.spoke", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"message_type": "report", "broadcast_scope": "task", "summary": "Builder report: generated candidate skill at skills/generated/task-survey-skill.md."}},
		{EventID: "evt-2", Sequence: 3, RunID: "run-1", TaskID: "task-1", Phase: "reviewing", Type: "memory.compaction_boundary_written", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"status": "completed", "summary": "Boundary summary", "boundary_kind": "run_terminal"}},
	}
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	snapshot, err := Restore(writer.Path())
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if len(snapshot.RecentEvents) == 0 {
		t.Fatal("expected recent events to include avatar report summary")
	}
	if snapshot.RecentEvents[0] != "Builder report: [broadcast task] generated candidate skill at skills/generated/task-survey-skill.md." {
		t.Fatalf("expected latest recent event to be avatar report summary, got %q", snapshot.RecentEvents[0])
	}
}

func TestRestore_UsesAskAvatarMessageSummaryInRecentEvents(t *testing.T) {
	writer, err := NewJSONLWriter(t.TempDir(), "session-avatar-ask")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	entries := []events.Envelope{
		{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Phase: "planning", Type: "run.started", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"input": "demo"}},
		{EventID: "evt-ask", Sequence: 2, RunID: "run-1", TaskID: "task-1", AvatarID: "avatar-researcher", Phase: "executing", Type: "avatar.spoke", EmittedAt: time.Now().UTC(), Source: "runtime", Payload: map[string]any{"message_type": "ask", "to_role": "Planner", "summary": "Researcher ask: confirm whether the current repository context is sufficient before the builder locks the next implementation slice."}},
		{EventID: "evt-2", Sequence: 3, RunID: "run-1", TaskID: "task-1", Phase: "reviewing", Type: "memory.compaction_boundary_written", EmittedAt: time.Now().UTC(), Source: "memory", Payload: map[string]any{"status": "completed", "summary": "Boundary summary", "boundary_kind": "run_terminal"}},
	}
	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	snapshot, err := Restore(writer.Path())
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if len(snapshot.RecentEvents) == 0 {
		t.Fatal("expected recent events to include avatar ask summary")
	}
	if snapshot.RecentEvents[0] != "Researcher ask: [to Planner] confirm whether the current repository context is sufficient before the builder locks the next implementation slice." {
		t.Fatalf("expected latest recent event to be avatar ask summary, got %q", snapshot.RecentEvents[0])
	}
}
