package memory

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/llm"
	runtelemetry "avatars/internal/telemetry"
)

func TestSQLiteStore_RoundTripsHotSnapshot(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	records := []SummaryRecord{
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Scope: ScopeStable, Summary: "Stable summary", Status: "completed", BoundaryKind: "run_terminal"},
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Scope: ScopeTask, Summary: "Task summary"},
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", AvatarID: "avatar-planner", Scope: ScopeAvatar, Role: "Planner", Summary: "Planner summary"},
	}
	for _, record := range records {
		if err := store.UpsertSummary(record); err != nil {
			t.Fatalf("upsert summary failed: %v", err)
		}
	}
	for _, record := range []EventRecord{
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", EventID: "evt-1", EventType: "task.decomposed", Summary: "Planner decomposed the task into 4 workflow nodes."},
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", AvatarID: "avatar-builder", EventID: "evt-2", EventType: "tool.completed", Summary: "Tool completed: read process_record.md."},
	} {
		if err := store.RecordEvent(record); err != nil {
			t.Fatalf("record event failed: %v", err)
		}
	}
	if err := store.RecordVerification(VerificationRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Tool: "write", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: filepath.Join(".avatars", "memory", "verifier", "run-a-write.json"), Warnings: []string{"environment warning"}, Checks: []string{"go test ./... => PASS"}}); err != nil {
		t.Fatalf("record verification failed: %v", err)
	}
	if err := store.RecordEvaluation(EvaluationRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Tool: "write", Kind: "verifier_verdict", Verdict: "PASS", Cause: "verification_completed", Summary: "Verification finished with PASS.", Source: "verification", ReportPath: filepath.Join(".avatars", "memory", "verifier", "run-a-write.json"), Details: []string{"go test ./... => PASS"}}); err != nil {
		t.Fatalf("record evaluation failed: %v", err)
	}
	if err := store.RecordWarmLesson(WarmLessonRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Kind: "workflow", Summary: "Keep the planner decomposition stable before execution.", Source: "task_summary", Confidence: "medium"}); err != nil {
		t.Fatalf("record warm lesson failed: %v", err)
	}
	if err := store.RecordTelemetry(TelemetryRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Snapshot: runtelemetry.Snapshot{Mode: "run", Status: "completed", Provider: "google", Model: "googleai/gemini-2.5-flash", LLMRequests: 1, LLMCompletions: 1, LLMFallbacks: 1, ToolRequests: 1, ToolCompletions: 1, DurationMillis: 42, UpdatedAt: time.Now().UTC()}}); err != nil {
		t.Fatalf("record telemetry failed: %v", err)
	}
	if err := store.RecordLLMStatus(LLMStatusRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Status: llm.ProviderStatus{Name: "google", Family: "genkit", Model: "googleai/gemini-2.5-flash", APIKeyEnv: "GEMINI_API_KEY or GOOGLE_API_KEY", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}}); err != nil {
		t.Fatalf("record llm status failed: %v", err)
	}
	if err := store.RecordEvolutionCandidate(EvolutionCandidateRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Kind: "workflow_pattern", Summary: "Promote this lesson into a reusable workflow pattern: Keep the planner decomposition stable before execution.", Source: "warm_lesson", Priority: "medium", Status: "open"}); err != nil {
		t.Fatalf("record evolution candidate failed: %v", err)
	}

	snapshot, err := store.LoadSnapshot("session-a")
	if err != nil {
		t.Fatalf("load snapshot failed: %v", err)
	}
	if snapshot.StableSummary != "Stable summary" {
		t.Fatalf("expected stable summary, got %q", snapshot.StableSummary)
	}
	if snapshot.TaskSummary != "Task summary" {
		t.Fatalf("expected task summary, got %q", snapshot.TaskSummary)
	}
	if snapshot.AvatarSummaries["avatar-planner"] != "Planner summary" {
		t.Fatalf("expected planner summary, got %q", snapshot.AvatarSummaries["avatar-planner"])
	}
	if len(snapshot.RecentEvents) != 2 {
		t.Fatalf("expected 2 recent events, got %d", len(snapshot.RecentEvents))
	}
	if snapshot.Telemetry == nil {
		t.Fatal("expected telemetry snapshot")
	}
	if snapshot.Telemetry.DurationMillis != 42 {
		t.Fatalf("expected telemetry duration 42, got %d", snapshot.Telemetry.DurationMillis)
	}
	if snapshot.LLMStatus == nil {
		t.Fatal("expected llm status snapshot")
	}
	if snapshot.LLMStatus.Name != "google" {
		t.Fatalf("expected llm status provider google, got %q", snapshot.LLMStatus.Name)
	}
	if snapshot.RecentEvents[0] != "Tool completed: read process_record.md." {
		t.Fatalf("expected most recent event first, got %q", snapshot.RecentEvents[0])
	}
	if snapshot.Verification == nil {
		t.Fatal("expected verification snapshot")
	}
	if snapshot.Verification.Verdict != "PASS" {
		t.Fatalf("expected verification verdict PASS, got %q", snapshot.Verification.Verdict)
	}
	if snapshot.Verification.ReportPath == "" {
		t.Fatal("expected verification report path")
	}
	if len(snapshot.VerificationHistory) != 1 {
		t.Fatalf("expected verification history length 1, got %d", len(snapshot.VerificationHistory))
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected evaluation record length 1, got %d", len(snapshot.EvaluationRecords))
	}
	if snapshot.EvaluationRecords[0].Tool != "write" {
		t.Fatalf("expected evaluation record tool write, got %q", snapshot.EvaluationRecords[0].Tool)
	}
	if len(snapshot.WarmLessons) != 1 {
		t.Fatalf("expected warm lesson length 1, got %d", len(snapshot.WarmLessons))
	}
	if len(snapshot.EvolutionCandidates) != 1 {
		t.Fatalf("expected evolution candidate length 1, got %d", len(snapshot.EvolutionCandidates))
	}
}

