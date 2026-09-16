package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const userRequirementRel = "docs/workflow/user_requirement.md"

// PersistUserRequirement stores the original NL task so later phase expansion
// and ENV LOCK can align to user text (C1) — language-agnostic.
func PersistUserRequirement(projectRoot, requirement string) error {
	requirement = strings.ToValidUTF8(strings.TrimSpace(requirement), "")
	if requirement == "" {
		return nil
	}
	path := filepath.Join(projectRoot, filepath.FromSlash(userRequirementRel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return WriteFileAtomic(path, []byte(requirement+"\n"), 0644)
}

// LoadUserRequirement returns the persisted original NL task, or "".
func LoadUserRequirement(projectRoot string) string {
	path := filepath.Join(projectRoot, filepath.FromSlash(userRequirementRel))
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

var planIdentityStop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"that": true, "this": true, "into": true, "when": true, "then": true,
	"plan": true, "phase": true, "goal": true, "status": true, "count": true,
	"project": true, "active": true, "draft": true, "pending": true,
	"implement": true, "implementation": true, "library": true, "package": true,
	"crate": true, "module": true, "tests": true, "test": true, "unit": true,
	"docs": true, "readme": true, "cli": true, "tool": true, "code": true,
	"file": true, "files": true, "flag": true, "usage": true, "help": true,
	"build": true, "create": true, "write": true, "add": true, "open": true,
	"source": true, "style": true, "pure": true, "must": true, "need": true,
	"want": true, "please": true, "just": true, "only": true, "same": true,
	"core": true, "api": true, "run": true, "new": true, "main": true,
	"http": true, "json": true, "yaml": true, "true": true, "false": true,
	"golang": true, "python": true, "javascript": true, "typescript": true,
	"retry": true, "jitter": true, "cache": true, "circuit": true, "breaker": true,
}

var productNameHintRe = regexp.MustCompile(`(?i)(?:模块名|库名|(?:crate|package|module|library)\s+(?:name\s+)?|named)\s*[:：]?\s*[\"'\x60]?([A-Za-z][A-Za-z0-9_-]{2,})`)

var camelIdentRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{2,}`)

// PlanMismatchesLiveTask reports that on-disk plan / user_requirement belong
// to a different product than the live NL task (F98). Cross-language: a
// leftover `--help` CLI plan must not drive a Python/JS/Rust library brief.
// Short continue/repair prompts ("接着修", "keep going") do not trigger.
func PlanMismatchesLiveTask(projectRoot, liveTask string) bool {
	liveTask = strings.TrimSpace(liveTask)
	if liveTask == "" || IsPlanEmpty(projectRoot) {
		return false
	}
	if looksLikeContinuationPrompt(liveTask) {
		return false
	}
	planPath := filepath.Join(projectRoot, filepath.FromSlash("docs/workflow/avatars_plan.md"))
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		return false
	}
	planHaystack := strings.ToLower(string(planBytes) + "\n" + LoadUserRequirement(projectRoot))
	anchors := productAnchors(liveTask)
	if len(anchors) == 0 {
		return false
	}
	hits := 0
	for _, a := range anchors {
		if strings.Contains(planHaystack, a) {
			hits++
		}
	}
	// Mismatch only when none of the named products appear in the old plan.
	return hits == 0
}

func productAnchors(task string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		tok = strings.Trim(tok, "\"'`.,;:()[]{}")
		if len(tok) < 4 || planIdentityStop[tok] || seen[tok] || isMostlyDigits(tok) {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	for _, m := range productNameHintRe.FindAllStringSubmatch(task, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range camelIdentRe.FindAllString(task, -1) {
		letters := 0
		for _, r := range m {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		if letters < 4 {
			continue
		}
		// Prefer coined identifiers (retrybudget, HashRingX, mylib) over English.
		if hasInternalCaseChange(m) || strings.Contains(m, "_") || looksCoinedIdent(m) {
			add(m)
		}
	}
	return out
}

func hasInternalCaseChange(s string) bool {
	sawLower, sawUpper := false, false
	for _, r := range s {
		if unicode.IsLower(r) {
			sawLower = true
		}
		if unicode.IsUpper(r) {
			if sawLower {
				return true
			}
			sawUpper = true
		}
		_ = sawUpper
	}
	return false
}

func looksCoinedIdent(s string) bool {
	lower := strings.ToLower(s)
	if planIdentityStop[lower] || commonEnglishWord[lower] {
		return false
	}
	if strings.ContainsAny(s, "_-") {
		return true
	}
	n := len([]rune(lower))
	if n < 6 || n > 32 {
		return false
	}
	// Compact invented names (retrybudget, hashringx), not generic English.
	return strings.ContainsAny(lower, "xyzqkj") && n >= 8
}

var commonEnglishWord = map[string]bool{
	"create": true, "library": true, "package": true, "python": true,
	"javascript": true, "typescript": true, "golang": true, "retry": true,
	"budget": true, "concurrent": true, "parallel": true, "circuit": true,
	"breaker": true, "config": true, "configuration": true, "maximum": true,
	"elapsed": true, "attempts": true, "invalid": true, "success": true,
	"readme": true, "testing": true, "process": true, "inside": true,
	"analyze": true, "repository": true, "explain": true, "report": true,
	"concrete": true, "issues": true, "modify": true, "summarize": true,
	"coverage": true, "pytest": true,
}

func looksLikeContinuationPrompt(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	if len([]rune(lower)) > 80 {
		return false
	}
	needles := []string{
		"接着", "继续", "keep going", "continue", "resume",
		"修到", "再改", "fix the failing", "until tests",
		"confirm the plan", "确认计划",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func isMostlyDigits(s string) bool {
	digits := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits++
		}
	}
	return digits*2 >= len([]rune(s))
}

var httpRouteTokenRe = regexp.MustCompile(`(?i)\b(GET|POST|PUT|PATCH|DELETE)\s+(/[a-z0-9_{}\/-]+)`)

// phaseDocCoversUserRoutes checks that distinctive HTTP path segments from the
// user requirement for this phase appear in the phase document (C1/C11).
// Soft: allow at most ~30% miss when many routes; zero miss when ≤3 routes.
func phaseDocCoversUserRoutes(phaseContent, userReq string, phaseNum int) bool {
	scoped := StripOtherPhaseSections(userReq, phaseNum)
	if strings.TrimSpace(scoped) == "" {
		scoped = userReq
	}
	matches := httpRouteTokenRe.FindAllStringSubmatch(scoped, -1)
	if len(matches) == 0 {
		return true
	}
	lower := strings.ToLower(phaseContent)
	missing := 0
	for _, m := range matches {
		path := strings.ToLower(m[2])
		if routeMentionedInDoc(lower, path) {
			continue
		}
		missing++
	}
	n := len(matches)
	if n <= 3 {
		return missing == 0
	}
	return missing*10 <= n*3
}

// routeMentionedInDoc requires nested paths (2+ real segments) to keep parent+child
// together — so flat POST /slots does not satisfy POST /rooms/{id}/slots.
func routeMentionedInDoc(docLower, path string) bool {
	if strings.Contains(docLower, path) {
		return true
	}
	alt := strings.ReplaceAll(path, "{id}", ":id")
	if alt != path && strings.Contains(docLower, alt) {
		return true
	}
	var real []string
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s == "" || strings.HasPrefix(s, "{") || s == "id" {
			continue
		}
		real = append(real, s)
	}
	if len(real) == 0 {
		return false
	}
	if len(real) == 1 {
		return strings.Contains(docLower, "/"+real[0])
	}
	parent, child := real[0], real[len(real)-1]
	for _, line := range strings.Split(docLower, "\n") {
		if strings.Contains(line, parent) && strings.Contains(line, child) &&
			(strings.Contains(line, "/"+parent) || strings.Contains(line, parent+"/")) {
			return true
		}
	}
	return false
}

func distinctivePathSegment(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if s == "" || s == "{id}" || s == "id" {
			continue
		}
		return "/" + s
	}
	return path
}
