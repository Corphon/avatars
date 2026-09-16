package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/planner"
	"avatars/internal/prompt"
	"avatars/internal/skills"
)

func TestC61_PromptCostFields_PerRoleBytes(t *testing.T) {
	sys := "system-hello"
	usr := "user-world!!"
	got := promptCostFields("Builder", sys, usr)
	if got[promptCostRoleKey] != "Builder" || got["role"] != "Builder" {
		t.Fatalf("role fields: %v", got)
	}
	if got[promptCostSystemBytesKey] != len(sys) {
		t.Fatalf("system bytes=%v want %d", got[promptCostSystemBytesKey], len(sys))
	}
	if got[promptCostUserBytesKey] != len(usr) {
		t.Fatalf("user bytes=%v want %d", got[promptCostUserBytesKey], len(usr))
	}
	if got[promptCostTotalBytesKey] != len(sys)+len(usr) {
		t.Fatalf("total=%v", got[promptCostTotalBytesKey])
	}
}

func TestC61_SynthesizerAlwaysOnNotDualInjected(t *testing.T) {
	// F77: compact always-on in the bundle must not be followed by a second
	// full "## Always-On Skills" dump on the Synthesizer path.
	defs := []skills.Definition{{
		Listing: skills.Listing{Name: "safety-core", AlwaysOn: true},
		Body:    "Never leak secrets. Repeat this rule so it is measurable.",
	}}
	bundle := prompt.Build("summarize the run").WithAlwaysOnSkills(alwaysOnPromptSkills(defs))
	roleBody := "You are the synthesizer."
	req := buildLLMSummaryRequest(bundle, planner.Build("summarize"), "read ok", "", "", "", false, nil, PersonalityConfig{}, roleBody)

	report := skillDualInjectReport(req.SystemPrompt)
	if report[SkillDualInjectPayloadKey] == true {
		t.Fatalf("F77: Synthesizer must not dual-inject compact+full always-on: %v\n--- sys ---\n%s", report, req.SystemPrompt)
	}
	compact := report[skillAlwaysOnCompactCountKey].(int)
	full := report[skillAlwaysOnFullCountKey].(int)
	if compact+full < 1 {
		t.Fatalf("expected at least one always-on inject: %v", report)
	}
	if compact > 0 && full > 0 {
		t.Fatalf("still dual-injected: %v", report)
	}
	cost := promptCostFields("Synthesizer", req.SystemPrompt, req.UserPrompt)
	if cost[promptCostTotalBytesKey].(int) < len(req.SystemPrompt) {
		t.Fatalf("total bytes should include system prompt: %v", cost)
	}
}

func TestC61_BuilderCodeGen_SingleAlwaysOnInject(t *testing.T) {
	defs := []skills.Definition{{
		Listing: skills.Listing{Name: "builder-core", AlwaysOn: true, Role: "builder"},
		Body:    "Prefer surgical edits.",
	}}
	sys := appendAlwaysOnRoleSkills(builderCodeGenSystemPrompt(), defs, "builder")
	report := skillDualInjectReport(sys)
	if report[SkillDualInjectPayloadKey] != false {
		t.Fatalf("Builder codegen sysPrompt should not mix compact+full always-on: %v\n%s", report, sys)
	}
	if report[skillAlwaysOnFullCountKey].(int) != 1 {
		t.Fatalf("Builder should still inject full always-on once: %v", report)
	}
}

