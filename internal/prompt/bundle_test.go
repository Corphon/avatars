package prompt

import (
	"strings"
	"testing"
)

func TestBuildWithResume_AddsRestoredSummarySection(t *testing.T) {
	bundle := BuildWithResume("continue task", "stable summary", "task summary", []string{"Planner created the workflow.", "Builder inspected process_record.md."}, []string{"Re-run verifier checks before declaring completion."}, []string{"Keep this verifier follow-up as a reusable project lesson."}, "latest_non_pass_verification: 2026-05-05T00:00:00Z | patch | PARTIAL")
	if len(bundle.DynamicSections) != 7 {
		t.Fatalf("expected 7 dynamic sections, got %d", len(bundle.DynamicSections))
	}
	if bundle.DynamicSections[1].Name != "restored_session_summary" {
		t.Fatalf("expected restored_session_summary section, got %q", bundle.DynamicSections[1].Name)
	}
	if bundle.DynamicSections[1].Content != "stable summary" {
		t.Fatalf("unexpected restored summary content: %q", bundle.DynamicSections[1].Content)
	}
	if bundle.DynamicSections[2].Name != "restored_task_summary" {
		t.Fatalf("expected restored_task_summary section, got %q", bundle.DynamicSections[2].Name)
	}
	if bundle.DynamicSections[2].Content != "task summary" {
		t.Fatalf("unexpected restored task summary content: %q", bundle.DynamicSections[2].Content)
	}
	if bundle.DynamicSections[3].Name != "restored_recent_events" {
		t.Fatalf("expected restored_recent_events section, got %q", bundle.DynamicSections[3].Name)
	}
	if bundle.DynamicSections[3].Content != "1. Planner created the workflow.\n2. Builder inspected process_record.md." {
		t.Fatalf("unexpected restored recent events content: %q", bundle.DynamicSections[3].Content)
	}
	if bundle.DynamicSections[4].Name != "restored_warm_lessons" {
		t.Fatalf("expected restored_warm_lessons section, got %q", bundle.DynamicSections[4].Name)
	}
	if bundle.DynamicSections[4].Content != "1. Re-run verifier checks before declaring completion." {
		t.Fatalf("unexpected restored warm lessons content: %q", bundle.DynamicSections[4].Content)
	}
	if bundle.DynamicSections[5].Name != "restored_project_lessons" {
		t.Fatalf("expected restored_project_lessons section, got %q", bundle.DynamicSections[5].Name)
	}
	if bundle.DynamicSections[5].Content != "1. Keep this verifier follow-up as a reusable project lesson." {
		t.Fatalf("unexpected restored project lessons content: %q", bundle.DynamicSections[5].Content)
	}
	if bundle.DynamicSections[6].Name != "restored_verification_context" {
		t.Fatalf("expected restored_verification_context section, got %q", bundle.DynamicSections[6].Name)
	}
	if bundle.DynamicSections[6].Content != "latest_non_pass_verification: 2026-05-05T00:00:00Z | patch | PARTIAL" {
		t.Fatalf("unexpected restored verification context content: %q", bundle.DynamicSections[6].Content)
	}
}

func TestBuild_AddsGuardedMutationContractToStablePrefix(t *testing.T) {
	bundle := Build("repair the verifier warning")
	joined := strings.Join(bundle.StablePrefix, "\n")
	if !strings.Contains(joined, "intent, expected_targets, and verification follow-up") {
		t.Fatalf("expected stable prefix to include guarded mutation contract, got %q", joined)
	}
	if !strings.Contains(joined, "changed_files remains the observed mutation evidence") {
		t.Fatalf("expected stable prefix to distinguish declared targets from observed changes, got %q", joined)
	}
}