func TestSQLiteStore_LoadMemoryLifecycleStatus(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()
	now := time.Date(2026, time.May, 21, 10, 0, 0, 0, time.UTC)
	if err := store.RecordWarmLesson(WarmLessonRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Kind: "workflow", Summary: "Old low-confidence lesson | with separator.", Source: "task_summary", Confidence: "low", UpdatedAt: now.AddDate(0, 0, -45)}); err != nil {
		t.Fatalf("record warm lesson failed: %v", err)
	}
	if err := store.RecordEvolutionCandidate(EvolutionCandidateRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Kind: "workflow_pattern", Summary: "Closed candidate.", Source: "warm_lesson", Priority: "low", Status: "closed", UpdatedAt: now.AddDate(0, 0, -10)}); err != nil {
		t.Fatalf("record evolution candidate failed: %v", err)
	}
	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-a", Kind: "workflow", Summary: "Low support project lesson.", Source: "warm_lesson", Confidence: "low", SupportCount: 1, SupportTasks: []string{"task-a"}, UpdatedAt: now}); err != nil {
		t.Fatalf("record project lesson failed: %v", err)
	}
	if err := store.RecordEvaluation(EvaluationRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Kind: "runtime_lifecycle", Verdict: "INFO", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Source: "runtime", Details: []string{"status: completed"}, UpdatedAt: now}); err != nil {
		t.Fatalf("record protected evaluation failed: %v", err)
	}
	if err := store.RecordVerification(VerificationRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Tool: "go test", Verdict: "PASS", Summary: "Verification passed.", UpdatedAt: now}); err != nil {
		t.Fatalf("record verification failed: %v", err)
	}
	status, err := store.LoadMemoryLifecycleStatus(now)
	if err != nil {
		t.Fatalf("load memory lifecycle status failed: %v", err)
	}
	if status.Path == "" || status.SizeBytes <= 0 {
		t.Fatalf("expected status path and size, got %+v", status)
	}
	if len(status.Tables) != 10 {
		t.Fatalf("expected 10 table statuses, got %+v", status.Tables)
	}
	if status.StaleWarmLessons != 1 || status.RetirableEvolutionCandidates != 1 || status.LowSupportProjectLessons != 1 {
		t.Fatalf("expected advisory retirement candidates, got %+v", status)
	}
	if status.RetirementCandidateTotal != 3 {
		t.Fatalf("expected total candidates 3, got %+v", status)
	}
	if status.ProtectedEvaluationRecords != 1 || status.ProtectedVerificationHistory != 1 {
		t.Fatalf("expected protected truth counts, got %+v", status)
	}
	if !status.MaintenanceSupported || status.MaintenanceMode != "dry_run_with_guarded_archive_apply" {
		t.Fatalf("expected guarded archive maintenance support, got %+v", status)
	}
	report, err := store.PlanMemoryMaintenance(now)
	if err != nil {
		t.Fatalf("plan memory maintenance failed: %v", err)
	}
	if report.Mode != "dry_run" || !report.ApplySupported || report.PlannedAction != "preview_archive_candidates" {
		t.Fatalf("expected dry-run archive candidate preview, got %+v", report)
	}
	if report.RetirementCandidateTotal != 3 || report.ProtectedTruthSkipped != 2 {
		t.Fatalf("expected candidates and protected skip counts, got %+v", report)
	}
	if report.Guardrail == "" {
		t.Fatalf("expected dry-run guardrail, got %+v", report)
	}
	archiveStatus, err := store.LoadMemoryArchiveStatus()
	if err != nil {
		t.Fatalf("load memory archive status failed: %v", err)
	}
	if !archiveStatus.SchemaReady {
		t.Fatalf("expected archive schema ready, got %+v", archiveStatus)
	}
	if archiveStatus.TombstoneCount != 0 || archiveStatus.ArchivedCount != 0 || archiveStatus.RetiredCount != 0 {
		t.Fatalf("expected empty archive tombstone counts, got %+v", archiveStatus)
	}
	if !archiveStatus.ApplySupported || archiveStatus.Mode != "reversible_archive" {
		t.Fatalf("expected reversible archive mode, got %+v", archiveStatus)
	}
	if len(archiveStatus.SupportedTables) != 3 || len(archiveStatus.ProtectedClasses) == 0 {
		t.Fatalf("expected archive support metadata, got %+v", archiveStatus)
	}
	candidates, err := store.PlanMemoryRetirementCandidates(now)
	if err != nil {
		t.Fatalf("plan memory retirement candidates failed: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected 3 retirement candidates, got %+v", candidates)
	}
	seen := map[string]MemoryRetirementCandidate{}
	for _, candidate := range candidates {
		if candidate.TombstoneID == "" || candidate.OriginalTable == "" || candidate.OriginalKey == "" || candidate.Reason == "" {
			t.Fatalf("expected complete deterministic candidate, got %+v", candidate)
		}
		if strings.Contains(candidate.OriginalTable, "evaluation") || strings.Contains(candidate.OriginalTable, "verification") {
			t.Fatalf("expected protected truth to stay out of retirement candidates, got %+v", candidate)
		}
		seen[candidate.OriginalTable] = candidate
	}
	for _, table := range []string{"warm_lessons", "evolution_candidates", "project_lessons"} {
		if _, ok := seen[table]; !ok {
			t.Fatalf("expected candidate for %s in %+v", table, candidates)
		}
	}
	secondCandidates, err := store.PlanMemoryRetirementCandidates(now)
	if err != nil {
		t.Fatalf("plan second memory retirement candidates failed: %v", err)
	}
	if len(secondCandidates) != len(candidates) {
		t.Fatalf("expected stable candidate count, got %+v", secondCandidates)
	}
	for index := range candidates {
		if candidates[index].TombstoneID != secondCandidates[index].TombstoneID {
			t.Fatalf("expected deterministic tombstone id at %d, got %q and %q", index, candidates[index].TombstoneID, secondCandidates[index].TombstoneID)
		}
	}
	applyReport, err := store.ApplyMemoryArchive(now)
	if err != nil {
		t.Fatalf("apply memory archive failed: %v", err)
	}
	if applyReport.Mode != "archive_apply" || applyReport.PlannedCandidates != 3 || applyReport.Archived != 3 || applyReport.RetiredFromHot != 3 {
		t.Fatalf("expected tombstone-first archive apply report, got %+v", applyReport)
	}
	for _, table := range []string{"warm_lessons", "evolution_candidates", "project_lessons"} {
		count, err := store.countMemoryRows(fmt.Sprintf("SELECT COUNT(*) FROM %s", table), "")
		if err != nil {
			t.Fatalf("count %s failed: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("expected %s retired from hot recall, got %d", table, count)
		}
	}
	tombstones, err := store.countMemoryRows(`SELECT COUNT(*) FROM memory_archive_tombstones WHERE state = 'retired'`, "")
	if err != nil {
		t.Fatalf("count tombstones failed: %v", err)
	}
	if tombstones != 3 {
		t.Fatalf("expected 3 retired tombstones, got %d", tombstones)
	}
	listedTombstones, err := store.ListMemoryArchiveTombstones(10)
	if err != nil {
		t.Fatalf("list memory archive tombstones failed: %v", err)
	}
	if len(listedTombstones) != 3 {
		t.Fatalf("expected 3 listed tombstones, got %+v", listedTombstones)
	}
	for _, tombstone := range listedTombstones {
		if tombstone.TombstoneID == "" || tombstone.OriginalTable == "" || tombstone.OriginalKey == "" || tombstone.Reason == "" || tombstone.State != "retired" || tombstone.ArchivedAt.IsZero() {
			t.Fatalf("expected complete retired tombstone, got %+v", tombstone)
		}
		if strings.Contains(tombstone.OriginalTable, "evaluation") || strings.Contains(tombstone.OriginalTable, "verification") {
			t.Fatalf("expected protected truth outside tombstones, got %+v", tombstone)
		}
	}
	var warmTombstone MemoryArchiveTombstone
	for _, tombstone := range listedTombstones {
		if tombstone.OriginalTable == "warm_lessons" {
			warmTombstone = tombstone
			break
		}
	}
	if warmTombstone.TombstoneID == "" {
		t.Fatalf("expected warm lesson tombstone in %+v", listedTombstones)
	}
	restoreReport, err := store.PlanMemoryArchiveRestore(warmTombstone.TombstoneID)
	if err != nil {
		t.Fatalf("plan memory archive restore failed: %v", err)
	}
	if !restoreReport.RestoreSupported || restoreReport.Conflict || restoreReport.HotRecordExists {
		t.Fatalf("expected restorable dry-run target, got %+v", restoreReport)
	}
	if restoreReport.TargetTable != "warm_lessons" || restoreReport.TargetKey == "" || restoreReport.TargetSummary == "" || restoreReport.Guardrail == "" {
		t.Fatalf("expected restore target details, got %+v", restoreReport)
	}
	if err := store.RecordWarmLesson(WarmLessonRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-a", Kind: "workflow", Summary: warmTombstone.OriginalSummary, Source: "task_summary", Confidence: "high", UpdatedAt: now}); err != nil {
		t.Fatalf("record conflicting warm lesson failed: %v", err)
	}
	conflictReport, err := store.PlanMemoryArchiveRestore(warmTombstone.TombstoneID)
	if err != nil {
		t.Fatalf("plan conflicting memory archive restore failed: %v", err)
	}
	if conflictReport.RestoreSupported || !conflictReport.Conflict || !conflictReport.HotRecordExists || !strings.Contains(conflictReport.ConflictReason, "already exists") {
		t.Fatalf("expected restore conflict dry-run target, got %+v", conflictReport)
	}
	protectedEvaluation, err := store.countMemoryRows(`SELECT COUNT(*) FROM evaluation_records`, "")
	if err != nil {
		t.Fatalf("count protected evaluations failed: %v", err)
	}
	protectedHistory, err := store.countMemoryRows(`SELECT COUNT(*) FROM verification_history`, "")
	if err != nil {
		t.Fatalf("count protected verification history failed: %v", err)
	}
	if protectedEvaluation != 1 || protectedHistory != 1 {
		t.Fatalf("expected protected truth unchanged, evaluations=%d history=%d", protectedEvaluation, protectedHistory)
	}
	secondApplyReport, err := store.ApplyMemoryArchive(now)
	if err != nil {
		t.Fatalf("second apply memory archive failed: %v", err)
	}
	if secondApplyReport.PlannedCandidates != 0 || secondApplyReport.Archived != 0 || secondApplyReport.RetiredFromHot != 0 {
		t.Fatalf("expected idempotent no-op after hot retirement, got %+v", secondApplyReport)
	}
}

