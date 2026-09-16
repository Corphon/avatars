package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeGoModBytes_DedupeToolchain(t *testing.T) {
	in := []byte("module example.com/cidrkit\n\ngo 1.21\n\ntoolchain go1.26.1\n\ntoolchain go1.26.1\n")
	out, changed := normalizeGoModBytes(in)
	if !changed {
		t.Fatal("expected change")
	}
	s := string(out)
	if strings.Count(s, "toolchain ") != 1 {
		t.Fatalf("want 1 toolchain, got:\n%s", s)
	}
	if !strings.Contains(s, "module example.com/cidrkit") || !strings.Contains(s, "go 1.21") {
		t.Fatalf("lost module/go:\n%s", s)
	}
}

func TestEnsureGoModNormalized_RewritesDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	raw := "module x\n\ngo 1.22\n\ntoolchain go1.22.0\n\ntoolchain go1.22.0\n"
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	if !ensureGoModNormalized(dir) {
		t.Fatal("expected rewrite")
	}
	got, _ := os.ReadFile(path)
	if strings.Count(string(got), "toolchain ") != 1 {
		t.Fatalf("still duplicated:\n%s", got)
	}
	if ensureGoModNormalized(dir) {
		t.Fatal("second pass should be no-op")
	}
}

func TestNormalizeGoModBytes_DropsMarkdownFence(t *testing.T) {
	in := []byte("module pqueue\n\ngo 1.21\n```\n")
	out, changed := normalizeGoModBytes(in)
	if !changed {
		t.Fatal("expected fence line dropped")
	}
	s := string(out)
	if strings.Contains(s, "```") {
		t.Fatalf("fence leaked:\n%s", s)
	}
	if !strings.Contains(s, "module pqueue") || !strings.Contains(s, "go 1.21") {
		t.Fatalf("lost module/go:\n%s", s)
	}
}
