package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxChecklistItems caps how many group rows appear in avatars_todo.md.
// Truncation placeholders must NOT count toward CountTodoProgress (S2.3).
var MaxChecklistItems = 16

// SyncTodoFromPlan refreshes avatars_todo.md with ALL phase checklists
// (Phase 1..PhaseCount), keeping group title rows. The header **Phase** points
// at the plan Active Phase (current work), but completed earlier phases stay
// visible so users see overall progress without checklist swap-loss.
func SyncTodoFromPlan(projectRoot string) error {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	planPath := filepath.Join(projectRoot, DocPaths["plan"])

	planContent, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	todoContent, err := os.ReadFile(todoPath)
	if err != nil {
		return fmt.Errorf("read todo: %w", err)
	}

	planStr := string(planContent)
	todoStr := string(todoContent)
	meta := ParsePlanMeta(planStr)
	activePhase := strconv.Itoa(meta.ActivePhase)
	if meta.ActivePhase < 1 {
		activePhase = "1"
	}
	phaseCount := meta.PhaseCount
	if phaseCount < 1 {
		phaseCount = 1
	}

	return rebuildAllPhaseChecklists(projectRoot, todoPath, todoStr, activePhase, phaseCount)
}

func parseActivePhaseNumber(planStr string) string {
	if idx := strings.Index(planStr, "**Active Phase**:"); idx >= 0 {
		rest := planStr[idx:]
		if end := strings.Index(rest, "\n"); end > 0 {
			phaseLine := rest[:end]
			phaseLine = strings.TrimPrefix(phaseLine, "**Active Phase**:")
			return strings.TrimSpace(phaseLine)
		}
	}
	return ""
}

func parseTodoPhaseNumber(todoStr string) string {
	if idx := strings.Index(todoStr, "**Phase**: "); idx >= 0 {
		rest := todoStr[idx+len("**Phase**: "):]
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			return strings.TrimSpace(fields[0])
		}
	}
	// Fallback: "## Phase N Checklist"
	if idx := strings.Index(todoStr, "## Phase "); idx >= 0 {
		rest := todoStr[idx+len("## Phase "):]
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			if _, err := strconv.Atoi(fields[0]); err == nil {
				return fields[0]
			}
		}
	}
	return ""
}

