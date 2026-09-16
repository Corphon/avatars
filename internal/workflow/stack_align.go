package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// PreferredDB is a language-agnostic storage stack preference.
type PreferredDB string

const (
	DBNone     PreferredDB = ""
	DBSQLite   PreferredDB = "sqlite"
	DBPostgres PreferredDB = "postgres"
	DBMySQL    PreferredDB = "mysql"
	DBMongo    PreferredDB = "mongodb"
)

var (
	sqliteTokens   = []string{"sqlite", "modernc.org/sqlite", "mattn/go-sqlite3", "better-sqlite", "sql.js"}
	postgresTokens = []string{"postgresql", "postgres", "pgx", "lib/pq", "psycopg", "sqlx/pgx"}
	mysqlTokens    = []string{"mysql", "mariadb", "gorm.io/driver/mysql"}
	mongoTokens    = []string{"mongodb", "mongo.driver", "pymongo"}
)

// DetectPreferredDB infers the intended database from user input and on-disk
// manifests (go.mod / requirements / package.json / Cargo.toml). User input
// wins over manifests when they conflict.
func DetectPreferredDB(input, projectRoot string) PreferredDB {
	fromInput := detectDBFromText(input)
	fromDisk := detectDBFromProject(projectRoot)
	if fromInput != DBNone {
		return fromInput
	}
	return fromDisk
}

func detectDBFromText(text string) PreferredDB {
	lower := strings.ToLower(text)
	// Explicit negation: "不要 PostgreSQL" / "not postgres"
	negPostgres := strings.Contains(lower, "不要 postgres") || strings.Contains(lower, "不要postgresql") ||
		strings.Contains(lower, "not postgres") || strings.Contains(lower, "no postgres") ||
		strings.Contains(lower, "不要 postgresql") || strings.Contains(lower, "without postgres")
	// F23: "不是 SQLite" / "不要数据库" must NOT lock Builder to SQLite.
	negSQLite := containsSQLiteNegation(lower)
	negAnyDB := containsDatabaseNegation(lower)
	posSQLite := strings.Contains(lower, "用 sqlite") || strings.Contains(lower, "使用 sqlite") ||
		strings.Contains(lower, "use sqlite") || strings.Contains(lower, "with sqlite") ||
		strings.Contains(lower, "存储用 sqlite") || strings.Contains(lower, "强制 sqlite")
	hasSQLite := containsAny(lower, sqliteTokens)
	hasPostgres := containsAny(lower, postgresTokens)
	hasMySQL := containsAny(lower, mysqlTokens)
	hasMongo := containsAny(lower, mongoTokens)

	if negAnyDB && !posSQLite && !hasPostgres && !hasMySQL && !hasMongo {
		return DBNone
	}
	if hasSQLite && negSQLite && !posSQLite {
		// Token hit only via negation ("不是 SQLite") — no stack lock.
		hasSQLite = false
	}

	if hasSQLite && (negPostgres || !hasPostgres) {
		return DBSQLite
	}
	if hasSQLite && hasPostgres {
		// Both mentioned: prefer the one with stronger "use X" phrasing.
		if posSQLite {
			return DBSQLite
		}
	}
	if hasPostgres && !negPostgres {
		return DBPostgres
	}
	if hasSQLite {
		return DBSQLite
	}
	if hasMySQL {
		return DBMySQL
	}
	if hasMongo {
		return DBMongo
	}
	return DBNone
}

