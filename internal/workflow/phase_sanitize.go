package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// sanitizePhaseDocMarkdown cleans LLM phase docs before WritePhase (L7/V7):
//   - unglue mid-line H2 headings anywhere (H3: `Layer## Risks`)
//   - cut Tasks at first glued/corrupt checkbox line (e.g. "...).app/api/...")
//   - salvage H2 sections that were glued mid-Tasks (e.g. `base().## Risks`)
//   - drop duplicate ### task groups (same normalized title)
func sanitizePhaseDocMarkdown(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = unglueMarkdownHeadings(content)
	tasksIdx := indexH2(content, "task")
	if tasksIdx < 0 {
		return content
	}
	afterTasks := content[tasksIdx:]
	nextH2Rel := indexNextH2(afterTasks[1:])
	var before, tasksBody, after string
	before = content[:tasksIdx]
	if nextH2Rel >= 0 {
		cut := 1 + nextH2Rel
		tasksBody = afterTasks[:cut]
		after = afterTasks[cut:]
	} else {
		tasksBody = afterTasks
	}
	cleaned, salvaged := cutCorruptTaskLinesWithSalvage(tasksBody)
	tasksBody = dedupePhaseTaskGroups(cleaned)
	if salvaged != "" {
		after = salvaged + "\n" + after
	}
	return before + tasksBody + after
}

var constructCheckedBoxRe = regexp.MustCompile(`(?m)^(\s*- )\[(?:[xX>])\]`)

// resetConstructCheckboxes clears done/bookmark boxes on a freshly generated
// phase doc. Construct runs before sources exist, so [x] here is a lie.
func resetConstructCheckboxes(content string) string {
	return constructCheckedBoxRe.ReplaceAllString(content, "${1}[ ]")
}

// phaseTasksCoverScope reports whether ## Tasks checkboxes cover the nouns in
// Scope & Success Criteria checkboxes (B10). Language-agnostic markdown compare.
// Cross-provider: same ≥80% gate for all models — do not loosen for weak/fast models.
func phaseTasksCoverScope(content string) bool {
	scopeItems := scopeOrSuccessItems(content)
	if len(scopeItems) == 0 {
		return true
	}
	taskItems := checkboxItemsInH2(content, "task")
	if len(taskItems) == 0 {
		return false
	}
	taskBlob := strings.ToLower(strings.Join(taskItems, " "))
	covered := 0
	for _, s := range scopeItems {
		if scopeItemCoveredByTasks(s, taskBlob) {
			covered++
		}
	}
	// C2: require ≥80% Scope coverage (was 60% — skeleton Tasks still passed).
	return covered*10 >= len(scopeItems)*8
}

// phaseTasksUncoveredScope lists Scope/Success bullets that Tasks do not cover.
// Used for gap-targeted regenerate prompts and actionable error text (F9).
func phaseTasksUncoveredScope(content string) []string {
	scopeItems := scopeOrSuccessItems(content)
	if len(scopeItems) == 0 {
		return nil
	}
	taskBlob := strings.ToLower(strings.Join(checkboxItemsInH2(content, "task"), " "))
	var uncovered []string
	for _, s := range scopeItems {
		if !scopeItemCoveredByTasks(s, taskBlob) {
			uncovered = append(uncovered, s)
		}
	}
	return uncovered
}

func scopeOrSuccessItems(content string) []string {
	scopeItems := checkboxItemsInH2(content, "scope")
	if len(scopeItems) == 0 {
		scopeItems = checkboxItemsInH2(content, "success")
	}
	return scopeItems
}

