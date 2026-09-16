package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"avatars/internal/mcp"
)

type MCPCallFunc func(ctx context.Context, spec mcp.ServerSpec, method string, params any) (mcp.CallResponse, error)

type MCPTool struct {
	RegisteredName string
	RemoteName     string
	Description    string
	ReadOnly       bool
	Spec           mcp.ServerSpec
	CallFn         MCPCallFunc
}

func RegisteredMCPToolName(server string, remoteTool string) string {
	return "mcp__" + sanitizeMCPName(server) + "__" + sanitizeMCPName(remoteTool)
}

func IsMCPToolName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "mcp__")
}

func (t MCPTool) Name() string {
	return t.RegisteredName
}

func (t MCPTool) IsConcurrencySafe(input any) bool {
	return t.ReadOnly
}

func (t MCPTool) Call(ctx context.Context, input any) (Result, error) {
	if t.CallFn == nil {
		return Result{}, errors.New("mcp tool caller is not configured")
	}
	args, err := normalizeMCPArgs(input)
	if err != nil {
		return Result{}, err
	}
	response, err := t.CallFn(ctx, t.Spec, "tools/call", map[string]any{
		"name":      t.RemoteName,
		"arguments": args,
	})
	if err != nil {
		return Result{}, err
	}
	if response.Error != nil {
		return Result{}, fmt.Errorf("mcp call failed (%d): %s", response.Error.Code, response.Error.Message)
	}
	return Result{Content: formatMCPToolResult(response.Result)}, nil
}

func sanitizeMCPName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unnamed"
	}
	return out
}

func normalizeMCPArgs(input any) (map[string]any, error) {
	switch value := input.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return value, nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("mcp tool arguments: %w", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("mcp tool arguments: %w", err)
		}
		if decoded == nil {
			decoded = map[string]any{}
		}
		return decoded, nil
	}
}

func formatMCPToolResult(raw json.RawMessage) string {
	if len(bytesTrimSpace(raw)) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return strings.TrimSpace(string(raw))
	}
	if content, ok := payload["content"].([]any); ok {
		parts := make([]string, 0, len(content))
		for _, entry := range content {
			item, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			text := strings.TrimSpace(fmt.Sprint(item["text"]))
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	pretty, err := json.Marshal(payload)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(pretty)
}

func bytesTrimSpace(raw json.RawMessage) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}
