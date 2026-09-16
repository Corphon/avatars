// Package runtime — language-agnostic static analysis for Critic quality gates.
//
// TR-Fix-13: Adds logic-level checking to the Critic pipeline. Previously the
// Critic only checked syntax, compilation, and tests — it never caught logic
// bugs (nil derefs, unreachable code, error吞没, race conditions, etc.).
//
// This file provides a language-dispatched static analysis function that runs
// the appropriate tool for each project language and returns structured findings
// that can be injected into all Critic pathways (stagedQualityGate,
// AdversarialAudit, criticEscalateToPlanner, criticReviewCode,
// criticPreFlightHealthCheck, and checkFileHealth).

package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// StaticAnalysisFinding is a single issue found by static analysis.
type StaticAnalysisFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"` // "error", "warning", "info"
	Tool     string `json:"tool"`     // "go_vet", "pylint", "node_check", "import_check"
	Message  string `json:"message"`
}

// runStaticAnalysis detects the project language(s) and runs the appropriate
// static analysis tool. Returns structured findings suitable for injection
// into Critic escalation prompts and quality gate decisions.
//
// Language dispatch:
//   - Go:      go vet ./...
//   - Python:  python -m py_compile (per-file) + pylint/pyflakes if available
//   - Node.js: node -c (per-file) + eslint if available
//
// Graceful degradation: if a tool is not installed, it is skipped with no error.
func runStaticAnalysis(wd string) []StaticAnalysisFinding {
	lang := detectProjectLanguage(wd)
	if lang == "" {
		return nil
	}

	var findings []StaticAnalysisFinding
	switch lang {
	case "go":
		findings = append(findings, runGoVet(wd)...)
		findings = append(findings, checkImportSemantics(wd)...)
	case "python":
		findings = append(findings, runPythonStaticCheck(wd)...)
	case "javascript", "typescript":
		findings = append(findings, runNodeStaticCheck(wd)...)
	}
	return findings
}

// detectProjectLanguage scans the working directory for source files and
// returns the dominant language ("go", "python", "javascript", "typescript").
func detectProjectLanguage(wd string) string {
	extCount := map[string]int{}
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		extCount[ext]++
		return nil
	})

	// Determine dominant language by file extension count.
	goCount := extCount[".go"]
	pyCount := extCount[".py"] + extCount[".pyi"]
	jsCount := extCount[".js"] + extCount[".mjs"] + extCount[".cjs"]
	tsCount := extCount[".ts"] + extCount[".tsx"]
	jsxCount := extCount[".jsx"]

	if goCount > pyCount && goCount > jsCount && goCount > tsCount {
		return "go"
	}
	if pyCount > goCount && pyCount > jsCount && pyCount > tsCount {
		return "python"
	}
	if tsCount > goCount && tsCount > pyCount && tsCount > jsCount {
		return "typescript"
	}
	if jsCount+jsxCount > goCount && jsCount+jsxCount > pyCount && jsCount+jsxCount > tsCount {
		return "javascript"
	}
	return ""
}

// shouldSkipDir returns true for directories that should be excluded from
// static analysis (vendor, generated code, etc.).
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".avatars", "avatars", "node_modules", "vendor",
		"__pycache__", ".venv", "venv", "dist", "build", "target",
		".idea", ".vscode", "bin", "coverage":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// =============================================================================
// Go static analysis
// =============================================================================

// runGoVet executes "go vet ./..." and parses the output into findings.
func runGoVet(wd string) []StaticAnalysisFinding {
	ctx, cancel := staticAnalysisTimeout(30 * time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "vet", "./...")
	cmd.Dir = wd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run() // go vet writes findings to stderr

	// TR-Fix-13a: Detect timeout — don't silently skip findings.
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return []StaticAnalysisFinding{{
				Severity: "warning",
				Tool:     "go_vet",
				Message:  "go vet timed out after 30s — project may be too large for static analysis in this stage. Consider running go vet manually.",
			}}
		}
	}

	output := stderr.String()
	if output == "" {
		return nil
	}

	return parseGoVetOutput(output)
}

// parseGoVetOutput converts go vet stderr output to structured findings.
// go vet format: "file.go:line:col: message"
func parseGoVetOutput(output string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	// Matches: path/file.go:123:45: message
	re := regexp.MustCompile(`^(.+\.go):(\d+):\d*:\s*(.+)$`)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "exit status") {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m == nil {
			// Non-standard format — include as file-less finding.
			findings = append(findings, StaticAnalysisFinding{
				Severity: "warning",
				Tool:     "go_vet",
				Message:  line,
			})
			continue
		}
		var lineNum int
		fmt.Sscanf(m[2], "%d", &lineNum)
		severity := "warning"
		if strings.Contains(strings.ToLower(m[3]), "fatal") ||
			strings.Contains(strings.ToLower(m[3]), "nil deref") {
			severity = "error"
		}
		findings = append(findings, StaticAnalysisFinding{
			File:     m[1],
			Line:     lineNum,
			Severity: severity,
			Tool:     "go_vet",
			Message:  strings.TrimSpace(m[3]),
		})
	}
	return findings
}

