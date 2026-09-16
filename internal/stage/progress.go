package stage

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// stageGenerateProgress replaces the old per-chunk "." spam with a single
// updating status line (KB of HTML, or thinking char count).
type stageGenerateProgress struct {
	out         io.Writer
	mode        string
	interactive bool
	minInterval time.Duration
	bytes       int
	thinking    int
	lastReport  time.Time
	printed     bool
	lastBytes   int
	lastThink   int
}

func newStageGenerateProgress(out io.Writer) *stageGenerateProgress {
	if out == nil {
		out = os.Stderr
	}
	interval := 2 * time.Second
	interactive := false
	if out == os.Stderr {
		if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			interactive = true
			interval = 200 * time.Millisecond
		}
	}
	return &stageGenerateProgress{
		out:         out,
		mode:        stageProgressMode(),
		interactive: interactive,
		minInterval: interval,
	}
}

func stageProgressMode() string {
	raw := strings.TrimSpace(os.Getenv("AVATARS_PROGRESS"))
	switch strings.ToLower(raw) {
	case "0", "off", "false", "quiet":
		return "off"
	case "json":
		return "json"
	default:
		return "text"
	}
}

func (p *stageGenerateProgress) OnThinking(chunk string) {
	if p == nil || chunk == "" {
		return
	}
	p.thinking += len(chunk)
	p.emit(false)
}

func (p *stageGenerateProgress) OnContent(chunk string) {
	if p == nil || chunk == "" {
		return
	}
	p.bytes += len(chunk)
	p.emit(false)
}

func (p *stageGenerateProgress) Close() {
	if p == nil {
		return
	}
	p.emit(true)
	if p.mode == "text" && p.printed && p.interactive {
		fmt.Fprintln(p.out)
	}
}

func (p *stageGenerateProgress) emit(force bool) {
	if p.mode == "off" {
		return
	}
	now := time.Now()
	if p.bytes == 0 && p.thinking == 0 {
		return
	}
	if !force && p.printed && now.Sub(p.lastReport) < p.minInterval {
		return
	}
	if force && p.printed && p.bytes == p.lastBytes && p.thinking == p.lastThink {
		return
	}
	p.lastReport = now
	p.lastBytes = p.bytes
	p.lastThink = p.thinking
	p.printed = true
	line := p.line()
	switch p.mode {
	case "json":
		raw, err := json.Marshal(map[string]any{
			"ts":             now.UTC().Format(time.RFC3339),
			"type":           "stage.generating",
			"bytes":          p.bytes,
			"thinking_chars": p.thinking,
			"line":           line,
		})
		if err != nil {
			return
		}
		fmt.Fprintln(p.out, string(raw))
	default:
		if p.interactive {
			fmt.Fprintf(p.out, "\r%-48s", line)
			return
		}
		fmt.Fprintln(p.out, line)
	}
}

func (p *stageGenerateProgress) line() string {
	if p.bytes == 0 && p.thinking > 0 {
		return fmt.Sprintf("💭 thinking… (%d chars)", p.thinking)
	}
	return fmt.Sprintf("⌛ generating stage… %.1f KB", float64(p.bytes)/1024.0)
}
