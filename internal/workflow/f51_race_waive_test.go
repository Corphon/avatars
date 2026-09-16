package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvidenceBasedVerificationMark_WaivesGoRaceWithoutCGO(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	todo := "## Phase 1 Checklist\n- [ ] Run `go test ./...`\n- [ ] Run `go test -race ./...` — no race conditions\n"
	out := evidenceBasedVerificationMark(todo, dir)
	if !strings.Contains(out, "- [x]") {
		t.Fatalf("expected some items marked, got:\n%s", out)
	}
	if !goRaceUnavailable(dir) {
		t.Skip("this environment can run -race; waive assertion only when cgo race is unavailable")
	}
	if !strings.Contains(out, "go test -race") || !strings.Contains(out, "waived") {
		t.Fatalf("F51: race item should be waived when cgo unavailable:\n%s", out)
	}
}
