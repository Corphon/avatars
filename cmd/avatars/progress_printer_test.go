package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestThinkingStreamPrinter_TextStreamsHeaderAndChunks(t *testing.T) {
	var buf bytes.Buffer
	p := &thinkingStreamPrinter{title: "Intent thinking", out: &buf, mode: "text"}
	p.Write("first I check whether this is a run request")
	p.Write(", then pick acceptEdits")
	p.Close()
	got := buf.String()
	if !strings.Contains(got, "💭 Intent thinking:") {
		t.Fatalf("missing header: %q", got)
	}
	if !strings.Contains(got, "first I check whether this is a run request, then pick acceptEdits") {
		t.Fatalf("missing streamed CoT: %q", got)
	}
	if !p.Received() {
		t.Fatal("expected Received after chunks")
	}
}

func TestThinkingStreamPrinter_TruncatesAtCap(t *testing.T) {
	var buf bytes.Buffer
	p := &thinkingStreamPrinter{title: "Intent thinking", out: &buf, mode: "text"}
	p.Write(strings.Repeat("方向", intentThinkingLiveCap+20))
	p.Close()
	got := buf.String()
	if !strings.Contains(got, "… (thinking truncated)") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if utf8Count := strings.Count(got, "方向"); utf8Count > intentThinkingLiveCap {
		t.Fatalf("dumped %d runes of payload, cap %d", utf8Count, intentThinkingLiveCap)
	}
}

func TestThinkingStreamPrinter_OffIsSilent(t *testing.T) {
	var buf bytes.Buffer
	p := &thinkingStreamPrinter{title: "Intent thinking", out: &buf, mode: "off"}
	p.Write("secret chain of thought")
	p.Close()
	if buf.Len() != 0 {
		t.Fatalf("off mode leaked %q", buf.String())
	}
	if p.Received() {
		t.Fatal("off mode must not mark Received")
	}
}

func TestThinkingStreamPrinter_JSONEmitsOneObjectOnClose(t *testing.T) {
	var buf bytes.Buffer
	p := &thinkingStreamPrinter{title: "Intent thinking", out: &buf, mode: "json"}
	p.Write("step one ")
	p.Write("step two about run vs clarify")
	p.Close()
	line := strings.TrimSpace(buf.String())
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("json mode should emit one line, got %q", buf.String())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, line)
	}
	if payload["type"] != "intent.thinking" {
		t.Fatalf("type=%v", payload["type"])
	}
	preview, _ := payload["preview"].(string)
	if !strings.Contains(preview, "run vs clarify") {
		t.Fatalf("preview=%q", preview)
	}
	chars, _ := payload["chars"].(float64)
	if int(chars) != 38 {
		t.Fatalf("chars=%v", payload["chars"])
	}
}

func TestWriteIntentJudgment_TextShowsActionAndReason(t *testing.T) {
	var buf bytes.Buffer
	writeIntentJudgment(&buf, "text", llmRouterDecision{
		Action:     "safe_run",
		Confidence: 86,
		Command:    []string{"run", "--new-task", "add a queue"},
		Reason:     "user asked to implement code, not a status question",
	})
	got := buf.String()
	if !strings.Contains(got, "🧭 Intent: safe_run") || !strings.Contains(got, "86%") {
		t.Fatalf("missing action/confidence: %q", got)
	}
	if !strings.Contains(got, "user asked to implement code") {
		t.Fatalf("missing reason: %q", got)
	}
}

func TestWriteIntentJudgment_OffIsSilent(t *testing.T) {
	var buf bytes.Buffer
	writeIntentJudgment(&buf, "off", llmRouterDecision{Action: "safe_run", Reason: "x"})
	if buf.Len() != 0 {
		t.Fatalf("off leaked %q", buf.String())
	}
}

func TestLastRunesPreview_UsesTail(t *testing.T) {
	got := lastRunesPreview("aaa bbb ccc ddd", 7)
	if got != "…ccc ddd" && !strings.HasSuffix(got, "ccc ddd") {
		t.Fatalf("got %q", got)
	}
}
