// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/planner"
	"avatars/internal/skillbuilder"
)

func TestStoreGenerateAndApprove(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if _, err := os.Stat(generatedPath); err != nil {
		t.Fatalf("expected generated skill file, got %v", err)
	}

	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if _, err := os.Stat(approvedPath); err != nil {
		t.Fatalf("expected approved skill file, got %v", err)
	}
	if _, err := os.Stat(generatedPath); !os.IsNotExist(err) {
		t.Fatalf("expected generated file to be moved away, got %v", err)
	}
	definition, err := store.LoadApproved(approvedPath)
	if err != nil {
		t.Fatalf("load approved failed: %v", err)
	}
	if len(definition.History) != 2 {
		t.Fatalf("expected 2 history entries after approve, got %d", len(definition.History))
	}
	if definition.History[0].Action != "generate" || definition.History[0].ToState != "generated" {
		t.Fatalf("unexpected first history entry: %+v", definition.History[0])
	}
	if definition.History[1].Action != "approve" || definition.History[1].FromState != "generated" || definition.History[1].ToState != "approved" {
		t.Fatalf("unexpected second history entry: %+v", definition.History[1])
	}
}

func TestStoreLifecycleHistory_PersistsAcrossTransitions(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	disabledPath, err := store.Disable(approvedPath)
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	restoredPath, err := store.Restore(disabledPath)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	archivedPath, err := store.Archive(restoredPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}

	definition, err := store.LoadInspect(archivedPath)
	if err != nil {
		t.Fatalf("load inspect failed: %v", err)
	}
	if len(definition.History) != 5 {
		t.Fatalf("expected 5 history entries, got %d", len(definition.History))
	}
	actions := []string{definition.History[0].Action, definition.History[1].Action, definition.History[2].Action, definition.History[3].Action, definition.History[4].Action}
	if strings.Join(actions, ",") != "generate,approve,disable,restore,archive" {
		t.Fatalf("unexpected history actions: %v", actions)
	}
	if definition.History[3].FromState != "disabled" || definition.History[3].ToState != "approved" {
		t.Fatalf("unexpected restore history entry: %+v", definition.History[3])
	}
	if definition.History[4].FromState != "approved" || definition.History[4].ToState != "archived" {
		t.Fatalf("unexpected archive history entry: %+v", definition.History[4])
	}
	if !strings.HasSuffix(definition.History[4].Path, filepath.Base(archivedPath)) {
		t.Fatalf("expected final history path to track archived file, got %s", definition.History[4].Path)
	}
}

func TestStoreArchive_MovesApprovedSkill(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	if _, err := os.Stat(archivedPath); err != nil {
		t.Fatalf("expected archived skill file, got %v", err)
	}
	if _, err := os.Stat(approvedPath); !os.IsNotExist(err) {
		t.Fatalf("expected approved file to be moved away, got %v", err)
	}

	definition, err := store.LoadInspect(archivedPath)
	if err != nil {
		t.Fatalf("load inspect failed: %v", err)
	}
	if definition.LifecycleState != "archived" {
		t.Fatalf("expected archived lifecycle state, got %q", definition.LifecycleState)
	}
	if definition.ArchivedAt.IsZero() {
		t.Fatal("expected archived timestamp to be persisted")
	}
}

func TestStoreRestore_MovesArchivedSkillBackToApproved(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	restoredPath, err := store.Restore(archivedPath)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if _, err := os.Stat(restoredPath); err != nil {
		t.Fatalf("expected restored approved skill file, got %v", err)
	}
	if _, err := os.Stat(archivedPath); !os.IsNotExist(err) {
		t.Fatalf("expected archived file to be moved away, got %v", err)
	}
	definition, err := store.LoadApproved(restoredPath)
	if err != nil {
		t.Fatalf("load approved failed: %v", err)
	}
	if definition.LifecycleState != "approved" {
		t.Fatalf("expected approved lifecycle state after restore, got %q", definition.LifecycleState)
	}
	if definition.ApprovedAt.IsZero() {
		t.Fatal("expected original approved timestamp to remain present")
	}
	if !definition.ArchivedAt.IsZero() {
		t.Fatal("expected archived timestamp to be cleared after restore")
	}
	if !definition.DisabledAt.IsZero() {
		t.Fatal("expected disabled timestamp to be cleared after restore")
	}
}

func TestStoreDisable_MovesApprovedSkillToDisabledState(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	disabledPath, err := store.Disable(approvedPath)
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	definition, err := store.LoadInspect(disabledPath)
	if err != nil {
		t.Fatalf("load inspect failed: %v", err)
	}
	if definition.LifecycleState != "disabled" {
		t.Fatalf("expected disabled lifecycle state, got %q", definition.LifecycleState)
	}
	if definition.DisabledAt.IsZero() {
		t.Fatal("expected disabled timestamp to be persisted")
	}
	if !definition.ArchivedAt.IsZero() {
		t.Fatal("expected archive timestamp to stay empty for disabled state")
	}
}

