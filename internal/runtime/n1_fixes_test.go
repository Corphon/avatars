package runtime

import (
	"strings"
	"testing"
)

func TestIsDependencyOrRuntimeFilename(t *testing.T) {
	for _, name := range []string{"sql.js", "Node.js", "express.js", "package.js", "test.js", "tokio.rs", "src/types/sql.js", "src/sql/sql.js"} {
		if !isDependencyOrRuntimeFilename(name) {
			t.Fatalf("expected dependency/runtime filename: %s", name)
		}
	}
	for _, name := range []string{"src/index.js", "app.js", "server.js", "notes.js", "src/routes/notes.ts"} {
		if isDependencyOrRuntimeFilename(name) {
			t.Fatalf("should allow project path: %s", name)
		}
	}
}

func TestExtractTaskFilePaths_SkipsNpmPackageNames(t *testing.T) {
	input := "Use Node.js with sql.js and write src/index.js plus src/routes/notes.js"
	paths := extractTaskFilePaths(input)
	joined := strings.Join(paths, ",")
	if strings.Contains(joined, "sql.js") && !strings.Contains(joined, "src/") {
		// bare sql.js must not appear
	}
	for _, p := range paths {
		if isDependencyOrRuntimeFilename(p) {
			t.Fatalf("extractTaskFilePaths returned dependency name %q in %#v", p, paths)
		}
	}
	foundIndex := false
	for _, p := range paths {
		if strings.Contains(p, "index.js") || strings.Contains(p, "notes.js") {
			foundIndex = true
		}
	}
	if !foundIndex {
		t.Fatalf("expected real project paths, got %#v", paths)
	}
}
