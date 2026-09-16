package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

// buildProjectInventory scans the project root for existing Go packages and
// returns a structured inventory for injection into the Builder's system prompt.
//
// This prevents I18 (duplicate packages like model/ + internal/models/):
// Builder knows which packages already exist and where they are, so it doesn't
// create duplicate directories for the same concern.
func buildProjectInventory(root string) string {
	var sb strings.Builder
	sb.WriteString("=== EXISTING PROJECT STRUCTURE ===\n")
	sb.WriteString("The following packages already exist. Do NOT create duplicate packages\n")
	sb.WriteString("for the same concern — use the existing ones.\n\n")

	packages := scanGoPackages(root)
	if len(packages) == 0 {
		sb.WriteString("(No existing Go packages found — this is a new project)\n")
		return sb.String()
	}

	// Group by directory.
	dirPackages := make(map[string][]string) // dir -> package names
	for _, pkg := range packages {
		dir := filepath.Dir(pkg.path)
		dirPackages[dir] = append(dirPackages[dir], pkg.name)
	}

	sb.WriteString("Existing Go packages:\n")
	for dir, names := range dirPackages {
		unique := dedupeStrings(names)
		displayDir := dir
		if displayDir == "." {
			displayDir = "(root)"
		}
		sb.WriteString("  " + displayDir + "/   → package " + strings.Join(unique, ", ") + "\n")
	}

	// List existing directories to guide where new files should go.
	sb.WriteString("\nExisting directories (place new files here when possible):\n")
	dirs := listGoDirs(root)
	for _, d := range dirs {
		sb.WriteString("  " + d + "/\n")
	}

	sb.WriteString("\nRULES:\n")
	sb.WriteString("- If a package for X already exists (e.g. internal/handler/), EDIT those files.\n")
	sb.WriteString("- Do NOT create a new directory for the same concern (e.g. handler/).\n")
	sb.WriteString("- Use 'go build ./...' to verify after changes.\n")
	sb.WriteString("- Import paths must use the module path from go.mod.\n")

	return sb.String()
}

type goPkgInfo struct {
	path string // relative file path
	name string // package name
}

// scanGoPackages finds all Go source files and extracts their package declarations.
func scanGoPackages(root string) []goPkgInfo {
	var pkgs []goPkgInfo
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDirForInventory(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// Extract package declaration from first non-comment line.
		lines := strings.SplitN(string(data), "\n", 5)
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if strings.HasPrefix(trimmed, "package ") {
				pkgName := strings.TrimPrefix(trimmed, "package ")
				pkgName = strings.TrimSpace(pkgName)
				// Remove trailing comment
				if idx := strings.Index(pkgName, "//"); idx >= 0 {
					pkgName = strings.TrimSpace(pkgName[:idx])
				}
				rel, _ := filepath.Rel(root, path)
				pkgs = append(pkgs, goPkgInfo{path: rel, name: pkgName})
				break
			}
			break
		}
		return nil
	})
	return pkgs
}

// listGoDirs returns directories containing Go files.
func listGoDirs(root string) []string {
	dirs := make(map[string]bool)
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDirForInventory(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			dir := filepath.Dir(path)
			rel, _ := filepath.Rel(root, dir)
			if rel != "." {
				dirs[rel] = true
			}
		}
		return nil
	})
	var result []string
	for d := range dirs {
		result = append(result, d)
	}
	return result
}

func shouldSkipDirForInventory(name string) bool {
	switch name {
	case ".git", ".avatars", "avatars", "vendor", "node_modules", "__pycache__":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func dedupeStrings(items []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}
