package runtime

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"avatars/internal/planner"
)

type WorkflowNodeStatus string

const (
	WorkflowNodeStatusPending   WorkflowNodeStatus = "pending"
	WorkflowNodeStatusActive    WorkflowNodeStatus = "active"
	WorkflowNodeStatusCompleted WorkflowNodeStatus = "completed"
	WorkflowNodeStatusFailed    WorkflowNodeStatus = "failed"
	WorkflowNodeStatusSkipped   WorkflowNodeStatus = "skipped"
)

type WorkflowNodeRuntime struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	AssignedRole string             `json:"assigned_role"`
	DependsOn    []string           `json:"depends_on"`
	Status       WorkflowNodeStatus `json:"status"`
	StatusReason string             `json:"status_reason,omitempty"`
	ArtifactIDs  []string           `json:"artifact_ids,omitempty"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

type WorkflowState struct {
	mu    sync.RWMutex // PARTIAL-6: RWMutex for concurrent reads
	nodes map[string]WorkflowNodeRuntime
	order []string
}

func NewWorkflowState(plan planner.Plan) *WorkflowState {
	state := &WorkflowState{
		nodes: make(map[string]WorkflowNodeRuntime, len(plan.Nodes)),
		order: make([]string, 0, len(plan.Nodes)),
	}
	now := time.Now().UTC()
	for _, node := range plan.Nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			continue
		}
		state.order = append(state.order, id)
		state.nodes[id] = WorkflowNodeRuntime{
			ID:           id,
			Title:        strings.TrimSpace(node.Title),
			AssignedRole: strings.TrimSpace(node.AssignedRole),
			DependsOn:    cleanWorkflowIDs(node.DependsOn),
			Status:       WorkflowNodeStatusPending,
			UpdatedAt:    now,
		}
	}
	return state
}

func (s *WorkflowState) Nodes() []WorkflowNodeRuntime {
	if s == nil {
		return nil
	}
	nodes := make([]WorkflowNodeRuntime, 0, len(s.order))
	for _, id := range s.order {
		nodes = append(nodes, s.Node(id))
	}
	return nodes
}

func (s *WorkflowState) Node(nodeID string) WorkflowNodeRuntime {
	if s == nil {
		return WorkflowNodeRuntime{}
	}
	s.mu.RLock()
	n := s.nodes[strings.TrimSpace(nodeID)]
	s.mu.RUnlock()
	return n
}

func (s *WorkflowState) ReadyNodes() []WorkflowNodeRuntime {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ready := make([]WorkflowNodeRuntime, 0, len(s.order))
	for _, id := range s.order {
		node := s.nodes[id]
		if node.Status != WorkflowNodeStatusPending {
			continue
		}
		if s.dependenciesSatisfiedLocked(node) {
			ready = append(ready, node)
		}
	}
	return ready
}

func (s *WorkflowState) PendingNodes() []WorkflowNodeRuntime {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	pending := make([]WorkflowNodeRuntime, 0, len(s.order))
	for _, id := range s.order {
		node := s.nodes[id]
		if node.Status == WorkflowNodeStatusPending {
			pending = append(pending, node)
		}
	}
	return pending
}

func (s *WorkflowState) CompletedNodeIDsBefore(nodeID string) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	target := strings.TrimSpace(nodeID)
	completed := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if id == target {
			break
		}
		node := s.nodes[id]
		if node.Status == WorkflowNodeStatusCompleted || node.Status == WorkflowNodeStatusSkipped {
			completed = append(completed, id)
		}
	}
	return completed
}

func (s *WorkflowState) RemainingNodeIDsAfter(nodeID string) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	target := strings.TrimSpace(nodeID)
	seenTarget := false
	remaining := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if id == target {
			seenTarget = true
			continue
		}
		if !seenTarget {
			continue
		}
		node := s.nodes[id]
		if node.Status != WorkflowNodeStatusCompleted && node.Status != WorkflowNodeStatusSkipped {
			remaining = append(remaining, id)
		}
	}
	return remaining
}

func (s *WorkflowState) PausePointForNode(nodeID string) (PausePoint, error) {
	return s.PausePointForNodeInRun("", "", nodeID)
}

func (s *WorkflowState) PausePointForNodeInRun(runID string, taskID string, nodeID string) (PausePoint, error) {
	if s == nil {
		return PausePoint{}, fmt.Errorf("workflow state is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := strings.TrimSpace(nodeID)
	node, ok := s.nodes[id]
	if !ok {
		return PausePoint{}, fmt.Errorf("workflow node %s not found", id)
	}
	digest := workflowNodePauseDigest(node)
	point := NewPausePoint(PausePointKindWorkflowNode, runID, taskID, node.ID, node.ID, "", "workflow", "node_pause", digest)
	point.NodeRole = strings.TrimSpace(node.AssignedRole)
	point.NodeTitle = strings.TrimSpace(node.Title)
	point.StatusReason = strings.TrimSpace(node.StatusReason)
	point.DependsOn = append([]string(nil), node.DependsOn...)
	return point, nil
}

func (s *WorkflowState) Activate(nodeID string) error {
	return s.transition(nodeID, WorkflowNodeStatusActive, "", nil)
}

func (s *WorkflowState) Complete(nodeID string, artifactIDs []string) error {
	return s.transition(nodeID, WorkflowNodeStatusCompleted, "", artifactIDs)
}

func (s *WorkflowState) Fail(nodeID string, err error) error {
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	return s.transition(nodeID, WorkflowNodeStatusFailed, reason, nil)
}

func (s *WorkflowState) Skip(nodeID string, reason string) error {
	return s.transition(nodeID, WorkflowNodeStatusSkipped, reason, nil)
}

// AddNode injects a new node into the workflow DAG at runtime.
// D-1: Enables Critic to dynamically add Researcher/Planner nodes mid-run.
// The node is appended to the order slice and nodes map; the scheduler's
// ReadyNodes() will discover it on the next loop iteration automatically.
func (s *WorkflowState) AddNode(node WorkflowNodeRuntime) error {
	if s == nil {
		return fmt.Errorf("workflow state is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(node.ID)
	if id == "" {
		return fmt.Errorf("node ID is required")
	}
	if _, exists := s.nodes[id]; exists {
		return fmt.Errorf("node %s already exists", id)
	}
	node.Status = WorkflowNodeStatusPending
	node.DependsOn = cleanWorkflowIDs(node.DependsOn)
	node.UpdatedAt = time.Now().UTC()
	s.nodes[id] = node
	s.order = append(s.order, id)
	return nil
}

func (s *WorkflowState) transition(nodeID string, status WorkflowNodeStatus, reason string, artifactIDs []string) error {
	if s == nil {
		return fmt.Errorf("workflow state is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(nodeID)
	node, ok := s.nodes[id]
	if !ok {
		return fmt.Errorf("workflow node %s not found", id)
	}
	if status == WorkflowNodeStatusActive && !s.dependenciesSatisfiedLocked(node) {
		return fmt.Errorf("workflow node %s dependencies are not satisfied", id)
	}
	node.Status = status
	node.StatusReason = strings.TrimSpace(reason)
	node.ArtifactIDs = cleanWorkflowIDs(artifactIDs)
	node.UpdatedAt = time.Now().UTC()
	s.nodes[id] = node
	return nil
}

// dependenciesSatisfiedLocked checks if dependencies are met. Must be
// called with s.mu held (used inside transition).
func (s *WorkflowState) dependenciesSatisfiedLocked(node WorkflowNodeRuntime) bool {
	for _, dependencyID := range node.DependsOn {
		dependency, ok := s.nodes[dependencyID]
		if !ok {
			return false
		}
		if dependency.Status != WorkflowNodeStatusCompleted && dependency.Status != WorkflowNodeStatusSkipped {
			return false
		}
	}
	return true
}

// HasCycle detects directed cycles in the workflow DAG. Returns true and
// a cycle description if a cycle is found. Call during plan construction
// or before RunReadyNodes to prevent infinite scheduling loops.
func (s *WorkflowState) HasCycle() (bool, string) {
	if s == nil {
		return false, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	const (
		white = 0 // unvisited
		gray  = 1 // in current DFS path
		black = 2 // fully explored
	)
	color := make(map[string]int, len(s.nodes))
	path := make([]string, 0, len(s.nodes))

	var dfs func(nodeID string) bool
	dfs = func(nodeID string) bool {
		c := color[nodeID]
		if c == gray {
			return true // back edge = cycle detected
		}
			if c == black {
			return false
		}
		color[nodeID] = gray
		path = append(path, nodeID)
		node, ok := s.nodes[nodeID]
		if ok {
			for _, depID := range node.DependsOn {
				if dfs(depID) {
					return true
				}
			}
		}
		path = path[:len(path)-1]
		color[nodeID] = black
		return false
	}

	for _, id := range s.order {
		if dfs(id) {
			return true, fmt.Sprintf("cycle detected in workflow DAG involving node %s", id)
		}
	}
	return false, ""
}

// dependenciesSatisfied acquires the lock for external callers.
func (s *WorkflowState) dependenciesSatisfied(node WorkflowNodeRuntime) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dependenciesSatisfiedLocked(node)
}

func cleanWorkflowIDs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		duplicate := false
		for _, existing := range cleaned {
			if existing == trimmed {
				duplicate = true
				break
			}
		}
		if !duplicate {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}

func workflowNodePauseDigest(node WorkflowNodeRuntime) string {
	return strings.TrimPrefix(hashPausePointParts([]string{
		strings.TrimSpace(node.ID),
		strings.TrimSpace(node.Title),
		strings.TrimSpace(node.AssignedRole),
		strings.Join(cleanWorkflowIDs(node.DependsOn), ","),
	}), "pause-")
}

func buildWorkflowCheckpoint(plan planner.Plan, blockedRole string, checkpointEvent string) *PendingApprovalWorkflow {
	return buildWorkflowCheckpointFromState(plan, nil, "", "", blockedRole, checkpointEvent)
}

func buildWorkflowCheckpointFromState(plan planner.Plan, state *WorkflowState, runID string, taskID string, blockedRole string, checkpointEvent string) *PendingApprovalWorkflow {
	trimmedRole := strings.TrimSpace(blockedRole)
	if trimmedRole == "" {
		return nil
	}
	checkpoint := &PendingApprovalWorkflow{
		PlanSummary:     strings.TrimSpace(plan.Summary),
		CheckpointEvent: strings.TrimSpace(checkpointEvent),
	}
	foundBlocked := false
	for _, node := range plan.Nodes {
		nodeID := strings.TrimSpace(node.ID)
		if nodeID == "" {
			continue
		}
		if foundBlocked {
			checkpoint.RemainingNodeIDs = append(checkpoint.RemainingNodeIDs, nodeID)
			checkpoint.RemainingNodes = append(checkpoint.RemainingNodes, pendingApprovalWorkflowNode(node))
			continue
		}
		if strings.EqualFold(strings.TrimSpace(node.AssignedRole), trimmedRole) {
			checkpoint.BlockedNodeID = nodeID
			checkpoint.BlockedNodeRole = strings.TrimSpace(node.AssignedRole)
			checkpoint.BlockedNodeTitle = strings.TrimSpace(node.Title)
			checkpoint.BlockedNodeDependsOn = cleanWorkflowIDs(node.DependsOn)
			if state != nil {
				if point, err := state.PausePointForNodeInRun(runID, taskID, nodeID); err == nil {
					checkpoint.PausePointID = point.ID
					checkpoint.PausePointKind = string(point.Kind)
					checkpoint.PausePointDigest = point.Digest
				}
			}
			foundBlocked = true
			continue
		}
		checkpoint.CompletedNodeIDs = append(checkpoint.CompletedNodeIDs, nodeID)
		checkpoint.CompletedNodes = append(checkpoint.CompletedNodes, pendingApprovalWorkflowNode(node))
	}
	if !foundBlocked {
		return nil
	}
	return checkpoint
}

// enrichWorkflowCheckpoint attaches live DAG snapshot + retry counts (S4.2).
// Call after buildWorkflowCheckpointFromState when engine state is available.
func enrichWorkflowCheckpoint(checkpoint *PendingApprovalWorkflow, state *WorkflowState, nodeRetryCounts map[string]int, taskInput string, readSummary string) *PendingApprovalWorkflow {
	if checkpoint == nil {
		return nil
	}
	if state != nil {
		snap := state.Snapshot()
		checkpoint.WorkflowSnapshot = &snap
	}
	if len(nodeRetryCounts) > 0 {
		checkpoint.NodeRetryCounts = make(map[string]int, len(nodeRetryCounts))
		for k, v := range nodeRetryCounts {
			checkpoint.NodeRetryCounts[k] = v
		}
	}
	checkpoint.TaskInput = strings.TrimSpace(taskInput)
	checkpoint.ReadSummary = strings.TrimSpace(readSummary)
	return checkpoint
}

func pendingApprovalWorkflowNode(node planner.WorkflowNode) PendingApprovalWorkflowNode {
	return PendingApprovalWorkflowNode{
		ID:        strings.TrimSpace(node.ID),
		Role:      strings.TrimSpace(node.AssignedRole),
		Title:     strings.TrimSpace(node.Title),
		DependsOn: cleanWorkflowIDs(node.DependsOn),
	}
}

// WorkflowStateSnapshot is a serializable representation of WorkflowState.
// R1: Persisted to transcript on suspension, restored on resume so the
// scheduler can continue from where it left off.
type WorkflowStateSnapshot struct {
	Nodes []WorkflowNodeRuntime `json:"nodes"`
	Order []string              `json:"order"`
}

// Snapshot returns a serializable copy of the WorkflowState. R1.
func (s *WorkflowState) Snapshot() WorkflowStateSnapshot {
	if s == nil {
		return WorkflowStateSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := make([]WorkflowNodeRuntime, 0, len(s.nodes))
	for _, id := range s.order {
		nodes = append(nodes, s.nodes[id])
	}
	return WorkflowStateSnapshot{Nodes: nodes, Order: append([]string{}, s.order...)}
}

// RestoreWorkflowState reconstructs a WorkflowState from a snapshot. R1.
func RestoreWorkflowState(snapshot WorkflowStateSnapshot) *WorkflowState {
	state := &WorkflowState{
		nodes: make(map[string]WorkflowNodeRuntime, len(snapshot.Nodes)),
		order: make([]string, 0, len(snapshot.Order)),
	}
	for _, node := range snapshot.Nodes {
		state.nodes[node.ID] = node
	}
	state.order = append(state.order, snapshot.Order...)
	return state
}
