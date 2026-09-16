// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package verification

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeExecutor struct {
	results map[string]fakeExecution
}

type fakeExecution struct {
	output string
	err    error
}

func (f fakeExecutor) Run(ctx context.Context, workingDir string, command []string) (string, error) {
	key := ""
	for index, part := range command {
		if index > 0 {
			key += " "
		}
		key += part
	}
	result, ok := f.results[key]
	if !ok {
		return "", errors.New("unexpected command")
	}
	return result.output, result.err
}

func newRaceRunnerForTest(executor Executor) Runner {
	checks := append(DefaultChecks(), DefaultRaceChecks()...)
	return NewRunner(".", executor, checks)
}

func TestRunnerRun_AllChecksPass(t *testing.T) {
	runner := NewRunner(".", fakeExecutor{results: map[string]fakeExecution{
		"go build ./...": {output: ""},
		"go test ./...":  {output: "ok\n"},
		"go vet ./...":   {output: ""},
		"go fmt ./...":   {output: ""},
	}}, DefaultChecks())

	report := runner.Run(context.Background())
	if report.Verdict != VerdictPass {
		t.Fatalf("expected PASS, got %s", report.Verdict)
	}
	if len(report.Checks) != 4 {
		t.Fatalf("expected 4 checks, got %d", len(report.Checks))
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", report.Warnings)
	}
}

func TestRaceRunnerRun_RaceCheckCanBePartial(t *testing.T) {
	runner := newRaceRunnerForTest(fakeExecutor{results: map[string]fakeExecution{
		"go build ./...":      {output: ""},
		"go test ./...":       {output: "ok\n"},
		"go test -race ./...": {output: "go: -race requires cgo; enable cgo by setting CGO_ENABLED=1", err: errors.New("exit status 2")},
		"go vet ./...":        {output: ""},
		"go fmt ./...":        {output: ""},
	}})

	report := runner.Run(context.Background())
	if report.Verdict != VerdictPartial {
		t.Fatalf("expected PARTIAL, got %s", report.Verdict)
	}
	if len(report.Warnings) != 1 {
		t.Fatalf("expected one warning, got %v", report.Warnings)
	}
	raceIdx := -1
	for i, c := range report.Checks {
		if c.Name == "go test -race" || strings.Contains(c.CommandRun, "-race") {
			raceIdx = i
			break
		}
	}
	if raceIdx < 0 {
		t.Fatalf("race check missing: %+v", report.Checks)
	}
	if report.Checks[raceIdx].Result != VerdictPartial {
		t.Fatalf("expected race check partial, got %s", report.Checks[raceIdx].Result)
	}
}

func TestRunnerRun_FailingCheckFailsReport(t *testing.T) {
	runner := NewRunner(".", fakeExecutor{results: map[string]fakeExecution{
		"go build ./...": {output: ""},
		"go test ./...":  {output: "FAIL\n", err: errors.New("exit status 1")},
		"go vet ./...":   {output: ""},
		"go fmt ./...":   {output: ""},
	}}, DefaultChecks())

	report := runner.Run(context.Background())
	if report.Verdict != VerdictFail {
		t.Fatalf("expected FAIL, got %s", report.Verdict)
	}
	if report.Checks[1].Result != VerdictFail {
		t.Fatalf("expected go test check fail, got %s", report.Checks[1].Result)
	}
}

func TestAnnotateEmptyCommandOutput_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	got := annotateEmptyCommandOutput(ctx, "", errors.New("signal: killed"))
	if !strings.Contains(got, "timed out") {
		t.Fatalf("empty timeout output must be annotated, got %q", got)
	}
	kept := annotateEmptyCommandOutput(ctx, "FAIL: hung wait\n", errors.New("exit status 1"))
	if kept != "FAIL: hung wait\n" {
		t.Fatalf("real output must be kept, got %q", kept)
	}
	cancelCtx, cancel2 := context.WithCancel(context.Background())
	cancel2()
	got = annotateEmptyCommandOutput(cancelCtx, "", errors.New("killed"))
	if !strings.Contains(got, "canceled") {
		t.Fatalf("empty cancel output must be annotated, got %q", got)
	}
}

func TestLooksLikeLanguageTestCommand(t *testing.T) {
	yes := [][]string{
		{"go", "test", "./..."},
		{"pytest", "-q"},
		{"cargo", "test"},
		{"npm", "test", "--", "--watchAll=false"},
	}
	for _, cmd := range yes {
		if !looksLikeLanguageTestCommand(cmd) {
			t.Fatalf("expected test command: %v", cmd)
		}
	}
	if looksLikeLanguageTestCommand([]string{"go", "build", "./..."}) {
		t.Fatal("go build must not look like a test suite")
	}
}
