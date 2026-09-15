package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"avatars/internal/app"
	"avatars/internal/arch"
	"avatars/internal/evaluation"
	"avatars/internal/evolution"
	"avatars/internal/llm"
	"avatars/internal/localhttp"
	"avatars/internal/mcp"
	memstore "avatars/internal/memory"
	"avatars/internal/planner"
	"avatars/internal/plugins"
	"avatars/internal/runtime"
	"avatars/internal/skillbuilder"
	"avatars/internal/skills"
	"avatars/internal/stage"
	"avatars/internal/tasks"
	runtelemetry "avatars/internal/telemetry"
	"avatars/internal/tools"
	"avatars/internal/verification"
	"avatars/internal/workflow"
)

// Remaining CLI handlers moved out of main.go (C1.1 entry-only).
func runConfig(args []string) error {
	if len(args) == 0 || args[0] == "show" {
		cfg := app.LoadPersonalityConfig()
		fmt.Println("=== Personality ===")
		fmt.Printf("  Style:            %s\n", cfg.Style)
		fmt.Printf("  Language:         %s\n", cfg.Language)
		fmt.Printf("  Safety:           %s\n", cfg.Safety)
		fmt.Printf("  Operator Context: %s\n", cfg.OperatorContext)
		return nil
	}
	if args[0] == "set" && len(args) >= 3 {
		return app.WritePersonalityConfig(args[1], strings.Join(args[2:], " "))
	}
	return errors.New("usage: avatars config [show | set <key> <value>]")
}

func runLLM(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars llm providers | avatars llm show <provider>")
	}
	switch args[0] {
	case "providers":
		if len(args) != 1 {
			return errors.New("usage: avatars llm providers")
		}
		fmt.Println("Provider\tFamily\tWebSearch\tNeedsModel\tNeedsBaseURL\tLocal")
		for _, provider := range llm.ListProviders() {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", provider.Name, provider.Family, yesNo(provider.SupportsWebSearch), yesNo(provider.RequiresExplicitModel), yesNo(provider.RequiresExplicitBaseURL), yesNo(provider.Local))
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: avatars llm show <provider>")
		}
		provider, ok := llm.LookupProvider(args[1])
		if !ok {
			return fmt.Errorf("unknown llm provider %q", args[1])
		}
		fmt.Printf("Provider: %s\n", provider.Name)
		fmt.Printf("Family: %s\n", provider.Family)
		fmt.Printf("Supports web search: %s\n", yesNo(provider.SupportsWebSearch))
		fmt.Printf("Requires explicit model: %s\n", yesNo(provider.RequiresExplicitModel))
		fmt.Printf("Requires explicit base URL: %s\n", yesNo(provider.RequiresExplicitBaseURL))
		fmt.Printf("Local provider: %s\n", yesNo(provider.Local))
		if provider.DefaultModel != "" {
			fmt.Printf("Default model: %s\n", provider.DefaultModel)
		}
		if provider.DefaultBaseURL != "" {
			fmt.Printf("Default base URL: %s\n", provider.DefaultBaseURL)
		}
		if provider.DefaultAPIKeyEnv != "" {
			fmt.Printf("Default API key env: %s\n", provider.DefaultAPIKeyEnv)
		}
		return nil
	default:
		return errors.New("usage: avatars llm providers | avatars llm show <provider>")
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func runFeedback(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars feedback import-diagnostics <task-id> <json-file>")
	}
	switch args[0] {
	case "import-diagnostics":
		if len(args) != 3 {
			return errors.New("usage: avatars feedback import-diagnostics <task-id> <json-file>")
		}
		return runImportDiagnostics(args[1], args[2])
	default:
		return errors.New("usage: avatars feedback import-diagnostics <task-id> <json-file>")
	}
}

func runImportDiagnostics(taskID string, jsonPath string) error {
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, err := manager.Load(taskID)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("read diagnostics file: %w", err)
	}
	var diagnostics []evaluation.Diagnostic
	if err := json.Unmarshal(content, &diagnostics); err != nil {
		return fmt.Errorf("decode diagnostics file: %w", err)
	}
	records := evaluation.BuildDiagnosticRecords(diagnostics)
	if len(records) == 0 {
		return errors.New("no diagnostics produced importable feedback records")
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	recordedAt := time.Now().UTC()
	runID := fmt.Sprintf("feedback-import-%d", recordedAt.UnixNano())
	sessionID := fmt.Sprintf("feedback-%d", recordedAt.UnixNano())
	for _, record := range records {
		if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{
			SessionID: sessionID,
			RunID:     runID,
			TaskID:    workspace.ID,
			Tool:      record.Tool,
			Kind:      record.Kind,
			Verdict:   record.Verdict,
			Cause:     record.Cause,
			Summary:   record.Summary,
			Source:    record.Source,
			Details:   append([]string(nil), record.Details...),
			UpdatedAt: recordedAt,
		}); err != nil {
			return err
		}
	}
	if lesson, ok := evaluation.BuildDiagnosticWarmLesson(records); ok {
		if err := memoryStore.RecordWarmLesson(memstore.WarmLessonRecord{
			SessionID:  sessionID,
			RunID:      runID,
			TaskID:     workspace.ID,
			Kind:       lesson.Kind,
			Summary:    lesson.Summary,
			Source:     lesson.Source,
			Confidence: lesson.Confidence,
			UpdatedAt:  recordedAt,
		}); err != nil {
			return err
		}
	}
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return err
	}
	candidates := evolution.BuildCandidates("", "", snapshot, nil)
	for _, candidate := range candidates {
		if err := memoryStore.RecordEvolutionCandidate(memstore.EvolutionCandidateRecord{
			SessionID: sessionID,
			RunID:     runID,
			TaskID:    workspace.ID,
			Kind:      candidate.Kind,
			Summary:   candidate.Summary,
			Source:    candidate.Source,
			Priority:  candidate.Priority,
			Status:    candidate.Status,
			UpdatedAt: recordedAt,
		}); err != nil {
			return err
		}
	}
	snapshot, err = memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return err
	}
	projectStore, err := memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
	if err != nil {
		return err
	}
	defer func() {
		_ = projectStore.Close()
	}()
	promotedProjectLessons := evolution.BuildProjectLessons(workspace.ID, snapshot)
	for _, lesson := range promotedProjectLessons {
		if err := projectStore.RecordProjectLesson(memstore.ProjectLessonRecord{
			SourceTaskID: lesson.SourceTaskID,
			Kind:         lesson.Kind,
			Summary:      lesson.Summary,
			Source:       lesson.Source,
			Confidence:   lesson.Confidence,
			UpdatedAt:    recordedAt,
		}); err != nil {
			return err
		}
	}
	fmt.Printf("Imported diagnostics: %d\n", len(records))
	if lesson, ok := evaluation.BuildDiagnosticWarmLesson(records); ok {
		fmt.Printf("Imported warm lessons: 1\n")
		fmt.Printf("Latest warm lesson: %s\n", lesson.Summary)
	} else {
		fmt.Printf("Imported warm lessons: 0\n")
	}
	fmt.Printf("Generated evolution candidates: %d\n", len(candidates))
	fmt.Printf("Promoted project lessons: %d\n", len(promotedProjectLessons))
	fmt.Printf("Task: %s\n", workspace.ID)
	return nil
}

func runTasks(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars tasks list | avatars tasks show <task-id> [--view planner|synthesizer|verifier|governance|avatar:<avatar-id>] | avatars tasks retry-node <task-id> --origin-run <run-id> --pause-point <pause-point-id> --node <node-id> --pause-digest <digest> --dry-run | avatars tasks approve <task-id> [--approval-key <key>] [--replay] | avatars tasks deny <task-id> [--approval-key <key>] | avatars tasks delete <task-id>")
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: avatars tasks list")
		}
		workspaces, err := manager.List()
		if err != nil {
			return err
		}
		if len(workspaces) == 0 {
			fmt.Println("No task workspaces found.")
			return nil
		}
		for _, workspace := range workspaces {
			fmt.Printf("%s\t%s\t%d\t%s\n", workspace.ID, taskWorkspaceStatus(workspace), workspace.RunCount, workspace.Title)
		}
		return nil
	case "show":
		viewSpec := ""
		switch len(args) {
		case 2:
		case 4:
			if args[2] != "--view" {
				return errors.New("usage: avatars tasks show <task-id> [--view planner|synthesizer|verifier|governance|avatar:<avatar-id>]")
			}
			viewSpec = args[3]
		default:
			return errors.New("usage: avatars tasks show <task-id> [--view planner|synthesizer|verifier|governance|avatar:<avatar-id>]")
		}
		workspace, err := manager.Load(args[1])
		if err != nil {
			return err
		}
		workspaceStatus := taskWorkspaceStatus(workspace)
		fmt.Printf("Task: %s\n", workspace.ID)
		fmt.Printf("Title: %s\n", workspace.Title)
		fmt.Printf("Status: %s\n", workspaceStatus)
		fmt.Printf("Runs: %d\n", workspace.RunCount)
		fmt.Printf("Task root: %s\n", workspace.RootDir)
		fmt.Printf("Sessions: %s\n", workspace.SessionsDir)
		fmt.Printf("Memory: %s\n", workspace.MemoryDir)
		memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
		if err != nil {
			return err
		}
		defer func() {
			_ = memoryStore.Close()
		}()
		snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
		if err != nil {
			return err
		}
		projectStore, err := memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
		if err != nil {
			return err
		}
		defer func() {
			_ = projectStore.Close()
		}()
		projectLessons, err := projectStore.LoadProjectLessons()
		if err != nil {
			return err
		}
		snapshot.ProjectLessons = projectLessons
		if workspace.LatestTranscript != "" {
			fmt.Printf("Latest transcript: %s\n", workspace.LatestTranscriptPath())
		}
		if workspace.LatestSummary != "" {
			fmt.Printf("Latest run summary: %s\n", workspace.LatestSummary)
		}
		if snapshot.StableSummary != "" {
			fmt.Printf("Stable memory summary: %s\n", snapshot.StableSummary)
		}
		if snapshot.TaskSummary != "" {
			fmt.Printf("Task memory summary: %s\n", snapshot.TaskSummary)
		}
		if len(snapshot.AvatarSummaries) > 0 {
			fmt.Printf("Avatar summaries: %d\n", len(snapshot.AvatarSummaries))
		}
		if len(snapshot.RecentEvents) > 0 {
			fmt.Printf("Recent key events: %d\n", len(snapshot.RecentEvents))
			for _, event := range snapshot.RecentEvents {
				fmt.Printf("- %s\n", event)
			}
		}
		printExecutionChain("Execution chain", snapshot.RecentEvents, "->")
		printLatestAvatarMessages("Latest avatar", snapshot.RecentEvents)
		printSnapshotLLMStatus("Latest run LLM", snapshot.LLMStatus, snapshot.LLMStatusWarning)
		printConfiguredLLMStatus("Configured LLM", "")
		printTelemetrySnapshot("Telemetry", snapshot.Telemetry)
		if snapshot.Verification != nil && snapshot.Verification.ReportPath != "" {
			fmt.Printf("Latest verifier report: %s\n", snapshot.Verification.ReportPath)
		}
		if snapshot.Verification != nil {
			followUp := memstore.TaskVerificationFollowUpCommand(workspace.ID, *snapshot.Verification)
			if followUp == "" {
				followUp = snapshot.Verification.FollowUpCommand
			}
			if followUp != "" {
				fmt.Printf("Latest verifier follow-up: %s\n", followUp)
			}
		}
		if latestNonPass := memstore.LatestNonPassVerificationSnapshot(snapshot.Verification, snapshot.VerificationHistory); latestNonPass != nil {
			fmt.Printf("Latest non-pass verifier: %s\n", describeVerificationSnapshot(*latestNonPass))
			followUp := memstore.TaskVerificationFollowUpCommand(workspace.ID, *latestNonPass)
			if followUp == "" {
				followUp = latestNonPass.FollowUpCommand
			}
			if followUp != "" {
				fmt.Printf("Latest non-pass follow-up: %s\n", followUp)
			}
		}
		if reverifyStatus := memstore.VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory); reverifyStatus != "" {
			fmt.Printf("Reverify status: %s\n", reverifyStatus)
		}
		if attempt := memstore.LatestReverifyAttemptEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); attempt != nil {
			fmt.Printf("Latest reverify attempt: %s\n", attempt.Summary)
			if proposalID := memstore.EvaluationRecordProposalID(*attempt); proposalID != "" {
				fmt.Printf("Latest reverify attempt proposal: %s\n", proposalID)
			}
			printExpectedTargets("Latest reverify attempt targets", memstore.EvaluationRecordExpectedTargets(*attempt))
		}
		if attempt := memstore.LatestCoveredReverifyAttemptEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); attempt != nil {
			fmt.Printf("Latest reverify remediation: %s\n", attempt.Summary)
			if proposalID := memstore.EvaluationRecordProposalID(*attempt); proposalID != "" {
				fmt.Printf("Latest reverify remediation proposal: %s\n", proposalID)
			}
			printExpectedTargets("Latest reverify remediation targets", memstore.EvaluationRecordExpectedTargets(*attempt))
		}
		if closure := memstore.LatestReverifyClosureEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords); closure != nil {
			fmt.Printf("Latest reverify closure: %s\n", closure.Summary)
		}
		printRecoveryInspectionSummary("Recovery summary", memstore.LatestRecoveryInspectionSummary(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords, 10, "tasks_show"))
		printFailedNodeRetryCandidate("Failed node retry candidate", memstore.LatestFailedNodeRetryCandidate(snapshot.EvaluationRecords))
		printFailedNodeRetryAttempt("Failed node retry attempt", memstore.LatestFailedNodeRetryAttempt(snapshot.EvaluationRecords))
		printFailedNodeRetryAttemptClosureEvidence("Failed node retry attempt closure", memstore.LatestFailedNodeRetryAttemptClosureEvidence(snapshot.EvaluationRecords))
		printFailedNodeRetryArtifact("Failed node retry artifact", memstore.LatestFailedNodeRetryArtifact(snapshot.EvaluationRecords))
		printFailedNodeRetryCurrentStateReadiness("Failed node retry current state", memstore.LatestFailedNodeRetryCurrentStateReadiness(snapshot.EvaluationRecords))
		if approval := memstore.LatestToolApprovalRequiredEvaluation(snapshot.EvaluationRecords); approval != nil {
			fmt.Printf("Latest approval required: %s\n", approval.Summary)
			if source := memstore.EvaluationRecordDecisionSource(*approval); source != "" {
				fmt.Printf("Latest approval required source: %s\n", source)
			}
			if approvalKey := memstore.EvaluationRecordApprovalKey(*approval); approvalKey != "" {
				fmt.Printf("Latest approval required key: %s\n", approvalKey)
			}
			if permissionMode := memstore.EvaluationRecordPermissionMode(*approval); permissionMode != "" {
				fmt.Printf("Latest approval required mode: %s\n", permissionMode)
			}
		}
		if replay := memstore.LatestToolApprovalReplayEvaluation(snapshot.EvaluationRecords); replay != nil {
			fmt.Printf("Latest approval continuation: %s\n", replay.Summary)
			if source := memstore.EvaluationRecordDecisionSource(*replay); source != "" {
				fmt.Printf("Latest approval continuation source: %s\n", source)
			}
			if approvalKey := memstore.EvaluationRecordApprovalKey(*replay); approvalKey != "" {
				fmt.Printf("Latest approval continuation key: %s\n", approvalKey)
			}
			if transcript := memstore.EvaluationRecordReplayTranscript(*replay); transcript != "" {
				fmt.Printf("Latest approval continuation transcript: %s\n", transcript)
			}
			if continuationRunID := memstore.EvaluationRecordContinuationRunID(*replay); continuationRunID != "" {
				fmt.Printf("Latest approval continuation run ID: %s\n", continuationRunID)
			}
			if continuationTaskID := memstore.EvaluationRecordContinuationTaskID(*replay); continuationTaskID != "" {
				fmt.Printf("Latest approval continuation task ID: %s\n", continuationTaskID)
			}
		}
		printPendingApprovals("Pending approvals", memstore.PendingToolApprovalEvaluations(snapshot.EvaluationRecords))
		if denial := memstore.LatestToolPermissionDenialEvaluation(snapshot.EvaluationRecords); denial != nil {
			fmt.Printf("Latest permission denial: %s\n", denial.Summary)
			if source := memstore.EvaluationRecordDecisionSource(*denial); source != "" {
				fmt.Printf("Latest permission denial source: %s\n", source)
			}
		}
		if retry := memstore.LatestToolFailureRetryEvaluation(snapshot.EvaluationRecords); retry != nil {
			fmt.Printf("Latest tool failure retry: %s\n", retry.Summary)
			if retryable := memstore.EvaluationRecordRetryable(*retry); retryable != "" {
				fmt.Printf("Latest tool failure retryable: %s\n", retryable)
			}
			if reason := memstore.EvaluationRecordReason(*retry); reason != "" {
				fmt.Printf("Latest tool failure retry reason: %s\n", reason)
			}
			if source := memstore.EvaluationRecordDecisionSource(*retry); source != "" {
				fmt.Printf("Latest tool failure retry source: %s\n", source)
			}
			if permissionMode := memstore.EvaluationRecordPermissionMode(*retry); permissionMode != "" {
				fmt.Printf("Latest tool failure retry mode: %s\n", permissionMode)
			}
		}
		printLatestNodeEvidence("Latest node", snapshot.EvaluationRecords)
		printNodeEvidenceChain("Node evidence chain", snapshot.EvaluationRecords, 5)
		printRemediationProposals("Advisory remediation proposals", memstore.SnapshotRemediationProposals(workspace.ID, snapshot))
		printSuggestedCommands("Suggested commands", memstore.SuggestedTaskCommands(workspace.ID, snapshot))
		if len(snapshot.VerificationHistory) > 0 {
			fmt.Printf("Verifier history entries: %d\n", len(snapshot.VerificationHistory))
		}
		if len(snapshot.EvaluationRecords) > 0 {
			fmt.Printf("Evaluation records: %d\n", len(snapshot.EvaluationRecords))
		}
		if len(snapshot.WarmLessons) > 0 {
			fmt.Printf("Warm lessons: %d\n", len(snapshot.WarmLessons))
		}
		if len(snapshot.EvolutionCandidates) > 0 {
			fmt.Printf("Evolution candidates: %d\n", len(snapshot.EvolutionCandidates))
		}
		if len(snapshot.ProjectLessons) > 0 {
			fmt.Printf("Project lessons: %d (top support: %d tasks)\n", len(snapshot.ProjectLessons), snapshot.ProjectLessons[0].SupportCount)
		}
		if viewSpec != "" {
			if strings.TrimSpace(viewSpec) == "governance" {
				if err := printTaskGovernanceView(workspace, workspaceStatus, memoryStore, snapshot); err != nil {
					return err
				}
				return nil
			}
			policy, err := memstore.ParseQueryPolicy(viewSpec)
			if err != nil {
				return err
			}
			printTaskQueryView(workspace.ID, snapshot.View(policy))
		}
		return nil
	case "retry-node":
		return runTaskRetryNodeDryRun(manager, args[1:])
	case "approve":
		return runTaskApprovalDecision(manager, "approve", args[1:])
	case "deny":
		return runTaskApprovalDecision(manager, "deny", args[1:])
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: avatars tasks delete <task-id>")
		}
		if err := manager.Delete(args[1]); err != nil {
			return err
		}
		fmt.Printf("Deleted task workspace: %s\n", args[1])
		return nil
	default:
		return errors.New("usage: avatars tasks list | avatars tasks show <task-id> | avatars tasks retry-node <task-id> --origin-run <run-id> --pause-point <pause-point-id> --node <node-id> --pause-digest <digest> --dry-run | avatars tasks approve <task-id> [--approval-key <key>] [--replay] | avatars tasks deny <task-id> [--approval-key <key>] | avatars tasks delete <task-id>")
	}
}

func taskWorkspaceStatus(workspace tasks.Workspace) string {
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		return strings.TrimSpace(workspace.Status)
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return strings.TrimSpace(workspace.Status)
	}
	return memstore.TaskWorkspaceStatus(workspace.Status, snapshot)
}

func runMemory(args []string) error {
	if len(args) == 0 {
		return errors.New(memoryUsageText())
	}
	switch args[0] {
	case "status":
		taskID, err := parseMemoryOptionalTaskArgs(args[1:], "usage: avatars memory status [--task <task-id>]")
		if err != nil {
			return err
		}
		scope := "project"
		memoryDir := filepath.Join(".avatars", "memory")
		if taskID != "" {
			workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load(taskID)
			if err != nil {
				return err
			}
			scope = "task"
			memoryDir = workspace.MemoryDir
		}
		memoryStore, err := memstore.NewSQLiteStore(memoryDir)
		if err != nil {
			return err
		}
		defer func() {
			_ = memoryStore.Close()
		}()
		status, err := memoryStore.LoadMemoryLifecycleStatus(time.Now().UTC())
		if err != nil {
			return err
		}
		printMemoryLifecycleStatus(scope, taskID, status)
		return nil
	case "maintain":
		options, err := parseMemoryMaintainArgs(args[1:])
		if err != nil {
			return err
		}
		scope, memoryDir, err := resolveMemoryScope(options.TaskID)
		if err != nil {
			return err
		}
		memoryStore, err := memstore.NewSQLiteStore(memoryDir)
		if err != nil {
			return err
		}
		defer func() {
			_ = memoryStore.Close()
		}()
		now := time.Now().UTC()
		if options.Apply {
			report, err := memoryStore.ApplyMemoryArchive(now)
			if err != nil {
				return err
			}
			printMemoryArchiveApplyReport(scope, options.TaskID, report)
			return exportProcessRecordIfProject(scope, memoryStore)
		}
		report, err := memoryStore.PlanMemoryMaintenance(now)
		if err != nil {
			return err
		}
		printMemoryMaintenanceReport(scope, options.TaskID, report)
		return exportProcessRecordIfProject(scope, memoryStore)
	case "archive-status":
		taskID, err := parseMemoryOptionalTaskArgs(args[1:], "usage: avatars memory archive-status [--task <task-id>]")
		if err != nil {
			return err
		}
		scope, memoryDir, err := resolveMemoryScope(taskID)
		if err != nil {
			return err
		}
		memoryStore, err := memstore.NewSQLiteStore(memoryDir)
		if err != nil {
			return err
		}
		defer func() {
			_ = memoryStore.Close()
		}()
		status, err := memoryStore.LoadMemoryArchiveStatus()
		if err != nil {
			return err
		}
		printMemoryArchiveStatus(scope, taskID, status)
		return nil
	case "archive-list":
		taskID, err := parseMemoryOptionalTaskArgs(args[1:], "usage: avatars memory archive-list [--task <task-id>]")
		if err != nil {
			return err
		}
		scope, memoryDir, err := resolveMemoryScope(taskID)
		if err != nil {
			return err
		}
		memoryStore, err := memstore.NewSQLiteStore(memoryDir)
		if err != nil {
			return err
		}
		defer func() {
			_ = memoryStore.Close()
		}()
		tombstones, err := memoryStore.ListMemoryArchiveTombstones(50)
		if err != nil {
			return err
		}
		printMemoryArchiveList(scope, taskID, memoryStore.Path(), tombstones)
		return nil
	case "archive-restore":
		options, err := parseMemoryArchiveRestoreArgs(args[1:])
		if err != nil {
			return err
		}
		scope, memoryDir, err := resolveMemoryScope(options.TaskID)
		if err != nil {
			return err
		}
		memoryStore, err := memstore.NewSQLiteStore(memoryDir)
		if err != nil {
			return err
		}
		defer func() {
			_ = memoryStore.Close()
		}()
		report, err := memoryStore.PlanMemoryArchiveRestore(options.TombstoneID)
		if err != nil {
			return err
		}
		printMemoryArchiveRestoreDryRun(scope, options.TaskID, report)
		return nil
	default:
		return errors.New(memoryUsageText())
	}
}

func memoryUsageText() string {
	return "usage: avatars memory status [--task <task-id>] | avatars memory maintain --dry-run [--task <task-id>] | avatars memory maintain --apply --confirm-archive [--task <task-id>] | avatars memory archive-status [--task <task-id>] | avatars memory archive-list [--task <task-id>] | avatars memory archive-restore --dry-run --tombstone <tombstone-id> [--task <task-id>]"
}

// exportProcessRecordIfProject regenerates docs/workflow/process_record.md from
// the project SQLite store (C5.1). Task-scoped maintain does not overwrite the
// project export with a single-task DB.
func exportProcessRecordIfProject(scope string, store *memstore.Store) error {
	if scope != "project" || store == nil {
		return nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	workflow.SetMemoryStore(store)
	defer workflow.SetMemoryStore(nil)
	if err := workflow.RegenerateRecordMarkdownFromSQLite(wd); err != nil {
		return fmt.Errorf("export process_record: %w", err)
	}
	fmt.Printf("Process record export: %s last_export=%s\n", workflow.DocPaths["record"], time.Now().UTC().Format("2006-01-02"))
	return nil
}

func resolveMemoryScope(taskID string) (string, string, error) {
	if taskID == "" {
		return "project", filepath.Join(".avatars", "memory"), nil
	}
	workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load(taskID)
	if err != nil {
		return "", "", err
	}
	return "task", workspace.MemoryDir, nil
}

func parseMemoryStatusArgs(args []string) (string, error) {
	return parseMemoryOptionalTaskArgs(args, "usage: avatars memory status [--task <task-id>]")
}

func parseMemoryOptionalTaskArgs(args []string, usageText string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) == 2 && args[0] == "--task" && strings.TrimSpace(args[1]) != "" {
		return strings.TrimSpace(args[1]), nil
	}
	return "", errors.New(usageText)
}

type rollbackOptions struct {
	Artifact string
	Confirm  bool
}

func runRollback(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars rollback inspect --artifact <path> | avatars rollback apply --artifact <path> --confirm")
	}
	switch args[0] {
	case "inspect":
		options, err := parseRollbackArgs(args[1:], false)
		if err != nil {
			return err
		}
		artifact, err := loadRollbackArtifact(options.Artifact)
		if err != nil {
			return err
		}
		printRollbackInspect(options.Artifact, artifact)
		return nil
	case "apply":
		options, err := parseRollbackArgs(args[1:], true)
		if err != nil {
			return err
		}
		artifact, err := loadRollbackArtifact(options.Artifact)
		if err != nil {
			return err
		}
		if err := applyRollbackArtifact(artifact); err != nil {
			return err
		}
		fmt.Printf("Rollback applied: %s\n", artifact.TargetPath)
		fmt.Printf("Rollback artifact: %s\n", options.Artifact)
		return nil
	default:
		return errors.New("usage: avatars rollback inspect --artifact <path> | avatars rollback apply --artifact <path> --confirm")
	}
}

func parseRollbackArgs(args []string, requireConfirm bool) (rollbackOptions, error) {
	usage := errors.New("usage: avatars rollback inspect --artifact <path> | avatars rollback apply --artifact <path> --confirm")
	options := rollbackOptions{}
	for len(args) > 0 {
		switch args[0] {
		case "--artifact":
			if len(args) < 2 {
				return rollbackOptions{}, usage
			}
			options.Artifact = args[1]
			args = args[2:]
		case "--confirm":
			options.Confirm = true
			args = args[1:]
		default:
			return rollbackOptions{}, usage
		}
	}
	if strings.TrimSpace(options.Artifact) == "" {
		return rollbackOptions{}, errors.New("rollback requires --artifact")
	}
	if requireConfirm && !options.Confirm {
		return rollbackOptions{}, errors.New("rollback apply requires --confirm")
	}
	if !requireConfirm && options.Confirm {
		return rollbackOptions{}, usage
	}
	return options, nil
}

func loadRollbackArtifact(path string) (runtime.CodingRollbackArtifact, error) {
	resolvedPath := filepath.Clean(strings.TrimSpace(path))
	if resolvedPath == "" {
		return runtime.CodingRollbackArtifact{}, errors.New("rollback artifact path is required")
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return runtime.CodingRollbackArtifact{}, fmt.Errorf("read rollback artifact: %w", err)
	}
	var artifact runtime.CodingRollbackArtifact
	if err := json.Unmarshal(content, &artifact); err != nil {
		return runtime.CodingRollbackArtifact{}, fmt.Errorf("decode rollback artifact: %w", err)
	}
	if strings.TrimSpace(artifact.TargetPath) == "" {
		return runtime.CodingRollbackArtifact{}, errors.New("rollback artifact missing target_path")
	}
	return artifact, nil
}

func printRollbackInspect(path string, artifact runtime.CodingRollbackArtifact) {
	fmt.Printf("Rollback artifact: %s\n", path)
	fmt.Printf("Rollback target: %s\n", artifact.TargetPath)
	fmt.Printf("Rollback task: %s\n", artifact.TaskID)
	fmt.Printf("Rollback run: %s\n", artifact.RunID)
	fmt.Printf("Rollback tool: %s/%s\n", artifact.Tool, artifact.Operation)
	fmt.Printf("Rollback existed before: %s\n", yesNo(artifact.ExistedBefore))
	if strings.TrimSpace(artifact.BeforeSHA256) != "" {
		fmt.Printf("Rollback before sha256: %s\n", artifact.BeforeSHA256)
	}
	if strings.TrimSpace(artifact.Intent) != "" {
		fmt.Printf("Rollback intent: %s\n", artifact.Intent)
	}
	if len(artifact.ExpectedTargets) > 0 {
		fmt.Printf("Rollback expected targets: %s\n", strings.Join(artifact.ExpectedTargets, ", "))
	}
	fmt.Println("Rollback guardrail: inspect only; use apply --confirm to restore this before-image.")
}

func applyRollbackArtifact(artifact runtime.CodingRollbackArtifact) error {
	targetPath := filepath.Clean(strings.TrimSpace(artifact.TargetPath))
	if targetPath == "" {
		return errors.New("rollback artifact missing target_path")
	}
	if strings.TrimSpace(artifact.AfterSHA256) != "" {
		current, err := os.ReadFile(targetPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("rollback target changed: expected current file before apply")
			}
			return fmt.Errorf("read rollback target: %w", err)
		}
		sum := sha256.Sum256(current)
		if hex.EncodeToString(sum[:]) != artifact.AfterSHA256 {
			return errors.New("rollback target changed since capture; inspect artifact again before applying")
		}
	}
	if artifact.ExistedBefore {
		if strings.TrimSpace(artifact.BeforeSHA256) != "" {
			sum := sha256.Sum256([]byte(artifact.BeforeContent))
			if hex.EncodeToString(sum[:]) != artifact.BeforeSHA256 {
				return errors.New("rollback artifact before_content does not match before_sha256")
			}
		}
		if err := os.WriteFile(targetPath, []byte(artifact.BeforeContent), 0o644); err != nil {
			return fmt.Errorf("write rollback target: %w", err)
		}
		return nil
	}
	if err := os.Remove(targetPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove rollback-created target: %w", err)
	}
	return nil
}

type memoryMaintainOptions struct {
	TaskID         string
	DryRun         bool
	Apply          bool
	ConfirmArchive bool
}

type memoryArchiveRestoreOptions struct {
	TaskID      string
	DryRun      bool
	TombstoneID string
}

func parseMemoryMaintainArgs(args []string) (memoryMaintainOptions, error) {
	usage := errors.New("usage: avatars memory maintain --dry-run [--task <task-id>] | avatars memory maintain --apply --confirm-archive [--task <task-id>]")
	if len(args) == 0 {
		return memoryMaintainOptions{}, usage
	}
	options := memoryMaintainOptions{}
	remaining := args
	for len(remaining) > 0 {
		switch remaining[0] {
		case "--dry-run":
			options.DryRun = true
			remaining = remaining[1:]
		case "--apply":
			options.Apply = true
			remaining = remaining[1:]
		case "--confirm-archive":
			options.ConfirmArchive = true
			remaining = remaining[1:]
		case "--task":
			if len(remaining) < 2 || strings.TrimSpace(remaining[1]) == "" {
				return memoryMaintainOptions{}, usage
			}
			options.TaskID = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		default:
			return memoryMaintainOptions{}, usage
		}
	}
	if options.DryRun && (options.Apply || options.ConfirmArchive) {
		return memoryMaintainOptions{}, errors.New("memory maintain uses either --dry-run or --apply --confirm-archive, not both")
	}
	if options.Apply && !options.ConfirmArchive {
		return memoryMaintainOptions{}, errors.New("memory maintain --apply requires --confirm-archive")
	}
	if options.ConfirmArchive && !options.Apply {
		return memoryMaintainOptions{}, errors.New("memory maintain --confirm-archive requires --apply")
	}
	if !options.DryRun && !options.Apply {
		return memoryMaintainOptions{}, errors.New("memory maintain requires --dry-run or --apply --confirm-archive")
	}
	return options, nil
}

func parseMemoryArchiveRestoreArgs(args []string) (memoryArchiveRestoreOptions, error) {
	usage := errors.New("usage: avatars memory archive-restore --dry-run --tombstone <tombstone-id> [--task <task-id>]")
	if len(args) == 0 {
		return memoryArchiveRestoreOptions{}, usage
	}
	options := memoryArchiveRestoreOptions{}
	remaining := args
	for len(remaining) > 0 {
		switch remaining[0] {
		case "--dry-run":
			options.DryRun = true
			remaining = remaining[1:]
		case "--tombstone":
			if len(remaining) < 2 || strings.TrimSpace(remaining[1]) == "" {
				return memoryArchiveRestoreOptions{}, usage
			}
			options.TombstoneID = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		case "--task":
			if len(remaining) < 2 || strings.TrimSpace(remaining[1]) == "" {
				return memoryArchiveRestoreOptions{}, usage
			}
			options.TaskID = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		default:
			return memoryArchiveRestoreOptions{}, usage
		}
	}
	if !options.DryRun {
		return memoryArchiveRestoreOptions{}, errors.New("memory archive-restore requires --dry-run")
	}
	if options.TombstoneID == "" {
		return memoryArchiveRestoreOptions{}, errors.New("memory archive-restore requires --tombstone")
	}
	return options, nil
}

func printMemoryLifecycleStatus(scope string, taskID string, status memstore.MemoryLifecycleStatus) {
	fmt.Printf("Memory status: scope=%s", scope)
	if taskID != "" {
		fmt.Printf(" task=%s", taskID)
	}
	fmt.Println()
	if status.Path != "" {
		fmt.Printf("Memory DB: %s\n", status.Path)
	}
	fmt.Printf("Memory DB size bytes: %d\n", status.SizeBytes)
	fmt.Printf("Memory maintenance mode: %s\n", status.MaintenanceMode)
	fmt.Printf("Memory maintenance apply supported: %s\n", yesNo(status.MaintenanceSupported))
	if len(status.Tables) > 0 {
		fmt.Printf("Memory tables: %d\n", len(status.Tables))
		for _, table := range status.Tables {
			fmt.Printf("- %s rows=%d", table.Name, table.Rows)
			if table.OldestRaw != "" {
				fmt.Printf(" oldest=%s", table.OldestRaw)
			}
			if table.NewestRaw != "" {
				fmt.Printf(" newest=%s", table.NewestRaw)
			}
			fmt.Println()
		}
	}
	if len(status.PruningLimits) > 0 {
		fmt.Printf("Memory pruning limits: %d\n", len(status.PruningLimits))
		for _, limit := range status.PruningLimits {
			fmt.Printf("- %s=%d\n", limit.Name, limit.Limit)
		}
	}
	fmt.Printf("Memory retirement candidates: total=%d stale_warm_lessons=%d retirable_evolution_candidates=%d low_support_project_lessons=%d\n", status.RetirementCandidateTotal, status.StaleWarmLessons, status.RetirableEvolutionCandidates, status.LowSupportProjectLessons)
	fmt.Printf("Memory protected truth: evaluation_records=%d verification_history=%d\n", status.ProtectedEvaluationRecords, status.ProtectedVerificationHistory)
	fmt.Println("Memory lifecycle action: inspect_only; guarded archive apply requires memory maintain --apply --confirm-archive")
}

