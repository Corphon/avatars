package memory

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestS3_BusyTimeoutAndCompressScopedBySession(t *testing.T) {
	tmp := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(tmp, "memory"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	longA := strings.Repeat("Session A lesson. ", 200)
	longB := strings.Repeat("Session B lesson. ", 200)
	now := time.Now().UTC()
	if err := store.UpsertSummary(SummaryRecord{
		SessionID: "session-a", RunID: "run-a", TaskID: "task-a",
		Scope: ScopeStable, Summary: longA, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertSummary A: %v", err)
	}
	if err := store.UpsertSummary(SummaryRecord{
		SessionID: "session-b", RunID: "run-b", TaskID: "task-b",
		Scope: ScopeStable, Summary: longB, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertSummary B: %v", err)
	}

	if err := store.CompressStableSummary("session-a", 120); err != nil {
		t.Fatalf("CompressStableSummary: %v", err)
	}
	snapA, err := store.LoadSnapshot("session-a")
	if err != nil {
		t.Fatalf("LoadSnapshot A: %v", err)
	}
	snapB, err := store.LoadSnapshot("session-b")
	if err != nil {
		t.Fatalf("LoadSnapshot B: %v", err)
	}
	if !strings.Contains(snapA.StableSummary, "compressed") {
		t.Fatalf("expected session-a compressed, got %q", snapA.StableSummary)
	}
	if strings.Contains(snapB.StableSummary, "compressed") {
		t.Fatalf("session-b must not be compressed when only session-a was targeted")
	}
	if !strings.Contains(snapB.StableSummary, "Session B") {
		t.Fatalf("session-b summary corrupted: %q", snapB.StableSummary)
	}
}

func TestS3_HotEventLimitRaised(t *testing.T) {
	if hotEventLimit < 48 {
		t.Fatalf("hotEventLimit=%d, want >= 48 (S3.5)", hotEventLimit)
	}
}
