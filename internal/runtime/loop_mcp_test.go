package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/events"
	"avatars/internal/mcp"
	"avatars/internal/registry"
	"avatars/internal/transcript"
	"avatars/internal/verification"
)

func TestDiscoverMCPTools_RegistersEchoAndTranscript(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-mcp-host")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() { _ = writer.Close() }()

	stub := &stubMCPClient{responses: map[string]mcp.CallResponse{
		"tools/list": {JSONRPC: "2.0", Result: json.RawMessage(`{"tools":[{"name":"echo","description":"Echo text","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}]}`)},
		"tools/call": {JSONRPC: "2.0", Result: json.RawMessage(`{"content":[{"type":"text","text":"pong"}]}`)},
	}}
	engine := NewEngine(registry.NewToolRegistry(), writer, events.NewStore(), "session-mcp-host", verification.DefaultPolicy(), nil, stub, nil).
		WithMCPServers([]mcp.ServerSpec{{Name: "mock", URL: "http://example.com/mcp"}})
	engine.discoverMCPTools(context.Background(), "run-1", "task-1")

	found := false
	for _, tool := range engine.builderLLMTools() {
		if tool.Name == "mcp__mock__echo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected mcp__mock__echo in builder tools, got %+v", engine.builderLLMTools())
	}

	result, err := engine.CallTool(context.Background(), "mcp__mock__echo", "call", map[string]any{"text": "ping"})
	if err != nil {
		t.Fatalf("call echo tool failed: %v", err)
	}
	if result.Content != "pong" {
		t.Fatalf("expected pong, got %q", result.Content)
	}
	content, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "mcp.requested") || !strings.Contains(text, "mcp.completed") {
		t.Fatalf("expected mcp events in transcript, got %s", text)
	}
}

func TestDiscoverMCPTools_FailureIsWarning(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-mcp-warn")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() { _ = writer.Close() }()

	stub := &stubMCPClient{err: errors.New("connection refused")}
	engine := NewEngine(registry.NewToolRegistry(), writer, events.NewStore(), "session-mcp-warn", verification.DefaultPolicy(), nil, stub, nil).
		WithMCPServers([]mcp.ServerSpec{{Name: "down", URL: "http://example.com/mcp"}})
	engine.discoverMCPTools(context.Background(), "run-1", "task-1")
	if !strings.Contains(engine.llmWarning, "connection refused") {
		t.Fatalf("expected warning, got %q", engine.llmWarning)
	}
}

func TestMCPTool_PlanModeDeniesWriteAllowsRead(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-mcp-plan")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() { _ = writer.Close() }()

	stub := &stubMCPClient{responses: map[string]mcp.CallResponse{
		"tools/list": {JSONRPC: "2.0", Result: json.RawMessage(`{"tools":[{"name":"echo"},{"name":"list_files"}]}`)},
		"tools/call": {JSONRPC: "2.0", Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)},
	}}
	engine := NewEngine(registry.NewToolRegistry(), writer, events.NewStore(), "session-mcp-plan", verification.DefaultPolicy(), nil, stub, nil).
		WithMCPServers([]mcp.ServerSpec{{Name: "mock", URL: "http://example.com/mcp"}}).
		WithPermissionMode(PermissionModePlan)
	engine.discoverMCPTools(context.Background(), "run-1", "task-1")

	if _, err := engine.CallTool(context.Background(), "mcp__mock__echo", "call", map[string]any{}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected plan mode to deny echo, got %v", err)
	}
	result, err := engine.CallTool(context.Background(), "mcp__mock__list_files", "call", map[string]any{})
	if err != nil {
		t.Fatalf("expected plan mode to allow list_files: %v", err)
	}
	if result.Content != "ok" {
		t.Fatalf("expected ok, got %q", result.Content)
	}
}

func TestHandleLLMMCPTool_UsesPermissionMode(t *testing.T) {
	tempDir := t.TempDir()
	writer, err := transcript.NewJSONLWriter(filepath.Join(tempDir, ".avatars", "sessions"), "session-mcp-llm")
	if err != nil {
		t.Fatalf("new writer failed: %v", err)
	}
	defer func() { _ = writer.Close() }()
	stub := &stubMCPClient{responses: map[string]mcp.CallResponse{
		"tools/list": {JSONRPC: "2.0", Result: json.RawMessage(`{"tools":[{"name":"echo"}]}`)},
		"tools/call": {JSONRPC: "2.0", Result: json.RawMessage(`{"content":[{"type":"text","text":"hi"}]}`)},
	}}
	engine := NewEngine(registry.NewToolRegistry(), writer, events.NewStore(), "session-mcp-llm", verification.DefaultPolicy(), nil, stub, nil).
		WithMCPServers([]mcp.ServerSpec{{Name: "mock", URL: "http://example.com/mcp"}})
	engine.discoverMCPTools(context.Background(), "run-1", "task-1")
	got, err := engine.handleLLMMCPTool(context.Background(), "mcp__mock__echo", map[string]any{"text": "x"})
	if err != nil {
		t.Fatalf("handle llm mcp tool: %v", err)
	}
	if got != "hi" {
		t.Fatalf("expected hi, got %q", got)
	}
}
