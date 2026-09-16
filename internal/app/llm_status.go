package app

import "avatars/internal/llm"

func CurrentLLMStatus(configPath string) (llm.ProviderStatus, error) {
	config, err := loadLLMConfig(configPath)
	if err != nil {
		return llm.ProviderStatus{}, err
	}
	return llm.ResolveProviderStatus(config), nil
}
