package runtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/tools"
)

type PendingApprovalRequest struct {
	ApprovalKey     string                       `json:"approval_key"`
	Tool            string                       `json:"tool"`
	Operation       string                       `json:"operation"`
	PermissionMode  string                       `json:"permission_mode,omitempty"`
	SessionID       string                       `json:"session_id,omitempty"`
	RequestedAt     time.Time                    `json:"requested_at"`
	Continuation    *PendingApprovalContinuation `json:"continuation,omitempty"`
	SuspendedAction *SuspendedActionFrame        `json:"suspended_action,omitempty"`
	Workflow        *PendingApprovalWorkflow     `json:"workflow,omitempty"`
	Shell           *PendingApprovalShellInput   `json:"shell,omitempty"`
	Write           *PendingApprovalWriteInput   `json:"write,omitempty"`
	Patch           *PendingApprovalPatchInput   `json:"patch,omitempty"`
	HMAC            string                       `json:"hmac,omitempty"`
}

type PendingApprovalContinuation struct {
	RunID                   string                `json:"run_id,omitempty"`
	TaskID                  string                `json:"task_id,omitempty"`
	AvatarID                string                `json:"avatar_id,omitempty"`
	Phase                   string                `json:"phase,omitempty"`
	TranscriptPath          string                `json:"transcript_path,omitempty"`
	OriginTranscriptPath    string                `json:"origin_transcript_path,omitempty"`
	PausePointID            string                `json:"pause_point_id,omitempty"`
	PausePointKind          string                `json:"pause_point_kind,omitempty"`
	RequestedActionID       string                `json:"requested_action_id,omitempty"`
	RequestedEventID        string                `json:"requested_event_id,omitempty"`
	BoundaryEventID         string                `json:"boundary_event_id,omitempty"`
	BoundaryType            string                `json:"boundary_type,omitempty"`
	BoundaryStatus          string                `json:"boundary_status,omitempty"`
	BoundarySummary         string                `json:"boundary_summary,omitempty"`
	ContinuationRunID       string                `json:"continuation_run_id,omitempty"`
	ContinuationTaskID      string                `json:"continuation_task_id,omitempty"`
	ContinuationEventID     string                `json:"continuation_event_id,omitempty"`
	ContinuationStatus      string                `json:"continuation_status,omitempty"`
	ContinuationSummary     string                `json:"continuation_summary,omitempty"`
	ContinuationTranscript  string                `json:"continuation_transcript,omitempty"`
	ContinuationRequestedAt time.Time             `json:"continuation_requested_at,omitempty"`
	ResumeAttemptID         string                `json:"resume_attempt_id,omitempty"`
	ResumeAttemptStatus     string                `json:"resume_attempt_status,omitempty"`
	ResumeAttempts          []ResumeAttemptRecord `json:"resume_attempts,omitempty"`
	RequestedAt             time.Time             `json:"requested_at,omitempty"`
}

type ResumeAttemptRecord struct {
	AttemptID              string    `json:"attempt_id,omitempty"`
	PausePointID           string    `json:"pause_point_id,omitempty"`
	Status                 string    `json:"status,omitempty"`
	ContinuationRunID      string    `json:"continuation_run_id,omitempty"`
	ContinuationTaskID     string    `json:"continuation_task_id,omitempty"`
	ContinuationTranscript string    `json:"continuation_transcript,omitempty"`
	Summary                string    `json:"summary,omitempty"`
	StartedAt              time.Time `json:"started_at,omitempty"`
	CompletedAt            time.Time `json:"completed_at,omitempty"`
}

