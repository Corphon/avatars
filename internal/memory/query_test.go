package memory

import (
	"strings"
	"testing"
	"time"

	runtelemetry "avatars/internal/telemetry"
)

func TestSnapshotView_PlannerAndVerifierPolicies(t *testing.T) {
	snapshot := Snapshot{
		StableSummary:       "Stable summary",
		TaskSummary:         "Task summary",
		AvatarSummaries:     map[string]string{"avatar-builder": "Builder summary"},
		RecentEvents:        []string{"Event A", "Event B"},
		Telemetry:           &runtelemetry.Snapshot{Mode: "run", Status: "completed", DurationMillis: 12, ToolRequests: 1, ToolCompletions: 1},
		Verification:        &VerificationSnapshot{Tool: "write", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: ".avatars/memory/verifier/run-a-write.json", Checks: []string{"go test ./... => PASS"}, Warnings: []string{"warning"}},
		VerificationHistory: []VerificationSnapshot{{Tool: "write", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: ".avatars/memory/verifier/run-a-write.json"}, {Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-b-patch.json"}},
		EvaluationRecords:   []EvaluationRecord{{Kind: "workflow_feedback", Verdict: "PASS", Cause: "verification_reverify_covered", Summary: "Verification PASS now covers the latest non-pass verification: PARTIAL via patch.", Source: "verification_followup", ReportPath: ".avatars/memory/verifier/run-a-write.json", Details: []string{"covered_tool: patch", "covered_verdict: PARTIAL"}}, {Kind: "verifier_verdict", Verdict: "PASS", Cause: "verification_completed", Summary: "Verification finished with PASS.", Source: "verification", ReportPath: ".avatars/memory/verifier/run-a-write.json", Details: []string{"go test ./... => PASS"}}},
		WarmLessons:         []WarmLesson{{Kind: "workflow", Summary: "Keep the planner decomposition stable before execution.", Source: "task_summary", Confidence: "medium"}},
		EvolutionCandidates: []EvolutionCandidate{{Kind: "workflow_pattern", Summary: "Promote this lesson into a reusable workflow pattern: Keep the planner decomposition stable before execution.", Source: "warm_lesson", Priority: "medium", Status: "open"}},
		ProjectLessons: []ProjectLesson{
			{SourceTaskID: "task-a", Kind: "verification", Summary: "Keep verifier follow-up visible across the repo.", Source: "warm_lesson", Confidence: "high", SupportCount: 1},
			{SourceTaskID: "task-b", Kind: "runtime_followup", Summary: "Add a deterministic follow-up path for patch when verifier verdict is PARTIAL before marking the task complete.", Source: "evolution_candidate", Confidence: "medium", SupportCount: 3},
		},
	}

	plannerView := snapshot.View(QueryPolicy{Reader: ReaderPlanner})
	if plannerView.StableSummary != "Stable summary" {
		t.Fatalf("expected planner view stable summary, got %q", plannerView.StableSummary)
	}
	if plannerView.TaskSummary != "Task summary" {
		t.Fatalf("expected planner view task summary, got %q", plannerView.TaskSummary)
	}
	if plannerView.Telemetry == nil || plannerView.Telemetry.DurationMillis != 12 {
		t.Fatalf("expected planner view telemetry duration 12, got %+v", plannerView.Telemetry)
	}
	if plannerView.AvatarSummary != "" {
		t.Fatalf("expected planner view to hide avatar summary, got %q", plannerView.AvatarSummary)
	}
	if len(plannerView.WarmLessons) != 1 {
		t.Fatalf("expected planner view warm lesson length 1, got %d", len(plannerView.WarmLessons))
	}
	if len(plannerView.EvolutionCandidates) != 1 {
		t.Fatalf("expected planner view evolution candidate length 1, got %d", len(plannerView.EvolutionCandidates))
	}
	if len(plannerView.EvaluationRecords) != 2 {
		t.Fatalf("expected planner view evaluation record length 2, got %d", len(plannerView.EvaluationRecords))
	}
	if len(plannerView.ProjectLessons) != 2 {
		t.Fatalf("expected planner view project lesson length 2, got %d", len(plannerView.ProjectLessons))
	}
	if plannerView.ProjectLessons[0].SupportCount != 3 {
		t.Fatalf("expected planner view to rank the strongest project lesson first, got support count %d", plannerView.ProjectLessons[0].SupportCount)
	}

	verifierView := snapshot.View(QueryPolicy{Reader: ReaderVerifier})
	if verifierView.StableSummary != "" {
		t.Fatalf("expected verifier view to omit stable summary, got %q", verifierView.StableSummary)
	}
	if verifierView.TaskSummary != "Task summary" {
		t.Fatalf("expected verifier view task summary, got %q", verifierView.TaskSummary)
	}
	if verifierView.Telemetry == nil || verifierView.Telemetry.Mode != "run" {
		t.Fatalf("expected verifier view telemetry, got %+v", verifierView.Telemetry)
	}
	if len(verifierView.RecentEvents) != 2 {
		t.Fatalf("expected verifier view recent events, got %d", len(verifierView.RecentEvents))
	}
	if verifierView.Verification == nil {
		t.Fatal("expected verifier view to include verification snapshot")
	}
	if verifierView.Verification.Verdict != "PASS" {
		t.Fatalf("expected verifier view verdict PASS, got %q", verifierView.Verification.Verdict)
	}
	if verifierView.Verification.ReportPath == "" {
		t.Fatal("expected verifier view to include report path")
	}
	if verifierView.Verification.FollowUpCommand != "" {
		t.Fatalf("expected PASS verification to omit follow-up command, got %q", verifierView.Verification.FollowUpCommand)
	}
	latestNonPass := LatestNonPassVerificationSnapshot(verifierView.Verification, verifierView.VerificationHistory)
	if latestNonPass == nil {
		t.Fatal("expected latest non-pass verification snapshot")
	}
	if latestNonPass.Verdict != "PARTIAL" || latestNonPass.Tool != "patch" {
		t.Fatalf("expected latest non-pass verification to use patch PARTIAL entry, got %+v", latestNonPass)
	}
	if latestNonPass.FollowUpCommand != "avatars verify" {
		t.Fatalf("expected latest non-pass verification follow-up command, got %+v", latestNonPass)
	}
	if reverifyStatus := VerificationReverifyStatus(verifierView.Verification, verifierView.VerificationHistory); reverifyStatus != "latest non-pass verification is covered by a later PASS" {
		t.Fatalf("expected covered reverify status, got %q", reverifyStatus)
	}
	closure := LatestReverifyClosureEvaluation(verifierView.Verification, verifierView.VerificationHistory, verifierView.EvaluationRecords)
	if closure == nil {
		t.Fatal("expected latest reverify closure evaluation")
	}
	if closure.Cause != "verification_reverify_covered" {
		t.Fatalf("expected reverify closure cause, got %+v", closure)
	}
	if len(verifierView.VerificationHistory) != 2 {
		t.Fatalf("expected verifier view history length 2, got %d", len(verifierView.VerificationHistory))
	}
	if verifierView.VerificationHistory[1].FollowUpCommand != "avatars verify" {
		t.Fatalf("expected PARTIAL verification history follow-up command, got %+v", verifierView.VerificationHistory[1])
	}
	if len(verifierView.EvaluationRecords) != 2 {
		t.Fatalf("expected verifier view evaluation record length 2, got %d", len(verifierView.EvaluationRecords))
	}
	if len(verifierView.WarmLessons) != 0 {
		t.Fatalf("expected verifier view to omit warm lessons, got %d", len(verifierView.WarmLessons))
	}
	if len(verifierView.EvolutionCandidates) != 0 {
		t.Fatalf("expected verifier view to omit evolution candidates, got %d", len(verifierView.EvolutionCandidates))
	}
	if len(verifierView.ProjectLessons) != 0 {
		t.Fatalf("expected verifier view to omit project lessons, got %d", len(verifierView.ProjectLessons))
	}
}

func TestLatestReverifyAttemptEvaluation(t *testing.T) {
	now := time.Now().UTC()
	current := &VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-patch.json", UpdatedAt: now}
	records := []EvaluationRecord{{Kind: "workflow_feedback", Verdict: "PARTIAL", Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Source: "verification_followup", Details: []string{"path: note.txt"}, UpdatedAt: now.Add(time.Nanosecond)}}
	attempt := LatestReverifyAttemptEvaluation(current, nil, records)
	if attempt == nil {
		t.Fatal("expected latest reverify attempt evaluation")
	}
	if attempt.Cause != "verification_reverify_fix_attempt" {
		t.Fatalf("expected reverify attempt cause, got %+v", attempt)
	}
}

func TestLatestCoveredReverifyAttemptEvaluation(t *testing.T) {
	now := time.Now().UTC()
	current := &VerificationSnapshot{Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: ".avatars/memory/verifier/run-pass.json", UpdatedAt: now}
	history := []VerificationSnapshot{{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-patch.json", UpdatedAt: now.Add(-time.Minute)}}
	records := []EvaluationRecord{
		{Kind: "workflow_feedback", Verdict: "PASS", Cause: "verification_reverify_covered", Summary: "Verification PASS now covers the latest non-pass verification: PARTIAL via patch.", Source: "verification_followup", UpdatedAt: now.Add(2 * time.Nanosecond)},
		{Kind: "workflow_feedback", Verdict: "PARTIAL", Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Source: "verification_followup", Details: []string{"path: note.txt"}, UpdatedAt: now.Add(time.Nanosecond)},
	}
	attempt := LatestCoveredReverifyAttemptEvaluation(current, history, records)
	if attempt == nil {
		t.Fatal("expected latest covered reverify attempt evaluation")
	}
	if attempt.Cause != "verification_reverify_fix_attempt" {
		t.Fatalf("expected covered reverify attempt cause, got %+v", attempt)
	}
}

func TestLatestVerifierRemediationPausePointEvaluation(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "resume_attempt_recorded", Summary: "Attempt only.", Details: []string{"pause_point_id: pause-a"}},
		{Cause: "verifier_remediation_pause_point", Summary: "Verifier opened remediation pause point.", Details: []string{"pause_point_id: pause-remediate", "pause_point_kind: verifier_remediation", "origin_run_id: run-before", "origin_task_id: demo-task", "expected_targets: note.txt, process_record.md"}},
	}
	record := LatestVerifierRemediationPausePointEvaluation(records)
	if record == nil {
		t.Fatal("expected latest verifier remediation pause point evaluation")
	}
	if EvaluationRecordPausePointID(*record) != "pause-remediate" {
		t.Fatalf("expected pause-remediate, got %+v", record)
	}
	if EvaluationRecordDetailValue(*record, "pause_point_kind") != "verifier_remediation" {
		t.Fatalf("expected verifier_remediation kind, got %+v", record)
	}
}

func TestLatestVerifierRemediationResumeAttemptEvaluation(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "verifier_remediation_pause_point", Summary: "Pause only.", Details: []string{"pause_point_id: pause-remediate"}},
		{Cause: "verifier_remediation_resume_attempt", Summary: "Remediation attempt started.", Details: []string{"pause_point_id: pause-remediate", "pause_point_kind: verifier_remediation", "resume_attempt_id: resume-remediate", "resume_attempt_status: started", "expected_targets: note.txt", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}},
	}
	record := LatestVerifierRemediationResumeAttemptEvaluation(records)
	if record == nil {
		t.Fatal("expected latest verifier remediation resume attempt evaluation")
	}
	if EvaluationRecordPausePointID(*record) != "pause-remediate" || EvaluationRecordResumeAttemptID(*record) != "resume-remediate" {
		t.Fatalf("expected remediation resume attempt coordinates, got %+v", record)
	}
	if EvaluationRecordRecoveryBoundaryGuardStatus(*record) != "guarded_ready" || EvaluationRecordRecoveryBoundaryRequiredAuthority(*record) != "verifier_reverify" {
		t.Fatalf("expected guarded remediation boundary metadata, got %+v", record.Details)
	}
}

func TestLatestVerifierRemediationClosureChain_PendingGuardedRemediation(t *testing.T) {
	now := time.Now().UTC()
	current := &VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-patch.json", UpdatedAt: now}
	records := []EvaluationRecord{
		{Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation resume attempt recorded for reverify repair work.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "expected_targets: note.txt, process_record.md", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}},
	}
	chain := LatestVerifierRemediationClosureChain(current, nil, records)
	if chain == nil {
		t.Fatal("expected verifier remediation closure chain")
	}
	if chain.Status != "pending_guarded_remediation" {
		t.Fatalf("expected pending guarded remediation status, got %+v", chain)
	}
	if chain.LatestNonPassReport != ".avatars/memory/verifier/run-patch.json" || chain.RemediationPausePointID != "pause-remediate" || chain.RemediationResumeAttemptID != "resume-remediate" {
		t.Fatalf("expected non-pass and guarded remediation coordinates, got %+v", chain)
	}
	if chain.GuardStatus != "guarded_ready" || chain.RequiredAuthority != "verifier_reverify" || chain.BoundarySource != "verifier_remediation_resume_attempt" {
		t.Fatalf("expected guarded boundary metadata, got %+v", chain)
	}
	if len(chain.ExpectedTargets) != 2 || chain.ExpectedTargets[0] != "note.txt" || chain.ExpectedTargets[1] != "process_record.md" {
		t.Fatalf("expected guarded remediation targets, got %+v", chain.ExpectedTargets)
	}
	if chain.PassClosureSummary != "" || chain.PassClosureReport != "" {
		t.Fatalf("expected pending chain without PASS closure, got %+v", chain)
	}
}

