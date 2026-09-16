package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

// sanitizeBuilderPathsForTask is like sanitizeBuilderPaths but uses task hints
// to remap misplaced packages into the declared project layout (G2).
// Language-aware: Python app/, Go internal/, JS/TS/Rust src/ — never force
// a Python app/ tree onto Go/Rust greenfield.
func sanitizeBuilderPathsForTask(files []builderCodeFile, taskInput string) (kept []builderCodeFile, redirected []string, dropped []string) {
	layout := detectDeclaredLayout(taskInput)
	charter := resolveLayoutCharter(".")
	lowerTask := strings.ToLower(taskInput)
	forbidCLI := forbidsCLIScaffold(lowerTask)
	seenIdx := map[string]int{}
	for _, f := range files {
		orig := filepath.ToSlash(filepath.Clean(f.Path))
		// L6: drop root _gen_/_verify_ helper scripts before path redirects.
		if isRootGenOrVerifyScript(orig) {
			dropped = append(dropped, orig+" (root gen/verify script blocked)")
			continue
		}
		path := sanitizeOnePath(orig)
		if layout.root != "" {
			path = remapToDeclaredLayout(path, layout)
		}
		// F52/F42: Critic layout charter wins over default layout remaps
		// (e.g. do not bury module-named public Go libs under internal/).
		if mapped, rej := directorMapTarget(charter, ".", path); rej == "" && mapped != "" && mapped != path {
			path = mapped
		}
		path = unburyModuleNamedInternalPathAt(".", path)
		if lifted := liftPrivateDirPublicAPIAt(".", path, f.Content); lifted != "" {
			path = lifted
		}

		// #14: never write workflow docs outside project-root docs/workflow/.
		if strings.Contains(path, "/docs/workflow/") || strings.HasPrefix(path, "docs/workflow/") {
			if !strings.HasPrefix(path, "docs/workflow/") {
				dropped = append(dropped, orig+" (workflow docs only at project root)")
				continue
			}
		}
		// Batch5/N: Builder must not rewrite SoT workflow manifests (todo/plan/record).
		if isProtectedWorkflowManifest(path) {
			dropped = append(dropped, orig+" (protected workflow SoT — Builder cannot write)")
			continue
		}
		// G4: root avatars_todo.md is never a write target — SoT is docs/workflow/.
		if base := filepath.Base(path); (path == base || path == "./"+base) && isProtectedWorkflowManifest(base) {
			dropped = append(dropped, orig+" (protected workflow SoT — Builder cannot write)")
			continue
		}
		// C10: junk scaffold entrypoints.
		if isJunkScaffoldPath(path) {
			dropped = append(dropped, orig+" (junk scaffold blocked)")
			continue
		}
		// F64: user forbade CLI / executable scaffold — drop root mains and cmd/.
		if forbidCLI && isForbiddenCLIEntrypointPath(path) {
			dropped = append(dropped, orig+" (CLI/entrypoint blocked by no-CLI constraint)")
			continue
		}

		// F63 (Go): coerce library-dir *_test.go away from package main.
		if fixed, ok := coerceGoLibTestPackage(path, f.Content); ok {
			f.Content = fixed
			redirected = append(redirected, orig+" (package main → lib test package)")
		}

		if path != orig {
			redirected = append(redirected, orig+" → "+path)
		}

		if idx, ok := seenIdx[path]; ok {
			merged := mergeBuilderFileContents(kept[idx].Content, f.Content)
			if merged != kept[idx].Content {
				if len(f.Content) > len(kept[idx].Content) {
					dropped = append(dropped, kept[idx].Path+" (duplicate merged; kept longer+union)")
				} else {
					dropped = append(dropped, orig+" (duplicate merged into existing)")
				}
				kept[idx].Content = merged
				kept[idx].Path = path
			} else {
				dropped = append(dropped, orig+" (duplicate identical after sanitize)")
			}
			continue
		}
		seenIdx[path] = len(kept)
		f.Path = path
		kept = append(kept, f)
	}
	kept, migDropped := dedupeMigrationVersionPrefixes(kept)
	dropped = append(dropped, migDropped...)
	kept, shadowDropped := dropModulePackageConflicts(kept)
	dropped = append(dropped, shadowDropped...)
	return kept, redirected, dropped
}

// declaredLayout captures the project tree the task asked for.
type declaredLayout struct {
	root          string // app | internal | src | ""
	lang          string // go | python | javascript | typescript | rust | ""
	topMigrations bool   // R7-2: prefer migrations/ over internal/**/migrations/
}

