package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type openAICompatibleWebSearchMode string

const (
	webSearchModeNone         openAICompatibleWebSearchMode = "none"
	webSearchModeEnableSearch openAICompatibleWebSearchMode = "enable_search"
	webSearchModePluginsWeb   openAICompatibleWebSearchMode = "plugins_web"
)

type openAICompatiblePreset struct {
	Provider                string
	BaseURL                 string
	APIKeyEnv               string
	ExtraAPIKeyEnvs         []string
	DefaultModel            string
	MaxOutputTokens         int // 0 = no provider cap; clamp request max_tokens otherwise
	RequiresExplicitBaseURL bool
	RequiresExplicitModel   bool
	Local                   bool
	WebSearchMode           openAICompatibleWebSearchMode
}

var openAICompatiblePresets = map[string]openAICompatiblePreset{
	"agnes": {
		// Official routes (do not rewrite one into another — keys are region-bound):
		//   China:         https://api.agnes-ai.cn/v1
		//   International: https://apihub.agnes-ai.com/v1
		//   Intl alternate: https://apihub.agnes-ai.cn/v1
		// Override with llm.base_url or AGNES_BASE_URL.
		Provider:        "agnes",
		BaseURL:         "https://api.agnes-ai.cn/v1",
		APIKeyEnv:       "AGNES_API_KEY",
		ExtraAPIKeyEnvs: []string{"AGNES_CN_API_KEY"},
		DefaultModel:    "agnes-2.5-flash",
		MaxOutputTokens: 65536, // Agnes rejects max_tokens above this (agnes-2.5-flash).
	},
	"glm": {
		Provider:     "glm",
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
		APIKeyEnv:    "ZHIPUAI_API_KEY",
		DefaultModel: "glm-5.2",
	},
	"kimi": {
		Provider:     "kimi",
		BaseURL:      "https://api.moonshot.cn/v1",
		APIKeyEnv:    "MOONSHOT_API_KEY",
		DefaultModel: "kimi-k2.7",
	},
	"qwen": {
		Provider:      "qwen",
		BaseURL:       "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv:     "DASHSCOPE_API_KEY",
		DefaultModel:  "qwen3.8-max-preview",
		WebSearchMode: webSearchModeEnableSearch,
	},
	"openrouter": {
		Provider:      "openrouter",
		BaseURL:       "https://openrouter.ai/api/v1",
		APIKeyEnv:     "OPENROUTER_API_KEY",
		DefaultModel:  "openai/gpt-4.1-mini",
		WebSearchMode: webSearchModePluginsWeb,
	},
	"doubao": {
		Provider:              "doubao",
		BaseURL:               "https://ark.cn-beijing.volces.com/api/v3",
		APIKeyEnv:             "ARK_API_KEY",
		RequiresExplicitModel: true,
	},
	"nvidia": {
		Provider:     "nvidia",
		BaseURL:      "https://integrate.api.nvidia.com/v1",
		APIKeyEnv:    "NVIDIA_API_KEY",
		DefaultModel: "nvidia/nemotron-3-super-120b-a12b",
	},
	"mistral": {
		Provider:     "mistral",
		BaseURL:      "https://api.mistral.ai/v1",
		APIKeyEnv:    "MISTRAL_API_KEY",
		DefaultModel: "mistral-large-latest",
	},
	"minimax": {
		Provider:              "minimax",
		BaseURL:               "https://api.minimaxi.com/v1",
		APIKeyEnv:             "MINIMAX_API_KEY",
		RequiresExplicitModel: true,
	},
	"mimo": {
		Provider:              "mimo",
		BaseURL:               "https://api.xiaomimimo.com/v1",
		APIKeyEnv:             "MIMO_API_KEY",
		RequiresExplicitModel: true,
	},
	"deepseek": {
		Provider:              "deepseek",
		BaseURL:               "https://api.deepseek.com",
		APIKeyEnv:             "DEEPSEEK_API_KEY",
		RequiresExplicitModel: true,
		// V4 lists a 384K output ceiling; the harness must not send that as
		// the everyday request or a missed yaml default streams until timeout.
		MaxOutputTokens: HardMaxOutputTokens,
	},
	"ollama": {
		Provider:     "ollama",
		BaseURL:      "http://localhost:11434/v1",
		DefaultModel: "llama3.2",
		Local:        true,
	},
	"lm-studio": {
		Provider:              "lm-studio",
		BaseURL:               "http://localhost:1234/v1",
		RequiresExplicitModel: true,
		Local:                 true,
	},
}

type OpenAICompatibleConfig struct {
	Provider        string
	Model           string
	EnableWebSearch bool
	ThinkMode       ThinkMode
	APIKey          string
	APIKeyEnv       string
	BaseURL         string
	Timeout         time.Duration
}

type OpenAICompatibleClient struct {
	config     OpenAICompatibleConfig
	preset     openAICompatiblePreset
	httpClient *http.Client
}

func NewOpenAICompatibleClient(config OpenAICompatibleConfig) (*OpenAICompatibleClient, error) {
	provider := normalizeProviderName(config.Provider)
	preset, ok := openAICompatiblePresets[provider]
	if !ok && provider != "openai-compatible" && provider != "custom-compatible" && provider != "custom" {
		return nil, fmt.Errorf("unsupported openai-compatible provider %q", config.Provider)
	}
	if provider == "openai-compatible" || provider == "custom-compatible" || provider == "custom" {
		preset = openAICompatiblePreset{Provider: provider, RequiresExplicitBaseURL: true, RequiresExplicitModel: true}
	}
	client := &OpenAICompatibleClient{
		config: OpenAICompatibleConfig{
			Provider:        provider,
			Model:           strings.TrimSpace(config.Model),
			EnableWebSearch: config.EnableWebSearch,
			ThinkMode:       normalizeThinkMode(config.ThinkMode),
			APIKey:          strings.TrimSpace(config.APIKey),
			APIKeyEnv:       strings.TrimSpace(config.APIKeyEnv),
			BaseURL:         strings.TrimSpace(config.BaseURL),
			Timeout:         defaultTimeout(config.Timeout),
		},
		preset: preset,
	}
	client.httpClient = &http.Client{Timeout: client.config.Timeout}
	return client, nil
}

