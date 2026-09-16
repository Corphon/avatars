package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LanguageAdversarialAuditor performs language-specific adversarial checks
// on generated files. C1: Each language gets its own duplicate-type detection
// and dependency validation.
type LanguageAdversarialAuditor interface {
	// Name returns the language identifier (e.g. "go", "python", "rust").
	Name() string
	// FindDuplicateDeclarations scans generated files for duplicate
	// type/class/struct declarations that would cause compilation errors.
	FindDuplicateDeclarations(projectRoot string, generatedFiles []string) []AdversarialFinding
	// CheckDependencies validates that external dependencies in generated
	// files are declared in the project's dependency manifest.
	CheckDependencies(projectRoot string, generatedFiles []string) []AdversarialFinding
}

// pythonAuditor implements LanguageAdversarialAuditor for Python.
type pythonAuditor struct{}

func (pythonAuditor) Name() string { return "python" }

func (pythonAuditor) FindDuplicateDeclarations(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding
	genDecls := make(map[string][]string)
	classRe := regexp.MustCompile(`(?m)^class\s+(\w+)\s*[(:]`)

	for _, filePath := range generatedFiles {
		if !strings.HasSuffix(filePath, ".py") {
			continue
		}
		fullPath := resolvePath(projectRoot, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		for _, match := range classRe.FindAllStringSubmatch(string(content), -1) {
			if len(match) >= 2 {
				genDecls[match[1]] = append(genDecls[match[1]], filePath)
			}
		}
	}
	for name, files := range genDecls {
		if len(files) > 1 {
			findings = append(findings, AdversarialFinding{
				Kind: "duplicate_type", File: files[0],
				Detail:   fmt.Sprintf("class %s declared in %d generated files: %s", name, len(files), strings.Join(files, ", ")),
				Evidence: "Multiple definitions will cause ImportError",
			})
		}
	}
	return findings
}

func (pythonAuditor) CheckDependencies(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding
	// Scan generated .py files for imports not in requirements.txt.
	knownDeps := parseRequirementsTxt(projectRoot)
	if len(knownDeps) == 0 {
		return nil // no requirements.txt, can't validate
	}
	importRe := regexp.MustCompile(`(?m)^(?:from|import)\s+(\w+)`)
	for _, filePath := range generatedFiles {
		if !strings.HasSuffix(filePath, ".py") {
			continue
		}
		fullPath := resolvePath(projectRoot, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		for _, match := range importRe.FindAllStringSubmatch(string(content), -1) {
			if len(match) >= 2 {
				pkg := match[1]
				if !isStdlibPython(pkg) && !knownDeps[pkg] {
					findings = append(findings, AdversarialFinding{
						Kind: "external_dependency", File: filePath,
						Detail:   fmt.Sprintf("imports %s which is not in requirements.txt", pkg),
						Evidence: "Missing dependency will cause ImportError at runtime",
					})
				}
			}
		}
	}
	return findings
}

// rustAuditor implements LanguageAdversarialAuditor for Rust.
type rustAuditor struct{}

func (rustAuditor) Name() string { return "rust" }

func (rustAuditor) FindDuplicateDeclarations(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding
	genDecls := make(map[string][]string)
	structRe := regexp.MustCompile(`(?m)^(?:pub\s+)?struct\s+(\w+)`)
	traitRe := regexp.MustCompile(`(?m)^(?:pub\s+)?trait\s+(\w+)`)

	for _, filePath := range generatedFiles {
		if !strings.HasSuffix(filePath, ".rs") {
			continue
		}
		fullPath := resolvePath(projectRoot, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		text := string(content)
		for _, re := range []*regexp.Regexp{structRe, traitRe} {
			for _, match := range re.FindAllStringSubmatch(text, -1) {
				if len(match) >= 2 {
					genDecls[match[1]] = append(genDecls[match[1]], filePath)
				}
			}
		}
	}
	for name, files := range genDecls {
		if len(files) > 1 {
			findings = append(findings, AdversarialFinding{
				Kind: "duplicate_type", File: files[0],
				Detail:   fmt.Sprintf("type %s declared in %d generated files: %s", name, len(files), strings.Join(files, ", ")),
				Evidence: "Multiple definitions will cause compilation error (E0428)",
			})
		}
	}
	return findings
}

func (rustAuditor) CheckDependencies(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding
	knownDeps := parseCargoTomlDeps(projectRoot)
	if len(knownDeps) == 0 {
		return nil
	}
	useRe := regexp.MustCompile(`(?m)^use\s+(\w+)`)
	externRe := regexp.MustCompile(`(?m)^extern\s+crate\s+(\w+)`)
	for _, filePath := range generatedFiles {
		if !strings.HasSuffix(filePath, ".rs") {
			continue
		}
		fullPath := resolvePath(projectRoot, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		text := string(content)
		for _, re := range []*regexp.Regexp{useRe, externRe} {
			for _, match := range re.FindAllStringSubmatch(text, -1) {
				if len(match) >= 2 {
					pkg := match[1]
					if !knownDeps[pkg] {
						findings = append(findings, AdversarialFinding{
							Kind: "external_dependency", File: filePath,
							Detail:   fmt.Sprintf("uses crate %s which is not in Cargo.toml dependencies", pkg),
							Evidence: "Missing dependency will cause compilation error",
						})
					}
				}
			}
		}
	}
	return findings
}

// tsAuditor implements LanguageAdversarialAuditor for TypeScript/JavaScript.
type tsAuditor struct{}

func (tsAuditor) Name() string { return "typescript" }

func (tsAuditor) FindDuplicateDeclarations(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding
	genDecls := make(map[string][]string)
	classRe := regexp.MustCompile(`(?m)^(?:export\s+)?(?:abstract\s+)?class\s+(\w+)`)
	ifaceRe := regexp.MustCompile(`(?m)^(?:export\s+)?interface\s+(\w+)`)

	for _, filePath := range generatedFiles {
		ext := strings.ToLower(filepath.Ext(filePath))
		if ext != ".ts" && ext != ".tsx" && ext != ".js" {
			continue
		}
		fullPath := resolvePath(projectRoot, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		text := string(content)
		for _, re := range []*regexp.Regexp{classRe, ifaceRe} {
			for _, match := range re.FindAllStringSubmatch(text, -1) {
				if len(match) >= 2 {
					genDecls[match[1]] = append(genDecls[match[1]], filePath)
				}
			}
		}
	}
	for name, files := range genDecls {
		if len(files) > 1 {
			findings = append(findings, AdversarialFinding{
				Kind: "duplicate_type", File: files[0],
				Detail:   fmt.Sprintf("type %s declared in %d generated files: %s", name, len(files), strings.Join(files, ", ")),
				Evidence: "Multiple definitions will cause compilation error (tsc)",
			})
		}
	}
	return findings
}

func (tsAuditor) CheckDependencies(projectRoot string, generatedFiles []string) []AdversarialFinding {
	var findings []AdversarialFinding
	knownDeps := parsePackageJSONDeps(projectRoot)
	if len(knownDeps) == 0 {
		return nil
	}
	importRe := regexp.MustCompile(`(?m)(?:import|require)\s*\(?['"]([^'"]+)['"]\)?`)
	for _, filePath := range generatedFiles {
		ext := strings.ToLower(filepath.Ext(filePath))
		if ext != ".ts" && ext != ".tsx" && ext != ".js" {
			continue
		}
		fullPath := resolvePath(projectRoot, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		for _, match := range importRe.FindAllStringSubmatch(string(content), -1) {
			if len(match) >= 2 {
				pkg := match[1]
				// Skip relative imports and node builtins.
				if strings.HasPrefix(pkg, ".") || strings.HasPrefix(pkg, "/") {
					continue
				}
				pkgName := extractNpmPackageName(pkg)
				if pkgName != "" && !knownDeps[pkgName] {
					findings = append(findings, AdversarialFinding{
						Kind: "external_dependency", File: filePath,
						Detail:   fmt.Sprintf("imports %s which is not in package.json dependencies", pkg),
						Evidence: "Missing dependency will cause runtime error or tsc failure",
					})
				}
			}
		}
	}
	return findings
}

// MultiLanguageAdversarialAudit runs the existing Go audit plus all
// registered language auditors. C1: Extends the original Go-only audit.
func MultiLanguageAdversarialAudit(projectRoot string, generatedFiles []string) []AdversarialFinding {
	// Original Go checks (always run).
	findings := AdversarialAudit(projectRoot, generatedFiles)

	// Determine which non-Go files are present and run language-specific auditors.
	auditors := []LanguageAdversarialAuditor{pythonAuditor{}, rustAuditor{}, tsAuditor{}}
	for _, auditor := range auditors {
		findings = append(findings, auditor.FindDuplicateDeclarations(projectRoot, generatedFiles)...)
		findings = append(findings, auditor.CheckDependencies(projectRoot, generatedFiles)...)
	}
	return findings
}

// --- Helpers ---

func resolvePath(projectRoot, filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(projectRoot, filePath)
}

func parseRequirementsTxt(projectRoot string) map[string]bool {
	deps := make(map[string]bool)
	data, err := os.ReadFile(filepath.Join(projectRoot, "requirements.txt"))
	if err != nil {
		return deps
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		// Extract package name before version specifier.
		pkg := strings.ToLower(strings.SplitN(line, "==", 2)[0])
		pkg = strings.SplitN(pkg, ">=", 2)[0]
		pkg = strings.SplitN(pkg, "~=", 2)[0]
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			deps[pkg] = true
		}
	}
	return deps
}

func parseCargoTomlDeps(projectRoot string) map[string]bool {
	deps := make(map[string]bool)
	data, err := os.ReadFile(filepath.Join(projectRoot, "Cargo.toml"))
	if err != nil {
		return deps
	}
	inDeps := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[dependencies]" {
			inDeps = true
			continue
		}
		if inDeps && strings.HasPrefix(trimmed, "[") {
			break
		}
		if inDeps && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			name := strings.SplitN(trimmed, "=", 2)[0]
			name = strings.TrimSpace(name)
			if name != "" {
				deps[name] = true
			}
		}
	}
	return deps
}

func parsePackageJSONDeps(projectRoot string) map[string]bool {
	deps := make(map[string]bool)
	data, err := os.ReadFile(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		return deps
	}
	// Simple heuristic: extract "dependencies" and "devDependencies" keys.
	for _, section := range []string{`"dependencies"`, `"devDependencies"`} {
		idx := strings.Index(string(data), section)
		if idx < 0 {
			continue
		}
		// Find the opening { and extract keys until closing }.
		rest := string(data)[idx+len(section):]
		start := strings.Index(rest, "{")
		if start < 0 {
			continue
		}
		rest = rest[start+1:]
		depth := 1
		for depth > 0 && len(rest) > 0 {
			lineEnd := strings.IndexAny(rest, "\n}")
			if lineEnd < 0 {
				break
			}
			line := strings.TrimSpace(rest[:lineEnd])
			rest = rest[lineEnd+1:]
			if strings.Contains(line, "{") {
				depth++
			}
			if strings.Contains(line, "}") {
				depth--
			}
			// Extract key: "pkg-name"
			if strings.HasPrefix(line, `"`) {
				keyEnd := strings.Index(line[1:], `"`)
				if keyEnd > 0 {
					deps[line[1:keyEnd+1]] = true
				}
			}
		}
	}
	return deps
}

func extractNpmPackageName(importPath string) string {
	// Scoped packages: @scope/name → name
	// Regular packages: lodash → lodash, lodash/fp → lodash
	path := strings.TrimPrefix(importPath, "@")
	if idx := strings.Index(path, "/"); idx > 0 {
		// For scoped packages (@scope/name), return the full scope+name.
		if strings.HasPrefix(importPath, "@") {
			return importPath[:idx+1+strings.Index(importPath[idx+1:], "/")+1]
		}
		return path[:idx]
	}
	return path
}

func isStdlibPython(pkg string) bool {
	stdlib := map[string]bool{
		"os": true, "sys": true, "json": true, "re": true, "math": true,
		"datetime": true, "collections": true, "itertools": true, "functools": true,
		"pathlib": true, "typing": true, "io": true, "csv": true, "sqlite3": true,
		"hashlib": true, "base64": true, "uuid": true, "random": true, "string": true,
		"subprocess": true, "shutil": true, "tempfile": true, "logging": true,
		"argparse": true, "unittest": true, "abc": true, "enum": true, "dataclasses": true,
	}
	return stdlib[strings.ToLower(pkg)]
}
