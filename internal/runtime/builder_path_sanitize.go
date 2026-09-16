package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// projectRootConfigFiles must live at the project root, never inside a package.
var projectRootConfigFiles = map[string]bool{
	"requirements.txt":     true,
	"requirements-dev.txt": true,
	"pyproject.toml":       true,
	"setup.py":             true,
	"setup.cfg":            true,
	"Pipfile":              true,
	"go.mod":               true,
	"go.sum":               true,
	"Cargo.toml":           true,
	"package.json":         true,
	"tsconfig.json":        true,
	"Makefile":             true,
	"Dockerfile":           true,
	"docker-compose.yml":   true,
	"docker-compose.yaml":  true,
	".gitignore":           true,
	"README.md":            true,
	"LICENSE":              true,
	"config.example.json":  true,
	"config.example.yaml":  true,
	"config.example.yml":   true,
	".env.example":         true,
}

// Root entrypoint basenames that may legitimately live at project root
// when no language-specific cmd/src entry exists yet.
var rootEntrypointBasenames = map[string]bool{
	"main.go":     true,
	"main.py":     true,
	"app.py":      true,
	"__main__.py": true,
	"index.js":    true,
	"index.ts":    true,
	"main.rs":     true,
	"lib.rs":      true,
	"mod.rs":      true,
}

var phaseDocNameRe = regexp.MustCompile(`(?i)^phase\d+\.md$`)

// sanitizeWritePath applies language-agnostic path hygiene to a single relative path.
// Returns the corrected path and whether it was redirected.
func sanitizeWritePath(path string) (string, bool) {
	orig := filepath.ToSlash(filepath.Clean(path))
	out := sanitizeOnePath(orig)
	return out, out != orig
}

// sanitizeBuilderPaths fixes common Builder path hallucinations (NL smoke #14-16 + Batch1/P6):
//   - docs/workflow/ cloned under a package dir → drop (root SoT only)
//   - nested package/package/foo.py → collapse to package/foo.py
//   - requirements.txt / config.example.json under package → move to root
//   - bare library sources at project root → language package dir (internal/, src/, or <stem>/)
//   - bare phaseN.md at root → docs/workflow/phaseN.md
//   - root migrations/*.sql (Go) → internal/storage/migrations/
//   - duplicate after sanitize → keep the longer content (mid NL M1)
func sanitizeBuilderPaths(files []builderCodeFile) (kept []builderCodeFile, redirected []string, dropped []string) {
	return sanitizeBuilderPathsForTask(files, "")
}

// mergeBuilderFileContents prefers the longer blob, then unions unique lines
// from the shorter one so truncated+complete pairs keep both sets of content (V2).
func mergeBuilderFileContents(a, b string) string {
	if a == b {
		return a
	}
	if strings.TrimSpace(a) == "" {
		return b
	}
	if strings.TrimSpace(b) == "" {
		return a
	}
	primary, secondary := a, b
	if len(b) > len(a) {
		primary, secondary = b, a
	}
	// If secondary is mostly already in primary, keep primary.
	secLines := strings.Split(secondary, "\n")
	primSet := map[string]bool{}
	for _, l := range strings.Split(primary, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			primSet[t] = true
		}
	}
	var missing []string
	for _, l := range secLines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if !primSet[t] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return primary
	}
	// Append genuinely unique lines (avoid rewriting whole files twice).
	out := strings.TrimRight(primary, "\n")
	out += "\n" + strings.Join(missing, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// isProtectedWorkflowManifest reports paths that only workflow sync/Confirm may write.
func isProtectedWorkflowManifest(path string) bool {
	p := filepath.ToSlash(filepath.Clean(path))
	base := strings.ToLower(filepath.Base(p))
	switch base {
	case "avatars_todo.md", "avatars_plan.md", "process_record.md", "process_record.yaml":
		return true
	}
	return false
}

// isRootGenOrVerifyScript reports root-level `_gen_*` / `_verify_*` pollution (L6).
func isRootGenOrVerifyScript(path string) bool {
	p := filepath.ToSlash(filepath.Clean(path))
	if strings.Contains(p, "/") {
		return false
	}
	lower := strings.ToLower(filepath.Base(p))
	if !(strings.HasPrefix(lower, "_gen_") || strings.HasPrefix(lower, "_verify_")) {
		return false
	}
	return strings.HasSuffix(lower, ".py") || strings.HasSuffix(lower, ".go") ||
		strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".ps1")
}

