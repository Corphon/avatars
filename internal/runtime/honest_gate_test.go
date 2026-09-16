package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"avatars/internal/verification"
)

func TestApplyHonestVerificationGate_DowngradesPass(t *testing.T) {
	tmp := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	if err := os.WriteFile("go.mod", []byte("module example.com/broken\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("broken.go", []byte("package main\n\nfunc init() { notDefined() }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := &Engine{changedFiles: []string{"broken.go"}}
	report := verification.Report{
		Verdict: verification.VerdictPass,
		Summary: "Verification finished with PASS. 5 passed, 0 partial, 0 failed.",
	}
	out := e.applyHonestVerificationGate(tmp, report)
	if out.Verdict != verification.VerdictFail {
		t.Fatalf("verdict=%s want FAIL summary=%s cwd=%s", out.Verdict, out.Summary, tmp)
	}
}

func TestApplyHonestVerificationGate_NoSourceWritesKeepsPass(t *testing.T) {
	e := &Engine{changedFiles: nil}
	report := verification.Report{Verdict: verification.VerdictPass, Summary: "PASS"}
	out := e.applyHonestVerificationGate(".", report)
	if out.Verdict != verification.VerdictPass {
		t.Fatalf("got %s", out.Verdict)
	}
	_ = filepath.Separator
}
