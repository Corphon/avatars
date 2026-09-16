package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"avatars/internal/workflow"
)

// FileChangeStat is one file's +/- line delta for a run footer (Cursor-style).
type FileChangeStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// RunFooter is the end-of-run change summary + next-step suggestions.
type RunFooter struct {
	Files          []FileChangeStat `json:"files"`
	TotalAdded     int              `json:"total_added"`
	TotalDeleted   int              `json:"total_deleted"`
	NextSteps      []string         `json:"next_steps"`
	StatsAvailable bool             `json:"stats_available"`
}

// NextStepInput drives language-agnostic next-step suggestions.
type NextStepInput struct {
	Status              string
	Summary             string
	BuildOK             bool
	BuildOKKnown        bool
	PhaseAdvanceBlocked string
	ClarifyPending      bool
	FilesChanged        int
}

// DedupeChangedFiles returns unique slash-normalized relative paths (stable order).
func DedupeChangedFiles(paths []string) []string {
	return DedupeChangedFilesUnder("", paths)
}

// DedupeChangedFilesUnder relativizes abs paths under projectRoot then dedupes (R7-7).
func DedupeChangedFilesUnder(projectRoot string, paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = normalizeChangedPath(projectRoot, p)
		if p == "" || p == "." || seen[p] {
			continue
		}
		if isHarnessNoisePath(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func normalizeChangedPath(projectRoot, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	slash := filepath.ToSlash(path)
	// P8-7: drop obvious escapes before relativizing.
	if slash == ".." || strings.HasPrefix(slash, "../") || strings.Contains(slash, "/../") {
		return ""
	}
	if projectRoot != "" {
		root := filepath.Clean(projectRoot)
		if filepath.IsAbs(path) {
			if rel, err := filepath.Rel(root, path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
				path = rel
			} else {
				// Absolute path outside project — omit from footer.
				return ""
			}
		} else {
			// Relative: ensure joining under root does not escape.
			joined := filepath.Join(root, path)
			if rel, err := filepath.Rel(root, joined); err != nil || rel == ".." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
				return ""
			}
			path = filepath.Clean(path)
		}
	}
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "./")
	// Drop unix-root-looking noise like "/app/main.py" that survived as abs-outside.
	if strings.HasPrefix(path, "/") {
		return ""
	}
	return path
}

// ComputeFileChangeStats builds per-file +/- stats for changed paths.
// Prefer git diff --numstat vs HEAD; new/untracked files use full line count as additions.
// Language-agnostic: any text file under the project.
func ComputeFileChangeStats(projectRoot string, changedFiles []string) RunFooter {
	paths := reconcileChangedPathsToDisk(projectRoot, DedupeChangedFilesUnder(projectRoot, changedFiles))
	footer := RunFooter{Files: make([]FileChangeStat, 0, len(paths)), StatsAvailable: true}
	if len(paths) == 0 {
		return footer
	}
	numstat := gitDiffNumstat(projectRoot, paths)
	for _, p := range paths {
		abs := p
		if projectRoot != "" && !filepath.IsAbs(p) {
			abs = filepath.Join(projectRoot, filepath.FromSlash(p))
		}
		_, statErr := os.Stat(abs)
		delta, inGit := numstat[p]
		// F70: drop ghost burial paths that were written then remapped
		// (internal/<mod>/… after unbury). Keep real deletions (F89).
		if statErr != nil && !inGit {
			if looksLikeRemappedGhostPath(projectRoot, p) {
				continue
			}
			st := FileChangeStat{Path: p, Deleted: 1}
			footer.Files = append(footer.Files, st)
			footer.TotalDeleted++
			continue
		}
		st := FileChangeStat{Path: p}
		if inGit {
			st.Added, st.Deleted = delta[0], delta[1]
		} else if n := countFileLines(abs); n > 0 {
			st.Added = n
		}
		footer.Files = append(footer.Files, st)
		footer.TotalAdded += st.Added
		footer.TotalDeleted += st.Deleted
	}
	return footer
}

// reconcileChangedPathsToDisk maps recorded write paths onto files that still
// exist (F79). After layout remap (pkg/pkg.ext → pkg.ext, internal/<mod>/ →
// root) the old path is gone; keep the survivor instead of dropping everything
// except go.mod.
func reconcileChangedPathsToDisk(wd string, paths []string) []string {
	wd = nonEmptyWD(wd)
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || p == "." {
			continue
		}
		resolved := resolveChangedPathToDisk(wd, p)
		if resolved == "" {
			if isHarnessNoisePath(p) || looksLikeRemappedGhostPath(wd, p) {
				continue
			}
			// F89: keep real deletions (junk purge) so the footer can show them.
			resolved = p
		}
		if resolved == "" || seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	return out
}