func scopeItemCoveredByTasks(scopeItem, taskBlob string) bool {
	// Dependency/manifest constraints are not implementable Tasks — cover via
	// explicit dep wording OR non-skeleton implementation Tasks (cross-lang).
	if isDependencyConstraintScope(scopeItem) {
		if dependencyConstraintMentionedInTasks(taskBlob) {
			return true
		}
		if taskBlobLooksLikeImplementation(taskBlob) {
			return true
		}
	}
	// Build/test gate bullets (`go test ./...`, pytest, cargo test, npm test)
	// belong in Verification. Tasks cover them by implementing the library
	// or by mentioning tests — they must not require echoing the runner command.
	if isVerificationCommandScope(scopeItem) {
		if verificationMentionedInTasks(taskBlob) || taskBlobLooksLikeImplementation(taskBlob) {
			return true
		}
	}
	// F27/F58: when Scope names an API/symbol (`Contains`, `ulidx.Parse()`),
	// matching that identifier OR its short name (`Parse`) in Tasks is enough.
	if ids := identifierTokens(scopeItem); len(ids) > 0 {
		for _, id := range ids {
			if strings.Contains(taskBlob, id) {
				return true
			}
		}
		// Identifiers present but none hit — still try prose tokens as backup
		// (Tasks may rephrase without the exact symbol spelling).
	}
	tokens := substantiveTokens(scopeItem)
	if len(tokens) == 0 {
		return true
	}
	hit := 0
	for _, tok := range tokens {
		if strings.Contains(taskBlob, tok) {
			hit++
		}
	}
	need := len(tokens) / 2
	if need < 1 {
		need = 1
	}
	// Cap need at 2 when Scope is long prose: require key nouns, not essay echo.
	if need > 2 {
		need = 2
	}
	return hit >= need
}

// identifierTokens extracts high-signal symbols from a Scope bullet: names in
// backticks (`Contains`, `Merge`, `ulidx.Parse()`). Also emits short names
// (Parse from pkg.Parse) so Tasks need not echo the package prefix (F58).
// Plain CapWords are NOT used — too many false hits on "Add/Create/…" verbs.
func identifierTokens(s string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(tok string) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		tok = strings.Trim(tok, "`\"'()[]")
		if i := strings.IndexAny(tok, " \t/"); i > 0 {
			tok = tok[:i]
		}
		// Strip trailing call/type noise: Parse() / Parse[] → Parse
		tok = strings.TrimRight(tok, "()[]{}")
		if len(tok) < 3 || seen[tok] {
			return
		}
		seen[tok] = true
		out = append(out, tok)
		// pkg.Symbol / module.Symbol → also accept Symbol alone (F58).
		if i := strings.LastIndex(tok, "."); i >= 0 && i+1 < len(tok) {
			short := tok[i+1:]
			short = strings.TrimRight(short, "()[]{}")
			if len(short) >= 3 && !seen[short] && !looksLikePackageManifest(short) {
				seen[short] = true
				out = append(out, short)
			}
		}
	}
	for _, m := range backtickIdentRe.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	return out
}

var backtickIdentRe = regexp.MustCompile("`([^`]+)`")

// looksLikePackageManifest reports common package-manager / module filenames
// that are constraints, not APIs (go.mod, package.json, Cargo.toml, …).
func looksLikePackageManifest(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "yarn.lock",
		"pnpm-lock.yaml", "cargo.toml", "cargo.lock", "pyproject.toml",
		"requirements.txt", "poetry.lock", "composer.json", "gemfile",
		"gemfile.lock", "mix.exs", "pubspec.yaml":
		return true
	}
	return false
}

// isDependencyConstraintScope reports Scope bullets about "no external deps" /
// stdlib-only / lockfile purity — cross-lang, not a code feature Task (F58).
func isDependencyConstraintScope(scopeItem string) bool {
	lower := strings.ToLower(scopeItem)
	ids := identifierTokens(scopeItem)
	onlyManifests := len(ids) > 0
	for _, id := range ids {
		if !looksLikePackageManifest(id) && !strings.Contains(id, ".") {
			// bare "Parse" etc. — not a pure constraint bullet
			onlyManifests = false
			break
		}
		if strings.Contains(id, ".") && !looksLikePackageManifest(id) {
			onlyManifests = false
			break
		}
	}
	if onlyManifests && len(ids) > 0 {
		return true
	}
	depHints := []string{
		"no external", "without external", "zero dependencies", "zero dependency",
		"no dependencies", "no dependency", "dependency-free", "dependencies-free",
		"stdlib only", "standard library only", "std library only",
		"no third-party", "no third party", "pure standard",
		"无外部依赖", "无第三方依赖", "仅标准库", "不要外部依赖",
	}
	for _, h := range depHints {
		if strings.Contains(lower, h) {
			return true
		}
	}
	// "No external dependencies in go.mod" without backticks.
	if strings.Contains(lower, "dependenc") && (strings.Contains(lower, "go.mod") ||
		strings.Contains(lower, "package.json") || strings.Contains(lower, "cargo.toml") ||
		strings.Contains(lower, "pyproject") || strings.Contains(lower, "requirements.txt")) {
		return true
	}
	return false
}

