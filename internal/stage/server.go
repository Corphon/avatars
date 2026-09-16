package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"avatars/internal/app"
	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/localhttp"
	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	runtimepkg "avatars/internal/runtime"
	"avatars/internal/skills"
)

type Server struct {
	store             *events.Store
	engine            *runtimepkg.Engine
	mux               *http.ServeMux
	assetDir          string
	archivedHistory   []events.Envelope
	taskFeedback      *OperatorTaskFeedbackSnapshot
	skillGovernance   *OperatorSkillGovernanceSnapshot
	configuredLLM     llm.ProviderStatus
	configuredWarning string
	authToken         string
}

type OperatorTelemetrySnapshot struct {
	Mode             string `json:"mode"`
	Status           string `json:"status"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	LLMRequests      int    `json:"llm_requests"`
	LLMCompletions   int    `json:"llm_completions"`
	LLMFailures      int    `json:"llm_failures"`
	LLMFallbacks     int    `json:"llm_fallbacks"`
	ToolRequests     int    `json:"tool_requests"`
	ToolCompletions  int    `json:"tool_completions"`
	ToolFailures     int    `json:"tool_failures"`
	VerificationRuns int    `json:"verification_runs"`
	DurationMillis   int    `json:"duration_ms"`
	UsageKnown       bool   `json:"usage_known"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	CostMicros       int64  `json:"cost_micros"`
}

type OperatorSnapshot struct {
	ConfiguredLLM        *llm.ProviderStatus              `json:"configured_llm,omitempty"`
	ConfiguredLLMWarning string                           `json:"configured_llm_warning,omitempty"`
	LatestRunLLM         *llm.ProviderStatus              `json:"latest_run_llm,omitempty"`
	LatestRunLLMWarning  string                           `json:"latest_run_llm_warning,omitempty"`
	LatestTelemetry      *OperatorTelemetrySnapshot       `json:"latest_telemetry,omitempty"`
	TaskFeedback         *OperatorTaskFeedbackSnapshot    `json:"task_feedback,omitempty"`
	SkillGovernance      *OperatorSkillGovernanceSnapshot `json:"skill_governance,omitempty"`
	LLMDiff              *OperatorDiffSnapshot            `json:"llm_diff,omitempty"`
	SelectedRunID        string                           `json:"selected_run_id,omitempty"`
	AvailableRuns        []OperatorRunSnapshot            `json:"available_runs,omitempty"`
	LatestSummary        string                           `json:"latest_summary,omitempty"`
	LatestRunStatus      string                           `json:"latest_run_status,omitempty"`
}

type OperatorTaskFeedbackSnapshot struct {
	Collaboration                        *OperatorTaskCollaborationSnapshot    `json:"collaboration,omitempty"`
	Sections                             []OperatorTaskFeedbackSectionSnapshot `json:"sections,omitempty"`
	TaskStatus                           string                                `json:"task_status,omitempty"`
	FinalityReadiness                    string                                `json:"finality_readiness,omitempty"`
	FinalityBlockerCount                 int                                   `json:"finality_blocker_count,omitempty"`
	SuggestedCommands                    []string                              `json:"suggested_commands,omitempty"`
	RemediationProposals                 []OperatorTaskRemediationProposal     `json:"remediation_proposals,omitempty"`
	Verification                         *OperatorVerificationSnapshot         `json:"verification,omitempty"`
	EvaluationCount                      int                                   `json:"evaluation_count"`
	WarmLessonCount                      int                                   `json:"warm_lesson_count"`
	EvolutionCount                       int                                   `json:"evolution_count"`
	ProjectLessonCount                   int                                   `json:"project_lesson_count"`
	ExecutionChain                       []string                              `json:"execution_chain,omitempty"`
	LatestAvatarReport                   string                                `json:"latest_avatar_report,omitempty"`
	LatestAvatarReportRoute              string                                `json:"latest_avatar_report_route,omitempty"`
	LatestAvatarAsk                      string                                `json:"latest_avatar_ask,omitempty"`
	LatestAvatarAskRoute                 string                                `json:"latest_avatar_ask_route,omitempty"`
	LatestAvatarAskStatus                string                                `json:"latest_avatar_ask_status,omitempty"`
	LatestAvatarFollowUp                 string                                `json:"latest_avatar_follow_up,omitempty"`
	LatestAvatarFollowUpRoute            string                                `json:"latest_avatar_follow_up_route,omitempty"`
	LatestAvatarChallenge                string                                `json:"latest_avatar_challenge,omitempty"`
	LatestAvatarChallengeRoute           string                                `json:"latest_avatar_challenge_route,omitempty"`
	LatestAvatarSummary                  string                                `json:"latest_avatar_summary,omitempty"`
	LatestAvatarSummaryRoute             string                                `json:"latest_avatar_summary_route,omitempty"`
	LatestNonPassVerification            string                                `json:"latest_non_pass_verification,omitempty"`
	LatestNonPassFollowUp                string                                `json:"latest_non_pass_follow_up,omitempty"`
	ReverifyStatus                       string                                `json:"reverify_status,omitempty"`
	LatestReverifyAttempt                string                                `json:"latest_reverify_attempt,omitempty"`
	LatestReverifyAttemptProposalID      string                                `json:"latest_reverify_attempt_proposal_id,omitempty"`
	LatestReverifyAttemptTargets         []string                              `json:"latest_reverify_attempt_targets,omitempty"`
	LatestReverifyRemediation            string                                `json:"latest_reverify_remediation,omitempty"`
	LatestReverifyRemediationProposalID  string                                `json:"latest_reverify_remediation_proposal_id,omitempty"`
	LatestReverifyRemediationTargets     []string                              `json:"latest_reverify_remediation_targets,omitempty"`
	LatestReverifyClosure                string                                `json:"latest_reverify_closure,omitempty"`
	WorkflowNodeNextAction               string                                `json:"workflow_node_next_action,omitempty"`
	WorkflowNodeVerifierReportPath       string                                `json:"workflow_node_verifier_report_path,omitempty"`
	LatestApprovalRequired               string                                `json:"latest_approval_required,omitempty"`
	LatestApprovalRequiredSource         string                                `json:"latest_approval_required_source,omitempty"`
	LatestApprovalRequiredKey            string                                `json:"latest_approval_required_key,omitempty"`
	LatestApprovalRequiredMode           string                                `json:"latest_approval_required_mode,omitempty"`
	LatestApprovalReplay                 string                                `json:"latest_approval_replay,omitempty"`
	LatestApprovalReplaySource           string                                `json:"latest_approval_replay_source,omitempty"`
	LatestApprovalReplayKey              string                                `json:"latest_approval_replay_key,omitempty"`
	LatestApprovalReplayTranscript       string                                `json:"latest_approval_replay_transcript,omitempty"`
	LatestApprovalContinuation           string                                `json:"latest_approval_continuation,omitempty"`
	LatestApprovalContinuationSource     string                                `json:"latest_approval_continuation_source,omitempty"`
	LatestApprovalContinuationKey        string                                `json:"latest_approval_continuation_key,omitempty"`
	LatestApprovalContinuationTranscript string                                `json:"latest_approval_continuation_transcript,omitempty"`
	LatestApprovalContinuationRunID      string                                `json:"latest_approval_continuation_run_id,omitempty"`
	LatestApprovalContinuationTaskID     string                                `json:"latest_approval_continuation_task_id,omitempty"`
	PendingApprovalCount                 int                                   `json:"pending_approval_count,omitempty"`
	PendingApprovals                     []OperatorTaskPendingApprovalSnapshot `json:"pending_approvals,omitempty"`
	LatestPermissionDenial               string                                `json:"latest_permission_denial,omitempty"`
	LatestPermissionDenialSource         string                                `json:"latest_permission_denial_source,omitempty"`
	LatestEvaluation                     string                                `json:"latest_evaluation,omitempty"`
	LatestWarmLesson                     string                                `json:"latest_warm_lesson,omitempty"`
	LatestEvolution                      string                                `json:"latest_evolution,omitempty"`
	TopProjectLesson                     string                                `json:"top_project_lesson,omitempty"`
}

type OperatorTaskRemediationProposal struct {
	ProposalID      string   `json:"proposal_id,omitempty"`
	Kind            string   `json:"kind,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	Intent          string   `json:"intent,omitempty"`
	ExpectedTargets []string `json:"expected_targets,omitempty"`
	FollowUpCommand string   `json:"follow_up_command,omitempty"`
}

type OperatorTaskPendingApprovalSnapshot struct {
	ApprovalKey    string `json:"approval_key,omitempty"`
	Tool           string `json:"tool,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Source         string `json:"source,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	Summary        string `json:"summary,omitempty"`
}

type OperatorTaskCollaborationSnapshot struct {
	ExecutionChain             []string `json:"execution_chain,omitempty"`
	FocusCue                   string   `json:"focus_cue,omitempty"`
	LatestAvatarReport         string   `json:"latest_avatar_report,omitempty"`
	LatestAvatarReportRoute    string   `json:"latest_avatar_report_route,omitempty"`
	LatestAvatarAsk            string   `json:"latest_avatar_ask,omitempty"`
	LatestAvatarAskRoute       string   `json:"latest_avatar_ask_route,omitempty"`
	LatestAvatarAskStatus      string   `json:"latest_avatar_ask_status,omitempty"`
	LatestAvatarFollowUp       string   `json:"latest_avatar_follow_up,omitempty"`
	LatestAvatarFollowUpRoute  string   `json:"latest_avatar_follow_up_route,omitempty"`
	LatestAvatarChallenge      string   `json:"latest_avatar_challenge,omitempty"`
	LatestAvatarChallengeRoute string   `json:"latest_avatar_challenge_route,omitempty"`
	LatestAvatarSummary        string   `json:"latest_avatar_summary,omitempty"`
	LatestAvatarSummaryRoute   string   `json:"latest_avatar_summary_route,omitempty"`
}

type OperatorTaskFeedbackSectionSnapshot struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Summary string `json:"summary"`
}

type OperatorSkillGovernanceSnapshot struct {
	Scope                        string                                    `json:"scope"`
	TrackedSkills                int                                       `json:"tracked_skills"`
	GeneratedCount               int                                       `json:"generated_count"`
	ApprovedCount                int                                       `json:"approved_count"`
	ArchivedCount                int                                       `json:"archived_count"`
	DisabledCount                int                                       `json:"disabled_count"`
	AlertCount                   int                                       `json:"alert_count"`
	BlockedInvariantCount        int                                       `json:"blocked_invariant_count"`
	CurrentGapCount              int                                       `json:"current_gap_count"`
	DriftHotspotCount            int                                       `json:"drift_hotspot_count"`
	MissingCurrentCount          int                                       `json:"missing_current_count"`
	UntrackedCurrentCount        int                                       `json:"untracked_current_count"`
	ErrorCount                   int                                       `json:"error_count"`
	WarningCount                 int                                       `json:"warning_count"`
	OverviewCommand              string                                    `json:"overview_command,omitempty"`
	TopAlert                     string                                    `json:"top_alert,omitempty"`
	TopAlertCommand              string                                    `json:"top_alert_command,omitempty"`
	TopHotspot                   string                                    `json:"top_hotspot,omitempty"`
	TopHotspotCommand            string                                    `json:"top_hotspot_command,omitempty"`
	TopBlockedInvariant          string                                    `json:"top_blocked_invariant,omitempty"`
	TopBlockedInvariantCommand   string                                    `json:"top_blocked_invariant_command,omitempty"`
	TopCurrentGap                string                                    `json:"top_current_gap,omitempty"`
	TopCurrentGapCommand         string                                    `json:"top_current_gap_command,omitempty"`
	TopDriftHotspot              string                                    `json:"top_drift_hotspot,omitempty"`
	TopDriftHotspotCommand       string                                    `json:"top_drift_hotspot_command,omitempty"`
	Alerts                       []OperatorSkillGovernanceAlert            `json:"alerts,omitempty"`
	Hotspots                     []OperatorSkillGovernanceHotspot          `json:"hotspots,omitempty"`
	BlockedInvariants            []OperatorSkillGovernanceInvariant        `json:"blocked_invariants,omitempty"`
	CurrentGaps                  []OperatorSkillGovernanceCurrentGap       `json:"current_gaps,omitempty"`
	DriftHotspots                []OperatorSkillGovernanceDriftHotspot     `json:"drift_hotspots,omitempty"`
	SelectionSections            []OperatorSkillGovernanceSelectionSection `json:"selection_sections,omitempty"`
	SelectionItems               []OperatorSkillGovernanceSelectionItem    `json:"selection_items,omitempty"`
	Details                      []OperatorSkillGovernanceDetail           `json:"details,omitempty"`
	SelectedItemKey              string                                    `json:"selected_item_key,omitempty"`
	SelectedItemKind             string                                    `json:"selected_item_kind,omitempty"`
	SelectedBlockedInvariantPath string                                    `json:"selected_blocked_invariant_path,omitempty"`
	SelectedBlockedInvariant     *OperatorSkillGovernanceInvariant         `json:"selected_blocked_invariant,omitempty"`
	SelectedCurrentGapPath       string                                    `json:"selected_current_gap_path,omitempty"`
	SelectedCurrentGap           *OperatorSkillGovernanceCurrentGap        `json:"selected_current_gap,omitempty"`
	SelectedDriftHotspotPath     string                                    `json:"selected_drift_hotspot_path,omitempty"`
	SelectedDriftHotspot         *OperatorSkillGovernanceDriftHotspot      `json:"selected_drift_hotspot,omitempty"`
	SelectedDetailPath           string                                    `json:"selected_detail_path,omitempty"`
	SelectedDetail               *OperatorSkillGovernanceDetail            `json:"selected_detail,omitempty"`
}