// detectDeclaredLayout infers layout root from task text + on-disk manifests.
// Go/Rust never default to Python-style app/ unless the task explicitly asks.
// Empty taskInput → only remap when a clear on-disk layout already exists
// (avoids inheriting a parent-repo go.mod during unit tests / nested dirs).
func detectDeclaredLayout(taskInput string) declaredLayout {
	if strings.TrimSpace(taskInput) == "" {
		switch {
		case dirExists("app") && (fileExists("requirements.txt") || fileExists("pyproject.toml") || fileExists("setup.py")):
			return declaredLayout{root: "app", lang: "python"}
		// F52: go.mod alone must not force layout.root=internal — only when
		// internal/ already exists (service tree). Bare module greenfields keep
		// root empty so public <mod>/ packages are not buried.
		case fileExists("go.mod"):
			if goInternalIsServiceTreeAt(".") {
				return declaredLayout{root: "internal", lang: "go"}
			}
			return declaredLayout{root: "", lang: "go"}
		case dirExists("src") && fileExists("Cargo.toml"):
			return declaredLayout{root: "src", lang: "rust"}
		case dirExists("src") && (fileExists("package.json") || fileExists("tsconfig.json")):
			return declaredLayout{root: "src", lang: "javascript"}
		default:
			return declaredLayout{}
		}
	}
	lang := detectTargetLanguage(taskInput)
	if lang == "" {
		lang = detectLanguageFromDisk()
	}
	lower := strings.ToLower(taskInput)
	topMig := wantsTopLevelMigrations(lower)

	explicitApp := containsAnyFold(lower,
		"app/core", "app/db", "app/models", "app/api", "app/main.py",
		"app/core/", "app/db/", "app/models/", "app/api/",
		"app\\core", "app\\db", "app\\models", "app\\api",
	)
	explicitInternal := containsAnyFold(lower,
		"internal/", "internal\\", "cmd/server", "cmd\\server",
	)
	explicitSrc := containsAnyFold(lower,
		"src/", "src\\", "src/main", "src\\main", "src/lib", "src/components",
	)

	switch {
	case lang == "go" || (fileExists("go.mod") && lang != "python" && lang != "javascript" && lang != "typescript" && lang != "rust"):
		if explicitApp && !explicitInternal {
			return declaredLayout{root: "app", lang: "go", topMigrations: topMig}
		}
		// F52: public/library-first Go must not default-bury under internal/.
		// Negative wording like "禁止 internal/" also contains "internal/" —
		// do not treat that as a request for a service tree.
		if wantsPublicLibraryLayout(lower) {
			return declaredLayout{root: "", lang: "go", topMigrations: topMig}
		}
		if explicitInternal || goInternalIsServiceTreeAt(".") || wantsGoServiceInternalLayout(lower) {
			return declaredLayout{root: "internal", lang: "go", topMigrations: topMig}
		}
		return declaredLayout{root: "", lang: "go", topMigrations: topMig}
	case lang == "rust" || (fileExists("Cargo.toml") && lang == ""):
		return declaredLayout{root: "src", lang: "rust", topMigrations: topMig}
	case lang == "javascript" || lang == "typescript" ||
		((fileExists("package.json") || fileExists("tsconfig.json")) && lang == ""):
		if explicitApp && !explicitSrc {
			return declaredLayout{root: "app", lang: langOr(lang, "javascript"), topMigrations: topMig}
		}
		return declaredLayout{root: "src", lang: langOr(lang, "javascript"), topMigrations: topMig}
	case lang == "python" || fileExists("requirements.txt") || fileExists("pyproject.toml") || fileExists("setup.py"):
		if explicitSrc && !explicitApp {
			return declaredLayout{root: "src", lang: "python", topMigrations: topMig}
		}
		if explicitApp || dirExists("app") || strings.Contains(lower, "fastapi") || strings.Contains(lower, "django") {
			return declaredLayout{root: "app", lang: "python", topMigrations: topMig}
		}
		return declaredLayout{root: "", lang: "python", topMigrations: topMig}
	default:
		if explicitApp {
			return declaredLayout{root: "app", lang: lang, topMigrations: topMig}
		}
		if explicitInternal {
			return declaredLayout{root: "internal", lang: lang, topMigrations: topMig}
		}
		if explicitSrc {
			return declaredLayout{root: "src", lang: lang, topMigrations: topMig}
		}
		return declaredLayout{root: "", lang: lang, topMigrations: topMig}
	}
}

// wantsPublicLibraryLayout detects library-first / forbid-bury prompts (cross-lang wording).
func wantsPublicLibraryLayout(lower string) bool {
	if containsAnyFold(lower,
		"pure library", "library-first", "library first", "纯库", "纯库优先",
		"not under internal", "not under `internal", "no internal/", "forbid internal",
		"禁止 internal", "不要进 internal", "不要再进 internal", "勿放进 internal", "勿再放进 internal",
		"包路径建议", "top-level package", "顶层", "public library", "public api package",
		"开源库", "只要库和测试", "只要库和单测", "只要库", "library and tests",
	) {
		return true
	}
	return (strings.Contains(lower, "禁止") || strings.Contains(lower, "不要") || strings.Contains(lower, "not under")) &&
		strings.Contains(lower, "internal")
}

// forbidsCLIScaffold detects negative constraints against shipping a CLI /
// executable scaffold (F64, cross-language). When true, root mains and cmd/
// entrypoints are dropped unless the task also explicitly requests them.
func forbidsCLIScaffold(lower string) bool {
	return containsAnyFold(lower,
		"no cli", "without cli", "without a cli", "don't create a cli", "do not create a cli",
		"no command-line", "no command line", "library only", "library-only",
		"不要 cli", "不要cli", "别做 cli", "别做cli", "无需 cli", "无需cli",
		"不要命令行", "不要做成带命令行", "不要做成命令行", "带命令行的小工具",
		"别铺脚手架", "不要脚手架", "不要可执行", "不要可执行文件",
		"不要写 cli", "不要写cli", "纯库不要 cli", "纯库、不要", "纯库，不要",
		"只要库和测试", "no cli tool", "not a cli", "not a command-line",
	)
}