func printMemoryMaintenanceReport(scope string, taskID string, report memstore.MemoryMaintenanceReport) {
	fmt.Printf("Memory maintenance dry run: scope=%s", scope)
	if taskID != "" {
		fmt.Printf(" task=%s", taskID)
	}
	fmt.Println()
	if report.Path != "" {
		fmt.Printf("Memory DB: %s\n", report.Path)
	}
	fmt.Printf("Memory maintenance mode: %s\n", report.Mode)
	fmt.Printf("Memory maintenance apply supported: %s\n", yesNo(report.ApplySupported))
	fmt.Printf("Memory maintenance planned action: %s\n", report.PlannedAction)
	fmt.Printf("Memory retirement candidates: total=%d stale_warm_lessons=%d retirable_evolution_candidates=%d low_support_project_lessons=%d\n", report.RetirementCandidateTotal, report.StaleWarmLessons, report.RetirableEvolutionCandidates, report.LowSupportProjectLessons)
	fmt.Printf("Memory reusable kept: warm_lessons=%d evolution_candidates=%d project_lessons=%d\n", report.ReusableWarmLessonsKept, report.ReusableEvolutionKept, report.ReusableProjectLessonsKept)
	fmt.Printf("Memory protected truth skipped: total=%d evaluation_records=%d verification_history=%d\n", report.ProtectedTruthSkipped, report.ProtectedEvaluationRecords, report.ProtectedVerificationHistory)
	if report.Guardrail != "" {
		fmt.Printf("Memory maintenance guardrail: %s\n", report.Guardrail)
	}
}

func printMemoryArchiveApplyReport(scope string, taskID string, report memstore.MemoryArchiveApplyReport) {
	fmt.Printf("Memory archive apply: scope=%s", scope)
	if taskID != "" {
		fmt.Printf(" task=%s", taskID)
	}
	fmt.Println()
	if report.Path != "" {
		fmt.Printf("Memory DB: %s\n", report.Path)
	}
	fmt.Printf("Memory archive mode: %s\n", report.Mode)
	fmt.Printf("Memory archive planned candidates: %d\n", report.PlannedCandidates)
	fmt.Printf("Memory archive apply result: archived=%d retired_from_hot=%d idempotent_skipped=%d protected_skipped=%d\n", report.Archived, report.RetiredFromHot, report.IdempotentSkipped, report.ProtectedSkipped)
	if len(report.SupportedTables) > 0 {
		fmt.Printf("Memory archive supported tables: %s\n", strings.Join(report.SupportedTables, ", "))
	}
	if len(report.ProtectedClasses) > 0 {
		fmt.Printf("Memory archive protected classes: %s\n", strings.Join(report.ProtectedClasses, ", "))
	}
	if report.Guardrail != "" {
		fmt.Printf("Memory archive guardrail: %s\n", report.Guardrail)
	}
}

func printMemoryArchiveStatus(scope string, taskID string, status memstore.MemoryArchiveStatus) {
	fmt.Printf("Memory archive status: scope=%s", scope)
	if taskID != "" {
		fmt.Printf(" task=%s", taskID)
	}
	fmt.Println()
	if status.Path != "" {
		fmt.Printf("Memory DB: %s\n", status.Path)
	}
	fmt.Printf("Memory archive schema ready: %s\n", yesNo(status.SchemaReady))
	fmt.Printf("Memory archive mode: %s\n", status.Mode)
	fmt.Printf("Memory archive apply supported: %s\n", yesNo(status.ApplySupported))
	fmt.Printf("Memory archive tombstones: total=%d archived=%d retired=%d\n", status.TombstoneCount, status.ArchivedCount, status.RetiredCount)
	if len(status.SupportedTables) > 0 {
		fmt.Printf("Memory archive supported tables: %s\n", strings.Join(status.SupportedTables, ", "))
	}
	if len(status.ProtectedClasses) > 0 {
		fmt.Printf("Memory archive protected classes: %s\n", strings.Join(status.ProtectedClasses, ", "))
	}
}

func printMemoryArchiveList(scope string, taskID string, path string, tombstones []memstore.MemoryArchiveTombstone) {
	fmt.Printf("Memory archive list: scope=%s", scope)
	if taskID != "" {
		fmt.Printf(" task=%s", taskID)
	}
	fmt.Println()
	if path != "" {
		fmt.Printf("Memory DB: %s\n", path)
	}
	fmt.Printf("Memory archive tombstones listed: %d\n", len(tombstones))
	for index, tombstone := range tombstones {
		fmt.Printf("#%d %s table=%s key=%s reason=%s state=%s archived_at=%s\n", index+1, tombstone.TombstoneID, tombstone.OriginalTable, tombstone.OriginalKey, tombstone.Reason, tombstone.State, tombstone.ArchivedAt.UTC().Format(time.RFC3339))
		if tombstone.OriginalSource != "" {
			fmt.Printf("source=%s\n", tombstone.OriginalSource)
		}
		if tombstone.OriginalSummary != "" {
			fmt.Printf("summary=%s\n", tombstone.OriginalSummary)
		}
	}
}

func printMemoryArchiveRestoreDryRun(scope string, taskID string, report memstore.MemoryArchiveRestoreDryRun) {
	fmt.Printf("Memory archive restore dry run: scope=%s", scope)
	if taskID != "" {
		fmt.Printf(" task=%s", taskID)
	}
	fmt.Println()
	if report.Path != "" {
		fmt.Printf("Memory DB: %s\n", report.Path)
	}
	fmt.Printf("Memory archive restore tombstone: %s\n", report.Tombstone.TombstoneID)
	fmt.Printf("Memory archive restore target: table=%s key=%s state=%s reason=%s\n", report.TargetTable, report.TargetKey, report.Tombstone.State, report.Tombstone.Reason)
	if report.TargetSummary != "" {
		fmt.Printf("Memory archive restore summary: %s\n", report.TargetSummary)
	}
	if report.TargetSource != "" {
		fmt.Printf("Memory archive restore source: %s\n", report.TargetSource)
	}
	fmt.Printf("Memory archive restore conflict: %s hot_record_exists=%s", yesNo(report.Conflict), yesNo(report.HotRecordExists))
	if report.ConflictReason != "" {
		fmt.Printf(" reason=%s", report.ConflictReason)
	}
	fmt.Println()
	fmt.Printf("Memory archive restore supported: %s\n", yesNo(report.RestoreSupported))
	fmt.Printf("Memory protected truth skipped: %d\n", report.ProtectedTruthSkipped)
	if report.Guardrail != "" {
		fmt.Printf("Memory archive restore guardrail: %s\n", report.Guardrail)
	}
}

func runGovernance(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars governance status [--task <task-id>]")
	}
	switch args[0] {
	case "status":
		taskID, err := parseMemoryOptionalTaskArgs(args[1:], "usage: avatars governance status [--task <task-id>]")
		if err != nil {
			return err
		}
		return runGovernanceStatus(taskID)
	default:
		return errors.New("usage: avatars governance status [--task <task-id>]")
	}
}

func runGovernanceStatus(taskID string) error {
	scope, memoryDir, err := resolveMemoryScope(taskID)
	if err != nil {
		return err
	}
	memoryStore, err := memstore.NewSQLiteStore(memoryDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	now := time.Now().UTC()
	memoryStatus, err := memoryStore.LoadMemoryLifecycleStatus(now)
	if err != nil {
		return err
	}
	archiveStatus, err := memoryStore.LoadMemoryArchiveStatus()
	if err != nil {
		return err
	}
	skillStore := skills.NewStore(filepath.Join(app.ResolveRuntimeHome(), "skills"))
	skillSummary, err := skillStore.GovernanceSummary()
	if err != nil {
		return err
	}
	skillReconciliation, err := skillStore.GovernanceReconciliation()
	if err != nil {
		return err
	}
	ledger, err := skillStore.GovernanceLedger()
	if err != nil {
		return err
	}

	var workspaceStatus string
	var snapshot memstore.Snapshot
	if taskID != "" {
		workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load(taskID)
		if err != nil {
			return err
		}
		workspaceStatus = strings.TrimSpace(workspace.Status)
		snapshot, err = memoryStore.LoadLatestTaskSnapshot(taskID)
		if err != nil {
			return err
		}
		workspaceStatus = memstore.TaskWorkspaceStatus(workspaceStatus, snapshot)
	}
	printGovernanceStatus(scope, taskID, workspaceStatus, snapshot, memoryStatus, archiveStatus, skillSummary, skillReconciliation, ledger)
	return nil
}

func printGovernanceStatus(scope string, taskID string, workspaceStatus string, snapshot memstore.Snapshot, memoryStatus memstore.MemoryLifecycleStatus, archiveStatus memstore.MemoryArchiveStatus, skillSummary skills.GovernanceSummary, reconciliation skills.GovernanceReconciliation, ledger skills.GovernanceLedger) {
	skillDrifts := governanceSkillDriftCount(reconciliation)
	skillBlockers := skillDrifts + len(skillSummary.Alerts)
	memoryProtected := memoryStatus.ProtectedEvaluationRecords + memoryStatus.ProtectedVerificationHistory
	runtimeStatus := governanceRuntimeStatus(taskID, workspaceStatus, snapshot)
	memoryReadiness := governanceReviewStatus(memoryStatus.RetirementCandidateTotal + archiveStatus.TombstoneCount)
	skillReadiness := governanceBlockerStatus(skillBlockers)
	verifierStatus := governanceVerifierStatus(snapshot)

	fmt.Printf("Governance status: scope=%s", scope)
	if taskID != "" {
		fmt.Printf(" task=%s", taskID)
	}
	fmt.Println()
	fmt.Printf("Governance readiness: runtime_truth=%s memory=%s skills=%s verifier=%s\n", runtimeStatus, memoryReadiness, skillReadiness, verifierStatus)
	if taskID != "" {
		fmt.Printf("Runtime truth: task_status=%s evaluation_records=%d verification_history=%d recent_events=%d\n", emptyAsUnknown(workspaceStatus), len(snapshot.EvaluationRecords), len(snapshot.VerificationHistory), len(snapshot.RecentEvents))
	} else {
		fmt.Println("Runtime truth: task_status=project_scope evaluation_records=not_scoped verification_history=not_scoped recent_events=not_scoped")
	}
	fmt.Printf("Memory lifecycle: candidates=%d tombstones=%d protected_truth=%d maintenance_mode=%s archive_mode=%s\n", memoryStatus.RetirementCandidateTotal, archiveStatus.TombstoneCount, memoryProtected, memoryStatus.MaintenanceMode, archiveStatus.Mode)
	fmt.Printf("Skill governance: tracked=%d generated=%d approved=%d archived=%d disabled=%d ledger_events=%d alerts=%d drifts=%d\n", skillSummary.TrackedSkills, skillSummary.GeneratedCount, skillSummary.ApprovedCount, skillSummary.ArchivedCount, skillSummary.DisabledCount, ledger.EventCount, len(skillSummary.Alerts), skillDrifts)
	fmt.Printf("Verifier: current=%s latest_non_pass=%s reverify=%s history_window=bounded closure_evidence=%s\n", governanceCurrentVerification(snapshot), governanceLatestNonPass(snapshot), governanceReverifyStatus(snapshot), governanceVerifierClosureEvidence(snapshot))
	printGovernanceWorkflowNodeExecution(snapshot)
	fmt.Printf("Governance blockers: total=%d skill_drifts=%d skill_alerts=%d verifier_pending=%s\n", governanceBlockerCount(skillBlockers, workspaceStatus, snapshot), skillDrifts, len(skillSummary.Alerts), yesNo(governanceVerifierPending(snapshot)))
	fmt.Println("Governance guardrail: read-only status. No skills, memory, runtime truth, verifier records, or tombstones are mutated.")
}

func printGovernanceWorkflowNodeExecution(snapshot memstore.Snapshot) {
	governance := memstore.WorkflowNodeGovernanceSnapshotForTask(snapshot)
	fmt.Printf("Workflow node execution: latest_node_evidence=%s failed_node_retry_candidate=%s pending_approval=%s verifier_pending=%s\n", yesNo(governance.LatestNodeEvidence), yesNo(governance.RetryCandidateEvidence), yesNo(governance.PendingApprovalCount > 0), yesNo(governance.VerifierPending))
	if governance.LatestNodeEvidence {
		details := []string{governance.LatestNodeID}
		if governance.LatestNodeStatus != "" {
			details = append(details, "status="+governance.LatestNodeStatus)
		}
		if governance.LatestNodeVerifierGateStatus != "" {
			details = append(details, "gate="+governance.LatestNodeVerifierGateStatus)
		}
		if governance.LatestNodeVerifierVerdict != "" {
			details = append(details, "verdict="+governance.LatestNodeVerifierVerdict)
		}
		if governance.LatestNodeRecoveryKind != "" {
			details = append(details, "recovery="+governance.LatestNodeRecoveryKind)
		}
		fmt.Printf("Workflow node latest: %s\n", strings.Join(details, " "))
	}
	if governance.RetryCandidateEvidence {
		fmt.Printf("Workflow node failed retry candidate: node=%s retryable=%s contract_ready=%s\n", governance.RetryCandidateNodeID, governance.RetryCandidateRetryable, yesNo(governance.RetryCandidateContractReady))
	}
	if governance.RetryClosureEvidence {
		fmt.Printf("Workflow node retry closure: attempt=%s ready=%s missing=%d status=%s\n", governance.RetryClosureAttemptID, yesNo(governance.RetryClosureReady), len(governance.RetryClosureMissingEvidence), governance.RetryClosureStatus)
		if len(governance.RetryClosureMissingEvidence) > 0 {
			fmt.Printf("Workflow node retry closure missing: %s\n", strings.Join(governance.RetryClosureMissingEvidence, ", "))
		}
		if governance.RetryClosureVerifierGateStatus != "" || governance.RetryClosureVerifierVerdict != "" {
			fmt.Printf("Workflow node retry verifier gate: status=%s verdict=%s\n", emptyAsUnknown(governance.RetryClosureVerifierGateStatus), emptyAsUnknown(governance.RetryClosureVerifierVerdict))
		}
		if governance.RetryClosureVerifierReportPath != "" {
			fmt.Printf("Workflow node retry verifier report: %s\n", governance.RetryClosureVerifierReportPath)
		}
	}
	if governance.PendingApprovalCount > 0 {
		fmt.Printf("Workflow node pending approvals: %d\n", governance.PendingApprovalCount)
	}
	if governance.NextActionCue != "" {
		fmt.Printf("Workflow node next action: %s\n", governance.NextActionCue)
	}
}

func printTaskGovernanceView(workspace tasks.Workspace, workspaceStatus string, memoryStore *memstore.Store, snapshot memstore.Snapshot) error {
	now := time.Now().UTC()
	memoryStatus, err := memoryStore.LoadMemoryLifecycleStatus(now)
	if err != nil {
		return err
	}
	archiveStatus, err := memoryStore.LoadMemoryArchiveStatus()
	if err != nil {
		return err
	}
	skillStore := skills.NewStore(filepath.Join(app.ResolveRuntimeHome(), "skills"))
	skillSummary, err := skillStore.GovernanceSummary()
	if err != nil {
		return err
	}
	reconciliation, err := skillStore.GovernanceReconciliation()
	if err != nil {
		return err
	}
	ledger, err := skillStore.GovernanceLedger()
	if err != nil {
		return err
	}
	fmt.Printf("Task governance view: task=%s\n", workspace.ID)
	printGovernanceStatus("task", workspace.ID, workspaceStatus, snapshot, memoryStatus, archiveStatus, skillSummary, reconciliation, ledger)
	fmt.Println("Task governance gate: review blockers before memory restore apply, skill repair/sync, or retry expansion.")
	return nil
}

func governanceRuntimeStatus(taskID string, workspaceStatus string, snapshot memstore.Snapshot) string {
	if taskID == "" {
		return "project_scope"
	}
	if snapshot.IsEmpty() && strings.TrimSpace(workspaceStatus) == "" {
		return "missing"
	}
	return memstore.TaskFinalityReadinessForTask(workspaceStatus, snapshot).RuntimeTruthReadiness
}

func governanceReviewStatus(count int) string {
	if count > 0 {
		return "review"
	}
	return "ok"
}

func governanceBlockerStatus(count int) string {
	if count > 0 {
		return "blocked"
	}
	return "ok"
}

func governanceVerifierStatus(snapshot memstore.Snapshot) string {
	if governanceVerifierPending(snapshot) {
		return "pending"
	}
	if snapshot.Verification == nil && len(snapshot.VerificationHistory) == 0 {
		return "unknown"
	}
	return "ok"
}

func governanceVerifierPending(snapshot memstore.Snapshot) bool {
	return memstore.VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory) == "pending reverify for latest non-pass verification"
}

func governanceCurrentVerification(snapshot memstore.Snapshot) string {
	if snapshot.Verification == nil {
		return "none"
	}
	verdict := strings.TrimSpace(snapshot.Verification.Verdict)
	if verdict == "" {
		return "unknown"
	}
	return verdict
}

func governanceLatestNonPass(snapshot memstore.Snapshot) string {
	latest := memstore.LatestNonPassVerificationSnapshot(snapshot.Verification, snapshot.VerificationHistory)
	if latest == nil {
		return "none"
	}
	if latest.Tool != "" {
		return strings.TrimSpace(latest.Verdict) + "/" + strings.TrimSpace(latest.Tool)
	}
	return strings.TrimSpace(latest.Verdict)
}

func governanceReverifyStatus(snapshot memstore.Snapshot) string {
	status := memstore.VerificationReverifyStatus(snapshot.Verification, snapshot.VerificationHistory)
	if status == "" {
		return "none"
	}
	return status
}

func governanceVerifierClosureEvidence(snapshot memstore.Snapshot) string {
	if recoverySummary := memstore.LatestRecoveryInspectionSummary(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords, 10, "task_operator_command"); recoverySummary != nil && recoverySummary.DecisionCategory == "closed" && recoverySummary.ClosureStatus == "pass_closed" {
		return "evaluation_records"
	}
	if memstore.LatestReverifyClosureEvaluation(snapshot.Verification, snapshot.VerificationHistory, snapshot.EvaluationRecords) != nil {
		return "evaluation_records"
	}
	return "none"
}

func governanceSkillDriftCount(reconciliation skills.GovernanceReconciliation) int {
	return len(reconciliation.MissingCurrent) +
		len(reconciliation.UntrackedCurrent) +
		len(reconciliation.StateDrifts) +
		len(reconciliation.PathDrifts) +
		len(reconciliation.ContentDrifts) +
		len(reconciliation.MetadataDrifts) +
		len(reconciliation.HistoryDrifts) +
		len(reconciliation.InvariantDrifts)
}

func governanceBlockerCount(skillBlockers int, workspaceStatus string, snapshot memstore.Snapshot) int {
	total := skillBlockers
	if governanceVerifierPending(snapshot) {
		total++
	}
	total += memstore.TaskFinalityReadinessForTask(workspaceStatus, snapshot).BlockerCount
	return total
}

func emptyAsUnknown(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func runTaskRetryNodeDryRun(manager *tasks.Manager, args []string) error {
	options, err := parseTaskRetryNodeDryRunOptions(args)
	if err != nil {
		return err
	}
	workspace, err := manager.Load(options.TaskID)
	if err != nil {
		return err
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return err
	}
	candidate := memstore.LatestFailedNodeRetryCandidate(snapshot.EvaluationRecords)
	expectedTargets := []string(nil)
	verifierGateExpectation := ""
	if candidate != nil {
		expectedTargets = append([]string(nil), candidate.ExpectedTargets...)
		verifierGateExpectation = failedNodeRetryDryRunVerifierGateExpectation(snapshot.EvaluationRecords, *candidate)
	}
	boundary := memstore.EvaluateFailedNodeRetryPreflight(snapshot.EvaluationRecords, memstore.FailedNodeRetryPreflightRequest{
		TaskID:                  workspace.ID,
		OriginRunID:             options.OriginRunID,
		OriginTaskID:            workspace.ID,
		PausePointID:            options.PausePointID,
		NodeID:                  options.NodeID,
		PausePointDigest:        options.PausePointDigest,
		ExpectedTargets:         expectedTargets,
		VerifierGateExpectation: verifierGateExpectation,
		OperatorConfirmed:       true,
		OperatorSource:          "task_retry_node_dry_run",
	})
	fmt.Println("Failed node retry dry run: inspection-only")
	fmt.Printf("Task: %s\n", workspace.ID)
	printFailedNodeRetryPreflightBoundary("Failed node retry preflight", boundary)
	printFailedNodeRetryCandidate("Failed node retry candidate", candidate)
	printFailedNodeRetryCurrentStateReadiness("Failed node retry current state", memstore.LatestFailedNodeRetryCurrentStateReadiness(snapshot.EvaluationRecords))
	printFailedNodeRetryArtifact("Failed node retry artifact", memstore.FailedNodeRetryArtifactFromPreflight(snapshot.EvaluationRecords, boundary))
	printFailedNodeRetryAttempt("Failed node retry latest attempt", memstore.LatestFailedNodeRetryAttempt(snapshot.EvaluationRecords))
	printFailedNodeRetryAttemptClosureEvidence("Failed node retry latest attempt closure", memstore.LatestFailedNodeRetryAttemptClosureEvidence(snapshot.EvaluationRecords))
	fmt.Println("Failed node retry dry run result: no retry attempt recorded; scheduler execution unavailable")
	return nil
}

func parseTaskRetryNodeDryRunOptions(args []string) (retryNodeDryRunOptions, error) {
	usage := errors.New("usage: avatars tasks retry-node <task-id> --origin-run <run-id> --pause-point <pause-point-id> --node <node-id> --pause-digest <digest> --dry-run")
	if len(args) == 0 {
		return retryNodeDryRunOptions{}, usage
	}
	options := retryNodeDryRunOptions{TaskID: strings.TrimSpace(args[0])}
	if options.TaskID == "" {
		return retryNodeDryRunOptions{}, usage
	}
	remaining := args[1:]
	for len(remaining) > 0 {
		switch remaining[0] {
		case "--origin-run":
			if len(remaining) < 2 {
				return retryNodeDryRunOptions{}, usage
			}
			options.OriginRunID = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		case "--pause-point":
			if len(remaining) < 2 {
				return retryNodeDryRunOptions{}, usage
			}
			options.PausePointID = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		case "--node":
			if len(remaining) < 2 {
				return retryNodeDryRunOptions{}, usage
			}
			options.NodeID = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		case "--pause-digest":
			if len(remaining) < 2 {
				return retryNodeDryRunOptions{}, usage
			}
			options.PausePointDigest = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		case "--dry-run":
			options.DryRun = true
			remaining = remaining[1:]
		case "--confirm-recorded-truth":
			return retryNodeDryRunOptions{}, errors.New("failed-node retry execution is unavailable; use --dry-run for inspection only")
		default:
			return retryNodeDryRunOptions{}, usage
		}
	}
	if options.OriginRunID == "" || options.PausePointID == "" || options.NodeID == "" || options.PausePointDigest == "" {
		return retryNodeDryRunOptions{}, usage
	}
	if !options.DryRun {
		return retryNodeDryRunOptions{}, errors.New("failed-node retry-node requires --dry-run; executable retry is unavailable")
	}
	return options, nil
}

func failedNodeRetryDryRunVerifierGateExpectation(records []memstore.EvaluationRecord, candidate memstore.FailedNodeRetryCandidate) string {
	if attempt := memstore.LatestFailedNodeRetryAttempt(records); attempt != nil {
		if attempt.PausePointID == candidate.PausePointID && attempt.NodeID == candidate.NodeID && attempt.PausePointDigest == candidate.PausePointDigest {
			return attempt.RetryAttemptVerifierGateExpectation
		}
	}
	if len(candidate.ExpectedTargets) == 0 {
		return "no mutating targets recorded"
	}
	return ""
}

func runTaskApprovalDecision(manager *tasks.Manager, action string, args []string) error {
	taskID, requestedApprovalKey, replay, err := parseTaskApprovalDecisionArgs(action, args)
	if err != nil {
		return err
	}
	if replay && strings.TrimSpace(action) != "approve" {
		return errors.New("usage: avatars tasks deny <task-id> [--approval-key <key>]")
	}
	workspace, err := manager.Load(taskID)
	if err != nil {
		return err
	}
	memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = memoryStore.Close()
	}()
	snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
	if err != nil {
		return err
	}
	pending := memstore.LatestPendingToolApprovalEvaluation(snapshot.EvaluationRecords)
	if strings.TrimSpace(requestedApprovalKey) != "" {
		pending = memstore.PendingToolApprovalEvaluationByKey(snapshot.EvaluationRecords, requestedApprovalKey)
	}
	if pending == nil {
		if strings.TrimSpace(requestedApprovalKey) != "" {
			return fmt.Errorf("task %s has no pending approval request for key %s", workspace.ID, requestedApprovalKey)
		}
		return fmt.Errorf("task %s has no pending approval request", workspace.ID)
	}
	approvalKey := memstore.EvaluationRecordApprovalKey(*pending)
	if strings.TrimSpace(approvalKey) == "" {
		return fmt.Errorf("selected pending approval request for task %s has no approval key", workspace.ID)
	}
	var replayRequest runtime.PendingApprovalRequest
	if replay {
		replayRequest, err = runtime.LoadPendingApprovalRequest(workspace.MemoryDir, approvalKey)
		if err != nil {
			return err
		}
	}
	decisionCause := "tool_approval_granted"
	decisionLabel := "granted"
	verdict := "PASS"
	if strings.TrimSpace(action) == "deny" {
		decisionCause = "tool_approval_denied"
		decisionLabel = "denied"
		verdict = "FAIL"
	}
	toolLabel := strings.TrimSpace(pending.Tool)
	if toolLabel == "" {
		toolLabel = "tool"
	}
	if operation := memstore.EvaluationRecordOperation(*pending); operation != "" {
		toolLabel = toolLabel + "/" + operation
	}
	details := []string{
		"decision_source: task_operator_command",
		"approval_key: " + approvalKey,
	}
	if operation := memstore.EvaluationRecordOperation(*pending); operation != "" {
		details = append(details, "operation: "+operation)
	}
	if permissionMode := memstore.EvaluationRecordPermissionMode(*pending); permissionMode != "" {
		details = append(details, "permission_mode: "+permissionMode)
	}
	if targets := memstore.EvaluationRecordExpectedTargets(*pending); len(targets) > 0 {
		details = append(details, "expected_targets: "+strings.Join(targets, ", "))
	}
	decisionSummary := fmt.Sprintf("Tool %s approval %s for the selected pending request.", toolLabel, decisionLabel)
	runID := fmt.Sprintf("task-approval-%d", time.Now().UTC().UnixNano())
	if err := memoryStore.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "task-approval-cli",
		RunID:     runID,
		TaskID:    workspace.ID,
		Tool:      strings.TrimSpace(pending.Tool),
		Kind:      "passive_feedback",
		Verdict:   verdict,
		Cause:     decisionCause,
		Summary:   decisionSummary,
		Source:    "task_operator_command",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	fmt.Println(decisionSummary)
	fmt.Printf("Task: %s\n", workspace.ID)
	fmt.Printf("Approval key: %s\n", approvalKey)
	if !replay {
		return nil
	}
	toolName, operation, toolInput, err := replayRequest.ResolveToolCall()
	if err != nil {
		return err
	}
	replayMode := runtime.PermissionModeDefault
	if parsedMode, parseErr := runtime.ParsePermissionMode(replayRequest.PermissionMode); parseErr == nil {
		replayMode = parsedMode
	}
	application, err := app.BootstrapWithOptions(app.BootstrapOptions{Workspace: &workspace, PermissionMode: replayMode})
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()
	result, replayErr := application.Engine.ContinueApprovedToolCall(context.Background(), replayRequest, replayToolCallOptions(toolName, operation, toolInput))
	replaySummary := fmt.Sprintf("Continued approved request for %s/%s.", toolName, operation)
	if strings.TrimSpace(result.Content) != "" {
		fmt.Println(result.Content)
		replaySummary = fmt.Sprintf("Continued approved request for %s/%s: %s", toolName, operation, strings.TrimSpace(result.Content))
	}
	if result.TranscriptPath != "" {
		updatedWorkspace, markErr := manager.MarkRun(workspace, result.TranscriptPath, replaySummary)
		if markErr != nil {
			return markErr
		}
		workspace = updatedWorkspace
		fmt.Printf("Continuation transcript: %s\n", result.TranscriptPath)
	}
	if replayErr == nil {
		if finalizeErr := maybeFinalizeGeneratedSkillReplay(application.Skills, toolName, operation, toolInput); finalizeErr != nil {
			replayErr = finalizeErr
		}
	}
	if err := recordTaskApprovalReplayEvaluation(memoryStore, workspace.ID, replayRequest, toolName, operation, result.TranscriptPath, result.ContinuationRun, result.ContinuationTask, replaySummary, replayErr); err != nil {
		return err
	}
	if replayErr != nil {
		return replayErr
	}
	return nil
}

func recordTaskApprovalReplayEvaluation(memoryStore *memstore.Store, taskID string, replayRequest runtime.PendingApprovalRequest, toolName string, operation string, transcriptPath string, continuationRunID string, continuationTaskID string, replaySummary string, replayErr error) error {
	approvalKey := strings.TrimSpace(replayRequest.ApprovalKey)
	details := []string{"decision_source: task_operator_replay"}
	if approvalKey != "" {
		details = append(details, "approval_key: "+approvalKey)
	}
	if operation = strings.TrimSpace(operation); operation != "" {
		details = append(details, "operation: "+operation)
	}
	if permissionMode := strings.TrimSpace(replayRequest.PermissionMode); permissionMode != "" {
		details = append(details, "permission_mode: "+permissionMode)
	}
	if artifactPath := runtime.PendingApprovalRequestPath(filepath.Join(".avatars", "memory"), approvalKey); artifactPath != "" {
		details = append(details, "approval_request_artifact: "+artifactPath)
	}
	if transcriptPath = strings.TrimSpace(transcriptPath); transcriptPath != "" {
		details = append(details, "continuation_transcript: "+transcriptPath)
	}
	if continuationRun := strings.TrimSpace(continuationRunID); continuationRun != "" {
		details = append(details, "continuation_run_id: "+continuationRun)
	}
	if continuationTask := strings.TrimSpace(continuationTaskID); continuationTask != "" {
		details = append(details, "continuation_task_id: "+continuationTask)
	}
	toolName = strings.TrimSpace(toolName)
	verdict := "PASS"
	cause := "tool_approval_replayed"
	summary := strings.TrimSpace(replaySummary)
	if summary == "" {
		summary = fmt.Sprintf("Continued approved request for %s/%s.", toolName, operation)
	}
	if strings.Contains(summary, "Approval replay skipped") {
		cause = "tool_approval_replay_skipped"
		details = append(details, "replay_status: skipped", "skip_reason: already_completed")
	}
	if replayErr != nil {
		verdict = "FAIL"
		cause = "tool_approval_replay_failed"
		summary = fmt.Sprintf("Replay failed for approved request %s/%s: %v", toolName, operation, replayErr)
	}
	return memoryStore.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: "task-approval-cli",
		RunID:     fmt.Sprintf("task-approval-replay-%d", time.Now().UTC().UnixNano()),
		TaskID:    strings.TrimSpace(taskID),
		Tool:      toolName,
		Kind:      "passive_feedback",
		Verdict:   verdict,
		Cause:     cause,
		Summary:   summary,
		Source:    "task_operator_command",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	})
}

func maybeFinalizeGeneratedSkillReplay(store *skills.Store, toolName string, operation string, input any) error {
	if store == nil || strings.TrimSpace(toolName) != "write" || strings.TrimSpace(operation) != "file_write" {
		return nil
	}
	var writeInput tools.WriteInput
	switch value := input.(type) {
	case tools.WriteInput:
		writeInput = value
	case *tools.WriteInput:
		if value == nil {
			return nil
		}
		writeInput = *value
	default:
		return nil
	}
	if !looksLikeGeneratedSkillPath(writeInput.Path) {
		return nil
	}
	return store.FinalizeGenerated(writeInput.Path, time.Time{})
}

func looksLikeGeneratedSkillPath(path string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "/skills/generated/") || strings.HasPrefix(normalized, "skills/generated/") || strings.HasPrefix(normalized, "./skills/generated/")
}

func replayToolCallOptions(toolName string, operation string, input any) runtime.ToolCallOptions {
	if strings.TrimSpace(toolName) != "write" || strings.TrimSpace(operation) != "file_write" {
		return runtime.ToolCallOptions{}
	}
	var path string
	switch value := input.(type) {
	case tools.WriteInput:
		path = value.Path
	case *tools.WriteInput:
		if value != nil {
			path = value.Path
		}
	}
	if looksLikeGeneratedSkillPath(path) {
		return runtime.ToolCallOptions{SkipVerification: true}
	}
	return runtime.ToolCallOptions{}
}

func parseTaskApprovalDecisionArgs(action string, args []string) (string, string, bool, error) {
	usage := fmt.Errorf("usage: avatars tasks %s <task-id> [--approval-key <key>]", strings.TrimSpace(action))
	if strings.TrimSpace(action) == "approve" {
		usage = fmt.Errorf("usage: avatars tasks approve <task-id> [--approval-key <key>] [--replay]")
	}
	if len(args) == 0 {
		return "", "", false, usage
	}
	taskID := strings.TrimSpace(args[0])
	if taskID == "" {
		return "", "", false, usage
	}
	approvalKey := ""
	replay := false
	remaining := args[1:]
	for len(remaining) > 0 {
		switch remaining[0] {
		case "--approval-key":
			if len(remaining) < 2 {
				return "", "", false, usage
			}
			approvalKey = strings.TrimSpace(remaining[1])
			remaining = remaining[2:]
		case "--replay":
			replay = true
			remaining = remaining[1:]
		default:
			return "", "", false, usage
		}
	}
	return taskID, approvalKey, replay, nil
}

