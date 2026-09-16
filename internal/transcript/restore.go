package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/events"
)

const restoreRecentEventLimit = 12

type RestoreSnapshot struct {
	SourceTranscript    string
	SessionID           string
	EventCount          int
	BoundaryEventID     string
	BoundaryType        string
	RunID               string
	TaskID              string
	Status              string
	Summary             string
	PausePointID        string
	PausePointKind      string
	ResumeAttemptID     string
	ResumeAttemptStatus string
	ResumePolicyReason  string
	ResumeRetryable     string
	TaskSummary         string
	AvatarSummaries     map[string]string
	RecentEvents        []string
}

func Restore(path string) (RestoreSnapshot, error) {
	// A 0-byte transcript file is a known transient state: a new
	// session was created, the file was opened in O_CREATE mode,
	// and no event has been Appended yet (or the writer was
	// interrupted before any fsync). Treat this as a normal empty
	// resume state — not an error. The runtime may then proceed
	// with a fresh start instead of erroring out on restore.
	//
	// Before returning the empty snapshot, check the session directory
	// for other transcript files belonging to the same session. If any
	// exist with > 0 bytes, the 0-byte file may be a symptom of flush
	// loss rather than a genuine empty session — the caller should
	// prefer those alternatives for resume.
	if info, statErr := os.Stat(filepath.Clean(path)); statErr == nil {
		if info.Size() == 0 {
			sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if altPath := findNonEmptySessionFile(filepath.Dir(path), sessionID, path); altPath != "" {
				return RestoreSnapshot{}, fmt.Errorf("transcript file %s is 0 bytes but session directory has a non-empty transcript %s — possible flush loss; pass the other file explicitly with --resume", path, altPath)
			}
			return RestoreSnapshot{
				SourceTranscript: path,
				SessionID:        sessionID,
				AvatarSummaries:  map[string]string{},
			}, nil
		}
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return RestoreSnapshot{}, err
	}
	defer func() {
		_ = file.Close()
	}()

	decoder := json.NewDecoder(file)
	snapshot := RestoreSnapshot{
		SourceTranscript: path,
		SessionID:        strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		AvatarSummaries:  map[string]string{},
	}
	var fallback RestoreSnapshot

	for {
		var envelope events.Envelope
		if err := decoder.Decode(&envelope); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return RestoreSnapshot{}, err
		}
		snapshot.EventCount++
		if summary := summarizeRestoreEvent(envelope); summary != "" {
			snapshot.RecentEvents = append(snapshot.RecentEvents, summary)
			if len(snapshot.RecentEvents) > restoreRecentEventLimit {
				snapshot.RecentEvents = append([]string(nil), snapshot.RecentEvents[len(snapshot.RecentEvents)-restoreRecentEventLimit:]...)
			}
		}
		snapshot.captureRuntimeCoordinates(envelope.Payload)
		// Track latest run/task for soft-boundary fallback (#13).
		if envelope.RunID != "" {
			snapshot.RunID = envelope.RunID
		}
		if envelope.TaskID != "" {
			snapshot.TaskID = envelope.TaskID
		}

		switch envelope.Type {
		case "memory.summary_updated":
			switch payloadString(envelope.Payload, "scope", "") {
			case "task":
				snapshot.TaskSummary = payloadString(envelope.Payload, "summary", snapshot.TaskSummary)
			case "avatar":
				if envelope.AvatarID != "" {
					snapshot.AvatarSummaries[envelope.AvatarID] = payloadString(envelope.Payload, "summary", snapshot.AvatarSummaries[envelope.AvatarID])
				}
			}
		case "memory.compaction_boundary_written":
			snapshot.BoundaryEventID = envelope.EventID
			snapshot.BoundaryType = payloadString(envelope.Payload, "boundary_kind", "run_terminal")
			snapshot.RunID = envelope.RunID
			snapshot.TaskID = envelope.TaskID
			snapshot.Status = payloadString(envelope.Payload, "status", "completed")
			snapshot.Summary = payloadString(envelope.Payload, "summary", "")
		case "run.completed":
			fallback = RestoreSnapshot{
				SourceTranscript: path,
				SessionID:        snapshot.SessionID,
				BoundaryEventID:  envelope.EventID,
				BoundaryType:     "run_terminal_fallback",
				RunID:            envelope.RunID,
				TaskID:           envelope.TaskID,
				Status:           payloadString(envelope.Payload, "status", "completed"),
				Summary:          payloadString(envelope.Payload, "summary", ""),
			}
			copyRestoreCoordinates(&fallback, snapshot)
		case "run.lifecycle_updated":
			snapshot.BoundaryEventID = envelope.EventID
			snapshot.BoundaryType = payloadString(envelope.Payload, "boundary_kind", "run_lifecycle")
			snapshot.RunID = envelope.RunID
			snapshot.TaskID = envelope.TaskID
			snapshot.Status = payloadString(envelope.Payload, "status", snapshot.Status)
			snapshot.Summary = payloadString(envelope.Payload, "summary", snapshot.Summary)
		}
	}
	reverseStrings(snapshot.RecentEvents)

	if snapshot.EventCount == 0 {
		return RestoreSnapshot{}, errors.New("transcript is empty")
	}
	if snapshot.BoundaryEventID == "" {
		if fallback.BoundaryEventID == "" {
			// NL smoke #13: last-resort — use the most recent event as a soft
			// boundary so a failed ConfirmPlan session remains resumable.
			if soft := softBoundaryFromRecent(snapshot); soft.BoundaryEventID != "" {
				return soft, nil
			}
			return RestoreSnapshot{}, errors.New("no resumable boundary found in transcript")
		}
		fallback.EventCount = snapshot.EventCount
		fallback.TaskSummary = snapshot.TaskSummary
		fallback.AvatarSummaries = snapshot.AvatarSummaries
		fallback.RecentEvents = snapshot.RecentEvents
		return fallback, nil
	}
	return snapshot, nil
}

