package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/planner"
	"avatars/internal/skillbuilder"
	"avatars/internal/skills"
)

// TestEvolutionProposalPersistsToGeneratedDir verifies that when the runtime's
// planning phase calls into the skills store with a Proposal (the "evolution"
// candidate), the file actually lands in <root>/generated/ with the right
// filename pattern, the right frontmatter, and is discoverable via
// ListGenerated(). This is the persistence half of the evolution pipeline —
// without it, a skill proposal is just an event with no artifact to review.
//
// Replicates the loop.go:358-383 path: build proposal via skillbuilder.Build,
// then e.skills.Generate(runID, taskID, proposal). The store call is the
// single point that writes to disk, appends history, and emits the governance
// ledger entry, so testing the store call is equivalent to testing the
// persistence side of the runtime's planning-time skill proposal.
func TestEvolutionProposalPersistsToGeneratedDir(t *testing.T) {
	tempDir := t.TempDir()
	storeRoot := filepath.Join(tempDir, "skills")
	skillStore := skills.NewStore(storeRoot)
	generatedRoot := filepath.Join(storeRoot, "generated")

	input := "Analyze the current repository and propose a refactoring plan"
	plan := planner.Build(input)
	repoSummary := "Read README.md, configs/agent.yaml, and cmd/avatars/main.go successfully."
	proposal := skillbuilder.Build(input, plan, repoSummary)

	if proposal.Slug == "" {
		t.Fatalf("proposal slug is empty, expected non-empty slug for filename")
	}

	const runID = "evolution-run-001"
	const taskID = "evolution-task-001"
	generatedPath, err := skillStore.Generate(runID, taskID, proposal)
	if err != nil {
		t.Fatalf("skillStore.Generate returned error: %v", err)
	}

	// 1. The returned path must be inside <root>/generated/.
	relToRoot, relErr := filepath.Rel(storeRoot, generatedPath)
	if relErr != nil {
		t.Fatalf("generated path %q is not inside store root %q: %v", generatedPath, storeRoot, relErr)
	}
	expectedPrefix := "generated" + string(os.PathSeparator)
	if !strings.HasPrefix(relToRoot, expectedPrefix) {
		t.Fatalf("generated path %q should be inside <root>/generated/, got relative %q", generatedPath, relToRoot)
	}

	// 2. The filename must follow the <sanitized-runID>-<sanitized-taskID>-<slug>.md pattern.
	expectedName := runID + "-" + taskID + "-" + proposal.Slug + ".md"
	if filepath.Base(generatedPath) != expectedName {
		t.Fatalf("expected filename %q, got %q", expectedName, filepath.Base(generatedPath))
	}

	// 3. The file must exist on disk.
	if _, statErr := os.Stat(generatedPath); statErr != nil {
		t.Fatalf("generated file does not exist on disk: %v", statErr)
	}

	// 4. The file content must contain the frontmatter scalars that
	// PrepareGenerated writes: lifecycle-state=generated, source-run-id, source-task-id.
	contentBytes, readErr := os.ReadFile(generatedPath)
	if readErr != nil {
		t.Fatalf("read generated file: %v", readErr)
	}
	content := string(contentBytes)
	for _, must := range []string{
		"lifecycle-state: generated",
		"source-run-id: " + runID,
		"source-task-id: " + taskID,
		"template-id: " + proposal.TemplateID,
		proposal.Name,
		proposal.Description,
	} {
		if !strings.Contains(content, must) {
			t.Fatalf("generated file is missing required fragment %q\n--- content ---\n%s\n--- end ---", must, content)
		}
	}

	// 5. ListGenerated() must surface the file so a user running
	// `avatars skills pending` can see it without scanning the filesystem.
	listings, listErr := skillStore.ListGenerated()
	if listErr != nil {
		t.Fatalf("ListGenerated returned error: %v", listErr)
	}
	found := false
	for _, listing := range listings {
		if listing.Path == generatedPath {
			found = true
			if listing.Name != proposal.Name {
				t.Fatalf("listing.Name = %q, want %q", listing.Name, proposal.Name)
			}
			if listing.LifecycleState != "generated" {
				t.Fatalf("listing.LifecycleState = %q, want %q", listing.LifecycleState, "generated")
			}
			if listing.SourceRunID != runID {
				t.Fatalf("listing.SourceRunID = %q, want %q", listing.SourceRunID, runID)
			}
			if listing.SourceTaskID != taskID {
				t.Fatalf("listing.SourceTaskID = %q, want %q", listing.SourceTaskID, taskID)
			}
			break
		}
	}
	if !found {
		t.Fatalf("ListGenerated did not return the new file at %q; got %d listings", generatedPath, len(listings))
	}

	// 6. Calling Generate a second time with the same runID/taskID must not
	// fail (idempotent on the store side) and must continue to return a
	// file in the generated dir. This guards against a future regression
	// where someone re-introduces the emit-only path that wrote no file.
	secondPath, err := skillStore.Generate(runID, taskID, proposal)
	if err != nil {
		t.Fatalf("second skillStore.Generate returned error: %v", err)
	}
	secondRel, secondRelErr := filepath.Rel(storeRoot, secondPath)
	if secondRelErr != nil || !strings.HasPrefix(secondRel, expectedPrefix) {
		t.Fatalf("second generated path %q should be inside %q (got rel %q, err %v)", secondPath, generatedRoot, secondRel, secondRelErr)
	}
	// Second call may rewrite the same file (the frontmatter rewrite is a
	// no-op when lifecycle-state is already generated) or land in a new
	// path; either way the file must be present.
	if _, statErr := os.Stat(secondPath); statErr != nil {
		t.Fatalf("second generated file does not exist on disk: %v", statErr)
	}
}

