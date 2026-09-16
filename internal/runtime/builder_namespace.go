package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// namespaceInfo holds the project namespace needed for correct import paths.
type namespaceInfo struct {
	ModulePath string // Go: go.mod module path
	Language   string // detected language
}

// extractNamespace scans the project root for manifest files and returns
// the namespace needed for correct import path construction.
func extractNamespace(root string) namespaceInfo {
	info := namespaceInfo{}

	// Go: go.mod → module <path>
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		info.Language = "go"
		re := regexp.MustCompile(`(?m)^module\s+(\S+)`)
		if m := re.FindStringSubmatch(string(data)); m != nil {
			info.ModulePath = strings.TrimSpace(m[1])
		}
		return info
	}

	// Python: internal imports use directory names, no manifest needed.
	if _, err := os.Stat(filepath.Join(root, "pyproject.toml")); err == nil {
		info.Language = "python"
		return info
	}
	if _, err := os.Stat(filepath.Join(root, "requirements.txt")); err == nil {
		info.Language = "python"
		return info
	}

	// Node.js: internal imports use relative paths.
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		info.Language = "javascript"
		re := regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)
		if m := re.FindStringSubmatch(string(data)); m != nil {
			info.ModulePath = m[1]
		}
		return info
	}

	// Rust: crate:: is always correct for internal imports.
	if data, err := os.ReadFile(filepath.Join(root, "Cargo.toml")); err == nil {
		info.Language = "rust"
		re := regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
		if m := re.FindStringSubmatch(string(data)); m != nil {
			info.ModulePath = m[1]
		}
		return info
	}

	return info
}

// buildNamespaceConstraint returns a system prompt fragment that constrains
// Builder's import paths to use the correct project namespace.
func buildNamespaceConstraint(root string) string {
	ns := extractNamespace(root)

	switch ns.Language {
	case "go":
		if ns.ModulePath != "" {
			return buildGoNamespaceConstraint(ns.ModulePath)
		}
		// No go.mod yet — new project. Guide Builder to create it first.
		return buildGoNewProjectConstraint(root)
	case "javascript":
		return buildJSNamespaceConstraint(ns.ModulePath)
	default:
		// Python, Rust, unknown: no namespace constraint needed.
		return ""
	}
}

func buildGoNamespaceConstraint(modulePath string) string {
	var sb strings.Builder
	sb.WriteString("\n\n=== MODULE NAMESPACE (CRITICAL FOR CORRECT IMPORTS) ===\n")
	sb.WriteString("Module path from go.mod: " + modulePath + "\n")
	sb.WriteString("ALL internal import paths MUST use this prefix:\n")
	sb.WriteString("  import \"" + modulePath + "/internal/models\"\n")
	sb.WriteString("  import \"" + modulePath + "/internal/handlers\"\n")
	sb.WriteString("  import \"" + modulePath + "/internal/storage\"\n")
	sb.WriteString("\nRULES:\n")
	sb.WriteString("1. NEVER make up a module path. Use ONLY: " + modulePath + "\n")
	sb.WriteString("2. Before writing any Go file, verify imports start with \"" + modulePath + "/\"\n")
	sb.WriteString("3. The go.mod module path is the SINGLE SOURCE OF TRUTH for all imports.\n")
	return sb.String()
}

func buildGoNewProjectConstraint(root string) string {
	projectName := filepath.Base(root)
	var sb strings.Builder
	sb.WriteString("\n\n=== NEW GO PROJECT — CREATE go.mod FIRST ===\n")
	sb.WriteString("There is NO go.mod yet. YOU MUST create it first, then use its module name.\n")
	sb.WriteString("1. Create go.mod with:  module " + projectName + "\n")
	sb.WriteString("   Use EXACTLY this module name. Do NOT invent a different one.\n")
	sb.WriteString("2. After go.mod exists, ALL imports use: \"" + projectName + "/...\"\n")
	sb.WriteString("   Example: import \"" + projectName + "/internal/models\"\n")
	sb.WriteString("3. The module name in go.mod = the import prefix. They MUST match.\n")
	return sb.String()
}

func buildJSNamespaceConstraint(packageName string) string {
	var sb strings.Builder
	sb.WriteString("\n\n=== PACKAGE NAMESPACE ===\n")
	sb.WriteString("Package name from package.json: " + packageName + "\n")
	sb.WriteString("For internal imports, use relative paths: require('./models/book')\n")
	sb.WriteString("The package name is for external consumers only.\n")
	return sb.String()
}
