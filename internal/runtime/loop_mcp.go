package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"avatars/internal/llm"
	"avatars/internal/mcp"
	"avatars/internal/tools"
)

func (e *Engine) callMCP(ctx context.Context, spec mcp.ServerSpec, method string, params any) (MCPCallResult, error) {
	if e.mcp == nil {
		return MCPCallResult{}, errors.New("mcp client not configured")
	}

	endpoint := spec.Endpoint()
	runID, taskID, taskTitle, runStartedPayload := e.newMCPRunContext("call", endpoint, method)

	if err := e.emit(runID, taskID, "", "planning", "run.started", "runtime", runStartedPayload, nil); err != nil {
		return MCPCallResult{}, err
	}
	if err := e.emit(runID, taskID, "", "planning", "task.created", "runtime", map[string]any{"title": taskTitle}, nil); err != nil {
		return MCPCallResult{}, err
	}
	if err := e.emit(runID, taskID, "", "executing", "mcp.requested", "mcp", map[string]any{"server_url": endpoint, "method": method, "params": params}, nil); err != nil {
		return MCPCallResult{}, err
	}

	response, err := e.mcp.Call(ctx, spec, method, params)
	if err != nil {
		if appendErr := e.emit(runID, taskID, "", "executing", "mcp.failed", "mcp", map[string]any{"server_url": endpoint, "method": method, "error": err.Error()}, nil); appendErr != nil {
			return MCPCallResult{}, appendErr
		}
		if appendErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "failed", "summary": err.Error()}, nil); appendErr != nil {
			return MCPCallResult{}, appendErr
		}
		if flushErr := e.transcript.Flush(); flushErr != nil {
			return MCPCallResult{}, flushErr
		}
		return MCPCallResult{TranscriptPath: e.transcript.Path()}, err
	}

	if response.Error != nil {
		rpcMessage := fmt.Sprintf("mcp call failed (%d): %s", response.Error.Code, response.Error.Message)
		if appendErr := e.emit(runID, taskID, "", "executing", "mcp.failed", "mcp", map[string]any{
			"server_url": endpoint,
			"method":     method,
			"error": map[string]any{
				"code":    response.Error.Code,
				"message": response.Error.Message,
				"data":    decodeRawMessage(response.Error.Data),
			},
		}, nil); appendErr != nil {
			return MCPCallResult{}, appendErr
		}
		if appendErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "failed", "summary": rpcMessage}, nil); appendErr != nil {
			return MCPCallResult{}, appendErr
		}
		if flushErr := e.transcript.Flush(); flushErr != nil {
			return MCPCallResult{}, flushErr
		}
		return MCPCallResult{TranscriptPath: e.transcript.Path()}, errors.New(rpcMessage)
	}

	if err := e.emit(runID, taskID, "", "executing", "mcp.completed", "mcp", map[string]any{
		"server_url": endpoint,
		"method":     method,
		"result":     decodeRawMessage(response.Result),
	}, nil); err != nil {
		return MCPCallResult{}, err
	}

	summary := mcpCallSummary(endpoint, method)
	if err := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "completed", "summary": summary}, nil); err != nil {
		return MCPCallResult{}, err
	}
	if err := e.emitStableBoundary(runID, taskID, "", "completed", summary, "mcp_terminal", map[string]any{"server_url": endpoint, "method": method}); err != nil {
		return MCPCallResult{}, err
	}
	if err := e.transcript.Flush(); err != nil {
		return MCPCallResult{}, err
	}

	return MCPCallResult{TranscriptPath: e.transcript.Path(), Summary: summary, Result: append([]byte(nil), response.Result...)}, nil
}

