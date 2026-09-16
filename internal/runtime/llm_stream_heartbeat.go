package runtime

import (
	"fmt"
	"time"

	"avatars/internal/llm"
)

const llmStreamHeartbeatInterval = 2 * time.Second

// streamByteHeartbeat emits on the first non-empty chunk, then at most once
// per llmStreamHeartbeatInterval. Used so jsonl is not stuck on llm.started
// while the provider is already streaming content or tool arguments.
type streamByteHeartbeat struct {
	bytes int
	last  time.Time
}

func (h *streamByteHeartbeat) note(chunk string) (emit bool, totalBytes int) {
	if h == nil || chunk == "" {
		if h == nil {
			return false, 0
		}
		return false, h.bytes
	}
	h.bytes += len(chunk)
	now := time.Now()
	if h.last.IsZero() || now.Sub(h.last) >= llmStreamHeartbeatInterval {
		h.last = now
		return true, h.bytes
	}
	return false, h.bytes
}

func (e *Engine) attachContentStreamHeartbeats(req *llm.Request, runID, taskID, avatarID, status, nodeID string) {
	if e == nil || req == nil {
		return
	}
	prev := req.StreamCallback
	var h streamByteHeartbeat
	req.StreamCallback = func(chunk string) {
		if prev != nil {
			prev(chunk)
		}
		ok, n := h.note(chunk)
		if !ok {
			return
		}
		payload := map[string]any{
			"bytes":   n,
			"message": fmt.Sprintf("receiving… (%d bytes)", n),
		}
		if nodeID != "" {
			payload["node_id"] = nodeID
		}
		_ = e.emit(runID, taskID, avatarID, status, "llm.streaming", "llm", payload, nil)
	}
}
