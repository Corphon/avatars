// Verified: complies with skills.md test_file_requirements — no network or external process startup.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/prompt"
)

// =============================================================================
// P1 VERIFICATION: Atomic Write prevents file truncation
// =============================================================================

func TestP1_AtomicWrite_TempFileCreatedAndRenamed(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "main.go")
	content := []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n")

	if err := writeFileAtomic(targetPath, content); err != nil {
		t.Fatalf("P1 FAIL: atomic write failed: %v", err)
	}

	// Verify target exists with correct content.
	written, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("P1 FAIL: target file not created: %v", err)
	}
	if string(written) != string(content) {
		t.Fatalf("P1 FAIL: content mismatch\n  want: %q\n  got:  %q", string(content), string(written))
	}

	// Verify no temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".avatars-tmp-") {
			t.Fatalf("P1 FAIL: temp file not cleaned up: %s", e.Name())
		}
	}
}

func TestP1_AtomicWrite_TruncatedGoFileRejected(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "broken.go")
	// Truncated: missing closing brace for main function.
	truncated := []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n")

	err := writeFileAtomic(targetPath, truncated)
	if err == nil {
		t.Fatal("P1 FAIL: truncated Go file should be rejected by health check")
	}
	if !strings.Contains(err.Error(), "health check") && !strings.Contains(err.Error(), "syntax") {
		t.Fatalf("P1 FAIL: expected health check or syntax error, got: %v", err)
	}

	// Verify target file was NOT created (atomic write fails, temp cleaned up).
	if _, statErr := os.Stat(targetPath); statErr == nil {
		t.Fatal("P1 FAIL: truncated file should not exist on disk")
	}
}

func TestP1_AtomicWrite_TruncatedPythonFileRejected(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "broken.py")
	// Truncated: if statement at end of file with no body.
	truncated := []byte("def calc(x):\n    return x * x\n\nif x > 0:\n")

	err := writeFileAtomic(targetPath, truncated)
	if err == nil {
		t.Fatal("P1 FAIL: truncated Python file should be rejected by health check")
	}
	if !strings.Contains(err.Error(), "health check") {
		t.Fatalf("P1 FAIL: expected health check error, got: %v", err)
	}

	// Verify target file was NOT created.
	if _, statErr := os.Stat(targetPath); statErr == nil {
		t.Fatal("P1 FAIL: truncated file should not exist on disk")
	}
}

func TestP1_AtomicWrite_TruncatedJSFileRejected(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "broken.js")
	// Truncated: unbalanced braces (missing closing brace).
	truncated := []byte("function main() {\n  if (true) {\n    return 1;\n  \n")

	err := writeFileAtomic(targetPath, truncated)
	if err == nil {
		t.Fatal("P1 FAIL: truncated JS file should be rejected by health check")
	}
	if !strings.Contains(err.Error(), "health check") {
		t.Fatalf("P1 FAIL: expected health check error, got: %v", err)
	}

	// Verify target file was NOT created.
	if _, statErr := os.Stat(targetPath); statErr == nil {
		t.Fatal("P1 FAIL: truncated file should not exist on disk")
	}
}

func TestP1_AtomicWrite_OverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "existing.go")
	original := []byte("package main\n\n// old\nfunc main() {}\n")

	if err := os.WriteFile(targetPath, original, 0644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	// Overwrite with atomic write.
	updated := []byte("package main\n\n// new\nfunc main() { println(\"hi\") }\n")
	if err := writeFileAtomic(targetPath, updated); err != nil {
		t.Fatalf("P1 FAIL: atomic overwrite failed: %v", err)
	}

	written, _ := os.ReadFile(targetPath)
	if string(written) != string(updated) {
		t.Fatalf("P1 FAIL: overwrite content mismatch\n  want: %q\n  got:  %q", string(updated), string(written))
	}
}

