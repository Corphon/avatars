package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTryAutoMarkPlanCriteria_HTTPNeedsTestOK(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "src", "routes"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "src", "routes", "notes.js"), []byte("export default {};\n"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "test"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "test", "notes.test.js"), []byte("test('x', () => {});\n"), 0644)

	plan := `# Project Plan
> **Status**: in-progress
> **Active Phase**: 1
> **Phase Count**: 1

## Success Criteria
- [ ] POST /notes returns 201
- [ ] GET /notes returns list
- [ ] GET /notes/:id returns note
- [ ] npm test passes
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	todo := `# Todo
## Phase 1 Checklist
- [x] Implement notes routes
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644); err != nil {
		t.Fatal(err)
	}

	// buildOK but testOK=false — must not mark HTTP behavior rows (J2-4).
	marked := TryAutoMarkPlanCriteriaWithTests(dir, "wrote notes routes",
		[]string{"src/routes/notes.js", "test/notes.test.js"}, true, false, "express notes api")
	if marked != 0 {
		t.Fatalf("HTTP criteria must stay unchecked when tests fail, marked=%d", marked)
	}
	body, _ := os.ReadFile(filepath.Join(wf, "avatars_plan.md"))
	if strings.Contains(string(body), "- [x] POST") || strings.Contains(string(body), "- [x] GET") {
		t.Fatalf("HTTP rows wrongly checked:\n%s", body)
	}
}

func TestPlanTestsEvidenceOK_NodeTestDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "test"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "test", "notes.test.js"), []byte("ok\n"), 0644)
	if !planTestsEvidenceOK(dir) {
		t.Fatal("expected test/*.test.js to count as test evidence")
	}
}

func TestCriteriaLooksLikeHTTPBehaviorDesc(t *testing.T) {
	if !criteriaLooksLikeHTTPBehaviorDesc("post /notes returns 201") {
		t.Fatal("expected HTTP behavior match")
	}
	if criteriaLooksLikeHTTPBehaviorDesc("project structure matches app/") {
		t.Fatal("structure line must not look like HTTP behavior")
	}
}
