package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runGoFileVerifier runs go vet on a standalone Go file.
// For files in a module (go.mod present), uses normal module mode.
// For standalone files without go.mod, disables module mode via
// GO111MODULE=off so go vet works without a module context.
func runGoFileVerifier(root string, path string) error {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return err
	}

	// Check if go.mod exists in root or any parent directory.
	hasMod := false
	for dir := root; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			hasMod = true
			break
		}
	}

	label := "go vet " + filepath.ToSlash(path)
	if !hasMod {
		// Standalone Go file without go.mod — disable module mode.
		cmd := exec.Command(goBin, "vet", path)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GO111MODULE=off")
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Println("Verifier: FAIL")
			fmt.Println("Command: " + label + " (GO111MODULE=off)")
			fmt.Println("Reason: go vet failed")
			if strings.TrimSpace(string(output)) != "" {
				fmt.Println(strings.TrimSpace(string(output)))
			}
			return err
		}
		fmt.Println("Verifier: PASS")
		fmt.Println("Command: " + label + " (GO111MODULE=off)")
		return nil
	}

	// Module mode: normal go vet.
	cmd := exec.Command(goBin, "vet", path)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: " + label)
		fmt.Println("Reason: go vet failed")
		if strings.TrimSpace(string(output)) != "" {
			fmt.Println(strings.TrimSpace(string(output)))
		}
		return err
	}
	fmt.Println("Verifier: PASS")
	fmt.Println("Command: " + label)
	return nil
}

// isCLIArgsError checks whether a non-zero exit from a script is caused
// by missing CLI arguments (argparse/commander/flag) rather than a crash.
func isCLIArgsError(output string) bool {
	lowered := strings.ToLower(output)

	// Python argparse: "usage:" + "the following arguments are required"
	if strings.Contains(lowered, "usage:") {
		if strings.Contains(lowered, "the following arguments are required") ||
			strings.Contains(lowered, "error: argument") ||
			strings.Contains(lowered, "error: unrecognized arguments") ||
			strings.Contains(lowered, "error: the following arguments") {
			return true
		}
	}
	if strings.HasPrefix(strings.TrimSpace(lowered), "usage:") {
		return true
	}

	// Node.js / commander / yargs patterns.
	if strings.Contains(lowered, "missing required argument") ||
		strings.Contains(lowered, "error: missing required") ||
		strings.Contains(lowered, "not enough non-option arguments") {
		return true
	}

	// Go flag package patterns.
	if strings.Contains(lowered, "flag provided but not defined") ||
		strings.Contains(lowered, "flag needs an argument") {
		return true
	}

	// Generic usage text -- must exclude real crash indicators.
	if strings.Contains(lowered, "usage:") || strings.Contains(lowered, "usage ") {
		if strings.Contains(lowered, "traceback") ||
			strings.Contains(lowered, "syntaxerror") ||
			strings.Contains(lowered, "referenceerror") ||
			strings.Contains(lowered, "cannot find module") {
			return false
		}
		return true
	}
	return false
}

