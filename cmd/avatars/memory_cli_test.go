package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	memstore "avatars/internal/memory"
)

func TestC51_MemoryMaintainDryRunExportsProcessRecord(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	docs := filepath.Join("docs", "workflow")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "# Process Record\n\n> Last export: 2026-07-10\n"
	if err := os.WriteFile(filepath.Join(docs, "process_record.md"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "workflow",
		RunID:     "workflow",
		TaskID:    "c5.1",
		Kind:      "task_completion",
		Verdict:   "PASS",
		Summary:   "C5.1 maintain export fixture",
		Source:    "test",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	_ = store.Close()

	output := captureRunOutput(t, []string{"memory", "maintain", "--dry-run"})
	if !strings.Contains(output, "Memory maintenance dry run: scope=project") {
		t.Fatalf("expected project dry-run, got %s", output)
	}
	if !strings.Contains(output, "Process record export: docs/workflow/process_record.md") {
		t.Fatalf("expected process_record export line, got %s", output)
	}

	body, err := os.ReadFile(filepath.Join(docs, "process_record.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	today := time.Now().UTC().Format("2006-01-02")
	if !strings.Contains(got, "> Last export: "+today) {
		t.Fatalf("expected Last export %s, got:\n%s", today, got)
	}
	if strings.Contains(got, "Last export: 2026-07-10") {
		t.Fatal("stale export date should be replaced")
	}
	if !strings.Contains(got, "C5.1 maintain export fixture") {
		t.Fatalf("expected sqlite evaluation in export:\n%s", got)
	}
}
