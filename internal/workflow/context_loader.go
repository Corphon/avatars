package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IsPlanEmpty returns true if the plan file doesn't exist or contains only
// the unfilled template (no user-defined phases, no real goals content).
func IsPlanEmpty(projectRoot string) bool {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return true
	}
	text := string(content)
	// Check for raw template placeholders (before fillPlanDefaults).
	if strings.Contains(text, "[brief one-line description]") {
		return true
	}
	if strings.Contains(text, "[Primary goal") {
		return true
	}
	// Post-fillPlanDefaults: plan has project name but still template structure.
	// Check: does it have "[Name]" placeholder for phase name? (never filled automatically)
	if strings.Contains(text, "[Name]") {
		return true
	}
	// Check: does "Success Criteria" have "[Measurable" placeholder?
	if strings.Contains(text, "[Measurable") {
		return true
	}
	// Check for at least one non-template phase goal.
	if strings.Contains(text, "[One-line goal]") {
		return true
	}
	return false
}

// LoadWorkflowContext reads plan + todo from files, and pulls project memory
// (pitfalls, task history) from SQLite. SQLite is the single source of truth
// for all memory; process_record.md is a git-friendly export regenerated from
// SQLite at the end of each run via RegenerateRecordMarkdownFromSQLite.
//
// Returns empty string if no docs exist (project not yet initialized).
func LoadWorkflowContext(projectRoot string) string {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])

	var sections []string

	if planContent, err := os.ReadFile(planPath); err == nil {
		planStr := strings.TrimSpace(string(planContent))
		if planStr != "" {
			sections = append(sections, "## Project Workflow Plan (avatars_plan.md)\n"+planStr)
		}
	}

	if todoContent, err := os.ReadFile(todoPath); err == nil {
		todoStr := strings.TrimSpace(string(todoContent))
		if todoStr != "" {
			sections = append(sections, "## Active Workspace (avatars_todo.md)\n"+todoStr)
		}
	}

	// NL smoke Batch1/G: surface missing phase docs as a process gate.
	if missing, err := MissingPhaseNumbers(projectRoot); err == nil && len(missing) > 0 {
		sections = append(sections, fmt.Sprintf(
			"## Phase Docs Process Tag\nStatus: incomplete\nMissing phase files: %v\nAction: fill docs/workflow/phaseN.md before advancing phases.",
			missing,
		))
	}

	// P0-2: Project memory from SQLite instead of process_record file.
	if memCtx := LoadMemoryContext(); memCtx != "" {
		sections = append(sections, memCtx)
	}

	if len(sections) == 0 {
		return ""
	}

	return "### Project Workflow State (always loaded)\n" +
		"Below is the persistent project workflow state. Use it to understand project " +
		"progress, current phase, completed tasks, known pitfalls, and what to work on next.\n\n" +
		strings.Join(sections, "\n\n---\n\n")
}

// LoadPlanSummary reads just the plan's Goals and active Phase section
// for lightweight context injection.
func LoadPlanSummary(projectRoot string) string {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	content, err := os.ReadFile(planPath)
	if err != nil {
		return ""
	}
	planStr := string(content)

	var summary strings.Builder
	summary.WriteString("Project Plan Snapshot:\n")

	if goalsStart := strings.Index(planStr, "## Goals"); goalsStart >= 0 {
		goalsSection := planStr[goalsStart:]
		if nextHeader := strings.Index(goalsSection[1:], "\n## "); nextHeader > 0 {
			goalsSection = goalsSection[:nextHeader+1]
		}
		summary.WriteString(strings.TrimSpace(goalsSection))
	}

	if phaseIdx := strings.Index(planStr, "**Active Phase**:"); phaseIdx >= 0 {
		rest := planStr[phaseIdx:]
		if newline := strings.Index(rest, "\n"); newline > 0 {
			summary.WriteString("\n" + strings.TrimSpace(rest[:newline]))
		}
	}

	return summary.String()
}