// protectedOnlyDrops reports whether every sanitize drop reason is a protected
// workflow SoT refusal (Batch6/T).
func protectedOnlyDrops(dropped []string) bool {
	if len(dropped) == 0 {
		return false
	}
	for _, d := range dropped {
		if !strings.Contains(d, "protected workflow SoT") {
			return false
		}
	}
	return true
}

var migrationVersionPrefixRe = regexp.MustCompile(`(?i)^(\d{3})_.+\.sql$`)

// dedupeMigrationVersionPrefixes keeps one SQL file per NNN_ version prefix
// under **/migrations/ (mid NL: 001_initial + 001_create_users + 001_create_tables).
// Prefer non-noop / longer content.
func dedupeMigrationVersionPrefixes(files []builderCodeFile) (kept []builderCodeFile, dropped []string) {
	type cand struct {
		path    string
		content string
	}
	best := map[string]cand{} // key: dir|NNN
	order := make([]string, 0, len(files))
	passThrough := make([]builderCodeFile, 0, len(files))

	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		base := filepath.Base(p)
		m := migrationVersionPrefixRe.FindStringSubmatch(base)
		underMig := strings.Contains(p, "/migrations/") || strings.HasPrefix(p, "migrations/")
		if m == nil || !underMig {
			passThrough = append(passThrough, f)
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(p))
		key := dir + "|" + m[1]
		score := migrationSQLPreferScore(f.Content)
		if prev, ok := best[key]; ok {
			prevScore := migrationSQLPreferScore(prev.content)
			if score > prevScore || (score == prevScore && len(f.Content) > len(prev.content)) {
				dropped = append(dropped, prev.path+" (duplicate migration version "+m[1]+" superseded)")
				best[key] = cand{path: p, content: f.Content}
			} else {
				dropped = append(dropped, p+" (duplicate migration version "+m[1]+")")
			}
			continue
		}
		best[key] = cand{path: p, content: f.Content}
		order = append(order, key)
	}

	kept = append(kept, passThrough...)
	for _, key := range order {
		c := best[key]
		kept = append(kept, builderCodeFile{Path: c.path, Content: c.content})
	}
	return kept, dropped
}

func migrationSQLPreferScore(content string) int {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	if trimmed == "" || strings.HasPrefix(lower, "-- no-op") || strings.HasPrefix(lower, "-- noop") {
		return 0
	}
	score := 1
	score += strings.Count(lower, "create table")
	if len(trimmed) > 200 {
		score += 2
	} else if len(trimmed) > 50 {
		score++
	}
	return score
}