// isForbiddenCLIEntrypointPath reports root / cmd-style entrypoints that must
// be dropped when the user forbade CLI scaffolding (cross-lang).
// F69: also covers tmp/_check/scratch probe mains (r22 tmp/b64check/main.go).
func isForbiddenCLIEntrypointPath(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(lower))
	dir := strings.ToLower(filepath.ToSlash(filepath.Dir(lower)))
	if isCLIEntrypointBasename(base) {
		if dir == "." || dir == "" {
			return true
		}
		// Scratch / probe trees are never a library deliverable under no-CLI.
		if strings.HasPrefix(lower, "tmp/") || strings.Contains(lower, "/tmp/") ||
			strings.HasPrefix(lower, "temp/") || strings.Contains(lower, "/temp/") ||
			strings.HasPrefix(lower, "scratch/") || strings.Contains(lower, "/scratch/") ||
			strings.HasPrefix(lower, "_check/") || strings.Contains(lower, "/_check/") ||
			strings.HasPrefix(lower, "_tmp/") || strings.Contains(lower, "/_tmp/") ||
			strings.HasPrefix(lower, "b64check/") || strings.Contains(lower, "/b64check/") {
			return true
		}
	}
	if strings.HasPrefix(lower, "cmd/") || strings.Contains(lower, "/cmd/") {
		return true
	}
	if strings.HasPrefix(lower, "bin/") || strings.Contains(lower, "/bin/") {
		switch {
		case strings.HasSuffix(lower, ".go"), strings.HasSuffix(lower, ".py"),
			strings.HasSuffix(lower, ".rs"), strings.HasSuffix(lower, ".js"),
			strings.HasSuffix(lower, ".ts"), strings.HasSuffix(lower, ".mjs"),
			strings.HasSuffix(lower, ".cjs"):
			return true
		}
	}
	return false
}

func isCLIEntrypointBasename(base string) bool {
	switch strings.ToLower(base) {
	case "main.go", "main.py", "main.rs", "main.js", "main.ts", "main.mjs", "main.cjs",
		"cli.go", "cli.py", "cli.js", "cli.ts", "app_cli.py", "__main__.py":
		return true
	default:
		return false
	}
}

// wantsGoServiceInternalLayout keeps remapping for typical Go services that
// expect handlers/config under internal/ (existing G2 tests).
func wantsGoServiceInternalLayout(lower string) bool {
	return containsAnyFold(lower,
		"http api", "http service", "microservice", "rest api",
		"cmd/server", "cmd\\server", "handlers/", "handler/",
		"handlers", "internal/config", "internal\\config", "internal/handler",
	)
}

func wantsTopLevelMigrations(lowerTask string) bool {
	if strings.Contains(lowerTask, "internal/storage/migrations") {
		return false
	}
	return strings.Contains(lowerTask, "migrations/") ||
		strings.Contains(lowerTask, "migrations\\") ||
		strings.Contains(lowerTask, "、migrations") ||
		strings.Contains(lowerTask, ",migrations") ||
		strings.Contains(lowerTask, " migrations")
}

func langOr(lang, fallback string) string {
	if lang == "" {
		return fallback
	}
	return lang
}

func detectLanguageFromDisk() string {
	switch {
	case fileExists("go.mod"):
		return "go"
	case fileExists("Cargo.toml"):
		return "rust"
	case fileExists("package.json") || fileExists("tsconfig.json"):
		return "javascript"
	case fileExists("requirements.txt") || fileExists("pyproject.toml") || fileExists("setup.py"):
		return "python"
	case dirExists("app") && !dirExists("internal") && !dirExists("cmd"):
		return "python"
	default:
		return ""
	}
}

