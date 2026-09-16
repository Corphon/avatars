package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBuilderGoImports_KeepsQuotedErrorWithModulePrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module lrux\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `package lrux

import (
	"errors"
	"sync"
)

func New(capacity int) error {
	if capacity <= 0 {
		return errors.New("lrux: capacity must be greater than zero")
	}
	return nil
}
`
	out, warnings := validateBuilderGoImports(dir, src)
	if !strings.Contains(out, `errors.New("lrux: capacity must be greater than zero")`) {
		t.Fatalf("error string was treated as an import:\n%s", out)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected import warnings: %v", warnings)
	}
}

func TestValidateBuilderGoImports_RemovesMissingModuleImport(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module lrux\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `package lrux

import (
	"errors"
	"lrux/internal/missing"
)

func New() error { return errors.New("ok") }
`
	out, warnings := validateBuilderGoImports(dir, src)
	if strings.Contains(out, "lrux/internal/missing") {
		t.Fatalf("missing module import should be removed:\n%s", out)
	}
	if !strings.Contains(out, `"errors"`) {
		t.Fatalf("std import should stay:\n%s", out)
	}
	if len(warnings) == 0 {
		t.Fatal("expected a warning for the removed import")
	}
}

func TestNeedsCodeImplementation_ChecklistAlign(t *testing.T) {
	input := "请接着把清单和真实进度对齐。用人话讲为什么这么划阶段。现在第几阶段。\n\n=== CHECKLIST SYNC ===\nUpdate workflow checklists to match evidence already on disk.\nDo not regenerate implementation sources.\n"
	if needsCodeImplementation(input) {
		t.Fatal("checklist-align must not start another codegen pass")
	}
	if !needsCodeImplementation("Implement an in-process LRU cache in lrux.go with tests") {
		t.Fatal("real implement work must still codegen")
	}
}

func TestAlignGoTestWaitKeys_RewritesMismatchedDoKey(t *testing.T) {
	src := `package flightx

import "testing"

func TestStressSameKey(t *testing.T) {
	var g Group
	go func() { g.Do("shared", fn) }()
	waitForDups(t, &g, "key", 999)
}
`
	out := alignGoTestWaitKeys(src)
	if strings.Contains(out, `"key"`) {
		t.Fatalf("wait helper still uses the wrong key:\n%s", out)
	}
	if !strings.Contains(out, `waitForDups(t, &g, "shared", 999)`) {
		t.Fatalf("expected waitForDups to use Do key shared:\n%s", out)
	}
}