func (c *OpenAICompatibleClient) Provider() string {
	return c.preset.Provider
}

func (c *OpenAICompatibleClient) Supports(capability Capability) bool {
	if capability != CapabilityWebSearch || !c.config.EnableWebSearch {
		return false
	}
	return c.preset.WebSearchMode != webSearchModeNone
}

func (c *OpenAICompatibleClient) Generate(ctx context.Context, request Request) (Response, error) {
	resolved, err := c.resolveRequestConfig()
	if err != nil {
		return Response{}, err
	}
	if resolved.fallback {
		return Response{}, fmt.Errorf("%s: LLM unavailable — no API key configured (provider=%s, preset=%s)",
			c.Provider(), c.Provider(), c.preset.Provider)
	}

	// P1-7b: Per-request model override takes precedence over client config.
	model := resolved.model
	if override := strings.TrimSpace(request.Model); override != "" {
		model = override
	}

	// BUG-18.4: Support per-request timeout override. Streaming generation
	// (e.g. large HTML files) may need much longer than the default.
	httpClient := c.httpClient
	if request.RequestTimeout > 0 {
		httpClient = &http.Client{Timeout: request.RequestTimeout}
	}

	// Build initial messages (system + user).
	messages := buildOpenAIMessages(request)

	// SP-Fix-1 + RC-1 / Z10: stream when content or thinking callbacks are set
	// so Builder can surface CoT progress while still handling tool calls.
	if request.StreamCallback != nil || request.ThinkingCallback != nil {
		return c.generateStream(ctx, resolved, model, request, messages, httpClient)
	}

	// Non-streaming path: delegate to shared tool-calling loop.
	return c.runNonStreamingToolLoop(ctx, resolved, model, request, messages, 0, nil, openAICompatibleUsage{}, httpClient)
}

type resolvedOpenAICompatibleRequest struct {
	endpoint string
	apiKey   string
	model    string
	fallback bool
}

func (c *OpenAICompatibleClient) resolveRequestConfig() (resolvedOpenAICompatibleRequest, error) {
	baseURL := strings.TrimSpace(c.config.BaseURL)
	if envURL := compatibleBaseURLFromEnv(c.Provider()); envURL != "" {
		baseURL = envURL
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(c.preset.BaseURL)
	}
	baseURL = canonicalizeCompatibleBaseURL(c.Provider(), baseURL)
	model := strings.TrimSpace(c.config.Model)
	if model == "" {
		model = strings.TrimSpace(c.preset.DefaultModel)
	}
	envNames := []string{c.config.APIKeyEnv, c.preset.APIKeyEnv}
	envNames = append(envNames, c.preset.ExtraAPIKeyEnvs...)
	apiKey := lookupCompatibleAPIKey(c.config.APIKey, envNames...)

	if baseURL == "" && c.preset.RequiresExplicitBaseURL {
		return resolvedOpenAICompatibleRequest{}, missingConfigurationError(c.Provider(), "base_url override is required for this preset")
	}
	if model == "" && c.preset.RequiresExplicitModel {
		return resolvedOpenAICompatibleRequest{}, missingConfigurationError(c.Provider(), "model must be set explicitly for this preset")
	}
	if !c.preset.Local && apiKey == "" {
		return resolvedOpenAICompatibleRequest{model: model, fallback: true}, nil
	}

	return resolvedOpenAICompatibleRequest{
		endpoint: resolveChatCompletionsEndpoint(baseURL),
		apiKey:   apiKey,
		model:    model,
	}, nil
}

