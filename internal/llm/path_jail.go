package llm

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"avatars/internal/writejail"
)

var (
	toolWriteRootMu sync.Mutex
	toolWriteRoot   string
)

// SetToolWriteRoot registers the project root that write/edit tools must stay inside.
// Empty clears the jail (tests / non-project contexts).
func SetToolWriteRoot(root string) {
	toolWriteRootMu.Lock()
	defer toolWriteRootMu.Unlock()
	toolWriteRoot = strings.TrimSpace(root)
}

// ClearToolWriteRoot clears the write jail root.
func ClearToolWriteRoot() {
	SetToolWriteRoot("")
}

// ToolWriteRoot returns the registered project root (may be empty).
func ToolWriteRoot() string {
	toolWriteRootMu.Lock()
	defer toolWriteRootMu.Unlock()
	return toolWriteRoot
}

// ConfinePath resolves path under root and rejects escapes (cross-platform).
// P9-1: AVATARS_HOME/skills/** is allowed as an extra write root.
func ConfinePath(root, path string) (string, error) {
	abs, err := writejail.Confine(root, path)
	if err != nil {
		return "", fmt.Errorf("%w", err)
	}
	return abs, nil
}

// ConfineToolPath applies RewriteToolPath then ConfinePath against ToolWriteRoot.
// When no write root is registered, only obvious ".." traversal is rejected
// (tests / non-project contexts keep working with absolute temp paths).
func ConfineToolPath(path string) (string, error) {
	return ConfineToolPathWithContent(path, "")
}

// ConfineToolPathWithContent is ConfineToolPath with the file body so layout
// rewrite can lift public-API sources out of private helper directories.
func ConfineToolPathWithContent(path, content string) (string, error) {
	rewritten := RewriteToolPathWithContent(path, content)
	root := ToolWriteRoot()
	return ConfinePath(root, rewritten)
}

// RelativizeForDisplay returns a project-relative slash path when under root.
func RelativizeForDisplay(root, absPath string) string {
	if root == "" {
		return filepath.ToSlash(absPath)
	}
	if rel, err := filepath.Rel(filepath.Clean(root), absPath); err == nil {
		slash := filepath.ToSlash(rel)
		if slash != ".." && !strings.HasPrefix(slash, "../") {
			return slash
		}
	}
	return filepath.ToSlash(absPath)
}

// DisplayToolPath is RelativizeForDisplay for either abs or project-relative paths.
func DisplayToolPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return RelativizeForDisplay(ToolWriteRoot(), path)
	}
	return filepath.ToSlash(path)
}

// FormatWriteResult describes a successful write using the on-disk path.
// If layout remap moved the file, remapped_from is appended (tool-result
// suffix — never prepended onto the LLM stable prefix, so prefix cache stays).
func FormatWriteResult(requested, writtenAbs string, n int) string {
	display := DisplayToolPath(writtenAbs)
	msg := fmt.Sprintf("Successfully wrote %d bytes to %s", n, display)
	from := DisplayToolPath(requested)
	if from != "" && from != display {
		msg += " (remapped from " + from + ")"
	}
	return msg
}
