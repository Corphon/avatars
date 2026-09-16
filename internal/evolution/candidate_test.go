package evolution

import (
	"testing"

	memstore "avatars/internal/memory"
)

func TestBuildCandidates_PrefersVerifierAndWarmLessons(t *testing.T) {
	snapshot := memstore.Snapshot{
		Verification: &memstore.VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL"},
		WarmLessons:  []memstore.WarmLesson{{Kind: "verification", Summary: "Before marking this task complete, repeat the verifier path for patch and expect PARTIAL.", Confidence: "high"}},
	}

	candidates := BuildCandidates("Keep the planner decomposition stable before execution.", "Run complete.", snapshot, nil)
	if len(candidates) != 3 {
		t.Fatalf("expected 3 evolution candidates, got %d", len(candidates))
	}
	if candidates[0].Kind != "runtime_followup" {
		t.Fatalf("expected first candidate kind runtime_followup, got %q", candidates[0].Kind)
	}
	if candidates[1].Priority != "high" {
		t.Fatalf("expected warm-lesson candidate priority high, got %q", candidates[1].Priority)
	}
	if candidates[2].Kind != "task_pattern" {
		t.Fatalf("expected task pattern candidate, got %q", candidates[2].Kind)
	}
}

func TestBuildCandidates_UsesEvaluationRecordsAsExperienceSignals(t *testing.T) {
	snapshot := memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{
			{Tool: "patch", Kind: "verifier_verdict", Verdict: "PARTIAL", Cause: "verification_partial", Summary: "Verification finished with PARTIAL.", Source: "verification"},
			{Tool: "patch", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "verification_warning", Summary: "CGO is not enabled", Source: "verification_warning"},
		},
	}

	candidates := BuildCandidates("", "", snapshot, nil)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 evolution candidates from evaluation records, got %d", len(candidates))
	}
	if candidates[0].Kind != "runtime_followup" {
		t.Fatalf("expected first evaluation candidate kind runtime_followup, got %q", candidates[0].Kind)
	}
	if candidates[0].Source != "evaluation_record" {
		t.Fatalf("expected first evaluation candidate source evaluation_record, got %q", candidates[0].Source)
	}
	if candidates[1].Kind != "passive_feedback_followup" {
		t.Fatalf("expected second evaluation candidate kind passive_feedback_followup, got %q", candidates[1].Kind)
	}
	if candidates[1].Priority != "medium" {
		t.Fatalf("expected passive feedback priority medium, got %q", candidates[1].Priority)
	}
	if candidates[1].Summary != "Capture and address passive feedback for patch before repeating the verifier path: CGO is not enabled" {
		t.Fatalf("unexpected passive feedback candidate summary %q", candidates[1].Summary)
	}
}

func TestBuildCandidates_UsesVerificationCheckPassiveFeedback(t *testing.T) {
	snapshot := memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{{
			Tool:    "write",
			Kind:    "passive_feedback",
			Verdict: "FAIL",
			Cause:   "verification_check_failed",
			Summary: "Verifier check go test ./... reported FAIL for write: undefined: missingSymbol",
			Source:  "verification_check",
		}},
	}

	candidates := BuildCandidates("", "", snapshot, nil)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 evolution candidate from check passive feedback, got %d", len(candidates))
	}
	if candidates[0].Kind != "passive_feedback_followup" {
		t.Fatalf("expected passive_feedback_followup candidate, got %q", candidates[0].Kind)
	}
	if candidates[0].Priority != "high" {
		t.Fatalf("expected high-priority candidate for failed check feedback, got %q", candidates[0].Priority)
	}
	if candidates[0].Source != "evaluation_record" {
		t.Fatalf("expected evaluation_record source, got %q", candidates[0].Source)
	}
	if candidates[0].Summary != "Capture and address passive feedback for write before repeating the verifier path: Verifier check go test ./... reported FAIL for write: undefined: missingSymbol" {
		t.Fatalf("unexpected check passive feedback candidate summary %q", candidates[0].Summary)
	}
}

func TestBuildProjectLessons_PromotesHighPrioritySignals(t *testing.T) {
	snapshot := memstore.Snapshot{
		EvolutionCandidates: []memstore.EvolutionCandidate{{Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Priority: "high", Status: "open"}},
		WarmLessons:         []memstore.WarmLesson{{Kind: "verification", Summary: "Before marking this task complete, repeat the verifier path for patch and expect PARTIAL.", Confidence: "high"}},
	}

	lessons := BuildProjectLessons("task-42", snapshot)
	if len(lessons) != 2 {
		t.Fatalf("expected 2 project lessons, got %d", len(lessons))
	}
	if lessons[0].SourceTaskID != "task-42" {
		t.Fatalf("expected project lesson source task id task-42, got %q", lessons[0].SourceTaskID)
	}
	if lessons[0].Source != "evolution_candidate" {
		t.Fatalf("expected first promoted lesson source evolution_candidate, got %q", lessons[0].Source)
	}
	if lessons[1].Source != "warm_lesson" {
		t.Fatalf("expected second promoted lesson source warm_lesson, got %q", lessons[1].Source)
	}
}

func TestBuildCandidates_UsesSkillCandidateSignals(t *testing.T) {
	snapshot := memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{{
			Kind:   "skill_candidate",
			Source: "skillbuilder",
			Details: []string{
				"skill_name: Task Survey Skill",
				"skill_state: generated",
				"skill_path: skills/generated/task-survey-skill.md",
			},
		}},
	}

	candidates := BuildCandidates("", "", snapshot, nil)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 skill promotion candidate, got %d", len(candidates))
	}
	if candidates[0].Kind != "skill_promotion" {
		t.Fatalf("expected skill_promotion candidate, got %q", candidates[0].Kind)
	}
	if candidates[0].Priority != "high" {
		t.Fatalf("expected generated skill promotion priority high, got %q", candidates[0].Priority)
	}
	if candidates[0].Source != "skill_candidate" {
		t.Fatalf("expected skill candidate source, got %q", candidates[0].Source)
	}
}