type OperatorSkillGovernanceSelectionItem struct {
	Key             string `json:"key"`
	Kind            string `json:"kind"`
	Section         string `json:"section,omitempty"`
	Path            string `json:"path,omitempty"`
	SkillName       string `json:"skill_name"`
	Summary         string `json:"summary,omitempty"`
	FollowUpCommand string `json:"follow_up_command,omitempty"`
	PriorityScore   int    `json:"priority_score,omitempty"`
	PriorityLabel   string `json:"priority_label,omitempty"`
}

type OperatorSkillGovernanceSelectionSection struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type OperatorSkillGovernanceAlert struct {
	Severity        string `json:"severity"`
	SkillName       string `json:"skill_name"`
	Reason          string `json:"reason"`
	Repairable      *bool  `json:"repairable,omitempty"`
	BlockedBy       string `json:"blocked_by,omitempty"`
	FollowUpCommand string `json:"follow_up_command,omitempty"`
}

type OperatorSkillGovernanceHotspot struct {
	SkillName       string `json:"skill_name"`
	CurrentState    string `json:"current_state"`
	TransitionCount int    `json:"transition_count"`
	FollowUpCommand string `json:"follow_up_command,omitempty"`
}

type OperatorSkillGovernanceCurrentGap struct {
	Category        string `json:"category"`
	SkillName       string `json:"skill_name"`
	Path            string `json:"path,omitempty"`
	CurrentState    string `json:"current_state,omitempty"`
	RecordedState   string `json:"recorded_state,omitempty"`
	RecordedAction  string `json:"recorded_action,omitempty"`
	ContentDigest   string `json:"content_digest,omitempty"`
	SourceTaskID    string `json:"source_task_id,omitempty"`
	SourceRunID     string `json:"source_run_id,omitempty"`
	FollowUpCommand string `json:"follow_up_command,omitempty"`
}

type OperatorSkillGovernanceDriftHotspot struct {
	SkillName       string                         `json:"skill_name"`
	Path            string                         `json:"path,omitempty"`
	CurrentState    string                         `json:"current_state,omitempty"`
	Drifts          []OperatorSkillGovernanceDrift `json:"drifts,omitempty"`
	FollowUpCommand string                         `json:"follow_up_command,omitempty"`
}

type OperatorSkillGovernanceInvariant struct {
	SkillName           string                               `json:"skill_name"`
	Path                string                               `json:"path,omitempty"`
	CurrentState        string                               `json:"current_state,omitempty"`
	Reason              string                               `json:"reason"`
	BlockedBy           string                               `json:"blocked_by,omitempty"`
	FollowUpCommand     string                               `json:"follow_up_command,omitempty"`
	RecentTransitions   []OperatorSkillGovernanceTransition  `json:"recent_transitions,omitempty"`
	RelatedDrifts       []OperatorSkillGovernanceDrift       `json:"related_drifts,omitempty"`
	RecentLedgerEntries []OperatorSkillGovernanceLedgerEntry `json:"recent_ledger_entries,omitempty"`
}

type OperatorSkillGovernanceDrift struct {
	Category string `json:"category"`
	Summary  string `json:"summary"`
}

type OperatorSkillGovernanceLedgerEntry struct {
	At           string `json:"at,omitempty"`
	Action       string `json:"action"`
	FromState    string `json:"from_state,omitempty"`
	ToState      string `json:"to_state,omitempty"`
	Path         string `json:"path,omitempty"`
	SourceTaskID string `json:"source_task_id,omitempty"`
	SourceRunID  string `json:"source_run_id,omitempty"`
}

type OperatorSkillGovernanceDetail struct {
	SkillName         string                              `json:"skill_name"`
	CurrentState      string                              `json:"current_state,omitempty"`
	Path              string                              `json:"path,omitempty"`
	FollowUpCommand   string                              `json:"follow_up_command,omitempty"`
	Alerts            []OperatorSkillGovernanceAlert      `json:"alerts,omitempty"`
	RecentTransitions []OperatorSkillGovernanceTransition `json:"recent_transitions,omitempty"`
}

type OperatorSkillGovernanceTransition struct {
	At        string `json:"at,omitempty"`
	Action    string `json:"action"`
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state,omitempty"`
}

