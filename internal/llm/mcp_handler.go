package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MCPToolHandler executes a namespaced MCP tool from the LLM native tool loop.
type MCPToolHandler func(ctx context.Context, name string, args map[string]any) (string, error)

var (
	mcpToolMu      sync.Mutex
	mcpToolHandler MCPToolHandler
)

func SetMCPToolHandler(h MCPToolHandler) {
	mcpToolMu.Lock()
	defer mcpToolMu.Unlock()
	mcpToolHandler = h
}

func ClearMCPToolHandler() {
	SetMCPToolHandler(nil)
}

func IsMCPToolName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "mcp__")
}

func invokeMCPToolHandler(ctx context.Context, name string, args map[string]any) (string, error) {
	mcpToolMu.Lock()
	h := mcpToolHandler
	mcpToolMu.Unlock()
	if h == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return h(ctx, name, args)
}
