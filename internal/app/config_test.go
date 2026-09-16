// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadLLMConfig_UsesDefaultsWhenConfigMissing(t *testing.T) {
	config, err := loadLLMConfig(filepath.Join(t.TempDir(), "missing-agent.yaml"))
	if err != nil {
		t.Fatalf("loadLLMConfig failed: %v", err)
	}
	if config.Provider != "genkit" {
		t.Fatalf("expected default provider genkit, got %q", config.Provider)
	}
	if config.Model != "googleai/gemini-2.5-flash" {
		t.Fatalf("expected default model, got %q", config.Model)
	}
	if config.ThinkMode != "auto" {
		t.Fatalf("expected default think mode auto, got %q", config.ThinkMode)
	}
}

func TestLoadLLMConfig_ParsesOverrides(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("default_model: qwen-plus\nllm:\n  provider: qwen\n  enable_web_search: false\n  think_mode: off\n  timeout_ms: 45000\n  api_key_env: DASHSCOPE_API_KEY\n  base_url: https://dashscope.aliyuncs.com/compatible-mode/v1\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	config, err := loadLLMConfig(configPath)
	if err != nil {
		t.Fatalf("loadLLMConfig failed: %v", err)
	}
	if config.Provider != "qwen" {
		t.Fatalf("expected provider qwen, got %q", config.Provider)
	}
	if config.Model != "qwen-plus" {
		t.Fatalf("expected model qwen-plus, got %q", config.Model)
	}
	if config.EnableWebSearch {
		t.Fatal("expected enable_web_search override to disable web search")
	}
	if config.ThinkMode != "off" {
		t.Fatalf("expected think mode off, got %q", config.ThinkMode)
	}
	if config.Timeout != 45*time.Second {
		t.Fatalf("expected timeout 45s, got %s", config.Timeout)
	}
	if config.APIKeyEnv != "DASHSCOPE_API_KEY" {
		t.Fatalf("expected api key env override, got %q", config.APIKeyEnv)
	}
	if config.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("expected base URL override, got %q", config.BaseURL)
	}
}

func TestLoadLLMConfig_SelectsActiveProviderProfile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  active_provider: deepseek\n  think_mode: off\n  providers:\n    deepseek:\n      model: deepseek-reasoner\n      api_key_env: DEEPSEEK_API_KEY\n      base_url: https://api.deepseek.com\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	config, err := loadLLMConfig(configPath)
	if err != nil {
		t.Fatalf("loadLLMConfig failed: %v", err)
	}
	if config.Provider != "deepseek" {
		t.Fatalf("expected provider deepseek, got %q", config.Provider)
	}
	if config.Model != "deepseek-reasoner" {
		t.Fatalf("expected selected profile model, got %q", config.Model)
	}
	if config.ThinkMode != "off" {
		t.Fatalf("expected inherited think mode off, got %q", config.ThinkMode)
	}
	if config.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("expected selected profile api key env, got %q", config.APIKeyEnv)
	}
	if config.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("expected selected profile base url, got %q", config.BaseURL)
	}
	if !config.EnableWebSearch {
		t.Fatal("expected default enable_web_search to remain true when profile does not override it")
	}
}

func TestLoadLLMConfig_ErrorsWhenActiveProviderProfileMissing(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  active_provider: missing\n  providers:\n    google:\n      provider: google\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	if _, err := loadLLMConfig(configPath); err == nil {
		t.Fatal("expected missing active provider profile to fail")
	}
}

func TestLoadLLMConfig_ActiveProviderDoesNotInheritOtherHost(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  active_provider: agnes\n  api_key_env: DEEPSEEK_API_KEY\n  base_url: https://api.deepseek.com\n  providers:\n    agnes:\n      provider: agnes\n      model: agnes-2.5-flash\n      api_key_env: AGNES_API_KEY\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	config, err := loadLLMConfig(configPath)
	if err != nil {
		t.Fatalf("loadLLMConfig failed: %v", err)
	}
	if config.Provider != "agnes" {
		t.Fatalf("expected provider agnes, got %q", config.Provider)
	}
	if config.APIKeyEnv != "AGNES_API_KEY" {
		t.Fatalf("expected Agnes key env, got %q", config.APIKeyEnv)
	}
	if config.BaseURL != "" && config.BaseURL != "https://api.agnes-ai.cn/v1" && config.BaseURL != "https://apihub.agnes-ai.cn/v1" && config.BaseURL != "https://apihub.agnes-ai.com/v1" {
		t.Fatalf("must not keep DeepSeek host for Agnes profile, got %q", config.BaseURL)
	}
}

