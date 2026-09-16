package runtime

import (
	"testing"

	"avatars/internal/planner"
)

func TestWorkflowState_ActivatesDependencyOrder(t *testing.T) {
	plan := planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-plan", Title: "Plan", AssignedRole: "Planner"},
		{ID: "node-build", Title: "Build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
		{ID: "node-review", Title: "Review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	}}
	state := NewWorkflowState(plan)

	ready := state.ReadyNodes()
	if len(ready) != 1 || ready[0].ID != "node-plan" {
		t.Fatalf("expected only node-plan ready, got %+v", ready)
	}
	if err := state.Activate("node-plan"); err != nil {
		t.Fatalf("activate node-plan failed: %v", err)
	}
	if err := state.Complete("node-plan", []string{"artifact-plan"}); err != nil {
		t.Fatalf("complete node-plan failed: %v", err)
	}
	ready = state.ReadyNodes()
	if len(ready) != 1 || ready[0].ID != "node-build" {
		t.Fatalf("expected node-build ready after plan completion, got %+v", ready)
	}
	if state.Node("node-plan").Status != WorkflowNodeStatusCompleted {
		t.Fatalf("expected node-plan completed, got %+v", state.Node("node-plan"))
	}
	if got := state.Node("node-plan").ArtifactIDs; len(got) != 1 || got[0] != "artifact-plan" {
		t.Fatalf("expected node-plan artifact ids, got %+v", got)
	}
}

func TestWorkflowState_RecordsSkippedNode(t *testing.T) {
	plan := planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-plan", Title: "Plan", AssignedRole: "Planner"},
		{ID: "node-build", Title: "Build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
	}}
	state := NewWorkflowState(plan)

	if err := state.Skip("node-plan", "operator deferred planning"); err != nil {
		t.Fatalf("skip node-plan failed: %v", err)
	}
	node := state.Node("node-plan")
	if node.Status != WorkflowNodeStatusSkipped {
		t.Fatalf("expected skipped status, got %+v", node)
	}
	if node.StatusReason != "operator deferred planning" {
		t.Fatalf("expected skip reason, got %+v", node)
	}
	if ready := state.ReadyNodes(); len(ready) != 1 || ready[0].ID != "node-build" {
		t.Fatalf("expected dependent node ready after skip, got %+v", ready)
	}
}

func TestWorkflowState_PausePointForNodeIncludesWorkflowCoordinates(t *testing.T) {
	plan := planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-plan", Title: "Plan", AssignedRole: "Planner"},
		{ID: "node-build", Title: "Build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
		{ID: "node-review", Title: "Review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	}}
	state := NewWorkflowState(plan)

	point, err := state.PausePointForNodeInRun("run-1", "task-1", "node-build")
	if err != nil {
		t.Fatalf("pause point for workflow node failed: %v", err)
	}
	if point.ID == "" || point.Kind != PausePointKindWorkflowNode {
		t.Fatalf("expected workflow node pause point, got %+v", point)
	}
	payload := point.Payload()
	assertPayloadValue(t, payload, "pause_point_kind", "workflow_node")
	assertPayloadValue(t, payload, "origin_run_id", "run-1")
	assertPayloadValue(t, payload, "origin_task_id", "task-1")
	assertPayloadValue(t, payload, "node_id", "node-build")
	assertPayloadValue(t, payload, "assigned_role", "Builder")
	assertPayloadValue(t, payload, "title", "Build")
	dependencies, ok := payload["depends_on"].([]string)
	if !ok || len(dependencies) != 1 || dependencies[0] != "node-plan" {
		t.Fatalf("expected workflow pause dependencies, got %+v", payload["depends_on"])
	}
}

func TestWorkflowState_FailedNodePausePointIncludesStatusReason(t *testing.T) {
	plan := planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-build", Title: "Build", AssignedRole: "Builder"},
	}}
	state := NewWorkflowState(plan)
	if err := state.Fail("node-build", errWorkflowTestFailure{}); err != nil {
		t.Fatalf("fail node-build failed: %v", err)
	}

	point, err := state.PausePointForNodeInRun("run-1", "task-1", "node-build")
	if err != nil {
		t.Fatalf("pause point for failed workflow node failed: %v", err)
	}
	if point.Kind != PausePointKindWorkflowNode {
		t.Fatalf("expected workflow node pause point, got %+v", point)
	}
	payload := point.Payload()
	assertPayloadValue(t, payload, "pause_point_kind", "workflow_node")
	assertPayloadValue(t, payload, "origin_run_id", "run-1")
	assertPayloadValue(t, payload, "origin_task_id", "task-1")
	assertPayloadValue(t, payload, "node_id", "node-build")
	assertPayloadValue(t, payload, "status_reason", "workflow test failure")
	if point.ID == PausePointID(PausePointKindVerifierRemediation, "run-1", "task-1", "node-build", "node-build", "", "workflow", "node_pause", point.Digest) {
		t.Fatalf("failed-node pause point must not use verifier remediation identity, got %+v", point)
	}
	if point.ID == PausePointID(PausePointKindApprovalGate, "run-1", "task-1", "node-build", "node-build", "", "workflow", "node_pause", point.Digest) {
		t.Fatalf("failed-node pause point must not use approval gate identity, got %+v", point)
	}
}

func TestWorkflowState_FailedNodeRetryContractCoordinatesAreStable(t *testing.T) {
	plan := planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-plan", Title: "Plan", AssignedRole: "Planner"},
		{ID: "node-build", Title: "Build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
	}}
	state := NewWorkflowState(plan)
	if err := state.Complete("node-plan", []string{"artifact-plan"}); err != nil {
		t.Fatalf("complete node-plan failed: %v", err)
	}
	if err := state.Fail("node-build", errWorkflowTestFailure{}); err != nil {
		t.Fatalf("fail node-build failed: %v", err)
	}

	point, err := state.PausePointForNodeInRun("run-contract", "task-contract", "node-build")
	if err != nil {
		t.Fatalf("pause point for failed workflow node failed: %v", err)
	}
	if point.ID == "" || point.Digest == "" {
		t.Fatalf("expected pause point id and digest, got %+v", point)
	}
	if point.Kind != PausePointKindWorkflowNode || point.OriginRunID != "run-contract" || point.OriginTaskID != "task-contract" || point.NodeID != "node-build" {
		t.Fatalf("expected workflow-node retry coordinates, got %+v", point)
	}
	if point.ActionID != "node-build" || point.Tool != "workflow" || point.Operation != "node_pause" || point.ApprovalKey != "" {
		t.Fatalf("expected scheduler-scoped pause point identity, got %+v", point)
	}
	if len(point.DependsOn) != 1 || point.DependsOn[0] != "node-plan" {
		t.Fatalf("expected dependency coordinate, got %+v", point.DependsOn)
	}

	payload := point.Payload()
	assertPayloadValue(t, payload, "digest", point.Digest)
	assertPayloadValue(t, payload, "pause_point_kind", "workflow_node")
	assertPayloadValue(t, payload, "origin_run_id", "run-contract")
	assertPayloadValue(t, payload, "origin_task_id", "task-contract")
	assertPayloadValue(t, payload, "node_id", "node-build")
}

