package runtime

import (
	"os"
	"regexp"
	"strings"

	"avatars/internal/workflow"
)

// checkUserRequirementGaps compares the user's original task input against
// generated code to detect missing features. The checklist audit only matches
// keywords from plan tasks — this catches requirements not captured in the plan.
//
// Returns a list of missing requirements (empty if all found).
// Signals are intentionally cross-language (Go/Python/JS/TS/Rust/…).
func checkUserRequirementGaps(userInput, codeText string) []string {
	lower := strings.ToLower(userInput)
	codeLower := strings.ToLower(codeText)

	// Requirement patterns: keyword → what code should contain
	checks := []struct {
		keyword     string
		codeSignal  string
		description string
	}{
		{"浏览器", "http.listenandserve|http.handlefunc|http.handler|servehttp|fastapi|flask|express|axum|actix|hyper|@app.get|@app.post|http.server", "HTTP server"},
		{"browser", "http.listenandserve|http.handlefunc|http.handler|servehttp|fastapi|flask|express|axum|actix|hyper|@app.get|@app.post|http.server", "HTTP server"},
		{"http", "http.listenandserve|http.handlefunc|http.handler|servehttp|fastapi|flask|express|axum|actix|hyper|@app.get|@app.post|reqwest|ureq", "HTTP server"},
		{"web", "http.listenandserve|http.handlefunc|http.handler|servehttp|fastapi|flask|express|axum|actix|hyper|@app.get|@app.post", "HTTP server"},
		{"网页", "http.listenandserve|http.handlefunc|http.handler|servehttp|fastapi|flask|express|axum|actix|hyper|@app.get|@app.post", "HTTP server"},
		{"api", "http.listenandserve|http.handlefunc|http.handler|servehttp|encoding/json|fastapi|express|axum|serde_json|@app.get|@app.post|json.stringify", "HTTP API"},
		{"rest", "http.listenandserve|http.handlefunc|http.handler|servehttp|fastapi|express|axum|@app.get|@app.post", "REST API"},
		{"数据库", "sql.open|database/sql|sqlite|postgres|sqlalchemy|aiosqlite|prisma|diesel|sqlx|sequelize|typeorm|peewee", "database"},
		{"database", "sql.open|database/sql|sqlite|postgres|sqlalchemy|aiosqlite|prisma|diesel|sqlx|sequelize|typeorm|peewee", "database"},
		{"json", "encoding/json|json.marshal|json.unmarshal|json.newencoder|json.newdecoder|serde_json|json.loads|json.dumps|json.stringify|json.parse|ujson", "JSON support"},
		{"/health", `"/health"|'/health'|/health`, "/health endpoint"},
		{"health check", `"/health"|'/health'`, "/health endpoint"},
		{"健康检查", `"/health"|'/health'`, "/health endpoint"},
		{"migrations/", "migrations/|alembic|prisma/migrations|diesel migration|sqlx::migrate", "migrations layout"},
		{"fastapi", "fastapi|from fastapi", "FastAPI app"},
		// L8: accept app/core/database.py as fulfilling app/db/ layout asks.
		{"app/db", "app/db|app/core/database|from app.core.database|from app.db", "db package layout"},
		{"app/db/", "app/db|app/core/database|from app.core.database|from app.db", "db package layout"},
	}

	var missing []string
	seen := map[string]bool{}
	for _, c := range checks {
		if !strings.Contains(lower, c.keyword) {
			continue // user didn't ask for this
		}
		signals := strings.Split(c.codeSignal, "|")
		found := false
		for _, s := range signals {
			if strings.Contains(codeLower, s) {
				found = true
				break
			}
		}
		if !found && !seen[c.description] {
			missing = append(missing, c.description)
			seen[c.description] = true
		}
	}

	// B11: language-agnostic prompt→disk checks (module path, routes, schema fields).
	for _, g := range extractPromptSpecGaps(userInput, codeText) {
		if !seen[g] {
			missing = append(missing, g)
			seen[g] = true
		}
	}

	// F103: planned public API / injectable clock vs on-disk symbols.
	// Language-agnostic (Go/Python/JS/TS/Rust/Java).
	for _, g := range checkPlannedPublicAPIGaps(userInput, codeText) {
		if !seen[g] {
			missing = append(missing, g)
			seen[g] = true
		}
	}

	return missing
}

