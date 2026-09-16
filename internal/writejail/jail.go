// Package writejail confines tool writes to a project root while allowing
// well-known runtime skill directories (language-agnostic).
package writejail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Confine resolves path under projectRoot. Absolute paths outside the project
// are rejected unless they fall under an allowed extra root (AVATARS_HOME/skills).
// Cross-platform: rejects "..", parent escapes, and arbitrary absolute outsides.
func Confine(projectRoot, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path escapes sandbox: empty path")
	}
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		cleaned := filepath.Clean(path)
		slash := filepath.ToSlash(cleaned)
		if slash == ".." || strings.HasPrefix(slash, "../") || strings.Contains(slash, "/../") {
			return "", fmt.Errorf("path escapes sandbox: %s", path)
		}
		if filepath.IsAbs(cleaned) {
			abs, err := filepath.Abs(cleaned)
			if err != nil {
				return "", fmt.Errorf("path escapes sandbox: %s", path)
			}
			if IsAllowedExtraRoot(abs) {
				return abs, nil
			}
			if writejailPermissive() {
				return abs, nil
			}
			return "", fmt.Errorf("path escapes sandbox: empty project root rejects absolute path %s", path)
		}
		return cleaned, nil
	}

	absRoot, err := filepath.Abs(filepath.Clean(projectRoot))
	if err != nil {
		return "", fmt.Errorf("path escapes sandbox: bad root: %w", err)
	}

	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absRoot, candidate)
	}
	absCandidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("path escapes sandbox: %s", path)
	}

	if underRoot(absRoot, absCandidate) {
		return absCandidate, nil
	}
	// P9-1: AVATARS_HOME/skills/** is a legitimate out-of-project write target
	// (generated/approved skill markdown), not path pollution.
	if IsAllowedExtraRoot(absCandidate) {
		return absCandidate, nil
	}
	return "", fmt.Errorf("path escapes sandbox: %s", path)
}

// IsAllowedExtraRoot reports whether absPath is under a runtime-allowed
// directory outside the project (currently AVATARS_HOME/skills and nested).
func IsAllowedExtraRoot(absPath string) bool {
	absPath = filepath.Clean(absPath)
	for _, skillsRoot := range runtimeSkillsRoots() {
		if underRoot(skillsRoot, absPath) {
			return true
		}
	}
	return false
}

func runtimeSkillsRoots() []string {
	seen := map[string]bool{}
	var out []string
	add := func(home string) {
		home = strings.TrimSpace(home)
		if home == "" {
			return
		}
		abs, err := filepath.Abs(filepath.Clean(home))
		if err != nil {
			return
		}
		skills := filepath.Join(abs, "skills")
		key := strings.ToLower(skills)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, skills)
	}
	add(os.Getenv("AVATARS_HOME"))
	return out
}

func underRoot(root, absPath string) bool {
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return false
	}
	slash := filepath.ToSlash(rel)
	return slash != ".." && !strings.HasPrefix(slash, "../")
}

func writejailPermissive() bool {
	v := strings.TrimSpace(os.Getenv("AVATARS_WRITEJAIL_PERMISSIVE"))
	return v == "1" || strings.EqualFold(v, "true")
}
