package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreciseEdit_InsertAfter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	original := "package p\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}
	result, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.go",
		Anchor:     "\t\"os\"",
		Content:    "\t\"strings\"",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "inserted") {
		t.Errorf("expected 'inserted' in result, got: %s", result.Content)
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "\"strings\"") {
		t.Errorf("expected 'strings' import in file, got:\n%s", string(got))
	}
	if !strings.Contains(string(got), "\"os\"") {
		t.Errorf("existing 'os' import was removed:\n%s", string(got))
	}
}

func TestPreciseEdit_InsertBefore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "line1\nline2\nline3\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "line2",
		Content:    "inserted_before",
		Position:   "before",
		IfMissing:  false,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(path)
	lines := strings.Split(string(got), "\n")
	if lines[0] != "line1" {
		t.Errorf("line1 moved: %s", lines[0])
	}
	if lines[1] != "inserted_before" {
		t.Errorf("expected inserted_before at line 2, got: %s", lines[1])
	}
	if lines[2] != "line2" {
		t.Errorf("line2 moved: %s", lines[2])
	}
}

func TestPreciseEdit_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "package p\n\nvar items = []string{\n\t\"a\",\n\t\"b\",\n}\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}

	// First call — should insert.
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "\t\"b\",",
		Content:    "\t\"c\",",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	// Second call — should skip.
	result, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "\t\"b\",",
		Content:    "\t\"c\",",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if !strings.Contains(result.Content, "already present") {
		t.Errorf("expected 'already present', got: %s", result.Content)
	}

	// Verify only one "c" entry.
	got, _ := os.ReadFile(path)
	count := strings.Count(string(got), "\"c\"")
	if count != 1 {
		t.Errorf("expected 1 occurrence of 'c', got %d:\n%s", count, string(got))
	}
}

func TestPreciseEdit_AnchorNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello\nworld\n"), 0o644)

	tool := PreciseEditTool{}
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "nonexistent",
		Content:    "something",
		Position:   "after",
		IfMissing:  false,
		WorkingDir: dir,
	})
	if err == nil {
		t.Fatal("expected error for missing anchor")
	}
	if !strings.Contains(err.Error(), "anchor not found") {
		t.Errorf("expected 'anchor not found', got: %v", err)
	}
}

func TestPreciseEdit_AnchorNotUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "item: a\nitem: b\nitem: c\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "item:",
		Content:    "item: d",
		Position:   "after",
		IfMissing:  false,
		WorkingDir: dir,
	})
	if err == nil {
		t.Fatal("expected error for non-unique anchor")
	}
	if !strings.Contains(err.Error(), "multiple times") {
		t.Errorf("expected 'multiple times', got: %v", err)
	}
}

func TestPreciseEdit_IfMissingFalseAlwaysInserts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "a\nb\nc\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}

	// First insert.
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "b",
		Content:    "x",
		Position:   "after",
		IfMissing:  false, // not idempotent!
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second insert with IfMissing=false — should insert again.
	_, err = tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "b",
		Content:    "x",
		Position:   "after",
		IfMissing:  false,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	got, _ := os.ReadFile(path)
	count := strings.Count(string(got), "x")
	if count != 2 {
		t.Errorf("expected 2 'x' lines, got %d:\n%s", count, string(got))
	}
}

func TestPreciseEdit_MultiLineContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "start\nmiddle\nend\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "middle",
		Content:    "line_a\nline_b\nline_c",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(path)
	lines := strings.Split(string(got), "\n")
	if lines[0] != "start" {
		t.Errorf("line 0: got %q", lines[0])
	}
	if lines[1] != "middle" {
		t.Errorf("line 1: got %q", lines[1])
	}
	if lines[2] != "line_a" {
		t.Errorf("line 2: got %q", lines[2])
	}
	if lines[3] != "line_b" {
		t.Errorf("line 3: got %q", lines[3])
	}
	if lines[4] != "line_c" {
		t.Errorf("line 4: got %q", lines[4])
	}
	if lines[5] != "end" {
		t.Errorf("line 5: got %q", lines[5])
	}
}

