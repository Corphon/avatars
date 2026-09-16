// Package workflow provides project workflow document management:
// startup preload (auto-create core docs from embedded templates),
// and template definitions for the avatars plan→phase→todo→record pipeline.
package workflow

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	memstore "avatars/internal/memory"
)

//go:embed templates/*
var embeddedTemplates embed.FS

// memoryStore is the SQLite-backed memory store for persisting task completions
// and pitfalls. When nil (default), writes are silently skipped (test/dev mode).
// Set via SetMemoryStore by the runtime during startup.
var memoryStore *memstore.Store
var memoryStoreMu sync.RWMutex

// SetMemoryStore sets the SQLite memory store used by RecordTaskCompletion,
// ReadPitfalls, and related functions. Call once during runtime startup.
func SetMemoryStore(store *memstore.Store) {
	memoryStoreMu.Lock()
	defer memoryStoreMu.Unlock()
	memoryStore = store
}

func getMemoryStore() *memstore.Store {
	memoryStoreMu.RLock()
	defer memoryStoreMu.RUnlock()
	return memoryStore
}

// Template file names as they appear in the embedded filesystem.
const (
	templatePlan    = "templates/avatars_plan.md"
	templateTodo    = "templates/avatars_todo.md"
	templateRecord  = "templates/process_record.md"
	templatePhase   = "templates/phase_template.md"
)

// DocPaths maps logical document names to their project-relative paths.
var DocPaths = map[string]string{
	"plan":    "docs/workflow/avatars_plan.md",
	"todo":    "docs/workflow/avatars_todo.md",
	"record":  "docs/workflow/process_record.md", // unified: always .md (was .yaml)
}

// PreloadDocs ensures the three core workflow documents exist under
// projectRoot/docs/. Missing documents are created from the embedded
// English templates. Returns the count of newly created files.
//
// projectRoot is typically the directory containing the avatars/
// subdirectory — i.e., the user's project root.
func PreloadDocs(projectRoot string) (created int, err error) {
	docsDir := filepath.Join(projectRoot, "docs", "workflow")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return 0, fmt.Errorf("create workflow directory: %w", err)
	}

	files := []struct {
		templatePath string
		docPath      string
	}{
		{templatePlan, DocPaths["plan"]},
		{templateTodo, DocPaths["todo"]},
		{templateRecord, DocPaths["record"]},
		// W7: Auto-create phase1.md from template so ConstructPhase
		// has a base to populate rather than starting from scratch.
		{templatePhase, "docs/workflow/phase1.md"},
	}

	for _, f := range files {
		destPath := filepath.Join(projectRoot, f.docPath)
		if _, statErr := os.Stat(destPath); statErr == nil {
			// File already exists — skip.
			continue
		}

		template, readErr := embeddedTemplates.ReadFile(f.templatePath)
		if readErr != nil {
			return created, fmt.Errorf("read embedded template %s: %w", f.templatePath, readErr)
		}

		// Fill in basic project context from the filesystem.
		projectName := filepath.Base(projectRoot)
		today := time.Now().Format("2006-01-02")
		content := bytes.ReplaceAll(template, []byte("[brief one-line description]"), []byte(projectName))
		content = bytes.ReplaceAll(content, []byte("[YYYY-MM-DD]"), []byte(today))
		content = bytes.ReplaceAll(content, []byte("[Primary goal"), []byte("Complete "+projectName))
		// Fill YAML template fields.
		content = bytes.ReplaceAll(content, []byte(`name: ""`), []byte(`name: "`+projectName+`"`))
		content = bytes.ReplaceAll(content, []byte(`created: ""`), []byte(`created: "`+today+`"`))

		if writeErr := os.WriteFile(destPath, content, 0o644); writeErr != nil {
			return created, fmt.Errorf("write %s: %w", f.docPath, writeErr)
		}
		created++
	}

	return created, nil
}

