package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhaseTasksUncoveredScope_AndErrorFormat(t *testing.T) {
	thin := `# Phase 1
## Scope & Success Criteria
- [ ] Add API key auth middleware for write endpoints
- [ ] DELETE /polls/{id} returns 204
- [ ] GET /health returns 200
## Tasks
### 1. Store
- [ ] Add DeletePoll method
- [ ] health handler returns 200
`
	gaps := phaseTasksUncoveredScope(thin)
	if len(gaps) == 0 {
		t.Fatal("expected uncovered Scope items")
	}
	joined := strings.ToLower(strings.Join(gaps, " "))
	if !strings.Contains(joined, "auth") && !strings.Contains(joined, "middleware") {
		t.Fatalf("expected auth/middleware gap, got %#v", gaps)
	}
	errSuffix := formatUncoveredScopeForError(gaps)
	if !strings.Contains(errSuffix, "uncovered:") {
		t.Fatalf("error format missing uncovered: %q", errSuffix)
	}
	prompt := formatUncoveredScopeForPrompt(gaps)
	if !strings.Contains(prompt, "Uncovered Scope items") {
		t.Fatalf("prompt missing gap list: %q", prompt)
	}
}

func TestPhaseTasksCoverScope_UnchangedThreshold(t *testing.T) {
	// Regression: F9 must NOT loosen the ≥80% gate for any provider.
	thin := `# Phase 1
## Scope & Success Criteria
- [ ] Add API key auth middleware for write endpoints
- [ ] DELETE /polls/{id} returns 204
## Tasks
### 1. Store
- [ ] Add DeletePoll method
`
	if phaseTasksCoverScope(thin) {
		t.Fatal("thin Tasks must still fail the coverage gate")
	}
	fat := `# Phase 1
## Scope & Success Criteria
- [ ] Add API key auth middleware for write endpoints
- [ ] DELETE /polls/{id} returns 204
## Tasks
### 1. Auth
- [ ] Add API key auth middleware for write endpoints
### 2. Delete
- [ ] DELETE /polls/{id} returns 204
`
	if !phaseTasksCoverScope(fat) {
		t.Fatal("fat Tasks must pass the coverage gate")
	}
}

func TestPhaseTasksCoverScope_APIIdentifiers_F27(t *testing.T) {
	// cidrkit-style: Scope uses `Contains` prose; Tasks name the API without echoing essay words.
	doc := `# Phase 1
## Scope & Success Criteria
- [ ] ` + "`Contains`" + ` correctly determines IP membership in a CIDR, with IPv4/IPv6 type safety
- [ ] ` + "`Overlaps`" + ` correctly detects intersection between two CIDRs
- [ ] ` + "`Merge`" + ` combines adjacent/aggregatable prefixes into a minimal list
- [ ] ` + "`Subtract`" + ` computes CIDR set difference, splitting into smaller prefixes
- [ ] Parse rejects illegal CIDR strings with a clear error
## Tasks
### 1. Core API
- [ ] Implement Contains for IPv4/IPv6 prefixes
- [ ] Implement Overlaps between two prefixes
- [ ] Implement Merge of adjacent prefixes
- [ ] Implement Subtract set difference
- [ ] Implement Parse with invalid-input errors
`
	if !phaseTasksCoverScope(doc) {
		t.Fatalf("API-named Tasks should cover backticked Scope (F27); uncovered=%v", phaseTasksUncoveredScope(doc))
	}
	// Still reject skeleton Tasks that omit the APIs.
	skeleton := `# Phase 1
## Scope & Success Criteria
- [ ] ` + "`Contains`" + ` correctly determines IP membership
- [ ] ` + "`Merge`" + ` combines adjacent prefixes
## Tasks
### 1. Setup
- [ ] Create go.mod
- [ ] Add package scaffolding
`
	if phaseTasksCoverScope(skeleton) {
		t.Fatal("skeleton without API names must still fail ≥80% gate")
	}
}

func TestPhaseTasksCoverScope_PkgQualifiedAndDepConstraint_F58(t *testing.T) {
	// r19 ulidx: Scope uses `ulidx.Parse()` + go.mod constraint; Tasks say Parse(...) only.
	doc := `# Phase 1 · Core Library
## Scope & Success Criteria
- [ ] ` + "`ulidx.New()`" + ` generates a 26-character Crockford base32 ULID string
- [ ] ` + "`ulidx.Parse()`" + ` correctly round-trips any generated ULID
- [ ] Timestamp portion is verifiable via injected fixed clock in tests
- [ ] No external dependencies in ` + "`go.mod`" + `
## Tasks
### 1. Core
- [ ] Create ulidx/ulid.go defining the ULID type
- [ ] Implement New() and NewWithTime(t time.Time) ULID with injectable clock
- [ ] Create ulidx/parse.go with Parse(s string) (ULID, error)
- [ ] Implement encode/decode in ulidx/encoding.go
- [ ] Add Timestamp() method so fixed clock injection is verifiable in tests
`
	if !phaseTasksCoverScope(doc) {
		t.Fatalf("F58: short API names + dep constraint should pass; uncovered=%v", phaseTasksUncoveredScope(doc))
	}
	// Python/JS style: pkg.fn + package.json constraint.
	py := `# Phase 1
## Scope & Success Criteria
- [ ] ` + "`mylib.load()`" + ` reads config from disk
- [ ] No external dependencies in ` + "`pyproject.toml`" + `
## Tasks
### 1. API
- [ ] Implement load() in mylib/loader.py reading YAML-free JSON
`
	if !phaseTasksCoverScope(py) {
		t.Fatalf("F58 cross-lang: uncovered=%v", phaseTasksUncoveredScope(py))
	}
}

