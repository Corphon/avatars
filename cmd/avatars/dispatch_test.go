package main

import (
	"strings"
	"testing"
)

func TestC11_Dispatch_RunVsIntent(t *testing.T) {
	runErr := run([]string{"run"})
	if runErr == nil || !strings.Contains(runErr.Error(), "task input cannot be empty") {
		t.Fatalf("avatars run with no task should hit runTask, got %v", runErr)
	}
	intentErr := run([]string{"intent"})
	if intentErr == nil || !strings.Contains(intentErr.Error(), "intent cannot be empty") {
		t.Fatalf("avatars intent with no text should hit runIntent, got %v", intentErr)
	}
}

func TestC11_Dispatch_BareTextIsIntentNotRun(t *testing.T) {
	// Compact NL: first token is not a command name → runIntent, not runTask.
	err := run([]string{"--from-file"})
	if err == nil {
		t.Fatal("expected intent path error for incomplete --from-file")
	}
	if strings.Contains(err.Error(), "task input cannot be empty") {
		t.Fatalf("bare/flag input must not dispatch as avatars run: %v", err)
	}
}
