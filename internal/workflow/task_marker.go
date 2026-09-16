package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"avatars/internal/platform"
)

// P2-4c: Configurable window for "recently modified" file detection.
var recentFileWindow = 10 * time.Minute

// MarkTaskAsDone finds a task in the current phase checklist of avatars_todo.md
// whose description contains taskMatch (case-insensitive substring), and marks
// it as [x]. Returns true if a matching task was found and marked.
func MarkTaskAsDone(projectRoot string, taskMatch string) (bool, error) {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return false, fmt.Errorf("read todo: %w", err)
	}

	todoStr := string(content)
	lowerMatch := strings.ToLower(strings.TrimSpace(taskMatch))
	if lowerMatch == "" {
		return false, nil
	}

	phase := parseTodoPhaseNumber(todoStr)
	section := extractPhaseChecklistSection(todoStr, phase)
	if section == "" {
		return false, nil
	}
	checklistStart := strings.Index(todoStr, section)
	if checklistStart < 0 {
		// Header may differ slightly; locate by active/plain header.
		for _, hdr := range []string{
			fmt.Sprintf("## Phase %s Checklist (active)", phase),
			fmt.Sprintf("## Phase %s Checklist", phase),
		} {
			if idx := strings.Index(todoStr, hdr); idx >= 0 {
				checklistStart = idx
				break
			}
		}
	}
	if checklistStart < 0 {
		return false, nil
	}
	afterChecklist := todoStr[checklistStart:]
	checklistEnd := strings.Index(afterChecklist[1:], "\n## ")
	var checklistSection string
	if checklistEnd > 0 {
		checklistSection = afterChecklist[:checklistEnd+1]
	} else {
		checklistSection = afterChecklist
	}

	// Find matching task.
	lines := strings.Split(checklistSection, "\n")
	matched := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		taskDesc := strings.TrimPrefix(trimmed, "- [ ]")
		taskDesc = strings.TrimSpace(taskDesc)

		// Match: the taskMatch appears anywhere in the task description.
		if strings.Contains(strings.ToLower(taskDesc), lowerMatch) {
			lines[i] = markCheckboxDone(line)
			matched = true
			break
		}
	}

	if !matched {
		return false, nil
	}

	// Rebuild the file.
	newChecklist := strings.Join(lines, "\n")
	newTodo := todoStr[:checklistStart] + newChecklist
	if checklistEnd > 0 {
		newTodo += afterChecklist[checklistEnd+1:]
	}

	if err := WriteFileAtomic(todoPath, []byte(newTodo), 0644); err != nil {
		return false, fmt.Errorf("write todo: %w", err)
	}

	return true, nil
}

// TryAutoMarkDone checks the todo checklist items against a completed task
// summary and file list, and marks any matching items as done.
// taskInput is the original user task description, used to extract
// action words and target file names for richer matching.
// Returns the number of items marked.
func TryAutoMarkDone(projectRoot string, taskSummary string, changedFiles []string, taskInput ...string) int {
	// W15: Guard against false positives — only auto-mark when files were actually changed.
	// Without this, keyword matching marks items even when Builder produced no output.
	if len(changedFiles) == 0 {
		return 0
	}
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return 0
	}

	todoStr := string(content)
	marked := 0

	// Build match candidates from actually changed files.
	candidates := []string{taskSummary}
	for _, f := range changedFiles {
		// P2-4b: Include directory-prefixed path to distinguish
		// same-named files in different directories.
		candidates = append(candidates, filepath.Base(f))
		if dir := filepath.Dir(f); dir != "." {
			candidates = append(candidates, filepath.ToSlash(filepath.Join(filepath.Base(dir), filepath.Base(f))))
		}
		parts := strings.Split(strings.TrimSuffix(f, ".go"), "/")
		if len(parts) > 0 {
			candidates = append(candidates, parts[len(parts)-1])
		}
		// W16: Read generated code and extract function/class names as match candidates.
		// This catches cases where the plan uses different names than the actual code
		// (e.g., plan says "tokenize()" but code has "word_frequency()").
		if codeContent, err := os.ReadFile(f); err == nil {
			candidates = append(candidates, extractCodeSymbols(string(codeContent))...)
		}
	}

	// W11a: Extract keywords from task input.
	// - File names mentioned in task (e.g., "app.go", "config.go", "Settings.jsx")
	// - Action words (add, import, wire, register, integrate, create, update, modify)
	// - Package/directory names (llm, vision, video, services, providers)
	if len(taskInput) > 0 && taskInput[0] != "" {
		ti := taskInput[0]
		// File extensions mentioned in task.
		for _, ext := range []string{".go", ".jsx", ".tsx", ".ts", ".js", ".yaml", ".md"} {
			re := strings.Split(ti, ext)
			for j := 0; j < len(re)-1; j++ {
				// Get the word before the extension.
				before := re[j]
				words := strings.Fields(before)
				if len(words) > 0 {
					lastWord := strings.TrimFunc(words[len(words)-1], func(r rune) bool {
						return r == '"' || r == '\'' || r == '`' || r == ',' || r == '.'
					})
					if len(lastWord) > 1 {
						candidates = append(candidates, lastWord+ext)
						candidates = append(candidates, lastWord)
					}
				}
			}
		}
		// Action words map to checklist descriptions.
		actionWords := extractActionWords(ti)
		candidates = append(candidates, actionWords...)
		// Package/directory keywords from task input.
		for _, kw := range []string{"llm", "vision", "video", "provider", "services", "config", "frontend", "app", "settings"} {
			if strings.Contains(strings.ToLower(ti), kw) {
				candidates = append(candidates, kw)
			}
		}
	}

	// Find the Active Phase Checklist section (R3: never mark Phase 1 while Active=2).
	phase := parseTodoPhaseNumber(todoStr)
	checklistSection := extractPhaseChecklistSection(todoStr, phase)
	if checklistSection == "" {
		return 0
	}
	checklistStart := strings.Index(todoStr, checklistSection)
	if checklistStart < 0 {
		return 0
	}
	afterChecklist := todoStr[checklistStart:]
	checklistEnd := strings.Index(afterChecklist[1:], "\n## ")

	lines := strings.Split(checklistSection, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		taskDesc := strings.TrimPrefix(trimmed, "- [ ]")
		taskDesc = strings.TrimSpace(taskDesc)
		lowerDesc := strings.ToLower(taskDesc)

		for _, candidate := range candidates {
			lowerCandidate := strings.ToLower(candidate)
			if len(lowerCandidate) < 4 {
				continue
			}
			if strings.Contains(lowerDesc, lowerCandidate) {
				lines[i] = strings.Replace(trimmed, "- [ ]", "- [x]", 1)
				marked++
				break
			}
		}
	}

	if marked == 0 {
		return 0
	}

	newChecklist := strings.Join(lines, "\n")
	newTodo := todoStr[:checklistStart] + newChecklist
	if checklistEnd > 0 {
		newTodo += afterChecklist[checklistEnd+1:]
	}

	if err := WriteFileAtomic(todoPath, []byte(newTodo), 0644); err != nil {
		return marked // best-effort: return count even if write fails
	}
	return marked
}

// IsMarkTaskDoneCheck is the boolean-only version for complexity bypass.
func IsMarkTaskDoneCheck(taskInput string) bool {
	isMark, _ := IsMarkTaskDone(taskInput)
	return isMark
}

// IsMarkTaskDone detects "mark X as done/completed/complete" in the input.
// Returns true and the extracted task description X.
func IsMarkTaskDone(taskInput string) (bool, string) {
	lower := strings.ToLower(taskInput)

	// Pattern: "mark ... as done/complete/completed"
	markers := []string{"mark ", "标记 ", "完成 "}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			afterMarker := lower[strings.Index(lower, marker)+len(marker):]
			// Find the end delimiter.
			endMarkers := []string{" as done", " as complete", " as completed", " 为完成", " done", " completed"}
			for _, endMarker := range endMarkers {
				if idx := strings.Index(afterMarker, endMarker); idx > 0 {
					taskDesc := strings.TrimSpace(taskInput[strings.Index(lower, marker)+len(marker):][:idx])
					// Strip surrounding quotes.
					taskDesc = strings.Trim(taskDesc, "'\"")
					taskDesc = strings.TrimSpace(taskDesc)
					if len(taskDesc) >= 3 {
						return true, taskDesc
					}
				}
			}
		}
	}

	// Simpler pattern: "complete task: X" or "done: X"
	simpleMarkers := []string{"complete task:", "done:", "完成:", "complete task ", "done "}
	for _, marker := range simpleMarkers {
		if idx := strings.Index(lower, marker); idx >= 0 {
			rest := strings.TrimSpace(taskInput[idx+len(marker):])
			rest = strings.Trim(rest, "'\"")
			rest = strings.TrimSpace(rest)
			if len(rest) >= 3 {
				return true, rest
			}
		}
	}

	return false, ""
}

