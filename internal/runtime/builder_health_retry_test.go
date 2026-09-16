package runtime

import (
	"strings"
	"testing"
)

func TestIsFixableCompileTestHealth(t *testing.T) {
	if !isFixableCompileTestHealth("go test failed: command timed out with no output (test suite likely hung — wall-clock wait or deadlock)") {
		t.Fatal("hung/timeout test runner should be retryable")
	}
	if !isFixableCompileTestHealth("go test failed: cannot use n (variable of type int64) as int") {
		t.Fatal("type mismatch from go test should be retryable")
	}
	if !isFixableCompileTestHealth("pytest failed: collected no tests") {
		t.Fatal("empty/failed pytest should be retryable")
	}
	if !isFixableCompileTestHealth("npm test\nSyntaxError") {
		t.Fatal("js runner syntax error should be retryable")
	}
	if isFixableCompileTestHealth("host toolchain not found on PATH") {
		t.Fatal("missing host toolchain must not trigger a codegen retry")
	}
	if !isFixableCompileTestHealth("go mod tidy: exit status 1: go.mod:4:4: unexpected newline in string") {
		t.Fatal("markdown-fence go.mod parse error should be retryable")
	}
	if isFixableCompileTestHealth("") {
		t.Fatal("empty health error is not retryable")
	}
}

func TestHealthRepairUserSuffixStaysOnUserTurn(t *testing.T) {
	s := healthRepairUserSuffix("go test failed: mismatched types")
	if !strings.Contains(s, "COMPILE/TEST GATE RED") {
		t.Fatalf("missing gate header:\n%s", s)
	}
	if !strings.Contains(s, "go test failed") {
		t.Fatalf("must echo the runner error:\n%s", s)
	}
	if strings.Contains(s, "F113") || strings.Contains(s, "F90") {
		t.Fatal("repair suffix must not carry task-id graffiti")
	}
	hung := healthRepairUserSuffix("go test failed: command timed out with no output (test suite likely hung — wall-clock wait or deadlock)")
	if !strings.Contains(hung, "block forever") {
		t.Fatalf("hung suite suffix must tell the model to unstick tests:\n%s", hung)
	}
}
