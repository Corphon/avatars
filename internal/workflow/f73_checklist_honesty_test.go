package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF73_HonestTestCountsNotInflated(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "durparse")
	_ = os.MkdirAll(lib, 0755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/durparse\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(lib, "durparse.go"), []byte("package durparse\n\nfunc Parse(s string) (int, error) { return 0, nil }\n"), 0644)
	tests := "package durparse\n\nimport \"testing\"\n" +
		"func TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n" +
		"func TestC(t *testing.T) {}\nfunc TestD(t *testing.T) {}\n"
	_ = os.WriteFile(filepath.Join(lib, "durparse_test.go"), []byte(tests), 0644)

	todo := `# Active Workspace

> **Phase**: 1

## Current Task
- [ ] Implement Phase 1: Core library + unit tests

## Phase 1 Checklist (active)
- [ ] 1. Module setup (0/1)
- [ ] 2. Core library implementation (0/10)
- [x] 3. Unit tests (25/25)
- [ ] 4. Verification pass (0/3)

## Completed

`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
	_ = os.WriteFile(todoPath, []byte(todo), 0644)

	n := reconcileChecklistCountHonesty(dir)
	if n == 0 {
		t.Fatal("expected checklist honesty rewrites")
	}
	body, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "(25/25)") {
		t.Fatalf("inflated 25/25 must not survive:\n%s", s)
	}
	if !strings.Contains(s, "(4/4)") {
		t.Fatalf("expected honest (4/4) test count, got:\n%s", s)
	}
	if strings.Contains(s, "Core library implementation (0/10)") {
		t.Fatalf("core impl should be marked from disk sources:\n%s", s)
	}
	if strings.Contains(s, "Verification pass (0/3)") {
		t.Fatalf("verification should mark when compile+tests green:\n%s", s)
	}
}

func TestF73_MarkCheckboxDoneHonestUsesRealTestCount(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "pkg")
	_ = os.MkdirAll(lib, 0755)
	_ = os.WriteFile(filepath.Join(lib, "x_test.go"), []byte(
		"package pkg\nimport \"testing\"\nfunc TestOne(t *testing.T) {}\nfunc TestTwo(t *testing.T) {}\nfunc TestThree(t *testing.T) {}\n"), 0644)
	got := markCheckboxDoneHonest("- [ ] 3. Unit tests (0/25)", dir)
	if !strings.Contains(got, "(3/3)") {
		t.Fatalf("expected (3/3), got %q", got)
	}
	if strings.Contains(got, "(25/25)") {
		t.Fatalf("must not invent 25/25: %q", got)
	}
}

func TestF73_ReplaceTrailingChecklistCount(t *testing.T) {
	got := replaceTrailingChecklistCount("- [x] Unit tests (25/25)", 4, 4)
	if got != "- [x] Unit tests (4/4)" {
		t.Fatalf("got %q", got)
	}
}

func TestF73_DocumentationMarkedFromReadmeAndHeaders(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "slugifyx")
	_ = os.MkdirAll(lib, 0755)
	header := "// Package slugifyx converts titles into URL slugs.\n//\n// Example: Slugify(\"Hello\") => \"hello\"\npackage slugifyx\n\nfunc Slugify(s string) string { return s }\n"
	_ = os.WriteFile(filepath.Join(lib, "slugify.go"), []byte(header), 0644)
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("# slugifyx\n\nLibrary for URL slugs.\n"), 0644)

	todo := `# Active Workspace

> **Phase**: 1

## Current Task
- [ ] Implement Phase 1: Library Implementation & Tests

## Phase 1 Checklist (active)
- [x] 1. Module Setup (1/1)
- [x] 2. Core Library Implementation (1/1)
- [x] 3. Unit Tests (1/1)
- [>] 4. Documentation (0/2)

## Completed

`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
	_ = os.WriteFile(todoPath, []byte(todo), 0644)

	n := reconcileChecklistCountHonesty(dir)
	if n == 0 {
		t.Fatal("expected documentation checklist rewrite")
	}
	body, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "Documentation (0/2)") || strings.Contains(s, "- [>] 4. Documentation") {
		t.Fatalf("documentation should be marked from README+header:\n%s", s)
	}
	if !strings.Contains(s, "- [x]") || !strings.Contains(s, "Documentation") {
		t.Fatalf("expected marked documentation row:\n%s", s)
	}
}

