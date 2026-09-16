package runtime

import (
	"os"
	"strings"
	"testing"
)

func TestSanitizeBuilderPaths_WorkflowUnderPackage(t *testing.T) {
	files := []builderCodeFile{
		{Path: "image_processor/docs/workflow/phase1.md", Content: "# bad"},
		{Path: "docs/workflow/phase1.md", Content: "# ok"},
		{Path: "image_processor/processor.py", Content: "x"},
	}
	kept, _, dropped := sanitizeBuilderPaths(files)
	if len(dropped) != 1 {
		t.Fatalf("expected 1 dropped workflow clone, got %v kept=%v", dropped, kept)
	}
	if len(kept) != 2 {
		t.Fatalf("kept=%d want 2: %+v", len(kept), kept)
	}
}

func TestSanitizeBuilderPaths_NestedPackage(t *testing.T) {
	files := []builderCodeFile{
		{Path: "image_processor/image_processor/config.py", Content: "x"},
	}
	kept, redirected, _ := sanitizeBuilderPaths(files)
	if len(kept) != 1 || kept[0].Path != "image_processor/config.py" {
		t.Fatalf("got kept=%+v redirected=%v", kept, redirected)
	}
}

func TestSanitizeBuilderPaths_RootConfigInPackage(t *testing.T) {
	files := []builderCodeFile{
		{Path: "image_processor/requirements.txt", Content: "Pillow"},
		{Path: "image_processor/config.example.json", Content: "{}"},
	}
	kept, redirected, _ := sanitizeBuilderPaths(files)
	if len(kept) != 2 {
		t.Fatalf("kept=%+v", kept)
	}
	if kept[0].Path != "requirements.txt" || kept[1].Path != "config.example.json" {
		t.Fatalf("paths=%q %q redirected=%v", kept[0].Path, kept[1].Path, redirected)
	}
}

func TestSanitizeBuilderPaths_RootLibrarySources(t *testing.T) {
	files := []builderCodeFile{
		{Path: "storage.go", Content: "package storage"},
		{Path: "config_test.go", Content: "package config"},
		{Path: "phase1.md", Content: "# Phase 1"},
		{Path: "migrate.py", Content: "x"},
	}
	kept, redirected, _ := sanitizeBuilderPaths(files)
	if len(kept) != 4 {
		t.Fatalf("kept=%d want 4: %+v redirected=%v", len(kept), kept, redirected)
	}
	want := map[string]bool{
		"internal/storage/storage.go":   true,
		"internal/config/config_test.go": true,
		"docs/workflow/phase1.md":       true,
		"migrate/migrate.py":            true, // no pyproject → <stem>/
	}
	for _, f := range kept {
		if !want[f.Path] {
			t.Errorf("unexpected path %q (all=%v redirected=%v)", f.Path, kept, redirected)
		}
	}
}

