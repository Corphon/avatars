// Package runtime — clarification hub for User-in-the-Loop quality loop.
//
// P7 implementation: Claude Code's AskUserQuestionTool pattern adapted to
// avatars' Planner→Researcher pipeline boundary.
//
// Optimal insertion point: AFTER Planner outputs its plan, BEFORE Researcher
// nodes start. If the Planner emitted clarifications, the workflow pauses
// at this boundary, the user answers, and the Planner re-runs with answers
// injected as hard constraints.
//
// Architecture:
//   User Input → Planner (decompose + emit clarifications?)
//     → YES clarifications → pause → user answers → Planner (re-plan with constraints)
//     → NO clarifications → Researcher → Builder → Critic → Synthesizer
//
// The pause uses the existing SchedulerNodeResult.PausePoint mechanism,
// same as tool-approval pauses. This avoids new pause infrastructure.

package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClarifyHub manages the clarification lifecycle within a run.
// It is created once per run and persists state to disk for REPL resume.
type ClarifyHub struct {
	memoryDir    string
	activeSession *ClarifySession
}

// NewClarifyHub creates a ClarifyHub for the given run.
func NewClarifyHub(memoryDir string) *ClarifyHub {
	return &ClarifyHub{memoryDir: memoryDir}
}

// NeedsClarification checks whether the Planner output contains unresolved
// clarifications. Returns the session if questions were emitted, nil otherwise.
//
// This is called at the Planner→Researcher boundary. If non-nil, the workflow
// should pause and present the questions to the user.
func (ch *ClarifyHub) NeedsClarification(runID, taskID, originalInput, plannerOutput string) *ClarifySession {
	questions := ExtractClarifications(plannerOutput)
	if len(questions) == 0 {
		return nil
	}

	// Validate each question.
	for i, q := range questions {
		if q.ID == "" {
			q.ID = fmt.Sprintf("clarify-%d", i+1)
			questions[i] = q
		}
		if err := ValidateClarifyQuestion(q); err != nil {
			// If a question is malformed, skip it rather than blocking.
			continue
		}
	}

	validQuestions := make([]ClarifyQuestion, 0, len(questions))
	for _, q := range questions {
		if ValidateClarifyQuestion(q) == nil {
			validQuestions = append(validQuestions, q)
		}
	}
	if len(validQuestions) == 0 {
		return nil
	}

	session := &ClarifySession{
		RunID:         runID,
		TaskID:        taskID,
		Questions:     validQuestions,
		Status:        ClarifyStatusPending,
		CreatedAt:     time.Now().UTC(),
		OriginalInput: originalInput,
	}
	ch.activeSession = session

	// Persist so REPL can load it after workflow pauses.
	ch.persist(session)

	return session
}

// ApplyAnswers records the user's answers and marks the session as answered.
// The answers are injected as constraints into the next Planner call.
func (ch *ClarifyHub) ApplyAnswers(answers []ClarifyAnswer) error {
	if ch.activeSession == nil {
		return fmt.Errorf("clarify: no active clarification session")
	}
	if ch.activeSession.Status != ClarifyStatusPending {
		return fmt.Errorf("clarify: session already %s", ch.activeSession.Status)
	}

	// Match answers to questions by ID.
	answered := make(map[string]bool)
	for _, a := range answers {
		answered[a.QuestionID] = true
	}
	for _, q := range ch.activeSession.Questions {
		if !answered[q.ID] {
			return fmt.Errorf("clarify: question %q not answered", q.ID)
		}
	}

	ch.activeSession.Answers = answers
	ch.activeSession.Status = ClarifyStatusAnswered
	ch.activeSession.AnsweredAt = time.Now().UTC()
	ch.persist(ch.activeSession)

	return nil
}

// ConstraintsForPlanner returns the answered clarifications formatted as
// Planner constraints. Call this before re-invoking the Planner.
func (ch *ClarifyHub) ConstraintsForPlanner() string {
	if ch.activeSession == nil || len(ch.activeSession.Answers) == 0 {
		return ""
	}
	return FormatClarificationsForPrompt(ch.activeSession.Answers)
}

// ActiveSession returns the current session if one is active.
func (ch *ClarifyHub) ActiveSession() *ClarifySession {
	return ch.activeSession
}

// LoadSession loads a persisted clarification session from disk.
func (ch *ClarifyHub) LoadSession(runID string) (*ClarifySession, error) {
	path := ch.sessionPath(runID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("clarify: load session: %w", err)
	}
	var session ClarifySession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("clarify: parse session: %w", err)
	}
	ch.activeSession = &session
	return &session, nil
}

// FormatForDisplay renders the questions as a user-readable string.
func FormatForDisplay(session *ClarifySession) string {
	if session == nil || len(session.Questions) == 0 {
		return ""
	}
	var buf strings.Builder
	buf.WriteString("\n⚠️  Before I proceed, I need to clarify a few things:\n\n")
	for i, q := range session.Questions {
		fmt.Fprintf(&buf, "  %d. %s\n", i+1, q.Question)
		for j, o := range q.Options {
			marker := "  "
			if j+1 <= len(q.Options) {
				marker = fmt.Sprintf("    %d.", j+1)
			}
			fmt.Fprintf(&buf, "%s %s — %s\n", marker, o.Label, o.Description)
		}
		buf.WriteString("\n")
	}
	buf.WriteString("Please answer each question (e.g., '1: A, 2: B').\n")
	return buf.String()
}

// sessionPath returns the disk path for a clarification session file.
func (ch *ClarifyHub) sessionPath(runID string) string {
	return filepath.Join(ch.memoryDir, "clarify_"+runID+".json")
}

// persist writes the session to disk.
func (ch *ClarifyHub) persist(session *ClarifySession) {
	path := ch.sessionPath(session.RunID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0644)
}

// Clear removes the persisted session file.
func (ch *ClarifyHub) Clear(runID string) {
	_ = os.Remove(ch.sessionPath(runID))
	ch.activeSession = nil
}
