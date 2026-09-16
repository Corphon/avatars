// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteToolCall_WritesFileInsideSandbox(t *testing.T) {
	workingDir := t.TempDir()
	tool := WriteTool{}

	result, err := tool.Call(context.Background(), WriteInput{
		Path:       filepath.Join("nested", "note.txt"),
		Content:    "hello",
		WorkingDir: workingDir,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	content, err := os.ReadFile(filepath.Join(workingDir, "nested", "note.txt"))
	if err != nil {
		t.Fatalf("read written file failed: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("expected hello, got %q", string(content))
	}
	if !strings.Contains(result.Content, "Wrote 5 bytes") {
		t.Fatalf("unexpected result summary: %q", result.Content)
	}
}

func TestWriteToolCall_RejectsSandboxEscape(t *testing.T) {
	workingDir := t.TempDir()
	tool := WriteTool{}

	_, err := tool.Call(context.Background(), WriteInput{
		Path:       filepath.Join("..", "escape.txt"),
		Content:    "bad",
		WorkingDir: workingDir,
	})
	if err == nil {
		t.Fatal("expected sandbox escape error")
	}
	if !strings.Contains(err.Error(), "escapes sandbox") {
		t.Fatalf("expected sandbox error, got %v", err)
	}
}

func TestWriteToolCall_RejectsOverwriteByDefault(t *testing.T) {
	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "note.txt")
	if err := os.WriteFile(targetPath, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	tool := WriteTool{}
	_, err := tool.Call(context.Background(), WriteInput{
		Path:       "note.txt",
		Content:    "new",
		WorkingDir: workingDir,
	})
	if err == nil {
		t.Fatal("expected overwrite rejection")
	}
	if !strings.Contains(err.Error(), "refuses to overwrite") {
		t.Fatalf("expected overwrite rejection, got %v", err)
	}
}

func TestWriteToolCall_AllowsOverwriteWhenEnabled(t *testing.T) {
	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "note.txt")
	if err := os.WriteFile(targetPath, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	tool := WriteTool{}
	_, err := tool.Call(context.Background(), WriteInput{
		Path:       "note.txt",
		Content:    "new",
		WorkingDir: workingDir,
		Overwrite:  true,
	})
	if err != nil {
		t.Fatalf("expected overwrite success, got %v", err)
	}
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read overwritten file failed: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("expected new, got %q", string(content))
	}
}

func TestWriteToolCall_AutoOverwriteStageHTML(t *testing.T) {
	workingDir := t.TempDir()
	stageDir := filepath.Join(workingDir, "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(stageDir, "demo.html")
	if err := os.WriteFile(targetPath, []byte("<html>old</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := WriteTool{}
	_, err := tool.Call(context.Background(), WriteInput{
		Path:       filepath.Join("stage", "demo.html"),
		Content:    "<html>new</html>",
		WorkingDir: workingDir,
	})
	if err != nil {
		t.Fatalf("stage html should auto-overwrite: %v", err)
	}
	got, _ := os.ReadFile(targetPath)
	if string(got) != "<html>new</html>" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteToolCall_AutoOverwriteWithEditIntent(t *testing.T) {
	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "page.html")
	if err := os.WriteFile(targetPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := WriteTool{}
	_, err := tool.Call(context.Background(), WriteInput{
		Path:            "page.html",
		Content:         "new",
		WorkingDir:      workingDir,
		Intent:          "Builder: edit page.html",
		ExpectedTargets: []string{"page.html"},
	})
	if err != nil {
		t.Fatalf("edit intent should auto-overwrite: %v", err)
	}
}

func TestWriteToolCall_AllowsAvatarsHomeSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AVATARS_HOME", home)
	absSkills := filepath.Join(home, "skills", "generated", "x.md")
	workingDir := t.TempDir()
	tool := WriteTool{}
	_, err := tool.Call(context.Background(), WriteInput{
		Path:       absSkills,
		Content:    "skill",
		WorkingDir: workingDir,
		Overwrite:  true,
	})
	if err != nil {
		t.Fatalf("AVATARS_HOME skills write must be allowed: %v", err)
	}
	data, err := os.ReadFile(absSkills)
	if err != nil || string(data) != "skill" {
		t.Fatalf("skills file not written: %v %q", err, data)
	}
}
