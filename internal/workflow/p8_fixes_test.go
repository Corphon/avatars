package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectRequiredEnvNames(t *testing.T) {
	names := DetectRequiredEnvNames("写接口鉴权，环境变量 NOTEBOARD_API_KEY，错误 401")
	if len(names) != 1 || names[0] != "NOTEBOARD_API_KEY" {
		t.Fatalf("got %#v", names)
	}
	if names := DetectRequiredEnvNames("plain CRUD without auth"); len(names) != 0 {
		t.Fatalf("expected empty, got %#v", names)
	}
}

func TestEnvConstraintPrompt(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Status**: in-progress
> **Active Phase**: 2
> **Phase Count**: 2
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase2.md"), []byte("# Phase 2\nUse NOTEBOARD_API_KEY for API key auth.\n"), 0644)
	got := EnvConstraintPrompt("list endpoints require API key", dir)
	if !strings.Contains(got, "ENV LOCK") || !strings.Contains(got, "NOTEBOARD_API_KEY") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "fallback") {
		t.Fatalf("ENV LOCK should forbid fallback aliases, got %q", got)
	}
}

func TestEnvConstraintPrompt_Phase1DoesNotLockPhase2Key(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 2
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte("# Phase 1\nScaffold polls CRUD. No auth yet.\n"), 0644)
	input := "## Phase 1\nBuild CRUD\n\n## Phase 2\nAuth with POLLBOX_API_KEY and X-API-Key\n"
	got := EnvConstraintPrompt(input, dir)
	if got != "" {
		t.Fatalf("Phase 1 must not ENV LOCK Phase-2 keys, got %q", got)
	}
}

func TestMarkCheckboxDone_BumpsZeroCount(t *testing.T) {
	got := markCheckboxDone("- [>] 2. Database migration & schema (0/3)")
	if !strings.Contains(got, "- [x]") {
		t.Fatalf("expected [x], got %q", got)
	}
	if !strings.Contains(got, "(3/3)") {
		t.Fatalf("expected (3/3), got %q", got)
	}
}

func TestPhaseTasksCoverScope(t *testing.T) {
	thin := `# Phase 1
## Scope & Success Criteria
- [ ] Add API key auth middleware for write endpoints
- [ ] DELETE /polls/{id} returns 204
## Tasks
### 1. Store
- [ ] Add DeletePoll method
`
	if phaseTasksCoverScope(thin) {
		t.Fatal("thin Tasks must not cover auth+delete Scope")
	}
	fat := `# Phase 1
## Scope & Success Criteria
- [ ] Add API key auth middleware for write endpoints
- [ ] DELETE /polls/{id} returns 204
## Tasks
### 1. Auth
- [ ] Implement API key auth middleware on write endpoints
### 2. Delete
- [ ] DELETE /polls/{id} handler returns 204
`
	if !phaseTasksCoverScope(fat) {
		t.Fatal("fat Tasks should cover Scope")
	}
}

func TestPreferSpecificEnvNames(t *testing.T) {
	got := PreferSpecificEnvNames([]string{"NOTEBOARD_API_KEY", "API_KEY"})
	if len(got) != 1 || got[0] != "NOTEBOARD_API_KEY" {
		t.Fatalf("got %#v", got)
	}
}

func TestPreferSpecificEnvNames_DropsBareKeyAlone(t *testing.T) {
	// D2: PreferSpecific must not return bare KEY when it is the only token.
	if got := PreferSpecificEnvNames([]string{"KEY"}); len(got) != 0 {
		t.Fatalf("bare KEY must be dropped, got %#v", got)
	}
	if got := PreferSpecificEnvNames([]string{"API_KEY", "KEY"}); len(got) != 0 {
		t.Fatalf("generic-only list must be empty, got %#v", got)
	}
	got := PreferSpecificEnvNames([]string{"CLIPVAULT_API_KEY", "KEY", "API_KEY"})
	if len(got) != 1 || got[0] != "CLIPVAULT_API_KEY" {
		t.Fatalf("got %#v", got)
	}
}

func TestDetectRequiredEnvNames_IgnoresBareKey(t *testing.T) {
	names := DetectRequiredEnvNames("鉴权用 KEY 或 API_KEY，也可用 CLIPVAULT_API_KEY")
	if len(names) != 1 || names[0] != "CLIPVAULT_API_KEY" {
		t.Fatalf("got %#v", names)
	}
}

func TestUpdatePlanStatus_MarksCurrentTask(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Status**: in-progress
> **Active Phase**: 2
> **Phase Count**: 2

### Phase 1: Core
- **Status**: completed
### Phase 2: Auth
- **Status**: in-progress
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	todo := `## Current Task
- [ ] Implement Phase 2

## Phase 2 Checklist (active)
- [x] auth
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	if err := UpdatePlanStatus(dir, "completed"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	if !strings.Contains(string(out), "- [x] Implement Phase 2") {
		t.Fatalf("Current Task not marked:\n%s", out)
	}
	planOut, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if strings.Contains(string(planOut), "- **Status**: in-progress") {
		t.Fatalf("F92: completed header should sync every phase status:\n%s", planOut)
	}
}

func TestF92_SyncCompletedPlanPhases(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Status**: completed
> **Active Phase**: 1
> **Phase Count**: 1

### Phase 1: Library + Tests + Docs
- **Status**: in-progress
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	SyncCompletedPlanPhases(dir)
	out, err := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "- **Status**: in-progress") {
		t.Fatalf("F92: phase must not stay in-progress when header is completed:\n%s", text)
	}
	if !strings.Contains(text, "- **Status**: completed") {
		t.Fatalf("F92: expected phase completed:\n%s", text)
	}
}
