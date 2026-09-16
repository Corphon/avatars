package app

import (
	"os"
	"path/filepath"
	"strings"
)

const runtimeHomeEnv = "AVATARS_HOME"

func ResolveRuntimePath(relativePath string) string {
	rel := filepath.Clean(strings.TrimSpace(relativePath))
	if rel == "" || rel == "." {
		return ResolveRuntimeHome()
	}
	for _, root := range runtimeHomeCandidates() {
		candidate := filepath.Join(root, rel)
		if pathExists(candidate) {
			return candidate
		}
	}
	return rel
}

func ResolveRuntimeHome() string {
	for _, root := range runtimeHomeCandidates() {
		if root != "" {
			return root
		}
	}
	return "."
}

func runtimeHomeCandidates() []string {
	seen := map[string]struct{}{}
	candidates := []string{}
	add := func(path string) {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "" {
			return
		}
		key := strings.ToLower(cleaned)
		if _, ok := seen[key]; ok {
			return
		}
		if dirExists(cleaned) {
			seen[key] = struct{}{}
			candidates = append(candidates, cleaned)
		}
	}
	if env := strings.TrimSpace(os.Getenv(runtimeHomeEnv)); env != "" {
		add(env)
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, "avatars"))
		add(cwd)
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		add(exeDir)
		add(filepath.Dir(exeDir))
		add(filepath.Dir(filepath.Dir(exeDir)))
	}
	return candidates
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
