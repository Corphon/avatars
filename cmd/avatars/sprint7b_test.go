package main

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestS7B_OfflineIntentRoute_NoLLM(t *testing.T) {
	cases := []struct {
		input   string
		command string
	}{
		{"analyze this repo and do not modify files", "run --new-task --permission-mode plan"},
		{"分析项目，找问题，写入 anal.md", "run --new-task --permission-mode plan"},
		{"analyze repo task=demo-task", "run --task demo-task analyze repo task=demo-task"},
		{"verify current project", "verify"},
		{"smoke repl routing", "smoke repl-routing --new-task"},
	}
	for _, tc := range cases {
		cands := offlineIntentCandidates(tc.input)
		if len(cands) == 0 {
			t.Fatalf("%q: no candidates", tc.input)
		}
		got := strings.Join(cands[0].Command, " ")
		if !strings.Contains(got, tc.command) {
			t.Fatalf("%q: got %q want contain %q", tc.input, got, tc.command)
		}
	}
}

func TestS7B_ClassifyCLIResumeMode(t *testing.T) {
	if got := classifyCLIResumeMode(true, false); got != resumeModeRestoreOnly {
		t.Fatalf("resume cmd: %s", got)
	}
	if got := classifyCLIResumeMode(false, true); got != resumeModeContinueFromPause {
		t.Fatalf("continue-from-pause: %s", got)
	}
	if got := classifyCLIResumeMode(false, false); got != resumeModeSoftReplan {
		t.Fatalf("run --resume: %s", got)
	}
}

func TestS7B_ParseContinueFromPause(t *testing.T) {
	options, err := parseRunCommandOptions([]string{"--continue-from-pause", "sess.jsonl", "continue the task"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.ContinueFromPause || options.ResumeTranscript != "sess.jsonl" {
		t.Fatalf("got %+v", options)
	}
	if options.Input != "continue the task" {
		t.Fatalf("input=%q", options.Input)
	}
}

func TestS7B_ResumeBannerRestoreOnly(t *testing.T) {
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	var (
		content []byte
		readErr error
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		content, readErr = io.ReadAll(reader)
	}()
	printResumeDispatchBanner(resumeModeRestoreOnly, "t.jsonl", "awaiting_approval")
	_ = writer.Close()
	wg.Wait()
	os.Stdout = original
	if readErr != nil {
		t.Fatal(readErr)
	}
	output := string(content)
	if !strings.Contains(output, "restore-only") || !strings.Contains(output, "does not continue the DAG") {
		t.Fatalf("banner=%s", output)
	}
	if !strings.Contains(output, "Boundary type: awaiting_approval") {
		t.Fatalf("missing boundary: %s", output)
	}
	if !strings.Contains(output, "run --continue-from-pause t.jsonl") {
		t.Fatalf("missing continue hint: %s", output)
	}
}