// RecordTaskCompletion persists a completed-task entry to SQLite
// (evaluation_records table) as the single source of truth.
// When learned contains a pitfall/hazard/blocker, it also appends
// to process_record.md for cross-session LLM context.
func RecordTaskCompletion(projectRoot string, taskTitle string, summary string, extras ...interface{}) error {
		files, learned := extractTaskCompletionExtras(extras)
	if len(files) == 0 && learned == "" {
		return nil
	}

	store := getMemoryStore()
	if store == nil {
		return nil // no SQLite store configured
	}

	taskLine := firstLine(taskTitle, 120)
	summaryLine := firstLine(summary, 120)

	// P2-7: Jaccard similarity dedup — skip write if a near-duplicate exists.
	if snapshot, err := store.LoadSnapshot("workflow"); err == nil {
		for _, existing := range snapshot.EvaluationRecords {
			if jaccardSimilarity(summaryLine, existing.Summary) > 0.7 {
				return nil // near-duplicate, skip
			}
		}
	}

	details := buildTaskCompletionDetails(projectRoot, files, learned)
	verdict := taskCompletionVerdict(summary)

	if err := store.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "workflow",
		RunID:     "workflow",
		TaskID:    taskLine,
		Kind:      "task_completion",
		Verdict:   verdict,
		Cause:     "workflow_record",
		Summary:   summaryLine,
		Source:    "RecordTaskCompletion",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}

	// S2.6: Pitfalls stay in SQLite (RecordEvaluation / warm_lessons).
	// process_record.md is regenerated at end of run — do not dual-write here.
	return nil
}

// pitfallSeverity returns a severity tag based on learned content.
func pitfallSeverity(learned string) string {
	lowered := strings.ToLower(learned)
	// High: crash, panic, data loss, security.
	if strings.Contains(lowered, "panic") || strings.Contains(lowered, "crash") ||
		strings.Contains(lowered, "security") || strings.Contains(lowered, "data loss") {
		return "high"
	}
	// Medium: build failure, syntax error, timeout.
	if strings.Contains(lowered, "error") || strings.Contains(lowered, "fail") ||
		strings.Contains(lowered, "timeout") || strings.Contains(lowered, "broken") {
		return "medium"
	}
	return "low"
}

func extractTaskCompletionExtras(extras []interface{}) (files []string, learned string) {
	for _, e := range extras {
		switch v := e.(type) {
		case []string: files = v
		case string: learned = v
		}
	}
	return
}

func buildTaskCompletionDetails(projectRoot string, files []string, learned string) []string {
	var details []string
	if len(files) > 0 {
		relFiles := make([]string, 0, len(files))
		for _, f := range files {
			rel := f
			if filepath.IsAbs(f) {
				if r, err := filepath.Rel(projectRoot, f); err == nil {
					rel = r
				}
			}
			relFiles = append(relFiles, filepath.ToSlash(rel))
		}
		details = append(details, "changed_files: "+strings.Join(relFiles, ", "))
	}
	if learned != "" {
		details = append(details, "learned: "+firstLine(learned, 200))
	}
	return details
}

// extractResultFromSummary derives a short result label from the run summary.
// Verification results take priority — "phase" keyword may appear in any summary.
func extractResultFromSummary(summary string) string {
	lower := strings.ToLower(summary)
	// Failures first (N1-10: timeout must not become PASS via default "OK").
	if strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") {
		return "FAIL: timeout"
	}
	if strings.Contains(lower, "run status: failed") || strings.Contains(lower, "run stopped") {
		return "FAIL: run stopped"
	}
	if strings.Contains(lower, "needs_remediation") {
		return "FAIL: needs remediation"
	}
	// Verification results (highest priority — definitive evidence).
	if strings.Contains(lower, "verification finished with pass") || strings.Contains(lower, "0 failed") {
		return "PASS: verification"
	}
	if strings.Contains(lower, "verification finished with fail") {
		if idx := strings.Index(lower, "[fail]"); idx >= 0 {
			return "FAIL: " + firstLine(summary[idx:], 80)
		}
		return "FAIL: verification"
	}
	// Meta entries (lowest priority — only if nothing else matches).
	if strings.Contains(lower, "plan construction") || strings.Contains(lower, "plan confirmed") {
		return "META: workflow planning"
	}
	// Completed normally.
	if strings.Contains(lower, "completed") || strings.Contains(lower, "synthesis status: completed") {
		return "OK: completed"
	}
	return "PARTIAL: inconclusive"
}

func taskCompletionVerdict(summary string) string {
	result := extractResultFromSummary(summary)
	upper := strings.ToUpper(result)
	if strings.HasPrefix(upper, "FAIL") {
		return "FAIL"
	}
	if strings.HasPrefix(upper, "PASS") || strings.HasPrefix(upper, "OK") {
		return "PASS"
	}
	if strings.HasPrefix(upper, "META") {
		return "PARTIAL"
	}
	return "PARTIAL"
}

