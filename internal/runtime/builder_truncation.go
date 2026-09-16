package runtime

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/llm"
)

// =============================================================================
// BUILDER TRUNCATION RECOVERY — I35
// Adapted from claude_code_main query.ts (two-phase escalation) and
// services/api/claude.ts (stop_reason === 'max_tokens' detection).
// =============================================================================

const (
	// DefaultBuilderMaxTokens is the daily codegen output request.
	// Prefer llm.DefaultMaxTokensForCode() at call sites; this constant
	// remains for escalation math and tests.
	DefaultBuilderMaxTokens = 16384

	// EscalatedBuilderMaxTokens is the truncation-recovery ceiling.
	// Kept at the harness hard cap, not the provider's theoretical maximum.
	EscalatedBuilderMaxTokens = 65536

	// DefaultBuilderMaxToolTurns is the default tool-calling loop limit for Builder.
	// TR-Fix-10: Raised from 5. Each file needs 1 turn (write_file/edit_file),
	// plus read_file overhead. 8 covers ~5 files + 3 reads.
	// IMPORTANT: Higher values cause the LLM to waste turns on unnecessary calls,
	// ballooning token consumption (O(turns²) context growth). The default is 8.
	DefaultBuilderMaxToolTurns = 8

	// PerFileMaxToolTurns is the tool-calling limit for per-file modification passes.
	// Each pass handles a single file, so fewer turns are needed.
	PerFileMaxToolTurns = 6
)

// MaxTruncationRecoveries is the hard limit on consecutive recovery attempts.
// TR-Fix-8: Made a var (was const) for runtime config overrides.
// Pattern from claude_code_main's MAX_OUTPUT_TOKENS_RECOVERY_LIMIT = 3.
var MaxTruncationRecoveries = 3

// SetMaxTruncationRecoveries overrides the default recovery limit.
// TR-Fix-8: Call during startup from config loader.
func SetMaxTruncationRecoveries(n int) {
	if n > 0 && n <= 10 {
		MaxTruncationRecoveries = n
	}
}

// builderMaxToolTurns returns the max tool turns for Builder code generation.
// TR-Fix-10: Checks per-role config override first, then falls back to
// DefaultBuilderMaxToolTurns.
func builderMaxToolTurns() int {
	if v := llm.MaxToolTurnsForRole("builder"); v > 0 {
		return v
	}
	return DefaultBuilderMaxToolTurns
}

func looksLikeSourceCodePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	switch ext {
	case ".go", ".py", ".rs", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs",
		".java", ".kt", ".c", ".h", ".cpp", ".cc", ".hpp", ".cs", ".rb", ".php", ".swift":
		return true
	default:
		return false
	}
}

// TruncationResult describes whether the Builder output was truncated
// and which files are incomplete.
type TruncationResult struct {
	IsTruncated     bool
	FinishReason    string
	IncompleteFiles []string
	Issues          []string // human-readable descriptions
}

// CheckBuilderTruncation checks if Builder output appears truncated.
// Uses two signals:
//   1. API finish_reason="length" (authoritative — the provider said it cut off)
//   2. Code completeness heuristics (defense-in-depth for when the API doesn't signal)
//
// Files already applied via surgical edit (AlreadyOnDisk) are skipped: their
// Content field is intentionally empty and must not trigger "empty file"
// truncation recovery (E4 / cross-language).
func CheckBuilderTruncation(resp llm.Response, files []builderCodeFile) TruncationResult {
	result := TruncationResult{
		FinishReason: resp.FinishReason,
	}

	// Signal 1: API-level truncation.
	if llm.IsTruncated(resp) {
		result.IsTruncated = true
		result.Issues = append(result.Issues,
			fmt.Sprintf("API finish_reason=%q — provider truncated output", resp.FinishReason))
	}

	// Signal 2: Code completeness heuristics on source files only.
	// Markdown/JSON/YAML brace noise must not trigger another generate turn.
	for _, f := range files {
		if f.AlreadyOnDisk {
			continue
		}
		if isWorkflowDocPath(f.Path) || !looksLikeSourceCodePath(f.Path) {
			continue
		}
		content := contentForTruncationCheck(f)
		if sourceLooksComplete(f.Path, content) {
			continue
		}
		issues := codeCompletenessIssues(content)
		if len(issues) > 0 {
			result.IncompleteFiles = append(result.IncompleteFiles, f.Path)
			for _, issue := range issues {
				result.Issues = append(result.Issues,
					fmt.Sprintf("%s: %s", f.Path, issue))
			}
		}
	}

	result.IncompleteFiles = uniqueStrings(result.IncompleteFiles)
	if len(result.IncompleteFiles) > 0 && !result.IsTruncated {
		// Heuristic-only detection — the API didn't signal but code looks broken.
		result.IsTruncated = true
	}
	if result.IsTruncated && !llm.IsTruncated(resp) && len(result.IncompleteFiles) == 0 {
		result.IsTruncated = false
	}

	return result
}