// ParseTodoActivePhase returns the **Phase**: N header from todo content, or 0.
func ParseTodoActivePhase(todoContent string) int {
	s := parseTodoPhaseNumber(todoContent)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func rebuildTodoChecklistFromPhase(projectRoot, todoPath, todoStr, activePhase string) error {
	// Legacy single-phase entry point — expand to all known phases.
	count := 1
	if n, err := strconv.Atoi(activePhase); err == nil && n > count {
		count = n
	}
	for n := 1; n <= 12; n++ {
		if _, err := os.Stat(filepath.Join(projectRoot, "docs", "workflow", fmt.Sprintf("phase%d.md", n))); err == nil {
			if n > count {
				count = n
			}
		}
	}
	return rebuildAllPhaseChecklists(projectRoot, todoPath, todoStr, activePhase, count)
}

// rebuildAllPhaseChecklists writes Phase 1..phaseCount checklist sections.
// Existing [x] marks are preserved when group labels still match.
func rebuildAllPhaseChecklists(projectRoot, todoPath, todoStr, activePhase string, phaseCount int) error {
	if phaseCount < 1 {
		phaseCount = 1
	}
	activeN, _ := strconv.Atoi(activePhase)
	if activeN < 1 {
		activeN = 1
	}
	if !strings.Contains(todoStr, "## Phase Checklist") && !strings.Contains(todoStr, "## Completed") &&
		!strings.Contains(todoStr, "Checklist") {
		if tmpl, err := embeddedTemplates.ReadFile(templateTodo); err == nil {
			todoStr = string(tmpl)
		}
	}
	// Batch5/N: always normalize LLM "Current Phase: Phase 3" junk to **Phase**: N.
	todoStr = normalizeTodoPhaseHeader(todoStr, activeN, phaseCount)
	todoStr = replacePhaseHeaderNumber(todoStr, activePhase)

	// Slice off every existing "## Phase … Checklist" block through the section
	// before ## Completed / ## Blockers / ## Notes (non-phase H2).
	before, after := splitAroundPhaseChecklists(todoStr)
	completedDescs := collectCompletedTaskDescs(todoStr)

	var body strings.Builder
	body.WriteString(before)
	if before != "" && !strings.HasSuffix(before, "\n\n") {
		if !strings.HasSuffix(before, "\n") {
			body.WriteByte('\n')
		}
		body.WriteByte('\n')
	}
	wroteAny := false
	activeAllDone := false
	for n := 1; n <= phaseCount; n++ {
		phasePath := filepath.Join(projectRoot, "docs", "workflow", fmt.Sprintf("phase%d.md", n))
		tasks := extractPhaseGroupChecklist(phasePath)
		phaseStr := strconv.Itoa(n)
		if len(tasks) == 0 {
			// PY1: never leave Critic chasing template placeholders in todo.
			if phaseStr == activePhase && todoHasTemplatePlaceholders(todoStr) {
				tasks = []string{fmt.Sprintf("- [ ] Expand phase %s tasks in docs/workflow/phase%s.md", phaseStr, phaseStr)}
			} else {
				continue
			}
		}
		wroteAny = true
		section := formatChecklistSectionWithRoot(phaseStr, tasks, completedDescs, phaseStr == activePhase, projectRoot)
		body.WriteString(section)
		if phaseStr == activePhase {
			activeAllDone = checklistSectionAllDone(tasks)
		}
	}
	if !wroteAny {
		// Strip leftover placeholder checklist rows even when phase docs are empty.
		cleaned := stripTemplatePlaceholderChecklistLines(todoStr)
		return WriteFileAtomic(todoPath, []byte(cleaned), 0644)
	}
	if after != "" {
		if !strings.HasPrefix(after, "## ") {
			body.WriteByte('\n')
		}
		body.WriteString(after)
	}
	out := body.String()
	if activeAllDone {
		out = markCurrentTaskDone(out, activeN)
	}
	return WriteFileAtomic(todoPath, []byte(out), 0644)
}

// splitAroundPhaseChecklists returns content before the first phase checklist
// and content starting at the first non-phase H2 after checklists.
func splitAroundPhaseChecklists(todoStr string) (before, after string) {
	start := findAnyPhaseChecklistHeader(todoStr)
	if start < 0 {
		if idx := strings.Index(todoStr, "## Phase Checklist"); idx >= 0 {
			start = idx
		}
	}
	if start < 0 {
		// Insert before ## Completed if present.
		for _, h := range []string{"\n## Completed", "\n## Blockers", "\n## Notes"} {
			if idx := strings.Index(todoStr, h); idx >= 0 {
				return todoStr[:idx+1], todoStr[idx+1:]
			}
		}
		return strings.TrimRight(todoStr, "\n") + "\n", ""
	}
	before = todoStr[:start]
	rest := todoStr[start:]
	// Walk sections; keep consecutive "## Phase N Checklist" blocks in the
	// middle (discarded — rebuilt), stop at first other ## heading.
	offset := 0
	for {
		if offset >= len(rest) {
			return before, ""
		}
		chunk := rest[offset:]
		if !strings.HasPrefix(chunk, "## ") {
			nl := strings.Index(chunk, "\n## ")
			if nl < 0 {
				return before, ""
			}
			offset += nl + 1
			continue
		}
		lineEnd := strings.Index(chunk, "\n")
		header := chunk
		if lineEnd > 0 {
			header = chunk[:lineEnd]
		}
		if isPhaseChecklistHeader(header) {
			// Skip this section until next ##
			next := strings.Index(chunk[1:], "\n## ")
			if next < 0 {
				return before, ""
			}
			offset += next + 1
			continue
		}
		// Non-phase section — this is "after".
		return before, rest[offset:]
	}
}

func isPhaseChecklistHeader(line string) bool {
	t := strings.TrimSpace(line)
	if t == "## Phase Checklist" {
		return true
	}
	if !strings.HasPrefix(t, "## Phase ") || !strings.Contains(t, "Checklist") {
		return false
	}
	rest := strings.TrimPrefix(t, "## Phase ")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false
	}
	_, err := strconv.Atoi(fields[0])
	return err == nil
}