func TestStoreListApproved_ReadsListingFrontmatter(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if _, err := store.Approve(generatedPath); err != nil {
		t.Fatalf("approve failed: %v", err)
	}

	listings, err := store.ListApproved()
	if err != nil {
		t.Fatalf("list approved failed: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("expected 1 approved skill, got %d", len(listings))
	}
	if listings[0].Name != "Task Survey Skill" {
		t.Fatalf("expected Task Survey Skill, got %s", listings[0].Name)
	}
	if listings[0].WhenToUse == "" {
		t.Fatal("expected when_to_use to be parsed")
	}
	if len(listings[0].AllowedTools) != 1 || listings[0].AllowedTools[0] != "read" {
		t.Fatalf("expected allowed tools to contain read, got %+v", listings[0].AllowedTools)
	}
	if listings[0].LifecycleState != "approved" {
		t.Fatalf("expected approved lifecycle state, got %q", listings[0].LifecycleState)
	}
	if listings[0].ApprovedAt.IsZero() {
		t.Fatal("expected approved timestamp to be persisted")
	}
}

func TestStoreListGenerated_ReadsLifecycleMetadata(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	if _, err := store.Generate("run-42", "task-42", proposal); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	listings, err := store.ListGenerated()
	if err != nil {
		t.Fatalf("list generated failed: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("expected 1 generated skill, got %d", len(listings))
	}
	if listings[0].LifecycleState != "generated" {
		t.Fatalf("expected generated lifecycle state, got %q", listings[0].LifecycleState)
	}
	if listings[0].SourceRunID != "run-42" {
		t.Fatalf("expected source run id run-42, got %q", listings[0].SourceRunID)
	}
	if listings[0].SourceTaskID != "task-42" {
		t.Fatalf("expected source task id task-42, got %q", listings[0].SourceTaskID)
	}
	if listings[0].GeneratedAt.IsZero() {
		t.Fatal("expected generated timestamp to be persisted")
	}
}

func TestStoreLoadApproved_ReadsFullDefinition(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}

	definition, err := store.LoadApproved(approvedPath)
	if err != nil {
		t.Fatalf("load approved failed: %v", err)
	}
	if definition.Name != "Task Survey Skill" {
		t.Fatalf("expected Task Survey Skill, got %s", definition.Name)
	}
	if definition.Version != "0.1.0" {
		t.Fatalf("expected version 0.1.0, got %s", definition.Version)
	}
	if definition.TemplateID != "phase2-task-survey" {
		t.Fatalf("expected template id phase2-task-survey, got %s", definition.TemplateID)
	}
	if !strings.Contains(definition.Body, "## Suggested Workflow") {
		t.Fatalf("expected full body content, got %s", definition.Body)
	}
	if definition.DisableModelInvocation != true {
		t.Fatal("expected disable model invocation to be true")
	}
	if definition.AlwaysOn {
		t.Fatal("expected default Task Survey Skill not to be always-on")
	}
	if definition.LifecycleState != "approved" {
		t.Fatalf("expected lifecycle state approved, got %q", definition.LifecycleState)
	}
	if definition.SourceRunID != "run-1" {
		t.Fatalf("expected source run id run-1, got %q", definition.SourceRunID)
	}
	if definition.SourceTaskID != "task-1" {
		t.Fatalf("expected source task id task-1, got %q", definition.SourceTaskID)
	}
	if definition.GeneratedAt.IsZero() {
		t.Fatal("expected generated timestamp to be present")
	}
	if definition.ApprovedAt.IsZero() {
		t.Fatal("expected approved timestamp to be present")
	}
}

func TestStoreLoadApproved_ReadsAlwaysOnFlag(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	skillPath := filepath.Join(tempDir, "skills", "approved", "always-on.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("mkdir approved failed: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: avatars-operating-contract
description: persistent contract
when_to_use: always
context: keep replies human and use memory first
always-on: true
lifecycle-state: approved
allowed-tools:
  - read
---
body
`), 0o644); err != nil {
		t.Fatalf("write always-on skill failed: %v", err)
	}
	definition, err := store.LoadApproved(skillPath)
	if err != nil {
		t.Fatalf("load approved failed: %v", err)
	}
	if !definition.AlwaysOn {
		t.Fatal("expected always-on flag to be parsed")
	}
	if definition.Name != "avatars-operating-contract" {
		t.Fatalf("unexpected skill name %q", definition.Name)
	}
}

func TestStoreListArchived_ReadsLifecycleMetadata(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if _, err := store.Archive(approvedPath); err != nil {
		t.Fatalf("archive failed: %v", err)
	}

	listings, err := store.ListArchived()
	if err != nil {
		t.Fatalf("list archived failed: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("expected 1 archived skill, got %d", len(listings))
	}
	if listings[0].LifecycleState != "archived" {
		t.Fatalf("expected archived lifecycle state, got %q", listings[0].LifecycleState)
	}
	if listings[0].ArchivedAt.IsZero() {
		t.Fatal("expected archived timestamp to be persisted")
	}
}

func TestStoreListDisabled_ReadsLifecycleMetadata(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if _, err := store.Disable(approvedPath); err != nil {
		t.Fatalf("disable failed: %v", err)
	}

	listings, err := store.ListDisabled()
	if err != nil {
		t.Fatalf("list disabled failed: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("expected 1 disabled skill, got %d", len(listings))
	}
	if listings[0].LifecycleState != "disabled" {
		t.Fatalf("expected disabled lifecycle state, got %q", listings[0].LifecycleState)
	}
	if listings[0].DisabledAt.IsZero() {
		t.Fatal("expected disabled timestamp to be persisted")
	}
	archivedListings, err := store.ListArchived()
	if err != nil {
		t.Fatalf("list archived failed: %v", err)
	}
	if len(archivedListings) != 0 {
		t.Fatalf("expected archived listing to exclude disabled entries, got %d", len(archivedListings))
	}
}

func TestStoreReviewGenerated_MatchesApprovedReference(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	seedGeneratedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := store.Approve(seedGeneratedPath); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	generatedPath, err := store.Generate("run-2", "task-2", proposal)
	if err != nil {
		t.Fatalf("second generate failed: %v", err)
	}

	review, err := store.ReviewGenerated(generatedPath)
	if err != nil {
		t.Fatalf("review generated failed: %v", err)
	}
	if !review.HasReference {
		t.Fatal("expected review to find a governed reference")
	}
	if review.Reference.LifecycleState != "approved" {
		t.Fatalf("expected approved reference, got %q", review.Reference.LifecycleState)
	}
	if review.MatchReason != "template-id: phase2-task-survey" {
		t.Fatalf("expected template-id match reason, got %q", review.MatchReason)
	}
	if len(review.ChangedFields) != 0 {
		t.Fatalf("expected no field diffs, got %+v", review.ChangedFields)
	}
	if review.BodyChanged {
		t.Fatal("expected identical body for the regenerated candidate")
	}
}

func TestStoreReviewGenerated_FallsBackToDisabledReference(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	seedGeneratedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	approvedPath, err := store.Approve(seedGeneratedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if _, err := store.Disable(approvedPath); err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	generatedPath, err := store.Generate("run-2", "task-2", proposal)
	if err != nil {
		t.Fatalf("second generate failed: %v", err)
	}

	review, err := store.ReviewGenerated(generatedPath)
	if err != nil {
		t.Fatalf("review generated failed: %v", err)
	}
	if !review.HasReference {
		t.Fatal("expected review to find a disabled reference")
	}
	if review.Reference.LifecycleState != "disabled" {
		t.Fatalf("expected disabled reference, got %q", review.Reference.LifecycleState)
	}
}

func TestStoreReviewGenerated_ReportsBodyDiffLines(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	seedGeneratedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("seed generate failed: %v", err)
	}
	if _, err := store.Approve(seedGeneratedPath); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	generatedPath, err := store.Generate("run-2", "task-2", proposal)
	if err != nil {
		t.Fatalf("second generate failed: %v", err)
	}
	content, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("read generated file failed: %v", err)
	}
	updatedContent := strings.Replace(string(content), "- Keep output short and evidence-based.", "- Keep output very short and evidence-based.", 1)
	if err := os.WriteFile(generatedPath, []byte(updatedContent), 0o644); err != nil {
		t.Fatalf("write updated generated file failed: %v", err)
	}

	review, err := store.ReviewGenerated(generatedPath)
	if err != nil {
		t.Fatalf("review generated failed: %v", err)
	}
	if !review.BodyChanged {
		t.Fatal("expected body diff to be detected")
	}
	if len(review.BodyDiffLines) == 0 {
		t.Fatal("expected line-level body diff output")
	}
	joined := strings.Join(review.BodyDiffLines, "\n")
	if !strings.Contains(joined, "@@ -") {
		t.Fatalf("expected unified diff hunk header in diff output, got %s", joined)
	}
	if !strings.Contains(joined, "  - Do not self-activate.") {
		t.Fatalf("expected unchanged context line in diff output, got %s", joined)
	}
	if !strings.Contains(joined, "- - Keep output short and evidence-based.") {
		t.Fatalf("expected removed line in diff output, got %s", joined)
	}
	if !strings.Contains(joined, "+ - Keep output very short and evidence-based.") {
		t.Fatalf("expected added line in diff output, got %s", joined)
	}
}

func TestStoreStatus_ReportsLifecycleDirectories(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	secondGeneratedPath, err := store.Generate("run-2", "task-2", proposal)
	if err != nil {
		t.Fatalf("second generate failed: %v", err)
	}
	if approvedPath == secondGeneratedPath {
		t.Fatal("expected approved and generated files to differ")
	}

	statuses, err := store.Status()
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if len(statuses) != 4 {
		t.Fatalf("expected 4 lifecycle directories, got %d", len(statuses))
	}
	approved := findDirectoryStatus(statuses, "approved")
	if approved == nil || !approved.Active || approved.FileCount != 1 {
		t.Fatalf("expected active approved directory with 1 file, got %+v", approved)
	}
	generated := findDirectoryStatus(statuses, "generated")
	if generated == nil || !generated.Active || generated.FileCount != 1 {
		t.Fatalf("expected active generated directory with 1 file, got %+v", generated)
	}
	archive := findDirectoryStatus(statuses, "archive")
	if archive == nil || !archive.Active || archive.FileCount != 0 {
		t.Fatalf("expected active empty archive directory, got %+v", archive)
	}
	templates := findDirectoryStatus(statuses, "templates")
	if templates == nil || templates.Active || templates.FileCount != 0 {
		t.Fatalf("expected reserved empty templates directory, got %+v", templates)
	}
	if len(approved.Files) != 1 || len(generated.Files) != 1 {
		t.Fatalf("expected current file listings in status, got approved=%+v generated=%+v", approved, generated)
	}
}

func TestStoreGovernanceSummary_AggregatesCurrentStatesAndTransitions(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedOne, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("first generate failed: %v", err)
	}
	approvedOne, err := store.Approve(generatedOne)
	if err != nil {
		t.Fatalf("first approve failed: %v", err)
	}
	disabledOne, err := store.Disable(approvedOne)
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if _, err := store.Restore(disabledOne); err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	generatedTwo, err := store.Generate("run-2", "task-2", proposal)
	if err != nil {
		t.Fatalf("second generate failed: %v", err)
	}
	approvedTwo, err := store.Approve(generatedTwo)
	if err != nil {
		t.Fatalf("second approve failed: %v", err)
	}
	if _, err := store.Archive(approvedTwo); err != nil {
		t.Fatalf("archive failed: %v", err)
	}

	if _, err := store.Generate("run-3", "task-3", proposal); err != nil {
		t.Fatalf("third generate failed: %v", err)
	}

	summary, err := store.GovernanceSummary()
	if err != nil {
		t.Fatalf("governance summary failed: %v", err)
	}
	if summary.TrackedSkills != 3 {
		t.Fatalf("expected 3 tracked skills, got %d", summary.TrackedSkills)
	}
	if summary.GeneratedCount != 1 || summary.ApprovedCount != 1 || summary.ArchivedCount != 1 || summary.DisabledCount != 0 {
		t.Fatalf("unexpected state counts: %+v", summary)
	}
	if summary.TransitionCount != 8 || len(summary.Transitions) != 8 {
		t.Fatalf("expected 8 aggregated transitions, got count=%d len=%d", summary.TransitionCount, len(summary.Transitions))
	}
	if len(summary.Hotspots) != 3 {
		t.Fatalf("expected 3 hotspots, got %d", len(summary.Hotspots))
	}
	if summary.Hotspots[0].TransitionCount != 4 || summary.Hotspots[0].CurrentState != "approved" {
		t.Fatalf("expected highest-churn skill first, got %+v", summary.Hotspots[0])
	}
	for index := 1; index < len(summary.Transitions); index++ {
		if summary.Transitions[index-1].At.Before(summary.Transitions[index].At) {
			t.Fatalf("expected transitions to be sorted latest-first, got %v before %v", summary.Transitions[index-1].At, summary.Transitions[index].At)
		}
	}
	foundArchive := false
	foundRestore := false
	foundHighChurn := false
	for _, transition := range summary.Transitions {
		if transition.Action == "archive" && transition.CurrentState == "archived" {
			foundArchive = true
		}
		if transition.Action == "restore" && transition.FromState == "disabled" && transition.ToState == "approved" {
			foundRestore = true
		}
	}
	for _, alert := range summary.Alerts {
		if alert.Reason == "high-churn: 4 transitions" && alert.SkillName == "Task Survey Skill" {
			foundHighChurn = true
		}
	}
	if !foundArchive || !foundRestore {
		t.Fatalf("expected archive and restore transitions in summary, got %+v", summary.Transitions)
	}
	if !foundHighChurn {
		t.Fatalf("expected high-churn alert in summary, got %+v", summary.Alerts)
	}
}

func TestStoreGovernanceLedger_PersistsAfterSkillRemoval(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	if err := os.Remove(skillHistoryPath(archivedPath)); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	ledger, err := store.GovernanceLedger()
	if err != nil {
		t.Fatalf("governance ledger failed: %v", err)
	}
	if ledger.EventCount != 3 {
		t.Fatalf("expected 3 ledger events, got %d", ledger.EventCount)
	}
	if ledger.SkillsSeen != 1 {
		t.Fatalf("expected 1 ledger skill, got %d", ledger.SkillsSeen)
	}
	if len(ledger.Entries) != 3 {
		t.Fatalf("expected 3 ledger entries, got %d", len(ledger.Entries))
	}
	if ledger.Entries[0].Action != "archive" || ledger.Entries[0].ToState != "archived" {
		t.Fatalf("expected latest ledger entry to be archive, got %+v", ledger.Entries[0])
	}
	if ledger.Entries[2].Action != "generate" || ledger.Entries[2].ToState != "generated" {
		t.Fatalf("expected oldest ledger entry to be generate, got %+v", ledger.Entries[2])
	}
}

func TestStoreGovernanceReconciliation_FlagsMissingCurrentAndDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	missingGenerated, err := store.Generate("run-missing", "task-missing", proposal)
	if err != nil {
		t.Fatalf("missing generate failed: %v", err)
	}
	missingApproved, err := store.Approve(missingGenerated)
	if err != nil {
		t.Fatalf("missing approve failed: %v", err)
	}
	missingArchived, err := store.Archive(missingApproved)
	if err != nil {
		t.Fatalf("missing archive failed: %v", err)
	}
	if err := os.Remove(missingArchived); err != nil {
		t.Fatalf("remove missing archived skill failed: %v", err)
	}
	if err := os.Remove(skillHistoryPath(missingArchived)); err != nil {
		t.Fatalf("remove missing archived history failed: %v", err)
	}

	driftGenerated, err := store.Generate("run-drift", "task-drift", proposal)
	if err != nil {
		t.Fatalf("drift generate failed: %v", err)
	}
	driftApproved, err := store.Approve(driftGenerated)
	if err != nil {
		t.Fatalf("drift approve failed: %v", err)
	}
	driftContent, err := os.ReadFile(driftApproved)
	if err != nil {
		t.Fatalf("read drift approved skill failed: %v", err)
	}
	updatedDriftContent, err := rewriteFrontmatterScalars(string(driftContent), map[string]string{"lifecycle-state": "disabled", "disabled-at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("rewrite drift skill failed: %v", err)
	}
	if err := os.WriteFile(driftApproved, []byte(updatedDriftContent), 0o644); err != nil {
		t.Fatalf("write drift approved skill failed: %v", err)
	}

	pathGenerated, err := store.Generate("run-path", "task-path", proposal)
	if err != nil {
		t.Fatalf("path generate failed: %v", err)
	}
	pathApproved, err := store.Approve(pathGenerated)
	if err != nil {
		t.Fatalf("path approve failed: %v", err)
	}
	renamedPath := filepath.Join(filepath.Dir(pathApproved), "renamed-task-survey-skill.md")
	if err := os.Rename(pathApproved, renamedPath); err != nil {
		t.Fatalf("rename approved skill failed: %v", err)
	}
	if err := os.Rename(skillHistoryPath(pathApproved), skillHistoryPath(renamedPath)); err != nil {
		t.Fatalf("rename approved skill history failed: %v", err)
	}

	manualPath := filepath.Join(tempDir, "skills", "approved", "manual-skill.md")
	manualContent := renderSkillFile(parsedFrontmatter{scalars: map[string]string{
		"name":                     "Manual Skill",
		"description":              "Manual current skill without governance ledger.",
		"when_to_use":              "Use when checking reconciliation output for unmanaged current files.",
		"context":                  "inline",
		"user-invocable":           "false",
		"disable-model-invocation": "true",
		"version":                  "0.1.0",
		"lifecycle-state":          "approved",
	}, allowedTools: []string{"read"}}, "## Purpose\nKeep this file unmanaged for reconciliation coverage.")
	if err := os.WriteFile(manualPath, []byte(manualContent), 0o644); err != nil {
		t.Fatalf("write manual skill failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation failed: %v", err)
	}
	if report.CurrentSkills != 3 {
		t.Fatalf("expected 3 current skills, got %d", report.CurrentSkills)
	}
	if report.LedgerSkills != 3 {
		t.Fatalf("expected 3 ledger skills, got %d", report.LedgerSkills)
	}
	if len(report.MissingCurrent) != 1 || report.MissingCurrent[0].Action != "archive" {
		t.Fatalf("expected one missing current archive entry, got %+v", report.MissingCurrent)
	}
	if len(report.UntrackedCurrent) != 1 || report.UntrackedCurrent[0].SkillName != "Manual Skill" {
		t.Fatalf("expected one unmanaged current skill, got %+v", report.UntrackedCurrent)
	}
	if len(report.StateDrifts) != 1 || report.StateDrifts[0].LedgerState != "approved" || report.StateDrifts[0].CurrentState != "disabled" {
		t.Fatalf("expected one state drift, got %+v", report.StateDrifts)
	}
	if len(report.PathDrifts) != 1 || report.PathDrifts[0].LedgerPath == report.PathDrifts[0].CurrentPath {
		t.Fatalf("expected one path drift, got %+v", report.PathDrifts)
	}
}

func TestStoreGovernanceReconciliation_SeparatesLegacyGeneratedSkillsByPath(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	if err := store.ensureLayout(); err != nil {
		t.Fatalf("ensure layout failed: %v", err)
	}

	firstPath := filepath.Join(tempDir, "skills", "generated", "first-task-survey-skill.md")
	secondPath := filepath.Join(tempDir, "skills", "generated", "second-task-survey-skill.md")
	firstContent := renderSkillFile(parsedFrontmatter{scalars: map[string]string{
		"name":                     "Task Survey Skill",
		"description":              "Generated candidate skill for task-scoped repository survey and synthesis.",
		"when_to_use":              "Use when the runtime needs a task-scoped survey before broader multi-avatar execution.",
		"context":                  "inline",
		"user-invocable":           "false",
		"disable-model-invocation": "true",
		"version":                  "0.1.0",
		"lifecycle-state":          "generated",
	}, allowedTools: []string{"read"}}, "## Purpose\nFirst generated skill.")
	secondContent := renderSkillFile(parsedFrontmatter{scalars: map[string]string{
		"name":                     "Task Survey Skill",
		"description":              "Generated candidate skill for task-scoped repository survey and synthesis.",
		"when_to_use":              "Use when the runtime needs a task-scoped survey before broader multi-avatar execution.",
		"context":                  "inline",
		"user-invocable":           "false",
		"disable-model-invocation": "true",
		"version":                  "0.1.0",
		"lifecycle-state":          "generated",
	}, allowedTools: []string{"read"}}, "## Purpose\nSecond generated skill.")
	if err := os.WriteFile(firstPath, []byte(firstContent), 0o644); err != nil {
		t.Fatalf("write first generated skill failed: %v", err)
	}
	if err := os.WriteFile(secondPath, []byte(secondContent), 0o644); err != nil {
		t.Fatalf("write second generated skill failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation failed: %v", err)
	}
	if report.CurrentSkills != 2 {
		t.Fatalf("expected 2 current skills, got %d", report.CurrentSkills)
	}
	if len(report.UntrackedCurrent) != 2 {
		t.Fatalf("expected 2 untracked current skills, got %+v", report.UntrackedCurrent)
	}
	if report.UntrackedCurrent[0].Path == report.UntrackedCurrent[1].Path {
		t.Fatalf("expected separate current paths, got %+v", report.UntrackedCurrent)
	}
}

func TestStoreSyncGovernanceLedger_AdoptsCurrentAndClearsDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	missingGenerated, err := store.Generate("run-missing", "task-missing", proposal)
	if err != nil {
		t.Fatalf("missing generate failed: %v", err)
	}
	missingApproved, err := store.Approve(missingGenerated)
	if err != nil {
		t.Fatalf("missing approve failed: %v", err)
	}
	missingArchived, err := store.Archive(missingApproved)
	if err != nil {
		t.Fatalf("missing archive failed: %v", err)
	}
	if err := os.Remove(missingArchived); err != nil {
		t.Fatalf("remove missing archived skill failed: %v", err)
	}
	if err := os.Remove(skillHistoryPath(missingArchived)); err != nil {
		t.Fatalf("remove missing archived history failed: %v", err)
	}

	driftGenerated, err := store.Generate("run-drift", "task-drift", proposal)
	if err != nil {
		t.Fatalf("drift generate failed: %v", err)
	}
	driftApproved, err := store.Approve(driftGenerated)
	if err != nil {
		t.Fatalf("drift approve failed: %v", err)
	}
	driftContent, err := os.ReadFile(driftApproved)
	if err != nil {
		t.Fatalf("read drift approved skill failed: %v", err)
	}
	updatedDriftContent, err := rewriteFrontmatterScalars(string(driftContent), map[string]string{"lifecycle-state": "disabled", "disabled-at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("rewrite drift skill failed: %v", err)
	}
	if err := os.WriteFile(driftApproved, []byte(updatedDriftContent), 0o644); err != nil {
		t.Fatalf("write drift approved skill failed: %v", err)
	}

	pathGenerated, err := store.Generate("run-path", "task-path", proposal)
	if err != nil {
		t.Fatalf("path generate failed: %v", err)
	}
	pathApproved, err := store.Approve(pathGenerated)
	if err != nil {
		t.Fatalf("path approve failed: %v", err)
	}
	renamedPath := filepath.Join(filepath.Dir(pathApproved), "renamed-task-survey-skill.md")
	if err := os.Rename(pathApproved, renamedPath); err != nil {
		t.Fatalf("rename approved skill failed: %v", err)
	}
	if err := os.Rename(skillHistoryPath(pathApproved), skillHistoryPath(renamedPath)); err != nil {
		t.Fatalf("rename approved skill history failed: %v", err)
	}

	manualPath := filepath.Join(tempDir, "skills", "approved", "manual-skill.md")
	manualContent := renderSkillFile(parsedFrontmatter{scalars: map[string]string{
		"name":                     "Manual Skill",
		"description":              "Manual current skill without governance ledger.",
		"when_to_use":              "Use when checking ledger sync for unmanaged current files.",
		"context":                  "inline",
		"user-invocable":           "false",
		"disable-model-invocation": "true",
		"version":                  "0.1.0",
		"lifecycle-state":          "approved",
	}, allowedTools: []string{"read"}}, "## Purpose\nKeep this file unmanaged for ledger sync coverage.")
	if err := os.WriteFile(manualPath, []byte(manualContent), 0o644); err != nil {
		t.Fatalf("write manual skill failed: %v", err)
	}

	syncReport, err := store.SyncGovernanceLedger()
	if err != nil {
		t.Fatalf("sync governance ledger failed: %v", err)
	}
	if len(syncReport.AdoptedCurrent) != 1 || syncReport.AdoptedCurrent[0].Action != "adopt-current" {
		t.Fatalf("expected one adopted current entry, got %+v", syncReport.AdoptedCurrent)
	}
	if len(syncReport.SyncedCurrent) != 2 {
		t.Fatalf("expected two synced current entries, got %+v", syncReport.SyncedCurrent)
	}
	stateSyncs := 0
	pathSyncs := 0
	for _, entry := range syncReport.SyncedCurrent {
		if entry.StateChanged {
			stateSyncs++
		}
		if entry.PathChanged {
			pathSyncs++
		}
	}
	if stateSyncs != 1 || pathSyncs != 1 {
		t.Fatalf("expected one state sync and one path sync, got state=%d path=%d entries=%+v", stateSyncs, pathSyncs, syncReport.SyncedCurrent)
	}
	if len(syncReport.UnresolvedMissing) != 1 || syncReport.UnresolvedMissing[0].Action != "archive" {
		t.Fatalf("expected one unresolved missing entry, got %+v", syncReport.UnresolvedMissing)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation after sync failed: %v", err)
	}
	if len(report.MissingCurrent) != 1 {
		t.Fatalf("expected missing current to remain unresolved, got %+v", report.MissingCurrent)
	}
	if len(report.UntrackedCurrent) != 0 || len(report.StateDrifts) != 0 || len(report.PathDrifts) != 0 {
		t.Fatalf("expected sync to clear current-file drift, got untracked=%+v state=%+v path=%+v", report.UntrackedCurrent, report.StateDrifts, report.PathDrifts)
	}

	ledger, err := store.GovernanceLedger()
	if err != nil {
		t.Fatalf("governance ledger after sync failed: %v", err)
	}
	actions := make([]string, 0, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		actions = append(actions, entry.Action)
	}
	if !containsString(actions, "adopt-current") || !containsString(actions, "sync-current-state") || !containsString(actions, "sync-current-path") {
		t.Fatalf("expected sync actions in ledger, got %v", actions)
	}
}

func TestStoreResolveMissingCurrent_AcknowledgesLedgerOnlyHistory(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	missingGenerated, err := store.Generate("run-missing", "task-missing", proposal)
	if err != nil {
		t.Fatalf("missing generate failed: %v", err)
	}
	missingApproved, err := store.Approve(missingGenerated)
	if err != nil {
		t.Fatalf("missing approve failed: %v", err)
	}
	missingArchived, err := store.Archive(missingApproved)
	if err != nil {
		t.Fatalf("missing archive failed: %v", err)
	}
	if err := os.Remove(missingArchived); err != nil {
		t.Fatalf("remove missing archived skill failed: %v", err)
	}
	if err := os.Remove(skillHistoryPath(missingArchived)); err != nil {
		t.Fatalf("remove missing archived history failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation before resolve failed: %v", err)
	}
	if len(report.MissingCurrent) != 1 {
		t.Fatalf("expected one missing current before resolve, got %+v", report.MissingCurrent)
	}

	resolved, err := store.ResolveMissingCurrent(filepath.Base(missingArchived))
	if err != nil {
		t.Fatalf("resolve missing current failed: %v", err)
	}
	if resolved.Action != "resolve-missing" || resolved.FromState != "archived" || resolved.ToState != "missing" {
		t.Fatalf("unexpected resolved entry %+v", resolved)
	}

	report, err = store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation after resolve failed: %v", err)
	}
	if len(report.MissingCurrent) != 0 {
		t.Fatalf("expected no missing current after resolve, got %+v", report.MissingCurrent)
	}

	ledger, err := store.GovernanceLedger()
	if err != nil {
		t.Fatalf("governance ledger after resolve failed: %v", err)
	}
	if len(ledger.Entries) == 0 || ledger.Entries[0].Action != "resolve-missing" || ledger.Entries[0].ToState != "missing" {
		t.Fatalf("expected latest ledger entry to resolve missing current, got %+v", ledger.Entries)
	}
}

func TestStoreMissingCurrentRestoreGuide_UsesRecordedLedgerMetadata(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-missing", "task-missing", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	if err := os.Remove(skillHistoryPath(archivedPath)); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	guide, err := store.MissingCurrentRestoreGuide(filepath.Base(archivedPath))
	if err != nil {
		t.Fatalf("missing current restore guide failed: %v", err)
	}
	if guide.SkillName != "Task Survey Skill" {
		t.Fatalf("expected guided skill name, got %+v", guide)
	}
	if guide.RecordedPath != archivedPath || guide.LatestAction != "archive" || guide.LatestLifecycleState != "archived" {
		t.Fatalf("unexpected restore guide %+v", guide)
	}
	if guide.SourceRunID != "run-missing" || guide.SourceTaskID != "task-missing" {
		t.Fatalf("expected source identifiers in guide, got %+v", guide)
	}
	if guide.SuggestedReconcileCheck != "avatars skills reconcile" {
		t.Fatalf("expected reconcile follow-up, got %+v", guide)
	}
}

func TestStoreRestoreMissingCurrent_RestoresArtifactIntoRecordedPath(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-missing", "task-missing", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	archivedContent, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatalf("read archived skill failed: %v", err)
	}
	restoreSourcePath := filepath.Join(tempDir, "restore-source.md")
	if err := os.WriteFile(restoreSourcePath, archivedContent, 0o644); err != nil {
		t.Fatalf("write restore source failed: %v", err)
	}
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	if err := os.Remove(skillHistoryPath(archivedPath)); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	result, err := store.RestoreMissingCurrent(filepath.Base(archivedPath), restoreSourcePath)
	if err != nil {
		t.Fatalf("restore missing current failed: %v", err)
	}
	if result.RestoredPath != archivedPath || result.SourcePath != restoreSourcePath {
		t.Fatalf("unexpected restore result %+v", result)
	}
	if result.LifecycleState != "archived" || result.LedgerEntry.Action != "restore-missing-current" {
		t.Fatalf("unexpected restore ledger entry %+v", result.LedgerEntry)
	}
	if _, err := os.Stat(archivedPath); err != nil {
		t.Fatalf("expected restored missing skill file, got %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation after restore failed: %v", err)
	}
	if len(report.MissingCurrent) != 0 {
		t.Fatalf("expected missing current to clear after restore, got %+v", report.MissingCurrent)
	}

	ledger, err := store.GovernanceLedger()
	if err != nil {
		t.Fatalf("governance ledger after restore failed: %v", err)
	}
	if len(ledger.Entries) == 0 || ledger.Entries[0].Action != "restore-missing-current" {
		t.Fatalf("expected latest ledger entry to record restore, got %+v", ledger.Entries)
	}
}

func TestStoreGovernanceReconciliation_FlagsMetadataDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-metadata", "task-metadata", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	content, err := os.ReadFile(approvedPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	brokenContent, err := rewriteFrontmatterScalars(string(content), map[string]string{"approved-at": "", "source-run-id": "", "source-task-id": ""})
	if err != nil {
		t.Fatalf("rewrite approved skill metadata failed: %v", err)
	}
	if err := os.WriteFile(approvedPath, []byte(brokenContent), 0o644); err != nil {
		t.Fatalf("write broken approved skill failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation failed: %v", err)
	}
	if len(report.MetadataDrifts) != 3 {
		t.Fatalf("expected three metadata drifts, got %+v", report.MetadataDrifts)
	}
	if !containsMetadataField(report.MetadataDrifts, "ApprovedAt") || !containsMetadataField(report.MetadataDrifts, "SourceRunID") || !containsMetadataField(report.MetadataDrifts, "SourceTaskID") {
		t.Fatalf("expected approved/source metadata drift fields, got %+v", report.MetadataDrifts)
	}
}

func TestStoreRepairMetadata_RehydratesLifecycleMetadata(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-metadata", "task-metadata", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	content, err := os.ReadFile(approvedPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	brokenContent, err := rewriteFrontmatterScalars(string(content), map[string]string{"approved-at": "", "source-run-id": "", "source-task-id": ""})
	if err != nil {
		t.Fatalf("rewrite approved skill metadata failed: %v", err)
	}
	if err := os.WriteFile(approvedPath, []byte(brokenContent), 0o644); err != nil {
		t.Fatalf("write broken approved skill failed: %v", err)
	}

	result, err := store.RepairMetadata(approvedPath)
	if err != nil {
		t.Fatalf("repair metadata failed: %v", err)
	}
	if result.RepairedPath != approvedPath || result.LifecycleState != "approved" {
		t.Fatalf("unexpected repair result %+v", result)
	}
	if !containsString(result.UpdatedFields, "approved-at") || !containsString(result.UpdatedFields, "source-run-id") || !containsString(result.UpdatedFields, "source-task-id") {
		t.Fatalf("expected repaired metadata fields, got %+v", result.UpdatedFields)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation after repair failed: %v", err)
	}
	if len(report.MetadataDrifts) != 0 {
		t.Fatalf("expected metadata drift to clear after repair, got %+v", report.MetadataDrifts)
	}

	ledger, err := store.GovernanceLedger()
	if err != nil {
		t.Fatalf("governance ledger after repair failed: %v", err)
	}
	if len(ledger.Entries) == 0 || ledger.Entries[0].Action != "repair-current-metadata" {
		t.Fatalf("expected latest ledger entry to record metadata repair, got %+v", ledger.Entries)
	}
}

func TestStoreGovernanceReconciliation_FlagsHistoryDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-history", "task-history", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if err := os.Remove(skillHistoryPath(approvedPath)); err != nil {
		t.Fatalf("remove history sidecar failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation failed: %v", err)
	}
	if len(report.HistoryDrifts) != 1 {
		t.Fatalf("expected one history drift, got %+v", report.HistoryDrifts)
	}
	if report.HistoryDrifts[0].Reason != "missing-history" || report.HistoryDrifts[0].ExpectedTransitions != 2 || report.HistoryDrifts[0].ActualTransitions != 0 {
		t.Fatalf("unexpected history drift %+v", report.HistoryDrifts[0])
	}
}

func TestStoreRepairHistory_RebuildsSidecarFromLedger(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-history", "task-history", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	history, err := readSkillHistory(approvedPath)
	if err != nil {
		t.Fatalf("read approved history failed: %v", err)
	}
	if err := writeSkillHistory(approvedPath, history[:1]); err != nil {
		t.Fatalf("write truncated history failed: %v", err)
	}

	result, err := store.RepairHistory(approvedPath)
	if err != nil {
		t.Fatalf("repair history failed: %v", err)
	}
	if result.RepairedPath != approvedPath || result.LifecycleState != "approved" || result.RebuiltTransitions != 2 {
		t.Fatalf("unexpected repair history result %+v", result)
	}

	rebuiltHistory, err := readSkillHistory(approvedPath)
	if err != nil {
		t.Fatalf("read rebuilt history failed: %v", err)
	}
	if len(rebuiltHistory) != 2 || rebuiltHistory[1].Action != "approve" {
		t.Fatalf("unexpected rebuilt history %+v", rebuiltHistory)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation after history repair failed: %v", err)
	}
	if len(report.HistoryDrifts) != 0 {
		t.Fatalf("expected history drift to clear after repair, got %+v", report.HistoryDrifts)
	}

	ledger, err := store.GovernanceLedger()
	if err != nil {
		t.Fatalf("governance ledger after history repair failed: %v", err)
	}
	if len(ledger.Entries) == 0 || ledger.Entries[0].Action != "repair-current-history" {
		t.Fatalf("expected latest ledger entry to record history repair, got %+v", ledger.Entries)
	}
}

func TestStoreGovernanceReconciliation_FlagsCorruptedHistoryDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-corrupt-history", "task-corrupt-history", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if err := os.WriteFile(skillHistoryPath(approvedPath), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatalf("write corrupted history sidecar failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation failed: %v", err)
	}
	if len(report.HistoryDrifts) != 1 {
		t.Fatalf("expected one history drift, got %+v", report.HistoryDrifts)
	}
	if report.HistoryDrifts[0].Reason != "corrupted-history" || report.HistoryDrifts[0].ExpectedTransitions != 2 || report.HistoryDrifts[0].ActualLatestAction != "invalid" {
		t.Fatalf("unexpected corrupted history drift %+v", report.HistoryDrifts[0])
	}

	summary, err := store.GovernanceSummary()
	if err != nil {
		t.Fatalf("governance summary failed: %v", err)
	}
	if !containsGovernanceAlert(summary.Alerts, "corrupted-history") {
		t.Fatalf("expected corrupted-history alert, got %+v", summary.Alerts)
	}

	definition, err := store.LoadInspect(approvedPath)
	if err != nil {
		t.Fatalf("load inspect with corrupted history failed: %v", err)
	}
	if definition.HistoryError == "" || len(definition.History) != 0 {
		t.Fatalf("expected inspect definition to preserve history error, got %+v", definition)
	}
}

func TestStoreRepairHistory_OverwritesCorruptedSidecar(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-corrupt-history", "task-corrupt-history", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if err := os.WriteFile(skillHistoryPath(approvedPath), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatalf("write corrupted history sidecar failed: %v", err)
	}

	result, err := store.RepairHistory(approvedPath)
	if err != nil {
		t.Fatalf("repair corrupted history failed: %v", err)
	}
	if result.RebuiltTransitions != 2 {
		t.Fatalf("expected rebuilt transitions after corrupted repair, got %+v", result)
	}
	rebuiltHistory, err := readSkillHistory(approvedPath)
	if err != nil {
		t.Fatalf("read rebuilt corrupted history failed: %v", err)
	}
	if len(rebuiltHistory) != 2 || rebuiltHistory[1].Action != "approve" {
		t.Fatalf("unexpected rebuilt history after corrupted repair %+v", rebuiltHistory)
	}
}

func TestStoreGovernanceSummary_FlagsImpossibleLifecycleJump(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-invariants", "task-invariants", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	history, err := readSkillHistory(archivedPath)
	if err != nil {
		t.Fatalf("read archived history failed: %v", err)
	}
	brokenHistory := []SkillHistoryEntry{history[0], history[2]}
	if err := writeSkillHistory(archivedPath, brokenHistory); err != nil {
		t.Fatalf("write broken history failed: %v", err)
	}

	summary, err := store.GovernanceSummary()
	if err != nil {
		t.Fatalf("governance summary failed: %v", err)
	}
	if !containsGovernanceAlert(summary.Alerts, "broken-transition-chain: expected-from=generated actual-from=approved action=archive") {
		t.Fatalf("expected broken transition chain alert, got %+v", summary.Alerts)
	}
}

func TestStoreGovernanceReconciliation_FlagsInvariantDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-invariants-reconcile", "task-invariants-reconcile", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	history, err := readSkillHistory(archivedPath)
	if err != nil {
		t.Fatalf("read archived history failed: %v", err)
	}
	brokenHistory := []SkillHistoryEntry{history[0], history[2]}
	if err := writeSkillHistory(archivedPath, brokenHistory); err != nil {
		t.Fatalf("write broken history failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation failed: %v", err)
	}
	if len(report.InvariantDrifts) != 1 {
		t.Fatalf("expected 1 invariant drift, got %+v", report.InvariantDrifts)
	}
	if report.InvariantDrifts[0].Reason != "broken-transition-chain: expected-from=generated actual-from=approved action=archive" {
		t.Fatalf("unexpected invariant drift %+v", report.InvariantDrifts[0])
	}
}

func TestStoreRepairInvariants_RebuildsBrokenTransitionChain(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-repair-invariants", "task-repair-invariants", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	history, err := readSkillHistory(archivedPath)
	if err != nil {
		t.Fatalf("read archived history failed: %v", err)
	}
	brokenHistory := []SkillHistoryEntry{history[0], history[2]}
	if err := writeSkillHistory(archivedPath, brokenHistory); err != nil {
		t.Fatalf("write broken history failed: %v", err)
	}

	result, err := store.RepairInvariants(archivedPath)
	if err != nil {
		t.Fatalf("repair invariants failed: %v", err)
	}
	if result.LedgerEntry.Action != "repair-current-invariants" || result.RebuiltTransitions != 3 {
		t.Fatalf("unexpected invariant repair result %+v", result)
	}
	rebuiltHistory, err := readSkillHistory(archivedPath)
	if err != nil {
		t.Fatalf("read rebuilt invariant history failed: %v", err)
	}
	if len(rebuiltHistory) != 3 || rebuiltHistory[1].Action != "approve" || rebuiltHistory[2].Action != "archive" {
		t.Fatalf("unexpected rebuilt invariant history %+v", rebuiltHistory)
	}
	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("reconcile after invariant repair failed: %v", err)
	}
	if len(report.InvariantDrifts) != 0 {
		t.Fatalf("expected cleared invariant drifts, got %+v", report.InvariantDrifts)
	}
}

func TestStoreInvariantRepairAssessment_FlagsLedgerAnomalyAsNonRepairable(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-ledger-anomaly", "task-ledger-anomaly", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	history, err := readSkillHistory(archivedPath)
	if err != nil {
		t.Fatalf("read archived history failed: %v", err)
	}
	brokenHistory := []SkillHistoryEntry{history[0], history[2]}
	if err := writeSkillHistory(archivedPath, brokenHistory); err != nil {
		t.Fatalf("write broken history failed: %v", err)
	}
	definition, err := store.readDefinition(archivedPath)
	if err != nil {
		t.Fatalf("read definition failed: %v", err)
	}
	if err := store.appendGovernanceLedgerEntry(governanceLedgerEntryFromDefinition(definition, "archive", "generated", "archived", archivedPath, time.Now().UTC().Add(time.Minute))); err != nil {
		t.Fatalf("append anomalous ledger entry failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(report.InvariantDrifts) != 1 {
		t.Fatalf("expected one invariant drift, got %+v", report.InvariantDrifts)
	}
	if report.InvariantDrifts[0].Repairable {
		t.Fatalf("expected non-repairable invariant drift, got %+v", report.InvariantDrifts[0])
	}
	if !strings.HasPrefix(report.InvariantDrifts[0].BlockedBy, "ledger-broken-transition-chain:") {
		t.Fatalf("expected ledger anomaly blocker, got %+v", report.InvariantDrifts[0])
	}
	if report.InvariantDrifts[0].SuggestedFollowUp != "avatars skills ledger" {
		t.Fatalf("expected ledger follow-up, got %+v", report.InvariantDrifts[0])
	}
	_, err = store.RepairInvariants(archivedPath)
	if err == nil || !strings.Contains(err.Error(), "lifecycle invariant drift is not repairable from current governance ledger") {
		t.Fatalf("expected non-repairable invariant error, got %v", err)
	}
}

func TestStoreLoadInspect_ComputesGovernanceAlerts(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-show-alerts", "task-show-alerts", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	archivedPath, err := store.Archive(approvedPath)
	if err != nil {
		t.Fatalf("archive failed: %v", err)
	}
	history, err := readSkillHistory(archivedPath)
	if err != nil {
		t.Fatalf("read archived history failed: %v", err)
	}
	brokenHistory := []SkillHistoryEntry{history[0], history[2]}
	if err := writeSkillHistory(archivedPath, brokenHistory); err != nil {
		t.Fatalf("write broken history failed: %v", err)
	}

	definition, err := store.LoadInspect(archivedPath)
	if err != nil {
		t.Fatalf("load inspect failed: %v", err)
	}
	if len(definition.GovernanceAlerts) == 0 {
		t.Fatalf("expected governance alerts, got %+v", definition)
	}
	if definition.GovernanceAlerts[0].Reason != "broken-transition-chain: expected-from=generated actual-from=approved action=archive" {
		t.Fatalf("unexpected governance alerts %+v", definition.GovernanceAlerts)
	}
}

func TestStoreLoadInspect_SupportsGeneratedSkill(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-show-generated", "task-show-generated", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	definition, err := store.LoadInspect(generatedPath)
	if err != nil {
		t.Fatalf("load inspect generated failed: %v", err)
	}
	if definition.LifecycleState != "generated" {
		t.Fatalf("expected generated lifecycle state, got %+v", definition)
	}
	if len(definition.History) != 1 {
		t.Fatalf("expected generated history entry, got %+v", definition.History)
	}
}

func TestStoreGovernanceReconciliation_FlagsContentDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-content", "task-content", proposal)
	if err != nil {
		t.Fatalf("content generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("content approve failed: %v", err)
	}
	content, err := os.ReadFile(approvedPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	updatedContent := strings.Replace(string(content), "Read process_record.md successfully.", "Read process_record.md and summarize drift signals.", 1)
	if updatedContent == string(content) {
		t.Fatal("expected content drift replacement to change the skill body")
	}
	if err := os.WriteFile(approvedPath, []byte(updatedContent), 0o644); err != nil {
		t.Fatalf("write content drift skill failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation failed: %v", err)
	}
	if len(report.ContentDrifts) != 1 {
		t.Fatalf("expected one content drift, got %+v", report.ContentDrifts)
	}
	if report.ContentDrifts[0].SkillName != "Task Survey Skill" || report.ContentDrifts[0].CurrentDigest == report.ContentDrifts[0].LedgerDigest {
		t.Fatalf("expected differing content digests, got %+v", report.ContentDrifts[0])
	}
}

func TestStoreSyncGovernanceLedger_ClearsContentDrift(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-content", "task-content", proposal)
	if err != nil {
		t.Fatalf("content generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("content approve failed: %v", err)
	}
	content, err := os.ReadFile(approvedPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	updatedContent := strings.Replace(string(content), "Read process_record.md successfully.", "Read process_record.md and summarize drift signals.", 1)
	if updatedContent == string(content) {
		t.Fatal("expected content drift replacement to change the skill body")
	}
	if err := os.WriteFile(approvedPath, []byte(updatedContent), 0o644); err != nil {
		t.Fatalf("write content drift skill failed: %v", err)
	}

	report, err := store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation before sync failed: %v", err)
	}
	if len(report.ContentDrifts) != 1 {
		t.Fatalf("expected one content drift before sync, got %+v", report.ContentDrifts)
	}

	syncReport, err := store.SyncGovernanceLedger()
	if err != nil {
		t.Fatalf("sync governance ledger failed: %v", err)
	}
	if len(syncReport.SyncedCurrent) != 1 {
		t.Fatalf("expected one synced current entry, got %+v", syncReport.SyncedCurrent)
	}
	if !syncReport.SyncedCurrent[0].ContentChanged || syncReport.SyncedCurrent[0].StateChanged || syncReport.SyncedCurrent[0].PathChanged {
		t.Fatalf("expected content-only sync entry, got %+v", syncReport.SyncedCurrent[0])
	}
	if syncReport.SyncedCurrent[0].Entry.Action != "sync-current-content" {
		t.Fatalf("expected sync-current-content action, got %+v", syncReport.SyncedCurrent[0].Entry)
	}

	report, err = store.GovernanceReconciliation()
	if err != nil {
		t.Fatalf("governance reconciliation after sync failed: %v", err)
	}
	if len(report.ContentDrifts) != 0 {
		t.Fatalf("expected content drift to clear after sync, got %+v", report.ContentDrifts)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsMetadataField(drifts []GovernanceMetadataDrift, field string) bool {
	for _, drift := range drifts {
		if drift.Field == field {
			return true
		}
	}
	return false
}

func containsGovernanceAlert(alerts []GovernanceAlert, reason string) bool {
	for _, alert := range alerts {
		if alert.Reason == reason {
			return true
		}
	}
	return false
}

func findDirectoryStatus(statuses []DirectoryStatus, name string) *DirectoryStatus {
	for index := range statuses {
		if statuses[index].Name == name {
			return &statuses[index]
		}
	}
	return nil
}

func TestReviewGenerated_ReportsReadyForApprovalWhenValid(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-review", "task-review", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	review, err := store.ReviewGenerated(generatedPath)
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if review.Candidate.Name == "" {
		t.Fatal("expected review candidate to have a name")
	}
	if !review.ReadyForApproval {
		t.Fatalf("expected ReadyForApproval=true for valid generated skill, got findings=%v", review.ValidationFindings)
	}
}

func TestReviewGenerated_ApproveAfterReviewMovesToApproved(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-auto", "task-auto", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	review, err := store.ReviewGenerated(generatedPath)
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if !review.ReadyForApproval {
		t.Fatalf("expected ReadyForApproval=true, got findings=%v", review.ValidationFindings)
	}

	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if _, err := os.Stat(approvedPath); err != nil {
		t.Fatalf("expected approved skill file, got %v", err)
	}
	if _, err := os.Stat(generatedPath); !os.IsNotExist(err) {
		t.Fatalf("expected generated file to be moved away after approve, got %v", err)
	}
	definition, err := store.LoadApproved(approvedPath)
	if err != nil {
		t.Fatalf("load approved failed: %v", err)
	}
	if definition.LifecycleState != "approved" {
		t.Fatalf("expected lifecycle state approved, got %q", definition.LifecycleState)
	}
}

func TestReviewGenerated_NotReadyForApprovalWhenValidationFails(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))

	// Create a generated skill with a short description that will fail validation
	generatedDir := filepath.Join(tempDir, "skills", "generated")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatalf("mkdir generated failed: %v", err)
	}
	shortDescSkill := `---
name: short-desc-skill
description: too short
when_to_use: this is a short when to use description that should be okay
context: some context here
template-id: survey-v1
lifecycle-state: generated
generated-at: 2026-01-01T00:00:00Z
user-invocable: false
disable-model-invocation: true
allowed-tools:
  - read
---

## Purpose
Test purpose section that is long enough to pass validation checks.

## Task Focus
Focus on reviewing generated skills for validation completeness.

## Steps
1. Load the generated skill
2. Validate all required fields
3. Report findings

## Output Format
Validation report with findings list.
`
	shortPath := filepath.Join(generatedDir, "short_desc_SKILL.md")
	if err := os.WriteFile(shortPath, []byte(shortDescSkill), 0o644); err != nil {
		t.Fatalf("write short-desc skill failed: %v", err)
	}

	review, err := store.ReviewGenerated(filepath.Base(shortPath))
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if review.ReadyForApproval {
		t.Fatalf("expected ReadyForApproval=false for skill with short description, got findings=%v", review.ValidationFindings)
	}
	found := false
	for _, f := range review.ValidationFindings {
		if strings.Contains(f, "description is too short") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'description is too short' finding, got %v", review.ValidationFindings)
	}
}

func TestAutoApproveFlow_ReviewThenApproveMatchesManualApprove(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-flow", "task-flow", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Simulate the auto-approve logic from loop.go
	review, reviewErr := store.ReviewGenerated(generatedPath)
	if reviewErr != nil {
		t.Fatalf("review failed: %v", reviewErr)
	}
	if !review.ReadyForApproval {
		t.Fatalf("expected ReadyForApproval, got findings=%v", review.ValidationFindings)
	}
	approvedPath, approveErr := store.Approve(generatedPath)
	if approveErr != nil {
		t.Fatalf("approve failed: %v", approveErr)
	}

	definition, err := store.LoadApproved(approvedPath)
	if err != nil {
		t.Fatalf("load approved failed: %v", err)
	}
	if len(definition.History) != 2 {
		t.Fatalf("expected 2 history entries (generate + approve), got %d", len(definition.History))
	}
	if definition.History[1].Action != "approve" {
		t.Fatalf("expected second history entry action=approve, got %q", definition.History[1].Action)
	}
}

// --- TODO-17: Intent Routing Skill integration test ---

func TestStoreLoadApproved_IntentRoutingSkillParsesCorrectly(t *testing.T) {
	// Verify the Intent Routing Skill file on disk parses with correct metadata.
	skillPath := filepath.Join("..", "..", "skills", "approved", "Intent_Routing_Skill.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read Intent Routing Skill: %v (file must exist in skills/approved/)", err)
	}
	content := string(data)

	// Parse using the store.
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	approvedDir := filepath.Join(tempDir, "skills", "approved")
	if err := os.MkdirAll(approvedDir, 0o755); err != nil {
		t.Fatalf("mkdir approved: %v", err)
	}
	destPath := filepath.Join(approvedDir, "intent-routing_SKILL.md")
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	definition, err := store.LoadApproved(destPath)
	if err != nil {
		t.Fatalf("LoadApproved: %v", err)
	}
	if !definition.AlwaysOn {
		t.Fatal("expected Intent Routing Skill always-on=true, got false")
	}
	if definition.UserInvocable {
		t.Fatal("expected Intent Routing Skill user-invocable=false, got true")
	}
	if definition.LifecycleState != "approved" {
		t.Fatalf("expected lifecycle-state=approved, got %q", definition.LifecycleState)
	}
	if definition.Name != "intent-routing" {
		t.Fatalf("expected name=intent-routing, got %q", definition.Name)
	}
	// Verify routing content is present in the body.
	if !strings.Contains(content, "Script Path") {
		t.Fatal("Intent Routing Skill must contain 'Script Path' routing rule")
	}
	if !strings.Contains(content, "Edit Path") {
		t.Fatal("Intent Routing Skill must contain 'Edit Path' routing rule")
	}
	if !strings.Contains(content, "Anti-Patterns") {
		t.Fatal("Intent Routing Skill must contain Anti-Patterns section")
	}
}

// --- TODO-18: Plan Mode Skill integration test ---

func TestStoreLoadApproved_PlanModeSkillParsesCorrectly(t *testing.T) {
	skillPath := filepath.Join("..", "..", "skills", "approved", "Plan_Mode_Skill.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read Plan Mode Skill: %v (file must exist in skills/approved/)", err)
	}
	content := string(data)

	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	approvedDir := filepath.Join(tempDir, "skills", "approved")
	if err := os.MkdirAll(approvedDir, 0o755); err != nil {
		t.Fatalf("mkdir approved: %v", err)
	}
	destPath := filepath.Join(approvedDir, "plan-mode_SKILL.md")
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	definition, err := store.LoadApproved(destPath)
	if err != nil {
		t.Fatalf("LoadApproved: %v", err)
	}
	if !definition.AlwaysOn {
		t.Fatal("expected Plan Mode Skill always-on=true, got false")
	}
	if definition.UserInvocable {
		t.Fatal("expected Plan Mode Skill user-invocable=false, got true")
	}
	if definition.LifecycleState != "approved" {
		t.Fatalf("expected lifecycle-state=approved, got %q", definition.LifecycleState)
	}
	if definition.Name != "plan-mode" {
		t.Fatalf("expected name=plan-mode, got %q", definition.Name)
	}
	// Verify plan mode content is present.
	if !strings.Contains(content, "read-only") {
		t.Fatal("Plan Mode Skill must describe read-only boundary")
	}
	if !strings.Contains(content, "script --apply") {
		t.Fatal("Plan Mode Skill must reference script --apply fast path")
	}
	if !strings.Contains(content, "Anti-Patterns") {
		t.Fatal("Plan Mode Skill must contain Anti-Patterns section")
	}
}

// --- TODO-19: Navigator Skill auto-regeneration ---

func TestStoreRegenerateNavigator_WritesNavigatorFile(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	if err := os.MkdirAll(filepath.Join(tempDir, "skills", "approved"), 0o755); err != nil {
		t.Fatalf("mkdir approved: %v", err)
	}

	// Create two approved skills.
	skill1Content := `---
name: test-skill-1
description: First test skill for navigator.
when_to_use: Always.
context: Test context.
version: 0.1.0
lifecycle-state: approved
always-on: true
allowed-tools:
  - read
---
Body 1.
`
	skill1Path := filepath.Join(tempDir, "skills", "approved", "test1_SKILL.md")
	if err := os.WriteFile(skill1Path, []byte(skill1Content), 0o644); err != nil {
		t.Fatalf("write skill1: %v", err)
	}

	skill2Content := `---
name: test-skill-2
description: Second test skill for navigator.
when_to_use: On demand.
context: Test context 2.
version: 0.1.0
lifecycle-state: approved
always-on: false
allowed-tools:
  - read
---
Body 2.
`
	skill2Path := filepath.Join(tempDir, "skills", "approved", "test2_SKILL.md")
	if err := os.WriteFile(skill2Path, []byte(skill2Content), 0o644); err != nil {
		t.Fatalf("write skill2: %v", err)
	}

	// Regenerate navigator.
	if err := store.RegenerateNavigator(); err != nil {
		t.Fatalf("RegenerateNavigator: %v", err)
	}

	// Verify navigator file exists and contains both skills.
	navPath := filepath.Join(tempDir, "skills", "approved", NavigatorSkillFilename)
	data, err := os.ReadFile(navPath)
	if err != nil {
		t.Fatalf("read navigator: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "test-skill-1") {
		t.Fatal("navigator must contain test-skill-1")
	}
	if !strings.Contains(content, "test-skill-2") {
		t.Fatal("navigator must contain test-skill-2")
	}
	if !strings.Contains(content, "always-on: true") {
		t.Fatal("navigator must have always-on: true in frontmatter")
	}
	if !strings.Contains(content, "skills-navigator") {
		t.Fatal("navigator must have name: skills-navigator")
	}
}

func TestStoreApprove_RegeneratesNavigator(t *testing.T) {
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))
	proposal := skillbuilder.Build("Test the navigator regeneration on approve", planner.Build("Test the navigator regeneration on approve"), "Read process_record.md successfully.")

	generatedPath, err := store.Generate("run-nav-test", "task-nav-test", proposal)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Approve the skill.
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := os.Stat(approvedPath); err != nil {
		t.Fatalf("approved skill file missing: %v", err)
	}

	// Manually regenerate the navigator (separate step, not auto-called by Approve).
	if err := store.RegenerateNavigator(); err != nil {
		t.Fatalf("RegenerateNavigator: %v", err)
	}

	// Verify navigator file was created.
	navPath := filepath.Join(tempDir, "skills", "approved", NavigatorSkillFilename)
	data, err := os.ReadFile(navPath)
	if err != nil {
		t.Fatalf("navigator file should exist after RegenerateNavigator: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "skills-navigator") {
		t.Fatal("navigator must contain skills-navigator name")
	}
	// The navigator should list the newly approved skill.
	if !strings.Contains(content, "task-survey-skill") {
		t.Fatal("navigator should list the newly approved skill")
	}
}

func TestSelfHealBuiltinSkills_EmptyDir(t *testing.T) {
	// When the approved directory is empty and no marker exists,
	// SelfHealBuiltinSkills should write the built-in always-on skills,
	// regenerate the navigator, and write a marker file.
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))

	count, err := store.SelfHealBuiltinSkills()
	if err != nil {
		t.Fatalf("SelfHealBuiltinSkills: %v", err)
	}
	if count != len(BuiltInSkillNames()) {
		t.Fatalf("expected %d skills written, got %d", len(BuiltInSkillNames()), count)
	}

	// Verify the marker file was created.
	markerPath := filepath.Join(tempDir, "skills", builtinHealedMarker)
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected marker file %s to exist: %v", builtinHealedMarker, err)
	}

	// Verify idempotency: calling again should return 0 (marker exists).
	count2, err := store.SelfHealBuiltinSkills()
	if err != nil {
		t.Fatalf("second SelfHealBuiltinSkills: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("expected 0 on second call (idempotent), got %d", count2)
	}

	// Verify both skills exist and are valid.
	names := BuiltInSkillNames()
	if len(names) < 2 {
		t.Fatalf("expected at least 2 builtin skill names, got %d", len(names))
	}

	for _, name := range names {
		skillPath := filepath.Join(tempDir, "skills", "approved", name)
		if _, err := os.Stat(skillPath); err != nil {
			t.Fatalf("expected builtin skill %s to exist: %v", name, err)
		}
		definition, err := store.LoadApproved(skillPath)
		if err != nil {
			t.Fatalf("LoadApproved(%s): %v", name, err)
		}
		if !definition.AlwaysOn {
			t.Fatalf("expected builtin skill %s always-on=true, got false", name)
		}
		if definition.LifecycleState != "approved" {
			t.Fatalf("expected lifecycle-state=approved for %s, got %q", name, definition.LifecycleState)
		}
	}

	// Verify the navigator was regenerated.
	navPath := filepath.Join(tempDir, "skills", "approved", NavigatorSkillFilename)
	data, err := os.ReadFile(navPath)
	if err != nil {
		t.Fatalf("navigator file should exist after self-heal: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "intent-routing") {
		t.Fatal("navigator should list intent-routing after self-heal")
	}
	if !strings.Contains(content, "plan-mode") {
		t.Fatal("navigator should list plan-mode after self-heal")
	}
}

func TestSelfHealBuiltinSkills_ExistingSkills_NoOverwrite(t *testing.T) {
	// When approved directory already has skills, SelfHealBuiltinSkills should
	// NOT overwrite or add anything.
	tempDir := t.TempDir()
	store := NewStore(filepath.Join(tempDir, "skills"))

	// Pre-create an approved skill to make the directory non-empty.
	proposal := skillbuilder.Build("test skill", planner.Build("test skill"), "test description")
	generatedPath, err := store.Generate("run-1", "task-1", proposal)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	approvedPath, err := store.Approve(generatedPath)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}

	count, err := store.SelfHealBuiltinSkills()
	if err != nil {
		t.Fatalf("SelfHealBuiltinSkills: %v", err)
	}
	// P0-7c: Self-heal now checks each builtin individually and writes
	// missing ones regardless of whether other skills exist.
	if count < len(BuiltInSkillNames()) {
		t.Fatalf("expected %d builtin skills written when missing, got %d", len(BuiltInSkillNames()), count)
	}

	// Verify built-in skills WERE written (new behavior).
	for _, name := range BuiltInSkillNames() {
		path := filepath.Join(tempDir, "skills", "approved", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Fatalf("builtin skill %s should exist after self-heal", name)
		}
	}

	// Verify the originally approved skill is still valid and unchanged.
	definition, err := store.LoadApproved(approvedPath)
	if err != nil {
		t.Fatalf("LoadApproved: %v", err)
	}
	if definition.Name == "" {
		t.Fatal("expected existing skill to have a name, got empty")
	}
}

func TestSelfHealBuiltinSkills_NilStore(t *testing.T) {
	var store *Store
	count, err := store.SelfHealBuiltinSkills()
	if err == nil {
		t.Fatal("expected error for nil store")
	}
	if count != 0 {
		t.Fatalf("expected 0 count for nil store, got %d", count)
	}
}
