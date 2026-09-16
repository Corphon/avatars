// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeGitExecutor struct {
	output     string
	err        error
	workingDir string
	command    []string
}

func (f *fakeGitExecutor) Run(ctx context.Context, workingDir string, command []string) (string, error) {
	f.workingDir = workingDir
	f.command = append([]string(nil), command...)
	return f.output, f.err
}

func TestGitToolCall_ReturnsOutput(t *testing.T) {
	workingDir := t.TempDir()
	executor := &fakeGitExecutor{output: " M file.txt"}
	tool := GitTool{executor: executor}

	result, err := tool.Call(context.Background(), GitInput{
		Args:          []string{"status", "--short"},
		WorkingDir:    workingDir,
		TimeoutMillis: 1000,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Content != "M file.txt" && result.Content != " M file.txt" {
		t.Fatalf("unexpected output: %q", result.Content)
	}
	if executor.workingDir != workingDir {
		t.Fatalf("expected working dir %q, got %q", workingDir, executor.workingDir)
	}
	if len(executor.command) < 2 || executor.command[0] != "git" || executor.command[1] != "status" {
		t.Fatalf("unexpected command: %v", executor.command)
	}
}

func TestGitToolCall_RejectsDisallowedSubcommand(t *testing.T) {
	tool := GitTool{}

	_, err := tool.Call(context.Background(), GitInput{
		Args:       []string{"commit", "-m", "bad"},
		WorkingDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected disallowed subcommand error")
	}
	if !strings.Contains(err.Error(), "read-only subcommands") {
		t.Fatalf("expected read-only error, got %v", err)
	}
}

func TestGitToolCall_PropagatesExecutorFailure(t *testing.T) {
	workingDir := t.TempDir()
	tool := GitTool{executor: &fakeGitExecutor{output: "fatal: not a git repository", err: errors.New("exit status 128")}}

	result, err := tool.Call(context.Background(), GitInput{
		Args:       []string{"status", "--short"},
		WorkingDir: workingDir,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "git tool command failed") {
		t.Fatalf("expected wrapped git error, got %v", err)
	}
	if !strings.Contains(result.Content, "fatal: not a git repository") {
		t.Fatalf("expected stderr in result, got %q", result.Content)
	}
}