func TestBundleWithAlwaysOnSkills_AddsCompactContractToStablePrefix(t *testing.T) {
	bundle := Build("analyze repository").WithAlwaysOnSkills([]AlwaysOnSkill{{
		Name:    "avatars-operating-contract",
		Context: "Governs clarification and memory reuse.",
		Body:    "Ask a short clarification when target/action is unclear. Reuse memory before rescanning.",
	}})
	joined := strings.Join(bundle.StablePrefix, "\n")
	if !strings.Contains(joined, "always-on skill: avatars-operating-contract") {
		t.Fatalf("expected always-on skill marker in stable prefix, got %q", joined)
	}
	if !strings.Contains(joined, "Reuse memory before rescanning") {
		t.Fatalf("expected always-on skill rules in stable prefix, got %q", joined)
	}
	if len(bundle.DynamicSections) != 1 || bundle.DynamicSections[0].Name != "user_request" {
		t.Fatalf("expected dynamic sections to remain unchanged, got %+v", bundle.DynamicSections)
	}
}

// TODO-02 (P0): The `@file` / `--from-file` content must reach the prompt as
// an `attached_file` section so the avatar sees the same evidence base as
// the user. This is the bundle-level contract: the section is appended
// right after `user_request`, the content is multi-line preserved (no
// `oneLine` collapse), and the section is cache-break so a different
// attached file does not poison the LLM prompt cache.
func TestBundleWithAttachedFile_AddsSectionAfterUserRequest(t *testing.T) {
	bundle := Build("execute the attached plan").WithAttachedFile(AttachedFile{
		Path:    "plan.md",
		Content: "## Plan\n\n1. do thing A\n2. do thing B",
	})
	if len(bundle.DynamicSections) < 2 {
		t.Fatalf("expected at least 2 dynamic sections, got %d", len(bundle.DynamicSections))
	}
	if bundle.DynamicSections[0].Name != "user_request" {
		t.Fatalf("expected user_request first, got %q", bundle.DynamicSections[0].Name)
	}
	if bundle.DynamicSections[1].Name != "attached_file" {
		t.Fatalf("expected attached_file right after user_request, got %q", bundle.DynamicSections[1].Name)
	}
	if !strings.Contains(bundle.DynamicSections[1].Content, "path: plan.md") {
		t.Fatalf("expected attached_file to start with path header, got %q", bundle.DynamicSections[1].Content)
	}
	if !strings.Contains(bundle.DynamicSections[1].Content, "## Plan\n\n1. do thing A\n2. do thing B") {
		t.Fatalf("expected attached_file to preserve multi-line content unchanged, got %q", bundle.DynamicSections[1].Content)
	}
	if !bundle.DynamicSections[1].CacheBreak {
		t.Fatalf("expected attached_file to be cache-break so different files don't poison the cache")
	}
}

// TODO-02 (P0): Empty path or empty content must short-circuit and not
// mutate the bundle. The defensive `if path == "" || content == ""` in
// `WithAttachedFile` is what prevents the avatar from getting an empty
// `attached_file:` header line if the caller passes an empty value.
func TestBundleWithAttachedFile_EmptyPathOrContentIsNoop(t *testing.T) {
	original := Build("user input")
	cases := []struct {
		label string
		file  AttachedFile
	}{
		{"empty_path", AttachedFile{Path: "", Content: "some content"}},
		{"whitespace_path", AttachedFile{Path: "   ", Content: "some content"}},
		{"empty_content", AttachedFile{Path: "plan.md", Content: ""}},
		{"whitespace_content", AttachedFile{Path: "plan.md", Content: "  \n\n  "}},
		{"both_empty", AttachedFile{Path: "", Content: ""}},
	}
	for _, tc := range cases {
		got := original.WithAttachedFile(tc.file)
		if len(got.DynamicSections) != len(original.DynamicSections) {
			t.Fatalf("%s: expected no section added, got %+v", tc.label, got.DynamicSections)
		}
	}
}

