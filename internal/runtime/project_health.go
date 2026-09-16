package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// projectCompileCheck runs language-appropriate compile/check without tests.
func projectCompileCheck(wd string) string {
	// U1: install package-manager deps before compile (npm/go/pip) — same
	// class of gap as go mod tidy before go build; not Node-only.
	if errStr := ensureProjectDependencies(wd); errStr != "" {
		return errStr
	}
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
		if errStr := detectConflictingGoLayout(wd); errStr != "" {
			if clean, _ := healConflictingGoLayout(wd); clean {
				// F55: purged root shell / buried dup — continue to build.
			} else if errStr2 := detectConflictingGoLayout(wd); errStr2 != "" {
				return errStr2
			}
		}
		if errStr := quickGoBuildCheck(wd); errStr != "" {
			return fmt.Sprintf("go build failed: %s", errStr)
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "Cargo.toml")); err == nil {
		if !rustEntrypointPresent(wd) {
			return "Cargo.toml present but missing src/main.rs and src/lib.rs"
		}
		if errStr := cargoCheck(wd); errStr != "" {
			return errStr
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "tsconfig.json")); err == nil {
		if errStr := tscCheck(wd); errStr != "" {
			return errStr
		}
	}
	if reason := hollowEntrypointCheck(wd); reason != "" {
		return reason
	}
	return ""
}

// projectTestCheck runs the project’s native test runner by manifest.
// Language-agnostic: Go / Node / Python / Rust. Returns "" when green or N/A.
func projectTestCheck(wd string) string {
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
		if errStr := quickGoTestCheck(wd); errStr != "" {
			return fmt.Sprintf("go test failed: %s", errStr)
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "package.json")); err == nil {
		if errStr := npmTestCheck(wd); errStr != "" {
			return errStr
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "pyproject.toml")); err == nil {
		if errStr := pytestCheck(wd); errStr != "" {
			return errStr
		}
	} else if _, err := os.Stat(filepath.Join(wd, "setup.py")); err == nil {
		if errStr := pytestCheck(wd); errStr != "" {
			return errStr
		}
	} else if _, err := os.Stat(filepath.Join(wd, "requirements.txt")); err == nil {
		// Only run pytest when tests/ exists — avoid false fails on pure libs.
		if _, err := os.Stat(filepath.Join(wd, "tests")); err == nil {
			if errStr := pytestCheck(wd); errStr != "" {
				return errStr
			}
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "Cargo.toml")); err == nil {
		if errStr := cargoTestCheck(wd); errStr != "" {
			return errStr
		}
	}
	return ""
}

func hungTestRunnerMessage(runner string, ctx context.Context, output []byte) string {
	if ctx.Err() == context.DeadlineExceeded && strings.TrimSpace(string(output)) == "" {
		return runner + " failed: command timed out with no output (test suite likely hung — wall-clock wait or deadlock)"
	}
	return ""
}

// npmTestCheck runs `npm test` when a test script exists.
func npmTestCheck(wd string) string {
	if _, err := exec.LookPath("npm"); err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(wd, "package.json"))
	if err != nil {
		return ""
	}
	lower := strings.ToLower(string(data))
	if !strings.Contains(lower, `"test"`) {
		return ""
	}
	// J2-2: install first so node-gyp / missing VS surfaces as host toolchain
	// instead of a vague "Cannot find module" during npm test.
	if errStr := ensureNpmDependencies(wd); errStr != "" {
		return errStr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npm", "test", "--", "--watchAll=false")
	cmd.Dir = wd
	output, err := cmd.CombinedOutput()
	if err != nil {
		if hung := hungTestRunnerMessage("npm test", ctx, output); hung != "" {
			return hung
		}
		errStr := strings.TrimSpace(string(output))
		if errStr == "" {
			errStr = err.Error()
		}
		errStr = clipCommandOutput(errStr, 1200)
		msg := fmt.Sprintf("npm test failed: %s", errStr)
		// J2-2: surface missing VS/node-gyp as host toolchain (not app bug).
		return annotateHostToolchainFailure(msg)
	}
	return ""
}

const npmInstallErrorMarker = "npm_install_last_error.txt"

