package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"avatars/internal/runtime"
)

// handleClarifyLoop checks if a run result has clarification questions pending.
// If so, it presents them to the user, collects answers, and re-runs the
// engine with answers injected as constraints. This loops until the task
// is unambiguous enough to proceed.
//
// P7: User-in-the-Loop quality loop — prevent LLM guesswork on ambiguous tasks.
func handleClarifyLoop(engine *runtime.Engine, input string, result runtime.RunResult) (runtime.RunResult, error) {
	currentResult := result
	currentInput := input
	round := 0
	const maxClarifyRounds = 3

	for currentResult.ClarifyPending != nil && round < maxClarifyRounds {
		round++
		session := currentResult.ClarifyPending

		// Render questions.
		display := runtime.FormatForDisplay(session)
		if display == "" {
			break
		}
		fmt.Fprint(os.Stderr, display)

		// Collect answers from stdin.
		answers, exitRequested := collectClarifyAnswers(session)
		if exitRequested {
			fmt.Fprintln(os.Stderr, "Clarification cancelled. Run aborted.")
			return runtime.RunResult{}, nil
		}
		if len(answers) == 0 {
			fmt.Fprintln(os.Stderr, "No answers received. Proceeding with original interpretation.")
			// Re-run engine with original input -- ClarifyPending result has no work done.
			var err error
			currentResult, err = engine.Run(context.Background(), "[NO-CLARIFY] "+currentInput)
			if err != nil {
				return currentResult, err
			}
			break
		}

		// Build constrained input for re-run.
		constrainedInput := buildClarifiedInput(currentInput, answers)
		fmt.Fprintf(os.Stderr, "\nRe-running with clarified intent...\n\n")

		// Re-run engine with the clarified input.
		var err error
		currentResult, err = engine.Run(context.Background(), "[NO-CLARIFY] "+constrainedInput)
		if err != nil {
			return currentResult, err
		}
		currentInput = constrainedInput
	}

	return currentResult, nil
}

// collectClarifyAnswers reads answers from stdin for each question.
// Returns the answers and a flag indicating the user wants to exit.
// Uses a 5-second timeout per question — if no input, returns empty answers
// so the run can proceed with [NO-CLARIFY] fallback.
func collectClarifyAnswers(session *runtime.ClarifySession) ([]runtime.ClarifyAnswer, bool) {
	var answers []runtime.ClarifyAnswer
	const clarifyTimeout = 5 * time.Second

	for i, q := range session.Questions {
		fmt.Fprintf(os.Stderr, "  [%d/%d] %s\n", i+1, len(session.Questions), q.Question)
		for j, o := range q.Options {
			fmt.Fprintf(os.Stderr, "    %d. %s — %s\n", j+1, o.Label, o.Description)
		}
		fmt.Fprintf(os.Stderr, "  Your choice (1-%d, or 'skip'/'exit'): ", len(q.Options))

		// Read with timeout — background runs have no stdin.
		lineCh := make(chan string, 1)
		go func() {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				lineCh <- scanner.Text()
			} else {
				lineCh <- ""
			}
		}()

		var line string
		select {
		case line = <-lineCh:
		case <-time.After(clarifyTimeout):
			fmt.Fprintln(os.Stderr, "  (timeout — proceeding without answers)")
			return answers, false
		}
		line = strings.TrimSpace(line)

		switch strings.ToLower(line) {
		case "exit", "quit", "/exit":
			return nil, true
		case "skip", "":
			continue
		}

		// Parse numeric choice.
		var choiceIdx int
		if _, scanErr := fmt.Sscanf(line, "%d", &choiceIdx); scanErr != nil || choiceIdx < 1 || choiceIdx > len(q.Options) {
			fmt.Fprintf(os.Stderr, "  Invalid choice. Skipping this question.\n")
			continue
		}

		answers = append(answers, runtime.ClarifyAnswer{
			QuestionID: q.ID,
			Selected:   q.Options[choiceIdx-1].Label,
		})
	}

	return answers, false
}

// buildClarifiedInput combines the original user input with clarification
// answers as explicit constraints.
func buildClarifiedInput(originalInput string, answers []runtime.ClarifyAnswer) string {
	if len(answers) == 0 {
		return originalInput
	}

	var constraints []string
	constraints = append(constraints, originalInput)
	constraints = append(constraints, "")
	constraints = append(constraints, "[CLARIFIED BY USER]")
	for _, a := range answers {
		constraints = append(constraints, fmt.Sprintf("- %s → %s", a.QuestionID, a.Selected))
	}

	return strings.Join(constraints, "\n")
}
