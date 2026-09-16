package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/workflow"
)

func TestDedupeChangedFilesUnder_AbsAndRel(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "handlers", "health.go")
	got := DedupeChangedFilesUnder(root, []string{abs, "handlers/health.go", `.\handlers\health.go`})
	if len(got) != 1 || got[0] != "handlers/health.go" {
		t.Fatalf("want single relativized path, got %#v", got)
	}
}

func TestBuildRunFooter_DocsOnlyNote(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(wf, "phase2.md"), []byte("# Phase 2\n"), 0644)
	footer := BuildRunFooter(root, []string{"docs/workflow/phase2.md"}, NextStepInput{Status: "completed"})
	joined := strings.Join(footer.NextSteps, "\n")
	if !strings.Contains(joined, "only updated workflow docs") {
		t.Fatalf("expected docs-only honesty note, got %v", footer.NextSteps)
	}
}

func TestPreferredMigrationsStubPath_GoTopLevel(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0644)
	got := preferredMigrationsStubPath(dir, "")
	if got != "migrations/001_init.sql" {
		t.Fatalf("got %s want migrations/001_init.sql", got)
	}
}

func TestSanitizeBuilderPaths_GoTopLevelMigrations(t *testing.T) {
	task := "golang HTTP with cmd/server, internal/handlers, migrations"
	kept, redirected, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "internal/storage/migrations/001_init.sql", Content: "CREATE TABLE t(id INT);\n"},
		{Path: "handlers/api.go", Content: "package handlers\n"},
		{Path: "main.go", Content: "package main\nfunc main() {}\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if !paths["migrations/001_init.sql"] {
		t.Fatalf("migrations should lift to top-level; kept=%v redirected=%v", paths, redirected)
	}
	if !paths["internal/handlers/api.go"] {
		t.Fatalf("handlers should lift under internal/; kept=%v", paths)
	}
	if !paths["cmd/server/main.go"] {
		t.Fatalf("main.go → cmd/server; kept=%v", paths)
	}
}

func TestRewriteBuilderToolPath_GoLayout(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0644)
	task := "Go service: cmd/server, internal/handlers, migrations"
	got := rewriteBuilderToolPath(dir, task, "handlers/health.go")
	if filepath.ToSlash(got) != "internal/handlers/health.go" {
		t.Fatalf("got %q", got)
	}
	gotAbs := rewriteBuilderToolPath(dir, task, filepath.Join(dir, "main.go"))
	wantAbs := filepath.Join(dir, "cmd", "server", "main.go")
	if gotAbs != wantAbs {
		t.Fatalf("abs rewrite got %q want %q", gotAbs, wantAbs)
	}
}

func TestRewriteBuilderToolPath_PublicLibDoesNotBury(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/ratebucket\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	task := "做一个 Go 开源库 ratebucket，只要库和测试，不要做成带命令行的小工具"
	got := rewriteBuilderToolPath(dir, task, "ratebucket.go")
	if filepath.ToSlash(got) != "ratebucket.go" {
		t.Fatalf("F78 first write must stay at root, not internal/ or <mod>/; got %q", got)
	}
	gotTest := rewriteBuilderToolPath(dir, task, "ratebucket_test.go")
	if filepath.ToSlash(gotTest) != "ratebucket_test.go" {
		t.Fatalf("F78 test file stays at root: got %q", gotTest)
	}
	// LLM already buried the public module — unbury to root before write.
	gotBuried := rewriteBuilderToolPath(dir, task, "internal/ratebucket/ratebucket.go")
	if filepath.ToSlash(gotBuried) != "ratebucket.go" {
		t.Fatalf("F78 unbury internal/<mod>/ to root; got %q", gotBuried)
	}
	// A leftover internal/<mod> dir must not lock later writes into internal/.
	if err := os.MkdirAll(filepath.Join(dir, "internal", "ratebucket"), 0755); err != nil {
		t.Fatal(err)
	}
	gotAfterShell := rewriteBuilderToolPath(dir, task, "ratebucket.go")
	if filepath.ToSlash(gotAfterShell) != "ratebucket.go" {
		t.Fatalf("F78 leftover internal/<mod> must not lock-in; got %q", gotAfterShell)
	}
	// rewrite must be idempotent (no lift↔collapse ping-pong).
	again := rewriteBuilderToolPath(dir, task, gotAfterShell)
	if filepath.ToSlash(again) != "ratebucket.go" {
		t.Fatalf("F78 rewrite not idempotent: %q → %q", gotAfterShell, again)
	}
}

func TestRewriteBuilderToolPath_PublicLibKeepsPrivateInternal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/jobpq\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobpq.go"), []byte("package jobpq\n"), 0644); err != nil {
		t.Fatal(err)
	}
	task := "纯库优先 Go library jobpq, tests only, no CLI"
	gotHelper := rewriteBuilderToolPath(dir, task, "internal/heap/heap.go")
	if filepath.ToSlash(gotHelper) != "internal/heap/heap.go" {
		t.Fatalf("genuine private helper must stay; got %q", gotHelper)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "heap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "heap", "heap.go"), []byte("package heap\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gotAfterHelper := rewriteBuilderToolPath(dir, task, "jobpq.go")
	if filepath.ToSlash(gotAfterHelper) != "jobpq.go" {
		t.Fatalf("private helper tree must not lock public writes into internal/; got %q", gotAfterHelper)
	}
	lifted := rewriteBuilderToolPathWithContent(dir, task, "internal/concurrency/concurrency_test.go",
		"package jobpq\n\nimport \"testing\"\nfunc TestX(t *testing.T) {}\n")
	if filepath.ToSlash(lifted) != "concurrency_test.go" {
		t.Fatalf("package jobpq under internal/concurrency/ must lift to root; got %q", lifted)
	}
	kept := rewriteBuilderToolPathWithContent(dir, task, "internal/heap/heap.go", "package heap\nfunc Push() {}\n")
	if filepath.ToSlash(kept) != "internal/heap/heap.go" {
		t.Fatalf("package heap helper must stay; got %q", kept)
	}
}