// extractActionWords extracts action-oriented keywords from task input.
// These help match checklist items that describe WHAT was done (not just file names).
// Example: "add import to app.go" → ["add import", "import", "add"]
// Example: "wire sapiens_ai provider" → ["wire", "provider", "sapiens_ai"]
func extractActionWords(taskInput string) []string {
	lower := strings.ToLower(taskInput)
	var words []string

	// Verb-object pairs.
	pairs := []string{
		"add import", "add case", "add option", "add provider",
		"wire provider", "wire up", "register provider",
		"create provider", "create file", "generate code",
		"update config", "modify file", "edit file",
		"integrate provider", "connect provider",
		"fix bug", "fix compilation", "resolve error",
	}
	for _, p := range pairs {
		if strings.Contains(lower, p) {
			words = append(words, p)
		}
	}

	// Single action verbs.
	verbs := []string{"import", "wire", "register", "create", "generate", "update", "modify", "edit", "add", "integrate", "connect", "fix"}
	for _, v := range verbs {
		if strings.Contains(lower, v) {
			words = append(words, v)
		}
	}

	// Provider/model names — extract any capitalized word near "provider" or "model".
	if strings.Contains(lower, "provider") || strings.Contains(lower, "model") {
		// Extract any CamelCase or snake_case identifiers as potential provider names.
		nameRe := regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_-]{2,}`)
		for _, m := range nameRe.FindAllString(taskInput, -1) {
			ml := strings.ToLower(m)
			// Filter out common non-provider words.
			if ml != "the" && ml != "and" && ml != "for" && ml != "with" && ml != "from" &&
				ml != "that" && ml != "this" && ml != "add" && ml != "import" &&
				ml != "case" && ml != "file" && ml != "task" && ml != "config" &&
				ml != "test" && ml != "main" && ml != "internal" && len(ml) > 2 {
				words = append(words, ml)
			}
		}
	}
	// Generic action keywords always relevant.
	for _, kw := range []string{"provider", "interface", "struct", "function", "package", "import"} {
		if strings.Contains(lower, kw) {
			words = append(words, kw)
		}
	}

	return words
}

// SyncMarksToPhase reads the avatars_todo.md checklist and mirrors [x] marks
// back into the corresponding phaseN.md file. This keeps the phase source in sync
// with the working todo. Returns the number of phase items marked.
func SyncMarksToPhase(projectRoot string) int {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	todoContent, err := os.ReadFile(todoPath)
	if err != nil {
		return 0
	}
	todoStr := string(todoContent)

	// Find the current phase number from the todo header.
	phaseNum := 1
	if idx := strings.Index(todoStr, "**Phase**:"); idx >= 0 {
		rest := todoStr[idx:]
		if nl := strings.Index(rest, "\n"); nl > 0 {
			header := rest[:nl]
			// Extract number: "**Phase**: 1 of 1" or "**Phase**: 1"
			for _, part := range strings.Fields(header) {
				if n, ok := parseInt(part); ok && n > 0 {
					phaseNum = n
					break
				}
			}
		}
	}

	// Read the phase file.
	phasePath := filepath.Join(projectRoot, "docs", "workflow", fmt.Sprintf("phase%d.md", phaseNum))
	phaseContent, err := os.ReadFile(phasePath)
	if err != nil {
		return 0
	}
	phaseStr := string(phaseContent)
	// F51: waive unverifiable race items in phase detail the same way as todo.
	if waived := evidenceBasedVerificationMark(phaseStr, projectRoot); waived != phaseStr {
		phaseStr = waived
		_ = os.WriteFile(phasePath, []byte(phaseStr), 0644)
	}

	// Collect task descriptions from marked [x] items in the todo.
	markedDescs := collectMarkedTodoDescs(todoStr)
	if len(markedDescs) == 0 {
		return 0
	}

	// Mark matching items in the phase file — Tasks section only (R4-9:
	// do not pollute Scope & Success Criteria via loose todo→phase sync).
	marked := 0
	lines := strings.Split(phaseStr, "\n")
	inTasks := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inTasks = strings.HasPrefix(trimmed, "## Tasks")
			continue
		}
		if !inTasks {
			continue
		}
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		desc := strings.TrimPrefix(trimmed, "- [ ]")
		desc = strings.TrimSpace(desc)
		lowerDesc := strings.ToLower(desc)

		for _, md := range markedDescs {
			mdLower := strings.ToLower(md)
			// Require substantial overlap — avoid "content" marking unrelated lines.
			if len(mdLower) < 8 && !strings.Contains(mdLower, ".") {
				continue
			}
			if strings.Contains(lowerDesc, mdLower) || (len(lowerDesc) >= 12 && strings.Contains(mdLower, lowerDesc)) {
				lines[i] = markCheckboxDone(line)
				marked++
				break
			}
		}
		// Path evidence fallback for Tasks.
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- [ ]") && checklistItemHasPathEvidence(projectRoot, desc) {
			lines[i] = markCheckboxDone(line)
			marked++
		}
	}

	if marked == 0 {
		return SyncPhaseCriteriaMarksToPlan(projectRoot)
	}

	// W4: Idempotency check — if the phase file already has all matching
	// items as [x], skip the write to avoid unnecessary file IO and races.
	newPhase := strings.Join(lines, "\n")
	if newPhase == phaseStr {
		return SyncPhaseCriteriaMarksToPlan(projectRoot)
	}
	_ = WriteFileAtomic(phasePath, []byte(newPhase), 0644)
	marked += SyncPhaseCriteriaMarksToPlan(projectRoot)
	return marked
}

// collectMarkedTodoDescs extracts descriptions of [x]-marked items from todo content.
func collectMarkedTodoDescs(todoStr string) []string {
	var descs []string
	checklistStart := strings.Index(todoStr, "## Phase ")
	if checklistStart < 0 {
		return descs
	}
	after := todoStr[checklistStart:]
	end := strings.Index(after[1:], "\n## ")
	var section string
	if end > 0 {
		section = after[:end+1]
	} else {
		section = after
	}
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [x]") {
			desc := strings.TrimPrefix(trimmed, "- [x]")
			desc = strings.TrimSpace(desc)
			if len(desc) > 3 {
				// Take first ~60 chars as match key.
				if len(desc) > 60 {
					desc = desc[:60]
				}
				descs = append(descs, desc)
			}
		}
	}
	return descs
}

// incompleteCheckboxPrefix reports whether trimmed is an incomplete checklist
// item. Both "- [ ]" and "- [>]" (resume bookmark) count as incomplete (R5-2/4).
func incompleteCheckboxPrefix(trimmed string) (prefix string, ok bool) {
	if strings.HasPrefix(trimmed, "- [ ]") {
		return "- [ ]", true
	}
	if strings.HasPrefix(trimmed, "- [>]") {
		return "- [>]", true
	}
	return "", false
}

// markCheckboxDone flips an incomplete checkbox to [x] (handles [>] bookmarks).
// B3/B12: also normalize trailing (0/N) → (N/N) so [x] never sits beside (0/N).
func markCheckboxDone(line string) string {
	out := line
	if strings.Contains(out, "- [>]") {
		out = strings.Replace(out, "- [>]", "- [x]", 1)
	} else {
		out = strings.Replace(out, "- [ ]", "- [x]", 1)
	}
	return bumpZeroCountToFull(out)
}

// unmarkCheckbox flips a falsely completed [x] back to [ ] (F117).
func unmarkCheckbox(line string) string {
	out := line
	if strings.Contains(out, "- [x]") {
		out = strings.Replace(out, "- [x]", "- [ ]", 1)
	} else if strings.Contains(out, "- [X]") {
		out = strings.Replace(out, "- [X]", "- [ ]", 1)
	}
	return out
}

// markCheckboxDoneHonest marks a checklist line done and rewrites trailing
// (done/total) using on-disk evidence so planner fiction (e.g. 25/25 with 4
// tests) cannot survive (F73, cross-language).
func markCheckboxDoneHonest(line, projectRoot string) string {
	out := markCheckboxDone(line)
	trimmed := strings.TrimSpace(out)
	desc := trimmed
	for _, p := range []string{"- [x] ", "- [X] "} {
		if strings.HasPrefix(desc, p) {
			desc = strings.TrimSpace(strings.TrimPrefix(desc, p))
			break
		}
	}
	lower := strings.ToLower(desc)
	if checklistItemRequiresLiveTestEvidence(lower) {
		n := countAllWorkflowTestSymbols(projectRoot)
		if n > 0 {
			return replaceTrailingChecklistCount(out, n, n)
		}
	}
	if checklistItemLooksLikeCoreImpl(lower) {
		files := countSubstantiveSourceFiles(projectRoot)
		if files > 0 {
			return replaceTrailingChecklistCount(out, files, files)
		}
	}
	if checklistItemLooksLikeDocumentation(lower) {
		n := countDocumentationArtifacts(projectRoot)
		if n > 0 {
			return replaceTrailingChecklistCount(out, n, n)
		}
	}
	return out
}

var trailingChecklistCountRe = regexp.MustCompile(`\((\d+)/(\d+)\)\s*$`)

func replaceTrailingChecklistCount(line string, done, total int) string {
	if total < 0 {
		total = 0
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	repl := fmt.Sprintf("(%d/%d)", done, total)
	if trailingChecklistCountRe.MatchString(strings.TrimSpace(line)) {
		// Preserve indentation / checkbox prefix; replace only trailing count.
		loc := trailingChecklistCountRe.FindStringIndex(line)
		if loc != nil {
			return line[:loc[0]] + repl + line[loc[1]:]
		}
	}
	return strings.TrimRight(line, " \t") + " " + repl
}

// CriticAuditAndMark reads the phase checklist and generated code files,
// verifies which items are actually implemented, and marks them as [x].
// W16: This is the Critic's "director audit" — precise marking based on code review.
func CriticAuditAndMark(projectRoot string) int {
	// G3: do not auto-mark checklist while compile/health is red.
	if !projectCompiles(projectRoot) {
		return 0
	}
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	todoContent, err := os.ReadFile(todoPath)
	if err != nil {
		return 0
	}
	todoStr := string(todoContent)

	// R3: audit Active Phase checklist only (not the first ## Phase block).
	phase := parseTodoPhaseNumber(todoStr)
	section := extractPhaseChecklistSection(todoStr, phase)
	if section == "" {
		return 0
	}

	// First, auto-mark bootstrap items, then evidence-based build/test marks (S6.7).
	todoStr = autoMarkBootstrapItems(todoStr, projectRoot)
	todoStr = evidenceBasedVerificationMark(todoStr, projectRoot)
	if err := WriteFileAtomic(todoPath, []byte(todoStr), 0644); err != nil {
		return 0
	}
	// Re-read section after evidence marks so pending list is current.
	todoContent, err = os.ReadFile(todoPath)
	if err != nil {
		return 0
	}
	todoStr = string(todoContent)
	phase = parseTodoPhaseNumber(todoStr)
	section = extractPhaseChecklistSection(todoStr, phase)
	if section == "" {
		return 0
	}
	checklistStart := strings.Index(todoStr, section)
	if checklistStart < 0 {
		return 0
	}
	after := todoStr[checklistStart:]
	end := strings.Index(after[1:], "\n## ")
	if end > 0 {
		section = after[:end+1]
	} else {
		section = after
	}

	// Collect incomplete [ ] / [>] items and their line indices.
	type pendingItem struct {
		lineIdx  int
		desc     string
		origLine string
		prefix   string
	}
	var items []pendingItem
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		prefix, ok := incompleteCheckboxPrefix(trimmed)
		if !ok {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		items = append(items, pendingItem{i, desc, line, prefix})
	}
	marked := 0
	if len(items) > 0 {
		codeText := collectGeneratedCodeText(projectRoot)
		activePhase := 1
		if planContent, pErr := ReadWorkflowDoc(projectRoot, "plan"); pErr == nil {
			activePhase = ParsePlanMeta(planContent).ActivePhase
			if activePhase < 1 {
				activePhase = 1
			}
		}
		for _, item := range items {
			if criteriaOverlapsLaterPhaseGoals(projectRoot, item.desc, activePhase) {
				continue
			}
			if isChecklistItemSatisfied(projectRoot, item.desc, codeText) {
				lines[item.lineIdx] = markCheckboxDoneHonest(item.origLine, projectRoot)
				marked++
			}
		}
		if marked > 0 {
			newSection := strings.Join(lines, "\n")
			newTodo := todoStr[:checklistStart] + newSection
			if end > 0 {
				newTodo += after[end+1:]
			}
			if err := WriteFileAtomic(todoPath, []byte(newTodo), 0644); err != nil {
				return 0
			}
		}
	}

	// R4-9: mark phase Tasks from disk paths; unmark Success Criteria that belong to later phases.
	if planContent, pErr := ReadWorkflowDoc(projectRoot, "plan"); pErr == nil {
		ap := ParsePlanMeta(planContent).ActivePhase
		if ap < 1 {
			ap = 1
		}
		marked += MarkPhaseChecklistByPathEvidence(projectRoot, ap)
		_ = UnmarkPhaseSuccessCriteriaFromLaterPhases(projectRoot, ap)
		_ = UnmarkSuccessCriteriaMissingAuthDeleteEvidence(projectRoot)
	}
	SyncMarksToPhase(projectRoot)
	// F73: rewrite inflated (N/N) against real test/source counts; mark
	// remaining impl/verification rows with disk evidence.
	marked += reconcileChecklistCountHonesty(projectRoot)
	// S6.7: Keep [>] resume bookmark honest after Critic marks.
	_ = UpdateTodoProgressBookmark(projectRoot)
	return marked
}

// collectGeneratedCodeText reads all recently generated code files and returns
// their combined text content for pattern matching.
// CollectGeneratedCodeText reads all recently generated source files and returns
// combined text content for requirement checking.
func CollectGeneratedCodeText(projectRoot string) string {
	return collectGeneratedCodeText(projectRoot)
}

func collectGeneratedCodeText(projectRoot string) string {
	var allText strings.Builder
	// First pass: collect files modified in the last 10 minutes.
	// P2-4c: Configurable window for "recent" file detection.
	cutoff := time.Now().Add(-recentFileWindow)
	hasRecent := false
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(path, ".avatars") || strings.Contains(path, ".git") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		ext := filepath.Ext(path)
		switch ext {
		case ".go", ".py", ".rs", ".js", ".ts", ".jsx", ".tsx", ".toml", ".yaml", ".yml":
			content, err := os.ReadFile(path)
			if err == nil {
				allText.WriteString(strings.ToLower(string(content)))
				allText.WriteString("\n")
				if !info.ModTime().Before(cutoff) {
					hasRecent = true
				}
			}
		}
		return nil
	})

	// Second pass: if no recently-modified code files, collect ALL project code
	// files. This handles the case where code was generated in a previous run
	// and the current Critic audit needs to verify against existing code.
	if !hasRecent {
		allText.Reset()
		_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.Contains(path, ".avatars") || strings.Contains(path, ".git") {
				return nil
			}
			ext := filepath.Ext(path)
			switch ext {
			case ".go", ".py", ".rs", ".js", ".ts", ".jsx", ".tsx", ".toml", ".yaml", ".yml":
				content, err := os.ReadFile(path)
				if err == nil {
					allText.WriteString(strings.ToLower(string(content)))
					allText.WriteString("\n")
				}
			}
			return nil
		})
	}
	return allText.String()
}

// isChecklistItemSatisfied checks if a checklist item description is satisfied
// by the generated code. Uses path evidence (language-agnostic), then pattern
// matching on function names, imports, and structural patterns.
func isChecklistItemSatisfied(projectRoot, itemDesc string, codeText string) bool {
	lower := strings.ToLower(itemDesc)

	// Feature tokens (--flags, ENV_VARS) require code-content evidence across
	// Go/Python/JS/Rust/… — path existence alone is not enough (E1).
	featureTokens := checklistFeatureTokens(itemDesc)
	if len(featureTokens) > 0 {
		codeLower := strings.ToLower(codeText)
		for _, tok := range featureTokens {
			if !codeContainsFeatureToken(codeLower, tok) {
				return false
			}
		}
		// All feature tokens present in code — satisfied.
		return true
	}

	// Live test-pass claims need a green suite, not compile + files on disk.
	if checklistItemRequiresLiveTestEvidence(lower) {
		if checklistItemClaimsTestsPass(lower) {
			return projectTestsPass(projectRoot)
		}
		return projectCompiles(projectRoot) && planTestsEvidenceOK(projectRoot) && substantiveTestSuite(projectRoot)
	}
	if checklistItemLooksLikeNamedTest(itemDesc) && projectTestsPass(projectRoot) && substantiveTestSuite(projectRoot) {
		return true
	}
	if checklistItemLooksLikeStaticCheck(lower) && (projectTestsPass(projectRoot) || projectCompiles(projectRoot)) {
		return true
	}
	if checklistItemLooksLikeAbsenceOrImportGate(lower) && (projectTestsPass(projectRoot) || projectCompiles(projectRoot)) {
		return true
	}

	// Generic "core library / implementation" rows — satisfied when
	// substantive non-test sources exist (Go/Python/JS/Rust/…).
	if checklistItemLooksLikeCoreImpl(lower) && countSubstantiveSourceFiles(projectRoot) > 0 {
		return true
	}
	// Verification pass after a green test suite (any language runner).
	if checklistItemLooksLikeVerification(lower) {
		return projectTestsPass(projectRoot)
	}
	// F73 residual: Documentation/README/header-comment rows — satisfied when
	// a README or source file-header docs exist (Go/Python/JS/Rust/…).
	if checklistItemLooksLikeDocumentation(lower) && countDocumentationArtifacts(projectRoot) > 0 {
		return true
	}

	// Phase 1: file/path evidence under projectRoot (not process CWD).
	if checklistItemHasPathEvidence(projectRoot, itemDesc) {
		return true
	}
	if satisfiedByFileExistence(projectRoot, lower) {
		return true
	}

	// Phase 2: Extract key terms from the checklist item and check code.
	terms := extractKeyTerms(lower)

	matched := 0
	required := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		required++
		if strings.Contains(codeText, term) {
			matched++
		}
	}

	// Require at least 60% of key terms to match, with minimum 2 terms.
	if required == 0 {
		return false
	}
	if required == 1 {
		return matched == 1
	}
	return float64(matched)/float64(required) >= 0.6
}

// satisfiedByFileExistence checks if a checklist item describes a file or command
// whose result can be verified by the file's existence under projectRoot.
func satisfiedByFileExistence(projectRoot, itemDesc string) bool {
	// "go mod init X" → go.mod exists
	if strings.Contains(itemDesc, "go mod init") {
		if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err == nil {
			return true
		}
	}
	if strings.Contains(itemDesc, "cargo init") || strings.Contains(itemDesc, "cargo new") {
		if _, err := os.Stat(filepath.Join(projectRoot, "Cargo.toml")); err == nil {
			return true
		}
	}
	if strings.Contains(itemDesc, "npm init") || strings.Contains(itemDesc, "package.json") {
		if _, err := os.Stat(filepath.Join(projectRoot, "package.json")); err == nil {
			return true
		}
	}
	// X3: generic handler/endpoint group labels — layout dir with sources is enough.
	lowerItem := strings.ToLower(itemDesc)
	if strings.Contains(lowerItem, "handler") || strings.Contains(lowerItem, "endpoint") ||
		strings.Contains(lowerItem, "route") || strings.Contains(lowerItem, "接口") {
		for _, token := range []string{"handlers", "internal/handlers", "app/handlers", "src/handlers",
			"routes", "api", "internal/api", "app/api"} {
			if checklistItemHasLayoutDirEvidence(projectRoot, token) {
				return true
			}
		}
	}
	return checklistItemHasPathEvidence(projectRoot, itemDesc)
}

// extractKeyTerms extracts meaningful search terms from a checklist item.
// Filters out common stop words and extracts function names, patterns, etc.
func extractKeyTerms(itemDesc string) []string {
	// Extract quoted strings (exact function names, file names).
	var terms []string
	re := regexp.MustCompile("`([^`]+)`|\"([^\"]+)\"|'([^']+)'")
	for _, m := range re.FindAllStringSubmatch(itemDesc, -1) {
		for g := 1; g < len(m); g++ {
			if m[g] != "" && len(m[g]) > 1 {
				terms = append(terms, strings.ToLower(m[g]))
			}
		}
	}
	// Extract function-like names: word followed by ()
	funcRe := regexp.MustCompile(`(\w+)\s*\(`)
	for _, m := range funcRe.FindAllStringSubmatch(itemDesc, -1) {
		if len(m) >= 2 && len(m[1]) > 2 {
			terms = append(terms, strings.ToLower(m[1]))
		}
	}
	// Extract import-like terms: "import X" or "use X"
	importRe := regexp.MustCompile(`(?:import|use|from)\s+(\w+)`)
	for _, m := range importRe.FindAllStringSubmatch(itemDesc, -1) {
		if len(m) >= 2 && len(m[1]) > 2 {
			terms = append(terms, strings.ToLower(m[1]))
		}
	}
	// Important action words that indicate implementation patterns.
	actions := map[string]string{
		"open": "open(", "read": "read(", "write": "write(",
		"catch": "except", "error": "error", "handle": "except",
		"sort": "sort", "print": "print(", "loop": "for ",
		"return": "return ", "parse": "parse", "argparse": "argparse",
		"sys.argv": "sys.argv", "filenotfound": "filenotfound",
		"exit": "exit(",
		// F73: do not map "implement/create" only to Python `def ` —
		// that falsely fails Go/Rust/JS core-impl checklist rows.
		"create": "func ", "implement": "func ",
	}
	for keyword, codePattern := range actions {
		if strings.Contains(itemDesc, keyword) {
			terms = append(terms, codePattern)
		}
	}
	return terms
}

// extractCodeSymbols extracts function names, class names, and key identifiers
// from source code for matching against checklist items. W16: This bridges the
// gap when plan uses different names than actual code (e.g., "tokenize" vs "word_frequency").
func extractCodeSymbols(code string) []string {
	var symbols []string
	// Go/Rust/Python function definitions: func name, fn name, def name
	funcRe := regexp.MustCompile(`(?:func|fn|def)\s+(\w+)`)
	for _, m := range funcRe.FindAllStringSubmatch(code, -1) {
		if len(m) >= 2 && len(m[1]) > 2 {
			symbols = append(symbols, m[1])
		}
	}
	// Class definitions
	classRe := regexp.MustCompile(`class\s+(\w+)`)
	for _, m := range classRe.FindAllStringSubmatch(code, -1) {
		if len(m) >= 2 && len(m[1]) > 2 {
			symbols = append(symbols, m[1])
		}
	}
	// Imports/uses: import "pkg", use crate, from module import name
	importRe := regexp.MustCompile(`(?:import|use|from)\s+[\(]?"?(\w+)`)
	for _, m := range importRe.FindAllStringSubmatch(code, -1) {
		if len(m) >= 2 && len(m[1]) > 2 {
			symbols = append(symbols, m[1])
		}
	}
	return symbols
}

// parseInt returns the integer value of a string and true if it's a valid positive int.
func parseInt(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0
}

// GetRemainingChecklistItems returns descriptions of all [ ] items in the
// current (header) phase checklist — not other phases' unfinished rows.
func GetRemainingChecklistItems(projectRoot string) []string {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return nil
	}
	section := extractPhaseChecklistSection(string(content), parseTodoPhaseNumber(string(content)))
	var items []string
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		prefix, ok := incompleteCheckboxPrefix(trimmed)
		if !ok {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if isTemplatePlaceholderLabel(desc) {
			continue
		}
		if looksLikeGluedChecklistLabel(trimmed) || looksLikeGluedChecklistLabel(desc) {
			continue
		}
		if len(desc) > 3 {
			items = append(items, desc)
		}
	}
	return items
}

