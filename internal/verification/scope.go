package verification

import (
	"path/filepath"
	"strings"
)

// langFamily is a coarse toolchain family for write-triggered verification.
type langFamily string

const (
	familyGo     langFamily = "go"
	familyPython langFamily = "python"
	familyJS     langFamily = "javascript"
	familyRust   langFamily = "rust"
	familyJava   langFamily = "java"
	familyC      langFamily = "c"
	familyCpp    langFamily = "cpp"
	familyShell  langFamily = "shell"
	familySQL    langFamily = "sql"
	familyConfig langFamily = "config"
	familyHTML   langFamily = "html"
	familyCSS    langFamily = "css"
)

// ScopeToChangedFiles drops toolchain checks that do not apply to the files
// just mutated. Docs/skills-only edits yield an empty check list (vacuous PASS).
// When changedFiles is empty, checks are left unchanged (full-suite / race runs).
func (r *Runner) ScopeToChangedFiles(changedFiles []string) {
	if r == nil || len(changedFiles) == 0 {
		return
	}
	r.checks = FilterChecksForChangedFiles(r.checks, changedFiles)
}

// FilterChecksForChangedFiles keeps only checks whose language family intersects
// the families implied by changed file paths. Non-source edits (skills, markdown
// docs, etc.) match no toolchain family → empty slice.
func FilterChecksForChangedFiles(checks []CommandCheck, changedFiles []string) []CommandCheck {
	if len(changedFiles) == 0 {
		return checks
	}
	fams := familiesFromPaths(changedFiles)
	if len(fams) == 0 {
		return nil
	}
	out := make([]CommandCheck, 0, len(checks))
	for _, c := range checks {
		cf := familyForCheck(c)
		if cf == "" {
			// Unknown / agnostic — keep only if we have a concrete source family.
			continue
		}
		if fams[cf] {
			out = append(out, c)
		}
	}
	return out
}

func familiesFromPaths(paths []string) map[langFamily]bool {
	out := map[langFamily]bool{}
	for _, p := range paths {
		for _, f := range familiesForPath(p) {
			out[f] = true
		}
	}
	return out
}

func familiesForPath(path string) []langFamily {
	slash := filepath.ToSlash(strings.TrimSpace(path))
	lower := strings.ToLower(slash)
	base := strings.ToLower(filepath.Base(slash))

	// Skills / workflow docs / plain notes: not a language delivery surface.
	if isNonSourceVerificationPath(lower, base) {
		return nil
	}

	switch base {
	case "go.mod", "go.sum":
		return []langFamily{familyGo}
	case "cargo.toml", "cargo.lock":
		return []langFamily{familyRust}
	case "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "tsconfig.json":
		return []langFamily{familyJS}
	case "requirements.txt", "pyproject.toml", "setup.py", "pipfile", "poetry.lock":
		return []langFamily{familyPython}
	case "pom.xml", "build.gradle", "build.gradle.kts":
		return []langFamily{familyJava}
	}

	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".go":
		return []langFamily{familyGo}
	case ".py", ".pyi", ".pyx":
		return []langFamily{familyPython}
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return []langFamily{familyJS}
	case ".rs":
		return []langFamily{familyRust}
	case ".java":
		return []langFamily{familyJava}
	case ".c", ".h":
		return []langFamily{familyC}
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx":
		return []langFamily{familyCpp}
	case ".sh", ".bash", ".ps1":
		return []langFamily{familyShell}
	case ".sql":
		return []langFamily{familySQL}
	case ".yaml", ".yml", ".toml", ".json", ".ini", ".cfg", ".env":
		return []langFamily{familyConfig}
	case ".html", ".htm":
		return []langFamily{familyHTML}
	case ".css":
		return []langFamily{familyCSS}
	default:
		return nil
	}
}

func isNonSourceVerificationPath(lowerSlash, base string) bool {
	if strings.Contains(lowerSlash, "/skills/") || strings.HasPrefix(lowerSlash, "skills/") {
		return true
	}
	if strings.Contains(lowerSlash, "/docs/workflow/") || strings.HasPrefix(lowerSlash, "docs/workflow/") {
		return true
	}
	switch base {
	case "avatars_todo.md", "avatars_plan.md", "process_record.md", "process_record.yaml",
		"readme.md", "coding_plan.md", "architecture.md", "changelog.md":
		return true
	}
	// Bare markdown / text without a source-family extension → skip toolchains.
	ext := filepath.Ext(base)
	switch ext {
	case ".md", ".txt", ".rst":
		return true
	}
	return false
}

func familyForCheck(c CommandCheck) langFamily {
	name := strings.ToLower(strings.TrimSpace(c.Name))
	cmd := strings.ToLower(strings.Join(c.Command, " "))
	joined := name + " " + cmd
	switch {
	case strings.HasPrefix(name, "go ") || strings.Contains(joined, "go build") ||
		strings.Contains(joined, "go test") || strings.Contains(joined, "go vet") ||
		strings.Contains(joined, "go fmt"):
		return familyGo
	case strings.Contains(name, "python") || strings.Contains(joined, "pytest") ||
		strings.Contains(joined, "py_compile"):
		return familyPython
	case strings.Contains(name, "javascript") || strings.Contains(name, "typescript") ||
		strings.Contains(name, "js ") || strings.HasPrefix(name, "js ") ||
		strings.Contains(name, "ts ") || strings.HasPrefix(name, "ts ") ||
		strings.Contains(joined, "eslint") || strings.Contains(joined, "tsc"):
		return familyJS
	case strings.Contains(name, "rust") || strings.Contains(joined, "cargo") ||
		strings.Contains(joined, "rustc"):
		return familyRust
	case strings.Contains(name, "java") || strings.Contains(joined, "javac"):
		return familyJava
	case strings.Contains(name, "c++") || strings.Contains(joined, "g++"):
		return familyCpp
	case name == "c syntax" || strings.HasPrefix(name, "c syntax"):
		return familyC
	case strings.Contains(name, "sql") || strings.Contains(joined, "sqlite3"):
		return familySQL
	case strings.Contains(name, "shell") || strings.Contains(joined, "bash -n") ||
		strings.Contains(name, "ps1"):
		return familyShell
	case strings.Contains(name, "html"):
		return familyHTML
	case strings.Contains(name, "css"):
		return familyCSS
	case strings.Contains(name, "yaml") || strings.Contains(name, "toml") ||
		strings.Contains(name, "config"):
		return familyConfig
	}
	return ""
}