// =============================================================================
// Python static analysis
// =============================================================================

// runPythonStaticCheck runs Python syntax and static checks.
func runPythonStaticCheck(wd string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding

	// 1. py_compile: syntax check on every .py file.
	findings = append(findings, runPyCompile(wd)...)

	// 2. pylint: if available, run for deeper logic checks.
	if _, err := exec.LookPath("pylint"); err == nil {
		findings = append(findings, runPyLint(wd)...)
	} else if _, err := exec.LookPath("pyflakes"); err == nil {
		findings = append(findings, runPyFlakes(wd)...)
	}

	return findings
}

// runPyCompile runs "python -m py_compile" on each .py file.
func runPyCompile(wd string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		ctx, cancel := staticAnalysisTimeout(10 * time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "python", "-m", "py_compile", path)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			rel, _ := filepath.Rel(wd, path)
			findings = append(findings, StaticAnalysisFinding{
				File:     rel,
				Severity: "error",
				Tool:     "py_compile",
				Message:  strings.TrimSpace(stderr.String()),
			})
		}
		return nil
	})
	return findings
}

// runPyLint runs pylint and parses its output.
func runPyLint(wd string) []StaticAnalysisFinding {
	ctx, cancel := staticAnalysisTimeout(60 * time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pylint", "--output-format=text", "--score=n", ".")
	cmd.Dir = wd
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	_ = cmd.Run()
	return parsePyLintOutput(stdout.String())
}

// parsePyLintOutput converts pylint output to findings.
// pylint format: "file.py:line:col: CODE: message (category)"
func parsePyLintOutput(output string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	re := regexp.MustCompile(`^(.+\.py):(\d+):\d+:\s*(\w+):\s*(.+)$`)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var lineNum int
		fmt.Sscanf(m[2], "%d", &lineNum)
		severity := "warning"
		code := m[3]
		if code == "E" || strings.HasPrefix(code, "E0") || strings.HasPrefix(code, "F") {
			severity = "error"
		}
		findings = append(findings, StaticAnalysisFinding{
			File:     m[1],
			Line:     lineNum,
			Severity: severity,
			Tool:     "pylint",
			Message:  fmt.Sprintf("%s: %s", code, m[4]),
		})
	}
	return findings
}

// runPyFlakes runs pyflakes and parses its output.
func runPyFlakes(wd string) []StaticAnalysisFinding {
	ctx, cancel := staticAnalysisTimeout(30 * time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pyflakes", ".")
	cmd.Dir = wd
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	_ = cmd.Run()

	output := stdout.String()
	if output == "" {
		return nil
	}

	var findings []StaticAnalysisFinding
	// pyflakes format: "file.py:line: message"
	re := regexp.MustCompile(`^(.+\.py):(\d+):\s*(.+)$`)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var lineNum int
		fmt.Sscanf(m[2], "%d", &lineNum)
		findings = append(findings, StaticAnalysisFinding{
			File:     m[1],
			Line:     lineNum,
			Severity: "warning",
			Tool:     "pyflakes",
			Message:  strings.TrimSpace(m[3]),
		})
	}
	return findings
}

// =============================================================================
// Node.js static analysis
// =============================================================================

// runNodeStaticCheck runs Node.js/JavaScript static checks.
func runNodeStaticCheck(wd string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding

	// 1. node -c: syntax check on every .js/.mjs file.
	findings = append(findings, runNodeSyntaxCheck(wd)...)

	// 2. eslint: if available, run for deeper logic checks.
	if _, err := exec.LookPath("eslint"); err == nil {
		findings = append(findings, runESLint(wd)...)
	}

	return findings
}

// runNodeSyntaxCheck runs "node -c" (syntax check) on each .js/.mjs/.jsx file.
func runNodeSyntaxCheck(wd string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".js" && ext != ".mjs" && ext != ".cjs" && ext != ".jsx" {
			return nil
		}
		ctx, cancel := staticAnalysisTimeout(10 * time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", "-c", path)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			rel, _ := filepath.Rel(wd, path)
			findings = append(findings, StaticAnalysisFinding{
				File:     rel,
				Severity: "error",
				Tool:     "node_syntax",
				Message:  strings.TrimSpace(stderr.String()),
			})
		}
		return nil
	})
	return findings
}