func containsSQLiteNegation(lower string) bool {
	needles := []string{
		"不是 sqlite", "不是sqlite", "不要 sqlite", "不要sqlite", "禁止 sqlite", "禁止sqlite",
		"别用 sqlite", "别用sqlite", "无需 sqlite", "不用 sqlite",
		"not sqlite", "no sqlite", "without sqlite", "don't use sqlite", "do not use sqlite",
		"never use sqlite", "avoid sqlite",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func containsDatabaseNegation(lower string) bool {
	needles := []string{
		"不是数据库", "不要数据库", "禁止数据库", "无数据库", "无需数据库", "不用数据库",
		"not a database", "no database", "without database", "don't use a database",
		"do not use a database", "no db ", "without db", "不是 db", "不要 db",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func detectDBFromProject(root string) PreferredDB {
	if root == "" {
		root = "."
	}
	// go.mod
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		lower := strings.ToLower(string(data))
		if containsAny(lower, sqliteTokens) {
			return DBSQLite
		}
		if containsAny(lower, postgresTokens) {
			return DBPostgres
		}
		if containsAny(lower, mysqlTokens) {
			return DBMySQL
		}
		if containsAny(lower, mongoTokens) {
			return DBMongo
		}
	}
	for _, name := range []string{"requirements.txt", "pyproject.toml", "Pipfile"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			lower := strings.ToLower(string(data))
			if containsAny(lower, postgresTokens) {
				return DBPostgres
			}
			if containsAny(lower, sqliteTokens) || strings.Contains(lower, "aiosqlite") {
				return DBSQLite
			}
			if containsAny(lower, mysqlTokens) {
				return DBMySQL
			}
			if containsAny(lower, mongoTokens) {
				return DBMongo
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		lower := strings.ToLower(string(data))
		if strings.Contains(lower, "pg") || strings.Contains(lower, "postgres") {
			return DBPostgres
		}
		if strings.Contains(lower, "better-sqlite") || strings.Contains(lower, "sqlite3") {
			return DBSQLite
		}
		if strings.Contains(lower, "mysql") {
			return DBMySQL
		}
		if strings.Contains(lower, "mongodb") {
			return DBMongo
		}
	}
	return DBNone
}

func containsAny(lower string, tokens []string) bool {
	for _, t := range tokens {
		if strings.Contains(lower, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// AlignStackDecisions rewrites conflicting Key Decisions / phase doc mentions
// to match the preferred DB. Language-agnostic (Go/Python/JS/Rust manifests).
// Returns a short human summary of changes (empty if none).
func AlignStackDecisions(projectRoot, input string) (string, error) {
	preferred := DetectPreferredDB(input, projectRoot)
	if preferred == DBNone {
		return "", nil
	}
	var changes []string
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	if data, err := os.ReadFile(planPath); err == nil {
		updated, n := rewriteStackInPlan(string(data), preferred)
		if n > 0 {
			if err := WriteFileAtomic(planPath, []byte(updated), 0644); err != nil {
				return "", err
			}
			changes = append(changes, fmt.Sprintf("avatars_plan.md Key Decisions → %s (%d edits)", preferred, n))
		}
	}
	// Phase docs 1..12
	for n := 1; n <= 12; n++ {
		p := PhaseDocPath(projectRoot, n)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		updated, count := rewriteStackInText(string(data), preferred)
		if count > 0 {
			if err := WriteFileAtomic(p, []byte(updated), 0644); err != nil {
				return "", err
			}
			changes = append(changes, fmt.Sprintf("phase%d.md → %s (%d edits)", n, preferred, count))
		}
	}
	if len(changes) == 0 {
		return "", nil
	}
	return strings.Join(changes, "; "), nil
}

func rewriteStackInPlan(plan string, preferred PreferredDB) (string, int) {
	lines := strings.Split(plan, "\n")
	edits := 0
	inDecisions := false
	today := time.Now().UTC().Format("2006-01-02")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Key Decisions") {
			inDecisions = true
			continue
		}
		if inDecisions && strings.HasPrefix(trimmed, "## ") {
			inDecisions = false
		}
		if !inDecisions {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "|---") || strings.HasPrefix(trimmed, "| Date") {
			continue
		}
		lower := strings.ToLower(line)
		if !decisionConflicts(lower, preferred) {
			continue
		}
		lines[i] = rewriteDecisionRow(line, preferred, today)
		edits++
	}
	return strings.Join(lines, "\n"), edits
}

func decisionConflicts(lower string, preferred PreferredDB) bool {
	switch preferred {
	case DBSQLite:
		return containsAny(lower, postgresTokens) || containsAny(lower, mysqlTokens)
	case DBPostgres:
		return containsAny(lower, sqliteTokens) && !containsAny(lower, postgresTokens)
	case DBMySQL:
		return containsAny(lower, postgresTokens) || containsAny(lower, sqliteTokens)
	case DBMongo:
		return containsAny(lower, postgresTokens) || containsAny(lower, sqliteTokens) || containsAny(lower, mysqlTokens)
	default:
		return false
	}
}

func rewriteDecisionRow(line string, preferred PreferredDB, today string) string {
	parts := strings.Split(line, "|")
	// | Date | Decision | Rationale |
	if len(parts) < 4 {
		return line
	}
	decision, rationale := dbDecisionText(preferred)
	// Keep table shape: empty, date, decision, rationale, empty
	parts[1] = " " + today + " "
	parts[2] = " " + decision + " "
	parts[3] = " " + rationale + " "
	return strings.Join(parts, "|")
}

func dbDecisionText(preferred PreferredDB) (decision, rationale string) {
	switch preferred {
	case DBSQLite:
		return "Database: SQLite (e.g. modernc.org/sqlite / aiosqlite / better-sqlite3)",
			"Aligned with user/task constraint and project manifests; keep storage local and portable"
	case DBPostgres:
		return "Database: PostgreSQL (e.g. pgx / psycopg)",
			"Aligned with user/task constraint for production-grade relational storage"
	case DBMySQL:
		return "Database: MySQL/MariaDB",
			"Aligned with user/task constraint"
	case DBMongo:
		return "Database: MongoDB",
			"Aligned with user/task constraint for document storage"
	default:
		return "Database: (unspecified)", "No stack constraint detected"
	}
}

var (
	rePostgresWord = regexp.MustCompile(`(?i)PostgreSQL|Postgres`)
	reSQLiteWord   = regexp.MustCompile(`(?i)SQLite`)
	rePgx          = regexp.MustCompile(`(?i)\bpgx\b|lib/pq|sqlx/pgx|psycopg2?`)
)

func rewriteStackInText(content string, preferred PreferredDB) (string, int) {
	if preferred != DBSQLite {
		// Only auto-rewrite toward SQLite for the common NL smoke conflict
		// (plan says Postgres, user demanded SQLite). Other directions are
		// plan-row only to avoid over-editing phase prose.
		return content, 0
	}
	edits := 0
	out := content
	if rePostgresWord.MatchString(out) {
		out = rePostgresWord.ReplaceAllString(out, "SQLite")
		edits++
	}
	if rePgx.MatchString(out) {
		out = rePgx.ReplaceAllString(out, "modernc.org/sqlite (or language SQLite driver)")
		edits++
	}
	_ = reSQLiteWord
	return out, edits
}

// StackConstraintPrompt returns a short Builder/Critic lock line when a DB
// preference is known.
func StackConstraintPrompt(input, projectRoot string) string {
	preferred := DetectPreferredDB(input, projectRoot)
	if preferred == DBNone {
		return ""
	}
	decision, rationale := dbDecisionText(preferred)
	return fmt.Sprintf("\n=== STACK LOCK ===\n%s\nRationale: %s\nDo NOT introduce a conflicting database stack.\n", decision, rationale)
}

// envNameRe extracts explicit ENV / environment variable names from prompts
// and phase docs (cross-lang: NOTEBOARD_API_KEY, MY_SERVICE_TOKEN, …).
var envNameRe = regexp.MustCompile(`\b([A-Z][A-Z0-9_]{2,63})\b`)

var envNameNoise = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"HTTP": true, "HTTPS": true, "JSON": true, "HTML": true, "CSS": true,
	"SQL": true, "API": true, "URL": true, "URI": true, "UUID": true,
	"CRUD": true, "REST": true, "JWT": true, "CORS": true, "UTF": true,
	"PHASE": true, "TODO": true, "FIXME": true, "NOTE": true, "WARN": true,
	"TRUE": true, "FALSE": true, "NULL": true, "NONE": true,
	"SQLITE": true, "POSTGRES": true, "MYSQL": true, "MONGODB": true,
	"FASTAPI": true, "EXPRESS": true, "DJANGO": true, "FLASK": true,
}

// envNameTooGeneric are tokens that must never become ENV LOCK alone (D2/C5).
// Bare KEY / API_KEY cause Builder to bind the wrong name (e.g. KEY vs CLIPVAULT_API_KEY).
var envNameTooGeneric = map[string]bool{
	"KEY": true, "TOKEN": true, "SECRET": true, "PASSWORD": true, "PASS": true, "AUTH": true,
	"API_KEY": true, "API_KEYS": true, "API_TOKEN": true, "API_SECRET": true,
	"ACCESS_TOKEN": true, "SECRET_KEY": true, "AUTH_TOKEN": true, "AUTH_KEY": true,
}

// isGenericEnvName reports names too vague to lock (no project prefix).
func isGenericEnvName(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	if u == "" || envNameNoise[u] || envNameTooGeneric[u] {
		return true
	}
	// Single segment like KEY already covered; require a project-ish prefix.
	if !strings.Contains(u, "_") {
		return true
	}
	return false
}

// DetectRequiredEnvNames finds env-var-like tokens near auth/key wording in
// user/phase text. Language-agnostic: works for Go/Python/JS/Rust prompts.
func DetectRequiredEnvNames(texts ...string) []string {
	joined := strings.Join(texts, "\n")
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	lower := strings.ToLower(joined)
	// Only scan when auth/env context is present to avoid noise.
	authish := strings.Contains(lower, "api key") || strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "鉴权") || strings.Contains(lower, "环境变量") ||
		strings.Contains(lower, "environ") || strings.Contains(lower, "x-api-key") ||
		strings.Contains(lower, "authorization") || strings.Contains(lower, "bearer") ||
		strings.Contains(lower, "secret") || strings.Contains(lower, "token")
	if !authish {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range envNameRe.FindAllString(joined, -1) {
		if envNameNoise[m] || isGenericEnvName(m) {
			continue
		}
		// Prefer names that look like secrets/keys.
		u := strings.ToUpper(m)
		if !(strings.Contains(u, "KEY") || strings.Contains(u, "TOKEN") ||
			strings.Contains(u, "SECRET") || strings.Contains(u, "PASS") ||
			strings.HasSuffix(u, "_ID") || strings.Contains(u, "AUTH")) {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// EnvConstraintPrompt returns an ENV LOCK block when the *active phase*
// (not the full multi-phase NL prompt) names specific environment variables.
// B5: scanning the whole user prompt leaked Phase-2 keys into Phase-1 Builder.
// C5: when the user NL names a specific key, prefer it over phase-invented
// generics (e.g. SLOTBOOK_API_KEY beats API_KEYS). Cross-language getenv styles.
func EnvConstraintPrompt(input, projectRoot string) string {
	active := 1
	var phaseTexts []string
	authish := false
	if projectRoot != "" {
		if plan, err := ReadWorkflowDoc(projectRoot, "plan"); err == nil {
			pm := ParsePlanMeta(plan)
			if pm.ActivePhase > 0 {
				active = pm.ActivePhase
			}
			if raw, err := os.ReadFile(PhaseDocPath(projectRoot, active)); err == nil {
				phaseText := string(raw)
				phaseTexts = append(phaseTexts, phaseText)
				lower := strings.ToLower(phaseText)
				authish = strings.Contains(lower, "auth") || strings.Contains(lower, "鉴权") ||
					strings.Contains(lower, "api key") || strings.Contains(lower, "api-key") ||
					strings.Contains(lower, "x-api-key") || strings.Contains(lower, "env") ||
					strings.Contains(lower, "environment") || strings.Contains(lower, "getenv") ||
					strings.Contains(lower, "process.env")
			}
		}
	}
	if !authish {
		return ""
	}
	// User NL (persisted or live input) is the fidelity source for env names.
	// C5/D2: always consult user_requirement.md even when the live input is a
	// short continue prompt without env tokens (e.g. "auth phase").
	var userParts []string
	if scoped := StripOtherPhaseSections(input, active); strings.TrimSpace(scoped) != "" {
		userParts = append(userParts, scoped)
	}
	if persisted := LoadUserRequirement(projectRoot); persisted != "" {
		ps := StripOtherPhaseSections(persisted, active)
		if strings.TrimSpace(ps) == "" {
			ps = persisted
		}
		userParts = append(userParts, ps)
	}
	userScoped := strings.Join(userParts, "\n")
	userNames := DetectRequiredEnvNames(userScoped)
	phaseNames := DetectRequiredEnvNames(phaseTexts...)
	var names []string
	if len(userNames) > 0 {
		// Prefer user-named envs; PreferSpecific drops API_KEY when *_API_KEY present.
		names = PreferSpecificEnvNames(userNames)
	} else {
		names = PreferSpecificEnvNames(append(append([]string{}, phaseNames...), userNames...))
	}
	if len(names) == 0 {
		names = PreferSpecificEnvNames(phaseNames)
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("\n=== ENV LOCK ===\nRequired environment variable name(s): %s\nUse EXACTLY these names (os.Getenv / process.env / std::env / getenv).\nDo NOT invent alternate names (e.g. API_KEY / API_KEYS when SLOTBOOK_API_KEY is required).\nDo NOT supply a default permanent secret (e.g. getenv(name, \"dev-key\")).\nDo NOT chain fallback aliases such as getenv(\"NOTEBOARD_API_KEY\", getenv(\"API_KEY\", ...)) — read ONLY the listed name(s).\n", strings.Join(names, ", "))
}

// PreferSpecificEnvNames drops generic short names when a more specific
// sibling exists (P9-5): e.g. drop API_KEY when NOTEBOARD_API_KEY is present.
// D2/C5: also drops bare KEY / API_KEY even when they are the only candidate —
// locking a truncated name is worse than no ENV LOCK.
func PreferSpecificEnvNames(names []string) []string {
	filtered := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || isGenericEnvName(n) || seen[n] {
			continue
		}
		seen[n] = true
		filtered = append(filtered, n)
	}
	if len(filtered) <= 1 {
		return filtered
	}
	drop := map[string]bool{}
	for _, a := range filtered {
		for _, b := range filtered {
			if a == b {
				continue
			}
			// Drop A when B ends with _A (NOTEBOARD_API_KEY vs API_KEY).
			if strings.HasSuffix(b, "_"+a) {
				drop[a] = true
			}
		}
	}
	out := make([]string, 0, len(filtered))
	for _, n := range filtered {
		if drop[n] {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return filtered
	}
	return out
}