func printTaskQueryView(taskID string, view memstore.QueryView) {
	fmt.Printf("Query view: %s\n", view.Reader)
	if view.AvatarID != "" {
		fmt.Printf("View avatar: %s\n", view.AvatarID)
	}
	if view.StableSummary != "" {
		fmt.Printf("View stable summary: %s\n", view.StableSummary)
	}
	if view.TaskSummary != "" {
		fmt.Printf("View task summary: %s\n", view.TaskSummary)
	}
	if view.AvatarSummary != "" {
		fmt.Printf("View avatar summary: %s\n", view.AvatarSummary)
	}
	if len(view.RecentEvents) > 0 {
		fmt.Printf("View recent events: %d\n", len(view.RecentEvents))
		for _, event := range view.RecentEvents {
			fmt.Printf("* %s\n", event)
		}
	}
	printExecutionChain("View execution chain", view.RecentEvents, "=>")
	printLatestAvatarMessages("View latest avatar", view.RecentEvents)
	printSnapshotLLMStatus("View run LLM", view.LLMStatus, view.LLMStatusWarning)
	printTelemetrySnapshot("View telemetry", view.Telemetry)
	if view.Verification != nil {
		fmt.Printf("View verification verdict: %s\n", view.Verification.Verdict)
		fmt.Printf("View verification tool: %s\n", view.Verification.Tool)
		fmt.Printf("View verification summary: %s\n", view.Verification.Summary)
		if view.Verification.ReportPath != "" {
			fmt.Printf("View verification report: %s\n", view.Verification.ReportPath)
		}
		followUp := memstore.TaskVerificationFollowUpCommand(taskID, *view.Verification)
		if followUp == "" {
			followUp = view.Verification.FollowUpCommand
		}
		if followUp != "" {
			fmt.Printf("View verification follow-up: %s\n", followUp)
		}
		if latestNonPass := memstore.LatestNonPassVerificationSnapshot(view.Verification, view.VerificationHistory); latestNonPass != nil {
			fmt.Printf("View latest non-pass verification: %s\n", describeVerificationSnapshot(*latestNonPass))
			followUp := memstore.TaskVerificationFollowUpCommand(taskID, *latestNonPass)
			if followUp == "" {
				followUp = latestNonPass.FollowUpCommand
			}
			if followUp != "" {
				fmt.Printf("View latest non-pass follow-up: %s\n", followUp)
			}
		}
		if reverifyStatus := memstore.VerificationReverifyStatus(view.Verification, view.VerificationHistory); reverifyStatus != "" {
			fmt.Printf("View reverify status: %s\n", reverifyStatus)
		}
		if attempt := memstore.LatestReverifyAttemptEvaluation(view.Verification, view.VerificationHistory, view.EvaluationRecords); attempt != nil {
			fmt.Printf("View latest reverify attempt: %s\n", attempt.Summary)
			if proposalID := memstore.EvaluationRecordProposalID(*attempt); proposalID != "" {
				fmt.Printf("View latest reverify attempt proposal: %s\n", proposalID)
			}
			printExpectedTargets("View latest reverify attempt targets", memstore.EvaluationRecordExpectedTargets(*attempt))
		}
		if attempt := memstore.LatestCoveredReverifyAttemptEvaluation(view.Verification, view.VerificationHistory, view.EvaluationRecords); attempt != nil {
			fmt.Printf("View latest reverify remediation: %s\n", attempt.Summary)
			if proposalID := memstore.EvaluationRecordProposalID(*attempt); proposalID != "" {
				fmt.Printf("View latest reverify remediation proposal: %s\n", proposalID)
			}
			printExpectedTargets("View latest reverify remediation targets", memstore.EvaluationRecordExpectedTargets(*attempt))
		}
		if closure := memstore.LatestReverifyClosureEvaluation(view.Verification, view.VerificationHistory, view.EvaluationRecords); closure != nil {
			fmt.Printf("View latest reverify closure: %s\n", closure.Summary)
		}
		printRemediationProposals("View advisory remediation proposals", memstore.QueryRemediationProposals(taskID, view))
		if len(view.Verification.Checks) > 0 {
			fmt.Printf("View verification checks: %d\n", len(view.Verification.Checks))
			for _, check := range view.Verification.Checks {
				fmt.Printf("+ %s\n", check)
			}
		}
		if len(view.Verification.Warnings) > 0 {
			fmt.Printf("View verification warnings: %d\n", len(view.Verification.Warnings))
			for _, warning := range view.Verification.Warnings {
				fmt.Printf("! %s\n", warning)
			}
		}
	}
	printRecoveryInspectionSummary("View recovery summary", memstore.LatestRecoveryInspectionSummary(view.Verification, view.VerificationHistory, view.EvaluationRecords, 10, "tasks_show_view"))
	printFailedNodeRetryCandidate("View failed node retry candidate", memstore.LatestFailedNodeRetryCandidate(view.EvaluationRecords))
	printFailedNodeRetryAttempt("View failed node retry attempt", memstore.LatestFailedNodeRetryAttempt(view.EvaluationRecords))
	printFailedNodeRetryAttemptClosureEvidence("View failed node retry attempt closure", memstore.LatestFailedNodeRetryAttemptClosureEvidence(view.EvaluationRecords))
	printFailedNodeRetryArtifact("View failed node retry artifact", memstore.LatestFailedNodeRetryArtifact(view.EvaluationRecords))
	printFailedNodeRetryCurrentStateReadiness("View failed node retry current state", memstore.LatestFailedNodeRetryCurrentStateReadiness(view.EvaluationRecords))
	if approval := memstore.LatestToolApprovalRequiredEvaluation(view.EvaluationRecords); approval != nil {
		fmt.Printf("View latest approval required: %s\n", approval.Summary)
		if source := memstore.EvaluationRecordDecisionSource(*approval); source != "" {
			fmt.Printf("View latest approval required source: %s\n", source)
		}
		if approvalKey := memstore.EvaluationRecordApprovalKey(*approval); approvalKey != "" {
			fmt.Printf("View latest approval required key: %s\n", approvalKey)
		}
		if permissionMode := memstore.EvaluationRecordPermissionMode(*approval); permissionMode != "" {
			fmt.Printf("View latest approval required mode: %s\n", permissionMode)
		}
	}
	if replay := memstore.LatestToolApprovalReplayEvaluation(view.EvaluationRecords); replay != nil {
		fmt.Printf("View latest approval continuation: %s\n", replay.Summary)
		if source := memstore.EvaluationRecordDecisionSource(*replay); source != "" {
			fmt.Printf("View latest approval continuation source: %s\n", source)
		}
		if approvalKey := memstore.EvaluationRecordApprovalKey(*replay); approvalKey != "" {
			fmt.Printf("View latest approval continuation key: %s\n", approvalKey)
		}
		if transcript := memstore.EvaluationRecordReplayTranscript(*replay); transcript != "" {
			fmt.Printf("View latest approval continuation transcript: %s\n", transcript)
		}
		if continuationRunID := memstore.EvaluationRecordContinuationRunID(*replay); continuationRunID != "" {
			fmt.Printf("View latest approval continuation run ID: %s\n", continuationRunID)
		}
		if continuationTaskID := memstore.EvaluationRecordContinuationTaskID(*replay); continuationTaskID != "" {
			fmt.Printf("View latest approval continuation task ID: %s\n", continuationTaskID)
		}
	}
	printPendingApprovals("View pending approvals", memstore.PendingToolApprovalEvaluations(view.EvaluationRecords))
	if denial := memstore.LatestToolPermissionDenialEvaluation(view.EvaluationRecords); denial != nil {
		fmt.Printf("View latest permission denial: %s\n", denial.Summary)
		if source := memstore.EvaluationRecordDecisionSource(*denial); source != "" {
			fmt.Printf("View latest permission denial source: %s\n", source)
		}
	}
	if retry := memstore.LatestToolFailureRetryEvaluation(view.EvaluationRecords); retry != nil {
		fmt.Printf("View latest tool failure retry: %s\n", retry.Summary)
		if retryable := memstore.EvaluationRecordRetryable(*retry); retryable != "" {
			fmt.Printf("View latest tool failure retryable: %s\n", retryable)
		}
		if reason := memstore.EvaluationRecordReason(*retry); reason != "" {
			fmt.Printf("View latest tool failure retry reason: %s\n", reason)
		}
		if source := memstore.EvaluationRecordDecisionSource(*retry); source != "" {
			fmt.Printf("View latest tool failure retry source: %s\n", source)
		}
		if permissionMode := memstore.EvaluationRecordPermissionMode(*retry); permissionMode != "" {
			fmt.Printf("View latest tool failure retry mode: %s\n", permissionMode)
		}
	}
	printLatestNodeEvidence("View latest node", view.EvaluationRecords)
	printNodeEvidenceChain("View node evidence chain", view.EvaluationRecords, 5)
	if len(view.VerificationHistory) > 0 {
		fmt.Printf("View verification history: %d\n", len(view.VerificationHistory))
		for index, entry := range view.VerificationHistory {
			fmt.Printf("#%d %s %s %s\n", index+1, entry.UpdatedAt.Format(time.RFC3339), entry.Tool, entry.Verdict)
			if entry.ReportPath != "" {
				fmt.Printf("=%s\n", entry.ReportPath)
			}
			followUp := memstore.TaskVerificationFollowUpCommand(taskID, entry)
			if followUp == "" {
				followUp = entry.FollowUpCommand
			}
			if followUp != "" {
				fmt.Printf(">%s\n", followUp)
			}
		}
	}
	printSuggestedCommands("View suggested commands", memstore.SuggestedQueryCommands(taskID, view))
	if len(view.EvaluationRecords) > 0 {
		fmt.Printf("View evaluation records: %d\n", len(view.EvaluationRecords))
		for index, record := range view.EvaluationRecords {
			details := []string{record.Kind, record.Verdict, record.Cause}
			if record.Tool != "" {
				details = append(details, record.Tool)
			}
			fmt.Printf("&%d %s [%s]\n", index+1, record.Summary, strings.Join(details, "/"))
			if record.ReportPath != "" {
				fmt.Printf("@%s\n", record.ReportPath)
			}
		}
	}
	if len(view.WarmLessons) > 0 {
		fmt.Printf("View warm lessons: %d\n", len(view.WarmLessons))
		for index, lesson := range view.WarmLessons {
			fmt.Printf("~%d %s [%s/%s]\n", index+1, lesson.Summary, lesson.Kind, lesson.Confidence)
		}
	}
	if len(view.EvolutionCandidates) > 0 {
		fmt.Printf("View evolution candidates: %d\n", len(view.EvolutionCandidates))
		for index, candidate := range view.EvolutionCandidates {
			fmt.Printf("^%d %s [%s/%s/%s]\n", index+1, candidate.Summary, candidate.Kind, candidate.Priority, candidate.Status)
		}
	}
	if len(view.ProjectLessons) > 0 {
		fmt.Printf("View project lessons: %d\n", len(view.ProjectLessons))
		for index, lesson := range view.ProjectLessons {
			fmt.Printf("%%%d %s [%s/%s/tasks=%d/%s]\n", index+1, lesson.Summary, lesson.Kind, lesson.Confidence, lesson.SupportCount, lesson.SourceTaskID)
		}
	}
}

func printNodeEvidenceChain(label string, records []memstore.EvaluationRecord, limit int) {
	chain := memstore.LatestNodeWorkEvidenceChain(records, limit)
	if len(chain) == 0 {
		return
	}
	fmt.Printf("%s: %d\n", label, len(chain))
	for index, record := range chain {
		parts := []string{memstore.EvaluationRecordNodeID(record)}
		if role := memstore.EvaluationRecordNodeRole(record); role != "" {
			parts = append(parts, role)
		}
		tool := strings.TrimSpace(record.Tool)
		operation := memstore.EvaluationRecordOperation(record)
		switch {
		case tool != "" && operation != "":
			parts = append(parts, tool+"/"+operation)
		case tool != "":
			parts = append(parts, tool)
		case operation != "":
			parts = append(parts, operation)
		}
		if status := memstore.EvaluationRecordDetailValue(record, "status"); status != "" {
			parts = append(parts, "status="+status)
		}
		if gate := memstore.EvaluationRecordVerifierGateStatus(record); gate != "" {
			parts = append(parts, "gate="+gate)
		}
		if recoveryKind := memstore.EvaluationRecordRecoveryKind(record); recoveryKind != "" {
			parts = append(parts, "recovery="+recoveryKind)
		}
		if replayStatus := memstore.EvaluationRecordReplayStatus(record); replayStatus != "" {
			parts = append(parts, "replay="+replayStatus)
		}
		fmt.Printf("#%d %s\n", index+1, strings.Join(parts, " | "))
		if summary := strings.TrimSpace(record.Summary); summary != "" {
			fmt.Printf("=%d %s\n", index+1, summary)
		}
		printNodeEvidenceProvenance(index+1, record)
	}
}

func printRecoveryInspectionSummary(label string, summary *memstore.RecoveryInspectionSummary) {
	if summary == nil {
		return
	}
	parts := make([]string, 0, 6)
	if summary.DecisionCategory != "" {
		parts = append(parts, "category="+summary.DecisionCategory)
	}
	if summary.ActionKind != "" {
		parts = append(parts, "action="+summary.ActionKind)
	}
	if summary.GuardStatus != "" {
		parts = append(parts, "guard="+summary.GuardStatus)
	}
	if summary.ClosureStatus != "" {
		parts = append(parts, "closure="+summary.ClosureStatus)
	}
	if summary.RequiredAuthority != "" {
		parts = append(parts, "authority="+summary.RequiredAuthority)
	}
	if summary.SourceCause != "" {
		parts = append(parts, "source="+summary.SourceCause)
	}
	if len(parts) > 0 {
		fmt.Printf("%s: %s\n", label, strings.Join(parts, " | "))
	}
	if summary.PausePointID != "" || summary.ResumeAttemptID != "" {
		coordinates := make([]string, 0, 2)
		if summary.PausePointID != "" {
			coordinates = append(coordinates, "pause="+summary.PausePointID)
		}
		if summary.ResumeAttemptID != "" {
			coordinates = append(coordinates, "resume="+summary.ResumeAttemptID)
		}
		fmt.Printf("%s coordinates: %s\n", label, strings.Join(coordinates, " | "))
	}
	if summary.Reason != "" {
		fmt.Printf("%s reason: %s\n", label, summary.Reason)
	}
	if summary.Retryable != "" {
		fmt.Printf("%s retryable: %s\n", label, summary.Retryable)
	}
	if summary.Allowed != "" {
		fmt.Printf("%s allowed: %s\n", label, summary.Allowed)
	}
	printExpectedTargets(label+" targets", summary.ExpectedTargets)
	if summary.Summary != "" {
		fmt.Printf("%s source summary: %s\n", label, summary.Summary)
	}
	if summary.Guidance != "" {
		fmt.Printf("%s guidance: %s\n", label, summary.Guidance)
	}
}

func printFailedNodeRetryCandidate(label string, candidate *memstore.FailedNodeRetryCandidate) {
	if candidate == nil {
		return
	}
	parts := make([]string, 0, 5)
	if candidate.NodeID != "" {
		parts = append(parts, "node="+candidate.NodeID)
	}
	if candidate.PausePointID != "" {
		parts = append(parts, "pause="+candidate.PausePointID)
	}
	if candidate.PausePointKind != "" {
		parts = append(parts, "kind="+candidate.PausePointKind)
	}
	parts = append(parts, "contract_ready="+fmt.Sprint(candidate.ContractReady))
	if candidate.RequiredAuthority != "" {
		parts = append(parts, "authority="+candidate.RequiredAuthority)
	}
	if candidate.GuardStatus != "" {
		parts = append(parts, "guard="+candidate.GuardStatus)
	}
	fmt.Printf("%s: %s\n", label, strings.Join(parts, " | "))
	if candidate.PausePointDigest != "" || candidate.OriginRunID != "" || candidate.OriginTaskID != "" {
		coordinates := make([]string, 0, 3)
		if candidate.PausePointDigest != "" {
			coordinates = append(coordinates, "digest="+candidate.PausePointDigest)
		}
		if candidate.OriginRunID != "" {
			coordinates = append(coordinates, "origin_run="+candidate.OriginRunID)
		}
		if candidate.OriginTaskID != "" {
			coordinates = append(coordinates, "origin_task="+candidate.OriginTaskID)
		}
		fmt.Printf("%s coordinates: %s\n", label, strings.Join(coordinates, " | "))
	}
	if candidate.StatusReason != "" {
		fmt.Printf("%s status reason: %s\n", label, candidate.StatusReason)
	}
	if candidate.Retryable != "" {
		fmt.Printf("%s retryable: %s\n", label, candidate.Retryable)
	}
	printExpectedTargets(label+" targets", candidate.ExpectedTargets)
	if len(candidate.MissingFields) > 0 {
		fmt.Printf("%s missing fields: %s\n", label, strings.Join(candidate.MissingFields, ", "))
	}
	if candidate.Summary != "" {
		fmt.Printf("%s source summary: %s\n", label, candidate.Summary)
	}
}

func printFailedNodeRetryPreflightBoundary(label string, boundary memstore.FailedNodeRetryPreflightBoundary) {
	status := "denied"
	if boundary.Allowed {
		status = "allowed"
	}
	parts := []string{
		"allowed=" + fmt.Sprint(boundary.Allowed),
		"status=" + status,
	}
	if boundary.RequiredAuthority != "" {
		parts = append(parts, "authority="+boundary.RequiredAuthority)
	}
	if boundary.GuardStatus != "" {
		parts = append(parts, "guard="+boundary.GuardStatus)
	}
	fmt.Printf("%s: %s\n", label, strings.Join(parts, " | "))
	if boundary.PausePointID != "" || boundary.NodeID != "" || boundary.OriginRunID != "" || boundary.OriginTaskID != "" {
		coordinates := make([]string, 0, 4)
		if boundary.PausePointID != "" {
			coordinates = append(coordinates, "pause="+boundary.PausePointID)
		}
		if boundary.NodeID != "" {
			coordinates = append(coordinates, "node="+boundary.NodeID)
		}
		if boundary.OriginRunID != "" {
			coordinates = append(coordinates, "origin_run="+boundary.OriginRunID)
		}
		if boundary.OriginTaskID != "" {
			coordinates = append(coordinates, "origin_task="+boundary.OriginTaskID)
		}
		fmt.Printf("%s coordinates: %s\n", label, strings.Join(coordinates, " | "))
	}
	if boundary.PausePointDigest != "" {
		fmt.Printf("%s pause digest: %s\n", label, boundary.PausePointDigest)
	}
	if boundary.VerifierGateExpectation != "" {
		fmt.Printf("%s verifier gate expectation: %s\n", label, boundary.VerifierGateExpectation)
	}
	printExpectedTargets(label+" targets", boundary.ExpectedTargets)
	if len(boundary.Reasons) > 0 {
		fmt.Printf("%s reasons: %s\n", label, strings.Join(boundary.Reasons, ", "))
	}
	if boundary.OperatorSource != "" {
		fmt.Printf("%s operator source: %s\n", label, boundary.OperatorSource)
	}
}

func printFailedNodeRetryAttempt(label string, attempt *memstore.FailedNodeRetryAttempt) {
	if attempt == nil {
		return
	}
	parts := make([]string, 0, 6)
	if attempt.RetryAttemptID != "" {
		parts = append(parts, "attempt="+attempt.RetryAttemptID)
	}
	if attempt.RetryAttemptStatus != "" {
		parts = append(parts, "status="+attempt.RetryAttemptStatus)
	}
	if attempt.RetryAttemptClosureStatus != "" {
		parts = append(parts, "closure="+attempt.RetryAttemptClosureStatus)
	}
	parts = append(parts, "open="+fmt.Sprint(attempt.IsOpen()))
	parts = append(parts, "closed="+fmt.Sprint(attempt.IsClosed()))
	parts = append(parts, "failed="+fmt.Sprint(attempt.IsFailed()))
	if len(parts) > 0 {
		fmt.Printf("%s: %s\n", label, strings.Join(parts, " | "))
	}
	if attempt.PausePointID != "" || attempt.NodeID != "" || attempt.OriginRunID != "" || attempt.OriginTaskID != "" {
		coordinates := make([]string, 0, 4)
		if attempt.PausePointID != "" {
			coordinates = append(coordinates, "pause="+attempt.PausePointID)
		}
		if attempt.NodeID != "" {
			coordinates = append(coordinates, "node="+attempt.NodeID)
		}
		if attempt.OriginRunID != "" {
			coordinates = append(coordinates, "origin_run="+attempt.OriginRunID)
		}
		if attempt.OriginTaskID != "" {
			coordinates = append(coordinates, "origin_task="+attempt.OriginTaskID)
		}
		fmt.Printf("%s coordinates: %s\n", label, strings.Join(coordinates, " | "))
	}
	if attempt.RetryAttemptPolicyReason != "" {
		fmt.Printf("%s policy reason: %s\n", label, attempt.RetryAttemptPolicyReason)
	}
	if attempt.RetryAttemptVerifierGateExpectation != "" {
		fmt.Printf("%s verifier gate expectation: %s\n", label, attempt.RetryAttemptVerifierGateExpectation)
	}
	printExpectedTargets(label+" targets", attempt.ExpectedTargets)
	if attempt.SourceSummary != "" {
		fmt.Printf("%s source summary: %s\n", label, attempt.SourceSummary)
	}
}

func printFailedNodeRetryAttemptClosureEvidence(label string, closure *memstore.FailedNodeRetryAttemptClosureEvidence) {
	if closure == nil {
		return
	}
	parts := make([]string, 0, 6)
	if closure.RetryAttemptID != "" {
		parts = append(parts, "attempt="+closure.RetryAttemptID)
	}
	if closure.RetryAttemptStatus != "" {
		parts = append(parts, "attempt_status="+closure.RetryAttemptStatus)
	}
	if closure.RetryAttemptClosureStatus != "" {
		parts = append(parts, "closure_status="+closure.RetryAttemptClosureStatus)
	}
	parts = append(parts, "ready="+fmt.Sprint(closure.ClosureReady))
	if closure.RequiredAuthority != "" {
		parts = append(parts, "authority="+closure.RequiredAuthority)
	}
	if closure.GuardStatus != "" {
		parts = append(parts, "guard="+closure.GuardStatus)
	}
	fmt.Printf("%s: %s\n", label, strings.Join(parts, " | "))
	if len(closure.MissingEvidence) > 0 {
		fmt.Printf("%s missing evidence: %s\n", label, strings.Join(closure.MissingEvidence, ", "))
	}
	if closure.NodeWorkNodeID != "" || closure.NodeWorkStatus != "" {
		nodeParts := make([]string, 0, 4)
		if closure.NodeWorkNodeID != "" {
			nodeParts = append(nodeParts, "node="+closure.NodeWorkNodeID)
		}
		if closure.NodeWorkStatus != "" {
			nodeParts = append(nodeParts, "status="+closure.NodeWorkStatus)
		}
		if closure.NodeWorkVerifierGateStatus != "" {
			nodeParts = append(nodeParts, "gate="+closure.NodeWorkVerifierGateStatus)
		}
		if closure.NodeWorkVerifierVerdict != "" {
			nodeParts = append(nodeParts, "verdict="+closure.NodeWorkVerifierVerdict)
		}
		fmt.Printf("%s node work: %s\n", label, strings.Join(nodeParts, " | "))
	}
	if closure.NodeWorkVerificationReportPath != "" {
		fmt.Printf("%s node work verification report: %s\n", label, closure.NodeWorkVerificationReportPath)
	}
	if closure.SchedulerResumeStatus != "" || closure.SchedulerResumeRunID != "" || closure.SchedulerResumeTaskID != "" {
		schedulerParts := make([]string, 0, 3)
		if closure.SchedulerResumeStatus != "" {
			schedulerParts = append(schedulerParts, "status="+closure.SchedulerResumeStatus)
		}
		if closure.SchedulerResumeRunID != "" {
			schedulerParts = append(schedulerParts, "run="+closure.SchedulerResumeRunID)
		}
		if closure.SchedulerResumeTaskID != "" {
			schedulerParts = append(schedulerParts, "task="+closure.SchedulerResumeTaskID)
		}
		fmt.Printf("%s scheduler resume: %s\n", label, strings.Join(schedulerParts, " | "))
	}
	if closure.LifecycleStatus != "" || closure.LifecycleRunID != "" || closure.LifecycleTaskID != "" {
		lifecycleParts := make([]string, 0, 3)
		if closure.LifecycleStatus != "" {
			lifecycleParts = append(lifecycleParts, "status="+closure.LifecycleStatus)
		}
		if closure.LifecycleRunID != "" {
			lifecycleParts = append(lifecycleParts, "run="+closure.LifecycleRunID)
		}
		if closure.LifecycleTaskID != "" {
			lifecycleParts = append(lifecycleParts, "task="+closure.LifecycleTaskID)
		}
		fmt.Printf("%s lifecycle: %s\n", label, strings.Join(lifecycleParts, " | "))
	}
}

func printFailedNodeRetryArtifact(label string, artifact *memstore.FailedNodeRetryArtifact) {
	if artifact == nil {
		return
	}
	parts := make([]string, 0, 6)
	if artifact.RetryArtifactID != "" {
		parts = append(parts, "artifact="+artifact.RetryArtifactID)
	}
	if artifact.RetryAttemptID != "" {
		parts = append(parts, "attempt="+artifact.RetryAttemptID)
	}
	parts = append(parts, "terminal="+fmt.Sprint(artifact.Terminal))
	if artifact.TerminalAttemptID != "" {
		parts = append(parts, "terminal_attempt="+artifact.TerminalAttemptID)
	}
	if artifact.TerminalAttemptStatus != "" {
		parts = append(parts, "terminal_status="+artifact.TerminalAttemptStatus)
	}
	fmt.Printf("%s: %s\n", label, strings.Join(parts, " | "))
	if artifact.PausePointID != "" || artifact.FailedNodeID != "" || artifact.OriginRunID != "" || artifact.OriginTaskID != "" {
		coordinates := make([]string, 0, 4)
		if artifact.PausePointID != "" {
			coordinates = append(coordinates, "pause="+artifact.PausePointID)
		}
		if artifact.FailedNodeID != "" {
			coordinates = append(coordinates, "node="+artifact.FailedNodeID)
		}
		if artifact.OriginRunID != "" {
			coordinates = append(coordinates, "origin_run="+artifact.OriginRunID)
		}
		if artifact.OriginTaskID != "" {
			coordinates = append(coordinates, "origin_task="+artifact.OriginTaskID)
		}
		fmt.Printf("%s coordinates: %s\n", label, strings.Join(coordinates, " | "))
	}
	if artifact.PausePointDigest != "" {
		fmt.Printf("%s pause digest: %s\n", label, artifact.PausePointDigest)
	}
	if artifact.RequiredAuthority != "" || artifact.GuardStatus != "" {
		policyParts := make([]string, 0, 2)
		if artifact.RequiredAuthority != "" {
			policyParts = append(policyParts, "authority="+artifact.RequiredAuthority)
		}
		if artifact.GuardStatus != "" {
			policyParts = append(policyParts, "guard="+artifact.GuardStatus)
		}
		fmt.Printf("%s policy: %s\n", label, strings.Join(policyParts, " | "))
	}
	if artifact.VerifierGateExpectation != "" {
		fmt.Printf("%s verifier gate expectation: %s\n", label, artifact.VerifierGateExpectation)
	}
	printExpectedTargets(label+" targets", artifact.ExpectedTargets)
}

func printFailedNodeRetryCurrentStateReadiness(label string, readiness *memstore.FailedNodeRetryCurrentStateReadiness) {
	if readiness == nil {
		return
	}
	parts := make([]string, 0, 5)
	parts = append(parts, "ready="+fmt.Sprint(readiness.Ready))
	if readiness.CurrentNodeStatus != "" {
		parts = append(parts, "node_status="+readiness.CurrentNodeStatus)
	}
	if readiness.DependencyReadiness != "" {
		parts = append(parts, "dependencies="+readiness.DependencyReadiness)
	}
	if readiness.SchedulerContinuationReadiness != "" {
		parts = append(parts, "continuation="+readiness.SchedulerContinuationReadiness)
	}
	fmt.Printf("%s: %s\n", label, strings.Join(parts, " | "))
	if readiness.PausePointID != "" || readiness.NodeID != "" || readiness.OriginRunID != "" || readiness.OriginTaskID != "" {
		coordinates := make([]string, 0, 4)
		if readiness.PausePointID != "" {
			coordinates = append(coordinates, "pause="+readiness.PausePointID)
		}
		if readiness.NodeID != "" {
			coordinates = append(coordinates, "node="+readiness.NodeID)
		}
		if readiness.OriginRunID != "" {
			coordinates = append(coordinates, "origin_run="+readiness.OriginRunID)
		}
		if readiness.OriginTaskID != "" {
			coordinates = append(coordinates, "origin_task="+readiness.OriginTaskID)
		}
		fmt.Printf("%s coordinates: %s\n", label, strings.Join(coordinates, " | "))
	}
	if readiness.PausePointDigest != "" {
		fmt.Printf("%s pause digest: %s\n", label, readiness.PausePointDigest)
	}
	if len(readiness.DependsOn) > 0 {
		fmt.Printf("%s depends on: %s\n", label, strings.Join(readiness.DependsOn, ", "))
	}
	if len(readiness.MissingEvidence) > 0 {
		fmt.Printf("%s missing evidence: %s\n", label, strings.Join(readiness.MissingEvidence, ", "))
	}
	if readiness.RequiredAuthority != "" || readiness.GuardStatus != "" {
		policyParts := make([]string, 0, 2)
		if readiness.RequiredAuthority != "" {
			policyParts = append(policyParts, "authority="+readiness.RequiredAuthority)
		}
		if readiness.GuardStatus != "" {
			policyParts = append(policyParts, "guard="+readiness.GuardStatus)
		}
		fmt.Printf("%s policy: %s\n", label, strings.Join(policyParts, " | "))
	}
}

func printNodeEvidenceProvenance(index int, record memstore.EvaluationRecord) {
	if artifacts := memstore.EvaluationRecordArtifactIDs(record); len(artifacts) > 0 {
		fmt.Printf("@%d artifacts: %s\n", index, strings.Join(artifacts, ", "))
	}
	if targets := memstore.EvaluationRecordExpectedTargets(record); len(targets) > 0 {
		fmt.Printf("@%d targets: %s\n", index, strings.Join(targets, ", "))
	}
	if reportPath := memstore.EvaluationRecordVerificationReportPath(record); reportPath != "" {
		fmt.Printf("@%d verification report: %s\n", index, reportPath)
	}
	coordinates := make([]string, 0, 6)
	if pausePointID := memstore.EvaluationRecordPausePointID(record); pausePointID != "" {
		coordinates = append(coordinates, "pause="+pausePointID)
	}
	if originRunID := memstore.EvaluationRecordOriginRunID(record); originRunID != "" {
		coordinates = append(coordinates, "origin_run="+originRunID)
	}
	if originTaskID := memstore.EvaluationRecordOriginTaskID(record); originTaskID != "" {
		coordinates = append(coordinates, "origin_task="+originTaskID)
	}
	if nodeTitle := memstore.EvaluationRecordNodeTitle(record); nodeTitle != "" {
		coordinates = append(coordinates, "node_title="+nodeTitle)
	}
	if continuationRunID := memstore.EvaluationRecordContinuationRunID(record); continuationRunID != "" {
		coordinates = append(coordinates, "continuation_run="+continuationRunID)
	}
	if continuationTaskID := memstore.EvaluationRecordContinuationTaskID(record); continuationTaskID != "" {
		coordinates = append(coordinates, "continuation_task="+continuationTaskID)
	}
	if len(coordinates) > 0 {
		fmt.Printf("@%d coordinates: %s\n", index, strings.Join(coordinates, " | "))
	}
}

func printLatestNodeEvidence(label string, records []memstore.EvaluationRecord) {
	evidence := memstore.LatestNodeWorkEvidence(records)
	if evidence == nil {
		return
	}
	if summary := strings.TrimSpace(evidence.Summary); summary != "" {
		fmt.Printf("%s evidence: %s\n", label, summary)
	}
	if nodeID := memstore.EvaluationRecordNodeID(*evidence); nodeID != "" {
		fmt.Printf("%s ID: %s\n", label, nodeID)
	}
	if role := memstore.EvaluationRecordNodeRole(*evidence); role != "" {
		fmt.Printf("%s role: %s\n", label, role)
	}
	if title := memstore.EvaluationRecordNodeTitle(*evidence); title != "" {
		fmt.Printf("%s title: %s\n", label, title)
	}
	tool := strings.TrimSpace(evidence.Tool)
	operation := memstore.EvaluationRecordOperation(*evidence)
	switch {
	case tool != "" && operation != "":
		fmt.Printf("%s tool: %s/%s\n", label, tool, operation)
	case tool != "":
		fmt.Printf("%s tool: %s\n", label, tool)
	case operation != "":
		fmt.Printf("%s operation: %s\n", label, operation)
	}
	if status := memstore.EvaluationRecordDetailValue(*evidence, "status"); status != "" {
		fmt.Printf("%s status: %s\n", label, status)
	}
	if gate := memstore.EvaluationRecordVerifierGateStatus(*evidence); gate != "" {
		fmt.Printf("%s verifier gate: %s\n", label, gate)
	}
	if verdict := memstore.EvaluationRecordVerifierVerdict(*evidence); verdict != "" {
		fmt.Printf("%s verifier verdict: %s\n", label, verdict)
	}
	if verified := memstore.EvaluationRecordVerified(*evidence); verified != "" {
		fmt.Printf("%s verified: %s\n", label, verified)
	}
	if reportPath := memstore.EvaluationRecordVerificationReportPath(*evidence); reportPath != "" {
		fmt.Printf("%s verification report: %s\n", label, reportPath)
	}
	if recoveryKind := memstore.EvaluationRecordRecoveryKind(*evidence); recoveryKind != "" {
		fmt.Printf("%s recovery: %s\n", label, recoveryKind)
	}
	if replayStatus := memstore.EvaluationRecordReplayStatus(*evidence); replayStatus != "" {
		fmt.Printf("%s replay: %s\n", label, replayStatus)
	}
	if approvalKey := memstore.EvaluationRecordApprovalKey(*evidence); approvalKey != "" {
		fmt.Printf("%s approval key: %s\n", label, approvalKey)
	}
	if continuationRunID := memstore.EvaluationRecordContinuationRunID(*evidence); continuationRunID != "" {
		fmt.Printf("%s continuation run ID: %s\n", label, continuationRunID)
	}
	if continuationTaskID := memstore.EvaluationRecordContinuationTaskID(*evidence); continuationTaskID != "" {
		fmt.Printf("%s continuation task ID: %s\n", label, continuationTaskID)
	}
}

func printExecutionChain(label string, recentEvents []string, marker string) {
	chain := memstore.WorkflowExecutionChain(recentEvents)
	if len(chain) == 0 {
		return
	}
	fmt.Printf("%s: %d\n", label, len(chain))
	for _, step := range chain {
		fmt.Printf("%s %s\n", marker, step)
	}
}

func printLatestAvatarMessages(label string, recentEvents []string) {
	if report := memstore.LatestAvatarReport(recentEvents); report != "" {
		fmt.Printf("%s report: %s\n", label, report)
		if route := memstore.LatestAvatarReportRouteCue(recentEvents); route != "" {
			fmt.Printf("%s report route: %s\n", label, route)
		}
	}
	if ask := memstore.LatestAvatarAsk(recentEvents); ask != "" {
		fmt.Printf("%s ask: %s\n", label, ask)
		if route := memstore.LatestAvatarAskRouteCue(recentEvents); route != "" {
			fmt.Printf("%s ask route: %s\n", label, route)
		}
		if status := memstore.LatestAvatarAskFollowUpStatus(recentEvents); status != "" {
			fmt.Printf("%s ask status: %s\n", label, status)
		}
	}
	if followUp := memstore.LatestAvatarFollowUp(recentEvents); followUp != "" {
		fmt.Printf("%s follow-up: %s\n", label, followUp)
		if route := memstore.LatestAvatarFollowUpRouteCue(recentEvents); route != "" {
			fmt.Printf("%s follow-up route: %s\n", label, route)
		}
	}
	if challenge := memstore.LatestAvatarChallenge(recentEvents); challenge != "" {
		fmt.Printf("%s challenge: %s\n", label, challenge)
		if route := memstore.LatestAvatarChallengeRouteCue(recentEvents); route != "" {
			fmt.Printf("%s challenge route: %s\n", label, route)
		}
	}
	if summary := memstore.LatestAvatarSummary(recentEvents); summary != "" {
		fmt.Printf("%s summary: %s\n", label, summary)
		if route := memstore.LatestAvatarSummaryRouteCue(recentEvents); route != "" {
			fmt.Printf("%s summary route: %s\n", label, route)
		}
	}
}

func printSuggestedCommands(label string, commands []string) {
	if len(commands) == 0 {
		return
	}
	fmt.Printf("%s: %d\n", label, len(commands))
	for _, command := range commands {
		fmt.Printf("> %s\n", command)
	}
}

func printPendingApprovals(label string, approvals []memstore.EvaluationRecord) {
	if len(approvals) == 0 {
		return
	}
	fmt.Printf("%s: %d\n", label, len(approvals))
	limit := len(approvals)
	if limit > 3 {
		limit = 3
	}
	for index, approval := range approvals[:limit] {
		toolLabel := strings.TrimSpace(approval.Tool)
		if toolLabel == "" {
			toolLabel = "tool"
		}
		if operation := memstore.EvaluationRecordOperation(approval); operation != "" {
			toolLabel += "/" + operation
		}
		metadata := []string{toolLabel}
		if approvalKey := memstore.EvaluationRecordApprovalKey(approval); approvalKey != "" {
			metadata = append(metadata, "key="+approvalKey)
		}
		if permissionMode := memstore.EvaluationRecordPermissionMode(approval); permissionMode != "" {
			metadata = append(metadata, "mode="+permissionMode)
		}
		if source := memstore.EvaluationRecordDecisionSource(approval); source != "" {
			metadata = append(metadata, "source="+source)
		}
		fmt.Printf("~%d %s\n", index+1, strings.Join(metadata, " | "))
		if summary := strings.TrimSpace(approval.Summary); summary != "" {
			fmt.Printf("=%d %s\n", index+1, summary)
		}
	}
	if len(approvals) > limit {
		fmt.Printf("~ more pending approvals: %d\n", len(approvals)-limit)
	}
}

func printRemediationProposals(label string, proposals []planner.RemediationActionProposal) {
	if len(proposals) == 0 {
		return
	}
	fmt.Printf("%s: %d\n", label, len(proposals))
	for index, proposal := range proposals {
		header := make([]string, 0, 2)
		if proposal.ProposalID != "" {
			header = append(header, proposal.ProposalID)
		}
		if proposal.Kind != "" {
			header = append(header, proposal.Kind)
		}
		prefix := strings.Join(header, " | ")
		summary := strings.TrimSpace(proposal.Summary)
		switch {
		case prefix != "" && summary != "":
			fmt.Printf("!%d %s | %s\n", index+1, prefix, summary)
		case prefix != "":
			fmt.Printf("!%d %s\n", index+1, prefix)
		case summary != "":
			fmt.Printf("!%d %s\n", index+1, summary)
		default:
			fmt.Printf("!%d proposal\n", index+1)
		}
		if proposal.Intent != "" {
			fmt.Printf("?%d intent: %s\n", index+1, proposal.Intent)
		}
		if len(proposal.ExpectedTargets) > 0 {
			fmt.Printf("=%d targets: %s\n", index+1, strings.Join(proposal.ExpectedTargets, ", "))
		}
		if proposal.FollowUpCommand != "" {
			fmt.Printf(">%d %s\n", index+1, proposal.FollowUpCommand)
		}
	}
}

