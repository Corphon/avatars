// Package platform provides OS-aware helpers for shell command execution.
// On Windows, it returns cmd.exe-based commands; on Unix, bash.
package platform

import (
	"context"
	"os/exec"
	"runtime"
)

// ShellCommand returns the shell executable and the flag for passing a
// command string. Use this instead of hardcoding "bash" / "-c".
//
//	cmd := exec.Command(platform.ShellCommand("-c", command)...)
func ShellCommand(flag string, command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", command}
	}
	return []string{"bash", flag, command}
}

// IsWindows returns true when running on Windows.
func IsWindows() bool {
	return runtime.GOOS == "windows"
}

// ShellCheckCommand returns the command for syntax-checking a shell script.
// On Windows, this is typically unavailable; callers should check first.
func ShellCheckCommand() (string, bool) {
	if runtime.GOOS == "windows" {
		return "", false
	}
	return "bash", true
}

// CommandContext is exec.CommandContext with Windows console-flash suppression
// applied (F48). Prefer this for short-lived toolchain children across languages.
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	HideConsoleWindow(cmd)
	return cmd
}

// Command is exec.Command with Windows console-flash suppression (F48).
func Command(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	HideConsoleWindow(cmd)
	return cmd
}
