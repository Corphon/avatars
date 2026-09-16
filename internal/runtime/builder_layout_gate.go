package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/projectfiles"
)

// taskRequiresMigrations reports whether the user/task explicitly asked for a
// migrations/ layout (cross-lang: Alembic, SQL, Prisma, Diesel, Flyway, …).
func taskRequiresMigrations(taskInput string) bool {
	lower := strings.ToLower(taskInput)
	needles := []string{
		"migrations/", "migrations\\", "migrations、", ",migrations", "、migrations",
		" alembic", "migration files", "prisma migrate", "prisma/schema",
		"diesel migration", "flyway", "liquibase", "typeorm migration",
		"knex migrate", "drizzle", "golang-migrate", "goose migration",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// migrationsLayoutPresent reports whether a language-appropriate migrations
// tree exists with at least one SQL (or alembic) artifact.
func migrationsLayoutPresent(wd string) bool {
	if hasSQLMigrationFiles(filepath.Join(wd, "internal", "storage", "migrations")) {
		return true
	}
	if hasSQLMigrationFiles(filepath.Join(wd, "migrations")) {
		return true
	}
	// V8: alembic.ini alone is not enough — need at least one version revision.
	if hasAlembicVersionFiles(wd) {
		return true
	}
	return false
}

func hasAlembicVersionFiles(wd string) bool {
	candidates := []string{
		filepath.Join(wd, "alembic", "versions"),
		filepath.Join(wd, "migrations", "versions"),
	}
	for _, dir := range candidates {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if strings.HasSuffix(name, ".py") && !strings.HasPrefix(name, "__") {
				return true
			}
		}
	}
	return false
}

func hasSQLMigrationFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".sql") {
			return true
		}
	}
	return false
}

// preferredMigrationsStubPath chooses where to place a gap-fill migration stub.
// When the task explicitly asks for migrations/, prefer that tree over alembic/
// (prompt-declared layout wins — P9-4, cross-lang).
func preferredMigrationsStubPath(wd, taskInput string) string {
	lower := strings.ToLower(taskInput)
	preferMigrationsDir := strings.Contains(lower, "migrations/") ||
		strings.Contains(lower, "migrations\\") ||
		strings.Contains(lower, "、migrations") ||
		strings.Contains(lower, ",migrations")

	// R7-2: Go greenfield prefers top-level migrations/ (prompt-friendly) over
	// the old internal/storage/migrations bias.
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
		if hasSQLMigrationFiles(filepath.Join(wd, "migrations")) {
			return "migrations/001_init.sql"
		}
		if !preferMigrationsDir && hasSQLMigrationFiles(filepath.Join(wd, "internal", "storage", "migrations")) {
			return "internal/storage/migrations/001_init.sql"
		}
		return "migrations/001_init.sql"
	}
	if _, err := os.Stat(filepath.Join(wd, "Cargo.toml")); err == nil {
		return "migrations/001_init.sql"
	}
	// Node: prefer Prisma when schema exists, else SQL migrations/.
	if _, err := os.Stat(filepath.Join(wd, "prisma", "schema.prisma")); err == nil {
		return "prisma/migrations/20260101000000_init/migration.sql"
	}
	if _, err := os.Stat(filepath.Join(wd, "package.json")); err == nil {
		return "migrations/001_init.sql"
	}
	// Prompt asked for migrations/ — honor it before alembic.ini heuristics.
	if preferMigrationsDir {
		if _, err := os.Stat(filepath.Join(wd, "pyproject.toml")); err == nil {
			return "migrations/versions/001_init.py"
		}
		if _, err := os.Stat(filepath.Join(wd, "requirements.txt")); err == nil {
			return "migrations/versions/001_init.py"
		}
		return "migrations/001_init.sql"
	}
	// Prefer existing Alembic tree layout when present.
	if _, err := os.Stat(filepath.Join(wd, "migrations", "alembic.ini")); err == nil {
		return "migrations/versions/001_init.py"
	}
	if _, err := os.Stat(filepath.Join(wd, "migrations", "versions")); err == nil {
		return "migrations/versions/001_init.py"
	}
	// V8: alembic.ini alone is not enough — stub a versions revision.
	if _, err := os.Stat(filepath.Join(wd, "alembic.ini")); err == nil {
		return "alembic/versions/001_init.py"
	}
	if _, err := os.Stat(filepath.Join(wd, "pyproject.toml")); err == nil {
		return "alembic/versions/001_init.py"
	}
	if _, err := os.Stat(filepath.Join(wd, "requirements.txt")); err == nil {
		return "migrations/versions/001_init.py"
	}
	return "migrations/001_init.sql"
}

