package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	memstore "avatars/internal/memory"
	"avatars/internal/transcript"
)

type ResumeResult struct {
	TranscriptPath string
	Restored       transcript.RestoreSnapshot
	Structured     memstore.Snapshot
	MemoryPath     string
}

func (e *Engine) resumeTranscript(ctx context.Context, transcriptPath string) (ResumeResult, error) {
	restored, err := transcript.Restore(transcriptPath)
	if err != nil {
		return ResumeResult{}, err
	}
	structured, err := e.mergeStoredHotMemory(&restored)
	if err != nil {
		return ResumeResult{}, err
	}

	runID := fmt.Sprintf("%s-run-%d", e.sessionID, time.Now().UTC().UnixNano())
	taskID := runID + "-task-resume"
	if e.taskID != "" {
		taskID = e.taskID
	}
	taskTitle := fmt.Sprintf("Resume %s", filepath.Base(transcriptPath))

	runStartedPayload := map[string]any{"mode": "resume", "transcript_path": transcriptPath}
	if e.taskID != "" {
		runStartedPayload["task_workspace_id"] = e.taskID
		if e.taskRoot != "" {
			runStartedPayload["task_workspace_root"] = e.taskRoot
		}
	}
	if err := e.emit(runID, taskID, "", "planning", "run.started", "runtime", runStartedPayload, nil); err != nil {
		return ResumeResult{}, err
	}
	if err := e.recordLLMStatus(runID, taskID); err != nil {
		return ResumeResult{}, err
	}
	if err := e.emit(runID, taskID, "", "planning", "task.created", "runtime", map[string]any{"title": taskTitle}, nil); err != nil {
		return ResumeResult{}, err
	}
	if err := e.emit(runID, taskID, "", "reviewing", "memory.resume_restored", "memory", map[string]any{
		"source_transcript":         transcriptPath,
		"restored_run_id":           restored.RunID,
		"restored_task_id":          restored.TaskID,
		"boundary_type":             restored.BoundaryType,
		"boundary_event_id":         restored.BoundaryEventID,
		"status":                    restored.Status,
		"summary":                   restored.Summary,
		"pause_point_id":            restored.PausePointID,
		"pause_point_kind":          restored.PausePointKind,
		"resume_attempt_id":         restored.ResumeAttemptID,
		"resume_attempt_status":     restored.ResumeAttemptStatus,
		"resume_policy_reason":      restored.ResumePolicyReason,
		"resume_retryable":          restored.ResumeRetryable,
		"task_summary":              restored.TaskSummary,
		"avatar_summary_count":      len(restored.AvatarSummaries),
		"recent_event_count":        len(restored.RecentEvents),
		"warm_lesson_count":         len(structured.WarmLessons),
		"evolution_candidate_count": len(structured.EvolutionCandidates),
		"memory_store":              structuredMemoryStoreLabel(e, structured),
		"event_count":               restored.EventCount,
	}, nil); err != nil {
		return ResumeResult{}, err
	}

	summary := fmt.Sprintf("Restored transcript state from %s. Latest stable summary: %s", transcriptPath, restored.Summary)
	if !structured.IsEmpty() && e != nil && e.memory != nil {
		summary += fmt.Sprintf(" Structured hot memory loaded from %s.", e.memory.Path())
	}
	if err := e.emit(runID, taskID, "", "reviewing", "run.completed", "runtime", map[string]any{"status": "completed", "summary": summary}, nil); err != nil {
		return ResumeResult{}, err
	}
	if err := e.emitStableBoundary(runID, taskID, "", "completed", summary, "resume_restore", map[string]any{"source_transcript": transcriptPath, "restored_run_id": restored.RunID, "restored_task_id": restored.TaskID}); err != nil {
		return ResumeResult{}, err
	}
	if err := e.transcript.Flush(); err != nil {
		return ResumeResult{}, err
	}

	result := ResumeResult{TranscriptPath: e.transcript.Path(), Restored: restored, Structured: structured}
	if e != nil && e.memory != nil {
		result.MemoryPath = e.memory.Path()
	}
	return result, nil
}

func structuredMemoryStoreLabel(e *Engine, snapshot memstore.Snapshot) string {
	if e == nil || e.memory == nil || snapshot.IsEmpty() {
		return ""
	}
	return "sqlite"
}