// inferLibraryPackage detects whether a file path targets a library package
// (as opposed to a standalone executable). Returns the package name and true.
//
// Detection covers all supported languages:
//   Go:     internal/models, pkg/xxx, cmd/xxx/helper.go (non-main)
//   Python: lib/xxx, internal/xxx, src/xxx (non-main)
//   Node:   lib/xxx, src/xxx (non-index)
//   Rust:   src/xxx (non-main) — uses crate:: imports
//   Java:   src/main/java/com/name/pkg — package = full path after java/
//   C#:     src/Project/Models — namespace = directory
func inferLibraryPackage(path string) (string, bool) {
	dir := filepath.ToSlash(filepath.Dir(path))
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)
	parts := strings.Split(filepath.ToSlash(filepath.Clean(dir)), "/")
	loweredBase := strings.ToLower(base)

	// Entry-point files are executables, never library packages.
	switch loweredBase {
	case "main.go", "main.py", "main.rs", "index.js", "index.ts",
		"app.js", "app.ts", "server.js", "server.ts":
		return "", false
	}

	for i, part := range parts {
		lower := strings.ToLower(part)

		// 1. Explicit library directories: internal/, pkg/, lib/, library/, packages/, modules/
		switch lower {
		case "internal", "pkg", "lib", "library", "packages", "modules":
			if i+1 < len(parts) {
				return parts[i+1], true
			}
			return nameWithoutExt, true
		}

		// 2. Java/Maven: src/main/java/com/name/pkg/ → package is path after java/
		if lower == "java" && i >= 2 && strings.ToLower(parts[i-1]) == "main" {
			if i+1 < len(parts) {
				// Return the package as a dotted path: com.name.pkg
				pkgPath := strings.Join(parts[i+1:], ".")
				return pkgPath, true
			}
		}

		// 3. Rust/Cargo: src/xxx/ (not src/main.rs) → library module
		//    Also covers general src/ subdirectories for any language.
		if lower == "src" && i+1 < len(parts) {
			nextPart := parts[i+1]
			// src/main.rs, src/lib.rs are special — not sub-packages
			if strings.ToLower(nextPart) == "main" || strings.ToLower(nextPart) == "lib" {
				return "", false
			}
			// src/models/, src/storage/, etc. → library modules
			return strings.ToLower(nextPart), true
		}
	}

	// 4. cmd/xxx/helper.go (non-main in cmd subdirectory) → still package main
	//    cmd/ is for executables. Non-main files in cmd/ go into the same main package.
	if len(parts) >= 2 && strings.ToLower(parts[0]) == "cmd" {
		return "", false
	}

	return "", false
}

// buildSiblingTypeContext discovers existing type definitions in files that
// are siblings of the target path (same or nearby directories) and returns
// them as LLM context. This enables cross-file type sharing: when creating
// internal/storage/store.go, the LLM sees Product from internal/models/product.go
// and can import/reference it instead of redefining.
//
// Claude Code pattern: "read related files first" — the LLM builds a mental
// model of the project by reading existing files before creating new ones.
func buildSiblingTypeContext(targetPath string) string {
	dir := filepath.Dir(targetPath)
	if dir == "." {
		return ""
	}

	// Find the project root (where go.mod, package.json, or pyproject.toml lives).
	projectRoot := findProjectRoot(dir)
	if projectRoot == "" {
		projectRoot = dir
	}

	// I11a: Inject module path so the LLM writes correct import statements.
	// Without this, storage.go imports "github.com/example/project/internal/models"
	// instead of the real module path like "inventory/internal/models".
	var contextBuilder strings.Builder
	if moduleInfo := readModuleInfo(projectRoot); moduleInfo != "" {
		contextBuilder.WriteString("# Project module information (use this exact import path):\n")
		contextBuilder.WriteString(moduleInfo)
		contextBuilder.WriteString("\n\n")
	}

	// Collect type definitions from sibling and nearby packages.
	found := 0

	// First, check the parent directory for sibling packages.
	parentDir := filepath.Dir(dir)
	if parentDir != "" && parentDir != "." {
		entries, err := os.ReadDir(parentDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
					continue
				}
				siblingDir := filepath.Join(parentDir, entry.Name())
				if siblingDir == dir {
					continue // skip self
				}
				// Check for type-defining files in each sibling package.
				typeFiles := findTypeFiles(siblingDir)
				for _, tf := range typeFiles {
					if found >= 3 {
						break
					}
					content, err := os.ReadFile(tf)
					if err != nil || len(content) > 8000 {
						continue
					}
					if found == 0 {
						contextBuilder.WriteString("# Existing types from sibling packages (reference these, don't redefine):\n\n")
					}
					contextBuilder.WriteString(fmt.Sprintf("## %s\n", filepath.ToSlash(tf)))
					// Extract only type/interface/struct declarations, not full file.
					extracted := extractTypeDeclarations(string(content))
					if extracted != "" {
						contextBuilder.WriteString(extracted)
					} else {
						contextBuilder.WriteString(string(content))
					}
					contextBuilder.WriteString("\n\n")
					found++
				}
			}
		}
	}

	result := contextBuilder.String()
	if len(result) > 3000 {
		result = result[:3000]
		if lastNL := strings.LastIndex(result, "\n"); lastNL > 2500 {
			result = result[:lastNL]
		}
		result += "\n[sibling type context truncated]"
	}
	return result
}