func lookupCompatibleAPIKey(explicit string, envNames ...string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	seen := map[string]bool{}
	for _, envName := range envNames {
		name := strings.TrimSpace(envName)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func compatibleBaseURLFromEnv(provider string) string {
	if strings.ToLower(strings.TrimSpace(provider)) != "agnes" {
		return ""
	}
	return strings.TrimSpace(os.Getenv("AGNES_BASE_URL"))
}

func canonicalizeCompatibleBaseURL(provider, baseURL string) string {
	_ = provider
	return baseURL
}

func resolveChatCompletionsEndpoint(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(trimmed, "/chat/completions") {
		return trimmed
	}
	return trimmed + "/chat/completions"
}

// openAIMessage is a typed message struct for OpenAI-compatible APIs.
// Using a struct (not map[string]any) ensures json.Marshal preserves
// field order: role before content. DeepSeek's API requires this ordering
// for native tool_calls to function correctly.
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"` // required by API even when empty
	// ReasoningContent is a pointer so "" is still serialized. A plain
	// string with omitempty drops empty values and DeepSeek V4 thinking
	// mode then returns 400 on the next tool-call turn (Z8).
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	Name             string           `json:"name,omitempty"`
}

// openAIRequest is the typed request payload for OpenAI-compatible APIs.
// Field order matters: model first, then messages, then tools.
type openAIRequest struct {
	Model          string           `json:"model"`
	Messages       []openAIMessage  `json:"messages"`
	Tools          []openAIToolDef  `json:"tools,omitempty"`
	ToolChoice     string           `json:"tool_choice,omitempty"`
	Stream         bool             `json:"stream"`
	MaxTokens      int              `json:"max_tokens,omitempty"`
	Temperature    float64          `json:"temperature"`
	ResponseFormat *json.RawMessage `json:"response_format,omitempty"`
	// Provider-specific fields (not in standard OpenAI spec).
	EnableSearch    bool                `json:"enable_search,omitempty"`
	Plugins         []map[string]string `json:"plugins,omitempty"`
	Thinking        *openAIThinking     `json:"thinking,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
}

type openAIToolDef struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// buildOpenAIMessages builds the initial message list from a Request.
func buildOpenAIMessages(request Request) []openAIMessage {
	messages := make([]openAIMessage, 0, 2)
	if systemPrompt := strings.TrimSpace(request.SystemPrompt); systemPrompt != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: systemPrompt})
	}
	if userPrompt := strings.TrimSpace(request.UserPrompt); userPrompt != "" {
		messages = append(messages, openAIMessage{Role: "user", Content: userPrompt})
	}
	return messages
}

// buildOpenAIToolDefs converts avatars ToolDefinitions to OpenAI function tool format
// using typed structs for correct JSON field ordering.
func buildOpenAIToolDefs(toolDefs []ToolDefinition) []openAIToolDef {
	tools := make([]openAIToolDef, 0, len(toolDefs))
	for _, td := range toolDefs {
		tools = append(tools, openAIToolDef{
			Type: "function",
			Function: openAIToolFunction{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
		})
	}
	return tools
}

// executeOpenAIToolCall executes a single tool call from the LLM and returns
// the tool result content string.
func executeOpenAIToolCall(ctx context.Context, tc openAIToolCall) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("error: failed to parse arguments: %v", err)
	}
	result, err := executeToolByName(ctx, tc.Function.Name, args)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return result
}

// executeToolByName dispatches a tool call by name with decoded arguments.
// This is the shared execution path for both genkit (via tool_bridge.go) and
// OpenAI-compatible (via executeOpenAIToolCall).
func executeToolByName(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		if path == "" {
			return "", fmt.Errorf("read_file: path is required")
		}
		cleanPath := filepath.Clean(path)
		info, err := os.Stat(cleanPath)
		if err != nil {
			return "", fmt.Errorf("read_file: %w", err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("read_file: path is a directory: %s", cleanPath)
		}
		content, err := os.ReadFile(cleanPath)
		if err != nil {
			return "", fmt.Errorf("read_file: %w", err)
		}
		return string(content), nil

	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if path == "" {
			return "", fmt.Errorf("write_file: path is required")
		}
		cleanPath, err := ConfineToolPathWithContent(path, content)
		if err != nil {
			return "", fmt.Errorf("write_file: %w", err)
		}
		content, err = prepareWriteContent(cleanPath, content)
		if err != nil {
			return "", err
		}
		dir := filepath.Dir(cleanPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("write_file: create directory: %w", err)
		}
		tmpPath := cleanPath + ".tmp"
		if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("write_file: write temp: %w", err)
		}
		// Windows: rename fails if destination exists — remove first (same as tools.WriteTool).
		_ = os.Remove(cleanPath)
		if err := os.Rename(tmpPath, cleanPath); err != nil {
			os.Remove(tmpPath)
			return "", fmt.Errorf("write_file: rename: %w", err)
		}
		NotifyFileMutation(cleanPath)
		return FormatWriteResult(path, cleanPath, len(content)), nil

	case "edit_file":
		path, _ := args["path"].(string)
		oldStr, _ := args["old_string"].(string)
		newStr, _ := args["new_string"].(string)
		if path == "" {
			return "", fmt.Errorf("edit_file: path is required")
		}
		cleanPath, err := ConfineToolPath(path)
		if err != nil {
			return "", fmt.Errorf("edit_file: %w", err)
		}
		fileContent, err := os.ReadFile(cleanPath)
		if err != nil {
			return "", fmt.Errorf("edit_file: read: %w", err)
		}
		text := string(fileContent)
		count := strings.Count(text, oldStr)
		if count == 0 {
			return "", fmt.Errorf("edit_file: old_string not found in %s — re-read and retry", cleanPath)
		}
		if count > 1 {
			return "", fmt.Errorf("edit_file: old_string appears %d times in %s — provide more context", count, cleanPath)
		}
		replaced := strings.Replace(text, oldStr, newStr, 1)
		replaced, err = prepareWriteContent(cleanPath, replaced)
		if err != nil {
			return "", err
		}
		tmpPath := cleanPath + ".tmp"
		if err := os.WriteFile(tmpPath, []byte(replaced), 0644); err != nil {
			return "", fmt.Errorf("edit_file: write temp: %w", err)
		}
		_ = os.Remove(cleanPath)
		if err := os.Rename(tmpPath, cleanPath); err != nil {
			os.Remove(tmpPath)
			return "", fmt.Errorf("edit_file: rename: %w", err)
		}
		NotifyFileMutation(cleanPath)
		return fmt.Sprintf("Successfully edited %s: replaced old_string (1 occurrence).", cleanPath), nil

	case "precise_edit":
		path, _ := args["path"].(string)
		anchor, _ := args["anchor"].(string)
		oldBlock, _ := args["old_block"].(string)
		newBlock, _ := args["new_block"].(string)
		position, _ := args["position"].(string)
		cleanPath, err := ConfineToolPath(path)
		if err != nil {
			return "", fmt.Errorf("precise_edit: %w", err)
		}
		return runPreciseEdit(cleanPath, anchor, oldBlock, newBlock, position)

	case "shell_done":
		command, _ := args["command"].(string)
		workingDir, _ := args["working_dir"].(string)
		return runShellDone(ctx, command, workingDir)

	case "git_diff":
		path, _ := args["path"].(string)
		return runGitDiff(ctx, path)

	default:
		if IsMCPToolName(name) {
			return invokeMCPToolHandler(ctx, name, args)
		}
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

type openAICompatibleChatCompletionResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          any              `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage openAICompatibleUsage `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAICompatibleUsage struct {
	PromptTokens          int                        `json:"prompt_tokens"`
	CompletionTokens      int                        `json:"completion_tokens"`
	TotalTokens           int                        `json:"total_tokens"`
	PromptCacheHitTokens  int                        `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int                        `json:"prompt_cache_miss_tokens"`
	CacheReadInputTokens  int                        `json:"cache_read_input_tokens"`
	PromptTokensDetails   *openAIPromptTokensDetails `json:"prompt_tokens_details"`
}

type openAIPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func (u openAICompatibleUsage) cachedPromptTokens() int {
	if u.PromptCacheHitTokens > 0 {
		return u.PromptCacheHitTokens
	}
	if u.CacheReadInputTokens > 0 {
		return u.CacheReadInputTokens
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return 0
}

func (u *openAICompatibleUsage) add(other openAICompatibleUsage) {
	if u == nil {
		return
	}
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.PromptCacheHitTokens += other.PromptCacheHitTokens
	u.PromptCacheMissTokens += other.PromptCacheMissTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	cached := other.cachedPromptTokens()
	if other.PromptCacheHitTokens == 0 && cached > 0 {
		u.PromptCacheHitTokens += cached
	}
}

func extractOpenAIContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			partMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := partMap["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

// parseTextToolCalls detects and parses tool calls that models output as text
// markup rather than native tool_calls. This is a compatibility fallback for
// models that don't support the OpenAI function calling protocol natively.
// DeepSeek V4 Pro supports native tool_calls per official docs (api-docs.deepseek.com).
//
// Supported formats:
//   - XML: <函数调用><函数名称>tool</函数名称><参数>{...}</参数></函数调用>
//   - Function call: <function_calls><invoke name="tool"><parameter name="...">...</invoke></function_calls>
//   - Anthropic XML: <function_calls><function name="tool"><parameter name="...">...</function></function_calls>
//
// generateStream handles streaming requests WITH tool support (RC-1).
// Sends stream:true with tools, reads SSE chunks, calls StreamCallback
// for each text delta, accumulates tool_call deltas, and if tool calls
// were received, executes them and continues with non-streaming turns.
func (c *OpenAICompatibleClient) generateStream(ctx context.Context, resolved resolvedOpenAICompatibleRequest, model string, request Request, messages []openAIMessage, httpClient *http.Client) (Response, error) {
	thinkMode := effectiveThinkMode(c.config.ThinkMode, request)
	thinkingOn := thinkingEnabledForRequest(c.Provider(), model, thinkMode)

	reqPayload := openAIRequest{
		Model:       model,
		Messages:    messages,
		Stream:      true,
		Temperature: 0,
	}
	// NF-1 / S5.6: Enforce default max_tokens for code generation when not specified.
	if mt := c.cappedMaxTokens(request); mt > 0 {
		reqPayload.MaxTokens = mt
	}
	// S5.2: First stream packet must include Tools / ToolChoice (same as tool_loop).
	applyToolsToOpenAIRequest(&reqPayload, request, 0)
	applyThinkingToOpenAIRequest(c.Provider(), thinkMode, &reqPayload)
	applyStructuredOutputToOpenAIRequest(&reqPayload, request, 0)

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return Response{}, fmt.Errorf("marshal stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolved.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build stream request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if resolved.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+resolved.apiKey)
	}

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("stream request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(httpResp.Body)
		return Response{}, fmt.Errorf("stream provider %q returned %d: %s", c.Provider(), httpResp.StatusCode, string(body))
	}

	// Parse SSE stream.
	var fullText strings.Builder
	var fullReasoning strings.Builder
	var streamError string
	var lastFinishReason string
	var streamToolCalls []streamToolCallAccumulator // SP-Fix-1
	scanner := newSSEScanner(httpResp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		// Mid-stream error detection: check for error field in SSE chunk.
		if chunk.Error != nil && chunk.Error.Message != "" {
			streamError = chunk.Error.Message
			break
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		// Track finish_reason from each chunk (last non-empty one wins).
		if chunk.Choices[0].FinishReason != "" {
			lastFinishReason = chunk.Choices[0].FinishReason
		}
		// NEW-4: Capture usage from the final stream chunk.
		if chunk.Usage != nil {
			scanner.usage = chunk.Usage
		}
		delta := chunk.Choices[0].Delta.Content
		// Z8: accumulate reasoning_content deltas for tool-loop replay.
		// Z10: also surface to ThinkingCallback (separate from answer StreamCallback).
		if rc := chunk.Choices[0].Delta.ReasoningContent; rc != "" {
			fullReasoning.WriteString(rc)
			if request.ThinkingCallback != nil {
				request.ThinkingCallback(rc)
			}
		}
		// SP-Fix-1: Handle tool_call deltas in streaming mode.
		// Accumulate tool call chunks (function name + arguments) into
		// a structured ToolCall list, same as the non-streaming path.
		if len(chunk.Choices[0].Delta.ToolCalls) > 0 {
			for _, tcDelta := range chunk.Choices[0].Delta.ToolCalls {
				accumulateStreamToolCall(&streamToolCalls, tcDelta)
				if request.StreamCallback != nil {
					if args := tcDelta.Function.Arguments; args != "" {
						request.StreamCallback(args)
					} else if name := tcDelta.Function.Name; name != "" {
						request.StreamCallback(name)
					}
				}
			}
		}
		if delta != "" {
			fullText.WriteString(delta)
			if request.StreamCallback != nil {
				request.StreamCallback(delta)
			}
		}
	}

	// Build ToolCalls from accumulated streaming deltas.
	toolCalls := finalizeStreamToolCalls(streamToolCalls)

	// RC-9: Parse text-format tool calls from the streaming text.
	// Some models (especially DeepSeek) output tool calls as text markup
	// (XML/function_call tags) rather than native tool_call SSE deltas.
	// This catches those cases so they are executed same as native calls.
	if len(toolCalls) == 0 && fullText.Len() > 0 {
		parsed := parseToolCallsRaw(fullText.String())
		if len(parsed) == 0 {
			// Fallback: comprehensive parser for other text formats.
			parsed = parseTextToolCalls(fullText.String())
		}
		for _, ptc := range parsed {
			var args map[string]any
			if argsErr := json.Unmarshal([]byte(ptc.Function.Arguments), &args); argsErr == nil {
				toolCalls = append(toolCalls, ToolCall{
					ID:        ptc.ID,
					Name:      ptc.Function.Name,
					Arguments: args,
				})
			}
		}
	}

	// RC-1: If streaming received tool calls, execute them and continue
	// the conversation with non-streaming requests.
	if len(toolCalls) > 0 {
		// Build assistant message with the streaming tool calls.
		assistantMsg := openAIMessage{
			Role:             "assistant",
			Content:          fullText.String(),
			ReasoningContent: reasoningContentForToolReplay(c.Provider(), thinkingOn, fullReasoning.String()),
		}
		nativeToolCalls := make([]openAIToolCall, 0, len(toolCalls))
		allStreamToolCalls := make([]ToolCall, 0, len(toolCalls))
		for _, tc := range toolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			nativeToolCalls = append(nativeToolCalls, openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      tc.Name,
					Arguments: string(argsJSON),
				},
			})
			allStreamToolCalls = append(allStreamToolCalls, tc)
		}
		assistantMsg.ToolCalls = nativeToolCalls
		messages = append(messages, assistantMsg)

		// Execute tools and append tool result messages.
		for _, ntc := range nativeToolCalls {
			messages = append(messages, openAIMessage{
				Role:       "tool",
				ToolCallID: ntc.ID,
				Content:    executeOpenAIToolCall(ctx, ntc),
			})
		}

		// Continue with non-streaming tool loop for remaining turns.
		totalUsage := openAICompatibleUsage{}
		if scanner.usage != nil {
			totalUsage = *scanner.usage
		}
		return c.runNonStreamingToolLoop(ctx, resolved, model, request, messages, 1, allStreamToolCalls, totalUsage, httpClient)
	}

	usage := usagePreferKnown(
		chunkToUsage(scanner.usage),
		EstimateUsageFromOutput(fullText.String(), fullReasoning.String()),
	)
	resp := Response{
		Text:         strings.TrimSpace(fullText.String()),
		Reasoning:    strings.TrimSpace(fullReasoning.String()),
		FinishReason: NormalizeFinishReason(c.Provider(), lastFinishReason),
		StreamError:  streamError,
		Provider:     c.Provider(),
		Model:        resolved.model,
		Fallback:     false,
		Usage:        usage,
		ToolCalls:    toolCalls,
	}
	if err := scanner.Err(); err != nil {
		return resp, fmt.Errorf("read openai-compatible stream: %w", err)
	}
	return resp, nil
}

// streamToolCallAccumulator tracks in-progress tool call assembly during SSE streaming.
type streamToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// accumulateStreamToolCall merges a streaming tool_call delta chunk into an accumulator.
func accumulateStreamToolCall(acc *[]streamToolCallAccumulator, delta streamToolCallDelta) {
	idx := delta.Index
	// Grow slice if needed.
	for len(*acc) <= idx {
		*acc = append(*acc, streamToolCallAccumulator{})
	}
	tc := &(*acc)[idx]
	if delta.ID != "" {
		tc.ID = delta.ID
	}
	if delta.Function.Name != "" {
		tc.Name = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		tc.Arguments.WriteString(delta.Function.Arguments)
	}
}

// finalizeStreamToolCalls converts accumulated streaming tool calls into
// the standard ToolCall slice format. SP-Fix-1.
func finalizeStreamToolCalls(acc []streamToolCallAccumulator) []ToolCall {
	if len(acc) == 0 {
		return nil
	}
	result := make([]ToolCall, len(acc))
	for i, tc := range acc {
		argsStr := tc.Arguments.String()
		var args map[string]any
		if argsStr != "" {
			_ = json.Unmarshal([]byte(argsStr), &args)
		}
		result[i] = ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: args,
		}
	}
	return result
}

