package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerResolve_ReusesStableWorkspace(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), ".avatars", "tasks"))
	first, created, err := manager.Resolve(ResolveOptions{Title: "Analyze repository"})
	if err != nil {
		t.Fatalf("resolve first workspace failed: %v", err)
	}
	if !created {
		t.Fatal("expected first workspace to be created")
	}
	second, created, err := manager.Resolve(ResolveOptions{Title: "Analyze repository"})
	if err != nil {
		t.Fatalf("resolve second workspace failed: %v", err)
	}
	if created {
		t.Fatal("expected second workspace to be reused")
	}
	if first.ID != second.ID {
		t.Fatalf("expected stable workspace id reuse, got %q and %q", first.ID, second.ID)
	}
}

func TestManagerResolve_ForceNewCreatesFreshWorkspace(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), ".avatars", "tasks"))
	first, _, err := manager.Resolve(ResolveOptions{Title: "Analyze repository"})
	if err != nil {
		t.Fatalf("resolve first workspace failed: %v", err)
	}
	second, created, err := manager.Resolve(ResolveOptions{Title: "Analyze repository", ForceNew: true})
	if err != nil {
		t.Fatalf("resolve fresh workspace failed: %v", err)
	}
	if !created {
		t.Fatal("expected forced new workspace to be created")
	}
	if first.ID == second.ID {
		t.Fatalf("expected fresh workspace id, got reused %q", first.ID)
	}
}

func TestManagerLoadByTranscript_FindsOwningWorkspace(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), ".avatars", "tasks"))
	workspace, _, err := manager.Resolve(ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	transcriptPath := filepath.Join(workspace.SessionsDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript failed: %v", err)
	}
	workspace, err = manager.MarkRun(workspace, transcriptPath, "summary")
	if err != nil {
		t.Fatalf("mark run failed: %v", err)
	}
	loaded, ok, err := manager.LoadByTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("load by transcript failed: %v", err)
	}
	if !ok {
		t.Fatal("expected transcript to map back to a task workspace")
	}
	if loaded.ID != workspace.ID {
		t.Fatalf("expected workspace %q, got %q", workspace.ID, loaded.ID)
	}
	if loaded.LatestTranscriptPath() != transcriptPath {
		t.Fatalf("expected latest transcript path %q, got %q", transcriptPath, loaded.LatestTranscriptPath())
	}
}

func TestManagerList_ReturnsLatestWorkspaceFirst(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), ".avatars", "tasks"))
	older, _, err := manager.Resolve(ResolveOptions{ID: "older-task", Title: "Older task"})
	if err != nil {
		t.Fatalf("resolve older workspace failed: %v", err)
	}
	newer, _, err := manager.Resolve(ResolveOptions{ID: "newer-task", Title: "Newer task"})
	if err != nil {
		t.Fatalf("resolve newer workspace failed: %v", err)
	}
	older, err = manager.MarkRun(older, filepath.Join(older.SessionsDir, "older.jsonl"), "older summary")
	if err != nil {
		t.Fatalf("mark older run failed: %v", err)
	}
	newer, err = manager.MarkRun(newer, filepath.Join(newer.SessionsDir, "newer.jsonl"), "newer summary")
	if err != nil {
		t.Fatalf("mark newer run failed: %v", err)
	}

	workspaces, err := manager.List()
	if err != nil {
		t.Fatalf("list workspaces failed: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(workspaces))
	}
	if workspaces[0].ID != newer.ID {
		t.Fatalf("expected newest workspace first, got %q", workspaces[0].ID)
	}
}

func TestReconcileFromSessions_HealsNewWithTranscript(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), ".avatars", "tasks"))
	ws, _, err := manager.Resolve(ResolveOptions{ID: "orphan-task", Title: "Orphan"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate killed run: session on disk, manifest still new/run_count=0.
	sess := filepath.Join(ws.SessionsDir, "20260714-120000.jsonl")
	if err := os.WriteFile(sess, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	list, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	var found Workspace
	for _, w := range list {
		if w.ID == ws.ID {
			found = w
		}
	}
	if found.ID == "" {
		t.Fatal("task missing from list")
	}
	if found.RunCount < 1 {
		t.Fatalf("run_count=%d want >=1", found.RunCount)
	}
	if found.Status == "new" {
		t.Fatal("status should leave new after session reconcile")
	}
	if found.LatestTranscript == "" {
		t.Fatal("expected latest_transcript filled")
	}
}

func TestInferTaskRunStatus(t *testing.T) {
	if got := inferTaskRunStatus("Direct path: wrote x. build_ok=false"); got != "failed" {
		t.Fatalf("got %s", got)
	}
	if got := inferTaskRunStatus("Run status: completed_unverified"); got != "completed_unverified" {
		t.Fatalf("got %s", got)
	}
	if got := inferTaskRunStatus("all good"); got != "stable" {
		t.Fatalf("got %s", got)
	}
}

func TestManagerDelete_RemovesWorkspaceDirectory(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), ".avatars", "tasks"))
	workspace, _, err := manager.Resolve(ResolveOptions{ID: "delete-me", Title: "Delete me"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	if err := manager.Delete(workspace.ID); err != nil {
		t.Fatalf("delete workspace failed: %v", err)
	}
	if _, err := os.Stat(workspace.RootDir); !os.IsNotExist(err) {
		t.Fatalf("expected workspace directory to be removed, got err=%v", err)
	}
}
