package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDedupeChangedFilesUnder_DropsEscapes(t *testing.T) {
	root := t.TempDir()
	got := DedupeChangedFilesUnder(root, []string{
		"../main.py",
		filepath.Join(root, "app", "main.py"),
		"/app/main.py",
		"app/main.py",
	})
	// ../ and /app/... (unix-root style when not under root) should be dropped;
	// abs-under-root and relative should collapse to one.
	found := false
	for _, p := range got {
		if p == "app/main.py" {
			found = true
		}
		if p == "../main.py" || p == "/app/main.py" {
			t.Fatalf("escape survived in footer: %#v", got)
		}
	}
	if !found {
		t.Fatalf("expected app/main.py, got %#v", got)
	}
}

func TestCollapseDuplicatePathSegments(t *testing.T) {
	if got := collapseDuplicatePathSegments("conftest/conftest.py"); got != "conftest.py" {
		t.Fatalf("got %q", got)
	}
	if got := collapseDuplicatePathSegments("models/models/user.py"); got != "models/user.py" {
		t.Fatalf("got %q", got)
	}
}

func TestRewriteBuilderToolPath_EscapedRelMapsInside(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "app"), 0755)
	task := "fastapi app under app/core and app/db with migrations/"
	got := rewriteBuilderToolPath(dir, task, "../main.py")
	slash := filepath.ToSlash(got)
	if slash != "app/main.py" && slash != filepath.ToSlash(filepath.Join(dir, "app", "main.py")) {
		t.Fatalf("escaped path should remap into app/, got %q", got)
	}
}

func TestCheckMissingDeclaredLayoutDirs(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0644)
	task := "layout: app/core, app/api, app/db, migrations/"
	stubs := checkMissingDeclaredLayoutDirs(dir, task, nil)
	paths := map[string]bool{}
	for _, s := range stubs {
		paths[s.Path] = true
	}
	if !paths["app/core/__init__.py"] || !paths["app/api/__init__.py"] || !paths["app/db/__init__.py"] {
		t.Fatalf("expected core/api/db stubs, got %#v", paths)
	}
}