// isVerificationCommandScope reports build/test gate bullets. Cross-language:
// go test / cargo test / pytest / npm test are Verification, not product Tasks.
func isVerificationCommandScope(scopeItem string) bool {
	lower := strings.ToLower(scopeItem)
	runners := []string{
		"go test", "go build", "go vet",
		"cargo test", "cargo build",
		"npm test", "pnpm test", "yarn test",
		"pytest", "python -m pytest", "python -m unittest",
		"mvn test", "gradle test",
		"dotnet test",
		"make test",
	}
	for _, r := range runners {
		if strings.Contains(lower, r) {
			return true
		}
	}
	if strings.Contains(lower, "test") && (strings.Contains(lower, "pass") ||
		strings.Contains(lower, "green") || strings.Contains(lower, "succeed")) {
		return true
	}
	return false
}

func verificationMentionedInTasks(taskBlob string) bool {
	hints := []string{
		"go test", "cargo test", "npm test", "pytest",
		"_test.", "test.go", "test.py", "test.ts", "test.js", "test.rs",
		".spec.", "_spec.",
		"unit test", "write tests", "add tests", "create tests",
		"测试", "跑绿",
	}
	for _, h := range hints {
		if strings.Contains(taskBlob, h) {
			return true
		}
	}
	return false
}

func dependencyConstraintMentionedInTasks(taskBlob string) bool {
	hints := []string{
		"go.mod", "package.json", "cargo.toml", "pyproject.toml", "requirements.txt",
		"no external", "stdlib", "standard library", "dependency", "dependencies",
		"go mod init", "npm init", "cargo init", "无外部", "标准库",
	}
	for _, h := range hints {
		if strings.Contains(taskBlob, h) {
			return true
		}
	}
	return false
}

// taskBlobLooksLikeImplementation reports Tasks that name real source paths /
// symbols rather than pure scaffolding (F58 soft cover for constraints).
func taskBlobLooksLikeImplementation(taskBlob string) bool {
	if len(strings.TrimSpace(taskBlob)) < 40 {
		return false
	}
	extHints := []string{".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs", ".java"}
	for _, ext := range extHints {
		if strings.Contains(taskBlob, ext) {
			return true
		}
	}
	verbs := []string{"implement", "create ", "add ", "write ", "parse", "encode", "decode", "export"}
	hit := 0
	for _, v := range verbs {
		if strings.Contains(taskBlob, v) {
			hit++
		}
	}
	return hit >= 2
}

// formatUncoveredScopeForPrompt builds a model-agnostic retry hint listing gaps.
func formatUncoveredScopeForPrompt(gaps []string) string {
	var b strings.Builder
	b.WriteString("Every Scope & Success Criteria checkbox MUST have a matching ## Tasks checkbox that implements it.\n")
	b.WriteString("If a Scope item names an API in backticks (e.g. `Contains`, `ulidx.Parse()`), the Task text MUST include that same name OR its short identifier (`Parse`).\n")
	b.WriteString("Dependency constraints (no external deps / go.mod / package.json) do not need a dedicated Task when implementation Tasks already deliver the library.\n")
	b.WriteString("Build/test gates (`go test ./...`, pytest, cargo test, npm test, `go build`) belong in Verification — do not reject Tasks for not echoing those commands.\n")
	b.WriteString("Skeleton-only Tasks are invalid.\n")
	if len(gaps) == 0 {
		return b.String()
	}
	b.WriteString("Uncovered Scope items — add an explicit `- [ ]` Task for EACH (keep existing good Tasks; keep Verification):\n")
	limit := len(gaps)
	if limit > 8 {
		limit = 8
	}
	for i := 0; i < limit; i++ {
		b.WriteString("- ")
		b.WriteString(gaps[i])
		b.WriteString("\n")
	}
	if len(gaps) > limit {
		b.WriteString(fmt.Sprintf("- ... and %d more uncovered Scope item(s)\n", len(gaps)-limit))
	}
	return b.String()
}

// formatUncoveredScopeForError appends a short gap list to thin-Tasks errors.
func formatUncoveredScopeForError(gaps []string) string {
	if len(gaps) == 0 {
		return ""
	}
	limit := len(gaps)
	if limit > 4 {
		limit = 4
	}
	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		g := strings.TrimSpace(gaps[i])
		if len(g) > 80 {
			g = g[:77] + "..."
		}
		parts = append(parts, g)
	}
	msg := "; uncovered: " + strings.Join(parts, " | ")
	if len(gaps) > limit {
		msg += fmt.Sprintf(" (+%d more)", len(gaps)-limit)
	}
	return msg
}

