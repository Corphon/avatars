package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	memstore "avatars/internal/memory"
	"avatars/internal/planner"
)

// executePlannerNodeWork records that planning already happened at plan-build time.
// Default DAG no longer lists Planner (C3); this remains for CriticHub replan slots.
func (e *Engine) executePlannerNodeWork(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	_ = ctx
	plannerID := work.plan.AvatarIDByRole("Planner")
	if plannerID == "" {
		plannerID = "avatar-planner"
	}
	summary := fmt.Sprintf("Planner node %s: plan already constructed upstream (%d nodes). No incremental replan in this node.", work.nodeID, len(work.plan.Nodes))
	_ = e.emit(work.runID, work.taskID, plannerID, "planning", "planner.node_acknowledged", "runtime", map[string]any{
		"node_id":     work.nodeID,
		"node_count":  len(work.plan.Nodes),
		"plan_title":  work.plan.Title,
		"honest_mode": "upstream_plan_only",
	}, nil)
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:     work.runID,
		TaskID:    work.taskID,
		AvatarID:  plannerID,
		NodeID:    work.nodeID,
		NodeRole:  "Planner",
		NodeTitle: work.nodeTitle,
		Tool:      "plan",
		Operation: "acknowledge",
		Status:    "acknowledged",
		Summary:   summary,
	}); err != nil {
		return workflowNodeWorkResult{}, err
	}
	return workflowNodeWorkResult{}, nil
}

func (e *Engine) executeSynthesizerNodeWork(ctx context.Context, work workflowNodeWorkContext) (workflowNodeWorkResult, error) {
	_ = ctx
	// C3: default DAG omits Synthesizer. If a leftover/custom plan still lists
	// the slot, only prepare evidence — real LLM synthesis stays at runOnce tail.
	synthesizerAvatarID := work.plan.AvatarIDByRole("Synthesizer")
	if synthesizerAvatarID == "" {
		synthesizerAvatarID = "avatar-synthesizer"
	}
	summary := fmt.Sprintf("Synthesizer node %s prepared evidence for tail synthesis (no LLM in DAG node).", work.nodeID)
	payload := map[string]any{
		"node_id":          work.nodeID,
		"node_role":        work.nodeRole,
		"node_title":       work.nodeTitle,
		"honest_mode":      "tail_synthesis_only",
		"read_summary_set": strings.TrimSpace(work.readSummary) != "",
		"builder_output":   strings.TrimSpace(work.generatedSkillPath) != "",
	}
	if err := e.emit(work.runID, work.taskID, synthesizerAvatarID, "reviewing", "synthesis.prepared", "runtime", payload, nil); err != nil {
		return workflowNodeWorkResult{}, err
	}
	if err := e.persistNodeWorkEvidence(nodeWorkEvidence{
		RunID:       work.runID,
		TaskID:      work.taskID,
		AvatarID:    synthesizerAvatarID,
		NodeID:      work.nodeID,
		NodeRole:    work.nodeRole,
		NodeTitle:   work.nodeTitle,
		Tool:        "synthesize",
		Operation:   "summary_prepare",
		Status:      "prepared",
		Summary:     summary,
		ArtifactIDs: workflowNodeArtifactIDs(WorkflowNodeRuntime{ID: work.nodeID}),
	}); err != nil {
		return workflowNodeWorkResult{}, err
	}
	return workflowNodeWorkResult{}, nil
}

