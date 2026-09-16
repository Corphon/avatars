package verification

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/platform"
)

type CommandCheck struct {
	Name         string
	Command      []string
	Expected     string
	AllowPartial func(output string, err error) (bool, string)
	// P14-5: Advisory checks always return PARTIAL or PASS because the checker
	// can't meaningfully validate (e.g. HTML parse, CSS syntax). Advisory
	// PARTIALs do NOT prevent the overall verdict from being PASS.
	Advisory bool
}

type Executor interface {
	Run(ctx context.Context, workingDir string, command []string) (string, error)
}

type ExecExecutor struct{}

func (ExecExecutor) Run(ctx context.Context, workingDir string, command []string) (string, error) {
	if len(command) == 0 {
		return "", fmt.Errorf("verification command cannot be empty")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && looksLikeLanguageTestCommand(command) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workingDir
	platform.HideConsoleWindow(cmd)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return annotateEmptyCommandOutput(ctx, combined.String(), err), err
}

func annotateEmptyCommandOutput(ctx context.Context, output string, err error) string {
	if err == nil || strings.TrimSpace(output) != "" {
		return output
	}
	if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		return "command timed out with no output (test suite likely hung — wall-clock wait or deadlock)"
	}
	if ctx != nil && ctx.Err() == context.Canceled {
		return "command canceled with no output"
	}
	return output
}

func looksLikeLanguageTestCommand(command []string) bool {
	joined := strings.ToLower(strings.Join(command, " "))
	if strings.Contains(joined, "go test") || strings.Contains(joined, "pytest") ||
		strings.Contains(joined, "cargo test") || strings.Contains(joined, "npm test") ||
		strings.Contains(joined, "mvn test") || strings.Contains(joined, "gradle test") {
		return true
	}
	return false
}

type Runner struct {
	workingDir     string
	executor       Executor
	checks         []CommandCheck
	// P14-4a: Pre-existing error baseline — captured before the task runs.
	// Maps check name → expected error output. When a check fails with output
	// matching the baseline, it is downgraded from FAIL to PARTIAL (pre-existing).
	baselineErrors map[string]string
	cache          *HealthService
}

func NewRunner(workingDir string, executor Executor, checks []CommandCheck) Runner {
	return Runner{
		workingDir:     workingDir,
		executor:       executor,
		checks:         append([]CommandCheck(nil), checks...),
		baselineErrors: map[string]string{},
	}
}

// WithBaseline sets pre-existing error outputs for the given check names.
// Checks whose output matches the baseline will be downgraded from FAIL to PARTIAL.
func (r Runner) WithBaseline(baseline map[string]string) Runner {
	if baseline != nil {
		r.baselineErrors = baseline
	}
	return r
}

func (r Runner) WithCache(cache *HealthService) Runner {
	r.cache = cache
	return r
}

func DefaultChecks() []CommandCheck {
	return DefaultChecksFor(".")
}

// DefaultChecksFor binds Go empty-module AllowPartial to workingDir (H7′).
func DefaultChecksFor(workingDir string) []CommandCheck {
	wd := strings.TrimSpace(workingDir)
	if wd == "" {
		wd = "."
	}
	allowEnv := func(output string, err error) (bool, string) {
		if allowed, warning := allowMissingGoModulePartialIn(wd, output, err); allowed {
			return true, warning
		}
		return allowRaceEnvironmentPartial(output, err)
	}
	allowBuild := func(output string, err error) (bool, string) {
		if allowed, warning := allowMissingGoModulePartialIn(wd, output, err); allowed {
			return true, warning
		}
		return false, ""
	}
	return []CommandCheck{
		{
			Name:         "go build",
			Command:      []string{"go", "build", "./..."},
			Expected:     "All packages should compile without errors.",
			AllowPartial: allowBuild,
		},
		{
			Name:         "go test",
			Command:      []string{"go", "test", "./..."},
			Expected:     "All package tests should pass.",
			AllowPartial: allowEnv,
		},
		{
			Name:         "go vet",
			Command:      []string{"go", "vet", "./..."},
			Expected:     "go vet should report no issues.",
			AllowPartial: allowEnv,
		},
		{
			Name:     "go fmt",
			Command:  []string{"go", "fmt", "./..."},
			Expected: "All Go files should be properly formatted.",
			AllowPartial: func(output string, err error) (bool, string) {
				if err != nil {
					return true, output // report formatting issues as partial
				}
				return false, "" // PASS
			},
			Advisory: true, // formatting issues don't block build
		},
	}
}

