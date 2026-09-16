package runtime

import (
	"context"
	"fmt"
	"strings"

	"avatars/internal/planner"
)

// continueWorkflowFromCheckpoint resumes remaining DAG nodes after an approval
// replay. S4.1: previously emit-only fake-complete; now marks the blocked node
// complete (write already replayed) and runs the scheduler for remaining work.
func (e *Engine) continueWorkflowFromCheckpoint(originRunID string, originTaskID string, approvalKey string, continuationRunID string, continuationTaskID string, defaultAvatarID string, toolName string, operation string, lifecycle RunLifecycle, outcome string, checkpoint *PendingApprovalWorkflow) error {
	if checkpoint == nil || strings.TrimSpace(outcome) != "completed" {
		return nil
	}
	remainingNodes := workflowContinuationNodes(checkpoint)
	if strings.TrimSpace(checkpoint.BlockedNodeID) == "" && len(remainingNodes) == 0 {
		return nil
	}

	// S4.2: restore retry counts from checkpoint when present.
	if len(checkpoint.NodeRetryCounts) > 0 {
		if e.nodeRetryCount == nil {
			e.nodeRetryCount = make(map[string]int)
		}
		for k, v := range checkpoint.NodeRetryCounts {
			e.nodeRetryCount[k] = v
		}
	}

	plan := planFromApprovalCheckpoint(checkpoint)
	workflowState := workflowStateFromApprovalCheckpoint(checkpoint)
	if workflowState == nil {
		workflowState = NewWorkflowState(plan)
	}

	blockedID := strings.TrimSpace(checkpoint.BlockedNodeID)
	if blockedID != "" {
		// Approval replay already applied the guarded write — mark Builder done.
		if node := workflowState.Node(blockedID); node.ID != "" {
			switch node.Status {
			case WorkflowNodeStatusPending, WorkflowNodeStatusActive, "":
				_ = workflowState.Activate(blockedID)
				_ = workflowState.Complete(blockedID, nil)
			}
		}
		if err := e.emit(originRunID, originTaskID, "", "planning", "workflow.node_completed", "runtime", map[string]any{
			"node_id":       blockedID,
			"title":         strings.TrimSpace(checkpoint.BlockedNodeTitle),
			"assigned_role": strings.TrimSpace(checkpoint.BlockedNodeRole),
			"approval_key":  strings.TrimSpace(approvalKey),
			"resume_mode":   "approval_checkpoint_scheduler",
		}, nil); err != nil {
			return err
		}
		if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
			RunID:              strings.TrimSpace(originRunID),
			TaskID:             strings.TrimSpace(originTaskID),
			AvatarID:           strings.TrimSpace(defaultAvatarID),
			NodeID:             blockedID,
			NodeRole:           strings.TrimSpace(checkpoint.BlockedNodeRole),
			NodeTitle:          strings.TrimSpace(checkpoint.BlockedNodeTitle),
			Tool:               strings.TrimSpace(toolName),
			Operation:          strings.TrimSpace(operation),
			Status:             "replay_completed",
			Summary:            fmt.Sprintf("Workflow node %s completed through approval replay; remaining nodes resume via scheduler.", blockedID),
			ApprovalKey:        strings.TrimSpace(approvalKey),
			ContinuationRunID:  strings.TrimSpace(continuationRunID),
			ContinuationTaskID: strings.TrimSpace(continuationTaskID),
			ReplayStatus:       "completed",
			PausePointID:       strings.TrimSpace(checkpoint.PausePointID),
			PausePointKind:     strings.TrimSpace(checkpoint.PausePointKind),
			RecoveryKind:       "approval_replay",
		}); err != nil {
			return err
		}
	}

	e.lastWorkflowSnapshot = ptrWorkflowSnapshot(workflowState.Snapshot())

	taskInput := strings.TrimSpace(checkpoint.TaskInput)
	if taskInput == "" {
		taskInput = strings.TrimSpace(checkpoint.PlanSummary)
	}
	readSummary := strings.TrimSpace(checkpoint.ReadSummary)
	if readSummary == "" {
		readSummary = "Resumed after approval replay; prior survey context may be incomplete."
	}

	avatarByID := map[string]planner.Avatar{}
	for _, a := range plan.Avatars {
		avatarByID[a.ID] = a
	}
	nodeByID := map[string]planner.WorkflowNode{}
	for _, n := range plan.Nodes {
		nodeByID[n.ID] = n
	}

	workCtx := workflowNodeWorkContext{
		runID:              strings.TrimSpace(originRunID),
		taskID:             strings.TrimSpace(originTaskID),
		plan:               plan,
		workflowState:      workflowState,
		input:              taskInput,
		readSummary:        readSummary,
		researcherAvatarID: plan.AvatarIDByRole("Researcher"),
		builderAvatarID:    plan.AvatarIDByRole("Builder"),
		criticAvatarID:     plan.AvatarIDByRole("Critic"),
	}
	if workCtx.researcherAvatarID == "" {
		workCtx.researcherAvatarID = "avatar-researcher"
	}
	if workCtx.builderAvatarID == "" {
		workCtx.builderAvatarID = "avatar-builder"
	}
	if workCtx.criticAvatarID == "" {
		workCtx.criticAvatarID = "avatar-critic"
	}

	ctx := context.Background()
	var pauseErr error
	var paused bool
	err := e.runWorkflowPlan(originRunID, originTaskID, plan, workflowState, avatarByID, nodeByID, func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		result, execErr := e.executeWorkflowNodeWork(ctx, node, workCtx)
		if execErr != nil {
			return SchedulerNodeResult{}, execErr
		}
		if strings.TrimSpace(result.pausePoint.ID) != "" {
			paused = true
			pauseErr = result.pauseErr
			if pauseErr == nil {
				reason := strings.TrimSpace(result.pausePoint.StatusReason)
				if reason == "" {
					reason = "workflow paused during approval continuation"
				}
				pauseErr = fmt.Errorf("%s", reason)
			}
			return SchedulerNodeResult{PausePoint: result.pausePoint, ArtifactIDs: result.artifactIDs}, nil
		}
		if strings.TrimSpace(result.readSummary) != "" {
			workCtx.readSummary = result.readSummary
		}
		return SchedulerNodeResult{ArtifactIDs: result.artifactIDs}, nil
	})
	if err != nil {
		return err
	}
	if paused || pauseErr != nil {
		// Do NOT mark the origin run completed — another approval/user gate is open.
		return pauseErr
	}

	synthID := plan.AvatarIDByRole("Synthesizer")
	if synthID == "" {
		synthID = "avatar-synthesizer"
	}
	if err := e.emit(originRunID, originTaskID, synthID, "reviewing", "synthesis.completed", "runtime", map[string]any{
		"lifecycle":   "tail_synthesis",
		"node_role":   "Synthesizer",
		"resume_mode": "approval_checkpoint_scheduler",
		"status":      "completed",
	}, nil); err != nil {
		return err
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:     strings.TrimSpace(originRunID),
		TaskID:    strings.TrimSpace(originTaskID),
		AvatarID:  synthID,
		NodeID:    "lifecycle-synthesize",
		NodeRole:  "Synthesizer",
		NodeTitle: "Summarize outcome for the user",
		Tool:      "synthesize",
		Operation: "summarize",
		Status:    "completed",
		Summary:   "Synthesizer tail lifecycle after approval continuation.",
	}); err != nil {
		return err
	}

	// Persist durable resume state after successful continuation.
	root := strings.TrimSpace(e.projectRoot)
	if root == "" {
		root = "."
	}
	_ = persistBuilderRetryStates(root, e.builderRetryStates)
	_ = persistNodeRetryCounts(root, e.nodeRetryCount)

	summary := "Workflow continuation completed after approval replay. Remaining nodes executed via scheduler."
	if err := e.emit(originRunID, originTaskID, "", "reviewing", "run.completed", "runtime", map[string]any{
		"status":               "completed",
		"summary":              summary,
		"continuation_run_id":  continuationRunID,
		"continuation_task_id": continuationTaskID,
		"resume_mode":          "approval_checkpoint_scheduler",
	}, nil); err != nil {
		return err
	}
	lifecycle.MarkCompleted(summary)
	if err := e.emit(originRunID, originTaskID, defaultAvatarID, "reviewing", "run.lifecycle_updated", "runtime", lifecycle.Payload(), nil); err != nil {
		return err
	}
	if err := e.persistRunLifecycleEvaluation(originRunID, originTaskID, defaultAvatarID, lifecycle); err != nil {
		return err
	}
	return e.emitStableBoundary(originRunID, originTaskID, "", "completed", summary, "run_terminal", map[string]any{
		"approval_key":           strings.TrimSpace(approvalKey),
		"continuation_run_id":    strings.TrimSpace(continuationRunID),
		"continuation_task_id":   strings.TrimSpace(continuationTaskID),
		"workflow_resume_source": "approval_checkpoint_scheduler",
	})
}

