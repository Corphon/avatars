package runtime

import (
	"os/exec"
	"strconv"
	"strings"
)

// OverEditWarning holds the result of a git diff --stat check after a Builder fix.
// WR-Fix-7: Detects when a fix changes far more lines than the original error scope,
// indicating the Builder likely rewrote files instead of making targeted edits.
type OverEditWarning struct {
	FilesChanged int
	LinesAdded   int
	LinesRemoved int
	IsOverEdit   bool
	Detail       string
}

// CheckOverEdit runs git diff --stat and returns a warning if the total changes
// exceed the threshold. WR-Fix-7: Called after Critic dispatches a fix to verify
// the fix was minimal.
func CheckOverEdit(maxExpectedLines int) OverEditWarning {
	cmd := exec.Command("git", "diff", "--stat", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return OverEditWarning{}
	}

	// Parse last line: "N files changed, X insertions(+), Y deletions(-)"
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return OverEditWarning{}
	}
	lastLine := lines[len(lines)-1]

	w := OverEditWarning{}
	parts := strings.Split(lastLine, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "file") {
			if n, err := strconv.Atoi(strings.Fields(part)[0]); err == nil {
				w.FilesChanged = n
			}
		}
		if strings.Contains(part, "insertion") || strings.Contains(part, "+") {
			if n, err := strconv.Atoi(strings.Fields(part)[0]); err == nil {
				w.LinesAdded = n
			}
		}
		if strings.Contains(part, "deletion") || strings.Contains(part, "-") {
			if n, err := strconv.Atoi(strings.Fields(part)[0]); err == nil {
				w.LinesRemoved = n
			}
		}
	}

	totalChanges := w.LinesAdded + w.LinesRemoved
	if maxExpectedLines > 0 && totalChanges > maxExpectedLines {
		w.IsOverEdit = true
		w.Detail = "Over-edit detected: fix changed " + strconv.Itoa(totalChanges) +
			" lines but was expected to change at most " + strconv.Itoa(maxExpectedLines) +
			" lines. Builder may have rewritten files instead of making targeted edits."
	}
	return w
}