func (e *Engine) inspectMCP(ctx context.Context, spec mcp.ServerSpec) (MCPInspectResult, error) {
	if e.mcp == nil {
		return MCPInspectResult{}, errors.New("mcp client not configured")
	}

	endpoint := spec.Endpoint()
	runID, taskID, taskTitle, runStartedPayload := e.newMCPRunContext("inspect", endpoint, "")
	if err := e.emit(runID, taskID, "", "planning", "run.started", "runtime", runStartedPayload, nil); err != nil {
		return MCPInspectResult{}, err
	}
	if err := e.emit(runID, taskID, "", "planning", "task.created", "runtime", map[string]any{"title": taskTitle}, nil); err != nil {
		return MCPInspectResult{}, err
	}

	probes := []struct {
		label      string
		method     string
		resultKey  string
		fallbackID string
	}{
		{label: "Tools", method: "tools/list", resultKey: "tools", fallbackID: "tool"},
		{label: "Resources", method: "resources/list", resultKey: "resources", fallbackID: "resource"},
		{label: "Prompts", method: "prompts/list", resultKey: "prompts", fallbackID: "prompt"},
	}
	sections := make([]MCPInspectionSection, 0, len(probes))
	for _, probe := range probes {
		if err := e.emit(runID, taskID, "", "executing", "mcp.requested", "mcp", map[string]any{"server_url": endpoint, "method": probe.method, "params": nil}, nil); err != nil {
			return MCPInspectResult{}, err
		}
		response, err := e.mcp.Call(ctx, spec, probe.method, nil)
		if err != nil {
			if appendErr := e.emit(runID, taskID, "", "executing", "mcp.failed", "mcp", map[string]any{"server_url": endpoint, "method": probe.method, "error": err.Error()}, nil); appendErr != nil {
				return MCPInspectResult{}, appendErr
			}
			if appendErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "failed", "summary": err.Error()}, nil); appendErr != nil {
				return MCPInspectResult{}, appendErr
			}
			if flushErr := e.transcript.Flush(); flushErr != nil {
				return MCPInspectResult{}, flushErr
			}
			return MCPInspectResult{TranscriptPath: e.transcript.Path()}, err
		}
		if response.Error != nil {
			if appendErr := e.emit(runID, taskID, "", "executing", "mcp.failed", "mcp", map[string]any{"server_url": endpoint, "method": probe.method, "error": map[string]any{"code": response.Error.Code, "message": response.Error.Message, "data": decodeRawMessage(response.Error.Data)}}, nil); appendErr != nil {
				return MCPInspectResult{}, appendErr
			}
			// -32601 method not found = capability unsupported; keep probing the rest.
			if response.Error.Code == -32601 {
				sections = append(sections, MCPInspectionSection{Label: probe.label, Method: probe.method, Supported: false})
				continue
			}
			rpcMessage := fmt.Sprintf("mcp inspect failed on %s (%d): %s", probe.method, response.Error.Code, response.Error.Message)
			if appendErr := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "failed", "summary": rpcMessage}, nil); appendErr != nil {
				return MCPInspectResult{}, appendErr
			}
			if flushErr := e.transcript.Flush(); flushErr != nil {
				return MCPInspectResult{}, flushErr
			}
			return MCPInspectResult{TranscriptPath: e.transcript.Path(), Sections: sections}, errors.New(rpcMessage)
		}
		if err := e.emit(runID, taskID, "", "executing", "mcp.completed", "mcp", map[string]any{"server_url": endpoint, "method": probe.method, "result": decodeRawMessage(response.Result)}, nil); err != nil {
			return MCPInspectResult{}, err
		}
		sections = append(sections, MCPInspectionSection{Label: probe.label, Method: probe.method, Supported: true, Names: extractMCPInspectionNames(response.Result, probe.resultKey, probe.fallbackID)})
	}

	summary := mcpInspectSummary(endpoint)
	if err := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "completed", "summary": summary}, nil); err != nil {
		return MCPInspectResult{}, err
	}
	if err := e.emitStableBoundary(runID, taskID, "", "completed", summary, "mcp_terminal", map[string]any{"server_url": endpoint, "mode": "inspect"}); err != nil {
		return MCPInspectResult{}, err
	}
	if err := e.transcript.Flush(); err != nil {
		return MCPInspectResult{}, err
	}
	return MCPInspectResult{TranscriptPath: e.transcript.Path(), Summary: summary, Sections: sections}, nil
}

