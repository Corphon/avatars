package runtime

import (
	"strings"
	"time"
)

type ResumeAttemptStatus string

const (
	ResumeAttemptStatusStarted   ResumeAttemptStatus = "started"
	ResumeAttemptStatusCompleted ResumeAttemptStatus = "completed"
	ResumeAttemptStatusFailed    ResumeAttemptStatus = "failed"
	ResumeAttemptStatusSkipped   ResumeAttemptStatus = "skipped"
)

type ResumeAttempt struct {
	ID                     string
	PausePointID           string
	Status                 ResumeAttemptStatus
	ContinuationRunID      string
	ContinuationTaskID     string
	ContinuationTranscript string
	Summary                string
	StartedAt              time.Time
	CompletedAt            time.Time
}

func NewResumeAttempt(pausePointID string, continuationRunID string, continuationTaskID string, startedAt time.Time) ResumeAttempt {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	pausePointID = strings.TrimSpace(pausePointID)
	continuationRunID = strings.TrimSpace(continuationRunID)
	return ResumeAttempt{
		ID:                 makeResumeAttemptID(pausePointID, continuationRunID),
		PausePointID:       pausePointID,
		Status:             ResumeAttemptStatusStarted,
		ContinuationRunID:  continuationRunID,
		ContinuationTaskID: strings.TrimSpace(continuationTaskID),
		StartedAt:          startedAt,
	}
}

func ResumeAttemptFromStatus(pausePointID string, continuationRunID string, continuationTaskID string, status string, transcript string, summary string, completedAt time.Time) ResumeAttempt {
	attempt := NewResumeAttempt(pausePointID, continuationRunID, continuationTaskID, completedAt)
	switch strings.TrimSpace(status) {
	case string(ResumeAttemptStatusCompleted):
		return attempt.Complete(transcript, summary, completedAt)
	case string(ResumeAttemptStatusFailed):
		return attempt.Fail(transcript, summary, completedAt)
	case string(ResumeAttemptStatusSkipped):
		return attempt.Skip(transcript, summary, completedAt)
	default:
		attempt.Summary = strings.TrimSpace(summary)
		return attempt
	}
}

func (a ResumeAttempt) Complete(transcript string, summary string, completedAt time.Time) ResumeAttempt {
	return a.finish(ResumeAttemptStatusCompleted, transcript, summary, completedAt)
}

func (a ResumeAttempt) Fail(transcript string, summary string, completedAt time.Time) ResumeAttempt {
	return a.finish(ResumeAttemptStatusFailed, transcript, summary, completedAt)
}

func (a ResumeAttempt) Skip(transcript string, summary string, completedAt time.Time) ResumeAttempt {
	return a.finish(ResumeAttemptStatusSkipped, transcript, summary, completedAt)
}

func (a ResumeAttempt) Retryable() bool {
	return normalizeResumeAttemptStatus(a.Status) == ResumeAttemptStatusFailed
}

func (a ResumeAttempt) Payload() map[string]any {
	payload := map[string]any{
		"resume_attempt_id":     strings.TrimSpace(a.ID),
		"resume_attempt_status": string(normalizeResumeAttemptStatus(a.Status)),
		"pause_point_id":        strings.TrimSpace(a.PausePointID),
		"continuation_run_id":   strings.TrimSpace(a.ContinuationRunID),
		"continuation_task_id":  strings.TrimSpace(a.ContinuationTaskID),
	}
	if transcript := strings.TrimSpace(a.ContinuationTranscript); transcript != "" {
		payload["continuation_transcript"] = transcript
	}
	if summary := strings.TrimSpace(a.Summary); summary != "" {
		payload["summary"] = summary
	}
	if !a.StartedAt.IsZero() {
		payload["started_at"] = a.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !a.CompletedAt.IsZero() {
		payload["completed_at"] = a.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	return payload
}

func (a ResumeAttempt) Record() ResumeAttemptRecord {
	return ResumeAttemptRecord{
		AttemptID:              strings.TrimSpace(a.ID),
		PausePointID:           strings.TrimSpace(a.PausePointID),
		Status:                 string(normalizeResumeAttemptStatus(a.Status)),
		ContinuationRunID:      strings.TrimSpace(a.ContinuationRunID),
		ContinuationTaskID:     strings.TrimSpace(a.ContinuationTaskID),
		ContinuationTranscript: strings.TrimSpace(a.ContinuationTranscript),
		Summary:                strings.TrimSpace(a.Summary),
		StartedAt:              a.StartedAt,
		CompletedAt:            a.CompletedAt,
	}
}

func (a ResumeAttempt) finish(status ResumeAttemptStatus, transcript string, summary string, completedAt time.Time) ResumeAttempt {
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	a.Status = normalizeResumeAttemptStatus(status)
	a.ContinuationTranscript = strings.TrimSpace(transcript)
	a.Summary = strings.TrimSpace(summary)
	a.CompletedAt = completedAt
	return a
}

func normalizeResumeAttemptStatus(status ResumeAttemptStatus) ResumeAttemptStatus {
	switch status {
	case ResumeAttemptStatusCompleted, ResumeAttemptStatusFailed, ResumeAttemptStatusSkipped:
		return status
	default:
		return ResumeAttemptStatusStarted
	}
}
