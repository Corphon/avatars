// Verified: complies with `skills.md` test_file_requirements — no network listeners or external process startup; uses mocks/locals only.
package stage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/events"
	"avatars/internal/llm"
	memstore "avatars/internal/memory"
	"avatars/internal/skills"
)

func TestServer_HistoryEndpointReturnsEvents(t *testing.T) {
	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "demo"}})

	server, err := NewServer(store, nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var history []events.Envelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &history); err != nil {
		t.Fatalf("unmarshal history failed: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 event, got %d", len(history))
	}
}

func TestServer_HistoryEndpointFiltersByRunID(t *testing.T) {
	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "first"}})
	store.Append(events.Envelope{EventID: "evt-2", Sequence: 2, RunID: "run-2", TaskID: "task-2", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "second"}})

	server, err := NewServer(store, nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events?run_id=run-2", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var history []events.Envelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &history); err != nil {
		t.Fatalf("unmarshal history failed: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 filtered event, got %d", len(history))
	}
	if history[0].RunID != "run-2" {
		t.Fatalf("expected run-2 history, got %q", history[0].RunID)
	}
}

func TestServer_OperatorEndpointReturnsConfiguredAndLatestRunState(t *testing.T) {
	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "Older run"}})
	store.Append(events.Envelope{EventID: "evt-2", Sequence: 2, RunID: "run-1", TaskID: "task-1", Type: "run.completed", EmittedAt: time.Now().UTC(), Payload: map[string]any{"status": "completed", "summary": "Older summary"}})
	store.Append(events.Envelope{EventID: "evt-3", Sequence: 3, RunID: "run-2", TaskID: "task-2", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "Latest run"}})
	store.Append(events.Envelope{EventID: "evt-4", Sequence: 4, RunID: "run-2", TaskID: "task-2", Type: "llm.status_updated", EmittedAt: time.Now().UTC(), Payload: map[string]any{"provider": "openrouter", "family": "openai-compatible", "model": "openai/gpt-4.1-mini", "supports_web_search": true, "web_search_enabled": true, "think_mode": "auto", "base_url": "https://openrouter.ai/api/v1", "api_key_env": "OPENROUTER_API_KEY", "warning": "latest run warning"}})
	store.Append(events.Envelope{EventID: "evt-5", Sequence: 5, RunID: "run-2", TaskID: "task-2", Type: "telemetry.updated", EmittedAt: time.Now().UTC(), Payload: map[string]any{"mode": "run", "status": "completed", "provider": "openrouter", "model": "openai/gpt-4.1-mini", "duration_ms": 84, "llm_requests": 1, "llm_completions": 1, "llm_failures": 0, "llm_fallbacks": 0, "tool_requests": 2, "tool_completions": 2, "tool_failures": 0, "verification_runs": 1, "usage_known": true, "prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30, "cost_micros": 2500}})
	store.Append(events.Envelope{EventID: "evt-6", Sequence: 6, RunID: "run-2", TaskID: "task-2", Type: "run.completed", EmittedAt: time.Now().UTC(), Payload: map[string]any{"status": "completed", "summary": "Latest summary"}})

	server, err := NewServer(store, nil, llm.ProviderStatus{Name: "google", Family: "genkit", Model: "googleai/gemini-2.5-flash", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}, "configured warning")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithTaskFeedback("task-2", "stable", memstore.Snapshot{
		Verification:        &memstore.VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: filepath.Join(".avatars", "tasks", "task-2", "memory", "verifier", "run-2-patch.json"), Warnings: []string{"CGO is not enabled"}, Checks: []string{"go test -race ./... => PARTIAL"}, UpdatedAt: time.Now().UTC()},
		EvaluationRecords:   []memstore.EvaluationRecord{{Summary: "CGO is not enabled"}},
		WarmLessons:         []memstore.WarmLesson{{Summary: "Repeat verifier before marking patch complete."}},
		EvolutionCandidates: []memstore.EvolutionCandidate{{Summary: "Add deterministic follow-up for partial verifier verdicts."}},
		ProjectLessons:      []memstore.ProjectLesson{{Summary: "Promote partial-verifier follow-up into the shared runtime playbook."}},
	})
	server = server.WithSkillGovernance(skills.GovernanceSummary{
		TrackedSkills:  3,
		GeneratedCount: 1,
		ApprovedCount:  1,
		ArchivedCount:  1,
		DisabledCount:  0,
		Alerts: []skills.GovernanceAlert{
			{Severity: "error", SkillName: "Task Survey Skill", Reason: "broken-transition-chain:approved->generated", Path: filepath.Join("skills", "archive", "task-survey-skill.md")},
			{Severity: "warning", SkillName: "Manual Skill", Reason: "missing-history", Path: filepath.Join("skills", "approved", "manual-skill.md")},
		},
		Hotspots: []skills.GovernanceHotspot{{SkillName: "Task Survey Skill", CurrentState: "archived", Path: filepath.Join("skills", "archive", "task-survey-skill.md"), TransitionCount: 5}},
		Transitions: []skills.GovernanceTransition{
			{SkillName: "Task Survey Skill", CurrentState: "archived", SkillPath: filepath.Join("skills", "archive", "task-survey-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 9, 0, 0, 0, time.UTC), Action: "archive", FromState: "approved", ToState: "archived", Path: filepath.Join("skills", "archive", "task-survey-skill.md")}},
			{SkillName: "Task Survey Skill", CurrentState: "archived", SkillPath: filepath.Join("skills", "archive", "task-survey-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 8, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", Path: filepath.Join("skills", "approved", "task-survey-skill.md")}},
			{SkillName: "Manual Skill", CurrentState: "approved", SkillPath: filepath.Join("skills", "approved", "manual-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 7, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", Path: filepath.Join("skills", "approved", "manual-skill.md")}},
		},
	}, skills.GovernanceReconciliation{
		InvariantDrifts: []skills.GovernanceInvariantDrift{
			{SkillName: "Task Survey Skill", CurrentPath: filepath.Join("skills", "archive", "task-survey-skill.md"), CurrentState: "archived", Reason: "broken-transition-chain:approved->generated", Repairable: true, SuggestedFollowUp: "avatars skills repair-invariants task-survey-skill.md"},
		},
	}, skills.GovernanceLedger{})

	req := httptest.NewRequest(http.MethodGet, "/api/operator", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.ConfiguredLLM == nil || snapshot.ConfiguredLLM.Name != "google" {
		t.Fatalf("expected configured google llm, got %+v", snapshot.ConfiguredLLM)
	}
	if snapshot.SelectedRunID != "run-2" {
		t.Fatalf("expected latest selected run to be run-2, got %q", snapshot.SelectedRunID)
	}
	if len(snapshot.AvailableRuns) != 2 {
		t.Fatalf("expected 2 available runs, got %d", len(snapshot.AvailableRuns))
	}
	latestRun := testFindRunSnapshot(snapshot.AvailableRuns, "run-2")
	if latestRun == nil {
		t.Fatal("expected run-2 snapshot in available runs")
	}
	if latestRun.Provider != "openrouter" || latestRun.Model != "openai/gpt-4.1-mini" {
		t.Fatalf("expected timeline provider metadata, got %+v", latestRun)
	}
	if latestRun.EventCount != 4 || latestRun.LLMRequests != 1 || latestRun.ToolRequests != 2 || latestRun.VerificationRuns != 1 {
		t.Fatalf("expected timeline counters, got %+v", latestRun)
	}
	if latestRun.StartedAt == "" || latestRun.CompletedAt == "" || latestRun.UpdatedAt == "" {
		t.Fatalf("expected timeline timestamps, got %+v", latestRun)
	}
	if snapshot.ConfiguredLLMWarning != "configured warning" {
		t.Fatalf("expected configured warning, got %q", snapshot.ConfiguredLLMWarning)
	}
	if snapshot.LatestRunLLM == nil || snapshot.LatestRunLLM.Name != "openrouter" {
		t.Fatalf("expected latest run openrouter llm, got %+v", snapshot.LatestRunLLM)
	}
	if snapshot.LatestRunLLMWarning != "latest run warning" {
		t.Fatalf("expected latest run warning, got %q", snapshot.LatestRunLLMWarning)
	}
	if snapshot.LatestTelemetry == nil || snapshot.LatestTelemetry.DurationMillis != 84 {
		t.Fatalf("expected latest telemetry duration 84, got %+v", snapshot.LatestTelemetry)
	}
	if snapshot.TaskFeedback == nil || snapshot.TaskFeedback.Verification == nil {
		t.Fatalf("expected task feedback snapshot, got %+v", snapshot.TaskFeedback)
	}
	if snapshot.TaskFeedback.Verification.Verdict != "PARTIAL" {
		t.Fatalf("expected task verification verdict PARTIAL, got %+v", snapshot.TaskFeedback.Verification)
	}
	if snapshot.TaskFeedback.LatestNonPassVerification == "" {
		t.Fatalf("expected latest non-pass verification summary, got %+v", snapshot.TaskFeedback)
	}
	if snapshot.TaskFeedback.LatestNonPassFollowUp != "avatars verify --task task-2" {
		t.Fatalf("expected latest non-pass verification follow-up, got %+v", snapshot.TaskFeedback)
	}
	if snapshot.TaskFeedback.ReverifyStatus != "pending reverify for latest non-pass verification" {
		t.Fatalf("expected pending reverify status, got %+v", snapshot.TaskFeedback)
	}
	if snapshot.TaskFeedback.WorkflowNodeNextAction != "run verifier reverify before closure" {
		t.Fatalf("expected workflow node verifier cue, got %+v", snapshot.TaskFeedback)
	}
	if section := testFindTaskFeedbackSection(snapshot.TaskFeedback.Sections, "workflow-node"); section == nil || !strings.Contains(section.Summary, "next_action=run verifier reverify before closure") {
		t.Fatalf("expected workflow node cue section, got %+v", snapshot.TaskFeedback.Sections)
	}
	if len(snapshot.TaskFeedback.RemediationProposals) != 2 {
		t.Fatalf("expected remediation proposals, got %+v", snapshot.TaskFeedback)
	}
	if snapshot.TaskFeedback.RemediationProposals[0].ProposalID != "inspect-latest-non-pass" {
		t.Fatalf("expected inspect remediation proposal first, got %+v", snapshot.TaskFeedback.RemediationProposals)
	}
	if snapshot.TaskFeedback.RemediationProposals[1].ProposalID != "remediate-latest-non-pass" {
		t.Fatalf("expected remediation attempt proposal second, got %+v", snapshot.TaskFeedback.RemediationProposals)
	}
	if snapshot.TaskFeedback.RemediationProposals[1].FollowUpCommand != "avatars verify --task task-2" {
		t.Fatalf("expected remediation proposal follow-up, got %+v", snapshot.TaskFeedback.RemediationProposals)
	}
	if len(snapshot.TaskFeedback.SuggestedCommands) != 2 {
		t.Fatalf("expected suggested task commands, got %+v", snapshot.TaskFeedback)
	}
	if snapshot.TaskFeedback.SuggestedCommands[0] != "avatars tasks show task-2 --view verifier" {
		t.Fatalf("expected verifier view suggestion first, got %+v", snapshot.TaskFeedback.SuggestedCommands)
	}
	if snapshot.TaskFeedback.SuggestedCommands[1] != "avatars verify --task task-2" {
		t.Fatalf("expected verify suggestion second, got %+v", snapshot.TaskFeedback.SuggestedCommands)
	}
	if snapshot.TaskFeedback.EvaluationCount != 1 || snapshot.TaskFeedback.WarmLessonCount != 1 || snapshot.TaskFeedback.EvolutionCount != 1 || snapshot.TaskFeedback.ProjectLessonCount != 1 {
		t.Fatalf("expected task feedback counters, got %+v", snapshot.TaskFeedback)
	}
	if snapshot.SkillGovernance == nil {
		t.Fatalf("expected skill governance snapshot, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.Scope != "repository" || snapshot.SkillGovernance.TrackedSkills != 3 || snapshot.SkillGovernance.AlertCount != 2 {
		t.Fatalf("expected skill governance counts, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.ErrorCount != 1 || snapshot.SkillGovernance.WarningCount != 1 {
		t.Fatalf("expected skill governance severity counts, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.BlockedInvariantCount != 0 {
		t.Fatalf("expected no blocked invariant count, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant != nil {
		t.Fatalf("expected no default blocked invariant selection, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.TopAlert == "" || snapshot.SkillGovernance.TopHotspot == "" {
		t.Fatalf("expected top governance entries, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.OverviewCommand != "avatars skills reconcile" {
		t.Fatalf("expected governance overview command, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.TopAlertCommand != "avatars skills repair-invariants task-survey-skill.md" {
		t.Fatalf("expected top alert repair command, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.TopHotspotCommand != "avatars skills show task-survey-skill.md" {
		t.Fatalf("expected top hotspot show command, got %+v", snapshot.SkillGovernance)
	}
	if len(snapshot.SkillGovernance.Alerts) != 2 {
		t.Fatalf("expected bounded alert list, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.Alerts[0].FollowUpCommand != "avatars skills repair-invariants task-survey-skill.md" {
		t.Fatalf("expected alert follow-up command, got %+v", snapshot.SkillGovernance.Alerts)
	}
	if snapshot.SkillGovernance.Alerts[0].Repairable == nil || !*snapshot.SkillGovernance.Alerts[0].Repairable {
		t.Fatalf("expected repairable invariant alert, got %+v", snapshot.SkillGovernance.Alerts[0])
	}
	if len(snapshot.SkillGovernance.Hotspots) != 1 {
		t.Fatalf("expected bounded hotspot list, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.Hotspots[0].FollowUpCommand != "avatars skills show task-survey-skill.md" {
		t.Fatalf("expected hotspot follow-up command, got %+v", snapshot.SkillGovernance.Hotspots)
	}
	if len(snapshot.SkillGovernance.Details) != 2 {
		t.Fatalf("expected bounded detail previews, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDetail == nil || snapshot.SkillGovernance.SelectedDetail.SkillName != "Task Survey Skill" {
		t.Fatalf("expected default selected governance detail, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.Details[0].FollowUpCommand != "avatars skills repair-invariants task-survey-skill.md" {
		t.Fatalf("expected first detail follow-up command, got %+v", snapshot.SkillGovernance.Details)
	}
	if len(snapshot.SkillGovernance.Details[0].Alerts) == 0 || snapshot.SkillGovernance.Details[0].Alerts[0].Repairable == nil || !*snapshot.SkillGovernance.Details[0].Alerts[0].Repairable {
		t.Fatalf("expected repairable detail alert, got %+v", snapshot.SkillGovernance.Details[0])
	}
	if snapshot.SkillGovernance.Details[0].CurrentState != "archived" || len(snapshot.SkillGovernance.Details[0].RecentTransitions) != 2 {
		t.Fatalf("expected first detail state and transitions, got %+v", snapshot.SkillGovernance.Details[0])
	}
	if snapshot.SkillGovernance.Details[1].SkillName != "Manual Skill" || snapshot.SkillGovernance.Details[1].CurrentState != "approved" {
		t.Fatalf("expected second detail to use transition fallback state, got %+v", snapshot.SkillGovernance.Details[1])
	}
	if snapshot.LLMDiff == nil || !snapshot.LLMDiff.HasDiff {
		t.Fatalf("expected llm diff to be present, got %+v", snapshot.LLMDiff)
	}
	if snapshot.LLMDiff.TotalCount == 0 {
		t.Fatalf("expected structured diff fields, got %+v", snapshot.LLMDiff)
	}
	providerField := findDiffField(snapshot.LLMDiff.Fields, "provider")
	if providerField == nil || !providerField.Changed || providerField.Configured != "google" || providerField.ViewedRun != "openrouter" {
		t.Fatalf("expected provider field drift, got %+v", providerField)
	}
	thinkField := findDiffField(snapshot.LLMDiff.Fields, "think_mode")
	if thinkField == nil || thinkField.Changed {
		t.Fatalf("expected aligned think mode field, got %+v", thinkField)
	}
	if len(snapshot.LLMDiff.Lines) == 0 {
		t.Fatal("expected llm diff lines")
	}
	if snapshot.LLMDiff.Lines[0] == "configured and latest run LLM states match" {
		t.Fatalf("expected diff line, got %q", snapshot.LLMDiff.Lines[0])
	}
	if snapshot.LatestSummary != "Latest summary" {
		t.Fatalf("expected latest summary, got %q", snapshot.LatestSummary)
	}
}

func TestServer_OperatorSnapshotSurfacesAwaitingApprovalRunStatus(t *testing.T) {
	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-awaiting", TaskID: "task-awaiting", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "Blocked run"}})
	store.Append(events.Envelope{EventID: "evt-2", Sequence: 2, RunID: "run-awaiting", TaskID: "task-awaiting", Type: "tool.awaiting_approval", EmittedAt: time.Now().UTC(), Payload: map[string]any{"tool": "write", "error": "approval required: guarded write needs confirmation"}})
	store.Append(events.Envelope{EventID: "evt-3", Sequence: 3, RunID: "run-awaiting", TaskID: "task-awaiting", Type: "run.completed", EmittedAt: time.Now().UTC(), Payload: map[string]any{"status": "awaiting_approval", "summary": "approval required: guarded write needs confirmation"}})
	store.Append(events.Envelope{EventID: "evt-4", Sequence: 4, RunID: "run-awaiting", TaskID: "task-awaiting", Type: "telemetry.updated", EmittedAt: time.Now().UTC(), Payload: map[string]any{"mode": "run", "status": "awaiting_approval", "duration_ms": 42, "tool_requests": 1, "tool_failures": 0}})

	server, err := NewServer(store, nil, llm.ProviderStatus{Name: "google", Family: "genkit", Model: "googleai/gemini-2.5-flash", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}

	snapshot := server.operatorSnapshot("", "", "")
	if snapshot.SelectedRunID != "run-awaiting" {
		t.Fatalf("expected selected run run-awaiting, got %q", snapshot.SelectedRunID)
	}
	if snapshot.LatestRunStatus != "awaiting_approval" {
		t.Fatalf("expected latest run status awaiting_approval, got %+v", snapshot)
	}
	if snapshot.LatestSummary != "approval required: guarded write needs confirmation" {
		t.Fatalf("expected blocked run summary, got %q", snapshot.LatestSummary)
	}
	if len(snapshot.AvailableRuns) != 1 {
		t.Fatalf("expected one available run, got %d", len(snapshot.AvailableRuns))
	}
	if snapshot.AvailableRuns[0].Status != "awaiting_approval" {
		t.Fatalf("expected available run status awaiting_approval, got %+v", snapshot.AvailableRuns[0])
	}
	if snapshot.AvailableRuns[0].CompletedAt == "" {
		t.Fatalf("expected blocked run to have completed timestamp, got %+v", snapshot.AvailableRuns[0])
	}
	if snapshot.LatestTelemetry == nil || snapshot.LatestTelemetry.Status != "awaiting_approval" {
		t.Fatalf("expected latest telemetry status awaiting_approval, got %+v", snapshot.LatestTelemetry)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesLatestReverifyClosure(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-covered", "stable", memstore.Snapshot{
		Verification:        &memstore.VerificationSnapshot{Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: filepath.Join(".avatars", "tasks", "task-covered", "memory", "verifier", "run-pass.json"), UpdatedAt: time.Now().UTC()},
		VerificationHistory: []memstore.VerificationSnapshot{{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: filepath.Join(".avatars", "tasks", "task-covered", "memory", "verifier", "run-partial.json"), UpdatedAt: time.Now().UTC().Add(-time.Minute)}},
		EvaluationRecords:   []memstore.EvaluationRecord{{Cause: "verification_reverify_covered", Summary: "Verification PASS now covers the latest non-pass verification: PARTIAL via patch.", UpdatedAt: time.Now().UTC()}, {Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Details: []string{"proposal_id: remediate-latest-non-pass", "expected_targets: note.txt, process_record.md"}, UpdatedAt: time.Now().UTC().Add(-time.Nanosecond)}},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.ReverifyStatus != "latest non-pass verification is covered by a later PASS" {
		t.Fatalf("expected covered reverify status, got %+v", feedback)
	}
	if feedback.LatestReverifyRemediation == "" {
		t.Fatalf("expected latest reverify remediation, got %+v", feedback)
	}
	if len(feedback.RemediationProposals) != 2 {
		t.Fatalf("expected remediation proposals, got %+v", feedback)
	}
	if feedback.LatestReverifyRemediationProposalID != "remediate-latest-non-pass" {
		t.Fatalf("expected remediation proposal id, got %+v", feedback)
	}
	if len(feedback.LatestReverifyRemediationTargets) != 2 {
		t.Fatalf("expected remediation targets, got %+v", feedback)
	}
	if feedback.LatestReverifyClosure == "" {
		t.Fatalf("expected latest reverify closure, got %+v", feedback)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesLatestReverifyAttempt(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-pending", "stable", memstore.Snapshot{
		Verification:      &memstore.VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: filepath.Join(".avatars", "tasks", "task-pending", "memory", "verifier", "run-partial.json"), UpdatedAt: time.Now().UTC()},
		EvaluationRecords: []memstore.EvaluationRecord{{Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Details: []string{"proposal_id: remediate-latest-non-pass", "expected_targets: note.txt"}, UpdatedAt: time.Now().UTC()}},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.ReverifyStatus != "pending reverify for latest non-pass verification" {
		t.Fatalf("expected pending reverify status, got %+v", feedback)
	}
	if feedback.LatestReverifyAttempt == "" {
		t.Fatalf("expected latest reverify attempt, got %+v", feedback)
	}
	if len(feedback.RemediationProposals) != 2 {
		t.Fatalf("expected remediation proposals, got %+v", feedback)
	}
	if feedback.LatestReverifyAttemptProposalID != "remediate-latest-non-pass" {
		t.Fatalf("expected attempt proposal id, got %+v", feedback)
	}
	if len(feedback.LatestReverifyAttemptTargets) != 1 || feedback.LatestReverifyAttemptTargets[0] != "note.txt" {
		t.Fatalf("expected latest reverify attempt targets, got %+v", feedback)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesLatestPermissionDenial(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-denied", "stable", memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{{Cause: "tool_permission_denied", Summary: "Tool write/file_write was denied: write tool target escapes sandbox: ../notes.txt", Details: []string{"decision_source: tool_sandbox"}, UpdatedAt: time.Now().UTC()}},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.LatestPermissionDenial == "" {
		t.Fatalf("expected latest permission denial, got %+v", feedback)
	}
	if feedback.LatestPermissionDenialSource != "tool_sandbox" {
		t.Fatalf("expected latest permission denial source, got %+v", feedback)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesLatestApprovalRequired(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-awaiting-approval", "stable", memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed", "origin_run_id: run-origin", "origin_task_id: task-awaiting-approval"}, UpdatedAt: time.Now().UTC().Add(2 * time.Second)},
			{Cause: "tool_approval_required", Summary: "Tool patch/file_patch is awaiting approval.", Details: []string{"decision_source: runtime_permission_mode", "permission_mode: default", "approval_key: approval-key-b", "operation: file_patch"}, UpdatedAt: time.Now().UTC().Add(time.Second)},
			{Cause: "tool_approval_required", Summary: "Tool write/file_write is awaiting approval: approval required: Permission mode default requires approval before mutating write actions, and approval prompts are not implemented yet.", Details: []string{"decision_source: runtime_permission_mode", "permission_mode: default", "approval_key: approval-key-a", "operation: file_write"}, UpdatedAt: time.Now().UTC()},
		},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.LatestApprovalRequired == "" {
		t.Fatalf("expected latest approval required, got %+v", feedback)
	}
	if feedback.LatestApprovalRequiredSource != "runtime_permission_mode" {
		t.Fatalf("expected latest approval required source, got %+v", feedback)
	}
	if feedback.LatestApprovalRequiredKey != "approval-key-b" {
		t.Fatalf("expected latest approval key approval-key-b, got %+v", feedback)
	}
	if feedback.LatestApprovalRequiredMode != "default" {
		t.Fatalf("expected latest approval mode default, got %+v", feedback)
	}
	if feedback.TaskStatus != "awaiting_approval" {
		t.Fatalf("expected task status awaiting_approval, got %+v", feedback)
	}
	if feedback.FinalityReadiness != "review" || feedback.FinalityBlockerCount != 1 {
		t.Fatalf("expected finality readiness review with one blocker, got %+v", feedback)
	}
	if feedback.PendingApprovalCount != 2 {
		t.Fatalf("expected pending approval count 2, got %+v", feedback)
	}
	if len(feedback.PendingApprovals) != 2 {
		t.Fatalf("expected pending approval snapshots, got %+v", feedback)
	}
	if feedback.PendingApprovals[0].ApprovalKey != "approval-key-b" {
		t.Fatalf("expected latest pending approval snapshot first, got %+v", feedback.PendingApprovals)
	}
	if feedback.WorkflowNodeNextAction != "inspect pending approval before continuation" {
		t.Fatalf("expected workflow node approval cue, got %+v", feedback)
	}
	if section := testFindTaskFeedbackSection(feedback.Sections, "workflow-node"); section == nil || !strings.Contains(section.Summary, "next_action=inspect pending approval before continuation") {
		t.Fatalf("expected workflow node cue section, got %+v", feedback.Sections)
	}
	if len(feedback.Sections) == 0 || !strings.Contains(feedback.Sections[len(feedback.Sections)-1].Summary, "pending_approvals=2") {
		t.Fatalf("expected verification section summary to include pending approvals, got %+v", feedback.Sections)
	}
	if section := testFindTaskFeedbackSection(feedback.Sections, "feedback"); section == nil || !strings.Contains(section.Summary, "status=awaiting_approval") || !strings.Contains(section.Summary, "finality=review") || !strings.Contains(section.Summary, "blockers=1") {
		t.Fatalf("expected feedback section summary to include task finality, got %+v", feedback.Sections)
	}
	if !strings.Contains(feedback.Sections[len(feedback.Sections)-1].Summary, "task_status=awaiting_approval") {
		t.Fatalf("expected verification section summary to include task status, got %+v", feedback.Sections)
	}
}

func TestBuildTaskFeedbackSnapshot_UsesSharedWorkflowNodeGovernanceCue(t *testing.T) {
	snapshot := memstore.Snapshot{EvaluationRecords: []memstore.EvaluationRecord{
		{Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Details: []string{"retry_attempt_id: retry-build", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
		{Cause: "node_work_evidence", Summary: "Retried workflow node completed.", Details: []string{"retry_attempt_id: retry-build", "node_id: node-build", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verification_report_path: verifier/retry-build.json"}},
		{Cause: "failed_node_retry_scheduler_resume", Summary: "Dependent workflow nodes resumed.", Details: []string{"retry_attempt_id: retry-build", "scheduler_resume_status: completed"}},
		{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated.", Details: []string{"retry_attempt_id: retry-build", "status: completed"}},
	}}
	expectedGovernance := memstore.WorkflowNodeGovernanceSnapshotForTask(snapshot)
	expectedCue := expectedGovernance.NextActionCue
	if expectedCue != "manual review required before risky continuation" || expectedGovernance.RetryClosureVerifierReportPath != "verifier/retry-build.json" {
		t.Fatalf("expected shared workflow node governance snapshot, got %+v", expectedGovernance)
	}

	feedback := buildTaskFeedbackSnapshot("task-retry-closed", "stable", snapshot)
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.WorkflowNodeNextAction != expectedCue {
		t.Fatalf("expected stage cue to match shared cue %q, got %+v", expectedCue, feedback)
	}
	if feedback.WorkflowNodeVerifierReportPath != "verifier/retry-build.json" {
		t.Fatalf("expected stage verifier report path, got %+v", feedback)
	}
	section := testFindTaskFeedbackSection(feedback.Sections, "workflow-node")
	if section == nil || !strings.Contains(section.Summary, "next_action="+expectedCue) || !strings.Contains(section.Summary, "verifier_report=present") {
		t.Fatalf("expected workflow-node section to include shared cue %q, got %+v", expectedCue, feedback.Sections)
	}
}

func TestBuildTaskFeedbackSnapshot_TaskFinalityShowsVerifierBlocker(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-finality", "stable", memstore.Snapshot{
		Verification: &memstore.VerificationSnapshot{Tool: "verify", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL."},
		EvaluationRecords: []memstore.EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed", "origin_run_id: run-origin", "origin_task_id: task-finality"}},
		},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.TaskStatus != "awaiting_verification" {
		t.Fatalf("expected task status awaiting_verification, got %+v", feedback)
	}
	if feedback.FinalityReadiness != "review" || feedback.FinalityBlockerCount != 0 {
		t.Fatalf("expected finality readiness review without double-counting verifier pending, got %+v", feedback)
	}
	if section := testFindTaskFeedbackSection(feedback.Sections, "feedback"); section == nil || !strings.Contains(section.Summary, "status=awaiting_verification") || !strings.Contains(section.Summary, "finality=review") || !strings.Contains(section.Summary, "blockers=0") {
		t.Fatalf("expected feedback section to show verifier blocker finality, got %+v", feedback.Sections)
	}
	if section := testFindTaskFeedbackSection(feedback.Sections, "verification"); section == nil || !strings.Contains(section.Summary, "task_status=awaiting_verification") {
		t.Fatalf("expected verification section to show verifier blocker task status, got %+v", feedback.Sections)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesLatestApprovalReplay(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-replayed-approval", "stable", memstore.Snapshot{
		EvaluationRecords: []memstore.EvaluationRecord{{
			Cause:     "tool_approval_replayed",
			Summary:   "Continued approved request for write/file_write: Wrote notes.txt.",
			Details:   []string{"decision_source: task_operator_replay", "approval_key: approval-key-r", "continuation_transcript: .avatars/tasks/task-replayed-approval/transcripts/replay.jsonl", "continuation_run_id: run-continued-1", "continuation_task_id: task-replayed-approval"},
			UpdatedAt: time.Now().UTC(),
		}},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.LatestApprovalReplay == "" {
		t.Fatalf("expected latest approval replay, got %+v", feedback)
	}
	if feedback.LatestApprovalReplaySource != "task_operator_replay" {
		t.Fatalf("expected latest approval replay source, got %+v", feedback)
	}
	if feedback.LatestApprovalReplayKey != "approval-key-r" {
		t.Fatalf("expected latest approval replay key, got %+v", feedback)
	}
	if feedback.LatestApprovalReplayTranscript != ".avatars/tasks/task-replayed-approval/transcripts/replay.jsonl" {
		t.Fatalf("expected latest approval replay transcript, got %+v", feedback)
	}
	if feedback.LatestApprovalContinuation == "" {
		t.Fatalf("expected latest approval continuation, got %+v", feedback)
	}
	if feedback.LatestApprovalContinuationSource != "task_operator_replay" {
		t.Fatalf("expected latest approval continuation source, got %+v", feedback)
	}
	if feedback.LatestApprovalContinuationKey != "approval-key-r" {
		t.Fatalf("expected latest approval continuation key, got %+v", feedback)
	}
	if feedback.LatestApprovalContinuationTranscript != ".avatars/tasks/task-replayed-approval/transcripts/replay.jsonl" {
		t.Fatalf("expected latest approval continuation transcript, got %+v", feedback)
	}
	if feedback.LatestApprovalContinuationRunID != "run-continued-1" {
		t.Fatalf("expected latest approval continuation run id, got %+v", feedback)
	}
	if feedback.LatestApprovalContinuationTaskID != "task-replayed-approval" {
		t.Fatalf("expected latest approval continuation task id, got %+v", feedback)
	}
	if len(feedback.Sections) == 0 || !strings.Contains(feedback.Sections[len(feedback.Sections)-1].Summary, "continuation=recent") {
		t.Fatalf("expected verification section summary to include continuation cue, got %+v", feedback.Sections)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesExecutionChain(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-chain", "stable", memstore.Snapshot{
		RecentEvents: []string{
			"Tool completed: read process_record.md.",
			"Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context.",
			"Researcher assigned: Survey current repository context",
			"Planner decomposed the task: Planner -> Researcher -> Builder -> Critic -> Synthesizer.",
		},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if len(feedback.ExecutionChain) != 3 {
		t.Fatalf("expected execution chain length 3, got %+v", feedback)
	}
	if feedback.ExecutionChain[0] != "Planner decomposed the task: Planner -> Researcher -> Builder -> Critic -> Synthesizer." {
		t.Fatalf("expected chronological execution chain, got %+v", feedback.ExecutionChain)
	}
	if feedback.ExecutionChain[2] != "Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context." {
		t.Fatalf("expected handoff at execution chain tail, got %+v", feedback.ExecutionChain)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesLatestAvatarMessages(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-messages", "stable", memstore.Snapshot{
		RecentEvents: []string{
			"Synthesizer summary: [broadcast task] completed planning run across 5 avatars and 5 workflow nodes.",
			"Critic challenge: [broadcast task] approval remains required for generated skills, and concurrent execution is still deferred.",
			"Planner follow-up: [to Researcher] repository context is sufficient for the current builder implementation slice; proceed with the bounded skill generation path.",
			"Researcher ask: [to Planner] confirm whether the current repository context is sufficient before the builder locks the next implementation slice.",
			"Builder report: [broadcast task] generated candidate skill at skills/generated/task-survey-skill.md.",
		},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if feedback.LatestAvatarReport == "" || feedback.LatestAvatarAsk == "" || feedback.LatestAvatarChallenge == "" || feedback.LatestAvatarSummary == "" {
		t.Fatalf("expected latest avatar messages, got %+v", feedback)
	}
	if feedback.Collaboration == nil {
		t.Fatalf("expected collaboration snapshot, got %+v", feedback)
	}
	if feedback.Collaboration.LatestAvatarReport != feedback.LatestAvatarReport {
		t.Fatalf("expected collaboration report mirror, got %+v", feedback.Collaboration)
	}
	if feedback.Collaboration.LatestAvatarAsk != feedback.LatestAvatarAsk {
		t.Fatalf("expected collaboration ask mirror, got %+v", feedback.Collaboration)
	}
	if feedback.Collaboration.LatestAvatarAskRoute != "to Planner" {
		t.Fatalf("expected collaboration ask route cue, got %+v", feedback.Collaboration)
	}
	if feedback.Collaboration.LatestAvatarAskStatus != "followed-up via to Researcher" {
		t.Fatalf("expected collaboration ask status cue, got %+v", feedback.Collaboration)
	}
	if feedback.Collaboration.LatestAvatarFollowUpRoute != "to Researcher" {
		t.Fatalf("expected collaboration follow-up route cue, got %+v", feedback.Collaboration)
	}
	if feedback.Collaboration.FocusCue != "followed-up via to Researcher" {
		t.Fatalf("expected collaboration focus cue, got %+v", feedback.Collaboration)
	}
	if feedback.Collaboration.LatestAvatarReportRoute != "broadcast task" || feedback.Collaboration.LatestAvatarChallengeRoute != "broadcast task" || feedback.Collaboration.LatestAvatarSummaryRoute != "broadcast task" {
		t.Fatalf("expected collaboration route cues, got %+v", feedback.Collaboration)
	}
	if len(feedback.Sections) < 2 {
		t.Fatalf("expected task feedback section summaries, got %+v", feedback.Sections)
	}
	if feedback.Sections[0].Key != "collaboration" || !strings.Contains(feedback.Sections[0].Summary, "focus=followed-up via to Researcher") || !strings.Contains(feedback.Sections[0].Summary, "ask_status=followed-up via to Researcher") || !strings.Contains(feedback.Sections[0].Summary, "report=broadcast task") || !strings.Contains(feedback.Sections[0].Summary, "ask=to Planner") || !strings.Contains(feedback.Sections[0].Summary, "follow_up=to Researcher") {
		t.Fatalf("expected collaboration section summary, got %+v", feedback.Sections)
	}
}

func TestBuildTaskFeedbackSnapshot_SurfacesCompactVerificationSectionCue(t *testing.T) {
	feedback := buildTaskFeedbackSnapshot("task-verify", "stable", memstore.Snapshot{
		Verification: &memstore.VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", UpdatedAt: time.Now().UTC()},
	})
	if feedback == nil {
		t.Fatal("expected task feedback")
	}
	if len(feedback.Sections) < 2 {
		t.Fatalf("expected verification section summary, got %+v", feedback.Sections)
	}
	found := false
	for _, section := range feedback.Sections {
		if section.Key == "verification" {
			found = true
			if !strings.Contains(section.Summary, "current=PARTIAL via patch") || !strings.Contains(section.Summary, "reverify=pending") {
				t.Fatalf("unexpected verification section summary: %+v", section)
			}
		}
	}
	if !found {
		t.Fatalf("expected verification section summary, got %+v", feedback.Sections)
	}
}

func TestServer_OperatorEndpointMarksAlignedLLMState(t *testing.T) {
	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Type: "llm.status_updated", EmittedAt: time.Now().UTC(), Payload: map[string]any{"provider": "google", "family": "genkit", "model": "googleai/gemini-2.5-flash", "supports_web_search": true, "web_search_enabled": true, "think_mode": "auto"}})

	configured := llm.ProviderStatus{Name: "google", Family: "genkit", Model: "googleai/gemini-2.5-flash", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}
	server, err := NewServer(store, nil, configured, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}

	snapshot := server.operatorSnapshot("", "", "")
	if snapshot.LLMDiff == nil {
		t.Fatal("expected llm diff snapshot")
	}
	if snapshot.LLMDiff.HasDiff {
		t.Fatalf("expected aligned llm state, got %+v", snapshot.LLMDiff)
	}
	if snapshot.LLMDiff.ChangedCount != 0 {
		t.Fatalf("expected zero changed fields, got %+v", snapshot.LLMDiff)
	}
	if len(snapshot.LLMDiff.Lines) != 1 || snapshot.LLMDiff.Lines[0] != "configured and latest run LLM states match" {
		t.Fatalf("expected aligned diff summary, got %+v", snapshot.LLMDiff)
	}
	providerField := findDiffField(snapshot.LLMDiff.Fields, "provider")
	if providerField == nil || providerField.Changed {
		t.Fatalf("expected aligned provider field, got %+v", providerField)
	}
}

func TestServer_OperatorEndpointSupportsHistoricalRunSelection(t *testing.T) {
	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-1", Sequence: 1, RunID: "run-1", TaskID: "task-1", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "Historical run"}})
	store.Append(events.Envelope{EventID: "evt-2", Sequence: 2, RunID: "run-1", TaskID: "task-1", Type: "llm.status_updated", EmittedAt: time.Now().UTC(), Payload: map[string]any{"provider": "google", "family": "genkit", "model": "googleai/gemini-2.5-flash", "supports_web_search": true, "web_search_enabled": true, "think_mode": "auto"}})
	store.Append(events.Envelope{EventID: "evt-3", Sequence: 3, RunID: "run-1", TaskID: "task-1", Type: "run.completed", EmittedAt: time.Now().UTC(), Payload: map[string]any{"status": "completed", "summary": "Historical summary"}})
	store.Append(events.Envelope{EventID: "evt-4", Sequence: 4, RunID: "run-2", TaskID: "task-2", Type: "run.started", EmittedAt: time.Now().UTC(), Payload: map[string]any{"input": "Latest run"}})
	store.Append(events.Envelope{EventID: "evt-5", Sequence: 5, RunID: "run-2", TaskID: "task-2", Type: "llm.status_updated", EmittedAt: time.Now().UTC(), Payload: map[string]any{"provider": "openrouter", "family": "openai-compatible", "model": "openai/gpt-4.1-mini", "supports_web_search": true, "web_search_enabled": true, "think_mode": "auto"}})
	store.Append(events.Envelope{EventID: "evt-6", Sequence: 6, RunID: "run-2", TaskID: "task-2", Type: "run.completed", EmittedAt: time.Now().UTC(), Payload: map[string]any{"status": "completed", "summary": "Latest summary"}})

	server, err := NewServer(store, nil, llm.ProviderStatus{Name: "google", Family: "genkit", Model: "googleai/gemini-2.5-flash", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/operator?run_id=run-1", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.SelectedRunID != "run-1" {
		t.Fatalf("expected selected run run-1, got %q", snapshot.SelectedRunID)
	}
	if snapshot.LatestRunLLM == nil || snapshot.LatestRunLLM.Name != "google" {
		t.Fatalf("expected historical google llm, got %+v", snapshot.LatestRunLLM)
	}
	if snapshot.LatestSummary != "Historical summary" {
		t.Fatalf("expected historical summary, got %q", snapshot.LatestSummary)
	}
	if snapshot.LLMDiff == nil || snapshot.LLMDiff.HasDiff {
		t.Fatalf("expected historical snapshot to align with configured google provider, got %+v", snapshot.LLMDiff)
	}
}

func TestServer_OperatorEndpointSelectsGovernedSkillDetail(t *testing.T) {
	server, err := NewServer(events.NewStore(), nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithSkillGovernance(skills.GovernanceSummary{
		TrackedSkills: 2,
		ApprovedCount: 2,
		Alerts: []skills.GovernanceAlert{
			{Severity: "error", SkillName: "Manual Skill", Reason: "broken-transition-chain:approved->generated", Path: filepath.Join("skills", "approved", "manual-skill.md")},
		},
		Hotspots: []skills.GovernanceHotspot{
			{SkillName: "Task Survey Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "task-survey-skill.md"), TransitionCount: 2},
			{SkillName: "Manual Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "manual-skill.md"), TransitionCount: 1},
		},
		Transitions: []skills.GovernanceTransition{
			{SkillName: "Task Survey Skill", CurrentState: "approved", SkillPath: filepath.Join("skills", "approved", "task-survey-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 10, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", Path: filepath.Join("skills", "approved", "task-survey-skill.md")}},
			{SkillName: "Manual Skill", CurrentState: "approved", SkillPath: filepath.Join("skills", "approved", "manual-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 11, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", Path: filepath.Join("skills", "approved", "manual-skill.md")}},
		},
	}, skills.GovernanceReconciliation{
		InvariantDrifts: []skills.GovernanceInvariantDrift{
			{SkillName: "Manual Skill", CurrentPath: filepath.Join("skills", "approved", "manual-skill.md"), CurrentState: "approved", Reason: "broken-transition-chain:approved->generated", Repairable: false, BlockedBy: "ledger-broken-transition-chain:approved->generated", SuggestedFollowUp: "avatars skills ledger"},
		},
	}, skills.GovernanceLedger{})

	req := httptest.NewRequest(http.MethodGet, "/api/operator?skill_path=skills/approved/manual-skill.md", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.SkillGovernance == nil || snapshot.SkillGovernance.SelectedDetail == nil {
		t.Fatalf("expected selected governance detail, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDetailPath != filepath.Join("skills", "approved", "manual-skill.md") {
		t.Fatalf("expected selected detail path, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDetail.SkillName != "Manual Skill" {
		t.Fatalf("expected manual skill selection, got %+v", snapshot.SkillGovernance.SelectedDetail)
	}
	if snapshot.SkillGovernance.SelectedDetail.FollowUpCommand != "avatars skills ledger" {
		t.Fatalf("expected selected detail to use ledger follow-up, got %+v", snapshot.SkillGovernance.SelectedDetail)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant == nil {
		t.Fatalf("expected selected blocked invariant, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.Path != filepath.Join("skills", "approved", "manual-skill.md") {
		t.Fatalf("expected selected blocked invariant path, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if len(snapshot.SkillGovernance.SelectedDetail.Alerts) == 0 {
		t.Fatalf("expected selected detail alerts, got %+v", snapshot.SkillGovernance.SelectedDetail)
	}
	selectedAlert := snapshot.SkillGovernance.SelectedDetail.Alerts[0]
	if selectedAlert.Repairable == nil || *selectedAlert.Repairable {
		t.Fatalf("expected non-repairable selected alert, got %+v", selectedAlert)
	}
	if selectedAlert.BlockedBy != "ledger-broken-transition-chain:approved->generated" {
		t.Fatalf("expected blocker to surface in selected alert, got %+v", selectedAlert)
	}
	if selectedAlert.FollowUpCommand != "avatars skills ledger" {
		t.Fatalf("expected selected alert follow-up to use ledger command, got %+v", selectedAlert)
	}
}

func TestServer_OperatorEndpointSelectsExactGovernanceItemByKey(t *testing.T) {
	server, err := NewServer(events.NewStore(), nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithSkillGovernance(skills.GovernanceSummary{
		TrackedSkills: 1,
		ApprovedCount: 1,
		Alerts: []skills.GovernanceAlert{
			{Severity: "error", SkillName: "Manual Skill", Reason: "broken-transition-chain:approved->generated", Path: filepath.Join("skills", "approved", "manual-skill.md")},
		},
		Hotspots: []skills.GovernanceHotspot{
			{SkillName: "Manual Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "manual-skill.md"), TransitionCount: 1},
		},
	}, skills.GovernanceReconciliation{
		InvariantDrifts: []skills.GovernanceInvariantDrift{
			{SkillName: "Manual Skill", CurrentPath: filepath.Join("skills", "approved", "manual-skill.md"), CurrentState: "approved", Reason: "broken-transition-chain:approved->generated", Repairable: false, BlockedBy: "ledger-broken-transition-chain:approved->generated", SuggestedFollowUp: "avatars skills ledger"},
		},
	}, skills.GovernanceLedger{})

	req := httptest.NewRequest(http.MethodGet, "/api/operator?skill_item=blocked-invariant|skills/approved/manual-skill.md", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.SkillGovernance == nil {
		t.Fatalf("expected skill governance snapshot, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedItemKey != "blocked-invariant|"+filepath.Join("skills", "approved", "manual-skill.md") {
		t.Fatalf("expected blocked invariant item key, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedItemKind != "blocked-invariant" {
		t.Fatalf("expected blocked invariant item kind, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant == nil {
		t.Fatalf("expected exact blocked invariant selection, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDetail != nil {
		t.Fatalf("expected exact item selection to avoid selecting detail too, got %+v", snapshot.SkillGovernance.SelectedDetail)
	}
	if len(snapshot.SkillGovernance.SelectionSections) != 2 {
		t.Fatalf("expected detail and blocked invariant selection sections, got %+v", snapshot.SkillGovernance.SelectionSections)
	}
	if snapshot.SkillGovernance.SelectionSections[0].Key != "detail" || snapshot.SkillGovernance.SelectionSections[0].Count != 1 {
		t.Fatalf("expected detail section first, got %+v", snapshot.SkillGovernance.SelectionSections)
	}
	if snapshot.SkillGovernance.SelectionSections[1].Key != "blocked-invariant" || snapshot.SkillGovernance.SelectionSections[1].Count != 1 {
		t.Fatalf("expected blocked invariant section second, got %+v", snapshot.SkillGovernance.SelectionSections)
	}
	if len(snapshot.SkillGovernance.SelectionItems) != 2 {
		t.Fatalf("expected two selection items, got %+v", snapshot.SkillGovernance.SelectionItems)
	}
	if snapshot.SkillGovernance.SelectionItems[0].Section != "detail" || snapshot.SkillGovernance.SelectionItems[1].Section != "blocked-invariant" {
		t.Fatalf("expected section-tagged selection items, got %+v", snapshot.SkillGovernance.SelectionItems)
	}
	if snapshot.SkillGovernance.SelectionItems[0].FollowUpCommand != "avatars skills ledger" {
		t.Fatalf("expected detail selection item follow-up, got %+v", snapshot.SkillGovernance.SelectionItems[0])
	}
	if snapshot.SkillGovernance.SelectionItems[1].FollowUpCommand != "avatars skills ledger" {
		t.Fatalf("expected blocked invariant selection item follow-up, got %+v", snapshot.SkillGovernance.SelectionItems[1])
	}
	if snapshot.SkillGovernance.SelectionItems[0].PriorityLabel != "urgent" || snapshot.SkillGovernance.SelectionItems[0].PriorityScore != 85 {
		t.Fatalf("expected detail selection item priority, got %+v", snapshot.SkillGovernance.SelectionItems[0])
	}
	if snapshot.SkillGovernance.SelectionItems[1].PriorityLabel != "urgent" || snapshot.SkillGovernance.SelectionItems[1].PriorityScore != 100 {
		t.Fatalf("expected blocked invariant selection item priority, got %+v", snapshot.SkillGovernance.SelectionItems[1])
	}
}

func TestServer_OperatorEndpointSurfacesBlockedInvariantsOutsideBoundedAlerts(t *testing.T) {
	server, err := NewServer(events.NewStore(), nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithSkillGovernance(skills.GovernanceSummary{
		TrackedSkills: 1,
		ApprovedCount: 1,
		Alerts: []skills.GovernanceAlert{
			{Severity: "warning", SkillName: "Visible Skill", Reason: "missing-history", Path: filepath.Join("skills", "approved", "visible-skill.md")},
		},
		Hotspots: []skills.GovernanceHotspot{
			{SkillName: "Visible Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "visible-skill.md"), TransitionCount: 1},
		},
		Transitions: []skills.GovernanceTransition{
			{SkillName: "Blocked Skill", CurrentState: "approved", SkillPath: filepath.Join("skills", "approved", "blocked-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 12, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", Path: filepath.Join("skills", "approved", "blocked-skill.md")}},
			{SkillName: "Blocked Skill", CurrentState: "approved", SkillPath: filepath.Join("skills", "approved", "blocked-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 12, 30, 0, 0, time.UTC), Action: "archive", FromState: "approved", ToState: "archived", Path: filepath.Join("skills", "archive", "blocked-skill.md")}},
		},
	}, skills.GovernanceReconciliation{
		MissingCurrent: []skills.GovernanceLedgerEntry{
			{SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "archive", "blocked-skill.md"), Action: "archive", ToState: "archived"},
			{SkillName: "Other Skill", SkillPath: filepath.Join("skills", "disabled", "other-skill.md"), Action: "disable", ToState: "disabled"},
		},
		UntrackedCurrent: []skills.GovernanceCurrentRecord{
			{SkillName: "Blocked Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "blocked-skill-copy.md"), ContentDigest: "digest-copy"},
			{SkillName: "Other Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "other-skill-copy.md"), ContentDigest: "digest-other-copy"},
		},
		HistoryDrifts: []skills.GovernanceHistoryDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentState: "approved", Reason: "missing-history", ExpectedTransitions: 2, ActualTransitions: 0, ExpectedLatestAction: "approve", ActualLatestAction: ""},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), CurrentState: "approved", Reason: "corrupted-history", ExpectedTransitions: 1, ActualTransitions: 0, ExpectedLatestAction: "approve", ActualLatestAction: ""},
		},
		MetadataDrifts: []skills.GovernanceMetadataDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentState: "approved", Field: "source_task_id", Expected: "task-1", Actual: "task-2"},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), CurrentState: "approved", Field: "template_id", Expected: "template-a", Actual: "template-b"},
		},
		StateDrifts: []skills.GovernanceStateDrift{
			{SkillName: "Blocked Skill", CurrentState: "approved", LedgerState: "archived", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), LedgerPath: filepath.Join("skills", "archive", "blocked-skill.md")},
			{SkillName: "Other Skill", CurrentState: "approved", LedgerState: "disabled", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), LedgerPath: filepath.Join("skills", "disabled", "other-skill.md")},
		},
		PathDrifts: []skills.GovernancePathDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), LedgerPath: filepath.Join("skills", "archive", "blocked-skill.md")},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), LedgerPath: filepath.Join("skills", "disabled", "other-skill.md")},
		},
		ContentDrifts: []skills.GovernanceContentDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentDigest: "digest-current", LedgerDigest: "digest-ledger", CurrentState: "approved", LedgerState: "archived"},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), CurrentDigest: "digest-other-current", LedgerDigest: "digest-other-ledger", CurrentState: "approved", LedgerState: "disabled"},
		},
		InvariantDrifts: []skills.GovernanceInvariantDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentState: "approved", Reason: "broken-transition-chain:approved->generated", Repairable: false, BlockedBy: "ledger-broken-transition-chain:approved->generated", SuggestedFollowUp: "avatars skills ledger"},
		},
	}, skills.GovernanceLedger{
		Entries: []skills.GovernanceLedgerEntry{
			{At: time.Date(2026, time.May, 4, 12, 45, 0, 0, time.UTC), Action: "archive", FromState: "approved", ToState: "archived", SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "archive", "blocked-skill.md"), SourceTaskID: "task-1", SourceRunID: "run-3"},
			{At: time.Date(2026, time.May, 4, 12, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "approved", "blocked-skill.md"), SourceTaskID: "task-1", SourceRunID: "run-2"},
			{At: time.Date(2026, time.May, 4, 11, 0, 0, 0, time.UTC), Action: "generate", ToState: "generated", SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "generated", "blocked-skill.md"), SourceTaskID: "task-1", SourceRunID: "run-1"},
			{At: time.Date(2026, time.May, 4, 10, 0, 0, 0, time.UTC), Action: "generate", ToState: "generated", SkillName: "Other Skill", SkillPath: filepath.Join("skills", "generated", "other-skill.md"), SourceTaskID: "task-other", SourceRunID: "run-other"},
		},
	})

	snapshot := server.operatorSnapshot("", "", "")
	if snapshot.SkillGovernance == nil {
		t.Fatalf("expected skill governance snapshot, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.BlockedInvariantCount != 1 {
		t.Fatalf("expected one blocked invariant, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.TopBlockedInvariant == "" {
		t.Fatalf("expected top blocked invariant summary, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.TopBlockedInvariantCommand != "avatars skills ledger" {
		t.Fatalf("expected blocked invariant ledger follow-up, got %+v", snapshot.SkillGovernance)
	}
	if len(snapshot.SkillGovernance.BlockedInvariants) != 1 {
		t.Fatalf("expected blocked invariant list, got %+v", snapshot.SkillGovernance)
	}
	blocked := snapshot.SkillGovernance.BlockedInvariants[0]
	if blocked.SkillName != "Blocked Skill" || blocked.BlockedBy != "ledger-broken-transition-chain:approved->generated" {
		t.Fatalf("expected blocked invariant details, got %+v", blocked)
	}
	if blocked.FollowUpCommand != "avatars skills ledger" {
		t.Fatalf("expected blocked invariant follow-up, got %+v", blocked)
	}
	if len(blocked.RecentTransitions) != 2 {
		t.Fatalf("expected blocked invariant transitions, got %+v", blocked)
	}
	if len(blocked.RecentLedgerEntries) != 3 {
		t.Fatalf("expected blocked invariant ledger entries, got %+v", blocked)
	}
}

func TestServer_OperatorEndpointSelectsCurrentGapOutsideGovernedDetails(t *testing.T) {
	server, err := NewServer(events.NewStore(), nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithSkillGovernance(skills.GovernanceSummary{}, skills.GovernanceReconciliation{
		MissingCurrent: []skills.GovernanceLedgerEntry{
			{SkillName: "Archived Skill", SkillPath: filepath.Join("skills", "archive", "archived-skill.md"), Action: "archive", ToState: "archived", SourceTaskID: "task-archived", SourceRunID: "run-archived"},
		},
		UntrackedCurrent: []skills.GovernanceCurrentRecord{
			{SkillName: "Manual Draft", CurrentState: "approved", Path: filepath.Join("skills", "approved", "manual-draft.md"), ContentDigest: "digest-manual"},
		},
	}, skills.GovernanceLedger{})

	req := httptest.NewRequest(http.MethodGet, "/api/operator?skill_path=skills/approved/manual-draft.md", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.SkillGovernance == nil {
		t.Fatalf("expected skill governance snapshot, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.CurrentGapCount != 2 || snapshot.SkillGovernance.MissingCurrentCount != 1 || snapshot.SkillGovernance.UntrackedCurrentCount != 1 {
		t.Fatalf("expected current gap counts, got %+v", snapshot.SkillGovernance)
	}
	if len(snapshot.SkillGovernance.CurrentGaps) != 2 {
		t.Fatalf("expected current gap list, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDetail != nil {
		t.Fatalf("expected no selected detail for gap-only snapshot, got %+v", snapshot.SkillGovernance.SelectedDetail)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant != nil {
		t.Fatalf("expected no selected blocked invariant for gap-only snapshot, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if snapshot.SkillGovernance.SelectedCurrentGap == nil {
		t.Fatalf("expected selected current gap, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedCurrentGapPath != filepath.Join("skills", "approved", "manual-draft.md") {
		t.Fatalf("expected selected current gap path, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedCurrentGap.Category != "untracked-current" {
		t.Fatalf("expected untracked current selection, got %+v", snapshot.SkillGovernance.SelectedCurrentGap)
	}
	if snapshot.SkillGovernance.SelectedCurrentGap.FollowUpCommand != "avatars skills sync" {
		t.Fatalf("expected sync follow-up for untracked current, got %+v", snapshot.SkillGovernance.SelectedCurrentGap)
	}
}

func TestServer_OperatorEndpointSelectsDriftHotspotOutsideOtherGovernanceSelections(t *testing.T) {
	server, err := NewServer(events.NewStore(), nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithSkillGovernance(skills.GovernanceSummary{}, skills.GovernanceReconciliation{
		HistoryDrifts: []skills.GovernanceHistoryDrift{
			{SkillName: "Orphaned Skill", CurrentPath: filepath.Join("skills", "approved", "orphaned-skill.md"), CurrentState: "approved", Reason: "missing-history", ExpectedTransitions: 2, ActualTransitions: 0, ExpectedLatestAction: "approve", ActualLatestAction: ""},
		},
		MetadataDrifts: []skills.GovernanceMetadataDrift{
			{SkillName: "Orphaned Skill", CurrentPath: filepath.Join("skills", "approved", "orphaned-skill.md"), CurrentState: "approved", Field: "source_task_id", Expected: "task-expected", Actual: "task-actual"},
		},
		StateDrifts: []skills.GovernanceStateDrift{
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), CurrentState: "approved", LedgerState: "archived", LedgerPath: filepath.Join("skills", "archive", "other-skill.md")},
		},
	}, skills.GovernanceLedger{})

	req := httptest.NewRequest(http.MethodGet, "/api/operator?skill_path=skills/approved/orphaned-skill.md", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.SkillGovernance == nil {
		t.Fatalf("expected skill governance snapshot, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.DriftHotspotCount != 2 {
		t.Fatalf("expected two drift hotspots, got %+v", snapshot.SkillGovernance)
	}
	if len(snapshot.SkillGovernance.DriftHotspots) != 2 {
		t.Fatalf("expected bounded drift hotspot list, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDetail != nil {
		t.Fatalf("expected no selected detail for drift hotspot snapshot, got %+v", snapshot.SkillGovernance.SelectedDetail)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant != nil {
		t.Fatalf("expected no selected blocked invariant for drift hotspot snapshot, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if snapshot.SkillGovernance.SelectedCurrentGap != nil {
		t.Fatalf("expected no selected current gap for drift hotspot snapshot, got %+v", snapshot.SkillGovernance.SelectedCurrentGap)
	}
	if snapshot.SkillGovernance.SelectedDriftHotspot == nil {
		t.Fatalf("expected selected drift hotspot, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDriftHotspotPath != filepath.Join("skills", "approved", "orphaned-skill.md") {
		t.Fatalf("expected selected drift hotspot path, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDriftHotspot.SkillName != "Orphaned Skill" {
		t.Fatalf("expected orphaned skill hotspot, got %+v", snapshot.SkillGovernance.SelectedDriftHotspot)
	}
	if len(snapshot.SkillGovernance.SelectedDriftHotspot.Drifts) != 2 {
		t.Fatalf("expected grouped drift entries, got %+v", snapshot.SkillGovernance.SelectedDriftHotspot)
	}
	if snapshot.SkillGovernance.SelectedDriftHotspot.Drifts[0].Category != "history" {
		t.Fatalf("expected history drift first, got %+v", snapshot.SkillGovernance.SelectedDriftHotspot.Drifts)
	}
	if snapshot.SkillGovernance.SelectedDriftHotspot.FollowUpCommand != "avatars skills repair-history orphaned-skill.md" {
		t.Fatalf("expected history repair follow-up, got %+v", snapshot.SkillGovernance.SelectedDriftHotspot)
	}
}

func TestServer_OperatorEndpointSelectsBlockedInvariantOutsideGovernedDetails(t *testing.T) {
	server, err := NewServer(events.NewStore(), nil, llm.ProviderStatus{}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithSkillGovernance(skills.GovernanceSummary{
		TrackedSkills: 1,
		ApprovedCount: 1,
		Alerts: []skills.GovernanceAlert{
			{Severity: "warning", SkillName: "Visible Skill", Reason: "missing-history", Path: filepath.Join("skills", "approved", "visible-skill.md")},
		},
		Hotspots: []skills.GovernanceHotspot{
			{SkillName: "Visible Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "visible-skill.md"), TransitionCount: 1},
		},
		Transitions: []skills.GovernanceTransition{
			{SkillName: "Blocked Skill", CurrentState: "approved", SkillPath: filepath.Join("skills", "approved", "blocked-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 12, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", Path: filepath.Join("skills", "approved", "blocked-skill.md")}},
			{SkillName: "Blocked Skill", CurrentState: "approved", SkillPath: filepath.Join("skills", "approved", "blocked-skill.md"), SkillHistoryEntry: skills.SkillHistoryEntry{At: time.Date(2026, time.May, 4, 12, 30, 0, 0, time.UTC), Action: "archive", FromState: "approved", ToState: "archived", Path: filepath.Join("skills", "archive", "blocked-skill.md")}},
		},
	}, skills.GovernanceReconciliation{
		MissingCurrent: []skills.GovernanceLedgerEntry{
			{SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "archive", "blocked-skill.md"), Action: "archive", ToState: "archived"},
			{SkillName: "Other Skill", SkillPath: filepath.Join("skills", "disabled", "other-skill.md"), Action: "disable", ToState: "disabled"},
		},
		UntrackedCurrent: []skills.GovernanceCurrentRecord{
			{SkillName: "Blocked Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "blocked-skill-copy.md"), ContentDigest: "digest-copy"},
			{SkillName: "Other Skill", CurrentState: "approved", Path: filepath.Join("skills", "approved", "other-skill-copy.md"), ContentDigest: "digest-other-copy"},
		},
		HistoryDrifts: []skills.GovernanceHistoryDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentState: "approved", Reason: "missing-history", ExpectedTransitions: 2, ActualTransitions: 0, ExpectedLatestAction: "approve", ActualLatestAction: ""},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), CurrentState: "approved", Reason: "corrupted-history", ExpectedTransitions: 1, ActualTransitions: 0, ExpectedLatestAction: "approve", ActualLatestAction: ""},
		},
		MetadataDrifts: []skills.GovernanceMetadataDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentState: "approved", Field: "source_task_id", Expected: "task-1", Actual: "task-2"},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), CurrentState: "approved", Field: "template_id", Expected: "template-a", Actual: "template-b"},
		},
		StateDrifts: []skills.GovernanceStateDrift{
			{SkillName: "Blocked Skill", CurrentState: "approved", LedgerState: "archived", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), LedgerPath: filepath.Join("skills", "archive", "blocked-skill.md")},
			{SkillName: "Other Skill", CurrentState: "approved", LedgerState: "disabled", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), LedgerPath: filepath.Join("skills", "disabled", "other-skill.md")},
		},
		PathDrifts: []skills.GovernancePathDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), LedgerPath: filepath.Join("skills", "archive", "blocked-skill.md")},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), LedgerPath: filepath.Join("skills", "disabled", "other-skill.md")},
		},
		ContentDrifts: []skills.GovernanceContentDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentDigest: "digest-current", LedgerDigest: "digest-ledger", CurrentState: "approved", LedgerState: "archived"},
			{SkillName: "Other Skill", CurrentPath: filepath.Join("skills", "approved", "other-skill.md"), CurrentDigest: "digest-other-current", LedgerDigest: "digest-other-ledger", CurrentState: "approved", LedgerState: "disabled"},
		},
		InvariantDrifts: []skills.GovernanceInvariantDrift{
			{SkillName: "Blocked Skill", CurrentPath: filepath.Join("skills", "approved", "blocked-skill.md"), CurrentState: "approved", Reason: "broken-transition-chain:approved->generated", Repairable: false, BlockedBy: "ledger-broken-transition-chain:approved->generated", SuggestedFollowUp: "avatars skills ledger"},
		},
	}, skills.GovernanceLedger{
		Entries: []skills.GovernanceLedgerEntry{
			{At: time.Date(2026, time.May, 4, 12, 45, 0, 0, time.UTC), Action: "archive", FromState: "approved", ToState: "archived", SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "archive", "blocked-skill.md"), SourceTaskID: "task-1", SourceRunID: "run-3"},
			{At: time.Date(2026, time.May, 4, 12, 0, 0, 0, time.UTC), Action: "approve", FromState: "generated", ToState: "approved", SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "approved", "blocked-skill.md"), SourceTaskID: "task-1", SourceRunID: "run-2"},
			{At: time.Date(2026, time.May, 4, 11, 0, 0, 0, time.UTC), Action: "generate", ToState: "generated", SkillName: "Blocked Skill", SkillPath: filepath.Join("skills", "generated", "blocked-skill.md"), SourceTaskID: "task-1", SourceRunID: "run-1"},
			{At: time.Date(2026, time.May, 4, 10, 0, 0, 0, time.UTC), Action: "generate", ToState: "generated", SkillName: "Other Skill", SkillPath: filepath.Join("skills", "generated", "other-skill.md"), SourceTaskID: "task-other", SourceRunID: "run-other"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/operator?skill_path=skills/approved/blocked-skill.md", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.SkillGovernance == nil {
		t.Fatalf("expected skill governance snapshot, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedDetail != nil {
		t.Fatalf("expected no governed skill detail match for blocked-only selection, got %+v", snapshot.SkillGovernance.SelectedDetail)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant == nil {
		t.Fatalf("expected selected blocked invariant, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariantPath != filepath.Join("skills", "approved", "blocked-skill.md") {
		t.Fatalf("expected selected blocked invariant path, got %+v", snapshot.SkillGovernance)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.SkillName != "Blocked Skill" {
		t.Fatalf("expected blocked skill selection, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.FollowUpCommand != "avatars skills ledger" {
		t.Fatalf("expected blocked invariant ledger follow-up, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if len(snapshot.SkillGovernance.SelectedBlockedInvariant.RecentTransitions) != 2 {
		t.Fatalf("expected selected blocked invariant transitions, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if len(snapshot.SkillGovernance.SelectedBlockedInvariant.RecentLedgerEntries) != 3 {
		t.Fatalf("expected selected blocked invariant ledger entries, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RecentLedgerEntries[0].Action != "archive" {
		t.Fatalf("expected latest ledger action first, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RecentLedgerEntries)
	}
	if len(snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts) != 7 {
		t.Fatalf("expected selected blocked invariant related drifts, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts[0].Category != "history" {
		t.Fatalf("expected history drift first, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts[1].Category != "metadata" {
		t.Fatalf("expected metadata drift second, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts[2].Category != "state" {
		t.Fatalf("expected state drift third, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts[3].Category != "path" {
		t.Fatalf("expected path drift fourth, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts[4].Category != "content" {
		t.Fatalf("expected content drift fifth, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts[5].Category != "missing-current" {
		t.Fatalf("expected missing-current drift sixth, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts)
	}
	if snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts[6].Category != "untracked-current" {
		t.Fatalf("expected untracked-current drift seventh, got %+v", snapshot.SkillGovernance.SelectedBlockedInvariant.RelatedDrifts)
	}
}

func TestServer_OperatorEndpointIncludesArchivedTranscriptRuns(t *testing.T) {
	tempDir := t.TempDir()
	archivedPath := filepath.Join(tempDir, "archived.jsonl")
	archivedEvents := []events.Envelope{
		{EventID: "evt-a1", Sequence: 1, RunID: "run-archived", TaskID: "task-1", Type: "run.started", EmittedAt: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC), Payload: map[string]any{"input": "Archived run"}},
		{EventID: "evt-a2", Sequence: 2, RunID: "run-archived", TaskID: "task-1", Type: "llm.status_updated", EmittedAt: time.Date(2026, 4, 28, 10, 0, 1, 0, time.UTC), Payload: map[string]any{"provider": "google", "family": "genkit", "model": "googleai/gemini-2.5-flash", "supports_web_search": true, "web_search_enabled": true, "think_mode": "auto"}},
		{EventID: "evt-a3", Sequence: 3, RunID: "run-archived", TaskID: "task-1", Type: "run.completed", EmittedAt: time.Date(2026, 4, 28, 10, 0, 2, 0, time.UTC), Payload: map[string]any{"status": "completed", "summary": "Archived summary"}},
	}
	writeTranscriptFile(t, archivedPath, archivedEvents)

	archivedHistory, err := LoadTranscriptHistory([]string{archivedPath})
	if err != nil {
		t.Fatalf("load transcript history failed: %v", err)
	}

	store := events.NewStore()
	store.Append(events.Envelope{EventID: "evt-l1", Sequence: 1, RunID: "run-live", TaskID: "task-1", Type: "run.started", EmittedAt: time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC), Payload: map[string]any{"input": "Live run"}})
	store.Append(events.Envelope{EventID: "evt-l2", Sequence: 2, RunID: "run-live", TaskID: "task-1", Type: "run.completed", EmittedAt: time.Date(2026, 4, 29, 10, 0, 2, 0, time.UTC), Payload: map[string]any{"status": "completed", "summary": "Live summary"}})

	server, err := NewServer(store, nil, llm.ProviderStatus{Name: "google", Family: "genkit", Model: "googleai/gemini-2.5-flash", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}, "")
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	server = server.WithArchivedHistory(archivedHistory)

	latestSnapshot := server.operatorSnapshot("", "", "")
	if latestSnapshot.SelectedRunID != "run-live" {
		t.Fatalf("expected live run to remain latest, got %q", latestSnapshot.SelectedRunID)
	}
	if len(latestSnapshot.AvailableRuns) != 2 {
		t.Fatalf("expected archived and live runs, got %d", len(latestSnapshot.AvailableRuns))
	}

	req := httptest.NewRequest(http.MethodGet, "/api/operator?run_id=run-archived", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}

	var snapshot OperatorSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("unmarshal operator snapshot failed: %v", err)
	}
	if snapshot.SelectedRunID != "run-archived" {
		t.Fatalf("expected archived run selection, got %q", snapshot.SelectedRunID)
	}
	if snapshot.LatestSummary != "Archived summary" {
		t.Fatalf("expected archived summary, got %q", snapshot.LatestSummary)
	}
	if snapshot.LatestRunLLM == nil || snapshot.LatestRunLLM.Name != "google" {
		t.Fatalf("expected archived llm status, got %+v", snapshot.LatestRunLLM)
	}

	historyReq := httptest.NewRequest(http.MethodGet, "/api/events?run_id=run-archived", nil)
	historyRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(historyRecorder, historyReq)
	if historyRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", historyRecorder.Code)
	}
	var history []events.Envelope
	if err := json.Unmarshal(historyRecorder.Body.Bytes(), &history); err != nil {
		t.Fatalf("unmarshal archived history failed: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected archived run event history, got %d", len(history))
	}
}

func findDiffField(fields []OperatorDiffField, key string) *OperatorDiffField {
	for index := range fields {
		if fields[index].Key == key {
			return &fields[index]
		}
	}
	return nil
}

func testFindRunSnapshot(runs []OperatorRunSnapshot, runID string) *OperatorRunSnapshot {
	for index := range runs {
		if runs[index].RunID == runID {
			return &runs[index]
		}
	}
	return nil
}

func testFindTaskFeedbackSection(sections []OperatorTaskFeedbackSectionSnapshot, key string) *OperatorTaskFeedbackSectionSnapshot {
	for index := range sections {
		if sections[index].Key == key {
			return &sections[index]
		}
	}
	return nil
}

func writeTranscriptFile(t *testing.T, path string, history []events.Envelope) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create transcript file failed: %v", err)
	}
	defer func() {
		_ = file.Close()
	}()
	encoder := json.NewEncoder(file)
	for _, envelope := range history {
		if err := encoder.Encode(envelope); err != nil {
			t.Fatalf("encode transcript event failed: %v", err)
		}
	}
}
