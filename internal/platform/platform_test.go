package platform

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestShellCommand_UsesHostShell(t *testing.T) {
	got := ShellCommand("-c", "echo hi")
	if runtime.GOOS == "windows" {
		want := []string{"cmd", "/c", "echo hi"}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("windows shell: got %v", got)
		}
		return
	}
	if len(got) != 3 || got[0] != "bash" || got[1] != "-c" || got[2] != "echo hi" {
		t.Fatalf("unix shell: got %v", got)
	}
}

func TestIsWindows_MatchesGOOS(t *testing.T) {
	if IsWindows() != (runtime.GOOS == "windows") {
		t.Fatalf("IsWindows=%v GOOS=%s", IsWindows(), runtime.GOOS)
	}
}

func TestShellCheckCommand_UnavailableOnWindows(t *testing.T) {
	name, ok := ShellCheckCommand()
	if runtime.GOOS == "windows" {
		if ok || name != "" {
			t.Fatalf("windows should report shellcheck unavailable, got %q ok=%v", name, ok)
		}
		return
	}
	if !ok || name != "bash" {
		t.Fatalf("unix shellcheck: name=%q ok=%v", name, ok)
	}
}

func TestCommand_SetsNameAndArgs(t *testing.T) {
	cmd := Command("go", "version")
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	if len(cmd.Args) < 2 || cmd.Args[len(cmd.Args)-1] != "version" {
		t.Fatalf("args=%v", cmd.Args)
	}
}

func TestCommandContext_SetsNameAndArgs(t *testing.T) {
	cmd := CommandContext(context.Background(), "go", "env", "GOOS")
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	if len(cmd.Args) < 3 || cmd.Args[len(cmd.Args)-1] != "GOOS" {
		t.Fatalf("args=%v", cmd.Args)
	}
}

func TestCommandContext_RespectsCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	cmd := CommandContext(ctx, "go", "version")
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	if cmd.Err != nil && ctx.Err() == nil {
		t.Fatalf("unexpected cmd.Err=%v", cmd.Err)
	}
}

func TestHideConsoleWindow_NilSafe(t *testing.T) {
	HideConsoleWindow(nil)
}

func TestUTF8BOM_Bytes(t *testing.T) {
	if len(UTF8BOM) != 3 || UTF8BOM[0] != 0xEF || UTF8BOM[1] != 0xBB || UTF8BOM[2] != 0xBF {
		t.Fatalf("UTF8BOM=%v", UTF8BOM)
	}
}

func TestConsoleOutputCodePage_DoesNotPanic(t *testing.T) {
	_ = ConsoleOutputCodePage()
}