type streamChunk struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []streamToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *openAICompatibleUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type streamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// buildResponseFormat constructs the response_format payload for OpenAI-compatible APIs.
// When schema is provided, uses json_schema + strict:true for guaranteed valid JSON.
// Falls back to json_object when no schema is available.
func buildResponseFormat(schema map[string]any) json.RawMessage {
	if schema == nil {
		return json.RawMessage(`{"type":"json_object"}`)
	}
	rf := map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "response",
			"strict": true,
			"schema": schema,
		},
	}
	b, _ := json.Marshal(rf)
	return json.RawMessage(b)
}

func chunkToUsage(u *openAICompatibleUsage) Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{
		PromptTokens:       u.PromptTokens,
		CompletionTokens:   u.CompletionTokens,
		TotalTokens:        u.TotalTokens,
		CachedPromptTokens: u.cachedPromptTokens(),
		Known:              u.TotalTokens > 0 || u.PromptTokens > 0 || u.CompletionTokens > 0,
	}
}

// sseScanner wraps bufio.Scanner for SSE stream parsing.
type sseScanner struct {
	scanner *bufio.Scanner
	usage   *openAICompatibleUsage
}

func newSSEScanner(r io.Reader) *sseScanner {
	s := bufio.NewScanner(r)
	// NEW-2: Increase buffer to 1MB to avoid silent truncation of long SSE data lines.
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &sseScanner{scanner: s}
}

