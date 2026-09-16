// Package runtime — context compaction for long-running sessions.
//
// P10: Adopts Claude Code's compact pattern
// (claude_code_main/services/compact/)
// to prevent context overflow in multi-turn REPL sessions.
//
// When the REPL history exceeds a threshold, older turns are compacted
// into summary entries while recent turns are preserved in full.
//
// Strategy:
//   - Keep last 3 runs fully intact
//   - Compress older runs into one-line summaries
//   - Provide the compacted context as a system message

package runtime

import (
	"fmt"
	"strings"
)

// CompactionConfig controls when and how compaction occurs.
type CompactionConfig struct {
	MaxFullRuns    int // runs to keep in full (default: 3)
	MaxCompactRuns int // total runs before compaction triggers (default: 10)
	MaxSummaryLen  int // max chars per compacted run summary (default: 120)
}

// DefaultCompactionConfig returns sensible defaults.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		MaxFullRuns:    3,
		MaxCompactRuns: 10,
		MaxSummaryLen:  120,
	}
}

// CompactContext takes a list of run records and produces a compacted
// context string suitable for injection into the system prompt.
//
// Returns (contextString, needsCompaction).
func CompactContext(runs []RunRecord, cfg CompactionConfig) (string, bool) {
	if len(runs) <= cfg.MaxFullRuns {
		return "", false
	}

	var buf strings.Builder
	buf.WriteString("\n## Session History (compacted)\n")

	// Older runs: compacted.
	older := runs[:len(runs)-cfg.MaxFullRuns]
	if len(older) > 0 {
		buf.WriteString("Earlier in this session:\n")
		for _, r := range older {
			status := "??"
			switch r.Status {
			case "completed":
				status = "OK"
			case "failed":
				status = "FAIL"
			case "needs_remediation":
				status = "FIX"
			case "awaiting_clarification":
				status = "ASK"
			}
			input := truncateStr(r.Input, cfg.MaxSummaryLen)
			fmt.Fprintf(&buf, "  [%s] %s\n", status, input)
		}
		buf.WriteString("\n")
	}

	// Recent runs: full detail.
	recent := runs[len(runs)-cfg.MaxFullRuns:]
	buf.WriteString("Recent runs:\n")
	for _, r := range recent {
		status := "??"
		switch r.Status {
		case "completed":
			status = "completed"
		case "failed":
			status = "FAILED"
		case "needs_remediation":
			status = "NEEDS FIX"
		case "awaiting_clarification":
			status = "AWAITING INPUT"
		}
		fmt.Fprintf(&buf, "  Run [%s]: %s — %s\n", status, r.Input,
			truncateStr(r.Summary, cfg.MaxSummaryLen))
	}

	return buf.String(), true
}

// FormatSessionContext produces the full session context for Planner injection.
// Combines: extracted memories + compacted run history + current mode.
func FormatSessionContext(sm *SessionManager, me *MemoryExtractor) string {
	if sm == nil {
		return ""
	}

	var buf strings.Builder

	// Extracted memories from previous runs.
	if me != nil {
		if memories := me.LoadRecent(5); memories != "" {
			buf.WriteString(memories)
		}
	}

	// Compacted run history.
	runs := sm.RecentRuns(20)
	cfg := DefaultCompactionConfig()
	if compacted, needsIt := CompactContext(runs, cfg); needsIt {
		buf.WriteString(compacted)
	}

	// Current session mode.
	fmt.Fprintf(&buf, "\n## Session State\n")
	fmt.Fprintf(&buf, "Mode: %s | Runs in session: %d\n", sm.Mode(), len(runs))

	return buf.String()
}
