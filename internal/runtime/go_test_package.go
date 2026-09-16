package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var goPackageClauseRe = regexp.MustCompile(`(?m)^(\s*package\s+)([A-Za-z_][\w]*)`)

// coerceGoLibTestPackage fixes a common Go-only failure (F63): library-dir
// *_test.go written as `package main`, which collides with the lib package
// ("found packages X and main"). Rewrites to package <dirname>.
// Leaves cmd/ and true root package-main tests alone.
func coerceGoLibTestPackage(path, content string) (string, bool) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if !strings.HasSuffix(strings.ToLower(path), "_test.go") {
		return content, false
	}
	pkg := parseGoPackageClause(content)
	if pkg != "main" {
		return content, false
	}
	slash := strings.ToLower(path)
	if strings.Contains(slash, "/cmd/") || strings.HasPrefix(slash, "cmd/") {
		return content, false
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." || dir == "" || dir == "/" {
		// Root *_test.go with package main is OK for package-main modules.
		return content, false
	}
	base := filepath.Base(dir)
	if base == "" || base == "." || base == "internal" || base == "pkg" || base == "testdata" {
		return content, false
	}
	want := sanitizeGoPackageIdent(base)
	if want == "" || want == "main" {
		return content, false
	}
	fixed, ok := replaceGoPackageClause(content, want)
	return fixed, ok
}

func sanitizeGoPackageIdent(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		case r == '-':
			// drop hyphens from directory segment
		default:
		}
	}
	out := strings.ToLower(b.String())
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "p" + out
	}
	return out
}

func replaceGoPackageClause(content, newPkg string) (string, bool) {
	loc := goPackageClauseRe.FindStringSubmatchIndex(content)
	if loc == nil || len(loc) < 6 {
		return content, false
	}
	return content[:loc[4]] + newPkg + content[loc[5]:], true
}

// goLibTestPackageMainReason is the health-guard counterpart of F63 when
// coercion did not run (e.g. tool-loop write).
func goLibTestPackageMainReason(path, content string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if !strings.HasSuffix(strings.ToLower(path), "_test.go") {
		return ""
	}
	if parseGoPackageClause(content) != "main" {
		return ""
	}
	slash := strings.ToLower(path)
	if strings.Contains(slash, "/cmd/") || strings.HasPrefix(slash, "cmd/") {
		return ""
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." || dir == "" || dir == "/" {
		return ""
	}
	base := filepath.Base(dir)
	if base == "" || base == "." || base == "internal" || base == "pkg" || base == "testdata" {
		return ""
	}
	ident := sanitizeGoPackageIdent(base)
	return "library-dir *_test.go must not use package main (use package " + ident + " or " + ident + "_test)"
}

// healGoLibTestPackagesOnDisk rewrites existing F63 smells before Critic aborts.
func healGoLibTestPackagesOnDisk(wd string) []string {
	var healed []string
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(wd, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".avatars/") || strings.HasPrefix(rel, ".git/") {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(rel), "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		fixed, ok := coerceGoLibTestPackage(rel, string(data))
		if !ok || fixed == string(data) {
			return nil
		}
		if writeErr := os.WriteFile(path, []byte(fixed), 0o644); writeErr == nil {
			healed = append(healed, rel)
		}
		return nil
	})
	return healed
}