// extractRecentWhats extracts the 'what' field values from the last N entries
// in the process_record history/completed section. Used for dedup.
func extractRecentWhats(content string, n int) []string {
	// Find the entries section — look for history: or completed: marker.
	var entriesSection string
	if idx := strings.LastIndex(content, "history:"); idx >= 0 {
		entriesSection = content[idx:]
	} else if idx := strings.LastIndex(content, "completed:"); idx >= 0 {
		entriesSection = content[idx:]
	} else {
		return nil
	}

	// Split by date entries (each starts with "  - date:").
	entries := strings.Split(entriesSection, "\n  - date:")
	// First element is the header, skip it. Process last N entries.
	var whats []string
	start := len(entries) - n
	if start < 1 {
		start = 1
	}
	for i := start; i < len(entries); i++ {
		entry := entries[i]
		// Extract the 'what' field value.
		if idx := strings.Index(entry, "what: \""); idx >= 0 {
			rest := entry[idx+len("what: \""):]
			if endIdx := strings.Index(rest, "\""); endIdx >= 0 {
				whats = append(whats, rest[:endIdx])
			}
		} else if idx := strings.Index(entry, "what: "); idx >= 0 {
			// Handle unquoted what values (old format fallback).
			rest := entry[idx+len("what: "):]
			if endIdx := strings.IndexAny(rest, "\n\r"); endIdx >= 0 {
				whats = append(whats, strings.TrimSpace(rest[:endIdx]))
			} else {
				whats = append(whats, strings.TrimSpace(rest))
			}
		}
	}
	return whats
}

func firstLine(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\n\r"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if maxLen <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-3]) + "..."
}

// Template returns the content of the named embedded template.
// Valid names: "plan", "todo", "record".
func Template(name string) (string, error) {
	var path string
	switch name {
	case "plan":
		path = templatePlan
	case "todo":
		path = templateTodo
	case "record":
		path = templateRecord
	case "phase":
		path = templatePhase
	default:
		return "", fmt.Errorf("unknown template: %s (valid: plan, todo, record, phase)", name)
	}
	b, err := embeddedTemplates.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// isActionablePitfallKind reports whether a warm_lesson kind should surface as
// a pitfall. Writers historically used verification/tool_runtime/workflow/etc.;
// ReadPitfalls used to only accept pitfall|hazard and silently dropped them (S3.3).
func isActionablePitfallKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "pitfall", "hazard", "blocker", "hazards", "pitfalls", "blockers",
		"verification", "tool_runtime", "workflow", "workflow_feedback",
		"outcome", "phase_transition":
		return true
	default:
		// Intent classification noise stays out of planner pitfall lists.
		return false
	}
}

// isActionableMemoryKind is a slightly wider filter for project_lessons that
// may carry pattern names (workflow_pattern, runtime_followup, …).
func isActionableMemoryKind(kind string) bool {
	k := strings.ToLower(strings.TrimSpace(kind))
	if isActionablePitfallKind(k) {
		return true
	}
	return strings.Contains(k, "workflow") || strings.Contains(k, "runtime") ||
		strings.Contains(k, "verification") || strings.Contains(k, "followup") ||
		strings.Contains(k, "pattern")
}

// ReadPitfalls returns pitfalls from SQLite warm_lessons (+ project lessons)
// as a formatted string for Planner injection. Returns "" if none found.
func ReadPitfalls(projectRoot string) string {
	_ = projectRoot
	store := getMemoryStore()
	if store == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Known pitfalls from previous tasks (avoid these):\n")
	count := 0

	if projectLessons, err := store.LoadProjectLessons(); err == nil {
		for _, lesson := range projectLessons {
			if !isActionablePitfallKind(lesson.Kind) && !isActionableMemoryKind(lesson.Kind) {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(lesson.Summary)
			sb.WriteString("\n")
			count++
		}
	}

	snapshot, err := store.LoadSnapshot("workflow")
	if err == nil {
		for _, lesson := range snapshot.WarmLessons {
			if !isActionablePitfallKind(lesson.Kind) {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(lesson.Summary)
			sb.WriteString("\n")
			count++
		}
		// Also include relevant evaluation record summaries.
		for _, record := range snapshot.EvaluationRecords {
			if record.Verdict == "FAIL" || record.Verdict == "PARTIAL" {
				sb.WriteString("- ")
				sb.WriteString(record.Summary)
				sb.WriteString("\n")
				count++
			}
		}
	}
	if count == 0 {
		return ""
	}
	return sb.String()
}

// SyncTodoFromPlan lives in sync_todo.go (S2.3 phase-aware rebuild).

func allTasksCompleted(todoStr string) bool {
	idx := strings.Index(todoStr, "## Phase")
	if idx < 0 {
		return false
	}
	section := todoStr[idx:]
	end := strings.Index(section[1:], "\n## ")
	if end > 0 {
		section = section[:end+1]
	}
	total := 0
	completed := 0
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [x]") {
			completed++
			total++
		} else if strings.HasPrefix(trimmed, "- [ ]") {
			total++
		}
	}
	return total > 0 && completed == total
}