// CountTodoProgressDetailed returns (completed, total, remaining) for the current phase checklist.
func CountTodoProgressDetailed(projectRoot string) (completed int, total int, remaining int) {
	completed, total = CountTodoProgress(projectRoot)
	return completed, total, total - completed
}

// CountTodoProgress returns (completed, total) counts for the current phase checklist.
func CountTodoProgress(projectRoot string) (completed int, total int) {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return 0, 0
	}
	return CountTodoProgressFromContent(string(content))
}

// CountTodoProgressFromContent counts [x]/[ ] in the active phase checklist only.
func CountTodoProgressFromContent(todoStr string) (completed int, total int) {
	section := extractPhaseChecklistSection(todoStr, parseTodoPhaseNumber(todoStr))
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip truncation placeholders — not real tasks (S2.3).
		if strings.Contains(trimmed, "more items — see full plan") {
			continue
		}
		if strings.HasPrefix(trimmed, "> (+") && strings.Contains(trimmed, "more items") {
			continue
		}
		if isTemplatePlaceholderLabel(trimmed) {
			continue
		}
		if looksLikeGluedChecklistLabel(trimmed) {
			continue
		}
		if strings.HasPrefix(trimmed, "- [x]") || strings.HasPrefix(trimmed, "- [X]") {
			completed++
			total++
		} else if strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [>]") {
			// W3: [>] is the resume bookmark — still an incomplete checklist item.
			total++
		}
	}
	return
}

// CheckImplementationCompleteness scans Go source files in projectRoot for
// function/method calls that reference symbols not defined anywhere. Returns
// a list of "missing X referenced in Y" messages. Used by Critic to detect
// Builder output that references unimplemented features (v4-P0).
//
// v4-P1-1: Filters out standard library symbols (fmt.*, os.*, sync.*, etc.)
// to eliminate false positives where stdlib functions were reported as "missing".
// Also filters builtins (len, make, append, etc.) and common third-party imports.
func CheckImplementationCompleteness(projectRoot string) []string {
	var missing []string
	// Collect all defined function/method names.
	defined := map[string]bool{}
	// Collect all imported package aliases so we can skip qualified calls.
	importedAliases := map[string]bool{}
	filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, ".avatars") || strings.Contains(path, "vendor") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			// func MethodName(
			if strings.HasPrefix(trimmed, "func ") {
				if idx := strings.Index(trimmed[5:], "("); idx > 0 {
					name := strings.TrimSpace(trimmed[5 : 5+idx])
					// Strip receiver type: func (r *Type) Method → Method
					if strings.HasPrefix(name, "(") {
						if rp := strings.Index(name, ") "); rp >= 0 {
							name = strings.TrimSpace(name[rp+2:])
						}
					}
					if name != "" {
						defined[name] = true
					}
				}
			}
			// Collect import aliases: import "fmt" → fmt; import f "fmt" → f
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "\"") {
				// Single import: import "fmt" or import alias "pkg"
				parseImportLine(trimmed, importedAliases)
			}
		}
		return nil
	})

	// v4-P1-1: Standard library package prefixes to skip.
	// These are the most common Go stdlib packages whose symbols would
	// be false positives if not filtered.
	stdlibPackages := map[string]bool{
		"fmt": true, "os": true, "io": true, "strings": true, "strconv": true,
		"sync": true, "time": true, "context": true, "errors": true,
		"path": true, "path/filepath": true, "sort": true, "math": true,
		"math/rand": true, "encoding/json": true, "encoding/xml": true,
		"encoding/base64": true, "net/http": true, "net/url": true,
		"regexp": true, "log": true, "bufio": true, "bytes": true,
		"reflect": true, "unicode": true, "unicode/utf8": true,
		"debug/pprof": true, "runtime": true, "syscall": true,
		"hash": true, "hash/fnv": true, "hash/crc32": true, "hash/crc64": true,
		"crypto": true, "crypto/sha256": true, "crypto/md5": true,
		"crypto/hmac": true, "crypto/rand": true,
		"database/sql": true, "html/template": true, "text/template": true,
		"flag": true, "testing": true, "unsafe": true,
	}
	// v4-P1-1: Go builtins and common predeclared identifiers.
	builtins := map[string]bool{
		"len": true, "cap": true, "make": true, "new": true, "append": true,
		"copy": true, "delete": true, "close": true, "panic": true,
		"recover": true, "print": true, "println": true,
		"complex": true, "real": true, "imag": true,
		"error": true, "bool": true, "string": true,
		"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
		"uintptr": true, "float32": true, "float64": true,
		"byte": true, "rune": true, "true": true, "false": true, "nil": true,
		"iota": true, "any": true, "comparable": true,
	}
	// Batch4/H: method names that are overwhelmingly stdlib / interface methods
	// when called as receiver.Method(...). Without this, f.Close() / t.Fatal()
	// / strings.Join gaps flag as impl_incomplete and trap Critic→Builder.
	commonStdlibMethods := map[string]bool{
		"Close": true, "Join": true, "Lock": true, "Unlock": true,
		"RLock": true, "RUnlock": true, "Fatal": true, "Fatalf": true,
		"Error": true, "Errorf": true, "Log": true, "Logf": true,
		"Skip": true, "Skipf": true, "SkipNow": true, "Helper": true,
		"Cleanup": true, "Parallel": true, "Run": true, "TempDir": true,
		"Read": true, "Write": true, "WriteString": true, "WriteByte": true,
		"Seek": true, "Sync": true, "Flush": true, "Reset": true,
		"String": true, "Bytes": true, "Len": true, "Cap": true, "Grow": true,
		"ReplaceAll": true, "Contains": true, "HasPrefix": true, "HasSuffix": true,
		"TrimSpace": true, "Split": true, "Trim": true, "TrimPrefix": true,
		"Marshal": true, "Unmarshal": true, "Encode": true, "Decode": true,
		"Do": true, "Add": true, "Load": true, "Store": true, "Swap": true,
		"CompareAndSwap": true, "Done": true, "Err": true, "Value": true,
		"Deadline": true, "WithCancel": true, "WithTimeout": true, "WithDeadline": true,
		"Background": true, "TODO": true, "After": true, "Sleep": true,
		"Now": true, "Since": true, "Until": true, "Parse": true, "Format": true,
		"IsNil": true, "IsValid": true, "Kind": true, "NumField": true,
		"Getenv": true, "Setenv": true, "Exit": true, "Args": true,
		"Open": true, "Create": true, "Remove": true, "MkdirAll": true,
		"Stat": true, "ReadFile": true, "WriteFile": true, "ReadDir": true,
		"Abs": true, "Rel": true, "Base": true, "Dir": true, "Ext": true,
		"Clean": true, "FromSlash": true, "ToSlash": true, "Walk": true,
		"NewReader": true, "NewWriter": true, "NewDecoder": true, "NewEncoder": true,
		"Printf": true, "Sprintf": true, "Fprintf": true, "Println": true,
		"Unwrap": true, "Is": true, "As": true,
		// H8: net/http + common stdlib method names false-flagged as missing.
		"ListenAndServe": true, "ListenAndServeTLS": true, "Shutdown": true,
		"HandleFunc": true, "Handle": true, "ServeHTTP": true, "Serve": true,
		"Header": true, "WriteHeader": true, "Set": true, "Get": true, "Del": true,
		"NewRequest": true, "NewServeMux": true, "ParseForm": true, "ParseMultipartForm": true,
		"Redirect": true, "NotFound": true, "StatusText": true,
		"Query": true, "PathValue": true, "FormValue": true, "PostFormValue": true,
		"MarshalJSON": true, "UnmarshalJSON": true,
		"Begin": true, "Commit": true, "Rollback": true, "QueryRow": true, "Exec": true,
		"Prepare": true, "Ping": true, "OpenDB": true,
	}

	// Check for calls to undefined symbols.
	filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, ".avatars") || strings.Contains(path, "vendor") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(content), "\n")
		for lineNum, line := range lines {
			// Skip comments and import lines.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "package ") {
				continue
			}
			// Look for method calls: .MethodName( or PackageName.FunctionName(
			for _, part := range strings.Split(line, ".") {
				if idx := strings.Index(part, "("); idx > 0 {
					candidate := strings.TrimSpace(part[:idx])
					// Skip empty, spaces, or non-identifier chars.
					if candidate == "" || strings.ContainsAny(candidate, " \t{}[]();,\"'<>") {
						continue
					}
					// v4-P1-1: Skip builtins (len, make, append, etc.)
					if builtins[candidate] {
						continue
					}
					// Batch4/H: common stdlib/interface methods on receivers
					// (f.Close, t.Fatal, filepath.Join via alias gaps).
					if commonStdlibMethods[candidate] {
						continue
					}
					// v4-P1-1: Skip standard library package prefixes.
					// Check if the text before this candidate is a stdlib package alias.
					// We check the previous segment for a package alias.
					if isStdlibCall(line, candidate, stdlibPackages, importedAliases) {
						continue
					}
					// Skip if candidate is a known imported alias itself.
					if importedAliases[candidate] {
						continue
					}
					// Skip type conversion patterns: Type(value) where Type is a type name.
					// These look like function calls but are conversions.
					if isLikelyTypeConversion(candidate, lines, lineNum) {
						continue
					}
					rel, _ := filepath.Rel(projectRoot, path)
					if !defined[candidate] {
						msg := fmt.Sprintf("missing %s referenced in %s:%d", candidate, rel, lineNum+1)
						// Dedup
						found := false
						for _, m := range missing {
							if m == msg {
								found = true
								break
							}
						}
						if !found {
							missing = append(missing, msg)
						}
					}
				}
			}
		}
		return nil
	})
	return missing
}