// TODO-02 (P0): Attached file sections must not displace the
// `restored_session_summary` / `restored_warm_lessons` / restored memory
// blocks that the resume path already injects. Insertion order is:
// user_request → attached_file → restored_session_summary → ... . This
// keeps the user's request + their attached evidence as the freshest
// top-of-prompt context, with restored memory below as the older
// historical layer.
func TestBundleWithAttachedFile_PreservesRestoredSectionOrder(t *testing.T) {
	bundle := BuildWithResume(
		"continue task",
		"stable summary",
		"task summary",
		[]string{"event A"},
		[]string{"lesson W"},
		[]string{"lesson P"},
		"verifier context",
	).WithAttachedFile(AttachedFile{Path: "context.md", Content: "## Context\n\nimportant evidence"})

	wantOrder := []string{
		"user_request",
		"attached_file",
		"restored_session_summary",
		"restored_task_summary",
		"restored_recent_events",
		"restored_warm_lessons",
		"restored_project_lessons",
		"restored_verification_context",
	}
	if len(bundle.DynamicSections) != len(wantOrder) {
		t.Fatalf("expected %d dynamic sections, got %d (%+v)", len(wantOrder), len(bundle.DynamicSections), bundle.DynamicSections)
	}
	for index, want := range wantOrder {
		if bundle.DynamicSections[index].Name != want {
			t.Fatalf("section[%d]: expected %q, got %q", index, want, bundle.DynamicSections[index].Name)
		}
	}
}

// === TODO-04 (P1) skill注入 tests ===
//
// TODO-04 fixes two related bugs in compactAlwaysOnSkill:
//   - body was collapsed to one line (oneLine) — losing all structure
//   - body was truncated at 1200 chars — losing most of the playbook
//
// The fix preserves newlines and bumps the cap to 4000 chars. The new
// capSkillContent helper is exercised through the public bundle path
// so we don't depend on unexported symbol tests.

func TestBundleWithAlwaysOnSkills_PreservesBodyNewlines(t *testing.T) {
	bundle := Build("analyze repository").WithAlwaysOnSkills([]AlwaysOnSkill{{
		Name:    "avatars-operating-contract",
		Context: "Governs clarification and memory reuse.",
		Body:    "## Rules\n\n1. Ask a short clarification when target is unclear.\n2. Reuse memory before rescanning.\n3. Surface evidence with file paths.",
	}})
	joined := strings.Join(bundle.StablePrefix, "\n")
	// The body must remain multi-line; the previous oneLine collapse would
	// have flattened the markdown headings into a single line.
	if !strings.Contains(joined, "## Rules\n") {
		t.Fatalf("expected body newlines preserved, got %q", joined)
	}
	if !strings.Contains(joined, "1. Ask a short clarification when target is unclear.\n2. Reuse memory before rescanning.") {
		t.Fatalf("expected numbered rules preserved as multi-line, got %q", joined)
	}
	// Old behavior would have truncated at 1200 chars; verify a >1500
	// char body is preserved in full.
	longBody := strings.Repeat("rule line.\n", 200) // ~2400 chars
	bundleLong := Build("analyze").WithAlwaysOnSkills([]AlwaysOnSkill{{
		Name: "long-skill",
		Body: longBody,
	}})
	joinedLong := strings.Join(bundleLong.StablePrefix, "\n")
	if !strings.Contains(joinedLong, "rule line.") {
		t.Fatalf("expected long body content to be present, got len=%d", len(joinedLong))
	}
	if !strings.Contains(joinedLong, "rule line.\nrule line.") {
		t.Fatalf("expected newlines preserved in long body, got %q", joinedLong[:min(200, len(joinedLong))])
	}
}

func TestBundleWithAlwaysOnSkills_CapsBodyAt4000Chars(t *testing.T) {
	body := strings.Repeat("x", 8000) // 8000 chars
	bundle := Build("analyze").WithAlwaysOnSkills([]AlwaysOnSkill{{
		Name: "huge-skill",
		Body: body,
	}})
	joined := strings.Join(bundle.StablePrefix, "\n")
	if !strings.Contains(joined, "[truncated: skill body exceeded 4000 chars]") {
		t.Fatalf("expected 4000-char truncation marker, got %q", joined[:min(300, len(joined))])
	}
	if strings.Count(joined, "x") > 4100 {
		t.Fatalf("expected at most ~4000 x's plus marker, got %d", strings.Count(joined, "x"))
	}
}

