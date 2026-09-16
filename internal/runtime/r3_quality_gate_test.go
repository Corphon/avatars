package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHollowCmdMainCheck_DetectsStub(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, "cmd", "server")
	if err := os.MkdirAll(cmdDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if reason := hollowCmdMainCheck(dir); reason == "" {
		t.Fatal("expected hollow cmd/server/main.go to be rejected")
	}
}

func TestHollowCmdMainCheck_OK(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, "cmd", "server")
	if err := os.MkdirAll(cmdDir, 0755); err != nil {
		t.Fatal(err)
	}
	src := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"ok\") }\n"
	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if reason := hollowCmdMainCheck(dir); reason != "" {
		t.Fatalf("valid main rejected: %s", reason)
	}
}

func TestCheckFileHealth_HollowMainEntrypoint(t *testing.T) {
	reason := checkFileHealth("cmd/server/main.go", "package main\n\nconst x = 1\n")
	if reason == "" {
		t.Fatal("expected hollow main entrypoint rejection")
	}
}

func TestCrossLangHealthCheck_GoTestFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/r3gate\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	failTest := "package main\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"boom\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(failTest), 0644); err != nil {
		t.Fatal(err)
	}
	errStr := crossLangHealthCheck(dir)
	if errStr == "" {
		t.Fatal("R3-14 FAIL: go test failure must fail crossLangHealthCheck")
	}
	lower := strings.ToLower(errStr)
	if !strings.Contains(lower, "go test") && !strings.Contains(lower, "fail") && !strings.Contains(lower, "boom") {
		t.Fatalf("expected go test failure signal, got %q", errStr)
	}

	if errStr := projectTestCheck(dir); errStr == "" {
		t.Fatal("projectTestCheck must fail on red go test")
	}
}

func TestProjectTestCheck_VacuousGoSuiteIsRed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/emptytests\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package emptytests\n\nfunc Allow() bool { return true }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	errStr := projectTestCheck(dir)
	if errStr == "" {
		t.Fatal("go test with no test files must not count as green")
	}
	if !strings.Contains(strings.ToLower(errStr), "no test files") && !strings.Contains(strings.ToLower(errStr), "go test") {
		t.Fatalf("expected vacuous-suite signal, got %q", errStr)
	}
}

func TestClarifyGoBuildNoNonTest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hashringx.go"), []byte("package hashringx\n"), 0644); err != nil {
		t.Fatal(err)
	}
	raw := "hashringx: no non-test Go files in " + dir
	got := clarifyGoBuildNoNonTest(dir, raw)
	if !strings.Contains(got, "module still has") || !strings.Contains(got, "hashringx.go") {
		t.Fatalf("F86: expected rewritten message, got %q", got)
	}
	if clarifyGoBuildNoNonTest(dir, "syntax error") != "syntax error" {
		t.Fatal("non-matching errors must pass through")
	}
}
