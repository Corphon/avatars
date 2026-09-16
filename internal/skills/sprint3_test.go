package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/planner"
	"avatars/internal/skillbuilder"
)

func TestS3_ValidateGeneratedCandidate_RoleSchema(t *testing.T) {
	proposal := skillbuilder.BuildForRole("Builder", "implement word count", planner.Plan{Title: "word count"}, "repo survey")
	def := Definition{
		Listing: Listing{
			Name:         proposal.Name,
			Description:  proposal.Description,
			WhenToUse:    proposal.WhenToUse,
			AllowedTools: proposal.AllowedTools,
			Context:      proposal.Context,
			AlwaysOn:     proposal.AlwaysOn,
			Role:         "builder",
		},
		UserInvocable:          proposal.UserInvocable,
		DisableModelInvocation: proposal.DisableModelInvocation,
		TemplateID:             proposal.TemplateID,
		Body:                   proposal.Body,
	}
	findings := validateGeneratedCandidate(def)
	for _, f := range findings {
		if strings.Contains(f, "Repo Survey Summary") || strings.Contains(f, "Suggested Workflow") {
			t.Fatalf("role skill must not require Task Survey headings, got: %v", findings)
		}
		if strings.Contains(f, "non-readonly") || strings.Contains(f, "disallowed tool") {
			t.Fatalf("builder write tools should be allowed, got: %v", findings)
		}
	}
}

func TestS3_ParseRoleFrontmatter(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "builder-role.md")
	content := `---
name: builder-role-test
description: Always-on role guidance for the Builder avatar used in unit tests only.
when_to_use: Injected when the Builder avatar is active during sprint3 tests.
context: Role playbook for Builder tests.
version: 0.1.0
lifecycle-state: approved
user-invocable: false
disable-model-invocation: true
always-on: true
role: builder
template-id: builtin-builder-role
allowed-tools:
  - read
---

# Builder Role

## Purpose
Guide builder.

## Constraints
- Match interfaces.
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	def, err := loadDefinitionFromContent(path, content)
	if err != nil {
		t.Fatalf("loadDefinitionFromContent: %v", err)
	}
	if def.Role != "builder" {
		t.Fatalf("Role=%q, want builder (always-on must not overwrite role)", def.Role)
	}
	if !def.AlwaysOn {
		t.Fatal("expected AlwaysOn=true")
	}
}

func TestS3_RecordSkillUse_AppearsInLedger(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)
	if err := store.RecordSkillUse("Task Survey Skill", "skills/approved/task.md", "task-1", true); err != nil {
		t.Fatalf("RecordSkillUse: %v", err)
	}
	ledger, err := store.GovernanceLedger()
	if err != nil {
		t.Fatalf("GovernanceLedger: %v", err)
	}
	found := false
	for _, e := range ledger.Entries {
		if e.Action == "task_completed" && e.SkillName == "Task Survey Skill" && e.ToState == "success" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected task_completed success entry, got %+v", ledger.Entries)
	}
}

func TestS3_ShouldAutoApprove_RequiresHistory(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)
	if store.ShouldAutoApprove("unknown-skill") {
		t.Fatal("ShouldAutoApprove must be false without history")
	}
	for i := 0; i < 7; i++ {
		if err := store.RecordSkillUse("builder-skill", "", "task", true); err != nil {
			t.Fatalf("RecordSkillUse: %v", err)
		}
	}
	// Without an approved definition, inferSkillRole defaults to builder (strict).
	if !store.ShouldAutoApprove("builder-skill") {
		t.Fatal("expected ShouldAutoApprove after 7 successes for builder threshold")
	}
}

func TestS3_SelfHealIncludesRoleBuiltins(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "skills"))
	if err := os.MkdirAll(store.approvedDir(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "skills", builtinHealedMarker), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("marker: %v", err)
	}
	n, err := store.SelfHealBuiltinSkills()
	if err != nil {
		t.Fatalf("SelfHealBuiltinSkills: %v", err)
	}
	if n < 5 {
		t.Fatalf("expected role builtins to heal despite marker, wrote %d", n)
	}
	for _, name := range []string{"Planner_Role_Skill.md", "Builder_Role_Skill.md", "Critic_Role_Skill.md"} {
		if _, err := os.Stat(filepath.Join(store.approvedDir(), name)); err != nil {
			t.Fatalf("missing builtin %s: %v", name, err)
		}
	}
}

func TestS3_SelfHealRefreshesOlderBuiltinVersion(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "skills"))
	if err := os.MkdirAll(store.approvedDir(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := `---
name: workflow-engine
description: stale workflow engine skill used only for self-heal version refresh tests.
when_to_use: Always active during self-heal version refresh unit tests.
context: Stale copy for version refresh.
version: 0.1.0
lifecycle-state: approved
user-invocable: false
disable-model-invocation: true
always-on: true
allowed-tools:
  - read
---

# Stale Workflow Engine
`
	path := filepath.Join(store.approvedDir(), "Workflow_Engine_Skill.md")
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	n, err := store.SelfHealBuiltinSkills()
	if err != nil {
		t.Fatalf("SelfHealBuiltinSkills: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least Workflow_Engine refresh, wrote %d", n)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read refreshed: %v", err)
	}
	if !strings.Contains(string(got), "version: 0.4.1") {
		t.Fatalf("expected embedded 0.4.1 workflow skill, got:\n%s", string(got))
	}
	if strings.Contains(string(got), "# Stale Workflow Engine") {
		t.Fatal("stale body should have been overwritten")
	}
}