func replacePhaseHeaderNumber(todoStr, activePhase string) string {
	if idx := strings.Index(todoStr, "**Phase**: "); idx >= 0 {
		rest := todoStr[idx+len("**Phase**: "):]
		lineEnd := strings.IndexAny(rest, "\r\n")
		line := rest
		tail := ""
		if lineEnd >= 0 {
			line = rest[:lineEnd]
			tail = rest[lineEnd:]
		}
		fields := strings.Fields(line)
		suffix := ""
		if len(fields) >= 2 {
			suffix = " " + strings.Join(fields[1:], " ")
		}
		return todoStr[:idx] + "**Phase**: " + activePhase + suffix + tail
	}
	return todoStr
}

func findAnyPhaseChecklistHeader(todoStr string) int {
	for n := 1; n <= 12; n++ {
		for _, suffix := range []string{
			fmt.Sprintf("## Phase %d Checklist (active)", n),
			fmt.Sprintf("## Phase %d Checklist", n),
		} {
			if idx := strings.Index(todoStr, suffix); idx >= 0 {
				return idx
			}
		}
	}
	return -1
}

// extractPhaseChecklistSection returns the checklist body for a specific phase
// (including the ## header line). Falls back to the first phase checklist.
func extractPhaseChecklistSection(todoStr, phase string) string {
	if phase == "" {
		phase = parseTodoPhaseNumber(todoStr)
	}
	if phase == "" {
		phase = "1"
	}
	for _, hdr := range []string{
		fmt.Sprintf("## Phase %s Checklist (active)", phase),
		fmt.Sprintf("## Phase %s Checklist", phase),
	} {
		idx := strings.Index(todoStr, hdr)
		if idx < 0 {
			continue
		}
		after := todoStr[idx:]
		end := strings.Index(after[1:], "\n## ")
		if end > 0 {
			return after[:end+1]
		}
		return after
	}
	// Fallback: first phase checklist block.
	start := findAnyPhaseChecklistHeader(todoStr)
	if start < 0 {
		return ""
	}
	after := todoStr[start:]
	end := strings.Index(after[1:], "\n## ")
	if end > 0 {
		return after[:end+1]
	}
	return after
}

// SetTodoActivePhasePointer updates only the **Phase**: N header so Critic /
// progress counters follow the locked phase without wiping other checklists.
func SetTodoActivePhasePointer(projectRoot string, phase int) error {
	if phase < 1 {
		return nil
	}
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return err
	}
	todoStr := normalizeTodoPhaseHeader(string(content), phase, 0)
	todoStr = replacePhaseHeaderNumber(todoStr, strconv.Itoa(phase))
	// Flip (active) marker onto the target phase section headers.
	for n := 1; n <= 12; n++ {
		nStr := strconv.Itoa(n)
		activeHdr := fmt.Sprintf("## Phase %s Checklist (active)", nStr)
		plainHdr := fmt.Sprintf("## Phase %s Checklist", nStr)
		if n == phase {
			todoStr = strings.ReplaceAll(todoStr, activeHdr, plainHdr)
			todoStr = strings.Replace(todoStr, plainHdr, activeHdr, 1)
		} else {
			todoStr = strings.ReplaceAll(todoStr, activeHdr, plainHdr)
		}
	}
	return WriteFileAtomic(todoPath, []byte(todoStr), 0644)
}