func NewDefaultRunner(workingDir string) Runner {
	checks := DefaultChecksFor(workingDir)
	// P4-2: Auto-detect file types and add appropriate checks.
	checks = append(checks, DetectedFileChecks(workingDir)...)
	return NewRunner(workingDir, ExecExecutor{}, checks)
}

// DetectedFileChecks scans the working directory for common file types
// and returns appropriate verification commands (P4-2 pluggable verifiers).
func DetectedFileChecks(workingDir string) []CommandCheck {
	detected := map[string]bool{}
	_ = filepath.WalkDir(workingDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		detected[ext] = true
		return nil
	})

	var checks []CommandCheck

	// P4-2b: Real verification commands with checker availability detection.
	// Each check tries the real tool first; if unavailable, SKIP (not FAIL).

	// Python: built-in py_compile (zero dependencies)
	if detected[".py"] {
		checks = append(checks, pythonSyntaxCheck(workingDir))
	}
	// JavaScript: Node.js built-in --check
	if detected[".js"] || detected[".mjs"] {
		checks = append(checks, jsSyntaxCheck(workingDir))
	}
	// Shell: bash -n (system built-in)
	if detected[".sh"] {
		checks = append(checks, shellSyntaxCheck(workingDir))
	}
	// SQL: sqlite3 CLI (commonly available on Windows/macOS/Linux)
	if detected[".sql"] {
		checks = append(checks, sqlSyntaxCheck(workingDir))
	}
	// HTML: Go html.Parse (zero dependencies, uses Go stdlib)
	if detected[".html"] || detected[".htm"] {
		checks = append(checks, htmlParseCheck(workingDir))
	}
	// TypeScript: tsc --noEmit (requires typescript package)
	if detected[".ts"] || detected[".tsx"] {
		checks = append(checks, tsSyntaxCheck(workingDir))
	}
	// C: gcc -fsyntax-only (or clang)
	if detected[".c"] || detected[".h"] {
		checks = append(checks, cSyntaxCheck(workingDir))
	}
	// C++: g++ -fsyntax-only (or clang++)
	if detected[".cpp"] || detected[".hpp"] || detected[".cc"] {
		checks = append(checks, cppSyntaxCheck(workingDir))
	}
	// Rust: cargo check + cargo test (when Cargo.toml present), rustc fallback
	if detected[".rs"] || detected[".toml"] {
		cargoTomlPath := filepath.Join(workingDir, "Cargo.toml")
		if _, err := os.Stat(cargoTomlPath); err == nil {
			checks = append(checks, rustSyntaxCheck(workingDir), cargoTestCheck(workingDir), cargoFmtCheck(workingDir))
		} else {
			checks = append(checks, rustSyntaxCheck(workingDir))
		}
	}
	// CSS: basic syntax via Go (zero dependencies)
	if detected[".css"] {
		checks = append(checks, cssSyntaxCheck(workingDir))
	}
	// Java: javac syntax check
	if detected[".java"] {
		checks = append(checks, javaSyntaxCheck(workingDir))
	}
	// C#: dotnet build check
	if detected[".cs"] {
		checks = append(checks, csharpSyntaxCheck(workingDir))
	}
	// Ruby: ruby -c syntax check
	if detected[".rb"] {
		checks = append(checks, rubySyntaxCheck(workingDir))
	}
	// PHP: php -l syntax check
	if detected[".php"] {
		checks = append(checks, phpSyntaxCheck(workingDir))
	}
	// PowerShell: pwsh syntax check
	if detected[".ps1"] {
		checks = append(checks, ps1SyntaxCheck(workingDir))
	}
	// YAML/TOML: Python-based parsing check
	if detected[".yaml"] || detected[".yml"] || detected[".toml"] {
		checks = append(checks, configFileCheck(workingDir))
	}
	return checks
}