func sanitizeOnePath(path string) string {
	path = collapseDuplicatePathSegments(filepath.ToSlash(path))
	base := filepath.Base(path)
	dir := filepath.ToSlash(filepath.Dir(path))

	// #16: root config files must not live inside a package directory.
	if projectRootConfigFiles[base] && dir != "." && dir != "" {
		return base
	}

	// Batch1/P6: bare phaseN.md at root → docs/workflow/
	if (dir == "." || dir == "") && phaseDocNameRe.MatchString(base) {
		return "docs/workflow/" + base
	}

	// Mid NL M1: Go root migrations/*.sql → internal/storage/migrations/
	// (go:embed lives next to migrate.go; empty root migrations/ is a dead path).
	if fileExists("go.mod") && strings.HasSuffix(strings.ToLower(base), ".sql") {
		if dir == "migrations" || strings.HasPrefix(path, "migrations/") {
			return "internal/storage/migrations/" + base
		}
	}

	// Batch1/P6: bare library source at project root → package layout.
	if (dir == "." || dir == "") && isSourceFileBasename(base) {
		if redirected := redirectRootSourceFile(base); redirected != "" {
			return redirected
		}
	}

	// #15: collapse nested duplicate package segments: pkg/pkg/file → pkg/file
	parts := strings.Split(path, "/")
	if len(parts) >= 3 {
		collapsed := make([]string, 0, len(parts))
		for i := 0; i < len(parts); i++ {
			if i+1 < len(parts) && parts[i] == parts[i+1] && parts[i] != "" &&
				!strings.Contains(parts[i], ".") {
				continue
			}
			collapsed = append(collapsed, parts[i])
		}
		newPath := strings.Join(collapsed, "/")
		if newPath != path {
			return newPath
		}
	}
	return path
}

func isSourceFileBasename(base string) bool {
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".go", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".rs", ".java", ".kt", ".cs":
		return true
	default:
		return false
	}
}

// redirectRootSourceFile maps a bare root source file into a conventional
// package directory. Language-agnostic: uses on-disk manifests + existing layout.
// Returns "" when the file is allowed to stay at root (entrypoint).
func redirectRootSourceFile(base string) string {
	ext := strings.ToLower(filepath.Ext(base))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.TrimSuffix(stem, "_test")
	if stem == "" {
		return ""
	}

	// Entrypoints stay at root only when no dedicated entry layout exists.
	if rootEntrypointBasenames[base] {
		switch ext {
		case ".go":
			if dirExists("cmd/server") || dirExists("cmd") {
				return "cmd/server/" + base
			}
			return ""
		case ".py":
			if dirExists("src") {
				return "src/" + base
			}
			return ""
		case ".js", ".ts", ".mjs", ".tsx", ".jsx":
			if dirExists("src") {
				return "src/" + base
			}
			return ""
		case ".rs":
			if dirExists("src") {
				return "src/" + base
			}
			return ""
		}
		return ""
	}

	// Library / non-entry modules never belong at repo root.
	switch {
	case fileExists("go.mod") || ext == ".go":
		// F34: prefer an existing library package (e.g. cronnext/doc.go) over
		// inventing internal/<stem>/ (e.g. internal/doc/doc.go) which splits
		// the module into a hollow docs package beside the real impl.
		if dir := preferredGoLibraryDir(base, stem); dir != "" {
			return dir + "/" + base
		}
		pkg := goPackageStem(stem)
		// F34: bare doc.go must not invent internal/doc/ — use module basename.
		if isGoPackageDocBasename(base) {
			if mod := goModuleDirName(); mod != "" {
				return mod + "/" + base
			}
		}
		// F78: module-named public library sources stay at repo root
		// (ttlcache.go), never internal/<mod>/ and never <mod>/<mod>.go.
		// Lifting to <mod>/ then collapsing back is the ping-pong that burned
		// Builder tokens. Existing <mod>/ impl trees still win via preferredGoLibraryDir.
		if publicProjectSourceStaysAtRoot(".", stem, pkg) {
			return ""
		}
		return "internal/" + pkg + "/" + base
	case fileExists("Cargo.toml") || ext == ".rs":
		return "src/" + base
	case fileExists("package.json") || fileExists("tsconfig.json") ||
		ext == ".js" || ext == ".ts" || ext == ".tsx" || ext == ".jsx" || ext == ".mjs":
		// N1-2: do not invent src/sql/sql.js from bare dependency name sql.js.
		if isDependencyOrRuntimeFilename(base) {
			return ""
		}
		if publicProjectSourceStaysAtRoot(".", stem, pkgDirName(stem)) {
			return ""
		}
		if dirExists("src") {
			return "src/" + pkgDirName(stem) + "/" + base
		}
		return pkgDirName(stem) + "/" + base
	case fileExists("pyproject.toml") || fileExists("requirements.txt") ||
		fileExists("setup.py") || ext == ".py":
		if publicProjectSourceStaysAtRoot(".", stem, pkgDirName(stem)) {
			return ""
		}
		if dirExists("src") {
			return "src/" + pkgDirName(stem) + "/" + base
		}
		return pkgDirName(stem) + "/" + base
	default:
		// Unknown stack: still keep libraries out of root.
		return pkgDirName(stem) + "/" + base
	}
}