type PendingApprovalWorkflow struct {
	PlanSummary          string                        `json:"plan_summary,omitempty"`
	PausePointID         string                        `json:"pause_point_id,omitempty"`
	PausePointKind       string                        `json:"pause_point_kind,omitempty"`
	PausePointDigest     string                        `json:"pause_point_digest,omitempty"`
	BlockedNodeID        string                        `json:"blocked_node_id,omitempty"`
	BlockedNodeRole      string                        `json:"blocked_node_role,omitempty"`
	BlockedNodeTitle     string                        `json:"blocked_node_title,omitempty"`
	BlockedNodeDependsOn []string                      `json:"blocked_node_depends_on,omitempty"`
	CompletedNodeIDs     []string                      `json:"completed_node_ids,omitempty"`
	RemainingNodeIDs     []string                      `json:"remaining_node_ids,omitempty"`
	CompletedNodes       []PendingApprovalWorkflowNode `json:"completed_nodes,omitempty"`
	RemainingNodes       []PendingApprovalWorkflowNode `json:"remaining_nodes,omitempty"`
	CheckpointEvent      string                        `json:"checkpoint_event,omitempty"`
	// S4.2: full DAG snapshot + retry counts so resume can re-enter the scheduler.
	WorkflowSnapshot *WorkflowStateSnapshot `json:"workflow_snapshot,omitempty"`
	NodeRetryCounts  map[string]int         `json:"node_retry_counts,omitempty"`
	TaskInput        string                 `json:"task_input,omitempty"`
	ReadSummary      string                 `json:"read_summary,omitempty"`
}

