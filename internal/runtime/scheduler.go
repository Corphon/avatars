package runtime

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"avatars/internal/llm"
)

func runtimeNumCPU() int {
	return runtime.NumCPU()
}

type NodeExecutor interface {
	ExecuteNode(node WorkflowNodeRuntime) (SchedulerNodeResult, error)
}

type NodeExecutorFunc func(node WorkflowNodeRuntime) (SchedulerNodeResult, error)

func (f NodeExecutorFunc) ExecuteNode(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
	return f(node)
}

type SchedulerNodeResult struct {
	ArtifactIDs []string
	Skip        bool
	Reason      string
	PausePoint  PausePoint
}

type SchedulerResult struct {
	ActivatedNodeIDs []string
	CompletedNodeIDs []string
	SkippedNodeIDs   []string
	FailedNodeID     string
	FailedPausePoint PausePoint
	PausedNodeID     string
	PausePoint       PausePoint
	ResumedFrom      string
}

// T1: Node timeout now reads from global timeout config. Falls back to 20min.
func nodeTimeoutDefault() time.Duration {
	return llm.TimeoutConfigOrDefault().NodeTimeout
}

// A2: Default max concurrent avatars when no config override is set.
var maxParallelAvatars = 0 // 0 = use runtime.NumCPU()

// SetMaxParallelAvatars overrides the global concurrency cap. Call once
// during startup from the config loader. A2.
func SetMaxParallelAvatars(n int) {
	if n > 0 {
		maxParallelAvatars = n
	}
}

func getMaxParallel() int {
	if maxParallelAvatars > 0 {
		return maxParallelAvatars
	}
	// Default: number of logical CPUs, capped at 8 to prevent OOM
	// when LLM generates many parallel avatars.
	n := runtimeNumCPU()
	if n > 8 {
		n = 8
	}
	if n < 2 {
		n = 2
	}
	return n
}

type Scheduler struct {
	State       *WorkflowState
	NodeTimeout time.Duration // per-avatar execution timeout (0 = use global config)
	MaxParallel int           // A2: max concurrent goroutines (0 = auto)
}

func NewScheduler(state *WorkflowState) *Scheduler {
	return &Scheduler{
		State:       state,
		NodeTimeout: nodeTimeoutDefault(),
		MaxParallel: getMaxParallel(),
	}
}

func (s *Scheduler) concurrencyCap() int {
	if s.MaxParallel > 0 {
		return s.MaxParallel
	}
	return getMaxParallel()
}

func (s *Scheduler) timeout() time.Duration {
	if s.NodeTimeout > 0 {
		return s.NodeTimeout
	}
	return nodeTimeoutDefault()
}

func (s *Scheduler) RunReadyNodes(executor NodeExecutor) (SchedulerResult, error) {
	if s == nil || s.State == nil {
		return SchedulerResult{}, fmt.Errorf("workflow state is required")
	}
	if executor == nil {
		return SchedulerResult{}, fmt.Errorf("node executor is required")
	}
	var result SchedulerResult
	for {
		ready := s.State.ReadyNodes()
		if len(ready) == 0 {
			if len(s.State.PendingNodes()) == 0 {
				return result, nil
			}
			// S6.9: Pending nodes blocked by a failed sibling are not a stall —
			// return with FailedNodeID so callers can persist recovery / retry.
			if result.FailedNodeID != "" {
				return result, nil
			}
			return result, fmt.Errorf("workflow stalled with unresolved pending nodes")
		}
		// Run ready nodes concurrently when there are multiple
		// independent nodes. Single nodes skip goroutine overhead.
		if len(ready) == 1 {
			nodeResult, err := s.runNodeWithTimeout(ready[0].ID, executor, true)
			result.merge(nodeResult)
			if err != nil {
				return result, err
			}
			if nodeResult.PausedNodeID != "" {
				return result, nil
			}
		} else {
			var mu sync.Mutex
			var wg sync.WaitGroup
			var paused bool
			// A2: Semaphore to cap concurrent goroutines.
			cap := s.concurrencyCap()
			sem := make(chan struct{}, cap)
			for _, node := range ready {
				wg.Add(1)
				sem <- struct{}{} // acquire slot
				go func(n WorkflowNodeRuntime) {
					defer func() { <-sem }() // release slot (A2)
					defer wg.Done()
					// P0-3d: recover isolation — panic in one avatar
					// must not crash the entire RunReadyNodes.
					defer func() {
						if r := recover(); r != nil {
							mu.Lock()
							defer mu.Unlock()
							recoveryErr := fmt.Errorf("avatar %s panicked: %v", n.ID, r)
							_ = s.State.Fail(n.ID, recoveryErr)
							result.FailedNodeID = n.ID
							result.FailedPausePoint = PausePoint{StatusReason: recoveryErr.Error()}
						}
					}()
					nodeResult, err := s.runNodeWithTimeout(n.ID, executor, true)
					mu.Lock()
					defer mu.Unlock()
					// S6.9: One node timeout/fail must not abort sibling completion.
					// Record FailedNodeID for pause/retry; continue the wave and
					// subsequent ready-sets unlocked by successful siblings.
					if err != nil {
						if result.FailedNodeID == "" {
							result.FailedNodeID = n.ID
							if strings.TrimSpace(nodeResult.FailedPausePoint.ID) == "" &&
								strings.TrimSpace(nodeResult.FailedPausePoint.StatusReason) == "" {
								result.FailedPausePoint = PausePoint{StatusReason: err.Error()}
							}
						}
					}
					if nodeResult.PausedNodeID != "" {
						paused = true
					}
					result.merge(nodeResult)
				}(node)
			}
			wg.Wait()
			if paused {
				return result, nil
			}
		}
	}
}