func (e *Engine) discoverMCPTools(ctx context.Context, runID string, taskID string) {
	if e == nil || e.mcp == nil || e.mcpAttached {
		return
	}
	e.mcpAttached = true
	warnings := make([]string, 0, len(e.mcpServers))
	for _, spec := range e.mcpServers {
		if !spec.Configured() {
			continue
		}
		response, err := e.mcp.Call(ctx, spec, "tools/list", nil)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("mcp %s: %s", spec.Key(), err.Error()))
			continue
		}
		if response.Error != nil {
			warnings = append(warnings, fmt.Sprintf("mcp %s tools/list (%d): %s", spec.Key(), response.Error.Code, response.Error.Message))
			continue
		}
		listed := parseMCPListedTools(response.Result)
		if len(listed) == 0 {
			continue
		}
		endpoint := spec.Endpoint()
		for _, listedTool := range listed {
			registered := tools.RegisteredMCPToolName(spec.Key(), listedTool.Name)
			mcpTool := tools.MCPTool{
				RegisteredName: registered,
				RemoteName:     listedTool.Name,
				Description:    listedTool.Description,
				ReadOnly:       mcpRemoteToolLooksReadOnly(listedTool.Name),
				CallFn: func(callCtx context.Context, callSpec mcp.ServerSpec, method string, params any) (mcp.CallResponse, error) {
					return e.callMCPRaw(callCtx, runID, taskID, endpoint, callSpec, method, params)
				},
				Spec: spec,
			}
			if e.tools != nil {
				if _, exists := e.tools.Get(registered); exists {
					continue
				}
				if err := e.tools.Register(mcpTool); err != nil {
					warnings = append(warnings, fmt.Sprintf("mcp register %s: %s", registered, err.Error()))
					continue
				}
			}
			e.mcpLLMTools = append(e.mcpLLMTools, llm.ToolDefinition{
				Name:        registered,
				Description: listedTool.Description,
				Parameters:  listedTool.InputSchema,
			})
		}
	}
	if len(warnings) == 0 {
		return
	}
	joined := strings.Join(warnings, "; ")
	if strings.TrimSpace(e.llmWarning) == "" {
		e.llmWarning = joined
		return
	}
	e.llmWarning = e.llmWarning + "; " + joined
}

func (e *Engine) builderLLMTools() []llm.ToolDefinition {
	out := append([]llm.ToolDefinition{}, llm.StandardCodeTools()...)
	if e == nil {
		return out
	}
	return append(out, e.mcpLLMTools...)
}

func (e *Engine) handleLLMMCPTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if e == nil || e.tools == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	decision, _, reason := permissionModePolicyDecision(e.permissionMode, name, args)
	if decision == "deny" || decision == "ask" {
		return "", fmt.Errorf("permission denied: %s", reason)
	}
	toolDefinition, ok := e.tools.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	result, err := toolDefinition.Call(ctx, args)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func (e *Engine) callMCPRaw(ctx context.Context, runID string, taskID string, endpoint string, spec mcp.ServerSpec, method string, params any) (mcp.CallResponse, error) {
	if e == nil || e.mcp == nil {
		return mcp.CallResponse{}, errors.New("mcp client not configured")
	}
	if strings.TrimSpace(runID) != "" {
		_ = e.emit(runID, taskID, "", "executing", "mcp.requested", "mcp", map[string]any{"server_url": endpoint, "method": method, "params": params}, nil)
	}
	response, err := e.mcp.Call(ctx, spec, method, params)
	if err != nil {
		if strings.TrimSpace(runID) != "" {
			_ = e.emit(runID, taskID, "", "executing", "mcp.failed", "mcp", map[string]any{"server_url": endpoint, "method": method, "error": err.Error()}, nil)
		}
		return mcp.CallResponse{}, err
	}
	if response.Error != nil {
		if strings.TrimSpace(runID) != "" {
			_ = e.emit(runID, taskID, "", "executing", "mcp.failed", "mcp", map[string]any{
				"server_url": endpoint,
				"method":     method,
				"error":      map[string]any{"code": response.Error.Code, "message": response.Error.Message, "data": decodeRawMessage(response.Error.Data)},
			}, nil)
		}
		return response, fmt.Errorf("mcp call failed (%d): %s", response.Error.Code, response.Error.Message)
	}
	if strings.TrimSpace(runID) != "" {
		_ = e.emit(runID, taskID, "", "executing", "mcp.completed", "mcp", map[string]any{"server_url": endpoint, "method": method, "result": decodeRawMessage(response.Result)}, nil)
	}
	return response, nil
}

