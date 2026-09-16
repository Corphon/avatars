package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF63_CoerceGoLibTestPackage(t *testing.T) {
	src := "package main\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n"
	fixed, ok := coerceGoLibTestPackage("jwtmini/jwt_test.go", src)
	if !ok {
		t.Fatal("expected coerce")
	}
	if parseGoPackageClause(fixed) != "jwtmini" {
		t.Fatalf("got package %q want jwtmini\n%s", parseGoPackageClause(fixed), fixed)
	}
	// cmd/ and root package main tests stay.
	if _, ok := coerceGoLibTestPackage("cmd/jwtmini/main_test.go", src); ok {
		t.Fatal("cmd test must not coerce")
	}
	if _, ok := coerceGoLibTestPackage("jwt_test.go", src); ok {
		t.Fatal("root test must not coerce")
	}
	if reason := goLibTestPackageMainReason("jwtmini/jwt_test.go", src); reason == "" {
		t.Fatal("health must refuse lib package main test")
	}
	if reason := checkFileHealth("jwtmini/jwt_test.go", src); reason == "" {
		t.Fatal("checkFileHealth must refuse")
	}
}

func TestF63_HealGoLibTestPackagesOnDisk(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "jwtmini")
	if err := os.MkdirAll(lib, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(lib, "jwt.go"), []byte("package jwtmini\n\nfunc Sign() {}\n"), 0644)
	testPath := filepath.Join(lib, "jwt_test.go")
	_ = os.WriteFile(testPath, []byte("package main\n\nimport \"testing\"\n\nfunc TestSign(t *testing.T) {}\n"), 0644)
	healed := healGoLibTestPackagesOnDisk(dir)
	if len(healed) != 1 {
		t.Fatalf("healed=%v", healed)
	}
	data, _ := os.ReadFile(testPath)
	if parseGoPackageClause(string(data)) != "jwtmini" {
		t.Fatalf("disk still package %q", parseGoPackageClause(string(data)))
	}
}

func TestF64_ForbidCLIScaffoldSanitize(t *testing.T) {
	task := "golang jwtmini pure library 纯库优先 不要 CLI 不要脚手架"
	kept, _, dropped := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "jwtmini/jwt.go", Content: "package jwtmini\n\nfunc Sign() {}\n"},
		{Path: "main.go", Content: "package main\n\nfunc main() {}\n"},
		{Path: "cmd/jwtmini/main.go", Content: "package main\n\nfunc main() {}\n"},
		{Path: "jwtmini/jwt_test.go", Content: "package main\n\nimport \"testing\"\n\nfunc TestSign(t *testing.T) {}\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
		if f.Path == "jwtmini/jwt_test.go" && parseGoPackageClause(f.Content) != "jwtmini" {
			t.Fatalf("F63 coerce missed: %s", f.Content)
		}
	}
	if paths["main.go"] || paths["cmd/jwtmini/main.go"] {
		t.Fatalf("F64 must drop CLI entrypoints; kept=%v dropped=%v", paths, dropped)
	}
	if !paths["jwtmini/jwt.go"] || !paths["jwtmini/jwt_test.go"] {
		t.Fatalf("library sources must remain; kept=%v", paths)
	}
	if !forbidsCLIScaffold(strings.ToLower(task)) {
		t.Fatal("forbidsCLIScaffold should match")
	}
}

func TestF65_WriteFailureDeliveryAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := writeFailureDeliveryAnswer(dir, "critic: go build failed: found packages jwtmini and main", []string{"jwtmini/jwt.go", "jwtmini/jwt_test.go"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "# Delivery Summary") {
		t.Fatalf("missing Delivery Summary:\n%s", text)
	}
	if !strings.Contains(text, "needs_remediation") {
		t.Fatalf("missing status:\n%s", text)
	}
	if !strings.Contains(text, "found packages") {
		t.Fatalf("missing error:\n%s", text)
	}
}

func TestF66_SuggestNextSteps_FailedNoAllPhasesComplete(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	_ = os.MkdirAll(wf, 0755)
	plan := `> **Active Phase**: 1
> **Phase Count**: 1
## Phases
### Phase 1: Core
- **Goal**: lib
`
	todo := `# Todo
> **Phase**: 1 of 1
## Phase 1 Checklist
- [x] one
`
	_ = os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644)
	_ = os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte(todo), 0644)

	steps := SuggestNextSteps(root, NextStepInput{
		Status:              "failed",
		Summary:             "critic: go build failed",
		BuildOK:             false,
		BuildOKKnown:        true,
		PhaseAdvanceBlocked: "build_failed",
	})
	joined := strings.Join(steps, "\n")
	if strings.Contains(joined, "All phases complete") {
		t.Fatalf("F66: must not claim All phases complete on failed, got %v", steps)
	}
	stepsOK := SuggestNextSteps(root, NextStepInput{
		Status:       "completed",
		BuildOK:      true,
		BuildOKKnown: true,
	})
	if !strings.Contains(strings.Join(stepsOK, "\n"), "All phases complete") {
		t.Fatalf("green completed should still say All phases complete, got %v", stepsOK)
	}
}
