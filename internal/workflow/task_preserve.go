package workflow

import (
	"strings"
)

// collectCompletedTaskDescs extracts task descriptions from [x] items
// in the current todo checklist, for preservation during sync.
func collectCompletedTaskDescs(todoStr string) []string {
	var descs []string
	inChecklist := false
	for _, line := range strings.Split(todoStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Phase ") && strings.Contains(trimmed, "Checklist") {
			inChecklist = true
			continue
		}
		if inChecklist && isH2(trimmed) {
			break
		}
		if inChecklist && strings.HasPrefix(trimmed, "- [x]") {
			desc := strings.TrimPrefix(trimmed, "- [x]")
			descs = append(descs, strings.TrimSpace(desc))
		}
	}
	return descs
}

// wasCompleted checks if a task description matches any previously
// completed task. Labels must match after stripping (N/M) counts and
// leading "1. " group numbers — no loose substring matching (R7: cross-
// phase false [x] on "Testing" / "Integration").
func wasCompleted(taskLine string, completedDescs []string) bool {
	desc := strings.TrimPrefix(strings.TrimSpace(taskLine), "- [ ]")
	desc = strings.TrimPrefix(desc, "- [x]")
	desc = strings.TrimSpace(desc)
	key := normalizeChecklistLabel(desc)
	if key == "" {
		return false
	}
	for _, cd := range completedDescs {
		if normalizeChecklistLabel(cd) == key {
			return true
		}
	}
	return false
}

// stripChecklistCountSuffix removes trailing " (2/2)" progress counts so
// SyncTodo can preserve [x] when phase docs still show open child boxes.
func stripChecklistCountSuffix(s string) string {
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		inner := s[i+2 : len(s)-1]
		if strings.Contains(inner, "/") {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func normalizeChecklistLabel(s string) string {
	s = stripChecklistCountSuffix(strings.TrimSpace(s))
	s = strings.TrimSpace(s)
	// "1. First group" → "First group"
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			continue
		}
		if s[i] == '.' && i+1 < len(s) && s[i+1] == ' ' {
			s = strings.TrimSpace(s[i+2:])
		}
		break
	}
	return strings.ToLower(s)
}
