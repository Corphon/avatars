package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessContextLeakReason_GoImport(t *testing.T) {
	content := `package main
import (
	"fmt"
	_ "example.com/hookbox/internal/llm/providers/a"
)
func main() { fmt.Println("x") }
`
	reason := harnessContextLeakReason("cmd/server/main.go", content)
	if reason == "" {
		t.Fatal("G1: llm/providers import must be refused")
	}
}

func TestHarnessContextLeakReason_JSRequire(t *testing.T) {
	content := `const x = require('./skills/approved/foo');
module.exports = x;
`
	reason := harnessContextLeakReason("src/index.js", content)
	if reason == "" {
		t.Fatal("G1: skills/approved require must be refused")
	}
}

func TestHarnessContextLeakReason_CleanGo(t *testing.T) {
	content := `package main
import (
	"fmt"
	"example.com/hookbox/internal/handler"
)
func main() { fmt.Println("ok") }
`
	if reason := harnessContextLeakReason("cmd/server/main.go", content); reason != "" {
		t.Fatalf("clean import must pass, got %q", reason)
	}
}

func TestMissingLocalGoImportReason(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/hookbox\n\ngo 1.22\n"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "internal", "handler"), 0755)
	content := `package main
import (
	"example.com/hookbox/internal/handler"
	_ "example.com/hookbox/internal/llm/providers/a"
)
func main() {}
`
	// Leak is caught by harnessContextLeakReason; missingLocal still flags the ghost package.
	reason := missingLocalGoImportReason(dir, "main.go", content)
	if reason == "" || !strings.Contains(reason, "llm/providers") {
		t.Fatalf("expected missing local import reason, got %q", reason)
	}
	// Existing package should not fail alone.
	okContent := `package main
import "example.com/hookbox/internal/handler"
func main() {}
`
	if reason := missingLocalGoImportReason(dir, "main.go", okContent); reason != "" {
		t.Fatalf("existing package must pass, got %q", reason)
	}
}

func TestCheckFileHealth_RejectsHarnessLeak(t *testing.T) {
	content := `package main
import _ "example.com/x/internal/llm/providers/a"
func main() {}
`
	if reason := checkFileHealth("cmd/server/main.go", content); reason == "" {
		t.Fatal("checkFileHealth must reject harness leak")
	}
}
