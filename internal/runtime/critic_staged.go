// Package runtime — staged quality gate for Critic.
//
// P5 fix: Adopts Claude Code's staged early-exit pattern
// (claude_code_main/verificationAgent.ts:42-43) to avoid 10min
// timeouts when code is trivially broken.
//
// The three stages:
//   Stage 1: Syntax (go/parser, py_compile heuristic, node -c) — <1s
//   Stage 2: Compilation (go build via existing quickGoBuildCheck) — <30s
//   Stage 3: Adversarial audit — full Decide flow, up to 3min
//
// Any stage failure returns immediately; subsequent stages only run
// if all previous stages passed.

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/projectfiles"
)

// stagedQualityGate runs fast-then-slow checks on Builder-generated files.
// Returns a non-empty CriticHubResult if a stage fails. Empty result means
// "all fast checks passed, continue to full adversarial review."
func (ch *CriticHub) stagedQualityGate(wd string, work workflowNodeWorkContext) CriticHubResult {
	// S4.8: Prefer files changed in this run; fall back to tree scan only when empty.
	generatedFiles := ch.filesForStagedGate(wd)
	if len(generatedFiles) == 0 {
		return CriticHubResult{}
	}

	// ---- Stage 1: Syntax (<1s) ----
	for _, f := range generatedFiles {
		ext := strings.ToLower(filepath.Ext(f))
		switch ext {
		case ".go":
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if _, err := parseGoFile(f, string(data)); err != nil {
				ch.emit(work, "critic.stage1_syntax_fail", map[string]any{
					"file": f, "error": err.Error(), "stage": "syntax",
				})
				return CriticHubResult{
					Decision:     fmt.Sprintf("Stage 1 (syntax) failed: %s — %s", filepath.Base(f), err.Error()),
					ErrorMessage: fmt.Sprintf("syntax error in %s: %s", f, err.Error()),
				}
			}
		case ".py":
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if reason := checkPythonSyntax(f, string(data)); reason != "" {
				ch.emit(work, "critic.stage1_syntax_fail", map[string]any{
					"file": f, "error": reason, "stage": "syntax",
				})
				return CriticHubResult{
					Decision:     fmt.Sprintf("Stage 1 (syntax) failed: %s — %s", filepath.Base(f), reason),
					ErrorMessage: reason,
				}
			}
		case ".js", ".mjs":
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if reason := checkNodeJSSyntax(f, string(data)); reason != "" {
				ch.emit(work, "critic.stage1_syntax_fail", map[string]any{
					"file": f, "error": reason, "stage": "syntax",
				})
				return CriticHubResult{
					Decision:     fmt.Sprintf("Stage 1 (syntax) failed: %s — %s", filepath.Base(f), reason),
					ErrorMessage: reason,
				}
			}
		case ".rs", ".ts", ".tsx":
			// R11-2: catch truncated Rust/TS before compile thrash.
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if reason := checkFileHealth(f, string(data)); reason != "" {
				ch.emit(work, "critic.stage1_syntax_fail", map[string]any{
					"file": f, "error": reason, "stage": "syntax",
				})
				return CriticHubResult{
					Decision:     fmt.Sprintf("Stage 1 (syntax) failed: %s — %s", filepath.Base(f), reason),
					ErrorMessage: reason,
				}
			}
		}
	}

	// ---- Stage 1.5: Static analysis (<15s) ----
	// TR-Fix-13: Language-agnostic static analysis catches logic bugs that
	// syntax+compile miss: nil derefs, unreachable code, error吞没,
	// race conditions, printf format errors, loop variable capture, etc.
	if findings := runStaticAnalysis(wd); len(findings) > 0 {
		errCount := 0
		var detailLines []string
		for _, f := range findings {
			if f.Severity == "error" {
				errCount++
			}
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, f.Line)
			}
			detailLines = append(detailLines, fmt.Sprintf("[%s] %s: %s", f.Tool, loc, f.Message))
		}
		detail := strings.Join(detailLines, "\n")
		ch.emit(work, "critic.stage1_5_static_analysis", map[string]any{
			"error_count":   errCount,
			"warning_count": len(findings) - errCount,
			"stage":         "static_analysis",
			"detail":        detail,
		})
		if errCount > 0 {
			return CriticHubResult{
				Decision:     fmt.Sprintf("Stage 1.5 (static analysis) found %d errors, %d warnings", errCount, len(findings)-errCount),
				ErrorMessage: detail,
			}
		}
		// Warnings only — continue to next stage but log them.
	}

	// ---- Stage 2: Compilation (<30s) — language-agnostic via health aggregator ----
	if errStr := projectCompileCheck(wd); errStr != "" {
		ch.emit(work, "critic.stage2_compile_fail", map[string]any{
			"error": errStr, "stage": "compile",
		})
		return CriticHubResult{
			Decision:     fmt.Sprintf("Stage 2 (compile) failed: %s", errStr),
			ErrorMessage: errStr,
		}
	}

	// ---- Stage 2.5: Test execution — go test / npm test / pytest / cargo test ----
	if errStr := projectTestCheck(wd); errStr != "" {
		ch.emit(work, "critic.stage2_5_test_fail", map[string]any{
			"error": errStr, "stage": "test",
		})
		return CriticHubResult{
			Decision:     fmt.Sprintf("Stage 2.5 (test) failed: %s", errStr),
			ErrorMessage: errStr,
		}
	}

	// ---- Stage 3: Adversarial (deferred to full Decide flow) ----
	return CriticHubResult{} // all fast checks passed
}