func TestLatestVerifierRemediationClosureChain_PassClosed(t *testing.T) {
	now := time.Now().UTC()
	current := &VerificationSnapshot{Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: ".avatars/memory/verifier/run-pass.json", UpdatedAt: now}
	history := []VerificationSnapshot{{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-patch.json", UpdatedAt: now.Add(-time.Minute)}}
	records := []EvaluationRecord{
		{Cause: "verification_reverify_covered", Summary: "Verification PASS now covers the latest non-pass verification: PARTIAL via patch.", ReportPath: ".avatars/memory/verifier/run-pass.json", Details: []string{"remediation_pause_point_id: pause-remediate", "remediation_resume_attempt_id: resume-remediate", "remediation_expected_targets: note.txt"}},
		{Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation resume attempt recorded for reverify repair work.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "expected_targets: note.txt", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}},
	}
	chain := LatestVerifierRemediationClosureChain(current, history, records)
	if chain == nil {
		t.Fatal("expected verifier remediation closure chain")
	}
	if chain.Status != "pass_closed" {
		t.Fatalf("expected PASS-closed remediation chain, got %+v", chain)
	}
	if chain.PassClosureReport != ".avatars/memory/verifier/run-pass.json" || chain.PassClosureSummary == "" {
		t.Fatalf("expected PASS closure provenance, got %+v", chain)
	}
	if chain.RemediationPausePointID != "pause-remediate" || chain.RemediationResumeAttemptID != "resume-remediate" {
		t.Fatalf("expected closure to carry remediation attempt coordinates, got %+v", chain)
	}
}

func TestLatestVerifierRemediationClosureChain_NormalFixOnly(t *testing.T) {
	now := time.Now().UTC()
	current := &VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-patch.json", UpdatedAt: now}
	records := []EvaluationRecord{{Cause: "verification_reverify_fix_attempt", Summary: "Normal fix attempt.", Details: []string{"expected_targets: note.txt"}}}
	if chain := LatestVerifierRemediationClosureChain(current, nil, records); chain != nil {
		t.Fatalf("expected no guarded closure chain for normal fix only, got %+v", chain)
	}
}

func TestEvaluationRecordExpectedTargets(t *testing.T) {
	record := EvaluationRecord{Details: []string{"path: note.txt", "expected_targets: note.txt, process_record.md", "expected_targets: note.txt"}}
	targets := EvaluationRecordExpectedTargets(record)
	if len(targets) != 2 {
		t.Fatalf("expected 2 expected targets, got %+v", targets)
	}
	if targets[0] != "note.txt" || targets[1] != "process_record.md" {
		t.Fatalf("unexpected expected targets: %+v", targets)
	}
}

func TestEvaluationRecordNodeEvidenceProvenance(t *testing.T) {
	record := EvaluationRecord{Details: []string{
		"artifact_ids: generated-skill, bootstrap-context",
		"expected_targets: skills/generated/example.md, process_record.md",
		"origin_run_id: run-origin",
		"origin_task_id: task-origin",
		"pause_point_id: pause-build",
		"continuation_run_id: run-continuation",
		"continuation_task_id: task-continuation",
		"verification_report_path: verifier/run.json",
	}}
	artifacts := EvaluationRecordArtifactIDs(record)
	if len(artifacts) != 2 || artifacts[0] != "generated-skill" || artifacts[1] != "bootstrap-context" {
		t.Fatalf("unexpected artifact ids: %+v", artifacts)
	}
	targets := EvaluationRecordExpectedTargets(record)
	if len(targets) != 2 || targets[0] != "skills/generated/example.md" || targets[1] != "process_record.md" {
		t.Fatalf("unexpected expected targets: %+v", targets)
	}
	if EvaluationRecordOriginRunID(record) != "run-origin" {
		t.Fatalf("expected origin run id, got %q", EvaluationRecordOriginRunID(record))
	}
	if EvaluationRecordOriginTaskID(record) != "task-origin" {
		t.Fatalf("expected origin task id, got %q", EvaluationRecordOriginTaskID(record))
	}
	if EvaluationRecordPausePointID(record) != "pause-build" {
		t.Fatalf("expected pause point id, got %q", EvaluationRecordPausePointID(record))
	}
	if EvaluationRecordContinuationRunID(record) != "run-continuation" {
		t.Fatalf("expected continuation run id, got %q", EvaluationRecordContinuationRunID(record))
	}
	if EvaluationRecordContinuationTaskID(record) != "task-continuation" {
		t.Fatalf("expected continuation task id, got %q", EvaluationRecordContinuationTaskID(record))
	}
	if EvaluationRecordVerificationReportPath(record) != "verifier/run.json" {
		t.Fatalf("expected verification report path, got %q", EvaluationRecordVerificationReportPath(record))
	}
}

func TestEvaluationRecordProposalID(t *testing.T) {
	record := EvaluationRecord{Details: []string{"path: note.txt", "proposal_id: remediate-latest-non-pass"}}
	proposalID := EvaluationRecordProposalID(record)
	if proposalID != "remediate-latest-non-pass" {
		t.Fatalf("expected proposal id, got %q", proposalID)
	}
}

func TestLatestToolPermissionDenialEvaluation(t *testing.T) {
	now := time.Now().UTC()
	records := []EvaluationRecord{
		{Cause: "tool_execution_failed", Summary: "Tool failed.", UpdatedAt: now},
		{Cause: "tool_permission_denied", Summary: "Tool write/file_write was denied: write tool target escapes sandbox: ../notes.txt", Details: []string{"decision_source: tool_sandbox"}, UpdatedAt: now.Add(time.Second)},
	}
	denial := LatestToolPermissionDenialEvaluation(records)
	if denial == nil {
		t.Fatal("expected latest tool permission denial evaluation")
	}
	if EvaluationRecordDecisionSource(*denial) != "tool_sandbox" {
		t.Fatalf("expected decision source tool_sandbox, got %+v", denial)
	}
}

func TestLatestToolFailureRetryEvaluation(t *testing.T) {
	now := time.Now().UTC()
	records := []EvaluationRecord{
		{Cause: "resume_policy_decision", Summary: "Policy only.", UpdatedAt: now.Add(2 * time.Second)},
		{Cause: "tool_permission_denied", Summary: "Tool write/file_write was denied.", Details: []string{"retryable: false", "reason: tool_failure_permission_gated", "permission_mode: dontAsk"}, UpdatedAt: now.Add(time.Second)},
		{Cause: "tool_execution_failed", Summary: "Tool failed.", Details: []string{"retryable: true", "reason: tool_failure_retryable"}, UpdatedAt: now},
	}
	retry := LatestToolFailureRetryEvaluation(records)
	if retry == nil {
		t.Fatal("expected latest tool failure retry evaluation")
	}
	if retry.Cause != "tool_permission_denied" {
		t.Fatalf("expected latest tool permission denial, got %+v", retry)
	}
	if EvaluationRecordRetryable(*retry) != "false" {
		t.Fatalf("expected retryable false, got %+v", retry)
	}
	if EvaluationRecordReason(*retry) != "tool_failure_permission_gated" {
		t.Fatalf("expected permission-gated reason, got %+v", retry)
	}
	if EvaluationRecordPermissionMode(*retry) != "dontAsk" {
		t.Fatalf("expected permission mode dontAsk, got %+v", retry)
	}
}

func TestPendingToolApprovalEvaluations(t *testing.T) {
	now := time.Now().UTC()
	records := []EvaluationRecord{
		{Cause: "tool_approval_granted", Summary: "Approval granted for key-b.", Details: []string{"approval_key: key-b"}, UpdatedAt: now.Add(3 * time.Second)},
		{Cause: "tool_approval_required", Summary: "Approval required for key-c.", Details: []string{"approval_key: key-c", "operation: file_patch"}, UpdatedAt: now.Add(2 * time.Second)},
		{Cause: "tool_approval_required", Summary: "Approval required for key-b.", Details: []string{"approval_key: key-b", "operation: file_write"}, UpdatedAt: now.Add(time.Second)},
		{Cause: "tool_approval_required", Summary: "Approval required for key-a.", Details: []string{"approval_key: key-a", "operation: shell_exec"}, UpdatedAt: now},
	}
	pending := PendingToolApprovalEvaluations(records)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending approvals, got %+v", pending)
	}
	if EvaluationRecordApprovalKey(pending[0]) != "key-c" {
		t.Fatalf("expected latest unresolved approval key-c first, got %+v", pending)
	}
	if EvaluationRecordApprovalKey(pending[1]) != "key-a" {
		t.Fatalf("expected older unresolved approval key-a second, got %+v", pending)
	}
	selected := PendingToolApprovalEvaluationByKey(records, "key-a")
	if selected == nil || EvaluationRecordOperation(*selected) != "shell_exec" {
		t.Fatalf("expected keyed pending approval lookup for key-a, got %+v", selected)
	}
	latest := LatestPendingToolApprovalEvaluation(records)
	if latest == nil || EvaluationRecordApprovalKey(*latest) != "key-c" {
		t.Fatalf("expected latest pending approval key-c, got %+v", latest)
	}
}

func TestLatestToolApprovalReplayEvaluationIncludesSkippedReplay(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "tool_approval_replay_skipped", Summary: "Approval replay skipped.", Details: []string{"approval_key: key-a", "replay_status: skipped"}},
		{Cause: "tool_approval_replayed", Summary: "Approval replayed.", Details: []string{"approval_key: key-a"}},
	}
	replay := LatestToolApprovalReplayEvaluation(records)
	if replay == nil || replay.Cause != "tool_approval_replay_skipped" {
		t.Fatalf("expected skipped replay to count as latest approval replay evaluation, got %+v", replay)
	}
}

func TestLatestResumeAttemptEvaluation(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "resume_attempt_recorded", Summary: "Approval replay skipped.", Details: []string{"pause_point_id: pause-a", "resume_attempt_id: resume-a", "resume_attempt_status: skipped", "continuation_run_id: run-a"}},
		{Cause: "resume_attempt_recorded", Summary: "Approval continuation completed.", Details: []string{"pause_point_id: pause-b", "resume_attempt_id: resume-b", "resume_attempt_status: completed", "continuation_run_id: run-b"}},
	}
	attempt := LatestResumeAttemptEvaluation(records)
	if attempt == nil {
		t.Fatal("expected latest resume attempt evaluation")
	}
	if EvaluationRecordPausePointID(*attempt) != "pause-a" {
		t.Fatalf("expected pause-a, got %+v", attempt)
	}
	if EvaluationRecordResumeAttemptID(*attempt) != "resume-a" {
		t.Fatalf("expected resume-a, got %+v", attempt)
	}
	if EvaluationRecordResumeAttemptStatus(*attempt) != "skipped" {
		t.Fatalf("expected skipped status, got %+v", attempt)
	}
}

func TestLatestResumePolicyEvaluation(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "resume_policy_decision", Summary: "Retry denied.", Details: []string{"pause_point_id: pause-a", "resume_attempt_id: resume-a", "retryable: false", "reason: already_completed"}},
		{Cause: "resume_attempt_recorded", Summary: "Attempt completed.", Details: []string{"pause_point_id: pause-a", "resume_attempt_id: resume-a", "resume_attempt_status: completed"}},
	}
	policy := LatestResumePolicyEvaluation(records)
	if policy == nil {
		t.Fatal("expected latest resume policy evaluation")
	}
	if EvaluationRecordRetryable(*policy) != "false" {
		t.Fatalf("expected retryable false, got %+v", policy)
	}
	if EvaluationRecordReason(*policy) != "already_completed" {
		t.Fatalf("expected already_completed reason, got %+v", policy)
	}
}

func TestLatestFailedNodePausePointEvaluation(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "resume_policy_decision", Summary: "Policy only.", Details: []string{"pause_point_id: pause-a", "status_reason: prior"}},
		{Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-b", "pause_point_kind: workflow_node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-b", "origin_task_id: task-b"}},
	}
	record := LatestFailedNodePausePointEvaluation(records)
	if record == nil {
		t.Fatal("expected latest failed-node pause point evaluation")
	}
	if EvaluationRecordPausePointID(*record) != "pause-b" {
		t.Fatalf("expected pause point id pause-b, got %+v", record)
	}
	if EvaluationRecordStatusReason(*record) != "tool failed" {
		t.Fatalf("expected status reason tool failed, got %+v", record)
	}
	if EvaluationRecordRetryable(*record) != "true" {
		t.Fatalf("expected retryable true, got %+v", record)
	}
}

func TestLatestFailedNodeRetryCandidate(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "tool_approval_required", Summary: "Approval required.", Details: []string{"pause_point_id: pause-approval", "approval_key: approval-a", "expected_targets: note.txt"}},
		{RunID: "run-b", TaskID: "task-b", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-b", "pause_point_kind: workflow_node", "pause_point_digest: digest-b", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-b", "origin_task_id: task-b", "expected_targets: note.txt, process_record.md"}},
	}
	candidate := LatestFailedNodeRetryCandidate(records)
	if candidate == nil {
		t.Fatal("expected latest failed-node retry candidate")
	}
	if !candidate.ContractReady {
		t.Fatalf("expected contract ready candidate, got %+v", candidate)
	}
	if candidate.PausePointID != "pause-b" || candidate.PausePointKind != "workflow_node" || candidate.PausePointDigest != "digest-b" {
		t.Fatalf("expected retry coordinates, got %+v", candidate)
	}
	if candidate.RequiredAuthority != "workflow_scheduler" || candidate.GuardStatus != "manual_review_required" {
		t.Fatalf("expected scheduler-scoped manual review candidate, got %+v", candidate)
	}
	if len(candidate.ExpectedTargets) != 2 || candidate.ExpectedTargets[0] != "note.txt" || candidate.ExpectedTargets[1] != "process_record.md" {
		t.Fatalf("expected retry targets, got %+v", candidate.ExpectedTargets)
	}
}

func TestLatestFailedNodeRetryCandidateMarksMissingFields(t *testing.T) {
	candidate := LatestFailedNodeRetryCandidate([]EvaluationRecord{
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-c", "node_id: node-build", "retryable: true"}},
	})
	if candidate == nil {
		t.Fatal("expected retry candidate")
	}
	if candidate.ContractReady {
		t.Fatalf("expected incomplete candidate, got %+v", candidate)
	}
	for _, want := range []string{"origin_run_id", "origin_task_id", "pause_point_kind=workflow_node", "pause_point_digest", "status_reason"} {
		found := false
		for _, missing := range candidate.MissingFields {
			if missing == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected missing field %q in %+v", want, candidate.MissingFields)
		}
	}
}