// parseImportLine extracts package aliases from an import statement.
func parseImportLine(line string, aliases map[string]bool) {
	line = strings.TrimSpace(line)
	// import "fmt" → alias = fmt
	if strings.HasPrefix(line, "import \"") {
		pkg := strings.Trim(line[len("import "):], "\"")
		alias := pkg
		if idx := strings.LastIndex(alias, "/"); idx >= 0 {
			alias = alias[idx+1:]
		}
		aliases[alias] = true
		return
	}
	// import alias "pkg" → alias
	if strings.HasPrefix(line, "import ") && strings.Contains(line, "\"") {
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			aliases[parts[1]] = true
		}
	}
	// Bare "pkg" in import block (multi-import)
	if strings.HasPrefix(line, "\"") {
		pkg := strings.Trim(line, "\"")
		alias := pkg
		if idx := strings.LastIndex(alias, "/"); idx >= 0 {
			alias = alias[idx+1:]
		}
		aliases[alias] = true
	}
}

// isStdlibCall checks if the candidate is being called as a method on a
// stdlib package alias (e.g., fmt.Println, os.Exit, sync.Mutex.Lock).
func isStdlibCall(line string, candidate string, stdlibPkgs map[string]bool, importedAliases map[string]bool) bool {
	// Find the candidate in the line and check what precedes the dot.
	candidateIdx := strings.Index(line, "."+candidate+"(")
	if candidateIdx < 0 {
		candidateIdx = strings.Index(line, "."+candidate+" (")
	}
	if candidateIdx < 0 {
		// Also check for method calls on stdlib types: r.Mutex.Lock()
		return false
	}
	// Get the text before the dot.
	before := line[:candidateIdx]
	// Find the last identifier before the dot.
	parts := strings.FieldsFunc(before, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_'
	})
	if len(parts) == 0 {
		return false
	}
	pkgAlias := parts[len(parts)-1]
	if stdlibPkgs[pkgAlias] || importedAliases[pkgAlias] {
		// The package alias is imported — but we still need to check if
		// the candidate is a method on a stdlib type (e.g., Mutex.Lock).
		// In that case the alias is the var name, not the package.
		// Heuristic: if the alias is a stdlib package name AND it appears
		// directly before the dot, treat it as a stdlib call.
		if stdlibPkgs[pkgAlias] {
			return true
		}
	}
	return false
}

// isLikelyTypeConversion detects patterns like int(x), string(b), []byte(s)
// which are type conversions, not function calls.
func isLikelyTypeConversion(candidate string, lines []string, lineNum int) bool {
	// Common type conversion names.
	conversionTypes := map[string]bool{
		"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
		"float32": true, "float64": true, "string": true, "bool": true,
		"byte": true, "rune": true, "[]byte": true, "[]rune": true,
		"[]string": true, "[]int": true,
	}
	return conversionTypes[candidate]
}

// TryAutoMarkPlanCriteria marks Success Criteria items in avatars_plan.md
// when they match completed task summaries or changed file names.
// taskInput is the original user task description for richer matching (W11a).
// buildOK/testOK gate verification-style criteria (tests pass / go run / cargo / npm).
// Returns the number of criteria marked as [x].
func TryAutoMarkPlanCriteria(projectRoot string, taskSummary string, changedFiles []string, buildOK bool, taskInput ...string) int {
	return TryAutoMarkPlanCriteriaWithTests(projectRoot, taskSummary, changedFiles, buildOK, buildOK, taskInput...)
}

// TryAutoMarkPlanCriteriaWithTests is like TryAutoMarkPlanCriteria but distinguishes
// compile (buildOK) from test suite (testOK). N1-3: "npm test passes" needs testOK.
func TryAutoMarkPlanCriteriaWithTests(projectRoot string, taskSummary string, changedFiles []string, buildOK, testOK bool, taskInput ...string) int {
	// NL smoke Batch1/P3: no file mutations → never invent criteria progress.
	if len(changedFiles) == 0 {
		return 0
	}
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return 0
	}

	planStr := string(content)

	scStart := strings.Index(planStr, "## Success Criteria")
	if scStart < 0 {
		return 0
	}

	afterSC := planStr[scStart:]
	scEnd := strings.Index(afterSC[1:], "\n## ")
	var scSection string
	if scEnd > 0 {
		scSection = afterSC[:scEnd+1]
	} else {
		scSection = afterSC
	}

	candidates := []string{taskSummary}
	for _, f := range changedFiles {
		candidates = append(candidates, filepath.Base(f))
		dir := filepath.Dir(f)
		for _, part := range strings.Split(dir, string(filepath.Separator)) {
			if len(part) > 3 {
				candidates = append(candidates, part)
			}
		}
	}

	// W11a: Extract keywords from task input for richer matching.
	if len(taskInput) > 0 && taskInput[0] != "" {
		ti := taskInput[0]
		for _, ext := range []string{".go", ".jsx", ".tsx", ".ts", ".js", ".py", ".rs"} {
			re := strings.Split(ti, ext)
			for j := 0; j < len(re)-1; j++ {
				before := re[j]
				words := strings.Fields(before)
				if len(words) > 0 {
					lastWord := strings.TrimFunc(words[len(words)-1], func(r rune) bool {
						return r == '"' || r == '\'' || r == '`' || r == ',' || r == '.'
					})
					if len(lastWord) > 1 {
						candidates = append(candidates, lastWord+ext, lastWord)
					}
				}
			}
		}
		candidates = append(candidates, extractActionWords(ti)...)
		for _, kw := range []string{"llm", "vision", "video", "provider", "services", "config", "frontend", "app", "settings"} {
			if strings.Contains(strings.ToLower(ti), kw) {
				candidates = append(candidates, kw)
			}
		}
		// Do NOT seed "compile"/"build"/"test" from user input alone — that
		// falsely marks Success Criteria when the build never passed.
	}

	codeText := collectGeneratedCodeText(projectRoot)
	lines := strings.Split(scSection, "\n")
	marked := 0
	activePhase := ParsePlanMeta(planStr).ActivePhase
	if activePhase < 1 {
		activePhase = 1
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		lowerDesc := strings.ToLower(strings.TrimPrefix(trimmed, "- [ ]"))
		// L2: Success Criteria with "Phase N:" only markable for Active Phase.
		if n := SuccessCriteriaPhaseNum(lowerDesc); n > 0 && n != activePhase {
			continue
		}
		// N1-3: Phase N criteria need that phase's deliverable checklist done
		// (not just Active Phase pointer + keyword hit from "test/" paths).
		if n := SuccessCriteriaPhaseNum(lowerDesc); n > 0 && !phaseChecklistReadyForCriteria(projectRoot, n) {
			continue
		}
		// R4-9: do not mark criteria that belong to later phase goals.
		if criteriaOverlapsLaterPhaseGoals(projectRoot, lowerDesc, activePhase) {
			continue
		}
		// X1: auth/DELETE/401 claims need real code evidence (cross-lang).
		if criteriaRequiresAuthOrDeleteEvidence(lowerDesc) &&
			!CriteriaAuthDeleteEvidenceOK(projectRoot, lowerDesc) {
			continue
		}
		// V4: health criteria need payload evidence; "tests pass" need a test tree.
		if criteriaLooksLikeHealthDesc(lowerDesc) {
			// defer to caller buildOK only if health payload also looks right —
			// TryAutoMarkPlanCriteria doesn't walk disk health here; require buildOK
			// AND presence of /health + ok|healthy in changed/summary later below.
			if !buildOK {
				continue
			}
			if !strings.Contains(strings.ToLower(taskSummary), "health") &&
				!strings.Contains(strings.ToLower(strings.Join(changedFiles, " ")), "health") &&
				!planHealthPayloadOK(projectRoot) {
				continue
			}
		}
		if criteriaLooksLikeTestsPassDesc(lowerDesc) {
			if !buildOK || !testOK || !planTestsEvidenceOK(projectRoot) {
				continue
			}
			// W4: coverage/% claims need real coverage evidence — never invent.
			if criteriaLooksLikeCoverageDesc(lowerDesc) {
				continue
			}
			// F109: race/sanitizer lines need that verifier, not a plain testOK.
			// Cross-lang: go test -race, -race, TSAN/ASAN, cargo + sanitizer.
			if criteriaLooksLikeRaceOrSanitizerDesc(lowerDesc) {
				continue
			}
			if !testsPassCriteriaMatchesProject(projectRoot, lowerDesc) {
				continue
			}
			// F73 residual: "go test / cargo test / npm test / pytest passes"
			// rarely overlaps a source filename, so keyword matching never
			// checks it even when the suite is green (r25). Cross-language.
			lines[i] = strings.Replace(trimmed, "- [ ]", "- [x]", 1)
			marked++
			continue
		}
		// J2-4: HTTP method/status claims need a green test suite (not just path keywords).
		if criteriaLooksLikeHTTPBehaviorDesc(lowerDesc) {
			if !buildOK || !testOK {
				continue
			}
		}
		// W4: structure/architecture claims need every mentioned path present.
		if criteriaLooksLikeStructureDesc(lowerDesc) {
			if !planStructurePathsOK(projectRoot, lowerDesc) {
				continue
			}
		}
		// Verification-style criteria need compile evidence (any language).
		if criteriaRequiresBuildEvidence(lowerDesc) && !buildOK {
			continue
		}
		// N1-3: npm/cargo/pytest wording also requires testOK.
		if criteriaLooksLikeTestsPassDesc(lowerDesc) && !testOK {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, "- [ ]"))
		if buildOK && testOK && (isChecklistItemSatisfied(projectRoot, desc, codeText) ||
			criteriaCoveredByGreenSuite(desc, codeText)) {
			lines[i] = strings.Replace(trimmed, "- [ ]", "- [x]", 1)
			marked++
			continue
		}
		for _, candidate := range candidates {
			lowerCandidate := strings.ToLower(candidate)
			if len(lowerCandidate) < 4 {
				continue
			}
			// Bare "test"/"build" must not mark future-phase criteria.
			if (lowerCandidate == "test" || lowerCandidate == "tests" || lowerCandidate == "build") &&
				SuccessCriteriaPhaseNum(lowerDesc) > 0 && SuccessCriteriaPhaseNum(lowerDesc) != activePhase {
				continue
			}
			// N1-3: bare "test"/"tests" from path parts must not mark whole npm-test criteria.
			if (lowerCandidate == "test" || lowerCandidate == "tests") && criteriaLooksLikeTestsPassDesc(lowerDesc) {
				continue
			}
			// W4: bare "app"/"api"/"models" must not mark whole structure lines.
			if criteriaLooksLikeStructureDesc(lowerDesc) && len(lowerCandidate) < 8 &&
				!strings.Contains(lowerCandidate, "/") && !strings.Contains(lowerCandidate, ".") {
				continue
			}
			if strings.Contains(lowerDesc, lowerCandidate) {
				lines[i] = strings.Replace(trimmed, "- [ ]", "- [x]", 1)
				marked++
				break
			}
		}
	}

	if marked == 0 {
		return SyncPhaseCriteriaMarksToPlan(projectRoot)
	}

	newSC := strings.Join(lines, "\n")
	newPlan := planStr[:scStart] + newSC
	if scEnd > 0 {
		newPlan += afterSC[scEnd+1:]
	}

	_ = WriteFileAtomic(planPath, []byte(newPlan), 0644)
	// L2: keep per-phase Status honest when Active Phase criteria get checked.
	_ = SetPhaseStatus(projectRoot, activePhase, "in-progress")
	marked += SyncPhaseCriteriaMarksToPlan(projectRoot)
	return marked
}

// SyncPhaseCriteriaMarksToPlan copies [x] Scope & Success Criteria from the
// active phase doc onto matching Success Criteria in avatars_plan.md so the
// two lists cannot disagree after a checklist-align run.
func SyncPhaseCriteriaMarksToPlan(projectRoot string) int {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return 0
	}
	meta := ParsePlanMeta(planContent)
	phaseNum := meta.ActivePhase
	if phaseNum < 1 {
		phaseNum = 1
	}
	phaseBody, err := os.ReadFile(PhaseDocPath(projectRoot, phaseNum))
	if err != nil {
		return 0
	}
	checked := checkedSuccessCriteria(string(phaseBody))
	if len(checked) == 0 {
		return 0
	}

	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	scStart := strings.Index(planContent, "## Success Criteria")
	if scStart < 0 {
		return 0
	}
	afterSC := planContent[scStart:]
	scEnd := strings.Index(afterSC[1:], "\n## ")
	var scSection string
	if scEnd > 0 {
		scSection = afterSC[:scEnd+1]
	} else {
		scSection = afterSC
	}
	lines := strings.Split(scSection, "\n")
	marked := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, "- [ ]"))
		if criteriaLooksLikeRaceOrSanitizerDesc(strings.ToLower(desc)) {
			continue
		}
		if !criteriaMatchesCheckedPhaseItem(desc, checked) {
			continue
		}
		lines[i] = strings.Replace(trimmed, "- [ ]", "- [x]", 1)
		marked++
	}
	if marked == 0 {
		return 0
	}
	newSC := strings.Join(lines, "\n")
	newPlan := planContent[:scStart] + newSC
	if scEnd > 0 {
		newPlan += afterSC[scEnd+1:]
	}
	if err := WriteFileAtomic(planPath, []byte(newPlan), 0644); err != nil {
		return 0
	}
	return marked
}

