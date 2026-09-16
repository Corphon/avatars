package runtime

import (
	"bytes"
	"os"
	"testing"
)

func TestC2_MergePhaseBlock_DoesNotDowngrade(t *testing.T) {
	if got := mergePhaseBlock("build_failed", "checklist_incomplete"); got != "build_failed" {
		t.Fatalf("build_failed must not lose to checklist, got %s", got)
	}
	if got := mergePhaseBlock("user_phase_lock", "checklist_incomplete"); got != "user_phase_lock" {
		t.Fatalf("user_phase_lock must not lose to checklist, got %s", got)
	}
	if got := mergePhaseBlock("checklist_incomplete", "user_phase_lock"); got != "user_phase_lock" {
		t.Fatalf("lock must win over checklist, got %s", got)
	}
	if got := mergePhaseBlock("checklist_incomplete", "build_failed"); got != "build_failed" {
		t.Fatalf("red health may upgrade checklist, got %s", got)
	}
	if got := mergePhaseBlock("plan_incomplete", "build_failed"); got != "plan_incomplete" {
		t.Fatalf("F60: plan_incomplete must not become build_failed, got %s", got)
	}
	if got := mergePhaseBlock("needs_remediation", "checklist_incomplete"); got != "needs_remediation" {
		t.Fatalf("F14: needs_remediation must not lose to checklist, got %s", got)
	}
}

func TestC2_Decide_PlanningGateBeatsBuildFailed(t *testing.T) {
	d := decidePhaseAdvance(phaseAdvanceInput{
		BuildOK: false, PhaseDocsOK: true, PlanningGate: true,
		Advance: func(string, int) (bool, int) { t.Fatal("must not advance"); return false, 0 },
	})
	if d.Blocked != "plan_incomplete" || d.Advanced {
		t.Fatalf("%+v", d)
	}
}

func TestC2_Decide_LockNotChecklist(t *testing.T) {
	calls := 0
	d := decidePhaseAdvance(phaseAdvanceInput{
		BuildOK: true, PhaseDocsOK: true, LockedPhase: 1,
		Advance: func(string, int) (bool, int) { calls++; return false, 0 },
	})
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if d.Blocked != "user_phase_lock" {
		t.Fatalf("got %q", d.Blocked)
	}
}

func TestC2_ResolvePhaseAdvance_OnceNoDoubleWrite(t *testing.T) {
	e := &Engine{}
	calls := 0
	in := phaseAdvanceInput{
		BuildOK: true, PhaseDocsOK: true,
		Advance: func(string, int) (bool, int) { calls++; return false, 0 },
	}
	d1 := e.resolvePhaseAdvance(in)
	if d1.Blocked != "checklist_incomplete" {
		t.Fatalf("first: %+v", d1)
	}
	in.BuildOK = false
	d2 := e.resolvePhaseAdvance(in)
	if calls != 1 {
		t.Fatalf("CriticDecide must run once, calls=%d", calls)
	}
	if d2.Blocked != "build_failed" {
		t.Fatalf("second call may upgrade to build_failed, got %q", d2.Blocked)
	}
	in.BuildOK = true
	d3 := e.resolvePhaseAdvance(in)
	if d3.Blocked != "build_failed" {
		t.Fatalf("must not downgrade back to checklist, got %q", d3.Blocked)
	}
	if calls != 1 {
		t.Fatalf("still once, calls=%d", calls)
	}
}

func TestC2_Decide_RunBlockedGreenIsNeedsRemediation(t *testing.T) {
	d := decidePhaseAdvance(phaseAdvanceInput{
		BuildOK: true, PhaseDocsOK: true, RunBlocked: true,
		Advance: func(string, int) (bool, int) { t.Fatal("must not advance"); return false, 0 },
	})
	if d.Blocked != "needs_remediation" || d.Advanced {
		t.Fatalf("%+v", d)
	}
}

func TestC2_Decide_PlanningAbortDoesNotCallAdvance(t *testing.T) {
	d := decidePhaseAdvance(phaseAdvanceInput{
		BuildOK: true, PhaseDocsOK: true, PlanningGate: true,
		Advance: func(string, int) (bool, int) { t.Fatal("must not advance"); return true, 2 },
	})
	if d.Blocked != "plan_incomplete" || d.Advanced {
		t.Fatalf("%+v", d)
	}
}

func TestC2_SingleProductionCriticDecideCallSite(t *testing.T) {
	needle := "workflow.CriticDecidePhaseAdvanceUpTo"
	for _, name := range []string{"loop.go", "loop_critic.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(needle)) {
			t.Fatalf("%s must not call %s; C2 authority is decidePhaseAdvance", name, needle)
		}
	}
	guard, err := os.ReadFile("phase_advance_guard.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(guard, []byte(needle)) {
		t.Fatalf("phase_advance_guard.go must be the single production call site for %s", needle)
	}
}

func TestF102_QuotaDoesNotBlockPhaseAdvanceWhenDiskGreen(t *testing.T) {
	r := RunResult{
		SynthesisStatus: `failed: openai-compatible provider "deepseek" returned 402: Insufficient Balance`,
		Summary:         "Multi-avatar run. Synthesis unavailable.",
		BuildOK:         true,
		BuildOKKnown:    true,
	}
	if runBlocksPhaseAdvance("completed", r, nil) {
		t.Fatal("F102: 402 on green disk must not hold phase advance")
	}
	if runBlocksPhaseAdvance("needs_remediation", r, nil) {
		t.Fatal("F102: 402 must not treat needs_remediation status as a compile hold")
	}
	r.BuildOK = false
	if !runBlocksPhaseAdvance("completed", r, nil) {
		t.Fatal("F102: real red disk must still block even if synthesis is 402")
	}
}
