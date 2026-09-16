package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const defaultShellTimeoutMillis = 30000

// shellRedirectionRx matches shell output redirection operators
// (>, >>, 2>, 1>, &>, >&, etc.) that could be used to overwrite
// files outside the working directory.
var shellRedirectionRx = regexp.MustCompile(`\d?>{1,2}|>&\d?`)

// dangerousShellTokens lists command substrings that are unconditionally
// blocked regardless of permission mode. The list is conservative: it covers
// destructive filesystem operations, privilege escalation, and remote-code-
// execution via curl-pipe-shell chains. All tokens are lowercase for
// case-insensitive matching.
var dangerousShellTokens = []string{
	"rm -rf /",
	"rm -rf --no-preserve-root",
	"mkfs.",
	"dd if=",
	"> /dev/sda",
	"sudo ",
	"chmod 777 /",
	"chown -r",
	"chown -R",
}

// pipeToShellTokens lists patterns that indicate a command piping output
// directly into a shell interpreter.
var pipeToShellTokens = []string{
	" | sh",
	" | bash",
	" | /bin/sh",
	" | /bin/bash",
}

// remoteFetchTokens lists command prefixes that fetch remote content and
// are blocked because they are commonly used in RCE chains.
var remoteFetchTokens = []string{
	"curl ",
	"wget ",
}

// pathTraversalShellTokens lists patterns that indicate an attempt to escape
// the working directory via path traversal.
var pathTraversalShellTokens = []string{
	"/etc/passwd",
	"/etc/shadow",
	"/root/",
	"/proc/",
	"/sys/",
	"~/.ssh",
	"~/.gnupg",
	"AppData",
	"NTUSER.DAT",
}

// ValidateShellCommand performs pre-execution safety checks on shell commands.
// It returns nil if the command is safe to execute, or an error describing the
// blocked pattern. This is the first line of defense; approval gating in the
// runtime layer provides a second independent check before the tool is invoked.
func ValidateShellCommand(command []string) error {
	return validateShellCommand(command)
}

func validateShellCommand(command []string) error {
	joined := strings.Join(command, " ")

	// Check for redirection operators (>, >>, 2>, &>, etc.).
	if shellRedirectionRx.MatchString(joined) {
		return fmt.Errorf("shell command contains output redirection: %q", truncateShellCommand(joined, 120))
	}

	lowered := strings.ToLower(joined)

	// Check for remote fetch commands (curl, wget) — blocked unconditionally
	// because they are a common RCE vector.
	for _, token := range remoteFetchTokens {
		if strings.Contains(lowered, token) {
			return fmt.Errorf("shell command contains remote fetch: %q", truncateShellCommand(joined, 120))
		}
	}

	// Check for pipe-to-shell chains.
	if strings.Contains(joined, "|") {
		for _, token := range pipeToShellTokens {
			if strings.Contains(lowered, token) {
				return fmt.Errorf("shell command contains pipe-to-shell: %q", truncateShellCommand(joined, 120))
			}
		}
	}

	// Check for dangerous destructive commands.
	for _, token := range dangerousShellTokens {
		if strings.Contains(lowered, token) {
			return fmt.Errorf("shell command contains dangerous pattern: %q", truncateShellCommand(joined, 120))
		}
	}

	// Check for path traversal to sensitive locations.
	for _, token := range pathTraversalShellTokens {
		if strings.Contains(joined, token) {
			return fmt.Errorf("shell command references sensitive path: %q", truncateShellCommand(joined, 120))
		}
	}

	// SEC-3: Shell injection hardening — command chaining separators.
	// Block ; && || unless inside a quoted string.
	if hasUnquotedSeparator(joined) {
		return fmt.Errorf("shell command contains command separator (; && ||): %q", truncateShellCommand(joined, 120))
	}

	// SEC-3: Shell injection hardening — command substitution.
	if strings.Contains(joined, "`") {
		return fmt.Errorf("shell command contains backtick substitution: %q", truncateShellCommand(joined, 120))
	}
	if strings.Contains(joined, "$(") {
		return fmt.Errorf("shell command contains $(...) substitution: %q", truncateShellCommand(joined, 120))
	}

	// SEC-3: Shell injection hardening — dangerous env vars.
	for _, envVar := range dangerousEnvVars {
		if strings.Contains(joined, envVar) {
			return fmt.Errorf("shell command sets dangerous env var: %q", truncateShellCommand(joined, 120))
		}
	}

	return nil
}

