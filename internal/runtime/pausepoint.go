package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type PausePointKind string

const (
	PausePointKindApprovalGate        PausePointKind = "approval_gate"
	PausePointKindWorkflowNode        PausePointKind = "workflow_node"
	PausePointKindVerifierRemediation PausePointKind = "verifier_remediation"
	PausePointKindUserInput           PausePointKind = "user_input" // C-3: Critic asks user
)

type PausePoint struct {
	ID           string
	Kind         PausePointKind
	OriginRunID  string
	OriginTaskID string
	AvatarID     string
	Phase        string
	NodeID       string
	NodeRole     string
	NodeTitle    string
	StatusReason string
	DependsOn    []string
	ActionID     string
	ApprovalKey  string
	Tool         string
	Operation    string
	Digest       string
}

func NewPausePoint(kind PausePointKind, originRunID string, originTaskID string, nodeID string, actionID string, approvalKey string, toolName string, operation string, digest string) PausePoint {
	point := PausePoint{
		Kind:         normalizePausePointKind(kind),
		OriginRunID:  strings.TrimSpace(originRunID),
		OriginTaskID: strings.TrimSpace(originTaskID),
		NodeID:       strings.TrimSpace(nodeID),
		ActionID:     strings.TrimSpace(actionID),
		ApprovalKey:  strings.TrimSpace(approvalKey),
		Tool:         strings.TrimSpace(toolName),
		Operation:    strings.TrimSpace(operation),
		Digest:       strings.TrimSpace(digest),
	}
	point.ID = PausePointID(point.Kind, point.OriginRunID, point.OriginTaskID, point.NodeID, point.ActionID, point.ApprovalKey, point.Tool, point.Operation, point.Digest)
	return point
}

func PausePointID(kind PausePointKind, originRunID string, originTaskID string, nodeID string, actionID string, approvalKey string, toolName string, operation string, digest string) string {
	parts := []string{
		string(normalizePausePointKind(kind)),
		strings.TrimSpace(originRunID),
		strings.TrimSpace(originTaskID),
		strings.TrimSpace(nodeID),
		strings.TrimSpace(actionID),
		strings.TrimSpace(approvalKey),
		strings.TrimSpace(toolName),
		strings.TrimSpace(operation),
		strings.TrimSpace(digest),
	}
	return hashPausePointParts(parts)
}

func (p PausePoint) Payload() map[string]any {
	payload := map[string]any{
		"pause_point_id":   strings.TrimSpace(p.ID),
		"pause_point_kind": string(normalizePausePointKind(p.Kind)),
		"origin_run_id":    strings.TrimSpace(p.OriginRunID),
		"origin_task_id":   strings.TrimSpace(p.OriginTaskID),
		"node_id":          strings.TrimSpace(p.NodeID),
		"action_id":        strings.TrimSpace(p.ActionID),
		"tool":             strings.TrimSpace(p.Tool),
		"operation":        strings.TrimSpace(p.Operation),
		"digest":           strings.TrimSpace(p.Digest),
	}
	if strings.TrimSpace(p.AvatarID) != "" {
		payload["avatar_id"] = strings.TrimSpace(p.AvatarID)
	}
	if strings.TrimSpace(p.NodeRole) != "" {
		payload["assigned_role"] = strings.TrimSpace(p.NodeRole)
	}
	if strings.TrimSpace(p.NodeTitle) != "" {
		payload["title"] = strings.TrimSpace(p.NodeTitle)
	}
	if strings.TrimSpace(p.StatusReason) != "" {
		payload["status_reason"] = strings.TrimSpace(p.StatusReason)
	}
	if len(p.DependsOn) > 0 {
		payload["depends_on"] = append([]string(nil), p.DependsOn...)
	}
	if strings.TrimSpace(p.Phase) != "" {
		payload["phase"] = strings.TrimSpace(p.Phase)
	}
	if strings.TrimSpace(p.ApprovalKey) != "" {
		payload["approval_key"] = strings.TrimSpace(p.ApprovalKey)
	}
	return payload
}

func normalizePausePointKind(kind PausePointKind) PausePointKind {
	switch kind {
	case PausePointKindWorkflowNode, PausePointKindVerifierRemediation, PausePointKindUserInput:
		return kind
	default:
		return PausePointKindApprovalGate
	}
}

func approvalGatePausePointID(originRunID string, originTaskID string, actionID string, approvalKey string, toolName string, operation string, digest string) string {
	parts := []string{
		strings.TrimSpace(originRunID),
		strings.TrimSpace(originTaskID),
		strings.TrimSpace(actionID),
		strings.TrimSpace(approvalKey),
		strings.TrimSpace(toolName),
		strings.TrimSpace(operation),
		strings.TrimSpace(digest),
	}
	return hashPausePointParts(parts)
}

func fallbackApprovalPausePointID(approvalKey string, toolName string, operation string) string {
	parts := []string{
		strings.TrimSpace(approvalKey),
		strings.TrimSpace(toolName),
		strings.TrimSpace(operation),
	}
	return hashPausePointParts(parts)
}

func hashPausePointParts(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "pause-" + hex.EncodeToString(sum[:8])
}