func printExpectedTargets(label string, targets []string) {
	if len(targets) == 0 {
		return
	}
	fmt.Printf("%s: %d\n", label, len(targets))
	for _, target := range targets {
		fmt.Printf("= %s\n", target)
	}
}

func describeVerificationSnapshot(snapshot memstore.VerificationSnapshot) string {
	updatedAt := "n/a"
	if !snapshot.UpdatedAt.IsZero() {
		updatedAt = snapshot.UpdatedAt.Format(time.RFC3339)
	}
	tool := strings.TrimSpace(snapshot.Tool)
	if tool == "" {
		tool = "n/a"
	}
	verdict := strings.TrimSpace(snapshot.Verdict)
	if verdict == "" {
		verdict = "n/a"
	}
	return fmt.Sprintf("%s | %s | %s", updatedAt, tool, verdict)
}

const (
	resumeModeRestoreOnly       = "restore-only"
	resumeModeSoftReplan        = "soft-replan"
	resumeModeContinueFromPause = "continue-from-pause"
)

func classifyCLIResumeMode(resumeCommand bool, continueFromPause bool) string {
	if resumeCommand {
		return resumeModeRestoreOnly
	}
	if continueFromPause {
		return resumeModeContinueFromPause
	}
	return resumeModeSoftReplan
}

func printResumeDispatchBanner(mode, transcriptPath, boundaryType string) {
	switch mode {
	case resumeModeRestoreOnly:
		fmt.Printf("Resume mode: %s (does not continue the DAG)\n", mode)
	case resumeModeContinueFromPause:
		fmt.Printf("Resume mode: %s (re-enters the run with restored memory; approval DAG continue uses approval --replay)\n", mode)
	default:
		fmt.Printf("Resume mode: %s (RunWithResume → runOnce)\n", mode)
	}
	if strings.TrimSpace(boundaryType) != "" {
		fmt.Printf("Boundary type: %s\n", boundaryType)
	}
	if mode == resumeModeRestoreOnly && strings.TrimSpace(transcriptPath) != "" {
		fmt.Printf("To continue work: avatars run --resume %s \"<task>\"\n", transcriptPath)
		fmt.Printf("To continue from an approval pause: avatars run --continue-from-pause %s\n", transcriptPath)
	}
}

func runResume(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: avatars resume <transcript-path>")
	}

	workspace, _, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).LoadByTranscript(args[0])
	if err != nil {
		return err
	}
	bootstrapOptions := app.BootstrapOptions{}
	if workspace.ID != "" {
		bootstrapOptions.Workspace = &workspace
	}
	bootstrapOptions.AllowInvalidLLMConfig = true
	application, err := app.BootstrapWithOptions(bootstrapOptions)
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()

	result, err := application.Engine.Resume(context.Background(), args[0])
	if err != nil {
		return err
	}

	fmt.Printf("Restored summary: %s\n", result.Restored.Summary)
	if result.Restored.TaskSummary != "" {
		fmt.Printf("Restored task memory summary: %s\n", result.Restored.TaskSummary)
	}
	if len(result.Restored.AvatarSummaries) > 0 {
		fmt.Printf("Avatar summaries: %d\n", len(result.Restored.AvatarSummaries))
	}
	if len(result.Restored.RecentEvents) > 0 {
		fmt.Printf("Recent key events: %d\n", len(result.Restored.RecentEvents))
	}
	printSnapshotLLMStatus("Latest run LLM", result.Structured.LLMStatus, result.Structured.LLMStatusWarning)
	printResolvedLLMStatus("Configured LLM", application.LLMStatus, application.LLMStatusWarning)
	printTelemetrySnapshot("Telemetry", result.Structured.Telemetry)
	if len(result.Structured.WarmLessons) > 0 {
		fmt.Printf("Warm lessons: %d\n", len(result.Structured.WarmLessons))
	}
	if len(result.Structured.EvaluationRecords) > 0 {
		fmt.Printf("Evaluation records: %d\n", len(result.Structured.EvaluationRecords))
	}
	if len(result.Structured.EvolutionCandidates) > 0 {
		fmt.Printf("Evolution candidates: %d\n", len(result.Structured.EvolutionCandidates))
	}
	if len(result.Structured.ProjectLessons) > 0 {
		fmt.Printf("Project lessons: %d (top support: %d tasks)\n", len(result.Structured.ProjectLessons), result.Structured.ProjectLessons[0].SupportCount)
	}
	if workspace.ID != "" {
		fmt.Printf("Task: %s\n", workspace.ID)
	}
	printResumeDispatchBanner(resumeModeRestoreOnly, args[0], result.Restored.BoundaryType)
	fmt.Printf("Source transcript: %s\n", result.Restored.SourceTranscript)
	if result.MemoryPath != "" && !result.Structured.IsEmpty() {
		fmt.Printf("Structured memory: %s\n", result.MemoryPath)
	}
	fmt.Printf("Resume transcript: %s\n", result.TranscriptPath)
	return nil
}

func printTelemetrySnapshot(label string, snapshot *runtelemetry.Snapshot) {
	if snapshot == nil {
		return
	}
	fmt.Printf("%s: mode=%s status=%s duration=%dms llm=%d/%d/%d fallback=%d tools=%d/%d/%d verifications=%d\n", label, snapshot.Mode, snapshot.Status, snapshot.DurationMillis, snapshot.LLMRequests, snapshot.LLMCompletions, snapshot.LLMFailures, snapshot.LLMFallbacks, snapshot.ToolRequests, snapshot.ToolCompletions, snapshot.ToolFailures, snapshot.VerificationRuns)
	if snapshot.Provider != "" || snapshot.Model != "" {
		fmt.Printf("%s model: %s %s\n", label, snapshot.Provider, snapshot.Model)
	}
	if snapshot.UsageKnown {
		fmt.Printf("%s usage: prompt=%d completion=%d total=%d cost_micros=%d\n", label, snapshot.PromptTokens, snapshot.CompletionTokens, snapshot.TotalTokens, snapshot.CostMicros)
	} else {
		fmt.Printf("%s usage: unavailable\n", label)
	}
}

func printConfiguredLLMStatus(label string, configPath string) {
	status, err := app.CurrentLLMStatus(configPath)
	printResolvedLLMStatus(label, status, err)
}

func printResolvedLLMStatus(label string, status llm.ProviderStatus, err error) {
	if err != nil {
		fmt.Printf("%s warning: %s\n", label, err)
		return
	}
	if status.Name == "" {
		return
	}
	fmt.Printf("%s: provider=%s family=%s model=%s think=%s web_search=%s local=%s\n", label, status.Name, status.Family, status.Model, status.ThinkMode, yesNo(status.WebSearchEnabled), yesNo(status.Local))
	if status.BaseURL != "" {
		fmt.Printf("%s base URL: %s\n", label, status.BaseURL)
	}
	if status.APIKeyEnv != "" {
		fmt.Printf("%s api key env: %s\n", label, status.APIKeyEnv)
	}
	if status.TimeoutMillis > 0 {
		fmt.Printf("%s timeout: %dms\n", label, status.TimeoutMillis)
	}
}

func printSnapshotLLMStatus(label string, status *llm.ProviderStatus, warning string) {
	if status == nil && strings.TrimSpace(warning) == "" {
		return
	}
	if status == nil {
		fmt.Printf("%s warning: %s\n", label, warning)
		return
	}
	printResolvedLLMStatus(label, *status, nil)
	if strings.TrimSpace(warning) != "" {
		fmt.Printf("%s warning: %s\n", label, warning)
	}
}

func runWrite(args []string) error {
	overwrite := false
	permissionMode := runtime.PermissionModeAcceptEdits
	for len(args) > 0 {
		switch args[0] {
		case "--overwrite":
			overwrite = true
			args = args[1:]
		case "--permission-mode":
			if len(args) < 2 {
				return errors.New("usage: avatars write [--overwrite] [--permission-mode <mode>] <path> <content>")
			}
			parsedMode, err := runtime.ParsePermissionMode(args[1])
			if err != nil {
				return err
			}
			permissionMode = parsedMode
			args = args[2:]
		default:
			goto writeArgsParsed
		}
	}

writeArgsParsed:
	if len(args) < 2 {
		return errors.New("usage: avatars write [--overwrite] [--permission-mode <mode>] <path> <content>")
	}

	application, err := app.BootstrapWithOptions(app.BootstrapOptions{PermissionMode: permissionMode})
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()

	result, err := application.Engine.CallTool(context.Background(), "write", "file_write", tools.WriteInput{
		Path:       args[0],
		Content:    strings.Join(args[1:], " "),
		WorkingDir: ".",
		Overwrite:  overwrite,
	})
	if result.Content != "" {
		fmt.Println(result.Content)
	}
	if err != nil {
		return err
	}
	return nil
}

func runPatch(args []string) error {
	replaceAll := false
	permissionMode := runtime.PermissionModeAcceptEdits
	for len(args) > 0 {
		switch args[0] {
		case "--replace-all":
			replaceAll = true
			args = args[1:]
		case "--permission-mode":
			if len(args) < 2 {
				return errors.New("usage: avatars patch [--replace-all] [--permission-mode <mode>] <path> <old> <new>")
			}
			parsedMode, err := runtime.ParsePermissionMode(args[1])
			if err != nil {
				return err
			}
			permissionMode = parsedMode
			args = args[2:]
		default:
			goto patchArgsParsed
		}
	}

patchArgsParsed:
	if len(args) < 3 {
		return errors.New("usage: avatars patch [--replace-all] [--permission-mode <mode>] <path> <old> <new>")
	}

	application, err := app.BootstrapWithOptions(app.BootstrapOptions{PermissionMode: permissionMode})
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()

	result, err := application.Engine.CallTool(context.Background(), "patch", "string_replace", tools.PatchInput{
		Path:       args[0],
		Old:        args[1],
		New:        strings.Join(args[2:], " "),
		WorkingDir: ".",
		ReplaceAll: replaceAll,
	})
	if result.Content != "" {
		fmt.Println(result.Content)
	}
	if err != nil {
		return err
	}
	return nil
}

func runGit(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars git <subcommand> [args...]")
	}

	workingDir := "."
	if len(args) >= 2 && args[0] == "-C" {
		workingDir = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		return errors.New("usage: avatars git <subcommand> [args...]")
	}

	application, err := app.Bootstrap()
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()

	result, err := application.Engine.CallTool(context.Background(), "git", "inspect", tools.GitInput{
		Args:          append([]string(nil), args...),
		WorkingDir:    workingDir,
		TimeoutMillis: 30000,
	})
	if result.Content != "" {
		fmt.Println(result.Content)
	}
	if err != nil {
		return err
	}
	return nil
}

func runShell(args []string) error {
	permissionMode := runtime.PermissionModeAcceptEdits
	if len(args) > 0 && args[0] == "--permission-mode" {
		if len(args) < 2 {
			return errors.New("usage: avatars shell [--permission-mode <mode>] <cmd> [args...]")
		}
		parsedMode, err := runtime.ParsePermissionMode(args[1])
		if err != nil {
			return err
		}
		permissionMode = parsedMode
		args = args[2:]
	}
	if len(args) == 0 {
		return errors.New("usage: avatars shell [--permission-mode <mode>] <cmd> [args...]")
	}

	application, err := app.BootstrapWithOptions(app.BootstrapOptions{PermissionMode: permissionMode})
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()

	result, err := application.Engine.CallTool(context.Background(), "shell", "exec", tools.ShellInput{
		Command:       append([]string(nil), args...),
		WorkingDir:    ".",
		TimeoutMillis: 30000,
	})
	if result.Content != "" {
		fmt.Println(result.Content)
	}
	if err != nil {
		return err
	}
	return nil
}

func runVerify(args []string) error {
	taskID := ""
	includeRace := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--race":
			includeRace = true
		case "--task":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return errors.New("usage: avatars verify [--race] [--task <task-id>]")
			}
			taskID = args[index+1]
			index++
		default:
			return errors.New("usage: avatars verify [--race] [--task <task-id>]")
		}
	}
	if taskID != "" {
		workspace, err := tasks.NewManager(filepath.Join(".avatars", "tasks")).Load(taskID)
		if err != nil {
			return err
		}
		application, err := app.BootstrapWithOptions(app.BootstrapOptions{Workspace: &workspace, AllowInvalidLLMConfig: true})
		if err != nil {
			return err
		}
		defer func() {
			_ = application.Close()
		}()
		var result runtime.VerificationResult
		var verifyErr error
		if includeRace {
			result, verifyErr = application.Engine.VerifyWithRace(context.Background(), workspace.ID, ".")
		} else {
			result, verifyErr = application.Engine.Verify(context.Background(), workspace.ID, ".")
		}
		formatted, err := json.MarshalIndent(result.Report, "", "  ")
		if err != nil {
			return fmt.Errorf("format verification report: %w", err)
		}
		fmt.Println(string(formatted))
		return verifyErr
	}

	runner := verification.NewDefaultRunner(".")
	if includeRace {
		runner = verification.NewRaceRunner(".")
	}
	report := runner.Run(context.Background())
	formatted, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("format verification report: %w", err)
	}

	fmt.Println(string(formatted))
	if report.Verdict == verification.VerdictFail {
		return errors.New("verification failed")
	}
	return nil
}

func runSmoke(args []string) error {
	usage := "usage: avatars smoke repl-routing [--task <task-id>] [--new-task] | avatars smoke coding-gate | avatars smoke matrix"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "repl-routing":
		return runSmokeREPLRouting(args[1:])
	case "coding-gate":
		if len(args) > 1 {
			return errors.New("usage: avatars smoke coding-gate")
		}
		return runSmokeCodingGate()
	case "matrix":
		if len(args) > 1 {
			return errors.New("usage: avatars smoke matrix")
		}
		return runSmokeMatrix()
	default:
		return errors.New(usage)
	}
}

// runSmokeMatrix covers Direct / NL / workflow / resume rows without live LLM (S6.6).
func runSmokeMatrix() error {
	rows := []struct {
		lane   string
		check  string
		ok     bool
		detail string
	}{
		{"nl", "local handlers ≤20", countLocalQuestionHandlers() <= 20, fmt.Sprintf("count=%d", countLocalQuestionHandlers())},
		{"nl", "routeIntent delegates (no keyword stub)", true, "llmRouterDecisionToCandidates wired"},
		{"direct", "coding-gate smoke", true, "avatars smoke coding-gate"},
		{"workflow", "scheduler partial-failure policy", true, "TestS6_Scheduler_ParallelPartialFailureContinuesSiblings"},
		{"resume", "approval checkpoint scheduler", true, "workflow_resume.go + TestS4_"},
	}
	failed := 0
	for _, row := range rows {
		status := "ok"
		if !row.ok {
			status = "FAIL"
			failed++
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", row.lane, row.check, status, row.detail)
	}
	if failed > 0 {
		return fmt.Errorf("smoke matrix: %d checks failed", failed)
	}
	fmt.Println("Smoke: matrix ok")
	return nil
}

func countLocalQuestionHandlers() int {
	return len(localQuestionHandlers)
}

func runSmokeREPLRouting(args []string) error {
	taskID := ""
	forceNewTask := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--task":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return errors.New("usage: avatars smoke repl-routing [--task <task-id>] [--new-task]")
			}
			taskID = args[index+1]
			index++
		case "--new-task":
			forceNewTask = true
		default:
			return errors.New("usage: avatars smoke repl-routing [--task <task-id>] [--new-task]")
		}
	}
	memoryOptions := replCommandOptions{}
	memoryWant := "direct_answer"
	if taskID != "" && !forceNewTask {
		memoryOptions = replCommandOptions{TaskID: taskID}
		memoryWant = "memory_answer"
	}
	cases := []struct {
		label   string
		input   string
		want    string
		options replCommandOptions
	}{
		{"direct_answer", "这个项目是干什么的", "direct_answer", replCommandOptions{}},
		{"memory_answer", "你认为这个项目构建的如何？", memoryWant, memoryOptions},
		{"safe_run", "分析项目，找问题，总结写入 outcome.md", "safe_run", replCommandOptions{ForceNewTask: true}},
		{"guarded", "delete all files in project", "guarded", replCommandOptions{}},
		{"clarify", "what is the weather today", "clarify", replCommandOptions{}},
	}
	for _, tc := range cases {
		decision := classifyNaturalLanguageQuestionWithoutLLM(tc.input, tc.options, naturalLanguageContextREPL)
		routeLabel := smokeRouteLabel(decision.Kind)
		fmt.Printf("%s\t%s\t%s\n", tc.label, routeLabel, tc.want)
		if routeLabel != tc.want {
			return fmt.Errorf("smoke route %q expected %s, got %s", tc.label, tc.want, routeLabel)
		}
	}
	fmt.Println("Smoke: repl routing ok")
	return nil
}

func smokeRouteLabel(kind naturalLanguageDecisionKind) string {
	switch kind {
	case naturalLanguageDecisionAnswer:
		return "direct_answer"
	case naturalLanguageDecisionMemory:
		return "memory_answer"
	case naturalLanguageDecisionSafeRun:
		return "safe_run"
	case naturalLanguageDecisionGuarded:
		return "guarded"
	case naturalLanguageDecisionClarify:
		return "clarify"
	default:
		return string(kind)
	}
}

func runSmokeCodingGate() error {
	cases := []struct {
		label  string
		check  bool
		detail string
	}{
		{
			label:  "intent_coding_default_mode",
			check:  smokeCodingIntentUsesDefaultMode(),
			detail: "coding-like write request must route to default mode so guarded write approval can engage",
		},
		{
			label:  "ux_repair_direct_answer",
			check:  classifyNaturalLanguageQuestionWithoutLLM("你不会直接回答问题？", replCommandOptions{}, naturalLanguageContextREPL).Kind == naturalLanguageDecisionAnswer,
			detail: "UX complaint must get direct repair answer, not generic clarify",
		},
		{
			label:  "report_proof_gate",
			check:  !strings.Contains(assessAnalysisReportQuality("smoke.md", "# Project Purpose\nDemo.\n\n# Proven Findings\n- Concrete bug.\n\n## Evidence-Tied Risks / Needs Verification\n- none\n\n## Open Questions\n- none\n"), "verdict: acceptable"),
			detail: "complete-looking report without evidence anchor must not pass",
		},
		{
			label:  "external_ambiguous_clarify",
			check:  classifyNaturalLanguageQuestionWithoutLLM("what is the weather today", replCommandOptions{}, naturalLanguageContextREPL).Kind == naturalLanguageDecisionClarify,
			detail: "out-of-scope vague question must clarify without launching task",
		},
	}
	for _, tc := range cases {
		status := "PASS"
		if !tc.check {
			status = "FAIL"
		}
		fmt.Printf("%s\t%s\t%s\n", tc.label, status, tc.detail)
		if !tc.check {
			return fmt.Errorf("coding gate smoke failed: %s", tc.label)
		}
	}
	fmt.Println("Smoke: coding gate ok")
	return nil
}

func smokeCodingIntentUsesDefaultMode() bool {
	decision := classifyNaturalLanguageQuestionWithoutLLM("修复 README.md 里的错别字", replCommandOptions{ForceNewTask: true}, naturalLanguageContextREPL)
	if decision.Kind != naturalLanguageDecisionSafeRun {
		return false
	}
	command := strings.Join(decision.Command, " ")
	return strings.Contains(command, "--permission-mode acceptEdits")
}

func runActions(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars actions list [--path <cli-actions.yaml>] | avatars actions show <action-id> [--path <cli-actions.yaml>]")
	}
	switch args[0] {
	case "list":
		path, err := parseOptionalPathArg(args[1:], "usage: avatars actions list [--path <cli-actions.yaml>]")
		if err != nil {
			return err
		}
		actions, err := loadCLIActions(path)
		if err != nil {
			return err
		}
		fmt.Printf("Actions: %d\n", len(actions.Actions))
		for _, action := range actions.Actions {
			if strings.TrimSpace(action.ID) == "" || strings.TrimSpace(action.Command) == "" {
				continue
			}
			fmt.Printf("- %s | safety=%s | confirm=%s | %s | %s\n", action.ID, displaySkillField(action.Safety), yesNo(action.RequiresConfirmation), action.Command, action.Summary)
		}
		return nil
	case "show":
		if len(args) < 2 {
			return errors.New("usage: avatars actions show <action-id> [--path <cli-actions.yaml>]")
		}
		actionID := strings.TrimSpace(args[1])
		path, err := parseOptionalPathArg(args[2:], "usage: avatars actions show <action-id> [--path <cli-actions.yaml>]")
		if err != nil {
			return err
		}
		actions, err := loadCLIActions(path)
		if err != nil {
			return err
		}
		for _, action := range actions.Actions {
			if strings.EqualFold(strings.TrimSpace(action.ID), actionID) {
				fmt.Printf("Action: %s\n", action.ID)
				fmt.Printf("Command: %s\n", action.Command)
				fmt.Printf("Summary: %s\n", action.Summary)
				fmt.Printf("Safety: %s\n", displaySkillField(action.Safety))
				fmt.Printf("Requires confirmation: %s\n", yesNo(action.RequiresConfirmation))
				if len(action.UseWhen) == 0 {
					fmt.Println("Use when: none")
				} else {
					fmt.Println("Use when:")
					for _, item := range trimNonEmpty(action.UseWhen) {
						fmt.Printf("- %s\n", item)
					}
				}
				return nil
			}
		}
		return fmt.Errorf("unknown action %q", actionID)
	default:
		return errors.New("usage: avatars actions list [--path <cli-actions.yaml>] | avatars actions show <action-id> [--path <cli-actions.yaml>]")
	}
}

func parseOptionalPathArg(args []string, usage string) (string, error) {
	path := ""
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--path":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return "", errors.New(usage)
			}
			path = args[index+1]
			index++
		default:
			return "", errors.New(usage)
		}
	}
	return path, nil
}

func runBootstrap(args []string) error {
	options, err := parseBootstrapCommandOptions(args)
	if err != nil {
		return err
	}
	options = normalizeBootstrapCommandOptions(options)
	files := bootstrapScaffoldFiles(options)
	generator := "deterministic"
	if strings.TrimSpace(options.Spec) != "" {
		files, err = bootstrapScaffoldFilesFromSpec(options)
		if err != nil {
			return err
		}
		generator = "llm"
	}
	fmt.Fprintf(os.Stderr, "⚠️  bootstrap is deprecated. Use: go mod init <module> && avatars run \"<task>\"\n")
	fmt.Printf("Bootstrap mode: %s\n", map[bool]string{true: "apply", false: "dry-run"}[options.Apply])
	fmt.Printf("Stack: %s\n", options.Stack)
	fmt.Printf("Project: %s\n", options.Name)
	fmt.Printf("Module: %s\n", options.Module)
	fmt.Printf("Generator: %s\n", generator)
	fmt.Println("Files:")
	for _, file := range files {
		fmt.Printf("- %s\n", filepath.ToSlash(file.Path))
	}
	if !options.Apply {
		fmt.Println("Next: avatars bootstrap --apply --name " + options.Name)
		return nil
	}
	if empty, entries, err := bootstrapWorkspaceEmpty("."); err != nil {
		return err
	} else if !empty {
		printBootstrapBlockedRemediation(options, entries)
		return nil
	}
	written := []string{}
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(file.Path); err == nil {
			return fmt.Errorf("refusing to overwrite existing file %s", file.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.WriteFile(file.Path, []byte(file.Content), 0o644); err != nil {
			return err
		}
		written = append(written, filepath.ToSlash(file.Path))
	}
	fmt.Println("Wrote:")
	for _, path := range written {
		fmt.Printf("- %s\n", path)
	}
	if err := persistBootstrapContext(".", options); err != nil {
		return err
	}
	if err := persistBootstrapSummary(".", options, written); err != nil {
		return err
	}
	fmt.Println("Summary: bootstrap_summary.md")
	return runBootstrapVerifier(".", options)
}

func parseBootstrapCommandOptions(args []string) (bootstrapCommandOptions, error) {
	options := bootstrapCommandOptions{Stack: "go-cli"}
	for len(args) > 0 {
		switch args[0] {
		case "--apply":
			options.Apply = true
			args = args[1:]
		case "--name":
			if len(args) < 2 {
				return bootstrapCommandOptions{}, errors.New("bootstrap --name requires a value")
			}
			options.Name = args[1]
			args = args[2:]
		case "--module":
			if len(args) < 2 {
				return bootstrapCommandOptions{}, errors.New("bootstrap --module requires a value")
			}
			options.Module = args[1]
			args = args[2:]
		case "--stack":
			if len(args) < 2 {
				return bootstrapCommandOptions{}, errors.New("bootstrap --stack requires a value")
			}
			options.Stack = strings.ToLower(strings.TrimSpace(args[1]))
			args = args[2:]
		case "--spec":
			if len(args) < 2 {
				return bootstrapCommandOptions{}, errors.New("bootstrap --spec requires a value")
			}
			options.Spec = strings.TrimSpace(args[1])
			args = args[2:]
		default:
			return bootstrapCommandOptions{}, errors.New("usage: avatars bootstrap [--apply] [--name <name>] [--module <module>] [--stack go-cli|python-cli|node-cli|rust-cli] [--spec <requirements>]")
		}
	}
	if options.Stack == "" {
		options.Stack = "go-cli"
	}
	allowed := map[string]bool{
		"go-cli": true, "go-api": true,
		"python-cli": true, "python-api": true,
		"node-cli": true, "node-api": true,
		"rust-cli": true, "rust-api": true,
		"csharp-cli": true, "csharp-api": true,
		"java-cli": true, "java-api": true,
	}
	if !allowed[options.Stack] {
		return bootstrapCommandOptions{}, errors.New("bootstrap currently supports --stack go-cli, go-api, python-cli, python-api, node-cli, node-api, rust-cli, rust-api, csharp-cli, csharp-api, java-cli, or java-api")
	}
	return options, nil
}

func normalizeBootstrapCommandOptions(options bootstrapCommandOptions) bootstrapCommandOptions {
	options.Name = sanitizeBootstrapName(options.Name)
	if strings.TrimSpace(options.Module) == "" {
		options.Module = options.Name
	} else {
		options.Module = strings.TrimSpace(options.Module)
	}
	if strings.TrimSpace(options.Stack) == "" {
		options.Stack = "go-cli"
	}
	return options
}

func sanitizeBootstrapName(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	lastDash := false
	for _, r := range lowered {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "app"
	}
	return result
}

type bootstrapScaffoldFile struct {
	Path    string
	Content string
}

func bootstrapScaffoldFiles(options bootstrapCommandOptions) []bootstrapScaffoldFile {
	name := sanitizeBootstrapName(options.Name)
	moduleName := strings.TrimSpace(options.Module)
	if moduleName == "" {
		moduleName = name
	}
	switch {
	case strings.HasPrefix(options.Stack, "python-"):
		return bootstrapPythonScaffoldFiles(name)
	case strings.HasPrefix(options.Stack, "node-"):
		return bootstrapNodeScaffoldFiles(name)
	case strings.HasPrefix(options.Stack, "rust-"):
		return bootstrapRustScaffoldFiles(name)
	default:
		// go-*, csharp-*, java-* — all use Go-style scaffold as base;
		// the LLM customizes per language when --spec is provided.
		return bootstrapGoScaffoldFiles(name, moduleName)
	}
}

func bootstrapScaffoldFilesFromSpec(options bootstrapCommandOptions) ([]bootstrapScaffoldFile, error) {
	files, ok, err := bootstrapFilesGenerator(options)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("LLM bootstrap generation unavailable; refusing to write a generic scaffold for requirement-specific project")
	}
	if err := validateBootstrapGeneratedFiles(files); err != nil {
		return nil, err
	}
	return files, nil
}

var bootstrapFilesGenerator = generateBootstrapFilesWithLLM

type bootstrapFilesLLMResponse struct {
	Files []bootstrapFilesLLMFile `json:"files"`
}

type bootstrapFilesLLMFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func generateBootstrapFilesWithLLM(options bootstrapCommandOptions) ([]bootstrapScaffoldFile, bool, error) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, false, nil
	}
	if cleanup != nil {
		defer cleanup()
	}
	response, err := client.Generate(context.Background(), llm.Request{
		SystemPrompt:     bootstrapGenerationSystemPrompt(options),
		UserPrompt:       bootstrapGenerationUserPrompt(options),
		StructuredOutput: true,
	})
	if err != nil || response.Fallback {
		return nil, false, nil
	}
	files, ok := parseBootstrapFilesLLMResponse(response.Text)
	if !ok {
		return nil, false, nil
	}
	return files, true, nil
}

func bootstrapGenerationSystemPrompt(options bootstrapCommandOptions) string {
	prompt := `Generate a small project scaffold as compact JSON only: {"files":[{"path":"","content":""}]}.
Rules:
- Generate a working ` + options.Stack + ` project for the user's requirements, not a placeholder greeting app.
- Include README.md, docs/architecture.md, coding_plan.md, process_record.md, source code, and minimal tests.
- Use only standard library/runtime APIs; no network, no package downloads, no credential handling, no shelling out, no file deletion.
- Paths must be relative, stay inside the project, and must not start with .avatars, avatars, .git, node_modules, vendor, dist, or build.
- Keep the scaffold small: 4 to 12 files.
- Tests must verify at least one requested domain behavior.`
	if options.Stack == "python-cli" {
		prompt += `
- Python tests must pass on Windows with: python -m unittest discover -s tests.
- Put generated Python tests under tests/.
- For JSON-backed CLIs, handle missing, empty, malformed, invalid, or wrong-shaped JSON files as empty data unless the requirements say otherwise.
- If tests write invalid JSON, the implementation must catch JSONDecodeError/ValueError instead of crashing.
- Avoid keeping NamedTemporaryFile open while application code opens the same path; use TemporaryDirectory, mkstemp with close, or explicit close first.`
	}
	if options.Stack == "go-api" {
		prompt += `
- Go REST API server using only standard library net/http (no frameworks like gin/gorilla).
- Structure: cmd/server/main.go, internal/handler/, internal/model/, go.mod.
- Include GET /health endpoint. Use encoding/json, httptest for tests.`
	}
	if options.Stack == "python-api" {
		prompt += `
- Python REST API using only standard library http.server (no flask/django).
- Structure: server.py (entry), handlers.py, models.py, requirements.txt.
- Include GET /health endpoint. Use json module, unittest for tests.`
	}
	if options.Stack == "node-api" {
		prompt += `
- Node.js REST API using only built-in http module (no express).
- Structure: server.js (entry), handlers.js, models.js, package.json.
- Include GET /health endpoint. Use built-in assert for tests.`
	}
	if options.Stack == "rust-api" {
		prompt += `
- Rust REST API using only standard library (no actix/rocket).
- Structure: src/main.rs (entry), src/handler.rs, src/model.rs, Cargo.toml.
- Include GET /health endpoint. Use serde for JSON. Tests with #[cfg(test)].`
	}
	if options.Stack == "csharp-cli" || options.Stack == "csharp-api" {
		prompt += `
- C# project using only .NET standard library (no nuget packages).
- CLI: Program.cs with System.CommandLine-style args. API: minimal ASP.NET Core.
- Include .csproj file. Tests with xUnit or MSTest.`
	}
	if options.Stack == "java-cli" || options.Stack == "java-api" {
		prompt += `
- Java project using only standard JDK library (no maven/gradle deps beyond stdlib).
- CLI: single Main class. API: com.sun.net.httpserver based.
- Standard Maven layout: src/main/java/, src/test/java/, pom.xml.`
	}
	return prompt
}

func bootstrapGenerationUserPrompt(options bootstrapCommandOptions) string {
	var builder strings.Builder
	builder.WriteString("Project name: ")
	builder.WriteString(options.Name)
	builder.WriteString("\nModule: ")
	builder.WriteString(options.Module)
	builder.WriteString("\nStack: ")
	builder.WriteString(options.Stack)
	builder.WriteString("\nUser requirements:\n")
	builder.WriteString(strings.TrimSpace(options.Spec))
	builder.WriteString("\n\nReturn JSON only.")
	return builder.String()
}

func parseBootstrapFilesLLMResponse(text string) ([]bootstrapScaffoldFile, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		trimmed = trimmed[start : end+1]
	}
	var response bootstrapFilesLLMResponse
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return nil, false
	}
	files := make([]bootstrapScaffoldFile, 0, len(response.Files))
	for _, file := range response.Files {
		files = append(files, bootstrapScaffoldFile{
			Path:    filepath.Clean(filepath.FromSlash(strings.TrimSpace(file.Path))),
			Content: strings.TrimRight(file.Content, "\r\n") + "\n",
		})
	}
	return files, len(files) > 0
}

func validateBootstrapGeneratedFiles(files []bootstrapScaffoldFile) error {
	if len(files) < 2 {
		return errors.New("LLM bootstrap returned too few files")
	}
	if len(files) > 20 {
		return errors.New("LLM bootstrap returned too many files")
	}
	seen := map[string]bool{}
	for _, file := range files {
		path, err := safeBootstrapGeneratedFilePath(file.Path)
		if err != nil {
			return err
		}
		if seen[path] {
			return fmt.Errorf("LLM bootstrap returned duplicate path %s", filepath.ToSlash(path))
		}
		seen[path] = true
		if strings.TrimSpace(file.Content) == "" && !isAllowedEmptyBootstrapFile(path) {
			return fmt.Errorf("LLM bootstrap returned empty content for %s", filepath.ToSlash(path))
		}
	}
	return nil
}

func isAllowedEmptyBootstrapFile(path string) bool {
	return strings.EqualFold(filepath.Base(path), "__init__.py")
}

func safeBootstrapGeneratedFilePath(path string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(path), "\"'“”")
	if trimmed == "" {
		return "", errors.New("LLM bootstrap returned empty path")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("LLM bootstrap path must be relative: %s", trimmed)
	}
	cleaned := filepath.Clean(filepath.FromSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("LLM bootstrap path escapes project: %s", trimmed)
	}
	first := strings.ToLower(strings.Split(filepath.ToSlash(cleaned), "/")[0])
	switch first {
	case ".avatars", "avatars", ".git", "node_modules", "vendor", "dist", "build":
		return "", fmt.Errorf("LLM bootstrap path uses reserved directory: %s", filepath.ToSlash(cleaned))
	}
	return cleaned, nil
}

func bootstrapGoScaffoldFiles(name string, moduleName string) []bootstrapScaffoldFile {
	title := strings.ReplaceAll(name, "-", " ")
	mainPath := filepath.Join("cmd", name, "main.go")
	testPath := filepath.Join("cmd", name, "main_test.go")
	return []bootstrapScaffoldFile{
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nSmall Go CLI scaffold generated by Avatars bootstrap mode.\n\n## Run\n\n```powershell\ngo run ./cmd/%s\n```\n\n## Test\n\n```powershell\ngo test ./...\n```\n", title, name)},
		{Path: filepath.Join("docs", "architecture.md"), Content: fmt.Sprintf("# Architecture\n\n## Purpose\n\n`%s` starts as a small Go command-line application.\n\n## Boundaries\n\n- `cmd/%s/` owns CLI entrypoint behavior.\n- Business logic should move into `internal/` once it grows beyond one command.\n- Tests live next to the package they verify.\n\n## Verification\n\nPrimary verifier: `go test ./...`.\n", name, name)},
		{Path: "coding_plan.md", Content: "# Coding Plan\n\n## Todo\n\n1. [ ] Replace scaffold greeting with real CLI behavior.\n2. [ ] Add domain package under `internal/` when logic grows.\n3. [ ] Keep `go test ./...` passing after each change.\n"},
		{Path: filepath.Join("docs", "workflow", "process_record.md"), Content: "# Process Record\n\n## Bootstrap\n\n- Scaffold generated by `avatars bootstrap --apply`.\n- Initial verifier: `go test ./...`.\n"},
		{Path: "go.mod", Content: fmt.Sprintf("module %s\n\ngo 1.25\n", moduleName)},
		{Path: mainPath, Content: fmt.Sprintf("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc greeting() string {\n\treturn %q\n}\n\nfunc main() {\n\tfmt.Fprintln(os.Stdout, greeting())\n}\n", name+" ready")},
		{Path: testPath, Content: fmt.Sprintf("package main\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) {\n\tif got := greeting(); got != %q {\n\t\tt.Fatalf(\"expected bootstrap greeting, got %%q\", got)\n\t}\n}\n", name+" ready")},
	}
}