// scanRecentSourceFiles returns .go/.py/.js/.mjs files in the working
// directory tree, skipping vendor, .avatars, node_modules, and .git.
func scanRecentSourceFiles(wd string) []string {
	var files []string
	_ = projectfiles.WalkDirCapped(wd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".py", ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx",
			".rs", ".java", ".kt", ".cs":
			files = append(files, path)
		}
		return nil
	})
	return files
}

// filesForStagedGate returns changedFiles when available (S4.8), else full scan.
func (ch *CriticHub) filesForStagedGate(wd string) []string {
	if ch != nil && ch.engine != nil && len(ch.engine.changedFiles) > 0 {
		out := make([]string, 0, len(ch.engine.changedFiles))
		for _, f := range ch.engine.changedFiles {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if !filepath.IsAbs(f) {
				f = filepath.Join(wd, f)
			}
			ext := strings.ToLower(filepath.Ext(f))
			switch ext {
			case ".go", ".py", ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx",
				".rs", ".java", ".kt", ".cs":
				out = append(out, f)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return scanRecentSourceFiles(wd)
}

// runNodeCheck runs `node -c` on the given file.
// Returns "" on success, error reason on failure.
// Requires the file to already exist on disk (skips check otherwise).
func runNodeCheck(filePath string) string {
	if _, err := exec.LookPath("node"); err != nil {
		return "" // node not available
	}
	// P3-fix: Only run node -c if the file exists on disk.
	// Heuristic checks in checkNodeJSSyntax handle in-memory content.
	if _, err := os.Stat(filePath); err != nil {
		return "" // file doesn't exist yet — heuristic layer already ran
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-c", filePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		errStr := string(output)
		errStr = clipCommandOutput(errStr, 1200)
		return fmt.Sprintf("node -c: %s", strings.TrimSpace(errStr))
	}
	return ""
}

// checkNodeJSSyntax runs heuristic checks on JS content,
// then delegates to node -c for precise validation.
func checkNodeJSSyntax(path string, content string) string {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) < 20 {
		return fmt.Sprintf("JS file %s is too small (%d bytes) — likely truncated", path, len(trimmed))
	}

	// Check last meaningful line for truncated constructs.
	lines := strings.Split(content, "\n")
	lastLine := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			lastLine = strings.TrimSpace(lines[i])
			break
		}
	}

	truncatedPrefixes := []string{
		"if ", "if(",
		"for ", "for(",
		"while ", "while(",
		"function ", "function(",
		"console.log('", "console.log(`",
	}
	for _, prefix := range truncatedPrefixes {
		if strings.HasPrefix(lastLine, prefix) {
			if !strings.HasSuffix(lastLine, ";") && !strings.HasSuffix(lastLine, "}") &&
				!strings.Contains(lastLine, "{") && !strings.Contains(lastLine, "=>") {
				return fmt.Sprintf("JS file %s appears truncated: last line starts with '%s' with no body",
					path, prefix)
			}
		}
	}

	// Check brace balance.
	openBraces := strings.Count(content, "{")
	closeBraces := strings.Count(content, "}")
	if openBraces != closeBraces {
		return fmt.Sprintf("JS file %s has unbalanced braces: %d { vs %d }", path, openBraces, closeBraces)
	}
	// J2-1: also catch mid-call truncations like Math.abs(now - createdTime
	openParens := strings.Count(content, "(")
	closeParens := strings.Count(content, ")")
	if openParens != closeParens {
		return fmt.Sprintf("JS file %s has unbalanced parentheses: %d ( vs %d )", path, openParens, closeParens)
	}
	if lastLine != "" && !strings.HasSuffix(lastLine, ";") && !strings.HasSuffix(lastLine, "}") &&
		!strings.HasSuffix(lastLine, ")") && !strings.HasSuffix(lastLine, "`") &&
		!strings.HasSuffix(lastLine, "\"") && !strings.HasSuffix(lastLine, "'") &&
		(strings.HasSuffix(lastLine, ",") || strings.Contains(lastLine, "(") && !strings.Contains(lastLine, ")")) {
		return fmt.Sprintf("JS file %s appears truncated at last line: %q", path, lastLine)
	}

	// Layer 2: node -c only when disk already matches this content (post-write /
	// purge). Pre-write health must not validate against a stale truncated file (J2-1).
	if onDisk, err := os.ReadFile(path); err == nil && string(onDisk) == content {
		if reason := runNodeCheck(path); reason != "" {
			return fmt.Sprintf("JS file %s: %s", path, reason)
		}
	}

	return ""
}