func checkedSuccessCriteria(phaseStr string) []string {
	var out []string
	inSuccess := false
	for _, line := range strings.Split(phaseStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSuccess = strings.Contains(trimmed, "Success Criteria")
			continue
		}
		if !inSuccess {
			continue
		}
		if strings.HasPrefix(trimmed, "- [x]") || strings.HasPrefix(trimmed, "- [X]") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "- [x]"), "- [X]")))
		}
	}
	return out
}

func criteriaMatchesCheckedPhaseItem(planDesc string, checked []string) bool {
	planIdents := exportedIdentsForCriteria(planDesc)
	planNorm := normalizeCriteriaText(planDesc)
	for _, item := range checked {
		if normalizeCriteriaText(item) == planNorm {
			return true
		}
		itemIdents := exportedIdentsForCriteria(item)
		if identOverlap(planIdents, itemIdents) {
			return true
		}
	}
	return false
}

func exportedIdentsForCriteria(desc string) []string {
	raw := reExportedAPIIdent.FindAllString(desc, -1)
	var out []string
	for _, id := range raw {
		switch strings.ToLower(id) {
		case "http", "json", "html", "uuid", "uri", "url", "cli", "api", "get", "put", "post",
			"for", "the", "and", "with", "when", "all", "not", "this", "from", "each",
			"true", "false", "both", "must", "never":
			continue
		}
		out = append(out, strings.ToLower(id))
	}
	return out
}

func identOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := map[string]bool{}
	for _, x := range b {
		set[x] = true
	}
	hit := 0
	for _, x := range a {
		if set[x] {
			hit++
		}
	}
	need := 1
	if len(a) >= 2 && len(b) >= 2 {
		need = 2
	}
	return hit >= need
}

func normalizeCriteriaText(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "`", "")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// PhaseChecklistReadyForCriteria reports whether Phase N's deliverable checklist
// is complete enough to allow Success Criteria auto-marks (R3 / N1-3).
func PhaseChecklistReadyForCriteria(projectRoot string, phaseNum int) bool {
	return phaseChecklistReadyForCriteria(projectRoot, phaseNum)
}

// phaseChecklistReadyForCriteria is true when Phase N's deliverable checklist
// items are done (verification-only leftovers OK). Prevents marking Phase 2
// Success Criteria while API-Key middleware tasks are still open (N1-3).
func phaseChecklistReadyForCriteria(projectRoot string, phaseNum int) bool {
	if phaseNum < 1 {
		return false
	}
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return false
	}
	section := extractPhaseChecklistSection(string(content), strconv.Itoa(phaseNum))
	if strings.TrimSpace(section) == "" {
		return false
	}
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		prefix, ok := incompleteCheckboxPrefix(trimmed)
		if !ok {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if isVerificationChecklistItem(desc) || checklistItemRequiresLiveTestEvidence(strings.ToLower(desc)) {
			continue
		}
		return false
	}
	return true
}

// SuccessCriteriaPhaseNum extracts N from "phase n:" / "Phase N –" / "阶段 n"
// prefixes on Success Criteria bullets. Returns 0 when not phase-scoped.
func SuccessCriteriaPhaseNum(lowerDesc string) int {
	s := strings.TrimSpace(lowerDesc)
	s = strings.TrimPrefix(s, "- [ ]")
	s = strings.TrimPrefix(s, "- [x]")
	s = strings.TrimSpace(s)
	// Colon, en-dash, em-dash, hyphen after phase number (X1 shiptrace: "Phase 2 –").
	re := regexp.MustCompile(`(?i)^phase\s+(\d+)\s*[:：\-–—]`)
	if m := re.FindStringSubmatch(s); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	reCN := regexp.MustCompile(`^阶段\s*(\d+)`)
	if m := reCN.FindStringSubmatch(s); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func criteriaLooksLikeHealthDesc(lowerDesc string) bool {
	return strings.Contains(lowerDesc, "/health") || strings.Contains(lowerDesc, "health endpoint") ||
		strings.Contains(lowerDesc, "health check")
}

func criteriaLooksLikeTestsPassDesc(lowerDesc string) bool {
	if strings.Contains(lowerDesc, "tests pass") ||
		(strings.Contains(lowerDesc, "test") && strings.Contains(lowerDesc, "pass")) ||
		strings.Contains(lowerDesc, "pytest") ||
		strings.Contains(lowerDesc, "cargo test") ||
		strings.Contains(lowerDesc, "npm test") ||
		strings.Contains(lowerDesc, "go test") ||
		strings.Contains(lowerDesc, "mvn test") ||
		strings.Contains(lowerDesc, "gradle test") ||
		strings.Contains(lowerDesc, "测试通过") ||
		strings.Contains(lowerDesc, "单测绿") ||
		strings.Contains(lowerDesc, "测试绿") ||
		strings.Contains(lowerDesc, "能测绿") ||
		strings.Contains(lowerDesc, "测绿") ||
		strings.Contains(lowerDesc, "suite is green") {
		return true
	}
	return false
}

// criteriaLooksLikeRaceOrSanitizerDesc is true for verifier lines that must
// not auto-check from a plain green suite (F109). Cross-language: Go -race,
// clang/gcc TSAN/ASAN, Rust sanitizer builds.
func criteriaLooksLikeRaceOrSanitizerDesc(lowerDesc string) bool {
	lowerDesc = strings.ToLower(lowerDesc)
	if strings.Contains(lowerDesc, "-race") || strings.Contains(lowerDesc, "–race") ||
		strings.Contains(lowerDesc, "race detector") || strings.Contains(lowerDesc, "race condition") {
		return true
	}
	if strings.Contains(lowerDesc, "-tsan") || strings.Contains(lowerDesc, "-asan") ||
		strings.Contains(lowerDesc, "thread sanitizer") || strings.Contains(lowerDesc, "address sanitizer") {
		return true
	}
	if strings.Contains(lowerDesc, "sanitizer") &&
		(strings.Contains(lowerDesc, "cargo") || strings.Contains(lowerDesc, "rustc") ||
			strings.Contains(lowerDesc, "clang") || strings.Contains(lowerDesc, "gcc")) {
		return true
	}
	return false
}

// testsPassCriteriaMatchesProject keeps toolchain-specific wording honest:
// "cargo test passes" must not check on a Go-only tree.
func testsPassCriteriaMatchesProject(projectRoot, lowerDesc string) bool {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(projectRoot, name))
		return err == nil
	}
	switch {
	case strings.Contains(lowerDesc, "cargo test"):
		return has("Cargo.toml")
	case strings.Contains(lowerDesc, "npm test"):
		return has("package.json")
	case strings.Contains(lowerDesc, "pytest"):
		return has("pyproject.toml") || has("pytest.ini") || has("setup.py") || has("requirements.txt")
	case strings.Contains(lowerDesc, "mvn test"):
		return has("pom.xml")
	case strings.Contains(lowerDesc, "gradle test"):
		return has("build.gradle") || has("build.gradle.kts")
	case strings.Contains(lowerDesc, "go test"):
		// Check after cargo: "cargo test" contains the letters "go test".
		return has("go.mod")
	default:
		return true
	}
}

func criteriaLooksLikeCoverageDesc(lowerDesc string) bool {
	return strings.Contains(lowerDesc, "coverage") ||
		(strings.Contains(lowerDesc, "%") && (strings.Contains(lowerDesc, "test") || strings.Contains(lowerDesc, "pytest")))
}

func criteriaLooksLikeStructureDesc(lowerDesc string) bool {
	return strings.Contains(lowerDesc, "structure") ||
		strings.Contains(lowerDesc, "layered architecture") ||
		strings.Contains(lowerDesc, "project structure") ||
		strings.Contains(lowerDesc, "directory layout")
}

// criteriaLooksLikeHTTPBehaviorDesc is true for Success Criteria that assert
// HTTP method/path/status behavior (e.g. "POST /notes returns 201"). These
// must not auto-check from route file names alone when tests are red (J2-4).
func criteriaLooksLikeHTTPBehaviorDesc(lowerDesc string) bool {
	hasMethod := strings.Contains(lowerDesc, "post ") || strings.Contains(lowerDesc, "get ") ||
		strings.Contains(lowerDesc, "put ") || strings.Contains(lowerDesc, "delete ") ||
		strings.Contains(lowerDesc, "patch ") ||
		strings.HasPrefix(strings.TrimSpace(lowerDesc), "post") ||
		strings.HasPrefix(strings.TrimSpace(lowerDesc), "get") ||
		strings.HasPrefix(strings.TrimSpace(lowerDesc), "put") ||
		strings.HasPrefix(strings.TrimSpace(lowerDesc), "delete")
	hasStatusOrReturn := strings.Contains(lowerDesc, "return") ||
		strings.Contains(lowerDesc, " 200") || strings.Contains(lowerDesc, " 201") ||
		strings.Contains(lowerDesc, " 204") || strings.Contains(lowerDesc, " 404") ||
		strings.Contains(lowerDesc, " 401") || strings.Contains(lowerDesc, " 403") ||
		strings.Contains(lowerDesc, " 409") ||
		strings.Contains(lowerDesc, "status") ||
		strings.Contains(lowerDesc, "unauthorized") || strings.Contains(lowerDesc, "conflict") ||
		strings.Contains(lowerDesc, "duplicate")
	hasPath := strings.Contains(lowerDesc, "/")
	if hasMethod && (hasStatusOrReturn || hasPath) {
		return true
	}
	if strings.Contains(lowerDesc, "endpoint") && (hasStatusOrReturn || hasMethod) {
		return true
	}
	return false
}

// PlanStructurePathsOK requires every path-like token in the criteria (e.g.
// app/api/, migrations/) to exist on disk (W4).
func PlanStructurePathsOK(projectRoot, lowerDesc string) bool {
	re := regexp.MustCompile(`(?:[a-z0-9_.-]+/)+[a-z0-9_.-]*/?`)
	paths := re.FindAllString(lowerDesc, -1)
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(p))); err != nil {
			return false
		}
	}
	return true
}

// planStructurePathsOK is kept as an alias for internal callers.
func planStructurePathsOK(projectRoot, lowerDesc string) bool {
	return PlanStructurePathsOK(projectRoot, lowerDesc)
}

var reExportedAPIIdent = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]{2,}\b`)

// criteriaCoveredByGreenSuite is true when a Success Criteria line names
// exported APIs that exist in source and the language test suite is already green.
func criteriaCoveredByGreenSuite(desc, codeText string) bool {
	if strings.TrimSpace(codeText) == "" {
		return false
	}
	idents := reExportedAPIIdent.FindAllString(desc, -1)
	if len(idents) == 0 {
		return false
	}
	codeLower := strings.ToLower(codeText)
	hit := 0
	for _, id := range idents {
		switch strings.ToLower(id) {
		case "http", "json", "html", "uuid", "uri", "url", "cli", "api", "get", "put", "post",
			"for", "the", "and", "with", "when", "all", "not", "this", "from", "each",
			"true", "false", "both", "must", "never":
			continue
		}
		if !strings.Contains(codeLower, strings.ToLower(id)) {
			return false
		}
		hit++
	}
	return hit > 0
}

func planHealthPayloadOK(projectRoot string) bool {
	ok := false
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", ".avatars", "venv", ".venv", "node_modules", "vendor", "__pycache__":
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		if ext != ".py" && ext != ".go" && ext != ".js" && ext != ".ts" {
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
		if strings.Contains(s, `"status"`) && (strings.Contains(s, `"ok"`) || strings.Contains(s, `"healthy"`)) {
			ok = true
			return filepath.SkipAll
		}
		return nil
	})
	return ok
}

// ProjectHasTestFiles reports whether any common-language test file exists
// (Go/Python/JS/TS/Rust). A runner that exits 0 with zero test files is not
// a green suite.
func ProjectHasTestFiles(projectRoot string) bool {
	return planTestsEvidenceOK(projectRoot)
}

// ProjectHasSubstantiveSources reports whether non-test implementation files
// exist. Used by local NL answers so empty directories stay quiet.
func ProjectHasSubstantiveSources(projectRoot string) bool {
	return countSubstantiveSourceFiles(projectRoot) > 0
}

// ProjectTestsGreen is true when the language test runner is green and the
// suite is not vacuous (test files exist). Cross-language via projectTestsPass.
func ProjectTestsGreen(projectRoot string) bool {
	return projectTestsPass(projectRoot)
}

func planTestsEvidenceOK(projectRoot string) bool {
	for _, dir := range []string{"tests", "test", "__tests__"} {
		if ents, err := os.ReadDir(filepath.Join(projectRoot, dir)); err == nil {
			for _, e := range ents {
				if !e.IsDir() {
					return true
				}
			}
		}
	}
	found := false
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", ".avatars", "venv", ".venv", "node_modules", "vendor":
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") ||
			(strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) ||
			strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".test.mjs") ||
			strings.HasSuffix(name, ".test.cjs") || strings.HasSuffix(name, ".test.ts") ||
			strings.HasSuffix(name, ".test.tsx") || strings.HasSuffix(name, ".spec.js") ||
			strings.HasSuffix(name, ".spec.ts") || strings.HasSuffix(name, ".spec.tsx") ||
			strings.HasSuffix(name, "_test.rs") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// substantiveTestSuite requires enough real test entrypoints that a gutted
// "2 tests left after truncating to pass compile" suite does not count (F68).
func substantiveTestSuite(projectRoot string) bool {
	return countAllWorkflowTestSymbols(projectRoot) >= 3
}

// countAllWorkflowTestSymbols counts test entrypoints across common languages
// (Go/Python/JS/TS/Rust). Used for F68 gates and F73 honest checklist counts.
func countAllWorkflowTestSymbols(projectRoot string) int {
	total := 0
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".avatars", "venv", ".venv", "node_modules", "vendor", "tmp", "temp", "scratch":
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		rel := path
		if r, e := filepath.Rel(projectRoot, path); e == nil {
			rel = r
		}
		relSlash := filepath.ToSlash(strings.ToLower(rel))
		isTest := strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") ||
			(strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) ||
			strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".test.ts") ||
			strings.HasSuffix(name, ".test.tsx") || strings.HasSuffix(name, ".spec.js") ||
			strings.HasSuffix(name, ".spec.ts") || strings.HasSuffix(name, "_test.rs") ||
			strings.Contains(relSlash, "/tests/") || strings.Contains(relSlash, "/__tests__/")
		if !isTest {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		total += countWorkflowTestSymbols(name, string(data))
		return nil
	})
	return total
}

func unmarkPhaseDocsTestPassClaims(projectRoot string) int {
	if projectTestsPass(projectRoot) && ProjectHasTestFiles(projectRoot) {
		return 0
	}
	n := 0
	for i := 1; i <= 8; i++ {
		n += unmarkTestPassCheckboxesInFile(PhaseDocPath(projectRoot, i), projectRoot)
	}
	return n
}

func unmarkTestPassCheckboxesInFile(path, projectRoot string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	changed := 0
	emptySuite := !ProjectHasTestFiles(projectRoot)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [x]") && !strings.HasPrefix(trimmed, "- [X]") {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), "waived") {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "- [x]"), "- [X]"))
		lower := strings.ToLower(desc)
		drop := checklistItemClaimsTestsPass(lower) || checklistItemLooksLikeVerification(lower)
		if emptySuite && (checklistItemRequiresLiveTestEvidence(lower) ||
			checklistItemLooksLikeNamedTest(desc) ||
			strings.Contains(lower, "write a test") ||
			strings.Contains(lower, "_test.go") ||
			strings.Contains(lower, "_test.py") ||
			strings.Contains(lower, ".test.") ||
			strings.Contains(lower, "unit test")) {
			drop = true
		}
		if drop {
			lines[i] = unmarkCheckbox(line)
			changed++
		}
	}
	if changed == 0 {
		return 0
	}
	_ = WriteFileAtomic(path, []byte(strings.Join(lines, "\n")), 0644)
	return changed
}

func checklistItemLooksLikeCoreImpl(lower string) bool {
	if checklistItemRequiresLiveTestEvidence(lower) {
		return false
	}
	needles := []string{
		"core library", "library implementation", "core api", "core impl",
		"implement", "implementation", "core types", "types and",
		"methods", "clock", "abstraction", "constructor", "public api",
		"scaffolding", "package scaffold", "module setup", "package structure",
		"data structure", "core method",
		"初始化模块", "包结构", "核心方法", "实现核心", "核心实现",
		"公开 api", "数据结构",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func checklistItemLooksLikeVerification(lower string) bool {
	if checklistItemRequiresLiveTestEvidence(lower) {
		return false
	}
	return strings.Contains(lower, "verification") ||
		strings.Contains(lower, "verify") ||
		strings.Contains(lower, "验证") ||
		strings.Contains(lower, "验收")
}

var (
	reGoStyleTestName = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*\b`)
	rePyStyleTestName = regexp.MustCompile(`\btest_[a-zA-Z0-9_]+\b`)
)

