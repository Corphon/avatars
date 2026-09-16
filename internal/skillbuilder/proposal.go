package skillbuilder

import (
	"path/filepath"
	"strings"
	"unicode"

	"avatars/internal/planner"
)

type Proposal struct {
	Slug                   string
	Name                   string
	Description            string
	WhenToUse              string
	AllowedTools           []string
	Context                string
	UserInvocable          bool
	DisableModelInvocation bool
	AlwaysOn               bool // 3.9: role-specific skills auto-activate after approval
	Version                string
	TemplateID             string
	Body                   string
}

// AttachedFile carries the file the user attached via `@<file>` REPL syntax
// (or `--from-file`). It is used by BuildForAttachedFile to derive a
// proposal whose Name/Description/Slug/Body reflect the attached file
// instead of the hardcoded "Task Survey Skill" template.
//
// Keeping this struct local to skillbuilder (instead of importing
// internal/prompt.AttachedFile) avoids an import cycle: prompt already
// imports nothing from skillbuilder, and the proposal package should not
// depend on prompt. The two structs are intentionally field-compatible
// (Path, Content) so callers can build one from the other.
type AttachedFile struct {
	Path    string
	Content string
}

// BuildForRole generates a role-specific skill proposal for the given
// avatar role. Each avatar (Planner/Researcher/Builder/Critic/Synthesizer)
// gets a dedicated template with role-specific guidance. This is how
// avatars learn to excel at their specific responsibilities (T8.5).
func BuildForRole(role string, task string, plan planner.Plan, repoSummary string) Proposal {
	title := strings.TrimSpace(task)
	if title == "" {
		title = plan.Title
	}
	if title == "" {
		title = "Untitled task"
	}
	taskFocus := englishSafeTaskFocus(title)
	repoSummary = strings.TrimSpace(repoSummary)
	if repoSummary == "" {
		repoSummary = "No repository survey was available when this skill candidate was generated."
	}

	// Use role-specific template if available.
	if tmpl, ok := avatarSkillTemplates[role]; ok {
		body := strings.ReplaceAll(tmpl.body, "{{TASK_FOCUS}}", taskFocus)
		body = strings.ReplaceAll(body, "{{REPO_SURVEY_SUMMARY}}", repoSummary)
		return Proposal{
			Slug:                   tmpl.slug,
			Name:                   tmpl.name,
			Description:            tmpl.description,
			WhenToUse:              "Use when the " + role + " avatar needs role-specific guidance for task execution.",
			AllowedTools:           tmpl.allowed,
			Context:                "inline",
			UserInvocable:          false,
			DisableModelInvocation: true,
			AlwaysOn:               true, // 3.9: role-specific skills auto-activate after approval
			Version:                "0.1.0",
			TemplateID:             "phase2-" + strings.ToLower(role) + "-skill",
			Body:                   body,
		}
	}
	return Build(task, plan, repoSummary)
}

func Build(task string, plan planner.Plan, repoSummary string) Proposal {
	title := strings.TrimSpace(task)
	if title == "" {
		title = plan.Title
	}
	if title == "" {
		title = "Untitled task"
	}
	taskFocus := englishSafeTaskFocus(title)

	repoSummary = strings.TrimSpace(repoSummary)
	if repoSummary == "" {
		repoSummary = "No repository survey was available when this skill candidate was generated."
	}

	body := renderTaskSurveyTemplate(taskFocus, repoSummary)

	return Proposal{
		Slug:                   "task-survey-skill",
		Name:                   "Task Survey Skill",
		Description:            "Generated candidate skill for task-scoped repository survey and synthesis.",
		WhenToUse:              "Use when the runtime needs a task-scoped survey before broader multi-avatar execution.",
		AllowedTools:           []string{"read"},
		Context:                "inline",
		UserInvocable:          false,
		DisableModelInvocation: true,
		Version:                "0.1.0",
		TemplateID:             "phase2-task-survey",
		Body:                   body,
	}
}

