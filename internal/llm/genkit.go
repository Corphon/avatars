package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	genkitai "github.com/firebase/genkit/go/ai"
	genkitapi "github.com/firebase/genkit/go/core/api"
	genkitruntime "github.com/firebase/genkit/go/genkit"
	anthropicplugin "github.com/firebase/genkit/go/plugins/anthropic"
	compatoaiplugin "github.com/firebase/genkit/go/plugins/compat_oai"
	googleplugin "github.com/firebase/genkit/go/plugins/googlegenai"
	openaisdk "github.com/openai/openai-go"
	genaisdk "google.golang.org/genai"
)

type GenkitConfig struct {
	Provider        string
	Model           string
	EnableWebSearch bool
	ThinkMode       ThinkMode
	APIKey          string
	APIKeyEnv       string
	BaseURL         string
}

type GenkitClient struct {
	config             GenkitConfig
	once               sync.Once
	runtime            *genkitruntime.Genkit
	initErr            error
	mu                 sync.Mutex // G3: protects failure counter + re-init
	consecutiveFails   int        // G3: track consecutive failures for auto-reset
}

func NewGenkitClient(config GenkitConfig) *GenkitClient {
	config.Provider = resolveGenkitProvider(normalizeProviderName(config.Provider), config.Model)
	if config.Provider == "" {
		config.Provider = "google"
	}
	config.ThinkMode = normalizeThinkMode(config.ThinkMode)
	return &GenkitClient{config: config}
}

// Reset clears the runtime and allows re-initialization on the next call.
// G3: Call after consecutive failures to force a fresh connection.
func (c *GenkitClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.once = sync.Once{}
	c.runtime = nil
	c.initErr = nil
	c.consecutiveFails = 0
}

func (c *GenkitClient) Provider() string {
	return c.config.Provider
}

func (c *GenkitClient) Supports(capability Capability) bool {
	return capability == CapabilityWebSearch && c.config.EnableWebSearch && c.config.Provider == "google"
}

func (c *GenkitClient) Generate(ctx context.Context, request Request) (Response, error) {
	model := normalizeGenkitModel(c.config.Provider, c.config.Model)
	if shouldUseDeterministicGenkitFallback(model) {
		// P0-5b: Deterministic fallback is a hard error for code generation.
		// Placeholder text silently corrupts downstream processing (P13-5).
		if request.Category == CategoryCodeGeneration {
			return Response{}, fmt.Errorf("genkit: code generation requires a live LLM — deterministic fallback is not available")
		}
		return c.fallbackResponse(model, request), nil
	}

	runtime, err := c.runtimeInstance(ctx, model)
	if err != nil {
		// G3: Track consecutive failures for auto-reset.
		c.mu.Lock()
		c.consecutiveFails++
		fails := c.consecutiveFails
		c.mu.Unlock()
		if fails >= 3 {
			c.Reset()
		}
		return Response{}, err
	}
	// G3: Reset failure counter on success (connection is healthy).
	c.mu.Lock()
	c.consecutiveFails = 0
	c.mu.Unlock()
	if runtime == nil {
		return Response{}, fmt.Errorf("%s: genkit runtime not initialized for model %q — check provider configuration",
			c.Provider(), model)
	}

	options := []genkitai.GenerateOption{
		genkitai.WithModelName(model),
	}
	if systemPrompt := strings.TrimSpace(request.SystemPrompt); systemPrompt != "" {
		options = append(options, genkitai.WithSystem(systemPrompt))
	}
	if userPrompt := strings.TrimSpace(request.UserPrompt); userPrompt != "" {
		options = append(options, genkitai.WithPrompt(userPrompt))
	}
	if config := c.generateConfig(request); config != nil {
		options = append(options, genkitai.WithConfig(config))
	}

	if len(request.Tools) > 0 {
		toolRefs := buildGenkitToolRefs(request.Tools)
		if len(toolRefs) > 0 {
			options = append(options, genkitai.WithTools(toolRefs...))
		}
	}

	// P0-5c: Use Generate (not GenerateText) to capture Usage for telemetry.
	resp, err := genkitruntime.Generate(ctx, runtime, options...)
	if err != nil {
		return Response{}, fmt.Errorf("genkit generate failed: %w", err)
	}

	// NEW-5: Usage.Known requires all three fields to be populated.
	usage := Usage{}
	if resp.Usage != nil && resp.Usage.TotalTokens > 0 && resp.Usage.InputTokens > 0 && resp.Usage.OutputTokens > 0 {
		usage = Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			Known:            true,
		}
	}

	// S5.1 / H-7: Rebuild ToolCalls from conversation history (genkit auto-executes
	// tools; final Message may lack ToolRequest parts, but History retains them).
	toolCalls := extractGenkitToolCalls(resp)

	return Response{
		Text:         strings.TrimSpace(resp.Text()),
		ToolCalls:    toolCalls,
		FinishReason: NormalizeFinishReason(c.Provider(), string(resp.FinishReason)),
		Provider:     c.Provider(),
		Model:        model,
		Fallback:     false,
		Usage:        usage,
	}, nil
}

// extractGenkitToolCalls walks request+response messages for ToolRequest parts. S5.1.
// Avoids ModelResponse.History() which can panic when Request is nil.
func extractGenkitToolCalls(resp *genkitai.ModelResponse) []ToolCall {
	if resp == nil {
		return nil
	}
	var msgs []*genkitai.Message
	if resp.Request != nil {
		msgs = append(msgs, resp.Request.Messages...)
	}
	if resp.Message != nil {
		msgs = append(msgs, resp.Message)
	}
	var out []ToolCall
	seen := map[string]bool{}
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		for _, part := range msg.Content {
			if part == nil || !part.IsToolRequest() || part.ToolRequest == nil {
				continue
			}
			tr := part.ToolRequest
			key := tr.Ref + "\x00" + tr.Name
			if tr.Ref == "" {
				key = fmt.Sprintf("%s\x00%v", tr.Name, tr.Input)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ToolCall{
				ID:        tr.Ref,
				Name:      tr.Name,
				Arguments: toolRequestInputToArgs(tr.Input),
			})
		}
	}
	return out
}

