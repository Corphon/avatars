package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	genkitai "github.com/firebase/genkit/go/ai"
)

func TestS5_ApplyToolsToOpenAIRequest(t *testing.T) {
	req := &openAIRequest{}
	applyToolsToOpenAIRequest(req, Request{Tools: StandardCodeTools(), ToolChoice: "required"}, 0)
	if len(req.Tools) == 0 {
		t.Fatal("expected tools on turn 0")
	}
	if req.ToolChoice != "required" {
		t.Fatalf("tool_choice=%v, want required", req.ToolChoice)
	}
	// Z8: tools must also attach on turn > 0 so multi-step write_file can continue.
	req2 := &openAIRequest{}
	applyToolsToOpenAIRequest(req2, Request{Tools: StandardCodeTools()}, 1)
	if len(req2.Tools) == 0 {
		t.Fatal("expected tools on turn > 0 (multi-turn tool loop)")
	}
}

func TestS5_GenerateStream_FirstPacketIncludesTools(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "deepseek",
		Model:    "deepseek-chat",
		APIKey:   "secret-token",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var sawTools, sawToolChoice bool
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		text := string(body)
		sawTools = strings.Contains(text, `"tools"`)
		sawToolChoice = strings.Contains(text, `"tool_choice"`)
		// Minimal SSE stream with no tool calls.
		sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	})}

	_, err = client.Generate(context.Background(), Request{
		UserPrompt:     "edit a file",
		Tools:          StandardCodeTools(),
		StreamCallback: func(string) {},
		Category:       CategoryCodeGeneration,
	})
	if err != nil {
		t.Fatalf("generateStream: %v", err)
	}
	if !sawTools || !sawToolChoice {
		t.Fatalf("stream first packet missing tools=%v tool_choice=%v", sawTools, sawToolChoice)
	}
}