func ptrWorkflowSnapshot(snap WorkflowStateSnapshot) *WorkflowStateSnapshot {
	return &snap
}

func planFromApprovalCheckpoint(checkpoint *PendingApprovalWorkflow) planner.Plan {
	plan := planner.Plan{
		Title:   "Resumed workflow",
		Summary: strings.TrimSpace(checkpoint.PlanSummary),
	}
	seenAvatars := map[string]bool{}
	addNode := func(n PendingApprovalWorkflowNode) {
		role := strings.TrimSpace(n.Role)
		if role == "" {
			role = workflowRoleFromNodeID(n.ID)
		}
		title := strings.TrimSpace(n.Title)
		if title == "" {
			title = strings.TrimSpace(n.ID)
		}
		plan.Nodes = append(plan.Nodes, planner.WorkflowNode{
			ID:           strings.TrimSpace(n.ID),
			Title:        title,
			AssignedRole: role,
			DependsOn:    append([]string(nil), n.DependsOn...),
		})
		avatarID := workflowContinuationAvatarID(role)
		if avatarID != "" && !seenAvatars[avatarID] {
			seenAvatars[avatarID] = true
			plan.Avatars = append(plan.Avatars, planner.Avatar{
				ID:             avatarID,
				Role:           role,
				Responsibility: role + " (resumed)",
			})
		}
	}
	for _, n := range checkpoint.CompletedNodes {
		addNode(n)
	}
	if id := strings.TrimSpace(checkpoint.BlockedNodeID); id != "" {
		addNode(PendingApprovalWorkflowNode{
			ID:        id,
			Role:      checkpoint.BlockedNodeRole,
			Title:     checkpoint.BlockedNodeTitle,
			DependsOn: checkpoint.BlockedNodeDependsOn,
		})
	}
	for _, n := range remainingOrIDs(checkpoint) {
		addNode(n)
	}
	return plan
}

