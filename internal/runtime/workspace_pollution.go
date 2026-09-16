package runtime

import (
	"path/filepath"
	"regexp"
	"strings"
)

// looksLikeTestWorkspacePollution reports paths that are usually accidental
// artifacts from go test / HTTP fixtures writing into the project root (F15),
// not intentional delivery (cmd/, internal/, testdata/, docs/).
func looksLikeTestWorkspacePollution(path string) bool {
	p := filepath.ToSlash(strings.TrimSpace(path))
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return false
	}
	if isHarnessNoisePath(p) {
		return true
	}
	lower := strings.ToLower(p)
	// Explicit delivery / fixture roots — never treat as pollution.
	if strings.HasPrefix(lower, "testdata/") ||
		strings.HasPrefix(lower, "cmd/") ||
		strings.HasPrefix(lower, "internal/") ||
		strings.HasPrefix(lower, "pkg/") ||
		strings.HasPrefix(lower, "app/") ||
		strings.HasPrefix(lower, "src/") ||
		strings.HasPrefix(lower, "docs/") ||
		strings.HasPrefix(lower, ".avatars/") {
		return false
	}
	base := filepath.Base(lower)
	// Port-like first segment: 8080/p/hello.md
	if portPathRe.MatchString(lower) {
		return true
	}
	// Bare HTTP route dumps at repo root: p/hello.md, p/missing.md
	if strings.HasPrefix(lower, "p/") {
		return true
	}
	// Loose markdown / junk at repo root (not under docs/testdata).
	if !strings.Contains(lower, "/") {
		switch {
		case strings.HasSuffix(lower, ".md") && base != "readme.md" && base != "architecture.md" && base != "changelog.md":
			return true
		case base == "notmarkdown.txt" || base == "secret.md":
			return true
		}
	}
	// Nested scrap outside package roots: subdir/nested.md at root
	if strings.HasSuffix(lower, ".md") && strings.Count(lower, "/") == 1 {
		first := strings.SplitN(lower, "/", 2)[0]
		switch first {
		case "subdir", "content", "static", "public", "www":
			return true
		}
	}
	return false
}

var portPathRe = regexp.MustCompile(`^[0-9]{2,5}/`)

// isHarnessNoisePath drops avatars test-runner logs and watch files that
// land in the workspace cwd (F89). Cross-language: the same log names are
// used regardless of whether the project is Go/Python/JS/Rust.
func isHarnessNoisePath(path string) bool {
	base := strings.ToLower(filepath.Base(filepath.ToSlash(strings.TrimSpace(path))))
	if base == "" {
		return false
	}
	if strings.HasPrefix(base, "avatars_test_") && strings.HasSuffix(base, ".log") {
		return true
	}
	if strings.HasPrefix(base, "avatars_r") && strings.HasSuffix(base, "_watch.txt") {
		return true
	}
	if strings.HasPrefix(base, "avatars_") && strings.HasSuffix(base, ".err.log") {
		return true
	}
	return false
}

// filterDeliveryChangedFiles drops test-pollution paths so auto-mark / footer
// stats do not treat HTTP fixture scrap as product delivery (F15).
func filterDeliveryChangedFiles(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if looksLikeTestWorkspacePollution(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}
