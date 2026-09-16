package health

import (
	"context"
	"testing"

	"avatars/internal/verification"
)

type countingExec struct {
	n int
}

func (c *countingExec) Run(ctx context.Context, workingDir string, command []string) (string, error) {
	c.n++
	return "ok", nil
}

func TestNew_MemoizesSecondExec(t *testing.T) {
	s := New()
	if s.Cache() == nil {
		t.Fatal("Cache must expose verification.HealthService")
	}
	exec := &countingExec{}
	wd := t.TempDir()
	cmd := []string{"go", "test", "./..."}
	if _, err := s.Exec(context.Background(), exec, wd, cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(context.Background(), exec, wd, cmd); err != nil {
		t.Fatal(err)
	}
	if exec.n != 1 {
		t.Fatalf("expected 1 exec, got %d", exec.n)
	}
}

func TestCache_NilSafe(t *testing.T) {
	var s *Service
	if s.Cache() != nil {
		t.Fatal("nil Service.Cache must be nil")
	}
	_ = verification.HealthService{}
}