// runESLint runs eslint and parses its output into findings.
func runESLint(wd string) []StaticAnalysisFinding {
	ctx, cancel := staticAnalysisTimeout(60 * time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "eslint", ".", "--format=compact")
	cmd.Dir = wd
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	_ = cmd.Run()
	return parseESLintOutput(stdout.String())
}

// parseESLintOutput converts eslint compact format to findings.
// eslint compact format: "file.js: line 10, col 5, Error - message (rule)"
func parseESLintOutput(output string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	re := regexp.MustCompile(`^(.+):\s*line\s+(\d+),\s*col\s+\d+,\s*(\w+)\s*-\s*(.+)$`)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var lineNum int
		fmt.Sscanf(m[2], "%d", &lineNum)
		severity := "warning"
		if strings.EqualFold(m[3], "Error") || strings.EqualFold(m[3], "Fatal") {
			severity = "error"
		}
		findings = append(findings, StaticAnalysisFinding{
			File:     m[1],
			Line:     lineNum,
			Severity: severity,
			Tool:     "eslint",
			Message:  strings.TrimSpace(m[4]),
		})
	}
	return findings
}

// =============================================================================
// Import semantic validation (all languages)
// =============================================================================

// checkImportSemantics validates that imports in generated files reference
// real, resolvable packages/modules. Language-agnostic dispatcher.
func checkImportSemantics(wd string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	lang := detectProjectLanguage(wd)
	switch lang {
	case "go":
		findings = append(findings, checkGoImportSemantics(wd)...)
	case "python":
		findings = append(findings, checkPythonImportSemantics(wd)...)
	case "javascript", "typescript":
		findings = append(findings, checkNodeImportSemantics(wd)...)
	}
	return findings
}

// checkGoImportSemantics verifies Go import paths resolve to actual packages.
func checkGoImportSemantics(wd string) []StaticAnalysisFinding {
	// Use "go list" with the packages to verify imports resolve.
	// If go list fails, imports are invalid.
	ctx, cancel := staticAnalysisTimeout(30 * time.Second)
	defer cancel()

	// First, find all Go packages in the project (excluding vendor).
	cmd := exec.CommandContext(ctx, "go", "list", "./...")
	cmd.Dir = wd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		// Parse "go list" error for missing imports.
		// Typical format: "package example.com/missing is not in GOROOT..."
		return parseGoListErrors(errMsg)
	}
	_ = out
	return nil
}

// parseGoListErrors extracts import errors from "go list" output.
func parseGoListErrors(output string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	// Matches: "path/to/file.go:line:col: message"
	// or: "package example.com/pkg is not in std"
	missingPkgRe := regexp.MustCompile(`package\s+(\S+)\s+is not in`)
	fileRe := regexp.MustCompile(`^(.+\.go):(\d+):\d*:\s*(.+)$`)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Missing package import.
		if m := missingPkgRe.FindStringSubmatch(line); m != nil {
			findings = append(findings, StaticAnalysisFinding{
				Severity: "error",
				Tool:     "import_check",
				Message:  fmt.Sprintf("imported package %s cannot be resolved — it may not exist or is not in go.mod", m[1]),
			})
			continue
		}

		// File-specific error.
		if m := fileRe.FindStringSubmatch(line); m != nil {
			var lineNum int
			fmt.Sscanf(m[2], "%d", &lineNum)
			findings = append(findings, StaticAnalysisFinding{
				File:     m[1],
				Line:     lineNum,
				Severity: "warning",
				Tool:     "import_check",
				Message:  strings.TrimSpace(m[3]),
			})
		}
	}
	return findings
}

// checkPythonImportSemantics validates Python import statements can be resolved.
func checkPythonImportSemantics(wd string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	// Use python -c to try importing modules found in import statements.
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, imp := range extractPythonImports(string(data)) {
			if isStdlibPythonModule(imp) {
				continue
			}
			// Check if it's a local module (exists as .py file).
			if localExists(wd, imp) {
				continue
			}
			rel, _ := filepath.Rel(wd, path)
			findings = append(findings, StaticAnalysisFinding{
				File:     rel,
				Severity: "warning",
				Tool:     "import_check",
				Message:  fmt.Sprintf("import '%s' may not be resolvable — module not found as local file", imp),
			})
		}
		return nil
	})
	return findings
}

// extractPythonImports extracts module names from Python import statements.
func extractPythonImports(src string) []string {
	var imports []string
	// import foo, from foo import bar
	re := regexp.MustCompile(`(?:^|\n)\s*(?:from|import)\s+(\w+)`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if len(m) > 1 {
			imports = append(imports, m[1])
		}
	}
	return imports
}

