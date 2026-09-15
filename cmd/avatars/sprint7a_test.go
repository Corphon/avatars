package main

import (
	"testing"

	"avatars/internal/runtime"
)

func TestS7A_ParseServeRunDemo(t *testing.T) {
	options, err := parseServeCommandOptions([]string{"--run-demo", "--task", "demo-task", "Inspect workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.RunDemo {
		t.Fatal("expected --run-demo")
	}
	if options.TaskID != "demo-task" {
		t.Fatalf("task id: %q", options.TaskID)
	}
	if options.Input != "Inspect workspace" {
		t.Fatalf("input: %q", options.Input)
	}
	if options.PermissionMode != runtime.PermissionModeAcceptEdits {
		t.Fatalf("serve still defaults to acceptEdits, got %q", options.PermissionMode)
	}
}

func TestS7A_ParseServeWithoutRunDemo(t *testing.T) {
	options, err := parseServeCommandOptions([]string{"--task", "demo-task"})
	if err != nil {
		t.Fatal(err)
	}
	if options.RunDemo {
		t.Fatal("run-demo must stay off unless flagged")
	}
}

func TestS7A_NaturalLanguagePermissionMode_MutatingUsesAcceptEdits(t *testing.T) {
	if got := naturalLanguagePermissionMode("implement a jwt library with tests"); got != "acceptEdits" {
		t.Fatalf("got %q", got)
	}
	if got := naturalLanguagePermissionMode("analyze this repository"); got != "plan" {
		t.Fatalf("got %q", got)
	}
}
