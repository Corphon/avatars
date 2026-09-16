package projectfiles

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// MaxWalkFiles caps repository walks so fixture trees cannot dominate a run (B6).
const MaxWalkFiles = 8000

// ShouldSkipWalkDir reports directories that must not be surveyed as project source.
// Fixture suffix is case-insensitive (`*_for_test` and `*_for_Test` both skip).
func ShouldSkipWalkDir(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case ".git", ".avatars", "avatars", "node_modules", "vendor",
		"venv", ".venv", "__pycache__", "target", "dist", "build", ".cargo",
		"claude_code_main", "caveman":
		return true
	}
	if strings.HasSuffix(n, "_for_test") {
		return true
	}
	return false
}

// WalkDirCapped walks root, skipping ShouldSkipWalkDir names and stopping after MaxWalkFiles files.
func WalkDirCapped(root string, fn fs.WalkDirFunc) error {
	seen := 0
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fn(path, d, err)
		}
		if d != nil && d.IsDir() && path != root && ShouldSkipWalkDir(d.Name()) {
			return filepath.SkipDir
		}
		if d != nil && !d.IsDir() {
			seen++
			if seen > MaxWalkFiles {
				return fs.SkipAll
			}
		}
		return fn(path, d, nil)
	})
}
