package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCriticDecidePhaseAdvance_SoftLayoutEvidence(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	// Minimal compiling Go module so projectCompiles can pass.
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "handlers"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "handlers", "api.go"), []byte("package handlers\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	plan := `> **Project**: demo
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 2

### Phase 1: Scaffold
- **Status**: in-progress
### Phase 2: Auth
- **Status**: pending
`
	todo := `> **Phase**: 1 of 2
## Phase 1 Checklist (active)
- [x] go.mod ready
- [>] Create internal/handlers package
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	// phase docs complete gate is outside CriticDecide; here we only need decide soft path.
	advanced, next := CriticDecidePhaseAdvanceUpTo(dir, 0)
	if !advanced || next != 2 {
		t.Fatalf("soft layout evidence should advance 1→2: advanced=%v next=%d", advanced, next)
	}
}

func TestChecklistItemHasLayoutDirEvidence_PythonAppAlias(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "app", "models"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "app", "models", "user.py"), []byte("class User: pass\n"), 0644)
	if !checklistItemHasLayoutDirEvidence(dir, "Create models package under app/") {
		t.Fatal("app/models should satisfy models layout alias")
	}
	if !checklistItemHasLayoutDirEvidence(dir, "Implement `app/models` entities") {
		t.Fatal("backtick app/models should match")
	}
}

func TestTryAdvancePhase_ProjectCompleteReturnsAdvanced(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1

### Phase 1: Only
- **Status**: in-progress
`
	todo := `> **Phase**: 1 of 1
## Phase 1 Checklist (active)
- [x] done
`
	filled := `# Phase 1 · Only
<!-- avatars:phase-detail=full -->

## Overview
Single-phase project ready to complete.

## Tasks
### 1. Done
- [x] done

## Verification
- [x] checklist complete
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(filled), 0644)
	advanced, next := tryAdvancePhaseUpTo(dir, 0)
	if !advanced || next != 0 {
		t.Fatalf("last-phase complete should return advanced=true,0 got %v %d", advanced, next)
	}
	out, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	body := string(out)
	if !strings.Contains(body, "> **Status**: completed") || !strings.Contains(body, "- **Status**: completed") {
		t.Fatalf("expected completed header+phase:\n%s", body)
	}
}

func TestF108_LaterPhaseDiskEvidenceAdvances(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/workqueuex\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "queue.go"), []byte("package workqueuex\n\nfunc New() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "worker.go"), []byte("package workqueuex\n\nfunc Run() {}\n"), 0644)
	plan := `> **Project**: workqueuex
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 3

### Phase 1: Core
- **Status**: in-progress
### Phase 2: Workers
- **Status**: pending
### Phase 3: Tests
- **Status**: pending
`
	todo := `> **Phase**: 1 of 3
## Phase 1 Checklist (active)
- [x] queue.go core
- [ ] xyzzy leftover clock methods (2/4)
`
	phase2 := `# Phase 2 · Workers
## Tasks
- [ ] Implement ` + "`worker.go`" + ` worker pool
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase2.md"), []byte(phase2), 0644)
	if !laterPhaseDeliverablesOnDisk(dir) {
		t.Fatal("worker.go on disk should count as later-phase evidence")
	}
	advanced, next := CriticDecidePhaseAdvanceUpTo(dir, 0)
	if !advanced || next != 2 {
		t.Fatalf("F108: disk-ahead should advance 1→2: advanced=%v next=%d", advanced, next)
	}
}