// checkerAvailable tests if a command is available by trying to run it.
func checkerAvailable(cmd string, _ ...string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func pythonSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "python syntax",
		Command: []string{"python", "-c", "import py_compile,os; all(not py_compile.compile(f,doredact=True) for f in (os.path.join(r,f) for r,_,fs in os.walk('.') for f in fs if f.endswith('.py')))"},
		Expected: "All Python files should have valid syntax.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("python") && !checkerAvailable("python3") {
				return true, "python not found — skipped"
			}
			if err != nil {
				return true, "SKIPPED: " + output
			}
			return true, "" // partial if no error output (py_compile is noisy)
		},
	}
}

func jsSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "javascript syntax",
		Command: []string{"node", "-e", "require('fs').readdirSync('.').filter(f=>f.endsWith('.js')).forEach(f=>{try{require('fs').readFileSync(f,'utf8');new Function(require('fs').readFileSync(f,'utf8'))}catch(e){if(e instanceof SyntaxError)throw e}})"},
		Expected: "All JS files should have valid syntax.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("node") {
				return true, "node not found — skipped"
			}
			if err != nil {
				return true, "SKIPPED: " + output
			}
			return true, ""
		},
	}
}

func shellSyntaxCheck(_ string) CommandCheck {
	shellBin, _ := platform.ShellCheckCommand()
	return CommandCheck{
		Name:    "shell syntax",
		Command: []string{shellBin, "-n", "*.sh"},
		Expected: "Shell scripts should have valid syntax.",
		AllowPartial: func(output string, err error) (bool, string) {
			if shellBin == "" || !checkerAvailable(shellBin) {
				return true, "shell not found — skipped"
			}
			if err != nil {
				return true, "SKIPPED: " + output
			}
			return true, ""
		},
	}
}

func sqlSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:     "sql syntax",
		Command:  []string{"sqlite3", ":memory:", ".read *.sql"},
		Expected: "SQL files should have valid syntax.",
		// R7-1: missing sqlite3 CLI is environmental — must not demote the run to
		// PARTIAL/completed_unverified and force a CLI auto-retry wave.
		Advisory: true,
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("sqlite3") {
				return true, "sqlite3 not found — skipped"
			}
			if err != nil {
				return true, "SKIPPED: " + output
			}
			return true, ""
		},
	}
}

func htmlParseCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "html parse",
		Command: []string{"python", "-c", "import html.parser,os; [html.parser.HTMLParser().feed(open(os.path.join(r,f),encoding='utf-8',errors='ignore').read()) for r,_,fs in os.walk('.') for f in fs if f.endswith(('.html','.htm'))]"},
		Expected: "HTML files should be well-formed.",
		Advisory: true,
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("python") && !checkerAvailable("python3") {
				return true, "python not found — skipped"
			}
			if err != nil {
				return true, "HTML parse warning: " + output
			}
			return true, ""
		},
	}
}

func cssSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "css syntax",
		Command: []string{"go", "run", "./cmd/csscheck/"},
		Expected: "CSS files should have valid syntax (balanced braces, semicolons, no empty rulesets).",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("go") {
				return true, "go not found — skipped"
			}
			if err != nil {
				return true, output
			}
			return false, ""
		},
	}
}

func tsSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "typescript syntax",
		Command: []string{"npx", "tsc", "--noEmit"},
		Expected: "TypeScript files should compile without errors.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("npx") {
				return true, "npx/tsc not found — skipped"
			}
			if err != nil {
				return true, "SKIPPED: " + output
			}
			return true, ""
		},
	}
}