// publicProjectSourceStaysAtRoot is true when stem/pkg is the project's public
// package identity (go.mod / package.json / Cargo.toml / pyproject) and there
// is not already an on-disk package directory with implementation files.
// Cross-lang: pins small libraries at repo root so pkg/pkg.ext ↔ pkg.ext
// cannot ping-pong (F78).
func publicProjectSourceStaysAtRoot(wd, stem, pkg string) bool {
	for n := range projectPackageNamesAt(wd) {
		if !strings.EqualFold(stem, n) && !strings.EqualFold(pkg, n) {
			continue
		}
		if dirHasImplSources(filepath.Join(nonEmptyWD(wd), n)) {
			return false
		}
		return true
	}
	return false
}

func goPackageStem(stem string) string {
	// config_test → config; user_store → storage when common aliases apply
	aliases := map[string]string{
		"migrate": "storage",
		"db":      "database",
		"conn":    "database",
		"connect": "storage",
	}
	if a, ok := aliases[stem]; ok {
		return a
	}
	return pkgDirName(stem)
}

func isGoPackageDocBasename(base string) bool {
	return strings.EqualFold(base, "doc.go") || strings.EqualFold(base, "doc_test.go")
}

// goModuleDirName returns the last path segment of the go.mod module path
// (example.com/cronnext → cronnext). Empty when go.mod is missing/unreadable.
func goModuleDirName() string {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		mod := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		mod = strings.Trim(mod, `"'`)
		if mod == "" {
			return ""
		}
		return filepath.Base(filepath.FromSlash(mod))
	}
	return ""
}

// preferredGoLibraryDir picks an on-disk library package directory for a bare
// root .go file. Prefers the module basename dir, then a single top-level
// impl package (not cmd/, not internal/doc-only hollow packages).
func preferredGoLibraryDir(base, stem string) string {
	_ = stem
	if mod := goModuleDirName(); mod != "" && dirExists(mod) && dirHasNonTestGoSources(mod) {
		return mod
	}
	candidates := listTopLevelGoLibDirs()
	if len(candidates) == 1 {
		return candidates[0]
	}
	// Multiple candidates: for package docs, prefer the one that already has
	// non-doc implementation files matching a conventional library layout.
	if isGoPackageDocBasename(base) {
		for _, c := range candidates {
			if dirHasImplGoSources(c) {
				return c
			}
		}
	}
	return ""
}

func listTopLevelGoLibDirs() []string {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		switch name {
		case "cmd", "docs", "testdata", "vendor", "node_modules", "tmp", ".avatars", ".git":
			continue
		}
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		// Skip hollow internal/<doc-only> trees; keep real internal packages
		// out of "preferred" for bare root docs (those belong beside the API).
		if name == "internal" {
			continue
		}
		if dirHasNonTestGoSources(name) || dirHasGoSources(name) {
			out = append(out, name)
		}
	}
	return out
}

func dirHasGoSources(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(ent.Name()), ".go") {
			return true
		}
	}
	return false
}

func dirHasNonTestGoSources(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		n := strings.ToLower(ent.Name())
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			return true
		}
	}
	return false
}

func dirHasImplGoSources(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		n := strings.ToLower(ent.Name())
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		if n == "doc.go" {
			continue
		}
		return true
	}
	return false
}

