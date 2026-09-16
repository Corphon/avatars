package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Capability string

const CapabilityWebSearch Capability = "web_search"

type ThinkMode string

const (
	ThinkModeAuto ThinkMode = "auto"
	ThinkModeOn   ThinkMode = "on"
	ThinkModeOff  ThinkMode = "off"
)

const (
	defaultGoogleModel    = "googleai/gemini-2.5-flash"
	defaultAnthropicModel = "anthropic/claude-3-5-haiku-latest"
	defaultOpenAIModel    = "openai/gpt-4.1-mini"
)

// ToolDefinition describes a tool that the LLM can call.
// For genkit: converted to genkitai.ToolDef via NewTool.
// For OpenAI-compatible: converted to OpenAI function definition format.
type ToolDefinition struct {
	Name        string         // e.g. "read_file"
	Description string         // e.g. "Read the contents of a file at path"
	Parameters  map[string]any // JSON Schema for the tool's parameters (properties, required, type)
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID        string         // unique ID for this tool call (from LLM)
	Name      string         // tool name to invoke
	Arguments map[string]any // decoded arguments
}

// RequestCategory classifies the LLM request type for fallback policy.
type RequestCategory string

const (
	CategoryAnalysis       RequestCategory = "analysis"
	CategoryCodeGeneration RequestCategory = "code_generation"
	CategorySummary        RequestCategory = "summary"
)

type Request struct {
	SystemPrompt   string
	UserPrompt     string
	Tools          []ToolDefinition   // available tools for the LLM to call
	ToolChoice     string             // "auto"(default), "required", "none"
	MaxTokens      int                // max output tokens (0 = provider default)
	Category       RequestCategory    // request type for fallback policy
	StreamCallback func(chunk string) // P0-6: per-request streaming callback (final answer content)
	// ThinkingCallback receives reasoning/CoT stream deltas (Z10). Separate from
	// StreamCallback so UIs can show "thinking…" without mixing into answer text.
	ThinkingCallback func(chunk string)
	JSONSchema       map[string]any // P1-1: JSON schema for structured output (nil = json_object only)
	Model            string         // P1-7: per-request model override (empty = use client default)
	UseWebSearch     bool
	ThinkMode        ThinkMode
	StructuredOutput bool
	RequestTimeout   time.Duration // per-request HTTP timeout override (0 = use client default)
	MaxToolTurns     int           // max tool-calling loop turns (0 = provider default, typically 5). Builder nodes need higher values.
}

// Standard JSON schemas for structured output (P1-1).
var (
	// ComplexitySchema matches complexitySpec with intent sub-object (P1-3).
	ComplexityJSONSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":     map[string]any{"type": "string", "enum": []string{"edit_file", "create_script", "bootstrap_project", "ask_question", "clarify"}},
					"target":     map[string]any{"type": "string"},
					"lang":       map[string]any{"type": "string"},
					"mode":       map[string]any{"type": "string", "enum": []string{"incremental", "full", "review_only"}},
					"confidence": map[string]any{"type": "number"},
				},
				"required": []string{"action", "confidence"},
			},
			"level":             map[string]any{"type": "string", "enum": []string{"trivial", "small", "medium", "large", "clarify"}},
			"suggested_avatars": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"parallel_groups":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"reason":            map[string]any{"type": "string"},
		},
		"required":             []string{"level", "reason"},
		"additionalProperties": false,
	}

	// ClassifierSchema matches the NL classifier output: {action, answer_id, question, confidence}.
	ClassifierJSONSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":     map[string]any{"type": "string", "enum": []string{"direct_answer", "memory_answer", "safe_run", "clarify", "guarded"}},
			"answer_id":  map[string]any{"type": "string"},
			"question":   map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "number"},
		},
		"required":             []string{"action", "confidence"},
		"additionalProperties": false,
	}
)

type Response struct {
	Text         string     // final text response (may be empty if only tool calls)
	Reasoning    string     // provider thinking / CoT (not mixed into Text)
	ToolCalls    []ToolCall // tool calls requested by the LLM (genkit: auto-executed, rarely populated)
	FinishReason string     // "stop", "length", "tool_calls", or empty if unknown
	Provider     string
	Model        string
	Fallback     bool
	StreamError  string // non-empty if the SSE stream was interrupted by a mid-stream error
	Usage        Usage
}

