//go:build !windows

package platform

import "os/exec"

// HideConsoleWindow is a no-op on non-Windows platforms.
func HideConsoleWindow(cmd *exec.Cmd) {}
