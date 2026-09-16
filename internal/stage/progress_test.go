package stage

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStageGenerateProgress_DoesNotDotSpam(t *testing.T) {
	var buf bytes.Buffer
	p := &stageGenerateProgress{
		out:         &buf,
		mode:        "text",
		interactive: false,
		minInterval: 2 * time.Second,
	}
	for i := 0; i < 400; i++ {
		p.OnContent("token")
	}
	p.Close()
	got := buf.String()
	if strings.Contains(got, "....") || strings.Count(got, ".") > 4 {
		t.Fatalf("per-chunk dots leaked: %q", got)
	}
	if !strings.Contains(got, "generating stage") {
		t.Fatalf("missing status line: %q", got)
	}
	if strings.Count(got, "\n") > 2 {
		t.Fatalf("too many lines for 400 chunks: %q", got)
	}
}

func TestStageGenerateProgress_OffIsSilent(t *testing.T) {
	var buf bytes.Buffer
	p := &stageGenerateProgress{out: &buf, mode: "off", minInterval: time.Millisecond}
	p.OnThinking("secret")
	p.OnContent("<html>")
	p.Close()
	if buf.Len() != 0 {
		t.Fatalf("off leaked %q", buf.String())
	}
}

func TestStageGenerateProgress_ThinkingThenHTML(t *testing.T) {
	var buf bytes.Buffer
	p := &stageGenerateProgress{
		out:         &buf,
		mode:        "text",
		interactive: false,
		minInterval: time.Hour, // only first + force Close
	}
	p.OnThinking("planning layout")
	p.OnContent("<!DOCTYPE html>")
	p.Close()
	got := buf.String()
	if !strings.Contains(got, "thinking") && !strings.Contains(got, "generating stage") {
		t.Fatalf("expected progress, got %q", got)
	}
	if strings.Contains(got, "planning layout") {
		t.Fatalf("must not dump CoT text: %q", got)
	}
}

func TestStageGenerateProgress_JSONIsOneObjectPerEmit(t *testing.T) {
	var buf bytes.Buffer
	p := &stageGenerateProgress{
		out:         &buf,
		mode:        "json",
		interactive: false,
		minInterval: time.Hour,
	}
	p.OnContent(strings.Repeat("a", 2048))
	p.Close()
	line := strings.TrimSpace(buf.String())
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if payload["type"] != "stage.generating" {
		t.Fatalf("type=%v", payload["type"])
	}
	if int(payload["bytes"].(float64)) != 2048 {
		t.Fatalf("bytes=%v", payload["bytes"])
	}
}
