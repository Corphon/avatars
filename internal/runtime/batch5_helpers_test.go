package runtime

import "testing"

func TestIsTransientLLMNetworkError(t *testing.T) {
	if !isTransientLLMNetworkError(`Post "https://api.deepseek.com/chat/completions": read tcp 1.2.3.4:1->5.6.7.8:443: wsarecv: A connection attempt failed`) {
		t.Fatal("expected network error")
	}
	if isTransientLLMNetworkError("exceeded max tool turns (18)") {
		t.Fatal("turn cap is not network")
	}
}

func TestIsProtectedWorkflowManifest(t *testing.T) {
	if !isProtectedWorkflowManifest("docs/workflow/avatars_todo.md") {
		t.Fatal("todo should be protected")
	}
	kept, _, dropped := sanitizeBuilderPaths([]builderCodeFile{
		{Path: "docs/workflow/avatars_todo.md", Content: "# wiped"},
		{Path: "cmd/server/main.go", Content: "package main"},
	})
	if len(dropped) != 1 {
		t.Fatalf("dropped=%v", dropped)
	}
	if len(kept) != 1 || kept[0].Path != "cmd/server/main.go" {
		t.Fatalf("kept=%v", kept)
	}
}