func resolveChangedPathToDisk(wd, path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	if fileExistsAt(wd, path) {
		return path
	}
	collapsed := collapseDuplicatePathSegmentsAt(wd, path)
	if collapsed != path && fileExistsAt(wd, collapsed) {
		return collapsed
	}
	if alt := suggestUnburiedAlt(wd, path); alt != "" {
		return alt
	}
	base := filepath.Base(path)
	if base != path && fileExistsAt(wd, base) {
		return base
	}
	return ""
}

func fileExistsAt(wd, rel string) bool {
	_, err := os.Stat(filepath.Join(nonEmptyWD(wd), filepath.FromSlash(rel)))
	return err == nil
}

func looksLikeRemappedGhostPath(wd, path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if suggestUnburiedAlt(wd, path) != "" {
		return true
	}
	collapsed := collapseDuplicatePathSegmentsAt(wd, path)
	if collapsed != path && fileExistsAt(wd, collapsed) {
		return true
	}
	base := filepath.Base(path)
	if base != path && fileExistsAt(wd, base) {
		return true
	}
	// F70: sibling tests in a buried module dir are ghosts once the public
	// module file exists at repo root (ratebucket.go vs internal/ratebucket/*).
	parts := strings.Split(path, "/")
	if len(parts) >= 3 {
		switch strings.ToLower(parts[0]) {
		case "internal", "pkg", "lib", "private":
			mod := parts[1]
			for _, ext := range []string{".go", ".py", ".js", ".ts", ".rs", ".mjs"} {
				if fileExistsAt(wd, mod+ext) {
					return true
				}
			}
		}
	}
	return false
}

func gitDiffNumstat(projectRoot string, paths []string) map[string][2]int {
	out := map[string][2]int{}
	if len(paths) == 0 {
		return out
	}
	args := []string{"-C", projectRoot, "diff", "--numstat", "HEAD", "--"}
	args = append(args, paths...)
	cmd := exec.Command("git", args...)
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Binary diffs show "-" for counts.
		if fields[0] == "-" || fields[1] == "-" {
			rel := filepath.ToSlash(fields[len(fields)-1])
			out[rel] = [2]int{0, 0}
			continue
		}
		add, _ := strconv.Atoi(fields[0])
		del, _ := strconv.Atoi(fields[1])
		rel := filepath.ToSlash(fields[len(fields)-1])
		out[rel] = [2]int{add, del}
	}
	return out
}

func countFileLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	if len(data) == 0 {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// SuggestNextSteps returns short actionable next steps (max 5).
func SuggestNextSteps(projectRoot string, in NextStepInput) []string {
	var steps []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, existing := range steps {
			if existing == s {
				return
			}
		}
		if len(steps) < 5 {
			steps = append(steps, s)
		}
	}

	status := strings.ToLower(strings.TrimSpace(in.Status))
	summaryLower := strings.ToLower(in.Summary)
	blocked := strings.TrimSpace(in.PhaseAdvanceBlocked)

	// F4: template / construct failures must not suggest "advance to Phase 2".
	planStillTemplate := false
	if workflow.IsPlanEmpty(projectRoot) ||
		strings.Contains(summaryLower, "template placeholders") ||
		strings.Contains(summaryLower, "construct_plan") ||
		strings.Contains(summaryLower, "plan construction failed") {
		planStillTemplate = true
		add("Fill docs/workflow/avatars_plan.md (or re-run so ConstructPlan succeeds), then confirm the plan — do not advance phases yet.")
	}

	// F11: confirm quality-gate failures are not compile failures (any provider).
	confirmFailed := blocked == "confirm_failed" ||
		strings.Contains(summaryLower, "failed: confirm_plan") ||
		strings.Contains(summaryLower, "confirm plan failed") ||
		strings.Contains(summaryLower, "thin tasks rejected") ||
		strings.Contains(summaryLower, "tasks do not cover scope")
	if confirmFailed && !planStillTemplate {
		add("Expand Active Phase ## Tasks so they cover every Scope & Success Criteria item, then re-run confirm — do not chase compile/test errors yet.")
	}

	if in.ClarifyPending || strings.Contains(summaryLower, "needs_clarification") || status == "awaiting_approval" {
		add("Answer the pending clarification / approval prompts, then re-run the same task.")
	}
	if status == "needs_remediation" || strings.Contains(summaryLower, "needs_remediation") {
		add("Fix the remediation findings (build/test/verification), then re-run.")
	}
	// Skip build-failed guidance when the real blocker is confirm/construct quality.
	if !confirmFailed && !planStillTemplate && ((in.BuildOKKnown && !in.BuildOK) || blocked == "build_failed") {
		add("Repair compile/test failures until the project health check is green, then re-run.")
	}
	if blocked == "phase_docs_incomplete" && !planStillTemplate && !confirmFailed {
		add("Complete or expand the active phase detail (phaseN.md), then continue.")
	}
	if blocked == "checklist_incomplete" {
		add("Finish the remaining Active Checklist items (or fix evidence gaps), then re-run to advance.")
	}
	if blocked == "needs_remediation" {
		add("Fix build/test/verification failures from this run, then re-run — do not expect phase advance while remediation is open.")
	}

	planContent, planErr := workflow.ReadWorkflowDoc(projectRoot, "plan")
	if planErr == nil && !planStillTemplate && !confirmFailed {
		meta := workflow.ParsePlanMeta(planContent)
		phase := meta.ActivePhase
		if todoContent, todoErr := workflow.ReadWorkflowDoc(projectRoot, "todo"); todoErr == nil {
			if n := workflow.ParseTodoActivePhase(todoContent); n > 0 {
				phase = n
			}
		}
		done, total := workflow.CountTodoProgress(projectRoot)
		remaining := total - done
		failedOrHold := status == "failed" || status == "needs_remediation" ||
			blocked == "needs_remediation" || blocked == "build_failed" ||
			blocked == "confirm_failed" || blocked == "construct_failed" ||
			strings.Contains(summaryLower, "go mod tidy failed") ||
			strings.Contains(summaryLower, "quality gate")
		if remaining > 0 && !failedOrHold {
			if strings.EqualFold(strings.TrimSpace(meta.Status), "completed") ||
				(phase > 0 && phase >= meta.PhaseCount && in.BuildOKKnown && in.BuildOK) {
				add("All phases complete — review the diff, run final verification, and commit when ready.")
			} else {
				add(fmt.Sprintf("Continue Phase %d: finish the remaining %d checklist item(s).", phase, remaining))
			}
		} else if remaining > 0 && failedOrHold {
			// F11′: failed/remediation runs must not also nag "continue checklist".
		} else if phase > 0 && phase < meta.PhaseCount {
			if blocked == "user_phase_lock" {
				add(fmt.Sprintf("Phase %d checklist is complete but locked — re-run without phase lock (or say \"enter Phase %d\") to advance.", phase, phase+1))
			} else if blocked == "build_failed" || blocked == "build_or_phase_docs" || blocked == "phase_docs_incomplete" ||
				blocked == "confirm_failed" || blocked == "construct_failed" || blocked == "needs_remediation" ||
				status == "needs_remediation" {
				// A6/F11/F14: do not suggest Phase N+1 while delivery/docs/confirm/remediation are red.
			} else if blocked != "" {
				add(fmt.Sprintf("Clear the phase-advance block (%s), then continue to Phase %d.", blocked, phase+1))
			} else {
				add(fmt.Sprintf("Re-run to advance into Phase %d (or say \"continue / enter Phase %d\").", phase+1, phase+1))
			}
		} else if phase >= meta.PhaseCount {
			// F66: never claim "All phases complete" when the run failed / build is red.
			failedish := status == "failed" || status == "needs_remediation" ||
				(in.BuildOKKnown && !in.BuildOK) ||
				blocked == "build_failed" || blocked == "delivery_incomplete" ||
				strings.Contains(summaryLower, "build failed") ||
				strings.Contains(summaryLower, "post_build_failed")
			if !failedish {
				add("All phases complete — review the diff, run final verification, and commit when ready.")
			}
		}
	}

	if in.FilesChanged > 0 && (status == "" || status == "completed" || status == "completed_unverified") {
		add("Review the file change list above, then commit or continue the next phase.")
	}
	if len(steps) == 0 {
		add("Re-run with a more specific next goal, or inspect the transcript for details.")
	}
	return steps
}

