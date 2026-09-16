package runtime

import (
	"testing"

	"avatars/internal/planner"
)

func TestAvatarExecutionContext_AllowsRoleScopedTools(t *testing.T) {
	plan := planner.Plan{
		Title: "analyze repository context",
		Avatars: []planner.Avatar{
			{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose"},
			{ID: "avatar-researcher", Role: "Researcher", Responsibility: "survey"},
			{ID: "avatar-builder", Role: "Builder", Responsibility: "build"},
			{ID: "avatar-critic", Role: "Critic", Responsibility: "review"},
			{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "summarize"},
		},
	}

	plannerContext, err := BuildAvatarContext(plan, "avatar-planner")
	if err != nil {
		t.Fatalf("build planner context failed: %v", err)
	}
	if !plannerContext.HasVisibleScope(AvatarContextScopeWorkflowGraph) {
		t.Fatalf("expected planner workflow graph visibility, got %+v", plannerContext)
	}
	if !plannerContext.CanUseTool("read", "file_read") {
		t.Fatalf("expected planner to use read tools")
	}

	researcherContext, err := BuildAvatarContext(plan, "avatar-researcher")
	if err != nil {
		t.Fatalf("build researcher context failed: %v", err)
	}
	if !researcherContext.CanUseTool("read", "file_read") {
		t.Fatalf("expected researcher to use read tools")
	}
	if researcherContext.CanUseTool("write", "file_write") {
		t.Fatalf("expected researcher to be denied guarded writes")
	}

	builderContext, err := BuildAvatarContext(plan, "avatar-builder")
	if err != nil {
		t.Fatalf("build builder context failed: %v", err)
	}
	if !builderContext.CanUseTool("write", "file_write") {
		t.Fatalf("expected builder to use guarded writes")
	}
	if builderContext.CanUseTool("shell", "command_exec") {
		t.Fatalf("expected builder shell access to remain off by default")
	}

	criticContext, err := BuildAvatarContext(plan, "avatar-critic")
	if err != nil {
		t.Fatalf("build critic context failed: %v", err)
	}
	if criticContext.CanUseTool("write", "file_write") {
		t.Fatalf("expected critic to be denied mutating tools")
	}
	if err := criticContext.RequireTool("write", "file_write"); err == nil {
		t.Fatal("expected critic write request to fail")
	}

	synthesizerContext, err := BuildAvatarContext(plan, "avatar-synthesizer")
	if err != nil {
		t.Fatalf("build synthesizer context failed: %v", err)
	}
	if !synthesizerContext.HasVisibleScope(AvatarContextScopeApprovedResult) {
		t.Fatalf("expected synthesizer approved result visibility, got %+v", synthesizerContext)
	}
	if synthesizerContext.CanUseTool("write", "file_write") {
		t.Fatalf("expected synthesizer to remain read-focused")
	}
}

func TestBuildAvatarContext_RejectsUnknownAvatar(t *testing.T) {
	plan := planner.Plan{
		Avatars: []planner.Avatar{{ID: "avatar-builder", Role: "Builder"}},
	}
	if _, err := BuildAvatarContext(plan, "avatar-missing"); err == nil {
		t.Fatal("expected unknown avatar lookup to fail")
	}
}