func TestP1_AtomicWrite_OriginalPreservedOnFailure(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "preserved.go")
	original := []byte("package main\n\nfunc main() {}\n")

	if err := os.WriteFile(targetPath, original, 0644); err != nil {
		t.Fatalf("seed file failed: %v", err)
	}

	// Try to overwrite with truncated content — missing closing brace.
	truncated := []byte("package main\n\nfunc broken() {\n\treturn\n")
	err := writeFileAtomic(targetPath, truncated)
	if err == nil {
		t.Fatal("P1 FAIL: should have rejected truncated overwrite")
	}

	// Original file must be intact.
	written, _ := os.ReadFile(targetPath)
	if string(written) != string(original) {
		t.Fatalf("P1 FAIL: original file was modified on failed overwrite\n  want: %q\n  got:  %q", string(original), string(written))
	}
}

// =============================================================================
// P3 VERIFICATION: Node.js syntax check
// =============================================================================

func TestP3_NodeJS_TruncatedFunctionDetected(t *testing.T) {
	// Function keyword at end with no body.
	reason := checkNodeJSSyntax("test.js", "function foo(\n")
	if reason == "" {
		t.Fatal("P3 FAIL: truncated JS function should be detected")
	}
	if !strings.Contains(reason, "truncated") && !strings.Contains(reason, "too small") {
		t.Fatalf("P3 FAIL: expected truncation detection, got: %s", reason)
	}
}

func TestP3_NodeJS_TruncatedForLoopDetected(t *testing.T) {
	reason := checkNodeJSSyntax("loop.js", "for (let i = 0; i < 10; i++) {\n  console.log(i)\n")
	if reason == "" {
		t.Fatal("P3 FAIL: truncated for loop (unclosed brace) should be detected")
	}
}

func TestP3_NodeJS_TruncatedIfDetected(t *testing.T) {
	reason := checkNodeJSSyntax("cond.js", "function main() {\n  if (x > 0)\n")
	if reason == "" {
		t.Fatal("P3 FAIL: truncated if statement should be detected")
	}
}

func TestP3_NodeJS_ValidJSPasses(t *testing.T) {
	dir := t.TempDir()
	jsPath := filepath.Join(dir, "valid.js")
	valid := "function main() {\n  const x = 1;\n  console.log(x);\n}\n\nmain();\n"
	os.WriteFile(jsPath, []byte(valid), 0644)
	reason := checkNodeJSSyntax(jsPath, valid)
	if reason != "" {
		t.Fatalf("P3 FAIL: valid JS should pass, got: %s", reason)
	}
}

func TestP3_NodeJS_UnbalancedBracesDetected(t *testing.T) {
	reason := checkNodeJSSyntax("unbalanced.js", "function main() {\n  if (true) {\n    return 1;\n}\n")
	if reason == "" {
		t.Fatal("P3 FAIL: unbalanced braces should be detected")
	}
	if !strings.Contains(reason, "unbalanced") {
		t.Fatalf("P3 FAIL: expected unbalanced brace error, got: %s", reason)
	}
}

// =============================================================================
// P2 VERIFICATION: Cross-language health check + checkFileHealth
// =============================================================================

func TestP2_CheckFileHealth_RejectsTruncatedGo(t *testing.T) {
	reason := checkFileHealth("main.go", "package main\n\nfunc broken(\n")
	if reason == "" {
		t.Fatal("P2 FAIL: truncated Go should fail checkFileHealth")
	}
}

func TestP2_CheckFileHealth_RejectsTruncatedPython(t *testing.T) {
	// if at end of file with colon but no body.
	reason := checkFileHealth("calc.py", "def add(a, b):\n    return a + b\n\nif True:\n")
	if reason == "" {
		t.Fatal("P2 FAIL: truncated Python should fail checkFileHealth")
	}
}

func TestP2_CheckFileHealth_RejectsTruncatedJS(t *testing.T) {
	reason := checkFileHealth("app.js", "function init() {\n  console.log('hi\n")
	if reason == "" {
		t.Fatal("P2 FAIL: truncated JS should fail checkFileHealth")
	}
}

