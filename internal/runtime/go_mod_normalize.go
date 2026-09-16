package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// normalizeGoModBytes deduplicates repeated `toolchain` / `go` directives so
// `go mod tidy` / `go build` do not die on "repeated toolchain statement" (F22).
// Keeps the first go line and the first toolchain line; drops later duplicates.
func normalizeGoModBytes(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return data, false
	}
	text := string(data)
	// Normalize CRLF for scanning; restore LF-only output (go fmt style).
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	seenGo := false
	seenToolchain := false
	changed := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		switch {
		case strings.HasPrefix(lower, "go "):
			if seenGo {
				changed = true
				continue
			}
			seenGo = true
		case strings.HasPrefix(lower, "toolchain "):
			if seenToolchain {
				changed = true
				continue
			}
			seenToolchain = true
		case strings.Trim(trim, "`") == "" && strings.Contains(trim, "```"):
			changed = true
			continue
		case trim == "```" || strings.HasPrefix(trim, "```"):
			changed = true
			continue
		}
		out = append(out, line)
	}
	// Drop trailing empty lines beyond one.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	normalized := strings.Join(out, "\n")
	if normalized != "" {
		normalized += "\n"
	}
	if !changed && normalized == string(data) {
		return data, false
	}
	if !changed && bytes.Equal([]byte(normalized), data) {
		return data, false
	}
	return []byte(normalized), changed || !bytes.Equal([]byte(normalized), data)
}

// ensureGoModNormalized rewrites go.mod in place when duplicate directives exist.
// Returns whether a rewrite happened.
func ensureGoModNormalized(wd string) bool {
	path := filepath.Join(wd, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	next, changed := normalizeGoModBytes(data)
	if !changed {
		return false
	}
	if err := os.WriteFile(path, next, 0644); err != nil {
		return false
	}
	return true
}