// LoadTodoSnapshot reads the current task and blockers from avatars_todo.md.
func LoadTodoSnapshot(projectRoot string) string {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return ""
	}
	todoStr := string(content)

	var snapshot strings.Builder
	snapshot.WriteString("Current Workspace:\n")

	if taskIdx := strings.Index(todoStr, "## Current Task"); taskIdx >= 0 {
		taskSection := todoStr[taskIdx:]
		if nextHeader := strings.Index(taskSection[1:], "\n## "); nextHeader > 0 {
			taskSection = taskSection[:nextHeader+1]
		}
		snapshot.WriteString(strings.TrimSpace(taskSection))
	}

	if blockIdx := strings.Index(todoStr, "## Blockers"); blockIdx >= 0 {
		blockSection := todoStr[blockIdx:]
		snapshot.WriteString("\n" + strings.TrimSpace(blockSection))
	}

	return snapshot.String()
}

// ReadWorkflowDoc reads a specific workflow document and returns its content.
// Valid names: "plan", "todo", "record".
func ReadWorkflowDoc(projectRoot string, name string) (string, error) {
	docPath, ok := DocPaths[name]
	if !ok {
		return "", fmt.Errorf("unknown workflow doc: %s (valid: plan, todo, record)", name)
	}
	fullPath := filepath.Join(projectRoot, docPath)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", docPath, err)
	}
	return string(content), nil
}

// WriteWorkflowDoc writes content to a specific workflow document.
func WriteWorkflowDoc(projectRoot string, name string, content string) error {
	docPath, ok := DocPaths[name]
	if !ok {
		return fmt.Errorf("unknown workflow doc: %s (valid: plan, todo, record)", name)
	}
	fullPath := filepath.Join(projectRoot, docPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create workflow directory: %w", err)
	}
	return WriteFileAtomic(fullPath, []byte(content), 0644)
}

// LoadMemoryContext merges project-scoped lessons with the workflow session
// snapshot and formats them for LLM injection. S3.2/S3.5: prefer recent
// actionable pitfalls; enforce a character budget so prompts stay lean.
func LoadMemoryContext() string {
	store := getMemoryStore()
	if store == nil {
		return ""
	}

	const maxMemoryChars = 12000
	var sb strings.Builder
	sb.WriteString("### Project Memory (from SQLite)\n")
	wrote := false

	// S3.2: project_lessons are cross-session; surface them first.
	if projectLessons, err := store.LoadProjectLessons(); err == nil && len(projectLessons) > 0 {
		sb.WriteString("**Project lessons (cross-session):**\n")
		for _, lesson := range projectLessons {
			if !isActionableMemoryKind(lesson.Kind) && !isActionablePitfallKind(lesson.Kind) {
				continue
			}
			line := fmt.Sprintf("- [%s] %s\n", lesson.Kind, lesson.Summary)
			if sb.Len()+len(line) > maxMemoryChars {
				sb.WriteString("... (memory budget exceeded)\n")
				return sb.String()
			}
			sb.WriteString(line)
			wrote = true
		}
	}

	snapshot, err := store.LoadSnapshot("workflow")
	if err != nil {
		if !wrote {
			return ""
		}
		return sb.String()
	}

	// Pitfalls / warm lessons — recency order already from LoadSnapshot.
	if len(snapshot.WarmLessons) > 0 {
		headerWritten := false
		for _, lesson := range snapshot.WarmLessons {
			if !isActionablePitfallKind(lesson.Kind) {
				continue
			}
			if !headerWritten {
				sb.WriteString("**Known pitfalls:**\n")
				headerWritten = true
			}
			line := fmt.Sprintf("- [%s] %s\n", lesson.Kind, lesson.Summary)
			if sb.Len()+len(line) > maxMemoryChars {
				sb.WriteString("... (memory budget exceeded)\n")
				return sb.String()
			}
			sb.WriteString(line)
			wrote = true
		}
	}

	// Recent task outcomes from evaluation_records.
	if len(snapshot.EvaluationRecords) > 0 {
		sb.WriteString("**Recent task outcomes:**\n")
		for _, record := range snapshot.EvaluationRecords {
			line := fmt.Sprintf("- [%s] %s\n", record.Verdict, record.Summary)
			if sb.Len()+len(line) > maxMemoryChars {
				sb.WriteString("... (memory budget exceeded)\n")
				break
			}
			sb.WriteString(line)
			wrote = true
		}
	}
	if !wrote {
		return ""
	}

	return sb.String()
}
