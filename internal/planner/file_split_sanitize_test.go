package planner

import (
	"os"
	"testing"
)

func TestSanitizeSplitTargets_DropsJunkAndRemapsDoc(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module example.com/geohashn\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("geohashn", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("geohashn/x.go", []byte("package geohashn\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := sanitizeSplitTargets([]string{
		"doc.go",
		"internal/debug/",
		"cmd/geohashn/",
		"tmp/dbg/main.go",
		"internal/geohashn/geohashn.go",
	})
	want := map[string]bool{
		"geohashn/doc.go":            true,
		"cmd/geohashn/":              true,
		"geohashn/geohashn.go":       true,
	}
	if len(got) != len(want) {
		t.Fatalf("got=%v want keys %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected %q in %v", g, got)
		}
	}
}

func TestInferBuildPhases_NoInventDefault(t *testing.T) {
	got := inferBuildPhases("build a pure geohash library with encode decode")
	if len(got) != 0 {
		t.Fatalf("expected no invented internal/models default, got %v", got)
	}
}