func containsAnyFold(lower string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(lower, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// Common package-ish top dirs Builder may dump at repo root instead of under
// the declared layout root.
var layoutTopPkgs = map[string]bool{
	"core": true, "db": true, "models": true, "api": true,
	"schemas": true, "services": true, "routers": true, "crud": true,
	"handlers": true, "handler": true, "config": true, "storage": true,
	"middleware": true, "auth": true, "routes": true, "controllers": true,
	"components": true, "hooks": true, "utils": true, "lib": true,
	// Domain packages commonly misplaced at repo root (Go/Python/JS/Rust).
	"database": true, "snippet": true, "snippets": true, "repo": true, "repository": true,
	"domain": true, "entities": true, "adapters": true, "ports": true,
}

// remapToDeclaredLayout lifts misplaced packages under the declared root.
func remapToDeclaredLayout(path string, layout declaredLayout) string {
	path = filepath.ToSlash(path)
	if path == "" || layout.root == "" {
		return path
	}
	// R7-2: when prompt asks for top-level migrations/, collapse nested
	// layout-root/.../migrations artifacts into migrations/ (cross-lang).
	if layout.topMigrations {
		if remapped, ok := remapNestedMigrationsToTopLevel(path); ok {
			return remapped
		}
	}
	prefix := layout.root + "/"
	if path == layout.root || strings.HasPrefix(path, prefix) {
		return path
	}
	// Never pull docs/migrations/tests into app/internal/src.
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	top := parts[0]
	switch top {
	case "docs", "migrations", "alembic", "tests", "test", "node_modules",
		"vendor", "target", "dist", "build", ".avatars", "prisma":
		return path
	case "cmd":
		// Go cmd/ is a sibling of internal/, never nest under internal/.
		if layout.lang == "go" || layout.root == "internal" {
			return path
		}
	}
	// F52 (cross-lang): never nest a project-canonical public package under
	// the layout root (Go module basename, npm package name, etc.).
	if preserveTopLevelPackage(top, layout) {
		return path
	}

	base := parts[len(parts)-1]
	ext := strings.ToLower(filepath.Ext(base))
	stem := strings.TrimSuffix(strings.ToLower(base), ext)
	stem = strings.TrimSuffix(stem, "_test")

	switch layout.root {
	case "app":
		return remapIntoApp(path, parts, base, stem, ext)
	case "internal":
		return remapIntoInternal(path, parts, base, stem, ext)
	case "src":
		return remapIntoSrc(path, parts, base, stem, ext, layout.lang)
	default:
		return path
	}
}

func remapIntoApp(path string, parts []string, base, stem, ext string) string {
	// Bare / stem/stem.* modules → conventional app subpackages (Python-first,
	// but also used when JS task explicitly asked for app/).
	if (len(parts) == 1 || (len(parts) == 2 && parts[0] == stem)) && isCodeExt(ext) {
		switch stem {
		case "main", "app", "__main__", "index":
			return "app/" + base
		case "models", "model", "auth", "schemas", "schema", "services", "service":
			// C4: keep module files as modules (app/models.py). Remapping to
			// app/models/__init__.py while models.py also exists causes ImportError.
			return "app/" + base
		case "database", "db":
			if ext == ".py" {
				return "app/" + base
			}
			return "app/db/" + base
		case "config", "settings":
			if ext == ".py" {
				return "app/" + base
			}
			return "app/core/" + base
		}
	}
	if layoutTopPkgs[parts[0]] {
		return "app/" + path
	}
	return path
}

func remapIntoInternal(path string, parts []string, base, stem, ext string) string {
	if len(parts) > 0 && preserveTopLevelPackage(parts[0], declaredLayout{root: "internal", lang: "go"}) {
		return path
	}
	if ext != ".go" && ext != ".sql" {
		// Non-Go files: only lift known package dirs.
		if layoutTopPkgs[parts[0]] {
			return "internal/" + path
		}
		return path
	}
	// Entrypoints belong under cmd/, not internal/.
	if len(parts) == 1 && (stem == "main" || strings.EqualFold(base, "main.go")) {
		return "cmd/server/" + base
	}
	if (len(parts) == 1 || (len(parts) == 2 && parts[0] == stem)) && ext == ".go" {
		pkg := goPackageStem(stem)
		// F52: module-named package dirs stay top-level (bloomx/bloomx.go).
		if preserveTopLevelPackage(pkg, declaredLayout{root: "internal", lang: "go"}) {
			if len(parts) == 2 {
				return path
			}
			return pkg + "/" + base
		}
		return "internal/" + pkg + "/" + base
	}
	if layoutTopPkgs[parts[0]] || parts[0] == "pkg" {
		if parts[0] == "pkg" {
			return path // pkg/ is a valid Go sibling
		}
		return "internal/" + path
	}
	return path
}

// preserveTopLevelPackage reports whether top is already the project's
// canonical public package directory and must not be nested under layout.root.
// Language-aware: Go module basename, npm package name, Python project name.
func preserveTopLevelPackage(top string, layout declaredLayout) bool {
	top = strings.TrimSpace(top)
	if top == "" || top == layout.root {
		return false
	}
	lang := layout.lang
	if lang == "" {
		lang = detectLanguageFromDisk()
	}
	switch lang {
	case "go":
		if mod := goModuleDirName(); mod != "" && strings.EqualFold(top, mod) {
			return true
		}
		return dirExists(top) && dirHasImplGoSources(top)
	case "javascript", "typescript":
		if name := nodePackageDirName(); name != "" && strings.EqualFold(top, name) {
			return true
		}
	case "python":
		if name := pythonPackageDirName(); name != "" && strings.EqualFold(top, name) {
			return true
		}
	}
	return false
}

func nodePackageDirName() string {
	data, err := os.ReadFile("package.json")
	if err != nil {
		return ""
	}
	// Minimal parse: "name": "foo" or "@scope/foo" → last segment.
	lower := string(data)
	idx := strings.Index(lower, `"name"`)
	if idx < 0 {
		idx = strings.Index(lower, `"name"`)
	}
	if idx < 0 {
		return ""
	}
	rest := lower[idx:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = rest[colon+1:]
	q1 := strings.Index(rest, `"`)
	if q1 < 0 {
		return ""
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, `"`)
	if q2 < 0 {
		return ""
	}
	name := rest[:q2]
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t\n") {
		return ""
	}
	return name
}

func pythonPackageDirName() string {
	for _, f := range []string{"pyproject.toml", "setup.cfg"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "name") && strings.Contains(line, "=") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}
				name := strings.TrimSpace(parts[1])
				name = strings.Trim(name, `"'`)
				name = strings.ReplaceAll(name, "-", "_")
				if name != "" {
					return name
				}
			}
		}
	}
	return ""
}

