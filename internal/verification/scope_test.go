package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilterChecksForChangedFiles_SkillOnlySkipsToolchains(t *testing.T) {
	checks := append(DefaultChecksFor(t.TempDir()), DetectedFileChecks(t.TempDir())...)
	// Even if DetectedFileChecks is empty, DefaultChecks are Go.
	got := FilterChecksForChangedFiles(checks, []string{
		filepath.Join("avatars", "skills", "generated", "foo-builder-skill.md"),
	})
	if len(got) != 0 {
		t.Fatalf("skill-only write should skip toolchains, got %d checks", len(got))
	}
}

func TestFilterChecksForChangedFiles_GoSourcesKeepGo(t *testing.T) {
	checks := DefaultChecksFor(".")
	got := FilterChecksForChangedFiles(checks, []string{"internal/store/store.go", "go.mod"})
	if len(got) == 0 {
		t.Fatal("go sources should keep go checks")
	}
	for _, c := range got {
		if familyForCheck(c) != familyGo {
			t.Fatalf("unexpected family for %q", c.Name)
		}
	}
}

func TestFilterChecksForChangedFiles_PythonKeepsPythonOnly(t *testing.T) {
	checks := []CommandCheck{
		{Name: "go build", Command: []string{"go", "build", "./..."}},
		{Name: "python syntax", Command: []string{"python", "-m", "py_compile"}},
		{Name: "js syntax", Command: []string{"node", "--check", "x.js"}},
	}
	got := FilterChecksForChangedFiles(checks, []string{"app/main.py", "requirements.txt"})
	if len(got) != 1 || got[0].Name != "python syntax" {
		t.Fatalf("want only python syntax, got %+v", got)
	}
}

func TestFilterChecksForChangedFiles_RustAndJS(t *testing.T) {
	checks := []CommandCheck{
		{Name: "go test", Command: []string{"go", "test", "./..."}},
		{Name: "rust syntax", Command: []string{"rustc", "--version"}},
		{Name: "javascript syntax", Command: []string{"node", "--check", "a.js"}},
	}
	got := FilterChecksForChangedFiles(checks, []string{"src/main.rs", "Cargo.toml"})
	if len(got) != 1 || familyForCheck(got[0]) != familyRust {
		t.Fatalf("want rust only, got %+v", got)
	}
	got = FilterChecksForChangedFiles(checks, []string{"src/index.ts"})
	if len(got) != 1 || familyForCheck(got[0]) != familyJS {
		t.Fatalf("want js only, got %+v", got)
	}
}

func TestAllowMissingGoModulePartialIn_NoSourcesIsPartial(t *testing.T) {
	dir := t.TempDir()
	// go.mod alone, no .go → PARTIAL (scaffold)
	if err := writeFile(filepath.Join(dir, "go.mod"), "module x\n\ngo 1.22\n"); err != nil {
		t.Fatal(err)
	}
	allowed, _ := allowMissingGoModulePartialIn(dir, `go: warning: "./..." matched no packages`+"\nno packages to test", errExit1{})
	if !allowed {
		t.Fatal("go.mod without sources must stay PARTIAL")
	}
}

func TestAllowMissingGoModulePartialIn_SourcesEmptyPkgFails(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, "go.mod"), "module x\n\ngo 1.22\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatal(err)
	}
	allowed, _ := allowMissingGoModulePartialIn(dir, `go: warning: "./..." matched no packages`+"\nno packages to test", errExit1{})
	if allowed {
		t.Fatal("go.mod + .go with matched no packages must FAIL (not partial)")
	}
}

func TestRunnerScopeToChangedFiles_EmptyChecksPass(t *testing.T) {
	r := NewDefaultRunner(t.TempDir())
	r.ScopeToChangedFiles([]string{"skills/generated/x.md"})
	report := r.Run(context.Background())
	if report.Verdict != VerdictPass {
		t.Fatalf("vacuous PASS expected, got %s (%s)", report.Verdict, report.Summary)
	}
}

type errExit1 struct{}

func (errExit1) Error() string { return "exit status 1" }

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