func hasNextPhase(planStr, current string) bool {
	next := fmt.Sprintf("### Phase %s:", advancePhaseNumber(current))
	return strings.Contains(planStr, next)
}

func advancePhaseNumber(current string) string {
	n := 1
	fmt.Sscanf(current, "%d", &n)
	return fmt.Sprintf("%d", n+1)
}

func updatePlanPhase(planStr, oldPhase, newPhase string) string {
	planStr = strings.Replace(planStr, "**Active Phase**: "+oldPhase, "**Active Phase**: "+newPhase, 1)
	oldMarker := "### Phase " + oldPhase + ":"
	oldIdx := strings.Index(planStr, oldMarker)
	if oldIdx >= 0 {
		oldSection := planStr[oldIdx:]
		oldSection = strings.Replace(oldSection, "**Status**: pending", "**Status**: completed", 1)
		oldSection = strings.Replace(oldSection, "**Status**: in-progress", "**Status**: completed", 1)
		planStr = planStr[:oldIdx] + oldSection
	}
	newMarker := "### Phase " + newPhase + ":"
	newIdx := strings.Index(planStr, newMarker)
	if newIdx >= 0 {
		newSection := planStr[newIdx:]
		newSection = strings.Replace(newSection, "**Status**: pending", "**Status**: in-progress", 1)
		planStr = planStr[:newIdx] + newSection
	}
	return planStr
}

func clearCompletedTasks(todoStr string) string {
	checklistIdx := strings.Index(todoStr, "## Phase")
	completedIdx := strings.Index(todoStr, "## Completed")
	blockersIdx := strings.Index(todoStr, "## Blockers")
	if checklistIdx >= 0 && completedIdx >= 0 && blockersIdx > completedIdx {
		header := todoStr[:checklistIdx]
		blockers := todoStr[blockersIdx:]
		return header + "## Phase Checklist\n<!-- Tasks loaded from phase detail file. -->\n\n## Completed\n<!-- Tasks from this phase that are finished. -->\n\n" + blockers
	}
	return todoStr
}

// appendToRecord writes a phase-transition event to warm_lessons in SQLite.
// process_record.md is regenerated from SQLite at end of run (export only).
func appendToRecord(recordPath, message string) error {
	store := getMemoryStore()
	if store == nil {
		return nil
	}
	return store.RecordWarmLesson(memstore.WarmLessonRecord{
		SessionID:  "workflow",
		TaskID:     recordPath, // carries phase context
		Kind:       "phase_transition",
		Summary:    message,
		Source:     "SyncTodoFromPlan",
		Confidence: "medium",
		UpdatedAt:  time.Now().UTC(),
	})
}

func isH2(line string) bool {
	return (strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ")) || line == "##"
}

// WriteFileAtomic writes data to path using temp+rename for atomicity.
// P1-6a: Prevents half-written files on crash/SIGINT.
// jaccardSimilarity computes token-level Jaccard similarity between two strings.
// Used for semantic dedup of task completion records. P2-7.
func jaccardSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	tokenize := func(s string) map[string]struct{} {
		tokens := make(map[string]struct{})
		for _, t := range strings.Fields(strings.ToLower(s)) {
			if len(t) > 2 {
				tokens[t] = struct{}{}
			}
		}
		return tokens
	}
	ta := tokenize(a)
	tb := tokenize(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	intersection := 0
	for t := range ta {
		if _, ok := tb[t]; ok {
			intersection++
		}
	}
	union := len(ta) + len(tb) - intersection
	return float64(intersection) / float64(union)
}

// WriteFileAtomic writes data to path using temp+rename for atomicity.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return fmt.Errorf("write temp %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp for %s: %w", filepath.Base(path), err)
	}
	return nil
}

