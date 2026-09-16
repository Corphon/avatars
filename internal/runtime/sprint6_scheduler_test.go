package runtime

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"avatars/internal/planner"
)

func TestS6_Scheduler_ParallelPartialFailureContinuesSiblings(t *testing.T) {
	state := NewWorkflowState(planner.Plan{Nodes: []planner.WorkflowNode{
		{ID: "node-a", Title: "A", AssignedRole: "Researcher"},
		{ID: "node-b", Title: "B", AssignedRole: "Researcher"},
		{ID: "node-c", Title: "C", AssignedRole: "Builder", DependsOn: []string{"node-a"}},
		{ID: "node-d", Title: "D", AssignedRole: "Builder", DependsOn: []string{"node-b"}},
	}})

	var bStarted int32
	result, err := NewScheduler(state).RunReadyNodes(NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		switch node.ID {
		case "node-a":
			return SchedulerNodeResult{}, errors.New("survey a failed")
		case "node-b":
			atomic.AddInt32(&bStarted, 1)
			time.Sleep(20 * time.Millisecond)
			return SchedulerNodeResult{}, nil
		case "node-d":
			return SchedulerNodeResult{}, nil
		default:
			return SchedulerNodeResult{}, fmt.Errorf("unexpected node %s", node.ID)
		}
	}))
	if err != nil {
		t.Fatalf("expected nil error on partial failure, got %v", err)
	}
	if result.FailedNodeID != "node-a" {
		t.Fatalf("FailedNodeID=%q, want node-a; result=%+v", result.FailedNodeID, result)
	}
	if atomic.LoadInt32(&bStarted) == 0 {
		t.Fatal("sibling node-b should still run when node-a fails")
	}
	if state.Node("node-b").Status != WorkflowNodeStatusCompleted {
		t.Fatalf("node-b status=%v, want completed", state.Node("node-b").Status)
	}
	if state.Node("node-d").Status != WorkflowNodeStatusCompleted {
		t.Fatalf("node-d (depends on successful node-b) status=%v, want completed", state.Node("node-d").Status)
	}
	if state.Node("node-c").Status == WorkflowNodeStatusCompleted {
		t.Fatal("node-c must not complete when dependency node-a failed")
	}
}