// checkMissingMigrationsLayout returns a human-readable gap when the task
// required migrations/ but none exist on disk yet (also checks in-memory files).
func checkMissingMigrationsLayout(wd, taskInput string, pending []builderCodeFile) string {
	if !taskRequiresMigrations(taskInput) {
		return ""
	}
	if migrationsLayoutPresent(wd) {
		return ""
	}
	if pendingSatisfiesMigrations(pending) {
		return ""
	}
	return preferredMigrationsStubPath(wd, taskInput)
}

// pendingSatisfiesMigrations requires a real SQL migration or Alembic revision
// under versions/ — not env.py / script.py.mako alone (W5).
func pendingSatisfiesMigrations(pending []builderCodeFile) bool {
	for _, f := range pending {
		p := strings.ToLower(filepath.ToSlash(f.Path))
		base := filepath.Base(p)
		if strings.HasPrefix(base, "__") {
			continue
		}
		if strings.HasSuffix(p, ".sql") && (strings.Contains(p, "migration") || strings.Contains(p, "/migrations/") || strings.Contains(p, "prisma/")) {
			return true
		}
		if strings.Contains(p, "/versions/") && strings.HasSuffix(p, ".py") {
			return true
		}
	}
	return false
}

// collectBuilderFinalizeGaps returns stubs still missing after tool-loop /
// early converge (W1/W5/W6/P8-3): migrations revision + declared layout packages.
func collectBuilderFinalizeGaps(wd, taskInput string, pending []builderCodeFile) []builderCodeFile {
	var out []builderCodeFile
	pendingAll := append([]builderCodeFile{}, pending...)
	if stub := checkMissingMigrationsLayout(wd, taskInput, pendingAll); stub != "" {
		out = append(out, builderCodeFile{Path: stub, Content: defaultMigrationsStubContent(stub, taskInput)})
		pendingAll = append(pendingAll, out[len(out)-1])
	}
	if stub := checkMissingRustEntrypoint(wd, pendingAll); stub != "" {
		out = append(out, builderCodeFile{Path: stub, Content: defaultRustMainStub()})
		pendingAll = append(pendingAll, out[len(out)-1])
	}
	for _, stub := range checkMissingDeclaredLayoutDirs(wd, taskInput, pendingAll) {
		out = append(out, stub)
		pendingAll = append(pendingAll, stub)
	}
	return out
}

// checkMissingRustEntrypoint stubs src/main.rs when Cargo.toml exists but no
// crate entrypoint is present (R10-8). Cross-check pending writes too.
func checkMissingRustEntrypoint(wd string, pending []builderCodeFile) string {
	hasCargo := false
	if _, err := os.Stat(filepath.Join(wd, "Cargo.toml")); err == nil {
		hasCargo = true
	}
	for _, f := range pending {
		base := filepath.Base(filepath.ToSlash(f.Path))
		if base == "Cargo.toml" {
			hasCargo = true
		}
		p := filepath.ToSlash(strings.ToLower(f.Path))
		if strings.HasSuffix(p, "src/main.rs") || strings.HasSuffix(p, "src/lib.rs") {
			if strings.TrimSpace(f.Content) != "" {
				return ""
			}
		}
	}
	if !hasCargo {
		return ""
	}
	if rustEntrypointPresent(wd) {
		return ""
	}
	return "src/main.rs"
}

func defaultRustMainStub() string {
	return "fn main() {\n    println!(\"noteboard placeholder — replace with real server bootstrap\");\n}\n"
}

// checkMissingDeclaredLayoutDirs stubs missing prompt-declared packages under
// app|src|internal (cross-lang). Covers app/core, app/api, app/db, etc.
func checkMissingDeclaredLayoutDirs(wd, taskInput string, pending []builderCodeFile) []builderCodeFile {
	lower := strings.ToLower(taskInput)
	layout := detectDeclaredLayout(taskInput)
	if layout.root == "" && wd != "" {
		layout = detectDeclaredLayoutUnder(wd, taskInput)
	}
	if layout.root == "" {
		return nil
	}
	// Declared subpackages commonly required by prompts (language-agnostic names).
	candidates := []string{"core", "api", "db", "models", "auth", "handlers", "middleware", "routes", "services"}
	var missing []builderCodeFile
	for _, name := range candidates {
		needles := []string{
			layout.root + "/" + name,
			layout.root + "\\" + name,
		}
		needed := false
		for _, n := range needles {
			if strings.Contains(lower, strings.ToLower(n)) {
				needed = true
				break
			}
		}
		if !needed {
			continue
		}
		dirRel := layout.root + "/" + name
		if _, err := os.Stat(filepath.Join(wd, filepath.FromSlash(dirRel))); err == nil {
			continue
		}
		pendingHas := false
		prefix := strings.ToLower(dirRel) + "/"
		for _, f := range pending {
			p := strings.ToLower(filepath.ToSlash(f.Path))
			if p == strings.ToLower(dirRel) || strings.HasPrefix(p, prefix) {
				pendingHas = true
				break
			}
		}
		if pendingHas {
			continue
		}
		stubPath, stubContent := layoutPackageStub(layout, name)
		if stubPath == "" {
			continue
		}
		missing = append(missing, builderCodeFile{Path: stubPath, Content: stubContent})
	}
	return missing
}