// BuildForAttachedFile produces a proposal whose Name/Description/Slug/Body
// are derived from the attached file's content. This is the entry point
// the runtime should use whenever the user supplied an `@<file>` (or
// `--from-file`) — without it, the skill proposal emitted at planning
// time is the hardcoded "Task Survey Skill" template regardless of what
// the user attached (TODO-10 fix).
//
// Behavior:
//   - If `attachedFile.Path` is empty, fall back to `Build` (no file
//     attached; preserve old behavior for non-@file runs).
//   - If `attachedFile.Content` is empty, fall back to `Build` and pass
//     the file path as the repo summary so the proposal at least
//     references the file (graceful degradation).
//   - Slug is derived from the file basename (lowercased, `_` -> `-`,
//     extension stripped, repeated `-` collapsed).
//   - Name prefers the first `# Heading` line in the content; falls
//     back to the title-cased basename.
//   - Description is the first non-heading paragraph, truncated to
//     `attachedDescriptionLimit` chars.
//   - Body is a fixed template that points at the source file path
//     and includes a content excerpt (capped at
//     `attachedExcerptLimit` bytes to keep the generated file readable
//     even for very large attachments).
func BuildForAttachedFile(task string, plan planner.Plan, attachedFile AttachedFile) Proposal {
	path := strings.TrimSpace(attachedFile.Path)
	if path == "" {
		return Build(task, plan, "User attached no file; falling back to generic task survey.")
	}

	content := attachedFile.Content
	if strings.TrimSpace(content) == "" {
		return Build(task, plan, "User attached "+path+" but the file is empty.")
	}

	slug := attachedFileSlug(path)
	name := attachedFileName(path, content)
	description := attachedFileDescription(content)
	excerpt := attachedFileExcerpt(content, attachedExcerptLimit)

	body := renderAttachedFileTemplate(path, len(content), excerpt, task)

	return Proposal{
		Slug:                   slug,
		Name:                   name,
		Description:            description,
		WhenToUse:              "Use when converting the attached file " + path + " into an avatars-usable skill.",
		AllowedTools:           []string{"read"},
		Context:                "inline",
		UserInvocable:          false,
		DisableModelInvocation: true,
		Version:                "0.1.0",
		TemplateID:             "phase2-attached-file-conversion",
		Body:                   body,
	}
}

const attachedDescriptionLimit = 200
const attachedExcerptLimit = 5000

func attachedFileSlug(path string) string {
	base := filepath.Base(path)
	// strip extension
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}
	// normalize: lowercase, replace `_` and spaces with `-`, drop non-alnum
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(base) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "attached-skill"
	}
	return slug
}

func attachedFileName(path string, content string) string {
	// Try first `# Heading` line.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			heading := strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			// strip trailing markdown decorations like `---` or `#`
			heading = strings.TrimRight(heading, "#-_ \t")
			if heading != "" {
				return heading
			}
		}
	}
	// Fall back to title-cased basename.
	base := filepath.Base(path)
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}
	base = strings.NewReplacer("_", " ", "-", " ").Replace(base)
	words := strings.Fields(base)
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	pretty := strings.Join(words, " ")
	if pretty == "" {
		pretty = "Attached File Skill"
	}
	return pretty
}

func attachedFileDescription(content string) string {
	// First non-empty, non-heading paragraph.
	var lines []string
	inHeading := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(lines) > 0 {
				break // end of first paragraph
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			inHeading = true
			continue
		}
		if inHeading {
			inHeading = false
		}
		lines = append(lines, trimmed)
	}
	desc := strings.TrimSpace(strings.Join(lines, " "))
	if desc == "" {
		desc = "Skill derived from attached file content."
	}
	if len(desc) > attachedDescriptionLimit {
		desc = strings.TrimSpace(desc[:attachedDescriptionLimit-1]) + "…"
	}
	return desc
}

func attachedFileExcerpt(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	return content[:limit] + "\n\n[…excerpt truncated at " + itoaInt(len(content)-limit) + " chars omitted…]"
}

