package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"avatars/internal/localhttp"
)

func TestS7A_MCPAuth(t *testing.T) {
	h := Handler(func() []Tool {
		return []Tool{{Name: "ping", Description: "ping"}}
	}, func(toolName string, params map[string]any) (string, error) {
		return "called " + toolName, nil
	}, "mcp-secret")

	unauth := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"ping"}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", rec.Code)
	}

	wrong := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"ping"}}`))
	wrong.Header.Set(localhttp.HeaderName, "nope")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, wrong)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}

	ok := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"ping"}}`))
	ok.Header.Set(localhttp.HeaderName, "mcp-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, ok)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed call: got %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "called ping") {
		t.Fatalf("expected tool result, got %s", rec.Body.String())
	}
}
