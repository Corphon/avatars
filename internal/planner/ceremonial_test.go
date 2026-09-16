package planner

import (
	"strings"
	"testing"
)

func assertNoCeremonialDAGNodes(t *testing.T, plan Plan) {
	t.Helper()
	for _, node := range plan.Nodes {
		if isCeremonialDAGRole(node.AssignedRole) {
			t.Fatalf("default DAG must not list ceremonial role %s: %+v", node.AssignedRole, plan.Nodes)
		}
		for _, dep := range node.DependsOn {
			if strings.HasPrefix(dep, "node-plan") || dep == "node-summarize" {
				t.Fatalf("work node %s still depends on ceremonial id %s: %+v", node.ID, dep, plan.Nodes)
			}
		}
	}
}

func TestC3_WithoutCeremonialDAGNodes_RewiresAndDrops(t *testing.T) {
	got := WithoutCeremonialDAGNodes([]WorkflowNode{
		{ID: "node-plan", AssignedRole: "Planner"},
		{ID: "node-survey", AssignedRole: "Researcher", DependsOn: []string{"node-plan"}},
		{ID: "node-build", AssignedRole: "Builder", DependsOn: []string{"node-survey"}},
		{ID: "node-review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
		{ID: "node-summarize", AssignedRole: "Synthesizer", DependsOn: []string{"node-review"}},
	})
	if len(got) != 3 {
		t.Fatalf("got %d nodes, want 3: %+v", len(got), got)
	}
	if got[0].ID != "node-survey" || len(got[0].DependsOn) != 0 {
		t.Fatalf("survey should be a root after stripping planner, got %+v", got[0])
	}
	if got[1].ID != "node-build" || len(got[1].DependsOn) != 1 || got[1].DependsOn[0] != "node-survey" {
		t.Fatalf("builder deps: %+v", got[1])
	}
	if got[2].ID != "node-review" || len(got[2].DependsOn) != 1 || got[2].DependsOn[0] != "node-build" {
		t.Fatalf("critic deps: %+v", got[2])
	}
}

func TestC3_WithoutCeremonialDAGNodes_Idempotent(t *testing.T) {
	nodes := []WorkflowNode{
		{ID: "node-build", AssignedRole: "Builder"},
		{ID: "node-review", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	}
	once := WithoutCeremonialDAGNodes(nodes)
	twice := WithoutCeremonialDAGNodes(once)
	if len(once) != 2 || len(twice) != 2 {
		t.Fatalf("idempotent strip changed length: %+v -> %+v", once, twice)
	}
}

func TestC3_DefaultPlansKeepAvatarsDropCeremonialNodes(t *testing.T) {
	small := BuildForComplexity("small task", Context{}, ComplexityAssessment{Level: ComplexitySmall})
	if small.AvatarIDByRole("Planner") == "" || small.AvatarIDByRole("Synthesizer") == "" {
		t.Fatalf("small plan must keep Planner/Synthesizer avatars, got %+v", small.Avatars)
	}
	assertNoCeremonialDAGNodes(t, small)
	if len(small.Nodes) != 1 || small.Nodes[0].AssignedRole != "Builder" {
		t.Fatalf("small DAG should be builder-only, got %+v", small.Nodes)
	}

	medium := BuildForComplexity("medium task", Context{}, ComplexityAssessment{Level: ComplexityMedium})
	if medium.AvatarIDByRole("Planner") == "" || medium.AvatarIDByRole("Synthesizer") == "" {
		t.Fatalf("medium plan must keep Planner/Synthesizer avatars, got %+v", medium.Avatars)
	}
	assertNoCeremonialDAGNodes(t, medium)

	large := BuildForComplexity("redesign the whole system", Context{}, ComplexityAssessment{
		Level:            ComplexityLarge,
		SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
	})
	if len(large.Avatars) != 5 {
		t.Fatalf("large plan must keep 5 avatars, got %d", len(large.Avatars))
	}
	assertNoCeremonialDAGNodes(t, large)
	if len(large.Nodes) != 3 {
		t.Fatalf("large linear DAG should be survey/build/review, got %+v", large.Nodes)
	}

	trivial := BuildForComplexity("hi", Context{}, ComplexityAssessment{Level: ComplexityTrivial})
	if len(trivial.Nodes) != 1 || trivial.Nodes[0].AssignedRole != "Direct" {
		t.Fatalf("trivial Direct DAG changed: %+v", trivial.Nodes)
	}
}

func TestC3_DynamicAvatarsStripCeremonialSlots(t *testing.T) {
	plan := BuildForComplexity("summarize repository health", Context{}, ComplexityAssessment{
		Level:            ComplexityMedium,
		FromLLM:          true,
		SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Synthesizer"},
	})
	assertNoCeremonialDAGNodes(t, plan)
	if plan.AvatarIDByRole("Planner") == "" || plan.AvatarIDByRole("Synthesizer") == "" {
		t.Fatalf("LLM-sized plan must still list Planner/Synthesizer avatars, got %+v", plan.Avatars)
	}
}

func TestC3_Build_AnalyzeTaskHasNoCeremonialDAG(t *testing.T) {
	plan := Build("Analyze the current repository and propose a refactoring plan")
	if plan.AvatarIDByRole("Planner") == "" || plan.AvatarIDByRole("Synthesizer") == "" {
		t.Fatalf("expected Planner and Synthesizer avatars, got %+v", plan.Avatars)
	}
	assertNoCeremonialDAGNodes(t, plan)
	if len(plan.Nodes) < 1 {
		t.Fatal("expected at least one work node")
	}
}