// extractPromptSpecGaps pulls concrete tokens from the user prompt and checks
// the code corpus. Works across Go/Python/JS/TS/Rust module & route styles.
func extractPromptSpecGaps(userInput, codeText string) []string {
	lower := strings.ToLower(userInput)
	codeLower := strings.ToLower(codeText)
	var gaps []string

	if m := regexp.MustCompile(`(?i)go\s+mod\s+init\s+([a-z0-9.\/_-]+)`).FindStringSubmatch(userInput); len(m) == 2 {
		mod := strings.ToLower(m[1])
		if !strings.Contains(codeLower, "module "+mod) && !strings.Contains(codeLower, `"`+mod+`"`) &&
			!strings.Contains(codeLower, mod) {
			gaps = append(gaps, "module path "+m[1])
		}
	} else if m := regexp.MustCompile(`(?i)module\s+` + "`" + `([a-z0-9.\/_-]+)` + "`").FindStringSubmatch(userInput); len(m) == 2 {
		mod := strings.ToLower(m[1])
		if !strings.Contains(codeLower, mod) {
			gaps = append(gaps, "module path "+m[1])
		}
	}

	routeRe := regexp.MustCompile(`(?i)\b(GET|POST|PUT|PATCH|DELETE)\s+(/[a-z0-9_{}\/-]+)`)
	for _, m := range routeRe.FindAllStringSubmatch(userInput, -1) {
		path := strings.ToLower(m[2])
		pathNorm := strings.ReplaceAll(path, "{id}", "")
		pathNorm = strings.ReplaceAll(pathNorm, "{}", "")
		segs := strings.Split(strings.Trim(path, "/"), "/")
		need := ""
		for i := len(segs) - 1; i >= 0; i-- {
			s := segs[i]
			if s == "" || s == "{id}" || s == "id" {
				continue
			}
			need = "/" + s
			break
		}
		if need == "" {
			need = pathNorm
		}
		if need != "" && !strings.Contains(codeLower, need) {
			gaps = append(gaps, "route "+m[1]+" "+m[2])
		}
	}

	if strings.Contains(lower, "sqlite") || strings.Contains(lower, "表") ||
		strings.Contains(lower, "schema") || strings.Contains(lower, "column") ||
		strings.Contains(lower, "字段") || strings.Contains(lower, "rooms:") ||
		strings.Contains(lower, "slots:") || strings.Contains(lower, "polls:") {
		fieldRe := regexp.MustCompile(`(?i)\b([a-z][a-z0-9_]{2,})\b`)
		prefer := map[string]bool{
			"title": true, "description": true, "closes_at": true, "updated_at": true,
			"created_at": true, "voter_token": true, "label": true, "position": true,
			"capacity": true, "booker_name": true, "starts_at": true, "ends_at": true,
			"start_time": true, "end_time": true, "note": true, "room_id": true,
		}
		asked := map[string]bool{}
		for _, m := range fieldRe.FindAllString(userInput, -1) {
			f := strings.ToLower(m)
			if prefer[f] {
				asked[f] = true
			}
		}
		for f := range asked {
			if !strings.Contains(codeLower, f) {
				gaps = append(gaps, "schema field "+f)
			}
		}
	}

	return gaps
}

