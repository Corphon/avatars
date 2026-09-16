package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func isFixableCompileTestHealth(err string) bool {
	err = strings.TrimSpace(err)
	if err == "" {
		return false
	}
	lower := strings.ToLower(err)
	if strings.Contains(lower, "host toolchain") || strings.Contains(lower, "not found on path") {
		return false
	}
	needles := []string{
		"go test", "go build", "build failed", "mismatched types", "undefined:",
		"syntax error", "cannot use", "pytest", "npm test", "cargo test",
		"cargo check", "tsc ", "no test files", "collected no tests",
		"[build failed]", "timed out", "hung", "deadlock", "test suite likely hung",
		"errors parsing", "unexpected newline", "redeclared",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func healthRepairUserSuffix(healthErr string) string {
	clip := strings.TrimSpace(healthErr)
	if len(clip) > 1200 {
		clip = clip[:1200] + " [...]"
	}
	return `

=== COMPILE/TEST GATE RED ===
The language compile/test runner failed with:
` + clip + `

Edit existing sources and tests so the project's native runner is green.
Do not claim a fix without writing it. Companion test files stay required.
If the runner timed out or hung, fix tests that block forever (a channel/event the test never signals, unbounded sleep, wait-without-timeout). A hung suite is not green.
`
}

func (e *Engine) retryHealthFailedBuilder(ctx context.Context, work workflowNodeWorkContext, wd, healthErr string) bool {
	if e == nil || e.llm == nil || !isFixableCompileTestHealth(healthErr) {
		return false
	}
	state := e.ensureBuilderRetryState(work.nodeID)
	if state.HealthFailedRetried {
		return false
	}
	state.HealthFailedRetried = true
	state.BuildFailed = true
	state.CompileError = healthErr
	state.AttemptNumber++
	_ = e.emit(work.runID, work.taskID, work.builderAvatarID, "executing", "builder.health_failed_retry", "runtime", map[string]any{
		"node_id": work.nodeID,
		"error":   healthErr,
	}, nil)
	retryFiles, retryErr := e.generateBuilderCode(ctx, work)
	if retryErr != nil && len(retryFiles) == 0 {
		return false
	}
	_ = applyHealthRetryFiles(wd, retryFiles)
	return true
}

func applyHealthRetryFiles(wd string, files []builderCodeFile) int {
	n := 0
	kept, _, _ := sanitizeBuilderPathsForTask(files, "")
	for _, cf := range kept {
		if cf.AlreadyOnDisk || strings.TrimSpace(cf.Content) == "" {
			continue
		}
		rel := filepath.ToSlash(strings.TrimSpace(cf.Path))
		if rel == "" || strings.HasPrefix(rel, "docs/workflow/") {
			continue
		}
		full := rel
		if !filepath.IsAbs(full) {
			full = filepath.Join(wd, filepath.FromSlash(rel))
		}
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			continue
		}
		if err := writeFileAtomic(full, []byte(cf.Content)); err != nil {
			continue
		}
		n++
	}
	return n
}
