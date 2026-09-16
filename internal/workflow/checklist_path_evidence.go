package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reBacktickPath = regexp.MustCompile("`([^`]+)`")
	// Language-agnostic source/config basenames mentioned in checklist prose.
	reBareSourceFile = regexp.MustCompile(`(?i)\b([a-zA-Z0-9][a-zA-Z0-9_-]*\.(?:go|py|rs|js|jsx|ts|tsx|mjs|cjs|sql|toml|yaml|yml|json|md|html|css|proto|java|kt|swift|rb|php|cs))\b`)
)

// checklistItemHasLayoutDirEvidence reports whether the item names a package/dir
// that exists either at the declared path or under a common layout root alias
// (language-agnostic: handlers ↔ internal/handlers|app/handlers|src/handlers).
// Used by Critic soft-advance (R7-1).
func checklistItemHasLayoutDirEvidence(projectRoot, itemDesc string) bool {
	if strings.TrimSpace(projectRoot) == "" || strings.TrimSpace(itemDesc) == "" {
		return false
	}
	for _, rel := range extractLayoutDirCandidates(itemDesc) {
		for _, alias := range layoutDirAliases(rel) {
			dir := filepath.Join(projectRoot, filepath.FromSlash(alias))
			if dirHasAnySource(dir) {
				return true
			}
		}
	}
	return false
}

// Common layout roots across Go/Python/JS/TS/Rust greenfield trees.
var layoutRootPrefixes = []string{"", "internal/", "app/", "src/", "cmd/", "pkg/"}

var reLayoutDirToken = regexp.MustCompile(`(?i)\b((?:internal|app|src|cmd|pkg|migrations|alembic|prisma)(?:/[a-zA-Z0-9_./-]+)?|[a-zA-Z][a-zA-Z0-9_-]{1,24})/?\b`)

// extractLayoutDirCandidates pulls path-ish directory tokens from checklist prose.
func extractLayoutDirCandidates(itemDesc string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = filepath.ToSlash(strings.Trim(strings.TrimSpace(s), "/"))
		if s == "" || seen[s] {
			return
		}
		// Skip pure verbs / noise.
		switch strings.ToLower(s) {
		case "create", "implement", "add", "write", "build", "define", "setup",
			"phase", "package", "module", "file", "files", "test", "tests",
			"with", "from", "that", "this", "using", "for", "and", "the":
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, m := range reBacktickPath.FindAllStringSubmatch(itemDesc, -1) {
		if len(m) >= 2 {
			p := filepath.ToSlash(m[1])
			if strings.Contains(p, ".") {
				add(filepath.Dir(p))
			} else {
				add(p)
			}
		}
	}
	for _, m := range reLayoutDirToken.FindAllStringSubmatch(itemDesc, -1) {
		if len(m) >= 2 {
			add(m[1])
		}
	}
	return out
}

func layoutDirAliases(rel string) []string {
	rel = filepath.ToSlash(strings.Trim(rel, "/"))
	if rel == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = filepath.ToSlash(strings.Trim(s, "/"))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(rel)
	// Strip known roots then re-prefix under each layout root.
	base := rel
	for _, root := range []string{"internal/", "app/", "src/", "cmd/", "pkg/"} {
		if strings.HasPrefix(base, root) {
			base = strings.TrimPrefix(base, root)
			break
		}
	}
	if base == "" {
		base = rel
	}
	for _, prefix := range layoutRootPrefixes {
		add(prefix + base)
	}
	// migrations / alembic / prisma stay as-is plus nested storage aliases.
	switch strings.ToLower(base) {
	case "migrations":
		add("migrations")
		add("internal/storage/migrations")
		add("app/db/migrations")
		add("alembic/versions")
		add("prisma/migrations")
	case "alembic":
		add("alembic")
		add("alembic/versions")
		add("migrations/versions")
	}
	return out
}

func dirHasAnySource(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		switch {
		case strings.HasSuffix(name, ".go"),
			strings.HasSuffix(name, ".py"),
			strings.HasSuffix(name, ".rs"),
			strings.HasSuffix(name, ".ts"),
			strings.HasSuffix(name, ".tsx"),
			strings.HasSuffix(name, ".js"),
			strings.HasSuffix(name, ".jsx"),
			strings.HasSuffix(name, ".mjs"),
			strings.HasSuffix(name, ".cjs"),
			strings.HasSuffix(name, ".sql"),
			strings.HasSuffix(name, ".java"),
			strings.HasSuffix(name, ".kt"),
			strings.HasSuffix(name, ".cs"):
			return true
		}
	}
	return false
}

