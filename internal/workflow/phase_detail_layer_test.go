package workflow

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestConstructPhasesLayered_OnlyExpandsActive(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := `> **Active Phase**: 1
> **Phase Count**: 3
### Phase 1: Scaffold
- **Goal**: skeleton
### Phase 2: Auth
- **Goal**: jwt
### Phase 3: CRUD
- **Goal**: api
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("# Todo\n> **Phase**: 1\n"), 0644)

	calls := 0
	gen, reused, stubbed, err := ConstructPhasesLayered(root, func(sys, usr string) (string, error) {
		calls++
		return `# Phase 1 · Scaffold
## Overview
Full detail for active phase.
## Tasks
### 1. Skeleton
- [ ] Create app/main.py
## Verification
- [ ] health ok
`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("LLM calls=%d want 1 (active only)", calls)
	}
	if len(gen) != 1 || gen[0] != "phase1.md" {
		t.Fatalf("generated=%v", gen)
	}
	if len(stubbed) < 2 {
		t.Fatalf("stubbed=%v reused=%v", stubbed, reused)
	}
	for _, n := range []int{2, 3} {
		body, err := os.ReadFile(filepath.Join(wf, "phase"+strconv.Itoa(n)+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if !IsPhaseDocDeferredStub(string(body)) {
			t.Fatalf("phase%d not deferred stub", n)
		}
		if strings.Contains(string(body), "Create app/main.py") {
			t.Fatalf("phase%d should not contain Active Phase tasks", n)
		}
	}
	p1, _ := os.ReadFile(filepath.Join(wf, "phase1.md"))
	if IsPhaseDocDeferredStub(string(p1)) {
		t.Fatal("phase1 still deferred")
	}
	if !PhaseDocsComplete(root) {
		t.Fatal("stubs must satisfy PhaseDocsComplete")
	}
}

func TestExpandActivePhaseDetail_OnAdvance(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 2
> **Phase Count**: 2
### Phase 1: A
- **Goal**: a
### Phase 2: B
- **Goal**: auth jwt
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("# Todo\n> **Phase**: 2\n"), 0644)
	title, goal := "B", "auth jwt"
	stub := buildPhaseDocSkeleton(2, title, goal, true)
	_ = os.WriteFile(filepath.Join(wf, "phase2.md"), []byte(stub), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(`# Phase 1
`+phaseDetailFullMarker+`
## Overview
done
## Tasks
### 1. A
- [x] done
## Verification
- [x] ok
`), 0644)

	if !PhaseDocNeedsFullDetail(root, 2) {
		t.Fatal("phase2 stub should need full detail")
	}
	calls := 0
	ok, n, err := ExpandActivePhaseDetail(root, func(sys, usr string) (string, error) {
		calls++
		return `# Phase 2 · Auth
## Overview
JWT detail from disk context.
## Tasks
### 1. Security
- [ ] Add JWT middleware
## Verification
- [ ] login works
`, nil
	})
	if err != nil || !ok || n != 2 || calls != 1 {
		t.Fatalf("ok=%v n=%d calls=%d err=%v", ok, n, calls, err)
	}
	body, _ := os.ReadFile(filepath.Join(wf, "phase2.md"))
	if IsPhaseDocDeferredStub(string(body)) {
		t.Fatal("phase2 still stub after expand")
	}
	if !strings.Contains(string(body), "JWT middleware") {
		t.Fatalf("missing expanded content:\n%s", body)
	}
}