// remapNestedMigrationsToTopLevel collapses <layoutRoot>/**/migrations/* → migrations/*.
// Covers Go internal/storage/migrations, Python app/db/migrations, JS src/migrations, etc.
func remapNestedMigrationsToTopLevel(path string) (string, bool) {
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "migrations/") || strings.HasPrefix(path, "alembic/") ||
		strings.HasPrefix(path, "prisma/migrations/") {
		return path, false
	}
	const marker = "/migrations/"
	idx := strings.Index(path, marker)
	if idx < 0 {
		return path, false
	}
	// Only rewrite when nested under a declared code root (not already top-level).
	prefix := path[:idx]
	switch {
	case prefix == "internal" || strings.HasPrefix(prefix, "internal/"),
		prefix == "app" || strings.HasPrefix(prefix, "app/"),
		prefix == "src" || strings.HasPrefix(prefix, "src/"),
		prefix == "pkg" || strings.HasPrefix(prefix, "pkg/"):
		// ok
	default:
		return path, false
	}
	rest := path[idx+len(marker):]
	if rest == "" {
		return path, false
	}
	lowerRest := strings.ToLower(rest)
	// Keep SQL / versioned migration artifacts; skip random non-migration files.
	if strings.HasSuffix(lowerRest, ".sql") || strings.HasSuffix(lowerRest, ".py") ||
		strings.Contains(rest, "/") || strings.HasSuffix(lowerRest, ".rs") {
		return "migrations/" + rest, true
	}
	return path, false
}

// rewriteBuilderToolPath remaps a tool-loop write path into the declared layout.
// path may be absolute; returns a project-relative slash path when under wd.
// Language-agnostic: uses task text + manifests under wd (go.mod / Cargo.toml /
// package.json / pyproject / requirements).
// P8-2: paths that escape wd are collapsed to basename (then layout-remapped)
// rather than written outside the project.
func rewriteBuilderToolPath(wd, taskInput, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	rel := path
	escaped := false
	if filepath.IsAbs(path) && wd != "" {
		if r, err := filepath.Rel(wd, path); err == nil && r != "" && !strings.HasPrefix(r, "..") {
			rel = r
		} else {
			// Absolute outside project — keep basename only.
			rel = filepath.Base(path)
			escaped = true
		}
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	// Relative escapes like ../main.py → basename.
	if rel == ".." || strings.HasPrefix(rel, "../") {
		rel = filepath.Base(filepath.ToSlash(path))
		escaped = true
	}
	rel = collapseDuplicatePathSegmentsAt(wd, rel)

	// F55 (cross-lang): always apply basename/path hygiene even when layout.root
	// is empty (library-first Go no longer defaults to internal/). Previously
	// absolute paths under wd were returned unchanged and resurrected root shells.
	// Sanitize/charter use on-disk manifests under wd — temporarily chdir so
	// go.mod / package.json / Cargo.toml resolution matches the project tree.
	applyHygiene := func() {
		if cleaned, changed := sanitizeWritePath(rel); changed && cleaned != "" {
			rel = cleaned
		}
		if wd == "" {
			rel = unburyModuleNamedInternalPathAt(".", rel)
			return
		}
		if mapped, rej := directorMapTarget(resolveLayoutCharter(wd), wd, rel); rej == "" && mapped != "" {
			rel = mapped
		}
		rel = unburyModuleNamedInternalPathAt(wd, rel)
	}
	if wd != "" {
		if old, err := os.Getwd(); err == nil && old != wd {
			if err := os.Chdir(wd); err == nil {
				applyHygiene()
				_ = os.Chdir(old)
			} else {
				applyHygiene()
			}
		} else {
			applyHygiene()
		}
	} else {
		applyHygiene()
	}

	layout := detectDeclaredLayout(taskInput)
	if layout.root == "" && wd != "" {
		layout = detectDeclaredLayoutUnder(wd, taskInput)
	} else if wd != "" {
		// Prefer task-declared root, but fill lang from wd manifests when missing.
		if layout.lang == "" {
			disk := detectDeclaredLayoutUnder(wd, "")
			if disk.lang != "" {
				layout.lang = disk.lang
			}
		}
		if !layout.topMigrations {
			layout.topMigrations = wantsTopLevelMigrations(strings.ToLower(taskInput))
		}
	}
	if layout.root != "" {
		rel = remapToDeclaredLayout(rel, layout)
	}

	// F70: even if a prior write created internal/<mod>/ (so layout.root
	// flipped to internal), never keep the public module package buried.
	rel = unburyModuleNamedInternalPathAt(wd, rel)
	rel = liftPrivateDirPublicAPIAt(wd, rel, "")

	out := rel
	if filepath.IsAbs(path) && wd != "" && !escaped {
		out = filepath.Join(wd, filepath.FromSlash(rel))
	} else if wd != "" && (escaped || filepath.IsAbs(path)) {
		out = filepath.Join(wd, filepath.FromSlash(rel))
	} else if filepath.IsAbs(path) && !escaped && rel == filepath.ToSlash(filepath.Clean(path)) {
		out = path
	}
	return out
}

// rewriteBuilderToolPathWithContent applies path hygiene then lifts public-API
// sources that landed under a private helper directory (Go package clause, or
// cross-lang public package name under internal/ / _internal/ / src/internal/).
func rewriteBuilderToolPathWithContent(wd, taskInput, path, content string) string {
	out := rewriteBuilderToolPath(wd, taskInput, path)
	rel := filepath.ToSlash(out)
	madeAbs := filepath.IsAbs(out)
	if madeAbs && wd != "" {
		if r, err := filepath.Rel(wd, out); err == nil && r != "" && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		}
	}
	lifted := liftPrivateDirPublicAPIAt(wd, rel, content)
	if lifted == "" || lifted == rel {
		return out
	}
	if madeAbs && wd != "" {
		return filepath.Join(wd, filepath.FromSlash(lifted))
	}
	return lifted
}