func TestP2_CheckFileHealth_RejectsTooSmallGoFile(t *testing.T) {
	reason := checkFileHealth("tiny.go", "package main\n")
	if reason == "" {
		t.Fatal("P2 FAIL: too-small Go file should fail checkFileHealth")
	}
	if !strings.Contains(reason, "too small") {
		t.Fatalf("P2 FAIL: expected 'too small' error, got: %s", reason)
	}
}

func TestP2_CheckFileHealth_AcceptsValidGoFile(t *testing.T) {
	valid := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	reason := checkFileHealth("valid.go", valid)
	if reason != "" {
		t.Fatalf("P2 FAIL: valid Go should pass, got: %s", reason)
	}
}

func TestP2_CheckFileHealth_AcceptsValidPythonFile(t *testing.T) {
	valid := "def add(a, b):\n    return a + b\n\nif __name__ == \"__main__\":\n    print(add(1, 2))\n"
	reason := checkFileHealth("valid.py", valid)
	if reason != "" {
		t.Fatalf("P2 FAIL: valid Python should pass, got: %s", reason)
	}
}

func TestP2_CheckFileHealth_AcceptsValidJSFile(t *testing.T) {
	dir := t.TempDir()
	jsPath := filepath.Join(dir, "valid.js")
	valid := "function add(a, b) {\n  return a + b;\n}\n\nconsole.log(add(1, 2));\n"
	os.WriteFile(jsPath, []byte(valid), 0644)
	reason := checkFileHealth(jsPath, valid)
	if reason != "" {
		t.Fatalf("P2 FAIL: valid JS should pass, got: %s", reason)
	}
}

// =============================================================================
// P5 VERIFICATION: Staged quality gate
// =============================================================================

func TestP5_StagedQualityGate_Stage1CatchesSyntaxError(t *testing.T) {
	dir := t.TempDir()
	// Write a Go file with syntax error.
	goFile := filepath.Join(dir, "broken.go")
	os.WriteFile(goFile, []byte("package main\n\nfunc broken(\n"), 0644)
	// Also need a go.mod so stage 2 doesn't mask stage 1's result.
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.22\n"), 0644)

	ch := &CriticHub{}
	work := workflowNodeWorkContext{runID: "test-run", taskID: "test-task"}

	result := ch.stagedQualityGate(dir, work)
	if result.Decision == "" {
		t.Fatal("P5 FAIL: Stage 1 should catch syntax error and return a decision")
	}
	if !strings.Contains(result.Decision, "Stage 1") && !strings.Contains(result.Decision, "syntax") {
		t.Fatalf("P5 FAIL: expected Stage 1 syntax failure, got: %s", result.Decision)
	}
}

func TestP5_StagedQualityGate_EmptyDirPassesStage1(t *testing.T) {
	dir := t.TempDir()
	ch := &CriticHub{}
	work := workflowNodeWorkContext{runID: "test-run", taskID: "test-task"}

	result := ch.stagedQualityGate(dir, work)
	if result.Decision != "" {
		t.Fatalf("P5 FAIL: empty dir should pass all fast checks, got: %s", result.Decision)
	}
}

func TestP5_StagedQualityGate_ValidGoPassesFastChecks(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.22\n"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	ch := &CriticHub{}
	work := workflowNodeWorkContext{runID: "test-run", taskID: "test-task"}

	result := ch.stagedQualityGate(dir, work)
	if result.Decision != "" {
		t.Fatalf("P5 FAIL: valid Go should pass fast checks, got: %s", result.Decision)
	}
}

// =============================================================================
// P6 VERIFICATION: Builder single approach constraint
// =============================================================================

