package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBuilderTargetLanguage_IgnoresLayoutRustHint(t *testing.T) {
	// H1: phase implement boilerplate used to list "JS/TS/Rust" and lock rust.
	boilerplate := formatConfirmImplementInput(1, 0)
	if strings.Contains(strings.ToLower(boilerplate), "rust") {
		t.Fatalf("implement boilerplate must not name rust: %s", boilerplate)
	}
	orig := "Build a Go HTTP taskboard with go module example.com/taskboard and SQLite"
	got := resolveBuilderTargetLanguage(boilerplate, orig)
	if got != "go" {
		t.Fatalf("resolveBuilderTargetLanguage=%q want go (boilerplate=%q)", got, boilerplate)
	}
	// Legacy poisoned string still present in older sessions / skills.
	poisoned := "Write real source files using the layout (app/ for Python, internal/+cmd/ for Go, src/ for JS/TS/Rust)"
	if detectTargetLanguage(poisoned) == "rust" {
		t.Fatal("detectTargetLanguage must ignore JS/TS/Rust layout noise")
	}
	if resolveBuilderTargetLanguage(poisoned, orig) != "go" {
		t.Fatalf("resolve with poisoned taskInput should prefer orig go, got %q", resolveBuilderTargetLanguage(poisoned, orig))
	}
}

func TestSanitizeBuilderPaths_AppLayoutRemap(t *testing.T) {
	task := "python fastapi layout: app/core/, app/db/, app/models/, app/api/"
	kept, redirected, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "core/config.py", Content: "DEBUG=True\n"},
		{Path: "db/session.py", Content: "engine=None\n"},
		{Path: "models.py", Content: "class User: pass\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	for _, want := range []string{"app/core/config.py", "app/db/session.py", "app/models.py"} {
		if !paths[want] {
			t.Fatalf("missing %s; kept=%v redirected=%v", want, paths, redirected)
		}
	}
}

func TestSanitizeBuilderPaths_GoInternalRemap(t *testing.T) {
	task := "golang service with internal/config and cmd/server"
	kept, redirected, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "config.go", Content: "package config\n\nvar X=1\n"},
		{Path: "handler/user.go", Content: "package handler\n\nfunc U() {}\n"},
		{Path: "main.go", Content: "package main\n\nfunc main() {}\n"},
		{Path: "cmd/server/main.go", Content: "package main\n\nfunc main() {}\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if !paths["internal/config/config.go"] {
		t.Fatalf("config.go should land under internal/; kept=%v redirected=%v", paths, redirected)
	}
	if !paths["internal/handler/user.go"] {
		t.Fatalf("handler/ should lift under internal/; kept=%v", paths)
	}
	if !paths["cmd/server/main.go"] {
		t.Fatalf("cmd/server must stay; kept=%v", paths)
	}
	// Root main.go → cmd/server/main.go (may merge with existing)
	if !paths["cmd/server/main.go"] {
		t.Fatalf("entrypoint should be under cmd/server; kept=%v redirected=%v", paths, redirected)
	}
}

func TestSanitizeBuilderPaths_JSSrcRemap(t *testing.T) {
	task := "typescript node app under src/components and src/lib"
	kept, redirected, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "components/Button.tsx", Content: "export const Button = () => null\n"},
		{Path: "index.ts", Content: "export * from './lib'\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if !paths["src/components/Button.tsx"] {
		t.Fatalf("components should lift under src/; kept=%v redirected=%v", paths, redirected)
	}
	if !paths["src/index.ts"] {
		t.Fatalf("index.ts should land under src/; kept=%v", paths)
	}
}

func TestSanitizeBuilderPaths_RustSrcRemap(t *testing.T) {
	task := "rust cargo project with src/lib.rs"
	kept, redirected, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "lib.rs", Content: "pub fn answer() -> i32 { 42 }\n"},
		{Path: "util.rs", Content: "pub fn n() -> i32 { 1 }\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if !paths["src/lib.rs"] {
		t.Fatalf("lib.rs → src/lib.rs; kept=%v redirected=%v", paths, redirected)
	}
	if !paths["src/util.rs"] {
		t.Fatalf("util.rs → src/util.rs; kept=%v", paths)
	}
}

