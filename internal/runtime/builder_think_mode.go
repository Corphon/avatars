package runtime

import (
	"time"

	"avatars/internal/llm"
)

// applyHarnessThinkModeOff forces ThinkModeOff on harness main-path LLM calls.
// Cross-provider: wall-clock/token budget must go to answer content or tool calls.
// CoT (reasoning_content) does not count as success and can empty-fail slow
// thinking models when think_mode is auto (provider default = on for DeepSeek V4).
//
// Applies to: Builder/Direct codegen, workflow construct/confirm/expand,
// Synthesizer summary. Critic plan-review intentionally inherits yaml (advisory).
// Unclassified Generate calls still inherit agent.yaml think_mode (escape hatch).
// Do not branch on provider/model.
func applyHarnessThinkModeOff(req *llm.Request) {
	if req == nil {
		return
	}
	req.ThinkMode = llm.ThinkModeOff
}

// applyBuilderCodeGenThinkMode forces ThinkModeOff on code-generation requests.
// F8: alias of applyHarnessThinkModeOff for Builder/Direct call sites.
func applyBuilderCodeGenThinkMode(req *llm.Request) {
	applyHarnessThinkModeOff(req)
}

// builderCodeGenLLMTimeout is yaml CodeGen timeout with a 180s floor.
// The long thinking floor applies only when ThinkMode is explicitly on,
// so Off/auto codegen does not idle for 10 minutes after files are on disk.
func builderCodeGenLLMTimeout(req llm.Request) time.Duration {
	d := llm.TimeoutForRequest(req)
	if d < 180*time.Second {
		d = 180 * time.Second
	}
	if req.ThinkMode == llm.ThinkModeOn {
		if floor := llm.ThinkingCodeGenTimeoutFloor(); d < floor {
			d = floor
		}
	}
	return d
}