func TestF73_DocumentationMarkedFromPythonDocstring(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "slugifyx")
	_ = os.MkdirAll(pkg, 0755)
	py := "\"\"\"slugifyx: turn titles into URL slugs.\"\"\"\n\ndef slugify(s):\n    return s.lower()\n"
	_ = os.WriteFile(filepath.Join(pkg, "slugify.py"), []byte(py), 0644)

	todo := `# Active Workspace

> **Phase**: 1

## Current Task
- [ ] Phase 1

## Phase 1 Checklist (active)
- [>] 4. Documentation (0/2)

## Completed

`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
	_ = os.WriteFile(todoPath, []byte(todo), 0644)

	if reconcileChecklistCountHonesty(dir) == 0 {
		t.Fatal("python module docstring should count as documentation")
	}
	body, _ := os.ReadFile(todoPath)
	if strings.Contains(string(body), "- [>] 4. Documentation") {
		t.Fatalf("expected python docstring to mark docs:\n%s", body)
	}
}

func TestF73_DocumentationMarkedFromJSRustAndJava(t *testing.T) {
	cases := []struct {
		name, rel, body string
	}{
		{"js", "slugifyx/index.js", "/** slugifyx: turn titles into URL slugs. */\nexport function slugify(s) { return s; }\n"},
		{"rust", "src/lib.rs", "//! slugifyx: turn titles into URL slugs.\n//!\n//! Pure library, no bin.\npub fn slugify(s: &str) -> String { s.to_string() }\n"},
		{"java", "src/Slugify.java", "/** slugifyx: turn titles into URL slugs. */\npublic final class Slugify {\n  public static String slugify(String s) { return s; }\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, filepath.FromSlash(tc.rel))
			_ = os.MkdirAll(filepath.Dir(path), 0755)
			_ = os.WriteFile(path, []byte(tc.body), 0644)
			todo := `# Active Workspace

> **Phase**: 1

## Phase 1 Checklist (active)
- [>] 4. Documentation (0/2)

`
			todoPath := filepath.Join(dir, DocPaths["todo"])
			_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
			_ = os.WriteFile(todoPath, []byte(todo), 0644)
			if reconcileChecklistCountHonesty(dir) == 0 {
				t.Fatal("header docs should mark documentation")
			}
			body, _ := os.ReadFile(todoPath)
			if strings.Contains(string(body), "- [>] 4. Documentation") {
				t.Fatalf("expected docs marked:\n%s", body)
			}
		})
	}
}

func TestF73_TestsPassCriteriaMarkedWithoutFilenameKeyword(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	lib := filepath.Join(dir, "slugifyx")
	_ = os.MkdirAll(lib, 0755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/slugifyx\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(lib, "slugify.go"), []byte("package slugifyx\n\nfunc Slugify(s string) string { return s }\n"), 0644)
	_ = os.WriteFile(filepath.Join(lib, "slugify_test.go"), []byte("package slugifyx\nimport \"testing\"\nfunc TestA(t *testing.T) {}\n"), 0644)
	plan := `# Project Plan
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1

## Success Criteria
- [ ] ` + "`go test ./...`" + ` passes with all unit tests green
- [ ] cargo test passes
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("## Phase 1 Checklist\n- [x] Library\n"), 0644)

	if n := TryAutoMarkPlanCriteriaWithTests(dir, "implemented library",
		[]string{"slugifyx/slugify.go"}, true, false); n != 0 {
		t.Fatalf("must not mark tests-pass when testOK=false, marked=%d", n)
	}
	n := TryAutoMarkPlanCriteriaWithTests(dir, "implemented library",
		[]string{"slugifyx/slugify.go"}, true, true)
	if n == 0 {
		t.Fatal("expected go test criteria marked from green suite, not filenames")
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	s := string(body)
	if !strings.Contains(s, "- [x] `go test") {
		t.Fatalf("go test criteria should be checked:\n%s", s)
	}
	if strings.Contains(s, "- [x] cargo test passes") {
		t.Fatalf("cargo test must not check on a Go-only tree:\n%s", s)
	}
}