func TestEvaluateFailedNodeRetryPreflightAllowsMatchingRecordedTruth(t *testing.T) {
	records := []EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "depends_on: node-plan", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review", "expected_targets: note.txt, process_record.md"}},
	}
	boundary := EvaluateFailedNodeRetryPreflight(records, FailedNodeRetryPreflightRequest{
		TaskID:                  "task-a",
		OriginRunID:             "run-origin",
		OriginTaskID:            "task-a",
		PausePointID:            "pause-node",
		NodeID:                  "node-build",
		PausePointDigest:        "digest-node",
		ExpectedTargets:         []string{"process_record.md", "note.txt", "note.txt"},
		VerifierGateExpectation: "verify mutating targets before closure",
		OperatorConfirmed:       true,
		OperatorSource:          "task_operator",
	})
	if !boundary.Allowed {
		t.Fatalf("expected matching preflight to be allowed, got %+v", boundary)
	}
	if len(boundary.Reasons) != 0 {
		t.Fatalf("expected no preflight reasons, got %+v", boundary.Reasons)
	}
	if boundary.RequiredAuthority != "workflow_scheduler" || boundary.GuardStatus != "manual_review_required" {
		t.Fatalf("expected scheduler manual-review boundary, got %+v", boundary)
	}
	if len(boundary.ExpectedTargets) != 2 || boundary.ExpectedTargets[0] != "process_record.md" || boundary.ExpectedTargets[1] != "note.txt" {
		t.Fatalf("expected normalized request targets to be preserved, got %+v", boundary.ExpectedTargets)
	}
}

func TestEvaluateFailedNodeRetryPreflightRejectsMismatchedRecordedTruth(t *testing.T) {
	records := []EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review", "expected_targets: note.txt"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt requested.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: requested", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
	}
	boundary := EvaluateFailedNodeRetryPreflight(records, FailedNodeRetryPreflightRequest{
		TaskID:                  "task-b",
		OriginRunID:             "run-other",
		OriginTaskID:            "task-other",
		PausePointID:            "pause-other",
		NodeID:                  "node-other",
		PausePointDigest:        "digest-other",
		ExpectedTargets:         []string{"other.txt"},
		VerifierGateExpectation: "different verifier gate",
		OperatorConfirmed:       false,
	})
	if boundary.Allowed {
		t.Fatalf("expected mismatched preflight to be denied, got %+v", boundary)
	}
	for _, want := range []string{"operator_confirmation_missing", "task_id_mismatch", "origin_run_id_mismatch", "origin_task_id_mismatch", "pause_point_id_mismatch", "node_id_mismatch", "pause_point_digest_mismatch", "expected_targets_mismatch", "verifier_gate_expectation_mismatch"} {
		if !containsString(boundary.Reasons, want) {
			t.Fatalf("expected reason %q in %+v", want, boundary.Reasons)
		}
	}
}

func TestEvaluateFailedNodeRetryPreflightRejectsCurrentStateAndAttemptStateGaps(t *testing.T) {
	basePause := EvaluationRecord{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}}
	request := FailedNodeRetryPreflightRequest{
		TaskID:                  "task-a",
		OriginRunID:             "run-origin",
		OriginTaskID:            "task-a",
		PausePointID:            "pause-node",
		NodeID:                  "node-build",
		PausePointDigest:        "digest-node",
		ExpectedTargets:         []string{"note.txt"},
		VerifierGateExpectation: "verify mutating targets before closure",
		OperatorConfirmed:       true,
	}
	missingReadiness := EvaluateFailedNodeRetryPreflight([]EvaluationRecord{basePause}, request)
	if missingReadiness.Allowed || !containsString(missingReadiness.Reasons, "current_state_readiness_incomplete") {
		t.Fatalf("expected current-state readiness denial, got %+v", missingReadiness)
	}
	readyPause := basePause
	readyPause.Details = append(append([]string(nil), basePause.Details...), "current_node_status: failed", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review")
	for _, tc := range []struct {
		status string
		reason string
	}{
		{status: "requested", reason: "retry_attempt_in_flight"},
		{status: "started", reason: "retry_attempt_in_flight"},
		{status: "completed", reason: "terminal_retry_scope"},
		{status: "skipped", reason: "terminal_retry_scope"},
		{status: "failed", reason: "retry_after_failure_policy_missing"},
		{status: "denied", reason: "retry_after_denial_policy_missing"},
	} {
		boundary := EvaluateFailedNodeRetryPreflight([]EvaluationRecord{
			readyPause,
			{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt " + tc.status + ".", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: " + tc.status, "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
		}, request)
		if boundary.Allowed || !containsString(boundary.Reasons, tc.reason) {
			t.Fatalf("expected %s denial for status %s, got %+v", tc.reason, tc.status, boundary)
		}
	}
	allowedAfterFailurePolicy := EvaluateFailedNodeRetryPreflight([]EvaluationRecord{
		readyPause,
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt failed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: failed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
		{RunID: "run-policy", TaskID: "task-a", Cause: "failed_node_retry_after_failure_policy", Summary: "Retry after failure allowed by operator policy.", Details: []string{"retry_attempt_id: retry-a", "allowed: true"}},
	}, request)
	if !allowedAfterFailurePolicy.Allowed {
		t.Fatalf("expected after-failure policy to allow preflight, got %+v", allowedAfterFailurePolicy)
	}
}

func TestEvaluateFailedNodeRetryPreflightRejectsIncompleteCandidateAndMissingGate(t *testing.T) {
	boundary := EvaluateFailedNodeRetryPreflight([]EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: approval_gate", "pause_point_digest: digest-node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
	}, FailedNodeRetryPreflightRequest{
		TaskID:            "task-a",
		OriginRunID:       "run-origin",
		OriginTaskID:      "task-a",
		PausePointID:      "pause-node",
		NodeID:            "node-build",
		PausePointDigest:  "digest-node",
		ExpectedTargets:   []string{"note.txt"},
		OperatorConfirmed: true,
	})
	if boundary.Allowed {
		t.Fatalf("expected incomplete preflight to be denied, got %+v", boundary)
	}
	for _, want := range []string{"pause_point_kind_not_workflow_node", "candidate_contract_incomplete", "verifier_gate_expectation_missing"} {
		if !containsString(boundary.Reasons, want) {
			t.Fatalf("expected reason %q in %+v", want, boundary.Reasons)
		}
	}
	missing := EvaluateFailedNodeRetryPreflight(nil, FailedNodeRetryPreflightRequest{OperatorConfirmed: true})
	if missing.Allowed || !containsString(missing.Reasons, "failed_node_retry_candidate_missing") {
		t.Fatalf("expected missing candidate denial, got %+v", missing)
	}
}

func TestFailedNodeRetryArtifactFromPreflightBuildsSchedulerScopedArtifact(t *testing.T) {
	records := []EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review", "expected_targets: note.txt, process_record.md"}},
		{RunID: "run-approval", TaskID: "task-a", Cause: "resume_attempt_recorded", Summary: "Approval replay attempt.", Details: []string{"retry_attempt_id: retry-approval", "resume_attempt_id: resume-approval", "resume_attempt_status: completed", "pause_point_id: pause-node", "pause_point_digest: digest-node", "node_id: node-build", "expected_targets: note.txt, process_record.md"}},
		{RunID: "run-remediate", TaskID: "task-a", Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation attempt.", Details: []string{"retry_attempt_id: retry-remediate", "resume_attempt_id: resume-remediate", "resume_attempt_status: completed", "pause_point_id: pause-node", "pause_point_digest: digest-node", "node_id: node-build", "expected_targets: note.txt, process_record.md"}},
	}
	boundary := EvaluateFailedNodeRetryPreflight(records, FailedNodeRetryPreflightRequest{
		TaskID:                  "task-a",
		OriginRunID:             "run-origin",
		OriginTaskID:            "task-a",
		PausePointID:            "pause-node",
		NodeID:                  "node-build",
		PausePointDigest:        "digest-node",
		ExpectedTargets:         []string{"process_record.md", "note.txt"},
		VerifierGateExpectation: "verify mutating targets before closure",
		OperatorConfirmed:       true,
		OperatorSource:          "task_operator",
	})
	artifact := FailedNodeRetryArtifactFromPreflight(records, boundary)
	if artifact == nil {
		t.Fatal("expected retry artifact")
	}
	if artifact.RetryArtifactID == "" || artifact.RetryAttemptID == "" {
		t.Fatalf("expected artifact and attempt ids, got %+v", artifact)
	}
	if artifact.TaskID != "task-a" || artifact.OriginRunID != "run-origin" || artifact.OriginTaskID != "task-a" {
		t.Fatalf("expected task/run coordinates, got %+v", artifact)
	}
	if artifact.PausePointID != "pause-node" || artifact.PausePointDigest != "digest-node" || artifact.FailedNodeID != "node-build" {
		t.Fatalf("expected pause/node coordinates, got %+v", artifact)
	}
	if artifact.StatusReason != "tool failed" || artifact.VerifierGateExpectation != "verify mutating targets before closure" {
		t.Fatalf("expected status reason and verifier gate expectation, got %+v", artifact)
	}
	if artifact.RequiredAuthority != "workflow_scheduler" || artifact.GuardStatus != "manual_review_required" {
		t.Fatalf("expected scheduler-scoped manual review artifact, got %+v", artifact)
	}
	if artifact.Terminal {
		t.Fatalf("expected approval/verifier attempts to be ignored for failed-node retry terminal state, got %+v", artifact)
	}
}

func TestFailedNodeRetryArtifactFromPreflightDetectsTerminalAttempt(t *testing.T) {
	records := []EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review", "expected_targets: note.txt"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Details: []string{"retry_attempt_id: retry-completed", "retry_attempt_status: completed", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt failed.", Details: []string{"retry_attempt_id: retry-failed", "retry_attempt_status: failed", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
	}
	boundary := EvaluateFailedNodeRetryPreflight(records, FailedNodeRetryPreflightRequest{
		TaskID:                  "task-a",
		OriginRunID:             "run-origin",
		OriginTaskID:            "task-a",
		PausePointID:            "pause-node",
		NodeID:                  "node-build",
		PausePointDigest:        "digest-node",
		ExpectedTargets:         []string{"note.txt"},
		VerifierGateExpectation: "verify mutating targets before closure",
		OperatorConfirmed:       true,
		OperatorSource:          "task_operator",
	})
	if boundary.Allowed || !containsString(boundary.Reasons, "terminal_retry_scope") {
		t.Fatalf("expected terminal retry scope denial, got %+v", boundary)
	}
	artifact := FailedNodeRetryArtifactFromPreflight(records, boundary)
	if artifact != nil {
		t.Fatalf("expected terminal retry scope to produce no executable artifact, got %+v", artifact)
	}
}

func TestFailedNodeRetryArtifactFromPreflightRequiresAllowedBoundary(t *testing.T) {
	artifact := FailedNodeRetryArtifactFromPreflight([]EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
	}, FailedNodeRetryPreflightBoundary{Allowed: false, Reasons: []string{"pause_point_id_mismatch"}})
	if artifact != nil {
		t.Fatalf("expected denied preflight to produce no artifact, got %+v", artifact)
	}
}

func TestLatestFailedNodeRetryArtifactBuildsReadOnlyInspectionArtifact(t *testing.T) {
	records := []EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review"}},
	}
	artifact := LatestFailedNodeRetryArtifact(records)
	if artifact == nil {
		t.Fatal("expected latest failed-node retry artifact")
	}
	if artifact.RetryArtifactID == "" || artifact.RetryAttemptID == "" {
		t.Fatalf("expected deterministic artifact and attempt ids, got %+v", artifact)
	}
	if artifact.OperatorSource != "artifact_inspection" {
		t.Fatalf("expected inspection source, got %+v", artifact)
	}
	if artifact.RequiredAuthority != "workflow_scheduler" || artifact.GuardStatus != "manual_review_required" {
		t.Fatalf("expected read-only scheduler/manual guard, got %+v", artifact)
	}
	if artifact.VerifierGateExpectation != "no mutating targets recorded" {
		t.Fatalf("expected non-mutating verifier gate expectation, got %+v", artifact)
	}
	if artifact.Terminal {
		t.Fatalf("expected non-terminal inspection artifact, got %+v", artifact)
	}
}

func TestLatestFailedNodeRetryArtifactRequiresVerifierGateForTargets(t *testing.T) {
	artifact := LatestFailedNodeRetryArtifact([]EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
	})
	if artifact != nil {
		t.Fatalf("expected missing verifier gate expectation to suppress artifact, got %+v", artifact)
	}
}

func TestLatestFailedNodeRetryCurrentStateReadiness(t *testing.T) {
	readiness := LatestFailedNodeRetryCurrentStateReadiness([]EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a", "depends_on: node-plan, node-survey", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review"}},
	})
	if readiness == nil {
		t.Fatal("expected current-state readiness")
	}
	if !readiness.Ready || len(readiness.MissingEvidence) != 0 {
		t.Fatalf("expected readiness to be complete, got %+v", readiness)
	}
	if readiness.CurrentNodeStatus != "failed" || readiness.DependencyReadiness != "recorded" || readiness.SchedulerContinuationReadiness != "requires_manual_review" {
		t.Fatalf("expected current-state fields, got %+v", readiness)
	}
	if len(readiness.DependsOn) != 2 || readiness.DependsOn[0] != "node-plan" || readiness.DependsOn[1] != "node-survey" {
		t.Fatalf("expected dependency ids, got %+v", readiness.DependsOn)
	}
	if readiness.RequiredAuthority != "workflow_scheduler" || readiness.GuardStatus != "manual_review_required" {
		t.Fatalf("expected manual scheduler guard, got %+v", readiness)
	}
}