func (s *Scheduler) ResumeFromPausePoint(point PausePoint, executor NodeExecutor) (SchedulerResult, error) {
	if s == nil || s.State == nil {
		return SchedulerResult{}, fmt.Errorf("workflow state is required")
	}
	if executor == nil {
		return SchedulerResult{}, fmt.Errorf("node executor is required")
	}
	nodeID := strings.TrimSpace(point.NodeID)
	if nodeID == "" {
		return SchedulerResult{}, fmt.Errorf("pause point node id is required")
	}
	result, err := s.runNode(nodeID, executor, s.State.Node(nodeID).Status == WorkflowNodeStatusPending)
	result.ResumedFrom = strings.TrimSpace(point.ID)
	if err != nil || result.PausedNodeID != "" {
		return result, err
	}
	next, err := s.RunReadyNodes(executor)
	result.merge(next)
	if result.ResumedFrom == "" {
		result.ResumedFrom = strings.TrimSpace(point.ID)
	}
	return result, err
}

// runNodeWithTimeout wraps runNode with a per-avatar context timeout.
// P0-3c: If a node exceeds its timeout, it is marked FAIL and the
// scheduler continues with remaining nodes.
// P8-1: Builder-like nodes that already have a green disk scaffold soft-complete
// instead of failing the whole run (cross-lang health check).
func (s *Scheduler) runNodeWithTimeout(nodeID string, executor NodeExecutor, activate bool) (SchedulerResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout())
	defer cancel()

	type nodeOutcome struct {
		result SchedulerResult
		err    error
	}
	done := make(chan nodeOutcome, 1)

	go func() {
		result, err := s.runNode(nodeID, executor, activate)
		done <- nodeOutcome{result, err}
	}()

	select {
	case outcome := <-done:
		return outcome.result, outcome.err
	case <-ctx.Done():
		timeoutErr := fmt.Errorf("avatar node %s timed out after %v", nodeID, s.timeout())
		if builderNodeSoftTimeoutOK(nodeID) {
			_ = s.State.Complete(nodeID, nil)
			return SchedulerResult{
				CompletedNodeIDs: []string{nodeID},
			}, nil
		}
		_ = s.State.Fail(nodeID, timeoutErr)
		return SchedulerResult{
			FailedNodeID:     nodeID,
			FailedPausePoint: PausePoint{StatusReason: timeoutErr.Error()},
		}, timeoutErr
	}
}

// builderNodeSoftTimeoutOK reports whether a timed-out Builder node may soft-complete
// because the project already has sources and passes cross-lang health.
func builderNodeSoftTimeoutOK(nodeID string) bool {
	id := strings.ToLower(strings.TrimSpace(nodeID))
	if !strings.Contains(id, "build") && !strings.Contains(id, "builder") {
		return false
	}
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		return false
	}
	if !diskHasAppSources(wd) {
		return false
	}
	return crossLangHealthCheck(wd) == ""
}

func (s *Scheduler) runNode(nodeID string, executor NodeExecutor, activate bool) (SchedulerResult, error) {
	var result SchedulerResult
	id := strings.TrimSpace(nodeID)
	if activate {
		if err := s.State.Activate(id); err != nil {
			return result, err
		}
		result.ActivatedNodeIDs = append(result.ActivatedNodeIDs, id)
	}
	node := s.State.Node(id)
	execution, err := executor.ExecuteNode(node)
	if err != nil {
		if failErr := s.State.Fail(id, err); failErr != nil {
			return result, failErr
		}
		if point, pointErr := s.State.PausePointForNode(id); pointErr == nil {
			point.StatusReason = strings.TrimSpace(err.Error())
			result.FailedNodeID = id
			result.FailedPausePoint = point
		}
		return result, err
	}
	if strings.TrimSpace(execution.PausePoint.ID) != "" {
		result.PausedNodeID = id
		result.PausePoint = execution.PausePoint
		return result, nil
	}
	if execution.Skip {
		if err := s.State.Skip(id, execution.Reason); err != nil {
			return result, err
		}
		result.SkippedNodeIDs = append(result.SkippedNodeIDs, id)
		return result, nil
	}
	if err := s.State.Complete(id, execution.ArtifactIDs); err != nil {
		return result, err
	}
	result.CompletedNodeIDs = append(result.CompletedNodeIDs, id)
	return result, nil
}

func (r *SchedulerResult) merge(other SchedulerResult) {
	if r == nil {
		return
	}
	r.ActivatedNodeIDs = append(r.ActivatedNodeIDs, other.ActivatedNodeIDs...)
	r.CompletedNodeIDs = append(r.CompletedNodeIDs, other.CompletedNodeIDs...)
	r.SkippedNodeIDs = append(r.SkippedNodeIDs, other.SkippedNodeIDs...)
	if other.PausedNodeID != "" {
		r.PausedNodeID = other.PausedNodeID
		r.PausePoint = other.PausePoint
	}
	if other.FailedNodeID != "" {
		r.FailedNodeID = other.FailedNodeID
		r.FailedPausePoint = other.FailedPausePoint
	}
	if other.ResumedFrom != "" {
		r.ResumedFrom = other.ResumedFrom
	}
}
