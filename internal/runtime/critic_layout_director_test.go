package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGenerateTarget(t *testing.T) {
	got := parseGenerateTarget("Generate doc.go AND its test file")
	if got != "doc.go" {
		t.Fatalf("got %q", got)
	}
	got = parseGenerateTarget("Generate cmd/geohashn/ AND its test file (final file)")
	if got != "cmd/geohashn/" {
		t.Fatalf("got %q", got)
	}
}

func TestDirectorRewriteGenerateTitle_RemapBareDoc(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/geohashn\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("geohashn", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("geohashn/geohashn.go", []byte("package geohashn\nfunc Encode() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("docs/workflow", 0755); err != nil {
		t.Fatal(err)
	}
	phase := "# Phase 1\n\n## Layout Note\nThe public library lives at the top-level `geohashn/` package, NOT under `internal/`.\n"
	if err := os.WriteFile("docs/workflow/phase1.md", []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}

	newTitle, changed, reject := directorRewriteGenerateTitle(dir, "Generate doc.go AND its test file")
	if reject != "" {
		t.Fatalf("reject=%q", reject)
	}
	if !changed || newTitle != "Generate geohashn/doc.go AND its test file" {
		t.Fatalf("got title=%q changed=%v", newTitle, changed)
	}
}

func TestDirectorRewriteGenerateTitle_BlockDebug(t *testing.T) {
	dir := t.TempDir()
	_, _, reject := directorRewriteGenerateTitle(dir, "Generate internal/debug/ AND its test file")
	if reject == "" || !strings.Contains(reject, "blocked") {
		t.Fatalf("expected block, got %q", reject)
	}
}

func TestDirectorRewriteGenerateTitle_RemapInternalLib(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/geohashn\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("docs/workflow", 0755); err != nil {
		t.Fatal(err)
	}
	phase := "包路径建议：geohashn\n纯库优先\nNOT under internal/\n"
	if err := os.WriteFile("docs/workflow/phase1.md", []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}
	newTitle, changed, reject := directorRewriteGenerateTitle(dir, "Generate internal/geohashn/geohashn.go AND its test file")
	if reject != "" {
		t.Fatalf("reject=%q", reject)
	}
	if !changed || newTitle != "Generate geohashn.go AND its test file" {
		t.Fatalf("F78 unbury to root; got %q changed=%v", newTitle, changed)
	}
}

func TestResolveLayoutCharter_NoCLIForbidsInternal(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/ratebucket\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("docs/workflow", 0755); err != nil {
		t.Fatal(err)
	}
	phase := "# Phase 1\nNo CLI tool or main package. Library sources and tests only.\n"
	if err := os.WriteFile("docs/workflow/phase1.md", []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}
	c := resolveLayoutCharter(dir)
	if !c.ForbidInternalLib {
		t.Fatalf("No CLI library charter must forbid internal/ burial, got %+v", c)
	}
	if c.LibraryDir != "ratebucket" {
		t.Fatalf("LibraryDir=%q want ratebucket", c.LibraryDir)
	}
	brief := criticDirectorLayoutBrief(dir)
	if !strings.Contains(brief, "internal/<helper>/") {
		t.Fatalf("charter must allow private internal helpers:\n%s", brief)
	}
	if strings.Contains(brief, "Do NOT place public library API under internal/\n") &&
		!strings.Contains(brief, "internal/<mod>/") {
		t.Fatalf("charter must not read as a blanket internal/ ban:\n%s", brief)
	}
}

func TestDetectConflictingGoLayout_DuplicateBuried(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/geohashn\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "geohashn"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "geohashn", "a.go"), []byte("package geohashn\nfunc Encode() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "geohashn"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "geohashn", "a.go"), []byte("package geohashn\nfunc Encode() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectConflictingGoLayout(dir)
	if got == "" || !strings.Contains(got, "both") {
		t.Fatalf("expected duplicate conflict, got %q", got)
	}
}