type OperatorVerificationSnapshot struct {
	Tool            string   `json:"tool,omitempty"`
	Verdict         string   `json:"verdict,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	ReportPath      string   `json:"report_path,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Checks          []string `json:"checks,omitempty"`
	FollowUpCommand string   `json:"follow_up_command,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

type OperatorDiffSnapshot struct {
	HasDiff      bool                `json:"has_diff"`
	ChangedCount int                 `json:"changed_count"`
	TotalCount   int                 `json:"total_count"`
	Lines        []string            `json:"lines,omitempty"`
	Fields       []OperatorDiffField `json:"fields,omitempty"`
}

type OperatorDiffField struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Configured string `json:"configured,omitempty"`
	ViewedRun  string `json:"viewed_run,omitempty"`
	Changed    bool   `json:"changed"`
	State      string `json:"state"`
}

type OperatorRunSnapshot struct {
	RunID            string `json:"run_id"`
	TaskID           string `json:"task_id,omitempty"`
	Status           string `json:"status,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Input            string `json:"input,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	CompletedAt      string `json:"completed_at,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
	DurationMillis   int    `json:"duration_ms"`
	EventCount       int    `json:"event_count"`
	LLMRequests      int    `json:"llm_requests"`
	ToolRequests     int    `json:"tool_requests"`
	VerificationRuns int    `json:"verification_runs"`
	LastSequence     uint64 `json:"last_sequence"`
}

func NewServer(store *events.Store, engine *runtimepkg.Engine, configured llm.ProviderStatus, configuredWarning string) (*Server, error) {
	assetDir := app.ResolveRuntimePath(filepath.Join("web", "stage"))
	server := &Server{store: store, engine: engine, mux: http.NewServeMux(), assetDir: assetDir, configuredLLM: configured, configuredWarning: configuredWarning}
	server.routes()
	return server, nil
}

func (s *Server) WithArchivedHistory(history []events.Envelope) *Server {
	if s == nil {
		return nil
	}
	s.archivedHistory = append([]events.Envelope(nil), history...)
	return s
}

func (s *Server) WithTaskFeedback(taskID string, baseStatus string, snapshot memstore.Snapshot) *Server {
	if s == nil {
		return nil
	}
	s.taskFeedback = buildTaskFeedbackSnapshot(taskID, baseStatus, snapshot)
	return s
}

func (s *Server) WithSkillGovernance(summary skills.GovernanceSummary, reconciliation skills.GovernanceReconciliation, ledger skills.GovernanceLedger) *Server {
	if s == nil {
		return nil
	}
	s.skillGovernance = buildSkillGovernanceSnapshot(summary, reconciliation, ledger)
	return s
}

func (s *Server) routes() {
	fileServer := http.FileServer(http.Dir(s.assetDir))
	s.mux.Handle("/app.js", fileServer)
	s.mux.Handle("/styles.css", fileServer)
	s.mux.HandleFunc("/api/events", s.handleHistory)
	s.mux.HandleFunc("/api/events/stream", s.handleSSE)
	s.mux.HandleFunc("/api/operator", s.handleOperator)
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.assetDir, "index.html"))
	})
}

func (s *Server) WithAuthToken(token string) *Server {
	if s == nil {
		return nil
	}
	s.authToken = strings.TrimSpace(token)
	return s
}

func (s *Server) ListenAndServe(ctx context.Context, addr string, demoTask string) error {
	addr = localhttp.NormalizeAddr(addr)
	token := strings.TrimSpace(s.authToken)
	if token == "" {
		token = localhttp.ResolveToken(localhttp.StageTokenEnv)
		s.authToken = token
	}
	if strings.TrimSpace(demoTask) != "" && s.engine != nil {
		go func() {
			_, _ = s.engine.Run(ctx, demoTask)
		}()
	}

	handler := localhttp.Middleware(token, s.mux)
	server := &http.Server{Addr: addr, Handler: handler}
	localhttp.LogListen("Stage server", addr, token)
	return server.ListenAndServe()
}

func (s *Server) Handler() http.Handler {
	if strings.TrimSpace(s.authToken) == "" {
		return s.mux
	}
	return localhttp.Middleware(s.authToken, s.mux)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(filterHistoryByRun(s.history(), r.URL.Query().Get("run_id")))
}

func (s *Server) handleOperator(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.operatorSnapshot(r.URL.Query().Get("run_id"), r.URL.Query().Get("skill_path"), r.URL.Query().Get("skill_item")))
}

func (s *Server) operatorSnapshot(selectedRunID string, selectedSkillPath string, selectedSkillItem string) OperatorSnapshot {
	snapshot := OperatorSnapshot{}
	if s.configuredLLM.Name != "" {
		configured := s.configuredLLM
		snapshot.ConfiguredLLM = &configured
	}
	snapshot.ConfiguredLLMWarning = s.configuredWarning
	snapshot.TaskFeedback = s.taskFeedback
	snapshot.SkillGovernance = selectSkillGovernanceDetail(s.skillGovernance, selectedSkillPath, selectedSkillItem)
	history := s.history()
	snapshot.AvailableRuns = collectRunSnapshots(history)
	if selectedRunID == "" {
		selectedRunID = latestRunID(history)
	}
	snapshot.SelectedRunID = selectedRunID
	history = filterHistoryByRun(history, selectedRunID)
	for index := len(history) - 1; index >= 0; index-- {
		event := history[index]
		switch event.Type {
		case "llm.status_updated":
			if snapshot.LatestRunLLM == nil {
				status, warning := providerStatusFromPayload(event.Payload)
				snapshot.LatestRunLLM = status
				snapshot.LatestRunLLMWarning = warning
			}
		case "telemetry.updated":
			if snapshot.LatestTelemetry == nil {
				snapshot.LatestTelemetry = telemetryFromPayload(event.Payload)
			}
		case "run.completed":
			if snapshot.LatestSummary == "" {
				snapshot.LatestSummary = stringValue(event.Payload, "summary")
				snapshot.LatestRunStatus = runStatusValue(event.Payload)
			}
		}
	}
	if snapshot.LatestRunStatus == "" && snapshot.SelectedRunID != "" {
		if selectedRun := findRunSnapshot(snapshot.AvailableRuns, snapshot.SelectedRunID); selectedRun != nil {
			snapshot.LatestRunStatus = selectedRun.Status
			if snapshot.LatestSummary == "" {
				snapshot.LatestSummary = selectedRun.Summary
			}
		}
	}
	snapshot.LLMDiff = diffOperatorLLM(snapshot.ConfiguredLLM, snapshot.ConfiguredLLMWarning, snapshot.LatestRunLLM, snapshot.LatestRunLLMWarning)
	return snapshot
}

func selectSkillGovernanceDetail(snapshot *OperatorSkillGovernanceSnapshot, selectedPath string, selectedItemKey string) *OperatorSkillGovernanceSnapshot {
	if snapshot == nil {
		return nil
	}
	cloned := *snapshot
	trimmedSelectedPath := strings.TrimSpace(selectedPath)
	trimmedSelectedItemKey := normalizeGovernanceSelectionItemKey(selectedItemKey)
	if trimmedSelectedItemKey != "" {
		for index := range snapshot.Details {
			detail := snapshot.Details[index]
			if governanceSelectionItemKey("detail", detail.Path, "") != trimmedSelectedItemKey {
				continue
			}
			selectedDetail := detail
			cloned.SelectedItemKey = trimmedSelectedItemKey
			cloned.SelectedItemKind = "detail"
			cloned.SelectedDetailPath = detail.Path
			cloned.SelectedDetail = &selectedDetail
			return &cloned
		}
		for index := range snapshot.BlockedInvariants {
			entry := snapshot.BlockedInvariants[index]
			if governanceSelectionItemKey("blocked-invariant", entry.Path, "") != trimmedSelectedItemKey {
				continue
			}
			selectedBlockedInvariant := entry
			cloned.SelectedItemKey = trimmedSelectedItemKey
			cloned.SelectedItemKind = "blocked-invariant"
			cloned.SelectedBlockedInvariantPath = entry.Path
			cloned.SelectedBlockedInvariant = &selectedBlockedInvariant
			return &cloned
		}
		for index := range snapshot.CurrentGaps {
			entry := snapshot.CurrentGaps[index]
			if governanceSelectionItemKey("current-gap", entry.Path, entry.Category) != trimmedSelectedItemKey {
				continue
			}
			selectedCurrentGap := entry
			cloned.SelectedItemKey = trimmedSelectedItemKey
			cloned.SelectedItemKind = "current-gap"
			cloned.SelectedCurrentGapPath = entry.Path
			cloned.SelectedCurrentGap = &selectedCurrentGap
			return &cloned
		}
		for index := range snapshot.DriftHotspots {
			entry := snapshot.DriftHotspots[index]
			if governanceSelectionItemKey("drift-hotspot", entry.Path, "") != trimmedSelectedItemKey {
				continue
			}
			selectedDriftHotspot := entry
			cloned.SelectedItemKey = trimmedSelectedItemKey
			cloned.SelectedItemKind = "drift-hotspot"
			cloned.SelectedDriftHotspotPath = entry.Path
			cloned.SelectedDriftHotspot = &selectedDriftHotspot
			return &cloned
		}
	}
	if len(snapshot.Details) > 0 {
		for index := range snapshot.Details {
			detail := snapshot.Details[index]
			if trimmedSelectedPath != "" && !sameGovernancePath(detail.Path, trimmedSelectedPath) {
				continue
			}
			selectedDetail := detail
			cloned.SelectedItemKey = governanceSelectionItemKey("detail", detail.Path, "")
			cloned.SelectedItemKind = "detail"
			cloned.SelectedDetailPath = detail.Path
			cloned.SelectedDetail = &selectedDetail
			if trimmedSelectedPath != "" {
				break
			}
			return &cloned
		}
	}
	if len(snapshot.BlockedInvariants) > 0 {
		for index := range snapshot.BlockedInvariants {
			entry := snapshot.BlockedInvariants[index]
			if trimmedSelectedPath != "" && !sameGovernancePath(entry.Path, trimmedSelectedPath) {
				continue
			}
			selectedBlockedInvariant := entry
			cloned.SelectedItemKey = governanceSelectionItemKey("blocked-invariant", entry.Path, "")
			cloned.SelectedItemKind = "blocked-invariant"
			cloned.SelectedBlockedInvariantPath = entry.Path
			cloned.SelectedBlockedInvariant = &selectedBlockedInvariant
			if trimmedSelectedPath != "" {
				break
			}
			break
		}
	}
	if len(snapshot.CurrentGaps) > 0 {
		for index := range snapshot.CurrentGaps {
			entry := snapshot.CurrentGaps[index]
			if trimmedSelectedPath != "" && !sameGovernancePath(entry.Path, trimmedSelectedPath) {
				continue
			}
			selectedCurrentGap := entry
			cloned.SelectedItemKey = governanceSelectionItemKey("current-gap", entry.Path, entry.Category)
			cloned.SelectedItemKind = "current-gap"
			cloned.SelectedCurrentGapPath = entry.Path
			cloned.SelectedCurrentGap = &selectedCurrentGap
			if trimmedSelectedPath != "" {
				break
			}
			break
		}
	}
	if len(snapshot.DriftHotspots) > 0 {
		for index := range snapshot.DriftHotspots {
			entry := snapshot.DriftHotspots[index]
			if trimmedSelectedPath != "" && !sameGovernancePath(entry.Path, trimmedSelectedPath) {
				continue
			}
			selectedDriftHotspot := entry
			cloned.SelectedItemKey = governanceSelectionItemKey("drift-hotspot", entry.Path, "")
			cloned.SelectedItemKind = "drift-hotspot"
			cloned.SelectedDriftHotspotPath = entry.Path
			cloned.SelectedDriftHotspot = &selectedDriftHotspot
			if trimmedSelectedPath != "" {
				break
			}
			break
		}
	}
	if trimmedSelectedPath == "" && len(snapshot.Details) > 0 && cloned.SelectedDetail == nil {
		defaultDetail := snapshot.Details[0]
		cloned.SelectedItemKey = governanceSelectionItemKey("detail", defaultDetail.Path, "")
		cloned.SelectedItemKind = "detail"
		cloned.SelectedDetailPath = defaultDetail.Path
		cloned.SelectedDetail = &defaultDetail
	}
	if trimmedSelectedPath == "" && len(snapshot.BlockedInvariants) > 0 && cloned.SelectedBlockedInvariant == nil {
		defaultBlockedInvariant := snapshot.BlockedInvariants[0]
		cloned.SelectedItemKey = governanceSelectionItemKey("blocked-invariant", defaultBlockedInvariant.Path, "")
		cloned.SelectedItemKind = "blocked-invariant"
		cloned.SelectedBlockedInvariantPath = defaultBlockedInvariant.Path
		cloned.SelectedBlockedInvariant = &defaultBlockedInvariant
	}
	if trimmedSelectedPath == "" && len(snapshot.CurrentGaps) > 0 && cloned.SelectedCurrentGap == nil && cloned.SelectedDetail == nil && cloned.SelectedBlockedInvariant == nil {
		defaultCurrentGap := snapshot.CurrentGaps[0]
		cloned.SelectedItemKey = governanceSelectionItemKey("current-gap", defaultCurrentGap.Path, defaultCurrentGap.Category)
		cloned.SelectedItemKind = "current-gap"
		cloned.SelectedCurrentGapPath = defaultCurrentGap.Path
		cloned.SelectedCurrentGap = &defaultCurrentGap
	}
	if trimmedSelectedPath == "" && len(snapshot.DriftHotspots) > 0 && cloned.SelectedDriftHotspot == nil && cloned.SelectedDetail == nil && cloned.SelectedBlockedInvariant == nil && cloned.SelectedCurrentGap == nil {
		defaultDriftHotspot := snapshot.DriftHotspots[0]
		cloned.SelectedItemKey = governanceSelectionItemKey("drift-hotspot", defaultDriftHotspot.Path, "")
		cloned.SelectedItemKind = "drift-hotspot"
		cloned.SelectedDriftHotspotPath = defaultDriftHotspot.Path
		cloned.SelectedDriftHotspot = &defaultDriftHotspot
	}
	return &cloned
}

func sameGovernancePath(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func buildTaskFeedbackSnapshot(taskID string, baseStatus string, snapshot memstore.Snapshot) *OperatorTaskFeedbackSnapshot {
	if snapshot.IsEmpty() {
		return nil
	}
	collaboration := buildTaskCollaborationSnapshot(snapshot)
	finality := memstore.TaskFinalityReadinessForTask(baseStatus, snapshot)
	feedback := &OperatorTaskFeedbackSnapshot{
		Collaboration:        collaboration,
		TaskStatus:           finality.TaskStatus,
		FinalityReadiness:    finality.RuntimeTruthReadiness,
		FinalityBlockerCount: finality.BlockerCount,
		EvaluationCount:      len(snapshot.EvaluationRecords),
		WarmLessonCount:      len(snapshot.WarmLessons),
		EvolutionCount:       len(snapshot.EvolutionCandidates),
		ProjectLessonCount:   len(snapshot.ProjectLessons),
	}
	if collaboration != nil {
		feedback.ExecutionChain = append([]string(nil), collaboration.ExecutionChain...)
		feedback.LatestAvatarReport = collaboration.LatestAvatarReport
		feedback.LatestAvatarReportRoute = collaboration.LatestAvatarReportRoute
		feedback.LatestAvatarAsk = collaboration.LatestAvatarAsk
		feedback.LatestAvatarAskRoute = collaboration.LatestAvatarAskRoute
		feedback.LatestAvatarAskStatus = collaboration.LatestAvatarAskStatus
		feedback.LatestAvatarFollowUp = collaboration.LatestAvatarFollowUp
		feedback.LatestAvatarFollowUpRoute = collaboration.LatestAvatarFollowUpRoute
		feedback.LatestAvatarChallenge = collaboration.LatestAvatarChallenge
		feedback.LatestAvatarChallengeRoute = collaboration.LatestAvatarChallengeRoute
		feedback.LatestAvatarSummary = collaboration.LatestAvatarSummary
		feedback.LatestAvatarSummaryRoute = collaboration.LatestAvatarSummaryRoute
	}
	if snapshot.Verification != nil {
		followUp := memstore.TaskVerificationFollowUpCommand(taskID, *snapshot.Verification)
		if followUp == "" {
			followUp = snapshot.Verification.FollowUpCommand
		}
		feedback.Verification = &OperatorVerificationSnapshot{
			Tool:            snapshot.Verification.Tool,
			Verdict:         snapshot.Verification.Verdict,
			Summary:         snapshot.Verification.Summary,
			ReportPath:      snapshot.Verification.ReportPath,
			Warnings:        append([]string(nil), snapshot.Verification.Warnings...),
			Checks:          append([]string(nil), snapshot.Verification.Checks...),
			FollowUpCommand: followUp,
			UpdatedAt:       formatTimestamp(snapshot.Verification.UpdatedAt),
		}
	}
	if latestNonPass := memstore.LatestNonPassVerificationSnapshot(snapshot.Verification, snapshot.VerificationHistory); latestNonPass != nil {
		tool := strings.TrimSpace(latestNonPass.Tool)
		if tool == "" {
			tool = "n/a"
		}
		verdict := strings.TrimSpace(latestNonPass.Verdict)
		if verdict == "" {
			verdict = "n/a"
		}
		feedback.LatestNonPassVerification = fmt.Sprintf("%s | %s | %s", formatTimestamp(latestNonPass.UpdatedAt), tool, verdict)
		feedback.LatestNonPassFollowUp = memstore.TaskVerificationFollowUpCommand(taskID, *latestNonPass)
		if feedback.LatestNonPassFollowUp == "" {
			feedback.LatestNonPassFollowUp = latestNonPass.FollowUpCommand
		}
	}
	feedback.ReverifyStatus = memstore.VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory)
	if attempt := memstore.LatestReverifyAttemptEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); attempt != nil {
		feedback.LatestReverifyAttempt = attempt.Summary
		feedback.LatestReverifyAttemptProposalID = memstore.EvaluationRecordProposalID(*attempt)
		feedback.LatestReverifyAttemptTargets = memstore.EvaluationRecordExpectedTargets(*attempt)
	}
	if attempt := memstore.LatestCoveredReverifyAttemptEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); attempt != nil {
		feedback.LatestReverifyRemediation = attempt.Summary
		feedback.LatestReverifyRemediationProposalID = memstore.EvaluationRecordProposalID(*attempt)
		feedback.LatestReverifyRemediationTargets = memstore.EvaluationRecordExpectedTargets(*attempt)
	}
	if closure := memstore.LatestReverifyClosureEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); closure != nil {
		feedback.LatestReverifyClosure = closure.Summary
	}
	workflowNodeGovernance := memstore.WorkflowNodeGovernanceSnapshotForTask(snapshot)
	feedback.WorkflowNodeNextAction = workflowNodeGovernance.NextActionCue
	feedback.WorkflowNodeVerifierReportPath = workflowNodeGovernance.RetryClosureVerifierReportPath
	feedback.PendingApprovals = pendingApprovalSnapshots(memstore.PendingToolApprovalEvaluations(snapshot.EvaluationRecords))
	feedback.PendingApprovalCount = len(feedback.PendingApprovals)
	if approval := memstore.LatestToolApprovalRequiredEvaluation(snapshot.EvaluationRecords); approval != nil {
		feedback.LatestApprovalRequired = approval.Summary
		feedback.LatestApprovalRequiredSource = memstore.EvaluationRecordDecisionSource(*approval)
		feedback.LatestApprovalRequiredKey = memstore.EvaluationRecordApprovalKey(*approval)
		feedback.LatestApprovalRequiredMode = memstore.EvaluationRecordPermissionMode(*approval)
	}
	if replay := memstore.LatestToolApprovalReplayEvaluation(snapshot.EvaluationRecords); replay != nil {
		feedback.LatestApprovalReplay = replay.Summary
		feedback.LatestApprovalReplaySource = memstore.EvaluationRecordDecisionSource(*replay)
		feedback.LatestApprovalReplayKey = memstore.EvaluationRecordApprovalKey(*replay)
		feedback.LatestApprovalReplayTranscript = memstore.EvaluationRecordReplayTranscript(*replay)
		feedback.LatestApprovalContinuation = replay.Summary
		feedback.LatestApprovalContinuationSource = memstore.EvaluationRecordDecisionSource(*replay)
		feedback.LatestApprovalContinuationKey = memstore.EvaluationRecordApprovalKey(*replay)
		feedback.LatestApprovalContinuationTranscript = memstore.EvaluationRecordReplayTranscript(*replay)
		feedback.LatestApprovalContinuationRunID = memstore.EvaluationRecordContinuationRunID(*replay)
		feedback.LatestApprovalContinuationTaskID = memstore.EvaluationRecordContinuationTaskID(*replay)
	}
	if denial := memstore.LatestToolPermissionDenialEvaluation(snapshot.EvaluationRecords); denial != nil {
		feedback.LatestPermissionDenial = denial.Summary
		feedback.LatestPermissionDenialSource = memstore.EvaluationRecordDecisionSource(*denial)
	}
	feedback.RemediationProposals = remediationProposalSnapshots(memstore.SnapshotRemediationProposals(taskID, snapshot))
	feedback.SuggestedCommands = memstore.SuggestedTaskCommands(taskID, snapshot)
	if len(snapshot.EvaluationRecords) > 0 {
		feedback.LatestEvaluation = snapshot.EvaluationRecords[0].Summary
	}
	if len(snapshot.WarmLessons) > 0 {
		feedback.LatestWarmLesson = snapshot.WarmLessons[0].Summary
	}
	if len(snapshot.EvolutionCandidates) > 0 {
		feedback.LatestEvolution = snapshot.EvolutionCandidates[0].Summary
	}
	if len(snapshot.ProjectLessons) > 0 {
		feedback.TopProjectLesson = snapshot.ProjectLessons[0].Summary
	}
	feedback.Sections = buildTaskFeedbackSections(feedback)
	if feedback.Verification == nil && feedback.EvaluationCount == 0 && feedback.WarmLessonCount == 0 && feedback.EvolutionCount == 0 && feedback.ProjectLessonCount == 0 && len(feedback.ExecutionChain) == 0 && feedback.LatestAvatarReport == "" && feedback.LatestAvatarAsk == "" && feedback.LatestAvatarChallenge == "" && feedback.LatestAvatarSummary == "" {
		return nil
	}
	return feedback
}

func buildTaskCollaborationSnapshot(snapshot memstore.Snapshot) *OperatorTaskCollaborationSnapshot {
	collaboration := &OperatorTaskCollaborationSnapshot{
		ExecutionChain:             memstore.WorkflowExecutionChain(snapshot.RecentEvents),
		LatestAvatarReport:         memstore.LatestAvatarReport(snapshot.RecentEvents),
		LatestAvatarReportRoute:    memstore.LatestAvatarReportRouteCue(snapshot.RecentEvents),
		LatestAvatarAsk:            memstore.LatestAvatarAsk(snapshot.RecentEvents),
		LatestAvatarAskRoute:       memstore.LatestAvatarAskRouteCue(snapshot.RecentEvents),
		LatestAvatarAskStatus:      memstore.LatestAvatarAskFollowUpStatus(snapshot.RecentEvents),
		LatestAvatarFollowUp:       memstore.LatestAvatarFollowUp(snapshot.RecentEvents),
		LatestAvatarFollowUpRoute:  memstore.LatestAvatarFollowUpRouteCue(snapshot.RecentEvents),
		LatestAvatarChallenge:      memstore.LatestAvatarChallenge(snapshot.RecentEvents),
		LatestAvatarChallengeRoute: memstore.LatestAvatarChallengeRouteCue(snapshot.RecentEvents),
		LatestAvatarSummary:        memstore.LatestAvatarSummary(snapshot.RecentEvents),
		LatestAvatarSummaryRoute:   memstore.LatestAvatarSummaryRouteCue(snapshot.RecentEvents),
	}
	collaboration.FocusCue = collaborationFocusCue(collaboration)
	if len(collaboration.ExecutionChain) == 0 && collaboration.LatestAvatarReport == "" && collaboration.LatestAvatarAsk == "" && collaboration.LatestAvatarChallenge == "" && collaboration.LatestAvatarSummary == "" {
		return nil
	}
	return collaboration
}

func buildTaskFeedbackSections(feedback *OperatorTaskFeedbackSnapshot) []OperatorTaskFeedbackSectionSnapshot {
	if feedback == nil {
		return nil
	}
	sections := make([]OperatorTaskFeedbackSectionSnapshot, 0, 3)
	if feedback.Collaboration != nil {
		sections = append(sections, OperatorTaskFeedbackSectionSnapshot{
			Key:     "collaboration",
			Label:   "collaboration",
			Summary: fmt.Sprintf("focus=%s ask_status=%s chain=%d report=%s ask=%s follow_up=%s challenge=%s summary=%s", compactFocusCue(feedback.Collaboration.FocusCue), compactAskStatusCue(feedback.Collaboration.LatestAvatarAskStatus), len(feedback.Collaboration.ExecutionChain), compactRouteCue(feedback.Collaboration.LatestAvatarReport != "", feedback.Collaboration.LatestAvatarReportRoute), compactRouteCue(feedback.Collaboration.LatestAvatarAsk != "", feedback.Collaboration.LatestAvatarAskRoute), compactRouteCue(feedback.Collaboration.LatestAvatarFollowUp != "", feedback.Collaboration.LatestAvatarFollowUpRoute), compactRouteCue(feedback.Collaboration.LatestAvatarChallenge != "", feedback.Collaboration.LatestAvatarChallengeRoute), compactRouteCue(feedback.Collaboration.LatestAvatarSummary != "", feedback.Collaboration.LatestAvatarSummaryRoute)),
		})
	}
	sections = append(sections, OperatorTaskFeedbackSectionSnapshot{
		Key:     "feedback",
		Label:   "feedback",
		Summary: fmt.Sprintf("status=%s finality=%s blockers=%d evaluations=%d warm=%d evolution=%d project=%d", compactFocusCue(feedback.TaskStatus), compactFocusCue(feedback.FinalityReadiness), feedback.FinalityBlockerCount, feedback.EvaluationCount, feedback.WarmLessonCount, feedback.EvolutionCount, feedback.ProjectLessonCount),
	})
	if strings.TrimSpace(feedback.WorkflowNodeNextAction) != "" || strings.TrimSpace(feedback.WorkflowNodeVerifierReportPath) != "" {
		summary := "state=available"
		if strings.TrimSpace(feedback.WorkflowNodeNextAction) != "" {
			summary = fmt.Sprintf("next_action=%s", strings.TrimSpace(feedback.WorkflowNodeNextAction))
		}
		if strings.TrimSpace(feedback.WorkflowNodeVerifierReportPath) != "" {
			summary += " verifier_report=present"
		}
		sections = append(sections, OperatorTaskFeedbackSectionSnapshot{
			Key:     "workflow-node",
			Label:   "workflow node",
			Summary: summary,
		})
	}
	if feedback.Verification != nil || feedback.LatestNonPassVerification != "" || feedback.ReverifyStatus != "" || feedback.PendingApprovalCount > 0 || feedback.LatestApprovalRequired != "" || feedback.LatestApprovalReplay != "" || feedback.LatestPermissionDenial != "" {
		verificationSummary := "state=unavailable"
		if feedback.Verification != nil {
			tool := strings.TrimSpace(feedback.Verification.Tool)
			if tool == "" {
				tool = "n/a"
			}
			verificationSummary = fmt.Sprintf("current=%s via %s", feedback.Verification.Verdict, tool)
		} else if feedback.LatestNonPassVerification != "" {
			verificationSummary = "current=latest-non-pass"
		}
		if len(feedback.SuggestedCommands) > 0 {
			verificationSummary += fmt.Sprintf(" suggestions=%d", len(feedback.SuggestedCommands))
		}
		if len(feedback.RemediationProposals) > 0 {
			verificationSummary += fmt.Sprintf(" proposals=%d", len(feedback.RemediationProposals))
		}
		if feedback.PendingApprovalCount > 0 {
			verificationSummary += fmt.Sprintf(" pending_approvals=%d", feedback.PendingApprovalCount)
		}
		if strings.TrimSpace(feedback.LatestApprovalRequiredSource) != "" {
			verificationSummary += fmt.Sprintf(" approval=%s", strings.TrimSpace(feedback.LatestApprovalRequiredSource))
		}
		if strings.TrimSpace(feedback.LatestApprovalRequiredMode) != "" {
			verificationSummary += fmt.Sprintf(" mode=%s", strings.TrimSpace(feedback.LatestApprovalRequiredMode))
		}
		if strings.TrimSpace(feedback.TaskStatus) != "" {
			verificationSummary += fmt.Sprintf(" task_status=%s", strings.TrimSpace(feedback.TaskStatus))
		}
		if strings.TrimSpace(feedback.LatestApprovalContinuation) != "" {
			verificationSummary += " continuation=recent"
		}
		if strings.TrimSpace(feedback.LatestPermissionDenialSource) != "" {
			verificationSummary += fmt.Sprintf(" denied=%s", strings.TrimSpace(feedback.LatestPermissionDenialSource))
		}
		if cue := compactReverifyStatus(feedback.ReverifyStatus); cue != "" {
			verificationSummary += fmt.Sprintf(" reverify=%s", cue)
		}
		sections = append(sections, OperatorTaskFeedbackSectionSnapshot{
			Key:     "verification",
			Label:   "verification",
			Summary: verificationSummary,
		})
	}
	return sections
}

func yesNoWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func compactReverifyStatus(status string) string {
	trimmed := strings.TrimSpace(status)
	if trimmed == "" {
		return ""
	}
	switch trimmed {
	case "pending reverify for latest non-pass verification":
		return "pending"
	case "latest non-pass verification is covered by a later PASS":
		return "covered"
	default:
		return trimmed
	}
}

func compactRouteCue(hasValue bool, route string) string {
	trimmedRoute := strings.TrimSpace(route)
	if trimmedRoute != "" {
		return trimmedRoute
	}
	return yesNoWord(hasValue)
}

func compactFocusCue(focus string) string {
	trimmedFocus := strings.TrimSpace(focus)
	if trimmedFocus == "" {
		return "none"
	}
	return trimmedFocus
}

func compactAskStatusCue(status string) string {
	trimmedStatus := strings.TrimSpace(status)
	if trimmedStatus == "" {
		return "none"
	}
	return trimmedStatus
}

func remediationProposalSnapshots(proposals []planner.RemediationActionProposal) []OperatorTaskRemediationProposal {
	if len(proposals) == 0 {
		return nil
	}
	snapshots := make([]OperatorTaskRemediationProposal, 0, len(proposals))
	for _, proposal := range proposals {
		snapshots = append(snapshots, OperatorTaskRemediationProposal{
			ProposalID:      proposal.ProposalID,
			Kind:            proposal.Kind,
			Summary:         proposal.Summary,
			Intent:          proposal.Intent,
			ExpectedTargets: append([]string(nil), proposal.ExpectedTargets...),
			FollowUpCommand: proposal.FollowUpCommand,
		})
	}
	return snapshots
}

func pendingApprovalSnapshots(records []memstore.EvaluationRecord) []OperatorTaskPendingApprovalSnapshot {
	if len(records) == 0 {
		return nil
	}
	snapshots := make([]OperatorTaskPendingApprovalSnapshot, 0, len(records))
	for _, record := range records {
		snapshots = append(snapshots, OperatorTaskPendingApprovalSnapshot{
			ApprovalKey:    memstore.EvaluationRecordApprovalKey(record),
			Tool:           record.Tool,
			Operation:      memstore.EvaluationRecordOperation(record),
			Source:         memstore.EvaluationRecordDecisionSource(record),
			PermissionMode: memstore.EvaluationRecordPermissionMode(record),
			Summary:        record.Summary,
		})
	}
	return snapshots
}

func collaborationFocusCue(collaboration *OperatorTaskCollaborationSnapshot) string {
	if collaboration == nil {
		return ""
	}
	askStatus := strings.TrimSpace(collaboration.LatestAvatarAskStatus)
	if askStatus != "" {
		return askStatus
	}
	askRoute := strings.TrimSpace(collaboration.LatestAvatarAskRoute)
	followUpRoute := strings.TrimSpace(collaboration.LatestAvatarFollowUpRoute)
	if collaboration.LatestAvatarFollowUp != "" {
		if followUpRoute != "" {
			return "latest follow-up via " + followUpRoute
		}
		return "latest follow-up"
	}
	if collaboration.LatestAvatarAsk != "" {
		if strings.HasPrefix(askRoute, "to ") {
			return "directed ask via " + askRoute
		}
		if askRoute != "" {
			return "latest ask via " + askRoute
		}
		return "latest ask"
	}
	challengeRoute := strings.TrimSpace(collaboration.LatestAvatarChallengeRoute)
	if collaboration.LatestAvatarChallenge != "" {
		if challengeRoute != "" {
			return "latest challenge via " + challengeRoute
		}
		return "latest challenge"
	}
	reportRoute := strings.TrimSpace(collaboration.LatestAvatarReportRoute)
	if collaboration.LatestAvatarReport != "" {
		if reportRoute != "" {
			return "latest report via " + reportRoute
		}
		return "latest report"
	}
	summaryRoute := strings.TrimSpace(collaboration.LatestAvatarSummaryRoute)
	if collaboration.LatestAvatarSummary != "" {
		if summaryRoute != "" {
			return "latest summary via " + summaryRoute
		}
		return "latest summary"
	}
	return ""
}

func buildSkillGovernanceSnapshot(summary skills.GovernanceSummary, reconciliation skills.GovernanceReconciliation, ledger skills.GovernanceLedger) *OperatorSkillGovernanceSnapshot {
	blockedInvariants := buildBlockedInvariantSnapshots(summary.Transitions, reconciliation, ledger, max(3, 3))
	currentGaps := buildSkillGovernanceCurrentGaps(reconciliation, max(3, 3))
	driftHotspots := buildSkillGovernanceDriftHotspots(reconciliation, max(3, 3))
	if summary.TrackedSkills == 0 && len(summary.Alerts) == 0 && len(summary.Hotspots) == 0 && len(blockedInvariants) == 0 && len(currentGaps) == 0 && len(driftHotspots) == 0 {
		return nil
	}
	const maxGovernanceItems = 3
	invariantDiagnostics := buildInvariantDiagnosticLookup(reconciliation)
	blockedInvariants = buildBlockedInvariantSnapshots(summary.Transitions, reconciliation, ledger, maxGovernanceItems)
	currentGaps = buildSkillGovernanceCurrentGaps(reconciliation, maxGovernanceItems)
	driftHotspots = buildSkillGovernanceDriftHotspots(reconciliation, maxGovernanceItems)
	snapshot := &OperatorSkillGovernanceSnapshot{
		Scope:                 "repository",
		TrackedSkills:         summary.TrackedSkills,
		GeneratedCount:        summary.GeneratedCount,
		ApprovedCount:         summary.ApprovedCount,
		ArchivedCount:         summary.ArchivedCount,
		DisabledCount:         summary.DisabledCount,
		AlertCount:            len(summary.Alerts),
		BlockedInvariantCount: countBlockedInvariants(reconciliation),
		CurrentGapCount:       len(reconciliation.MissingCurrent) + len(reconciliation.UntrackedCurrent),
		DriftHotspotCount:     countDriftHotspots(reconciliation),
		MissingCurrentCount:   len(reconciliation.MissingCurrent),
		UntrackedCurrentCount: len(reconciliation.UntrackedCurrent),
		OverviewCommand:       "avatars skills reconcile",
	}
	for _, alert := range summary.Alerts {
		switch alert.Severity {
		case "error":
			snapshot.ErrorCount++
		case "warning":
			snapshot.WarningCount++
		}
	}
	if len(summary.Alerts) > 0 {
		snapshot.TopAlert = fmt.Sprintf("%s | %s | %s", summary.Alerts[0].Severity, summary.Alerts[0].SkillName, summary.Alerts[0].Reason)
		snapshot.TopAlertCommand = governanceAlertFollowUp(summary.Alerts[0], invariantDiagnostics)
		limit := len(summary.Alerts)
		if limit > maxGovernanceItems {
			limit = maxGovernanceItems
		}
		snapshot.Alerts = make([]OperatorSkillGovernanceAlert, 0, limit)
		for _, alert := range summary.Alerts[:limit] {
			snapshot.Alerts = append(snapshot.Alerts, buildOperatorGovernanceAlert(alert, invariantDiagnostics))
		}
	}
	if len(summary.Hotspots) > 0 {
		snapshot.TopHotspot = fmt.Sprintf("%s | transitions=%d | current=%s", summary.Hotspots[0].SkillName, summary.Hotspots[0].TransitionCount, summary.Hotspots[0].CurrentState)
		snapshot.TopHotspotCommand = governanceHotspotFollowUp(summary.Hotspots[0])
		limit := len(summary.Hotspots)
		if limit > maxGovernanceItems {
			limit = maxGovernanceItems
		}
		snapshot.Hotspots = make([]OperatorSkillGovernanceHotspot, 0, limit)
		for _, hotspot := range summary.Hotspots[:limit] {
			snapshot.Hotspots = append(snapshot.Hotspots, OperatorSkillGovernanceHotspot{
				SkillName:       hotspot.SkillName,
				CurrentState:    hotspot.CurrentState,
				TransitionCount: hotspot.TransitionCount,
				FollowUpCommand: governanceHotspotFollowUp(hotspot),
			})
		}
	}
	if len(blockedInvariants) > 0 {
		snapshot.TopBlockedInvariant = fmt.Sprintf("%s | %s | blocker=%s", blockedInvariants[0].SkillName, blockedInvariants[0].Reason, blockedInvariants[0].BlockedBy)
		snapshot.TopBlockedInvariantCommand = blockedInvariants[0].FollowUpCommand
		snapshot.BlockedInvariants = blockedInvariants
	}
	if len(currentGaps) > 0 {
		snapshot.TopCurrentGap = fmt.Sprintf("%s | %s | path=%s", currentGaps[0].Category, currentGaps[0].SkillName, emptyGovernanceValue(currentGaps[0].Path))
		snapshot.TopCurrentGapCommand = currentGaps[0].FollowUpCommand
		snapshot.CurrentGaps = currentGaps
	}
	if len(driftHotspots) > 0 {
		snapshot.TopDriftHotspot = fmt.Sprintf("%s | drifts=%d | path=%s", driftHotspots[0].SkillName, len(driftHotspots[0].Drifts), emptyGovernanceValue(driftHotspots[0].Path))
		snapshot.TopDriftHotspotCommand = driftHotspots[0].FollowUpCommand
		snapshot.DriftHotspots = driftHotspots
	}
	snapshot.Details = buildSkillGovernanceDetails(summary, invariantDiagnostics, maxGovernanceItems)
	snapshot.SelectionItems = buildSkillGovernanceSelectionItems(snapshot)
	snapshot.SelectionSections = buildSkillGovernanceSelectionSections(snapshot.SelectionItems)
	return snapshot
}

func buildSkillGovernanceSelectionItems(snapshot *OperatorSkillGovernanceSnapshot) []OperatorSkillGovernanceSelectionItem {
	if snapshot == nil {
		return nil
	}
	items := make([]OperatorSkillGovernanceSelectionItem, 0, len(snapshot.Details)+len(snapshot.BlockedInvariants)+len(snapshot.CurrentGaps)+len(snapshot.DriftHotspots))
	for _, detail := range snapshot.Details {
		priorityScore, priorityLabel := governanceSelectionDetailPriority(detail)
		items = append(items, OperatorSkillGovernanceSelectionItem{
			Key:             governanceSelectionItemKey("detail", detail.Path, ""),
			Kind:            "detail",
			Section:         governanceSelectionSectionKey("detail"),
			Path:            detail.Path,
			SkillName:       detail.SkillName,
			Summary:         fmt.Sprintf("Detail | %s | %s", detail.SkillName, emptyGovernanceValue(detail.CurrentState)),
			FollowUpCommand: detail.FollowUpCommand,
			PriorityScore:   priorityScore,
			PriorityLabel:   priorityLabel,
		})
	}
	for _, entry := range snapshot.BlockedInvariants {
		priorityScore, priorityLabel := governanceSelectionBlockedInvariantPriority(entry)
		items = append(items, OperatorSkillGovernanceSelectionItem{
			Key:             governanceSelectionItemKey("blocked-invariant", entry.Path, ""),
			Kind:            "blocked-invariant",
			Section:         governanceSelectionSectionKey("blocked-invariant"),
			Path:            entry.Path,
			SkillName:       entry.SkillName,
			Summary:         fmt.Sprintf("Blocked invariant | %s | %s", entry.SkillName, emptyGovernanceValue(entry.CurrentState)),
			FollowUpCommand: entry.FollowUpCommand,
			PriorityScore:   priorityScore,
			PriorityLabel:   priorityLabel,
		})
	}
	for _, gap := range snapshot.CurrentGaps {
		priorityScore, priorityLabel := governanceSelectionCurrentGapPriority(gap)
		items = append(items, OperatorSkillGovernanceSelectionItem{
			Key:             governanceSelectionItemKey("current-gap", gap.Path, gap.Category),
			Kind:            "current-gap",
			Section:         governanceSelectionSectionKey("current-gap"),
			Path:            gap.Path,
			SkillName:       gap.SkillName,
			Summary:         fmt.Sprintf("Current gap | %s | %s", gap.Category, gap.SkillName),
			FollowUpCommand: gap.FollowUpCommand,
			PriorityScore:   priorityScore,
			PriorityLabel:   priorityLabel,
		})
	}
	for _, hotspot := range snapshot.DriftHotspots {
		priorityScore, priorityLabel := governanceSelectionDriftHotspotPriority(hotspot)
		items = append(items, OperatorSkillGovernanceSelectionItem{
			Key:             governanceSelectionItemKey("drift-hotspot", hotspot.Path, ""),
			Kind:            "drift-hotspot",
			Section:         governanceSelectionSectionKey("drift-hotspot"),
			Path:            hotspot.Path,
			SkillName:       hotspot.SkillName,
			Summary:         fmt.Sprintf("Drift hotspot | %s | %s", hotspot.SkillName, emptyGovernanceValue(hotspot.CurrentState)),
			FollowUpCommand: hotspot.FollowUpCommand,
			PriorityScore:   priorityScore,
			PriorityLabel:   priorityLabel,
		})
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func buildSkillGovernanceSelectionSections(items []OperatorSkillGovernanceSelectionItem) []OperatorSkillGovernanceSelectionSection {
	if len(items) == 0 {
		return nil
	}
	sections := make([]OperatorSkillGovernanceSelectionSection, 0, 4)
	indexByKey := make(map[string]int, 4)
	for _, item := range items {
		sectionKey := strings.TrimSpace(item.Section)
		if sectionKey == "" {
			continue
		}
		if index, ok := indexByKey[sectionKey]; ok {
			sections[index].Count++
			continue
		}
		indexByKey[sectionKey] = len(sections)
		sections = append(sections, OperatorSkillGovernanceSelectionSection{
			Key:   sectionKey,
			Label: governanceSelectionSectionLabel(sectionKey),
			Count: 1,
		})
	}
	if len(sections) == 0 {
		return nil
	}
	return sections
}

func governanceSelectionPriorityLabel(score int) string {
	if score >= 80 {
		return "urgent"
	}
	if score >= 60 {
		return "review"
	}
	return "inspect"
}

func governanceSelectionDetailPriority(detail OperatorSkillGovernanceDetail) (int, string) {
	for _, alert := range detail.Alerts {
		if strings.EqualFold(alert.Severity, "error") {
			return 85, governanceSelectionPriorityLabel(85)
		}
	}
	for _, alert := range detail.Alerts {
		if strings.EqualFold(alert.Severity, "warning") {
			return 60, governanceSelectionPriorityLabel(60)
		}
	}
	return 30, governanceSelectionPriorityLabel(30)
}

func governanceSelectionBlockedInvariantPriority(entry OperatorSkillGovernanceInvariant) (int, string) {
	return 100, governanceSelectionPriorityLabel(100)
}

func governanceSelectionCurrentGapPriority(gap OperatorSkillGovernanceCurrentGap) (int, string) {
	if strings.EqualFold(gap.Category, "missing-current") {
		return 90, governanceSelectionPriorityLabel(90)
	}
	return 75, governanceSelectionPriorityLabel(75)
}

func governanceSelectionDriftHotspotPriority(hotspot OperatorSkillGovernanceDriftHotspot) (int, string) {
	if len(hotspot.Drifts) >= 3 {
		return 80, governanceSelectionPriorityLabel(80)
	}
	return 65, governanceSelectionPriorityLabel(65)
}

func buildSkillGovernanceCurrentGaps(reconciliation skills.GovernanceReconciliation, maxItems int) []OperatorSkillGovernanceCurrentGap {
	if maxItems <= 0 {
		return nil
	}
	gaps := make([]OperatorSkillGovernanceCurrentGap, 0, maxItems)
	appendGap := func(gap OperatorSkillGovernanceCurrentGap) {
		if len(gaps) >= maxItems {
			return
		}
		gaps = append(gaps, gap)
	}
	for _, entry := range reconciliation.MissingCurrent {
		appendGap(OperatorSkillGovernanceCurrentGap{
			Category:        "missing-current",
			SkillName:       entry.SkillName,
			Path:            entry.SkillPath,
			RecordedState:   entry.ToState,
			RecordedAction:  entry.Action,
			SourceTaskID:    entry.SourceTaskID,
			SourceRunID:     entry.SourceRunID,
			FollowUpCommand: governanceMissingCurrentFollowUp(entry),
		})
	}
	for _, record := range reconciliation.UntrackedCurrent {
		appendGap(OperatorSkillGovernanceCurrentGap{
			Category:        "untracked-current",
			SkillName:       record.SkillName,
			Path:            record.Path,
			CurrentState:    record.CurrentState,
			ContentDigest:   record.ContentDigest,
			FollowUpCommand: governanceUntrackedCurrentFollowUp(record),
		})
	}
	if len(gaps) == 0 {
		return nil
	}
	return gaps
}

func buildSkillGovernanceDriftHotspots(reconciliation skills.GovernanceReconciliation, maxItems int) []OperatorSkillGovernanceDriftHotspot {
	if maxItems <= 0 {
		return nil
	}
	type groupedHotspot struct {
		SkillName    string
		Path         string
		CurrentState string
		Drifts       []OperatorSkillGovernanceDrift
	}
	grouped := make(map[string]*groupedHotspot)
	orderedKeys := make([]string, 0)
	appendDrift := func(skillName string, path string, currentState string, drift OperatorSkillGovernanceDrift) {
		key := governanceSkillSnapshotKey(skillName, path)
		if key == "" {
			return
		}
		hotspot, ok := grouped[key]
		if !ok {
			hotspot = &groupedHotspot{SkillName: skillName, Path: path, CurrentState: currentState}
			grouped[key] = hotspot
			orderedKeys = append(orderedKeys, key)
		}
		hotspot.Drifts = append(hotspot.Drifts, drift)
		if hotspot.CurrentState == "" {
			hotspot.CurrentState = currentState
		}
	}
	for _, drift := range reconciliation.HistoryDrifts {
		appendDrift(drift.SkillName, drift.CurrentPath, drift.CurrentState, OperatorSkillGovernanceDrift{
			Category: "history",
			Summary:  fmt.Sprintf("%s | transitions %d/%d | latest %s/%s", drift.Reason, drift.ActualTransitions, drift.ExpectedTransitions, emptyGovernanceValue(drift.ActualLatestAction), emptyGovernanceValue(drift.ExpectedLatestAction)),
		})
	}
	for _, drift := range reconciliation.MetadataDrifts {
		appendDrift(drift.SkillName, drift.CurrentPath, drift.CurrentState, OperatorSkillGovernanceDrift{
			Category: "metadata",
			Summary:  fmt.Sprintf("%s | actual=%s | expected=%s", drift.Field, emptyGovernanceValue(drift.Actual), emptyGovernanceValue(drift.Expected)),
		})
	}
	for _, drift := range reconciliation.StateDrifts {
		appendDrift(drift.SkillName, drift.CurrentPath, drift.CurrentState, OperatorSkillGovernanceDrift{
			Category: "state",
			Summary:  fmt.Sprintf("current=%s | ledger=%s", emptyGovernanceValue(drift.CurrentState), emptyGovernanceValue(drift.LedgerState)),
		})
	}
	for _, drift := range reconciliation.PathDrifts {
		appendDrift(drift.SkillName, drift.CurrentPath, "", OperatorSkillGovernanceDrift{
			Category: "path",
			Summary:  fmt.Sprintf("current=%s | ledger=%s", emptyGovernanceValue(drift.CurrentPath), emptyGovernanceValue(drift.LedgerPath)),
		})
	}
	for _, drift := range reconciliation.ContentDrifts {
		appendDrift(drift.SkillName, drift.CurrentPath, drift.CurrentState, OperatorSkillGovernanceDrift{
			Category: "content",
			Summary:  fmt.Sprintf("current=%s | ledger=%s | states %s/%s", emptyGovernanceValue(drift.CurrentDigest), emptyGovernanceValue(drift.LedgerDigest), emptyGovernanceValue(drift.CurrentState), emptyGovernanceValue(drift.LedgerState)),
		})
	}
	if len(grouped) == 0 {
		return nil
	}
	sort.SliceStable(orderedKeys, func(i int, j int) bool {
		left := grouped[orderedKeys[i]]
		right := grouped[orderedKeys[j]]
		if len(left.Drifts) != len(right.Drifts) {
			return len(left.Drifts) > len(right.Drifts)
		}
		if left.SkillName != right.SkillName {
			return left.SkillName < right.SkillName
		}
		return left.Path < right.Path
	})
	if len(orderedKeys) > maxItems {
		orderedKeys = orderedKeys[:maxItems]
	}
	hotspots := make([]OperatorSkillGovernanceDriftHotspot, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		group := grouped[key]
		hotspots = append(hotspots, OperatorSkillGovernanceDriftHotspot{
			SkillName:       group.SkillName,
			Path:            group.Path,
			CurrentState:    group.CurrentState,
			Drifts:          append([]OperatorSkillGovernanceDrift(nil), group.Drifts...),
			FollowUpCommand: governanceDriftHotspotFollowUp(group.Path, group.Drifts),
		})
	}
	return hotspots
}

func countBlockedInvariants(reconciliation skills.GovernanceReconciliation) int {
	count := 0
	for _, drift := range reconciliation.InvariantDrifts {
		if !drift.Repairable {
			count++
		}
	}
	return count
}

func countDriftHotspots(reconciliation skills.GovernanceReconciliation) int {
	seen := make(map[string]struct{})
	appendKey := func(skillName string, path string) {
		key := governanceSkillSnapshotKey(skillName, path)
		if key == "" {
			return
		}
		seen[key] = struct{}{}
	}
	for _, drift := range reconciliation.HistoryDrifts {
		appendKey(drift.SkillName, drift.CurrentPath)
	}
	for _, drift := range reconciliation.MetadataDrifts {
		appendKey(drift.SkillName, drift.CurrentPath)
	}
	for _, drift := range reconciliation.StateDrifts {
		appendKey(drift.SkillName, drift.CurrentPath)
	}
	for _, drift := range reconciliation.PathDrifts {
		appendKey(drift.SkillName, drift.CurrentPath)
	}
	for _, drift := range reconciliation.ContentDrifts {
		appendKey(drift.SkillName, drift.CurrentPath)
	}
	return len(seen)
}

func buildBlockedInvariantSnapshots(transitions []skills.GovernanceTransition, reconciliation skills.GovernanceReconciliation, ledger skills.GovernanceLedger, maxItems int) []OperatorSkillGovernanceInvariant {
	if maxItems <= 0 {
		return nil
	}
	const maxInvariantTransitions = 3
	const maxInvariantRelatedDrifts = 7
	const maxInvariantLedgerEntries = 3
	transitionsByKey := make(map[string][]skills.GovernanceTransition)
	ledgerEntriesBySkill := make(map[string][]skills.GovernanceLedgerEntry)
	for _, transition := range transitions {
		key := governanceSkillSnapshotKey(transition.SkillName, transition.SkillPath)
		transitionsByKey[key] = append(transitionsByKey[key], transition)
	}
	for _, entry := range ledger.Entries {
		key := strings.TrimSpace(entry.SkillName)
		if key == "" {
			continue
		}
		ledgerEntriesBySkill[key] = append(ledgerEntriesBySkill[key], entry)
	}
	blocked := make([]OperatorSkillGovernanceInvariant, 0, maxItems)
	for _, drift := range reconciliation.InvariantDrifts {
		if drift.Repairable {
			continue
		}
		entry := OperatorSkillGovernanceInvariant{
			SkillName:       drift.SkillName,
			Path:            drift.CurrentPath,
			CurrentState:    drift.CurrentState,
			Reason:          drift.Reason,
			BlockedBy:       drift.BlockedBy,
			FollowUpCommand: strings.TrimSpace(drift.SuggestedFollowUp),
		}
		if entry.FollowUpCommand == "" {
			entry.FollowUpCommand = "avatars skills ledger"
		}
		if matches := transitionsByKey[governanceSkillSnapshotKey(drift.SkillName, drift.CurrentPath)]; len(matches) > 0 {
			limit := len(matches)
			if limit > maxInvariantTransitions {
				limit = maxInvariantTransitions
			}
			entry.RecentTransitions = make([]OperatorSkillGovernanceTransition, 0, limit)
			for _, transition := range matches[:limit] {
				entry.RecentTransitions = append(entry.RecentTransitions, OperatorSkillGovernanceTransition{
					At:        formatTimestamp(transition.At),
					Action:    transition.Action,
					FromState: transition.FromState,
					ToState:   transition.ToState,
				})
			}
		}
		entry.RelatedDrifts = buildBlockedInvariantRelatedDrifts(reconciliation, drift, maxInvariantRelatedDrifts)
		if matches := ledgerEntriesBySkill[strings.TrimSpace(drift.SkillName)]; len(matches) > 0 {
			limit := len(matches)
			if limit > maxInvariantLedgerEntries {
				limit = maxInvariantLedgerEntries
			}
			entry.RecentLedgerEntries = make([]OperatorSkillGovernanceLedgerEntry, 0, limit)
			for _, ledgerEntry := range matches[:limit] {
				entry.RecentLedgerEntries = append(entry.RecentLedgerEntries, OperatorSkillGovernanceLedgerEntry{
					At:           formatTimestamp(ledgerEntry.At),
					Action:       ledgerEntry.Action,
					FromState:    ledgerEntry.FromState,
					ToState:      ledgerEntry.ToState,
					Path:         ledgerEntry.SkillPath,
					SourceTaskID: ledgerEntry.SourceTaskID,
					SourceRunID:  ledgerEntry.SourceRunID,
				})
			}
		}
		blocked = append(blocked, entry)
		if len(blocked) >= maxItems {
			break
		}
	}
	if len(blocked) == 0 {
		return nil
	}
	return blocked
}

func buildBlockedInvariantRelatedDrifts(reconciliation skills.GovernanceReconciliation, invariant skills.GovernanceInvariantDrift, maxItems int) []OperatorSkillGovernanceDrift {
	if maxItems <= 0 {
		return nil
	}
	targetKey := governanceSkillSnapshotKey(invariant.SkillName, invariant.CurrentPath)
	targetSkillName := strings.TrimSpace(invariant.SkillName)
	related := make([]OperatorSkillGovernanceDrift, 0, maxItems)
	appendRelated := func(drift OperatorSkillGovernanceDrift) {
		if len(related) >= maxItems {
			return
		}
		related = append(related, drift)
	}
	for _, drift := range reconciliation.HistoryDrifts {
		if governanceSkillSnapshotKey(drift.SkillName, drift.CurrentPath) != targetKey {
			continue
		}
		appendRelated(OperatorSkillGovernanceDrift{
			Category: "history",
			Summary:  fmt.Sprintf("%s | transitions %d/%d | latest %s/%s", drift.Reason, drift.ActualTransitions, drift.ExpectedTransitions, emptyGovernanceValue(drift.ActualLatestAction), emptyGovernanceValue(drift.ExpectedLatestAction)),
		})
	}
	for _, drift := range reconciliation.MetadataDrifts {
		if governanceSkillSnapshotKey(drift.SkillName, drift.CurrentPath) != targetKey {
			continue
		}
		appendRelated(OperatorSkillGovernanceDrift{
			Category: "metadata",
			Summary:  fmt.Sprintf("%s | actual=%s | expected=%s", drift.Field, emptyGovernanceValue(drift.Actual), emptyGovernanceValue(drift.Expected)),
		})
	}
	for _, drift := range reconciliation.StateDrifts {
		if governanceSkillSnapshotKey(drift.SkillName, drift.CurrentPath) != targetKey {
			continue
		}
		appendRelated(OperatorSkillGovernanceDrift{
			Category: "state",
			Summary:  fmt.Sprintf("current=%s | ledger=%s", emptyGovernanceValue(drift.CurrentState), emptyGovernanceValue(drift.LedgerState)),
		})
	}
	for _, drift := range reconciliation.PathDrifts {
		if governanceSkillSnapshotKey(drift.SkillName, drift.CurrentPath) != targetKey {
			continue
		}
		appendRelated(OperatorSkillGovernanceDrift{
			Category: "path",
			Summary:  fmt.Sprintf("current=%s | ledger=%s", emptyGovernanceValue(drift.CurrentPath), emptyGovernanceValue(drift.LedgerPath)),
		})
	}
	for _, drift := range reconciliation.ContentDrifts {
		if governanceSkillSnapshotKey(drift.SkillName, drift.CurrentPath) != targetKey {
			continue
		}
		appendRelated(OperatorSkillGovernanceDrift{
			Category: "content",
			Summary:  fmt.Sprintf("current=%s | ledger=%s | states %s/%s", emptyGovernanceValue(drift.CurrentDigest), emptyGovernanceValue(drift.LedgerDigest), emptyGovernanceValue(drift.CurrentState), emptyGovernanceValue(drift.LedgerState)),
		})
	}
	for _, entry := range reconciliation.MissingCurrent {
		if strings.TrimSpace(entry.SkillName) != targetSkillName {
			continue
		}
		appendRelated(OperatorSkillGovernanceDrift{
			Category: "missing-current",
			Summary:  fmt.Sprintf("recorded=%s | state=%s | action=%s", emptyGovernanceValue(entry.SkillPath), emptyGovernanceValue(entry.ToState), emptyGovernanceValue(entry.Action)),
		})
	}
	for _, record := range reconciliation.UntrackedCurrent {
		if strings.TrimSpace(record.SkillName) != targetSkillName {
			continue
		}
		appendRelated(OperatorSkillGovernanceDrift{
			Category: "untracked-current",
			Summary:  fmt.Sprintf("path=%s | state=%s | digest=%s", emptyGovernanceValue(record.Path), emptyGovernanceValue(record.CurrentState), emptyGovernanceValue(record.ContentDigest)),
		})
	}
	if len(related) == 0 {
		return nil
	}
	return related
}

func emptyGovernanceValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "n/a"
	}
	return trimmed
}

func buildSkillGovernanceDetails(summary skills.GovernanceSummary, invariantDiagnostics map[string]skills.GovernanceInvariantDrift, maxItems int) []OperatorSkillGovernanceDetail {
	if maxItems <= 0 {
		return nil
	}
	alertsByKey := make(map[string][]skills.GovernanceAlert)
	hotspotsByKey := make(map[string]skills.GovernanceHotspot)
	transitionsByKey := make(map[string][]skills.GovernanceTransition)
	selectedKeys := make([]string, 0, maxItems)
	seen := make(map[string]struct{})
	appendKey := func(key string) {
		if key == "" || len(selectedKeys) >= maxItems {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		selectedKeys = append(selectedKeys, key)
	}
	for _, alert := range summary.Alerts {
		key := governanceSkillSnapshotKey(alert.SkillName, alert.Path)
		alertsByKey[key] = append(alertsByKey[key], alert)
		appendKey(key)
	}
	for _, hotspot := range summary.Hotspots {
		key := governanceSkillSnapshotKey(hotspot.SkillName, hotspot.Path)
		hotspotsByKey[key] = hotspot
		appendKey(key)
	}
	for _, transition := range summary.Transitions {
		key := governanceSkillSnapshotKey(transition.SkillName, transition.SkillPath)
		transitionsByKey[key] = append(transitionsByKey[key], transition)
	}
	if len(selectedKeys) == 0 {
		return nil
	}
	const maxDetailAlerts = 3
	const maxDetailTransitions = 3
	details := make([]OperatorSkillGovernanceDetail, 0, len(selectedKeys))
	for _, key := range selectedKeys {
		detail := OperatorSkillGovernanceDetail{}
		if alerts := alertsByKey[key]; len(alerts) > 0 {
			detail.SkillName = alerts[0].SkillName
			detail.Path = alerts[0].Path
			detail.FollowUpCommand = governanceAlertFollowUp(alerts[0], invariantDiagnostics)
			limit := len(alerts)
			if limit > maxDetailAlerts {
				limit = maxDetailAlerts
			}
			detail.Alerts = make([]OperatorSkillGovernanceAlert, 0, limit)
			for _, alert := range alerts[:limit] {
				detail.Alerts = append(detail.Alerts, buildOperatorGovernanceAlert(alert, invariantDiagnostics))
			}
		}
		if hotspot, ok := hotspotsByKey[key]; ok {
			if detail.SkillName == "" {
				detail.SkillName = hotspot.SkillName
			}
			if detail.Path == "" {
				detail.Path = hotspot.Path
			}
			detail.CurrentState = hotspot.CurrentState
			if detail.FollowUpCommand == "" {
				detail.FollowUpCommand = governanceHotspotFollowUp(hotspot)
			}
		}
		if transitions := transitionsByKey[key]; len(transitions) > 0 {
			if detail.SkillName == "" {
				detail.SkillName = transitions[0].SkillName
			}
			if detail.Path == "" {
				detail.Path = transitions[0].SkillPath
			}
			if detail.CurrentState == "" {
				detail.CurrentState = transitions[0].CurrentState
			}
			limit := len(transitions)
			if limit > maxDetailTransitions {
				limit = maxDetailTransitions
			}
			detail.RecentTransitions = make([]OperatorSkillGovernanceTransition, 0, limit)
			for _, transition := range transitions[:limit] {
				detail.RecentTransitions = append(detail.RecentTransitions, OperatorSkillGovernanceTransition{
					At:        formatTimestamp(transition.At),
					Action:    transition.Action,
					FromState: transition.FromState,
					ToState:   transition.ToState,
				})
			}
		}
		if detail.SkillName == "" && detail.Path == "" {
			continue
		}
		details = append(details, detail)
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

func governanceSkillSnapshotKey(skillName string, path string) string {
	if strings.TrimSpace(path) != "" {
		return filepath.Clean(path)
	}
	return strings.TrimSpace(skillName)
}

func governanceSelectionItemKey(kind string, path string, variant string) string {
	parts := []string{strings.TrimSpace(kind), governanceSkillSnapshotKey("", path)}
	if trimmedVariant := strings.TrimSpace(variant); trimmedVariant != "" {
		parts = append(parts, trimmedVariant)
	}
	return strings.Join(parts, "|")
}

func governanceSelectionSectionKey(kind string) string {
	switch strings.TrimSpace(kind) {
	case "detail":
		return "detail"
	case "blocked-invariant":
		return "blocked-invariant"
	case "current-gap":
		return "current-gap"
	case "drift-hotspot":
		return "drift-hotspot"
	default:
		return ""
	}
}

func governanceSelectionSectionLabel(sectionKey string) string {
	switch strings.TrimSpace(sectionKey) {
	case "detail":
		return "Detail previews"
	case "blocked-invariant":
		return "Blocked invariants"
	case "current-gap":
		return "Current gaps"
	case "drift-hotspot":
		return "Drift hotspots"
	default:
		return "Governance items"
	}
}

func normalizeGovernanceSelectionItemKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "|")
	if len(parts) >= 2 {
		parts[0] = strings.TrimSpace(parts[0])
		parts[1] = governanceSkillSnapshotKey("", parts[1])
	}
	if len(parts) >= 3 {
		parts[2] = strings.TrimSpace(parts[2])
	}
	return strings.Join(parts, "|")
}

func buildOperatorGovernanceAlert(alert skills.GovernanceAlert, invariantDiagnostics map[string]skills.GovernanceInvariantDrift) OperatorSkillGovernanceAlert {
	snapshot := OperatorSkillGovernanceAlert{
		Severity:        alert.Severity,
		SkillName:       alert.SkillName,
		Reason:          alert.Reason,
		FollowUpCommand: governanceAlertFollowUp(alert, invariantDiagnostics),
	}
	if diagnostic, ok := invariantDiagnostics[governanceInvariantDiagnosticKey(alert.SkillName, alert.Path, alert.Reason)]; ok {
		repairable := diagnostic.Repairable
		snapshot.Repairable = &repairable
		snapshot.BlockedBy = diagnostic.BlockedBy
	}
	return snapshot
}

func buildInvariantDiagnosticLookup(reconciliation skills.GovernanceReconciliation) map[string]skills.GovernanceInvariantDrift {
	if len(reconciliation.InvariantDrifts) == 0 {
		return nil
	}
	diagnostics := make(map[string]skills.GovernanceInvariantDrift, len(reconciliation.InvariantDrifts))
	for _, drift := range reconciliation.InvariantDrifts {
		diagnostics[governanceInvariantDiagnosticKey(drift.SkillName, drift.CurrentPath, drift.Reason)] = drift
	}
	return diagnostics
}

func governanceInvariantDiagnosticKey(skillName string, path string, reason string) string {
	parts := []string{
		strings.TrimSpace(skillName),
		strings.TrimSpace(reason),
	}
	if trimmedPath := strings.TrimSpace(path); trimmedPath != "" {
		parts = append(parts, filepath.Clean(trimmedPath))
	} else {
		parts = append(parts, "")
	}
	return strings.Join(parts, "|")
}

func governanceAlertFollowUp(alert skills.GovernanceAlert, invariantDiagnostics map[string]skills.GovernanceInvariantDrift) string {
	if diagnostic, ok := invariantDiagnostics[governanceInvariantDiagnosticKey(alert.SkillName, alert.Path, alert.Reason)]; ok {
		if suggested := strings.TrimSpace(diagnostic.SuggestedFollowUp); suggested != "" {
			return suggested
		}
	}
	target := filepath.Base(strings.TrimSpace(alert.Path))
	if target == "" || target == "." {
		return "avatars skills reconcile"
	}
	switch {
	case alert.Reason == "corrupted-history",
		alert.Reason == "missing-history":
		return fmt.Sprintf("avatars skills repair-history %s", target)
	case strings.HasPrefix(alert.Reason, "broken-transition-chain:"),
		strings.HasPrefix(alert.Reason, "invalid-transition:"),
		strings.HasPrefix(alert.Reason, "invalid-start-transition:"):
		return fmt.Sprintf("avatars skills repair-invariants %s", target)
	default:
		return fmt.Sprintf("avatars skills show %s", target)
	}
}

func governanceHotspotFollowUp(hotspot skills.GovernanceHotspot) string {
	target := filepath.Base(strings.TrimSpace(hotspot.Path))
	if target == "" || target == "." {
		return "avatars skills timeline"
	}
	return fmt.Sprintf("avatars skills show %s", target)
}

func governanceMissingCurrentFollowUp(entry skills.GovernanceLedgerEntry) string {
	target := filepath.Base(strings.TrimSpace(entry.SkillPath))
	if target == "" || target == "." {
		return "avatars skills reconcile"
	}
	return fmt.Sprintf("avatars skills restore-guide %s", target)
}

func governanceUntrackedCurrentFollowUp(record skills.GovernanceCurrentRecord) string {
	if strings.TrimSpace(record.Path) == "" {
		return "avatars skills reconcile"
	}
	return "avatars skills sync"
}

func governanceDriftHotspotFollowUp(path string, drifts []OperatorSkillGovernanceDrift) string {
	target := filepath.Base(strings.TrimSpace(path))
	if target == "" || target == "." {
		return "avatars skills reconcile"
	}
	for _, drift := range drifts {
		if drift.Category == "history" {
			return fmt.Sprintf("avatars skills repair-history %s", target)
		}
	}
	for _, drift := range drifts {
		if drift.Category == "metadata" {
			return fmt.Sprintf("avatars skills repair-metadata %s", target)
		}
	}
	for _, drift := range drifts {
		if drift.Category == "state" || drift.Category == "path" || drift.Category == "content" {
			return "avatars skills sync"
		}
	}
	return fmt.Sprintf("avatars skills show %s", target)
}

func (s *Server) history() []events.Envelope {
	liveHistory := []events.Envelope(nil)
	if s != nil && s.store != nil {
		liveHistory = s.store.History()
	}
	if len(s.archivedHistory) == 0 {
		return liveHistory
	}
	merged := make([]events.Envelope, 0, len(s.archivedHistory)+len(liveHistory))
	merged = append(merged, s.archivedHistory...)
	merged = append(merged, liveHistory...)
	return merged
}

func collectRunSnapshots(history []events.Envelope) []OperatorRunSnapshot {
	byRunID := make(map[string]*OperatorRunSnapshot)
	for _, event := range history {
		if event.RunID == "" {
			continue
		}
		run, ok := byRunID[event.RunID]
		if !ok {
			run = &OperatorRunSnapshot{RunID: event.RunID, TaskID: event.TaskID, Status: "running", StartedAt: formatTimestamp(event.EmittedAt)}
			byRunID[event.RunID] = run
		}
		if run.TaskID == "" {
			run.TaskID = event.TaskID
		}
		run.EventCount++
		run.UpdatedAt = formatTimestamp(event.EmittedAt)
		if event.Sequence > run.LastSequence {
			run.LastSequence = event.Sequence
		}
		switch event.Type {
		case "run.started":
			run.StartedAt = formatTimestamp(event.EmittedAt)
			if run.Input == "" {
				run.Input = stringValue(event.Payload, "input")
			}
		case "llm.status_updated":
			if provider := stringValue(event.Payload, "provider"); provider != "" {
				run.Provider = provider
			}
			if model := stringValue(event.Payload, "model"); model != "" {
				run.Model = model
			}
		case "telemetry.updated":
			if telemetry := telemetryFromPayload(event.Payload); telemetry != nil {
				run.DurationMillis = telemetry.DurationMillis
				run.LLMRequests = telemetry.LLMRequests
				run.ToolRequests = telemetry.ToolRequests
				run.VerificationRuns = telemetry.VerificationRuns
				if telemetry.Provider != "" {
					run.Provider = telemetry.Provider
				}
				if telemetry.Model != "" {
					run.Model = telemetry.Model
				}
				if telemetry.Status != "" {
					run.Status = telemetry.Status
				}
			}
		case "run.completed":
			run.Status = runStatusValue(event.Payload)
			run.CompletedAt = formatTimestamp(event.EmittedAt)
			if summary := stringValue(event.Payload, "summary"); summary != "" {
				run.Summary = summary
			}
		}
		if run.DurationMillis == 0 && run.StartedAt != "" && run.CompletedAt != "" {
			run.DurationMillis = durationFromTimestamps(run.StartedAt, run.CompletedAt)
		}
	}
	runs := make([]OperatorRunSnapshot, 0, len(byRunID))
	for _, run := range byRunID {
		runs = append(runs, *run)
	}
	sort.Slice(runs, func(i int, j int) bool {
		if runs[i].UpdatedAt != runs[j].UpdatedAt {
			return runs[i].UpdatedAt > runs[j].UpdatedAt
		}
		return runs[i].LastSequence > runs[j].LastSequence
	})
	return runs
}

func LoadTranscriptHistory(paths []string) ([]events.Envelope, error) {
	history := make([]events.Envelope, 0)
	for _, path := range paths {
		loaded, err := loadTranscriptFile(path)
		if err != nil {
			return nil, err
		}
		history = append(history, loaded...)
	}
	return history, nil
}

func loadTranscriptFile(path string) ([]events.Envelope, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	decoder := json.NewDecoder(file)
	history := make([]events.Envelope, 0, 64)
	for {
		var envelope events.Envelope
		if err := decoder.Decode(&envelope); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode transcript %s: %w", path, err)
		}
		history = append(history, envelope)
	}
	return history, nil
}

func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func durationFromTimestamps(startedAt string, completedAt string) int {
	if startedAt == "" || completedAt == "" {
		return 0
	}
	started, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return 0
	}
	completed, err := time.Parse(time.RFC3339, completedAt)
	if err != nil {
		return 0
	}
	if completed.Before(started) {
		return 0
	}
	return int(completed.Sub(started).Milliseconds())
}

func latestRunID(history []events.Envelope) string {
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].RunID != "" {
			return history[index].RunID
		}
	}
	return ""
}

func filterHistoryByRun(history []events.Envelope, runID string) []events.Envelope {
	if runID == "" {
		return history
	}
	filtered := make([]events.Envelope, 0, len(history))
	for _, event := range history {
		if event.RunID == runID {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func findRunSnapshot(runs []OperatorRunSnapshot, runID string) *OperatorRunSnapshot {
	for index := range runs {
		if runs[index].RunID == runID {
			return &runs[index]
		}
	}
	return nil
}

func runStatusValue(payload map[string]any) string {
	status := stringValue(payload, "status")
	if status == "" {
		return "completed"
	}
	return status
}

func diffOperatorLLM(configured *llm.ProviderStatus, configuredWarning string, latest *llm.ProviderStatus, latestWarning string) *OperatorDiffSnapshot {
	lines := make([]string, 0, 8)
	fields := make([]OperatorDiffField, 0, 9)
	if configured == nil && latest == nil {
		if configuredWarning != "" || latestWarning != "" {
			if configuredWarning != latestWarning {
				appendDiffField(&fields, &lines, "warning", "Warning", configuredWarning, latestWarning)
			}
		}
		return finalizeDiffSnapshot(lines, fields)
	}
	if configured == nil && latest != nil {
		lines = append(lines, fmt.Sprintf("configured state missing; latest run provider=%s model=%s", latest.Name, latest.Model))
	}
	if configured != nil && latest == nil {
		lines = append(lines, fmt.Sprintf("latest run state missing; configured provider=%s model=%s", configured.Name, configured.Model))
	}
	appendDiffField(&fields, &lines, "provider", "Provider", providerName(configured), providerName(latest))
	appendDiffField(&fields, &lines, "family", "Family", providerFamily(configured), providerFamily(latest))
	appendDiffField(&fields, &lines, "model", "Model", providerModel(configured), providerModel(latest))
	appendDiffField(&fields, &lines, "think_mode", "Think Mode", providerThinkMode(configured), providerThinkMode(latest))
	appendDiffField(&fields, &lines, "web_search", "Web Search", providerWebSearch(configured), providerWebSearch(latest))
	appendDiffField(&fields, &lines, "base_url", "Base URL", providerBaseURL(configured), providerBaseURL(latest))
	appendDiffField(&fields, &lines, "api_key_env", "API Key Env", providerAPIKeyEnv(configured), providerAPIKeyEnv(latest))
	appendDiffField(&fields, &lines, "local", "Local", providerLocal(configured), providerLocal(latest))
	appendDiffField(&fields, &lines, "warning", "Warning", configuredWarning, latestWarning)
	if len(lines) == 0 {
		lines = append(lines, "configured and latest run LLM states match")
	}
	return finalizeDiffSnapshot(lines, fields)
}

func appendDiffField(fields *[]OperatorDiffField, lines *[]string, key string, label string, configured string, latest string) {
	field := OperatorDiffField{
		Key:        key,
		Label:      label,
		Configured: configured,
		ViewedRun:  latest,
		Changed:    configured != latest,
		State:      "aligned",
	}
	if field.Changed {
		field.State = "changed"
		*lines = append(*lines, fmt.Sprintf("%s drift: configured=%q latest=%q", key, configured, latest))
	}
	*fields = append(*fields, field)
}

func finalizeDiffSnapshot(lines []string, fields []OperatorDiffField) *OperatorDiffSnapshot {
	changedCount := 0
	for _, field := range fields {
		if field.Changed {
			changedCount++
		}
	}
	hasDiff := changedCount > 0
	if len(fields) == 0 && len(lines) == 1 && lines[0] == "configured and latest run LLM states match" {
		hasDiff = false
	}
	return &OperatorDiffSnapshot{HasDiff: hasDiff, ChangedCount: changedCount, TotalCount: len(fields), Lines: lines, Fields: fields}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func providerStatusFromPayload(payload map[string]any) (*llm.ProviderStatus, string) {
	if payload == nil {
		return nil, ""
	}
	status := &llm.ProviderStatus{
		Name:                    stringValue(payload, "provider"),
		Family:                  stringValue(payload, "family"),
		Model:                   stringValue(payload, "model"),
		BaseURL:                 stringValue(payload, "base_url"),
		APIKeyEnv:               stringValue(payload, "api_key_env"),
		SupportsWebSearch:       boolValue(payload, "supports_web_search"),
		WebSearchEnabled:        boolValue(payload, "web_search_enabled"),
		ThinkMode:               llm.ThinkMode(stringValue(payload, "think_mode")),
		RequiresExplicitModel:   boolValue(payload, "requires_explicit_model"),
		RequiresExplicitBaseURL: boolValue(payload, "requires_explicit_base_url"),
		Local:                   boolValue(payload, "local"),
	}
	if status.Name == "" {
		return nil, stringValue(payload, "warning")
	}
	return status, stringValue(payload, "warning")
}

func telemetryFromPayload(payload map[string]any) *OperatorTelemetrySnapshot {
	if payload == nil {
		return nil
	}
	return &OperatorTelemetrySnapshot{
		Mode:             stringValue(payload, "mode"),
		Status:           stringValue(payload, "status"),
		Provider:         stringValue(payload, "provider"),
		Model:            stringValue(payload, "model"),
		LLMRequests:      intValue(payload, "llm_requests"),
		LLMCompletions:   intValue(payload, "llm_completions"),
		LLMFailures:      intValue(payload, "llm_failures"),
		LLMFallbacks:     intValue(payload, "llm_fallbacks"),
		ToolRequests:     intValue(payload, "tool_requests"),
		ToolCompletions:  intValue(payload, "tool_completions"),
		ToolFailures:     intValue(payload, "tool_failures"),
		VerificationRuns: intValue(payload, "verification_runs"),
		DurationMillis:   intValue(payload, "duration_ms"),
		UsageKnown:       boolValue(payload, "usage_known"),
		PromptTokens:     intValue(payload, "prompt_tokens"),
		CompletionTokens: intValue(payload, "completion_tokens"),
		TotalTokens:      intValue(payload, "total_tokens"),
		CostMicros:       int64(intValue(payload, "cost_micros")),
	}
}

func providerName(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return status.Name
}

func providerFamily(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return status.Family
}

func providerModel(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return status.Model
}

func providerThinkMode(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return string(status.ThinkMode)
}

func providerWebSearch(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return yesNo(status.WebSearchEnabled)
}

func providerBaseURL(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return status.BaseURL
}

func providerAPIKeyEnv(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return status.APIKeyEnv
}

func providerLocal(status *llm.ProviderStatus) string {
	if status == nil {
		return ""
	}
	return yesNo(status.Local)
}

func stringValue(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

func boolValue(payload map[string]any, key string) bool {
	if payload == nil {
		return false
	}
	value, ok := payload[key]
	if !ok {
		return false
	}
	flag, _ := value.(bool)
	return flag
}

func intValue(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	value, ok := payload[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}