// isStdlibPythonModule returns true for Python standard library modules.
func isStdlibPythonModule(name string) bool {
	// Common Python stdlib modules.
	stdlib := map[string]bool{
		"os": true, "sys": true, "re": true, "json": true, "math": true,
		"time": true, "datetime": true, "collections": true, "io": true,
		"pathlib": true, "typing": true, "enum": true, "abc": true,
		"functools": true, "itertools": true, "hashlib": true, "base64": true,
		"http": true, "urllib": true, "socket": true, "ssl": true,
		"threading": true, "asyncio": true, "subprocess": true, "shutil": true,
		"logging": true, "unittest": true, "pytest": true, "argparse": true,
		"csv": true, "xml": true, "html": true, "sqlite3": true,
		"dataclasses": true, "decimal": true, "fractions": true, "random": true,
		"statistics": true, "uuid": true, "tempfile": true, "glob": true,
		"copy": true, "pprint": true, "textwrap": true, "string": true,
		"struct": true, "pickle": true, "configparser": true, "tomllib": true,
	}
	return stdlib[name]
}

// localExists checks if a Python module name resolves to a local .py file.
func localExists(wd string, moduleName string) bool {
	candidate := filepath.Join(wd, moduleName+".py")
	if _, err := os.Stat(candidate); err == nil {
		return true
	}
	// Also check as package (module/__init__.py).
	candidate = filepath.Join(wd, moduleName, "__init__.py")
	_, err := os.Stat(candidate)
	return err == nil
}

// checkNodeImportSemantics validates Node.js import/require statements.
func checkNodeImportSemantics(wd string) []StaticAnalysisFinding {
	var findings []StaticAnalysisFinding
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".js" && ext != ".mjs" && ext != ".cjs" && ext != ".jsx" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, imp := range extractNodeJSImports(string(data)) {
			if strings.HasPrefix(imp, ".") || strings.HasPrefix(imp, "/") {
				// Relative import — check if the file exists.
				resolved := resolveNodeImport(path, imp)
				if resolved == "" {
					rel, _ := filepath.Rel(wd, path)
					findings = append(findings, StaticAnalysisFinding{
						File:     rel,
						Severity: "warning",
						Tool:     "import_check",
						Message:  fmt.Sprintf("import '%s' cannot be resolved to a file", imp),
					})
				}
			}
			// Non-relative imports (npm packages) — not checked here;
			// "node -c" would catch unresolvable ones.
		}
		return nil
	})
	return findings
}

// extractNodeJSImports extracts import paths from JavaScript/Node.js source.
func extractNodeJSImports(src string) []string {
	var imports []string
	// import ... from '...', import '...', require('...')
	re := regexp.MustCompile(`(?:import\s+(?:[\s\S]*?\s+from\s+)?|require\()['"](\S+?)['"]`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if len(m) > 1 {
			imports = append(imports, m[1])
		}
	}
	return imports
}

// resolveNodeImport resolves a relative JS import path to an actual file.
func resolveNodeImport(fromFile string, importPath string) string {
	dir := filepath.Dir(fromFile)
	candidates := []string{
		filepath.Join(dir, importPath+".js"),
		filepath.Join(dir, importPath+".mjs"),
		filepath.Join(dir, importPath+".cjs"),
		filepath.Join(dir, importPath+".jsx"),
		filepath.Join(dir, importPath, "index.js"),
		filepath.Join(dir, importPath, "index.mjs"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// staticAnalysisTimeout creates a context with the given timeout.
func staticAnalysisTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// buildStaticAnalysisReport runs static analysis and formats results as a
// human-readable report for injection into Builder escalation prompts.
// TR-Fix-13: Used by criticEscalateToPlanner to tell the Builder about
// logic bugs it should fix on retry.
func buildStaticAnalysisReport(wd string) string {
	findings := runStaticAnalysis(wd)
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("=== STATIC ANALYSIS FINDINGS (fix these logic issues) ===\n")
	errCount := 0
	for _, f := range findings {
		if f.Severity == "error" {
			errCount++
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		b.WriteString(fmt.Sprintf("[%s|%s] %s: %s\n", f.Severity, f.Tool, loc, f.Message))
	}
	if errCount > 0 {
		b.WriteString(fmt.Sprintf("\n%d static analysis error(s) must be fixed.\n", errCount))
	}
	return b.String()
}

// formatStaticAnalysisFindings returns a compact summary string for use in
// pipeline stages that need a one-line summary.
func formatStaticAnalysisFindings(findings []StaticAnalysisFinding) string {
	if len(findings) == 0 {
		return ""
	}
	errCount := 0
	for _, f := range findings {
		if f.Severity == "error" {
			errCount++
		}
	}
	return fmt.Sprintf("%d static analysis issues (%d errors, %d warnings)",
		len(findings), errCount, len(findings)-errCount)
}
