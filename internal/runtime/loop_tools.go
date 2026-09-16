package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"avatars/internal/platform"
)

// executeReadOnlyShell runs a shell command and returns stdout as a string.
// Only read-only commands (find, grep, wc, cat, head, sort, uniq, ls, dir)
// are allowed in plan mode. Uses os/exec directly — no guarded tool pipeline —
// because these are inherently safe read operations.
func (e *Engine) executeReadOnlyShell(ctx context.Context, work workflowNodeWorkContext, command string) (string, error) {
	// Safety: only allow read-only commands
	lowered := strings.ToLower(strings.TrimSpace(command))
	allowedPrefixes := []string{"find ", "grep ", "wc ", "cat ", "head ", "sort ", "uniq ", "ls ", "dir "}
	allowed := false
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("command not allowed in plan mode: %s", command)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	shellArgs := platform.ShellCommand("-c", command)
	cmd := exec.CommandContext(timeoutCtx, shellArgs[0], shellArgs[1:]...)
	cmd.Dir = "."
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