func (s *sseScanner) Scan() bool {
	return s.scanner.Scan()
}

func (s *sseScanner) Text() string {
	return s.scanner.Text()
}

func (s *sseScanner) Err() error {
	if s == nil || s.scanner == nil {
		return nil
	}
	return s.scanner.Err()
}

func usageFromPartialBody(body []byte) Usage {
	if len(body) == 0 {
		return Usage{}
	}
	var completion openAICompatibleChatCompletionResponse
	if json.Unmarshal(body, &completion) == nil {
		u := chunkToUsage(&completion.Usage)
		if u.Known {
			return u
		}
	}
	return EstimateUsageFromOutput(string(body), "")
}

func parseTextToolCalls(content string) []openAIToolCall {
	if content == "" {
		return nil
	}

	// Normalize CJK bracket variants to ASCII for robust matching.
	// DeepSeek models sometimes output 〈function_calls〉 (CJK corner brackets
	// U+300C/U+300D) or fullwidth variants instead of ASCII < >.
	content = normalizeBrackets(content)

	var calls []openAIToolCall

	// Robust approach: search for invoke/function blocks by the "invoke name="
	// or "function name=" patterns, then extract all parameter name/value pairs.
	// This works regardless of whether the outer wrapper is closed or what
	// bracket characters are used (after normalizeBrackets).
	calls = append(calls, robustParseInvokeCalls(content)...)

	// Fallback: wrapper-based parsing for complete XML blocks.
	calls = append(calls, parseDeepSeekInvokeXML(content)...)

	// Pattern 2: Simple Chinese XML.
	calls = append(calls, parseXMLToolCalls(content, "函数调用", "函数名称", "参数")...)
	calls = append(calls, parseXMLToolCalls(content, "tool_call", "tool_name", "parameters")...)

	// Pattern 3: Anthropic-style function_calls XML.
	calls = append(calls, parseAnthropicStyleXML(content)...)

	// Pattern 4: JSON code block containing tool calls.
	calls = append(calls, parseJSONBlockToolCalls(content)...)

	return calls
}