func TestBundleWithAlwaysOnSkills_PreservesContextNewlines(t *testing.T) {
	bundle := Build("analyze").WithAlwaysOnSkills([]AlwaysOnSkill{{
		Name:    "ctx-skill",
		Context: "line A\nline B\nline C",
		Body:    "rule",
	}})
	joined := strings.Join(bundle.StablePrefix, "\n")
	if !strings.Contains(joined, "context:\nline A\nline B\nline C") {
		t.Fatalf("expected context newlines preserved, got %q", joined)
	}
}

func TestWithREPLContext_InsertsAfterUserRequest(t *testing.T) {
	bundle := Build("hello")
	bundle = bundle.WithREPLContext("Turn 1: write a script\nTurn 2: fix the bug")
	if len(bundle.DynamicSections) < 2 {
		t.Fatalf("expected at least 2 dynamic sections, got %d", len(bundle.DynamicSections))
	}
	if bundle.DynamicSections[0].Name != "user_request" {
		t.Fatalf("expected first section to be user_request, got %q", bundle.DynamicSections[0].Name)
	}
	if bundle.DynamicSections[1].Name != "repl_context" {
		t.Fatalf("expected second section to be repl_context, got %q", bundle.DynamicSections[1].Name)
	}
	if !strings.Contains(bundle.DynamicSections[1].Content, "Turn 1") {
		t.Fatalf("expected repl_context to contain turn data, got %q", bundle.DynamicSections[1].Content)
	}
}

func TestWithREPLContext_EmptyContextIsNoop(t *testing.T) {
	bundle := Build("hello")
	original := len(bundle.DynamicSections)
	bundle = bundle.WithREPLContext("")
	if len(bundle.DynamicSections) != original {
		t.Fatalf("expected no new section for empty context, got %d sections", len(bundle.DynamicSections))
	}
	bundle = bundle.WithREPLContext("   ")
	if len(bundle.DynamicSections) != original {
		t.Fatalf("expected no new section for whitespace context, got %d sections", len(bundle.DynamicSections))
	}
}

func TestWithREPLContext_InsertsAfterAttachedFile(t *testing.T) {
	bundle := Build("hello")
	bundle = bundle.WithAttachedFile(AttachedFile{Path: "test.py", Content: "print(1)"})
	bundle = bundle.WithREPLContext("Turn 1: do something")
	// Order should be: user_request, attached_file, repl_context
	found := []string{}
	for _, s := range bundle.DynamicSections {
		found = append(found, s.Name)
	}
	expected := []string{"user_request", "attached_file", "repl_context"}
	if len(found) != len(expected) {
		t.Fatalf("expected %d sections, got %d: %v", len(expected), len(found), found)
	}
	for i, name := range expected {
		if found[i] != name {
			t.Fatalf("expected section[%d]=%q, got %q (all: %v)", i, name, found[i], found)
		}
	}
}

func TestWithWorkflowContext_DoesNotPrependStablePrefix(t *testing.T) {
	base := Build("implement tokenbucketx")
	prefixBefore := strings.Join(base.StablePrefix, "\n")
	got := base.WithWorkflowContext("## Plan\n- [ ] 1. write library\nStatus: in_progress")
	prefixAfter := strings.Join(got.StablePrefix, "\n")
	if prefixAfter != prefixBefore {
		t.Fatalf("workflow context must not mutate StablePrefix (provider prefix cache)")
	}
	if !strings.Contains(prefixAfter, "avatars bootstrap runtime") {
		t.Fatal("stable role prefix must remain first")
	}
	last := got.DynamicSections[len(got.DynamicSections)-1]
	if last.Name != "workflow_context" {
		t.Fatalf("expected workflow_context as last dynamic section, got %q", last.Name)
	}
	if !last.CacheBreak {
		t.Fatal("workflow_context must be CacheBreak so the tail can change")
	}
	if !strings.Contains(last.Content, "write library") {
		t.Fatalf("missing workflow body: %q", last.Content)
	}
	empty := base.WithWorkflowContext("  \n")
	if len(empty.DynamicSections) != len(base.DynamicSections) {
		t.Fatal("empty workflow context must be a no-op")
	}
}
