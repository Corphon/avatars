package workflow

import (
	"fmt"
	"strings"
)

// PhaseBuilderNode describes one workflow-phase-driven Builder node.
type PhaseBuilderNode struct {
	ID        string
	Title     string
	Phase     int    // 1-based phase number
	Goal      string
	File      string // detail file path
	DependsOn string // node ID this phase depends on (N1: wired up, was dead code)
}

// BuildPhaseNodes reads the workflow plan and returns one node per phase.
// Returns nil if the plan doesn't exist or has no phases.
//
// S6.7: Not on the live DAG path. Runtime uses a single Builder +
// InjectPhasePrompt / injectPhaseContextIntoPlan for the active phase.
// Keep this helper for ValidatePhaseDAG tests and optional future
// phase-per-node planners — do not wire a second DAG construction path.
func BuildPhaseNodes(root string, reviewNodeID string) []PhaseBuilderNode {
	planContent, err := ReadWorkflowDoc(root, "plan")
	if err != nil {
		return nil
	}

	phases := extractPhaseInfos(planContent)
	if len(phases) == 0 {
		return nil
	}

	var nodes []PhaseBuilderNode
	dependsOn := reviewNodeID

	for i, ph := range phases {
		phaseNum := i + 1
		nodeID := fmt.Sprintf("node-phase-%d", phaseNum)
		title := fmt.Sprintf("Phase %d: %s", phaseNum, trunc(ph.Name, 50))
		nodes = append(nodes, PhaseBuilderNode{
			ID:        nodeID,
			Title:     title,
			Phase:     phaseNum,
			Goal:      ph.Goal,
			File:      ph.File,
			DependsOn: dependsOn, // N1: phase N+1 depends on phase N (phase 1 depends on reviewNodeID)
		})
		dependsOn = nodeID // next phase depends on this one
	}
	return nodes
}

// InjectPhasePrompt lives in phase_inject.go (R4-10): injects phaseN.md,
// prior-phase context, disk inventory, and strips future-phase sections.

// phaseInfo is a parsed phase from the plan markdown.
type phaseInfo struct {
	Name string
	Goal string
	File string
}

func extractPhaseInfos(content string) []phaseInfo {
	var phases []phaseInfo
	lines := strings.Split(content, "\n")
	var current *phaseInfo

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "### Phase ") {
			if current != nil {
				phases = append(phases, *current)
			}
			current = &phaseInfo{}
			rest := strings.TrimPrefix(trimmed, "### ")
			if idx := strings.Index(rest, ": "); idx > 0 {
				current.Name = strings.TrimSpace(rest[idx+2:])
			} else {
				current.Name = rest
			}
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(trimmed, "- **Goal**:") {
			current.Goal = strings.TrimSpace(strings.TrimPrefix(trimmed, "- **Goal**:"))
		}
		if strings.HasPrefix(trimmed, "- **Detail**:") {
			current.File = strings.TrimSpace(strings.TrimPrefix(trimmed, "- **Detail**:"))
		}
	}

	if current != nil {
		phases = append(phases, *current)
	}
	return phases
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// ValidatePhaseDAG checks a list of PhaseBuilderNodes for dependency integrity:
//   - Every DependsOn target must exist in the node list (no dangling references)
//   - The dependency graph must be acyclic (no circular dependencies)
//
// N1: This was dead code (dependsOn was assigned but explicitly ignored with _).
// Now wired up — call this from ConstructAllPhases to catch malformed phase plans
// before they cause silent ordering bugs at runtime.
func ValidatePhaseDAG(nodes []PhaseBuilderNode) error {
	if len(nodes) == 0 {
		return nil
	}

	nodeIDs := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeIDs[n.ID] = true
	}

	// Check 1: Every DependsOn must reference a real node.
	for _, n := range nodes {
		if n.DependsOn == "" {
			continue // first node has no dependency — OK
		}
		if !nodeIDs[n.DependsOn] {
			return fmt.Errorf("phase %q depends on %q which does not exist in the plan", n.ID, n.DependsOn)
		}
	}

	// Check 2: No cycles (DFS-based).
	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	var dfs func(nodeID string) error
	dfs = func(nodeID string) error {
		if inStack[nodeID] {
			return fmt.Errorf("circular dependency detected at phase %s", nodeID)
		}
		if visited[nodeID] {
			return nil
		}
		visited[nodeID] = true
		inStack[nodeID] = true
		defer func() { inStack[nodeID] = false }()

		for _, n := range nodes {
			if n.ID == nodeID && n.DependsOn != "" {
				if err := dfs(n.DependsOn); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for _, n := range nodes {
		if err := dfs(n.ID); err != nil {
			return err
		}
	}

	return nil
}
