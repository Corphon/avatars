package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandardCodeTools(t *testing.T) {
	tools := StandardCodeTools()
	if len(tools) != 6 {
		t.Fatalf("expected 6 standard code tools (incl. git_diff), got %d", len(tools))
	}
	names := make(map[string]bool)
	for _, td := range tools {
		names[td.Name] = true
	}
	for _, want := range []string{"read_file", "write_file", "edit_file", "precise_edit", "shell_done", "git_diff"} {
		if !names[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
}

func TestStandardReadOnlyTools(t *testing.T) {
	tools := StandardReadOnlyTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 read-only tool, got %d", len(tools))
	}
	if tools[0].Name != "read_file" {
		t.Errorf("unexpected read-only tools: %v", tools)
	}
	for _, td := range tools {
		if td.Name == "shell_done" {
			t.Fatal("shell_done must not be classified as read-only")
		}
	}
}

func TestBuildGenkitToolRefs(t *testing.T) {
	tools := StandardCodeTools()
	refs := buildGenkitToolRefs(tools)
	if len(refs) != 6 {
		t.Fatalf("expected 6 genkit tool refs (incl. git_diff), got %d", len(refs))
	}
	names := map[string]bool{}
	for _, ref := range refs {
		if ref.Name() == "" {
			t.Error("tool ref has empty name")
		}
		names[ref.Name()] = true
	}
	if !names["git_diff"] {
		t.Error("git_diff must be registered on genkit path (S5.5)")
	}
}

func TestExecuteToolByName_ReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "hello world"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := executeToolByName(context.Background(), "read_file", map[string]any{
		"path": path,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("expected %q, got %q", content, result)
	}
}

func TestExecuteToolByName_ReadFile_Directory(t *testing.T) {
	dir := t.TempDir()
	_, err := executeToolByName(context.Background(), "read_file", map[string]any{
		"path": dir,
	})
	if err == nil {
		t.Error("expected error for directory path, got nil")
	}
}

func TestExecuteToolByName_WriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	content := "generated content"

	result, err := executeToolByName(context.Background(), "write_file", map[string]any{
		"path":    path,
		"content": content,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Successfully wrote") {
		t.Errorf("unexpected result: %s", result)
	}

	// Verify file contents.
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) != content {
		t.Errorf("expected %q, got %q", content, string(read))
	}
}

func TestExecuteToolByName_EditFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	original := "package main\n\nfunc main() {\n\toldLogic()\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := executeToolByName(context.Background(), "edit_file", map[string]any{
		"path":       path,
		"old_string": "oldLogic()",
		"new_string": "newLogic()",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Successfully edited") {
		t.Errorf("unexpected result: %s", result)
	}

	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := "package main\n\nfunc main() {\n\tnewLogic()\n}\n"
	if string(read) != expected {
		t.Errorf("expected %q, got %q", expected, string(read))
	}
}

func TestExecuteToolByName_EditFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	original := "package main\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := executeToolByName(context.Background(), "edit_file", map[string]any{
		"path":       path,
		"old_string": "nonexistent code",
		"new_string": "replacement",
	})
	if err == nil {
		t.Error("expected error for old_string not found, got nil")
	}
}

func TestExecuteToolByName_EditFile_MultipleMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	original := "func a() { f() }\nfunc b() { f() }\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := executeToolByName(context.Background(), "edit_file", map[string]any{
		"path":       path,
		"old_string": "f()",
		"new_string": "g()",
	})
	if err == nil {
		t.Error("expected error for multiple matches, got nil")
	}
}

func TestParseDeepSeekInvokeXML(t *testing.T) {
	// Simulate DeepSeek's actual tool call output format.
	input := `<函数调用>
<invoke name="read_file">
<parameter name="path" string="true">foo/bar.go</parameter>
</invoke>
</函数调用>`

	calls := parseDeepSeekInvokeXML(input)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read_file" {
		t.Errorf("expected read_file, got %s", calls[0].Function.Name)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("failed to parse arguments: %v", err)
	}
	if args["path"] != "foo/bar.go" {
		t.Errorf("expected path=foo/bar.go, got %v", args["path"])
	}
}

func TestParseDeepSeekInvokeXML_MultiParam(t *testing.T) {
	input := `<函数调用>
<invoke name="edit_file">
<parameter name="path" string="true">src/main.go</parameter>
<parameter name="old_string" string="true">Hello, World!</parameter>
<parameter name="new_string" string="true">你好, 世界!</parameter>
</invoke>
</函数调用>`

	calls := parseDeepSeekInvokeXML(input)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "edit_file" {
		t.Errorf("expected edit_file, got %s", calls[0].Function.Name)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("failed to parse arguments: %v", err)
	}
	if args["path"] != "src/main.go" {
		t.Errorf("path mismatch: %v", args["path"])
	}
	if args["old_string"] != "Hello, World!" {
		t.Errorf("old_string mismatch: %v", args["old_string"])
	}
}

