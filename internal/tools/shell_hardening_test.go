package tools

import "testing"

func TestShellHardening_BlocksSemicolon(t *testing.T) {
	err := validateShellCommand([]string{"echo", "hello", ";", "rm", "-rf", "/"})
	if err == nil {
		t.Fatal("should block semicolon separator")
	}
}

func TestShellHardening_BlocksDoubleAmpersand(t *testing.T) {
	err := validateShellCommand([]string{"echo", "a", "&&", "echo", "b"})
	if err == nil {
		t.Fatal("should block &&")
	}
}

func TestShellHardening_BlocksDoublePipe(t *testing.T) {
	err := validateShellCommand([]string{"echo", "a", "||", "echo", "b"})
	if err == nil {
		t.Fatal("should block ||")
	}
}

func TestShellHardening_BlocksDollarParen(t *testing.T) {
	err := validateShellCommand([]string{"echo", "$(whoami)"})
	if err == nil {
		t.Fatal("should block $()")
	}
}

func TestShellHardening_BlocksBacktick(t *testing.T) {
	err := validateShellCommand([]string{"echo", "`whoami`"})
	if err == nil {
		t.Fatal("should block backtick")
	}
}

func TestShellHardening_BlocksLDPreload(t *testing.T) {
	err := validateShellCommand([]string{"env", "LD_PRELOAD=evil.so", "ls"})
	if err == nil {
		t.Fatal("should block LD_PRELOAD")
	}
}

func TestShellHardening_AllowsNormalCommands(t *testing.T) {
	for _, cmd := range [][]string{
		{"go", "build", "./..."},
		{"git", "status"},
		{"ls", "-la"},
		{"python", "-m", "py_compile", "test.py"},
	} {
		if err := validateShellCommand(cmd); err != nil {
			t.Fatalf("should allow %v, got: %v", cmd, err)
		}
	}
}