// normalizeBrackets replaces CJK/Unicode bracket variants with ASCII equivalents.
// DeepSeek models may output tool call XML using CJK corner brackets or fullwidth
// angle brackets instead of ASCII < > characters.
// parseToolCallsRaw extracts tool calls from raw model text WITHOUT bracket
// normalization. Instead of normalizing FF5C/Unicode brackets, it searches for
// the invariant substrings "nvoke name=" and "parameter name=" that appear in
// every format variant. This avoids the corruption that normalizeBrackets causes
// with closing tags like </parameter>.
func parseToolCallsRaw(content string) []openAIToolCall {
	var calls []openAIToolCall
	remaining := content

	for {
		// Find tool name via "nvoke name=\"" or "unction name=\"" (skip first char
		// which may be any bracket variant).
		invokeIdx := strings.Index(remaining, "nvoke name=\"")
		if invokeIdx < 0 {
			invokeIdx = strings.Index(remaining, "unction name=\"")
		}
		if invokeIdx < 0 {
			break
		}
		// Back up to include the opening bracket/delimiter.
		if invokeIdx > 0 {
			invokeIdx-- // include the '<' or '｜' before "invoke"
		}
		afterInvoke := remaining[invokeIdx+1:] // skip the opening delimiter
		nameStart := strings.Index(afterInvoke, "nvoke name=\"")
		if nameStart < 0 {
			nameStart = strings.Index(afterInvoke, "unction name=\"")
		}
		if nameStart < 0 {
			remaining = afterInvoke
			continue
		}
		// Skip "nvoke name=\"" or "unction name=\"" to get to the tool name.
		prefixLen := len("nvoke name=\"")
		if strings.HasPrefix(afterInvoke[nameStart:], "unction name=\"") {
			prefixLen = len("unction name=\"")
		}
		toolNameStart := nameStart + prefixLen
		toolNameEnd := strings.Index(afterInvoke[toolNameStart:], "\"")
		if toolNameEnd < 0 {
			remaining = afterInvoke[toolNameStart:]
			continue
		}
		toolName := afterInvoke[toolNameStart : toolNameStart+toolNameEnd]
		if toolName == "" {
			remaining = afterInvoke[toolNameStart+toolNameEnd:]
			continue
		}

		// Extract parameters: search for "parameter name=\"KEY\"" patterns
		// and extract VALUE that follows after the ">".
		blockStart := toolNameStart + toolNameEnd
		block := afterInvoke[blockStart:]
		// Find end of this invoke block: next "nvoke name=" or end of text.
		nextInvoke := strings.Index(block, "nvoke name=\"")
		if nextInvoke < 0 {
			nextInvoke = strings.Index(block, "unction name=\"")
		}
		if nextInvoke >= 0 {
			block = block[:nextInvoke]
		}

		params := map[string]any{}
		paramSearch := block
		for {
			// Find "parameter name="
			pIdx := strings.Index(paramSearch, "parameter name=\"")
			if pIdx < 0 {
				break
			}
			rest := paramSearch[pIdx+len("parameter name=\""):]
			// Find end of parameter name (closing quote).
			pNameEnd := strings.Index(rest, "\"")
			if pNameEnd < 0 {
				break
			}
			paramName := rest[:pNameEnd]
			// Skip past attributes (string="true") to find ">"
			afterName := rest[pNameEnd+1:]
			valOpen := strings.Index(afterName, ">")
			if valOpen < 0 {
				break
			}
			// VALUE starts after ">"
			valStart := valOpen + 1
			// VALUE ends at "</parameter>" or "<" or end of block.
			valSearch := afterName[valStart:]
			valEnd := strings.Index(valSearch, "</parameter>")
			if valEnd < 0 {
				// Try to find any "<" which marks the start of next tag.
				valEnd = strings.Index(valSearch, "<")
			}
			if valEnd < 0 {
				valEnd = len(valSearch)
			}
			paramValue := valSearch[:valEnd]

			params[paramName] = paramValue
			paramSearch = afterName[valStart+valEnd:]
		}

		if len(params) > 0 {
			argsBytes, _ := json.Marshal(params)
			calls = append(calls, openAIToolCall{
				ID:   fmt.Sprintf("raw-%s-%d", toolName, len(calls)),
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: toolName, Arguments: string(argsBytes)},
			})
		}
		remaining = afterInvoke[blockStart:]
	}
	return calls
}