func contentForTruncationCheck(f builderCodeFile) string {
	if strings.TrimSpace(f.Content) != "" {
		return f.Content
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return f.Content
	}
	return string(data)
}

func sourceLooksComplete(path, content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".go" {
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, path, content, parser.ParseComments); err == nil {
			return true
		}
	}
	return len(codeCompletenessIssues(content)) == 0
}

func uniqueStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func isWorkflowDocPath(path string) bool {
	slash := filepath.ToSlash(strings.ToLower(strings.TrimSpace(path)))
	if slash == "" {
		return false
	}
	base := filepath.Base(slash)
	if strings.HasPrefix(base, "phase") && strings.HasSuffix(base, ".md") {
		return true
	}
	switch base {
	case "avatars_plan.md", "avatars_todo.md", "process_record.md",
		"user_requirement.md", "architecture.md":
		return true
	}
	return strings.Contains(slash, "/docs/workflow/")
}

// EscalateMaxTokens returns the next MaxTokens level after a truncation.
// Daily → double → harness ceiling. Does not treat the model output
// maximum as the everyday request.
func EscalateMaxTokens(current int) int {
	if current <= 0 || current < DefaultBuilderMaxTokens {
		return DefaultBuilderMaxTokens
	}
	next := current * 2
	if next > EscalatedBuilderMaxTokens {
		return EscalatedBuilderMaxTokens
	}
	return next
}

// BuildRecoveryPrompt creates a continuation prompt for the Builder retry.
//
// TR-Fix-4: Removed contradictory instructions. The old prompt told the model to
// "Pick up mid-thought" AND "regenerated COMPLETELY" AND "EMIT CODE DIRECTLY" —
// three conflicting signals. Now aligned with claude_code_main query.ts:1226 pattern:
// a single, consistent instruction: resume from where it stopped.
//
// Pattern adapted from claude_code_main query.ts:1226-1227:
//
//	"Output token limit hit. Resume directly — no apology, no recap of what you
//	 were doing. Pick up mid-thought if that is where the cut happened.
//	 Break remaining work into smaller pieces."
// BuildRecoveryPromptWithContent creates a continuation prompt for the Builder
// retry, optionally including the truncated content prefix so the LLM knows
// exactly where to resume. TR-Fix-6.
//
// incompleteFiles: paths of truncated files.
// fileContentPrefixes: optional map of path → first N chars of truncated content.
// Pass nil if content is unavailable.
func BuildRecoveryPromptWithContent(incompleteFiles []string, fileContentPrefixes map[string]string) string {
	var b strings.Builder
	b.WriteString("\n\n=== RECOVERY: Previous output was truncated by token limit ===\n")
	b.WriteString("Your previous response was cut off. Resume directly — no apology, no recap.\n")
	b.WriteString("Pick up mid-thought if that is where the cut happened.\n")
	b.WriteString("Break remaining work into smaller pieces.\n")

	if len(incompleteFiles) > 0 {
		b.WriteString("\nThe following files were truncated. Continue each from where it stopped:\n")
		for _, path := range incompleteFiles {
			b.WriteString("- " + path)
			if prefix, ok := fileContentPrefixes[path]; ok && prefix != "" {
				// TR-Fix-6: Include existing content prefix so LLM knows
				// where to continue. Cap at 500 chars to save context.
				display := prefix
				if len(display) > 500 {
					display = display[:500] + "\n// [...truncated...]"
				}
				b.WriteString("\n```\n" + display + "\n```\n")
			} else {
				b.WriteString(" (continue from the cut-off point, do not repeat existing content)\n")
			}
		}
	}

	b.WriteString("\nDo not re-explain the task. Produce ONLY the continuation of the remaining work.\n")
	return b.String()
}

