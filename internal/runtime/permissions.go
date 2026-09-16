package runtime

import "avatars/internal/runtime/approval"

// PermissionMode is an alias of approval.Mode so existing callers keep compiling.
type PermissionMode = approval.Mode

const (
	PermissionModeDefault           = approval.ModeDefault
	PermissionModeAcceptEdits       = approval.ModeAcceptEdits
	PermissionModeDontAsk           = approval.ModeDontAsk
	PermissionModePlan              = approval.ModePlan
	PermissionModeBypassPermissions = approval.ModeBypassPermissions
)

func ParsePermissionMode(value string) (PermissionMode, error) {
	return approval.Parse(value)
}

func normalizePermissionMode(mode PermissionMode) PermissionMode {
	return approval.Normalize(mode)
}

func permissionModePolicyDecision(mode PermissionMode, toolName string, input any) (string, string, string) {
	return approval.Policy(mode, toolName, toolIsReadOnly(toolName, input))
}
