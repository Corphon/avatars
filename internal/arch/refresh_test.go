package arch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshArchDocFromProject_ExternalDepsAndLayers(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module example.com/taskboard\n\ngo 1.22\n")
	mustWrite("cmd/server/main.go", `package main
import (
  "net/http"
  "modernc.org/sqlite"
)
func main() { _ = sqlite.DriverName; http.ListenAndServe(":8080", nil) }
`)
	mustWrite("internal/handlers/tasks.go", "package handlers\n")
	mustWrite("internal/database/db.go", "package database\n")

	doc := NewArchDoc("test", dir, nil)
	doc.Overview = "Phase 1 must implement JWT SQLite checklist you must create a build a full requirements: " + strings.Repeat("x", 200)
	issues := RefreshArchDocFromProject(dir, doc, RefreshOptions{
		PreserveOverview: true,
		ProjectHealthy:   true,
		HealthKnown:      true,
	})
	_ = issues

	if OverviewLooksLikePrompt(doc.Overview) {
		t.Fatalf("refresh should sanitize prompt overview, got: %q", doc.Overview)
	}
	if doc.Meta.Status != "draft" {
		t.Fatalf("expected draft without regpoints, got %s", doc.Meta.Status)
	}

	foundHTTP := false
	for _, ep := range doc.EntryPoints {
		if ep.Kind == "http-server" {
			foundHTTP = true
		}
	}
	if !foundHTTP {
		t.Fatalf("expected http-server entry kind, got %+v", doc.EntryPoints)
	}

	foundHandlers := false
	for _, layer := range doc.Layers {
		if strings.Contains(layer.Name, "handlers") && !strings.Contains(layer.Description, "Project directory") {
			foundHandlers = true
		}
	}
	if !foundHandlers {
		t.Fatalf("expected handlers layer with real description, got %+v", doc.Layers)
	}

	joined := strings.Join(doc.Dependencies.External, "\n")
	if !strings.Contains(joined, "modernc.org/sqlite") {
		t.Fatalf("expected modernc.org/sqlite in External deps, got %q", joined)
	}
	if strings.Contains(joined, "net/http") || strings.Contains(joined, "fmt") {
		t.Fatalf("stdlib should not appear in External deps: %q", joined)
	}
}

func TestIsGoStdlib(t *testing.T) {
	if !isGoStdlib("fmt") || !isGoStdlib("net/http") || !isGoStdlib("encoding/json") {
		t.Fatal("expected stdlib detection")
	}
	if isGoStdlib("modernc.org/sqlite") || isGoStdlib("github.com/foo/bar") {
		t.Fatal("third-party must not be stdlib")
	}
}

func TestInferEntryKind_HTTP(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cmd", "server", "main.go")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("package main\nfunc main() { http.ListenAndServe(\":8080\", nil) }\n"), 0o644)
	if got := InferEntryKind(dir, filepath.Join("cmd", "server", "main.go")); got != "http-server" {
		t.Fatalf("got %s", got)
	}
}

func TestFilterSelfLoops(t *testing.T) {
	edges := filterImportNoise([]ImportEdge{
		{From: "handlers", To: "handlers", Kind: "internal"},
		{From: "handlers", To: "task", Kind: "internal"},
	})
	if len(edges) != 1 || edges[0].To != "task" {
		t.Fatalf("got %+v", edges)
	}
}