func TestWorkflowCheckpointFromStateCarriesPausePointAndCoordinates(t *testing.T) {
	plan := planner.Plan{Summary: "Trusted workflow", Nodes: []planner.WorkflowNode{
		{ID: "node-plan", Title: "Plan", AssignedRole: "Planner"},
		{ID: "node-survey", Title: "Survey", AssignedRole: "Researcher", DependsOn: []string{"node-plan"}},
		{ID: "node-build", Title: "Build", AssignedRole: "Builder", DependsOn: []string{"node-survey"}},
		{ID: "node-review", Title: "Review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	}}
	state := NewWorkflowState(plan)
	if err := state.Complete("node-plan", nil); err != nil {
		t.Fatalf("complete node-plan failed: %v", err)
	}
	if err := state.Complete("node-survey", nil); err != nil {
		t.Fatalf("complete node-survey failed: %v", err)
	}
	if err := state.Activate("node-build"); err != nil {
		t.Fatalf("activate node-build failed: %v", err)
	}

	checkpoint := buildWorkflowCheckpointFromState(plan, state, "run-1", "task-1", "Builder", "tool.awaiting_approval")
	if checkpoint == nil {
		t.Fatal("expected workflow checkpoint")
	}
	if checkpoint.PausePointID == "" || checkpoint.PausePointKind != "workflow_node" {
		t.Fatalf("expected workflow node pause point in checkpoint, got %+v", checkpoint)
	}
	if checkpoint.BlockedNodeID != "node-build" || checkpoint.BlockedNodeRole != "Builder" {
		t.Fatalf("expected blocked builder coordinate, got %+v", checkpoint)
	}
	if len(checkpoint.BlockedNodeDependsOn) != 1 || checkpoint.BlockedNodeDependsOn[0] != "node-survey" {
		t.Fatalf("expected blocked dependencies, got %+v", checkpoint.BlockedNodeDependsOn)
	}
	if len(checkpoint.CompletedNodeIDs) != 2 || checkpoint.CompletedNodeIDs[0] != "node-plan" || checkpoint.CompletedNodeIDs[1] != "node-survey" {
		t.Fatalf("expected completed node coordinates, got %+v", checkpoint.CompletedNodeIDs)
	}
	if len(checkpoint.RemainingNodeIDs) != 1 || checkpoint.RemainingNodeIDs[0] != "node-review" {
		t.Fatalf("expected remaining node coordinates, got %+v", checkpoint.RemainingNodeIDs)
	}
}

func TestWorkflowState_HasCycle_NoCycle(t *testing.T) {
	plan := planner.Plan{
		Nodes: []planner.WorkflowNode{
			{ID: "a", DependsOn: nil},
			{ID: "b", DependsOn: []string{"a"}},
			{ID: "c", DependsOn: []string{"a", "b"}},
		},
	}
	state := NewWorkflowState(plan)
	hasCycle, msg := state.HasCycle()
	if hasCycle {
		t.Fatalf("expected no cycle, got: %s", msg)
	}
}

func TestWorkflowState_HasCycle_Detected(t *testing.T) {
	plan := planner.Plan{
		Nodes: []planner.WorkflowNode{
			{ID: "a", DependsOn: []string{"c"}}, // a → c
			{ID: "b", DependsOn: []string{"a"}}, // b → a
			{ID: "c", DependsOn: []string{"b"}}, // c → b  — cycle a→c→b→a
		},
	}
	state := NewWorkflowState(plan)
	hasCycle, msg := state.HasCycle()
	if !hasCycle {
		t.Fatal("expected cycle to be detected, got none")
	}
	t.Logf("cycle message: %s", msg)
}

func TestWorkflowState_HasCycle_SelfLoop(t *testing.T) {
	plan := planner.Plan{
		Nodes: []planner.WorkflowNode{
			{ID: "a", DependsOn: []string{"a"}}, // self-loop
		},
	}
	state := NewWorkflowState(plan)
	hasCycle, _ := state.HasCycle()
	if !hasCycle {
		t.Fatal("expected self-loop to be detected as cycle")
	}
}

func TestWorkflowState_PendingNodes_Locked(t *testing.T) {
	plan := planner.Plan{
		Nodes: []planner.WorkflowNode{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"a"}},
		},
	}
	state := NewWorkflowState(plan)
	pending := state.PendingNodes()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending nodes, got %d", len(pending))
	}
	// Activate one, Pending should still work.
	_ = state.Activate("a")
	pending = state.PendingNodes()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after activation, got %d", len(pending))
	}
}

type errWorkflowTestFailure struct{}

func (errWorkflowTestFailure) Error() string {
	return "workflow test failure"
}