// findProjectRoot walks up from dir looking for a project marker file.
func findProjectRoot(dir string) string {
	markers := []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml", "pom.xml"}
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(d, m)); err == nil {
				return d
			}
		}
	}
	return ""
}

// findTypeFiles returns Go/Python/TypeScript files in dir that likely contain
// type definitions (not main executables or tests).
func findTypeFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		// Skip test files and main entrypoints.
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") ||
			strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".spec.js") ||
			name == "main.go" || name == "main.py" || name == "index.js" {
			continue
		}
		switch ext {
		case ".go", ".py", ".ts", ".js", ".mjs", ".rs":
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files
}

// extractTypeDeclarations extracts type/struct/interface/class declarations
// from source code. Returns only the type signatures, not implementation bodies.
func extractTypeDeclarations(source string) string {
	lines := strings.Split(source, "\n")
	var extracted []string
	inType := false
	braceDepth := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Go: type Xxx struct { ... }
		if strings.HasPrefix(trimmed, "type ") && (strings.Contains(trimmed, "struct") || strings.Contains(trimmed, "interface")) {
			extracted = append(extracted, trimmed)
			if strings.Contains(trimmed, "{") && !strings.Contains(trimmed, "}") {
				inType = true
				braceDepth = 1
			}
			continue
		}

		// Python: class Xxx:
		if strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":") {
			extracted = append(extracted, trimmed)
			continue
		}

		// TypeScript/JavaScript: interface Xxx { or type Xxx =
		if (strings.HasPrefix(trimmed, "interface ") || strings.HasPrefix(trimmed, "type ")) && strings.Contains(trimmed, "{") {
			extracted = append(extracted, trimmed)
			if !strings.Contains(trimmed, "}") {
				inType = true
				braceDepth = 1
			}
			continue
		}

		if inType {
			extracted = append(extracted, line)
			braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
			if braceDepth <= 0 {
				inType = false
			}
		}
	}

	if len(extracted) == 0 {
		return ""
	}
	return strings.Join(extracted, "\n")
}

// readModuleInfo reads the project module/package identifier from standard
// manifest files. Returns a string the LLM can use for correct import paths.
func readModuleInfo(projectRoot string) string {
	// Go: go.mod contains "module <path>"
	if data, err := os.ReadFile(filepath.Join(projectRoot, "go.mod")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "module ") {
				modulePath := strings.TrimSpace(strings.TrimPrefix(trimmed, "module "))
				return fmt.Sprintf("Go module: %s\nImport example: import \"%s/internal/models\"", modulePath, modulePath)
			}
		}
	}
	// Node.js: package.json contains "name"
	if data, err := os.ReadFile(filepath.Join(projectRoot, "package.json")); err == nil {
		content := string(data)
		if idx := strings.Index(content, "\"name\""); idx >= 0 {
			rest := content[idx+6:]
			if start := strings.Index(rest, "\""); start >= 0 {
				if end := strings.Index(rest[start+1:], "\""); end >= 0 {
					name := rest[start+1 : start+1+end]
					return fmt.Sprintf("Node.js package: %s\nRequire example: const {%s} = require('%s')", name, "{ Product }", name)
				}
			}
		}
	}
	// Python: pyproject.toml or setup.py
	if data, err := os.ReadFile(filepath.Join(projectRoot, "pyproject.toml")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "name = ") {
				name := strings.Trim(trimmed[7:], "\" '")
				return fmt.Sprintf("Python package: %s\nImport example: from %s.models import Product", name, name)
			}
		}
	}
	return ""
}

// readGoModulePath reads the Go module path from go.mod in the current
// working directory. Returns "" if go.mod is not found or unreadable.
func readGoModulePath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(cwd, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "module "))
		}
	}
	return ""
}
