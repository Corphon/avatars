package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"avatars/internal/events"
	"avatars/internal/runtime"
)

// intentThinkingLiveCap is the max runes dumped live for intent CoT.
// Enough to judge direction; avoids flooding the terminal on long traces.
const intentThinkingLiveCap = 5000

// progressMode: "off" | "text" | "json"
func progressMode() string {
	raw := strings.TrimSpace(os.Getenv("AVATARS_PROGRESS"))
	switch strings.ToLower(raw) {
	case "0", "off", "false", "quiet":
		return "off"
	case "json":
		return "json"
	default:
		// unset, "1", "on", "text", anything else → lively text
		return "text"
	}
}

// stderrLooksInteractive reports whether stderr is a terminal. When false
// (redirected file/pipe), carriage-return spinners are unreadable in logs (F72).
func stderrLooksInteractive() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// writeCarriageProgress writes a TTY-style \r spinner only on interactive
// stderr; otherwise emits a plain newline so redirected logs stay readable.
func writeCarriageProgress(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	if stderrLooksInteractive() && progressMode() != "json" {
		fmt.Fprintf(os.Stderr, "\r%s", msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

func clearCarriageProgress() {
	if stderrLooksInteractive() && progressMode() != "json" {
		fmt.Fprintf(os.Stderr, "\r                              \r")
		return
	}
	// Non-TTY / json: nothing to clear; prior line already ended with \n.
}

// thinkingStreamPrinter streams LLM CoT to a writer (stderr in production).
// progressMode "off" is silent; "json" buffers and emits one object on Close.
type thinkingStreamPrinter struct {
	title     string
	out       io.Writer
	mode      string
	started   bool
	closed    bool
	truncated bool
	runes     int
	buf       strings.Builder
}

func newIntentThinkingPrinter() *thinkingStreamPrinter {
	return &thinkingStreamPrinter{
		title: "Intent thinking",
		out:   os.Stderr,
		mode:  progressMode(),
	}
}

func (p *thinkingStreamPrinter) Received() bool {
	return p != nil && (p.started || p.buf.Len() > 0)
}

func (p *thinkingStreamPrinter) Write(chunk string) {
	if p == nil || p.closed || p.mode == "off" || chunk == "" {
		return
	}
	if p.mode == "json" {
		p.buf.WriteString(chunk)
		return
	}
	if !p.started {
		clearCarriageProgress()
		fmt.Fprintf(p.out, "💭 %s:\n", p.title)
		p.started = true
	}
	if p.truncated {
		return
	}
	remaining := intentThinkingLiveCap - p.runes
	if remaining <= 0 {
		p.truncated = true
		fmt.Fprint(p.out, "\n   … (thinking truncated)\n")
		return
	}
	toWrite := chunk
	r := []rune(chunk)
	if len(r) > remaining {
		toWrite = string(r[:remaining])
		p.truncated = true
	}
	fmt.Fprint(p.out, toWrite)
	p.runes += utf8.RuneCountInString(toWrite)
	if p.truncated {
		fmt.Fprint(p.out, "\n   … (thinking truncated)\n")
	}
}

func (p *thinkingStreamPrinter) Close() {
	if p == nil || p.closed {
		return
	}
	p.closed = true
	if p.mode == "off" {
		return
	}
	if p.mode == "json" {
		if p.buf.Len() == 0 {
			return
		}
		raw, err := json.Marshal(map[string]any{
			"ts":      time.Now().UTC().Format(time.RFC3339),
			"type":    "intent.thinking",
			"chars":   utf8.RuneCountInString(p.buf.String()),
			"preview": lastRunesPreview(p.buf.String(), 400),
		})
		if err != nil {
			return
		}
		fmt.Fprintln(p.out, string(raw))
		return
	}
	if p.started {
		fmt.Fprintln(p.out)
	}
}

func lastRunesPreview(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return "…" + string(r[len(r)-max:])
}

func writeIntentJudgment(w io.Writer, mode string, d llmRouterDecision) {
	if w == nil || mode == "off" {
		return
	}
	action := strings.TrimSpace(d.Action)
	reason := strings.TrimSpace(d.Reason)
	if action == "" && reason == "" {
		return
	}
	if mode == "json" {
		payload := map[string]any{
			"ts":         time.Now().UTC().Format(time.RFC3339),
			"type":       "intent.judgment",
			"action":     action,
			"confidence": d.Confidence,
			"reason":     reason,
		}
		if len(d.Command) > 0 {
			payload["command"] = lastRunesPreview(strings.Join(d.Command, " "), 80)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintln(w, string(raw))
		return
	}
	line := "🧭 Intent: " + action
	if d.Confidence > 0 {
		line += fmt.Sprintf(" · %d%%", d.Confidence)
	}
	if len(d.Command) > 0 {
		cmd := lastRunesPreview(strings.Join(d.Command, " "), 80)
		if cmd != "" {
			line += " · " + cmd
		}
	}
	fmt.Fprintln(w, line)
	if reason != "" {
		fmt.Fprintf(w, "   %s\n", reason)
	}
}

func startRunProgressPrinter(store *events.Store) func() {
	mode := progressMode()
	if store == nil || mode == "off" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("AVATARS_COMPACT_HEARTBEAT")), "1") {
			done := make(chan struct{})
			go func() {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				n := 0
				for {
					select {
					case <-done:
						return
					case <-ticker.C:
						n++
						fmt.Fprintf(os.Stderr, "⌛ still working… (%ds)\n", n*15)
					}
				}
			}()
			return func() { close(done) }
		}
		return func() {}
	}

	eventsChannel, unsubscribe := store.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var (
			mu           sync.Mutex
			llmStartedAt time.Time
			llmLabel     string
			lastPulse    string
		)
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()

		printLine := func(line string) {
			if strings.TrimSpace(line) == "" {
				return
			}
			fmt.Fprintln(os.Stderr, line)
		}
		printJSON := func(event events.Envelope, line string) {
			payload := map[string]any{
				"ts":   time.Now().UTC().Format(time.RFC3339),
				"seq":  event.Sequence,
				"type": event.Type,
				"line": line,
			}
			if event.RunID != "" {
				payload["run_id"] = event.RunID
			}
			if event.TaskID != "" {
				payload["task_id"] = event.TaskID
			}
			if role := inferProgressRole(event); role != "" {
				payload["role"] = role
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return
			}
			fmt.Fprintln(os.Stderr, string(raw))
		}
		emit := func(event events.Envelope, line string) {
			if mode == "json" {
				printJSON(event, line)
				return
			}
			printLine(line)
		}

		for {
			select {
			case event, ok := <-eventsChannel:
				if !ok {
					return
				}
				line := runProgressLine(event)
				if line != "" {
					emit(event, line)
				}
				// Track LLM wait for heartbeat.
				mu.Lock()
				switch event.Type {
				case "llm.started", "heartbeat.builder_generating", "heartbeat.critic_auditing", "llm.thinking", "llm.streaming":
					llmStartedAt = time.Now()
					llmLabel = inferProgressRole(event)
					if llmLabel == "" {
						llmLabel = "LLM"
					}
					if event.Type == "llm.thinking" {
						llmLabel = "thinking"
					}
					if event.Type == "llm.streaming" {
						llmLabel = "receiving"
					}
					if provider := payloadStringValue(event.Payload, "provider"); provider != "" {
						llmLabel = llmLabel + "/" + provider
					}
				case "llm.completed", "llm.failed", "run.completed",
					"builder.code_generation_converged", "workflow.node_completed",
					"tool.completed", "avatar.message", "critic.project_health_verified":
					// R5-5: clear wait heartbeat as soon as drafting/node work finishes.
					llmStartedAt = time.Time{}
					llmLabel = ""
				}
				mu.Unlock()

				if mode == "text" && runtime.ShouldPulsePhase(event.Type) {
					if wd, err := os.Getwd(); err == nil {
						if pulse := runtime.LivePhasePulseLine(wd); pulse != "" && pulse != lastPulse {
							lastPulse = pulse
							printLine(pulse)
						}
					}
				}
			case <-ticker.C:
				mu.Lock()
				started := llmStartedAt
				label := llmLabel
				mu.Unlock()
				if started.IsZero() {
					continue
				}
				elapsed := time.Since(started).Round(time.Second)
				msg := fmt.Sprintf("⌛ still drafting… (%s, %s)", label, elapsed)
				if strings.HasPrefix(label, "thinking") {
					msg = fmt.Sprintf("💭 thinking… (%s)", elapsed)
				}
				if mode == "json" {
					printJSON(events.Envelope{Type: "heartbeat.llm_wait"}, msg)
				} else {
					printLine(msg)
				}
			}
		}
	}()
	return func() {
		unsubscribe()
		<-done
	}
}

func inferProgressRole(event events.Envelope) string {
	if role := payloadStringValue(event.Payload, "role"); role != "" {
		return role
	}
	if role := payloadStringValue(event.Payload, "assigned_role"); role != "" {
		return role
	}
	if role := payloadStringValue(event.Payload, "avatar_role"); role != "" {
		return role
	}
	id := payloadStringValue(event.Payload, "avatar_id")
	lower := strings.ToLower(id)
	switch {
	case strings.Contains(lower, "builder"):
		return "Builder"
	case strings.Contains(lower, "critic"):
		return "Critic"
	case strings.Contains(lower, "research"):
		return "Researcher"
	case strings.Contains(lower, "plan"):
		return "Planner"
	case strings.Contains(lower, "synth"):
		return "Synthesizer"
	default:
		return ""
	}
}
