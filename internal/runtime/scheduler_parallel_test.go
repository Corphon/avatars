package runtime

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"avatars/internal/planner"
)

// TestSchedulerParallelism verifies that the scheduler runs independent
// nodes concurrently — total wall-clock time should be less than the
// sum of individual node times. A6.
func TestSchedulerParallelism(t *testing.T) {
	state := NewWorkflowState(planner.Plan{})
	state.AddNode(WorkflowNodeRuntime{ID: "a", Status: WorkflowNodeStatusPending})
	state.AddNode(WorkflowNodeRuntime{ID: "b", Status: WorkflowNodeStatusPending})
	state.AddNode(WorkflowNodeRuntime{ID: "c", Status: WorkflowNodeStatusPending})
	state.AddNode(WorkflowNodeRuntime{ID: "d", Status: WorkflowNodeStatusPending})
	state.AddNode(WorkflowNodeRuntime{ID: "e", Status: WorkflowNodeStatusPending})

	nodeTimes := map[string]time.Duration{
		"a": 100 * time.Millisecond,
		"b": 100 * time.Millisecond,
		"c": 100 * time.Millisecond,
		"d": 50 * time.Millisecond,
		"e": 50 * time.Millisecond,
	}

	executor := NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		d, ok := nodeTimes[node.ID]
		if !ok {
			d = 50 * time.Millisecond
		}
		time.Sleep(d)
		return SchedulerNodeResult{ArtifactIDs: []string{node.ID}}, nil
	})

	s := NewScheduler(state)
	s.MaxParallel = 5 // allow all to run concurrently

	start := time.Now()
	result, err := s.RunReadyNodes(executor)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunReadyNodes: %v", err)
	}
	if len(result.CompletedNodeIDs) != 5 {
		t.Fatalf("expected 5 completed nodes, got %d: %v", len(result.CompletedNodeIDs), result.CompletedNodeIDs)
	}

	// Serial time would be ~400ms (100+100+100+50+50).
	// With 5x concurrency, should be < 200ms (< 50% of serial).
	serialTime := 400 * time.Millisecond
	if elapsed > serialTime/2 {
		t.Errorf("parallel execution too slow: %v (expected < %v)", elapsed, serialTime/2)
	}
	t.Logf("5 nodes parallel: %v (serial would be ~%v)", elapsed, serialTime)
}

// TestSchedulerConcurrencyCap verifies the semaphore limits concurrent goroutines.
func TestSchedulerConcurrencyCap(t *testing.T) {
	state := NewWorkflowState(planner.Plan{})
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("node-%d", i)
		state.AddNode(WorkflowNodeRuntime{ID: id, Status: WorkflowNodeStatusPending})
	}

	var maxConcurrent int
	var current int
	mu := newSyncMutex()

	executor := NodeExecutorFunc(func(node WorkflowNodeRuntime) (SchedulerNodeResult, error) {
		mu.Lock()
		current++
		if current > maxConcurrent {
			maxConcurrent = current
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		current--
		mu.Unlock()
		return SchedulerNodeResult{}, nil
	})

	s := NewScheduler(state)
	s.MaxParallel = 3 // cap at 3

	_, err := s.RunReadyNodes(executor)
	if err != nil {
		t.Fatalf("RunReadyNodes: %v", err)
	}

	if maxConcurrent > 3 {
		t.Errorf("max concurrent goroutines %d exceeds cap of 3", maxConcurrent)
	}
	if maxConcurrent < 2 {
		t.Errorf("expected at least 2 concurrent goroutines, got %d — semaphore may be too strict", maxConcurrent)
	}
	t.Logf("Max concurrent goroutines: %d (cap: 3)", maxConcurrent)
}

type syncMutex struct {
	ch chan struct{}
}

func newSyncMutex() *syncMutex {
	return &syncMutex{ch: make(chan struct{}, 1)}
}

func (m *syncMutex) Lock()   { m.ch <- struct{}{} }
func (m *syncMutex) Unlock() { <-m.ch }

func init() { _ = strings.Contains }
