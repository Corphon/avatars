package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecklistFeatureTokens_FlagsAndEnv(t *testing.T) {
	toks := checklistFeatureTokens("Add `--delim` and `CSVMESH_STRICT` support")
	joined := strings.Join(toks, "\n")
	if !strings.Contains(joined, "--delim") || !strings.Contains(joined, "CSVMESH_STRICT") {
		t.Fatalf("tokens=%v", toks)
	}
	how := checklistFeatureTokens("Implement `--how left|inner` join logic")
	if len(how) != 1 || how[0] != "--how" {
		t.Fatalf("how tokens=%v want [--how]", how)
	}
	if len(checklistFeatureTokens("Edit `cmd/csvmesh/main.go`")) != 0 {
		t.Fatal("path-only must not yield feature tokens")
	}
}

func TestChecklistItemHasPathEvidence_RejectsWireFlags(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "cmd", "csvmesh", "main.go"), "package main\n")
	if checklistItemHasPathEvidence(root, "Wire CLI flags and environment in `cmd/csvmesh/main.go`") {
		t.Fatal("wire+flags must not pass on path existence alone")
	}
	if checklistItemHasPathEvidence(root, "Implement `--how left|inner` in join") {
		t.Fatal("feature flag item must not pass on path alone")
	}
	if !checklistItemHasPathEvidence(root, "Create entrypoint `cmd/csvmesh/main.go`") {
		t.Fatal("plain create-path item should still pass")
	}
}

func TestIsChecklistItemSatisfied_NamedTestAndStaticCheck(t *testing.T) {
	root := t.TempDir()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	testsPassCache.Store(abs, true)
	t.Cleanup(func() { testsPassCache.Delete(abs) })
	mustWriteFile(t, filepath.Join(root, "lib.go"), "package lib\n")
	mustWriteFile(t, filepath.Join(root, "lib_test.go"), "package lib\n\nimport \"testing\"\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\nfunc TestC(t *testing.T) {}\n")
	if !isChecklistItemSatisfied(root, "`TestDoExecutesOnceUnderConcurrency`: launch 100 goroutines", "") {
		t.Fatal("named Test* item should tick when the suite is already green")
	}
	if !isChecklistItemSatisfied(root, "Run `go vet ./...` — must exit 0 with no output", "") {
		t.Fatal("static-check item should tick when tests pass")
	}
	if !isChecklistItemSatisfied(root, "`gofmt -l` must be empty", "") {
		t.Fatal("gofmt gate should tick when the suite is already green")
	}
	if !isChecklistItemSatisfied(root, "Toolchain verification gates (2/4)", "") {
		t.Fatal("toolchain verification group should tick when tests pass")
	}
	if !isChecklistItemSatisfied(root, "cargo fmt and prettier format check", "") {
		t.Fatal("format-check items should tick across languages when tests pass")
	}
	if !isChecklistItemSatisfied(root, "No HTTP handlers, server, or CLI flag parsing anywhere in the package", "") {
		t.Fatal("absence-of-HTTP gate should tick when the suite is already green")
	}
	if !isChecklistItemSatisfied(root, "The external test package compiles, confirming the module is importable", "") {
		t.Fatal("importable/external-test gate should tick when tests pass")
	}
}

func TestIsChecklistItemSatisfied_RequiresFeatureInCode(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "cmd", "app", "main.go"), "package main\n")
	desc := "Wire `--delim` into `cmd/app/main.go`"
	if isChecklistItemSatisfied(root, desc, "package main\nfunc main() {}\n") {
		t.Fatal("missing --delim in code must not satisfy")
	}
	code := "package main\nvar delim = fs.String(\"delim\", \",\", \"\")\n"
	if !isChecklistItemSatisfied(root, desc, code) {
		t.Fatal("code with delim should satisfy")
	}
}

func TestScaffoldLineMatchesSignals_IgnoresFeatureAndNonScaffold(t *testing.T) {
	signals := map[string]bool{"test": true, "project": true, "scaffold": true}
	if scaffoldLineMatchesSignals("- [ ] end-to-end and regression tests (0/7)", signals) {
		t.Fatal("regression tests must not scaffold-auto-mark")
	}
	if scaffoldLineMatchesSignals("- [ ] implement `--how` join logic", signals) {
		t.Fatal("feature flag line must not scaffold-auto-mark")
	}
	if !scaffoldLineMatchesSignals("- [ ] project scaffolding & test fixtures", signals) {
		t.Fatal("explicit scaffolding line should match")
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
