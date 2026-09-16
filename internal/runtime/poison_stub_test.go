package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikePoisonStubContent_HashCommentTS(t *testing.T) {
	// mid_ts_nl_p2_r1: "# test.ts — stub" → tsc TS1127
	poison := "# test.ts — stub\n"
	if reason := looksLikePoisonStubContent("src/test/test.ts", poison); reason == "" {
		t.Fatal("expected poison reject for # stub in .ts")
	}
	if reason := looksLikePoisonStubContent("src/notes.ts", "// Placeholder — replace with real implementation.\nexport {};\n"); reason != "" {
		t.Fatalf("valid TS placeholder should pass, got %q", reason)
	}
}

func TestLooksLikePoisonStubContent_CommentOnlyStub(t *testing.T) {
	for _, tc := range []struct {
		path, content string
	}{
		{"foo.py", "# foo.py — stub\n"},
		{"lib.rs", "// lib.rs — stub\n"},
		{"main.go", "// main.go — stub: LLM did not generate\n"},
	} {
		if reason := looksLikePoisonStubContent(tc.path, tc.content); reason == "" {
			t.Fatalf("expected poison for %s", tc.path)
		}
	}
}

func TestIsJunkScaffoldPath(t *testing.T) {
	junk := []string{
		"d.ts", "test.ts", "tsconfig.js", "src/tsconfig/tsconfig.js",
		"src/types/sql.js", "src/test/test.ts", "sql.js",
		"default.py", "default/default.py",
		"internal/zzdiag/zzdiag_test.go", "zzdiag.go", "tests/zzdiag/test_probe.py",
		"tmp/diag/probe.js", "scratch/diag/probe.ts", "__diag__/probe.rs",
	}
	for _, p := range junk {
		if !isJunkScaffoldPath(p) {
			t.Fatalf("expected junk: %s", p)
		}
	}
	ok := []string{"src/notes.ts", "test/notes.test.ts", "src/index.js", "app/main.go",
		"internal/diagnostics/report.go", "diagnostics.py", "src/diag.rs"}
	for _, p := range ok {
		if isJunkScaffoldPath(p) {
			t.Fatalf("should allow: %s", p)
		}
	}
}

func TestCreateMinimalStub_TSUsesSlashComment(t *testing.T) {
	stub := createMinimalStub("src/notes.ts", "typescript")
	if strings.HasPrefix(strings.TrimSpace(stub), "#") {
		t.Fatalf("TS stub must not start with #: %q", stub)
	}
	if !strings.Contains(stub, "export {}") {
		t.Fatalf("TS stub should export empty module: %q", stub)
	}
	if reason := checkFileHealth("src/notes.ts", stub); reason != "" {
		t.Fatalf("createMinimalStub TS should pass health: %s", reason)
	}
}

func TestCheckFileHealth_RejectsJunkAndPoison(t *testing.T) {
	if reason := checkFileHealth("src/types/sql.js", "module.exports = {};\n"); reason == "" {
		t.Fatal("expected junk path reject")
	}
	if reason := checkFileHealth("src/test/test.ts", "# test.ts — stub\n"); reason == "" {
		t.Fatal("expected poison stub reject")
	}
	if reason := checkFileHealth("internal/zzdiag/zzdiag_test.go", "package hashringx\nfunc TestX(t *testing.T) {}\n"); reason == "" {
		t.Fatal("expected junk diagnostic path reject")
	}
}

func TestPurgeUnhealthySourcesOnDisk_RemovesPoisonTS(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src", "test"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0755)
	good := "export function ok(): number { return 1; }\n"
	poison := "# test.ts — stub\n"
	_ = os.WriteFile(filepath.Join(dir, "src", "notes.ts"), []byte(good), 0644)
	badPath := filepath.Join(dir, "src", "test", "test.ts")
	_ = os.WriteFile(badPath, []byte(poison), 0644)
	junkPath := filepath.Join(dir, "src", "types")
	_ = os.MkdirAll(junkPath, 0755)
	sqlPath := filepath.Join(junkPath, "sql.js")
	_ = os.WriteFile(sqlPath, []byte("// sql.js — stub\n"), 0644)

	removed := purgeUnhealthySourcesOnDisk(dir)
	if len(removed) == 0 {
		t.Fatal("expected poison/junk purge")
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatalf("poison test.ts should be deleted")
	}
	if _, err := os.Stat(sqlPath); !os.IsNotExist(err) {
		t.Fatalf("junk sql.js should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "notes.ts")); err != nil {
		t.Fatalf("healthy notes.ts should remain: %v", err)
	}
}

func TestHealthFailureFingerprint_PoisonStub(t *testing.T) {
	a := healthFailureFingerprint(`tsc failed: src/test/test.ts(1,1): error TS1127: Invalid character.`)
	b := healthFailureFingerprint(`File src/foo.ts: comment-only stub content (poison)`)
	if a != "poison_stub_compile" || b != "poison_stub_compile" {
		t.Fatalf("got %q %q", a, b)
	}
}

func TestExtractTaskFilePaths_SkipsJunk(t *testing.T) {
	paths := extractTaskFilePaths("Create src/notes.ts and also write test.ts and src/types/sql.js and d.ts")
	joined := strings.Join(paths, ",")
	for _, bad := range []string{"test.ts", "sql.js", "d.ts"} {
		for _, p := range paths {
			if filepath.Base(p) == bad || p == bad {
				t.Fatalf("junk path %q should be skipped, got %v", bad, paths)
			}
		}
	}
	if !strings.Contains(joined, "notes.ts") {
		t.Fatalf("expected notes.ts kept, got %v", paths)
	}
}
