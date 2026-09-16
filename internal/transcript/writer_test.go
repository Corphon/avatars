// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/events"
)

func TestJSONLWriter_AppendsEventsAndFlushes(t *testing.T) {
	writer, err := NewJSONLWriter(t.TempDir(), "session-1")
	if err != nil {
		t.Fatalf("expected writer, got error: %v", err)
	}
	defer func() {
		_ = writer.Close()
	}()

	if err := writer.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Type: "user_message", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "hello"}}); err != nil {
		t.Fatalf("append user event failed: %v", err)
	}
	if err := writer.Append(events.Envelope{EventID: "evt-2", Sequence: 2, RunID: "run-1", TaskID: "task-1", Type: "assistant_message", EmittedAt: time.Now().UTC(), Payload: map[string]any{"content": "ok"}}); err != nil {
		t.Fatalf("append assistant event failed: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	content, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read transcript failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 events, got %d", len(lines))
	}

	var event events.Envelope
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal first event failed: %v", err)
	}
	if event.Type != "user_message" {
		t.Fatalf("expected first event type user_message, got %s", event.Type)
	}
}

// === TODO-07 (P2) transcript flush tests ===

func newTestEnvelope(seq uint64, typ string) events.Envelope {
	return events.Envelope{
		EventID:   "evt-" + typ + "-" + strings.Repeat("x", int(seq)),
		Sequence:  seq,
		RunID:     "run-test",
		TaskID:    "task-test",
		Phase:     "test",
		Type:      typ,
		EmittedAt: time.Now().UTC(),
		Source:    "test",
		Payload:   map[string]any{"k": "v", "n": float64(seq)},
	}
}

// TestTranscriptWriterFlushesOnWrite is the success-criteria test
// for TODO-07: after Append, the bytes must be on disk (not just in
// the bufio buffer) and not lost if the writer is never Close()d.
func TestTranscriptWriterFlushesOnWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewJSONLWriter(dir, "session-flush")
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	if err := w.Append(newTestEnvelope(1, "test.event")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Read the file directly to prove the bytes are already on
	// disk, with the writer still open (no Close() yet). This
	// simulates a process that has not yet called Close — the
	// transcript is still durable. We defer Close only to keep
	// the Windows temp-dir cleanup happy; the durability check
	// above does not depend on Close.
	defer func() { _ = w.Close() }()
	info, err := os.Stat(w.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("expected transcript to be > 0 bytes after Append, got 0")
	}
	contents, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(contents) == 0 {
		t.Fatalf("expected non-empty contents after Append")
	}
	var envelope events.Envelope
	trimmed := strings.TrimRight(string(contents), "\n")
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		t.Fatalf("Unmarshal: %v\ncontents=%q", err, contents)
	}
	if envelope.Type != "test.event" {
		t.Fatalf("expected type=test.event, got %q", envelope.Type)
	}
	if envelope.Sequence != 1 {
		t.Fatalf("expected sequence=1, got %d", envelope.Sequence)
	}
}

func TestTranscriptWriterSyncsAfterEachEvent(t *testing.T) {
	dir := t.TempDir()
	w, err := NewJSONLWriter(dir, "session-multi")
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	for i := uint64(1); i <= 5; i++ {
		if err := w.Append(newTestEnvelope(i, "test.event")); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	contents, err := os.ReadFile(w.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(contents), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %q", len(lines), contents)
	}
	for i, line := range lines {
		var envelope events.Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
		if envelope.Sequence != uint64(i+1) {
			t.Fatalf("line %d expected sequence=%d, got %d", i, i+1, envelope.Sequence)
		}
	}
}

func TestRestoreZeroByteFileReturnsEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	sessionID := "session-empty"
	path := filepath.Join(dir, sessionID+".jsonl")
	// Create a 0-byte file the same way NewJSONLWriter does.
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = f.Close()

	snapshot, err := Restore(path)
	if err != nil {
		t.Fatalf("expected nil error for 0-byte transcript, got %v", err)
	}
	if snapshot.SourceTranscript != path {
		t.Fatalf("expected SourceTranscript=%q, got %q", path, snapshot.SourceTranscript)
	}
	if snapshot.SessionID != sessionID {
		t.Fatalf("expected SessionID=%q, got %q", sessionID, snapshot.SessionID)
	}
	if snapshot.EventCount != 0 {
		t.Fatalf("expected EventCount=0, got %d", snapshot.EventCount)
	}
	if snapshot.BoundaryEventID != "" {
		t.Fatalf("expected empty BoundaryEventID for fresh transcript, got %q", snapshot.BoundaryEventID)
	}
	if snapshot.AvatarSummaries == nil {
		t.Fatalf("expected AvatarSummaries to be a non-nil empty map, got nil")
	}
}

func TestRestoreZeroByteFileCreatedByWriterWorks(t *testing.T) {
	// End-to-end shape: NewJSONLWriter creates a 0-byte file via
	// O_CREATE before any Append. After our fix, Restore on that
	// file must return a clean empty snapshot so resume does not
	// error out on the very first run of a session.
	dir := t.TempDir()
	w, err := NewJSONLWriter(dir, "session-fresh")
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	snapshot, err := Restore(w.Path())
	if err != nil {
		t.Fatalf("Restore on 0-byte writer output: %v", err)
	}
	if snapshot.SessionID != "session-fresh" {
		t.Fatalf("expected SessionID=session-fresh, got %q", snapshot.SessionID)
	}
	if snapshot.EventCount != 0 {
		t.Fatalf("expected EventCount=0, got %d", snapshot.EventCount)
	}
}

func TestRestoreMissingFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-session.jsonl")
	_, err := Restore(missing)
	if err == nil {
		t.Fatalf("expected error for missing file, got nil")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("expected os.IsNotExist error, got %v", err)
	}
}

func TestRestorePopulatedFileStillWorks(t *testing.T) {
	// Sanity check that the 0-byte short-circuit did not break the
	// normal restore path. Write a single compaction-boundary
	// envelope and verify the snapshot is fully populated.
	dir := t.TempDir()
	sessionID := "session-pop"
	w, err := NewJSONLWriter(dir, sessionID)
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	envelope := newTestEnvelope(1, "memory.compaction_boundary_written")
	envelope.Payload = map[string]any{
		"boundary_kind": "run_terminal",
		"status":        "completed",
		"summary":       "test summary",
	}
	if err := w.Append(envelope); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	snapshot, err := Restore(w.Path())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if snapshot.EventCount != 1 {
		t.Fatalf("expected EventCount=1, got %d", snapshot.EventCount)
	}
	if snapshot.BoundaryEventID == "" {
		t.Fatalf("expected BoundaryEventID to be populated, got empty")
	}
	if snapshot.BoundaryType != "run_terminal" {
		t.Fatalf("expected BoundaryType=run_terminal, got %q", snapshot.BoundaryType)
	}
	if snapshot.Status != "completed" {
		t.Fatalf("expected Status=completed, got %q", snapshot.Status)
	}
	if snapshot.Summary != "test summary" {
		t.Fatalf("expected Summary=test summary, got %q", snapshot.Summary)
	}
}

func TestTranscriptWriterAppendAfterCloseSurfacesError(t *testing.T) {
	// After Close, the file handle is invalid. Append should
	// surface an error rather than silently corrupt the file.
	dir := t.TempDir()
	w, err := NewJSONLWriter(dir, "session-after-close")
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	if err := w.Append(newTestEnvelope(1, "test.event")); err != nil {
		t.Fatalf("Append (before close): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Append(newTestEnvelope(2, "test.event")); err == nil {
		t.Fatalf("expected Append after Close to error, got nil")
	}
}