func checkboxItemsInH2(content, h2Keyword string) []string {
	idx := indexH2(content, h2Keyword)
	if idx < 0 {
		return nil
	}
	body := content[idx:]
	if rel := indexNextH2(body[1:]); rel >= 0 {
		body = body[:1+rel]
	}
	var items []string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "- [ ]"):
			items = append(items, strings.TrimSpace(t[len("- [ ]"):]))
		case strings.HasPrefix(t, "- [x]"), strings.HasPrefix(t, "- [X]"):
			items = append(items, strings.TrimSpace(t[len("- [x]"):]))
		}
	}
	return items
}

func substantiveTokens(s string) []string {
	lower := strings.ToLower(s)
	// Strip markdown noise.
	lower = strings.ReplaceAll(lower, "`", " ")
	lower = strings.ReplaceAll(lower, "*", " ")
	parts := regexp.MustCompile(`[^a-z0-9_/\.-]+`).Split(lower, -1)
	stop := map[string]bool{
		"the": true, "and": true, "with": true, "from": true, "that": true,
		"this": true, "for": true, "are": true, "is": true, "to": true, "a": true,
		"an": true, "of": true, "in": true, "on": true, "or": true, "be": true,
		"must": true, "should": true, "all": true, "any": true, "via": true,
		"returns": true, "return": true, "using": true, "into": true,
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.Trim(p, "./-")
		if len(p) < 4 || stop[p] || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func indexH2(content, keyword string) int {
	lines := strings.Split(content, "\n")
	off := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") && strings.Contains(strings.ToLower(t), keyword) {
			return off
		}
		off += len(line) + 1
	}
	return -1
}

func indexNextH2(content string) int {
	lines := strings.Split(content, "\n")
	off := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			return off
		}
		off += len(line) + 1
	}
	return -1
}

// gluedPathRe matches mid-sentence path glue without whitespace, e.g.
// "purposes).app/api/v1/__init__.py" or "purposes).app/"
var gluedPathRe = regexp.MustCompile(`\)\.[A-Za-z0-9_/\\.-]+\.(py|go|ts|tsx|js|jsx|sql|md)\b`)

// gluedHeadingRe matches Tasks lines that glue into the next H2, e.g.
// `Base = declarative_base().## Risks` (V7), “python-dotenv`.## Risks“ (W2),
// or `### 2. Data Model & Database Layer## Risks` (H3 — no punctuation before ##).
var gluedHeadingRe = regexp.MustCompile(`(?i)(?:\.+|[\x60A-Za-z0-9_)])\s*##\s+(Risks|Verification|Notes|Dependencies|Scope|Success)`)

// unglueHeadingRe splits any non-heading character glued directly onto ## H2.
// Broader than gluedHeadingRe: runs on the whole document before Tasks surgery.
var unglueHeadingRe = regexp.MustCompile(`(?m)([^\n#])(##\s+(?:Risks|Verification|Notes|Dependencies|Scope(?:\s*&\s*Success\s*Criteria)?|Success Criteria|Key Decisions)\b)`)

func unglueMarkdownHeadings(content string) string {
	return unglueHeadingRe.ReplaceAllString(content, "$1\n$2")
}

func cutCorruptTaskLines(tasksSection string) string {
	cleaned, _ := cutCorruptTaskLinesWithSalvage(tasksSection)
	return cleaned
}

