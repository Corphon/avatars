package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikeResumeContinuationIntent(t *testing.T) {
	yes := []string{
		"请接着把刚才那轮没过门的修到能过 go test ./...",
		"接着修到 pytest 绿",
		"刚才那轮还是红的，接着修",
		"fix the failing tests",
		"continue fixing the failing tests",
		"make the tests pass",
		"继续推进",
		"继续",
		"keep going",
	}
	for _, in := range yes {
		if !looksLikeResumeContinuationIntent(strings.ToLower(in)) {
			t.Fatalf("expected continuation: %q", in)
		}
	}
	no := []string{
		"接着做一个开源库 ttlcache，从零写",
		"从零新建一个 library",
		"create a rust crate from scratch",
	}
	for _, in := range no {
		if looksLikeResumeContinuationIntent(strings.ToLower(in)) {
			t.Fatalf("greenfield must not look like resume: %q", in)
		}
	}
}

func TestRewriteRunCommandToResume(t *testing.T) {
	cmd := []string{"run", "--new-task", "--permission-mode", "acceptEdits", "接着修到能过 go test"}
	got := rewriteRunCommandToResume(cmd, ".avatars/tasks/demo/sessions/s.jsonl", "fallback")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "run --resume .avatars/tasks/demo/sessions/s.jsonl") {
		t.Fatalf("missing --resume: %q", joined)
	}
	if strings.Contains(joined, "--new-task") {
		t.Fatalf("still --new-task: %q", joined)
	}
	if !strings.Contains(joined, "--permission-mode acceptEdits") {
		t.Fatalf("lost permission mode: %q", joined)
	}
	if !strings.Contains(joined, "接着修到能过 go test") {
		t.Fatalf("lost user text: %q", joined)
	}
}

func TestCoerceContinuationRunToResume(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(".avatars", "tasks", "go-hashringx-demo", "sessions")
	if err := os.MkdirAll(sess, 0755); err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(sess, "run.jsonl")
	if err := os.WriteFile(tp, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	input := "请接着把刚才那轮没过门的修到能过 go test ./..."
	nd := naturalLanguageDecision{
		Kind:    naturalLanguageDecisionSafeRun,
		Reason:  "llm router: coding work",
		Command: []string{"run", "--new-task", "--permission-mode", "acceptEdits", input},
	}
	got := coerceContinuationRunToResume(nd, input, strings.ToLower(input))
	joined := strings.Join(got.Command, " ")
	if !strings.Contains(joined, "--resume") {
		t.Fatalf("F87: expected --resume, got %q", joined)
	}
	if strings.Contains(joined, "--new-task") {
		t.Fatalf("F87: still --new-task: %q", joined)
	}
	if !strings.Contains(joined, "--task go-hashringx-demo") {
		t.Fatalf("F87: expected existing task id, got %q", joined)
	}
}

func TestLooksLikeUserRejectedResume(t *testing.T) {
	if !looksLikeUserRejectedResume("不要 resume，请开新任务") {
		t.Fatal("plain 不要 resume should reject coerce")
	}
	if looksLikeUserRejectedResume("请继续写代码，不要 resume 一个莫名其妙的数字") {
		t.Fatal("不要 resume 数字 still wants the real transcript")
	}
	if looksLikeUserRejectedResume("don't resume that number, keep going") {
		t.Fatal("don't resume that number still wants continuation")
	}
}

func TestSanitizeResumeTranscriptArg_ReplacesSnowflake(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(".avatars", "tasks", "ttlcache-demo", "sessions")
	if err := os.MkdirAll(sess, 0755); err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(sess, "run.jsonl")
	if err := os.WriteFile(tp, []byte(strings.Repeat("{}\n", 40)), 0644); err != nil {
		t.Fatal(err)
	}

	got := sanitizeResumeTranscriptArg([]string{"run", "--resume", "1788158629149528000", "--permission-mode", "acceptEdits", "继续写"})
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "1788158629149528000") {
		t.Fatalf("F113: snowflake must not survive: %q", joined)
	}
	if !strings.Contains(joined, "--resume") || !strings.Contains(joined, ".jsonl") {
		t.Fatalf("F113: expected transcript path, got %q", joined)
	}
	if !strings.Contains(joined, "--task ttlcache-demo") {
		t.Fatalf("F113: expected --task bind, got %q", joined)
	}
}

func TestLatestResumableTranscriptPath_PrefersSubstantialOverPolluted(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	origSess := filepath.Join(".avatars", "tasks", "go-ttlcache-real", "sessions")
	pollSess := filepath.Join(".avatars", "tasks", "prompttxt-skill-go-go-test-e92d1d2f", "sessions")
	if err := os.MkdirAll(origSess, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pollSess, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origSess, "old.jsonl"), []byte(strings.Repeat("event\n", 80)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pollSess, "new.jsonl"), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := latestResumableTranscriptPath(".")
	if !strings.Contains(got, "go-ttlcache-real") {
		t.Fatalf("F114: expected original task transcript, got %q", got)
	}
	if strings.Contains(got, "prompttxt-") {
		t.Fatalf("F114: must not pick polluted slug: %q", got)
	}
}

func TestCoerceContinuation_DoesNotOverrideExplicitNewTaskReject(t *testing.T) {
	nd := naturalLanguageDecision{
		Kind:    naturalLanguageDecisionSafeRun,
		Command: []string{"run", "--new-task", "--permission-mode", "acceptEdits", "不要 resume，开新任务继续做"},
	}
	got := coerceContinuationRunToResume(nd, nd.Command[len(nd.Command)-1], strings.ToLower(nd.Command[len(nd.Command)-1]))
	joined := strings.Join(got.Command, " ")
	if strings.Contains(joined, "--resume") {
		t.Fatalf("F114: explicit 不要 resume should keep --new-task, got %q", joined)
	}
}
