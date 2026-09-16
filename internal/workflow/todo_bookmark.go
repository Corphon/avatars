package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// UpdateTodoProgressBookmark marks the Phase Checklist in avatars_todo.md
// with progress indicators so the LLM knows where to resume after interruption.
//
// It adds two markers:
//   - [>] on the first incomplete task (CURRENT — pick up here)
//   - "↳ next: ..." annotation below the current task pointing to the next one
func UpdateTodoProgressBookmark(projectRoot string) error {
	todoPath := filepath.Join(projectRoot, DocPaths["todo"])
	content, err := os.ReadFile(todoPath)
	if err != nil {
		return err
	}
	todoStr := string(content)

	// Find the Phase Checklist section.
	checklistStart := strings.Index(todoStr, "## Phase ")
	if checklistStart < 0 {
		checklistStart = strings.Index(todoStr, "## Phase Checklist")
	}
	if checklistStart < 0 {
		return nil // no phase checklist section yet
	}

	// Split: before / checklist / after.
	afterHeader := todoStr[checklistStart:]
	nextSection := strings.Index(afterHeader[1:], "\n## ")
	var before, checklistSection, after string
	if nextSection > 0 {
		before = todoStr[:checklistStart]
		checklistSection = afterHeader[:nextSection+1]
		after = afterHeader[nextSection+1:]
	} else {
		before = todoStr[:checklistStart]
		checklistSection = afterHeader
	}

	// Parse and rebuild checklist lines. Strip leftover bookmark arrows first
	// so repeated calls cannot stack "↳ (last task in this phase)" (F75).
	lines := strings.Split(checklistSection, "\n")
	var newLines []string
	firstIncompleteIdx := -1

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isTodoBookmarkAnnotation(trimmed) {
			continue
		}

		if strings.HasPrefix(trimmed, "## ") || trimmed == "" {
			newLines = append(newLines, line)
			continue
		}

		if strings.HasPrefix(trimmed, "- [>]") {
			line = strings.Replace(line, "- [>]", "- [ ]", 1)
			trimmed = strings.TrimSpace(line)
		}

		if firstIncompleteIdx < 0 && strings.HasPrefix(trimmed, "- [ ]") {
			firstIncompleteIdx = len(newLines)
		}

		newLines = append(newLines, line)
	}

	if firstIncompleteIdx >= 0 {
		newLines[firstIncompleteIdx] = strings.Replace(
			newLines[firstIncompleteIdx], "- [ ]", "- [>]", 1,
		)

		nextPreview := ""
		for j := firstIncompleteIdx + 1; j < len(newLines); j++ {
			t := strings.TrimSpace(newLines[j])
			if strings.HasPrefix(t, "- [ ]") {
				taskDesc := strings.TrimPrefix(t, "- [ ] ")
				nextPreview = truncateRunes(taskDesc, 80)
				break
			}
		}

		annotation := fmt.Sprintf("↳ next: %s", nextPreview)
		if nextPreview == "" {
			annotation = "↳ (last task in this phase)"
		}

		tail := make([]string, len(newLines)-firstIncompleteIdx-1)
		copy(tail, newLines[firstIncompleteIdx+1:])
		newLines = append(newLines[:firstIncompleteIdx+1], annotation)
		newLines = append(newLines, tail...)
	}

	today := time.Now().Format("2006-01-02")
	result := before + strings.Join(newLines, "\n") + after
	result = replaceTodoTimestamp(result, today)

	return WriteFileAtomic(todoPath, []byte(result), 0644)
}

func isTodoBookmarkAnnotation(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "↳")
}

func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if max > 3 && len(runes) > max {
		return string(runes[:max-3]) + "..."
	}
	return string(runes[:max])
}

// replaceTodoTimestamp replaces the "> **Updated**: " date in todo content.
func replaceTodoTimestamp(content, today string) string {
	marker := "> **Updated**: "
	idx := strings.Index(content, marker)
	if idx < 0 {
		return content
	}
	rest := content[idx+len(marker):]
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		return content[:idx+len(marker)] + today
	}
	return content[:idx+len(marker)] + today + rest[nl:]
}
