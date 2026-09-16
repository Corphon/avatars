// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package skillbuilder

import (
	"strings"
	"testing"

	"avatars/internal/planner"
)

func TestBuild_GeneratesCandidateMarkdown(t *testing.T) {
	proposal := Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")
	markdown := proposal.Markdown()

	if proposal.Slug == "" {
		t.Fatal("expected non-empty slug")
	}
	if !strings.Contains(markdown, "name: Task Survey Skill") {
		t.Fatalf("expected name in markdown, got %s", markdown)
	}
	if !strings.Contains(markdown, "allowed-tools:") || !strings.Contains(markdown, "  - read") {
		t.Fatalf("expected allowed-tools block, got %s", markdown)
	}
	if !strings.Contains(markdown, "disable-model-invocation: true") {
		t.Fatalf("expected disable-model-invocation flag, got %s", markdown)
	}
	if !strings.Contains(markdown, "## Repo Survey Summary") {
		t.Fatalf("expected body sections, got %s", markdown)
	}
	if !strings.Contains(markdown, "Analyze the current repository and propose a refactoring plan") {
		t.Fatalf("expected English task focus to be preserved, got %s", markdown)
	}
}

func TestBuild_RewritesNonEnglishTaskFocusToEnglishFallback(t *testing.T) {
	proposal := Build("分析当前仓库并给出重构方案", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")
	markdown := proposal.Markdown()

	if strings.Contains(markdown, "分析当前仓库并给出重构方案") {
		t.Fatalf("expected non-English task text to be excluded from generated markdown, got %s", markdown)
	}
	if !strings.Contains(markdown, "Current repository analysis and implementation planning task.") {
		t.Fatalf("expected English fallback task focus, got %s", markdown)
	}
}

// TestBuildForAttachedFile_DerivesNameAndSlugFromFile is the success-criteria
// test for TODO-10. Before this fix, the skill proposal emitted by the
// runtime at planning time was the hardcoded "Task Survey Skill" template
// regardless of what the user attached via `@<file>` (the user reported:
// "把它修改为 avatars可用的skills格式 → output was unrelated generic
// 'Task Survey Skill'"). This test pins the new contract: when an attached
// file is present, the proposal's Slug comes from the file basename and
// the Name comes from the first `# Heading` in the file content.
func TestBuildForAttachedFile_DerivesNameAndSlugFromFile(t *testing.T) {
	const attachedPath = "svg-canvas-js-hybrid-animation_SKILL.md"
	const attachedContent = `# SVG + Canvas + JS 混合动画开发（通用型）

This skill describes how to combine SVG, Canvas, and JavaScript to build
performant hybrid animation systems. It covers declarative SVG structure,
procedural Canvas effects, and the bridge between them.

## When to use
- Cross-renderer animation requirements.
- Performance-sensitive scenes where SVG-only or Canvas-only is not enough.

## Workflow
1. Define the SVG scaffold.
2. Layer Canvas on top for the hot path.
3. Bridge via Web Animations API.
`
	proposal := BuildForAttachedFile(
		"把它修改为 avatars可用的skills格式",
		planner.Build("Convert attached file to avatars skill format"),
		AttachedFile{Path: attachedPath, Content: attachedContent},
	)
	markdown := proposal.Markdown()

	// 1. Slug must be derived from the file basename, NOT the hardcoded
	//    "task-survey-skill" template. Hyphen-normalized, no extension.
	if proposal.Slug == "task-survey-skill" {
		t.Fatalf("slug should NOT be the hardcoded template; got %q", proposal.Slug)
	}
	if !strings.Contains(proposal.Slug, "svg-canvas") {
		t.Fatalf("expected slug to contain file basename 'svg-canvas', got %q", proposal.Slug)
	}
	if strings.Contains(proposal.Slug, "_") {
		t.Fatalf("expected slug to be underscore-normalized, got %q", proposal.Slug)
	}
	if strings.HasSuffix(proposal.Slug, ".md") {
		t.Fatalf("expected slug to drop the .md extension, got %q", proposal.Slug)
	}

	// 2. Name must come from the first `# Heading` in the file content.
	if !strings.Contains(proposal.Name, "SVG + Canvas + JS 混合动画开发") {
		t.Fatalf("expected Name to come from first heading; got %q", proposal.Name)
	}
	if proposal.Name == "Task Survey Skill" {
		t.Fatalf("Name should NOT be the hardcoded template; got %q", proposal.Name)
	}

	// 3. Description must contain a non-trivial excerpt of the file
	//    content (first paragraph), NOT the hardcoded template text.
	if !strings.Contains(proposal.Description, "combine SVG, Canvas") {
		t.Fatalf("expected Description to come from file content, got %q", proposal.Description)
	}
	if strings.Contains(proposal.Description, "task-scoped repository survey") {
		t.Fatalf("Description should NOT be the hardcoded template text; got %q", proposal.Description)
	}

	// 4. TemplateID must be the new attached-file template, not the old
	//    phase2-task-survey.
	if proposal.TemplateID != "phase2-attached-file-conversion" {
		t.Fatalf("expected TemplateID=phase2-attached-file-conversion, got %q", proposal.TemplateID)
	}

	// 5. Markdown serialization must include the file's content in the
	//    body excerpt and the source path header.
	if !strings.Contains(markdown, "## Source File") {
		t.Fatalf("expected body to have a Source File section, got %s", markdown)
	}
	if !strings.Contains(markdown, "Path: "+attachedPath) {
		t.Fatalf("expected body to quote the source path, got %s", markdown)
	}
	if !strings.Contains(markdown, "## Source Excerpt") {
		t.Fatalf("expected body to have a Source Excerpt section, got %s", markdown)
	}
	if !strings.Contains(markdown, "SVG + Canvas + JS 混合动画开发") {
		t.Fatalf("expected body excerpt to contain file content, got %s", markdown)
	}
	if !strings.Contains(markdown, "## Suggested Workflow") {
		t.Fatalf("expected body to have a Suggested Workflow section, got %s", markdown)
	}

	// 6. WhenToUse should reference the attached path.
	if !strings.Contains(proposal.WhenToUse, attachedPath) {
		t.Fatalf("expected WhenToUse to mention the attached path, got %q", proposal.WhenToUse)
	}
}

// TestBuildForAttachedFile_FallsBackToBuildWhenPathEmpty proves backward
// compatibility: when no @file is attached (Path == ""), the function
// returns the same hardcoded "Task Survey Skill" template that
// Build(input, plan, repoSummary) returns. This is the no-attached-file
// path the runtime takes by default.
func TestBuildForAttachedFile_FallsBackToBuildWhenPathEmpty(t *testing.T) {
	proposal := BuildForAttachedFile(
		"some task",
		planner.Build("some task"),
		AttachedFile{Path: "", Content: "irrelevant"},
	)
	if proposal.Slug != "task-survey-skill" {
		t.Fatalf("expected fallback to Build()'s hardcoded slug, got %q", proposal.Slug)
	}
	if proposal.TemplateID != "phase2-task-survey" {
		t.Fatalf("expected fallback to Build()'s template id, got %q", proposal.TemplateID)
	}
}

// TestBuildForAttachedFile_FallsBackToBuildWhenContentEmpty proves the
// second graceful-degradation path: path set but content blank (e.g. the
// file was unreadable). The proposal should still mention the file path
// but use the hardcoded template — better than crashing.
func TestBuildForAttachedFile_FallsBackToBuildWhenContentEmpty(t *testing.T) {
	proposal := BuildForAttachedFile(
		"some task",
		planner.Build("some task"),
		AttachedFile{Path: "ghost.md", Content: "   \n\n  "},
	)
	if proposal.Slug != "task-survey-skill" {
		t.Fatalf("expected fallback to Build()'s hardcoded slug, got %q", proposal.Slug)
	}
	// The body should at least mention the empty-file path in the repo
	// summary section (Build's repo summary is the third arg, and we
	// pass a descriptive string for this case).
	markdown := proposal.Markdown()
	if !strings.Contains(markdown, "ghost.md") {
		t.Fatalf("expected fallback body to mention the empty path, got %s", markdown)
	}
}

// TestBuildForAttachedFile_NameFallsBackToBasenameWhenNoHeading proves the
// extraction fallback chain: if the file has no `# Heading` line, the
// proposal Name should come from the title-cased basename.
func TestBuildForAttachedFile_NameFallsBackToBasenameWhenNoHeading(t *testing.T) {
	proposal := BuildForAttachedFile(
		"task text",
		planner.Build("task text"),
		AttachedFile{
			Path:    "my_tooling_skill.md",
			Content: "This file has no markdown heading.\n\nJust plain prose from start to finish.",
		},
	)
	if !strings.Contains(proposal.Name, "My Tooling Skill") {
		t.Fatalf("expected Name to be title-cased basename 'My Tooling Skill', got %q", proposal.Name)
	}
	if !strings.Contains(proposal.Slug, "my-tooling-skill") {
		t.Fatalf("expected Slug to be hyphen-normalized basename, got %q", proposal.Slug)
	}
}

// TestBuildForAttachedFile_TruncatesExcerptForLargeFiles proves the body
// excerpt is bounded so the generated file doesn't blow up when the user
// attaches a 33KB skill file.
func TestBuildForAttachedFile_TruncatesExcerptForLargeFiles(t *testing.T) {
	huge := strings.Repeat("Lorem ipsum dolor sit amet. ", 500) // ~14KB
	proposal := BuildForAttachedFile(
		"task text",
		planner.Build("task text"),
		AttachedFile{Path: "huge.md", Content: huge},
	)
	markdown := proposal.Markdown()
	if len(markdown) > 12000 {
		t.Fatalf("expected markdown to be bounded, got %d bytes", len(markdown))
	}
	if !strings.Contains(markdown, "excerpt truncated at") {
		t.Fatalf("expected truncated excerpt marker, got %s", markdown)
	}
}
