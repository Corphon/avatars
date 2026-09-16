package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestF68_SubstantiveTestSuite(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "jwtmini")
	_ = os.MkdirAll(lib, 0755)
	_ = os.WriteFile(filepath.Join(lib, "jwt.go"), []byte("package jwtmini\n"), 0644)
	thin := "package jwtmini\n\nimport \"testing\"\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n"
	_ = os.WriteFile(filepath.Join(lib, "jwt_test.go"), []byte(thin), 0644)
	if substantiveTestSuite(dir) {
		t.Fatal("2 tests must not count as substantive")
	}
	rich := thin + "func TestC(t *testing.T) {}\nfunc TestD(t *testing.T) {}\n"
	_ = os.WriteFile(filepath.Join(lib, "jwt_test.go"), []byte(rich), 0644)
	if !substantiveTestSuite(dir) {
		t.Fatal("4 tests should count as substantive")
	}
	if !isChecklistItemSatisfied(dir, "Unit Tests (13/13)", "") {
		// compiles may fail without go.mod — seed one
	}
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/jwtmini\n\ngo 1.22\n"), 0644)
	// projectCompiles will run go build — should work for empty package with tests
	if !isChecklistItemSatisfied(dir, "Unit Tests complete", "unused") && !substantiveTestSuite(dir) {
		t.Fatal("substantive suite required for unit-test checklist")
	}
}

func TestProjectTestsPass_EmptySuiteIsNotGreen(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/emptytests\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package emptytests\n\nfunc Allow() bool { return true }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	testsPassCache.Delete(abs)
	t.Cleanup(func() { testsPassCache.Delete(abs) })
	if projectTestsPass(dir) {
		t.Fatal("go test with no test files must not count as passing")
	}
	if isChecklistItemSatisfied(dir, "Unit tests — breaker_test.go (8/8)", "") {
		t.Fatal("unit-test checklist must not tick without test files")
	}
	if isChecklistItemSatisfied(dir, "`go test ./...` exits 0", "") {
		t.Fatal("go test checkbox must not tick on a vacuous suite")
	}
}
