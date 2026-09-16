// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientCall_SendsJSONRPCRequest(t *testing.T) {
	var methods []string
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", req.Method)
		}
		if req.URL.String() != "http://example.com/mcp" {
			t.Fatalf("unexpected url %s", req.URL.String())
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}
		var envelope requestEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		methods = append(methods, envelope.Method)
		switch envelope.Method {
		case "initialize":
			return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`)
		case "notifications/initialized":
			return jsonResponse(http.StatusAccepted, "")
		case "tools/list":
			if !strings.Contains(string(body), `"cursor":"0"`) {
				t.Fatalf("expected params in %s", body)
			}
			return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":"mcp-1","result":{"tools":[{"name":"read_file"}]}}`)
		default:
			t.Fatalf("unexpected method %s", envelope.Method)
			return nil, nil
		}
	})}

	client := NewClient(Config{}, httpClient)
	response, err := client.Call(context.Background(), HTTPSpec("http://example.com/mcp"), "tools/list", map[string]any{"cursor": "0"})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("expected nil rpc error, got %+v", response.Error)
	}
	if !strings.Contains(string(response.Result), `"read_file"`) {
		t.Fatalf("expected read_file in result, got %s", string(response.Result))
	}
	if len(methods) < 3 || methods[0] != "initialize" || methods[1] != "notifications/initialized" || methods[2] != "tools/list" {
		t.Fatalf("expected initialize then initialized then tools/list, got %v", methods)
	}
}

func TestClientCall_SendsSessionHeaderAfterInitialize(t *testing.T) {
	var listHadSession bool
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}
		var envelope requestEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch envelope.Method {
		case "initialize":
			resp, err := jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":"1","result":{}}`)
			if err != nil {
				return nil, err
			}
			resp.Header.Set("Mcp-Session-Id", "sess-1")
			return resp, nil
		case "notifications/initialized":
			if req.Header.Get("Mcp-Session-Id") != "sess-1" {
				t.Fatalf("expected session header on initialized, got %q", req.Header.Get("Mcp-Session-Id"))
			}
			return jsonResponse(http.StatusNoContent, "")
		case "tools/list":
			listHadSession = req.Header.Get("Mcp-Session-Id") == "sess-1"
			return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":"2","result":{"tools":[]}}`)
		default:
			t.Fatalf("unexpected method %s", envelope.Method)
			return nil, nil
		}
	})}

	client := NewClient(Config{}, httpClient)
	if _, err := client.Call(context.Background(), HTTPSpec("http://example.com/mcp"), "tools/list", nil); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if !listHadSession {
		t.Fatal("expected tools/list to send Mcp-Session-Id")
	}
}

func TestClientCall_SendsConfiguredHeaders(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("expected expanded auth header, got %q", req.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(req.Body)
		var envelope requestEnvelope
		_ = json.Unmarshal(body, &envelope)
		if envelope.Method == "initialize" {
			return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		}
		if envelope.Method == "notifications/initialized" {
			return jsonResponse(http.StatusAccepted, "")
		}
		return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":"1","result":{}}`)
	})}

	client := NewClient(Config{}, httpClient)
	spec := ServerSpec{Name: "search", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer secret"}}
	if _, err := client.Call(context.Background(), spec, "tools/list", nil); err != nil {
		t.Fatalf("call failed: %v", err)
	}
}

func TestClientCall_ReturnsRPCError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		var envelope requestEnvelope
		_ = json.Unmarshal(body, &envelope)
		if envelope.Method == "initialize" {
			return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		}
		if envelope.Method == "notifications/initialized" {
			return jsonResponse(http.StatusAccepted, "")
		}
		return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","id":"mcp-1","error":{"code":-32601,"message":"method not found"}}`)
	})}

	client := NewClient(Config{}, httpClient)
	response, err := client.Call(context.Background(), HTTPSpec("https://example.com/mcp"), "tools/missing", nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if response.Error == nil {
		t.Fatal("expected rpc error")
	}
	if response.Error.Code != -32601 {
		t.Fatalf("expected code -32601, got %d", response.Error.Code)
	}
}

func TestClientCall_RejectsUnsupportedScheme(t *testing.T) {
	client := NewClient(Config{}, nil)
	_, err := client.Call(context.Background(), HTTPSpec("ftp://example.com/mcp"), "tools/list", nil)
	if err == nil {
		t.Fatal("expected scheme validation error")
	}
	if !strings.Contains(err.Error(), "unsupported mcp url scheme") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientCall_StdioInitializeAndLineProtocol(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	var closed atomic.Bool
	go serveStdioJSONRPC(t, serverRead, serverWrite)

	client := NewClient(Config{}, nil)
	client.DialStdio = func(ctx context.Context, spec ServerSpec) (StdioConn, error) {
		return StdioConn{
			Stdin:  clientWrite,
			Stdout: clientRead,
			Wait: func() error {
				closed.Store(true)
				_ = clientWrite.Close()
				_ = serverWrite.Close()
				return nil
			},
		}, nil
	}

	spec := ServerSpec{Name: "fs", Transport: "stdio", Command: "fake-mcp"}
	response, err := client.Call(context.Background(), spec, "tools/list", nil)
	if err != nil {
		t.Fatalf("stdio call failed: %v", err)
	}
	if !strings.Contains(string(response.Result), `"echo"`) {
		t.Fatalf("expected echo tool, got %s", string(response.Result))
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if !closed.Load() {
		t.Fatal("expected stdio Wait to run on Close")
	}
}

func TestServerSpec_Configured(t *testing.T) {
	if HTTPSpec("").Configured() {
		t.Fatal("empty http spec should not be configured")
	}
	if !(ServerSpec{Command: "npx"}).Configured() {
		t.Fatal("stdio command should be configured")
	}
	if (ServerSpec{Transport: "stdio"}).Configured() {
		t.Fatal("stdio without command should not be configured")
	}
}

func jsonResponse(status int, body string) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func serveStdioJSONRPC(t *testing.T, reader io.Reader, writer io.Writer) {
	t.Helper()
	bufReader := bufio.NewReader(reader)
	var mu sync.Mutex
	writeLine := func(payload string) {
		mu.Lock()
		defer mu.Unlock()
		if _, err := io.WriteString(writer, payload+"\n"); err != nil {
			t.Errorf("write stdio response: %v", err)
		}
	}
	for {
		line, err := bufReader.ReadBytes('\n')
		if err != nil {
			return
		}
		var envelope requestEnvelope
		if err := json.Unmarshal(bytesTrim(line), &envelope); err != nil {
			t.Errorf("decode stdio request: %v", err)
			return
		}
		switch envelope.Method {
		case "initialize":
			writeLine(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`)
		case "notifications/initialized":
			continue
		case "tools/list":
			writeLine(`{"jsonrpc":"2.0","id":"2","result":{"tools":[{"name":"echo"}]}}`)
		default:
			writeLine(`{"jsonrpc":"2.0","id":"x","error":{"code":-32601,"message":"method not found"}}`)
		}
	}
}

func bytesTrim(line []byte) []byte {
	return []byte(strings.TrimSpace(string(line)))
}