func TestC61_BuilderCodeGenEmitsPromptBytes(t *testing.T) {
	store := events.NewStore()
	stub := &stubWorkflowLLM{err: errors.New("401 unauthorized")}
	e := &Engine{llm: stub, store: store, sessionID: "c61"}
	work := workflowNodeWorkContext{
		runID:           "run-c61",
		taskID:          "task-c61",
		builderAvatarID: "avatar-builder",
		nodeID:          "node-build",
		nodeRole:        "Builder",
		input:           "add hello.go with a Hello function",
		alwaysOnSkills: []skills.Definition{{
			Listing: skills.Listing{Name: "builder-core", AlwaysOn: true, Role: "builder"},
			Body:    "Prefer surgical edits.",
		}},
	}
	_, err := e.generateBuilderCodeSingle(context.Background(), work, work.input)
	if err == nil {
		t.Fatal("expected stub LLM error")
	}
	started := findEvent(store, "llm.started")
	if started == nil {
		t.Fatal("expected llm.started before Builder Generate")
	}
	if started.Payload[promptCostRoleKey] != "Builder" {
		t.Fatalf("role=%v payload=%v", started.Payload[promptCostRoleKey], started.Payload)
	}
	sysN, _ := started.Payload[promptCostSystemBytesKey].(int)
	usrN, _ := started.Payload[promptCostUserBytesKey].(int)
	total, _ := started.Payload[promptCostTotalBytesKey].(int)
	if sysN <= 0 || usrN <= 0 || total != sysN+usrN {
		t.Fatalf("prompt bytes missing: sys=%d user=%d total=%d payload=%v", sysN, usrN, total, started.Payload)
	}
	if started.Payload[SkillDualInjectPayloadKey] != false {
		t.Fatalf("Builder codegen should not report compact+full dual-inject: %v", started.Payload)
	}
	failed := findEvent(store, "llm.failed")
	if failed == nil {
		t.Fatal("expected llm.failed with the same byte fields")
	}
	if failed.Payload[promptCostTotalBytesKey] != total {
		t.Fatalf("llm.failed total=%v want %d", failed.Payload[promptCostTotalBytesKey], total)
	}
}

func TestC61_MakeWorkflowGenerate_EmitsPromptBytes(t *testing.T) {
	store := events.NewStore()
	stub := &stubWorkflowLLM{text: "# Project Plan\n## Goals\nok\n## Phases\nok\n## Success Criteria\nok\n"}
	e := &Engine{llm: stub, store: store, sessionID: "c61-wf"}
	gen := e.makeWorkflowGenerate(context.Background(), time.Second, "run", "task", "construct_plan")
	sys, usr := "planner-system-prompt", "planner-user-prompt"
	if _, err := gen(sys, usr); err != nil {
		t.Fatal(err)
	}
	started := findEvent(store, "workflow.llm.started")
	if started == nil {
		t.Fatal("expected workflow.llm.started")
	}
	if started.Payload[promptCostRoleKey] != "Planner" {
		t.Fatalf("role=%v", started.Payload[promptCostRoleKey])
	}
	if started.Payload[promptCostSystemBytesKey] != len(sys) || started.Payload[promptCostUserBytesKey] != len(usr) {
		t.Fatalf("bytes payload=%v", started.Payload)
	}
}

func TestC61_NarrateShowsPromptBytes(t *testing.T) {
	got := NarrateRunProgress(events.Envelope{
		Type: "llm.started",
		Payload: map[string]any{
			"provider":           "deepseek",
			"role":               "Builder",
			"prompt_bytes_total": 4096,
		},
	})
	if !strings.Contains(got, "Builder") || !strings.Contains(got, "deepseek") || !strings.Contains(got, "4096") {
		t.Fatalf("narrate=%q", got)
	}
}

func TestC62_DirectDoesNotSwallowPrimaryFailureWithoutFallbacks(t *testing.T) {
	prev := append([]string(nil), llm.GlobalFallbackProviders...)
	t.Cleanup(func() { llm.SetFallbackProviders(prev) })
	llm.SetFallbackProviders(nil)

	store := events.NewStore()
	inner := &stubWorkflowLLM{err: errors.New("401 unauthorized")}
	e := &Engine{
		llm:       llm.NewRetryClientWithFallbacks(inner, nil),
		store:     store,
		sessionID: "c62-direct",
	}
	_, err := e.executeDirectNodeWork(context.Background(), workflowNodeWorkContext{
		runID:    "run-c62",
		taskID:   "task-c62",
		nodeID:   "node-direct",
		nodeRole: "Direct",
		input:    "write a Hello function in hello.go",
	})
	if err == nil {
		t.Fatal("Direct must surface primary failure when fallback_providers is empty")
	}
	if !llm.IsPrimaryFailureWithoutFallbacks(err) {
		t.Fatalf("got %v", err)
	}
	started := findEvent(store, "llm.started")
	if started == nil || started.Payload[promptCostRoleKey] != "Direct" {
		t.Fatalf("expected Direct llm.started with prompt bytes, got %#v", started)
	}
	failed := findEvent(store, "llm.failed")
	if failed == nil {
		t.Fatal("expected llm.failed, not a silent direct.completed")
	}
}

func findEvent(store *events.Store, typ string) *events.Envelope {
	for _, env := range store.History() {
		if env.Type == typ {
			e := env
			return &e
		}
	}
	return nil
}