type Usage struct {
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
	CachedPromptTokens int
	CostMicros         int64
	Known              bool
	Estimated          bool // true when tokens were inferred from bytes, not the provider usage chunk
}

type Config struct {
	Provider           string
	Model              string
	EnableWebSearch    bool
	ThinkMode          ThinkMode
	APIKey             string
	APIKeyEnv          string
	BaseURL            string
	Timeout            time.Duration
	FallbackProviders  []string       // G2: fallback provider list from agent.yaml
	MaxToolTurnsByRole map[string]int // SP-Fix-3: per-role tool calling turns
}

type Client interface {
	Provider() string
	Supports(capability Capability) bool
	Generate(ctx context.Context, request Request) (Response, error)
}

func DefaultConfig() Config {
	return Config{
		Provider:        "genkit",
		Model:           defaultGoogleModel,
		EnableWebSearch: true,
		ThinkMode:       ThinkModeAuto,
		Timeout:         60 * time.Second,
	}
}

func NewClient(config Config) (Client, error) {
	resolved := DefaultConfig()
	explicitProvider := strings.TrimSpace(config.Provider)
	explicitModel := strings.TrimSpace(config.Model)
	if provider := explicitProvider; provider != "" {
		resolved.Provider = provider
	}
	if model := explicitModel; model != "" {
		resolved.Model = model
	}
	providerName := normalizeProviderName(resolved.Provider)
	if explicitProvider != "" && explicitModel == defaultGoogleModel && providerName != "" && providerName != "genkit" && providerName != "google" {
		explicitModel = ""
		resolved.Model = ""
	}
	if explicitModel == "" {
		if model := defaultModelForProvider(resolveGenkitProvider(providerName, resolved.Model)); model != "" && usesGenkitProvider(providerName, resolved.Model) {
			resolved.Model = model
		} else if !usesGenkitProvider(providerName, resolved.Model) {
			resolved.Model = ""
		}
	}
	resolved.EnableWebSearch = config.EnableWebSearch
	resolved.ThinkMode = normalizeThinkMode(config.ThinkMode)
	if strings.TrimSpace(config.APIKey) != "" {
		resolved.APIKey = strings.TrimSpace(config.APIKey)
	}
	if strings.TrimSpace(config.APIKeyEnv) != "" {
		resolved.APIKeyEnv = strings.TrimSpace(config.APIKeyEnv)
	}
	if strings.TrimSpace(config.BaseURL) != "" {
		resolved.BaseURL = strings.TrimSpace(config.BaseURL)
	}
	if config.Timeout > 0 {
		resolved.Timeout = config.Timeout
	}
	if len(config.FallbackProviders) > 0 {
		resolved.FallbackProviders = append([]string(nil), config.FallbackProviders...)
	} else if len(GlobalFallbackProviders) > 0 {
		resolved.FallbackProviders = append([]string(nil), GlobalFallbackProviders...)
	}

	primary, err := newPrimaryClient(resolved, providerName)
	if err != nil {
		return nil, err
	}
	fallbacks := buildFallbackClients(resolved, providerName)
	return NewRetryClientWithFallbacks(primary, fallbacks), nil
}

// newPrimaryClient constructs the inner (non-retry) client for a resolved config. S5.4.
func newPrimaryClient(resolved Config, providerName string) (Client, error) {
	if usesGenkitProvider(providerName, resolved.Model) {
		genkitProvider := resolveGenkitProvider(providerName, resolved.Model)
		if !shouldUseDeterministicGenkitFallback(normalizeGenkitModel(genkitProvider, resolved.Model)) {
			if lookupGenkitAPIKey(genkitProvider, resolved.APIKey, resolved.APIKeyEnv) == "" {
				return nil, fmt.Errorf("%s: no API key configured (set API key or %s)", genkitProvider, genkitAPIKeyHint(genkitProvider))
			}
		}
		return NewGenkitClient(GenkitConfig{
			Provider:        genkitProvider,
			Model:           resolved.Model,
			EnableWebSearch: resolved.EnableWebSearch,
			ThinkMode:       resolved.ThinkMode,
			APIKey:          resolved.APIKey,
			APIKeyEnv:       resolved.APIKeyEnv,
			BaseURL:         resolved.BaseURL,
		}), nil
	}

	ocClient, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:        providerName,
		Model:           resolved.Model,
		EnableWebSearch: resolved.EnableWebSearch,
		ThinkMode:       resolved.ThinkMode,
		APIKey:          resolved.APIKey,
		APIKeyEnv:       resolved.APIKeyEnv,
		BaseURL:         resolved.BaseURL,
		Timeout:         resolved.Timeout,
	})
	if err != nil {
		return nil, err
	}
	// S5.4: Fail at construct when remote preset has no API key.
	resolvedReq, err := ocClient.resolveRequestConfig()
	if err != nil {
		return nil, err
	}
	if resolvedReq.fallback {
		return nil, fmt.Errorf("%s: no API key configured (provider=%s)", providerName, providerName)
	}
	return ocClient, nil
}

