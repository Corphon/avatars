package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"avatars/internal/localhttp"
)

// Tool represents an MCP tool exposed by the avatars server.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToolLister returns the set of tools available on this server.
type ToolLister func() []Tool

// ToolCaller dispatches a tool call and returns the result text.
type ToolCaller func(toolName string, params map[string]any) (string, error)

// Handler returns the JSON-RPC mux, wrapped with a token gate when token is set.
func Handler(lister ToolLister, caller ToolCaller, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req requestEnvelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeMCPError(w, -32700, "parse error", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			tools := lister()
			writeMCPResult(w, req.ID, map[string]any{"tools": tools})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			switch p := req.Params.(type) {
			case json.RawMessage:
				_ = json.Unmarshal(p, &params)
			case map[string]any:
				if n, ok := p["name"].(string); ok {
					params.Name = n
				}
				if a, ok := p["arguments"].(map[string]any); ok {
					params.Arguments = a
				}
			}
			if params.Name == "" {
				writeMCPError(w, -32602, "invalid params: name is required", req.ID)
				return
			}
			result, err := caller(params.Name, params.Arguments)
			if err != nil {
				writeMCPResult(w, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "Error: " + err.Error()}},
					"isError": true,
				})
				return
			}
			writeMCPResult(w, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": result}},
			})
		case "initialize":
			writeMCPResult(w, req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "avatars", "version": "0.1.0"},
				"capabilities":    map[string]any{"tools": map[string]bool{}},
			})
		default:
			writeMCPError(w, -32601, "method not found: "+req.Method, req.ID)
		}
	})
	if strings.TrimSpace(token) == "" {
		return mux
	}
	return localhttp.Middleware(token, mux)
}

// ServeMCP starts an HTTP server that exposes avatars skills as MCP
// tools via a JSON-RPC 2.0 endpoint at POST /mcp.
func ServeMCP(addr string, lister ToolLister, caller ToolCaller) error {
	if addr == "" {
		addr = ":5100"
	}
	addr = localhttp.NormalizeAddr(addr)
	token := localhttp.ResolveToken(localhttp.MCPTokenEnv, localhttp.StageTokenEnv)
	handler := Handler(lister, caller, token)
	server := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}
	localhttp.LogListen("MCP server", addr+"/mcp", token)
	return server.ListenAndServe()
}

func writeMCPResult(w http.ResponseWriter, id string, result any) {
	resp := CallResponse{JSONRPC: "2.0", ID: id}
	raw, _ := json.Marshal(result)
	resp.Result = raw
	_ = json.NewEncoder(w).Encode(resp)
}

func writeMCPError(w http.ResponseWriter, code int, message string, id string) {
	resp := CallResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
