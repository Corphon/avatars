// Package runtime — automatic memory extraction from completed runs.
//
// P10: Adopts Claude Code's extractMemories pattern
// (claude_code_main/services/extractMemories/extractMemories.ts)
// to derive persistent insights from run outcomes without LLM involvement.
//
// Unlike Claude Code which uses a background sub-agent, avatars uses
// lightweight heuristic extraction — zero LLM cost, sub-millisecond.
//
// Extracted categories:
//   - Failure patterns: what went wrong and why
//   - Success patterns: what worked well
//   - User feedback: rejections and clarifications
//   - Key decisions: architectural choices made

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExtractedMemory is one piece of derived knowledge from a run.
type ExtractedMemory struct {
	Category string    `json:"category"` // failure / success / feedback / decision
	Content  string    `json:"content"`
	Source   string    `json:"source"` // run_id
	Time     time.Time `json:"time"`
}

// MemoryExtractor analyzes run results and extracts persistent memories.
type MemoryExtractor struct {
	memoryDir string
}

// NewMemoryExtractor creates a memory extractor for the given project root.
func NewMemoryExtractor(projectRoot string) *MemoryExtractor {
	return &MemoryExtractor{
		memoryDir: filepath.Join(projectRoot, ".avatars", "memory"),
	}
}

// ExtractFromRun analyzes a completed run and returns derived memories.
func (me *MemoryExtractor) ExtractFromRun(runID, input, status, summary string, planFeedback string) []ExtractedMemory {
	var memories []ExtractedMemory

	// Memory 1: Failure patterns
	if status == "failed" || status == "needs_remediation" {
		failure := me.extractFailurePattern(input, summary)
		if failure != "" {
			memories = append(memories, ExtractedMemory{
				Category: "failure",
				Content:  failure,
				Source:   runID,
				Time:     time.Now().UTC(),
			})
		}
	}

	// Memory 2: Success patterns
	if status == "completed" {
		success := me.extractSuccessPattern(input, summary)
		if success != "" {
			memories = append(memories, ExtractedMemory{
				Category: "success",
				Content:  success,
				Source:   runID,
				Time:     time.Now().UTC(),
			})
		}
	}

	// Memory 3: User feedback from previous runs (already captured by FeedbackLog)
	if planFeedback != "" {
		memories = append(memories, ExtractedMemory{
			Category: "feedback",
			Content:  fmt.Sprintf("User provided feedback before this run: %s", planFeedback),
			Source:   runID,
			Time:     time.Now().UTC(),
		})
	}

	return memories
}

// extractFailurePattern derives a failure pattern from the run outcome.
func (me *MemoryExtractor) extractFailurePattern(input, summary string) string {
	lowered := strings.ToLower(summary)

	pattern := "Run failed. Input: " + truncateStr(input, 120) + ". "

	// Detect specific failure modes.
	switch {
	case strings.Contains(lowered, "syntax error"):
		pattern += "Failure mode: SYNTAX_ERROR — generated code had syntax errors."
	case strings.Contains(lowered, "go build"):
		pattern += "Failure mode: BUILD_FAILURE — generated code did not compile."
	case strings.Contains(lowered, "timeout"):
		pattern += "Failure mode: TIMEOUT — LLM generation exceeded time limit."
	case strings.Contains(lowered, "permission denied"):
		pattern += "Failure mode: PERMISSION — tool call was denied by permission policy."
	case strings.Contains(lowered, "no repository evidence"):
		pattern += "Failure mode: EMPTY_SURVEY — Researcher found no project files."
	default:
		pattern += "Failure mode: UNKNOWN — " + truncateStr(summary, 200)
	}

	return pattern
}

// extractSuccessPattern derives a success pattern from the run outcome.
func (me *MemoryExtractor) extractSuccessPattern(input, summary string) string {
	return fmt.Sprintf("Run completed successfully. Task: %s. Outcome: %s",
		truncateStr(input, 120), truncateStr(summary, 200))
}

// Persist writes extracted memories to disk.
func (me *MemoryExtractor) Persist(memories []ExtractedMemory) error {
	if len(memories) == 0 {
		return nil
	}
	if err := os.MkdirAll(me.memoryDir, 0755); err != nil {
		return err
	}

	path := filepath.Join(me.memoryDir, "extracted.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, m := range memories {
		fmt.Fprintf(f, "## %s (%s)\n", m.Category, m.Time.Format(time.RFC3339))
		fmt.Fprintf(f, "%s\n", m.Content)
		fmt.Fprintf(f, "<!-- source: %s -->\n\n", m.Source)
	}
	return nil
}

// LoadRecent loads extracted memories from the last N runs for injection
// into the next Planner's structured context.
func (me *MemoryExtractor) LoadRecent(maxEntries int) string {
	path := filepath.Join(me.memoryDir, "extracted.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	// Parse the markdown file and return the last N entries.
	entries := strings.Split(string(data), "## ")
	if len(entries) <= 1 {
		return ""
	}

	// entries[0] is before the first "## " heading
	start := len(entries) - maxEntries
	if start < 1 {
		start = 1
	}

	var buf strings.Builder
	buf.WriteString("\n## Extracted Memories (from previous runs)\n")
	buf.WriteString("Learn from these patterns. Avoid repeating failures:\n\n")
	for i := start; i < len(entries); i++ {
		buf.WriteString("## " + entries[i])
	}
	return buf.String()
}

// containsAnyKeyword returns true if s contains any of the given keywords.
func containsAnyKeyword(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
