// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package runtime

import (
	"strings"
	"testing"

	memstore "avatars/internal/memory"
)

func TestDeriveWarmLesson_UsesToolRuntimePassiveFeedbackWhenVerifierAbsent(t *testing.T) {
	lesson, ok := deriveWarmLesson("session-a", "run-a", "task-a", "", "", memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{
			{Tool: "write", Kind: "passive_feedback", Verdict: "FAIL", Cause: "tool_execution_failed", Summary: "Tool write/file_write failed: path escapes sandbox root", Source: "tool_runtime"},
		},
	})
	if !ok {
		t.Fatal("expected tool runtime passive feedback to produce a warm lesson")
	}
	if lesson.Kind != "tool_runtime" {
		t.Fatalf("expected tool_runtime warm lesson kind, got %q", lesson.Kind)
	}
	if lesson.Source != "tool_runtime" {
		t.Fatalf("expected tool_runtime warm lesson source, got %q", lesson.Source)
	}
	if lesson.Confidence != "high" {
		t.Fatalf("expected high confidence warm lesson, got %q", lesson.Confidence)
	}
	if !strings.Contains(lesson.Summary, "Before repeating write") {
		t.Fatalf("expected warm lesson summary to mention the tool, got %q", lesson.Summary)
	}
	if !strings.Contains(lesson.Summary, "path escapes sandbox root") {
		t.Fatalf("expected warm lesson summary to include runtime failure detail, got %q", lesson.Summary)
	}
}

func TestDeriveWarmLesson_PrefersVerifierOverToolRuntimeFeedback(t *testing.T) {
	lesson, ok := deriveWarmLesson("session-a", "run-a", "task-a", "", "", memstore.Snapshot{
		Verification: &memstore.VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL"},
		EvaluationRecords: []memstore.EvaluationRecord{
			{Tool: "write", Kind: "passive_feedback", Verdict: "FAIL", Cause: "tool_execution_failed", Summary: "Tool write/file_write failed: path escapes sandbox root", Source: "tool_runtime"},
		},
	})
	if !ok {
		t.Fatal("expected verifier snapshot to produce a warm lesson")
	}
	if lesson.Kind != "verification" {
		t.Fatalf("expected verification warm lesson kind, got %q", lesson.Kind)
	}
	if !strings.Contains(lesson.Summary, "repeat the verifier path for patch") {
		t.Fatalf("expected verifier warm lesson summary, got %q", lesson.Summary)
	}
}
