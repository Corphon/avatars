package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"avatars/internal/mcp"
)

func TestRegisteredMCPToolName(t *testing.T) {
	got := RegisteredMCPToolName("repo inspector", "read_file")
	if got != "mcp__repo_inspector__read_file" {
		t.Fatalf("unexpected name %q", got)
	}
}

func TestMCPToolCall_FormatsTextContent(t *testing.T) {
	tool := MCPTool{
		RegisteredName: "mcp__mock__echo",
		RemoteName:     "echo",
		Spec:           mcp.ServerSpec{Name: "mock", URL: "http://example.com/mcp"},
		CallFn: func(ctx context.Context, spec mcp.ServerSpec, method string, params any) (mcp.CallResponse, error) {
			if method != "tools/call" {
				t.Fatalf("expected tools/call, got %s", method)
			}
			body, _ := json.Marshal(params)
			if !strings.Contains(string(body), `"echo"`) {
				t.Fatalf("expected remote name in %s", body)
			}
			return mcp.CallResponse{Result: json.RawMessage(`{"content":[{"type":"text","text":"pong"}]}`)}, nil
		},
	}
	result, err := tool.Call(context.Background(), map[string]any{"message": "ping"})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if result.Content != "pong" {
		t.Fatalf("expected pong, got %q", result.Content)
	}
}
