package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"avatars/internal/llm"
	"avatars/internal/mcp"
	"avatars/internal/runtime"

	"gopkg.in/yaml.v3"
)

const defaultAgentConfigPath = "configs/agent.yaml"

type agentConfigFile struct {
	DefaultModel string                `yaml:"default_model"`
	LLM          agentLLMSettings      `yaml:"llm"`
	LLMDefaults  agentLLMDefaults      `yaml:"llm_defaults"`
	MCP          agentMCPSettings      `yaml:"mcp"`
	Skills       agentSkillsSettings   `yaml:"skills"`
	Workflow     agentWorkflowSettings `yaml:"workflow"`
	Features     agentFeaturesSettings `yaml:"features"`
	ModelRouting map[string]string     `yaml:"model_routing"`
}

// agentFeaturesSettings: only Memory is a real runtime gate (SQLite hot-memory).
// Stage and Skills keys are accepted for backward compatibility but ignored;
// stage/serve and the skill store always load (B3).
type agentFeaturesSettings struct {
	Stage  bool `yaml:"stage"`
	Skills bool `yaml:"skills"`
	Memory bool `yaml:"memory"`
}

type agentWorkflowSettings struct {
	AutoConfirm bool `yaml:"auto_confirm"`
}

type agentLLMDefaults struct {
	MaxTokens          int            `yaml:"max_tokens"`
	Temperature        float64        `yaml:"temperature"`
	TopP               float64        `yaml:"top_p"`
	StructuredOutput   bool           `yaml:"structured_output"`
	TimeoutMs          int            `yaml:"timeout_ms"`
	ShellDoneTimeoutMs int            `yaml:"shell_done_timeout_ms"`
	FallbackProviders  []string       `yaml:"fallback_providers"`
	MaxToolTurnsByRole map[string]int `yaml:"max_tool_turns_by_role"`
	MaxParallelAvatars int            `yaml:"max_parallel_avatars"`
}

type agentSkillsSettings struct {
	AutoApproveGenerated bool `yaml:"auto_approve_generated"`
}

type agentLLMSettings struct {
	ActiveProvider  string                      `yaml:"active_provider"`
	Provider        string                      `yaml:"provider"`
	Model           string                      `yaml:"model"`
	ClassifierModel string                      `yaml:"classifier_model"` // NL7: separate model for NL intent routing
	EnableWebSearch *bool                       `yaml:"enable_web_search"`
	ThinkMode       string                      `yaml:"think_mode"`
	APIKey          string                      `yaml:"api_key"`
	APIKeyEnv       string                      `yaml:"api_key_env"`
	BaseURL         string                      `yaml:"base_url"`
	TimeoutMillis   int                         `yaml:"timeout_ms"`
	Providers       map[string]agentLLMSettings `yaml:"providers"`
}

type agentMCPSettings struct {
	Servers map[string]agentMCPServerSettings `yaml:"servers"`
}

