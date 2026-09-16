package verification

import (
	"context"
	"testing"
)

type countingExecutor struct {
	n    *int
	out  string
	err  error
	last []string
}

func (c countingExecutor) Run(_ context.Context, _ string, command []string) (string, error) {
	*c.n++
	c.last = append([]string(nil), command...)
	return c.out, c.err
}

func TestS7B_HealthServiceMemoSkipsSecondExec(t *testing.T) {
	h := NewHealthService()
	n := 0
	fn := func() string {
		n++
		return "ok"
	}
	if got := h.Memo(".", "go-build", fn); got != "ok" {
		t.Fatalf("first memo=%q", got)
	}
	if got := h.Memo(".", "go-build", fn); got != "ok" {
		t.Fatalf("second memo=%q", got)
	}
	if n != 1 {
		t.Fatalf("expected one exec, got %d", n)
	}
	if got := h.Memo(".", "go-test", fn); got != "ok" {
		t.Fatalf("different kind should miss cache, got %q", got)
	}
	if n != 2 {
		t.Fatalf("expected second kind to exec, got %d", n)
	}
}

func TestS7B_HealthServiceExecSkipsSecondRun(t *testing.T) {
	h := NewHealthService()
	n := 0
	exec := countingExecutor{n: &n, out: "built"}
	out, err := h.Exec(context.Background(), exec, ".", []string{"go", "build", "./..."})
	if err != nil || out != "built" {
		t.Fatalf("first exec: out=%q err=%v", out, err)
	}
	out, err = h.Exec(context.Background(), exec, ".", []string{"go", "build", "./..."})
	if err != nil || out != "built" {
		t.Fatalf("second exec: out=%q err=%v", out, err)
	}
	if n != 1 {
		t.Fatalf("expected one executor.Run, got %d", n)
	}
}

func TestS7B_RunnerUsesHealthCache(t *testing.T) {
	n := 0
	exec := countingExecutor{n: &n, out: ""}
	h := NewHealthService()
	r := NewRunner(".", exec, []CommandCheck{
		{Name: "go build", Command: []string{"go", "build", "./..."}, Expected: "ok"},
	}).WithCache(h)
	_ = r.Run(context.Background())
	_ = r.Run(context.Background())
	if n != 1 {
		t.Fatalf("expected cached runner exec, got %d", n)
	}
}

func TestHealthServiceResetClearsMemo(t *testing.T) {
	h := NewHealthService()
	n := 0
	fn := func() string {
		n++
		if n == 1 {
			return "stale"
		}
		return "fresh"
	}
	if got := h.Memo(".", "go-build", fn); got != "stale" {
		t.Fatalf("first=%q", got)
	}
	h.Reset()
	if got := h.Memo(".", "go-build", fn); got != "fresh" {
		t.Fatalf("after reset want fresh, got %q", got)
	}
	if n != 2 {
		t.Fatalf("reset must re-exec, n=%d", n)
	}
}