// robustParseInvokeCalls directly scans content for invoke/function blocks and
// extracts parameter name/value pairs. This handles DeepSeek's actual output
// format without relying on properly closed wrapper tags or exact XML structure.
func robustParseInvokeCalls(content string) []openAIToolCall {
	var calls []openAIToolCall

	for _, nameTag := range []string{"invoke name=\"", "function name=\""} {
		remaining := content
		for {
			start := strings.Index(remaining, nameTag)
			if start < 0 {
				break
			}
			afterTag := remaining[start+len(nameTag):]
			nameEnd := strings.Index(afterTag, "\"")
			if nameEnd < 0 {
				remaining = afterTag
				continue
			}
			toolName := strings.TrimSpace(afterTag[:nameEnd])
			if toolName == "" {
				remaining = afterTag[nameEnd:]
				continue
			}

			// Extract parameters from ALL remaining text until the next invoke
			// or end of content. This handles missing </invoke> close tags.
			// We extract parameter name="KEY">VALUE</parameter> pairs.
			var endOfBlock int
			nextInvoke := strings.Index(afterTag[nameEnd:], nameTag)
			if nextInvoke >= 0 {
				endOfBlock = nameEnd + nextInvoke
			} else {
				endOfBlock = len(afterTag)
			}
			blockContent := afterTag[nameEnd:endOfBlock]

			params := map[string]any{}
			paramSearch := blockContent
			for {
				paramStart := strings.Index(paramSearch, "parameter name=\"")
				if paramStart < 0 {
					break
				}
				paramRest := paramSearch[paramStart+len("parameter name=\""):]
				paramNameEnd := strings.Index(paramRest, "\"")
				if paramNameEnd < 0 {
					paramSearch = paramRest[1:] // skip malformed parameter
					continue
				}
				paramName := paramRest[:paramNameEnd]
				afterParamName := paramRest[paramNameEnd+1:]
				valueTagEnd := strings.Index(afterParamName, ">")
				if valueTagEnd < 0 {
					paramSearch = afterParamName[1:] // skip malformed parameter
					continue
				}
				valueStart := valueTagEnd + 1
				valueEnd := strings.Index(afterParamName[valueStart:], "</parameter>")
				if valueEnd < 0 {
					// Missing closing tag for this parameter — skip it,
					// don't break the entire parameter loop.
					paramSearch = afterParamName[valueStart:]
					continue
				}
				paramValue := afterParamName[valueStart : valueStart+valueEnd]
				params[paramName] = paramValue
				paramSearch = afterParamName[valueStart+valueEnd+len("</parameter>"):]
			}

			if toolName != "" && len(params) > 0 {
				argsBytes, _ := json.Marshal(params)
				calls = append(calls, openAIToolCall{
					ID:   fmt.Sprintf("robust-%s-%d", toolName, len(calls)),
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: toolName, Arguments: string(argsBytes)},
				})
			}
			remaining = afterTag[nameEnd:]
		}
	}
	return calls
}

func normalizeBrackets(s string) string {
	// DeepSeek models sometimes output ｜ (U+FF5C FULLWIDTH VERTICAL LINE) in
	// place of < angle brackets. Patterns observed:
	//   ｜｜ → <  (double FF5C = one angle bracket)
	//   <｜｜ → < (ASCII < followed by double FF5C = just one <)
	//   ｜ → <   (single FF5C = one angle bracket)
	// We must process these in order to avoid double-replacement.
	replacer := strings.NewReplacer(
		"<｜｜", "<", // <｜｜ → < (already has ASCII <, FF5C is extraneous)
		"｜｜", "<", // ｜｜ → <
		"｜", "<", // ｜ → <
		"〈", "<", // LEFT CORNER BRACKET
		"〉", ">", // RIGHT CORNER BRACKET
		"《", "<", // LEFT DOUBLE ANGLE BRACKET
		"》", ">", // RIGHT DOUBLE ANGLE BRACKET
		"＜", "<", // FULLWIDTH LESS-THAN SIGN
		"＞", ">", // FULLWIDTH GREATER-THAN SIGN
	)
	return replacer.Replace(s)
}

// parseDeepSeekInvokeXML handles DeepSeek's actual tool call output format.
// DeepSeek uses two wrapper variants:
//
//	Variant A (Chinese): <函数调用><invoke name="...">...</invoke></函数调用>
//	Variant B (English): <function_calls><invoke name="...">...</invoke></function_calls>
//	Variant C (Anthropic): <function_calls><function name="...">...</function></function_calls>
//
// Parameter tags may have xml attribute decorations (string="true") that we ignore.
func parseDeepSeekInvokeXML(content string) []openAIToolCall {
	var calls []openAIToolCall

	for _, wrapper := range []string{"<函数调用>", "<function_calls>", "<tool_calls>"} {
		wrapperClose := strings.Replace(wrapper, "<", "</", 1)
		calls = append(calls, parseInvokeWrapper(content, wrapper, wrapperClose)...)
	}
	return calls
}

func parseInvokeWrapper(content, wrapper, wrapperClose string) []openAIToolCall {
	var calls []openAIToolCall

	for {
		start := strings.Index(content, wrapper)
		if start < 0 {
			break
		}
		end := strings.Index(content[start:], wrapperClose)
		if end < 0 {
			break
		}
		block := content[start : start+end+len(wrapperClose)]
		content = content[start+end+len(wrapperClose):]

		// Find all <invoke name="..."> blocks inside.
		for _, invokeTag := range []string{"<invoke name=\"", "<function name=\""} {
			calls = append(calls, parseInvokeBlock(block, invokeTag)...)
		}
	}
	return calls
}