// changedFilesAreDocsOnly reports whether every changed path is workflow/docs markdown.
func changedFilesAreDocsOnly(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		p = filepath.ToSlash(strings.ToLower(strings.TrimSpace(p)))
		if strings.HasPrefix(p, "docs/workflow/") ||
			strings.HasSuffix(p, "avatars_plan.md") ||
			strings.HasSuffix(p, "avatars_todo.md") ||
			strings.HasSuffix(p, "process_record.md") ||
			(strings.Contains(p, "/phase") && strings.HasSuffix(p, ".md")) {
			continue
		}
		return false
	}
	return true
}

// BuildRunFooter combines file stats + next steps for CLI / events / reports.
func BuildRunFooter(projectRoot string, changedFiles []string, in NextStepInput) RunFooter {
	footer := ComputeFileChangeStats(projectRoot, changedFiles)
	in.FilesChanged = len(footer.Files)
	footer.NextSteps = SuggestNextSteps(projectRoot, in)
	// R7-5: be honest when the run only touched workflow docs / checkmarks.
	paths := make([]string, 0, len(footer.Files))
	for _, f := range footer.Files {
		paths = append(paths, f.Path)
	}
	if changedFilesAreDocsOnly(paths) {
		docNote := "This run only updated workflow docs / checkmarks — source files were unchanged."
		already := false
		for _, s := range footer.NextSteps {
			if strings.Contains(s, "only updated workflow docs") {
				already = true
				break
			}
		}
		if !already {
			footer.NextSteps = append([]string{docNote}, footer.NextSteps...)
		}
	}
	return footer
}

// Headline returns e.g. "3 files changed, +42 -7".
func (f RunFooter) Headline() string {
	n := len(f.Files)
	switch n {
	case 0:
		return "0 files changed"
	case 1:
		return fmt.Sprintf("1 file changed, +%d -%d", f.TotalAdded, f.TotalDeleted)
	default:
		return fmt.Sprintf("%d files changed, +%d -%d", n, f.TotalAdded, f.TotalDeleted)
	}
}

// FormatCLI renders the Cursor-style footer for stdout.
func (f RunFooter) FormatCLI() string {
	var b strings.Builder
	b.WriteString("\n---\n")
	b.WriteString(f.Headline())
	b.WriteByte('\n')
	for _, file := range f.Files {
		b.WriteString(fmt.Sprintf("  %s  +%d -%d\n", file.Path, file.Added, file.Deleted))
	}
	if len(f.NextSteps) > 0 {
		b.WriteString("\nNext steps:\n")
		for i, step := range f.NextSteps {
			b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, step))
		}
	}
	b.WriteString("---")
	return b.String()
}

// FormatMarkdown appends/returns markdown sections for analysis reports.
func (f RunFooter) FormatMarkdown() string {
	var b strings.Builder
	b.WriteString("## Files Changed\n\n")
	b.WriteString(f.Headline())
	b.WriteString("\n\n")
	if len(f.Files) == 0 {
		b.WriteString("_No source files were written in this run._\n")
	} else {
		b.WriteString("| File | + | - |\n|---|---:|---:|\n")
		for _, file := range f.Files {
			b.WriteString(fmt.Sprintf("| `%s` | %d | %d |\n", file.Path, file.Added, file.Deleted))
		}
	}
	b.WriteString("\n## Next Steps\n\n")
	if len(f.NextSteps) == 0 {
		b.WriteString("- _(none)_\n")
	} else {
		for _, step := range f.NextSteps {
			b.WriteString("- " + step + "\n")
		}
	}
	return b.String()
}

// EventPayload is embedded into run.completed / run.change_summary events.
func (f RunFooter) EventPayload() map[string]any {
	files := make([]map[string]any, 0, len(f.Files))
	for _, file := range f.Files {
		files = append(files, map[string]any{
			"path":    file.Path,
			"added":   file.Added,
			"deleted": file.Deleted,
		})
	}
	return map[string]any{
		"files_changed_count":   len(f.Files),
		"files_changed":         files,
		"lines_added":           f.TotalAdded,
		"lines_deleted":         f.TotalDeleted,
		"files_changed_summary": f.Headline(),
		"next_steps":            append([]string(nil), f.NextSteps...),
	}
}

// AppendRunFooterMarkdown appends footer sections to an analysis report body.
func AppendRunFooterMarkdown(report string, footer RunFooter) string {
	section := footer.FormatMarkdown()
	trimmed := strings.TrimRight(report, "\n")
	if trimmed == "" {
		return "# Run Summary\n\n" + section + "\n"
	}
	return trimmed + "\n\n" + section + "\n"
}

