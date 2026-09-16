// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOpenAICompatibleClientGenerate_UsesPresetAndParsesResponse(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "deepseek",
		Model:    "deepseek-chat",
		APIKey:   "secret-token",
		BaseURL:  "https://api.deepseek.com",
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.deepseek.com/chat/completions" {
			t.Fatalf("unexpected request URL %q", request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("unexpected authorization header %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}
		text := string(body)
		if !strings.Contains(text, "deepseek-chat") {
			t.Fatalf("expected request body to include model, got %s", text)
		}
		if !strings.Contains(text, "operator-facing synthesis") {
			t.Fatalf("expected request body to include prompt content, got %s", text)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Live provider synthesis."}}],"usage":{"prompt_tokens":21,"completion_tokens":9,"total_tokens":30}}`)),
		}, nil
	})}

	response, err := client.Generate(context.Background(), Request{
		SystemPrompt: "You are the runtime synthesizer.",
		UserPrompt:   "Write an operator-facing synthesis.",
	})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if response.Fallback {
		t.Fatal("did not expect fallback response")
	}
	if response.Text != "Live provider synthesis." {
		t.Fatalf("unexpected response text %q", response.Text)
	}
	if !response.Usage.Known {
		t.Fatal("expected usage to be marked known")
	}
	if response.Usage.TotalTokens != 30 {
		t.Fatalf("expected total tokens 30, got %d", response.Usage.TotalTokens)
	}
}

func TestOpenAICompatibleClientGenerate_QwenSetsEnableSearch(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:        "qwen",
		Model:           "qwen-plus",
		EnableWebSearch: true,
		APIKey:          "dashscope-token",
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}
		if !strings.Contains(string(body), `"enable_search":true`) {
			t.Fatalf("expected qwen payload to include enable_search, got %s", string(body))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Qwen synthesis."}}]}`)),
		}, nil
	})}

	response, err := client.Generate(context.Background(), Request{UserPrompt: "Summarize the task.", UseWebSearch: true})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if response.Text != "Qwen synthesis." {
		t.Fatalf("unexpected response text %q", response.Text)
	}
}

func TestOpenAICompatibleClientGenerate_OpenRouterUsesWebPlugin(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:        "openrouter",
		EnableWebSearch: true,
		APIKey:          "openrouter-token",
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://openrouter.ai/api/v1/chat/completions" {
			t.Fatalf("unexpected request URL %q", request.URL.String())
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}
		text := string(body)
		if !strings.Contains(text, `"model":"openai/gpt-4.1-mini"`) {
			t.Fatalf("expected default model in request body, got %s", text)
		}
		if !strings.Contains(text, `"plugins":[{"id":"web"}]`) {
			t.Fatalf("expected openrouter payload to include web plugin, got %s", text)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"OpenRouter synthesis."}}],"usage":{"prompt_tokens":15,"completion_tokens":5,"total_tokens":20}}`)),
		}, nil
	})}

	response, err := client.Generate(context.Background(), Request{UserPrompt: "Search recent context.", UseWebSearch: true})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if response.Text != "OpenRouter synthesis." {
		t.Fatalf("unexpected response text %q", response.Text)
	}
	if !response.Usage.Known || response.Usage.TotalTokens != 20 {
		t.Fatalf("expected known usage totals, got %+v", response.Usage)
	}
}

func TestOpenAICompatibleClientGenerate_ErrorsWithoutCredential(t *testing.T) {
	// P13-5: missing credentials should return an error, not placeholder text.
	t.Setenv("DEEPSEEK_API_KEY", "")

	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{Provider: "deepseek", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	_, err = client.Generate(context.Background(), Request{UserPrompt: "Keep the run deterministic."})
	if err == nil {
		t.Fatal("expected error for missing credential, got nil")
	}
	if !strings.Contains(err.Error(), "no API key configured") {
		t.Fatalf("expected API key error, got: %v", err)
	}
}

func TestDeepseekRequiresExplicitModel_NoHardcodedPro(t *testing.T) {
	// Z1: deepseek model must come from agent.yaml, not a hardcoded DefaultModel.
	preset, ok := openAICompatiblePresets["deepseek"]
	if !ok {
		t.Fatal("missing deepseek preset")
	}
	if preset.DefaultModel != "" {
		t.Fatalf("deepseek DefaultModel must be empty (got %q) — use agent.yaml only", preset.DefaultModel)
	}
	if !preset.RequiresExplicitModel {
		t.Fatal("deepseek must RequireExplicitModel so config cannot silently fall back to pro")
	}
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{Provider: "deepseek"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.resolveRequestConfig()
	if err == nil {
		t.Fatal("expected error when model unset")
	}
	if !strings.Contains(err.Error(), "model must be set explicitly") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestZ8_DeepseekToolLoopReplaysReasoningContent(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:  "deepseek",
		Model:     "deepseek-v4-flash",
		APIKey:    "secret-token",
		ThinkMode: ThinkModeAuto,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	call := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call++
		body, _ := io.ReadAll(request.Body)
		text := string(body)
		switch call {
		case 1:
			if !strings.Contains(text, `"tools"`) {
				t.Fatalf("turn1 request missing tools: %s", text)
			}
			// No reasoning_content in response — client must still pin "" on replay.
			payload := `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"no-such-file.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
		case 2:
			if !strings.Contains(text, `"reasoning_content"`) {
				t.Fatalf("turn2 must replay reasoning_content, body=%s", text)
			}
			if !strings.Contains(text, `"tools"`) {
				t.Fatalf("turn2 must still carry tools, body=%s", text)
			}
			payload := `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
		default:
			t.Fatalf("unexpected call %d", call)
			return nil, nil
		}
	})}

	resp, err := client.Generate(context.Background(), Request{
		UserPrompt: "implement",
		Tools: []ToolDefinition{{
			Name:        "read_file",
			Description: "r",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
		MaxToolTurns: 4,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("text=%q", resp.Text)
	}
	if call != 2 {
		t.Fatalf("expected 2 HTTP calls, got %d", call)
	}
}

func TestZ8_ThinkModeOffSendsDisabled(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:  "deepseek",
		Model:     "deepseek-v4-flash",
		APIKey:    "secret-token",
		ThinkMode: ThinkModeOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		text := string(body)
		if !strings.Contains(text, `"thinking"`) || !strings.Contains(text, `"disabled"`) {
			t.Fatalf("expected thinking disabled, got %s", text)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":1}}`)),
		}, nil
	})}
	if _, err := client.Generate(context.Background(), Request{UserPrompt: "hi"}); err != nil {
		t.Fatal(err)
	}
}