func TestSanitizeBuilderPaths_GoDoesNotForceApp(t *testing.T) {
	task := "golang HTTP API with handlers"
	kept, _, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "handlers/api.go", Content: "package handlers\n\nfunc H() {}\n"},
	}, task)
	if len(kept) != 1 || kept[0].Path == "app/handlers/api.go" {
		t.Fatalf("Go must not remap into app/; got %+v", kept)
	}
	if kept[0].Path != "internal/handlers/api.go" {
		t.Fatalf("want internal/handlers/api.go got %s", kept[0].Path)
	}
}

func TestSanitizeBuilderPaths_GoPublicLibNotBuried(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/bloomx\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("bloomx", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("bloomx/bloomx.go", []byte("package bloomx\n\nfunc New() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("docs/workflow", 0755); err != nil {
		t.Fatal(err)
	}
	phase := "纯库优先\n包路径建议：bloomx\n禁止 internal/\n"
	if err := os.WriteFile("docs/workflow/phase1.md", []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}
	task := "golang bloomx pure library 纯库优先 禁止 internal/ 顶层 bloomx/"
	kept, redirected, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "bloomx/bloomx_test.go", Content: "package bloomx\n\nfunc TestNew(t *testing.T) {}\n"},
		{Path: "doc.go", Content: "// Package bloomx docs.\npackage bloomx\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if paths["internal/bloomx/bloomx_test.go"] {
		t.Fatalf("F52: must not bury bloomx under internal/; redirected=%v kept=%v", redirected, paths)
	}
	if !paths["bloomx/bloomx_test.go"] {
		t.Fatalf("want bloomx/bloomx_test.go; kept=%v redirected=%v", paths, redirected)
	}
	if paths["doc.go"] || paths["internal/doc/doc.go"] {
		t.Fatalf("bare doc.go should land under bloomx/; kept=%v", paths)
	}
	if !paths["bloomx/doc.go"] {
		t.Fatalf("want bloomx/doc.go; kept=%v redirected=%v", paths, redirected)
	}
}

func TestSanitizeBuilderPaths_GoPublicLibUnburyWithoutPhaseWording(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/ratebucket\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	task := "做一个 Go 开源库 ratebucket，只要库和测试，不要做成带命令行的小工具"
	kept, redirected, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "internal/ratebucket/ratebucket.go", Content: "package ratebucket\n\nfunc Allow() bool { return true }\n"},
		{Path: "internal/ratebucket/ratebucket_test.go", Content: "package ratebucket\n\nfunc TestAllow(t *testing.T) {}\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if paths["internal/ratebucket/ratebucket.go"] || paths["internal/ratebucket/ratebucket_test.go"] {
		t.Fatalf("F70: must unbury public lib; redirected=%v kept=%v", redirected, paths)
	}
	if !paths["ratebucket.go"] || !paths["ratebucket_test.go"] {
		t.Fatalf("F78: want root ratebucket.go; kept=%v redirected=%v", paths, redirected)
	}
	if paths["ratebucket/ratebucket.go"] || paths["ratebucket/ratebucket_test.go"] {
		t.Fatalf("F78: must not invent <mod>/ tree; kept=%v redirected=%v", paths, redirected)
	}
}

func TestWantsPublicLibraryLayout_OpenSourceLibNL(t *testing.T) {
	lower := strings.ToLower("做一个 Go 开源库 ratebucket，只要库和测试，不要做成带命令行的小工具")
	if !wantsPublicLibraryLayout(lower) {
		t.Fatal("开源库 / 只要库和测试 must count as public-library layout")
	}
	if !forbidsCLIScaffold(lower) {
		t.Fatal("不要做成带命令行的小工具 must forbid CLI scaffold")
	}
}

