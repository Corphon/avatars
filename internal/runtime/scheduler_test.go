package runtime

import (
	"reflect"
	"testing"

	"avatars/internal/planner"
)

func TestScheduler_ActivatesReadyNodesInDependencyOrder(t *testing.T) {
	state := NewWorkflowState(planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-plan", Title: "Plan", AssignedRole: "Planner"},
		{ID: "node-build", Title: "Build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
		{ID: "node-review", Title: "Review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	}})
	var executed []string

	result, err := NewScheduler(state).RunReadyNodes(NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		executed = append(executed, node.ID)
		return SchedulerNodeResult{}, nil
	}))
	if err != nil {
		t.Fatalf("scheduler run failed: %v", err)
	}
	expected := []string{"node-plan", "node-build", "node-review"}
	if !reflect.DeepEqual(executed, expected) {
		t.Fatalf("expected execution order %+v, got %+v", expected, executed)
	}
	if !reflect.DeepEqual(result.CompletedNodeIDs, expected) {
		t.Fatalf("expected completed node ids %+v, got %+v", expected, result.CompletedNodeIDs)
	}
	if state.Node("node-review").Status != WorkflowNodeStatusCompleted {
		t.Fatalf("expected node-review completed, got %+v", state.Node("node-review"))
	}
}

func TestScheduler_RecordsNodeFailure(t *testing.T) {
	state := NewWorkflowState(planner.Plan{Nodes: []planner.WorkflowNode{{ID: "node-build", Title: "Build", AssignedRole: "Builder"}}})

	result, err := NewScheduler(state).RunReadyNodes(NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		return SchedulerNodeResult{}, errSchedulerTestFailure{}
	}))
	if err == nil {
		t.Fatal("expected scheduler failure")
	}
	node := state.Node("node-build")
	if node.Status != WorkflowNodeStatusFailed || node.StatusReason == "" {
		t.Fatalf("expected failed node status and reason, got %+v", node)
	}
	if result.FailedNodeID != "node-build" || result.FailedPausePoint.ID == "" {
		t.Fatalf("expected failed-node pause point result, got %+v", result)
	}
	if result.FailedPausePoint.Kind != PausePointKindWorkflowNode || result.FailedPausePoint.StatusReason != "scheduler test failure" {
		t.Fatalf("expected workflow-node failed pause point reason, got %+v", result.FailedPausePoint)
	}
}

func TestScheduler_StopsAtPausePoint(t *testing.T) {
	state := NewWorkflowState(planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-plan", Title: "Plan", AssignedRole: "Planner"},
		{ID: "node-build", Title: "Build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
	}})
	pause, err := state.PausePointForNodeInRun("run-1", "task-1", "node-build")
	if err != nil {
		t.Fatalf("build pause point failed: %v", err)
	}

	result, err := NewScheduler(state).RunReadyNodes(NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		if node.ID == "node-build" {
			return SchedulerNodeResult{PausePoint: pause}, nil
		}
		return SchedulerNodeResult{}, nil
	}))
	if err != nil {
		t.Fatalf("scheduler run failed: %v", err)
	}
	if result.PausedNodeID != "node-build" || result.PausePoint.ID != pause.ID {
		t.Fatalf("expected scheduler pause at node-build, got %+v", result)
	}
	if state.Node("node-build").Status != WorkflowNodeStatusActive {
		t.Fatalf("expected paused node to remain active, got %+v", state.Node("node-build"))
	}
}

func TestScheduler_ResumesFromPausePoint(t *testing.T) {
	state := NewWorkflowState(planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-build", Title: "Build", AssignedRole: "Builder"},
		{ID: "node-review", Title: "Review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	}})
	if err := state.Activate("node-build"); err != nil {
		t.Fatalf("activate node-build failed: %v", err)
	}
	pause, err := state.PausePointForNodeInRun("run-1", "task-1", "node-build")
	if err != nil {
		t.Fatalf("build pause point failed: %v", err)
	}

	result, err := NewScheduler(state).ResumeFromPausePoint(pause, NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		return SchedulerNodeResult{}, nil
	}))
	if err != nil {
		t.Fatalf("resume from pause point failed: %v", err)
	}
	if result.ResumedFrom != pause.ID {
		t.Fatalf("expected resumed from pause point %q, got %+v", pause.ID, result)
	}
	if state.Node("node-build").Status != WorkflowNodeStatusCompleted || state.Node("node-review").Status != WorkflowNodeStatusCompleted {
		t.Fatalf("expected resumed scheduler to complete build and review, got build=%+v review=%+v", state.Node("node-build"), state.Node("node-review"))
	}
}

type errSchedulerTestFailure struct{}

func (errSchedulerTestFailure) Error() string {
	return "scheduler test failure"
}
