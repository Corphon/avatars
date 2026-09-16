package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/llm"
)

func TestWorkflowDocLLMTimeout_Floor(t *testing.T) {
	prev := llm.GlobalTimeoutConfig
	t.Cleanup(func() { llm.GlobalTimeoutConfig = prev })

	cfg := llm.DefaultTimeoutConfig()
	cfg.PlanAutoFill = 30 * time.Second
	llm.GlobalTimeoutConfig = &cfg
	if got := workflowDocLLMTimeout(); got != workflowDocLLMTimeoutFloor {
		t.Fatalf("floor: got %v want %v", got, workflowDocLLMTimeoutFloor)
	}

	cfg.PlanAutoFill = 5 * time.Minute
	llm.GlobalTimeoutConfig = &cfg
	if got := workflowDocLLMTimeout(); got != 5*time.Minute {
		t.Fatalf("honor larger PlanAutoFill: got %v", got)
	}
}

func TestDefaultPlanAutoFill_IsAtLeastFloor(t *testing.T) {
	cfg := llm.DefaultTimeoutConfig()
	if cfg.PlanAutoFill < workflowDocLLMTimeoutFloor {
		t.Fatalf("PlanAutoFill default %v < floor %v", cfg.PlanAutoFill, workflowDocLLMTimeoutFloor)
	}
}

type stubWorkflowLLM struct {
	lastReq llm.Request
	text    string
	err     error
}

func (s *stubWorkflowLLM) Provider() string             { return "stub" }
func (s *stubWorkflowLLM) Supports(llm.Capability) bool { return true }
func (s *stubWorkflowLLM) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	s.lastReq = req
	if s.err != nil {
		return llm.Response{}, s.err
	}
	return llm.Response{Text: s.text}, nil
}

func TestMakeWorkflowGenerate_ThinkModeOffAndEmptyFails(t *testing.T) {
	stub := &stubWorkflowLLM{text: ""}
	e := &Engine{llm: stub}
	gen := e.makeWorkflowGenerate(context.Background(), time.Second, "run", "task", "construct_plan")
	_, err := gen("sys", "usr")
	if err == nil || !strings.Contains(err.Error(), "empty content") {
		t.Fatalf("expected empty content error, got %v", err)
	}
	if stub.lastReq.ThinkMode != llm.ThinkModeOff {
		t.Fatalf("ThinkMode=%q want off", stub.lastReq.ThinkMode)
	}

	stub.text = "# Project Plan\n## Goals\nok\n## Phases\nok\n## Success Criteria\nok\n"
	out, err := gen("sys", "usr")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## Goals") {
		t.Fatalf("unexpected out=%q", out)
	}
}

func TestMakeWorkflowGenerate_PropagatesError(t *testing.T) {
	stub := &stubWorkflowLLM{err: errors.New("provider overloaded")}
	e := &Engine{llm: stub}
	gen := e.makeWorkflowGenerate(context.Background(), time.Second, "run", "task", "construct_plan")
	_, err := gen("sys", "usr")
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("got %v", err)
	}
}

func TestSuggestNextSteps_TemplatePlanNoPhaseAdvance(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "docs", "workflow")
	if err := os.MkdirAll(wf, 0755); err != nil {
		t.Fatal(err)
	}
	content := `# Project Plan
> **Project**: demo
> **Phase Count**: 2
> **Active Phase**: 1
## Goals
- Complete demo — what must be true when this is done]
## Phases
### Phase 1: [Name]
- **Goal**: [One-line goal]
- **Status**: pending
## Success Criteria
- [ ] [Measurable outcome]
`
	if err := os.WriteFile(filepath.Join(wf, "avatars_plan.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	steps := SuggestNextSteps(root, NextStepInput{
		Status:  "failed",
		Summary: "confirm plan: plan still has template placeholders ([Name]/[One-line goal])",
	})
	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "avatars_plan.md") && !strings.Contains(joined, "ConstructPlan") {
		t.Fatalf("expected construct/fill guidance, got %v", steps)
	}
	if strings.Contains(joined, "advance into Phase") {
		t.Fatalf("must not suggest phase advance while plan is template: %v", steps)
	}
}
