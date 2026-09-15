package main

import (
	"fmt"
	"math/rand"
	"strings"
	"unicode/utf8"
)

// executionCueBootstrap returns a natural, varied response when avatars is about
// to scaffold a new project. Avoids robotic repetition by randomizing phrasing.
func executionCueBootstrap(input string) string {
	return pickCue([]string{
		"OK — I'll scaffold the project, then verify.",
		"Got it. Scaffolding the project structure.",
		"Understood. I'll scaffold the skeleton and then check it.",
	})
}

// executionCueScript returns a natural response for script generation.
func executionCueScript(input string) string {
	return pickCue([]string{
		"OK — I'll write the script, then run verification.",
		"Got it. Generating the code.",
		"Sure. I'll write it and then check syntax.",
	})
}

// executionCueEdit returns a natural response for file editing.
func executionCueEdit(input string) string {
	target := extractTargetFile(input)
	if target != "" {
		return pickCue([]string{
			fmt.Sprintf("OK — I'll edit %s, then verify.", target),
			fmt.Sprintf("Got it. Editing %s.", target),
			fmt.Sprintf("Sure. Changing %s, then I'll check it.", target),
		})
	}
	return pickCue([]string{
		"OK — I'll edit the file, then verify.",
		"Got it. Starting the edit.",
		"Sure. I'll change it and then check syntax.",
	})
}

// executionCueDirect returns a natural response for direct action commands.
func executionCueDirect(input string) string {
	return pickCue([]string{
		"OK — running it directly.",
		"Got it.",
		"Sure.",
	})
}

// executionCueDefault returns a natural response for complex mutating tasks
// that will go through the Planner→Builder pipeline with approval.
func executionCueDefault(input string) string {
	return pickCue([]string{
		"Got it — I'll write the code. I'll confirm before applying changes.",
		"Understood. I'll plan first and confirm before editing.",
		"OK — I'll figure out the approach, then ask before changing code.",
	})
}

// executionCueAcceptEdits is for modes that write without an approval prompt
// (F100). Must not promise "ask / confirm" — acceptEdits already authorized writes.
func executionCueAcceptEdits(input string) string {
	return pickCue([]string{
		"Got it — I'll write the code, then verify.",
		"Understood. I'll implement it and check the result.",
		"OK — I'll write the files and run verification.",
	})
}

// executionCueForMutatingRun picks the ask-first cue only for permission-mode default.
func executionCueForMutatingRun(mode, input string) string {
	if strings.TrimSpace(mode) == "default" {
		return executionCueDefault(input)
	}
	return executionCueAcceptEdits(input)
}

// executionCuePlan returns a natural response for read-only analysis tasks.
func executionCuePlan(input string) string {
	return pickCue([]string{
		"OK — I'll take a look first (read-only).",
		"Got it. Analyzing read-only.",
		"Sure. Let me inspect the project (read-only).",
	})
}

// executionCueTaskIntro acknowledges the user's task with their own words,
// making the interaction feel more conversational and less robotic.
// Uses the architecture context when available to show understanding.
func executionCueTaskIntro(input string) string {
	trimmed := strings.TrimSpace(input)
	trimmed = truncateRunes(trimmed, 240)
	if trimmed == "" {
		return ""
	}

	intros := []string{
		"Got it — you want: %s",
		"Understood: %s",
		"Received: %s",
		"OK: %s",
	}
	return fmt.Sprintf(pickCue(intros), trimmed)
}

// pickCue selects a random element from a slice. Uses a simple deterministic
// approach to avoid importing crypto/rand while still providing variety.
func pickCue(options []string) string {
	if len(options) == 0 {
		return ""
	}
	return options[rand.Intn(len(options))]
}

// extractTargetFile attempts to extract a filename from the instruction text
// to make responses feel more personalized.
func extractTargetFile(input string) string {
	for _, ext := range []string{".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".html", ".css", ".yaml", ".yml", ".json", ".md"} {
		if idx := strings.Index(input, ext); idx >= 0 {
			start := idx
			for start > 0 {
				prev := input[start-1]
				if prev == ' ' || prev == '\t' || prev == '\n' || prev == '"' || prev == '\'' {
					break
				}
				start--
			}
			return input[start : idx+len(ext)]
		}
	}
	return ""
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if max > 1 {
		return string(runes[:max-1]) + "…"
	}
	return string(runes[:max])
}