func TestLatestFailedNodeRetryCurrentStateReadinessShowsMissingEvidence(t *testing.T) {
	readiness := LatestFailedNodeRetryCurrentStateReadiness([]EvaluationRecord{
		{RunID: "run-origin", TaskID: "task-a", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: task-a"}},
	})
	if readiness == nil {
		t.Fatal("expected current-state readiness")
	}
	if readiness.Ready {
		t.Fatalf("expected readiness to be incomplete, got %+v", readiness)
	}
	for _, want := range []string{"current_node_status", "dependency_readiness", "scheduler_continuation_readiness"} {
		if !containsString(readiness.MissingEvidence, want) {
			t.Fatalf("expected missing evidence %q in %+v", want, readiness.MissingEvidence)
		}
	}
}

func TestLatestFailedNodeRetryAttempt(t *testing.T) {
	now := time.Now().UTC()
	records := []EvaluationRecord{
		{RunID: "run-ignored", TaskID: "task-ignored", Cause: "resume_attempt_recorded", Summary: "Approval replay attempt.", Details: []string{"pause_point_id: pause-approval", "resume_attempt_id: resume-approval", "resume_attempt_status: completed"}, UpdatedAt: now.Add(2 * time.Second)},
		{RunID: "run-ignored", TaskID: "task-ignored", Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation attempt.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "resume_attempt_status: started"}, UpdatedAt: now.Add(time.Second)},
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt started.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: started", "retry_attempt_policy_reason: retry requested by operator after failed workflow node", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "retry_attempt_closure_status: pending_node_work", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "status_reason: tool failed", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt, process_record.md"}, UpdatedAt: now},
	}
	attempt := LatestFailedNodeRetryAttempt(records)
	if attempt == nil {
		t.Fatal("expected latest failed-node retry attempt")
	}
	if attempt.RetryAttemptID != "retry-a" || attempt.RetryAttemptStatus != "started" {
		t.Fatalf("expected started retry attempt, got %+v", attempt)
	}
	if !attempt.IsOpen() || attempt.IsClosed() || attempt.IsFailed() {
		t.Fatalf("expected started attempt to be open only, got %+v", attempt)
	}
	if attempt.RetryAttemptPolicyReason != "retry requested by operator after failed workflow node" {
		t.Fatalf("expected policy reason, got %+v", attempt)
	}
	if attempt.RetryAttemptVerifierGateExpectation != "verify mutating targets before closure" || attempt.RetryAttemptClosureStatus != "pending_node_work" {
		t.Fatalf("expected verifier gate expectation and closure status, got %+v", attempt)
	}
	if attempt.TaskID != "task-a" || attempt.OriginRunID != "run-origin" || attempt.OriginTaskID != "task-a" {
		t.Fatalf("expected task/run coordinates, got %+v", attempt)
	}
	if attempt.PausePointID != "pause-node" || attempt.PausePointKind != "workflow_node" || attempt.PausePointDigest != "digest-node" {
		t.Fatalf("expected pause coordinates, got %+v", attempt)
	}
	if attempt.NodeID != "node-build" || attempt.StatusReason != "tool failed" {
		t.Fatalf("expected failed node coordinates, got %+v", attempt)
	}
	if len(attempt.ExpectedTargets) != 2 || attempt.ExpectedTargets[0] != "note.txt" || attempt.ExpectedTargets[1] != "process_record.md" {
		t.Fatalf("expected retry attempt targets, got %+v", attempt.ExpectedTargets)
	}
	if attempt.RequiredAuthority != "workflow_scheduler" || attempt.GuardStatus != "manual_review_required" {
		t.Fatalf("expected scheduler authority metadata without execution readiness, got %+v", attempt)
	}
}

func TestFailedNodeRetryAttemptStatusHelpers(t *testing.T) {
	cases := []struct {
		status     string
		wantOpen   bool
		wantClosed bool
		wantFailed bool
	}{
		{status: "requested", wantOpen: true},
		{status: "started", wantOpen: true},
		{status: "completed", wantClosed: true},
		{status: "skipped", wantClosed: true},
		{status: "denied", wantClosed: true},
		{status: "failed", wantFailed: true},
	}
	for _, tc := range cases {
		attempt := FailedNodeRetryAttempt{RetryAttemptStatus: tc.status}
		if attempt.IsOpen() != tc.wantOpen || attempt.IsClosed() != tc.wantClosed || attempt.IsFailed() != tc.wantFailed {
			t.Fatalf("unexpected status helpers for %q: open=%v closed=%v failed=%v", tc.status, attempt.IsOpen(), attempt.IsClosed(), attempt.IsFailed())
		}
	}
}

func TestFailedNodeRetryAttemptChainFiltersOtherRecoveryAttempts(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "resume_attempt_recorded", Summary: "Approval replay attempt.", Details: []string{"retry_attempt_id: retry-approval", "resume_attempt_id: resume-approval", "resume_attempt_status: completed"}},
		{Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation attempt.", Details: []string{"retry_attempt_id: retry-remediate", "resume_attempt_id: resume-remediate", "resume_attempt_status: started"}},
		{Cause: "failed_node_retry_attempt", Summary: "Missing retry id.", Details: []string{"retry_attempt_status: requested", "pause_point_id: pause-missing"}},
		{Cause: "failed_node_retry_attempt", Summary: "Retry attempt two.", Details: []string{"retry_attempt_id: retry-b", "retry_attempt_status: failed", "pause_point_id: pause-b"}},
		{Cause: "failed_node_retry_attempt", Summary: "Retry attempt one.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "pause_point_id: pause-a"}},
	}
	chain := LatestFailedNodeRetryAttemptChain(records, 5)
	if len(chain) != 2 {
		t.Fatalf("expected only failed-node retry attempts with ids, got %+v", chain)
	}
	if chain[0].RetryAttemptID != "retry-b" || !chain[0].IsFailed() || chain[0].IsClosed() {
		t.Fatalf("expected latest failed attempt first and not closed, got %+v", chain[0])
	}
	if chain[1].RetryAttemptID != "retry-a" || !chain[1].IsClosed() {
		t.Fatalf("expected completed attempt second and closed, got %+v", chain[1])
	}
	if latest := LatestFailedNodeRetryAttempt(records); latest == nil || latest.RetryAttemptID != "retry-b" {
		t.Fatalf("expected latest retry-b attempt, got %+v", latest)
	}
}

func TestLatestFailedNodeRetryAttemptClosureEvidence(t *testing.T) {
	records := []EvaluationRecord{
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "status_reason: tool failed", "origin_run_id: run-origin", "origin_task_id: task-a", "expected_targets: note.txt"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "node_work_evidence", Summary: "Retried node work completed.", Details: []string{"retry_attempt_id: retry-a", "node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: completed", "origin_run_id: run-retry", "origin_task_id: task-a", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verified: true", "verification_report_path: .avatars/verification/retry-a.json"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_scheduler_resume", Summary: "Dependent workflow nodes resumed after retry.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed", "continuation_run_id: run-retry", "continuation_task_id: task-a"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"retry_attempt_id: retry-a", "status: completed", "origin_run_id: run-retry", "origin_task_id: task-a"}},
	}
	closure := LatestFailedNodeRetryAttemptClosureEvidence(records)
	if closure == nil {
		t.Fatal("expected retry attempt closure evidence")
	}
	if closure.RetryAttemptID != "retry-a" || !closure.ClosureReady {
		t.Fatalf("expected ready closure for retry-a, got %+v", closure)
	}
	if len(closure.MissingEvidence) != 0 {
		t.Fatalf("expected no missing evidence, got %+v", closure.MissingEvidence)
	}
	if closure.NodeWorkNodeID != "node-build" || closure.NodeWorkStatus != "completed" {
		t.Fatalf("expected node work evidence, got %+v", closure)
	}
	if closure.NodeWorkVerifierGateStatus != "verified_pass" || closure.NodeWorkVerifierVerdict != "PASS" || closure.NodeWorkVerificationReportPath == "" {
		t.Fatalf("expected verifier gate evidence, got %+v", closure)
	}
	if closure.SchedulerResumeStatus != "completed" || closure.SchedulerResumeRunID != "run-retry" || closure.SchedulerResumeTaskID != "task-a" {
		t.Fatalf("expected scheduler resume evidence, got %+v", closure)
	}
	if closure.LifecycleStatus != "completed" || closure.LifecycleRunID != "run-retry" || closure.LifecycleTaskID != "task-a" {
		t.Fatalf("expected lifecycle evidence, got %+v", closure)
	}
	if !closure.IsInspectionOnly() {
		t.Fatalf("expected closure evidence to remain inspection-only, got %+v", closure)
	}
}

func TestRemediationEnvelopeIncludesFailedNodeRetryClosureVerifierFacts(t *testing.T) {
	envelope := RemediationEnvelope("task-a", nil, nil, []EvaluationRecord{
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "expected_targets: note.txt"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "node_work_evidence", Summary: "Retried node work completed.", Details: []string{"retry_attempt_id: retry-a", "node_id: node-build", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verification_report_path: verifier/retry-a.json"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_scheduler_resume", Summary: "Scheduler resume completed.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated.", Details: []string{"retry_attempt_id: retry-a", "status: completed"}},
	})
	if envelope == nil {
		t.Fatal("expected remediation envelope")
	}
	if envelope.FailedNodeRetryClosureStatus != "completed" || envelope.FailedNodeRetryClosureReady != "true" {
		t.Fatalf("expected failed-node retry closure fields, got %+v", envelope)
	}
	if envelope.FailedNodeRetryClosureVerifierGate != "verified_pass" || envelope.FailedNodeRetryClosureVerifierVerdict != "PASS" || envelope.FailedNodeRetryClosureReport != "verifier/retry-a.json" {
		t.Fatalf("expected failed-node retry closure verifier fields, got %+v", envelope)
	}
	if len(envelope.ExpectedTargets) != 1 || envelope.ExpectedTargets[0] != "note.txt" {
		t.Fatalf("expected retry closure targets, got %+v", envelope)
	}
}

func TestRetryClosureVerifierFactsStayAlignedAcrossContracts(t *testing.T) {
	const (
		taskID     = "task-a"
		gateStatus = "verified_pass"
		verdict    = "PASS"
		reportPath = "verifier/retry-a.json"
	)
	records := []EvaluationRecord{
		{RunID: "run-retry", TaskID: taskID, Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "expected_targets: note.txt"}},
		{RunID: "run-retry", TaskID: taskID, Cause: "node_work_evidence", Summary: "Retried node work completed.", Details: []string{"retry_attempt_id: retry-a", "node_id: node-build", "status: completed", "verifier_gate_status: " + gateStatus, "verifier_verdict: " + verdict, "verification_report_path: " + reportPath}},
		{RunID: "run-retry", TaskID: taskID, Cause: "failed_node_retry_scheduler_resume", Summary: "Scheduler resume completed.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed"}},
		{RunID: "run-retry", TaskID: taskID, Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated.", Details: []string{"retry_attempt_id: retry-a", "status: completed"}},
	}
	snapshot := Snapshot{EvaluationRecords: records}

	closure := LatestFailedNodeRetryAttemptClosureEvidence(records)
	if closure == nil {
		t.Fatal("expected retry closure evidence")
	}
	if closure.NodeWorkVerifierGateStatus != gateStatus || closure.NodeWorkVerifierVerdict != verdict || closure.NodeWorkVerificationReportPath != reportPath {
		t.Fatalf("expected closure verifier facts to match fixture, got %+v", closure)
	}

	governance := WorkflowNodeGovernanceSnapshotForTask(snapshot)
	if governance.RetryClosureVerifierGateStatus != gateStatus || governance.RetryClosureVerifierVerdict != verdict || governance.RetryClosureVerifierReportPath != reportPath {
		t.Fatalf("expected governance verifier facts to match fixture, got %+v", governance)
	}

	context := PromptVerificationContext(taskID, snapshot)
	for _, want := range []string{
		"failed_node_retry_attempt_closure_verifier_gate_status: " + gateStatus,
		"failed_node_retry_attempt_closure_verifier_verdict: " + verdict,
		"failed_node_retry_attempt_closure_verification_report_path: " + reportPath,
	} {
		if !strings.Contains(context, want) {
			t.Fatalf("expected prompt context to include %q, got %q", want, context)
		}
	}

	envelope := RemediationEnvelope(taskID, nil, nil, records)
	if envelope == nil {
		t.Fatal("expected remediation envelope")
	}
	if envelope.FailedNodeRetryClosureVerifierGate != gateStatus || envelope.FailedNodeRetryClosureVerifierVerdict != verdict || envelope.FailedNodeRetryClosureReport != reportPath {
		t.Fatalf("expected planner envelope verifier facts to match fixture, got %+v", envelope)
	}
}

func TestFailedNodeRetryAttemptClosureEvidenceMarksMissingAndRequiresBinding(t *testing.T) {
	records := []EvaluationRecord{
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_attempt", Summary: "Failed-node retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "node_work_evidence", Summary: "Unbound node work completed.", Details: []string{"node_id: node-build", "node_role: Builder", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS"}},
		{RunID: "run-retry", TaskID: "task-a", Cause: "failed_node_retry_scheduler_resume", Summary: "Bound scheduler resume.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed"}},
	}
	closure := LatestFailedNodeRetryAttemptClosureEvidence(records)
	if closure == nil {
		t.Fatal("expected retry attempt closure evidence")
	}
	if closure.ClosureReady {
		t.Fatalf("expected incomplete closure evidence, got %+v", closure)
	}
	if closure.NodeWorkNodeID != "" || closure.NodeWorkVerifierGateStatus != "" {
		t.Fatalf("expected unbound node work evidence to be ignored, got %+v", closure)
	}
	for _, want := range []string{"node_work_evidence", "verifier_gate_evidence", "lifecycle_evidence"} {
		found := false
		for _, missing := range closure.MissingEvidence {
			if missing == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected missing evidence %q in %+v", want, closure.MissingEvidence)
		}
	}
	if !closure.IsInspectionOnly() {
		t.Fatalf("expected closure evidence to remain inspection-only, got %+v", closure)
	}
}

