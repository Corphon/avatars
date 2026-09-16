package runtime

import (
	"fmt"
	"os"
	"strings"

	"avatars/internal/platform"
)

// buildGapInstructionWithErrors constructs a Builder dispatch instruction that
// includes ACTUAL build/test errors AND root-cause diagnosis guidance.
//
// Key insight (I31→I32): Builder was failing because the instruction said "FIX
// ERRORS" but didn't guide Builder to find the ROOT CAUSE.  Adding concrete
// diagnosis steps (read the failing file, find the bug pattern, understand WHY
// before fixing) matches how CC handles errors in a single agent flow.
func buildGapInstructionWithErrors(wd string, gapItems []string, originalLang string) string {
	var sb strings.Builder

	// Cross-language: go build/test, cargo, npm, pytest, tsc — not Go-only.
	buildErrs := filterErrorLines(projectCompileCheck(wd))
	testErrs := filterErrorLines(projectTestCheck(wd))
	hasSpecificErrors := len(buildErrs) > 0 || len(testErrs) > 0

	if hasSpecificErrors {
		sb.WriteString("DIAGNOSE AND FIX — read failing files, understand root cause, apply minimal fix:\n\n")
		sb.WriteString("Use edit_file / precise_edit for existing files. Do NOT rewrite whole files.\n\n")

		if len(buildErrs) > 0 {
			sb.WriteString("=== compile/check errors ===\n")
			for _, e := range buildErrs {
				sb.WriteString(e)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
		if len(testErrs) > 0 {
			sb.WriteString("=== test errors ===\n")
			for _, e := range testErrs {
				sb.WriteString(e)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}

		// Include the failing source code context so Builder sees the actual bug.
		sb.WriteString("=== FAILING SOURCE CODE ===\n")
		sb.WriteString(extractFailingCodeContext(wd, buildErrs, testErrs))
		sb.WriteString("\n")

		sb.WriteString("=== HOW TO FIX (READ CAREFULLY) ===\n")
		sb.WriteString("1. READ the failing code above. The ROOT CAUSE is in the code, not the error message.\n")
		sb.WriteString("2. ASK YOURSELF: why does this specific code produce this specific error?\n")
		sb.WriteString("   Examples of root-cause thinking:\n")
		sb.WriteString("   - 'Request.RequestURI can't be set' → httptest.NewRequest sets RequestURI,\n")
		sb.WriteString("     which Client.Do() rejects. FIX: use http.NewRequest instead.\n")
		sb.WriteString("   - 'unexpected end of JSON input' → the file is empty but json.Unmarshal is\n")
		sb.WriteString("     called on it. FIX: check len(data)==0 before unmarshalling.\n")
		sb.WriteString("   - 'undefined: Handler' → the type is defined but not imported. FIX: add import.\n")
		sb.WriteString("3. Make the SMALLEST possible change — one function, one line if possible.\n")
		sb.WriteString("4. Do NOT rewrite the entire file. Do NOT create new files.\n")
		sb.WriteString("5. After your fix, go build && go test must pass.\n")
	} else {
		sb.WriteString(fmt.Sprintf("FILL GAPS in original task. Language: %s\n\n", originalLang))
		sb.WriteString("Items NOT done:\n")
		for _, item := range gapItems {
			sb.WriteString(item)
			sb.WriteString("\n")
		}
		sb.WriteString("\nGenerate the actual code files. Only output files in the SAME language.\n")
	}

	return sb.String()
}

// extractFailingCodeContext reads source files referenced in error output and
// returns relevant code snippets.  This gives Builder the actual buggy code to
// analyze, not just error messages.
func extractFailingCodeContext(wd string, buildErrs, testErrs []string) string {
	allErrs := append(append([]string{}, buildErrs...), testErrs...)
	seenFiles := make(map[string]bool)
	var sb strings.Builder

	for _, errLine := range allErrs {
		// Extract file:line references like "handlers_test.go:518" or "./internal/handlers/handlers_test.go:46"
		file, lineNum := parseFileLine(errLine)
		if file == "" || seenFiles[file] {
			continue
		}
		seenFiles[file] = true

		// Try multiple paths.
		for _, candidate := range []string{
			wd + "/" + file,
			file,
		} {
			data, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			lines := strings.Split(string(data), "\n")
			start := lineNum - 5
			if start < 0 {
				start = 0
			}
			end := lineNum + 5
			if end > len(lines) {
				end = len(lines)
			}

			sb.WriteString(fmt.Sprintf("--- %s (lines %d-%d) ---\n", file, start+1, end))
			for i := start; i < end; i++ {
				marker := "  "
				if i+1 == lineNum {
					marker = ">>" // highlight the error line
				}
				sb.WriteString(fmt.Sprintf("%s %4d: %s\n", marker, i+1, lines[i]))
			}
			sb.WriteString("\n")
			break
		}
	}

	if sb.Len() == 0 {
		return "(could not locate failing source files)\n"
	}
	return sb.String()
}

// parseFileLine extracts file path and line number from a Go error message.
// Examples: "handlers_test.go:518: request failed" → ("handlers_test.go", 518)
// "internal/handlers/handlers_test.go:46:2: imported and not used" → ("internal/handlers/handlers_test.go", 46)
func parseFileLine(errLine string) (string, int) {
	// Find the first ":<digits>:" pattern.
	for i := 0; i < len(errLine); i++ {
		if errLine[i] == ':' && i+1 < len(errLine) && errLine[i+1] >= '0' && errLine[i+1] <= '9' {
			// Found ":digit" — extract file path before this colon.
			file := errLine[:i]
			// Skip leading "./" if present.
			file = strings.TrimPrefix(file, "./")
			// Extract line number.
			j := i + 1
			for j < len(errLine) && errLine[j] >= '0' && errLine[j] <= '9' {
				j++
			}
			lineNum := 0
			fmt.Sscanf(errLine[i+1:j], "%d", &lineNum)
			if lineNum > 0 {
				return file, lineNum
			}
		}
	}
	return "", 0
}

func runGoBuild(wd string) []string {
	cmd := platform.Command("go", "build", "./...")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return filterErrorLines(string(out))
}

func runGoTest(wd string) []string {
	cmd := platform.Command("go", "test", "./...")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return filterErrorLines(string(out))
}

func filterErrorLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "? ") && strings.Contains(trimmed, "[no test files]") {
			continue
		}
		lines = append(lines, trimmed)
	}
	if len(lines) > 20 {
		lines = lines[:20]
		lines = append(lines, "... (truncated)")
	}
	return lines
}
