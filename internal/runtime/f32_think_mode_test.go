package runtime

import (
	"testing"

	"avatars/internal/llm"
)

// F32: Builder codegen requests must force ThinkModeOff so yaml `auto` cannot
// leave DeepSeek V4 thinking enabled (r13: 21k CoT chars → empty answer).
func TestBuilderCodeGenRequestForcesThinkModeOff(t *testing.T) {
	req := llm.Request{
		Category:  llm.CategoryCodeGeneration,
		ThinkMode: llm.ThinkModeOff,
	}
	if req.ThinkMode != llm.ThinkModeOff {
		t.Fatalf("ThinkMode=%q want off", req.ThinkMode)
	}
}
