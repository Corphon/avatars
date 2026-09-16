package llm

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfinePath_RejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := ConfinePath(root, "../outside.txt"); err == nil {
		t.Fatal("expected escape error for ../")
	}
	if _, err := ConfinePath(root, filepath.Join(root, "..", "x.txt")); err == nil {
		t.Fatal("expected escape error for abs parent")
	}
	got, err := ConfinePath(root, "app/main.py")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "app", "main.py")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestConfineToolPath_UsesRewriteThenJail(t *testing.T) {
	root := t.TempDir()
	SetToolWriteRoot(root)
	defer ClearToolWriteRoot()
	ClearPathRewriteHandler()
	_, err := ConfineToolPath("../evil.py")
	if err == nil || !strings.Contains(err.Error(), "escapes sandbox") {
		t.Fatalf("expected sandbox escape, got %v", err)
	}
}