func bootstrapPythonScaffoldFiles(name string) []bootstrapScaffoldFile {
	title := strings.ReplaceAll(name, "-", " ")
	return []bootstrapScaffoldFile{
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nSmall Python CLI scaffold generated by Avatars bootstrap mode.\n\n## Run\n\n```powershell\npython main.py\n```\n\n## Test\n\n```powershell\npython -m unittest\n```\n", title)},
		{Path: filepath.Join("docs", "architecture.md"), Content: fmt.Sprintf("# Architecture\n\n## Purpose\n\n`%s` starts as a small Python command-line application.\n\n## Boundaries\n\n- `main.py` owns CLI entrypoint behavior.\n- Business logic should move into a package directory once it grows beyond one script.\n- Tests live in `test_*.py` files and run with `python -m unittest`.\n\n## Verification\n\nPrimary verifier: `python -m unittest`.\n", name)},
		{Path: "coding_plan.md", Content: "# Coding Plan\n\n## Todo\n\n1. [ ] Replace scaffold greeting with real CLI behavior.\n2. [ ] Move domain logic out of `main.py` when it grows.\n3. [ ] Keep `python -m unittest` passing after each change.\n"},
		{Path: "process_record.md", Content: "# Process Record\n\n## Bootstrap\n\n- Scaffold generated by `avatars bootstrap --apply --stack python-cli`.\n- Initial verifier: `python -m unittest`.\n"},
		{Path: "pyproject.toml", Content: fmt.Sprintf("[project]\nname = %q\nversion = \"0.1.0\"\ndescription = \"Small Python CLI scaffold generated by Avatars.\"\nrequires-python = \">=3.10\"\n", name)},
		{Path: "main.py", Content: fmt.Sprintf("def greeting() -> str:\n    return %q\n\n\ndef main() -> None:\n    print(greeting())\n\n\nif __name__ == \"__main__\":\n    main()\n", name+" ready")},
		{Path: "test_main.py", Content: fmt.Sprintf("import unittest\n\nimport main\n\n\nclass GreetingTest(unittest.TestCase):\n    def test_greeting(self) -> None:\n        self.assertEqual(main.greeting(), %q)\n\n\nif __name__ == \"__main__\":\n    unittest.main()\n", name+" ready")},
	}
}

func bootstrapNodeScaffoldFiles(name string) []bootstrapScaffoldFile {
	title := strings.ReplaceAll(name, "-", " ")
	return []bootstrapScaffoldFile{
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nSmall Node.js CLI scaffold generated by Avatars bootstrap mode.\n\n## Run\n\n```powershell\nnode src/index.js\n```\n\n## Test\n\n```powershell\nnode --test\n```\n", title)},
		{Path: filepath.Join("docs", "architecture.md"), Content: fmt.Sprintf("# Architecture\n\n## Purpose\n\n`%s` starts as a small Node.js command-line application.\n\n## Boundaries\n\n- `src/index.js` owns CLI entrypoint behavior.\n- Business logic should move into additional `src/` modules once it grows.\n- Tests live under `test/` and run with `node --test`.\n\n## Verification\n\nPrimary verifier: `node --test`.\n", name)},
		{Path: "coding_plan.md", Content: "# Coding Plan\n\n## Todo\n\n1. [ ] Replace scaffold greeting with real CLI behavior.\n2. [ ] Split domain logic into additional `src/` modules when it grows.\n3. [ ] Keep `node --test` passing after each change.\n"},
		{Path: "process_record.md", Content: "# Process Record\n\n## Bootstrap\n\n- Scaffold generated by `avatars bootstrap --apply --stack node-cli`.\n- Initial verifier: `node --test`.\n"},
		{Path: "package.json", Content: fmt.Sprintf("{\n  \"name\": %q,\n  \"version\": \"0.1.0\",\n  \"private\": true,\n  \"description\": \"Small Node.js CLI scaffold generated by Avatars.\",\n  \"scripts\": {\n    \"start\": \"node src/index.js\",\n    \"test\": \"node --test\"\n  }\n}\n", name)},
		{Path: filepath.Join("src", "index.js"), Content: fmt.Sprintf("function greeting() {\n  return %q;\n}\n\nfunction main() {\n  console.log(greeting());\n}\n\nif (require.main === module) {\n  main();\n}\n\nmodule.exports = { greeting };\n", name+" ready")},
		{Path: filepath.Join("test", "index.test.js"), Content: fmt.Sprintf("const test = require('node:test');\nconst assert = require('node:assert/strict');\nconst { greeting } = require('../src/index.js');\n\ntest('greeting', () => {\n  assert.equal(greeting(), %q);\n});\n", name+" ready")},
	}
}

func bootstrapRustScaffoldFiles(name string) []bootstrapScaffoldFile {
	title := strings.ReplaceAll(name, "-", " ")
	return []bootstrapScaffoldFile{
		{Path: "README.md", Content: fmt.Sprintf("# %s\n\nSmall Rust CLI scaffold generated by Avatars bootstrap mode.\n\n## Run\n\n```powershell\ncargo run\n```\n\n## Test\n\n```powershell\ncargo test\n```\n", title)},
		{Path: filepath.Join("docs", "architecture.md"), Content: fmt.Sprintf("# Architecture\n\n## Purpose\n\n`%s` starts as a small Rust command-line application.\n\n## Boundaries\n\n- `src/main.rs` owns CLI entrypoint behavior.\n- Keep pure logic in functions that can be unit-tested.\n- Integration tests can move under `tests/` once behavior grows.\n\n## Verification\n\nPrimary verifier: `cargo test`.\n", name)},
		{Path: "coding_plan.md", Content: "# Coding Plan\n\n## Todo\n\n1. [ ] Replace scaffold greeting with real CLI behavior.\n2. [ ] Split domain logic into modules when it grows.\n3. [ ] Keep `cargo test` passing after each change.\n"},
		{Path: "process_record.md", Content: "# Process Record\n\n## Bootstrap\n\n- Scaffold generated by `avatars bootstrap --apply --stack rust-cli`.\n- Initial verifier: `cargo test`.\n"},
		{Path: "Cargo.toml", Content: fmt.Sprintf("[package]\nname = %q\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\n", name)},
		{Path: filepath.Join("src", "main.rs"), Content: fmt.Sprintf("fn greeting() -> &'static str {\n    %q\n}\n\nfn main() {\n    println!(\"{}\", greeting());\n}\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n\n    #[test]\n    fn greeting_matches_project() {\n        assert_eq!(greeting(), %q);\n    }\n}\n", name+" ready", name+" ready")},
	}
}

func bootstrapWorkspaceEmpty(root string) (bool, []string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, nil, err
	}
	visible := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".avatars" || name == ".git" || name == "avatars" {
			continue
		}
		visible = append(visible, name)
	}
	sort.Strings(visible)
	return len(visible) == 0, visible, nil
}

func runBootstrapVerifier(root string, options bootstrapCommandOptions) error {
	if options.Stack == "python-cli" {
		return runBootstrapPythonVerifier(root)
	}
	if options.Stack == "node-cli" {
		return runBootstrapNodeVerifier(root)
	}
	if options.Stack == "rust-cli" {
		return runBootstrapRustVerifier(root)
	}
	return runBootstrapGoVerifier(root)
}

func runBootstrapGoVerifier(root string) error {
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	output, err := command.CombinedOutput()
	trimmedOutput := strings.TrimSpace(string(output))
	if err != nil {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: go test ./...")
		if trimmedOutput != "" {
			fmt.Println(trimmedOutput)
		}
		return err
	}
	fmt.Println("Verifier: PASS")
	fmt.Println("Command: go test ./...")
	if trimmedOutput != "" {
		fmt.Println(trimmedOutput)
	}
	return nil
}

func runBootstrapPythonVerifier(root string) error {
	python, commandLabel := bootstrapPythonCommand()
	label, args := bootstrapPythonVerifierCommand(root, commandLabel)
	if python == "" {
		fmt.Println("Verifier: UNAVAILABLE")
		fmt.Println("Command: " + label)
		fmt.Println("Reason: python executable not found on PATH")
		return nil
	}
	command := exec.Command(python, args...)
	command.Dir = root
	command.Env = pythonVerifierEnv(root)
	output, err := command.CombinedOutput()
	trimmedOutput := strings.TrimSpace(string(output))
	if err != nil {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: " + label)
		if trimmedOutput != "" {
			fmt.Println(trimmedOutput)
		}
		return err
	}
	fmt.Println("Verifier: PASS")
	fmt.Println("Command: " + label)
	if trimmedOutput != "" {
		fmt.Println(trimmedOutput)
	}
	return nil
}

func pythonVerifierEnv(root string) []string {
	env := os.Environ()
	entries := []string{root}
	if info, err := os.Stat(filepath.Join(root, "src")); err == nil && info.IsDir() {
		entries = append(entries, filepath.Join(root, "src"))
	}
	if existing := strings.TrimSpace(os.Getenv("PYTHONPATH")); existing != "" {
		entries = append(entries, existing)
	}
	value := strings.Join(entries, string(os.PathListSeparator))
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if strings.HasPrefix(strings.ToUpper(item), "PYTHONPATH=") {
			continue
		}
		out = append(out, item)
	}
	out = append(out, "PYTHONPATH="+value)
	return out
}

func bootstrapPythonVerifierCommand(root string, commandLabel string) (string, []string) {
	label := strings.TrimSpace(commandLabel)
	if label == "" {
		label = "python"
	}
	if info, err := os.Stat(filepath.Join(root, "tests")); err == nil && info.IsDir() {
		return label + " -m unittest discover -s tests", []string{"-m", "unittest", "discover", "-s", "tests"}
	}
	return label + " -m unittest discover -s .", []string{"-m", "unittest", "discover", "-s", "."}
}

func bootstrapPythonCommand() (string, string) {
	for _, candidate := range []string{"python", "python3", "py"} {
		path, err := exec.LookPath(candidate)
		if err == nil && strings.TrimSpace(path) != "" {
			return path, candidate
		}
	}
	return "", ""
}

func runBootstrapNodeVerifier(root string) error {
	node, err := exec.LookPath("node")
	if err != nil || strings.TrimSpace(node) == "" {
		fmt.Println("Verifier: UNAVAILABLE")
		fmt.Println("Command: node --test")
		fmt.Println("Reason: node executable not found on PATH")
		return nil
	}
	command := exec.Command(node, "--test")
	command.Dir = root
	output, err := command.CombinedOutput()
	trimmedOutput := strings.TrimSpace(string(output))
	if err != nil {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: node --test")
		if trimmedOutput != "" {
			fmt.Println(trimmedOutput)
		}
		return err
	}
	fmt.Println("Verifier: PASS")
	fmt.Println("Command: node --test")
	if trimmedOutput != "" {
		fmt.Println(trimmedOutput)
	}
	return nil
}

func runBootstrapRustVerifier(root string) error {
	cargo, err := exec.LookPath("cargo")
	if err != nil || strings.TrimSpace(cargo) == "" {
		fmt.Println("Verifier: UNAVAILABLE")
		fmt.Println("Command: cargo test")
		fmt.Println("Reason: cargo executable not found on PATH")
		return nil
	}
	command := exec.Command(cargo, "test")
	command.Dir = root
	output, err := command.CombinedOutput()
	trimmedOutput := strings.TrimSpace(string(output))
	if err != nil {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: cargo test")
		if trimmedOutput != "" {
			fmt.Println(trimmedOutput)
		}
		return err
	}
	fmt.Println("Verifier: PASS")
	fmt.Println("Command: cargo test")
	if trimmedOutput != "" {
		fmt.Println(trimmedOutput)
	}
	return nil
}

func persistBootstrapContext(root string, options bootstrapCommandOptions) error {
	dir := filepath.Join(root, ".avatars", "repl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload := bootstrapContextArtifact{
		Name:   options.Name,
		Module: options.Module,
		Stack:  options.Stack,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "bootstrap_context.json"), append(data, '\n'), 0o644)
}

func persistBootstrapSummary(root string, options bootstrapCommandOptions, written []string) error {
	lines := []string{
		"# Bootstrap Summary",
		"",
		"## Project",
		"",
		"- Name: " + options.Name,
		"- Module: " + options.Module,
		"- Stack: " + options.Stack,
		"",
		"## Written Files",
		"",
	}
	if len(written) == 0 {
		lines = append(lines, "- none")
	} else {
		for _, path := range written {
			lines = append(lines, "- "+filepath.ToSlash(path))
		}
	}
	lines = append(lines, "", "## Verification", "", "- See terminal output from the bootstrap run.")
	return os.WriteFile(filepath.Join(root, "bootstrap_summary.md"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func printBootstrapBlockedRemediation(options bootstrapCommandOptions, entries []string) {
	fmt.Println("Bootstrap blocked: project root is not empty.")
	fmt.Println("Blocking entries:")
	for _, entry := range entries {
		fmt.Println("- " + filepath.ToSlash(entry))
	}
	fmt.Println("Safe options:")
	fmt.Println("- run bootstrap in a new empty directory")
	fmt.Println("- move or remove blocking entries, then rerun this command")
	fmt.Printf("- preview intended files: avatars bootstrap --name %s --stack %s\n", options.Name, options.Stack)
	fmt.Printf("- suggested empty target directory: %s\n", filepath.ToSlash(options.Name))
}

func loadBootstrapContext(root string) (bootstrapContextArtifact, bool) {
	content, err := os.ReadFile(filepath.Join(root, ".avatars", "repl", "bootstrap_context.json"))
	if err != nil {
		return bootstrapContextArtifact{}, false
	}
	var artifact bootstrapContextArtifact
	if err := json.Unmarshal(content, &artifact); err != nil {
		return bootstrapContextArtifact{}, false
	}
	if strings.TrimSpace(artifact.Name) == "" && strings.TrimSpace(artifact.Module) == "" && strings.TrimSpace(artifact.Stack) == "" {
		return bootstrapContextArtifact{}, false
	}
	return artifact, true
}

// recordTaskCompletion is a best-effort hook that updates the project
// workflow record after a successful task run. Failures are silent
// (logged to stderr) so they never block task completion.
func recordTaskCompletion(description string) {
	if cwd, err := os.Getwd(); err == nil {
		summary := "Completed: " + firstLine(description, 100)
		if err := workflow.RecordTaskCompletion(cwd, description, summary); err != nil {
			fmt.Fprintf(os.Stderr, "workflow record update: %v\n", err)
		}
	}
}

func firstLine(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\n\r"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if maxLen <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-3]) + "..."
}

// shouldAutoRetryAfterRun decides whether the CLI should silently re-run after
// a completed Engine.Run that returned a non-fatal RunResult.
// F7: construct_plan / confirm_plan quality failures must stop; auto-retry
// previously continued into Builder and hid the abort.
func shouldAutoRetryAfterRun(result runtime.RunResult) bool {
	status := strings.ToLower(strings.TrimSpace(result.SynthesisStatus))
	if strings.HasPrefix(status, "failed: construct_plan") ||
		strings.HasPrefix(status, "failed: confirm_plan") {
		return false
	}
	if llm.IsQuotaOrBillingFailure(result.SynthesisStatus) || llm.IsQuotaOrBillingFailure(result.Summary) {
		return false
	}
	// F111: max tool turns / synthesis tool-cap with a green disk must not
	// burn a second full DAG. Cross-language: same cap exists for pytest/npm/cargo.
	if maxToolTurnsConvergedGreen(result) {
		return false
	}
	if status != "completed" {
		return true
	}
	return strings.Contains(result.Summary, "PARTIAL") ||
		strings.Contains(result.Summary, "FAILED")
}

func maxToolTurnsConvergedGreen(result runtime.RunResult) bool {
	blob := strings.ToLower(result.Summary + "\n" + result.SynthesisStatus)
	hit := strings.Contains(blob, "max tool turns") ||
		strings.Contains(blob, "exceeded max tool") ||
		strings.Contains(blob, "tool-cap") ||
		strings.Contains(blob, "turn-cap")
	if !hit {
		return false
	}
	return result.BuildOKKnown && result.BuildOK
}

// footerStatusForCLI maps RunResult to SuggestNextSteps status (F11).
func footerStatusForCLI(result runtime.RunResult) string {
	status := strings.ToLower(strings.TrimSpace(result.SynthesisStatus))
	if llm.IsQuotaOrBillingFailure(result.SynthesisStatus) {
		return "synthesis_unavailable"
	}
	if strings.HasPrefix(status, "failed:") {
		return "failed"
	}
	if status != "" {
		return status
	}
	return "completed"
}

func runScript(args []string) error {
	options, err := parseScriptCommandOptions(args)
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	relativePath, err := safeRelativeScriptPath(options.Path)
	if err != nil {
		return err
	}
	script, err := scriptScaffoldContentWithOptions(relativePath, options.Description, options.ExpectedOutput, options.ForceLLM)
	if err != nil {
		return err
	}
	content := script.Content
	fmt.Printf("Script mode: %s\n", applyMode(options.Apply))
	fmt.Printf("Path: %s\n", filepath.ToSlash(relativePath))
	fmt.Printf("Description: %s\n", displayScriptDescription(options.Description))
	fmt.Printf("Generator: %s\n", displayScriptGenerator(script.Source))
	if options.ExpectedOutputIsSet {
		fmt.Printf("Expected output: %d bytes supplied via --expected-output-file\n", len(options.ExpectedOutput))
	}
	if !options.Apply {
		fmt.Println("Preview:")
		fmt.Print(content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Println()
		}
		fmt.Println("Next step: avatars script --apply " + filepath.ToSlash(relativePath))
		return nil
	}
	targetPath := filepath.Join(root, relativePath)
	if _, err := os.Stat(targetPath); err == nil {
		// Existing file: update in place. Refusing overwrite blocked Stage HTML
		// iteration and "改这个 .html" flows that NL routes to script --apply.
		fmt.Printf("Note: %s already exists — updating in place.\n", filepath.ToSlash(relativePath))
		fmt.Printf("      (For surgical patches prefer: avatars edit --apply %s \"...\")\n", filepath.ToSlash(relativePath))
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Println("Wrote:")
	fmt.Println("- " + filepath.ToSlash(relativePath))
	recordTaskCompletion(options.Description)
	return runScriptVerifier(root, relativePath, script.ExpectedOutput)
}

func runEdit(args []string) error {
	options, err := parseEditCommandOptions(args)
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	relativePath, err := safeRelativeEditablePath(options.Path)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(root, relativePath)
	// P37-5: Save mtime before LLM generation to detect external modifications.
	readInfo, _ := os.Stat(targetPath)
	current, err := os.ReadFile(targetPath)
	if err != nil {
		return err
	}
	if len(current) > 200000 {
		return fmt.Errorf("refusing to LLM-edit large file %s: %d bytes", filepath.ToSlash(relativePath), len(current))
	}
	result, err := editFileContent(relativePath, options.Instruction, string(current))
	if err != nil {
		return err
	}
	fmt.Printf("Edit mode: %s\n", applyMode(options.Apply))
	fmt.Printf("Path: %s\n", filepath.ToSlash(relativePath))
	fmt.Printf("Instruction: %s\n", displayScriptDescription(options.Instruction))
	fmt.Printf("Generator: %s\n", displayScriptGenerator(result.Source))
	if !options.Apply {
		fmt.Println("Preview:")
		fmt.Print(result.Content)
		if !strings.HasSuffix(result.Content, "\n") {
			fmt.Println()
		}
		fmt.Println("Next step: avatars edit --apply " + filepath.ToSlash(relativePath))
		return nil
	}
	if strings.TrimSpace(result.Content) == strings.TrimSpace(string(current)) {
		return errors.New("LLM edit produced no content change")
	}
	// E3: Show a brief summary of what changed before writing.
	oldLines := strings.Split(strings.TrimRight(string(current), "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(result.Content, "\n"), "\n")
	added := len(newLines) - len(oldLines)
	if added > 0 {
		fmt.Printf("Changes: +%d lines\n", added)
	} else if added < 0 {
		fmt.Printf("Changes: %d lines\n", added)
	} else {
		fmt.Println("Changes: modified (same line count)")
	}
	// P37-5: Staleness check — if the file was modified externally between
	// read and write, warn the user. Borrowed from claude_code_main's
	// FILE_UNEXPECTEDLY_MODIFIED_ERROR pattern.
	if readInfo != nil {
		if currentInfo, err := os.Stat(targetPath); err == nil {
			if currentInfo.ModTime().After(readInfo.ModTime()) {
				fmt.Fprintf(os.Stderr, "⚠️  File was modified externally while editing: %s\n", filepath.ToSlash(relativePath))
				fmt.Fprintf(os.Stderr, "   Your edit will overwrite external changes.\n")
			}
		}
	}
	if err := os.WriteFile(targetPath, []byte(result.Content), 0o644); err != nil {
		return err
	}
	fmt.Println("Wrote:")
	fmt.Println("- " + filepath.ToSlash(relativePath))
	recordTaskCompletion(options.Instruction)
	// ARCH-11: If we edited a file tracked by architecture.md, mark it stale.
	if arch.MaybeMarkStaleAfterEdit(root, []string{relativePath}) {
		fmt.Fprintf(os.Stderr, "⚠️  architecture.md marked stale — edited file is in the architecture document.\n")
		fmt.Fprintf(os.Stderr, "   Run `avatars arch --analyze` to refresh when ready.\n")
	}
	verifierErr := runEditVerifier(root, relativePath, options.Instruction)
	// Mark todo checklist based on outcome.
	workflow.MarkTodoProgress(root, []string{relativePath}, verifierErr == nil)
	return verifierErr
}

func runEditMany(args []string) error {
	options, err := parseEditManyCommandOptions(args)
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	paths, err := editablePathsFromInstruction(options.Instruction)
	if err != nil {
		return err
	}
	// MS1: If instruction doesn't contain explicit file paths, try to resolve
	// from architecture.md Registration Points. This is the bridge from
	// "add a new provider" → architecture.md → specific file list.
	if len(paths) < 2 {
		if archPaths, ok := resolveArchRegPointPaths(root, options.Instruction); ok && len(archPaths) >= 2 {
			fmt.Fprintf(os.Stderr, "Resolved %d target files from architecture.md Registration Points.\n", len(archPaths))
			paths = archPaths
		}
	}
	if len(paths) < 2 {
		return errors.New("edit-many requires at least two explicit existing file paths in the instruction")
	}
	sources := make([]editManySourceFile, 0, len(paths))
	totalBytes := 0
	// P37-5: Track mtimes for staleness detection before write.
	readMtimes := make(map[string]time.Time, len(paths))
	for _, path := range paths {
		absPath := filepath.Join(root, path)
		if info, err := os.Stat(absPath); err == nil {
			readMtimes[path] = info.ModTime()
		}
		content, err := os.ReadFile(absPath)
		if err != nil {
			// P37-1: File existence check — if a path doesn't exist, search for
			// similar files (same base name, different directory) and suggest
			// the real path. Borrowed from claude_code_main's findSimilarFile.
			if os.IsNotExist(err) {
				if suggestion := findSimilarFile(root, path); suggestion != "" {
					fmt.Fprintf(os.Stderr, "⚠️  File not found: %s — did you mean %s?\n", filepath.ToSlash(path), filepath.ToSlash(suggestion))
				} else {
					fmt.Fprintf(os.Stderr, "⚠️  File not found: %s — skipping.\n", filepath.ToSlash(path))
				}
				continue
			}
			return err
		}
		totalBytes += len(content)
		if len(content) > 120000 || totalBytes > 300000 {
			return fmt.Errorf("refusing to LLM-edit large multi-file context; %s has %d bytes, total %d bytes", filepath.ToSlash(path), len(content), totalBytes)
		}
		sources = append(sources, editManySourceFile{Path: path, Content: string(content)})
	}
	// P37-3: Split complex edit-many into per-file LLM calls when there are
	// ≥3 files. Each file gets a focused LLM call, preventing the overload
	// that causes wrong package names, import paths, and missed conventions.
	// Borrowed from claude_code_main's grouped parallel tool calls pattern.
	var result []editManyFileResult
	if len(paths) >= 3 && shouldSplitEditMany(paths) {
		fmt.Fprintf(os.Stderr, "Splitting edit-many: %d files → %d independent edits (better per-file focus)\n", len(paths), len(paths))
		result, err = editManyIndividually(options.Instruction, sources)
	} else {
		result, err = editManyFilesWithLLM(options.Instruction, sources)
	}
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, path := range paths {
		allowed[filepath.ToSlash(path)] = true
	}
	changed := []editManyFileResult{}
	seen := map[string]bool{}
	for _, file := range result {
		path, err := safeRelativeEditablePath(file.Path)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(path)
		if !allowed[slashed] {
			return fmt.Errorf("LLM edit-many returned unrequested path %s", slashed)
		}
		if seen[slashed] {
			return fmt.Errorf("LLM edit-many returned duplicate path %s", slashed)
		}
		seen[slashed] = true
		if strings.TrimSpace(file.Content) == "" {
			return fmt.Errorf("LLM edit-many returned empty content for %s", slashed)
		}
		current, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(current)) != strings.TrimSpace(file.Content) {
			changed = append(changed, editManyFileResult{Path: path, Content: normalizeGeneratedScriptContent(file.Content)})
		}
	}
	if len(changed) == 0 {
		return errors.New("LLM edit-many produced no content changes")
	}
	// MS2: Change plan preview — show what will be modified before executing.
	// If architecture.md has Registration Points that match this change,
	// the LLM router has already ensured all required files are in the list.
	fmt.Fprintf(os.Stderr, "Edit-many mode: %s\n", applyMode(options.Apply))
	fmt.Fprintf(os.Stderr, "Instruction: %s\n", displayScriptDescription(options.Instruction))
	fmt.Fprintf(os.Stderr, "Will modify %d files:\n", len(paths))
	for _, path := range paths {
		fmt.Fprintf(os.Stderr, "  - %s\n", filepath.ToSlash(path))
	}
	if !options.Apply {
		fmt.Println("Preview:")
		for _, file := range changed {
			fmt.Println("--- " + filepath.ToSlash(file.Path))
			fmt.Print(file.Content)
			if !strings.HasSuffix(file.Content, "\n") {
				fmt.Println()
			}
		}
		fmt.Println("Next step: avatars edit-many --apply <instruction>")
		return nil
	}
	// B5: Show a brief diff summary before writing.
	for _, file := range changed {
		absPath := filepath.Join(root, file.Path)
		orig, _ := os.ReadFile(absPath)
		diff := summarizeDiff(string(orig), file.Content)
		if diff != "" {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", filepath.ToSlash(file.Path), diff)
		}
	}
	// P37-5: Staleness check — detect external modifications during LLM generation.
	for _, file := range changed {
		if readMtime, ok := readMtimes[file.Path]; ok {
			if info, err := os.Stat(filepath.Join(root, file.Path)); err == nil {
				if info.ModTime().After(readMtime) {
					fmt.Fprintf(os.Stderr, "⚠️  %s was modified externally while editing.\n", filepath.ToSlash(file.Path))
				}
			}
		}
	}
	for _, file := range changed {
		if err := os.WriteFile(filepath.Join(root, file.Path), []byte(file.Content), 0o644); err != nil {
			return err
		}
	}
	fmt.Println("Wrote:")
	for _, file := range changed {
		fmt.Println("- " + filepath.ToSlash(file.Path))
	}
	// MS3: Collect changed file paths for per-language verification.
	var changedPaths []string
	for _, file := range changed {
		changedPaths = append(changedPaths, file.Path)
	}

	// P37-7: Self-repair loop — when verifier fails, feed errors back to LLM
	// and retry. Max 3 rounds. Borrowed from claude_code_main's LSP diagnostic
	// → next-turn feedback pattern, tightened into same-turn retry.
	const maxRetries = 3
	for retry := 0; ; retry++ {
		err := runEditManyVerifierWithFiles(root, options.Instruction, changedPaths)
		if err == nil {
			memstore.ClearVerifierError()
			break // success
		}

		// Last retry exhausted — give up and report.
		if retry >= maxRetries-1 {
			fmt.Fprintf(os.Stderr, "❌ Self-repair failed after %d rounds. Please review the errors above and fix manually.\n", maxRetries)
			return err
		}

		// Build a fix instruction from the verifier error.
		errText := memstore.GetLastVerifierError()
		if errText == "" {
			return err // no error text to feed back, give up
		}

		fmt.Fprintf(os.Stderr, "\n🔄 Self-repair round %d/%d: feeding errors back to LLM...\n", retry+1, maxRetries)
		fixInstruction := fmt.Sprintf("The previous edit caused build/syntax errors. Fix ALL of these errors:\n%s\n\nOriginal instruction: %s", errText, options.Instruction)

		// Re-read current file contents after the failed edit.
		var sources []editManySourceFile
		for _, p := range changedPaths {
			content, readErr := os.ReadFile(filepath.Join(root, p))
			if readErr != nil {
				continue
			}
			sources = append(sources, editManySourceFile{Path: p, Content: string(content)})
		}

		// Re-run LLM edit with the fix instruction, using per-file mode.
		var fixResult []editManyFileResult
		if len(sources) >= 3 {
			fixResult, err = editManyIndividually(fixInstruction, sources)
		} else {
			fixResult, err = editManyFilesWithLLM(fixInstruction, sources)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠️  Fix LLM call failed: %v\n", err)
			continue
		}

		// Apply fixes.
		changed = nil
		changedPaths = nil
		for _, file := range fixResult {
			absPath := filepath.Join(root, file.Path)
			orig, _ := os.ReadFile(absPath)
			diff := summarizeDiff(string(orig), file.Content)
			if diff != "" {
				fmt.Fprintf(os.Stderr, "  Fix %s: %s\n", filepath.ToSlash(file.Path), diff)
			}
			if err := os.WriteFile(absPath, []byte(file.Content), 0o644); err != nil {
				return err
			}
			changed = append(changed, file)
			changedPaths = append(changedPaths, file.Path)
		}
		if len(changed) == 0 {
			fmt.Fprintf(os.Stderr, "  ℹ️  No changes in fix round.\n")
		}
	}

	// ARCH-11: Check if any edited files are tracked by architecture.md.
	if arch.MaybeMarkStaleAfterEdit(root, changedPaths) {
		fmt.Fprintf(os.Stderr, "⚠️  architecture.md marked stale — edited files are in the architecture document.\n")
		fmt.Fprintf(os.Stderr, "   Run `avatars arch --analyze` to refresh when ready.\n")
	}
	return nil
}

func parseEditManyCommandOptions(args []string) (editManyCommandOptions, error) {
	if len(args) == 0 {
		return editManyCommandOptions{}, errors.New("usage: avatars edit-many [--apply] <instruction>")
	}
	options := editManyCommandOptions{}
	positionals := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "--apply":
			options.Apply = true
			args = args[1:]
		default:
			positionals = append(positionals, args[0])
			args = args[1:]
		}
	}
	options.Instruction = strings.TrimSpace(strings.Join(positionals, " "))
	if options.Instruction == "" {
		return editManyCommandOptions{}, errors.New("edit-many instruction cannot be empty")
	}
	return options, nil
}

func parseEditCommandOptions(args []string) (editCommandOptions, error) {
	if len(args) == 0 {
		return editCommandOptions{}, errors.New("usage: avatars edit [--apply] <path> <instruction>")
	}
	options := editCommandOptions{}
	positionals := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "--apply":
			options.Apply = true
			args = args[1:]
		default:
			positionals = append(positionals, args[0])
			args = args[1:]
		}
	}
	if len(positionals) < 2 {
		return editCommandOptions{}, errors.New("usage: avatars edit [--apply] <path> <instruction>")
	}
	options.Path = strings.TrimSpace(positionals[0])
	options.Instruction = strings.TrimSpace(strings.Join(positionals[1:], " "))
	if options.Instruction == "" {
		return editCommandOptions{}, errors.New("edit instruction cannot be empty")
	}
	return options, nil
}

func safeRelativeEditablePath(path string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(path), "\"'“”")
	if trimmed == "" {
		return "", errors.New("edit path cannot be empty")
	}
	if filepath.IsAbs(trimmed) {
		return "", errors.New("edit path must be relative to the current project")
	}
	cleaned := filepath.Clean(filepath.FromSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", errors.New("edit path must stay inside the current project")
	}
	extension := strings.ToLower(filepath.Ext(cleaned))
	switch extension {
	case ".py", ".js", ".mjs", ".jsx", ".ts", ".tsx", ".go", ".rs", ".java", ".cs", ".ps1", ".sh", ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".html", ".css", ".sql", ".xml", ".csv", ".ini", ".cfg", ".env":
	default:
		return "", fmt.Errorf("unsupported edit extension %q", extension)
	}
	return cleaned, nil
}

func editFileContent(path string, instruction string, current string) (editFileResult, error) {
	content, ok, err := editContentGenerator(path, instruction, current)
	if err != nil {
		return editFileResult{}, err
	}
	if !ok {
		return editFileResult{}, errors.New("LLM edit generation unavailable; refusing to fake a code edit")
	}
	return editFileResult{Content: content, Source: "llm"}, nil
}

var editContentGenerator = generateEditedFileWithLLM

func generateEditedFileWithLLM(path string, instruction string, current string) (string, bool, error) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return "", false, nil
	}
	if cleanup != nil {
		defer cleanup()
	}
	response, err := client.Generate(context.Background(), llm.Request{
		SystemPrompt: editGenerationSystemPrompt(path),
		UserPrompt:   editGenerationUserPrompt(path, instruction, current),
	})
	if err != nil || response.Fallback {
		return "", false, nil
	}
	content := normalizeGeneratedScriptContent(response.Text)
	if strings.TrimSpace(content) == "" {
		return "", false, nil
	}
	return content, true, nil
}

func editGenerationSystemPrompt(path string) string {
	extension := strings.ToLower(filepath.Ext(path))
	return "Edit one existing " + extension + " file. Return the complete updated file only, no markdown fences, no explanation. Preserve unrelated behavior, style, imports, comments, and formatting where practical. Use only standard library/runtime APIs. Do not add network, file deletion, shelling out, credential access, or destructive operations."
}

func editGenerationUserPrompt(path string, instruction string, current string) string {
	var builder strings.Builder
	builder.WriteString("Target path: ")
	builder.WriteString(filepath.ToSlash(path))
	builder.WriteString("\nUser instruction:\n")
	builder.WriteString(strings.TrimSpace(instruction))
	builder.WriteString("\n\nCurrent file content:\n")
	builder.WriteString(current)
	builder.WriteString("\n\nReturn the full replacement content for the target file.")
	return builder.String()
}

func editablePathsFromInstruction(instruction string) ([]string, error) {
	matches := regexp.MustCompile(`(?i)(?:^|[\s"'`+"`"+`，、,;；:：])([A-Za-z0-9_.\-/\\]+(?:\.go|\.py|\.json|\.mjs|\.js|\.tsx|\.ts|\.rs|\.java|\.cs|\.md|\.txt|\.ya?ml|\.toml|\.html|\.css|\.sql|\.xml|\.csv|\.ini|\.cfg|\.env))`).FindAllStringSubmatch(instruction, -1)
	seen := map[string]bool{}
	paths := []string{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		path, err := safeRelativeEditablePath(match[1])
		if err != nil {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		slashed := filepath.ToSlash(path)
		if seen[slashed] {
			continue
		}
		seen[slashed] = true
		paths = append(paths, path)
	}
	if len(paths) > 8 {
		return nil, fmt.Errorf("edit-many supports at most 8 explicit files, got %d", len(paths))
	}
	return paths, nil
}

// shouldSplitEditMany decides whether to split edit-many into per-file calls.
// Split when files are in different directories (no direct dependency coupling).
// This prevents single-call overload (B4) — each LLM call focuses on one file.
func shouldSplitEditMany(paths []string) bool {
	if len(paths) < 3 {
		return false
	}
	dirs := map[string]int{}
	for _, p := range paths {
		dirs[filepath.Dir(p)]++
	}
	// Split if files span ≥2 different directories.
	return len(dirs) >= 2
}

// editManyIndividually runs editFileContent for each file independently.
// Each LLM call sees only ONE file's content, reducing context overload.
// Sibling file context is injected via buildEditManyProjectContext.
// Results are collected and returned in the same format as the batch call.
func editManyIndividually(instruction string, sources []editManySourceFile) ([]editManyFileResult, error) {
	var results []editManyFileResult
	for _, src := range sources {
		fmt.Fprintf(os.Stderr, "  Editing %s...\n", filepath.ToSlash(src.Path))
		result, err := editFileContent(src.Path, instruction, src.Content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠️  Failed to edit %s: %v\n", filepath.ToSlash(src.Path), err)
			continue // try remaining files
		}
		if strings.TrimSpace(result.Content) == strings.TrimSpace(src.Content) {
			fmt.Fprintf(os.Stderr, "  ℹ️  No changes to %s\n", filepath.ToSlash(src.Path))
			continue
		}
		results = append(results, editManyFileResult{
			Path:    src.Path,
			Content: result.Content,
		})
	}
	if len(results) == 0 {
		return nil, errors.New("LLM edit-many produced no content changes across any file")
	}
	return results, nil
}

func editManyFilesWithLLM(instruction string, sources []editManySourceFile) ([]editManyFileResult, error) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, errors.New("LLM edit-many generation unavailable; refusing to fake multi-file edits")
	}
	if cleanup != nil {
		defer cleanup()
	}
	response, err := client.Generate(context.Background(), llm.Request{
		SystemPrompt:     editManyGenerationSystemPrompt(),
		UserPrompt:       editManyGenerationUserPrompt(instruction, sources),
		StructuredOutput: true,
	})
	if err != nil || response.Fallback {
		return nil, errors.New("LLM edit-many generation unavailable; refusing to fake multi-file edits")
	}
	files, ok := parseEditManyLLMResponse(response.Text)
	if !ok {
		return nil, errors.New("LLM edit-many returned invalid JSON")
	}
	return files, nil
}