func TestF73_DocumentationUnmarkedWithoutEvidence(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "pkg")
	_ = os.MkdirAll(lib, 0755)
	_ = os.WriteFile(filepath.Join(lib, "x.go"), []byte("package pkg\n\nfunc F() {}\n"), 0644)

	todo := `# Active Workspace

> **Phase**: 1

## Phase 1 Checklist (active)
- [ ] 4. Documentation (0/2)

`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
	_ = os.WriteFile(todoPath, []byte(todo), 0644)

	_ = reconcileChecklistCountHonesty(dir)
	body, _ := os.ReadFile(todoPath)
	if !strings.Contains(string(body), "- [ ] 4. Documentation") {
		t.Fatalf("bare source without header/README must not mark docs:\n%s", body)
	}
}

func TestF109_RaceCriteriaNotMarkedFromPlainTestOK(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package x\n\nfunc F() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "lib_test.go"), []byte("package x\nimport \"testing\"\nfunc TestF(t *testing.T) {}\n"), 0644)
	plan := `# Project Plan
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1

## Success Criteria
- [ ] ` + "`go test ./...`" + ` passes
- [ ] ` + "`go test -race ./...`" + ` — no race conditions
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("## Phase 1 Checklist\n- [x] Library\n"), 0644)
	n := TryAutoMarkPlanCriteriaWithTests(dir, "implemented library", []string{"lib.go", "lib_test.go"}, true, true)
	if n == 0 {
		t.Fatal("expected plain go test criteria to mark")
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	s := string(body)
	if strings.Contains(s, "- [x] `go test -race") {
		t.Fatalf("F109: race criteria must not [x] from testOK alone:\n%s", s)
	}
	if !strings.Contains(s, "- [ ] `go test -race") {
		t.Fatalf("race line should remain unchecked:\n%s", s)
	}
}

func TestPlanCriteriaMarkedFromExportedAPIWhenTestsGreen(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module bloomx\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "bloom.go"), []byte(`package bloomx

type Filter struct{}

func NewWithEstimates(n uint, fpRate float64) (*Filter, error) { return NewWithBits(1, 1) }
func NewWithBits(m, k uint) (*Filter, error) { return &Filter{}, nil }
func (f *Filter) Add(data []byte) {}
func (f *Filter) AddString(s string) {}
func (f *Filter) MightContain(data []byte) bool { return true }
func (f *Filter) MightContainString(s string) bool { return true }
`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "bloom_test.go"), []byte("package bloomx\nimport \"testing\"\nfunc TestAdd(t *testing.T) {}\n"), 0644)
	plan := `# Project Plan
> **Active Phase**: 1
> **Phase Count**: 1

## Success Criteria
- [ ] ` + "`go test ./...`" + ` passes (all tests green)
- [ ] bloomx.NewWithEstimates(1000, 0.01) and bloomx.NewWithBits(8000, 7) both construct usable filters
- [ ] For every element added via Add/AddString, MightContain/MightContainString returns true
- [ ] MightContain on random absent elements never panics
- [ ] Concurrent Add + MightContain from multiple goroutines completes without race (verified by ` + "`go test -race`" + `)
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("## Phase 1 Checklist\n- [x] Library\n"), 0644)
	n := TryAutoMarkPlanCriteriaWithTests(dir, "implemented library", []string{"bloom.go", "bloom_test.go"}, true, true)
	if n < 3 {
		t.Fatalf("expected API/behavior criteria marked from green suite, marked=%d", n)
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	s := string(body)
	if !strings.Contains(s, "- [x] bloomx.NewWithEstimates") {
		t.Fatalf("constructor criteria should be checked:\n%s", s)
	}
	if !strings.Contains(s, "- [x] For every element added") {
		t.Fatalf("Add/MightContain criteria should be checked:\n%s", s)
	}
	if !strings.Contains(s, "- [x] MightContain on random absent") {
		t.Fatalf("never-panic criteria should be checked:\n%s", s)
	}
	if strings.Contains(s, "- [x] Concurrent Add") {
		t.Fatalf("race criteria must stay open:\n%s", s)
	}
}

func TestSyncPhaseCriteriaMarksToPlan(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(`# Project Plan
> **Active Phase**: 1
> **Phase Count**: 1