func genkitAPIKeyHint(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	default:
		return "GEMINI_API_KEY or GOOGLE_API_KEY"
	}
}

// buildFallbackClients creates secondary clients from config / global list. S5.4.
// Skips the primary provider and entries that fail to construct (missing key).
func buildFallbackClients(primary Config, primaryProvider string) []Client {
	entries := primary.FallbackProviders
	if len(entries) == 0 {
		entries = GlobalFallbackProviders
	}
	if len(entries) == 0 {
		return nil
	}
	primaryName := normalizeProviderName(primaryProvider)
	if primaryName == "" {
		primaryName = normalizeProviderName(primary.Provider)
	}
	out := make([]Client, 0, len(entries))
	seen := map[string]bool{primaryName: true}
	for _, entry := range entries {
		fbProvider, fbModel := resolveFallbackEntry(entry)
		if fbProvider == "" || seen[fbProvider] {
			continue
		}
		seen[fbProvider] = true
		cfg := Config{
			Provider:        fbProvider,
			Model:           fbModel,
			EnableWebSearch: primary.EnableWebSearch,
			ThinkMode:       primary.ThinkMode,
			Timeout:         primary.Timeout,
		}
		client, err := newPrimaryClient(cfg, fbProvider)
		if err != nil {
			continue // skip unusable fallbacks; primary still runs
		}
		out = append(out, client)
	}
	return out
}

// resolveFallbackEntry maps yaml entries like "qwen", "deepseek-chat", or
// "google/gemini-2.5-flash" to (provider, optional model). S5.4.
func resolveFallbackEntry(entry string) (provider string, model string) {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return "", ""
	}
	if i := strings.Index(trimmed, "/"); i > 0 {
		return normalizeProviderName(trimmed[:i]), trimmed
	}
	normalized := normalizeProviderName(trimmed)
	if _, ok := openAICompatiblePresets[normalized]; ok {
		return normalized, ""
	}
	if usesGenkitProvider(normalized, "") {
		return resolveGenkitProvider(normalized, ""), ""
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "deepseek"):
		return "deepseek", trimmed
	case strings.HasPrefix(lower, "qwen"):
		return "qwen", trimmed
	case strings.HasPrefix(lower, "gemini"), strings.HasPrefix(lower, "google"):
		return "google", trimmed
	case strings.HasPrefix(lower, "claude"):
		return "anthropic", trimmed
	case strings.HasPrefix(lower, "gpt"), strings.HasPrefix(lower, "o1"), strings.HasPrefix(lower, "o3"):
		return "openai", trimmed
	case strings.HasPrefix(lower, "glm"):
		return "glm", trimmed
	case strings.HasPrefix(lower, "kimi"), strings.HasPrefix(lower, "moonshot"):
		return "kimi", trimmed
	case strings.HasPrefix(lower, "agnes"):
		return "agnes", trimmed
	default:
		return normalized, ""
	}
}

func normalizeProviderName(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	switch normalized {
	case "googleai", "google-ai":
		return "google"
	case "lmstudio":
		return "lm-studio"
	case "moonshot":
		return "kimi"
	case "zhipu", "bigmodel":
		return "glm"
	case "volcengine", "ark":
		return "doubao"
	case "nvidia-nim":
		return "nvidia"
	case "agnes-ai", "agnesai":
		return "agnes"
	}
	return normalized
}

func normalizeThinkMode(mode ThinkMode) ThinkMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case "on":
		return ThinkModeOn
	case "off":
		return ThinkModeOff
	default:
		return ThinkModeAuto
	}
}

