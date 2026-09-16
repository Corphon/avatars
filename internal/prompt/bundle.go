package prompt

import (
	"fmt"
	"strings"
)

type Section struct {
	Name       string
	Content    string
	CacheBreak bool
}

type Bundle struct {
	StablePrefix    []string
	DynamicSections []Section
}

type AlwaysOnSkill struct {
	Name    string
	Context string
	Body    string
}

func Build(userInput string) Bundle {
	return BuildWithResume(userInput, "", "", nil, nil, nil, "")
}

func BuildWithResume(userInput string, restoredSummary string, restoredTaskSummary string, restoredRecentEvents []string, restoredWarmLessons []string, restoredProjectLessons []string, restoredVerificationContext string) Bundle {
	dynamic := []Section{
		{
			Name:       "user_request",
			Content:    userInput,
			CacheBreak: true,
		},
	}
	if restoredSummary != "" {
		dynamic = append(dynamic, Section{
			Name:       "restored_session_summary",
			Content:    restoredSummary,
			CacheBreak: true,
		})
	}
	if restoredTaskSummary != "" {
		dynamic = append(dynamic, Section{
			Name:       "restored_task_summary",
			Content:    restoredTaskSummary,
			CacheBreak: true,
		})
	}
	if len(restoredRecentEvents) > 0 {
		dynamic = append(dynamic, Section{
			Name:       "restored_recent_events",
			Content:    formatRecentEvents(restoredRecentEvents),
			CacheBreak: true,
		})
	}
	if len(restoredWarmLessons) > 0 {
		dynamic = append(dynamic, Section{
			Name:       "restored_warm_lessons",
			Content:    formatWarmLessons(restoredWarmLessons),
			CacheBreak: true,
		})
	}
	if len(restoredProjectLessons) > 0 {
		dynamic = append(dynamic, Section{
			Name:       "restored_project_lessons",
			Content:    formatWarmLessons(restoredProjectLessons),
			CacheBreak: true,
		})
	}
	if strings.TrimSpace(restoredVerificationContext) != "" {
		dynamic = append(dynamic, Section{
			Name:       "restored_verification_context",
			Content:    strings.TrimSpace(restoredVerificationContext),
			CacheBreak: true,
		})
	}

	return Bundle{
		StablePrefix: []string{
			"avatars bootstrap runtime",
			"prefer deterministic and locally auditable execution",
			"for guarded mutations, always state intent, expected_targets, and verification follow-up in English",
			"treat expected_targets as declared intended scope only; changed_files remains the observed mutation evidence",
		},
		DynamicSections: dynamic,
	}
}

func (b Bundle) WithAlwaysOnSkills(skills []AlwaysOnSkill) Bundle {
	if len(skills) == 0 {
		return b
	}
	out := Bundle{
		StablePrefix:    append([]string{}, b.StablePrefix...),
		DynamicSections: append([]Section{}, b.DynamicSections...),
	}
	for _, skill := range skills {
		content := compactAlwaysOnSkill(skill)
		if content == "" {
			continue
		}
		out.StablePrefix = append(out.StablePrefix, content)
	}
	return out
}

func compactAlwaysOnSkill(skill AlwaysOnSkill) string {
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		name = "always-on skill"
	}
	lines := []string{"always-on skill: " + name}
	if context := strings.TrimSpace(skill.Context); context != "" {
		lines = append(lines, "context:")
		lines = append(lines, capSkillContent(context, 4000))
	}
	if body := strings.TrimSpace(skill.Body); body != "" {
		lines = append(lines, "rules:")
		lines = append(lines, capSkillContent(body, 4000))
	}
	return strings.Join(lines, "\n")
}

// capSkillContent returns the value with newlines preserved, trimmed only
// when the raw byte length exceeds limit. Truncation is a last-resort
// safety valve: skill bodies are first-class evidence for the avatar LLM
// and should be readable in their original multi-line form.
func capSkillContent(value string, limit int) string {
	trimmed := strings.TrimRight(value, "\n")
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "\n[truncated: skill body exceeded " + fmt.Sprintf("%d", limit) + " chars]"
}

func formatRecentEvents(events []string) string {
	lines := make([]string, 0, len(events))
	for index, event := range events {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, event))
	}
	return strings.Join(lines, "\n")
}

