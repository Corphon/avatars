// Package runtime — cross-language health check aggregator.
//
// P2 fix: Aggregates go build + python/node syntax checks to prevent
// false-positive "completed" status when generated code doesn't compile.
// Inspired by claude_code_main/verificationAgent.ts VERDICT contract.
//
// This is called from runCompletionStatus to verify that the run's output
// actually passes compilation checks before reporting "completed".

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

// crossLangHealthCheck runs language-appropriate compilation checks on all
// source files in the working directory. Returns "" if all checks pass,
// or an error string describing the first failure.
//
// R10-3: manifests (Cargo.toml / go.mod / …) force the matching checker even
// when the source scan is empty or previously omitted a language extension.
func crossLangHealthCheck(wd string) string {
	files := scanRecentSourceFiles(wd)

	hasGo, hasPy, hasJS, hasTS := false, false, false, false
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".go":
			hasGo = true
		case ".py":
			hasPy = true
		case ".js", ".mjs", ".cjs", ".jsx":
			hasJS = true
		case ".ts", ".tsx":
			hasTS = true
		}
	}

	_, hasGoMod := os.Stat(filepath.Join(wd, "go.mod"))
	_, hasCargo := os.Stat(filepath.Join(wd, "Cargo.toml"))
	_, hasPackageJSON := os.Stat(filepath.Join(wd, "package.json"))
	_, hasTSConfig := os.Stat(filepath.Join(wd, "tsconfig.json"))
	_, hasPyProject := os.Stat(filepath.Join(wd, "pyproject.toml"))
	_, hasRequirements := os.Stat(filepath.Join(wd, "requirements.txt"))
	_, hasSetup := os.Stat(filepath.Join(wd, "setup.py"))

	if hasGoMod == nil {
		hasGo = true
	}
	if hasPackageJSON == nil {
		hasJS = true
	}
	if hasTSConfig == nil {
		hasTS = true
	}
	if hasPyProject == nil || hasRequirements == nil || hasSetup == nil {
		hasPy = true
	}

	// Truly empty workspace: no sources and no language manifest.
	if len(files) == 0 && hasGoMod != nil && hasCargo != nil && hasPackageJSON != nil &&
		hasTSConfig != nil && hasPyProject != nil && hasRequirements != nil && hasSetup != nil {
		return ""
	}

	// U1: deps before compile/test (npm / go tidy / pip).
	if errStr := ensureProjectDependencies(wd); errStr != "" {
		return errStr
	}

	// Go: reject split/hollow package layouts before compile can false-green (F34).
	if hasGo {
		if errStr := detectConflictingGoLayout(wd); errStr != "" {
			if clean, _ := healConflictingGoLayout(wd); !clean {
				if errStr2 := detectConflictingGoLayout(wd); errStr2 != "" {
					return errStr2
				}
			}
		}
		if errStr := quickGoBuildCheck(wd); errStr != "" {
			return fmt.Sprintf("go build failed after run completion: %s", errStr)
		}
	}

	// Rust: require an entrypoint, then cargo check (R10-3/8).
	if hasCargo == nil {
		if !rustEntrypointPresent(wd) {
			return "Cargo.toml present but missing src/main.rs and src/lib.rs"
		}
		if reason := cargoCheck(wd); reason != "" {
			return reason
		}
	}

	// TypeScript: run `tsc --noEmit` if tsconfig.json exists. S1.
	if hasTS {
		if hasTSConfig == nil {
			if reason := tscCheck(wd); reason != "" {
				return reason
			}
		}
	}

	// Node.js: syntax when no package.json (tests via projectTestCheck).
	if hasJS {
		if hasPackageJSON != nil {
			for _, f := range files {
				ext := strings.ToLower(filepath.Ext(f))
				if ext == ".js" || ext == ".mjs" || ext == ".cjs" {
					data, err := os.ReadFile(f)
					if err != nil {
						continue
					}
					if reason := checkNodeJSSyntax(f, string(data)); reason != "" {
						return reason
					}
				}
			}
		}
	}

	// Python: py_compile when no pytest manifest (tests via projectTestCheck).
	if hasPy {
		if hasPyProject != nil && hasSetup != nil {
			for _, f := range files {
				if strings.HasSuffix(f, ".py") {
					if reason := pyCompileCheck(f); reason != "" {
						return reason
					}
				}
			}
		}
	}

	// Native test runners for every language present (R3-13/14, cross-lang).
	if errStr := projectTestCheck(wd); errStr != "" {
		return errStr
	}
	return ""
}

// rustEntrypointPresent reports src/main.rs or src/lib.rs on disk (R10-8).
func rustEntrypointPresent(wd string) bool {
	for _, rel := range []string{
		filepath.Join("src", "main.rs"),
		filepath.Join("src", "lib.rs"),
	} {
		if st, err := os.Stat(filepath.Join(wd, rel)); err == nil && !st.IsDir() && st.Size() > 0 {
			return true
		}
	}
	return false
}

// cargoCheck runs `cargo check` in the given directory. S1.
// R10-3: missing cargo on PATH is a hard failure when Cargo.toml exists —
// never silently green-wash a Rust tree.
func cargoCheck(wd string) string {
	if _, err := exec.LookPath("cargo"); err != nil {
		return annotateHostToolchainFailure("cargo not found on PATH — cannot verify Rust project")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cargo", "check")
	cmd.Dir = wd
	output, err := cmd.CombinedOutput()
	if err != nil {
		errStr := string(output)
		errStr = clipCommandOutput(errStr, 1200)
		msg := fmt.Sprintf("cargo check failed: %s", strings.TrimSpace(errStr))
		return annotateHostToolchainFailure(msg)
	}
	return ""
}

// tscCheck runs `tsc --noEmit` in the given directory. S1.
// Prefers local node_modules/.bin/tsc after ensureProjectDependencies (U1).
func tscCheck(wd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	localTsc := filepath.Join(wd, "node_modules", ".bin", "tsc")
	if _, err := os.Stat(localTsc); err == nil {
		cmd = exec.CommandContext(ctx, localTsc, "--noEmit")
	} else if _, err := os.Stat(localTsc + ".cmd"); err == nil {
		cmd = exec.CommandContext(ctx, localTsc+".cmd", "--noEmit")
	} else if _, err := exec.LookPath("tsc"); err == nil {
		cmd = exec.CommandContext(ctx, "tsc", "--noEmit")
	} else if _, err := exec.LookPath("npx"); err == nil {
		cmd = exec.CommandContext(ctx, "npx", "--no-install", "tsc", "--noEmit")
	} else {
		return "" // tsc not available
	}
	cmd.Dir = wd
	output, err := cmd.CombinedOutput()
	if err != nil {
		errStr := string(output)
		errStr = clipCommandOutput(errStr, 1200)
		return fmt.Sprintf("tsc --noEmit failed: %s", strings.TrimSpace(errStr))
	}
	return ""
}

// pyCompileCheck validates a single Python file (syntax + optional py_compile).
func pyCompileCheck(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if reason := checkPythonSyntax(path, string(data)); reason != "" {
		return reason
	}
	py := "python"
	if _, err := exec.LookPath(py); err != nil {
		py = "python3"
		if _, err := exec.LookPath(py); err != nil {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-m", "py_compile", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		errStr := strings.TrimSpace(string(output))
		if errStr == "" {
			errStr = err.Error()
		}
		errStr = clipCommandOutput(errStr, 1200)
		return fmt.Sprintf("py_compile %s: %s", path, errStr)
	}
	return ""
}
