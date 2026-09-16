package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_LiteralLongTaskID(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".avatars", "tasks")
	manager := NewManager(base)
	longID := "builder-avatar-phase-1-gomod-cmdservermaingo-md-2961f1fa" // >48 chars
	if len(longID) <= 48 {
		t.Fatalf("fixture id should exceed sanitize truncate length")
	}
	root := filepath.Join(base, longID)
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ws := Workspace{
		ID:        longID,
		Title:     "long id task",
		Status:    "stable",
		RunCount:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	raw, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "task.json"), append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := manager.Load(longID)
	if err != nil {
		t.Fatalf("Load long id: %v", err)
	}
	if loaded.ID != longID {
		t.Fatalf("id=%q want %q", loaded.ID, longID)
	}

	list, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List len=%d want 1 (sanitize must not drop long ids)", len(list))
	}
	if list[0].ID != longID {
		t.Fatalf("list id=%q", list[0].ID)
	}
}