// checklistItemHasPathEvidence reports whether the checklist description names
// a file that exists under projectRoot (basename or relative path). Language-agnostic.
func checklistItemHasPathEvidence(projectRoot, itemDesc string) bool {
	if strings.TrimSpace(projectRoot) == "" || strings.TrimSpace(itemDesc) == "" {
		return false
	}
	// Feature / wiring work cannot be satisfied by path existence alone (E1).
	if checklistItemNeedsContentEvidence(itemDesc) {
		return false
	}
	names := extractReferencedPaths(itemDesc)
	if len(names) == 0 {
		return false
	}
	index := buildProjectBasenameIndex(projectRoot)
	for _, name := range names {
		name = filepath.ToSlash(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		// Direct relative path under project.
		if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(name))); err == nil {
			return true
		}
		base := strings.ToLower(filepath.Base(name))
		if base == "" {
			continue
		}
		if _, ok := index[base]; ok {
			return true
		}
	}
	return false
}

// checklistItemNeedsContentEvidence reports items that must be proven by source
// content (flags/ENV/wiring), not merely by a file path existing. Cross-language.
func checklistItemNeedsContentEvidence(itemDesc string) bool {
	if len(checklistFeatureTokens(itemDesc)) > 0 {
		return true
	}
	lower := strings.ToLower(itemDesc)
	wirey := strings.Contains(lower, "wire") || strings.Contains(lower, "extend") ||
		strings.Contains(lower, "add flag") || strings.Contains(lower, "add option") ||
		strings.Contains(lower, "hook up") || strings.Contains(lower, "接入")
	target := strings.Contains(lower, "flag") || strings.Contains(lower, "cli") ||
		strings.Contains(lower, "env") || strings.Contains(lower, "option") ||
		strings.Contains(lower, "argument") || strings.Contains(lower, "参数")
	return wirey && target
}