func TestLoadLLMConfig_KeepsTopLevelAPIKeyWhenSwitchingToAgnes(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  active_provider: agnes\n  api_key: sk-agnes-local\n  providers:\n    agnes:\n      provider: agnes\n      model: agnes-2.5-flash\n      api_key_env: AGNES_API_KEY\n      base_url: https://api.agnes-ai.cn/v1\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := loadLLMConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.APIKey != "sk-agnes-local" {
		t.Fatalf("top-level api_key must survive profile switch, got %q", config.APIKey)
	}
	if config.APIKeyEnv != "AGNES_API_KEY" {
		t.Fatalf("profile api_key_env, got %q", config.APIKeyEnv)
	}
	if config.BaseURL != "https://api.agnes-ai.cn/v1" {
		t.Fatalf("agnes base_url, got %q", config.BaseURL)
	}
}

func TestLoadLLMConfig_DoesNotLeakDefaultModelIntoSelectedProfile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("default_model: googleai/gemini-2.5-flash\nllm:\n  active_provider: doubao\n  providers:\n    doubao:\n      provider: doubao\n      api_key_env: ARK_API_KEY\n      base_url: https://ark.cn-beijing.volces.com/api/v3\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := loadLLMConfig(configPath)
	if err == nil {
		t.Fatal("expected doubao profile without explicit model to fail instead of inheriting default_model")
	}
	if !strings.Contains(err.Error(), "model must be set explicitly") {
		t.Fatalf("expected explicit model validation error, got %v", err)
	}
}

func TestLoadLLMConfig_SelectsOpenRouterProfile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  active_provider: openrouter\n  providers:\n    openrouter:\n      provider: openrouter\n      model: openai/gpt-4.1-mini\n      enable_web_search: true\n      api_key_env: OPENROUTER_API_KEY\n      base_url: https://openrouter.ai/api/v1\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	config, err := loadLLMConfig(configPath)
	if err != nil {
		t.Fatalf("loadLLMConfig failed: %v", err)
	}
	if config.Provider != "openrouter" {
		t.Fatalf("expected provider openrouter, got %q", config.Provider)
	}
	if config.Model != "openai/gpt-4.1-mini" {
		t.Fatalf("expected openrouter model, got %q", config.Model)
	}
	if !config.EnableWebSearch {
		t.Fatal("expected openrouter web search to stay enabled")
	}
	if config.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("expected openrouter api key env, got %q", config.APIKeyEnv)
	}
	if config.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("expected openrouter base url, got %q", config.BaseURL)
	}
}

func TestLoadLLMConfig_ErrorsWhenSelectedProfileRequiresExplicitModel(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  active_provider: doubao\n  providers:\n    doubao:\n      provider: doubao\n      api_key_env: ARK_API_KEY\n      base_url: https://ark.cn-beijing.volces.com/api/v3\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := loadLLMConfig(configPath)
	if err == nil {
		t.Fatal("expected doubao profile without model to fail")
	}
	if !strings.Contains(err.Error(), "model must be set explicitly") {
		t.Fatalf("expected explicit model error, got %v", err)
	}
}

func TestLoadLLMConfig_ErrorsWhenSelectedProfileRequiresExplicitBaseURL(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  active_provider: minimax\n  providers:\n    minimax:\n      provider: minimax\n      model: MiniMax-M2.7\n      api_key_env: MINIMAX_API_KEY\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := loadLLMConfig(configPath)
	if err == nil {
		t.Fatal("expected minimax profile without base_url to fail")
	}
	if !strings.Contains(err.Error(), "base_url override is required") {
		t.Fatalf("expected explicit base_url error, got %v", err)
	}
}

func TestLoadLLMConfig_ErrorsWhenFlatCustomCompatibleConfigIsIncomplete(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  provider: custom-compatible\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	_, err := loadLLMConfig(configPath)
	if err == nil {
		t.Fatal("expected incomplete custom-compatible config to fail")
	}
	if !strings.Contains(err.Error(), "base_url override is required") {
		t.Fatalf("expected explicit base_url error, got %v", err)
	}
}

