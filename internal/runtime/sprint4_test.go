package runtime

import (
	"strings"
	"testing"

	"avatars/internal/planner"
)

func TestS4_PlanFromApprovalCheckpoint_BuildsRemainingRoles(t *testing.T) {
	cp := &PendingApprovalWorkflow{
		PlanSummary:     "test plan",
		BlockedNodeID:   "node-build",
		BlockedNodeRole: "Builder",
		BlockedNodeTitle: "Build",
		CompletedNodes: []PendingApprovalWorkflowNode{
			{ID: "node-plan", Role: "Planner", Title: "Plan"},
		},
		RemainingNodes: []PendingApprovalWorkflowNode{
			{ID: "node-review", Role: "Critic", Title: "Review", DependsOn: []string{"node-build"}},
			{ID: "node-summarize", Role: "Synthesizer", Title: "Summarize", DependsOn: []string{"node-review"}},
		},
	}
	plan := planFromApprovalCheckpoint(cp)
	if len(plan.Nodes) < 3 {
		t.Fatalf("expected >=3 nodes, got %d", len(plan.Nodes))
	}
	state := workflowStateFromApprovalCheckpoint(cp)
	if state == nil {
		t.Fatal("expected workflow state")
	}
	if state.Node("node-plan").Status != WorkflowNodeStatusCompleted {
		t.Fatalf("node-plan should be completed, got %s", state.Node("node-plan").Status)
	}
}

func TestS4_AvatarIDForWorkflowNode_ParallelSurvey(t *testing.T) {
	plan := planner.Plan{
		Avatars: []planner.Avatar{
			{ID: "avatar-researcher-docs", Role: "Researcher", InstanceID: "docs"},
			{ID: "avatar-researcher-src", Role: "Researcher", InstanceID: "src"},
			{ID: "avatar-researcher-cfg", Role: "Researcher", InstanceID: "cfg"},
		},
	}
	id := avatarIDForWorkflowNode(plan, WorkflowNodeRuntime{ID: "node-survey-src", AssignedRole: "Researcher"})
	if id != "avatar-researcher-src" {
		t.Fatalf("got %q, want avatar-researcher-src", id)
	}
}

func TestS4_RecordNodeRetryFailure_Increments(t *testing.T) {
	e := &Engine{projectRoot: t.TempDir()}
	e.recordNodeRetryFailure("node-build")
	e.recordNodeRetryFailure("node-build")
	if e.nodeRetryCount["node-build"] != 2 {
		t.Fatalf("count=%d", e.nodeRetryCount["node-build"])
	}
}

func TestS4_EnrichWorkflowCheckpoint(t *testing.T) {
	plan := planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-plan", AssignedRole: "Planner"},
		{ID: "node-build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
	}}
	state := NewWorkflowState(plan)
	_ = state.Activate("node-plan")
	_ = state.Complete("node-plan", nil)
	cp := buildWorkflowCheckpointFromState(plan, state, "run", "task", "Builder", "tool.awaiting_approval")
	cp = enrichWorkflowCheckpoint(cp, state, map[string]int{"node-build": 1}, "do the thing", "survey done")
	if cp.WorkflowSnapshot == nil || len(cp.WorkflowSnapshot.Nodes) == 0 {
		t.Fatal("expected snapshot")
	}
	if cp.NodeRetryCounts["node-build"] != 1 {
		t.Fatal("expected retry counts")
	}
	if !strings.Contains(cp.TaskInput, "do the thing") {
		t.Fatal("expected task input")
	}
}