// AlignCurrentTaskToPhase rewrites ## Current Task so it matches the locked
// phase (Batch6/V / S6). Prevents "Phase 1" header with "Implement Phase 3".
func AlignCurrentTaskToPhase(projectRoot string, phase int) error {
	if phase < 1 {
		return nil
	}
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(content), "\n")
	taskLine := fmt.Sprintf("- [ ] Implement Phase %d", phase)
	if title := phaseTitleHint(projectRoot, phase); title != "" {
		taskLine = fmt.Sprintf("- [ ] Implement Phase %d: %s", phase, title)
	}
	out := make([]string, 0, len(lines)+2)
	inCurrent := false
	replaced := false
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "## Current Task" {
			out = append(out, lines[i])
			inCurrent = true
			continue
		}
		if inCurrent {
			if strings.HasPrefix(trimmed, "## ") {
				if !replaced {
					out = append(out, taskLine)
					replaced = true
				}
				inCurrent = false
				out = append(out, lines[i])
				continue
			}
			if !replaced && strings.HasPrefix(trimmed, "- [") {
				out = append(out, taskLine)
				replaced = true
				for i+1 < len(lines) {
					next := strings.TrimSpace(lines[i+1])
					if strings.HasPrefix(next, "## ") {
						break
					}
					if next == "" || strings.HasPrefix(next, "- [") {
						i++
						continue
					}
					break
				}
				continue
			}
			if replaced && strings.HasPrefix(trimmed, "- [") {
				continue
			}
		}
		out = append(out, lines[i])
	}
	if inCurrent && !replaced {
		out = append(out, taskLine)
	}
	return WriteFileAtomic(todoPath, []byte(strings.Join(out, "\n")), 0644)
}

func phaseTitleHint(projectRoot string, phase int) string {
	plan, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(fmt.Sprintf(`(?i)Phase\s*%d[:\s—\-]+([^\n|]+)`, phase))
	if m := re.FindStringSubmatch(plan); len(m) > 1 {
		t := strings.TrimSpace(m[1])
		t = strings.Trim(t, "|* ")
		// Prefer phase heading title over long Goals prose.
		if idx := strings.Index(t, "."); idx > 20 && idx < 80 {
			t = strings.TrimSpace(t[:idx])
		}
		if len(t) > 80 {
			// E10: cut on rune/word boundary; avoid mid-token / open backtick.
			r := []rune(t)
			cut := 80
			if cut > len(r) {
				cut = len(r)
			}
			for cut > 40 && r[cut-1] != ' ' && r[cut-1] != ',' && r[cut-1] != ';' {
				cut--
			}
			t = strings.TrimSpace(string(r[:cut]))
			t = strings.TrimRight(t, "`\"'")
		}
		return t
	}
	return ""
}

// normalizeTodoPhaseHeader strips LLM-written "Current Phase: Phase N …" headers
// and ensures a canonical `> **Phase**: N of M` line (Batch5/N).
func normalizeTodoPhaseHeader(todoStr string, phase, phaseCount int) string {
	if phase < 1 {
		phase = 1
	}
	lines := strings.Split(todoStr, "\n")
	out := make([]string, 0, len(lines)+1)
	hasCanonical := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		// Drop LLM drift headers.
		if strings.HasPrefix(lower, "> current phase:") ||
			strings.HasPrefix(lower, "current phase:") ||
			(strings.HasPrefix(trimmed, "> ") && strings.Contains(lower, "current phase")) {
			continue
		}
		if strings.Contains(trimmed, "**Phase**:") {
			hasCanonical = true
		}
		out = append(out, line)
	}
	todoStr = strings.Join(out, "\n")
	if !hasCanonical {
		of := ""
		if phaseCount > 0 {
			of = fmt.Sprintf(" of %d", phaseCount)
		}
		insert := fmt.Sprintf("> **Phase**: %d%s", phase, of)
		// Prefer after title / Active Workspace heading.
		if idx := strings.Index(todoStr, "\n"); idx >= 0 {
			todoStr = todoStr[:idx+1] + insert + "\n" + todoStr[idx+1:]
		} else {
			todoStr = insert + "\n" + todoStr
		}
	}
	return todoStr
}