func TestRewriteBuilderToolPath_PythonPrivateInternalUnbury(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"ttlcache\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	task := "做一个很小的开源库 ttlcache，只要库和测试"
	got := rewriteBuilderToolPath(dir, task, "_internal/ttlcache/ttlcache.py")
	if filepath.ToSlash(got) != "ttlcache.py" {
		t.Fatalf("public python package under _internal/<name>/ must unbury; got %q", got)
	}
	kept := rewriteBuilderToolPath(dir, task, "_internal/helpers/util.py")
	if filepath.ToSlash(kept) != "_internal/helpers/util.py" {
		t.Fatalf("genuine private helper must stay; got %q", kept)
	}
}

func TestCheckPhaseScopedRequirementGaps_ListAuth(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 2
> **Phase Count**: 2
### Phase 1: Core
- **Status**: completed
### Phase 2: Auth Middleware and Delete
- **Status**: in-progress
- **Goal**: Add API key auth and DELETE
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	prompt := "写接口与列表需鉴权；错误返回 401"
	code := `
package main
func route() {
  handlers.APIKeyMiddleware(h.CreateSnippet)(w, r)
  h.ListSnippets(w, r)
}
`
	missing := checkPhaseScopedRequirementGaps(dir, prompt, code)
	joined := strings.Join(missing, ",")
	if !strings.Contains(joined, "list endpoint API auth") {
		t.Fatalf("expected list auth gap, got %v", missing)
	}
	fixed := strings.Replace(code, "h.ListSnippets(w, r)", "handlers.APIKeyMiddleware(h.ListSnippets)(w, r)", 1)
	if gaps := checkPhaseScopedRequirementGaps(dir, prompt, fixed); len(gaps) != 0 {
		for _, g := range gaps {
			if g == "list endpoint API auth" {
				t.Fatalf("list auth still reported: %v", gaps)
			}
		}
	}
}

func TestCheckPhaseScopedRequirementGaps_FastAPIDepends(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 1
### Phase 1: Auth
- **Status**: in-progress
- **Goal**: API key auth for list endpoints
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	prompt := "list endpoints require API key auth"
	code := `
from fastapi import Depends, FastAPI
app = FastAPI()
@app.get("/items")
def list_items(user=Depends(get_api_key)):
    return []
`
	if gaps := checkPhaseScopedRequirementGaps(dir, prompt, code); len(gaps) != 0 {
		t.Fatalf("FastAPI Depends should satisfy list auth, got %v", gaps)
	}
}

func TestRewriteBuilderToolPath_PythonAppLayout(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "app"), 0755)
	task := "fastapi app under app/core and app/db with migrations/"
	got := rewriteBuilderToolPath(dir, task, "models/user.py")
	if filepath.ToSlash(got) != "app/models/user.py" {
		t.Fatalf("got %q want app/models/user.py", got)
	}
}

func TestRemapNestedMigrations_AppAndSrc(t *testing.T) {
	got, ok := remapNestedMigrationsToTopLevel("app/db/migrations/001_init.sql")
	if !ok || got != "migrations/001_init.sql" {
		t.Fatalf("app nested: got %q ok=%v", got, ok)
	}
	got, ok = remapNestedMigrationsToTopLevel("src/migrations/001_init.sql")
	if !ok || got != "migrations/001_init.sql" {
		t.Fatalf("src nested: got %q ok=%v", got, ok)
	}
	got, ok = remapNestedMigrationsToTopLevel("migrations/001_init.sql")
	if ok {
		t.Fatalf("top-level should not remap: %q", got)
	}
}

func TestUpdatePlanStatus_SyncsPhaseStatuses(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Status**: in-progress
> **Active Phase**: 2
> **Phase Count**: 2

### Phase 1: Core
- **Status**: completed
### Phase 2: Auth
- **Status**: in-progress
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	if err := workflow.UpdatePlanStatus(dir, "completed"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	body := string(out)
	if !strings.Contains(body, "> **Status**: completed") {
		t.Fatalf("header not completed:\n%s", body)
	}
	if strings.Contains(body, "- **Status**: in-progress") {
		t.Fatalf("phase status still in-progress:\n%s", body)
	}
	if !strings.Contains(body, "### Phase 2: Auth\n- **Status**: completed") &&
		!strings.Contains(body, "### Phase 2: Auth\r\n- **Status**: completed") {
		// SetPhaseStatus replaces within the phase block; accept any completed under Phase 2.
		meta := workflow.ParsePlanMeta(body)
		for _, ph := range meta.Phases {
			if ph.Number == 2 && ph.Status != "completed" {
				t.Fatalf("phase 2 status=%q body:\n%s", ph.Status, body)
			}
		}
	}
}