func effectiveThinkMode(defaultMode ThinkMode, request Request) ThinkMode {
	mode := normalizeThinkMode(defaultMode)
	if strings.TrimSpace(string(request.ThinkMode)) != "" {
		mode = normalizeThinkMode(request.ThinkMode)
	}
	if request.StructuredOutput && mode == ThinkModeAuto {
		return ThinkModeOff
	}
	return mode
}

func usesGenkitProvider(provider string, model string) bool {
	switch resolveGenkitProvider(provider, model) {
	case "google", "anthropic", "openai":
		return true
	default:
		return false
	}
}

func resolveGenkitProvider(provider string, model string) string {
	switch provider {
	case "", "genkit":
		if prefix, _, ok := strings.Cut(strings.TrimSpace(model), "/"); ok {
			switch normalizeProviderName(prefix) {
			case "anthropic", "openai", "google":
				return normalizeProviderName(prefix)
			}
		}
		return "google"
	case "google", "anthropic", "openai":
		return provider
	default:
		return ""
	}
}

func defaultModelForProvider(provider string) string {
	switch provider {
	case "anthropic":
		return defaultAnthropicModel
	case "openai":
		return defaultOpenAIModel
	case "google", "genkit", "":
		return defaultGoogleModel
	default:
		return ""
	}
}

func defaultTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return DefaultConfig().Timeout
}

func missingConfigurationError(provider string, message string) error {
	return fmt.Errorf("llm provider %q is not fully configured: %s", provider, message)
}

// --- Standard tool definitions for code generation ---

// StandardCodeTools returns the standard set of tools for Builder/code-generation
// avatars: read_file, write_file, edit_file, shell_done.
// Callers pass these as Request.Tools to enable native function calling.
func StandardCodeTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "read_file",
			Description: "Read the full contents of a file at the given path. Use this to inspect existing code before editing.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to read (relative or absolute).",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write content to a file, creating it if it doesn't exist. Use this for new files. For modifying existing files, prefer edit_file to avoid overwriting unrelated content.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to write (relative or absolute).",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The full content to write to the file.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "edit_file",
			Description: "Make a precise edit to an existing file by replacing old_string with new_string. The old_string must appear exactly once in the file — provide enough surrounding context to make it unique. This is the preferred tool for modifying existing files; it avoids accidental overwrites.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to edit (relative or absolute).",
					},
					"old_string": map[string]any{
						"type":        "string",
						"description": "The exact text to replace — must match exactly once in the file. Include enough surrounding lines to make it unique.",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "The replacement text.",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
		{
			Name:        "precise_edit",
			Description: "Make a precise edit using an anchor string to locate the edit point. Supports position modes: replace (default), before (insert new_block before anchor), after (insert new_block after anchor).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to edit.",
					},
					"anchor": map[string]any{
						"type":        "string",
						"description": "Unique text to locate the edit point in the file.",
					},
					"old_block": map[string]any{
						"type":        "string",
						"description": "The block to replace (optional: if empty, anchor IS the block to replace).",
					},
					"new_block": map[string]any{
						"type":        "string",
						"description": "The replacement text.",
					},
					"position": map[string]any{
						"type":        "string",
						"description": "Where to place new_block relative to anchor: 'replace' (default), 'before', or 'after'.",
					},
				},
				"required": []string{"path", "anchor", "new_block"},
			},
		},
		{
			Name:        "shell_done",
			Description: "Execute a shell command and return its output. Use for build checks (go build, cargo check), running tests, git operations, or inspecting the project structure.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The shell command to execute.",
					},
					"working_dir": map[string]any{
						"type":        "string",
						"description": "Working directory for the command (defaults to project root).",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "git_diff",
			Description: "Show git diff of changed files. Use to verify a fix only changed what was intended — no over-editing into unrelated areas. Returns unified diff of working tree changes.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Specific file to diff (omit for all changed files).",
					},
				},
				"required": []string{},
			},
		},
	}
}

// RetryClient wraps a Client with automatic retry, backoff, caching, and
// optional fallback provider chain (S5.4).
type RetryClient struct {
	inner     Client
	fallbacks []Client
	cache     map[string]cachedResponse
	mu        sync.RWMutex
}

type cachedResponse struct {
	resp      Response
	expiresAt time.Time
}

