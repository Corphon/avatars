package planner

import (
	"testing"
)

func TestS4_SplitBuilderNodes_ParallelByDefault(t *testing.T) {
	nodes := []WorkflowNode{
		{ID: "node-plan", AssignedRole: "Planner"},
		{ID: "node-build", AssignedRole: "Builder", DependsOn: []string{"node-plan"}},
		{ID: "node-review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	}
	out := splitBuilderNodes(nodes, []string{"a.go", "b.go"})
	var builders []WorkflowNode
	for _, n := range out {
		if n.AssignedRole == "Builder" {
			builders = append(builders, n)
		}
	}
	if len(builders) != 2 {
		t.Fatalf("builders=%d", len(builders))
	}
	// Parallel: both depend on node-plan, not on each other.
	for _, b := range builders {
		for _, dep := range b.DependsOn {
			if dep == builders[0].ID || dep == builders[1].ID {
				t.Fatalf("parallel builders must not depend on each other: %+v", b)
			}
		}
	}
}

func TestS4_ApplyParallelGroups(t *testing.T) {
	nodes := []WorkflowNode{
		{ID: "b1", AssignedRole: "Builder", DependsOn: []string{"plan"}},
		{ID: "b2", AssignedRole: "Builder", DependsOn: []string{"b1"}},
		{ID: "b3", AssignedRole: "Builder", DependsOn: []string{"b2"}},
	}
	out := applyParallelGroups(nodes, []int{3})
	if len(out[1].DependsOn) == 1 && out[1].DependsOn[0] == "b1" {
		t.Fatalf("expected sibling dep cleared, got %+v", out[1].DependsOn)
	}
}
