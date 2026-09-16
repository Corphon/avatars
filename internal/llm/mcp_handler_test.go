package llm

import (
	"context"
	"testing"
)

func TestExecuteToolByName_MCPHandler(t *testing.T) {
	SetMCPToolHandler(func(ctx context.Context, name string, args map[string]any) (string, error) {
		if name != "mcp__mock__echo" {
			t.Fatalf("unexpected name %s", name)
		}
		return "pong", nil
	})
	defer ClearMCPToolHandler()

	got, err := executeToolByName(context.Background(), "mcp__mock__echo", map[string]any{"text": "ping"})
	if err != nil {
		t.Fatalf("execute mcp tool: %v", err)
	}
	if got != "pong" {
		t.Fatalf("expected pong, got %q", got)
	}
}