// itoaInt is a tiny dependency-free int formatter; avoids importing strconv
// for one call site.
func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func renderAttachedFileTemplate(path string, contentLen int, excerpt string, task string) string {
	taskSummary := strings.TrimSpace(task)
	if taskSummary == "" {
		taskSummary = "(no task text provided alongside the attached file)"
	}
	var b strings.Builder
	b.WriteString("## Purpose\n")
	b.WriteString("Convert the attached file into an avatars-usable skill. The proposal's Name, Description, and Workflow should be re-derived from the source content during LLM review, not copy-pasted.\n\n")
	b.WriteString("## Source File\n")
	b.WriteString("- Path: ")
	b.WriteString(path)
	b.WriteString("\n- Length: ")
	b.WriteString(itoaInt(contentLen))
	b.WriteString(" chars\n\n")
	b.WriteString("## Task Summary\n")
	b.WriteString(taskSummary)
	b.WriteString("\n\n")
	b.WriteString("## Source Excerpt\n")
	b.WriteString(excerpt)
	b.WriteString("\n\n## Suggested Workflow\n")
	b.WriteString("1. Read the full source file to recover structure that the excerpt above may have cut off.\n")
	b.WriteString("2. Re-derive the skill's Name, Description, WhenToUse from the source's leading headings.\n")
	b.WriteString("3. Re-derive AllowedTools from the source's domain (e.g. a draw-anim file implies `read` + maybe `write`).\n")
	b.WriteString("4. Replace this Proposal body with the actual workflow steps; do not ship the placeholder text above.\n")
	b.WriteString("5. Require explicit human approval before activation.\n\n")
	b.WriteString("## Constraints\n")
	b.WriteString("- Read-only tools in this candidate version; do not self-activate.\n")
	b.WriteString("- Source excerpt is included for context only; the reviewer should re-derive, not transcribe.\n")
	b.WriteString("- Keep the output short and evidence-based.")
	return b.String()
}

func englishSafeTaskFocus(task string) string {
	if isEnglishSafeText(task) {
		return task
	}
	return "Current repository analysis and implementation planning task."
}

func isEnglishSafeText(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func (p Proposal) Markdown() string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("name: ")
	builder.WriteString(p.Name)
	builder.WriteString("\n")
	builder.WriteString("description: ")
	builder.WriteString(p.Description)
	builder.WriteString("\n")
	builder.WriteString("when_to_use: ")
	builder.WriteString(p.WhenToUse)
	builder.WriteString("\n")
	builder.WriteString("allowed-tools:\n")
	for _, tool := range p.AllowedTools {
		builder.WriteString("  - ")
		builder.WriteString(tool)
		builder.WriteString("\n")
	}
	builder.WriteString("context: ")
	builder.WriteString(p.Context)
	builder.WriteString("\n")
	builder.WriteString("user-invocable: ")
	builder.WriteString(boolString(p.UserInvocable))
	builder.WriteString("\n")
	builder.WriteString("disable-model-invocation: ")
	builder.WriteString(boolString(p.DisableModelInvocation))
	builder.WriteString("\n")
	builder.WriteString("always-on: ")
	builder.WriteString(boolString(p.AlwaysOn))
	builder.WriteString("\n")
	if p.TemplateID != "" {
		// K1: Extract role from template-id (e.g. "phase2-builder-skill" → "builder").
		if role := extractRoleFromTemplateID(p.TemplateID); role != "" {
			builder.WriteString("role: ")
			builder.WriteString(role)
			builder.WriteString("\n")
		}
	}
	builder.WriteString("version: ")
	builder.WriteString(p.Version)
	builder.WriteString("\n")
	builder.WriteString("template-id: ")
	builder.WriteString(p.TemplateID)
	builder.WriteString("\n")
	builder.WriteString("---\n\n")
	builder.WriteString(strings.TrimSpace(p.Body))
	builder.WriteString("\n")
	return builder.String()
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// ExtractRoleFromTemplateID extracts the avatar role from a template ID
// like "phase2-builder-skill" → "builder". Used when writing role frontmatter
// and when the store backfills Role from template-id (S3.9).
func ExtractRoleFromTemplateID(templateID string) string {
	return extractRoleFromTemplateID(templateID)
}

// extractRoleFromTemplateID extracts the avatar role from a template ID
// like "phase2-builder-skill" → "builder". K1: Used to write role frontmatter
// in generated skill markdown files so the store can filter by role.
func extractRoleFromTemplateID(templateID string) string {
	// TemplateID format: "phase2-<role>-skill" or "phase2-<role>"
	parts := strings.Split(strings.ToLower(templateID), "-")
	knownRoles := map[string]bool{
		"planner": true, "researcher": true, "builder": true,
		"critic": true, "synthesizer": true, "runner": true,
	}
	for _, part := range parts {
		if knownRoles[part] {
			return part
		}
	}
	return ""
}
