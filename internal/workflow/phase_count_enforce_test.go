package workflow

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEstimatePhaseCount_ExplicitPhaseMarkers(t *testing.T) {
	desc := `Phase 1 scaffold; Phase 2 auth; Phase 3 CRUD; Phase 4 comments; Phase 5 tests.
本轮只要 Phase 1。`
	if got := estimatePhaseCount(desc); got != 5 {
		t.Fatalf("estimatePhaseCount=%d want 5", got)
	}
}

func TestEstimatePhaseCount_ChineseStageMarkers(t *testing.T) {
	desc := `阶段1 骨架；阶段2 鉴权；阶段3 CRUD；阶段4 评论；阶段5 测试`
	if got := estimatePhaseCount(desc); got != 5 {
		t.Fatalf("estimatePhaseCount=%d want 5", got)
	}
}

func TestEstimatePhaseCount_ChineseOrdinalStages(t *testing.T) {
	desc := `第一段脚手架（app/core|db|models|api），第二段 JWT 登录注册，第三段 CRUD+评论。三段都跑通。含鉴权、登录、注册、测试。`
	if got := estimatePhaseCount(desc); got != 3 {
		t.Fatalf("estimatePhaseCount=%d want 3 (must not inflate via keywords)", got)
	}
}

func TestEstimatePhaseCount_NoExplicitKeepsSoftCap(t *testing.T) {
	desc := `build a small fastapi app with auth middleware and deploy`
	got := estimatePhaseCount(desc)
	if got < 1 || got > 3 {
		t.Fatalf("estimatePhaseCount=%d want 1..3", got)
	}
}

func TestEnforcePhaseCountInPlan_BumpsCollapsedCount(t *testing.T) {
	plan := `# Project Plan

> **Project**: taskboard
> **Created**: 2026-07-15
> **Status**: draft
> **Active Phase**: 1
> **Phase Count**: 1

## Goals
- Scaffold

## Phases

### Phase 1: Scaffold
- **Goal**: skeleton
- **Status**: pending
- **Detail**: docs/workflow/phase1.md

## Success Criteria
- [ ] builds
`
	task := "Phase 1 .. Phase 5 greenfield; 本轮只要 Phase 1"
	out := EnforcePhaseCountInPlan(plan, task)
	meta := ParsePlanMeta(out)
	if meta.PhaseCount != 5 {
		t.Fatalf("PhaseCount=%d want 5\n%s", meta.PhaseCount, out)
	}
	for i := 2; i <= 5; i++ {
		if !strings.Contains(out, "### Phase "+strconv.Itoa(i)) {
			t.Fatalf("missing Phase %d stub\n%s", i, out)
		}
	}
}

