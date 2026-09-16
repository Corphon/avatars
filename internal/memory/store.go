package memory

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"avatars/internal/llm"
	runtelemetry "avatars/internal/telemetry"

	_ "modernc.org/sqlite"
)

const (
	ScopeStable              = "stable"
	ScopeTask                = "task"
	ScopeAvatar              = "avatar"
	// S3.5: raise hot-event window so recent failures survive into the next prompt.
	hotEventLimit            = 48
	verificationHistoryLimit = 6
	warmLessonLimit          = 12
	evolutionCandidateLimit  = 6
	projectLessonLimit       = 12
	evaluationRecordLimit    = 16
)

type SummaryRecord struct {
	SessionID    string
	RunID        string
	TaskID       string
	AvatarID     string
	Scope        string
	Role         string
	Status       string
	BoundaryKind string
	Summary      string
	UpdatedAt    time.Time
}

type EventRecord struct {
	SessionID string
	RunID     string
	TaskID    string
	AvatarID  string
	EventID   string
	EventType string
	Phase     string
	Source    string
	Summary   string
	EmittedAt time.Time
}

type VerificationRecord struct {
	SessionID  string
	RunID      string
	TaskID     string
	AvatarID   string
	Tool       string
	Verdict    string
	Summary    string
	ReportPath string
	Warnings   []string
	Checks     []string
	UpdatedAt  time.Time
}

type VerificationSnapshot struct {
	Tool            string
	Verdict         string
	Summary         string
	ReportPath      string
	Warnings        []string
	Checks          []string
	FollowUpCommand string
	UpdatedAt       time.Time
}

func verificationFollowUpCommand(snapshot VerificationSnapshot) string {
	if IsNonPassVerificationVerdict(snapshot.Verdict) {
		return "avatars verify"
	}
	return ""
}

func TaskVerificationFollowUpCommand(taskID string, snapshot VerificationSnapshot) string {
	if !IsNonPassVerificationVerdict(snapshot.Verdict) {
		return ""
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	if trimmedTaskID == "" {
		return verificationFollowUpCommand(snapshot)
	}
	return fmt.Sprintf("avatars verify --task %s", trimmedTaskID)
}

func VerificationReverifyStatus(current *VerificationSnapshot, history []VerificationSnapshot) string {
	latestNonPass := LatestNonPassVerificationSnapshot(current, history)
	if latestNonPass == nil {
		return ""
	}
	if current == nil || IsNonPassVerificationVerdict(current.Verdict) || sameVerificationSnapshot(*current, *latestNonPass) {
		return "pending reverify for latest non-pass verification"
	}
	verdict := strings.TrimSpace(strings.ToUpper(current.Verdict))
	if verdict == "PASS" {
		return "latest non-pass verification is covered by a later PASS"
	}
	if verdict == "" {
		return "pending reverify for latest non-pass verification"
	}
	return fmt.Sprintf("latest non-pass verification is followed by a later %s", verdict)
}

func sameVerificationSnapshot(left VerificationSnapshot, right VerificationSnapshot) bool {
	if !left.UpdatedAt.IsZero() && !right.UpdatedAt.IsZero() && left.UpdatedAt.Equal(right.UpdatedAt) {
		return strings.TrimSpace(left.Tool) == strings.TrimSpace(right.Tool) && strings.TrimSpace(left.Verdict) == strings.TrimSpace(right.Verdict) && strings.TrimSpace(left.ReportPath) == strings.TrimSpace(right.ReportPath)
	}
	return strings.TrimSpace(left.Tool) == strings.TrimSpace(right.Tool) && strings.TrimSpace(left.Verdict) == strings.TrimSpace(right.Verdict) && strings.TrimSpace(left.Summary) == strings.TrimSpace(right.Summary) && strings.TrimSpace(left.ReportPath) == strings.TrimSpace(right.ReportPath)
}

func IsNonPassVerificationVerdict(verdict string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(verdict))
	return trimmed == "FAIL" || trimmed == "PARTIAL"
}

func normalizeVerificationSnapshot(snapshot VerificationSnapshot) VerificationSnapshot {
	if strings.TrimSpace(snapshot.FollowUpCommand) == "" {
		snapshot.FollowUpCommand = verificationFollowUpCommand(snapshot)
	}
	return snapshot
}

func LatestNonPassVerificationSnapshot(current *VerificationSnapshot, history []VerificationSnapshot) *VerificationSnapshot {
	if current != nil && IsNonPassVerificationVerdict(current.Verdict) {
		cloned := normalizeVerificationSnapshot(*current)
		cloned.Warnings = append([]string(nil), current.Warnings...)
		cloned.Checks = append([]string(nil), current.Checks...)
		return &cloned
	}
	for _, entry := range history {
		if !IsNonPassVerificationVerdict(entry.Verdict) {
			continue
		}
		cloned := normalizeVerificationSnapshot(entry)
		cloned.Warnings = append([]string(nil), entry.Warnings...)
		cloned.Checks = append([]string(nil), entry.Checks...)
		return &cloned
	}
	return nil
}

type EvaluationRecord struct {
	SessionID  string
	RunID      string
	TaskID     string
	AvatarID   string
	Tool       string
	Kind       string
	Verdict    string
	Cause      string
	Summary    string
	Source     string
	ReportPath string
	Details    []string
	UpdatedAt  time.Time
}