func TestSanitizeWritePath_GoDocPrefersExistingLibrary(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/cronnext\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("cronnext", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("cronnext/parse.go", []byte("package cronnext\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, redirected := sanitizeWritePath("doc.go")
	if !redirected || got != "cronnext/doc.go" {
		t.Fatalf("got %q redirected=%v want cronnext/doc.go (F34)", got, redirected)
	}
}

func TestSanitizeWritePath_GoDocUsesModuleNameWhenNoLibYet(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/cronnext\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, redirected := sanitizeWritePath("doc.go")
	if !redirected || got != "cronnext/doc.go" {
		t.Fatalf("got %q redirected=%v want cronnext/doc.go (not internal/doc/)", got, redirected)
	}
}

func TestSanitizeWritePath_GoModuleNamedLibNotBuried(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/ratebucket\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, redirected := sanitizeWritePath("ratebucket.go")
	if got != "ratebucket.go" {
		t.Fatalf("F78: module-named lib stays at repo root; got %q redirected=%v", got, redirected)
	}
	gotTest, _ := sanitizeWritePath("ratebucket_test.go")
	if gotTest != "ratebucket_test.go" {
		t.Fatalf("F78: ratebucket_test.go stays at root; got %q", gotTest)
	}
	// Unrelated packages still follow the service default.
	gotCfg, _ := sanitizeWritePath("config.go")
	if gotCfg != "internal/config/config.go" {
		t.Fatalf("config.go may still lift under internal/; got %q", gotCfg)
	}
}

func TestSanitizeWritePath_GoEntrypointWithCmdServer(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("cmd/server", 0755); err != nil {
		t.Fatal(err)
	}
	got, redirected := sanitizeWritePath("main.go")
	if !redirected || got != "cmd/server/main.go" {
		t.Fatalf("got %q redirected=%v want cmd/server/main.go", got, redirected)
	}
}

func TestSanitizeBuilderPaths_MultiLangRootSources(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("Cargo.toml", []byte("[package]\nname=\"x\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("src", 0755); err != nil {
		t.Fatal(err)
	}
	files := []builderCodeFile{
		{Path: "auth.rs", Content: "pub fn x(){}"},
		{Path: "main.rs", Content: "fn main(){}"},
	}
	kept, redirected, _ := sanitizeBuilderPaths(files)
	if len(kept) != 2 {
		t.Fatalf("kept=%+v redirected=%v", kept, redirected)
	}
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if !paths["src/auth.rs"] {
		t.Fatalf("auth.rs should move under src/, got %+v", kept)
	}
	if !paths["src/main.rs"] {
		t.Fatalf("main.rs should move under src/ when src/ exists, got %+v", kept)
	}
}

func TestSanitizeBuilderPaths_PythonWithSrc(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("pyproject.toml", []byte("[project]\nname='x'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("src", 0755); err != nil {
		t.Fatal(err)
	}
	kept, _, _ := sanitizeBuilderPaths([]builderCodeFile{{Path: "storage.py", Content: "x"}})
	if len(kept) != 1 || kept[0].Path != "src/storage/storage.py" {
		t.Fatalf("got %+v want src/storage/storage.py", kept)
	}
}

func TestSanitizeBuilderPaths_DuplicateKeepsLonger(t *testing.T) {
	files := []builderCodeFile{
		{Path: "internal/storage/migrate.go", Content: "package storage\n// short"},
		{Path: "internal/storage/migrate.go", Content: "package storage\n\nimport (\n\t\"embed\"\n)\n\n//go:embed migrations/*.sql\nvar migrationFS embed.FS\n"},
	}
	kept, _, dropped := sanitizeBuilderPaths(files)
	if len(kept) != 1 {
		t.Fatalf("kept=%d want 1: %+v dropped=%v", len(kept), kept, dropped)
	}
	if !strings.Contains(kept[0].Content, "//go:embed") {
		t.Fatalf("expected longer content kept, got %q", kept[0].Content)
	}
	if len(dropped) != 1 || !strings.Contains(dropped[0], "duplicate merged") {
		t.Fatalf("dropped=%v", dropped)
	}
}

func TestSanitizeBuilderPaths_GoRootMigrationsRedirect(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/x\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	kept, redirected, _ := sanitizeBuilderPaths([]builderCodeFile{
		{Path: "migrations/001_users.sql", Content: "CREATE TABLE users;"},
	})
	if len(kept) != 1 || kept[0].Path != "internal/storage/migrations/001_users.sql" {
		t.Fatalf("kept=%+v redirected=%v", kept, redirected)
	}
}

func TestSanitizeBuilderPaths_DedupeMigrationVersions(t *testing.T) {
	rich := `CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  email TEXT NOT NULL UNIQUE
);
CREATE TABLE boards (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL
);
`
	kept, _, dropped := sanitizeBuilderPaths([]builderCodeFile{
		{Path: "internal/storage/migrations/001_create_tables.sql", Content: "-- no-op: schema already created\n"},
		{Path: "internal/storage/migrations/001_create_users.sql", Content: "CREATE TABLE users (id INTEGER PRIMARY KEY);\n"},
		{Path: "internal/storage/migrations/001_initial.sql", Content: rich},
		{Path: "internal/storage/migrations/002_indexes.sql", Content: "CREATE INDEX idx_users_email ON users(email);\n"},
	})
	if len(kept) != 2 {
		t.Fatalf("kept=%d want 2: %+v dropped=%v", len(kept), kept, dropped)
	}
	var got001 string
	for _, f := range kept {
		if strings.Contains(f.Path, "001_") {
			got001 = f.Path
			if !strings.Contains(f.Content, "CREATE TABLE boards") {
				t.Fatalf("expected richest 001 kept, got %q", f.Content)
			}
		}
	}
	if got001 != "internal/storage/migrations/001_initial.sql" {
		t.Fatalf("got001=%q", got001)
	}
	if len(dropped) < 2 {
		t.Fatalf("expected >=2 version drops, got %v", dropped)
	}
}

func TestPurgeMisplacedRootDuplicates(t *testing.T) {
	tmp := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	if err := os.WriteFile("go.mod", []byte("module example.com/blog\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("internal/storage", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("internal/storage/storage.go", []byte("package storage\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("storage.go", []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	deleted := purgeMisplacedRootDuplicates(tmp)
	if len(deleted) != 1 {
		t.Fatalf("deleted=%v want 1", deleted)
	}
	if _, err := os.Stat("storage.go"); !os.IsNotExist(err) {
		t.Fatal("root storage.go should be removed")
	}
	if _, err := os.Stat("internal/storage/storage.go"); err != nil {
		t.Fatal("correct path must remain")
	}
}

func TestCriticBuilderCycleCap(t *testing.T) {
	e := &Engine{}
	for i := 0; i < maxCriticBuilderCycles; i++ {
		if !e.beginCriticBuilderCycle("test") {
			t.Fatalf("cycle %d should succeed", i+1)
		}
	}
	if e.beginCriticBuilderCycle("test") {
		t.Fatal("should be capped")
	}
	if !e.criticBuilderCyclesExhausted() {
		t.Fatal("expected exhausted")
	}
}
