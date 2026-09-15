package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readIntentInputFile(path string) (string, error) {
	content, _, err := readIntentInputFileWithResolve(path)
	return content, err
}

func readIntentInputFileWithResolve(path string) (content string, resolved string, err error) {
	trimmed := strings.TrimSpace(path)
	trimmed = strings.Trim(trimmed, `"'`)
	if trimmed == "" {
		return "", "", fmt.Errorf("input file path cannot be empty")
	}
	resolved = resolveIntentInputPath(trimmed)
	data, readErr := os.ReadFile(resolved)
	if readErr != nil {
		if resolved != trimmed {
			return "", resolved, fmt.Errorf("read input file %q (resolved %q): %w", trimmed, resolved, readErr)
		}
		return "", resolved, fmt.Errorf("read input file %q: %w", trimmed, readErr)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", resolved, fmt.Errorf("input file %q is empty", resolved)
	}
	return string(data), resolved, nil
}

// resolveIntentInputPath maps a bare plan filename onto docs/workflow when
// the given path is missing. Language-agnostic; does not search the whole tree.
func resolveIntentInputPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return trimmed
	}
	if intentInputFileExists(trimmed) {
		return trimmed
	}
	base := filepath.Base(trimmed)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return trimmed
	}
	for _, dir := range []string{
		filepath.Join("docs", "workflow"),
		"docs",
	} {
		candidate := filepath.Join(dir, base)
		if intentInputFileExists(candidate) {
			return candidate
		}
	}
	return trimmed
}

func intentInputFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
