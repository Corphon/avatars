package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/llm"
)

func TestCheckBuilderTruncation_FinishReasonLength(t *testing.T) {
	resp := llm.Response{FinishReason: "length"}
	files := []builderCodeFile{
		{Path: "main.go", Content: "package main\n\nfunc main() {\n\t// valid code\n}\n"},
	}
	result := CheckBuilderTruncation(resp, files)
	if !result.IsTruncated {
		t.Fatal("expected IsTruncated=true for finish_reason=length")
	}
}

func TestCheckBuilderTruncation_FinishReasonStop(t *testing.T) {
	resp := llm.Response{FinishReason: "stop"}
	files := []builderCodeFile{
		{Path: "main.go", Content: "package main\n\nfunc main() {\n\t// valid code\n}\n"},
	}
	result := CheckBuilderTruncation(resp, files)
	if result.IsTruncated {
		t.Fatal("expected IsTruncated=false for finish_reason=stop with valid code")
	}
}

func TestCheckBuilderTruncation_UnbalancedBraces(t *testing.T) {
	resp := llm.Response{FinishReason: "stop"} // API didn't signal, but code is broken
	files := []builderCodeFile{
		{Path: "broken.go", Content: "package main\n\nfunc main() {\n\tif true {\n\t// missing closing braces!\n"},
	}
	result := CheckBuilderTruncation(resp, files)
	if !result.IsTruncated {
		t.Fatal("expected IsTruncated=true for code with unbalanced braces")
	}
	if len(result.IncompleteFiles) == 0 {
		t.Fatal("expected incomplete files to include broken.go")
	}
}

func TestCheckBuilderTruncation_CompleteCode(t *testing.T) {
	resp := llm.Response{FinishReason: "stop"}
	files := []builderCodeFile{
		{Path: "valid.go", Content: `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`},
	}
	result := CheckBuilderTruncation(resp, files)
	if result.IsTruncated {
		t.Fatalf("expected IsTruncated=false for complete code, got issues: %v", result.Issues)
	}
}

func TestEscalateMaxTokens(t *testing.T) {
	if got := EscalateMaxTokens(0); got != DefaultBuilderMaxTokens {
		t.Fatalf("0 → expected %d, got %d", DefaultBuilderMaxTokens, got)
	}
	if got := EscalateMaxTokens(8000); got != DefaultBuilderMaxTokens {
		t.Fatalf("below daily → expected %d, got %d", DefaultBuilderMaxTokens, got)
	}
	if got := EscalateMaxTokens(DefaultBuilderMaxTokens); got != DefaultBuilderMaxTokens*2 {
		t.Fatalf("daily → expected %d, got %d", DefaultBuilderMaxTokens*2, got)
	}
	if got := EscalateMaxTokens(DefaultBuilderMaxTokens * 2); got != EscalatedBuilderMaxTokens {
		t.Fatalf("doubled → expected %d, got %d", EscalatedBuilderMaxTokens, got)
	}
	if got := EscalateMaxTokens(EscalatedBuilderMaxTokens); got != EscalatedBuilderMaxTokens {
		t.Fatalf("ceiling must hold, got %d", got)
	}
	if got := EscalateMaxTokens(99999); got != EscalatedBuilderMaxTokens {
		t.Fatalf("above ceiling → expected %d, got %d", EscalatedBuilderMaxTokens, got)
	}
}

func TestBuildRecoveryPrompt(t *testing.T) {
	prompt := BuildRecoveryPrompt([]string{"a.go", "b.go"})
	if !strings.Contains(prompt, "truncated") {
		t.Fatal("recovery prompt should mention truncation")
	}
	if !strings.Contains(prompt, "a.go") {
		t.Fatal("recovery prompt should list incomplete files")
	}
	if !strings.Contains(prompt, "b.go") {
		t.Fatal("recovery prompt should list incomplete files")
	}
	if !strings.Contains(prompt, "Produce ONLY the continuation") {
		t.Fatal("recovery prompt should tell Builder to continue from cut-off point")
	}
}

func TestCodeCompletenessIssues_Complete(t *testing.T) {
	complete := `package main

func main() {
	fmt.Println("hello")
}
`
	issues := codeCompletenessIssues(complete)
	if len(issues) > 0 {
		t.Fatalf("expected no issues for complete code, got: %v", issues)
	}
}

func TestCodeCompletenessIssues_Truncated(t *testing.T) {
	truncated := `package main

func main() {
	if true {
		fmt.Println("hello")
	// missing closing braces!
`
	issues := codeCompletenessIssues(truncated)
	if len(issues) == 0 {
		t.Fatal("expected issues for truncated code")
	}
	t.Logf("issues found: %v", issues)
}

