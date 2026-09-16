package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/projectfiles"
)

// harnessContextLeakReason reports when source content references harness-only
// paths that must never land in user deliverables (G1; any common language).
func harnessContextLeakReason(path, content string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".py", ".rs":
	default:
		return ""
	}
	slash := strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(slash, "/llm/providers/") ||
		strings.Contains(slash, "/skills/approved/") ||
		strings.Contains(slash, "/skills/generated/") {
		return "path looks like avatars harness leak (llm/providers or skills/*) — refuse"
	}

	for i, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "#") ||
			strings.HasPrefix(trim, "/*") || strings.HasPrefix(trim, "*") {
			continue
		}
		lower := strings.ToLower(trim)
		// Go grouped imports put the path on its own line (`_ "…/llm/providers/a"`).
		looksImportish := strings.Contains(lower, "import") || strings.Contains(lower, "require") ||
			strings.Contains(lower, "from ") || strings.HasPrefix(lower, "use ") ||
			(ext == ".go" && strings.Contains(trim, `"`) && (strings.Contains(lower, "/") || strings.HasPrefix(trim, "_")))
		if !looksImportish {
			continue
		}
		if looksLikeHarnessLeakImport(lower) {
			return fmt.Sprintf("line %d: harness context leak %q — refuse", i+1, truncateForLeak(trim, 100))
		}
	}
	return ""
}

func looksLikeHarnessLeakImport(lowerLine string) bool {
	needles := []string{
		"llm/providers",
		"skills/approved",
		"skills/generated",
		"avatars/internal",
	}
	for _, n := range needles {
		if strings.Contains(lowerLine, n) {
			return true
		}
	}
	return false
}

// isAvatarsHarnessRoot reports the avatars CLI module root (not a user project
// that happens to import avatars).
func isAvatarsHarnessRoot(wd string) bool {
	wd = filepath.Clean(strings.TrimSpace(wd))
	if wd == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(wd, "go.mod"))
	if err != nil {
		return false
	}
	if !goModModuleIsExact(string(data), "avatars") {
		return false
	}
	if _, err := os.Stat(filepath.Join(wd, "internal", "runtime", "engine.go")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(wd, "cmd", "avatars")); err != nil {
		return false
	}
	return true
}

func goModModuleIsExact(goMod, want string) bool {
	for _, line := range strings.Split(goMod, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "module")) == want
	}
	return false
}

func findAvatarsHarnessRoot(path string) (string, bool) {
	dir, err := filepath.Abs(path)
	if err != nil {
		dir = filepath.Clean(path)
	}
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		if isAvatarsHarnessRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// isAvatarsHarnessTree is true when wd is the harness module root or a source
// subdirectory (internal/, cmd/, …). Fixture islands (*_for_test, avatars/,
// .avatars/) stay regular project roots so NL smoke can still purge poison.
func isAvatarsHarnessTree(wd string) bool {
	abs, err := filepath.Abs(strings.TrimSpace(wd))
	if err != nil {
		abs = filepath.Clean(wd)
	}
	root, ok := findAvatarsHarnessRoot(abs)
	if !ok {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return true
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return true
	}
	first, _, _ := strings.Cut(rel, "/")
	if projectfiles.ShouldSkipWalkDir(first) {
		return false
	}
	return true
}

func fileLivesInHarnessTree(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	return isAvatarsHarnessTree(path)
}

func (e *Engine) sweepRoot() string {
	if e != nil {
		if p := strings.TrimSpace(e.projectRoot); p != "" {
			return p
		}
	}
	wd, _ := os.Getwd()
	return wd
}

func truncateForLeak(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// missingLocalGoImportReason rejects Go files that import module-local packages
// whose directories do not exist on disk (G1 ghost packages). Content-based —
// does not require the file to already be written.
func missingLocalGoImportReason(workingDir, path, content string) string {
	if !strings.HasSuffix(strings.ToLower(path), ".go") {
		return ""
	}
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}
	modulePath, _ := parseGoMod(workingDir)
	if modulePath == "" {
		return ""
	}
	imports := extractGoImports(content)
	var missing []string
	for _, imp := range imports {
		if !strings.HasPrefix(imp, modulePath+"/") {
			continue
		}
		rel := strings.TrimPrefix(imp, modulePath+"/")
		if rel == "" {
			continue
		}
		abs := filepath.Join(workingDir, filepath.FromSlash(rel))
		if _, err := os.Stat(abs); err == nil {
			continue
		}
		if _, err := os.Stat(abs + ".go"); err == nil {
			continue
		}
		missing = append(missing, imp)
	}
	if len(missing) == 0 {
		return ""
	}
	return "local package import(s) missing on disk: " + strings.Join(missing, ", ") + " — refuse"
}
