package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsHostToolchainFailure_RustLinker(t *testing.T) {
	cases := []string{
		`cargo check failed: error occurred in cc-rs: failed to find tool "gcc.exe": program not found`,
		`error: error calling dlltool 'dlltool.exe': program not found`,
		`linker 'link.exe' not found`,
		`host toolchain unavailable: cargo not found on PATH`,
		`note: the msvc targets depend on the msvc linker`,
		`npm install failed: gyp ERR! find VS could not find any Visual Studio installation`,
		`node-gyp rebuild failed: Could not find any Visual Studio`,
		`npm ERR! gyp ERR! stack Error: Could not find any Visual Studio`,
	}
	for _, c := range cases {
		if !isHostToolchainFailure(c) {
			t.Fatalf("expected host toolchain failure for %q", c)
		}
	}
	if isHostToolchainFailure("cargo check failed: error[E0425]: cannot find value `foo` in this scope") {
		t.Fatal("application compile error must NOT be classified as host toolchain")
	}
	if isHostToolchainFailure("npm test failed: AssertionError: expected 201 to equal 400") {
		t.Fatal("app test failure must NOT be classified as host toolchain")
	}
}

func TestAnnotateHostToolchainFailure(t *testing.T) {
	msg := annotateHostToolchainFailure(`failed to find tool "gcc.exe": program not found`)
	if !strings.HasPrefix(msg, hostToolchainUnavailablePrefix) {
		t.Fatalf("expected prefix, got %q", msg)
	}
	if !isHostToolchainFailure(msg) {
		t.Fatal("annotated message should still classify as host toolchain")
	}
}

func TestHealthFailureFingerprint_SameCause(t *testing.T) {
	a := healthFailureFingerprint(`failed to find tool "gcc.exe"`)
	b := healthFailureFingerprint(`error occurred in cc-rs: failed to find tool "gcc.exe": program not found (see https://docs.rs/cc)`)
	if a != "host_toolchain" || b != "host_toolchain" {
		t.Fatalf("env failures should share fingerprint, got %q %q", a, b)
	}
	e := &Engine{}
	if n := e.noteQualityFailure(`failed to find tool "gcc.exe"`); n != 1 {
		t.Fatalf("first note got %d", n)
	}
	if n := e.noteQualityFailure(`dlltool.exe: program not found`); n != 2 {
		t.Fatalf("second env note got %d", n)
	}
	if !e.sameQualityFailureRepeated(`error calling dlltool 'dlltool.exe'`) {
		t.Fatal("expected same-cause after 2 notes")
	}
}

func TestPurgeUnhealthySourcesOnDisk_RemovesTruncatedRust(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "tests"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0755)
	good := "fn main() {\n    println!(\"hi\");\n}\n"
	bad := "#[tokio::test]\nasync fn test_list() {\n    let notes: Vec\n"
	_ = os.WriteFile(filepath.Join(dir, "src", "main.rs"), []byte(good), 0644)
	badPath := filepath.Join(dir, "tests", "integration_test.rs")
	_ = os.WriteFile(badPath, []byte(bad), 0644)

	removed := purgeUnhealthySourcesOnDisk(dir)
	if len(removed) == 0 {
		t.Fatal("expected truncated test file to be purged")
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatalf("truncated file should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "main.rs")); err != nil {
		t.Fatalf("healthy main.rs should remain: %v", err)
	}
}

func TestPurgeUnhealthySourcesOnDisk_RemovesTruncatedJS(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "test"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0755)
	good := "export function ok() { return 1; }\n"
	bad := "test('x', () => {\n  assert.ok(Math.abs(now - createdTime\n"
	_ = os.WriteFile(filepath.Join(dir, "src", "index.js"), []byte(good), 0644)
	badPath := filepath.Join(dir, "test", "notes.test.js")
	_ = os.WriteFile(badPath, []byte(bad), 0644)

	removed := purgeUnhealthySourcesOnDisk(dir)
	if len(removed) == 0 {
		t.Fatal("expected truncated JS test to be purged")
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatalf("truncated JS should be deleted, stat err=%v", err)
	}
}

func TestCodeCompletenessIssues_IncompleteTypeAnnotation(t *testing.T) {
	content := "#[tokio::test]\nasync fn test_list() {\n    let notes: Vec\n"
	issues := codeCompletenessIssues(content)
	if len(issues) == 0 {
		t.Fatal("expected issues for incomplete `let notes: Vec`")
	}
}

func TestTryFixStagedQualityFailure_SkipsHostToolchain(t *testing.T) {
	e := &Engine{nodeRetryCount: map[string]int{}}
	ch := NewCriticHub(e)
	ok := ch.tryFixStagedQualityFailure(context.Background(), workflowNodeWorkContext{}, t.TempDir(), CriticHubResult{
		Decision:     "Stage 2 failed",
		ErrorMessage: `cargo check failed: failed to find tool "gcc.exe": program not found`,
	})
	if ok {
		t.Fatal("host toolchain failure must not report fixed")
	}
	if e.criticBuilderCycles != 0 {
		t.Fatalf("Builder cycle must not increment for env skip, got %d", e.criticBuilderCycles)
	}
}
