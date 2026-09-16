package projectfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestS7B_ShouldSkipWalkDir(t *testing.T) {
	for _, name := range []string{".git", "node_modules", "avatars", "SceneIntruderMCP_for_test", "sheetforge_for_Test", "new_project_for_test", "claude_code_main"} {
		if !ShouldSkipWalkDir(name) {
			t.Fatalf("expected skip %q", name)
		}
	}
	if ShouldSkipWalkDir("internal") || ShouldSkipWalkDir("cmd") {
		t.Fatal("must not skip source dirs")
	}
}

func TestS7B_WalkDirCappedSkipsIgnored(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0755)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0644)
	var seen []string
	err := WalkDirCapped(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return err
		}
		seen = append(seen, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range seen {
		if name == "index.js" {
			t.Fatalf("walked into node_modules: %v", seen)
		}
	}
	if len(seen) != 1 || seen[0] != "main.go" {
		t.Fatalf("seen=%v", seen)
	}
}