// BuildRecoveryPrompt is a convenience wrapper that matches the old signature.
func BuildRecoveryPrompt(incompleteFiles []string) string {
	return BuildRecoveryPromptWithContent(incompleteFiles, nil)
}

// codeCompletenessIssues checks for signs of truncation in generated code.
// Returns empty slice if code appears complete, or a list of issue descriptions.
func codeCompletenessIssues(content string) []string {
	if len(strings.TrimSpace(content)) == 0 {
		return []string{"empty file"}
	}

	var issues []string

	stripped, unclosed := llm.SourceWithoutLiterals(content)
	if unclosed {
		issues = append(issues, "unclosed string or comment")
	}

	// Braces outside strings/comments are a real cut-off signal.
	// Parentheses and brackets are not: error strings, []byte, generics,
	// and doc comments routinely mismatch a naive count in every common language.
	openBraces := strings.Count(stripped, "{")
	closeBraces := strings.Count(stripped, "}")
	if openBraces != closeBraces {
		issues = append(issues,
			fmt.Sprintf("unbalanced braces: %d open, %d close", openBraces, closeBraces))
	}

	// Check 4: Last non-empty, non-comment line analysis.
	lines := strings.Split(content, "\n")
	lastLine := lastSignificantLine(lines)
	if lastLine != "" {
		trimmed := strings.TrimSpace(lastLine)

		// Incomplete Go / Rust / TS constructs that shouldn't be the last line.
		incompletePrefixes := []string{
			"if ", "for ", "switch ", "select ", "case ", "default:",
			"func ", "type ", "var ", "const ", "import ",
			"else", "else if",
			"fn ", "pub fn ", "pub async fn ", "async fn ", "impl ", "struct ", "enum ", "mod ",
			"def ", "class ", "async def ",
		}
		for _, prefix := range incompletePrefixes {
			if strings.HasPrefix(trimmed, prefix) && !strings.Contains(trimmed, "{") && !strings.HasSuffix(trimmed, ":") {
				issues = append(issues,
					fmt.Sprintf("trailing incomplete construct: %q", trimmed))
				break
			}
		}

		// Mid-signature cutoffs: "-> (StatusCode, Json" / trailing comma / bare "Result"
		if strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, "->") ||
			strings.HasSuffix(trimmed, "Result") ||
			(strings.Contains(trimmed, "->") && !strings.Contains(trimmed, "{") &&
				!strings.HasSuffix(trimmed, ";") && !strings.HasSuffix(trimmed, "}")) {
			issues = append(issues,
				fmt.Sprintf("trailing truncated signature/type: %q", truncatedStr(trimmed, 80)))
		}

		// R11-2: incomplete type annotation — "let notes: Vec" / "x: Option"
		if idx := strings.LastIndex(trimmed, ":"); idx >= 0 {
			after := strings.TrimSpace(trimmed[idx+1:])
			if after != "" && !strings.ContainsAny(after, ";{}()=<>,") &&
				!strings.HasSuffix(trimmed, ";") && !strings.HasSuffix(trimmed, "{") &&
				!strings.HasSuffix(trimmed, ",") {
				issues = append(issues,
					fmt.Sprintf("trailing incomplete type annotation: %q", truncatedStr(trimmed, 80)))
			}
		}

		// Check for proper termination characters.
		if !strings.HasSuffix(trimmed, "}") &&
			!strings.HasSuffix(trimmed, ")") &&
			!strings.HasSuffix(trimmed, ";") &&
			!strings.HasSuffix(trimmed, "`") &&
			!strings.HasSuffix(trimmed, "\"") &&
			!strings.HasSuffix(trimmed, "//") &&
			!looksLikeCommentLine(trimmed) {
			// Only flag if file is substantial (>500 chars) and already has other issues.
			if len(content) > 500 && len(issues) > 0 {
				issues = append(issues,
					fmt.Sprintf("last line lacks termination: %q", truncatedStr(trimmed, 80)))
			}
		}
	}

	// Check 5: Unclosed backtick strings (raw string literals in Go).
	backtickCount := strings.Count(content, "`")
	if backtickCount%2 != 0 {
		issues = append(issues, "unclosed backtick string literal")
	}

	return issues
}