// softBoundaryFromRecent builds a minimal restore snapshot from the last
// known run/task IDs captured while scanning, so corrupted sessions without
// run.completed can still be resumed.
func softBoundaryFromRecent(snapshot RestoreSnapshot) RestoreSnapshot {
	if snapshot.RunID == "" && snapshot.TaskID == "" {
		return RestoreSnapshot{}
	}
	status := snapshot.Status
	if status == "" {
		status = "failed"
	}
	summary := snapshot.Summary
	if summary == "" {
		summary = "SOFT RESUME FALLBACK: no hard compaction/lifecycle boundary in transcript; continuing with recent context only"
	}
	return RestoreSnapshot{
		SourceTranscript: snapshot.SourceTranscript,
		SessionID:        snapshot.SessionID,
		EventCount:       snapshot.EventCount,
		BoundaryEventID:  "soft-boundary-" + snapshot.SessionID,
		BoundaryType:     "soft_resume_fallback",
		RunID:            snapshot.RunID,
		TaskID:           snapshot.TaskID,
		Status:           status,
		Summary:          summary,
		TaskSummary:      snapshot.TaskSummary,
		AvatarSummaries:  snapshot.AvatarSummaries,
		RecentEvents:     snapshot.RecentEvents,
		PausePointID:     snapshot.PausePointID,
		PausePointKind:   snapshot.PausePointKind,
	}
}

func (s *RestoreSnapshot) captureRuntimeCoordinates(payload map[string]any) {
	if s == nil {
		return
	}
	s.PausePointID = payloadString(payload, "pause_point_id", s.PausePointID)
	s.PausePointKind = payloadString(payload, "pause_point_kind", s.PausePointKind)
	attemptID := payloadString(payload, "resume_attempt_id", "")
	if attemptID != "" {
		s.ResumeAttemptID = attemptID
		s.ResumeAttemptStatus = payloadString(payload, "resume_attempt_status", s.ResumeAttemptStatus)
		if status := payloadString(payload, "status", ""); isResumeAttemptStatus(status) {
			s.ResumeAttemptStatus = status
		}
	}
	s.ResumePolicyReason = payloadString(payload, "resume_policy_reason", s.ResumePolicyReason)
	s.ResumePolicyReason = payloadString(payload, "reason", s.ResumePolicyReason)
	s.ResumeRetryable = payloadString(payload, "retryable", s.ResumeRetryable)
}

