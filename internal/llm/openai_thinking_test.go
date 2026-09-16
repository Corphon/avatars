package llm

import (
	"encoding/json"
	"testing"
)

func TestReasoningContentForToolReplay_PinsEmptyForDeepseek(t *testing.T) {
	got := reasoningContentForToolReplay("deepseek", true, "")
	if got == nil {
		t.Fatal("expected non-nil pointer so JSON emits reasoning_content")
	}
	if *got != "" {
		t.Fatalf("expected empty string, got %q", *got)
	}
	raw, err := json.Marshal(openAIMessage{
		Role:             "assistant",
		Content:          "",
		ReasoningContent: got,
		ToolCalls: []openAIToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "write_file", Arguments: `{"path":"a.go","content":"package a"}`},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContainsKey(string(raw), "reasoning_content") {
		t.Fatalf("marshal dropped reasoning_content: %s", raw)
	}
}

func TestReasoningContentOmitemptyStringWouldDropEmpty(t *testing.T) {
	// Document the pre-Z8 footgun: plain string + omitempty drops "".
	type legacy struct {
		ReasoningContent string `json:"reasoning_content,omitempty"`
	}
	raw, _ := json.Marshal(legacy{ReasoningContent: ""})
	if jsonContainsKey(string(raw), "reasoning_content") {
		t.Fatalf("expected omitempty to drop empty string, got %s", raw)
	}
}

func TestApplyThinkingToOpenAIRequest(t *testing.T) {
	var req openAIRequest
	applyThinkingToOpenAIRequest("deepseek", ThinkModeOff, &req)
	if req.Thinking == nil || req.Thinking.Type != "disabled" {
		t.Fatalf("off → disabled, got %#v", req.Thinking)
	}
	req = openAIRequest{}
	applyThinkingToOpenAIRequest("deepseek", ThinkModeOn, &req)
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		t.Fatalf("on → enabled, got %#v", req.Thinking)
	}
	req = openAIRequest{}
	applyThinkingToOpenAIRequest("deepseek", ThinkModeAuto, &req)
	if req.Thinking != nil {
		t.Fatalf("auto should omit thinking field, got %#v", req.Thinking)
	}
	req = openAIRequest{}
	applyThinkingToOpenAIRequest("qwen", ThinkModeOff, &req)
	if req.Thinking != nil {
		t.Fatal("qwen should not get thinking toggle")
	}

	req = openAIRequest{Model: "glm-5.2"}
	applyThinkingToOpenAIRequest("glm", ThinkModeOff, &req)
	if req.Thinking == nil || req.Thinking.Type != "disabled" {
		t.Fatalf("glm-5.2 off → disabled, got %#v", req.Thinking)
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("glm-5.2 off must not set reasoning_effort, got %q", req.ReasoningEffort)
	}

	req = openAIRequest{Model: "GLM-5.3-Flash"}
	applyThinkingToOpenAIRequest("glm", ThinkModeOff, &req)
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		t.Fatalf("glm-5.3-flash cannot disable thinking, got %#v", req.Thinking)
	}
	if req.ReasoningEffort != "low" {
		t.Fatalf("glm-5.3-flash off → reasoning_effort=low, got %q", req.ReasoningEffort)
	}

	req = openAIRequest{Model: "glm-5.3"}
	applyThinkingToOpenAIRequest("glm", ThinkModeOff, &req)
	if req.Thinking == nil || req.Thinking.Type != "enabled" || req.ReasoningEffort != "low" {
		t.Fatalf("glm-5.3 off → enabled+low, thinking=%#v effort=%q", req.Thinking, req.ReasoningEffort)
	}
}

func TestApplyToolsToOpenAIRequest_EveryTurn(t *testing.T) {
	req := &openAIRequest{}
	applyToolsToOpenAIRequest(req, Request{
		Tools: []ToolDefinition{{Name: "write_file", Description: "w", Parameters: map[string]any{"type": "object"}}},
	}, 1)
	if len(req.Tools) != 1 {
		t.Fatalf("turn 1 must still carry tools, got %d", len(req.Tools))
	}
	if req.ToolChoice != "auto" {
		t.Fatalf("tool_choice=%q", req.ToolChoice)
	}
}

func jsonContainsKey(raw, key string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