// applyWritePathRewrite returns the project-relative path after layout remap
// (F105). remappedFrom is the original requested path when it differs.
func applyWritePathRewrite(wd, taskInput, requested, content string) (final, remappedFrom string) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", ""
	}
	out := rewriteBuilderToolPathWithContent(wd, taskInput, requested, content)
	final = filepath.ToSlash(out)
	if wd != "" && filepath.IsAbs(out) {
		if rel, err := filepath.Rel(wd, out); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
			final = filepath.ToSlash(rel)
		}
	}
	reqSlash := filepath.ToSlash(requested)
	if wd != "" && filepath.IsAbs(requested) {
		if rel, err := filepath.Rel(wd, requested); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
			reqSlash = filepath.ToSlash(rel)
		}
	}
	if reqSlash != final {
		return final, reqSlash
	}
	return final, ""
}

// collapseDuplicatePathSegments folds pkg/pkg.py and pkg/pkg/file into pkg/file.
// Cross-lang: catches Builder mistakes like conftest/conftest.py, models/models.py.
func collapseDuplicatePathSegments(path string) string {
	return collapseDuplicatePathSegmentsAt(".", path)
}

func collapseDuplicatePathSegmentsAt(wd, path string) string {
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return path
	}
	// pkg/pkg.ext → pkg.ext unless the public package dir already has impl
	// (keep bloomx/bloomx.go). Folding a greenfield public name to root stops
	// the F78 lift↔collapse ping-pong across Go/Python/JS.
	if len(parts) == 2 {
		stem := strings.TrimSuffix(parts[1], filepath.Ext(parts[1]))
		stem = strings.TrimSuffix(stem, "_test")
		if parts[0] != "" && strings.EqualFold(parts[0], stem) {
			pkgDir := parts[0]
			if wd != "" && wd != "." {
				pkgDir = filepath.Join(wd, parts[0])
			}
			if dirHasImplSources(pkgDir) {
				return path
			}
			return parts[1]
		}
	}
	// pkg/pkg/file → pkg/file
	if len(parts) >= 3 {
		collapsed := make([]string, 0, len(parts))
		for i := 0; i < len(parts); i++ {
			if i+1 < len(parts) && parts[i] == parts[i+1] && parts[i] != "" &&
				!strings.Contains(parts[i], ".") {
				continue
			}
			collapsed = append(collapsed, parts[i])
		}
		return strings.Join(collapsed, "/")
	}
	return path
}

// detectDeclaredLayoutUnder infers layout from manifests under wd (not process CWD).
func detectDeclaredLayoutUnder(wd, taskInput string) declaredLayout {
	topMig := wantsTopLevelMigrations(strings.ToLower(taskInput))
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(wd, name))
		return err == nil
	}
	switch {
	case exists("go.mod"):
		// F52/F70: only force internal/ for a real service tree. A leftover
		// internal/<mod>/ burial must not lock later writes into internal/.
		// Public-library tasks keep root empty even when genuine private
		// helpers (internal/heap) exist — those must not bury public API.
		if wantsPublicLibraryLayout(strings.ToLower(taskInput)) {
			return declaredLayout{root: "", lang: "go", topMigrations: topMig}
		}
		if goInternalIsServiceTreeAt(wd) {
			return declaredLayout{root: "internal", lang: "go", topMigrations: topMig}
		}
		return declaredLayout{root: "", lang: "go", topMigrations: topMig}
	case exists("Cargo.toml"):
		return declaredLayout{root: "src", lang: "rust", topMigrations: topMig}
	case exists("package.json"), exists("tsconfig.json"):
		if wantsPublicLibraryLayout(strings.ToLower(taskInput)) {
			return declaredLayout{root: "", lang: "javascript", topMigrations: topMig}
		}
		return declaredLayout{root: "src", lang: "javascript", topMigrations: topMig}
	case exists("pyproject.toml"), exists("requirements.txt"), exists("setup.py"):
		if wantsPublicLibraryLayout(strings.ToLower(taskInput)) {
			return declaredLayout{root: "", lang: "python", topMigrations: topMig}
		}
		if exists("app") || exists(filepath.Join("app", "__init__.py")) || exists(filepath.Join("app", "main.py")) {
			return declaredLayout{root: "app", lang: "python", topMigrations: topMig}
		}
		return declaredLayout{root: "app", lang: "python", topMigrations: topMig}
	default:
		// Fall back to task-only detection (may still be empty).
		layout := detectDeclaredLayout(taskInput)
		layout.topMigrations = layout.topMigrations || topMig
		return layout
	}
}