func TestSQLiteStore_LoadLatestTaskSnapshotAcrossSessions(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()
	baseTime := time.Date(2026, 4, 29, 9, 0, 0, 0, time.UTC)

	for _, record := range []SummaryRecord{
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-stable", Scope: ScopeStable, Summary: "Stable summary A"},
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-stable", Scope: ScopeTask, Summary: "Task summary A"},
		{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Scope: ScopeStable, Summary: "Stable summary B"},
		{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Scope: ScopeTask, Summary: "Task summary B"},
		{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", AvatarID: "avatar-planner", Scope: ScopeAvatar, Summary: "Planner summary B"},
	} {
		if err := store.UpsertSummary(record); err != nil {
			t.Fatalf("upsert summary failed: %v", err)
		}
	}
	for _, record := range []EventRecord{
		{SessionID: "session-a", RunID: "run-a", TaskID: "task-stable", EventID: "evt-a", EventType: "task.decomposed", Summary: "Planner created an initial workflow."},
		{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", EventID: "evt-b", EventType: "tool.completed", Summary: "Tool completed: inspected hot-memory.db."},
	} {
		if err := store.RecordEvent(record); err != nil {
			t.Fatalf("record event failed: %v", err)
		}
	}
	if err := store.RecordVerification(VerificationRecord{SessionID: "session-a", RunID: "run-a-verify", TaskID: "task-stable", Tool: "write", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: filepath.Join(".avatars", "tasks", "task-stable", "memory", "verifier", "run-a-write.json"), Checks: []string{"go test ./... => PASS"}}); err != nil {
		t.Fatalf("record verification failed: %v", err)
	}
	if err := store.RecordVerification(VerificationRecord{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: filepath.Join(".avatars", "tasks", "task-stable", "memory", "verifier", "run-b-patch.json"), Warnings: []string{"CGO is not enabled"}, Checks: []string{"go test -race ./... => PARTIAL"}}); err != nil {
		t.Fatalf("record verification failed: %v", err)
	}
	if err := store.RecordEvaluation(EvaluationRecord{SessionID: "session-a", RunID: "run-a-verify", TaskID: "task-stable", Tool: "write", Kind: "verifier_verdict", Verdict: "PASS", Cause: "verification_completed", Summary: "Verification finished with PASS.", Source: "verification", ReportPath: filepath.Join(".avatars", "tasks", "task-stable", "memory", "verifier", "run-a-write.json"), Details: []string{"go test ./... => PASS"}}); err != nil {
		t.Fatalf("record evaluation failed: %v", err)
	}
	if err := store.RecordEvaluation(EvaluationRecord{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Tool: "patch", Kind: "passive_feedback", Verdict: "PARTIAL", Cause: "verification_warning", Summary: "CGO is not enabled", Source: "verification_warning", ReportPath: filepath.Join(".avatars", "tasks", "task-stable", "memory", "verifier", "run-b-patch.json"), Details: []string{"go test -race ./... => PARTIAL"}}); err != nil {
		t.Fatalf("record passive evaluation failed: %v", err)
	}
	if err := store.RecordWarmLesson(WarmLessonRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-stable", Kind: "workflow", Summary: "Keep the planner decomposition stable before execution.", Source: "task_summary", Confidence: "medium"}); err != nil {
		t.Fatalf("record warm lesson failed: %v", err)
	}
	if err := store.RecordTelemetry(TelemetryRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-stable", Snapshot: runtelemetry.Snapshot{Mode: "run", Status: "completed", Provider: "google", Model: "googleai/gemini-2.5-flash", LLMRequests: 1, LLMCompletions: 1, ToolRequests: 1, ToolCompletions: 1, DurationMillis: 24, UpdatedAt: baseTime.Add(30 * time.Second)}}); err != nil {
		t.Fatalf("record first telemetry failed: %v", err)
	}
	if err := store.RecordLLMStatus(LLMStatusRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-stable", Status: llm.ProviderStatus{Name: "google", Family: "genkit", Model: "googleai/gemini-2.5-flash", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}, UpdatedAt: baseTime.Add(20 * time.Second)}); err != nil {
		t.Fatalf("record first llm status failed: %v", err)
	}
	if err := store.RecordWarmLesson(WarmLessonRecord{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Kind: "verification", Summary: "Before marking this task complete, repeat the verifier path for patch and expect PARTIAL.", Source: "verification", Confidence: "high"}); err != nil {
		t.Fatalf("record second warm lesson failed: %v", err)
	}
	if err := store.RecordTelemetry(TelemetryRecord{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Snapshot: runtelemetry.Snapshot{Mode: "run", Status: "completed", Provider: "google", Model: "googleai/gemini-2.5-flash", LLMRequests: 1, LLMCompletions: 1, ToolRequests: 2, ToolCompletions: 2, DurationMillis: 64, UpdatedAt: baseTime.Add(2 * time.Minute)}}); err != nil {
		t.Fatalf("record second telemetry failed: %v", err)
	}
	if err := store.RecordLLMStatus(LLMStatusRecord{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Status: llm.ProviderStatus{Name: "openrouter", Family: "openai-compatible", Model: "openai/gpt-4.1-mini", BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", SupportsWebSearch: true, WebSearchEnabled: true, ThinkMode: llm.ThinkModeAuto}, Warning: "current config fell back to defaults", UpdatedAt: baseTime.Add(90 * time.Second)}); err != nil {
		t.Fatalf("record second llm status failed: %v", err)
	}
	if err := store.RecordEvolutionCandidate(EvolutionCandidateRecord{SessionID: "session-a", RunID: "run-a", TaskID: "task-stable", Kind: "workflow_pattern", Summary: "Promote this lesson into a reusable workflow pattern: Keep the planner decomposition stable before execution.", Source: "warm_lesson", Priority: "medium", Status: "open"}); err != nil {
		t.Fatalf("record first evolution candidate failed: %v", err)
	}
	if err := store.RecordEvolutionCandidate(EvolutionCandidateRecord{SessionID: "session-b", RunID: "run-b", TaskID: "task-stable", Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Source: "verification", Priority: "high", Status: "open"}); err != nil {
		t.Fatalf("record second evolution candidate failed: %v", err)
	}

	snapshot, err := store.LoadLatestTaskSnapshot("task-stable")
	if err != nil {
		t.Fatalf("load latest task snapshot failed: %v", err)
	}
	if snapshot.StableSummary != "Stable summary B" {
		t.Fatalf("expected latest stable summary, got %q", snapshot.StableSummary)
	}
	if snapshot.TaskSummary != "Task summary B" {
		t.Fatalf("expected latest task summary, got %q", snapshot.TaskSummary)
	}
	if snapshot.AvatarSummaries["avatar-planner"] != "Planner summary B" {
		t.Fatalf("expected latest planner summary, got %q", snapshot.AvatarSummaries["avatar-planner"])
	}
	if len(snapshot.RecentEvents) != 2 {
		t.Fatalf("expected 2 recent events, got %d", len(snapshot.RecentEvents))
	}
	if snapshot.RecentEvents[0] != "Tool completed: inspected hot-memory.db." {
		t.Fatalf("expected latest task event first, got %q", snapshot.RecentEvents[0])
	}
	if snapshot.Verification == nil {
		t.Fatal("expected latest verification snapshot")
	}
	if snapshot.Telemetry == nil {
		t.Fatal("expected latest telemetry snapshot")
	}
	if snapshot.Telemetry.DurationMillis != 64 {
		t.Fatalf("expected latest telemetry duration 64, got %d", snapshot.Telemetry.DurationMillis)
	}
	if snapshot.LLMStatus == nil {
		t.Fatal("expected latest llm status snapshot")
	}
	if snapshot.LLMStatus.Name != "openrouter" {
		t.Fatalf("expected latest llm status provider openrouter, got %q", snapshot.LLMStatus.Name)
	}
	if snapshot.LLMStatusWarning != "current config fell back to defaults" {
		t.Fatalf("expected latest llm status warning, got %q", snapshot.LLMStatusWarning)
	}
	if snapshot.Verification.Verdict != "PARTIAL" {
		t.Fatalf("expected latest verification verdict PARTIAL, got %q", snapshot.Verification.Verdict)
	}
	if snapshot.Verification.ReportPath == "" {
		t.Fatal("expected latest verification report path")
	}
	if len(snapshot.VerificationHistory) != 2 {
		t.Fatalf("expected latest verification history length 2, got %d", len(snapshot.VerificationHistory))
	}
	if len(snapshot.EvaluationRecords) != 2 {
		t.Fatalf("expected evaluation record length 2, got %d", len(snapshot.EvaluationRecords))
	}
	if snapshot.EvaluationRecords[0].Tool != "patch" {
		t.Fatalf("expected latest evaluation record tool patch, got %q", snapshot.EvaluationRecords[0].Tool)
	}
	if snapshot.VerificationHistory[0].Verdict != "PARTIAL" {
		t.Fatalf("expected most recent verification verdict PARTIAL, got %q", snapshot.VerificationHistory[0].Verdict)
	}
	if snapshot.VerificationHistory[1].Verdict != "PASS" {
		t.Fatalf("expected previous verification verdict PASS, got %q", snapshot.VerificationHistory[1].Verdict)
	}
	if len(snapshot.WarmLessons) != 2 {
		t.Fatalf("expected warm lesson length 2, got %d", len(snapshot.WarmLessons))
	}
	if snapshot.WarmLessons[0].Kind != "verification" {
		t.Fatalf("expected latest warm lesson kind verification, got %q", snapshot.WarmLessons[0].Kind)
	}
	if len(snapshot.EvolutionCandidates) != 2 {
		t.Fatalf("expected evolution candidate length 2, got %d", len(snapshot.EvolutionCandidates))
	}
	if snapshot.EvolutionCandidates[0].Kind != "runtime_followup" {
		t.Fatalf("expected latest evolution candidate kind runtime_followup, got %q", snapshot.EvolutionCandidates[0].Kind)
	}
}

func TestSQLiteStore_MigratesVerificationReportPathColumn(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "hot-memory.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy sqlite store failed: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE hot_verifications (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
	tool TEXT NOT NULL,
	verdict TEXT NOT NULL,
	summary TEXT NOT NULL,
	warnings_json TEXT NOT NULL,
	checks_json TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (session_id, tool)
);
`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy schema failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite store failed: %v", err)
	}

	store, err := NewSQLiteStore(baseDir)
	if err != nil {
		t.Fatalf("open migrated sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	reportPath := filepath.Join(".avatars", "memory", "verifier", "run-legacy-write.json")
	if err := store.RecordVerification(VerificationRecord{SessionID: "session-legacy", RunID: "run-legacy", TaskID: "task-legacy", Tool: "write", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: reportPath, Checks: []string{"go test ./... => PASS"}}); err != nil {
		t.Fatalf("record migrated verification failed: %v", err)
	}

	snapshot, err := store.LoadSnapshot("session-legacy")
	if err != nil {
		t.Fatalf("load migrated snapshot failed: %v", err)
	}
	if snapshot.Verification == nil {
		t.Fatal("expected migrated verification snapshot")
	}
	if snapshot.Verification.ReportPath != reportPath {
		t.Fatalf("expected migrated verification report path %q, got %q", reportPath, snapshot.Verification.ReportPath)
	}
}

func TestSQLiteStore_MigratesEvaluationRecordToolColumn(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "hot-memory.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy sqlite store failed: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE evaluation_records (
	session_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	avatar_id TEXT NOT NULL,
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
`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy evaluation schema failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite store failed: %v", err)
	}

	store, err := NewSQLiteStore(baseDir)
	if err != nil {
		t.Fatalf("open migrated sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	if err := store.RecordEvaluation(EvaluationRecord{SessionID: "session-legacy", RunID: "run-legacy", TaskID: "task-legacy", Tool: "patch", Kind: "verifier_verdict", Verdict: "PARTIAL", Cause: "verification_partial", Summary: "Verification finished with PARTIAL.", Source: "verification", Details: []string{"go test -race ./... => PARTIAL"}}); err != nil {
		t.Fatalf("record migrated evaluation failed: %v", err)
	}

	snapshot, err := store.LoadSnapshot("session-legacy")
	if err != nil {
		t.Fatalf("load migrated snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != 1 {
		t.Fatalf("expected migrated evaluation record length 1, got %d", len(snapshot.EvaluationRecords))
	}
	if snapshot.EvaluationRecords[0].Tool != "patch" {
		t.Fatalf("expected migrated evaluation record tool patch, got %q", snapshot.EvaluationRecords[0].Tool)
	}
}

func TestSQLiteStore_PrunesVerificationHistoryPerTask(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	for index := 0; index < verificationHistoryLimit+2; index++ {
		record := VerificationRecord{
			SessionID:  fmt.Sprintf("session-%d", index),
			RunID:      fmt.Sprintf("run-%02d", index),
			TaskID:     "task-history",
			Tool:       "write",
			Verdict:    "PASS",
			Summary:    fmt.Sprintf("Verification run %d", index),
			ReportPath: filepath.Join(".avatars", "tasks", "task-history", "memory", "verifier", fmt.Sprintf("run-%02d.json", index)),
			UpdatedAt:  time.Date(2026, 4, 29, 10, 0, index, 0, time.UTC),
		}
		if err := store.RecordVerification(record); err != nil {
			t.Fatalf("record verification history failed: %v", err)
		}
	}

	snapshot, err := store.LoadLatestTaskSnapshot("task-history")
	if err != nil {
		t.Fatalf("load task history snapshot failed: %v", err)
	}
	if len(snapshot.VerificationHistory) != verificationHistoryLimit {
		t.Fatalf("expected verification history length %d, got %d", verificationHistoryLimit, len(snapshot.VerificationHistory))
	}
	if snapshot.VerificationHistory[0].Summary != "Verification run 7" {
		t.Fatalf("expected latest history summary to be kept, got %q", snapshot.VerificationHistory[0].Summary)
	}
	if snapshot.VerificationHistory[len(snapshot.VerificationHistory)-1].Summary != "Verification run 2" {
		t.Fatalf("expected oldest retained history summary to be run 2, got %q", snapshot.VerificationHistory[len(snapshot.VerificationHistory)-1].Summary)
	}
}

func TestSQLiteStore_DeduplicatesAndPrunesWarmLessonsPerTask(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	baseTime := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)
	for index := 0; index < warmLessonLimit+2; index++ {
		record := WarmLessonRecord{
			SessionID:  fmt.Sprintf("session-%d", index),
			RunID:      fmt.Sprintf("run-%02d", index),
			TaskID:     "task-lessons",
			Kind:       fmt.Sprintf("kind-%d", index),
			Summary:    fmt.Sprintf("Lesson %d", index),
			Source:     "task_summary",
			Confidence: "medium",
			UpdatedAt:  baseTime.Add(time.Duration(index) * time.Minute),
		}
		if err := store.RecordWarmLesson(record); err != nil {
			t.Fatalf("record warm lesson failed: %v", err)
		}
	}
	if err := store.RecordWarmLesson(WarmLessonRecord{SessionID: "session-update", RunID: "run-update", TaskID: "task-lessons", Kind: "kind-6", Summary: "Lesson 6", Source: "verification", Confidence: "high", UpdatedAt: baseTime.Add(10 * time.Minute)}); err != nil {
		t.Fatalf("update warm lesson failed: %v", err)
	}

	snapshot, err := store.LoadLatestTaskSnapshot("task-lessons")
	if err != nil {
		t.Fatalf("load task lessons snapshot failed: %v", err)
	}
	if len(snapshot.WarmLessons) != warmLessonLimit {
		t.Fatalf("expected warm lesson length %d, got %d", warmLessonLimit, len(snapshot.WarmLessons))
	}
	if snapshot.WarmLessons[0].Summary != "Lesson 6" {
		t.Fatalf("expected latest warm lesson summary Lesson 6, got %q", snapshot.WarmLessons[0].Summary)
	}
	if snapshot.WarmLessons[0].Confidence != "high" {
		t.Fatalf("expected updated lesson confidence high, got %q", snapshot.WarmLessons[0].Confidence)
	}
	if snapshot.WarmLessons[len(snapshot.WarmLessons)-1].Summary != "Lesson 3" {
		t.Fatalf("expected oldest retained warm lesson Lesson 3, got %q", snapshot.WarmLessons[len(snapshot.WarmLessons)-1].Summary)
	}
}

func TestSQLiteStore_DeduplicatesAndPrunesEvolutionCandidatesPerTask(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	baseTime := time.Date(2026, 4, 29, 11, 0, 0, 0, time.UTC)
	for index := 0; index < evolutionCandidateLimit+2; index++ {
		record := EvolutionCandidateRecord{
			SessionID: fmt.Sprintf("session-%d", index),
			RunID:     fmt.Sprintf("run-%02d", index),
			TaskID:    "task-evolution",
			Kind:      fmt.Sprintf("kind-%d", index),
			Summary:   fmt.Sprintf("Candidate %d", index),
			Source:    "warm_lesson",
			Priority:  "medium",
			Status:    "open",
			UpdatedAt: baseTime.Add(time.Duration(index) * time.Minute),
		}
		if err := store.RecordEvolutionCandidate(record); err != nil {
			t.Fatalf("record evolution candidate failed: %v", err)
		}
	}
	if err := store.RecordEvolutionCandidate(EvolutionCandidateRecord{SessionID: "session-update", RunID: "run-update", TaskID: "task-evolution", Kind: "kind-7", Summary: "Candidate 7", Source: "verification", Priority: "high", Status: "open", UpdatedAt: baseTime.Add(10 * time.Minute)}); err != nil {
		t.Fatalf("update evolution candidate failed: %v", err)
	}

	snapshot, err := store.LoadLatestTaskSnapshot("task-evolution")
	if err != nil {
		t.Fatalf("load task evolution snapshot failed: %v", err)
	}
	if len(snapshot.EvolutionCandidates) != evolutionCandidateLimit {
		t.Fatalf("expected evolution candidate length %d, got %d", evolutionCandidateLimit, len(snapshot.EvolutionCandidates))
	}
	if snapshot.EvolutionCandidates[0].Summary != "Candidate 7" {
		t.Fatalf("expected latest evolution candidate summary Candidate 7, got %q", snapshot.EvolutionCandidates[0].Summary)
	}
	if snapshot.EvolutionCandidates[0].Priority != "high" {
		t.Fatalf("expected updated evolution candidate priority high, got %q", snapshot.EvolutionCandidates[0].Priority)
	}
	if snapshot.EvolutionCandidates[len(snapshot.EvolutionCandidates)-1].Summary != "Candidate 2" {
		t.Fatalf("expected oldest retained evolution candidate Candidate 2, got %q", snapshot.EvolutionCandidates[len(snapshot.EvolutionCandidates)-1].Summary)
	}
}

func TestSQLiteStore_DeduplicatesAndPrunesEvaluationRecordsPerTask(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	baseTime := time.Date(2026, 4, 29, 11, 30, 0, 0, time.UTC)
	for index := 0; index < evaluationRecordLimit+2; index++ {
		record := EvaluationRecord{
			SessionID: fmt.Sprintf("session-%d", index),
			RunID:     fmt.Sprintf("run-%02d", index),
			TaskID:    "task-evaluation",
			Kind:      fmt.Sprintf("kind-%d", index),
			Verdict:   "PARTIAL",
			Cause:     "verification_warning",
			Summary:   fmt.Sprintf("Evaluation %d", index),
			Source:    "verification",
			Details:   []string{"detail"},
			UpdatedAt: baseTime.Add(time.Duration(index) * time.Minute),
		}
		if err := store.RecordEvaluation(record); err != nil {
			t.Fatalf("record evaluation failed: %v", err)
		}
	}
	if err := store.RecordEvaluation(EvaluationRecord{SessionID: "session-update", RunID: "run-update", TaskID: "task-evaluation", Kind: "kind-7", Verdict: "FAIL", Cause: "verification_failed", Summary: "Evaluation 7", Source: "verification", Details: []string{"updated"}, UpdatedAt: baseTime.Add(10 * time.Minute)}); err != nil {
		t.Fatalf("update evaluation failed: %v", err)
	}

	snapshot, err := store.LoadLatestTaskSnapshot("task-evaluation")
	if err != nil {
		t.Fatalf("load evaluation snapshot failed: %v", err)
	}
	if len(snapshot.EvaluationRecords) != evaluationRecordLimit {
		t.Fatalf("expected evaluation record length %d, got %d", evaluationRecordLimit, len(snapshot.EvaluationRecords))
	}
	if snapshot.EvaluationRecords[0].Summary != "Evaluation 7" {
		t.Fatalf("expected latest evaluation summary Evaluation 7, got %q", snapshot.EvaluationRecords[0].Summary)
	}
	if snapshot.EvaluationRecords[0].Verdict != "FAIL" {
		t.Fatalf("expected updated evaluation verdict FAIL, got %q", snapshot.EvaluationRecords[0].Verdict)
	}
	if snapshot.EvaluationRecords[len(snapshot.EvaluationRecords)-1].Summary != "Evaluation 2" {
		t.Fatalf("expected oldest retained evaluation summary Evaluation 2, got %q", snapshot.EvaluationRecords[len(snapshot.EvaluationRecords)-1].Summary)
	}
}

func TestSQLiteStore_DeduplicatesAndPrunesProjectLessons(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	baseTime := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	for index := 0; index < projectLessonLimit+2; index++ {
		record := ProjectLessonRecord{
			SourceTaskID: fmt.Sprintf("task-%d", index),
			Kind:         fmt.Sprintf("kind-%d", index),
			Summary:      fmt.Sprintf("Project lesson %d", index),
			Source:       "evolution_candidate",
			Confidence:   "high",
			UpdatedAt:    baseTime.Add(time.Duration(index) * time.Minute),
		}
		if err := store.RecordProjectLesson(record); err != nil {
			t.Fatalf("record project lesson failed: %v", err)
		}
	}
	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-update", Kind: "kind-7", Summary: "Project lesson 7", Source: "warm_lesson", Confidence: "high", UpdatedAt: baseTime.Add(10 * time.Minute)}); err != nil {
		t.Fatalf("update project lesson failed: %v", err)
	}
	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-update", Kind: "kind-7", Summary: "Project lesson 7", Source: "warm_lesson", Confidence: "high", UpdatedAt: baseTime.Add(11 * time.Minute)}); err != nil {
		t.Fatalf("repeat project lesson support failed: %v", err)
	}

	lessons, err := store.LoadProjectLessons()
	if err != nil {
		t.Fatalf("load project lessons failed: %v", err)
	}
	if len(lessons) != projectLessonLimit {
		t.Fatalf("expected project lesson length %d, got %d", projectLessonLimit, len(lessons))
	}
	if lessons[0].Summary != "Project lesson 7" {
		t.Fatalf("expected latest project lesson summary Project lesson 7, got %q", lessons[0].Summary)
	}
	if lessons[0].Source != "warm_lesson" {
		t.Fatalf("expected updated project lesson source warm_lesson, got %q", lessons[0].Source)
	}
	if lessons[0].SupportCount != 2 {
		t.Fatalf("expected updated project lesson support count 2, got %d", lessons[0].SupportCount)
	}
	if lessons[len(lessons)-1].Summary != "Project lesson 2" {
		t.Fatalf("expected oldest retained project lesson Project lesson 2, got %q", lessons[len(lessons)-1].Summary)
	}
}

func TestSQLiteStore_LoadProjectLessonsRanksCrossTaskSupport(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	baseTime := time.Date(2026, 4, 29, 13, 0, 0, 0, time.UTC)
	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-a", Kind: "workflow_pattern", Summary: "Keep planner decomposition stable before execution.", Source: "warm_lesson", Confidence: "high", UpdatedAt: baseTime}); err != nil {
		t.Fatalf("record first project lesson failed: %v", err)
	}
	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-b", Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch before marking the task complete.", Source: "evolution_candidate", Confidence: "medium", UpdatedAt: baseTime.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("record second project lesson failed: %v", err)
	}
	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-c", Kind: "workflow_pattern", Summary: "Keep planner decomposition stable before execution.", Source: "warm_lesson", Confidence: "high", UpdatedAt: baseTime.Add(1 * time.Minute)}); err != nil {
		t.Fatalf("record cross-task support lesson failed: %v", err)
	}

	lessons, err := store.LoadProjectLessons()
	if err != nil {
		t.Fatalf("load ranked project lessons failed: %v", err)
	}
	if len(lessons) != 2 {
		t.Fatalf("expected 2 project lessons, got %d", len(lessons))
	}
	if lessons[0].Summary != "Keep planner decomposition stable before execution." {
		t.Fatalf("expected cross-task supported lesson to rank first, got %q", lessons[0].Summary)
	}
	if lessons[0].SupportCount != 2 {
		t.Fatalf("expected support count 2 for the ranked lesson, got %d", lessons[0].SupportCount)
	}
	if len(lessons[0].SupportTasks) != 2 {
		t.Fatalf("expected 2 supporting task ids, got %d", len(lessons[0].SupportTasks))
	}
	if lessons[1].Summary != "Add a deterministic follow-up path for patch before marking the task complete." {
		t.Fatalf("expected single-task lesson to rank second, got %q", lessons[1].Summary)
	}
}

