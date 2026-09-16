package approval

import "testing"

func TestParse_EmptyIsAcceptEdits(t *testing.T) {
	got, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if got != ModeAcceptEdits {
		t.Fatalf("got %q", got)
	}
}

func TestParse_Invalid(t *testing.T) {
	if _, err := Parse("yolo"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPolicy_DefaultAsksOnMutating(t *testing.T) {
	d, _, _ := Policy(ModeDefault, "write", false)
	if d != "ask" {
		t.Fatalf("mutating default want ask, got %s", d)
	}
	d, _, _ = Policy(ModeDefault, "read_file", true)
	if d != "allow" {
		t.Fatalf("readonly default want allow, got %s", d)
	}
}

func TestPolicy_PlanDeniesMutating(t *testing.T) {
	d, _, _ := Policy(ModePlan, "write", false)
	if d != "deny" {
		t.Fatalf("got %s", d)
	}
}

func TestContinuationMode_BumpsDefault(t *testing.T) {
	if ContinuationMode(ModeDefault) != ModeAcceptEdits {
		t.Fatal("approved continuation must not re-ask under default")
	}
	if ContinuationMode(ModePlan) != ModePlan {
		t.Fatal("plan mode must stay plan")
	}
}
