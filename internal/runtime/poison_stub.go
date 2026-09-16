package runtime

import (
	"path/filepath"
	"regexp"
	"strings"
)

// looksLikePoisonStubContent rejects comment-only / harness stubs that break
// compilers (T2): e.g. "# test.ts — stub" under .ts (TS1127), or
// "// foo — stub: LLM did not generate". Cross-language (Go/Py/Rs/JS/TS).
func looksLikePoisonStubContent(path, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "empty file"
	}
	ext := strings.ToLower(filepath.Ext(path))
	// JS/TS must never start with Python/shell `#` comments.
	if isJSorTSExt(ext) && strings.HasPrefix(trimmed, "#") {
		return "JS/TS file starts with '#' (invalid — likely poison stub)"
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "stub") {
		return ""
	}
	// Whole-file comment stub patterns from createMinimalStub / LLM.
	if poisonStubLineOnly(trimmed) && !sourceHasExecutableSignal(trimmed, ext) {
		return "comment-only stub content (poison)"
	}
	if (strings.Contains(lower, "llm did not") || strings.Contains(lower, "stub generated because")) &&
		!sourceHasExecutableSignal(trimmed, ext) {
		return "LLM placeholder stub without real code"
	}
	return ""
}

var poisonStubLineRe = regexp.MustCompile(`(?is)^(?:#|//|/\*)\s*\S+.*(?:—|--?|–)\s*stub\b`)

func poisonStubLineOnly(trimmed string) bool {
	lines := strings.Split(trimmed, "\n")
	nonEmpty := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		nonEmpty++
		if !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "//") &&
			!strings.HasPrefix(t, "/*") && !strings.HasPrefix(t, "*") {
			return false
		}
	}
	if nonEmpty == 0 {
		return false
	}
	return poisonStubLineRe.MatchString(trimmed) ||
		strings.Contains(strings.ToLower(trimmed), "— stub") ||
		strings.Contains(strings.ToLower(trimmed), "- stub")
}

func isJSorTSExt(ext string) bool {
	switch ext {
	case ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx":
		return true
	default:
		return false
	}
}

func sourceHasExecutableSignal(content, ext string) bool {
	switch ext {
	case ".go":
		return strings.Contains(content, "func ") || strings.Contains(content, "package ")
	case ".py":
		return strings.Contains(content, "def ") || strings.Contains(content, "class ") ||
			strings.Contains(content, "import ")
	case ".rs":
		return strings.Contains(content, "fn ") || strings.Contains(content, "struct ") ||
			strings.Contains(content, "mod ") || strings.Contains(content, "use ")
	default:
		return strings.Contains(content, "function") || strings.Contains(content, "=>") ||
			strings.Contains(content, "export ") || strings.Contains(content, "import ") ||
			strings.Contains(content, "const ") || strings.Contains(content, "let ") ||
			strings.Contains(content, "class ") || strings.Contains(content, "var ")
	}
}

// isJunkScaffoldPath reports paths that must never be mandatory-stubbed or
// kept on disk as "sources" (T3): dep basenames under any dir, bogus
// tsconfig.js / d.ts, bare root test.ts, etc.
func isJunkScaffoldPath(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	if isDependencyOrRuntimeBasename(base) {
		return true
	}
	switch base {
	case "d.ts", "tsconfig.js", "package.js", "typescript.js":
		return true
	case "default.py", "default.js", "default.ts":
		return true
	}
	lowerPath := strings.ToLower(path)
	if strings.HasSuffix(lowerPath, "/default/default.py") ||
		strings.HasSuffix(lowerPath, "/default/default.js") ||
		strings.HasSuffix(lowerPath, "/default/default.ts") {
		return true
	}
	// Bare root test.* (matched from prose "write test.ts") — not a suite path.
	slashCount := strings.Count(strings.TrimPrefix(path, "./"), "/")
	if slashCount == 0 {
		switch base {
		case "test.ts", "test.js", "test.mjs", "test.cjs", "test.py", "test.go", "test.rs":
			return true
		}
	}
	if strings.HasSuffix(lowerPath, "/test/test.ts") || strings.HasSuffix(lowerPath, "/test/test.js") {
		return true
	}
	if isJunkDiagnosticProbePath(path) {
		return true
	}
	return false
}

// isJunkDiagnosticProbePath reports throwaway diagnostic/probe trees that
// Builder sometimes writes to "debug" a failing test. Cross-language: the
// same junk shows up as internal/zzdiag (Go), tests/zzdiag (Py), tmp/diag
// (JS/TS/Rust). Real product packages named diagnostics/ are not matched.
func isJunkDiagnosticProbePath(path string) bool {
	slash := filepath.ToSlash(strings.ToLower(strings.TrimSpace(path)))
	if slash == "" {
		return false
	}
	for _, part := range strings.Split(slash, "/") {
		switch part {
		case "zzdiag", "zzprobe", "_diag", "__diag__", "tmpdiag":
			return true
		}
	}
	base := filepath.Base(slash)
	if strings.HasPrefix(base, "zzdiag") || strings.HasPrefix(base, "zzprobe") {
		return true
	}
	if strings.Contains(slash, "/tmp/diag/") || strings.HasPrefix(slash, "tmp/diag/") ||
		strings.Contains(slash, "/scratch/diag/") || strings.HasPrefix(slash, "scratch/diag/") {
		return true
	}
	return false
}

// isDependencyOrRuntimeBasename is the basename-only package/runtime list.
func isDependencyOrRuntimeBasename(base string) bool {
	base = strings.ToLower(strings.TrimSpace(base))
	switch base {
	case "node.js", "nodejs.js", "sql.js", "package.js", "test.js",
		"express.js", "react.js", "vue.js", "angular.js", "next.js", "nuxt.js",
		"jest.js", "mocha.js", "webpack.js", "vite.js", "rollup.js",
		"lodash.js", "jquery.js", "axios.js", "socket.js", "marked.js",
		"typescript.js", "babel.js", "eslint.js", "prettier.js",
		"tokio.rs", "serde.rs", "axum.rs", "sqlx.rs", "anyhow.rs":
		return true
	default:
		return false
	}
}

// isSourceExtForMandatoryStub limits gap-stubs to real source languages —
// never README.md / d.ts / yaml from prose extraction (T3).
func isSourceExtForMandatoryStub(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".rs", ".py", ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx":
		return true
	default:
		return false
	}
}