// checkPlannedPublicAPIGaps compares planned public identifiers and an
// injectable clock/time source against on-disk public symbols (F103).
// Cross-language: Go exported names, Python def/class, JS/TS export,
// Rust pub fn/struct, Java public class/method.
func checkPlannedPublicAPIGaps(userInput, codeText string) []string {
	if strings.TrimSpace(userInput) == "" || strings.TrimSpace(codeText) == "" {
		return nil
	}
	var gaps []string
	if asksInjectableClock(userInput) && !codeHasInjectableClock(codeText) {
		gaps = append(gaps, "injectable clock on public constructor/config")
	}
	if asksInjectableClock(userInput) && waitUsesWallTimeDespiteClock(codeText) {
		gaps = append(gaps, "Wait/Sleep/After must observe injected clock, not wall time")
	}
	public := collectPublicSymbolSet(codeText)
	for _, ident := range plannedPublicIdentifiers(userInput) {
		if !publicSymbolPresent(public, codeText, ident) {
			gaps = append(gaps, "public API "+ident)
		}
	}
	return gaps
}

func asksInjectableClock(userInput string) bool {
	lower := strings.ToLower(userInput)
	needles := []string{
		"injectable clock", "injectable time", "fake clock", "fake time",
		"mock clock", "mock time", "clock injection", "可注入", "注入 clock",
		"注入clock", "time source", "clock:", "withclock",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	// "Clock" as a required config/API name, not the word "o'clock".
	if regexp.MustCompile(`(?i)\bClock\b`).MatchString(userInput) &&
		(strings.Contains(lower, "config") || strings.Contains(lower, "inject") ||
			strings.Contains(lower, "interface") || strings.Contains(lower, "构造") ||
			strings.Contains(lower, "可测试") || strings.Contains(lower, "test")) {
		return true
	}
	return false
}

func codeHasInjectableClock(codeText string) bool {
	// A Clock *interface* alone is not enough — it must be a public constructor
	// or config option (Go Clock field, Python clock=, JS fake timers, Rust Instant).
	// RE2 rejects counted repeats above 1000.
	configRes := []*regexp.Regexp{
		regexp.MustCompile(`(?is)type\s+\w*(Config|Options|Settings)\w*\s+struct\s*\{[^}]{0,800}\bClock\b`),
		regexp.MustCompile(`(?is)(?:class|struct)\s+\w*(Config|Options|Settings)\w*[^\{]{0,80}\{[^}]{0,800}\bclock\b`),
		regexp.MustCompile(`(?i)func\s+New\w*\s*\([^)]{0,200}\bClock\b`),
		regexp.MustCompile(`(?i)def\s+(__init__|__new__)\s*\([^)]{0,200}\bclock\b`),
		regexp.MustCompile(`(?i)constructor\s*\([^)]{0,200}\bclock\b`),
		regexp.MustCompile(`(?i)fn\s+new\s*\([^)]{0,200}\bClock\b`),
		regexp.MustCompile(`(?i)WithClock\s*\(`),
		regexp.MustCompile(`(?i)\bclock\s*[:=]\s*(None|null|undefined|nil|Option|Optional)`),
		regexp.MustCompile(`(?i)pub\s+clock\s*:`),
		regexp.MustCompile(`(?i)public\s+\w*Clock\s+\w+`),
	}
	for _, re := range configRes {
		if re.MatchString(codeText) {
			return true
		}
	}
	return false
}

func waitUsesWallTimeDespiteClock(codeText string) bool {
	lower := strings.ToLower(codeText)
	wall := []string{
		"time.after(", "time.sleep(", "<-time.after",
		"asyncio.sleep(", "settimeout(",
		"thread::sleep(", "thread.sleep(",
	}
	hasWall := false
	for _, w := range wall {
		if strings.Contains(lower, w) {
			hasWall = true
			break
		}
	}
	if !hasWall {
		return false
	}
	// Unexported wait() plus Wait() — polling Clock.Now with wall sleep still hangs.
	waitLike := regexp.MustCompile(`(?i)(?:func\s+(?:\([^)]*\)\s+)?wait\w*\s*\(|def\s+wait\w*\s*\(|function\s+wait\w*\s*\(|fn\s+wait\w*\s*\()`)
	return waitLike.MatchString(codeText)
}

var plannedIdentRe = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]{1,48})`")

func plannedPublicIdentifiers(userInput string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if plannedIdentNoise[strings.ToLower(name)] || seen[name] {
			return
		}
		if len(name) < 2 {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, m := range plannedIdentRe.FindAllStringSubmatch(userInput, -1) {
		add(m[1])
	}
	// Success-criteria style: Allow()/Wait(ctx)/Peek without backticks.
	callRe := regexp.MustCompile(`\b([A-Z][A-Za-z0-9]{1,40})\s*\(`)
	for _, m := range callRe.FindAllStringSubmatch(userInput, -1) {
		add(m[1])
	}
	return out
}

var plannedIdentNoise = map[string]bool{
	"if": true, "for": true, "func": true, "funcn": true, "http": true,
	"json": true, "true": true, "false": true, "none": true, "null": true,
	"this": true, "self": true, "test": true, "type": true, "name": true,
	"with": true, "from": true, "class": true, "return": true, "import": true,
	"phase": true, "plan": true, "todo": true, "must": true, "should": true,
	"error": true, "errors": true, "string": true, "int": true, "bool": true,
	"context": true, "config": true, "new": true, "get": true, "set": true,
	"read": true, "write": true, "file": true, "main": true, "init": true,
	"printf": true, "println": true, "len": true, "make": true, "chan": true,
	"go": true, "python": true, "javascript": true, "typescript": true,
	"rust": true, "java": true, "node": true, "npm": true, "cargo": true,
}

func collectPublicSymbolSet(codeText string) map[string]bool {
	out := map[string]bool{}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^func\s+(?:\([^)]+\)\s+)?([A-Z][A-Za-z0-9_]*)\s*\(`),
		regexp.MustCompile(`(?m)^type\s+([A-Z][A-Za-z0-9_]*)\s+`),
		regexp.MustCompile(`(?m)^def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		regexp.MustCompile(`(?m)^class\s+([A-Za-z_][A-Za-z0-9_]*)\s*[:\(]`),
		regexp.MustCompile(`(?m)(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		regexp.MustCompile(`(?m)export\s+(?:const|let|class|type|interface|enum)\s+([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`(?m)pub\s+(?:async\s+)?(?:fn|struct|enum|trait|type|const)\s+([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`(?m)public\s+(?:static\s+)?(?:final\s+)?(?:class|interface|enum|void|int|long|boolean|String|[\w.<>,\[\]]+)\s+([A-Za-z_][A-Za-z0-9_]*)\s*[\(\{]`),
	}
	for _, re := range patterns {
		for _, m := range re.FindAllStringSubmatch(codeText, -1) {
			out[m[1]] = true
			out[strings.ToLower(m[1])] = true
		}
	}
	return out
}

func publicSymbolPresent(public map[string]bool, codeText, ident string) bool {
	if public[ident] || public[strings.ToLower(ident)] {
		return true
	}
	// Fallback: identifier appears as a definition-ish token.
	re := regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(ident) + `\b`)
	return re.MatchString(codeText)
}

// authLooksFailOpen detects empty-key bypasses (B9), default permanent
// secrets like getenv(..., "dev-key") (C5), and empty-configured-key
// compare fail-open (Q1: `if key != s.apiKey` when both "" allows).
// Cross-language shapes.
func authLooksFailOpen(codeLower string) bool {
	defaultSecretRes := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:getenv|environ\.get|os\.getenv|process\.env\.\w+\s*\|\||env::var(?:_os)?)\s*\([^)]{0,80}(?:,|\|\||\?\?)\s*["']dev[-_]?key["']`),
		regexp.MustCompile(`(?i)(?:getenv|environ\.get|os\.getenv)\s*\(\s*["'][^"']*(?:KEY|SECRET|TOKEN|PASS|AUTH)[^"']*["']\s*,\s*["'][^"']{1,64}["']\s*\)`),
		regexp.MustCompile(`(?i)default\s*=\s*["']dev[-_]?key["']`),
		regexp.MustCompile(`(?i)(?:api_keys?|apikey)\s*=\s*["']dev[-_]?key["']`),
		regexp.MustCompile(`(?i)\|\|\s*["']dev[-_]?key["']`),
		regexp.MustCompile(`(?i)\?\?\s*["']dev[-_]?key["']`),
	}
	for _, re := range defaultSecretRes {
		if re.MatchString(codeLower) {
			return true
		}
	}
	// Q1: inequality compare alone is fail-open when configured key may be "".
	if authCompareLooksEmptyKeyFailOpen(codeLower) {
		return true
	}
	patterns := []string{
		`apikey == ""`, `api_key == ""`, `apiKey == ""`, `apikey == ''`, `api_key == ''`,
		`apikey==""`, `api_key==""`, `len(apikey) == 0`, `len(api_key) == 0`,
		`if not api_key`, `if not apikey`, `if !apikey`, `if !api_key`, `if (!apikey)`, `if (!api_key)`,
		`apikey is none`, `api_key is none`, `apikey is null`, `api_key is null`,
		`if apikey == "" {`, `if api_key == "" {`, `if h.apikey == ""`,
	}
	for _, p := range patterns {
		idx := strings.Index(codeLower, p)
		if idx < 0 {
			continue
		}
		window := codeLower[idx:]
		if len(window) > 280 {
			window = window[:280]
		}
		allowSignals := []string{
			"next(", "return next", "call_next", "handler(", "next.servehttp",
			"return null", "return none", "pass\n", "continue",
		}
		rejectSignals := []string{"unauthorized", "401", "forbidden", "statusunauthorized"}
		earliestAllow := -1
		for _, a := range allowSignals {
			if i := strings.Index(window, a); i >= 0 && (earliestAllow < 0 || i < earliestAllow) {
				earliestAllow = i
			}
		}
		earliestReject := -1
		for _, r := range rejectSignals {
			if i := strings.Index(window, r); i >= 0 && (earliestReject < 0 || i < earliestReject) {
				earliestReject = i
			}
		}
		if earliestAllow >= 0 && (earliestReject < 0 || earliestAllow < earliestReject) {
			return true
		}
	}
	return false
}

// authCompareLooksEmptyKeyFailOpen detects Q1-style gates:
//   if key != s.apiKey { unauthorized }
// When the configured key is unset (""), request key "" also matches → allow.
// Safe patterns reject empty configured keys before the compare.
func authCompareLooksEmptyKeyFailOpen(codeLower string) bool {
	compareRes := []*regexp.Regexp{
		regexp.MustCompile(`(?i)if\s+\w+\s*!=\s*(?:\w+\.)?api[_]?key\b`),
		regexp.MustCompile(`(?i)if\s+(?:\w+\.)?api[_]?key\s*!=\s*\w+`),
		regexp.MustCompile(`(?i)if\s+\w+\s*!==?\s*(?:\w+\.)?api[_]?key\b`),
		regexp.MustCompile(`(?i)if\s+\w+\s*!=\s*(?:\w+\.)?api[_]?secret\b`),
	}
	matched := false
	for _, re := range compareRes {
		if re.MatchString(codeLower) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	if configuredAPIKeyEmptyIsRejected(codeLower) {
		return false
	}
	return true
}

func configuredAPIKeyEmptyIsRejected(codeLower string) bool {
	rejectRes := []*regexp.Regexp{
		regexp.MustCompile(`(?i)if\s+(?:\w+\.)?api[_]?key\s*==\s*["']{2}[^;{]{0,40}\{[^}]{0,240}(?:unauthorized|401|forbidden|statusunauthorized|abort)`),
		regexp.MustCompile(`(?i)if\s+len\s*\(\s*(?:\w+\.)?api[_]?key\s*\)\s*==\s*0[^;{]{0,40}\{[^}]{0,240}(?:unauthorized|401|forbidden|statusunauthorized)`),
		regexp.MustCompile(`(?i)if\s+not\s+(?:\w+\.)?api[_]?key\s*:`),
		regexp.MustCompile(`(?i)if\s+!(?:\w+\.)?api[_]?key\s*\{[^}]{0,240}(?:unauthorized|401|forbidden)`),
		regexp.MustCompile(`(?i)if\s+(?:\w+\.)?api[_]?key\s*==\s*(?:null|none|undefined)`),
	}
	for _, re := range rejectRes {
		if re.MatchString(codeLower) {
			return true
		}
	}
	return false
}

// checkPhaseScopedRequirementGaps adds auth/delete/list-auth gaps only when the
// active phase is actually responsible for them (R7-3). Avoids failing Phase 1
// Critic preflight on a full multi-phase prompt that mentions Phase 2 auth.
// Auth/delete signals are language-agnostic (Go middleware, FastAPI Depends,
// Express middleware, Axum layers, …).
func checkPhaseScopedRequirementGaps(projectRoot, userInput, codeText string) []string {
	if !authGapsInScope(projectRoot, userInput) {
		return nil
	}
	lower := strings.ToLower(userInput)
	codeLower := strings.ToLower(codeText)
	var missing []string
	seen := map[string]bool{}
	add := func(desc string, ok bool) {
		if seen[desc] || ok {
			return
		}
		missing = append(missing, desc)
		seen[desc] = true
	}

	if promptAsksAPIAuth(lower) {
		add("API auth middleware", hasAuthGate(codeLower) && !authLooksFailOpen(codeLower))
		if hasAuthGate(codeLower) && authLooksFailOpen(codeLower) {
			add("auth fail-open (empty key allows writes)", false)
		}
	}
	if promptAsksDeleteEndpoint(lower) {
		add("DELETE HTTP endpoint", hasDeleteEndpoint(codeLower))
	}
	if requiresListAuth(lower) {
		add("list endpoint API auth", listEndpointLooksProtected(codeLower))
	}
	return missing
}

func authGapsInScope(projectRoot, userInput string) bool {
	lower := strings.ToLower(userInput)
	planContent, err := workflow.ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return promptAsksAPIAuth(lower) || requiresListAuth(lower) || promptAsksDeleteEndpoint(lower)
	}
	meta := workflow.ParsePlanMeta(planContent)
	// Do NOT assume ActivePhase>=2 means auth — only inspect active phase text.
	if meta.ActivePhase >= 1 && meta.ActivePhase <= len(meta.Phases) {
		goal := strings.ToLower(meta.Phases[meta.ActivePhase-1].Goal + " " + meta.Phases[meta.ActivePhase-1].Name)
		if phaseTextAsksAuth(goal) {
			return true
		}
	}
	if meta.ActivePhase >= 1 {
		if raw, err := os.ReadFile(workflow.PhaseDocPath(projectRoot, meta.ActivePhase)); err == nil {
			if phaseTextAsksAuth(strings.ToLower(string(raw))) {
				return true
			}
		}
	}
	return false
}

func phaseTextAsksAuth(lower string) bool {
	return strings.Contains(lower, "auth") || strings.Contains(lower, "鉴权") ||
		strings.Contains(lower, "api key") || strings.Contains(lower, "api-key") ||
		strings.Contains(lower, "x-api-key") || strings.Contains(lower, "middleware") ||
		strings.Contains(lower, "bearer") || strings.Contains(lower, "delete")
}

func promptAsksAPIAuth(lower string) bool {
	needles := []string{
		"api key", "api-key", "x-api-key", "鉴权", "authorization",
		"bearer token", "auth middleware", "authenticate",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func promptAsksDeleteEndpoint(lower string) bool {
	if strings.Contains(lower, "delete /") || strings.Contains(lower, "method delete") {
		return true
	}
	// "DELETE /snippets" style or "add delete endpoint"
	if strings.Contains(lower, "delete") &&
		(strings.Contains(lower, "endpoint") || strings.Contains(lower, "接口") ||
			strings.Contains(lower, "/snippets") || strings.Contains(lower, "handler")) {
		return true
	}
	return false
}

func requiresListAuth(userLower string) bool {
	needles := []string{
		"列表需鉴权",
		"写接口与列表需鉴权",
		"list needs auth",
		"list requires auth",
		"protect list",
		"lists need auth",
		"list endpoints require",
		"list must be authenticated",
		"authenticate list",
	}
	for _, n := range needles {
		if strings.Contains(userLower, n) {
			return true
		}
	}
	// Chinese: 写…列表…鉴权 ; English: write…list…auth in one ask.
	if strings.Contains(userLower, "列表") && strings.Contains(userLower, "鉴权") &&
		(strings.Contains(userLower, "写接口") || strings.Contains(userLower, "写")) {
		return true
	}
	if (strings.Contains(userLower, "list") && strings.Contains(userLower, "auth")) &&
		(strings.Contains(userLower, "write") || strings.Contains(userLower, "post") || strings.Contains(userLower, "create")) {
		return true
	}
	return false
}

func hasAuthGate(codeLower string) bool {
	signals := []string{
		"apikeymiddleware", "x-api-key", "api_key", "apikey",
		"authorization", "bearer ", "requireauth", "requires_auth",
		"login_required", "httpbearer", "httpmiddleware",
		"securityschem", "depends(", "canactivate", "use(auth",
		"authenticate", "auth_middleware", "authmiddleware",
		"layer(auth", "from_fn(auth", "middleware::",
	}
	for _, s := range signals {
		if strings.Contains(codeLower, s) {
			return true
		}
	}
	return false
}

func hasDeleteEndpoint(codeLower string) bool {
	signals := []string{
		"methoddelete", "http.methoddelete", "http::method::delete",
		".delete(", "router.delete", "app.delete", "@app.delete",
		`methods=["delete"]`, `methods=['delete']`,
		"deletesnippet", "delete_handler", "deletehandler",
	}
	for _, s := range signals {
		if strings.Contains(codeLower, s) {
			return true
		}
	}
	return false
}

// Cross-language: Go middleware wrap, FastAPI Depends/decorators, Express use/auth,
// Axum layers — any explicit auth binding near list routes.
var listAuthProtectRes = []*regexp.Regexp{
	regexp.MustCompile(`apikeymiddleware\s*\([^)]*list`),
	regexp.MustCompile(`middleware\s*\([^)]*list`),
	regexp.MustCompile(`require(?:s)?_?auth[^;\n]{0,120}list`),
	regexp.MustCompile(`@(?:require|auth|login_required|dependencies)[^\n]{0,80}\n[^\n]{0,120}list`),
	regexp.MustCompile(`depends\s*\([^)]*(?:api[_-]?key|auth|security|httpbearer)`),
	regexp.MustCompile(`\.(?:use|all)\s*\([^)]*(?:auth|apikey|bearer)`),
	regexp.MustCompile(`layer\s*\([^)]*auth`),
	regexp.MustCompile(`list[a-z0-9_]*\s*=\s*[^\n]*(?:auth|depend|middleware)`),
}

func listEndpointLooksProtected(codeLower string) bool {
	for _, re := range listAuthProtectRes {
		if re.MatchString(codeLower) {
			return true
		}
	}
	// Explicit Go-style wrap of List* handlers (kept as a clear positive).
	if strings.Contains(codeLower, "apikeymiddleware") &&
		(strings.Contains(codeLower, "apikeymiddleware(h.list") ||
			strings.Contains(codeLower, "apikeymiddleware(list")) {
		return true
	}
	return false
}