func layoutPackageStub(layout declaredLayout, pkg string) (path, content string) {
	// C4: never stub a package dir when a sibling module file already exists.
	switch layout.lang {
	case "python", "":
		if layout.root == "app" || layout.root == "src" {
			if fileExists(layout.root + "/" + pkg + ".py") {
				return "", ""
			}
			return layout.root + "/" + pkg + "/__init__.py", "# " + pkg + " package\n"
		}
	case "go":
		if layout.root == "internal" {
			return "internal/" + pkg + "/" + pkg + ".go", "package " + pkg + "\n"
		}
	case "javascript", "typescript":
		if layout.root == "src" || layout.root == "app" {
			ext := ".js"
			if layout.lang == "typescript" {
				ext = ".ts"
			}
			for _, e := range []string{".js", ".ts", ".tsx", ".mjs"} {
				if fileExists(layout.root + "/" + pkg + e) {
					return "", ""
				}
			}
			return layout.root + "/" + pkg + "/index" + ext, "// " + pkg + " package\n"
		}
	case "rust":
		if layout.root == "src" {
			return "src/" + pkg + ".rs", "// " + pkg + " module\n"
		}
	}
	if layout.root == "app" {
		if fileExists("app/" + pkg + ".py") {
			return "", ""
		}
		return "app/" + pkg + "/__init__.py", "# " + pkg + " package\n"
	}
	return "", ""
}

// checkMissingAPIPackage stubs app/api when the task requires that layout (W6).
// Kept for back-compat; prefer checkMissingDeclaredLayoutDirs.
func checkMissingAPIPackage(wd, taskInput string, pending []builderCodeFile) string {
	for _, f := range checkMissingDeclaredLayoutDirs(wd, taskInput, pending) {
		if strings.Contains(filepath.ToSlash(f.Path), "/api/") || strings.HasSuffix(filepath.ToSlash(f.Path), "/api/__init__.py") {
			return f.Path
		}
	}
	return ""
}

// countProjectSourceFiles counts non-trivial source/config files for honest
// converge reporting (W1).
func countProjectSourceFiles(wd string) int {
	n := 0
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			if info != nil && info.IsDir() {
				if projectfiles.ShouldSkipWalkDir(info.Name()) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		if name == ".gitkeep" || name == ".keep" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".py", ".go", ".ts", ".tsx", ".js", ".jsx", ".sql", ".toml", ".ini",
			".txt", ".md", ".json", ".yml", ".yaml", ".rs", ".java", ".cs":
			n++
		}
		return nil
	})
	return n
}

// defaultMigrationsStubContent returns SQL or Alembic Python stub content.
func defaultMigrationsStubContent(path, taskInput string) string {
	if strings.HasSuffix(strings.ToLower(path), ".py") {
		return defaultAlembicRevisionStub(taskInput)
	}
	return defaultMigrationsStubSQL(taskInput)
}

// defaultAlembicRevisionStub is a minimal Alembic revision when only alembic.ini exists.
func defaultAlembicRevisionStub(taskInput string) string {
	lower := strings.ToLower(taskInput)
	var ops strings.Builder
	ops.WriteString("    # Minimal usable revision — replace with real ORM ops as models land.\n")
	if strings.Contains(lower, "user") || strings.Contains(lower, "jwt") || strings.Contains(lower, "auth") {
		ops.WriteString("    op.execute(\"\"\"\n")
		ops.WriteString("    CREATE TABLE IF NOT EXISTS users (\n")
		ops.WriteString("      id INTEGER PRIMARY KEY,\n")
		ops.WriteString("      email TEXT UNIQUE NOT NULL,\n")
		ops.WriteString("      hashed_password TEXT NOT NULL,\n")
		ops.WriteString("      created_at TEXT\n")
		ops.WriteString("    )\n")
		ops.WriteString("    \"\"\")\n")
	} else {
		ops.WriteString("    op.execute(\"\"\"\n")
		ops.WriteString("    CREATE TABLE IF NOT EXISTS schema_migrations (\n")
		ops.WriteString("      version TEXT PRIMARY KEY,\n")
		ops.WriteString("      applied_at TEXT\n")
		ops.WriteString("    )\n")
		ops.WriteString("    \"\"\")\n")
	}
	return fmt.Sprintf(`"""auto stub revision (task required migrations)"""

from alembic import op

revision = "001_init"
down_revision = None
branch_labels = None
depends_on = None


def upgrade() -> None:
%s

def downgrade() -> None:
    pass
`, ops.String())
}

