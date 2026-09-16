package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SearchInput describes a code search request.
type SearchInput struct {
	Pattern  string `json:"pattern"`            // regex pattern to search for
	Path     string `json:"path,omitempty"`     // directory or file to search (default ".")
	FileType string `json:"file_type,omitempty"` // e.g. "go", "py", "js" — maps to --type or glob
	MaxLines int    `json:"max_lines,omitempty"` // max result lines (default 200)
}

// SearchTool searches code using grep/rg. It wraps the system grep
// command with sensible defaults for code search: recursive, line
// numbers, color off, 200-line limit.
type SearchTool struct{}

func (SearchTool) Name() string {
	return "search"
}

func (SearchTool) IsConcurrencySafe(input any) bool {
	return true
}

func (t SearchTool) Call(ctx context.Context, input any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	in, ok := input.(SearchInput)
	if !ok {
		if patStr, isStr := input.(string); isStr {
			in = SearchInput{Pattern: patStr}
		} else {
			return Result{}, fmt.Errorf("search requires a pattern string or {pattern, path, file_type} object")
		}
	}

	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		return Result{}, fmt.Errorf("search: pattern is required")
	}

	searchPath := strings.TrimSpace(in.Path)
	if searchPath == "" {
		searchPath = "."
	}

	fileType := strings.TrimSpace(in.FileType)
	maxLines := in.MaxLines
	if maxLines <= 0 || maxLines > 500 {
		maxLines = 200
	}

	// Prefer rg (ripgrep) if available, fall back to grep.
	args := buildGrepArgs(pattern, searchPath, fileType, maxLines)
	cmd := "grep"
	if rgPath, err := exec.LookPath("rg"); err == nil {
		cmd = rgPath
		args = buildRgArgs(pattern, searchPath, fileType, maxLines)
	}

	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx2, cmd, args...)
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	if err != nil {
		if ctx2.Err() != nil {
			return Result{}, fmt.Errorf("search timed out after 30s")
		}
		// grep returns exit code 1 for "no matches" — not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return Result{Content: "(no matches found)"}, nil
		}
		return Result{}, fmt.Errorf("search: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	output := stdout.String()
	if strings.TrimSpace(output) == "" {
		return Result{Content: "(no matches found)"}, nil
	}

	if len(output) > 100000 {
		output = output[:100000] + "\n... [truncated at 100KB]"
	}

	return Result{Content: output}, nil
}

func buildRgArgs(pattern, path, fileType string, maxLines int) []string {
	args := []string{
		"--no-heading",
		"--line-number",
		"--color", "never",
		"--max-count", fmt.Sprintf("%d", maxLines),
	}
	if fileType != "" {
		args = append(args, "--type", fileType)
	}
	args = append(args, "--", pattern, path)
	return args
}

func buildGrepArgs(pattern, path, fileType string, maxLines int) []string {
	args := []string{
		"-rn",       // recursive, line numbers
		"--color=never",
		"-I",         // skip binary files
	}
	if fileType != "" {
		args = append(args, "--include", "*."+fileType)
	}
	args = append(args, "-m", fmt.Sprintf("%d", maxLines))
	args = append(args, "--", pattern, path)
	return args
}
