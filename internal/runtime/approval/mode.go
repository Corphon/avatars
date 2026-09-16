// Package approval owns permission-mode parsing and policy decisions.
// Engine.ContinueApprovedToolCall stays on runtime.Engine (needs transcript
// / DAG state); this package is the leaf so cmd and runtime share one policy.
package approval

import (
	"fmt"
	"strings"
)

type Mode string

const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModeDontAsk           Mode = "dontAsk"
	ModePlan              Mode = "plan"
	ModeBypassPermissions Mode = "bypassPermissions"
)

func Parse(value string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "acceptedits":
		return ModeAcceptEdits, nil
	case "default":
		return ModeDefault, nil
	case "dontask":
		return ModeDontAsk, nil
	case "plan":
		return ModePlan, nil
	case "bypasspermissions":
		return ModeBypassPermissions, nil
	default:
		return "", fmt.Errorf("invalid permission mode %q (expected one of: default, acceptEdits, dontAsk, plan, bypassPermissions)", strings.TrimSpace(value))
	}
}

func Normalize(mode Mode) Mode {
	parsed, err := Parse(string(mode))
	if err != nil {
		return ModeAcceptEdits
	}
	return parsed
}

// ContinuationMode is the permission bump used by Engine.ContinueApprovedToolCall:
// operator already approved the mutating gate, so default must not re-ask.
func ContinuationMode(current Mode) Mode {
	if current == ModeDefault || current == "" {
		return ModeAcceptEdits
	}
	return current
}

// Policy returns allow/ask/deny for a tool given whether it is read-only.
// Callers compute readonly (language-agnostic) so this package stays a leaf.
func Policy(mode Mode, toolName string, readonly bool) (decision, source, reason string) {
	resolvedMode := Normalize(mode)
	label := strings.TrimSpace(toolName)
	if label == "" {
		label = "tool"
	}

	switch resolvedMode {
	case ModeDefault:
		if readonly {
			return "allow", "runtime_builtin_policy", fmt.Sprintf("Permission mode default allows readonly %s actions inside the sandbox.", label)
		}
		return "ask", "runtime_permission_mode", fmt.Sprintf("Permission mode default requires approval before mutating %s actions, and approval prompts are not implemented yet.", label)
	case ModeAcceptEdits:
		return "allow", "runtime_builtin_policy", fmt.Sprintf("Permission mode acceptEdits allows %s actions within the sandbox.", label)
	case ModeDontAsk:
		if readonly {
			return "allow", "runtime_builtin_policy", fmt.Sprintf("Permission mode dontAsk allows readonly %s actions inside the sandbox.", label)
		}
		return "deny", "runtime_permission_mode", fmt.Sprintf("Permission denied: PermissionModeDontAsk blocks mutating %s actions. Use --permission-mode default or acceptEdits to enable writes.", label)
	case ModePlan:
		if readonly {
			return "allow", "runtime_builtin_policy", fmt.Sprintf("Permission mode plan allows readonly %s actions for planning.", label)
		}
		return "deny", "runtime_permission_mode", fmt.Sprintf("Permission mode plan is read-only and blocks mutating %s actions.", label)
	case ModeBypassPermissions:
		return "allow", "runtime_builtin_policy", fmt.Sprintf("Permission mode bypassPermissions allows %s actions within the sandbox without extra approval.", label)
	default:
		return "allow", "runtime_builtin_policy", fmt.Sprintf("Permission mode allows %s actions within the sandbox.", label)
	}
}