// dangerousEnvVars lists environment variables that can alter program behavior
// in security-sensitive ways (dynamic linker hijacking, library injection).
var dangerousEnvVars = []string{
	"LD_PRELOAD=",
	"LD_LIBRARY_PATH=",
	"DYLD_INSERT_LIBRARIES=",
	"DYLD_LIBRARY_PATH=",
}

// hasUnquotedSeparator checks for command chaining separators (; && ||)
// outside of quoted strings. Simple heuristic: if the separator appears
// and there's no unbalanced quote count in the prefix, it's unquoted.
func hasUnquotedSeparator(command string) bool {
	// Check for ; — only flag if not inside quotes.
	for i := 0; i < len(command); i++ {
		if command[i] == ';' {
			if !isInsideQuotes(command, i) {
				return true
			}
		}
	}
	// Check for && and || — these are always suspicious in shell commands.
	if strings.Contains(command, "&&") || strings.Contains(command, "||") {
		return true
	}
	return false
}

// isInsideQuotes checks if position i in s is inside a quoted string.
func isInsideQuotes(s string, i int) bool {
	inSingle := false
	inDouble := false
	for j := 0; j < i; j++ {
		switch s[j] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		}
	}
	return inSingle || inDouble
}

func truncateShellCommand(command string, limit int) string {
	if len(command) <= limit {
		return command
	}
	return command[:limit] + "..."
}

type shellExecutor interface {
	Run(ctx context.Context, workingDir string, command []string) (string, error)
}

type execShellExecutor struct{}

func (execShellExecutor) Run(ctx context.Context, workingDir string, command []string) (string, error) {
	if len(command) == 0 {
		return "", errors.New("shell command cannot be empty")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workingDir
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

type ShellTool struct {
	executor shellExecutor
}

func (ShellTool) Name() string {
	return "shell"
}

func (ShellTool) IsConcurrencySafe(input any) bool {
	return false
}

func (t ShellTool) Call(ctx context.Context, input any) (Result, error) {
	shellInput, err := normalizeShellInput(input)
	if err != nil {
		return Result{}, err
	}

	workingDir := filepath.Clean(shellInput.WorkingDir)
	info, err := os.Stat(workingDir)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("shell tool requires a directory working dir, got file: %s", workingDir)
	}

	if err := validateShellCommand(shellInput.Command); err != nil {
		return Result{}, err
	}

	timeout := time.Duration(shellInput.TimeoutMillis) * time.Millisecond
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executor := t.executor
	if executor == nil {
		executor = execShellExecutor{}
	}

	output, err := executor.Run(callCtx, workingDir, shellInput.Command)
	trimmed := strings.TrimSpace(output)
	if err != nil {
		if trimmed == "" {
			trimmed = err.Error()
		} else {
			trimmed = trimmed + "\n" + err.Error()
		}
		return Result{Content: trimmed}, fmt.Errorf("shell tool command failed: %w", err)
	}

	return Result{Content: trimmed}, nil
}

func normalizeShellInput(input any) (ShellInput, error) {
	switch value := input.(type) {
	case ShellInput:
		return finalizeShellInput(value)
	case *ShellInput:
		if value == nil {
			return ShellInput{}, errors.New("shell tool input cannot be nil")
		}
		return finalizeShellInput(*value)
	default:
		return ShellInput{}, errors.New("shell tool expects tools.ShellInput")
	}
}

func finalizeShellInput(input ShellInput) (ShellInput, error) {
	if len(input.Command) == 0 {
		return ShellInput{}, errors.New("shell tool command cannot be empty")
	}
	if strings.TrimSpace(input.WorkingDir) == "" {
		return ShellInput{}, errors.New("shell tool working dir cannot be empty")
	}
	if input.TimeoutMillis <= 0 {
		input.TimeoutMillis = defaultShellTimeoutMillis
	}
	return input, nil
}