func (e *Engine) runWorkflowPlan(runID string, taskID string, plan planner.Plan, workflowState *WorkflowState, avatarByID map[string]planner.Avatar, nodeByID map[string]planner.WorkflowNode, nodeWork func(node WorkflowNodeRuntime) (SchedulerNodeResult, error)) error {
	// v3-P0-3: Actually call HasCycle() to detect DAG cycles before execution.
	// PHANTOM-2 was a phantom fix — the comment existed but no call was made.
	if hasCycle, cycleNode := workflowState.HasCycle(); hasCycle {
		return fmt.Errorf("workflow DAG has a cycle involving node %q — refusing to execute", cycleNode)
	}
	// S4.2: keep last snapshot for approval/resume enrichment.
	if workflowState != nil {
		snap := workflowState.Snapshot()
		e.lastWorkflowSnapshot = &snap
	}
	scheduler := NewScheduler(workflowState)
	schedulerResult, err := scheduler.RunReadyNodes(NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		plannerNode := planner.WorkflowNode{
			ID:           node.ID,
			Title:        node.Title,
			AssignedRole: node.AssignedRole,
			DependsOn:    append([]string(nil), node.DependsOn...),
		}
		if err := e.emit(runID, taskID, "", "planning", "workflow.node_created", "runtime", map[string]any{"node_id": node.ID, "title": node.Title, "assigned_role": node.AssignedRole, "depends_on": node.DependsOn}, nil); err != nil {
			return SchedulerNodeResult{}, err
		}
		if err := e.emit(runID, taskID, "", "planning", "workflow.node_activated", "runtime", map[string]any{"node_id": node.ID, "title": node.Title, "assigned_role": node.AssignedRole}, nil); err != nil {
			return SchedulerNodeResult{}, err
		}
		// S4.5: prefer node-specific avatar (InstanceID / parallel survey IDs).
		assignedAvatarID := avatarIDForWorkflowNode(plan, node)
		if assignedAvatarID != "" {
			if err := e.emit(runID, taskID, assignedAvatarID, "planning", "avatar.assigned", "runtime", map[string]any{
				"node_id":   node.ID,
				"role":      node.AssignedRole,
				"title":     node.Title,
				"avatar_id": assignedAvatarID,
			}, nil); err != nil {
				return SchedulerNodeResult{}, err
			}
			if avatar, ok := avatarByID[assignedAvatarID]; ok {
				if err := e.emitAvatarSummary(runID, taskID, assignedAvatarID, avatar.Role, avatarSummaryText(avatar.Role, avatar.Responsibility, node.Title)); err != nil {
					return SchedulerNodeResult{}, err
				}
			}
			for _, dependencyID := range node.DependsOn {
				dependency, ok := nodeByID[dependencyID]
				if !ok {
					continue
				}
				if err := e.emitAvatarHandoff(runID, taskID, dependency, plannerNode, plan); err != nil {
					return SchedulerNodeResult{}, err
				}
			}
		}
		var nodeWorkResult SchedulerNodeResult
		if nodeWork != nil {
			var err error
			nodeWorkResult, err = nodeWork(node)
			if err != nil {
				return SchedulerNodeResult{}, err
			}
			if strings.TrimSpace(nodeWorkResult.PausePoint.ID) != "" {
				return nodeWorkResult, nil
			}
		}
		artifactIDs := nodeWorkResult.ArtifactIDs
		if len(artifactIDs) == 0 {
			artifactIDs = workflowNodeArtifactIDs(node)
		}
		if err := e.emit(runID, taskID, "", "planning", "workflow.node_completed", "runtime", map[string]any{
			"node_id":       node.ID,
			"title":         node.Title,
			"assigned_role": node.AssignedRole,
			"node_role":     node.AssignedRole,
			"node_title":    node.Title,
			"artifact_ids":  artifactIDs,
		}, nil); err != nil {
			return SchedulerNodeResult{}, err
		}
		for _, next := range workflowState.Nodes() {
			for _, dependencyID := range next.DependsOn {
				if dependencyID != node.ID {
					continue
				}
				if err := e.emit(runID, taskID, "", "planning", "workflow.edge_advanced", "runtime", map[string]any{"from_node_id": dependencyID, "to_node_id": next.ID, "from_title": node.Title, "to_title": next.Title}, nil); err != nil {
					return SchedulerNodeResult{}, err
				}
			}
		}
		return SchedulerNodeResult{ArtifactIDs: artifactIDs}, nil
	}))
	if schedulerResult.FailedNodeID != "" && strings.TrimSpace(schedulerResult.FailedPausePoint.ID) != "" {
		if persistErr := e.persistFailedNodeRecoveryEvaluation(runID, taskID, schedulerResult.FailedPausePoint, true); persistErr != nil && err == nil {
			return persistErr
		}
	}
	return err
}

func (e *Engine) persistFailedNodeRecoveryEvaluation(runID string, taskID string, point PausePoint, retryable bool) error {
	if e == nil || e.memory == nil || strings.TrimSpace(point.ID) == "" {
		return nil
	}
	details := []string{
		"pause_point_id: " + strings.TrimSpace(point.ID),
		"pause_point_kind: " + string(normalizePausePointKind(point.Kind)),
		"node_id: " + strings.TrimSpace(point.NodeID),
		"current_node_status: failed",
		"status_reason: " + strings.TrimSpace(point.StatusReason),
		"retryable: " + fmt.Sprintf("%t", retryable),
		"origin_run_id: " + strings.TrimSpace(point.OriginRunID),
		"origin_task_id: " + strings.TrimSpace(point.OriginTaskID),
		"pause_point_digest: " + strings.TrimSpace(point.Digest),
		"dependency_readiness: recorded",
		"scheduler_continuation_readiness: requires_manual_review",
	}
	if len(point.DependsOn) > 0 {
		details = append(details, "depends_on: "+strings.Join(point.DependsOn, ", "))
	}
	return e.memory.RecordEvaluation(memstore.EvaluationRecord{
		SessionID: e.sessionID,
		RunID:     strings.TrimSpace(runID),
		TaskID:    strings.TrimSpace(taskID),
		Kind:      "runtime_lifecycle",
		Verdict:   "INFO",
		Cause:     "failed_node_pause_point",
		Summary:   fmt.Sprintf("Failed workflow node %s recorded as recovery coordinate.", strings.TrimSpace(point.NodeID)),
		Source:    "runtime",
		Details:   details,
		UpdatedAt: time.Now().UTC(),
	})
}