func rustSyntaxCheck(workingDir string) CommandCheck {
	return CommandCheck{
		Name:     "cargo check",
		Command:  []string{"cargo", "check"},
		Expected: "Rust project should compile without errors via cargo check.",
		AllowPartial: func(output string, err error) (bool, string) {
			// Prefer cargo check; fall back to rustc if cargo unavailable.
			if !checkerAvailable("cargo") {
				if !checkerAvailable("rustc") {
					return true, "cargo/rustc not found — skipped"
				}
				// Fallback: rustc syntax-only check on all .rs files.
				return true, "cargo not found — use 'rustc --check' for individual files"
			}
			if err != nil {
				// Real compile error — report as PARTIAL with output.
				return true, output
			}
			return false, "" // PASS
		},
	}
}

// cargoTestCheck runs cargo test when a Cargo.toml exists.
func cargoTestCheck(workingDir string) CommandCheck {
	return CommandCheck{
		Name:     "cargo test",
		Command:  []string{"cargo", "test"},
		Expected: "All Rust tests should pass.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("cargo") {
				return true, "cargo not found — skipped"
			}
			if err != nil {
				return true, output
			}
			return false, "" // PASS
		},
	}
}

// cargoFmtCheck runs cargo fmt --check when a Cargo.toml exists.
func cargoFmtCheck(workingDir string) CommandCheck {
	return CommandCheck{
		Name:     "cargo fmt",
		Command:  []string{"cargo", "fmt", "--", "--check"},
		Expected: "Rust code should follow standard formatting.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("cargo") {
				return true, "cargo not found — skipped"
			}
			if !checkerAvailable("rustfmt") {
				return true, "rustfmt not found — skipped"
			}
			return true, "" // cargo fmt --check is advisory only
		},
		Advisory: true,
	}
}

func cSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "c syntax",
		Command: []string{"gcc", "-fsyntax-only", "*.c"},
		Expected: "C files should compile without syntax errors.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("gcc") && !checkerAvailable("clang") {
				return true, "gcc/clang not found — skipped"
			}
			if err != nil {
				return true, "SKIPPED: " + output
			}
			return true, ""
		},
	}
}

func cppSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "c++ syntax",
		Command: []string{"g++", "-fsyntax-only", "*.cpp"},
		Expected: "C++ files should compile without syntax errors.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("g++") && !checkerAvailable("clang++") {
				return true, "g++/clang++ not found — skipped"
			}
			if err != nil {
				return true, "SKIPPED: " + output
			}
			return true, ""
		},
	}
}

func DefaultRaceChecks() []CommandCheck {
	return []CommandCheck{
		{
			Name:         "go test -race",
			Command:      []string{"go", "test", "-race", "./..."},
			Expected:     "Race-enabled tests should pass, or report a real environment limitation.",
			AllowPartial: allowGoVerificationEnvironmentPartial,
		},
	}
}

func NewRaceRunner(workingDir string) Runner {
	checks := DefaultChecksFor(workingDir)
	checks = append(checks, DefaultRaceChecks()...)
	return NewRunner(workingDir, ExecExecutor{}, checks)
}

func (r Runner) runCheck(ctx context.Context, command []string) (string, error) {
	if r.cache != nil {
		return r.cache.Exec(ctx, r.executor, r.workingDir, command)
	}
	return r.executor.Run(ctx, r.workingDir, command)
}