func (e *Engine) newMCPRunContext(mode string, serverURL string, method string) (string, string, string, map[string]any) {
	runID := fmt.Sprintf("%s-run-%d", e.sessionID, time.Now().UTC().UnixNano())
	taskID := runID + "-task-mcp"
	taskTitle := fmt.Sprintf("MCP call %s", method)
	runStartedPayload := map[string]any{"mode": "mcp", "server_url": serverURL, "method": method}
	if mode == "inspect" {
		taskID = runID + "-task-mcp-inspect"
		taskTitle = fmt.Sprintf("MCP inspect %s", serverURL)
		runStartedPayload = map[string]any{"mode": "mcp_inspect", "server_url": serverURL}
	}
	if e.taskID != "" {
		taskID = e.taskID
		runStartedPayload["task_workspace_id"] = e.taskID
		if e.taskRoot != "" {
			runStartedPayload["task_workspace_root"] = e.taskRoot
		}
	}
	return runID, taskID, taskTitle, runStartedPayload
}

func mcpCallSummary(serverURL string, method string) string {
	return fmt.Sprintf("MCP call to %s method %s completed.", serverURL, method)
}

func mcpInspectSummary(serverURL string) string {
	return fmt.Sprintf("MCP capability inspection completed for %s.", serverURL)
}

type mcpListedTool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

func parseMCPListedTools(raw json.RawMessage) []mcpListedTool {
	decoded := decodeRawMessage(raw)
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil
	}
	entries, ok := root["tools"].([]any)
	if !ok {
		return nil
	}
	out := make([]mcpListedTool, 0, len(entries))
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(item["name"]))
		if name == "" {
			continue
		}
		listed := mcpListedTool{Name: name, Description: strings.TrimSpace(fmt.Sprint(item["description"]))}
		if schema, ok := item["inputSchema"].(map[string]any); ok && schema != nil {
			listed.InputSchema = schema
		} else {
			listed.InputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, listed)
	}
	return out
}

func extractMCPInspectionNames(raw json.RawMessage, resultKey string, fallbackID string) []string {
	decoded := decodeRawMessage(raw)
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil
	}
	entries, ok := root[resultKey].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"name", "title", "uri"} {
			value, ok := item[key]
			if !ok {
				continue
			}
			text := strings.TrimSpace(fmt.Sprint(value))
			if text == "" {
				continue
			}
			names = append(names, text)
			goto nextEntry
		}
		names = append(names, fallbackID)
	nextEntry:
	}
	return names
}

func mcpToolLooksReadOnly(toolName string) bool {
	name := strings.TrimSpace(toolName)
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(name, "__")
		if len(parts) >= 3 {
			return mcpRemoteToolLooksReadOnly(parts[len(parts)-1])
		}
	}
	return mcpRemoteToolLooksReadOnly(name)
}

func mcpRemoteToolLooksReadOnly(remoteName string) bool {
	lowered := strings.ToLower(strings.TrimSpace(remoteName))
	for _, token := range []string{"list", "get", "read", "search", "find", "describe", "inspect", "show"} {
		if lowered == token || strings.HasPrefix(lowered, token+"_") || strings.Contains(lowered, "_"+token) {
			return true
		}
	}
	return false
}
