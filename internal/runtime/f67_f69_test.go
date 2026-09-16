package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestF69_ForbidTmpMainEntrypoint(t *testing.T) {
	task := "golang jwtmini pure library 不要 CLI 不要脚手架"
	kept, _, dropped := sanitizeBuilderPathsForTask([]builderCodeFile{
		{Path: "jwtmini/jwt.go", Content: "package jwtmini\n\nfunc Sign() {}\n"},
		{Path: "tmp/b64check/main.go", Content: "package main\n\nfunc main() {}\n"},
		{Path: "scratch/probe/main.py", Content: "print(1)\n"},
	}, task)
	paths := map[string]bool{}
	for _, f := range kept {
		paths[f.Path] = true
	}
	if paths["tmp/b64check/main.go"] || paths["scratch/probe/main.py"] {
		t.Fatalf("F69 must drop tmp/scratch mains; kept=%v dropped=%v", paths, dropped)
	}
	if !paths["jwtmini/jwt.go"] {
		t.Fatal("library source must remain")
	}
	if !isForbiddenCLIEntrypointPath("tmp/b64check/main.go") {
		t.Fatal("tmp main must be forbidden")
	}
}

func TestF68_TestCoverageCollapse(t *testing.T) {
	big := "package jwtmini\n\nimport \"testing\"\n" +
		"func TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\nfunc TestC(t *testing.T) {}\n" +
		"func TestD(t *testing.T) {}\nfunc TestE(t *testing.T) {}\nfunc TestF(t *testing.T) {}\n"
	small := "package jwtmini\n\nimport \"testing\"\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n"
	if !testCoverageCollapse("jwtmini/jwt_test.go", big, small) {
		t.Fatal("expected collapse detection")
	}
	if testCoverageCollapse("jwtmini/jwt_test.go", small, big) {
		t.Fatal("growth must not count as collapse")
	}
	if testCoverageCollapse("jwtmini/jwt.go", big, small) {
		t.Fatal("non-test paths must not use this gate")
	}
}

func TestF67_ReconcileFinalDeliveryAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "jwtmini"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jwtmini", "jwt.go"), []byte("package jwtmini\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jwtmini", "jwt_test.go"), []byte("package jwtmini\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stale := "# Delivery Summary\n\n构建失败，需要修复。needs_remediation。jwt_test.go:167 syntax error\n"
	if err := reconcileFinalDeliveryAnswer(dir, "completed_unverified", stale, true, true, "",
		[]string{"jwtmini/jwt.go", "jwtmini/jwt_test.go"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "completed_unverified") {
		t.Fatalf("missing final status:\n%s", text)
	}
	if !strings.Contains(text, "build_ok=true") {
		t.Fatalf("missing build_ok=true:\n%s", text)
	}
	if !strings.Contains(text, "Correction Note") {
		t.Fatalf("expected correction note for stale failure:\n%s", text)
	}
	if !strings.Contains(text, "jwtmini/jwt.go") {
		t.Fatalf("missing changed files:\n%s", text)
	}
}

func TestF90_ReconcileFlagsToolchainAndFixedLies(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hashringx.go"), []byte("package hashringx\n"), 0644); err != nil {
		t.Fatal(err)
	}
	synth := "已修复迁移。当前环境无 Go 工具链，无法验证。已把 `internal/zzdiag/zzdiag_test.go` 改成 package zzdiag。"
	health := "go test failed: TestMigration: 0 keys moved"
	if err := reconcileFinalDeliveryAnswer(dir, "needs_remediation", synth, false, true, health,
		[]string{"hashringx.go", "internal/zzdiag/zzdiag_test.go"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "F90") {
		t.Fatalf("expected F90 correction notes:\n%s", text)
	}
	if !strings.Contains(text, "no toolchain") && !strings.Contains(text, "language toolchain") {
		t.Fatalf("expected no-toolchain lie correction:\n%s", text)
	}
	if !strings.Contains(text, "do not call it fixed") {
		t.Fatalf("expected fixed-lie correction:\n%s", text)
	}
	if !strings.Contains(text, "NOT ON DISK") {
		t.Fatalf("expected deleted zzdiag marked missing:\n%s", text)
	}
}

func TestF90_PreciseEditClaimWhileHealthRed(t *testing.T) {
	_, fixed := synthesizerHonestyFlags(
		"Applied precise_edit: n > int64(total). Tests should pass now.",
		"go test failed: mismatched types",
		true,
	)
	if !fixed {
		t.Fatal("precise_edit claim while health is red must be a fixed-lie")
	}
	_, ok := synthesizerHonestyFlags("Delivery Summary: still compiling.", "go test failed", true)
	if ok {
		t.Fatal("honest red status must not be flagged as a fixed-lie")
	}
}