// goServiceInternalPkgs are conventional Go service/app packages under
// internal/. Helper names (heap, concurrency, queue, worker) are NOT here —
// those are private library helpers and must not flip layout.root to internal/.
var goServiceInternalPkgs = map[string]bool{
	"handlers": true, "handler": true, "config": true, "storage": true,
	"middleware": true, "auth": true, "routes": true, "controllers": true,
	"api": true, "db": true, "database": true, "models": true,
	"services": true, "routers": true, "crud": true,
	"repository": true, "repo": true, "domain": true, "adapters": true,
	"ports": true, "server": true, "http": true,
}

// goInternalIsServiceTreeAt reports whether internal/ is a real service tree
// (handlers, config, storage, … or cmd/ alongside internal/) rather than a
// mistaken burial of the public module package at internal/<mod>/ or a
// genuine private helper (internal/heap). F70: the latter must not flip
// layout.root to internal and lock later writes into the burial.
func goInternalIsServiceTreeAt(wd string) bool {
	root := nonEmptyWD(wd)
	internal := filepath.Join(root, "internal")
	info, err := os.Stat(internal)
	if err != nil || !info.IsDir() {
		return false
	}
	mod := goModuleDirNameAt(wd)
	if mod == "" && (wd == "" || wd == ".") {
		mod = goModuleDirName()
	}
	ents, err := os.ReadDir(internal)
	if err != nil {
		return false
	}
	hasHelperOrOther := false
	for _, e := range ents {
		name := e.Name()
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if mod != "" && strings.EqualFold(name, mod) {
			continue
		}
		if strings.EqualFold(name, "doc") || strings.EqualFold(name, "debug") {
			continue
		}
		if goServiceInternalPkgs[strings.ToLower(name)] {
			return true
		}
		hasHelperOrOther = true
	}
	if !hasHelperOrOther {
		return false
	}
	// cmd/ next to internal/ is the Go service layout, even if the internal
	// package names are not in the conventional list.
	cmd := filepath.Join(root, "cmd")
	if st, err := os.Stat(cmd); err == nil && st.IsDir() {
		return true
	}
	// Non-standard internal packages (auto, plugins, …) count as a service
	// tree only when the public module is not already a root/top-level library.
	// A public lib with genuine helpers (jobpq.go + internal/heap) must not lock
	// later writes into internal/.
	if publicGoLibraryPresentAt(wd, mod) {
		return false
	}
	return true
}

func publicGoLibraryPresentAt(wd, mod string) bool {
	if strings.TrimSpace(mod) == "" {
		return false
	}
	root := nonEmptyWD(wd)
	if dirHasImplGoSources(filepath.Join(root, mod)) {
		return true
	}
	for pkg := range goPackageNamesInDir(root, false) {
		if pkg != "main" && strings.EqualFold(pkg, mod) {
			return true
		}
	}
	return false
}

// unburyModuleNamedInternalPathAt lifts internal/<mod>/… to <mod>/….
// Cross-cuts charter ForbidInternalLib so the first tool write cannot persist
// a public-library burial even before phase1.md exists.
func unburyModuleNamedInternalPathAt(wd, path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return path
	}
	mod := ""
	if strings.TrimSpace(wd) != "" {
		mod = goModuleDirNameAt(wd)
	}
	if mod == "" {
		mod = goModuleDirName()
	}
	if mod == "" {
		return path
	}
	lower := strings.ToLower(path)
	prefix := "internal/" + strings.ToLower(mod)
	if lower == prefix {
		return mod
	}
	if strings.HasPrefix(lower, prefix+"/") {
		rest := path[len(prefix)+1:]
		if rest == "" {
			if dirHasImplGoSources(filepath.Join(nonEmptyWD(wd), mod)) {
				return mod
			}
			return mod + ".go"
		}
		// Existing <mod>/ tree wins; otherwise land at repo root so we do not
		// invent a parallel package directory (F78).
		if dirHasImplGoSources(filepath.Join(nonEmptyWD(wd), mod)) {
			return mod + "/" + rest
		}
		return rest
	}
	return path
}

// privateImplDirPrefixes are directories that mean "not part of the public API".
// Go's internal/ is compiler-enforced; _internal/ and src/internal/ are the
// same pattern in Python/JS trees. Do not invent these for other languages.
var privateImplDirPrefixes = []string{"internal/", "_internal/", "src/internal/"}

func privateImplRelPath(path string) (rest string, ok bool) {
	path = filepath.ToSlash(path)
	lower := strings.ToLower(path)
	for _, prefix := range privateImplDirPrefixes {
		if lower == strings.TrimSuffix(prefix, "/") {
			return "", true
		}
		if strings.HasPrefix(lower, prefix) {
			return path[len(prefix):], true
		}
	}
	return "", false
}

