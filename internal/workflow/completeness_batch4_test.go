package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckImplementationCompleteness_SkipsStdlibMethods(t *testing.T) {
	dir := t.TempDir()
	src := `package main

import (
	"os"
	"path/filepath"
	"testing"
)

func helper() error {
	f, err := os.Open("x")
	if err != nil {
		return err
	}
	defer f.Close()
	_ = filepath.Join("a", "b")
	return nil
}

func TestX(t *testing.T) {
	t.Fatal("boom")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	missing := CheckImplementationCompleteness(dir)
	for _, m := range missing {
		lower := strings.ToLower(m)
		if strings.Contains(lower, "missing close") ||
			strings.Contains(lower, "missing join") ||
			strings.Contains(lower, "missing fatal") {
			t.Fatalf("stdlib false positive: %s (all=%v)", m, missing)
		}
	}
}

func TestCheckImplementationCompleteness_FlagsRealMissing(t *testing.T) {
	dir := t.TempDir()
	src := `package main

func run() {
	doSomethingSpecial()
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	missing := CheckImplementationCompleteness(dir)
	found := false
	for _, m := range missing {
		if strings.Contains(m, "doSomethingSpecial") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected missing doSomethingSpecial, got %v", missing)
	}
}