func checklistItemLooksLikeNamedTest(itemDesc string) bool {
	if strings.TrimSpace(itemDesc) == "" {
		return false
	}
	if reGoStyleTestName.MatchString(itemDesc) || rePyStyleTestName.MatchString(itemDesc) {
		return true
	}
	lower := strings.ToLower(itemDesc)
	return strings.Contains(lower, "#[test]") || strings.Contains(lower, "it('") ||
		strings.Contains(lower, "it(\"") || strings.Contains(lower, "test('")
}

func checklistItemLooksLikeAbsenceOrImportGate(lower string) bool {
	needles := []string{
		"no http", "no cli", "net/http", "no flag", "no handlers", "no server",
		"stdlib only", "no third-party", "no require", "external test package",
		"importable", "no vcs host",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func checklistItemLooksLikeStaticCheck(lower string) bool {
	needles := []string{
		"go vet", "gofmt", "go fmt", "cargo clippy", "clippy", "cargo fmt",
		"rustfmt", "eslint", "ruff", "mypy", "staticcheck", "tsc --noemit",
		"dart analyze", "dart format", "golangci", "prettier", "black --check",
		"dotnet format", "clang-format", "toolchain verification",
		"format check", "lint check",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func checklistItemLooksLikeDocumentation(lower string) bool {
	if checklistItemRequiresLiveTestEvidence(lower) || checklistItemLooksLikeCoreImpl(lower) ||
		checklistItemLooksLikeVerification(lower) {
		return false
	}
	needles := []string{
		"documentation", "readme", "docstring", "file header", "header comment",
		"header documentation", "api doc", "源码头", "文件头注释", "文件头",
		"说明文档", "文档",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// countDocumentationArtifacts counts user-facing docs: one root README plus
// source files that open with a file-header comment/docstring. Skips
// docs/workflow (avatars internals). Cross-language: Go/Python/JS/TS/Rust/Java/Kotlin/C#.
func countDocumentationArtifacts(projectRoot string) int {
	n := 0
	for _, name := range []string{"README.md", "README.rst", "README.txt", "README", "readme.md"} {
		info, err := os.Stat(filepath.Join(projectRoot, name))
		if err == nil && !info.IsDir() && info.Size() >= 20 {
			n++
			break
		}
	}
	n += countSourceFilesWithHeaderDocs(projectRoot)
	return n
}

// isCommonSourceExt is the language set avatars treats as first-class library
// sources for checklist honesty (Go/Python/JS/TS/Rust plus Java/Kotlin/C#).
func isCommonSourceExt(ext string) bool {
	switch ext {
	case ".go", ".py", ".rs", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs",
		".java", ".kt", ".cs":
		return true
	default:
		return false
	}
}

func countSourceFilesWithHeaderDocs(projectRoot string) int {
	count := 0
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".avatars", "venv", ".venv", "node_modules", "vendor", "tmp", "temp", "scratch", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") ||
			(strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) ||
			strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".test.ts") ||
			strings.HasSuffix(name, ".spec.js") || strings.HasSuffix(name, ".spec.ts") ||
			strings.HasSuffix(name, "_test.rs") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !isCommonSourceExt(ext) {
			return nil
		}
		if info.Size() < 40 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if sourceFileHasHeaderDoc(string(data)) {
			count++
		}
		return nil
	})
	return count
}

func sourceFileHasHeaderDoc(content string) bool {
	s := strings.TrimSpace(content)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "#!") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		} else {
			return false
		}
	}
	if strings.HasPrefix(s, "# -*-") || strings.HasPrefix(s, "# coding") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	switch {
	case strings.HasPrefix(s, "//!"), strings.HasPrefix(s, "/*!"), strings.HasPrefix(s, "/**"):
		return len(s) > 20
	case strings.HasPrefix(s, `"""`) || strings.HasPrefix(s, `'''`):
		return true
	case strings.HasPrefix(s, "/*"):
		return true
	case strings.HasPrefix(s, "//") || strings.HasPrefix(s, "#"):
		return headerLineCommentDoc(s)
	}
	return false
}

func headerLineCommentDoc(s string) bool {
	n := 0
	chars := 0
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") {
			n++
			chars += len(t)
			continue
		}
		break
	}
	return n >= 2 || chars >= 40
}

// countSubstantiveSourceFiles counts non-test source files with enough content
// to count as real library/app code (cross-language).
func countSubstantiveSourceFiles(projectRoot string) int {
	count := 0
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".avatars", "venv", ".venv", "node_modules", "vendor", "tmp", "temp", "scratch", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") ||
			(strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) ||
			strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".test.ts") ||
			strings.HasSuffix(name, ".spec.js") || strings.HasSuffix(name, ".spec.ts") ||
			strings.HasSuffix(name, "_test.rs") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !isCommonSourceExt(ext) {
			return nil
		}
		if info.Size() < 40 {
			return nil
		}
		count++
		return nil
	})
	return count
}

// ReconcileChecklistHonesty rewrites inflated (N/N) counts to match disk
// and marks remaining impl/verification rows when sources or tests exist.
func ReconcileChecklistHonesty(projectRoot string) int {
	n := reconcileChecklistCountHonesty(projectRoot)
	n += unmarkPhaseDocsTestPassClaims(projectRoot)
	return n
}

// reconcileChecklistCountHonesty rewrites inflated [x] (N/N) counts to match
// on-disk test/source evidence and marks remaining impl/verification rows (F73).
func reconcileChecklistCountHonesty(projectRoot string) int {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	data, err := os.ReadFile(todoPath)
	if err != nil {
		return 0
	}
	todoStr := string(data)
	phase := parseTodoPhaseNumber(todoStr)
	section := extractPhaseChecklistSection(todoStr, phase)
	if section == "" {
		return 0
	}
	checklistStart := strings.Index(todoStr, section)
	if checklistStart < 0 {
		return 0
	}
	after := todoStr[checklistStart:]
	end := strings.Index(after[1:], "\n## ")
	if end > 0 {
		section = after[:end+1]
	} else {
		section = after
	}
	testN := countAllWorkflowTestSymbols(projectRoot)
	srcN := countSubstantiveSourceFiles(projectRoot)
	compiles := projectCompiles(projectRoot)
	lines := strings.Split(section, "\n")
	changed := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		isDone := strings.HasPrefix(trimmed, "- [x]") || strings.HasPrefix(trimmed, "- [X]")
		prefix, incomplete := incompleteCheckboxPrefix(trimmed)
		if !isDone && !incomplete {
			continue
		}
		desc := trimmed
		if isDone {
			desc = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(desc, "- [x]"), "- [X]"))
		} else {
			desc = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
		lower := strings.ToLower(desc)
		switch {
		case checklistItemRequiresLiveTestEvidence(lower):
			if checklistItemClaimsTestsPass(lower) && !projectTestsPass(projectRoot) {
				if isDone {
					lines[i] = unmarkCheckbox(line)
					changed++
				}
				continue
			}
			if testN == 0 {
				if isDone {
					lines[i] = unmarkCheckbox(line)
					changed++
				}
				continue
			}
			if compiles {
				if !isDone && substantiveTestSuite(projectRoot) {
					lines[i] = markCheckboxDoneHonest(line, projectRoot)
					changed++
					continue
				}
				if isDone {
					next := replaceTrailingChecklistCount(line, testN, testN)
					if next != line {
						lines[i] = next
						changed++
					}
				}
			}
		case checklistItemLooksLikeCoreImpl(lower) && srcN > 0:
			if !isDone {
				lines[i] = markCheckboxDoneHonest(line, projectRoot)
				changed++
			} else {
				next := replaceTrailingChecklistCount(line, srcN, srcN)
				if next != line {
					lines[i] = next
					changed++
				}
			}
		case checklistItemLooksLikeVerification(lower):
			if !projectTestsPass(projectRoot) {
				if isDone {
					lines[i] = unmarkCheckbox(line)
					changed++
				}
				continue
			}
			if !isDone {
				lines[i] = markCheckboxDone(line)
				changed++
			}
		case checklistItemLooksLikeDocumentation(lower):
			docN := countDocumentationArtifacts(projectRoot)
			if docN == 0 {
				continue
			}
			if !isDone {
				lines[i] = markCheckboxDoneHonest(line, projectRoot)
				changed++
				continue
			}
			next := replaceTrailingChecklistCount(line, docN, docN)
			if next != line {
				lines[i] = next
				changed++
			}
		}
	}
	if changed == 0 {
		return 0
	}
	newSection := strings.Join(lines, "\n")
	newTodo := todoStr[:checklistStart] + newSection
	if end > 0 {
		newTodo += after[end+1:]
	}
	activeTasks := extractActiveChecklistLines(newTodo)
	if checklistSectionAllDone(activeTasks) {
		metaPhase := 1
		if n := ParseTodoActivePhase(newTodo); n > 0 {
			metaPhase = n
		}
		newTodo = markCurrentTaskDone(newTodo, metaPhase)
	}
	if err := WriteFileAtomic(todoPath, []byte(newTodo), 0644); err != nil {
		return 0
	}
	return changed
}

var (
	wfGoTestRe   = regexp.MustCompile(`(?m)^\s*func\s+Test[A-Z_]\w*\s*\(`)
	wfPyTestRe   = regexp.MustCompile(`(?m)^\s*def\s+test_\w+\s*\(`)
	wfJsTestRe   = regexp.MustCompile(`(?m)^\s*(?:it|test|describe)\s*\(`)
	wfRustTestRe = regexp.MustCompile(`(?m)^\s*#\s*\[\s*test\s*\]`)
)

func countWorkflowTestSymbols(baseName, content string) int {
	lower := strings.ToLower(baseName)
	switch {
	case strings.HasSuffix(lower, ".go"):
		return len(wfGoTestRe.FindAllStringIndex(content, -1))
	case strings.HasSuffix(lower, ".py"):
		return len(wfPyTestRe.FindAllStringIndex(content, -1))
	case strings.HasSuffix(lower, ".rs"):
		return len(wfRustTestRe.FindAllStringIndex(content, -1))
	case strings.HasSuffix(lower, ".js"), strings.HasSuffix(lower, ".jsx"),
		strings.HasSuffix(lower, ".ts"), strings.HasSuffix(lower, ".tsx"),
		strings.HasSuffix(lower, ".mjs"), strings.HasSuffix(lower, ".cjs"):
		return len(wfJsTestRe.FindAllStringIndex(content, -1))
	default:
		return len(wfGoTestRe.FindAllStringIndex(content, -1)) +
			len(wfPyTestRe.FindAllStringIndex(content, -1)) +
			len(wfJsTestRe.FindAllStringIndex(content, -1)) +
			len(wfRustTestRe.FindAllStringIndex(content, -1))
	}
}

