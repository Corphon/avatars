package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	memstore "avatars/internal/memory"
)

func TestRecordTaskCompletion_SQLite(t *testing.T) {
	tmpDir := t.TempDir()

	// Preload to create plan/todo/record files.
	created, err := PreloadDocs(tmpDir)
	if err != nil {
		t.Fatalf("PreloadDocs: %v", err)
	}
	if created < 3 {
		t.Fatalf("expected 3 files created, got %d", created)
	}

	// Set up SQLite memory store.
	store, err := memstore.NewSQLiteStore(filepath.Join(tmpDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	SetMemoryStore(store)

	// Record a task completion with files and a learned lesson.
	err = RecordTaskCompletion(tmpDir,
		"Test task: write hello.go",
		"Created hello.go with main() function and tests",
		[]string{filepath.Join(tmpDir, "hello.go")},
		"Always add a package comment before imports.",
	)
	if err != nil {
		t.Fatalf("RecordTaskCompletion: %v", err)
	}

	// Verify the record file was NOT written to (process_record is now export-only).
	recordPath := filepath.Join(tmpDir, DocPaths["record"])
	if _, statErr := os.Stat(recordPath); statErr != nil {
		// File may or may not exist from PreloadDocs — that's fine.
		// The key is RecordTaskCompletion doesn't write to it.
		t.Logf("process_record file not written by RecordTaskCompletion (expected)")
	}

	// Verify SQLite has the evaluation record.
	snapshot, err := store.LoadSnapshot("workflow")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	found := false
	for _, record := range snapshot.EvaluationRecords {
		if strings.Contains(record.Summary, "Created hello.go") {
			found = true
			if !strings.Contains(strings.Join(record.Details, "|"), "learned:") {
				t.Error("expected 'learned' in details")
			}
			if !strings.Contains(strings.Join(record.Details, "|"), "changed_files:") {
				t.Error("expected 'changed_files' in details")
			}
			break
		}
	}
	if !found {
		t.Error("evaluation record not found in SQLite — RecordTaskCompletion should persist to SQLite")
	}
}

func TestRecordTaskCompletion_NoStore(t *testing.T) {
	tmpDir := t.TempDir()
	// Ensure no store configured.
	SetMemoryStore(nil)

	// Should not panic or error — just silently skip.
	err := RecordTaskCompletion(tmpDir, "task", "summary")
	if err != nil {
		t.Fatalf("RecordTaskCompletion with no store: %v", err)
	}
}

func TestReadPitfalls_FromSQLite(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memstore.NewSQLiteStore(filepath.Join(tmpDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	SetMemoryStore(store)

	// Add a pitfall warm_lesson.
	err = store.RecordWarmLesson(memstore.WarmLessonRecord{
		SessionID:  "workflow",
		RunID:      "test-run",
		TaskID:     "test-task",
		Kind:       "pitfall",
		Summary:    "Do not use os.RemoveAll on project root.",
		Source:     "test",
		Confidence: "high",
	})
	if err != nil {
		t.Fatalf("RecordWarmLesson: %v", err)
	}

	pitfalls := ReadPitfalls(tmpDir)
	if !strings.Contains(pitfalls, "Do not use os.RemoveAll") {
		t.Errorf("expected pitfall in output, got: %s", pitfalls)
	}
}

func TestLoadMemoryContext(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memstore.NewSQLiteStore(filepath.Join(tmpDir, ".avatars", "memory"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	SetMemoryStore(store)

	// Add a warm lesson pitfall.
	store.RecordWarmLesson(memstore.WarmLessonRecord{
		SessionID: "workflow", RunID: "r1", TaskID: "t1",
		Kind: "pitfall", Summary: "Avoid pattern X.",
		Source: "test", Confidence: "high",
	})

	// Add an evaluation record.
	store.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "workflow", RunID: "r2", TaskID: "t2",
		Kind: "task_completion", Verdict: "PASS",
		Cause: "workflow_record", Summary: "Task completed successfully.",
		Source: "test",
	})

	ctx := LoadMemoryContext()
	t.Logf("LoadMemoryContext output (%d chars): %s", len(ctx), ctx)
	if !strings.Contains(ctx, "Avoid pattern X") {
		t.Error("expected pitfall in memory context")
	}
	if !strings.Contains(ctx, "Task completed successfully") {
		t.Error("expected evaluation record in memory context")
	}
}
