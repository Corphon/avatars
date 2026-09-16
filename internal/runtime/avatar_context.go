package runtime

import (
	"fmt"
	"strings"

	"avatars/internal/planner"
)

type AvatarContextScope string

const (
	AvatarContextScopeTaskSummary    AvatarContextScope = "task_summary"
	AvatarContextScopeWorkflowGraph  AvatarContextScope = "workflow_graph"
	AvatarContextScopeAvatarSummary  AvatarContextScope = "avatar_summary"
	AvatarContextScopeApprovedResult AvatarContextScope = "approved_result"
)

type AvatarToolPolicy struct {
	AllowReadTools     bool
	AllowGuardedMutate bool
	AllowShell         bool
}

type AvatarExecutionContext struct {
	AvatarID       string
	Role           string
	Responsibility string
	VisibleScopes  []AvatarContextScope
	ToolPolicy     AvatarToolPolicy
}

func BuildAvatarContext(plan planner.Plan, avatarID string) (AvatarExecutionContext, error) {
	trimmedAvatarID := strings.TrimSpace(avatarID)
	for _, avatar := range plan.Avatars {
		if strings.TrimSpace(avatar.ID) != trimmedAvatarID {
			continue
		}
		return contextForAvatar(avatar), nil
	}
	if trimmedAvatarID == "" {
		return AvatarExecutionContext{}, fmt.Errorf("avatar id is required")
	}
	return AvatarExecutionContext{}, fmt.Errorf("avatar %s not found in plan", trimmedAvatarID)
}

func contextForAvatar(avatar planner.Avatar) AvatarExecutionContext {
	role := strings.TrimSpace(avatar.Role)
	context := AvatarExecutionContext{
		AvatarID:       strings.TrimSpace(avatar.ID),
		Role:           role,
		Responsibility: strings.TrimSpace(avatar.Responsibility),
	}
	switch role {
	case "Planner":
		context.VisibleScopes = []AvatarContextScope{AvatarContextScopeTaskSummary, AvatarContextScopeWorkflowGraph}
		context.ToolPolicy = AvatarToolPolicy{AllowReadTools: true}
	case "Researcher":
		context.VisibleScopes = []AvatarContextScope{AvatarContextScopeTaskSummary, AvatarContextScopeAvatarSummary}
		context.ToolPolicy = AvatarToolPolicy{AllowReadTools: true}
	case "Builder":
		context.VisibleScopes = []AvatarContextScope{AvatarContextScopeTaskSummary, AvatarContextScopeAvatarSummary}
		context.ToolPolicy = AvatarToolPolicy{AllowReadTools: true, AllowGuardedMutate: true}
	case "Runner":
		context.VisibleScopes = []AvatarContextScope{AvatarContextScopeTaskSummary, AvatarContextScopeAvatarSummary}
		context.ToolPolicy = AvatarToolPolicy{AllowReadTools: true, AllowGuardedMutate: true, AllowShell: true}
	case "Critic":
		context.VisibleScopes = []AvatarContextScope{AvatarContextScopeTaskSummary, AvatarContextScopeAvatarSummary}
		context.ToolPolicy = AvatarToolPolicy{AllowReadTools: true}
	case "Synthesizer":
		context.VisibleScopes = []AvatarContextScope{AvatarContextScopeTaskSummary, AvatarContextScopeApprovedResult}
		context.ToolPolicy = AvatarToolPolicy{AllowReadTools: true}
	default:
		context.VisibleScopes = []AvatarContextScope{AvatarContextScopeTaskSummary}
	}
	return context
}

func (c AvatarExecutionContext) CanUseTool(toolName string, operation string) bool {
	tool := strings.TrimSpace(toolName)
	switch tool {
	case "read", "git":
		return c.ToolPolicy.AllowReadTools
	case "write", "patch":
		return c.ToolPolicy.AllowGuardedMutate
	case "shell":
		return c.ToolPolicy.AllowShell
	default:
		return false
	}
}

func (c AvatarExecutionContext) RequireTool(toolName string, operation string) error {
	if c.CanUseTool(toolName, operation) {
		return nil
	}
	role := strings.TrimSpace(c.Role)
	if role == "" {
		role = "unknown"
	}
	return fmt.Errorf("avatar role %s is not allowed to use %s/%s", role, strings.TrimSpace(toolName), strings.TrimSpace(operation))
}

func (c AvatarExecutionContext) HasVisibleScope(scope AvatarContextScope) bool {
	for _, visible := range c.VisibleScopes {
		if visible == scope {
			return true
		}
	}
	return false
}