func TestPromptVerificationContextIncludesFailedNodeRetryAttemptEvidence(t *testing.T) {
	now := time.Date(2026, time.May, 5, 3, 14, 49, 0, time.UTC)
	snapshot := Snapshot{
		EvaluationRecords: []EvaluationRecord{
			{RunID: "run-origin", TaskID: "demo-task", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "current_node_status: failed", "status_reason: tool failed", "retryable: true", "origin_run_id: run-origin", "origin_task_id: demo-task", "depends_on: node-plan", "dependency_readiness: recorded", "scheduler_continuation_readiness: requires_manual_review", "expected_targets: note.txt"}, UpdatedAt: now.Add(4 * time.Second)},
			{RunID: "run-retry", TaskID: "demo-task", Cause: "failed_node_retry_attempt", Summary: "Retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure", "pause_point_id: pause-node", "pause_point_kind: workflow_node", "pause_point_digest: digest-node", "node_id: node-build", "origin_run_id: run-origin", "origin_task_id: demo-task", "expected_targets: note.txt"}, UpdatedAt: now.Add(3 * time.Second)},
			{RunID: "run-retry", TaskID: "demo-task", Cause: "node_work_evidence", Summary: "Retry node work completed.", Details: []string{"retry_attempt_id: retry-a", "node_id: node-build", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verification_report_path: verifier/retry-a.json"}, UpdatedAt: now.Add(2500 * time.Millisecond)},
			{RunID: "run-retry", TaskID: "demo-task", Cause: "failed_node_retry_attempt", Summary: "Retry attempt started.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: started"}, UpdatedAt: now.Add(2 * time.Second)},
			{RunID: "run-retry", TaskID: "demo-task", Cause: "failed_node_retry_scheduler_resume", Summary: "Retry scheduler resume completed.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed", "continuation_run_id: run-retry", "continuation_task_id: demo-task"}, UpdatedAt: now},
			{RunID: "run-retry", TaskID: "demo-task", Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"retry_attempt_id: retry-a", "status: completed", "origin_run_id: run-retry", "origin_task_id: demo-task"}, UpdatedAt: now.Add(-time.Second)},
		},
	}
	context := PromptVerificationContext("demo-task", snapshot)
	if !strings.Contains(context, "failed_node_retry_attempt_id: retry-a") {
		t.Fatalf("expected retry attempt id in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_attempt_status: completed") {
		t.Fatalf("expected latest retry attempt status in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_attempt_closed: true") {
		t.Fatalf("expected closed retry attempt flag in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_attempt_closure_ready: true") {
		t.Fatalf("expected closure readiness in prompt context, got %q", context)
	}
	if strings.Contains(context, "failed_node_retry_attempt_closure_missing_evidence:") {
		t.Fatalf("expected complete closure evidence in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_attempt_closure_verifier_gate_status: verified_pass") {
		t.Fatalf("expected closure verifier gate in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_attempt_closure_verifier_verdict: PASS") {
		t.Fatalf("expected closure verifier verdict in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_attempt_closure_verification_report_path: verifier/retry-a.json") {
		t.Fatalf("expected closure verifier report path in prompt context, got %q", context)
	}
	if strings.Contains(context, "failed_node_retry_artifact_id:") {
		t.Fatalf("expected terminal retry scope to suppress executable-looking artifact in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_current_state_ready: true") {
		t.Fatalf("expected current-state readiness in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_current_node_status: failed") || !strings.Contains(context, "failed_node_retry_depends_on: node-plan") {
		t.Fatalf("expected current-state node/dependency fields in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_dependency_readiness: recorded") || !strings.Contains(context, "failed_node_retry_scheduler_continuation_readiness: requires_manual_review") {
		t.Fatalf("expected current-state readiness fields in prompt context, got %q", context)
	}
}

func TestLatestNodeWorkEvidence(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "node_work_evidence", Summary: "Builder awaits approval.", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: replay_completed", "artifact_ids: generated-skill, bootstrap-context", "approval_key: approval-build", "continuation_run_id: continuation-run", "continuation_task_id: continuation-task", "replay_status: completed", "verifier_gate_status: pending_approval", "verifier_verdict: PASS", "verified: true", "verification_report_path: verifier/run.json", "pause_point_id: pause-build", "pause_point_kind: workflow_node", "recovery_kind: approval_required"}},
		{Cause: "node_work_evidence", Summary: "Missing node id.", Details: []string{"node_role: Researcher"}},
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"node_id: node-review", "pause_point_id: pause-review"}},
	}
	record := LatestNodeWorkEvidence(records)
	if record == nil {
		t.Fatal("expected latest node work evidence")
	}
	if EvaluationRecordNodeID(*record) != "node-build" {
		t.Fatalf("expected node-build, got %+v", record)
	}
	if EvaluationRecordNodeRole(*record) != "Builder" {
		t.Fatalf("expected Builder, got %+v", record)
	}
	if EvaluationRecordOperation(*record) != "file_write" {
		t.Fatalf("expected file_write, got %+v", record)
	}
	artifacts := EvaluationRecordArtifactIDs(*record)
	if len(artifacts) != 2 || artifacts[0] != "generated-skill" || artifacts[1] != "bootstrap-context" {
		t.Fatalf("expected artifact ids, got %+v", artifacts)
	}
	if EvaluationRecordPausePointID(*record) != "pause-build" {
		t.Fatalf("expected pause point id pause-build, got %+v", record)
	}
	if EvaluationRecordApprovalKey(*record) != "approval-build" {
		t.Fatalf("expected approval key, got %+v", record)
	}
	if EvaluationRecordContinuationRunID(*record) != "continuation-run" {
		t.Fatalf("expected continuation run id, got %+v", record)
	}
	if EvaluationRecordContinuationTaskID(*record) != "continuation-task" {
		t.Fatalf("expected continuation task id, got %+v", record)
	}
	if EvaluationRecordReplayStatus(*record) != "completed" {
		t.Fatalf("expected completed replay status, got %+v", record)
	}
	if EvaluationRecordVerifierGateStatus(*record) != "pending_approval" {
		t.Fatalf("expected pending approval verifier gate, got %+v", record)
	}
	if EvaluationRecordVerifierVerdict(*record) != "PASS" {
		t.Fatalf("expected verifier verdict PASS, got %+v", record)
	}
	if EvaluationRecordVerified(*record) != "true" {
		t.Fatalf("expected verified true, got %+v", record)
	}
	if EvaluationRecordVerificationReportPath(*record) != "verifier/run.json" {
		t.Fatalf("expected verification report path, got %+v", record)
	}
	if EvaluationRecordRecoveryKind(*record) != "approval_required" {
		t.Fatalf("expected approval_required recovery kind, got %+v", record)
	}
}

func TestWorkflowNodeGovernanceNextActionCue(t *testing.T) {
	pendingApproval := Snapshot{EvaluationRecords: []EvaluationRecord{{Cause: "tool_approval_required", Details: []string{"approval_key: approval-build", "status: pending"}}}}
	if got := WorkflowNodeGovernanceNextActionCue(pendingApproval); got != "inspect pending approval before continuation" {
		t.Fatalf("expected approval cue, got %q", got)
	}

	pendingReverify := Snapshot{Verification: &VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL"}}
	if got := WorkflowNodeGovernanceNextActionCue(pendingReverify); got != "run verifier reverify before closure" {
		t.Fatalf("expected verifier cue, got %q", got)
	}

	retryCandidate := Snapshot{EvaluationRecords: []EvaluationRecord{{Cause: "failed_node_pause_point", Details: []string{"pause_point_id: pause-build", "pause_point_kind: workflow_node", "pause_point_digest: digest-build", "node_id: node-build", "retryable: true", "origin_run_id: run-failed-node", "origin_task_id: demo-task", "expected_targets: note.txt"}}}}
	if got := WorkflowNodeGovernanceNextActionCue(retryCandidate); got != "inspect failed-node retry preflight before scheduler continuation" {
		t.Fatalf("expected retry preflight cue, got %q", got)
	}

	incompleteClosure := Snapshot{EvaluationRecords: []EvaluationRecord{{Cause: "failed_node_retry_attempt", Details: []string{"retry_attempt_id: retry-build", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed"}}}}
	if got := WorkflowNodeGovernanceNextActionCue(incompleteClosure); got != "collect missing retry closure evidence before risky continuation" {
		t.Fatalf("expected missing evidence cue, got %q", got)
	}

	readyClosure := Snapshot{EvaluationRecords: []EvaluationRecord{
		{Cause: "failed_node_retry_attempt", Details: []string{"retry_attempt_id: retry-build", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed"}},
		{Cause: "node_work_evidence", Details: []string{"retry_attempt_id: retry-build", "node_id: node-build", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS"}},
		{Cause: "failed_node_retry_scheduler_resume", Details: []string{"retry_attempt_id: retry-build", "scheduler_resume_status: completed"}},
		{Cause: "run_lifecycle_updated", Details: []string{"retry_attempt_id: retry-build", "status: completed"}},
	}}
	if got := WorkflowNodeGovernanceNextActionCue(readyClosure); got != "manual review required before risky continuation" {
		t.Fatalf("expected manual review cue, got %q", got)
	}

	if got := WorkflowNodeGovernanceNextActionCue(Snapshot{}); got != "" {
		t.Fatalf("expected empty cue without evidence, got %q", got)
	}
}

func TestWorkflowNodeGovernanceSnapshotForTask(t *testing.T) {
	snapshot := Snapshot{Verification: &VerificationSnapshot{Tool: "verify", Verdict: "PASS"}, EvaluationRecords: []EvaluationRecord{
		{TaskID: "demo-task", Cause: "failed_node_pause_point", Details: []string{"pause_point_id: pause-build", "pause_point_kind: workflow_node", "pause_point_digest: digest-build", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-failed-node", "origin_task_id: demo-task", "expected_targets: note.txt"}},
		{Cause: "failed_node_retry_attempt", Details: []string{"retry_attempt_id: retry-build", "retry_attempt_status: completed", "retry_attempt_closure_status: node_scheduler_verifier_lifecycle_closed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
		{Cause: "node_work_evidence", Summary: "Retried workflow node completed.", Details: []string{"retry_attempt_id: retry-build", "node_id: node-build", "status: completed", "recovery_kind: failed_node_retry", "verifier_gate_status: verified_pass", "verifier_verdict: PASS", "verification_report_path: verifier/retry-build.json"}},
		{Cause: "failed_node_retry_scheduler_resume", Details: []string{"retry_attempt_id: retry-build", "scheduler_resume_status: completed"}},
		{Cause: "run_lifecycle_updated", Details: []string{"retry_attempt_id: retry-build", "status: completed"}},
	}}
	governance := WorkflowNodeGovernanceSnapshotForTask(snapshot)
	if !governance.LatestNodeEvidence || governance.LatestNodeID != "node-build" || governance.LatestNodeStatus != "completed" {
		t.Fatalf("expected latest node evidence, got %+v", governance)
	}
	if governance.LatestNodeVerifierGateStatus != "verified_pass" || governance.LatestNodeVerifierVerdict != "PASS" || governance.LatestNodeRecoveryKind != "failed_node_retry" {
		t.Fatalf("expected latest node verifier fields, got %+v", governance)
	}
	if !governance.RetryCandidateEvidence || governance.RetryCandidateNodeID != "node-build" || governance.RetryCandidateRetryable != "true" || !governance.RetryCandidateContractReady {
		t.Fatalf("expected retry candidate fields, got %+v", governance)
	}
	if governance.PendingApprovalCount != 0 || governance.VerifierPending {
		t.Fatalf("expected no pending approval or verifier blocker, got %+v", governance)
	}
	if !governance.RetryClosureEvidence || !governance.RetryClosureReady || governance.RetryClosureAttemptID != "retry-build" || governance.RetryClosureStatus != "completed" {
		t.Fatalf("expected retry closure fields, got %+v", governance)
	}
	if len(governance.RetryClosureMissingEvidence) != 0 || governance.RetryClosureVerifierGateStatus != "verified_pass" || governance.RetryClosureVerifierVerdict != "PASS" || governance.RetryClosureVerifierReportPath != "verifier/retry-build.json" {
		t.Fatalf("expected retry closure verifier fields, got %+v", governance)
	}
	if governance.NextActionCue != "manual review required before risky continuation" {
		t.Fatalf("expected manual review cue, got %+v", governance)
	}
}

func TestLatestNodeWorkEvidenceChain(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "node_work_evidence", Summary: "Builder replay completed.", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: replay_completed", "replay_status: completed"}},
		{Cause: "node_work_evidence", Summary: "Missing node id.", Details: []string{"node_role: Critic"}},
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"node_id: node-review"}},
		{Cause: "node_work_evidence", Summary: "Researcher read completed.", Details: []string{"node_id: node-survey", "node_role: Researcher", "tool: read", "operation: file_read", "status: completed"}},
		{Cause: "node_work_evidence", Summary: "Synthesizer completed.", Details: []string{"node_id: node-synthesize", "node_role: Synthesizer", "status: completed"}},
	}
	chain := LatestNodeWorkEvidenceChain(records, 2)
	if len(chain) != 2 {
		t.Fatalf("expected two bounded node evidence records, got %+v", chain)
	}
	if EvaluationRecordNodeID(chain[0]) != "node-build" {
		t.Fatalf("expected newest valid node-build first, got %+v", chain)
	}
	if EvaluationRecordNodeID(chain[1]) != "node-survey" {
		t.Fatalf("expected second valid node-survey, got %+v", chain)
	}
	if LatestNodeWorkEvidenceChain(records, 0) != nil {
		t.Fatal("expected nil chain for zero limit")
	}
}

func TestLatestRecoveryEvidenceChain(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "verification_reverify_covered", Summary: "PASS covers remediation.", Details: []string{"remediation_resume_attempt_id: resume-remediate"}},
		{Cause: "node_work_evidence", Summary: "Normal researcher read.", Details: []string{"node_id: node-survey", "node_role: Researcher", "tool: read", "operation: file_read", "status: completed"}},
		{Cause: "node_work_evidence", Summary: "Builder replay completed.", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: replay_completed", "recovery_kind: approval_replay"}},
		{Cause: "verifier_remediation_resume_attempt", Summary: "Remediation attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate"}},
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-failed", "node_id: node-review"}},
		{Cause: "verification_warning", Summary: "Warning only."},
	}
	chain := LatestRecoveryEvidenceChain(records, 3)
	if len(chain) != 3 {
		t.Fatalf("expected three bounded recovery records, got %+v", chain)
	}
	if chain[0].Cause != "verification_reverify_covered" {
		t.Fatalf("expected newest closure first, got %+v", chain)
	}
	if chain[1].Cause != "node_work_evidence" || EvaluationRecordRecoveryKind(chain[1]) != "approval_replay" {
		t.Fatalf("expected recovery-bearing node evidence second, got %+v", chain)
	}
	if chain[2].Cause != "verifier_remediation_resume_attempt" {
		t.Fatalf("expected remediation attempt third, got %+v", chain)
	}
	if LatestRecoveryEvidenceChain(records, 0) != nil {
		t.Fatal("expected nil chain for zero limit")
	}
}

func TestLatestRecoveryDecision(t *testing.T) {
	closed := LatestRecoveryDecision([]EvaluationRecord{
		{Cause: "verification_reverify_covered", Summary: "PASS covers remediation.", Details: []string{"remediation_resume_attempt_id: resume-remediate"}},
		{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true"}},
	}, 5)
	if closed == nil || closed.Category != "closed" || closed.SourceCause != "verification_reverify_covered" {
		t.Fatalf("expected closed recovery decision from reverify closure, got %+v", closed)
	}
	retryable := LatestRecoveryDecision([]EvaluationRecord{
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-failed", "status_reason: tool failed", "retryable: true"}},
	}, 5)
	if retryable == nil || retryable.Category != "retryable" || retryable.PausePointID != "pause-failed" {
		t.Fatalf("expected retryable failed-node decision, got %+v", retryable)
	}
	blocked := LatestRecoveryDecision([]EvaluationRecord{
		{Cause: "resume_policy_decision", Summary: "Retry denied.", Details: []string{"pause_point_id: pause-a", "resume_attempt_id: resume-a", "retryable: false", "allowed: false", "reason: verifier_targets_changed"}},
	}, 5)
	if blocked == nil || blocked.Category != "blocked" || blocked.Reason != "verifier_targets_changed" || blocked.Allowed != "false" {
		t.Fatalf("expected blocked policy decision, got %+v", blocked)
	}
	manual := LatestRecoveryDecision([]EvaluationRecord{
		{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true", "reason: verifier_targets_explicitly_updated"}},
	}, 5)
	if manual == nil || manual.Category != "manual_required" || manual.ResumeAttemptID != "resume-remediate" {
		t.Fatalf("expected manual-required remediation attempt decision, got %+v", manual)
	}
}

func TestLatestRecoveryActionProposal(t *testing.T) {
	if proposal := LatestRecoveryActionProposal([]EvaluationRecord{
		{Cause: "verification_reverify_covered", Summary: "PASS covers remediation.", Details: []string{"remediation_resume_attempt_id: resume-remediate"}},
	}, 5); proposal != nil {
		t.Fatalf("expected no proposal for closed recovery decision, got %+v", proposal)
	}
	retryProposal := LatestRecoveryActionProposal([]EvaluationRecord{
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-failed", "status_reason: tool failed", "retryable: true"}},
	}, 5)
	if retryProposal == nil || retryProposal.ActionKind != "plan_failed_node_retry" || retryProposal.RequiredAuthority != "workflow_scheduler" {
		t.Fatalf("expected failed-node retry proposal, got %+v", retryProposal)
	}
	remediationProposal := LatestRecoveryActionProposal([]EvaluationRecord{
		{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true", "expected_targets: note.txt, process_record.md"}},
	}, 5)
	if remediationProposal == nil || remediationProposal.ActionKind != "run_verifier_remediation_repair" || remediationProposal.RequiredAuthority != "verifier_reverify" {
		t.Fatalf("expected verifier remediation repair proposal, got %+v", remediationProposal)
	}
	if len(remediationProposal.ExpectedTargets) != 2 || remediationProposal.ExpectedTargets[0] != "note.txt" || remediationProposal.ExpectedTargets[1] != "process_record.md" {
		t.Fatalf("expected remediation proposal targets, got %+v", remediationProposal)
	}
}

func TestLatestRecoveryActionRequest(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true", "reason: verifier_targets_explicitly_updated", "expected_targets: note.txt"}},
	}
	request := LatestRecoveryActionRequest(records, 5, "task_operator_command")
	if request == nil {
		t.Fatal("expected recovery action request")
	}
	if request.ActionKind != "run_verifier_remediation_repair" || request.RequiredAuthority != "verifier_reverify" {
		t.Fatalf("expected verifier remediation request shape, got %+v", request)
	}
	if request.OperatorSource != "task_operator_command" || request.PausePointID != "pause-remediate" || request.ResumeAttemptID != "resume-remediate" {
		t.Fatalf("expected operator and recovery coordinates, got %+v", request)
	}
	if len(request.ExpectedTargets) != 1 || request.ExpectedTargets[0] != "note.txt" {
		t.Fatalf("expected request targets, got %+v", request)
	}
	if closed := LatestRecoveryActionRequest([]EvaluationRecord{{Cause: "verification_reverify_covered", Summary: "Closed."}}, 5, "task_operator_command"); closed != nil {
		t.Fatalf("expected no recovery action request for closed decision, got %+v", closed)
	}
}

func TestLatestRecoveryExecutionBoundary(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true", "reason: verifier_targets_explicitly_updated", "expected_targets: note.txt"}},
	}
	boundary := LatestRecoveryExecutionBoundary(records, 5, "task_operator_command")
	if boundary == nil {
		t.Fatal("expected guarded recovery execution boundary")
	}
	if boundary.ActionKind != "run_verifier_remediation_repair" || boundary.RequiredAuthority != "verifier_reverify" {
		t.Fatalf("expected verifier remediation guarded boundary, got %+v", boundary)
	}
	if !boundary.VerifierRequired || boundary.ApprovalRequired || boundary.GuardStatus != "guarded_ready" {
		t.Fatalf("expected guarded verifier-ready boundary, got %+v", boundary)
	}
	if boundary.OperatorSource != "task_operator_command" || boundary.PausePointID != "pause-remediate" || boundary.ResumeAttemptID != "resume-remediate" {
		t.Fatalf("expected boundary coordinates, got %+v", boundary)
	}
	if len(boundary.ExpectedTargets) != 1 || boundary.ExpectedTargets[0] != "note.txt" {
		t.Fatalf("expected boundary targets, got %+v", boundary)
	}

	manual := LatestRecoveryExecutionBoundary([]EvaluationRecord{
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-failed", "status_reason: tool failed", "retryable: true"}},
	}, 5, "task_operator_command")
	if manual == nil {
		t.Fatal("expected manual review boundary for non-verifier recovery request")
	}
	if manual.ActionKind != "plan_failed_node_retry" || manual.GuardStatus != "manual_review_required" || !manual.ApprovalRequired {
		t.Fatalf("expected manual review boundary, got %+v", manual)
	}

	if closed := LatestRecoveryExecutionBoundary([]EvaluationRecord{{Cause: "verification_reverify_covered", Summary: "Closed."}}, 5, "task_operator_command"); closed != nil {
		t.Fatalf("expected no recovery execution boundary for closed decision, got %+v", closed)
	}
	runtimeBoundary := LatestRecoveryExecutionBoundary(records, 5, "runtime_reverify_repair")
	if runtimeBoundary == nil || runtimeBoundary.OperatorSource != "runtime_reverify_repair" || runtimeBoundary.GuardStatus != "guarded_ready" {
		t.Fatalf("expected runtime-scoped guarded boundary, got %+v", runtimeBoundary)
	}
	noPause := LatestRecoveryExecutionBoundary([]EvaluationRecord{
		{Cause: "verification_reverify_fix_attempt", Summary: "Fix attempt.", Details: []string{"expected_targets: note.txt"}},
	}, 5, "runtime_reverify_repair")
	if noPause == nil || noPause.GuardStatus != "manual_review_required" || noPause.VerifierRequired {
		t.Fatalf("expected no-pause verifier request to stay manual, got %+v", noPause)
	}
}

func TestRecoveryAuthorityModelsStaySeparated(t *testing.T) {
	cases := []struct {
		name              string
		records           []EvaluationRecord
		wantAction        string
		wantAuthority     string
		wantGuard         string
		wantVerifierReady bool
		wantApprovalGate  bool
	}{
		{
			name: "approval required stays operator approval",
			records: []EvaluationRecord{
				{Cause: "tool_approval_required", Summary: "Approval required.", Details: []string{"pause_point_id: pause-approval", "approval_key: approval-a", "expected_targets: note.txt"}},
			},
			wantAction:       "request_tool_approval",
			wantAuthority:    "operator_approval",
			wantGuard:        "manual_review_required",
			wantApprovalGate: true,
		},
		{
			name: "approval replay retry stays runtime resume policy",
			records: []EvaluationRecord{
				{Cause: "resume_policy_decision", Summary: "Resume attempt may start.", Details: []string{"pause_point_id: pause-approval", "resume_attempt_id: resume-approval", "retryable: true", "allowed: true", "reason: previous_attempt_failed"}},
			},
			wantAction:       "start_recovery_retry",
			wantAuthority:    "runtime_resume_policy",
			wantGuard:        "manual_review_required",
			wantApprovalGate: true,
		},
		{
			name: "verifier remediation with pause is verifier guarded",
			records: []EvaluationRecord{
				{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true", "reason: verifier_targets_explicitly_updated", "expected_targets: note.txt"}},
			},
			wantAction:        "run_verifier_remediation_repair",
			wantAuthority:     "verifier_reverify",
			wantGuard:         "guarded_ready",
			wantVerifierReady: true,
		},
		{
			name: "failed node retry stays scheduler planning",
			records: []EvaluationRecord{
				{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-failed", "pause_point_kind: workflow_node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-a", "origin_task_id: task-a", "expected_targets: note.txt"}},
			},
			wantAction:       "plan_failed_node_retry",
			wantAuthority:    "workflow_scheduler",
			wantGuard:        "manual_review_required",
			wantApprovalGate: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			boundary := LatestRecoveryExecutionBoundary(tc.records, 5, "task_operator_command")
			if boundary == nil {
				t.Fatal("expected recovery execution boundary metadata")
			}
			if boundary.ActionKind != tc.wantAction || boundary.RequiredAuthority != tc.wantAuthority || boundary.GuardStatus != tc.wantGuard {
				t.Fatalf("expected action=%s authority=%s guard=%s, got %+v", tc.wantAction, tc.wantAuthority, tc.wantGuard, boundary)
			}
			if boundary.VerifierRequired != tc.wantVerifierReady || boundary.ApprovalRequired != tc.wantApprovalGate {
				t.Fatalf("expected verifier=%t approval=%t, got %+v", tc.wantVerifierReady, tc.wantApprovalGate, boundary)
			}
		})
	}
}

func TestLatestRecoveryInspectionSummary(t *testing.T) {
	now := time.Now().UTC()
	current := &VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-patch.json", UpdatedAt: now}
	records := []EvaluationRecord{
		{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "retryable: true", "allowed: true", "reason: verifier_targets_explicitly_updated", "expected_targets: note.txt, process_record.md", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}},
	}
	summary := LatestRecoveryInspectionSummary(current, nil, records, 5, "task_operator_command")
	if summary == nil {
		t.Fatal("expected recovery inspection summary")
	}
	if summary.DecisionCategory != "manual_required" || summary.ActionKind != "run_verifier_remediation_repair" {
		t.Fatalf("expected manual verifier remediation summary, got %+v", summary)
	}
	if summary.GuardStatus != "guarded_ready" || summary.ClosureStatus != "pending_guarded_remediation" {
		t.Fatalf("expected guard and closure status in summary, got %+v", summary)
	}
	if summary.PausePointID != "pause-remediate" || summary.ResumeAttemptID != "resume-remediate" {
		t.Fatalf("expected recovery coordinates in summary, got %+v", summary)
	}
	if summary.RequiredAuthority != "verifier_reverify" || summary.SourceCause != "verifier_remediation_resume_attempt" {
		t.Fatalf("expected authority and source in summary, got %+v", summary)
	}
	if len(summary.ExpectedTargets) != 2 || summary.ExpectedTargets[0] != "note.txt" || summary.ExpectedTargets[1] != "process_record.md" {
		t.Fatalf("expected deduplicated expected targets, got %+v", summary.ExpectedTargets)
	}
	if summary.Guidance != "Guarded remediation is pending verifier closure; keep repair and reverify explicit." {
		t.Fatalf("expected pending guarded remediation guidance, got %+v", summary)
	}

	passCurrent := &VerificationSnapshot{Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: ".avatars/memory/verifier/run-pass.json", UpdatedAt: now.Add(time.Minute)}
	history := []VerificationSnapshot{{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", ReportPath: ".avatars/memory/verifier/run-patch.json", UpdatedAt: now}}
	closedRecords := []EvaluationRecord{
		{Cause: "verification_reverify_covered", Summary: "PASS covers remediation.", ReportPath: ".avatars/memory/verifier/run-pass.json", Details: []string{"remediation_pause_point_id: pause-remediate", "remediation_resume_attempt_id: resume-remediate", "remediation_expected_targets: note.txt"}},
		{Cause: "verifier_remediation_resume_attempt", Summary: "Attempt started.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "expected_targets: note.txt", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify"}},
	}
	closed := LatestRecoveryInspectionSummary(passCurrent, history, closedRecords, 5, "task_operator_command")
	if closed == nil {
		t.Fatal("expected closed recovery inspection summary")
	}
	if closed.DecisionCategory != "closed" || closed.ActionKind != "" || closed.GuardStatus != "" {
		t.Fatalf("expected closed summary without executable boundary, got %+v", closed)
	}
	if closed.RequiredAuthority != "" {
		t.Fatalf("expected closed summary to suppress stale authority, got %+v", closed)
	}
	if closed.ClosureStatus != "pass_closed" || closed.ResumeAttemptID != "resume-remediate" {
		t.Fatalf("expected PASS closure coordinates in summary, got %+v", closed)
	}
	if closed.Guidance != "Verifier PASS closed the remediation chain; keep the closure coordinates for audit." {
		t.Fatalf("expected closed remediation guidance, got %+v", closed)
	}

	boundedHistory := []VerificationSnapshot{{Tool: "verify", Verdict: "PASS", Summary: "Verification finished with PASS.", ReportPath: ".avatars/memory/verifier/run-pass-later.json", UpdatedAt: now.Add(2 * time.Minute)}}
	closedFromRecords := LatestRecoveryInspectionSummary(passCurrent, boundedHistory, closedRecords, 5, "task_operator_command")
	if closedFromRecords == nil {
		t.Fatal("expected closed recovery summary from evaluation records after non-pass verifier falls out of bounded history")
	}
	if closedFromRecords.DecisionCategory != "closed" || closedFromRecords.ClosureStatus != "pass_closed" {
		t.Fatalf("expected records-only PASS closure summary, got %+v", closedFromRecords)
	}
	if closedFromRecords.Reason != "bounded_history_window" {
		t.Fatalf("expected bounded history reason, got %+v", closedFromRecords)
	}
	if closedFromRecords.Guidance != "Verifier PASS closed the remediation chain; keep the closure coordinates for audit." {
		t.Fatalf("expected standard closed remediation guidance for bounded history closure, got %+v", closedFromRecords)
	}
}

func TestLatestRecoveryInspectionSummary_NonVerifierRecoveryDoesNotCarryClosure(t *testing.T) {
	failedRecords := []EvaluationRecord{
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-failed", "pause_point_kind: workflow_node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-a", "origin_task_id: task-a", "expected_targets: note.txt"}},
	}
	failedNode := LatestRecoveryInspectionSummary(nil, nil, []EvaluationRecord{
		{Cause: "failed_node_pause_point", Summary: "Failed node.", Details: []string{"pause_point_id: pause-failed", "pause_point_kind: workflow_node", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-a", "origin_task_id: task-a", "expected_targets: note.txt"}},
	}, 5, "task_operator_command")
	if failedNode == nil {
		t.Fatal("expected failed-node recovery summary")
	}
	if failedNode.DecisionCategory != "retryable" || failedNode.ActionKind != "plan_failed_node_retry" || failedNode.GuardStatus != "manual_review_required" {
		t.Fatalf("expected failed-node retry summary, got %+v", failedNode)
	}
	if failedNode.ClosureStatus != "" || failedNode.RequiredAuthority != "workflow_scheduler" {
		t.Fatalf("expected failed-node summary without verifier closure, got %+v", failedNode)
	}
	if failedNode.PausePointID != "pause-failed" || failedNode.Retryable != "true" || failedNode.Reason != "" {
		t.Fatalf("expected failed-node pause and retry coordinates, got %+v", failedNode)
	}
	if len(failedNode.ExpectedTargets) != 1 || failedNode.ExpectedTargets[0] != "note.txt" {
		t.Fatalf("expected failed-node expected targets, got %+v", failedNode.ExpectedTargets)
	}
	if failedNode.Guidance != "A failed workflow node is retryable; inspect the pause point before planning a bounded retry." {
		t.Fatalf("expected failed-node retry guidance, got %+v", failedNode)
	}
	failedBoundary := LatestRecoveryExecutionBoundary(failedRecords, 5, "task_operator_command")
	if failedBoundary == nil {
		t.Fatal("expected failed-node manual review boundary")
	}
	if failedBoundary.GuardStatus != "manual_review_required" || !failedBoundary.ApprovalRequired || failedBoundary.VerifierRequired {
		t.Fatalf("expected failed-node retry to stay manual-review metadata, got %+v", failedBoundary)
	}
	if failedBoundary.RequiredAuthority != "workflow_scheduler" || failedBoundary.ActionKind != "plan_failed_node_retry" {
		t.Fatalf("expected failed-node retry authority metadata, got %+v", failedBoundary)
	}

	approval := LatestRecoveryInspectionSummary(nil, nil, []EvaluationRecord{
		{Cause: "tool_approval_required", Summary: "Approval required.", Details: []string{"pause_point_id: pause-approval", "approval_key: approval-a", "expected_targets: note.txt"}},
	}, 5, "task_operator_command")
	if approval == nil {
		t.Fatal("expected approval-required recovery summary")
	}
	if approval.DecisionCategory != "manual_required" || approval.ActionKind != "request_tool_approval" || approval.RequiredAuthority != "operator_approval" {
		t.Fatalf("expected approval-required summary, got %+v", approval)
	}
	if approval.ClosureStatus != "" || approval.GuardStatus != "manual_review_required" {
		t.Fatalf("expected approval summary without verifier closure, got %+v", approval)
	}
	if len(approval.ExpectedTargets) != 1 || approval.ExpectedTargets[0] != "note.txt" {
		t.Fatalf("expected approval target in summary, got %+v", approval.ExpectedTargets)
	}
	if approval.Guidance != "Tool approval is required; review the pending approval before replaying guarded work." {
		t.Fatalf("expected approval guidance, got %+v", approval)
	}
}

func TestNodeWorkEvidenceRecoveryCoordinateShapes(t *testing.T) {
	records := []EvaluationRecord{
		{Cause: "node_work_evidence", Summary: "Builder needs verifier remediation.", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: needs_remediation", "origin_run_id: run-c", "origin_task_id: task-c", "verifier_gate_status: blocked_fail", "verifier_verdict: FAIL", "verified: false", "verification_report_path: .avatars/verification/report.json", "recovery_kind: verifier_remediation"}},
		{Cause: "node_work_evidence", Summary: "Builder replay completed.", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: replay_completed", "origin_run_id: run-a", "origin_task_id: task-a", "approval_key: approval-build", "continuation_run_id: run-b", "continuation_task_id: task-a", "replay_status: completed", "pause_point_id: pause-build", "pause_point_kind: workflow_node", "recovery_kind: approval_replay"}},
		{Cause: "node_work_evidence", Summary: "Builder awaits approval.", Details: []string{"node_id: node-build", "node_role: Builder", "tool: write", "operation: file_write", "status: awaiting_approval", "origin_run_id: run-a", "origin_task_id: task-a", "pause_point_id: pause-build", "pause_point_kind: workflow_node", "recovery_kind: approval_required", "verifier_gate_status: pending_approval", "verified: false"}},
		{Cause: "node_work_evidence", Summary: "Researcher read completed.", Details: []string{"node_id: node-survey", "node_role: Researcher", "tool: read", "operation: file_read", "status: completed", "origin_run_id: run-a", "origin_task_id: task-a"}},
	}
	chain := LatestNodeWorkEvidenceChain(records, 4)
	if len(chain) != 4 {
		t.Fatalf("expected four node evidence records, got %+v", chain)
	}
	remediation := chain[0]
	if EvaluationRecordRecoveryKind(remediation) != "verifier_remediation" || EvaluationRecordVerificationReportPath(remediation) == "" {
		t.Fatalf("expected verifier remediation evidence with report path, got %+v", remediation)
	}
	if EvaluationRecordPausePointID(remediation) != "" || EvaluationRecordDetailValue(remediation, "pause_point_kind") != "" {
		t.Fatalf("expected verifier remediation node evidence not to claim pause-point authority, got %+v", remediation)
	}
	replay := chain[1]
	if EvaluationRecordRecoveryKind(replay) != "approval_replay" || EvaluationRecordReplayStatus(replay) != "completed" {
		t.Fatalf("expected approval replay evidence shape, got %+v", replay)
	}
	if EvaluationRecordApprovalKey(replay) != "approval-build" || EvaluationRecordContinuationRunID(replay) != "run-b" || EvaluationRecordContinuationTaskID(replay) != "task-a" {
		t.Fatalf("expected approval replay continuation lineage, got %+v", replay)
	}
	awaiting := chain[2]
	if EvaluationRecordRecoveryKind(awaiting) != "approval_required" || EvaluationRecordPausePointID(awaiting) != "pause-build" || EvaluationRecordDetailValue(awaiting, "pause_point_kind") != "workflow_node" {
		t.Fatalf("expected approval-required workflow-node pause coordinates, got %+v", awaiting)
	}
	if EvaluationRecordApprovalKey(awaiting) != "" || EvaluationRecordReplayStatus(awaiting) != "" {
		t.Fatalf("expected approval-required evidence to avoid replay lineage, got %+v", awaiting)
	}
	normal := chain[3]
	if EvaluationRecordRecoveryKind(normal) != "" || EvaluationRecordVerifierGateStatus(normal) != "" || EvaluationRecordPausePointID(normal) != "" || EvaluationRecordApprovalKey(normal) != "" {
		t.Fatalf("expected normal node evidence to avoid recovery authority fields, got %+v", normal)
	}
}

func TestTaskWorkspaceStatus(t *testing.T) {
	snapshot := Snapshot{
		EvaluationRecords: []EvaluationRecord{{Cause: "tool_approval_required", Summary: "Approval required for key-a.", Details: []string{"approval_key: key-a"}}},
	}
	if got := TaskWorkspaceStatus("stable", snapshot); got != "awaiting_approval" {
		t.Fatalf("expected awaiting_approval status, got %q", got)
	}
	lifecycleSnapshot := Snapshot{
		EvaluationRecords: []EvaluationRecord{{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to continued.", Details: []string{"status: continued", "origin_run_id: run-origin", "origin_task_id: task-1"}}},
	}
	if got := TaskWorkspaceStatus("stable", lifecycleSnapshot); got != "continued" {
		t.Fatalf("expected continued status from run lifecycle update, got %q", got)
	}
	lifecycleWithApproval := Snapshot{
		EvaluationRecords: []EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed", "origin_run_id: run-origin", "origin_task_id: task-1"}},
			{Cause: "tool_approval_required", Summary: "Approval required for key-a.", Details: []string{"approval_key: key-a"}},
		},
	}
	if got := TaskWorkspaceStatus("stable", lifecycleWithApproval); got != "awaiting_approval" {
		t.Fatalf("expected pending approval to override completed lifecycle, got %q", got)
	}
	pendingReverify := Snapshot{
		Verification: &VerificationSnapshot{Tool: "verify", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL."},
		EvaluationRecords: []EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed", "origin_run_id: run-origin", "origin_task_id: task-1"}},
		},
	}
	if got := TaskWorkspaceStatus("stable", pendingReverify); got != "awaiting_verification" {
		t.Fatalf("expected pending reverify to override completed lifecycle, got %q", got)
	}
	incompleteRetryClosure := Snapshot{
		EvaluationRecords: []EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed", "origin_run_id: run-origin", "origin_task_id: task-1"}},
			{Cause: "failed_node_retry_attempt", Summary: "Retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
		},
	}
	if got := TaskWorkspaceStatus("stable", incompleteRetryClosure); got != "awaiting_retry_closure" {
		t.Fatalf("expected incomplete retry closure to override completed lifecycle, got %q", got)
	}
	readyRetryClosure := Snapshot{
		EvaluationRecords: []EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"retry_attempt_id: retry-a", "status: completed", "origin_run_id: run-origin", "origin_task_id: task-1"}},
			{Cause: "failed_node_retry_attempt", Summary: "Retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
			{Cause: "node_work_evidence", Summary: "Retried node work completed.", Details: []string{"retry_attempt_id: retry-a", "node_id: node-build", "status: completed", "verifier_gate_status: verified_pass", "verifier_verdict: PASS"}},
			{Cause: "failed_node_retry_scheduler_resume", Summary: "Scheduler resume completed.", Details: []string{"retry_attempt_id: retry-a", "scheduler_resume_status: completed"}},
		},
	}
	if got := TaskWorkspaceStatus("stable", readyRetryClosure); got != "completed" {
		t.Fatalf("expected ready retry closure to allow lifecycle status, got %q", got)
	}
	if got := TaskWorkspaceStatus("new", Snapshot{}); got != "new" {
		t.Fatalf("expected new status without pending approvals, got %q", got)
	}
	if got := TaskWorkspaceStatus("", Snapshot{}); got != "stable" {
		t.Fatalf("expected stable default status, got %q", got)
	}
}

func TestTaskFinalityReadinessForTask(t *testing.T) {
	pendingApproval := Snapshot{
		EvaluationRecords: []EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed"}},
			{Cause: "tool_approval_required", Summary: "Approval required for key-a.", Details: []string{"approval_key: key-a"}},
		},
	}
	approvalReadiness := TaskFinalityReadinessForTask("stable", pendingApproval)
	if approvalReadiness.TaskStatus != "awaiting_approval" || approvalReadiness.RuntimeTruthReadiness != "review" || !approvalReadiness.NeedsReview || approvalReadiness.BlockerCount != 1 {
		t.Fatalf("expected pending approval finality readiness, got %+v", approvalReadiness)
	}

	pendingVerifier := Snapshot{Verification: &VerificationSnapshot{Tool: "verify", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL."}}
	verifierReadiness := TaskFinalityReadinessForTask("stable", pendingVerifier)
	if verifierReadiness.TaskStatus != "awaiting_verification" || verifierReadiness.RuntimeTruthReadiness != "review" || !verifierReadiness.NeedsReview || verifierReadiness.BlockerCount != 0 {
		t.Fatalf("expected verifier finality readiness without double-counting verifier pending, got %+v", verifierReadiness)
	}

	incompleteRetryClosure := Snapshot{
		EvaluationRecords: []EvaluationRecord{
			{Cause: "run_lifecycle_updated", Summary: "Run lifecycle updated to completed.", Details: []string{"status: completed"}},
			{Cause: "failed_node_retry_attempt", Summary: "Retry attempt completed.", Details: []string{"retry_attempt_id: retry-a", "retry_attempt_status: completed", "retry_attempt_verifier_gate_expectation: verify mutating targets before closure"}},
		},
	}
	retryReadiness := TaskFinalityReadinessForTask("stable", incompleteRetryClosure)
	if retryReadiness.TaskStatus != "awaiting_retry_closure" || retryReadiness.RuntimeTruthReadiness != "review" || !retryReadiness.NeedsReview || retryReadiness.BlockerCount != 1 {
		t.Fatalf("expected retry closure finality readiness, got %+v", retryReadiness)
	}
}

func TestSuggestedTaskCommands_KeyedApprovalWhenMultiplePending(t *testing.T) {
	snapshot := Snapshot{
		EvaluationRecords: []EvaluationRecord{
			{Cause: "tool_approval_required", Summary: "Approval required for key-b.", Details: []string{"approval_key: key-b", "operation: file_patch", "approval_request_artifact: approvals/key-b.json"}},
			{Cause: "tool_approval_required", Summary: "Approval required for key-a.", Details: []string{"approval_key: key-a", "operation: file_write"}},
		},
	}
	commands := SuggestedTaskCommands("demo-task", snapshot)
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "avatars tasks approve demo-task --approval-key key-b --replay") {
		t.Fatalf("expected keyed replay command for latest pending approval, got %+v", commands)
	}
	if !strings.Contains(joined, "avatars tasks approve demo-task --approval-key key-b") {
		t.Fatalf("expected keyed approve command for latest pending approval, got %+v", commands)
	}
	if !strings.Contains(joined, "avatars tasks deny demo-task --approval-key key-b") {
		t.Fatalf("expected keyed deny command for latest pending approval, got %+v", commands)
	}
	if strings.Contains(joined, "avatars tasks approve demo-task\navatars tasks deny demo-task") {
		t.Fatalf("expected keyed commands instead of ambiguous default commands, got %+v", commands)
	}
}

func TestPromptVerificationContext(t *testing.T) {
	now := time.Date(2026, time.May, 5, 3, 14, 49, 0, time.UTC)
	snapshot := Snapshot{
		Verification: &VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", UpdatedAt: now},
		EvaluationRecords: []EvaluationRecord{
			{Cause: "verifier_remediation_resume_attempt", Summary: "Verifier remediation resume attempt recorded for reverify repair work.", Details: []string{"pause_point_id: pause-remediate", "resume_attempt_id: resume-remediate", "expected_targets: note.txt, process_record.md", "recovery_boundary_guard_status: guarded_ready", "recovery_boundary_required_authority: verifier_reverify", "recovery_boundary_source: verifier_remediation_resume_attempt"}, UpdatedAt: now.Add(2 * time.Second)},
			{RunID: "run-failed", TaskID: "demo-task", Cause: "failed_node_pause_point", Summary: "Failed workflow node recorded as recovery coordinate.", Details: []string{"pause_point_id: pause-build", "pause_point_kind: workflow_node", "pause_point_digest: digest-build", "node_id: node-build", "status_reason: tool failed", "retryable: true", "origin_run_id: run-failed", "origin_task_id: demo-task", "expected_targets: note.txt"}, UpdatedAt: now.Add(1500 * time.Millisecond)},
			{Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed to address the failing verifier warning while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Details: []string{"proposal_id: remediate-latest-non-pass", "expected_targets: note.txt, process_record.md"}, UpdatedAt: now.Add(time.Second)},
		},
	}
	context := PromptVerificationContext("demo-task", snapshot)
	if !strings.Contains(context, "current_verification: PARTIAL via patch") {
		t.Fatalf("expected current verification context, got %q", context)
	}
	if !strings.Contains(context, "latest_non_pass_follow_up: avatars verify --task demo-task") {
		t.Fatalf("expected task-scoped follow-up, got %q", context)
	}
	if !strings.Contains(context, "reverify_status: pending reverify for latest non-pass verification") {
		t.Fatalf("expected reverify status, got %q", context)
	}
	if !strings.Contains(context, "latest_reverify_attempt_targets: note.txt, process_record.md") {
		t.Fatalf("expected reverify targets, got %q", context)
	}
	if !strings.Contains(context, "latest_reverify_attempt_proposal_id: remediate-latest-non-pass") {
		t.Fatalf("expected reverify proposal id, got %q", context)
	}
	if !strings.Contains(context, "latest_guarded_remediation_guard_status: guarded_ready") {
		t.Fatalf("expected guarded remediation guard status, got %q", context)
	}
	if !strings.Contains(context, "latest_guarded_remediation_authority: verifier_reverify") {
		t.Fatalf("expected guarded remediation authority, got %q", context)
	}
	if !strings.Contains(context, "verifier_remediation_closure_status: pending_guarded_remediation") {
		t.Fatalf("expected pending guarded remediation closure status, got %q", context)
	}
	if !strings.Contains(context, "verifier_remediation_closure_resume_attempt: resume-remediate") {
		t.Fatalf("expected guarded remediation closure coordinates, got %q", context)
	}
	if !strings.Contains(context, "recovery_summary_category: manual_required") {
		t.Fatalf("expected recovery summary category in prompt context, got %q", context)
	}
	if !strings.Contains(context, "recovery_summary_guard_status: guarded_ready") {
		t.Fatalf("expected recovery summary guard status in prompt context, got %q", context)
	}
	if !strings.Contains(context, "recovery_summary_closure_status: pending_guarded_remediation") {
		t.Fatalf("expected recovery summary closure status in prompt context, got %q", context)
	}
	if !strings.Contains(context, "recovery_summary_guidance: Guarded remediation is pending verifier closure; keep repair and reverify explicit.") {
		t.Fatalf("expected recovery summary guidance in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_pause_point: pause-build") {
		t.Fatalf("expected failed-node retry pause point in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_pause_point_digest: digest-build") {
		t.Fatalf("expected failed-node retry pause digest in prompt context, got %q", context)
	}
	if !strings.Contains(context, "failed_node_retry_contract_ready: true") {
		t.Fatalf("expected failed-node retry contract readiness in prompt context, got %q", context)
	}
}

func TestSnapshotRemediationProposals(t *testing.T) {
	now := time.Date(2026, time.May, 5, 3, 14, 49, 0, time.UTC)
	snapshot := Snapshot{
		Verification:      &VerificationSnapshot{Tool: "patch", Verdict: "PARTIAL", Summary: "Verification finished with PARTIAL.", UpdatedAt: now},
		EvaluationRecords: []EvaluationRecord{{Cause: "verification_reverify_fix_attempt", Summary: "Tool write/file_write completed to address the failing verifier warning while the latest non-pass verification still awaits reverify: PARTIAL via patch.", Details: []string{"proposal_id: remediate-latest-non-pass", "expected_targets: note.txt, process_record.md"}, UpdatedAt: now.Add(time.Second)}},
	}

	proposals := SnapshotRemediationProposals("demo-task", snapshot)
	if len(proposals) != 2 {
		t.Fatalf("expected 2 remediation proposals, got %+v", proposals)
	}
	if proposals[0].ProposalID != "inspect-latest-non-pass" || proposals[0].Kind != "inspect_verification" {
		t.Fatalf("unexpected first remediation proposal: %+v", proposals[0])
	}
	if proposals[0].FollowUpCommand != "avatars verify --task demo-task" {
		t.Fatalf("expected inspect proposal follow-up, got %+v", proposals[0])
	}
	if proposals[1].ProposalID != "remediate-latest-non-pass" || proposals[1].Kind != "remediation_attempt" {
		t.Fatalf("unexpected second remediation proposal: %+v", proposals[1])
	}
	if len(proposals[1].ExpectedTargets) != 2 || proposals[1].ExpectedTargets[0] != "note.txt" || proposals[1].ExpectedTargets[1] != "process_record.md" {
		t.Fatalf("expected remediation targets to flow through proposal surface, got %+v", proposals[1])
	}
	if proposals[1].FollowUpCommand != "avatars verify --task demo-task" {
		t.Fatalf("expected remediation proposal follow-up, got %+v", proposals[1])
	}

	view := snapshot.View(QueryPolicy{Reader: ReaderVerifier})
	viewProposals := QueryRemediationProposals("demo-task", view)
	if len(viewProposals) != 2 {
		t.Fatalf("expected verifier query remediation proposals, got %+v", viewProposals)
	}
}

func TestSnapshotView_AvatarPolicy(t *testing.T) {
	snapshot := Snapshot{
		StableSummary:   "Stable summary",
		TaskSummary:     "Task summary",
		AvatarSummaries: map[string]string{"avatar-builder": "Builder summary"},
		RecentEvents:    []string{"Event A"},
	}

	view, err := ParseQueryPolicy("avatar:avatar-builder")
	if err != nil {
		t.Fatalf("parse query policy failed: %v", err)
	}
	avatarView := snapshot.View(view)
	if avatarView.AvatarSummary != "Builder summary" {
		t.Fatalf("expected avatar summary, got %q", avatarView.AvatarSummary)
	}
	if avatarView.TaskSummary != "Task summary" {
		t.Fatalf("expected avatar task summary, got %q", avatarView.TaskSummary)
	}
	if avatarView.StableSummary != "" {
		t.Fatalf("expected avatar view to omit stable summary, got %q", avatarView.StableSummary)
	}
	if _, err := ParseQueryPolicy("avatar:"); err == nil {
		t.Fatal("expected avatar policy without id to fail")
	}
}

func TestWorkflowExecutionChain_FiltersAndOrdersPlanningEvents(t *testing.T) {
	recentEvents := []string{
		"Tool completed: read process_record.md.",
		"Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context.",
		"Researcher assigned: Survey current repository context",
		"Workflow node created for Researcher: Survey current repository context",
		"Planner decomposed the task: Deterministic planning chain.",
	}

	chain := WorkflowExecutionChain(recentEvents)
	if len(chain) != 4 {
		t.Fatalf("expected 4 execution chain entries, got %d", len(chain))
	}
	if chain[0] != "Planner decomposed the task: Deterministic planning chain." {
		t.Fatalf("expected chronological chain start, got %q", chain[0])
	}
	if chain[3] != "Planner handed off Decompose task and set checkpoints to Researcher for Survey current repository context." {
		t.Fatalf("expected handoff at chain tail, got %q", chain[3])
	}
}

func TestLatestAvatarMessages_SelectMostRecentTypedEntries(t *testing.T) {
	recentEvents := []string{
		"Synthesizer summary: [broadcast task] completed planning run across 5 avatars and 5 workflow nodes.",
		"Critic challenge: [broadcast task] approval remains required for generated skills, and concurrent execution is still deferred.",
		"Planner follow-up: [to Researcher] repository context is sufficient for the current builder implementation slice; proceed with the bounded skill generation path.",
		"Researcher ask: [to Planner] confirm whether the current repository context is sufficient before the builder locks the next implementation slice.",
		"Builder report: [broadcast task] generated candidate skill at skills/generated/task-survey-skill.md.",
		"Researcher report: [broadcast task] Read process_record.md successfully.",
	}

	if report := LatestAvatarReport(recentEvents); report != "Builder report: [broadcast task] generated candidate skill at skills/generated/task-survey-skill.md." {
		t.Fatalf("unexpected latest report: %q", report)
	}
	if route := LatestAvatarReportRouteCue(recentEvents); route != "broadcast task" {
		t.Fatalf("unexpected latest report route: %q", route)
	}
	if ask := LatestAvatarAsk(recentEvents); ask != "Researcher ask: [to Planner] confirm whether the current repository context is sufficient before the builder locks the next implementation slice." {
		t.Fatalf("unexpected latest ask: %q", ask)
	}
	if route := LatestAvatarAskRouteCue(recentEvents); route != "to Planner" {
		t.Fatalf("unexpected latest ask route: %q", route)
	}
	if status := LatestAvatarAskFollowUpStatus(recentEvents); status != "followed-up via to Researcher" {
		t.Fatalf("unexpected latest ask status: %q", status)
	}
	if followUp := LatestAvatarFollowUp(recentEvents); followUp != "Planner follow-up: [to Researcher] repository context is sufficient for the current builder implementation slice; proceed with the bounded skill generation path." {
		t.Fatalf("unexpected latest follow-up: %q", followUp)
	}
	if route := LatestAvatarFollowUpRouteCue(recentEvents); route != "to Researcher" {
		t.Fatalf("unexpected latest follow-up route: %q", route)
	}
	if challenge := LatestAvatarChallenge(recentEvents); challenge != "Critic challenge: [broadcast task] approval remains required for generated skills, and concurrent execution is still deferred." {
		t.Fatalf("unexpected latest challenge: %q", challenge)
	}
	if route := LatestAvatarChallengeRouteCue(recentEvents); route != "broadcast task" {
		t.Fatalf("unexpected latest challenge route: %q", route)
	}
	if summary := LatestAvatarSummary(recentEvents); summary != "Synthesizer summary: [broadcast task] completed planning run across 5 avatars and 5 workflow nodes." {
		t.Fatalf("unexpected latest summary: %q", summary)
	}
	if route := LatestAvatarSummaryRouteCue(recentEvents); route != "broadcast task" {
		t.Fatalf("unexpected latest summary route: %q", route)
	}
}

func TestLatestAvatarAskFollowUpStatus_PendingWhenAskIsNewer(t *testing.T) {
	recentEvents := []string{
		"Researcher ask: [to Planner] confirm whether the current repository context is sufficient before the builder locks the next implementation slice.",
		"Planner follow-up: [to Researcher] repository context is sufficient for the current builder implementation slice; proceed with the bounded skill generation path.",
	}

	if status := LatestAvatarAskFollowUpStatus(recentEvents); status != "pending via to Planner" {
		t.Fatalf("unexpected pending ask status: %q", status)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