func TestP6_BuilderStablePrefix_HasSingleApproachRule(t *testing.T) {
	prefix := prompt.BuilderStablePrefix
	if !strings.Contains(prefix, "exactly ONE implementation approach") {
		t.Fatal("P6 FAIL: Builder StablePrefix missing single-approach rule (#12)")
	}
	if strings.Contains(prefix, "Do NOT generate multiple") {
		// prohibition present
	} else if !strings.Contains(prefix, "Do NOT generate") && !strings.Contains(prefix, "do not generate") {
		t.Fatal("P6 FAIL: Builder StablePrefix should explicitly prohibit multiple implementations")
	}
	if !strings.Contains(prefix, "never wall") && !strings.Contains(prefix, "Wall-clock waits") {
		t.Fatal("Builder prefix must end with injectable-clock vs wall-wait rule")
	}
}

// =============================================================================
// P4 VERIFICATION: Planner layered decomposition
// =============================================================================

func TestP4_PlannerStablePrefix_HasLayeredDecompositionRules(t *testing.T) {
	prefix := prompt.PlannerStablePrefix
	if !strings.Contains(prefix, "Node 1") || !strings.Contains(prefix, "Node 2") {
		t.Fatal("P4 FAIL: Planner StablePrefix missing layered decomposition rules (#11-12)")
	}
	if !strings.Contains(prefix, "CLI") && !strings.Contains(prefix, "entry-point") {
		t.Fatal("P4 FAIL: Planner should reference CLI/entry-point wiring")
	}
}

// =============================================================================
// INTEGRATION: Full write pipeline health chain
// =============================================================================

func TestIntegration_WriteFileAtomic_ThenCheckFileHealth_Pipeline(t *testing.T) {
	// Simulates the full pipeline: writeFileAtomic → health check → verify on disk.
	dir := t.TempDir()

	validGo := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"ok\")\n}\n"
	targetPath := filepath.Join(dir, "main.go")

	// Step 1: Atomic write with internal health check.
	if err := writeFileAtomic(targetPath, []byte(validGo)); err != nil {
		t.Fatalf("INTEGRATION FAIL: writeFileAtomic rejected valid Go: %v", err)
	}

	// Step 2: Post-write health check on the actual file.
	content, _ := os.ReadFile(targetPath)
	if reason := checkFileHealth(targetPath, string(content)); reason != "" {
		t.Fatalf("INTEGRATION FAIL: checkFileHealth rejected valid Go after write: %s", reason)
	}

	// Verify the file is on disk and correct.
	if _, statErr := os.Stat(targetPath); statErr != nil {
		t.Fatal("INTEGRATION FAIL: file not on disk after successful atomic write")
	}
}

func TestIntegration_TruncatedContentBlockedAtAllLayers(t *testing.T) {
	dir := t.TempDir()

	// Different truncated patterns across 3 languages.
	tests := []struct {
		lang, path, content string
	}{
		{"Go", "broken.go", "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n"},
		{"Python", "broken.py", "def calc():\n    return x * x\n\nif x > 0:"},
		{"JS", "broken.js", "function main() {\n  if (true) {\n    return 1;\n  \n"},
	}

	for _, tc := range tests {
		targetPath := filepath.Join(dir, tc.path)
		t.Run(tc.lang, func(t *testing.T) {
			// Layer 1: atomic write rejects.
			err := writeFileAtomic(targetPath, []byte(tc.content))
			if err == nil {
				t.Errorf("Layer 1 FAIL: writeFileAtomic should reject truncated %s", tc.lang)
			}

			// Layer 2: checkFileHealth rejects.
			if reason := checkFileHealth(tc.path, tc.content); reason == "" {
				t.Errorf("Layer 2 FAIL: checkFileHealth should reject truncated %s", tc.lang)
			}

			// Layer 3: File not on disk.
			if _, statErr := os.Stat(targetPath); statErr == nil {
				t.Errorf("Layer 3 FAIL: truncated %s file should not exist", tc.lang)
			}
		})
	}
}
