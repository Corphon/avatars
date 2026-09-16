package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSurgicalApplyFromDump_ReplaceLine(t *testing.T) {
	dir := t.TempDir()
	path := "sample.txt"
	original := "def add(a, b):\n    return a + b\n\ndef main():\n    print(add(1, 2))\n"
	proposed := "def add(a, b):\n    return a + b + 1\n\ndef main():\n    print(add(1, 2))\n"
	if err := os.WriteFile(filepath.Join(dir, path), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := SurgicalApplyFromDump(original, proposed, path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaces < 1 && res.Applied() < 1 {
		t.Fatalf("expected replace/apply, got %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, path))
	if !strings.Contains(string(got), "return a + b + 1") {
		t.Fatalf("replace not applied:\n%s\nresult=%+v", got, res)
	}
	if strings.Count(string(got), "def main") != 1 {
		t.Fatal("should not duplicate main")
	}
}

func TestSurgicalApplyFromDump_InsertLine(t *testing.T) {
	dir := t.TempDir()
	path := "cfg.txt"
	original := "const a = 1;\nconst b = 2;\n"
	proposed := "const a = 1;\nconst c = 3;\nconst b = 2;\n"
	if err := os.WriteFile(filepath.Join(dir, path), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := SurgicalApplyFromDump(original, proposed, path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied() < 1 {
		t.Fatalf("expected apply, got %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, path))
	if !strings.Contains(string(got), "const c = 3;") {
		t.Fatalf("insert missing:\n%s\n%+v", got, res)
	}
}

func TestSurgicalApplyFromDump_RefuseDissimilar(t *testing.T) {
	dir := t.TempDir()
	path := "x.go"
	original := "package main\n\nfunc main() {\n\tprintln(\"a\")\n}\n"
	proposed := "package other\n\nfunc totallyDifferent() {}\n"
	if err := os.WriteFile(filepath.Join(dir, path), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := SurgicalApplyFromDump(original, proposed, path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied() != 0 {
		t.Fatalf("dissimilar dump must not apply: %+v", res)
	}
	if !strings.Contains(res.Reason, "similarity") {
		t.Fatalf("expected similarity refuse, got %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, path))
	if string(got) != original {
		t.Fatal("file must be unchanged")
	}
}

func TestComputeLineHunks_Replace(t *testing.T) {
	a := strings.Split("a\nb\nc\n", "\n")
	b := strings.Split("a\nB\nc\n", "\n")
	hunks := computeLineHunks(a, b)
	if len(hunks) != 1 || hunks[0].Kind != hunkReplace {
		t.Fatalf("expected one replace hunk, got %+v", hunks)
	}
}