func TestPreciseEdit_SubstringMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	// Lines where the anchor "RegisterProvider" appears as part of a longer line.
	original := "svc.RegisterProvider(\"a\", providerA)\nsvc.RegisterProvider(\"b\", providerB)\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "RegisterProvider(\"b\"",
		Content:    "svc.RegisterProvider(\"c\", providerC)",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "providerC") {
		t.Errorf("providerC not found:\n%s", string(got))
	}
}

func TestPreciseEdit_InvalidPosition(t *testing.T) {
	tool := PreciseEditTool{}
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "x",
		Content:    "y",
		Position:   "below",
		IfMissing:  false,
		WorkingDir: "/tmp",
	})
	if err == nil {
		t.Fatal("expected error for invalid position")
	}
	if !strings.Contains(err.Error(), "must be 'before' or 'after'") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPreciseEdit_ConcurrencySafety(t *testing.T) {
	tool := PreciseEditTool{}
	if tool.IsConcurrencySafe(nil) {
		t.Error("precise_edit should not claim to be concurrency-safe")
	}
}

func TestPreciseEdit_Name(t *testing.T) {
	tool := PreciseEditTool{}
	if tool.Name() != "precise_edit" {
		t.Errorf("expected 'precise_edit', got %q", tool.Name())
	}
}

// PE-3: Safety verification tests.

func TestPreciseEdit_Safety_SizeIncrease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "line1\nline2\nline3\n"
	os.WriteFile(path, []byte(original), 0o644)
	originalSize := len(original)

	tool := PreciseEditTool{}
	result, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "line2",
		Content:    "inserted_line",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify size increased.
	got, _ := os.ReadFile(path)
	if len(got) <= originalSize {
		t.Errorf("file should have grown: was %d, now %d", originalSize, len(got))
	}

	// Verify result includes size info.
	if !strings.Contains(result.Content, "bytes") {
		t.Errorf("result should include size info: %s", result.Content)
	}

	// Verify all original lines are preserved.
	for _, expected := range []string{"line1", "line2", "line3", "inserted_line"} {
		if !strings.Contains(string(got), expected) {
			t.Errorf("missing expected content %q in:\n%s", expected, string(got))
		}
	}
}

func TestPreciseEdit_Safety_AllOriginalContentPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	original := "package p\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	os.WriteFile(path, []byte(original), 0o644)

	tool := PreciseEditTool{}
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.go",
		Anchor:     "\t\"os\"",
		Content:    "\t\"strings\"",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(path)
	// Verify every original line is still present.
	for _, expected := range []string{"package p", "import (", "\"fmt\"", "\"os\"", "func main()", "fmt.Println"} {
		if !strings.Contains(string(got), expected) {
			t.Errorf("original content %q lost after insertion:\n%s", expected, string(got))
		}
	}
	// Verify new content is present.
	if !strings.Contains(string(got), "\"strings\"") {
		t.Errorf("new content 'strings' not found:\n%s", string(got))
	}
}

func TestPreciseEdit_Safety_IdempotentPreservesSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "a\nb\nc\n"
	os.WriteFile(path, []byte(original), 0o644)
	originalSize := len(original)

	tool := PreciseEditTool{}
	// First insert.
	_, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "b",
		Content:    "x",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	afterFirst, _ := os.ReadFile(path)
	firstSize := len(afterFirst)

	// Second call — idempotent, should not change file.
	result, err := tool.Call(context.Background(), PreciseEditInput{
		FilePath:   "test.txt",
		Anchor:     "b",
		Content:    "x",
		Position:   "after",
		IfMissing:  true,
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !strings.Contains(result.Content, "already present") {
		t.Errorf("expected 'already present': %s", result.Content)
	}

	afterSecond, _ := os.ReadFile(path)
	if len(afterSecond) != firstSize {
		t.Errorf("idempotent call changed file size: %d → %d", firstSize, len(afterSecond))
	}
	if originalSize == firstSize {
		t.Errorf("first insert should have increased size: %d", originalSize)
	}
}