// criteriaRequiresBuildEvidence reports Success Criteria that must not be
// auto-checked without a successful language health/build probe.
func criteriaRequiresBuildEvidence(lowerDesc string) bool {
	needles := []string{
		"test", "tests pass", "integration", "go run", "go build", "go test",
		"cargo ", "npm test", "pytest", "compile", "curl", "started with",
		"service can be started", "all integration",
		"returns 200", "returns 201", "returns 204", "returns 404",
		"post /", "get /", "put /", "delete /", "patch /",
		// G3: stub / leftover claims need a green build (hookbox falsely marked these).
		"stub", "leftover", "fully implemented", "no leftover",
	}
	for _, n := range needles {
		if strings.Contains(lowerDesc, n) {
			return true
		}
	}
	return false
}

// packageJSONHasTestScript reports whether package.json defines a "test" script.
func packageJSONHasTestScript(projectRoot string) bool {
	data, err := os.ReadFile(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), `"test"`)
}

// checklistItemRequiresLiveTestEvidence is true for checklist lines that claim
// tests exist/pass — path evidence alone must not mark them (N1-1).
func checklistItemRequiresLiveTestEvidence(lowerDesc string) bool {
	if strings.Contains(lowerDesc, "integration test") ||
		strings.Contains(lowerDesc, "unit test") ||
		strings.Contains(lowerDesc, "e2e test") ||
		strings.Contains(lowerDesc, "end-to-end") ||
		strings.Contains(lowerDesc, "end to end") ||
		strings.Contains(lowerDesc, "regression test") ||
		strings.Contains(lowerDesc, "test file") {
		return true
	}
	if strings.Contains(lowerDesc, "test") &&
		(strings.Contains(lowerDesc, "suite") || strings.Contains(lowerDesc, "coverage") ||
			strings.Contains(lowerDesc, "passing") || strings.Contains(lowerDesc, "npm") ||
			strings.Contains(lowerDesc, "pytest") || strings.Contains(lowerDesc, "cargo") ||
			strings.Contains(lowerDesc, "go test") || strings.Contains(lowerDesc, "regression")) {
		return true
	}
	// Bare "Integration Tests (3/3)" / "Tests" checklist titles.
	trimmed := strings.TrimSpace(lowerDesc)
	if strings.HasPrefix(trimmed, "integration test") || trimmed == "tests" ||
		strings.HasPrefix(trimmed, "tests ") || strings.HasPrefix(trimmed, "testing") {
		return true
	}
	if strings.Contains(lowerDesc, "write tests") || strings.Contains(lowerDesc, "add tests") ||
		strings.Contains(lowerDesc, "test cases") || strings.Contains(lowerDesc, "unit tests") {
		return true
	}
	if strings.Contains(lowerDesc, "编写测试") || strings.Contains(lowerDesc, "单元测试") ||
		strings.Contains(lowerDesc, "测试用例") || strings.Contains(lowerDesc, "写测试") ||
		strings.Contains(lowerDesc, "单测") {
		return true
	}
	if strings.Contains(lowerDesc, "测试") &&
		(strings.Contains(lowerDesc, "编写") || strings.Contains(lowerDesc, "单元") ||
			strings.Contains(lowerDesc, "用例")) {
		return true
	}
	return false
}

func checklistItemClaimsTestsPass(lowerDesc string) bool {
	if strings.Contains(lowerDesc, "-race") || strings.Contains(lowerDesc, "–race") ||
		strings.Contains(lowerDesc, "tsan") || strings.Contains(lowerDesc, "asan") {
		return true
	}
	if strings.Contains(lowerDesc, "write test") || strings.Contains(lowerDesc, "add test") ||
		strings.Contains(lowerDesc, "create test") || strings.Contains(lowerDesc, "编写测试") ||
		strings.Contains(lowerDesc, "写测试") || strings.Contains(lowerDesc, "写单元") {
		return false
	}
	return strings.Contains(lowerDesc, "go test") ||
		strings.Contains(lowerDesc, "pytest") ||
		strings.Contains(lowerDesc, "npm test") ||
		strings.Contains(lowerDesc, "cargo test") ||
		strings.Contains(lowerDesc, "dotnet test") ||
		strings.Contains(lowerDesc, "mvn test") ||
		strings.Contains(lowerDesc, "passing") ||
		strings.Contains(lowerDesc, "passes") ||
		strings.Contains(lowerDesc, "跑绿") ||
		strings.Contains(lowerDesc, "测试通过") ||
		(strings.Contains(lowerDesc, "verify") && strings.Contains(lowerDesc, "test")) ||
		(strings.Contains(lowerDesc, "验证") && strings.Contains(lowerDesc, "测试"))
}

var testsPassCache sync.Map

func projectTestsPass(projectRoot string) bool {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		root = projectRoot
	}
	if v, ok := testsPassCache.Load(root); ok {
		return v.(bool)
	}
	ok := runProjectTestsPass(root)
	if ok && !planTestsEvidenceOK(root) {
		ok = false
	}
	testsPassCache.Store(root, ok)
	return ok
}

func runProjectTestsPass(projectRoot string) bool {
	if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "test", "./...")
		cmd.Dir = projectRoot
		platform.HideConsoleWindow(cmd)
		return cmd.Run() == nil
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "Cargo.toml")); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "cargo", "test")
		cmd.Dir = projectRoot
		platform.HideConsoleWindow(cmd)
		return cmd.Run() == nil
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "package.json")); err == nil && packageJSONHasTestScript(projectRoot) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "npm", "test", "--", "--watchAll=false")
		cmd.Dir = projectRoot
		platform.HideConsoleWindow(cmd)
		return cmd.Run() == nil
	}
	if fileExistsJoin(projectRoot, "pytest.ini") || fileExistsJoin(projectRoot, "pyproject.toml") || hasFilesWithExt(projectRoot, "_test.py") {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "python", "-m", "pytest", "-q")
		cmd.Dir = projectRoot
		platform.HideConsoleWindow(cmd)
		if cmd.Run() == nil {
			return true
		}
		cmd3 := exec.CommandContext(ctx, "python3", "-m", "pytest", "-q")
		cmd3.Dir = projectRoot
		platform.HideConsoleWindow(cmd3)
		return cmd3.Run() == nil
	}
	return false
}

// projectCompiles does a quick multi-language build/syntax check.
// Returns false when a known toolchain fails — phase must NOT advance.
func projectCompiles(projectRoot string) bool {
	// Go
	goModPath := filepath.Join(projectRoot, "go.mod")
	if _, err := os.Stat(goModPath); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "./...")
		cmd.Dir = projectRoot
		if err := cmd.Run(); err != nil {
			return false
		}
		return true
	}
	// Rust
	cargoPath := filepath.Join(projectRoot, "Cargo.toml")
	if _, err := os.Stat(cargoPath); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "cargo", "check")
		cmd.Dir = projectRoot
		if err := cmd.Run(); err != nil {
			return false
		}
		return true
	}
	// Node / TypeScript — prefer real test/compile evidence (N1-1/5).
	// Previously returned true after scanning only root *.js (src-only trees
	// always looked green) and never ran npm test → false Phase advance.
	if _, err := os.Stat(filepath.Join(projectRoot, "package.json")); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		if _, err := os.Stat(filepath.Join(projectRoot, "tsconfig.json")); err == nil {
			cmd := exec.CommandContext(ctx, "npx", "--yes", "tsc", "--noEmit")
			cmd.Dir = projectRoot
			if err := cmd.Run(); err != nil {
				return false
			}
		}
		if packageJSONHasTestScript(projectRoot) {
			cmd := exec.CommandContext(ctx, "npm", "test", "--", "--watchAll=false")
			cmd.Dir = projectRoot
			if err := cmd.Run(); err != nil {
				return false
			}
			return true
		}
		// No test script: syntax-check JS/TS under src/ (and root), not root-only.
		ok := false
		_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				switch info.Name() {
				case "node_modules", ".git", ".avatars", "dist", "build", "coverage":
					return filepath.SkipDir
				}
				return nil
			}
			name := strings.ToLower(info.Name())
			if !strings.HasSuffix(name, ".js") && !strings.HasSuffix(name, ".mjs") &&
				!strings.HasSuffix(name, ".cjs") {
				return nil
			}
			cmd := exec.CommandContext(ctx, "node", "--check", path)
			cmd.Dir = projectRoot
			if err := cmd.Run(); err != nil {
				ok = false
				return filepath.SkipAll
			}
			ok = true
			return nil
		})
		return ok
	}
	// Python — compile all .py under project (bounded).
	if hasFilesWithExt(projectRoot, ".py") || fileExistsJoin(projectRoot, "pyproject.toml") || fileExistsJoin(projectRoot, "requirements.txt") {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var pyFiles []string
		_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				if info != nil && info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "avatars" || info.Name() == ".avatars" || info.Name() == "vendor") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".py") {
				pyFiles = append(pyFiles, path)
			}
			return nil
		})
		for _, f := range pyFiles {
			cmd := exec.CommandContext(ctx, "python", "-m", "py_compile", f)
			cmd.Dir = projectRoot
			if err := cmd.Run(); err != nil {
				// Try python3
				cmd3 := exec.CommandContext(ctx, "python3", "-m", "py_compile", f)
				cmd3.Dir = projectRoot
				if err3 := cmd3.Run(); err3 != nil {
					return false
				}
			}
		}
		return true
	}
	// No build system detected — can't verify compilation, allow advance.
	return true
}

func fileExistsJoin(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

// hasFilesWithExt returns true if the directory contains at least one file
// with the given extension (non-recursive, top-level only for speed).
func hasFilesWithExt(dir string, ext string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			return true
		}
	}
	return false
}

// isVerificationChecklistItem returns true if the checklist item describes
// a build/test/check action rather than a code deliverable.
func isVerificationChecklistItem(desc string) bool {
	lower := strings.ToLower(desc)
	verifyPatterns := []string{
		"cargo build", "cargo test", "cargo fmt", "cargo clippy",
		"go build", "go test", "go fmt", "go vet",
		"pytest", "npm test", "npm run",
		"integration test", "unit test", "e2e test",
		"compile", "compilation",
		"zero error", "zero warning", "no error", "no warning",
		"pass", "passes", "passing",
		"ci", "ci/cd", "pipeline",
		"to confirm", "verify that",
	}
	for _, p := range verifyPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// CriticDecidePhaseAdvance is the Critic's phase-completion decision.
// Instead of ratio-based gating, the Critic (总导演) evaluates whether
// the phase is substantially complete:
//   - If ALL remaining items are verification-type → phase complete → advance
//   - If any code deliverable items remain → not complete → don't advance
//
// This prevents verification items from distorting completion ratios on small lists.
//
// maxPhase: when >0 (user PHASE LOCK), never advance past that phase (Batch5/N).
func CriticDecidePhaseAdvance(projectRoot string) (advanced bool, newPhase int) {
	return CriticDecidePhaseAdvanceUpTo(projectRoot, 0)
}

// CriticDecidePhaseAdvanceUpTo is like CriticDecidePhaseAdvance but refuses to
// leave a user-locked phase (maxPhase). maxPhase<=0 means no lock.
func CriticDecidePhaseAdvanceUpTo(projectRoot string, maxPhase int) (advanced bool, newPhase int) {
	if maxPhase > 0 {
		planContent, err := ReadWorkflowDoc(projectRoot, "plan")
		if err == nil {
			meta := ParsePlanMeta(planContent)
			if meta.ActivePhase >= maxPhase {
				return false, 0
			}
		}
	}

	// R4-2/R4-9: sync path-evidence marks before counting remaining items.
	_ = CriticAuditAndMark(projectRoot)

	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return false, 0
	}
	todoStr := string(content)
	// C7: normalize any [x]…(0/N) leftovers before counting.
	if fixed := normalizeCheckedZeroCounts(todoStr); fixed != todoStr {
		todoStr = fixed
		_ = WriteFileAtomic(todoPath, []byte(todoStr), 0644)
	}

	// Find remaining [ ] items in the Active Phase checklist only (R3).
	var remaining []string
	section := extractPhaseChecklistSection(todoStr, parseTodoPhaseNumber(todoStr))
	if section == "" {
		return false, 0
	}
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		prefix, ok := incompleteCheckboxPrefix(trimmed)
		if !ok {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		remaining = append(remaining, desc)
	}

	if len(remaining) == 0 {
		// All items done — but verify code actually compiles before advancing.
		if !projectCompiles(projectRoot) {
			return false, 0 // build broken — Critic must dispatch Builder to fix
		}
		// C2: refuse advance when phase.md Tasks still don't cover Scope.
		if phaseNum := parseTodoPhaseNumber(todoStr); phaseNum != "" {
			if n, err := strconv.Atoi(phaseNum); err == nil && n > 0 {
				if raw, err := os.ReadFile(PhaseDocPath(projectRoot, n)); err == nil {
					if !phaseTasksCoverScope(string(raw)) {
						return false, 0
					}
				}
			}
		}
		return tryAdvancePhaseUpTo(projectRoot, maxPhase)
	}

	// Check if ALL remaining items are verification-type.
	allVerification := true
	for _, item := range remaining {
		if !isVerificationChecklistItem(item) {
			allVerification = false
			break
		}
	}

	if allVerification && len(remaining) > 0 {
		// VG-1: Hard gate — code MUST compile before advancing.
		if !projectCompiles(projectRoot) {
			return false, 0
		}
		todoStr = markRemainingAsDeferredByCritic(todoStr)
		_ = WriteFileAtomic(todoPath, []byte(todoStr), 0644)
		return tryAdvancePhaseUpTo(projectRoot, maxPhase)
	}

	// R7-1: health-green + soft layout/dir evidence for every remaining item →
	// mark and advance in the same Critic pass (do not wait for CLI auto-retry).
	if projectCompiles(projectRoot) {
		codeText := collectGeneratedCodeText(projectRoot)
		allSoftOK := true
		for _, item := range remaining {
			if isVerificationChecklistItem(item) {
				continue
			}
			if isChecklistItemSatisfied(projectRoot, item, codeText) {
				continue
			}
			if checklistItemHasLayoutDirEvidence(projectRoot, item) {
				continue
			}
			allSoftOK = false
			break
		}
		if allSoftOK {
			todoStr = markRemainingAsDeferredByCritic(todoStr)
			_ = WriteFileAtomic(todoPath, []byte(todoStr), 0644)
			return tryAdvancePhaseUpTo(projectRoot, maxPhase)
		}
	}

	// F108: Builder already delivered later-phase files while Active Phase
	// is still on an earlier checklist. Cross-language path evidence; do not
	// stick on checklist_incomplete when disk is ahead.
	if projectCompiles(projectRoot) && laterPhaseDeliverablesOnDisk(projectRoot) {
		todoStr = markRemainingAsDeferredByCritic(todoStr)
		_ = WriteFileAtomic(todoPath, []byte(todoStr), 0644)
		return tryAdvancePhaseUpTo(projectRoot, maxPhase)
	}

	// Code deliverable items remain — Critic should dispatch Builder, not advance.
	return false, 0
}