func LatestReverifyClosureEvaluation(current *VerificationSnapshot, history []VerificationSnapshot, records []EvaluationRecord) *EvaluationRecord {
	if VerificationReverifyStatus(current, history) != "latest non-pass verification is covered by a later PASS" {
		return nil
	}
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "verification_reverify_covered" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestReverifyAttemptEvaluation(current *VerificationSnapshot, history []VerificationSnapshot, records []EvaluationRecord) *EvaluationRecord {
	if VerificationReverifyStatus(current, history) != "pending reverify for latest non-pass verification" {
		return nil
	}
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "verification_reverify_fix_attempt" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestCoveredReverifyAttemptEvaluation(current *VerificationSnapshot, history []VerificationSnapshot, records []EvaluationRecord) *EvaluationRecord {
	if VerificationReverifyStatus(current, history) != "latest non-pass verification is covered by a later PASS" {
		return nil
	}
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "verification_reverify_fix_attempt" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestVerifierRemediationPausePointEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "verifier_remediation_pause_point" {
			continue
		}
		if EvaluationRecordPausePointID(record) == "" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

func LatestVerifierRemediationResumeAttemptEvaluation(records []EvaluationRecord) *EvaluationRecord {
	for _, record := range records {
		if strings.TrimSpace(record.Cause) != "verifier_remediation_resume_attempt" {
			continue
		}
		if EvaluationRecordPausePointID(record) == "" || EvaluationRecordResumeAttemptID(record) == "" {
			continue
		}
		cloned := record
		cloned.Details = append([]string(nil), record.Details...)
		return &cloned
	}
	return nil
}

type WarmLessonRecord struct {
	SessionID  string
	RunID      string
	TaskID     string
	Kind       string
	Summary    string
	Source     string
	Confidence string
	UpdatedAt  time.Time
}

type WarmLesson struct {
	Kind       string
	Summary    string
	Source     string
	Confidence string
	UpdatedAt  time.Time
}

type EvolutionCandidateRecord struct {
	SessionID string
	RunID     string
	TaskID    string
	Kind      string
	Summary   string
	Source    string
	Priority  string
	Status    string
	UpdatedAt time.Time
}

type EvolutionCandidate struct {
	Kind      string
	Summary   string
	Source    string
	Priority  string
	Status    string
	UpdatedAt time.Time
}

type ProjectLessonRecord struct {
	SourceTaskID string
	Kind         string
	Summary      string
	Source       string
	Confidence   string
	SupportCount int
	SupportTasks []string
	UpdatedAt    time.Time
}

type ProjectLesson struct {
	SourceTaskID string
	Kind         string
	Summary      string
	Source       string
	Confidence   string
	SupportCount int
	SupportTasks []string
	UpdatedAt    time.Time
}

type TelemetryRecord struct {
	SessionID string
	RunID     string
	TaskID    string
	Snapshot  runtelemetry.Snapshot
}

type LLMStatusRecord struct {
	SessionID string
	RunID     string
	TaskID    string
	Status    llm.ProviderStatus
	Warning   string
	UpdatedAt time.Time
}

type Snapshot struct {
	StableSummary       string
	TaskSummary         string
	AvatarSummaries     map[string]string
	RecentEvents        []string
	LLMStatus           *llm.ProviderStatus
	LLMStatusWarning    string
	Telemetry           *runtelemetry.Snapshot
	Verification        *VerificationSnapshot
	VerificationHistory []VerificationSnapshot
	EvaluationRecords   []EvaluationRecord
	WarmLessons         []WarmLesson
	EvolutionCandidates []EvolutionCandidate
	ProjectLessons      []ProjectLesson
}

func (s Snapshot) IsEmpty() bool {
	return s.StableSummary == "" && s.TaskSummary == "" && len(s.AvatarSummaries) == 0 && len(s.RecentEvents) == 0 && s.LLMStatus == nil && s.LLMStatusWarning == "" && s.Telemetry == nil && s.Verification == nil && len(s.VerificationHistory) == 0 && len(s.EvaluationRecords) == 0 && len(s.WarmLessons) == 0 && len(s.EvolutionCandidates) == 0 && len(s.ProjectLessons) == 0
}

// SummaryCompressor is a callback that compresses a long summary into a concise
// version. The runtime injects an LLM-based compressor during bootstrap. PARTIAL-7.
type SummaryCompressor func(summary string, maxLen int) string

type Store struct {
	db         *sql.DB
	path       string
	compressor SummaryCompressor // P2-2a: LLM-based summary compression
}

type MemoryLifecycleStatus struct {
	Path                         string
	SizeBytes                    int64
	Tables                       []MemoryTableStatus
	PruningLimits                []MemoryPruningLimit
	StaleWarmLessons             int
	RetirableEvolutionCandidates int
	LowSupportProjectLessons     int
	ProtectedEvaluationRecords   int
	ProtectedVerificationHistory int
	RetirementCandidateTotal     int
	MaintenanceSupported         bool
	MaintenanceMode              string
}

type MemoryTableStatus struct {
	Name      string
	Rows      int
	OldestRaw string
	NewestRaw string
}

type MemoryPruningLimit struct {
	Name  string
	Limit int
}

type MemoryMaintenanceReport struct {
	Path                         string
	Mode                         string
	ApplySupported               bool
	PlannedAction                string
	StaleWarmLessons             int
	RetirableEvolutionCandidates int
	LowSupportProjectLessons     int
	RetirementCandidateTotal     int
	ReusableWarmLessonsKept      int
	ReusableEvolutionKept        int
	ReusableProjectLessonsKept   int
	ProtectedEvaluationRecords   int
	ProtectedVerificationHistory int
	ProtectedTruthSkipped        int
	Guardrail                    string
}

type MemoryArchiveStatus struct {
	Path             string
	SchemaReady      bool
	TombstoneCount   int
	ArchivedCount    int
	RetiredCount     int
	SupportedTables  []string
	ProtectedClasses []string
	ApplySupported   bool
	Mode             string
}

type MemoryArchiveApplyReport struct {
	Path              string
	Mode              string
	PlannedCandidates int
	Archived          int
	RetiredFromHot    int
	IdempotentSkipped int
	ProtectedSkipped  int
	SupportedTables   []string
	ProtectedClasses  []string
	Guardrail         string
}

type MemoryArchiveTombstone struct {
	TombstoneID          string
	OriginalTable        string
	OriginalKey          string
	OriginalSummary      string
	OriginalSource       string
	OriginalMetadataJSON string
	Reason               string
	State                string
	ArchivedAt           time.Time
	CreatedAt            time.Time
}

type MemoryArchiveRestoreDryRun struct {
	Path                  string
	Tombstone             MemoryArchiveTombstone
	TargetTable           string
	TargetKey             string
	TargetSummary         string
	TargetSource          string
	TargetMetadataJSON    string
	HotRecordExists       bool
	Conflict              bool
	ConflictReason        string
	RestoreSupported      bool
	ProtectedTruthSkipped int
	Guardrail             string
}

type MemoryRetirementCandidate struct {
	TombstoneID          string
	OriginalTable        string
	OriginalKey          string
	OriginalSummary      string
	OriginalSource       string
	OriginalMetadataJSON string
	Reason               string
}

func NewSQLiteStore(baseDir string) (*Store, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create memory directory: %w", err)
	}

	path := filepath.Join(baseDir, "hot-memory.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite memory store: %w", err)
	}

	// SQLite WAL mode enables concurrent reads alongside a single writer,
	// eliminating the reader/writer mutual blocking of the default
	// journal mode.  Single writer is enforced via SetMaxOpenConns(1).
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	// S3.3: wait briefly under contention instead of failing with SQLITE_BUSY.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, path: path}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) init() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store not configured")
	}

	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS hot_summaries (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
	scope TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	boundary_kind TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (session_id, scope, avatar_id)
);
CREATE INDEX IF NOT EXISTS idx_hot_summaries_session_updated_at
	ON hot_summaries(session_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS hot_events (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
	event_id TEXT NOT NULL,
	event_type TEXT NOT NULL,
	phase TEXT NOT NULL,
	source TEXT NOT NULL,
	summary TEXT NOT NULL,
	emitted_at TEXT NOT NULL,
	PRIMARY KEY (event_id)
);
CREATE INDEX IF NOT EXISTS idx_hot_events_session_emitted_at
	ON hot_events(session_id, emitted_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS idx_hot_events_task_emitted_at
	ON hot_events(task_id, emitted_at DESC, event_id DESC);
CREATE TABLE IF NOT EXISTS hot_telemetry (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	mode TEXT NOT NULL,
	status TEXT NOT NULL,
	provider TEXT NOT NULL,
	model TEXT NOT NULL,
	llm_requests INTEGER NOT NULL DEFAULT 0,
	llm_completions INTEGER NOT NULL DEFAULT 0,
	llm_failures INTEGER NOT NULL DEFAULT 0,
	llm_fallbacks INTEGER NOT NULL DEFAULT 0,
	tool_requests INTEGER NOT NULL DEFAULT 0,
	tool_completions INTEGER NOT NULL DEFAULT 0,
	tool_failures INTEGER NOT NULL DEFAULT 0,
	verification_runs INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	total_tokens INTEGER NOT NULL DEFAULT 0,
	cost_micros INTEGER NOT NULL DEFAULT 0,
	usage_known INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (session_id, mode)
);
CREATE INDEX IF NOT EXISTS idx_hot_telemetry_session_updated_at
	ON hot_telemetry(session_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_hot_telemetry_task_updated_at
	ON hot_telemetry(task_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS hot_llm_status (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	provider TEXT NOT NULL,
	family TEXT NOT NULL,
	model TEXT NOT NULL,
	base_url TEXT NOT NULL,
	api_key_env TEXT NOT NULL,
	supports_web_search INTEGER NOT NULL DEFAULT 0,
	web_search_enabled INTEGER NOT NULL DEFAULT 0,
	think_mode TEXT NOT NULL,
	requires_explicit_model INTEGER NOT NULL DEFAULT 0,
	requires_explicit_base_url INTEGER NOT NULL DEFAULT 0,
	local INTEGER NOT NULL DEFAULT 0,
	warning TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	PRIMARY KEY (session_id)
);
CREATE INDEX IF NOT EXISTS idx_hot_llm_status_session_updated_at
	ON hot_llm_status(session_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_hot_llm_status_task_updated_at
	ON hot_llm_status(task_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS hot_verifications (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
	tool TEXT NOT NULL,
	verdict TEXT NOT NULL,
	summary TEXT NOT NULL,
	report_path TEXT NOT NULL DEFAULT '',
	warnings_json TEXT NOT NULL,
	checks_json TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (session_id, tool)
);
CREATE INDEX IF NOT EXISTS idx_hot_verifications_task_updated_at
	ON hot_verifications(task_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS verification_history (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
	tool TEXT NOT NULL,
	verdict TEXT NOT NULL,
	summary TEXT NOT NULL,
	report_path TEXT NOT NULL DEFAULT '',
	warnings_json TEXT NOT NULL,
	checks_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_verification_history_task_updated_at
	ON verification_history(task_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_verification_history_session_updated_at
	ON verification_history(session_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS evaluation_records (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
	tool TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL,
	verdict TEXT NOT NULL,
	cause TEXT NOT NULL,
	summary TEXT NOT NULL,
	source TEXT NOT NULL,
	report_path TEXT NOT NULL DEFAULT '',
	details_json TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(task_id, kind, summary)
);
CREATE INDEX IF NOT EXISTS idx_evaluation_records_task_updated_at
	ON evaluation_records(task_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_evaluation_records_session_updated_at
	ON evaluation_records(session_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS warm_lessons (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	summary TEXT NOT NULL,
	source TEXT NOT NULL,
	confidence TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(task_id, kind, summary)
);
CREATE INDEX IF NOT EXISTS idx_warm_lessons_task_updated_at
	ON warm_lessons(task_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_warm_lessons_session_updated_at
	ON warm_lessons(session_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS evolution_candidates (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	summary TEXT NOT NULL,
	source TEXT NOT NULL,
	priority TEXT NOT NULL,
	status TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(task_id, kind, summary)
);
CREATE INDEX IF NOT EXISTS idx_evolution_candidates_task_updated_at
	ON evolution_candidates(task_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_evolution_candidates_session_updated_at
	ON evolution_candidates(session_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS project_lessons (
	source_task_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	summary TEXT NOT NULL,
	source TEXT NOT NULL,
	confidence TEXT NOT NULL,
	support_count INTEGER NOT NULL DEFAULT 1,
	support_tasks_json TEXT NOT NULL DEFAULT '[]',
	updated_at TEXT NOT NULL,
	UNIQUE(kind, summary)
);
CREATE INDEX IF NOT EXISTS idx_project_lessons_updated_at
	ON project_lessons(updated_at DESC);
CREATE TABLE IF NOT EXISTS memory_archive_tombstones (
	tombstone_id TEXT NOT NULL,
	original_table TEXT NOT NULL,
	original_key TEXT NOT NULL,
	original_summary TEXT NOT NULL,
	original_source TEXT NOT NULL,
	original_metadata_json TEXT NOT NULL DEFAULT '{}',
	reason TEXT NOT NULL,
	state TEXT NOT NULL,
	archived_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (tombstone_id)
);
	CREATE INDEX IF NOT EXISTS idx_memory_archive_tombstones_table_state
		ON memory_archive_tombstones(original_table, state, archived_at DESC);
	CREATE TABLE IF NOT EXISTS repl_context (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		data TEXT NOT NULL DEFAULT '{}',
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	INSERT OR IGNORE INTO repl_context (id, data) VALUES (1, '{}');
CREATE TABLE IF NOT EXISTS schema_versions (
	version INTEGER NOT NULL,
	description TEXT NOT NULL,
	applied_at TEXT NOT NULL,
	PRIMARY KEY (version)
);
`); err != nil {
		return fmt.Errorf("initialize sqlite memory schema: %w", err)
	}
		// Apply versioned migrations in order. Each migration is
	// idempotent: it checks the current schema version and only
	// applies if the version is below the migration number.
	migrations := []struct {
		version int
		desc    string
		sql     string
	}{
		{1, "add report_path to hot_verifications",
			"ALTER TABLE hot_verifications ADD COLUMN report_path TEXT NOT NULL DEFAULT ''"},
		{2, "add tool to evaluation_records",
			"ALTER TABLE evaluation_records ADD COLUMN tool TEXT NOT NULL DEFAULT ''"},
		{3, "add support_count to project_lessons",
			"ALTER TABLE project_lessons ADD COLUMN support_count INTEGER NOT NULL DEFAULT 1"},
		{4, "add support_tasks_json to project_lessons",
			"ALTER TABLE project_lessons ADD COLUMN support_tasks_json TEXT NOT NULL DEFAULT '[]'"},
	}
	currentVersion := s.schemaVersion()
	for _, m := range migrations {
		if currentVersion >= m.version {
			continue
		}
		if _, err := s.db.Exec(m.sql); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
				_ = s.recordSchemaVersion(m.version, m.desc)
				continue
			}
			return fmt.Errorf("migration %d (%s): %w", m.version, m.desc, err)
		}
		if err := s.recordSchemaVersion(m.version, m.desc); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}

	return nil
}

func (s *Store) UpsertSummary(record SummaryRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("summary record session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("summary record run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("summary record task id is required")
	}
	if record.Scope == "" {
		return fmt.Errorf("summary record scope is required")
	}
	if record.Summary == "" {
		return nil
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}

	_, err := s.db.Exec(`
INSERT INTO hot_summaries (
	session_id,
	run_id,
	task_id,
	avatar_id,
	scope,
	role,
	status,
	boundary_kind,
	summary,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, scope, avatar_id) DO UPDATE SET
	run_id = excluded.run_id,
	task_id = excluded.task_id,
	role = excluded.role,
	status = excluded.status,
	boundary_kind = excluded.boundary_kind,
	summary = excluded.summary,
	updated_at = excluded.updated_at;
`, record.SessionID, record.RunID, record.TaskID, record.AvatarID, record.Scope, record.Role, record.Status, record.BoundaryKind, record.Summary, record.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert hot summary: %w", err)
	}
	return nil
}

func (s *Store) LoadSnapshot(sessionID string) (Snapshot, error) {
	snapshot := Snapshot{AvatarSummaries: map[string]string{}}
	if s == nil || s.db == nil || sessionID == "" {
		return snapshot, nil
	}
	loaded, err := s.loadSnapshot(`
SELECT scope, avatar_id, summary
FROM hot_summaries
WHERE session_id = ?
ORDER BY updated_at DESC
`, sessionID, `
SELECT summary
FROM hot_events
WHERE session_id = ?
ORDER BY emitted_at DESC, event_id DESC
LIMIT ?
`, sessionID, `
SELECT tool, verdict, summary, report_path, warnings_json, checks_json, updated_at
FROM hot_verifications
WHERE session_id = ?
ORDER BY updated_at DESC
LIMIT 1
`, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.Telemetry, err = s.loadTelemetry(`
SELECT mode, status, provider, model, llm_requests, llm_completions, llm_failures, llm_fallbacks, tool_requests, tool_completions, tool_failures, verification_runs, duration_ms, prompt_tokens, completion_tokens, total_tokens, cost_micros, usage_known, updated_at
FROM hot_telemetry
WHERE session_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT 1
`, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.VerificationHistory, err = s.loadVerificationHistory(`
SELECT tool, verdict, summary, report_path, warnings_json, checks_json, updated_at
FROM verification_history
WHERE session_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.EvaluationRecords, err = s.loadEvaluationRecords(`
SELECT session_id, run_id, task_id, avatar_id, tool, kind, verdict, cause, summary, source, report_path, details_json, updated_at
FROM evaluation_records
WHERE session_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.WarmLessons, err = s.loadWarmLessons(`
SELECT kind, summary, source, confidence, updated_at
FROM warm_lessons
WHERE session_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.EvolutionCandidates, err = s.loadEvolutionCandidates(`
SELECT kind, summary, source, priority, status, updated_at
FROM evolution_candidates
WHERE session_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.LLMStatus, loaded.LLMStatusWarning, err = s.loadLLMStatus(`
SELECT provider, family, model, base_url, api_key_env, supports_web_search, web_search_enabled, think_mode, requires_explicit_model, requires_explicit_base_url, local, warning, updated_at
FROM hot_llm_status
WHERE session_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT 1
`, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	return loaded, nil
}

func (s *Store) LoadLatestTaskSnapshot(taskID string) (Snapshot, error) {
	snapshot := Snapshot{AvatarSummaries: map[string]string{}}
	if s == nil || s.db == nil || taskID == "" {
		return snapshot, nil
	}
	loaded, err := s.loadSnapshot(`
SELECT scope, avatar_id, summary
FROM hot_summaries
WHERE task_id = ?
ORDER BY updated_at DESC
`, taskID, `
SELECT summary
FROM hot_events
WHERE task_id = ?
ORDER BY emitted_at DESC, event_id DESC
LIMIT ?
`, taskID, `
SELECT tool, verdict, summary, report_path, warnings_json, checks_json, updated_at
FROM hot_verifications
WHERE task_id = ?
ORDER BY updated_at DESC
LIMIT 1
`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.Telemetry, err = s.loadTelemetry(`
SELECT mode, status, provider, model, llm_requests, llm_completions, llm_failures, llm_fallbacks, tool_requests, tool_completions, tool_failures, verification_runs, duration_ms, prompt_tokens, completion_tokens, total_tokens, cost_micros, usage_known, updated_at
FROM hot_telemetry
WHERE task_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT 1
`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.LLMStatus, loaded.LLMStatusWarning, err = s.loadLLMStatus(`
SELECT provider, family, model, base_url, api_key_env, supports_web_search, web_search_enabled, think_mode, requires_explicit_model, requires_explicit_base_url, local, warning, updated_at
FROM hot_llm_status
WHERE task_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT 1
`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.VerificationHistory, err = s.loadVerificationHistory(`
SELECT tool, verdict, summary, report_path, warnings_json, checks_json, updated_at
FROM verification_history
WHERE task_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.EvaluationRecords, err = s.loadEvaluationRecords(`
SELECT session_id, run_id, task_id, avatar_id, tool, kind, verdict, cause, summary, source, report_path, details_json, updated_at
FROM evaluation_records
WHERE task_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.WarmLessons, err = s.loadWarmLessons(`
SELECT kind, summary, source, confidence, updated_at
FROM warm_lessons
WHERE task_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	loaded.EvolutionCandidates, err = s.loadEvolutionCandidates(`
SELECT kind, summary, source, priority, status, updated_at
FROM evolution_candidates
WHERE task_id = ?
ORDER BY updated_at DESC, run_id DESC
LIMIT ?
`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	return loaded, nil
}

func (s *Store) loadSnapshot(summaryQuery string, summaryValue string, eventQuery string, eventValue string, verificationQuery string, verificationValue string) (Snapshot, error) {
	snapshot := Snapshot{AvatarSummaries: map[string]string{}}
	if s == nil || s.db == nil || summaryValue == "" {
		return snapshot, nil
	}

	rows, err := s.db.Query(summaryQuery, summaryValue)
	if err != nil {
		return Snapshot{}, fmt.Errorf("query hot summaries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var scope string
		var avatarID string
		var summary string
		if err := rows.Scan(&scope, &avatarID, &summary); err != nil {
			return Snapshot{}, fmt.Errorf("scan hot summary: %w", err)
		}
		switch scope {
		case ScopeStable:
			if snapshot.StableSummary == "" {
				snapshot.StableSummary = summary
			}
		case ScopeTask:
			if snapshot.TaskSummary == "" {
				snapshot.TaskSummary = summary
			}
		case ScopeAvatar:
			if avatarID != "" && snapshot.AvatarSummaries[avatarID] == "" {
				snapshot.AvatarSummaries[avatarID] = summary
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("iterate hot summaries: %w", err)
	}

	eventRows, err := s.db.Query(eventQuery, eventValue, hotEventLimit)
	if err != nil {
		return Snapshot{}, fmt.Errorf("query hot events: %w", err)
	}
	defer eventRows.Close()

	for eventRows.Next() {
		var summary string
		if err := eventRows.Scan(&summary); err != nil {
			return Snapshot{}, fmt.Errorf("scan hot event: %w", err)
		}
		if summary != "" {
			snapshot.RecentEvents = append(snapshot.RecentEvents, summary)
		}
	}
	if err := eventRows.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("iterate hot events: %w", err)
	}

	verificationRows, err := s.db.Query(verificationQuery, verificationValue)
	if err != nil {
		return Snapshot{}, fmt.Errorf("query hot verifications: %w", err)
	}
	defer verificationRows.Close()

	if verificationRows.Next() {
		var tool string
		var verdict string
		var summary string
		var reportPath string
		var warningsJSON string
		var checksJSON string
		var updatedAtRaw string
		if err := verificationRows.Scan(&tool, &verdict, &summary, &reportPath, &warningsJSON, &checksJSON, &updatedAtRaw); err != nil {
			return Snapshot{}, fmt.Errorf("scan hot verification: %w", err)
		}
		warnings, err := decodeStringSlice(warningsJSON)
		if err != nil {
			return Snapshot{}, fmt.Errorf("decode verification warnings: %w", err)
		}
		checks, err := decodeStringSlice(checksJSON)
		if err != nil {
			return Snapshot{}, fmt.Errorf("decode verification checks: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
		if err != nil {
			return Snapshot{}, fmt.Errorf("parse verification updated_at: %w", err)
		}
		snapshot.Verification = &VerificationSnapshot{
			Tool:       tool,
			Verdict:    verdict,
			Summary:    summary,
			ReportPath: reportPath,
			Warnings:   warnings,
			Checks:     checks,
			UpdatedAt:  updatedAt,
		}
		normalized := normalizeVerificationSnapshot(*snapshot.Verification)
		snapshot.Verification = &normalized
	}
	if err := verificationRows.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("iterate hot verifications: %w", err)
	}
	return snapshot, nil
}

func (s *Store) loadVerificationHistory(query string, value string) ([]VerificationSnapshot, error) {
	if s == nil || s.db == nil || value == "" {
		return nil, nil
	}
	rows, err := s.db.Query(query, value, verificationHistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("query verification history: %w", err)
	}
	defer rows.Close()

	history := make([]VerificationSnapshot, 0, verificationHistoryLimit)
	for rows.Next() {
		var tool string
		var verdict string
		var summary string
		var reportPath string
		var warningsJSON string
		var checksJSON string
		var updatedAtRaw string
		if err := rows.Scan(&tool, &verdict, &summary, &reportPath, &warningsJSON, &checksJSON, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan verification history: %w", err)
		}
		warnings, err := decodeStringSlice(warningsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode verification history warnings: %w", err)
		}
		checks, err := decodeStringSlice(checksJSON)
		if err != nil {
			return nil, fmt.Errorf("decode verification history checks: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse verification history updated_at: %w", err)
		}
		history = append(history, normalizeVerificationSnapshot(VerificationSnapshot{
			Tool:       tool,
			Verdict:    verdict,
			Summary:    summary,
			ReportPath: reportPath,
			Warnings:   warnings,
			Checks:     checks,
			UpdatedAt:  updatedAt,
		}))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verification history: %w", err)
	}
	return history, nil
}

func (s *Store) loadEvaluationRecords(query string, value string) ([]EvaluationRecord, error) {
	if s == nil || s.db == nil || value == "" {
		return nil, nil
	}
	rows, err := s.db.Query(query, value, evaluationRecordLimit)
	if err != nil {
		return nil, fmt.Errorf("query evaluation records: %w", err)
	}
	defer rows.Close()

	records := make([]EvaluationRecord, 0, evaluationRecordLimit)
	for rows.Next() {
		var record EvaluationRecord
		var detailsJSON string
		var updatedAtRaw string
		if err := rows.Scan(&record.SessionID, &record.RunID, &record.TaskID, &record.AvatarID, &record.Tool, &record.Kind, &record.Verdict, &record.Cause, &record.Summary, &record.Source, &record.ReportPath, &detailsJSON, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan evaluation record: %w", err)
		}
		details, err := decodeStringSlice(detailsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode evaluation details: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse evaluation updated_at: %w", err)
		}
		record.Details = details
		record.UpdatedAt = updatedAt
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evaluation records: %w", err)
	}
	return records, nil
}

func (s *Store) loadWarmLessons(query string, value string) ([]WarmLesson, error) {
	if s == nil || s.db == nil || value == "" {
		return nil, nil
	}
	rows, err := s.db.Query(query, value, warmLessonLimit)
	if err != nil {
		return nil, fmt.Errorf("query warm lessons: %w", err)
	}
	defer rows.Close()

	lessons := make([]WarmLesson, 0, warmLessonLimit)
	for rows.Next() {
		var kind string
		var summary string
		var source string
		var confidence string
		var updatedAtRaw string
		if err := rows.Scan(&kind, &summary, &source, &confidence, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan warm lesson: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse warm lesson updated_at: %w", err)
		}
		lessons = append(lessons, WarmLesson{
			Kind:       kind,
			Summary:    summary,
			Source:     source,
			Confidence: confidence,
			UpdatedAt:  updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate warm lessons: %w", err)
	}
	return lessons, nil
}

func (s *Store) loadTelemetry(query string, value string) (*runtelemetry.Snapshot, error) {
	if s == nil || s.db == nil || value == "" {
		return nil, nil
	}
	var snapshot runtelemetry.Snapshot
	var updatedAt string
	var usageKnown int
	err := s.db.QueryRow(query, value).Scan(
		&snapshot.Mode,
		&snapshot.Status,
		&snapshot.Provider,
		&snapshot.Model,
		&snapshot.LLMRequests,
		&snapshot.LLMCompletions,
		&snapshot.LLMFailures,
		&snapshot.LLMFallbacks,
		&snapshot.ToolRequests,
		&snapshot.ToolCompletions,
		&snapshot.ToolFailures,
		&snapshot.VerificationRuns,
		&snapshot.DurationMillis,
		&snapshot.PromptTokens,
		&snapshot.CompletionTokens,
		&snapshot.TotalTokens,
		&snapshot.CostMicros,
		&usageKnown,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query telemetry snapshot: %w", err)
	}
	snapshot.UsageKnown = usageKnown != 0
	parsedTime, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse telemetry updated at: %w", err)
	}
	snapshot.UpdatedAt = parsedTime
	return &snapshot, nil
}

func (s *Store) loadLLMStatus(query string, value string) (*llm.ProviderStatus, string, error) {
	if s == nil || s.db == nil || value == "" {
		return nil, "", nil
	}
	var status llm.ProviderStatus
	var supportsWebSearch int
	var webSearchEnabled int
	var requiresExplicitModel int
	var requiresExplicitBaseURL int
	var local int
	var warning string
	var updatedAt string
	err := s.db.QueryRow(query, value).Scan(
		&status.Name,
		&status.Family,
		&status.Model,
		&status.BaseURL,
		&status.APIKeyEnv,
		&supportsWebSearch,
		&webSearchEnabled,
		&status.ThinkMode,
		&requiresExplicitModel,
		&requiresExplicitBaseURL,
		&local,
		&warning,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("query llm status snapshot: %w", err)
	}
	status.SupportsWebSearch = supportsWebSearch != 0
	status.WebSearchEnabled = webSearchEnabled != 0
	status.RequiresExplicitModel = requiresExplicitModel != 0
	status.RequiresExplicitBaseURL = requiresExplicitBaseURL != 0
	status.Local = local != 0
	if _, err := time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, "", fmt.Errorf("parse llm status updated at: %w", err)
	}
	return &status, warning, nil
}

func (s *Store) loadEvolutionCandidates(query string, value string) ([]EvolutionCandidate, error) {
	if s == nil || s.db == nil || value == "" {
		return nil, nil
	}
	rows, err := s.db.Query(query, value, evolutionCandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("query evolution candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]EvolutionCandidate, 0, evolutionCandidateLimit)
	for rows.Next() {
		var kind string
		var summary string
		var source string
		var priority string
		var status string
		var updatedAtRaw string
		if err := rows.Scan(&kind, &summary, &source, &priority, &status, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan evolution candidate: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse evolution candidate updated_at: %w", err)
		}
		candidates = append(candidates, EvolutionCandidate{
			Kind:      kind,
			Summary:   summary,
			Source:    source,
			Priority:  priority,
			Status:    status,
			UpdatedAt: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evolution candidates: %w", err)
	}
	return candidates, nil
}

func (s *Store) LoadProjectLessons() ([]ProjectLesson, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
SELECT source_task_id, kind, summary, source, confidence, support_count, support_tasks_json, updated_at
FROM project_lessons
ORDER BY updated_at DESC
LIMIT ?
`, projectLessonLimit)
	if err != nil {
		return nil, fmt.Errorf("query project lessons: %w", err)
	}
	defer rows.Close()

	lessons := make([]ProjectLesson, 0, projectLessonLimit)
	for rows.Next() {
		var sourceTaskID string
		var kind string
		var summary string
		var source string
		var confidence string
		var supportCount int
		var supportTasksJSON string
		var updatedAtRaw string
		if err := rows.Scan(&sourceTaskID, &kind, &summary, &source, &confidence, &supportCount, &supportTasksJSON, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan project lesson: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse project lesson updated_at: %w", err)
		}
		supportTasks, err := decodeStringSlice(supportTasksJSON)
		if err != nil {
			return nil, fmt.Errorf("decode project lesson support tasks: %w", err)
		}
		supportTasks = normalizeSupportTasks(sourceTaskID, supportTasks)
		if supportCount <= 0 {
			supportCount = len(supportTasks)
		}
		if supportCount <= 0 {
			supportCount = 1
		}
		lessons = append(lessons, ProjectLesson{
			SourceTaskID: sourceTaskID,
			Kind:         kind,
			Summary:      summary,
			Source:       source,
			Confidence:   confidence,
			SupportCount: supportCount,
			SupportTasks: append([]string(nil), supportTasks...),
			UpdatedAt:    updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project lessons: %w", err)
	}
	rankProjectLessons(lessons)
	return lessons, nil
}

func (s *Store) RecordEvent(record EventRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("event record session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("event record run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("event record task id is required")
	}
	if record.EventID == "" {
		return fmt.Errorf("event record event id is required")
	}
	if record.EventType == "" {
		return fmt.Errorf("event record event type is required")
	}
	if record.Summary == "" {
		return nil
	}
	if record.EmittedAt.IsZero() {
		record.EmittedAt = time.Now().UTC()
	}

	_, err := s.db.Exec(`
INSERT INTO hot_events (
	session_id,
	run_id,
	task_id,
	avatar_id,
	event_id,
	event_type,
	phase,
	source,
	summary,
	emitted_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(event_id) DO UPDATE SET
	session_id = excluded.session_id,
	run_id = excluded.run_id,
	task_id = excluded.task_id,
	avatar_id = excluded.avatar_id,
	event_type = excluded.event_type,
	phase = excluded.phase,
	source = excluded.source,
	summary = excluded.summary,
	emitted_at = excluded.emitted_at;
`, record.SessionID, record.RunID, record.TaskID, record.AvatarID, record.EventID, record.EventType, record.Phase, record.Source, record.Summary, record.EmittedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record hot event: %w", err)
	}
	if _, err := s.db.Exec(`
DELETE FROM hot_events
WHERE task_id = ?
	AND event_id NOT IN (
		SELECT event_id
		FROM hot_events
		WHERE task_id = ?
		ORDER BY emitted_at DESC, event_id DESC
		LIMIT ?
	);
`, record.TaskID, record.TaskID, hotEventLimit); err != nil {
		return fmt.Errorf("prune hot events: %w", err)
	}
	return nil
}

func (s *Store) RecordVerification(record VerificationRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("verification record session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("verification record run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("verification record task id is required")
	}
	if record.Tool == "" {
		return fmt.Errorf("verification record tool is required")
	}
	if record.Verdict == "" {
		return fmt.Errorf("verification record verdict is required")
	}
	if record.Summary == "" {
		return nil
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	warningsJSON, err := encodeStringSlice(record.Warnings)
	if err != nil {
		return fmt.Errorf("encode verification warnings: %w", err)
	}
	checksJSON, err := encodeStringSlice(record.Checks)
	if err != nil {
		return fmt.Errorf("encode verification checks: %w", err)
	}

	_, err = s.db.Exec(`
INSERT INTO hot_verifications (
	session_id,
	run_id,
	task_id,
	avatar_id,
	tool,
	verdict,
	summary,
	report_path,
	warnings_json,
	checks_json,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, tool) DO UPDATE SET
	run_id = excluded.run_id,
	task_id = excluded.task_id,
	avatar_id = excluded.avatar_id,
	verdict = excluded.verdict,
	summary = excluded.summary,
	report_path = excluded.report_path,
	warnings_json = excluded.warnings_json,
	checks_json = excluded.checks_json,
	updated_at = excluded.updated_at;
`, record.SessionID, record.RunID, record.TaskID, record.AvatarID, record.Tool, record.Verdict, record.Summary, record.ReportPath, warningsJSON, checksJSON, record.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record verification snapshot: %w", err)
	}
	if _, err := s.db.Exec(`
INSERT INTO verification_history (
	session_id,
	run_id,
	task_id,
	avatar_id,
	tool,
	verdict,
	summary,
	report_path,
	warnings_json,
	checks_json,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, record.SessionID, record.RunID, record.TaskID, record.AvatarID, record.Tool, record.Verdict, record.Summary, record.ReportPath, warningsJSON, checksJSON, record.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("append verification history: %w", err)
	}
	if _, err := s.db.Exec(`
DELETE FROM verification_history
WHERE task_id = ?
	AND rowid NOT IN (
		SELECT rowid
		FROM verification_history
		WHERE task_id = ?
		ORDER BY updated_at DESC, run_id DESC
		LIMIT ?
	);
`, record.TaskID, record.TaskID, verificationHistoryLimit); err != nil {
		return fmt.Errorf("prune verification history: %w", err)
	}
	return nil
}

func (s *Store) RecordEvaluation(record EvaluationRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("evaluation record session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("evaluation record run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("evaluation record task id is required")
	}
	if record.Kind == "" {
		return fmt.Errorf("evaluation record kind is required")
	}
	if record.Summary == "" {
		return nil
	}
	if record.Source == "" {
		record.Source = "verification"
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	detailsJSON, err := encodeStringSlice(record.Details)
	if err != nil {
		return fmt.Errorf("encode evaluation details: %w", err)
	}

	if _, err := s.db.Exec(`
INSERT INTO evaluation_records (
	session_id,
	run_id,
	task_id,
	avatar_id,
	tool,
	kind,
	verdict,
	cause,
	summary,
	source,
	report_path,
	details_json,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id, kind, summary) DO UPDATE SET
	session_id = excluded.session_id,
	run_id = excluded.run_id,
	avatar_id = excluded.avatar_id,
	tool = excluded.tool,
	verdict = excluded.verdict,
	cause = excluded.cause,
	source = excluded.source,
	report_path = excluded.report_path,
	details_json = excluded.details_json,
	updated_at = excluded.updated_at;
`, record.SessionID, record.RunID, record.TaskID, record.AvatarID, record.Tool, record.Kind, record.Verdict, record.Cause, record.Summary, record.Source, record.ReportPath, detailsJSON, record.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record evaluation record: %w", err)
	}
	if _, err := s.db.Exec(`
DELETE FROM evaluation_records
WHERE task_id = ?
	AND rowid NOT IN (
		SELECT rowid
		FROM evaluation_records
		WHERE task_id = ?
		ORDER BY updated_at DESC, run_id DESC
		LIMIT ?
	);
`, record.TaskID, record.TaskID, evaluationRecordLimit); err != nil {
		return fmt.Errorf("prune evaluation records: %w", err)
	}
	return nil
}

func (s *Store) RecordTelemetry(record TelemetryRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("telemetry record session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("telemetry record run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("telemetry record task id is required")
	}
	if strings.TrimSpace(record.Snapshot.Mode) == "" {
		record.Snapshot.Mode = "run"
	}
	if record.Snapshot.UpdatedAt.IsZero() {
		record.Snapshot.UpdatedAt = time.Now().UTC()
	}
	usageKnown := 0
	if record.Snapshot.UsageKnown {
		usageKnown = 1
	}
	if _, err := s.db.Exec(`
INSERT INTO hot_telemetry (
	session_id,
	run_id,
	task_id,
	mode,
	status,
	provider,
	model,
	llm_requests,
	llm_completions,
	llm_failures,
	llm_fallbacks,
	tool_requests,
	tool_completions,
	tool_failures,
	verification_runs,
	duration_ms,
	prompt_tokens,
	completion_tokens,
	total_tokens,
	cost_micros,
	usage_known,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, mode) DO UPDATE SET
	run_id = excluded.run_id,
	task_id = excluded.task_id,
	status = excluded.status,
	provider = excluded.provider,
	model = excluded.model,
	llm_requests = excluded.llm_requests,
	llm_completions = excluded.llm_completions,
	llm_failures = excluded.llm_failures,
	llm_fallbacks = excluded.llm_fallbacks,
	tool_requests = excluded.tool_requests,
	tool_completions = excluded.tool_completions,
	tool_failures = excluded.tool_failures,
	verification_runs = excluded.verification_runs,
	duration_ms = excluded.duration_ms,
	prompt_tokens = excluded.prompt_tokens,
	completion_tokens = excluded.completion_tokens,
	total_tokens = excluded.total_tokens,
	cost_micros = excluded.cost_micros,
	usage_known = excluded.usage_known,
	updated_at = excluded.updated_at;
`, record.SessionID, record.RunID, record.TaskID, record.Snapshot.Mode, record.Snapshot.Status, record.Snapshot.Provider, record.Snapshot.Model, record.Snapshot.LLMRequests, record.Snapshot.LLMCompletions, record.Snapshot.LLMFailures, record.Snapshot.LLMFallbacks, record.Snapshot.ToolRequests, record.Snapshot.ToolCompletions, record.Snapshot.ToolFailures, record.Snapshot.VerificationRuns, record.Snapshot.DurationMillis, record.Snapshot.PromptTokens, record.Snapshot.CompletionTokens, record.Snapshot.TotalTokens, record.Snapshot.CostMicros, usageKnown, record.Snapshot.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record telemetry snapshot: %w", err)
	}
	return nil
}

func (s *Store) RecordLLMStatus(record LLMStatusRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("llm status record session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("llm status record run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("llm status record task id is required")
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	supportsWebSearch := 0
	if record.Status.SupportsWebSearch {
		supportsWebSearch = 1
	}
	webSearchEnabled := 0
	if record.Status.WebSearchEnabled {
		webSearchEnabled = 1
	}
	requiresExplicitModel := 0
	if record.Status.RequiresExplicitModel {
		requiresExplicitModel = 1
	}
	requiresExplicitBaseURL := 0
	if record.Status.RequiresExplicitBaseURL {
		requiresExplicitBaseURL = 1
	}
	local := 0
	if record.Status.Local {
		local = 1
	}
	if _, err := s.db.Exec(`
INSERT INTO hot_llm_status (
	session_id,
	run_id,
	task_id,
	provider,
	family,
	model,
	base_url,
	api_key_env,
	supports_web_search,
	web_search_enabled,
	think_mode,
	requires_explicit_model,
	requires_explicit_base_url,
	local,
	warning,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
	run_id = excluded.run_id,
	task_id = excluded.task_id,
	provider = excluded.provider,
	family = excluded.family,
	model = excluded.model,
	base_url = excluded.base_url,
	api_key_env = excluded.api_key_env,
	supports_web_search = excluded.supports_web_search,
	web_search_enabled = excluded.web_search_enabled,
	think_mode = excluded.think_mode,
	requires_explicit_model = excluded.requires_explicit_model,
	requires_explicit_base_url = excluded.requires_explicit_base_url,
	local = excluded.local,
	warning = excluded.warning,
	updated_at = excluded.updated_at;
`, record.SessionID, record.RunID, record.TaskID, record.Status.Name, record.Status.Family, record.Status.Model, record.Status.BaseURL, record.Status.APIKeyEnv, supportsWebSearch, webSearchEnabled, string(record.Status.ThinkMode), requiresExplicitModel, requiresExplicitBaseURL, local, record.Warning, record.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record llm status snapshot: %w", err)
	}
	return nil
}

func (s *Store) RecordWarmLesson(record WarmLessonRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("warm lesson session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("warm lesson run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("warm lesson task id is required")
	}
	if record.Kind == "" {
		return fmt.Errorf("warm lesson kind is required")
	}
	if record.Summary == "" {
		return nil
	}
	if record.Source == "" {
		record.Source = "runtime"
	}
	if record.Confidence == "" {
		record.Confidence = "medium"
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}

	if _, err := s.db.Exec(`
INSERT INTO warm_lessons (
	session_id,
	run_id,
	task_id,
	kind,
	summary,
	source,
	confidence,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id, kind, summary) DO UPDATE SET
	session_id = excluded.session_id,
	run_id = excluded.run_id,
	source = excluded.source,
	confidence = excluded.confidence,
	updated_at = excluded.updated_at;
`, record.SessionID, record.RunID, record.TaskID, record.Kind, record.Summary, record.Source, record.Confidence, record.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record warm lesson: %w", err)
	}
	if _, err := s.db.Exec(`
DELETE FROM warm_lessons
WHERE task_id = ?
	AND rowid NOT IN (
		SELECT rowid
		FROM warm_lessons
		WHERE task_id = ?
		ORDER BY updated_at DESC, run_id DESC
		LIMIT ?
	);
`, record.TaskID, record.TaskID, warmLessonLimit); err != nil {
		return fmt.Errorf("prune warm lessons: %w", err)
	}
	return nil
}

func (s *Store) RecordEvolutionCandidate(record EvolutionCandidateRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return fmt.Errorf("evolution candidate session id is required")
	}
	if record.RunID == "" {
		return fmt.Errorf("evolution candidate run id is required")
	}
	if record.TaskID == "" {
		return fmt.Errorf("evolution candidate task id is required")
	}
	if record.Kind == "" {
		return fmt.Errorf("evolution candidate kind is required")
	}
	if record.Summary == "" {
		return nil
	}
	if record.Source == "" {
		record.Source = "runtime"
	}
	if record.Priority == "" {
		record.Priority = "medium"
	}
	if record.Status == "" {
		record.Status = "open"
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}

	if _, err := s.db.Exec(`
INSERT INTO evolution_candidates (
	session_id,
	run_id,
	task_id,
	kind,
	summary,
	source,
	priority,
	status,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id, kind, summary) DO UPDATE SET
	session_id = excluded.session_id,
	run_id = excluded.run_id,
	source = excluded.source,
	priority = excluded.priority,
	status = excluded.status,
	updated_at = excluded.updated_at;
`, record.SessionID, record.RunID, record.TaskID, record.Kind, record.Summary, record.Source, record.Priority, record.Status, record.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record evolution candidate: %w", err)
	}
	if _, err := s.db.Exec(`
DELETE FROM evolution_candidates
WHERE task_id = ?
	AND rowid NOT IN (
		SELECT rowid
		FROM evolution_candidates
		WHERE task_id = ?
		ORDER BY updated_at DESC, run_id DESC
		LIMIT ?
	);
`, record.TaskID, record.TaskID, evolutionCandidateLimit); err != nil {
		return fmt.Errorf("prune evolution candidates: %w", err)
	}
	return nil
}

func (s *Store) RecordProjectLesson(record ProjectLessonRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.Kind == "" {
		return fmt.Errorf("project lesson kind is required")
	}
	if record.Summary == "" {
		return nil
	}
	if record.Source == "" {
		record.Source = "project_promotion"
	}
	if record.Confidence == "" {
		record.Confidence = "medium"
	}
	record.SupportTasks = normalizeSupportTasks(record.SourceTaskID, record.SupportTasks)
	if record.SupportCount <= 0 {
		record.SupportCount = len(record.SupportTasks)
	}
	if record.SupportCount <= 0 {
		record.SupportCount = 1
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	existingCount, existingTasks, err := s.loadProjectLessonSupport(record.Kind, record.Summary)
	if err != nil {
		return err
	}
	record.SupportTasks = mergeSupportTasks(existingTasks, record.SupportTasks)
	if existingCount > len(record.SupportTasks) {
		record.SupportCount = existingCount
	} else {
		record.SupportCount = len(record.SupportTasks)
	}
	supportTasksJSON, err := encodeStringSlice(record.SupportTasks)
	if err != nil {
		return fmt.Errorf("encode project lesson support tasks: %w", err)
	}

	if _, err := s.db.Exec(`
INSERT INTO project_lessons (
	source_task_id,
	kind,
	summary,
	source,
	confidence,
	support_count,
	support_tasks_json,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, summary) DO UPDATE SET
	source_task_id = excluded.source_task_id,
	source = excluded.source,
	confidence = excluded.confidence,
	support_count = excluded.support_count,
	support_tasks_json = excluded.support_tasks_json,
	updated_at = excluded.updated_at;
`, record.SourceTaskID, record.Kind, record.Summary, record.Source, record.Confidence, record.SupportCount, supportTasksJSON, record.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record project lesson: %w", err)
	}
	if _, err := s.db.Exec(`
DELETE FROM project_lessons
WHERE rowid NOT IN (
	SELECT rowid
	FROM project_lessons
	ORDER BY support_count DESC,
		CASE LOWER(confidence)
			WHEN 'high' THEN 3
			WHEN 'medium' THEN 2
			WHEN 'low' THEN 1
			ELSE 0
		END DESC,
		updated_at DESC,
		summary ASC
	LIMIT ?
);
`, projectLessonLimit); err != nil {
		return fmt.Errorf("prune project lessons: %w", err)
	}
	return nil
}

func (s *Store) loadProjectLessonSupport(kind string, summary string) (int, []string, error) {
	if s == nil || s.db == nil {
		return 0, nil, nil
	}
	var sourceTaskID string
	var supportCount int
	var supportTasksJSON string
	err := s.db.QueryRow(`
SELECT source_task_id, support_count, support_tasks_json
FROM project_lessons
WHERE kind = ? AND summary = ?
`, kind, summary).Scan(&sourceTaskID, &supportCount, &supportTasksJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("load project lesson support: %w", err)
	}
	supportTasks, err := decodeStringSlice(supportTasksJSON)
	if err != nil {
		return 0, nil, fmt.Errorf("decode project lesson support tasks: %w", err)
	}
	supportTasks = normalizeSupportTasks(sourceTaskID, supportTasks)
	if supportCount <= 0 {
		supportCount = len(supportTasks)
	}
	return supportCount, supportTasks, nil
}

func normalizeSupportTasks(sourceTaskID string, tasks []string) []string {
	seen := make(map[string]struct{}, len(tasks)+1)
	normalized := make([]string, 0, len(tasks)+1)
	appendTask := func(taskID string) {
		trimmed := strings.TrimSpace(taskID)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	appendTask(sourceTaskID)
	for _, taskID := range tasks {
		appendTask(taskID)
	}
	sort.Strings(normalized)
	return normalized
}

func mergeSupportTasks(existing []string, incoming []string) []string {
	merged := make([]string, 0, len(existing)+len(incoming))
	merged = append(merged, existing...)
	merged = append(merged, incoming...)
	return normalizeSupportTasks("", merged)
}

func rankProjectLessons(lessons []ProjectLesson) {
	sort.SliceStable(lessons, func(i int, j int) bool {
		left := lessons[i]
		right := lessons[j]
		leftSupport := left.SupportCount
		if leftSupport <= 0 {
			leftSupport = len(left.SupportTasks)
		}
		if leftSupport <= 0 {
			leftSupport = 1
		}
		rightSupport := right.SupportCount
		if rightSupport <= 0 {
			rightSupport = len(right.SupportTasks)
		}
		if rightSupport <= 0 {
			rightSupport = 1
		}
		if leftSupport != rightSupport {
			return leftSupport > rightSupport
		}
		leftConfidence := confidenceRank(left.Confidence)
		rightConfidence := confidenceRank(right.Confidence)
		if leftConfidence != rightConfidence {
			return leftConfidence > rightConfidence
		}
		leftSource := projectLessonSourceRank(left.Source)
		rightSource := projectLessonSourceRank(right.Source)
		if leftSource != rightSource {
			return leftSource > rightSource
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Summary != right.Summary {
			return left.Summary < right.Summary
		}
		return left.SourceTaskID < right.SourceTaskID
	})
}

func confidenceRank(confidence string) int {
	switch strings.ToLower(strings.TrimSpace(confidence)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func projectLessonSourceRank(source string) int {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "evolution_candidate":
		return 2
	case "warm_lesson":
		return 1
	default:
		return 0
	}
}

func encodeStringSlice(values []string) (string, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeStringSlice(encoded string) ([]string, error) {
	if encoded == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}


// PromoteProjectLessonsToWarm upgrades high-confidence project lessons
// (support_count >= minSupport) to warm lessons scoped to the current
// task. This enables long-term learning: patterns that prove successful
// across multiple tasks automatically influence future runs.
func (s *Store) PromoteProjectLessonsToWarm(taskID string, minSupport int) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if minSupport <= 0 {
		minSupport = 3
	}
	lessons, err := s.LoadProjectLessons()
	if err != nil {
		return 0, err
	}
	promoted := 0
	for _, lesson := range lessons {
		if lesson.SupportCount < minSupport {
			continue
		}
		if lesson.Confidence != "high" && lesson.Confidence != "confirmed" {
			continue
		}
		record := WarmLessonRecord{
			SessionID:  "project-cross-task",
			TaskID:     taskID,
			Kind:       lesson.Kind,
			Summary:    lesson.Summary,
			Source:     "promoted_from_project_lesson",
			Confidence: lesson.Confidence,
			UpdatedAt:  time.Now().UTC(),
		}
		if err := s.RecordWarmLesson(record); err != nil {
			continue
		}
		promoted++
	}
	return promoted, nil
}
// WithCompressor sets the LLM-based summary compression callback. PARTIAL-7.
func (s *Store) WithCompressor(c SummaryCompressor) *Store {
	s.compressor = c
	return s
}

// CompressStableSummary compresses the stable summary for one session when it
// exceeds maxLen. Uses LLM-based compression when a compressor callback is set
// (PARTIAL-7), otherwise falls back to character truncation.
//
// S3.4: SELECT and UPDATE both require session_id so multi-session stores do
// not cross-write. Callers that previously used CompressStableSummary(maxLen)
// must pass the active session id.
func (s *Store) CompressStableSummary(sessionID string, maxLen int) error {
	if s == nil || s.db == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	// Must fully release the read connection before Exec: SetMaxOpenConns(1)
	// deadlocks if rows stay open across the UPDATE (S3.4 fix follow-up).
	var summary string
	err := func() error {
		rows, qErr := s.db.Query(
			`SELECT summary FROM hot_summaries WHERE scope = ? AND session_id = ? ORDER BY updated_at DESC LIMIT 1`,
			ScopeStable, sessionID,
		)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		if !rows.Next() {
			return nil
		}
		return rows.Scan(&summary)
	}()
	if err != nil {
		return err
	}
	if summary == "" || len(summary) <= maxLen {
		return nil
	}
	var compressed string
	if s.compressor != nil {
		compressed = s.compressor(summary, maxLen)
	} else {
		compressed = summary[:maxLen]
		if idx := strings.LastIndex(compressed, ". "); idx > maxLen/2 {
			compressed = compressed[:idx+1]
		}
	}
	if len(compressed) >= len(summary) {
		return nil // compression didn't help
	}
	compressed += fmt.Sprintf(" [...compressed from %d chars]", len(summary))
	_, err = s.db.Exec(
		`UPDATE hot_summaries SET summary = ?, updated_at = ? WHERE scope = ? AND session_id = ?`,
		compressed, time.Now().UTC().Format(time.RFC3339Nano), ScopeStable, sessionID,
	)
	return err
}

// MemoryBudget returns the total character count of all memory data that
// would be injected into a system prompt. P2-2b.
func (s *Store) MemoryBudget(sessionID string) int {
	if s == nil || s.db == nil {
		return 0
	}
	var total int
	s.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(summary)), 0) FROM hot_summaries WHERE session_id = ?`, sessionID).Scan(&total)
	var events int
	s.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(summary)), 0) FROM hot_events WHERE session_id = ?`, sessionID).Scan(&events)
	total += events
	var evals int
	s.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(summary)), 0) FROM evaluation_records WHERE session_id = ?`, sessionID).Scan(&evals)
	total += evals
	return total
}

// schemaVersion returns the highest applied migration version, or 0 if
// no migrations have been recorded yet.
func (s *Store) schemaVersion() int {
	if s == nil || s.db == nil {
		return 0
	}
	var version int
	err := s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_versions").Scan(&version)
	if err != nil {
		return 0
	}
	return version
}

func (s *Store) recordSchemaVersion(version int, description string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO schema_versions (version, description, applied_at) VALUES (?, ?, ?)",
		version, description, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) LoadMemoryLifecycleStatus(now time.Time) (MemoryLifecycleStatus, error) {
	status := MemoryLifecycleStatus{
		Path:                 s.Path(),
		MaintenanceSupported: true,
		MaintenanceMode:      "dry_run_with_guarded_archive_apply",
		PruningLimits: []MemoryPruningLimit{
			{Name: "hot_events_per_task", Limit: hotEventLimit},
			{Name: "verification_history_per_scope", Limit: verificationHistoryLimit},
			{Name: "evaluation_records_per_task", Limit: evaluationRecordLimit},
			{Name: "warm_lessons_per_task", Limit: warmLessonLimit},
			{Name: "evolution_candidates_per_task", Limit: evolutionCandidateLimit},
			{Name: "project_lessons_global", Limit: projectLessonLimit},
		},
	}
	if s == nil || s.db == nil {
		return status, nil
	}
	if stat, err := os.Stat(s.path); err == nil {
		status.SizeBytes = stat.Size()
	}
	tables := []struct {
		name       string
		timeColumn string
	}{
		{name: "hot_summaries", timeColumn: "updated_at"},
		{name: "hot_events", timeColumn: "emitted_at"},
		{name: "hot_telemetry", timeColumn: "updated_at"},
		{name: "hot_llm_status", timeColumn: "updated_at"},
		{name: "hot_verifications", timeColumn: "updated_at"},
		{name: "verification_history", timeColumn: "updated_at"},
		{name: "evaluation_records", timeColumn: "updated_at"},
		{name: "warm_lessons", timeColumn: "updated_at"},
		{name: "evolution_candidates", timeColumn: "updated_at"},
		{name: "project_lessons", timeColumn: "updated_at"},
	}
	for _, table := range tables {
		tableStatus, err := s.memoryTableStatus(table.name, table.timeColumn)
		if err != nil {
			return MemoryLifecycleStatus{}, err
		}
		status.Tables = append(status.Tables, tableStatus)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	staleBefore := now.UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	var err error
	status.StaleWarmLessons, err = s.countMemoryRows(`SELECT COUNT(*) FROM warm_lessons WHERE LOWER(confidence) = 'low' OR updated_at < ?`, staleBefore)
	if err != nil {
		return MemoryLifecycleStatus{}, fmt.Errorf("count stale warm lessons: %w", err)
	}
	status.RetirableEvolutionCandidates, err = s.countMemoryRows(`SELECT COUNT(*) FROM evolution_candidates WHERE LOWER(status) IN ('closed', 'superseded', 'rejected', 'retired') OR updated_at < ?`, staleBefore)
	if err != nil {
		return MemoryLifecycleStatus{}, fmt.Errorf("count retirable evolution candidates: %w", err)
	}
	status.LowSupportProjectLessons, err = s.countMemoryRows(`SELECT COUNT(*) FROM project_lessons WHERE support_count <= 1 AND LOWER(confidence) IN ('', 'low', 'medium')`, "")
	if err != nil {
		return MemoryLifecycleStatus{}, fmt.Errorf("count low-support project lessons: %w", err)
	}
	status.ProtectedEvaluationRecords, err = s.countMemoryRows(`SELECT COUNT(*) FROM evaluation_records WHERE kind = 'runtime_lifecycle' OR cause IN ('run_lifecycle_updated', 'failed_node_pause_point', 'failed_node_retry_attempt', 'failed_node_retry_scheduler_resume', 'node_work_evidence', 'tool_approval_required', 'tool_approval_replayed', 'tool_approval_denied', 'verifier_remediation_pause_point', 'verifier_remediation_resume_attempt', 'verification_reverify_covered')`, "")
	if err != nil {
		return MemoryLifecycleStatus{}, fmt.Errorf("count protected evaluation records: %w", err)
	}
	status.ProtectedVerificationHistory, err = s.countMemoryRows(`SELECT COUNT(*) FROM verification_history`, "")
	if err != nil {
		return MemoryLifecycleStatus{}, fmt.Errorf("count protected verification history: %w", err)
	}
	status.RetirementCandidateTotal = status.StaleWarmLessons + status.RetirableEvolutionCandidates + status.LowSupportProjectLessons
	return status, nil
}

func (s *Store) PlanMemoryMaintenance(now time.Time) (MemoryMaintenanceReport, error) {
	status, err := s.LoadMemoryLifecycleStatus(now)
	if err != nil {
		return MemoryMaintenanceReport{}, err
	}
	report := MemoryMaintenanceReport{
		Path:                         status.Path,
		Mode:                         "dry_run",
		ApplySupported:               true,
		PlannedAction:                "preview_archive_candidates",
		StaleWarmLessons:             status.StaleWarmLessons,
		RetirableEvolutionCandidates: status.RetirableEvolutionCandidates,
		LowSupportProjectLessons:     status.LowSupportProjectLessons,
		RetirementCandidateTotal:     status.RetirementCandidateTotal,
		ProtectedEvaluationRecords:   status.ProtectedEvaluationRecords,
		ProtectedVerificationHistory: status.ProtectedVerificationHistory,
		Guardrail:                    "Dry-run does not delete, archive, retire, or vacuum records. Apply requires --apply --confirm-archive and is advisory-only.",
	}
	var errCount error
	report.ReusableWarmLessonsKept, errCount = s.countMemoryRows(`SELECT COUNT(*) FROM warm_lessons WHERE LOWER(confidence) IN ('high', 'medium')`, "")
	if errCount != nil {
		return MemoryMaintenanceReport{}, fmt.Errorf("count reusable warm lessons: %w", errCount)
	}
	report.ReusableEvolutionKept, errCount = s.countMemoryRows(`SELECT COUNT(*) FROM evolution_candidates WHERE LOWER(status) NOT IN ('closed', 'superseded', 'rejected', 'retired')`, "")
	if errCount != nil {
		return MemoryMaintenanceReport{}, fmt.Errorf("count reusable evolution candidates: %w", errCount)
	}
	report.ReusableProjectLessonsKept, errCount = s.countMemoryRows(`SELECT COUNT(*) FROM project_lessons WHERE support_count > 1 OR LOWER(confidence) = 'high'`, "")
	if errCount != nil {
		return MemoryMaintenanceReport{}, fmt.Errorf("count reusable project lessons: %w", errCount)
	}
	report.ProtectedTruthSkipped = report.ProtectedEvaluationRecords + report.ProtectedVerificationHistory
	return report, nil
}

func (s *Store) LoadMemoryArchiveStatus() (MemoryArchiveStatus, error) {
	status := MemoryArchiveStatus{
		Path:             s.Path(),
		ApplySupported:   true,
		Mode:             "reversible_archive",
		SupportedTables:  memoryArchiveSupportedTables(),
		ProtectedClasses: memoryArchiveProtectedClasses(),
	}
	if s == nil || s.db == nil {
		return status, nil
	}
	var tableCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'memory_archive_tombstones'`).Scan(&tableCount); err != nil {
		return MemoryArchiveStatus{}, fmt.Errorf("inspect archive schema: %w", err)
	}
	status.SchemaReady = tableCount > 0
	if !status.SchemaReady {
		return status, nil
	}
	var err error
	status.TombstoneCount, err = s.countMemoryRows(`SELECT COUNT(*) FROM memory_archive_tombstones`, "")
	if err != nil {
		return MemoryArchiveStatus{}, fmt.Errorf("count memory archive tombstones: %w", err)
	}
	status.ArchivedCount, err = s.countMemoryRows(`SELECT COUNT(*) FROM memory_archive_tombstones WHERE state = 'archived'`, "")
	if err != nil {
		return MemoryArchiveStatus{}, fmt.Errorf("count archived memory tombstones: %w", err)
	}
	status.RetiredCount, err = s.countMemoryRows(`SELECT COUNT(*) FROM memory_archive_tombstones WHERE state = 'retired'`, "")
	if err != nil {
		return MemoryArchiveStatus{}, fmt.Errorf("count retired memory tombstones: %w", err)
	}
	return status, nil
}

func (s *Store) ListMemoryArchiveTombstones(limit int) ([]MemoryArchiveTombstone, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT tombstone_id,
	original_table,
	original_key,
	original_summary,
	original_source,
	original_metadata_json,
	reason,
	state,
	archived_at,
	created_at
FROM memory_archive_tombstones
ORDER BY archived_at DESC, tombstone_id ASC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list memory archive tombstones: %w", err)
	}
	defer rows.Close()
	tombstones := make([]MemoryArchiveTombstone, 0)
	for rows.Next() {
		var tombstone MemoryArchiveTombstone
		var archivedAtRaw, createdAtRaw string
		if err := rows.Scan(
			&tombstone.TombstoneID,
			&tombstone.OriginalTable,
			&tombstone.OriginalKey,
			&tombstone.OriginalSummary,
			&tombstone.OriginalSource,
			&tombstone.OriginalMetadataJSON,
			&tombstone.Reason,
			&tombstone.State,
			&archivedAtRaw,
			&createdAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan memory archive tombstone: %w", err)
		}
		archivedAt, err := time.Parse(time.RFC3339Nano, archivedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse memory archive archived_at: %w", err)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse memory archive created_at: %w", err)
		}
		tombstone.ArchivedAt = archivedAt
		tombstone.CreatedAt = createdAt
		tombstones = append(tombstones, tombstone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory archive tombstones: %w", err)
	}
	return tombstones, nil
}

func (s *Store) LoadMemoryArchiveTombstone(tombstoneID string) (*MemoryArchiveTombstone, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	trimmedID := strings.TrimSpace(tombstoneID)
	if trimmedID == "" {
		return nil, fmt.Errorf("memory archive tombstone id is required")
	}
	row := s.db.QueryRow(`
SELECT tombstone_id,
	original_table,
	original_key,
	original_summary,
	original_source,
	original_metadata_json,
	reason,
	state,
	archived_at,
	created_at
FROM memory_archive_tombstones
WHERE tombstone_id = ?
`, trimmedID)
	tombstone, err := scanMemoryArchiveTombstone(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("memory archive tombstone %s not found", trimmedID)
		}
		return nil, err
	}
	return tombstone, nil
}

func (s *Store) PlanMemoryArchiveRestore(tombstoneID string) (MemoryArchiveRestoreDryRun, error) {
	report := MemoryArchiveRestoreDryRun{
		Path:                  s.Path(),
		RestoreSupported:      false,
		Guardrail:             "Dry-run only. No records are restored, inserted, updated, deleted, or vacuumed. Protected runtime and verifier truth remain out of scope.",
		ProtectedTruthSkipped: len(memoryArchiveProtectedClasses()),
	}
	tombstone, err := s.LoadMemoryArchiveTombstone(tombstoneID)
	if err != nil {
		return MemoryArchiveRestoreDryRun{}, err
	}
	if tombstone == nil {
		return report, nil
	}
	report.Tombstone = *tombstone
	report.TargetTable = tombstone.OriginalTable
	report.TargetKey = tombstone.OriginalKey
	report.TargetSummary = tombstone.OriginalSummary
	report.TargetSource = tombstone.OriginalSource
	report.TargetMetadataJSON = tombstone.OriginalMetadataJSON
	if !isMemoryArchiveSupportedTable(tombstone.OriginalTable) {
		report.Conflict = true
		report.ConflictReason = "tombstone targets unsupported or protected table"
		return report, nil
	}
	exists, err := s.memoryArchiveHotRecordExists(*tombstone)
	if err != nil {
		return MemoryArchiveRestoreDryRun{}, err
	}
	report.HotRecordExists = exists
	report.Conflict = exists
	if exists {
		report.ConflictReason = "matching hot advisory record already exists"
		return report, nil
	}
	report.RestoreSupported = true
	return report, nil
}

type memoryArchiveTombstoneScanner interface {
	Scan(dest ...any) error
}

func scanMemoryArchiveTombstone(scanner memoryArchiveTombstoneScanner) (*MemoryArchiveTombstone, error) {
	var tombstone MemoryArchiveTombstone
	var archivedAtRaw, createdAtRaw string
	if err := scanner.Scan(
		&tombstone.TombstoneID,
		&tombstone.OriginalTable,
		&tombstone.OriginalKey,
		&tombstone.OriginalSummary,
		&tombstone.OriginalSource,
		&tombstone.OriginalMetadataJSON,
		&tombstone.Reason,
		&tombstone.State,
		&archivedAtRaw,
		&createdAtRaw,
	); err != nil {
		return nil, err
	}
	archivedAt, err := time.Parse(time.RFC3339Nano, archivedAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse memory archive archived_at: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse memory archive created_at: %w", err)
	}
	tombstone.ArchivedAt = archivedAt
	tombstone.CreatedAt = createdAt
	return &tombstone, nil
}

func (s *Store) memoryArchiveHotRecordExists(tombstone MemoryArchiveTombstone) (bool, error) {
	metadata, err := memoryCandidateMetadataMap(tombstone.OriginalMetadataJSON)
	if err != nil {
		return false, err
	}
	var query string
	var args []any
	switch tombstone.OriginalTable {
	case "warm_lessons":
		taskID := memoryCandidateMetadataString(metadata, "task_id")
		kind := memoryCandidateMetadataString(metadata, "kind")
		if taskID == "" || kind == "" {
			return false, fmt.Errorf("invalid warm lesson archive metadata for key %q", tombstone.OriginalKey)
		}
		query = `SELECT COUNT(*) FROM warm_lessons WHERE task_id = ? AND kind = ? AND summary = ?`
		args = []any{taskID, kind, tombstone.OriginalSummary}
	case "evolution_candidates":
		taskID := memoryCandidateMetadataString(metadata, "task_id")
		kind := memoryCandidateMetadataString(metadata, "kind")
		if taskID == "" || kind == "" {
			return false, fmt.Errorf("invalid evolution candidate archive metadata for key %q", tombstone.OriginalKey)
		}
		query = `SELECT COUNT(*) FROM evolution_candidates WHERE task_id = ? AND kind = ? AND summary = ?`
		args = []any{taskID, kind, tombstone.OriginalSummary}
	case "project_lessons":
		kind := memoryCandidateMetadataString(metadata, "kind")
		if kind == "" {
			return false, fmt.Errorf("invalid project lesson archive metadata for key %q", tombstone.OriginalKey)
		}
		query = `SELECT COUNT(*) FROM project_lessons WHERE kind = ? AND summary = ?`
		args = []any{kind, tombstone.OriginalSummary}
	default:
		return false, nil
	}
	var count int
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect memory archive restore conflict: %w", err)
	}
	return count > 0, nil
}

func (s *Store) PlanMemoryRetirementCandidates(now time.Time) ([]MemoryRetirementCandidate, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	staleBefore := now.UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	candidates := make([]MemoryRetirementCandidate, 0)
	warmRows, err := s.db.Query(`
SELECT task_id, kind, summary, source, confidence, updated_at
FROM warm_lessons
WHERE LOWER(confidence) = 'low' OR updated_at < ?
ORDER BY task_id ASC, kind ASC, summary ASC
`, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("query warm lesson retirement candidates: %w", err)
	}
	defer warmRows.Close()
	for warmRows.Next() {
		var taskID, kind, summary, source, confidence, updatedAt string
		if err := warmRows.Scan(&taskID, &kind, &summary, &source, &confidence, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan warm lesson retirement candidate: %w", err)
		}
		metadata, err := memoryCandidateMetadata(map[string]any{"task_id": taskID, "kind": kind, "confidence": confidence, "updated_at": updatedAt})
		if err != nil {
			return nil, err
		}
		reason := "stale_or_low_confidence"
		candidates = append(candidates, newMemoryRetirementCandidate("warm_lessons", strings.Join([]string{taskID, kind, summary}, "|"), summary, source, metadata, reason))
	}
	if err := warmRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate warm lesson retirement candidates: %w", err)
	}
	evolutionRows, err := s.db.Query(`
SELECT task_id, kind, summary, source, priority, status, updated_at
FROM evolution_candidates
WHERE LOWER(status) IN ('closed', 'superseded', 'rejected', 'retired') OR updated_at < ?
ORDER BY task_id ASC, kind ASC, summary ASC
`, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("query evolution retirement candidates: %w", err)
	}
	defer evolutionRows.Close()
	for evolutionRows.Next() {
		var taskID, kind, summary, source, priority, status, updatedAt string
		if err := evolutionRows.Scan(&taskID, &kind, &summary, &source, &priority, &status, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan evolution retirement candidate: %w", err)
		}
		metadata, err := memoryCandidateMetadata(map[string]any{"task_id": taskID, "kind": kind, "priority": priority, "status": status, "updated_at": updatedAt})
		if err != nil {
			return nil, err
		}
		reason := "closed_or_stale_candidate"
		candidates = append(candidates, newMemoryRetirementCandidate("evolution_candidates", strings.Join([]string{taskID, kind, summary}, "|"), summary, source, metadata, reason))
	}
	if err := evolutionRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evolution retirement candidates: %w", err)
	}
	projectRows, err := s.db.Query(`
SELECT source_task_id, kind, summary, source, confidence, support_count, support_tasks_json, updated_at
FROM project_lessons
WHERE support_count <= 1 AND LOWER(confidence) IN ('', 'low', 'medium')
ORDER BY kind ASC, summary ASC
`)
	if err != nil {
		return nil, fmt.Errorf("query project lesson retirement candidates: %w", err)
	}
	defer projectRows.Close()
	for projectRows.Next() {
		var sourceTaskID, kind, summary, source, confidence, supportTasksJSON, updatedAt string
		var supportCount int
		if err := projectRows.Scan(&sourceTaskID, &kind, &summary, &source, &confidence, &supportCount, &supportTasksJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan project lesson retirement candidate: %w", err)
		}
		metadata, err := memoryCandidateMetadata(map[string]any{"source_task_id": sourceTaskID, "kind": kind, "confidence": confidence, "support_count": supportCount, "support_tasks_json": supportTasksJSON, "updated_at": updatedAt})
		if err != nil {
			return nil, err
		}
		reason := "low_support_project_lesson"
		candidates = append(candidates, newMemoryRetirementCandidate("project_lessons", strings.Join([]string{kind, summary}, "|"), summary, source, metadata, reason))
	}
	if err := projectRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project lesson retirement candidates: %w", err)
	}
	return candidates, nil
}

func (s *Store) ApplyMemoryArchive(now time.Time) (MemoryArchiveApplyReport, error) {
	report := MemoryArchiveApplyReport{
		Path:             s.Path(),
		Mode:             "archive_apply",
		SupportedTables:  memoryArchiveSupportedTables(),
		ProtectedClasses: memoryArchiveProtectedClasses(),
		Guardrail:        "Advisory records are tombstoned before leaving hot recall. No protected truth is archived, deleted, or vacuumed by this path.",
	}
	if s == nil || s.db == nil {
		return report, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	candidates, err := s.PlanMemoryRetirementCandidates(now)
	if err != nil {
		return MemoryArchiveApplyReport{}, err
	}
	report.PlannedCandidates = len(candidates)
	tx, err := s.db.Begin()
	if err != nil {
		return MemoryArchiveApplyReport{}, fmt.Errorf("begin memory archive apply: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, candidate := range candidates {
		if !isMemoryArchiveSupportedTable(candidate.OriginalTable) {
			report.ProtectedSkipped++
			continue
		}
		inserted, err := insertMemoryArchiveTombstone(tx, candidate, now)
		if err != nil {
			return MemoryArchiveApplyReport{}, err
		}
		if inserted {
			report.Archived++
		} else {
			report.IdempotentSkipped++
		}
		deleted, err := retireMemoryArchiveCandidate(tx, candidate)
		if err != nil {
			return MemoryArchiveApplyReport{}, err
		}
		report.RetiredFromHot += deleted
	}
	if err := tx.Commit(); err != nil {
		return MemoryArchiveApplyReport{}, fmt.Errorf("commit memory archive apply: %w", err)
	}
	committed = true
	return report, nil
}

func insertMemoryArchiveTombstone(tx *sql.Tx, candidate MemoryRetirementCandidate, now time.Time) (bool, error) {
	result, err := tx.Exec(`
INSERT INTO memory_archive_tombstones (
	tombstone_id,
	original_table,
	original_key,
	original_summary,
	original_source,
	original_metadata_json,
	reason,
	state,
	archived_at,
	created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'retired', ?, ?)
ON CONFLICT(tombstone_id) DO NOTHING;
`, candidate.TombstoneID, candidate.OriginalTable, candidate.OriginalKey, candidate.OriginalSummary, candidate.OriginalSource, candidate.OriginalMetadataJSON, candidate.Reason, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("insert memory archive tombstone: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect memory archive tombstone insert: %w", err)
	}
	return affected > 0, nil
}

func retireMemoryArchiveCandidate(tx *sql.Tx, candidate MemoryRetirementCandidate) (int, error) {
	metadata, err := memoryCandidateMetadataMap(candidate.OriginalMetadataJSON)
	if err != nil {
		return 0, err
	}
	var result sql.Result
	switch candidate.OriginalTable {
	case "warm_lessons":
		taskID := memoryCandidateMetadataString(metadata, "task_id")
		kind := memoryCandidateMetadataString(metadata, "kind")
		if taskID == "" || kind == "" {
			return 0, fmt.Errorf("invalid warm lesson archive metadata for key %q", candidate.OriginalKey)
		}
		result, err = tx.Exec(`DELETE FROM warm_lessons WHERE task_id = ? AND kind = ? AND summary = ?`, taskID, kind, candidate.OriginalSummary)
	case "evolution_candidates":
		taskID := memoryCandidateMetadataString(metadata, "task_id")
		kind := memoryCandidateMetadataString(metadata, "kind")
		if taskID == "" || kind == "" {
			return 0, fmt.Errorf("invalid evolution candidate archive metadata for key %q", candidate.OriginalKey)
		}
		result, err = tx.Exec(`DELETE FROM evolution_candidates WHERE task_id = ? AND kind = ? AND summary = ?`, taskID, kind, candidate.OriginalSummary)
	case "project_lessons":
		kind := memoryCandidateMetadataString(metadata, "kind")
		if kind == "" {
			return 0, fmt.Errorf("invalid project lesson archive metadata for key %q", candidate.OriginalKey)
		}
		result, err = tx.Exec(`DELETE FROM project_lessons WHERE kind = ? AND summary = ?`, kind, candidate.OriginalSummary)
	default:
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("retire memory archive candidate: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inspect memory archive retirement: %w", err)
	}
	return int(affected), nil
}

func memoryCandidateMetadataMap(encoded string) (map[string]any, error) {
	values := map[string]any{}
	if strings.TrimSpace(encoded) == "" {
		return values, nil
	}
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, fmt.Errorf("decode memory candidate metadata: %w", err)
	}
	return values, nil
}

func memoryCandidateMetadataString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func memoryArchiveSupportedTables() []string {
	return []string{
		"warm_lessons",
		"evolution_candidates",
		"project_lessons",
	}
}

func memoryArchiveProtectedClasses() []string {
	return []string{
		"runtime_lifecycle",
		"verification_history",
		"approval_evidence",
		"retry_evidence",
		"node_work_evidence",
	}
}

func isMemoryArchiveSupportedTable(table string) bool {
	for _, supported := range memoryArchiveSupportedTables() {
		if table == supported {
			return true
		}
	}
	return false
}

func newMemoryRetirementCandidate(table string, key string, summary string, source string, metadataJSON string, reason string) MemoryRetirementCandidate {
	candidate := MemoryRetirementCandidate{
		OriginalTable:        strings.TrimSpace(table),
		OriginalKey:          strings.TrimSpace(key),
		OriginalSummary:      strings.TrimSpace(summary),
		OriginalSource:       strings.TrimSpace(source),
		OriginalMetadataJSON: strings.TrimSpace(metadataJSON),
		Reason:               strings.TrimSpace(reason),
	}
	candidate.TombstoneID = memoryTombstoneID(candidate)
	return candidate
}

func memoryCandidateMetadata(values map[string]any) (string, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode memory candidate metadata: %w", err)
	}
	return string(encoded), nil
}

func memoryTombstoneID(candidate MemoryRetirementCandidate) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		candidate.OriginalTable,
		candidate.OriginalKey,
		candidate.Reason,
	}, "\x00")))
	return "memory-tombstone-" + hex.EncodeToString(digest[:])[:16]
}

func (s *Store) memoryTableStatus(table string, timeColumn string) (MemoryTableStatus, error) {
	status := MemoryTableStatus{Name: table}
	if s == nil || s.db == nil {
		return status, nil
	}
	query := fmt.Sprintf("SELECT COUNT(*), COALESCE(MIN(%s), ''), COALESCE(MAX(%s), '') FROM %s", timeColumn, timeColumn, table)
	row := s.db.QueryRow(query)
	if err := row.Scan(&status.Rows, &status.OldestRaw, &status.NewestRaw); err != nil {
		return MemoryTableStatus{}, fmt.Errorf("query memory table %s: %w", table, err)
	}
	return status, nil
}

func (s *Store) countMemoryRows(query string, value string) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var count int
	var err error
	if value == "" {
		err = s.db.QueryRow(query).Scan(&count)
	} else {
		err = s.db.QueryRow(query, value).Scan(&count)
	}
	if err != nil {
		return 0, err
	}
	return count, nil
}