func TestPhaseTasksCoverScope_VerificationGatesDoNotNeedEcho(t *testing.T) {
	doc := `# Phase 1
## Scope & Success Criteria
- [ ] ` + "`go test ./...`" + ` passes (all tests green)
- [ ] ` + "`go build ./...`" + ` succeeds
- [ ] Public API exposes Trigger, Flush, and Cancel
## Tasks
### 1. Library
- [ ] Create debounce.go with Trigger, Flush, and Cancel
- [ ] Add debounce_test.go covering last-trigger-wins
`
	if !phaseTasksCoverScope(doc) {
		t.Fatalf("implementation Tasks should cover build/test gates; uncovered=%v", phaseTasksUncoveredScope(doc))
	}
	py := `# Phase 1
## Scope & Success Criteria
- [ ] pytest passes
- [ ] ` + "`load()`" + ` reads JSON from disk
## Tasks
### 1. API
- [ ] Implement load() in mylib/loader.py reading JSON from disk
`
	if !phaseTasksCoverScope(py) {
		t.Fatalf("pytest gate should not block Python Tasks; uncovered=%v", phaseTasksUncoveredScope(py))
	}
}

func TestResetConstructCheckboxes(t *testing.T) {
	in := "## Scope & Success Criteria\n- [x] `go test ./...` passes\n- [X] Trigger works\n- [>] bookmark\n## Tasks\n- [x] Write debounce.go\n"
	got := resetConstructCheckboxes(in)
	if strings.Contains(got, "[x]") || strings.Contains(got, "[X]") || strings.Contains(got, "[>]") {
		t.Fatalf("construct must not keep done boxes:\n%s", got)
	}
	if !strings.Contains(got, "- [ ] `go test") || !strings.Contains(got, "- [ ] Trigger") || !strings.Contains(got, "- [ ] Write") {
		t.Fatalf("labels must stay:\n%s", got)
	}
}

func TestPhaseDocThinRejected_NeedsFullDetail(t *testing.T) {
	tmp := t.TempDir()
	docs := filepath.Join(tmp, "docs", "workflow")
	if err := os.MkdirAll(docs, 0755); err != nil {
		t.Fatal(err)
	}
	draft := annotatePhaseThinRejected(`# Phase 1 · Core
## Overview
Real overview text for the library phase.
## Scope & Success Criteria
- [ ] Implement Contains
## Tasks
### 1. API
- [ ] Implement Contains helper
## Verification
- [ ] go test ./... passes
`)
	if err := os.WriteFile(filepath.Join(docs, "phase1.md"), []byte(draft), 0644); err != nil {
		t.Fatal(err)
	}
	if !IsPhaseDocThinRejected(draft) {
		t.Fatal("expected thin-rejected marker")
	}
	if !PhaseDocNeedsFullDetail(tmp, 1) {
		t.Fatal("thin-rejected draft must still need full detail (Confirm hard-stop)")
	}
	if IsPhaseDocEmpty(draft) {
		t.Fatal("thin-rejected draft is not an empty template")
	}
}

func TestBuildPlanPrompt_LibraryTestsSamePhase_F59(t *testing.T) {
	sys, _ := BuildPlanPrompt("pure library with unit tests", "demo")
	if !strings.Contains(sys, "SAME early phase") {
		t.Fatalf("F59: plan prompt must keep library+tests in same early phase:\n%s", sys)
	}
	phaseSys, _ := BuildPhasePrompt("# Plan\n", PhaseInfo{Number: 1, Name: "Core", Goal: "API"}, "demo", "library with go test")
	if !strings.Contains(phaseSys, "required tests") && !strings.Contains(phaseSys, "F59") {
		t.Fatalf("F59: phase prompt should mention required tests in-phase:\n%s", phaseSys)
	}
	if strings.Contains(phaseSys, "[estimated duration]") {
		t.Fatal("phase template must not ask for calendar estimates in the title")
	}
	planSys, _ := BuildPlanPrompt("pure library", "demo")
	if !strings.Contains(planSys, "calendar estimates") && !strings.Contains(planSys, "2-3 days") {
		t.Fatal("plan prompt must forbid calendar estimates in phase titles")
	}
}

func TestBuildPhasePrompt_BehavioralOutcomesStayOnUserSide(t *testing.T) {
	sys, user := BuildPhasePrompt("# Plan\n", PhaseInfo{Number: 1, Name: "Core", Goal: "API"}, "demo", "library with tests")
	if !strings.Contains(user, "PLANNING GRANULARITY") || !strings.Contains(user, "shadow") {
		t.Fatalf("user prompt must ask for behavioral outcomes, not test recipes:\n%s", user)
	}
	if strings.Contains(sys, "PLANNING GRANULARITY") || strings.Contains(sys, "shadow counters") {
		t.Fatal("granularity note must not sit in the system prompt (prefix cache)")
	}
	planSys, planUser := BuildPlanPrompt("pure library with unit tests", "demo")
	if strings.Contains(planSys, "PLANNING GRANULARITY") {
		t.Fatal("plan system prompt must stay cache-stable")
	}
	if !strings.Contains(planUser, "PLANNING GRANULARITY") {
		t.Fatal("plan user prompt should carry the granularity note")
	}
}