func TestCodeCompletenessIssues_Empty(t *testing.T) {
	issues := codeCompletenessIssues("")
	if len(issues) == 0 {
		t.Fatal("expected issues for empty content")
	}
}

func TestCodeCompletenessIssues_DocParensAndByteSlicesAreComplete(t *testing.T) {
	src := `package bloomx

import "errors"

var errInvalidFPRate = errors.New("bloomx: false-positive rate must be in the open interval (0, 1)")

func Add(data []byte) {
	_ = data
}

func MightContain(data []byte) bool {
	return true
}
`
	issues := codeCompletenessIssues(src)
	if len(issues) > 0 {
		t.Fatalf("complete library with doc parens and []byte must not look truncated: %v", issues)
	}
	resp := llm.Response{FinishReason: "stop"}
	got := CheckBuilderTruncation(resp, []builderCodeFile{{Path: "bloom.go", Content: src}})
	if got.IsTruncated {
		t.Fatalf("CheckBuilderTruncation must not retry a complete file: %+v", got)
	}
}

func TestCheckBuilderTruncation_ParserBeatsHeuristicOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bloom.go")
	src := `package bloomx

import "errors"

var errInvalidFPRate = errors.New("bloomx: false-positive rate must be in the open interval (0, 1)")

func Add(data []byte) {}
`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	resp := llm.Response{FinishReason: "stop"}
	got := CheckBuilderTruncation(resp, []builderCodeFile{{Path: path, Content: ""}})
	if got.IsTruncated {
		t.Fatalf("disk file that parses must not look truncated: %+v", got)
	}
}

func TestBuilderFilesLookComplete(t *testing.T) {
	src := "package p\nfunc F() {}\n"
	if !builderFilesLookComplete([]builderCodeFile{{Path: "x.go", Content: src}}) {
		t.Fatal("parseable go must look complete")
	}
	if builderFilesLookComplete([]builderCodeFile{{Path: "x.go", Content: "package p\nfunc F() {\n"}}) {
		t.Fatal("truncated go must not look complete")
	}
}

func TestCodeCompletenessIssues_UnclosedBacktick(t *testing.T) {
	truncated := "const tmpl = `unclosed raw string\n"
	issues := codeCompletenessIssues(truncated)
	if len(issues) == 0 {
		t.Fatal("expected issues for unclosed backtick")
	}
}

func TestLLMIsTruncated(t *testing.T) {
	if !llm.IsTruncated(llm.Response{FinishReason: "length"}) {
		t.Fatal("IsTruncated should return true for 'length'")
	}
	if llm.IsTruncated(llm.Response{FinishReason: "stop"}) {
		t.Fatal("IsTruncated should return false for 'stop'")
	}
	if llm.IsTruncated(llm.Response{FinishReason: "tool_calls"}) {
		t.Fatal("IsTruncated should return false for 'tool_calls'")
	}
	if llm.IsTruncated(llm.Response{FinishReason: ""}) {
		t.Fatal("IsTruncated should return false for empty finish_reason")
	}
}

func TestCheckBuilderTruncation_SkipsAlreadyOnDiskEmpty(t *testing.T) {
	resp := llm.Response{FinishReason: "stop"}
	files := []builderCodeFile{
		{Path: "internal/join/join.go", Content: "", AlreadyOnDisk: true},
		{Path: "docs/workflow/phase1.md", Content: ""},
	}
	result := CheckBuilderTruncation(resp, files)
	if result.IsTruncated {
		t.Fatalf("AlreadyOnDisk + empty workflow doc must not trigger truncation: %+v", result)
	}
}

func TestCheckBuilderTruncation_EmptySourceStillFlags(t *testing.T) {
	resp := llm.Response{FinishReason: "stop"}
	files := []builderCodeFile{
		{Path: "main.go", Content: ""},
	}
	result := CheckBuilderTruncation(resp, files)
	if !result.IsTruncated {
		t.Fatal("empty source file should still flag truncation")
	}
}

func TestF99_TruncationCountIsNotResetOnNestedSuccess(t *testing.T) {
	e := &Engine{builderRetryStates: map[string]*builderRetryState{
		"builder-1": {TruncationCount: 2, EscalatedMaxTokens: EscalatedBuilderMaxTokens},
	}}
	state := e.ensureBuilderRetryState("builder-1")
	if state.TruncationCount != 2 {
		t.Fatalf("nested generateBuilderCodeSingle must keep TruncationCount, got %d", state.TruncationCount)
	}
}