// NewRetryClient creates a retry+cache wrapper around an existing LLM client.
func NewRetryClient(inner Client) *RetryClient {
	return NewRetryClientWithFallbacks(inner, nil)
}

// NewRetryClientWithFallbacks creates a retry wrapper with a real provider
// switch chain. On primary failure, each fallback is tried once. S5.4.
func NewRetryClientWithFallbacks(inner Client, fallbacks []Client) *RetryClient {
	return &RetryClient{
		inner:     inner,
		fallbacks: append([]Client(nil), fallbacks...),
		cache:     make(map[string]cachedResponse),
	}
}

// SP-Fix-7: Disk-persistent cache directory. When set, cache entries are
// written to ~/.avatars/cache/llm/<hash>.json and loaded on startup.
// Only analysis/summary requests are cached; code generation is never cached.
var diskCacheDir string

// SetDiskCacheDir enables disk-persistent caching for RetryClient.
// SP-Fix-7: Call during startup to enable cross-run cache reuse.
func SetDiskCacheDir(dir string) {
	diskCacheDir = dir
	_ = os.MkdirAll(dir, 0755)
}

func (r *RetryClient) diskCachePath(key string) string {
	if diskCacheDir == "" {
		return ""
	}
	return filepath.Join(diskCacheDir, key+".json")
}