// defaultMigrationsStubSQL returns a usable SQLite DDL stub when Builder
// omitted migrations/ (L4: stub immediately instead of long LLM gap-regen).
func defaultMigrationsStubSQL(taskInput string) string {
	lower := strings.ToLower(taskInput)
	var b strings.Builder
	b.WriteString("-- Auto-created migrations stub (task required migrations/)\n")
	b.WriteString("CREATE TABLE IF NOT EXISTS schema_migrations (\n")
	b.WriteString("  version TEXT PRIMARY KEY,\n")
	b.WriteString("  applied_at TEXT\n")
	b.WriteString(");\n\n")
	if strings.Contains(lower, "user") || strings.Contains(lower, "jwt") || strings.Contains(lower, "auth") {
		b.WriteString("CREATE TABLE IF NOT EXISTS users (\n")
		b.WriteString("  id INTEGER PRIMARY KEY AUTOINCREMENT,\n")
		b.WriteString("  email TEXT NOT NULL UNIQUE,\n")
		b.WriteString("  hashed_password TEXT NOT NULL,\n")
		b.WriteString("  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP\n")
		b.WriteString(");\n\n")
	}
	if strings.Contains(lower, "workspace") {
		b.WriteString("CREATE TABLE IF NOT EXISTS workspaces (\n")
		b.WriteString("  id INTEGER PRIMARY KEY AUTOINCREMENT,\n")
		b.WriteString("  name TEXT NOT NULL,\n")
		b.WriteString("  owner_id INTEGER,\n")
		b.WriteString("  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP\n")
		b.WriteString(");\n\n")
	}
	if strings.Contains(lower, "document") {
		b.WriteString("CREATE TABLE IF NOT EXISTS documents (\n")
		b.WriteString("  id INTEGER PRIMARY KEY AUTOINCREMENT,\n")
		b.WriteString("  title TEXT NOT NULL,\n")
		b.WriteString("  content TEXT NOT NULL DEFAULT '',\n")
		b.WriteString("  workspace_id INTEGER,\n")
		b.WriteString("  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP\n")
		b.WriteString(");\n")
	}
	return b.String()
}

// taskRequiresHealthEndpoint reports Phase1 /health success criteria.
func taskRequiresHealthEndpoint(taskInput string) bool {
	lower := strings.ToLower(taskInput)
	return strings.Contains(lower, "/health") ||
		strings.Contains(lower, "health-check") ||
		strings.Contains(lower, "health check") ||
		strings.Contains(lower, "健康检查")
}

// healthEndpointPresentInTree scans sources for a /health route (PY4).
func healthEndpointPresentInTree(wd string) bool {
	found := false
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			if info != nil && info.IsDir() {
				if projectfiles.ShouldSkipWalkDir(info.Name()) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		if ext != ".go" && ext != ".py" && ext != ".js" && ext != ".ts" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		s := string(data)
		if strings.Contains(s, `"/health"`) || strings.Contains(s, `'/health'`) || strings.Contains(s, "`/health`") {
			found = true
			return filepath.SkipAll
		}
		if strings.Contains(s, `HandleFunc("/health"`) || strings.Contains(s, "@app.get(\"/health\")") ||
			strings.Contains(s, "@app.get('/health')") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// healthPayloadLooksOK reports whether sources include {"status":"ok"}-ish shape.
func healthPayloadLooksOK(wd string) bool {
	ok := false
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			if info != nil && info.IsDir() {
				if projectfiles.ShouldSkipWalkDir(info.Name()) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		if ext != ".go" && ext != ".py" && ext != ".js" && ext != ".ts" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		s := strings.ToLower(string(data))
		if !strings.Contains(s, "/health") {
			return nil
		}
		if strings.Contains(s, `"status"`) && (strings.Contains(s, `"ok"`) || strings.Contains(s, "'ok'") ||
			strings.Contains(s, `"healthy"`) || strings.Contains(s, "'healthy'")) {
			ok = true
			return filepath.SkipAll
		}
		return nil
	})
	return ok
}
