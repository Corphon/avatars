package workflow

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	memstore "avatars/internal/memory"
)

func TestS3_ReadPitfalls_AcceptsVerificationKind(t *testing.T) {
	tmp := t.TempDir()
	store, err := memstore.NewSQLiteStore(filepath.Join(tmp, "memory"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	SetMemoryStore(store)
	t.Cleanup(func() { SetMemoryStore(nil) })

	if err := store.RecordWarmLesson(memstore.WarmLessonRecord{
		SessionID:  "workflow",
		RunID:      "run-s3",
		TaskID:     "task-s3",
		Kind:       "verification",
		Summary:    "Before marking complete, re-run verifier for write and expect PASS.",
		Source:     "verification",
		Confidence: "high",
		UpdatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordWarmLesson: %v", err)
	}

	out := ReadPitfalls(tmp)
	if !strings.Contains(out, "re-run verifier") {
		t.Fatalf("expected verification pitfall to surface, got: %q", out)
	}
}

func TestS3_LoadMemoryContext_MergesProjectLessons(t *testing.T) {
	tmp := t.TempDir()
	store, err := memstore.NewSQLiteStore(filepath.Join(tmp, "memory"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	SetMemoryStore(store)
	t.Cleanup(func() { SetMemoryStore(nil) })

	if err := store.RecordProjectLesson(memstore.ProjectLessonRecord{
		SourceTaskID: "task-a",
		Kind:         "workflow_pattern",
		Summary:      "Keep planner decomposition stable before execution.",
		Source:       "warm_lesson",
		Confidence:   "high",
		UpdatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordProjectLesson: %v", err)
	}
	if err := store.RecordWarmLesson(memstore.WarmLessonRecord{
		SessionID:  "workflow",
		RunID:      "run-b",
		TaskID:     "task-b",
		Kind:       "tool_runtime",
		Summary:    "Address shell timeout before retrying write.",
		Source:     "tool_runtime",
		Confidence: "high",
		UpdatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordWarmLesson: %v", err)
	}

	ctx := LoadMemoryContext()
	if !strings.Contains(ctx, "Keep planner decomposition") {
		t.Fatalf("expected project lesson in context, got: %q", ctx)
	}
	if !strings.Contains(ctx, "shell timeout") {
		t.Fatalf("expected warm lesson in context, got: %q", ctx)
	}
}

func TestS3_IsActionablePitfallKind(t *testing.T) {
	if !isActionablePitfallKind("verification") {
		t.Fatal("verification should be actionable")
	}
	if isActionablePitfallKind("intent_classification") {
		t.Fatal("intent_classification should stay out of pitfall lists")
	}
}