func copyRestoreCoordinates(target *RestoreSnapshot, source RestoreSnapshot) {
	if target == nil {
		return
	}
	target.PausePointID = source.PausePointID
	target.PausePointKind = source.PausePointKind
	target.ResumeAttemptID = source.ResumeAttemptID
	target.ResumeAttemptStatus = source.ResumeAttemptStatus
	target.ResumePolicyReason = source.ResumePolicyReason
	target.ResumeRetryable = source.ResumeRetryable
}

func isResumeAttemptStatus(value string) bool {
	for _, candidate := range []string{"started", "completed", "failed", "skipped"} {
		if value == candidate {
			return true
		}
	}
	return false
}

func payloadString(payload map[string]any, key string, fallback string) string {
	if payload == nil {
		return fallback
	}
	value, ok := payload[key]
	if !ok {
		return fallback
	}
	text, ok := value.(string)
	if !ok {
		return fallback
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func summarizeRestoreEvent(envelope events.Envelope) string {
	switch envelope.Type {
	case "task.decomposed":
		summary := payloadString(envelope.Payload, "summary", "")
		if summary == "" {
			return ""
		}
		return fmt.Sprintf("Planner decomposed the task: %s", summary)
	case "workflow.node_created":
		title := payloadString(envelope.Payload, "title", "")
		role := payloadString(envelope.Payload, "assigned_role", "")
		if title == "" {
			return ""
		}
		if role == "" {
			return fmt.Sprintf("Workflow node created: %s", title)
		}
		return fmt.Sprintf("Workflow node created for %s: %s", role, title)
	case "avatar.assigned":
		title := payloadString(envelope.Payload, "title", "")
		role := payloadString(envelope.Payload, "role", "")
		if title == "" {
			return ""
		}
		if role == "" {
			return fmt.Sprintf("Avatar assigned: %s", title)
		}
		return fmt.Sprintf("%s assigned: %s", role, title)
	case "avatar.handoff":
		summary := payloadString(envelope.Payload, "summary", "")
		if summary != "" {
			return summary
		}
		fromRole := payloadString(envelope.Payload, "from_role", "")
		fromTitle := payloadString(envelope.Payload, "from_title", "")
		toRole := payloadString(envelope.Payload, "to_role", "")
		toTitle := payloadString(envelope.Payload, "to_title", "")
		if fromRole == "" && toRole == "" && toTitle == "" {
			return ""
		}
		if fromRole == "" {
			if toRole == "" {
				return fmt.Sprintf("Avatar handoff prepared for %s.", toTitle)
			}
			return fmt.Sprintf("Work handed off to %s for %s.", toRole, toTitle)
		}
		if toRole == "" {
			if fromTitle == "" {
				return fmt.Sprintf("%s handed off completed work.", fromRole)
			}
			return fmt.Sprintf("%s handed off %s.", fromRole, fromTitle)
		}
		if fromTitle == "" {
			if toTitle == "" {
				return fmt.Sprintf("%s handed off work to %s.", fromRole, toRole)
			}
			return fmt.Sprintf("%s handed off work to %s for %s.", fromRole, toRole, toTitle)
		}
		if toTitle == "" {
			return fmt.Sprintf("%s handed off %s to %s.", fromRole, fromTitle, toRole)
		}
		return fmt.Sprintf("%s handed off %s to %s for %s.", fromRole, fromTitle, toRole, toTitle)
	case "avatar.spoke":
		messageType := payloadString(envelope.Payload, "message_type", "")
		if messageType != "report" && messageType != "ask" && messageType != "challenge" && messageType != "summarize" {
			return ""
		}
		summary := payloadString(envelope.Payload, "summary", "")
		if summary != "" {
			return summarizeAvatarMessageWithRouting(envelope.Payload, summary)
		}
		return summarizeAvatarMessageWithRouting(envelope.Payload, payloadString(envelope.Payload, "content", ""))
	case "tool.requested":
		toolName := payloadString(envelope.Payload, "tool", "")
		operation := payloadString(envelope.Payload, "operation", "")
		if toolName == "" {
			return ""
		}
		if operation == "" {
			return fmt.Sprintf("Tool requested: %s", toolName)
		}
		return fmt.Sprintf("Tool requested: %s (%s)", toolName, operation)
	case "tool.completed":
		return payloadString(envelope.Payload, "summary", "")
	case "tool.failed":
		toolName := payloadString(envelope.Payload, "tool", "")
		errText := payloadString(envelope.Payload, "error", "")
		if toolName == "" && errText == "" {
			return ""
		}
		if toolName == "" {
			return fmt.Sprintf("Tool failed: %s", errText)
		}
		if errText == "" {
			return fmt.Sprintf("Tool failed: %s", toolName)
		}
		return fmt.Sprintf("Tool failed: %s (%s)", toolName, errText)
	case "tool.denied":
		toolName := payloadString(envelope.Payload, "tool", "")
		errText := payloadString(envelope.Payload, "error", "")
		if toolName == "" && errText == "" {
			return ""
		}
		if toolName == "" {
			return fmt.Sprintf("Tool denied: %s", errText)
		}
		if errText == "" {
			return fmt.Sprintf("Tool denied: %s", toolName)
		}
		return fmt.Sprintf("Tool denied: %s (%s)", toolName, errText)
	case "tool.awaiting_approval":
		toolName := payloadString(envelope.Payload, "tool", "")
		errText := payloadString(envelope.Payload, "error", "")
		if toolName == "" && errText == "" {
			return ""
		}
		if toolName == "" {
			return fmt.Sprintf("Tool awaiting approval: %s", errText)
		}
		if errText == "" {
			return fmt.Sprintf("Tool awaiting approval: %s", toolName)
		}
		return fmt.Sprintf("Tool awaiting approval: %s (%s)", toolName, errText)
	case "memory.resume_restored":
		source := payloadString(envelope.Payload, "source_transcript", "")
		if source == "" {
			return ""
		}
		return fmt.Sprintf("Resume state restored from %s", source)
	case "mcp.completed":
		serverURL := payloadString(envelope.Payload, "server_url", "")
		method := payloadString(envelope.Payload, "method", "")
		if serverURL == "" && method == "" {
			return ""
		}
		parts := []string{"MCP call completed"}
		if method != "" {
			parts = append(parts, method)
		}
		if serverURL != "" {
			parts = append(parts, "at "+serverURL)
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func summarizeAvatarMessageWithRouting(payload map[string]any, summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	routeCue := avatarMessageRouteCue(payload)
	if routeCue == "" {
		return summary
	}
	markerIndex := strings.Index(summary, ": ")
	if markerIndex >= 0 {
		prefix := summary[:markerIndex+2]
		suffix := summary[markerIndex+2:]
		return prefix + "[" + routeCue + "] " + suffix
	}
	return summary + " [" + routeCue + "]"
}

func avatarMessageRouteCue(payload map[string]any) string {
	toRole := payloadString(payload, "to_role", "")
	if toRole != "" {
		return "to " + toRole
	}
	toAvatarID := payloadString(payload, "to_avatar_id", "")
	if toAvatarID != "" {
		return "to " + toAvatarID
	}
	broadcastScope := payloadString(payload, "broadcast_scope", "")
	if broadcastScope != "" {
		return "broadcast " + broadcastScope
	}
	return ""
}

// findNonEmptySessionFile looks in dir for JSONL files matching the session
// prefix that have > 0 bytes and are not the exclude path. Returns the path
// of the first matching file, or "" if none found.
func findNonEmptySessionFile(dir string, sessionID string, excludePath string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, sessionID) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		candidate := filepath.Join(dir, name)
		if candidate == excludePath {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Size() > 0 {
			return candidate
		}
	}
	return ""
}

func reverseStrings(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
