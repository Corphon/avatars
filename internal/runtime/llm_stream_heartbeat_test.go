package runtime

import (
	"testing"
	"time"
)

func TestStreamByteHeartbeat_FirstChunkThenCadence(t *testing.T) {
	var h streamByteHeartbeat
	emit, n := h.note("abc")
	if !emit || n != 3 {
		t.Fatalf("first non-empty chunk must emit, emit=%v n=%d", emit, n)
	}
	emit, n = h.note("x")
	if emit || n != 4 {
		t.Fatalf("immediate second chunk must not emit, emit=%v n=%d", emit, n)
	}
	emit, _ = h.note("")
	if emit {
		t.Fatal("empty delta must not emit")
	}
	h.last = time.Now().Add(-3 * time.Second)
	emit, n = h.note("yy")
	if !emit || n != 6 {
		t.Fatalf("after cadence must emit cumulative bytes, emit=%v n=%d", emit, n)
	}
}
