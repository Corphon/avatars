package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MarkScaffoldProgressFromDisk marks active-phase todo groups that already have
// clear disk evidence (V3: avoid permanent (0/N) after a green scaffold).
// Returns how many checklist lines were flipped to [x].
func MarkScaffoldProgressFromDisk(projectRoot string) int {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	data, err := os.ReadFile(todoPath)
	if err != nil {
		return 0
	}
	todo := string(data)
	signals := scaffoldDiskSignals(projectRoot)
	if len(signals) == 0 {
		return 0
	}

	lines := strings.Split(todo, "\n")
	inActive := false
	marked := 0
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## Phase ") && strings.Contains(t, "Checklist") {
			inActive = strings.Contains(t, "(active)")
			continue
		}
		if inActive && strings.HasPrefix(t, "## ") {
			break
		}
		if !inActive {
			continue
		}
		if !(strings.HasPrefix(t, "- [ ]") || strings.HasPrefix(t, "- [>]")) {
			continue
		}
		lower := strings.ToLower(t)
		if !scaffoldLineMatchesSignals(lower, signals) {
			continue
		}
		// Only upgrade when child count is still 0/N or missing — don't invent
		// full (N/N) without SyncMarks; at least clear the false-zero parent.
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		rest := strings.TrimPrefix(t, "- [ ]")
		rest = strings.TrimPrefix(rest, "- [>]")
		rest = strings.TrimSpace(rest)
		// Rewrite (0/N) → (N/N) when we have strong evidence for the whole group.
		rest = bumpZeroCountToFull(rest)
		lines[i] = indent + "- [x] " + rest
		marked++
	}
	if marked == 0 {
		return 0
	}
	out := strings.Join(lines, "\n")
	// If active checklist is now all done, mark Current Task.
	activeTasks := extractActiveChecklistLines(out)
	if checklistSectionAllDone(activeTasks) {
		metaPhase := 1
		if n := ParseTodoActivePhase(out); n > 0 {
			metaPhase = n
		}
		out = markCurrentTaskDone(out, metaPhase)
	}
	_ = WriteFileAtomic(todoPath, []byte(out), 0644)
	_ = SyncMarksToPhase(projectRoot)
	return marked
}

func bumpZeroCountToFull(label string) string {
	re := regexp.MustCompile(`\((\d+)/(\d+)\)$`)
	m := re.FindStringSubmatch(label)
	if len(m) != 3 {
		return label
	}
	if m[1] != "0" {
		return label
	}
	return re.ReplaceAllString(label, "("+m[2]+"/"+m[2]+")")
}

// normalizeCheckedZeroCounts fixes "[x] … (0/N)" leftovers anywhere in a todo
// or phase document (C7 / B3 leak paths that bypassed markCheckboxDone).
func normalizeCheckedZeroCounts(content string) string {
	lines := strings.Split(content, "\n")
	changed := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !(strings.HasPrefix(t, "- [x]") || strings.HasPrefix(t, "- [X]")) {
			continue
		}
		bumped := bumpZeroCountToFull(line)
		if bumped != line {
			lines[i] = bumped
			changed = true
		}
	}
	if !changed {
		return content
	}
	return strings.Join(lines, "\n")
}

func extractActiveChecklistLines(todo string) []string {
	var out []string
	inActive := false
	for _, line := range strings.Split(todo, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## Phase ") && strings.Contains(t, "Checklist") {
			inActive = strings.Contains(t, "(active)")
			continue
		}
		if inActive && strings.HasPrefix(t, "## ") {
			break
		}
		if inActive && (strings.HasPrefix(t, "- [") || strings.HasPrefix(t, "- [>]")) {
			out = append(out, t)
		}
	}
	return out
}

func scaffoldDiskSignals(projectRoot string) map[string]bool {
	s := map[string]bool{}
	check := func(rel string) bool {
		_, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(rel)))
		return err == nil
	}
	if check("requirements.txt") || check("pyproject.toml") || check("go.mod") {
		s["deps"] = true
		s["project"] = true
		s["setup"] = true
		s["skeleton"] = true
		s["scaffold"] = true
	}
	if check("app/core/config.py") || check("internal/config") {
		s["config"] = true
		s["configuration"] = true
	}
	if check("app/db/session.py") || check("app/db/base.py") || check("app/db/database.py") ||
		check("app/core/database.py") || check("internal/database") || check("internal/storage") {
		s["database"] = true
		s["session"] = true
		s["engine"] = true
		s["sqlalchemy"] = true
	}
	if check("app/api") || check("app/api/__init__.py") {
		s["api"] = true
		s["router"] = true
	}
	if check("app/models") || check("internal/model") {
		s["model"] = true
		s["models"] = true
	}
	if check("app/api/health.py") || check("app/api/v1/health.py") || check("app/main.py") {
		s["health"] = true
		s["endpoint"] = true
		s["fastapi"] = true
		s["entrypoint"] = true
		s["application"] = true
	}
	if check("migrations/001_init.sql") || check("migrations/env.py") ||
		dirHasFiles(filepath.Join(projectRoot, "migrations", "versions")) {
		s["migration"] = true
		s["migrations"] = true
		s["alembic"] = true
	}
	if check("tests/test_health.py") || dirHasFiles(filepath.Join(projectRoot, "tests")) {
		s["test"] = true
		s["verification"] = true
		s["pytest"] = true
	}
	return s
}

func dirHasFiles(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// W5: .gitkeep alone is not a real migration/test artifact.
		if name == ".gitkeep" || name == ".keep" {
			continue
		}
		return true
	}
	return false
}

func scaffoldLineMatchesSignals(lowerLine string, signals map[string]bool) bool {
	// Never scaffold-auto-mark feature work (--flags / ENV) — that needs Critic
	// content evidence (E1, cross-language).
	if len(checklistFeatureTokens(lowerLine)) > 0 {
		return false
	}
	// Only lines that look like scaffolding/setup may be disk-bumped. Otherwise
	// "tests" / "project" substrings falsely complete Phase-N feature rows.
	scaffoldHints := []string{
		"scaffold", "setup", "skeleton", "fixture", "testdata", "bootstrap",
		"go.mod", "cargo.toml", "package.json", "pyproject", "requirements.txt",
		"dependenc", "project layout", "scaffolding", "init module", "mod init",
	}
	isScaffoldLine := false
	for _, h := range scaffoldHints {
		if strings.Contains(lowerLine, h) {
			isScaffoldLine = true
			break
		}
	}
	if !isScaffoldLine {
		return false
	}
	for key, ok := range signals {
		if !ok {
			continue
		}
		if strings.Contains(lowerLine, key) {
			return true
		}
	}
	return false
}
