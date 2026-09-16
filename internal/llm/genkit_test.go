// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses only local fallback behavior.
package llm

import (
	"context"
	"strings"
	"testing"
)

func TestGenkitClientGenerate_ErrorsWithoutGoogleAICredentials(t *testing.T) {
	// P13-5: missing credentials should return error, not placeholder text.
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	client := NewGenkitClient(GenkitConfig{Model: "gemini-2.5-flash", EnableWebSearch: true})
	_, err := client.Generate(context.Background(), Request{
		SystemPrompt: "You are the runtime synthesizer.",
		UserPrompt:   "Summarize the planning outcome and mention the bootstrap read.",
	})
	if err == nil {
		t.Fatal("expected error for missing credentials, got nil")
	}
	if !strings.Contains(err.Error(), "runtime not initialized") {
		t.Fatalf("expected runtime error, got: %v", err)
	}
}

func TestGenkitClientGenerate_UsesDeterministicModelFallback(t *testing.T) {
	// bootstrap-deterministic model intentionally uses fallback for initialization.
	t.Setenv("GEMINI_API_KEY", "placeholder")
	t.Setenv("GOOGLE_API_KEY", "")

	client := NewGenkitClient(GenkitConfig{Model: "bootstrap-deterministic", EnableWebSearch: true})
	response, err := client.Generate(context.Background(), Request{UserPrompt: "Keep the existing deterministic bootstrap flow stable."})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if response.Model != "bootstrap-deterministic" {
		t.Fatalf("expected deterministic model to stay unchanged, got %q", response.Model)
	}
	if !response.Fallback {
		t.Fatal("expected deterministic model to force fallback")
	}
	if client.runtime != nil {
		t.Fatal("did not expect live runtime initialization for deterministic model")
	}
}

func TestGenkitClientGenerate_ErrorsWithoutAnthropicCredentials(t *testing.T) {
	// P13-5 / S5.4: missing credentials fail at construct (not Generate).
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := NewClient(Config{Provider: "anthropic", Model: DefaultConfig().Model})
	if err == nil {
		t.Fatal("expected construct error for missing anthropic credentials, got nil")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Fatalf("expected API key error, got: %v", err)
	}
}

func TestGenkitClientGenerate_ErrorsWithoutOpenAICredentials(t *testing.T) {
	// P13-5 / S5.4: missing credentials fail at construct.
	t.Setenv("OPENAI_API_KEY", "")

	_, err := NewClient(Config{Provider: "openai", Model: DefaultConfig().Model})
	if err == nil {
		t.Fatal("expected construct error for missing openai credentials, got nil")
	}
}

func TestNewClient_ErrorsWithoutDeepseekCredentials(t *testing.T) {
	// P13-5 / S5.4: missing credentials fail at construct, not soft Generate fallback.
	// Z1: deepseek also RequiresExplicitModel — pass a model so the error is about API key.
	t.Setenv("DEEPSEEK_API_KEY", "")
	_, err := NewClient(Config{Provider: "deepseek", Model: "deepseek-v4-flash"})
	if err == nil {
		t.Fatal("expected construct error for missing deepseek credentials, got nil")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Fatalf("expected API key error, got: %v", err)
	}
}

func TestEffectiveThinkMode_DisablesAutoForStructuredOutput(t *testing.T) {
	mode := effectiveThinkMode(ThinkModeAuto, Request{StructuredOutput: true})
	if mode != ThinkModeOff {
		t.Fatalf("expected structured output to force off in auto mode, got %q", mode)
	}

	mode = effectiveThinkMode(ThinkModeOn, Request{StructuredOutput: true})
	if mode != ThinkModeOn {
		t.Fatalf("expected explicit on to be preserved, got %q", mode)
	}
}

// F32: Builder sets Request.ThinkMode=Off; must beat yaml config Auto (DeepSeek V4).
func TestEffectiveThinkMode_RequestOffBeatsConfigAuto(t *testing.T) {
	mode := effectiveThinkMode(ThinkModeAuto, Request{ThinkMode: ThinkModeOff, Category: CategoryCodeGeneration})
	if mode != ThinkModeOff {
		t.Fatalf("request Off must beat config Auto, got %q", mode)
	}
}