type PendingApprovalWorkflowNode struct {
	ID        string   `json:"id,omitempty"`
	Role      string   `json:"role,omitempty"`
	Title     string   `json:"title,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
}

type PendingApprovalShellInput struct {
	Command         []string `json:"command,omitempty"`
	WorkingDir      string   `json:"working_dir,omitempty"`
	TimeoutMillis   int      `json:"timeout_ms,omitempty"`
	ProposalID      string   `json:"proposal_id,omitempty"`
	Intent          string   `json:"intent,omitempty"`
	ExpectedTargets []string `json:"expected_targets,omitempty"`
}

type PendingApprovalWriteInput struct {
	Path            string   `json:"path,omitempty"`
	Content         string   `json:"content,omitempty"`
	WorkingDir      string   `json:"working_dir,omitempty"`
	Overwrite       bool     `json:"overwrite,omitempty"`
	ProposalID      string   `json:"proposal_id,omitempty"`
	Intent          string   `json:"intent,omitempty"`
	ExpectedTargets []string `json:"expected_targets,omitempty"`
}

type PendingApprovalPatchInput struct {
	Path            string   `json:"path,omitempty"`
	Old             string   `json:"old,omitempty"`
	New             string   `json:"new,omitempty"`
	WorkingDir      string   `json:"working_dir,omitempty"`
	ReplaceAll      bool     `json:"replace_all,omitempty"`
	ProposalID      string   `json:"proposal_id,omitempty"`
	Intent          string   `json:"intent,omitempty"`
	ExpectedTargets []string `json:"expected_targets,omitempty"`
}

func PendingApprovalRequestPath(memoryDir string, approvalKey string) string {
	trimmedMemoryDir := strings.TrimSpace(memoryDir)
	trimmedApprovalKey := strings.TrimSpace(approvalKey)
	if trimmedMemoryDir == "" || trimmedApprovalKey == "" {
		return ""
	}
	return filepath.Join(trimmedMemoryDir, "approvals", fmt.Sprintf("%s.json", sanitizeArtifactName(trimmedApprovalKey)))
}

func LoadPendingApprovalRequest(memoryDir string, approvalKey string) (PendingApprovalRequest, error) {
	path := PendingApprovalRequestPath(memoryDir, approvalKey)
	if path == "" {
		return PendingApprovalRequest{}, fmt.Errorf("pending approval request path unavailable")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return PendingApprovalRequest{}, fmt.Errorf("read pending approval request: %w", err)
	}
	var request PendingApprovalRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return PendingApprovalRequest{}, fmt.Errorf("decode pending approval request: %w", err)
	}
	// Verify HMAC integrity if the request carries an HMAC tag.
	if strings.TrimSpace(request.HMAC) != "" {
		if err := request.VerifyHMAC(); err != nil {
			return PendingApprovalRequest{}, fmt.Errorf("pending approval request HMAC verification failed (possible tampering or replay): %w", err)
		}
	}
	return request, nil
}

// approvalRequestHMACKey derives the HMAC key from sessionID and a fixed salt.
func approvalRequestHMACKey(sessionID string) string {
	const salt = "avatars-approval-request-v1"
	return sessionID + ":" + salt
}

// ComputeHMAC computes an HMAC-SHA256 over the request payload and stores it
// in the HMAC field. The HMAC covers the JSON representation of the request
// with the HMAC field cleared (to avoid self-referencing).
func (r *PendingApprovalRequest) ComputeHMAC() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return fmt.Errorf("cannot compute HMAC: approval request has no session ID")
	}
	// Save and clear HMAC so the digest covers only the payload.
	saved := r.HMAC
	r.HMAC = ""
	payload, err := json.Marshal(r)
	r.HMAC = saved
	if err != nil {
		return fmt.Errorf("marshal approval request for HMAC computation: %w", err)
	}
	key := approvalRequestHMACKey(r.SessionID)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(payload)
	r.HMAC = hex.EncodeToString(mac.Sum(nil))
	return nil
}

// VerifyHMAC recomputes the HMAC over the payload (HMAC field cleared) and
// compares it against the stored HMAC. Returns an error if they differ.
func (r PendingApprovalRequest) VerifyHMAC() error {
	stored := strings.TrimSpace(r.HMAC)
	if stored == "" {
		return fmt.Errorf("approval request has no HMAC tag")
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return fmt.Errorf("cannot verify HMAC: approval request has no session ID")
	}
	// Compute expected HMAC over the payload with HMAC cleared.
	cleaned := r
	cleaned.HMAC = ""
	payload, err := json.Marshal(cleaned)
	if err != nil {
		return fmt.Errorf("marshal approval request for HMAC verification: %w", err)
	}
	key := approvalRequestHMACKey(r.SessionID)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(stored)) {
		return fmt.Errorf("HMAC mismatch: stored %s does not match computed %s", stored[:16]+"...", expected[:16]+"...")
	}
	return nil
}

func (r PendingApprovalRequest) ResolveToolCall() (string, string, any, error) {
	toolName := strings.TrimSpace(r.Tool)
	operation := strings.TrimSpace(r.Operation)
	if toolName == "" || operation == "" {
		return "", "", nil, fmt.Errorf("pending approval request is missing tool metadata")
	}
	switch toolName {
	case "shell":
		if r.Shell == nil {
			return "", "", nil, fmt.Errorf("pending approval request for shell is missing shell input")
		}
		return toolName, operation, tools.ShellInput{
			Command:         append([]string(nil), r.Shell.Command...),
			WorkingDir:      r.Shell.WorkingDir,
			TimeoutMillis:   r.Shell.TimeoutMillis,
			ProposalID:      r.Shell.ProposalID,
			Intent:          r.Shell.Intent,
			ExpectedTargets: append([]string(nil), r.Shell.ExpectedTargets...),
		}, nil
	case "write":
		if r.Write == nil {
			return "", "", nil, fmt.Errorf("pending approval request for write is missing write input")
		}
		return toolName, operation, tools.WriteInput{
			Path:            r.Write.Path,
			Content:         r.Write.Content,
			WorkingDir:      r.Write.WorkingDir,
			Overwrite:       r.Write.Overwrite,
			ProposalID:      r.Write.ProposalID,
			Intent:          r.Write.Intent,
			ExpectedTargets: append([]string(nil), r.Write.ExpectedTargets...),
		}, nil
	case "patch":
		if r.Patch == nil {
			return "", "", nil, fmt.Errorf("pending approval request for patch is missing patch input")
		}
		return toolName, operation, tools.PatchInput{
			Path:            r.Patch.Path,
			Old:             r.Patch.Old,
			New:             r.Patch.New,
			WorkingDir:      r.Patch.WorkingDir,
			ReplaceAll:      r.Patch.ReplaceAll,
			ProposalID:      r.Patch.ProposalID,
			Intent:          r.Patch.Intent,
			ExpectedTargets: append([]string(nil), r.Patch.ExpectedTargets...),
		}, nil
	default:
		return "", "", nil, fmt.Errorf("pending approval replay does not support tool %s", toolName)
	}
}

func (r PendingApprovalRequest) ValidateSuspendedAction(toolName string, operation string, input any) error {
	if r.SuspendedAction == nil {
		return nil
	}
	return r.SuspendedAction.ValidateResolvedToolCall(toolName, operation, input)
}

func (r PendingApprovalRequest) PausePointID() string {
	if r.Continuation != nil && strings.TrimSpace(r.Continuation.PausePointID) != "" {
		return strings.TrimSpace(r.Continuation.PausePointID)
	}
	if r.SuspendedAction != nil {
		return suspendedActionPausePointID(*r.SuspendedAction)
	}
	return fallbackApprovalPausePointID(r.ApprovalKey, r.Tool, r.Operation)
}

func buildPendingApprovalRequest(toolName string, operation string, input any, permissionMode PermissionMode, approvalKey string, requestedAt time.Time) (PendingApprovalRequest, bool) {
	request := PendingApprovalRequest{
		ApprovalKey:    strings.TrimSpace(approvalKey),
		Tool:           strings.TrimSpace(toolName),
		Operation:      strings.TrimSpace(operation),
		PermissionMode: string(normalizePermissionMode(permissionMode)),
		RequestedAt:    requestedAt,
	}
	switch value := input.(type) {
	case tools.ShellInput:
		request.Shell = &PendingApprovalShellInput{
			Command:         append([]string(nil), value.Command...),
			WorkingDir:      value.WorkingDir,
			TimeoutMillis:   value.TimeoutMillis,
			ProposalID:      value.ProposalID,
			Intent:          value.Intent,
			ExpectedTargets: append([]string(nil), value.ExpectedTargets...),
		}
		return request, true
	case *tools.ShellInput:
		if value == nil {
			return PendingApprovalRequest{}, false
		}
		request.Shell = &PendingApprovalShellInput{
			Command:         append([]string(nil), value.Command...),
			WorkingDir:      value.WorkingDir,
			TimeoutMillis:   value.TimeoutMillis,
			ProposalID:      value.ProposalID,
			Intent:          value.Intent,
			ExpectedTargets: append([]string(nil), value.ExpectedTargets...),
		}
		return request, true
	case tools.WriteInput:
		request.Write = &PendingApprovalWriteInput{
			Path:            value.Path,
			Content:         value.Content,
			WorkingDir:      value.WorkingDir,
			Overwrite:       value.Overwrite,
			ProposalID:      value.ProposalID,
			Intent:          value.Intent,
			ExpectedTargets: append([]string(nil), value.ExpectedTargets...),
		}
		return request, true
	case *tools.WriteInput:
		if value == nil {
			return PendingApprovalRequest{}, false
		}
		request.Write = &PendingApprovalWriteInput{
			Path:            value.Path,
			Content:         value.Content,
			WorkingDir:      value.WorkingDir,
			Overwrite:       value.Overwrite,
			ProposalID:      value.ProposalID,
			Intent:          value.Intent,
			ExpectedTargets: append([]string(nil), value.ExpectedTargets...),
		}
		return request, true
	case tools.PatchInput:
		request.Patch = &PendingApprovalPatchInput{
			Path:            value.Path,
			Old:             value.Old,
			New:             value.New,
			WorkingDir:      value.WorkingDir,
			ReplaceAll:      value.ReplaceAll,
			ProposalID:      value.ProposalID,
			Intent:          value.Intent,
			ExpectedTargets: append([]string(nil), value.ExpectedTargets...),
		}
		return request, true
	case *tools.PatchInput:
		if value == nil {
			return PendingApprovalRequest{}, false
		}
		request.Patch = &PendingApprovalPatchInput{
			Path:            value.Path,
			Old:             value.Old,
			New:             value.New,
			WorkingDir:      value.WorkingDir,
			ReplaceAll:      value.ReplaceAll,
			ProposalID:      value.ProposalID,
			Intent:          value.Intent,
			ExpectedTargets: append([]string(nil), value.ExpectedTargets...),
		}
		return request, true
	default:
		return PendingApprovalRequest{}, false
	}
}
