package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/app"
	"avatars/internal/planner"
	"avatars/internal/runtime"
	"avatars/internal/skillbuilder"
	"avatars/internal/tasks"
	"avatars/internal/tools"
)

func TestPluginsCLIAccessesLocalMarketplaceAndSkills(t *testing.T) {
	tempDir := t.TempDir()
	pluginRoot := filepath.Join(tempDir, "caveman", "plugins", "caveman")
	if err := os.MkdirAll(filepath.Join(pluginRoot, ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", "caveman"), 0o755); err != nil {
		t.Fatalf("mkdir skills dir failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".agents", "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".agents", "plugins", "marketplace.json"), []byte(`{
  "name": "my-project-marketplace",
  "plugins": [
    {
      "name": "caveman",
      "source": { "source": "local", "path": "../../caveman/plugins/caveman" },
      "policy": { "installation": "AVAILABLE" },
      "category": "Productivity"
    }
  ]
}`), 0o644); err != nil {
		t.Fatalf("write marketplace failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), []byte(`{
  "name": "caveman",
  "version": "0.1.0",
  "description": "Ultra-compressed communication mode.",
  "skills": "./skills/"
}`), 0o644); err != nil {
		t.Fatalf("write plugin manifest failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "skills", "caveman", "SKILL.md"), []byte(`---
name: caveman
description: ultra compressed mode
when_to_use: use when terse output is required
---
Say less.
`), 0o644); err != nil {
		t.Fatalf("write plugin skill failed: %v", err)
	}
	t.Setenv("AVATARS_HOME", tempDir)

	pluginsOutput := captureRunOutput(t, []string{"plugins", "list"})
	if !strings.Contains(pluginsOutput, "Plugins: 1") || !strings.Contains(pluginsOutput, "Skills: 1") {
		t.Fatalf("expected plugin registry counts, got %s", pluginsOutput)
	}
	if !strings.Contains(pluginsOutput, "caveman") || !strings.Contains(pluginsOutput, "skills=1") {
		t.Fatalf("expected plugin details, got %s", pluginsOutput)
	}

	skillsOutput := captureRunOutput(t, []string{"skills", "list"})
	if !strings.Contains(skillsOutput, "Plugin skills:") || !strings.Contains(skillsOutput, "caveman:caveman") {
		t.Fatalf("expected plugin skill to appear in skills list, got %s", skillsOutput)
	}
}

func TestTasksCLI_ApproveReplayFinalizesGeneratedSkillCandidate(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/generatedskill\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatalf("write go.mod failed: %v", err)
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := manager.Resolve(tasks.ResolveOptions{ID: "demo-task", Title: "Demo task"})
	if err != nil {
		t.Fatalf("resolve workspace failed: %v", err)
	}
	application, err := app.BootstrapWithOptions(app.BootstrapOptions{Workspace: &workspace, PermissionMode: runtime.PermissionModeDefault, AllowInvalidLLMConfig: true})
	if err != nil {
		t.Fatalf("bootstrap app failed: %v", err)
	}
	prepared, err := application.Skills.PrepareGenerated("run-generated", workspace.ID, skillbuilder.Build("Analyze the current repository and propose a refactoring plan", planner.Build("Analyze the current repository and propose a refactoring plan"), "Read process_record.md successfully."))
	if err != nil {
		_ = application.Close()
		t.Fatalf("prepare generated skill failed: %v", err)
	}
	_, err = application.Engine.CallTool(context.Background(), "write", "file_write", tools.WriteInput{Path: filepath.ToSlash(prepared.Path), Content: prepared.Content, WorkingDir: tempDir})
	_ = application.Close()
	if err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("expected initial generated skill write to await approval, got %v", err)
	}
	approveOutput := captureRunOutput(t, []string{"tasks", "approve", "demo-task", "--replay"})
	if !strings.Contains(approveOutput, "approval granted") {
		t.Fatalf("expected approve output, got %s", approveOutput)
	}
	if _, err := os.Stat(prepared.Path); err != nil {
		t.Fatalf("expected generated skill markdown after continuation, got %v", err)
	}
	historyPath := strings.TrimSuffix(prepared.Path, filepath.Ext(prepared.Path)) + ".history.json"
	if _, err := os.Stat(historyPath); err != nil {
		t.Fatalf("expected generated skill history after continuation finalize, got %v", err)
	}
}

func TestSkillsCLI_StatusReportsLifecycleDirectories(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	statusOutput := captureRunOutput(t, []string{"skills", "status"})

	if !strings.Contains(statusOutput, "Directory: approved") {
		t.Fatalf("expected approved directory status, got %s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Directory: generated") {
		t.Fatalf("expected generated directory status, got %s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Directory: archive") || !strings.Contains(statusOutput, "Mode: active") {
		t.Fatalf("expected active archive directory status, got %s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Directory: templates") {
		t.Fatalf("expected templates directory status, got %s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Candidate skills generated by runtime runs and waiting for explicit approval.") {
		t.Fatalf("expected generated purpose text, got %s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Activated skills that the runtime can list, load, and invoke.") {
		t.Fatalf("expected approved purpose text, got %s", statusOutput)
	}
	if !strings.Contains(statusOutput, "Retired or disabled skills preserved for audit and rollback review; excluded from the runtime-loaded registry.") {
		t.Fatalf("expected archive purpose text, got %s", statusOutput)
	}
	if !strings.Contains(statusOutput, "task-survey-skill.md") {
		t.Fatalf("expected current generated skill file to be listed, got %s", statusOutput)
	}
}

func TestSkillsCLI_PendingAndApprovedMetadata(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	writeBuiltinHealedMarker(t)
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	pendingOutput := captureRunOutput(t, []string{"skills", "pending"})
	if !strings.Contains(pendingOutput, "generated=") {
		t.Fatalf("expected pending output to include generation timestamp, got %s", pendingOutput)
	}
	if !strings.Contains(pendingOutput, "task=") || !strings.Contains(pendingOutput, "run=") {
		t.Fatalf("expected pending output to include source task and run ids, got %s", pendingOutput)
	}

	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	if len(generatedEntries) != 1 {
		t.Fatalf("expected 1 generated skill file, got %d", len(generatedEntries))
	}
	approveOutput := captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	if !strings.Contains(approveOutput, "Approved skill:") {
		t.Fatalf("expected approve confirmation, got %s", approveOutput)
	}
	listOutput := captureRunOutput(t, []string{"skills", "list"})
	if !strings.Contains(listOutput, "approved=") {
		t.Fatalf("expected approved list output to include approval timestamp, got %s", listOutput)
	}
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	if len(approvedEntries) != 1 {
		t.Fatalf("expected 1 approved skill file, got %d", len(approvedEntries))
	}
	showOutput := captureRunOutput(t, []string{"skills", "show", approvedEntries[0].Name()})
	if !strings.Contains(showOutput, "LifecycleState: approved") {
		t.Fatalf("expected show output to include approved lifecycle state, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "GeneratedAt:") || !strings.Contains(showOutput, "ApprovedAt:") {
		t.Fatalf("expected show output to include lifecycle timestamps, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "SourceTaskID:") || !strings.Contains(showOutput, "SourceRunID:") {
		t.Fatalf("expected show output to include source ids, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "HistoryEntries: 2") || !strings.Contains(showOutput, "TransitionHistory:") {
		t.Fatalf("expected show output to include lifecycle history, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "GovernanceAlerts: none") {
		t.Fatalf("expected show output to report no governance alerts, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "SuggestedFollowUp: none") {
		t.Fatalf("expected show output to report no follow-up, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "| generate | unknown -> generated |") || !strings.Contains(showOutput, "| approve | generated -> approved |") {
		t.Fatalf("expected show output to include generate and approve history entries, got %s", showOutput)
	}
}

func TestSkillsCLI_PromotionQueue(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "--task", "demo-task", "Analyze the current repository and propose a refactoring plan"})
	output := captureRunOutput(t, []string{"skills", "promotion", "--task", "demo-task"})
	if !strings.Contains(output, "Task: demo-task") {
		t.Fatalf("expected task header in promotion output, got %s", output)
	}
	if !strings.Contains(output, "Skill promotions:") {
		t.Fatalf("expected promotion queue count, got %s", output)
	}
	if !strings.Contains(output, "Skill promotion mode: advisory-only; no approval, activation, or skill file mutation is performed.") {
		t.Fatalf("expected advisory-only guardrail, got %s", output)
	}
	if !strings.Contains(output, "Priority high:") || !strings.Contains(output, "Skill promotion queue:") {
		t.Fatalf("expected priority summary and queue entries, got %s", output)
	}
}

func TestSkillsCLI_ShowSupportsGeneratedSkill(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	if len(generatedEntries) != 1 {
		t.Fatalf("expected 1 generated skill file, got %d", len(generatedEntries))
	}

	showOutput := captureRunOutput(t, []string{"skills", "show", generatedEntries[0].Name()})
	if !strings.Contains(showOutput, "LifecycleState: generated") {
		t.Fatalf("expected generated lifecycle state, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "HistoryEntries: 1") || !strings.Contains(showOutput, "| generate | unknown -> generated |") {
		t.Fatalf("expected generated history entry, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "GovernanceAlerts: none") || !strings.Contains(showOutput, "SuggestedFollowUp: none") {
		t.Fatalf("expected clean generated inspection output, got %s", showOutput)
	}
}

func TestSkillsCLI_ReviewGeneratedCandidate(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	firstGeneratedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read first generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", firstGeneratedEntries[0].Name()})
	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	if len(generatedEntries) != 1 {
		t.Fatalf("expected 1 generated skill file, got %d", len(generatedEntries))
	}
	reviewOutput := captureRunOutput(t, []string{"skills", "review", generatedEntries[0].Name()})
	if !strings.Contains(reviewOutput, "MatchedReference:") {
		t.Fatalf("expected matched reference output, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "MatchReason: template-id: phase2-task-survey") {
		t.Fatalf("expected template-id review reason, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "ChangedFields: 0") {
		t.Fatalf("expected no field diffs for identical candidate, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "BodyChanged: no") {
		t.Fatalf("expected unchanged body summary, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "BodyDiffLines: 0") {
		t.Fatalf("expected zero body diff lines for identical candidate, got %s", reviewOutput)
	}
}

func TestSkillsCLI_ReviewGeneratedCandidate_ShowsBodyDiffLines(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	firstGeneratedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read first generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", firstGeneratedEntries[0].Name()})
	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	generatedPath := filepath.Join("skills", "generated", generatedEntries[0].Name())
	content, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("read generated file failed: %v", err)
	}
	updatedContent := strings.Replace(string(content), "- Keep output short and evidence-based.", "- Keep output very short and evidence-based.", 1)
	if err := os.WriteFile(generatedPath, []byte(updatedContent), 0o644); err != nil {
		t.Fatalf("write updated generated file failed: %v", err)
	}
	reviewOutput := captureRunOutput(t, []string{"skills", "review", generatedEntries[0].Name()})
	if !strings.Contains(reviewOutput, "BodyChanged: yes") {
		t.Fatalf("expected changed body summary, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "BodyDiff:") {
		t.Fatalf("expected body diff section, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "@@ -") {
		t.Fatalf("expected unified diff hunk header in review output, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "  - Do not self-activate.") {
		t.Fatalf("expected context line in review output, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "- - Keep output short and evidence-based.") {
		t.Fatalf("expected removed body line in review output, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "+ - Keep output very short and evidence-based.") {
		t.Fatalf("expected added body line in review output, got %s", reviewOutput)
	}
}

func TestSkillsCLI_ArchiveAndArchivedListing(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	writeBuiltinHealedMarker(t)
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	if len(generatedEntries) != 1 {
		t.Fatalf("expected 1 generated skill file, got %d", len(generatedEntries))
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	if len(approvedEntries) != 1 {
		t.Fatalf("expected 1 approved skill file, got %d", len(approvedEntries))
	}
	archiveOutput := captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	if !strings.Contains(archiveOutput, "Archived skill:") {
		t.Fatalf("expected archive confirmation, got %s", archiveOutput)
	}
	archivedListOutput := captureRunOutput(t, []string{"skills", "archived"})
	if !strings.Contains(archivedListOutput, "archived=") {
		t.Fatalf("expected archived list output to include archive timestamp, got %s", archivedListOutput)
	}
	archivedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	if len(archivedEntries) != 1 {
		t.Fatalf("expected 1 archived skill file, got %d", len(archivedEntries))
	}
	showOutput := captureRunOutput(t, []string{"skills", "show", archivedEntries[0].Name()})
	if !strings.Contains(showOutput, "LifecycleState: archived") {
		t.Fatalf("expected show output to include archived lifecycle state, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "ArchivedAt:") {
		t.Fatalf("expected show output to include archive timestamp, got %s", showOutput)
	}
	if entries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved")); err != nil || len(entries) != 0 {
		t.Fatalf("expected approved directory to be empty after archive, err=%v len=%d", err, len(entries))
	}
}

func TestSkillsCLI_RestoreArchivedSkill(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	writeBuiltinHealedMarker(t)
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archivedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	if len(archivedEntries) != 1 {
		t.Fatalf("expected 1 archived skill file, got %d", len(archivedEntries))
	}
	restoreOutput := captureRunOutput(t, []string{"skills", "restore", archivedEntries[0].Name()})
	if !strings.Contains(restoreOutput, "Restored skill:") {
		t.Fatalf("expected restore confirmation, got %s", restoreOutput)
	}
	listOutput := captureRunOutput(t, []string{"skills", "list"})
	if !strings.Contains(listOutput, "approved=") {
		t.Fatalf("expected approved listing after restore, got %s", listOutput)
	}
	archivedListOutput := captureRunOutput(t, []string{"skills", "archived"})
	if !strings.Contains(archivedListOutput, "No archived skills found.") {
		t.Fatalf("expected archive listing to be empty after restore, got %s", archivedListOutput)
	}
	restoredEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read restored approved skills failed: %v", err)
	}
	if len(restoredEntries) != 1 {
		t.Fatalf("expected 1 restored approved skill file, got %d", len(restoredEntries))
	}
	showOutput := captureRunOutput(t, []string{"skills", "show", restoredEntries[0].Name()})
	if !strings.Contains(showOutput, "LifecycleState: approved") {
		t.Fatalf("expected restored skill to report approved lifecycle state, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "ArchivedAt: unknown") {
		t.Fatalf("expected archived timestamp to be cleared after restore, got %s", showOutput)
	}
}

func TestSkillsCLI_DisableAndRestoreDisabledSkill(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	writeBuiltinHealedMarker(t)
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	disableOutput := captureRunOutput(t, []string{"skills", "disable", approvedEntries[0].Name()})
	if !strings.Contains(disableOutput, "Disabled skill:") {
		t.Fatalf("expected disable confirmation, got %s", disableOutput)
	}
	disabledOutput := captureRunOutput(t, []string{"skills", "disabled"})
	if !strings.Contains(disabledOutput, "disabled=") {
		t.Fatalf("expected disabled listing to include disabled timestamp, got %s", disabledOutput)
	}
	archivedOutput := captureRunOutput(t, []string{"skills", "archived"})
	if !strings.Contains(archivedOutput, "No archived skills found.") {
		t.Fatalf("expected archived listing to exclude disabled entries, got %s", archivedOutput)
	}
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archive skills failed: %v", err)
	}
	if len(archiveEntries) != 1 {
		t.Fatalf("expected 1 disabled skill file in archive, got %d", len(archiveEntries))
	}
	showOutput := captureRunOutput(t, []string{"skills", "show", archiveEntries[0].Name()})
	if !strings.Contains(showOutput, "LifecycleState: disabled") {
		t.Fatalf("expected show output to include disabled lifecycle state, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "DisabledAt:") {
		t.Fatalf("expected show output to include disabled timestamp, got %s", showOutput)
	}
	restoreOutput := captureRunOutput(t, []string{"skills", "restore", archiveEntries[0].Name()})
	if !strings.Contains(restoreOutput, "Restored skill:") {
		t.Fatalf("expected restore confirmation, got %s", restoreOutput)
	}
	restoredEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read restored approved skills failed: %v", err)
	}
	if len(restoredEntries) != 1 {
		t.Fatalf("expected 1 restored approved skill file, got %d", len(restoredEntries))
	}
	restoredShow := captureRunOutput(t, []string{"skills", "show", restoredEntries[0].Name()})
	if !strings.Contains(restoredShow, "DisabledAt: unknown") {
		t.Fatalf("expected disabled timestamp to be cleared after restore, got %s", restoredShow)
	}
}

func TestSkillsCLI_TimelineReportsCountsAndTransitions(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	writeBuiltinHealedMarker(t)
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "disable", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archive skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "restore", archiveEntries[0].Name()})
	restoredEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read restored approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", restoredEntries[0].Name()})
	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})

	timelineOutput := captureRunOutput(t, []string{"skills", "timeline"})
	if !strings.Contains(timelineOutput, "TrackedSkills: 2") {
		t.Fatalf("expected tracked skills count, got %s", timelineOutput)
	}
	if !strings.Contains(timelineOutput, "Generated: 1") || !strings.Contains(timelineOutput, "Approved: 0") || !strings.Contains(timelineOutput, "Archived: 1") || !strings.Contains(timelineOutput, "Disabled: 0") {
		t.Fatalf("expected current state counts, got %s", timelineOutput)
	}
	if !strings.Contains(timelineOutput, "Transitions: 6") {
		t.Fatalf("expected transition count, got %s", timelineOutput)
	}
	if !strings.Contains(timelineOutput, "ChurnHotspots:") || !strings.Contains(timelineOutput, "Task Survey Skill | transitions=5 | current=archived") {
		t.Fatalf("expected churn hotspot output, got %s", timelineOutput)
	}
	if !strings.Contains(timelineOutput, "GovernanceAlerts:") || !strings.Contains(timelineOutput, "high-churn: 5 transitions") {
		t.Fatalf("expected high-churn governance alert, got %s", timelineOutput)
	}
	if !strings.Contains(timelineOutput, "RecentTransitions:") {
		t.Fatalf("expected transition section, got %s", timelineOutput)
	}
	if !strings.Contains(timelineOutput, "| archive | approved -> archived | Task Survey Skill | current=archived |") {
		t.Fatalf("expected archive transition in timeline output, got %s", timelineOutput)
	}
	if !strings.Contains(timelineOutput, "| generate | unknown -> generated | Task Survey Skill | current=generated |") {
		t.Fatalf("expected generated transition in timeline output, got %s", timelineOutput)
	}
}

func TestSkillsCLI_LedgerPersistsAfterSkillRemoval(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	if err := os.Remove(strings.TrimSuffix(archivedPath, filepath.Ext(archivedPath)) + ".history.json"); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	ledgerOutput := captureRunOutput(t, []string{"skills", "ledger"})
	if !strings.Contains(ledgerOutput, "Events: 3") {
		t.Fatalf("expected ledger event count, got %s", ledgerOutput)
	}
	if !strings.Contains(ledgerOutput, "SkillsSeen: 1") {
		t.Fatalf("expected ledger skill count, got %s", ledgerOutput)
	}
	if !strings.Contains(ledgerOutput, "LedgerEvents:") {
		t.Fatalf("expected ledger events section, got %s", ledgerOutput)
	}
	if !strings.Contains(ledgerOutput, "| archive | approved -> archived | Task Survey Skill |") {
		t.Fatalf("expected archive ledger entry, got %s", ledgerOutput)
	}
	if !strings.Contains(ledgerOutput, "| generate | unknown -> generated | Task Survey Skill |") {
		t.Fatalf("expected generate ledger entry, got %s", ledgerOutput)
	}
}

func TestSkillsCLI_ReconcileReportsMissingAndUntrackedCurrent(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	writeBuiltinHealedMarker(t)
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	if err := os.Remove(strings.TrimSuffix(archivedPath, filepath.Ext(archivedPath)) + ".history.json"); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}
	manualPath := filepath.Join("skills", "approved", "manual-skill.md")
	manualContent := "---\nname: Manual Skill\ndescription: Manual current skill without ledger history.\nwhen_to_use: Use when testing reconciliation output.\ncontext: inline\nuser-invocable: false\ndisable-model-invocation: true\nversion: 0.1.0\nlifecycle-state: approved\nallowed-tools:\n  - read\n---\n\n## Purpose\nKeep this skill unmanaged for reconciliation coverage.\n"
	if err := os.WriteFile(manualPath, []byte(manualContent), 0o644); err != nil {
		t.Fatalf("write manual skill failed: %v", err)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "MissingCurrent: 1") {
		t.Fatalf("expected missing current count, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "UntrackedCurrent: 1") {
		t.Fatalf("expected untracked current count, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "MissingCurrentEntries:") || !strings.Contains(reconcileOutput, "| archive | approved -> archived | Task Survey Skill |") {
		t.Fatalf("expected missing current details, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "UntrackedCurrentEntries:") || !strings.Contains(reconcileOutput, "Manual Skill | current=approved |") {
		t.Fatalf("expected untracked current details, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "StateDriftEntries: none") || !strings.Contains(reconcileOutput, "PathDriftEntries: none") {
		t.Fatalf("expected no state/path drift entries in this scenario, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_SyncAdoptsCurrentAndClearsDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	writeBuiltinHealedMarker(t)
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	missingGeneratedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", missingGeneratedEntries[0].Name()})
	missingApprovedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", missingApprovedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	archivedHistoryPath := strings.TrimSuffix(archivedPath, filepath.Ext(archivedPath)) + ".history.json"
	if err := os.Remove(archivedHistoryPath); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	driftGeneratedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read second generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", driftGeneratedEntries[len(driftGeneratedEntries)-1].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills for drift setup failed: %v", err)
	}
	driftPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	driftContent, err := os.ReadFile(driftPath)
	if err != nil {
		t.Fatalf("read drift skill failed: %v", err)
	}
	updatedDriftContent := strings.Replace(string(driftContent), "lifecycle-state: approved", "lifecycle-state: disabled", 1)
	if updatedDriftContent == string(driftContent) {
		t.Fatal("expected drift skill lifecycle state replacement to change content")
	}
	updatedDriftContent = strings.Replace(updatedDriftContent, "approved-at:", "disabled-at: 2026-04-30T00:00:00Z\napproved-at:", 1)
	if err := os.WriteFile(driftPath, []byte(updatedDriftContent), 0o644); err != nil {
		t.Fatalf("write drift skill failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	pathGeneratedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read third generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", pathGeneratedEntries[len(pathGeneratedEntries)-1].Name()})
	approvedEntries, err = readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills for path setup failed: %v", err)
	}
	var pathSkillName string
	for _, entry := range approvedEntries {
		if entry.Name() != filepath.Base(driftPath) {
			pathSkillName = entry.Name()
			break
		}
	}
	if pathSkillName == "" {
		t.Fatal("expected a second approved skill for path drift setup")
	}
	pathSkillPath := filepath.Join("skills", "approved", pathSkillName)
	renamedPath := filepath.Join("skills", "approved", "renamed-"+pathSkillName)
	if err := os.Rename(pathSkillPath, renamedPath); err != nil {
		t.Fatalf("rename approved skill failed: %v", err)
	}
	pathHistoryPath := strings.TrimSuffix(pathSkillPath, filepath.Ext(pathSkillPath)) + ".history.json"
	renamedHistoryPath := strings.TrimSuffix(renamedPath, filepath.Ext(renamedPath)) + ".history.json"
	if err := os.Rename(pathHistoryPath, renamedHistoryPath); err != nil {
		t.Fatalf("rename approved skill history failed: %v", err)
	}

	manualPath := filepath.Join("skills", "approved", "manual-skill.md")
	manualContent := "---\nname: Manual Skill\ndescription: Manual current skill without ledger history.\nwhen_to_use: Use when testing ledger sync output.\ncontext: inline\nuser-invocable: false\ndisable-model-invocation: true\nversion: 0.1.0\nlifecycle-state: approved\nallowed-tools:\n  - read\n---\n\n## Purpose\nKeep this skill unmanaged for ledger sync coverage.\n"
	if err := os.WriteFile(manualPath, []byte(manualContent), 0o644); err != nil {
		t.Fatalf("write manual skill failed: %v", err)
	}

	syncOutput := captureRunOutput(t, []string{"skills", "sync"})
	if !strings.Contains(syncOutput, "AdoptedCurrent: 1") {
		t.Fatalf("expected adopted current count, got %s", syncOutput)
	}
	if !strings.Contains(syncOutput, "SyncedCurrent: 2") {
		t.Fatalf("expected synced current count, got %s", syncOutput)
	}
	if !strings.Contains(syncOutput, "UnresolvedMissingCurrent: 1") {
		t.Fatalf("expected unresolved missing count, got %s", syncOutput)
	}
	if !strings.Contains(syncOutput, "adopt-current | Manual Skill | current=approved |") {
		t.Fatalf("expected adopted current details, got %s", syncOutput)
	}
	if !strings.Contains(syncOutput, "sync-current-state") || !strings.Contains(syncOutput, "sync-current-path") {
		t.Fatalf("expected state and path sync details, got %s", syncOutput)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "MissingCurrent: 1") {
		t.Fatalf("expected missing current to remain unresolved, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "UntrackedCurrent: 0") || !strings.Contains(reconcileOutput, "StateDrifts: 0") || !strings.Contains(reconcileOutput, "PathDrifts: 0") {
		t.Fatalf("expected sync to clear current-file drift, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_ResolveMissingCurrentClearsMissingFindings(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	if err := os.Remove(strings.TrimSuffix(archivedPath, filepath.Ext(archivedPath)) + ".history.json"); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "MissingCurrent: 1") {
		t.Fatalf("expected one missing current before resolve, got %s", reconcileOutput)
	}

	resolveOutput := captureRunOutput(t, []string{"skills", "resolve-missing", archiveEntries[0].Name()})
	if !strings.Contains(resolveOutput, "ResolvedMissingCurrent: Task Survey Skill") {
		t.Fatalf("expected resolved missing current output, got %s", resolveOutput)
	}
	if !strings.Contains(resolveOutput, "ResolutionAction: resolve-missing") || !strings.Contains(resolveOutput, "ResolutionState: missing") {
		t.Fatalf("expected resolve details, got %s", resolveOutput)
	}

	reconcileOutput = captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "MissingCurrent: 0") || !strings.Contains(reconcileOutput, "MissingCurrentEntries: none") {
		t.Fatalf("expected no missing current after resolve, got %s", reconcileOutput)
	}

	ledgerOutput := captureRunOutput(t, []string{"skills", "ledger"})
	if !strings.Contains(ledgerOutput, "| resolve-missing | archived -> missing | Task Survey Skill |") {
		t.Fatalf("expected resolve-missing ledger entry, got %s", ledgerOutput)
	}
}

func TestSkillsCLI_RestoreGuideReportsRecordedRecoveryFacts(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	if err := os.Remove(archivedPath); err != nil {
		t.Fatalf("remove archived skill failed: %v", err)
	}
	if err := os.Remove(strings.TrimSuffix(archivedPath, filepath.Ext(archivedPath)) + ".history.json"); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	guideOutput := captureRunOutput(t, []string{"skills", "restore-guide", archiveEntries[0].Name()})
	if !strings.Contains(guideOutput, "RestoreSkill: Task Survey Skill") {
		t.Fatalf("expected guided skill name, got %s", guideOutput)
	}
	if !strings.Contains(guideOutput, "LatestAction: archive") || !strings.Contains(guideOutput, "LatestState: archived") {
		t.Fatalf("expected latest recorded state in guide, got %s", guideOutput)
	}
	if !strings.Contains(guideOutput, "RecordedPath: ") || !strings.Contains(guideOutput, archivedPath) {
		t.Fatalf("expected recorded path in guide, got %s", guideOutput)
	}
	if !strings.Contains(guideOutput, "SuggestedReconcileCheck: avatars skills reconcile") {
		t.Fatalf("expected reconcile follow-up in guide, got %s", guideOutput)
	}
}

func TestSkillsCLI_RestoreMissingCurrentRestoresArtifact(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archived skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
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
	if err := os.Remove(strings.TrimSuffix(archivedPath, filepath.Ext(archivedPath)) + ".history.json"); err != nil {
		t.Fatalf("remove archived skill history failed: %v", err)
	}

	restoreOutput := captureRunOutput(t, []string{"skills", "restore-missing", archiveEntries[0].Name(), restoreSourcePath})
	if !strings.Contains(restoreOutput, "RestoredMissingCurrent: ") || !strings.Contains(restoreOutput, archivedPath) {
		t.Fatalf("expected restored path in output, got %s", restoreOutput)
	}
	if !strings.Contains(restoreOutput, "LedgerAction: restore-missing-current") {
		t.Fatalf("expected restore ledger action, got %s", restoreOutput)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "MissingCurrent: 0") || !strings.Contains(reconcileOutput, "MissingCurrentEntries: none") {
		t.Fatalf("expected missing current to clear after restore, got %s", reconcileOutput)
	}

	ledgerOutput := captureRunOutput(t, []string{"skills", "ledger"})
	if !strings.Contains(ledgerOutput, "| restore-missing-current | archived -> archived | Task Survey Skill |") {
		t.Fatalf("expected restore-missing-current ledger entry, got %s", ledgerOutput)
	}
}

func TestSkillsCLI_ReconcileReportsMetadataDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	approvedPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	content, err := os.ReadFile(approvedPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	brokenContent := removeFrontmatterScalar(string(content), "approved-at")
	brokenContent = removeFrontmatterScalar(brokenContent, "source-run-id")
	brokenContent = removeFrontmatterScalar(brokenContent, "source-task-id")
	if err := os.WriteFile(approvedPath, []byte(brokenContent), 0o644); err != nil {
		t.Fatalf("write broken approved skill failed: %v", err)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "MetadataDrifts: 3") {
		t.Fatalf("expected metadata drift count, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "MetadataDriftEntries:") || !strings.Contains(reconcileOutput, "ApprovedAt | expected=present | actual=unknown") {
		t.Fatalf("expected approved metadata drift details, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "SourceRunID") || !strings.Contains(reconcileOutput, "SourceTaskID") {
		t.Fatalf("expected source metadata drift details, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_RepairMetadataClearsMetadataDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	approvedPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	content, err := os.ReadFile(approvedPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	brokenContent := removeFrontmatterScalar(string(content), "approved-at")
	brokenContent = removeFrontmatterScalar(brokenContent, "source-run-id")
	brokenContent = removeFrontmatterScalar(brokenContent, "source-task-id")
	if err := os.WriteFile(approvedPath, []byte(brokenContent), 0o644); err != nil {
		t.Fatalf("write broken approved skill failed: %v", err)
	}

	repairOutput := captureRunOutput(t, []string{"skills", "repair-metadata", approvedEntries[0].Name()})
	if !strings.Contains(repairOutput, "RepairedMetadata: ") || !strings.Contains(repairOutput, approvedPath) {
		t.Fatalf("expected repaired path in output, got %s", repairOutput)
	}
	if !strings.Contains(repairOutput, "LedgerAction: repair-current-metadata") {
		t.Fatalf("expected metadata repair ledger action, got %s", repairOutput)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "MetadataDrifts: 0") || !strings.Contains(reconcileOutput, "MetadataDriftEntries: none") {
		t.Fatalf("expected metadata drift to clear after repair, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_ReconcileReportsHistoryDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	approvedPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	if err := os.Remove(skillHistorySidecarPath(approvedPath)); err != nil {
		t.Fatalf("remove history sidecar failed: %v", err)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "HistoryDrifts: 1") {
		t.Fatalf("expected history drift count, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "HistoryDriftEntries:") || !strings.Contains(reconcileOutput, "reason=missing-history") {
		t.Fatalf("expected history drift details, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "expected-transitions=2") || !strings.Contains(reconcileOutput, "actual-transitions=0") {
		t.Fatalf("expected history transition counts, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_RepairHistoryClearsHistoryDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	approvedPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	if err := os.Remove(skillHistorySidecarPath(approvedPath)); err != nil {
		t.Fatalf("remove history sidecar failed: %v", err)
	}

	repairOutput := captureRunOutput(t, []string{"skills", "repair-history", approvedEntries[0].Name()})
	if !strings.Contains(repairOutput, "RepairedHistory: ") || !strings.Contains(repairOutput, approvedPath) {
		t.Fatalf("expected repaired history path in output, got %s", repairOutput)
	}
	if !strings.Contains(repairOutput, "RebuiltTransitions: 2") || !strings.Contains(repairOutput, "LedgerAction: repair-current-history") {
		t.Fatalf("expected history repair output details, got %s", repairOutput)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "HistoryDrifts: 0") || !strings.Contains(reconcileOutput, "HistoryDriftEntries: none") {
		t.Fatalf("expected history drift to clear after repair, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_ShowReportsCorruptedHistorySidecar(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	approvedPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	if err := os.WriteFile(skillHistorySidecarPath(approvedPath), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatalf("write corrupted history sidecar failed: %v", err)
	}

	showOutput := captureRunOutput(t, []string{"skills", "show", approvedEntries[0].Name()})
	if !strings.Contains(showOutput, "HistoryStatus: corrupted") {
		t.Fatalf("expected corrupted history status, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "GovernanceAlerts:") || !strings.Contains(showOutput, "error | corrupted-history") {
		t.Fatalf("expected corrupted history governance alert, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "SuggestedFollowUp: avatars skills repair-history") {
		t.Fatalf("expected suggested follow-up for corrupted history, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "HistoryError: decode skill history") {
		t.Fatalf("expected history decode error details, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "HistoryEntries: 0") {
		t.Fatalf("expected no parsed history entries for corrupted sidecar, got %s", showOutput)
	}
}

func TestSkillsCLI_ShowReportsBrokenTransitionChain(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archive skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	brokenHistory := []map[string]string{
		{"at": "2026-01-01T00:00:00Z", "action": "generate", "to_state": "generated", "path": archivedPath},
		{"at": "2026-01-01T00:02:00Z", "action": "archive", "from_state": "approved", "to_state": "archived", "path": archivedPath},
	}
	encodedHistory, err := json.MarshalIndent(brokenHistory, "", "  ")
	if err != nil {
		t.Fatalf("marshal broken history failed: %v", err)
	}
	if err := os.WriteFile(skillHistorySidecarPath(archivedPath), append(encodedHistory, '\n'), 0o644); err != nil {
		t.Fatalf("write broken history sidecar failed: %v", err)
	}

	showOutput := captureRunOutput(t, []string{"skills", "show", archiveEntries[0].Name()})
	if !strings.Contains(showOutput, "GovernanceAlerts:") || !strings.Contains(showOutput, "error | broken-transition-chain: expected-from=generated actual-from=approved action=archive") {
		t.Fatalf("expected broken transition governance alert, got %s", showOutput)
	}
	if !strings.Contains(showOutput, "SuggestedFollowUp: avatars skills repair-invariants") {
		t.Fatalf("expected suggested follow-up for broken transition chain, got %s", showOutput)
	}
}

func TestSkillsCLI_RepairInvariantsRebuildsBrokenTransitionChain(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archive skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	brokenHistory := []map[string]string{
		{"at": "2026-01-01T00:00:00Z", "action": "generate", "to_state": "generated", "path": archivedPath},
		{"at": "2026-01-01T00:02:00Z", "action": "archive", "from_state": "approved", "to_state": "archived", "path": archivedPath},
	}
	encodedHistory, err := json.MarshalIndent(brokenHistory, "", "  ")
	if err != nil {
		t.Fatalf("marshal broken history failed: %v", err)
	}
	if err := os.WriteFile(skillHistorySidecarPath(archivedPath), append(encodedHistory, '\n'), 0o644); err != nil {
		t.Fatalf("write broken history sidecar failed: %v", err)
	}

	repairOutput := captureRunOutput(t, []string{"skills", "repair-invariants", archiveEntries[0].Name()})
	if !strings.Contains(repairOutput, "RepairedInvariants:") || !strings.Contains(repairOutput, "LedgerAction: repair-current-invariants") {
		t.Fatalf("expected invariant repair output, got %s", repairOutput)
	}
	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "InvariantDrifts: 0") || !strings.Contains(reconcileOutput, "InvariantDriftEntries: none") {
		t.Fatalf("expected cleared invariant drifts, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_ReconcileReportsNonRepairableLedgerInvariantAnomaly(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archive skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	brokenHistory := []map[string]string{
		{"at": "2026-01-01T00:00:00Z", "action": "generate", "to_state": "generated", "path": archivedPath},
		{"at": "2026-01-01T00:02:00Z", "action": "archive", "from_state": "approved", "to_state": "archived", "path": archivedPath},
	}
	encodedHistory, err := json.MarshalIndent(brokenHistory, "", "  ")
	if err != nil {
		t.Fatalf("marshal broken history failed: %v", err)
	}
	if err := os.WriteFile(skillHistorySidecarPath(archivedPath), append(encodedHistory, '\n'), 0o644); err != nil {
		t.Fatalf("write broken history sidecar failed: %v", err)
	}
	archivedContent, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatalf("read archived skill failed: %v", err)
	}
	sourceRunID := ""
	sourceTaskID := ""
	templateID := ""
	for _, line := range strings.Split(string(archivedContent), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "template-id:"):
			templateID = strings.TrimSpace(strings.TrimPrefix(trimmed, "template-id:"))
		case strings.HasPrefix(trimmed, "source-run-id:"):
			sourceRunID = strings.TrimSpace(strings.TrimPrefix(trimmed, "source-run-id:"))
		case strings.HasPrefix(trimmed, "source-task-id:"):
			sourceTaskID = strings.TrimSpace(strings.TrimPrefix(trimmed, "source-task-id:"))
		}
	}
	anomalousEntry := map[string]any{
		"at":             time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
		"action":         "archive",
		"from_state":     "generated",
		"to_state":       "archived",
		"skill_name":     "Task Survey Skill",
		"skill_path":     archivedPath,
		"template_id":    templateID,
		"source_run_id":  sourceRunID,
		"source_task_id": sourceTaskID,
	}
	encodedEntry, err := json.Marshal(anomalousEntry)
	if err != nil {
		t.Fatalf("marshal anomalous ledger entry failed: %v", err)
	}
	ledgerPath := filepath.Join("skills", "governance-ledger.jsonl")
	file, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open governance ledger failed: %v", err)
	}
	if _, err := file.Write(append(encodedEntry, '\n')); err != nil {
		_ = file.Close()
		t.Fatalf("append anomalous ledger entry failed: %v", err)
	}
	_ = file.Close()

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "repairable=no") {
		t.Fatalf("expected non-repairable invariant drift, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "blocked-by=ledger-broken-transition-chain:") {
		t.Fatalf("expected ledger anomaly blocker, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "follow-up=avatars skills ledger") {
		t.Fatalf("expected ledger follow-up, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_ReconcileReportsCorruptedHistoryDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	approvedPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	if err := os.WriteFile(skillHistorySidecarPath(approvedPath), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatalf("write corrupted history sidecar failed: %v", err)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "HistoryDrifts: 1") {
		t.Fatalf("expected corrupted history drift count, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "reason=corrupted-history") || !strings.Contains(reconcileOutput, "actual-latest=invalid") {
		t.Fatalf("expected corrupted history drift details, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "expected-transitions=2") {
		t.Fatalf("expected reconstructed transition count, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_TimelineReportsImpossibleLifecycleJump(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "archive", approvedEntries[0].Name()})
	archiveEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "archive"))
	if err != nil {
		t.Fatalf("read archive skills failed: %v", err)
	}
	archivedPath := filepath.Join("skills", "archive", archiveEntries[0].Name())
	brokenHistory := []map[string]string{
		{"at": "2026-01-01T00:00:00Z", "action": "generate", "to_state": "generated", "path": archivedPath},
		{"at": "2026-01-01T00:02:00Z", "action": "archive", "from_state": "approved", "to_state": "archived", "path": archivedPath},
	}
	encodedHistory, err := json.MarshalIndent(brokenHistory, "", "  ")
	if err != nil {
		t.Fatalf("marshal broken history failed: %v", err)
	}
	if err := os.WriteFile(skillHistorySidecarPath(archivedPath), append(encodedHistory, '\n'), 0o644); err != nil {
		t.Fatalf("write broken history sidecar failed: %v", err)
	}

	timelineOutput := captureRunOutput(t, []string{"skills", "timeline"})
	if !strings.Contains(timelineOutput, "GovernanceAlerts:") || !strings.Contains(timelineOutput, "broken-transition-chain: expected-from=generated actual-from=approved action=archive") {
		t.Fatalf("expected impossible lifecycle jump alert, got %s", timelineOutput)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "InvariantDrifts: 1") {
		t.Fatalf("expected invariant drift count, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "InvariantDriftEntries:") || !strings.Contains(reconcileOutput, "reason=broken-transition-chain: expected-from=generated actual-from=approved action=archive") {
		t.Fatalf("expected invariant drift details, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_ReconcileReportsContentDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	contentPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	content, err := os.ReadFile(contentPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	updatedContent := strings.Replace(string(content), "Read README.md successfully.", "Read README.md and summarize drift signals.", 1)
	if updatedContent == string(content) {
		t.Fatal("expected content drift replacement to change the skill body")
	}
	if err := os.WriteFile(contentPath, []byte(updatedContent), 0o644); err != nil {
		t.Fatalf("write content drift skill failed: %v", err)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "ContentDrifts: 1") {
		t.Fatalf("expected content drift count, got %s", reconcileOutput)
	}
	if !strings.Contains(reconcileOutput, "ContentDriftEntries:") || !strings.Contains(reconcileOutput, "ledger-state=approved | current-state=approved |") {
		t.Fatalf("expected content drift details, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_SyncClearsContentDrift(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	if err := os.WriteFile("process_record.md", []byte("# Process Record\nbootstrap context\n"), 0o644); err != nil {
		t.Fatalf("write process_record failed: %v", err)
	}
	if err := os.WriteFile("README.md", []byte("# Readme\nproject context\n"), 0o644); err != nil {
		t.Fatalf("write README failed: %v", err)
	}

	_ = captureRunOutput(t, []string{"run", "Analyze the current repository and propose a refactoring plan"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	_ = captureRunOutput(t, []string{"skills", "approve", generatedEntries[0].Name()})
	approvedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "approved"))
	if err != nil {
		t.Fatalf("read approved skills failed: %v", err)
	}
	contentPath := filepath.Join("skills", "approved", approvedEntries[0].Name())
	content, err := os.ReadFile(contentPath)
	if err != nil {
		t.Fatalf("read approved skill failed: %v", err)
	}
	updatedContent := strings.Replace(string(content), "Read README.md successfully.", "Read README.md and summarize drift signals.", 1)
	if updatedContent == string(content) {
		t.Fatal("expected content drift replacement to change the skill body")
	}
	if err := os.WriteFile(contentPath, []byte(updatedContent), 0o644); err != nil {
		t.Fatalf("write content drift skill failed: %v", err)
	}

	syncOutput := captureRunOutput(t, []string{"skills", "sync"})
	if !strings.Contains(syncOutput, "SyncedCurrent: 1") {
		t.Fatalf("expected one synced current entry, got %s", syncOutput)
	}
	if !strings.Contains(syncOutput, "sync-current-content") || !strings.Contains(syncOutput, "content-drift=yes") {
		t.Fatalf("expected content sync details, got %s", syncOutput)
	}

	reconcileOutput := captureRunOutput(t, []string{"skills", "reconcile"})
	if !strings.Contains(reconcileOutput, "ContentDrifts: 0") || !strings.Contains(reconcileOutput, "ContentDriftEntries: none") {
		t.Fatalf("expected sync to clear content drift, got %s", reconcileOutput)
	}
}

func TestSkillsCLI_GenerateCreatesPendingCandidate(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	output := captureRunOutput(t, []string{"skills", "generate", "--task", "demo-task", "Analyze the project and create a reusable survey skill"})
	if !strings.Contains(output, "Generated skill:") || !strings.Contains(output, "Skill preview: Task Survey Skill") {
		t.Fatalf("expected generate output, got %s", output)
	}
	if !strings.Contains(output, "LLM draft status:") || !strings.Contains(output, "Ready for approval:") {
		t.Fatalf("expected llm draft and readiness output, got %s", output)
	}
	if !strings.Contains(output, "Review reference: none") {
		t.Fatalf("expected explicit missing review reference output, got %s", output)
	}
	if !strings.Contains(output, "Next step: avatars skills review") {
		t.Fatalf("expected review follow-up output, got %s", output)
	}
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	if len(generatedEntries) != 1 {
		t.Fatalf("expected one generated skill, got %d", len(generatedEntries))
	}
	pendingOutput := captureRunOutput(t, []string{"skills", "pending"})
	if !strings.Contains(pendingOutput, "Task Survey Skill") || !strings.Contains(pendingOutput, "task=demo-task") {
		t.Fatalf("expected pending generated skill, got %s", pendingOutput)
	}
}

func TestSkillsCLI_ReviewGeneratedCandidate_ShowsValidationFindings(t *testing.T) {
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	output := captureRunOutput(t, []string{"skills", "generate", "--task", "demo-task", "Analyze the project and create a reusable survey skill"})
	generatedEntries, err := readSkillMarkdownEntries(filepath.Join("skills", "generated"))
	if err != nil {
		t.Fatalf("read generated skills failed: %v", err)
	}
	if len(generatedEntries) != 1 {
		t.Fatalf("expected one generated skill file, got %d", len(generatedEntries))
	}
	reviewOutput := captureRunOutput(t, []string{"skills", "review", generatedEntries[0].Name()})
	if !strings.Contains(reviewOutput, "ReadyForApproval:") {
		t.Fatalf("expected readiness output, got %s", reviewOutput)
	}
	if !strings.Contains(reviewOutput, "ValidationFindings:") {
		t.Fatalf("expected validation findings output, got %s", reviewOutput)
	}
	_ = output
}

func TestPlanModeSkillFileParsesCorrectly(t *testing.T) {
	// Verify the Plan Mode Skill file on disk parses correctly.
	skillPath := filepath.Join("..", "..", "skills", "approved", "Plan_Mode_Skill.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read Plan Mode Skill file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "always-on: true") {
		t.Fatal("Plan Mode Skill must have always-on: true")
	}
	if !strings.Contains(content, "user-invocable: false") {
		t.Fatal("Plan Mode Skill must have user-invocable: false")
	}
	if !strings.Contains(content, "lifecycle-state: approved") {
		t.Fatal("Plan Mode Skill must have lifecycle-state: approved")
	}
	if !strings.Contains(content, "read-only") {
		t.Fatal("Plan Mode Skill must describe read-only boundary")
	}
	if !strings.Contains(content, "Anti-Patterns") {
		t.Fatal("Plan Mode Skill must contain Anti-Patterns section")
	}
}

func TestNavigatorSkillFileParsesCorrectly(t *testing.T) {
	// Verify the Navigator Skill file on disk parses correctly.
	skillPath := filepath.Join("..", "..", "skills", "approved", "_Navigator_Skill.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read Navigator Skill file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "always-on: true") {
		t.Fatal("Navigator Skill must have always-on: true")
	}
	if !strings.Contains(content, "user-invocable: false") {
		t.Fatal("Navigator Skill must have user-invocable: false")
	}
	if !strings.Contains(content, "skills-navigator") {
		t.Fatal("Navigator Skill must have name: skills-navigator")
	}
	if !strings.Contains(content, "intent-routing") {
		t.Fatal("Navigator Skill must list intent-routing skill")
	}
	if !strings.Contains(content, "plan-mode") {
		t.Fatal("Navigator Skill must list plan-mode skill")
	}
}

// --- BUG-1: @file content triggers work request routing ---