// checklistFeatureTokens extracts CLI flags and ENV-style names that must appear
// in source to count as done (cross-language: Go/Python/JS/Rust/…).
// Examples: `--delim`, `--how`, `CSVMESH_STRICT`, `CLIPVAULT_API_KEY`.
func checklistFeatureTokens(itemDesc string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	// Backtick / quote spans first.
	for _, m := range reBacktickPath.FindAllStringSubmatch(itemDesc, -1) {
		if len(m) < 2 {
			continue
		}
		inner := strings.TrimSpace(m[1])
		if inner == "" {
			continue
		}
		// Skip pure file paths.
		if strings.Contains(inner, "/") || strings.Contains(inner, `\`) ||
			regexp.MustCompile(`(?i)\.(go|py|rs|js|jsx|ts|tsx|mjs|cjs|java|kt|cs)$`).MatchString(inner) {
			continue
		}
		for _, part := range splitFeatureTokenParts(inner) {
			add(part)
		}
	}
	// Prose: --flag and ENV_VARS.
	for _, m := range regexp.MustCompile(`--[a-zA-Z][a-zA-Z0-9_-]*`).FindAllString(itemDesc, -1) {
		add(m)
	}
	for _, m := range regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`).FindAllString(itemDesc, -1) {
		// Avoid matching markdown headings noise; require underscore (ENV style).
		add(m)
	}
	return out
}

func splitFeatureTokenParts(inner string) []string {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return nil
	}
	var parts []string
	// `--how left|inner` → --how ; `status=active` left alone if no flag.
	if strings.HasPrefix(inner, "--") {
		fields := strings.Fields(inner)
		if len(fields) > 0 {
			flag := fields[0]
			// Strip value alternatives: --how left|inner → --how
			if i := strings.IndexAny(flag, "|="); i > 0 {
				flag = flag[:i]
			}
			parts = append(parts, flag)
		}
		return parts
	}
	if regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$`).MatchString(inner) {
		parts = append(parts, inner)
		return parts
	}
	// `left|inner` alone is not a feature token.
	return nil
}

func codeContainsFeatureToken(codeLower, token string) bool {
	tok := strings.ToLower(strings.TrimSpace(token))
	if tok == "" {
		return false
	}
	if strings.Contains(codeLower, tok) {
		return true
	}
	// CLI flags may appear without dashes in const/enum form: delim, how.
	if strings.HasPrefix(tok, "--") {
		bare := strings.TrimPrefix(tok, "--")
		if bare != "" && strings.Contains(codeLower, bare) {
			return true
		}
	}
	return false
}

func extractReferencedPaths(itemDesc string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, m := range reBacktickPath.FindAllStringSubmatch(itemDesc, -1) {
		if len(m) >= 2 {
			add(m[1])
		}
	}
	for _, m := range reBareSourceFile.FindAllStringSubmatch(itemDesc, -1) {
		if len(m) >= 2 {
			add(m[1])
		}
	}
	lower := strings.ToLower(itemDesc)
	// Manifest cues without backticks.
	for _, cue := range []string{"go.mod", "go.sum", "cargo.toml", "package.json", "pyproject.toml", "requirements.txt", "tsconfig.json"} {
		if strings.Contains(lower, cue) {
			add(cue)
		}
	}
	return out
}

func buildProjectBasenameIndex(projectRoot string) map[string]string {
	index := map[string]string{}
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() {
			switch name {
			case ".git", ".avatars", "avatars", "node_modules", "vendor", "target", "dist", "build", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		base := strings.ToLower(filepath.Base(rel))
		if base != "" {
			index[base] = filepath.ToSlash(rel)
		}
		return nil
	})
	return index
}

// criteriaOverlapsLaterPhaseGoals is true when a Success Criteria line looks
// more like a later phase's goal than the active phase (cross-phase pollution).
func criteriaOverlapsLaterPhaseGoals(projectRoot, itemDesc string, activePhase int) bool {
	if activePhase < 1 {
		return false
	}
	// X1: explicit "Phase N" with N > active always belongs to a later phase.
	if n := SuccessCriteriaPhaseNum(itemDesc); n > activePhase {
		return true
	}
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return false
	}
	meta := ParsePlanMeta(planContent)
	if activePhase >= len(meta.Phases) {
		return false
	}
	lowerDesc := strings.ToLower(itemDesc)
	activeGoal := strings.ToLower(meta.Phases[activePhase-1].Goal + " " + meta.Phases[activePhase-1].Name)
	for i := activePhase; i < len(meta.Phases); i++ {
		later := strings.ToLower(meta.Phases[i].Goal + " " + meta.Phases[i].Name)
		hitsLater := distinctiveTokenHits(lowerDesc, later)
		hitsActive := distinctiveTokenHits(lowerDesc, activeGoal)
		if hitsLater >= 2 && hitsLater > hitsActive {
			return true
		}
		// High-signal auth/DELETE tokens alone count as later-phase overlap.
		if hitsLater >= 1 && criteriaRequiresAuthOrDeleteEvidence(lowerDesc) &&
			phaseGoalLooksAuthDelete(later) && !phaseGoalLooksAuthDelete(activeGoal) {
			return true
		}
	}
	return false
}

func phaseGoalLooksAuthDelete(lowerGoal string) bool {
	return strings.Contains(lowerGoal, "auth") || strings.Contains(lowerGoal, "delete") ||
		strings.Contains(lowerGoal, "api key") || strings.Contains(lowerGoal, "api-key") ||
		strings.Contains(lowerGoal, "鉴权")
}

func distinctiveTokenHits(desc, goal string) int {
	if goal == "" || desc == "" {
		return 0
	}
	hits := 0
	for _, tok := range strings.FieldsFunc(goal, func(r rune) bool {
		return r == ' ' || r == ',' || r == '/' || r == '-' || r == ':' || r == '(' || r == ')' || r == '.'
	}) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if len(tok) < 4 {
			continue
		}
		switch tok {
		case "with", "from", "that", "this", "phase", "implement", "create", "build", "add", "support":
			continue
		}
		if strings.Contains(desc, tok) {
			hits++
		}
	}
	return hits
}

// MarkPhaseChecklistByPathEvidence marks unchecked Tasks items in phaseN.md
// when named files exist on disk. Does not touch Success Criteria that belong
// to later phases. Returns number marked.
func MarkPhaseChecklistByPathEvidence(projectRoot string, phaseNum int) int {
	if phaseNum < 1 {
		return 0
	}
	phasePath := PhaseDocPath(projectRoot, phaseNum)
	data, err := os.ReadFile(phasePath)
	if err != nil {
		return 0
	}
	phaseStr := string(data)
	codeText := collectGeneratedCodeText(projectRoot)
	lines := strings.Split(phaseStr, "\n")
	inTasks := false
	inSuccess := false
	marked := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inTasks = strings.HasPrefix(trimmed, "## Tasks")
			inSuccess = strings.Contains(trimmed, "Success Criteria")
			continue
		}
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, "- [ ]"))
		if inSuccess && criteriaOverlapsLaterPhaseGoals(projectRoot, desc, phaseNum) {
			continue
		}
		if !inTasks && !inSuccess {
			continue
		}
		if !isChecklistItemSatisfied(projectRoot, desc, codeText) && !checklistItemHasPathEvidence(projectRoot, desc) {
			continue
		}
		lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
		marked++
	}
	if marked == 0 {
		return 0
	}
	_ = WriteFileAtomic(phasePath, []byte(strings.Join(lines, "\n")), 0644)
	return marked
}

// UnmarkPhaseSuccessCriteriaFromLaterPhases clears [x] on Success Criteria that
// clearly belong to later phases (R4-9 over-mark fix).
func UnmarkPhaseSuccessCriteriaFromLaterPhases(projectRoot string, phaseNum int) int {
	phasePath := PhaseDocPath(projectRoot, phaseNum)
	data, err := os.ReadFile(phasePath)
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	inSuccess := false
	unmarked := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSuccess = strings.Contains(trimmed, "Success Criteria")
			continue
		}
		if !inSuccess {
			continue
		}
		if !strings.HasPrefix(trimmed, "- [x]") {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, "- [x]"))
		if criteriaOverlapsLaterPhaseGoals(projectRoot, desc, phaseNum) {
			lines[i] = strings.Replace(line, "- [x]", "- [ ]", 1)
			unmarked++
		}
	}
	if unmarked == 0 {
		return 0
	}
	_ = WriteFileAtomic(phasePath, []byte(strings.Join(lines, "\n")), 0644)
	return unmarked
}