func TestSQLiteStore_MigratesProjectLessonSupportColumns(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "hot-memory.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy sqlite store failed: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE project_lessons (
	source_task_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	summary TEXT NOT NULL,
	source TEXT NOT NULL,
	confidence TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(kind, summary)
);
`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy project lesson schema failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite store failed: %v", err)
	}

	store, err := NewSQLiteStore(baseDir)
	if err != nil {
		t.Fatalf("open migrated sqlite store failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-a", Kind: "runtime_followup", Summary: "Keep project lesson support evidence.", Source: "evolution_candidate", Confidence: "high"}); err != nil {
		t.Fatalf("record migrated project lesson failed: %v", err)
	}
	if err := store.RecordProjectLesson(ProjectLessonRecord{SourceTaskID: "task-b", Kind: "runtime_followup", Summary: "Keep project lesson support evidence.", Source: "evolution_candidate", Confidence: "high"}); err != nil {
		t.Fatalf("record second migrated project lesson failed: %v", err)
	}

	lessons, err := store.LoadProjectLessons()
	if err != nil {
		t.Fatalf("load migrated project lessons failed: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("expected 1 migrated project lesson, got %d", len(lessons))
	}
	if lessons[0].SupportCount != 2 {
		t.Fatalf("expected migrated project lesson support count 2, got %d", lessons[0].SupportCount)
	}
}
