package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdversarialAudit_DetectsDuplicateTypes verifies the adversarial audit
// catches types declared in multiple generated files (Phase 6).
func TestAdversarialAudit_DetectsDuplicateTypes(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644)

	// Create two generated files that both declare the SAME type.
	os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package pkg\ntype Note struct { ID int }\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package pkg\ntype Note struct { Title string }\n"), 0644)

	findings := AdversarialAudit(tmpDir, []string{"a.go", "b.go"})

	// Should find the duplicate type.
	hasDup := false
	for _, f := range findings {
		t.Logf("Finding: kind=%s file=%s detail=%s", f.Kind, f.File, f.Detail)
		if f.Kind == "duplicate_type" && strings.Contains(f.Detail, "Note") {
			hasDup = true
		}
	}
	if !hasDup {
		t.Error("expected duplicate_type finding for Note declared in a.go and b.go")
	}
}

// TestAdversarialAudit_NoFalsePositives verifies clean code produces no findings.
func TestAdversarialAudit_NoFalsePositives(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644)

	// Well-formed file with unique types.
	os.WriteFile(filepath.Join(tmpDir, "main.go"),
		[]byte("package main\nimport \"fmt\"\ntype App struct { Name string }\nfunc main() { fmt.Println(\"ok\") }\n"), 0644)

	findings := AdversarialAudit(tmpDir, []string{"main.go"})

	if len(findings) != 0 {
		for _, f := range findings {
			t.Errorf("unexpected finding on clean code: kind=%s detail=%s", f.Kind, f.Detail)
		}
	}
}

// TestAdversarialAudit_DetectsExternalDeps verifies import checking.
func TestAdversarialAudit_DetectsExternalDeps(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644)

	// File importing external package not in go.mod.
	os.WriteFile(filepath.Join(tmpDir, "main.go"),
		[]byte("package main\nimport \"github.com/spf13/cobra\"\nfunc main() {}\n"), 0644)

	findings := AdversarialAudit(tmpDir, []string{"main.go"})

	hasExtDep := false
	for _, f := range findings {
		t.Logf("Finding: kind=%s file=%s detail=%s", f.Kind, f.File, f.Detail)
		if f.Kind == "external_dependency" && strings.Contains(f.Detail, "cobra") {
			hasExtDep = true
		}
	}
	if !hasExtDep {
		t.Error("expected external_dependency finding for cobra import")
	}
}
