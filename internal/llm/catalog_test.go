// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package llm

import "testing"

func TestLookupProvider_OpenRouter(t *testing.T) {
	provider, ok := LookupProvider("openrouter")
	if !ok {
		t.Fatal("expected openrouter provider info")
	}
	if provider.Family != "openai-compatible" {
		t.Fatalf("expected openai-compatible family, got %q", provider.Family)
	}
	if provider.DefaultBaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("unexpected openrouter base url %q", provider.DefaultBaseURL)
	}
	if !provider.SupportsWebSearch {
		t.Fatal("expected openrouter web search support")
	}
}

func TestLookupProvider_Google(t *testing.T) {
	provider, ok := LookupProvider("google")
	if !ok {
		t.Fatal("expected google provider info")
	}
	if provider.DefaultModel != defaultGoogleModel {
		t.Fatalf("unexpected google default model %q", provider.DefaultModel)
	}
	if !provider.SupportsWebSearch {
		t.Fatal("expected google provider info to expose web search support")
	}
}

func TestResolveProviderStatus_DefaultConfig(t *testing.T) {
	status := ResolveProviderStatus(DefaultConfig())
	if status.Name != "google" {
		t.Fatalf("expected default provider google, got %q", status.Name)
	}
	if status.Model != defaultGoogleModel {
		t.Fatalf("expected default model %q, got %q", defaultGoogleModel, status.Model)
	}
	if !status.WebSearchEnabled {
		t.Fatal("expected default config web search to be enabled")
	}
}