// laterPhaseDeliverablesOnDisk reports that a later phase's named files already
// exist (F108). Used so Active Phase does not freeze when Builder shipped ahead.
func laterPhaseDeliverablesOnDisk(projectRoot string) bool {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return false
	}
	meta := ParsePlanMeta(planContent)
	active := meta.ActivePhase
	if active < 1 {
		active = 1
	}
	max := meta.PhaseCount
	if max < active+1 {
		return false
	}
	for n := active + 1; n <= max; n++ {
		if phaseHasPathEvidenceOnDisk(projectRoot, n) {
			return true
		}
	}
	return false
}

func phaseHasPathEvidenceOnDisk(projectRoot string, phaseNum int) bool {
	data, err := os.ReadFile(PhaseDocPath(projectRoot, phaseNum))
	if err != nil {
		return false
	}
	inTasks := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inTasks = strings.HasPrefix(trimmed, "## Tasks")
			continue
		}
		if !inTasks {
			continue
		}
		desc := trimmed
		for _, p := range []string{"- [ ] ", "- [x] ", "- [>] ", "- [X] "} {
			if strings.HasPrefix(desc, strings.TrimSpace(p)) || strings.HasPrefix(desc, p) {
				desc = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(desc), strings.TrimSpace(p)))
				break
			}
		}
		if !strings.HasPrefix(trimmed, "- [") {
			continue
		}
		if checklistItemHasPathEvidence(projectRoot, desc) {
			return true
		}
	}
	return false
}

// markRemainingAsDeferredByCritic marks remaining [ ] items as [x] with a
// "verified by Critic" note. Used when Critic decides all remaining items
// are verification-type and the phase is substantially complete.
// markRemainingAsDeferredByCritic marks remaining [ ] items in the Phase
// Checklist section only (S2.4) — never Current Task / Blockers.
func markRemainingAsDeferredByCritic(todoStr string) string {
	lines := strings.Split(todoStr, "\n")
	inChecklist := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Phase ") && strings.Contains(trimmed, "Checklist") {
			inChecklist = true
			continue
		}
		if inChecklist && isH2(trimmed) {
			inChecklist = false
		}
		if !inChecklist {
			continue
		}
		if _, ok := incompleteCheckboxPrefix(strings.TrimSpace(line)); ok {
			// Skip legacy truncation placeholders if any remain.
			if strings.Contains(trimmed, "more items — see full plan") {
				continue
			}
			lines[i] = markCheckboxDone(line) + " (verified by Critic)"
		}
	}
	return strings.Join(lines, "\n")
}

// TryAdvancePhase advances the workflow to the next phase when the current
// phase's checklist is fully complete. v3-P0-2: This function NOW ACTUALLY
// ADVANCES the phase (was returning false,0 which broke phase progression).
// It updates the plan's Active Phase marker and syncs the next phase's todo.
// CriticDecidePhaseAdvance calls this after verifying projectCompiles.
func TryAdvancePhase(projectRoot string) (advanced bool, newPhase int) {
	return tryAdvancePhaseUpTo(projectRoot, 0)
}

func tryAdvancePhaseUpTo(projectRoot string, maxPhase int) (advanced bool, newPhase int) {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	todoContent, err := os.ReadFile(todoPath)
	if err != nil {
		return false, 0
	}
	todoStr := string(todoContent)

	// First, auto-mark bootstrap items that are always present.
	origTodoStr := todoStr
	todoStr = autoMarkBootstrapItems(todoStr, projectRoot)
	if todoStr != origTodoStr {
		_ = WriteFileAtomic(todoPath, []byte(todoStr), 0644)
	}

	// Read the plan to find current active phase and total phase count.
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	planContent, err := os.ReadFile(planPath)
	if err != nil {
		return false, 0
	}
	meta := ParsePlanMeta(string(planContent))
	if meta.PhaseCount == 0 {
		return false, 0
	}

	// Batch5/N: user PHASE LOCK — refuse to leave locked phase.
	if maxPhase > 0 && meta.ActivePhase >= maxPhase {
		return false, 0
	}

	// Check if current phase checklist is fully complete.
	completed, total := CountTodoProgressFromContent(todoStr)
	if total == 0 {
		return false, 0
	}
	if completed < total {
		return false, 0 // still has incomplete items
	}

	// Current phase is complete — advance to next.
	nextPhase := meta.ActivePhase + 1
	if maxPhase > 0 && nextPhase > maxPhase {
		return false, 0
	}
	if nextPhase > meta.PhaseCount {
		// All phases complete — mark plan as completed.
		// X1/Y1: refuse completed when auth/DELETE criteria lack code evidence,
		// or the last phase doc is still a deferred stub.
		if PlanHasOpenAuthDeleteCriteria(projectRoot) {
			_ = UnmarkSuccessCriteriaMissingAuthDeleteEvidence(projectRoot)
			return false, 0
		}
		if PhaseDocNeedsFullDetail(projectRoot, meta.ActivePhase) {
			return false, 0
		}
		_ = UpdatePlanStatus(projectRoot, "completed")
		return true, 0
	}

	// Update the plan's Active Phase marker.
	if err := UpdateActivePhase(projectRoot, nextPhase); err != nil {
		return false, 0
	}

	// G4/X3: path/layout evidence then force-complete prior phase checklist.
	_ = MarkPhaseChecklistByPathEvidence(projectRoot, meta.ActivePhase)
	_ = forceCompletePhaseChecklist(projectRoot, meta.ActivePhase)

	// Sync the next phase's todo checklist.
	if err := SyncTodoForPhase(projectRoot, nextPhase); err != nil {
		return false, 0
	}

	// S6.7: Bookmark the first incomplete item so resume/LLM know where to pick up.
	_ = UpdateTodoProgressBookmark(projectRoot)

	return true, nextPhase
}

// forceCompletePhaseChecklist marks all incomplete items in Phase N's checklist
// as [x] (G4: leftover [>] after soft-advance / phase_advanced).
func forceCompletePhaseChecklist(projectRoot string, phaseNum int) error {
	if phaseNum < 1 {
		return nil
	}
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return err
	}
	todoStr := string(content)
	section := extractPhaseChecklistSection(todoStr, strconv.Itoa(phaseNum))
	if section == "" {
		return nil
	}
	start := strings.Index(todoStr, section)
	if start < 0 {
		return nil
	}
	after := todoStr[start:]
	end := strings.Index(after[1:], "\n## ")
	var body string
	if end > 0 {
		body = after[:end+1]
	} else {
		body = after
	}
	lines := strings.Split(body, "\n")
	changed := false
	for i, line := range lines {
		if _, ok := incompleteCheckboxPrefix(strings.TrimSpace(line)); ok {
			lines[i] = markCheckboxDone(line)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	newBody := strings.Join(lines, "\n")
	newTodo := todoStr[:start] + newBody
	if end > 0 {
		newTodo += after[end+1:]
	}
	return WriteFileAtomic(todoPath, []byte(newTodo), 0644)
}

// autoMarkBootstrapItems marks meta/bootstrap items that are always satisfied
// when the corresponding files exist. E.g., "Define phase 1 tasks" when phase1.md exists.
func autoMarkBootstrapItems(todoStr string, projectRoot string) string {
	lines := strings.Split(todoStr, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		prefix, ok := incompleteCheckboxPrefix(trimmed)
		if !ok {
			continue
		}
		desc := strings.ToLower(strings.TrimPrefix(trimmed, prefix))
		desc = strings.TrimSpace(desc)
		// "Define phase N tasks" — check if phaseN.md exists (deterministic evidence).
		if strings.Contains(desc, "define phase") && strings.Contains(desc, "tasks") {
			for n := 1; n <= 5; n++ {
				if strings.Contains(desc, fmt.Sprintf("phase %d", n)) || strings.Contains(desc, fmt.Sprintf("phase%d", n)) {
					phasePath := filepath.Join(projectRoot, "docs", "workflow", fmt.Sprintf("phase%d.md", n))
					if _, err := os.Stat(phasePath); err == nil {
						lines[i] = markCheckboxDone(line)
						changed = true
					}
					break
				}
			}
		}
		// NOTE: We do NOT auto-mark "Save the file", "Run cargo test", etc.
		// Those require actual evidence (verification results, file content checks).
		// The Critic is the 总导演 — it must actually audit, not auto-mark blindly.
	}
	if changed {
		return strings.Join(lines, "\n")
	}
	return todoStr
}

// evidenceBasedVerificationMark marks build/test checklist items when
// real verification evidence exists (S6.7 — was dead; now called from CriticAuditAndMark).
func evidenceBasedVerificationMark(todoStr string, projectRoot string) string {
	lines := strings.Split(todoStr, "\n")
	changed := false
	compiles := projectCompiles(projectRoot)
	hasRustTarget := false
	if info, err := os.Stat(filepath.Join(projectRoot, "target")); err == nil && info.IsDir() {
		hasRustTarget = true
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		prefix, ok := incompleteCheckboxPrefix(trimmed)
		if !ok {
			continue
		}
		desc := strings.ToLower(strings.TrimPrefix(trimmed, prefix))
		desc = strings.TrimSpace(desc)

		isBuildItem := strings.Contains(desc, "go build") || strings.Contains(desc, "cargo build") ||
			(strings.Contains(desc, "compile") && (strings.Contains(desc, "build") || strings.Contains(desc, "pass") || strings.Contains(desc, "error")))
		isRaceItem := strings.Contains(desc, "go test -race") || strings.Contains(desc, "go test –race") ||
			(strings.Contains(desc, "go test") && strings.Contains(desc, "-race"))
		isTestItem := !isRaceItem && (strings.Contains(desc, "go test") || strings.Contains(desc, "cargo test") || strings.Contains(desc, "pytest"))

		if isBuildItem {
			ok := false
			if strings.Contains(desc, "cargo") {
				ok = hasRustTarget
			} else {
				ok = compiles
			}
			if ok {
				lines[i] = markCheckboxDoneHonest(line, projectRoot)
				changed = true
			}
		}
		// F51: race checks that cannot run (no cgo) must not block phase advance.
		// F109: do not [x] as if race actually ran — waive explicitly, never
		// mark from compile-green alone.
		if isRaceItem {
			if goRaceUnavailable(projectRoot) {
				lines[i] = markCheckboxDone(line) + " (waived: -race requires cgo; unavailable in this environment)"
				changed = true
			}
		}
		if isTestItem {
			ok := false
			if strings.Contains(desc, "cargo") {
				ok = projectTestsPass(projectRoot)
			} else if strings.Contains(desc, "go test") || strings.Contains(desc, "pytest") || strings.Contains(desc, "npm test") {
				ok = projectTestsPass(projectRoot)
			}
			if ok {
				lines[i] = markCheckboxDoneHonest(line, projectRoot)
				changed = true
			}
		}
	}
	if changed {
		return strings.Join(lines, "\n")
	}
	return todoStr
}

// goRaceUnavailable reports that `go test -race` cannot run in this environment
// (typically Windows without cgo). Used to waive checklist items (F51).
func goRaceUnavailable(projectRoot string) bool {
	if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "CGO_ENABLED")
	cmd.Dir = projectRoot
	platform.HideConsoleWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err == nil {
		val := strings.TrimSpace(string(out))
		if val == "0" {
			return true
		}
	}
	// Probe race briefly; treat cgo-required errors as unavailable.
	probe := exec.CommandContext(ctx, "go", "test", "-race", "-c", "-o", os.DevNull, ".")
	probe.Dir = projectRoot
	platform.HideConsoleWindow(probe)
	probeOut, probeErr := probe.CombinedOutput()
	if probeErr == nil {
		return false
	}
	joined := strings.ToLower(string(probeOut) + " " + probeErr.Error())
	return strings.Contains(joined, "cgo") || strings.Contains(joined, "-race requires")
}

// markRemainingAsDeferred converts remaining [ ] items to [x] with a "(deferred)" suffix.
// Used when phase completion reaches 85%+ threshold.
func markRemainingAsDeferred(todoStr string) string {
	lines := strings.Split(todoStr, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if _, ok := incompleteCheckboxPrefix(trimmed); ok {
			lines[i] = markCheckboxDone(line) + " (deferred: verified by Critic as substantially complete)"
		}
	}
	return strings.Join(lines, "\n")
}

// extractChecklistItems extracts all incomplete task descriptions from markdown content.
func extractChecklistItems(mdContent string) []string {
	var items []string
	for _, line := range strings.Split(mdContent, "\n") {
		trimmed := strings.TrimSpace(line)
		prefix, ok := incompleteCheckboxPrefix(trimmed)
		if !ok {
			continue
		}
		desc := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if len(desc) > 3 {
			items = append(items, desc)
		}
	}
	return items
}