// ensureProjectDependencies installs language package-manager deps before
// compile/test gates (U1). Cross-language: npm / go mod tidy / pip -r.
// Returns a non-empty error string on hard failure (incl. host toolchain).
func ensureProjectDependencies(wd string) string {
	if _, err := os.Stat(filepath.Join(wd, "package.json")); err == nil {
		if errStr := ensureNpmDependencies(wd); errStr != "" {
			return errStr
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
		goSum := filepath.Join(wd, "go.sum")
		sumInfo, sumErr := os.Stat(goSum)
		needTidy := sumErr != nil || (sumInfo != nil && sumInfo.Size() == 0)
		if !needTidy {
			if _, err := os.Stat(filepath.Join(wd, ".avatars", "go_mod_tidy_failed")); err == nil {
				needTidy = true
			}
		}
		if needTidy {
			if tidyErr := runGoModTidyWithRetry(wd, 3); tidyErr != nil {
				_ = os.MkdirAll(filepath.Join(wd, ".avatars"), 0755)
				_ = os.WriteFile(filepath.Join(wd, ".avatars", "go_mod_tidy_failed"), []byte(tidyErr.Error()), 0644)
				return fmt.Sprintf("go mod tidy failed: %v", tidyErr)
			}
			_ = os.Remove(filepath.Join(wd, ".avatars", "go_mod_tidy_failed"))
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "requirements.txt")); err == nil {
		if errStr := ensurePipRequirements(wd); errStr != "" {
			return errStr
		}
	}
	return ""
}

// ensureNpmDependencies runs `npm install` when deps are missing. Caches
// toolchain failures under .avatars/ so Critic does not re-burn 3 minutes (J2-2).
func ensureNpmDependencies(wd string) string {
	if _, err := exec.LookPath("npm"); err != nil {
		return ""
	}
	marker := filepath.Join(wd, ".avatars", npmInstallErrorMarker)
	if data, err := os.ReadFile(marker); err == nil {
		msg := strings.TrimSpace(string(data))
		if msg != "" {
			return msg
		}
	}
	if !npmDepsNeedInstall(wd) {
		return ""
	}
	var lastMsg string
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		cmd := exec.CommandContext(ctx, "npm", "install", "--no-audit", "--no-fund")
		cmd.Dir = wd
		withInheritedProxyEnv(cmd)
		output, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			_ = os.Remove(marker)
			return ""
		}
		errStr := strings.TrimSpace(string(output))
		if errStr == "" {
			errStr = err.Error()
		}
		errStr = clipCommandOutput(errStr, 1200)
		lastMsg = annotateHostToolchainFailure(fmt.Sprintf("npm install failed: %s", errStr))
		if !looksLikeDependencyNetworkFailure(lastMsg) {
			break
		}
	}
	// Only sticky-cache env failures — transient network should retry later.
	if isHostToolchainFailure(lastMsg) {
		_ = os.MkdirAll(filepath.Join(wd, ".avatars"), 0755)
		_ = os.WriteFile(marker, []byte(lastMsg), 0644)
	}
	return lastMsg
}

func npmDepsNeedInstall(wd string) bool {
	nm := filepath.Join(wd, "node_modules")
	if st, err := os.Stat(nm); err != nil || !st.IsDir() {
		return true
	}
	entries, err := os.ReadDir(nm)
	if err != nil || len(entries) == 0 {
		return true
	}
	data, err := os.ReadFile(filepath.Join(wd, "package.json"))
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	_, hasTSConfig := os.Stat(filepath.Join(wd, "tsconfig.json"))
	needsTS := hasTSConfig == nil || strings.Contains(lower, `"typescript"`)
	if needsTS {
		if _, err := os.Stat(filepath.Join(nm, "typescript")); err != nil {
			return true
		}
	}
	// U1: @types/node declared but missing → tsc TS2591 on node:test.
	if strings.Contains(lower, `"@types/node"`) {
		if _, err := os.Stat(filepath.Join(nm, "@types", "node")); err != nil {
			return true
		}
	}
	// If a known native addon is declared but missing, retry install once.
	for _, dep := range []string{"better-sqlite3", "sqlite3", "bcrypt", "sharp", "canvas", "sql.js", "express", "tsx"} {
		if !strings.Contains(lower, `"`+dep+`"`) {
			continue
		}
		if _, err := os.Stat(filepath.Join(nm, dep)); err != nil {
			return true
		}
	}
	return false
}

const pipInstallMarker = "pip_requirements_installed.ok"

// ensurePipRequirements runs `pip install -r requirements.txt` once per workspace
// when the marker is absent (U1 cross-lang counterpart to npm install).
func ensurePipRequirements(wd string) string {
	marker := filepath.Join(wd, ".avatars", pipInstallMarker)
	if _, err := os.Stat(marker); err == nil {
		return ""
	}
	req := filepath.Join(wd, "requirements.txt")
	if _, err := os.Stat(req); err != nil {
		return ""
	}
	py := "python"
	if _, err := exec.LookPath(py); err != nil {
		py = "python3"
		if _, err := exec.LookPath(py); err != nil {
			return ""
		}
	}
	var lastMsg string
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		cmd := exec.CommandContext(ctx, py, "-m", "pip", "install", "-r", "requirements.txt", "-q")
		cmd.Dir = wd
		withInheritedProxyEnv(cmd)
		output, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			_ = os.MkdirAll(filepath.Join(wd, ".avatars"), 0755)
			_ = os.WriteFile(marker, []byte("ok\n"), 0644)
			return ""
		}
		errStr := strings.TrimSpace(string(output))
		if errStr == "" {
			errStr = err.Error()
		}
		errStr = clipCommandOutput(errStr, 1200)
		lastMsg = fmt.Sprintf("pip install -r requirements.txt failed: %s", errStr)
		if !looksLikeDependencyNetworkFailure(lastMsg) {
			break
		}
	}
	return lastMsg
}