// dirHasImplSources is the cross-lang counterpart of dirHasImplGoSources:
// any implementation source (not tests-only, not docs-only) under dir.
func dirHasImplSources(dir string) bool {
	if dirHasImplGoSources(dir) {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		n := ent.Name()
		lower := strings.ToLower(n)
		if !isSourceFileBasename(n) {
			continue
		}
		if strings.HasSuffix(lower, "_test.go") || strings.HasSuffix(lower, "_test.py") ||
			strings.HasSuffix(lower, ".test.js") || strings.HasSuffix(lower, ".test.ts") ||
			strings.HasSuffix(lower, "_test.rs") {
			continue
		}
		if strings.EqualFold(n, "doc.go") || strings.EqualFold(n, "__init__.py") {
			// __init__.py counts as impl when non-empty; checked below via size.
			if strings.EqualFold(n, "__init__.py") {
				info, err := ent.Info()
				if err == nil && info.Size() > 40 {
					return true
				}
			}
			continue
		}
		return true
	}
	return false
}

func pkgDirName(stem string) string {
	stem = strings.ReplaceAll(stem, "-", "_")
	stem = strings.ToLower(stem)
	if stem == "" {
		return "pkg"
	}
	return stem
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// purgeMisplacedRootDuplicates deletes root-level source/docs that should live
// under a package path when the correct redirected path already exists.
// Language-agnostic; uses the same sanitize rules as write hygiene.
// Returns deleted relative paths.
func purgeMisplacedRootDuplicates(wd string) []string {
	if isAvatarsHarnessTree(wd) {
		return nil
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		return nil
	}
	var deleted []string
	// G4: root avatars_todo.md / avatars_plan.md are pollution when SoT exists.
	for _, name := range []string{"avatars_todo.md", "avatars_plan.md", "process_record.md"} {
		rootFull := filepath.Join(wd, name)
		sot := filepath.Join(wd, "docs", "workflow", name)
		if fileExists(rootFull) && fileExists(sot) {
			if err := os.Remove(rootFull); err == nil {
				deleted = append(deleted, name+" (root pollution; SoT is docs/workflow/)")
			}
		}
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		base := ent.Name()
		if !isSourceFileBasename(base) && !phaseDocNameRe.MatchString(base) {
			continue
		}
		corrected, changed := sanitizeWritePath(base)
		if !changed || corrected == "" || corrected == base {
			continue
		}
		correctFull := filepath.Join(wd, filepath.FromSlash(corrected))
		rootFull := filepath.Join(wd, base)
		// F55: drop root shells even when the redirected path is missing yet,
		// if a real library package dir already exists (doc.go optional).
		if !fileExists(correctFull) {
			if lib := findSiblingLibraryPackage(wd); isGoPackageDocBasename(base) && lib != "" {
				if err := os.Remove(rootFull); err == nil {
					deleted = append(deleted, base+" (root package-doc shell; library is "+lib+"/)")
				}
			}
			continue
		}
		if err := os.Remove(rootFull); err != nil {
			continue
		}
		deleted = append(deleted, base+" (duplicate of "+corrected+")")
	}
	return deleted
}

// purgeRootGenScripts deletes root-level helper pollution like `_gen_*.py` /
// `_verify_*.py` that are not part of the declared project layout (L6).
func purgeRootGenScripts(wd string) []string {
	if isAvatarsHarnessTree(wd) {
		return nil
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		return nil
	}
	var deleted []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		base := ent.Name()
		lower := strings.ToLower(base)
		if !(strings.HasPrefix(lower, "_gen_") || strings.HasPrefix(lower, "_verify_")) {
			continue
		}
		if !(strings.HasSuffix(lower, ".py") || strings.HasSuffix(lower, ".go") ||
			strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".ps1")) {
			continue
		}
		full := filepath.Join(wd, base)
		if err := os.Remove(full); err != nil {
			continue
		}
		deleted = append(deleted, base)
	}
	return deleted
}