// lastSignificantLine returns the last non-empty, non-comment-only line.
func lastSignificantLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		// Skip pure comment lines.
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return trimmed
	}
	return ""
}

func looksLikeCommentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") ||
		strings.HasSuffix(trimmed, "*/") || strings.HasPrefix(trimmed, "#")
}

func truncatedStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func builderFilesLookComplete(files []builderCodeFile) bool {
	seen := 0
	for _, f := range files {
		if f.AlreadyOnDisk || isWorkflowDocPath(f.Path) || !looksLikeSourceCodePath(f.Path) {
			continue
		}
		seen++
		if !sourceLooksComplete(f.Path, contentForTruncationCheck(f)) {
			return false
		}
	}
	return seen > 0
}

// =============================================================================
// BUILDER RETRY STATE PERSISTENCE — TR-Fix-12
// Persists builderRetryStates to .avatars/ so truncation recovery state
// survives engine restarts and resume cycles.
// =============================================================================

const builderRetryStateFile = "builder_retry_state.json"

// persistBuilderRetryStates writes the builder retry states to disk.
// Called after truncation escalation to ensure recovery state is durable.
func persistBuilderRetryStates(projectRoot string, states map[string]*builderRetryState) error {
	if len(states) == 0 {
		return nil
	}
	avatarsDir := filepath.Join(projectRoot, ".avatars")
	if err := os.MkdirAll(avatarsDir, 0755); err != nil {
		return fmt.Errorf("persist builder retry state: mkdir: %w", err)
	}
	path := filepath.Join(avatarsDir, builderRetryStateFile)
	data, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return fmt.Errorf("persist builder retry state: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("persist builder retry state: write: %w", err)
	}
	return nil
}

// loadBuilderRetryStates reads the builder retry states from disk.
// Returns nil if the file doesn't exist (first run).
func loadBuilderRetryStates(projectRoot string) (map[string]*builderRetryState, error) {
	path := filepath.Join(projectRoot, ".avatars", builderRetryStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load builder retry state: read: %w", err)
	}
	var states map[string]*builderRetryState
	if err := json.Unmarshal(data, &states); err != nil {
		return nil, fmt.Errorf("load builder retry state: unmarshal: %w", err)
	}
	return states, nil
}

const nodeRetryCountFile = "node_retry_counts.json"

// persistNodeRetryCounts writes per-node failure counts (S4.2 / S4.3).
func persistNodeRetryCounts(projectRoot string, counts map[string]int) error {
	if len(counts) == 0 {
		return nil
	}
	avatarsDir := filepath.Join(projectRoot, ".avatars")
	if err := os.MkdirAll(avatarsDir, 0755); err != nil {
		return fmt.Errorf("persist node retry counts: mkdir: %w", err)
	}
	path := filepath.Join(avatarsDir, nodeRetryCountFile)
	data, err := json.MarshalIndent(counts, "", "  ")
	if err != nil {
		return fmt.Errorf("persist node retry counts: marshal: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// loadNodeRetryCounts restores per-node failure counts on resume (S4.2).
func loadNodeRetryCounts(projectRoot string) (map[string]int, error) {
	path := filepath.Join(projectRoot, ".avatars", nodeRetryCountFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load node retry counts: read: %w", err)
	}
	var counts map[string]int
	if err := json.Unmarshal(data, &counts); err != nil {
		return nil, fmt.Errorf("load node retry counts: unmarshal: %w", err)
	}
	return counts, nil
}