// pytestCheck runs pytest when available.
func pytestCheck(wd string) string {
	runner := ""
	if _, err := exec.LookPath("pytest"); err == nil {
		runner = "pytest"
	} else if _, err := exec.LookPath("python"); err == nil {
		runner = "python"
	} else if _, err := exec.LookPath("python3"); err == nil {
		runner = "python3"
	} else {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runner == "pytest" {
		cmd = exec.CommandContext(ctx, "pytest", "-q")
	} else {
		cmd = exec.CommandContext(ctx, runner, "-m", "pytest", "-q")
	}
	cmd.Dir = wd
	output, err := cmd.CombinedOutput()
	if err != nil {
		if hung := hungTestRunnerMessage("pytest", ctx, output); hung != "" {
			return hung
		}
		errStr := strings.TrimSpace(string(output))
		if errStr == "" {
			errStr = err.Error()
		}
		// Empty collection is not a green suite (same class as Go [no test files]).
		if strings.Contains(strings.ToLower(errStr), "no tests ran") ||
			strings.Contains(strings.ToLower(errStr), "collected 0 items") {
			return "pytest collected no tests"
		}
		errStr = clipCommandOutput(errStr, 1200)
		return fmt.Sprintf("pytest failed: %s", errStr)
	}
	return ""
}

// cargoTestCheck runs `cargo test` when cargo is available.
func cargoTestCheck(wd string) string {
	if _, err := exec.LookPath("cargo"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cargo", "test")
	cmd.Dir = wd
	output, err := cmd.CombinedOutput()
	if err != nil {
		if hung := hungTestRunnerMessage("cargo test", ctx, output); hung != "" {
			return hung
		}
		errStr := strings.TrimSpace(string(output))
		if errStr == "" {
			errStr = err.Error()
		}
		errStr = clipCommandOutput(errStr, 1200)
		return fmt.Sprintf("cargo test failed: %s", errStr)
	}
	return ""
}

// hollowEntrypointCheck rejects stub entrypoints across common layouts.
// Go: cmd/*/main.go without func main
// Python: empty/pass-only main.py / __main__.py
// JS/TS: empty index.js / main.js / main.ts under common roots
func hollowEntrypointCheck(workingDir string) string {
	if reason := hollowCmdMainCheck(workingDir); reason != "" {
		return reason
	}
	candidates := []string{
		"main.py", "__main__.py",
		"main.js", "index.js", "src/main.js", "src/index.js",
		"main.ts", "index.ts", "src/main.ts", "src/index.ts",
		"src/main.rs",
	}
	for _, rel := range candidates {
		path := filepath.Join(workingDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			return fmt.Sprintf("%s is empty", rel)
		}
		lower := strings.ToLower(body)
		switch {
		case strings.HasSuffix(rel, ".py"):
			stripped := strings.TrimSpace(strings.TrimPrefix(lower, "#!/usr/bin/env python3"))
			stripped = strings.TrimSpace(strings.TrimPrefix(stripped, "#!/usr/bin/env python"))
			if stripped == "" || stripped == "pass" || stripped == "pass\n" {
				return fmt.Sprintf("%s is a hollow Python entrypoint", rel)
			}
		case strings.HasSuffix(rel, ".js"), strings.HasSuffix(rel, ".ts"):
			if len(body) < 20 && !strings.Contains(body, "function") && !strings.Contains(body, "=>") && !strings.Contains(body, "export") {
				return fmt.Sprintf("%s looks like a hollow JS/TS entrypoint", rel)
			}
		case strings.HasSuffix(rel, ".rs"):
			// lib.rs crates are fine without fn main; only flag main.rs hollow.
			if strings.HasSuffix(rel, "main.rs") && !strings.Contains(body, "fn main") {
				return fmt.Sprintf("%s has no fn main (hollow entrypoint)", rel)
			}
			if issues := codeCompletenessIssues(body); len(issues) > 0 {
				return fmt.Sprintf("%s looks truncated: %s", rel, issues[0])
			}
		}
	}
	return ""
}
