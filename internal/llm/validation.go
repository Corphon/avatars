package llm

import "strings"

func ValidateConfig(config Config) error {
	provider := normalizeProviderName(config.Provider)
	model := strings.TrimSpace(config.Model)
	if provider == "" || usesGenkitProvider(provider, model) {
		// Genkit key checks happen in NewClient (S5.4 construct-time).
		// Config load must still succeed so REPL can start and report status.
		return nil
	}
	client, err := NewOpenAICompatibleClient(OpenAICompatibleConfig{
		Provider:        provider,
		Model:           model,
		EnableWebSearch: config.EnableWebSearch,
		ThinkMode:       config.ThinkMode,
		APIKey:          strings.TrimSpace(config.APIKey),
		APIKeyEnv:       strings.TrimSpace(config.APIKeyEnv),
		BaseURL:         strings.TrimSpace(config.BaseURL),
		Timeout:         config.Timeout,
	})
	if err != nil {
		return err
	}
	// Structural validation only (base_url / model). Missing API key is deferred
	// to NewClient construct-time failure (S5.4) so loadLLMConfig still works.
	_, err = client.resolveRequestConfig()
	return err
}