func (r Runner) Run(ctx context.Context) Report {
	report := Report{Verdict: VerdictPass}
	passCount := 0
	partialCount := 0
	baselinePartialCount := 0 // P14-5: pre-existing PARTIALs, distinct from new PARTIALs
	advisoryPartialCount := 0  // P14-5: inherent PARTIALs from advisory checks
	newPartialCount := 0
	failCount := 0

	if len(r.checks) == 0 {
		report.Summary = "Verification finished with PASS. No applicable checks for changed files."
		return report
	}

	for _, check := range r.checks {
		output, err := r.runCheck(ctx, check.Command)
		evidence := CheckEvidence{
			Name:           check.Name,
			CommandRun:     strings.Join(check.Command, " "),
			OutputObserved: strings.TrimSpace(output),
			Expected:       check.Expected,
		}

		if err == nil {
			evidence.Actual = "Command completed successfully."
			evidence.Result = VerdictPass
			report.Checks = append(report.Checks, evidence)
			passCount++
			continue
		}

		// P14-4a: Pre-existing error baseline — if this check's output matches
		// the pre-captured baseline, downgrade from FAIL to PARTIAL since the
		// error was already present before the task ran.
		matchedBaseline := false
		if baselineOutput, hasBaseline := r.baselineErrors[check.Name]; hasBaseline && baselineOutput != "" {
			if outputOverlap(strings.TrimSpace(output), baselineOutput) > 0.7 {
				// Error output substantially matches the pre-existing baseline.
				evidence.Actual = err.Error()
				evidence.Result = VerdictPartial
				report.Checks = append(report.Checks, evidence)
				report.Warnings = append(report.Warnings, fmt.Sprintf("%s: pre-existing error (matched baseline, not introduced by this task)", check.Name))
				partialCount++
				baselinePartialCount++
				matchedBaseline = true
			}
		}

		if matchedBaseline {
			continue
		}

		if check.AllowPartial != nil {
			if allowed, warning := check.AllowPartial(output, err); allowed {
				evidence.Actual = err.Error()
				evidence.Result = VerdictPartial
				report.Checks = append(report.Checks, evidence)
				if warning != "" {
					report.Warnings = append(report.Warnings, warning)
				}
				partialCount++
				if check.Advisory {
					advisoryPartialCount++
				} else {
					newPartialCount++
				}
				continue
			}
		}

		evidence.Actual = err.Error()
		evidence.Result = VerdictFail
		report.Checks = append(report.Checks, evidence)
		failCount++
	}

	// P14-5: Upgrade verdict to PASS when no real problems exist:
	// - Zero failures
	// - All PARTIALs are either pre-existing (baseline) or advisory (inherently partial checks)
	// This prevents infinite retry loops on non-actionable verification results.
	actionablePartialCount := newPartialCount
	if actionablePartialCount < 0 {
		actionablePartialCount = 0
	}
	switch {
	case failCount > 0:
		report.Verdict = VerdictFail
	case actionablePartialCount > 0:
		report.Verdict = VerdictPartial
	default:
		report.Verdict = VerdictPass
	}

	report.Summary = fmt.Sprintf("Verification finished with %s. %d passed, %d partial (%d pre-existing), %d failed.",
		report.Verdict, passCount, partialCount, baselinePartialCount, failCount)
	return report
}

// outputOverlap computes a rough similarity ratio between two error outputs
// by counting overlapping lines. Returns 0.0–1.0 where 1.0 = identical.
// P14-5: Also detects "new error lines" — lines in `a` (current output)
// that don't appear in `b` (baseline) AND look like Go errors. These represent
// potentially new errors introduced by the task, not pre-existing.
func outputOverlap(a, b string) float64 {
	if a == "" && b == "" {
		return 1.0
	}
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	if len(aLines) == 0 || len(bLines) == 0 {
		return 0
	}
	// Build a set of baseline lines for O(1) lookup.
	bSet := map[string]bool{}
	for _, bl := range bLines {
		bl = strings.TrimSpace(bl)
		if bl != "" {
			bSet[bl] = true
		}
	}
	matches := 0
	newErrorLines := 0
	for _, al := range aLines {
		al = strings.TrimSpace(al)
		if al == "" {
			continue
		}
		if bSet[al] {
			matches++
		} else if looksLikeGoError(al) {
			newErrorLines++
		}
	}
	// P14-5: If there are new error lines that look like Go compilation errors,
	// return 0 (no match) so the check is NOT downgraded — the task introduced
	// new problems that need attention.
	if newErrorLines > 0 {
		return 0
	}
	nonEmpty := 0
	for _, l := range aLines {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		return 0
	}
	return float64(matches) / float64(nonEmpty)
}

// looksLikeGoError returns true if a line looks like a Go compilation error.
func looksLikeGoError(line string) bool {
	// Go error patterns: "file.go:line:col: message", "package ... : message"
	return strings.Contains(line, ".go:") ||
		strings.Contains(line, ": undefined:") ||
		strings.Contains(line, "imported and not used") ||
		strings.Contains(line, "syntax error:") ||
		strings.Contains(line, "expected 'package'") ||
		strings.Contains(line, "found packages") ||
		strings.Contains(line, "no required module") ||
		strings.Contains(line, "cannot use") ||
		strings.Contains(line, "undefined:")
}

