package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"avatars/internal/workflow"
)

// apiLockCache avoids re-scanning all Go files on every Builder call.
var apiLockCache sync.Map

// criticEscalateToPlanner is called when the Critic's Builder dispatch loop stalls.
// It emits an escalation event and dispatches the Builder once more with a
// simplified, focused prompt that explicitly lists what's missing.
// v4-P1-2: Gap items are filtered to only reference files that actually exist
// on disk, preventing hallucinated file paths from misleading the Builder.
// #1: Injects existing type/function signatures as "API lock" so Builder
// doesn't create incompatible duplicate types on retry.
// #2: Runs go build first and injects compilation errors so Builder can
// fix the EXACT problem rather than guessing.
func (e *Engine) criticEscalateToPlanner(work workflowNodeWorkContext, runID, taskID string, remaining, total int) {
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		return
	}
	_ = e.emit(runID, taskID, work.criticAvatarID, "reviewing", "critic.escalating_to_planner", "runtime", map[string]any{
		"remaining": remaining, "total": total,
		"reason": "Builder dispatch loop stalled — escalating with focused prompt",
	}, nil)

	gapItems := workflow.GetRemainingChecklistItems(wd)
	// v4-P1-2: Only include gap items that reference real files or are
	// general instructions (not file-specific).
	validItems := filterGapItemsByFileExistence(gapItems, wd)

	// #1: Extract existing type/function signatures from disk as "API lock".
	apiLock := extractAPILock(wd)

	// #2: Run go build and capture compilation errors.
	compileErrors := captureCompileErrors(wd)
	if compileErrors == "" {
		compileErrors = crossLangHealthCheck(wd)
	}

	// IM-Fix-2: Inject stripped import feedback so Builder doesn't
	// repeat hallucinated imports on retry.
	strippedMsg := buildStrippedImportFeedback(e.strippedImports)
	e.strippedImports = nil // clear after injection

	// IM-Fix-6: Detect missing imports from "undefined: X" errors.
	missingImportHints := detectMissingImports(compileErrors, apiLock)

	// TR-Fix-13: Language-agnostic static analysis for logic bugs.
	staticAnalysisReport := buildStaticAnalysisReport(wd)

	focusedInput := fmt.Sprintf("CRITICAL: %d/%d items still incomplete.\n\n%s\n\n%s\n\n%s\n\n%s\n\n%s\n\n%s\n\nImplement ONLY these items. One file at a time. Do NOT read files — write code directly.\nIMPORTANT: Only modify files that ALREADY EXIST on disk. Do NOT reference or create files at paths that don't exist.",
		remaining, total, strings.Join(validItems, "\n"), apiLock, compileErrors, strippedMsg, missingImportHints, staticAnalysisReport)

	rePlanWork := work
	rePlanWork.input = focusedInput
	rePlanWork.customSystemPrompt = criticFixSystemPrompt // WR-Fix-1
	if _, replanErr := e.executeBuilderNodeWork(context.Background(), rePlanWork); replanErr == nil {
		workflow.CriticAuditAndMark(wd)
	}
}

// detectMissingImports parses "undefined: X" errors from go build output and
// cross-references with the API lock to suggest which import the Builder needs
// to add. IM-Fix-6: Turns vague "undefined" errors into actionable hints.
func detectMissingImports(compileErrors string, apiLock string) string {
	if compileErrors == "" || apiLock == "" {
		return ""
	}
	// Extract undefined symbols from compile errors.
	re := regexp.MustCompile(`undefined:\s+(\w+)`)
	matches := re.FindAllStringSubmatch(compileErrors, -1)
	if len(matches) == 0 {
		return ""
	}
	var hints []string
	seen := map[string]bool{}
	for _, m := range matches {
		if len(m) >= 2 {
			symbol := m[1]
			if seen[symbol] {
				continue
			}
			seen[symbol] = true
			// Check if this symbol exists in the API lock (meaning it's
			// defined somewhere but the Builder forgot to import it).
			if strings.Contains(apiLock, symbol) {
				hints = append(hints, fmt.Sprintf(
					"MISSING IMPORT: %q is defined in the codebase but not imported. Add the correct import statement for it.", symbol))
			}
		}
	}
	if len(hints) == 0 {
		return ""
	}
	return "=== MISSING IMPORT HINTS (IM-Fix-6) ===\n" + strings.Join(hints, "\n") + "\n"
}