// ReplaceRunFooterMarkdown replaces any existing Files Changed / Next Steps
// appendix with an updated footer (used after defer phase-advance refresh).
func ReplaceRunFooterMarkdown(report string, footer RunFooter) string {
	trimmed := strings.TrimRight(report, "\n")
	if idx := strings.Index(trimmed, "\n## Files Changed\n"); idx >= 0 {
		trimmed = trimmed[:idx]
	} else if strings.HasPrefix(trimmed, "## Files Changed\n") {
		trimmed = ""
	}
	return AppendRunFooterMarkdown(trimmed, footer)
}

func (e *Engine) changedFilesSnapshot() []string {
	if e == nil || e.changedFiles == nil {
		return nil
	}
	return append([]string(nil), e.changedFiles...)
}

// noteChangedFile appends a successfully written path (tool-loop or Builder finalize).
func (e *Engine) noteChangedFile(path string) {
	if e == nil {
		return
	}
	root := e.projectRoot
	if root == "" {
		root = "."
	}
	path = normalizeChangedPath(root, path)
	if path == "" {
		return
	}
	if isHarnessNoisePath(path) {
		return
	}
	if e.health != nil {
		e.health.Reset()
	}
	e.emitMu.Lock()
	defer e.emitMu.Unlock()
	for _, existing := range e.changedFiles {
		if existing == path {
			return
		}
	}
	e.changedFiles = append(e.changedFiles, path)
	// F106: lift may leave empty private-dir shells; drop them immediately
	// (cross-lang: internal/, _internal/, src/internal/, private/).
	_ = purgeEmptyBurialDirs(root)
}

func (e *Engine) reconcileChangedFilesToDisk(wd string) {
	if e == nil {
		return
	}
	e.emitMu.Lock()
	defer e.emitMu.Unlock()
	e.changedFiles = reconcileChangedPathsToDisk(wd, e.changedFiles)
}

func (e *Engine) buildRunFooter(projectRoot string, in NextStepInput) RunFooter {
	if projectRoot == "" {
		projectRoot = "."
	}
	e.reconcileChangedFilesToDisk(projectRoot)
	paths := e.changedFilesSnapshot()
	// R5-1: mtime fallback only when the hook recorded nothing — avoids
	// flooding footers with unrelated tree churn during short analysis runs.
	if len(paths) == 0 && !e.runStartedAt.IsZero() {
		paths = append(paths, FilesTouchedSince(projectRoot, e.runStartedAt.Add(-2*time.Second))...)
	}
	return BuildRunFooter(projectRoot, paths, in)
}

// FilesTouchedSince returns project-relative paths modified at/after since.
// Skips .git / .avatars; language-agnostic for any on-disk delivery.
func FilesTouchedSince(projectRoot string, since time.Time) []string {
	if projectRoot == "" {
		projectRoot = "."
	}
	var out []string
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".avatars" || name == "node_modules" || name == "vendor" || name == "target" {
				return filepath.SkipDir
			}
			// Workflow scaffolding is tracked separately; footer cares about delivery files.
			if name == "docs" {
				// Still walk docs/ but skip docs/workflow below via path check.
			}
			return nil
		}
		rel, relErr := filepath.Rel(projectRoot, path)
		if relErr != nil {
			rel = path
		}
		relSlash := filepath.ToSlash(rel)
		if strings.HasPrefix(relSlash, "docs/workflow/") {
			return nil
		}
		if isHarnessNoisePath(relSlash) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.ModTime().Before(since) {
			return nil
		}
		out = append(out, relSlash)
		return nil
	})
	return out
}

// mergeFooterIntoPayload builds run.completed payload with file stats + next steps.
func (e *Engine) mergeFooterIntoPayload(projectRoot, status, summary string, result *RunResult) map[string]any {
	payload := map[string]any{
		"status":  status,
		"summary": summary,
	}
	in := NextStepInput{
		Status:  status,
		Summary: summary,
	}
	if result != nil {
		in.BuildOK = result.BuildOK
		in.BuildOKKnown = result.BuildOKKnown
		in.PhaseAdvanceBlocked = result.PhaseAdvanceBlocked
		in.ClarifyPending = result.ClarifyPending != nil
	}
	footer := e.buildRunFooter(projectRoot, in)
	if result != nil {
		result.Footer = footer
	}
	for k, v := range footer.EventPayload() {
		payload[k] = v
	}
	return payload
}