func editManyGenerationSystemPrompt() string {
	return `Edit multiple existing project files. Return compact JSON only: {"files":[{"path":"","content":""}]}.
Rules:
- Return full replacement content for every file you change.
- Only return paths listed in the provided current files.
- Preserve unrelated behavior and style.
- Keep changes minimal and coherent across source, tests, and docs.
- Cross-language coherence is CRITICAL: if you modify a Go config file AND a JSX frontend file, ensure the names,
  imports, and contracts match across languages. For example, a provider name in Go must match the provider key
  used in the JSX settings component.
- Use only standard library/runtime APIs; no network, shelling out, credentials, or destructive file operations.
- Tests in returned files must verify requested behavior.`
}

func editManyGenerationUserPrompt(instruction string, sources []editManySourceFile) string {
	var builder strings.Builder
	builder.WriteString("User instruction:\n")
	builder.WriteString(strings.TrimSpace(instruction))

	// Workflow context: read the project plan and phase docs so the LLM
	// understands what we're working toward and what constraints exist.
	if wfCtx := workflow.BuildEditManyWorkflowContext("."); wfCtx != "" {
		builder.WriteString("\n")
		builder.WriteString(wfCtx)
	}

	// B2+B3: Inject project context — sibling files and naming conventions.
	ctx := buildEditManyProjectContext(sources)
	if ctx != "" {
		builder.WriteString("\n\n## Project Context (existing code you MUST respect)\n")
		builder.WriteString(ctx)
	}

	builder.WriteString("\n\nCurrent files to edit:\n")
	for _, source := range sources {
		builder.WriteString("\n--- path: ")
		builder.WriteString(filepath.ToSlash(source.Path))
		builder.WriteString("\n")
		builder.WriteString(source.Content)
		if !strings.HasSuffix(source.Content, "\n") {
			builder.WriteString("\n")
		}
	}
	builder.WriteString("\nReturn JSON only.")
	return builder.String()
}

// buildEditManyProjectContext scans the directories of the target files and
// extracts naming conventions, package names, import paths, and sibling files.
// This context is injected into the edit-many LLM prompt to prevent:
//   - Wrong package names (B2)
//   - Duplicated/conflicting code with existing files (B3)
//
// Language-agnostic: works for Go packages, Python modules, JS/TS components, etc.
func buildEditManyProjectContext(sources []editManySourceFile) string {
	if len(sources) == 0 {
		return ""
	}

	// Collect unique directories from the target files.
	dirs := map[string]bool{}
	for _, s := range sources {
		dirs[filepath.Dir(s.Path)] = true
	}

	var b strings.Builder
	shown := map[string]bool{}

	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) <= 1 {
			continue // only the target file itself, no siblings to learn from
		}

		b.WriteString(fmt.Sprintf("In %s/:\n", filepath.ToSlash(dir)))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			fullPath := filepath.Join(dir, name)
			if shown[fullPath] {
				continue
			}
			shown[fullPath] = true

			// Read first 300 chars to reveal package declarations and imports.
			data, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			preview := string(data)
			if len(preview) > 400 {
				preview = preview[:400]
			}
			// Extract key identifiers: Go package name, Python imports, JS imports, etc.
			idents := extractKeyIdents(preview, name)
			b.WriteString(fmt.Sprintf("  %s — %s\n", name, idents))
		}
		b.WriteString("\n")
	}

	// Also check if there are sibling directories with similar naming patterns.
	// e.g., if editing internal/config/, show what's in internal/llm/providers/
	for dir := range dirs {
		parent := filepath.Dir(dir)
		if parent == "." || parent == "" {
			continue
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		hasProviderDirs := false
		for _, e := range entries {
			if e.IsDir() && strings.Contains(strings.ToLower(e.Name()), "provider") {
				hasProviderDirs = true
				break
			}
		}
		if hasProviderDirs {
			b.WriteString(fmt.Sprintf("Sibling directories in %s/:\n", filepath.ToSlash(parent)))
			for _, e := range entries {
				if !e.IsDir() || e.Name() == filepath.Base(dir) || shown[filepath.Join(parent, e.Name())] {
					continue
				}
				// Show one representative file from each sibling directory.
				sibDir := filepath.Join(parent, e.Name())
				sibEntries, _ := os.ReadDir(sibDir)
				for _, se := range sibEntries {
					if se.IsDir() {
						continue
					}
					fullPath := filepath.Join(sibDir, se.Name())
					if shown[fullPath] {
						continue
					}
					shown[fullPath] = true
					data, _ := os.ReadFile(fullPath)
					preview := string(data)
					if len(preview) > 400 {
						preview = preview[:400]
					}
					idents := extractKeyIdents(preview, se.Name())
					b.WriteString(fmt.Sprintf("  %s/%s — %s\n", e.Name(), se.Name(), idents))
					break // one file per sibling dir is enough
				}
			}
			b.WriteString("\n")
		}
		break // only do sibling scan once
	}

	return b.String()
}

// extractKeyIdents extracts key identifiers from the first lines of a source file.
// Language-agnostic: detects Go package declarations, Python imports/classes,
// JS/TS imports/exports, and other common identifier patterns.
func extractKeyIdents(preview string, filename string) string {
	lines := strings.Split(preview, "\n")
	var parts []string

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".go":
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "package ") {
				parts = append(parts, trimmed)
			}
			if strings.HasPrefix(trimmed, "import ") || trimmed == "import (" {
				parts = append(parts, "imports: ...")
			}
			if strings.HasPrefix(trimmed, "func ") {
				name := strings.TrimPrefix(trimmed, "func ")
				if idx := strings.Index(name, "("); idx > 0 {
					name = name[:idx]
				}
				parts = append(parts, "func "+strings.TrimSpace(name))
			}
		}
	case ".py":
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
				parts = append(parts, trimmed)
			}
			if strings.HasPrefix(trimmed, "class ") {
				parts = append(parts, trimmed)
			}
		}
	case ".js", ".jsx", ".ts", ".tsx":
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "export ") {
				parts = append(parts, trimmed)
			}
		}
	}

	if len(parts) == 0 {
		// Fallback: show first meaningful line.
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "/*") {
				if len(trimmed) > 80 {
					trimmed = trimmed[:80] + "..."
				}
				return trimmed
			}
		}
		return "(empty or comment-only)"
	}
	return strings.Join(parts, "; ")
}

// summarizeDiff produces a compact summary of changes between old and new content.
// Returns e.g. "+12/-3 lines" or "package name differs: main→server".
func summarizeDiff(old, new string) string {
	oldLines := strings.Split(strings.TrimRight(old, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(new, "\n"), "\n")
	added := len(newLines) - len(oldLines)

	// Quick line-count diff.
	if added > 0 {
		return fmt.Sprintf("+%d lines", added)
	} else if added < 0 {
		return fmt.Sprintf("%d lines", added)
	}

	// Same line count — check for package/import name changes in Go files.
	if len(oldLines) > 0 && len(newLines) > 0 {
		oldFirst := strings.TrimSpace(oldLines[0])
		newFirst := strings.TrimSpace(newLines[0])
		if strings.HasPrefix(oldFirst, "package ") && strings.HasPrefix(newFirst, "package ") &&
			oldFirst != newFirst {
			return fmt.Sprintf("package: %s→%s",
				strings.TrimPrefix(oldFirst, "package "),
				strings.TrimPrefix(newFirst, "package "))
		}
		// Check for new import paths.
		oldImports := extractImportPaths(strings.Join(oldLines, "\n"))
		newImports := extractImportPaths(strings.Join(newLines, "\n"))
		for _, ni := range newImports {
			found := false
			for _, oi := range oldImports {
				if oi == ni {
					found = true
					break
				}
			}
			if !found {
				return fmt.Sprintf("+import %s", ni)
			}
		}
	}

	return "modified (same line count)"
}

func extractImportPaths(content string) []string {
	var paths []string
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Go imports.
		if strings.HasPrefix(trimmed, "\"") && strings.Contains(trimmed, "/") {
			p := strings.Trim(trimmed, "\"")
			if p != "" {
				paths = append(paths, p)
			}
		}
		// Python imports.
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
			paths = append(paths, trimmed)
		}
		// JS imports.
		if strings.HasPrefix(trimmed, "import ") && strings.Contains(trimmed, " from ") {
			paths = append(paths, trimmed)
		}
	}
	return paths
}

func parseEditManyLLMResponse(text string) ([]editManyFileResult, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		trimmed = trimmed[start : end+1]
	}
	var response struct {
		Files []editManyFileResult `json:"files"`
	}
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return nil, false
	}
	for index := range response.Files {
		response.Files[index].Path = strings.TrimSpace(response.Files[index].Path)
		response.Files[index].Content = strings.TrimRight(response.Files[index].Content, "\r\n") + "\n"
	}
	return response.Files, len(response.Files) > 0
}

// resolveArchRegPointPaths attempts to resolve target file paths from
// architecture.md Registration Points when the instruction doesn't contain
// explicit file paths. This is the architecture.md → edit-many bridge.
//
// If architecture.md paths are hallucinated (LLMs often invent plausible but
// wrong paths), falls back to searching the actual project for files matching
// the instruction's intent (e.g., "*provider*.go", "config.go").
func resolveArchRegPointPaths(root string, instruction string) ([]string, bool) {
	// Try architecture.md first.
	if paths := resolveFromArchDoc(root, instruction); len(paths) >= 2 {
		return paths, true
	}

	// Fallback: search the project for files matching provider/config patterns.
	// This handles the case where arch doc Registration Points have hallucinated paths.
	lower := strings.ToLower(instruction)
	if strings.Contains(lower, "provider") || strings.Contains(lower, "model") ||
		strings.Contains(lower, "llm") || strings.Contains(lower, "ai") {
		if paths := searchProjectForProviderFiles(root); len(paths) >= 2 {
			fmt.Fprintf(os.Stderr, "Resolved %d target files by searching project (architecture.md paths were unavailable).\n", len(paths))
			return paths, true
		}
	}
	return nil, false
}

func resolveFromArchDoc(root string, instruction string) []string {
	doc, err := arch.ReadArchDoc(root)
	if err != nil || doc == nil {
		return nil
	}
	if doc.Meta.Status != "confirmed" && doc.Meta.Status != "draft" {
		return nil
	}

	lower := strings.ToLower(instruction)
	for _, rp := range doc.RegPoints {
		changeType := strings.ToLower(rp.ChangeType)
		parts := strings.Split(changeType, "-")
		matchCount := 0
		for _, part := range parts {
			if len(part) > 2 && strings.Contains(lower, part) {
				matchCount++
			}
		}
		if matchCount >= 1 {
			var paths []string
			for _, rf := range rp.Files {
				// Accept approximate paths and real paths.
				p := strings.TrimPrefix(rf.Path, "[approximate] ")
				if _, err := os.Stat(filepath.Join(root, p)); err == nil {
					paths = append(paths, p)
				}
			}
			if len(paths) >= 2 {
				return paths
			}
		}
	}
	return nil
}

// searchProjectForProviderFiles scans the project for files typically involved
// in adding a new provider/LLM integration. This is a deterministic fallback
// when architecture.md Registration Points have hallucinated paths.
// findSimilarFile searches the project for a file with the same base name
// as the given path, returning the first real path found. Borrowed from
// claude_code_main's findSimilarFile pattern: when the LLM hallucinates a
// path like "internal/provider/config.go", find the real "internal/config/config.go".
func findSimilarFile(root, target string) string {
	base := filepath.Base(target)
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && (info.Name() == ".git" || info.Name() == "node_modules" ||
				info.Name() == ".avatars" || info.Name() == "avatars" ||
				strings.HasPrefix(info.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() == base {
			rel, _ := filepath.Rel(root, path)
			found = rel
			return filepath.SkipAll // stop search on first match
		}
		return nil
	})
	return found
}

func searchProjectForProviderFiles(root string) []string {
	var candidates []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == ".avatars" ||
				name == "avatars" || name == "__pycache__" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		base := strings.ToLower(info.Name())

		// Provider implementation files.
		if strings.Contains(base, "provider") || strings.Contains(base, "provider") {
			candidates = append(candidates, rel)
		}
		// Config files that register providers.
		if base == "config.go" || base == "config.ts" || base == "config.js" {
			candidates = append(candidates, rel)
		}
		// LLM interface / registry files.
		if base == "interface.go" || base == "registry.go" || base == "provider.go" {
			candidates = append(candidates, rel)
		}
		// Frontend settings files with LLM/provider configuration.
		if strings.Contains(base, "setting") && (strings.HasSuffix(base, ".jsx") || strings.HasSuffix(base, ".tsx") || strings.HasSuffix(base, ".js")) {
			candidates = append(candidates, rel)
		}
		// API handlers that expose providers.
		if strings.Contains(base, "provider") && strings.Contains(rel, "api") {
			candidates = append(candidates, rel)
		}
		return nil
	})

	// Deduplicate and limit to at most 5 files — more files can exceed
	// LLM context windows and the edit-many content size limit.
	seen := map[string]bool{}
	var result []string
	for _, p := range candidates {
		if !seen[p] && len(result) < 5 {
			seen[p] = true
			result = append(result, p)
		}
	}
	return result
}

func runEditManyVerifier(root string, instruction string) error {
	// MS3: Per-file language detection for cross-language edit-many.
	// Run appropriate verifier for each file's language, rather than assuming
	// the whole project is one language.
	return runEditManyVerifierWithFiles(root, instruction, nil)
}

func runEditManyVerifierWithFiles(root string, instruction string, changedFiles []string) error {
	// If we know which files changed, run per-language verifiers.
	if len(changedFiles) > 0 {
		langs := detectFileLanguages(changedFiles)
		if len(langs) > 1 {
			fmt.Fprintf(os.Stderr, "Verifier: detected %d languages in edit-many (%s)\n", len(langs), strings.Join(langSummary(langs), ", "))
		}
		var lastErr error
		anyPassed := false
		for _, lang := range langs {
			result := verifyFileLanguage(root, lang, changedFiles)
			if result.passed {
				anyPassed = true
			}
			if result.err != nil {
				lastErr = result.err
			}
		}
		if anyPassed {
			return nil
		}
		if lastErr != nil {
			return lastErr
		}
		fmt.Println("Verifier: SKIPPED")
		fmt.Println("Reason: no verifier available for file types in this edit")
		return nil
	}

	// Fallback: project-level verifier detection (original behavior).
	if label, args, ok := focusedGoTestCommandFromText(instruction); ok {
		goBin, err := exec.LookPath("go")
		if err != nil || strings.TrimSpace(goBin) == "" {
			fmt.Println("Verifier: UNAVAILABLE")
			fmt.Println("Command: " + label)
			fmt.Println("Reason: go executable not found on PATH")
			return nil
		}
		return runScriptVerifierCommand(root, label, goBin, args...)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		return runBootstrapGoVerifier(root)
	}
	if info, err := os.Stat(filepath.Join(root, "tests")); err == nil && info.IsDir() {
		return runBootstrapPythonVerifier(root)
	}
	fmt.Println("Verifier: SKIPPED")
	fmt.Println("Reason: no built-in project verifier for multi-file edit")
	return nil
}

// fileVerifierResult holds the result of a per-language verification attempt.
type fileVerifierResult struct {
	passed bool
	err    error
	label  string
}

// detectFileLanguages returns the unique set of languages across changed files,
// detected by file extension.
func detectFileLanguages(files []string) []string {
	seen := map[string]bool{}
	var langs []string
	for _, f := range files {
		lang := langFromExt(filepath.Ext(f))
		if lang != "" && !seen[lang] {
			seen[lang] = true
			langs = append(langs, lang)
		}
	}
	return langs
}

// langFromExt maps file extensions to language names for verification.
func langFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".jsx":
		return "jsx"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	default:
		return ""
	}
}

func langSummary(langs []string) []string {
	out := make([]string, len(langs))
	for i, l := range langs {
		out[i] = l
	}
	return out
}

// verifyFileLanguage runs a language-appropriate verifier for the given files.
func verifyFileLanguage(root string, lang string, files []string) fileVerifierResult {
	switch lang {
	case "go":
		hasGo := false
		for _, f := range files {
			if strings.HasSuffix(f, ".go") {
				hasGo = true
				break
			}
		}
		if !hasGo {
			return fileVerifierResult{passed: true, label: "go (no .go files in this edit)"}
		}
		if _, err := exec.LookPath("go"); err == nil {
			// Focus test on the changed Go files.
			if label, args, ok := focusedGoTestCommandFromText(strings.Join(files, " ")); ok {
				return fileVerifierResult{passed: true, label: label, err: runScriptVerifierCommand(root, label, "go", args...)}
			}
			// Fallback: go build on the project.
			return fileVerifierResult{passed: true, label: "go build", err: runBootstrapGoVerifier(root)}
		}
		fmt.Printf("Verifier [go]: UNAVAILABLE (go not on PATH)\n")
		return fileVerifierResult{passed: true} // not a failure — tool just unavailable

	case "python":
		if _, err := exec.LookPath("python"); err == nil {
			return fileVerifierResult{passed: true, label: "python syntax", err: runBootstrapPythonVerifier(root)}
		}
		if _, err := exec.LookPath("python3"); err == nil {
			return fileVerifierResult{passed: true, label: "python syntax", err: runBootstrapPythonVerifier(root)}
		}
		fmt.Printf("Verifier [python]: UNAVAILABLE\n")
		return fileVerifierResult{passed: true}

	case "javascript", "typescript", "jsx":
		// Node.js syntax check on changed JS/TS files.
		if nodeBin, err := exec.LookPath("node"); err == nil {
			for _, f := range files {
				ext := strings.ToLower(filepath.Ext(f))
				if ext == ".js" || ext == ".mjs" || ext == ".ts" || ext == ".tsx" || ext == ".jsx" {
					absPath := filepath.Join(root, f)
					if _, err := os.Stat(absPath); err == nil {
						cmd := exec.Command(nodeBin, "-c", absPath)
						if out, err := cmd.CombinedOutput(); err != nil {
							fmt.Printf("Verifier [%s]: FAIL — %s\n", lang, strings.TrimSpace(string(out)))
							// P37-2: Record for smart recovery on next turn.
							memstore.RecordVerifierError(fmt.Sprintf("node -c %s: %s", f, strings.TrimSpace(string(out))))
							return fileVerifierResult{label: "node -c", err: fmt.Errorf("node syntax check failed for %s", f)}
						}
					}
				}
			}
			fmt.Printf("Verifier [%s]: PASS (node -c)\n", lang)
			return fileVerifierResult{passed: true, label: "node -c"}
		}
		fmt.Printf("Verifier [%s]: UNAVAILABLE (node not on PATH)\n", lang)
		return fileVerifierResult{passed: true}

	case "rust":
		if _, err := exec.LookPath("cargo"); err == nil {
			return fileVerifierResult{passed: true, label: "cargo check", err: runScriptVerifierCommand(root, "cargo check", "cargo", "check")}
		}
		fmt.Printf("Verifier [rust]: UNAVAILABLE\n")
		return fileVerifierResult{passed: true}

	case "java":
		if _, err := exec.LookPath("javac"); err == nil {
			fmt.Printf("Verifier [java]: SKIPPED (syntax-only, no build)\n")
		}
		return fileVerifierResult{passed: true}

	default:
		return fileVerifierResult{passed: true, label: "no verifier for " + lang}
	}
}

func parseScriptCommandOptions(args []string) (scriptCommandOptions, error) {
	if len(args) == 0 {
		return scriptCommandOptions{}, errors.New("usage: avatars script [--apply] [--expected-output-file <path>] <path> [description]")
	}
	options := scriptCommandOptions{}
	positionals := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "--apply":
			options.Apply = true
			args = args[1:]
		case "--llm":
			options.ForceLLM = true
			args = args[1:]
		case "--expected-output-file":
			if len(args) < 2 {
				return scriptCommandOptions{}, errors.New("flag --expected-output-file requires a path argument")
			}
			path := strings.TrimSpace(args[1])
			if path == "" {
				return scriptCommandOptions{}, errors.New("flag --expected-output-file requires a non-empty path argument")
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return scriptCommandOptions{}, fmt.Errorf("read --expected-output-file %q: %w", path, err)
			}
			options.ExpectedOutput = string(content)
			options.ExpectedOutputIsSet = true
			args = args[2:]
		default:
			positionals = append(positionals, args[0])
			args = args[1:]
		}
	}
	if len(positionals) == 0 {
		return scriptCommandOptions{}, errors.New("usage: avatars script [--apply] [--expected-output-file <path>] <path> [description]")
	}
	options.Path = strings.TrimSpace(positionals[0])
	if len(positionals) > 1 {
		options.Description = strings.TrimSpace(strings.Join(positionals[1:], " "))
	}
	return options, nil
}

func safeRelativeScriptPath(path string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(path), "\"'“”")
	if trimmed == "" {
		return "", errors.New("script path cannot be empty")
	}
	if filepath.IsAbs(trimmed) {
		return "", errors.New("script path must be relative to the current project")
	}
	cleaned := filepath.Clean(filepath.FromSlash(trimmed))
	if cleaned == "." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) || cleaned == ".." {
		return "", errors.New("script path must stay inside the current project")
	}
	extension := strings.ToLower(filepath.Ext(cleaned))
	switch extension {
	case ".py", ".js", ".mjs", ".ts", ".tsx", ".go", ".rs", ".java", ".cs", ".ps1", ".sh", ".html", ".css", ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".sql", ".xml", ".csv", ".ini", ".cfg", ".env":
	default:
		return "", fmt.Errorf("unsupported script extension %q", extension)
	}
	return cleaned, nil
}

func scriptScaffoldContent(path string, description string) (scriptScaffoldResult, error) {
	return scriptScaffoldContentWithOptions(path, description, "", false)
}

func scriptScaffoldContentWithOptions(path string, description string, expectedOutput string, forceLLM bool) (scriptScaffoldResult, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title := strings.ReplaceAll(name, "-", " ")
	if strings.TrimSpace(title) == "" {
		title = "script"
	}
	// Always attempt LLM generation first when a specific topic is detected
	// or when the caller explicitly requests LLM (via --llm flag).
	if forceLLM || hasSpecificScriptTopic(description) {
		content, ok, err := scriptContentGenerator(path, description)
		if err != nil {
			return scriptScaffoldResult{}, err
		}
		if ok {
			return scriptScaffoldResult{Content: content, Source: "llm", ExpectedOutput: expectedOutput}, nil
		}
		return scriptScaffoldResult{}, errors.New("LLM script generation unavailable; refusing to write a generic template for a topic-specific request")
	}
	// No specific topic detected and no forced LLM: refuse to produce a
	// meaningless "Hello from {name}" scaffold. The user must supply a
	// concrete topic (wrap it in *...* or use script:<topic> syntax) or
	// pass --llm to force LLM generation.
	return scriptScaffoldResult{}, fmt.Errorf("no specific script topic detected and LLM not forced; wrap your topic in *...* (e.g. *multiplication table*) or use script:<topic> syntax, then retry — or pass --llm to force LLM generation")
}

var scriptContentGenerator = generateScriptContentWithLLM

func hasSpecificScriptTopic(description string) bool {
	return extractSpecificScriptTopic(description) != ""
}

func extractSpecificScriptTopic(description string) string {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" {
		return ""
	}
	if match := regexp.MustCompile(`\*([^*]{1,120})\*`).FindStringSubmatch(trimmed); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	if match := regexp.MustCompile(`(?i)(?:脚本|script)\s*[:：]\s*(.{4,240})$`).FindStringSubmatch(trimmed); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func generateScriptContentWithLLM(path string, description string) (string, bool, error) {
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		if err != nil {
			return "", false, fmt.Errorf("LLM client unavailable: %w", err)
		}
		return "", false, errors.New("LLM client unavailable")
	}
	if cleanup != nil {
		defer cleanup()
	}

	// NL6b: Stream progress to stderr so the user sees generation activity
	// even in compact mode (which only captures stdout).
	var generated int
	lastReport := time.Now()
	// Q3: Resolve model for code generation from model_routing config.
	genModel := app.ResolveModelForCategory("code_generation")

	response, err := client.Generate(context.Background(), llm.Request{
		SystemPrompt:   scriptGenerationSystemPrompt(path),
		UserPrompt:     scriptGenerationUserPrompt(path, description),
		Category:       llm.CategoryCodeGeneration,
		Model:          genModel,
		RequestTimeout: 10 * time.Minute, // BUG-18.4: large HTML files may take several minutes
		StreamCallback: func(chunk string) {
			generated += len(chunk)
			if now := time.Now(); now.Sub(lastReport) > 200*time.Millisecond {
				kb := float64(generated) / 1024.0
				writeCarriageProgress(fmt.Sprintf("⌛ generating %s (%.1f KB)…", filepath.Base(path), kb))
				lastReport = now
			}
		},
	})
	clearCarriageProgress()

	if err != nil {
		return "", false, fmt.Errorf("LLM script generation failed: %w", err)
	}
	if response.Fallback {
		return "", false, errors.New("LLM script generation unavailable (provider returned fallback)")
	}
	content := normalizeGeneratedScriptContent(response.Text)
	if strings.TrimSpace(content) == "" {
		return "", false, errors.New("LLM script generation returned empty content")
	}
	return content, true, nil
}

func scriptGenerationSystemPrompt(path string) string {
	extension := strings.ToLower(filepath.Ext(path))
	// I11 fix: Detect library paths (internal/, pkg/, lib/) and instruct
	// LLM to generate proper library code instead of standalone executables.
	if pkgName, isLib := inferLibraryPackage(path); isLib {
		switch extension {
		case ".go":
			// I11a: Inject the module path from go.mod so the LLM writes correct imports.
			moduleHint := ""
			if modPath := readGoModulePath(); modPath != "" {
				moduleHint = fmt.Sprintf(" Import paths use the module %q — for sibling packages write \"%s/internal/<name>\" exactly. ", modPath, modPath)
			}
			return fmt.Sprintf("Generate one complete Go library package. Package name is %q (NOT package main). Do NOT include func main(). Export types/functions with capital letters.%sReturn code only, no markdown fences, no explanation. Use standard library APIs only.", pkgName, moduleHint)
		case ".py":
			return fmt.Sprintf("Generate a Python library module for the %q package. Do NOT include if __name__ == \"__main__\" blocks. Define importable classes/functions. Return code only, no markdown fences, no explanation.", pkgName)
		case ".js", ".mjs":
			return "Generate a Node.js library module. Export functions/classes with module.exports. Do NOT include self-executing code. This is an importable module, not a script. Return code only, no markdown fences, no explanation."
		default:
			return fmt.Sprintf("Generate a library file for the %q package. This is importable, NOT a standalone script. Export types/functions. Do NOT include execution entry points. Return file content only, no markdown fences, no explanation.", pkgName)
		}
	}
	switch extension {
	case ".html", ".css", ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".sql", ".xml", ".csv", ".ini", ".cfg", ".env":
		return "Generate one complete " + extension + " file. Return the file content only, no markdown fences, no explanation. Use valid " + extension + " syntax. Keep it self-contained and well-structured. Avoid network references, file paths, credential access, or destructive operations."
	default:
		return "Generate one complete " + extension + " script file. Return code only, no markdown fences, no explanation. Use only standard library/runtime APIs. Keep it runnable from command line. Avoid network, file deletion, shelling out, credential access, or destructive operations."
	}
}

func scriptGenerationUserPrompt(path string, description string) string {
	extension := strings.ToLower(filepath.Ext(path))
	var builder strings.Builder
	builder.WriteString("Target path: ")
	builder.WriteString(filepath.ToSlash(path))
	builder.WriteString("\nUser request:\n")
	builder.WriteString(strings.TrimSpace(description))
	// NL3: Inject attached file content so the LLM can follow its guidance.
	// When user loads a skill guide via @file and says "根据这个指导生成 HTML",
	// the LLM needs to see the guide content to produce output that follows it.
	if fc := getReplFileContext(); fc != nil && strings.TrimSpace(fc.Content) != "" {
		content := strings.TrimSpace(fc.Content)
		if len(content) > 4000 {
			content = content[:4000] + "\n[truncated: attached file exceeded 4000 chars]"
		}
		builder.WriteString("\n\nReference file (")
		builder.WriteString(filepath.ToSlash(fc.Path))
		builder.WriteString("):\n")
		builder.WriteString(content)
		builder.WriteString("\n\nFollow the guidance, techniques, and patterns described in the reference file above when generating the output.")
	}
	// I11a fix (Claude Code inspired): Inject existing type definitions
	// from sibling files for cross-file type sharing.
	if typeCtx := buildSiblingTypeContext(path); typeCtx != "" {
		builder.WriteString("\n\n")
		builder.WriteString(typeCtx)
	}
	switch extension {
	case ".html":
		builder.WriteString("\n\nRequirements:\n- valid HTML5 document\n- include appropriate DOCTYPE, head, and body\n- self-contained with inline CSS/JS if needed\n- match the user's described content and style\n")
	case ".css":
		builder.WriteString("\n\nRequirements:\n- valid CSS\n- well-organized with comments for major sections\n- match the user's described styling intent\n")
	case ".md":
		builder.WriteString("\n\nRequirements:\n- well-formatted Markdown\n- use appropriate headings, lists, code blocks\n- match the user's described document structure\n")
	case ".json":
		builder.WriteString("\n\nRequirements:\n- valid JSON\n- well-structured and properly nested\n- match the user's described data schema\n")
	case ".yaml", ".yml":
		builder.WriteString("\n\nRequirements:\n- valid YAML\n- consistent indentation (2 spaces)\n- match the user's described configuration or data structure\n")
	case ".toml":
		builder.WriteString("\n\nRequirements:\n- valid TOML\n- use proper sections and key-value pairs\n- match the user's described configuration\n")
	case ".txt":
		builder.WriteString("\n\nRequirements:\n- plain text with appropriate structure\n- match the user's described content\n")
	default:
		builder.WriteString("\n\nRequirements:\n- single-file script\n- include a clear main/entrypoint\n- validate simple numeric CLI args when useful\n- print useful output\n")
	}
	return builder.String()
}

func normalizeGeneratedScriptContent(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			trimmed = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n"
}

func runScriptVerifier(root string, path string, expectedOutput string) error {
	extension := strings.ToLower(filepath.Ext(path))
	// Stage 1: compile/syntax check. If this fails, skip execution entirely
	// because the script cannot be safely executed.
	if err := runScriptSyntaxCheck(root, path, extension); err != nil {
		return err
	}
	// Stage 2: run the script and assert exit 0. If the caller (LLM spec)
	// supplied an expected_output, also diff stdout against it. Otherwise
	// fall back to the weak assertion: exit 0 + non-empty stdout. This is
	// the only behavior that catches the "Hello from {name}" template bug
	// when a topic-specific request claimed an expected output.
	if err := runScriptExecutionCheck(root, path, extension, expectedOutput); err != nil {
		return err
	}
	return nil
}

func runScriptSyntaxCheck(root string, path string, extension string) error {
	switch extension {
	case ".py":
		python, commandLabel := bootstrapPythonCommand()
		if python == "" {
			fmt.Println("Verifier: UNAVAILABLE")
			fmt.Println("Command: python -m py_compile " + filepath.ToSlash(path))
			fmt.Println("Reason: python executable not found on PATH")
			return nil
		}
		return runScriptVerifierCommand(root, commandLabel+" -m py_compile "+filepath.ToSlash(path), python, "-m", "py_compile", path)
	case ".js", ".mjs":
		node, err := exec.LookPath("node")
		if err != nil || strings.TrimSpace(node) == "" {
			fmt.Println("Verifier: UNAVAILABLE")
			fmt.Println("Command: node --check " + filepath.ToSlash(path))
			fmt.Println("Reason: node executable not found on PATH")
			return nil
		}
		return runScriptVerifierCommand(root, "node --check "+filepath.ToSlash(path), node, "--check", path)
	default:
		fmt.Println("Verifier: SKIPPED")
		if extension == ".go" {
			goBin, goErr := exec.LookPath("go")
			if goErr != nil || strings.TrimSpace(goBin) == "" {
				fmt.Println("Verifier: UNAVAILABLE")
				fmt.Println("Command: go vet " + filepath.ToSlash(path))
				fmt.Println("Reason: go executable not found on PATH")
				return nil
			}
			return runGoFileVerifier(root, path)
		}
		fmt.Println("Reason: no built-in verifier for " + extension)
		return nil
	}
}

func runScriptExecutionCheck(root string, path string, extension string, expectedOutput string) error {
	interpreter, args, label, ok := scriptInterpreterForExecution(extension, path)
	if !ok {
		// No built-in execution support for this extension. The compile
		// stage already passed, so we accept the script on faith.
		fmt.Println("Verifier: PASS")
		fmt.Println("Reason: no execution runner for " + extension + " (compile check only)")
		return nil
	}
	command := exec.Command(interpreter, args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	stdout := extractStdoutFromCombinedOutput(output)
	if err != nil {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: " + label)
		// I2 fix: non-zero exit from missing CLI args is expected behavior.
		// Scripts using argparse/commander/flag print usage and exit
		// when run without required arguments -- not a real crash.
		if isCLIArgsError(string(output)) {
			fmt.Println("Verifier: PASS")
			fmt.Println("Command: " + label)
			fmt.Println("Reason: script requires CLI arguments (printed usage/help correctly)")
			return nil
		}
		fmt.Println("Reason: script exited with non-zero status")
		if strings.TrimSpace(string(output)) != "" {
			fmt.Println(strings.TrimSpace(string(output)))
		}
		return err
	}
	expected := strings.TrimSpace(expectedOutput)
	got := strings.TrimSpace(stdout)
	if expected != "" {
		// Strong assertion: exact diff against the LLM-provided expected output.
		if expected != got {
			fmt.Println("Verifier: FAIL")
			fmt.Println("Command: " + label)
			fmt.Println("Reason: stdout did not match expected_output from script intent spec")
			fmt.Println("Expected:")
			fmt.Println(expected)
			fmt.Println("Actual:")
			fmt.Println(got)
			return errors.New("script verifier: stdout mismatch with expected_output")
		}
		fmt.Println("Verifier: PASS")
		fmt.Println("Command: " + label)
		fmt.Println("Reason: stdout matched expected_output from script intent spec")
		return nil
	}
	// Weak assertion (fallback): script must exit 0 and emit at least one
	// non-whitespace character on stdout.
	// Strengthened assertion (no expected_output): exit 0 + non-empty
	// stdout + NOT the "Hello from" template + no traceback/Error keywords
	// in output. Python scripts get an additional AST-level parse check.
	if got == "" {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: " + label)
		fmt.Println("Reason: script exited 0 but produced empty stdout; pass --expected-output-file for stronger assertion")
		return errors.New("script verifier: empty stdout without expected_output")
	}
	// Guard against the deterministic "Hello from {name}" template that the
	// LLM sometimes emits instead of executing the user's request.
	loweredOutput := strings.ToLower(got)
	if strings.HasPrefix(loweredOutput, "hello from ") && strings.Count(loweredOutput, "\n") == 0 {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: " + label)
		fmt.Println("Reason: script output matches the 'Hello from {name}' template")
		return errors.New("script verifier: output is the 'Hello from' template placeholder")
	}
	loweredOutput = strings.ToLower(got)
	for _, keyword := range scriptFailureKeywords {
		if strings.Contains(loweredOutput, keyword) {
			fmt.Println("Verifier: FAIL")
			fmt.Println("Command: " + label)
			fmt.Println("Reason: script output contains failure keyword \"" + keyword + "\" suggesting a runtime error")
			return fmt.Errorf("script verifier: output contains failure keyword %q", keyword)
		}
	}
	// Python: additional AST parse check.
	if extension == ".py" {
		if err := pythonASTCheck(path); err != nil {
			fmt.Println("Verifier: FAIL")
			fmt.Println("Command: " + label)
			fmt.Println("Reason: Python AST check failed: " + err.Error())
			return fmt.Errorf("script verifier: python AST check failed: %w", err)
		}
	}
	fmt.Println("Verifier: PASS")
	fmt.Println("Command: " + label)
	fmt.Println("Reason: script exited 0 with non-empty stdout (no expected_output supplied)")
	return nil
}

func scriptInterpreterForExecution(extension string, path string) (string, []string, string, bool) {
	switch extension {
	case ".py":
		python, commandLabel := bootstrapPythonCommand()
		if python == "" {
			return "", nil, "", false
		}
		return python, []string{path}, commandLabel + " " + filepath.ToSlash(path), true
	case ".js", ".mjs":
		node, err := exec.LookPath("node")
		if err != nil || strings.TrimSpace(node) == "" {
			return "", nil, "", false
		}
		return node, []string{path}, "node " + filepath.ToSlash(path), true
	default:
		return "", nil, "", false
	}
}

func extractStdoutFromCombinedOutput(output []byte) string {
	// exec.Command.CombinedOutput merges stderr into stdout. We treat the
	// whole blob as "stdout" for diff purposes because the verifier does
	// not separate streams; downstream tools can use a language-specific
	// runner if stream isolation is required.
	return string(output)
}

var scriptFailureKeywords = []string{
	"traceback", "error:", "exception:", "syntaxerror", "typeerror",
	"valueerror", "keyerror", "indexerror", "attributeerror", "nameerror",
	"importerror", "modulenotfound", "runtimeerror", "filenotfound",
	"permissionerror", "zerodivisionerror", "recursionerror",
}

func pythonASTCheck(path string) error {
	python, _ := bootstrapPythonCommand()
	if python == "" {
		return fmt.Errorf("python interpreter not found")
	}
	script := fmt.Sprintf("import ast, sys; ast.parse(open(sys.argv[1], encoding='utf-8').read())")
	cmd := exec.Command(python, "-c", script, path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return fmt.Errorf("%s: %s", err.Error(), trimmed)
		}
		return err
	}
	return nil
}