func TestParseDeepSeekInvokeXML_EnglishWrapper(t *testing.T) {
	// This is the actual format deepseek-v4-pro uses.
	input := `<function_calls>
<invoke name="edit_file">
<parameter name="path" string="true">toolcall_test/greeter.go</parameter>
<parameter name="old_string" string="true">	fmt.Printf("Hello, %s!\n", name)</parameter>
<parameter name="new_string" string="true">	fmt.Printf("你好, %s!\n", name)</parameter>
</invoke>
</function_calls>`

	calls := parseTextToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call — parser should detect <function_calls> wrapper")
	}
	if calls[0].Function.Name != "edit_file" {
		t.Errorf("expected edit_file, got %s", calls[0].Function.Name)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("failed to parse arguments: %v", err)
	}
	if args["path"] != "toolcall_test/greeter.go" {
		t.Errorf("path mismatch: %v", args["path"])
	}
	if args["old_string"] != "\tfmt.Printf(\"Hello, %s!\\n\", name)" {
		t.Errorf("old_string mismatch: %q", args["old_string"])
	}
}

func TestNormalizeBrackets_FF5C(t *testing.T) {
	// Simulate the actual byte pattern DeepSeek V4 Pro outputs.
	// FF5C FF5C = ｜｜ replaces < angle brackets.
	input := "｜｜tool_calls>\n<｜｜invoke name=\"edit_file\">\n<｜｜/"

	result := normalizeBrackets(input)
	if !strings.Contains(result, "<tool_calls>") {
		t.Errorf("expected normalized <tool_calls>, got: %q", result)
	}
	if !strings.Contains(result, "<invoke name=\"edit_file\">") {
		t.Errorf("expected normalized <invoke>, got: %q", result)
	}
}

func TestParseTextToolCalls_FF5CFormat(t *testing.T) {
	// Full test: ｜｜ bracketed tool calls with the actual DeepSeek V4 Pro format.
	input := "现在修改文件：\n｜｜function_calls>\n｜｜invoke name=\"edit_file\">\n｜｜parameter name=\"path\" string=\"true\">foo.go｜｜/parameter>\n｜｜parameter name=\"old_string\" string=\"true\">Hello｜｜/parameter>\n｜｜/invoke>\n｜｜/function_calls>"

	calls := parseTextToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call from FF5C-bracketed format")
	}
	if calls[0].Function.Name != "edit_file" {
		t.Errorf("expected edit_file, got %s", calls[0].Function.Name)
	}
}

func TestParseTextToolCalls_ToolCallsWrapper(t *testing.T) {
	// The actual format DeepSeek V4 Pro uses with the e2e prompt.
	input := `<tool_calls>
<invoke name="edit_file">
<parameter name="path" string="true">foo.go</parameter>
<parameter name="old_string" string="true">Hello</parameter>
<parameter name="new_string" string="true">你好</parameter>
</invoke>
</tool_calls>`

	calls := parseTextToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call from <tool_calls> wrapper")
	}
	if calls[0].Function.Name != "edit_file" {
		t.Errorf("expected edit_file, got %s", calls[0].Function.Name)
	}
}

func TestRobustParseInvokeCalls_NoClosingTags(t *testing.T) {
	// The real DeepSeek V4 Pro output often lacks </invoke> and </tool_calls>
	// closing tags. The parser must work without them.
	text := "找到了目标行。\n\n｜｜tool_calls>\n<｜｜invoke name=\"edit_file\">\n<｜｜parameter name=\"path\" string=\"true\">toolcall_test/greeter.go</parameter>\n<｜｜parameter name=\"old_string\" string=\"true\">Hello</parameter>\n<｜｜parameter name=\"new_string\" string=\"true\">你好</parameter>"

	norm := normalizeBrackets(text)
	calls := robustParseInvokeCalls(norm)
	if len(calls) == 0 {
		t.Fatal("robustParseInvokeCalls returned 0 calls without closing tags")
	}
	if calls[0].Function.Name != "edit_file" {
		t.Errorf("expected edit_file, got %s", calls[0].Function.Name)
	}
	var args map[string]any
	json.Unmarshal([]byte(calls[0].Function.Arguments), &args)
	if args["path"] != "toolcall_test/greeter.go" {
		t.Errorf("path mismatch: %v", args["path"])
	}
}

func TestParseTextToolCalls_FallbackFormat(t *testing.T) {
	// Test the full parseTextToolCalls pipeline with DeepSeek format.
	input := `<函数调用>
<invoke name="shell_done">
<parameter name="command" string="true">go build ./...</parameter>
<parameter name="working_dir" string="true">/project</parameter>
</invoke>
</函数调用>`

	calls := parseTextToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call")
	}
	if calls[0].Function.Name != "shell_done" {
		t.Errorf("expected shell_done, got %s", calls[0].Function.Name)
	}
}

func TestExecuteToolByName_UnknownTool(t *testing.T) {
	_, err := executeToolByName(context.Background(), "unknown_tool", nil)
	if err == nil {
		t.Error("expected error for unknown tool, got nil")
	}
}