// buildStrippedImportFeedback formats a list of previously stripped hallucinated
// imports into a warning that can be injected into the Builder's retry prompt.
// IM-Fix-2: Prevents the LLM from repeating the same bad imports on retry.
func buildStrippedImportFeedback(warnings []DependencyWarning) string {
	if len(warnings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("=== PREVIOUSLY STRIPPED IMPORTS (do NOT re-import these) ===\n")
	sb.WriteString("The following imports were removed from your previous output because they are NOT in go.mod:\n")
	seen := map[string]bool{}
	for _, dw := range warnings {
		if seen[dw.ImportPath] {
			continue
		}
		seen[dw.ImportPath] = true
		sb.WriteString("- ")
		sb.WriteString(dw.ImportPath)
		if dw.StdlibAlt != "" {
			sb.WriteString(" → use ")
			sb.WriteString(dw.StdlibAlt)
			sb.WriteString(" instead")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Use ONLY the go.mod module path prefix for internal imports.\n")
	return sb.String()
}

// criticCheckImplCompleteness checks that all symbols called in generated code
// actually exist. If missing symbols are found, it dispatches the Builder to
// implement them. Returns true if fixes were attempted.
// v4-P1-2: Missing items are filtered to only reference files that exist on disk.
// #1+#2: Injects API lock and compilation errors into the Builder dispatch.
func (e *Engine) criticCheckImplCompleteness(work workflowNodeWorkContext, runID, taskID string) bool {
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		return false
	}
	// Batch5/M / G11: if the project already compiles with real sources, symbol-
	// scan false positives must not restart Builder. Empty disks never skip.
	if projectLooksSkipGreenSafe(wd) {
		return false
	}
	missing := workflow.CheckImplementationCompleteness(wd)
	if len(missing) == 0 {
		return false
	}
	// v4-P1-2: Filter out items referencing non-existent files.
	validMissing := filterGapItemsByFileExistence(missing, wd)
	if len(validMissing) == 0 {
		return false
	}
	if e != nil && !e.beginCriticBuilderCycle("impl_completeness") {
		_ = e.emit(runID, taskID, work.criticAvatarID, "reviewing", "critic.cycle_cap_skip_impl", "runtime", map[string]any{
			"cycles": e.criticBuilderCycles,
		}, nil)
		return false
	}
	_ = e.emit(runID, taskID, work.criticAvatarID, "reviewing", "critic.impl_incomplete", "runtime", map[string]any{
		"missing":      validMissing,
		"filtered_out": len(missing) - len(validMissing),
		"reason":       "Builder output references symbols that don't exist — dispatching fix",
	}, nil)

	// #1: Extract existing type/function signatures as "API lock".
	apiLock := extractAPILock(wd)
	// #2: Run go build and capture compilation errors.
	compileErrors := captureCompileErrors(wd)

	gapWork := work
	gapWork.input = "MISSING IMPLEMENTATIONS:\n" + strings.Join(validMissing, "\n") +
		"\n\n" + apiLock +
		"\n\n" + compileErrors +
		"\n\nImplement ONLY these missing parts." +
		"\nIMPORTANT: Only modify files that ALREADY EXIST on disk. Do NOT reference or create files at paths that don't exist."
	gapWork.customSystemPrompt = criticFixSystemPrompt // WR-Fix-1
	_, fixErr := e.executeBuilderNodeWork(context.Background(), gapWork)
	if fixErr == nil {
		workflow.CriticAuditAndMark(wd)
	}
	return true
}

// extractAPILock scans Go source files in projectRoot and extracts all
// exported type/struct/func signatures as a text block. This is injected
// into Builder retry prompts as an "API lock" — Builder must use these
// EXACT signatures and must NOT create duplicate incompatible types.
// #1: Prevents cross-file API inconsistency on retry.
func extractAPILock(projectRoot string) string {
	if cached, ok := apiLockCache.Load(projectRoot); ok {
		return cached.(string)
	}
	ctx := extractStructuredContext("", collectGoFilePaths(projectRoot))
	if ctx == nil || ctx.Empty() {
		return ""
	}
	// IM-Fix-4: Inject module path so Builder uses correct import prefixes.
	modulePath, _ := parseGoMod(projectRoot)
	var b strings.Builder
	if modulePath != "" {
		b.WriteString("=== MODULE PATH (use this exact prefix for ALL internal imports) ===\n")
		b.WriteString(modulePath)
		b.WriteString("\nDO NOT use relative paths or directory names as import prefixes.\n\n")
	}
	b.WriteString("=== EXISTING API LOCK (use these EXACT signatures — do NOT create duplicate types) ===\n")
	if len(ctx.StructDecls) > 0 {
		b.WriteString("-- STRUCTS (do NOT redefine these with different fields) --\n")
		for _, s := range ctx.StructDecls {
			b.WriteString(fmt.Sprintf("type %s struct {\n", s.Name))
			for _, f := range s.Fields {
				b.WriteString(fmt.Sprintf("  %s %s\n", f.Name, f.Type))
			}
			b.WriteString("}\n")
			b.WriteString(fmt.Sprintf("  // defined in %s, package %s\n", s.File, s.Package))
		}
		b.WriteString("\n")
	}
	if len(ctx.FuncDecls) > 0 {
		b.WriteString("-- FUNCTIONS (do NOT create functions with conflicting signatures) --\n")
		for _, f := range ctx.FuncDecls {
			b.WriteString(fmt.Sprintf("func %s(%s) %s\n", f.Name, f.Params, f.Returns))
			b.WriteString(fmt.Sprintf("  // defined in %s, package %s\n", f.File, f.Package))
		}
		b.WriteString("\n")
	}
	if len(ctx.InterfaceDecls) > 0 {
		b.WriteString("-- INTERFACES (implement these EXACT methods) --\n")
		for _, iface := range ctx.InterfaceDecls {
			b.WriteString(fmt.Sprintf("type %s interface {\n", iface.Name))
			for _, m := range iface.Methods {
				b.WriteString(fmt.Sprintf("  %s(%s) %s\n", m.Name, m.Params, m.Returns))
			}
			b.WriteString("}\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("RULE: If a type/function already exists in the API lock above, USE IT. Do NOT create a new type with the same name but different fields.\n")
	result := b.String()
	apiLockCache.Store(projectRoot, result)
	return result
}

// extractCrossLangAPILock is the Builder retry lock for any common language
// (F103). Go uses AST signatures; Python/JS/TS/Rust/Java use public-def regexes.
func extractCrossLangAPILock(projectRoot string) string {
	goLock := extractAPILock(projectRoot)
	other := extractNonGoPublicAPILock(projectRoot)
	if goLock == "" {
		return other
	}
	if other == "" {
		return goLock
	}
	return goLock + "\n" + other
}

var nonGoPublicDefRe = regexp.MustCompile(`(?m)^(?:` +
	`def\s+([A-Za-z_][\w]*)\s*\(([^)]*)\)` + `|` +
	`(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][\w]*)\s*\(([^)]*)\)` + `|` +
	`export\s+(?:const|class|type)\s+([A-Za-z_][\w]*)` + `|` +
	`pub\s+(?:async\s+)?(?:fn|struct|enum|trait)\s+([A-Za-z_][\w]*)` + `|` +
	`public\s+(?:static\s+)?(?:class|interface|void|int|[\w.]+)\s+([A-Za-z_][\w]*)` +
	`)`)

func extractNonGoPublicAPILock(projectRoot string) string {
	var b strings.Builder
	extOK := map[string]bool{
		".py": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
		".rs": true, ".java": true,
	}
	count := 0
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, ".avatars") || strings.Contains(path, "node_modules") ||
			strings.Contains(path, "vendor") || strings.Contains(path, "target") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !extOK[ext] {
			return nil
		}
		base := strings.ToLower(filepath.Base(path))
		if strings.Contains(base, "test") || strings.Contains(base, "spec") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil || len(raw) == 0 {
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		if rel == "" {
			rel = path
		}
		for _, m := range nonGoPublicDefRe.FindAllStringSubmatch(string(raw), 40) {
			sig := strings.TrimSpace(m[0])
			if sig == "" {
				continue
			}
			if count == 0 {
				b.WriteString("=== EXISTING PUBLIC API (non-Go — do not duplicate incompatible names) ===\n")
			}
			b.WriteString(fmt.Sprintf("%s  // %s\n", sig, rel))
			count++
			if count >= 48 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return b.String()
}

// captureCompileErrors runs `go build ./...` in projectRoot and returns
// the error output as a text block. This is injected into Builder retry
// prompts so Builder can fix the EXACT compilation errors rather than
// guessing what's wrong.
// #2: Gives Builder precise compilation error context.
// G11: only when projectRoot itself has go.mod — never inherit a parent module.
func captureCompileErrors(projectRoot string) string {
	if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*1000*1000*1000) // 30s
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = projectRoot
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return "" // compilation passes, no errors to report
	}
	output := strings.TrimSpace(stderr.String())
	if output == "" {
		return fmt.Sprintf("=== COMPILATION ERRORS (fix these EXACT errors) ===\ngo build failed: %v\n", err)
	}
	// Truncate very long error output.
	if len(output) > 3000 {
		output = output[:3000] + "\n[...more errors truncated...]"
	}
	return fmt.Sprintf("=== COMPILATION ERRORS (fix these EXACT errors) ===\n%s\n", output)
}

// collectGoFilePaths walks projectRoot and returns all .go file paths,
// excluding .avatars, vendor, and test files.
func collectGoFilePaths(projectRoot string) []string {
	var paths []string
	filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, ".avatars") || strings.Contains(path, "vendor") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	return paths
}

// filterGapItemsByFileExistence filters gap items to only include those that
// either (a) don't reference a specific file path, or (b) reference a file
// that actually exists on disk. This prevents hallucinated file paths from
// misleading the Builder. v4-P1-2.
func filterGapItemsByFileExistence(items []string, projectRoot string) []string {
	var valid []string
	for _, item := range items {
		// Extract file path from "missing X referenced in Y:Z" or similar patterns.
		path := extractFilePathFromGapItem(item)
		if path == "" {
			// No file path in this item — keep it (general instruction).
			valid = append(valid, item)
			continue
		}
		// Check if the file exists on disk.
		fullPath := path
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(projectRoot, path)
		}
		if _, err := os.Stat(fullPath); err == nil {
			valid = append(valid, item)
		}
		// If file doesn't exist, skip this item (hallucinated path).
	}
	return valid
}

// extractFilePathFromGapItem tries to extract a file path from a gap item
// string. Handles patterns like "missing X referenced in path/to/file.go:N".
func extractFilePathFromGapItem(item string) string {
	// Pattern: "missing X referenced in Y" or "missing X referenced in Y:N"
	idx := strings.Index(item, "referenced in ")
	if idx < 0 {
		// Try other patterns: "in path/file.go" or just "path/file.go"
		return extractGoFilePath(item)
	}
	rest := item[idx+len("referenced in "):]
	// Strip line number suffix.
	if colonIdx := strings.LastIndex(rest, ":"); colonIdx > 0 {
		// Check if the part after : is a number (line number).
		afterColon := rest[colonIdx+1:]
		if _, err := strconv.Atoi(strings.TrimSpace(afterColon)); err == nil {
			rest = rest[:colonIdx]
		}
	}
	rest = strings.TrimSpace(rest)
	if strings.HasSuffix(rest, ".go") {
		return rest
	}
	return ""
}

// extractGoFilePath finds a .go file path anywhere in a string.
func extractGoFilePath(s string) string {
	words := strings.Fields(s)
	for _, w := range words {
		w = strings.Trim(w, "\"'`,.;:()[]{}")
		if strings.HasSuffix(w, ".go") && strings.Contains(w, "/") {
			return w
		}
	}
	return ""
}
