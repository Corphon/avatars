package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeBuilderFileContents_KeepsUnion(t *testing.T) {
	a := "from app import x\n\ndef a():\n    pass\n"
	b := "from app import x\nfrom app import y\n\ndef a():\n    pass\n\ndef b():\n    return 1\n"
	got := mergeBuilderFileContents(a, b)
	if !strings.Contains(got, "from app import y") || !strings.Contains(got, "def b()") {
		t.Fatalf("merge dropped unique lines:\n%s", got)
	}
}

func TestSanitizeBuilderPaths_MergesDuplicates(t *testing.T) {
	files := []builderCodeFile{
		{Path: "app/core/config.py", Content: "DEBUG=True\n"},
		{Path: "app/core/config.py", Content: "DEBUG=True\nSECRET=1\n"},
	}
	kept, _, dropped := sanitizeBuilderPaths(files)
	if len(kept) != 1 {
		t.Fatalf("kept=%v", kept)
	}
	if !strings.Contains(kept[0].Content, "SECRET=1") {
		t.Fatalf("content not merged: %q dropped=%v", kept[0].Content, dropped)
	}
}

func TestIsUniversalProjectConfigPath_EnvExample(t *testing.T) {
	if !isUniversalProjectConfigPath(".env.example") {
		t.Fatal(".env.example should be allowed")
	}
	if !isUniversalProjectConfigPath("configs/.env.sample") {
		t.Fatal(".env.sample should be allowed")
	}
}

func TestIsFuturePhaseModelPath(t *testing.T) {
	if !isFuturePhaseModelPath("app/models/tag.py") {
		t.Fatal("tag.py should be future-phase")
	}
	if isFuturePhaseModelPath("app/models/user.py") {
		t.Fatal("user.py should stay in phase1")
	}
}

func TestMigrationsLayoutPresent_RequiresAlembicVersions(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "alembic.ini"), []byte("[alembic]\n"), 0644)
	if migrationsLayoutPresent(dir) {
		t.Fatal("alembic.ini alone must not count")
	}
	_ = os.MkdirAll(filepath.Join(dir, "alembic", "versions"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "alembic", "versions", "001_init.py"), []byte("revision='001'\n"), 0644)
	if !migrationsLayoutPresent(dir) {
		t.Fatal("versions/*.py should count")
	}
}

func TestPreferredMigrationsStubPath_Alembic(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "alembic.ini"), []byte("[alembic]\n"), 0644)
	got := preferredMigrationsStubPath(dir, "alembic migrations")
	if got != "alembic/versions/001_init.py" {
		t.Fatalf("got %s", got)
	}
	content := defaultMigrationsStubContent(got, "alembic migrations")
	if !strings.Contains(content, "def upgrade") {
		t.Fatalf("expected alembic stub:\n%s", content)
	}
}

func TestPendingSatisfiesMigrations_IgnoresEnvPy(t *testing.T) {
	pending := []builderCodeFile{
		{Path: "migrations/env.py", Content: "from alembic import context\n"},
		{Path: "migrations/versions/.gitkeep", Content: ""},
	}
	if pendingSatisfiesMigrations(pending) {
		t.Fatal("env.py alone must not satisfy")
	}
	pending = append(pending, builderCodeFile{Path: "migrations/versions/001_init.py", Content: "revision='1'\n"})
	if !pendingSatisfiesMigrations(pending) {
		t.Fatal("versions/*.py should satisfy")
	}
}

func TestCheckMissingAPIPackage(t *testing.T) {
	dir := t.TempDir()
	task := "layout: app/main.py, app/api/, app/db/, migrations/"
	if got := checkMissingAPIPackage(dir, task, nil); got != "app/api/__init__.py" {
		t.Fatalf("got %q", got)
	}
	_ = os.MkdirAll(filepath.Join(dir, "app", "api"), 0755)
	if got := checkMissingAPIPackage(dir, task, nil); got != "" {
		t.Fatalf("expected empty after mkdir, got %q", got)
	}
}

func TestCollectBuilderFinalizeGaps(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("fastapi\n"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "migrations", "versions"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "migrations", "alembic.ini"), []byte("[alembic]\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "migrations", "versions", ".gitkeep"), []byte(""), 0644)
	task := "app/api/ migrations/ sqlalchemy"
	gaps := collectBuilderFinalizeGaps(dir, task, nil)
	paths := map[string]bool{}
	for _, g := range gaps {
		paths[g.Path] = true
	}
	if !paths["migrations/versions/001_init.py"] {
		t.Fatalf("expected alembic revision stub, got %+v", gaps)
	}
	if !paths["app/api/__init__.py"] {
		t.Fatalf("expected api stub, got %+v", gaps)
	}
}

func TestHealthPayloadLooksOK_AcceptsHealthy(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "app"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "app", "main.py"), []byte(`
@app.get("/health")
def health():
    return {"status": "healthy"}
`), 0644)
	if !healthPayloadLooksOK(dir) {
		t.Fatal("healthy should count as ok-ish")
	}
}

func TestSanitizeBuilderPaths_BlocksRootGenScripts(t *testing.T) {
	files := []builderCodeFile{
		{Path: "_gen_phase1.py", Content: "print(1)"},
		{Path: "app/main.py", Content: "x=1"},
		{Path: "_verify_app.py", Content: "print(2)"},
	}
	kept, _, dropped := sanitizeBuilderPaths(files)
	if len(kept) != 1 || kept[0].Path != "app/main.py" {
		t.Fatalf("kept=%v", kept)
	}
	if len(dropped) < 2 {
		t.Fatalf("dropped=%v", dropped)
	}
}

func TestPurgeRootGenScripts(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "_gen_phase1.py"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "app_main.py"), []byte("y"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "app"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "app", "main.py"), []byte("z"), 0644)
	deleted := purgeRootGenScripts(dir)
	if len(deleted) != 1 || deleted[0] != "_gen_phase1.py" {
		t.Fatalf("deleted=%v", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "_gen_phase1.py")); !os.IsNotExist(err) {
		t.Fatal("gen script still present")
	}
}

func TestDefaultMigrationsStubSQL(t *testing.T) {
	sql := defaultMigrationsStubSQL("workspace document user jwt sqlite migrations/")
	for _, needle := range []string{"users", "workspaces", "documents", "schema_migrations"} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("missing %s in:\n%s", needle, sql)
		}
	}
}

func TestDiskHasAppSources(t *testing.T) {
	dir := t.TempDir()
	if diskHasAppSources(dir) {
		t.Fatal("empty should be false")
	}
	_ = os.MkdirAll(filepath.Join(dir, "app"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "app", "main.py"), []byte("x"), 0644)
	if !diskHasAppSources(dir) {
		t.Fatal("expected true")
	}
}
