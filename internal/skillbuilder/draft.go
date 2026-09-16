package skillbuilder

import (
	"os"
	"strings"

	"avatars/internal/planner"
)

type Draft struct {
	Description string
	WhenToUse   string
	Body        string
}

func BuildWithDraft(task string, plan planner.Plan, repoSummary string, draft Draft) Proposal {
	proposal := Build(task, plan, repoSummary)

	if description := trimBoundedText(draft.Description, 24, 180); description != "" {
		proposal.Description = description
	}
	if whenToUse := trimBoundedText(draft.WhenToUse, 24, 220); whenToUse != "" {
		proposal.WhenToUse = whenToUse
	}
	if body := strings.TrimSpace(draft.Body); TaskSurveyBodyLooksValid(body) {
		proposal.Body = body
	}

	return proposal
}

// K4: Reads from skills/templates/_template.md if present, falling back to
// the hardcoded default. Users can customize the template by editing the file.
func TaskSurveyRequiredBodySections() []string {
	// Try to read the customizable template file.
	if headings := readTemplateHeadings("skills/templates/_template.md"); len(headings) > 0 {
		return headings
	}
	// Fallback: hardcoded defaults (keep for backward compat).
	return []string{
		"## Purpose",
		"## Task Focus",
		"## Repo Survey Summary",
		"## Suggested Workflow",
		"## Constraints",
	}
}

// readTemplateHeadings extracts `## Heading` lines from a markdown template
// file. K4: Returns nil if the file doesn't exist or has no headings.
func readTemplateHeadings(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var headings []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") && !strings.HasPrefix(trimmed, "### ") {
			headings = append(headings, trimmed)
		}
	}
	return headings
}

func TaskSurveyBodyLooksValid(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	for _, heading := range TaskSurveyRequiredBodySections() {
		if !strings.Contains(trimmed, heading) {
			return false
		}
	}
	return true
}

func trimBoundedText(value string, min int, max int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) < min {
		return ""
	}
	if len(trimmed) > max {
		trimmed = strings.TrimSpace(trimmed[:max])
	}
	return trimmed
}
