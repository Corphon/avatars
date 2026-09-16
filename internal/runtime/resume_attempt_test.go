package runtime

import (
	"testing"
	"time"
)

func TestResumeAttemptStartsWithStableIdentity(t *testing.T) {
	startedAt := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	attempt := NewResumeAttempt("pause-1", "run-continued", "task-1", startedAt)
	again := NewResumeAttempt("pause-1", "run-continued", "task-1", startedAt)

	if attempt.ID == "" {
		t.Fatalf("expected resume attempt id, got %+v", attempt)
	}
	if attempt.ID != again.ID {
		t.Fatalf("expected stable resume attempt id, got %s and %s", attempt.ID, again.ID)
	}
	if attempt.Status != ResumeAttemptStatusStarted {
		t.Fatalf("expected started status, got %q", attempt.Status)
	}
	if attempt.Retryable() {
		t.Fatal("started attempt should not be retryable")
	}
}

func TestResumeAttemptCompletes(t *testing.T) {
	completedAt := time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC)
	attempt := NewResumeAttempt("pause-1", "run-continued", "task-1", time.Time{}).Complete("sessions/continued.jsonl", "Continuation completed.", completedAt)

	if attempt.Status != ResumeAttemptStatusCompleted {
		t.Fatalf("expected completed status, got %q", attempt.Status)
	}
	if attempt.Retryable() {
		t.Fatal("completed attempt should not be retryable")
	}
	payload := attempt.Payload()
	assertPayloadValue(t, payload, "resume_attempt_status", "completed")
	assertPayloadValue(t, payload, "continuation_transcript", "sessions/continued.jsonl")
	assertPayloadValue(t, payload, "summary", "Continuation completed.")
}

func TestResumeAttemptFailedIsRetryable(t *testing.T) {
	attempt := NewResumeAttempt("pause-1", "run-continued", "task-1", time.Time{}).Fail("", "Continuation failed.", time.Time{})

	if attempt.Status != ResumeAttemptStatusFailed {
		t.Fatalf("expected failed status, got %q", attempt.Status)
	}
	if !attempt.Retryable() {
		t.Fatal("failed attempt should remain retryable")
	}
}

func TestResumeAttemptSkipped(t *testing.T) {
	attempt := NewResumeAttempt("pause-1", "run-continued", "task-1", time.Time{}).Skip("sessions/continued.jsonl", "Replay skipped.", time.Time{})

	if attempt.Status != ResumeAttemptStatusSkipped {
		t.Fatalf("expected skipped status, got %q", attempt.Status)
	}
	record := attempt.Record()
	if record.Status != "skipped" || record.PausePointID != "pause-1" || record.AttemptID == "" {
		t.Fatalf("expected skipped record with pause point and attempt id, got %+v", record)
	}
}