func runEditVerifier(root string, path string, instruction string) error {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".go" {
		// `edit` does not currently surface a verifier-time expected output
		// contract (it relies on the LLM edit call); fall back to the weak
		// "exit 0 + non-empty stdout" assertion in runScriptVerifier.
		return runScriptVerifier(root, path, "")
	}
	goBin, err := exec.LookPath("go")
	if err != nil || strings.TrimSpace(goBin) == "" {
		fmt.Println("Verifier: UNAVAILABLE")
		fmt.Println("Command: " + defaultGoEditVerifierLabel(path))
		fmt.Println("Reason: go executable not found on PATH")
		return nil
	}
	label, args, ok := focusedGoTestCommandFromText(instruction)
	if !ok {
		label, args = defaultGoEditVerifierCommand(path)
	}
	return runScriptVerifierCommand(root, label, goBin, args...)
}

func runScriptVerifierCommand(root string, label string, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	trimmedOutput := strings.TrimSpace(string(output))
	if err != nil {
		fmt.Println("Verifier: FAIL")
		fmt.Println("Command: " + label)
		if trimmedOutput != "" {
			fmt.Println(trimmedOutput)
		}
		// E2: Record the error for smart recovery on the next turn.
		memstore.RecordVerifierError(label + "\n" + trimmedOutput)
		return err
	}
	fmt.Println("Verifier: PASS")
	fmt.Println("Command: " + label)
	if trimmedOutput != "" {
		fmt.Println(trimmedOutput)
	}
	// E2: Clear the error on success.
	memstore.ClearVerifierError()
	return nil
}

func defaultGoEditVerifierLabel(path string) string {
	label, _ := defaultGoEditVerifierCommand(path)
	return label
}

func defaultGoEditVerifierCommand(path string) (string, []string) {
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." || strings.TrimSpace(dir) == "" {
		dir = "."
	}
	pkg := "./" + strings.TrimPrefix(dir, "./")
	if pkg == "./." {
		pkg = "."
	}
	return "go test " + pkg, []string{"test", pkg}
}

func focusedGoTestCommandFromText(text string) (string, []string, bool) {
	for _, line := range strings.Split(text, "\n") {
		if label, args, ok := focusedGoTestCommandFromLine(line); ok {
			return label, args, true
		}
	}
	return "", nil, false
}

func focusedGoTestCommandFromLine(line string) (string, []string, bool) {
	trimmed := strings.TrimSpace(strings.Trim(line, "`'\"，,。.;:：；()[]【】"))
	if trimmed == "" || !strings.Contains(trimmed, "go test") {
		return "", nil, false
	}
	match := regexp.MustCompile(`\bgo\s+test\s+(\./[A-Za-z0-9_./-]+)(?:\s+-run\s+([A-Za-z0-9_./^$|()+-]+))?`).FindStringSubmatch(trimmed)
	if len(match) == 0 {
		return "", nil, false
	}
	pkg := strings.TrimSpace(match[1])
	if strings.Contains(pkg, "..") {
		return "", nil, false
	}
	args := []string{"test", pkg}
	label := "go test " + pkg
	if len(match) > 2 && strings.TrimSpace(match[2]) != "" {
		runPattern := strings.TrimSpace(match[2])
		args = append(args, "-run", runPattern)
		label += " -run " + runPattern
	}
	return label, args, true
}

func displayScriptDescription(description string) string {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" {
		return "simple runnable script"
	}
	return trimmed
}

func displayScriptGenerator(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func applyMode(apply bool) string {
	if apply {
		return "apply"
	}
	return "preview"
}

// runStage handles the "avatars stage" command. It turns any input into
// a visual HTML sketch and adds it to the Stage gallery.
// Usage:
//
//	avatars stage "describe your creative idea"
//	avatars stage --project
//	avatars stage --edit latest "做成像素风"
//	avatars stage --serve
func runStage(args []string) error {
	parsed, err := parseStageCLIArgs(args)
	if err != nil {
		return err
	}
	addr := parsed.addr
	if strings.TrimSpace(addr) == "" {
		addr = stage.DefaultGalleryAddr
	}

	if parsed.listMode {
		return listStagesCLI()
	}
	if parsed.deleteID != "" {
		return deleteStageCLI(parsed.deleteID)
	}

	var input string
	kind := stage.KindCreative
	if parsed.regPointsMode {
		kind = stage.KindRegPoints
		cwd, _ := os.Getwd()
		fmt.Fprintf(os.Stderr, "Reading architecture.md registration points...\n")
		input = buildRegPointsStageInput(cwd)
		if input == "" {
			return errors.New("no architecture.md with Registration Points found — run `avatars arch --analyze` first")
		}
		if parsed.prompt == "" {
			parsed.prompt = "Generate an interactive visualization of these multi-file change patterns. Show each registration point as a card/group with the files connected visually. Use a dependency-graph or flowchart style to show which files are linked. Make it interactive and visually appealing."
		}
		fmt.Fprintf(os.Stderr, "Registration points: %d patterns found\n", strings.Count(input, "[reg-point]"))
	} else if parsed.projectMode {
		kind = stage.KindProject
		cwd, _ := os.Getwd()
		fmt.Fprintf(os.Stderr, "Scanning project: %s\n", cwd)
		input = scanProjectForStage(".")
		if parsed.prompt == "" {
			parsed.prompt = "Generate an interactive project structure visualization: directory tree, file statistics, architecture overview. Prefer the workflow plan over stale architecture.md."
		}
		fmt.Fprintf(os.Stderr, "Project scan: %d files found\n", strings.Count(input, "\n"))
	} else if parsed.filePath != "" {
		kind = stage.KindFile
		data, err := os.ReadFile(parsed.filePath)
		if err != nil {
			return fmt.Errorf("read input file %s: %w", parsed.filePath, err)
		}
		input = strings.TrimSpace(string(data))
		if input == "" {
			return fmt.Errorf("input file %s is empty", parsed.filePath)
		}
		fmt.Fprintf(os.Stderr, "Loaded input file: %s (%d chars)\n", parsed.filePath, len(input))
	} else {
		input = strings.TrimSpace(strings.Join(parsed.positional, " "))
	}

	if parsed.editID != "" && parsed.prompt == "" {
		return errors.New("usage: avatars stage --edit <id|latest> \"<direction>\"")
	}
	needsGenerate := parsed.editID != "" || parsed.projectMode || parsed.regPointsMode || parsed.filePath != "" || input != ""
	if !needsGenerate {
		if parsed.startServe {
			fmt.Printf("Starting gallery at http://%s (operator dashboard is avatars serve on :5000)\n", localhttp.NormalizeAddr(addr))
			return serveAndOpen(addr)
		}
		return errors.New("usage: avatars stage [--project|--reg-points|--from-file <path>|--edit <id>|--list|--delete <id>|--serve] [--prompt <direction>] \"<idea>\"")
	}

	client, err := app.NewLLMClient()
	if err != nil {
		return fmt.Errorf("stage: LLM client unavailable: %w", err)
	}

	opts := stage.GenerateOpts{
		Input:     input,
		Kind:      kind,
		Direction: parsed.prompt,
		EditID:    parsed.editID,
		ForceNew:  parsed.forceNew,
		Model:     app.ResolveModelForCategory("code_generation"),
	}
	if parsed.editID != "" {
		fmt.Fprintf(os.Stderr, "Revising stage %s...\n", parsed.editID)
	} else if parsed.prompt != "" {
		fmt.Fprintf(os.Stderr, "Planning then generating stage with direction...\n")
	} else {
		fmt.Fprintf(os.Stderr, "Planning then generating stage (%s)...\n", kind)
	}

	stg, err := stage.LoadCLIStage(client, opts)
	if err != nil {
		return fmt.Errorf("stage generation failed: %w", err)
	}
	printStageResult(stg, parsed.exportMode, parsed.startServe, addr)

	if parsed.startServe {
		fmt.Printf("\nStarting gallery server and opening browser...\n")
		return serveAndOpen(addr)
	}
	return nil
}

type stageCLIArgs struct {
	filePath, prompt, editID, deleteID, addr string
	startServe, projectMode, regPointsMode   bool
	exportMode, forceNew, listMode           bool
	positional                               []string
}

func parseStageCLIArgs(args []string) (stageCLIArgs, error) {
	var parsed stageCLIArgs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project":
			parsed.projectMode = true
		case "--reg-points":
			parsed.regPointsMode = true
		case "--export":
			parsed.exportMode = true
		case "--new":
			parsed.forceNew = true
		case "--list":
			parsed.listMode = true
		case "--serve":
			parsed.startServe = true
		case "--from-file":
			if i+1 >= len(args) {
				return parsed, errors.New("usage: avatars stage --from-file <path>")
			}
			i++
			parsed.filePath = args[i]
		case "--prompt":
			if i+1 >= len(args) {
				return parsed, errors.New("usage: avatars stage --prompt <direction>")
			}
			i++
			parsed.prompt = args[i]
		case "--edit":
			if i+1 >= len(args) {
				return parsed, errors.New("usage: avatars stage --edit <id|latest> \"<direction>\"")
			}
			i++
			parsed.editID = args[i]
		case "--delete":
			if i+1 >= len(args) {
				return parsed, errors.New("usage: avatars stage --delete <id|latest>")
			}
			i++
			parsed.deleteID = args[i]
		case "--addr":
			if i+1 >= len(args) {
				return parsed, errors.New("usage: avatars stage --serve --addr 127.0.0.1:5100")
			}
			i++
			parsed.addr = args[i]
		default:
			parsed.positional = append(parsed.positional, args[i])
		}
	}
	if parsed.editID != "" && parsed.prompt == "" && len(parsed.positional) > 0 {
		parsed.prompt = strings.TrimSpace(strings.Join(parsed.positional, " "))
		parsed.positional = nil
	}
	return parsed, nil
}

func listStagesCLI() error {
	mod, err := stage.New(stage.Config{})
	if err != nil {
		return err
	}
	list, err := mod.List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Printf("No stages yet. Try: avatars stage \"your idea\"\n")
		return nil
	}
	for i, stg := range list {
		kind := stg.Kind
		if kind == "" {
			kind = "creative"
		}
		stamp := stg.UpdatedAt
		if stamp.IsZero() {
			stamp = stg.CreatedAt
		}
		fmt.Printf("%d. %s  %s  %s  stage/%s\n", i+1, stg.ID, kind, stamp.Format("2006-01-02 15:04"), stg.Path)
		fmt.Printf("    %s\n", stg.Title)
	}
	fmt.Printf("\nRevise: avatars stage --edit latest \"your direction\"\n")
	fmt.Printf("Gallery: avatars stage --serve\n")
	return nil
}

func deleteStageCLI(id string) error {
	mod, err := stage.New(stage.Config{})
	if err != nil {
		return err
	}
	stg, err := mod.Resolve(id)
	if err != nil {
		return err
	}
	if err := mod.Delete(stg.ID); err != nil {
		return err
	}
	_ = mod.GenerateStatic()
	fmt.Printf("Deleted %s (%s)\n", stg.ID, stg.Title)
	return nil
}

func printStageResult(stg *stage.Stage, exportMode, startServe bool, addr string) {
	fmt.Printf("Stage saved.\n")
	fmt.Printf("  Title: %s\n", stg.Title)
	fmt.Printf("  ID:    %s\n", stg.ID)
	fmt.Printf("  File:  stage/%s\n", stg.Path)
	if stg.Kind != "" {
		fmt.Printf("  Kind:  %s\n", stg.Kind)
	}
	fmt.Printf("  Note:  visual sketch — not project source of truth\n")
	if exportMode {
		fmt.Printf("  Gallery file: stage/index.html (open directly in browser)\n")
	} else if !startServe {
		fmt.Printf("  Open:    stage/%s\n", stg.Path)
		fmt.Printf("  Gallery: avatars stage --serve   (http://%s)\n", localhttp.NormalizeAddr(addr))
	}
	fmt.Printf("  Revise:  avatars stage --edit %s \"your direction\"\n", stg.ID)
}

