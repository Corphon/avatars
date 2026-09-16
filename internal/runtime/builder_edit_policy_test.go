package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLooksLikeModificationTask_GreenfieldNotMod(t *testing.T) {
	cases := []string{
		"从零搭建 Go HTTP 服务，创建 cmd/server/main.go",
		"bootstrap a new project with main.go and handlers",
		"空目录 scaffold Phase 1",
		"create a new Flask API with app.py",
	}
	for _, c := range cases {
		if looksLikeModificationTask(c) {
			t.Fatalf("greenfield classified as modification: %q", c)
		}
	}
}

func TestLooksLikeModificationTask_FixIsMod(t *testing.T) {
	if !looksLikeModificationTask("fix updated_at bug in repository.go") {
		t.Fatal("expected fix+file to be modification")
	}
}

func TestLooksLikeModificationTask_SoftAddNeedsExistingFile(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	// Soft "add" without existing file → not modification.
	if looksLikeModificationTask("add JWT middleware to handlers.go") {
		t.Fatal("soft add without on-disk file should not be modification")
	}
	if err := os.WriteFile("handlers.go", []byte("package handlers\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !looksLikeModificationTask("add JWT middleware to handlers.go") {
		t.Fatal("soft add with existing file should be modification")
	}
}

func TestHollowEntrypointCheck_PythonStub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("pass\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if reason := hollowEntrypointCheck(dir); reason == "" {
		t.Fatal("expected hollow main.py detection")
	}
}