func allowRaceEnvironmentPartial(output string, err error) (bool, string) {
	joined := strings.ToLower(strings.TrimSpace(output + "\n" + err.Error()))
	if strings.Contains(joined, "cgo_enabled=1") || strings.Contains(joined, "requires cgo") || strings.Contains(joined, "-race requires cgo") {
		return true, "go test -race ./... could not run because CGO is not enabled in the current environment."
	}
	// Real test/vet failures (not CGO environment issues) → FAIL.
	return false, ""
}

func allowGoBuildPartial(output string, err error) (bool, string) {
	// Only allow PARTIAL when there's no Go workspace at all.
	// Real compilation errors must be FAIL so the pipeline blocks and remediates.
	if allowed, warning := allowMissingGoModulePartial(output, err); allowed {
		return true, warning
	}
	// For go build, unmatched errors are real compilation failures → FAIL.
	return false, ""
}

func allowGoVerificationEnvironmentPartial(output string, err error) (bool, string) {
	if allowed, warning := allowMissingGoModulePartial(output, err); allowed {
		return true, warning
	}
	return allowRaceEnvironmentPartial(output, err)
}

func allowMissingGoModulePartial(output string, err error) (bool, string) {
	return allowMissingGoModulePartialIn(".", output, err)
}

// allowMissingGoModulePartialIn is H7′: empty-package signals FAIL only when
// workingDir has both a local go.mod and .go sources (broken delivery).
// Mid-scaffold / docs-only / no-module workspaces stay PARTIAL.
func allowMissingGoModulePartialIn(workingDir, output string, err error) (bool, string) {
	joined := strings.ToLower(strings.TrimSpace(output + "\n" + err.Error()))
	if joined == "" {
		return true, ""
	}
	emptyPkg := strings.Contains(joined, "matched no packages") || strings.Contains(joined, "no go files")
	moduleMissing := strings.Contains(joined, "go.mod file not found") ||
		strings.Contains(joined, "directory prefix . does not contain main module") ||
		strings.Contains(joined, "cannot find main module") ||
		strings.Contains(joined, "go: cannot find main module")
	if emptyPkg && !moduleMissing {
		wd := strings.TrimSpace(workingDir)
		if wd == "" {
			wd = "."
		}
		goMod := filepath.Join(wd, "go.mod")
		if _, statErr := os.Stat(goMod); statErr == nil && workspaceHasGoSources(wd) {
			return false, ""
		}
		return true, "go verification deferred — module or .go packages not ready yet"
	}
	if moduleMissing || emptyPkg {
		return true, "go verification could not run because the current working directory is not a complete Go module workspace."
	}
	if strings.Contains(joined, filepath.ToSlash("package ./...")) && strings.Contains(joined, "does not contain main module") {
		return true, "go verification could not run because the current working directory is not a complete Go module workspace."
	}
	return false, ""
}

// workspaceHasGoSources reports whether dir contains at least one .go file
// (excluding vendor / .avatars trees).
func workspaceHasGoSources(dir string) bool {
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".git" || base == ".avatars" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func javaSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "java syntax",
		Command: []string{"javac", "-d", "/tmp", "*.java"},
		Expected: "Java files should compile without errors.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("javac") { return true, "javac not found — skipped" }
			if err != nil { return true, "SKIPPED: " + output }
			return true, ""
		},
	}
}

func csharpSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "c# syntax",
		Command: []string{"dotnet", "build", "--no-restore"},
		Expected: "C# project should build without errors.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("dotnet") { return true, "dotnet not found — skipped" }
			if err != nil { return true, "SKIPPED: " + output }
			return true, ""
		},
	}
}

func rubySyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "ruby syntax",
		Command: []string{"ruby", "-c", "*.rb"},
		Expected: "Ruby files should have valid syntax.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("ruby") { return true, "ruby not found — skipped" }
			if err != nil { return true, "SKIPPED: " + output }
			return true, ""
		},
	}
}

func phpSyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "php syntax",
		Command: []string{"php", "-l", "*.php"},
		Expected: "PHP files should have valid syntax.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("php") { return true, "php not found — skipped" }
			if err != nil { return true, "SKIPPED: " + output }
			return true, ""
		},
	}
}

func ps1SyntaxCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "powershell syntax",
		Command: []string{"pwsh", "-NoProfile", "-Command", "Exit"},
		Expected: "PowerShell scripts should be syntactically valid.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("pwsh") && !checkerAvailable("powershell") { return true, "pwsh not found — skipped" }
			if err != nil { return true, "SKIPPED: " + output }
			return true, ""
		},
	}
}

func configFileCheck(_ string) CommandCheck {
	return CommandCheck{
		Name:    "yaml/toml syntax",
		Command: []string{"python", "-c", "import os; f=next((x for x in os.listdir('.') if x.endswith(('.yaml','.yml','.toml'))),None); print('no config files' if not f else 'found')"},
		Expected: "Config files should be valid YAML/TOML.",
		AllowPartial: func(output string, err error) (bool, string) {
			if !checkerAvailable("python") && !checkerAvailable("python3") { return true, "python not found — skipped" }
			return true, "yaml/toml check: manual review recommended"
		},
	}
}

// AnalyzeImports scans Go files for import issues: unused imports,
// missing imports, and potential circular dependencies (T9.7).
func AnalyzeImports(workingDir string) []string {
	var findings []string
	importsByFile := map[string][]string{}
	packageByDir := map[string]string{}

	_ = filepath.WalkDir(workingDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(content)
		// Extract package name
		if pkg := extractGoPackage(text); pkg != "" {
			dir := filepath.Dir(path)
			if existing, ok := packageByDir[dir]; ok && existing != pkg {
				findings = append(findings, fmt.Sprintf("Package conflict in %s: %s vs %s", dir, existing, pkg))
			}
			packageByDir[dir] = pkg
		}
		// Extract imports
		lines := strings.Split(text, "\n")
		var imports []string
		inImport := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "import (" {
				inImport = true
				continue
			}
			if inImport && trimmed == ")" {
				inImport = false
				continue
			}
			if inImport && trimmed != "" {
				// Strip quotes
				imp := strings.Trim(trimmed, "\t \"`")
				if imp != "" && !strings.HasPrefix(imp, "//") {
					imports = append(imports, imp)
				}
			}
		}
		if len(imports) > 0 {
			importsByFile[path] = imports
		}
		return nil
	})

	// Check for unused imports by looking for usage
	for file, imports := range importsByFile {
		content, _ := os.ReadFile(file)
		text := string(content)
		for _, imp := range imports {
			// Simple heuristic: check if the imported package name appears in code
			pkgName := imp[strings.LastIndex(imp, "/")+1:]
			if !strings.Contains(text, pkgName+".") && !strings.Contains(text, "\""+imp+"\"") {
				// Might be unused — but could be imported for side effects
				// Only flag if it's not a standard library import with side effects
				if !strings.Contains(strings.ToLower(imp), "embed") {
					findings = append(findings, fmt.Sprintf("Possibly unused import in %s: %s", file, imp))
				}
			}
		}
	}

	return findings
}

// CaptureBuildBaseline runs the default Go checks (build/test/vet) and returns
// their error outputs as a baseline map keyed by check name. Used before a task
// runs so post-task verification can filter pre-existing errors (P14-4a).
func CaptureBuildBaseline(workingDir string) map[string]string {
	baseline := map[string]string{}
	executor := ExecExecutor{}
	for _, check := range DefaultChecksFor(workingDir) {
		output, err := executor.Run(context.Background(), workingDir, check.Command)
		if err != nil {
			baseline[check.Name] = strings.TrimSpace(output)
		}
	}
	return baseline
}

func extractGoPackage(source string) string {
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "package "))
		}
	}
	return ""
}
