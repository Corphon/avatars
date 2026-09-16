package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BuildEditManyWorkflowContext reads the project's workflow documents and
// returns a compact context string for injection into the edit-many LLM prompt.
func BuildEditManyWorkflowContext(root string) string {
	var b strings.Builder

	plan, err := ReadWorkflowDoc(root, "plan")
	if err != nil || plan == "" {
		return ""
	}

	meta := ParsePlanMeta(plan)
	if meta.Project != "" {
		b.WriteString(fmt.Sprintf("Project: %s (status: %s)\n", meta.Project, meta.Status))
	}

	goals := extractSection(plan, "## Goals", "## Phases")
	if goals != "" {
		b.WriteString("Goals:\n")
		for _, line := range strings.Split(goals, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				b.WriteString("  " + trimmed + "\n")
			}
		}
	}

	for _, phase := range meta.Phases {
		if phase.Status == "active" {
			b.WriteString(fmt.Sprintf("Active phase: %s — %s\n", phase.Name, phase.Goal))
			phaseContent, _ := ReadWorkflowDoc(root, fmt.Sprintf("phase%d", phase.Number))
			if phaseContent != "" {
				tasks := extractSection(phaseContent, "## Tasks", "## Verification")
				if tasks == "" {
					tasks = extractSection(phaseContent, "## Implementation Tasks", "## Verification")
				}
				if tasks != "" {
					b.WriteString("Phase tasks:\n")
					for _, line := range strings.Split(tasks, "\n") {
						trimmed := strings.TrimSpace(line)
						if strings.HasPrefix(trimmed, "- [") {
							b.WriteString("  " + trimmed + "\n")
						}
					}
				}
			}
			break
		}
	}

	decisions := extractSection(plan, "## Key Decisions", "## Risks")
	if decisions != "" {
		for _, line := range strings.Split(decisions, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "| ") && !strings.HasPrefix(trimmed, "| Date") && !strings.HasPrefix(trimmed, "|--") {
				b.WriteString("Decision: " + trimmed + "\n")
			}
		}
	}

	pitfalls := ReadPitfalls(root)
	if pitfalls != "" {
		if len(pitfalls) > 300 {
			pitfalls = pitfalls[:300] + "..."
		}
		b.WriteString("\nPitfalls to avoid:\n" + pitfalls)
	}

	if b.Len() == 0 {
		return ""
	}

	return "\n## Project Workflow Context\n" + b.String()
}

// RecordEditManyOutcome records the result of an edit-many operation.
func RecordEditManyOutcome(root string, instruction string, filesChanged []string, verifierErr string) {
	summary := fmt.Sprintf("edit-many: %s", trimLen(instruction, 100))
	if verifierErr != "" {
		summary += fmt.Sprintf(" — VERIFIER FAILED: %s", trimLen(verifierErr, 200))
	} else if len(filesChanged) > 0 {
		summary += fmt.Sprintf(" — modified %s", strings.Join(filesChanged, ", "))
	}
	RecordTaskCompletion(root, summary, summary, filesChanged, verifierErr)
}

func extractSection(content, startHeader, endHeader string) string {
	startIdx := strings.Index(content, startHeader)
	if startIdx < 0 {
		return ""
	}
	startIdx += len(startHeader)
	if nl := strings.Index(content[startIdx:], "\n"); nl >= 0 {
		startIdx += nl + 1
	}
	endIdx := strings.Index(content[startIdx:], endHeader)
	if endIdx < 0 {
		return strings.TrimSpace(content[startIdx:])
	}
	return strings.TrimSpace(content[startIdx : startIdx+endIdx])
}

func trimLen(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// MarkTodoProgress updates the todo.md after an edit: marks checklist items,
// moves the completed task to Completed, and sets the new Current Task.
func MarkTodoProgress(root string, filesChanged []string, verifierPassed bool) {
	todoPath := filepath.Join(root, "docs", "workflow", "avatars_todo.md")
	data, err := os.ReadFile(todoPath)
	if err != nil {
		return
	}
	content := string(data)
	now := time.Now().UTC().Format(time.RFC3339)

	// Mark checklist items.
	if len(filesChanged) > 0 {
		content = strings.Replace(content,
			"- [ ] Files identified in phase1.md modified",
			"- [x] Files identified in phase1.md modified", 1)
	}
	if verifierPassed {
		content = strings.Replace(content,
			"- [ ] Build/tests pass after changes",
			"- [x] Build/tests pass after changes", 1)
	}

	// Move current task to Completed and clear it for the next task.
	// Find the current task line and move it.
	lines := strings.Split(content, "\n")
	var result []string
	inCompleted := false
	taskMoved := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "> **Updated**:") {
			result = append(result, fmt.Sprintf("> **Updated**: %s", now))
			continue
		}
		// Detect Completed section.
		if trimmed == "## Completed" {
			inCompleted = true
		}
		// Move current task to Completed.
		if strings.HasPrefix(trimmed, "- [ ] ") && strings.Contains(line, "## Current Task") == false && !taskMoved {
			// This is a task from Current Task section (preceded by ## Current Task).
			// Check if the previous non-empty line was "## Current Task".
			for j := i - 1; j >= 0; j-- {
				prevTrimmed := strings.TrimSpace(lines[j])
				if prevTrimmed == "" {
					continue
				}
				if prevTrimmed == "## Current Task" {
					// Move to Completed with [x].
					if inCompleted {
						// Already in Completed section — just append.
					} else {
						// Need to insert into Completed section.
						taskText := strings.TrimPrefix(trimmed, "- [ ] ")
						result = append(result, fmt.Sprintf("- [x] %s", taskText))
						taskMoved = true
						continue // skip the original line
					}
				}
				break
			}
		}
		// Reset Current Task to show new work.
		if trimmed == "## Current Task" && taskMoved {
			result = append(result, line)
			result = append(result, "- [ ] (next task — edit to update)")
			continue
		}
		// Skip the old task line that was just moved.
		if taskMoved && strings.HasPrefix(trimmed, "- [ ] ") && i > 0 {
			for j := i - 1; j >= 0; j-- {
				if strings.TrimSpace(lines[j]) == "## Current Task" {
					continue // this is the old task line, skip it
				}
				if strings.TrimSpace(lines[j]) == "" {
					continue
				}
				break
			}
		}
		// Update Notes with what was just done.
		if trimmed == "## Notes" {
			result = append(result, line)
			if len(filesChanged) > 0 {
				status := "✅"
				if !verifierPassed {
					status = "⚠️"
				}
				result = append(result, fmt.Sprintf("- %s Edited: %s", status, strings.Join(filesChanged, ", ")))
			}
			continue
		}
		// Skip stale Task note.
		if strings.HasPrefix(trimmed, "- Task:") && taskMoved {
			continue
		}
		result = append(result, line)
		_ = i
	}
	if taskMoved || len(filesChanged) > 0 {
		_ = os.WriteFile(todoPath, []byte(strings.Join(result, "\n")), 0644)
	}
}

func extractUpdatedField(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "> **Updated**:") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "> **Updated**:"))
		}
	}
	return ""
}

func EnsureWorkflowDir(root string) error {
	return os.MkdirAll(filepath.Join(root, "docs", "workflow"), 0755)
}