func TestEnforcePlanPhaseCountFromTask_WritesDisk(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Phase Count**: 1
> **Active Phase**: 1

### Phase 1: A
- **Goal**: x
- **Status**: pending
- **Detail**: docs/workflow/phase1.md
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	// Minimal filled phase1 so skeleton logic does not need to invent it.
	phase1 := `# Phase 1 · A

## Overview
Scaffold

## Tasks
### 1. Scaffold
- [ ] create files

## Verification
- [ ] builds
`
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase1), 0644); err != nil {
		t.Fatal(err)
	}
	todo := `# Active Workspace
> **Phase**: 1
## Current Task
- [ ] Implement Phase 1
## Phase 1 Checklist (active)
- [ ] 1. [Task Group Name] (0/2)
## Completed
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	n, err := EnforcePlanPhaseCountFromTask(root, "Phase 1 Phase 2 Phase 3 Phase 4 Phase 5")
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("n=%d want 5", n)
	}
	data, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if !strings.Contains(string(data), "> **Phase Count**: 5") {
		t.Fatalf("plan not updated:\n%s", data)
	}
	for i := 2; i <= 5; i++ {
		p := filepath.Join(wf, "phase"+strconv.Itoa(i)+".md")
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("missing phase%d.md: %v", i, err)
		}
		if IsPhaseDocEmpty(string(body)) {
			t.Fatalf("phase%d.md still empty template", i)
		}
	}
	if !PhaseDocsComplete(root) {
		t.Fatalf("PhaseDocsComplete=false after skeletons")
	}
	todoOut, _ := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	if strings.Contains(string(todoOut), "[Task Group Name]") {
		t.Fatalf("todo still has placeholders:\n%s", todoOut)
	}
	if !strings.Contains(string(todoOut), "Scaffold") {
		t.Fatalf("todo not synced from phase1:\n%s", todoOut)
	}
}

func TestSyncTodoFromPlan_OverwritesPlaceholdersWhenPhaseFilled(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Phase Count**: 1
> **Active Phase**: 1
### Phase 1: Scaffold
- **Goal**: skeleton
- **Status**: pending
`
	phase1 := `# Phase 1
## Tasks
### 1. Project Scaffolding
- [x] Create requirements.txt
- [x] Create app/main.py
### 2. Database Foundation
- [x] Configure SQLAlchemy
`
	todo := `# Active Workspace
> **Phase**: 1
## Current Task
- [ ] Implement Phase 1: Scaffolding
## Phase 1 Checklist (active)
- [>] 1. [Task Group Name] (0/2)
- [ ] 2. [Task Group Name] (0/1)
## Completed
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase1), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	if err := SyncTodoFromPlan(root); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(wf, "avatars_todo.md"))
	s := string(out)
	if strings.Contains(s, "[Task Group Name]") {
		t.Fatalf("placeholders remain:\n%s", s)
	}
	if !strings.Contains(s, "- [x] 1. Project Scaffolding") && !strings.Contains(s, "- [x] Project Scaffolding") {
		t.Fatalf("expected checked group from phase1:\n%s", s)
	}
	if !strings.Contains(s, "- [x] Implement Phase 1") {
		t.Fatalf("Current Task should be marked done:\n%s", s)
	}
}

func TestCountTodoProgress_IgnoresPlaceholders(t *testing.T) {
	todo := `# Active Workspace
> **Phase**: 1
## Phase 1 Checklist (active)
- [ ] 1. [Task Group Name] (0/2)
- [ ] Real work item
## Completed
`
	c, total := CountTodoProgressFromContent(todo)
	if total != 1 || c != 0 {
		t.Fatalf("completed=%d total=%d want 0/1", c, total)
	}
}

func TestEstimatePhaseCount_RepairDoesNotInflateFromTestMigration(t *testing.T) {
	desc := "修复 hashringx 模块的 go test ./... 失败：1) 根包 TestMigration 0 keys moved 2) 清理诊断文件"
	if got := estimatePhaseCount(desc); got != 1 {
		t.Fatalf("repair+TestMigration must stay 1, got %d", got)
	}
	if got := estimatePhaseCount("请接着把刚才那轮没过门的修到能过 go test"); got != 1 {
		t.Fatalf("continuation repair must stay 1, got %d", got)
	}
}

func TestEstimatePhaseCount_MigrationWordNotSubstring(t *testing.T) {
	// Greenfield without explicit stages: "TestMigration" must not count as a migration phase.
	got := estimatePhaseCount("implement consistent hashing library with TestMigration coverage")
	if got != 1 {
		t.Fatalf("TestMigration substring must not bump phases, got %d", got)
	}
}

func TestEnforcePhaseCountInPlan_RepairKeepsExistingCount(t *testing.T) {
	plan := `# Project Plan
> **Phase Count**: 2
> **Active Phase**: 1

### Phase 1: Library
- **Goal**: impl+tests
### Phase 2: Deferred
- **Goal**: later
`
	task := "请接着把刚才那轮没过门的修到能过 go test ./... TestMigration still fails"
	out := EnforcePhaseCountInPlan(plan, task)
	meta := ParsePlanMeta(out)
	if meta.PhaseCount != 2 {
		t.Fatalf("F88: repair must not pad Phase Count, got %d\n%s", meta.PhaseCount, out)
	}
	if strings.Contains(out, "### Phase 3") {
		t.Fatalf("F88: repair must not add TBD Phase 3\n%s", out)
	}
}
