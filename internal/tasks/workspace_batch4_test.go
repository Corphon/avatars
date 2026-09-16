package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeTaskSummary_StripsControlAndCaps(t *testing.T) {
	long := strings.Repeat("a", 3000)
	got := sanitizeTaskSummary("line1\nline2\x00" + long)
	if strings.Contains(got, "\n") || strings.ContainsRune(got, 0) {
		t.Fatalf("control chars remain: %q", got)
	}
	if len([]rune(got)) > 2000 {
		t.Fatalf("summary not capped: %d runes", len([]rune(got)))
	}
}

func TestManagerList_SurfacesCorruptManifest(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".avatars", "tasks")
	manager := NewManager(base)
	id := "broken-task"
	root := filepath.Join(base, id)
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "task.json"), []byte(`{"id": "broken", "title": "oops`), 0644); err != nil {
		t.Fatal(err)
	}
	list, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list))
	}
	if list[0].Status != "corrupt" {
		t.Fatalf("expected corrupt status, got %q", list[0].Status)
	}
}

func TestManagerSave_AtomicRoundTrip(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), ".avatars", "tasks"))
	ws, _, err := manager.Resolve(ResolveOptions{ID: "atomic-task", Title: "Atomic"})
	if err != nil {
		t.Fatal(err)
	}
	ws, err = manager.MarkRunOutcome(ws, filepath.Join(ws.SessionsDir, "s.jsonl"), "hello\nworld\x01with quotes \"x\"", "stable")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ws.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("manifest not valid JSON: %v\n%s", err, raw)
	}
	summary, _ := decoded["latest_summary"].(string)
	if strings.Contains(summary, "\n") {
		t.Fatalf("summary still has newline: %q", summary)
	}
}
