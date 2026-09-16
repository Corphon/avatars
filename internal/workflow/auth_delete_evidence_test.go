package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuccessCriteriaPhaseNum_EnDash(t *testing.T) {
	if n := SuccessCriteriaPhaseNum("phase 2 – unauthenticated requests return 401"); n != 2 {
		t.Fatalf("en-dash: got %d want 2", n)
	}
	if n := SuccessCriteriaPhaseNum("Phase 2: auth"); n != 2 {
		t.Fatalf("colon: got %d want 2", n)
	}
	if n := SuccessCriteriaPhaseNum("go build ./..."); n != 0 {
		t.Fatalf("unscoped: got %d want 0", n)
	}
}

func TestCriteriaAuthDeleteEvidence_BlocksWithoutCode(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "docs", "workflow"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "cmd", "server"), 0755)
	plan := `# Plan
> **Active Phase**: 1
> **Phase Count**: 2

### Phase 1: CRUD
- **Goal**: shipments CRUD

### Phase 2: Authentication & Deletion
- **Goal**: API key authentication and DELETE endpoint

## Success Criteria
- [ ] Phase 1 – all six endpoints return correct JSON
- [ ] Phase 2 – unauthenticated requests return 401; DELETE /shipments/{id} returns 204
`
	_ = os.WriteFile(filepath.Join(dir, "docs", "workflow", "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(dir, "cmd", "server", "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	desc := "phase 2 – unauthenticated requests return 401; delete /shipments/{id} returns 204"
	if CriteriaAuthDeleteEvidenceOK(dir, desc) {
		t.Fatal("expected auth/DELETE evidence missing")
	}
	if !criteriaOverlapsLaterPhaseGoals(dir, desc, 1) {
		t.Fatal("Phase 2 criteria must overlap later goals while Active=1")
	}
	n := TryAutoMarkPlanCriteriaWithTests(dir, "shipments ok", []string{"cmd/server/main.go"}, true, true, "shiptrace")
	if n > 0 {
		body, _ := os.ReadFile(filepath.Join(dir, "docs", "workflow", "avatars_plan.md"))
		if strings.Contains(string(body), "- [x] Phase 2") {
			t.Fatalf("Phase 2 criteria must not auto-mark without auth/DELETE code; marked=%d\n%s", n, body)
		}
	}
}

func TestCriteriaAuthDeleteEvidence_OKWithCode(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "internal", "auth"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "internal", "handlers"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "internal", "auth", "key.go"), []byte(`
package auth
import "net/http"
func Middleware(key string, next http.Handler) http.Handler {
  return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    if r.Header.Get("X-API-Key") != key { w.WriteHeader(401); return }
    next.ServeHTTP(w, r)
  })
}
`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "internal", "handlers", "delete.go"), []byte(`
package handlers
import "net/http"
func DeleteShipment() http.HandlerFunc {
  return func(w http.ResponseWriter, r *http.Request) {
    if r.Method == http.MethodDelete { w.WriteHeader(204) }
  }
}
`), 0644)
	desc := "phase 2 – unauthenticated return 401; delete /shipments/{id} returns 204"
	if !CriteriaAuthDeleteEvidenceOK(dir, desc) {
		t.Fatal("expected auth+DELETE evidence present")
	}
}

func TestUnmarkSuccessCriteriaMissingAuthDeleteEvidence(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.MkdirAll(filepath.Join(dir, "cmd", "server"), 0755)
	plan := `# Plan
> **Active Phase**: 2

## Success Criteria
- [x] Phase 2 – unauthenticated requests return 401; DELETE /shipments/{id} returns 204
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(dir, "cmd", "server", "main.go"), []byte("package main\nfunc main(){}\n"), 0644)
	n := UnmarkSuccessCriteriaMissingAuthDeleteEvidence(dir)
	if n != 1 {
		t.Fatalf("unmark count=%d want 1", n)
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if !strings.Contains(string(body), "- [ ] Phase 2") {
		t.Fatalf("expected unmark:\n%s", body)
	}
}

func TestPhaseDocsComplete_RejectsActiveDeferredStub(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `# Plan
> **Active Phase**: 2
> **Phase Count**: 2
> **Phase Docs**: complete

### Phase 1: A
- **Goal**: crud
### Phase 2: B
- **Goal**: auth
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte("# Phase 1\n## Tasks\n### 1. X\n- [x] done\n"), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase2.md"), []byte(buildPhaseDocSkeleton(2, "B", "auth", true)), 0644)
	if !IsPhaseDocDeferredStub(string(mustRead(t, filepath.Join(wf, "phase2.md")))) {
		t.Fatal("expected deferred stub")
	}
	if PhaseDocsComplete(dir) {
		t.Fatal("Active Phase 2 deferred stub must make PhaseDocsComplete false")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIsChecklistItemSatisfied_HandlerLayout(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "internal", "handlers"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "internal", "handlers", "shipments.go"), []byte("package handlers\n"), 0644)
	if !isChecklistItemSatisfied(dir, "Handler Implementation (bottom-up, grouped by endpoint)", "") {
		t.Fatal("expected handlers layout to satisfy generic handler checklist item")
	}
}