## Success Criteria
- [ ] bloomx.NewWithEstimates(1000, 0.01) and bloomx.NewWithBits(8000, 7) both construct usable filters
- [ ] For every element added via Add/AddString, MightContain/MightContainString returns true
- [ ] Concurrent Add + MightContain from multiple goroutines completes without race (verified by `+"`go test -race`"+`)
`), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(`# Phase 1
## Scope & Success Criteria
- [x] bloomx.NewWithEstimates(1000, 0.01) and bloomx.NewWithBits(8000, 7) both construct usable filters
- [x] For every element added via Add/AddString, MightContain/MightContainString returns true
- [x] Concurrent Add + MightContain from multiple goroutines completes without race (verified by `+"`go test -race`"+`)
`), 0644)
	n := SyncPhaseCriteriaMarksToPlan(dir)
	if n < 2 {
		t.Fatalf("expected plan criteria copied from phase [x], marked=%d", n)
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	s := string(body)
	if !strings.Contains(s, "- [x] bloomx.NewWithEstimates") {
		t.Fatalf("constructor line should sync:\n%s", s)
	}
	if !strings.Contains(s, "- [x] For every element added") {
		t.Fatalf("Add/MightContain line should sync:\n%s", s)
	}
	if strings.Contains(s, "- [x] Concurrent Add") {
		t.Fatalf("race line must stay open:\n%s", s)
	}
}

func TestTryAutoMarkSyncsPhaseChecksWhenKeywordsMiss(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/lib\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\nfunc Helper() {}\n"), 0644)
	plan := `# Project Plan
> **Active Phase**: 1
> **Phase Count**: 1

## Success Criteria
- [ ] constructors return usable filters
- [ ] membership queries return true for added elements
- [ ] Concurrent access completes without race (verified by ` + "`go test -race`" + `)
`
	phase := `# Phase 1
## Scope & Success Criteria
- [x] constructors return usable filters
- [x] membership queries return true for added elements
- [x] Concurrent access completes without race (verified by ` + "`go test -race`" + `)
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase), 0644)
	n := TryAutoMarkPlanCriteriaWithTests(dir, "align checklists", []string{"docs/workflow/phase1.md"}, true, true)
	if n < 2 {
		t.Fatalf("expected phase [x] copied onto plan when keyword auto-mark misses, marked=%d", n)
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	s := string(body)
	if !strings.Contains(s, "- [x] constructors return usable filters") {
		t.Fatalf("constructor line should sync from phase:\n%s", s)
	}
	if !strings.Contains(s, "- [x] membership queries return true") {
		t.Fatalf("membership line should sync from phase:\n%s", s)
	}
	if strings.Contains(s, "- [x] Concurrent access") {
		t.Fatalf("race line must stay open:\n%s", s)
	}
}

func TestF117_UncheckTestsWithZeroEvidence(t *testing.T) {
	dir := t.TempDir()
	todo := `# Active Workspace

> **Phase**: 1

## Phase 1 Checklist (active)
- [x] Write Unit Tests (0/12)
- [x] Supporting Test Files (0/2)
- [ ] Core library implementation (0/10)

## Completed

`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
	_ = os.WriteFile(todoPath, []byte(todo), 0644)
	n := reconcileChecklistCountHonesty(dir)
	if n == 0 {
		t.Fatal("expected [x] test rows with 0 evidence to uncheck")
	}
	body, _ := os.ReadFile(todoPath)
	s := string(body)
	if strings.Contains(s, "- [x] Write Unit Tests") {
		t.Fatalf("F117: unit tests [x] with 0 tests must uncheck:\n%s", s)
	}
	if strings.Contains(s, "- [x] Supporting Test Files") {
		t.Fatalf("F117: test files [x] with 0 tests must uncheck:\n%s", s)
	}
}

