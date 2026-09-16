package runtime

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	goTestFuncRe     = regexp.MustCompile(`(?m)^\s*func\s+Test[A-Z_]\w*\s*\(`)
	pyTestFuncRe     = regexp.MustCompile(`(?m)^\s*def\s+test_\w+\s*\(`)
	jsTestCallRe     = regexp.MustCompile(`(?m)^\s*(?:it|test|describe)\s*\(`)
	rustTestAttrRe   = regexp.MustCompile(`(?m)^\s*#\s*\[\s*test\s*\]`)
	genericTestFnRe  = regexp.MustCompile(`(?m)^\s*(?:async\s+)?(?:function|const|let|var)\s+test[A-Z_]\w*`)
)

// isTestSourcePath reports common unit-test file shapes across languages (F68).
func isTestSourcePath(path string) bool {
	path = filepath.ToSlash(strings.ToLower(strings.TrimSpace(path)))
	if path == "" {
		return false
	}
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasSuffix(base, "_test.rs"),
		strings.HasSuffix(base, ".test.js"),
		strings.HasSuffix(base, ".test.jsx"),
		strings.HasSuffix(base, ".test.ts"),
		strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".spec.js"),
		strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".spec.tsx"),
		strings.HasPrefix(base, "test_"):
		return true
	}
	if strings.Contains(path, "/tests/") || strings.Contains(path, "/test/") ||
		strings.Contains(path, "/__tests__/") {
		switch {
		case strings.HasSuffix(base, ".py"), strings.HasSuffix(base, ".js"),
			strings.HasSuffix(base, ".jsx"), strings.HasSuffix(base, ".ts"),
			strings.HasSuffix(base, ".tsx"), strings.HasSuffix(base, ".rs"),
			strings.HasSuffix(base, ".go"):
			return true
		}
	}
	return false
}

// countTestSymbols counts language-agnostic test entrypoints in source text.
func countTestSymbols(path, content string) int {
	lowerPath := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lowerPath, ".go"):
		return len(goTestFuncRe.FindAllStringIndex(content, -1))
	case strings.HasSuffix(lowerPath, ".py"):
		return len(pyTestFuncRe.FindAllStringIndex(content, -1))
	case strings.HasSuffix(lowerPath, ".rs"):
		return len(rustTestAttrRe.FindAllStringIndex(content, -1))
	case strings.HasSuffix(lowerPath, ".js"), strings.HasSuffix(lowerPath, ".jsx"),
		strings.HasSuffix(lowerPath, ".ts"), strings.HasSuffix(lowerPath, ".tsx"),
		strings.HasSuffix(lowerPath, ".mjs"), strings.HasSuffix(lowerPath, ".cjs"):
		n := len(jsTestCallRe.FindAllStringIndex(content, -1))
		n += len(genericTestFnRe.FindAllStringIndex(content, -1))
		return n
	default:
		n := len(goTestFuncRe.FindAllStringIndex(content, -1))
		n += len(pyTestFuncRe.FindAllStringIndex(content, -1))
		n += len(jsTestCallRe.FindAllStringIndex(content, -1))
		n += len(rustTestAttrRe.FindAllStringIndex(content, -1))
		return n
	}
}

// testCoverageCollapse reports F68: proposed "fix" guts the suite to pass compile
// (e.g. 13 tests → 2) while claiming health green.
func testCoverageCollapse(path, existing, proposed string) bool {
	if !isTestSourcePath(path) {
		return false
	}
	existN := countTestSymbols(path, existing)
	propN := countTestSymbols(path, proposed)
	if existN >= 3 && propN < existN && propN <= existN/2 {
		return true
	}
	existLines := nonEmptyLineCount(existing)
	propLines := nonEmptyLineCount(proposed)
	if existLines >= 80 && propLines > 0 && propLines*10 < existLines*6 { // <60%
		if existN >= 2 && propN < existN {
			return true
		}
	}
	return false
}

func nonEmptyLineCount(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