func extractPhaseGroupChecklist(phasePath string) []string {
	detail, err := os.ReadFile(phasePath)
	if err != nil {
		return nil
	}
	// H3: heal glued headings on read so SyncTodo never copies Layer## Risks.
	body := sanitizePhaseDocMarkdown(string(detail))
	if body != string(detail) {
		_ = WriteFileAtomic(phasePath, []byte(body), 0644)
	}
	var groups []phaseTaskGroup
	var cur *phaseTaskGroup
	inTasks := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") && strings.Contains(strings.ToLower(t), "task") {
			inTasks = true
			continue
		}
		if inTasks && strings.HasPrefix(t, "## ") && !strings.Contains(strings.ToLower(t), "task") {
			break
		}
		if !inTasks {
			continue
		}
		if strings.HasPrefix(t, "### ") {
			if cur != nil {
				groups = append(groups, *cur)
			}
			cur = &phaseTaskGroup{hdr: t}
			continue
		}
		if cur != nil && strings.HasPrefix(t, "- [x]") {
			cur.done++
			cur.total++
		}
		if cur != nil && strings.HasPrefix(t, "- [ ]") {
			cur.total++
		}
	}
	if cur != nil {
		groups = append(groups, *cur)
	}
	// L1: dedupe ### groups by normalized title (dirty phase docs repeat sections).
	groups = dedupeTaskGroups(groups)
	var tasks []string
	for _, g := range groups {
		label := strings.TrimPrefix(g.hdr, "### ")
		if isTemplatePlaceholderLabel(label) {
			continue
		}
		if looksLikeGluedChecklistLabel(label) || looksLikeGluedChecklistLabel(g.hdr) {
			continue
		}
		mark := "[ ]"
		if g.total > 0 && g.done == g.total {
			mark = "[x]"
		}
		cnt := ""
		if g.total > 0 {
			cnt = fmt.Sprintf(" (%d/%d)", g.done, g.total)
		}
		tasks = append(tasks, fmt.Sprintf("- %s %s%s", mark, label, cnt))
	}
	if len(tasks) == 0 {
		inCL := false
		for _, line := range strings.Split(body, "\n") {
			t := strings.TrimSpace(line)
			if isH2(t) && strings.Contains(strings.ToLower(t), "task") {
				inCL = true
				continue
			}
			if inCL && isH2(t) {
				break
			}
			if inCL && (strings.HasPrefix(t, "- [ ]") || strings.HasPrefix(t, "- [x]")) {
				if isTemplatePlaceholderLabel(t) {
					continue
				}
				if looksLikeGluedChecklistLabel(t) {
					continue
				}
				tasks = append(tasks, t)
			}
		}
	}
	return tasks
}

// looksLikeGluedChecklistLabel rejects checklist rows that still contain
// mid-line ## headings (should not count toward Critic gaps).
func looksLikeGluedChecklistLabel(line string) bool {
	return gluedHeadingRe.MatchString(line) || strings.Contains(line, "## Risks") ||
		strings.Contains(line, "## Verification") || strings.Contains(line, "## Notes")
}

type phaseTaskGroup struct {
	hdr         string
	done, total int
}

func dedupeTaskGroups(groups []phaseTaskGroup) []phaseTaskGroup {
	seen := map[string]int{}
	var out []phaseTaskGroup
	for _, g := range groups {
		key := normalizeChecklistLabel(strings.TrimPrefix(g.hdr, "### "))
		if key == "" {
			out = append(out, g)
			continue
		}
		if i, ok := seen[key]; ok {
			if g.total > out[i].total || (g.total == out[i].total && g.done > out[i].done) {
				out[i] = g
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, g)
	}
	return out
}

func checklistSectionAllDone(tasks []string) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		trimmed := strings.TrimSpace(t)
		if strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [>]") {
			return false
		}
	}
	return true
}

func todoHasTemplatePlaceholders(todoStr string) bool {
	return strings.Contains(todoStr, "[Task Group Name]") ||
		strings.Contains(todoStr, "[Specific, actionable") ||
		strings.Contains(todoStr, "[Verifiable task-level outcome]")
}

