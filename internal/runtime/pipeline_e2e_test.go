package runtime

import (
	"strings"
	"testing"
)

// TestPipelineE2E_FullGoFile verifies a complete Go file survives the full
// code extraction pipeline without truncation or corruption.
func TestPipelineE2E_FullGoFile(t *testing.T) {
	// Simulate a realistic LLM output with FILE: header.
	llmOutput := `FILE: cmd/app/main.go
package main

import (
	"flag"
	"fmt"
	"os"

	"myapp/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		printHelp()
		return
	}
	s, err := store.Open("tasks.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()
	switch os.Args[1] {
	case "add":
		addCmd(s, os.Args[2:])
	case "list":
		listCmd(s)
	case "complete":
		completeCmd(s, os.Args[2:])
	default:
		printHelp()
	}
}

func printHelp() {
	fmt.Println("Usage: app <add|list|complete> [args]")
}

func addCmd(s *store.Store, args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	title := fs.String("title", "", "task title")
	fs.Parse(args)
	if *title == "" {
		fmt.Println("usage: add -title <text>")
		return
	}
	id, err := s.Add(*title)
	if err != nil {
		fmt.Fprintf(os.Stderr, "add: %v\n", err)
		return
	}
	fmt.Printf("Task #%d added.\n", id)
}

func listCmd(s *store.Store) {
	tasks, err := s.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		return
	}
	for _, t := range tasks {
		mark := " "
		if t.Status == "completed" {
			mark = "X"
		}
		fmt.Printf("[%s] #%d %s\n", mark, t.ID, t.Title)
	}
}

func completeCmd(s *store.Store, args []string) {
	fs := flag.NewFlagSet("complete", flag.ExitOnError)
	id := fs.Int64("id", 0, "task id")
	fs.Parse(args)
	if *id == 0 {
		fmt.Println("usage: complete -id <n>")
		return
	}
	if err := s.Complete(*id); err != nil {
		fmt.Fprintf(os.Stderr, "complete: %v\n", err)
		return
	}
	fmt.Printf("Task #%d completed.\n", *id)
}`

	// Step 1: parseBuilderCodeResponse
	files := parseBuilderCodeResponse(llmOutput)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Path != "cmd/app/main.go" {
		t.Fatalf("expected path cmd/app/main.go, got %s", f.Path)
	}

	// Verify content completeness
	openBraces := strings.Count(f.Content, "{")
	closeBraces := strings.Count(f.Content, "}")
	if openBraces != closeBraces {
		t.Fatalf("parseBuilderCodeResponse produced unbalanced braces: %d open, %d close. Content may be truncated.",
			openBraces, closeBraces)
	}
	if !strings.Contains(f.Content, "func main()") {
		t.Fatal("main function missing from parsed content")
	}
	if !strings.Contains(f.Content, "func completeCmd") {
		t.Fatal("completeCmd function missing from parsed content")
	}
	// The last function should have a closing brace
	if !strings.HasSuffix(strings.TrimSpace(f.Content), "}") {
		t.Fatalf("content doesn't end with closing brace, ends with: %q",
			f.Content[len(f.Content)-10:])
	}
	t.Logf("parseBuilderCodeResponse: %d lines, braces %d/%d OK",
		strings.Count(f.Content, "\n")+1, openBraces, closeBraces)

	// Step 2: supersetMerge with old existing content (simulating second run fix)
	existing := `package main
import "fmt"
func main() { fmt.Println("hello") }`
	merged := supersetMerge(existing, f.Content)
	mergedOpen := strings.Count(merged, "{")
	mergedClose := strings.Count(merged, "}")
	if mergedOpen != mergedClose {
		t.Fatalf("supersetMerge produced unbalanced braces: %d open, %d close", mergedOpen, mergedClose)
	}
	if strings.Contains(merged, `fmt.Println("hello")`) && strings.Contains(merged, "store.Open") {
		t.Log("supersetMerge returned merged content (append mode — old + new coexisting)")
	}
	if !strings.Contains(merged, "store.Open") {
		t.Fatal("supersetMerge lost the new content (store.Open missing)")
	}
	if !strings.Contains(merged, "func completeCmd") {
		t.Fatal("supersetMerge lost completeCmd function")
	}
	t.Logf("supersetMerge: %d lines, braces %d/%d OK",
		strings.Count(merged, "\n")+1, mergedOpen, mergedClose)

	// Step 3: Verify the final content compiles (structurally)
	issues := codeCompletenessIssues(merged)
	if len(issues) > 0 {
		t.Fatalf("final content has issues: %v", issues)
	}
	t.Log("Pipeline E2E: PASS — full Go file survives parseBuilderCodeResponse + supersetMerge intact")
}

// TestPipelineE2E_MultiFile verifies multi-file LLM output parsing.
func TestPipelineE2E_MultiFile(t *testing.T) {
	llmOutput := `FILE: models/task.go
package models
type Task struct {
	ID    int64
	Title string
}
FILE: store/db.go
package store
import "database/sql"
func Open(path string) (*sql.DB, error) {
	return sql.Open("sqlite3", path)
}`

	files := parseBuilderCodeResponse(llmOutput)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "models/task.go" {
		t.Errorf("file 0 path: %s", files[0].Path)
	}
	if files[1].Path != "store/db.go" {
		t.Errorf("file 1 path: %s", files[1].Path)
	}
	if !strings.Contains(files[0].Content, "type Task struct") {
		t.Error("file 0 missing Task struct")
	}
	if !strings.Contains(files[1].Content, "func Open") {
		t.Error("file 1 missing Open function")
	}
	t.Logf("Multi-file parse: %d files OK", len(files))
}

// TestPipelineE2E_StripMarkdownFences verifies markdown stripping.
func TestPipelineE2E_StripMarkdownFences(t *testing.T) {
	// LLM wraps code in ``` fences
	llmOutput := "```go\nFILE: main.go\npackage main\nfunc main() {}\n```"
	files := parseBuilderCodeResponse(llmOutput)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if strings.Contains(files[0].Content, "```") {
		t.Error("markdown fences not stripped from content")
	}
	if !strings.Contains(files[0].Content, "func main()") {
		t.Error("content lost after fence stripping")
	}
	t.Log("Markdown fence stripping: OK")
}