type agentMCPServerSettings struct {
	Transport   string            `yaml:"transport"`
	Command     string            `yaml:"command"`
	Args        []string          `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	URL         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	TimeoutMs   int               `yaml:"timeout_ms"`
	Description string            `yaml:"description"`
}

type MCPServerConfig struct {
	Name        string
	Transport   string
	Command     string
	Args        []string
	Env         map[string]string
	URL         string
	Headers     map[string]string
	Timeout     time.Duration
	Description string
}

func (c MCPServerConfig) Spec() mcp.ServerSpec {
	args := append([]string(nil), c.Args...)
	return mcp.ServerSpec{
		Name:        c.Name,
		Transport:   c.Transport,
		Command:     c.Command,
		Args:        args,
		Env:         cloneStringMap(c.Env),
		URL:         c.URL,
		Headers:     cloneStringMap(c.Headers),
		Timeout:     c.Timeout,
		Description: c.Description,
	}
}

// PersonalityConfig is loaded from configs/personality.yaml and injected
// into the LLM system prompt so every run carries the operator's context.

func LoadPersonalityConfig() runtime.PersonalityConfig {
	cfg := runtime.PersonalityConfig{Style: "balanced", Safety: "standard"}
	data, err := os.ReadFile("configs/personality.yaml")
	if err != nil {
		return cfg
	}
	type wrapper struct {
		Personality runtime.PersonalityConfig `yaml:"personality"`
	}
	var w wrapper
	if err := yaml.Unmarshal(data, &w); err != nil {
		return cfg
	}
	if w.Personality.Style != "" {
		cfg.Style = w.Personality.Style
	}
	if w.Personality.Language != "" {
		cfg.Language = w.Personality.Language
	}
	if w.Personality.Safety != "" {
		cfg.Safety = w.Personality.Safety
	}
	if w.Personality.OperatorContext != "" {
		cfg.OperatorContext = w.Personality.OperatorContext
	}
	return cfg
}

// WritePersonalityConfig updates a single key in configs/personality.yaml.
// Supported keys: style, language, safety, operator_context.
func WritePersonalityConfig(key string, value string) error {
	cfg := LoadPersonalityConfig()
	trimmedKey := strings.TrimSpace(strings.ToLower(key))
	trimmedValue := strings.TrimSpace(value)
	switch trimmedKey {
	case "style":
		if trimmedValue != "concise" && trimmedValue != "detailed" && trimmedValue != "balanced" {
			return fmt.Errorf("style must be one of: concise, detailed, balanced")
		}
		cfg.Style = trimmedValue
	case "language":
		cfg.Language = trimmedValue
	case "safety":
		if trimmedValue != "strict" && trimmedValue != "standard" && trimmedValue != "permissive" {
			return fmt.Errorf("safety must be one of: strict, standard, permissive")
		}
		cfg.Safety = trimmedValue
	case "operator_context":
		cfg.OperatorContext = trimmedValue
	default:
		return fmt.Errorf("unknown config key %q; supported keys: style, language, safety, operator_context", key)
	}
	// Write back to config file using yaml
	type wrapper struct {
		Personality runtime.PersonalityConfig `yaml:"personality"`
	}
	w := wrapper{Personality: cfg}
	data, err := yaml.Marshal(&w)
	if err != nil {
		return fmt.Errorf("marshal personality config: %w", err)
	}
	if err := os.WriteFile("configs/personality.yaml", data, 0o644); err != nil {
		return fmt.Errorf("write personality config: %w", err)
	}
	fmt.Printf("Config %s set to: %s\n", key, trimmedValue)
	return nil
}

func loadLLMConfig(configPath string) (llm.Config, error) {
	config := llm.DefaultConfig()
	parsed, err := loadAgentConfigFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return llm.Config{}, err
	}
	resolvedLLM, err := parsed.LLM.resolveSettings()
	if err != nil {
		return llm.Config{}, err
	}

	if provider := strings.TrimSpace(resolvedLLM.Provider); provider != "" {
		config.Provider = provider
	}
	activeProvider := normalizeProviderProfileKey(parsed.LLM.ActiveProvider)
	if model := strings.TrimSpace(resolvedLLM.Model); model != "" {
		config.Model = model
	} else if activeProvider == "" {
		if model := strings.TrimSpace(parsed.DefaultModel); model != "" {
			config.Model = model
		}
	} else {
		config.Model = ""
	}
	if resolvedLLM.EnableWebSearch != nil {
		config.EnableWebSearch = *resolvedLLM.EnableWebSearch
	}
	if thinkMode := strings.TrimSpace(resolvedLLM.ThinkMode); thinkMode != "" {
		config.ThinkMode = llm.ThinkMode(thinkMode)
	}
	if apiKey := strings.TrimSpace(resolvedLLM.APIKey); apiKey != "" {
		config.APIKey = apiKey
	}
	if apiKeyEnv := strings.TrimSpace(resolvedLLM.APIKeyEnv); apiKeyEnv != "" {
		config.APIKeyEnv = apiKeyEnv
	}
	if baseURL := strings.TrimSpace(resolvedLLM.BaseURL); baseURL != "" {
		config.BaseURL = baseURL
	}
	if timeoutMillis := resolvedLLM.TimeoutMillis; timeoutMillis > 0 {
		config.Timeout = time.Duration(timeoutMillis) * time.Millisecond
	}
	if err := llm.ValidateConfig(config); err != nil {
		if activeProvider != "" {
			return llm.Config{}, fmt.Errorf("llm active_provider %q is invalid: %w", parsed.LLM.ActiveProvider, err)
		}
		return llm.Config{}, err
	}

	// T1+L2: Load timeout config into global singleton so all packages
	// can read timeouts without creating an import cycle with this package.
	loadGlobalTimeoutConfig(&parsed)

	// G2: Load fallback providers from agent.yaml llm_defaults.
	if len(parsed.LLMDefaults.FallbackProviders) > 0 {
		config.FallbackProviders = parsed.LLMDefaults.FallbackProviders
		llm.GlobalFallbackProviders = parsed.LLMDefaults.FallbackProviders
	}

	// S5.6: Load max_tokens into global so Builder / tool_loop share one source.
	if parsed.LLMDefaults.MaxTokens > 0 {
		llm.GlobalDefaultMaxTokens = parsed.LLMDefaults.MaxTokens
	}

	// SP-Fix-3: Load per-role max tool turns.
	if len(parsed.LLMDefaults.MaxToolTurnsByRole) > 0 {
		config.MaxToolTurnsByRole = parsed.LLMDefaults.MaxToolTurnsByRole
		llm.GlobalMaxToolTurnsByRole = parsed.LLMDefaults.MaxToolTurnsByRole
	}

	// A2: Load max parallel avatars from config.
	if v := parsed.LLMDefaults.MaxParallelAvatars; v > 0 {
		runtime.SetMaxParallelAvatars(v)
	}

	return config, nil
}

// loadGlobalTimeoutConfig populates llm.GlobalTimeoutConfig from agent.yaml
// llm_defaults section. Called once during startup via loadLLMConfig.
func loadGlobalTimeoutConfig(parsed *agentConfigFile) {
	cfg := llm.DefaultTimeoutConfig()
	d := parsed.LLMDefaults

	if d.TimeoutMs > 0 {
		base := time.Duration(d.TimeoutMs) * time.Millisecond
		cfg.LLMClassify = base
		cfg.CodeGen = base
		cfg.Summary = base / 2
		if cfg.Summary < 30*time.Second {
			cfg.Summary = 30 * time.Second
		}
		cfg.DefaultLLM = base / 4
		if cfg.DefaultLLM < 10*time.Second {
			cfg.DefaultLLM = 10 * time.Second
		}
		cfg.PlanReview = base / 2
		if cfg.PlanReview < 30*time.Second {
			cfg.PlanReview = 30 * time.Second
		}
		// Workflow doc fills (ConstructPlan/Expand) need the full HTTP budget —
		// do not halve. Floor 180s so short timeout_ms cannot recreate the
		// flash-style "60s of reasoning, zero content" failure mode.
		cfg.PlanAutoFill = base
		if cfg.PlanAutoFill < 180*time.Second {
			cfg.PlanAutoFill = 180 * time.Second
		}
	}

	// S5.7 / M-15: Configurable shell_done timeout (default 30s preserved).
	if d.ShellDoneTimeoutMs > 0 {
		cfg.ShellDone = time.Duration(d.ShellDoneTimeoutMs) * time.Millisecond
	}

	llm.GlobalTimeoutConfig = &cfg
}

func LoadMCPServerConfigs(configPath string) ([]MCPServerConfig, error) {
	parsed, err := loadAgentConfigFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []MCPServerConfig{}, nil
		}
		return nil, err
	}
	servers := make([]MCPServerConfig, 0, len(parsed.MCP.Servers))
	for name, entry := range parsed.MCP.Servers {
		cfg := parseMCPServerConfig(name, entry)
		if cfg.Name == "" || !cfg.Spec().Configured() {
			continue
		}
		servers = append(servers, cfg)
	}
	sort.Slice(servers, func(i int, j int) bool {
		return normalizeNamedConfigKey(servers[i].Name) < normalizeNamedConfigKey(servers[j].Name)
	})
	return servers, nil
}

func LoadSkillsAutoApprove(configPath string) bool {
	parsed, err := loadAgentConfigFile(configPath)
	if err != nil {
		return false
	}
	return parsed.Skills.AutoApproveGenerated
}

// LoadFeaturesMemory returns whether SQLite hot-memory should be created and
// injected. Default false when the key is absent (matches agent.yaml product
// posture until operators opt in). S3.1.
func LoadFeaturesMemory(configPath string) bool {
	parsed, err := loadAgentConfigFile(configPath)
	if err != nil {
		return false
	}
	return parsed.Features.Memory
}

func ResolveMCPServerTarget(configPath string, target string) (MCPServerConfig, bool, error) {
	trimmedTarget := strings.TrimSpace(target)
	if trimmedTarget == "" {
		return MCPServerConfig{}, false, fmt.Errorf("mcp server target cannot be empty")
	}
	servers, err := LoadMCPServerConfigs(configPath)
	if err != nil {
		return MCPServerConfig{}, false, err
	}
	normalizedTarget := normalizeNamedConfigKey(trimmedTarget)
	for _, server := range servers {
		if normalizeNamedConfigKey(server.Name) != normalizedTarget {
			continue
		}
		return server, true, nil
	}
	return MCPServerConfig{Name: trimmedTarget, URL: trimmedTarget, Transport: inferMCPTransport("", "", trimmedTarget)}, false, nil
}

func parseMCPServerConfig(name string, entry agentMCPServerSettings) MCPServerConfig {
	trimmedName := strings.TrimSpace(name)
	args := make([]string, 0, len(entry.Args))
	for _, arg := range entry.Args {
		args = append(args, expandEnvRefs(arg))
	}
	cfg := MCPServerConfig{
		Name:        trimmedName,
		Transport:   inferMCPTransport(entry.Transport, expandEnvRefs(entry.Command), expandEnvRefs(entry.URL)),
		Command:     expandEnvRefs(entry.Command),
		Args:        args,
		Env:         expandStringMap(entry.Env),
		URL:         expandEnvRefs(entry.URL),
		Headers:     expandStringMap(entry.Headers),
		Description: strings.TrimSpace(entry.Description),
	}
	if entry.TimeoutMs > 0 {
		cfg.Timeout = time.Duration(entry.TimeoutMs) * time.Millisecond
	}
	return cfg
}

func inferMCPTransport(transport string, command string, rawURL string) string {
	trimmed := strings.ToLower(strings.TrimSpace(transport))
	if trimmed == "stdio" || trimmed == "http" || trimmed == "https" {
		if trimmed == "https" {
			return "http"
		}
		return trimmed
	}
	if strings.TrimSpace(command) != "" {
		return "stdio"
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "stdio:") {
		return "stdio"
	}
	return "http"
}

func expandEnvRefs(value string) string {
	return os.Expand(strings.TrimSpace(value), os.Getenv)
}

func expandStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		out[trimmedKey] = expandEnvRefs(value)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// LoadClassifierModel reads the agent config and returns the classifier_model
// override if set. Returns empty string if not configured, so callers fall
// back to the default model.
func LoadClassifierModel() string {
	parsed, err := loadAgentConfigFile(ResolveRuntimePath(defaultAgentConfigPath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.LLM.ClassifierModel)
}

// LoadWorkflowAutoConfirm returns workflow.auto_confirm from agent.yaml.
// Default false (S2.8): first-run plan should be reviewable unless explicitly enabled.
func LoadWorkflowAutoConfirm() bool {
	parsed, err := loadAgentConfigFile(ResolveRuntimePath(defaultAgentConfigPath))
	if err != nil {
		return false
	}
	return parsed.Workflow.AutoConfirm
}

// LoadModelRouting returns the model_routing map from agent.yaml.
// Keys are category names (classifier, code_generation, analysis, summary),
// values are model identifiers. Used to select different models for
// different task types.
func LoadModelRouting() map[string]string {
	parsed, err := loadAgentConfigFile(ResolveRuntimePath(defaultAgentConfigPath))
	if err != nil {
		return nil
	}
	routing := make(map[string]string, len(parsed.ModelRouting))
	for k, v := range parsed.ModelRouting {
		if strings.TrimSpace(v) != "" {
			routing[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return routing
}

// ResolveModelForCategory returns the model to use for a given LLM request
// category, based on the model_routing config. Returns empty string if no
// override is configured, meaning the default model should be used.
func ResolveModelForCategory(category string) string {
	routing := LoadModelRouting()
	if routing == nil {
		return ""
	}
	return routing[strings.ToLower(strings.TrimSpace(category))]
}

func loadAgentConfigFile(configPath string) (agentConfigFile, error) {
	resolvedPath := strings.TrimSpace(configPath)
	if resolvedPath == "" {
		resolvedPath = ResolveRuntimePath(defaultAgentConfigPath)
	} else if !filepath.IsAbs(resolvedPath) {
		resolvedPath = ResolveRuntimePath(resolvedPath)
	}
	content, err := os.ReadFile(filepath.Clean(resolvedPath))
	if err != nil {
		return agentConfigFile{}, err
	}
	var parsed agentConfigFile
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		return agentConfigFile{}, err
	}
	return parsed, nil
}

func (settings agentLLMSettings) resolveSettings() (agentLLMSettings, error) {
	activeProvider := normalizeProviderProfileKey(settings.ActiveProvider)
	if activeProvider == "" {
		return settings.baseSettings(), nil
	}
	if len(settings.Providers) == 0 {
		return agentLLMSettings{}, fmt.Errorf("llm active_provider %q requires a matching providers entry", settings.ActiveProvider)
	}
	var profile agentLLMSettings
	matched := false
	for key, candidate := range settings.Providers {
		if normalizeProviderProfileKey(key) != activeProvider {
			continue
		}
		profile = candidate
		matched = true
		break
	}
	if !matched {
		return agentLLMSettings{}, fmt.Errorf("llm active_provider %q does not match any configured provider profile", settings.ActiveProvider)
	}
	base := settings.baseSettings()
	overlay := profile.baseSettings()
	if strings.TrimSpace(overlay.Provider) == "" {
		overlay.Provider = activeProvider
	}
	// Selecting a named profile must not keep another provider's host/key-env
	// from top-level llm.base_url (e.g. DeepSeek leftovers when switching to Agnes).
	// Keep a top-level plaintext api_key when it was not bound to the other
	// provider's env — that is how operators try a new profile locally.
	if !sameLLMProvider(base.Provider, overlay.Provider) {
		base.BaseURL = ""
		oldEnv := strings.TrimSpace(base.APIKeyEnv)
		newEnv := strings.TrimSpace(overlay.APIKeyEnv)
		if oldEnv != "" && newEnv != "" && !strings.EqualFold(oldEnv, newEnv) {
			base.APIKey = ""
		}
		base.APIKeyEnv = ""
	}
	resolved := mergeLLMSettings(base, overlay)
	if strings.TrimSpace(resolved.Provider) == "" {
		resolved.Provider = activeProvider
	}
	return resolved, nil
}

func sameLLMProvider(a, b string) bool {
	left := normalizeProviderProfileKey(a)
	right := normalizeProviderProfileKey(b)
	return left != "" && left == right
}

func (settings agentLLMSettings) baseSettings() agentLLMSettings {
	return agentLLMSettings{
		Provider:        settings.Provider,
		Model:           settings.Model,
		EnableWebSearch: settings.EnableWebSearch,
		ThinkMode:       settings.ThinkMode,
		APIKey:          settings.APIKey,
		APIKeyEnv:       settings.APIKeyEnv,
		BaseURL:         settings.BaseURL,
		TimeoutMillis:   settings.TimeoutMillis,
	}
}

func mergeLLMSettings(base agentLLMSettings, overlay agentLLMSettings) agentLLMSettings {
	merged := base
	if provider := strings.TrimSpace(overlay.Provider); provider != "" {
		merged.Provider = provider
	}
	if model := strings.TrimSpace(overlay.Model); model != "" {
		merged.Model = model
	}
	if overlay.EnableWebSearch != nil {
		merged.EnableWebSearch = overlay.EnableWebSearch
	}
	if thinkMode := strings.TrimSpace(overlay.ThinkMode); thinkMode != "" {
		merged.ThinkMode = thinkMode
	}
	if apiKey := strings.TrimSpace(overlay.APIKey); apiKey != "" {
		merged.APIKey = apiKey
	}
	if apiKeyEnv := strings.TrimSpace(overlay.APIKeyEnv); apiKeyEnv != "" {
		merged.APIKeyEnv = apiKeyEnv
	}
	if baseURL := strings.TrimSpace(overlay.BaseURL); baseURL != "" {
		merged.BaseURL = baseURL
	}
	if overlay.TimeoutMillis > 0 {
		merged.TimeoutMillis = overlay.TimeoutMillis
	}
	return merged
}

func normalizeProviderProfileKey(value string) string {
	return normalizeNamedConfigKey(value)
}

func normalizeNamedConfigKey(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	return normalized
}