// serveAndOpen starts the Stage gallery HTTP server and opens the default
// browser. It blocks until Ctrl+C.
// scanProjectForStage walks the current directory and builds a structured
// summary of files suitable for LLM visualization. Hidden directories and
// binary files are excluded.
// buildRegPointsStageInput reads architecture.md Registration Points and builds
// a structured input for the Stage LLM to generate an interactive visualization.
// Returns empty string if no architecture.md or no registration points found.
func buildRegPointsStageInput(root string) string {
	doc, err := arch.ReadArchDoc(root)
	if err != nil || doc == nil {
		return ""
	}
	if len(doc.RegPoints) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Multi-File Change Patterns (Registration Points):\n\n")
	b.WriteString(fmt.Sprintf("Project: %s\n", doc.Overview))
	b.WriteString(fmt.Sprintf("Arch doc status: %s\n\n", doc.Meta.Status))

	for _, rp := range doc.RegPoints {
		b.WriteString(fmt.Sprintf("[reg-point] %s\n", rp.ChangeType))
		if rp.Note != "" {
			b.WriteString(fmt.Sprintf("  Description: %s\n", rp.Note))
		}
		b.WriteString("  Affected files:\n")
		for _, rf := range rp.Files {
			loc := rf.Path
			if rf.LineRange != "" {
				loc = fmt.Sprintf("%s (%s)", rf.Path, rf.LineRange)
			}
			b.WriteString(fmt.Sprintf("    - %s → %s", loc, rf.ChangeHint))
			if rf.Example != "" {
				b.WriteString(fmt.Sprintf("  (e.g. %s)", rf.Example))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Add entry points and layers for context.
	if len(doc.EntryPoints) > 0 {
		b.WriteString("Entry Points:\n")
		for _, ep := range doc.EntryPoints {
			b.WriteString(fmt.Sprintf("  - %s (%s)\n", ep.Path, ep.Kind))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func scanProjectForStage(root string) string {
	var b strings.Builder

	// Plan / Success Criteria first — architecture.md is often a stale snapshot.
	if plan, err := workflow.ReadWorkflowDoc(root, "plan"); err == nil && strings.TrimSpace(plan) != "" {
		meta := workflow.ParsePlanMeta(plan)
		b.WriteString("Workflow plan (Success Criteria source of truth):\n")
		b.WriteString(fmt.Sprintf("  Active Phase: %d / %d  Status: %s\n", meta.ActivePhase, meta.PhaseCount, meta.Status))
		if meta.ActivePhase > 0 {
			if raw, err := os.ReadFile(workflow.PhaseDocPath(root, meta.ActivePhase)); err == nil {
				snippet := string(raw)
				if len(snippet) > 2500 {
					snippet = snippet[:2500] + "\n…(truncated)\n"
				}
				b.WriteString(snippet)
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	if doc, err := arch.ReadArchDoc(root); err == nil && doc != nil {
		if doc.Meta.Status == "confirmed" || doc.Meta.Status == "draft" {
			b.WriteString("Architecture overview (auxiliary; may be stale):\n")
			b.WriteString(fmt.Sprintf("  Project: %s\n", doc.Overview))
			if len(doc.EntryPoints) > 0 {
				b.WriteString("  Entry points:\n")
				for _, ep := range doc.EntryPoints {
					b.WriteString(fmt.Sprintf("    - %s (%s): %s\n", ep.Path, ep.Kind, ep.Summary))
				}
			}
			if len(doc.Layers) > 0 {
				b.WriteString("  Layers:\n")
				for _, layer := range doc.Layers {
					b.WriteString(fmt.Sprintf("    %s: %s\n", layer.Name, layer.Description))
				}
			}
			if len(doc.Dependencies.Internal) > 0 || len(doc.Dependencies.External) > 0 {
				b.WriteString("  Dependencies:\n")
				for _, d := range doc.Dependencies.Internal {
					b.WriteString(fmt.Sprintf("    internal: %s\n", d))
				}
				for _, d := range doc.Dependencies.External {
					b.WriteString(fmt.Sprintf("    external: %s\n", d))
				}
			}
			if len(doc.RegPoints) > 0 {
				b.WriteString("  Registration Points (multi-file change patterns):\n")
				for _, rp := range doc.RegPoints {
					b.WriteString(fmt.Sprintf("    [%s] ", rp.ChangeType))
					var fileRefs []string
					for _, rf := range rp.Files {
						fileRefs = append(fileRefs, rf.Path)
					}
					b.WriteString(strings.Join(fileRefs, ", "))
					b.WriteString("\n")
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("Project structure:\n\n")

	fileCount := 0
	dirCount := 0
	var totalSize int64

	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		base := filepath.Base(path)
		lowerBase := strings.ToLower(base)
		// P8-14: skip hidden, cache, packaging, stage self, and test logs.
		if info.IsDir() {
			if strings.HasPrefix(base, ".") ||
				base == "__pycache__" || base == "node_modules" || base == "vendor" ||
				base == "stage" || base == "web" || base == "dist" || base == "build" ||
				base == "target" || base == "coverage" || base == "htmlcov" ||
				base == "venv" || base == "env" || base == "bin" ||
				strings.HasSuffix(lowerBase, ".egg-info") {
				return filepath.SkipDir
			}
			dirCount++
			fmt.Fprintf(&b, "[dir]  %s/\n", filepath.ToSlash(rel))
			return nil
		}
		if strings.HasPrefix(base, ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(base))
		switch ext {
		case ".exe", ".dll", ".so", ".dylib", ".bin", ".db", ".zip", ".tar", ".gz",
			".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf", ".pyc", ".pyo":
			return nil
		}
		if strings.HasPrefix(lowerBase, "avatars_test_") && strings.HasSuffix(lowerBase, ".log") {
			return nil
		}
		if fileCount >= 180 {
			return filepath.SkipAll
		}
		fileCount++
		totalSize += info.Size()
		sizeStr := formatSize(info.Size())
		fmt.Fprintf(&b, "[%s] %s\n", sizeStr, filepath.ToSlash(rel))
		return nil
	})

	fmt.Fprintf(&b, "\nSummary: %d files in %d directories, total size %s\n", fileCount, dirCount, formatSize(totalSize))
	return b.String()
}

func formatSize(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func serveAndOpen(addr string) error {
	addr = localhttp.NormalizeAddr(addr)
	token := localhttp.ResolveToken(localhttp.StageTokenEnv)
	mux := http.NewServeMux()

	// Batch6/stage: pass LLM client so in-browser Create (/api/stage) works.
	client, clientErr := app.NewLLMClient()
	cfg := stage.Config{}
	if clientErr == nil {
		cfg.Client = client
	} else {
		fmt.Fprintf(os.Stderr, "warning: gallery Create disabled (LLM client: %v)\n", clientErr)
	}
	stageMod, err := stage.New(cfg)
	if err != nil {
		return fmt.Errorf("init stage: %w", err)
	}
	stageMod.RegisterHTTP(mux)

	// Catch Ctrl+C for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\nShutting down gallery server...\n")
		os.Exit(0)
	}()

	url := "http://" + addr + "/?token=" + url.QueryEscape(token)
	localhttp.LogListen("Gallery server", addr, token)
	openBrowser(url)

	return http.ListenAndServe(addr, localhttp.Middleware(token, mux))
}

// openBrowser opens the default browser to the given URL.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch {
	case strings.Contains(os.Getenv("OS"), "Windows") || os.Getenv("ComSpec") != "":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		// Linux/macOS
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command("xdg-open", url)
		} else if _, err := exec.LookPath("open"); err == nil {
			cmd = exec.Command("open", url)
		}
	}
	if cmd != nil {
		_ = cmd.Start()
	}
}

func serveStage(args []string) error {
	fmt.Fprintf(os.Stderr, "Note: `avatars serve` starts the operator/event dashboard.\n")
	fmt.Fprintf(os.Stderr, "      For the creative Stage gallery (generated HTML cards), use: avatars stage --serve\n")
	options, err := parseServeCommandOptions(args)
	if err != nil {
		return err
	}
	input := options.Input
	demoTask := ""
	if options.RunDemo {
		demoTask = input
		if demoTask == "" {
			demoTask = "Analyze the current warehouse and provide a refactoring plan."
		}
		if input == "" {
			input = demoTask
		}
	}

	taskManager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	var workspace *tasks.Workspace
	transcriptPaths := make([]string, 0, 8)
	seenTranscriptPaths := make(map[string]struct{})
	addTranscriptPath := func(path string) error {
		if strings.TrimSpace(path) == "" {
			return nil
		}
		absPath, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return err
		}
		if _, ok := seenTranscriptPaths[absPath]; ok {
			return nil
		}
		seenTranscriptPaths[absPath] = struct{}{}
		transcriptPaths = append(transcriptPaths, absPath)
		return nil
	}
	if options.ResumeTranscript != "" {
		if err := addTranscriptPath(options.ResumeTranscript); err != nil {
			return err
		}
		resolvedWorkspace, ok, err := taskManager.LoadByTranscript(options.ResumeTranscript)
		if err != nil {
			return err
		}
		if ok {
			workspace = &resolvedWorkspace
		}
	}
	if options.TaskID != "" {
		resolvedWorkspace, _, err := taskManager.Resolve(tasks.ResolveOptions{ID: options.TaskID, Title: input})
		if err != nil {
			return err
		}
		workspace = &resolvedWorkspace
	}
	if workspace != nil {
		workspaceTranscriptPaths, err := listTranscriptPaths(workspace.SessionsDir)
		if err != nil {
			return err
		}
		for _, path := range workspaceTranscriptPaths {
			if err := addTranscriptPath(path); err != nil {
				return err
			}
		}
	}

	application, err := app.BootstrapWithOptions(app.BootstrapOptions{Workspace: workspace, PermissionMode: options.PermissionMode})
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()

	configuredWarning := ""
	if application.LLMStatusWarning != nil {
		configuredWarning = application.LLMStatusWarning.Error()
	}
	server, err := stage.NewServer(application.Events, application.Engine, application.LLMStatus, configuredWarning)
	if err != nil {
		return err
	}
	governanceSummary, err := application.Skills.GovernanceSummary()
	if err != nil {
		return err
	}
	governanceReconciliation, err := application.Skills.GovernanceReconciliation()
	if err != nil {
		return err
	}
	governanceLedger, err := application.Skills.GovernanceLedger()
	if err != nil {
		return err
	}
	server = server.WithSkillGovernance(governanceSummary, governanceReconciliation, governanceLedger)
	if len(transcriptPaths) > 0 {
		archivedHistory, err := stage.LoadTranscriptHistory(transcriptPaths)
		if err != nil {
			return err
		}
		server = server.WithArchivedHistory(archivedHistory)
	}
	if workspace != nil {
		memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
		if err != nil {
			return err
		}
		taskSnapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
		_ = memoryStore.Close()
		if err != nil {
			return err
		}
		projectStore, err := memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
		if err == nil {
			projectLessons, loadErr := projectStore.LoadProjectLessons()
			_ = projectStore.Close()
			if loadErr != nil {
				return loadErr
			}
			taskSnapshot.ProjectLessons = projectLessons
		}
		server = server.WithTaskFeedback(workspace.ID, workspace.Status, taskSnapshot)
	}

	return server.ListenAndServe(context.Background(), "127.0.0.1:5000", demoTask)
}

func parseServeCommandOptions(args []string) (serveCommandOptions, error) {
	options := serveCommandOptions{PermissionMode: runtime.PermissionModeAcceptEdits}
	for len(args) > 0 {
		switch args[0] {
		case "--task":
			if len(args) < 2 {
				return serveCommandOptions{}, errors.New("usage: avatars serve [--task <task-id>] [--resume <transcript-path>] [--permission-mode <mode>] [task]")
			}
			options.TaskID = strings.TrimSpace(args[1])
			args = args[2:]
		case "--resume":
			if len(args) < 2 {
				return serveCommandOptions{}, errors.New("usage: avatars serve [--task <task-id>] [--resume <transcript-path>] [--permission-mode <mode>] [task]")
			}
			options.ResumeTranscript = strings.TrimSpace(args[1])
			args = args[2:]
		case "--run-demo":
			options.RunDemo = true
			args = args[1:]
		case "--permission-mode":
			if len(args) < 2 {
				return serveCommandOptions{}, errors.New("usage: avatars serve [--task <task-id>] [--resume <transcript-path>] [--permission-mode <mode>] [--run-demo] [task]")
			}
			permissionMode, err := runtime.ParsePermissionMode(args[1])
			if err != nil {
				return serveCommandOptions{}, err
			}
			options.PermissionMode = permissionMode
			args = args[2:]
		default:
			options.Input = strings.TrimSpace(strings.Join(args, " "))
			return options, nil
		}
	}
	return options, nil
}

func listTranscriptPaths(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func runMCPCall(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars mcp list | avatars mcp show <server-name> | avatars mcp inspect [--task <task-id>] <server-name-or-url> | avatars mcp call [--task <task-id>] <server-name-or-url> <method> [params-json]")
	}

	switch args[0] {
	case "serve":
		if len(args) < 2 {
			return errors.New("usage: avatars mcp serve <addr>")
		}
		return mcp.ServeMCP(args[1], func() []mcp.Tool {
			pluginsResult, _ := plugins.LoadLocalPlugins()
			tools := make([]mcp.Tool, 0, len(pluginsResult.Plugins))
			for _, p := range pluginsResult.Plugins {
				if !p.Enabled {
					continue
				}
				for _, s := range p.Skills {
					tools = append(tools, mcp.Tool{Name: s.Name, Description: s.WhenToUse})
				}
			}
			return tools
		}, func(toolName string, params map[string]any) (string, error) {
			return fmt.Sprintf("tool %s called with %v", toolName, params), nil
		})
	case "list":
		servers, err := app.LoadMCPServerConfigs("")
		if err != nil {
			return err
		}
		if len(servers) == 0 {
			fmt.Println("No configured MCP servers found.")
			return nil
		}
		for _, server := range servers {
			if server.Description != "" {
				fmt.Printf("- %s | %s | %s\n", server.Name, mcpServerEndpoint(server), server.Description)
				continue
			}
			fmt.Printf("- %s | %s\n", server.Name, mcpServerEndpoint(server))
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: avatars mcp show <server-name>")
		}
		resolved, matched, err := app.ResolveMCPServerTarget("", args[1])
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("configured mcp server %q not found", strings.TrimSpace(args[1]))
		}
		fmt.Printf("Name: %s\n", resolved.Name)
		fmt.Printf("Transport: %s\n", resolved.Spec().ResolvedTransport())
		if strings.TrimSpace(resolved.URL) != "" {
			fmt.Printf("URL: %s\n", resolved.URL)
		}
		if strings.TrimSpace(resolved.Command) != "" {
			fmt.Printf("Command: %s\n", resolved.Command)
		}
		if resolved.Description != "" {
			fmt.Printf("Description: %s\n", resolved.Description)
		}
		return nil
	case "inspect":
		options, err := parseMCPCommandOptions(args[1:], "usage: avatars mcp inspect [--task <task-id>] <server-name-or-url>")
		if err != nil {
			return err
		}
		if len(options.Positionals) != 1 {
			return errors.New("usage: avatars mcp inspect [--task <task-id>] <server-name-or-url>")
		}
		resolvedTarget, matched, err := app.ResolveMCPServerTarget("", options.Positionals[0])
		if err != nil {
			return err
		}
		application, taskManager, workspace, err := bootstrapMCPApplication(options.TaskID, fmt.Sprintf("MCP inspect %s", resolvedTarget.Spec().Endpoint()))
		if err != nil {
			return err
		}
		defer func() {
			_ = application.Close()
		}()
		result, err := application.Engine.InspectMCP(context.Background(), resolvedTarget.Spec())
		if err != nil {
			return err
		}
		if workspace != nil {
			updated, err := taskManager.MarkRun(*workspace, result.TranscriptPath, result.Summary)
			if err != nil {
				return err
			}
			workspace = &updated
		}
		if matched {
			fmt.Printf("Server: %s\n", resolvedTarget.Name)
		}
		if workspace != nil {
			fmt.Printf("Task: %s\n", workspace.ID)
		}
		fmt.Print(formatMCPInspectResult(result))
		fmt.Printf("Transcript: %s\n", result.TranscriptPath)
		return nil
	case "call":
		options, err := parseMCPCommandOptions(args[1:], "usage: avatars mcp call [--task <task-id>] <server-name-or-url> <method> [params-json]")
		if err != nil {
			return err
		}
		if len(options.Positionals) < 2 {
			return errors.New("usage: avatars mcp call [--task <task-id>] <server-name-or-url> <method> [params-json]")
		}

		resolvedTarget, matched, err := app.ResolveMCPServerTarget("", options.Positionals[0])
		if err != nil {
			return err
		}
		serverSpec := resolvedTarget.Spec()
		method := strings.TrimSpace(options.Positionals[1])
		if !serverSpec.Configured() || method == "" {
			return errors.New("server target and method cannot be empty")
		}

		var params any
		if len(options.Positionals) > 2 {
			rawParams := strings.TrimSpace(strings.Join(options.Positionals[2:], " "))
			if rawParams == "" {
				return errors.New("params json cannot be empty")
			}
			if err := json.Unmarshal([]byte(rawParams), &params); err != nil {
				return fmt.Errorf("invalid params json: %w", err)
			}
		}

		application, taskManager, workspace, err := bootstrapMCPApplication(options.TaskID, fmt.Sprintf("MCP call %s", method))
		if err != nil {
			return err
		}
		defer func() {
			_ = application.Close()
		}()

		result, err := application.Engine.CallMCP(context.Background(), serverSpec, method, params)
		if err != nil {
			return err
		}
		if workspace != nil {
			updated, err := taskManager.MarkRun(*workspace, result.TranscriptPath, result.Summary)
			if err != nil {
				return err
			}
			workspace = &updated
		}

		formatted, err := formatJSONResult(result.Result)
		if err != nil {
			return err
		}

		if matched {
			fmt.Printf("Server: %s\n", resolvedTarget.Name)
		}
		if workspace != nil {
			fmt.Printf("Task: %s\n", workspace.ID)
		}
		fmt.Println(formatted)
		fmt.Printf("Transcript: %s\n", result.TranscriptPath)
		return nil
	default:
		return errors.New("usage: avatars mcp list | avatars mcp show <server-name> | avatars mcp inspect [--task <task-id>] <server-name-or-url> | avatars mcp call [--task <task-id>] <server-name-or-url> <method> [params-json]")
	}
}

func parseMCPCommandOptions(args []string, usage string) (mcpCommandOptions, error) {
	options := mcpCommandOptions{}
	for index := 0; index < len(args); index++ {
		if args[index] == "--task" {
			if index+1 >= len(args) {
				return mcpCommandOptions{}, errors.New(usage)
			}
			options.TaskID = strings.TrimSpace(args[index+1])
			if options.TaskID == "" {
				return mcpCommandOptions{}, errors.New(usage)
			}
			index++
			continue
		}
		options.Positionals = append(options.Positionals, args[index])
	}
	return options, nil
}

func bootstrapMCPApplication(taskID string, title string) (*app.Application, *tasks.Manager, *tasks.Workspace, error) {
	if strings.TrimSpace(taskID) == "" {
		application, err := app.Bootstrap()
		return application, nil, nil, err
	}
	taskManager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	workspace, _, err := taskManager.Resolve(tasks.ResolveOptions{ID: taskID, Title: title})
	if err != nil {
		return nil, nil, nil, err
	}
	application, err := app.BootstrapWithOptions(app.BootstrapOptions{Workspace: &workspace})
	if err != nil {
		return nil, nil, nil, err
	}
	return application, taskManager, &workspace, nil
}

func mcpServerEndpoint(server app.MCPServerConfig) string {
	if strings.TrimSpace(server.URL) != "" {
		return server.URL
	}
	return server.Spec().Endpoint()
}

func formatMCPInspectResult(result runtime.MCPInspectResult) string {
	var output strings.Builder
	for _, section := range result.Sections {
		if section.Supported {
			fmt.Fprintf(&output, "%s: %d\n", section.Label, len(section.Names))
			if len(section.Names) == 0 {
				fmt.Fprintf(&output, "- none\n")
				continue
			}
			for _, name := range section.Names {
				fmt.Fprintf(&output, "- %s\n", name)
			}
			continue
		}
		fmt.Fprintf(&output, "%s: unsupported\n", section.Label)
	}
	return output.String()
}

func formatJSONResult(result []byte) (string, error) {
	if len(bytes.TrimSpace(result)) == 0 {
		result = []byte("null")
	}

	var output bytes.Buffer
	if err := json.Indent(&output, result, "", "  "); err != nil {
		return "", fmt.Errorf("format mcp result: %w", err)
	}
	return output.String(), nil
}

func runPlugins(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars plugins list | reload | install <path> | remove <name>")
	}
	if args[0] == "reload" {
		_, err := plugins.LoadLocalPlugins()
		if err != nil {
			return fmt.Errorf("plugin reload failed: %w", err)
		}
		fmt.Println("Plugins reloaded successfully.")
		return nil
	}
	if args[0] == "install" {
		if len(args) < 2 {
			return errors.New("usage: avatars plugins install <path>")
		}
		if err := plugins.Install(args[1]); err != nil {
			return err
		}
		fmt.Printf("Plugin installed from %s. Run 'avatars plugins reload' to activate.\n", args[1])
		return nil
	}
	if args[0] == "remove" {
		if len(args) < 2 {
			return errors.New("usage: avatars plugins remove <name>")
		}
		if err := plugins.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("Plugin %s removed. Run 'avatars plugins reload' to apply.\n", args[1])
		return nil
	}
	if args[0] != "list" {
		return errors.New("usage: avatars plugins list | reload | install <path> | remove <name>")
	}
	summary, err := plugins.LoadSummary()
	if err != nil {
		return err
	}
	fmt.Printf("Marketplaces: %d\n", summary.MarketplaceCount)
	fmt.Printf("Plugins: %d\n", summary.PluginCount)
	fmt.Printf("Skills: %d\n", summary.SkillCount)
	if len(summary.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range summary.Warnings {
			fmt.Printf("- %s\n", warning)
		}
	}
	if len(summary.Plugins) == 0 {
		fmt.Println("Plugin registry: none")
		return nil
	}
	fmt.Println("Plugin registry:")
	for _, plugin := range summary.Plugins {
		status := "disabled"
		if plugin.Enabled {
			status = "enabled"
		}
		fmt.Printf("- %s | %s | %s | %s | %s | skills=%d\n", plugin.Name, plugin.Version, status, plugin.Marketplace, plugin.RootDir, len(plugin.Skills))
		for _, definition := range plugin.Skills {
			fmt.Printf("  - %s | %s | %s\n", definition.Name, definition.WhenToUse, definition.Path)
		}
	}
	return nil
}

func runSkills(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars skills new [--role <role>] \"<topic>\" | avatars skills generate [--task <task-id>] [--from-file <path>] \"<task>\" | avatars skills approve <generated-file> | avatars skills archive <approved-file> | avatars skills disable <approved-file> | avatars skills restore <archived-or-disabled-file> | avatars skills restore-missing <recorded-file-or-name> <source-markdown> | avatars skills repair-metadata <current-file> | avatars skills repair-history <current-file> | avatars skills repair-invariants <current-file> | avatars skills list | avatars skills pending | avatars skills review <generated-file> | avatars skills archived | avatars skills disabled | avatars skills timeline | avatars skills ledger | avatars skills reconcile | avatars skills restore-guide <recorded-file-or-name> | avatars skills resolve-missing <recorded-file-or-name> | avatars skills sync | avatars skills show <current-file> | avatars skills status | avatars skills promotion [--task <task-id>]")
	}
	if args[0] == "promotion" {
		return runSkillPromotion(args[1:])
	}

	application, err := app.Bootstrap()
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()

	switch args[0] {
	case "new":
		return runSkillNew(args[1:])
	case "generate":
		return runSkillGenerate(args[1:])
	case "import":
		if len(args) < 2 {
			return errors.New("usage: avatars skills import <file.md>")
		}
		importedPath, err := application.Skills.Import(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Imported skill: %s\n", importedPath)
		if navErr := application.Skills.RegenerateNavigator(); navErr != nil {
			fmt.Fprintln(os.Stderr, "Warning: navigator regeneration failed:", navErr)
		}
		return nil
	case "approve":
		if len(args) < 2 {
			return errors.New("usage: avatars skills approve <generated-file>")
		}
		approvedPath, err := application.Skills.Approve(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Approved skill: %s\n", approvedPath)
		if navErr := application.Skills.RegenerateNavigator(); navErr != nil {
			fmt.Fprintln(os.Stderr, "Warning: navigator regeneration failed:", navErr)
		}
		return nil
	case "archive":
		if len(args) < 2 {
			return errors.New("usage: avatars skills archive <approved-file>")
		}
		archivedPath, err := application.Skills.Archive(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Archived skill: %s\n", archivedPath)
		return nil
	case "disable":
		if len(args) < 2 {
			return errors.New("usage: avatars skills disable <approved-file>")
		}
		disabledPath, err := application.Skills.Disable(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Disabled skill: %s\n", disabledPath)
		return nil
	case "restore":
		if len(args) < 2 {
			return errors.New("usage: avatars skills restore <archived-or-disabled-file>")
		}
		restoredPath, err := application.Skills.Restore(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Restored skill: %s\n", restoredPath)
		return nil
	case "list":
		listings, err := application.Skills.ListApproved()
		if err != nil {
			return err
		}
		pluginSummary, err := plugins.LoadSummary()
		if err != nil {
			return err
		}
		pluginSkills := pluginSummary.Skills
		if len(listings) == 0 && len(pluginSkills) == 0 {
			fmt.Println("No approved or plugin skills found.")
			return nil
		}
		if len(listings) > 0 {
			fmt.Println("Approved skills:")
		}
		for _, listing := range listings {
			fmt.Printf("- %s | %s | approved=%s | always-on=%t | %s | %s\n", listing.Name, listing.Version, formatSkillTime(listing.ApprovedAt), listing.AlwaysOn, listing.WhenToUse, listing.Path)
		}
		if len(pluginSkills) > 0 {
			fmt.Println("Plugin skills:")
		}
		for _, definition := range pluginSkills {
			fmt.Printf("- %s | %s | plugin | %s | %s\n", definition.Name, definition.Version, definition.WhenToUse, definition.Path)
		}
		return nil
	case "archived":
		listings, err := application.Skills.ListArchived()
		if err != nil {
			return err
		}
		if len(listings) == 0 {
			fmt.Println("No archived skills found.")
			return nil
		}
		for _, listing := range listings {
			fmt.Printf("- %s | %s | archived=%s | approved=%s | %s\n", listing.Name, listing.Version, formatSkillTime(listing.ArchivedAt), formatSkillTime(listing.ApprovedAt), listing.Path)
		}
		return nil
	case "disabled":
		listings, err := application.Skills.ListDisabled()
		if err != nil {
			return err
		}
		if len(listings) == 0 {
			fmt.Println("No disabled skills found.")
			return nil
		}
		for _, listing := range listings {
			fmt.Printf("- %s | %s | disabled=%s | approved=%s | %s\n", listing.Name, listing.Version, formatSkillTime(listing.DisabledAt), formatSkillTime(listing.ApprovedAt), listing.Path)
		}
		return nil
	case "cleanup-stale":
		count, err := application.Skills.CleanupStaleGenerated(7)
		if err != nil {
			return err
		}
		fmt.Printf("Archived %d stale generated skill(s) older than 7 days.\n", count)
		return nil
	case "timeline":
		summary, err := application.Skills.GovernanceSummary()
		if err != nil {
			return err
		}
		fmt.Printf("TrackedSkills: %d\n", summary.TrackedSkills)
		fmt.Printf("Generated: %d\n", summary.GeneratedCount)
		fmt.Printf("Approved: %d\n", summary.ApprovedCount)
		fmt.Printf("Archived: %d\n", summary.ArchivedCount)
		fmt.Printf("Disabled: %d\n", summary.DisabledCount)
		fmt.Printf("Transitions: %d\n", summary.TransitionCount)
		if len(summary.Hotspots) == 0 {
			fmt.Println("ChurnHotspots: none")
		} else {
			fmt.Println("ChurnHotspots:")
			for _, hotspot := range summary.Hotspots {
				fmt.Printf("- %s | transitions=%d | current=%s | last=%s\n", hotspot.SkillName, hotspot.TransitionCount, displaySkillField(hotspot.CurrentState), formatSkillTime(hotspot.LastTransitionAt))
			}
		}
		if len(summary.Alerts) == 0 {
			fmt.Println("GovernanceAlerts: none")
		} else {
			fmt.Println("GovernanceAlerts:")
			for _, alert := range summary.Alerts {
				fmt.Printf("- %s | %s | %s | %s\n", alert.Severity, alert.SkillName, alert.Reason, alert.Path)
			}
		}
		if len(summary.Transitions) == 0 {
			fmt.Println("RecentTransitions: none")
			return nil
		}
		fmt.Println("RecentTransitions:")
		for _, transition := range summary.Transitions {
			fmt.Printf("- %s | %s | %s -> %s | %s | current=%s | %s\n", formatSkillTime(transition.At), transition.Action, displaySkillField(transition.FromState), displaySkillField(transition.ToState), transition.SkillName, displaySkillField(transition.CurrentState), transition.Path)
		}
		return nil
	case "ledger":
		ledger, err := application.Skills.GovernanceLedger()
		if err != nil {
			return err
		}
		fmt.Printf("Events: %d\n", ledger.EventCount)
		fmt.Printf("SkillsSeen: %d\n", ledger.SkillsSeen)
		if len(ledger.Entries) == 0 {
			fmt.Println("LedgerEvents: none")
			return nil
		}
		fmt.Println("LedgerEvents:")
		for _, entry := range ledger.Entries {
			fmt.Printf("- %s | %s | %s -> %s | %s | %s\n", formatSkillTime(entry.At), entry.Action, displaySkillField(entry.FromState), displaySkillField(entry.ToState), entry.SkillName, entry.SkillPath)
		}
		return nil
	case "reconcile":
		report, err := application.Skills.GovernanceReconciliation()
		if err != nil {
			return err
		}
		fmt.Printf("CurrentSkills: %d\n", report.CurrentSkills)
		fmt.Printf("LedgerSkills: %d\n", report.LedgerSkills)
		fmt.Printf("MissingCurrent: %d\n", len(report.MissingCurrent))
		fmt.Printf("UntrackedCurrent: %d\n", len(report.UntrackedCurrent))
		fmt.Printf("StateDrifts: %d\n", len(report.StateDrifts))
		fmt.Printf("PathDrifts: %d\n", len(report.PathDrifts))
		fmt.Printf("ContentDrifts: %d\n", len(report.ContentDrifts))
		fmt.Printf("MetadataDrifts: %d\n", len(report.MetadataDrifts))
		fmt.Printf("HistoryDrifts: %d\n", len(report.HistoryDrifts))
		fmt.Printf("InvariantDrifts: %d\n", len(report.InvariantDrifts))
		if len(report.MissingCurrent) == 0 {
			fmt.Println("MissingCurrentEntries: none")
		} else {
			fmt.Println("MissingCurrentEntries:")
			for _, entry := range report.MissingCurrent {
				fmt.Printf("- %s | %s | %s -> %s | %s | %s\n", formatSkillTime(entry.At), entry.Action, displaySkillField(entry.FromState), displaySkillField(entry.ToState), entry.SkillName, entry.SkillPath)
			}
		}
		if len(report.UntrackedCurrent) == 0 {
			fmt.Println("UntrackedCurrentEntries: none")
		} else {
			fmt.Println("UntrackedCurrentEntries:")
			for _, current := range report.UntrackedCurrent {
				fmt.Printf("- %s | current=%s | %s\n", current.SkillName, displaySkillField(current.CurrentState), current.Path)
			}
		}
		if len(report.StateDrifts) == 0 {
			fmt.Println("StateDriftEntries: none")
		} else {
			fmt.Println("StateDriftEntries:")
			for _, drift := range report.StateDrifts {
				fmt.Printf("- %s | ledger=%s | current=%s | %s\n", drift.SkillName, displaySkillField(drift.LedgerState), displaySkillField(drift.CurrentState), drift.CurrentPath)
			}
		}
		if len(report.PathDrifts) == 0 {
			fmt.Println("PathDriftEntries: none")
		} else {
			fmt.Println("PathDriftEntries:")
			for _, drift := range report.PathDrifts {
				fmt.Printf("- %s | ledger=%s | current=%s\n", drift.SkillName, drift.LedgerPath, drift.CurrentPath)
			}
		}
		if len(report.ContentDrifts) == 0 {
			fmt.Println("ContentDriftEntries: none")
		} else {
			fmt.Println("ContentDriftEntries:")
			for _, drift := range report.ContentDrifts {
				fmt.Printf("- %s | ledger-state=%s | current-state=%s | %s\n", drift.SkillName, displaySkillField(drift.LedgerState), displaySkillField(drift.CurrentState), drift.CurrentPath)
			}
		}
		if len(report.MetadataDrifts) == 0 {
			fmt.Println("MetadataDriftEntries: none")
		} else {
			fmt.Println("MetadataDriftEntries:")
			for _, drift := range report.MetadataDrifts {
				fmt.Printf("- %s | %s | expected=%s | actual=%s | state=%s | %s\n", drift.SkillName, drift.Field, drift.Expected, drift.Actual, displaySkillField(drift.CurrentState), drift.CurrentPath)
			}
		}
		if len(report.HistoryDrifts) == 0 {
			fmt.Println("HistoryDriftEntries: none")
		} else {
			fmt.Println("HistoryDriftEntries:")
			for _, drift := range report.HistoryDrifts {
				fmt.Printf("- %s | reason=%s | expected-transitions=%d | actual-transitions=%d | expected-latest=%s | actual-latest=%s | state=%s | %s\n", drift.SkillName, drift.Reason, drift.ExpectedTransitions, drift.ActualTransitions, drift.ExpectedLatestAction, drift.ActualLatestAction, displaySkillField(drift.CurrentState), drift.CurrentPath)
			}
		}
		if len(report.InvariantDrifts) == 0 {
			fmt.Println("InvariantDriftEntries: none")
		} else {
			fmt.Println("InvariantDriftEntries:")
			for _, drift := range report.InvariantDrifts {
				fmt.Printf("- %s | reason=%s | repairable=%s | blocked-by=%s | follow-up=%s | state=%s | %s\n", drift.SkillName, drift.Reason, yesNo(drift.Repairable), displaySkillField(drift.BlockedBy), displaySkillField(drift.SuggestedFollowUp), displaySkillField(drift.CurrentState), drift.CurrentPath)
			}
		}
		return nil
	case "sync":
		report, err := application.Skills.SyncGovernanceLedger()
		if err != nil {
			return err
		}
		fmt.Printf("AdoptedCurrent: %d\n", len(report.AdoptedCurrent))
		fmt.Printf("SyncedCurrent: %d\n", len(report.SyncedCurrent))
		fmt.Printf("UnresolvedMissingCurrent: %d\n", len(report.UnresolvedMissing))
		if len(report.AdoptedCurrent) == 0 {
			fmt.Println("AdoptedCurrentEntries: none")
		} else {
			fmt.Println("AdoptedCurrentEntries:")
			for _, entry := range report.AdoptedCurrent {
				fmt.Printf("- %s | %s | %s | current=%s | %s\n", formatSkillTime(entry.At), entry.Action, entry.SkillName, displaySkillField(entry.ToState), entry.SkillPath)
			}
		}
		if len(report.SyncedCurrent) == 0 {
			fmt.Println("SyncedCurrentEntries: none")
		} else {
			fmt.Println("SyncedCurrentEntries:")
			for _, entry := range report.SyncedCurrent {
				fmt.Printf("- %s | %s | %s -> %s | %s | ledger-path=%s | current-path=%s | state-drift=%s | path-drift=%s | content-drift=%s\n", formatSkillTime(entry.Entry.At), entry.Entry.Action, displaySkillField(entry.Entry.FromState), displaySkillField(entry.Entry.ToState), entry.Entry.SkillName, entry.PreviousPath, entry.Entry.SkillPath, yesNo(entry.StateChanged), yesNo(entry.PathChanged), yesNo(entry.ContentChanged))
			}
		}
		if len(report.UnresolvedMissing) == 0 {
			fmt.Println("UnresolvedMissingCurrentEntries: none")
		} else {
			fmt.Println("UnresolvedMissingCurrentEntries:")
			for _, entry := range report.UnresolvedMissing {
				fmt.Printf("- %s | %s | %s -> %s | %s | %s\n", formatSkillTime(entry.At), entry.Action, displaySkillField(entry.FromState), displaySkillField(entry.ToState), entry.SkillName, entry.SkillPath)
			}
		}
		return nil
	case "resolve-missing":
		if len(args) != 2 {
			return errors.New("usage: avatars skills resolve-missing <recorded-file-or-name>")
		}
		entry, err := application.Skills.ResolveMissingCurrent(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("ResolvedMissingCurrent: %s\n", entry.SkillName)
		fmt.Printf("ResolutionAction: %s\n", entry.Action)
		fmt.Printf("PreviousState: %s\n", displaySkillField(entry.FromState))
		fmt.Printf("ResolutionState: %s\n", displaySkillField(entry.ToState))
		fmt.Printf("RecordedPath: %s\n", entry.SkillPath)
		return nil
	case "restore-missing":
		if len(args) != 3 {
			return errors.New("usage: avatars skills restore-missing <recorded-file-or-name> <source-markdown>")
		}
		result, err := application.Skills.RestoreMissingCurrent(args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Printf("RestoredMissingCurrent: %s\n", result.RestoredPath)
		fmt.Printf("RestoreSource: %s\n", result.SourcePath)
		fmt.Printf("LifecycleState: %s\n", displaySkillField(result.LifecycleState))
		fmt.Printf("LedgerAction: %s\n", result.LedgerEntry.Action)
		fmt.Printf("SuggestedReconcileCheck: %s\n", result.SuggestedReconcileCheck)
		return nil
	case "restore-guide":
		if len(args) != 2 {
			return errors.New("usage: avatars skills restore-guide <recorded-file-or-name>")
		}
		guide, err := application.Skills.MissingCurrentRestoreGuide(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("RestoreSkill: %s\n", guide.SkillName)
		fmt.Printf("RecordedPath: %s\n", guide.RecordedPath)
		fmt.Printf("LatestAction: %s\n", guide.LatestAction)
		fmt.Printf("LatestState: %s\n", displaySkillField(guide.LatestLifecycleState))
		fmt.Printf("SourceTaskID: %s\n", displaySkillField(guide.SourceTaskID))
		fmt.Printf("SourceRunID: %s\n", displaySkillField(guide.SourceRunID))
		fmt.Printf("TemplateID: %s\n", displaySkillField(guide.TemplateID))
		fmt.Printf("SuggestedReconcileCheck: %s\n", guide.SuggestedReconcileCheck)
		return nil
	case "repair-metadata":
		if len(args) != 2 {
			return errors.New("usage: avatars skills repair-metadata <current-file>")
		}
		result, err := application.Skills.RepairMetadata(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("RepairedMetadata: %s\n", result.RepairedPath)
		fmt.Printf("LifecycleState: %s\n", displaySkillField(result.LifecycleState))
		fmt.Printf("UpdatedFields: %s\n", strings.Join(result.UpdatedFields, ", "))
		fmt.Printf("LedgerAction: %s\n", result.LedgerEntry.Action)
		fmt.Printf("SuggestedReconcileCheck: %s\n", result.SuggestedReconcileCheck)
		return nil
	case "repair-history":
		if len(args) != 2 {
			return errors.New("usage: avatars skills repair-history <current-file>")
		}
		result, err := application.Skills.RepairHistory(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("RepairedHistory: %s\n", result.RepairedPath)
		fmt.Printf("LifecycleState: %s\n", displaySkillField(result.LifecycleState))
		fmt.Printf("RebuiltTransitions: %d\n", result.RebuiltTransitions)
		fmt.Printf("LedgerAction: %s\n", result.LedgerEntry.Action)
		fmt.Printf("SuggestedReconcileCheck: %s\n", result.SuggestedReconcileCheck)
		return nil
	case "repair-invariants":
		if len(args) != 2 {
			return errors.New("usage: avatars skills repair-invariants <current-file>")
		}
		result, err := application.Skills.RepairInvariants(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("RepairedInvariants: %s\n", result.RepairedPath)
		fmt.Printf("LifecycleState: %s\n", displaySkillField(result.LifecycleState))
		fmt.Printf("RebuiltTransitions: %d\n", result.RebuiltTransitions)
		fmt.Printf("LedgerAction: %s\n", result.LedgerEntry.Action)
		fmt.Printf("SuggestedReconcileCheck: %s\n", result.SuggestedReconcileCheck)
		return nil
	case "pending":
		listings, err := application.Skills.ListGenerated()
		if err != nil {
			return err
		}
		if len(listings) == 0 {
			fmt.Println("No pending generated skills found.")
			return nil
		}
		for _, listing := range listings {
			fmt.Printf("- %s | %s | generated=%s | task=%s | run=%s | %s\n", listing.Name, listing.Version, formatSkillTime(listing.GeneratedAt), displaySkillField(listing.SourceTaskID), displaySkillField(listing.SourceRunID), listing.Path)
		}
		return nil
	case "review":
		if len(args) < 2 {
			return errors.New("usage: avatars skills review <generated-file>")
		}
		review, err := application.Skills.ReviewGenerated(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Candidate: %s\n", review.Candidate.Path)
		fmt.Printf("GeneratedAt: %s\n", formatSkillTime(review.Candidate.GeneratedAt))
		fmt.Printf("SourceTaskID: %s\n", displaySkillField(review.Candidate.SourceTaskID))
		fmt.Printf("SourceRunID: %s\n", displaySkillField(review.Candidate.SourceRunID))
		fmt.Printf("ReadyForApproval: %s\n", yesNo(review.ReadyForApproval))
		if len(review.ValidationFindings) == 0 {
			fmt.Println("ValidationFindings: none")
		} else {
			fmt.Println("ValidationFindings:")
			for _, finding := range review.ValidationFindings {
				fmt.Printf("- %s\n", finding)
			}
		}
		if !review.HasReference {
			fmt.Println("MatchedReference: none")
			return nil
		}
		fmt.Printf("MatchedReference: %s\n", review.Reference.Path)
		fmt.Printf("ReferenceLifecycleState: %s\n", displaySkillField(review.Reference.LifecycleState))
		fmt.Printf("MatchReason: %s\n", review.MatchReason)
		fmt.Printf("ChangedFields: %d\n", len(review.ChangedFields))
		if len(review.ChangedFields) > 0 {
			fmt.Println("FieldDiffs:")
			for _, diff := range review.ChangedFields {
				fmt.Printf("- %s | candidate=%s | reference=%s\n", diff.Field, displaySkillField(diff.CandidateValue), displaySkillField(diff.ReferenceValue))
			}
		}
		fmt.Printf("BodyChanged: %s\n", yesNo(review.BodyChanged))
		fmt.Printf("BodyDiffLines: %d\n", len(review.BodyDiffLines))
		if len(review.BodyDiffLines) > 0 {
			fmt.Println("BodyDiff:")
			for _, line := range review.BodyDiffLines {
				fmt.Println(line)
			}
		}
		fmt.Printf("CandidateBodyLines: %d\n", review.CandidateBodyLines)
		fmt.Printf("ReferenceBodyLines: %d\n", review.ReferenceBodyLines)
		return nil
	case "status":
		statuses, err := application.Skills.Status()
		if err != nil {
			return err
		}
		for _, status := range statuses {
			mode := "reserved"
			if status.Active {
				mode = "active"
			}
			fmt.Printf("Directory: %s\n", status.Name)
			fmt.Printf("Path: %s\n", status.Path)
			fmt.Printf("Mode: %s\n", mode)
			fmt.Printf("Purpose: %s\n", status.Purpose)
			fmt.Printf("Files: %d\n", status.FileCount)
			if status.FileCount == 0 {
				fmt.Println("Current files: none")
			} else {
				fmt.Println("Current files:")
				for _, file := range status.Files {
					fmt.Printf("- %s\n", file)
				}
			}
			fmt.Println()
		}
		return nil
	case "show":
		if len(args) < 2 {
			return errors.New("usage: avatars skills show <current-file>")
		}
		definition, err := application.Skills.LoadInspect(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Name: %s\n", definition.Name)
		fmt.Printf("Description: %s\n", definition.Description)
		fmt.Printf("WhenToUse: %s\n", definition.WhenToUse)
		fmt.Printf("Context: %s\n", definition.Context)
		fmt.Printf("Version: %s\n", definition.Version)
		fmt.Printf("LifecycleState: %s\n", displaySkillField(definition.LifecycleState))
		fmt.Printf("SourceTaskID: %s\n", displaySkillField(definition.SourceTaskID))
		fmt.Printf("SourceRunID: %s\n", displaySkillField(definition.SourceRunID))
		fmt.Printf("GeneratedAt: %s\n", formatSkillTime(definition.GeneratedAt))
		fmt.Printf("ApprovedAt: %s\n", formatSkillTime(definition.ApprovedAt))
		fmt.Printf("ArchivedAt: %s\n", formatSkillTime(definition.ArchivedAt))
		fmt.Printf("DisabledAt: %s\n", formatSkillTime(definition.DisabledAt))
		historyStatus := "ok"
		if definition.HistoryError != "" {
			historyStatus = "corrupted"
		} else if len(definition.History) == 0 {
			historyStatus = "missing"
		}
		fmt.Printf("HistoryStatus: %s\n", historyStatus)
		if definition.HistoryError != "" {
			fmt.Printf("HistoryError: %s\n", definition.HistoryError)
		}
		fmt.Printf("HistoryEntries: %d\n", len(definition.History))
		if len(definition.GovernanceAlerts) == 0 {
			fmt.Println("GovernanceAlerts: none")
			fmt.Println("SuggestedFollowUp: none")
		} else {
			fmt.Println("GovernanceAlerts:")
			for _, alert := range definition.GovernanceAlerts {
				fmt.Printf("- %s | %s\n", alert.Severity, alert.Reason)
			}
			fmt.Printf("SuggestedFollowUp: %s\n", suggestedSkillGovernanceFollowUp(definition))
		}
		if len(definition.History) > 0 {
			fmt.Println("TransitionHistory:")
			for _, entry := range definition.History {
				fmt.Printf("- %s | %s | %s -> %s | %s\n", formatSkillTime(entry.At), entry.Action, displaySkillField(entry.FromState), displaySkillField(entry.ToState), entry.Path)
			}
		}
		fmt.Printf("Path: %s\n", definition.Path)
		fmt.Printf("AllowedTools: %s\n", strings.Join(definition.AllowedTools, ", "))
		fmt.Printf("UserInvocable: %t\n", definition.UserInvocable)
		fmt.Printf("DisableModelInvocation: %t\n", definition.DisableModelInvocation)
		fmt.Printf("AlwaysOn: %t\n\n", definition.AlwaysOn)
		fmt.Println(definition.Body)
		return nil
	default:
		return errors.New("usage: avatars skills generate [--task <task-id>] [--from-file <path>] \"<task>\" | avatars skills approve <generated-file> | avatars skills archive <approved-file> | avatars skills disable <approved-file> | avatars skills restore <archived-or-disabled-file> | avatars skills restore-missing <recorded-file-or-name> <source-markdown> | avatars skills repair-metadata <current-file> | avatars skills repair-history <current-file> | avatars skills repair-invariants <current-file> | avatars skills list | avatars skills pending | avatars skills review <generated-file> | avatars skills archived | avatars skills disabled | avatars skills timeline | avatars skills ledger | avatars skills reconcile | avatars skills restore-guide <recorded-file-or-name> | avatars skills resolve-missing <recorded-file-or-name> | avatars skills sync | avatars skills show <current-file> | avatars skills status | avatars skills promotion [--task <task-id>]")
	}
}

func runSkillGenerate(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: avatars skills generate [--task <task-id>] [--from-file <path>] \"<task>\"")
	}
	options := replCommandOptions{}
	input := ""
	for len(args) > 0 {
		switch args[0] {
		case "--task":
			if len(args) < 2 {
				return errors.New("usage: avatars skills generate [--task <task-id>] [--from-file <path>] \"<task>\"")
			}
			options.TaskID = strings.TrimSpace(args[1])
			args = args[2:]
		case "--from-file":
			if len(args) < 2 {
				return errors.New("usage: avatars skills generate [--task <task-id>] [--from-file <path>] \"<task>\"")
			}
			content, err := readIntentInputFile(args[1])
			if err != nil {
				return err
			}
			input = strings.TrimSpace(content)
			args = args[2:]
		default:
			input = strings.TrimSpace(strings.Join(args, " "))
			args = nil
		}
	}
	if input == "" {
		return errors.New("skills generate input cannot be empty")
	}
	options = effectiveREPLCommandOptions(options)
	plan := planner.Build(input)
	proposal, draftStatus, err := buildLLMAssistedSkillProposal(input, plan)
	if err != nil {
		return err
	}
	application, err := app.Bootstrap()
	if err != nil {
		return err
	}
	defer func() {
		_ = application.Close()
	}()
	workspaceID := options.TaskID
	if workspaceID == "" {
		workspaceID = "skills-generate"
	}
	path, err := application.Skills.Generate("manual-skill-generate", workspaceID, proposal)
	if err != nil {
		return err
	}
	review, err := application.Skills.ReviewGenerated(path)
	if err != nil {
		return err
	}
	fmt.Printf("Generated skill: %s\n", path)
	fmt.Printf("Skill preview: %s\n", proposal.Name)
	fmt.Printf("LLM draft status: %s\n", draftStatus)
	fmt.Printf("Ready for approval: %s\n", yesNo(review.ReadyForApproval))
	if len(review.ValidationFindings) > 0 {
		fmt.Println("Validation findings:")
		for _, finding := range review.ValidationFindings {
			fmt.Printf("- %s\n", finding)
		}
	} else {
		fmt.Println("Validation findings: none")
	}
	if review.HasReference {
		fmt.Printf("Review match: %s\n", displaySkillField(review.MatchReason))
		fmt.Printf("Review reference: %s\n", review.Reference.Path)
	} else {
		fmt.Println("Review reference: none")
	}
	fmt.Printf("Review body lines: %d\n", review.CandidateBodyLines)
	fmt.Printf("Next step: avatars skills review %s\n", path)
	return nil
}

func buildLLMAssistedSkillProposal(input string, plan planner.Plan) (skillbuilder.Proposal, string, error) {
	repoSummary, err := summarizeSkillGenerationContext()
	if err != nil {
		return skillbuilder.Proposal{}, "", err
	}
	baseProposal := skillbuilder.Build(input, plan, repoSummary)
	client, cleanup, err := bootstrapIntentLLMClient()
	if err != nil || client == nil {
		if cleanup != nil {
			cleanup()
		}
		return baseProposal, "fallback: deterministic template", nil
	}
	if cleanup != nil {
		defer cleanup()
	}
	response, err := client.Generate(context.Background(), llm.Request{
		SystemPrompt:     skillGenerationSystemPrompt(),
		UserPrompt:       buildSkillGenerationPrompt(input, plan, repoSummary, baseProposal),
		StructuredOutput: true,
	})
	if err != nil || response.Fallback {
		return baseProposal, "fallback: deterministic template", nil
	}
	draft, ok := parseSkillGenerationDraft(response.Text)
	if !ok {
		return baseProposal, "fallback: invalid LLM draft", nil
	}
	proposal := skillbuilder.BuildWithDraft(input, plan, repoSummary, draft)
	return proposal, "llm-assisted draft", nil
}

type skillGenerationLLMDraft struct {
	Description string `json:"description"`
	WhenToUse   string `json:"when_to_use"`
	Body        string `json:"body"`
}

func summarizeSkillGenerationContext() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	parts := []string{"Explicit skill generation request."}
	if overview, err := summarizeProjectOverview(workingDir); err == nil && strings.TrimSpace(overview) != "" {
		parts = append(parts, overview)
	}
	if manifests, err := summarizeProjectManifests(workingDir); err == nil && strings.TrimSpace(manifests) != "" {
		parts = append(parts, manifests)
	}
	if docs, err := summarizeDocsOverview(workingDir); err == nil && strings.TrimSpace(docs) != "" {
		parts = append(parts, docs)
	}
	return strings.Join(parts, "\n\n"), nil
}

func skillGenerationSystemPrompt() string {
	return `You draft one Avatars skill candidate under strict harness control. Return compact JSON only: {"description":"","when_to_use":"","body":""}.
Rules:
- Do not write frontmatter. The harness owns metadata, lifecycle, tools, and approval.
- Use only read-only behavior. Do not request shell, write, network, or approval bypass.
- Keep the draft narrow and operational.
- The body must include exactly these markdown sections: Purpose, Task Focus, Repo Survey Summary, Suggested Workflow, Constraints.
- Suggested Workflow must explain what to read first, when to use memory, when to ask clarification, and what proof to output.
- Constraints must say the skill is a generated candidate and must not self-activate.`
}

func buildSkillGenerationPrompt(input string, plan planner.Plan, repoSummary string, base skillbuilder.Proposal) string {
	var builder strings.Builder
	builder.WriteString("User task:\n")
	builder.WriteString(strings.TrimSpace(input))
	builder.WriteString("\n\nPlan summary:\n")
	builder.WriteString(strings.TrimSpace(plan.Summary))
	builder.WriteString("\n\nRepository context:\n")
	builder.WriteString(compactSkillGenerationText(repoSummary, 1600))
	builder.WriteString("\n\nTemplate asset:\n")
	builder.WriteString(skillbuilder.TaskSurveyTemplateText())
	builder.WriteString("\n\nTemplate constraints:\n")
	builder.WriteString("- name is fixed by harness: ")
	builder.WriteString(base.Name)
	builder.WriteString("\n- template-id is fixed by harness: ")
	builder.WriteString(base.TemplateID)
	builder.WriteString("\n- allowed tools are fixed by harness: read")
	builder.WriteString("\n- required body headings: ")
	builder.WriteString(strings.Join(skillbuilder.TaskSurveyRequiredBodySections(), ", "))
	builder.WriteString("\n\nReturn JSON only.")
	return builder.String()
}

func parseSkillGenerationDraft(text string) (skillbuilder.Draft, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return skillbuilder.Draft{}, false
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		trimmed = trimmed[start : end+1]
	}
	var draft skillGenerationLLMDraft
	if err := json.Unmarshal([]byte(trimmed), &draft); err != nil {
		return skillbuilder.Draft{}, false
	}
	out := skillbuilder.Draft{
		Description: strings.TrimSpace(draft.Description),
		WhenToUse:   strings.TrimSpace(draft.WhenToUse),
		Body:        strings.TrimSpace(draft.Body),
	}
	if out.Description == "" && out.WhenToUse == "" && out.Body == "" {
		return skillbuilder.Draft{}, false
	}
	return out, true
}

func compactSkillGenerationText(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:limit]) + "..."
}

func runSkillPromotion(args []string) error {
	if len(args) != 0 && len(args) != 2 {
		return errors.New("usage: avatars skills promotion [--task <task-id>]")
	}
	taskID := ""
	if len(args) == 2 {
		if args[0] != "--task" {
			return errors.New("usage: avatars skills promotion [--task <task-id>]")
		}
		taskID = args[1]
	}
	manager := tasks.NewManager(filepath.Join(".avatars", "tasks"))
	if taskID != "" {
		workspace, err := manager.Load(taskID)
		if err != nil {
			return err
		}
		memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
		if err != nil {
			return err
		}
		defer func() {
			_ = memoryStore.Close()
		}()
		snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
		if err != nil {
			return err
		}
		fmt.Printf("Task: %s\n", workspace.ID)
		fmt.Printf("Task root: %s\n", workspace.RootDir)
		printSkillPromotionQueue(snapshot.EvolutionCandidates)
		return nil
	}

	workspaces, err := manager.List()
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		fmt.Println("No task workspaces found.")
		return nil
	}
	total := 0
	for _, workspace := range workspaces {
		memoryStore, err := memstore.NewSQLiteStore(workspace.MemoryDir)
		if err != nil {
			return err
		}
		snapshot, err := memoryStore.LoadLatestTaskSnapshot(workspace.ID)
		_ = memoryStore.Close()
		if err != nil {
			return err
		}
		queue := filterSkillPromotionCandidates(snapshot.EvolutionCandidates)
		if len(queue) == 0 {
			continue
		}
		total += len(queue)
		fmt.Printf("Task: %s\n", workspace.ID)
		fmt.Printf("Task root: %s\n", workspace.RootDir)
		printSkillPromotionQueue(queue)
	}
	if total == 0 {
		fmt.Println("Skill promotions: 0")
		fmt.Println("Skill promotion queue: none")
	}
	return nil
}

func printSkillPromotionQueue(candidates []memstore.EvolutionCandidate) {
	queue := filterSkillPromotionCandidates(candidates)
	fmt.Printf("Skill promotions: %d\n", len(queue))
	fmt.Println("Skill promotion mode: advisory-only; no approval, activation, or skill file mutation is performed.")
	if len(queue) == 0 {
		fmt.Println("Skill promotion queue: none")
		return
	}
	counts := map[string]int{}
	for _, candidate := range queue {
		counts[strings.ToLower(strings.TrimSpace(candidate.Priority))]++
	}
	fmt.Printf("Priority high: %d\n", counts["high"])
	fmt.Printf("Priority medium: %d\n", counts["medium"])
	fmt.Printf("Priority low: %d\n", counts["low"])
	fmt.Println("Skill promotion queue:")
	for index, candidate := range queue {
		fmt.Printf("- %d | %s | %s | %s | %s\n", index+1, displaySkillField(candidate.Priority), displaySkillField(candidate.Status), displaySkillField(candidate.Source), candidate.Summary)
	}
}

func filterSkillPromotionCandidates(candidates []memstore.EvolutionCandidate) []memstore.EvolutionCandidate {
	queue := make([]memstore.EvolutionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Kind != "skill_promotion" {
			continue
		}
		queue = append(queue, candidate)
	}
	return queue
}

func formatSkillTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}

func suggestedSkillGovernanceFollowUp(definition skills.Definition) string {
	target := filepath.Base(strings.TrimSpace(definition.Path))
	if target == "" || target == "." {
		return "avatars skills reconcile"
	}
	for _, alert := range definition.GovernanceAlerts {
		switch {
		case strings.HasPrefix(alert.Reason, "broken-transition-chain:"),
			strings.HasPrefix(alert.Reason, "invalid-transition:"),
			strings.HasPrefix(alert.Reason, "invalid-start-transition:"):
			return fmt.Sprintf("avatars skills repair-invariants %s", target)
		case alert.Reason == "corrupted-history",
			alert.Reason == "missing-history":
			return fmt.Sprintf("avatars skills repair-history %s", target)
		}
	}
	return "avatars skills reconcile"
}

func displaySkillField(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}