func TestS5_NormalizeFinishReason_WiredInToolLoop(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "deepseek",
		Model:    "deepseek-chat",
		APIKey:   "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"finish_reason":"max_tokens","message":{"content":"partial"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
			)),
		}, nil
	})}
	resp, err := client.Generate(context.Background(), Request{UserPrompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != FinishReasonLength {
		t.Fatalf("FinishReason=%q, want %q", resp.FinishReason, FinishReasonLength)
	}
	if !IsTruncated(resp) {
		t.Fatal("expected IsTruncated after normalization")
	}
}

func TestS5_ExtractGenkitToolCalls(t *testing.T) {
	resp := &genkitai.ModelResponse{
		Request: &genkitai.ModelRequest{
			Messages: []*genkitai.Message{
				{
					Role: genkitai.RoleModel,
					Content: []*genkitai.Part{
						genkitai.NewToolRequestPart(&genkitai.ToolRequest{
							Name:  "write_file",
							Ref:   "call_1",
							Input: map[string]any{"path": "a.go", "content": "package a"},
						}),
					},
				},
			},
		},
		Message: &genkitai.Message{
			Role:    genkitai.RoleModel,
			Content: []*genkitai.Part{genkitai.NewTextPart("done")},
		},
	}
	calls := extractGenkitToolCalls(resp)
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	if calls[0].Name != "write_file" {
		t.Fatalf("name=%q", calls[0].Name)
	}
	if calls[0].Arguments["path"] != "a.go" {
		t.Fatalf("args=%v", calls[0].Arguments)
	}
}

func TestS5_FallbackProviders_SwitchChain(t *testing.T) {
	primary := &stubClient{provider: "primary", err: context.DeadlineExceeded}
	fallback := &stubClient{provider: "qwen", text: "from-fallback"}
	client := NewRetryClientWithFallbacks(primary, []Client{fallback})
	resp, err := client.Generate(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("expected fallback success, got %v", err)
	}
	if resp.Text != "from-fallback" {
		t.Fatalf("text=%q", resp.Text)
	}
	if !resp.Fallback {
		t.Fatal("expected Fallback=true on switched response")
	}
	if primary.calls < 1 || fallback.calls != 1 {
		t.Fatalf("primary.calls=%d fallback.calls=%d", primary.calls, fallback.calls)
	}
}

func TestS5_ResolveFallbackEntry(t *testing.T) {
	p, m := resolveFallbackEntry("qwen")
	if p != "qwen" || m != "" {
		t.Fatalf("qwen → %q %q", p, m)
	}
	p, m = resolveFallbackEntry("deepseek-chat")
	if p != "deepseek" || m != "deepseek-chat" {
		t.Fatalf("deepseek-chat → %q %q", p, m)
	}
	p, m = resolveFallbackEntry("agnes-2.5-flash")
	if p != "agnes" || m != "agnes-2.5-flash" {
		t.Fatalf("agnes-2.5-flash → %q %q", p, m)
	}
}

func TestS5_PreciseEdit_SharedSemantics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	if err := os.WriteFile(path, []byte("package p\n\nfunc A() {}\nfunc B() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	msg, err := runPreciseEdit(path, "func A() {}", "", "func A() { return }", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "precise_edit") {
		t.Fatalf("msg=%s", msg)
	}
	// Genkit path must use same helper (position after).
	msg, err = executeToolByName(context.Background(), "precise_edit", map[string]any{
		"path": path, "anchor": "func B() {}", "new_block": "\n// note\n", "position": "after",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "after anchor") {
		t.Fatalf("msg=%s", msg)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "// note") {
		t.Fatalf("file=%s", data)
	}
}

func TestS5_GitDiff_Wired(t *testing.T) {
	_, err := executeToolByName(context.Background(), "git_diff", map[string]any{})
	if err != nil {
		t.Fatalf("git_diff should not error: %v", err)
	}
}

func TestS5_ShellDone_UsesTimeoutConfig(t *testing.T) {
	prev := GlobalTimeoutConfig
	cfg := DefaultTimeoutConfig()
	cfg.ShellDone = 200 * time.Millisecond
	GlobalTimeoutConfig = &cfg
	t.Cleanup(func() { GlobalTimeoutConfig = prev })

	if got := TimeoutConfigOrDefault().ShellDone; got != 200*time.Millisecond {
		t.Fatalf("ShellDone config not wired: got %v", got)
	}
	// Config path is the product contract (M-15). Process-kill timing on Windows
	// cmd children is OS-dependent; do not assert wall-clock kill here.
}

func TestS5_PromptCacheKey_IncludesToolsModelCategory(t *testing.T) {
	a := promptCacheKey(Request{SystemPrompt: "s", UserPrompt: "u", Category: CategoryAnalysis, Model: "m1"})
	b := promptCacheKey(Request{SystemPrompt: "s", UserPrompt: "u", Category: CategoryAnalysis, Model: "m2"})
	if a == b {
		t.Fatal("model must affect cache key")
	}
	c := promptCacheKey(Request{
		SystemPrompt: "s", UserPrompt: "u", Category: CategoryAnalysis, Model: "m1",
		Tools: []ToolDefinition{{Name: "read_file"}},
	})
	if a == c {
		t.Fatal("tools must affect cache key")
	}
}

func TestS5_DefaultMaxTokensForCode(t *testing.T) {
	prev := GlobalDefaultMaxTokens
	t.Cleanup(func() { GlobalDefaultMaxTokens = prev })
	GlobalDefaultMaxTokens = 0
	if DefaultMaxTokensForCode() != DefaultBuilderBudget {
		t.Fatalf("got %d", DefaultMaxTokensForCode())
	}
	GlobalDefaultMaxTokens = 1000
	if DefaultMaxTokensForCode() != 1000 {
		t.Fatalf("got %d", DefaultMaxTokensForCode())
	}
	if resolveMaxTokens(Request{Category: CategoryCodeGeneration}) != 1000 {
		t.Fatal("resolveMaxTokens should use global")
	}
	if resolveMaxTokens(Request{MaxTokens: 42, Category: CategoryCodeGeneration}) != 42 {
		t.Fatal("explicit MaxTokens wins")
	}
	GlobalDefaultMaxTokens = 262144
	if DefaultMaxTokensForCode() != HardMaxOutputTokens {
		t.Fatalf("stale huge yaml must clamp to %d, got %d", HardMaxOutputTokens, DefaultMaxTokensForCode())
	}
}

func TestClampMaxTokensForProvider_AgnesCap(t *testing.T) {
	if got := clampMaxTokensForProvider(65536, 262144); got != 65536 {
		t.Fatalf("agnes cap: got %d", got)
	}
	if got := clampMaxTokensForProvider(65536, 1024); got != 1024 {
		t.Fatalf("under cap must stay: got %d", got)
	}
	if got := clampMaxTokensForProvider(0, 262144); got != 262144 {
		t.Fatalf("unknown provider must not clamp: got %d", got)
	}
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{Provider: "agnes", APIKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	prev := GlobalDefaultMaxTokens
	t.Cleanup(func() { GlobalDefaultMaxTokens = prev })
	GlobalDefaultMaxTokens = 262144
	if got := client.cappedMaxTokens(Request{Category: CategoryCodeGeneration}); got != 65536 {
		t.Fatalf("agnes code-gen cap: got %d", got)
	}
	ds, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ds.cappedMaxTokens(Request{Category: CategoryCodeGeneration}); got != HardMaxOutputTokens {
		t.Fatalf("deepseek code-gen must not send the model 384K ceiling, got %d", got)
	}
}

func TestS5_ToolRequestInputToArgs_Struct(t *testing.T) {
	type in struct {
		Path string `json:"path"`
	}
	args := toolRequestInputToArgs(in{Path: "x.go"})
	raw, _ := json.Marshal(args)
	if !strings.Contains(string(raw), "x.go") {
		t.Fatalf("args=%v", args)
	}
}

type stubClient struct {
	provider string
	text     string
	err      error
	fallback bool
	calls    int
}

func (s *stubClient) Provider() string         { return s.provider }
func (s *stubClient) Supports(Capability) bool { return false }
func (s *stubClient) Generate(context.Context, Request) (Response, error) {
	s.calls++
	if s.err != nil {
		return Response{}, s.err
	}
	return Response{Text: s.text, Provider: s.provider, Fallback: s.fallback}, nil
}

func TestC62_EmptyFallbackProviders_PrimaryFailureIsExplicit(t *testing.T) {
	prev := append([]string(nil), GlobalFallbackProviders...)
	t.Cleanup(func() { SetFallbackProviders(prev) })
	SetFallbackProviders(nil)

	primary := &stubClient{provider: "primary", err: errors.New("401 unauthorized")}
	client := NewRetryClientWithFallbacks(primary, nil)
	_, err := client.Generate(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected primary failure")
	}
	if !IsPrimaryFailureWithoutFallbacks(err) {
		t.Fatalf("want annotated empty-fallback error, got %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("must keep primary error: %v", err)
	}
	if !strings.Contains(err.Error(), "fallback_providers is empty") {
		t.Fatalf("CLI/grep must see empty fallback_providers: %v", err)
	}
}

func TestC62_FallbackReturned_NoProvidersConfigured(t *testing.T) {
	prev := append([]string(nil), GlobalFallbackProviders...)
	t.Cleanup(func() { SetFallbackProviders(prev) })
	SetFallbackProviders(nil)

	primary := &stubClient{provider: "primary", text: "placeholder", fallback: true}
	client := NewRetryClientWithFallbacks(primary, nil)
	resp, err := client.Generate(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatalf("S5.4 must not return placeholder text, got %q", resp.Text)
	}
	if !IsPrimaryFailureWithoutFallbacks(err) {
		t.Fatalf("want no-fallback primary failure, got %v", err)
	}
}