// TestEvolutionProposalFromAttachedFilePersistsToGeneratedDir is the
// TODO-10 success-criteria #1+#3 test for the @file path. It mirrors
// TestEvolutionProposalPersistsToGeneratedDir above but routes the
// proposal through skillbuilder.BuildForAttachedFile (the function the
// runtime now uses when an attached file is present, see
// internal/runtime/loop.go:367-381 and 1893-1905). The contract is:
//
//   - The proposal Slug is derived from the attached file's basename
//     (NOT the hardcoded "task-survey-skill").
//   - The proposal Name comes from the file's first `# Heading`.
//   - The proposal Body contains the file's content (or an excerpt).
//   - The generated file on disk reflects all of the above in its
//     frontmatter and body.
//   - ListGenerated() returns the file with all metadata populated.
//
// This is the regression test for the user's complaint:
// "@svg-canvas-js-hybrid-animation_SKILL.md 把它修改为 avatars可用的skills格式
// → output was unrelated generic 'Task Survey Skill'".
func TestEvolutionProposalFromAttachedFilePersistsToGeneratedDir(t *testing.T) {
	tempDir := t.TempDir()
	storeRoot := filepath.Join(tempDir, "skills")
	skillStore := skills.NewStore(storeRoot)
	generatedRoot := filepath.Join(storeRoot, "generated")

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

	input := "把它修改为 avatars可用的skills格式"
	plan := planner.Build(input)
	proposal := skillbuilder.BuildForAttachedFile(
		input,
		plan,
		skillbuilder.AttachedFile{Path: attachedPath, Content: attachedContent},
	)

	// Pre-flight: the proposal must reflect the attached file BEFORE
	// we even call Generate. This is the unit-level proof that the
	// runtime's call to BuildForAttachedFile will produce a
	// content-aware proposal.
	if proposal.Slug == "task-survey-skill" {
		t.Fatalf("proposal.Slug must NOT be the hardcoded template; got %q", proposal.Slug)
	}
	if !strings.Contains(proposal.Slug, "svg-canvas") {
		t.Fatalf("proposal.Slug should contain file basename 'svg-canvas'; got %q", proposal.Slug)
	}
	if !strings.Contains(proposal.Name, "SVG + Canvas") {
		t.Fatalf("proposal.Name should come from the file's first heading; got %q", proposal.Name)
	}
	if !strings.Contains(proposal.Description, "combine SVG, Canvas") {
		t.Fatalf("proposal.Description should come from file content; got %q", proposal.Description)
	}

	const runID = "evolution-run-002"
	const taskID = "evolution-task-002"
	generatedPath, err := skillStore.Generate(runID, taskID, proposal)
	if err != nil {
		t.Fatalf("skillStore.Generate returned error: %v", err)
	}

	// 1. Path inside <root>/generated/.
	relToRoot, relErr := filepath.Rel(storeRoot, generatedPath)
	if relErr != nil {
		t.Fatalf("generated path %q is not inside store root %q: %v", generatedPath, storeRoot, relErr)
	}
	expectedPrefix := "generated" + string(os.PathSeparator)
	if !strings.HasPrefix(relToRoot, expectedPrefix) {
		t.Fatalf("generated path %q should be inside %q, got relative %q", generatedPath, generatedRoot, relToRoot)
	}

	// 2. Filename includes the file-derived slug, NOT the hardcoded
	//    "task-survey-skill" slug.
	expectedName := runID + "-" + taskID + "-" + proposal.Slug + ".md"
	if filepath.Base(generatedPath) != expectedName {
		t.Fatalf("expected filename %q, got %q", expectedName, filepath.Base(generatedPath))
	}
	if strings.Contains(generatedPath, "task-survey-skill") {
		t.Fatalf("generated filename should not contain the hardcoded slug; got %q", generatedPath)
	}

	// 3. File content includes the proposal's name, description, and
	//    the file's content excerpt in the body.
	contentBytes, readErr := os.ReadFile(generatedPath)
	if readErr != nil {
		t.Fatalf("read generated file: %v", readErr)
	}
	content := string(contentBytes)
	for _, must := range []string{
		"lifecycle-state: generated",
		"source-run-id: " + runID,
		"source-task-id: " + taskID,
		"template-id: phase2-attached-file-conversion",
		proposal.Name,
		proposal.Description,
		"## Source File",
		"Path: " + attachedPath,
		"## Source Excerpt",
		"SVG + Canvas + JS 混合动画开发",
		"combine SVG, Canvas",
	} {
		if !strings.Contains(content, must) {
			t.Fatalf("generated file missing required fragment %q\n--- content ---\n%s\n--- end ---", must, content)
		}
	}

	// 4. The file must NOT contain the old hardcoded "Task Survey Skill"
	//    Name or its description text — the whole point of this fix.
	for _, mustNot := range []string{
		"name: Task Survey Skill",
		"task-scoped repository survey and synthesis",
	} {
		if strings.Contains(content, mustNot) {
			t.Fatalf("generated file should not contain hardcoded template fragment %q\n--- content ---\n%s\n--- end ---", mustNot, content)
		}
	}

	// 5. ListGenerated() surfaces the file with file-derived metadata.
	listings, listErr := skillStore.ListGenerated()
	if listErr != nil {
		t.Fatalf("ListGenerated returned error: %v", listErr)
	}
	found := false
	for _, listing := range listings {
		if listing.Path == generatedPath {
			found = true
			if listing.Name != proposal.Name {
				t.Fatalf("listing.Name = %q, want %q", listing.Name, proposal.Name)
			}
			// TemplateID lives on the embedded Definition, not on the
			// bare Listing returned by ListGenerated. The file itself
			// already asserts the frontmatter template-id (see #3
			// above), so we don't need to re-check it here.
			if listing.LifecycleState != "generated" {
				t.Fatalf("listing.LifecycleState = %q, want %q", listing.LifecycleState, "generated")
			}
			break
		}
	}
	if !found {
		t.Fatalf("ListGenerated did not return the new file at %q; got %d listings", generatedPath, len(listings))
	}
}

