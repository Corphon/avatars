package llm

import "sort"

type ProviderInfo struct {
	Name                    string
	Family                  string
	DefaultModel            string
	DefaultBaseURL          string
	DefaultAPIKeyEnv        string
	SupportsWebSearch       bool
	RequiresExplicitModel   bool
	RequiresExplicitBaseURL bool
	Local                   bool
}

type ProviderStatus struct {
	Name                    string
	Family                  string
	Model                   string
	BaseURL                 string
	APIKeyEnv               string
	TimeoutMillis           int
	SupportsWebSearch       bool
	WebSearchEnabled        bool
	ThinkMode               ThinkMode
	RequiresExplicitModel   bool
	RequiresExplicitBaseURL bool
	Local                   bool
}

func ListProviders() []ProviderInfo {
	providers := []ProviderInfo{
		{
			Name:              "google",
			Family:            "genkit",
			DefaultModel:      defaultModelForProvider("google"),
			DefaultAPIKeyEnv:  "GEMINI_API_KEY or GOOGLE_API_KEY",
			SupportsWebSearch: true,
		},
		{
			Name:             "anthropic",
			Family:           "genkit",
			DefaultModel:     defaultModelForProvider("anthropic"),
			DefaultAPIKeyEnv: "ANTHROPIC_API_KEY",
		},
		{
			Name:             "openai",
			Family:           "genkit",
			DefaultModel:     defaultModelForProvider("openai"),
			DefaultAPIKeyEnv: "OPENAI_API_KEY",
		},
	}
	for _, name := range []string{"agnes", "deepseek", "doubao", "glm", "kimi", "lm-studio", "minimax", "mimo", "mistral", "nvidia", "ollama", "openrouter", "qwen"} {
		if preset, ok := openAICompatiblePresets[name]; ok {
			providers = append(providers, providerInfoFromPreset(preset))
		}
	}
	providers = append(providers, ProviderInfo{
		Name:                    "custom-compatible",
		Family:                  "openai-compatible",
		RequiresExplicitModel:   true,
		RequiresExplicitBaseURL: true,
	})
	sort.Slice(providers, func(i int, j int) bool {
		return providers[i].Name < providers[j].Name
	})
	return providers
}

func LookupProvider(name string) (ProviderInfo, bool) {
	normalized := normalizeProviderName(name)
	for _, provider := range ListProviders() {
		if provider.Name == normalized {
			return provider, true
		}
	}
	return ProviderInfo{}, false
}

func providerInfoFromPreset(preset openAICompatiblePreset) ProviderInfo {
	return ProviderInfo{
		Name:                    preset.Provider,
		Family:                  "openai-compatible",
		DefaultModel:            preset.DefaultModel,
		DefaultBaseURL:          preset.BaseURL,
		DefaultAPIKeyEnv:        preset.APIKeyEnv,
		SupportsWebSearch:       preset.WebSearchMode != webSearchModeNone,
		RequiresExplicitModel:   preset.RequiresExplicitModel,
		RequiresExplicitBaseURL: preset.RequiresExplicitBaseURL,
		Local:                   preset.Local,
	}
}

func ResolveProviderStatus(config Config) ProviderStatus {
	effectiveProvider := resolveConfiguredProvider(config)
	provider, ok := LookupProvider(effectiveProvider)
	if !ok {
		provider = ProviderInfo{Name: effectiveProvider, Family: "unknown"}
	}
	model := config.Model
	if model == "" {
		model = provider.DefaultModel
	}
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = provider.DefaultBaseURL
	}
	apiKeyEnv := config.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = provider.DefaultAPIKeyEnv
	}
	return ProviderStatus{
		Name:                    provider.Name,
		Family:                  provider.Family,
		Model:                   model,
		BaseURL:                 baseURL,
		APIKeyEnv:               apiKeyEnv,
		TimeoutMillis:           int(config.Timeout.Milliseconds()),
		SupportsWebSearch:       provider.SupportsWebSearch,
		WebSearchEnabled:        config.EnableWebSearch && provider.SupportsWebSearch,
		ThinkMode:               normalizeThinkMode(config.ThinkMode),
		RequiresExplicitModel:   provider.RequiresExplicitModel,
		RequiresExplicitBaseURL: provider.RequiresExplicitBaseURL,
		Local:                   provider.Local,
	}
}

func resolveConfiguredProvider(config Config) string {
	provider := normalizeProviderName(config.Provider)
	model := config.Model
	if provider == "" || provider == "genkit" {
		return resolveGenkitProvider(provider, model)
	}
	if resolved := resolveGenkitProvider(provider, model); resolved != "" && usesGenkitProvider(provider, model) {
		return resolved
	}
	return provider
}