func parseInvokeBlock(content, invokePrefix string) []openAIToolCall {
	var calls []openAIToolCall
	const invokeClose = "</invoke>"
	const funcClose = "</function>"

	for {
		start := strings.Index(content, invokePrefix)
		if start < 0 {
			break
		}
		rest := content[start+len(invokePrefix):]
		nameEnd := strings.Index(rest, "\"")
		if nameEnd < 0 {
			break
		}
		name := strings.TrimSpace(rest[:nameEnd])

		// Find matching close tag.
		closeTag := invokeClose
		if invokePrefix == "<function name=\"" {
			closeTag = funcClose
		}
		closeStart := strings.Index(rest, closeTag)
		if closeStart < 0 {
			break
		}
		invokeBlock := rest[nameEnd : closeStart+len(closeTag)]
		content = rest[nameEnd+closeStart+len(closeTag):]

		// Extract parameters.
		params := map[string]any{}
		paramBlock := invokeBlock
		for {
			paramStart := strings.Index(paramBlock, "<parameter name=\"")
			if paramStart < 0 {
				break
			}
			paramRest := paramBlock[paramStart+len("<parameter name=\""):]
			// Find end of name attribute (before ">)
			paramNameEnd := strings.Index(paramRest, "\"")
			if paramNameEnd < 0 {
				break
			}
			paramName := strings.TrimSpace(paramRest[:paramNameEnd])

			// Skip any attributes like string="true" until >
			afterName := paramRest[paramNameEnd+1:]
			tagEnd := strings.Index(afterName, ">")
			if tagEnd < 0 {
				break
			}

			// Find </parameter>
			valStart := tagEnd + 1
			valEnd := strings.Index(afterName[valStart:], "</parameter>")
			if valEnd < 0 {
				break
			}
			paramVal := afterName[valStart : valStart+valEnd]

			params[paramName] = paramVal
			paramBlock = afterName[valStart+valEnd+len("</parameter>"):]
		}

		if name != "" && len(params) > 0 {
			argsBytes, err := json.Marshal(params)
			if err != nil {
				continue
			}
			calls = append(calls, openAIToolCall{
				ID:   fmt.Sprintf("ds-%s-%d", name, len(calls)),
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: name, Arguments: string(argsBytes)},
			})
		}
	}
	return calls
}

func parseXMLToolCalls(content, blockTag, nameTag, paramTag string) []openAIToolCall {
	var calls []openAIToolCall
	openTag := "<" + blockTag + ">"
	closeTag := "</" + blockTag + ">"

	for {
		start := strings.Index(content, openTag)
		if start < 0 {
			break
		}
		end := strings.Index(content[start:], closeTag)
		if end < 0 {
			break
		}
		block := content[start : start+end+len(closeTag)]
		content = content[start+end+len(closeTag):]

		// Extract name.
		nameOpen := "<" + nameTag + ">"
		nameClose := "</" + nameTag + ">"
		ns := strings.Index(block, nameOpen)
		ne := strings.Index(block, nameClose)
		if ns < 0 || ne < 0 || ne <= ns {
			continue
		}
		name := strings.TrimSpace(block[ns+len(nameOpen) : ne])

		// Extract params (JSON).
		paramOpen := "<" + paramTag + ">"
		paramClose := "</" + paramTag + ">"
		ps := strings.Index(block, paramOpen)
		pe := strings.Index(block, paramClose)
		if ps < 0 || pe < 0 || pe <= ps {
			continue
		}
		argsStr := block[ps+len(paramOpen) : pe]

		// Validate JSON.
		var args map[string]any
		if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
			// Try unescaping.
			argsStr = strings.ReplaceAll(argsStr, `\"`, `"`)
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				continue
			}
		}

		calls = append(calls, openAIToolCall{
			ID:   fmt.Sprintf("text-%s-%d", name, len(calls)),
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: name, Arguments: argsStr},
		})
	}
	return calls
}

func parseAnthropicStyleXML(content string) []openAIToolCall {
	var calls []openAIToolCall
	// <function_calls><invoke name="tool_name"><parameter name="p">val</parameter></invoke></function_calls>
	// or <function_calls><function name="tool_name"><parameter name="p">val</parameter></function></function_calls>

	for _, invokeTag := range []string{"invoke", "function"} {
		prefix := "<" + invokeTag + " name=\""
		for {
			start := strings.Index(content, prefix)
			if start < 0 {
				break
			}
			rest := content[start+len(prefix):]
			nameEnd := strings.Index(rest, "\"")
			if nameEnd < 0 {
				content = rest
				break
			}
			name := rest[:nameEnd]
			closeTag := "</" + invokeTag + ">"
			blockEnd := strings.Index(rest, closeTag)
			if blockEnd < 0 {
				content = rest[nameEnd:]
				break
			}
			content = rest[nameEnd+blockEnd+len(closeTag):]

			// Extract parameters as JSON.
			params := map[string]any{}
			block := rest[:blockEnd+len(closeTag)]
			for {
				paramStart := strings.Index(block, "<parameter name=\"")
				if paramStart < 0 {
					break
				}
				paramRest := block[paramStart+len("<parameter name=\""):]
				paramNameEnd := strings.Index(paramRest, "\"")
				if paramNameEnd < 0 {
					break
				}
				paramName := paramRest[:paramNameEnd]
				paramValStart := strings.Index(paramRest[paramNameEnd:], ">")
				paramValEnd := strings.Index(paramRest[paramNameEnd:], "</parameter>")
				if paramValStart < 0 || paramValEnd < 0 {
					break
				}
				paramVal := paramRest[paramNameEnd+paramValStart+1 : paramNameEnd+paramValEnd]
				params[paramName] = paramVal
				block = paramRest[paramNameEnd+paramValEnd+len("</parameter>"):]
			}

			if len(params) > 0 {
				argsBytes, _ := json.Marshal(params)
				calls = append(calls, openAIToolCall{
					ID:   fmt.Sprintf("xml-%s-%d", name, len(calls)),
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: name, Arguments: string(argsBytes)},
				})
			}
		}
	}
	return calls
}

func parseJSONBlockToolCalls(content string) []openAIToolCall {
	var calls []openAIToolCall
	// Look for ```json ... ``` blocks that contain tool call arrays.
	for {
		start := strings.Index(content, "```json")
		if start < 0 {
			break
		}
		rest := content[start+len("```json"):]
		end := strings.Index(rest, "```")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(rest[:end])
		content = rest[end+len("```"):]

		// Try parsing as JSON array of tool calls.
		var raw []map[string]any
		if err := json.Unmarshal([]byte(block), &raw); err != nil {
			continue
		}
		for _, item := range raw {
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(map[string]any)
			if name == "" {
				continue
			}
			argsBytes, _ := json.Marshal(args)
			calls = append(calls, openAIToolCall{
				ID:   fmt.Sprintf("json-%s-%d", name, len(calls)),
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: name, Arguments: string(argsBytes)},
			})
		}
	}
	return calls
}
