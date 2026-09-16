// Package runtime — session state persistence and recovery.
//
// P10: Adopts Claude Code's cross-run state machine pattern
// (claude_code_main/state/AppStateStore.ts + bootstrap/state.ts)
// to enable session resume, mode matching, and historical context.
//
// Key guarantees:
//   1. Session state is persisted to .avatars/session.json (file-based, not SQLite)
//   2. Each run appends a RunSummary to the session
//   3. Session can be inspected with `avatars session status`
//   4. Mode transitions are tracked across runs

package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionMode mirrors the PermissionMode but as a session-level concept.
type SessionMode string

const (
	SessionModeNormal  SessionMode = "normal"
	SessionModePlan    SessionMode = "plan"
	SessionModeExplore SessionMode = "explore"
)

// SessionState is the top-level session record persisted to disk.
// Mirrors Claude Code's AppStateStore session concept.
type SessionState struct {
	ID           string      `json:"id"`
	ProjectRoot  string      `json:"project_root"`
	Mode         SessionMode `json:"mode"`
	StartedAt    time.Time   `json:"started_at"`
	LastActiveAt time.Time   `json:"last_active_at"`
	Runs         []RunRecord `json:"runs"`
}

// RunRecord is a lightweight summary of one run within the session.
type RunRecord struct {
	RunID     string   `json:"run_id"`
	TaskID    string   `json:"task_id"`
	Input     string   `json:"input"`
	Status    string   `json:"status"` // completed / failed / needs_remediation / awaiting_clarification
	Summary   string   `json:"summary"`
	Artifacts []string `json:"artifacts,omitempty"`
	StartedAt string   `json:"started_at"`
	EndedAt   string   `json:"ended_at"`
}

// SessionManager manages session lifecycle: load, update, persist.
type SessionManager struct {
	mu      sync.RWMutex
	session *SessionState
	path    string
}

// NewSessionManager creates or loads the session for the given project root.
func NewSessionManager(projectRoot string) *SessionManager {
	path := filepath.Join(projectRoot, ".avatars", "session.json")
	sm := &SessionManager{path: path}
	sm.loadOrCreate(projectRoot)
	return sm
}

// loadOrCreate loads an existing session or creates a new one.
func (sm *SessionManager) loadOrCreate(projectRoot string) {
	data, err := os.ReadFile(sm.path)
	if err == nil {
		var session SessionState
		if json.Unmarshal(data, &session) == nil {
			sm.session = &session
			sm.session.LastActiveAt = time.Now().UTC()
			return
		}
	}
	// New session.
	sm.session = &SessionState{
		ID:          fmt.Sprintf("session-%s", time.Now().UTC().Format("20060102-150405")),
		ProjectRoot: projectRoot,
		Mode:        SessionModeNormal,
		StartedAt:   time.Now().UTC(),
	}
	sm.persist()
}

// Session returns the current session state (read-only copy).
func (sm *SessionManager) Session() SessionState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.session == nil {
		return SessionState{}
	}
	return *sm.session
}

// RecordRun appends a run to the session and persists.
func (sm *SessionManager) RecordRun(record RunRecord) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.session == nil {
		return
	}
	record.StartedAt = time.Now().UTC().Format(time.RFC3339)
	sm.session.LastActiveAt = time.Now().UTC()
	sm.session.Runs = append(sm.session.Runs, record)
	// Keep last 50 runs.
	if len(sm.session.Runs) > 50 {
		sm.session.Runs = sm.session.Runs[len(sm.session.Runs)-50:]
	}
	sm.persist()
}

// MarkRunComplete updates the last run's status and end time.
func (sm *SessionManager) MarkRunComplete(status, summary string, artifacts []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.session == nil || len(sm.session.Runs) == 0 {
		return
	}
	last := &sm.session.Runs[len(sm.session.Runs)-1]
	last.Status = status
	last.EndedAt = time.Now().UTC().Format(time.RFC3339)
	if summary != "" {
		last.Summary = summary
	}
	if len(artifacts) > 0 {
		last.Artifacts = artifacts
	}
	sm.session.LastActiveAt = time.Now().UTC()
	sm.persist()
}

// SetMode transitions the session to a new mode.
func (sm *SessionManager) SetMode(mode SessionMode) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.session == nil {
		return
	}
	sm.session.Mode = mode
	sm.session.LastActiveAt = time.Now().UTC()
	sm.persist()
}

// Mode returns the current session mode.
func (sm *SessionManager) Mode() SessionMode {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.session == nil {
		return SessionModeNormal
	}
	return sm.session.Mode
}

// RecentRuns returns the last N runs.
func (sm *SessionManager) RecentRuns(n int) []RunRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.session == nil {
		return nil
	}
	runs := sm.session.Runs
	if len(runs) <= n {
		out := make([]RunRecord, len(runs))
		copy(out, runs)
		return out
	}
	out := make([]RunRecord, n)
	copy(out, runs[len(runs)-n:])
	return out
}

// MostRecentTranscript returns the transcript path of the most recent
// completed run, or empty string if none.
func (sm *SessionManager) MostRecentTranscript() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.session == nil {
		return ""
	}
	for i := len(sm.session.Runs) - 1; i >= 0; i-- {
		if sm.session.Runs[i].Status == "completed" {
			// Transcript path is typically in .avatars/sessions/<run-id>.jsonl
			return filepath.Join(
				filepath.Dir(sm.path), "sessions",
				sm.session.Runs[i].RunID+".jsonl",
			)
		}
	}
	return ""
}

// persist writes the session to disk.
func (sm *SessionManager) persist() {
	if sm.session == nil {
		return
	}
	dir := filepath.Dir(sm.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, err := json.MarshalIndent(sm.session, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(sm.path, data, 0644)
}