func TestRewriteBuilderToolPath_PythonAndJSPublicLibStayRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"ttlcache\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	task := "做一个很小的开源库 ttlcache，只要库和测试"
	got := rewriteBuilderToolPath(dir, task, "ttlcache.py")
	if filepath.ToSlash(got) != "ttlcache.py" {
		t.Fatalf("python public lib must stay at root; got %q", got)
	}
	folded := rewriteBuilderToolPath(dir, task, "ttlcache/ttlcache.py")
	if filepath.ToSlash(folded) != "ttlcache.py" {
		t.Fatalf("python pkg/pkg.py must fold to root; got %q", folded)
	}

	js := t.TempDir()
	if err := os.WriteFile(filepath.Join(js, "package.json"), []byte(`{"name":"ttlcache"}`), 0644); err != nil {
		t.Fatal(err)
	}
	gotJS := rewriteBuilderToolPath(js, task, "ttlcache.js")
	if filepath.ToSlash(gotJS) != "ttlcache.js" {
		t.Fatalf("js public lib must stay at root when no src/; got %q", gotJS)
	}
	foldedJS := rewriteBuilderToolPath(js, task, "ttlcache/ttlcache.js")
	if filepath.ToSlash(foldedJS) != "ttlcache.js" {
		t.Fatalf("js pkg/pkg.js must fold to root; got %q", foldedJS)
	}
}

func TestCriticDirectorLayoutBrief_DoesNotMandateDocGo(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/bloomx\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("bloomx", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("bloomx/bloomx.go", []byte("package bloomx\n"), 0644); err != nil {
		t.Fatal(err)
	}
	brief := criticDirectorLayoutBrief(dir)
	if strings.Contains(brief, "Package docs: bloomx/doc.go (never") {
		t.Fatalf("F47: charter must not mandate standalone doc.go:\n%s", brief)
	}
	if !strings.Contains(brief, "file-header") && !strings.Contains(brief, "optional") {
		t.Fatalf("F47: charter should allow header comments / optional doc.go:\n%s", brief)
	}
}

func TestDeliveryLooksEmpty_StubOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "__init__.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if !deliveryLooksEmpty(dir) {
		t.Fatal("stub-only app/ should look empty")
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "main.py"), []byte("from fastapi import FastAPI\napp=FastAPI()\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if deliveryLooksEmpty(dir) {
		t.Fatal("main.py should count as delivery")
	}
}

func TestDeliveryLooksEmpty_RootMainGo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if deliveryLooksEmpty(dir) {
		t.Fatal("root main.go must count as delivery")
	}
}

func TestDeliveryLooksEmpty_GoModAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !deliveryLooksEmpty(dir) {
		t.Fatal("go.mod alone is not delivery")
	}
}

func TestDeliveryLooksEmpty_TopLevelPackageDir_F61(t *testing.T) {
	// Library-first Go: base32x/*.go must count (not only app/internal/src).
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "base32x"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/base32x\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base32x", "base32x.go"), []byte("package base32x\n\nfunc Encode(b []byte) string { return \"\" }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if deliveryLooksEmpty(dir) {
		t.Fatal("F61: top-level package dir sources must count as delivery")
	}
}

func TestProjectLooksSkipGreenSafe_EmptyDisk(t *testing.T) {
	dir := t.TempDir()
	if projectLooksSkipGreenSafe(dir) {
		t.Fatal("empty disk must not skip-green")
	}
}

func TestZ9_ProviderBusyAndTransientNetworkNeedles(t *testing.T) {
	busy := "LLM deepseek returned 503: Service is too busy. Please try again later."
	if !isProviderBusyError(busy) {
		t.Fatal("expected busy classification")
	}
	if !isTransientLLMNetworkError(busy) {
		t.Fatal("busy must also match expanded transient needles (returned 503)")
	}
	deadline := "context deadline exceeded"
	if isProviderBusyError(deadline) {
		t.Fatal("deadline is not provider busy")
	}
	// Deadline is classified via isDeadline in the Builder path, not network needles.
	if isTransientLLMNetworkError(deadline) {
		t.Fatal("plain deadline must not be treated as network transient")
	}
	legacy := "status 503"
	if !isTransientLLMNetworkError(legacy) {
		t.Fatal("legacy status 503 needle must still match")
	}
}