func stripTemplatePlaceholderChecklistLines(todoStr string) string {
	if !todoHasTemplatePlaceholders(todoStr) {
		return todoStr
	}
	lines := strings.Split(todoStr, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if isTemplatePlaceholderLabel(line) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// markCurrentTaskDone sets the Current Task checkbox to [x] when the active
// phase checklist is fully complete (PY1 SoT alignment).
func markCurrentTaskDone(todoStr string, activePhase int) string {
	lines := strings.Split(todoStr, "\n")
	inCurrent := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "## Current Task" {
			inCurrent = true
			continue
		}
		if inCurrent {
			if strings.HasPrefix(trimmed, "## ") {
				break
			}
			if strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [>]") {
				rest := trimmed
				if strings.HasPrefix(rest, "- [ ]") {
					rest = strings.TrimSpace(rest[len("- [ ]"):])
				} else {
					rest = strings.TrimSpace(rest[len("- [>]"):])
				}
				if activePhase > 0 && !strings.Contains(rest, fmt.Sprintf("Phase %d", activePhase)) &&
					!strings.Contains(strings.ToLower(rest), "phase") {
					// Still mark — Current Task is the active phase work item.
				}
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines[i] = indent + "- [x] " + rest
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func formatChecklistSection(phase string, tasks []string, completedDescs []string, isActive bool) string {
	return formatChecklistSectionWithRoot(phase, tasks, completedDescs, isActive, "")
}

func formatChecklistSectionWithRoot(phase string, tasks []string, completedDescs []string, isActive bool, projectRoot string) string {
	truncatedNotice := ""
	if len(tasks) > MaxChecklistItems {
		omitted := len(tasks) - MaxChecklistItems
		// Non-checkbox placeholder so CountTodoProgress ignores it (S2.3).
		truncatedNotice = fmt.Sprintf("> (+%d more items — see full plan in phase%s.md)\n", omitted, phase)
		tasks = tasks[:MaxChecklistItems]
	}
	var sb strings.Builder
	if isActive {
		sb.WriteString("## Phase " + phase + " Checklist (active)\n")
	} else {
		sb.WriteString("## Phase " + phase + " Checklist\n")
	}
	if truncatedNotice != "" {
		sb.WriteString(truncatedNotice)
	}
	for _, t := range tasks {
		line := t
		if wasCompleted(t, completedDescs) {
			if !checklistCountIncomplete(t) {
				line = strings.Replace(t, "- [ ]", "- [x]", 1)
			} else if checklistGroupHasDiskEvidence(t, projectRoot) {
				// Keep a previously completed group when disk still has the
				// sources/tests; do not resurrect [x] for (0/N) with no files.
				line = strings.Replace(t, "- [ ]", "- [x]", 1)
			}
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

// checklistCountIncomplete reports "- [ ] Label (0/4)" style rows where
// done < total — wasCompleted must not upgrade these to [x] (L1).
func checklistCountIncomplete(taskLine string) bool {
	s := strings.TrimSpace(taskLine)
	s = strings.TrimPrefix(s, "- [ ]")
	s = strings.TrimPrefix(s, "- [x]")
	s = strings.TrimPrefix(s, "- [>]")
	s = strings.TrimSpace(s)
	i := strings.LastIndex(s, " (")
	if i < 0 || !strings.HasSuffix(s, ")") {
		return false
	}
	inner := s[i+2 : len(s)-1]
	parts := strings.Split(inner, "/")
	if len(parts) != 2 {
		return false
	}
	done, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	total, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || total <= 0 {
		return false
	}
	return done < total
}

func checklistGroupHasDiskEvidence(taskLine, projectRoot string) bool {
	if strings.TrimSpace(projectRoot) == "" {
		return false
	}
	lower := strings.ToLower(taskLine)
	if checklistItemRequiresLiveTestEvidence(lower) {
		return countAllWorkflowTestSymbols(projectRoot) > 0
	}
	if checklistItemLooksLikeCoreImpl(lower) ||
		strings.Contains(lower, "scaffold") ||
		strings.Contains(lower, "constructor") ||
		strings.Contains(lower, "public api") ||
		strings.Contains(lower, "types") {
		return countSubstantiveSourceFiles(projectRoot) > 0
	}
	return false
}

// RegenerateRecordMarkdownFromSQLite rebuilds process_record.md from SQLite
// warm lessons + recent evaluation records. Export-only; runtime never reads
// this file back as SoT (S2.6 / docs/avatars.md).
func RegenerateRecordMarkdownFromSQLite(projectRoot string) error {
	store := getMemoryStore()
	recordPath := filepath.Join(projectRoot, DocPaths["record"])
	_ = os.MkdirAll(filepath.Dir(recordPath), 0755)

	var hazards, pitfalls, blockers, recent []string
	if store != nil {
		if snapshot, err := store.LoadSnapshot("workflow"); err == nil {
			for _, lesson := range snapshot.WarmLessons {
				line := strings.TrimSpace(lesson.Summary)
				if line == "" {
					continue
				}
				kind := strings.ToLower(strings.TrimSpace(lesson.Kind))
				switch {
				case strings.Contains(kind, "hazard") || strings.HasPrefix(strings.ToLower(line), "hazard:"):
					hazards = append(hazards, line)
				case strings.Contains(kind, "blocker") || strings.HasPrefix(strings.ToLower(line), "blocker:"):
					blockers = append(blockers, line)
				default:
					pitfalls = append(pitfalls, line)
				}
			}
			for i, rec := range snapshot.EvaluationRecords {
				if i >= 12 {
					break
				}
				recent = append(recent, fmt.Sprintf("[%s] %s", rec.Verdict, firstLine(rec.Summary, 160)))
			}
		}
	}

	today := time.Now().UTC().Format("2006-01-02")
	var b strings.Builder
	b.WriteString("# Process Record\n\n")
	b.WriteString("> **Export-only document.** Regenerated from SQLite (`hot-memory.db`) at end of run.\n")
	b.WriteString("> Runtime reads plan + todo + SQLite via LoadWorkflowContext — not this file.\n")
	b.WriteString("> Last export: " + today + "\n\n")
	b.WriteString("## Entry Format\n\n")
	b.WriteString("Each entry: `> Added: YYYY-MM-DD | severity: low/medium/high | source: task-id`\n\n")
	b.WriteString("## Hazards\n\n")
	b.WriteString("*Recurring failure patterns. Builder should read SQLite pitfalls before generating code.*\n\n")
	b.WriteString("<!-- hazards auto-appended below -->\n")
	writeExportBullets(&b, hazards)
	b.WriteString("\n## Pitfalls\n\n")
	b.WriteString("*Repeated errors found while executing tasks.*\n\n")
	b.WriteString("<!-- pitfalls auto-appended below -->\n")
	writeExportBullets(&b, pitfalls)
	b.WriteString("\n## Blockers\n\n")
	b.WriteString("*Current blocking items.*\n\n")
	b.WriteString("<!-- blockers auto-appended below -->\n")
	writeExportBullets(&b, blockers)
	b.WriteString("\n## Recent Outcomes\n\n")
	b.WriteString("<!-- recent evaluation_records export -->\n")
	writeExportBullets(&b, recent)
	b.WriteString("\n## Resolved\n\n")
	b.WriteString("*Resolved blockers. Entries unused for 30 days are moved here automatically.*\n\n")
	b.WriteString("<!-- resolved auto-appended below -->\n")

	return WriteFileAtomic(recordPath, []byte(b.String()), 0644)
}

func writeExportBullets(b *strings.Builder, items []string) {
	if len(items) == 0 {
		b.WriteString("\n")
		return
	}
	seen := map[string]bool{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		key := strings.ToLower(item)
		if item == "" || seen[key] {
			continue
		}
		seen[key] = true
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteByte('\n')
	}
}