func TestF93_DetectMismatchedGoUsageExample(t *testing.T) {
	dir := t.TempDir()
	src := `package breakgate

type Config struct {
	Name      string
	Threshold int
}

type Breaker struct{}

func New(name string, cfg Config) (*Breaker, error) {
	return &Breaker{}, nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "breaker.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	synth := "用法：`brk, err := New(Config{Name: \"payment\", Threshold: 5})`"
	got := detectMismatchedUsageExamples(dir, synth)
	if len(got) == 0 {
		t.Fatal("expected F93 mismatch for New(Config{...}) vs New(name, cfg)")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "New(name string, cfg Config)") {
		t.Fatalf("expected disk signature in correction:\n%s", joined)
	}
	if !strings.Contains(joined, "New(Config{...})") {
		t.Fatalf("expected model snippet in correction:\n%s", joined)
	}
	if sigs := formatAuthoritativeExportSignatures(dir); !strings.Contains(sigs, "func New(name string, cfg Config)") {
		t.Fatalf("expected synthesizer API evidence, got %q", sigs)
	}

	if err := reconcileFinalDeliveryAnswer(dir, "completed", synth, true, true, "ok",
		[]string{"breaker.go"}); err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(answer)
	if !strings.Contains(text, "## Correct API") || !strings.Contains(text, "F93") {
		t.Fatalf("expected Correct API section:\n%s", text)
	}
}

func TestF93_DetectMismatchedPythonUsageExample(t *testing.T) {
	dir := t.TempDir()
	src := "def create(name, threshold):\n    return name, threshold\n"
	if err := os.WriteFile(filepath.Join(dir, "breaker.py"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectMismatchedUsageExamples(dir, "create({name: 'payment', threshold: 5})")
	if len(got) == 0 {
		t.Fatal("expected Python object-literal mismatch")
	}
}

func TestF94_ReconcileTruncatesRunesNotBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 1900; i++ {
		b.WriteString("测")
	}
	synth := b.String()
	if err := reconcileFinalDeliveryAnswer(dir, "completed", synth, true, true, "ok",
		[]string{"lib.go"}); err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(answer) {
		t.Fatal("F94: answer.md must stay valid UTF-8 after truncation")
	}
	if strings.Contains(string(answer), "\uFFFD") {
		t.Fatal("F94: must not insert replacement runes")
	}
}

func TestReconcileOmitsRedSynthAndWritesPlanProgress(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := "> **Active Phase**: 1\n> **Phase Count**: 2\n### Phase 1: Core\n- **Goal**: lib\n"
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "limiter.go"), []byte("package tokenbucket\n"), 0644); err != nil {
		t.Fatal(err)
	}
	synth := "This is the first phase and the only phase. Everything is done except tests are still red."
	if err := reconcileFinalDeliveryAnswer(dir, "needs_remediation", synth, false, true, "go test failed: exit status 1",
		[]string{"limiter.go"}); err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(answer)
	if strings.Contains(text, "only phase") {
		t.Fatalf("red delivery must not copy unique-phase synth notes:\n%s", text)
	}
	if !strings.Contains(text, "Active phase: 1 of 2") {
		t.Fatalf("expected plan progress, got:\n%s", text)
	}
}

func TestReconcileStripsUniquePhaseClaimWhenPlanHasTwo(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	plan := "> **Active Phase**: 1\n> **Phase Count**: 2\n### Phase 1: Core\n- **Goal**: lib\n"
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\n"), 0644)
	synth := "This is the first phase and the only phase. Everything is done."
	if err := reconcileFinalDeliveryAnswer(dir, "completed", synth, true, true, "",
		[]string{"lib.go"}); err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(answer)
	if strings.Contains(text, "only phase") {
		t.Fatalf("green delivery must not keep unique-phase claim:\n%s", text)
	}
	if !strings.Contains(text, "Active phase: 1 of 2") {
		t.Fatalf("expected plan progress, got:\n%s", text)
	}
}

func TestReconcileResolvesWorkflowBasename(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "avatars_todo.md"), []byte("# Todo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\n"), 0644); err != nil {
		t.Fatal(err)
	}
	synth := "See `avatars_todo.md` for the checklist. Sources: `lib.go`."
	if err := reconcileFinalDeliveryAnswer(dir, "completed", synth, true, true, "",
		[]string{"lib.go"}); err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(answer)
	if strings.Contains(text, "## Path Corrections") {
		t.Fatalf("workflow basename on disk under docs/workflow must not be missing:\n%s", text)
	}
	if strings.Contains(text, "avatars_todo.md") && strings.Contains(text, "NOT ON DISK") {
		t.Fatalf("avatars_todo.md must not be marked missing:\n%s", text)
	}
	missing := claimedPathsMissingOnDisk(dir, synth)
	for _, p := range missing {
		if p == "avatars_todo.md" {
			t.Fatalf("avatars_todo.md should resolve to docs/workflow, missing=%v", missing)
		}
	}
	if !claimedPathExistsOnDisk(dir, "avatars_todo.md") {
		t.Fatal("expected docs/workflow/avatars_todo.md to satisfy the basename")
	}
}
