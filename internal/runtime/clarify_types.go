// Package runtime — clarification types for User-in-the-Loop quality loop.
//
// P7: Adopts Claude Code's AskUserQuestionTool pattern
// (claude_code_main/tools/AskUserQuestionTool/AskUserQuestionTool.tsx)
// to prevent LLM guesswork on ambiguous tasks.
//
// When the Planner encounters an ambiguous request, it emits ClarifyQuestion
// items instead of committing to a full decomposition. The workflow pauses
// until the user answers, then re-plans with the answers as constraints.

package runtime

import (
	"fmt"
	"time"
)

// ClarifyQuestion represents one ambiguity the Planner wants the user to resolve.
// Mirrors Claude Code's AskUserQuestion input schema.
type ClarifyQuestion struct {
	ID       string           `json:"id"`
	Question string           `json:"question"`
	Header   string           `json:"header"` // short label, max 12 chars
	Options  []ClarifyOption  `json:"options"` // 2-4 options
	Multi    bool             `json:"multi,omitempty"`
}

// ClarifyOption is one choice within a question.
type ClarifyOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ClarifyAnswer is the user's response to one question.
type ClarifyAnswer struct {
	QuestionID string `json:"question_id"`
	Selected   string `json:"selected"` // label of chosen option
	Notes      string `json:"notes,omitempty"`
}

// ClarifySession holds the state of an active clarification interaction.
// It is persisted to disk so REPL can resume after user input.
type ClarifySession struct {
	RunID       string            `json:"run_id"`
	TaskID      string            `json:"task_id"`
	Questions   []ClarifyQuestion `json:"questions"`
	Answers     []ClarifyAnswer   `json:"answers,omitempty"`
	Status      string            `json:"status"` // pending / answered / timeout
	CreatedAt   time.Time         `json:"created_at"`
	AnsweredAt  time.Time         `json:"answered_at,omitempty"`
	OriginalInput string          `json:"original_input"`
}

// ClarifySession status constants.
const (
	ClarifyStatusPending  = "pending"
	ClarifyStatusAnswered = "answered"
	ClarifyStatusTimeout  = "timeout"
)

// ExtractClarifications parses Planner output text and extracts any ClarifyQuestion
// blocks. Returns nil if the Planner didn't emit any clarifications.
//
// Detection heuristic: looks for JSON blocks matching ClarifyQuestion schema,
// or markdown lists starting with "## Clarification Needed" headings.
func ExtractClarifications(_ /* plannerOutput */ string) []ClarifyQuestion {
	// Phase 1: Stub — always returns nil until we wire up Planner output parsing.
	// The Planner StablePrefix rule #13 instructs the LLM to emit clarifications
	// in a specific JSON format. This function will parse that format.
	return nil
}

// FormatClarificationsForPrompt formats answered clarifications as constraints
// to inject into the next Planner call.
func FormatClarificationsForPrompt(answers []ClarifyAnswer) string {
	if len(answers) == 0 {
		return ""
	}
	var buf string
	buf = "\n## User Clarifications (from previous round)\n"
	buf += "The user answered the following clarifying questions. Treat these as hard constraints:\n\n"
	for _, a := range answers {
		buf += "- **Q**: " + a.QuestionID + " → **A**: " + a.Selected
		if a.Notes != "" {
			buf += " (" + a.Notes + ")"
		}
		buf += "\n"
	}
	return buf
}

// ValidateClarifyQuestion checks that a ClarifyQuestion meets the schema constraints:
// - 2-4 options
// - Unique option labels
// - Non-empty question text
func ValidateClarifyQuestion(q ClarifyQuestion) error {
	if q.Question == "" {
		return errClarify("question text is required")
	}
	if len(q.Options) < 2 || len(q.Options) > 4 {
		return errClarify("must have 2-4 options, got %d", len(q.Options))
	}
	seen := map[string]bool{}
	for _, o := range q.Options {
		if o.Label == "" {
			return errClarify("option label is required")
		}
		if seen[o.Label] {
			return errClarify("duplicate option label: %s", o.Label)
		}
		seen[o.Label] = true
	}
	return nil
}

// errClarify is a helper for clarification validation errors.
func errClarify(format string, args ...any) error {
	return &clarifyError{msg: fmt.Sprintf(format, args...)}
}

type clarifyError struct {
	msg string
}

func (e *clarifyError) Error() string {
	return "clarify: " + e.msg
}