func TestGLM53FlashThinkModeOffSendsLowEffort(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:  "glm",
		Model:     "GLM-5.3-Flash",
		APIKey:    "secret-token",
		ThinkMode: ThinkModeOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		text := string(body)
		if strings.Contains(text, `"disabled"`) {
			t.Fatalf("glm-5.3-flash rejects thinking disabled, body=%s", text)
		}
		if !strings.Contains(text, `"thinking"`) || !strings.Contains(text, `"enabled"`) {
			t.Fatalf("expected thinking enabled, got %s", text)
		}
		if !strings.Contains(text, `"reasoning_effort"`) || !strings.Contains(text, `"low"`) {
			t.Fatalf("expected reasoning_effort=low, got %s", text)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":1}}`)),
		}, nil
	})}
	if _, err := client.Generate(context.Background(), Request{UserPrompt: "hi", ThinkMode: ThinkModeOff}); err != nil {
		t.Fatal(err)
	}
}

func TestZ10_ThinkingCallbackReceivesReasoningDeltas(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:  "deepseek",
		Model:     "deepseek-v4-flash",
		APIKey:    "secret-token",
		ThinkMode: ThinkModeAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		sse := "" +
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"step1\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"step2\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(sse))}, nil
	})}
	var thinking, content strings.Builder
	resp, err := client.Generate(context.Background(), Request{
		UserPrompt:       "hi",
		StreamCallback:   func(c string) { content.WriteString(c) },
		ThinkingCallback: func(c string) { thinking.WriteString(c) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if thinking.String() != "step1step2" {
		t.Fatalf("thinking=%q", thinking.String())
	}
	if content.String() != "answer" {
		t.Fatalf("content=%q", content.String())
	}
	if resp.Text != "answer" {
		t.Fatalf("text=%q", resp.Text)
	}
	if resp.Reasoning != "step1step2" {
		t.Fatalf("reasoning=%q", resp.Reasoning)
	}
}

func TestStreamCallback_ReceivesToolArgumentDeltas(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		APIKey:   "secret-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			sse := "" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\\\"no-such.txt\\\"}\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n"
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(sse))}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"total_tokens":1}}`)),
		}, nil
	})}
	var got strings.Builder
	_, err = client.Generate(context.Background(), Request{
		UserPrompt:     "hi",
		StreamCallback: func(c string) { got.WriteString(c) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "read_file") && !strings.Contains(got.String(), "no-such.txt") {
		t.Fatalf("tool-call stream must reach StreamCallback, got %q", got.String())
	}
	if !strings.Contains(got.String(), "done") {
		t.Fatalf("non-stream follow-up text after first tool turn must reach StreamCallback, got %q", got.String())
	}
}

func TestStreamCallback_ReceivesNonStreamToolArguments(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		APIKey:   "secret-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			sse := "" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\\\"no-such.txt\\\"}\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n"
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(sse))}, nil
		}
		if calls == 2 {
			body := `{"choices":[{"message":{"tool_calls":[{"id":"c2","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"bloom.go\",\"content\":\"package bloomx\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"total_tokens":8}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"wrote bloom.go"},"finish_reason":"stop"}],"usage":{"total_tokens":1}}`)),
		}, nil
	})}
	var got strings.Builder
	_, err = client.Generate(context.Background(), Request{
		UserPrompt:     "hi",
		MaxToolTurns:   5,
		StreamCallback: func(c string) { got.WriteString(c) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "bloom.go") || !strings.Contains(got.String(), "package bloomx") {
		t.Fatalf("non-stream tool arguments must reach StreamCallback, got %q", got.String())
	}
}

func TestGenerateStream_AppliesStructuredOutputResponseFormat(t *testing.T) {
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		APIKey:   "secret-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	var body string
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		raw, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("read body: %v", readErr)
		}
		body = string(raw)
		sse := "" +
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"classify as run\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"action\\\":\\\"safe_run\\\"}\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(sse))}, nil
	})}
	resp, err := client.Generate(context.Background(), Request{
		UserPrompt:       "implement a queue",
		StructuredOutput: true,
		ThinkMode:        ThinkModeOn,
		ThinkingCallback: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"response_format"`) || !strings.Contains(body, `"json_object"`) {
		t.Fatalf("stream structured output must set response_format json_object, body=%s", body)
	}
	if !strings.Contains(body, `"thinking"`) || !strings.Contains(body, `"enabled"`) {
		t.Fatalf("intent-style ThinkModeOn must enable thinking, body=%s", body)
	}
	if resp.Reasoning != "classify as run" {
		t.Fatalf("reasoning=%q", resp.Reasoning)
	}
	if resp.Text != `{"action":"safe_run"}` {
		t.Fatalf("text=%q", resp.Text)
	}
}

func TestAgnesPreset_ChatCompletionsEndpoint(t *testing.T) {
	info, ok := LookupProvider("agnes-ai")
	if !ok || info.Name != "agnes" {
		t.Fatalf("agnes-ai alias should resolve, got %+v ok=%v", info, ok)
	}
	if info.DefaultModel != "agnes-2.5-flash" || info.DefaultBaseURL != "https://api.agnes-ai.cn/v1" || info.DefaultAPIKeyEnv != "AGNES_API_KEY" {
		t.Fatalf("unexpected agnes catalog: %+v", info)
	}

	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "agnes",
		APIKey:   "secret-token",
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.agnes-ai.cn/v1/chat/completions" {
			t.Fatalf("unexpected request URL %q", request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("unexpected authorization header %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body failed: %v", err)
		}
		if !strings.Contains(string(body), `"model":"agnes-2.5-flash"`) {
			t.Fatalf("expected default model in body, got %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_xxx","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)),
		}, nil
	})}
	resp, err := client.Generate(context.Background(), Request{UserPrompt: "ping"})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text=%q", resp.Text)
	}
}

func TestAgnesPreset_PreservesRegionalHosts(t *testing.T) {
	if got := canonicalizeCompatibleBaseURL("agnes", "https://api.agnes-ai.cn/v1"); got != "https://api.agnes-ai.cn/v1" {
		t.Fatalf("China service host must stay: %q", got)
	}
	if got := canonicalizeCompatibleBaseURL("agnes", "https://apihub.agnes-ai.com/v1"); got != "https://apihub.agnes-ai.com/v1" {
		t.Fatalf("international host must stay: %q", got)
	}
	if got := canonicalizeCompatibleBaseURL("agnes", "https://apihub.agnes-ai.cn/v1"); got != "https://apihub.agnes-ai.cn/v1" {
		t.Fatalf("intl alternate host must stay: %q", got)
	}
}

func TestAgnesPreset_AGNESBaseURLEnvOverrides(t *testing.T) {
	t.Setenv("AGNES_BASE_URL", "https://apihub.agnes-ai.com/v1")
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider: "agnes",
		APIKey:   "secret-token",
		BaseURL:  "https://api.agnes-ai.cn/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := client.resolveRequestConfig()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.endpoint != "https://apihub.agnes-ai.com/v1/chat/completions" {
		t.Fatalf("AGNES_BASE_URL should win, got %q", resolved.endpoint)
	}
}

func TestLookupCompatibleAPIKey_FallsBackToExtraEnv(t *testing.T) {
	t.Setenv("AGNES_API_KEY", "")
	t.Setenv("AGNES_CN_API_KEY", "cn-secret")
	got := lookupCompatibleAPIKey("", "AGNES_API_KEY", "AGNES_CN_API_KEY")
	if got != "cn-secret" {
		t.Fatalf("expected extra Agnes env, got %q", got)
	}
	got = lookupCompatibleAPIKey("explicit-key", "AGNES_API_KEY", "AGNES_CN_API_KEY")
	if got != "explicit-key" {
		t.Fatalf("explicit key must win, got %q", got)
	}
}