func remainingOrIDs(checkpoint *PendingApprovalWorkflow) []PendingApprovalWorkflowNode {
	if len(checkpoint.RemainingNodes) > 0 {
		return checkpoint.RemainingNodes
	}
	out := make([]PendingApprovalWorkflowNode, 0, len(checkpoint.RemainingNodeIDs))
	for _, id := range checkpoint.RemainingNodeIDs {
		out = append(out, normalizeWorkflowContinuationNode(PendingApprovalWorkflowNode{ID: id}, id))
	}
	return out
}

func workflowStateFromApprovalCheckpoint(checkpoint *PendingApprovalWorkflow) *WorkflowState {
	if checkpoint == nil {
		return nil
	}
	if checkpoint.WorkflowSnapshot != nil && len(checkpoint.WorkflowSnapshot.Nodes) > 0 {
		return RestoreWorkflowState(*checkpoint.WorkflowSnapshot)
	}
	plan := planFromApprovalCheckpoint(checkpoint)
	state := NewWorkflowState(plan)
	completed := map[string]bool{}
	for _, id := range checkpoint.CompletedNodeIDs {
		completed[strings.TrimSpace(id)] = true
	}
	for _, n := range checkpoint.CompletedNodes {
		completed[strings.TrimSpace(n.ID)] = true
	}
	for id := range completed {
		if id == "" {
			continue
		}
		_ = state.Activate(id)
		_ = state.Complete(id, nil)
	}
	return state
}