// liftPrivateDirPublicAPIAt moves a public-API file out of a private dir.
// Genuine private helpers (internal/heap with package heap) stay put.
// Only the misplaced file is lifted — the helper tree is not deleted.
func liftPrivateDirPublicAPIAt(wd, path, content string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return path
	}
	rest, inPrivate := privateImplRelPath(path)
	if !inPrivate || rest == "" {
		return path
	}
	parts := strings.Split(rest, "/")
	sub := parts[0]
	base := parts[len(parts)-1]
	if sub == "" || base == "" {
		return path
	}

	// Cross-lang: public package identity buried under a private prefix.
	if isPublicProjectPackageNameAt(wd, sub) {
		return publicPackageDestAt(wd, strings.Join(parts[1:], "/"), base)
	}

	if content == "" || !strings.HasSuffix(strings.ToLower(base), ".go") {
		return path
	}
	pkg := parseGoPackageClause(content)
	pkg = strings.TrimSuffix(pkg, "_test")
	if pkg == "" || pkg == "main" {
		return path
	}
	if !isPublicProjectPackageNameAt(wd, pkg) {
		return path
	}
	return publicPackageDestAt(wd, base, base)
}

func publicPackageDestAt(wd, rest, base string) string {
	if rest == "" {
		rest = base
	}
	names := projectPackageNamesAt(wd)
	for n := range names {
		if dirHasImplSources(filepath.Join(nonEmptyWD(wd), n)) {
			if !strings.Contains(rest, "/") {
				return n + "/" + rest
			}
			return n + "/" + base
		}
	}
	if strings.Contains(rest, "/") {
		return base
	}
	return rest
}

func nonEmptyWD(wd string) string {
	if strings.TrimSpace(wd) == "" {
		return "."
	}
	return wd
}

// isPublicProjectPackageNameAt reports whether name is the project's public
// package identity from language manifests (go.mod / package.json / Cargo.toml /
// pyproject). Cross-lang: used to stop pkg/pkg.ext ↔ pkg.ext ping-pong.
func isPublicProjectPackageNameAt(wd, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for n := range projectPackageNamesAt(wd) {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

func projectPackageNamesAt(wd string) map[string]bool {
	wd = nonEmptyWD(wd)
	out := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		out[s] = true
	}
	add(goModuleDirNameAt(wd))
	if data, err := os.ReadFile(filepath.Join(wd, "package.json")); err == nil {
		add(parseJSONStringField(string(data), "name"))
	}
	if data, err := os.ReadFile(filepath.Join(wd, "Cargo.toml")); err == nil {
		add(parseTOMLName(string(data)))
	}
	if data, err := os.ReadFile(filepath.Join(wd, "pyproject.toml")); err == nil {
		add(parseTOMLName(string(data)))
	}
	if data, err := os.ReadFile(filepath.Join(wd, "setup.cfg")); err == nil {
		add(parseTOMLName(string(data)))
	}
	return out
}

func parseJSONStringField(raw, key string) string {
	needle := `"` + key + `"`
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(needle):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = rest[colon+1:]
	q1 := strings.Index(rest, `"`)
	if q1 < 0 {
		return ""
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, `"`)
	if q2 < 0 {
		return ""
	}
	name := rest[:q2]
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t\n") {
		return ""
	}
	return name
}

func parseTOMLName(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "name") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		name = strings.Trim(name, `"'`)
		name = strings.ReplaceAll(name, "-", "_")
		if name != "" && !strings.ContainsAny(name, " \t") {
			return name
		}
	}
	return ""
}

func remapIntoSrc(path string, parts []string, base, stem, ext string, lang string) string {
	if lang == "rust" {
		// Rust: keep lib.rs/main.rs at src/; lift other modules under src/.
		if len(parts) == 1 && (base == "lib.rs" || base == "main.rs" || base == "mod.rs") {
			return "src/" + base
		}
		if !strings.HasPrefix(path, "src/") && isCodeExt(ext) {
			return "src/" + path
		}
		return path
	}
	// JS/TS/Python src layout.
	if (len(parts) == 1 || (len(parts) == 2 && parts[0] == stem)) && isCodeExt(ext) {
		// N1-2: bare sql.js / Node.js are packages, not src modules.
		if isDependencyOrRuntimeFilename(base) {
			return path
		}
		switch stem {
		case "main", "index", "app":
			return "src/" + base
		default:
			if layoutTopPkgs[stem] || len(parts) == 1 {
				return "src/" + path
			}
		}
	}
	if layoutTopPkgs[parts[0]] {
		return "src/" + path
	}
	return path
}

func isCodeExt(ext string) bool {
	switch ext {
	case ".py", ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".rs", ".java", ".kt", ".cs":
		return true
	default:
		return false
	}
}

// Back-compat wrappers used by older tests / call sites.
func taskWantsAppLayout(taskInput string) bool {
	return detectDeclaredLayout(taskInput).root == "app"
}

func remapToAppLayout(path string) string {
	return remapToDeclaredLayout(path, declaredLayout{root: "app", lang: "python"})
}