func TestLoadMCPServerConfigs_ReturnsSortedServers(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("mcp:\n  servers:\n    zeta:\n      url: https://zeta.example.com/mcp\n      description: Last server\n    alpha:\n      url: https://alpha.example.com/mcp\n      description: First server\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	servers, err := LoadMCPServerConfigs(configPath)
	if err != nil {
		t.Fatalf("LoadMCPServerConfigs failed: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 mcp servers, got %d", len(servers))
	}
	if servers[0].Name != "alpha" || servers[1].Name != "zeta" {
		t.Fatalf("expected sorted mcp servers, got %+v", servers)
	}
	if servers[0].Description != "First server" {
		t.Fatalf("expected description to round-trip, got %+v", servers[0])
	}
}

func TestResolveMCPServerTarget_ByNameOrURL(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("mcp:\n  servers:\n    repo-inspector:\n      url: https://example.com/mcp\n      description: Demo server\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	resolved, matched, err := ResolveMCPServerTarget(configPath, "repo-inspector")
	if err != nil {
		t.Fatalf("ResolveMCPServerTarget failed: %v", err)
	}
	if !matched {
		t.Fatal("expected configured server name to match")
	}
	if resolved.URL != "https://example.com/mcp" {
		t.Fatalf("expected configured server url, got %+v", resolved)
	}

	raw, matched, err := ResolveMCPServerTarget(configPath, "https://raw.example.com/mcp")
	if err != nil {
		t.Fatalf("ResolveMCPServerTarget raw url failed: %v", err)
	}
	if matched {
		t.Fatal("expected raw url target to bypass configured server match")
	}
	if raw.URL != "https://raw.example.com/mcp" {
		t.Fatalf("expected raw url passthrough, got %+v", raw)
	}
}

func TestLoadMCPServerConfigs_ExpandsEnvAndStdio(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "secret-token")
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("mcp:\n  servers:\n    skipped:\n      url: \"\"\n    files:\n      transport: stdio\n      command: npx\n      args: [\"-y\", \"server\"]\n      description: Disk tools\n    search:\n      url: https://search.example.com/mcp\n      headers:\n        Authorization: Bearer ${MCP_TEST_TOKEN}\n      timeout_ms: 15000\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	servers, err := LoadMCPServerConfigs(configPath)
	if err != nil {
		t.Fatalf("LoadMCPServerConfigs failed: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 enabled servers, got %+v", servers)
	}
	if servers[0].Name != "files" || servers[0].Transport != "stdio" || servers[0].Command != "npx" {
		t.Fatalf("expected stdio files server first, got %+v", servers[0])
	}
	if len(servers[0].Args) != 2 || servers[0].Args[1] != "server" {
		t.Fatalf("expected stdio args, got %+v", servers[0].Args)
	}
	if servers[1].Name != "search" {
		t.Fatalf("expected search server, got %+v", servers[1])
	}
	if servers[1].Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("expected expanded header, got %+v", servers[1].Headers)
	}
	if servers[1].Timeout != 15*time.Second {
		t.Fatalf("expected 15s timeout, got %s", servers[1].Timeout)
	}
	spec := servers[1].Spec()
	if spec.Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("expected spec header copy, got %+v", spec.Headers)
	}
}

func TestLoadSkillsAutoApprove_DefaultFalseWhenMissing(t *testing.T) {
	result := LoadSkillsAutoApprove(filepath.Join(t.TempDir(), "missing-agent.yaml"))
	if result {
		t.Fatal("expected auto-approve to default to false when config file missing")
	}
}

func TestLoadSkillsAutoApprove_DefaultFalseWhenNotSet(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  provider: genkit\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	result := LoadSkillsAutoApprove(configPath)
	if result {
		t.Fatal("expected auto-approve to default to false when skills section absent")
	}
}

func TestLoadSkillsAutoApprove_TrueWhenSet(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  provider: genkit\nskills:\n  auto_approve_generated: true\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	result := LoadSkillsAutoApprove(configPath)
	if !result {
		t.Fatal("expected auto-approve to be true when explicitly set")
	}
}

func TestLoadSkillsAutoApprove_FalseWhenExplicitlyFalse(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "agent.yaml")
	content := []byte("llm:\n  provider: genkit\nskills:\n  auto_approve_generated: false\n")
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	result := LoadSkillsAutoApprove(configPath)
	if result {
		t.Fatal("expected auto-approve to be false when explicitly set to false")
	}
}
