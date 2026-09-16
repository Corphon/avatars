//go:build windows

package platform

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW prevents a console window from flashing for child processes.
const createNoWindow = 0x08000000

// HideConsoleWindow configures cmd so Windows does not flash a console for
// short-lived toolchain children (go/npm/cargo/python/cmd). No-op on Unix.
func HideConsoleWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
