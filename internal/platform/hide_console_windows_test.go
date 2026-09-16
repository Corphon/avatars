//go:build windows

package platform

import (
	"os/exec"
	"testing"
)

func TestHideConsoleWindow_SetsNoWindowFlags(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "echo ok")
	HideConsoleWindow(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("expected SysProcAttr")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("expected HideWindow")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW, flags=%#x", cmd.SysProcAttr.CreationFlags)
	}
}

func TestCommand_AppliesConsoleSuppression(t *testing.T) {
	cmd := Command("cmd", "/c", "echo ok")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("Command should hide the Windows console")
	}
}
