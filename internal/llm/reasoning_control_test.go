// internal/llm/reasoning_control_test.go
package llm

import "testing"

func TestNormalizeReasoningRequest_DisablesByDefault(t *testing.T) {
	model, extra, enabled := NormalizeReasoningRequest("deepseek", "deepseek-reasoner", nil)
	if enabled {
		t.Fatalf("reasoning should be disabled by default")
	}
	if model != "deepseek-chat" {
		t.Fatalf("expected deepseek-chat, got %q", model)
	}
	if extra != nil {
		t.Fatalf("expected nil extra params, got %#v", extra)
	}
}

func TestNormalizeReasoningRequest_AllowsExplicitEnable(t *testing.T) {
	model, extra, enabled := NormalizeReasoningRequest("google", "gemini-2.0-flash-thinking-exp", map[string]interface{}{
		ExtraParamReasoningEnabled: true,
		"candidateCount":           1,
	})
	if !enabled {
		t.Fatalf("reasoning should be enabled when explicitly requested")
	}
	if model != "gemini-2.0-flash-thinking-exp" {
		t.Fatalf("model should remain unchanged, got %q", model)
	}
	if _, exists := extra[ExtraParamReasoningEnabled]; exists {
		t.Fatalf("sentinel flag should be removed from extra params")
	}
	if extra["candidateCount"] != 1 {
		t.Fatalf("unexpected extra params: %#v", extra)
	}
}

func TestApplyReasoningDefaults_GoogleAndQwen(t *testing.T) {
	googleBody := map[string]interface{}{
		"generationConfig": map[string]interface{}{},
	}
	ApplyReasoningDefaults("google", googleBody, "gemini-2.5-flash", false)
	config, _ := googleBody["generationConfig"].(map[string]interface{})
	thinking, _ := config["thinkingConfig"].(map[string]interface{})
	if thinking["thinkingBudget"] != 0 {
		t.Fatalf("expected thinkingBudget=0, got %#v", thinking)
	}

	qwenBody := map[string]interface{}{}
	ApplyReasoningDefaults("qwen", qwenBody, "qwen3-max", false)
	if qwenBody["enable_thinking"] != false {
		t.Fatalf("expected enable_thinking=false, got %#v", qwenBody["enable_thinking"])
	}

	qwenEnabledBody := map[string]interface{}{}
	ApplyReasoningDefaults("qwen", qwenEnabledBody, "qwen3-max", true)
	if _, exists := qwenEnabledBody["enable_thinking"]; exists {
		t.Fatalf("explicitly enabled reasoning should not be overwritten")
	}

	nvidiaBody := map[string]interface{}{}
	ApplyReasoningDefaults("nvidia", nvidiaBody, "moonshotai/kimi-k2.5", false)
	kwargs, _ := nvidiaBody["chat_template_kwargs"].(map[string]interface{})
	if kwargs["thinking"] != false {
		t.Fatalf("expected chat_template_kwargs.thinking=false, got %#v", kwargs)
	}

	nvidiaEnabledBody := map[string]interface{}{}
	ApplyReasoningDefaults("nvidia", nvidiaEnabledBody, "moonshotai/kimi-k2.5", true)
	if _, exists := nvidiaEnabledBody["chat_template_kwargs"]; exists {
		t.Fatalf("explicitly enabled reasoning should not inject nvidia thinking defaults")
	}
}