func TestReconcileMarksCoreTypesClockMethodsWhenSourcesExist(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/tokenbucket\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "limiter.go"), []byte("package tokenbucket\n\nfunc Allow() bool { return true }\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "fake_clock.go"), []byte("package tokenbucket\n\ntype Clock interface{ Now() }\n"), 0644)
	tests := "package tokenbucket\n\nimport \"testing\"\n" +
		"func TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\nfunc TestC(t *testing.T) {}\n"
	_ = os.WriteFile(filepath.Join(dir, "limiter_test.go"), []byte(tests), 0644)

	todo := `# Active Workspace

> **Phase**: 1

## Current Task
- [ ] Implement Phase 1: Core library + tests

## Phase 1 Checklist (active)
- [ ] 1. Core types and clock abstraction (0/5)
- [ ] 2. Limiter implementation (0/4)
- [ ] 3. Wait and Stats methods (0/3)
- [ ] 4. Fake clock for testing (0/4)
- [ ] 5. Unit tests (0/8)

## Completed

`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
	_ = os.WriteFile(todoPath, []byte(todo), 0644)
	if n := reconcileChecklistCountHonesty(dir); n == 0 {
		t.Fatal("expected checklist rows to mark from on-disk sources")
	}
	body, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, leftover := range []string{
		"Core types and clock abstraction (0/5)",
		"Limiter implementation (0/4)",
		"Wait and Stats methods (0/3)",
	} {
		if strings.Contains(s, leftover) {
			t.Fatalf("expected %q marked from disk sources:\n%s", leftover, s)
		}
	}
}

func TestReconcileMarksLocalizedDeliveryGroupsFromDisk(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/ttlcache\n\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "ttlcache.go"), []byte("package ttlcache\n\nfunc Get(k string) (any, bool) { return nil, false }\nfunc Set(k string, v any) {}\n"), 0644)
	tests := "package ttlcache\n\nimport \"testing\"\n" +
		"func TestGet(t *testing.T) {}\nfunc TestSet(t *testing.T) {}\n" +
		"func TestDelete(t *testing.T) {}\nfunc TestTTL(t *testing.T) {}\n"
	_ = os.WriteFile(filepath.Join(dir, "ttlcache_test.go"), []byte(tests), 0644)

	todo := `# Active Workspace

> **Phase**: 1

## Current Task
- [ ] Implement Phase 1: in-process cache + unit tests

## Phase 1 Checklist (active)
- [ ] 1. 初始化模块与包结构 (0/2)
- [ ] 2. 定义公开 API 与内部数据结构 (0/4)
- [ ] 3. 实现核心方法 (0/4)
- [ ] 4. 编写测试 (0/2)

## Completed

`
	todoPath := filepath.Join(dir, DocPaths["todo"])
	_ = os.MkdirAll(filepath.Dir(todoPath), 0755)
	_ = os.WriteFile(todoPath, []byte(todo), 0644)

	n := reconcileChecklistCountHonesty(dir)
	if n == 0 {
		t.Fatal("expected localized checklist groups to mark from disk")
	}
	body, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, leftover := range []string{
		"初始化模块与包结构 (0/2)",
		"定义公开 API 与内部数据结构 (0/4)",
		"实现核心方法 (0/4)",
		"编写测试 (0/2)",
	} {
		if strings.Contains(s, leftover) {
			t.Fatalf("expected %q marked from disk sources:\n%s", leftover, s)
		}
	}
	if !strings.Contains(s, "- [x]") {
		t.Fatalf("expected checked boxes, got:\n%s", s)
	}
}

func TestReconcileUnmarksGoTestWhenSuiteFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/redsuite\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package redsuite\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib_test.go"), []byte("package redsuite\n\nimport \"testing\"\nfunc TestBoom(t *testing.T) { t.Fatal(\"red\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	todo := `# Active Workspace

> **Phase**: 1

## Phase 1 Checklist (active)
- [x] 5. Verify Build and Test Suite (10/10)
- [x] Run go test ./...

## Completed
`
	if err := os.WriteFile(filepath.Join(dir, DocPaths["todo"]), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}
	phase := `# Phase 1

## Success Criteria
- [x] go test ./... passes with all unit tests green
- [x] go test -race ./... passes
`
	if err := os.WriteFile(filepath.Join(wf, "phase1.md"), []byte(phase), 0644); err != nil {
		t.Fatal(err)
	}

	if n := ReconcileChecklistHonesty(dir); n == 0 {
		t.Fatal("expected failing tests to unmark go-test claims")
	}
	todoBody, err := os.ReadFile(filepath.Join(dir, DocPaths["todo"]))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(todoBody), "- [x] 5. Verify") || strings.Contains(string(todoBody), "- [x] Run go test") {
		t.Fatalf("todo still claims tests passed:\n%s", todoBody)
	}
	phaseBody, err := os.ReadFile(filepath.Join(wf, "phase1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(phaseBody), "- [x] go test") {
		t.Fatalf("phase1 still claims go test green:\n%s", phaseBody)
	}
}
