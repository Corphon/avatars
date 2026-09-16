package runtime

import (
	"fmt"
	"os"
	"strings"

	"avatars/internal/workflow"
)

// criticPreFlightHealthCheck runs language-appropriate verification
// BEFORE the Critic dispatch loop. This catches issues like Python syntax
// errors, truncated files, and compilation failures regardless of checklist state.
//
// Returns true if the health check found issues (should trigger escalation).
func (e *Engine) criticPreFlightHealthCheck(work workflowNodeWorkContext) bool {
	_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.preflight_started", "runtime", nil, nil)

	wd, err := os.Getwd()
	if err != nil {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.preflight_no_wd", "runtime", map[string]any{"error": err.Error()}, nil)
		return false
	}

	// TR-Fix-13: Run language-agnostic static analysis before health check.
	// Catches logic bugs early — before the full Critic dispatch loop.
	saFindings := runStaticAnalysis(wd)
	if len(saFindings) > 0 {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing",
			"critic.preflight_static_analysis", "runtime",
			map[string]any{
				"count":  len(saFindings),
				"report": formatStaticAnalysisFindings(saFindings),
			}, nil)
	}

	healthErr := crossLangHealthCheck(wd)
	if healthErr != "" {
		_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing", "critic.preflight_health_err", "runtime", map[string]any{"error": healthErr}, nil)
	}
	if healthErr == "" {
		// Also check requirement gaps (node input + original NL task hint).
		codeText := workflow.CollectGeneratedCodeText(wd)
		missing := checkUserRequirementGaps(work.input, codeText)
		hint := work.input
		if e != nil && strings.TrimSpace(e.layoutTaskHint) != "" {
			hint = e.layoutTaskHint
			if hint != work.input {
				for _, m := range checkUserRequirementGaps(hint, codeText) {
					dup := false
					for _, existing := range missing {
						if existing == m {
							dup = true
							break
						}
					}
					if !dup {
						missing = append(missing, m)
					}
				}
			}
		}
		for _, m := range checkPhaseScopedRequirementGaps(wd, hint, codeText) {
			dup := false
			for _, existing := range missing {
				if existing == m {
					dup = true
					break
				}
			}
			if !dup {
				missing = append(missing, m)
			}
		}
		if len(missing) == 0 {
			// Static analysis findings with errors still trigger escalation.
			for _, f := range saFindings {
				if f.Severity == "error" {
					healthErr = fmt.Sprintf("Static analysis found errors: %s", f.Message)
					break
				}
			}
			if healthErr == "" {
				return false
			}
		} else {
			healthErr = "Missing features: " + missing[0]
		}
	}

	_ = e.emit(work.runID, work.taskID, work.criticAvatarID, "reviewing",
		"critic.preflight_failed", "runtime",
		map[string]any{"error": healthErr}, nil)

	return true
}
