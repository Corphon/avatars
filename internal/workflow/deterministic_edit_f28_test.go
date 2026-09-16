package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractDeterministicEdits_GreenfieldLibraryNoProviderInject(t *testing.T) {
	// tidebucket-shaped task: mentions import package + add + .go, but is NOT
	// provider wiring — must not invent llm/providers blank imports (F28).
	task := `
Implement Phase 1: token bucket library.
Add injectable clock and table-driven tests in bucket.go.
Users import package example.com/tidebucket/internal/bucket.
Do not invent unexported helpers as blank imports.
`
	edits := ExtractDeterministicEdits(task)
	for _, e := range edits {
		if e.Operation != "add_go_import" {
			continue
		}
		t.Fatalf("greenfield library task must not emit add_go_import, got %+v", e)
	}
}

func TestExtractDeterministicEdits_ProviderWiringStillDetected(t *testing.T) {
	task := `Wire provider openai: add import for provider openai into app.go`
	edits := ExtractDeterministicEdits(task)
	found := false
	for _, e := range edits {
		if e.Operation == "add_go_import" {
			found = true
			if !strings.Contains(e.Params["import_path"], "<<provider:openai>>") &&
				!strings.Contains(e.Params["import_path"], "openai") {
				t.Fatalf("expected openai provider placeholder, got %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("provider wiring task must still emit add_go_import")
	}
}

func TestResolveProviderImportPath_NoInventFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/tidebucket\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := resolveProviderImportPath(dir, "unexported")
	if got != "" {
		t.Fatalf("missing provider package must not invent path, got %q", got)
	}
}

func TestResolveProviderImportPath_ExistingPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "internal", "llm", "providers", "openai")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "openai.go"), []byte("package openai\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := resolveProviderImportPath(dir, "openai")
	want := "example.com/app/internal/llm/providers/openai"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyAddGoImport_RefusesMissingHarnessPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/tidebucket\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "bucket.go")
	body := "package tidebucket\n\nimport (\n\t\"sync\"\n)\n\ntype Bucket struct{}\n"
	if err := os.WriteFile(src, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyDeterministicEdit(dir, DeterministicEdit{
		File:      "bucket.go",
		Operation: "add_go_import",
		Params:    map[string]string{"import_path": "<<provider:unexported>>"},
	})
	if applied {
		t.Fatal("must not apply invented provider import")
	}
	if err == nil {
		t.Fatal("expected error for missing provider package")
	}
	data, _ := os.ReadFile(src)
	if strings.Contains(string(data), "llm/providers") {
		t.Fatalf("poison import leaked onto disk:\n%s", data)
	}
}

func TestApplyAddGoImport_RefusesExplicitMissingPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/tidebucket\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "bucket.go")
	body := "package tidebucket\n\nimport (\n\t\"sync\"\n)\n\ntype Bucket struct{}\n"
	if err := os.WriteFile(src, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	poison := "example.com/tidebucket/internal/llm/providers/unexported"
	applied, err := ApplyDeterministicEdit(dir, DeterministicEdit{
		File:      "bucket.go",
		Operation: "add_go_import",
		Params:    map[string]string{"import_path": poison},
	})
	if applied || err == nil {
		t.Fatalf("must refuse missing harness path, applied=%v err=%v", applied, err)
	}
	data, _ := os.ReadFile(src)
	if strings.Contains(string(data), "llm/providers") {
		t.Fatalf("poison import leaked onto disk:\n%s", data)
	}
}

func TestExtractProviderName_RejectsUnexportedStopword(t *testing.T) {
	lower, _ := extractProviderNameFromTask("add unexported helpers to bucket.go")
	if lower != "" {
		t.Fatalf("stopword unexported must not become provider name, got %q", lower)
	}
}