// cutCorruptTaskLinesWithSalvage truncates Tasks at the first corrupt glue and
// returns any salvaged H2 section that was glued onto a task line (V7/W2).
func cutCorruptTaskLinesWithSalvage(tasksSection string) (cleaned, salvaged string) {
	lines := strings.Split(tasksSection, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- [") || strings.HasPrefix(t, "### ") {
			if gluedPathRe.MatchString(t) || gluedHeadingRe.MatchString(t) || looksLikeGluedPathResidue(t) {
				if prefix, rest, ok := splitGluedHeading(t); ok {
					if prefix != "" {
						out = append(out, prefix)
					}
					tail := append([]string{rest}, lines[i+1:]...)
					salvaged = strings.Join(tail, "\n")
				}
				break
			}
		}
		if gluedHeadingRe.MatchString(t) && !strings.HasPrefix(t, "## ") {
			if prefix, rest, ok := splitGluedHeading(t); ok {
				if prefix != "" {
					out = append(out, prefix)
				}
				tail := append([]string{rest}, lines[i+1:]...)
				salvaged = strings.Join(tail, "\n")
			}
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), salvaged
}

// splitGluedHeading finds `.`/`..` + `## Heading` glue and returns the cleaned
// task prefix (if any) plus the salvaged H2 line starting at `##`.
func splitGluedHeading(t string) (prefix, heading string, ok bool) {
	loc := gluedHeadingRe.FindStringIndex(t)
	if loc == nil {
		return "", "", false
	}
	hashAt := strings.Index(t[loc[0]:], "##")
	if hashAt < 0 {
		return "", "", false
	}
	abs := loc[0] + hashAt
	heading = strings.TrimSpace(t[abs:])
	if !strings.HasPrefix(heading, "## ") {
		return "", "", false
	}
	prefix = strings.TrimSpace(t[:abs])
	prefix = strings.TrimRight(prefix, " `.\"'")
	// H3: keep ### task-group titles after unglue (was discarded → SyncTodo lost groups).
	if prefix == "" || prefix == "-" {
		prefix = ""
	}
	return prefix, heading, true
}

func looksLikeGluedPathResidue(t string) bool {
	// e.g. ends with ").app/api..." or contains ").app/" mid-checkbox
	if strings.Contains(t, ").") && (strings.Contains(t, "/") || strings.Contains(t, "\\")) {
		if strings.HasPrefix(t, "- [") || strings.HasPrefix(t, "### ") {
			return true
		}
	}
	// V7/W2: `.## Risks` / ``.`## Risks`` without a path slash
	if gluedHeadingRe.MatchString(t) {
		return true
	}
	return false
}

// phaseDocHasEmptySuccessCriteria reports Scope & Success Criteria with no
// checklist bullets (V7: LLM left the section blank).
func phaseDocHasEmptySuccessCriteria(content string) bool {
	idx := indexH2(content, "success")
	if idx < 0 {
		idx = indexH2(content, "scope")
	}
	if idx < 0 {
		return false
	}
	section := content[idx:]
	if rel := indexNextH2(section[1:]); rel >= 0 {
		section = section[:1+rel]
	}
	for _, line := range strings.Split(section, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- [") {
			return false
		}
	}
	return true
}

// phaseDocHasTaskCheckboxes reports ## Tasks contains at least one checkbox (G9).
func phaseDocHasTaskCheckboxes(content string) bool {
	idx := indexH2(content, "task")
	if idx < 0 {
		return false
	}
	section := content[idx:]
	if rel := indexNextH2(section[1:]); rel >= 0 {
		section = section[:1+rel]
	}
	for _, line := range strings.Split(section, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- [") {
			return true
		}
	}
	return false
}

func dedupePhaseTaskGroups(tasksSection string) string {
	lines := strings.Split(tasksSection, "\n")
	type block struct {
		titleKey string
		lines    []string
		total    int
	}
	var header []string
	var blocks []block
	var cur *block
	started := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if !started {
			header = append(header, line)
			if strings.HasPrefix(t, "## ") {
				started = true
			}
			continue
		}
		if strings.HasPrefix(t, "### ") {
			if cur != nil {
				blocks = append(blocks, *cur)
			}
			key := normalizeChecklistLabel(strings.TrimPrefix(t, "### "))
			cur = &block{titleKey: key, lines: []string{line}}
			continue
		}
		if cur == nil {
			header = append(header, line)
			continue
		}
		cur.lines = append(cur.lines, line)
		if strings.HasPrefix(t, "- [") {
			cur.total++
		}
	}
	if cur != nil {
		blocks = append(blocks, *cur)
	}
	seen := map[string]int{} // key → index in kept
	var kept []block
	for _, b := range blocks {
		if b.titleKey == "" {
			kept = append(kept, b)
			continue
		}
		if i, ok := seen[b.titleKey]; ok {
			// Prefer richer child checklist.
			if b.total > kept[i].total {
				kept[i] = b
			}
			continue
		}
		seen[b.titleKey] = len(kept)
		kept = append(kept, b)
	}
	var out strings.Builder
	for i, h := range header {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(h)
	}
	for _, b := range kept {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(strings.Join(b.lines, "\n"))
	}
	return out.String()
}