func formatWarmLessons(lessons []string) string {
	lines := make([]string, 0, len(lessons))
	for index, lesson := range lessons {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, lesson))
	}
	return strings.Join(lines, "\n")
}

// AttachedFile represents a file the user explicitly attached to a task
// (via `avatars run --from-file` or the REPL `@<path>` syntax). The path
// and content travel together into the prompt as a dedicated section so
// every avatar sees the same evidence base, instead of each avatar having
// to re-read the file independently. The content is multi-line preserved
// and not truncated to one line: attached files are first-class evidence,
// not a summary.
type AttachedFile struct {
	Path    string
	Content string
}

// WithAttachedFile returns a copy of the bundle with an `attached_file`
// dynamic section appended after `user_request`. The section is content
// rich (no `oneLine`/`compactAlwaysOnSkill` treatment) so the avatar LLM
// sees the raw attached content. The section is cache-break so different
// attached files do not poison the LLM prompt cache.
func (b Bundle) WithAttachedFile(file AttachedFile) Bundle {
	path := strings.TrimSpace(file.Path)
	content := strings.TrimSpace(file.Content)
	if path == "" || content == "" {
		return b
	}
	out := Bundle{
		StablePrefix:    append([]string{}, b.StablePrefix...),
		DynamicSections: append([]Section{}, b.DynamicSections...),
	}
	section := Section{
		Name:       "attached_file",
		Content:    fmt.Sprintf("path: %s\n---\n%s", path, content),
		CacheBreak: true,
	}
	if len(out.DynamicSections) == 0 {
		out.DynamicSections = []Section{section}
		return out
	}
	// Insert right after `user_request` so the attached evidence is the
	// first thing the avatar reads after the user's request, and before
	// any restored memory / verifier context sections.
	inserted := make([]Section, 0, len(out.DynamicSections)+1)
	inserted = append(inserted, out.DynamicSections[0])
	inserted = append(inserted, section)
	inserted = append(inserted, out.DynamicSections[1:]...)
	out.DynamicSections = inserted
	return out
}

// WithREPLContext returns a copy of the bundle with a `repl_context`
// dynamic section appended after `user_request` (and `attached_file` if
// present). The section contains a compact summary of recent REPL turns
// so the avatar LLM can maintain conversational continuity across runs
// without relying solely on transcript restore. Empty context is a no-op.
// WithWorkflowContext appends plan/todo/process state as a *dynamic* user
// section. It must never prepend StablePrefix: provider prefix-cache (DeepSeek
// prompt_cache_hit_tokens, OpenAI cached_tokens, Anthropic cache_read) keys
// off a byte-identical system prefix. Checkboxes and phase status change every
// node, so putting workflow at the front of the system prompt zeros the hit
// rate. Empty context is a no-op.
func (b Bundle) WithWorkflowContext(context string) Bundle {
	trimmed := strings.TrimSpace(context)
	if trimmed == "" {
		return b
	}
	out := Bundle{
		StablePrefix:    append([]string{}, b.StablePrefix...),
		DynamicSections: append([]Section{}, b.DynamicSections...),
	}
	out.DynamicSections = append(out.DynamicSections, Section{
		Name:       "workflow_context",
		Content:    trimmed,
		CacheBreak: true,
	})
	return out
}

func (b Bundle) WithREPLContext(context string) Bundle {
	trimmed := strings.TrimSpace(context)
	if trimmed == "" {
		return b
	}
	out := Bundle{
		StablePrefix:    append([]string{}, b.StablePrefix...),
		DynamicSections: append([]Section{}, b.DynamicSections...),
	}
	section := Section{
		Name:       "repl_context",
		Content:    trimmed,
		CacheBreak: true,
	}
	// Insert after user_request (+ attached_file if present).
	insertIdx := 1
	for i, s := range out.DynamicSections {
		if s.Name == "user_request" || s.Name == "attached_file" {
			insertIdx = i + 1
		}
	}
	inserted := make([]Section, 0, len(out.DynamicSections)+1)
	inserted = append(inserted, out.DynamicSections[:insertIdx]...)
	inserted = append(inserted, section)
	inserted = append(inserted, out.DynamicSections[insertIdx:]...)
	out.DynamicSections = inserted
	return out
}