func TestSanitizeDropsOffLanguageSources(t *testing.T) {
	task := "golang HTTP bookmark service, standard library only"
	kept, _, dropped := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "internal/store/store.go", Content: "package store\n\nfunc Save() error { return nil }\n"},
		{Path: "bookmarks.js", Content: "const base = process.argv[2]\n"},
		{Path: "internal/bookmarks/bookmarks.js", Content: "// Placeholder — replace with real implementation.\nexport {};\n"},
		{Path: "notes.md", Content: "# notes\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if paths["bookmarks.js"] || paths["internal/bookmarks/bookmarks.js"] {
		t.Fatalf("js sources must be dropped from a Go project: kept=%v dropped=%v", paths, dropped)
	}
	if !paths["internal/store/store.go"] {
		t.Fatalf("go source must stay: kept=%v", paths)
	}
	if !paths["notes.md"] {
		t.Fatalf("markdown is not a foreign source language: kept=%v", paths)
	}
	named := "golang service and also write bookmarks.js"
	keptNamed, _, _ := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "bookmarks.js", Content: "const base = 1\n"},
	}, named)
	if len(keptNamed) != 1 || !strings.HasSuffix(filepath.ToSlash(keptNamed[0].Path), "bookmarks.js") {
		t.Fatalf("explicit path must stay, got %+v", keptNamed)
	}
}

func TestRejectToolWriteUsesOnDiskPythonAndStoredBrief(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "booking"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "booking", "domain.py"), []byte("class Booking:\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if reason := rejectToolWrite("bookings.js", "add the http module, standard library only"); reason == "" {
		t.Fatal("javascript must be rejected when the tree is Python")
	}
	if reason := rejectToolWrite("booking/http_api.py", "add the http module, standard library only"); reason != "" {
		t.Fatalf("python write: %s", reason)
	}

	empty := t.TempDir()
	reqDir := filepath.Join(empty, "docs", "workflow")
	if err := os.MkdirAll(reqDir, 0o755); err != nil {
		t.Fatal(err)
	}
	brief := "用 Python 做个本地服务。只用 Python 标准库。\n"
	if err := os.WriteFile(filepath.Join(reqDir, "user_requirement.md"), []byte(brief), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	if reason := rejectToolWrite("bookings.js", "接着弄，只用标准库"); reason == "" {
		t.Fatal("stored brief should keep the project on Python")
	}
}

func TestRemoveOffLanguageResidueDeletesRootJS(t *testing.T) {
	dir := t.TempDir()
	js := filepath.Join(dir, "bookings.js")
	if err := os.WriteFile(js, []byte("const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "bookings")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "bookings.js"), []byte("const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeOffLanguageResidue(dir, []builderCodeFile{{Path: "bookings/bookings.js"}}, nil)
	if _, err := os.Stat(js); !os.IsNotExist(err) {
		t.Fatal("root bookings.js should be removed with the rejected write")
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatal("empty directory left by the rejected write should be removed")
	}
}

func TestRejectToolWriteOffLanguageAndWorkflowShadow(t *testing.T) {
	task := "golang HTTP ledger, standard library only"
	if reason := rejectToolWrite("ledger.js", task); reason == "" {
		t.Fatal("javascript must be rejected in a Go task")
	}
	if reason := rejectToolWrite("storage/file.go", task); reason != "" {
		t.Fatalf("go source must be writable: %s", reason)
	}
	if reason := rejectToolWrite("notes.md", task); reason != "" {
		t.Fatalf("markdown must stay: %s", reason)
	}
	if reason := rejectToolWrite("avatars_todo.md", task); reason == "" {
		t.Fatal("root workflow checklist must be rejected")
	}
	if reason := rejectToolWrite("docs/workflow/avatars_plan.md", task); reason == "" {
		t.Fatal("workflow plan must not be written by the tool")
	}
	if reason := rejectToolWrite("bookmarks.js", "golang service and also write bookmarks.js"); reason != "" {
		t.Fatalf("a path named in the task must stay: %s", reason)
	}
}

func TestRootMainSatisfiedByCmdEntry(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "server", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !rootEntryAlreadyCovered(dir, "main.go", nil) {
		t.Fatal("cmd/server/main.go should cover a root main.go")
	}
	if rootEntryAlreadyCovered(dir, "internal/store/store.go", nil) {
		t.Fatal("package file is not a root entry")
	}
	if !rootEntryAlreadyCovered(dir, "main.py", map[string]bool{"app/main.py": true}) {
		t.Fatal("app/main.py should cover a root main.py")
	}
}