func toolRequestInputToArgs(input any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	if m, ok := input.(map[string]any); ok {
		return m
	}
	// Struct inputs from typed genkit tools — round-trip via JSON.
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var args map[string]any
	if err := json.Unmarshal(data, &args); err != nil {
		return map[string]any{}
	}
	return args
}

func (c *GenkitClient) runtimeInstance(ctx context.Context, model string) (*genkitruntime.Genkit, error) {
	apiKey := lookupGenkitAPIKey(c.config.Provider, c.config.APIKey, c.config.APIKeyEnv)
	if apiKey == "" {
		return nil, nil
	}

	c.once.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				c.initErr = fmt.Errorf("genkit init failed: %v", recovered)
			}
		}()

		plugin, err := c.buildPlugin(apiKey)
		if err != nil {
			c.initErr = err
			return
		}

		c.runtime = genkitruntime.Init(ctx,
			genkitruntime.WithPlugins(plugin),
			genkitruntime.WithDefaultModel(model),
		)
	})

	if c.initErr != nil {
		return nil, c.initErr
	}
	return c.runtime, nil
}

func (c *GenkitClient) generateConfig(request Request) any {
	mode := effectiveThinkMode(c.config.ThinkMode, request)
	switch c.config.Provider {
	case "google":
		config := &genaisdk.GenerateContentConfig{}
		hasConfig := false
		if request.UseWebSearch && c.config.EnableWebSearch {
			config.Tools = []*genaisdk.Tool{{GoogleSearch: &genaisdk.GoogleSearch{}}}
			hasConfig = true
		}
		if thinkingConfig := googleThinkingConfig(mode); thinkingConfig != nil {
			config.ThinkingConfig = thinkingConfig
			hasConfig = true
		}
		if hasConfig {
			return config
		}
	case "anthropic":
		if mode == ThinkModeOn {
			return &anthropicsdk.MessageNewParams{
				Thinking: anthropicsdk.ThinkingConfigParamUnion{
					OfEnabled: &anthropicsdk.ThinkingConfigEnabledParam{
						BudgetTokens: *anthropicsdk.IntPtr(1024),
					},
				},
			}
		}
	case "openai":
		if request.StructuredOutput || mode != ThinkModeAuto {
			return &openaisdk.ChatCompletionNewParams{}
		}
	}

	return nil
}

func (c *GenkitClient) buildPlugin(apiKey string) (genkitapi.Plugin, error) {
	switch c.config.Provider {
	case "google":
		return &googleplugin.GoogleAI{APIKey: apiKey}, nil
	case "anthropic":
		return &anthropicplugin.Anthropic{APIKey: apiKey, BaseURL: strings.TrimSpace(c.config.BaseURL)}, nil
	case "openai":
		return &compatoaiplugin.OpenAICompatible{Provider: "openai", APIKey: apiKey, BaseURL: strings.TrimSpace(c.config.BaseURL)}, nil
	default:
		return nil, fmt.Errorf("unsupported genkit provider %q", c.config.Provider)
	}
}

func googleThinkingConfig(mode ThinkMode) *genaisdk.ThinkingConfig {
	switch mode {
	case ThinkModeOff:
		return &genaisdk.ThinkingConfig{ThinkingBudget: genaisdk.Ptr[int32](0)}
	case ThinkModeOn:
		return &genaisdk.ThinkingConfig{ThinkingBudget: genaisdk.Ptr[int32](1024)}
	default:
		return nil
	}
}

func (c *GenkitClient) fallbackResponse(model string, request Request) Response {
	return Response{
		Text:     buildDeterministicFallback(request),
		Provider: c.Provider(),
		Model:    model,
		Fallback: true,
	}
}

func normalizeGenkitModel(provider string, model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return defaultModelForProvider(provider)
	}
	if strings.Contains(trimmed, "/") || shouldUseDeterministicGenkitFallback(trimmed) {
		return trimmed
	}
	return genkitModelPrefix(provider) + "/" + trimmed
}

func genkitModelPrefix(provider string) string {
	if provider == "google" {
		return "googleai"
	}
	return provider
}

func shouldUseDeterministicGenkitFallback(model string) bool {
	trimmed := strings.TrimSpace(model)
	return strings.HasPrefix(trimmed, "bootstrap-") || strings.EqualFold(trimmed, "deterministic")
}

func lookupGenkitAPIKey(provider string, explicit string, explicitEnv string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	if envName := strings.TrimSpace(explicitEnv); envName != "" {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	switch provider {
	case "anthropic":
		return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	case "openai":
		return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	case "deepseek":
		return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	default:
		if value := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); value != "" {
			return value
		}
		return strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}
}

func buildDeterministicFallback(request Request) string {
	focus := compactPromptText(request.UserPrompt)
	if focus == "" {
		focus = compactPromptText(request.SystemPrompt)
	}
	if focus == "" {
		return "Local deterministic synthesis preserved runtime continuity without a live provider response."
	}
	return fmt.Sprintf("Local deterministic synthesis preserved runtime continuity. Request focus: %s", focus)
}

func compactPromptText(text string) string {
	collapsed := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(collapsed) <= 240 {
		return collapsed
	}
	return collapsed[:237] + "..."
}
