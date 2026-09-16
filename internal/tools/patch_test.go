// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchToolCall_ReplacesSingleMatch(t *testing.T) {
	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "note.txt")
	if err := os.WriteFile(targetPath, []byte("before patch"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	tool := PatchTool{}
	result, err := tool.Call(context.Background(), PatchInput{
		Path:       "note.txt",
		Old:        "before patch",
		New:        "after patch",
		WorkingDir: workingDir,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read patched file failed: %v", err)
	}
	if string(content) != "after patch" {
		t.Fatalf("expected after patch, got %q", string(content))
	}
	if !strings.Contains(result.Content, "Patched 1 occurrence") {
		t.Fatalf("unexpected patch summary: %q", result.Content)
	}
}

func TestPatchToolCall_RejectsSandboxEscape(t *testing.T) {
	workingDir := t.TempDir()
	tool := PatchTool{}

	_, err := tool.Call(context.Background(), PatchInput{
		Path:       filepath.Join("..", "escape.txt"),
		Old:        "before",
		New:        "after",
		WorkingDir: workingDir,
	})
	if err == nil {
		t.Fatal("expected sandbox escape error")
	}
	if !strings.Contains(err.Error(), "escapes sandbox") {
		t.Fatalf("expected sandbox error, got %v", err)
	}
}

func TestPatchToolCall_RejectsMultipleMatchesWithoutReplaceAll(t *testing.T) {
	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "note.txt")
	if err := os.WriteFile(targetPath, []byte("repeat\nrepeat"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	tool := PatchTool{}
	_, err := tool.Call(context.Background(), PatchInput{
		Path:       "note.txt",
		Old:        "repeat",
		New:        "done",
		WorkingDir: workingDir,
	})
	if err == nil {
		t.Fatal("expected multiple match error")
	}
	if !strings.Contains(err.Error(), "exactly one match") {
		t.Fatalf("expected single match error, got %v", err)
	}
}

func TestPatchToolCall_ReplacesAllWhenEnabled(t *testing.T) {
	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "note.txt")
	if err := os.WriteFile(targetPath, []byte("repeat\nrepeat"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	tool := PatchTool{}
	_, err := tool.Call(context.Background(), PatchInput{
		Path:       "note.txt",
		Old:        "repeat",
		New:        "done",
		WorkingDir: workingDir,
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatalf("expected replace-all success, got %v", err)
	}
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read patched file failed: %v", err)
	}
	if string(content) != "done\ndone" {
		t.Fatalf("expected both matches replaced, got %q", string(content))
	}
}