func (r *RetryClient) loadDiskCache(key string) (Response, bool) {
	path := r.diskCachePath(key)
	if path == "" {
		return Response{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Response{}, false
	}
	var entry struct {
		Text         string    `json:"text"`
		FinishReason string    `json:"finish_reason"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return Response{}, false
	}
	if time.Now().After(entry.ExpiresAt) {
		return Response{}, false
	}
	return Response{Text: entry.Text, FinishReason: entry.FinishReason}, true
}

func (r *RetryClient) saveDiskCache(key string, resp Response) {
	path := r.diskCachePath(key)
	if path == "" {
		return
	}
	entry := struct {
		Text         string    `json:"text"`
		FinishReason string    `json:"finish_reason"`
		ExpiresAt    time.Time `json:"expires_at"`
	}{Text: resp.Text, FinishReason: resp.FinishReason, ExpiresAt: time.Now().Add(5 * time.Minute)}
	data, _ := json.Marshal(entry)
	_ = os.WriteFile(path, data, 0644)
}

func (r *RetryClient) Provider() string             { return r.inner.Provider() }
func (r *RetryClient) Supports(cap Capability) bool { return r.inner.Supports(cap) }

// Generate retries transient failures with exponential backoff.
// P1-5a + P1-8: Caches analysis/summary responses for 5 min TTL.
func (r *RetryClient) Generate(ctx context.Context, request Request) (Response, error) {
	// P1-8: Check prompt-level cache for cacheable request types.
	cacheable := request.Category == CategoryAnalysis || request.Category == CategorySummary
	var cacheKey string
	if cacheable {
		cacheKey = promptCacheKey(request)
		r.mu.RLock()
		if cached, ok := r.cache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
			r.mu.RUnlock()
			return cached.resp, nil
		}
		r.mu.RUnlock()
		// SP-Fix-7: Try disk cache if memory cache misses (cross-run reuse).
		if diskResp, ok := r.loadDiskCache(cacheKey); ok {
			r.mu.Lock()
			r.cache[cacheKey] = cachedResponse{resp: diskResp, expiresAt: time.Now().Add(5 * time.Minute)}
			r.mu.Unlock()
			return diskResp, nil
		}
	}

	resp, err := r.generateWithRetry(ctx, r.inner, request)
	if err == nil {
		if cacheable {
			r.mu.Lock()
			r.cache[cacheKey] = cachedResponse{resp: resp, expiresAt: time.Now().Add(5 * time.Minute)}
			r.mu.Unlock()
			r.saveDiskCache(cacheKey, resp)
		}
		return resp, nil
	}

	// S5.4: Real fallback_providers switch chain (not just error text).
	primaryErr := err
	for i, fb := range r.fallbacks {
		if fb == nil {
			continue
		}
		fbResp, fbErr := r.generateWithRetry(ctx, fb, request)
		if fbErr == nil {
			fbResp.Fallback = true
			if cacheable {
				r.mu.Lock()
				r.cache[cacheKey] = cachedResponse{resp: fbResp, expiresAt: time.Now().Add(5 * time.Minute)}
				r.mu.Unlock()
				r.saveDiskCache(cacheKey, fbResp)
			}
			return fbResp, nil
		}
		primaryErr = fmt.Errorf("%w; fallback[%d] %s: %v", primaryErr, i, fb.Provider(), fbErr)
	}
	if len(r.fallbacks) == 0 && HasFallbackProviders() {
		primaryErr = fmt.Errorf("%w (fallback_providers configured but none constructed — check API keys)", primaryErr)
	}
	if len(r.fallbacks) == 0 && !HasFallbackProviders() {
		primaryErr = AnnotatePrimaryFailureWithoutFallbacks(primaryErr)
	}
	return Response{}, primaryErr
}

// generateWithRetry runs the inner client with transient retry/backoff.
func (r *RetryClient) generateWithRetry(ctx context.Context, client Client, request Request) (Response, error) {
	const maxRetries = 1
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := client.Generate(ctx, request)
		// Tool calls produce empty Text but valid output — treat as success.
		hasOutput := resp.Text != "" || len(resp.ToolCalls) > 0
		if err == nil && !resp.Fallback && hasOutput {
			return resp, nil
		}
		lastErr = err
		if err == nil {
			if resp.Fallback {
				if !HasFallbackProviders() && len(r.fallbacks) == 0 {
					lastErr = fmt.Errorf("llm: fallback returned (no fallback providers configured — add fallback_providers to agent.yaml llm_defaults)")
				} else {
					lastErr = fmt.Errorf("llm: fallback returned (deterministic fallback)")
				}
			} else {
				lastErr = fmt.Errorf("llm: empty response text returned")
			}
			if len(request.Tools) > 0 {
				errStr := lastErr.Error()
				if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") ||
					strings.Contains(errStr, "500") || strings.Contains(errStr, "502") ||
					strings.Contains(errStr, "503") || strings.Contains(errStr, "server error") {
					// Transient — safe to retry, no tools were executed.
				} else {
					return Response{}, lastErr
				}
			}
			break
		}
		errStr := err.Error()

		if strings.Contains(errStr, "401") || strings.Contains(errStr, "403") ||
			strings.Contains(errStr, "invalid api key") || strings.Contains(errStr, "unauthorized") {
			return Response{}, err
		}

		if len(request.Tools) > 0 {
			if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") ||
				strings.Contains(errStr, "500") || strings.Contains(errStr, "502") ||
				strings.Contains(errStr, "503") || strings.Contains(errStr, "server error") {
				// Transient — safe to retry.
			} else {
				return Response{}, err
			}
		}

		if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") {
			if attempt < maxRetries {
				continue
			}
		}

		if strings.Contains(errStr, "500") || strings.Contains(errStr, "502") ||
			strings.Contains(errStr, "503") || strings.Contains(errStr, "server error") {
			if attempt < 2 {
				continue
			}
		}

		if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") {
			if attempt < 1 {
				continue
			}
		}

		break
	}

	return Response{}, lastErr
}

func promptCacheKey(request Request) string {
	var b strings.Builder
	b.WriteString(request.SystemPrompt)
	b.WriteByte(0)
	b.WriteString(request.UserPrompt)
	b.WriteByte(0)
	b.WriteString(string(request.Category))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(request.Model))
	b.WriteByte(0)
	if len(request.Tools) > 0 {
		names := make([]string, 0, len(request.Tools))
		for _, td := range request.Tools {
			names = append(names, td.Name)
		}
		b.WriteString(strings.Join(names, ","))
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])[:16]
}

// TimeoutForRequest returns an appropriate timeout based on request type.
// T1: Reads from global timeout config; falls back to previous hardcoded values.
func TimeoutForRequest(req Request) time.Duration {
	cfg := TimeoutConfigOrDefault()
	switch req.Category {
	case CategoryCodeGeneration:
		return cfg.CodeGen
	case CategorySummary:
		return cfg.Summary
	default:
		return cfg.DefaultLLM
	}
}

// StandardReadOnlyTools returns tools that cannot execute a shell.
// shell_done is intentionally excluded: it runs arbitrary commands and is not read-only.
func StandardReadOnlyTools() []ToolDefinition {
	all := StandardCodeTools()
	var result []ToolDefinition
	for _, td := range all {
		if td.Name == "read_file" {
			result = append(result, td)
		}
	}
	return result
}
